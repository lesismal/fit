package admin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
)

// fakeRole records the members it is moved to.
type fakeRole struct {
	mu      sync.Mutex
	applied []*config.TunnelNode
	stopped bool
	fail    bool
	host    *app.Host
}

func (r *fakeRole) Apply(member any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail {
		return errors.New("cannot")
	}
	r.applied = append(r.applied, member.(*config.TunnelNode))
	return nil
}

func (r *fakeRole) Stop(context.Context) {
	r.mu.Lock()
	r.stopped = true
	r.mu.Unlock()
}

var (
	fakeMu   sync.Mutex
	fakeLast *fakeRole
)

func init() {
	app.Register(app.Info{
		ID: "tunnel.node",
		Member: func(c *config.Config) any {
			if c.Tunnel == nil {
				return nil
			}
			return c.Tunnel.Node
		},
		New: func(h *app.Host) (app.Role, error) {
			fakeMu.Lock()
			defer fakeMu.Unlock()
			fakeLast = &fakeRole{host: h}
			return fakeLast, nil
		},
	})
}

func lastRole() *fakeRole {
	fakeMu.Lock()
	defer fakeMu.Unlock()
	return fakeLast
}

func TestAppsFollowConfigs(t *testing.T) {
	f := start(t, doc("one"), &config.Admin{})
	m := app.NewManager(f.server.Engine(), nil, nil, f.configs.EditMember)
	t.Cleanup(func() { m.Stop(context.Background()) })
	f.configs.UseApps(m)

	// A role is started by the config that sets it...
	with := strings.TrimSuffix(doc("one"), "}") + `, "tunnel": {"node": {}}}`
	if _, err := f.configs.Apply([]byte(with), SourceAdmin); err != nil {
		t.Fatal(err)
	}
	r := lastRole()
	if got := m.Running(); len(got) != 1 || got[0] != "tunnel.node" || r == nil || len(r.applied) != 1 {
		t.Fatalf("running %v, role %+v", got, r)
	}
	// ...moved to the next...
	if _, err := f.configs.Apply([]byte(with), SourceAdmin); err != nil {
		t.Fatal(err)
	}
	if len(r.applied) != 2 {
		t.Errorf("applied %d times", len(r.applied))
	}
	// ...and stopped by one that leaves it out.
	if _, err := f.configs.Apply([]byte(doc("one")), SourceAdmin); err != nil {
		t.Fatal(err)
	}
	if !r.stopped || len(m.Running()) != 0 {
		t.Errorf("stopped %v, running %v", r.stopped, m.Running())
	}
}

func TestAppFailureIsAWarning(t *testing.T) {
	f := start(t, doc("one"), &config.Admin{})
	m := app.NewManager(f.server.Engine(), nil, nil, f.configs.EditMember)
	t.Cleanup(func() { m.Stop(context.Background()) })
	f.configs.UseApps(m)
	with := strings.TrimSuffix(doc("one"), "}") + `, "tunnel": {"node": {}}}`
	if _, err := f.configs.Apply([]byte(with), SourceAdmin); err != nil {
		t.Fatal(err)
	}
	lastRole().fail = true
	r, err := f.configs.Apply([]byte(strings.Replace(with, `"one"`, `"two"`, 1)), SourceAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0], "tunnel.node: cannot; it runs as it did") {
		t.Errorf("warnings %q", r.Warnings)
	}
	if got := f.served(t); got != "two" {
		t.Errorf("served %q: the rest of the config runs", got)
	}
}

func TestEditMember(t *testing.T) {
	f := start(t, doc("one"), &config.Admin{})
	m := app.NewManager(f.server.Engine(), nil, nil, f.configs.EditMember)
	t.Cleanup(func() { m.Stop(context.Background()) })
	f.configs.UseApps(m)

	// A member the config leaves out is made, and its role started.
	var was []byte
	ws, err := f.configs.EditMember("tunnel.node", []string{"tunnel", "node"}, func(cur []byte) ([]byte, error) {
		was = cur
		return []byte(`{}`), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if was != nil {
		t.Errorf("the member was %q", was)
	}
	if len(ws) != 1 || !strings.Contains(ws[0], "/tunnel/node changed in the running config only") {
		t.Errorf("warnings %q", ws)
	}
	if got := m.Running(); len(got) != 1 {
		t.Fatalf("running %v", got)
	}
	cur := f.configs.history[len(f.configs.history)-1]
	var top map[string]json.RawMessage
	if err := json.Unmarshal(cur.data, &top); err != nil || top["tunnel"] == nil || top["routes"] == nil {
		t.Errorf("the config is %s", cur.data)
	}
	if cur.Source != "tunnel.node" {
		t.Errorf("source %q", cur.Source)
	}

	// A change that does not load is not applied.
	_, err = f.configs.EditMember("tunnel.node", []string{"tunnel", "node"}, func(cur []byte) ([]byte, error) {
		return []byte(`{"nope": 1}`), nil
	})
	var errs config.Errors
	if !errors.As(err, &errs) || !strings.Contains(err.Error(), "nope") {
		t.Errorf("err %v", err)
	}
	if len(m.Running()) != 1 {
		t.Error("the role stopped")
	}
}

func TestHostEdit(t *testing.T) {
	f := start(t, doc("one"), &config.Admin{})
	m := app.NewManager(f.server.Engine(), nil, nil, f.configs.EditMember)
	t.Cleanup(func() { m.Stop(context.Background()) })
	f.configs.UseApps(m)
	with := strings.TrimSuffix(doc("one"), "}") + `, "tunnel": {"node": {}}}`
	if _, err := f.configs.Apply([]byte(with), SourceAdmin); err != nil {
		t.Fatal(err)
	}
	r := lastRole()
	done := make(chan error, 1)
	r.host.Edit(func(cur []byte) ([]byte, error) {
		if strings.TrimSpace(string(cur)) != "{}" {
			t.Errorf("the member is %q", cur)
		}
		return cur, nil
	}, func(_ []string, err error) { done <- err })
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(r.applied) != 2 {
		t.Errorf("applied %d times", len(r.applied))
	}
	// After Stop, edits are refused.
	m.Stop(context.Background())
	r.host.Edit(func(cur []byte) ([]byte, error) { return cur, nil }, func(_ []string, err error) { done <- err })
	if err := <-done; !errors.Is(err, app.ErrStopped) {
		t.Errorf("err %v", err)
	}
}
