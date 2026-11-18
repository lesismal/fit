package node

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/tunnel/internal/link"
	"github.com/lesismal/fit/tunnel/internal/proto"
	"github.com/lesismal/fit/tunnel/internal/tunnel"
	"github.com/lesismal/fit/tunnel/internal/tunneltest"
)

// A console that does not start fails the first Apply, and is returned
// beside the rest applied after; an Apply after Stop is refused.
func TestApply(t *testing.T) {
	v := newEnv(t)
	if info := v.n.Info(); info.Name != "" {
		t.Fatalf("info before the first Apply: %+v", info)
	}
	for _, member := range []any{nil, (*config.TunnelNode)(nil), &config.TunnelHub{}} {
		if err := v.n.Apply(member); err == nil {
			t.Fatalf("applied %T", member)
		}
	}
	busy := consoleJSON(busyAddr(t))
	if err := v.apply(`{"name": "lab", "console": ` + busy + `}`); err == nil || v.n.started {
		t.Fatalf("a first Apply with a busy console: %v", err)
	}
	if err := v.apply(`{"name": "lab"}`); err != nil {
		t.Fatal(err)
	}
	if err := v.apply(`{"name": "lab", "meta": {"a": "b"}, "console": ` + busy + `}`); err == nil || v.n.Config().Meta["a"] != "b" {
		t.Fatalf("a later Apply with a busy console: %v", err)
	}
	v.n.Stop(context.Background())
	v.n.Stop(context.Background())
	if err := v.apply(`{"name": "lab"}`); !errors.Is(err, app.ErrStopped) {
		t.Fatalf("an Apply after Stop: %v", err)
	}
}

// open asks the node, over l, to open tunnel id to service.
func open(l *link.Link, id uint64, service string) error {
	var rsp proto.TunnelOpenRsp
	return l.Call(proto.MethodTunnelOpen, &proto.TunnelOpenReq{ID: id, Service: service, Proxy: "me"}, &rsp, 5*time.Second)
}

// What a hub calls on the node: its info, its services, and tunnels it
// opens, refuses or fails to.
func TestHubCalls(t *testing.T) {
	v := newEnv(t)
	s := newHub(t, v.e)
	echo, dead := tunneltest.Echo(t, ""), freeAddr(t)
	if err := v.apply(fmt.Sprintf(`{"name": "lab", "max_tunnels": 2, "hubs": [%s], "services": [{"name": "echo", "address": %q}, {"name": "dead", "address": %q}],
 "remote_services": {"enabled": true, "allow": ["127.0.0.1:*"]}}`,
		s.uplinkJSON(`, "call_timeout": "1s"`), echo, dead)); err != nil {
		t.Fatal(err)
	}
	l := s.link()
	var info proto.Info
	if err := l.Call(proto.MethodNodeInfo, nil, &info, 5*time.Second); err != nil || info.Name != "lab" || len(info.Services) != 2 {
		t.Fatalf("info %+v, %v", info, err)
	}
	if err := l.Call(proto.MethodNodeSetServices, "not json", nil, 5*time.Second); err == nil {
		t.Fatal("services that do not decode")
	}
	if err := l.Call(proto.MethodTunnelOpen, "not json", nil, 5*time.Second); err == nil {
		t.Fatal("a tunnel request that does not decode")
	}
	if err := open(l, 1, "nothing"); err == nil || !strings.Contains(err.Error(), "no service") {
		t.Fatalf("a tunnel to no service: %v", err)
	}
	if err := open(l, 1, "echo"); err != nil {
		t.Fatal(err)
	}
	if err := open(l, 1, "echo"); err == nil || err.Error() != tunnel.ErrTaken.Error() {
		t.Fatalf("an ID in use: %v", err)
	}
	if err := open(l, 2, "echo"); err != nil {
		t.Fatal(err)
	}
	if ts := v.n.Tunnels(); len(ts) != 2 || ts[0].Created.After(ts[1].Created) {
		t.Fatalf("tunnels %+v", ts)
	}
	if err := open(l, 3, "echo"); err == nil || err.Error() != tunnel.ErrFull.Error() {
		t.Fatalf("past max_tunnels: %v", err)
	}
	if err := open(l, 4, "dead"); err == nil {
		t.Fatal("a tunnel to a service that is down")
	}
	if rtt := v.n.ServiceRTT(); len(rtt) != 2 || rtt["dead"].Loss == 0 {
		t.Fatalf("service RTT %+v", rtt)
	}

	// The hub sets the services: the node's config changes, what was
	// measured of the services that changed goes, and the node tells the
	// hub, which may fail.
	echo2 := tunneltest.Echo(t, "")
	if err := l.Call(proto.MethodNodeSetServices, []proto.Service{{Name: "echo", Addr: echo2}}, &info, 5*time.Second); err != nil || len(info.Services) != 1 || info.Services[0].Addr != echo2 {
		t.Fatalf("info %+v, %v", info, err)
	}
	if !strings.Contains(v.current(), echo2) || len(v.n.ServiceRTT()) != 0 {
		t.Fatalf("member %s, service RTT %+v", v.current(), v.n.ServiceRTT())
	}
	waitUpdate(t, s, echo2)
	s.mu.Lock()
	s.update = func(proto.Info) error { return errors.New("refused") }
	s.mu.Unlock()
	if err := l.Call(proto.MethodNodeSetServices, []proto.Service{{Name: "echo", Addr: echo}}, &info, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	waitUpdate(t, s, echo)

	// An address the allow list does not hold is refused.
	if err := l.Call(proto.MethodNodeSetServices, []proto.Service{{Name: "meta", Addr: "169.254.169.254:80"}}, &info, 5*time.Second); err == nil || !strings.Contains(err.Error(), "169.254.169.254:80") {
		t.Fatalf("an address the allow list does not hold: %v", err)
	}
	if strings.Contains(v.current(), "169.254") {
		t.Fatalf("written: %s", v.current())
	}

	// A config that takes longer than call_timeout to change.
	v.holdEdits()
	if err := l.Call(proto.MethodNodeSetServices, []proto.Service{}, &info, 5*time.Second); err == nil || !strings.Contains(err.Error(), "too long") {
		t.Fatalf("a slow config: %v", err)
	}
	v.release()
}

// waitUpdate waits for the node to tell s of a service at addr.
func waitUpdate(t *testing.T, s *hub, addr string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case info := <-s.updates:
			if len(info.Services) == 1 && info.Services[0].Addr == addr {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for an update to %s", addr)
		}
	}
}

// Tunnels idle past idle_timeout close.
func TestSweep(t *testing.T) {
	v := newEnv(t)
	s := newHub(t, v.e)
	if err := v.apply(fmt.Sprintf(`{"name": "lab", "idle_timeout": "50ms", "hubs": [%s], "services": [{"name": "echo", "address": %q}]}`,
		s.uplinkJSON(""), tunneltest.Echo(t, ""))); err != nil {
		t.Fatal(err)
	}
	if err := open(s.link(), 1, "echo"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the idle tunnel to close", func() bool { return len(v.n.Tunnels()) == 0 })
}

// The console's API: what it refuses, and the tunnels and hubs it
// closes.
func TestPages(t *testing.T) {
	v := newEnv(t)
	s := newHub(t, v.e)
	if err := v.apply(fmt.Sprintf(`{"name": "lab", "hubs": [%s], "services": [{"name": "echo", "address": %q}], "console": %s}`,
		s.uplinkJSON(""), tunneltest.Echo(t, ""), consoleJSON("127.0.0.1:0"))); err != nil {
		t.Fatal(err)
	}
	pg := login(t, v.n.console.Addr())
	for _, path := range []string{"/api/hubs", "/api/hubs/update", "/api/hubs/delete", "/api/services", "/api/tunnels/close"} {
		pg.expect(400, path, "not json")
	}
	if err := open(s.link(), 7, "echo"); err != nil {
		t.Fatal(err)
	}
	pg.expect(400, "/api/tunnels/close", map[string]string{"hub": "hub", "id": "x"})
	pg.expect(404, "/api/tunnels/close", map[string]string{"hub": "nope", "id": "7"})
	pg.expect(404, "/api/tunnels/close", map[string]string{"hub": "hub", "id": "8"})
	pg.expect(200, "/api/tunnels/close", map[string]string{"hub": "hub", "id": "7"})
	waitFor(t, "the tunnel to close", func() bool { return len(v.n.Tunnels()) == 0 })
	pg.expect(400, "/api/hubs/delete", map[string]string{"name": "nope"})
	pg.expect(200, "/api/hubs/delete", map[string]string{"name": "hub"})
	if len(v.n.Uplinks()) != 0 || strings.Contains(v.current(), "hub") {
		t.Fatalf("the hub stayed: %s", v.current())
	}
}

// A hub may not set the node's services unless remote_services lets it,
// and then only to the addresses its allow list holds.
func TestRemoteServices(t *testing.T) {
	v := newEnv(t)
	s := newHub(t, v.e)
	echo := tunneltest.Echo(t, "")
	member := func(remote string) string {
		return fmt.Sprintf(`{"name": "lab", "hubs": [%s], "services": [{"name": "echo", "address": %q}]%s}`, s.uplinkJSON(""), echo, remote)
	}
	evil := []proto.Service{{Name: "ssh", Addr: "127.0.0.1:22"}, {Name: "meta", Addr: "169.254.169.254:80"}, {Name: "lan", Addr: "192.168.1.1:445"}}
	for _, remote := range []string{"", `, "remote_services": {}`, `, "remote_services": {"allow": ["*:*"]}`} {
		if err := v.apply(member(remote)); err != nil {
			t.Fatal(err)
		}
		l := s.link()
		if err := l.Call(proto.MethodNodeSetServices, evil, nil, 5*time.Second); err == nil || !strings.Contains(err.Error(), "remote_services") {
			t.Fatalf("%q: a hub set the services: %v", remote, err)
		}
		if strings.Contains(v.current(), "169.254") || v.n.Config().Services[0].Address != echo {
			t.Fatalf("%q: the services changed: %s", remote, v.current())
		}
		// Nor does a tunnel go anywhere but to the node's own services.
		if err := open(l, 1, "ssh"); err == nil || !strings.Contains(err.Error(), "no service") {
			t.Fatalf("%q: a tunnel to a service the node lacks: %v", remote, err)
		}
		// A hub that is gone, and its link.
		_ = l.Close()
	}
	// Enabled without a list: any address.
	if err := v.apply(member(`, "remote_services": {"enabled": true}`)); err != nil {
		t.Fatal(err)
	}
	var info proto.Info
	if err := s.link().Call(proto.MethodNodeSetServices, evil[:1], &info, 5*time.Second); err != nil || info.Services[0].Addr != "127.0.0.1:22" {
		t.Fatalf("%+v, %v", info, err)
	}
}

// A hub that does not know the password - a fake one, or one between the
// node and the hub - is not registered with, and nothing it calls is
// served: it neither sets the node's services nor opens a tunnel.
func TestFakeHub(t *testing.T) {
	v := newEnv(t)
	fake := newHubWith(t, v.e, "a guess")
	if err := v.apply(fmt.Sprintf(`{"name": "lab", "hubs": [%s], "services": [{"name": "echo", "address": %q}], "remote_services": {"enabled": true}}`,
		fake.uplinkJSON(""), tunneltest.Echo(t, ""))); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the registration to fail", func() bool {
		st := v.n.Uplinks()[0].Status()
		return st.State == "failed" && strings.Contains(st.Error, "decrypt")
	})
	select {
	case <-fake.links:
		t.Fatal("the node registered with a fake hub")
	default:
	}
	if strings.Contains(v.current(), "127.0.0.1:22") || len(v.n.Tunnels()) != 0 {
		t.Fatalf("the fake hub changed the node: %s", v.current())
	}
}
