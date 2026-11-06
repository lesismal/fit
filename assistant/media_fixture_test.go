package assistant

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// The secrets of the media fixture, which no request to a model may carry:
// the library's and the policy's secrets and a push target's stream key in
// the config, the viewers' subjects (unless the settings send them), and a
// signed playback URL an item's error quotes.
const (
	mediaLibSecret  = "LIBSECRET-0123456789abcdef"
	mediaAuthSecret = "AUTHSECRET-0123456789abcdef"
	mediaStreamKey  = "STREAMKEY-abcd-efgh-ijkl"
	mediaSig        = "Zm9vYmFyYmF6cXV4cXV1eA"
	mediaAlice      = "alice@example.com"
	mediaBob        = "bob@example.com"
	mediaSignedURL  = "https://cdn.example.com/vod/films/films/broken/index.m3u8?exp=1762387200&sid=s9&sub=alice%40example.com&sig=" + mediaSig
)

// mediaBase is the config of a site with a fixed library: films, played on
// demand by the route vod under the policy site, and as the channel
// tv/films, which pushes to an outside server.
const mediaBase = `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
"upstreams": [{"name": "app", "addresses": ["10.0.0.7:9000"]}],
"media_library": [{"name": "films", "roots": ["/srv/media/films"], "secret": "` + mediaLibSecret + `"}],
"media_auth": [{"name": "site", "secret": "` + mediaAuthSecret + `", "url_signing": {"ttl": "2m", "max_sessions": 2}}],
"streaming": {"channels": [{"name": "tv/films", "library": "films", "items": ["films/bbb", "films/news"],
  "push": [{"name": "yt", "url": "rtmp://a.rtmp.youtube.com/live2/` + mediaStreamKey + `"}]}]},
"routes": [{"name": "vod", "match": {"path": ["/vod/*"]}, "handler": {"module": "vod", "params": {"library": "films", "auth": "site"}}}],
"admin": {"address": "127.0.0.1:2019"}}`

// fakeMedia is the Admin API's media endpoints over the fixed library:
// three items, the channel tv/films playing films/bbb, and the policy
// site's sessions; changes are recorded, never made.
type fakeMedia struct {
	mu       sync.Mutex
	changes  []string // method, path and the console's user
	bodies   []string
	usage    map[string]ItemUsage
	sessions []sessionView
	ended    map[string]bool
	// fail answers these requests, "METHOD /path", with the status.
	fail map[string]int
	// noPolicies answers GET /media/auth with none, and twoSigning has
	// the policy inline sign URLs too.
	noPolicies, twoSigning bool
}

func newFakeMedia() *fakeMedia {
	return &fakeMedia{
		usage: map[string]ItemUsage{
			"films/bbb":    {Channels: []string{"tv/films"}, Playing: []string{"tv/films"}, Sessions: 2, Counted: true, InUse: true},
			"films/news":   {Channels: []string{"tv/films"}, Playing: []string{}, Counted: true, InUse: true},
			"films/broken": {Channels: []string{}, Playing: []string{}, Counted: true},
		},
		sessions: []sessionView{
			{Subject: mediaAlice, ID: "s1", Resource: "/vod/films/films/bbb/", Address: "203.0.113.7", Start: "2026-11-06T10:00:00Z", Last: "2026-11-06T10:05:00Z"},
			{Subject: mediaAlice, ID: "s2", Resource: "/vod/films/films/news/", Address: "203.0.113.8", Start: "2026-11-06T10:01:00Z", Last: "2026-11-06T10:05:00Z"},
			{Subject: mediaBob, ID: "s3", Resource: "/vod/films/films/bbb/", Address: "198.51.100.4", Start: "2026-11-06T10:02:00Z", Last: "2026-11-06T10:05:00Z"},
			{Subject: "carol", ID: "s4", Resource: "/vod/films/films/bbb/", Address: "198.51.100.9", Start: "2026-11-06T09:00:00Z", Last: "2026-11-06T09:05:00Z", Ended: true},
		},
		ended: map[string]bool{},
		fail:  map[string]int{},
	}
}

func (m *fakeMedia) changed() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.changes)
}

func (m *fakeMedia) lastBody() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.bodies) == 0 {
		return ""
	}
	return m.bodies[len(m.bodies)-1]
}

func (m *fakeMedia) failWith(req string, status int) {
	m.mu.Lock()
	m.fail[req] = status
	m.mu.Unlock()
}

func (m *fakeMedia) setUsage(id string, u ItemUsage) {
	m.mu.Lock()
	m.usage[id] = u
	m.mu.Unlock()
}

var mediaItems = map[string]string{
	"films/bbb": `{"id": "films/bbb", "title": "Big Buck Bunny", "root": "/srv/media/films", "path": "films/bbb.mp4", "duration": 596,
  "renditions": [{"path": "films/bbb.mp4", "name": "", "width": 1920, "height": 1080, "tracks": [{"codec": "h264"}, {"codec": "aac"}]}],
  "subtitles": [{"path": "films/bbb.en.vtt", "language": "en", "format": "vtt"}]}`,
	"films/news": `{"id": "films/news", "title": "News", "root": "/srv/media/films", "path": "films/news.mp4", "duration": 600,
  "renditions": [{"path": "films/news.mp4", "width": 1280, "height": 720, "tracks": [{"codec": "h264"}, {"codec": "aac"}]}]}`,
	"films/broken": `{"id": "films/broken", "title": "Broken", "root": "/srv/media/films", "path": "films/broken.mp4",
  "error": "mp4: the file is fragmented (moof before moov); fetched as ` + mediaSignedURL + `", "renditions": []}`,
}

// mount adds the media endpoints to mux.
func (m *fakeMedia) mount(mux *http.ServeMux) {
	writeJSON := func(w http.ResponseWriter, status int, body string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
	lib := func(w http.ResponseWriter, r *http.Request) bool {
		if r.PathValue("name") != "films" {
			writeJSON(w, 404, `{"error": "no media library named \"`+r.PathValue("name")+`\" runs here"}`)
			return false
		}
		return true
	}
	failed := func(w http.ResponseWriter, r *http.Request) bool {
		m.mu.Lock()
		st := m.fail[r.Method+" "+r.URL.Path]
		m.mu.Unlock()
		if st == 409 {
			writeJSON(w, st, `{"error": "the item is in use", "usage": {"channels": ["tv/late"], "in_use": true}}`)
			return true
		}
		if st != 0 {
			writeJSON(w, st, `{"error": "scripted failure"}`)
			return true
		}
		return false
	}
	change := func(w http.ResponseWriter, r *http.Request) string {
		body, _ := io.ReadAll(r.Body)
		m.mu.Lock()
		m.changes = append(m.changes, r.Method+" "+r.URL.Path+" "+r.Header.Get(ConsoleUserHeader))
		m.bodies = append(m.bodies, string(body))
		m.mu.Unlock()
		return string(body)
	}
	mux.HandleFunc("GET /media/library", func(w http.ResponseWriter, r *http.Request) {
		if failed(w, r) {
			return
		}
		writeJSON(w, 200, `{"libraries": [{"name": "films", "roots": ["/srv/media/films"], "status": {"scans": 3}, "items": 3, "errors": 1, "problems": 1, "bytes": 123456789,
  "pool": {"queued": 0, "busy": 1, "refused": 0, "expired": 2}}]}`)
	})
	mux.HandleFunc("GET /media/library/{name}", func(w http.ResponseWriter, r *http.Request) {
		if !lib(w, r) {
			return
		}
		ids := []string{"films/bbb", "films/broken", "films/news"}
		if r.URL.Query().Get("error") == "true" {
			ids = []string{"films/broken"}
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		var page []string
		for _, id := range ids {
			if limit > 0 && len(page) >= limit {
				break
			}
			page = append(page, mediaItems[id])
		}
		writeJSON(w, 200, `{"name": "films", "items": 3, "errors": 1, "problem_list": [{"path": "films/old.json", "error": "an unknown field"}],
  "matched": `+strconv.Itoa(len(ids))+`, "offset": 0, "limit": `+strconv.Itoa(limit)+`, "page": [`+strings.Join(page, ",")+`]}`)
	})
	mux.HandleFunc("GET /media/library/{name}/items/{id...}", func(w http.ResponseWriter, r *http.Request) {
		if !lib(w, r) {
			return
		}
		it, ok := mediaItems[r.PathValue("id")]
		if !ok {
			writeJSON(w, 404, `{"error": "no item \"`+r.PathValue("id")+`\" in the library"}`)
			return
		}
		writeJSON(w, 200, it)
	})
	mux.HandleFunc("GET /media/library/{name}/usage", func(w http.ResponseWriter, r *http.Request) {
		if !lib(w, r) || failed(w, r) {
			return
		}
		id := r.URL.Query().Get("item")
		m.mu.Lock()
		u, ok := m.usage[id]
		m.mu.Unlock()
		if !ok {
			writeJSON(w, 404, `{"error": "medialib: no such item"}`)
			return
		}
		data, _ := json.Marshal(map[string]any{"library": "films", "item": id, "channels": u.Channels, "playing": u.Playing,
			"sessions": u.Sessions, "sessions_counted": u.Counted, "in_use": u.InUse})
		writeJSON(w, 200, string(data))
	})
	mux.HandleFunc("GET /media/library/{name}/sidecar", func(w http.ResponseWriter, r *http.Request) {
		if !lib(w, r) || failed(w, r) {
			return
		}
		if r.URL.Query().Get("item") == "films/news" {
			writeJSON(w, 200, `{"exists": true, "path": "films/news.json", "modified": "2026-11-01T00:00:00Z", "sidecar": {"id": "films/news", "title": "News"}, "files": ["news.mp4"]}`)
			return
		}
		writeJSON(w, 200, `{"exists": false, "path": "films/bbb.json", "sidecar": {"title": "Big Buck Bunny", "renditions": [{"file": "bbb.mp4"}]}, "files": ["bbb.mp4", "bbb.en.vtt"]}`)
	})
	for _, op := range []string{"rename", "move", "delete"} {
		mux.HandleFunc("POST /media/library/{name}/"+op, func(w http.ResponseWriter, r *http.Request) {
			if !lib(w, r) || failed(w, r) {
				return
			}
			var in struct {
				Item  string `json:"item"`
				Force bool   `json:"force"`
			}
			_ = json.Unmarshal([]byte(change(w, r)), &in)
			m.mu.Lock()
			u := m.usage[in.Item]
			m.mu.Unlock()
			if u.InUse && !in.Force {
				data, _ := json.Marshal(map[string]any{"error": "the item is in use", "usage": u})
				writeJSON(w, 409, string(data))
				return
			}
			writeJSON(w, 200, `{"op": "`+op+`", "item": "`+in.Item+`", "files": [{"from": "films/x.mp4"}]}`)
		})
	}
	mux.HandleFunc("PUT /media/library/{name}/sidecar", func(w http.ResponseWriter, r *http.Request) {
		if !lib(w, r) || failed(w, r) {
			return
		}
		change(w, r)
		writeJSON(w, 200, `{"op": "sidecar", "files": [{"to": "films/bbb.json"}]}`)
	})
	mux.HandleFunc("POST /media/library/{name}/rescan", func(w http.ResponseWriter, r *http.Request) {
		if !lib(w, r) || failed(w, r) {
			return
		}
		change(w, r)
		writeJSON(w, 202, `{"queued": true, "status": {"scanning": true}}`)
	})
	mux.HandleFunc("GET /media/auth", func(w http.ResponseWriter, r *http.Request) {
		if failed(w, r) {
			return
		}
		m.mu.Lock()
		none, two := m.noPolicies, m.twoSigning
		m.mu.Unlock()
		if none {
			writeJSON(w, 200, `{"policies": []}`)
			return
		}
		writeJSON(w, 200, `{"policies": [{"name": "site", "url_signing": true, "settings": {"ttl": "2m0s", "play_grace": "30m0s", "session_idle": "2m0s", "max_sessions": 2, "on_limit": "refuse"},
  "keys": [{"name": "k1", "default": true, "file": true}, {"name": "k0", "file": false}], "sessions": 3, "subjects": 2,
  "refusals": {"expired": 4, "signature": 1, "address": 0, "referer": 0, "origin": 0, "session_limit": 7, "session_ended": 0},
  "token_routes": [{"route": "tokens", "url_base": "https://cdn.example.com", "url_prefixes": ["/vod/films/"], "max_ttl": "1h0m0s"}]},
 {"name": "staff", "url_signing": false, "keys": [{"name": "", "default": true, "file": true}], "sessions": 0, "subjects": 0, "refusals": {}, "token_routes": []},
 {"name": "inline", "url_signing": `+strconv.FormatBool(two)+`, "keys": [{"name": "", "default": true, "file": false}], "sessions": 0, "subjects": 0, "refusals": {}, "token_routes": []}]}`)
	})
	mux.HandleFunc("GET /media/auth/{policy}/sessions", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("policy") != "site" {
			writeJSON(w, 404, `{"error": "no media_auth policy"}`)
			return
		}
		if failed(w, r) {
			return
		}
		q := r.URL.Query()
		offset, _ := strconv.Atoi(q.Get("offset"))
		limit, _ := strconv.Atoi(q.Get("limit"))
		m.mu.Lock()
		var all []sessionView
		for _, ss := range m.sessions {
			if (q.Get("subject") == "" || ss.Subject == q.Get("subject")) && strings.HasPrefix(ss.Resource, q.Get("resource")) {
				ss.Ended = ss.Ended || m.ended[ss.ID]
				all = append(all, ss)
			}
		}
		m.mu.Unlock()
		page := []sessionView{}
		for i := offset; i < len(all) && (limit == 0 || len(page) < limit); i++ {
			page = append(page, all[i])
		}
		data, _ := json.Marshal(map[string]any{"total": len(all), "matched": len(all), "offset": offset, "limit": limit, "max_sessions": 2, "page": page,
			"subjects": []map[string]any{{"subject": mediaAlice, "sessions": 2}}})
		writeJSON(w, 200, string(data))
	})
	mux.HandleFunc("POST /media/auth/{policy}/sessions/end", func(w http.ResponseWriter, r *http.Request) {
		if failed(w, r) {
			return
		}
		var in struct {
			Subject, ID string
		}
		_ = json.Unmarshal([]byte(change(w, r)), &in)
		m.mu.Lock()
		gone := m.ended[in.ID]
		m.ended[in.ID] = true
		m.mu.Unlock()
		if gone {
			writeJSON(w, 404, `{"error": "the policy has no such session playing"}`)
			return
		}
		writeJSON(w, 200, `{"ended": true}`)
	})
	mux.HandleFunc("POST /media/auth/{policy}/rotate", func(w http.ResponseWriter, r *http.Request) {
		if failed(w, r) {
			return
		}
		var in struct {
			Key string `json:"key"`
		}
		_ = json.Unmarshal([]byte(change(w, r)), &in)
		writeJSON(w, 200, `{"rotated": ["`+in.Key+`"], "since": 1762387200}`)
	})
	mux.HandleFunc("GET /media/stats", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, `{"time": 1, "libraries": [{"name": "films", "items": 3, "errors": 1, "pool": {"busy": 1, "expired": 2}}],
  "vod": [{"library": "films", "requests": 900, "bytes": 1000000, "cache": {"entries": 10, "hits": 50, "misses": 450}}],
  "policies": [{"name": "site", "sessions": 3, "refusals": {"session_limit": 7}, "resources": [{"resource": "/vod/films/films/bbb/", "sessions": 2}]}]}`)
	})
	channel := `{"name": "tv/films", "library": "films", "state": "playing", "order": "sequence", "loop": true, "items": 2,
  "now": {"item": "films/bbb", "title": "Big Buck Bunny", "from": "list", "duration": 596, "position": 42.5},
  "next": {"item": "films/news", "title": "News", "from": "list", "duration": 600},
  "played": 12, "failed": 1, "underruns": 3,
  "push": [{"name": "yt", "stream": "tv/films", "channel": "tv/films", "protocol": "rtmp", "url": "rtmp://a.rtmp.youtube.com/live2", "state": "retrying",
    "error": "publish refused: NetStream.Publish.BadName", "retry_in": 8, "connects": 0, "failures": 9, "bytes": 0, "bitrate": 0}]}`
	mux.HandleFunc("GET /streaming/channels", func(w http.ResponseWriter, r *http.Request) {
		if failed(w, r) {
			return
		}
		writeJSON(w, 200, `{"channels": [`+channel+`]}`)
	})
	mux.HandleFunc("GET /streaming/channels/{name...}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("name") != "tv/films" {
			writeJSON(w, 404, `{"error": "no channel named \"`+r.PathValue("name")+`\""}`)
			return
		}
		writeJSON(w, 200, channel)
	})
	mux.HandleFunc("GET /streaming/push", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, `{"push": [{"name": "yt", "stream": "tv/films", "channel": "tv/films", "state": "retrying"},
  {"name": "live/cam#1", "stream": "live/cam", "protocol": "rtmp", "url": "rtmp://cdn.example.net/live", "state": "connected", "bytes": 1000}]}`)
	})
}
