package btstream

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/bittorrent"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/fibtest"
	"github.com/lesismal/fit/observe"
	"github.com/lesismal/fit/runtime"
)

func TestParseRange(t *testing.T) {
	for _, c := range []struct {
		h                    string
		size                 int64
		start, n             int64
		partial, satisfiable bool
	}{
		{"", 100, 0, 100, false, true},
		{"bytes=0-9", 100, 0, 10, true, true},
		{"bytes=90-", 100, 90, 10, true, true},
		{"bytes=90-500", 100, 90, 10, true, true},
		{"bytes=-10", 100, 90, 10, true, true},
		{"bytes=-500", 100, 0, 100, true, true},
		{"bytes=-0", 100, 0, 0, false, false},
		{"bytes=100-", 100, 0, 0, false, false},
		{"bytes=0-", 0, 0, 0, false, false},
		{"bytes=-5", 0, 0, 0, false, false},
		{"bytes=9-0", 100, 0, 100, false, true},
		{"bytes=a-b", 100, 0, 100, false, true},
		{"bytes=-x", 100, 0, 100, false, true},
		{"bytes=0-x", 100, 0, 100, false, true},
		{"bytes=5", 100, 0, 100, false, true},
		{"items=0-9", 100, 0, 100, false, true},
		{"bytes=0-1,5-6", 100, 0, 100, false, true},
	} {
		start, n, partial, ok := parseRange(c.h, c.size)
		if start != c.start || n != c.n || partial != c.partial || ok != c.satisfiable {
			t.Errorf("%q of %d: %d %d %v %v", c.h, c.size, start, n, partial, ok)
		}
	}
}

func TestNoneMatchAndTypes(t *testing.T) {
	if !noneMatch(`"a", W/"b"`, `"b"`) || !noneMatch("*", `"x"`) || noneMatch(`"a"`, `"b"`) || noneMatch("", `"b"`) {
		t.Error("noneMatch")
	}
	for p, want := range map[string]string{"a/b.MKV": "video/x-matroska", "x.srt": "application/x-subrip", "y": "application/octet-stream", "z.bin": "application/octet-stream"} {
		if got := contentType(p); got != want {
			t.Errorf("%s: %s, want %s", p, got, want)
		}
	}
}

func TestProvisionErrors(t *testing.T) {
	for _, c := range []struct{ params, auth, want string }{
		{`{"magnet": true}`, "", "magnet needs auth"},
		{`{"prefix": "bt/"}`, "", "does not start with /"},
		{`{"max_streams": 5000}`, "", "max_streams"},
		{`{"readahead_bytes": 1000}`, "", "readahead_bytes"},
		{`{"max_temporary": 1001}`, "", "max_temporary"},
		{`{"temporary_ttl": "200h"}`, "", "temporary_ttl"},
		{`{"cors": "a\nb"}`, "", "line break"},
		{`{"auth": "nope"}`, "", "no media_auth policy"},
	} {
		auth := c.auth
		if auth == "" {
			auth = "[]"
		}
		doc := `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}], "media_auth": ` + auth + `,
			"routes": [{"match": {"path": ["/bt/*"]}, "handler": {"module": "bt_stream", "params": ` + c.params + `}}]}`
		cfg, err := config.Load([]byte(doc))
		if err == nil {
			_, err = runtime.Compile(cfg, runtime.Env{Metrics: observe.NewMetrics()})
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.params, err, c.want)
		}
	}
}

// TestNoWorkerBlocks serves a request whose torrent's info never comes:
// ServeHTTP returns while the stream waits, on its own goroutine, and the
// answer, 504, comes from there once info_timeout passed.
func TestNoWorkerBlocks(t *testing.T) {
	c := makeContent(t, 100000, 32<<10)
	f := startFit(t, `{"auth": "site", "magnet": true, "info_timeout": "300ms"}`, authConf)
	g := f.rt.Current()
	h := g.Listeners[0].Routes[0].Handler
	g.Release()
	path := "/bt/" + c.id + "/0?magnet=" + url.QueryEscape(c.mi.Magnet().String())
	r, _ := http.NewRequest("GET", "http://fit"+path, nil)
	r.RequestURI = path
	r.Header.Set("Authorization", "Bearer "+pubToken)
	rec := &fibtest.Recorder{}
	returned := make(chan struct{})
	go func() {
		fibhttp.Serve(h, fibhttp.NewStreamContext(nil, r, rec))
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(hang):
		t.Fatal("ServeHTTP did not return")
	}
	if rec.Wait(0) {
		t.Fatalf("answered %d before the info timeout", rec.Response.StatusCode)
	}
	if !rec.Wait(hang) {
		t.Fatal("no answer")
	}
	if rec.Response.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("status %d", rec.Response.StatusCode)
	}
}

// TestDrain ends a stream of a config swapped out.
func TestDrain(t *testing.T) {
	c := makeContent(t, 1<<20, 64<<10)
	f := startFit(t, `{"prefix": "/bt"}`, "")
	h := f.handler(t)
	if _, err := f.role.Add(bittorrent.AddRequest{Torrent: c.torrent}); err != nil {
		t.Fatal(err)
	}
	got := make(chan result, 1)
	go func() { got <- fetch("GET", f.base+"/bt/"+c.id+"/show/ep1.mp4") }()
	waitFor(t, "the stream", func() bool { return h.Streams() == 1 })
	h.Drain()
	r := <-got
	if r.err != nil || r.resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status %d len %d cl %d hdr %v", r.resp.StatusCode, len(r.body), r.resp.ContentLength, r.resp.Header)
	}
	waitFor(t, "the stream to end", func() bool { return h.Streams() == 0 })
	f.must(t, "GET", "/bt/"+c.id+"/show/ep1.mp4", http.StatusServiceUnavailable)
}

// TestDrainBeforeHead drains a stream as it is about to read its first
// chunk: the response, not begun, is 503 with Retry-After, not an empty 200.
func TestDrainBeforeHead(t *testing.T) {
	c := makeContent(t, 1<<20, 64<<10)
	f := startFit(t, `{"prefix": "/bt"}`, "")
	h := f.handler(t)
	if _, err := f.role.Add(bittorrent.AddRequest{Torrent: c.torrent}); err != nil {
		t.Fatal(err)
	}
	testHookPump = h.Drain
	t.Cleanup(func() { testHookPump = nil })
	r := fetch("GET", f.base+"/bt/"+c.id+"/show/ep1.mp4")
	if r.err != nil || r.resp.StatusCode != http.StatusServiceUnavailable || r.resp.Header.Get("Retry-After") == "" {
		t.Fatalf("err %v status %d len %d hdr %v", r.err, r.resp.StatusCode, len(r.body), r.resp.Header)
	}
	waitFor(t, "the stream to end", func() bool { return h.Streams() == 0 })
}
