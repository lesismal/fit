package tunneltest

import (
	"errors"
	"io"
	"net"
	"syscall"
	"testing"
	"time"
)

func TestEcho(t *testing.T) {
	for _, banner := range []string{"", "hi "} {
		c, err := net.Dial("tcp", Echo(t, banner))
		if err != nil {
			t.Fatal(err)
		}
		_ = c.SetDeadline(time.Now().Add(10 * time.Second))
		if _, err := c.Write([]byte("ping")); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, len(banner)+4)
		if _, err := io.ReadFull(c, got); err != nil || string(got) != banner+"ping" {
			t.Fatalf("got %q, %v", got, err)
		}
		_ = c.Close()
	}
}

// Stall hands over the connections it accepts, and closes those the test
// left with it as the test ends.
func TestStall(t *testing.T) {
	var c net.Conn
	t.Run("stall", func(t *testing.T) {
		conns, addr := Stall(t)
		var err error
		if c, err = net.Dial("tcp", addr); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for len(conns) == 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
	})
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err != io.EOF {
		t.Errorf("read from a connection Stall closed: %v", err)
	}
}

func TestEngine(t *testing.T) {
	if Engine(t) == nil {
		t.Fatal("no engine")
	}
}

// FreeAddr never hands out a port twice, though the kernel may.
func TestFreeAddr(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		addr := FreeAddr(t)
		if seen[addr] {
			t.Fatalf("%s handed out twice", addr)
		}
		seen[addr] = true
		l, err := net.Listen("tcp", addr)
		if err != nil {
			t.Fatalf("%s: %v", addr, err)
		}
		_ = l.Close()
	}
}

// FreeUDPAddr never hands out a port twice either.
func TestFreeUDPAddr(t *testing.T) {
	seen := map[string]bool{}
	for range 50 {
		addr := FreeUDPAddr(t)
		if seen[addr] {
			t.Fatalf("%s handed out twice", addr)
		}
		seen[addr] = true
	}
}

// Taken knows a bind refused, by fit's check or by the kernel.
func TestTaken(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, err = net.Listen("tcp", l.Addr().String())
	for _, e := range []error{err, errors.New("tunnel.hub: console_socket: TCP 127.0.0.1:1 is taken: another process listens there")} {
		if !Taken(e) {
			t.Errorf("%v not taken", e)
		}
	}
	for _, e := range []error{nil, errors.New("listen on 127.0.0.1:bad: unknown port")} {
		if Taken(e) {
			t.Errorf("%v taken", e)
		}
	}
}

// Start tries again while what it starts finds its address taken, and Bind
// gives each try another address.
func TestStartAgain(t *testing.T) {
	tries := 0
	if got := Start(t, func() (int, error) {
		if tries++; tries < 3 {
			return 0, errors.New("TCP 127.0.0.1:1 is taken: x")
		}
		return tries, nil
	}); got != 3 {
		t.Fatalf("started on try %d", got)
	}
	var addrs []string
	got := Bind(t, "udp", func(addr string) error {
		if addrs = append(addrs, addr); len(addrs) < 2 {
			return syscall.EADDRINUSE
		}
		return nil
	})
	if len(addrs) != 2 || addrs[0] == addrs[1] || got != addrs[1] {
		t.Fatalf("tried %v, got %s", addrs, got)
	}
}

// Start fails the test at once on another error, and after Attempts tries
// of taken addresses.
func TestStartFails(t *testing.T) {
	for _, tc := range []struct {
		err   error
		tries int
	}{{errors.New("bad config"), 1}, {syscall.EADDRINUSE, Attempts}} {
		tries := 0
		ft := &fatalT{TB: t}
		func() {
			defer func() { _ = recover() }()
			Start(ft, func() (struct{}, error) { tries++; return struct{}{}, tc.err })
		}()
		if !ft.failed || tries != tc.tries {
			t.Errorf("%v: failed %v after %d tries", tc.err, ft.failed, tries)
		}
	}
}

// fatalT records Fatal, and stops the caller with a panic.
type fatalT struct {
	testing.TB
	failed bool
}

func (f *fatalT) Fatal(...any)        { f.failed = true; panic("fatal") }
func (f *fatalT) Logf(string, ...any) {}
func (f *fatalT) Helper()             {}
