package tunnel_test

import (
	"strings"
	"testing"

	"github.com/lesismal/fit/config"
)

// The tunnel's members load as strictly as the rest of the config, and are
// checked beyond decoding.
func TestConfig(t *testing.T) {
	up := `{"endpoints": [{"address": "hub.example:7000"}], "password": "pw"}`
	for _, tc := range []struct {
		name, doc, want string
	}{
		{"a server", `{"tunnel": {"server": {"password": "pw", "listeners": [{"protocol": "tcp", "address": ":7000"}, {"protocol": "ws", "address": ":7001"}, {"protocol": "quic", "address": ":7000"}]}}}`, ""},
		{"a node", `{"tunnel": {"node": {"servers": [` + up + `], "services": [{"name": "ssh", "address": "10.0.0.5:22"}]}}}`, ""},
		{"a client", `{"tunnel": {"client": {"servers": [` + up + `], "forwards": [{"listen": "127.0.0.1:2222", "node": "lab", "service": "ssh", "server": "hub.example:7000"}]}}}`, ""},
		{"camelCase", `{"tunnel": {"server": {"linkWsAddr": ":7001"}}}`, "unknown field"},
		{"an unknown protocol", `{"tunnel": {"server": {"listeners": [{"protocol": "udp", "address": ":7000"}]}}}`, "listeners[0].protocol"},
		{"two listeners on one address", `{"tunnel": {"server": {"listeners": [{"protocol": "tcp", "address": ":7000"}, {"protocol": "ws", "address": ":7000"}]}}}`, "listens on :7000 already"},
		{"a path on tcp", `{"tunnel": {"server": {"listeners": [{"protocol": "tcp", "address": ":7000", "path": "/x"}]}}}`, "only a ws listener"},
		{"a socket without a console", `{"tunnel": {"server": {"password": "pw", "console_socket": {"address": ":7002"}}}}`, "lacks"},
		{"half a certificate", `{"tunnel": {"server": {"quic": {"cert": "x"}}}}`, "cert and key go together"},
		{"a bad certificate", `{"tunnel": {"server": {"quic": {"cert": "x", "key": "y"}}}}`, "do not load"},
		{"two services of a name", `{"tunnel": {"node": {"services": [{"name": "a", "address": "h:1"}, {"name": "a", "address": "h:2"}]}}}`, "already named"},
		{"a service without a port", `{"tunnel": {"node": {"services": [{"name": "a", "address": "nowhere"}]}}}`, "services[0].address"},
		{"a server without a password", `{"tunnel": {"node": {"servers": [{"endpoints": [{"address": "h:1"}]}]}}}`, "missing required field"},
		{"a bad endpoint", `{"tunnel": {"node": {"servers": [{"endpoints": [{"address": "http://h:1"}], "password": "x"}]}}}`, "the scheme http"},
		{"every endpoint off", `{"tunnel": {"node": {"servers": [{"endpoints": [{"address": "h:1", "disabled": true}], "password": "x"}]}}}`, "every endpoint is disabled"},
		{"two servers of a name", `{"tunnel": {"node": {"servers": [` + up + `, ` + up + `]}}}`, "already named"},
		{"a pool too large", `{"tunnel": {"node": {"servers": [{"endpoints": [{"address": "h:1"}], "password": "x", "pool_size": 101}]}}}`, "pool_size"},
		{"a forward to no server", `{"tunnel": {"client": {"forwards": [{"listen": "127.0.0.1:1", "node": "n", "service": "s", "server": "nope"}]}}}`, "no server named"},
		{"two forwards on one address", `{"tunnel": {"client": {"forwards": [{"listen": "127.0.0.1:1", "node": "n", "service": "s"}, {"listen": "127.0.0.1:1", "node": "n", "service": "t"}]}}}`, "listens on 127.0.0.1:1 already"},
		{"a web proxy to http", `{"tunnel": {"client": {"web_proxies": [{"listen": "127.0.0.1:1", "url": "http://h/", "password": "p"}]}}}`, "not a ws:// URL"},
		{"a bad duration", `{"tunnel": {"client": {"open_timeout": "soon"}}}`, "not a duration"},
		{"a console off the loopback without TLS", `{"tunnel": {"client": {"console": {"address": "0.0.0.0:7280", "users": [{"username": "a", "password_hash": "$2a$10$x"}]}}}}`, "not a loopback address"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.Load([]byte(tc.doc))
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}
