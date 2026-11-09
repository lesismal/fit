package admin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fib"
	"github.com/lesismal/fib/bittorrent/bencode"
	"github.com/lesismal/fib/bittorrent/metainfo"
	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/bittorrent"
	"github.com/lesismal/fit/bittorrent/tracker"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/observe"
	"github.com/lesismal/fit/runtime"
)

// btHang bounds the waits of these tests: a guard against a hang, never a
// measure of speed.
const btHang = 90 * time.Second

// btTorrentFile writes a file of n random bytes as dir/name and returns its
// .torrent file.
func btTorrentFile(t *testing.T, dir, name string, n int, announce ...string) []byte {
	t.Helper()
	data := make([]byte, n)
	rnd := rand.New(rand.NewPCG(uint64(n), uint64(len(name))))
	for i := range data {
		data[i] = byte(rnd.Uint32())
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	b := metainfo.Builder{Version: metainfo.Hybrid, PieceLength: 32 << 10}
	for _, a := range announce {
		b.Trackers = append(b.Trackers, []string{a})
	}
	mi, err := b.BuildPath(p)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := bencode.Marshal(mi)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// btFixture is an Admin API with a BitTorrent client role, and a tracker
// role, on its engine.
type btFixture struct {
	*fixture
	engine  *fib.Engine
	role    *bittorrent.Role
	tracker *tracker.Role
	dl      string
}

// startBTRoles starts the roles on e, the client's member extra more of
// its fields.
func startBTRoles(t *testing.T, e *fib.Engine, extra string) (*bittorrent.Role, *tracker.Role, string) {
	t.Helper()
	dl := t.TempDir()
	js := `{"listen": "127.0.0.1:0", "data_dir": ` + strconv.Quote(t.TempDir()) + `, "download_dir": ` + strconv.Quote(dl) +
		`, "dht": false, "save_interval": "50ms", "categories": [{"name": "films", "dir": "films"}], "alt_speed": {"upload_rate": 1000}` + extra + `}`
	c, err := config.Load([]byte(`{"bittorrent": {"client": ` + js + `, "tracker": {"listen": "127.0.0.1:0"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	r := bittorrent.New(&app.Host{ID: bittorrent.ID, Engine: e, Upstream: e})
	if err := r.Apply(c.BitTorrent.Client); err != nil {
		r.Stop(context.Background())
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Stop(context.Background()) })
	tr := tracker.New(&app.Host{ID: tracker.ID, Engine: e, Upstream: e})
	if err := tr.Apply(c.BitTorrent.Tracker); err != nil {
		tr.Stop(context.Background())
		t.Fatal(err)
	}
	t.Cleanup(func() { tr.Stop(context.Background()) })
	return r, tr, dl
}

func startBT(t *testing.T, a *config.Admin, extra string) *btFixture {
	t.Helper()
	e := testEngine(t)
	f := &btFixture{fixture: startEnv(t, doc("x"), a, runtime.Env{Metrics: observe.NewMetrics(), Engine: e}), engine: e}
	f.role, f.tracker, f.dl = startBTRoles(t, e, extra)
	return f
}

// call sends a request with body of contentType and decodes its JSON
// answer, which must have status want.
func (f *btFixture) call(t *testing.T, method, path, contentType string, body []byte, want int) map[string]any {
	t.Helper()
	var in io.Reader
	if body != nil {
		in = bytes.NewReader(body)
	}
	req, _ := http.NewRequest(method, f.api+path, in)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, got := f.do(t, req)
	if resp.StatusCode != want {
		t.Fatalf("%s %s: %d %s, want %d", method, path, resp.StatusCode, got, want)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(got), &v); err != nil {
		t.Fatalf("%s %s: %v %q", method, path, err, got)
	}
	return v
}

func (f *btFixture) json(t *testing.T, method, path, body string, want int) map[string]any {
	t.Helper()
	var b []byte
	ct := ""
	if body != "" {
		b, ct = []byte(body), "application/json"
	}
	return f.call(t, method, path, ct, b, want)
}

// TestBitTorrentWithoutRoles answers 404 for every endpoint while the
// roles do not run.
func TestBitTorrentWithoutRoles(t *testing.T) {
	f := &btFixture{fixture: startEnv(t, doc("x"), &config.Admin{}, runtime.Env{Metrics: observe.NewMetrics(), Engine: testEngine(t)})}
	id := strings.Repeat("ab", 20)
	for _, r := range []struct{ method, path, body string }{
		{"GET", "/bittorrent/torrents", ""}, {"POST", "/bittorrent/torrents", `{"magnet": "x"}`},
		{"GET", "/bittorrent/torrents/" + id, ""}, {"POST", "/bittorrent/torrents/" + id, `{"action": "pause"}`},
		{"PATCH", "/bittorrent/torrents/" + id, `{}`}, {"DELETE", "/bittorrent/torrents/" + id, ""},
		{"GET", "/bittorrent/torrents/" + id + "/files", ""}, {"GET", "/bittorrent/torrents/" + id + "/peers", ""},
		{"GET", "/bittorrent/torrents/" + id + "/trackers", ""}, {"GET", "/bittorrent/alt_speed", ""},
		{"PUT", "/bittorrent/alt_speed", `{"mode": "on"}`}, {"GET", "/bittorrent/publish", ""}, {"GET", "/bittorrent/events", ""},
		{"GET", "/bittorrent/tracker/stats", ""}, {"GET", "/bittorrent/tracker/torrents", ""},
		{"GET", "/bittorrent/tracker/torrents/" + id, ""}, {"GET", "/bittorrent/tracker/passkey?user=a", ""},
		{"GET", "/bittorrent/rss", ""}, {"GET", "/bittorrent/rss/items", ""}, {"POST", "/bittorrent/rss/feeds/a/poll", ""},
		{"PUT", "/bittorrent/rss/feeds/a", `{"enabled": false}`}, {"PUT", "/bittorrent/rss/rules/a", `{"enabled": false}`},
	} {
		v := f.json(t, r.method, r.path, r.body, http.StatusNotFound)
		if !strings.Contains(v["error"].(string), "does not run here") {
			t.Errorf("%s %s: %v", r.method, r.path, v)
		}
	}
	if _, body := f.get(t, "/node"); !strings.Contains(body, `"`+CapBitTorrent+`"`) {
		t.Errorf("/node: %s", body)
	}
}

// TestBitTorrentAuth refuses the endpoints without the Admin API's token.
func TestBitTorrentAuth(t *testing.T) {
	tok := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tok, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := startBT(t, &config.Admin{TokenFile: tok}, "")
	for _, p := range []string{"/bittorrent/torrents", "/bittorrent/events", "/bittorrent/tracker/stats"} {
		if resp, body := f.get(t, p); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s without the token: %d %s", p, resp.StatusCode, body)
		}
	}
	req, _ := http.NewRequest("GET", f.api+"/bittorrent/torrents", nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	if resp, body := f.do(t, req); resp.StatusCode != http.StatusOK {
		t.Fatalf("with the token: %d %s", resp.StatusCode, body)
	}
}

// TestBitTorrentTorrents drives the torrents' endpoints: adding in every
// form, listing and filtering, a torrent's status, files, peers and
// trackers, actions, changes and removal, with their errors.
func TestBitTorrentTorrents(t *testing.T) {
	f := startBT(t, &config.Admin{}, `, "limits": {"max_torrents": 6}`)
	// The files where their categories download to, as if downloaded.
	films := filepath.Join(f.dl, "films")
	tA := btTorrentFile(t, films, "a.bin", 200000, "udp://tracker.example:6969/announce")
	tB := btTorrentFile(t, f.dl, "b.bin", 150000)
	tC := btTorrentFile(t, films, "c.bin", 100000)
	tD := btTorrentFile(t, f.dl, "d.bin", 90000)

	// JSON, a magnet link.
	const hash = "0123456789abcdef0123456789abcdef01234567"
	magnet := "magnet:?xt=urn:btih:" + hash + "&dn=film&tr=http%3A%2F%2Ft.example%2Fannounce"
	v := f.json(t, "POST", "/bittorrent/torrents", `{"magnet": `+strconv.Quote(magnet)+`, "paused": true, "category": "films"}`, http.StatusCreated)
	if v["id"] != hash || v["category"] != "films" || v["paused"] != true {
		t.Fatalf("magnet %v", v)
	}
	// A .torrent file as application/x-bittorrent, its options in the query.
	v = f.call(t, "POST", "/bittorrent/torrents?category=films&sequential=1", "application/x-bittorrent", tA, http.StatusCreated)
	idA := v["id"].(string)
	if v["sequential"] != true || v["category"] != "films" || v["name"] != "a.bin" {
		t.Fatalf("raw %v", v)
	}
	// JSON with the file in base64 and limits.
	v = f.json(t, "POST", "/bittorrent/torrents", `{"torrent": "`+base64.StdEncoding.EncodeToString(tB)+`", "paused": true,
		"limits": {"upload_rate": 1000, "seed_time": "12h", "seed_ratio": 1.5}, "priorities": [4]}`, http.StatusCreated)
	idB := v["id"].(string)
	lim := v["limits"].(map[string]any)
	if lim["upload_rate"] != 1000.0 || lim["seed_time"] != float64(12*time.Hour) || lim["seed_ratio"] != 1.5 {
		t.Fatalf("limits %v", v)
	}
	// A form.
	var form bytes.Buffer
	mw := multipart.NewWriter(&form)
	fw, _ := mw.CreateFormFile("torrent", "c.torrent")
	_, _ = fw.Write(tC)
	_ = mw.WriteField("category", "films")
	_ = mw.WriteField("paused", "true")
	_ = mw.Close()
	v = f.call(t, "POST", "/bittorrent/torrents", mw.FormDataContentType(), form.Bytes(), http.StatusCreated)
	idC := v["id"].(string)
	if v["paused"] != true || v["category"] != "films" {
		t.Fatalf("form %v", v)
	}
	// A URL.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/d.torrent" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/x-bittorrent")
		_, _ = w.Write(tD)
	}))
	defer srv.Close()
	v = f.json(t, "POST", "/bittorrent/torrents", `{"url": "`+srv.URL+`/d.torrent", "paused": true}`, http.StatusCreated)
	idD := v["id"].(string)
	if v["name"] != "d.bin" {
		t.Fatalf("url %v", v)
	}

	// The add's errors.
	for _, c := range []struct {
		ct, body string
		want     int
	}{
		{"application/x-bittorrent", string(tA), http.StatusConflict},
		{"application/json", `{"magnet": "magnet:?xt=urn:btih:nothex"}`, http.StatusBadRequest},
		{"application/json", `{}`, http.StatusBadRequest},
		{"application/json", `{"magnet": ` + strconv.Quote(magnet) + `, "url": "http://x/"}`, http.StatusBadRequest},
		{"application/json", `{"magnet": `, http.StatusBadRequest},
		{"application/json", `{"url": "ftp://x/a.torrent"}`, http.StatusBadRequest},
		{"application/json", `{"url": "` + srv.URL + `/missing.torrent"}`, http.StatusBadGateway},
		{"application/json", `{"magnet": "magnet:?xt=urn:btih:` + strings.Repeat("1", 40) + `", "category": "nope"}`, http.StatusBadRequest},
		{"application/json", `{"magnet": "magnet:?xt=urn:btih:` + strings.Repeat("1", 40) + `", "limits": {"seed_time": "soon"}}`, http.StatusBadRequest},
		{"application/json", `{"magnet": "magnet:?xt=urn:btih:` + strings.Repeat("1", 40) + `", "limits": {"seed_time": true}}`, http.StatusBadRequest},
		{"application/x-bittorrent", "not a torrent", http.StatusBadRequest},
		{"application/x-bittorrent?", "x", http.StatusUnsupportedMediaType},
		{"text/plain", "magnet:?xt=urn:btih:" + hash, http.StatusUnsupportedMediaType},
		{"multipart/form-data", "x", http.StatusBadRequest},
	} {
		f.call(t, "POST", "/bittorrent/torrents", c.ct, []byte(c.body), c.want)
	}
	f.call(t, "POST", "/bittorrent/torrents?paused=maybe", "application/x-bittorrent", tA, http.StatusBadRequest)
	// A form whose field passes its bound, and one with too many parts.
	form.Reset()
	mw = multipart.NewWriter(&form)
	_ = mw.WriteField("magnet", strings.Repeat("m", maxFormFieldBytes+1))
	_ = mw.Close()
	f.call(t, "POST", "/bittorrent/torrents", mw.FormDataContentType(), form.Bytes(), http.StatusRequestEntityTooLarge)
	form.Reset()
	mw = multipart.NewWriter(&form)
	for i := range maxFormParts + 1 {
		_ = mw.WriteField("x"+strconv.Itoa(i), "y")
	}
	_ = mw.Close()
	f.call(t, "POST", "/bittorrent/torrents", mw.FormDataContentType(), form.Bytes(), http.StatusBadRequest)
	form.Reset()
	mw = multipart.NewWriter(&form)
	_ = mw.WriteField("magnet", magnet)
	_ = mw.WriteField("sequential", "sometimes")
	_ = mw.Close()
	f.call(t, "POST", "/bittorrent/torrents", mw.FormDataContentType(), form.Bytes(), http.StatusBadRequest)
	// A .torrent file past the bound, its length not told.
	req, _ := http.NewRequest("POST", f.api+"/bittorrent/torrents", io.LimitReader(zeros{}, maxUploadBytes+1))
	req.Header.Set("Content-Type", "application/x-bittorrent")
	if resp, body := f.do(t, req); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("past the bound: %d %s", resp.StatusCode, body)
	}
	// Told.
	req, _ = http.NewRequest("POST", f.api+"/bittorrent/torrents", bytes.NewReader(nil))
	req.Header.Set("Content-Type", "application/x-bittorrent")
	req.ContentLength, req.Body = maxUploadBytes+1, io.NopCloser(io.LimitReader(zeros{}, maxUploadBytes+1))
	if resp, body := f.do(t, req); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("past the bound, told: %d %s", resp.StatusCode, body)
	}
	// max_torrents (6): five held, one more, then none.
	f.json(t, "POST", "/bittorrent/torrents", `{"magnet": "magnet:?xt=urn:btih:`+strings.Repeat("2", 40)+`", "paused": true}`, http.StatusCreated)
	f.json(t, "POST", "/bittorrent/torrents", `{"magnet": "magnet:?xt=urn:btih:`+strings.Repeat("3", 40)+`", "paused": true}`, http.StatusTooManyRequests)

	// The list.
	list := f.json(t, "GET", "/bittorrent/torrents", "", http.StatusOK)
	if list["total"] != 6.0 || len(list["torrents"].([]any)) != 6 {
		t.Fatalf("list %v", list)
	}
	names := func(v map[string]any) []string {
		var out []string
		for _, x := range v["torrents"].([]any) {
			out = append(out, x.(map[string]any)["id"].(string))
		}
		return out
	}
	if got := names(f.json(t, "GET", "/bittorrent/torrents?category=films", "", http.StatusOK)); len(got) != 3 || got[0] != hash || got[1] != idA || got[2] != idC {
		t.Fatalf("films %v", got)
	}
	if got := names(f.json(t, "GET", "/bittorrent/torrents?category=", "", http.StatusOK)); len(got) != 3 {
		t.Fatalf("no category %v", got)
	}
	if got := names(f.json(t, "GET", "/bittorrent/torrents?q=B.BIN", "", http.StatusOK)); len(got) != 1 || got[0] != idB {
		t.Fatalf("q %v", got)
	}
	if got := names(f.json(t, "GET", "/bittorrent/torrents?state=paused,stopped", "", http.StatusOK)); len(got) < 4 {
		t.Fatalf("paused %v", got)
	}
	page := f.json(t, "GET", "/bittorrent/torrents?offset=4&limit=1", "", http.StatusOK)
	if page["total"] != 6.0 || page["offset"] != 4.0 || len(names(page)) != 1 || names(page)[0] != idD {
		t.Fatalf("page %v", page)
	}
	if got := f.json(t, "GET", "/bittorrent/torrents?offset=99", "", http.StatusOK); len(got["torrents"].([]any)) != 0 {
		t.Fatalf("past the end %v", got)
	}
	f.json(t, "GET", "/bittorrent/torrents?state=sleeping", "", http.StatusBadRequest)
	f.json(t, "GET", "/bittorrent/torrents?limit=0", "", http.StatusBadRequest)
	f.json(t, "GET", "/bittorrent/torrents?offset=-1", "", http.StatusBadRequest)

	// One torrent, by its ID in any case, its files, peers and trackers.
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		for deadline := time.Now().Add(btHang); !cond(); time.Sleep(10 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
		}
	}
	waitFor("a.bin seeding", func() bool {
		return f.json(t, "GET", "/bittorrent/torrents/"+strings.ToUpper(idA), "", http.StatusOK)["state"] == "seeding"
	})
	one := f.json(t, "GET", "/bittorrent/torrents/"+idA, "", http.StatusOK)
	if files := one["files"].([]any); len(files) != 1 || files[0].(map[string]any)["path"] != "a.bin" {
		t.Fatalf("files %v", one)
	}
	if v := f.json(t, "GET", "/bittorrent/torrents/"+hash, "", http.StatusOK); v["state"] != "paused" {
		t.Fatalf("the magnet %v", v)
	}
	if v := f.json(t, "GET", "/bittorrent/torrents/"+idA+"/files", "", http.StatusOK); len(v["files"].([]any)) != 1 {
		t.Fatalf("files %v", v)
	}
	if v := f.json(t, "GET", "/bittorrent/torrents/"+hash+"/files", "", http.StatusOK); len(v["files"].([]any)) != 0 {
		t.Fatalf("a magnet's files %v", v)
	}
	if v := f.json(t, "GET", "/bittorrent/torrents/"+idA+"/peers", "", http.StatusOK); len(v["peers"].([]any)) != 0 {
		t.Fatalf("peers %v", v)
	}
	if v := f.json(t, "GET", "/bittorrent/torrents/"+idA+"/trackers", "", http.StatusOK); len(v["trackers"].([]any)) != 1 ||
		v["trackers"].([]any)[0].(map[string]any)["url"] != "udp://tracker.example:6969/announce" {
		t.Fatalf("trackers %v", v)
	}
	// Each tracker's announces (I210): a hybrid's v2 hash too, its sources
	// in fib's order, no web seeds.
	waitFor("the tracker tried", func() bool {
		v := f.json(t, "GET", "/bittorrent/torrents/"+idA+"/trackers", "", http.StatusOK)
		tr := v["trackers"].([]any)[0].(map[string]any)
		return tr["fails"].(float64) > 0 && tr["error"] != nil && tr["updating"] == false
	})
	v = f.json(t, "GET", "/bittorrent/torrents/"+idA+"/trackers", "", http.StatusOK)
	tr0 := v["trackers"].([]any)[0].(map[string]any)
	if tr0["tier"] != 0.0 || tr0["seeders"] != -1.0 || tr0["last_announce"] == nil || tr0["refused"] != nil || tr0["v2"] == nil {
		t.Fatalf("an unreachable tracker %v", tr0)
	}
	if srcs := v["sources"].([]any); len(srcs) != 6 || srcs[0].(map[string]any)["source"] != "tracker" || srcs[0].(map[string]any)["enabled"] != true {
		t.Fatalf("sources %v", v["sources"])
	}
	if ws, ok := v["web_seeds"].([]any); !ok || len(ws) != 0 {
		t.Fatalf("web seeds %v", v["web_seeds"])
	}
	if v := f.json(t, "GET", "/bittorrent/torrents/"+hash+"/trackers", "", http.StatusOK); len(v["trackers"].([]any)) != 1 {
		t.Fatalf("a magnet's trackers %v", v)
	}
	missing := strings.Repeat("9", 40)
	for _, p := range []string{"", "/files", "/peers", "/trackers"} {
		f.json(t, "GET", "/bittorrent/torrents/"+missing+p, "", http.StatusNotFound)
		f.json(t, "GET", "/bittorrent/torrents/xyz"+p, "", http.StatusNotFound)
	}

	// Actions.
	if v := f.json(t, "POST", "/bittorrent/torrents/"+idA, `{"action": "pause"}`, http.StatusOK); v["paused"] != true || v["state"] != "paused" {
		t.Fatalf("pause %v", v)
	}
	if v := f.json(t, "POST", "/bittorrent/torrents/"+idA, `{"action": "resume"}`, http.StatusOK); v["paused"] != false {
		t.Fatalf("resume %v", v)
	}
	waitFor("a.bin seeding again", func() bool {
		return f.json(t, "GET", "/bittorrent/torrents/"+idA, "", http.StatusOK)["state"] == "seeding"
	})
	f.json(t, "POST", "/bittorrent/torrents/"+idA, `{"action": "recheck"}`, http.StatusOK)
	waitFor("a.bin seeding after its check", func() bool {
		return f.json(t, "GET", "/bittorrent/torrents/"+idA, "", http.StatusOK)["state"] == "seeding"
	})
	f.json(t, "POST", "/bittorrent/torrents/"+hash, `{"action": "recheck"}`, http.StatusBadRequest)
	f.json(t, "POST", "/bittorrent/torrents/"+idA, `{"action": "dance"}`, http.StatusBadRequest)
	f.json(t, "POST", "/bittorrent/torrents/"+idA, `{"action": `, http.StatusBadRequest)
	f.json(t, "POST", "/bittorrent/torrents/"+missing, `{"action": "pause"}`, http.StatusNotFound)
	f.call(t, "POST", "/bittorrent/torrents/"+idA, "text/plain", []byte("pause"), http.StatusUnsupportedMediaType)

	// Changes.
	v = f.json(t, "PATCH", "/bittorrent/torrents/"+idA, `{"sequential": false, "category": "", "limits": {"download_rate": 5000, "seed_time": 60000000000},
		"queue_position": 0, "priorities": [{"index": 0, "priority": 6}]}`, http.StatusOK)
	if v["sequential"] != false || v["category"] != nil || v["queue_position"] != 0.0 ||
		v["limits"].(map[string]any)["download_rate"] != 5000.0 || v["limits"].(map[string]any)["seed_time"] != 6e10 ||
		v["files"].([]any)[0].(map[string]any)["priority"] != 6.0 {
		t.Fatalf("patch %v", v)
	}
	if v = f.json(t, "PATCH", "/bittorrent/torrents/"+idA, `{"limits": null, "paused": true}`, http.StatusOK); v["limits"] != nil || v["paused"] != true {
		t.Fatalf("limits cleared %v", v)
	}
	if v = f.json(t, "PATCH", "/bittorrent/torrents/"+idA, `{"paused": false}`, http.StatusOK); v["paused"] != false {
		t.Fatalf("unpaused %v", v)
	}
	f.json(t, "PATCH", "/bittorrent/torrents/"+idA, `{"priorities": [{"index": 0, "priority": 9}]}`, http.StatusBadRequest)
	f.json(t, "PATCH", "/bittorrent/torrents/"+idA, `{"priorities": [{"index": 3, "priority": 1}]}`, http.StatusBadRequest)
	f.json(t, "PATCH", "/bittorrent/torrents/"+idA, `{"category": "nope"}`, http.StatusBadRequest)
	f.json(t, "PATCH", "/bittorrent/torrents/"+idA, `{"queue_position": -1}`, http.StatusBadRequest)
	f.json(t, "PATCH", "/bittorrent/torrents/"+idA, `{"limits": {"seed_time": "x"}}`, http.StatusBadRequest)
	f.json(t, "PATCH", "/bittorrent/torrents/"+idA, `{"limits": 7}`, http.StatusBadRequest)
	f.json(t, "PATCH", "/bittorrent/torrents/"+missing, `{"sequential": true}`, http.StatusNotFound)
	f.json(t, "PATCH", "/bittorrent/torrents/"+idA, ``, http.StatusOK)

	// Removal.
	f.json(t, "DELETE", "/bittorrent/torrents/"+idC+"?delete_data=maybe", "", http.StatusBadRequest)
	if v := f.json(t, "DELETE", "/bittorrent/torrents/"+idC+"?delete_data=1", "", http.StatusOK); v["removed"] != idC || v["data_deleted"] != true {
		t.Fatalf("delete %v", v)
	}
	f.json(t, "DELETE", "/bittorrent/torrents/"+idC, "", http.StatusNotFound)
	if v := f.json(t, "DELETE", "/bittorrent/torrents/"+idD, "", http.StatusOK); v["data_deleted"] != false {
		t.Fatalf("delete %v", v)
	}
	if _, err := os.Stat(filepath.Join(films, "c.bin")); err == nil {
		t.Fatal("delete_data left the file")
	}
	if _, err := os.Stat(filepath.Join(f.dl, "d.bin")); err != nil {
		t.Fatal("a removal without delete_data took the file")
	}
	_ = idB
}

// zeros reads zero bytes for ever.
type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// TestBitTorrentAltSpeedPublishTracker drives the alternative rates, the
// publish entries and the tracker's endpoints.
func TestBitTorrentAltSpeedPublishTracker(t *testing.T) {
	f := startBT(t, &config.Admin{}, "")
	if v := f.json(t, "GET", "/bittorrent/alt_speed", "", http.StatusOK); v["mode"] != "auto" || v["configured"] != true || v["active"] != false {
		t.Fatalf("alt_speed %v", v)
	}
	if v := f.json(t, "PUT", "/bittorrent/alt_speed", `{"mode": "on"}`, http.StatusOK); v["mode"] != "on" || v["active"] != true || v["upload_rate"] != 1000.0 {
		t.Fatalf("on %v", v)
	}
	f.json(t, "PUT", "/bittorrent/alt_speed", `{"mode": "fast"}`, http.StatusBadRequest)
	f.json(t, "PUT", "/bittorrent/alt_speed", `{"mode": 1}`, http.StatusBadRequest)

	if v := f.json(t, "GET", "/bittorrent/publish", "", http.StatusOK); len(v["entries"].([]any)) != 0 {
		t.Fatalf("publish %v", v)
	}
	f.json(t, "GET", "/bittorrent/publish/none", "", http.StatusNotFound)

	st := f.json(t, "GET", "/bittorrent/tracker/stats", "", http.StatusOK)
	if st["private"] != false || st["udp"] == "" || st["refused"] == nil {
		t.Fatalf("stats %v", st)
	}
	if v := f.json(t, "GET", "/bittorrent/tracker/torrents", "", http.StatusOK); v["total"] != 0.0 || len(v["torrents"].([]any)) != 0 ||
		v["source"] != "store" {
		t.Fatalf("tracker torrents %v", v)
	}
	f.json(t, "GET", "/bittorrent/tracker/torrents?limit=5000", "", http.StatusBadRequest)
	f.json(t, "GET", "/bittorrent/tracker/torrents?after=zz", "", http.StatusBadRequest)
	h := strings.Repeat("ab", 20)
	if v := f.json(t, "GET", "/bittorrent/tracker/torrents?after="+h, "", http.StatusOK); v["total"] != 0.0 || v["next"] != "" {
		t.Fatalf("tracker torrents after %v", v)
	}
	if v := f.json(t, "GET", "/bittorrent/tracker/torrents/"+h, "", http.StatusOK); v["info_hash"] != h || v["tracked"] != false {
		t.Fatalf("tracker torrent %v", v)
	}
	f.json(t, "GET", "/bittorrent/tracker/torrents/nothex", "", http.StatusBadRequest)
	f.json(t, "GET", "/bittorrent/tracker/passkey", "", http.StatusBadRequest)
	if v := f.json(t, "GET", "/bittorrent/tracker/passkey?user=ada", "", http.StatusNotFound); !strings.Contains(v["error"].(string), "public") {
		t.Fatalf("passkey %v", v)
	}
}

// TestBTErrorStatus maps each of the role's errors to its status.
func TestBTErrorStatus(t *testing.T) {
	for err, want := range map[error]int{
		bittorrent.ErrNotFound: 404, bittorrent.ErrExists: 409, bittorrent.ErrBroken: 409, bittorrent.ErrCannotCheck: 409, bittorrent.ErrTooMany: 429,
		bittorrent.ErrTooManyTemporary: 429, bittorrent.ErrBusy: 503, bittorrent.ErrTooManySubscribers: 503,
		bittorrent.ErrNotRunning: 503, tracker.ErrNotRunning: 503, bittorrent.ErrBadRequest: 400,
		fmt.Errorf("%w: x", bittorrent.ErrFetch): 502, errors.New("disk"): 500,
	} {
		w := httptest.NewRecorder()
		btError(w, err)
		if w.Code != want {
			t.Errorf("%v: %d, want %d", err, w.Code, want)
		}
		if want == 503 && w.Header().Get("Retry-After") == "" {
			t.Errorf("%v: no Retry-After", err)
		}
	}
}

// sseEvent is a server-sent event as read.
type sseEvent struct {
	id, event, data string
	comment         string
}

// sseStream reads a stream of server-sent events.
type sseStream struct {
	t      *testing.T
	resp   *http.Response
	cancel context.CancelFunc
	events chan sseEvent
}

// openEvents opens /bittorrent/events with header, which must answer
// want; the stream is read on a goroutine of its own.
func (f *btFixture) openEvents(t *testing.T, want int, header ...string) *sseStream {
	t.Helper()
	return openSSE(t, &http.Client{Transport: &http.Transport{}}, f.api+"/bittorrent/events", want, header...)
}

// openSSE opens the stream of events at url with c and header, which must
// answer want; the stream is read on a goroutine of its own.
func openSSE(t *testing.T, c *http.Client, url string, want int, header ...string) *sseStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := c.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if resp.StatusCode != want {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		cancel()
		t.Fatalf("events: %d %s, want %d", resp.StatusCode, body, want)
	}
	s := &sseStream{t: t, resp: resp, cancel: cancel, events: make(chan sseEvent, 1<<16)}
	t.Cleanup(s.close)
	if want != http.StatusOK {
		resp.Body.Close()
		return s
	}
	go func() {
		defer close(s.events)
		sc := bufio.NewScanner(resp.Body)
		var e sseEvent
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				s.events <- e
				e = sseEvent{}
			case strings.HasPrefix(line, ":"):
				e.comment += line
			case strings.HasPrefix(line, "id: "):
				e.id = line[4:]
			case strings.HasPrefix(line, "event: "):
				e.event = line[7:]
			case strings.HasPrefix(line, "data: "):
				e.data = line[6:]
			}
		}
	}()
	return s
}

func (s *sseStream) close() {
	s.cancel()
	_ = s.resp.Body.Close()
}

// next returns the next event that match accepts, failing after btHang or
// when the stream ends.
func (s *sseStream) next(what string, match func(sseEvent) bool) sseEvent {
	s.t.Helper()
	timer := time.NewTimer(btHang)
	defer timer.Stop()
	for {
		select {
		case e, ok := <-s.events:
			if !ok {
				s.t.Fatalf("the stream ended waiting for %s", what)
			}
			if match(e) {
				return e
			}
		case <-timer.C:
			s.t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// ended waits for the stream to end, returning its last events.
func (s *sseStream) ended() []sseEvent {
	s.t.Helper()
	var got []sseEvent
	timer := time.NewTimer(btHang)
	defer timer.Stop()
	for {
		select {
		case e, ok := <-s.events:
			if !ok {
				return got
			}
			got = append(got, e)
		case <-timer.C:
			s.t.Fatal("the stream did not end")
		}
	}
}

// TestBitTorrentEvents streams the role's events: an add's events,
// heartbeats, a reconnect with Last-Event-ID, a gap, the bound on
// subscribers, a disconnect freeing a slot, a slow client dropped, the end
// when the role stops, and the console's 501.
func TestBitTorrentEvents(t *testing.T) {
	defer func(d time.Duration) { btHeartbeat = d }(btHeartbeat)
	btHeartbeat = 20 * time.Millisecond
	f := startBT(t, &config.Admin{MaxConns: 64}, "")
	s := f.openEvents(t, http.StatusOK)
	if ct := s.resp.Header.Get("Content-Type"); ct != "text/event-stream" || s.resp.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("headers %v", s.resp.Header)
	}
	const hash = "0123456789abcdef0123456789abcdef01234567"
	f.json(t, "POST", "/bittorrent/torrents", `{"magnet": "magnet:?xt=urn:btih:`+hash+`&dn=film", "paused": true}`, http.StatusCreated)
	added := s.next("added", func(e sseEvent) bool { return e.event == bittorrent.EventAdded })
	var ev bittorrent.Event
	if err := json.Unmarshal([]byte(added.data), &ev); err != nil || ev.Torrent != hash || ev.Name != "film" || strconv.FormatUint(ev.ID, 10) != added.id {
		t.Fatalf("added %+v %v", added, err)
	}
	s.next("a heartbeat", func(e sseEvent) bool { return strings.Contains(e.comment, "heartbeat") })
	f.json(t, "DELETE", "/bittorrent/torrents/"+hash, "", http.StatusOK)
	removed := s.next("removed", func(e sseEvent) bool { return e.event == bittorrent.EventRemoved })

	// Back after the add: the removal again, then what comes.
	again := f.openEvents(t, http.StatusOK, "Last-Event-ID", added.id)
	if e := again.next("the removal replayed", func(e sseEvent) bool { return e.id != "" }); e.id != removed.id {
		t.Fatalf("replayed %+v, want %s", e, removed.id)
	}
	again.close()
	after := f.openEvents(t, http.StatusOK)
	after.close()
	// Past the last event: reset.
	reset := f.openEvents(t, http.StatusOK, "Last-Event-ID", "999999")
	reset.next("reset", func(e sseEvent) bool { return e.event == "reset" })
	reset.close()
	byQuery := f.openEvents(t, http.StatusOK)
	byQuery.close()
	f.openEvents(t, http.StatusBadRequest, "Last-Event-ID", "x")

	// The bound: the subscribers the test holds, and the HTTP streams.
	s.close()
	var held []*bittorrent.Subscription
	waitSubscribe := func() {
		t.Helper()
		for deadline := time.Now().Add(btHang); ; time.Sleep(10 * time.Millisecond) {
			sub, _, _, err := f.role.Subscribe(0)
			if err == nil {
				held = append(held, sub)
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("no slot freed: %v", err)
			}
		}
	}
	// The streams closed above free their slots once their handlers see
	// it: every slot is taken here, which waits for them, then one is
	// given back for the stream.
	for range bittorrent.MaxSubscribers {
		waitSubscribe()
	}
	held[len(held)-1].Close()
	held = held[:len(held)-1]
	last := f.openEvents(t, http.StatusOK)
	f.openEvents(t, http.StatusServiceUnavailable)
	last.close()
	waitSubscribe()
	for _, sub := range held {
		sub.Close()
	}

	// A client that reads nothing while far more events come than its
	// buffer and the connection's hold: it is dropped, told why when it
	// reads again.
	raw := f.rawEvents(t)
	c := f.role.Client()
	for i := range 40000 {
		c.Ban(netip.AddrPortFrom(netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)}), 0))
	}
	if !strings.Contains(raw.drain(t), "event: lagged") {
		t.Fatal("the slow client was not told it lagged")
	}

	// The role stops: the stream ends.
	end := f.openEvents(t, http.StatusOK)
	f.role.Stop(context.Background())
	got := end.ended()
	if len(got) == 0 || got[len(got)-1].event != "end" {
		t.Fatalf("the end: %+v", got)
	}
}

// rawStream is a stream of events read by hand, as slowly as a test wants.
type rawStream struct {
	resp *http.Response
}

// rawEvents opens /bittorrent/events and reads nothing until drain.
func (f *btFixture) rawEvents(t *testing.T) *rawStream {
	t.Helper()
	tr := &http.Transport{ReadBufferSize: 512, DisableCompression: true}
	resp, err := (&http.Client{Transport: tr}).Get(f.api + "/bittorrent/events")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events: %d", resp.StatusCode)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return &rawStream{resp: resp}
}

// drain reads the stream to its end.
func (r *rawStream) drain(t *testing.T) string {
	t.Helper()
	done := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(r.resp.Body)
		done <- b
	}()
	select {
	case b := <-done:
		return string(b)
	case <-time.After(btHang):
		t.Fatal("the slow stream did not end")
	}
	return ""
}

// TestBitTorrentConsole drives the endpoints through the console: a
// session needed, the role's API on the console's pool, and the event
// stream refused.
func TestBitTorrentConsole(t *testing.T) {
	e := testEngine(t)
	f := &consoleFixture{fixture: startEnv(t, doc("one"), &config.Admin{}, runtime.Env{Metrics: observe.NewMetrics(), Engine: e}), now: time.Now()}
	hash, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	c := &config.Console{Address: "127.0.0.1:0", Users: []config.ConsoleUser{{Username: "ada", PasswordHash: string(hash)}}}
	var err error
	f.console, err = StartConsole(testEngine(t), c, &config.Admin{}, f.configs, nil, nil, func(format string, args ...any) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.logs = append(f.logs, strings.TrimSpace(fmt.Sprintf(format, args...)))
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.console.Shutdown(context.Background()) })
	f.url = strings.TrimSuffix(f.console.URL(), "/")
	startBTRoles(t, e, "")

	b := f.browser(t)
	if code, _, _ := b.do("GET", "/api/m/admin/bittorrent/torrents", ""); code != http.StatusUnauthorized {
		t.Fatalf("without a login: %d", code)
	}
	if code := b.login("ada", "pw"); code != http.StatusOK {
		t.Fatalf("login: %d", code)
	}
	const h = "0123456789abcdef0123456789abcdef01234567"
	if code, body, _ := b.do("POST", "/api/m/admin/bittorrent/torrents", `{"magnet": "magnet:?xt=urn:btih:`+h+`", "paused": true}`); code != http.StatusCreated {
		t.Fatalf("add: %d %s", code, body)
	}
	if code, body, _ := b.do("GET", "/api/m/admin/bittorrent/torrents", ""); code != http.StatusOK || !strings.Contains(body, h) {
		t.Fatalf("list: %d %s", code, body)
	}
	if !f.logged(`"ada": POST /bittorrent/torrents: 201`) {
		t.Errorf("the add is not logged: %q", f.logLines())
	}
}

// TestBitTorrentConsoleEvents streams the events through the console's own
// route (I204): behind a session, the add's events and heartbeats, a
// replay from Last-Event-ID, a reset, a bad ID, the bound on streams, a
// client gone freeing its slot, a slow client dropped, and the end when
// the role stops; and 404 without the role.
func TestBitTorrentConsoleEvents(t *testing.T) {
	defer func(d time.Duration) { btHeartbeat = d }(btHeartbeat)
	btHeartbeat = 20 * time.Millisecond
	e := testEngine(t)
	f := &consoleFixture{fixture: startEnv(t, doc("one"), &config.Admin{}, runtime.Env{Metrics: observe.NewMetrics(), Engine: e}), now: time.Now()}
	hash, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	c := &config.Console{Address: "127.0.0.1:0", Users: []config.ConsoleUser{{Username: "ada", PasswordHash: string(hash)}}, MaxConns: 128}
	var err error
	f.console, err = StartConsole(testEngine(t), c, &config.Admin{}, f.configs, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.console.Shutdown(context.Background()) })
	f.url = strings.TrimSuffix(f.console.URL(), "/")
	const path = "/api/m/admin/bittorrent/events"

	b := f.browser(t)
	if code, _, _ := b.do("GET", path, ""); code != http.StatusUnauthorized {
		t.Fatalf("without a login: %d", code)
	}
	if code := b.login("ada", "pw"); code != http.StatusOK {
		t.Fatalf("login: %d", code)
	}
	if code, body, _ := b.do("GET", path, ""); code != http.StatusNotFound || !strings.Contains(body, "does not run here") {
		t.Fatalf("without the role: %d %s", code, body)
	}
	role, _, _ := startBTRoles(t, e, "")
	client := &http.Client{Jar: b.c.Jar, Transport: &http.Transport{}}
	open := func(want int, header ...string) *sseStream {
		t.Helper()
		return openSSE(t, client, f.url+path, want, header...)
	}
	s := open(http.StatusOK)
	if ct := s.resp.Header.Get("Content-Type"); ct != "text/event-stream" || s.resp.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("headers %v", s.resp.Header)
	}
	const h = "0123456789abcdef0123456789abcdef01234567"
	if code, body, _ := b.do("POST", "/api/m/admin/bittorrent/torrents", `{"magnet": "magnet:?xt=urn:btih:`+h+`&dn=film", "paused": true}`); code != http.StatusCreated {
		t.Fatalf("add: %d %s", code, body)
	}
	added := s.next("added", func(e sseEvent) bool { return e.event == bittorrent.EventAdded })
	var ev bittorrent.Event
	if err := json.Unmarshal([]byte(added.data), &ev); err != nil || ev.Torrent != h || ev.Name != "film" || strconv.FormatUint(ev.ID, 10) != added.id {
		t.Fatalf("added %+v %v", added, err)
	}
	s.next("a heartbeat", func(e sseEvent) bool { return strings.Contains(e.comment, "heartbeat") })
	if code, body, _ := b.do("DELETE", "/api/m/admin/bittorrent/torrents/"+h, ""); code != http.StatusOK {
		t.Fatalf("remove: %d %s", code, body)
	}
	removed := s.next("removed", func(e sseEvent) bool { return e.event == bittorrent.EventRemoved })
	again := open(http.StatusOK, "Last-Event-ID", added.id)
	if e := again.next("the removal replayed", func(e sseEvent) bool { return e.id != "" }); e.id != removed.id {
		t.Fatalf("replayed %+v, want %s", e, removed.id)
	}
	again.close()
	byQuery := openSSE(t, client, f.url+path+"?after="+added.id, http.StatusOK)
	byQuery.next("the removal replayed by ?after", func(e sseEvent) bool { return e.id == removed.id })
	byQuery.close()
	reset := open(http.StatusOK, "Last-Event-ID", "999999")
	reset.next("reset", func(e sseEvent) bool { return e.event == "reset" })
	reset.close()
	open(http.StatusBadRequest, "Last-Event-ID", "x")
	s.close()

	// The bound: the role's subscribers, the console's streams among them.
	var held []*bittorrent.Subscription
	waitSubscribe := func() {
		t.Helper()
		for deadline := time.Now().Add(btHang); ; time.Sleep(10 * time.Millisecond) {
			sub, _, _, err := role.Subscribe(0)
			if err == nil {
				held = append(held, sub)
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("no slot freed: %v", err)
			}
		}
	}
	// Every slot taken once the streams closed above have let theirs go,
	// which they do on their own goroutines; then one given back for the
	// console's stream.
	for range bittorrent.MaxSubscribers {
		waitSubscribe()
	}
	held[0].Close()
	held = held[1:]
	last := open(http.StatusOK)
	if code, _, hd := b.do("GET", path, ""); code != http.StatusServiceUnavailable || hd.Get("Retry-After") == "" {
		t.Fatalf("past the bound: %d %v", code, hd)
	}
	last.close()
	waitSubscribe()
	for _, sub := range held {
		sub.Close()
	}

	// A client that reads nothing while far more events come than its
	// buffer and the connection hold: it is dropped, told why.
	req, _ := http.NewRequest("GET", f.url+path, nil)
	slow, err := (&http.Client{Jar: b.c.Jar, Transport: &http.Transport{ReadBufferSize: 512, DisableCompression: true}}).Do(req)
	if err != nil || slow.StatusCode != http.StatusOK {
		t.Fatalf("slow: %v %v", err, slow)
	}
	t.Cleanup(func() { _ = slow.Body.Close() })
	cl := role.Client()
	for i := range 40000 {
		cl.Ban(netip.AddrPortFrom(netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)}), 0))
	}
	if !strings.Contains((&rawStream{resp: slow}).drain(t), "event: lagged") {
		t.Fatal("the slow client was not told it lagged")
	}

	// The bans' events may still be on their way: a stream opened now
	// could lag behind them and be dropped before the end. Wait for a
	// quiet subscriber first.
	for quietFor := time.Now(); ; {
		quiet, _, _, err := role.Subscribe(0)
		if err != nil {
			t.Fatal(err)
		}
		idle := time.NewTimer(200 * time.Millisecond)
		lagged := false
	wait:
		for {
			select {
			case _, ok := <-quiet.Events():
				if !ok { // dropped as lagged: subscribe again
					lagged = true
					break wait
				}
				idle.Reset(200 * time.Millisecond)
			case <-idle.C:
				break wait
			}
		}
		quiet.Close()
		if !lagged {
			break
		}
		if time.Since(quietFor) > btHang {
			t.Fatal("the bans' events never quieted")
		}
	}

	// The role stops: the stream ends.
	end := open(http.StatusOK)
	role.Stop(context.Background())
	got := end.ended()
	if len(got) == 0 || got[len(got)-1].event != "end" {
		t.Fatalf("the end: %+v", got)
	}
}

// TestBitTorrentPublishedTorrent serves a publish entry's .torrent file,
// directly and through the console, and 404 for one it does not publish.
func TestBitTorrentPublishedTorrent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.bin"), bytes.Repeat([]byte("x"), 70000), 0o644); err != nil {
		t.Fatal(err)
	}
	f := startBT(t, &config.Admin{}, `, "publish": [{"name": "pub", "dir": `+strconv.Quote(dir)+`, "check_interval": "50ms"}]`)
	var id string
	for deadline := time.Now().Add(btHang); id == ""; time.Sleep(20 * time.Millisecond) {
		v := f.json(t, "GET", "/bittorrent/publish/pub", "", http.StatusOK)
		if ts, _ := v["torrents"].([]any); len(ts) == 1 {
			id, _ = ts[0].(map[string]any)["id"].(string)
		}
		if time.Now().After(deadline) {
			t.Fatalf("not published: %v", v)
		}
	}
	req, _ := http.NewRequest("GET", f.api+"/bittorrent/publish/pub/torrents/"+strings.ToUpper(id), nil)
	resp, body := f.do(t, req)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/x-bittorrent" ||
		!strings.Contains(resp.Header.Get("Content-Disposition"), `filename=a.bin.torrent`) {
		t.Fatalf("%d %v", resp.StatusCode, resp.Header)
	}
	mi, err := metainfo.Parse([]byte(body))
	if err != nil || mi.Info.Name != "a.bin" {
		t.Fatalf("%v %+v", err, mi)
	}
	f.json(t, "GET", "/bittorrent/publish/pub/torrents/"+strings.Repeat("ab", 20), "", http.StatusNotFound)
	f.json(t, "GET", "/bittorrent/publish/other/torrents/"+id, "", http.StatusNotFound)
}
