package hub

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/lesismal/fit/tunnel/internal/transport"
	"github.com/lesismal/fit/tunnel/internal/wscrypt"
)

// wsProxy is a WebSocket to the console socket: its messages, as they
// arrive.
type wsProxy struct {
	t    *testing.T
	c    transport.Conn
	msgs chan []byte
	done chan struct{}
}

func (w *wsProxy) OnData(data []byte) {
	select {
	case w.msgs <- bytes.Clone(data):
	default:
	}
}

func (w *wsProxy) OnClose(error) { close(w.done) }

func (v *env) dialSocket(addr string) *wsProxy {
	v.t.Helper()
	w := &wsProxy{t: v.t, msgs: make(chan []byte, 256), done: make(chan struct{})}
	c, err := transport.Dial(v.e, "ws://"+addr+"/", 5*time.Second, func(transport.Conn) transport.Handler { return w })
	if err != nil {
		v.t.Fatal(err)
	}
	w.c = c
	v.t.Cleanup(func() { _ = c.Close() })
	return w
}

func (w *wsProxy) next() []byte {
	w.t.Helper()
	select {
	case b := <-w.msgs:
		return b
	case <-time.After(10 * time.Second):
		w.t.Fatal("timed out waiting for a message")
		return nil
	}
}

func (w *wsProxy) waitClosed() {
	w.t.Helper()
	select {
	case <-w.done:
	case <-time.After(10 * time.Second):
		w.t.Fatal("the WebSocket did not close")
	}
}

// session exchanges hellos with the socket.
func (w *wsProxy) session(password string) *wscrypt.Session {
	w.t.Helper()
	secret, err := wscrypt.NewSecret(password)
	if err != nil {
		w.t.Fatal(err)
	}
	hub, proxy := w.next(), wscrypt.Hello()
	_ = w.c.Send(bytes.Clone(proxy))
	sess, err := wscrypt.NewSession(secret, hub, proxy, false)
	if err != nil {
		w.t.Fatal(err)
	}
	return sess
}

// The console socket carries the console to a WebSocket, sent in more than
// one message before the console is connected; it refuses WebSockets past
// max_conns, a bad hello, and messages once the console is gone.
func TestConsoleSocket(t *testing.T) {
	sock := freeAddr(t)
	v := newHub(t, fmt.Sprintf(`{"password": "pw", "console": %s, "console_socket": {"address": %q, "password": "s", "max_conns": 1}}`,
		consoleJSON("127.0.0.1:0"), sock))
	w := v.dialSocket(sock)
	sess := w.session("s")
	// The request goes in two messages, the second while the console is
	// being dialed.
	_ = w.c.Send(sess.Seal([]byte("GET / HTTP/1.1\r\n")))
	_ = w.c.Send(sess.Seal([]byte("Host: x\r\nConnection: close\r\n\r\n")))

	// Past max_conns, a WebSocket closes at once, maybe before its dial
	// has returned.
	refused := &wsProxy{t: t, msgs: make(chan []byte, 1), done: make(chan struct{})}
	if c, err := transport.Dial(v.e, "ws://"+sock+"/", 5*time.Second, func(transport.Conn) transport.Handler { return refused }); err == nil {
		refused.waitClosed()
		_ = c.Close()
	}

	var got []byte
	for !bytes.Contains(got, []byte("core/workbench.js")) {
		plain, err := sess.Open(w.next())
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, plain...)
	}
	if !bytes.HasPrefix(got, []byte("HTTP/1.1 200")) {
		t.Fatalf("the page: %.200s", got)
	}
	w.waitClosed()
	waitFor(t, "the WebSocket to go", func() bool {
		h := v.s.socket.h
		h.mu.Lock()
		defer h.mu.Unlock()
		return len(h.conns) == 0
	})

	// A hello of the wrong length.
	w = v.dialSocket(sock)
	w.next()
	_ = w.c.Send([]byte("short"))
	w.waitClosed()
	waitFor(t, "the WebSocket to go", func() bool {
		h := v.s.socket.h
		h.mu.Lock()
		defer h.mu.Unlock()
		return len(h.conns) == 0
	})

	// Without the console, there is nothing to carry.
	v.s.console.Stop(context.Background())
	w = v.dialSocket(sock)
	sess = w.session("s")
	_ = w.c.Send(sess.Seal([]byte("GET / HTTP/1.1\r\n\r\n")))
	w.waitClosed()
}

// What arrives for a proxy that closed is dropped.
func TestSocketClosed(t *testing.T) {
	p := &proxy{closed: true}
	p.message([]byte("x"))
	u := &socketUpstream{p: p}
	u.OnData(nil, []byte("x"))
	u.OnPriorityData(nil, nil)
	(&socketHandler{}).OnPriorityData(nil, nil)
}

func TestConsoleAddr(t *testing.T) {
	for addr, want := range map[string]string{
		"":             "",
		"no port":      "",
		":80":          "127.0.0.1:80",
		"0.0.0.0:80":   "127.0.0.1:80",
		"[::]:80":      "127.0.0.1:80",
		"10.1.2.3:80":  "10.1.2.3:80",
		"[::1]:80":     "[::1]:80",
		"example:8080": "example:8080",
	} {
		if got := consoleAddr(addr); got != want {
			t.Errorf("consoleAddr(%q) = %q, want %q", addr, got, want)
		}
	}
}
