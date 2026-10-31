// Package tunneltest is what the tunnel's tests share: a fib engine that
// runs for a test, and the services tunnels are tested against.
package tunneltest

import (
	"io"
	"net"
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
