package cluster

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ---- Strategies ----

// check checks s, filling in its defaults, for a rollout of n members.
func (s *Strategy) check(n int) error {
	switch s.Mode {
	case StrategyAll:
		if s.BatchSize != 0 || s.BatchPercent != 0 || s.Canary != 0 || s.PauseMs != 0 {
			return fieldErr("strategy.mode", "all at once takes no batches, canary or pause")
		}
	case StrategyRolling, StrategyCanary:
		switch {
		case s.BatchSize < 0 || s.BatchSize > n:
			return fieldErr("strategy.batch_size", "a batch is 1 to %d members", n)
		case s.BatchPercent < 0 || s.BatchPercent > 100:
			return fieldErr("strategy.batch_percent", "a batch is 1 to 100 percent of the members")
		case s.BatchSize > 0 && s.BatchPercent > 0:
			return fieldErr("strategy.batch_size", "a batch is given by its size or its percentage, not both")
		case s.Mode == StrategyRolling && s.BatchSize == 0 && s.BatchPercent == 0:
			return fieldErr("strategy.batch_size", "a rolling rollout's batches are given by their size or percentage")
		case s.Mode == StrategyRolling && s.Canary != 0:
			return fieldErr("strategy.canary", "only a canary rollout has a canary")
		case s.Mode == StrategyCanary && (s.Canary < 1 || s.Canary >= n):
			if n < 2 {
				return fieldErr("strategy.mode", "a canary needs two members at least: one to go first, and the rest")
			}
			return fieldErr("strategy.canary", "the canary is 1 to %d members, fewer than all %d", n-1, n)
		case s.PauseMs < 0 || s.PauseMs > maxPauseMs:
			return fieldErr("strategy.pause_ms", "the pause between batches is 0 to %d ms", maxPauseMs)
		}
	default:
		return fieldErr("strategy.mode", "%q is not a strategy: %s, %s or %s", s.Mode, StrategyAll, StrategyRolling, StrategyCanary)
	}
	switch s.OnFailure {
	case "":
		s.OnFailure = defaultOnFailureMode
	case OnFailureBatch, OnFailureAll:
	default:
		return fieldErr("strategy.on_failure", "%q: a failed gate rolls back its batch, or all", s.OnFailure)
	}
	g := &s.Gate
	switch {
	case g.WaitMs < 0 || g.WaitMs > maxGateWaitMs:
		return fieldErr("strategy.gate.wait_ms", "the gate's wait is 0 to %d ms", maxGateWaitMs)
	case math.IsNaN(g.Max5xxRatio) || g.Max5xxRatio < 0 || g.Max5xxRatio > 1:
		return fieldErr("strategy.gate.max_5xx_ratio", "the share of 5xx is from 0 (not gated) to 1")
	case g.WindowMs < 0 || g.WindowMs > maxGateWindowMs:
		return fieldErr("strategy.gate.window_ms", "the window is 0 to %d ms", maxGateWindowMs)
	case g.MinRequests < 0 || g.MinRequests > maxGateMinRequests:
		return fieldErr("strategy.gate.min_requests", "min_requests is 0 to %d", maxGateMinRequests)
	case g.Max5xxRatio == 0 && (g.WindowMs != 0 || g.MinRequests != 0):
		return fieldErr("strategy.gate.max_5xx_ratio", "a window and min_requests go with a share of 5xx to gate")
	}
	if g.Max5xxRatio > 0 {
		if g.WindowMs == 0 {
			g.WindowMs = DefaultGateWindowMs
		}
		if g.MinRequests == 0 {
			g.MinRequests = DefaultMinRequests
		}
	}
	return nil
}

// batches splits names into s's batches.
func (s *Strategy) batches(names []string) []Batch {
	var out []Batch
	add := func(nodes []string, canary bool) {
		out = append(out, Batch{Index: len(out), Nodes: slices.Clone(nodes), Canary: canary, State: BatchPending})
	}
	rest := names
	if s.Mode == StrategyAll {
		add(rest, false)
		return out
	}
	if s.Mode == StrategyCanary {
		add(rest[:s.Canary], true)
		rest = rest[s.Canary:]
	}
	size := s.BatchSize
	if s.BatchPercent > 0 {
		size = (len(rest)*s.BatchPercent + 99) / 100
	}
	if size <= 0 {
		size = len(rest)
	}
	for len(rest) > 0 {
		k := min(size, len(rest))
		add(rest[:k], false)
		rest = rest[k:]
	}
	return out
}

// describe says what s does, for the audit log.
func (s *Strategy) describe() string {
	var b strings.Builder
	switch s.Mode {
	case StrategyAll:
		b.WriteString("all at once")
	case StrategyCanary:
		fmt.Fprintf(&b, "a canary of %d, then", s.Canary)
		fallthrough
	default:
		switch {
		case s.BatchSize > 0:
			fmt.Fprintf(&b, " batches of %d", s.BatchSize)
		case s.BatchPercent > 0:
			fmt.Fprintf(&b, " batches of %d%%", s.BatchPercent)
		default:
			b.WriteString(" the rest at once")
		}
		if s.PauseMs > 0 {
			fmt.Fprintf(&b, ", %s apart", time.Duration(s.PauseMs)*time.Millisecond)
		}
	}
	fmt.Fprintf(&b, "; gate after %s", time.Duration(s.Gate.WaitMs)*time.Millisecond)
	if s.Gate.Max5xxRatio > 0 {
		fmt.Fprintf(&b, ", 5xx at most %g over %s (from %d requests)", s.Gate.Max5xxRatio, time.Duration(s.Gate.WindowMs)*time.Millisecond, s.Gate.MinRequests)
	}
	b.WriteString("; a failed gate rolls back " + map[string]string{OnFailureBatch: "its batch", OnFailureAll: "all"}[s.OnFailure])
	return strings.TrimSpace(b.String())
}

// ---- Starting and steering ----

// startRollout is StartRollout (rolldraft.go), once a draft is saved.
func (m *Manager) startRollout(user, id string, req *StartRequest) (Rollout, error) {
	m.mu.Lock()
	r, err := m.steerableLocked(id)
	if err != nil {
		m.mu.Unlock()
		return Rollout{}, err
	}
	if err := m.allowRolloutLocked(user, r.Kind, r.From, r.nodes()); err != nil {
		m.mu.Unlock()
		return Rollout{}, err
	}
	m.expireLocked()
	if r.State != RolloutPlanned {
		m.mu.Unlock()
		return Rollout{}, errConflict("the rollout is " + r.State + ": only a planned rollout starts")
	}
	for _, n := range req.Exclude {
		if r.member(n) == nil {
			m.mu.Unlock()
			return Rollout{}, fieldErr("exclude", "%q is not a member of the rollout", n)
		}
	}
	var names, busy []string
	for _, x := range r.Members {
		if x.Status != MemberReady || slices.Contains(req.Exclude, x.Node) {
			continue
		}
		if id := m.ops.busy[x.Node]; id != "" {
			busy = append(busy, x.Node+" ("+id+")")
		}
		names = append(names, x.Node)
	}
	if len(names) == 0 {
		m.mu.Unlock()
		return Rollout{}, errConflict("no member of the rollout is ready to be applied")
	}
	s := req.Strategy
	if err := s.check(len(names)); err != nil {
		m.mu.Unlock()
		return Rollout{}, err
	}
	if len(busy) > 0 {
		m.mu.Unlock()
		return Rollout{}, errConflict("a rollout or an operation runs on " + strings.Join(busy, ", ") + ": wait for it to end")
	}
	for i := range r.Members {
		x := &r.Members[i]
		switch {
		case x.Status == MemberReady && slices.Contains(req.Exclude, x.Node):
			x.Status, x.Config = MemberExcluded, nil
		case x.Status == MemberReady:
			x.Status = MemberPending
		}
	}
	r.Strategy, r.Batches, r.Batch = &s, s.batches(names), 0
	r.State, r.StartedBy, r.Started, r.Reason = RolloutRunning, user, m.now().UTC(), ""
	r.count()
	m.holdLocked(r)
	rr := m.runLocked(r)
	out := r.copy(true, false)
	entry := AuditEntry{At: m.now().UTC(), User: user, Action: ActionRolloutStart, Rollout: r.ID, Template: r.Template, Group: r.Group,
		Detail: clip(fmt.Sprintf("%d members in %d batches: %s", len(names), len(r.Batches), s.describe()), 512), Nodes: names, Outcome: AuditOK}
	entry.setDiffText(rolloutDiff(r, names))
	for _, x := range r.Members {
		if slices.Contains(names, x.Node) {
			entry.Outcomes = append(entry.Outcomes, AuditOutcome{Node: x.Node, Status: x.Status, Before: x.Version, Digest: x.Digest})
		}
	}
	m.mu.Unlock()
	m.saveRollouts(entry)
	m.logf("cluster: %q started the rollout %s on %s: %s", user, r.ID, strings.Join(names, ", "), s.describe())
	go m.runRollout(r, rr)
	return out, nil
}

// steerableLocked returns the rollout id, unless the manager cannot steer
// it. m.mu is held.
func (m *Manager) steerableLocked(id string) (*Rollout, error) {
	switch {
	case m.closed:
		return nil, errClosed
	case m.readOnly != "":
		return nil, errReadOnly(m.readOnly)
	}
	r := m.rolloutLocked(id)
	if r == nil {
		return nil, errNoRollout(id)
	}
	return r, nil
}

// runLocked registers r's goroutine about to start. m.mu is held.
func (m *Manager) runLocked(r *Rollout) *rolloutRun {
	if m.ro.runs == nil {
		m.ro.runs = map[string]*rolloutRun{}
	}
	rr := &rolloutRun{wake: make(chan struct{}, 1)}
	m.ro.runs[r.ID] = rr
	m.ro.wg.Add(1)
	return rr
}

// The steering of a rollout, SteerRollout's what.
const (
	SteerPause    = "pause"
	SteerResume   = "resume"
	SteerGo       = "go"
	SteerAbort    = "abort"
	SteerRollback = "rollback"
	SteerDiscard  = "discard"
)

// SteerRollout pauses, resumes, aborts, rolls back, goes on with (after
// its canary) or discards (a plan) the rollout id, as user. It writes the
// store: off a fib worker.
func (m *Manager) SteerRollout(user, id, what string) (Rollout, error) {
	m.mu.Lock()
	r, err := m.steerableLocked(id)
	if err == nil {
		err = m.allowRolloutLocked(user, r.Kind, r.From, r.nodes())
	}
	if err != nil {
		m.mu.Unlock()
		return Rollout{}, err
	}
	m.expireLocked()
	rr := m.ro.runs[id]
	refuse := func(why string) (Rollout, error) {
		m.mu.Unlock()
		return Rollout{}, errConflict("the rollout is " + r.State + ": " + why)
	}
	var start *rolloutRun
	detail := ""
	switch what {
	case SteerPause:
		if r.State != RolloutRunning || rr == nil {
			return refuse("only a running rollout pauses")
		}
		rr.pause = true
		wake(rr)
		detail = "pauses after the batch under way"
	case SteerResume, SteerGo:
		switch {
		case what == SteerResume && r.State == RolloutRunning && rr != nil && rr.pause:
			rr.pause = false // the pause asked for is taken back
		case what == SteerResume && r.State != RolloutPaused, what == SteerGo && r.State != RolloutWaiting:
			return refuse(map[string]string{SteerResume: "only a paused rollout resumes", SteerGo: "only a rollout whose canary waits goes on"}[what])
		default:
			r.State, r.Reason = RolloutRunning, ""
			start = m.runLocked(r)
		}
	case SteerAbort:
		switch {
		case r.State == RolloutRunning && rr != nil:
			rr.abort = true
			wake(rr)
			detail = "stops after the members being applied"
		case r.State == RolloutPaused || r.State == RolloutWaiting:
			m.endLocked(r, RolloutAborted, "aborted by "+user+"; what was applied stays")
		default:
			return refuse("only a running, paused or waiting rollout is aborted")
		}
	case SteerRollback:
		switch r.State {
		case RolloutPaused, RolloutWaiting, RolloutDone, RolloutFailed, RolloutAborted:
		default:
			return refuse("a rollout is rolled back once it is paused, waiting or ended")
		}
		if !slices.ContainsFunc(r.Members, func(x RolloutMember) bool { return toRollBack(&x) }) {
			return refuse("none of its members has a config of the rollout's to roll back")
		}
		var busy []string
		for _, x := range r.Members {
			if id := m.ops.busy[x.Node]; toRollBack(&x) && id != "" && id != "rollout "+r.ID {
				busy = append(busy, x.Node+" ("+id+")")
			}
		}
		if len(busy) > 0 {
			m.mu.Unlock()
			return Rollout{}, errConflict("a rollout or an operation runs on " + strings.Join(busy, ", ") + ": wait for it to end")
		}
		r.State, r.Reason, r.Ended = RolloutRollingBack, "rolled back by "+user, time.Time{}
		m.holdLocked(r)
		start = m.runLocked(r)
	case SteerDiscard:
		if r.State != RolloutPlanned && r.State != RolloutExpired {
			return refuse("only a plan is discarded")
		}
		i := slices.Index(m.ro.list, r)
		m.ro.list = slices.Delete(m.ro.list, i, i+1)
		r.State = "discarded"
	default:
		m.mu.Unlock()
		return Rollout{}, fieldErr("action", "%q: a rollout is paused, resumed, gone on with, aborted, rolled back or discarded", what)
	}
	out := r.copy(true, false)
	entries := []AuditEntry{{At: m.now().UTC(), User: user, Action: "rollout." + what, Rollout: r.ID, Template: r.Template, Group: r.Group, Detail: detail,
		Nodes: r.nodes(), Outcome: AuditOK}}
	if what == SteerAbort && r.State == RolloutAborted {
		entries = append(entries, rolloutAudit(r, m.now().UTC()))
	}
	m.mu.Unlock()
	m.saveRollouts(entries...)
	m.logf("cluster: %q asked the rollout %s to %s", user, id, what)
	if start != nil {
		go m.runRollout(r, start)
	}
	return out, nil
}

// wake has a rollout's goroutine look at what it is told.
func wake(rr *rolloutRun) {
	select {
	case rr.wake <- struct{}{}:
	default:
	}
}

// toRollBack reports whether x has a config of the rollout's applied, which
// a rollback takes back.
func toRollBack(x *RolloutMember) bool {
	switch x.Status {
	case MemberApplied, MemberDone, MemberFailed, MemberRollbackFailed:
		return x.Applied != 0 || x.Unsure
	}
	return false
}

// endLocked ends r in state, for why: its members are let go, and what it
// keeps of their configs and diffs dropped. m.mu is held.
func (m *Manager) endLocked(r *Rollout, state, why string) {
	r.State, r.Reason, r.Ended, r.NextAt = state, why, m.now().UTC(), time.Time{}
	r.shed()
	r.count()
	m.releaseLocked(r)
	m.expireLocked()
}

// rolloutAudit is r's audit entry once it ends: each member's outcome and
// versions. m.mu is held.
func rolloutAudit(r *Rollout, at time.Time) AuditEntry {
	e := AuditEntry{At: at, User: r.StartedBy, Action: ActionRolloutEnd, Rollout: r.ID, Template: r.Template, Group: r.Group,
		Detail: clip(r.State+": "+r.Reason, 512), Outcome: r.State}
	for _, x := range r.Members {
		if x.Status == MemberUnchanged || x.Status == MemberExcluded || x.Status == MemberError {
			continue
		}
		a := AuditOutcome{Node: x.Node, Status: x.Status, Detail: memberDetail(&x), Before: x.Version, After: x.Applied, Digest: x.Digest}
		if x.RolledBack != 0 {
			a.After = x.RolledBack
		}
		if x.Error != nil {
			a.Error = clip(x.Error.Kind+": "+x.Error.Message, maxAuditError)
		}
		e.Outcomes = append(e.Outcomes, a)
	}
	return e
}

// memberDetail says what a rollout did on a member: its versions.
func memberDetail(x *RolloutMember) string {
	var parts []string
	if x.Version != 0 {
		parts = append(parts, "from version "+strconv.FormatUint(x.Version, 10))
	}
	if x.Applied != 0 {
		parts = append(parts, "applied as version "+strconv.FormatUint(x.Applied, 10))
	}
	if x.RolledBack != 0 {
		parts = append(parts, "rolled back as version "+strconv.FormatUint(x.RolledBack, 10))
	}
	if x.Digest != "" {
		parts = append(parts, "config "+shortDigest(x.Digest))
	}
	return strings.Join(parts, ", ")
}
