package proxy

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// upgradeConfig is a config whose one backend is taken out of rotation by
// its first failure, so that a test sees any failure an answer counts, and
// whose listener speaks HTTP/1.1 alone, so that an h2c upgrade reaches the
// proxy.
func upgradeConfig(fields string) string {
	return `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0", "protocols": ["http/1.1"]}],
  "upstreams": [{"name": "app", "addresses": [%s], "max_fails": 1, "fail_timeout": "1m"` + fields + `}],
  "routes": [{"handler": {"module": "proxy", "params": {"upstream": "app"}}}]
}`
}

// upgradeBackend serves each connection's first request with serve, and
// answers a request that asks to switch to nothing with 200 "ok".
func upgradeBackend(t *testing.T, serve func(c net.Conn, req *stdhttp.Request)) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				for br := bufio.NewReader(c); ; {
					req, err := stdhttp.ReadRequest(br)
					if err != nil {
						return
					}
					if req.Header.Get("Upgrade") == "" {
						if _, err := io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"); err != nil {
							return
						}
						continue
					}
					serve(c, req)
					return
				}
			}()
		}
	}()
	return l.Addr().String()
}

// upgradeRequest sends a WebSocket handshake for path through fit at base
// and returns the connection and the response.
func upgradeRequest(t *testing.T, base, path string) (net.Conn, *bufio.Reader, *stdhttp.Response) {
	t.Helper()
	c, err := net.DialTimeout("tcp", strings.TrimPrefix(base, "http://"), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	_ = c.SetDeadline(time.Now().Add(20 * time.Second))
	if _, err := io.WriteString(c, "GET "+path+" HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n"+
		"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: MDEyMzQ1Njc4OWFiY2RlZg==\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(c)
	resp, err := stdhttp.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c, br, resp
}

// stillInRotation checks that the backend at base answers a plain request:
// with max_fails 1, one failure counted would have taken it out.
func stillInRotation(t *testing.T, base string) {
	t.Helper()
	resp, err := client(false).Get(base + "/plain")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != stdhttp.StatusOK || string(body) != "ok" {
		t.Fatalf("a plain request after: %d %q, want 200 ok", resp.StatusCode, body)
	}
}

// TestUpgradeLargeRefusal: an upstream that answers a request asking to
// switch with anything but 101 and a large body, as a backend that ignores
// Upgrade does for a large file, has it streamed back as any response is,
// not buffered whole: while the client does not read, the backend's writes
// stall at what the sockets and fit's bounded queues take, far short of the
// body; once it reads, it gets all of it, and the backend, which did
// nothing wrong, stays in rotation.
func TestUpgradeLargeRefusal(t *testing.T) {
	const piece, pieces = 64 << 10, 1024 // 64 MiB
	for _, framing := range []string{"length", "chunked", "close"} {
		t.Run(framing, func(t *testing.T) {
			var written atomic.Int64
			b := upgradeBackend(t, func(c net.Conn, _ *stdhttp.Request) {
				head := "HTTP/1.1 200 OK\r\n"
				switch framing {
				case "length":
					head += fmt.Sprintf("Content-Length: %d\r\n", piece*pieces)
				case "chunked":
					head += "Transfer-Encoding: chunked\r\n"
				case "close":
					head += "Connection: close\r\n"
				}
				if _, err := io.WriteString(c, head+"\r\n"); err != nil {
					return
				}
				data := bytes.Repeat([]byte("x"), piece)
				if framing == "chunked" {
					data = append(fmt.Appendf(nil, "%x\r\n", piece), append(data, "\r\n"...)...)
				}
				_ = c.SetWriteDeadline(time.Now().Add(30 * time.Second))
				for range pieces {
					n, err := c.Write(data)
					written.Add(int64(n))
					if err != nil {
						return
					}
				}
				if framing == "chunked" {
					_, _ = io.WriteString(c, "0\r\n\r\n")
				}
			})
			base := startFit(t, upgradeConfig(""), b)
			_, _, resp := upgradeRequest(t, base, "/big")
			defer resp.Body.Close()
			if resp.StatusCode != stdhttp.StatusOK {
				t.Fatalf("a large refusal: %d, want the upstream's 200", resp.StatusCode)
			}
			// The client reads nothing: the backend's writes stall once the
			// sockets' buffers and fit's queues are full, which hold a
			// fraction of the body.
			var stalled int64
			deadline := time.Now().Add(10 * time.Second)
			for {
				before := written.Load()
				time.Sleep(200 * time.Millisecond)
				if after := written.Load(); after == before {
					stalled = after
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("the backend's writes did not stall: %d bytes written", written.Load())
				}
			}
			if stalled >= piece*pieces/2 {
				t.Fatalf("the backend wrote %d of %d bytes to a client that read none", stalled, piece*pieces)
			}
			n, err := io.Copy(io.Discard, resp.Body)
			if err != nil || n != piece*pieces {
				t.Fatalf("read %d of %d bytes: %v", n, piece*pieces, err)
			}
			stillInRotation(t, base)
		})
	}
}

// TestUpgradeSmallRefusalSlowClient: a refusal within the threshold reaches
// a client that reads it slowly whole, its header as the upstream sent it.
func TestUpgradeSmallRefusalSlowClient(t *testing.T) {
	body := bytes.Repeat([]byte("0123456789abcdef"), (60<<10)/16)
	b := upgradeBackend(t, func(c net.Conn, _ *stdhttp.Request) {
		_, _ = fmt.Fprintf(c, "HTTP/1.1 404 Not Found\r\nContent-Length: %d\r\nX-Why: test\r\n\r\n%s", len(body), body)
	})
	base := startFit(t, upgradeConfig(""), b)
	_, _, resp := upgradeRequest(t, base, "/missing")
	defer resp.Body.Close()
	if resp.StatusCode != stdhttp.StatusNotFound || resp.Header.Get("X-Why") != "test" {
		t.Fatalf("%d %v", resp.StatusCode, resp.Header)
	}
	var got bytes.Buffer
	buf := make([]byte, 4<<10)
	for {
		time.Sleep(5 * time.Millisecond)
		n, err := resp.Body.Read(buf)
		got.Write(buf[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(got.Bytes(), body) {
		t.Fatalf("got %d bytes, want %d", got.Len(), len(body))
	}
	stillInRotation(t, base)
}

// TestUpgradeRefusalCut: a refusal the upstream cuts short gets 502 when
// it was small enough to arrive whole, and when it was streaming, the
// client's response is cut short as well, its connection closed before the
// length it announced; a client that goes while a refusal trickles in has
// the upstream's connection closed, counted against neither backend.
func TestUpgradeRefusalCut(t *testing.T) {
	t.Run("upstream streamed", func(t *testing.T) {
		b := upgradeBackend(t, func(c net.Conn, _ *stdhttp.Request) {
			_, _ = io.WriteString(c, "HTTP/1.1 404 Not Found\r\nContent-Length: 1048576\r\n\r\n"+strings.Repeat("x", 128<<10))
		})
		base := startFit(t, upgradeConfig(""), b)
		_, _, resp := upgradeRequest(t, base, "/")
		defer resp.Body.Close()
		if resp.StatusCode != stdhttp.StatusNotFound {
			t.Fatalf("a streamed refusal: %d, want the upstream's 404", resp.StatusCode)
		}
		n, err := io.Copy(io.Discard, resp.Body)
		if !errors.Is(err, io.ErrUnexpectedEOF) || n != 128<<10 {
			t.Fatalf("read %d bytes, %v: want what came and the cut", n, err)
		}
	})
	t.Run("upstream", func(t *testing.T) {
		b := upgradeBackend(t, func(c net.Conn, _ *stdhttp.Request) {
			_, _ = io.WriteString(c, "HTTP/1.1 404 Not Found\r\nContent-Length: 32768\r\n\r\n"+strings.Repeat("x", 1024))
		})
		base := startFit(t, upgradeConfig(""), b)
		_, _, resp := upgradeRequest(t, base, "/")
		resp.Body.Close()
		if resp.StatusCode != stdhttp.StatusBadGateway {
			t.Fatalf("a refusal cut short: %d, want 502", resp.StatusCode)
		}
	})
	t.Run("client", func(t *testing.T) {
		closed := make(chan struct{}, 1)
		b := upgradeBackend(t, func(c net.Conn, _ *stdhttp.Request) {
			defer func() { closed <- struct{}{} }()
			if _, err := io.WriteString(c, "HTTP/1.1 404 Not Found\r\nTransfer-Encoding: chunked\r\n\r\n"); err != nil {
				return
			}
			for range 200 {
				time.Sleep(50 * time.Millisecond)
				if _, err := io.WriteString(c, "1\r\nx\r\n"); err != nil {
					return
				}
			}
		})
		base := startFit(t, upgradeConfig(""), b)
		c, err := net.Dial("tcp", strings.TrimPrefix(base, "http://"))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(c, "GET / HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
		time.Sleep(200 * time.Millisecond)
		c.Close()
		select {
		case <-closed:
		case <-time.After(5 * time.Second):
			t.Fatal("the upstream's connection outlived the client")
		}
		stillInRotation(t, base)
	})
}

// TestUpgradeSaturated: a request that asks to switch is turned away with
// 503 at once when the pool is full, as any other is, without reaching the
// backend.
func TestUpgradeSaturated(t *testing.T) {
	var reached atomic.Int32
	release := make(chan struct{})
	b := upgradeBackend(t, func(c net.Conn, _ *stdhttp.Request) {
		reached.Add(1)
		<-release
	})
	defer close(release)
	base := startFit(t, upgradeConfig(`, "max_conns": 1, "max_pending": 1`), b)
	for range 2 {
		c, err := net.Dial("tcp", strings.TrimPrefix(base, "http://"))
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		_, _ = io.WriteString(c, "GET / HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
	}
	time.Sleep(300 * time.Millisecond)
	start := time.Now()
	_, _, resp := upgradeRequest(t, base, "/")
	resp.Body.Close()
	if resp.StatusCode != stdhttp.StatusServiceUnavailable || time.Since(start) > time.Second {
		t.Fatalf("an upgrade past max_pending: %d after %v", resp.StatusCode, time.Since(start))
	}
	if n := reached.Load(); n != 1 {
		t.Fatalf("%d handshakes reached the backend, want 1", n)
	}
}

// TestUpgradeH2C: a request asking to switch to h2c, which a listener that
// does not speak h2c leaves to its handler, is proxied as the ordinary
// request it also is, its body streamed, rather than passed upstream as a
// switch: h2c's Upgrade is the client's offer to its own hop.
func TestUpgradeH2C(t *testing.T) {
	const size = 4 << 20
	var headers atomic.Pointer[stdhttp.Header]
	b := serveBackend(t, "http/1.1", stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		headers.Store(&r.Header)
		w.Header().Set("Content-Length", fmt.Sprint(size))
		_, _ = w.Write(bytes.Repeat([]byte("y"), size))
	}))
	base := startFit(t, upgradeConfig(""), b)
	c, err := net.Dial("tcp", strings.TrimPrefix(base, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(20 * time.Second))
	_, _ = io.WriteString(c, "GET /file HTTP/1.1\r\nHost: x\r\nConnection: Upgrade, HTTP2-Settings\r\nUpgrade: h2c\r\nHTTP2-Settings: AAMAAABkAAQCAAAAAAIAAAAA\r\n\r\n")
	resp, err := stdhttp.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	n, err := io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != stdhttp.StatusOK || n != size || err != nil {
		t.Fatalf("%d, %d bytes, %v", resp.StatusCode, n, err)
	}
	if h := *headers.Load(); h.Get("Upgrade") != "" || h.Get("Http2-Settings") != "" {
		t.Fatalf("the upstream got the switch: %v", h)
	}
}
