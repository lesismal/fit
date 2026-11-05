package config

import (
	"strings"
	"testing"
)

// Multipath TCP is for a listener's TCP connections. A listener that takes
// UDP alone, or a TURN server with no TCP or TLS listener, has none for it
// to apply to, and the config is refused rather than left quietly plain. A
// listener with a TCP side takes it, HTTP/3's UDP side notwithstanding.
func TestValidateMPTCP(t *testing.T) {
	cases := []struct {
		name, doc, want string
	}{
		{
			name: "MPTCP on a udp listener",
			doc: `{"listeners": [{"name": "dns", "address": "127.0.0.1:0", "protocols": ["udp"], "mptcp": true}],
  "upstreams": [{"name": "u", "addresses": ["127.0.0.1:1"]}],
  "services": [{"name": "s", "listeners": ["dns"], "upstream": "u"}]}`,
			want: `listeners[0].mptcp: Multipath TCP is for TCP connections; this listener takes UDP alone`,
		},
		{
			name: "MPTCP on an HTTP/3 listener alone",
			doc: `{"auto_https": {"disable": true}, "listeners": [{"name": "web", "address": "127.0.0.1:0", "protocols": ["h3"], "mptcp": true,
  "tls": {"certificates": [{"cert_file": "/tmp/c.pem", "key_file": "/tmp/k.pem"}]}}]}`,
			want: `listeners[0].mptcp: Multipath TCP is for TCP connections; this listener takes UDP alone`,
		},
		{
			name: "MPTCP on a TURN server without a TCP listener",
			doc:  `{"webrtc": {"turn": {"listen": ["127.0.0.1:3478"], "realm": "r", "relay_ip": "10.0.0.5", "secret": "s", "mptcp": true}}}`,
			want: `webrtc.turn.mptcp: Multipath TCP is for TCP connections; this TURN server has no listen_tcp or listen_tls to apply it to`,
		},
		{
			name: "MPTCP on an HTTP listener",
			doc:  `{"listeners": [{"name": "web", "address": "127.0.0.1:0", "mptcp": true}]}`,
		},
		{
			name: "MPTCP on a tcp listener",
			doc: `{"listeners": [{"name": "raw", "address": "127.0.0.1:0", "protocols": ["tcp"], "mptcp": true}],
  "upstreams": [{"name": "u", "addresses": ["127.0.0.1:1"]}],
  "services": [{"name": "s", "listeners": ["raw"], "upstream": "u"}]}`,
		},
		{
			name: "MPTCP on HTTP with HTTP/3",
			doc: `{"auto_https": {"disable": true}, "listeners": [{"name": "web", "address": "127.0.0.1:0", "protocols": ["http/1.1", "h3"], "mptcp": true,
  "tls": {"certificates": [{"cert_file": "/tmp/c.pem", "key_file": "/tmp/k.pem"}]}}]}`,
		},
		{
			name: "MPTCP on TURN's listen_tcp",
			doc: `{"webrtc": {"turn": {"listen": ["127.0.0.1:3478"], "listen_tcp": ["127.0.0.1:3479"],
  "realm": "r", "relay_ip": "10.0.0.5", "secret": "s", "mptcp": true}}}`,
		},
		{
			name: "MPTCP on TURN's listen_tls",
			doc: `{"webrtc": {"turn": {"listen": ["127.0.0.1:3478"], "listen_tls": ["127.0.0.1:5349"],
  "tls": {"certificates": [{"cert_file": "/tmp/c.pem", "key_file": "/tmp/k.pem"}]},
  "realm": "r", "relay_ip": "10.0.0.5", "secret": "s", "mptcp": true}}}`,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load([]byte(tt.doc))
			if tt.want == "" {
				if err != nil {
					t.Fatalf("a valid config was refused: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load: %v\nwant %s", err, tt.want)
			}
		})
	}
}
