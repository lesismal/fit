package fitconf

import (
	"strings"
	"testing"

	"github.com/lesismal/fit/config"
)

// TestShorthands checks the shorter ways to write a config: defs and use,
// tls, a listener's and an upstream's arguments, a site of any host and
// numbers of bytes in units, each the same config as its long form.
func TestShorthands(t *testing.T) {
	for _, c := range []struct{ name, fit, json string }{
		{"def and use", `
def pool { max_conns 8192; connect_timeout 5s }
upstream a { addresses 10.0.0.1:80; use pool }
upstream b { addresses 10.0.0.2:80; use pool }
`, `{"upstreams": [
			{"name": "a", "addresses": ["10.0.0.1:80"], "max_conns": 8192, "connect_timeout": "5s"},
			{"name": "b", "addresses": ["10.0.0.2:80"], "max_conns": 8192, "connect_timeout": "5s"}]}`},
		{"defs using defs, at the top and in blocks", `
def hdr { middleware headers { response { delete Server } } }
def routes {
	route a { match { path /a }; use hdr; handler respond }
}
use routes
`, `{"routes": [{"name": "a", "match": {"path": ["/a"]},
			"middleware": [{"module": "headers", "params": {"response": {"delete": ["Server"]}}}],
			"handler": {"module": "respond"}}]}`},
		{"tls", `listener a { address :443; tls cert.pem key.pem }`,
			`{"listeners": [{"name": "a", "address": ":443", "tls": {"certificates": [{"cert_file": "cert.pem", "key_file": "key.pem"}]}}]}`},
		{"tls with a block", `listener a { address :443; tls a.pem a.key { certificates { cert_file b.pem; key_file b.key } } }`,
			`{"listeners": [{"name": "a", "address": ":443", "tls": {"certificates": [
				{"cert_file": "a.pem", "key_file": "a.key"}, {"cert_file": "b.pem", "key_file": "b.key"}]}}]}`},
		{"listener", "listener a :8080\nlistener b :8443 h2 h3 { tls c.pem k.pem }\n",
			`{"listeners": [{"name": "a", "address": ":8080"},
				{"name": "b", "address": ":8443", "protocols": ["h2", "h3"], "tls": {"certificates": [{"cert_file": "c.pem", "key_file": "k.pem"}]}}]}`},
		{"upstream", "upstream a 10.0.0.1:80 10.0.0.2:80 { max_conns 8 }\n",
			`{"upstreams": [{"name": "a", "addresses": ["10.0.0.1:80", "10.0.0.2:80"], "max_conns": 8}]}`},
		{"site *", "site * {\n\tlisteners web\n\troute /a /b { handler respond }\n\troute { handler respond { status 404 } }\n}\n",
			`{"routes": [
				{"match": {"path": ["/a", "/b"]}, "listeners": ["web"], "handler": {"module": "respond"}},
				{"listeners": ["web"], "handler": {"module": "respond", "params": {"status": 404}}}]}`},
		{"bytes", `
limits { max_header_bytes 16KiB; max_pending_bytes 1gib }
route a {
	middleware cache { max_bytes 1GB; max_entry_bytes 2MiB }
	handler respond
}
`, `{"limits": {"max_header_bytes": 16384, "max_pending_bytes": 1073741824},
			"routes": [{"name": "a", "middleware": [{"module": "cache", "params": {"max_bytes": 1000000000, "max_entry_bytes": 2097152}}],
				"handler": {"module": "respond"}}]}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, err := Adapt([]byte(c.fit))
			if err != nil {
				t.Fatal(err)
			}
			if !same(t, r.JSON, []byte(c.json)) {
				t.Errorf("got\n%s\nwant\n%s", r.JSON, c.json)
			}
		})
	}
}

func TestShorthandErrors(t *testing.T) {
	for _, c := range []struct{ fit, want string }{
		{"upstream a { use pool }\ndef pool { max_conns 1 }\n", "1:18: use pool: there is no def pool defined before it"},
		{"def a { }\ndef a { }\n", "2:5: def a is defined twice"},
		{"route r { def a { } }\n", "1:11: def is given at the top of a file only"},
		{"def a\n", "1:1: def takes its name and a block"},
		{"def a/b { }\n", `1:5: "a/b" cannot name a def`},
		{"def a { use a }\nuse a\n", "def a uses itself"},
		{"def a { use b }\ndef b { use a }\nuse a\n", "uses itself"},
		{"use\n", "1:1: use takes the defs to read"},
		{"def a { }\nuse a { }\n", "2:1: use takes the defs to read"},
		{"listener a { address :1; tls c.pem }\n", "1:30: tls takes a certificate's file and its key's"},
		{"listener a :1 { address :2 }\n", "1:12: address is given twice, as an argument and in the block"},
		{"listener a :1 h2 { protocols h3 }\n", "1:15: protocols is given twice"},
		{"route a /x { handler respond }\n", "1:1: route takes one argument, its name, and a block"},
		{"site * a.example { route { handler respond } }\n", "1:6: * is a site of any host, given alone"},
	} {
		_, err := Adapt([]byte(c.fit))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: %v, want %q", c.fit, err, c.want)
		}
	}
	// A number of bytes in no unit fit knows, or quoted, is left for the
	// config's check to report.
	for _, s := range []string{"16XB", `"16KiB"`, "99999999999TiB", "-1KiB", "KiB"} {
		r, err := Adapt([]byte("limits { max_header_bytes " + s + " }\n"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := config.Load(r.JSON); err == nil {
			t.Errorf("%s: loads", s)
		}
	}
}

// TestCompactWriter checks that FromJSON writes the shorthands, and blocks
// that fit on a line on one, and that what it writes reads back the same.
func TestCompactWriter(t *testing.T) {
	src := `{"listeners": [{"name": "web", "address": ":8080", "protocols": ["http/1.1", "h2c"]},
		{"name": "tls", "address": ":8443", "tls": {"certificates": [{"cert_file": "c.pem", "key_file": "k.pem"}], "acme": ["a.example"]}}],
	"upstreams": [{"name": "app", "addresses": ["10.0.0.1:80", "10.0.0.2:80"]},
		{"name": "big", "addresses": ["10.0.0.3:80"], "max_conns": 8192, "max_pending": 8192, "max_idle_conns": 8192, "connect_timeout": "5s", "max_fails": 0}],
	"routes": [{"name": "a", "match": {"path": ["/a"]},
		"middleware": [{"module": "cache", "params": {"max_bytes": 1073741824, "max_entry_bytes": 1000}}],
		"handler": {"module": "proxy", "params": {"upstream": "app"}}},
		{"name": "b", "handler": {"module": "respond", "params": {"body": "a body far too long to be written in a block on one line, past eighty"}}}]}`
	out, err := FromJSON([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	want := `listener web :8080 http/1.1 h2c
listener tls :8443 { tls c.pem k.pem { acme a.example } }

upstream app 10.0.0.1:80 10.0.0.2:80

upstream big 10.0.0.3:80 {
	max_conns 8192
	max_pending 8192
	max_idle_conns 8192
	connect_timeout 5s
	max_fails 0
}

route a {
	match { path /a }
	middleware cache { max_bytes 1GiB; max_entry_bytes 1000 }
	handler proxy { upstream app }
}

route b {
	handler respond {
		body "a body far too long to be written in a block on one line, past eighty"
	}
}
`
	if string(out) != want {
		t.Errorf("got\n%s\nwant\n%s", out, want)
	}
	r, err := Adapt(out)
	if err != nil {
		t.Fatal(err)
	}
	if !same(t, r.JSON, []byte(src)) {
		t.Errorf("reads back\n%s", r.JSON)
	}
}
