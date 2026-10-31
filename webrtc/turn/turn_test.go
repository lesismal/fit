package turn

import (
	"context"
	"errors"
	"net"
	"net/netip"
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

// client drives fib's turn.Client over a UDP socket of its own: a goroutine
// reads it, another fires the client's timers, and a mutex serializes them.
type client struct {
	mu        sync.Mutex
	c         *fibturn.Client
	conn      *net.UDPConn
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
	u := &client{conn: conn, got: make(chan string, 1024), allocated: make(chan struct{}), failed: make(chan error, 1), done: make(chan struct{})}
	c, err := fibturn.NewClient(fibturn.ClientConfig{
		Server: server, Username: user, Password: pass, BindChannels: bind,
		Send:        func(to netip.AddrPort, b []byte) { _, _ = conn.WriteToUDPAddrPort(b, to) },
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
		buf := make([]byte, 2048)
		for {
			n, from, err := conn.ReadFromUDPAddrPort(buf)
			if err != nil {
				return
			}
			u.mu.Lock()
			_, data, _ := c.HandlePacket(from, buf[:n], time.Now())
			if data != nil {
				select {
				case u.got <- string(data):
				default:
				}
			}
			u.mu.Unlock()
		}
	}()
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
			for _, msg := range []string{"one", "two", strings.Repeat("x", 1200)} {
				exchange(t, u, p, relayed, msg)
			}
			u.do(func(c *fibturn.Client) { c.Close(time.Now()) })
			eventually(t, "the allocation's end", func() bool { return s.Allocations() == 0 })
		})
	}
	var b observe.TextWriter
	s.WriteMetrics(&b)
	for _, want := range []string{"fit_turn_allocations 0", `fit_turn_relayed_bytes_total{direction="to_peer"}`, "fit_turn_restarts_total 0"} {
		if !strings.Contains(string(b.B), want) {
			t.Errorf("metrics lack %s:\n%s", want, b.B)
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
}

func TestBandwidth(t *testing.T) {
	e := startEngine(t)
	m := member()
	m.UserBandwidth = 4000
	s := startRole(t, e, m)
	u := newClient(t, serverAddr(t, s), "alice", "pw", true)
	relayed := u.mustAllocate(t)
	p := newPeer(t)
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
	p := newPeer(t)
	refused := func(u *client, to netip.AddrPort) bool {
		u.do(func(c *fibturn.Client) { _ = c.CreatePermission(to.Addr(), time.Now()) })
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			var err error
			u.do(func(c *fibturn.Client) { err = c.SendTo(to, []byte("x")) })
			if err != nil {
				return true
			}
			time.Sleep(20 * time.Millisecond)
		}
		return false
	}
	if !refused(u, p.addr) {
		t.Fatal("a permission for a loopback peer was not refused")
	}
	if s.peersRefused.Load() == 0 {
		t.Fatal("no refusal counted")
	}

	// denied_peers refuses what allowed_peers does not list first.
	m = member()
	m.AllowedPeers = []string{"127.0.0.1"}
	m.DeniedPeers = []string{"127.0.0.0/8"}
	if err := s.Apply(m); err != nil {
		t.Fatal(err)
	}
	u = newClient(t, addr, "alice", "pw", false)
	exchange(t, u, p, u.mustAllocate(t), "allowed")
	if !refused(u, netip.MustParseAddrPort("127.0.0.2:9")) {
		t.Fatal("a permission for a denied peer was not refused")
	}
}

func TestReload(t *testing.T) {
	e := startEngine(t)
	m := member()
	s := startRole(t, e, m)
	addr := serverAddr(t, s)
	u := newClient(t, addr, "alice", "pw", true)
	relayed := u.mustAllocate(t)
	p := newPeer(t)
	exchange(t, u, p, relayed, "before")

	// Users, the secret, the peers and the bandwidth change in place.
	m2 := member()
	m2.Users = []config.TURNUser{{Name: "dave", Password: "pw2"}}
	m2.Secret = ""
	m2.UserBandwidth = 1 << 20
	if err := s.Apply(m2); err != nil {
		t.Fatal(err)
	}
	if s.restarts.Load() != 0 || s.Allocations() != 1 {
		t.Fatalf("%d restarts, %d allocations", s.restarts.Load(), s.Allocations())
	}
	exchange(t, u, p, relayed, "after")
	if _, err := newClient(t, addr, "alice", "pw", false).allocate(t); err == nil {
		t.Fatal("a user taken out still allocates")
	}
	newClient(t, addr, "dave", "pw2", false).mustAllocate(t)

	// A quota starts the server again on the same address.
	m3 := *m2
	m3.Listen = []string{addr.String()}
	m3.MaxAllocations = 10
	if err := s.Apply(&m3); err != nil {
		t.Fatal(err)
	}
	if s.restarts.Load() != 1 || s.Allocations() != 0 || serverAddr(t, s) != addr {
		t.Fatalf("%d restarts, %d allocations, at %v", s.restarts.Load(), s.Allocations(), serverAddr(t, s))
	}
	newClient(t, addr, "dave", "pw2", false).mustAllocate(t)

	// Another address: the new one listens, the old one is gone.
	m4 := m3
	m4.Listen = []string{"127.0.0.1:0"}
	if err := s.Apply(&m4); err != nil {
		t.Fatal(err)
	}
	moved := serverAddr(t, s)
	if moved == addr {
		t.Fatal("the server did not move")
	}
	c := newClient(t, moved, "dave", "pw2", true)
	exchange(t, c, p, c.mustAllocate(t), "moved")
	free, err := net.ListenPacket("udp", addr.String())
	if err != nil {
		t.Fatalf("the old address is still bound: %v", err)
	}
	_ = free.Close()
}

func TestReloadRefused(t *testing.T) {
	e := startEngine(t)
	s := startRole(t, e, member())
	addr := serverAddr(t, s)
	// An address another socket holds.
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
	p := newPeer(t)
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
	q := newPeer(t)
	exchange(t, other, q, other.mustAllocate(t), "meanwhile")
	eventually(t, "the cut client's allocation to expire", func() bool { return s.Allocations() == 1 })
	close(stop)
	wg.Wait()
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
