package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSyncRun checks fit sync run: a job runs to its end and exits 0, one
// whose records fail exits 3 with their count, one stopped exits 1.
func TestSyncRun(t *testing.T) {
	dir := t.TempDir()
	store := fmt.Sprintf("cli-%s-%d", t.Name(), time.Now().UnixNano())
	conf := filepath.Join(dir, "sync.json")
	_ = os.WriteFile(conf, []byte(`{"sync": {"state_dir": "`+filepath.ToSlash(dir)+`", "pipelines": [
		{"name": "copy", "mode": "once", "job": {"max_parallel": 3},
		 "source": {"module": "generate", "params": {"records": 100, "chunk_records": 10}},
		 "sinks": [{"name": "out", "sink": {"module": "memory", "params": {"store": "`+store+`-a"}}, "batch": {"max_wait": "1ms"}}]},
		{"name": "full", "mode": "once", "on_error": "skip", "retry": {"max_attempts": 1},
		 "source": {"module": "generate", "params": {"records": 100, "chunk_records": 10}},
		 "sinks": [{"name": "out", "sink": {"module": "memory", "params": {"store": "`+store+`-b", "max_records": 60}}, "batch": {"max_wait": "1ms"}}]},
		{"name": "stops", "mode": "once", "retry": {"max_attempts": 1},
		 "source": {"module": "generate", "params": {"records": 100}},
		 "sinks": [{"name": "out", "sink": {"module": "memory", "params": {"store": "`+store+`-c", "max_records": 60}}, "batch": {"max_wait": "1ms"}}]},
		{"name": "stream",
		 "source": {"module": "generate", "params": {"records": 50}},
		 "sinks": [{"name": "out", "sink": {"module": "memory", "params": {"store": "`+store+`-d"}}, "batch": {"max_wait": "1ms"}}]}]}}`), 0o644)
	for _, c := range []struct {
		args           []string
		code           int
		stdout, stderr string
	}{
		{[]string{"sync", "run", "-c", conf, "copy"}, 0, "pipeline copy: finished: 100 read, 100 written, 0 failed, 10 of 10 chunks", ""},
		{[]string{"sync", "run", "copy", "-c", conf, "--once"}, 0, "100 written", ""},
		{[]string{"sync", "run", "-c", conf, "full"}, 3, "60 written, 40 failed", "40 records failed"},
		{[]string{"sync", "run", "-c", conf, "stops"}, 1, "pipeline stops: stopped", "the memory store is full"},
		{[]string{"sync", "run", "-c", conf, "--once", "stream"}, 0, "50 read, 50 written, 0 failed, in", ""},
		{[]string{"sync", "run", "-c", conf, "nope"}, 1, "", `no pipeline "nope"`},
		{[]string{"sync", "run", "-c", conf}, 2, "", "name one pipeline"},
		{[]string{"sync", "run", "-c", conf, "a", "b"}, 2, "", "name one pipeline"},
		{[]string{"sync"}, 2, "", "fit sync run"},
		{[]string{"sync", "-h"}, 0, "fit sync run", ""},
		{[]string{"sync", "run", "-bogus"}, 2, "", ""},
	} {
		code, stdout, stderr := runFit(c.args...)
		if code != c.code || !strings.Contains(stdout, c.stdout) || !strings.Contains(stderr, c.stderr) {
			t.Errorf("%v: %d %q %q", c.args, code, stdout, stderr)
		}
	}
}

// TestValidateSync checks that fit validate checks the sync role's
// pipelines, their modules' params included.
func TestValidateSync(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "sync.json")
	_ = os.WriteFile(good, []byte(`{"sync": {"state_dir": "`+filepath.ToSlash(dir)+`", "pipelines": [{"name": "p", "mode": "once",
		"source": {"module": "generate", "params": {"records": 10, "tx_size": 2}},
		"sinks": [{"name": "out", "sink": {"module": "stdout"}}]}]}}`), 0o644)
	if code, stdout, stderr := runFit("validate", "-c", good); code != 0 || !strings.Contains(stdout, "valid") {
		t.Fatalf("%d %q %q", code, stdout, stderr)
	}
	bad := filepath.Join(dir, "bad.json")
	_ = os.WriteFile(bad, []byte(`{"sync": {"state_dir": "x", "pipelines": [{"name": "p",
		"source": {"module": "generate", "params": {"record": 10}},
		"sinks": [{"name": "out", "sink": {"module": "elasticsearch"}}]}]}}`), 0o644)
	code, _, stderr := runFit("validate", "-c", bad)
	if code != 1 || !strings.Contains(stderr, `record`) || !strings.Contains(stderr, `no module "elasticsearch" in sync.sinks`) {
		t.Fatalf("%d %q", code, stderr)
	}
}
