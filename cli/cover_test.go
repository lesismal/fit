package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestAdaptToFitRefused checks that fit adapt fails on a config it cannot
// write as a .conf file: a list of one value in the params of a module this
// fit does not know, which would read back as the value alone.
func TestAdaptToFitRefused(t *testing.T) {
	f := filepath.Join(t.TempDir(), "fit.json")
	writeFile(t, f, `{"listeners": [{"name": "web", "address": ":8080"}],
  "routes": [{"handler": {"module": "elsewhere", "params": {"x": [1]}}}]}`)
	code, stdout, stderr := runFit("adapt", "-c", f, "-to", "fit")
	if code != 1 || stdout != "" || !strings.Contains(stderr, "fit adapt: ") || !strings.Contains(stderr, "fewer than two values") {
		t.Errorf("%d %q %q", code, stdout, stderr)
	}
}
