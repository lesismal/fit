package mediapages

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/listener"
	"github.com/lesismal/fit/observe"
	"github.com/lesismal/fit/runtime"
	"github.com/lesismal/fit/upstream"
)

const waitTimeout = 10 * time.Second

// fit is fit serving a config, as it does: compiled, swapped in and served.
type fit struct {
	env    runtime.Env
	rt     *runtime.Runtime
	server *listener.Server
	base   string
}

func startFit(t *testing.T, doc string) *fit {
	t.Helper()
	up, stopUp, err := upstream.StartEngine()
	if err != nil {
		t.Fatal(err)
	}
	f := &fit{env: runtime.Env{Metrics: observe.NewMetrics(), Engine: up}, rt: new(runtime.Runtime)}
	f.swap(t, doc)
	if f.server, err = listener.Start(f.rt); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = f.server.Close()
		f.rt.Close()
		stopUp()
	})
	f.base = "http://" + f.server.Addrs()[0].Addr.String()
	return f
}

// swap compiles doc and swaps it in, as a reload does.
func (f *fit) swap(t *testing.T, doc string) {
	t.Helper()
	c, err := config.Load([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	g, err := runtime.Compile(c, f.env)
	if err != nil {
		t.Fatal(err)
	}
	f.rt.Swap(g)
}

// conf is a config with media_pages on /media/*, its params params, and a
// policy "live": ann may publish and play room1's streams, viv (a bcrypt
// user) may play them only, and tok, a static token, may do anything.
func conf(params string) string {
	hash, _ := bcrypt.GenerateFromPassword([]byte("viv-password"), bcrypt.MinCost)
	return `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "routes": [{"match": {"path": ["/media", "/media/*"]}, "handler": {"module": "media_pages", "params": ` + params + `}}],
  "media_auth": [{"name": "live",
    "users": [
      {"name": "ann", "password": "ann-password", "publish": ["room1-*"], "play": ["room1-*"]},
      {"name": "viv", "password_hash": "` + string(hash) + `", "play": ["room1-*"]}
    ],
    "tokens": [{"name": "all", "token": "0123456789abcdef-token", "publish": ["*"], "play": ["*"]}]
  }]
}`
}

func do(t *testing.T, method, url, body string, hdr map[string]string) (*http.Response, string) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, url, r)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: waitTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func basic(user, pw string) map[string]string {
	req, _ := http.NewRequest("GET", "/", nil)
	req.SetBasicAuth(user, pw)
	return map[string]string{"Authorization": req.Header.Get("Authorization")}
}

var bearer = map[string]string{"Authorization": "Bearer 0123456789abcdef-token"}

// events follows a room's events.
type events struct {
	resp *http.Response
	ch   chan []member1
	done chan struct{}
}

type member1 struct {
	Name string `json:"name"`
	state
}

func follow(t *testing.T, url string, hdr map[string]string) *events {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("events: %d %s", resp.StatusCode, b)
	}
	e := &events{resp: resp, ch: make(chan []member1, 64), done: make(chan struct{})}
	t.Cleanup(e.close)
	go func() {
		defer close(e.done)
		sc := bufio.NewScanner(resp.Body)
		name := ""
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: ") && name == "room":
				var v struct{ Members []member1 }
				if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &v) == nil {
					// The reader keeps reading: when the test is not
					// looking, the oldest event is dropped.
					for {
						select {
						case e.ch <- v.Members:
						default:
							select {
							case <-e.ch:
							default:
							}
							continue
						}
						break
					}
				}
			case line == "":
				name = ""
			}
		}
	}()
	return e
}

func (e *events) close() { e.resp.Body.Close() }

// waitFor waits for an event whose members ok accepts.
func (e *events) waitFor(t *testing.T, what string, ok func([]member1) bool) []member1 {
	t.Helper()
	deadline := time.After(waitTimeout)
	for {
		select {
		case m := <-e.ch:
			if ok(m) {
				return m
			}
		case <-e.done:
			t.Fatalf("the event stream ended waiting for %s", what)
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func names(m []member1) string {
	s := make([]string, len(m))
	for i, x := range m {
		s[i] = x.Name
	}
	return strings.Join(s, ",")
}

func join(t *testing.T, f *fit, room, body string, hdr map[string]string) (int, map[string]any) {
	t.Helper()
	resp, b := do(t, "POST", f.base+"/media/api/rooms/"+room+"/join", body, hdr)
	var v map[string]any
	_ = json.Unmarshal([]byte(b), &v)
	return resp.StatusCode, v
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The pages, their assets and the config API.
func TestPages(t *testing.T) {
	f := startFit(t, conf(`{"auth": "live", "login": true, "title": "Our media", "hls": "/hls/{stream}/index.m3u8",
		"whep": "https://media.example.com/whep/{stream}",
		"links": [{"name": "RTMP", "url": "rtmp://{host}/live/{stream}?token={token}"}],
		"ice_servers": [{"urls": ["stun:stun.example.com:3478"]}]}`))
	resp, _ := do(t, "GET", f.base+"/media?room=x", "", nil)
	if resp.StatusCode != http.StatusPermanentRedirect || resp.Header.Get("Location") != "/media/?room=x" {
		t.Fatalf("redirect: %d %v", resp.StatusCode, resp.Header)
	}
	for _, p := range []string{"/media/", "/media/call", "/media/publish", "/media/play"} {
		resp, body := do(t, "GET", f.base+p, "", nil)
		csp := resp.Header.Get("Content-Security-Policy")
		if resp.StatusCode != 200 || !strings.Contains(body, `<base href="/media/">`) || !strings.Contains(body, "<title>Our media</title>") ||
			!strings.Contains(csp, "connect-src 'self' https://media.example.com") || !strings.Contains(csp, "script-src 'self'") {
			t.Fatalf("%s: %d %v %.200s", p, resp.StatusCode, resp.Header, body)
		}
	}
	resp, body := do(t, "GET", f.base+"/media/app.js", "", nil)
	etag := resp.Header.Get("Etag")
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/javascript") || etag == "" || !strings.Contains(body, "replaceTrack") {
		t.Fatalf("app.js: %d %v", resp.StatusCode, resp.Header)
	}
	if resp, _ := do(t, "GET", f.base+"/media/app.js", "", map[string]string{"If-None-Match": etag}); resp.StatusCode != http.StatusNotModified {
		t.Fatalf("If-None-Match: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "GET", f.base+"/media/app.css", "", nil); resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/css") {
		t.Fatalf("app.css: %d", resp.StatusCode)
	}
	for _, p := range []string{"/media/nothing", "/media/api/nothing", "/media/api/rooms/bad%20name/join", "/media/web/app.js"} {
		if resp, _ := do(t, "GET", f.base+p, "", nil); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: %d", p, resp.StatusCode)
		}
	}
	if resp, _ := do(t, "POST", f.base+"/media/", "x", nil); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST page: %d", resp.StatusCode)
	}
	resp, body = do(t, "GET", f.base+"/media/api/config", "", nil)
	var c struct {
		Title, WHIP, WHEP, HLS string
		Login, Auth            bool
		Public                 map[string]bool
		Links                  []Link
		ICEServers             []map[string]any
		Room                   struct{ MaxMembers, HeartbeatMs int }
	}
	if err := json.Unmarshal([]byte(body), &c); err != nil || resp.StatusCode != 200 {
		t.Fatalf("config: %d %s", resp.StatusCode, body)
	}
	if c.Title != "Our media" || c.WHIP != "/whip/{stream}" || c.WHEP != "https://media.example.com/whep/{stream}" || !c.Login || !c.Auth ||
		c.Public["play"] || len(c.Links) != 1 || len(c.ICEServers) != 1 || c.Room.MaxMembers != 16 || c.Room.HeartbeatMs != 10000 {
		t.Fatalf("config: %s", body)
	}
	if resp, _ := do(t, "POST", f.base+"/media/api/config", "{}", nil); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST config: %d", resp.StatusCode)
	}
}

// The login answers a right password with a token as a cookie, and the
// token lets its holder into the room API.
func TestLogin(t *testing.T) {
	f := startFit(t, conf(`{"auth": "live", "login": true}`))
	login := f.base + "/media/api/login"
	if resp, _ := do(t, "POST", login, `{"user": "ann", "password": "wrong"}`, nil); resp.StatusCode != 401 {
		t.Fatalf("a wrong password: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "POST", login, `{"user": "nobody", "password": "x"}`, nil); resp.StatusCode != 401 {
		t.Fatalf("an unknown user: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "POST", login, `not json`, nil); resp.StatusCode != 400 {
		t.Fatalf("not JSON: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "GET", login, ``, nil); resp.StatusCode != 405 {
		t.Fatalf("GET: %d", resp.StatusCode)
	}
	resp, body := do(t, "POST", login, `{"user": "ann", "password": "ann-password"}`, nil)
	var v struct {
		Token, Subject string
		Expires        int64
		Publish, Play  []string
	}
	if resp.StatusCode != 200 || json.Unmarshal([]byte(body), &v) != nil || !strings.HasPrefix(v.Token, "fm1.") || v.Subject != "ann" ||
		len(v.Publish) != 1 || v.Expires < time.Now().Add(11*time.Hour).Unix() {
		t.Fatalf("login: %d %s", resp.StatusCode, body)
	}
	ck := resp.Header.Get("Set-Cookie")
	for _, want := range []string{"fit_media_token=" + v.Token, "Path=/", "HttpOnly", "SameSite=Strict", "Max-Age="} {
		if !strings.Contains(ck, want) {
			t.Fatalf("Set-Cookie %q lacks %q", ck, want)
		}
	}
	if strings.Contains(ck, "Secure") {
		t.Fatalf("Secure without TLS: %q", ck)
	}
	// The cookie, and the token as a bearer token, let ann in.
	if code, _ := join(t, f, "room1", `{"name": "ann", "publishing": true}`, map[string]string{"Cookie": "fit_media_token=" + v.Token}); code != 200 {
		t.Fatalf("join with the cookie: %d", code)
	}
	if code, _ := join(t, f, "room2", `{"name": "ann"}`, map[string]string{"Authorization": "Bearer " + v.Token}); code != 403 {
		t.Fatalf("join another room: %d", code)
	}
	// A bcrypt user, checked on mediaauth's pool.
	resp, body = do(t, "POST", login, `{"user": "viv", "password": "viv-password"}`, nil)
	if resp.StatusCode != 200 || !strings.Contains(body, `"subject":"viv"`) || !strings.Contains(body, `"publish":[]`) {
		t.Fatalf("bcrypt login: %d %s", resp.StatusCode, body)
	}
	resp, _ = do(t, "POST", f.base+"/media/api/logout", `{}`, nil)
	if ck := resp.Header.Get("Set-Cookie"); resp.StatusCode != 200 || !strings.Contains(ck, "Max-Age=0") {
		t.Fatalf("logout: %d %q", resp.StatusCode, ck)
	}
	// A body past max_message_bytes.
	if resp, _ := do(t, "POST", login, `{"user": "ann", "password": "`+strings.Repeat("x", 2000)+`"}`, nil); resp.StatusCode != 413 {
		t.Fatalf("a large body: %d", resp.StatusCode)
	}

	// Without login, there is no login.
	g := startFit(t, conf(`{"auth": "live"}`))
	if resp, _ := do(t, "POST", g.base+"/media/api/login", `{"user": "ann", "password": "ann-password"}`, nil); resp.StatusCode != 404 {
		t.Fatalf("login off: %d", resp.StatusCode)
	}
}

// The params are checked.
func TestParams(t *testing.T) {
	for _, tc := range []struct{ params, want string }{
		{`{"prefix": "media"}`, "prefix"},
		{`{"prefix": "/a b"}`, "prefix"},
		{`{"login": true}`, "login"},
		{`{"auth": "nope"}`, "no media_auth policy"},
		{`{"whip": "/whip/"}`, "{stream}"},
		{`{"whep": "ftp://x/{stream}"}`, "neither"},
		{`{"hls": "/x/{stream}\"><script>"}`, "{stream}"},
		{`{"title": "<b>"}`, "title"},
		{`{"links": [{"name": "x", "url": "a\"b"}]}`, "links"},
		{`{"member_ttl": "1s"}`, "member_ttl"},
	} {
		c, err := config.Load([]byte(conf(tc.params)))
		if err == nil {
			_, err = runtime.Compile(c, runtime.Env{Metrics: observe.NewMetrics()})
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.params, err, tc.want)
		}
	}
}

// Members join, announce their switches and leave, and the watchers of the
// room see each change; the room API checks the policy.
func TestRoom(t *testing.T) {
	f := startFit(t, conf(`{"auth": "live", "max_members": 3}`))
	ev := f.base + "/media/api/rooms/room1/events"

	// No credentials: 401, for the events and a join.
	if resp, _ := do(t, "GET", ev, "", nil); resp.StatusCode != 401 || resp.Header.Get("Www-Authenticate") == "" {
		t.Fatalf("events without credentials: %d %v", resp.StatusCode, resp.Header)
	}
	if code, _ := join(t, f, "room1", `{"name": "ann"}`, nil); code != 401 {
		t.Fatalf("join without credentials: %d", code)
	}
	if resp, _ := do(t, "GET", f.base+"/media/api/rooms/room9/events", "", basic("ann", "ann-password")); resp.StatusCode != 403 {
		t.Fatalf("events of another room: %d", resp.StatusCode)
	}

	w := follow(t, ev, basic("viv", "viv-password"))
	w.waitFor(t, "the empty room", func(m []member1) bool { return len(m) == 0 })

	code, v := join(t, f, "room1", `{"name": "ann", "audio": true, "video": true, "audioDevice": true, "videoDevice": false}`, basic("ann", "ann-password"))
	if code != 200 || v["publish"] != true || v["key"] == nil || v["stream"] != "room1-ann" {
		t.Fatalf("ann joins: %d %v", code, v)
	}
	annKey := v["key"].(string)
	m := w.waitFor(t, "ann", func(m []member1) bool { return names(m) == "ann" })
	if !m[0].Audio || !m[0].Video || !m[0].AudioDevice || m[0].VideoDevice || m[0].Publishing {
		t.Fatalf("ann: %+v", m[0])
	}

	// viv may play, not publish: she joins as a watcher of the room, and
	// may not announce that she publishes.
	code, v = join(t, f, "room1", `{"name": "viv"}`, basic("viv", "viv-password"))
	if code != 200 || v["publish"] != false {
		t.Fatalf("viv joins: %d %v", code, v)
	}
	vivKey := v["key"].(string)
	if code, _ := join(t, f, "room1", `{"name": "viv2", "publishing": true}`, basic("viv", "viv-password")); code != 403 {
		t.Fatalf("viv announces publishing: %d", code)
	}
	resp, _ := do(t, "POST", f.base+"/media/api/rooms/room1/members/viv", `{"key": "`+vivKey+`", "publishing": true}`, nil)
	if resp.StatusCode != 403 {
		t.Fatalf("viv's update to publishing: %d", resp.StatusCode)
	}
	w.waitFor(t, "ann and viv", func(m []member1) bool { return names(m) == "ann,viv" })

	// ann switches her microphone off and publishes.
	resp, _ = do(t, "POST", f.base+"/media/api/rooms/room1/members/ann", `{"key": "`+annKey+`", "audio": false, "video": true, "publishing": true}`, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("ann's update: %d", resp.StatusCode)
	}
	w.waitFor(t, "ann's microphone off", func(m []member1) bool { return len(m) == 2 && !m[0].Audio && m[0].Publishing })

	// A wrong key: 404, as no member.
	if resp, _ := do(t, "POST", f.base+"/media/api/rooms/room1/members/ann", `{"key": "nope"}`, nil); resp.StatusCode != 404 {
		t.Fatalf("a wrong key: %d", resp.StatusCode)
	}
	// The name is ann's: 409 for another, and ann's own key takes it back.
	if code, _ := join(t, f, "room1", `{"name": "ann"}`, bearer); code != 409 {
		t.Fatalf("ann's name taken: %d", code)
	}
	code, v = join(t, f, "room1", `{"name": "ann", "key": "`+annKey+`", "audio": true}`, bearer)
	if code != 200 || v["key"] != annKey {
		t.Fatalf("ann joins again: %d %v", code, v)
	}
	// max_members: 3.
	if code, _ := join(t, f, "room1", `{"name": "third"}`, bearer); code != 200 {
		t.Fatalf("a third member: %d", code)
	}
	if code, _ := join(t, f, "room1", `{"name": "fourth"}`, bearer); code != 503 {
		t.Fatalf("a fourth member: %d", code)
	}
	if code, _ := join(t, f, "room1", `{"name": "bad name"}`, bearer); code != 400 {
		t.Fatalf("a bad name: %d", code)
	}
	if code, _ := join(t, f, "room1", `{"name": "x", "pad": "`+strings.Repeat("x", 2000)+`"}`, bearer); code != 413 {
		t.Fatalf("a large body: %d", code)
	}

	// ann leaves.
	if resp, _ := do(t, "POST", f.base+"/media/api/rooms/room1/leave", `{"name": "ann", "key": "`+annKey+`"}`, nil); resp.StatusCode != 200 {
		t.Fatalf("leave: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "POST", f.base+"/media/api/rooms/room1/leave", `{"name": "ann", "key": "`+annKey+`"}`, nil); resp.StatusCode != 404 {
		t.Fatalf("leave again: %d", resp.StatusCode)
	}
	w.waitFor(t, "ann gone", func(m []member1) bool { return names(m) == "viv,third" })
	if resp, _ := do(t, "GET", f.base+"/media/api/rooms/room1/join", "", bearer); resp.StatusCode != 405 {
		t.Fatalf("GET join: %d", resp.StatusCode)
	}
}

// A member without a heartbeat leaves after member_ttl; a room with no one
// is forgotten; max_rooms bounds the rooms.
func TestRoomTTLAndLimits(t *testing.T) {
	f := startFit(t, conf(`{"max_rooms": 1, "max_members": 1, "member_ttl": "3s"}`))
	if code, _ := join(t, f, "a", `{"name": "x"}`, nil); code != 200 {
		t.Fatalf("join: %d", code)
	}
	if code, _ := join(t, f, "b", `{"name": "x"}`, nil); code != 503 {
		t.Fatalf("a second room: %d", code)
	}
	if resp, _ := do(t, "GET", f.base+"/media/api/rooms/b/events", "", nil); resp.StatusCode != 503 {
		t.Fatalf("watch a second room: %d", resp.StatusCode)
	}
	w := follow(t, f.base+"/media/api/rooms/a/events", nil)
	w.waitFor(t, "x", func(m []member1) bool { return names(m) == "x" })
	// Two watchers a member: one more is refused.
	w2 := follow(t, f.base+"/media/api/rooms/a/events", nil)
	if resp, _ := do(t, "GET", f.base+"/media/api/rooms/a/events", "", nil); resp.StatusCode != 503 {
		t.Fatalf("a third watcher: %d", resp.StatusCode)
	}
	w2.close()
	start := time.Now()
	w.waitFor(t, "x gone quiet", func(m []member1) bool { return len(m) == 0 })
	if d := time.Since(start); d < 2*time.Second {
		t.Fatalf("x left after %s", d)
	}
	w.close()
	// With no one in it, room a goes, and b may come.
	h := hubOf(t, f)
	eventually(t, "room a forgotten", func() bool { h.mu.Lock(); defer h.mu.Unlock(); return len(h.rooms) == 0 })
	if code, _ := join(t, f, "b", `{"name": "x"}`, nil); code != 200 {
		t.Fatalf("join b: %d", code)
	}
}

func hubOf(t *testing.T, f *fit) *hub {
	t.Helper()
	hubsMu.Lock()
	defer hubsMu.Unlock()
	h := hubs[hubKey{f.env.Engine, "/media"}]
	if h == nil {
		t.Fatal("no hub")
	}
	return h
}

func watcherCount(h *hub) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.watchers
}

// A watcher that goes is forgotten; one that does not take its events is
// sent only the latest, and is cut off after stall_timeout, while the room
// goes on for the rest.
func TestWatcherCutAndSlow(t *testing.T) {
	f := startFit(t, conf(`{"stall_timeout": "1s", "max_members": 256}`))
	h := hubOf(t, f)
	w := follow(t, f.base+"/media/api/rooms/r/events", nil)
	w.waitFor(t, "the room", func(m []member1) bool { return true })
	w.close()
	eventually(t, "the cut watcher forgotten", func() bool { return watcherCount(h) == 0 })

	// A watcher that reads nothing.
	conn, err := net.Dial("tcp", strings.TrimPrefix(f.base, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetReadBuffer(4096)
	}
	if _, err := conn.Write([]byte("GET /media/api/rooms/r/events HTTP/1.1\r\nHost: x\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the slow watcher", func() bool { return watcherCount(h) == 1 })
	good := follow(t, f.base+"/media/api/rooms/r/events", nil)

	// Members with long names make large events; a few hundred changes
	// are megabytes the slow watcher does not take.
	name := strings.Repeat("n", 60)
	var keys []string
	for i := range 100 {
		code, v := join(t, f, "r", `{"name": "`+name+string(rune('A'+i/26))+string(rune('a'+i%26))+`"}`, nil)
		if code != 200 {
			t.Fatalf("join %d: %d", i, code)
		}
		keys = append(keys, v["key"].(string))
	}
	var wg sync.WaitGroup
	for i, k := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n := name + string(rune('A'+i/26)) + string(rune('a'+i%26))
			for j := range 20 {
				resp, _ := do(t, "POST", f.base+"/media/api/rooms/r/members/"+n, `{"key": "`+k+`", "audio": `+map[bool]string{true: "true", false: "false"}[j%2 == 0]+`}`, nil)
				if resp.StatusCode != 200 {
					t.Errorf("update: %d", resp.StatusCode)
					return
				}
			}
		}()
	}
	wg.Wait()
	// The good watcher sees the last state; the slow one is cut off.
	good.waitFor(t, "every member's last state", func(m []member1) bool {
		if len(m) != 100 {
			return false
		}
		for _, x := range m {
			if x.Audio {
				return false
			}
		}
		return true
	})
	eventually(t, "the slow watcher cut off", func() bool { return watcherCount(h) == 1 })
}

// A reload keeps the rooms; the config swapped out drains its event
// streams, and the browser follows the room again under the new one.
func TestReload(t *testing.T) {
	f := startFit(t, conf(`{}`))
	f.rt.SetDrainTimeout(200 * time.Millisecond)
	if code, _ := join(t, f, "r", `{"name": "x"}`, nil); code != 200 {
		t.Fatal(code)
	}
	w := follow(t, f.base+"/media/api/rooms/r/events", nil)
	w.waitFor(t, "x", func(m []member1) bool { return names(m) == "x" })
	h := hubOf(t, f)
	f.swap(t, conf(`{"max_members": 5}`))
	select {
	case <-w.done:
	case <-time.After(waitTimeout):
		t.Fatal("the old config's event stream was not drained")
	}
	if hubOf(t, f) != h {
		t.Fatal("the reload made a new hub")
	}
	w = follow(t, f.base+"/media/api/rooms/r/events", nil)
	w.waitFor(t, "x, kept", func(m []member1) bool { return names(m) == "x" })
	if h.set.Load().maxMembers != 5 {
		t.Fatal("the new settings are not applied")
	}
	w.close()
	eventually(t, "no watchers", func() bool { return watcherCount(h) == 0 })
	// The last config gone, the hub goes.
	f.rt.Close()
	eventually(t, "the hub closed", func() bool {
		hubsMu.Lock()
		defer hubsMu.Unlock()
		return hubs[hubKey{f.env.Engine, "/media"}] == nil
	})
}
