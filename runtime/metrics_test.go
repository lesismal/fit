package runtime

import (
	"testing"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/observe"
)

// TestMetricsLifetime checks that a graph takes the RequestMetrics of each
// listener's routes, shares them with the graph after it, and gives them
// back when it is cleaned up or fails to compile.
func TestMetricsLifetime(t *testing.T) {
	m := observe.NewMetrics()
	compileWith := func(doc string) (*Graph, error) {
		t.Helper()
		c, err := config.Load([]byte(doc))
		if err != nil {
			t.Fatal(err)
		}
		return Compile(c, Env{Metrics: m})
	}
	keys := func() []string {
		var ks []string
		for _, r := range m.AllRequests() {
			ks = append(ks, r.Listener+"/"+r.Route)
		}
		return ks
	}
	doc := func(route string) string {
		return `{"admin": {}, "metrics": {}, "listeners": [{"name": "a", "address": ":0"}, {"name": "b", "address": ":0"}],
  "routes": [
    {"name": "` + route + `", "handler": {"module": "respond"}},
    {"listeners": ["b"], "handler": {"module": "respond"}}
  ]}`
	}
	g1, err := compileWith(doc("r"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a/", "a/r", "b/", "b/#1", "b/r"}
	if got := keys(); !equal(got, want) {
		t.Fatalf("after one graph: %v, want %v", got, want)
	}
	l := g1.Listeners[1]
	if r := l.RequestMetrics(l.Routes[0]); r == nil || r.Listener != "b" {
		t.Fatalf("b's first route counts in %+v", r)
	}
	if r := g1.Listeners[0].RequestMetrics(nil); r == nil || r.Route != "" {
		t.Fatalf("a's unmatched requests count in %+v", r)
	}

	g2, err := compileWith(doc("s"))
	if err != nil {
		t.Fatal(err)
	}
	if g1.Listeners[0].Unmatched != g2.Listeners[0].Unmatched {
		t.Error("two graphs count a listener's unmatched requests apart")
	}
	// A listener that fails after another took its routes' metrics.
	if _, err := compileWith(`{"admin": {}, "metrics": {}, "listeners": [{"name": "c", "address": ":0"},
    {"name": "d", "address": ":0", "tls": {"certificates": [{"cert_file": "/nonexistent.crt", "key_file": "/nonexistent.key"}]}}],
  "routes": [{"name": "x", "handler": {"module": "respond"}}]}`); err == nil {
		t.Fatal("a broken config compiled")
	}
	g1.Release()
	want = []string{"a/", "a/s", "b/", "b/#1", "b/s"}
	if got := keys(); !equal(got, want) {
		t.Fatalf("after the first graph and a broken one: %v, want %v", got, want)
	}
	g2.Release()
	if got := keys(); len(got) != 0 {
		t.Fatalf("after every graph: %v", got)
	}

	// A config without metrics counts nothing.
	g3, err := compileWith(`{"listeners": [{"name": "a", "address": ":0"}], "routes": [{"handler": {"module": "respond"}}]}`)
	if err != nil {
		t.Fatal(err)
	}
	defer g3.Release()
	if l := g3.Listeners[0]; g3.Metrics != nil || l.RequestMetrics(nil) != nil || l.RequestMetrics(l.Routes[0]) != nil {
		t.Error("a config without metrics counts requests")
	}
	if got := keys(); len(got) != 0 {
		t.Errorf("a config without metrics took %v", got)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
