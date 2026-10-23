package runtime

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/certtest"
	"github.com/lesismal/fit/module"
	"github.com/lesismal/fit/tlsmgr"
)

func init() {
	module.Register(module.Info{ID: module.Middlewares + ".test_not_middleware", New: func() any { return new(struct{}) }})
}

// TestCompileFails: what only compiling a config finds fails it, and what
// was made for it is cleaned up.
func TestCompileFails(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.pem")
	cert, err := certtest.Write(t.TempDir(), "a.example")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, doc, want string
	}{
		{"upstream CA file", `{"listeners": [{"name": "web", "address": ":0"}],
  "upstreams": [{"name": "a", "addresses": ["127.0.0.1:1"]}, {"name": "b", "addresses": ["127.0.0.1:2"], "protocol": "h2", "tls_ca_file": "` + filepath.ToSlash(missing) + `"}],
  "routes": [{"handler": {"module": "respond"}}]}`, `upstreams[1].tls_ca_file`},
		{"client CA file", fmt.Sprintf(`{"listeners": [{"name": "web", "address": ":0",
  "tls": {"certificates": [{"cert_file": %q, "key_file": %q}], "client_auth": {"ca_files": [%q]}}}],
  "routes": [{"handler": {"module": "respond"}}]}`, cert.CertFile, cert.KeyFile, missing), `listeners[0].tls.client_auth`},
		{"not a middleware", `{"listeners": [{"name": "web", "address": ":0"}],
  "routes": [{"middleware": [{"module": "test_not_middleware"}], "handler": {"module": "respond"}}]}`,
			`http.middleware.test_not_middleware is not a middleware`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := compile(t, tt.doc); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Compile: %v\nwant %s", err, tt.want)
			}
		})
	}
}

// TestCompileBadMatchers: matchers config.Load would have refused fail
// compiling too.
func TestCompileBadMatchers(t *testing.T) {
	for name, set := range map[string]func(*config.Match){
		"remote_ip":   func(m *config.Match) { m.RemoteIP = []string{"bogus"} },
		"path_regexp": func(m *config.Match) { m.PathRegexp = []string{"("} },
		"expr":        func(m *config.Match) { m.Expr = "(((" },
	} {
		c, err := config.Load([]byte(`{"listeners": [{"name": "web", "address": ":0"}],
  "routes": [{"match": {"path": ["/"]}, "handler": {"module": "respond"}}]}`))
		if err != nil {
			t.Fatal(err)
		}
		set(c.Routes[0].Match)
		if _, err := Compile(c, Env{}); err == nil {
			t.Errorf("%s: compiled", name)
		}
	}
}

// TestCompileACME: a listener's acme names are the graph's, once each.
func TestCompileACME(t *testing.T) {
	acme, err := tlsmgr.NewACME(&config.ACME{}, &tlsmgr.FileStorage{Root: t.TempDir()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer acme.Close()
	c, err := config.Load([]byte(`{"listeners": [
    {"name": "a", "address": ":0", "tls": {"acme": ["a.example", "b.example"]}},
    {"name": "b", "address": ":0", "tls": {"acme": ["a.example"]}}],
  "routes": [{"handler": {"module": "respond"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	g, err := Compile(c, Env{ACME: acme})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Release()
	if strings.Join(g.ACMENames, ",") != "a.example,b.example" {
		t.Fatalf("names %q", g.ACMENames)
	}
}

func TestGraphMore(t *testing.T) {
	g := mustCompile(t, upstreamDoc(""))
	if ps := g.Upstreams(); len(ps) != 2 || ps[0].Name() != "a" || ps[1].Name() != "b" {
		t.Errorf("upstreams %v", ps)
	}
	// A file whose sum two loads disagree on is given one no file has, so
	// that the watcher loads it again.
	g.watchFiles(map[string][sha256.Size]byte{"f": {1}})
	g.watchFiles(map[string][sha256.Size]byte{"f": {2}})
	if g.CertFiles["f"] != ([sha256.Size]byte{}) {
		t.Errorf("sum %x", g.CertFiles["f"])
	}
	g.Release()
	// A graph cleaned up is not drained.
	g.drain()
}
