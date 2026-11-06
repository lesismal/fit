package cluster

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
)

// roMember is a fit as a rollout sees it, through its Admin API's token:
// GET /node, GET and PUT /config with If-Match and ?dry_run=1, GET
// /versions, POST /rollback/{n}, GET /status with its requests, and GET
// /config/check. Its requests are counted 10 more at each read of /status,
// all of them 5xx while its config has "5xx-please"; its consistency check
// finds one more difference while its config has "drift-please"; a config
// with "bad-config" is refused.
type roMember struct {
	srv *httptest.Server

	mu   sync.Mutex
	hist []roVersion
	// total and fivexx are the requests counted.
	total, fivexx uint64
	drift         int
	// noMetrics answers no requests; caps replaces the capabilities;
	// putDelay holds a PUT before it applies; putStatus answers PUT
	// /config with it; rollbackStatus POST /rollback; nodeStatus GET
	// /node, after nodeOK more answers; statusStatus GET /status, and
	// checkStatus GET /config/check; noNewETag answers a dry run
	// without new_etag; badPut answers an apply that is not JSON.
	noMetrics      bool
	caps           []string
	putDelay       time.Duration
	putStatus      int
	rollbackStatus int
	nodeStatus     int
	nodeOK         int
	statusStatus   int
	checkStatus    int
	noNewETag      bool
	badPut         bool
	// dropPut answers an apply with what is not JSON, without applying
	// it; nodeFailOnce has the GET /node after a PUT fail once;
	// configStatus answers GET /config with it; dryStatus a dry run.
	dropPut      bool
	nodeFailOnce bool
	nodeFailing  bool
	configStatus int
	dryStatus    int
	// dryClose and putClose cut the connection of a dry run and of an
	// apply (not applied); dryUnchanged says a dry run changes nothing;
	// statusBad answers GET /status with what is not JSON; statusDelay
	// and nodeDelay hold GET /status and GET /node.
	dryClose      bool
	putClose      bool
	dryUnchanged  bool
	statusBad     bool
	statusDelay   time.Duration
	nodeDelay     time.Duration
	resetCounts   bool
	puts, dryRuns int
	rollbacks     int
	applied       chan struct{}
}

// roVersion is a config a roMember ran.
type roVersion struct {
	n    uint64
	etag string
	cfg  string
}

func etagOf(data []byte) string {
	sum := sha256.Sum256(data)
	return `"` + hex.EncodeToString(sum[:8]) + `"`
}

func newRoMember(t *testing.T, cfg string) *roMember {
	t.Helper()
	f := &roMember{hist: []roVersion{{n: 1, etag: etagOf([]byte(cfg)), cfg: cfg}}, applied: make(chan struct{}, 16)}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *roMember) set(fn func(f *roMember)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

// cur returns the version running; f.mu is held.
func (f *roMember) cur() roVersion { return f.hist[len(f.hist)-1] }

func (f *roMember) config() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cur().cfg
}

func (f *roMember) version() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cur().n
}

// edit applies cfg as a person would on the member itself.
func (f *roMember) edit(cfg string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hist = append(f.hist, roVersion{n: f.cur().n + 1, etag: etagOf([]byte(cfg)), cfg: cfg})
}

func (f *roMember) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+fakeToken {
		w.WriteHeader(401)
		return
	}
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	var hold time.Duration
	isPut := r.Method == "PUT" && r.URL.Path == "/config"
	dry := r.URL.Query().Get("dry_run") == "1"
	switch {
	case isPut && !dry:
		hold = f.putDelay
	case r.URL.Path == "/status":
		hold = f.statusDelay
	case r.URL.Path == "/node":
		hold = f.nodeDelay
	}
	if hold > 0 {
		f.mu.Unlock()
		time.Sleep(hold)
		f.mu.Lock()
	}
	if isPut && (dry && f.dryClose || !dry && f.putClose) {
		if !dry && f.nodeFailOnce {
			f.nodeFailing = true
		}
		f.mu.Unlock()
		conn, _, _ := w.(http.Hijacker).Hijack()
		_ = conn.Close()
		return
	}
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	cur := f.cur()
	answer := func(status int, v any) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	switch {
	case r.Method == "GET" && r.URL.Path == "/node":
		if f.nodeFailing {
			f.nodeFailing = false
			answer(500, map[string]string{"error": "no node now"})
			return
		}
		if f.nodeStatus != 0 && f.nodeOK <= 0 {
			answer(f.nodeStatus, map[string]string{"error": "no node"})
			return
		}
		f.nodeOK--
		caps := f.caps
		if caps == nil {
			caps = []string{"config.check", "config.dry_run", "config.if_match", "config.reload", "config.versions", "node", "status", "status.requests"}
		}
		answer(200, map[string]any{"id": "id-" + f.srv.Listener.Addr().String(), "hostname": "h", "fit_version": "v1", "schema_digest": "sha256:x",
			"capabilities": caps, "config_version": cur.n, "config_etag": cur.etag})
	case r.Method == "GET" && r.URL.Path == "/config":
		if f.configStatus != 0 {
			answer(f.configStatus, map[string]string{"error": "no config"})
			return
		}
		w.Header().Set("ETag", cur.etag)
		_, _ = w.Write([]byte(cur.cfg))
	case isPut:
		switch {
		case r.Header.Get("If-Match") != cur.etag:
			answer(412, map[string]string{"error": "the config has changed since the ETag given in If-Match"})
			return
		case !json.Valid(body):
			answer(400, map[string]string{"error": "not JSON"})
			return
		case strings.Contains(string(body), "bad-config"):
			answer(400, map[string]any{"error": "the config was not applied; the running config stays",
				"errors": []map[string]string{{"path": "/routes/1/handler", "message": "no such module"}, {"message": "two"}}})
			return
		case !dry && f.putStatus != 0:
			answer(f.putStatus, map[string]string{"error": "refused"})
			return
		case dry && f.dryStatus != 0:
			answer(f.dryStatus, map[string]string{"error": "dry run refused"})
			return
		}
		if !dry && f.nodeFailOnce {
			f.nodeFailing = true
		}
		if !dry && f.dropPut {
			_, _ = w.Write([]byte(`{"version":`))
			return
		}
		etag := etagOf(body)
		if dry {
			f.dryRuns++
			a := map[string]any{"dry_run": true, "version": cur.n, "etag": cur.etag, "new_etag": etag, "unchanged": etag == cur.etag || f.dryUnchanged, "warnings": []string{"w"}}
			if f.noNewETag {
				delete(a, "new_etag")
			}
			answer(200, a)
			return
		}
		f.puts++
		f.hist = append(f.hist, roVersion{n: cur.n + 1, etag: etag, cfg: string(body)})
		select {
		case f.applied <- struct{}{}:
		default:
		}
		if f.badPut {
			_, _ = w.Write([]byte(`{"version":`))
			return
		}
		answer(200, map[string]any{"version": cur.n + 1, "etag": etag, "warnings": []string{"applied"}})
	case r.Method == "GET" && r.URL.Path == "/versions":
		var vs []any
		for _, v := range f.hist {
			vs = append(vs, map[string]any{"version": v.n, "etag": v.etag})
		}
		answer(200, map[string]any{"current": cur.n, "versions": vs})
	case r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/rollback/"):
		f.rollbacks++
		n, _ := strconv.ParseUint(strings.TrimPrefix(r.URL.Path, "/rollback/"), 10, 64)
		switch {
		case f.rollbackStatus != 0:
			answer(f.rollbackStatus, map[string]string{"error": "no rollback"})
			return
		case r.Header.Get("If-Match") != "" && r.Header.Get("If-Match") != cur.etag:
			answer(412, map[string]string{"error": "changed"})
			return
		}
		for _, v := range f.hist {
			if v.n == n {
				f.hist = append(f.hist, roVersion{n: cur.n + 1, etag: v.etag, cfg: v.cfg})
				answer(200, map[string]any{"version": cur.n + 1, "etag": v.etag})
				return
			}
		}
		answer(404, map[string]string{"error": "version is not kept"})
	case r.Method == "GET" && r.URL.Path == "/status":
		if f.statusStatus != 0 {
			answer(f.statusStatus, map[string]string{"error": "no status"})
			return
		}
		if f.statusBad {
			_, _ = w.Write([]byte(`{"requests":`))
			return
		}
		if f.resetCounts {
			f.total, f.fivexx = 0, 0
		}
		f.total += 10
		if strings.Contains(cur.cfg, "5xx-please") {
			f.fivexx += 10
		}
		st := map[string]any{"version": cur.n, "reloads": map[string]any{"succeeded": len(f.hist) - 1}}
		if !f.noMetrics {
			st["requests"] = map[string]any{"total": f.total, "classes": map[string]uint64{"2xx": f.total - f.fivexx, "5xx": f.fivexx}}
		}
		answer(200, st)
	case r.Method == "GET" && r.URL.Path == "/config/check":
		if f.checkStatus != 0 {
			answer(f.checkStatus, map[string]string{"error": "no check"})
			return
		}
		d := f.drift
		if strings.Contains(cur.cfg, "drift-please") {
			d++
		}
		answer(200, map[string]any{"version": cur.n, "checked_at": "2026-11-06T00:00:00Z", "summary": map[string]int{"drift": d, "info": 0}})
	default:
		w.WriteHeader(404)
	}
}

// roBase is a member's own config: a listener, its own route, and its
// upstream backend.
func roBase(extra string) string {
	return `{"listeners":[{"name":"web","address":"127.0.0.1:0"}],"upstreams":[{"name":"backend","addresses":["127.0.0.1:1"` + extra + `]}],` +
		`"routes":[{"name":"own","handler":{"module":"respond","params":{"body":"ok"}}}]}`
}

// roFixture is a manager with the members m1 to mN, by token, labelled
// role=edge, a group edge of them, and a template api of a route attached
// to it.
type roFixture struct {
	*fixture
	s  *served
	ms map[string]*roMember
}

func newRoFixture(t *testing.T, n int) *roFixture {
	t.Helper()
	return newRoFixtureWith(t, n, config.Cluster{MaxNodes: 16, MaxConcurrency: 4})
}

// newRoFixtureWith is newRoFixture with the manager's settings set.
func newRoFixtureWith(t *testing.T, n int, set config.Cluster, prep ...func(name string, f *roMember)) *roFixture {
	t.Helper()
	f := newFixture(t, set)
	k := &roFixture{fixture: f, s: f.serve(), ms: map[string]*roMember{}}
	for i := 1; i <= n; i++ {
		name := "m" + strconv.Itoa(i)
		k.ms[name] = newRoMember(t, roBase(""))
		for _, p := range prep {
			p(name, k.ms[name])
		}
		in := tokenInput(name, k.ms[name].srv.URL)
		in.Labels = map[string]string{"role": "edge"}
		f.add(in)
	}
	if _, err := f.m.AddGroup("tester", &GroupInput{Name: "edge", Selector: "role=edge"}); err != nil {
		t.Fatal(err)
	}
	k.template(`"api"`)
	return k
}

// template sets the template api's route's body.
func (k *roFixture) template(body string) {
	k.t.Helper()
	in := &TemplateInput{Name: "api", Groups: []string{"edge"},
		Config: json.RawMessage(`{"routes":[{"name":"api","match":{"path":["/api"]},"handler":{"module":"respond","params":{"body":` + body + `}}}]}`)}
	var err error
	if _, ok := k.m.TemplateByName("api"); ok {
		_, err = k.m.UpdateTemplate("tester", "api", in)
	} else {
		_, err = k.m.AddTemplate("tester", in)
	}
	if err != nil {
		k.t.Fatal(err)
	}
}

// plan plans req and waits for it to be planned.
func (k *roFixture) plan(req RolloutRequest) Rollout {
	k.t.Helper()
	r, err := k.m.PlanRollout("ann", &req)
	if err != nil {
		k.t.Fatal(err)
	}
	return k.waitRollout(r.ID, "planned", func(r Rollout) bool { return r.State != RolloutPlanning })
}

// waitRollout waits for the rollout id to satisfy cond.
func (k *roFixture) waitRollout(id, what string, cond func(Rollout) bool) Rollout {
	k.t.Helper()
	var r Rollout
	waitFor(k.t, "the rollout "+what, func() bool {
		var ok bool
		r, ok = k.m.RolloutByID(id)
		return ok && cond(r)
	})
	return r
}

func (k *roFixture) start(id string, s Strategy, exclude ...string) Rollout {
	k.t.Helper()
	r, err := k.m.StartRollout("bob", id, &StartRequest{Strategy: s, Exclude: exclude})
	if err != nil {
		k.t.Fatal(err)
	}
	return r
}

func ended(r Rollout) bool { return r.finished() }

func memberOf(t *testing.T, r Rollout, name string) RolloutMember {
	t.Helper()
	for _, x := range r.Members {
		if x.Node == name {
			return x
		}
	}
	t.Fatalf("no member %s in %+v", name, r)
	return RolloutMember{}
}

func statuses(r Rollout) string {
	var out []string
	for _, x := range r.Members {
		out = append(out, x.Node+"="+x.Status)
	}
	return strings.Join(out, " ")
}

func fast(s Strategy) Strategy {
	if s.Gate.WaitMs == 0 {
		s.Gate.WaitMs = 1
	}
	return s
}

// TestRolloutRolling plans a template's rollout on five members, each
// one's diff and dry run taken, and applies it in batches of two, each
// gated; the members get the template's route, keep their own, and are
// let go; the store keeps the rollout without the configs, the audit log
// its plan, start and end.
func TestRolloutRolling(t *testing.T) {
	k := newRoFixture(t, 5)
	r := k.plan(RolloutRequest{Kind: RolloutTemplate, Template: "api", Description: "the api route"})
	if r.State != RolloutPlanned || r.Counts[MemberReady] != 5 || r.PlannedBy != "ann" {
		t.Fatalf("%+v", r)
	}
	x := memberOf(t, r, "m1")
	if len(x.Changes) != 1 || x.Changes[0].Path != "routes[api]" || x.Changes[0].State != "added" || x.Changes[0].Sync != SyncOutOfSync ||
		x.Changes[0].Template != "api" || x.Version != 1 || x.NewETag == "" || x.Config != nil || len(x.Warnings) != 1 {
		t.Fatalf("%+v", x)
	}
	r = k.start(r.ID, fast(Strategy{Mode: StrategyRolling, BatchSize: 2}))
	if len(r.Batches) != 3 || r.State != RolloutRunning || r.Strategy.OnFailure != OnFailureBatch {
		t.Fatalf("%+v", r)
	}
	if _, err := k.m.StartOp("op", &OpRequest{Op: OpReload, Members: []string{"m5"}}); err == nil {
		t.Error("an operation ran on a member of a running rollout")
	}
	r = k.waitRollout(r.ID, "done", ended)
	if r.State != RolloutDone || r.Counts[MemberDone] != 5 {
		t.Fatalf("%s %s %s", r.State, r.Reason, statuses(r))
	}
	for i, b := range r.Batches {
		if b.State != BatchPassed || b.Ended.IsZero() || len(b.Nodes) != []int{2, 2, 1}[i] {
			t.Errorf("batch %d: %+v", i, b)
		}
	}
	for name, m := range k.ms {
		cfg := m.config()
		if !strings.Contains(cfg, `"name": "api"`) || !strings.Contains(cfg, `"name": "own"`) || m.version() != 2 {
			t.Errorf("%s: %s", name, cfg)
		}
		x := memberOf(t, r, name)
		if x.Applied != 2 || x.Gate == nil || !x.Gate.Passed || x.Gate.DriftAfter == nil || x.Changes[0].Have != nil || len(x.Changed) != 1 {
			t.Errorf("%s: %+v %+v", name, x, x.Gate)
		}
	}
	if _, err := k.m.StartOp("op", &OpRequest{Op: OpCheck, Members: []string{"m5"}}); err != nil {
		t.Errorf("the members are still held: %v", err)
	}
	var doc document
	if err := json.Unmarshal(k.store.Section(Section), &doc); err != nil || len(doc.Rollouts) != 1 || doc.Rollouts[0].State != RolloutDone {
		t.Fatalf("the store: %v %+v", err, doc.Rollouts)
	}
	for _, x := range doc.Rollouts[0].Members {
		if x.Config != nil || x.Changes[0].Want != nil {
			t.Errorf("the store keeps %s's config", x.Node)
		}
	}
	var acts []string
	for _, e := range k.m.Audit() {
		if e.Rollout == r.ID {
			acts = append(acts, e.Action)
			if e.Action == ActionRolloutEnd && (len(e.Outcomes) != 5 || !strings.Contains(e.Outcomes[0].Detail, "applied as version 2")) {
				t.Errorf("%+v", e)
			}
			// I148: the end's outcome and each member's versions; the
			// start's diff of what it applies, its digest and members.
			if e.Action == ActionRolloutEnd && (e.Outcome != RolloutDone || e.Outcomes[0].After != 2 || e.Outcomes[0].Before == 0 || e.Outcomes[0].Digest == "") {
				t.Errorf("%+v", e)
			}
			if e.Action == ActionRolloutStart && (len(e.Nodes) != 5 || e.Diff == "" || !strings.HasPrefix(e.Digest, "sha256:") || len(e.Outcomes) != 5) {
				t.Errorf("%+v", e)
			}
		}
	}
	if strings.Join(acts, ",") != "rollout.plan,rollout.start,rollout.end" {
		t.Errorf("audit: %v", acts)
	}
	// The members' sync states were read after their batches: in sync.
	waitFor(t, "the sync states", func() bool {
		for _, s := range k.m.SyncStates() {
			if s.State != SyncInSync {
				return false
			}
		}
		return len(k.m.SyncStates()) == 5
	})
	// Planned again: nothing to change.
	r = k.plan(RolloutRequest{Kind: RolloutSync})
	if r.Counts[MemberUnchanged] != 5 {
		t.Fatalf("%s", statuses(r))
	}
	if _, err := k.m.StartRollout("bob", r.ID, &StartRequest{Strategy: Strategy{Mode: StrategyAll}}); err == nil {
		t.Error("a rollout with nothing to apply started")
	}
}

// TestRolloutGateFails: a member given a config that loads but fails its
// gate (its share of 5xx) stops the rollout, and its batch is rolled back,
// each member to the version it ran; the next batch is never applied. With
// on_failure all, every member applied is.
func TestRolloutGateFails(t *testing.T) {
	for _, all := range []bool{false, true} {
		t.Run(fmt.Sprint(all), func(t *testing.T) {
			k := newRoFixture(t, 5)
			k.ms["m3"].edit(roBase(`,"5xx-please"`))
			r := k.plan(RolloutRequest{Kind: RolloutTemplate, Template: "api"})
			s := Strategy{Mode: StrategyRolling, BatchSize: 2, Gate: Gate{WaitMs: 1, Max5xxRatio: 0.5, WindowMs: 30, MinRequests: 5}}
			if all {
				s.OnFailure = OnFailureAll
			}
			r = k.start(r.ID, s)
			r = k.waitRollout(r.ID, "failed", ended)
			if r.State != RolloutFailed || !strings.Contains(r.Reason, "the gate of batch 2 failed: m3") || !strings.Contains(r.Reason, "rolled back") {
				t.Fatalf("%s: %s %s", r.State, r.Reason, statuses(r))
			}
			m3 := memberOf(t, r, "m3")
			if m3.Status != MemberRolledBack || m3.Gate == nil || m3.Gate.Passed || m3.Gate.Ratio == nil || *m3.Gate.Ratio != 1 || m3.RolledBack != 4 ||
				m3.Error == nil || !strings.Contains(m3.Error.Message, "5xx") {
				t.Fatalf("%+v %+v", m3, m3.Gate)
			}
			if !strings.Contains(k.ms["m3"].config(), "5xx-please") || strings.Contains(k.ms["m3"].config(), `"api"`) {
				t.Errorf("m3 runs %s", k.ms["m3"].config())
			}
			if x := memberOf(t, r, "m4"); x.Status != MemberRolledBack || strings.Contains(k.ms["m4"].config(), `"api"`) {
				t.Errorf("m4: %+v", x)
			}
			if x := memberOf(t, r, "m5"); x.Status != MemberPending || k.ms["m5"].version() != 1 {
				t.Errorf("m5: %+v", x)
			}
			want := MemberDone
			if all {
				want = MemberRolledBack
			}
			if x := memberOf(t, r, "m1"); x.Status != want {
				t.Errorf("m1: %+v", x)
			}
			if r.Batches[1].State != BatchRolledBack || r.Batches[2].State != BatchPending {
				t.Errorf("%+v", r.Batches)
			}
		})
	}
}

// TestRolloutCanary: a canary waits for its go; the rest then go in their
// batches. A member edited on itself after the plan is skipped, not
// overwritten.
func TestRolloutCanary(t *testing.T) {
	k := newRoFixture(t, 4)
	r := k.plan(RolloutRequest{Kind: RolloutTemplate, Template: "api"})
	k.ms["m3"].edit(roBase(`,"edited-here"`))
	r = k.start(r.ID, fast(Strategy{Mode: StrategyCanary, Canary: 1, BatchPercent: 50}))
	if len(r.Batches) != 3 || !r.Batches[0].Canary {
		t.Fatalf("%+v", r.Batches)
	}
	r = k.waitRollout(r.ID, "waiting", func(r Rollout) bool { return r.State == RolloutWaiting })
	if memberOf(t, r, "m1").Status != MemberDone || memberOf(t, r, "m2").Status != MemberPending || r.Batch != 1 {
		t.Fatalf("%s", statuses(r))
	}
	if _, err := k.m.SteerRollout("bob", r.ID, SteerResume); err == nil {
		t.Error("a waiting canary resumed")
	}
	if _, err := k.m.SteerRollout("bob", r.ID, SteerGo); err != nil {
		t.Fatal(err)
	}
	r = k.waitRollout(r.ID, "done", ended)
	m3 := memberOf(t, r, "m3")
	if r.State != RolloutDone || m3.Status != MemberSkipped || m3.Error == nil || !strings.Contains(m3.Error.Message, "changed since the plan") {
		t.Fatalf("%s %s %+v", r.State, statuses(r), m3.Error)
	}
	if !strings.Contains(k.ms["m3"].config(), "edited-here") || strings.Contains(k.ms["m3"].config(), `"api"`) {
		t.Errorf("m3 was overwritten: %s", k.ms["m3"].config())
	}
	if _, err := k.m.SteerRollout("bob", r.ID, SteerGo); err == nil {
		t.Error("a done rollout went on")
	}
}

func TestStrategies(t *testing.T) {
	names := []string{"a", "b", "c", "d", "e"}
	for _, c := range []struct {
		s    Strategy
		want string
	}{
		{Strategy{Mode: StrategyAll}, "[a b c d e]"},
		{Strategy{Mode: StrategyRolling, BatchSize: 2}, "[a b] [c d] [e]"},
		{Strategy{Mode: StrategyRolling, BatchPercent: 40}, "[a b] [c d] [e]"},
		{Strategy{Mode: StrategyRolling, BatchPercent: 1}, "[a] [b] [c] [d] [e]"},
		{Strategy{Mode: StrategyCanary, Canary: 1}, "[a]* [b c d e]"},
		{Strategy{Mode: StrategyCanary, Canary: 2, BatchSize: 2}, "[a b]* [c d] [e]"},
	} {
		if err := c.s.check(5); err != nil {
			t.Fatalf("%+v: %v", c.s, err)
		}
		var got []string
		for _, b := range c.s.batches(names) {
			s := fmt.Sprint(b.Nodes)
			if b.Canary {
				s += "*"
			}
			got = append(got, s)
		}
		if strings.Join(got, " ") != c.want {
			t.Errorf("%+v: %v", c.s, got)
		}
		if c.s.describe() == "" {
			t.Error("no description")
		}
	}
	s := Strategy{Mode: StrategyCanary, Canary: 1, PauseMs: 10, BatchSize: 1, Gate: Gate{Max5xxRatio: 0.1}}
	if err := s.check(3); err != nil || s.Gate.WindowMs != DefaultGateWindowMs || s.Gate.MinRequests != DefaultMinRequests {
		t.Fatalf("%v %+v", err, s)
	}
	if d := s.describe(); !strings.Contains(d, "a canary of 1") || !strings.Contains(d, "5xx at most 0.1") || !strings.Contains(d, "10ms apart") {
		t.Error(d)
	}
	for field, s := range map[string]Strategy{
		"strategy.mode":                {Mode: "x"},
		"strategy.mode ":               {Mode: StrategyAll, BatchSize: 1},
		"strategy.batch_size":          {Mode: StrategyRolling, BatchSize: 9},
		"strategy.batch_percent":       {Mode: StrategyRolling, BatchPercent: 101},
		"strategy.batch_size ":         {Mode: StrategyRolling, BatchSize: 1, BatchPercent: 10},
		"strategy.batch_size  ":        {Mode: StrategyRolling},
		"strategy.canary":              {Mode: StrategyRolling, BatchSize: 1, Canary: 1},
		"strategy.canary ":             {Mode: StrategyCanary, Canary: 5},
		"strategy.pause_ms":            {Mode: StrategyRolling, BatchSize: 1, PauseMs: -1},
		"strategy.on_failure":          {Mode: StrategyAll, OnFailure: "x"},
		"strategy.gate.wait_ms":        {Mode: StrategyAll, Gate: Gate{WaitMs: -1}},
		"strategy.gate.max_5xx_ratio":  {Mode: StrategyAll, Gate: Gate{Max5xxRatio: 2}},
		"strategy.gate.window_ms":      {Mode: StrategyAll, Gate: Gate{Max5xxRatio: .5, WindowMs: -1}},
		"strategy.gate.min_requests":   {Mode: StrategyAll, Gate: Gate{Max5xxRatio: .5, MinRequests: -1}},
		"strategy.gate.max_5xx_ratio ": {Mode: StrategyAll, Gate: Gate{WindowMs: 5}},
	} {
		err := s.check(5)
		if fe, ok := err.(*FieldError); !ok || fe.Field != strings.TrimSpace(field) {
			t.Errorf("%s: %v", field, err)
		}
	}
	one := Strategy{Mode: StrategyCanary, Canary: 1}
	if err := one.check(1); err == nil || !strings.Contains(err.Error(), "two members") {
		t.Error(err)
	}
}

// waitMembers waits for the member's status in the rollout id.
func (k *roFixture) waitMember(id, name, status string) Rollout {
	k.t.Helper()
	return k.waitRollout(id, name+" "+status, func(r Rollout) bool {
		x := r.member(name)
		return x != nil && x.Status == status
	})
}
