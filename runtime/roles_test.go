package runtime

import (
	"strings"
	"testing"
)

// TestCompileWithoutRole checks a config with a role this build lacks, as
// the runtime's tests are built without the tunnel's, does not compile.
func TestCompileWithoutRole(t *testing.T) {
	g, err := compile(t, `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
		"routes": [{"handler": {"module": "respond"}}], "tunnel": {"node": {}}}`)
	if err == nil || !strings.Contains(err.Error(), "built without") {
		if g != nil {
			g.Release()
		}
		t.Fatalf("%v", err)
	}
}
