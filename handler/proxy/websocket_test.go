package proxy

import (
	"bufio"
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net"
	stdhttp "net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// wsAccept is the Sec-WebSocket-Accept for key (RFC 6455).
func wsAccept(key string) string {
	h := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(h[:])
}

// wsBackend is a WebSocket server that, once switched, echoes the bytes it
// gets, or on the message "flood" sends floodBytes as fast as the
// connection takes them. /refuse is answered 403.
type wsBackend struct {
	addr string
	// written counts the flood's bytes the connection took; closed the
	// connections that ended.
	written atomic.Int64
	closed  chan struct{}
	headers chan stdhttp.Header
}

const floodBytes = 32 << 20

func newWSBackend(t *testing.T) *wsBackend {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	b := &wsBackend{addr: l.Addr().String(), closed: make(chan struct{}, 16), headers: make(chan stdhttp.Header, 16)}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go b.serve(c)
		}
	}()
	return b
}

func (b *wsBackend) serve(c net.Conn) {
	defer func() {
		c.Close()
		b.closed <- struct{}{}
	}()
	br := bufio.NewReader(c)
	req, err := stdhttp.ReadRequest(br)
	if err != nil {
		return
	}
	b.headers <- req.Header
	if req.URL.Path == "/refuse" || !strings.EqualFold(req.Header.Get("Upgrade"), "websocket") {
		_, _ = io.WriteString(c, "HTTP/1.1 403 Forbidden\r\nContent-Length: 7\r\nX-Why: test\r\n\r\nrefused")
		return
	}
	resp := "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n" +
		"Sec-WebSocket-Accept: " + wsAccept(req.Header.Get("Sec-WebSocket-Key")) + "\r\n"
	if req.Header.Get("Sec-WebSocket-Protocol") != "" {
		resp += "Sec-WebSocket-Protocol: chat\r\n"
	}
	if _, err := io.WriteString(c, resp+"\r\n"); err != nil {
		return
	}
	buf := make([]byte, 64<<10)
	for {
		n, err := br.Read(buf)
		if err != nil {
			return
		}
		if string(buf[:n]) == "flood" {
			chunk := bytes.Repeat([]byte("f"), 64<<10)
			for b.written.Load() < floodBytes {
				m, err := c.Write(chunk)
				b.written.Add(int64(m))
				if err != nil {
					return
				}
			}
			continue
		}
		if _, err := c.Write(buf[:n]); err != nil {
			return
		}
	}
}

// wsDial opens a WebSocket through fit at base to path, and returns the
// connection, its reader past the 101, and the response.
func wsDial(t *testing.T, base, path string, protocols string) (net.Conn, *bufio.Reader, *stdhttp.Response) {
	t.Helper()
	return wsDialWith(t, &net.Dialer{Timeout: 5 * time.Second}, base, path, protocols)
}

func wsDialWith(t *testing.T, d *net.Dialer, base, path string, protocols string) (net.Conn, *bufio.Reader, *stdhttp.Response) {
	t.Helper()
	c, err := d.Dial("tcp", strings.TrimPrefix(base, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	_ = c.SetDeadline(time.Now().Add(20 * time.Second))
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef"))
	req := "GET " + path + " HTTP/1.1\r\nHost: x\r\nConnection: keep-alive, Upgrade\r\nUpgrade: websocket\r\n" +
		"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: " + key + "\r\n"
	if protocols != "" {
		req += "Sec-WebSocket-Protocol: " + protocols + "\r\n"
	}
	if _, err := io.WriteString(c, req+"\r\n"); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(c)
	resp, err := stdhttp.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode == stdhttp.StatusSwitchingProtocols && resp.Header.Get("Sec-WebSocket-Accept") != wsAccept(key) {
		t.Fatalf("accept %q, want %q", resp.Header.Get("Sec-WebSocket-Accept"), wsAccept(key))
	}
	return c, br, resp
}

func TestWebSocketEcho(t *testing.T) {
	b := newWSBackend(t)
	base := startFit(t, retryConfig(``, ``), b.addr)
	c, br, resp := wsDial(t, base, "/chat", "chat, superchat")
	if resp.StatusCode != stdhttp.StatusSwitchingProtocols || resp.Header.Get("Sec-WebSocket-Protocol") != "chat" ||
		!strings.EqualFold(resp.Header.Get("Upgrade"), "websocket") {
		t.Fatalf("%d %v", resp.StatusCode, resp.Header)
	}
	h := <-b.headers
	if h.Get("Sec-WebSocket-Version") != "13" || h.Get("X-Forwarded-For") == "" || !strings.EqualFold(h.Get("Connection"), "Upgrade") {
		t.Errorf("the upstream's handshake: %v", h)
	}
	for _, msg := range []string{"hello", "world"} {
		if _, err := io.WriteString(c, msg); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, len(msg))
		if _, err := io.ReadFull(br, got); err != nil || string(got) != msg {
			t.Fatalf("echo %q %v", got, err)
		}
	}

	// 16 MB each way at once.
	const size = 16 << 20
	payload := bytes.Repeat([]byte("0123456789abcdef"), size/16)
	go func() { _, _ = c.Write(payload) }()
	h1, h2 := sha256.New(), sha256.New()
	h1.Write(payload)
	if n, err := io.CopyN(h2, br, size); err != nil || n != size || !bytes.Equal(h1.Sum(nil), h2.Sum(nil)) {
		t.Fatalf("large echo: %d %v", n, err)
	}

	// The client goes: the upstream's connection ends too.
	c.Close()
	select {
	case <-b.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the upstream connection outlived the client's")
	}
}

func TestWebSocketRefused(t *testing.T) {
	b := newWSBackend(t)
	base := startFit(t, retryConfig(``, ``), b.addr)
	_, br, resp := wsDial(t, base, "/refuse", "")
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 100))
	if resp.StatusCode != stdhttp.StatusForbidden || string(body) != "refused" || resp.Header.Get("X-Why") != "test" {
		t.Fatalf("%d %q %v", resp.StatusCode, body, resp.Header)
	}
	_ = br

	// No upstream at all.
	base = startFit(t, retryConfig(``, ``), refused(t))
	if _, _, resp := wsDial(t, base, "/", ""); resp.StatusCode != stdhttp.StatusBadGateway {
		t.Fatalf("no upstream: %d", resp.StatusCode)
	}
}

func TestWebSocketIdleTimeout(t *testing.T) {
	b := newWSBackend(t)
	base := startFit(t, retryConfig(``, `, "tunnel_idle_timeout": "300ms"`), b.addr)
	c, br, resp := wsDial(t, base, "/", "")
	if resp.StatusCode != stdhttp.StatusSwitchingProtocols {
		t.Fatalf("%d", resp.StatusCode)
	}
	// Traffic keeps it open past the timeout.
	for range 4 {
		time.Sleep(150 * time.Millisecond)
		if _, err := io.WriteString(c, "x"); err != nil {
			t.Fatal(err)
		}
		if _, err := br.ReadByte(); err != nil {
			t.Fatalf("closed while in use: %v", err)
		}
	}
	start := time.Now()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := br.ReadByte(); err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("idle: %v after %v", err, time.Since(start))
	}
	select {
	case <-b.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the upstream connection outlived the idle timeout")
	}
}
