package proxy

import (
	"bytes"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	fib "github.com/lesismal/fib"
	"github.com/lesismal/fib/hpack"
	fibhttp "github.com/lesismal/fib/http"
	"github.com/lesismal/fib/http3"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/certtest"
	"github.com/lesismal/fit/listener"
	"github.com/lesismal/fit/runtime"
	"github.com/lesismal/fit/upstream"
)

// startFitExt serves a WebSocket backend at backend over a TLS listener
// with HTTP/1.1, h2 and h3, and an h2c one, and returns their addresses.
func startFitExt(t *testing.T, backend string, cert *certtest.Cert) (tlsAddr listener.Addr, h2cAddr string) {
	t.Helper()
	doc := fmt.Sprintf(`{
  "listeners": [
    {"name": "tls", "address": "127.0.0.1:0", "protocols": ["http/1.1", "h2", "h3"],
      "tls": {"certificates": [{"cert_file": %q, "key_file": %q}]}},
    {"name": "h2c", "address": "127.0.0.1:0", "protocols": ["h2c"]}
  ],
  "upstreams": [{"name": "app", "addresses": [%q]}],
  "routes": [{"handler": {"module": "proxy", "params": {"upstream": "app"}}}]
}`, cert.CertFile, cert.KeyFile, backend)
	c, err := config.Load([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	engine, stop, err := upstream.StartEngine()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	g, err := runtime.Compile(c, runtime.Env{Engine: engine})
	if err != nil {
		t.Fatal(err)
	}
	rt := new(runtime.Runtime)
	rt.Swap(g)
	s, err := listener.Start(rt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = s.Close()
		rt.Close()
	})
	addrs := s.Addrs()
	return addrs[0], addrs[1].Addr.String()
}

// extConnect opens a WebSocket by extended CONNECT with do, which sends the
// request, and returns the writer of what goes to it and a reader of what
// comes back.
func extConnect(t *testing.T, target string, do func(*stdhttp.Request) (int, stdhttp.Header, io.Reader, error)) (io.WriteCloser, io.Reader) {
	t.Helper()
	pr, pw := io.Pipe()
	t.Cleanup(func() { pw.Close() })
	req, err := stdhttp.NewRequest(stdhttp.MethodConnect, target, pr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(":protocol", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Protocol", "chat")
	status, header, body, err := do(req)
	if err != nil {
		t.Fatal(err)
	}
	if status != stdhttp.StatusOK || header.Get("Sec-WebSocket-Protocol") != "chat" || header.Get("Sec-WebSocket-Accept") != "" {
		t.Fatalf("status %d header %v", status, header)
	}
	return pw, body
}

// echoes checks that what goes through w comes back on r: a message, then
// a megabyte.
func echoes(t *testing.T, w io.Writer, r io.Reader) {
	t.Helper()
	for _, msg := range [][]byte{[]byte("hello"), bytes.Repeat([]byte("0123456789abcdef"), 1<<16)} {
		go func() { _, _ = w.Write(msg) }()
		got := make([]byte, len(msg))
		if _, err := io.ReadFull(r, got); err != nil || !bytes.Equal(got, msg) {
			t.Fatalf("echo of %d bytes: %v", len(msg), err)
		}
	}
}

// TestWebSocketExtendedConnect opens WebSockets over h2, h2c and h3 entries,
// each an extended CONNECT that goes upstream as an HTTP/1.1 handshake.
func TestWebSocketExtendedConnect(t *testing.T) {
	b := newWSBackend(t)
	cert, err := certtest.Write(t.TempDir(), "a.example", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	tlsAddr, h2cAddr := startFitExt(t, b.addr, cert)
	t.Run("h2", func(t *testing.T) {
		conn, err := tls.Dial("tcp", tlsAddr.Addr.String(), &tls.Config{RootCAs: certtest.Pool(cert),
			ServerName: "a.example", NextProtos: []string{"h2"}})
		if err != nil {
			t.Fatal(err)
		}
		w, r := extConnect(t, "https://a.example/chat", h2Connect(t, conn))
		echoes(t, w, r)
		h := <-b.headers
		if h.Get("Sec-WebSocket-Key") == "" || !strings.EqualFold(h.Get("Upgrade"), "websocket") || h.Get("Sec-WebSocket-Version") != "13" {
			t.Errorf("the upstream's handshake: %v", h)
		}
	})
	t.Run("h2c", func(t *testing.T) {
		conn, err := net.DialTimeout("tcp", h2cAddr, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		w, r := extConnect(t, "http://x/chat", h2Connect(t, conn))
		echoes(t, w, r)
		<-b.headers
	})
	t.Run("h3", func(t *testing.T) {
		engine, err := fib.New(fib.DefaultConfig())
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- engine.Run() }()
		t.Cleanup(func() {
			engine.Stop()
			<-done
			_ = engine.Close()
		})
		cfg := http3.DefaultClientConfig()
		cfg.TLSConfig = &tls.Config{RootCAs: certtest.Pool(cert), ServerName: "a.example"}
		cfg.StreamResponseBody = true
		cfg.StreamUnknownLengthAtHeader = true
		client := http3.NewClient(engine, cfg)
		t.Cleanup(client.Close)
		port := strconv.Itoa(tlsAddr.UDPAddr.(*net.UDPAddr).Port)
		w, r := extConnect(t, "https://127.0.0.1:"+port+"/chat", func(req *stdhttp.Request) (int, stdhttp.Header, io.Reader, error) {
			type answer struct {
				resp *fibhttp.ClientResponse
				err  error
			}
			answered := make(chan answer, 1)
			br, bw := io.Pipe()
			t.Cleanup(func() { bw.Close() })
			client.Do(req, func(resp *fibhttp.ClientResponse, err error) {
				if err == nil {
					resp.OnBody(func(data []byte, fin bool, err error) {
						_, _ = bw.Write(data)
						if fin || err != nil {
							bw.Close()
						}
					})
				}
				answered <- answer{resp, err}
			})
			select {
			case a := <-answered:
				if a.err != nil {
					return 0, nil, nil, a.err
				}
				return a.resp.StatusCode, a.resp.Header, br, nil
			case <-time.After(10 * time.Second):
				return 0, nil, nil, fmt.Errorf("no answer")
			}
		})
		echoes(t, w, r)
		<-b.headers
	})
}

// h2Connect sends an extended CONNECT over conn as a bare HTTP/2 client:
// net/http's Transport refuses a :protocol field, and x/net's would be a
// dependency for a test. The request's body goes out in DATA frames as it
// is read, within the windows the server gives it, as a client's must: one
// sent past them has its stream reset with FLOW_CONTROL_ERROR, as fib's
// server does once a tunnel's reads are held and the window stops coming
// back. The response's DATA comes back through the reader returned, the
// window given back as it arrives.
func h2Connect(t *testing.T, conn net.Conn) func(*stdhttp.Request) (int, stdhttp.Header, io.Reader, error) {
	return func(req *stdhttp.Request) (int, stdhttp.Header, io.Reader, error) {
		t.Cleanup(func() { conn.Close() })
		var mu sync.Mutex
		write := func(typ, flags byte, stream uint32, payload []byte) {
			mu.Lock()
			defer mu.Unlock()
			head := []byte{byte(len(payload) >> 16), byte(len(payload) >> 8), byte(len(payload)), typ, flags,
				byte(stream >> 24), byte(stream >> 16), byte(stream >> 8), byte(stream)}
			_, _ = conn.Write(append(head, payload...))
		}
		_, _ = io.WriteString(conn, "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n")
		write(0x4, 0, 0, nil)                            // SETTINGS
		write(0x8, 0, 0, []byte{0x00, 0xff, 0x00, 0x00}) // the connection's window, larger
		enc := hpack.NewEncoder()
		block := enc.Begin(nil)
		for _, f := range [][2]string{{":method", "CONNECT"}, {":protocol", "websocket"}, {":scheme", req.URL.Scheme},
			{":path", req.URL.RequestURI()}, {":authority", req.URL.Host}} {
			block = enc.AppendField(block, f[0], f[1], false)
		}
		for k, vv := range req.Header {
			if !strings.HasPrefix(k, ":") {
				for _, v := range vv {
					block = enc.AppendField(block, strings.ToLower(k), v, false)
				}
			}
		}
		write(0x1, 0x4, 1, block) // HEADERS, END_HEADERS
		// The server's windows for what this side sends: 65535 bytes each
		// until its SETTINGS and WINDOW_UPDATEs say otherwise (RFC 9113
		// section 6.9). gone says the connection has ended.
		flow := sync.NewCond(new(sync.Mutex))
		connWindow, streamWindow, initialWindow, gone := int64(65535), int64(65535), int64(65535), false
		go func() {
			buf := make([]byte, 16<<10)
			for {
				n, err := req.Body.Read(buf)
				for p := buf[:n]; len(p) > 0; {
					flow.L.Lock()
					for (connWindow <= 0 || streamWindow <= 0) && !gone {
						flow.Wait()
					}
					if gone {
						flow.L.Unlock()
						return
					}
					m := min(int64(len(p)), connWindow, streamWindow)
					connWindow -= m
					streamWindow -= m
					flow.L.Unlock()
					write(0x0, 0, 1, p[:m])
					p = p[m:]
				}
				if err != nil {
					return
				}
			}
		}()
		type head struct {
			status int
			header stdhttp.Header
		}
		heads := make(chan head, 1)
		pr, pw := io.Pipe()
		go func() {
			defer pw.Close()
			defer func() {
				flow.L.Lock()
				gone = true
				flow.Broadcast()
				flow.L.Unlock()
			}()
			dec := hpack.NewDecoder(4096)
			hdr := make([]byte, 9)
			for {
				if _, err := io.ReadFull(conn, hdr); err != nil {
					return
				}
				payload := make([]byte, int(hdr[0])<<16|int(hdr[1])<<8|int(hdr[2]))
				if _, err := io.ReadFull(conn, payload); err != nil {
					return
				}
				typ, flags, stream := hdr[3], hdr[4], binary.BigEndian.Uint32(hdr[5:])&0x7fffffff
				switch {
				case typ == 0x4 && flags&0x1 == 0:
					flow.L.Lock()
					for i := 0; i+6 <= len(payload); i += 6 {
						if binary.BigEndian.Uint16(payload[i:]) == 0x4 { // SETTINGS_INITIAL_WINDOW_SIZE
							v := int64(binary.BigEndian.Uint32(payload[i+2:]))
							streamWindow += v - initialWindow
							initialWindow = v
						}
					}
					flow.Broadcast()
					flow.L.Unlock()
					write(0x4, 0x1, 0, nil) // SETTINGS ACK
				case typ == 0x8 && len(payload) == 4 && (stream == 0 || stream == 1):
					flow.L.Lock()
					inc := int64(binary.BigEndian.Uint32(payload) & 0x7fffffff)
					if stream == 0 {
						connWindow += inc
					} else {
						streamWindow += inc
					}
					flow.Broadcast()
					flow.L.Unlock()
				case typ == 0x1 && stream == 1:
					h := head{header: stdhttp.Header{}}
					_ = dec.Decode(payload, func(f hpack.HeaderField) error {
						if f.Name == ":status" {
							h.status, _ = strconv.Atoi(f.Value)
						} else {
							h.header.Add(f.Name, f.Value)
						}
						return nil
					})
					heads <- h
				case typ == 0x0 && stream == 1:
					if len(payload) > 0 {
						n := uint32(len(payload))
						inc := []byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}
						write(0x8, 0, 0, inc)
						write(0x8, 0, 1, inc)
						if _, err := pw.Write(payload); err != nil {
							return
						}
					}
				case typ == 0x3 && stream == 1, typ == 0x7:
					return // RST_STREAM, GOAWAY
				}
			}
		}()
		select {
		case h := <-heads:
			return h.status, h.header, pr, nil
		case <-time.After(10 * time.Second):
			return 0, nil, nil, fmt.Errorf("no answer")
		}
	}
}
