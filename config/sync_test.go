package config

import (
	"strings"
	"testing"
	"time"
)

func TestSync(t *testing.T) {
	head := `{"sync": {"state_dir": "/var/lib/fit/sync", "pipelines": `
	sink := `{"name": "m", "sink": {"module": "memory"}}`
	c, err := Load([]byte(head + `[{"name": "a", "source": {"module": "generate"}, "sinks": [` + sink + `]},
		{"name": "b", "mode": "once", "on_error": "dead_letter", "checkpoint_interval": "1s",
		 "source": {"module": "generate"}, "transforms": [{"module": "x"}],
		 "sinks": [{"name": "m", "sink": {"module": "memory"}, "batch": {"max_records": 3, "max_bytes": 100, "max_wait": "5ms"}}],
		 "dead_letter": {"name": "dl", "sink": {"module": "memory"}},
		 "queue": {"max_records": 7, "max_bytes": 900}, "retry": {"max_attempts": 2, "backoff": "1s", "max_backoff": "3s"}}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !c.HasApps() {
		t.Fatal("a config of sync alone has no listeners, and needs none")
	}
	a, b := &c.Sync.Pipelines[0], &c.Sync.Pipelines[1]
	if a.Once() || a.ErrorPolicy() != SyncStop || a.Interval() != DefaultSyncCheckpoint || a.Records() != DefaultSyncQueueRecords ||
		a.Bytes() != DefaultSyncQueueBytes || a.Attempts() != DefaultSyncMaxAttempts || a.Backoff(1) != DefaultSyncBackoff ||
		a.Backoff(100) != DefaultSyncMaxBackoff || a.Sinks[0].Records() != DefaultSyncBatchRecords ||
		a.Sinks[0].Bytes() != DefaultSyncBatchBytes || a.Sinks[0].Wait() != DefaultSyncBatchWait {
		t.Fatalf("defaults: %+v", a)
	}
	if !b.Once() || b.ErrorPolicy() != SyncDeadLetter || b.Interval() != time.Second || b.Records() != 7 || b.Bytes() != 900 ||
		b.Attempts() != 2 || b.Backoff(1) != time.Second || b.Backoff(2) != 2*time.Second || b.Backoff(3) != 3*time.Second ||
		b.Sinks[0].Records() != 3 || b.Sinks[0].Bytes() != 100 || b.Sinks[0].Wait() != 5*time.Millisecond {
		t.Fatalf("settings: %+v", b)
	}
	if s, loc, err := b.Schedule(); a.Parallel() != DefaultSyncMaxParallel || s != nil || loc != nil || err != nil {
		t.Fatalf("job defaults: %d %v %v %v", a.Parallel(), s, loc, err)
	}
	b.Job = &SyncJob{MaxParallel: 9, Schedule: "@daily", TimeZone: "Asia/Shanghai"}
	if s, loc, err := b.Schedule(); b.Parallel() != 9 || s == nil || err != nil || loc.String() != "Asia/Shanghai" {
		t.Fatalf("job: %d %v %v %v", b.Parallel(), s, loc, err)
	}
	b.Job.TimeZone = ""
	if _, loc, _ := b.Schedule(); loc != time.UTC {
		t.Fatalf("job time zone: %v", loc)
	}
	for _, bad := range []struct{ doc, want string }{
		{`[{"name": "-a", "source": {"module": "g"}, "sinks": [` + sink + `]}]`, "not a valid name"},
		{`[{"name": "a", "source": {"module": "g"}, "sinks": [` + sink + `]}, {"name": "a", "source": {"module": "g"}, "sinks": [` + sink + `]}]`, `another pipeline is already named "a"`},
		{`[{"name": "a", "source": {"module": ""}, "sinks": [` + sink + `]}]`, "the module name is empty"},
		{`[{"name": "a", "source": {"module": "g"}, "transforms": [{"module": ""}], "sinks": [` + sink + `]}]`, "transforms[0].module: the module name is empty"},
		{`[{"name": "a", "source": {"module": "g"}, "sinks": []}]`, "at least one sink"},
		{`[{"name": "a", "source": {"module": "g"}}]`, "sinks"},
		{`[{"name": "a", "source": {"module": "g"}, "sinks": [` + sink + `,` + sink + `]}]`, `another sink of pipeline "a" is already named "m"`},
		{`[{"name": "a", "source": {"module": "g"}, "sinks": [{"name": "m n", "sink": {"module": "memory"}}]}]`, "not a valid name"},
		{`[{"name": "a", "source": {"module": "g"}, "sinks": [{"name": "m", "sink": {"module": ""}}]}]`, "the module name is empty"},
		{`[{"name": "a", "on_error": "dead_letter", "source": {"module": "g"}, "sinks": [` + sink + `]}]`, "needs a dead_letter sink"},
		{`[{"name": "a", "dead_letter": {"name": "dl", "sink": {"module": "memory"}}, "source": {"module": "g"}, "sinks": [` + sink + `]}]`, "only with on_error dead_letter"},
		{`[{"name": "a", "on_error": "dead_letter", "dead_letter": ` + sink + `, "source": {"module": "g"}, "sinks": [` + sink + `]}]`, "already named"},
		{`[{"name": "a", "mode": "batch", "source": {"module": "g"}, "sinks": [` + sink + `]}]`, "mode"},
		{`[{"name": "a", "retry": {"backoff": "2s", "max_backoff": "1s"}, "source": {"module": "g"}, "sinks": [` + sink + `]}]`, "shorter than backoff"},
		{`[{"name": "a", "job": {}, "source": {"module": "g"}, "sinks": [` + sink + `]}]`, "job is for pipelines of mode once"},
		{`[{"name": "a", "mode": "once", "job": {"schedule": "* * *"}, "source": {"module": "g"}, "sinks": [` + sink + `]}]`, "job.schedule: \"* * *\" has 3 fields"},
		{`[{"name": "a", "mode": "once", "job": {"schedule": "@reboot"}, "source": {"module": "g"}, "sinks": [` + sink + `]}]`, "not a descriptor"},
		{`[{"name": "a", "mode": "once", "job": {"time_zone": "Mars/Base"}, "source": {"module": "g"}, "sinks": [` + sink + `]}]`, "not a time zone"},
		{`[{"name": "a", "mode": "once", "job": {"max_parallel": 257}, "source": {"module": "g"}, "sinks": [` + sink + `]}]`, "max_parallel"},
	} {
		if _, err := Load([]byte(head + bad.doc + `}}`)); err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%s: %v, want %q", bad.doc, err, bad.want)
		}
	}
	if _, err := Load([]byte(`{"sync": {"state_dir": " "}}`)); err == nil || !strings.Contains(err.Error(), "state_dir may not be empty") {
		t.Errorf("%v", err)
	}
	if _, err := Load([]byte(`{"sync": {}}`)); err == nil || !strings.Contains(err.Error(), "state_dir") {
		t.Errorf("%v", err)
	}
}
