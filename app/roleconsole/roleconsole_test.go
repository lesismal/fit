package roleconsole

import (
	"testing"

	"github.com/lesismal/fib"

	"github.com/lesismal/fit/app"
)

func TestModule(t *testing.T) {
	e, err := fib.New(fib.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = e.Run(); close(done) }()
	t.Cleanup(func() { e.Stop(); <-done; _ = e.Close() })

	c := New(&app.Host{Engine: e}, "tunnel.hub", nil)
	if m := c.Module(); m == nil || m.ID != "tunnel.hub" || c.cookie != "fit_tunnel_hub" {
		t.Fatalf("module %+v, cookie %q", m, c.cookie)
	}
	if o := c.options(nil); o.Modules()[0] != c.Module() {
		t.Fatal("the console's module is not the role's")
	}
}
