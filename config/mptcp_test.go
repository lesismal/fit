package config

import "testing"

// MPTCP is a listener option everywhere fit listens over TCP: a data-plane
// listener, the Admin API, the console and the roles' pages, the streaming
// role's RTMP and RTSP listeners, and TURN's TCP and TLS listeners. It is off
// wherever it is not written.
func TestMPTCPDecodes(t *testing.T) {
	const hash = `"$2a$10$abcdefghijklmnopqrstuuK3o5D2wC6Qn7oW7v3yTzR3z0e6Gm9Ka"`
	doc := `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0", "mptcp": true}],
  "admin": {"address": "127.0.0.1:0", "mptcp": true,
    "console": {"address": "127.0.0.1:0", "mptcp": true, "users": [{"username": "a", "password_hash": ` + hash + `}]}},
  "tunnel": {"node": {"console": {"address": "127.0.0.1:0", "mptcp": true, "users": [{"username": "a", "password_hash": ` + hash + `}]}}},
  "streaming": {"rtmp": {"listen": "127.0.0.1:1935", "mptcp": true}, "rtsp": {"listen": "127.0.0.1:8554", "mptcp": true}},
  "webrtc": {"turn": {"listen": [":3478"], "listen_tcp": [":3479"], "realm": "r", "relay_ip": "10.0.0.5", "secret": "s", "mptcp": true}}
}`
	c, err := Load([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Listeners[0].MPTCP {
		t.Fatal("the listener's mptcp did not decode")
	}
	if !c.Admin.MPTCP || c.Admin.Console == nil || !c.Admin.Console.MPTCP {
		t.Fatal("the Admin API's or the console's mptcp did not decode")
	}
	if rc := c.Tunnel.Node.Console; rc == nil || !rc.MPTCP {
		t.Fatal("a role's console mptcp did not decode")
	}
	if !c.Streaming.RTMP.MPTCP || !c.Streaming.RTSP.MPTCP {
		t.Fatal("the streaming listeners' mptcp did not decode")
	}
	if !c.WebRTC.TURN.MPTCP {
		t.Fatal("the TURN server's mptcp did not decode")
	}
}

// A config that does not ask for Multipath TCP gets plain TCP: the zero is
// off, everywhere.
func TestMPTCPDefaultsOff(t *testing.T) {
	const hash = `"$2a$10$abcdefghijklmnopqrstuuK3o5D2wC6Qn7oW7v3yTzR3z0e6Gm9Ka"`
	c, err := Load([]byte(`{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "admin": {"address": "127.0.0.1:0", "console": {"address": "127.0.0.1:0", "users": [{"username": "a", "password_hash": ` + hash + `}]}},
  "tunnel": {"node": {"console": {"address": "127.0.0.1:0", "users": [{"username": "a", "password_hash": ` + hash + `}]}}},
  "streaming": {"rtmp": {"listen": "127.0.0.1:1935"}, "rtsp": {"listen": "127.0.0.1:8554"}},
  "webrtc": {"turn": {"listen": [":3478"], "realm": "r", "relay_ip": "10.0.0.5", "secret": "s"}}
}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listeners[0].MPTCP || c.Admin.MPTCP || c.Admin.Console.MPTCP || c.Tunnel.Node.Console.MPTCP ||
		c.Streaming.RTMP.MPTCP || c.Streaming.RTSP.MPTCP || c.WebRTC.TURN.MPTCP {
		t.Fatal("a config without mptcp turned Multipath TCP on")
	}
}

// A role's console is the admin console with the role's pages: its mptcp is
// carried over by Console.
func TestRoleConsoleMPTCP(t *testing.T) {
	rc := &RoleConsole{Address: "127.0.0.1:0", MPTCP: true}
	if !rc.Console().MPTCP {
		t.Fatal("RoleConsole.Console dropped mptcp")
	}
}
