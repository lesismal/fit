package cluster

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/managed"
)

// cfgMember is a fit as templates see it, through its Admin API's token:
// GET /node, GET and PUT /config, and GET, PUT /node/managed.
type cfgMember struct {
	srv *httptest.Server

	mu      sync.Mutex
	cfg     string
	rev     int
	rec     managed.Record
	noCap   bool // it lists no node.managed
	putFail int  // PUT /node/managed answers this status
	cfgFail int  // GET /config answers this status
	badCfg  bool // GET /config answers what is not an object
	// noManaged answers /node/managed 404; race has PUT /node/managed
	// answer 412 once, as when another read told the member first:
	// "same" keeps what it was sent, "other" keeps nothing, "getfail"
	// fails the read after it.
	noManaged bool
	race      string
	puts      int
	delay     time.Duration
}

func newCfgMember(t *testing.T, cfg string) *cfgMember {
	t.Helper()
	c := &cfgMember{cfg: cfg, rev: 1, rec: managed.Record{Paths: []managed.Path{}}}
	c.srv = httptest.NewServer(http.HandlerFunc(c.serve))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *cfgMember) set(fn func(c *cfgMember)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fn(c)
}

func (c *cfgMember) record() managed.Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rec
}

func (c *cfgMember) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+fakeToken {
		w.WriteHeader(401)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	time.Sleep(c.delay)
	w.Header().Set("Content-Type", "application/json")
	etag := `"c` + strconv.Itoa(c.rev) + `"`
	switch r.Method + " " + r.URL.Path {
	case "GET /node":
		caps := []string{"config.if_match", "node", "node.managed"}
		if c.noCap {
			caps = []string{"node"}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "id-" + c.srv.Listener.Addr().String(), "hostname": "host-x",
			"fit_version": "v1", "schema_digest": "sha256:other", "capabilities": caps})
	case "GET /config":
		switch {
		case c.cfgFail != 0:
			w.WriteHeader(c.cfgFail)
			_, _ = w.Write([]byte(`{"error":"no config"}`))
		case c.badCfg:
			_, _ = w.Write([]byte(`[1]`))
		default:
			w.Header().Set("ETag", etag)
			_, _ = w.Write([]byte(c.cfg))
		}
	case "PUT /config":
		if r.Header.Get("If-Match") != etag {
			w.WriteHeader(412)
			return
		}
		b, _ := io.ReadAll(r.Body)
		c.cfg = string(b)
		c.rev++
		_, _ = w.Write([]byte(`{}`))
	case "GET /node/managed":
		if c.race == "getfail-now" {
			c.race = ""
			w.WriteHeader(500)
			return
		}
		if c.noManaged {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("ETag", c.rec.ETag())
		_ = json.NewEncoder(w).Encode(c.rec)
	case "PUT /node/managed":
		c.puts++
		if c.putFail != 0 {
			w.WriteHeader(c.putFail)
			_, _ = w.Write([]byte(`{"error":"cannot keep it"}`))
			return
		}
		if c.race != "" {
			var sent managed.Record
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &sent)
			sent.Normalize()
			switch c.race {
			case "same":
				c.rec = sent
			case "getfail":
				c.race = "getfail-now"
				w.WriteHeader(412)
				return
			}
			c.race = ""
			w.WriteHeader(412)
			return
		}
		if im := r.Header.Get("If-Match"); im != "" && im != c.rec.ETag() {
			w.WriteHeader(412)
			return
		}
		var rec managed.Record
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &rec)
		rec.Normalize()
		c.rec = rec
		_, _ = w.Write([]byte(`{}`))
	default:
		w.WriteHeader(404)
	}
}

// newTplFixture is a manager with members a, b and c, by token, their
// labels region eu, us and eu; a group edge of every member with region,
// and its variable backend.
func newTplFixture(t *testing.T) (*fixture, *served, map[string]*cfgMember) {
	t.Helper()
	f := newFixture(t, config.Cluster{MaxNodes: 8})
	s := f.serve()
	ms := map[string]*cfgMember{}
	for name, region := range map[string]string{"a": "eu", "b": "us", "c": "eu"} {
		ms[name] = newCfgMember(t, `{"listeners":[{"name":"web","address":"127.0.0.1:0"}],"routes":[{"name":"own","handler":{"module":"respond"}},{"handler":{"module":"x"}}]}`)
		in := tokenInput(name, ms[name].srv.URL)
		in.Labels = map[string]string{"region": region}
		f.add(in)
	}
	if _, err := f.m.AddGroup("tester", &GroupInput{Name: "edge", Selector: "region", Variables: map[string]string{"backend": "10.0.0.1:80"}}); err != nil {
		t.Fatal(err)
	}
	return f, s, ms
}

func (s *served) must(method, path, body string, want int, header ...string) string {
	s.t.Helper()
	code, got, _ := s.do(method, "/api/m/cluster"+path, body, header...)
	if code != want {
		s.t.Fatalf("%s %s: %d %s", method, path, code, got)
	}
	return got
}

// quiet waits for the manager to be done telling members what they own
// after a change, so that a test's own read is the only one.
func quiet(t *testing.T, m *Manager) {
	t.Helper()
	waitFor(t, "the refresh to end", func() bool {
		m.tpl.mu.Lock()
		defer m.tpl.mu.Unlock()
		return !m.tpl.refreshing
	})
}

func syncOne(t *testing.T, f *fixture, s *served, node string) MemberSync {
	t.Helper()
	quiet(t, f.m)
	var b syncBody
	raw := s.must("POST", "/sync", `{"nodes":[`+js(node)+`]}`, 200)
	if err := json.Unmarshal([]byte(raw), &b); err != nil || len(b.Nodes) != 1 {
		t.Fatalf("%s %v", raw, err)
	}
	return b.Nodes[0]
}

func partOf(t *testing.T, ms MemberSync, path string) PartSync {
	t.Helper()
	for _, p := range ms.Parts {
		if p.Path == path {
			return p
		}
	}
	t.Fatalf("no part %s in %+v", path, ms)
	return PartSync{}
}

// TestTemplatesAPI adds, reads, changes and removes templates; renders them
// per member with their variables, an overlap settled by order; follows
// the sync states, each reason of a conflict, and the members told what
// they own; makes a template from a member; compares two members.
func TestTemplatesAPI(t *testing.T) {
	old := refreshDelay
	refreshDelay = 10 * time.Millisecond
	t.Cleanup(func() { refreshDelay = old })
	f, s, ms := newTplFixture(t)
	s.must("GET", "/templates", "", 200)
	tpl := `{"name":"base","groups":["edge"],"config":{"log":{"level":"info"},
"upstreams":[{"name":"api","addresses":["${backend}"],"tls":{"key_file":"/etc/k.pem"}}],
"routes":[{"name":"api","handler":{"module":"respond","params":{"body":"${node.name}/${node.labels.region}/${node.id}/${node.hostname} $${lit}","token":"s3cret-value"}}}]}}`
	v := s.must("POST", "/templates", tpl, 201)
	for _, want := range []string{`"token":"[secret]"`, `"path":"routes[api]"`, `"variables":["backend","node.hostname","node.id","node.labels.region","node.name"]`,
		`upstreams[api].tls.key_file names a file`, `"nodes":["a","b","c"]`} {
		if !strings.Contains(v, want) {
			t.Errorf("%s lacks %s", v, want)
		}
	}
	if strings.Contains(v, "s3cret-value") {
		t.Fatal("a secret answered")
	}
	s.must("POST", "/templates", tpl, 409)
	s.must("POST", "/templates", `{"name":"x","config":{"admin":{}}}`, 400)
	s.must("GET", "/templates/none", "", 404)
	s.must("PUT", "/templates/none", `{}`, 404)
	s.must("DELETE", "/templates/none", "", 404)
	s.must("PATCH", "/templates/base", "", 405)
	s.must("GET", "/templates/base/x", "", 404)
	s.must("PUT", "/templates/base", `{"from":{"node":"a","paths":["log"]}}`, 400)
	s.must("PUT", "/templates/base", `{}`, 412, "If-Match", `"0"`)

	// a: out of sync, every part missing; told what it owns.
	st := syncOne(t, f, s, "a")
	if st.State != SyncOutOfSync || partOf(t, st, "routes[api]").Reason != ReasonMissing || st.Managed == "failed" || len(st.Templates) != 1 || st.Note == "" {
		t.Fatalf("%+v", st)
	}
	if want := partOf(t, st, "routes[api]").Want; !strings.Contains(string(want), "a/eu/id-127.0.0.1:") || !strings.Contains(string(want), "/host-x ${lit}") ||
		!strings.Contains(string(want), `"token":"[secret]"`) {
		t.Fatalf("%s", want)
	}
	if rec := ms["a"].record(); rec.Manager != "" || len(rec.Paths) != 3 || rec.Paths[0].Hash != "" {
		t.Fatalf("%+v", rec)
	}
	// Synced again: kept, nothing told.
	puts := ms["a"].puts
	if st := syncOne(t, f, s, "a"); st.Managed != "kept" || ms["a"].puts != puts {
		t.Fatalf("%+v", st)
	}

	// Its desired config applied: in sync; then the template changes: out
	// of sync; changed on the member: a conflict.
	var d desiredBody
	if err := json.Unmarshal([]byte(s.must("GET", "/desired/a", "", 200)), &d); err != nil || !strings.Contains(string(d.Desired), `"own"`) {
		t.Fatalf("%+v %v", d, err)
	}
	f.m.mu.Lock()
	var desired *Desired
	f.m.mu.Unlock()
	done := make(chan struct{})
	if err := f.m.Desired("a", func(dd *Desired, p *Problem) { desired = dd; close(done) }); err != nil {
		t.Fatal(err)
	}
	<-done
	if desired == nil || !strings.Contains(string(desired.Config), "s3cret-value") {
		t.Fatalf("%+v", desired)
	}
	var compact bytes.Buffer
	_ = json.Compact(&compact, desired.Config)
	ms["a"].set(func(c *cfgMember) { c.cfg = compact.String() })
	if st := syncOne(t, f, s, "a"); st.State != SyncInSync || st.Counts.InSync != 3 || st.Managed != "told" || ms["a"].record().Paths[0].Hash == "" {
		t.Fatalf("%+v", st)
	}
	if err := f.m.Desired("nobody", nil); err == nil {
		t.Error("nobody")
	}
	cur := s.must("GET", "/templates/base", "", 200)
	var tv TemplateView
	_ = json.Unmarshal([]byte(cur), &tv)
	// The secret stays as kept, sent back masked.
	changed := strings.Replace(string(tv.Config), `"level":"info"`, `"level":"debug"`, 1)
	s.must("PUT", "/templates/base", `{"groups":["edge"],"config":`+changed+`}`, 200)
	st = syncOne(t, f, s, "a")
	if p := partOf(t, st, "log"); p.State != SyncOutOfSync || p.Reason != ReasonTemplateChanged || partOf(t, st, "routes[api]").State != SyncInSync {
		t.Fatalf("%+v", st)
	}
	ms["a"].set(func(c *cfgMember) {
		c.cfg = strings.Replace(c.cfg, `{"name":"api","addresses"`, `{"name":"api2","addresses"`, 1)
	})
	if st := syncOne(t, f, s, "a"); partOf(t, st, "upstreams[api]").Reason != ReasonRemovedHere || st.State != SyncConflict {
		t.Fatalf("%+v", st)
	}
	ms["a"].set(func(c *cfgMember) { c.cfg = strings.Replace(c.cfg, "s3cret-value", "other", 1) })
	if st := syncOne(t, f, s, "a"); partOf(t, st, "routes[api]").Reason != ReasonChangedHere {
		t.Fatalf("%+v", st)
	}
	// b has a log of its own: its own value, a conflict.
	ms["b"].set(func(c *cfgMember) { c.cfg = `{"log":{"level":"warn"}}` })
	if st := syncOne(t, f, s, "b"); partOf(t, st, "log").Reason != ReasonOwnValue {
		t.Fatalf("%+v", st)
	}

	// An overlap: a later template wins; a variable missing is an error.
	s.must("POST", "/groups", `{"name":"eu","selector":"region=eu","variables":{"level":"error"}}`, 201)
	s.must("POST", "/templates", `{"name":"eu-log","groups":["eu"],"order":5,"config":{"log":{"level":"${level}"}}}`, 201)
	st = syncOne(t, f, s, "c")
	if p := partOf(t, st, "log"); p.Template != "eu-log" || !slices.Equal(p.Overrides, []string{"base"}) || !strings.Contains(string(p.Want), `"error"`) {
		t.Fatalf("%+v", st)
	}
	s.must("POST", "/templates", `{"name":"needs","groups":["eu"],"config":{"engine":{"x":"${nope}"}}}`, 201)
	if st := syncOne(t, f, s, "c"); st.State != SyncError || len(st.Errors) != 1 || !strings.Contains(st.Errors[0], "nope") {
		t.Fatalf("%+v", st)
	}
	s.must("DELETE", "/templates/needs", "", 200)

	// The group renamed: the templates follow; removed: they leave it.
	s.must("PUT", "/groups/eu", `{"name":"europe","selector":"region=eu","variables":{"level":"error"}}`, 200)
	if v := s.must("GET", "/templates/eu-log", "", 200); !strings.Contains(v, `"groups":["europe"]`) {
		t.Fatal(v)
	}
	s.must("DELETE", "/groups/europe", "", 200)
	if v := s.must("GET", "/templates/eu-log", "", 200); !strings.Contains(v, `"groups":[]`) {
		t.Fatal(v)
	}
	// Renamed, with a group no one has.
	s.must("PUT", "/templates/eu-log", `{"name":"eu-log2","groups":["ghost"],"config":{"log":{}}}`, 200)
	if v := s.must("GET", "/templates/eu-log2", "", 200); !strings.Contains(v, `"missing_groups":["ghost"]`) {
		t.Fatal(v)
	}
	s.must("PUT", "/templates/eu-log2", `{"name":"base","config":{}}`, 409)

	// Every member, and the states kept.
	var all syncBody
	_ = json.Unmarshal([]byte(s.must("POST", "/sync", `{}`, 200)), &all)
	if len(all.Nodes) != 3 || len(all.Pending) != 0 {
		t.Fatalf("%+v", all)
	}
	var kept struct{ Nodes []MemberSync }
	_ = json.Unmarshal([]byte(s.must("GET", "/sync", "", 200)), &kept)
	if len(kept.Nodes) != 3 || kept.Nodes[0].Parts[0].Want != nil {
		t.Fatalf("%+v", kept)
	}
	s.must("POST", "/sync", `{"template":"base"}`, 200)
	s.must("POST", "/sync", `{"template":"none"}`, 404)
	s.must("POST", "/sync", `{"nodes":["none"]}`, 404)
	s.must("POST", "/sync", `{"nodes":["a","a"]}`, 200)
	s.must("POST", "/sync", `[`, 400)
	s.must("POST", "/sync", `{"nodes":[`+strings.Repeat(`"a",`, 8)+`"a"]}`, 400)
	s.must("POST", "/sync", strings.Repeat(" ", MaxBodyBytes+1), 413)

	// A template from a member's config; the import's parts.
	imp := s.must("POST", "/import", `{"node":"b"}`, 200)
	if !strings.Contains(imp, `"path":"log","member":"log","ownable":true`) {
		t.Fatal(imp)
	}
	imp = s.must("POST", "/import", `{"node":"c"}`, 200)
	if !strings.Contains(imp, `"path":"routes[#1]","member":"routes","ownable":false`) {
		t.Fatal(imp)
	}
	s.must("POST", "/import", `{"node":"none"}`, 404)
	s.must("POST", "/import", `[`, 400)
	s.must("POST", "/import", strings.Repeat(" ", MaxBodyBytes+1), 413)
	s.must("POST", "/templates", `{"name":"from-c","groups":["edge"],"from":{"node":"c","paths":["routes[own]","listeners[web]","listeners[web]"]}}`, 201)
	s.must("POST", "/templates", `{"name":"x","from":{"node":"c","paths":["routes[gone]"]}}`, 400)
	s.must("POST", "/templates", `{"name":"x","from":{"node":"c","paths":["nope"]}}`, 400)
	s.must("POST", "/templates", `{"name":"x","from":{"node":"c","paths":[]}}`, 400)
	s.must("POST", "/templates", `{"name":"x","from":{"node":"none","paths":["log"]}}`, 404)
	if st := syncOne(t, f, s, "c"); partOf(t, st, "routes[own]").State != SyncInSync || partOf(t, st, "listeners[web]").State != SyncInSync {
		t.Fatalf("%+v", st)
	}

	// Compare.
	if !strings.Contains(s.must("POST", "/compare", `{"a":"a","b":"c"}`, 200), `"state":"same"`) {
		t.Error("same")
	}
	cmp := s.must("POST", "/compare", `{"a":"a","b":"b"}`, 200)
	for _, want := range []string{`"state":"only_a"`, `"path":"log","state":"differs"`} {
		if !strings.Contains(cmp, want) {
			t.Errorf("%s lacks %s", cmp, want)
		}
	}
	if !strings.Contains(s.must("POST", "/compare", `{"a":"b","b":"c"}`, 200), `"state":"only_b"`) {
		t.Error("only b")
	}
	s.must("POST", "/compare", `{"a":"a","b":"a"}`, 400)
	s.must("POST", "/compare", `{"a":"a","b":"none"}`, 404)
	s.must("POST", "/compare", `[`, 400)
	s.must("POST", "/compare", strings.Repeat(" ", MaxBodyBytes+1), 413)

	// Members that fail.
	ms["b"].set(func(c *cfgMember) { c.cfgFail = 500 })
	if st := syncOne(t, f, s, "b"); st.State != SyncError || st.Error == nil {
		t.Fatalf("%+v", st)
	}
	s.must("POST", "/compare", `{"a":"a","b":"b"}`, 502)
	s.must("POST", "/templates", `{"name":"x","from":{"node":"b","paths":["log"]}}`, 502)
	ms["b"].set(func(c *cfgMember) { c.cfgFail, c.badCfg = 0, true })
	if st := syncOne(t, f, s, "b"); st.Error == nil || st.Error.Kind != KindBadAnswer {
		t.Fatalf("%+v", st)
	}
	s.must("POST", "/import", `{"node":"b"}`, 502)
	ms["b"].set(func(c *cfgMember) { c.badCfg, c.putFail, c.cfg, c.rec = false, 500, `{}`, managed.Record{} })
	if st := syncOne(t, f, s, "b"); st.Managed != "failed" || st.ManagedError == "" {
		t.Fatalf("%+v", st)
	}
	ms["b"].set(func(c *cfgMember) { c.putFail, c.noCap = 0, true })
	if st := syncOne(t, f, s, "b"); st.Managed != "unsupported" {
		t.Fatalf("%+v", st)
	}
	s.must("GET", "/desired/none", "", 404)

	// Removing every template: a is told it owns nothing, by the refresh.
	for _, n := range []string{"base", "eu-log2", "from-c"} {
		s.must("DELETE", "/templates/"+n, "", 200)
	}
	waitFor(t, "a told it owns nothing", func() bool { return len(ms["a"].record().Paths) == 0 })
	if st := syncOne(t, f, s, "a"); st.State != SyncUnmanaged || len(st.Released) != 0 {
		t.Fatalf("%+v", st)
	}
	if audit := js(f.m.Audit()); !strings.Contains(audit, `"action":"template.update","template":"base","detail":"config (log)"`) {
		t.Errorf("%s", audit)
	}
}

// TestTemplateInput checks a template's fields and its config.
func TestTemplateInput(t *testing.T) {
	now := time.Now()
	ok := TemplateInput{Name: "t", Groups: []string{" g", "g", ""}, Config: json.RawMessage(`{"log":{}}`)}
	tp, err := ok.template(nil, now)
	if err != nil || !slices.Equal(tp.Groups, []string{"g"}) {
		t.Fatalf("%+v %v", tp, err)
	}
	if tp, err := (&TemplateInput{Name: "t"}).template(nil, now); err != nil || string(tp.Config) != "{}" {
		t.Fatalf("%+v %v", tp, err)
	}
	many := make([]string, MaxTemplateGroups+1)
	for i := range many {
		many[i] = "g" + strconv.Itoa(i)
	}
	big := `{"routes":[`
	for i := range MaxTemplateParts + 1 {
		if i > 0 {
			big += ","
		}
		big += `{"name":"r` + strconv.Itoa(i) + `"}`
	}
	big += `]}`
	for field, in := range map[string]TemplateInput{
		"name":        {Name: "bad name"},
		"description": {Name: "t", Description: "a\nb"},
		"groups":      {Name: "t", Groups: many},
		"order":       {Name: "t", Order: MaxOrder + 1},
	} {
		if _, err := in.template(nil, now); err == nil || err.(*FieldError).Field != field {
			t.Errorf("%s: %v", field, err)
		}
	}
	if _, err := (&TemplateInput{Name: "t", Groups: []string{"bad name"}}).template(nil, now); err == nil {
		t.Error("a bad group")
	}
	for _, c := range []string{`[`, `[1]`, `{"nope":1}`, `{"admin":{}}`, `{"schema_version":1}`, `{"routes":{}}`, `{"routes":[1]}`, `{"routes":[{}]}`,
		`{"routes":[{"name":"a"},{"name":"a"}]}`, `{"routes":[{"name":"a-${x}"}]}`, big, `{"log":{"x":"${node.nope}"}}`, `{"log":{"x":"${1x}"}}`,
		`{"log":{"x":"${node.labels.-x}"}}`, `{"log":{"x":"${open"}}`, `{"log":"` + strings.Repeat("x", MaxTemplateBytes) + `"}`,
		`{"log":{"token":"[secret]"}}`, `{"routes":[{"name":"` + strings.Repeat("x", 300) + `"}]}`} {
		if _, err := (&TemplateInput{Name: "t", Config: json.RawMessage(c)}).template(nil, now); err == nil {
			t.Errorf("%.80s: taken", c)
		}
	}
	// Secrets put back by name, by index and by member; a mask with none.
	old := Template{Config: json.RawMessage(`{"routes":[{"name":"a","secret":"s1"}],"log":{"keys":["k0","k1"]}}`)}
	in := TemplateInput{Name: "t", Config: json.RawMessage(`{"log":{"keys":["[secret]","new"]},"routes":[{"name":"b"},{"name":"a","secret":"[secret]"}]}`)}
	tp, err = in.template(&old, now)
	if err != nil || !strings.Contains(string(tp.Config), `"secret":"s1"`) || !strings.Contains(string(tp.Config), `["k0","new"]`) {
		t.Fatalf("%s %v", tp.Config, err)
	}
	in.Config = json.RawMessage(`{"log":{"keys":["k","[secret]","[secret]"]}}`)
	if _, err := in.template(&old, now); err == nil {
		t.Error("a mask past the kept list")
	}
	in.Config = json.RawMessage(`{"log":["[secret]"]}`)
	if _, err := in.template(&old, now); err == nil {
		t.Error("a mask in another shape")
	}
	// What a change says.
	a := Template{Name: "a", Description: "d", Groups: []string{"g"}, Order: 1, Config: json.RawMessage(`{"log":{},"engine":{}}`)}
	b := Template{Name: "b", Config: json.RawMessage(`{"log":{"x":1},"routes":[{"name":"r"}]}`)}
	if got := templateChanges(&a, &b); got != "name, description, groups, order, config (log, routes[r], engine)" {
		t.Error(got)
	}
	if got := templateChanges(&a, &a); got != "nothing" {
		t.Error(got)
	}
	if partChanges(json.RawMessage(`[`), a.Config) != "" {
		t.Error("bad config")
	}
	if got := describeTemplate(&TemplateView{}); got != "owns nothing" {
		t.Error(got)
	}
	if _, _, err := parsePart("routes"); err == nil {
		t.Error("a list whole")
	}
	if m, n, err := parsePart(" routes[a[b]] "); err != nil || m != "routes" || n != "a[b]" {
		t.Error(m, n, err)
	}
}

// TestSecretsAndVariables: what is masked, and how variables expand.
func TestSecretsAndVariables(t *testing.T) {
	v, _ := parseJV([]byte(`{"rate_limit":{"key":"ip"},"x":{"key":"abc"},"push":[{"url":"rtmp://k"}],"password_file":"/f","n":{"api_key":"z"},"e":""}`))
	got := string(masked(v).bytes())
	if got != `{"rate_limit":{"key":"ip"},"x":{"key":"[secret]"},"push":[{"url":"[secret]"}],"password_file":"/f","n":{"api_key":"[secret]"},"e":""}` {
		t.Error(got)
	}
	for in, want := range map[string]string{"a": "a", "${x}-$${y}": "X-${y}", "$$${x}": "$${x}"} {
		got, err := expand(in, func(string) (string, error) { return "X", nil }, nil)
		if err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	if _, err := expand("${x}", func(string) (string, error) { return "", errors.New("no") }, nil); err == nil {
		t.Error("lookup error")
	}
	in := &renderInput{name: "n", labels: map[string]string{"r": "eu"}, groups: map[string]map[string]string{"g": {}},
		templates: []Template{{Name: "t", Groups: []string{"g"}, Config: json.RawMessage(`{"log":{"a":"${node.id}"}}`)},
			{Name: "u", Groups: []string{"g"}, Config: json.RawMessage(`{"engine":{"a":"${node.labels.zz}"}}`)},
			{Name: "bad", Groups: []string{"g"}, Config: json.RawMessage(`[`)}}}
	if _, _, errs := in.render(); len(errs) != 3 {
		t.Errorf("%q", errs)
	}
	// desired: a list made, an element added, one replaced.
	cfg, _ := parseJV([]byte(`{"routes":"odd","upstreams":[{"name":"u","x":1}]}`))
	d := desired(cfg, []*rendered{{member: "routes", name: "r", value: &jv{kind: 's', str: "v"}},
		{member: "upstreams", name: "u", value: &jv{kind: 's', str: "w"}}, {member: "upstreams", name: "z", value: &jv{kind: 's', str: "z"}}})
	if got := string(d.bytes()); got != `{"routes":["v"],"upstreams":["w","z"]}` {
		t.Error(got)
	}
	if lookupJV(cfg, "routes", "r") != nil {
		t.Error("not a list")
	}
}

// TestSyncBounds: too many reads at once are refused, and a read that
// outlasts syncWait answers its members pending.
func TestSyncBounds(t *testing.T) {
	f, s, ms := newTplFixture(t)
	old := syncWait
	syncWait = 50 * time.Millisecond
	t.Cleanup(func() { syncWait = old })
	ms["a"].set(func(c *cfgMember) { c.delay = 300 * time.Millisecond })
	var b syncBody
	_ = json.Unmarshal([]byte(s.must("POST", "/sync", `{"nodes":["a"]}`, 200)), &b)
	if len(b.Pending) != 1 || len(b.Nodes) != 0 {
		t.Fatalf("%+v", b)
	}
	f.m.tpl.mu.Lock()
	f.m.tpl.syncs = MaxSyncs
	f.m.tpl.mu.Unlock()
	s.must("POST", "/sync", `{}`, 429)
	f.m.tpl.mu.Lock()
	f.m.tpl.syncs = 0
	f.m.tpl.mu.Unlock()
	for range MaxReads {
		f.m.reads <- struct{}{}
	}
	s.must("GET", "/desired/a", "", 429)
	s.must("POST", "/import", `{"node":"a"}`, 429)
	for range MaxReads {
		<-f.m.reads
	}
	waitFor(t, "a read", func() bool { return len(f.m.SyncStates()) == 1 })
}

// TestTextEndpoints: a template's text tabs.
func TestTextEndpoints(t *testing.T) {
	_, s, _ := newTplFixture(t)
	for _, fmtName := range textFormats {
		text := s.must("POST", "/text/render?format="+fmtName, `{"log":{"level":"${lvl}"}}`, 200)
		code, got, _ := s.do("POST", "/api/m/cluster/text/parse?format="+fmtName, text, "Content-Type", "text/plain")
		if code != 200 || !strings.Contains(got, `"level": "${lvl}"`) {
			t.Errorf("%s: %d %s from %s", fmtName, code, got, text)
		}
	}
	s.must("POST", "/text/render?format=xml", `{}`, 400)
	s.must("POST", "/text/parse?format=xml", `{}`, 400)
	s.must("POST", "/text/render?format=json", `[1]`, 400)
	s.must("POST", "/text/render?format=json", strings.Repeat(" ", MaxBodyBytes+1), 413)
	s.must("POST", "/text/parse?format=json", strings.Repeat(" ", MaxBodyBytes+1), 413)
	s.must("POST", "/text/parse?format=json", `[1]`, 400)
	s.must("POST", "/text/parse?format=json", `{`, 400)
	s.must("POST", "/text/parse?format=yaml", "a: [", 400)
	if got := s.must("POST", "/text/parse?format=json", ``, 200); strings.TrimSpace(got) != "{}" {
		t.Error(got)
	}
}

// TestJV reads and writes the values templates and configs are kept as.
func TestJV(t *testing.T) {
	v, err := parseJV([]byte(`{"b":[true,false,null,1.50,"<&>"],"a":{"b":1,"b":2}}`))
	if err != nil || string(v.bytes()) != `{"b":[true,false,null,1.50,"<&>"],"a":{"b":2}}` {
		t.Fatalf("%s %v", v.bytes(), err)
	}
	if v.clone() == v || (*jv)(nil).clone() != nil || (*jv)(nil).get("x") != nil {
		t.Error("clone")
	}
	for _, bad := range []string{`{"a":1} 2`, `[1,`, `{"a":`, `{"a"`, ``, `]`, strings.Repeat("[", maxDepth+2) + strings.Repeat("]", maxDepth+2)} {
		if _, err := parseJV([]byte(bad)); err == nil {
			t.Errorf("%.20s: read", bad)
		}
	}
	// A walk that fails stops, from inside arrays and objects.
	v, _ = parseJV([]byte(`{"a":[{"b":"x"}]}`))
	if err := v.walkStrings(nil, func([]string, string) (string, error) { return "", errors.New("no") }); err == nil {
		t.Error("walk")
	}
	for name, want := range map[string]bool{"Password": true, "db_password_env": false, "max_tokens": false, "token_ttl": false, "psk": true,
		"session_secret": true, "x_signature": true, "name": false, "tokens": true} {
		if secretName(name) != want {
			t.Errorf("%s", name)
		}
	}
	paths := make([]string, MaxTemplateParts+1)
	for i := range paths {
		paths[i] = "log"
	}
	if _, err := fragmentOf(v, paths); err == nil {
		t.Error("too many parts")
	}
}

// TestSyncFailures: a member down, one whose /node/managed fails, and the
// desired config of a member that cannot be read.
func TestSyncFailures(t *testing.T) {
	f, s, ms := newTplFixture(t)
	s.must("POST", "/templates", strings.Repeat(" ", MaxBodyBytes+1), 413)
	for _, n := range []string{"t1", "t2", "t3"} {
		s.must("POST", "/templates", `{"name":"`+n+`","groups":["edge"],"config":{"log":{}}}`, 201)
	}
	if v := s.must("GET", "/templates", "", 200); !strings.Contains(v, `"max_templates":128`) || strings.Index(v, `"t1"`) > strings.Index(v, `"t3"`) {
		t.Fatal(v)
	}
	ms["a"].srv.Close()
	if st := syncOne(t, f, s, "a"); st.State != SyncError || st.Error == nil {
		t.Fatalf("%+v", st)
	}
	done := make(chan *Problem, 1)
	if err := f.m.Desired("a", func(d *Desired, p *Problem) { done <- p }); err != nil {
		t.Fatal(err)
	}
	if p := <-done; p == nil {
		t.Error("desired of a member down")
	}
	// /node/managed answers 404: a fit that serves no such endpoint.
	ms["b"].set(func(c *cfgMember) { c.noManaged = true })
	if st := syncOne(t, f, s, "b"); st.State != SyncError || st.Error == nil {
		t.Fatalf("%+v", st)
	}
	// A member removed while it was being read.
	ms["c"].set(func(c *cfgMember) { c.delay = 200 * time.Millisecond })
	got := make(chan *syncOut, 1)
	go f.m.syncMember("c", false, func(o *syncOut) { got <- o })
	time.Sleep(50 * time.Millisecond)
	if err := f.m.Remove("tester", "c"); err != nil {
		t.Fatal(err)
	}
	if o := <-got; o.sync.State != SyncError {
		t.Fatalf("%+v", o.sync)
	}
}

// TestTellRaces: a member told first by another read is not told twice;
// one whose record changed meanwhile is told again, once.
func TestTellRaces(t *testing.T) {
	f, s, ms := newTplFixture(t)
	s.must("POST", "/templates", `{"name":"t","groups":["edge"],"config":{"log":{}}}`, 201)
	quiet(t, f.m)
	for race, want := range map[string]string{"same": "kept", "other": "told", "getfail": "failed"} {
		ms["a"].set(func(c *cfgMember) { c.rec, c.race = managed.Record{}, race })
		if st := syncOne(t, f, s, "a"); st.Managed != want {
			t.Errorf("%s: %+v", race, st)
		}
	}
	// The templates' bound.
	for i := len(f.m.Templates()); i < MaxTemplates; i++ {
		if _, err := f.m.AddTemplate("t", &TemplateInput{Name: "x" + strconv.Itoa(i)}); err != nil {
			t.Fatal(err)
		}
	}
	s.must("POST", "/templates", `{"name":"one-more"}`, 409)
	s.must("PUT", "/templates/t", `{"name":"x1"}`, 409)
}
