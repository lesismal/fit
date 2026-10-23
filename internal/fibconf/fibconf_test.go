package fibconf

import (
	"testing"

	"github.com/lesismal/fib"

	"github.com/lesismal/fit/config"
)

func TestDefaults(t *testing.T) {
	want := fib.DefaultConfig()
	for _, e := range []*config.Engine{nil, {}} {
		got := Config("fit", e)
		if got.Name != "fit" {
			t.Errorf("Name = %q, want fit", got.Name)
		}
		got.Name = want.Name
		if got != want {
			t.Errorf("Config(%v) = %+v, want fib's defaults %+v", e, got, want)
		}
	}
}

func TestSet(t *testing.T) {
	e := &config.Engine{EventLoops: 3, Workers: 40, MinWorkers: 4, TaskQueue: 5000, PollBatch: 256,
		ReadBufferSize: 32 << 10, WriteWatermark: 128 << 10, Backlog: 4096}
	got := Config("fit", e)
	for _, c := range []struct {
		name      string
		got, want int
	}{
		{"IOPollerCount", got.IOPollerCount, 3},
		{"WorkerCount", got.WorkerCount, 40},
		{"MinWorkerCount", got.MinWorkerCount, 4},
		{"MaxEvents", got.MaxEvents, 5000},
		{"PollBatchSize", got.PollBatchSize, 256},
		{"ReadBufferSize", got.ReadBufferSize, 32 << 10},
		{"WriteBufferHighWatermark", got.WriteBufferHighWatermark, 128 << 10},
		{"Backlog", got.Backlog, 4096},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
	// What it leaves out stays fib's.
	def := fib.DefaultConfig()
	if got := Config("fit", &config.Engine{Workers: 40}); got.MaxEvents != def.MaxEvents || got.IOPollerCount != def.IOPollerCount {
		t.Errorf("Workers alone changed MaxEvents %d or IOPollerCount %d", got.MaxEvents, got.IOPollerCount)
	}
}
