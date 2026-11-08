package fitsync

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/module"
	"github.com/lesismal/fit/observe"
)

// laggingSource keeps its lag, as the postgres source does; unitSource says
// its unit too.
type laggingSource struct{ holdSource }

func (*laggingSource) Lag() int64 { return 7 }

type unitSource struct{ laggingSource }

func (*unitSource) LagUnit() string { return LagOffsets }

func init() {
	module.Register(module.Info{ID: Sources + ".lagging", New: func() any { return new(laggingSource) }, Doc: "Tests'."})
	module.Register(module.Info{ID: Sources + ".unitlag", New: func() any { return new(unitSource) }, Doc: "Tests'."})
}

// TestLagger: a source that keeps its lag is asked for it.
func TestLagger(t *testing.T) {
	_, sn := memStore(t)
	rt := NewRuntime(Options{})
	defer rt.Close(context.Background())
	sink := fmt.Sprintf(`"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}}]`, sn)
	if err := rt.Apply(syncConfig(t, t.TempDir(), `{"name": "a", "source": {"module": "lagging"}, `+sink+`}`,
		`{"name": "b", "source": {"module": "unitlag"}, `+sink+`}`)); err != nil {
		t.Fatal(err)
	}
	if l := state(rt, "a").SourceLag; l == nil || l.Behind != 7 || l.Unit != LagBytes {
		t.Errorf("%+v", l)
	}
	if l := state(rt, "b").SourceLag; l == nil || l.Unit != LagOffsets {
		t.Errorf("%+v", l)
	}
}

// logLines keeps what a logger says.
type logLines struct {
	mu    sync.Mutex
	lines []string
}

func (l *logLines) logger() *logs.Logger {
	return logs.Func(func(format string, args ...any) {
		l.mu.Lock()
		l.lines = append(l.lines, fmt.Sprintf(format, args...))
		l.mu.Unlock()
	})
}

func (l *logLines) count(sub string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, s := range l.lines {
		if strings.Contains(s, sub) {
			n++
		}
	}
	return n
}

// TestMetrics: each metric of a pipeline and its sinks, as /metrics writes
// them.
func TestMetrics(t *testing.T) {
	s, sn := memStore(t)
	_, on := memStore(t)
	var calls atomic.Int32
	s.SetHook(func(_ context.Context, batch []*Record) error {
		if calls.Add(1) == 1 {
			return errors.New("once") // tried again
		}
		for i, r := range batch {
			if keyID(r) == 3 {
				return &BatchError{Index: i, Err: Permanent(errors.New("a bad row"))}
			}
		}
		return nil
	})
	saved := errLogEvery
	errLogEvery = time.Hour
	defer func() { errLogEvery = saved }()
	var log logLines
	rt := NewRuntime(Options{Log: log.logger()})
	defer rt.Close(context.Background())
	applyOne(t, rt, t.TempDir(), fmt.Sprintf(`{"name": "p", "on_error": "dead_letter", "retry": {"max_attempts": 3, "backoff": "1ms"},
		"source": {"module": "hold", "params": {"records": 10, "lag": 42}},
		"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}, "batch": {"max_records": 100, "max_wait": "50ms"}},
		          {"name": "o", "sink": {"module": "memory", "params": {"store": %q}}, "batch": {"max_wait": "1ms"}}],
		"dead_letter": {"name": "dl", "sink": {"module": "dead_letters"}}}`, sn, on))
	waitFor(t, "the records", func() bool { return state(rt, "p").Acknowledged == 10 })
	st := state(rt, "p")
	m := st.Sinks[0]
	// Once failed, then 3 written before the bad row, then 6 after it:
	// in three writes, or more if the records came in several batches.
	if m.Name != "m" || m.Records != 9 || m.Batches < 3 || m.Errors != 2 || m.Retries != 1 || m.Failed != 1 || m.Bytes <= 0 {
		t.Fatalf("%+v", m)
	}
	if st.ReadBytes <= 0 || st.SourceLag == nil || st.SourceLag.Behind != 42 || st.SourceLag.Unit != LagOffsets {
		t.Fatalf("%+v", st)
	}

	w := &observe.TextWriter{}
	(&role{rt: rt}).WriteMetrics(w)
	out := string(w.B)
	for _, want := range []string{
		`fit_sync_pipeline_state{pipeline="p",state="running"} 1`,
		`fit_sync_pipeline_state{pipeline="p",state="stopped"} 0`,
		`fit_sync_records_read_total{pipeline="p"} 10`,
		fmt.Sprintf(`fit_sync_read_bytes_total{pipeline="p"} %d`, st.ReadBytes),
		`fit_sync_records_acknowledged_total{pipeline="p"} 10`,
		`fit_sync_records_failed_total{pipeline="p"} 1`,
		`fit_sync_errors_total{pipeline="p"} 2`,
		`fit_sync_queue_records{pipeline="p"} 0`,
		`fit_sync_queue_bytes{pipeline="p"} 0`,
		`fit_sync_queue_max_records{pipeline="p"} 10000`,
		`fit_sync_queue_max_bytes{pipeline="p"} `,
		`fit_sync_lag_seconds{pipeline="p"} `,
		`fit_sync_last_commit_timestamp_seconds{pipeline="p"} `,
		`fit_sync_source_lag{pipeline="p",unit="offsets"} 42`,
		`fit_sync_sink_records_total{pipeline="p",sink="m"} 9`,
		`fit_sync_sink_records_total{pipeline="p",sink="o"} 10`,
		fmt.Sprintf(`fit_sync_sink_bytes_total{pipeline="p",sink="m"} %d`, m.Bytes),
		fmt.Sprintf(`fit_sync_sink_batches_total{pipeline="p",sink="m"} %d`, m.Batches),
		`fit_sync_sink_errors_total{pipeline="p",sink="m"} 2`,
		`fit_sync_sink_retries_total{pipeline="p",sink="m"} 1`,
		`fit_sync_sink_failed_total{pipeline="p",sink="m"} 1`,
		`fit_sync_dead_letters_total{pipeline="p",result="written"} 1`,
		`fit_sync_dead_letters_total{pipeline="p",result="failed"} 0`,
		`fit_sync_dead_letters_replayed_total{pipeline="p"} 0`,
		`fit_sync_dead_letters_kept{pipeline="p"} 1`,
		`fit_sync_dead_letters_kept_bytes{pipeline="p"} `,
		`fit_sync_dead_letters_dropped_total{pipeline="p"} 0`,
		"# TYPE fit_sync_sink_records_total counter",
		"# TYPE fit_sync_queue_records gauge",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("no %s in\n%s", want, out)
		}
	}
	// The error lines are limited: one in the hour, of the three.
	if n := log.count("pipeline p: "); n != 1 {
		t.Errorf("%d error lines: %q", n, log.lines)
	}
	if log.count("pipeline p runs from where its source starts") != 1 {
		t.Errorf("%q", log.lines)
	}

	// No pipeline, no metrics.
	w = &observe.TextWriter{}
	NewRuntime(Options{}).WriteMetrics(w)
	if len(w.B) != 0 {
		t.Errorf("%s", w.B)
	}
}

// TestDeadLetterNotWrittenCounted: a dead letter the dead_letter sink could
// not take is counted, and a pipeline without one shows no dead letters.
func TestDeadLetterNotWrittenCounted(t *testing.T) {
	s, sn := memStore(t)
	s.SetHook(func(context.Context, []*Record) error { return Permanent(errors.New("bad")) })
	rt := NewRuntime(Options{})
	defer rt.Close(context.Background())
	applyOne(t, rt, t.TempDir(), fmt.Sprintf(`{"name": "p", "on_error": "dead_letter", "retry": {"max_attempts": 1},
		"source": {"module": "hold", "params": {"records": 1}},
		"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}}],
		"dead_letter": {"name": "dl", "sink": {"module": "dead_letters", "params": {"max_bytes": 10}}}}`, sn))
	waitFor(t, "the stop", func() bool { return state(rt, "p").State == "stopped" })
	if st := state(rt, "p"); st.DeadLetters.NotWritten != 1 || st.DeadLetters.Written != 0 {
		t.Fatalf("%+v", st.DeadLetters)
	}
	w := &observe.TextWriter{}
	rt.WriteMetrics(w)
	if !strings.Contains(string(w.B), `fit_sync_dead_letters_total{pipeline="p",result="failed"} 1`) {
		t.Errorf("%s", w.B)
	}
}

// TestLogErrHolds: the lines held back are told with the next.
func TestLogErrHolds(t *testing.T) {
	var log logLines
	c := syncConfig(t, t.TempDir(), `{"name": "p", "source": {"module": "hold"},
		"sinks": [{"name": "m", "sink": {"module": "stdout"}}]}`)
	p := &pipe{rt: NewRuntime(Options{Log: log.logger()}), cfg: &c.Pipelines[0]}
	p.logErr("one")
	p.logErr("two")
	p.logErr("three")
	p.errLog.mu.Lock()
	p.errLog.last = time.Now().Add(-time.Hour)
	p.errLog.mu.Unlock()
	p.logErr("four")
	if log.count("pipeline p: one") != 1 || log.count("pipeline p: four (and 2 more errors since the last logged)") != 1 || len(log.lines) != 2 {
		t.Fatalf("%q", log.lines)
	}
	var r Run
	r.ReportLag(5, LagBytes) // no Lag: nothing
}
