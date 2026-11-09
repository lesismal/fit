package synctidb

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	fitsync "github.com/lesismal/fit/sync"
	synckafka "github.com/lesismal/fit/sync/kafka"
	mysqlwire "github.com/lesismal/fit/sync/mysql/wire"
)

// miniServer is a MySQL server on loopback that logs a client in with
// mysql_native_password, answers SELECT 'row' with two text rows and every
// other command OK; it closes when the test ends.
func miniServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		conns []net.Conn
	)
	// The connections still open are closed too, so that a server
	// goroutine reading one never holds the test's end.
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		for _, nc := range conns {
			nc.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, nc)
			mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer nc.Close()
				serveMini(nc)
			}()
		}
	}()
	return ln.Addr().String()
}

func serveMini(nc net.Conn) {
	br := bufio.NewReader(nc)
	write := func(seq byte, p []byte) {
		h := []byte{byte(len(p)), byte(len(p) >> 8), byte(len(p) >> 16), seq}
		_, _ = nc.Write(append(h, p...))
	}
	read := func() ([]byte, bool) {
		var h [4]byte
		if _, err := io.ReadFull(br, h[:]); err != nil {
			return nil, false
		}
		b := make([]byte, int(h[0])|int(h[1])<<8|int(h[2])<<16)
		_, err := io.ReadFull(br, b)
		return b, err == nil
	}
	const caps = 1<<0 | 1<<2 | 1<<9 | 1<<13 | 1<<15 | 1<<17 | 1<<18 | 1<<19 | 1<<21
	scramble := []byte("abcdefghij0123456789")
	g := []byte{10}
	g = append(g, "8.0.11-TiDB-v8.5.0"...)
	g = append(g, 0)
	g = binary.LittleEndian.AppendUint32(g, 7)
	g = append(g, scramble[:8]...)
	g = append(g, 0)
	g = binary.LittleEndian.AppendUint16(g, uint16(caps&0xffff))
	g = append(g, 45)
	g = binary.LittleEndian.AppendUint16(g, 2)
	g = binary.LittleEndian.AppendUint16(g, uint16(caps>>16))
	g = append(g, 21)
	g = append(g, make([]byte, 10)...)
	g = append(g, scramble[8:]...)
	g = append(g, 0)
	g = append(g, "mysql_native_password"...)
	g = append(g, 0)
	write(0, g)
	if _, ok := read(); !ok {
		return
	}
	ok := []byte{0, 0, 0, 2, 0, 0, 0}
	write(2, ok)
	for {
		p, alive := read()
		if !alive || len(p) == 0 || p[0] == 1 { // COM_QUIT
			return
		}
		if p[0] == 3 && string(p[1:]) == "SELECT 'row'" {
			lenenc := func(b []byte, v string) []byte { return append(append(b, byte(len(v))), v...) }
			col := lenenc(lenenc(lenenc(lenenc(lenenc(lenenc(nil, "def"), ""), ""), ""), "c"), "")
			col = append(col, 0x0c, 45, 0, 64, 0, 0, 0, 0xfd, 0, 0, 0, 0, 0)
			eof := []byte{0xfe, 0, 0, 2, 0}
			write(1, []byte{1})
			write(2, col)
			write(3, eof)
			write(4, lenenc(nil, "row"))
			write(5, lenenc(nil, "two"))
			write(6, eof)
			continue
		}
		write(1, ok)
	}
}

// TestWireDB goes through the real client: a query's rows, and the errors
// of a closed connection and of nothing listening.
func TestWireDB(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute) // a hang guard
	defer cancel()
	c, err := dialWire(ctx, mysqlwire.Config{Addr: miniServer(t), User: "u"})
	if err != nil {
		t.Fatal(err)
	}
	rs, err := queryAll(ctx, c, "SELECT 'row'")
	if err != nil || len(rs) != 2 || str(rs[0][0]) != "row" || str(rs[1][0]) != "two" {
		t.Fatal(rs, err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Query(ctx, "SELECT 'row'"); !errors.Is(err, mysqlwire.ErrClosed) {
		t.Fatal(err)
	}
	if _, err := dialWire(ctx, mysqlwire.Config{Addr: "127.0.0.1:1", User: "u"}); err == nil {
		t.Fatal("no error")
	}
}

// TestLabels: canal-json's ENUM and SET numbers become the snapshot's
// labels; what is not a number, or not of such a column, is left as it is,
// and the simple protocol, which sends labels, is never changed.
func TestLabels(t *testing.T) {
	s := &Source{schemas: map[string]*table{"d.t": {db: "d", name: "t", cols: []column{
		{name: "id", typ: synckafka.ParseMySQLType("bigint")},
		{name: "e", typ: synckafka.ParseMySQLType("enum('a','b')"), elements: []string{"a", "b"}},
		{name: "s", typ: synckafka.ParseMySQLType("set('x','y')"), elements: []string{"x", "y"}},
	}}}}
	m := func(fs ...fitsync.Field) fitsync.Value { return fitsync.Map(fs...) }
	f := func(n, v string) fitsync.Field { return fitsync.Field{Name: n, Value: fitsync.String(v)} }
	rec := &fitsync.Record{Namespace: "d.t", After: m(f("id", "1"), f("e", "2"), f("s", "3")),
		Before: m(f("id", "1"), f("e", "b"))}
	s.labels(rec)
	if js(t, rec.After) != `{"map":{"id":"1","e":"b","s":"x,y"}}` || js(t, rec.Before) != `{"map":{"id":"1","e":"b"}}` {
		t.Fatal(js(t, rec.After), js(t, rec.Before))
	}
	rec = &fitsync.Record{Namespace: "d.t", After: m(f("id", "1"), f("e", "9"))}
	s.labels(rec)
	if js(t, rec.After) != `{"map":{"id":"1","e":""}}` {
		t.Fatal(js(t, rec.After))
	}
	s.simple = &synckafka.TiCDCSimple{}
	rec = &fitsync.Record{Namespace: "d.t", After: m(f("e", "2"))}
	s.labels(rec)
	if js(t, rec.After) != `{"map":{"e":"2"}}` {
		t.Fatal(js(t, rec.After))
	}
}
