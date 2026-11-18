package uplink

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/tunnel/internal/auth"
	"github.com/lesismal/fit/tunnel/internal/link"
	"github.com/lesismal/fit/tunnel/internal/proto"
	"github.com/lesismal/fit/tunnel/internal/transport"
	"github.com/lesismal/fit/tunnel/internal/tunnel"
	"github.com/lesismal/fit/tunnel/internal/tunneltest"
)

func init() { auth.Iterations = 1000 }

// fakeHub registers every link that asks, numbering them, and answers
// pings, or fails them while failPings is set. It takes the password p,
// and proves it, unless noProof is set.
type fakeHub struct {
	addr      string
	conns     atomic.Uint32
	failPings atomic.Bool
	noProof   atomic.Bool
	links     chan *link.Link
}

// hubHello is a hub's answer to a hello, for password.
func hubHello(password string) link.CallFunc {
	salt := auth.Random(auth.SaltLen)
	key, _ := auth.Derive(password, salt)
	return func(l *link.Link, body []byte) (any, error) {
		var h auth.Hello
		if err := link.Decode(body, &h); err != nil {
			return nil, err
		}
		rsp, hs, sess, err := auth.Answer(&h, salt, key, nil)
		if err != nil {
			return nil, err
		}
		l.SetValue(hs)
		return link.Sealing{Reply: rsp, Session: sess}, nil
	}
}

func newFakeHub(t *testing.T) *fakeHub { return newFakeHubWith(t, "p") }

func newFakeHubWith(t *testing.T, password string) *fakeHub {
	t.Helper()
	s := &fakeHub{links: make(chan *link.Link, 16)}
	r := link.NewRouter()
	r.HandleCall(proto.MethodHello, hubHello(password), true)
	r.HandleCall(proto.MethodRegister, func(l *link.Link, _ []byte) (any, error) {
		n := s.conns.Add(1)
		s.links <- l
		rsp := &proto.RegisterRsp{Peer: 7, Conn: n, Index: int(n - 1), Hub: "srv"}
		if hs, _ := l.Value().(*auth.Handshake); hs != nil && !s.noProof.Load() {
			rsp.Proof = hs.HubProof()
		}
		return rsp, nil
	}, true)
	r.HandleCall(proto.MethodPing, func(*link.Link, []byte) (any, error) {
		if s.failPings.Load() {
			return nil, errors.New("no pings")
		}
		return &proto.PingRsp{}, nil
	}, true)
	r.HandleCall(proto.MethodNodes, func(*link.Link, []byte) (any, error) { return []string{"a"}, nil }, true)
	l, err := transport.Listen(tunneltest.Engine(t), "127.0.0.1:0", transport.ListenOptions{}, func(c transport.Conn) transport.Handler {
		return link.New(c, r)
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	s.addr = l.URL()
	return s
}

// closedAddr is a TCP address nothing listens on.
func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func info() proto.Info { return proto.Info{Name: "n", Type: "node"} }

func newUplink(t *testing.T, cfg *config.TunnelUplink) *Uplink {
	t.Helper()
	u := New(tunneltest.Engine(t), cfg, nil, nil, info, func(*Uplink, *link.Router) {})
	t.Cleanup(u.Stop)
	return u
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestDials(t *testing.T) {
	cfg := &config.TunnelUplink{QUICPin: "ab", Endpoints: []config.TunnelEndpoint{
		{Address: "h:1", Disabled: true},
		{Address: ""},
		{Address: "quic://h:2"},
		{Address: "ws://h:3/x"},
	}}
	got := Dials(cfg)
	if len(got) != 2 || got[0] != "quic://h:2?pin=ab" || got[1] != "ws://h:3/x" {
		t.Errorf("dials %q", got)
	}
	other := *cfg
	if !Same(cfg, &other) {
		t.Error("a copy is not the same uplink")
	}
	other.Endpoints = append([]config.TunnelEndpoint(nil), cfg.Endpoints...)
	other.Endpoints[0].Disabled = false
	if Same(cfg, &other) {
		t.Error("an endpoint enabled is the same uplink")
	}
}

// An uplink falls back to the endpoints that answer, registers its pool,
// pings, and calls the hub; a kick and a stop show in its status.
func TestUplink(t *testing.T) {
	s := newFakeHub(t)
	cfg := &config.TunnelUplink{
		Name:          "srv",
		Password:      "p",
		BodyPassword:  "",
		PoolSize:      2,
		RetryInterval: config.Duration(20 * time.Millisecond),
		PingInterval:  config.Duration(20 * time.Millisecond),
		Endpoints:     []config.TunnelEndpoint{{Address: closedAddr(t)}, {Address: "tcp://" + s.addr}},
	}
	u := newUplink(t, cfg)
	var registered, pongs atomic.Int32
	u.OnRegistered = func(*Uplink) { registered.Add(1) }
	u.OnPing = func(req *proto.PingReq) {}
	u.OnPong = func(*Uplink, *proto.PingRsp) { pongs.Add(1) }
	u.Start()
	waitFor(t, "the pool", func() bool { return u.Status().Pool == 2 })
	waitFor(t, "a pong", func() bool { return pongs.Load() > 0 })
	if n := registered.Load(); n != 1 {
		t.Errorf("OnRegistered ran %d times", n)
	}
	st := u.Status()
	if st.State != StateRegistered || st.Hub != "srv" || st.PeerID != 7 || st.Protos["tcp"] != 2 || len(st.Endpoints) != 2 || st.Addr != "tcp://"+s.addr {
		t.Errorf("status %+v", st)
	}
	var nodes []string
	if err := u.Call(proto.MethodNodes, nil, &nodes); err != nil || len(nodes) != 1 {
		t.Errorf("call: %v, %v", nodes, err)
	}
	if u.RTT().Samples == 0 {
		t.Errorf("rtt %+v", u.RTT())
	}

	// A failed ping counts as lost.
	s.failPings.Store(true)
	waitFor(t, "a lost ping", func() bool { return u.RTT().Loss > 0 })

	l := <-s.links
	_ = l.Notify(proto.MethodKick, "go away")
	waitFor(t, "the kick", func() bool { return u.Status().State == StateFailed })
	if e := u.Status().Error; !strings.Contains(e, "go away") {
		t.Errorf("error %q", e)
	}

	u.Stop()
	u.Stop()
	if st := u.Status(); st.State != StateStopped {
		t.Errorf("state %q", st.State)
	}
	if _, err := u.Proxy(); !errors.Is(err, ErrNotRegistered) {
		t.Errorf("proxy: %v", err)
	}
	if _, _, err := u.NextTunnelID(); !errors.Is(err, ErrNotRegistered) {
		t.Errorf("tunnel ID: %v", err)
	}
	if err := u.Call(proto.MethodNodes, nil, nil); !errors.Is(err, ErrNotRegistered) {
		t.Errorf("call: %v", err)
	}
}

// New tunnels go over the link that carries the fewest, and none goes over
// links that carry as many as they may.
func TestNextTunnelID(t *testing.T) {
	s := newFakeHub(t)
	u := newUplink(t, &config.TunnelUplink{Password: "p", PoolSize: 2, Endpoints: []config.TunnelEndpoint{{Address: s.addr}}})
	var links []*link.Link
	for range 2 {
		l, err := u.dial()
		if err != nil {
			t.Fatal(err)
		}
		u.register(l)
		links = append(links, l)
	}
	add := func(l *link.Link, n int) {
		for range n {
			id, _, err := u.NextTunnelID()
			if err != nil {
				t.Fatal(err)
			}
			if err := u.Tunnels.Add(tunnel.New(u.Tunnels, l, id, true)); err != nil {
				t.Fatal(err)
			}
		}
	}
	add(links[0], 1)
	for range 2 {
		if _, l, err := u.NextTunnelID(); err != nil || l != links[1] {
			t.Fatalf("the link with fewer tunnels was not taken: %v", err)
		}
	}
	add(links[0], proto.MaxTunnelsPerLink-1)
	add(links[1], proto.MaxTunnelsPerLink)
	if _, _, err := u.NextTunnelID(); !errors.Is(err, ErrLinksFull) {
		t.Errorf("err %v", err)
	}

	// Losing the links closes their tunnels, and the uplink connects again.
	for _, l := range links {
		u.lost(l)
	}
	u.lost(links[0])
	if st := u.Status(); st.State != StateConnecting || st.Tunnels != 0 {
		t.Errorf("status %+v", st)
	}
}

// An uplink with no address to dial fails, and one whose config failed
// does not start.
func TestNoAddress(t *testing.T) {
	u := newUplink(t, &config.TunnelUplink{PoolSize: 1, Endpoints: []config.TunnelEndpoint{{Address: "h:1", Disabled: true}}})
	u.Start()
	waitFor(t, "the failure", func() bool { return u.Status().State == StateFailed })
	if e := u.Status().Error; e != "no address to dial" {
		t.Errorf("error %q", e)
	}

	u = newUplink(t, &config.TunnelUplink{Endpoints: []config.TunnelEndpoint{{Address: "h:1"}}})
	u.state = StateFailed
	u.Start()
	if u.pinging.Load() {
		t.Error("a failed uplink pinged")
	}
}

// A dial fails with why each address failed.
func TestDialFails(t *testing.T) {
	a, b := closedAddr(t), closedAddr(t)
	u := newUplink(t, &config.TunnelUplink{Endpoints: []config.TunnelEndpoint{{Address: a}, {Address: b}}})
	if _, err := u.dial(); err == nil || !strings.Contains(err.Error(), a+": ") || !strings.Contains(err.Error(), "; "+b+": ") {
		t.Errorf("err %v", err)
	}
}

// The links are sealed, a body password or not.
func TestBodyPassword(t *testing.T) {
	u := newUplink(t, &config.TunnelUplink{BodyPassword: "b", Endpoints: []config.TunnelEndpoint{{Address: "h:1"}}})
	if st := u.Status(); !st.BodyEncrypted || !st.HasBodyPassword {
		t.Errorf("status %+v", st)
	}
	if _, _, err := u.keys(make([]byte, 3)); err == nil {
		t.Error("a salt of the wrong size")
	}
	salt := auth.Random(auth.SaltLen)
	u.Config.Password = "p"
	k1, b1, err := u.keys(salt)
	if err != nil || b1 == nil {
		t.Fatal(err)
	}
	if k2, _, _ := u.keys(salt); &k2[0] != &k1[0] {
		t.Error("the keys under the same salt were derived again")
	}
}

// A hub that does not know the password, or does not prove it, is not
// registered with, and nothing of it is served: the node's methods it
// calls are refused.
func TestFakeHub(t *testing.T) {
	served := make(chan struct{}, 4)
	for _, tc := range []struct {
		name string
		hub  func() *fakeHub
		want string
	}{
		{"another password", func() *fakeHub { return newFakeHubWith(t, "guess") }, "decrypt"},
		{"no proof", func() *fakeHub { s := newFakeHub(t); s.noProof.Store(true); return s }, "did not prove"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.hub()
			u := New(tunneltest.Engine(t), &config.TunnelUplink{Password: "p", PoolSize: 1, RetryInterval: config.Duration(time.Hour),
				Endpoints: []config.TunnelEndpoint{{Address: s.addr}}}, nil, nil, info, func(_ *Uplink, r *link.Router) {
				r.HandleCall(proto.MethodNodeSetServices, func(*link.Link, []byte) (any, error) { served <- struct{}{}; return "ok", nil }, true)
			})
			t.Cleanup(u.Stop)
			u.Start()
			waitFor(t, "the failure", func() bool { return u.Status().State == StateFailed })
			if st := u.Status(); st.Pool != 0 || !strings.Contains(st.Error, tc.want) {
				t.Errorf("status %+v", st)
			}
		})
	}
	// A hub that skips the handshake: what it calls is refused.
	r := link.NewRouter()
	calls := make(chan error, 1)
	r.HandleCall(proto.MethodHello, func(l *link.Link, _ []byte) (any, error) {
		calls <- l.Call(proto.MethodNodeSetServices, []proto.Service{{Name: "ssh", Addr: "127.0.0.1:22"}}, nil, 5*time.Second)
		return nil, errors.New("hold on")
	}, false)
	ln, err := transport.Listen(tunneltest.Engine(t), "127.0.0.1:0", transport.ListenOptions{}, func(c transport.Conn) transport.Handler { return link.New(c, r) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	u := New(tunneltest.Engine(t), &config.TunnelUplink{Password: "p", PoolSize: 1, RetryInterval: config.Duration(time.Hour),
		Endpoints: []config.TunnelEndpoint{{Address: ln.URL()}}}, nil, nil, info, func(_ *Uplink, r *link.Router) {
		r.HandleCall(proto.MethodNodeSetServices, func(*link.Link, []byte) (any, error) { served <- struct{}{}; return "ok", nil }, true)
	})
	t.Cleanup(u.Stop)
	l, err := u.dial()
	if err != nil {
		t.Fatal(err)
	}
	go u.register(l)
	if err := <-calls; err == nil || !strings.Contains(err.Error(), "not proven") {
		t.Errorf("a call before the handshake: %v", err)
	}
	select {
	case <-served:
		t.Fatal("a fake hub's call was served")
	default:
	}
}

// A dial of an uplink that stopped fails, and closes a link it made.
func TestDialStopped(t *testing.T) {
	s := newFakeHub(t)
	for _, addr := range []string{closedAddr(t), s.addr} {
		u := newUplink(t, &config.TunnelUplink{Endpoints: []config.TunnelEndpoint{{Address: addr}}})
		u.Stop()
		if _, err := u.dial(); !errors.Is(err, ErrStopped) {
			t.Errorf("dial of %s: %v", addr, err)
		}
	}
}

// A ping needs a registered link, and does not overlap another.
func TestPingSkips(t *testing.T) {
	u := newUplink(t, &config.TunnelUplink{Endpoints: []config.TunnelEndpoint{{Address: "h:1"}}})
	u.ping()
	u.pinging.Store(true)
	u.ping()
	if st := u.RTT(); st.Samples != 0 || st.Loss != 0 {
		t.Errorf("rtt %+v", st)
	}
}

// replyConn answers each call sent on it with reply, and then closes its
// link, as a hub that registers a link and drops it at once.
type replyConn struct {
	l     *link.Link
	reply any
	mu    sync.Mutex
}

func (c *replyConn) Send(b []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	body, _ := json.Marshal(c.reply)
	f := make([]byte, link.LengthSize+link.HeaderSize, link.LengthSize+link.HeaderSize+len(body))
	binary.BigEndian.PutUint32(f, uint32(link.HeaderSize+len(body)))
	f[link.LengthSize] = 2 // a reply
	copy(f[link.LengthSize+1:], b[link.LengthSize+1:link.LengthSize+link.HeaderSize])
	c.l.OnData(append(f, body...))
	c.l.OnClose(nil)
	return nil
}

func (c *replyConn) Close() error         { return nil }
func (c *replyConn) CloseAfterSend()      {}
func (c *replyConn) LocalAddr() net.Addr  { return nil }
func (c *replyConn) RemoteAddr() net.Addr { return nil }

// failConn is a connection that sends nothing.
type failConn struct{}

func (failConn) Send([]byte) error    { return errors.New("down") }
func (failConn) Close() error         { return nil }
func (failConn) CloseAfterSend()      {}
func (failConn) LocalAddr() net.Addr  { return nil }
func (failConn) RemoteAddr() net.Addr { return nil }

// A link whose registration fails is closed, and the uplink says why.
func TestRegisterFails(t *testing.T) {
	u := newUplink(t, &config.TunnelUplink{Endpoints: []config.TunnelEndpoint{{Address: "h:1"}}})
	u.register(link.New(&failConn{}, u.router))
	if st := u.Status(); st.State != StateFailed || st.Error != "register: down" {
		t.Errorf("status %+v", st)
	}
}

// A link the uplink let go before it registered is not taken; one whose
// hello is answered by a link that closed fails.
func TestRegisterLost(t *testing.T) {
	s := newFakeHub(t)
	u := newUplink(t, &config.TunnelUplink{Password: "p", Endpoints: []config.TunnelEndpoint{{Address: s.addr}}})
	l, err := u.dial()
	if err != nil {
		t.Fatal(err)
	}
	u.mu.Lock()
	delete(u.pool, l)
	u.mu.Unlock()
	u.register(l)
	if st := u.Status(); st.Pool != 0 || st.State != StateConnecting {
		t.Errorf("status %+v", st)
	}
	c := &replyConn{reply: &auth.HelloRsp{Version: auth.Version}}
	c.l = link.New(c, u.router)
	u.register(c.l)
	if st := u.Status(); st.Pool != 0 || st.State != StateFailed {
		t.Errorf("status %+v", st)
	}
}
