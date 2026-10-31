package turn

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fib"
	"github.com/lesismal/fib/webrtc/stun"
	fibturn "github.com/lesismal/fib/webrtc/turn"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/observe"
)

const (
	realm  = "fit.test"
	secret = "a-shared-secret"
)

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

// member is a TURN server on loopback that relays to loopback peers.
func member() *config.TURNServer {
	return &config.TURNServer{
		Listen:       []string{"127.0.0.1:0"},
		Realm:        realm,
		Users:        []config.TURNUser{{Name: "alice", Password: "pw"}},
		Secret:       secret,
		RelayIP:      "127.0.0.1",
		AllowedPeers: []string{"127.0.0.0/8"},
	}
}

func startRole(t *testing.T, e *fib.Engine, m *config.TURNServer) *Server {
	t.Helper()
	s := New(&app.Host{ID: ID, Engine: e})
	if err := s.Apply(m); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Stop(context.Background()) })
	return s
}

func serverAddr(t *testing.T, s *Server) netip.AddrPort {
	t.Helper()
	addrs := s.Addrs()
	if len(addrs) == 0 {
		t.Fatal("the server listens nowhere")
	}
	ap := addrs[0].(*net.UDPAddr).AddrPort()
	return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
}

// client drives fib's turn.Client over a UDP socket of its own, or a TCP
// or TLS connection: a goroutine reads it, another fires the client's
// timers, and a mutex serializes them.
type client struct {
	mu        sync.Mutex
	c         *fibturn.Client
	conn      net.Conn
	got       chan string
	allocated chan struct{}
	failed    chan error
	done      chan struct{}
	closeOnce sync.Once
}

func newClient(t *testing.T, server netip.AddrPort, user, pass string, bind bool) *client {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	u := startClient(t, conn, server, user, pass, bind, func(to netip.AddrPort, b []byte) { _, _ = conn.WriteToUDPAddrPort(b, to) })
	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, err := conn.ReadFromUDPAddrPort(buf)
			if err != nil {
				return
			}
			u.handle(from, buf[:n])
		}
	}()
	return u
}

// newStreamClient is newClient over conn, a TCP or TLS connection to
// server, whose messages it frames with fib's FrameLen.
func newStreamClient(t *testing.T, conn net.Conn, server netip.AddrPort, user, pass string, bind bool) *client {
	t.Helper()
	u := startClient(t, conn, server, user, pass, bind, nil)
	go func() {
		var buf []byte
		chunk := make([]byte, 4096)
		for {
			n, err := conn.Read(chunk)
			if err != nil {
				return
			}
			buf = append(buf, chunk[:n]...)
			for {
				n, err := fibturn.FrameLen(buf)
				if err != nil {
					_ = conn.Close()
					return
				}
				if n == 0 || n > len(buf) {
					break
				}
				u.handle(server, buf[:n])
				buf = buf[n:]
			}
		}
	}()
	return u
}

func startClient(t *testing.T, conn net.Conn, server netip.AddrPort, user, pass string, bind bool, send func(netip.AddrPort, []byte)) *client {
	t.Helper()
	u := &client{conn: conn, got: make(chan string, 1024), allocated: make(chan struct{}), failed: make(chan error, 1), done: make(chan struct{})}
	stream := send == nil
	if stream {
		send = func(_ netip.AddrPort, b []byte) { _, _ = conn.Write(b) }
	}
	c, err := fibturn.NewClient(fibturn.ClientConfig{
		Server: server, Username: user, Password: pass, BindChannels: bind, Stream: stream,
		Send:        send,
		OnAllocated: func(_, _ netip.AddrPort) { close(u.allocated) },
		OnFailed: func(err error) {
			select {
			case u.failed <- err:
			default:
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	u.c = c
	go func() {
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-u.done:
				return
			case now := <-tick.C:
				u.mu.Lock()
				if d, ok := c.NextTimeout(); ok && !d.After(now) {
					c.HandleTimeout(now)
				}
				u.mu.Unlock()
			}
		}
	}()
	t.Cleanup(u.cut)
	return u
}

// handle passes a message from the server to the client.
func (u *client) handle(from netip.AddrPort, b []byte) {
	u.mu.Lock()
	defer u.mu.Unlock()
	_, data, _ := u.c.HandlePacket(from, b, time.Now())
	if data != nil {
		select {
		case u.got <- string(data):
		default:
		}
	}
}

// cut closes the client's socket without deallocating, as a client that
// vanishes does.
func (u *client) cut() {
	u.closeOnce.Do(func() {
		close(u.done)
		_ = u.conn.Close()
	})
}

func (u *client) do(f func(c *fibturn.Client)) {
	u.mu.Lock()
	defer u.mu.Unlock()
	f(u.c)
}

// allocate asks for an allocation and returns its relayed address, or the
// error it failed with.
func (u *client) allocate(t *testing.T) (netip.AddrPort, error) {
	t.Helper()
	u.do(func(c *fibturn.Client) { _ = c.Allocate(time.Now()) })
	select {
	case <-u.allocated:
	case err := <-u.failed:
		return netip.AddrPort{}, err
	case <-time.After(10 * time.Second):
		t.Fatal("no allocation, and no failure")
	}
	var relayed netip.AddrPort
	u.do(func(c *fibturn.Client) { relayed, _, _ = c.Relayed() })
	return relayed, nil
}

func (u *client) mustAllocate(t *testing.T) netip.AddrPort {
	t.Helper()
	relayed, err := u.allocate(t)
	if err != nil {
		t.Fatal(err)
	}
	return relayed
}

type peer struct {
	conn *net.UDPConn
	addr netip.AddrPort
}

func newPeer(t *testing.T) *peer {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &peer{conn: conn, addr: conn.LocalAddr().(*net.UDPAddr).AddrPort()}
}

// exchange sends msg from u to p through the relay, and p's echo back.
func exchange(t *testing.T, u *client, p *peer, relayed netip.AddrPort, msg string) {
	t.Helper()
	buf := make([]byte, 2048)
	var (
		n    int
		from netip.AddrPort
		err  error
	)
	// The first datagrams may beat their permission or channel; send
	// until one arrives.
	for range 50 {
		u.do(func(c *fibturn.Client) { _ = c.SendTo(p.addr, []byte(msg)) })
		_ = p.conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		if n, from, err = p.conn.ReadFromUDPAddrPort(buf); err == nil && string(buf[:n]) == msg {
			break
		}
	}
	if err != nil || string(buf[:n]) != msg || from != relayed {
		t.Fatalf("the peer read %q from %v (relayed %v): %v", buf[:n], from, relayed, err)
	}
	if _, err := p.conn.WriteToUDPAddrPort([]byte("echo:"+msg), relayed); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case got := <-u.got:
			if got == "echo:"+msg {
				return
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("no echo of %q", msg)
		}
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func code(err error) int {
	var e *fibturn.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return 0
}

// allow lets peers on the role's own address, relay_ip, be reached on
// their ports, as allowed_peers lists them with their port: the port-aware
// check allows it on its relayed ports alone otherwise.
func allow(t *testing.T, s *Server, peers ...*peer) {
	t.Helper()
	s.mu.Lock()
	m := *s.cur.cfg
	s.mu.Unlock()
	m.AllowedPeers = slices.Clone(m.AllowedPeers)
	for _, p := range peers {
		m.AllowedPeers = append(m.AllowedPeers, p.addr.String())
	}
	if err := s.Apply(&m); err != nil {
		t.Fatal(err)
	}
}

func metrics(s *Server) string {
	var b observe.TextWriter
	s.WriteMetrics(&b)
	return string(b.B)
}

// metric returns the value of the sample named, labels and all, as in
// fit_turn_freed_total{reason="deleted"}, or -1 without one.
func metric(s *Server, sample string) float64 {
	m := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(sample) + ` (\S+)$`).FindStringSubmatch(metrics(s))
	if m == nil {
		return -1
	}
	v, _ := strconv.ParseFloat(m[1], 64)
	return v
}

func TestRelay(t *testing.T) {
	e := startEngine(t)
	keyed := member()
	keyed.Users = append(keyed.Users, config.TURNUser{Name: "bob", Key: "0x" + hexKey("bob", realm, "bobpw")})
	s := startRole(t, e, keyed)
	addr := serverAddr(t, s)
	ephemeralUser, ephemeralPass := fibturn.EphemeralCredentials([]byte(secret), "carol", time.Now().Add(time.Hour))
	for _, tc := range []struct {
		name, user, pass string
		bind             bool
	}{
		{"password, indications", "alice", "pw", false},
		{"password, channels", "alice", "pw", true},
		{"key", "bob", "bobpw", true},
		{"secret", ephemeralUser, ephemeralPass, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := newClient(t, addr, tc.user, tc.pass, tc.bind)
			relayed := u.mustAllocate(t)
			if relayed.Addr() != netip.MustParseAddr("127.0.0.1") {
				t.Fatalf("relayed %v", relayed)
			}
			p := newPeer(t)
			allow(t, s, p)
			for _, msg := range []string{"one", "two", strings.Repeat("x", 1200)} {
				exchange(t, u, p, relayed, msg)
			}
			u.do(func(c *fibturn.Client) { c.Close(time.Now()) })
			eventually(t, "the allocation's end", func() bool { return s.Allocations() == 0 })
		})
	}
	m := metrics(s)
	for _, want := range []string{"fit_turn_allocations 0", `fit_turn_relayed_bytes_total{direction="to_peer"}`, "fit_turn_restarts_total 0",
		"fit_turn_allocated_total 4", `fit_turn_freed_total{reason="deleted"} 4`, "fit_turn_permissions 0", "fit_turn_draining_servers 0"} {
		if !strings.Contains(m, want) {
			t.Errorf("metrics lack %s:\n%s", want, m)
		}
	}
	if s.toPeer.Load() == 0 || s.fromPeer.Load() == 0 {
		t.Errorf("relayed %d bytes to peers and %d from them", s.toPeer.Load(), s.fromPeer.Load())
	}
}

func hexKey(user, realm, pass string) string {
	const digits = "0123456789abcdef"
	k := stun.LongTermKey(user, realm, pass)
	b := make([]byte, 0, 2*len(k))
	for _, c := range k {
		b = append(b, digits[c>>4], digits[c&15])
	}
	return string(b)
}

func TestRefusedCredentials(t *testing.T) {
	e := startEngine(t)
	s := startRole(t, e, member())
	addr := serverAddr(t, s)
	expiredUser, expiredPass := fibturn.EphemeralCredentials([]byte(secret), "carol", time.Now().Add(-time.Minute))
	otherUser, otherPass := fibturn.EphemeralCredentials([]byte("another secret"), "carol", time.Now().Add(time.Hour))
	for _, tc := range []struct{ name, user, pass string }{
		{"wrong password", "alice", "nope"},
		{"unknown user", "mallory", "pw"},
		{"expired", expiredUser, expiredPass},
		{"another secret", otherUser, otherPass},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := newClient(t, addr, tc.user, tc.pass, false)
			if _, err := u.allocate(t); err == nil {
				t.Fatal("allocated")
			}
		})
	}
	if s.Allocations() != 0 {
		t.Fatalf("%d allocations", s.Allocations())
	}
	if n := metric(s, "fit_turn_auth_failures_total"); n < 4 {
		t.Fatalf("%v auth failures counted", n)
	}
}

func TestQuotas(t *testing.T) {
	e := startEngine(t)
	m := member()
	m.Users = append(m.Users, config.TURNUser{Name: "bob", Password: "pw"})
	m.MaxAllocations = 2
	m.MaxAllocationsPerUser = 1
	s := startRole(t, e, m)
	addr := serverAddr(t, s)
	newClient(t, addr, "alice", "pw", false).mustAllocate(t)
	if _, err := newClient(t, addr, "alice", "pw", false).allocate(t); code(err) != 486 {
		t.Fatalf("alice's second allocation: %v, want 486", err)
	}
	newClient(t, addr, "bob", "pw", false).mustAllocate(t)
	user, pass := fibturn.EphemeralCredentials([]byte(secret), "carol", time.Now().Add(time.Hour))
	if _, err := newClient(t, addr, user, pass, false).allocate(t); code(err) != 508 {
		t.Fatalf("a third allocation: %v, want 508", err)
	}
	if metric(s, `fit_turn_refused_total{code="486"}`) != 1 || metric(s, `fit_turn_refused_total{code="508"}`) != 1 {
		t.Fatalf("refusals:\n%s", metrics(s))
	}
}

// permErr sends to peer until its permission's refusal shows, and returns
// it, or nil if none shows within a second.
func permErr(u *client, peer netip.AddrPort) error {
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		var err error
		u.do(func(c *fibturn.Client) { err = c.SendTo(peer, []byte("x")) })
		if err != nil {
			return err
		}
		time.Sleep(20 * time.Millisecond)
	}
	return nil
}

// TestCaps checks max_permissions and max_channels: one more of either is
// refused with 508, and counted.
func TestCaps(t *testing.T) {
	e := startEngine(t)
	m := member()
	m.MaxPermissions = 2
	m.MaxChannels = 1
	s := startRole(t, e, m)
	addr := serverAddr(t, s)
	u := newClient(t, addr, "alice", "pw", false)
	u.mustAllocate(t)
	for _, ip := range []string{"127.0.0.2", "127.0.0.3"} {
		u.do(func(c *fibturn.Client) { _ = c.CreatePermission(netip.MustParseAddr(ip), time.Now()) })
	}
	eventually(t, "two permissions", func() bool { return metric(s, "fit_turn_permissions") == 2 })
	if err := permErr(u, netip.MustParseAddrPort("127.0.0.4:9")); code(err) != 508 {
		t.Fatalf("a third permission: %v, want 508", err)
	}

	v := newClient(t, addr, "alice", "pw", true)
	v.mustAllocate(t)
	for _, peer := range []string{"127.0.0.2:9", "127.0.0.3:9"} {
		v.do(func(c *fibturn.Client) { _ = c.SendTo(netip.MustParseAddrPort(peer), []byte("x")) })
	}
	eventually(t, "the second channel refused", func() bool {
		return metric(s, `fit_turn_refused_total{code="508"}`) == 2 && metric(s, "fit_turn_channels") == 1
	})

	// A cap raised in place lets the next request through.
	m2 := *m
	m2.MaxPermissions = 3
	if err := s.Apply(&m2); err != nil {
		t.Fatal(err)
	}
	u.do(func(c *fibturn.Client) { _ = c.CreatePermission(netip.MustParseAddr("127.0.0.5"), time.Now()) })
	eventually(t, "a third permission after the reload", func() bool { return metric(s, "fit_turn_permissions") == 5 })
	if s.restarts.Load() != 0 {
		t.Fatal("the cap's change restarted the server")
	}
}

func TestBandwidth(t *testing.T) {
	e := startEngine(t)
	m := member()
	m.UserBandwidth = 4000
	s := startRole(t, e, m)
	u := newClient(t, serverAddr(t, s), "alice", "pw", true)
	relayed := u.mustAllocate(t)
	p := newPeer(t)
	allow(t, s, p)
	exchange(t, u, p, relayed, "warm")
	// The burst is a second's worth: 4000 bytes. Send 40 KB at once.
	chunk := strings.Repeat("y", 1000)
	for range 40 {
		u.do(func(c *fibturn.Client) { _ = c.SendTo(p.addr, []byte(chunk)) })
	}
	got := 0
	buf := make([]byte, 2048)
	for {
		_ = p.conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		n, _, err := p.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			break
		}
		got += n
	}
	// What the burst held, and what refilled while the datagrams arrived.
	if got == 0 || got > 8000 {
		t.Fatalf("the peer got %d bytes", got)
	}
	if s.dropped.Load() == 0 {
		t.Fatal("nothing dropped")
	}
}

func TestPrivatePeers(t *testing.T) {
	e := startEngine(t)
	m := member()
	m.AllowedPeers = nil
	s := startRole(t, e, m)
	addr := serverAddr(t, s)
	u := newClient(t, addr, "alice", "pw", false)
	u.mustAllocate(t)
	if err := permErr(u, netip.MustParseAddrPort("127.0.0.2:9")); code(err) != 403 {
		t.Fatalf("a permission for a loopback peer: %v, want 403", err)
	}
	if metric(s, "fit_turn_peers_refused_total") != 1 {
		t.Fatalf("refusals:\n%s", metrics(s))
	}

	// denied_peers refuses what allowed_peers does not list first.
	m = member()
	m.AllowedPeers = []string{"127.0.0.3"}
	m.DeniedPeers = []string{"127.0.0.0/8"}
	if err := s.Apply(m); err != nil {
		t.Fatal(err)
	}
	u = newClient(t, addr, "alice", "pw", false)
	u.mustAllocate(t)
	if err := permErr(u, netip.MustParseAddrPort("127.0.0.3:9")); err != nil {
		t.Fatalf("an allowed peer: %v", err)
	}
	if err := permErr(u, netip.MustParseAddrPort("127.0.0.2:9")); code(err) != 403 {
		t.Fatalf("a denied peer: %v, want 403", err)
	}
}

// TestOwnAddress checks the port-aware peer check: relay_ip, though
// allowed_peers covers it, is relayed to on the server's relayed ports
// alone, which two of its clients use to reach each other, and on a port
// allowed_peers lists with it; never on its other ports, where fit's own
// listeners are.
func TestOwnAddress(t *testing.T) {
	e := startEngine(t)
	s := startRole(t, e, member())
	addr := serverAddr(t, s)
	u := newClient(t, addr, "alice", "pw", true)
	relayed := u.mustAllocate(t)
	p := newPeer(t)
	buf := make([]byte, 2048)
	for range 20 {
		u.do(func(c *fibturn.Client) { _ = c.SendTo(p.addr, []byte("in")) })
		_ = p.conn.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
		if n, _, err := p.conn.ReadFromUDPAddrPort(buf); err == nil {
			t.Fatalf("a port of relay_ip that is not a relay got %q", buf[:n])
		}
	}
	if metric(s, "fit_turn_unrelayed_total") <= 0 {
		t.Fatalf("nothing counted:\n%s", metrics(s))
	}
	allow(t, s, p)
	exchange(t, u, p, relayed, "listed")

	// Two clients relay to each other's relayed addresses.
	v := newClient(t, addr, "alice", "pw", false)
	vRelayed := v.mustAllocate(t)
	v.do(func(c *fibturn.Client) { _ = c.CreatePermission(relayed.Addr(), time.Now()) })
	deadline := time.Now().Add(10 * time.Second)
	for {
		u.do(func(c *fibturn.Client) { _ = c.SendTo(vRelayed, []byte("hello v")) })
		select {
		case got := <-v.got:
			if got != "hello v" {
				t.Fatalf("v got %q", got)
			}
			return
		case <-time.After(50 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatal("one client's relay did not reach the other's")
		}
	}
}

// TestPeerAllowed checks the peer rules' order and ports without a
// network.
func TestPeerAllowed(t *testing.T) {
	core, err := fibturn.NewServer(fibturn.ServerConfig{Realm: realm,
		Auth: func(string, string) ([]byte, bool) { return nil, false },
		Send: func(fibturn.FiveTuple, []byte) {},
		AllocateRelay: func(_ fibturn.FiveTuple, _ string, _ bool, done func(fibturn.Relay, error)) {
			done(nil, errors.New("no"))
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := New(&app.Host{ID: ID})
	s.live.Store(&[]*running{{core: core, relayIP: netip.MustParseAddr("10.0.0.5"), publicIP: netip.MustParseAddr("203.0.113.5")}})
	m := member()
	m.AllowedPeers = []string{"10.0.0.0/8", "10.0.0.5:8189", "192.168.1.1:5000"}
	m.DeniedPeers = []string{"8.8.8.8:53", "9.9.9.0/24"}
	r, err := newRules(m)
	if err != nil {
		t.Fatal(err)
	}
	s.rules.Store(r)
	for _, tc := range []struct {
		peer string
		want bool
	}{
		{"10.0.0.5:0", true},        // a permission on the own address
		{"10.0.0.5:8080", false},    // not a relayed port, whatever 10/8 says
		{"203.0.113.5:8080", false}, // the public address alike
		{"10.0.0.5:8189", true},     // listed with its port
		{"[::ffff:10.0.0.5]:8189", true},
		{"10.1.2.3:80", true},   // by the prefix
		{"192.168.1.1:0", true}, // a port of it is allowed
		{"192.168.1.1:5000", true},
		{"192.168.1.1:5001", false}, // denied by default
		{"192.168.1.2:0", false},
		{"8.8.8.8:53", false},
		{"8.8.8.8:54", true},
		{"8.8.8.8:0", true},
		{"9.9.9.9:0", false},
	} {
		if got := s.peerAllowed(netip.MustParseAddrPort(tc.peer)); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.peer, got, tc.want)
		}
	}
}

func TestReload(t *testing.T) {
	e := startEngine(t)
	m := member()
	s := startRole(t, e, m)
	s.drainTimeout = 10 * time.Second
	addr := serverAddr(t, s)
	u := newClient(t, addr, "alice", "pw", true)
	relayed := u.mustAllocate(t)
	p := newPeer(t)
	allow(t, s, p)
	exchange(t, u, p, relayed, "before")

	// Users, the secret, the peers, the bandwidth, the lifetimes and the
	// quotas change in place.
	m2 := member()
	m2.Users = []config.TURNUser{{Name: "dave", Password: "pw2"}, {Name: "erin", Password: "pw3"}}
	m2.Secret = ""
	m2.AllowedPeers = append(m2.AllowedPeers, p.addr.String())
	m2.UserBandwidth = 1 << 20
	m2.MaxAllocations = 10
	m2.MaxAllocationsPerUser = 1
	m2.DefaultLifetime = config.Duration(5 * time.Minute)
	if err := s.Apply(m2); err != nil {
		t.Fatal(err)
	}
	if s.restarts.Load() != 0 || s.Allocations() != 1 {
		t.Fatalf("%d restarts, %d allocations", s.restarts.Load(), s.Allocations())
	}
	s.mu.Lock()
	lim := s.cur.core.Limits()
	s.mu.Unlock()
	if lim.MaxAllocations != 10 || lim.MaxAllocationsPerUser != 1 || lim.DefaultLifetime != 5*time.Minute {
		t.Fatalf("limits %+v", lim)
	}
	exchange(t, u, p, relayed, "after")
	if _, err := newClient(t, addr, "alice", "pw", false).allocate(t); err == nil {
		t.Fatal("a user taken out still allocates")
	}
	newClient(t, addr, "dave", "pw2", false).mustAllocate(t)
	if _, err := newClient(t, addr, "dave", "pw2", false).allocate(t); code(err) != 486 {
		t.Fatalf("dave's second allocation: %v, want 486", err)
	}

	// A realm on the same address starts the server again: the two cannot
	// share it, and the allocations end.
	m3 := *m2
	m3.Listen = []string{addr.String()}
	m3.Realm = "other.test"
	if err := s.Apply(&m3); err != nil {
		t.Fatal(err)
	}
	if s.restarts.Load() != 1 || s.Allocations() != 0 || serverAddr(t, s) != addr {
		t.Fatalf("%d restarts, %d allocations, at %v", s.restarts.Load(), s.Allocations(), serverAddr(t, s))
	}
	c := newClient(t, addr, "dave", "pw2", true)
	cRelayed := c.mustAllocate(t)
	exchange(t, c, p, cRelayed, "restarted")

	// Another address: the new server listens there, and the old one
	// drains, relaying and refreshing for the allocation it holds and
	// refusing new ones, until that one ends.
	m4 := m3
	m4.Listen = []string{"127.0.0.1:0"}
	if err := s.Apply(&m4); err != nil {
		t.Fatal(err)
	}
	moved := serverAddr(t, s)
	if moved == addr || s.restarts.Load() != 2 {
		t.Fatalf("at %v, %d restarts", moved, s.restarts.Load())
	}
	if metric(s, "fit_turn_draining_servers") != 1 || metric(s, "fit_turn_allocations") != 1 {
		t.Fatalf("metrics:\n%s", metrics(s))
	}
	exchange(t, c, p, cRelayed, "draining")
	if _, err := newClient(t, addr, "erin", "pw3", false).allocate(t); code(err) != 508 {
		t.Fatalf("an allocation on the server draining: %v, want 508", err)
	}
	n := newClient(t, moved, "dave", "pw2", true)
	exchange(t, n, p, n.mustAllocate(t), "moved")
	if free, err := net.ListenPacket("udp", addr.String()); err == nil {
		_ = free.Close()
		t.Fatal("the old address is free while it drains")
	}
	c.do(func(c *fibturn.Client) { c.Close(time.Now()) })
	eventually(t, "the drain's end", func() bool {
		free, err := net.ListenPacket("udp", addr.String())
		if err != nil {
			return false
		}
		_ = free.Close()
		return metric(s, "fit_turn_draining_servers") == 0
	})
}

// TestDrainTimeout checks that a server draining closes at the drain
// timeout, ending the allocations it holds, and that a reload back onto
// its address ends it at once.
func TestDrainTimeout(t *testing.T) {
	e := startEngine(t)
	s := startRole(t, e, member())
	s.drainTimeout = 300 * time.Millisecond
	addr := serverAddr(t, s)
	newClient(t, addr, "alice", "pw", false).mustAllocate(t)
	moved := member()
	moved.NonceSecret = "another server"
	if err := s.Apply(moved); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the drain timed out", func() bool {
		return metric(s, `fit_turn_freed_total{reason="closed"}`) == 1 && metric(s, "fit_turn_draining_servers") == 0
	})

	s.drainTimeout = time.Hour
	at := serverAddr(t, s)
	newClient(t, at, "alice", "pw", false).mustAllocate(t)
	back := member()
	back.Listen = []string{addr.String()}
	if err := s.Apply(back); err != nil {
		t.Fatal(err)
	}
	// And back onto the address that drains.
	there := member()
	there.Listen = []string{at.String()}
	if err := s.Apply(there); err != nil {
		t.Fatalf("a reload onto the address of a server draining: %v", err)
	}
	if serverAddr(t, s) != at || metric(s, `fit_turn_freed_total{reason="closed"}`) != 2 {
		t.Fatalf("at %v:\n%s", serverAddr(t, s), metrics(s))
	}
	newClient(t, at, "alice", "pw", false).mustAllocate(t)
}

func TestReloadRefused(t *testing.T) {
	e := startEngine(t)
	s := startRole(t, e, member())
	addr := serverAddr(t, s)
	// An address another socket holds, over UDP and over TCP.
	taken, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	m := member()
	m.Listen = []string{taken.LocalAddr().String()}
	if err := s.Apply(m); err == nil {
		t.Fatal("listened on a taken address")
	}
	takenTCP, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer takenTCP.Close()
	m = member()
	m.ListenTCP = []string{takenTCP.Addr().String()}
	if err := s.Apply(m); err == nil || !strings.Contains(err.Error(), "tcp") {
		t.Fatalf("listened on a taken TCP address: %v", err)
	}
	if serverAddr(t, s) != addr {
		t.Fatal("the server moved")
	}
	newClient(t, addr, "alice", "pw", false).mustAllocate(t)
	s.Stop(context.Background())
	if err := s.Apply(member()); !errors.Is(err, app.ErrStopped) {
		t.Fatalf("Apply after Stop: %v", err)
	}
}

// TestCutClient checks that a client gone without deallocating, and a peer
// that floods its relayed address meanwhile, cost the server nothing past
// the allocation's lifetime, and hold up no other client.
func TestCutClient(t *testing.T) {
	e := startEngine(t)
	m := member()
	m.DefaultLifetime = config.Duration(2 * time.Second)
	m.MaxLifetime = config.Duration(2 * time.Second)
	s := startRole(t, e, m)
	addr := serverAddr(t, s)
	u := newClient(t, addr, "alice", "pw", true)
	relayed := u.mustAllocate(t)
	p, q := newPeer(t), newPeer(t)
	allow(t, s, p, q)
	exchange(t, u, p, relayed, "hello")
	u.cut()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		b := []byte(strings.Repeat("z", 1000))
		for {
			select {
			case <-stop:
				return
			default:
				_, _ = p.conn.WriteToUDPAddrPort(b, relayed)
			}
		}
	})
	other := newClient(t, addr, "alice", "pw", true)
	exchange(t, other, q, other.mustAllocate(t), "meanwhile")
	eventually(t, "the cut client's allocation to expire", func() bool { return s.Allocations() == 1 })
	close(stop)
	wg.Wait()
	eventually(t, "the other client's refresh", func() bool { return metric(s, "fit_turn_refreshed_total") >= 1 })
	if metric(s, `fit_turn_freed_total{reason="expired"}`) != 1 {
		t.Fatalf("metrics:\n%s", metrics(s))
	}
}

// certFiles writes a self-signed certificate for turn.test and 127.0.0.1
// to PEM files in dir, and returns their paths and a pool that trusts it.
func certFiles(t *testing.T, dir string) (certFile, keyFile string, pool *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "turn.test"},
		DNSNames: []string{"turn.test"}, IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool = x509.NewCertPool()
	pool.AddCert(cert)
	return certFile, keyFile, pool
}

func addrPort(t *testing.T, a net.Addr) netip.AddrPort {
	t.Helper()
	ap, err := netip.ParseAddrPort(a.String())
	if err != nil {
		t.Fatal(err)
	}
	return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
}

// TestStream checks TURN over TCP and TLS through fit, with fib's client
// framing its messages: allocations relay UDP to a peer both ways, end
// when their connection closes, a connection idle without one is closed,
// and a certificate changed on reload is the next handshake's.
func TestStream(t *testing.T) {
	e := startEngine(t)
	dir := t.TempDir()
	certFile, keyFile, pool := certFiles(t, dir)
	m := member()
	m.ListenTCP = []string{"127.0.0.1:0"}
	m.ListenTLS = []string{"127.0.0.1:0"}
	m.TLS = &config.TURNTLS{Certificates: []config.Certificate{{CertFile: certFile, KeyFile: keyFile}}}
	m.StreamIdleTimeout = config.Duration(300 * time.Millisecond)
	s := startRole(t, e, m)
	tcpAddrs, tlsAddrs := s.StreamAddrs()
	if len(tcpAddrs) != 1 || len(tlsAddrs) != 1 {
		t.Fatalf("tcp %v, tls %v", tcpAddrs, tlsAddrs)
	}
	tcpAddr, tlsAddr := addrPort(t, tcpAddrs[0]), addrPort(t, tlsAddrs[0])
	p := newPeer(t)
	allow(t, s, p)

	tcpConn, err := net.Dial("tcp", tcpAddr.String())
	if err != nil {
		t.Fatal(err)
	}
	overTCP := newStreamClient(t, tcpConn, tcpAddr, "alice", "pw", true)
	exchange(t, overTCP, p, overTCP.mustAllocate(t), "over tcp")

	dialTLS := func(pool *x509.CertPool) (*tls.Conn, error) {
		return tls.Dial("tcp", tlsAddr.String(), &tls.Config{RootCAs: pool, ServerName: "turn.test"})
	}
	tlsConn, err := dialTLS(pool)
	if err != nil {
		t.Fatal(err)
	}
	overTLS := newStreamClient(t, tlsConn, tlsAddr, "alice", "pw", false)
	exchange(t, overTLS, p, overTLS.mustAllocate(t), "over tls")
	if s.Allocations() != 2 {
		t.Fatalf("%d allocations", s.Allocations())
	}

	// A connection closed ends its allocation.
	overTCP.cut()
	eventually(t, "the TCP client's allocation freed", func() bool {
		return s.Allocations() == 1 && metric(s, `fit_turn_freed_total{reason="closed"}`) == 1
	})
	exchange(t, overTLS, p, func() netip.AddrPort {
		var r netip.AddrPort
		overTLS.do(func(c *fibturn.Client) { r, _, _ = c.Relayed() })
		return r
	}(), "still over tls")

	// A connection that asks for nothing is closed at stream_idle_timeout.
	idle, err := net.Dial("tcp", tcpAddr.String())
	if err != nil {
		t.Fatal(err)
	}
	defer idle.Close()
	_ = idle.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := idle.Read(make([]byte, 1)); err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("an idle connection was not closed: %v", err)
	}

	// Another certificate in the same files, applied on reload, without
	// a restart.
	_, _, pool2 := certFiles(t, dir)
	m2 := *m
	m2.AllowedPeers = append(slices.Clone(m.AllowedPeers), p.addr.String())
	if err := s.Apply(&m2); err != nil {
		t.Fatal(err)
	}
	if s.restarts.Load() != 0 || s.Allocations() != 1 {
		t.Fatalf("%d restarts, %d allocations", s.restarts.Load(), s.Allocations())
	}
	if c, err := dialTLS(pool); err == nil {
		_ = c.Close()
		t.Fatal("the old certificate is still served")
	}
	c, err := dialTLS(pool2)
	if err != nil {
		t.Fatalf("the new certificate: %v", err)
	}
	again := newStreamClient(t, c, tlsAddr, "alice", "pw", true)
	exchange(t, again, p, again.mustAllocate(t), "new certificate")
}

func TestLimiter(t *testing.T) {
	var l limiter
	l.init()
	now := time.Unix(1000, 0)
	if !l.allow("a", true, 100, 100, now) || l.allow("a", true, 1, 100, now) {
		t.Fatal("the burst is not a second's worth")
	}
	if !l.allow("a", false, 100, 100, now) {
		t.Fatal("the directions share a bucket")
	}
	if !l.allow("a", true, 50, 100, now.Add(500*time.Millisecond)) {
		t.Fatal("no refill")
	}
	if len(l.buckets) != 2 {
		t.Fatalf("%d buckets", len(l.buckets))
	}
	l.allow("b", true, 1, 100, now.Add(time.Hour))
	if len(l.buckets) != 1 {
		t.Fatalf("%d buckets after a sweep", len(l.buckets))
	}
}
