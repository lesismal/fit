package admin

import (
	"net/http"
	"strings"
	"testing"

	"github.com/lesismal/fit/config"
	_ "github.com/lesismal/fit/middleware/cache"
)

// TestCachePurge purges a response a cache zone keeps through the Admin
// API, by URL and by tag, and reads what the zones keep.
func TestCachePurge(t *testing.T) {
	f := start(t, `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "routes": [{"middleware": [{"module": "cache", "params": {"zone": "admin"}}],
    "handler": {"module": "respond", "params": {"body": "x", "headers": {"Cache-Control": "max-age=60", "Cache-Tag": "t"}}}}]}`, &config.Admin{})
	site := "http://" + f.server.Addrs()[0].Addr.String()
	fetch := func(path string) string {
		resp, err := f.client.Get(site + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.Header.Get("Cache-Status")
	}
	purge := func(body string) (int, string) {
		req, _ := http.NewRequest("POST", f.api+"/cache/purge", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, got := f.do(t, req)
		return resp.StatusCode, got
	}
	fetch("/a")
	fetch("/b")
	if _, got := f.get(t, "/cache"); !strings.Contains(got, `"zone":"admin","keys":2`) {
		t.Errorf("GET /cache: %s", got)
	}
	if status, got := purge(`{"zone": "admin", "urls": ["` + site + `/a"]}`); status != 200 || got != `{"purged":1}`+"\n" {
		t.Errorf("by URL: %d %s", status, got)
	}
	if cs := fetch("/a"); !strings.HasPrefix(cs, "fit; fwd=uri-miss") {
		t.Errorf("/a after its purge: %q", cs)
	}
	if cs := fetch("/b"); !strings.HasPrefix(cs, "fit; hit") {
		t.Errorf("/b: %q", cs)
	}
	if status, got := purge(`{"tags": ["t"]}`); status != 200 || got != `{"purged":2}`+"\n" {
		t.Errorf("by tag: %d %s", status, got)
	}
	for body, want := range map[string]int{
		`{"zone": "nothing", "all": true}`: 404,
		`{"zone": "admin"}`:                400,
		`{"urls": ["/relative"]}`:          400,
		`not json`:                         400,
	} {
		if status, got := purge(body); status != want {
			t.Errorf("%s: %d %s, want %d", body, status, got, want)
		}
	}
}
