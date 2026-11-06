// Package tunneltest is what the tunnel's tests share: a fib engine that
// runs for a test, and the services tunnels are tested against.
package tunneltest

import (
	"io"
	"net"
	"sync"
	"testing"

	"github.com/lesismal/fib"
)

// Engine returns a fib engine that runs until the test ends.
func Engine(t testing.TB) *fib.Engine {
	t.Helper()
	e, err := fib.New(fib.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = e.Run(); close(done) }()
	t.Cleanup(func() { e.Stop(); <-done; _ = e.Close() })
	return e
}

// handed is every port FreeAddr has handed out in this process.
var handed struct {
	sync.Mutex
	ports map[int]bool
}

// FreeAddr is a local TCP address nothing listens on, whose port no other
// call in this process has handed out. The kernel may give a port it gave
// before, once that listener closed; and fib's engines listen with
// SO_REUSEPORT, so two bindings handed the same port both listen on it
// without an error, and the kernel spreads connections between them: a
// test would talk to one server when it means the other.
func FreeAddr(t testing.TB) string {
	t.Helper()
	handed.Lock()
	defer handed.Unlock()
	if handed.ports == nil {
		handed.ports = map[int]bool{}
	}
	// A port handed out before stays held while the kernel is asked
	// again, so that it is not given back at once.
	var held []net.Listener
	defer func() {
		for _, l := range held {
			_ = l.Close()
		}
	}()
	for range 1000 {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, l)
		addr := l.Addr().(*net.TCPAddr)
		if !handed.ports[addr.Port] {
			handed.ports[addr.Port] = true
			return addr.String()
		}
	}
	t.Fatal("no local port that was not handed out before")
	return ""
}

// Echo listens on 127.0.0.1 and echoes what each connection sends, after
// banner, until the test ends. It returns its address.
func Echo(t testing.TB, banner string) string {
	t.Helper()
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
				if banner != "" {
					if _, err := c.Write([]byte(banner)); err != nil {
						return
					}
				}
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	return l.Addr().String()
}

// Stall listens on 127.0.0.1 and hands each connection it accepts to the
// test, which reads it as slowly as it likes. It returns the connections
// and its address.
func Stall(t testing.TB) (<-chan net.Conn, string) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	conns := make(chan net.Conn, 16)
	t.Cleanup(func() {
		_ = l.Close()
		for {
			select {
			case c := <-conns:
				_ = c.Close()
			default:
				return
			}
		}
	})
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			conns <- c
		}
	}()
	return conns, l.Addr().String()
}
