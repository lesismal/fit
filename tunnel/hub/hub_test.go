package hub

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/tunnel/internal/link"
	"github.com/lesismal/fit/tunnel/internal/proto"
	"github.com/lesismal/fit/tunnel/internal/transport"
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
	if next := v.s.settings(); next.verifier != set.verifier || &next.secret[0] != &set.secret[0] || v.s.socket.h.max.Load() != 3 {
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

// A hub without admin.store keeps the certificate it made in a file beside
// the config, 0600, not in the config, which nothing would keep: after a
// restart it presents the same one, so the pins of its peers stay good. A
// file that does not hold one fails the Apply, and is left as it is.
func TestCertKeptBesideConfig(t *testing.T) {
	dir := t.TempDir()
	member := `{"password": "pw", "listeners": [{"protocol": "tls", "address": "127.0.0.1:0"}, {"protocol": "quic", "address": "127.0.0.1:0"}]}`
	start := func() *env {
		v := newEnv(t)
		v.m.KeepBeside(filepath.Join(dir, "fit.json"))
		if err := v.apply(member); err != nil {
			t.Fatal(err)
		}
		return v
	}
	v := start()
	fp := v.s.Fingerprint()
	// The test's host is test.host's; fit's is tunnel.hub's.
	kept := filepath.Join(dir, "fit."+v.s.h.ID+".pem")
	fi, err := os.Stat(kept)
	if err != nil || fp == "" {
		t.Fatalf("kept %v, fingerprint %q", err, fp)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if l := v.s.Status().Listeners; len(l) != 2 || l[0].Fingerprint != fp || l[1].Fingerprint != fp {
		t.Fatalf("listeners %+v", l)
	}
	// Applied again, it keeps it, and the config is not edited.
	if err := v.apply(member); err != nil || v.s.Fingerprint() != fp {
		t.Fatalf("again: %v %s", err, v.s.Fingerprint())
	}
	select {
	case err := <-v.edited:
		t.Fatalf("the config was edited: %v %s", err, v.current())
	case <-time.After(100 * time.Millisecond):
	}
	v.s.Stop(context.Background())

	// A restart presents the same certificate.
	if w := start(); w.s.Fingerprint() != fp {
		t.Fatalf("restarted with %s, was %s", w.s.Fingerprint(), fp)
	}

	// A file that holds no certificate fails the first Apply.
	if err := os.WriteFile(kept, []byte("nothing"), 0o600); err != nil {
		t.Fatal(err)
	}
	w := newEnv(t)
	w.m.KeepBeside(filepath.Join(dir, "fit.json"))
	if err := w.apply(member); err == nil || !strings.Contains(err.Error(), "does not hold a certificate") {
		t.Fatalf("a bad file: %v", err)
	}
	if data, _ := os.ReadFile(kept); string(data) != "nothing" {
		t.Fatal("the bad file was overwritten")
	}

	// Where nothing can be written, the certificate goes into the config,
	// as with a store, and the hub starts.
	r := newEnv(t)
	r.m.KeepBeside(filepath.Join(dir, "none", "fit.json"))
	if err := r.apply(member); err != nil || r.s.Fingerprint() == "" {
		t.Fatalf("unwritable: %v", err)
	}
	if err := r.waitEdit(); err != nil || !strings.Contains(r.current(), "BEGIN CERTIFICATE") {
		t.Fatalf("not kept in the config: %v %s", err, r.current())
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

// Links that do not register cannot take every place: past
// max_unregistered_per_ip from one address, or max_unregistered in all, a
// link closes as it opens, while max_links has room; one that registers
// frees its place, and registered links never count.
func TestUnregisteredLimits(t *testing.T) {
	for _, extra := range []string{`, "max_unregistered_per_ip": 2`, `, "max_unregistered": 2`} {
		v := newHub(t, tcpMember(extra))
		idle := []*peer{v.dial(v.listen()), v.dial(v.listen())}
		waitFor(t, "the idle links to count", func() bool { return v.s.Status().Links == 2 })
		v.dial(v.listen()).waitClosed()
		// A registration frees its link's place.
		idle[0].mustRegister(proto.TypeNode, "lab")
		late := v.dial(v.listen())
		late.mustRegister(proto.TypeProxy, "me")
		// The idle link, closing, frees its own.
		_ = idle[1].l.Close()
		waitFor(t, "the idle link to go", func() bool { return v.s.Status().Links == 2 })
		v.dial(v.listen()).mustRegister(proto.TypeProxy, "other")
		v.s.mu.Lock()
		n, ips := len(v.s.unregistered), len(v.s.unregIP)
		v.s.mu.Unlock()
		if n != 0 || ips != 0 {
			t.Fatalf("%s: %d unregistered links, %d addresses", extra, n, ips)
		}
	}
}

func TestAddrIP(t *testing.T) {
	want := netip.MustParseAddr("10.0.0.1")
	for _, a := range []net.Addr{
		&net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 1},
		&net.UDPAddr{IP: net.ParseIP("::ffff:10.0.0.1"), Port: 1},
		&net.UnixAddr{Name: "10.0.0.1:5", Net: "unix"},
	} {
		if got := addrIP(a); got != want {
			t.Errorf("%v: %v", a, got)
		}
	}
	if addrIP(nil).IsValid() || addrIP(&net.UnixAddr{Name: "/tmp/x"}).IsValid() {
		t.Error("an address with no IP")
	}
}

// Registrations the hub refuses.
func TestRegisterRefused(t *testing.T) {
	v := newHub(t, tcpMember(`, "max_peers": 1, "max_links_per_peer": 1`))
	p := v.dial(v.listen())
	if err := p.call(proto.MethodRegister, "not json", nil); err == nil {
		t.Fatal("a registration that does not decode")
	}
	if err := p.register(proto.TypeNode, "lab", "wrong"); err == nil {
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
	if err := p.register(proto.TypeNode, "lab", "pw"); err == nil || !strings.Contains(err.Error(), "already") {
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
	if err := w.dial(w.listen()).register(proto.TypeNode, "lab", "pw"); err == nil || !strings.Contains(err.Error(), "takes tokens only") {
		t.Fatalf("registered without a password: %v", err)
	}
}

// A link that closes while it registers is not registered.
func TestRegisterClosed(t *testing.T) {
	v := newHub(t, tcpMember(""))
	l := link.New(fakeConn{}, v.s.router, nil)
	l.OnClose(nil)
	req := proto.RegisterReq{Info: proto.NewInfo("lab", proto.TypeNode)}
	proto.Sign(&req, "pw")
	body, _ := link.Encode(&req)
	if _, err := v.s.register(l, body); !errors.Is(err, link.ErrClosed) {
		t.Fatalf("registered a closed link: %v", err)
	}
	if len(v.s.Peers()) != 0 {
		t.Fatal("the peer stayed")
	}
}

// A registration's answer proves the hub, and the hub sends nothing else on
// the link until the answer has gone, since the peer serves nothing before
// it; a kick reaches the link even so.
func TestRegisterAnswerFirst(t *testing.T) {
	v := newHub(t, tcpMember(""))
	l := link.New(fakeConn{}, v.s.router, nil)
	req := proto.RegisterReq{Info: proto.NewInfo("lab", proto.TypeNode)}
	proto.Sign(&req, "pw")
	body, _ := link.Encode(&req)
	got, err := v.s.register(l, body)
	if err != nil {
		t.Fatal(err)
	}
	then, ok := got.(*link.Then)
	if !ok {
		t.Fatalf("answered %T", got)
	}
	if err := proto.CheckProof(&req, then.Rsp.(*proto.RegisterRsp), "pw"); err != nil {
		t.Fatal(err)
	}
	peer := v.s.peerByName(proto.TypeNode, "lab")
	if peer == nil {
		t.Fatal("not registered")
	}
	if links := v.s.peerLinks(peer); len(links) != 0 {
		t.Fatalf("%d links to send on before the answer", len(links))
	}
	if err := v.s.call(peer, proto.MethodNodeInfo, nil, nil); !errors.Is(err, errGone) {
		t.Fatalf("called before the answer: %v", err)
	}
	if nodes := v.s.nodeList(0).Nodes; len(nodes) != 1 {
		t.Fatalf("nodes %+v", nodes)
	}
	v.s.mu.Lock()
	all := peer.allLinksLocked()
	v.s.mu.Unlock()
	if len(all) != 1 || all[0] != l {
		t.Fatalf("all links %v", all)
	}
	then.After()
	if links := v.s.peerLinks(peer); len(links) != 1 || links[0] != l {
		t.Fatalf("links after the answer %v", links)
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

// Without allow_proxy_services, no peer sets a node's services through
// the hub: not a proxy, which holds only the shared password, and not a
// node; the node is never asked.
func TestNodeServicesRefused(t *testing.T) {
	v := newHub(t, tcpMember(""))
	n := v.dial(v.listen()).mustRegister(proto.TypeNode, "lab", proto.Service{Name: "a", Addr: "127.0.0.1:1"})
	asked := make(chan struct{}, 4)
	n.mu.Lock()
	n.services = func([]proto.Service) (any, error) {
		asked <- struct{}{}
		info := proto.NewInfo("lab", proto.TypeNode)
		return &info, nil
	}
	n.mu.Unlock()
	evil := []proto.Service{{Name: "x", Addr: "10.0.0.1:6379"}}
	c := v.dial(v.listen()).mustRegister(proto.TypeProxy, "me")
	if err := c.call(proto.MethodSetNodeServices, &proto.SetServicesReq{Node: "lab", Services: evil}, nil); err == nil || !strings.Contains(err.Error(), "allow_proxy_services") {
		t.Fatalf("a proxy set a node's services: %v", err)
	}
	if err := v.apply(tcpMember(`, "allow_proxy_services": true`)); err != nil {
		t.Fatal(err)
	}
	other := v.dial(v.listen()).mustRegister(proto.TypeNode, "other")
	if err := other.call(proto.MethodSetNodeServices, &proto.SetServicesReq{Node: "lab", Services: evil}, nil); err == nil {
		t.Fatal("a node set another node's services")
	}
	select {
	case <-asked:
		t.Fatal("the node was asked")
	default:
	}
	if svc := v.s.peerByName(proto.TypeNode, "lab").Info().Services; len(svc) != 1 || svc[0].Name != "a" {
		t.Fatalf("the node's services changed: %+v", svc)
	}
}

// A proxy sets and refreshes a node's services through the hub.
func TestNodeServices(t *testing.T) {
	v := newHub(t, tcpMember(`, "allow_proxy_services": true`))
	n := v.dial(v.listen()).mustRegister(proto.TypeNode, "lab", proto.Service{Name: "a", Addr: "127.0.0.1:1"})
	n2 := v.dial(v.listen()).mustRegister(proto.TypeNode, "beta")
	c := v.dial(v.listen()).mustRegister(proto.TypeProxy, "me")
	if list := v.s.nodeList(0); len(list.Nodes) != 2 || list.Nodes[0].Info.Name != "beta" {
		t.Fatalf("nodes %+v", list.Nodes)
	}
	if err := c.call(proto.MethodSetNodeServices, "not json", nil); err == nil {
		t.Fatal("a request that does not decode")
	}
	if err := c.call(proto.MethodSetNodeServices, &proto.SetServicesReq{Node: "nobody"}, nil); err == nil {
		t.Fatal("set the services of no node")
	}
	// No services are an empty list.
	var info proto.Info
	if err := c.call(proto.MethodSetNodeServices, &proto.SetServicesReq{Node: "lab"}, &info); err != nil || len(info.Services) != 0 {
		t.Fatalf("%+v %v", info, err)
	}
	want := []proto.Service{{Name: "b", Addr: "127.0.0.1:2"}}
	if err := c.call(proto.MethodSetNodeServices, &proto.SetServicesReq{Node: "lab", Services: want}, &info); err != nil || len(info.Services) != 1 {
		t.Fatalf("%+v %v", info, err)
	}
	node := v.s.peerByName(proto.TypeNode, "lab")
	if info, err := v.s.refreshNode(node); err != nil || info.Services[0].Name != "b" {
		t.Fatalf("refresh: %+v %v", info, err)
	}

	// A node that fails, or answers as another, changes nothing.
	n.mu.Lock()
	n.services = func([]proto.Service) (any, error) { return nil, errors.New("no") }
	n.nodeInfo = func() (any, error) { return nil, errors.New("no") }
	n.mu.Unlock()
	if err := c.call(proto.MethodSetNodeServices, &proto.SetServicesReq{Node: "lab", Services: want}, nil); err == nil {
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
// readyLink is p's first link the hub may send on. The hub marks a link so
// once its answer to the registration has gone out, which may be after the
// registering side has read the answer: so it waits for it.
func readyLink(t *testing.T, v *env, p *Peer) *link.Link {
	t.Helper()
	var l *link.Link
	waitFor(t, "a link of "+p.info.Name+" to send on", func() bool {
		if links := v.s.peerLinks(p); len(links) > 0 {
			l = links[0]
		}
		return l != nil
	})
	return l
}

func TestOpenTunnelLinkFull(t *testing.T) {
	v := newHub(t, tcpMember(""))
	svc := proto.Service{Name: "echo", Addr: "127.0.0.1:1"}
	v.dial(v.listen()).mustRegister(proto.TypeNode, "lab", svc)
	c := v.dial(v.listen()).mustRegister(proto.TypeProxy, "me")
	other := link.New(fakeConn{}, v.s.router, nil)
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
	proxyLink := readyLink(t, v, v.s.peerByName(proto.TypeProxy, "me"))
	fill(func(r *Route) { r.ProxyLink = proxyLink })
	if err := c.call(proto.MethodTunnelOpen, openReq(c, 1, "lab", "echo"), nil); err == nil || !strings.Contains(err.Error(), "the link carries") {
		t.Fatalf("a full proxy link: %v", err)
	}
	node := v.s.peerByName(proto.TypeNode, "lab")
	nodeLink := readyLink(t, v, node)
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
	// The hub picks among the links it may send on, which a link is only
	// once its answer has gone out (readyLink): both of the node's first.
	node := v.s.peerByName(proto.TypeNode, "lab")
	waitFor(t, "both of the node's links to send on", func() bool { return len(v.s.peerLinks(node)) == 2 })
	readyLink(t, v, v.s.peerByName(proto.TypeProxy, "me"))
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

// An end that breaks its flow control has its tunnel closed by the hub,
// at both ends, before what it sends past its window fills the link it
// shares with other tunnels; the links stay, and so do the other tunnels.
func TestRouteWindow(t *testing.T) {
	v := newHub(t, tcpMember(""))
	svc := proto.Service{Name: "echo", Addr: "127.0.0.1:1"}
	n := v.dial(v.listen()).mustRegister(proto.TypeNode, "lab", svc)
	c := v.dial(v.listen()).mustRegister(proto.TypeProxy, "me")
	open := func(id uint32) *proto.TunnelOpenReq {
		req := openReq(c, id, "lab", "echo")
		if err := c.call(proto.MethodTunnelOpen, req, nil); err != nil {
			t.Fatal(err)
		}
		return req
	}
	closed := func(p *peer, id uint64) {
		t.Helper()
		for {
			got, _, _ := proto.ParseFrame(p.wait(proto.MethodTunnelClose).body)
			if got == id {
				return
			}
		}
	}

	// An ack of nothing, or of more than was sent, from either end.
	for i, bad := range []struct {
		from *peer
		n    int64
	}{{n, proto.MaxInFlight}, {c, 1}, {n, 0}, {c, -5}} {
		req := open(uint32(10 + i))
		_ = bad.from.l.SendTunnel(proto.MethodTunnelAck, req.ID, proto.AckPayload(bad.n))
		closed(c, req.ID)
		closed(n, req.ID)
	}

	// A proxy that sends past its window: the frames past it are not
	// passed on.
	keep, flood := open(20), open(21)
	chunk := make([]byte, proto.MaxChunk)
	for sent := 0; sent <= proto.MaxInFlight; sent += len(chunk) {
		_ = c.l.SendTunnel(proto.MethodTunnelData, flood.ID, chunk)
	}
	closed(c, flood.ID)
	closed(n, flood.ID)
	waitFor(t, "the flooded route to go", func() bool { return len(v.s.Routes()) == 1 })
	// What an end acknowledges gives its window back: an end that keeps
	// to it sends any amount.
	sent := 0
	for sent < 2*proto.MaxInFlight {
		_ = c.l.SendTunnel(proto.MethodTunnelData, keep.ID, chunk)
		sent += len(chunk)
		n.wait(proto.MethodTunnelData)
		_ = n.l.SendTunnel(proto.MethodTunnelAck, keep.ID, proto.AckPayload(int64(len(chunk))))
	}
	_ = c.l.SendTunnel(proto.MethodTunnelData, keep.ID, []byte("end"))
	n.wait(proto.MethodTunnelData)
	if r := v.s.route(keep.ID); r == nil || r.up.Load() != int64(sent+3) {
		t.Fatalf("the kept tunnel: %+v", r)
	}
	if routes := v.s.Routes(); len(routes) != 1 || routes[0].ID != keep.ID {
		t.Fatalf("routes %+v", routes)
	}
	select {
	case <-c.l.Done():
		t.Fatal("the proxy's link closed")
	case <-n.l.Done():
		t.Fatal("the node's link closed")
	default:
	}
}

// tls and wss listeners present the hub's certificate, made and kept as a
// quic listener's is; a listener with cert_file presents its own, read
// again at each Apply, and one whose files do not load is shown with why.
func TestListenerCerts(t *testing.T) {
	v := newHub(t, `{"password": "pw", "listeners": [{"protocol": "wss", "address": "127.0.0.1:0"}, {"protocol": "tls", "address": "127.0.0.1:0"}]}`)
	if err := v.waitEdit(); err != nil || !strings.Contains(v.current(), "BEGIN CERTIFICATE") {
		t.Fatalf("the certificate kept: %v %s", err, v.current())
	}
	st := v.s.Status()
	if st.Fingerprint == "" || st.QUICFingerprint != st.Fingerprint || len(st.Listeners) != 2 {
		t.Fatalf("status %+v", st)
	}
	for _, l := range st.Listeners {
		if l.Error != "" || l.Fingerprint != st.Fingerprint || l.OwnCert || !strings.HasPrefix(l.URL, l.Protocol+"://") {
			t.Fatalf("listener %+v", l)
		}
	}
	// A listener's own certificate, from its files.
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	write := func() string {
		c, k, err := transport.NewSelfSigned("127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(certFile, c, 0o600)
		_ = os.WriteFile(keyFile, k, 0o600)
		cert, _ := tls.X509KeyPair(c, k)
		return transport.Fingerprint(cert)
	}
	fp := write()
	cert, key := hubCertOf(t, v.current())
	own := fmt.Sprintf(`{"password": "pw", "quic": {"cert": %q, "key": %q}, "listeners": [{"protocol": "wss", "address": "127.0.0.1:0"}, {"protocol": "tls", "address": "127.0.0.1:0", "cert_file": %q, "key_file": %q}]}`,
		cert, key, certFile, keyFile)
	if err := v.apply(own); err != nil {
		t.Fatal(err)
	}
	st = v.s.Status()
	wssURL := st.Listeners[0].URL
	if l := st.Listeners[1]; l.Error != "" || l.Fingerprint != fp || !l.OwnCert || st.Listeners[0].Fingerprint != st.Fingerprint {
		t.Fatalf("listeners %+v", st.Listeners)
	}
	// A new certificate in the files opens the listener anew; the other
	// stays.
	fp2 := write()
	if err := v.apply(own); err != nil {
		t.Fatal(err)
	}
	st = v.s.Status()
	if st.Listeners[1].Fingerprint != fp2 || st.Listeners[0].URL != wssURL {
		t.Fatalf("listeners %+v", st.Listeners)
	}
	_ = os.Remove(keyFile)
	if err := v.apply(own); err == nil || !strings.Contains(err.Error(), "the certificate") {
		t.Fatalf("a listener whose key is gone: %v", err)
	}
	if st = v.s.Status(); st.Listeners[1].Error == "" || st.Listeners[1].URL != "" {
		t.Fatalf("listeners %+v", st.Listeners)
	}
}

// hubCertOf is the hub's certificate and key in member, as the hub wrote
// them.
func hubCertOf(t *testing.T, member string) (string, string) {
	t.Helper()
	var m struct {
		QUIC struct {
			Cert string `json:"cert"`
			Key  string `json:"key"`
		} `json:"quic"`
	}
	if err := json.Unmarshal([]byte(member), &m); err != nil || m.QUIC.Key == "" {
		t.Fatalf("the certificate in %s: %v", member, err)
	}
	return m.QUIC.Cert, m.QUIC.Key
}
