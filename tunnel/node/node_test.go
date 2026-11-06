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
	// Nothing listens on port 1, a privileged port no test binds: a
	// free port let go of could be another process's by the dial.
	echo, dead := tunneltest.Echo(t, ""), "127.0.0.1:1"
	if err := v.apply(fmt.Sprintf(`{"name": "lab", "max_tunnels": 2, "allow_hub_services": true, "hubs": [%s], "services": [{"name": "echo", "address": %q}, {"name": "dead", "address": %q}]}`,
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

	// A config that takes longer than call_timeout to change.
	v.holdEdits()
	if err := l.Call(proto.MethodNodeSetServices, []proto.Service{}, &info, 5*time.Second); err == nil || !strings.Contains(err.Error(), "too long") {
		t.Fatalf("a slow config: %v", err)
	}
	v.release()
}

// Without allow_hub_services, a hub, or anyone who registered with it,
// cannot point the node at an address of its choosing: the node refuses,
// and its config and services stay as they are.
func TestHubServicesRefused(t *testing.T) {
	v := newEnv(t)
	s := newHub(t, v.e)
	echo := tunneltest.Echo(t, "")
	if err := v.apply(fmt.Sprintf(`{"name": "lab", "hubs": [%s], "services": [{"name": "echo", "address": %q}]}`, s.uplinkJSON(""), echo)); err != nil {
		t.Fatal(err)
	}
	l := s.link()
	before := v.current()
	err := l.Call(proto.MethodNodeSetServices, []proto.Service{{Name: "x", Addr: "10.0.0.1:6379"}}, nil, 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "allow_hub_services") {
		t.Fatalf("a hub set the services: %v", err)
	}
	if v.current() != before || len(v.n.Info().Services) != 1 || v.n.Info().Services[0].Addr != echo {
		t.Fatalf("member %s, info %+v", v.current(), v.n.Info())
	}
	if err := open(l, 1, "x"); err == nil {
		t.Fatal("a tunnel to the address a hub tried to set")
	}
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
