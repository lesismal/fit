package listener

import (
	"testing"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/runtime"
)

// TestStartDefaultQueue checks a graph that bounds no connection's write
// queue, as one made without a config's limits, still starts, with fib's
// sizing.
func TestStartDefaultQueue(t *testing.T) {
	c, err := config.Load([]byte(`{"listeners": [{"name": "web", "address": "127.0.0.1:0"}], "routes": [{"handler": {"module": "respond"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	g, err := runtime.Compile(c, runtime.Env{})
	if err != nil {
		t.Fatal(err)
	}
	g.MaxWriteQueueBytes = 0
	rt := new(runtime.Runtime)
	rt.Swap(g)
	defer rt.Close()
	s, err := Start(rt)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
}
