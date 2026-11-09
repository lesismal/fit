package assistant

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/lesismal/fit/assistant/internal/fakeapi"
	"github.com/lesismal/fit/config"
)

// The fixed torrents of fakeBT: sintel downloading, bbb paused, and a
// private tracker's passkey in sintel's tracker URL and magnet link, which
// never reach the model.
const (
	btSintel  = "08ada5a7a6183aae1e09d831df6748d566095a10"
	btBBB     = "dd8255ecdc7ca55fb0bbf81323d87062db1f6d1c"
	btNew     = "0123456789abcdef0123456789abcdef01234567"
	btPasskey = "PASSKEY0123456789abcdef"
)

// btBase is a config whose client has the category films.
const btBase = `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
"bittorrent": {"client": {"listen": ":6881", "data_dir": "/var/lib/fit/bt", "categories": [{"name": "films"}]}},
"admin": {"address": "127.0.0.1:2019"}}`

// fakeBT is the Admin API's /bittorrent endpoints, as admin/bittorrent.go
// answers them; changes are recorded, and made to its torrents' pause.
type fakeBT struct {
	mu      sync.Mutex
	reqs    []string // every request: method, URI, the console's user and body
	paused  map[string]bool
	down    bool            // the role does not run: 404
	fail    map[string]int  // "METHOD /path" answers this status
	held    map[string]bool // torrents the client holds
	temp    map[string]bool // torrents bt_stream added on the fly
	changes []string
}

func newFakeBT() *fakeBT {
	return &fakeBT{paused: map[string]bool{btBBB: true}, fail: map[string]int{}, held: map[string]bool{btSintel: true, btBBB: true}, temp: map[string]bool{}}
}

func (f *fakeBT) status(id string) string {
	name := map[string]string{btSintel: "Sintel", btBBB: "Big Buck Bunny", btNew: "New"}[id]
	state := "downloading"
	if f.paused[id] {
		state = "paused"
	}
	tmp := ""
	if f.temp[id] {
		tmp = `, "temporary": true`
	}
	return `{"id": "` + id + `", "name": "` + name + `", "state": "` + state + `", "paused": ` + map[bool]string{true: "true", false: "false"}[f.paused[id]] +
		`, "magnet": "magnet:?xt=urn:btih:` + id + `&tr=https%3A%2F%2Ftracker.example%2F` + btPasskey + `%2Fannounce", "dir": "/dl", "size": 1000, "wanted": 1000,
		"completed": 400, "uploaded": 1500, "download_rate": 100, "upload_rate": 5, "peers": 3, "seeders": 1, "queue_position": 0, "pieces": 10, "pieces_have": 4,
		"category": "films"` + tmp + `}`
}

func (f *fakeBT) handler(next http.Handler) http.Handler {
	mux := http.NewServeMux()
	answer := func(w http.ResponseWriter, status int, body string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
	gate := func(w http.ResponseWriter, r *http.Request) bool {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.reqs = append(f.reqs, strings.TrimSpace(r.Method+" "+r.URL.RequestURI()+" "+r.Header.Get(ConsoleUserHeader)+" "+string(b)))
		if r.Method != "GET" {
			f.changes = append(f.changes, strings.TrimSpace(r.Method+" "+r.URL.Path+" "+r.Header.Get(ConsoleUserHeader)+" "+string(b)))
		}
		if f.down {
			answer(w, 404, `{"error": "the bittorrent.client role does not run here"}`)
			return false
		}
		if st := f.fail[r.Method+" "+r.URL.Path]; st != 0 {
			answer(w, st, `{"error": "scripted failure"}`)
			return false
		}
		if id := r.PathValue("id"); id != "" && !f.held[id] {
			answer(w, 404, `{"error": "bittorrent: no such torrent"}`)
			return false
		}
		if r.Method == "POST" && r.PathValue("id") != "" {
			var a struct{ Action string }
			_ = json.Unmarshal(b, &a)
			f.paused[r.PathValue("id")] = a.Action == "pause"
		}
		if r.Method == "POST" && r.URL.Path == "/bittorrent/torrents" {
			f.held[btNew] = true
		}
		return true
	}
	mux.HandleFunc("GET /bittorrent/torrents", func(w http.ResponseWriter, r *http.Request) {
		if gate(w, r) {
			f.mu.Lock()
			defer f.mu.Unlock()
			answer(w, 200, `{"torrents": [`+f.status(btSintel)+`, `+f.status(btBBB)+`], "total": 2, "offset": 0}`)
		}
	})
	mux.HandleFunc("POST /bittorrent/torrents", func(w http.ResponseWriter, r *http.Request) {
		if gate(w, r) {
			f.mu.Lock()
			defer f.mu.Unlock()
			answer(w, 201, f.status(btNew))
		}
	})
	mux.HandleFunc("/bittorrent/torrents/{id}", func(w http.ResponseWriter, r *http.Request) {
		if gate(w, r) {
			f.mu.Lock()
			defer f.mu.Unlock()
			st := f.status(r.PathValue("id"))
			if r.Method == "GET" {
				files := make([]string, 120)
				for i := range files {
					files[i] = `{"index": 1, "path": "f", "length": 10, "completed": 5, "priority": 4}`
				}
				st = strings.TrimSuffix(st, "}") + `, "files": [` + strings.Join(files, ",") + `]}`
			}
			answer(w, 200, st)
		}
	})
	mux.HandleFunc("GET /bittorrent/torrents/{id}/peers", func(w http.ResponseWriter, r *http.Request) {
		if gate(w, r) {
			peers := make([]string, 60)
			for i := range peers {
				peers[i] = `{"addr": "203.0.113.7:51413", "id": "2d4642303030312d", "client": "Transmission 4.0", "seed": true, "download_rate": 10}`
			}
			answer(w, 200, `{"peers": [`+strings.Join(peers, ",")+`]}`)
		}
	})
	mux.HandleFunc("GET /bittorrent/torrents/{id}/trackers", func(w http.ResponseWriter, r *http.Request) {
		if gate(w, r) {
			answer(w, 200, `{"trackers": [{"url": "https://tracker.example/`+btPasskey+`/announce", "tier": 0}, {"url": "udp://open.example:1337", "tier": 1},
				{"url": "::bad", "tier": 2}], "web_seeds": ["https://u:p@seed.example/files/sintel.mkv?token=`+btPasskey+`"]}`)
		}
	})
	mux.HandleFunc("GET /bittorrent/alt_speed", func(w http.ResponseWriter, r *http.Request) {
		if gate(w, r) {
			answer(w, 200, `{"mode": "auto", "configured": true, "active": false, "upload_rate": 0, "download_rate": 0}`)
		}
	})
	mux.HandleFunc("GET /bittorrent/tracker/stats", func(w http.ResponseWriter, r *http.Request) {
		if gate(w, r) {
			answer(w, 200, `{"torrents": 2, "peers": 9, "announces": 40, "scrapes": 3, "refused": {"unregistered": 1, "passkey": 2, "access": 0}, "private": true, "allowed": 2}`)
		}
	})
	mux.HandleFunc("GET /bittorrent/tracker/torrents", func(w http.ResponseWriter, r *http.Request) {
		if gate(w, r) {
			answer(w, 200, `{"torrents": [{"info_hash": "`+btSintel+`", "seeders": 3, "leechers": 2, "completed": 7, "tracked": true}], "total": 2, "offset": 0}`)
		}
	})
	mux.HandleFunc("GET /bittorrent/rss", func(w http.ResponseWriter, r *http.Request) {
		if gate(w, r) {
			answer(w, 200, `{"configured": true, "feeds": [{"name": "shows", "url": "https://rss.example/feeds/`+btPasskey+`/shows.xml", "enabled": true,
				"interval": 900000000000, "last_result": "ok", "seen": 3, "added": 1}], "rules": [{"name": "s", "enabled": true, "category": "films", "added": 1}]}`)
		}
	})
	mux.HandleFunc("GET /bittorrent/rss/items", func(w http.ResponseWriter, r *http.Request) {
		if gate(w, r) {
			answer(w, 200, `{"items": [{"feed": "shows", "key": "https://rss.example/dl/`+btPasskey+`", "title": "Show S01E01", "outcome": "added", "rule": "s",
				"torrent": "`+btSintel+`"}], "total": 3}`)
		}
	})
	mux.Handle("/", next)
	return mux
}

func (f *fakeBT) last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reqs[len(f.reqs)-1]
}

func (f *fakeBT) changed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.changes)
}

// TestBTToolsRead: the read tools read the torrents, one torrent whole
// and the tracker through the Admin API, computing ratio and time left,
// bounding files and peers, and never handing over a passkey: no magnet
// link, trackers by scheme, host and last element, web seeds without user
// or query, no peer IDs.
func TestBTToolsRead(t *testing.T) {
	bt := newFakeBT()
	a := toolsAssistant(t, &config.Config{}, func(o *Options) { o.API = bt.handler(fakeAdmin()) })
	out := mustRun(t, a, "bt_torrents", `{"state": "downloading,paused", "category": "", "q": "s", "limit": 10}`)
	var list struct {
		Torrents []map[string]any `json:"torrents"`
		Total    int              `json:"total"`
		Alt      map[string]any   `json:"alt_speed"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil || list.Total != 2 || len(list.Torrents) != 2 || list.Alt["mode"] != "auto" {
		t.Fatalf("%v %s", err, out)
	}
	s := list.Torrents[0]
	if s["ratio"] != 1.5 || s["eta_seconds"] != 6.0 || s["magnet"] != nil || s["uploaded"] != nil || s["dir"] != nil || s["name"] != "Sintel" {
		t.Errorf("%v", s)
	}
	if list.Torrents[1]["eta_seconds"] != nil {
		t.Errorf("a paused torrent has a time left: %v", list.Torrents[1])
	}
	if got := bt.reqs[0]; got != "GET /bittorrent/torrents?category=&limit=10&offset=0&q=s&state=downloading%2Cpaused" {
		t.Error(got)
	}
	mustRun(t, a, "bt_torrents", `{}`)
	if got := bt.reqs[2]; got != "GET /bittorrent/torrents?limit=50&offset=0" {
		t.Error(got)
	}
	wantErr(t, a, "bt_torrents", `{"limit": 201}`, "limit is 1 to 200")
	wantErr(t, a, "bt_torrents", `[`, "not what the tool takes")

	out = mustRun(t, a, "bt_torrent", `{"torrent": "`+strings.ToUpper(btSintel)+`"}`)
	var one struct {
		Status     map[string]any   `json:"status"`
		Files      []any            `json:"files"`
		FilesTotal int              `json:"files_total"`
		Peers      []map[string]any `json:"peers"`
		PeersTotal int              `json:"peers_total"`
		Trackers   []struct{ URL string }
		WebSeeds   []string `json:"web_seeds"`
	}
	if err := json.Unmarshal([]byte(out), &one); err != nil || one.Status["ratio"] != 1.5 || one.Status["files"] != nil || len(one.Files) != 100 ||
		one.FilesTotal != 120 || len(one.Peers) != 50 || one.PeersTotal != 60 || one.Peers[0]["id"] != nil || one.Peers[0]["client"] != "Transmission 4.0" {
		t.Fatalf("%v %s", err, out)
	}
	if one.Trackers[0].URL != "https://tracker.example/[redacted]/announce" || one.Trackers[1].URL != "udp://open.example:1337" ||
		one.Trackers[2].URL != "[redacted]" || one.WebSeeds[0] != "https://seed.example/files/sintel.mkv" {
		t.Errorf("%+v %v", one.Trackers, one.WebSeeds)
	}
	if strings.Contains(out, btPasskey) || strings.Contains(out, "magnet:") {
		t.Errorf("a passkey reaches the model: %s", out)
	}
	wantErr(t, a, "bt_torrent", `{"torrent": "xyz"}`, "torrent is a torrent's ID")
	wantErr(t, a, "bt_torrent", `{"torrent": "`+btNew+`"}`, "no such torrent")
	wantErr(t, a, "bt_torrent", `[`, "not what the tool takes")
	if out := mustRun(t, a, "bt_torrent", `{"torrent": "`+btSintel+strings.Repeat("0", 24)+`"}`); !strings.Contains(out, `"id":"`+btSintel+`"`) {
		t.Error(out)
	}

	out = mustRun(t, a, "bt_tracker_stats", `{}`)
	if !strings.Contains(out, `"announces":40`) || !strings.Contains(out, `"swarms":[{"info_hash":"`+btSintel) || !strings.Contains(out, `"swarms_total":2`) {
		t.Error(out)
	}
	if got := bt.last(); got != "GET /bittorrent/tracker/torrents?limit=50" {
		t.Error(got)
	}

	out = mustRun(t, a, "bt_rss", `{"feed": "shows", "outcome": "added", "items": 5}`)
	if !strings.Contains(out, `"url":"https://rss.example/[redacted]/shows.xml"`) || !strings.Contains(out, `"title":"Show S01E01"`) ||
		!strings.Contains(out, `"items_total":3`) || !strings.Contains(out, `"rules":[{"name":"s"`) || strings.Contains(out, btPasskey) {
		t.Error(out)
	}
	if got := bt.last(); got != "GET /bittorrent/rss/items?feed=shows&limit=5&outcome=added" {
		t.Error(got)
	}
	if out := mustRun(t, a, "bt_rss", `{"items": 0}`); strings.Contains(out, `"items"`) || bt.last() != "GET /bittorrent/rss" {
		t.Error(out, bt.last())
	}
	wantErr(t, a, "bt_rss", `{"items": 101}`, "items is 0 to 100")

	bt.mu.Lock()
	bt.down = true
	bt.mu.Unlock()
	for _, c := range [][2]string{{"bt_torrents", `{}`}, {"bt_torrent", `{"torrent": "` + btSintel + `"}`}, {"bt_tracker_stats", `{}`}, {"bt_rss", `{}`}} {
		wantErr(t, a, c[0], c[1], "does not run here")
	}
	if len(bt.changed()) != 0 {
		t.Errorf("reading changed %v", bt.changed())
	}
}

// startBTChat starts an assistant on fakeConfigs over btBase and fakeBT,
// with settings extra, whose model answers turns.
func startBTChat(t *testing.T, extra string, turns ...fakeapi.Turn) (*propFixture, *fakeBT) {
	t.Helper()
	cfgs := newFakeConfigs(btBase)
	bt := newFakeBT()
	sc := &script{turns: turns}
	f := startChat(t, extra, sc.next, func(o *Options) { o.API = bt.handler(cfgs.handler()) })
	return &propFixture{chatFixture: f, cfgs: cfgs, sc: sc}, bt
}

// TestBTProposal: adding, pausing and resuming torrents are proposed in a
// chat, change nothing until applied, and are applied in order on the
// confirm, as the person who applies them, after the config change of the
// same proposal; one that fails leaves the others done.
func TestBTProposal(t *testing.T) {
	turns := []fakeapi.Turn{
		{Calls: []fakeapi.Call{
			call("p1", ToolBTAddPropose, `{"summary": "Add the new film", "magnet": "magnet:?xt=urn:btih:`+btNew+`&dn=New", "category": "films", "sequential": true}`),
			call("p2", ToolBTPausePropose, `{"summary": "Pause Sintel and BBB", "torrents": ["`+btSintel+`", "`+btBBB+`"]}`),
			call("p3", ToolBTResumePropose, `{"summary": "Resume BBB", "torrents": ["`+btBBB+`"]}`),
		}},
		{Text: "Proposed: add New in films, pause Sintel, resume BBB."},
	}
	f, bt := startBTChat(t, `"default_level": "apply"`, turns...)
	s := f.chat(`{"message": "add this film and pause sintel"}`)
	if s.end == nil {
		t.Fatal(s.err)
	}
	res := s.of(EventToolResult)
	if len(res) != 3 || res[0].Error || res[1].Error || res[2].Error || !strings.Contains(res[1].Content, "Big Buck Bunny") ||
		!strings.Contains(res[1].Content, "paused already") {
		t.Fatalf("%+v", res)
	}
	if got := bt.changed(); len(got) != 0 {
		t.Fatalf("proposing changed %v", got)
	}
	conv := s.end.Conversation
	_, v, body := f.view(conv)
	if v == nil || !v.Valid || v.Kind != "" || len(v.Media) != 3 || v.Media[0].Op != BTAdd || v.Media[0].Torrents[0].ID != btNew ||
		len(v.Media[1].Torrents) != 1 || v.Media[1].Torrents[0].Name != "Sintel" || v.Media[2].Torrents[0].ID != btBBB {
		t.Fatalf("%s", body)
	}
	st, out := f.applyMedia(conv, v)
	if st != 200 || out.Outcome != OutcomeApplied || len(out.Media) != 3 || !strings.Contains(string(out.Media[0].Result), `"id":"`+btNew+`"`) ||
		!strings.Contains(string(out.Media[1].Result), `"paused":1`) || !strings.Contains(string(out.Media[2].Result), `"resumed":1`) {
		t.Fatalf("%d %s", st, out.raw)
	}
	want := []string{
		`POST /bittorrent/torrents ada {"category":"films","magnet":"magnet:?xt=urn:btih:` + btNew + `\u0026dn=New","paused":false,"sequential":true}`,
		`POST /bittorrent/torrents/` + btSintel + ` ada {"action":"pause"}`,
		`POST /bittorrent/torrents/` + btBBB + ` ada {"action":"resume"}`,
	}
	if got := bt.changed(); !slices.Equal(got, want) {
		t.Errorf("%q\n%q", got, want)
	}
	if e := f.audit(""); e[0].Kind != AuditApply || e[0].Outcome != OutcomeApplied {
		t.Errorf("%+v", e[0])
	}

	// With a config change, a URL, a torrent gone and a failure: the config
	// first, then the steps, the outcome partial.
	r := f.tool("", ToolConfigPropose, `{"summary": "Another listener", "members": {"listeners": [{"name": "web", "address": "127.0.0.1:0"}, {"name": "b", "address": "127.0.0.1:0"}]}}`)
	conv = r.Conversation
	for _, c := range [][2]string{
		{ToolBTAddPropose, `{"summary": "Add by URL", "url": "https://example.com/x.torrent", "paused": true}`},
		{ToolBTResumePropose, `{"summary": "Resume Sintel", "torrents": ["` + btSintel + `"]}`},
	} {
		if r := f.tool(conv, c[0], c[1]); r.Error {
			t.Fatalf("%s: %+v", c[0], r)
		}
	}
	bt.mu.Lock()
	bt.fail["POST /bittorrent/torrents"] = 502
	delete(bt.held, btSintel)
	bt.mu.Unlock()
	_, v, body = f.view(conv)
	if v == nil || v.Valid {
		// Sintel is gone: the step no longer checks.
		t.Fatalf("a step on a torrent gone checks: %s", body)
	}
	bt.mu.Lock()
	bt.held[btSintel] = true
	bt.mu.Unlock()
	_, v, body = f.view(conv)
	if v == nil || !v.Valid || v.Kind != KindPatch {
		t.Fatalf("%s", body)
	}
	bt.mu.Lock()
	delete(bt.held, btSintel)
	bt.mu.Unlock()
	// Gone between the view and the apply: the check refuses it again.
	if st, out := f.applyMedia(conv, v); st != 422 {
		t.Fatalf("%d %s", st, out.raw)
	}
	bt.mu.Lock()
	bt.held[btSintel] = true
	bt.mu.Unlock()
	st, out = f.applyMedia(conv, v)
	if st != 200 || out.Outcome != OutcomePartial || out.Version != 2 || out.Media[0].Error != "scripted failure" || out.Media[1].Error != "" {
		t.Fatalf("%d %s", st, out.raw)
	}
	if got := f.cfgs.changed(); len(got) != 1 {
		t.Errorf("%v", got)
	}
	if got := bt.changed(); !strings.Contains(got[len(got)-2], `"url":"https://example.com/x.torrent"`) || !strings.Contains(got[len(got)-2], `"paused":true`) {
		t.Errorf("%q", got)
	}
}

// TestBTProposeChecks: each BitTorrent step is checked on what runs now,
// and one that is wrong is refused, saying why, and not added.
func TestBTProposeChecks(t *testing.T) {
	f, bt := startBTChat(t, `"default_level": "apply"`)
	sum := `"summary": "x", `
	long := strings.Repeat("a", 9000)
	for _, c := range []struct{ tool, args, err string }{
		{ToolBTAddPropose, `{"summary": "x"}`, "by magnet or by url, one of them"},
		{ToolBTAddPropose, `{` + sum + `"magnet": "magnet:?xt=urn:btih:` + btNew + `", "url": "https://x/y.torrent"}`, "one of them"},
		{ToolBTAddPropose, `{` + sum + `"magnet": "magnet:?dn=x"}`, "a magnet link that names its torrent"},
		{ToolBTAddPropose, `{` + sum + `"url": "ftp://x/y.torrent"}`, "http or https URL"},
		{ToolBTAddPropose, `{` + sum + `"url": "https://x/` + long + `"}`, "at most 8192 bytes"},
		{ToolBTAddPropose, `{` + sum + `"magnet": "magnet:?xt=urn:btih:` + btSintel + `"}`, `holds this torrent already, as "Sintel"`},
		{ToolBTAddPropose, `{` + sum + `"magnet": "magnet:?xt=urn:btih:` + btNew + `", "category": "music"}`, `no category "music"; it has films`},
		{ToolBTPausePropose, `{` + sum + `"torrents": []}`, "torrents are 1 to 100"},
		{ToolBTPausePropose, `{` + sum + `"torrents": ["nothex"]}`, "torrent is a torrent's ID"},
		{ToolBTPausePropose, `{` + sum + `"torrents": ["` + btSintel + `", "` + strings.ToUpper(btSintel) + `"]}`, "named twice"},
		{ToolBTPausePropose, `{` + sum + `"torrents": ["` + btNew + `"]}`, "no such torrent"},
		{ToolBTPausePropose, `{` + sum + `"torrents": ["` + btBBB + `"]}`, "every torrent named is paused already"},
		{ToolBTResumePropose, `{` + sum + `"torrents": ["` + btSintel + `"]}`, "every torrent named is not paused already"},
		{ToolBTAddPropose, `{"summary": "", "magnet": "magnet:?xt=urn:btih:` + btNew + `"}`, "summary is 1 to"},
	} {
		if r := f.tool("", c.tool, c.args); !r.Error || !strings.Contains(r.Text, c.err) {
			t.Errorf("%s %s: %+v, want %q", c.tool, c.args, r, c.err)
		}
	}
	// One step per torrent and operation; one add per source.
	r := f.tool("", ToolBTPausePropose, `{"summary": "Pause", "torrents": ["`+btSintel+`"]}`)
	if r.Error {
		t.Fatalf("%+v", r)
	}
	if r2 := f.tool(r.Conversation, ToolBTPausePropose, `{"summary": "Pause again", "torrents": ["`+btSintel+`"]}`); !r2.Error || !strings.Contains(r2.Text, "names "+btSintel+" already") {
		t.Errorf("%+v", r2)
	}
	r = f.tool("", ToolBTAddPropose, `{"summary": "Add", "url": "https://x/y.torrent"}`)
	if r2 := f.tool(r.Conversation, ToolBTAddPropose, `{"summary": "Add again", "url": "https://x/y.torrent"}`); !r2.Error || !strings.Contains(r2.Text, "adds this torrent already") {
		t.Errorf("%+v", r2)
	}
	// A torrent bt_stream added on the fly is kept, with a warning.
	bt.mu.Lock()
	bt.held[btNew], bt.temp[btNew] = true, true
	bt.mu.Unlock()
	if r := f.tool("", ToolBTAddPropose, `{"summary": "Keep it", "magnet": "magnet:?xt=urn:btih:`+btNew+`"}`); r.Error || !strings.Contains(r.Text, "on the fly") {
		t.Errorf("%+v", r)
	}
	// A client without categories, and without the role.
	f.cfgs.mu.Lock()
	f.cfgs.versions = append(f.cfgs.versions, []byte(`{"bittorrent": {"client": {"listen": ":1", "data_dir": "/x"}}}`))
	f.cfgs.mu.Unlock()
	if r := f.tool("", ToolBTAddPropose, `{"summary": "x", "url": "https://x/z.torrent", "category": "films"}`); !r.Error || !strings.Contains(r.Text, "has no categories") {
		t.Errorf("%+v", r)
	}
	bt.mu.Lock()
	bt.down = true
	bt.mu.Unlock()
	if r := f.tool("", ToolBTAddPropose, `{"summary": "x", "url": "https://x/z.torrent"}`); !r.Error || !strings.Contains(r.Text, "does not run here") {
		t.Errorf("%+v", r)
	}
	if len(bt.changed()) != 0 {
		t.Errorf("proposing changed %v", bt.changed())
	}
}

// TestBTToolsServed: the read tools go to a read token, the propose ones
// to a propose token only, in their own group, and a manager reads a
// member's torrents.
func TestBTToolsServed(t *testing.T) {
	f, _ := startBTChat(t, "")
	names := func(level string) []string {
		var out []string
		for _, t := range f.a.ExternalTools(level) {
			out = append(out, t.Name)
		}
		return out
	}
	read, propose := names(config.AssistantRead), names(config.AssistantPropose)
	for _, n := range []string{"bt_torrents", "bt_torrent", "bt_tracker_stats", "bt_rss"} {
		if !slices.Contains(read, n) || !slices.Contains(memberTools, n) {
			t.Errorf("read lacks %s", n)
		}
	}
	for _, n := range []string{ToolBTAddPropose, ToolBTPausePropose, ToolBTResumePropose} {
		if slices.Contains(read, n) || !slices.Contains(propose, n) {
			t.Errorf("%s: %v %v", n, read, propose)
		}
		if tl := f.a.Registry().Tool(n); tl == nil || tl.Group != GroupBitTorrent {
			t.Errorf("%s: %+v", n, tl)
		}
	}
}

func TestBTHelpers(t *testing.T) {
	for in, want := range map[string]string{
		"https://t.example/announce": "https://t.example/announce", "https://t.example/k/announce?x=1": "https://t.example/[redacted]/announce",
		"udp://t.example:80": "udp://t.example:80", "/announce": "[redacted]", "http://u:p@t.example/a/b/c": "http://t.example/[redacted]/c",
	} {
		if got := trackerURL(in); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
	if got := webSeedURL("x"); got != "[redacted]" {
		t.Error(got)
	}
	if _, err := btSourceID("magnet:?xt=urn:btmh:1220" + strings.Repeat("ab", 32)); err != nil {
		t.Error(err)
	}
	if id, _ := btSourceID("magnet:?xt=urn:btmh:1220" + strings.Repeat("ab", 32)); id != strings.Repeat("ab", 20) {
		t.Error(id)
	}
}
