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
	pending    []entry
	mark       Position
	dirty      bool
	saved      bool
	sinks      []*sinkRun
}

type sinkRun struct {
	r     *run
	name  string
	cfg   *config.SyncSink
	sink  Sink
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
	r := &run{p: p, wake: make(chan struct{}, 1), space: make(chan struct{}, 1)}
	r.ctx, r.cancel = context.WithCancel(context.Background())
	r.srcCtx, r.cancelSrc = context.WithCancel(r.ctx)
	for i := range p.sinks {
		r.sinks = append(r.sinks, &sinkRun{r: r, name: p.cfg.Sinks[i].Name, cfg: &p.cfg.Sinks[i], sink: p.sinks[i], wake: make(chan struct{}, 1)})
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
	r.mu.Lock()
	for r.inRecords > 0 && (r.inRecords+1 > limRecords || r.inBytes+int64(size) > limBytes) {
		r.mu.Unlock()
		select {
		case <-r.space:
		case <-r.srcCtx.Done():
			return errStopped
		}
		r.mu.Lock()
	}
	if r.srcCtx.Err() != nil {
		r.mu.Unlock()
		return errStopped
	}
	rec.seq, rec.made = r.next, false
	r.next++
	r.pending = append(r.pending, entry{left: -1, size: size, pos: rec.Position, boundary: rec.boundary()})
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
	freed := false
	for len(r.pending) > 0 && r.pending[0].left == 0 {
		e := r.pending[0]
		r.pending[0] = entry{}
		r.pending = r.pending[1:]
		r.base++
		r.inRecords--
		r.inBytes -= int64(e.size)
		if e.boundary && e.pos != nil {
			r.mark, r.dirty = e.pos, true
		}
		freed = true
	}
	if freed {
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
}

// dispatch takes records from the queue, puts them through the transforms
// and hands what they become to every sink, until the source has ended and
// the queue is empty, or the run stops.
func (r *run) dispatch() {
	cfg := r.p.cfg
	c := newStages(r.p.transforms)
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
				if !r.failed(rec, err, fmt.Sprintf("transform %d (%s)", t, cfg.Transforms[t].Module), 1) {
					c.done()
					return
				}
				outs = nil
			}
			r.mu.Lock()
			r.pending[rec.seq-r.base].left = len(outs) * len(r.sinks)
			for _, s := range r.sinks {
				if len(s.queue) == 0 {
					s.first = time.Now()
				}
				for _, o := range outs {
					n := o.rec.Size()
					s.queue = append(s.queue, item{o.rec, n})
					s.bytes += int64(n)
				}
			}
			r.advance()
			r.mu.Unlock()
		}
		c.done()
		for _, s := range r.sinks {
			notify(s.wake)
		}
		if r.crash(CrashDispatched) {
			return
		}
	}
}

// failed takes a record that failed at where, after attempts, as on_error
// says: it reports whether the run goes on past it.
func (r *run) failed(rec *Record, err error, where string, attempts int) bool {
	switch r.p.cfg.ErrorPolicy() {
	case config.SyncSkip:
		r.p.rt.opts.Log.Warnf("pipeline %s: %s: a record of %s is skipped after %d attempts: %v", r.p.cfg.Name, where, rec.Namespace, attempts, err)
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
			return true
		}
		if r.ctx.Err() != nil {
			return false
		}
		if n >= r.p.cfg.Attempts() {
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
			full := len(s.queue) >= maxRecords || s.bytes >= maxBytes
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
		if attempt < cfg.Attempts() {
			r.p.rt.opts.Log.Debugf("pipeline %s: sink %s: attempt %d failed: %v", cfg.Name, s.name, attempt, err)
			if !r.sleep(cfg.Backoff(attempt)) {
				return false
			}
			continue
		}
		if !r.failed(recs[i], err, "sink "+s.name, attempt) {
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
	if !r.dirty || r.killed.Load() {
		r.mu.Unlock()
		return
	}
	pos := r.mark
	r.dirty = false
	r.mu.Unlock()
	err := r.p.store.save(pos)
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
		r.dirty = true
		r.mu.Unlock()
		return
	}
	r.mu.Lock()
	r.saved = true
	r.mu.Unlock()
	r.p.noteSaved(pos)
	if r.crash(CrashCheckpointSaved) {
		return
	}
	if c, ok := r.p.src.(Confirmer); ok {
		c.Confirm(pos)
	}
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
		err := p.src.Run(r.srcCtx, &Run{From: from, Once: p.cfg.Once(), Emit: r.emit})
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
