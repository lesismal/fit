package assistant

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/lesismal/fit/adapter/fitconf"
	"github.com/lesismal/fit/adapter/yaml"
	"github.com/lesismal/fit/assistant/internal/fakeapi"
	"github.com/lesismal/fit/config"
)

// propSecret is a secret of fakeConfigs' config, which a proposal may
// carry back where it was and nowhere else.
const propSecret = "basic-secret-0123456789"

// propBase is the config fakeConfigs starts with: it loads, and holds a
// secret in a route's middleware.
const propBase = `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
"upstreams": [{"name": "app", "addresses": ["10.0.0.7:9000"]}],
"routes": [{"name": "api", "match": {"path": ["/api/*"]}, "middleware": [{"module": "headers", "params": {"secret": "` + propSecret + `"}}],
  "handler": {"module": "proxy", "params": {"upstream": "app"}}}],
"admin": {"address": "127.0.0.1:2019", "max_conns": 8}}`

// fakeConfigs is the Admin API's config endpoints as a proposal uses them:
// versions with ETags, If-Match, PUT /config/text by members, reconcile,
// rollback, the cache, and render in the three formats.
type fakeConfigs struct {
	mu       sync.Mutex
	versions [][]byte // version n is versions[n-1]
	changes  []string // the changes asked for: method, path, If-Match
	bodies   []string
	check    string
	fail     int // when not 0, a change answers it
	zones    []string
	purges   []string
	down     bool            // every request answers 500
	broken   map[string]bool // these requests, "METHOD /path", answer 500
	// failPurge fails a purge of a zone, as when it went since the check.
	failPurge bool
}

func newFakeConfigs(data string) *fakeConfigs {
	return &fakeConfigs{versions: [][]byte{[]byte(data)}, check: `{"differences":[]}`, zones: []string{"main"}}
}

func etagOf(data []byte) string {
	s := sha256.Sum256(data)
	return `"` + hex.EncodeToString(s[:6]) + `"`
}

func (f *fakeConfigs) current() (uint64, []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return uint64(len(f.versions)), f.versions[len(f.versions)-1]
}

// set applies data as a new version, as a change made elsewhere.
func (f *fakeConfigs) set(data string) {
	f.mu.Lock()
	f.versions = append(f.versions, []byte(data))
	f.mu.Unlock()
}

func (f *fakeConfigs) changed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.changes)
}

func (f *fakeConfigs) handler() http.Handler {
	mux := http.NewServeMux()
	writeJSON := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("GET /config", func(w http.ResponseWriter, r *http.Request) {
		n, data := f.current()
		w.Header().Set("ETag", etagOf(data))
		w.Header().Set("Fit-Config-Version", strconv.FormatUint(n, 10))
		_, _ = w.Write(data)
	})
	mux.HandleFunc("GET /versions/{n}", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.PathValue("n"))
		f.mu.Lock()
		defer f.mu.Unlock()
		if n < 1 || n > len(f.versions) {
			writeJSON(w, 404, map[string]string{"error": "not kept"})
			return
		}
		_, _ = w.Write(f.versions[n-1])
	})
	mux.HandleFunc("GET /config/check", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		_, _ = io.WriteString(w, f.check)
	})
	mux.HandleFunc("GET /cache", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var zs []map[string]any
		for _, z := range f.zones {
			zs = append(zs, map[string]any{"zone": z, "keys": 1})
		}
		writeJSON(w, 200, zs)
	})
	mux.HandleFunc("POST /cache/purge", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req PurgeStep
		_ = json.Unmarshal(body, &req)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.purges = append(f.purges, string(body))
		if req.Zone != "" && !slices.Contains(f.zones, req.Zone) || f.failPurge && req.Zone != "" {
			writeJSON(w, 404, map[string]string{"error": "no cache uses the zone"})
			return
		}
		writeJSON(w, 200, map[string]int{"purged": 2})
	})
	mux.HandleFunc("POST /config/render", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var out []byte
		var err error
		if r.URL.Query().Get("format") == "conf" {
			out, err = fitconf.FromJSON(body)
		} else {
			out, err = yaml.FromJSON(body)
		}
		if err == nil && bytes.Contains(body, []byte("render-fails")) {
			err = errors.New("the text cannot be rendered")
		}
		if err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		_, _ = w.Write(out)
	})
	change := func(w http.ResponseWriter, r *http.Request, edit func(cur []byte) ([]byte, error)) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		defer f.mu.Unlock()
		cur := f.versions[len(f.versions)-1]
		f.changes = append(f.changes, r.Method+" "+r.URL.RequestURI()+" "+r.Header.Get("If-Match"))
		f.bodies = append(f.bodies, string(body))
		switch {
		case r.Header.Get("If-Match") != etagOf(cur):
			writeJSON(w, 412, map[string]string{"error": "the config changed"})
			return
		case f.fail != 0:
			writeJSON(w, f.fail, map[string]any{"error": "the config was not applied; the running config stays",
				"errors": []map[string]string{{"path": "listeners[0].address", "message": "address in use"}}})
			return
		}
		data, err := edit(cur)
		if err == nil {
			_, err = config.Load(data)
		}
		if err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		f.versions = append(f.versions, data)
		writeJSON(w, 200, map[string]any{"version": len(f.versions), "etag": etagOf(data), "warnings": []string{"a warning"}})
	}
	mux.HandleFunc("PUT /config/text", func(w http.ResponseWriter, r *http.Request) {
		members := strings.Split(r.URL.Query().Get("members"), ",")
		change(w, r, func(cur []byte) ([]byte, error) {
			var base, frag map[string]json.RawMessage
			_ = json.Unmarshal(cur, &base)
			body := []byte(f.bodies[len(f.bodies)-1])
			if err := json.Unmarshal(body, &frag); err != nil {
				return nil, err
			}
			for _, m := range members {
				if v, ok := frag[m]; ok {
					base[m] = v
				} else {
					delete(base, m)
				}
			}
			return json.Marshal(base)
		})
	})
	mux.HandleFunc("POST /config/reconcile", func(w http.ResponseWriter, r *http.Request) {
		change(w, r, func(cur []byte) ([]byte, error) { return bytes.Replace(cur, []byte(`"1h"`), []byte(`"2h"`), 1), nil })
	})
	mux.HandleFunc("POST /rollback/{n}", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.PathValue("n"))
		change(w, r, func([]byte) ([]byte, error) { return f.versions[n-1], nil })
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		down, broken := f.down, f.broken[r.Method+" "+r.URL.Path]
		f.mu.Unlock()
		if down || broken {
			w.WriteHeader(500)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// propFixture is a chat fixture on fakeConfigs.
type propFixture struct {
	*chatFixture
	cfgs *fakeConfigs
	sc   *script
}

// startProposals starts an assistant on fakeConfigs, with settings extra,
// whose model answers sc's turns.
func startProposals(t *testing.T, extra string, turns ...fakeapi.Turn) *propFixture {
	t.Helper()
	cfgs := newFakeConfigs(propBase)
	sc := &script{turns: turns}
	f := startChat(t, extra, sc.next, func(o *Options) { o.API = cfgs.handler() })
	return &propFixture{chatFixture: f, cfgs: cfgs, sc: sc}
}

// view reads the conversation's proposal as ada's browser does.
func (f *propFixture) view(conv string) (int, *ProposalView, string) {
	f.t.Helper()
	status, body := f.do("GET", "/api/m/ai/proposals/"+conv, "")
	var out struct {
		Proposal *ProposalView `json:"proposal"`
	}
	_ = json.Unmarshal([]byte(body), &out)
	return status, out.Proposal, body
}

// applyIt confirms the proposal v shows.
func (f *propFixture) applyIt(conv string, v *ProposalView) (int, applyWire) {
	f.t.Helper()
	body, _ := json.Marshal(map[string]any{"id": v.ID, "revision": v.Revision, "etag": v.ETag, "user": v.User})
	status, out := f.do("POST", "/api/m/ai/proposals/"+conv+"/apply", string(body))
	var res applyWire
	_ = json.Unmarshal([]byte(out), &res)
	res.raw = out
	return status, res
}

type applyWire struct {
	Outcome  string        `json:"outcome"`
	Version  uint64        `json:"version"`
	Warnings []string      `json:"warnings"`
	Purged   []purgeResult `json:"purged"`
	Proposal *ProposalView `json:"proposal"`
	Error    string        `json:"error"`
	Errors   []apiProblem  `json:"errors"`
	raw      string
}

func (f *propFixture) audit(query string) []AuditEntry {
	f.t.Helper()
	status, body := f.do("GET", "/api/m/ai/audit"+query, "")
	var out struct {
		Entries []AuditEntry `json:"entries"`
	}
	if status != 200 || json.Unmarshal([]byte(body), &out) != nil {
		f.t.Fatalf("audit: %d %s", status, body)
	}
	return out.Entries
}

func call(id, name, args string) fakeapi.Call { return fakeapi.Call{ID: id, Name: name, Args: args} }

// TestProposeAndApply: a change asked in words becomes one proposal of two
// steps, checked and shown with its diff in three formats; nothing is
// applied until the confirm, and then the steps go as one change with
// If-Match, a new version, its purge after it; the audit log holds the
// chat and the apply, and the model hears of it.
func TestProposeAndApply(t *testing.T) {
	f := startProposals(t, `"default_level": "apply"`,
		fakeapi.Turn{Calls: []fakeapi.Call{
			call("p1", ToolConfigPropose, `{"summary": "Add a route for /health", "ops": [{"op": "add", "path": "/routes/0", "value": {"name": "health", "match": {"path": ["/health"]}, "handler": {"module": "respond", "params": {"body": "ok"}}}}]}`),
			call("p2", ToolConfigPropose, `{"summary": "A second backend", "members": {"upstreams": [{"name": "app", "addresses": ["10.0.0.7:9000", "10.0.0.8:9000"]}]}}`),
			call("p3", ToolPurgePropose, `{"summary": "Drop the cached health page", "zone": "main", "urls": ["https://example.com/health"]}`)}},
		fakeapi.Turn{Text: "The proposal adds /health and a backend."},
		fakeapi.Turn{Text: "Done."})
	s := f.chat(`{"message": "add a /health route and a second backend 10.0.0.8:9000"}`)
	if s.end == nil || s.end.Reason != ReasonAnswer {
		t.Fatalf("%+v %s", s.end, s.err)
	}
	conv := s.of(EventStart)[0].Conversation
	props := s.of(EventProposal)
	if len(props) != 3 || props[0].ID != props[2].ID || props[2].Revision != 3 || props[1].Name != ToolConfigPropose || props[0].Text != "Add a route for /health" {
		t.Fatalf("%+v", props)
	}
	res := s.of(EventToolResult)
	if res[0].Error || !strings.Contains(res[0].Content, `"valid":true`) || !strings.Contains(res[1].Content, `+        \"10.0.0.8:9000\"`) {
		t.Errorf("%+v", res)
	}
	if got := f.cfgs.changed(); len(got) != 0 {
		t.Fatalf("a proposal applied something: %v", got)
	}

	status, v, body := f.view(conv)
	if status != 200 || v == nil || len(v.Steps) != 3 || !v.Valid || !v.CanApply || v.Stale || v.Kind != KindPatch ||
		!slices.Equal(v.Changed, []string{"upstreams", "routes"}) || len(v.Purges) != 1 || v.Version != 1 {
		t.Fatalf("%d %s", status, body)
	}
	for _, f := range diffFormats {
		if !strings.Contains(v.Diff[f], "--- version 1\n+++ proposed\n") || !strings.Contains(v.Diff[f], "10.0.0.8:9000") {
			t.Errorf("%s diff: %s", f, v.Diff[f])
		}
	}
	if strings.Contains(body, propSecret) {
		t.Errorf("the view: %s", body)
	}
	// Reading it changed nothing: no change, and the store's sections
	// as they were.
	before := f.store.Section(sectionOf("ada"))
	f.view(conv)
	if len(f.cfgs.changed()) != 0 || !bytes.Equal(before, f.store.Section(sectionOf("ada"))) {
		t.Error("viewing a proposal changed something")
	}

	// A confirm of another revision is refused.
	stale := *v
	stale.Revision = 1
	if status, out := f.applyIt(conv, &stale); status != 409 || out.Outcome != OutcomeRefused {
		t.Fatalf("%d %s", status, out.raw)
	}
	status, out := f.applyIt(conv, v)
	if status != 200 || out.Outcome != OutcomeApplied || out.Version != 2 || len(out.Purged) != 1 || out.Purged[0].Purged != 2 || out.Warnings[0] != "a warning" {
		t.Fatalf("%d %s", status, out.raw)
	}
	if got := f.cfgs.changed(); len(got) != 1 || got[0] != "PUT /config/text?format=json&members=upstreams%2Croutes "+etagOf([]byte(propBase)) {
		t.Errorf("%v", got)
	}
	_, data := f.cfgs.current()
	if !strings.Contains(string(data), propSecret) || !strings.Contains(string(data), `"health"`) || !strings.Contains(string(data), "10.0.0.8") {
		t.Errorf("applied: %s", data)
	}
	if _, v, _ := f.view(conv); v != nil {
		t.Error("the proposal is still pending")
	}
	if !strings.Contains(f.logText(), `"ada": ada applied the proposal`) {
		t.Error(f.logText())
	}

	entries := f.audit("")
	if len(entries) != 3 || entries[0].Kind != AuditApply || entries[0].Version != 2 || entries[0].Outcome != OutcomeApplied ||
		entries[0].Proposal == nil || len(entries[0].Proposal.Steps) != 3 || entries[2].Kind != AuditChat || len(entries[2].Calls) != 3 ||
		entries[2].Proposal == nil || entries[2].Outcome != ReasonAnswer || entries[1].Outcome != OutcomeRefused {
		t.Errorf("%+v", entries)
	}

	// The model hears what became of it.
	f.chat(`{"conversation": "` + conv + `", "message": "thanks"}`)
	reqs := f.model.Requests()
	if sys := fakeapi.System(reqs[len(reqs)-1]); !strings.Contains(sys, "as config version 2") || !strings.Contains(sys, "Proposals: to change") {
		t.Errorf("%s", sys)
	}
	if sys := fakeapi.System(reqs[1]); !strings.Contains(sys, "Pending proposal") || !strings.Contains(sys, "3. cache_purge_propose: Drop the cached health page") {
		t.Errorf("%s", sys)
	}
	noSecrets(t, reqs)
}
