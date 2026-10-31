package config

import (
	"strings"
	"testing"
	"time"
)

func TestWebRTCMembers(t *testing.T) {
	good := `{"webrtc": {"turn": {"listen": [":3478", "[::1]:3478"], "realm": "example.com",
		"users": [{"name": "alice", "password": "pw"}, {"name": "bob", "key": "0x0123456789abcdef0123456789ABCDEF"}],
		"secret": "s", "relay_ip": "10.0.0.5", "public_ip": "203.0.113.5", "port_min": 49152, "port_max": 49999,
		"allowed_peers": ["10.1.0.0/16", "10.0.0.5"], "denied_peers": ["198.51.100.0/24"], "default_lifetime": "5m"}}}`
	c, err := Load([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if !c.HasApps() {
		t.Fatal("a TURN server is not a role")
	}
	tr := c.WebRTC.TURN
	if tr.MaxAllocs() != DefaultTURNMaxAllocations || tr.MaxAllocsPerUser() != DefaultTURNMaxAllocationsPerUser ||
		tr.Bandwidth() != DefaultTURNUserBandwidth || len(tr.Users[1].KeyBytes()) != 16 {
		t.Fatalf("%+v", tr)
	}
	if p, ok := ParsePeerPrefix("10.0.0.5"); !ok || p.Bits() != 32 {
		t.Fatalf("an address as a prefix: %v %v", p, ok)
	}
	turn := func(fields string) string {
		return `{"webrtc": {"turn": {"listen": [":3478"], "realm": "r", "relay_ip": "10.0.0.5", ` + fields + `}}}`
	}
	for _, bad := range []struct{ doc, want string }{
		{turn(`"users": []`), "no one could log in"},
		{turn(`"secret": "s", "realm": " "`), "duplicate"},
		{`{"webrtc": {"turn": {"listen": [], "realm": "r", "relay_ip": "10.0.0.5", "secret": "s"}}}`, "at least one address"},
		{`{"webrtc": {"turn": {"listen": [":3478", ":3478"], "realm": "r", "relay_ip": "10.0.0.5", "secret": "s"}}}`, "listed already"},
		{`{"webrtc": {"turn": {"listen": ["nope"], "realm": "r", "relay_ip": "10.0.0.5", "secret": "s"}}}`, "not a host and port"},
		{`{"webrtc": {"turn": {"listen": [":3478"], "realm": " ", "relay_ip": "10.0.0.5", "secret": "s"}}}`, "realm is empty"},
		{turn(`"users": [{"name": "a", "password": "p"}, {"name": "a", "password": "q"}]`), "already named"},
		{turn(`"users": [{"name": "a"}]`), "set password or key"},
		{turn(`"users": [{"name": "a", "password": "p", "key": "00"}]`), "set password or key"},
		{turn(`"users": [{"name": "a", "key": "xyz"}]`), "32 hex digits"},
		{`{"webrtc": {"turn": {"listen": [":3478"], "realm": "r", "relay_ip": "0.0.0.0", "secret": "s"}}}`, "wildcard"},
		{`{"webrtc": {"turn": {"listen": [":3478"], "realm": "r", "relay_ip": "host", "secret": "s"}}}`, "not an IP address"},
		{turn(`"secret": "s", "public_ip": "2001:db8::1"`), "relay_ip's family"},
		{turn(`"secret": "s", "port_min": 5000`), "together"},
		{turn(`"secret": "s", "port_min": 6000, "port_max": 5000`), "above port_max"},
		{turn(`"secret": "s", "max_lifetime": "1m"`), "below default_lifetime"},
		{turn(`"secret": "s", "allowed_peers": ["10.0.0.0/33"]`), "not an address or a prefix"},
		{turn(`"secret": "s", "denied_peers": ["x"]`), "not an address or a prefix"},
	} {
		_, err := Load([]byte(bad.doc))
		if err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%s: %v, want %q", bad.doc, err, bad.want)
		}
	}
}

func TestWebRTCMedia(t *testing.T) {
	good := `{"webrtc": {"turn": {"listen": [":3478"], "realm": "r", "relay_ip": "10.0.0.5", "secret": "s"},
		"media": {"listen": ":8189", "public_ips": ["203.0.113.5"], "codecs": ["opus", "vp8", "H264"],
		"ice_servers": [{"urls": ["stun:stun.example.com:3478"]}, {"urls": ["turn:t.example.com?transport=udp"], "username": "u", "credential": "p"}],
		"turn_urls": ["turn:turn.example.com:3478"], "connect_timeout": "10s"}}}`
	c, err := Load([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	m := c.WebRTC.Media
	if !c.HasApps() || m.MaxPCs() != DefaultMediaMaxPeerConnections || m.MaxStreamCount() != DefaultMediaMaxStreams ||
		m.MaxViewerCount() != DefaultMediaMaxViewers || m.Bitrate() != DefaultMediaMaxBitrate || m.SDPBytes() != DefaultMediaMaxSDPBytes ||
		m.Connect() != 10*time.Second || m.ICE() != DefaultMediaICETimeout || m.KeyFrames() != DefaultMediaKeyFrameInterval ||
		m.CredentialTTL() != DefaultMediaTURNCredentialTTL {
		t.Fatalf("%+v", m)
	}
	if _, err := Load([]byte(`{"webrtc": {"media": {"listen": "127.0.0.1:0"}}}`)); err != nil {
		t.Fatalf("media alone: %v", err)
	}
	media := func(fields string) string { return `{"webrtc": {"media": {"listen": ":8189", ` + fields + `}}}` }
	for _, bad := range []struct{ doc, want string }{
		{`{"webrtc": {"media": {"listen": "nope"}}}`, "not a host and port"},
		{media(`"public_ips": ["x"]`), "not an IP address"},
		{media(`"public_ips": ["::"]`), "wildcard"},
		{media(`"ice_servers": [{"urls": []}]`), "at least one URL"},
		{media(`"ice_servers": [{"urls": ["http://x"]}]`), "not a stun:"},
		{media(`"ice_servers": [{"urls": ["stun:x"], "username": "a\"b"}]`), "double quote"},
		{media(`"turn_urls": ["stun:x"]`), "not a turn: or turns: URL"},
		{media(`"turn_urls": ["turn:x"]`), "webrtc.turn's secret"},
		{media(`"codecs": ["h265"]`), "not one of"},
		{media(`"max_viewers": -1`), "less than the minimum"},
	} {
		_, err := Load([]byte(bad.doc))
		if err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%s: %v, want %q", bad.doc, err, bad.want)
		}
	}
}

func TestWebRTCProxy(t *testing.T) {
	c, err := Load([]byte(`{"webrtc": {"proxy": {"listen": ":8190", "public_ips": ["203.0.113.5"], "idle_timeout": "10s"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	p := c.WebRTC.Proxy
	if !c.HasApps() || p.Sessions() != DefaultProxyMaxSessions || p.Relays() != DefaultProxyMaxRelays ||
		p.SessionIdle() != DefaultProxySessionTimeout || p.Idle() != 10*time.Second {
		t.Fatalf("%+v", p)
	}
	for _, bad := range []struct{ doc, want string }{
		{`{"webrtc": {"proxy": {"listen": "nope"}}}`, "not a host and port"},
		{`{"webrtc": {"proxy": {"listen": ":8190", "public_ips": ["0.0.0.0"]}}}`, "wildcard"},
		{`{"webrtc": {"media": {"listen": ":8189"}, "proxy": {"listen": ":8189"}}}`, "webrtc.media listens on :8189"},
		{`{"webrtc": {"proxy": {"listen": ":8190", "max_relays": -1}}}`, "less than the minimum"},
	} {
		_, err := Load([]byte(bad.doc))
		if err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%s: %v, want %q", bad.doc, err, bad.want)
		}
	}
}
