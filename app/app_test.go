package app

import (
	"context"
	"errors"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/lesismal/fit/app/console"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
)

// withRoles runs the test with infos as the only roles registered, and puts
// the registry back after it.
func withRoles(t *testing.T, infos ...Info) {
	t.Helper()
	mu.Lock()
	saved := roles
	roles = nil
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		roles = saved
		mu.Unlock()
	})
	for _, i := range infos {
		Register(i)
	}
}

// fakeRole records the members applied to it and whether it stopped.
type fakeRole struct {
	mu       sync.Mutex
	applied  []any
	applyErr error
	stopped  bool
}

func (r *fakeRole) Apply(member any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.applied = append(r.applied, member)
	return r.applyErr
}

func (r *fakeRole) Stop(context.Context) {
	r.mu.Lock()
	r.stopped = true
	r.mu.Unlock()
}

func (r *fakeRole) isStopped() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopped
}

func hubMember(c *config.Config) any {
	if c.LLM == nil {
		return (*config.LLMHub)(nil)
	}
	return c.LLM.Hub
}

func tunnelHubMember(c *config.Config) any {
	if c.Tunnel == nil {
		return (*config.TunnelHub)(nil)
	}
	return c.Tunnel.Hub
}

func TestInfoPath(t *testing.T) {
	if got := (Info{ID: "tunnel.hub"}).Path(); !slices.Equal(got, []string{"tunnel", "hub"}) {
		t.Fatalf("Path = %v", got)
	}
}

func TestRegisterPanics(t *testing.T) {
	withRoles(t)
	newRole := func(*Host) (Role, error) { return &fakeRole{}, nil }
	mustPanic := func(name string, info Info, want string) {
		t.Helper()
		defer func() {
			r := recover()
			if r == nil || !strings.Contains(r.(string), want) {
				t.Fatalf("%s: panic = %v, want %q", name, r, want)
			}
		}()
		Register(info)
	}
	mustPanic("no ID", Info{Member: hubMember, New: newRole}, "lacks")
	mustPanic("no Member", Info{ID: "llm.hub", New: newRole}, "lacks")
	mustPanic("no New", Info{ID: "llm.hub", Member: hubMember}, "lacks")
	Register(Info{ID: "tunnel.hub", Member: tunnelHubMember, New: newRole})
	Register(Info{ID: "llm.hub", Member: hubMember, New: newRole})
	mustPanic("twice", Info{ID: "llm.hub", Member: hubMember, New: newRole}, "twice")
	var ids []string
	for _, i := range Roles() {
		ids = append(ids, i.ID)
	}
	if !slices.Equal(ids, []string{"llm.hub", "tunnel.hub"}) {
		t.Fatalf("Roles = %v, want sorted", ids)
	}
}

func TestSet(t *testing.T) {
	for _, tc := range []struct {
		member any
		want   bool
	}{
		{nil, false},
		{(*config.LLMHub)(nil), false},
		{(*config.LLMNode)(nil), false},
		{(*config.LLMProxy)(nil), false},
		{(*config.TunnelHub)(nil), false},
		{(*config.TunnelNode)(nil), false},
		{(*config.TunnelProxy)(nil), false},
		{&config.LLMHub{}, true},
		{&config.LLMNode{}, true},
		{&config.LLMProxy{}, true},
		{&config.TunnelHub{}, true},
		{&config.TunnelNode{}, true},
		{&config.TunnelProxy{}, true},
		{"another member", true},
	} {
		if got := set(tc.member); got != tc.want {
			t.Errorf("set(%#v) = %v, want %v", tc.member, got, tc.want)
		}
	}
}

func TestCheck(t *testing.T) {
	newRole := func(*Host) (Role, error) { return &fakeRole{}, nil }
	var checkErr error
	withRoles(t,
		Info{ID: "llm.hub", Member: hubMember, New: newRole, Check: func(*config.Config) error { return checkErr }},
		Info{ID: "tunnel.hub", Member: tunnelHubMember, New: newRole},
	)
	every := &config.Config{
		LLM:    &config.LLM{Hub: &config.LLMHub{}, Node: &config.LLMNode{}, Proxy: &config.LLMProxy{}},
		Tunnel: &config.Tunnel{Hub: &config.TunnelHub{}, Node: &config.TunnelNode{}, Proxy: &config.TunnelProxy{}},
	}
	if got := len(members(every)); got != 6 {
		t.Fatalf("members = %d, want 6", got)
	}
	// The LLM hub and the tunnel hub are registered; the other four roles are not.
	err := Check(every)
	var es config.Errors
	if !errors.As(err, &es) || len(es) != 4 || !strings.Contains(es[0].Msg, "built without the llm.node role") {
		t.Fatalf("Check = %v", err)
	}

	only := &config.Config{LLM: &config.LLM{Hub: &config.LLMHub{}}}
	if err := Check(only); err != nil {
		t.Fatalf("Check of a registered role = %v", err)
	}
	if err := Check(&config.Config{}); err != nil {
		t.Fatalf("Check of no roles = %v", err)
	}
	for _, tc := range []struct {
		name string
		err  error
		want []string
	}{
		{"Errors", config.Errors{{Msg: "a"}, {Msg: "b"}}, []string{"a", "b"}},
		{"Error", &config.Error{Path: "llm.hub", Msg: "c"}, []string{"c"}},
		{"plain", errors.New("d"), []string{"d"}},
	} {
		checkErr = tc.err
		err := Check(only)
		var es config.Errors
		if !errors.As(err, &es) {
			t.Fatalf("%s: Check = %v", tc.name, err)
		}
		var msgs []string
		for _, e := range es {
			msgs = append(msgs, e.Msg)
		}
		if !slices.Equal(msgs, tc.want) {
			t.Fatalf("%s: messages = %v, want %v", tc.name, msgs, tc.want)
		}
	}
}

func TestManagerApply(t *testing.T) {
	hub := &fakeRole{}
	var newErr error
	tunnelHub := &fakeRole{}
	var hosts []*Host
	withRoles(t,
		Info{ID: "llm.hub", Member: hubMember, New: func(h *Host) (Role, error) {
			hosts = append(hosts, h)
			return hub, nil
		}},
		Info{ID: "tunnel.hub", Member: tunnelHubMember, New: func(h *Host) (Role, error) {
			hosts = append(hosts, h)
			if newErr != nil {
				return nil, newErr
			}
			return tunnelHub, nil
		}},
	)
	root, err := logs.New(nil, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	m := NewManager(nil, nil, root, nil)
	defer m.Stop(context.Background())

	both := &config.Config{LLM: &config.LLM{Hub: &config.LLMHub{}}, Tunnel: &config.Tunnel{Hub: &config.TunnelHub{}}}

	// New fails for the tunnel hub: the LLM hub runs alone.
	newErr = errors.New("no listener")
	if err := m.Apply(both); err == nil || !strings.Contains(err.Error(), "tunnel.hub: no listener") {
		t.Fatalf("Apply = %v", err)
	}
	if got := m.Running(); !slices.Equal(got, []string{"llm.hub"}) {
		t.Fatalf("Running = %v", got)
	}
	for _, h := range hosts {
		if h.Log == nil {
			t.Fatalf("host %s has no log", h.ID)
		}
	}

	// The tunnel hub's first Apply fails: it is stopped and not kept.
	newErr = nil
	tunnelHub.applyErr = errors.New("bad member")
	if err := m.Apply(both); err == nil || !strings.Contains(err.Error(), "tunnel.hub: bad member") {
		t.Fatalf("Apply = %v", err)
	}
	if !tunnelHub.isStopped() || slices.Contains(m.Running(), "tunnel.hub") {
		t.Fatal("a tunnel hub whose first Apply failed is kept")
	}

	// Both start; then an Apply to the running LLM hub fails, and it runs on.
	tunnelHub.applyErr, tunnelHub.stopped = nil, false
	if err := m.Apply(both); err != nil {
		t.Fatalf("Apply = %v", err)
	}
	hub.applyErr = errors.New("cannot move")
	if err := m.Apply(both); err == nil || !strings.Contains(err.Error(), "it runs as it did") {
		t.Fatalf("Apply = %v", err)
	}
	if got := m.Running(); !slices.Equal(got, []string{"llm.hub", "tunnel.hub"}) {
		t.Fatalf("Running = %v", got)
	}

	// A config leaving the LLM hub out stops it.
	if err := m.Apply(&config.Config{Tunnel: both.Tunnel}); err != nil {
		t.Fatalf("Apply = %v", err)
	}
	if !hub.isStopped() || !slices.Equal(m.Running(), []string{"tunnel.hub"}) {
		t.Fatalf("hub stopped %v, Running %v", hub.isStopped(), m.Running())
	}

	m.Stop(context.Background())
	if !tunnelHub.isStopped() || len(m.Running()) != 0 {
		t.Fatal("Stop left a role running")
	}
}

// pagedRole is a role with a module.
type pagedRole struct {
	fakeRole
	m *console.Module
}

func (r *pagedRole) Module() *console.Module { return r.m }

// The manager lists the modules of the running roles that have them, in the
// order of their IDs, and none once they stop.
func TestManagerModules(t *testing.T) {
	hub := &pagedRole{m: &console.Module{ID: "llm.hub"}}
	tunnelHub := &pagedRole{m: &console.Module{ID: "tunnel.hub"}}
	withRoles(t,
		Info{ID: "tunnel.hub", Member: tunnelHubMember, New: func(*Host) (Role, error) { return tunnelHub, nil }},
		Info{ID: "llm.hub", Member: hubMember, New: func(*Host) (Role, error) { return hub, nil }},
		Info{ID: "llm.node", Member: func(c *config.Config) any {
			if c.LLM == nil {
				return (*config.LLMNode)(nil)
			}
			return c.LLM.Node
		}, New: func(*Host) (Role, error) { return &fakeRole{}, nil }},
	)
	m := NewManager(nil, nil, nil, nil)
	defer m.Stop(context.Background())
	if got := m.Modules(); len(got) != 0 {
		t.Fatalf("Modules before Apply = %v", got)
	}
	c := &config.Config{LLM: &config.LLM{Hub: &config.LLMHub{}, Node: &config.LLMNode{}}, Tunnel: &config.Tunnel{Hub: &config.TunnelHub{}}}
	if err := m.Apply(c); err != nil {
		t.Fatal(err)
	}
	if got := m.Modules(); len(got) != 2 || got[0] != hub.m || got[1] != tunnelHub.m {
		t.Fatalf("Modules = %v", got)
	}
	m.Stop(context.Background())
	if got := m.Modules(); len(got) != 0 {
		t.Fatalf("Modules after Stop = %v", got)
	}
}

func TestManagerHostWithoutLog(t *testing.T) {
	m := NewManager(nil, nil, nil, nil)
	defer m.Stop(context.Background())
	if h := m.host("llm.hub"); h.Log != nil || h.ID != "llm.hub" || h.m != m {
		t.Fatalf("host = %+v", h)
	}
}

// A role keeps its files beside the config file, named after it, or in the
// directory of configs; none for standard input, or before KeepBeside.
func TestKeptFile(t *testing.T) {
	m := NewManager(nil, nil, nil, nil)
	defer m.Stop(context.Background())
	h := m.host("tunnel.hub")
	if f := h.KeptFile("pem"); f != "" {
		t.Fatalf("before KeepBeside: %q", f)
	}
	if f := (&Host{ID: "tunnel.hub"}).KeptFile("pem"); f != "" {
		t.Fatalf("without a Manager: %q", f)
	}
	dir := t.TempDir()
	m.KeepBeside(filepath.Join(dir, "fit.yaml"))
	if f := h.KeptFile("pem"); f != filepath.Join(dir, "fit.tunnel.hub.pem") {
		t.Fatalf("beside a file: %q", f)
	}
	m.KeepBeside(dir)
	if f := h.KeptFile("pem"); f != filepath.Join(dir, "fit.tunnel.hub.pem") {
		t.Fatalf("in a directory: %q", f)
	}
	wd, _ := os.Getwd()
	m.KeepBeside("conf.json")
	if f := h.KeptFile("pem"); f != filepath.Join(wd, "conf.tunnel.hub.pem") {
		t.Fatalf("a relative file: %q", f)
	}
	for _, p := range []string{"-", ""} {
		m.KeepBeside(p)
		if f := h.KeptFile("pem"); f != "" {
			t.Fatalf("%q: %q", p, f)
		}
	}
}

type editResult struct {
	warnings []string
	err      error
}

func TestHostEditWithoutEditor(t *testing.T) {
	m := NewManager(nil, nil, nil, nil)
	defer m.Stop(context.Background())
	h := m.host("llm.hub")
	got := make(chan editResult, 1)
	h.Edit(func(b []byte) ([]byte, error) { return b, nil }, func(ws []string, err error) { got <- editResult{ws, err} })
	if r := <-got; r.err == nil || !strings.Contains(r.err.Error(), "cannot be changed here") {
		t.Fatalf("Edit = %v", r.err)
	}
	// done may be nil.
	h.Edit(func(b []byte) ([]byte, error) { return b, nil }, nil)
}

func TestHostEdit(t *testing.T) {
	var gotPath []string
	editor := func(source string, path []string, edit func([]byte) ([]byte, error)) ([]string, error) {
		gotPath = path
		out, err := edit([]byte(`{}`))
		return []string{source + " " + string(out)}, err
	}
	m := NewManager(nil, nil, nil, editor)
	defer m.Stop(context.Background())
	got := make(chan editResult, 1)
	m.host("tunnel.hub").Edit(func([]byte) ([]byte, error) { return []byte(`{"name":"x"}`), nil },
		func(ws []string, err error) { got <- editResult{ws, err} })
	r := <-got
	if r.err != nil || !slices.Equal(r.warnings, []string{`tunnel.hub {"name":"x"}`}) || !slices.Equal(gotPath, []string{"tunnel", "hub"}) {
		t.Fatalf("Edit = %v %v, path %v", r.warnings, r.err, gotPath)
	}
}

func TestHostEditBusyAndStopped(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	editor := func(string, []string, func([]byte) ([]byte, error)) ([]string, error) {
		once.Do(func() { close(entered) })
		<-release
		return nil, nil
	}
	m := NewManager(nil, nil, nil, editor)
	h := m.host("llm.hub")
	results := make(chan error, MaxEdits+2)
	done := func(_ []string, err error) { results <- err }
	nop := func(b []byte) ([]byte, error) { return b, nil }

	// The first edit holds the editor; MaxEdits more fill the queue, and the
	// one past them is refused at once.
	h.Edit(nop, done)
	<-entered
	for range MaxEdits {
		h.Edit(nop, done)
	}
	h.Edit(nop, done)
	if err := <-results; !errors.Is(err, ErrBusy) {
		t.Fatalf("an Edit past MaxEdits = %v, want ErrBusy", err)
	}

	// Stop refuses what still waits once the editor lets go.
	stopped := make(chan struct{})
	go func() {
		m.Stop(context.Background())
		close(stopped)
	}()
	for !isClosed(m.stop) {
		runtime.Gosched()
	}
	close(release)
	<-stopped
	var nStopped, nDone int
	for range MaxEdits + 1 {
		err := <-results
		switch {
		case errors.Is(err, ErrStopped):
			nStopped++
		case err == nil:
			nDone++
		default:
			t.Fatalf("an Edit = %v", err)
		}
	}
	if nStopped == 0 {
		t.Fatalf("no Edit waiting was refused by Stop (%d done)", nDone)
	}

	h.Edit(nop, done)
	if err := <-results; !errors.Is(err, ErrStopped) {
		t.Fatalf("an Edit after Stop = %v, want ErrStopped", err)
	}
}

func isClosed(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// A role's policy is its own list, then the global access of the config
// the Manager applied last; a Host of no Manager goes by its own alone, and
// a list that does not compile is an error.
func TestHostAccessGlobal(t *testing.T) {
	m := NewManager(nil, nil, nil, nil)
	defer m.Stop(context.Background())
	if err := m.Apply(&config.Config{Access: &config.Access{Deny: []string{"10.0.0.0/8"}}}); err != nil {
		t.Fatal(err)
	}
	h := m.Host("x")
	own := &config.Access{Allow: []string{"10.1.0.0/16"}, Deny: []string{"10.1.2.0/24"}}
	p, err := h.Access(own)
	if err != nil {
		t.Fatal(err)
	}
	for addr, want := range map[string]bool{"10.1.1.1": true, "10.1.2.1": false, "10.2.0.1": false, "192.0.2.1": false} {
		if p.Allows(netip.MustParseAddr(addr)) != want {
			t.Errorf("own and global: %s", addr)
		}
	}
	if p, err = h.Access(nil); err != nil {
		t.Fatal(err)
	}
	for addr, want := range map[string]bool{"10.1.1.1": false, "192.0.2.1": true} {
		if p.Allows(netip.MustParseAddr(addr)) != want {
			t.Errorf("global alone: %s", addr)
		}
	}
	if err := m.Apply(&config.Config{}); err != nil {
		t.Fatal(err)
	}
	if p, err = h.Access(nil); err != nil || p != nil {
		t.Fatalf("no access anywhere: %v %v", p, err)
	}
	if p, err = (&Host{}).Access(&config.Access{Deny: []string{"10.0.0.0/8"}}); err != nil || p.Allows(netip.MustParseAddr("10.0.0.1")) || !p.Allows(netip.MustParseAddr("192.0.2.1")) {
		t.Fatalf("no Manager: %v", err)
	}
	if _, err := h.Access(&config.Access{DenyFiles: []string{filepath.Join(t.TempDir(), "missing")}}); err == nil {
		t.Fatal("a list file that is not there")
	}
}
