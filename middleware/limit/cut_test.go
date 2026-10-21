package limit

import (
	"net"
	stdhttp "net/http"
	"testing"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/listener"
	"github.com/lesismal/fit/module"
	"github.com/lesismal/fit/runtime"
)

func init() {
	module.Register(module.Info{ID: module.Handlers + ".test_hang", New: func() any { return new(hang) }})
}

// hang answers /ok at once, and keeps every other request until its client
// goes, as a proxy waiting on a slow upstream does.
type hang struct{}

func (hang) ServeHTTP(c *fibhttp.Context) {
	if c.Request.URL.Path == "/ok" {
		_ = c.Respond(200, "text/plain", []byte("ok"))
		return
	}
	c.Retain()
	c.OnCancel(func(error) { c.Release() })
}

// TestConcurrencyClientGone checks, over the network, that a request whose
// client hangs up gives its place back: the third way a request ends.
func TestConcurrencyClientGone(t *testing.T) {
	c, err := config.Load([]byte(`{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "routes": [{"middleware": [{"module": "concurrency_limit", "params": {"max": 1}}], "handler": {"module": "test_hang"}}]
}`))
	if err != nil {
		t.Fatal(err)
	}
	g, err := runtime.Compile(c, runtime.Env{})
	if err != nil {
		t.Fatal(err)
	}
	rt := new(runtime.Runtime)
	rt.Swap(g)
	s, err := listener.Start(rt)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = s.Close()
		rt.Close()
	}()
	addr := s.Addrs()[0].Addr.String()
	get := func(path string) int {
		res, err := (&stdhttp.Client{Timeout: 5 * time.Second}).Get("http://" + addr + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}

	for round := range 3 {
		// A request that hangs takes the one place.
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Write([]byte("GET /hang HTTP/1.1\r\nHost: x\r\n\r\n")); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for get("/ok") != 429 {
			if time.Now().After(deadline) {
				t.Fatalf("round %d: the hanging request never took the place", round)
			}
			time.Sleep(5 * time.Millisecond)
		}
		// Its client goes: the place is free again.
		conn.Close()
		for get("/ok") != 200 {
			if time.Now().After(deadline) {
				t.Fatalf("round %d: the place was not given back when the client went", round)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}
