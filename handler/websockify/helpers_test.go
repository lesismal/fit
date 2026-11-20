package websockify

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"strings"
	"sync"
	"testing"
	"time"

	fib "github.com/lesismal/fib"
	"github.com/lesismal/fib/websocket"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/listener"
	"github.com/lesismal/fit/runtime"
	"github.com/lesismal/fit/upstream"
)

// fit is fit serving a config, as fit run does.
type fit struct {
	t      testing.TB
	addr   string
	addrs  []listener.Addr
	rt     *runtime.Runtime
	s      *listener.Server
	env    runtime.Env
	engine *fib.Engine
}

// routeDoc is a config with a plain listener whose one route is a
// websockify handler with params, and upstreams, a JSON array, when not
// empty.
func routeDoc(params, upstreams string) string {
	if upstreams == "" {
		upstreams = "[]"
	}
	return `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "upstreams": ` + upstreams + `,
  "routes": [{"handler": {"module": "websockify", "params": ` + params + `}}]
}`
}

func startFit(t testing.TB, doc string) *fit {
	t.Helper()
	c, err := config.Load([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	engine, stop, err := upstream.StartEngine()
	if err != nil {
		t.Fatal(err)
	}
	env := runtime.Env{Engine: engine}
	g, err := runtime.Compile(c, env)
	if err != nil {
		stop()
		t.Fatal(err)
	}
	rt := new(runtime.Runtime)
	rt.Swap(g)
	s, err := listener.Start(rt)
	if err != nil {
		stop()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = s.Close()
		rt.Close()
		stop()
	})
	addrs := s.Addrs()
	return &fit{t: t, addr: addrs[0].Addr.String(), addrs: addrs, rt: rt, s: s, env: env, engine: engine}
}

// reload replaces f's config with doc.
func (f *fit) reload(doc string) {
	f.t.Helper()
	c, err := config.Load([]byte(doc))
	if err != nil {
		f.t.Fatal(err)
	}
	env := f.env
	env.Previous = f.rt.Current()
	g, err := runtime.Compile(c, env)
	env.Previous.Release()
	if err != nil {
		f.t.Fatal(err)
	}
	if _, _, err := f.s.Reload(g); err != nil {
		f.t.Fatal(err)
	}
}

// handler is the websockify handler of f's current config.
func (f *fit) handler() *Websockify {
	g := f.rt.Current()
	defer g.Release()
	for _, m := range g.Modules() {
		if w, ok := m.(*Websockify); ok {
			return w
		}
	}
	f.t.Fatal("no websockify handler")
	return nil
}

// stats is what f's handlers counted, for the bridge name.
func (f *fit) stats(name string) Stats {
	for _, s := range AllStats(f.engine) {
		if s.Name == name {
			return s
		}
	}
	return Stats{}
}

// eventually waits up to d for cond.
func eventually(t testing.TB, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// clientEngine is a fib engine for WebSocket clients, stopped when t ends.
func clientEngine(t testing.TB) *fib.Engine {
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
	return engine
}

// msg is a message a client got.
type msg struct {
	op   websocket.Opcode
	data []byte
}

// closed is how a client's connection ended.
type closed struct {
	code   uint16
	reason string
}

// client is a WebSocket client of fib's, through fit.
type client struct {
	t      testing.TB
	conn   *websocket.Connection
	msgs   chan msg
	closed chan closed
	// buf is what was read of the messages and not yet taken.
	buf []byte
}

// dialWS opens a WebSocket to fit at path, offering protocols, with header.
// It returns the client, or the response and error of a refused handshake.
func dialWS(t testing.TB, engine *fib.Engine, url string, header stdhttp.Header, protocols ...string) (*client, *stdhttp.Response, error) {
	t.Helper()
	cfg := websocket.DefaultDialerConfig()
	cfg.Subprotocols = protocols
	cfg.MaxMessageBytes = 64 << 20
	d := websocket.NewDialer(engine, cfg)
	c := &client{t: t, msgs: make(chan msg, 1<<16), closed: make(chan closed, 1)}
	conn, resp, err := d.Go(url, header, websocket.HandlerFuncs{
		Message: func(_ *websocket.Connection, op websocket.Opcode, data []byte) {
			c.msgs <- msg{op, bytes.Clone(data)}
		},
		Close: func(_ *websocket.Connection, code uint16, reason string, _ error) {
			c.closed <- closed{code, reason}
		},
	}).Wait()
	if err != nil {
		return nil, resp, err
	}
	c.conn = conn
	t.Cleanup(func() { conn.Abort(nil) })
	return c, resp, nil
}

// mustDial is dialWS, failing t when the handshake is refused.
func mustDial(t testing.TB, engine *fib.Engine, url string, header stdhttp.Header, protocols ...string) *client {
	t.Helper()
	c, resp, err := dialWS(t, engine, url, header, protocols...)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("dial %s: %d %v", url, status, err)
	}
	return c
}

// next returns the next message, or fails t after d.
func (c *client) next(d time.Duration) msg {
	c.t.Helper()
	select {
	case m := <-c.msgs:
		return m
	case <-time.After(d):
		c.t.Fatal("no message")
		return msg{}
	}
}

// read returns the next n bytes the messages carry, decoding text ones from
// base64 when b64.
func (c *client) read(n int, b64 bool) []byte {
	c.t.Helper()
	for len(c.buf) < n {
		m := c.next(30 * time.Second)
		if b64 {
			if m.op != websocket.Text {
				c.t.Fatalf("a message of opcode %d, want text", m.op)
			}
			dec, err := base64.StdEncoding.DecodeString(string(m.data))
			if err != nil {
				c.t.Fatal(err)
			}
			m.data = dec
		} else if m.op != websocket.Binary {
			c.t.Fatalf("a message of opcode %d, want binary", m.op)
		}
		c.buf = append(c.buf, m.data...)
	}
	out := c.buf[:n]
	c.buf = c.buf[n:]
	return out
}

// waitClosed returns how the connection ended, or fails t after d.
func (c *client) waitClosed(d time.Duration) closed {
	c.t.Helper()
	select {
	case cl := <-c.closed:
		return cl
	case <-time.After(d):
		c.t.Fatal("the WebSocket did not close")
		return closed{}
	}
}

// backend serves each connection with serve, on a goroutine of its own.
func backend(t testing.TB, serve func(net.Conn)) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	t.Cleanup(func() {
		l.Close()
		wg.Wait()
	})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer c.Close()
				serve(c)
			}()
		}
	}()
	return l.Addr().String()
}

func echo(c net.Conn) { _, _ = io.Copy(c, c) }

// refused is an address nothing listens on.
func refused(t testing.TB) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

// pattern is n bytes that are not all alike, seeded by seed.
func pattern(n int, seed byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*31) ^ seed
	}
	return b
}

func sha(b []byte) [32]byte { return sha256.Sum256(b) }

// q quotes s for a JSON document.
func q(s string) string { return fmt.Sprintf("%q", s) }

// rawRequest sends req, raw HTTP/1.1, to addr and returns the status line
// and header of the answer.
func rawRequest(t testing.TB, addr, req string) string {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(c, req); err != nil {
		t.Fatal(err)
	}
	var head []byte
	buf := make([]byte, 4096)
	for !bytes.Contains(head, []byte("\r\n\r\n")) {
		n, err := c.Read(buf)
		head = append(head, buf[:n]...)
		if err != nil {
			break
		}
	}
	h, _, _ := strings.Cut(string(head), "\r\n\r\n")
	return h
}
