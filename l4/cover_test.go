package l4_test

import (
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/listener"
	"github.com/lesismal/fit/runtime"
	"github.com/lesismal/fit/upstream"
)

// TestTCPEngineStopped closes a client whose upstream cannot be dialed at
// all, the engine that dials it having stopped: each attempt fails at once,
// without a callback.
func TestTCPEngineStopped(t *testing.T) {
	c, err := config.Load([]byte(tcpConfig("", backend(t, echo))))
	if err != nil {
		t.Fatal(err)
	}
	engine, stop, err := upstream.StartEngine()
	if err != nil {
		t.Fatal(err)
	}
	g, err := runtime.Compile(c, runtime.Env{Engine: engine})
	if err != nil {
		stop()
		t.Fatal(err)
	}
	rt := new(runtime.Runtime)
	rt.Swap(g)
	s, err := listener.Start(rt)
	if err != nil {
		stop()
		t.Fatal(err)
	}
	stop()
	t.Cleanup(func() {
		_ = s.Close()
		rt.Close()
	})
	f := &fit{t: t, addr: s.Addrs()[0].Addr.String()}
	expectClosed(t, f.dial(0), 5*time.Second)
}
