package netguard

import (
	"errors"
	"io"
	"net"
	"net/netip"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	fib "github.com/lesismal/fib"
)

func TestInternal(t *testing.T) {
	for addr, want := range map[string]string{
		"127.0.0.1": "127.0.0.0/8", "::ffff:127.0.0.1": "127.0.0.0/8", "0.0.0.0": "0.0.0.0/8",
		"169.254.169.254": "169.254.0.0/16", "100.100.100.200": "100.100.100.200/32",
		"224.0.0.1": "224.0.0.0/4", "255.255.255.255": "255.255.255.255/32",
		"::": "::/128", "::1": "::1/128", "fe80::1": "fe80::/10", "ff02::1": "ff00::/8", "fd00:ec2::254": "fd00:ec2::254/128",
		"10.0.0.1": "", "192.168.1.1": "", "100.100.100.201": "", "8.8.8.8": "", "2001:db8::1": "", "fd00:ec2::253": "",
	} {
		p, ok := Internal(netip.MustParseAddr(addr))
		if got := p.String(); ok != (want != "") || ok && got != want {
			t.Errorf("Internal(%s) = %s, %v; want %q", addr, got, ok, want)
		}
	}
}

func TestRefused(t *testing.T) {
	a := netip.MustParseAddr
	pp := func(ss ...string) []netip.Prefix {
		var out []netip.Prefix
		for _, s := range ss {
			out = append(out, netip.MustParsePrefix(s))
		}
		return out
	}
	self := a("10.0.0.5")
	for _, tc := range []struct {
		remote, local netip.Addr
		allow         []netip.Prefix
		want          bool
	}{
		{a("127.0.0.1"), a("127.0.0.1"), nil, true},
		{a("::ffff:169.254.169.254"), self, nil, true},
		{a("8.8.8.8"), self, nil, false},
		{self, self, nil, true},
		{a("::ffff:10.0.0.5"), self, nil, true},
		{self, netip.Addr{}, nil, false},
		// A prefix inside the range lets it through, a wider one does not.
		{a("127.0.0.1"), a("127.0.0.1"), pp("127.0.0.0/8"), false},
		{a("127.0.0.1"), a("127.0.0.1"), pp("127.0.0.1/32"), false},
		{a("127.0.0.1"), a("127.0.0.1"), pp("0.0.0.0/0", "::/0"), true},
		{a("169.254.169.254"), self, pp("169.254.169.254/32"), false},
		{a("169.254.1.1"), self, pp("169.254.169.254/32"), true},
		// The host's own address only by itself.
		{self, self, pp("10.0.0.0/8"), true},
		{self, self, pp("10.0.0.5/32"), false},
	} {
		if got := Refused(tc.remote, tc.local, tc.allow); got != tc.want {
			t.Errorf("Refused(%s from %s, %v) = %v, want %v", tc.remote, tc.local, tc.allow, got, tc.want)
		}
	}
}

// recorder is a handler that says what it heard.
type recorder struct {
	opened chan struct{}
	data   chan string
	closed chan struct{}
}

func newRecorder() *recorder {
	return &recorder{opened: make(chan struct{}, 1), data: make(chan string, 16), closed: make(chan struct{}, 1)}
}

func (r *recorder) OnOpen(*fib.Connection)                 { r.opened <- struct{}{} }
func (r *recorder) OnData(_ *fib.Connection, data []byte)  { r.data <- string(data) }
func (r *recorder) OnPriorityData(*fib.Connection, []byte) {}
func (r *recorder) OnClose(*fib.Connection, error)         { r.closed <- struct{}{} }

// TestDial dials a server that greets each connection: one ok refuses is
// closed, its handler hears nothing and done is told the refusal; one ok
// lets through is handed to its handler, which gets the greeting straight
// from the engine, the stand-in having stepped aside.
func TestDial(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = c.Write([]byte("hello"))
				_, _ = io.Copy(io.Discard, c)
			}()
		}
	}()
	engine, err := fib.New(fib.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- engine.Run() }()
	t.Cleanup(func() {
		engine.Stop()
		<-stopped
		_ = engine.Close()
	})

	refusal := errors.New("refused")
	type result struct {
		c   *fib.Connection
		err error
	}
	dial := func(h fib.Handler, ok func(remote, local netip.Addr) bool) result {
		t.Helper()
		done := make(chan result, 1)
		if err := Dial(nil, engine, "tcp", l.Addr().String(), 5*time.Second, h, func(c *fib.Connection, err error) {
			done <- result{c, err}
		}, ok, refusal); err != nil {
			t.Fatal(err)
		}
		select {
		case r := <-done:
			return r
		case <-time.After(5 * time.Second):
			t.Fatal("the dial did not finish")
		}
		return result{}
	}

	var seen [2]netip.Addr
	rec := newRecorder()
	r := dial(rec, func(remote, local netip.Addr) bool {
		seen = [2]netip.Addr{remote, local}
		return !Refused(remote, local, nil)
	})
	if r.c != nil || r.err != refusal {
		t.Fatalf("a refused connection: %v, %v", r.c, r.err)
	}
	if seen[0] != netip.MustParseAddr("127.0.0.1") || seen[1] != netip.MustParseAddr("127.0.0.1") {
		t.Errorf("ok was asked about %v", seen)
	}
	select {
	case <-rec.opened:
		t.Error("the handler of a refused connection was opened")
	case s := <-rec.data:
		t.Errorf("the handler of a refused connection got %q", s)
	case <-rec.closed:
		t.Error("the handler of a refused connection was closed")
	case <-time.After(200 * time.Millisecond):
	}

	rec = newRecorder()
	r = dial(rec, func(remote, local netip.Addr) bool { return true })
	if r.err != nil || r.c == nil {
		t.Fatalf("a connection let through: %v, %v", r.c, r.err)
	}
	<-rec.opened
	select {
	case s := <-rec.data:
		if s != "hello" {
			t.Errorf("got %q", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the handler got nothing")
	}
	_ = r.c.Close()
	select {
	case <-rec.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler was not closed")
	}
}

// TestDialLocalAddr checks ok is asked about the address a UDP connection
// was opened from, and about none for a Unix socket's.
func TestDialLocalAddr(t *testing.T) {
	engine, err := fib.New(fib.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- engine.Run() }()
	t.Cleanup(func() {
		engine.Stop()
		<-stopped
		_ = engine.Close()
	})
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	type dialCase struct {
		network, addr string
		want          netip.Addr
	}
	cases := []dialCase{{"udp", pc.LocalAddr().String(), netip.MustParseAddr("127.0.0.1")}}
	if runtime.GOOS != "windows" {
		sock := filepath.Join(t.TempDir(), "s")
		ul, err := net.Listen("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ul.Close() })
		cases = append(cases, dialCase{"unix", sock, netip.Addr{}})
	}

	refusal := errors.New("refused")
	for _, tc := range cases {
		asked := make(chan netip.Addr, 1)
		done := make(chan error, 1)
		if err := Dial(nil, engine, tc.network, tc.addr, 5*time.Second, newRecorder(), func(c *fib.Connection, err error) {
			done <- err
		}, func(_, local netip.Addr) bool {
			asked <- local
			return false
		}, refusal); err != nil {
			t.Fatalf("%s: %v", tc.network, err)
		}
		select {
		case err := <-done:
			if err != refusal {
				t.Errorf("%s: done told %v, want the refusal", tc.network, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: the dial did not finish", tc.network)
		}
		if got := <-asked; got != tc.want {
			t.Errorf("%s: ok was asked about %v from, want %v", tc.network, got, tc.want)
		}
	}
}
