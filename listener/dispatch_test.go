package listener

import (
	"fmt"
	"io"
	stdhttp "net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/fibtest"
	"github.com/lesismal/fit/observe"
	"github.com/lesismal/fit/runtime"
)

// dispatchDoc has four listeners, the requests going to the last, so that a
// route found by scanning the listeners pays for it.
const dispatchDoc = `{
  "listeners": [
    {"name": "a", "address": "127.0.0.1:0"},
    {"name": "b", "address": "127.0.0.1:0"},
    {"name": "c", "address": "127.0.0.1:0"},
    {"name": "web", "address": "127.0.0.1:0"}
  ],
  "routes": [
    {"name": "hello", "listeners": ["web"], "match": {"path": ["/hello"]},
     "handler": {"module": "respond", "params": {"body": "hello\n"}}}
  ]
}`

// BenchmarkDispatch serves a request through a listener's dispatcher, with
// and without the access log and metrics, without a network: what fit adds
// to every request.
func BenchmarkDispatch(b *testing.B) {
	for _, bb := range []struct {
		name         string
		log, metrics bool
	}{
		{"bare", false, false},
		{"log", true, false},
		{"metrics", false, true},
		{"log+metrics", true, true},
	} {
		b.Run(bb.name, func(b *testing.B) { benchmarkDispatch(b, bb.log, bb.metrics) })
	}
}

func benchmarkDispatch(b *testing.B, withLog, withMetrics bool) {
	doc := dispatchDoc
	if withMetrics {
		doc = strings.Replace(doc, "{", `{"admin": {}, "metrics": {},`, 1)
	}
	c, err := config.Load([]byte(doc))
	if err != nil {
		b.Fatal(err)
	}
	var env runtime.Env
	if withLog {
		env.AccessLog = observe.NewAccessLogTo(io.Discard, time.Second, 64<<20)
		defer env.AccessLog.Close()
	}
	if withMetrics {
		env.Metrics = observe.NewMetrics()
	}
	g, err := runtime.Compile(c, env)
	if err != nil {
		b.Fatal(err)
	}
	rt := new(runtime.Runtime)
	rt.Swap(g)
	defer rt.Close()
	d := &dispatcher{rt: rt, name: "web"}
	var clients atomic.Int32
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		remote := fmt.Sprintf("192.0.2.%d:4321", clients.Add(1))
		u := &url.URL{Path: "/hello"}
		for pb.Next() {
			r := &stdhttp.Request{Method: "GET", URL: u, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
				Header: stdhttp.Header{}, Host: "example.com", RemoteAddr: remote, RequestURI: "/hello"}
			if rec := fibtest.ServeRequest(d, r); rec.Response.StatusCode != 200 && rec.Response.StatusCode != 0 {
				b.Fatalf("status %d", rec.Response.StatusCode)
			}
		}
	})
}

// TestDispatchFollowsReload checks that a dispatcher finds its listener in
// each new graph, wherever the graph has it, and none in a graph without it.
func TestDispatchFollowsReload(t *testing.T) {
	rt := new(runtime.Runtime)
	defer rt.Close()
	d := &dispatcher{rt: rt, name: "web"}
	body := func() string {
		rec := fibtest.Serve(d, "GET", "/hello")
		return string(rec.Response.Body)
	}
	doc := func(names []string, reply string) string {
		ls := ""
		for i, n := range names {
			if i > 0 {
				ls += ","
			}
			ls += `{"name": "` + n + `", "address": "127.0.0.1:0"}`
		}
		return `{"listeners": [` + ls + `], "routes": [{"name": "r", "listeners": ["` + names[len(names)-1] + `"],
		  "handler": {"module": "respond", "params": {"body": "` + reply + `"}}}]}`
	}
	for _, step := range []struct {
		names []string
		want  string
	}{
		{[]string{"web"}, "one"},
		{[]string{"a", "b", "web"}, "three"},
		{[]string{"web", "other"}, "404 page not found\n"},
		{[]string{"a", "web"}, "two"},
	} {
		swap(t, rt, doc(step.names, strings.TrimSuffix(step.want, "\n")))
		for range 2 { // the first request looks the listener up, the second finds it kept
			if got := body(); got != step.want {
				t.Fatalf("listeners %v: %q, want %q", step.names, got, step.want)
			}
		}
	}
}
