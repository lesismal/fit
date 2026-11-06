package cluster

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
)

// opFixture is a manager serving its module, and a way to run operations
// through it.
type opFixture struct {
	*fixture
	s *served
}

func newOpFixture(t *testing.T, set config.Cluster) *opFixture {
	f := newFixture(t, set)
	return &opFixture{fixture: f, s: f.serve()}
}

// start posts the operation body and returns it started.
func (o *opFixture) startOp(body string, want int) Op {
	o.t.Helper()
	code, got, _ := o.s.do("POST", "/api/m/cluster/ops", body)
	if code != want {
		o.t.Fatalf("POST /ops %s: %d %s", body, code, got)
	}
	var op Op
	_ = json.Unmarshal([]byte(got), &op)
	return op
}

// wait waits for the operation id to finish, through the API, and returns
// it.
func (o *opFixture) wait(id string) Op {
	o.t.Helper()
	var op Op
	waitFor(o.t, "the operation "+id, func() bool {
		code, got, _ := o.s.do("GET", "/api/m/cluster/ops/"+id, "")
		if code != 200 {
			o.t.Fatalf("GET /ops/%s: %d %s", id, code, got)
		}
		op = Op{}
		_ = json.Unmarshal([]byte(got), &op)
		return op.Finished
	})
	return op
}

// run starts the operation body and waits for it.
func (o *opFixture) run(body string) Op {
	o.t.Helper()
	return o.wait(o.startOp(body, 202).ID)
}

func outcomeOf(t *testing.T, op Op, name string) Outcome {
	t.Helper()
	for _, x := range op.Outcomes {
		if x.Node == name {
			return x
		}
	}
	t.Fatalf("no outcome of %s in %+v", name, op)
	return Outcome{}
}

// auditOf returns the audit entry of the operation id.
func (o *opFixture) auditOf(id string) AuditEntry {
	o.t.Helper()
	var e AuditEntry
	waitFor(o.t, "the audit of "+id, func() bool {
		for _, x := range o.m.Audit() {
			if x.Op == id {
				e = x
				return true
			}
		}
		return false
	})
	return e
}

// TestOpOnFiveOneDown reloads a group of five members, one of which is
// down: four are done, each with the version it applied, and the fifth
// failed with why; the operation and each outcome are in the audit log.
func TestOpOnFiveOneDown(t *testing.T) {
	o := newOpFixture(t, config.Cluster{MaxConcurrency: 2})
	var fakes []*fakeMember
	for i, name := range []string{"m1", "m2", "m3", "m4"} {
		fm := newFakeMember(t, false)
		fakes = append(fakes, fm)
		in := consoleInput(name, fm.srv.URL)
		if i%2 == 1 {
			in = tokenInput(name, fm.srv.URL)
		}
		in.Labels = map[string]string{"tier": "edge"}
		o.add(in)
	}
	down := tokenInput("m5", downURL(t))
	down.Labels = map[string]string{"tier": "edge"}
	o.add(down)
	if _, err := o.m.AddGroup("tester", &GroupInput{Name: "edge", Selector: "tier=edge"}); err != nil {
		t.Fatal(err)
	}

	started := o.startOp(`{"op":"reload","group":"edge"}`, 202)
	if started.ID == "" || len(started.Outcomes) != 5 || started.Counts.Pending+started.Counts.Running+started.Counts.Done+started.Counts.Failed != 5 {
		t.Fatalf("%+v", started)
	}
	op := o.wait(started.ID)
	if op.Counts.Done != 4 || op.Counts.Failed != 1 || op.Op != OpReload || op.Group != "edge" || op.User != "admin" || op.Ended.IsZero() {
		t.Fatalf("%+v", op)
	}
	for _, name := range []string{"m1", "m2", "m3", "m4"} {
		x := outcomeOf(t, op, name)
		if x.Status != OutcomeDone || x.Result == nil || x.Result.Version != 8 || !strings.Contains(x.Detail, "version 8") {
			t.Errorf("%s: %+v", name, x)
		}
	}
	failed := outcomeOf(t, op, "m5")
	if failed.Status != OutcomeFailed || failed.Error == nil || failed.Error.Kind != KindUnreachable || failed.Error.Message == "" {
		t.Errorf("m5: %+v", failed)
	}
	for _, fm := range fakes {
		fm.mu.Lock()
		if cur := fm.current(); cur.n != 8 {
			t.Errorf("a member's version %d", cur.n)
		}
		fm.mu.Unlock()
	}

	e := o.auditOf(op.ID)
	if e.Action != "op.reload" || e.Group != "edge" || e.User != "admin" || e.Detail != "4 done, 1 failed" || len(e.Outcomes) != 5 {
		t.Fatalf("%+v", e)
	}
	if a := e.Outcomes[4]; a.Node != "m5" || a.Status != OutcomeFailed || !strings.HasPrefix(a.Error, "unreachable: ") {
		t.Errorf("%+v", a)
	}
	// The list holds it, the latest first.
	code, got, _ := o.s.do("GET", "/api/m/cluster/ops", "")
	if code != 200 || !strings.Contains(got, `"id":"`+op.ID+`"`) {
		t.Errorf("%d %s", code, got)
	}
	if code, _, _ := o.s.do("GET", "/api/m/cluster/ops/nothing", ""); code != 404 {
		t.Errorf("an unknown operation: %d", code)
	}
	if strings.Contains(got, fakePassword) || strings.Contains(got, fakeToken) {
		t.Error("a credential answered")
	}
}

// TestOpPurge purges the cache of every member, by key and prefix, by tag
// and all: each member's zone hears it; a member without the zone, or
// without prefixes, fails with why.
func TestOpPurge(t *testing.T) {
	o := newOpFixture(t, config.Cluster{})
	var fakes []*fakeMember
	for _, name := range []string{"p1", "p2", "p3"} {
		fm := newFakeMember(t, false)
		fm.set(func(f *fakeMember) { f.zones = []string{"site"} })
		fakes = append(fakes, fm)
		o.add(tokenInput(name, fm.srv.URL))
	}
	op := o.run(`{"op":"purge","members":["p1","p2","p3"],"purge":{"zone":"site","keys":["https://a.example/x"],"prefixes":["https://a.example/img/"]}}`)
	if op.Counts.Done != 3 || op.Purge == nil || op.Purge.Zone != "site" {
		t.Fatalf("%+v", op)
	}
	for i, fm := range fakes {
		fm.mu.Lock()
		purges := slices.Clone(fm.purges)
		fm.mu.Unlock()
		if len(purges) != 1 || !strings.Contains(purges[0], `"prefixes":["https://a.example/img/"]`) || !strings.Contains(purges[0], `"urls":["https://a.example/x"]`) || !strings.Contains(purges[0], `"zone":"site"`) {
			t.Errorf("member %d heard %v", i, purges)
		}
		if x := op.Outcomes[i]; x.Result == nil || x.Result.Purged == nil || *x.Result.Purged != 3 || x.Detail != "purged 3 keys" {
			t.Errorf("%+v", x)
		}
	}
	if e := o.auditOf(op.ID); !strings.Contains(e.Detail, "zone site: keys https://a.example/x; prefixes https://a.example/img/") {
		t.Errorf("%+v", e)
	}

	// p2 has no zone other, and p3 no prefixes.
	fakes[2].set(func(f *fakeMember) { f.caps = []string{"cache", "node"} })
	fakes[2].set(func(f *fakeMember) { f.zones = []string{"site", "other"} })
	fakes[0].set(func(f *fakeMember) { f.zones = []string{"site", "other"} })
	waitFor(t, "p3's capabilities read", func() bool {
		st := o.state("p3")
		return st.Node != nil && !slices.Contains(st.Node.Capabilities, capCachePrefix)
	})
	op = o.run(`{"op":"purge","members":["p1","p2","p3"],"purge":{"zone":"other","prefixes":["https://a.example/"]}}`)
	if op.Counts.Done != 1 || op.Counts.Failed != 2 {
		t.Fatalf("%+v", op)
	}
	if x := outcomeOf(t, op, "p2"); x.Error == nil || x.Error.Status != 404 || !strings.Contains(x.Error.Message, `no cache uses the zone "other"`) {
		t.Errorf("%+v", x)
	}
	if x := outcomeOf(t, op, "p3"); x.Error == nil || x.Error.Kind != KindUnsupported || !strings.Contains(x.Error.Message, "cache.prefix") {
		t.Errorf("%+v", x)
	}
	// Tags and all, which p3 takes.
	op = o.run(`{"op":"purge","members":["p3"],"purge":{"all":true}}`)
	if op.Counts.Done != 1 {
		t.Errorf("%+v", op)
	}
	fakes[2].mu.Lock()
	last := fakes[2].purges[len(fakes[2].purges)-1]
	fakes[2].mu.Unlock()
	if !strings.Contains(last, `"all":true`) {
		t.Errorf("%s", last)
	}
	if op := o.run(`{"op":"purge","members":["p1"],"purge":{"tags":["t"]}}`); op.Counts.Done != 1 {
		t.Errorf("%+v", op)
	}
}

// TestOpRollbackCheckTest rolls members back to their previous config,
// runs their consistency checks and tests them.
func TestOpRollbackCheckTest(t *testing.T) {
	o := newOpFixture(t, config.Cluster{})
	fm := newFakeMember(t, false)
	o.add(consoleInput("r1", fm.srv.URL))
	o.add(tokenInput("r2", fm.srv.URL))

	// 6 "a", 7 "abc": back to 6.
	op := o.run(`{"op":"rollback","members":["r1"]}`)
	if x := op.Outcomes[0]; x.Status != OutcomeDone || x.Result.Version != 8 || x.Result.From != 6 || !strings.Contains(x.Detail, "rolled back to version 6, as version 8; w") {
		t.Fatalf("%+v", x)
	}
	// A reload makes 9, the same config as 8: the previous one is 7.
	o.run(`{"op":"reload","members":["r2"]}`)
	op = o.run(`{"op":"rollback","members":["r2"]}`)
	if x := op.Outcomes[0]; x.Status != OutcomeDone || x.Result.From != 7 || x.Result.Version != 10 {
		t.Fatalf("%+v", x)
	}
	// A member with nothing earlier.
	fm.set(func(f *fakeMember) { f.hist = []fakeVersion{{1, `"z"`}, {2, `"z"`}} })
	op = o.run(`{"op":"rollback","members":["r1"]}`)
	if x := op.Outcomes[0]; x.Status != OutcomeFailed || x.Error.Kind != KindNoEarlier || !strings.Contains(x.Error.Message, "no earlier config") {
		t.Fatalf("%+v", x)
	}
	// A member that refuses the config.
	fm.set(func(f *fakeMember) { f.hist = nil; f.opFail = 400 })
	op = o.run(`{"op":"rollback","members":["r1","r2"]}`)
	for _, x := range op.Outcomes {
		if x.Status != OutcomeFailed || x.Error.Status != 400 || x.Error.Kind != KindRefused ||
			!strings.Contains(x.Error.Message, "the running config stays: /routes/0: no (and 1 more)") {
			t.Errorf("%+v", x.Error)
		}
	}
	fm.set(func(f *fakeMember) { f.opFail = 403 })
	if x := o.run(`{"op":"reload","members":["r2"]}`).Outcomes[0]; x.Error == nil || x.Error.Kind != KindForbidden {
		t.Errorf("%+v", x)
	}
	fm.set(func(f *fakeMember) { f.opFail = 401 })
	if x := o.run(`{"op":"reload","members":["r2"]}`).Outcomes[0]; x.Error == nil || x.Error.Kind != KindAuth {
		t.Errorf("%+v", x)
	}
	fm.set(func(f *fakeMember) { f.opFail = http.StatusTeapot })
	if x := o.run(`{"op":"reload","members":["r2"]}`).Outcomes[0]; x.Error == nil || x.Error.Kind != KindRefused || !strings.HasSuffix(x.Error.Message, "I'm a teapot") {
		t.Errorf("%+v", x)
	}
	fm.set(func(f *fakeMember) { f.opFail = 0 })

	op = o.run(`{"op":"check","members":["r1","r2"]}`)
	for _, x := range op.Outcomes {
		if x.Status != OutcomeDone || x.Result.Drift == nil || x.Result.Drift.Count != 2 || x.Detail != "2 differences" {
			t.Errorf("%+v", x)
		}
	}
	op = o.run(`{"op":"test","members":["r1","r2"]}`)
	for _, x := range op.Outcomes {
		if x.Status != OutcomeDone || x.Result.FitVersion != "v1.2.3" || x.Result.NodeID == "" || !strings.Contains(x.Detail, "fit v1.2.3") {
			t.Errorf("%+v", x)
		}
	}

	// Answers that are not the JSON expected.
	fm.set(func(f *fakeMember) { f.badJSON = true })
	for _, op := range []string{"check", "rollback"} {
		if x := o.run(`{"op":"` + op + `","members":["r2"]}`).Outcomes[0]; x.Error == nil || x.Error.Kind != KindBadAnswer {
			t.Errorf("%s: %+v", op, x)
		}
	}

	// The audit log cannot be written: the operation runs, and says so.
	o.store.mu.Lock()
	o.store.fail = errors.New("disk full")
	o.store.mu.Unlock()
	op = o.run(`{"op":"test","members":["r1"]}`)
	waitFor(t, "the log", func() bool {
		return strings.Contains(o.log.String(), "the operation "+op.ID+" is not in the audit log")
	})
	o.store.mu.Lock()
	o.store.fail = nil
	o.store.mu.Unlock()
}

// TestOpUnsupported refuses an operation on a member that does not list
// what it needs, read from its GET /node when no poll read it yet.
func TestOpUnsupported(t *testing.T) {
	o := newOpFixture(t, config.Cluster{PollInterval: config.Duration(time.Hour)})
	fm := newFakeMember(t, false)
	fm.set(func(f *fakeMember) { f.caps = []string{"node"} })
	o.add(tokenInput("old", fm.srv.URL))
	for _, op := range []string{"reload", "rollback", "check"} {
		x := o.run(`{"op":"` + op + `","members":["old"]}`).Outcomes[0]
		if x.Error == nil || x.Error.Kind != KindUnsupported {
			t.Errorf("%s: %+v", op, x)
		}
	}
	// No poll read it, and its GET /node fails.
	fm.set(func(f *fakeMember) { f.nodeStatus = 500 })
	o.add(tokenInput("old2", fm.srv.URL))
	o.m.mu.Lock()
	n := o.m.nodes["old2"]
	o.m.mu.Unlock()
	n.mu.Lock()
	n.state.Node = nil
	n.mu.Unlock()
	if x := o.run(`{"op":"reload","members":["old2"]}`).Outcomes[0]; x.Error == nil || x.Error.Status != 500 || strings.Contains(x.Error.Message, fakeToken) {
		t.Errorf("%+v", x)
	}
}

// TestOpRefused refuses what is not an operation, or runs on nothing, or on
// members busy with another; and bounds the operations at once.
func TestOpRefused(t *testing.T) {
	o := newOpFixture(t, config.Cluster{})
	fm := newFakeMember(t, false)
	o.add(tokenInput("a", fm.srv.URL))
	o.add(tokenInput("b", fm.srv.URL))
	if _, err := o.m.AddGroup("tester", &GroupInput{Name: "g", Members: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.m.AddGroup("tester", &GroupInput{Name: "empty", Selector: "nothing"}); err != nil {
		t.Fatal(err)
	}
	for body, want := range map[string]int{
		`{`:                                                                               400,
		`{"op":"dance","members":["a"]}`:                                                  400,
		`{"op":"reload"}`:                                                                 400,
		`{"op":"reload","group":"nothing"}`:                                               404,
		`{"op":"reload","group":"empty"}`:                                                 400,
		`{"op":"reload","members":["zz"]}`:                                                404,
		`{"op":"reload","group":"g","members":["b"]}`:                                     400,
		`{"op":"reload","members":["a"],"purge":{"all":true}}`:                            400,
		`{"op":"purge","members":["a"]}`:                                                  400,
		`{"op":"purge","members":["a"],"purge":{}}`:                                       400,
		`{"op":"purge","members":["a"],"purge":{"all":true,"tags":["t"]}}`:                400,
		`{"op":"purge","members":["a"],"purge":{"keys":["/relative"]}}`:                   400,
		`{"op":"purge","members":["a"],"purge":{"tags":[""]}}`:                            400,
		`{"op":"purge","members":["a"],"purge":{"zone":"a\u0001"}}`:                       400,
		`{"op":"reload","members":["a"],"x":"` + strings.Repeat("x", MaxBodyBytes) + `"}`: 413,
	} {
		o.startOp(body, want)
	}
	many := make([]string, MaxPurgeItems+1)
	for i := range many {
		many[i] = "t"
	}
	o.startOp(js(OpRequest{Op: OpPurge, Members: []string{"a"}, Purge: &PurgeParams{Tags: many}}), 400)

	// a group's members, a selection in it.
	if op := o.run(`{"op":"test","group":"g","members":["a"]}`); len(op.Outcomes) != 1 || op.Group != "g" {
		t.Errorf("%+v", op)
	}
	if op := o.run(`{"op":"test","members":["b","a","b"]}`); len(op.Outcomes) != 2 || op.Outcomes[0].Node != "a" {
		t.Errorf("%+v", op)
	}

	// A member busy with an operation is not given another, and at most
	// MaxRunningOps run at once.
	o.m.mu.Lock()
	o.m.ops.busy["a"] = "held"
	o.m.mu.Unlock()
	o.startOp(`{"op":"test","members":["a","b"]}`, 409)
	o.m.mu.Lock()
	delete(o.m.ops.busy, "a")
	o.m.ops.running = MaxRunningOps
	o.m.mu.Unlock()
	o.startOp(`{"op":"test","members":["b"]}`, 429)
	o.m.mu.Lock()
	o.m.ops.running = 0
	o.m.mu.Unlock()

	// A read-only manager runs none: it could not audit them.
	o.m.mu.Lock()
	o.m.readOnly = "test"
	o.m.mu.Unlock()
	o.startOp(`{"op":"test","members":["a"]}`, 409)
	o.m.mu.Lock()
	o.m.readOnly = ""
	o.m.mu.Unlock()
	o.m.Close()
	if _, err := o.m.StartOp("u", &OpRequest{Op: OpTest, Members: []string{"a"}}); !errors.Is(err, errClosed) {
		t.Error(err)
	}
	o.startOp(`{"op":"test","members":["a"]}`, 503)
}

// TestOpClose stops the manager while an operation runs: it ends, each
// member's outcome said, and Close returns.
func TestOpClose(t *testing.T) {
	f := newFixture(t, config.Cluster{MaxConcurrency: 1})
	fm := newFakeMember(t, false)
	fm.set(func(f *fakeMember) { f.delay = time.Second })
	for _, name := range []string{"a", "b", "c"} {
		f.add(tokenInput(name, fm.srv.URL))
	}
	op, err := f.m.StartOp("u", &OpRequest{Op: OpTest, Members: []string{"a", "b", "c"}})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the operation to start", func() bool {
		got, _ := f.m.OpByID(op.ID)
		return got.Counts.Running == 1
	})
	f.m.waitOps(time.Millisecond) // it runs: the wait ends first
	f.m.Close()
	got, _ := f.m.OpByID(op.ID)
	if !got.Finished || got.Counts.Failed != 3 {
		t.Errorf("%+v", got)
	}
	if !strings.Contains(f.log.String(), "the operation "+op.ID+" test ended") {
		t.Errorf("%s", f.log.String())
	}
}

// TestOpsKept keeps MaxKeptOps operations, and bounds the audit log by its
// size.
func TestOpsKept(t *testing.T) {
	o := newOpFixture(t, config.Cluster{})
	fm := newFakeMember(t, false)
	o.add(tokenInput("a", fm.srv.URL))
	var first string
	for i := range MaxKeptOps + 2 {
		op := o.run(`{"op":"test","members":["a"]}`)
		if i == 0 {
			first = op.ID
		}
	}
	if ops := o.m.Ops(); len(ops) != MaxKeptOps {
		t.Errorf("%d kept", len(ops))
	}
	if _, ok := o.m.OpByID(first); ok {
		t.Error("the oldest kept")
	}
	// The latest is in the audit log, and the store.
	if !strings.Contains(string(o.store.Section(Section)), `"action":"op.test"`) {
		t.Error("not in the store")
	}

	big := AuditEntry{Action: "op.test", Outcomes: make([]AuditOutcome, 256)}
	for i := range big.Outcomes {
		big.Outcomes[i] = AuditOutcome{Node: strings.Repeat("n", 64), Status: OutcomeFailed, Error: strings.Repeat("e", maxAuditError)}
	}
	var log []AuditEntry
	for range 100 {
		log = trimAudit(append(log, big))
	}
	if size := len(log) * auditSize(&big); size > MaxAuditBytes || len(log) < 10 || len(log) == 100 {
		t.Errorf("%d entries, %d bytes", len(log), size)
	}
	if got := trimAudit(make([]AuditEntry, MaxAudit+5)); len(got) != MaxAudit {
		t.Errorf("%d", len(got))
	}
	if got := clip("héllo", 2); got != "h…" {
		t.Errorf("%q", got)
	}
	if got := (&PurgeParams{All: true}).describe(); got != "all of every zone" {
		t.Error(got)
	}
}

// TestOpMemberRemoved removes a member while an operation waits to reach
// it: it fails, saying so.
func TestOpMemberRemoved(t *testing.T) {
	f := newFixture(t, config.Cluster{MaxConcurrency: 1})
	slow, fast := newFakeMember(t, false), newFakeMember(t, false)
	slow.set(func(f *fakeMember) { f.delay = 500 * time.Millisecond })
	f.add(tokenInput("a", slow.srv.URL))
	f.add(tokenInput("b", fast.srv.URL))
	op, err := f.m.StartOp("u", &OpRequest{Op: OpTest, Members: []string{"a", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a to run", func() bool { got, _ := f.m.OpByID(op.ID); return got.Outcomes[0].Status == OutcomeRunning })
	if err := f.m.Remove("u", "b"); err != nil {
		t.Fatal(err)
	}
	var got Op
	waitFor(t, "the operation to end", func() bool { got, _ = f.m.OpByID(op.ID); return got.Finished })
	if x := got.Outcomes[1]; x.Status != OutcomeFailed || !strings.Contains(x.Error.Message, `no member is named "b"`) {
		t.Errorf("%+v", x)
	}
}

// TestTrimOps never drops an operation that runs.
func TestTrimOps(t *testing.T) {
	m := &Manager{}
	for range MaxKeptOps + 1 {
		m.ops.list = append(m.ops.list, &Op{})
	}
	m.trimOpsLocked()
	if len(m.ops.list) != MaxKeptOps+1 {
		t.Errorf("%d kept", len(m.ops.list))
	}
	m.ops.list[3].Finished = true
	m.trimOpsLocked()
	if len(m.ops.list) != MaxKeptOps || slices.ContainsFunc(m.ops.list, func(o *Op) bool { return o.Finished }) {
		t.Errorf("%d kept", len(m.ops.list))
	}
}
