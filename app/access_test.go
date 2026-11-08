package app

import (
	"context"
	"io"
	"net/netip"
	"testing"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
)

// TestHostAccess: a role's policy, as its pages' gate takes it on each
// Apply, is its own access, then the global access of the config applied.
func TestHostAccess(t *testing.T) {
	var host *Host
	withRoles(t, Info{ID: "llm.hub", Member: hubMember, New: func(h *Host) (Role, error) {
		host = h
		return &fakeRole{}, nil
	}})
	root, err := logs.New(nil, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	m := NewManager(nil, nil, root, nil)
	defer m.Stop(context.Background())
	local := netip.MustParseAddr("127.0.0.1")
	allows := func(own *config.Access) bool {
		t.Helper()
		p, err := host.Access(own)
		if err != nil {
			t.Fatal(err)
		}
		return p.Allows(local)
	}
	apply := func(global *config.Access) {
		t.Helper()
		if err := m.Apply(&config.Config{Access: global, LLM: &config.LLM{Hub: &config.LLMHub{}}}); err != nil {
			t.Fatal(err)
		}
	}
	apply(nil)
	if !allows(nil) {
		t.Fatal("refused with no access anywhere")
	}
	apply(&config.Access{Deny: []string{"127.0.0.0/8"}})
	if allows(nil) {
		t.Fatal("the global deny does not reach a role")
	}
	if !allows(&config.Access{Allow: []string{"127.0.0.1"}}) {
		t.Fatal("a role's own allow does not decide before the global deny")
	}
	apply(nil)
	if !allows(nil) {
		t.Fatal("the global deny outlived its config")
	}
}
