package buildinfo

import (
	"runtime/debug"
	"testing"
)

func TestVersions(t *testing.T) {
	if fit, fib := Versions("v9.9.9"); fit != "v9.9.9" || fib == "" {
		t.Errorf("Versions: %q %q", fit, fib)
	}
	if fit, fib := versionsOf("", nil, false); fit != "unknown" || fib != "unknown" {
		t.Errorf("no build info: %q %q", fit, fib)
	}
	if fit, _ := versionsOf("v1", nil, false); fit != "v1" {
		t.Errorf("no build info, a version set: %q", fit)
	}
	info := &debug.BuildInfo{Main: debug.Module{Version: "v1.2.3"}, Deps: []*debug.Module{
		{Path: "example.com/other", Version: "v0.1.0"},
		{Path: fibModule, Version: "v1.7.2"},
	}}
	if fit, fib := versionsOf("", info, true); fit != "v1.2.3" || fib != "v1.7.2" {
		t.Errorf("recorded: %q %q", fit, fib)
	}
	info.Deps[1].Replace = &debug.Module{Path: "../fib"}
	if _, fib := versionsOf("", info, true); fib != "replaced by ../fib" {
		t.Errorf("replaced: %q", fib)
	}
	info.Deps[1].Replace = &debug.Module{Path: "example.com/fib", Version: "v0.0.1"}
	if _, fib := versionsOf("", info, true); fib != "replaced by example.com/fib v0.0.1" {
		t.Errorf("replaced by a version: %q", fib)
	}
}
