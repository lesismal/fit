package cache

import (
	"context"
	"fmt"
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/fibtest"
	"github.com/lesismal/fit/listener"
	_ "github.com/lesismal/fit/middleware/compress"
	"github.com/lesismal/fit/module"
	"github.com/lesismal/fit/runtime"
)

func init() {
	module.Register(module.Info{ID: module.Handlers + ".test_origin", New: func() any { return new(origin) }})
}

// The origin's state, which the tests read and set.
var (
	hits   sync.Map // path → *atomic.Int64
	answer atomic.Pointer[func(c *fibhttp.Context)]
	// release lets the slow stream go on.
	release = make(chan struct{}, 1)
	// gone counts the slow streams whose client the server heard go.
	gone atomic.Int64
	// failing has the paths that answer 500 when it is set do so.
	failing atomic.Bool
)

func hitsOf(path string) int64 {
	n, _ := hits.LoadOrStore(path, new(atomic.Int64))
	return n.(*atomic.Int64).Load()
}

func resetHits() { hits.Clear() }

// long is a body larger than a test's max_entry_bytes.
var long = strings.Repeat("0123456789abcdef", 64<<10)

// origin answers by path, counting what it answers.
type origin struct{}

func (origin) ServeHTTP(c *fibhttp.Context) {
	p := c.Request.URL.Path
	n, _ := hits.LoadOrStore(p, new(atomic.Int64))
	count := n.(*atomic.Int64).Add(1)
	if fn := answer.Load(); fn != nil && strings.HasPrefix(p, "/custom") {
		(*fn)(c)
		return
	}
	date := clock().UTC().Format(stdhttp.TimeFormat)
	h := stdhttp.Header{"Content-Type": {"text/plain"}, "Date": {date}}
	body := fmt.Sprintf("%s %d", p, count)
	status := 200
	switch p {
	case "/fresh":
		h["Cache-Control"] = []string{"max-age=60"}
		h["Etag"] = []string{`"v1"`}
		h["Last-Modified"] = []string{"Mon, 02 Jan 2006 15:04:05 GMT"}
	case "/s-maxage":
		h["Cache-Control"] = []string{"max-age=0, s-maxage=60"}
	case "/expires":
		h["Expires"] = []string{clock().Add(time.Minute).UTC().Format(stdhttp.TimeFormat)}
	case "/expires-bad":
		h["Expires"] = []string{"0"}
	case "/heuristic":
		h["Last-Modified"] = []string{clock().Add(-100 * time.Second).UTC().Format(stdhttp.TimeFormat)}
	case "/plain":
	case "/no-store":
		h["Cache-Control"] = []string{"max-age=60, no-store"}
	case "/private":
		h["Cache-Control"] = []string{"private, max-age=60"}
	case "/no-cache":
		h["Cache-Control"] = []string{"no-cache, max-age=60"}
	case "/cookie":
		h["Cache-Control"] = []string{"max-age=60"}
		h["Set-Cookie"] = []string{"a=b"}
	case "/vary-star":
		h["Cache-Control"] = []string{"max-age=60"}
		h["Vary"] = []string{"*"}
	case "/error":
		h["Cache-Control"] = []string{"max-age=60"}
		status = 500
	case "/found":
		h["Cache-Control"] = []string{"max-age=60"}
		h["Location"] = []string{"/fresh"}
		status = 302
	case "/found-plain":
		h["Location"] = []string{"/fresh"}
		status = 302
	case "/aged":
		h["Cache-Control"] = []string{"max-age=60"}
		h["Age"] = []string{"70"}
	case "/vary":
		h["Cache-Control"] = []string{"max-age=60"}
		h["Vary"] = []string{"Accept-Language"}
		body += " " + c.Request.Header.Get("Accept-Language")
	case "/must":
		h["Cache-Control"] = []string{"max-age=60, must-revalidate"}
	case "/public":
		h["Cache-Control"] = []string{"public, max-age=60"}
	case "/large":
		h["Cache-Control"] = []string{"max-age=60"}
		body = long
	case "/stream", "/slow":
		c.Header().Set("Cache-Control", "max-age=60")
		c.Header().Set("Content-Type", "text/plain")
		for i := range 64 {
			if p == "/slow" && i == 1 {
				c.OnCancel(func(error) { gone.Add(1) })
				c.Flush()
				select {
				case <-release:
				case <-time.After(5 * time.Second):
				}
			}
			_, _ = io.WriteString(c, long[i*1024:(i+1)*1024])
		}
		return
	case "/short":
		c.Header().Set("Cache-Control", "max-age=60")
		c.Header().Set("Content-Length", "100")
		_, _ = io.WriteString(c, "only part of it")
		return
	case "/revalidate":
		h["Cache-Control"] = []string{"max-age=60"}
		h["Etag"] = []string{`"r1"`}
		if c.Request.Header.Get("If-None-Match") == `"r1"` {
			h["Cache-Control"] = []string{"max-age=120"}
			_ = c.WriteResponse(fibhttp.Response{StatusCode: 304, Header: h})
			return
		}
	case "/late", "/late-private":
		// Answered later, from a timer, as an upstream that takes its time.
		h["Cache-Control"] = []string{"max-age=60"}
		if p == "/late-private" {
			h["Cache-Control"] = []string{"private"}
		}
		delay := 200 * time.Millisecond
		if d, err := time.ParseDuration(c.Request.URL.Query().Get("delay")); err == nil {
			delay = d
		}
		c.Retain()
		time.AfterFunc(delay, func() {
			_ = c.WriteResponse(fibhttp.Response{StatusCode: 200, Header: h, Body: []byte(body)})
			c.Release()
		})
		return
	case "/swr":
		h["Cache-Control"] = []string{"max-age=10, stale-while-revalidate=60"}
		h["Etag"] = []string{`"s1"`}
		if c.Request.Header.Get("If-None-Match") == `"s1"` {
			_ = c.WriteResponse(fibhttp.Response{StatusCode: 304, Header: h})
			return
		}
	case "/sie", "/sie-param":
		h["Cache-Control"] = []string{"max-age=10, stale-if-error=60"}
		if p == "/sie-param" {
			h["Cache-Control"] = []string{"max-age=10"}
		}
		if failing.Load() {
			status = 500
			h["Cache-Control"] = nil
		}
	case "/tagged":
		h["Cache-Control"] = []string{"max-age=60"}
		h["Cache-Tag"] = []string{"t1, t2"}
		if c.Request.URL.RawQuery == "b" {
			h["Cache-Tag"] = nil
			h["Surrogate-Key"] = []string{"t2 t3"}
		}
	case "/gzip":
		h["Cache-Control"] = []string{"max-age=60"}
		h["Content-Type"] = []string{"application/json"}
		body = strings.Repeat(`{"a": 1}`, 1000)
	default:
		status = 404
	}
	_ = c.WriteResponse(fibhttp.Response{StatusCode: status, Header: h, Body: []byte(body)})
}

// at moves the cache's clock to start plus d for the rest of the test.
func at(t *testing.T, start time.Time, d time.Duration) {
	clock = func() time.Time { return start.Add(d) }
	t.Cleanup(func() { clock = time.Now })
}

// serve runs fit with middleware in front of the origin, and returns its
// address.
func serve(t *testing.T, middleware string) string {
	t.Helper()
	base, stop := start(t, middleware)
	t.Cleanup(func() {
		stop()
		resetHits()
		answer.Store(nil)
		failing.Store(false)
	})
	return base
}

// start runs fit with middleware in front of the origin, and returns its
// address and what stops it.
func start(t *testing.T, middleware string) (string, func()) {
	t.Helper()
	c, err := config.Load([]byte(`{
  "listeners": [{"name": "web", "address": "127.0.0.1:0", "protocols": ["http/1.1", "h2c"]}],
  "routes": [{"middleware": ` + middleware + `, "handler": {"module": "test_origin"}}]
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
	return "http://" + s.Addrs()[0].Addr.String(), func() {
		_ = s.Close()
		rt.Close()
	}
}

func clients() map[string]*stdhttp.Client {
	var h1, h2 stdhttp.Protocols
	h1.SetHTTP1(true)
	h2.SetUnencryptedHTTP2(true)
	return map[string]*stdhttp.Client{
		"http/1.1": {Transport: &stdhttp.Transport{Protocols: &h1, DisableCompression: true}, Timeout: 10 * time.Second},
		"h2c":      {Transport: &stdhttp.Transport{Protocols: &h2, DisableCompression: true}, Timeout: 10 * time.Second},
	}
}

type reply struct {
	status int
	header stdhttp.Header
	body   string
}

func (r reply) cacheStatus() string { return r.header.Get("Cache-Status") }

// freshHit reports whether r was answered with a fresh response the cache
// keeps in memory.
func freshHit(r reply) bool {
	cs := r.cacheStatus()
	return strings.HasPrefix(cs, "fit; hit; ttl=") && !strings.Contains(cs, "ttl=-") && !strings.Contains(cs, "detail")
}

func get(t *testing.T, c *stdhttp.Client, method, url string, fields ...string) reply {
	t.Helper()
	req, _ := stdhttp.NewRequest(method, url, nil)
	for i := 0; i+1 < len(fields); i += 2 {
		if fields[i] == "Host" {
			req.Host = fields[i+1]
			continue
		}
		req.Header.Add(fields[i], fields[i+1])
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return reply{resp.StatusCode, resp.Header, string(body)}
}

const cacheOnly = `[{"module": "cache", "params": {"zone": "test"}}]`

func TestHit(t *testing.T) {
	for proto, c := range clients() {
		t.Run(proto, func(t *testing.T) {
			base := serve(t, cacheOnly)
			first := get(t, c, "GET", base+"/fresh")
			if first.status != 200 || first.body != "/fresh 1" || first.cacheStatus() != "fit; fwd=uri-miss; stored" {
				t.Fatalf("first: %d %q %q", first.status, first.body, first.cacheStatus())
			}
			second := get(t, c, "GET", base+"/fresh")
			if second.body != "/fresh 1" || !strings.HasPrefix(second.cacheStatus(), "fit; hit; ttl=") ||
				second.header.Get("Age") == "" || second.header.Get("Etag") != `"v1"` || hitsOf("/fresh") != 1 {
				t.Fatalf("second: %q %q age %q, %d hits", second.body, second.cacheStatus(), second.header.Get("Age"), hitsOf("/fresh"))
			}
			head := get(t, c, "HEAD", base+"/fresh")
			if head.status != 200 || head.body != "" || !strings.HasPrefix(head.cacheStatus(), "fit; hit") ||
				head.header.Get("Content-Length") != "8" || hitsOf("/fresh") != 1 {
				t.Fatalf("HEAD: %d %q %q length %q", head.status, head.body, head.cacheStatus(), head.header.Get("Content-Length"))
			}
			other := get(t, c, "GET", base+"/fresh?x=1")
			if other.body != "/fresh 2" || other.cacheStatus() != "fit; fwd=uri-miss; stored" {
				t.Fatalf("another query: %q %q", other.body, other.cacheStatus())
			}
		})
	}
}

// TestStorable checks which responses are kept: a second request for a
// response that was not is answered by the origin again.
func TestStorable(t *testing.T) {
	base := serve(t, `[{"module": "cache", "params": {"zone": "test", "max_bytes": 1048576, "max_entry_bytes": 65536}}]`)
	c := clients()["http/1.1"]
	for _, tt := range []struct {
		path   string
		fields []string
		kept   bool
	}{
		{"/fresh", nil, true},
		{"/s-maxage", nil, true},
		{"/expires", nil, true},
		{"/heuristic", nil, true},
		{"/found", nil, true},
		{"/public", []string{"Authorization", "Bearer x"}, true},
		{"/plain", nil, false},       // no lifetime
		{"/expires-bad", nil, false}, // expired
		{"/no-store", nil, false},
		{"/private", nil, false},
		{"/no-cache", nil, false},
		{"/cookie", nil, false},
		{"/vary-star", nil, false},
		{"/error", nil, false},
		{"/found-plain", nil, false}, // a 302 needs a lifetime of its own
		{"/aged", nil, false},        // older than its lifetime
		{"/large", nil, false},       // past max_entry_bytes
		{"/must", []string{"Authorization", "Bearer x"}, true},
		{"/s-maxage?auth", []string{"Authorization", "Bearer x"}, true},
		{"/fresh?auth", []string{"Authorization", "Bearer x"}, false},
		{"/fresh?no-store", []string{"Cache-Control", "no-store"}, false},
	} {
		path, _, _ := strings.Cut(tt.path, "?")
		resetHits()
		first := get(t, c, "GET", base+tt.path, tt.fields...)
		second := get(t, c, "GET", base+tt.path, tt.fields...)
		if tt.kept && (first.body != second.body || second.status != first.status) {
			t.Errorf("%s: the second answer differs", tt.path)
		}
		if kept := hitsOf(path) == 1; kept != tt.kept {
			t.Errorf("%s %v: kept %v (%d hits; %q then %q)", tt.path, tt.fields, kept, hitsOf(path), first.cacheStatus(), second.cacheStatus())
		}
		if tt.path == "/large" && len(second.body) != len(long) {
			t.Errorf("/large: %d bytes", len(second.body))
		}
	}
}

func TestFreshness(t *testing.T) {
	start := time.Now()
	at(t, start, 0)
	base := serve(t, `[{"module": "cache", "params": {"zone": "test"}}]`)
	c := clients()["h2c"]
	get(t, c, "GET", base+"/fresh")
	get(t, c, "GET", base+"/heuristic") // fresh for 10s
	get(t, c, "GET", base+"/expires")
	at(t, start, 30*time.Second)
	// The Date it was given is up to a second old already.
	if r := get(t, c, "GET", base+"/fresh"); r.header.Get("Age") != "30" || r.cacheStatus() != "fit; hit; ttl=29" && r.cacheStatus() != "fit; hit; ttl=30" {
		t.Errorf("at 30s: age %q, %q", r.header.Get("Age"), r.cacheStatus())
	}
	// Stale: fetched again by the cache, with its validators, and answered
	// from what that keeps.
	if r := get(t, c, "GET", base+"/heuristic"); !strings.HasPrefix(r.cacheStatus(), "fit; hit") || hitsOf("/heuristic") != 2 {
		t.Errorf("heuristic at 30s: %q, %d fetches", r.cacheStatus(), hitsOf("/heuristic"))
	}
	if r := get(t, c, "GET", base+"/expires"); !strings.HasPrefix(r.cacheStatus(), "fit; hit") {
		t.Errorf("expires at 30s: %q", r.cacheStatus())
	}
	at(t, start, 61*time.Second)
	if r := get(t, c, "GET", base+"/fresh"); !freshHit(r) || r.body != "/fresh 2" {
		t.Errorf("at 61s: %q %q", r.cacheStatus(), r.body)
	}
	if r := get(t, c, "GET", base+"/fresh"); !strings.HasPrefix(r.cacheStatus(), "fit; hit") || r.body != "/fresh 2" {
		t.Errorf("then: %q %q", r.cacheStatus(), r.body)
	}
}

func TestDefaultTTL(t *testing.T) {
	base := serve(t, `[{"module": "cache", "params": {"zone": "test", "default_ttl": "1m"}}]`)
	c := clients()["http/1.1"]
	get(t, c, "GET", base+"/plain")
	if r := get(t, c, "GET", base+"/plain"); !strings.HasPrefix(r.cacheStatus(), "fit; hit") || hitsOf("/plain") != 1 {
		t.Errorf("default_ttl: %q", r.cacheStatus())
	}
	get(t, c, "GET", base+"/nothing") // 404 is heuristically cacheable
	if r := get(t, c, "GET", base+"/nothing"); r.status != 404 || !strings.HasPrefix(r.cacheStatus(), "fit; hit") {
		t.Errorf("404: %d %q", r.status, r.cacheStatus())
	}
	get(t, c, "GET", base+"/error")
	if get(t, c, "GET", base+"/error"); hitsOf("/error") != 2 {
		t.Error("a 500 was kept")
	}
}

func TestVary(t *testing.T) {
	base := serve(t, cacheOnly)
	for proto, c := range clients() {
		resetHits()
		url := base + "/vary?" + proto
		en := get(t, c, "GET", url, "Accept-Language", "en")
		fr := get(t, c, "GET", url, "Accept-Language", "fr")
		if fr.cacheStatus() != "fit; fwd=vary-miss; stored" {
			t.Errorf("%s fr: %q", proto, fr.cacheStatus())
		}
		en2 := get(t, c, "GET", url, "Accept-Language", "en")
		fr2 := get(t, c, "GET", url, "Accept-Language", "fr")
		if en2.body != en.body || fr2.body != fr.body || en.body == fr.body || !strings.HasPrefix(en2.cacheStatus(), "fit; hit") {
			t.Errorf("%s: %q %q, then %q %q", proto, en.body, fr.body, en2.body, fr2.body)
		}
		none := get(t, c, "GET", url)
		if none.cacheStatus() == "" || strings.HasPrefix(none.cacheStatus(), "fit; hit") {
			t.Errorf("%s: no Accept-Language took a variant: %q", proto, none.cacheStatus())
		}
	}
}

func TestConditional(t *testing.T) {
	base := serve(t, cacheOnly)
	c := clients()["http/1.1"]
	get(t, c, "GET", base+"/fresh")
	for _, fields := range [][]string{
		{"If-None-Match", `"v1"`},
		{"If-None-Match", `W/"v1"`},
		{"If-None-Match", `"x", "v1"`},
		{"If-None-Match", `*`},
		{"If-Modified-Since", "Mon, 02 Jan 2006 15:04:05 GMT"},
		{"If-Modified-Since", "Tue, 03 Jan 2006 15:04:05 GMT"},
	} {
		r := get(t, c, "GET", base+"/fresh", fields...)
		if r.status != 304 || r.body != "" || r.header.Get("Etag") != `"v1"` || r.header.Get("Cache-Control") != "max-age=60" ||
			r.header.Get("Content-Type") != "" || !strings.HasPrefix(r.cacheStatus(), "fit; hit") {
			t.Errorf("%v: %d %q %v", fields, r.status, r.body, r.header)
		}
	}
	for _, fields := range [][]string{
		{"If-None-Match", `"v2"`},
		{"If-Modified-Since", "Sun, 01 Jan 2006 15:04:05 GMT"},
		{"If-None-Match", `"v2"`, "If-Modified-Since", "Tue, 03 Jan 2006 15:04:05 GMT"},
	} {
		if r := get(t, c, "GET", base+"/fresh", fields...); r.status != 200 || r.body != "/fresh 1" {
			t.Errorf("%v: %d %q", fields, r.status, r.body)
		}
	}
	if hitsOf("/fresh") != 1 {
		t.Errorf("%d hits", hitsOf("/fresh"))
	}
}

// TestRevalidate checks that a stale response is fetched again by the
// cache with its own validators, and freshened by the origin's 304, for a
// client that sent none and for one that sent its own.
func TestRevalidate(t *testing.T) {
	start := time.Now()
	at(t, start, 0)
	base := serve(t, cacheOnly)
	c := clients()["h2c"]
	get(t, c, "GET", base+"/revalidate")
	at(t, start, 90*time.Second)
	r := get(t, c, "GET", base+"/revalidate")
	if r.status != 200 || r.body != "/revalidate 1" || !strings.HasPrefix(r.cacheStatus(), "fit; hit") ||
		r.header.Get("Cache-Control") != "max-age=120" || hitsOf("/revalidate") != 2 {
		t.Fatalf("revalidated: %d %q %q %q, %d fetches", r.status, r.body, r.cacheStatus(), r.header.Get("Cache-Control"), hitsOf("/revalidate"))
	}
	at(t, start, 300*time.Second)
	r = get(t, c, "GET", base+"/revalidate", "If-None-Match", `"r1"`)
	if r.status != 304 || !strings.HasPrefix(r.cacheStatus(), "fit; hit") || hitsOf("/revalidate") != 3 {
		t.Errorf("with the client's validators: %d %q, %d fetches", r.status, r.cacheStatus(), hitsOf("/revalidate"))
	}
	if r := get(t, c, "GET", base+"/revalidate"); r.status != 200 || !strings.HasPrefix(r.cacheStatus(), "fit; hit") || hitsOf("/revalidate") != 3 {
		t.Errorf("then: %d %q", r.status, r.cacheStatus())
	}
}

func TestRequestDirectives(t *testing.T) {
	start := time.Now()
	at(t, start, 0)
	base := serve(t, cacheOnly)
	c := clients()["http/1.1"]
	if r := get(t, c, "GET", base+"/fresh", "Cache-Control", "only-if-cached"); r.status != 504 {
		t.Errorf("only-if-cached, nothing kept: %d", r.status)
	}
	get(t, c, "GET", base+"/fresh")
	get(t, c, "GET", base+"/must")
	at(t, start, 20*time.Second)
	for _, tt := range []struct {
		path   string
		fields []string
		want   string
	}{
		{"/fresh", []string{"Cache-Control", "only-if-cached"}, "fit; hit"},
		{"/fresh", []string{"Cache-Control", "max-age=30"}, "fit; hit"},
		{"/fresh", []string{"Cache-Control", "min-fresh=30"}, "fit; hit"},
		{"/fresh", []string{"Cache-Control", "max-age=10"}, "fit; fwd=request; stored"},
		// From here on, what is kept is the response stored just now.
		{"/fresh", []string{"Cache-Control", "min-fresh=61"}, "fit; fwd=request; stored"},
		{"/fresh", []string{"Cache-Control", "no-cache"}, "fit; fwd=request; stored"},
		{"/fresh", []string{"Pragma", "no-cache"}, "fit; fwd=request; stored"},
		{"/fresh", []string{"Pragma", "no-cache", "Cache-Control", "max-age=60"}, "fit; hit"},
		{"/fresh", []string{"Range", "bytes=0-1"}, "fit; fwd=bypass; stored"},
	} {
		if r := get(t, c, "GET", base+tt.path, tt.fields...); !strings.HasPrefix(r.cacheStatus(), tt.want) {
			t.Errorf("%v: %q, want %q", tt.fields, r.cacheStatus(), tt.want)
		}
	}
	at(t, start, 100*time.Second) // /fresh was stored again at 20s
	for _, tt := range []struct {
		path   string
		fields []string
		want   string
	}{
		{"/fresh", []string{"Cache-Control", "max-stale"}, "fit; hit; ttl=-2"},
		{"/fresh", []string{"Cache-Control", "max-stale=30"}, "fit; hit"},
		// Fetched again by the cache, and answered from what that keeps.
		{"/fresh", []string{"Cache-Control", "max-stale=10"}, "fit; hit; ttl=59"},
		{"/must", []string{"Cache-Control", "max-stale"}, "fit; fwd=stale; stored"},
	} {
		if r := get(t, c, "GET", base+tt.path, tt.fields...); !strings.HasPrefix(r.cacheStatus(), tt.want) {
			t.Errorf("%s %v: %q, want %q", tt.path, tt.fields, r.cacheStatus(), tt.want)
		}
	}
}

func TestInvalidate(t *testing.T) {
	answer.Store(new(func(c *fibhttp.Context) {
		_ = c.WriteResponse(fibhttp.Response{StatusCode: 201, Header: stdhttp.Header{
			"Location": {"/fresh"}, "Content-Location": {"http://elsewhere/s-maxage"}}})
	}))
	base := serve(t, cacheOnly)
	c := clients()["http/1.1"]
	for _, path := range []string{"/fresh", "/s-maxage", "/expires"} {
		get(t, c, "GET", base+path)
	}
	get(t, c, "POST", base+"/expires")
	get(t, c, "POST", base+"/custom")
	if r := get(t, c, "GET", base+"/expires"); r.cacheStatus() != "fit; fwd=uri-miss; stored" {
		t.Errorf("the POSTed URI: %q", r.cacheStatus())
	}
	if r := get(t, c, "GET", base+"/fresh"); r.cacheStatus() != "fit; fwd=uri-miss; stored" {
		t.Errorf("the Location: %q", r.cacheStatus())
	}
	if r := get(t, c, "GET", base+"/s-maxage"); !strings.HasPrefix(r.cacheStatus(), "fit; hit") {
		t.Errorf("another host's Content-Location: %q", r.cacheStatus())
	}
}

// TestBodies checks a body written in parts, kept once it has all gone, one
// cut short, and one past max_entry_bytes, passed through whole.
func TestBodies(t *testing.T) {
	base := serve(t, `[{"module": "cache", "params": {"zone": "test", "max_entry_bytes": 131072}}]`)
	for proto, c := range clients() {
		resetHits()
		r := get(t, c, "GET", base+"/stream")
		if r.body != long[:64<<10] {
			t.Fatalf("%s: %d bytes", proto, len(r.body))
		}
		r = get(t, c, "GET", base+"/stream")
		if r.body != long[:64<<10] || !strings.HasPrefix(r.cacheStatus(), "fit; hit") || r.header.Get("Content-Length") != "65536" {
			t.Errorf("%s: %d bytes, %q", proto, len(r.body), r.cacheStatus())
		}
		for range 2 {
			req, _ := stdhttp.NewRequest("GET", base+"/short", nil)
			if resp, err := c.Do(req); err == nil {
				_, _ = io.ReadAll(resp.Body)
				resp.Body.Close()
			}
		}
		if hitsOf("/short") != 2 {
			t.Errorf("%s: a body short of its length was kept", proto)
		}
		for range 2 {
			if r := get(t, c, "GET", base+"/large"); len(r.body) != len(long) {
				t.Errorf("%s: /large: %d bytes", proto, len(r.body))
			}
		}
		if hitsOf("/large") != 2 {
			t.Errorf("%s: a body past max_entry_bytes was kept", proto)
		}
	}
}

// letGo lets the slow origin go on.
func letGo(t *testing.T) {
	select {
	case release <- struct{}{}:
	case <-time.After(5 * time.Second):
		t.Error("the origin did not take its release")
	}
}

// TestClientGone checks that a response whose client goes before it ends
// is not kept, and gives back what it held.
func TestClientGone(t *testing.T) {
	base := serve(t, cacheOnly)
	var s *store
	zones.Lock()
	s = zones.m["test"]
	zones.Unlock()
	for proto, c := range clients() {
		resetHits()
		gone.Store(0)
		ctx, cancel := context.WithCancel(context.Background())
		url := base + "/slow?" + proto
		req, _ := stdhttp.NewRequestWithContext(ctx, "GET", url, nil)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 512)
		if _, err := io.ReadFull(resp.Body, buf); err != nil {
			t.Fatal(err)
		}
		if s.capturing.Load() == 0 {
			t.Errorf("%s: nothing held while the body goes", proto)
		}
		cancel()
		resp.Body.Close()
		// The origin goes on only once the server has heard the client go:
		// the client says so on its own time, and a rest of the body that
		// went before it does is the whole response, which is kept.
		deadline := time.Now().Add(5 * time.Second)
		for gone.Load() == 0 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if gone.Load() == 0 {
			t.Fatalf("%s: the server did not hear the client go", proto)
		}
		letGo(t)
		deadline = time.Now().Add(5 * time.Second)
		for s.capturing.Load() != 0 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if n := s.capturing.Load(); n != 0 {
			t.Errorf("%s: %d bytes still held", proto, n)
		}
		go letGo(t)
		if r := get(t, c, "GET", url); r.cacheStatus() != "fit; fwd=uri-miss; stored" || len(r.body) != 64<<10 {
			t.Errorf("%s: after the client went: %q, %d bytes", proto, r.cacheStatus(), len(r.body))
		}
	}
}

// TestCompressed checks the cache in front of compress: each encoding is a
// variant of its own.
func TestCompressed(t *testing.T) {
	base := serve(t, `[{"module": "cache", "params": {"zone": "test"}}, {"module": "compress"}]`)
	c := clients()["h2c"]
	gz := get(t, c, "GET", base+"/gzip", "Accept-Encoding", "gzip")
	plain := get(t, c, "GET", base+"/gzip")
	gz2 := get(t, c, "GET", base+"/gzip", "Accept-Encoding", "gzip")
	plain2 := get(t, c, "GET", base+"/gzip")
	if gz.header.Get("Content-Encoding") != "gzip" || plain.header.Get("Content-Encoding") != "" ||
		gz2.body != gz.body || plain2.body != plain.body || hitsOf("/gzip") != 2 ||
		!strings.HasPrefix(gz2.cacheStatus(), "fit; hit") || !strings.HasPrefix(plain2.cacheStatus(), "fit; hit") {
		t.Errorf("%q %q, %d hits, %q %q", gz.header.Get("Content-Encoding"), plain.header.Get("Content-Encoding"),
			hitsOf("/gzip"), gz2.cacheStatus(), plain2.cacheStatus())
	}
}

func TestEviction(t *testing.T) {
	s := newStore("evict", 16*4096)
	body := make([]byte, 1024)
	for i := range 1000 {
		s.put(fmt.Sprintf("/k%d", i), &entry{status: 200, header: stdhttp.Header{}, body: body})
		if i == 0 {
			continue
		}
		// /k0 is used each time, so it stays.
		if e, _ := s.get("/k0", nil); e == nil {
			t.Fatalf("/k0 was evicted after %d", i)
		}
	}
	keys, size := s.stats()
	if size > 16*4096 || keys < 16 || keys > 16*4 {
		t.Errorf("%d keys, %d bytes", keys, size)
	}
	s.remove("/k0")
	if e, ok := s.get("/k0", nil); e != nil || ok {
		t.Error("/k0 was not removed")
	}
	if s.put("/big", &entry{status: 200, body: make([]byte, 8192)}); func() bool { e, _ := s.get("/big", nil); return e != nil }() {
		t.Error("an entry larger than a shard was kept")
	}
}

func TestVariantsBounded(t *testing.T) {
	s := newStore("variants", 1<<20)
	for i := range 20 {
		s.put("/v", &entry{status: 200, varyNames: []string{"X"}, varyValues: []string{fmt.Sprint(i)}})
	}
	sh := s.shard("/v")
	if n := len(sh.items["/v"].variants); n != maxVariants {
		t.Errorf("%d variants", n)
	}
	if e, _ := s.get("/v", stdhttp.Header{"X": {"19"}}); e == nil {
		t.Error("the newest variant went")
	}
	if e, _ := s.get("/v", stdhttp.Header{"X": {"0"}}); e != nil {
		t.Error("the oldest variant stayed")
	}
}

// TestZones checks that a zone outlives a reload: the new config's cache
// takes it before the old one's gives it back.
func TestZones(t *testing.T) {
	load := func() *Cache {
		m := &Cache{Zone: "reload"}
		if err := m.Provision(&module.Context{}); err != nil {
			t.Fatal(err)
		}
		return m
	}
	old := load()
	old.store.put("/k", &entry{status: 200})
	next := load()
	old.Cleanup()
	if e, _ := next.store.get("/k", nil); e == nil {
		t.Error("the zone did not outlive the reload")
	}
	next.Cleanup()
	if again := load(); again.store == next.store {
		t.Error("a zone no cache uses was kept")
	} else {
		again.Cleanup()
	}
}

func TestParams(t *testing.T) {
	for _, tt := range []struct{ params, err string }{
		{`{"max_bytes": 1048576, "max_entry_bytes": 1048576}`, "more than a sixteenth"},
		{`{"max_bytes": -1}`, "less than the minimum 0"},
	} {
		v, err := config.ParseValue([]byte(`{"module": "cache", "params": `+tt.params+`}`), "middleware")
		if err != nil {
			t.Fatal(err)
		}
		var ref config.ModuleRef
		err = v.Decode(&ref)
		if err == nil {
			_, err = module.Load(module.Middlewares, ref, nil)
		}
		if err == nil || !strings.Contains(err.Error(), tt.err) {
			t.Errorf("%s: %v, want %q", tt.params, err, tt.err)
		}
	}
	m := &Cache{MaxBytes: 1 << 20}
	if err := m.Provision(&module.Context{}); err != nil || m.MaxEntryBytes != 1<<16 {
		t.Errorf("max_entry_bytes for 1 MiB: %d %v", m.MaxEntryBytes, err)
	}
	m.Cleanup()
}

// BenchmarkHit answers a 4 KB response from the cache.
func BenchmarkHit(b *testing.B) {
	m := &Cache{Zone: "bench"}
	if err := m.Provision(&module.Context{}); err != nil {
		b.Fatal(err)
	}
	defer m.Cleanup()
	body := []byte(strings.Repeat("x", 4096))
	h := m.Wrap(fibhttp.HandlerFunc(func(c *fibhttp.Context) {
		_ = c.WriteResponse(fibhttp.Response{StatusCode: 200, Header: stdhttp.Header{
			"Content-Type": {"text/plain"}, "Cache-Control": {"max-age=3600"}, "Etag": {`"b"`}}, Body: body})
	}))
	r := httptest.NewRequest("GET", "http://example.com/bench?x=1", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	if rec := fibtest.ServeRequest(h, r); rec.Response.StatusCode != 200 {
		b.Fatal(rec.Response.StatusCode)
	}
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		fibtest.ServeRequest(h, r)
	}
}
