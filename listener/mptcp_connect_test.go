package listener

import (
	"bufio"
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"slices"
	"testing"

	fib "github.com/lesismal/fib"

	"github.com/lesismal/fit/internal/mptcptest"
	"github.com/lesismal/fit/runtime"
)

// mptcpDoc is a config with one HTTP listener that answers "hello", its
// mptcp set as asked. auto_https is off so that the listener is the only one
// and the test dials its port alone.
func mptcpDoc(mptcp bool) string {
	field := ""
	if mptcp {
		field = `, "mptcp": true`
	}
	return `{"auto_https": {"disable": true},
  "listeners": [{"name": "web", "address": "127.0.0.1:0", "protocols": ["http/1.1"]` + field + `}],
  "routes": [{"handler": {"module": "respond", "params": {"body": "hello\n"}}}]}`
}

// skipUnlessMPTCP skips a test that can check Multipath TCP for real: this
// machine's kernel must offer it, and the fib this build uses must honor
// Binding.MultipathTCP. A kernel that offers it and a fib that ignores the
// field would give a plain connection that is fib's, not fit's, so the test
// skips rather than reads it as fit's fault.
func skipUnlessMPTCP(t *testing.T) {
	t.Helper()
	mptcptest.SkipUnlessSupported(t)
	if !fibMPTCP(t) {
		t.Skip("the fib this build uses does not honor Binding.MultipathTCP yet")
	}
}

// fibMPTCP reports whether the fib this build uses honors
// Binding.MultipathTCP: a binding that asks for it, dialed by a client that
// asks too, gives a connection the two sides agree is Multipath TCP. The
// listener tests ask fib itself, not how it was built, so they run whenever
// it honors the field.
func fibMPTCP(t *testing.T) bool {
	t.Helper()
	engine, err := fib.New(fib.DefaultConfig())
	if err != nil {
		t.Fatalf("fib.New: %v", err)
	}
	defer engine.Close()
	i, err := engine.AddBinding(fib.Binding{Addr: "127.0.0.1:0", Handler: mptcpNop{}, MultipathTCP: true})
	if err != nil {
		t.Fatalf("fib.Engine.AddBinding: %v", err)
	}
	addrs, err := engine.BindingAddrs(i)
	if err != nil || len(addrs) == 0 {
		t.Fatalf("fib.Engine.BindingAddrs: %v, %d addresses", err, len(addrs))
	}
	c, err := mptcptest.Dial(addrs[0].String())
	if err != nil {
		t.Fatalf("dial %s: %v", addrs[0], err)
	}
	defer c.Close()
	return mptcptest.InUse(c)
}

// mptcpNop is the probe binding's handler: the handshake is all the probe
// looks at.
type mptcpNop struct{}

func (mptcpNop) OnOpen(*fib.Connection)                 {}
func (mptcpNop) OnData(*fib.Connection, []byte)         {}
func (mptcpNop) OnPriorityData(*fib.Connection, []byte) {}
func (mptcpNop) OnClose(*fib.Connection, error)         {}

// mptcpDial dials addr asking for Multipath TCP, reports whether the
// connection uses it, and checks that the listener serves an HTTP request
// over it: the transport the handshake chose must be one fib serves as well.
func mptcpDial(t *testing.T, addr string) bool {
	t.Helper()
	c, err := mptcptest.Dial(addr)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	defer c.Close()
	using := mptcptest.InUse(c)
	if status, body := mptcpGet(t, c, addr); status != 200 || body != "hello\n" {
		t.Fatalf("GET / over the connection: %d %q", status, body)
	}
	return using
}

// mptcpGet sends an HTTP/1.1 request for / over c and returns the response's
// status and body.
func mptcpGet(t *testing.T, c net.Conn, host string) (int, string) {
	t.Helper()
	if _, err := fmt.Fprintf(c, "GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", host); err != nil {
		t.Fatal(err)
	}
	resp, err := stdhttp.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}

// TestListenerGraphMPTCP: the graph carries a listener's mptcp from the
// config to the runtime listener, which is what the server binds by. It runs
// anywhere, kernel support or not.
func TestListenerGraphMPTCP(t *testing.T) {
	for _, tt := range []struct {
		name  string
		mptcp bool
		want  bool
	}{
		{"MPTCP set in the config", true, true},
		{"MPTCP left out", false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			g := compile(t, mptcpDoc(tt.mptcp))
			defer g.Release()
			if len(g.Listeners) != 1 {
				t.Fatalf("%d listeners, want 1", len(g.Listeners))
			}
			if got := g.Listeners[0].MPTCP; got != tt.want {
				t.Fatalf("runtime.Listener.MPTCP = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestBindingSameMPTCP: a binding made for one mptcp setting does not serve
// the other, so a reload that changes it binds the listener anew instead of
// keeping a socket whose transport was fixed when it was made. The test
// needs neither a kernel nor fib support: it is same's rule alone.
func TestBindingSameMPTCP(t *testing.T) {
	l := &runtime.Listener{Name: "web", Address: "127.0.0.1:0", Protocols: []string{"http/1.1"}, MPTCP: true}
	b := &binding{index: -1, udpIndex: -1, name: l.Name, address: l.Address, protocols: l.Protocols, mptcp: true}
	if !b.same(l) {
		t.Fatal("a binding made for an MPTCP listener does not serve it")
	}
	plain := *l
	plain.MPTCP = false
	if b.same(&plain) {
		t.Fatal("a binding made for an MPTCP listener serves a plain one: a reload that turned mptcp off would keep the MPTCP socket")
	}
	b.mptcp = false
	if !b.same(&plain) {
		t.Fatal("a plain binding does not serve a plain listener")
	}
	if b.same(l) {
		t.Fatal("a plain binding serves an MPTCP listener: a reload that turned mptcp on would keep the plain socket")
	}
}

// TestListenerMPTCP: a listener whose config sets mptcp takes an MPTCP
// client's connection as MPTCP, and one without it takes the same client as
// plain TCP. Skips where the kernel or fib cannot make the difference.
func TestListenerMPTCP(t *testing.T) {
	skipUnlessMPTCP(t)
	for _, tt := range []struct {
		name  string
		mptcp bool
		want  bool
	}{
		{"MPTCP listener", true, true},
		{"MPTCP left off", false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, s := start(t, mptcpDoc(tt.mptcp))
			addr := addrOf(s, "web")
			if addr == "" {
				t.Fatal("the graph has no web listener")
			}
			if got := mptcpDial(t, addr); got != tt.want {
				t.Fatalf("Multipath TCP in use = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestListenerReloadMPTCP: a reload that flips mptcp binds the listener
// anew, and the new one behaves as the new setting says. The old binding is
// removed as the reload returns (no drain), so no socket of the old
// transport can take the next dial.
func TestListenerReloadMPTCP(t *testing.T) {
	skipUnlessMPTCP(t)
	_, s := start(t, mptcpDoc(false))
	s.SetDrainTimeout(0)
	if mptcpDial(t, addrOf(s, "web")) {
		t.Fatal("a listener without mptcp took an MPTCP connection as MPTCP")
	}

	added, removed, err := s.Reload(compile(t, mptcpDoc(true)))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(names(added), []string{"web"}) || !slices.Equal(names(removed), []string{"web"}) {
		t.Fatalf("turning mptcp on added %v and removed %v, want the listener bound anew", names(added), names(removed))
	}
	if !mptcpDial(t, addrOf(s, "web")) {
		t.Fatal("after a reload that turned mptcp on, a new MPTCP dial is plain TCP: the binding was kept")
	}

	if _, _, err := s.Reload(compile(t, mptcpDoc(false))); err != nil {
		t.Fatal(err)
	}
	if mptcpDial(t, addrOf(s, "web")) {
		t.Fatal("after a reload that turned mptcp off, a new MPTCP dial is in use: the binding was kept")
	}
}
