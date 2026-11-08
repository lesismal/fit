// Package app runs the roles of the apps fit carries beside its gateway: the
// LLM feature's hub, node and proxy, the tunnel's hub, node and proxy, and
// WebRTC's TURN server, media server and media proxy.
// Each role is a member of fit's config, llm.hub or tunnel.hub, and runs
// while the config sets it, on the listeners' fib engine, which every role
// in the process shares with the gateway.
//
// A role registers itself with Register in an init function, as a module
// does; package app/standard imports every role fit ships with. The Manager
// starts a role when a config applied sets its member, moves it to each
// config after, and stops it when one leaves it out. What a role's web pages
// change is written back into its member with Host.Edit, as a config applied
// like any other, and so kept in admin.store when the config is kept there.
// See docs/decisions/028-llm-and-tunnel.md.
//
// Applying a config, and Edit's work, run on the control plane, never on a
// fib worker.
package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lesismal/fib"

	"github.com/lesismal/fit/access"
	"github.com/lesismal/fit/app/console"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/linktoken"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/observe"
)

// Role is a running role.
type Role interface {
	// Apply moves the role to member, its member of the config being
	// applied: a *config.TunnelHub for tunnel.hub. The first Apply
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

// Paged is a role with web pages: the workbench shows its module beside the
// other roles', on admin.console as on the role's own console.
type Paged interface {
	// Module returns the role's module, the same one for the role's life.
	Module() *console.Module
}

// Info describes a role.
type Info struct {
	// ID is the path of the role's member in the config, its parts joined
	// with dots, as in tunnel.hub.
	ID string
	// Member returns the role's member of c, or nil when c leaves it out.
	Member func(c *config.Config) any
	// New returns the role, not started yet: its first Apply starts it.
	New func(h *Host) (Role, error)
	// Check, when not nil, checks the role's member, when c sets it,
	// beyond what loading the config does, as when the config is compiled.
	Check func(c *config.Config) error
	// CertFiles, when not nil, returns the certificate and key files the
	// role's member of c, when c sets it, has the role read when it is
	// applied: fit run's -watch-certs applies the running config again
	// when they change, as it does for the listeners' (see CertFiles).
	CertFiles func(c *config.Config) []string
}

// CertFiles is the certificate and key files the members of c have their
// roles read, those of every role registered.
func CertFiles(c *config.Config) []string {
	var out []string
	for _, info := range Roles() {
		if info.CertFiles != nil && set(info.Member(c)) {
			out = append(out, info.CertFiles(c)...)
		}
	}
	return out
}

// Path returns the parts of the role's member's path, as in
// ["tunnel", "hub"].
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
	case *config.LLMHub:
		return m != nil
	case *config.LLMNode:
		return m != nil
	case *config.LLMProxy:
		return m != nil
	case *config.TunnelHub:
		return m != nil
	case *config.TunnelNode:
		return m != nil
	case *config.TunnelProxy:
		return m != nil
	case *config.TURNServer:
		return m != nil
	case *config.WebRTC:
		return m != nil
	case *config.WebRTCProxy:
		return m != nil
	case *config.Streaming:
		return m != nil
	case *config.Sync:
		return m != nil
	case *config.BitTorrentClient:
		return m != nil
	case *config.BitTorrentTracker:
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
	if a := c.LLM; a != nil {
		if a.Hub != nil {
			ms = append(ms, memberAt{"llm.hub", a.Hub.At})
		}
		if a.Node != nil {
			ms = append(ms, memberAt{"llm.node", a.Node.At})
		}
		if a.Proxy != nil {
			ms = append(ms, memberAt{"llm.proxy", a.Proxy.At})
		}
	}
	if t := c.Tunnel; t != nil {
		if t.Hub != nil {
			ms = append(ms, memberAt{"tunnel.hub", t.Hub.At})
		}
		if t.Node != nil {
			ms = append(ms, memberAt{"tunnel.node", t.Node.At})
		}
		if t.Proxy != nil {
			ms = append(ms, memberAt{"tunnel.proxy", t.Proxy.At})
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
	if len(c.MediaAuth) > 0 {
		ms = append(ms, memberAt{"media_auth", c.MediaAuth[0].At})
	}
	if c.Streaming != nil {
		ms = append(ms, memberAt{"streaming", c.Streaming.At})
	}
	if len(c.MediaLibrary) > 0 {
		ms = append(ms, memberAt{"media_library", c.MediaLibrary[0].At})
	}
	if c.Sync != nil {
		ms = append(ms, memberAt{"sync", c.Sync.At})
	}
	if b := c.BitTorrent; b != nil && b.Client != nil {
		ms = append(ms, memberAt{"bittorrent.client", b.Client.At})
	}
	if b := c.BitTorrent; b != nil && b.Tracker != nil {
		ms = append(ms, memberAt{"bittorrent.tracker", b.Tracker.At})
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
	// ID is the role's, as in tunnel.hub.
	ID string
	// Engine is the listeners' engine, which the role adds its bindings to
	// and dials its connections on, with handlers of their own.
	Engine *fib.Engine
	// Upstream is the engine fit's upstream pools dial through, for an
	// HTTP client of fib's to dial on.
	Upstream *fib.Engine
	// Log is the role's logger: llm, tunnel or webrtc, its messages
	// prefixed with its ID.
	Log *logs.Logger
	// Admin is the admin logger, where the role records what its managers
	// change, as the tokens it issues.
	Admin *logs.Logger

	m *Manager
}

// Access compiles a role's own access, own, and returns its policy: own,
// then the config's global access, as the config being applied has it. A
// role calls it on every Apply, for the global access may have changed when
// its own member has not, and sets the result on the access.Gate its
// bindings' Filter asks; nil serves everyone.
func (h *Host) Access(own *config.Access) (*access.Policy, error) {
	l, err := access.Compile(own, nil)
	if err != nil {
		return nil, err
	}
	var global *access.List
	if h.m != nil {
		global = h.m.global.Load()
	}
	return access.Chain(l, global), nil
}

// DefaultDrainTimeout is how long a role may let what a server it replaced
// holds run out, unless the Manager's SetDrainTimeout says otherwise: the
// listeners' default.
const DefaultDrainTimeout = 30 * time.Second

// DrainTimeout is how long a role may let what a server it replaced holds
// run out, as a listener a reload removed finishes its requests: fit run's
// -shutdown-timeout.
func (h *Host) DrainTimeout() time.Duration {
	if h.m != nil {
		if d := time.Duration(h.m.drain.Load()); d > 0 {
			return d
		}
	}
	return DefaultDrainTimeout
}

// KeptFile is the file the role keeps what it made of name in, such as a
// self-signed certificate whose fingerprint its peers pin, beside the
// config: prefix.ID.name, prefix being the Manager's KeepBeside. It is ""
// when there is none, as when the config is kept in admin.store: the role
// then writes what it made into its member, through Edit, which the store
// keeps.
func (h *Host) KeptFile(name string) string {
	if h.m == nil {
		return ""
	}
	p := h.m.kept.Load()
	if p == nil || *p == "" {
		return ""
	}
	return *p + "." + h.ID + "." + name
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
	// modules is the running roles' modules, for consoles that must not
	// wait for an Apply.
	modules atomic.Pointer[[]*console.Module]

	// drain is the roles' drain timeout, in nanoseconds; 0 for the default.
	drain atomic.Int64
	// kept is KeepBeside's prefix of the files the roles keep.
	kept atomic.Pointer[string]
	// global is the config's access, for the roles' policies; nil when it
	// sets none.
	global atomic.Pointer[access.List]

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

// SetDrainTimeout sets how long a role may let what a server it replaced
// holds run out, as the listeners' drain timeout does for their requests.
func (m *Manager) SetDrainTimeout(d time.Duration) { m.drain.Store(int64(d)) }

// KeepBeside has the roles keep what they make that must outlive a restart,
// and that the config, without admin.store, would not keep, in files
// beside the config at path: a config file, whose files are named after
// it without its extension, as /etc/fit/fit.tunnel.hub.pem beside
// /etc/fit/fit.json, or a directory of them, whose files are fit.* in it.
// "" or "-", standard input, keeps none. Call it before Apply.
func (m *Manager) KeepBeside(path string) {
	prefix := BesidePrefix(path)
	m.kept.Store(&prefix)
}

// BesidePrefix is what the names of the files kept beside the config at path
// begin with: the config file's path without its extension, as /etc/fit/fit
// for /etc/fit/fit.json, or fit in a directory of config files; "" for ""
// or "-", standard input, beside which nothing is kept.
func BesidePrefix(path string) string {
	if path == "" || path == "-" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if fi, err := os.Stat(path); err == nil && fi.IsDir() {
		return filepath.Join(path, "fit")
	}
	return strings.TrimSuffix(path, filepath.Ext(path))
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
	// The global access, which every role's policy ends with: set before
	// the roles apply, which take it into their gates. Compiling the
	// listeners' config checked it; a file that has gone bad since is
	// reported, and the roles go by none.
	global, err := access.Compile(c.Access, nil)
	if err != nil {
		errs = append(errs, fmt.Errorf("access: %w", err))
	}
	m.global.Store(global)
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

// noteMetered records the running roles that have metrics, and their
// modules. m.mu is held.
func (m *Manager) noteMetered() {
	var ms []Metered
	var mods []*console.Module
	for _, id := range slices.Sorted(maps.Keys(m.running)) {
		if mt, ok := m.running[id].(Metered); ok {
			ms = append(ms, mt)
		}
		if p, ok := m.running[id].(Paged); ok {
			mods = append(mods, p.Module())
		}
	}
	m.metered.Store(&ms)
	m.modules.Store(&mods)
}

// Modules returns the modules of the running roles that have pages, in the
// order of their IDs. It does not wait for an Apply.
func (m *Manager) Modules() []*console.Module {
	if p := m.modules.Load(); p != nil {
		return *p
	}
	return nil
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

// Host returns the Host m gives the role id, for a role made by hand, as
// tests make one, that goes by m's global access as m's roles do.
func (m *Manager) Host(id string) *Host { return m.host(id) }

func (m *Manager) host(id string) *Host {
	h := &Host{ID: id, Engine: m.engine, Upstream: m.upstream, m: m}
	if m.root != nil {
		name := config.LoggerLLM
		switch {
		case strings.HasPrefix(id, "tunnel."):
			name = config.LoggerTunnel
		case strings.HasPrefix(id, "webrtc."), id == "streaming", id == "media_auth", id == "media_library":
			name = config.LoggerWebRTC
		case id == "sync":
			name = config.LoggerSync
		case strings.HasPrefix(id, "bittorrent."):
			name = config.LoggerBitTorrent
		}
		h.Log = m.root.Logger(name).Prefixed(id + ": ")
		h.Admin = m.root.Logger(config.LoggerAdmin)
	}
	return h
}

// TokenIssuer is a role whose listener issues link tokens (decision 035):
// the tunnel's hub and the LLM feature's roles.
type TokenIssuer interface {
	LinkTokens() *linktoken.Manager
}

// ErrNoRole is the error of a role that is not running.
var ErrNoRole = errors.New("app: no such role is running")

// ErrNoTokens is the error of a role that issues no link tokens.
var ErrNoTokens = errors.New("app: the role issues no link tokens: only tunnel.hub and the llm roles do")

// LinkTokens returns the token manager of the running role id.
func (m *Manager) LinkTokens(id string) (*linktoken.Manager, error) {
	m.mu.Lock()
	r := m.running[id]
	m.mu.Unlock()
	if r == nil {
		return nil, ErrNoRole
	}
	ti, ok := r.(TokenIssuer)
	if !ok {
		return nil, ErrNoTokens
	}
	return ti.LinkTokens(), nil
}

// TokenRoles returns the IDs of the running roles that issue link tokens,
// sorted.
func (m *Manager) TokenRoles() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var ids []string
	for id, r := range m.running {
		if _, ok := r.(TokenIssuer); ok {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
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
