package syncdoris

import (
	"bufio"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"testing"
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
