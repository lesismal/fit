// Package tunneltest is what the tunnel's tests share: a fib engine that
// runs for a test, the services tunnels are tested against, the free
// addresses they listen on, and the app.Host a role is made on.
package tunneltest

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/lesismal/fib"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
)

// HostRole is the ID of the role RegisterHostRole registers.
const HostRole = "test.host"

// hosts are the app.Hosts Managers made for HostRole, by engine.
var hosts struct {
	sync.Mutex
	m map[*fib.Engine]*app.Host
}

type hostRole struct{}

func (hostRole) Apply(any) error      { return nil }
func (hostRole) Stop(context.Context) {}

// RegisterHostRole registers the role test.host, which every config has
// and which runs nothing: it keeps the app.Host a Manager makes for it,
// for Host to hand a test, that makes a role of its own on it. A test
// package calls it from its init.
func RegisterHostRole() {
	app.Register(app.Info{ID: HostRole, Member: func(*config.Config) any { return struct{}{} },
		New: func(h *app.Host) (app.Role, error) {
			hosts.Lock()
			defer hosts.Unlock()
			if hosts.m == nil {
				hosts.m = map[*fib.Engine]*app.Host{}
			}
			hosts.m[h.Engine] = h
			return hostRole{}, nil
		}})
}

// Host takes the app.Host the Manager on e made for test.host, as it
// applied a config. The hosts are kept by their engine, a test's own, so
// that a host a test left behind, as a start that failed after the Manager
// made it, is never another test's.
func Host(t testing.TB, e *fib.Engine) *app.Host {
	t.Helper()
	hosts.Lock()
	defer hosts.Unlock()
	h := hosts.m[e]
	if h == nil {
		t.Fatal("no app.Host made on the engine")
	}
	delete(hosts.m, e)
	return h
}

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

// handed is every port FreeAddr and FreeUDPAddr have handed out in this
// process, by network.
var handed struct {
	sync.Mutex
	ports map[string]map[string]bool
}

// FreeAddr is a local TCP address nothing listens on, whose port no other
// call in this process has handed out. The kernel may give a port it gave
// before, once that listener closed; and fib's engines listen with
// SO_REUSEPORT, so two bindings handed the same port both listen on it
// without an error, and the kernel spreads connections between them: a
// test would talk to one server when it means the other.
//
// The port is let go of before it is returned, so another process, as
// another package's tests run beside these, may take it before the test
// binds it: a test that binds it starts with Start, which tries again
// with other addresses.
func FreeAddr(t testing.TB) string {
	t.Helper()
	return free(t, "tcp")
}

// FreeUDPAddr is FreeAddr for UDP.
func FreeUDPAddr(t testing.TB) string {
	t.Helper()
	return free(t, "udp")
}

func free(t testing.TB, network string) string {
	t.Helper()
	handed.Lock()
	defer handed.Unlock()
	if handed.ports == nil {
		handed.ports = map[string]map[string]bool{}
	}
	ports := handed.ports[network]
	if ports == nil {
		ports = map[string]bool{}
		handed.ports[network] = ports
	}
	// A port handed out before stays held while the kernel is asked
	// again, so that it is not given back at once.
	var held []io.Closer
	defer func() {
		for _, l := range held {
			_ = l.Close()
		}
	}()
	for range 1000 {
		var addr string
		if network == "udp" {
			pc, err := net.ListenPacket("udp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			held, addr = append(held, pc), pc.LocalAddr().String()
		} else {
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			held, addr = append(held, l), l.Addr().String()
		}
		if !ports[addr] {
			ports[addr] = true
			return addr
		}
	}
	t.Fatal("no local port that was not handed out before")
	return ""
}

// Taken reports whether err is a binding's that found its address held:
// fit's own check before it binds, or the bind's EADDRINUSE.
func Taken(err error) bool {
	return err != nil && (errors.Is(err, syscall.EADDRINUSE) || IsTakenText(err.Error()))
}

// IsTakenText reports whether s, an error as a role's pages answer it, says
// an address was taken.
func IsTakenText(s string) bool {
	return strings.Contains(s, " is taken: ") || strings.Contains(s, "address already in use") ||
		strings.Contains(s, "Only one usage of each socket address")
}

// Attempts is how many times Start tries.
const Attempts = 5

// Start returns what try starts. try picks its addresses with FreeAddr,
// so another process may take one before it binds it: then try, having
// undone what it started, returns the error, and Start tries again, with
// the addresses FreeAddr picks anew, up to Attempts times. Another error
// fails the test.
func Start[T any](t testing.TB, try func() (T, error)) T {
	t.Helper()
	for attempt := 1; ; attempt++ {
		v, err := try()
		if err == nil {
			return v
		}
		if !Taken(err) || attempt == Attempts {
			t.Fatal(err)
		}
		t.Logf("attempt %d: %v; trying other addresses", attempt, err)
	}
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

// Bind returns a free address on network, "tcp" or "udp", that try took:
// try binds what it is testing on the address, and returns an error that
// says so when it was taken, after undoing what it did, to be tried again
// on another.
func Bind(t testing.TB, network string, try func(addr string) error) string {
	t.Helper()
	return Start(t, func() (string, error) {
		addr := free(t, network)
		return addr, try(addr)
	})
}
