package fitsync

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lesismal/fit/observe"
)

// What the pipelines tell the Admin API's /metrics and their log (I189):
// counters per pipeline and per sink, the queue, the lag in time and in the
// source's position, the dead letters; and error lines, at most one each
// errLogEvery for a pipeline.

// sinkMeter counts what a pipeline's sink does, across its runs.
type sinkMeter struct {
	// records and bytes are those written and acknowledged; batches the
	// writes tried, errors those that failed, retries those tried again
	// after one failed; failed the records on_error took (or that stopped
	// the pipeline), replayed the dead letters written to it again.
	records, bytes, batches, errors, retries, failed atomic.Int64
}

// wrote counts a write of items that ended with err.
func (m *sinkMeter) wrote(items []item, err error) {
	m.batches.Add(1)
	n := len(items)
	if err != nil {
		m.errors.Add(1)
		n = 0
		var be *BatchError
		if errors.As(err, &be) && be.Index > 0 && be.Index < len(items) {
			n = be.Index
		}
	}
	size := int64(0)
	for _, it := range items[:n] {
		size += int64(it.size)
	}
	m.records.Add(int64(n))
	m.bytes.Add(size)
}

// sinkMeter returns the meter of the pipeline's sink i.
func (p *pipe) sinkMeter(i int) *sinkMeter { return &p.sinkMeters[i] }

// Lagger is a Source that keeps how far behind its server's head it is, as
// the postgres source keeps its slot's WAL, asked each time the status or
// the metrics are: in bytes, or in the unit a LagUnit() string method
// says. Its Lag must not block. A source can instead tell it as it learns
// it, through Run.ReportLag, which comes first.
type Lagger interface {
	Lag() int64
}

// setLag keeps how far behind its server's head the source is.
func (m *meter) setLag(l SourceLag) { m.lag.Store(&l) }

// SinkStatus is what a pipeline's sink did since the pipeline started, as
// Status shows it.
type SinkStatus struct {
	Name string `json:"name"`
	// Records and Bytes are what it wrote; bytes as the queue reckons a
	// record's size, not the bytes on the wire.
	Records int64 `json:"records"`
	Bytes   int64 `json:"bytes"`
	// Batches counts the writes tried, Errors those that failed, Retries
	// those tried again; Failed the records it still failed after their
	// attempts.
	Batches int64 `json:"batches"`
	Errors  int64 `json:"errors"`
	Retries int64 `json:"retries"`
	Failed  int64 `json:"failed"`
}

// DeadLetterStatus is what became of a pipeline's dead letters, as Status
// shows it.
type DeadLetterStatus struct {
	// Written counts the dead letters written to the dead_letter sink,
	// NotWritten those it could not take, which stopped the pipeline, and
	// Replayed those replayed to the sinks.
	Written    int64 `json:"written"`
	NotWritten int64 `json:"not_written"`
	Replayed   int64 `json:"replayed"`
	// Store is the dead_letters sink's file, when the dead_letter sink is
	// one: what it keeps against its bounds.
	Store *DeadLetterStore `json:"store,omitempty"`
}

// DeadLetterStore is a dead_letters sink's file, as Status shows it.
type DeadLetterStore struct {
	Path       string `json:"path"`
	Kept       int    `json:"kept"`
	Bytes      int64  `json:"bytes"`
	MaxRecords int    `json:"max_records"`
	MaxBytes   int64  `json:"max_bytes"`
	// Dropped counts the dead letters dropped for room, with when_full
	// drop_oldest, since the file was opened.
	Dropped int64 `json:"dropped"`
	// Err is why the file could not be opened, if it could not.
	Err string `json:"error,omitempty"`
}

// fillMetrics adds to st what the sinks' meters, the source's lag and the
// dead letters say.
func (p *pipe) fillMetrics(st *Status) {
	st.ReadBytes = p.meter.readBytes.Load()
	st.SourceLag = p.meter.lag.Load()
	if l, ok := p.src.(Lagger); ok && st.SourceLag == nil {
		unit := LagBytes
		if u, ok := l.(interface{ LagUnit() string }); ok {
			unit = u.LagUnit()
		}
		st.SourceLag = &SourceLag{Behind: l.Lag(), Unit: unit, At: time.Now()}
	}
	for i := range p.cfg.Sinks {
		m := &p.sinkMeters[i]
		st.Sinks = append(st.Sinks, SinkStatus{Name: p.cfg.Sinks[i].Name, Records: m.records.Load(), Bytes: m.bytes.Load(),
			Batches: m.batches.Load(), Errors: m.errors.Load(), Retries: m.retries.Load(), Failed: m.failed.Load()})
	}
	if p.cfg.DeadLetter != nil {
		st.DeadLetters = &DeadLetterStatus{Written: p.meter.dlWritten.Load(), NotWritten: p.meter.dlFailed.Load(), Replayed: p.meter.replayed.Load()}
		if d, ok := p.dl.(*DeadLetters); ok {
			st.DeadLetters.Store = d.status()
		}
	}
}

// errLogEvery is how often a pipeline logs an error at most: the errors in
// between are counted, and the next line says how many; a variable for
// tests.
var errLogEvery = 10 * time.Second

// errLog limits a pipeline's error lines.
type errLog struct {
	mu   sync.Mutex
	last time.Time
	held int
}

// logErr logs a warning of the pipeline's, unless one was logged less than
// errLogEvery ago, in which case it is counted, and told with the next.
func (p *pipe) logErr(format string, args ...any) {
	l := &p.errLog
	now := time.Now()
	l.mu.Lock()
	if !l.last.IsZero() && now.Sub(l.last) < errLogEvery {
		l.held++
		l.mu.Unlock()
		return
	}
	held := l.held
	l.last, l.held = now, 0
	l.mu.Unlock()
	msg := fmt.Sprintf(format, args...)
	if held > 0 {
		msg += fmt.Sprintf(" (and %d more errors since the last logged)", held)
	}
	p.rt.opts.Log.Warnf("pipeline %s: %s", p.cfg.Name, msg)
}

// states are the states a pipeline can be in, as fit_sync_pipeline_state
// writes each.
var states = []string{"starting", "running", "paused", "retrying", "finished", "stopped", "scheduled", "killed"}

// WriteMetrics writes the sync role's metrics.
func (r *role) WriteMetrics(w observe.MetricWriter) { r.rt.WriteMetrics(w) }

// WriteMetrics writes the pipelines' metrics to w, in Prometheus's names:
// per pipeline, its state, the records and bytes read, the records
// acknowledged and failed, the errors, the queue against its bounds, the
// lag in time and in the source's position, the dead letters; per sink,
// the records and bytes written, the batches, errors, retries and records
// failed.
func (rt *Runtime) WriteMetrics(w observe.MetricWriter) {
	sts := rt.Status()
	if len(sts) == 0 {
		return
	}
	family := func(name, typ, help string, value func(*Status) (float64, bool)) {
		w.Family(name, typ, help)
		for i := range sts {
			if v, ok := value(&sts[i]); ok {
				w.Sample(name, v, "pipeline", sts[i].Name)
			}
		}
	}
	w.Family("fit_sync_pipeline_state", "gauge", "A pipeline's state: 1 for the one it is in, 0 for the others.")
	for _, st := range sts {
		for _, s := range states {
			v := 0.0
			if st.State == s {
				v = 1
			}
			w.Sample("fit_sync_pipeline_state", v, "pipeline", st.Name, "state", s)
		}
	}
	family("fit_sync_records_read_total", "counter", "Records a pipeline's source read, again after a restart.",
		func(s *Status) (float64, bool) { return float64(s.Read), true })
	family("fit_sync_read_bytes_total", "counter", "The size of the records a pipeline's source read, as the queue reckons it.",
		func(s *Status) (float64, bool) { return float64(s.ReadBytes), true })
	family("fit_sync_records_acknowledged_total", "counter", "Records every sink of a pipeline acknowledged, those on_error took among them.",
		func(s *Status) (float64, bool) { return float64(s.Acknowledged), true })
	family("fit_sync_records_failed_total", "counter", "Records of a pipeline on_error took: skipped or written as dead letters.",
		func(s *Status) (float64, bool) { return float64(s.Failed), true })
	family("fit_sync_errors_total", "counter", "A pipeline's errors: failed writes (each attempt), records a transform failed, dead letters not written, the source's failures.",
		func(s *Status) (float64, bool) { return float64(s.Errors), true })
	family("fit_sync_queue_records", "gauge", "Records a pipeline read and its sinks have not all acknowledged yet.",
		func(s *Status) (float64, bool) { return float64(s.Queue.Records), true })
	family("fit_sync_queue_bytes", "gauge", "The size of the records in a pipeline's queue.",
		func(s *Status) (float64, bool) { return float64(s.Queue.Bytes), true })
	family("fit_sync_queue_max_records", "gauge", "A pipeline's queue.max_records.",
		func(s *Status) (float64, bool) { return float64(s.Queue.MaxRecords), true })
	family("fit_sync_queue_max_bytes", "gauge", "A pipeline's queue.max_bytes.",
		func(s *Status) (float64, bool) { return float64(s.Queue.MaxBytes), true })
	family("fit_sync_lag_seconds", "gauge", "How long after its commit time the last record a pipeline acknowledged was acknowledged; none when its records carry no commit time.",
		func(s *Status) (float64, bool) { return s.LagSeconds, !s.LastCommit.IsZero() })
	family("fit_sync_last_commit_timestamp_seconds", "gauge", "The commit time of the last record a pipeline acknowledged, in seconds since the Unix epoch.",
		func(s *Status) (float64, bool) {
			return float64(s.LastCommit.UnixMilli()) / 1e3, !s.LastCommit.IsZero()
		})
	w.Family("fit_sync_source_lag", "gauge", "How far behind its server's head a pipeline's source is, in its unit: a slot's or a binlog's bytes, Kafka's offsets; from sources that tell it.")
	for _, st := range sts {
		if l := st.SourceLag; l != nil {
			w.Sample("fit_sync_source_lag", float64(l.Behind), "pipeline", st.Name, "unit", l.Unit)
		}
	}

	sink := func(name, typ, help string, value func(*SinkStatus) int64) {
		w.Family(name, typ, help)
		for _, st := range sts {
			for i := range st.Sinks {
				w.Sample(name, float64(value(&st.Sinks[i])), "pipeline", st.Name, "sink", st.Sinks[i].Name)
			}
		}
	}
	sink("fit_sync_sink_records_total", "counter", "Records a sink wrote.", func(s *SinkStatus) int64 { return s.Records })
	sink("fit_sync_sink_bytes_total", "counter", "The size of the records a sink wrote, as the queue reckons it.", func(s *SinkStatus) int64 { return s.Bytes })
	sink("fit_sync_sink_batches_total", "counter", "Writes of batches a sink was asked for, each attempt.", func(s *SinkStatus) int64 { return s.Batches })
	sink("fit_sync_sink_errors_total", "counter", "Writes of batches a sink failed.", func(s *SinkStatus) int64 { return s.Errors })
	sink("fit_sync_sink_retries_total", "counter", "Writes of batches tried again after one failed.", func(s *SinkStatus) int64 { return s.Retries })
	sink("fit_sync_sink_failed_total", "counter", "Records a sink still failed once their attempts were spent.", func(s *SinkStatus) int64 { return s.Failed })

	w.Family("fit_sync_dead_letters_total", "counter", "Records written to a pipeline's dead_letter sink (written), or that it could not take (failed).")
	for _, st := range sts {
		if d := st.DeadLetters; d != nil {
			w.Sample("fit_sync_dead_letters_total", float64(d.Written), "pipeline", st.Name, "result", "written")
			w.Sample("fit_sync_dead_letters_total", float64(d.NotWritten), "pipeline", st.Name, "result", "failed")
		}
	}
	w.Family("fit_sync_dead_letters_replayed_total", "counter", "Dead letters replayed to a pipeline's sinks from the Admin API.")
	for _, st := range sts {
		if d := st.DeadLetters; d != nil {
			w.Sample("fit_sync_dead_letters_replayed_total", float64(d.Replayed), "pipeline", st.Name)
		}
	}
	store := func(name, typ, help string, value func(*DeadLetterStore) float64) {
		w.Family(name, typ, help)
		for _, st := range sts {
			if d := st.DeadLetters; d != nil && d.Store != nil {
				w.Sample(name, value(d.Store), "pipeline", st.Name)
			}
		}
	}
	store("fit_sync_dead_letters_kept", "gauge", "Dead letters a dead_letters sink keeps.",
		func(d *DeadLetterStore) float64 { return float64(d.Kept) })
	store("fit_sync_dead_letters_kept_bytes", "gauge", "The size of the dead letters a dead_letters sink keeps, in its file.",
		func(d *DeadLetterStore) float64 { return float64(d.Bytes) })
	store("fit_sync_dead_letters_dropped_total", "counter", "Dead letters a dead_letters sink dropped for room, with when_full drop_oldest.",
		func(d *DeadLetterStore) float64 { return float64(d.Dropped) })
}
