package runtime

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/certtest"
	"github.com/lesismal/fit/module"
)

// holder is a handler that holds its graph for each request until the test
// lets go, as the proxy does, and counts its drains.
type holder struct {
	graph   module.Holder
	drained atomic.Int32
}

func (h *holder) Provision(ctx *module.Context) error {
	h.graph = ctx.Graph()
	return nil
}

func (h *holder) ServeHTTP(c *fibhttp.Context) { h.graph.Retain() }
func (h *holder) Drain()                       { h.drained.Add(1) }

func init() {
	module.Register(module.Info{ID: module.Handlers + ".test_holder", New: func() any { return new(holder) }})
}

const holderDoc = `{"listeners": [{"name": "web", "address": ":0"}], "routes": [{"handler": {"module": "test_holder"}}]}`

func holderOf(g *Graph) *holder { return g.modules[0].(*holder) }

// TestDrain: a graph swapped out while a request holds it is drained after
// the drain timeout, and cleaned up once the request lets go; one whose
// requests are done first is never drained.
func TestDrain(t *testing.T) {
	rt := new(Runtime)
	rt.SetDrainTimeout(100 * time.Millisecond)
	defer rt.Close()

	g1 := mustCompile(t, holderDoc)
	rt.Swap(g1)
	h1 := holderOf(g1)
	h1.ServeHTTP(nil) // a request that outlives its handler
	rt.Swap(mustCompile(t, holderDoc))
	time.Sleep(50 * time.Millisecond)
	if h1.drained.Load() != 0 {
		t.Fatal("drained before the drain timeout")
	}
	time.Sleep(150 * time.Millisecond)
	if h1.drained.Load() != 1 {
		t.Fatalf("drained %d times after the timeout", h1.drained.Load())
	}
	if g1.refs.Load() != 1 {
		t.Fatalf("refs %d", g1.refs.Load())
	}
	h1.graph.Release()
	if !g1.cleaned {
		t.Fatal("not cleaned up once the request let go")
	}

	// Done before the timeout: cleaned up, never drained.
	g3 := mustCompile(t, holderDoc)
	rt.Swap(g3)
	h3 := holderOf(g3)
	h3.ServeHTTP(nil)
	rt.Swap(mustCompile(t, holderDoc))
	h3.graph.Release()
	time.Sleep(200 * time.Millisecond)
	if h3.drained.Load() != 0 || !g3.cleaned {
		t.Fatalf("drained %d, cleaned %v", h3.drained.Load(), g3.cleaned)
	}

	// A negative timeout never drains.
	rt.SetDrainTimeout(-1)
	g5 := rt.Current()
	g5.Release()
	h5 := holderOf(g5)
	h5.ServeHTTP(nil)
	rt.Swap(mustCompile(t, holderDoc))
	time.Sleep(150 * time.Millisecond)
	if h5.drained.Load() != 0 {
		t.Fatal("drained with a negative timeout")
	}
	h5.graph.Release()
}

func upstreamDoc(extra string) string {
	return `{"listeners": [{"name": "web", "address": ":0"}],
  "upstreams": [{"name": "a", "addresses": ["127.0.0.1:1"]}, {"name": "b", "addresses": ["127.0.0.1:2"]` + extra + `}],
  "routes": [{"handler": {"module": "respond"}}]}`
}

func compileAfter(t *testing.T, doc string, prev *Graph) *Graph {
	t.Helper()
	c, err := config.Load([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	g, err := Compile(c, Env{Previous: prev})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// TestSharedPools: a graph takes over the pools of the upstreams it leaves
// as they are, CA file included, and each pool closes when the last graph
// using it is done.
func TestSharedPools(t *testing.T) {
	g1 := compileAfter(t, upstreamDoc(""), nil)
	g2 := compileAfter(t, upstreamDoc(`, "max_conns": 9`), g1)
	a1, _ := g1.Upstream("a")
	a2, _ := g2.Upstream("a")
	b1, _ := g1.Upstream("b")
	b2, _ := g2.Upstream("b")
	if a1 != a2 || b1 == b2 {
		t.Fatalf("a shared %v, b shared %v", a1 == a2, b1 == b2)
	}
	g1.Release()

	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	writeCA(t, ca, "a")
	withCA := `, "protocol": "h2", "tls_ca_file": "` + ca + `"`
	g3 := compileAfter(t, upstreamDoc(withCA), g2)
	g4 := compileAfter(t, upstreamDoc(withCA), g3)
	b3, _ := g3.Upstream("b")
	b4, _ := g4.Upstream("b")
	if b3 != b4 {
		t.Fatal("an unchanged upstream with a CA file was not shared")
	}
	// The CA file changes: a new pool, which reads it again.
	writeCA(t, ca, "b")
	g5 := compileAfter(t, upstreamDoc(withCA), g4)
	if b5, _ := g5.Upstream("b"); b5 == b4 {
		t.Fatal("a changed CA file kept the old pool")
	}
	for _, g := range []*Graph{g2, g3, g4, g5} {
		g.Release()
	}
}

// writeCA writes a new certificate for name to path.
func writeCA(t *testing.T, path, name string) {
	t.Helper()
	c, err := certtest.Write(t.TempDir(), name)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(c.CertFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
