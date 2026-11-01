package server

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/lesismal/fit/tunnel/internal/proto"
)

// The console's API: what it refuses, and what it changes.
func TestPages(t *testing.T) {
	v := newServer(t, tcpMember(`, "console": `+consoleJSON("127.0.0.1:0")))
	pg := login(t, v.s.console.Addr())

	// Bodies that do not decode.
	for _, path := range []string{"/api/listeners", "/api/listeners/update", "/api/listeners/delete", "/api/settings",
		"/api/peers/refresh", "/api/peers/services", "/api/peers/kick", "/api/tunnels/close"} {
		pg.expect(400, path, "not json")
	}

	// Listeners: a ws path without its slash gets one.
	ws := map[string]string{"protocol": "ws", "address": "127.0.0.1:0", "path": "x"}
	pg.expect(200, "/api/listeners", ws)
	if !strings.Contains(v.current(), `"/x"`) {
		t.Fatalf("member %s", v.current())
	}
	tcp := map[string]string{"protocol": "tcp", "address": "127.0.0.1:0"}
	none := map[string]string{"protocol": "tcp", "address": "127.0.0.1:1"}
	pg.expect(400, "/api/listeners/update", map[string]any{"old": none, "new": tcp})
	pg.expect(400, "/api/listeners/update", map[string]any{"old": ws, "new": tcp})
	pg.expect(200, "/api/listeners/update", map[string]any{"old": ws, "new": map[string]string{"protocol": "ws", "address": "127.0.0.1:0", "path": "/y"}})
	if !strings.Contains(v.current(), `"/y"`) || strings.Contains(v.current(), `"/x"`) {
		t.Fatalf("member %s", v.current())
	}
	pg.expect(400, "/api/listeners/delete", none)
	// A member whose listeners do not decode is not changed.
	cur := v.current()
	v.setMember(`{"listeners": 5}`)
	pg.expect(400, "/api/listeners", none)
	v.setMember(cur)

	// Settings: passwords set, then cleared; the console socket moved.
	sock := freeAddr(t)
	pg.expect(200, "/api/settings", map[string]any{"password": "pw2", "body_password": "b", "socket_address": sock, "socket_password": "sp"})
	if cs := v.s.settings().cfg; cs.Password != "pw2" || cs.BodyPassword != "b" || cs.ConsoleSocket == nil || cs.ConsoleSocket.Password != "sp" {
		t.Fatalf("settings %+v", cs)
	}
	pg.expect(200, "/api/settings", map[string]any{"clear_body_password": true, "socket_address": sock, "clear_socket_password": true})
	if cs := v.s.settings().cfg; cs.BodyPassword != "" || cs.ConsoleSocket == nil || cs.ConsoleSocket.Password != "" {
		t.Fatalf("settings %+v", cs)
	}
	var st Status
	pg.do("GET", "/api/status", nil, &st)
	if !st.Settings.HasPassword || st.Settings.HasBodyPassword || st.Settings.HasSocketPassword || st.Settings.SocketAddress != sock {
		t.Fatalf("settings %+v", st.Settings)
	}
	cur = v.current()
	v.setMember(`{"console_socket": 5}`)
	pg.expect(400, "/api/settings", map[string]any{"socket_address": sock})
	v.setMember(cur)
	pg.expect(200, "/api/settings", map[string]any{"socket_address": ""})
	if v.s.settings().cfg.ConsoleSocket != nil {
		t.Fatal("the console socket stayed")
	}
}

// The console's API on peers and tunnels.
func TestPagesPeers(t *testing.T) {
	v := newServer(t, tcpMember(`, "console": `+consoleJSON("127.0.0.1:0")))
	pg := login(t, v.s.console.Addr())
	svc := proto.Service{Name: "echo", Addr: "127.0.0.1:1"}
	n := v.dial(v.listen()).mustRegister(proto.TypeNode, "lab", svc)
	c := v.dial(v.listen()).mustRegister(proto.TypeClient, "me")
	nodeID, clientID := n.rsp.Peer, c.rsp.Peer

	pg.expect(404, "/api/peers/refresh", map[string]any{"id": 999})
	pg.expect(404, "/api/peers/refresh", map[string]any{"id": clientID})
	if s := pg.expect(200, "/api/peers/refresh", map[string]any{"id": nodeID}); !strings.Contains(s, "echo") {
		t.Fatalf("refresh: %s", s)
	}
	pg.expect(404, "/api/peers/services", map[string]any{"id": clientID})
	if s := pg.expect(200, "/api/peers/services", map[string]any{"id": nodeID, "services": []ServiceView{{Name: " b ", Address: "127.0.0.1:2"}}}); !strings.Contains(s, `"name":"b"`) {
		t.Fatalf("services: %s", s)
	}
	n.mu.Lock()
	n.nodeInfo = func() (any, error) { return nil, errors.New("no") }
	n.services = func([]proto.Service) (any, error) { return nil, errors.New("no") }
	n.mu.Unlock()
	pg.expect(502, "/api/peers/refresh", map[string]any{"id": nodeID})
	pg.expect(502, "/api/peers/services", map[string]any{"id": nodeID, "services": []ServiceView{}})

	// Tunnels close by ID.
	req := openReq(c, 1, "lab", "b")
	if err := c.call(proto.MethodTunnelOpen, req, nil); err != nil {
		t.Fatal(err)
	}
	pg.expect(400, "/api/tunnels/close", map[string]string{"id": "x"})
	pg.expect(404, "/api/tunnels/close", map[string]string{"id": "1"})
	pg.expect(200, "/api/tunnels/close", map[string]string{"id": strconv.FormatUint(req.ID, 10)})
	c.wait(proto.MethodTunnelClose)
	n.wait(proto.MethodTunnelClose)

	// A peer kicked hears why, and goes.
	pg.expect(404, "/api/peers/kick", map[string]any{"id": 999})
	pg.expect(200, "/api/peers/kick", map[string]any{"id": nodeID})
	if why := string(n.wait(proto.MethodKick).body); !strings.Contains(why, "kicked") {
		t.Fatalf("kicked for %q", why)
	}
	n.waitClosed()
	waitFor(t, "the node to go", func() bool { return v.s.peerByID(nodeID) == nil })
	if peers := v.s.Peers(); len(peers) != 1 {
		t.Fatalf("peers %d", len(peers))
	}
}
