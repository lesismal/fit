package limit

import (
	"io"
	"net"
	stdhttp "net/http"
	"strconv"
	"strings"
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
	module.Register(module.Info{ID: module.Handlers + ".test_count", New: func() any { return new(count) }})
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

// count answers with how many bytes the request's body had, taking a
// streamed one with OnBody; a body that fails is let go unanswered.
type count struct{}

func (count) ServeHTTP(c *fibhttp.Context) {
	if c.RequestBody() == nil {
		_ = c.Respond(200, "text/plain", []byte(strconv.Itoa(len(c.Body()))))
		return
	}
	c.Retain()
	n := 0
	c.OnBody(func(data []byte, fin bool, err error) {
		n += len(data)
		switch {
		case err != nil:
			c.Release()
		case fin:
			_ = c.Respond(200, "text/plain", []byte(strconv.Itoa(n)))
			c.Release()
		}
	})
}

// serve starts a listener for config, closed when the test ends, and
// returns its address.
func serve(t *testing.T, cfg string) string {
	t.Helper()
	c, err := config.Load([]byte(cfg))
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
	t.Cleanup(func() {
		_ = s.Close()
		rt.Close()
	})
	return s.Addrs()[0].Addr.String()
}

// TestBodyOverNetwork checks body_limit on bodies sent chunked, with no
// declared length, small enough to arrive whole and large enough to
// stream, and on declared lengths, over HTTP/1.1.
func TestBodyOverNetwork(t *testing.T) {
	addr := serve(t, `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "routes": [{"middleware": [{"module": "body_limit", "params": {"max_bytes": 100000}}], "handler": {"module": "test_count"}}]
}`)
	client := &stdhttp.Client{Timeout: 10 * time.Second}
	for _, tt := range []struct {
		size    int
		chunked bool
		want    int
	}{
		{1000, true, 200}, {100000, true, 200}, {100001, true, 413}, {1 << 20, true, 413},
		{100000, false, 200}, {100001, false, 413},
	} {
		var body io.Reader = strings.NewReader(strings.Repeat("x", tt.size))
		if tt.chunked {
			body = io.MultiReader(body) // hides the length: sent chunked
		}
		req, err := stdhttp.NewRequest("POST", "http://"+addr+"/", body)
		if err != nil {
			t.Fatal(err)
		}
		res, err := client.Do(req)
		if err != nil {
			// The server may refuse before the client has sent the whole
			// body, and close: a 413 the client did not get to read.
			if tt.want == 413 {
				continue
			}
			t.Fatalf("%d bytes, chunked %v: %v", tt.size, tt.chunked, err)
		}
		got, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != tt.want {
			t.Errorf("%d bytes, chunked %v: %d, want %d", tt.size, tt.chunked, res.StatusCode, tt.want)
		} else if tt.want == 200 && string(got) != strconv.Itoa(tt.size) {
			t.Errorf("%d bytes, chunked %v: the handler saw %s", tt.size, tt.chunked, got)
		}
	}
}

// TestConcurrencyClientGone checks, over the network, that a request whose
// client hangs up gives its place back: the third way a request ends.
func TestConcurrencyClientGone(t *testing.T) {
	addr := serve(t, `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "routes": [{"middleware": [{"module": "concurrency_limit", "params": {"max": 1}}], "handler": {"module": "test_hang"}}]
}`)
	get := func(path string) int {
		res, err := (&stdhttp.Client{Timeout: 5 * time.Second}).Get("http://" + addr + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}

	// hang sends a request that hangs, and reports whether it took the place
	// rather than being refused. The /ok requests that check on it take the
	// place too, for as long as each is served, and one sent on a connection
	// kept alive may well be served before /hang, which comes on a new one;
	// /hang is then refused, rightly, and sent again.
	hang := func() (net.Conn, bool) {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Write([]byte("GET /hang HTTP/1.1\r\nHost: x\r\n\r\n")); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if get("/ok") == 429 {
				return conn, true
			}
			// Whatever /hang was answered with comes back meanwhile.
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Millisecond))
			if n, _ := conn.Read(make([]byte, 64)); n > 0 {
				conn.Close()
				return nil, false
			}
		}
		t.Fatal("the hanging request was neither kept nor refused")
		return nil, false
	}

	for round := range 3 {
		// A request that hangs takes the one place.
		var conn net.Conn
		for tries := 0; ; tries++ {
			var kept bool
			if conn, kept = hang(); kept {
				break
			}
			if tries == 10 {
				t.Fatalf("round %d: the hanging request was refused every time", round)
			}
		}
		deadline := time.Now().Add(5 * time.Second)
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
