package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fib"
	"github.com/lesismal/fib/media/av"
	"github.com/lesismal/fib/media/hub"
	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/certtest"
	"github.com/lesismal/fit/mediaauth"
	"github.com/lesismal/fit/medialib"
	_ "github.com/lesismal/fit/middleware/auth"
	"github.com/lesismal/fit/observe"
	"github.com/lesismal/fit/runtime"
	_ "github.com/lesismal/fit/streaming" // the hls handler
	"github.com/lesismal/fit/streaming/channels"
	"github.com/lesismal/fit/upstream"
)

// mediaFixture is fit with media libraries, vod and hls routes, media_auth
// policies and media_tokens, its Admin API, and a channel.
type mediaFixture struct {
	*fixture
	engine *fib.Engine
	dir    string // the secret files'
	web    string // the listener's origin
	secure string // the TLS listener's
	hub    *hub.Hub
}

// mediaDoc is the fixture's config: libraries of medialib's testdata, each
// behind a vod route of another policy.
func mediaDoc(t *testing.T, root, dir string, hls bool) string {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	var libs []string
	for _, n := range []string{"films", "tok", "pubs", "free", "odd", "later"} {
		libs = append(libs, `{"name": "`+n+`", "roots": [`+q(root)+`]}`)
	}
	basic := `"middleware": [{"module": "basic_auth", "params": {"users": [{"username": "backend", "password_hash": ` + q(string(hash)) + `}]}}]`
	routes := `
    {"name": "films", "listeners": ["web"], "match": {"path": ["/vod/*"]}, "handler": {"module": "vod", "params": {"library": "films", "auth": "site", "cors": "*"}}},
    {"listeners": ["web"], "match": {"path": ["/tok/*"]}, "handler": {"module": "vod", "params": {"library": "tok", "prefix": "/tok", "auth": "tok"}}},
    {"listeners": ["web"], "match": {"path": ["/pub/*"]}, "handler": {"module": "vod", "params": {"library": "pubs", "prefix": "/pub/", "auth": "pub"}}},
    {"listeners": ["secure"], "match": {"path": ["/free/*"]}, "handler": {"module": "vod", "params": {"library": "free", "prefix": "/free/"}}},
    {"listeners": ["web"], "match": {"path": ["/odd/*"]}, "handler": {"module": "vod", "params": {"library": "odd", "prefix": "/o%d/", "auth": "site"}}},
    {"listeners": ["web"], "match": {"path": ["/later/*"]}, "handler": {"module": "vod", "params": {"library": "later", "prefix": "/later/", "auth": "site"}}},
    {"name": "mt", "listeners": ["web"], "match": {"path": ["/media-tokens", "/media-tokens/*"]}, ` + basic + `,
     "handler": {"module": "media_tokens", "params": {"auth": "site", "url_prefixes": ["/vod/"], "max_ttl": "30m", "url_base": "https://media.example.com/"}}},
    {"name": "mt2", "listeners": ["web"], "match": {"path": ["/mt2", "/mt2/*"]}, ` + basic + `,
     "handler": {"module": "media_tokens", "params": {"prefix": "/mt2", "auth": "file1"}}}`
	if hls {
		routes += `,
    {"listeners": ["web"], "match": {"path": ["/hls/*"]}, "handler": {"module": "hls", "params": {"prefix": "/hls/", "auth": "tok"}}}`
	}
	return `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"},
    {"name": "secure", "address": "127.0.0.1:0", "tls": {"certificates": [{"cert_file": ` + q(filepath.Join(dir, "cert.pem")) + `, "key_file": ` + q(filepath.Join(dir, "key.pem")) + `}]}},
    {"name": "proxy", "address": "127.0.0.1:0", "protocols": ["socks"], "socks": {"clients": ["127.0.0.1/32"]}}],
  "media_library": [` + strings.Join(libs, ", ") + `],
  "media_auth": [
    {"name": "site", "keys": [{"key": "k1", "secret_file": ` + q(filepath.Join(dir, "k1")) + `}, {"key": "k2", "secret_file": ` + q(filepath.Join(dir, "k2")) + `}],
     "url_signing": {"max_sessions": 3}},
    {"name": "tok", "secret": "the token policy's secret"},
    {"name": "pub", "public": ["play"]},
    {"name": "inline", "secret": "an inline secret of 16+", "url_signing": {"ttl": "5m", "on_limit": "end_oldest", "bind_address": "subnet", "referers": ["https://*.example.com"]}},
    {"name": "file1", "secret_file": ` + q(filepath.Join(dir, "single")) + `}
  ],
  "routes": [` + routes + `]
}`
}

func startMedia(t *testing.T) *mediaFixture {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "medialib", "testdata", "lib"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for name, secret := range map[string]string{"k1": "the first key, 16 bytes+", "k2": "the second key, 16 bytes+", "single": "the single secret, 16+"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(secret), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ca, err := certtest.NewCA("media CA")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := ca.Issue(t.TempDir(), "", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	copyFile(t, cert.CertFile, filepath.Join(dir, "cert.pem"))
	copyFile(t, cert.KeyFile, filepath.Join(dir, "key.pem"))
	up, stopUp, err := upstream.StartEngine()
	if err != nil {
		t.Fatal(err)
	}
	f := &mediaFixture{engine: up, dir: dir}
	f.fixture = startEnv(t, mediaDoc(t, root, dir, true), &config.Admin{}, runtime.Env{Metrics: observe.NewMetrics(), Engine: up})
	// The libraries as the role applies them, but for later, which is not
	// loaded.
	libs := slices.DeleteFunc(slices.Clone(f.configs.CurrentConfig().MediaLibrary), func(l config.MediaLibrary) bool { return l.Name == "later" })
	if err := medialib.Apply(up, libs, nil); err != nil {
		t.Fatal(err)
	}
	// The policies as the role applies them.
	if err := mediaauth.Apply(up, f.configs.CurrentConfig().MediaAuth); err != nil {
		t.Fatal(err)
	}
	f.hub = hub.New(hub.Config{})
	m := channels.New(channels.Options{Hub: f.hub, Libraries: up, Dial: up})
	channels.Register(up, m)
	t.Cleanup(func() {
		channels.Unregister(up, m)
		m.Stop()
		f.hub.Close()
		medialib.Remove(up)
		mediaauth.Remove(up)
		stopUp()
	})
	for _, l := range medialib.Libraries(up) {
		for deadline := time.Now().Add(20 * time.Second); l.Status().Scans == 0; time.Sleep(5 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("no scan of %s", l.Name())
			}
		}
	}
	sc := &config.Streaming{Channels: []config.StreamChannel{{Name: "tv/films", Library: "films", Items: []string{"films/bbb"},
		Push: []config.StreamPush{{URL: "rtmp://127.0.0.1:1/app/the-stream-key", RetryMin: config.Duration(time.Hour)}}}}}
	if err := m.Apply(sc.Channels, sc.PushTargets()); err != nil {
		t.Fatal(err)
	}
	for _, a := range f.server.Addrs() {
		switch a.Name {
		case "web":
			f.web = "http://" + a.Addr.String()
		case "secure":
			f.secure = "https://" + a.Addr.String()
		}
	}
	return f
}

// call sends method path with body, the header fields in pairs, to the
// Admin API, and decodes its answer, which must have status want.
func (f *mediaFixture) call(t *testing.T, method, path, body string, want int, header ...string) map[string]any {
	t.Helper()
	req, _ := http.NewRequest(method, f.api+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, got := f.do(t, req)
	if resp.StatusCode != want {
		t.Fatalf("%s %s %s: %d %s, want %d", method, path, body, resp.StatusCode, got, want)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(got), &v); err != nil {
		t.Fatalf("%s %s: %v: %s", method, path, err, got)
	}
	v["_cache"] = resp.Header.Get("Cache-Control")
	return v
}

// play fetches u, as a player would, and returns its status.
func play(t *testing.T, u string, header ...string) int {
	t.Helper()
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

// sign signs a URL of path for subject and session with the policy's key.
func (f *mediaFixture) sign(t *testing.T, policy, path, subject, session, key string, expiry time.Time) string {
	t.Helper()
	res, _ := mediaauth.ResourceOf(path)
	q, err := mediaauth.Lookup(f.engine, policy).SignURL(res, mediaauth.Viewer{Subject: subject, Session: session}, expiry, key)
	if err != nil {
		t.Fatal(err)
	}
	return f.web + path + "?" + q
}

// TestMediaPreview signs previews: through url_signing, which plays until
// its session is ended; through a token; public; of a channel; and the
// refusals.
func TestMediaPreview(t *testing.T) {
	f := startMedia(t)
	before := time.Now()
	pv := f.call(t, "POST", "/media/preview", `{"library": "films", "item": "films/bbb"}`, 200, ConsoleUserHeader, "alice", ConsoleHostHeader, "evil example")
	base := f.web + "/vod/films/films/bbb/"
	if pv["_cache"] != "no-store" || pv["signed"] != "url" || pv["subject"] != "workbench:alice" || pv["route"] != "films" || pv["cors"] != "*" ||
		!strings.HasPrefix(pv["url"].(string), base+"index.m3u8?exp=") || !strings.HasPrefix(pv["poster"].(string), base+"poster.jpg?exp=") ||
		pv["session"] == "" || !strings.Contains(pv["url"].(string), "sid="+pv["session"].(string)) {
		t.Fatalf("the preview: %v", pv)
	}
	if exp := int64(pv["expires"].(float64)); exp < before.Add(PreviewTTL).Unix()-1 || exp > time.Now().Add(PreviewTTL).Unix() {
		t.Errorf("expires %d", exp)
	}
	if got := play(t, pv["url"].(string)); got != 200 {
		t.Fatalf("the preview's playlist: %d", got)
	}
	if got := play(t, pv["poster"].(string)); got != 200 {
		t.Fatalf("the preview's poster: %d", got)
	}
	if got := play(t, base+"index.m3u8"); got == 200 {
		t.Fatal("the playlist without the signature plays")
	}
	// Ended, the session's next request is refused at once.
	end := `{"subject": "workbench:alice", "id": "` + pv["session"].(string) + `"}`
	if got := f.call(t, "POST", "/media/auth/site/sessions/end", end, 200); got["ended"] != true {
		t.Fatalf("end: %v", got)
	}
	if got := play(t, pv["url"].(string)); got != 403 {
		t.Fatalf("the ended session's playlist: %d", got)
	}
	f.call(t, "POST", "/media/auth/site/sessions/end", end, 404)
	f.call(t, "POST", "/media/auth/site/sessions/end", `{"subject": "workbench:alice"}`, 400)
	f.call(t, "POST", "/media/auth/site/sessions/end", `{`, 400)
	f.call(t, "POST", "/media/auth/none/sessions/end", end, 404)
	// Expired, refused too.
	if got := play(t, f.sign(t, "site", "/vod/films/films/bbb/index.m3u8", "u", "", "", time.Now().Add(-time.Second))); got != 403 {
		t.Fatalf("an expired URL: %d", got)
	}

	// The Admin API's own caller.
	if pv := f.call(t, "POST", "/media/preview", `{"library": "films", "item": "films/bbb"}`, 200); pv["subject"] != "workbench:admin-api" {
		t.Errorf("no console user: %v", pv)
	}
	// A token, for a policy without url_signing; a prefix without its '/'.
	tok := f.call(t, "POST", "/media/preview", `{"library": "tok", "item": "films/bbb"}`, 200)
	if tok["signed"] != "token" || tok["session"] != "" || tok["route"] != "#2" || tok["cors"] != "" ||
		!strings.HasPrefix(tok["url"].(string), f.web+"/tok/films/bbb/index.m3u8?token="+url.QueryEscape(mediaauth.TokenPrefix)) ||
		!strings.Contains(tok["poster"].(string), "/tok/films/bbb/poster.jpg?token=") {
		t.Fatalf("the token preview: %v", tok)
	}
	if got := play(t, tok["url"].(string)); got != 200 {
		t.Fatalf("the token preview's playlist: %d", got)
	}
	// Public: play needs nothing, or there is no policy.
	pub := f.call(t, "POST", "/media/preview", `{"library": "pubs", "item": "sintel"}`, 200)
	if pub["signed"] != "public" || pub["url"] != f.web+"/pub/sintel/index.m3u8" || strings.Contains(pub["poster"].(string), "?") {
		t.Fatalf("pubs: %v", pub)
	}
	if got := play(t, pub["url"].(string)); got != 200 {
		t.Fatalf("pubs' playlist: %d", got)
	}
	// On a TLS listener, https.
	if free := f.call(t, "POST", "/media/preview", `{"library": "free", "item": "sintel"}`, 200); free["signed"] != "public" || free["url"] != f.secure+"/free/sintel/index.m3u8" {
		t.Fatalf("free: %v", free)
	}
	// A channel: its hls route, with a token for its stream.
	ch := f.call(t, "POST", "/media/preview", `{"channel": "tv/films"}`, 200)
	if ch["signed"] != "token" || ch["route"] != "#9" || !strings.HasPrefix(ch["url"].(string), f.web+"/hls/tv/films/index.m3u8?token=") || ch["poster"] != "" {
		t.Fatalf("the channel: %v", ch)
	}

	for _, c := range []struct {
		body string
		want int
	}{
		{`{"library": "lost", "item": "films/bbb"}`, 404},
		{`{"library": "films", "item": "films/none"}`, 404},
		{`{"library": "films", "item": "broken"}`, 404},
		{`{"library": "later", "item": "films/bbb"}`, 503},
		{`{"library": "odd", "item": "films/bbb"}`, 400},
		{`{"channel": "tv/none"}`, 404},
		{`{}`, 400},
		{`{"library": "films"}`, 400},
		{`{"library": "films", "item": "films/bbb", "channel": "tv/films"}`, 400},
		{`[`, 400},
	} {
		f.call(t, "POST", "/media/preview", c.body, c.want)
	}
	f.call(t, "POST", "/media/preview", `{"library": "films", "item": "films/bbb"}`, 400, ConsoleUserHeader, strings.Repeat("u", 120))
	// A policy the running config no longer has.
	if err := mediaauth.Apply(f.engine, f.configs.CurrentConfig().MediaAuth[:1]); err != nil {
		t.Fatal(err)
	}
	f.call(t, "POST", "/media/preview", `{"library": "tok", "item": "films/bbb"}`, 503)
	if pols := f.call(t, "GET", "/media/auth", "", 200)["policies"].([]any); len(pols) != 1 {
		t.Errorf("the policies running: %v", pols)
	}
	req, _ := http.NewRequest("POST", f.api+"/media/preview", strings.NewReader(`{"channel": "tv/films"}`))
	req.Header.Set("Content-Type", "text/plain")
	if resp, _ := f.do(t, req); resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("a body not JSON: %d", resp.StatusCode)
	}

	// Without an hls route, a channel has no URL.
	root, _ := filepath.Abs(filepath.Join("..", "medialib", "testdata", "lib"))
	if resp, body := f.load(t, mediaDoc(t, root, f.dir, false)); resp.StatusCode != 200 {
		t.Fatalf("load: %d %s", resp.StatusCode, body)
	}
	f.call(t, "POST", "/media/preview", `{"channel": "tv/films"}`, 404)
}

// TestMediaAuth lists the policies, pages their sessions, rotates their
// keys and signs test URLs.
func TestMediaAuth(t *testing.T) {
	f := startMedia(t)
	body := f.call(t, "GET", "/media/auth", "", 200)
	raw, _ := json.Marshal(body)
	for _, secret := range []string{f.dir, "the first key", "secret_file", "the token policy's secret", "an inline secret"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("GET /media/auth holds %q: %s", secret, raw)
		}
	}
	pols := body["policies"].([]any)
	if len(pols) != 5 {
		t.Fatalf("policies: %v", pols)
	}
	site := pols[0].(map[string]any)
	set := site["settings"].(map[string]any)
	keys := site["keys"].([]any)
	k1, k2 := keys[0].(map[string]any), keys[1].(map[string]any)
	if site["name"] != "site" || site["url_signing"] != true || set["ttl"] != "10m0s" || set["play_grace"] != "30m0s" || set["session_idle"] != "2m0s" ||
		set["max_sessions"] != 3.0 || set["on_limit"] != "refuse" || set["cookie"] != false || set["bind_address"] != "ip" ||
		len(set["referers"].([]any)) != 0 || len(set["origins"].([]any)) != 0 ||
		k1["name"] != "k1" || k1["default"] != true || k1["file"] != true || k1["modified"] == nil || k2["name"] != "k2" || k2["default"] != false ||
		len(site["refusals"].(map[string]any)) != len(mediaauth.URLReasons) {
		t.Errorf("site: %v", site)
	}
	routes := site["token_routes"].([]any)
	if len(routes) != 1 || routes[0].(map[string]any)["route"] != "mt" || routes[0].(map[string]any)["url_base"] != "https://media.example.com" ||
		routes[0].(map[string]any)["max_ttl"] != "30m0s" || routes[0].(map[string]any)["url_prefixes"].([]any)[0] != "/vod/" {
		t.Errorf("site's media_tokens routes: %v", routes)
	}
	tok := pols[1].(map[string]any)
	if tok["url_signing"] != false || tok["settings"] != nil || len(tok["keys"].([]any)) != 1 || len(tok["token_routes"].([]any)) != 0 {
		t.Errorf("tok: %v", tok)
	}
	if k := tok["keys"].([]any)[0].(map[string]any); k["name"] != "" || k["default"] != true || k["file"] != false || k["modified"] != nil {
		t.Errorf("tok's key: %v", k)
	}
	if set := pols[3].(map[string]any)["settings"].(map[string]any); set["ttl"] != "5m0s" || set["on_limit"] != "end_oldest" || set["bind_address"] != "subnet" ||
		set["referers"].([]any)[0] != "https://*.example.com" {
		t.Errorf("inline's settings: %v", set)
	}
	file1 := pols[4].(map[string]any)
	if k := file1["keys"].([]any)[0].(map[string]any); k["name"] != "" || k["file"] != true || k["modified"] == nil {
		t.Errorf("file1's key: %v", k)
	}
	if r := file1["token_routes"].([]any); len(r) != 1 || r[0].(map[string]any)["route"] != "mt2" || r[0].(map[string]any)["max_ttl"] != "24h0m0s" ||
		r[0].(map[string]any)["url_base"] != "" || len(r[0].(map[string]any)["url_prefixes"].([]any)) != 0 {
		t.Errorf("file1's media_tokens routes: %v", r)
	}
	// A key whose file is gone has no time.
	if err := os.Rename(filepath.Join(f.dir, "k2"), filepath.Join(f.dir, "k2.away")); err != nil {
		t.Fatal(err)
	}
	if k := f.call(t, "GET", "/media/auth", "", 200)["policies"].([]any)[0].(map[string]any)["keys"].([]any)[1].(map[string]any); k["file"] != true || k["modified"] != nil {
		t.Errorf("k2 without its file: %v", k)
	}
	if err := os.Rename(filepath.Join(f.dir, "k2.away"), filepath.Join(f.dir, "k2")); err != nil {
		t.Fatal(err)
	}

	// Sessions: two subjects, paged and filtered.
	exp := time.Now().Add(time.Hour)
	bbb := "/vod/films/films/bbb/index.m3u8"
	for _, s := range []struct{ subject, sid, path string }{{"u1", "a", bbb}, {"u1", "b", bbb}, {"u1", "c", "/vod/films/sintel/index.m3u8"}, {"u2", "a", bbb}} {
		if got := play(t, f.sign(t, "site", s.path, s.subject, s.sid, "", exp)); got != 200 {
			t.Fatalf("%v: %d", s, got)
		}
	}
	f.call(t, "POST", "/media/auth/site/sessions/end", `{"subject": "u1", "id": "c"}`, 200)
	page := f.call(t, "GET", "/media/auth/site/sessions?limit=2&offset=1", "", 200)
	list := page["page"].([]any)
	subjects := page["subjects"].([]any)
	if page["total"] != 4.0 || page["matched"] != 4.0 || page["offset"] != 1.0 || page["limit"] != 2.0 || page["max_sessions"] != 3.0 || len(list) != 2 ||
		list[0].(map[string]any)["id"] != "b" || list[1].(map[string]any)["ended"] != true ||
		len(subjects) != 1 || subjects[0].(map[string]any)["subject"] != "u1" || subjects[0].(map[string]any)["sessions"] != 2.0 {
		t.Errorf("a page: %v", page)
	}
	for query, want := range map[string][2]int{
		"subject=u2": {1, 1}, "resource=/vod/films/films/": {3, 3}, "resource=/vod/films/sintel/&subject=u1": {1, 1},
		"resource=/x/": {0, 0}, "offset=9": {4, 0}, "": {4, 4},
	} {
		page := f.call(t, "GET", "/media/auth/site/sessions?"+query, "", 200)
		if page["matched"] != float64(want[0]) || len(page["page"].([]any)) != want[1] {
			t.Errorf("%s: %v", query, page)
		}
	}
	if page := f.call(t, "GET", "/media/auth/site/sessions?subject=u2", "", 200); page["subjects"].([]any)[0].(map[string]any)["sessions"] != 1.0 {
		t.Errorf("u2: %v", page)
	}
	if page := f.call(t, "GET", "/media/auth/tok/sessions", "", 200); page["total"] != 0.0 || page["max_sessions"] != 0.0 || len(page["page"].([]any)) != 0 {
		t.Errorf("tok: %v", page)
	}
	for _, bad := range []string{"limit=0", "limit=1001", "offset=-1", "limit=x"} {
		f.call(t, "GET", "/media/auth/site/sessions?"+bad, "", 400)
	}
	f.call(t, "GET", "/media/auth/none/sessions", "", 404)
	if got := f.call(t, "GET", "/media/auth", "", 200)["policies"].([]any)[0].(map[string]any); got["sessions"] != 3.0 || got["subjects"] != 2.0 {
		t.Errorf("site's sessions: %v", got)
	}

	// Rotation: the key's file is written anew, its URLs refused at once,
	// the other key's still good.
	u1 := f.sign(t, "site", bbb, "r", "", "k1", exp)
	u2 := f.sign(t, "site", bbb, "r", "", "k2", exp)
	rot := f.call(t, "POST", "/media/auth/site/rotate", `{"key": "k1"}`, 200)
	if r := rot["rotated"].([]any); len(r) != 1 || r[0] != "k1" || rot["since"].(float64) < float64(exp.Add(-time.Hour-time.Minute).Unix()) {
		t.Fatalf("rotate: %v", rot)
	}
	if data, err := os.ReadFile(filepath.Join(f.dir, "k1")); err != nil || len(data) < 16 || string(data) == "the first key, 16 bytes+" {
		t.Fatalf("k1's file: %q %v", data, err)
	}
	if got := play(t, u1); got != 403 {
		t.Errorf("k1's URL after its rotation: %d", got)
	}
	if got := play(t, u2); got != 200 {
		t.Errorf("k2's URL after k1's rotation: %d", got)
	}
	if rot := f.call(t, "POST", "/media/auth/site/rotate", `{"all": true}`, 200); len(rot["rotated"].([]any)) != 2 {
		t.Errorf("all: %v", rot)
	}
	if got := play(t, u2); got != 403 {
		t.Errorf("k2's URL after all: %d", got)
	}
	if rot := f.call(t, "POST", "/media/auth/file1/rotate", `{}`, 200); len(rot["rotated"].([]any)) != 1 || rot["rotated"].([]any)[0] != "" {
		t.Errorf("a single secret: %v", rot)
	}
	f.call(t, "POST", "/media/auth/site/rotate", `{}`, 400)
	f.call(t, "POST", "/media/auth/site/rotate", `{"key": "k9"}`, 404)
	f.call(t, "POST", "/media/auth/file1/rotate", `{"key": "k1"}`, 400)
	f.call(t, "POST", "/media/auth/inline/rotate", `{}`, 409)
	f.call(t, "POST", "/media/auth/tok/rotate", `{}`, 409)
	f.call(t, "POST", "/media/auth/site/rotate", `{"key": 1}`, 400)
	f.call(t, "POST", "/media/auth/none/rotate", `{}`, 404)
	// An empty body is {}.
	req, _ := http.NewRequest("POST", f.api+"/media/auth/file1/rotate", nil)
	req.Header.Set("Content-Type", "application/json")
	if resp, body := f.do(t, req); resp.StatusCode != 200 || !strings.Contains(body, `"rotated":[""]`) {
		t.Errorf("an empty body: %d %s", resp.StatusCode, body)
	}
	if err := os.Remove(filepath.Join(f.dir, "single")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(f.dir, "single"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, "single", "x"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := f.call(t, "POST", "/media/auth/file1/rotate", `{}`, 500); strings.Contains(got["error"].(string), f.dir) {
		t.Errorf("an unwritten file: %v", got)
	}

	// Test URLs: on the vod route's listener, or the media_tokens route's
	// url_base; bound to an address when asked.
	tu := f.call(t, "POST", "/media/auth/site/urls", `{"subject": "tester", "path": "`+bbb+`"}`, 200)
	if tu["_cache"] != "no-store" || tu["subject"] != "tester" || tu["session"] == "" || !strings.HasPrefix(tu["url"].(string), f.web+bbb+"?exp=") ||
		int64(tu["expires"].(float64)) > time.Now().Add(config.DefaultURLTTL).Unix() {
		t.Fatalf("a test URL: %v", tu)
	}
	if got := play(t, tu["url"].(string)); got != 200 {
		t.Errorf("the test URL: %d", got)
	}
	viaMT := f.call(t, "POST", "/media/auth/site/urls", `{"subject": "tester", "path": "`+bbb+`", "route": "mt", "ttl": "20m"}`, 200)
	if !strings.HasPrefix(viaMT["url"].(string), "https://media.example.com"+bbb+"?") || int64(viaMT["expires"].(float64)) < time.Now().Add(19*time.Minute).Unix() {
		t.Errorf("through mt: %v", viaMT)
	}
	bound := f.call(t, "POST", "/media/auth/site/urls", `{"subject": "tester", "path": "`+bbb+`", "address": "203.0.113.9", "ttl": "45m"}`, 200)
	if got := play(t, bound["url"].(string)); got != 403 {
		t.Errorf("a URL bound to another address: %d", got)
	}
	if other := f.call(t, "POST", "/media/auth/site/urls", `{"subject": "tester", "path": "/elsewhere/a/index.m3u8"}`, 200); !strings.HasPrefix(other["url"].(string), "/elsewhere/a/index.m3u8?") {
		t.Errorf("a path no route serves: %v", other)
	}
	for _, c := range []struct {
		policy, body string
		want         int
	}{
		{"site", `{"subject": "tester", "path": "` + bbb + `", "route": "mt", "ttl": "1h"}`, 400},
		{"site", `{"subject": "tester", "path": "` + bbb + `", "ttl": "2h"}`, 400},
		{"site", `{"subject": "tester", "path": "` + bbb + `", "ttl": "500ms"}`, 400},
		{"site", `{"subject": "tester", "path": "` + bbb + `", "ttl": "soon"}`, 400},
		{"site", `{"subject": "tester", "path": "/hls/tv/films/index.m3u8", "route": "mt"}`, 400},
		{"site", `{"subject": "tester", "path": "` + bbb + `", "route": "nope"}`, 404},
		{"site", `{"subject": "", "path": "` + bbb + `"}`, 400},
		{"site", `{"subject": "tester", "path": "` + bbb + `", "address": "fe80::1%en0"}`, 400},
		{"site", `{"subject": "tester", "path": "` + bbb + `", "address": "here"}`, 400},
		{"site", `{"subject": "tester", "path": "index.m3u8"}`, 400},
		{"site", `{"subject": `, 400},
		{"tok", `{"subject": "tester", "path": "` + bbb + `"}`, 404},
		{"none", `{"subject": "tester", "path": "` + bbb + `"}`, 404},
	} {
		f.call(t, "POST", "/media/auth/"+c.policy+"/urls", c.body, c.want)
	}
}

// sink is a hub subscriber that drops what it is sent, as a player.
type sink struct{}

func (sink) OnTracks([]*av.Track) {}
func (sink) OnPacket(*av.Packet)  {}
func (sink) OnClose(error)        {}

// TestMediaStats reads what the media pages chart, and the metrics of the
// vod handlers.
func TestMediaStats(t *testing.T) {
	f := startMedia(t)
	sub, err := f.hub.Subscribe("tv/films", sink{}, hub.SubscribeOptions{Wait: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)
	exp := time.Now().Add(time.Hour)
	for _, sid := range []string{"a", "b"} {
		if got := play(t, f.sign(t, "site", "/vod/films/films/bbb/index.m3u8", "u", sid, "", exp)); got != 200 {
			t.Fatalf("%s: %d", sid, got)
		}
	}
	if got := play(t, f.sign(t, "site", "/vod/films/sintel/index.m3u8", "u", "c", "", exp)); got != 200 {
		t.Fatalf("c: %d", got)
	}
	play(t, f.sign(t, "site", "/vod/films/sintel/index.m3u8", "u", "d", "", time.Now().Add(-time.Minute)))

	st := f.call(t, "GET", "/media/stats", "", 200)
	if ms := int64(st["time"].(float64)); ms < time.Now().Add(-time.Minute).UnixMilli() {
		t.Errorf("time: %d", ms)
	}
	libs := st["libraries"].([]any)
	films := libs[slices.IndexFunc(libs, func(l any) bool { return l.(map[string]any)["name"] == "films" })].(map[string]any)
	if len(libs) != 5 || films["items"] != 5.0 || films["errors"] != 1.0 || films["pool"].(map[string]any)["workers"] == 0.0 ||
		films["tables"].(map[string]any)["misses"] == 0.0 {
		t.Errorf("libraries: %v", libs)
	}
	vods := st["vod"].([]any)
	v := vods[slices.IndexFunc(vods, func(l any) bool { return l.(map[string]any)["library"] == "films" })].(map[string]any)
	if v["requests"] != 4.0 || v["bytes"] == 0.0 || v["cache"].(map[string]any)["max"] != float64(256<<20) {
		t.Errorf("vod: %v", vods)
	}
	pols := st["policies"].([]any)
	site := pols[slices.IndexFunc(pols, func(p any) bool { return p.(map[string]any)["name"] == "site" })].(map[string]any)
	res := site["resources"].([]any)
	if len(pols) != 5 || site["sessions"] != 3.0 || site["refusals"].(map[string]any)["expired"] != 1.0 || len(res) != 2 ||
		res[0].(map[string]any)["resource"] != "/vod/films/films/bbb/" || res[0].(map[string]any)["sessions"] != 2.0 {
		t.Errorf("policies: %v", pols)
	}
	chs := st["channels"].([]any)
	ch := chs[0].(map[string]any)
	push := ch["push"].([]any)
	if len(chs) != 1 || ch["name"] != "tv/films" || ch["library"] != "films" || ch["players"] != 1.0 || len(push) != 1 ||
		push[0].(map[string]any)["name"] != "tv/films#1" {
		t.Errorf("channels: %v", chs)
	}
	if raw, _ := json.Marshal(st); strings.Contains(string(raw), "the-stream-key") || strings.Contains(string(raw), "sig=") {
		t.Errorf("a secret in the stats: %s", raw)
	}

	_, metrics := f.get(t, "/metrics")
	for _, line := range []string{`fit_vod_requests_total{library="films"} 4`, `fit_vod_cache_lookups_total{library="films",result="miss"}`} {
		if !strings.Contains(metrics, line) {
			t.Errorf("metrics lack %s", line)
		}
	}

	// Without libraries, policies or channels: empty lists.
	empty := start(t, `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}], "routes": [{"handler": {"module": "respond", "params": {"body": "x"}}}]}`, &config.Admin{})
	_, body := empty.get(t, "/media/stats")
	for _, want := range []string{`"libraries":[]`, `"vod":[]`, `"policies":[]`, `"channels":[]`} {
		if !strings.Contains(body, want) {
			t.Errorf("empty stats lack %s: %s", want, body)
		}
	}
}

// TestMediaConsole serves the media pages' endpoints through the console:
// its user is the preview's subject, whatever a client sends, and its
// pages may load media from the vod routes' listeners, and only then.
func TestMediaConsole(t *testing.T) {
	f := startMedia(t)
	cf := consoleOver(t, f.fixture)
	b := cf.browser(t)
	if code := b.login("ada", "pw"); code != 200 {
		t.Fatalf("login: %d", code)
	}
	code, body, hdr := b.do("POST", "/api/m/admin/media/preview", `{"library": "films", "item": "films/bbb"}`, ConsoleUserHeader, "mallory", ConsoleHostHeader, "evil.example")
	if code != 200 || !strings.Contains(body, `"subject":"workbench:ada"`) || hdr.Get("Cache-Control") != "no-store" {
		t.Fatalf("a preview through the console: %d %s %v", code, body, hdr)
	}
	if cf.logged("sig=") || cf.logged("exp=") {
		t.Errorf("the URL was logged: %v", cf.logLines())
	}
	_, _, hdr = b.do("GET", "/", "")
	csp := hdr.Get("Content-Security-Policy")
	both := f.web + " " + f.secure + ";"
	for _, want := range []string{"img-src 'self' data: " + both, "connect-src 'self' " + both, "media-src 'self' blob: " + both} {
		if !strings.Contains(csp, want) {
			t.Errorf("the policy lacks %q: %s", want, csp)
		}
	}

	// A reload is seen once it is read again: without vod routes, the
	// console's own policy.
	if resp, body := f.load(t, `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}], "routes": [{"handler": {"module": "respond", "params": {"body": "x"}}}]}`); resp.StatusCode != 200 {
		t.Fatalf("load: %d %s", resp.StatusCode, body)
	}
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		_, _, hdr = b.do("GET", "/", "")
		if !strings.Contains(hdr.Get("Content-Security-Policy"), "media-src") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the policy after a reload: %s", hdr.Get("Content-Security-Policy"))
		}
	}

	plain := startConsole(t, &config.Console{})
	_, _, hdr = plain.browser(t).do("GET", "/", "")
	if got := hdr.Get("Content-Security-Policy"); strings.Contains(got, "media-src") || !strings.Contains(got, "img-src 'self' data:;") {
		t.Errorf("a console without vod routes: %s", got)
	}
}

// consoleOver starts a console in front of f's Configs, its user ada.
func consoleOver(t *testing.T, f *fixture) *consoleFixture {
	t.Helper()
	cf := &consoleFixture{fixture: f, now: time.Now()}
	hash, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	c := &config.Console{Address: "127.0.0.1:0", Users: []config.ConsoleUser{{Username: "ada", PasswordHash: string(hash)}}}
	var err error
	cf.console, err = StartConsole(testEngine(t), c, &config.Admin{}, f.configs, nil, nil, func(format string, args ...any) {
		cf.mu.Lock()
		defer cf.mu.Unlock()
		cf.logs = append(cf.logs, strings.TrimSpace(fmtSprintf(format, args...)))
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cf.console.Shutdown(context.Background()) })
	cf.url = strings.TrimSuffix(cf.console.URL(), "/")
	return cf
}

// The hosts and origins of URLs: a listener's own host, or the console's
// Host when it listens on every address, checked strictly.
func TestMediaOrigins(t *testing.T) {
	for in, want := range map[string]string{
		"example.com:9400": "example.com", "[::1]:9400": "::1", "10.0.0.1": "10.0.0.1", "::1": "::1", "fe80::1": "fe80::1",
		"": "", "a b": "", "evil.example\r\nX: y": "", "[::1": "", "[]:80": "", "0.0.0.0:80": "", "[::]:80": "", "a:b:c": "",
		"x[y]": "", strings.Repeat("a", 300): "", strings.Repeat("a", 254) + ":1": "",
	} {
		if got := hostOf(in); got != want {
			t.Errorf("hostOf(%q) = %q, want %q", in, got, want)
		}
	}
	wild := &mediaListener{scheme: "http", port: "9401"}
	for host, want := range map[string]string{"media.example:9400": "http://media.example:9401", "[2001:db8::1]:9400": "http://[2001:db8::1]:9401", "bad host": "http://127.0.0.1:9401"} {
		if got := wild.origin(host); got != want {
			t.Errorf("origin for %q: %s, want %s", host, got, want)
		}
	}
	own := &mediaListener{scheme: "https", host: "::1", port: "8443"}
	if got := own.origin("media.example"); got != "https://[::1]:8443" {
		t.Errorf("a listener's own host: %s", got)
	}
	v := &mediaView{listeners: []*mediaListener{{name: "a"}, {name: "b"}}}
	if v.listenerFor(nil).name != "a" || v.listenerFor([]string{"b"}).name != "b" || v.listenerFor([]string{"c"}) != nil {
		t.Error("listenerFor")
	}
	if got := (&Configs{}).running(); got == nil || len(got.Routes) != 0 {
		t.Errorf("no running config: %v", got)
	}
	if got := (&Configs{rt: new(runtime.Runtime)}).graphVersion(); got != 0 {
		t.Errorf("the version of no graph: %d", got)
	}
}
