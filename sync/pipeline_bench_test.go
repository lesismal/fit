package fitsync

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
)

// benchLines is a source of lines, as the file source reads them: each
// record a message of one line and a position of its own.
type benchLines struct {
	Records int64 `json:"records"`
	Size    int   `json:"size"`
}

func (s *benchLines) Run(ctx context.Context, r *Run) error {
	line := strings.Repeat("x", s.Size)
	for n := range s.Records {
		rec := &Record{Source: "bench", Namespace: "f", Op: OpMessage,
			After:    Map(Field{"line", String(line)}),
			Position: strconv.AppendInt(make(Position, 0, 24), n+1, 10)}
		if err := r.Emit(rec); err != nil {
			return err
		}
	}
	return nil
}

// benchDiscard is a sink that counts what it is handed.
type benchDiscard struct{}

var benchWritten atomic.Int64

func (benchDiscard) Write(_ context.Context, batch []*Record) error {
	benchWritten.Add(int64(len(batch)))
	return nil
}

func init() {
	module.Register(module.Info{ID: Sources + ".benchlines", New: func() any { return new(benchLines) }, Doc: "Benchmarks'."})
	module.Register(module.Info{ID: Sinks + ".benchdiscard", New: func() any { return new(benchDiscard) }, Doc: "Benchmarks'."})
}

// BenchmarkPipeline runs b.N records of a line each through a pipeline,
// from its source through dispatch to a sink that drops them: what the
// runtime costs a record, its allocations and the GC's marking of them
// included. An op is a record.
func BenchmarkPipeline(b *testing.B) {
	for _, size := range []int{128, 1024} {
		b.Run(fmt.Sprintf("line=%d", size), func(b *testing.B) {
			src := fmt.Sprintf(`{"sync": {"state_dir": %q, "pipelines": [{"name": "p", "mode": "once",
				"source": {"module": "benchlines", "params": {"records": %d, "size": %d}},
				"sinks": [{"name": "d", "sink": {"module": "benchdiscard"}, "batch": {"max_wait": "1ms"}}]}]}}`, b.TempDir(), b.N, size)
			c, err := config.Load([]byte(src))
			if err != nil {
				b.Fatal(err)
			}
			if err := Check(c.Sync); err != nil {
				b.Fatal(err)
			}
			benchWritten.Store(0)
			rt := NewRuntime(Options{})
			b.ReportAllocs()
			b.ResetTimer()
			if err := rt.Apply(c.Sync); err != nil {
				b.Fatal(err)
			}
			for benchWritten.Load() < int64(b.N) {
				time.Sleep(100 * time.Microsecond)
			}
			b.StopTimer()
			rt.Close(context.Background())
		})
	}
}
