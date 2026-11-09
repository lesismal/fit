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
	if c.MediaLibrary("a") != nil {
		t.Error("a media library without an Env")
	}
	if c.LLMProxy() != nil {
		t.Error("an llm.proxy without an Env")
	}
	if api := (&Context{env: &Env{LLMProxy: &config.LLMProxyAPI{Address: "127.0.0.1:1"}}}).LLMProxy(); api == nil || api.Address != "127.0.0.1:1" {
		t.Errorf("the llm.proxy %v", api)
	}
	if _, ok := c.Upstream("a"); ok {
		t.Error("an upstream without an Env")
	}
	if c.WriteStallTimeout() != config.DefaultWriteStallTimeout.Std() {
		t.Errorf("write stall %v", c.WriteStallTimeout())
	}
}
