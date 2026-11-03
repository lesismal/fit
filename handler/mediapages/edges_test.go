package mediapages

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/observe"
	"github.com/lesismal/fit/runtime"
)

// A login needs a policy with users: one of tokens alone is refused.
func TestLoginWithoutUsers(t *testing.T) {
	c, err := config.Load([]byte(`{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "routes": [{"handler": {"module": "media_pages", "params": {"auth": "toks", "login": true}}}],
  "media_auth": [{"name": "toks", "tokens": [{"name": "all", "token": "0123456789abcdef-token", "play": ["*"]}]}]
}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.Compile(c, runtime.Env{Metrics: observe.NewMetrics()}); err == nil || !strings.Contains(err.Error(), `"toks" has no users`) {
		t.Fatalf("login with a policy of tokens: %v", err)
	}
}

// A TURN server's username and credential reach the page's config; a path
// the route sends that is not below the prefix is 404; logout takes POST.
func TestPagesEdges(t *testing.T) {
	doc := strings.Replace(conf(`{"auth": "live", "ice_servers": [{"urls": ["turn:turn.example.com"], "username": "u", "credential": "c"}]}`),
		`["/media", "/media/*"]`, `["/media", "/media/*", "/elsewhere"]`, 1)
	f := startFit(t, doc)
	resp, body := do(t, "GET", f.base+"/media/api/config", "", nil)
	var c struct{ ICEServers []map[string]any }
	if resp.StatusCode != 200 || json.Unmarshal([]byte(body), &c) != nil || len(c.ICEServers) != 1 ||
		c.ICEServers[0]["username"] != "u" || c.ICEServers[0]["credential"] != "c" {
		t.Fatalf("config: %d %s", resp.StatusCode, body)
	}
	if resp, _ := do(t, "GET", f.base+"/elsewhere", "", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a path outside the prefix: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "GET", f.base+"/media/api/logout", "", nil); resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != "POST" {
		t.Fatalf("GET logout: %d %v", resp.StatusCode, resp.Header)
	}
}

// The room API answers a body that is not JSON 400, an operation it has
// not 404, and an update that changes nothing 200; a body without a length
// past max_message_bytes is 413 once it outgrows it.
func TestRoomEdges(t *testing.T) {
	f := startFit(t, conf(`{"auth": "live"}`))
	api := f.base + "/media/api/rooms/room1/"
	if code, _ := join(t, f, "room1", `not json`, bearer); code != http.StatusBadRequest {
		t.Fatalf("a join that is not JSON: %d", code)
	}
	code, v := join(t, f, "room1", `{"name": "ann", "audio": true}`, bearer)
	if code != 200 {
		t.Fatalf("join: %d %v", code, v)
	}
	key := v["key"].(string)
	for _, tc := range []struct {
		op, body string
		want     int
	}{
		{"members/ann", `not json`, http.StatusBadRequest},
		{"members/ann", `{"key": "` + key + `", "audio": true}`, http.StatusOK},
		{"unknown", `{}`, http.StatusNotFound},
	} {
		if resp, body := do(t, "POST", api+tc.op, tc.body, nil); resp.StatusCode != tc.want {
			t.Errorf("%s %s: %d %s, want %d", tc.op, tc.body, resp.StatusCode, body, tc.want)
		}
	}

	// A chunked body: its length is learned only as it comes.
	big := strings.Repeat("x", 2000)
	req, _ := http.NewRequest("POST", api+"join", io.MultiReader(strings.NewReader(`{"name": "`), strings.NewReader(big)))
	req.Header.Set("Authorization", bearer["Authorization"])
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge || req.ContentLength != 0 {
		t.Fatalf("a chunked body past the limit: %d (length %d)", resp.StatusCode, req.ContentLength)
	}
}
