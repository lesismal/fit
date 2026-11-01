package app

import (
	"context"
	"errors"
	"io"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

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
	if c.Agent == nil {
		return (*config.AgentHub)(nil)
	}
	return c.Agent.Hub
}

func serverMember(c *config.Config) any {
	if c.Tunnel == nil {
		return (*config.TunnelServer)(nil)
	}
	return c.Tunnel.Server
}

func TestInfoPath(t *testing.T) {
	if got := (Info{ID: "tunnel.server"}).Path(); !slices.Equal(got, []string{"tunnel", "server"}) {
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
	mustPanic("no Member", Info{ID: "agent.hub", New: newRole}, "lacks")
	mustPanic("no New", Info{ID: "agent.hub", Member: hubMember}, "lacks")
	Register(Info{ID: "tunnel.server", Member: serverMember, New: newRole})
	Register(Info{ID: "agent.hub", Member: hubMember, New: newRole})
	mustPanic("twice", Info{ID: "agent.hub", Member: hubMember, New: newRole}, "twice")
	var ids []string
	for _, i := range Roles() {
		ids = append(ids, i.ID)
	}
	if !slices.Equal(ids, []string{"agent.hub", "tunnel.server"}) {
		t.Fatalf("Roles = %v, want sorted", ids)
	}
}

func TestSet(t *testing.T) {
	for _, tc := range []struct {
		member any
		want   bool
	}{
		{nil, false},
		{(*config.AgentHub)(nil), false},
		{(*config.AgentNode)(nil), false},
		{(*config.AgentClient)(nil), false},
		{(*config.TunnelServer)(nil), false},
		{(*config.TunnelNode)(nil), false},
		{(*config.TunnelClient)(nil), false},
		{&config.AgentHub{}, true},
		{&config.AgentNode{}, true},
		{&config.AgentClient{}, true},
		{&config.TunnelServer{}, true},
		{&config.TunnelNode{}, true},
		{&config.TunnelClient{}, true},
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
		Info{ID: "agent.hub", Member: hubMember, New: newRole, Check: func(*config.Config) error { return checkErr }},
		Info{ID: "tunnel.server", Member: serverMember, New: newRole},
	)
	every := &config.Config{
		Agent:  &config.Agent{Hub: &config.AgentHub{}, Node: &config.AgentNode{}, Client: &config.AgentClient{}},
		Tunnel: &config.Tunnel{Server: &config.TunnelServer{}, Node: &config.TunnelNode{}, Client: &config.TunnelClient{}},
	}
	if got := len(members(every)); got != 6 {
		t.Fatalf("members = %d, want 6", got)
	}
	// The hub and the server are registered; the other four roles are not.
	err := Check(every)
	var es config.Errors
	if !errors.As(err, &es) || len(es) != 4 || !strings.Contains(es[0].Msg, "built without the agent.node role") {
		t.Fatalf("Check = %v", err)
	}

	only := &config.Config{Agent: &config.Agent{Hub: &config.AgentHub{}}}
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
		{"Error", &config.Error{Path: "agent.hub", Msg: "c"}, []string{"c"}},
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
	server := &fakeRole{}
	var hosts []*Host
	withRoles(t,
		Info{ID: "agent.hub", Member: hubMember, New: func(h *Host) (Role, error) {
			hosts = append(hosts, h)
			return hub, nil
		}},
		Info{ID: "tunnel.server", Member: serverMember, New: func(h *Host) (Role, error) {
			hosts = append(hosts, h)
			if newErr != nil {
				return nil, newErr
			}
			return server, nil
		}},
	)
	root, err := logs.New(nil, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	m := NewManager(nil, nil, root, nil)
	defer m.Stop(context.Background())

	both := &config.Config{Agent: &config.Agent{Hub: &config.AgentHub{}}, Tunnel: &config.Tunnel{Server: &config.TunnelServer{}}}

	// New fails for the server: the hub runs alone.
	newErr = errors.New("no listener")
	if err := m.Apply(both); err == nil || !strings.Contains(err.Error(), "tunnel.server: no listener") {
		t.Fatalf("Apply = %v", err)
	}
	if got := m.Running(); !slices.Equal(got, []string{"agent.hub"}) {
		t.Fatalf("Running = %v", got)
	}
	for _, h := range hosts {
		if h.Log == nil {
			t.Fatalf("host %s has no log", h.ID)
		}
	}

	// The server's first Apply fails: it is stopped and not kept.
	newErr = nil
	server.applyErr = errors.New("bad member")
	if err := m.Apply(both); err == nil || !strings.Contains(err.Error(), "tunnel.server: bad member") {
		t.Fatalf("Apply = %v", err)
	}
	if !server.isStopped() || slices.Contains(m.Running(), "tunnel.server") {
		t.Fatal("a server whose first Apply failed is kept")
	}

	// Both start; then an Apply to the running hub fails, and it runs on.
	server.applyErr, server.stopped = nil, false
	if err := m.Apply(both); err != nil {
		t.Fatalf("Apply = %v", err)
	}
	hub.applyErr = errors.New("cannot move")
	if err := m.Apply(both); err == nil || !strings.Contains(err.Error(), "it runs as it did") {
		t.Fatalf("Apply = %v", err)
	}
	if got := m.Running(); !slices.Equal(got, []string{"agent.hub", "tunnel.server"}) {
		t.Fatalf("Running = %v", got)
	}

	// A config leaving the hub out stops it.
	if err := m.Apply(&config.Config{Tunnel: both.Tunnel}); err != nil {
		t.Fatalf("Apply = %v", err)
	}
	if !hub.isStopped() || !slices.Equal(m.Running(), []string{"tunnel.server"}) {
		t.Fatalf("hub stopped %v, Running %v", hub.isStopped(), m.Running())
	}

	m.Stop(context.Background())
	if !server.isStopped() || len(m.Running()) != 0 {
		t.Fatal("Stop left a role running")
	}
}

func TestManagerHostWithoutLog(t *testing.T) {
	m := NewManager(nil, nil, nil, nil)
	defer m.Stop(context.Background())
	if h := m.host("agent.hub"); h.Log != nil || h.ID != "agent.hub" || h.m != m {
		t.Fatalf("host = %+v", h)
	}
}

type editResult struct {
	warnings []string
	err      error
}

func TestHostEditWithoutEditor(t *testing.T) {
	m := NewManager(nil, nil, nil, nil)
	defer m.Stop(context.Background())
	h := m.host("agent.hub")
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
	m.host("tunnel.server").Edit(func([]byte) ([]byte, error) { return []byte(`{"name":"x"}`), nil },
		func(ws []string, err error) { got <- editResult{ws, err} })
	r := <-got
	if r.err != nil || !slices.Equal(r.warnings, []string{`tunnel.server {"name":"x"}`}) || !slices.Equal(gotPath, []string{"tunnel", "server"}) {
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
	h := m.host("agent.hub")
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
