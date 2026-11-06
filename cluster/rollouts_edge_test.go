package cluster

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestRolloutEdges: the rarer ways a plan, an apply, a gate and a rollback
// go.
func TestRolloutEdges(t *testing.T) {
	t.Run("plan", func(t *testing.T) {
		k := newRoFixture(t, 4)
		k.ms["m1"].set(func(f *roMember) { f.dryClose = true })
		k.ms["m2"].set(func(f *roMember) { f.dryUnchanged = true })
		r := k.plan(RolloutRequest{Kind: RolloutTemplate, Template: "api"})
		if x := memberOf(t, r, "m1"); x.Status != MemberError || x.Error.Kind != KindUnreachable {
			t.Errorf("%+v", x)
		}
		if x := memberOf(t, r, "m2"); x.Status != MemberUnchanged || x.Changes != nil {
			t.Errorf("%+v", x)
		}
		// A copy to a member whose config cannot be read.
		k.ms["m4"].set(func(f *roMember) { f.configStatus = 500 })
		r = k.plan(RolloutRequest{Kind: RolloutCopy, From: "m3", Paths: []string{"routes[own]"}, Nodes: []string{"m4"}})
		if x := memberOf(t, r, "m4"); x.Status != MemberError {
			t.Errorf("%+v", x)
		}
	})
	t.Run("apply cut", func(t *testing.T) {
		// The apply's connection cut: not applied, the member says so; and
		// with the member unread after, not known, then found never applied.
		k := newRoFixtureWith(t, 2, slowPolls)
		k.ms["m1"].set(func(f *roMember) { f.putClose = true })
		k.ms["m2"].set(func(f *roMember) { f.putClose, f.nodeFailAfterPut = true, true })
		r := k.waitRollout(k.oneMember(fast(Strategy{Mode: StrategyAll}), "m1", "m2").ID, "failed", ended)
		if x := memberOf(t, r, "m1"); x.Status != MemberFailed || x.Unsure || x.Error.Kind != KindUnreachable {
			t.Errorf("%+v", x)
		}
		if x := memberOf(t, r, "m2"); x.Status != MemberFailed || !strings.Contains(x.Error.Message, "was not answered (") {
			t.Errorf("%+v", x)
		}
	})
	t.Run("member removed", func(t *testing.T) {
		k := newRoFixture(t, 1)
		r := k.plan(RolloutRequest{Kind: RolloutTemplate, Template: "api"})
		if err := k.m.Remove("tester", "m1"); err != nil {
			t.Fatal(err)
		}
		r = k.waitRollout(k.start(r.ID, fast(Strategy{Mode: StrategyAll})).ID, "failed", ended)
		if x := memberOf(t, r, "m1"); x.Status != MemberRollbackFailed || x.Error.Kind != KindUnreachable {
			t.Fatalf("%s %+v", r.Reason, x)
		}
	})
	t.Run("all skipped", func(t *testing.T) {
		k := newRoFixture(t, 1)
		r := k.plan(RolloutRequest{Kind: RolloutTemplate, Template: "api"})
		k.ms["m1"].edit(roBase(`,"x"`))
		r = k.waitRollout(k.start(r.ID, fast(Strategy{Mode: StrategyAll})).ID, "done", ended)
		if r.State != RolloutDone || memberOf(t, r, "m1").Status != MemberSkipped {
			t.Fatalf("%s %s", r.State, statuses(r))
		}
	})
	t.Run("unreachable after", func(t *testing.T) {
		// Gone after its apply: the gate, its counts and its rollback fail.
		k := newRoFixtureWith(t, 1, slowPolls)
		r := k.oneMember(Strategy{Mode: StrategyAll, Gate: Gate{WaitMs: 200, Max5xxRatio: 0.5, WindowMs: 200, MinRequests: 1}})
		<-k.ms["m1"].applied
		time.Sleep(50 * time.Millisecond)
		k.ms["m1"].srv.CloseClientConnections()
		k.ms["m1"].srv.Close()
		r = k.waitRollout(r.ID, "failed", ended)
		if x := memberOf(t, r, "m1"); x.Status != MemberRollbackFailed || x.Error.Kind != KindUnreachable {
			t.Fatalf("%s %+v", r.Reason, x)
		}
	})
	t.Run("gate reads", func(t *testing.T) {
		k := newRoFixtureWith(t, 1, slowPolls)
		f := k.ms["m1"]
		x := RolloutMember{Node: "m1", NewETag: f.cur().etag}
		g := Gate{Max5xxRatio: 0.5, MinRequests: 1}
		if res := k.m.gateMember(&x, &g, nil); res.Passed || !strings.Contains(res.Reasons[0], "could not be counted") {
			t.Errorf("%+v", res)
		}
		f.set(func(f *roMember) { f.statusStatus = 500 })
		if res := k.m.gateMember(&x, &g, &requestCounts{}); res.Passed || !strings.Contains(res.Reasons[0], "could not be read") {
			t.Errorf("%+v", res)
		}
		f.set(func(f *roMember) { f.statusStatus, f.statusBad = 0, true })
		if res := k.m.gateMember(&x, &g, &requestCounts{}); res.Passed || !strings.Contains(res.Reasons[0], "not the JSON expected") {
			t.Errorf("%+v", res)
		}
	})
	t.Run("rollback fails", func(t *testing.T) {
		// A rollback a person asks for, of which one member fails.
		k := newRoFixture(t, 2)
		r := k.waitRollout(k.oneMember(fast(Strategy{Mode: StrategyAll}), "m1", "m2").ID, "done", ended)
		k.ms["m2"].set(func(f *roMember) { f.rollbackStatus = 500 })
		if _, err := k.m.SteerRollout("bob", r.ID, SteerRollback); err != nil {
			t.Fatal(err)
		}
		r = k.waitRollout(r.ID, "rolled back", ended)
		if r.State != RolloutRolledBack || !strings.Contains(r.Reason, "1 members could not be rolled back") {
			t.Fatalf("%s %s", r.State, r.Reason)
		}
	})
	t.Run("abort while applying", func(t *testing.T) {
		// Members beyond max_concurrency wait their turn; aborted, they are
		// left pending.
		k := newRoFixture(t, 6)
		for _, f := range k.ms {
			f.set(func(f *roMember) { f.putDelay = 200 * time.Millisecond })
		}
		r := k.plan(RolloutRequest{Kind: RolloutTemplate, Template: "api"})
		r = k.start(r.ID, fast(Strategy{Mode: StrategyAll}))
		k.waitMember(r.ID, "m1", MemberApplying)
		if _, err := k.m.SteerRollout("bob", r.ID, SteerAbort); err != nil {
			t.Fatal(err)
		}
		r = k.waitRollout(r.ID, "aborted", ended)
		if r.State != RolloutAborted || r.Counts[MemberPending] == 0 {
			t.Fatalf("%s %s", r.State, statuses(r))
		}
	})
	t.Run("woken for nothing", func(t *testing.T) {
		k := newRoFixture(t, 2)
		r := k.plan(RolloutRequest{Kind: RolloutTemplate, Template: "api"})
		r = k.start(r.ID, fast(Strategy{Mode: StrategyRolling, BatchSize: 1, PauseMs: 100}))
		k.waitRollout(r.ID, "between batches", func(r Rollout) bool { return r.Batch == 1 && !r.NextAt.IsZero() })
		k.m.mu.Lock()
		rr := k.m.ro.runs[r.ID]
		k.m.mu.Unlock()
		if rr != nil {
			wake(rr)
			wake(rr) // one wake is enough
		}
		if r = k.waitRollout(r.ID, "done", ended); r.State != RolloutDone {
			t.Fatalf("%s", r.State)
		}
	})
	t.Run("kept and saved", func(t *testing.T) {
		k := newRoFixture(t, 1)
		k.m.mu.Lock()
		for i := range MaxKeptRollouts + 5 {
			k.m.ro.list = append(k.m.ro.list, &Rollout{ID: "old" + strconv.Itoa(i), State: RolloutDone})
		}
		k.m.mu.Unlock()
		if n := len(k.m.Rollouts()); n != MaxKeptRollouts {
			t.Errorf("%d kept", n)
		}
		if _, ok := k.m.RolloutByID("old0"); ok {
			t.Error("the oldest is kept")
		}
		// Past the bound with none ended: none is dropped.
		k.m.mu.Lock()
		k.m.ro.list = nil
		for i := range MaxKeptRollouts + 1 {
			k.m.ro.list = append(k.m.ro.list, &Rollout{ID: "p" + strconv.Itoa(i), State: RolloutPlanned, Planned: time.Now()})
		}
		k.m.mu.Unlock()
		if n := len(k.m.Rollouts()); n != MaxKeptRollouts+1 {
			t.Errorf("%d kept", n)
		}
		k.m.mu.Lock()
		k.m.ro.list = nil
		k.m.mu.Unlock()
		k.m.ro.wg.Add(1)
		k.m.waitRollouts(time.Millisecond)
		k.m.ro.wg.Done()
		k.store.mu.Lock()
		k.store.fail = errors.New("disk full")
		k.store.mu.Unlock()
		k.m.saveRollouts()
		if !strings.Contains(k.log.String(), "the rollouts were not saved: saving the members: disk full") {
			t.Error(k.log.String())
		}
		k.store.mu.Lock()
		k.store.fail = nil
		k.store.mu.Unlock()
	})
	t.Run("helpers", func(t *testing.T) {
		var b strings.Builder
		b.WriteString(`{"errors":[`)
		for i := range MaxRolloutErrors + 3 {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(`{"path":"/a","message":"m"}`)
		}
		b.WriteString(`]}`)
		if n := len(configErrors([]byte(b.String()))); n != MaxRolloutErrors {
			t.Errorf("%d errors", n)
		}
		if got := clipList([]string{"a", "b", "c"}, 2, 1); len(got) != 2 {
			t.Error(got)
		}
	})
}

// TestRolloutStopsMore: a manager stopped while a batch's counts are read
// before it is applied, and while its gate reads its members, leaves the
// rollout paused, its members as they were; a gate of a member applied
// before the stop is checked when it resumes.
func TestRolloutStopsMore(t *testing.T) {
	k := newRoFixtureWith(t, 1, slowPolls)
	k.ms["m1"].set(func(f *roMember) { f.statusDelay = 300 * time.Millisecond })
	r := k.oneMember(Strategy{Mode: StrategyAll, Gate: Gate{WaitMs: 1, Max5xxRatio: 0.5, WindowMs: 1, MinRequests: 1}})
	k.waitRollout(r.ID, "applying", func(r Rollout) bool { return r.Batches[0].State == BatchApplying })
	time.Sleep(50 * time.Millisecond)
	k.m.Close()
	k.start2()
	r, _ = k.m.RolloutByID(r.ID)
	if r.State != RolloutPaused || memberOf(t, r, "m1").Status != MemberPending || k.ms["m1"].version() != 1 {
		t.Fatalf("%s %s", r.State, statuses(r))
	}
	// Resumed, the gate's reads held, and the manager stopped again,
	// without waiting for them.
	k.ms["m1"].set(func(f *roMember) { f.statusDelay, f.nodeDelay = 0, 400*time.Millisecond })
	if _, err := k.m.SteerRollout("bob", r.ID, SteerResume); err != nil {
		t.Fatal(err)
	}
	k.waitMember(r.ID, "m1", MemberApplied)
	k.waitRollout(r.ID, "gating", func(r Rollout) bool { return r.Batches[0].State == BatchGating })
	time.Sleep(100 * time.Millisecond)
	old := closeWait
	closeWait = 10 * time.Millisecond
	k.m.Close()
	closeWait = old
	k.m.waitRollouts(5 * time.Second)
	k.ms["m1"].set(func(f *roMember) { f.nodeDelay = 0 })
	k.start2()
	r, _ = k.m.RolloutByID(r.ID)
	if r.State != RolloutPaused || memberOf(t, r, "m1").Status != MemberApplied {
		t.Fatalf("%s %s", r.State, statuses(r))
	}
	if _, err := k.m.SteerRollout("bob", r.ID, SteerResume); err != nil {
		t.Fatal(err)
	}
	if r = k.waitRollout(r.ID, "done", ended); r.State != RolloutDone || memberOf(t, r, "m1").Applied != 2 {
		t.Fatalf("%s %s %s", r.State, r.Reason, statuses(r))
	}
}
