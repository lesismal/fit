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
	for _, member := range []any{nil, (*config.TunnelNode)(nil), &config.TunnelServer{}} {
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
	return l.Call(proto.MethodTunnelOpen, &proto.TunnelOpenReq{ID: id, Service: service, Client: "me"}, &rsp, 5*time.Second)
}

// What a server calls on the node: its info, its services, and tunnels it
// opens, refuses or fails to.
func TestServerCalls(t *testing.T) {
	v := newEnv(t)
	s := newServer(t, v.e)
	echo, dead := tunneltest.Echo(t, ""), freeAddr(t)
	if err := v.apply(fmt.Sprintf(`{"name": "lab", "max_tunnels": 2, "servers": [%s], "services": [{"name": "echo", "address": %q}, {"name": "dead", "address": %q}]}`,
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

	// The server sets the services: the node's config changes, what was
	// measured of the services that changed goes, and the node tells the
	// server, which may fail.
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

// waitUpdate waits for the node to tell s of a service at addr.
func waitUpdate(t *testing.T, s *server, addr string) {
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
	s := newServer(t, v.e)
	if err := v.apply(fmt.Sprintf(`{"name": "lab", "idle_timeout": "50ms", "servers": [%s], "services": [{"name": "echo", "address": %q}]}`,
		s.uplinkJSON(""), tunneltest.Echo(t, ""))); err != nil {
		t.Fatal(err)
	}
	if err := open(s.link(), 1, "echo"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the idle tunnel to close", func() bool { return len(v.n.Tunnels()) == 0 })
}

// The console's API: what it refuses, and the tunnels and servers it
// closes.
func TestPages(t *testing.T) {
	v := newEnv(t)
	s := newServer(t, v.e)
	if err := v.apply(fmt.Sprintf(`{"name": "lab", "servers": [%s], "services": [{"name": "echo", "address": %q}], "console": %s}`,
		s.uplinkJSON(""), tunneltest.Echo(t, ""), consoleJSON("127.0.0.1:0"))); err != nil {
		t.Fatal(err)
	}
	pg := login(t, v.n.console.Addr())
	for _, path := range []string{"/api/servers", "/api/servers/update", "/api/servers/delete", "/api/services", "/api/tunnels/close"} {
		pg.expect(400, path, "not json")
	}
	if err := open(s.link(), 7, "echo"); err != nil {
		t.Fatal(err)
	}
	pg.expect(400, "/api/tunnels/close", map[string]string{"server": "hub", "id": "x"})
	pg.expect(404, "/api/tunnels/close", map[string]string{"server": "nope", "id": "7"})
	pg.expect(404, "/api/tunnels/close", map[string]string{"server": "hub", "id": "8"})
	pg.expect(200, "/api/tunnels/close", map[string]string{"server": "hub", "id": "7"})
	waitFor(t, "the tunnel to close", func() bool { return len(v.n.Tunnels()) == 0 })
	pg.expect(400, "/api/servers/delete", map[string]string{"name": "nope"})
	pg.expect(200, "/api/servers/delete", map[string]string{"name": "hub"})
	if len(v.n.Uplinks()) != 0 || strings.Contains(v.current(), "hub") {
		t.Fatalf("the server stayed: %s", v.current())
	}
}
