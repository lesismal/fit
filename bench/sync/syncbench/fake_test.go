package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDB is a database in memory, as an execer: it counts the rows the
// INSERTs it is given hold, and fails a statement holding fail.
type fakeDB struct {
	mu     sync.Mutex
	rows   int64
	stmts  []string
	fail   string
	closed int
}

func (f *fakeDB) exec(_ context.Context, sql string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != "" && strings.Contains(sql, f.fail) {
		return errors.New("fake: " + f.fail)
	}
	f.stmts = append(f.stmts, sql)
	f.rows += int64(strings.Count(sql, "VALUES (") + strings.Count(sql, "),("))
	return nil
}

func (f *fakeDB) count(_ context.Context, sql string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != "" && strings.Contains(sql, f.fail) {
		return 0, errors.New("fake: " + f.fail)
	}
	return f.rows, nil
}

func (f *fakeDB) close() {
	f.mu.Lock()
	f.closed++
	f.mu.Unlock()
}

func (f *fakeDB) n() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rows
}

// useDB has mysqlDial and pgDial give db for the test.
func useDB(t *testing.T, db *fakeDB) {
	t.Helper()
	m, p := mysqlDial, pgDial
	mysqlDial = func(context.Context, string, string, string) (execer, error) { return db, nil }
	pgDial = func(context.Context, string, string, string, string) (execer, error) { return db, nil }
	t.Cleanup(func() { mysqlDial, pgDial = m, p })
}

// listen serves each connection to a fresh listener with serve, until
// the test ends.
func listen(t *testing.T, serve func(net.Conn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var conns []net.Conn
	go func() {
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
				serve(nc)
			}()
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		for _, c := range conns {
			c.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	return ln.Addr().String()
}

// fakeMySQL is a MySQL server of the text protocol: it greets, takes any
// password, answers SELECT COUNT with count's answer, a query holding FAIL
// with an error, and any other query with OK.
func fakeMySQL(t *testing.T, count func() int64) string {
	return listen(t, func(nc net.Conn) {
		br := bufio.NewReader(nc)
		var seq byte
		write := func(p []byte) {
			h := []byte{byte(len(p)), byte(len(p) >> 8), byte(len(p) >> 16), seq}
			seq++
			nc.Write(append(h, p...))
		}
		read := func() []byte {
			var h [4]byte
			if _, err := io.ReadFull(br, h[:]); err != nil {
				return nil
			}
			n := int(h[0]) | int(h[1])<<8 | int(h[2])<<16
			seq = h[3] + 1
			b := make([]byte, n)
			if _, err := io.ReadFull(br, b); err != nil {
				return nil
			}
			return b
		}
		lenenc := func(b []byte, s string) []byte { return append(append(b, byte(len(s))), s...) }
		ok := []byte{0, 0, 0, 2, 0, 0, 0}
		eof := []byte{0xfe, 0, 0, 2, 0}
		var caps uint32 = 1 | 4 | 512 | 8192 | 32768 | 1<<19 // long password and flag, 4.1, transactions, secure, plugin auth
		g := []byte{10}
		g = append(g, "8.0.0-fake"...)
		g = append(g, 0, 1, 0, 0, 0)
		g = append(g, "abcdefgh"...)
		g = append(g, 0, byte(caps&0xff), byte(caps>>8), 45, 2, 0, byte(caps>>16), byte(caps>>24), 21)
		g = append(g, make([]byte, 10)...)
		g = append(g, "ijklmnopqrst"...)
		g = append(g, 0)
		g = append(g, "mysql_native_password"...)
		g = append(g, 0)
		write(g)
		if read() == nil {
			return
		}
		write(ok)
		for {
			p := read()
			if len(p) == 0 || p[0] == 1 {
				return
			}
			q := string(p[1:])
			switch {
			case strings.Contains(q, "FAIL"):
				write(append([]byte{0xff, 0x28, 0x04, '#'}, "42000failed on purpose"...))
			case strings.HasPrefix(q, "SELECT COUNT"):
				write([]byte{1})
				col := lenenc(nil, "def")
				for _, s := range []string{"", "", "", "COUNT(*)", ""} {
					col = lenenc(col, s)
				}
				col = append(col, 0x0c, 63, 0, 21, 0, 0, 0, 8, 0x81, 0, 0, 0, 0)
				write(col)
				write(eof)
				var n string
				if count != nil {
					n = itoa(count())
				}
				write(lenenc(nil, n))
				write(eof)
			default:
				write(ok)
			}
		}
	})
}

func itoa(n int64) string {
	b := make([]byte, 0, 20)
	if n == 0 {
		return "0"
	}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// fakePG is a PostgreSQL server of the simple query protocol, trusting
// every user: SELECT count answers count's, a query holding FAIL an
// error, any other query a command tag.
func fakePG(t *testing.T, count func() int64) string {
	return listen(t, func(nc net.Conn) {
		br := bufio.NewReader(nc)
		msg := func(typ byte, body []byte) []byte {
			b := []byte{typ}
			b = binary.BigEndian.AppendUint32(b, uint32(len(body)+4))
			return append(b, body...)
		}
		var h [4]byte
		if _, err := io.ReadFull(br, h[:]); err != nil {
			return
		}
		if _, err := io.ReadFull(br, make([]byte, binary.BigEndian.Uint32(h[:])-4)); err != nil {
			return
		}
		var out []byte
		out = append(out, msg('R', []byte{0, 0, 0, 0})...)
		out = append(out, msg('K', []byte{0, 0, 0, 7, 0, 0, 0, 9})...)
		out = append(out, msg('Z', []byte{'I'})...)
		nc.Write(out)
		for {
			typ, err := br.ReadByte()
			if err != nil || typ == 'X' {
				return
			}
			if _, err := io.ReadFull(br, h[:]); err != nil {
				return
			}
			body := make([]byte, binary.BigEndian.Uint32(h[:])-4)
			if _, err := io.ReadFull(br, body); err != nil {
				return
			}
			q := string(body)
			out = out[:0]
			switch {
			case strings.Contains(q, "FAIL"):
				out = append(out, msg('E', []byte("SERROR\x00C42000\x00Mfailed on purpose\x00\x00"))...)
			case strings.HasPrefix(q, "SELECT count"):
				f := []byte{0, 1}
				f = append(f, "count\x00"...)
				f = append(f, 0, 0, 0, 0, 0, 0, 0, 0, 0, 20, 0, 8, 0xff, 0xff, 0xff, 0xff, 0, 0)
				out = append(out, msg('T', f)...)
				v := itoa(count())
				d := []byte{0, 1}
				d = binary.BigEndian.AppendUint32(d, uint32(len(v)))
				out = append(out, msg('D', append(d, v...))...)
				out = append(out, msg('C', []byte("SELECT 1\x00"))...)
			case strings.HasPrefix(q, "SELECT none"):
				out = append(out, msg('C', []byte("SELECT 0\x00"))...)
			default:
				out = append(out, msg('C', []byte("OK\x00"))...)
			}
			out = append(out, msg('Z', []byte{'I'})...)
			nc.Write(out)
		}
	})
}

// fakeKafka is one Kafka broker of Metadata v1, ListOffsets v1 and Fetch
// v4, holding the topics of topics, a partition each, a message a value.
type fakeKafka struct {
	mu     sync.Mutex
	topics map[string][][]byte
	addr   string
}

func (k *fakeKafka) add(topic string, values ...string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, v := range values {
		k.topics[topic] = append(k.topics[topic], []byte(v))
	}
}

func newFakeKafka(t *testing.T) *fakeKafka {
	k := &fakeKafka{topics: map[string][][]byte{}}
	k.addr = listen(t, func(nc net.Conn) {
		br := bufio.NewReader(nc)
		for {
			var h [4]byte
			if _, err := io.ReadFull(br, h[:]); err != nil {
				return
			}
			req := make([]byte, binary.BigEndian.Uint32(h[:]))
			if _, err := io.ReadFull(br, req); err != nil {
				return
			}
			api := int16(binary.BigEndian.Uint16(req))
			corr := req[4:8]
			cl := int(int16(binary.BigEndian.Uint16(req[8:])))
			body := req[10+max(cl, 0):]
			resp := k.answer(api, body)
			out := binary.BigEndian.AppendUint32(nil, uint32(len(resp)+4))
			out = append(append(out, corr...), resp...)
			if _, err := nc.Write(out); err != nil {
				return
			}
		}
	})
	return k
}

type kbuf struct{ b []byte }

func (w *kbuf) i16(v int16)  { w.b = binary.BigEndian.AppendUint16(w.b, uint16(v)) }
func (w *kbuf) i32(v int32)  { w.b = binary.BigEndian.AppendUint32(w.b, uint32(v)) }
func (w *kbuf) i64(v int64)  { w.b = binary.BigEndian.AppendUint64(w.b, uint64(v)) }
func (w *kbuf) str(s string) { w.i16(int16(len(s))); w.b = append(w.b, s...) }

type kread struct{ b []byte }

func (r *kread) i16() int16 { v := int16(binary.BigEndian.Uint16(r.b)); r.b = r.b[2:]; return v }
func (r *kread) i32() int32 { v := int32(binary.BigEndian.Uint32(r.b)); r.b = r.b[4:]; return v }
func (r *kread) i64() int64 { v := int64(binary.BigEndian.Uint64(r.b)); r.b = r.b[8:]; return v }
func (r *kread) i8() byte   { v := r.b[0]; r.b = r.b[1:]; return v }
func (r *kread) str() string {
	n := int(r.i16())
	s := string(r.b[:n])
	r.b = r.b[n:]
	return s
}

var castagnoliTable = crc32.MakeTable(crc32.Castagnoli)

// batch is a record batch of one record of value at offset.
func batch(offset int64, value []byte) []byte {
	var rec []byte
	rec = append(rec, 0)              // attributes
	rec = binary.AppendVarint(rec, 0) // timestamp delta
	rec = binary.AppendVarint(rec, 0) // offset delta
	rec = binary.AppendVarint(rec, -1)
	rec = binary.AppendVarint(rec, int64(len(value)))
	rec = append(rec, value...)
	rec = binary.AppendVarint(rec, 0)
	records := binary.AppendVarint(nil, int64(len(rec)))
	records = append(records, rec...)

	w := &kbuf{}
	w.i64(offset)
	w.i32(0) // length, below
	w.i32(0)
	w.b = append(w.b, 2)
	w.i32(0) // CRC, below
	w.i16(0)
	w.i32(0)
	now := time.Now().UnixMilli()
	w.i64(now)
	w.i64(now)
	w.i64(-1)
	w.i16(-1)
	w.i32(-1)
	w.i32(1)
	w.b = append(w.b, records...)
	binary.BigEndian.PutUint32(w.b[8:], uint32(len(w.b)-12))
	binary.BigEndian.PutUint32(w.b[17:], crc32.Checksum(w.b[21:], castagnoliTable))
	return w.b
}

func (k *fakeKafka) answer(api int16, body []byte) []byte {
	k.mu.Lock()
	defer k.mu.Unlock()
	w := &kbuf{}
	r := &kread{b: body}
	switch api {
	case 18: // ApiVersions
		w.i16(0)
		w.i32(3)
		for _, a := range [][3]int16{{1, 4, 4}, {2, 1, 1}, {3, 1, 1}} {
			w.i16(a[0])
			w.i16(a[1])
			w.i16(a[2])
		}
	case 3: // Metadata
		var names []string
		for range r.i32() {
			names = append(names, r.str())
		}
		host, port, _ := net.SplitHostPort(k.addr)
		w.i32(1)
		w.i32(1)
		w.str(host)
		p := 0
		for _, c := range port {
			p = p*10 + int(c-'0')
		}
		w.i32(int32(p))
		w.i16(-1)
		w.i32(1)
		w.i32(int32(len(names)))
		for _, n := range names {
			if _, ok := k.topics[n]; !ok {
				w.i16(3)
				w.str(n)
				w.b = append(w.b, 0)
				w.i32(0)
				continue
			}
			w.i16(0)
			w.str(n)
			w.b = append(w.b, 0)
			w.i32(1)
			w.i16(0)
			w.i32(0)
			w.i32(1)
			w.i32(1)
			w.i32(1)
			w.i32(1)
			w.i32(1)
		}
	case 2: // ListOffsets
		r.i32()
		nt := r.i32()
		w.i32(nt)
		for range nt {
			name := r.str()
			w.str(name)
			np := r.i32()
			w.i32(np)
			for range np {
				p := r.i32()
				r.i64()
				w.i32(p)
				w.i16(0)
				w.i64(-1)
				w.i64(int64(len(k.topics[name])))
			}
		}
	case 1: // Fetch
		r.i32()
		r.i32()
		r.i32()
		r.i32()
		r.i8()
		w.i32(0)
		nt := r.i32()
		w.i32(nt)
		for range nt {
			name := r.str()
			w.str(name)
			np := r.i32()
			w.i32(np)
			for range np {
				p := r.i32()
				off := r.i64()
				r.i32()
				msgs := k.topics[name]
				w.i32(p)
				w.i16(0)
				w.i64(int64(len(msgs)))
				w.i64(int64(len(msgs)))
				w.i32(-1)
				var set []byte
				// Two at most an answer, so that a count reads again.
				for o := off; o < int64(len(msgs)) && o < off+2; o++ {
					set = append(set, batch(o, msgs[o])...)
				}
				w.i32(int32(len(set)))
				w.b = append(w.b, set...)
			}
		}
	}
	return w.b
}
