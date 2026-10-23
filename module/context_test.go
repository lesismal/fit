package module

import (
	"testing"

	"github.com/lesismal/fit/config"
)

// TestContextWithoutEnv: a module provisioned without an Env, as in tests,
// has no engine and no upstreams.
func TestContextWithoutEnv(t *testing.T) {
	c := new(Context)
	if c.Engine() != nil {
		t.Error("an engine without an Env")
	}
	if _, ok := c.Upstream("a"); ok {
		t.Error("an upstream without an Env")
	}
	if c.WriteStallTimeout() != config.DefaultWriteStallTimeout.Std() {
		t.Errorf("write stall %v", c.WriteStallTimeout())
	}
}
