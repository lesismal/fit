package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
