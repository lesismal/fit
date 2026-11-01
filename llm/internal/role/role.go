// Package role is what the agent's three roles share: their identity, their
// mesh and its write-back, their pages, and the pool their blocking work
// runs on. A role embeds a *Base, registers its methods on Router before its
// first Apply, and moves the base to each member it is applied.
package role

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"runtime/debug"
	"sync"
	"time"

	"github.com/lesismal/fit/agent/internal/mesh"
	"github.com/lesismal/fit/agent/internal/model"
	"github.com/lesismal/fit/agent/internal/pool"
	"github.com/lesismal/fit/agent/internal/webui"
	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/app/console"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
)

// The bounds of a role's pool, which runs its pages' methods and the
// methods its peers call that wait (a config change, a peer).
const (
	Workers = 16
	Queue   = 256
	// EditTimeout bounds the wait for a change of the config.
	EditTimeout = 30 * time.Second
)

// Spec says what a role is.
type Spec struct {
	// Role is model.RoleHub, model.RoleNode or model.RoleClient.
	Role string
	// Pages are the role's own files: index.html and app.js.
	Pages fs.FS
	// Me adds to what GET /api/me answers.
	Me func() map[string]any
}

// Common are the fields every role's member has.
type Common struct {
	ID      string
	Name    string
	Console *config.RoleConsole
	Listen  *config.AgentListen
	Peers   []config.AgentPeer
	Limits  *config.AgentLimits
}

// Base is the part of a role every role has.
type Base struct {
	Spec
	H      *app.Host
	Log    *logs.Logger
	Router *mesh.Router
	Mesh   *mesh.Mesh
	// Pool runs what waits: the pages' methods, and the methods peers call
	// that block.
	Pool *pool.Pool

	started  int64
	hostname string

	mu         sync.Mutex
	id, name   string
	console    *console.Console
	consoleKey []byte
	applied    bool
	stopped    bool
}

// Version is the version a role announces.
var Version = func() string {
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" {
		return "fit " + bi.Main.Version
	}
	return "fit"
}()

// New returns the base of a role on h.
func New(h *app.Host, spec Spec) *Base {
	host, _ := os.Hostname()
	b := &Base{Spec: spec, H: h, Log: h.Log, Router: mesh.NewRouter(), Pool: pool.New(Workers, Queue),
		started: time.Now().Unix(), hostname: host}
	b.Mesh = mesh.New(mesh.Options{Self: b.Self, AcceptRoles: config.AgentPeerRoles(spec.Role), Engine: h.Engine, Log: h.Log}, b.Router)
	mesh.RegisterAdmin(b.Router, b.Mesh, b.EditMesh)
	return b
}

// running are the roles running, by the engine they share and their name,
// for tests to find them.
var (
	runningMu sync.Mutex
	running   = map[runningKey]any{}
)

type runningKey struct {
	engine any
	role   string
}

// Register records owner, the role b is the base of, until Stop.
func (b *Base) Register(owner any) {
	runningMu.Lock()
	running[runningKey{b.H.Engine, b.Role}] = owner
	runningMu.Unlock()
}

// Find returns the role named role running on engine, or nil.
func Find(engine any, role string) any {
	runningMu.Lock()
	defer runningMu.Unlock()
	return running[runningKey{engine, role}]
}

// Self is the role's identity.
func (b *Base) Self() model.Identity {
	b.mu.Lock()
	id, name, c := b.id, b.name, b.console
	b.mu.Unlock()
	if name == "" {
		name = b.hostname
	}
	self := model.Identity{Role: b.Role, ID: id, Name: name, Hostname: b.hostname, Version: Version, PID: os.Getpid(),
		RPCAddr: advertise(b.Mesh.ListenAddr()), StartedAt: b.started}
	if c != nil {
		self.HTTPURL = c.URL()
	}
	return self
}

// NewID returns a new ID for a role: <role>-<16 hex digits>.
func NewID(role string) string {
	p := make([]byte, 8)
	_, _ = rand.Read(p)
	return role + "-" + hex.EncodeToString(p)
}

// Apply moves the base to c: the identity, the mesh and the pages. The
// first Apply of a member without an ID makes one, and writes it back.
func (b *Base) Apply(c Common) error {
	b.mu.Lock()
	if b.stopped {
		b.mu.Unlock()
		return errors.New("stopped")
	}
	first := !b.applied
	b.applied = true
	oldID := b.id
	if c.ID != "" {
		b.id = c.ID
	} else if b.id == "" {
		b.id = NewID(b.Role)
	}
	writeID := c.ID == "" && first
	id := b.id
	b.name = c.Name
	b.mu.Unlock()
	if oldID != "" && oldID != id {
		// Peers know the role by its ID: they say hello again.
		for _, p := range b.Mesh.Peers() {
			b.Mesh.Kick(p.ID)
		}
	}
	if writeID {
		b.H.Edit(func(cur []byte) ([]byte, error) {
			return EditJSON(cur, func(m map[string]json.RawMessage) error {
				if _, ok := m["id"]; !ok {
					m["id"], _ = json.Marshal(id)
				}
				return nil
			})
		}, func(ws []string, err error) {
			if err != nil {
				b.Log.Warnf("the ID %s could not be written into the config: %v", id, err)
			}
		})
	}
	l := c.Limits
	b.Mesh.SetLimits(mesh.Options{MaxConns: l.Conns(), MaxInflight: l.Inflight(), PingInterval: l.Ping(),
		SlowTimeout: l.Slow(), RetryInterval: l.Retry(), StreamIdle: l.Idle()})
	var listen model.ListenConfig
	if c.Listen != nil {
		listen = model.ListenConfig{Addr: c.Listen.Address, Password: c.Listen.Password}
	}
	peers := make([]model.DialReq, 0, len(c.Peers))
	for _, p := range c.Peers {
		peers = append(peers, model.DialReq{Addr: p.Address, Password: p.Password, Role: p.Role})
	}
	if err := b.Mesh.Configure(listen, peers); err != nil {
		b.Log.Warnf("%v", err)
	}
	return b.applyConsole(c.Console)
}

// applyConsole starts, changes or stops the pages.
func (b *Base) applyConsole(cfg *config.RoleConsole) error {
	var key []byte
	if cfg != nil {
		key, _ = json.Marshal(cfg)
	}
	b.mu.Lock()
	same := bytes.Equal(key, b.consoleKey) && (cfg == nil) == (b.console == nil)
	old := b.console
	b.mu.Unlock()
	if same {
		return nil
	}
	if old != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		old.Stop(ctx)
		cancel()
	}
	var c *console.Console
	if cfg != nil {
		var err error
		c, err = console.Start(b.H.Engine, console.Options{
			Config: cfg, Cookie: "fit_agent_" + b.Role, Pages: webui.Pages(b.Pages), Log: b.Log,
			Routes: func(api *console.API) {
				webui.Register(api, webui.Options{Router: b.Router, Pool: b.Pool, Me: b.me})
			},
		})
		if err != nil {
			b.mu.Lock()
			b.console, b.consoleKey = nil, nil
			b.mu.Unlock()
			return fmt.Errorf("console: %w", err)
		}
	}
	b.mu.Lock()
	b.console, b.consoleKey = c, key
	b.mu.Unlock()
	return nil
}

func (b *Base) me() map[string]any {
	out := map[string]any{"identity": b.Self()}
	if b.Me != nil {
		for k, v := range b.Me() {
			out[k] = v
		}
	}
	return out
}

// Console returns the pages, or nil.
func (b *Base) Console() *console.Console {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.console
}

// Stop drains the mesh until ctx ends, and stops the pages and the pool.
func (b *Base) Stop(ctx context.Context) {
	runningMu.Lock()
	if k := (runningKey{b.H.Engine, b.Role}); running[k] != nil {
		delete(running, k)
	}
	runningMu.Unlock()
	b.mu.Lock()
	b.stopped = true
	c := b.console
	b.console = nil
	b.mu.Unlock()
	if c != nil {
		c.Stop(ctx)
	}
	b.Mesh.Shutdown(ctx)
	b.Pool.Stop(ctx)
}

// Edit changes the role's member of the config with fn, which is given its
// JSON object's members, and waits until the config changed has been
// applied, or ctx ends. Call it off fib's workers: it waits.
func (b *Base) Edit(ctx context.Context, fn func(m map[string]json.RawMessage) error) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, EditTimeout)
		defer cancel()
	}
	done := make(chan error, 1)
	b.H.Edit(func(cur []byte) ([]byte, error) { return EditJSON(cur, fn) }, func(ws []string, err error) {
		for _, w := range ws {
			b.Log.Warnf("%s", w)
		}
		done <- err
	})
	select {
	case err := <-done:
		var ce config.Errors
		var e *config.Error
		if errors.As(err, &ce) || errors.As(err, &e) {
			return mesh.BadRequest("%v", err)
		}
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// EditJSON decodes the object cur, nil for none, has fn change its members,
// and encodes it again; the members fn leaves alone keep their bytes.
func EditJSON(cur []byte, fn func(m map[string]json.RawMessage) error) ([]byte, error) {
	m := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(cur)) > 0 && string(bytes.TrimSpace(cur)) != "null" {
		if err := json.Unmarshal(cur, &m); err != nil {
			return nil, err
		}
	}
	if err := fn(m); err != nil {
		return nil, err
	}
	return json.Marshal(m)
}

// Set sets m[key] to v, or deletes it when v is the zero value of its type
// and omit is set.
func Set(m map[string]json.RawMessage, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	switch string(b) {
	case "null", `""`, "[]", "{}", "false", "0":
		delete(m, key)
		return nil
	}
	m[key] = b
	return nil
}

// EditMesh is the mesh's Editor: it writes the listener and the peers.
func (b *Base) EditMesh(ctx context.Context, fn func(s *mesh.Settings) error) error {
	return b.Edit(ctx, func(m map[string]json.RawMessage) error {
		var s mesh.Settings
		if raw, ok := m["listen"]; ok {
			var l config.AgentListen
			if err := json.Unmarshal(raw, &l); err != nil {
				return err
			}
			s.Listen = &model.ListenConfig{Addr: l.Address, Password: l.Password}
		}
		if raw, ok := m["peers"]; ok {
			var ps []config.AgentPeer
			if err := json.Unmarshal(raw, &ps); err != nil {
				return err
			}
			for _, p := range ps {
				s.Peers = append(s.Peers, model.DialReq{Addr: p.Address, Password: p.Password, Role: p.Role})
			}
		}
		if err := fn(&s); err != nil {
			return err
		}
		if s.Listen == nil {
			delete(m, "listen")
		} else {
			m["listen"], _ = json.Marshal(map[string]string{"address": s.Listen.Addr, "password": s.Listen.Password})
		}
		peers := make([]map[string]string, 0, len(s.Peers))
		for _, p := range s.Peers {
			peers = append(peers, map[string]string{"address": p.Addr, "password": p.Password, "role": p.Role})
		}
		return Set(m, "peers", peers)
	})
}

// SetName writes the name into the config.
func (b *Base) SetName(ctx context.Context, name string) error {
	return b.Edit(ctx, func(m map[string]json.RawMessage) error { return Set(m, "name", name) })
}

// advertise turns a listen address into one others can use: an empty or
// unspecified host becomes the host's first IPv4 address that is not a
// loopback one.
func advertise(addr string) string {
	if addr == "" {
		return ""
	}
	return advertiseHost(addr)
}

// hostIP is the first non-loopback IPv4 address of the host, read once.
var hostIP = sync.OnceValue(func() string {
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && n.IP.To4() != nil {
				return n.IP.String()
			}
		}
	}
	return "127.0.0.1"
})

func advertiseHost(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if ip := net.ParseIP(host); host != "" && (ip == nil || !ip.IsUnspecified()) {
		return addr
	}
	return net.JoinHostPort(hostIP(), port)
}
