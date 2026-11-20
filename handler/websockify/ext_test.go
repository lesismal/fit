package websockify

import (
	"bytes"
	"crypto/rand"
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

	"github.com/lesismal/fit/internal/certtest"
	"github.com/lesismal/fit/listener"
)

// TestExtendedConnect bridges WebSockets opened by extended CONNECT over
// h2, h2c and h3 (RFC 8441, RFC 9220), framed here by hand: a small message
// and one of 1 MiB, in frames of 64 KiB.
func TestExtendedConnect(t *testing.T) {
	cert, err := certtest.Write(t.TempDir(), "a.example", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	f := startFit(t, fmt.Sprintf(`{
  "listeners": [
    {"name": "tls", "address": "127.0.0.1:0", "protocols": ["http/1.1", "h2", "h3"],
      "tls": {"certificates": [{"cert_file": %q, "key_file": %q}]}},
    {"name": "h2c", "address": "127.0.0.1:0", "protocols": ["h2c"]}
  ],
  "routes": [{"handler": {"module": "websockify", "params": {"address": %q, "name": "ext"}}}]
}`, cert.CertFile, cert.KeyFile, backend(t, echo)))
	tlsAddr, h2cAddr := f.addrs[0], f.addrs[1].Addr.String()
	t.Run("h2", func(t *testing.T) {
		conn, err := tls.Dial("tcp", tlsAddr.Addr.String(), &tls.Config{RootCAs: certtest.Pool(cert),
			ServerName: "a.example", NextProtos: []string{"h2"}})
		if err != nil {
			t.Fatal(err)
		}
		w, r := extConnect(t, "https://a.example/ws", h2Connect(t, conn))
		echoFrames(t, w, r)
	})
	t.Run("h2c", func(t *testing.T) {
		conn, err := net.DialTimeout("tcp", h2cAddr, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		w, r := extConnect(t, "http://x/ws", h2Connect(t, conn))
		echoFrames(t, w, r)
	})
	t.Run("h3", func(t *testing.T) {
		w, r := extConnect(t, "https://127.0.0.1:"+h3Port(tlsAddr)+"/ws", h3Connect(t, cert))
		echoFrames(t, w, r)
	})
	if s := f.stats("ext"); s.Bridged != 3 {
		t.Errorf("stats %+v", s)
	}
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
	req.Header.Set("Sec-WebSocket-Protocol", "binary")
	status, header, body, err := do(req)
	if err != nil {
		t.Fatal(err)
	}
	if status != stdhttp.StatusOK || header.Get("Sec-WebSocket-Protocol") != "binary" {
		t.Fatalf("status %d header %v", status, header)
	}
	return pw, body
}

// clientFrame is a masked frame of payload, as a client sends: the first
// of a binary message, or a continuation.
func clientFrame(payload []byte, first, fin bool) []byte {
	b0 := byte(0x0)
	if first {
		b0 = 0x2
	}
	if fin {
		b0 |= 0x80
	}
	f := []byte{b0}
	switch n := len(payload); {
	case n < 126:
		f = append(f, 0x80|byte(n))
	case n < 1<<16:
		f = append(f, 0x80|126, byte(n>>8), byte(n))
	default:
		f = append(f, 0x80|127)
		f = binary.BigEndian.AppendUint64(f, uint64(n))
	}
	var mask [4]byte
	_, _ = rand.Read(mask[:])
	f = append(f, mask[:]...)
	for i, c := range payload {
		f = append(f, c^mask[i%4])
	}
	return f
}

// readServerFrame reads an unmasked frame from r and returns its opcode and
// payload.
func readServerFrame(r io.Reader) (byte, []byte, error) {
	var h [2]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, nil, err
	}
	n := uint64(h[1] & 0x7f)
	switch n {
	case 126:
		var e [2]byte
		if _, err := io.ReadFull(r, e[:]); err != nil {
			return 0, nil, err
		}
		n = uint64(binary.BigEndian.Uint16(e[:]))
	case 127:
		var e [8]byte
		if _, err := io.ReadFull(r, e[:]); err != nil {
			return 0, nil, err
		}
		n = binary.BigEndian.Uint64(e[:])
	}
	p := make([]byte, n)
	_, err := io.ReadFull(r, p)
	return h[0] & 0xf, p, err
}

// echoFrames sends a message and a mebibyte, in frames, and checks that the
// binary messages that come back carry them.
func echoFrames(t *testing.T, w io.Writer, r io.Reader) {
	t.Helper()
	for _, want := range [][]byte{[]byte("hello"), pattern(1<<20, 4)} {
		go func() {
			for rest := want; len(rest) > 0; {
				n := min(len(rest), 64<<10)
				if _, err := w.Write(clientFrame(rest[:n], len(rest) == len(want), n == len(rest))); err != nil {
					return
				}
				rest = rest[n:]
			}
		}()
		var got []byte
		for len(got) < len(want) {
			op, p, err := readServerFrame(r)
			if err != nil || op != 0x2 {
				t.Fatalf("after %d bytes: opcode %d %q, %v", len(got), op, p, err)
			}
			got = append(got, p...)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%d bytes came back otherwise", len(want))
		}
	}
}

// h3Port is the port of tlsAddr's HTTP/3 listener.
func h3Port(tlsAddr listener.Addr) string {
	return strconv.Itoa(tlsAddr.UDPAddr.(*net.UDPAddr).Port)
}

// h3Connect sends an extended CONNECT with fib's HTTP/3 client, which
// trusts cert, and returns the response's status, header and a reader of
// its body as it streams.
func h3Connect(t *testing.T, cert *certtest.Cert) func(*stdhttp.Request) (int, stdhttp.Header, io.Reader, error) {
	t.Helper()
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
	return func(req *stdhttp.Request) (int, stdhttp.Header, io.Reader, error) {
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
	}
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
