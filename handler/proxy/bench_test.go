package proxy

import (
	"bufio"
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// BenchmarkProxy sends small GETs through the proxy to an HTTP/1.1 backend,
// over keep-alive connections, one a goroutine, as fitbench's proxy
// scenario does. The client is a raw one, so that what it allocates is
// little next to what fit does.
func BenchmarkProxy(b *testing.B) {
	backend := serveBackend(b, "http/1.1", stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "hello, world")
	}))
	base := startFit(b, `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0", "protocols": ["http/1.1"]}],
  "upstreams": [{"name": "app", "addresses": [%s], "max_conns": 4096}],
  "routes": [{"handler": {"module": "proxy", "params": {"upstream": "app"}}}]
}`, backend)
	addr := strings.TrimPrefix(base, "http://")
	req := []byte("GET /proxy HTTP/1.1\r\nHost: bench\r\nUser-Agent: bench\r\n\r\n")
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			b.Error(err)
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		for pb.Next() {
			if _, err := conn.Write(req); err != nil {
				b.Error(err)
				return
			}
			if err := readResponse(r); err != nil {
				b.Error(err)
				return
			}
		}
	})
}

// readResponse reads one response with a Content-Length off r.
func readResponse(r *bufio.Reader) error {
	n := -1
	status := ""
	for {
		line, err := r.ReadSlice('\n')
		if err != nil {
			return err
		}
		if status == "" {
			status = string(line[9:12])
		}
		if len(line) <= 2 {
			break
		}
		if len(line) > 16 && strings.EqualFold(string(line[:15]), "content-length:") {
			n, _ = strconv.Atoi(strings.TrimSpace(string(line[15:])))
		}
	}
	if status != "200" || n < 0 {
		return fmt.Errorf("status %s, length %d", status, n)
	}
	_, err := r.Discard(n)
	return err
}

// BenchmarkProxyDownload streams a 64MB body through the proxy from an
// HTTP/1.1 backend, over HTTP/1.1 and h2c, as fast as the client reads:
// the download path, which writes each piece as it arrives.
func BenchmarkProxyDownload(b *testing.B) {
	const size = 64 << 20
	chunk := make([]byte, 256<<10)
	backend := serveBackend(b, "http/1.1", stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(size))
		for sent := 0; sent < size; sent += len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	base := startFit(b, config1(`{"upstream": "app"}`), backend)
	for _, h2c := range []bool{false, true} {
		name := "http/1.1"
		if h2c {
			name = "h2c"
		}
		b.Run(name, func(b *testing.B) {
			c := client(h2c)
			c.Timeout = time.Minute
			b.SetBytes(size)
			for b.Loop() {
				resp, err := c.Get(base + "/big")
				if err != nil {
					b.Fatal(err)
				}
				n, err := io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if err != nil || n != size {
					b.Fatalf("%d bytes: %v", n, err)
				}
			}
		})
	}
}
