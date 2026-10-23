package config

import (
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/internal/sysmem"
)

func TestDuration(t *testing.T) {
	type params struct {
		Timeout  Duration   `json:"timeout"`
		Timeouts []Duration `json:"timeouts,omitempty"`
		Optional *Duration  `json:"optional,omitempty"`
	}
	v, err := ParseValue([]byte(`{"timeout": "1m30s", "timeouts": ["0", "250ms", "1.5h"], "optional": "2s"}`), "p")
	if err != nil {
		t.Fatal(err)
	}
	var p params
	if err := v.Decode(&p); err != nil {
		t.Fatal(err)
	}
	if p.Timeout.Std() != 90*time.Second || len(p.Timeouts) != 3 || p.Timeouts[0] != 0 ||
		p.Timeouts[1].Std() != 250*time.Millisecond || p.Timeouts[2].Std() != 90*time.Minute ||
		*p.Optional != Duration(2*time.Second) || p.Timeout.String() != "1m30s" {
		t.Fatalf("decoded %+v", p)
	}

	v, err = ParseValue([]byte(`{"timeout": 10, "timeouts": ["10", "-1s", "1d"]}`), "p")
	if err != nil {
		t.Fatal(err)
	}
	err = v.Decode(&p)
	want := strings.Join([]string{
		`1:13: p.timeout: expected a string, got a number`,
		`1:30: p.timeouts[0]: "10" is not a duration, such as "10s" or "1m30s"`,
		`1:36: p.timeouts[1]: "-1s" is negative`,
		`1:43: p.timeouts[2]: "1d" is not a duration, such as "10s" or "1m30s"`,
	}, "\n")
	if err == nil || err.Error() != want {
		t.Fatalf("errors:\n%v\nwant:\n%s", err, want)
	}
}

func TestLimits(t *testing.T) {
	c, err := Load([]byte(`{"listeners": [{"name": "a", "address": ":1"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Limits.WriteStall(); got != 30*time.Second {
		t.Fatalf("default write stall timeout %v", got)
	}
	// Half the memory, or the fallback where it cannot be read.
	if got, n := c.Limits.PendingBytes(), sysmem.Total(); n == 0 && got != FallbackMaxPendingBytes || n > 0 && got != int64(n/2) {
		t.Fatalf("default max_pending_bytes %d, memory %d", got, n)
	}
	c, err = Load([]byte(`{"listeners": [{"name": "a", "address": ":1"}], "limits": {"write_stall_timeout": "5s"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Limits.WriteStall(); got != 5*time.Second {
		t.Fatalf("write stall timeout %v", got)
	}
	_, err = Load([]byte(`{"listeners": [{"name": "a", "address": ":1"}], "limits": {"write_stall_timeout": "soon"}}`))
	if err == nil || !strings.Contains(err.Error(), `limits.write_stall_timeout: "soon" is not a duration`) {
		t.Fatalf("error %v", err)
	}
}

func TestEngine(t *testing.T) {
	c, err := Load([]byte(`{"listeners": [{"name": "a", "address": ":1"}],
  "engine": {"event_loops": 4, "workers": 64, "min_workers": 8, "task_queue": 4096, "poll_batch": 512,
    "read_buffer_size": 32768, "write_watermark": 131072, "backlog": 1024}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := Engine{EventLoops: 4, Workers: 64, MinWorkers: 8, TaskQueue: 4096, PollBatch: 512,
		ReadBufferSize: 32768, WriteWatermark: 131072, Backlog: 1024}
	got := *c.Engine
	got.At = want.At
	if got != want {
		t.Fatalf("engine %+v", got)
	}
	for doc, msg := range map[string]string{
		`{"event_loops": -1}`:              "engine.event_loops",
		`{"event_loops": 2048}`:            "engine.event_loops",
		`{"read_buffer_size": 134217728}`:  "engine.read_buffer_size",
		`{"loops": 2}`:                     "engine.loops: unknown field",
		`{"workers": 4, "min_workers": 8}`: "min_workers, 8, is more than workers, 4",
	} {
		_, err := Load([]byte(`{"listeners": [{"name": "a", "address": ":1"}], "engine": ` + doc + `}`))
		if err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("%s: error %v, want %q", doc, err, msg)
		}
	}
	// min_workers alone is checked against fib's default workers by fib.
	if _, err := Load([]byte(`{"listeners": [{"name": "a", "address": ":1"}], "engine": {"min_workers": 8}}`)); err != nil {
		t.Fatal(err)
	}
}
