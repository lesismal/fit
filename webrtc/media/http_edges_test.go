package media

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/observe"
	"github.com/lesismal/fit/runtime"
	"github.com/lesismal/fit/upstream"
)

// chunked sends body with no Content-Length, as a client streaming its
// offer does.
func chunked(t *testing.T, url, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", url, struct{ io.Reader }{strings.NewReader(body)})
	req.Header.Set("Content-Type", "application/sdp")
	resp, _ := do(t, req)
	return resp
}

// cutOffer sends the start of a chunked offer to addr's path, then closes
// the connection.
func cutOffer(t *testing.T, base, path string) {
	t.Helper()
	c, err := net.Dial("tcp", strings.TrimPrefix(base, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(c, "POST "+path+" HTTP/1.1\r\nHost: x\r\nContent-Type: application/sdp\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nv=0\r\n\r\n")
	// The server reads the chunk before the connection goes.
	_ = c.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	_, _ = bufio.NewReader(c).ReadByte()
	_ = c.Close()
}

// The whip handler answers OPTIONS of a session's URL, refuses an offer
// past max_sdp_bytes whether or not it says its length, and forgets an
// offer whose client left before its end; a stream name that is not one
// fails the config.
func TestHandlerEdges(t *testing.T) {
	f := startFit(t, doc(`{"listen": "127.0.0.1:0", "max_sdp_bytes": 1024}`))
	a, e := f.media().Publish("s", offerOf(t, newPublisher(t, startEngine(t)).peer))
	if e != nil {
		t.Fatal(e)
	}
	req, _ := http.NewRequest("OPTIONS", f.base+"/whip/s/"+a.ID, nil)
	if resp, _ := do(t, req); resp.StatusCode != http.StatusNoContent || !strings.Contains(resp.Header.Get("Allow"), "DELETE") {
		t.Fatalf("OPTIONS of a session: %d %v", resp.StatusCode, resp.Header)
	}
	big := "v=0\r\n" + strings.Repeat("a=x\r\n", 400)
	if resp, _ := post(t, f.base+"/whip/t", "application/sdp", big); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("a large offer: %d", resp.StatusCode)
	}
	if resp := chunked(t, f.base+"/whip/t", big); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("a large chunked offer: %d", resp.StatusCode)
	}
	cutOffer(t, f.base, "/whip/t")
	if resp := chunked(t, f.base+"/whip/t", "not sdp"); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a chunked offer after a cut one: %d", resp.StatusCode)
	}

	if err := compileErr(t, `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
		"routes": [{"match": {"path": ["/whip/*"]}, "handler": {"module": "whip", "params": {"stream": "a/b"}}}]}`); err == nil ||
		!strings.Contains(err.Error(), "stream") {
		t.Fatalf("a bad stream name: %v", err)
	}
}

// compileErr is the error compiling doc gives, or nil.
func compileErr(t *testing.T, doc string) error {
	t.Helper()
	c, err := config.Load([]byte(doc))
	if err != nil {
		return err
	}
	up, stop, err := upstream.StartEngine()
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	g, err := runtime.Compile(c, runtime.Env{Metrics: observe.NewMetrics(), Engine: up})
	if err == nil {
		rt := new(runtime.Runtime)
		rt.Swap(g)
		rt.Close()
	}
	return err
}

// The webrtc_proxy handler: an endpoint takes POST and passes OPTIONS on;
// an ID no session has is 404; a GET below a session's URL is passed on,
// its Links rewritten; an answer with no Location, one that is not a URL,
// and one cut short are 502; a chunked offer past max_sdp_bytes is 413 and
// one cut short is forgotten; a restart that names another session's
// ufrag is 502; and once the session's media server leaves the upstream,
// its URL answers 502, and its DELETE ends it at fit.
func TestProxyHandlerEdges(t *testing.T) {
	fs := startFake(t)
	other := startFake(t)
	f := startFit(t, proxyDoc(loopbackProxy, &fit{base: fs.URL}))

	if code := request(t, "GET", f.base+"/whip/cam"); code != http.StatusMethodNotAllowed {
		t.Fatalf("GET of the endpoint: %d", code)
	}
	if code := request(t, "GET", f.base+"/whip/cam/"+strings.Repeat("ab", 16)); code != http.StatusNotFound {
		t.Fatalf("GET of no session: %d", code)
	}
	req, _ := http.NewRequest("OPTIONS", f.base+"/whip/cam", nil)
	if resp, _ := do(t, req); resp.StatusCode != http.StatusNoContent || resp.Header.Get("Accept-Post") != "application/sdp" {
		t.Fatalf("OPTIONS: %d %v", resp.StatusCode, resp.Header)
	}
	for _, path := range []string{"/whip/noloc", "/whip/badloc", "/whip/cut"} {
		if resp, body := post(t, f.base+path, "application/sdp", "v=0\r\n"); resp.StatusCode != http.StatusBadGateway {
			t.Fatalf("%s: %d %s", path, resp.StatusCode, body)
		}
	}
	if resp := chunked(t, f.base+"/whip/cam?u=big", strings.Repeat("a=x\r\n", 10000)); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("a large chunked offer: %d", resp.StatusCode)
	}
	cutOffer(t, f.base, "/whip/cam?u=cut")

	resp, body := post(t, f.base+"/whip/cam?u=a", "application/sdp", "v=0\r\n")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	locA := resp.Header.Get("Location")
	resp, body = do(t, mustReq(t, "GET", f.base+locA+"/sse"))
	if resp.StatusCode != http.StatusOK || body != "got /res/a/sse" || resp.Header.Get("Link") != "<"+locA+"/sse/more>; rel=\"more\"" {
		t.Fatalf("GET below the session: %d %q %v", resp.StatusCode, body, resp.Header)
	}
	resp, _ = post(t, f.base+"/whip/cam?u=b", "application/sdp", "v=0\r\n")
	locB := resp.Header.Get("Location")
	restart := func(loc string) int {
		req := mustReq(t, "PATCH", f.base+loc)
		req.Body = io.NopCloser(strings.NewReader("a=ice-ufrag:n\r\na=ice-pwd:nnnnnnnnnnnnnnnnnnnnnnnn\r\n"))
		r, _ := do(t, req)
		return r.StatusCode
	}
	if code := restart(locA); code != http.StatusOK {
		t.Fatalf("a restart: %d", code)
	}
	if code := restart(locB); code != http.StatusBadGateway {
		t.Fatalf("a restart to another session's ufrag: %d", code)
	}

	f.reload(t, proxyDoc(loopbackProxy, &fit{base: other.URL}))
	if code := request(t, "GET", f.base+locA); code != http.StatusBadGateway {
		t.Fatalf("GET of a session whose media server left: %d", code)
	}
	if code := request(t, "DELETE", f.base+locA); code != http.StatusBadGateway {
		t.Fatalf("DELETE of a session whose media server left: %d", code)
	}
	if f.proxy().Session(locA[strings.LastIndexByte(locA, '/')+1:]) != nil {
		t.Fatal("the session outlived its DELETE")
	}
	if fs.deletes.Load() != 0 || other.deletes.Load() != 0 {
		t.Fatal("a DELETE reached a media server")
	}
}

func mustReq(t *testing.T, method, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

// webrtc_proxy's params: an upstream that is not, and a media address that
// is neither an IP nor an IP and a port, fail the config; an IP alone is
// one.
func TestProxyHandlerParams(t *testing.T) {
	route := func(params string) string {
		return `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "upstreams": [{"name": "media", "addresses": ["127.0.0.1:1"]}],
  "routes": [{"match": {"path": ["/whip/*"]}, "handler": {"module": "webrtc_proxy", "params": ` + params + `}}]}`
	}
	if err := compileErr(t, route(`{"upstream": "nope"}`)); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("an upstream that is not: %v", err)
	}
	if err := compileErr(t, route(`{"upstream": "media", "media_addrs": {"127.0.0.1:1": "0.0.0.0"}}`)); err == nil ||
		!strings.Contains(err.Error(), "media_addrs") {
		t.Fatalf("an unspecified media address: %v", err)
	}
	if err := compileErr(t, route(`{"upstream": "media", "media_addrs": {"127.0.0.1:1": "10.0.0.2"}}`)); err != nil {
		t.Fatal(err)
	}
}

// Requests and answers past the listener's stream threshold, under a
// max_sdp_bytes above it, come in pieces: an offer is passed on whole, to
// an endpoint deeper than a session's URL could be; one cut short reaches
// no media server and gives its reservation back; a chunked one past the
// limit is 413; an answer cut short is 502; and a request below a session
// whose media server is gone is 502.
func TestProxyStreamedBodies(t *testing.T) {
	fs := startFake(t)
	doc := strings.Replace(proxyDoc(loopbackProxy, &fit{base: fs.URL}), `"timeout": "1s"`, `"timeout": "1s", "max_sdp_bytes": 262144`, 1)
	f := startFit(t, doc)
	offer := "v=0\r\n" + strings.Repeat("a=x\r\n", 20000) // 100 KB

	resp, body := post(t, f.base+"/whip/a/b/c?u=deep", "application/sdp", offer)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("a streamed offer: %d %s", resp.StatusCode, body)
	}
	if got, _ := fs.posted.Load().(string); got != "/whip/a/b/c?u=deep" {
		t.Fatalf("the deep endpoint reached the media server as %q", got)
	}
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, "/whip/a/b/c/") {
		t.Fatalf("Location %q", loc)
	}

	// Cut after the first pieces: nothing is posted.
	fs.posted.Store("")
	c, err := net.Dial("tcp", strings.TrimPrefix(f.base, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(c, "POST /whip/cam?u=cut HTTP/1.1\r\nHost: x\r\nContent-Type: application/sdp\r\nContent-Length: 200000\r\n\r\n"+offer)
	_ = c.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	_, _ = bufio.NewReader(c).ReadByte()
	_ = c.Close()
	eventually(t, "the cut offer's reservation given back", func() bool {
		p := f.proxy()
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.reserved == 1 // the deep session's
	})
	if got, _ := fs.posted.Load().(string); got != "" {
		t.Fatalf("a cut offer reached the media server as %q", got)
	}

	if resp := chunked(t, f.base+"/whip/cam?u=big", strings.Repeat(offer, 3)); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("a large chunked offer: %d", resp.StatusCode)
	}
	if resp, body := post(t, f.base+"/whip/cutlong", "application/sdp", "v=0\r\n"); resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("an answer cut short: %d %s", resp.StatusCode, body)
	}

	fs.Close()
	if code := request(t, "GET", f.base+loc+"/sse"); code != http.StatusBadGateway {
		t.Fatalf("GET below a session whose media server is gone: %d", code)
	}
}
