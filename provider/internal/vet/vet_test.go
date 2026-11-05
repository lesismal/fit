package vet

import (
	"strings"
	"testing"

	"github.com/lesismal/fit/module"

	_ "github.com/lesismal/fit/handler/proxy"
	_ "github.com/lesismal/fit/handler/respond"
	_ "github.com/lesismal/fit/middleware/headers"
	_ "github.com/lesismal/fit/middleware/rewrite"
)

func TestLocal(t *testing.T) {
	for _, h := range []string{"", "localhost", "LOCALHOST.", "a.localhost", "127.0.0.1", "127.9.9.9", "[::1]", "::1",
		"::ffff:127.0.0.1", "0.0.0.0", "0.1.2.3", "::", "169.254.169.254", "fe80::1", "fe80::1%eth0", "127.1",
		"2130706433", "0x7f000001", "0177.0.0.1", "0x7f.1"} {
		if !Local(h) {
			t.Errorf("Local(%q) = false", h)
		}
	}
	for _, h := range []string{"10.0.0.1", "192.168.1.1", "2001:db8::1", "db.example.net", "example.com.", "web", "x0x1.example", "deadbeef"} {
		if Local(h) {
			t.Errorf("Local(%q) = true", h)
		}
	}
}

func TestRoute(t *testing.T) {
	c := NewChecker("own")
	for _, ok := range []string{
		`{"name": "a", "listeners": ["missing"], "match": {"host": ["**.example.com"], "path": ["/a/*"]}, "handler": {"module": "proxy", "params": {"upstream": "own"}}}`,
		`{"middleware": [{"module": "headers", "params": {"request": {"set": {"X-A": "b"}}}}], "handler": {"module": "respond", "params": {"status": 500}}}`,
		`{"handler": {"module": "not_linked_here"}}`,
	} {
		if err := c.Route([]byte(ok)); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for route, want := range map[string]string{
		`{"match": {"host": ["bad host"]}, "handler": {"module": "respond"}}`:                                                                  "match.host[0]",
		`{"match": {"path": ["/a*b"]}, "handler": {"module": "respond"}}`:                                                                      "has a '*' other than at its end",
		`{"match": {"header": {"bad name": []}}, "handler": {"module": "respond"}}`:                                                            "not a valid header name",
		`{"middleware": [{"module": "headers", "params": {"request": {"set": {"X-A": "b\nInjected: 1"}}}}], "handler": {"module": "respond"}}`: "middleware 0: params.request: the value of X-A holds a line break",
		`{"middleware": [{"module": "rewrite", "params": {"add_prefix": "nope"}}], "handler": {"module": "respond"}}`:                          "add_prefix",
		`{"handler": {"module": "proxy", "params": {"upstream": "admin"}}}`:                                                                    `there is no upstream named "admin"`,
		`{"handler": {"module": "proxy", "params": {"upstream": "own", "host_header": "a\r\nb"}}}`:                                             "host_header",
		`{"handler": {"module": "proxy", "params": {"upstream": "own", "upstream": "own"}}}`:                                                   "duplicate key",
		`{"handler": {"module": "proxy", "params": {"Upstream": "own"}}}`:                                                                      "Upstream",
	} {
		err := c.Route([]byte(route))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", route, err, want)
		}
	}
	if err := c.Module(module.Middlewares, []byte(`{"module": "headers", "params": {"response": {"add": {"A": ["\u0000"]}}}}`)); err == nil {
		t.Error("a NUL in a header value is taken")
	}
}

func TestUpstreamAndServerNames(t *testing.T) {
	if err := Upstream([]byte(`{"name": "u", "addresses": ["10.0.0.1:80"], "load_balancing": "p2c"}`)); err != nil {
		t.Error(err)
	}
	if err := Upstream([]byte(`{"name": "u", "addresses": ["10.0.0.1:80"], "load_balancing": "bogus"}`)); err == nil {
		t.Error("load_balancing bogus is taken")
	}
	if err := ServerNames([]string{"a.example.com", "*.b.example.com"}); err != nil {
		t.Error(err)
	}
	if err := ServerNames([]string{"10.0.0.1"}); err == nil {
		t.Error("an IP address is taken as a server name")
	}
}
