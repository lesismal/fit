package mediapages

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fib"

	"github.com/lesismal/fit/config"
	_ "github.com/lesismal/fit/handler/vod"
	"github.com/lesismal/fit/listener"
	"github.com/lesismal/fit/medialib"
	"github.com/lesismal/fit/observe"
	"github.com/lesismal/fit/runtime"
	"github.com/lesismal/fit/streaming/channels"
	"github.com/lesismal/fit/upstream"
)

// libRoot is medialib's test library: films/bbb (with a poster), sintel
// (a sidecar's), clips/late, clips/Av1-Opus-…, and broken, which does not
// probe.
const libRoot = "../../medialib/testdata/lib"

// libDoc is a config with the library films, a vod route on /vod/* and
// media_pages on /media/* of params, under the policy live: ann may play
// films/* of it, viv (a bcrypt user) everything, and the token "all"
// anything. signing is the policy's url_signing, or "".
func libDoc(params, signing string) string {
	hash, _ := bcrypt.GenerateFromPassword([]byte("viv-password"), bcrypt.MinCost)
	if signing != "" {
		signing = `, "url_signing": ` + signing
	}
	return `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "media_library": [{"name": "films", "roots": ["` + libRoot + `"]}, {"name": "other", "roots": ["` + libRoot + `/films"]}],
  "routes": [
    {"match": {"path": ["/media", "/media/*"]}, "handler": {"module": "media_pages", "params": ` + params + `}},
    {"match": {"path": ["/vod/*"]}, "handler": {"module": "vod", "params": {"library": "films", "auth": "live"}}}
  ],
  "media_auth": [{"name": "live", "secret": "a-secret-of-some-length-0123456789"` + signing + `,
    "users": [
      {"name": "ann", "password": "ann-password", "play": ["vod/films/films/*", "tv/*"]},
      {"name": "viv", "password_hash": "` + string(hash) + `", "play": ["*"]}
    ],
    "tokens": [{"name": "all", "token": "0123456789abcdef-token", "publish": ["*"], "play": ["*"]}]
  }]
}`
}

// startLib serves doc with its libraries applied, as the role does, and
// waits for their first scans unless apply is false.
func startLib(t *testing.T, doc string, apply bool) *fit {
	t.Helper()
	up, stopUp, err := upstream.StartEngine()
	if err != nil {
		t.Fatal(err)
	}
	f := &fit{env: runtime.Env{Metrics: observe.NewMetrics(), Engine: up}, rt: new(runtime.Runtime)}
	c, err := config.Load([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	g, err := runtime.Compile(c, f.env)
	if err != nil {
		t.Fatal(err)
	}
	if apply {
		if err := medialib.Apply(up, c.MediaLibrary, nil); err != nil {
			t.Fatal(err)
		}
	}
	f.rt.Swap(g)
	if f.server, err = listener.Start(f.rt); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = f.server.Close()
		f.rt.Close()
		medialib.Remove(up)
		stopUp()
	})
	f.base = "http://" + f.server.Addrs()[0].Addr.String()
	if apply {
		for _, name := range []string{"films", "other"} {
			l := medialib.Lookup(up, name)
			eventually(t, "the scan of "+name, func() bool { st := l.Status(); return st.Scans >= 1 && !st.Scanning })
		}
	}
	return f
}

type pageJSON struct {
	Library string     `json:"library"`
	Total   int        `json:"total"`
	Offset  int        `json:"offset"`
	Items   []itemJSON `json:"items"`
}

func ids(p pageJSON) string {
	var s []string
	for _, it := range p.Items {
		s = append(s, it.ID)
	}
	return strings.Join(s, ",")
}

func getPage(t *testing.T, f *fit, query string, hdr map[string]string) (int, pageJSON) {
	t.Helper()
	resp, body := do(t, "GET", f.base+"/media/api/library/films"+query, "", hdr)
	var p pageJSON
	if resp.StatusCode == 200 {
		if err := json.Unmarshal([]byte(body), &p); err != nil {
			t.Fatalf("%s: %v", body, err)
		}
	}
	return resp.StatusCode, p
}

type playJSON struct {
	URL     string `json:"url"`
	Expires int64  `json:"expires"`
	Signed  bool   `json:"signed"`
	Title   string `json:"title"`
	Kind    string `json:"kind"`
}

func play(t *testing.T, f *fit, body string, hdr map[string]string) (int, playJSON, string) {
	t.Helper()
	resp, out := do(t, "POST", f.base+"/media/api/play", body, hdr)
	var p playJSON
	if resp.StatusCode == 200 {
		if err := json.Unmarshal([]byte(out), &p); err != nil {
			t.Fatalf("%s: %v", out, err)
		}
	}
	return resp.StatusCode, p, out
}

const libParams = `{"auth": "live", "login": true, "libraries": ["films", "other"]}`

func TestLibraryPages(t *testing.T) {
	f := startLib(t, libDoc(libParams, ""), true)

	for _, p := range []string{"/media/library", "/media/watch"} {
		if resp, body := do(t, "GET", f.base+p+"?library=films&id=films/bbb", "", nil); resp.StatusCode != 200 || !strings.Contains(body, `id="view-library"`) {
			t.Fatalf("%s: %d", p, resp.StatusCode)
		}
	}
	resp, body := do(t, "GET", f.base+"/media/vendor/hls.min.js", "", nil)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/javascript; charset=utf-8" || !strings.Contains(body, "Hls") {
		t.Fatalf("hls.min.js: %d %v", resp.StatusCode, resp.Header)
	}
	if resp, body := do(t, "GET", f.base+"/media/vendor/hls.js-LICENSE.txt", "", nil); resp.StatusCode != 200 || !strings.Contains(body, "Apache License, Version 2.0") {
		t.Fatalf("the license: %d %q", resp.StatusCode, body)
	}
	resp, _ = do(t, "GET", f.base+"/media/", "", nil)
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "worker-src 'self' blob:") {
		t.Errorf("CSP %q", csp)
	}
	_, body = do(t, "GET", f.base+"/media/api/config", "", nil)
	if !strings.Contains(body, `"library":{"channels":false,"libraries":["films","other"]}`) {
		t.Errorf("config %s", body)
	}

	// Without credentials, nothing is listed.
	if resp, _ := do(t, "GET", f.base+"/media/api/library", "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no credentials: %d", resp.StatusCode)
	}
	ann := basic("ann", "ann-password")
	resp, body = do(t, "GET", f.base+"/media/api/library", "", ann)
	if resp.StatusCode != 200 || !strings.Contains(body, `"subject":"ann"`) || !strings.Contains(body, `{"name":"films","ready":true,"scanned":`) ||
		!strings.Contains(body, `"channels":[]`) {
		t.Fatalf("overview: %d %s", resp.StatusCode, body)
	}

	// ann sees the items she may play, those under films/; the token all
	// sees every item that can be served, broken not among them.
	if code, p := getPage(t, f, "", ann); code != 200 || ids(p) != "films/bbb" || p.Total != 1 {
		t.Fatalf("ann: %d %+v", code, p)
	} else if it := p.Items[0]; it.Poster != "/vod/films/films/bbb/poster.jpg" || it.Title != "bbb" || it.Duration != 1 ||
		it.Width != 128 || it.Height != 96 || len(it.Renditions) != 2 || it.Audio != 1 || it.Subtitles != 2 {
		t.Errorf("bbb: %+v", it)
	}
	code, p := getPage(t, f, "", bearer)
	if code != 200 || p.Total != 4 || !strings.HasPrefix(ids(p), "clips/Av1-Opus-") || !strings.HasSuffix(ids(p), ",clips/late,films/bbb,sintel") {
		t.Fatalf("all: %d %s", code, ids(p))
	}
	if code, p := getPage(t, f, "?offset=1&limit=2", bearer); code != 200 || ids(p) != "clips/late,films/bbb" || p.Total != 4 || p.Offset != 1 {
		t.Errorf("a page: %d %+v", code, p)
	}
	if code, p := getPage(t, f, "?q=SINT", bearer); code != 200 || ids(p) != "sintel" || p.Items[0].Poster != "/vod/films/sintel/poster.png" {
		t.Errorf("q: %d %+v", code, p)
	}
	if code, p := getPage(t, f, "?offset=9", bearer); code != 200 || p.Total != 4 || len(p.Items) != 0 {
		t.Errorf("past the end: %d %+v", code, p)
	}
	for _, q := range []string{"?offset=-1", "?limit=0", "?limit=501", "?offset=x", "?q=" + strings.Repeat("a", 129)} {
		if code, _ := getPage(t, f, q, bearer); code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, code)
		}
	}
	// viv's password is a bcrypt hash, checked on mediaauth's pool.
	viv := basic("viv", "viv-password")
	if code, p := getPage(t, f, "", viv); code != 200 || p.Total != 4 {
		t.Errorf("viv: %d %+v", code, p)
	}
	if code, _ := getPage(t, f, "", basic("viv", "wrong")); code != http.StatusUnauthorized {
		t.Errorf("a wrong password: %d", code)
	}
	for _, c := range []struct {
		method, path string
		want         int
	}{
		{"POST", "/media/api/library", http.StatusMethodNotAllowed},
		{"GET", "/media/api/library/nothing", http.StatusNotFound},
		{"GET", "/media/api/libraryx", http.StatusNotFound},
		{"GET", "/media/api/library/", http.StatusNotFound},
		{"GET", "/media/api/play", http.StatusMethodNotAllowed},
	} {
		if resp, _ := do(t, c.method, f.base+c.path, "", bearer); resp.StatusCode != c.want {
			t.Errorf("%s %s: %d, want %d", c.method, c.path, resp.StatusCode, c.want)
		}
	}

	// The login's cookie plays what api/play answers: the plain URL, as
	// the policy signs none.
	resp, body = do(t, "POST", f.base+"/media/api/login", `{"user": "ann", "password": "ann-password"}`, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("login: %d %s", resp.StatusCode, body)
	}
	cookie := map[string]string{"Cookie": strings.Split(resp.Header.Get("Set-Cookie"), ";")[0]}
	code, pl, out := play(t, f, `{"library": "films", "id": "films/bbb"}`, cookie)
	if code != 200 || pl.URL != "/vod/films/films/bbb/index.m3u8" || pl.Signed || pl.Expires != 0 || pl.Title != "bbb" || pl.Kind != "vod" {
		t.Fatalf("play: %d %s", code, out)
	}
	if resp, body := do(t, "GET", f.base+pl.URL, "", cookie); resp.StatusCode != 200 || !strings.Contains(body, "#EXTM3U") {
		t.Fatalf("the playlist with the cookie: %d %s", resp.StatusCode, body)
	}
	if resp, _ := do(t, "GET", f.base+pl.URL, "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the playlist without: %d", resp.StatusCode)
	}
	for _, c := range []struct {
		body string
		hdr  map[string]string
		want int
	}{
		{`{"library": "films", "id": "sintel"}`, cookie, http.StatusForbidden},
		{`{"library": "films", "id": "films/bbb"}`, nil, http.StatusUnauthorized},
		{`{"library": "films", "id": "nothing"}`, cookie, http.StatusNotFound},
		{`{"library": "films", "id": "broken"}`, bearer, http.StatusNotFound},
		{`{"library": "nothing", "id": "films/bbb"}`, cookie, http.StatusNotFound},
		{`{"channel": "tv/films"}`, cookie, http.StatusNotFound},
		{`{"library": "films"}`, cookie, http.StatusBadRequest},
		{`{"library": "films", "id": "films/bbb", "channel": "tv"}`, cookie, http.StatusBadRequest},
		{`nonsense`, cookie, http.StatusBadRequest},
	} {
		if code, _, out := play(t, f, c.body, c.hdr); code != c.want {
			t.Errorf("play %s: %d %s, want %d", c.body, code, out, c.want)
		}
	}
	if code, pl, _ := play(t, f, `{"library": "other", "id": "sintel"}`, bearer); code != 200 || pl.URL != "/vod/other/sintel/index.m3u8" {
		t.Errorf("the other library: %d %+v", code, pl)
	}
}

func TestLibrarySigned(t *testing.T) {
	f := startLib(t, libDoc(libParams, `{"ttl": "1m", "play_grace": "1m"}`), true)
	ann := basic("ann", "ann-password")
	start := time.Now()
	code, pl, out := play(t, f, `{"library": "films", "id": "films/bbb"}`, ann)
	if code != 200 || !pl.Signed || pl.Expires < start.Add(59*time.Second).Unix() || pl.Expires > time.Now().Add(61*time.Second).Unix() {
		t.Fatalf("play: %d %s", code, out)
	}
	u, err := url.Parse(pl.URL)
	if err != nil || u.Path != "/vod/films/films/bbb/index.m3u8" {
		t.Fatalf("the URL %q", pl.URL)
	}
	q := u.Query()
	if q.Get("sub") != "ann" || q.Get("ip") != "127.0.0.1" || len(q.Get("sid")) != 16 || q.Get("sig") == "" || q.Get("kid") != "" {
		t.Fatalf("the URL's query %v", q)
	}
	// The signed URL plays with no other credentials, and hands its grant
	// on; another session is signed each time.
	if resp, body := do(t, "GET", f.base+pl.URL, "", nil); resp.StatusCode != 200 || !strings.Contains(body, "g=1") {
		t.Fatalf("the signed playlist: %d %s", resp.StatusCode, body)
	}
	if _, pl2, _ := play(t, f, `{"library": "films", "id": "films/bbb"}`, ann); strings.Contains(pl2.URL, q.Get("sid")) {
		t.Errorf("the same session twice: %s", pl2.URL)
	}

	// A URL signed to expire before now is refused.
	now = func() time.Time { return time.Now().Add(-2 * time.Minute) }
	t.Cleanup(func() { now = time.Now })
	_, old, _ := play(t, f, `{"library": "films", "id": "films/bbb"}`, ann)
	if resp, body := do(t, "GET", f.base+old.URL, "", nil); resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "expired") {
		t.Fatalf("an expired URL: %d %s", resp.StatusCode, body)
	}
}

func TestLibraryNotScanned(t *testing.T) {
	f := startLib(t, libDoc(libParams, ""), false)
	resp, body := do(t, "GET", f.base+"/media/api/library", "", bearer)
	if resp.StatusCode != 200 || !strings.Contains(body, `{"name":"films","ready":false}`) {
		t.Fatalf("overview: %d %s", resp.StatusCode, body)
	}
	if resp, _ := do(t, "GET", f.base+"/media/api/library/films", "", bearer); resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") != "1" {
		t.Errorf("items: %d", resp.StatusCode)
	}
	if code, _, _ := play(t, f, `{"library": "films", "id": "films/bbb"}`, bearer); code != http.StatusServiceUnavailable {
		t.Errorf("play: %d", code)
	}
}

func TestLibraryChannels(t *testing.T) {
	list := []channels.ChannelStatus{
		{Name: "tv/films", State: "playing", Now: &channels.ItemStatus{Title: "Big Buck Bunny"}, Next: &channels.ItemStatus{Title: "Sintel"}},
		{Name: "secret/news", State: "idle"},
	}
	channelList = func(*fib.Engine) []channels.ChannelStatus { return list }
	t.Cleanup(func() { channelList = defaultChannelList })
	params := `{"auth": "live", "channels": true, "hls": "/hls/{stream}/index.m3u8?format=ts", "vod": "https://cdn.example.com/v/{library}/{id}/master.m3u8"}`
	f := startLib(t, libDoc(params, `{}`), false)
	ann := basic("ann", "ann-password")
	resp, body := do(t, "GET", f.base+"/media/api/library", "", ann)
	if resp.StatusCode != 200 || !strings.Contains(body, `"libraries":[],"channels":[{"name":"tv/films","state":"playing","now":"Big Buck Bunny","next":"Sintel"}]`) {
		t.Fatalf("overview: %d %s", resp.StatusCode, body)
	}
	if resp, body := do(t, "GET", f.base+"/media/api/library", "", bearer); !strings.Contains(body, `{"name":"secret/news","state":"idle"}`) {
		t.Fatalf("all: %d %s", resp.StatusCode, body)
	}
	code, pl, out := play(t, f, `{"channel": "tv/films"}`, ann)
	if code != 200 || !pl.Signed || pl.Kind != "channel" || !strings.HasPrefix(pl.URL, "/hls/tv/films/index.m3u8?format=ts&exp=") {
		t.Fatalf("play: %d %s", code, out)
	}
	// A channel whose path no URL can be signed for is an error of the
	// config, not the viewer's.
	list = append(list, channels.ChannelStatus{Name: "tv/a b", State: "idle"})
	if code, _, out := play(t, f, `{"channel": "tv/a b"}`, ann); code != http.StatusInternalServerError {
		t.Errorf("an unsignable channel: %d %s", code, out)
	}
	for body, want := range map[string]int{`{"channel": "secret/news"}`: http.StatusForbidden, `{"channel": "tv/none"}`: http.StatusNotFound,
		`{"library": "films", "id": "films/bbb"}`: http.StatusNotFound} {
		if code, _, out := play(t, f, body, ann); code != want {
			t.Errorf("%s: %d %s, want %d", body, code, out, want)
		}
	}
	if resp, _ := do(t, "GET", f.base+"/media/api/library/films", "", ann); resp.StatusCode != http.StatusNotFound {
		t.Errorf("a library not listed: %d", resp.StatusCode)
	}

	// Each listing is bounded.
	list = nil
	for i := 0; i < maxListedChannels+5; i++ {
		list = append(list, channels.ChannelStatus{Name: "tv/c", State: "idle"})
	}
	_, body = do(t, "GET", f.base+"/media/api/library", "", ann)
	if n := strings.Count(body, `"tv/c"`); n != maxListedChannels {
		t.Errorf("%d channels listed", n)
	}

	// The streaming role's channels, of an engine without the role: none.
	if got := defaultChannelList(nil); got != nil {
		t.Errorf("no role: %v", got)
	}
	m := channels.New(channels.Options{})
	channels.Register(f.env.Engine, m)
	t.Cleanup(func() { channels.Unregister(f.env.Engine, m) })
	if got := defaultChannelList(f.env.Engine); len(got) != 0 {
		t.Errorf("a role without channels: %v", got)
	}
}

func TestLibraryPublicAndNoPolicy(t *testing.T) {
	doc := strings.Replace(libDoc(`{"libraries": ["films"]}`, ""), `"secret"`, `"public": ["play"], "secret"`, 1)
	f := startLib(t, doc, true)
	if code, p := getPage(t, f, "", nil); code != 200 || p.Total != 4 {
		t.Fatalf("public: %d %+v", code, p)
	}
	if code, pl, _ := play(t, f, `{"library": "films", "id": "sintel"}`, nil); code != 200 || pl.Signed {
		t.Errorf("play: %d %+v", code, pl)
	}
}

func TestLibraryOff(t *testing.T) {
	f := startLib(t, libDoc(`{"auth": "live"}`, ""), false)
	_, body := do(t, "GET", f.base+"/media/api/config", "", nil)
	if !strings.Contains(body, `"library":null`) {
		t.Errorf("config %s", body)
	}
	if resp, _ := do(t, "GET", f.base+"/media/api/library", "", bearer); resp.StatusCode != http.StatusNotFound {
		t.Errorf("library: %d", resp.StatusCode)
	}
	if code, _, _ := play(t, f, `{"channel": "tv/films"}`, bearer); code != http.StatusNotFound {
		t.Errorf("play: %d", code)
	}
}

func TestLibraryParams(t *testing.T) {
	for params, want := range map[string]string{
		`{"libraries": ["nothing"]}`:        `there is no media_library named "nothing"`,
		`{"libraries": ["films", "films"]}`: `"films" is listed twice`,
		`{"libraries": ["a","b","c","d","e","f","g","h","i","j","k","l","m","n","o","p","q"]}`: "17 libraries: at most 16",
		`{"vod": "/v/{id}/index.m3u8"}`:                           "name libraries too",
		`{"channels": true}`:                                      "set hls",
		`{"libraries": ["films"], "vod": "/v/{id}"}`:              "is not a path",
		`{"libraries": ["films"], "vod": "/v/{library}/x.m3u8"}`:  "is not a path",
		`{"libraries": ["films"], "vod": "/v/{id}/{id}/x.m3u8"}`:  "is not a path",
		`{"libraries": ["films"], "vod": "/v/{id}/{library}"}`:    "is not a path",
		`{"libraries": ["films"], "vod": "/v/{id}/x.m3u8?a=b"}`:   "is not a path",
		`{"libraries": ["films"], "vod": "ftp://h/{id}/x.m3u8"}`:  "is not a path",
		`{"libraries": ["films"], "vod": "https:///{id}/x.m3u8"}`: "is not a path",
	} {
		c, err := config.Load([]byte(libDoc(params, "")))
		if err == nil {
			_, err = runtime.Compile(c, runtime.Env{Metrics: observe.NewMetrics()})
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", params, err, want)
		}
	}
	// vod on another server joins the pages' Content-Security-Policy.
	f := startLib(t, libDoc(`{"libraries": ["films"], "vod": "https://cdn.example.com/v/{library}/{id}/index.m3u8"}`, ""), false)
	resp, _ := do(t, "GET", f.base+"/media/", "", nil)
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "img-src 'self' data: blob: https://cdn.example.com;") {
		t.Errorf("CSP %q", csp)
	}
}
