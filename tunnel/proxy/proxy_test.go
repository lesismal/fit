//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd || windows

package proxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/tunnel/internal/common"
	"github.com/lesismal/fit/tunnel/internal/tunneltest"
	"github.com/lesismal/fit/tunnel/internal/uplink"
)

// hubDoc is a hub with a console socket, a node reaching echo, and a
// proxy with a console, a forward to echo and a web proxy to the socket.
func hubDoc(t *testing.T, listen, sock, echo, fwd, proxy, con, extra string) string {
	t.Helper()
	return fmt.Sprintf(`{"tunnel": {
 "hub": {"name": "hub", "password": "pw", "allow_proxy_services": true, "listeners": [{"protocol": "tcp", "address": %q}], "console": %s, "console_socket": {"address": %q, "password": "sock"}},
 "node": {"name": "lab", "allow_hub_services": true, "hubs": [%s], "services": [{"name": "echo", "address": %q}]},
 "proxy": {"name": "me", "hubs": [%s], "forwards": [{"listen": %q, "node": "lab", "service": "echo"}],
  "web_proxies": [{"name": "hub", "listen": %q, "url": "ws://%s/", "password": "sock"}], "console": %s%s}
}}`, listen, consoleJSON(freeAddr(t)), sock, uplinkJSON("hub", listen), echo, uplinkJSON("hub", listen), fwd, proxy, sock, consoleJSON(con), extra)
}

// The pages' API, and the proxy's state as the changes it makes apply.
func TestProxyAPI(t *testing.T) {
	listen, sock, fwd, proxy, con := freeAddr(t), freeAddr(t), freeAddr(t), freeAddr(t), freeAddr(t)
	echo := tunneltest.Echo(t, "")
	h := start(t, hubDoc(t, listen, sock, echo, fwd, proxy, con, ""))
	h.waitNode("hub", "lab", "echo")
	p := login(t, con)

	// Two tunnels, listed oldest first, one closed from the pages.
	echoOnce(t, fwd, "one")
	echoOnce(t, fwd, "two")
	tunnels := h.c.Tunnels()
	if len(tunnels) != 2 || tunnels[0].Created.After(tunnels[1].Created) || tunnels[0].Hub != "hub" {
		t.Fatalf("tunnels %+v", tunnels)
	}
	id := strconv.FormatUint(tunnels[0].ID, 10)
	p.expect("/api/tunnels/close", map[string]string{"hub": "hub", "id": "x"}, 400, "not a tunnel's")
	p.expect("/api/tunnels/close", map[string]string{"hub": "nope", "id": id}, 404, "no such hub")
	p.expect("/api/tunnels/close", map[string]string{"hub": "hub", "id": "999999999"}, 404, "no such tunnel")
	p.expect("/api/tunnels/close", map[string]string{"hub": "hub", "id": id}, 200, "ok")
	waitFor(t, "the tunnel to close", func() bool { return len(h.c.Tunnels()) == 1 })

	// The nodes, asked for again.
	p.expect("/api/nodes/refresh", map[string]string{"hub": "nope"}, 404, "no such hub")
	p.expect("/api/nodes/refresh", map[string]string{"hub": "hub"}, 200, `"lab"`)

	// A node's services, set through the hub.
	extra := tunneltest.Echo(t, "")
	p.expect("/api/nodes/services", map[string]any{"hub": "nope", "node": "lab"}, 404, "no such hub")
	p.expect("/api/nodes/services", map[string]any{"hub": "hub", "node": "ghost", "services": []ServiceView{}}, 502, "ghost")
	p.expect("/api/nodes/services", map[string]any{"hub": "hub", "node": "lab", "services": []ServiceView{
		{Name: "echo", Address: echo}, {Name: " extra ", Address: " " + extra + " ", Description: "more"}}}, 200, "ok")
	h.waitNode("hub", "lab", "extra")
	if _, err := h.c.SetNodeServices(h.c.uplink("hub"), "ghost", nil); err == nil {
		t.Fatal("the services of a missing node were set")
	}

	// The meta, told to the hubs the proxy keeps.
	p.expect("/api/meta", map[string]string{"site": "home"}, 200, "ok")
	if got := h.c.Status().Meta["site"]; got != "home" {
		t.Fatalf("meta %q", got)
	}

	// Forwards: one listened on, one refused, one taken.
	byHub, toMissing, badService, taken := freeAddr(t), freeAddr(t), freeAddr(t), freeAddr(t)
	p.expect("/api/forwards", map[string]string{"listen": fwd, "node": "lab", "service": "echo"}, 400, "forwarded already")
	p.expect("/api/forwards", map[string]string{"listen": byHub, "node": "lab", "service": "extra", "hub": " hub "}, 200, "ok")
	p.expect("/api/forwards", map[string]string{"listen": toMissing, "node": "ghost", "service": "echo"}, 200, "ok")
	p.expect("/api/forwards", map[string]string{"listen": badService, "node": "lab", "service": "nope", "hub": "hub"}, 200, "ok")
	echoOnce(t, byHub, "by name")
	refused(t, toMissing)
	refused(t, badService)
	l, err := net.Listen("tcp", taken)
	if err != nil {
		t.Fatal(err)
	}
	p.expect("/api/forwards", map[string]string{"listen": taken, "node": "lab", "service": "echo"}, 400, "forward "+taken)
	var fv *ForwardView
	for _, f := range h.c.Forwards() {
		if f.Listen == taken {
			fv = &f
		}
	}
	if fv == nil || fv.Error == "" {
		t.Fatalf("forwards %+v", h.c.Forwards())
	}
	_ = l.Close()
	p.expect("/api/forwards/delete", map[string]string{"listen": "127.0.0.1:1"}, 400, "not forwarded")
	for _, a := range []string{taken, toMissing, badService} {
		p.expect("/api/forwards/delete", map[string]string{"listen": a}, 200, "ok")
	}

	// A hub that does not answer, a forward through it, and its
	// changes.
	far := freeAddr(t)
	ep := []common.EndpointReq{{Protocol: "tcp", Address: far, Enabled: true}}
	p.expect("/api/hubs", common.HubReq{Name: "far", Endpoints: ep, Password: "pw"}, 200, "ok")
	p.expect("/api/hubs", common.HubReq{Name: "far", Endpoints: ep, Password: "pw"}, 400, "exists")
	if u := h.c.uplink("far"); u == nil {
		t.Fatal("no hub far")
	}
	p.expect("/api/nodes/refresh", map[string]string{"hub": "far"}, 502, "")
	viaFar := freeAddr(t)
	p.expect("/api/forwards", map[string]string{"listen": viaFar, "node": "lab", "service": "echo", "hub": "far"}, 200, "ok")
	refused(t, viaFar)
	// A forward to any hub skips the one not registered.
	echoOnce(t, fwd, "still")
	p.expect("/api/hubs/update", map[string]any{"name": "far", "hub": common.HubReq{Name: "far", Endpoints: ep, PoolSize: 1}}, 200, "ok")
	p.expect("/api/hubs/update", map[string]any{"name": "nope", "hub": common.HubReq{Endpoints: ep}}, 400, "no hub")
	p.expect("/api/forwards/delete", map[string]string{"listen": viaFar}, 200, "ok")
	p.expect("/api/hubs/delete", map[string]string{"name": "far"}, 200, "ok")
	p.expect("/api/hubs/delete", map[string]string{"name": "far"}, 400, "no hub")
	if h.c.uplink("far") != nil {
		t.Fatal("hub far is still linked")
	}

	// Bodies the pages send that do not decode.
	for _, path := range []string{"/api/hubs", "/api/hubs/update", "/api/hubs/delete", "/api/nodes/refresh",
		"/api/nodes/services", "/api/meta", "/api/forwards", "/api/forwards/delete", "/api/tunnels/close"} {
		if code, _ := p.do("POST", path, "not an object", nil); code != 400 {
			t.Errorf("%s: %d", path, code)
		}
	}

	// The status, with the nodes' shares.
	var st Status
	if code, s := p.do("GET", "/api/status", nil, &st); code != 200 || st.Name != "me" || len(st.WebProxies) != 1 || len(st.Hubs) != 1 ||
		len(st.Hubs[0].Nodes) != 1 || st.Hubs[0].Nodes[0].Share != 1 {
		t.Fatalf("status %d %s", code, s)
	}
}

// pick refuses what no hub can carry, and a forward past max_tunnels
// refuses a connection.
func TestPick(t *testing.T) {
	listen, sock, fwd, proxy, con := freeAddr(t), freeAddr(t), freeAddr(t), freeAddr(t), freeAddr(t)
	h := start(t, hubDoc(t, listen, sock, tunneltest.Echo(t, ""), fwd, proxy, con, `, "max_tunnels": 1`))
	h.waitNode("hub", "lab", "echo")
	echoOnce(t, fwd, "held")
	refused(t, fwd)
	if _, err := h.c.pick(&config.TunnelForward{Hub: "nope"}); err == nil {
		t.Fatal("a missing hub was picked")
	}
	if _, err := h.c.pick(&config.TunnelForward{Node: "lab", Service: "nope"}); err == nil {
		t.Fatal("a missing service was picked")
	}
	u := h.c.uplink("hub")
	// A hub whose nodes are not known yet is passed over.
	h.c.mu.Lock()
	list := h.c.nodes[u]
	delete(h.c.nodes, u)
	h.c.mu.Unlock()
	_, _ = h.c.pick(&config.TunnelForward{Node: "lab", Service: "echo"})
	h.c.setNodeLatency(u, nil)
	h.c.setNodes(u, list)
	if got, err := h.c.pick(&config.TunnelForward{Node: "lab", Service: "echo"}); err != nil || got != u {
		t.Fatalf("%v, %v", got, err)
	}
	// Latency of an uplink the proxy does not have changes nothing.
	h.c.setNodeLatency(&uplink.Uplink{}, nil)
}

// The tunnels idle past idle_timeout are closed.
func TestSweepIdle(t *testing.T) {
	listen, sock, fwd, proxy, con := freeAddr(t), freeAddr(t), freeAddr(t), freeAddr(t), freeAddr(t)
	h := start(t, hubDoc(t, listen, sock, tunneltest.Echo(t, ""), fwd, proxy, con, `, "idle_timeout": "100ms"`))
	h.waitNode("hub", "lab", "echo")
	echoOnce(t, fwd, "x")
	waitFor(t, "the idle tunnel to close", func() bool { return len(h.c.Tunnels()) == 0 })
}

// Apply refuses what is not a proxy's member, and a first Apply that does
// not open everything opens nothing.
func TestApplyRefuses(t *testing.T) {
	c := New(&app.Host{ID: ID, Engine: tunneltest.Engine(t)})
	if info := c.Info(); info.Name != "" {
		t.Fatalf("info before Apply: %+v", info)
	}
	if err := c.Apply(nil); err == nil {
		t.Fatal("nil was applied")
	}
	if err := c.Apply((*config.TunnelProxy)(nil)); err == nil {
		t.Fatal("a nil member was applied")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	free := freeAddr(t)
	cfg := &config.TunnelProxy{Name: "me",
		Forwards:   []config.TunnelForward{{Listen: free, Node: "n", Service: "s"}, {Listen: l.Addr().String(), Node: "n", Service: "s"}},
		WebProxies: []config.TunnelWebProxy{{Listen: l.Addr().String(), URL: "ws://127.0.0.1:1/", Password: "p"}},
		Console:    &config.RoleConsole{Address: "127.0.0.1:bad", Users: []config.ConsoleUser{{Username: "a", PasswordHash: hashOnce()}}},
	}
	err = c.Apply(cfg)
	for _, want := range []string{"console", "forward " + l.Addr().String(), "web proxy " + l.Addr().String()} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Apply = %v, want %q", err, want)
		}
	}
	if fs := c.Forwards(); len(fs) != 0 {
		t.Fatalf("forwards left open: %+v", fs)
	}
	if conn, err := net.Dial("tcp", free); err == nil {
		conn.Close()
		t.Fatal("a forward of a failed Apply listens")
	}
	c.Stop(context.Background())
	c.Stop(context.Background())
	if err := c.Apply(&config.TunnelProxy{}); !errors.Is(err, app.ErrStopped) {
		t.Fatalf("Apply after Stop: %v", err)
	}
}

// After the first Apply, what does not open is shown with its error, and
// the rest runs.
func TestApplyAfterStart(t *testing.T) {
	c := New(&app.Host{ID: ID, Engine: tunneltest.Engine(t)})
	defer c.Stop(context.Background())
	if err := c.Apply(&config.TunnelProxy{Name: "me"}); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	err = c.Apply(&config.TunnelProxy{Name: "me", WebProxies: []config.TunnelWebProxy{{Listen: l.Addr().String(), URL: "ws://127.0.0.1:1/", Password: "p"}}})
	if err == nil || !strings.Contains(err.Error(), "web proxy") {
		t.Fatalf("Apply = %v", err)
	}
	if v := c.WebProxies(); len(v) != 1 || v[0].Error == "" || v[0].Addr != "" {
		t.Fatalf("web proxies %+v", v)
	}
}

// The role registered runs a proxy when the config has one.
func TestRegistered(t *testing.T) {
	m := app.NewManager(tunneltest.Engine(t), nil, nil, nil)
	defer m.Stop(context.Background())
	if err := m.Apply(&config.Config{}); err != nil {
		t.Fatal(err)
	}
	<-hosts
	if err := m.Apply(&config.Config{Tunnel: &config.Tunnel{Proxy: &config.TunnelProxy{Name: "me"}}}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(m.Running(), ID) {
		t.Fatalf("running %v", m.Running())
	}
}
