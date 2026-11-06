package cluster

import (
	"strings"
	"testing"
	"time"
)

// oneMember plans the template's change on m1 alone, and starts it with s.
func (k *roFixture) oneMember(s Strategy, nodes ...string) Rollout {
	k.t.Helper()
	if len(nodes) == 0 {
		nodes = []string{"m1"}
	}
	r := k.plan(RolloutRequest{Kind: RolloutTemplate, Template: "api", Nodes: nodes})
	if r.Counts[MemberReady] != len(nodes) {
		k.t.Fatalf("%s", statuses(r))
	}
	return k.start(r.ID, s)
}

// TestRolloutGates: each check of the gate, failed and passed.
func TestRolloutGates(t *testing.T) {
	ratio := func(min int) Strategy {
		return Strategy{Mode: StrategyAll, Gate: Gate{WaitMs: 1, Max5xxRatio: 0.5, WindowMs: 20, MinRequests: min}}
	}
	t.Run("no metrics", func(t *testing.T) {
		k := newRoFixture(t, 2)
		k.ms["m2"].set(func(f *roMember) { f.noMetrics = true })
		r := k.waitRollout(k.oneMember(ratio(1), "m1", "m2").ID, "failed", ended)
		if r.State != RolloutFailed || !strings.Contains(r.Reason, "cannot be gated on m2") || k.ms["m1"].version() != 1 || k.ms["m2"].version() != 1 {
			t.Fatalf("%s %s", r.Reason, statuses(r))
		}
		if !strings.Contains(r.Reason, "nothing was applied to roll back") {
			t.Error(r.Reason)
		}
	})
	t.Run("status unread", func(t *testing.T) {
		k := newRoFixture(t, 1)
		k.ms["m1"].set(func(f *roMember) { f.statusStatus = 500 })
		r := k.waitRollout(k.oneMember(ratio(1)).ID, "failed", ended)
		if x := memberOf(t, r, "m1"); x.Status != MemberFailed || !strings.Contains(x.Error.Message, "GET /status could not be read") {
			t.Fatalf("%+v", x)
		}
	})
	t.Run("too few requests", func(t *testing.T) {
		k := newRoFixture(t, 1)
		k.template(`"5xx-please"`)
		r := k.waitRollout(k.oneMember(ratio(1_000_000)).ID, "done", ended)
		x := memberOf(t, r, "m1")
		if r.State != RolloutDone || len(x.Gate.Notes) != 1 || x.Gate.Ratio != nil || x.Gate.Requests == nil {
			t.Fatalf("%s %+v", r.Reason, x.Gate)
		}
	})
	t.Run("drift", func(t *testing.T) {
		k := newRoFixture(t, 1)
		k.template(`"drift-please"`)
		r := k.waitRollout(k.oneMember(fast(Strategy{Mode: StrategyAll})).ID, "failed", ended)
		x := memberOf(t, r, "m1")
		if x.Status != MemberRolledBack || !strings.Contains(r.Reason, "finds 1 differences, 0 before") || *x.Gate.DriftBefore != 0 || *x.Gate.DriftAfter != 1 {
			t.Fatalf("%s %+v", r.Reason, x.Gate)
		}
	})
	t.Run("counts went back", func(t *testing.T) {
		k := newRoFixtureWith(t, 1, slowPolls)
		r := k.oneMember(Strategy{Mode: StrategyAll, Gate: Gate{WaitMs: 1, Max5xxRatio: 0.5, WindowMs: 300, MinRequests: 1}})
		k.waitRollout(r.ID, "gating", func(r Rollout) bool { return r.Batches[0].State == BatchGating })
		time.Sleep(50 * time.Millisecond)
		k.ms["m1"].set(func(f *roMember) { f.resetCounts = true })
		r = k.waitRollout(r.ID, "failed", ended)
		if !strings.Contains(r.Reason, "went back") {
			t.Fatalf("%s", r.Reason)
		}
	})
	t.Run("changed and unread", func(t *testing.T) {
		// Changed on the member during the gate: the gate fails, and its
		// rollback finds the config changed, and leaves it.
		k := newRoFixtureWith(t, 1, slowPolls)
		r := k.oneMember(Strategy{Mode: StrategyAll, Gate: Gate{WaitMs: 300}})
		<-k.ms["m1"].applied
		k.ms["m1"].edit(roBase(`,"edited"`))
		k.ms["m1"].set(func(f *roMember) { f.checkStatus = 500 })
		r = k.waitRollout(r.ID, "failed", ended)
		x := memberOf(t, r, "m1")
		if x.Status != MemberRollbackFailed || !strings.Contains(r.Reason, "runs another config now") || !strings.Contains(r.Reason, "consistency check could not be read") ||
			!strings.Contains(x.Error.Message, "not rolled back") || !strings.Contains(r.Reason, "1 members could not be") {
			t.Fatalf("%s %+v", r.Reason, x)
		}
	})
	t.Run("unreachable", func(t *testing.T) {
		k := newRoFixtureWith(t, 1, slowPolls)
		r := k.oneMember(Strategy{Mode: StrategyAll, Gate: Gate{WaitMs: 300, Max5xxRatio: 0.5, WindowMs: 300}})
		<-k.ms["m1"].applied
		k.ms["m1"].set(func(f *roMember) { f.nodeStatus = 500 })
		r = k.waitRollout(r.ID, "failed", ended)
		if !strings.Contains(r.Reason, "not reachable") {
			t.Fatalf("%s", r.Reason)
		}
	})
	t.Run("baseline unread", func(t *testing.T) {
		k := newRoFixtureWith(t, 1, slowPolls)
		k.ms["m1"].set(func(f *roMember) { f.noMetrics = false })
		r := k.plan(RolloutRequest{Kind: RolloutTemplate, Template: "api"})
		// Counts read before the apply, and not after it.
		r = k.start(r.ID, Strategy{Mode: StrategyAll, Gate: Gate{WaitMs: 1, Max5xxRatio: 0.5, WindowMs: 50, MinRequests: 1}})
		<-k.ms["m1"].applied
		k.ms["m1"].set(func(f *roMember) { f.noMetrics = true })
		r = k.waitRollout(r.ID, "failed", ended)
		if !strings.Contains(r.Reason, "counts no requests now") && !strings.Contains(r.Reason, "could not be counted") {
			t.Fatalf("%s", r.Reason)
		}
	})
}

// TestRolloutApplies: an apply refused fails its batch, whose others are
// rolled back; an apply answered badly is found applied by the member's
// ETag; one whose fate is not known is rolled back, or found never applied.
func TestRolloutApplies(t *testing.T) {
	t.Run("refused", func(t *testing.T) {
		k := newRoFixture(t, 2)
		k.ms["m2"].set(func(f *roMember) { f.putStatus = 500 })
		r := k.waitRollout(k.oneMember(fast(Strategy{Mode: StrategyAll}), "m1", "m2").ID, "failed", ended)
		if x := memberOf(t, r, "m2"); x.Status != MemberFailed || x.Error.Kind != KindRefused || k.ms["m2"].version() != 1 {
			t.Fatalf("%+v", x)
		}
		if x := memberOf(t, r, "m1"); x.Status != MemberRolledBack || x.Gate != nil && x.Gate.Passed {
			t.Fatalf("%+v", x)
		}
	})
	t.Run("answered badly", func(t *testing.T) {
		k := newRoFixture(t, 1)
		k.ms["m1"].set(func(f *roMember) { f.badPut = true })
		r := k.waitRollout(k.oneMember(fast(Strategy{Mode: StrategyAll})).ID, "done", ended)
		if x := memberOf(t, r, "m1"); r.State != RolloutDone || x.Applied != 2 {
			t.Fatalf("%s %+v", r.Reason, x)
		}
	})
	t.Run("answered badly, not applied", func(t *testing.T) {
		k := newRoFixture(t, 1)
		k.ms["m1"].set(func(f *roMember) { f.dropPut = true })
		r := k.waitRollout(k.oneMember(fast(Strategy{Mode: StrategyAll})).ID, "failed", ended)
		if x := memberOf(t, r, "m1"); x.Status != MemberFailed || x.Error.Kind != KindBadAnswer {
			t.Fatalf("%s %+v", r.Reason, x)
		}
	})
	t.Run("unsure, applied", func(t *testing.T) {
		k := newRoFixtureWith(t, 1, slowPolls)
		k.ms["m1"].set(func(f *roMember) { f.badPut, f.nodeFailOnce = true, true })
		r := k.waitRollout(k.oneMember(fast(Strategy{Mode: StrategyAll})).ID, "failed", ended)
		if x := memberOf(t, r, "m1"); x.Status != MemberRolledBack || !strings.Contains(x.Error.Message, "is not known") || k.ms["m1"].version() != 3 {
			t.Fatalf("%s %+v", r.Reason, x)
		}
	})
	t.Run("unsure, never applied", func(t *testing.T) {
		k := newRoFixtureWith(t, 1, slowPolls)
		k.ms["m1"].set(func(f *roMember) { f.dropPut, f.nodeFailOnce = true, true })
		r := k.waitRollout(k.oneMember(fast(Strategy{Mode: StrategyAll})).ID, "failed", ended)
		if x := memberOf(t, r, "m1"); x.Status != MemberFailed || x.Unsure || k.ms["m1"].version() != 1 || !strings.Contains(r.Reason, "rolled back") {
			t.Fatalf("%s %+v", r.Reason, x)
		}
	})
	t.Run("rollbacks fail", func(t *testing.T) {
		for status, kind := range map[int]string{404: KindNoEarlier, 500: KindRefused} {
			k := newRoFixture(t, 1)
			k.template(`"5xx-please"`)
			k.ms["m1"].set(func(f *roMember) { f.rollbackStatus = status })
			r := k.waitRollout(k.oneMember(Strategy{Mode: StrategyAll, Gate: Gate{WaitMs: 1, Max5xxRatio: 0.1, WindowMs: 20, MinRequests: 1}}).ID, "failed", ended)
			if x := memberOf(t, r, "m1"); x.Status != MemberRollbackFailed || x.Error.Kind != kind || r.Batches[0].State != BatchFailed {
				t.Errorf("%d: %+v %+v", status, x, r.Batches)
			}
		}
	})
}

// TestRolloutSteering: pause, resume, a pause taken back, abort while
// between batches, during a gate and while paused, and a rollback asked
// for once it is done.
func TestRolloutSteering(t *testing.T) {
	k := newRoFixture(t, 3)
	between := func(r Rollout) bool { return r.Batch == 1 && !r.NextAt.IsZero() }
	r := k.plan(RolloutRequest{Kind: RolloutTemplate, Template: "api"})
	r = k.start(r.ID, fast(Strategy{Mode: StrategyRolling, BatchSize: 1, PauseMs: 3600_000}))
	k.waitRollout(r.ID, "between batches", between)
	if _, err := k.m.SteerRollout("bob", r.ID, SteerPause); err != nil {
		t.Fatal(err)
	}
	r = k.waitRollout(r.ID, "paused", func(r Rollout) bool { return r.State == RolloutPaused })
	if _, err := k.m.SteerRollout("bob", r.ID, SteerPause); err == nil {
		t.Error("paused twice")
	}
	if _, err := k.m.SteerRollout("bob", r.ID, SteerResume); err != nil {
		t.Fatal(err)
	}
	r = k.waitRollout(r.ID, "between batches again", func(r Rollout) bool { return r.Batch == 2 && !r.NextAt.IsZero() })
	if _, err := k.m.SteerRollout("bob", r.ID, SteerAbort); err != nil {
		t.Fatal(err)
	}
	r = k.waitRollout(r.ID, "aborted", ended)
	if r.State != RolloutAborted || memberOf(t, r, "m3").Status != MemberPending || memberOf(t, r, "m2").Status != MemberDone {
		t.Fatalf("%s %s", r.State, statuses(r))
	}
	// Rolled back as asked: m1 and m2.
	if _, err := k.m.SteerRollout("bob", r.ID, SteerRollback); err != nil {
		t.Fatal(err)
	}
	r = k.waitRollout(r.ID, "rolled back", ended)
	if r.State != RolloutRolledBack || r.Counts[MemberRolledBack] != 2 || k.ms["m1"].version() != 3 || strings.Contains(k.ms["m2"].config(), `"api"`) {
		t.Fatalf("%s %s", r.State, statuses(r))
	}
	if _, err := k.m.SteerRollout("bob", r.ID, SteerRollback); err == nil {
		t.Error("rolled back twice")
	}

	// A pause taken back before it is taken; an abort during a gate.
	k.ms["m1"].set(func(f *roMember) { f.putDelay = 200 * time.Millisecond })
	r = k.plan(RolloutRequest{Kind: RolloutTemplate, Template: "api"})
	r = k.start(r.ID, Strategy{Mode: StrategyRolling, BatchSize: 2, Gate: Gate{WaitMs: 600_000}})
	k.waitMember(r.ID, "m1", MemberApplying)
	if _, err := k.m.SteerRollout("bob", r.ID, SteerPause); err != nil {
		t.Fatal(err)
	}
	if _, err := k.m.SteerRollout("bob", r.ID, SteerResume); err != nil {
		t.Fatal(err)
	}
	r = k.waitRollout(r.ID, "gating", func(r Rollout) bool { return r.Batches[0].State == BatchGating })
	if _, err := k.m.SteerRollout("bob", r.ID, SteerAbort); err != nil {
		t.Fatal(err)
	}
	r = k.waitRollout(r.ID, "aborted", ended)
	if r.State != RolloutAborted || r.Batches[0].State != BatchAborted || memberOf(t, r, "m1").Status != MemberApplied {
		t.Fatalf("%s %+v %s", r.State, r.Batches, statuses(r))
	}

	// An abort while paused; a rollback while waiting.
	k.ms["m1"].set(func(f *roMember) { f.putDelay = 0 })
	k.template(`"v2"`)
	r = k.plan(RolloutRequest{Kind: RolloutSync})
	r = k.start(r.ID, fast(Strategy{Mode: StrategyRolling, BatchSize: 1, PauseMs: 3600_000}))
	k.waitRollout(r.ID, "between batches", between)
	_, _ = k.m.SteerRollout("bob", r.ID, SteerPause)
	r = k.waitRollout(r.ID, "paused", func(r Rollout) bool { return r.State == RolloutPaused })
	if _, err := k.m.SteerRollout("bob", r.ID, SteerAbort); err != nil {
		t.Fatal(err)
	}
	if r, _ = k.m.RolloutByID(r.ID); r.State != RolloutAborted {
		t.Fatalf("%s", r.State)
	}
	k.template(`"v3"`)
	r = k.plan(RolloutRequest{Kind: RolloutSync})
	r = k.start(r.ID, fast(Strategy{Mode: StrategyCanary, Canary: 1}))
	r = k.waitRollout(r.ID, "waiting", func(r Rollout) bool { return r.State == RolloutWaiting })
	if _, err := k.m.SteerRollout("bob", r.ID, SteerRollback); err != nil {
		t.Fatal(err)
	}
	r = k.waitRollout(r.ID, "rolled back", ended)
	if r.State != RolloutRolledBack || r.Counts[MemberRolledBack] != 1 || r.Batches[0].State != BatchRolledBack {
		t.Fatalf("%s %s", r.State, statuses(r))
	}
	// Nothing to roll back.
	r = k.plan(RolloutRequest{Kind: RolloutSync})
	r = k.start(r.ID, fast(Strategy{Mode: StrategyCanary, Canary: 1}))
	r = k.waitRollout(r.ID, "waiting", func(r Rollout) bool { return r.State == RolloutWaiting })
	k.m.mu.Lock()
	for i := range k.m.rolloutLocked(r.ID).Members {
		k.m.rolloutLocked(r.ID).Members[i].Applied = 0
	}
	k.m.mu.Unlock()
	if _, err := k.m.SteerRollout("bob", r.ID, SteerRollback); err == nil {
		t.Error("rolled back nothing")
	}
	// A member held by another while the rollback is asked.
	k.m.mu.Lock()
	k.m.rolloutLocked(r.ID).Members[0].Applied = 4
	k.m.ops.busy[r.Members[0].Node] = "op z"
	k.m.mu.Unlock()
	if _, err := k.m.SteerRollout("bob", r.ID, SteerRollback); err == nil || !strings.Contains(err.Error(), "op z") {
		t.Error(err)
	}
}

// TestRolloutStops: a manager stopping while a rollout plans, waits
// between batches, gates and rolls back leaves it to be found where it
// was.
func TestRolloutStops(t *testing.T) {
	old := closeWait
	closeWait = 3 * time.Second
	t.Cleanup(func() { closeWait = old })
	k := newRoFixture(t, 2)
	r := k.plan(RolloutRequest{Kind: RolloutTemplate, Template: "api"})
	r = k.start(r.ID, Strategy{Mode: StrategyAll, Gate: Gate{WaitMs: 600_000}})
	k.waitRollout(r.ID, "gating", func(r Rollout) bool { return r.Batches[0].State == BatchGating })
	k.m.Close()
	k.start2()
	r, _ = k.m.RolloutByID(r.ID)
	if r.State != RolloutPaused || memberOf(t, r, "m1").Status != MemberApplied {
		t.Fatalf("%s %s", r.State, statuses(r))
	}
	// Planning cut short.
	k.ms["m1"].set(func(f *roMember) { f.putDelay = 0 })
	k.m.mu.Lock()
	k.m.ops.busy = nil
	k.m.mu.Unlock()
	k.ms["m2"].set(func(f *roMember) { f.dryStatus = 0 })
	r2, err := k.m.PlanRollout("ann", &RolloutRequest{Kind: RolloutSync})
	if err != nil {
		t.Fatal(err)
	}
	k.m.Close()
	k.start2()
	if x, _ := k.m.RolloutByID(r2.ID); x.State != RolloutExpired && x.State != RolloutPlanned {
		t.Errorf("%s", x.State)
	}
	// A rollback cut short.
	if _, err := k.m.SteerRollout("bob", r.ID, SteerRollback); err != nil {
		t.Fatal(err)
	}
	k.m.Close()
	k.start2()
	if x, _ := k.m.RolloutByID(r.ID); x.State != RolloutRolledBack && x.State != RolloutFailed {
		t.Errorf("%s %s", x.State, x.Reason)
	}
}
