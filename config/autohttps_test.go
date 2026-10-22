package config

import (
	"slices"
	"strings"
	"testing"
)

func TestAutoHTTPSName(t *testing.T) {
	for name, want := range map[string]bool{
		"a.example": true, "WWW.Example.COM.": true, "x.fit.test": true,
		"localhost": false, "a.localhost": false, "printer.local": false, "db.internal": false,
		"nas.home.arpa": false, "x.invalid": false, "intranet": false, "*.a.example": false, "**.a.example": false,
		"10.0.0.1": false, "[::1]": false,
	} {
		if got := AutoHTTPSName(name); got != want {
			t.Errorf("%q: %v", name, got)
		}
	}
}

func TestEffective(t *testing.T) {
	load := func(doc string) *Config {
		t.Helper()
		c, err := Load([]byte(doc))
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	routes := `"routes": [
    {"match": {"host": ["A.example", "localhost", "*.w.example"]}, "handler": {"module": "respond"}},
    {"listeners": ["other"], "match": {"host": ["b.example"]}, "handler": {"module": "respond"}},
    {"match": {"host": ["a.example", "c.example"]}, "handler": {"module": "respond"}}
  ]`

	// Written out: as written, but for an empty tls.
	c := load(`{"listeners": [
    {"name": "web", "address": ":8443", "tls": {}},
    {"name": "other", "address": ":9443", "tls": {}},
    {"name": "files", "address": ":10443", "tls": {"acme": ["z.example"]}},
    {"name": "plain", "address": ":8080"}
  ], ` + routes + `}`)
	ls, redirect := c.Effective()
	if len(ls) != 4 || redirect != nil {
		t.Fatalf("%d listeners, redirects %v", len(ls), redirect)
	}
	if got := ls[0].TLS.ACME; !slices.Equal(got, []string{"a.example", "c.example"}) {
		t.Errorf("web: %v", got)
	}
	if got := ls[1].TLS.ACME; !slices.Equal(got, []string{"a.example", "b.example", "c.example"}) {
		t.Errorf("other: %v", got)
	}
	if got := ls[2].TLS.ACME; !slices.Equal(got, []string{"z.example"}) || ls[3].TLS != nil {
		t.Errorf("files: %v, plain: %v", got, ls[3].TLS)
	}
	if len(c.Listeners[0].TLS.ACME) != 0 {
		t.Error("the config was changed")
	}

	// None written: https and http, which routes may name.
	routes = strings.Replace(routes, `"listeners": ["other"]`, `"listeners": ["https"]`, 1)
	c = load(`{"acme": {"directory": "https://ca.example/dir"}, ` + routes[:len(routes)-len(`]`)] +
		`, {"listeners": ["https"], "match": {"host": ["d.example"]}, "handler": {"module": "respond"}}]}`)
	ls, redirect = c.Effective()
	if len(ls) != 2 || ls[0].Name != "https" || ls[0].Address != ":443" || ls[1].Name != "http" || ls[1].Address != ":80" ||
		ls[1].TLS != nil || redirect["http"] != 443 {
		t.Fatalf("%+v, %v", ls, redirect)
	}
	if got := ls[0].TLS.ACME; !slices.Equal(got, []string{"a.example", "b.example", "c.example", "d.example"}) {
		t.Errorf("https: %v", got)
	}

	// Other ports, and no redirects.
	c = load(`{"auto_https": {"https_port": 8443, "http_port": 8080}, ` + routes + `}`)
	if ls, redirect = c.Effective(); len(ls) != 2 || ls[0].Address != ":8443" || ls[1].Address != ":8080" || redirect["http"] != 8443 {
		t.Errorf("ports: %+v, %v", ls, redirect)
	}
	c = load(`{"auto_https": {"disable_redirects": true}, ` + routes + `}`)
	if ls, redirect = c.Effective(); len(ls) != 1 || ls[0].Name != "https" || redirect != nil {
		t.Errorf("no redirects: %+v, %v", ls, redirect)
	}
}
