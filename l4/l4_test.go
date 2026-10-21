package l4_test

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/listener"
	"github.com/lesismal/fit/runtime"
	"github.com/lesismal/fit/upstream"
)

// tcpConfig is a config with a tcp listener whose service, with params,
// proxies to the upstream at addrs.
func tcpConfig(params string, addrs ...string) string {
	quoted := make([]string, len(addrs))
	for i, a := range addrs {
		quoted[i] = fmt.Sprintf("%q", a)
	}
	return `{
  "listeners": [{"name": "tcp", "address": "127.0.0.1:0", "protocols": ["tcp"]}],
  "upstreams": [{"name": "app", "addresses": [` + strings.Join(quoted, ", ") + `]}],
  "services": [{"name": "svc", "listeners": ["tcp"], "upstream": "app"` + params + `}]
}`
}

// fit is fit serving a config, as fit run does.
type fit struct {
	t    testing.TB
	addr string
	rt   *runtime.Runtime
	s    *listener.Server
	env  runtime.Env
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
	return &fit{t: t, addr: s.Addrs()[0].Addr.String(), rt: rt, s: s, env: env}
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

// dial connects to fit's tcp listener, with a receive buffer of rcvbuf
// bytes when it is not 0, set before connecting.
func (f *fit) dial(rcvbuf int) *net.TCPConn {
	f.t.Helper()
	d := net.Dialer{Timeout: 5 * time.Second}
	if rcvbuf > 0 {
		d.Control = setRcvbuf(rcvbuf)
	}
	c, err := d.Dial("tcp", f.addr)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { c.Close() })
	return c.(*net.TCPConn)
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
	go func() {
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
func refused(t *testing.T) string {
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

// expectClosed checks that c is closed by fit within d: its read ends
// without data.
func expectClosed(t *testing.T, c net.Conn, d time.Duration) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(d))
	n, err := c.Read(make([]byte, 1))
	if n != 0 || err == nil || isTimeout(err) {
		t.Fatalf("read %d bytes, %v; want the connection closed", n, err)
	}
}

func isTimeout(err error) bool {
	ne, ok := err.(net.Error)
	return ok && ne.Timeout()
}

// TestTCPEcho proxies connections to an echo server: one that says hello,
// and 50 at once with a little more each.
func TestTCPEcho(t *testing.T) {
	f := startFit(t, tcpConfig("", backend(t, echo)))
	c := f.dial(0)
	if _, err := c.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 5)
	if _, err := io.ReadFull(c, got); err != nil || string(got) != "hello" {
		t.Fatalf("read %q, %v", got, err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 50)
	for i := range 50 {
		c := f.dial(0)
		wg.Add(1)
		go func() {
			defer wg.Done()
			want := pattern(10000+i, byte(i))
			if _, err := c.Write(want); err != nil {
				errs <- err
				return
			}
			got := make([]byte, len(want))
			if _, err := io.ReadFull(c, got); err != nil || !bytes.Equal(got, want) {
				errs <- fmt.Errorf("connection %d: %v", i, err)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// TestTCPLarge sends 32MB each way at once through fit.
func TestTCPLarge(t *testing.T) {
	const size = 32 << 20
	f := startFit(t, tcpConfig("", backend(t, echo)))
	c := f.dial(0)
	want := pattern(size, 7)
	// No half-close: an end of one side ends the other.
	go func() { _, _ = c.Write(want) }()
	_ = c.SetReadDeadline(time.Now().Add(30 * time.Second))
	h := sha256.New()
	n, err := io.CopyN(h, c, size)
	if err != nil || n != size || !bytes.Equal(h.Sum(nil), sha(want)) {
		t.Fatalf("read %d bytes, %v; want the %d sent back", n, err, size)
	}
}

func sha(b []byte) []byte {
	s := sha256.Sum256(b)
	return s[:]
}

// TestTCPSlowClient has a client stop reading while its upstream sends:
// fit holds the upstream back rather than taking what it sends, and the
// client then gets all of it.
func TestTCPSlowClient(t *testing.T) {
	const size = 64 << 20
	data := pattern(size, 3)
	var sent atomic.Int64
	f := startFit(t, tcpConfig("", backend(t, func(c net.Conn) {
		// The client's word is read first: a socket closed with input
		// unread resets the connection, and the reset would throw away
		// what fit has not yet read of what it sent.
		if _, err := io.ReadFull(c, make([]byte, 2)); err != nil {
			return
		}
		for rest := data; len(rest) > 0; {
			n, err := c.Write(rest[:min(len(rest), 64<<10)])
			sent.Add(int64(n))
			if err != nil {
				return
			}
			rest = rest[n:]
		}
	})))
	c := f.dial(64 << 10)
	if _, err := c.Write([]byte("go")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	if n := sent.Load(); n >= size/2 {
		t.Fatalf("the upstream sent %d bytes to a client that read none; want them held back", n)
	}
	_ = c.SetReadDeadline(time.Now().Add(30 * time.Second))
	h := sha256.New()
	if n, err := io.CopyN(h, c, size); err != nil || !bytes.Equal(h.Sum(nil), sha(data)) {
		t.Fatalf("read %d bytes, %v", n, err)
	}
}

// TestTCPSlowUpstream has an upstream stop reading while its client sends:
// fit holds the client back, and the upstream then gets all of it.
func TestTCPSlowUpstream(t *testing.T) {
	const size = 64 << 20
	data := pattern(size, 5)
	read := make(chan error, 1)
	start := make(chan struct{})
	f := startFit(t, tcpConfig("", backend(t, func(c net.Conn) {
		<-start
		h := sha256.New()
		n, err := io.CopyN(h, c, size)
		if err == nil && !bytes.Equal(h.Sum(nil), sha(data)) {
			err = fmt.Errorf("the upstream got %d bytes, not those sent", n)
		}
		read <- err
	})))
	c := f.dial(0)
	var sent atomic.Int64
	go func() {
		for rest := data; len(rest) > 0; {
			n, err := c.Write(rest[:min(len(rest), 64<<10)])
			sent.Add(int64(n))
			if err != nil {
				return
			}
			rest = rest[n:]
		}
	}()
	time.Sleep(time.Second)
	if n := sent.Load(); n >= size/2 {
		t.Fatalf("the client sent %d bytes to an upstream that read none; want them held back", n)
	}
	close(start)
	select {
	case err := <-read:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the upstream did not get it all")
	}
}

// TestTCPHangUp ends one side when the other does: the client gets what the
// upstream sent before it closed, and the upstream hears the client close.
func TestTCPHangUp(t *testing.T) {
	t.Run("upstream", func(t *testing.T) {
		f := startFit(t, tcpConfig("", backend(t, func(c net.Conn) {
			_, _ = c.Write(pattern(1<<20, 9))
		})))
		c := f.dial(0)
		_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
		got, err := io.ReadAll(c)
		if err != nil || !bytes.Equal(got, pattern(1<<20, 9)) {
			t.Fatalf("read %d bytes, %v; want 1MB, then the end", len(got), err)
		}
	})
	t.Run("client-late", func(t *testing.T) {
		ended := make(chan error, 1)
		f := startFit(t, tcpConfig("", backend(t, func(c net.Conn) {
			_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
			got, err := io.ReadAll(c)
			if err == nil && string(got) != "last words" {
				err = fmt.Errorf("got %q", got)
			}
			ended <- err
		})))
		c := f.dial(0)
		if _, err := c.Write([]byte("last words")); err != nil {
			t.Fatal(err)
		}
		time.Sleep(300 * time.Millisecond)
		c.Close()
		if err := <-ended; err != nil {
			t.Fatal(err)
		}
	})
	t.Run("client", func(t *testing.T) {
		ended := make(chan error, 1)
		f := startFit(t, tcpConfig("", backend(t, func(c net.Conn) {
			_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
			got, err := io.ReadAll(c)
			if err == nil && string(got) != "last words" {
				err = fmt.Errorf("got %q", got)
			}
			ended <- err
		})))
		c := f.dial(0)
		if _, err := c.Write([]byte("last words")); err != nil {
			t.Fatal(err)
		}
		c.Close()
		if err := <-ended; err != nil {
			t.Fatal(err)
		}
	})
}

// TestTCPConnectFailure closes a client whose upstream refuses, and tries
// the next address when there is one.
func TestTCPConnectFailure(t *testing.T) {
	f := startFit(t, tcpConfig("", refused(t)))
	expectClosed(t, f.dial(0), 5*time.Second)

	f = startFit(t, tcpConfig("", refused(t), backend(t, echo)))
	for range 4 {
		c := f.dial(0)
		_, _ = c.Write([]byte("hi"))
		got := make([]byte, 2)
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.ReadFull(c, got); err != nil || string(got) != "hi" {
			t.Fatalf("read %q, %v; want the live address's echo", got, err)
		}
	}
}

// TestTCPIdleTimeout closes a connection that carries nothing for the
// service's idle_timeout, and not one that keeps carrying something.
func TestTCPIdleTimeout(t *testing.T) {
	f := startFit(t, tcpConfig(`, "idle_timeout": "300ms"`, backend(t, echo)))
	busy, idle := f.dial(0), f.dial(0)
	start := time.Now()
	for time.Since(start) < 900*time.Millisecond {
		_, _ = busy.Write([]byte("x"))
		_ = busy.SetReadDeadline(time.Now().Add(time.Second))
		if _, err := io.ReadFull(busy, make([]byte, 1)); err != nil {
			t.Fatalf("a busy connection after %v: %v", time.Since(start), err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	expectClosed(t, idle, time.Second)
}

// TestTCPMaxConns closes a connection past the service's max_conns at once,
// and takes another once one has ended.
func TestTCPMaxConns(t *testing.T) {
	f := startFit(t, tcpConfig(`, "max_conns": 2`, backend(t, echo)))
	roundTrip := func(c net.Conn) error {
		_, _ = c.Write([]byte("ok"))
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, err := io.ReadFull(c, make([]byte, 2))
		return err
	}
	a, b := f.dial(0), f.dial(0)
	if roundTrip(a) != nil || roundTrip(b) != nil {
		t.Fatal("the first two connections were not proxied")
	}
	expectClosed(t, f.dial(0), 5*time.Second)
	a.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c := f.dial(0)
		if roundTrip(c) == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no connection was taken once one had ended")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestTCPReload keeps a tcp listener across a reload: a connection made
// before goes on with the old service until the drain timeout closes it, and
// one made after goes to the new service's upstream.
func TestTCPReload(t *testing.T) {
	greet := func(word string) func(net.Conn) {
		return func(c net.Conn) {
			_, _ = c.Write([]byte(word))
			echo(c)
		}
	}
	f := startFit(t, tcpConfig("", backend(t, greet("old"))))
	f.s.SetDrainTimeout(500 * time.Millisecond)
	old := f.dial(0)
	got := make([]byte, 3)
	if _, err := io.ReadFull(old, got); err != nil || string(got) != "old" {
		t.Fatalf("read %q, %v", got, err)
	}
	f.reload(tcpConfig("", backend(t, greet("new"))))
	c := f.dial(0)
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(c, got); err != nil || string(got) != "new" {
		t.Fatalf("after the reload, read %q, %v", got, err)
	}
	_, _ = old.Write([]byte("abc"))
	_ = old.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(old, got); err != nil || string(got) != "abc" {
		t.Fatalf("the old connection: read %q, %v", got, err)
	}
	expectClosed(t, old, 5*time.Second)
}

// BenchmarkTCPProxy echoes 256KB at a time over one connection, straight
// to the echo server and through fit, which costs the copy each way.
func BenchmarkTCPProxy(b *testing.B) {
	for _, through := range []bool{false, true} {
		name := "direct"
		if through {
			name = "fit"
		}
		b.Run(name, func(b *testing.B) {
			addr := backend(b, echo)
			if through {
				addr = startFit(b, tcpConfig("", addr)).addr
			}
			c, err := net.Dial("tcp", addr)
			if err != nil {
				b.Fatal(err)
			}
			defer c.Close()
			chunk := pattern(256<<10, 1)
			got := make([]byte, len(chunk))
			b.SetBytes(int64(2 * len(chunk)))
			b.ResetTimer()
			for range b.N {
				go func() { _, _ = c.Write(chunk) }()
				if _, err := io.ReadFull(c, got); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
