package cluster

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
)

// fastAlerts has the evaluator run every few milliseconds while a test
// runs.
func fastAlerts(t *testing.T) {
	old := alertTick
	alertTick = 20 * time.Millisecond
	t.Cleanup(func() { alertTick = old })
}

// TestAlertsAPI drives the rules, webhooks and silences through the
// module's endpoints: ETags, refusals and their statuses, a webhook's test,
// the alerts and their summary, and a body too large.
func TestAlertsAPI(t *testing.T) {
	f := newFixture(t, config.Cluster{})
	s := f.serve()
	sink := newHookSink(t)
	code, body, hdr := s.do("GET", "/api/m/cluster/alerts/config", "")
	var cfg alertConfigBody
	if code != 200 || json.Unmarshal([]byte(body), &cfg) != nil || len(cfg.Rules) != len(defaultRules()) || hdr.Get("ETag") != strconv.Quote(cfg.Version) {
		t.Fatalf("%d %s", code, body)
	}
	tag := hdr.Get("ETag")
	hook := js(map[string]any{"name": "ops", "url": sink.srv.URL + "/hook/ops?token=sekret-token", "secret": "shh"})
	got := s.must("POST", "/alerts/webhooks", hook, 201, "If-Match", tag)
	if strings.Contains(got, "sekret-token") || strings.Contains(got, "shh") || !strings.Contains(got, `"secret_set":true`) {
		t.Fatal(got)
	}
	s.must("POST", "/alerts/webhooks", hook, 412, "If-Match", tag)
	s.must("POST", "/alerts/webhooks", hook, 409)
	s.must("POST", "/alerts/webhooks", `{"name":"x","url":"http://example.com/"}`, 400)
	s.must("POST", "/alerts/webhooks", `{`, 400)
	s.must("PUT", "/alerts/webhooks/ops", `{"format":"slack"}`, 200)
	s.must("PUT", "/alerts/webhooks/none", `{}`, 404)
	s.must("PUT", "/alerts/webhooks/ops", `{}`, 412, "If-Match", tag)
	s.must("PATCH", "/alerts/webhooks/ops", `{}`, 405)
	s.must("GET", "/alerts/webhooks/ops/x", ``, 404)
	res := s.must("POST", "/alerts/webhooks/ops/test", ``, 200)
	if !strings.Contains(res, `"ok":true`) {
		t.Fatal(res)
	}
	if r := sink.wait(t, 1)[0]; !strings.Contains(string(r.Body), "[TEST]") {
		t.Fatalf("%s", r.Body)
	}
	s.must("POST", "/alerts/webhooks/none/test", ``, 404)
	// Rules.
	s.must("POST", "/alerts/rules", `{"name":"down","kind":"member_down","webhooks":["ops"]}`, 201)
	s.must("POST", "/alerts/rules", `{"name":"down","kind":"member_down"}`, 409)
	got = s.must("POST", "/alerts/rules", `{"name":"x","kind":"what"}`, 400)
	if !strings.Contains(got, `"field":"kind"`) {
		t.Fatal(got)
	}
	s.must("PUT", "/alerts/rules/down", `{"kind":"member_down","for_ms":120000,"webhooks":["ops"]}`, 200)
	s.must("PUT", "/alerts/rules/none", `{"kind":"member_down"}`, 404)
	s.must("PUT", "/alerts/rules/down", `{}`, 412, "If-Match", tag)
	s.must("DELETE", "/alerts/rules/down", ``, 412, "If-Match", tag)
	s.must("DELETE", "/alerts/webhooks/ops", ``, 409)
	s.must("DELETE", "/alerts/rules/down", ``, 200)
	s.must("DELETE", "/alerts/rules/down", ``, 404)
	s.must("GET", "/alerts/rules/down", ``, 405)
	s.must("PUT", "/alerts/rules/down/x", `{}`, 404)
	s.must("DELETE", "/alerts/webhooks/ops", ``, 412, "If-Match", tag)
	s.must("DELETE", "/alerts/webhooks/ops", ``, 200)
	s.must("DELETE", "/alerts/webhooks/ops", ``, 404)
	// Silences.
	got = s.must("POST", "/alerts/silences", `{"node":"a","duration_ms":60000,"comment":"upgrade"}`, 201)
	var sil Silence
	_ = json.Unmarshal([]byte(got), &sil)
	s.must("POST", "/alerts/silences", `{"duration_ms":60000}`, 400)
	s.must("DELETE", "/alerts/silences/"+sil.ID, ``, 412, "If-Match", tag)
	s.must("DELETE", "/alerts/silences/"+sil.ID, ``, 200)
	s.must("DELETE", "/alerts/silences/none", ``, 404)
	s.must("GET", "/alerts/silences/"+sil.ID, ``, 405)
	s.must("DELETE", "/alerts/silences/"+sil.ID+"/x", ``, 404)
	// The alerts and their summary.
	got = s.must("GET", "/alerts", ``, 200)
	if !strings.Contains(got, `"active":[]`) || !strings.Contains(got, `"counts":{"firing":0`) {
		t.Fatal(got)
	}
	if got = s.must("GET", "/alerts/summary", ``, 200); !strings.Contains(got, `"firing":0`) {
		t.Fatal(got)
	}
	big := `{"name":"` + strings.Repeat("x", MaxBodyBytes) + `"}`
	s.must("POST", "/alerts/rules", big, 413)
	s.must("POST", "/drift/fix", big, 413)
	// Drift, and a fix of nothing or of a member there is none of.
	if got = s.must("GET", "/drift", ``, 200); !strings.Contains(got, `"nodes":[]`) {
		t.Fatal(got)
	}
	s.must("POST", "/drift/fix", `{"nodes":[]}`, 400)
	s.must("POST", "/drift/fix", `{`, 400)
	s.must("POST", "/drift/fix", `{"nodes":["none"]}`, 404)
	// A section of a newer fit: read-only.
	f.m.al.mu.Lock()
	f.m.al.readOnly = "newer"
	f.m.al.mu.Unlock()
	s.must("POST", "/alerts/rules", `{"name":"y","kind":"drift"}`, 409)
	if strings.Contains(s.logs.String()+f.log.String(), "sekret-token") || strings.Contains(f.log.String(), "shh") {
		t.Fatal("a webhook's URL or secret was logged")
	}
}

// TestFixDriftLimits refuses a fix of more members than the manager keeps.
func TestFixDriftLimits(t *testing.T) {
	f := newFixture(t, config.Cluster{MaxNodes: 1})
	if _, err := f.m.FixDrift("u", []string{"a", "b"}); err == nil || !strings.Contains(err.Error(), "more than 1") {
		t.Fatal(err)
	}
}

// TestAlertsEndToEnd: a member failing its polls fires once it has failed
// for the rule's time, by the manager's clock, is sent, and resolves once
// it is up again; a webhook that does not answer delays neither the poller
// nor the evaluator, and times out.
func TestAlertsEndToEnd(t *testing.T) {
	fastAlerts(t)
	clock := &offsetClock{}
	f := &fixture{t: t, engine: testEngine(t), store: newMemStore(), log: &logBuf{},
		set: config.Cluster{PollInterval: config.Duration(50 * time.Millisecond), Timeout: config.Duration(5 * time.Second)}}
	m, err := New(Options{Engine: f.engine, Settings: &f.set, Store: f.store, Logf: f.log.logf, Now: clock.now, NodeID: "mgr-e2e"})
	if err != nil {
		t.Fatal(err)
	}
	f.m = m
	t.Cleanup(m.Close)
	fake := newFakeMember(t, false)
	f.add(tokenInput("a", fake.srv.URL))
	f.waitUp("a")
	slow, sink := newHookSink(t), newHookSink(t)
	slow.set(func(h *hookSink) { h.hold = make(chan struct{}) })
	if _, err := m.AddWebhook("ann", &WebhookInput{Name: "slow", URL: slow.srv.URL + "/slow", TimeoutMs: 1000}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AddWebhook("ann", &WebhookInput{Name: "ops", URL: sink.srv.URL + "/ops"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.UpdateAlertRule("ann", "member-down", &AlertRule{Kind: AlertMemberDown, ForMs: 60_000}); err != nil {
		t.Fatal(err)
	}
	// The fake's drift and backends down are the other rules'.
	for _, r := range defaultRules() {
		if r.Name != "member-down" {
			if err := m.RemoveAlertRule("ann", r.Name); err != nil {
				t.Fatal(err)
			}
		}
	}
	fake.set(func(f *fakeMember) { f.nodeStatus = 500 })
	f.waitState("a", "failing", func(s State) bool { return s.Status == StatusError })
	waitFor(t, "the alert pending", func() bool { return len(m.Alerts().Pending) == 1 })
	time.Sleep(100 * time.Millisecond)
	if v := m.Alerts(); len(v.Active) != 0 {
		t.Fatalf("fired before its time: %+v", v.Active)
	}
	clock.add(2 * time.Minute)
	waitFor(t, "the alert to fire", func() bool { return m.AlertSummary().Critical == 1 })
	sink.wait(t, 1)
	// The slow webhook holds its notification: the poller polls on, the
	// evaluator evaluates on.
	waitFor(t, "the slow webhook to be under way", func() bool { return len(slow.requests()) == 1 })
	polled := f.state("a").Polled
	waitFor(t, "more polls", func() bool { return f.state("a").Polled.Sub(polled) > 150*time.Millisecond })
	fake.set(func(f *fakeMember) { f.nodeStatus = 0 })
	f.waitUp("a")
	waitFor(t, "the alert to resolve", func() bool { return m.AlertSummary().Firing == 0 && len(m.Alerts().History) == 1 })
	if r := sink.wait(t, 2)[1]; !strings.Contains(string(r.Body), `"status":"resolved"`) {
		t.Fatalf("%s", r.Body)
	}
	var st WebhookStatus
	waitFor(t, "the slow webhook to time out", func() bool {
		for _, w := range m.AlertConfig().Webhooks {
			if w.Name == "slow" {
				st = w.Status
			}
		}
		return st.Failed >= 1
	})
	if st.LastTookMs < 900 || st.LastError == "" {
		t.Fatalf("%+v", st)
	}
	// What the evaluator changed is in the store.
	waitFor(t, "the alerts written", func() bool {
		var d alertsDoc
		_ = json.Unmarshal(f.store.Section(AlertsSection), &d)
		return len(d.History) == 1
	})
}

// driftMember is a fit as the drift reads see it, by token: GET /node with
// its config's ETag, GET /config, and its managed record.
type driftMember struct {
	srv   *httptest.Server
	mu    sync.Mutex
	cfg   string
	rev   int
	reads atomic.Int64
	puts  atomic.Int64
}

func newDriftMember(t *testing.T, cfg string) *driftMember {
	d := &driftMember{cfg: cfg, rev: 1}
	d.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+fakeToken {
			w.WriteHeader(401)
			return
		}
		d.mu.Lock()
		etag, cfg := `"d`+strconv.Itoa(d.rev)+`"`, d.cfg
		d.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /node":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "id-d", "hostname": "h", "fit_version": "v1", "config_etag": etag,
				"capabilities": []string{"node", "node.managed"}})
		case "GET /config":
			d.reads.Add(1)
			w.Header().Set("ETag", etag)
			_, _ = w.Write([]byte(cfg))
		case "GET /node/managed":
			_, _ = w.Write([]byte(`{"paths":[]}`))
		case "PUT /node/managed":
			d.puts.Add(1)
			_, _ = io.Copy(io.Discard, r.Body)
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(d.srv.Close)
	return d
}

func (d *driftMember) edit(cfg string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cfg, d.rev = cfg, d.rev+1
}

// TestDriftOnPoll: a member a template is attached to is read against it
// after its polls, once its config changed (its ETag), and not again while
// it stays the same; its drift fires and resolves the drift rule; Fix
// plans a sync rollout of it, which applies nothing.
func TestDriftOnPoll(t *testing.T) {
	fastAlerts(t)
	old := refreshDelay
	refreshDelay = 10 * time.Millisecond
	t.Cleanup(func() { refreshDelay = old })
	f := newFixture(t, config.Cluster{})
	s := f.serve()
	base := `{"routes":[{"name":"own","handler":{"module":"respond"}}]}`
	want := `{"routes":[{"name":"own","handler":{"module":"respond"}},{"name":"api","handler":{"module":"respond","params":{"status":204}}}]}`
	d := newDriftMember(t, base)
	other := newDriftMember(t, base)
	f.add(tokenInput("a", d.srv.URL))
	f.add(tokenInput("b", other.srv.URL))
	if _, err := f.m.AddGroup("u", &GroupInput{Name: "g", Members: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.UpdateAlertRule("u", "drift", &AlertRule{Kind: AlertDrift}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.AddTemplate("u", &TemplateInput{Name: "t", Groups: []string{"g"},
		Config: json.RawMessage(`{"routes":[{"name":"api","handler":{"module":"respond","params":{"status":204}}}]}`)}); err != nil {
		t.Fatal(err)
	}
	quiet(t, f.m)
	// What a page's read or the refresh saw is cleared: the drift reads
	// alone read it again.
	f.m.tpl.mu.Lock()
	delete(f.m.tpl.last, "a")
	f.m.tpl.mu.Unlock()
	syncOf := func() (MemberSync, bool) {
		for _, x := range f.m.SyncStates() {
			if x.Node == "a" {
				return x, true
			}
		}
		return MemberSync{}, false
	}
	waitFor(t, "a drift read", func() bool { x, ok := syncOf(); return ok && x.State == SyncOutOfSync })
	waitFor(t, "the drift alert", func() bool { return f.m.AlertSummary().Warning == 1 })
	v := f.m.Alerts()
	if len(v.Active) != 1 || v.Active[0].Node != "a" || !strings.Contains(v.Active[0].Summary, "1 parts out of sync") {
		t.Fatalf("%+v", v.Active)
	}
	reads := d.reads.Load()
	time.Sleep(300 * time.Millisecond) // several polls
	if n := d.reads.Load(); n != reads {
		t.Fatalf("read %d times more though its config did not change", n-reads)
	}
	if other.reads.Load() != 0 {
		t.Fatal("a member no template is attached to was read")
	}
	var dr struct {
		Nodes []DriftNode
	}
	_ = json.Unmarshal([]byte(s.must("GET", "/drift", ``, 200)), &dr)
	if len(dr.Nodes) != 2 || !dr.Nodes[0].Drifted || !dr.Nodes[0].Fixable || !dr.Nodes[0].Managed || dr.Nodes[1].Managed || dr.Nodes[1].Drifted {
		t.Fatalf("%+v", dr.Nodes)
	}
	// Fix: a plan, nothing applied.
	var ro Rollout
	_ = json.Unmarshal([]byte(s.must("POST", "/drift/fix", `{"nodes":["a"]}`, 202)), &ro)
	if ro.Kind != RolloutSync || len(ro.Members) != 1 || ro.Members[0].Node != "a" || ro.PlannedBy != "admin" || !strings.Contains(ro.Description, "drift of a") {
		t.Fatalf("%+v", ro)
	}
	// Brought in sync on the member: read again at the next poll, and
	// resolved; the reads told the member nothing.
	puts := d.puts.Load()
	d.edit(want)
	waitFor(t, "the read after the change", func() bool { x, _ := syncOf(); return x.State == SyncInSync })
	waitFor(t, "the drift alert to resolve", func() bool { return f.m.AlertSummary().Warning == 0 })
	if n := d.puts.Load(); n != puts {
		t.Fatalf("the drift reads told the member %d times", n-puts)
	}
}

// TestDriftReadsBounded: a drift read waits for no room: with none, the
// member is read after a later poll; members gone are forgotten.
func TestDriftReadsBounded(t *testing.T) {
	f := newFixture(t, config.Cluster{MaxConcurrency: 1})
	d := newDriftMember(t, `{}`)
	f.add(tokenInput("a", d.srv.URL))
	if _, err := f.m.AddGroup("u", &GroupInput{Name: "g", Members: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.AddTemplate("u", &TemplateInput{Name: "t", Groups: []string{"g"}, Config: json.RawMessage(`{"log":{"level":"info"}}`)}); err != nil {
		t.Fatal(err)
	}
	f.waitUp("a")
	quiet(t, f.m)
	al := &f.m.al
	al.evalMu.Lock() // the evaluator waits, and starts no read
	waitFor(t, "the reads under way to end", func() bool {
		al.mu.Lock()
		defer al.mu.Unlock()
		return len(al.driftBusy) == 0
	})
	al.driftSem <- struct{}{}
	f.m.tpl.mu.Lock()
	delete(f.m.tpl.last, "a")
	f.m.tpl.mu.Unlock()
	al.mu.Lock()
	al.driftPoll["gone"] = time.Now()
	al.driftPoll["a"] = time.Time{}
	al.mu.Unlock()
	reads := d.reads.Load()
	f.m.startDriftReads()
	<-al.driftSem
	al.evalMu.Unlock()
	al.mu.Lock()
	_, gone := al.driftPoll["gone"]
	busy := al.driftBusy["a"]
	al.mu.Unlock()
	if d.reads.Load() != reads || busy || gone {
		t.Fatalf("read without room (%v), or a member gone kept (%v)", busy, gone)
	}
	waitFor(t, "a read after a later poll", func() bool { return d.reads.Load() > reads })
}

// TestAlertsOddEnds: a failed rollout as the evaluator reads it; a
// notification for a webhook gone; an audit log that cannot be written; a
// webhook changed in nothing; nothing kept, written.
func TestAlertsOddEnds(t *testing.T) {
	clock := newClock()
	m, st, log := bareManager(t, clock.now)
	m.ro.list = []*Rollout{{ID: "r1", Kind: RolloutSync, State: RolloutFailed, Reason: "gate", Ended: clock.now(), Members: []RolloutMember{{Node: "a"}}},
		{ID: "r2", State: RolloutDone}}
	in := m.alertInput()
	if len(in.rollouts) != 1 || in.rollouts[0].id != "r1" || in.rollouts[0].nodes[0] != "a" || !strings.Contains(in.rollouts[0].what, "sync") {
		t.Fatalf("%+v", in.rollouts)
	}
	m.deliver(map[string]*batch{"gone": {firing: []Alert{{}}}})
	m.readOnly = "the document is read-only"
	if _, err := m.AddWebhook("u", &WebhookInput{Name: "w", URL: "https://h.example/"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log.String(), "the audit entry of alert.webhook.add was not written") {
		t.Fatal(log.String())
	}
	if _, err := m.UpdateWebhook("u", "w", &WebhookInput{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log.String(), "alert.webhook.update w: nothing") {
		t.Fatal(log.String())
	}
	m.al.mu.Lock()
	m.al.cfg = alertConfig{}
	m.al.dirty = true
	m.al.mu.Unlock()
	m.saveAlerts()
	if got := string(st.Section(AlertsSection)); !strings.Contains(got, `"rules":[]`) || !strings.Contains(got, `"silences":[]`) || !strings.Contains(got, `"webhooks":[]`) {
		t.Fatal(got)
	}
}
