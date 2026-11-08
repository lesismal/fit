package tracker

import (
	"context"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fib"
	"github.com/lesismal/fib/bittorrent/metainfo"
	fibtracker "github.com/lesismal/fib/bittorrent/tracker"
	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/mediaauth"
	"github.com/lesismal/fit/module"
)

// hang bounds every wait of these tests: a guard against a hang, never a
// measure of speed, so it is generous for slow CI runners.
const hang = 90 * time.Second

// startEngine starts an engine the role shares, as fit's listeners' is.
func startEngine(t *testing.T) *fib.Engine {
	t.Helper()
	cfg := fib.DefaultConfig()
	cfg.LogSettings = false
	e, err := fib.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- e.Run() }()
	t.Cleanup(func() {
		e.Stop()
		<-done
		_ = e.Close()
	})
	return e
}

// load loads a config of the tracker member js, and more of the config's
// top-level fields in extra.
func load(t *testing.T, js, extra string) *config.Config {
	t.Helper()
	s := `{"bittorrent": {"tracker": ` + js + `}`
	if extra != "" {
		s += ", " + extra
	}
	c, err := config.Load([]byte(s + "}"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// startRole starts a role on e with the tracker member of c, applying c's
// media_auth policies first, as the app Manager would, and stops it when
// the test ends.
func startRole(t *testing.T, e *fib.Engine, c *config.Config) *Role {
	t.Helper()
	if len(c.MediaAuth) > 0 {
		if err := mediaauth.Apply(e, c.MediaAuth); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { mediaauth.Remove(e) })
	}
	r := New(&app.Host{ID: ID, Engine: e, Upstream: e})
	if err := r.Apply(c.BitTorrent.Tracker); err != nil {
		r.Stop(context.Background())
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Stop(context.Background()) })
	return r
}

// serveHTTP binds an HTTP listener on e whose handler is the bt_tracker
// module, as a route of fit's has it, and returns its base URL.
func serveHTTP(t *testing.T, e *fib.Engine) string {
	t.Helper()
	c, err := config.Load([]byte(`{"listeners": [{"name": "w", "address": ":0"}], "routes": [{"handler": {"module": "bt_tracker"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	m, err := module.Load(module.Handlers, c.Routes[0].Handler, &module.Env{Engine: e})
	if err != nil {
		t.Fatal(err)
	}
	h := m.(*Handler)
	i, err := e.AddBinding(fib.Binding{Addr: "127.0.0.1:0", Handler: fibhttp.NewHandler(h)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.RemoveBinding(i) })
	return "http://" + bindingAddr(t, e, i).String()
}

func bindingAddr(t *testing.T, e *fib.Engine, i int) netip.AddrPort {
	t.Helper()
	addrs, err := e.BindingAddrs(i)
	if err != nil || len(addrs) == 0 {
		t.Fatal(addrs, err)
	}
	ap, err := netip.ParseAddrPort(addrs[0].String())
	if err != nil {
		t.Fatal(err)
	}
	return ap
}

// newClient returns fib's tracker client on e, HTTP and UDP; UDP requests
// are sent retries times after the first, every timeout.
func newClient(t *testing.T, e *fib.Engine, timeout time.Duration, retries int) *fibtracker.Client {
	t.Helper()
	hc := fibhttp.NewClient(e, fibhttp.DefaultClientConfig())
	t.Cleanup(hc.Close)
	udp, err := fibtracker.ListenUDPClient(e, fibtracker.UDPClientConfig{Timeout: timeout, Retransmits: retries, Network: "udp4"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = udp.Close() })
	return &fibtracker.Client{HTTP: fibtracker.NewHTTPClient(hc), UDP: udp}
}

// udpURL is the UDP announce URL of r, with path, as "/announce".
func udpURL(r *Role, path string) string {
	return "udp://127.0.0.1:" + strconv.Itoa(int(r.UDPAddr().Port())) + path
}

func hashOf(s string) metainfo.InfoHash {
	var h metainfo.InfoHash
	copy(h[:], s)
	return h
}

func peerID(i int) fibtracker.PeerID {
	var id fibtracker.PeerID
	copy(id[:], fmt.Sprintf("-FB0001-%012d", i))
	return id
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), hang)
	t.Cleanup(cancel)
	return c
}

// announce announces peer i of h at port, with left bytes missing.
func announce(t *testing.T, cl *fibtracker.Client, u string, h metainfo.InfoHash, i int, left int64, ev fibtracker.Event) (*fibtracker.AnnounceResponse, error) {
	t.Helper()
	return cl.Announce(ctx(t), u, &fibtracker.AnnounceRequest{
		InfoHash: h, PeerID: peerID(i), Port: uint16(20000 + i), Left: left, Event: ev,
	})
}

// mustAnnounce is announce, failing the test on an error.
func mustAnnounce(t *testing.T, cl *fibtracker.Client, u string, h metainfo.InfoHash, i int, left int64, ev fibtracker.Event) *fibtracker.AnnounceResponse {
	t.Helper()
	resp, err := announce(t, cl, u, h, i, left, ev)
	if err != nil {
		t.Fatalf("announce of peer %d to %s: %v", i, u, err)
	}
	return resp
}

// refusedWith fails the test unless err is the tracker's refusal with a
// reason holding want.
func refusedWith(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("got %v, want a refusal with %q", err, want)
	}
}

// waitFor waits for cond, polling, failing the test after hang.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(hang)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
