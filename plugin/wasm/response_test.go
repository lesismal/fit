package wasm

import (
	"errors"
	"io"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/internal/fibtest"
)

// holds counts the holds taken on a config, which a followed response keeps
// until its context closes.
type holds struct{ n atomic.Int64 }

func (h *holds) Retain()  { h.n.Add(1) }
func (h *holds) Release() { h.n.Add(-1) }

// counted puts a counter in place of m's hold on its config, and checks,
// as the test ends, that every hold was given back: every context closed.
func counted(t *testing.T, m *WASM) *holds {
	t.Helper()
	h := &holds{}
	m.graph = h
	t.Cleanup(func() {
		deadline := time.Now().Add(10 * time.Second)
		for h.n.Load() != 0 {
			if time.Now().After(deadline) {
				t.Errorf("%d holds left: contexts not closed", h.n.Load())
				return
			}
			time.Sleep(time.Millisecond)
		}
	})
	return h
}

// streamed answers in three writes, with the trailer x-upstream, as a proxy
// passing on an upstream's response does.
var streamed = fibhttp.HandlerFunc(func(c *fibhttp.Context) {
	c.Header().Set("Trailer", "X-Upstream")
	c.Header().Set("Content-Type", "text/plain")
	c.WriteHeader(201)
	for _, part := range []string{"one ", "two ", "three"} {
		_, _ = io.WriteString(c, part)
		c.Flush()
	}
	c.Header().Set("X-Upstream", "up")
})

// echoBody answers with the request's test field and its body.
var echoBody = fibhttp.HandlerFunc(func(c *fibhttp.Context) {
	_ = c.Respond(200, "text/plain", []byte("[initial body]"+c.Request.Header.Get("Test")))
})

// TestProxyWasmResponseHeaders runs the SDK's http_headers example, which
// adds response fields in proxy_on_response_headers, one of them from its
// configuration, and changes a request field.
func TestProxyWasmResponseHeaders(t *testing.T) {
	m := loadOne(t, "http_headers", map[string]any{"config": `{"header": "x-config", "value": "from config"}`})
	counted(t, m)
	h := m.Wrap(echoBody)
	for range 3 {
		resp := fibtest.ServeRequest(h, httptest.NewRequest("GET", "http://example.com/x", nil)).Response
		if resp.StatusCode != 200 || string(resp.Body) != "[initial body]best" ||
			resp.Header.Get("X-Proxy-Wasm-Go-Sdk-Example") != "http_headers" || resp.Header.Get("X-Config") != "from config" {
			t.Fatalf("%d %q %v", resp.StatusCode, resp.Body, resp.Header)
		}
	}
	// Without the response's callbacks the fields are not added.
	h = loadOne(t, "http_headers", map[string]any{"config": `{"header": "x-config", "value": "v"}`, "response": "none"}).Wrap(echoBody)
	if resp := fibtest.ServeRequest(h, httptest.NewRequest("GET", "http://example.com/x", nil)).Response; resp.Header.Get("X-Config") != "" {
		t.Errorf("response none: %v", resp.Header)
	}
}

// TestProxyWasmResponseBody runs the SDK's http_body example, which holds
// the response's body until its end, then appends, prepends or replaces.
func TestProxyWasmResponseBody(t *testing.T) {
	m := loadOne(t, "http_body", map[string]any{})
	counted(t, m)
	h := m.Wrap(echoBody)
	for op, want := range map[string]string{
		"append":  "[initial body]best[this is appended body]",
		"prepend": "[this is prepended body][initial body]best",
		"replace": "[this is replaced body]",
	} {
		r := httptest.NewRequest("PUT", "http://example.com/x", strings.NewReader("x"))
		r.Header.Set("Content-Length", "1")
		r.Header.Set("Test", "best")
		r.Header.Set("Buffer-Operation", op)
		r.Header.Set("Buffer-Replace-At", "response")
		resp := fibtest.ServeRequest(h, r).Response
		if resp.StatusCode != 200 || string(resp.Body) != want || resp.Header.Get("Content-Length") != "" {
			t.Errorf("%s: %d %q %v", op, resp.StatusCode, resp.Body, resp.Header)
		}
	}
	// With the header alone the body goes by untouched.
	h = loadOne(t, "http_body", map[string]any{"response": "headers"}).Wrap(echoBody)
	r := httptest.NewRequest("PUT", "http://example.com/x", strings.NewReader("x"))
	r.Header.Set("Content-Length", "1")
	r.Header.Set("Buffer-Replace-At", "response")
	if resp := fibtest.ServeRequest(h, r).Response; string(resp.Body) != "[initial body]" {
		t.Errorf("response headers: %q", resp.Body)
	}
}

// TestProxyWasmResponseTrailers runs fit's own filter on a response written
// in parts with a trailer: each part is upper-cased, the status is read as
// :status, and the trailer changed.
func TestProxyWasmResponseTrailers(t *testing.T) {
	m := loadOne(t, "response", map[string]any{"config": "upper"})
	counted(t, m)
	h := m.Wrap(streamed)
	for range 3 {
		resp := fibtest.ServeRequest(h, httptest.NewRequest("GET", "http://example.com/x", nil)).Response
		if resp.StatusCode != 201 || string(resp.Body) != "ONE TWO THREE" || resp.Header.Get("X-Response-Code") != "201" {
			t.Fatalf("%d %q %v", resp.StatusCode, resp.Body, resp.Header)
		}
		if tr := resp.Trailer; tr.Get("X-Body-Bytes") != "13" || tr.Get("X-Upstream-Seen") != "up" || tr.Get("X-Upstream") != "" {
			t.Fatalf("trailer %v", tr)
		}
	}
	// A response with no trailer gets the one the plugin adds at its end.
	resp := fibtest.ServeRequest(m.Wrap(echoBody), httptest.NewRequest("GET", "http://example.com/x", nil)).Response
	if string(resp.Body) != "[INITIAL BODY]" || resp.Trailer.Get("X-Body-Bytes") != "14" {
		t.Errorf("%q %v", resp.Body, resp.Trailer)
	}
	// Nor is the body called for a response that has none.
	resp = fibtest.ServeRequest(m.Wrap(fibhttp.HandlerFunc(func(c *fibhttp.Context) { c.WriteHeader(204) })),
		httptest.NewRequest("GET", "http://example.com/x", nil)).Response
	if resp.StatusCode != 204 || resp.Header.Get("X-Response-Code") != "204" || resp.Trailer != nil {
		t.Errorf("204: %v %v", resp.Header, resp.Trailer)
	}
}

// TestProxyWasmResponseFails checks what a response whose callback cannot
// go on does: fails, or goes on without the plugin when on_error is skip.
// A plugin that answers once the response has begun, one that runs past
// its timeout, and a body held past max_body_bytes each fail.
func TestProxyWasmResponseFails(t *testing.T) {
	// A failed response is never written: no need to wait long for it.
	defer func(d time.Duration) { fibtest.Timeout = d }(fibtest.Timeout)
	fibtest.Timeout = time.Second
	long := fibhttp.HandlerFunc(func(c *fibhttp.Context) {
		_ = c.Respond(200, "text/plain", []byte(strings.Repeat("a", 100)))
	})
	for _, tt := range []struct {
		name, filter string
		params       map[string]any
		h            fibhttp.Handler
		skipped      string
	}{
		{"answers", "response", map[string]any{"config": "answer"}, echoBody, "[initial body]"},
		{"too slow", "response", map[string]any{"config": "slow:400", "timeout": "100ms"}, echoBody, "[initial body]"},
		{"holds too much", "http_body", map[string]any{"max_body_bytes": 50}, long, strings.Repeat("a", 100)},
	} {
		for _, onError := range []string{"fail", "skip"} {
			tt.params["on_error"] = onError
			m := loadOne(t, tt.filter, tt.params)
			counted(t, m)
			h := m.Wrap(tt.h)
			for range 2 { // and again, on an instance made again
				r := httptest.NewRequest("PUT", "http://example.com/x", strings.NewReader("x"))
				r.Header.Set("Content-Length", "1")
				r.Header.Set("Buffer-Replace-At", "response")
				rec := fibtest.ServeRequest(h, r)
				switch {
				case onError == "fail" && rec.Written:
					t.Errorf("%s, fail: answered %d %q", tt.name, rec.Response.StatusCode, rec.Response.Body)
				case onError == "skip" && (!rec.Written || string(rec.Response.Body) != tt.skipped):
					t.Errorf("%s, skip: %v %d %q", tt.name, rec.Written, rec.Response.StatusCode, rec.Response.Body)
				}
			}
		}
	}
}

// TestProxyWasmMaxStreams checks that an instance follows no more responses
// than max_streams at once: past it a request is answered 503, and once one
// ends there is room again.
func TestProxyWasmMaxStreams(t *testing.T) {
	m := loadOne(t, "response", map[string]any{"config": "upper", "instances": 1, "max_streams": 1})
	counted(t, m)
	release := make(chan struct{})
	held := fibhttp.HandlerFunc(func(c *fibhttp.Context) {
		if c.Request.URL.Path != "/held" {
			_ = c.Respond(200, "text/plain", []byte("ok"))
			return
		}
		c.Retain()
		go func() {
			<-release
			_ = c.Respond(200, "text/plain", []byte("held"))
			c.Release()
		}()
	})
	h := m.Wrap(held)
	first := &fibtest.Recorder{}
	fibhttp.Serve(h, fibhttp.NewStreamContext(nil, httptest.NewRequest("GET", "http://example.com/held", nil), first))
	// The first is under way once the second finds no room.
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp := fibtest.ServeRequest(h, httptest.NewRequest("GET", "http://example.com/", nil)).Response
		if resp.StatusCode == 503 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no 503 while one is followed: %d", resp.StatusCode)
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	if !first.Wait(10*time.Second) || string(first.Response.Body) != "HELD" {
		t.Fatalf("the first: %v %q", first.Written, first.Response.Body)
	}
	if resp := fibtest.ServeRequest(h, httptest.NewRequest("GET", "http://example.com/", nil)).Response; string(resp.Body) != "OK" {
		t.Errorf("once it ended: %d %q", resp.StatusCode, resp.Body)
	}
}

// TestProxyWasmResponseClientGone checks that a request whose client goes
// while its response is held has its context closed, and holds nothing.
func TestProxyWasmResponseClientGone(t *testing.T) {
	m := loadOne(t, "response", map[string]any{"config": "upper"})
	counted(t, m)
	started := make(chan *fibhttp.Context, 1)
	h := m.Wrap(fibhttp.HandlerFunc(func(c *fibhttp.Context) {
		c.Retain()
		started <- c
	}))
	sr := &fibhttp.StreamRequest{Request: *httptest.NewRequest("GET", "http://example.com/", nil)}
	rec := &fibtest.Recorder{}
	fibhttp.Serve(h, sr.Context(nil, rec, nil))
	c := <-started
	sr.Cancel(errors.New("gone"))
	_ = c.Respond(200, "text/plain", []byte("late"))
	c.Release()
}

// TestBodyBuffer checks proxy_set_buffer_bytes's arithmetic, and its bound.
func TestBodyBuffer(t *testing.T) {
	cl := &call{m: &WASM{MaxBodyBytes: 8}}
	for _, tt := range []struct {
		start, size uint32
		data, want  string
	}{
		{0, 0, "ab", "ab"},
		{1 << 31, 0, "cd", "abcd"},
		{0, 0, "<", "<abcd"},
		{1, 2, "x", "<xcd"},
		{0, 1 << 31, "new", "new"},
	} {
		if s := cl.setBody(tt.start, tt.size, []byte(tt.data)); s != pwOK || string(cl.buf) != tt.want {
			t.Fatalf("%d %d %q: %d %q, want %q", tt.start, tt.size, tt.data, s, cl.buf, tt.want)
		}
	}
	if s := cl.setBody(3, 0, []byte(strconv.Itoa(123456))); s != pwBadArgument || string(cl.buf) != "new" {
		t.Errorf("past the bound: %d %q", s, cl.buf)
	}
}

// loadOne is loadFilter with one instance, unless params says otherwise:
// each runs the Go runtime the filter is built with, which the race
// detector makes slow to start.
func loadOne(t *testing.T, name string, params map[string]any) *WASM {
	t.Helper()
	if _, ok := params["instances"]; !ok {
		params["instances"] = 1
	}
	return loadFilter(t, name, params)
}
