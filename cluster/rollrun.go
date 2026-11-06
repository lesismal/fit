package cluster

import (
	"encoding/json"
	"fmt"
	"maps"
	stdhttp "net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// How a rollout runs: a goroutine of its own, which waits only for its
// members' calls, out on fib's asynchronous client and each bounded by the
// calls' timeout, and for its gates' and pauses' timers. It applies one
// batch at a time, at most max_concurrency members of it at once; a pause
// or an abort asked for is taken between batches (an abort also cuts a
// gate's wait short); a canary's go, a resume and a rollback start a
// goroutine anew. The rollout's state is written to the store when a batch
// starts and ends, and whenever it changes hands, so that a manager
// restarted in the middle finds it where it stopped, paused.

// capIfMatch and capDryRun are the capabilities a member's config changes
// need, as package admin names them (admin.CapIfMatch, admin.CapDryRun);
// capStatusRequests that of GET /status's requests (admin.CapStatusRequests).
const (
	capIfMatch        = "config.if_match"
	capDryRun         = "config.dry_run"
	capStatusRequests = "status.requests"
)

// The actions an AuditEntry records of rollouts; the steering ones are
// "rollout." and SteerRollout's what.
const (
	ActionRolloutPlan  = "rollout.plan"
	ActionRolloutStart = "rollout.start"
	ActionRolloutEnd   = "rollout.end"
)

// batchResult is how a batch went.
type batchResult struct {
	passed  bool
	why     string
	stopped bool // the manager stops
	aborted bool // a person aborted it during its gate's wait
}

// requestCounts are a member's GET /status's requests.
type requestCounts struct {
	Total   uint64            `json:"total"`
	Classes map[string]uint64 `json:"classes"`
}

// runRollout runs r, from the batch it is at, until it ends, pauses or
// waits for a person. A goroutine of its own.
func (m *Manager) runRollout(r *Rollout, rr *rolloutRun) {
	defer m.ro.wg.Done()
	defer func() {
		m.mu.Lock()
		if m.ro.runs[r.ID] == rr {
			delete(m.ro.runs, r.ID)
		}
		m.mu.Unlock()
	}()
	m.mu.Lock()
	rolling := r.State == RolloutRollingBack
	m.mu.Unlock()
	if rolling {
		m.rollBackAll(r)
		return
	}
	for {
		m.mu.Lock()
		switch {
		case rr.abort:
			m.endLocked(r, RolloutAborted, "aborted by a person; what was applied stays")
			m.finishLocked(r)
			return
		case rr.pause:
			r.State, r.Reason, r.NextAt = RolloutPaused, "paused by a person after its batch", time.Time{}
			r.count()
			m.mu.Unlock()
			m.saveRollouts()
			return
		case r.Batch >= len(r.Batches):
			m.endLocked(r, RolloutDone, "every batch passed its gate")
			m.finishLocked(r)
			return
		}
		b := r.Batch
		m.mu.Unlock()
		res := m.runBatch(r, rr, b)
		switch {
		case res.stopped:
			m.pauseForStop(r)
			return
		case res.aborted:
			m.mu.Lock()
			r.Batches[b].State, r.Batches[b].Ended = BatchAborted, m.now().UTC()
			m.endLocked(r, RolloutAborted, "aborted by a person during the gate of batch "+strconv.Itoa(b+1)+"; what was applied stays")
			m.finishLocked(r)
			return
		case !res.passed:
			m.failRollout(r, b, res.why)
			return
		}
		m.afterBatch(r, b)
		m.mu.Lock()
		bt := &r.Batches[b]
		bt.State, bt.Ended = BatchPassed, m.now().UTC()
		r.Batch = b + 1
		more := r.Batch < len(r.Batches)
		r.count()
		var pause time.Duration
		switch {
		case more && bt.Canary && !rr.abort:
			r.State, r.Reason = RolloutWaiting, "the canary passed its gate: go on to apply the rest"
			r.count()
			m.mu.Unlock()
			m.saveRollouts()
			m.logf("cluster: the rollout %s's canary passed its gate; it waits for a person to go on", r.ID)
			return
		case more && r.Strategy.PauseMs > 0 && !rr.abort && !rr.pause:
			pause = time.Duration(r.Strategy.PauseMs) * time.Millisecond
			r.NextAt = m.now().Add(pause).UTC()
		}
		m.mu.Unlock()
		m.saveRollouts()
		if pause > 0 && !m.waitPause(rr, pause) {
			m.pauseForStop(r)
			return
		}
		m.mu.Lock()
		r.NextAt = time.Time{}
		m.mu.Unlock()
	}
}

// waitPause waits d between batches, or until a person pauses or aborts;
// false when the manager stops.
func (m *Manager) waitPause(rr *rolloutRun, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			return true
		case <-m.stop:
			return false
		case <-rr.wake:
			m.mu.Lock()
			told := rr.abort || rr.pause
			m.mu.Unlock()
			if told {
				return true
			}
		}
	}
}

// pauseForStop pauses r as the manager stops, where it is.
func (m *Manager) pauseForStop(r *Rollout) {
	m.mu.Lock()
	r.State, r.Reason, r.NextAt = RolloutPaused, "the manager stopped while it ran: it was paused where it stopped; resume it", time.Time{}
	r.count()
	m.mu.Unlock()
	m.saveRollouts()
}

// finishLocked writes r, ended, with its audit entry; m.mu is held, and
// let go.
func (m *Manager) finishLocked(r *Rollout) {
	entry := rolloutAudit(r, m.now().UTC())
	state, why, counts := r.State, r.Reason, r.Counts
	m.mu.Unlock()
	m.saveRollouts(entry)
	m.logf("cluster: the rollout %s ended %s (%s): %d done, %d failed, %d skipped, %d rolled back", r.ID, state, why,
		counts[MemberDone], counts[MemberFailed], counts[MemberSkipped], counts[MemberRolledBack])
}

// runBatch applies the batch b of r and checks its gate.
func (m *Manager) runBatch(r *Rollout, rr *rolloutRun, b int) batchResult {
	m.mu.Lock()
	bt := &r.Batches[b]
	bt.State, bt.Started, bt.GateAt, bt.Ended = BatchApplying, m.now().UTC(), time.Time{}, time.Time{}
	s := *r.Strategy
	var apply, gate []RolloutMember
	for _, n := range bt.Nodes {
		x := r.member(n)
		switch x.Status {
		case MemberPending, MemberApplying:
			apply = append(apply, *x)
		case MemberApplied:
			gate = append(gate, *x) // applied before the manager restarted
		}
	}
	m.mu.Unlock()
	m.saveRollouts()

	// A share of 5xx is gated only on members that count requests: those
	// that do not fail the batch before anything is applied.
	if s.Gate.Max5xxRatio > 0 {
		bad := map[string]string{}
		var badMu sync.Mutex
		m.each(apply, func(x RolloutMember) {
			var st struct {
				Requests *requestCounts `json:"requests"`
			}
			why := "it counts no requests (set metrics in its config), so its share of 5xx cannot be gated"
			switch p := m.getSync(x.Node, "/status", &st); {
			case p != nil:
				why = "its GET /status could not be read: " + p.Message
			case st.Requests != nil:
				return
			}
			badMu.Lock()
			bad[x.Node] = why
			badMu.Unlock()
		})
		if m.stopping() {
			return batchResult{stopped: true}
		}
		if len(bad) > 0 {
			names := slices.Sorted(maps.Keys(bad))
			for _, n := range names {
				m.setMember(r, n, func(x *RolloutMember) {
					x.Status, x.Error = MemberFailed, &Problem{Kind: KindUnsupported, Message: clip(bad[n], maxMessage), At: m.now().UTC()}
				})
			}
			return batchResult{why: "the share of 5xx cannot be gated on " + strings.Join(names, ", ") + ": nothing of the batch was applied"}
		}
	}

	m.each(apply, func(x RolloutMember) {
		m.mu.Lock()
		abort := rr.abort
		m.mu.Unlock()
		if abort {
			return // left pending
		}
		m.applyMember(r, x)
	})
	if m.stopping() {
		return batchResult{stopped: true}
	}

	m.mu.Lock()
	var failed []string
	gate = gate[:0]
	for _, n := range bt.Nodes {
		x := r.member(n)
		switch x.Status {
		case MemberApplied:
			gate = append(gate, *x)
		case MemberFailed:
			failed = append(failed, n+" ("+x.Error.Message+")") // a failure always says why
		}
	}
	abort := rr.abort
	m.mu.Unlock()
	switch {
	case len(failed) > 0:
		return batchResult{why: "batch " + strconv.Itoa(b+1) + " failed: " + clip(strings.Join(failed, "; "), 768)}
	case abort:
		return batchResult{aborted: true}
	case len(gate) == 0:
		return batchResult{passed: true}
	}

	// The gate: WaitMs after the batch, or its window when longer, each
	// member's requests counted from now.
	wait := time.Duration(s.Gate.WaitMs) * time.Millisecond
	if w := time.Duration(s.Gate.WindowMs) * time.Millisecond; s.Gate.Max5xxRatio > 0 && w > wait {
		wait = w
	}
	m.mu.Lock()
	bt.State, bt.GateAt = BatchGating, m.now().Add(wait).UTC()
	m.mu.Unlock()
	m.saveRollouts()
	base := map[string]*requestCounts{}
	var baseMu sync.Mutex
	if s.Gate.Max5xxRatio > 0 {
		m.each(gate, func(x RolloutMember) {
			var st struct {
				Requests *requestCounts `json:"requests"`
			}
			if p := m.getSync(x.Node, "/status", &st); p == nil && st.Requests != nil {
				baseMu.Lock()
				base[x.Node] = st.Requests
				baseMu.Unlock()
			}
		})
	}
	if wait > 0 {
		t := time.NewTimer(wait)
		cut := false
		for !cut {
			select {
			case <-t.C:
				cut = true
			case <-m.stop:
				t.Stop()
				return batchResult{stopped: true}
			case <-rr.wake:
				m.mu.Lock()
				abort = rr.abort
				m.mu.Unlock()
				if abort {
					t.Stop()
					return batchResult{aborted: true}
				}
			}
		}
	}
	gates := map[string]GateResult{}
	var gateMu sync.Mutex
	m.each(gate, func(x RolloutMember) {
		baseMu.Lock()
		before := base[x.Node]
		baseMu.Unlock()
		g := m.gateMember(&x, &s.Gate, before)
		gateMu.Lock()
		gates[x.Node] = g
		gateMu.Unlock()
	})
	if m.stopping() {
		return batchResult{stopped: true} // what failed then is the stop's
	}
	var fails []string
	for _, x := range gate {
		g := gates[x.Node]
		m.setMember(r, x.Node, func(x *RolloutMember) {
			if x.Gate != nil {
				g.DriftBefore = x.Gate.DriftBefore
			}
			x.Gate = &g
			if !g.Passed {
				x.Status = MemberFailed
				x.Error = &Problem{Kind: KindRefused, Message: clip("its gate failed: "+strings.Join(g.Reasons, "; "), maxMessage), At: g.Checked}
			}
		})
		if !g.Passed {
			fails = append(fails, x.Node+" ("+strings.Join(g.Reasons, "; ")+")")
		}
	}
	if len(fails) > 0 {
		slices.Sort(fails)
		return batchResult{why: "the gate of batch " + strconv.Itoa(b+1) + " failed: " + clip(strings.Join(fails, "; "), 768)}
	}
	m.mu.Lock()
	for _, x := range gate {
		if y := r.member(x.Node); y.Status == MemberApplied {
			y.Status = MemberDone
		}
	}
	r.count()
	m.mu.Unlock()
	return batchResult{passed: true}
}

// each runs fn on each of xs, at most max_concurrency at once, and returns
// once every one has.
func (m *Manager) each(xs []RolloutMember, fn func(RolloutMember)) {
	sem := make(chan struct{}, m.o.Settings.Concurrency())
	var wg sync.WaitGroup
	for _, x := range xs {
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			fn(x)
		}()
	}
	wg.Wait()
}

// nodeConfig reads the member's GET /node for the config it runs.
func (m *Manager) nodeConfig(name string) (*nodeAnswer, *Problem) {
	var na nodeAnswer
	if p := m.getSync(name, "/node", &na); p != nil {
		return nil, p
	}
	return &na, nil
}

// applyMember applies x's desired config to its member, with If-Match on
// the ETag the plan read: applied, skipped when its config changed since,
// or failed.
func (m *Manager) applyMember(r *Rollout, x RolloutMember) {
	m.setMember(r, x.Node, func(x *RolloutMember) { x.Status = MemberApplying })
	var before *int
	if caps, p := m.capabilitiesSync(x.Node); p == nil && slices.Contains(caps, capCheck) {
		var ck checkAnswer
		if p := m.getSync(x.Node, "/config/check", &ck); p == nil {
			d := ck.Summary.Drift
			before = &d
		}
	}
	h := stdhttp.Header{"If-Match": {x.ConfigETag}}
	rep, prob := m.callSync(x.Node, "PUT", "/config", h, x.Config)
	if m.stopping() {
		return // as it is: a restart finds out by its ETag
	}
	set := func(fn func(y *RolloutMember)) {
		m.setMember(r, x.Node, func(y *RolloutMember) {
			if before != nil {
				y.Gate = &GateResult{DriftBefore: before}
			}
			fn(y)
		})
	}
	var a struct {
		Version  uint64   `json:"version"`
		Warnings []string `json:"warnings"`
	}
	switch {
	case prob == nil && rep.Status >= 200 && rep.Status <= 299 && json.Unmarshal(rep.Body, &a) == nil && a.Version != 0:
		set(func(y *RolloutMember) {
			y.Status, y.Applied, y.Error = MemberApplied, a.Version, nil
			y.Warnings = append(y.Warnings, clipList(a.Warnings, MaxRolloutErrors, 256)...)
		})
	case prob == nil && rep.Status != stdhttp.StatusPreconditionFailed && (rep.Status < 200 || rep.Status > 299):
		// Refused: the running config stays.
		set(func(y *RolloutMember) {
			y.Status, y.Error, y.Errors = MemberFailed, replyProblem(rep, "its apply"), configErrors(rep.Body)
		})
	default:
		// The config changed since the plan (412), or the answer was lost:
		// the member's ETag says whether it runs the rollout's config.
		na, p := m.nodeConfig(x.Node)
		switch {
		case p != nil:
			why := "its apply was not answered"
			if prob != nil {
				why += " (" + prob.Message + ")"
			}
			set(func(y *RolloutMember) {
				y.Status, y.Unsure = MemberFailed, true
				y.Error = &Problem{Kind: KindUnreachable, Message: clip(why+", and its GET /node could not be read ("+p.Message+"): whether it runs the rollout's config is not known", maxMessage), At: m.now().UTC()}
			})
		case na.ConfigETag == x.NewETag:
			set(func(y *RolloutMember) { y.Status, y.Applied, y.Error = MemberApplied, na.ConfigVersion, nil })
		case prob == nil && rep.Status == stdhttp.StatusPreconditionFailed:
			set(func(y *RolloutMember) {
				y.Status = MemberSkipped
				y.Error = &Problem{Kind: KindRefused, Status: rep.Status, At: m.now().UTC(), Message: clip(fmt.Sprintf(
					"its config changed since the plan (it runs version %d, ETag %s; the plan read %s): it was not overwritten", na.ConfigVersion, na.ConfigETag, x.ConfigETag), maxMessage)}
			})
		default:
			p := prob
			if p == nil {
				p = &Problem{Kind: KindBadAnswer, Status: rep.Status, Message: "its apply's answer is not the JSON expected", At: m.now().UTC()}
			}
			set(func(y *RolloutMember) { y.Status, y.Error = MemberFailed, p })
		}
	}
	m.mu.Lock()
	if n := m.nodes[x.Node]; n != nil {
		n.next.Store(0) // the poller reads what changed
	}
	m.mu.Unlock()
	m.poke()
}

// gateMember checks the gate g on x's member: reachable, running the config
// the rollout applied, no new differences in its consistency check, and its
// share of 5xx since before at most g's, when gated.
func (m *Manager) gateMember(x *RolloutMember, g *Gate, before *requestCounts) (res GateResult) {
	res.Checked = m.now().UTC()
	fail := func(format string, args ...any) {
		if len(res.Reasons) < maxGateReasons {
			res.Reasons = append(res.Reasons, clip(fmt.Sprintf(format, args...), 256))
		}
	}
	defer func() { res.Passed = len(res.Reasons) == 0 }()
	na, p := m.nodeConfig(x.Node)
	if p != nil {
		fail("it is not reachable: %s", p.Message)
		return res
	}
	res.Version = na.ConfigVersion
	if na.ConfigETag != x.NewETag {
		fail("it runs another config now (version %d, ETag %s), not the one the rollout applied", na.ConfigVersion, na.ConfigETag)
	}
	if x.Gate != nil && x.Gate.DriftBefore != nil {
		var ck checkAnswer
		if p := m.getSync(x.Node, "/config/check", &ck); p != nil {
			fail("its consistency check could not be read: %s", p.Message)
		} else {
			d := ck.Summary.Drift
			res.DriftAfter = &d
			if d > *x.Gate.DriftBefore {
				fail("its consistency check finds %d differences, %d before the rollout", d, *x.Gate.DriftBefore)
			}
		}
	}
	if g.Max5xxRatio <= 0 {
		return res
	}
	var st struct {
		Requests *requestCounts `json:"requests"`
	}
	switch p := m.getSync(x.Node, "/status", &st); {
	case p != nil:
		fail("its GET /status could not be read: %s", p.Message)
	case st.Requests == nil:
		fail("it counts no requests now (set metrics in its config)")
	case before == nil:
		fail("its requests could not be counted when the batch was applied")
	case st.Requests.Total < before.Total || st.Requests.Classes["5xx"] < before.Classes["5xx"]:
		fail("its counts of requests went back: it restarted in the window")
	default:
		total, errs := st.Requests.Total-before.Total, st.Requests.Classes["5xx"]-before.Classes["5xx"]
		res.Requests, res.Errors5xx = &total, &errs
		if total < uint64(g.MinRequests) {
			res.Notes = append(res.Notes, fmt.Sprintf("%d requests in the window, fewer than %d: its share of 5xx is not judged", total, g.MinRequests))
			return res
		}
		ratio := float64(errs) / float64(total)
		res.Ratio = &ratio
		if ratio > g.Max5xxRatio {
			fail("%d of its %d requests in the window were 5xx (%.1f%%), more than %.1f%%", errs, total, ratio*100, g.Max5xxRatio*100)
		}
	}
	return res
}

// afterBatch has the members of r's batch b that passed read again: the
// poller at once, and their sync states, which tells each what the manager
// owns there in sync now.
func (m *Manager) afterBatch(r *Rollout, b int) {
	m.mu.Lock()
	var names []string
	for _, n := range r.Batches[b].Nodes {
		if x := r.member(n); x.Status == MemberDone {
			names = append(names, n)
		}
	}
	m.mu.Unlock()
	if len(names) > 0 {
		m.runSync(names, true, nil)
	}
}

// failRollout stops r, whose batch b failed for why, and rolls back the
// batch, or every member applied when its strategy says so.
func (m *Manager) failRollout(r *Rollout, b int, why string) {
	m.mu.Lock()
	bt := &r.Batches[b]
	bt.State, bt.Ended = BatchFailed, m.now().UTC()
	all := r.Strategy.OnFailure == OnFailureAll
	var back []RolloutMember
	for _, x := range r.Members {
		if toRollBack(&x) && (all || slices.Contains(bt.Nodes, x.Node)) {
			back = append(back, x)
		}
	}
	m.mu.Unlock()
	m.saveRollouts()
	failed := m.rollBack(r, back)
	m.mu.Lock()
	for i := range r.Batches {
		c := &r.Batches[i]
		if i == b || all && c.State == BatchPassed {
			if !slices.ContainsFunc(c.Nodes, func(n string) bool { return r.member(n).Status == MemberRollbackFailed }) && len(back) > 0 {
				c.State = BatchRolledBack
			}
		}
	}
	scope := "its batch"
	if all {
		scope = "every member applied"
	}
	switch {
	case len(back) == 0:
		why += "; nothing was applied to roll back"
	case failed > 0:
		why += fmt.Sprintf("; %s rolled back, but %d members could not be", scope, failed)
	default:
		why += "; " + scope + " rolled back"
	}
	m.endLocked(r, RolloutFailed, why)
	m.finishLocked(r)
}

// rollBackAll rolls back every member of r applied, as a person asked.
func (m *Manager) rollBackAll(r *Rollout) {
	m.mu.Lock()
	var back []RolloutMember
	for _, x := range r.Members {
		if toRollBack(&x) {
			back = append(back, x)
		}
	}
	m.mu.Unlock()
	failed := m.rollBack(r, back)
	if m.stopping() {
		m.mu.Lock()
		r.State, r.Reason, r.Ended = RolloutFailed, "the manager stopped while it rolled back: roll it back again", m.now().UTC()
		m.mu.Unlock()
		m.saveRollouts()
		return
	}
	m.mu.Lock()
	for i := range r.Batches {
		c := &r.Batches[i]
		if c.State == BatchPassed || c.State == BatchFailed {
			c.State = BatchRolledBack
		}
	}
	why := r.Reason
	if failed > 0 {
		why += fmt.Sprintf("; %d members could not be rolled back", failed)
	}
	m.endLocked(r, RolloutRolledBack, why)
	m.finishLocked(r)
}

// rollBack rolls each of xs back to the version it ran at the plan, by its
// POST /rollback/{n} with If-Match on the ETag of the rollout's config, and
// returns how many could not be.
func (m *Manager) rollBack(r *Rollout, xs []RolloutMember) int {
	var mu sync.Mutex
	failed := 0
	m.each(xs, func(x RolloutMember) {
		ok := m.rollBackMember(r, x)
		if !ok && !m.stopping() {
			mu.Lock()
			failed++
			mu.Unlock()
		}
	})
	return failed
}

// rollBackMember rolls x's member back; false when it could not.
func (m *Manager) rollBackMember(r *Rollout, x RolloutMember) bool {
	h := stdhttp.Header{"If-Match": {x.NewETag}}
	rep, prob := m.callSync(x.Node, "POST", "/rollback/"+strconv.FormatUint(x.Version, 10), h, nil)
	if m.stopping() {
		return false
	}
	set := func(fn func(y *RolloutMember)) { m.setMember(r, x.Node, fn) }
	defer func() {
		m.mu.Lock()
		if n := m.nodes[x.Node]; n != nil {
			n.next.Store(0)
		}
		m.mu.Unlock()
		m.poke()
	}()
	var a struct {
		Version uint64 `json:"version"`
	}
	switch {
	case prob != nil:
		set(func(y *RolloutMember) { y.Status, y.Error = MemberRollbackFailed, prob })
		return false
	case rep.Status >= 200 && rep.Status <= 299 && json.Unmarshal(rep.Body, &a) == nil:
		set(func(y *RolloutMember) {
			y.Status, y.RolledBack, y.Unsure = MemberRolledBack, a.Version, false
		})
		return true
	case rep.Status == stdhttp.StatusPreconditionFailed:
		na, p := m.nodeConfig(x.Node)
		if p == nil && x.Unsure && na.ConfigETag == x.ConfigETag {
			// It never ran the rollout's config: nothing to roll back.
			set(func(y *RolloutMember) { y.Unsure = false })
			return true
		}
		set(func(y *RolloutMember) {
			y.Status = MemberRollbackFailed
			y.Error = &Problem{Kind: KindRefused, Status: rep.Status, At: m.now().UTC(),
				Message: "its config changed since the rollout applied it: it was not rolled back"}
		})
		return false
	case rep.Status == stdhttp.StatusNotFound:
		set(func(y *RolloutMember) {
			y.Status = MemberRollbackFailed
			y.Error = &Problem{Kind: KindNoEarlier, Status: rep.Status, At: m.now().UTC(),
				Message: "it keeps no version " + strconv.FormatUint(x.Version, 10) + " any more to roll back to"}
		})
		return false
	default:
		set(func(y *RolloutMember) { y.Status, y.Error = MemberRollbackFailed, replyProblem(rep, "its rollback") })
		return false
	}
}

// waitRollouts waits for the rollouts' goroutines to end, at most wait.
func (m *Manager) waitRollouts(wait time.Duration) {
	ended := make(chan struct{})
	go func() { m.ro.wg.Wait(); close(ended) }()
	select {
	case <-ended:
	case <-time.After(wait):
	}
}
