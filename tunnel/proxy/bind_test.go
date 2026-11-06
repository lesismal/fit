package proxy

import (
	"context"
	"testing"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/bindtest"
	"github.com/lesismal/fit/tunnel/internal/tunneltest"
)

// TestForwardsRefuseHeldAddresses checks a forward and a web proxy do not
// listen beside another server on their addresses, which fib's SO_REUSEPORT
// would let them, and each says why.
func TestForwardsRefuseHeldAddresses(t *testing.T) {
	c := New(&app.Host{ID: ID, Engine: tunneltest.Engine(t)})
	defer c.Stop(context.Background())
	if err := c.Apply(&config.TunnelProxy{Name: "me"}); err != nil {
		t.Fatal(err)
	}
	fwd, web := bindtest.Held(t, "tcp"), bindtest.Held(t, "tcp")
	err := c.Apply(&config.TunnelProxy{Name: "me",
		Forwards:   []config.TunnelForward{{Listen: fwd, Node: "n", Service: "s"}},
		WebProxies: []config.TunnelWebProxy{{Listen: web, URL: "ws://127.0.0.1:1/", Password: "p"}},
	})
	bindtest.Refused(t, err, "tcp", fwd)
	bindtest.Refused(t, err, "tcp", web)
	if fs := c.Forwards(); len(fs) != 1 || fs[0].Addr != "" {
		t.Fatalf("forwards %+v", fs)
	}
	if ws := c.WebProxies(); len(ws) != 1 || ws[0].Addr != "" {
		t.Fatalf("web proxies %+v", ws)
	}
}
