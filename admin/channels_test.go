package admin

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fib/media/hub"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/medialib"
	"github.com/lesismal/fit/streaming/channels"
	"github.com/lesismal/fit/upstream"
)

// TestChannels drives the streaming role's channels and push targets
// through the Admin API: lists, one channel, skips, a stop and a start, and
// a push target's actions.
func TestChannels(t *testing.T) {
	f := start(t, `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}], "routes": [{"handler": {"module": "respond", "params": {"body": "x"}}}]}`, &config.Admin{})
	engine := f.configs.env.Engine
	decode := func(method, path, body string, want int) map[string]any {
		t.Helper()
		var req *http.Request
		if body == "" {
			req, _ = http.NewRequest(method, f.api+path, nil)
		} else {
			req, _ = http.NewRequest(method, f.api+path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
		}
		resp, got := f.do(t, req)
		if resp.StatusCode != want {
			t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, got)
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(got), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	decode("GET", "/streaming/channels", "", 404)
	decode("GET", "/streaming/push", "", 404)
	decode("GET", "/streaming/channels/x", "", 404)
	decode("POST", "/streaming/push/x", `{"action": "stop"}`, 404)

	root, err := filepath.Abs(filepath.Join("..", "medialib", "testdata", "lib"))
	if err != nil {
		t.Fatal(err)
	}
	if err := medialib.Apply(engine, []config.MediaLibrary{{Name: "films", Roots: []string{root}}}, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { medialib.Remove(engine) })
	l := medialib.Lookup(engine, "films")
	for deadline := time.Now().Add(20 * time.Second); l.Status().Scans == 0; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("no scan")
		}
	}
	h := hub.New(hub.Config{})
	dial, stopDial, err := upstream.StartEngine()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stopDial)
	m := channels.New(channels.Options{Hub: h, Libraries: engine, Dial: dial})
	channels.Register(engine, m)
	t.Cleanup(func() {
		channels.Unregister(engine, m)
		m.Stop()
		h.Close()
	})
	chs := []config.StreamChannel{{Name: "tv/films", Library: "films", Items: []string{"films/bbb", "sintel"}}}
	c := &config.Streaming{Channels: chs, Push: []config.StreamPush{{Stream: "tv/films", URL: "rtmp://127.0.0.1:1/app/secret-key", RetryMin: config.Duration(time.Hour)}}}
	if err := m.Apply(c.Channels, c.PushTargets()); err != nil {
		t.Fatal(err)
	}

	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		list := decode("GET", "/streaming/channels", "", 200)["channels"].([]any)
		if len(list) != 1 {
			t.Fatalf("%v", list)
		}
		ch := list[0].(map[string]any)
		if ch["state"] == channels.StatePlaying {
			if ch["now"].(map[string]any)["item"] != "films/bbb" || ch["library"] != "films" || ch["order"] != "sequence" {
				t.Errorf("%v", ch)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not playing: %v", ch)
		}
	}
	one := decode("GET", "/streaming/channels/tv/films", "", 200)
	if one["name"] != "tv/films" || len(one["push"].([]any)) != 1 {
		t.Errorf("%v", one)
	}
	decode("GET", "/streaming/channels/tv/none", "", 404)
	decode("POST", "/streaming/channels/tv/none", `{"action": "skip"}`, 404)

	st := decode("POST", "/streaming/channels/tv/films", `{"action": "skip", "item": "sintel"}`, 200)
	if st["state"] == channels.StateStopped {
		t.Errorf("%v", st)
	}
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		now, _ := decode("GET", "/streaming/channels/tv/films", "", 200)["now"].(map[string]any)
		if now != nil && now["item"] == "sintel" && now["from"] == "admin" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not skipped: %v", now)
		}
	}
	decode("POST", "/streaming/channels/tv/films", `{"action": "skip", "item": "none"}`, 404)
	decode("POST", "/streaming/channels/tv/films", `{"action": "rewind"}`, 400)
	decode("POST", "/streaming/channels/tv/films", `{"action": `, 400)
	req, _ := http.NewRequest("POST", f.api+"/streaming/channels/tv/films", strings.NewReader(`{"action": "stop"}`))
	req.Header.Set("Content-Type", "text/plain")
	if resp, _ := f.do(t, req); resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("a body not JSON: %d", resp.StatusCode)
	}
	if st := decode("POST", "/streaming/channels/tv/films", `{"action": "stop"}`, 200); st["state"] != channels.StateStopped {
		t.Errorf("%v", st)
	}
	decode("POST", "/streaming/channels/tv/films", `{"action": "skip"}`, 409)
	if st := decode("POST", "/streaming/channels/tv/films", `{"action": "start"}`, 200); st["state"] == channels.StateStopped {
		t.Errorf("%v", st)
	}

	push := decode("GET", "/streaming/push", "", 200)["push"].([]any)
	if len(push) != 1 {
		t.Fatalf("%v", push)
	}
	p := push[0].(map[string]any)
	if p["name"] != "tv/films#1" || p["url"] != "rtmp://127.0.0.1:1/app/…" || strings.Contains(p["error"].(string)+p["url"].(string), "secret") {
		t.Errorf("%v", p)
	}
	if st := decode("POST", "/streaming/push/tv/films%231", `{"action": "stop"}`, 200); st["state"] != channels.PushStopped {
		t.Errorf("%v", st)
	}
	decode("POST", "/streaming/push/tv/films%231", `{"action": "start"}`, 200)
	decode("POST", "/streaming/push/tv/films%231", `{"action": "restart"}`, 200)
	decode("POST", "/streaming/push/tv/films%231", `{"action": "nope"}`, 400)
	decode("POST", "/streaming/push/none", `{"action": "stop"}`, 404)
	decode("POST", "/streaming/push/tv/films%231", `{"action": `, 400)
}
