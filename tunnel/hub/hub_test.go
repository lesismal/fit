package hub

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/tunnel/internal/auth"
	"github.com/lesismal/fit/tunnel/internal/link"
	"github.com/lesismal/fit/tunnel/internal/proto"
)

// tcpMember is a hub member with the password pw, listening over TCP on
// a port of its own, and extra members after.
func tcpMember(extra string) string {
	return `{"name": "hub", "password": "pw", "listeners": [{"protocol": "tcp", "address": "127.0.0.1:0"}]` + extra + `}`
}

// A first Apply that does not open everything fails, and leaves nothing
// open; an Apply after Stop is refused.
func TestApplyFails(t *testing.T) {
	v := newEnv(t)
	if err := v.s.Apply(nil); err == nil {
		t.Fatal("applied no member")
	}
	if err := v.s.Apply((*config.TunnelHub)(nil)); err == nil {
		t.Fatal("applied a nil member")
	}
	if err := v.s.Apply(&config.TunnelNode{}); err == nil {
		t.Fatal("applied another role's member")
	}
	busy := busyAddr(t)
	for name, member := range map[string]string{
		"listener":       fmt.Sprintf(`{"password": "pw", "listeners": [{"protocol": "tcp", "address": %q}]}`, busy),
		"console":        fmt.Sprintf(`{"password": "pw", "console": %s}`, consoleJSON(busy)),
		"console socket": fmt.Sprintf(`{"password": "pw", "console": %s, "console_socket": {"address": %q}}`, consoleJSON("127.0.0.1:0"), busy),
	} {
		if err := v.apply(member); err == nil {
			t.Fatalf("%s: applied", name)
		}
		if v.s.started || len(v.s.Status().Listen) != 0 {
			t.Fatalf("%s: left open %+v", name, v.s.Status().Listeners)
		}
	}
	// A certificate that does not load, as a config that loads does not
	// have.
	if err := v.s.Apply(&config.TunnelHub{Password: "pw", QUIC: &config.TunnelHubQUIC{Cert: "not a cert", Key: "not a key"}}); err == nil {
		t.Fatal("applied a certificate that does not load")
	}
	v.s.Stop(context.Background())
	v.s.Stop(context.Background())
	if err := v.apply(tcpMember("")); !errors.Is(err, app.ErrStopped) {
		t.Fatalf("an Apply after Stop: %v", err)
	}
}

// Applies after the first keep what did not change, and move what did.
func TestApplyMoves(t *testing.T) {
	sock := freeAddr(t)
	con := consoleJSON("127.0.0.1:0")
	v := newHub(t, fmt.Sprintf(`{"password": "pw", "console": %s, "console_socket": {"address": %q, "password": "s"},
 "listeners": [{"protocol": "tcp", "address": "127.0.0.1:0"}, {"protocol": "quic", "address": "127.0.0.1:0"}]}`, con, sock))
	set := v.s.settings()
	if v.s.Fingerprint() == "" || v.s.SocketAddr() == "" {
		t.Fatalf("status %+v", v.s.Status())
	}
	// The member the hub made a certificate for is kept with it.
	if err := v.waitEdit(); err != nil || !strings.Contains(v.current(), "BEGIN CERTIFICATE") {
		t.Fatalf("the certificate kept: %v %s", err, v.current())
	}
	// The same passwords and socket address keep their secrets and socket.
	if err := v.apply(fmt.Sprintf(`{"password": "pw", "console": %s, "console_socket": {"address": %q, "password": "s", "max_conns": 3},
 "listeners": [{"protocol": "tcp", "address": "127.0.0.1:0"}]}`, con, sock)); err != nil {
		t.Fatal(err)
	}
	if next := v.s.settings(); &next.keys["pw"][0] != &set.keys["pw"][0] || &next.secret[0] != &set.secret[0] || v.s.socket.h.max.Load() != 3 {
		t.Fatal("the secrets were made anew")
	}
	// Without a quic listener the certificate goes; a busy listener is
	// shown with its error, beside the rest applied.
	busy := busyAddr(t)
	err := v.apply(fmt.Sprintf(`{"password": "pw", "listeners": [{"protocol": "tcp", "address": "127.0.0.1:0"}, {"protocol": "tcp", "address": %q}]}`, busy))
	if err == nil {
		t.Fatal("a busy listener applied")
	}
	st := v.s.Status()
	if st.QUICFingerprint != "" || st.Socket != "" || len(st.Listeners) != 2 || st.Listeners[1].Error == "" {
		t.Fatalf("status %+v", st)
	}
}

// What the hub cannot write of the certificate it made is logged, and
// leaves the config as it is.
func TestWriteCertRefused(t *testing.T) {
	for _, cur := range []string{`{"quic": 5}`, `{"quic": {"cert": 5}}`, `{"quic": {"cert": "kept"}}`} {
		v := newEnv(t)
		v.setMember(cur)
		if err := v.s.Apply(mustLoad(t, `{"password": "pw", "listeners": [{"protocol": "quic", "address": "127.0.0.1:0"}]}`)); err != nil {
			t.Fatal(err)
		}
		err := v.waitEdit()
		if cur == `{"quic": {"cert": "kept"}}` {
			// The config's own certificate stays.
			if strings.Contains(v.current(), "BEGIN") {
				t.Fatalf("%s: overwritten: %s", cur, v.current())
			}
			continue
		}
		if err == nil {
			t.Fatalf("%s: written: %s", cur, v.current())
		}
	}
}

func mustLoad(t *testing.T, member string) *config.TunnelHub {
	t.Helper()
	cfg, err := load(member)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// A link that has not registered is served nothing else, and closed past
// register_timeout; past max_links, a link closes as it opens.
func TestUnregisteredLinks(t *testing.T) {
	v := newHub(t, tcpMember(`, "max_links": 1, "register_timeout": "500ms"`))
	p := v.dial(v.listen())
	if err := p.call(proto.MethodNodes, nil, nil); err == nil || !strings.Contains(err.Error(), "register first") {
		t.Fatalf("a call before registering: %v", err)
	}
	waitFor(t, "the first link to count", func() bool { return v.s.Status().Links == 1 })
	q := v.dial(v.listen())
	q.waitClosed()
	p.waitClosed()
}

// Links that have not registered have a cap of their own, within
// max_links, so that they cannot crowd out the peers'.
func TestPendingLinks(t *testing.T) {
	v := newHub(t, tcpMember(`, "max_pending_links": 1`))
	addr := v.listen()
	p := v.dial(addr)
	waitFor(t, "the first link to count", func() bool { return v.s.Status().Links == 1 })
	v.dial(addr).waitClosed()
	p.mustRegister(proto.TypeNode, "lab")
	q := v.dial(addr).mustRegister(proto.TypeNode, "lab")
	q.l.Close()
	waitFor(t, "the link to go", func() bool { return v.s.Status().Links == 1 })
	// A link that closes before it registers gives its place back.
	r := v.dial(addr)
	waitFor(t, "the link to count", func() bool { return v.s.Status().Links == 2 })
	r.l.Close()
	waitFor(t, "the link to go", func() bool { return v.s.Status().Links == 1 })
	v.dial(addr).mustRegister(proto.TypeProxy, "me")
	v.s.mu.Lock()
	pending := v.s.pending
	v.s.mu.Unlock()
	if pending != 0 {
		t.Fatalf("%d pending", pending)
	}
}

// Nodes and proxies register with the passwords of their roles, or a node
// with its own; a proxy cannot register as a node, nor a node under the
// name of one with its own password; a password that changes kicks those
// who registered with it.
func TestRolePasswords(t *testing.T) {
	v := newHub(t, `{"name": "hub", "node_password": "np", "proxy_password": "pp", "node_secrets": [{"name": "lab", "password": "ls"}],
 "listeners": [{"protocol": "tcp", "address": "127.0.0.1:0"}]}`)
	addr := v.listen()
	refused := func(typ, name, pw string) {
		t.Helper()
		if err := v.dial(addr).register(typ, name, pw); err == nil {
			t.Fatalf("%s %s registered with %q", typ, name, pw)
		}
	}
	registered := func(typ, name, pw string) *peer {
		t.Helper()
		p := v.dial(addr)
		if err := p.register(typ, name, pw); err != nil {
			t.Fatalf("%s %s with %q: %v", typ, name, pw, err)
		}
		return p
	}
	proxy := registered(proto.TypeProxy, "me", "pp")
	refused(proto.TypeNode, "beta", "pp")
	refused(proto.TypeProxy, "you", "np")
	beta := registered(proto.TypeNode, "beta", "np")
	refused(proto.TypeNode, "lab", "np")
	refused(proto.TypeNode, "lab", "pp")
	lab := registered(proto.TypeNode, "lab", "ls")
	// The proxy the node password does not let in sees the nodes.
	var list proto.NodeList
	if err := proxy.call(proto.MethodNodes, nil, &list); err != nil || len(list.Nodes) != 2 {
		t.Fatalf("nodes %+v, %v", list, err)
	}
	// A node does not list the nodes.
	if err := beta.call(proto.MethodNodes, nil, nil); err == nil || !strings.Contains(err.Error(), "only a proxy") {
		t.Fatalf("a node listed the nodes: %v", err)
	}
	// A peer of the name that proved another password is refused.
	node := v.s.peerByName(proto.TypeNode, "lab")
	v.s.mu.Lock()
	node.cred = auth.Key("another")
	v.s.mu.Unlock()
	if err := v.dial(addr).register(proto.TypeNode, "lab", "ls"); err == nil || !strings.Contains(err.Error(), "another password") {
		t.Fatalf("a second lab: %v", err)
	}
	v.s.mu.Lock()
	node.cred = v.s.settings().key(proto.TypeNode, "lab")
	v.s.mu.Unlock()

	// lab's password changes: lab is kicked, the rest stay.
	if err := v.apply(`{"name": "hub", "node_password": "np", "proxy_password": "pp", "node_secrets": [{"name": "lab", "password": "ls2"}],
 "listeners": [{"protocol": "tcp", "address": "127.0.0.1:0"}]}`); err != nil {
		t.Fatal(err)
	}
	if n := lab.wait(proto.MethodKick); !strings.Contains(string(n.body), "password") {
		t.Fatalf("kick %q", n.body)
	}
	lab.waitClosed()
	select {
	case <-beta.l.Done():
		t.Fatal("beta was kicked")
	case <-proxy.l.Done():
		t.Fatal("the proxy was kicked")
	case <-time.After(100 * time.Millisecond):
	}
}

// Registrations the hub refuses.
func TestRegisterRefused(t *testing.T) {
	v := newHub(t, tcpMember(`, "max_peers": 1, "max_links_per_peer": 1`))
	p := v.dial(v.listen())
	if err := p.call(proto.MethodRegister, "not json", nil); err == nil {
		t.Fatal("a registration that does not decode")
	}
	if err := v.dial(v.listen()).register(proto.TypeNode, "lab", "wrong"); err == nil || !strings.Contains(err.Error(), "decrypt") {
		t.Fatal("a wrong password registered")
	}
	// The numbers wrap past zero.
	v.s.mu.Lock()
	v.s.nextID, v.s.nextConn = math.MaxUint32, math.MaxUint32
	v.s.mu.Unlock()
	p.mustRegister(proto.TypeNode, "lab")
	if p.rsp.Peer != 1 || p.rsp.Conn != 1 {
		t.Fatalf("numbered %+v", p.rsp)
	}
	if err := p.register(proto.TypeNode, "lab", "pw"); err == nil || !strings.Contains(err.Error(), "second hello") {
		t.Fatalf("registered twice: %v", err)
	}
	if err := p.call(proto.MethodRegister, &proto.RegisterReq{}, nil); err == nil || !strings.Contains(err.Error(), "already") {
		t.Fatalf("registered twice: %v", err)
	}
	if err := v.dial(v.listen()).register(proto.TypeNode, "lab", "pw"); err == nil || !strings.Contains(err.Error(), "links") {
		t.Fatalf("past max_links_per_peer: %v", err)
	}
	if err := v.dial(v.listen()).register(proto.TypeProxy, "me", "pw"); err == nil || !strings.Contains(err.Error(), "peers") {
		t.Fatalf("past max_peers: %v", err)
	}

	// Without a password, nothing registers.
	w := newHub(t, `{"listeners": [{"protocol": "tcp", "address": "127.0.0.1:0"}]}`)
	if err := w.dial(w.listen()).register(proto.TypeNode, "lab", "pw"); err == nil || !strings.Contains(err.Error(), "password") {
		t.Fatalf("registered without a password: %v", err)
	}
	// Hellos the hub refuses.
	for what, h := range map[string]auth.Hello{
		"another version": {Version: 1, Type: proto.TypeNode, Name: "lab", Nonce: auth.Random(auth.NonceLen)},
		"no name":         {Version: auth.Version, Type: proto.TypeNode, Nonce: auth.Random(auth.NonceLen)},
		"a long name":     {Version: auth.Version, Type: proto.TypeNode, Name: strings.Repeat("x", MaxName+1), Nonce: auth.Random(auth.NonceLen)},
		"another type":    {Version: auth.Version, Type: "agent", Name: "lab", Nonce: auth.Random(auth.NonceLen)},
		"short nonce":     {Version: auth.Version, Type: proto.TypeNode, Name: "lab", Nonce: []byte("x")},
	} {
		if err := v.dial(v.listen()).call(proto.MethodHello, &h, nil); err == nil {
			t.Errorf("%s: taken", what)
		}
	}
	if err := v.dial(v.listen()).call(proto.MethodHello, "not json", nil); err == nil {
		t.Error("a hello that does not decode")
	}
	// A registration before the hello, or of another name than the
	// hello's, or with a MAC of another handshake.
	if err := v.dial(v.listen()).call(proto.MethodRegister, &proto.RegisterReq{}, nil); err == nil || !strings.Contains(err.Error(), "after the hello") {
		t.Errorf("a registration before the hello: %v", err)
	}
	q := v.dial(v.listen())
	hs, err := hello(q.l, proto.TypeProxy, "me", "pw", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := q.call(proto.MethodRegister, &proto.RegisterReq{Info: proto.NewInfo("you", proto.TypeProxy), MAC: hs.PeerMAC()}, nil); err == nil || !strings.Contains(err.Error(), "another peer") {
		t.Errorf("a registration of another name: %v", err)
	}
	if err := q.call(proto.MethodRegister, &proto.RegisterReq{Info: proto.NewInfo("me", proto.TypeProxy), MAC: hs.HubProof()}, nil); err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Errorf("a registration with another MAC: %v", err)
	}
	if err := q.call(proto.MethodRegister, "not json", nil); err == nil {
		t.Error("a registration that does not decode")
	}
}

// A recorded handshake replayed on a link of its own does not register:
// the hub's random bytes differ, so the recorded registration does not
// open, and the link closes.
func TestRegisterReplayed(t *testing.T) {
	v := newHub(t, tcpMember(""))
	addr := v.listen()
	// Record what a peer sends.
	rec := &recConn{}
	l := link.New(rec, link.NewRouter())
	h := auth.Hello{Version: auth.Version, Type: proto.TypeNode, Name: "lab", Nonce: auth.Random(auth.NonceLen)}
	p := v.dial(addr)
	var rsp auth.HelloRsp
	if err := p.call(proto.MethodHello, &h, &rsp); err != nil {
		t.Fatal(err)
	}
	key, _ := auth.Derive("pw", rsp.Salt)
	hs, _ := auth.NewHandshake(&h, &rsp, key, nil)
	sess, _ := hs.Session(false)
	l.Seal(sess)
	_ = l.Notify(proto.MethodRegister, &proto.RegisterReq{Info: proto.NewInfo("lab", proto.TypeNode), MAC: hs.PeerMAC()})
	recorded := bytes.Clone(rec.sent[len(rec.sent)-1])

	// Replay it on a new link, after the same hello.
	q := v.dial(addr)
	if err := q.call(proto.MethodHello, &h, nil); err != nil {
		t.Fatal(err)
	}
	q.l.Seal(sess)
	if err := q.l.Conn().Send(recorded); err != nil {
		t.Fatal(err)
	}
	q.waitClosed()
	if len(v.s.Peers()) != 0 {
		t.Fatal("a replayed registration registered")
	}
}

// recConn records what a link sends.
type recConn struct{ sent [][]byte }

func (c *recConn) Send(b []byte) error  { c.sent = append(c.sent, b); return nil }
func (c *recConn) Close() error         { return nil }
func (c *recConn) CloseAfterSend()      {}
func (c *recConn) LocalAddr() net.Addr  { return nil }
func (c *recConn) RemoteAddr() net.Addr { return nil }

// A link that closes while it registers is not registered.
func TestRegisterClosed(t *testing.T) {
	v := newHub(t, tcpMember(""))
	l := link.New(fakeConn{}, v.s.router)
	h := auth.Hello{Version: auth.Version, Type: proto.TypeNode, Name: "lab", Nonce: auth.Random(auth.NonceLen)}
	hb, _ := link.Encode(&h)
	out, err := v.s.hello(l, hb)
	if err != nil {
		t.Fatal(err)
	}
	sealing := out.(link.Sealing)
	l.Seal(sealing.Session)
	key, _ := auth.Derive("pw", v.s.salt)
	hs, _ := auth.NewHandshake(&h, sealing.Reply.(*auth.HelloRsp), key, nil)
	l.OnClose(nil)
	req := proto.RegisterReq{Info: proto.NewInfo("lab", proto.TypeNode), MAC: hs.PeerMAC()}
	body, _ := link.Encode(&req)
	if _, err := v.s.register(l, body); !errors.Is(err, link.ErrClosed) {
		t.Fatalf("registered a closed link: %v", err)
	}
	if len(v.s.Peers()) != 0 {
		t.Fatal("the peer stayed")
	}
}

// Updates and pings that do not decode, and an update that renames.
func TestUpdatePing(t *testing.T) {
	v := newHub(t, tcpMember(""))
	p := v.dial(v.listen()).mustRegister(proto.TypeProxy, "me")
	if err := p.call(proto.MethodUpdate, "not json", nil); err == nil {
		t.Fatal("an update that does not decode")
	}
	if err := p.call(proto.MethodUpdate, proto.NewInfo("other", proto.TypeProxy), nil); err == nil {
		t.Fatal("a peer renamed itself")
	}
	if err := p.call(proto.MethodPing, "not json", nil); err == nil {
		t.Fatal("a ping that does not decode")
	}
}

// The hub sets and refreshes a node's services; no peer has it set them.
func TestNodeServices(t *testing.T) {
	v := newHub(t, tcpMember(""))
	n := v.dial(v.listen()).mustRegister(proto.TypeNode, "lab", proto.Service{Name: "a", Addr: "127.0.0.1:1"})
	n2 := v.dial(v.listen()).mustRegister(proto.TypeNode, "beta")
	c := v.dial(v.listen()).mustRegister(proto.TypeProxy, "me")
	if list := v.s.nodeList(0); len(list.Nodes) != 2 || list.Nodes[0].Info.Name != "beta" {
		t.Fatalf("nodes %+v", list.Nodes)
	}
	// A proxy, or a node, asking the hub to set a node's services is
	// refused, and the node is not asked.
	asked := make(chan struct{}, 4)
	n.mu.Lock()
	n.services = func([]proto.Service) (any, error) { asked <- struct{}{}; return nil, errors.New("no") }
	n.mu.Unlock()
	evil := map[string]any{"node": "lab", "services": []proto.Service{{Name: "ssh", Addr: "127.0.0.1:22"}}}
	for _, p := range []*peer{c, n2} {
		if err := p.call(proto.MethodSetNodeServices, evil, nil); err == nil || !strings.Contains(err.Error(), "no method") {
			t.Fatalf("a peer set a node's services: %v", err)
		}
	}
	select {
	case <-asked:
		t.Fatal("the node was asked")
	default:
	}
	n.mu.Lock()
	n.services = nil
	n.mu.Unlock()
	node := v.s.peerByName(proto.TypeNode, "lab")
	// No services are an empty list.
	if info, err := v.s.setServices(node, nil); err != nil || len(info.Services) != 0 {
		t.Fatalf("%+v %v", info, err)
	}
	want := []proto.Service{{Name: "b", Addr: "127.0.0.1:2"}}
	if info, err := v.s.setServices(node, want); err != nil || len(info.Services) != 1 {
		t.Fatalf("%+v %v", info, err)
	}
	if info, err := v.s.refreshNode(node); err != nil || info.Services[0].Name != "b" {
		t.Fatalf("refresh: %+v %v", info, err)
	}

	// A node that fails, or answers as another, changes nothing.
	n.mu.Lock()
	n.services = func([]proto.Service) (any, error) { return nil, errors.New("no") }
	n.nodeInfo = func() (any, error) { return nil, errors.New("no") }
	n.mu.Unlock()
	if _, err := v.s.setServices(node, want); err == nil {
		t.Fatal("a node's failure succeeded")
	}
	if _, err := v.s.refreshNode(node); err == nil {
		t.Fatal("a node's failure refreshed")
	}
	other := proto.NewInfo("other", proto.TypeNode)
	n.mu.Lock()
	n.services = func([]proto.Service) (any, error) { return &other, nil }
	n.nodeInfo = func() (any, error) { return &other, nil }
	n.mu.Unlock()
	if _, err := v.s.setServices(node, want); err == nil {
		t.Fatal("took another node's info")
	}
	if _, err := v.s.refreshNode(node); err == nil {
		t.Fatal("took another node's info")
	}
	if node.Info().Name != "lab" {
		t.Fatalf("the node became %+v", node.Info())
	}
	_ = n2

	// A peer with no link left is not called.
	if err := v.s.call(&Peer{links: map[int]*link.Link{}}, proto.MethodNodeInfo, nil, nil); !errors.Is(err, errGone) {
		t.Fatalf("a call to a peer gone: %v", err)
	}
}

// openReq is a request of c's to open its seq-th tunnel to node's service.
func openReq(c *peer, seq uint32, node, service string) *proto.TunnelOpenReq {
	return &proto.TunnelOpenReq{ID: proto.TunnelID(c.rsp.Conn, seq), Node: node, Service: service}
}

// Tunnels the hub refuses to open.
func TestOpenTunnelRefused(t *testing.T) {
	v := newHub(t, tcpMember(`, "max_tunnels": 2`))
	svc := proto.Service{Name: "echo", Addr: "127.0.0.1:1"}
	n := v.dial(v.listen()).mustRegister(proto.TypeNode, "lab", svc)
	c := v.dial(v.listen()).mustRegister(proto.TypeProxy, "me")
	for name, tc := range map[string]struct {
		p   *peer
		req any
	}{
		"not json":     {c, "not json"},
		"from a node":  {n, openReq(n, 1, "lab", "echo")},
		"another link": {c, &proto.TunnelOpenReq{ID: proto.TunnelID(c.rsp.Conn+100, 1), Node: "lab", Service: "echo"}},
		"no node":      {c, openReq(c, 1, "nobody", "echo")},
		"no service":   {c, openReq(c, 1, "lab", "nothing")},
	} {
		if err := tc.p.call(proto.MethodTunnelOpen, tc.req, nil); err == nil {
			t.Fatalf("%s: opened", name)
		}
	}
	if err := c.call(proto.MethodTunnelOpen, openReq(c, 1, "lab", "echo"), nil); err != nil {
		t.Fatal(err)
	}
	if err := c.call(proto.MethodTunnelOpen, openReq(c, 1, "lab", "echo"), nil); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("an ID in use: %v", err)
	}
	// The node's failure removes the route.
	n.mu.Lock()
	n.open = func(*proto.TunnelOpenReq) (any, error) { return nil, errors.New("refused") }
	n.mu.Unlock()
	if err := c.call(proto.MethodTunnelOpen, openReq(c, 2, "lab", "echo"), nil); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("a node's refusal: %v", err)
	}
	// A proxy that closes the tunnel while the node opens it.
	n.mu.Lock()
	n.open = func(req *proto.TunnelOpenReq) (any, error) {
		v.s.CloseRoute(req.ID, "gone")
		return &proto.TunnelOpenRsp{ID: req.ID}, nil
	}
	n.mu.Unlock()
	if err := c.call(proto.MethodTunnelOpen, openReq(c, 3, "lab", "echo"), nil); err == nil || !strings.Contains(err.Error(), "while opening") {
		t.Fatalf("closed while opening: %v", err)
	}
	n.wait(proto.MethodTunnelClose)
	if len(v.s.Routes()) != 1 {
		t.Fatalf("routes %d", len(v.s.Routes()))
	}
	// Past max_tunnels.
	v.s.mu.Lock()
	v.s.routes[math.MaxUint64] = &Route{ID: math.MaxUint64}
	v.s.mu.Unlock()
	if err := c.call(proto.MethodTunnelOpen, openReq(c, 4, "lab", "echo"), nil); err == nil || !strings.Contains(err.Error(), "as many tunnels") {
		t.Fatalf("past max_tunnels: %v", err)
	}
}

// The tunnels a link carries are bounded, at either end.
func TestOpenTunnelLinkFull(t *testing.T) {
	v := newHub(t, tcpMember(""))
	svc := proto.Service{Name: "echo", Addr: "127.0.0.1:1"}
	v.dial(v.listen()).mustRegister(proto.TypeNode, "lab", svc)
	c := v.dial(v.listen()).mustRegister(proto.TypeProxy, "me")
	other := link.New(fakeConn{}, v.s.router)
	fill := func(on func(r *Route)) {
		v.s.mu.Lock()
		clear(v.s.routes)
		for i := range proto.MaxTunnelsPerLink {
			r := &Route{ID: uint64(1000 + i), ProxyLink: other, NodeLink: other}
			on(r)
			v.s.routes[r.ID] = r
		}
		v.s.mu.Unlock()
	}
	proxyLink := v.s.peerLinks(v.s.peerByName(proto.TypeProxy, "me"))[0]
	fill(func(r *Route) { r.ProxyLink = proxyLink })
	if err := c.call(proto.MethodTunnelOpen, openReq(c, 1, "lab", "echo"), nil); err == nil || !strings.Contains(err.Error(), "the link carries") {
		t.Fatalf("a full proxy link: %v", err)
	}
	node := v.s.peerByName(proto.TypeNode, "lab")
	nodeLink := v.s.peerLinks(node)[0]
	fill(func(r *Route) { r.Node, r.NodeLink = node, nodeLink })
	if err := c.call(proto.MethodTunnelOpen, openReq(c, 1, "lab", "echo"), nil); err == nil || !strings.Contains(err.Error(), "every link") {
		t.Fatalf("a full node link: %v", err)
	}
	// A node listed without a link left.
	v.s.mu.Lock()
	clear(v.s.routes)
	ghost := &Peer{ID: 99, links: map[int]*link.Link{}, info: proto.Info{Name: "ghost", Type: proto.TypeNode, Services: []proto.Service{svc}}}
	v.s.byName[ghost.key()] = ghost
	v.s.mu.Unlock()
	if err := c.call(proto.MethodTunnelOpen, openReq(c, 1, "ghost", "echo"), nil); err == nil || !strings.Contains(err.Error(), "disconnected") {
		t.Fatalf("a node without links: %v", err)
	}
}

// Tunnels go on the node's least loaded link, frames pass both ways, and
// the proxy hears of the tunnels a node that leaves carried.
func TestRoutes(t *testing.T) {
	v := newHub(t, tcpMember(""))
	svc := proto.Service{Name: "echo", Addr: "127.0.0.1:1"}
	n1 := v.dial(v.listen()).mustRegister(proto.TypeNode, "lab", svc)
	n2 := v.dial(v.listen()).mustRegister(proto.TypeNode, "lab", svc)
	c := v.dial(v.listen()).mustRegister(proto.TypeProxy, "me")
	var opened [2]chan uint64
	for i, n := range []*peer{n1, n2} {
		ch := make(chan uint64, 4)
		opened[i] = ch
		n.mu.Lock()
		n.open = func(req *proto.TunnelOpenReq) (any, error) {
			ch <- req.ID
			return &proto.TunnelOpenRsp{ID: req.ID, Addr: "127.0.0.1:1"}, nil
		}
		n.mu.Unlock()
	}
	a, b := openReq(c, 1, "lab", "echo"), openReq(c, 2, "lab", "echo")
	for _, req := range []*proto.TunnelOpenReq{a, b} {
		if err := c.call(proto.MethodTunnelOpen, req, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(opened[0]) != 1 || len(opened[1]) != 1 {
		t.Fatalf("tunnels per node link: %d, %d", len(opened[0]), len(opened[1]))
	}
	routes := v.s.Routes()
	if len(routes) != 2 || routes[0].ID != a.ID {
		t.Fatalf("routes %+v", routes)
	}
	// Frames pass each way; ones too short, of no tunnel or from a link
	// that does not carry it are dropped.
	_ = c.l.Notify(proto.MethodTunnelData, []byte{1})
	_ = c.l.SendTunnel(proto.MethodTunnelData, 12345, []byte("x"))
	_ = n2.l.SendTunnel(proto.MethodTunnelData, a.ID, []byte("x"))
	_ = c.l.SendTunnel(proto.MethodTunnelData, a.ID, []byte("up"))
	n1.wait(proto.MethodTunnelData)
	_ = n1.l.SendTunnel(proto.MethodTunnelData, a.ID, []byte("down"))
	c.wait(proto.MethodTunnelData)
	st := v.s.Status()
	if st.Tunnels[0].Up != 2 || st.Tunnels[0].Down != 4 {
		t.Fatalf("tunnels %+v", st.Tunnels)
	}
	if v.s.CloseRoute(999, "none") || v.s.removeRoute(&Route{ID: 999}) {
		t.Fatal("closed a tunnel that is not")
	}
	// The node's link that carries b leaves: the proxy hears.
	_ = n2.l.Close()
	if id, _, _ := proto.ParseFrame(c.wait(proto.MethodTunnelClose).body); id != b.ID {
		t.Fatalf("closed %d, want %d", id, b.ID)
	}
	waitFor(t, "b's route to go", func() bool { return len(v.s.Routes()) == 1 })
}
