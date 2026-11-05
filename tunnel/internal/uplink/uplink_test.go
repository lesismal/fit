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
	"github.com/lesismal/fit/tunnel/internal/link"
	"github.com/lesismal/fit/tunnel/internal/proto"
	"github.com/lesismal/fit/tunnel/internal/transport"
	"github.com/lesismal/fit/tunnel/internal/tunnel"
	"github.com/lesismal/fit/tunnel/internal/tunneltest"
)

// fakePassword is the fake hub's password.
const fakePassword = "p"

// fakeHub registers every link that asks, numbering them, and answers
// pings, or fails them while failPings is set.
type fakeHub struct {
	addr      string
	conns     atomic.Uint32
	failPings atomic.Bool
	links     chan *link.Link
}

func newFakeHub(t *testing.T) *fakeHub {
	t.Helper()
	return newFakeHubOn(t, "127.0.0.1:0", transport.ListenOptions{})
}

// newFakeHubOn is a fakeHub listening on addr, with the certificate o
// gives a listener with TLS.
func newFakeHubOn(t *testing.T, addr string, o transport.ListenOptions) *fakeHub {
	t.Helper()
	s := &fakeHub{links: make(chan *link.Link, 16)}
	r := link.NewRouter()
	r.HandleCall(proto.MethodRegister, func(l *link.Link, body []byte) (any, error) {
		var req proto.RegisterReq
		if err := link.Decode(body, &req); err != nil {
			return nil, err
		}
		n := s.conns.Add(1)
		rsp := &proto.RegisterRsp{Peer: 7, Conn: n, Index: int(n - 1), Hub: "srv"}
		proto.Prove(&req, rsp, fakePassword)
		return &link.Then{Rsp: rsp, After: func() { s.links <- l }}, nil
	}, true)
	r.HandleCall(proto.MethodPing, func(*link.Link, []byte) (any, error) {
		if s.failPings.Load() {
			return nil, errors.New("no pings")
		}
		return &proto.PingRsp{}, nil
	}, true)
	r.HandleCall(proto.MethodNodes, func(*link.Link, []byte) (any, error) { return []string{"a"}, nil }, true)
	l, err := transport.Listen(tunneltest.Engine(t), addr, o, func(c transport.Conn) transport.Handler {
		return link.New(c, r, nil)
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
	cfg := &config.TunnelUplink{Pin: "ab", Endpoints: []config.TunnelEndpoint{
		{Address: "h:1", Disabled: true},
		{Address: ""},
		{Address: "quic://h:2"},
		{Address: "ws://h:3/x"},
		{Address: "tls://h:4"},
		{Address: "wss://h:5/x"},
	}}
	got := Dials(cfg)
	if len(got) != 4 || got[0] != "quic://h:2?pin=ab" || got[1] != "ws://h:3/x" || got[2] != "tls://h:4?pin=ab" || got[3] != "wss://h:5/x?pin=ab" {
		t.Errorf("dials %q", got)
	}
	// By CAs: a quic endpoint may also take any certificate, for tests,
	// and tls and wss may not.
	ca := &config.TunnelUplink{CAFile: "/c a.pem", QUICInsecure: true, Endpoints: []config.TunnelEndpoint{{Address: "tls://h:4"}, {Address: "quic://h:2"}}}
	if got := Dials(ca); len(got) != 2 || got[0] != "tls://h:4?ca=%2Fc+a.pem" || got[1] != "quic://h:2?ca=%2Fc+a.pem" {
		t.Errorf("dials %q", got)
	}
	insecure := &config.TunnelUplink{QUICInsecure: true, Endpoints: []config.TunnelEndpoint{{Address: "wss://h:5/x"}, {Address: "quic://h:2"}}}
	if got := Dials(insecure); len(got) != 2 || got[0] != "wss://h:5/x" || got[1] != "quic://h:2?insecure=1" {
		t.Errorf("dials %q", got)
	}
	other := *cfg
	if !Same(cfg, &other) {
		t.Error("a copy is not the same uplink")
	}
	other.CAFile = "x"
	if Same(cfg, &other) {
		t.Error("another ca_file is the same uplink")
	}
	other.CAFile = ""
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
	u := newUplink(t, &config.TunnelUplink{Password: fakePassword, PoolSize: 2, Endpoints: []config.TunnelEndpoint{{Address: s.addr}}})
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

// A body password seals the links' bodies.
func TestBodyPassword(t *testing.T) {
	u := newUplink(t, &config.TunnelUplink{BodyPassword: "b", Endpoints: []config.TunnelEndpoint{{Address: "h:1"}}})
	if st := u.Status(); !st.BodyEncrypted || !st.HasBodyPassword || u.coder == nil {
		t.Errorf("status %+v", st)
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
	if rsp, ok := c.reply.(*proto.RegisterRsp); ok {
		var req proto.RegisterReq
		_ = json.Unmarshal(b[link.LengthSize+link.HeaderSize:], &req)
		proto.Prove(&req, rsp, "")
	}
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
	u.register(link.New(&failConn{}, u.router, nil))
	if st := u.Status(); st.State != StateFailed || st.Error != "register: down" {
		t.Errorf("status %+v", st)
	}
}

// An answer to the registration that is not one fails it, and the uplink
// says why.
func TestRegisterBadAnswer(t *testing.T) {
	u := newUplink(t, &config.TunnelUplink{Endpoints: []config.TunnelEndpoint{{Address: "h:1"}}})
	c := &replyConn{reply: "not an answer"}
	c.l = link.New(c, u.router, nil)
	u.register(c.l)
	if st := u.Status(); st.State != StateFailed || !strings.HasPrefix(st.Error, "register: json: cannot unmarshal") || st.Pool != 0 {
		t.Errorf("status %+v", st)
	}
}

// A link registered after it closed, or after the uplink let it go, is not
// taken.
func TestRegisterLost(t *testing.T) {
	u := newUplink(t, &config.TunnelUplink{Endpoints: []config.TunnelEndpoint{{Address: "h:1"}}})
	c := &replyConn{reply: &proto.RegisterRsp{Conn: 1}}
	c.l = link.New(c, u.router, nil)
	u.pool[c.l] = "h:1"
	u.register(c.l)
	c2 := &replyConn{reply: &proto.RegisterRsp{Conn: 2}}
	c2.l = link.New(c2, u.router, nil)
	u.register(c2.l)
	if st := u.Status(); st.Pool != 0 || st.State != StateConnecting {
		t.Errorf("status %+v", st)
	}
}

// impostor is a hub at an address that answers each registration as answer
// says, or cuts the link without answering when answer returns nil, and
// kicks the node and calls it as a hub would, to open a tunnel and set its
// services: right after its answer, or before it when before is set.
type impostor struct {
	addr  string
	regs  atomic.Int32
	calls chan error
}

func newImpostor(t *testing.T, before bool, answer func(req *proto.RegisterReq) *proto.RegisterRsp) *impostor {
	t.Helper()
	s := &impostor{calls: make(chan error, 64)}
	attack := func(l *link.Link, wait bool) {
		_ = l.Notify(proto.MethodKick, "impostor")
		calls := func() {
			s.calls <- l.Call(proto.MethodTunnelOpen, &proto.TunnelOpenReq{ID: 1, Service: "db"}, nil, 5*time.Second)
			s.calls <- l.Call(proto.MethodNodeSetServices, []proto.Service{{Name: "x", Addr: "10.0.0.1:6379"}}, nil, 5*time.Second)
		}
		if wait {
			calls()
		} else {
			go calls()
		}
	}
	r := link.NewRouter()
	r.HandleCall(proto.MethodRegister, func(l *link.Link, body []byte) (any, error) {
		var req proto.RegisterReq
		if err := link.Decode(body, &req); err != nil {
			return nil, err
		}
		s.regs.Add(1)
		if before {
			attack(l, true)
		}
		rsp := answer(&req)
		if rsp == nil {
			_ = l.Close()
			return nil, link.ErrClosed
		}
		return &link.Then{Rsp: rsp, After: func() {
			// Sent right behind the answer, before the node could have
			// looked at it from another goroutine.
			if !before {
				attack(l, false)
			}
		}}, nil
	}, false)
	ln, err := transport.Listen(tunneltest.Engine(t), "127.0.0.1:0", transport.ListenOptions{}, func(c transport.Conn) transport.Handler {
		return link.New(c, r, nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	s.addr = ln.URL()
	return s
}

// A node serves nothing of a hub that has not proven the password in its
// answer to the registration: one without it, one of a fit before hubs
// proved themselves, one that sends the node's own MAC back, one that
// replays a real hub's answer to another link, one that cuts the link
// before it answers. It closes the link at once and says why. A hub that
// proves it is served.
func TestImpostorHub(t *testing.T) {
	// A real hub's answer to an earlier registration of the node, as an
	// attacker on its path might have kept.
	old := proto.RegisterReq{Info: info()}
	proto.Sign(&old, fakePassword)
	kept := proto.RegisterRsp{Peer: 1, Conn: 1, Hub: "srv"}
	proto.Prove(&old, &kept, fakePassword)

	honest := func(req *proto.RegisterReq) *proto.RegisterRsp {
		rsp := &proto.RegisterRsp{Peer: 1, Conn: 1, Hub: "srv"}
		proto.Prove(req, rsp, fakePassword)
		return rsp
	}
	for _, c := range []struct {
		name   string
		before bool
		answer func(req *proto.RegisterReq) *proto.RegisterRsp
		err    string
	}{
		{"honest", false, honest, ""},
		// What a hub sends before its answer is not served, even when the
		// answer proves it.
		{"calls before the answer", true, honest, ""},
		{"no password", false, func(req *proto.RegisterReq) *proto.RegisterRsp {
			rsp := &proto.RegisterRsp{Peer: 1, Conn: 1, Hub: "srv"}
			proto.Prove(req, rsp, "guess")
			return rsp
		}, proto.ErrHubProof.Error()},
		{"old hub", false, func(*proto.RegisterReq) *proto.RegisterRsp {
			return &proto.RegisterRsp{Peer: 1, Conn: 1, Hub: "srv"}
		}, "the hub runs a fit without hub authentication, older than this side's fit"},
		{"reflected", false, func(req *proto.RegisterReq) *proto.RegisterRsp {
			return &proto.RegisterRsp{Peer: 1, Conn: 1, Hub: "srv", Proof: req.MAC}
		}, proto.ErrHubProof.Error()},
		{"replayed", false, func(*proto.RegisterReq) *proto.RegisterRsp {
			rsp := kept
			return &rsp
		}, proto.ErrHubProof.Error()},
		{"cut", false, func(*proto.RegisterReq) *proto.RegisterRsp { return nil }, "link: closed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newImpostor(t, c.before, c.answer)
			var served atomic.Int32
			u := New(tunneltest.Engine(t), &config.TunnelUplink{
				Name: "srv", Password: fakePassword, PoolSize: 1,
				RetryInterval: config.Duration(20 * time.Millisecond),
				Endpoints:     []config.TunnelEndpoint{{Address: s.addr}},
			}, nil, nil, info, func(_ *Uplink, r *link.Router) {
				for _, m := range []proto.Method{proto.MethodTunnelOpen, proto.MethodNodeSetServices} {
					r.HandleCall(m, func(*link.Link, []byte) (any, error) { served.Add(1); return "ok", nil }, true)
				}
				// In place of the uplink's, to count it.
				r.HandleNotify(proto.MethodKick, func(*link.Link, []byte) { served.Add(1) }, true)
			})
			t.Cleanup(u.Stop)
			u.Start()
			if c.before {
				for range 2 {
					if err := <-s.calls; err == nil || !strings.Contains(err.Error(), "not proven") {
						t.Errorf("a call before the answer: %v", err)
					}
				}
				waitFor(t, "the registration", func() bool { return u.Status().State == StateRegistered })
				if n := served.Load(); n != 0 {
					t.Errorf("served %d calls and notifies before the answer", n)
				}
				return
			}
			if c.err == "" {
				waitFor(t, "the kick and the calls", func() bool { return served.Load() == 3 })
				for range 2 {
					if err := <-s.calls; err != nil {
						t.Error(err)
					}
				}
				return
			}
			// Each link the node opens is refused, and it dials again.
			waitFor(t, "a second registration", func() bool { return s.regs.Load() >= 2 })
			if st := u.Status(); st.State != StateFailed || !strings.Contains(st.Error, c.err) || st.Pool != 0 {
				t.Errorf("status %+v", st)
			}
			if c.name != "cut" {
				for range 2 {
					if err := <-s.calls; err == nil {
						t.Error("a call of the impostor was answered")
					}
				}
			}
			if n := served.Load(); n != 0 {
				t.Errorf("served %d calls and notifies of the impostor", n)
			}
		})
	}
}

// A guard setup gives the router still checks what a proven hub sends:
// what it refuses is not served.
func TestSetupGuard(t *testing.T) {
	s := newImpostor(t, false, func(req *proto.RegisterReq) *proto.RegisterRsp {
		rsp := &proto.RegisterRsp{Peer: 1, Conn: 1, Hub: "srv"}
		proto.Prove(req, rsp, fakePassword)
		return rsp
	})
	var served, guarded atomic.Int32
	u := New(tunneltest.Engine(t), &config.TunnelUplink{
		Name: "srv", Password: fakePassword, PoolSize: 1,
		Endpoints: []config.TunnelEndpoint{{Address: s.addr}},
	}, nil, nil, info, func(_ *Uplink, r *link.Router) {
		for _, m := range []proto.Method{proto.MethodTunnelOpen, proto.MethodNodeSetServices} {
			r.HandleCall(m, func(*link.Link, []byte) (any, error) { served.Add(1); return "ok", nil }, true)
		}
		r.Guard = func(_ *link.Link, m proto.Method) error {
			if m == proto.MethodNodeSetServices {
				guarded.Add(1)
				return errors.New("not here")
			}
			return nil
		}
	})
	t.Cleanup(u.Stop)
	u.Start()
	errs := []error{<-s.calls, <-s.calls}
	if errs[0] != nil || errs[1] == nil || !strings.Contains(errs[1].Error(), "not here") {
		t.Fatalf("the tunnel's open and the services' set: %v", errs)
	}
	if served.Load() != 1 || guarded.Load() != 1 {
		t.Fatalf("served %d, guarded %d", served.Load(), guarded.Load())
	}
}
