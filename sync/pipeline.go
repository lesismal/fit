package fitsync

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lesismal/fit/config"
)

// Point is where a crash hook may kill a pipeline.
type Point string

// The points a crash hook is asked at.
const (
	// CrashEmitted is after a record entered the queue.
	CrashEmitted Point = "emitted"
	// CrashDispatched is after records were handed to the sinks.
	CrashDispatched Point = "dispatched"
	// CrashWritten is after a sink wrote a batch, before it is acknowledged.
	CrashWritten Point = "written"
	// CrashAcknowledged is after a sink's batch was acknowledged.
	CrashAcknowledged Point = "acknowledged"
	// CrashCheckpointWritten is after the checkpoint's temporary file was
	// written and synced, before it is renamed into place.
	CrashCheckpointWritten Point = "checkpoint_written"
	// CrashCheckpointSaved is after the checkpoint was saved.
	CrashCheckpointSaved Point = "checkpoint_saved"
)

// entry is a source record in flight, from Emit until every sink has
// acknowledged every record it became.
type entry struct {
	// left is the acknowledgements still owed; -1 until it is dispatched.
	left     int
	size     int
	pos      Position
	boundary bool
	// failed is set once on_error took one of what it became.
	failed bool
	// commit is the record's commit time, for the lag Status shows.
	commit time.Time
}

type item struct {
	rec  *Record
	size int
}

// run is one run of a pipeline, from its checkpoint until it ends, fails, is
// stopped or killed. A failed run is followed by another.
type run struct {
	p                   *pipe
	ctx, srcCtx         context.Context
	cancel, cancelSrc   context.CancelFunc
	wake, space         chan struct{}
	killed              atomic.Bool
	saveMu, deadLetters sync.Mutex

	mu         sync.Mutex
	inRecords  int
	inBytes    int64
	queue      []*Record
	srcDone    bool
	srcErr     error
	drained    bool
	err        error
	base, next uint64
	// waiting counts the source's emits waiting for room in the queue,
	// which only the oldest record's writes free: the writer holding that
	// record writes it without waiting for its batch to fill.
	waiting int
	pending []entry
	from    Position
	mark    Position
	dirty   bool
	saved   bool
	// sinks are the sinks' writers, groups the writers of each sink: one,
	// or a job's max_parallel for a ParallelSink.
	sinks  []*sinkRun
	groups [][]*sinkRun
	// read counts the source records emitted; done and failed those past
	// the head of the queue, and markDone and markFailed as of the mark,
	// all from the job's progress on; startDone and started are done and
	// the time when the run began, for the ETA. finishing says the source
	// ended and everything was written, which the last checkpoint records.
	read, done, failed, markDone, markFailed, startDone int64
	started                                             time.Time
	finishing                                           bool
	// acked is the mark not yet told to an Acknowledger source.
	acked Position
}

type sinkRun struct {
	r     *run
	name  string
	cfg   *config.SyncSink
	sink  Sink
	m     *sinkMeter
	queue []item
	bytes int64
	first time.Time
	wake  chan struct{}
}

func notify(c chan struct{}) {
	select {
	case c <- struct{}{}:
	default:
	}
}

func newRun(p *pipe) *run {
	r := &run{p: p, wake: make(chan struct{}, 1), space: make(chan struct{}, 1), started: time.Now()}
	r.ctx, r.cancel = context.WithCancel(context.Background())
	r.srcCtx, r.cancelSrc = context.WithCancel(r.ctx)
	// A job reading a snapshot in chunks, which has no transactions and
	// each key once, writes with several writers where a sink takes them.
	snapshot := len(p.jobChunks()) > 0
	for i := range p.sinks {
		n := 1
		if _, ok := p.sinks[i].(ParallelSink); ok && snapshot {
			n = p.cfg.Parallel()
		}
		var g []*sinkRun
		for range n {
			s := &sinkRun{r: r, name: p.cfg.Sinks[i].Name, cfg: &p.cfg.Sinks[i], sink: p.sinks[i], m: p.sinkMeter(i), wake: make(chan struct{}, 1)}
			g = append(g, s)
			r.sinks = append(r.sinks, s)
		}
		r.groups = append(r.groups, g)
	}
	if prog := p.progress(nil, nil, false); prog != nil {
		r.done, r.failed = prog.Written+prog.Failed, prog.Failed
		r.read, r.markDone, r.markFailed, r.startDone = r.done, r.done, r.failed, r.done
	}
	return r
}

// crash asks the crash hook at pt, and kills the run when it says so.
func (r *run) crash(pt Point) bool {
	if h := r.p.rt.opts.Crash; h != nil && h(r.p.cfg.Name, pt) {
		r.kill()
		return true
	}
	return r.killed.Load()
}

// kill stops the run as kill -9 would: nothing more is saved.
func (r *run) kill() {
	r.killed.Store(true)
	r.cancel()
}

// fail stops the run for err, which on_error stop or a dead letter that
// could not be written gives.
func (r *run) fail(err error) {
	r.mu.Lock()
	if r.err == nil {
		r.err = err
	}
	r.mu.Unlock()
	r.cancel()
}

// emit is Run.Emit: it waits for room in the queue, then queues rec.
func (r *run) emit(rec *Record) error {
	size := rec.Size()
	limRecords, limBytes := r.p.cfg.Records(), r.p.cfg.Bytes()
	if !r.p.held(r.srcCtx) {
		return errStopped
	}
	r.mu.Lock()
	for r.inRecords > 0 && (r.inRecords+1 > limRecords || r.inBytes+int64(size) > limBytes) {
		r.waiting++
		r.mu.Unlock()
		for _, s := range r.sinks {
			notify(s.wake)
		}
		select {
		case <-r.space:
		case <-r.srcCtx.Done():
			r.mu.Lock()
			r.waiting--
			r.mu.Unlock()
			return errStopped
		}
		r.mu.Lock()
		r.waiting--
	}
	if r.srcCtx.Err() != nil {
		r.mu.Unlock()
		return errStopped
	}
	rec.seq, rec.made = r.next, false
	r.next++
	r.pending = append(r.pending, entry{left: -1, size: size, pos: rec.Position, boundary: rec.boundary(), commit: rec.CommitTime})
	r.p.meter.read.Add(1)
	r.p.meter.readBytes.Add(int64(size))
	r.read++
	r.inRecords++
	r.inBytes += int64(size)
	r.queue = append(r.queue, rec)
	r.mu.Unlock()
	notify(r.wake)
	if r.crash(CrashEmitted) {
		return errStopped
	}
	return nil
}

// advance drops the acknowledged records at the head of the queue, freeing
// their room, and moves the mark to the last of them that ends a
// transaction. r.mu is held.
func (r *run) advance() {
	freed, n, nFailed := false, int64(0), int64(0)
	var commit time.Time
	for len(r.pending) > 0 && r.pending[0].left == 0 {
		e := r.pending[0]
		r.pending[0] = entry{}
		r.pending = r.pending[1:]
		r.base++
		r.inRecords--
		r.inBytes -= int64(e.size)
		r.done++
		n++
		if e.failed {
			r.failed++
			nFailed++
		}
		if !e.commit.IsZero() {
			commit = e.commit
		}
		if e.boundary && e.pos != nil {
			r.mark, r.dirty = e.pos, true
			r.markDone, r.markFailed = r.done, r.failed
			r.acked = e.pos
		}
		freed = true
	}
	if freed {
		r.p.meter.passed(n, nFailed, commit)
		notify(r.space)
	}
}

// ack records that s wrote items.
func (r *run) ack(items []item) {
	r.mu.Lock()
	for _, it := range items {
		r.pending[it.rec.seq-r.base].left--
	}
	r.advance()
	r.mu.Unlock()
	r.tellAcked()
}

// tellAcked tells an Acknowledger source the mark advance last moved to,
// if it has not been told yet; r.mu is not held.
func (r *run) tellAcked() {
	a, ok := r.p.src.(Acknowledger)
	if !ok {
		return
	}
	r.mu.Lock()
	pos := r.acked
	r.acked = nil
	r.mu.Unlock()
	if pos != nil && !r.killed.Load() {
		a.Acknowledged(pos)
	}
}

// dispatch takes records from the queue, puts them through the transforms
// and hands what they become to every sink, until the source has ended and
// the queue is empty, or the run stops.
func (r *run) dispatch() {
	cfg := r.p.cfg
	c := newStages(r.p.transforms)
	var keyBuf []byte
	for {
		r.mu.Lock()
		for len(r.queue) == 0 && !r.srcDone {
			r.mu.Unlock()
			select {
			case <-r.wake:
			case <-r.ctx.Done():
				return
			}
			r.mu.Lock()
		}
		batch := r.queue
		r.queue = nil
		if len(batch) == 0 {
			r.drained = true
			r.mu.Unlock()
			for _, s := range r.sinks {
				notify(s.wake)
			}
			return
		}
		r.mu.Unlock()
		c.run(batch)
		k := 0
		for i, rec := range batch {
			start := k
			for k < len(c.outs) && c.outs[k].src == i {
				k++
			}
			outs := c.outs[start:k]
			if err := c.errs[i]; err != nil {
				t := c.where[i]
				r.p.noteErr(err)
				r.p.logErr("transform %d (%s) failed a record of %s: %v", t, cfg.Transforms[t].Module, rec.Namespace, err)
				if !r.failedOn(rec, err, fmt.Sprintf("transform %d (%s)", t, cfg.Transforms[t].Module), 1) {
					c.done()
					return
				}
				outs = nil
			}
			if len(outs) > 1 {
				// The records of one source record share its Origin.
				for j, o := range outs {
					o.rec.sub = uint32(j)
				}
			}
			r.mu.Lock()
			r.pending[rec.seq-r.base].left = len(outs) * len(r.groups)
			for _, g := range r.groups {
				for _, o := range outs {
					lane := rec.lane - 1
					if lane < 0 {
						lane = laneOf(o.rec, len(g), &keyBuf)
					}
					s := g[lane%len(g)]
					if len(s.queue) == 0 {
						s.first = time.Now()
					}
					n := o.rec.Size()
					s.queue = append(s.queue, item{o.rec, n})
					s.bytes += int64(n)
				}
			}
			r.advance()
			r.mu.Unlock()
		}
		c.done()
		r.tellAcked()
		for _, s := range r.sinks {
			notify(s.wake)
		}
		if r.crash(CrashDispatched) {
			return
		}
	}
}

// failedOn takes a record that failed at where, after attempts, as on_error
// says: it reports whether the run goes on past it.
func (r *run) failedOn(rec *Record, err error, where string, attempts int) bool {
	if r.p.cfg.ErrorPolicy() != config.SyncStop {
		r.mu.Lock()
		r.pending[rec.seq-r.base].failed = true
		r.mu.Unlock()
	}
	switch r.p.cfg.ErrorPolicy() {
	case config.SyncSkip:
		r.p.logErr("%s: a record of %s is skipped after %d attempts: %v", where, rec.Namespace, attempts, err)
		return true
	case config.SyncDeadLetter:
		return r.deadLetter(rec, err, where, attempts)
	}
	r.fail(fmt.Errorf("%s: %w", where, err))
	return false
}

// deadLetter writes rec, with err, to the dead_letter sink, trying as often
// as a write is; when it cannot, the run fails.
func (r *run) deadLetter(rec *Record, cause error, where string, attempts int) bool {
	doc, err := rec.AppendJSON(nil)
	if err != nil {
		doc, _ = String(string(doc)).AppendJSON(nil)
	}
	dl := &Record{Source: rec.Source, Namespace: rec.Namespace, Op: OpMessage, Key: rec.Key, CommitTime: rec.CommitTime,
		Position: rec.Position, After: Map(
			Field{"error", String(cause.Error())},
			Field{"at", String(where)},
			Field{"attempts", Int(int64(attempts))},
			Field{"record", JSON(doc)})}
	r.deadLetters.Lock()
	defer r.deadLetters.Unlock()
	for n := 1; ; n++ {
		err := r.p.dl.Write(r.ctx, []*Record{dl})
		if err == nil {
			r.p.meter.dlWritten.Add(1)
			r.p.logErr("%s: a record of %s failed after %d attempts and is kept as a dead letter: %v", where, rec.Namespace, attempts, cause)
			return true
		}
		if r.ctx.Err() != nil {
			return false
		}
		r.p.noteErr(fmt.Errorf("dead_letter sink %s: %w", r.p.cfg.DeadLetter.Name, err))
		r.p.logErr("dead_letter sink %s: attempt %d failed: %v", r.p.cfg.DeadLetter.Name, n, err)
		if n >= r.p.cfg.Attempts() {
			r.p.meter.dlFailed.Add(1)
			r.fail(fmt.Errorf("dead_letter sink %s: %w; the record had failed at %s: %v", r.p.cfg.DeadLetter.Name, err, where, cause))
			return false
		}
		if !r.sleep(r.p.cfg.Backoff(n)) {
			return false
		}
	}
}

// sleep waits d, and reports false when the run stops first.
func (r *run) sleep(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-r.ctx.Done():
		return false
	}
}

// loop gathers s's batches and writes them, until the source has ended and
// every record is written, or the run stops.
func (s *sinkRun) loop() {
	r := s.r
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	maxRecords, maxBytes, wait := s.cfg.Records(), s.cfg.Bytes(), s.cfg.Wait()
	var recs []*Record
	for {
		r.mu.Lock()
		for {
			// A batch not full is written at once when the source waits
			// for room that its first record holds: with several
			// writers, the others may have written all theirs.
			full := len(s.queue) >= maxRecords || s.bytes >= maxBytes ||
				r.waiting > 0 && len(s.queue) > 0 && s.queue[0].rec.seq == r.base
			if full || len(s.queue) > 0 && (r.drained || time.Since(s.first) >= wait) {
				break
			}
			if len(s.queue) == 0 && r.drained {
				r.mu.Unlock()
				return
			}
			var tc <-chan time.Time
			if len(s.queue) > 0 {
				timer.Reset(wait - time.Since(s.first))
				tc = timer.C
			}
			r.mu.Unlock()
			select {
			case <-s.wake:
			case <-tc:
			case <-r.ctx.Done():
				return
			}
			if tc != nil && !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			r.mu.Lock()
		}
		n, size := 0, int64(0)
		for n < len(s.queue) && n < maxRecords && (n == 0 || size+int64(s.queue[n].size) <= maxBytes) {
			size += int64(s.queue[n].size)
			n++
		}
		batch := make([]item, n)
		copy(batch, s.queue)
		clear(s.queue[:n])
		s.queue = s.queue[n:]
		s.bytes -= size
		s.first = time.Now()
		r.mu.Unlock()
		recs = recs[:0]
		for _, it := range batch {
			recs = append(recs, it.rec)
		}
		if !s.write(batch, recs) {
			return
		}
	}
}

// write writes a batch, again from the record that failed with a backoff,
// and takes a record still failing as on_error says. It reports whether
// the run goes on.
func (s *sinkRun) write(batch []item, recs []*Record) bool {
	r := s.r
	cfg := r.p.cfg
	for i, attempt := 0, 0; i < len(batch); {
		if r.ctx.Err() != nil {
			return false
		}
		err := s.sink.Write(r.ctx, recs[i:])
		s.m.wrote(batch[i:], err)
		if err == nil {
			if r.crash(CrashWritten) {
				return false
			}
			r.ack(batch[i:])
			return !r.crash(CrashAcknowledged)
		}
		var be *BatchError
		if errors.As(err, &be) && be.Index >= 0 && be.Index < len(batch)-i {
			if be.Index > 0 {
				r.ack(batch[i : i+be.Index])
				i += be.Index
				attempt = 0
			}
			err = be.Err
		}
		attempt++
		if r.ctx.Err() != nil {
			return false
		}
		r.p.noteErr(fmt.Errorf("sink %s: %w", s.name, err))
		r.p.logErr("sink %s: attempt %d failed: %v", s.name, attempt, err)
		if attempt < cfg.Attempts() && !IsPermanent(err) {
			s.m.retries.Add(1)
			if !r.sleep(cfg.Backoff(attempt)) {
				return false
			}
			continue
		}
		s.m.failed.Add(1)
		if !r.failedOn(recs[i], err, "sink "+s.name, attempt) {
			return false
		}
		r.ack(batch[i : i+1])
		i++
		attempt = 0
	}
	return true
}

// checkpoint saves the mark when it moved, and tells the source.
func (r *run) checkpoint() {
	r.saveMu.Lock()
	defer r.saveMu.Unlock()
	r.mu.Lock()
	if !r.dirty && !r.finishing || r.killed.Load() {
		r.mu.Unlock()
		return
	}
	pos, finishing := r.mark, r.finishing
	at := &markState{pos, r.markDone, r.markFailed}
	r.dirty, r.finishing = false, false
	r.mu.Unlock()
	if pos == nil {
		pos = r.from
	}
	prog := r.p.progress(r, at, finishing)
	err := r.p.store.save(pos, prog)
	if errors.Is(err, errCrashed) {
		r.kill()
		return
	}
	if r.killed.Load() {
		return
	}
	if err != nil {
		r.p.rt.opts.Log.Errorf("pipeline %s: saving its checkpoint: %v", r.p.cfg.Name, err)
		r.mu.Lock()
		r.dirty, r.finishing = true, finishing
		r.mu.Unlock()
		return
	}
	r.mu.Lock()
	r.saved = true
	r.mu.Unlock()
	r.p.noteSaved(pos)
	if prog != nil {
		r.p.mu.Lock()
		r.p.job = prog
		r.p.mu.Unlock()
	}
	if r.crash(CrashCheckpointSaved) {
		return
	}
	if c, ok := r.p.src.(Confirmer); ok {
		c.Confirm(pos)
	}
}

// saveStart is Run.Start: the source's start saved as the checkpoint, while
// the run has none and nothing is acknowledged, so that a run killed before
// its first checkpoint starts again from it.
func (r *run) saveStart(pos Position) error {
	r.saveMu.Lock()
	defer r.saveMu.Unlock()
	r.mu.Lock()
	has := r.from != nil || r.mark != nil
	r.mu.Unlock()
	if r.killed.Load() {
		return errStopped
	}
	if has {
		return nil
	}
	err := r.p.store.save(pos, nil)
	if errors.Is(err, errCrashed) {
		r.kill()
		return errStopped
	}
	if err != nil {
		return fmt.Errorf("saving the source's start as the checkpoint: %w", err)
	}
	r.from = pos
	r.p.noteSaved(pos)
	if r.crash(CrashCheckpointSaved) {
		return errStopped
	}
	return nil
}

// outcome is how a run ended.
type outcome int

const (
	outFinished outcome = iota // the source ended and everything was written
	outStopped                 // Stop asked it to
	outFailed                  // on_error stop, or a dead letter not written
	outRetry                   // the source failed
	outKilled                  // a crash hook killed it
)

// do runs r from from until it ends, and saves its checkpoint, unless it
// was killed.
func (r *run) do(from Position) (outcome, error) {
	p := r.p
	r.from = from
	var wg sync.WaitGroup
	sinksDone := make(chan struct{})
	var sinks sync.WaitGroup
	for _, s := range r.sinks {
		sinks.Add(1)
		go func() { defer sinks.Done(); s.loop() }()
	}
	go func() { sinks.Wait(); close(sinksDone) }()
	wg.Add(3)
	go func() {
		defer wg.Done()
		run := &Run{From: from, Once: p.cfg.Once(), Emit: r.emit, Pipeline: p.cfg.Name, Lag: p.meter.setLag}
		if !p.cfg.Once() {
			run.Start = r.saveStart
		}
		var err error
		if c, ok := p.src.(Chunker); ok && p.cfg.Once() {
			err = p.runChunks(r.srcCtx, c, run, p.cfg.Parallel())
		} else {
			err = p.src.Run(r.srcCtx, run)
		}
		r.mu.Lock()
		r.srcDone = true
		if err != nil && r.srcCtx.Err() == nil {
			r.srcErr = err
		}
		r.mu.Unlock()
		notify(r.wake)
	}()
	go func() { defer wg.Done(); r.dispatch() }()
	go func() {
		defer wg.Done()
		t := time.NewTicker(p.cfg.Interval())
		defer t.Stop()
		for {
			select {
			case <-t.C:
				r.checkpoint()
			case <-r.ctx.Done():
				return
			}
		}
	}()
	stopped := false
	select {
	case <-sinksDone:
	case <-p.stopping:
		stopped = true
		r.cancelSrc()
		select {
		case <-sinksDone:
		case <-p.stopCtx.Done():
			r.cancel()
			<-sinksDone
		}
	}
	r.cancel()
	wg.Wait()
	if r.killed.Load() {
		return outKilled, nil
	}
	r.mu.Lock()
	r.finishing = p.cfg.Once() && !stopped && r.err == nil && r.srcErr == nil && r.drained
	r.mu.Unlock()
	r.checkpoint()
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case r.err != nil:
		return outFailed, r.err
	case r.srcErr != nil:
		return outRetry, r.srcErr
	case stopped:
		return outStopped, nil
	case r.drained:
		return outFinished, nil
	}
	return outStopped, nil
}
