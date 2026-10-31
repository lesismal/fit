// Package app runs the roles of the apps fit carries beside its gateway: the
// agent's hub, node and client, the tunnel's server, node and client, and
// WebRTC's TURN server, media server and media proxy.
// Each role is a member of fit's config, agent.hub or tunnel.server, and runs
// while the config sets it, on the listeners' fib engine, which every role
// in the process shares with the gateway.
//
// A role registers itself with Register in an init function, as a module
// does; package app/standard imports every role fit ships with. The Manager
// starts a role when a config applied sets its member, moves it to each
// config after, and stops it when one leaves it out. What a role's web pages
// change is written back into its member with Host.Edit, as a config applied
// like any other, and so kept in admin.store when the config is kept there.
// See docs/decisions/028-agent-and-tunnel.md.
//
// Applying a config, and Edit's work, run on the control plane, never on a
// fib worker.
package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/lesismal/fib"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/observe"
)

// Role is a running role.
type Role interface {
	// Apply moves the role to member, its member of the config being
	// applied: a *config.TunnelServer for tunnel.server. The first Apply
	// starts it. Configs are applied one at a time, on the control plane;
	// Apply may bind addresses and start dials, and returns once they are
	// under way. An error leaves the role as it was before, and is
	// reported with the config's other warnings: the rest of the config
	// still runs. Apply must not wait for an Edit of its own, which waits
	// for the apply.
	Apply(member any) error
	// Stop ends the role: its bindings are removed and its connections
	// closed, waiting for what is under way until ctx ends at most.
	Stop(ctx context.Context)
}

// Metered is a role with metrics of its own, which the Admin API's
// /metrics and OpenTelemetry's export carry with fit's.
type Metered interface {
	// WriteMetrics writes the role's metrics to w. It is called from a
	// scrape's goroutine, beside the role's work, and must not wait for an
	// Apply.
	WriteMetrics(w observe.MetricWriter)
}

// Info describes a role.
type Info struct {
	// ID is the path of the role's member in the config, its parts joined
	// with dots, as in tunnel.server.
	ID string
	// Member returns the role's member of c, or nil when c leaves it out.
	Member func(c *config.Config) any
	// New returns the role, not started yet: its first Apply starts it.
	New func(h *Host) (Role, error)
	// Check, when not nil, checks the role's member, when c sets it,
	// beyond what loading the config does, as when the config is compiled.
	Check func(c *config.Config) error
}

// Path returns the parts of the role's member's path, as in
// ["tunnel", "server"].
func (i Info) Path() []string { return strings.Split(i.ID, ".") }

var (
	mu    sync.RWMutex
	roles []Info
)

// Register adds a role. It panics if the ID is taken or Info is missing a
// func, which are mistakes in the role's code.
func Register(info Info) {
	if info.ID == "" || info.Member == nil || info.New == nil {
		panic(fmt.Sprintf("app: role %q lacks an ID, Member or New", info.ID))
	}
	mu.Lock()
	defer mu.Unlock()
	if slices.ContainsFunc(roles, func(i Info) bool { return i.ID == info.ID }) {
		panic(fmt.Sprintf("app: role %s is registered twice", info.ID))
	}
	roles = append(roles, info)
	slices.SortFunc(roles, func(a, b Info) int { return strings.Compare(a.ID, b.ID) })
}

// Roles returns the roles registered, sorted by ID.
func Roles() []Info {
	mu.RLock()
	defer mu.RUnlock()
	return slices.Clone(roles)
}

// set reports whether member, as Info.Member returns it, is set: a nil
// pointer of the member's type is not.
func set(member any) bool {
	if member == nil {
		return false
	}
	switch m := member.(type) {
	case *config.AgentHub:
		return m != nil
	case *config.AgentNode:
		return m != nil
	case *config.AgentClient:
		return m != nil
	case *config.TunnelServer:
		return m != nil
	case *config.TunnelNode:
		return m != nil
	case *config.TunnelClient:
		return m != nil
	case *config.TURNServer:
		return m != nil
	case *config.WebRTC:
		return m != nil
	case *config.WebRTCProxy:
		return m != nil
	}
	return true
}

// Check checks c's members of every role registered, and refuses a member
// of a role this binary lacks.
func Check(c *config.Config) error {
	var errs config.Errors
	add := func(err error) {
		var ce config.Errors
		var e *config.Error
		switch {
		case errors.As(err, &ce):
			errs = append(errs, ce...)
		case errors.As(err, &e):
			errs = append(errs, e)
		default:
			errs = append(errs, &config.Error{Msg: err.Error()})
		}
	}
	have := Roles()
	for _, m := range members(c) {
		i := slices.IndexFunc(have, func(i Info) bool { return i.ID == m.id })
		if i < 0 {
			errs = append(errs, m.at.Errorf("this fit was built without the %s role", m.id))
			continue
		}
		if have[i].Check != nil {
			if err := have[i].Check(c); err != nil {
				add(err)
			}
		}
	}
	if len(errs) > 0 {
		return errs
	}
	return nil
}

type memberAt struct {
	id string
	at config.Value
}

// members returns the roles c sets, whether or not this binary has them.
func members(c *config.Config) []memberAt {
	var ms []memberAt
	if a := c.Agent; a != nil {
		if a.Hub != nil {
			ms = append(ms, memberAt{"agent.hub", a.Hub.At})
		}
		if a.Node != nil {
			ms = append(ms, memberAt{"agent.node", a.Node.At})
		}
		if a.Client != nil {
			ms = append(ms, memberAt{"agent.client", a.Client.At})
		}
	}
	if t := c.Tunnel; t != nil {
		if t.Server != nil {
			ms = append(ms, memberAt{"tunnel.server", t.Server.At})
		}
		if t.Node != nil {
			ms = append(ms, memberAt{"tunnel.node", t.Node.At})
		}
		if t.Client != nil {
			ms = append(ms, memberAt{"tunnel.client", t.Client.At})
		}
	}
	if w := c.WebRTC; w != nil && w.TURN != nil {
		ms = append(ms, memberAt{"webrtc.turn", w.TURN.At})
	}
	if w := c.WebRTC; w != nil && w.Media != nil {
		ms = append(ms, memberAt{"webrtc.media", w.Media.At})
	}
	if w := c.WebRTC; w != nil && w.Proxy != nil {
		ms = append(ms, memberAt{"webrtc.proxy", w.Proxy.At})
	}
	return ms
}

// Editor changes the running base config: it applies the config edit makes
// of the member at path, as source, from the member as it is now, JSON or
// nil when the base config leaves it out, and returns the warnings applying
// it gave. admin.Configs.EditMember is one.
type Editor func(source string, path []string, edit func(cur []byte) ([]byte, error)) (warnings []string, err error)

// Host is what a role is given: the engine it shares, its log, and the way
// to change its own member of the config.
type Host struct {
	// ID is the role's, as in tunnel.server.
	ID string
	// Engine is the listeners' engine, which the role adds its bindings to
	// and dials its connections on, with handlers of their own.
	Engine *fib.Engine
	// Upstream is the engine fit's upstream pools dial through, for an
	// HTTP client of fib's to dial on.
	Upstream *fib.Engine
	// Log is the role's logger: agent, tunnel or webrtc, its messages
	// prefixed with its ID.
	Log *logs.Logger

	m *Manager
}

// ErrBusy is an Edit refused because too many are waiting already.
var ErrBusy = errors.New("app: too many config changes are waiting; try again")

// ErrStopped is an Edit refused because fit is shutting down.
var ErrStopped = errors.New("app: fit is shutting down")

// Edit changes the role's member of the running config with edit, which is
// given the member as it is now, as JSON, and returns it changed; the config
// with it is applied as one from the role's web pages, its Apply called
// with the member decoded, and kept in admin.store when there is one. It
// returns at once: the change is made on a goroutine of the Manager's own,
// one at a time, and done, which may be nil, is called there with the
// warnings and the error applying it gave. A config that does not load or
// compile is not applied, and done is given its errors.
func (h *Host) Edit(edit func(cur []byte) ([]byte, error), done func(warnings []string, err error)) {
	if done == nil {
		done = func([]string, error) {}
	}
	h.m.edit(h.ID, edit, done)
}

// MaxEdits is how many Edits may wait at once, across the roles.
const MaxEdits = 64

type editReq struct {
	id   string
	edit func([]byte) ([]byte, error)
	done func([]string, error)
}

// Manager runs the roles a config sets.
type Manager struct {
	engine, upstream *fib.Engine
	root             *logs.Root
	editor           Editor

	mu      sync.Mutex // held for a whole Apply
	running map[string]Role
	// metered is the running roles that have metrics, for scrapes that
	// must not wait for an Apply.
	metered atomic.Pointer[[]Metered]

	edits   chan editReq
	stop    chan struct{}
	stopped sync.WaitGroup
	closing sync.Once
}

// NewManager returns a Manager that runs roles on engine, whose HTTP clients
// dial through upstream, logging to root, which may be nil, and changing
// the config through editor. It runs no role until Apply.
func NewManager(engine, upstream *fib.Engine, root *logs.Root, editor Editor) *Manager {
	m := &Manager{engine: engine, upstream: upstream, root: root, editor: editor,
		running: map[string]Role{}, edits: make(chan editReq, MaxEdits), stop: make(chan struct{})}
	m.stopped.Add(1)
	go m.runEdits()
	return m
}

func (m *Manager) edit(id string, edit func([]byte) ([]byte, error), done func([]string, error)) {
	select {
	case <-m.stop:
		done(nil, ErrStopped)
		return
	default:
	}
	select {
	case m.edits <- editReq{id: id, edit: edit, done: done}:
	default:
		done(nil, ErrBusy)
	}
}

// runEdits makes the changes Edit asked for, one at a time.
func (m *Manager) runEdits() {
	defer m.stopped.Done()
	for {
		select {
		case <-m.stop:
			for {
				select {
				case r := <-m.edits:
					r.done(nil, ErrStopped)
				default:
					return
				}
			}
		case r := <-m.edits:
			if m.editor == nil {
				r.done(nil, errors.New("app: the config cannot be changed here"))
				continue
			}
			ws, err := m.editor(r.id, strings.Split(r.id, "."), r.edit)
			r.done(ws, err)
		}
	}
}

// Apply moves the roles to c: it starts each role c sets that is not
// running, applies c to each running one, and stops each c leaves out. Each
// role's error is returned, joined, beside the others; the roles without
// one run c.
func (m *Manager) Apply(c *config.Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var errs []error
	for _, info := range Roles() {
		member := info.Member(c)
		r := m.running[info.ID]
		if !set(member) {
			if r != nil {
				r.Stop(context.Background())
				delete(m.running, info.ID)
			}
			continue
		}
		if r == nil {
			var err error
			if r, err = info.New(m.host(info.ID)); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", info.ID, err))
				continue
			}
			if err := r.Apply(member); err != nil {
				r.Stop(context.Background())
				errs = append(errs, fmt.Errorf("%s: %w", info.ID, err))
				continue
			}
			m.running[info.ID] = r
			continue
		}
		if err := r.Apply(member); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w; it runs as it did", info.ID, err))
		}
	}
	m.noteMetered()
	return errors.Join(errs...)
}

// noteMetered records the running roles that have metrics. m.mu is held.
func (m *Manager) noteMetered() {
	var ms []Metered
	for _, id := range slices.Sorted(maps.Keys(m.running)) {
		if mt, ok := m.running[id].(Metered); ok {
			ms = append(ms, mt)
		}
	}
	m.metered.Store(&ms)
}

// WriteMetrics writes the metrics of the running roles that have them, in
// the order of their IDs.
func (m *Manager) WriteMetrics(w observe.MetricWriter) {
	if ms := m.metered.Load(); ms != nil {
		for _, mt := range *ms {
			mt.WriteMetrics(w)
		}
	}
}

func (m *Manager) host(id string) *Host {
	h := &Host{ID: id, Engine: m.engine, Upstream: m.upstream, m: m}
	if m.root != nil {
		name := config.LoggerAgent
		switch {
		case strings.HasPrefix(id, "tunnel."):
			name = config.LoggerTunnel
		case strings.HasPrefix(id, "webrtc."):
			name = config.LoggerWebRTC
		}
		h.Log = m.root.Logger(name).Prefixed(id + ": ")
	}
	return h
}

// Running returns the IDs of the roles running, sorted.
func (m *Manager) Running() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.running))
	for id := range m.running {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// Stop stops every role, waiting for each until ctx ends at most, and
// refuses Edits from then on. Stop it before the engine.
func (m *Manager) Stop(ctx context.Context) {
	m.closing.Do(func() { close(m.stop) })
	m.stopped.Wait()
	m.mu.Lock()
	defer m.mu.Unlock()
	var wg sync.WaitGroup
	for id, r := range m.running {
		wg.Go(func() { r.Stop(ctx) })
		delete(m.running, id)
	}
	wg.Wait()
	m.noteMetered()
}
