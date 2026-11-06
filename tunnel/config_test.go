package tunnel_test

import (
	"strings"
	"testing"

	"github.com/lesismal/fit/config"
)

// The tunnel's members load as strictly as the rest of the config, and are
// checked beyond decoding.
func TestConfig(t *testing.T) {
	pin := strings.Repeat("ab", 32)
	up := `{"endpoints": [{"address": "wss://hub.example/tunnel"}, {"address": "tls://hub.example:7000"}, {"address": "quic://hub.example:7443"}], "password": "pw", "pin": "` + pin + `"}`
	bare := func(address, extra string) string {
		return `{"tunnel": {"node": {"hubs": [{"endpoints": [{"address": "` + address + `"}], "password": "pw"` + extra + `}]}}}`
	}
	listener := func(protocol, address, extra string) string {
		return `{"tunnel": {"hub": {"password": "pw"` + extra + `, "listeners": [{"protocol": "` + protocol + `", "address": "` + address + `"}]}}}`
	}
	for _, tc := range []struct {
		name, doc, want string
	}{
		{"a hub", `{"tunnel": {"hub": {"password": "pw", "listeners": [{"protocol": "tls", "address": ":7000"}, {"protocol": "wss", "address": ":7001", "path": "/t"}, {"protocol": "quic", "address": ":7000"}]}}}`, ""},
		{"a hub in the clear with body_password", `{"tunnel": {"hub": {"password": "pw", "body_password": "b", "listeners": [{"protocol": "tcp", "address": ":7000"}, {"protocol": "ws", "address": ":7001"}]}}}`, ""},
		{"a listener's own certificate", `{"tunnel": {"hub": {"listeners": [{"protocol": "wss", "address": ":7001", "cert_file": "c.pem", "key_file": "k.pem"}]}}}`, ""},

		// Off the loopback, tcp and ws carry the links unauthenticated
		// after registration; on it, and with body_password, they may.
		{"tcp on every address", listener("tcp", ":7000", ""), "a tcp listener on \":7000\", which is not a loopback address"},
		{"ws on a public address", listener("ws", "0.0.0.0:7001", ""), "use tls or wss"},
		{"tcp on IPv6's every address", listener("tcp", "[::]:7000", ""), "or set the hub's body_password"},
		{"tcp on the loopback", listener("tcp", "127.0.0.1:7000", ""), ""},
		{"ws on the loopback", listener("ws", "127.8.0.1:7000", ""), ""},
		{"tcp on IPv6's loopback", listener("tcp", "[::1]:7000", ""), ""},
		{"tcp on localhost", listener("tcp", "localhost:7000", ""), ""},
		{"tcp with body_password", listener("tcp", ":7000", `, "body_password": "b"`), ""},
		{"a tcp endpoint", bare("hub.example:7000", ""), "carries the links in the clear and unauthenticated: use tls:// or wss:// with pin"},
		{"a tcp:// endpoint", bare("tcp://10.0.0.1:7000", ""), "use tls:// or wss://"},
		{"a ws endpoint", bare("ws://hub.example/tunnel", ""), "or set body_password"},
		{"a disabled bare endpoint", `{"tunnel": {"node": {"hubs": [{"endpoints": [{"address": "h:1", "disabled": true}, {"address": "tls://h:2"}], "password": "pw"}]}}}`, "\"h:1\", whose host is not a loopback one"},
		{"a tcp endpoint on the loopback", bare("127.0.0.1:7000", ""), ""},
		{"a ws endpoint on localhost", bare("ws://localhost:7001/tunnel", ""), ""},
		{"a tcp endpoint on IPv6's loopback", bare("tcp://[::1]:7000", ""), ""},
		{"a tcp endpoint with body_password", bare("hub.example:7000", `, "body_password": "b"`), ""},
		{"a tls endpoint", bare("tls://hub.example:7000", ""), ""},
		{"a tls endpoint with a path", bare("tls://hub.example:7000/x", ""), "has a path"},
		{"a tls endpoint without a port", bare("tls://hub.example", ""), "not a tls:// URL"},
		{"a pin in the address", bare("wss://hub.example/t?pin=ab", ""), "has a query"},

		// The review's attacks: a fragment that would have overridden the
		// pin, turned quic's check off, swapped the CAs, or dropped the
		// pin; and a user. The hub's real pin is configured each time.
		{"a pin in a tls fragment", bare("tls://hub.example:7000#?pin="+strings.Repeat("cd", 32)+"&", `, "pin": "`+pin+`"`), "has a query, a fragment or a user"},
		{"insecure in a quic fragment", bare("quic://hub.example:7000#?insecure=1&", `, "pin": "`+pin+`"`), "a fragment"},
		{"a CA file in a wss fragment", bare("wss://hub.example/tunnel#?ca=/tmp/evil.pem&", `, "pin": "`+pin+`"`), "a fragment"},
		{"an empty query in a fragment", bare("tls://hub.example:443#?x=", `, "pin": "`+pin+`"`), "a fragment"},
		{"a bare fragment", bare("tls://hub.example:443#", `, "pin": "`+pin+`"`), "a fragment"},
		{"an empty query", bare("tls://hub.example:443?", `, "pin": "`+pin+`"`), "a query"},
		{"a user", bare("tls://u:p@hub.example:443", `, "pin": "`+pin+`"`), "a user"},
		{"a fragment on tcp", bare("127.0.0.1:7000#x", ""), "a fragment"},
		{"a tcp port that is not one", bare("127.0.0.1:x", ""), "not a host and port"},
		{"a tcp endpoint with a path", bare("tcp://127.0.0.1:7000/x", ""), "not a host and port"},

		// quic_insecure off the loopback is refused as tcp is, unless
		// body_password, pin or ca_file.
		{"quic_insecure off the loopback", bare("quic://hub.example:7443", `, "quic_insecure": true`), "quic_insecure takes any certificate"},
		{"quic_insecure on the loopback", bare("quic://127.0.0.1:7443", `, "quic_insecure": true`), ""},
		{"quic_insecure on localhost", bare("quic://localhost:7443", `, "quic_insecure": true`), ""},
		{"quic_insecure with body_password", bare("quic://hub.example:7443", `, "quic_insecure": true, "body_password": "b"`), ""},
		{"quic_insecure under a pin", bare("quic://hub.example:7443", `, "quic_insecure": true, "pin": "`+pin+`"`), ""},
		{"quic_insecure beside tls", bare("tls://hub.example:7443", `, "quic_insecure": true`), ""},
		{"quic_insecure with a bad quic endpoint", bare("quic://hub.example", `, "quic_insecure": true`), "not a quic:// URL"},
		{"quic_insecure past a loopback endpoint", `{"tunnel": {"node": {"hubs": [{"endpoints": [{"address": "quic://127.0.0.1:1"}, {"address": "quic://[::1]:2"}, {"address": "quic://hub.example:3", "disabled": true}], "password": "pw", "quic_insecure": true}]}}}`, "\"quic://hub.example:3\", whose host is not a loopback one"},
		{"a bad pin", bare("tls://hub.example:7000", `, "pin": "abcd"`), "not a certificate pin"},
		{"a pin with colons", bare("tls://hub.example:7000", `, "pin": "`+strings.TrimSuffix(strings.Repeat("AB:", 32), ":")+`"`), ""},
		{"pin and ca_file", bare("tls://hub.example:7000", `, "pin": "`+pin+`", "ca_file": "ca.pem"`), "set one"},
		{"quic_pin, pin's old name", bare("quic://hub.example:7443", `, "quic_pin": "`+pin+`"`), ""},
		{"quic_pin and pin", bare("quic://hub.example:7443", `, "quic_pin": "`+pin+`", "pin": "`+pin+`"`), "old name"},
		{"a certificate on tcp", `{"tunnel": {"hub": {"listeners": [{"protocol": "tcp", "address": "127.0.0.1:7000", "cert_file": "c", "key_file": "k"}]}}}`, "only a tls, wss or quic listener"},
		{"half a listener's certificate", `{"tunnel": {"hub": {"listeners": [{"protocol": "tls", "address": ":7000", "cert_file": "c"}]}}}`, "cert_file and key_file go together"},
		{"a node", `{"tunnel": {"node": {"hubs": [` + up + `], "services": [{"name": "ssh", "address": "10.0.0.5:22"}]}}}`, ""},
		{"a proxy", `{"tunnel": {"proxy": {"hubs": [` + up + `], "forwards": [{"listen": "127.0.0.1:2222", "node": "lab", "service": "ssh", "hub": "wss://hub.example/tunnel"}]}}}`, ""},
		{"camelCase", `{"tunnel": {"hub": {"linkWsAddr": ":7001"}}}`, "unknown field"},
		{"an unknown protocol", `{"tunnel": {"hub": {"listeners": [{"protocol": "udp", "address": ":7000"}]}}}`, "listeners[0].protocol"},
		{"two listeners on one address", `{"tunnel": {"hub": {"listeners": [{"protocol": "tcp", "address": ":7000"}, {"protocol": "ws", "address": ":7000"}]}}}`, "tunnel.hub.listeners[0] listens on TCP :7000 already"},
		{"a path on tcp", `{"tunnel": {"hub": {"listeners": [{"protocol": "tcp", "address": ":7000", "path": "/x"}]}}}`, "only a ws or wss listener"},
		{"a socket without a console", `{"tunnel": {"hub": {"password": "pw", "console_socket": {"address": ":7002"}}}}`, "lacks"},
		{"half a certificate", `{"tunnel": {"hub": {"quic": {"cert": "x"}}}}`, "cert and key go together"},
		{"a bad certificate", `{"tunnel": {"hub": {"quic": {"cert": "x", "key": "y"}}}}`, "do not load"},
		{"two services of a name", `{"tunnel": {"node": {"services": [{"name": "a", "address": "h:1"}, {"name": "a", "address": "h:2"}]}}}`, "already named"},
		{"a service without a port", `{"tunnel": {"node": {"services": [{"name": "a", "address": "nowhere"}]}}}`, "services[0].address"},
		{"a hub without a token or password", `{"tunnel": {"node": {"hubs": [{"endpoints": [{"address": "h:1"}]}]}}}`, "set the token the hub issued"},
		{"a bad endpoint", `{"tunnel": {"node": {"hubs": [{"endpoints": [{"address": "http://h:1"}], "password": "x"}]}}}`, "the scheme http"},
		{"every endpoint off", `{"tunnel": {"node": {"hubs": [{"endpoints": [{"address": "h:1", "disabled": true}], "password": "x"}]}}}`, "every endpoint is disabled"},
		{"two hubs of a name", `{"tunnel": {"node": {"hubs": [` + up + `, ` + up + `]}}}`, "already named"},
		{"a pool too large", `{"tunnel": {"node": {"hubs": [{"endpoints": [{"address": "h:1"}], "password": "x", "pool_size": 101}]}}}`, "pool_size"},
		{"a forward to no hub", `{"tunnel": {"proxy": {"forwards": [{"listen": "127.0.0.1:1", "node": "n", "service": "s", "hub": "nope"}]}}}`, "no hub named"},
		{"two forwards on one address", `{"tunnel": {"proxy": {"forwards": [{"listen": "127.0.0.1:1", "node": "n", "service": "s"}, {"listen": "127.0.0.1:1", "node": "n", "service": "t"}]}}}`, "tunnel.proxy.forwards[0] listens on TCP 127.0.0.1:1 already"},
		{"a web proxy to http", `{"tunnel": {"proxy": {"web_proxies": [{"listen": "127.0.0.1:1", "url": "http://h/", "password": "p"}]}}}`, "not a ws:// URL"},
		{"a bad duration", `{"tunnel": {"proxy": {"open_timeout": "soon"}}}`, "not a duration"},
		{"a console off the loopback without TLS", `{"tunnel": {"proxy": {"console": {"address": "0.0.0.0:7280", "users": [{"username": "a", "password_hash": "$2a$10$x"}]}}}}`, "not a loopback address"},
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
