// Package role is what the LLM feature's three roles share: their identity, their
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
	"net"
	"net/netip"
	"os"
	"runtime/debug"
	"sync"
	"time"

	"github.com/lesismal/fit/access"
	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/app/console"
	"github.com/lesismal/fit/app/workbench"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/certpin"
	"github.com/lesismal/fit/internal/linktoken"
	"github.com/lesismal/fit/llm/internal/mesh"
	"github.com/lesismal/fit/llm/internal/model"
	"github.com/lesismal/fit/llm/internal/pool"
	"github.com/lesismal/fit/llm/internal/webui"
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
	// Role is model.RoleHub, model.RoleNode or model.RoleProxy.
	Role string
	// Me adds to what GET /api/me answers.
	Me func() map[string]any
}

// Common are the fields every role's member has.
type Common struct {
	ID      string
	Name    string
	Console *config.RoleConsole
	Listen  *config.LLMListen
	Tokens  []config.LinkToken
	Peers   []config.LLMPeer
	Limits  *config.LLMLimits
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

	module *console.Module
	// tokens issues and revokes the listener's link tokens.
	tokens *linktoken.Manager
	// gate is the access the role's listeners take connections by: the
	// member's own, then the global one. ApplyAccess sets it on every
	// Apply; the bindings' Filter, Allows, asks it.
	gate access.Gate

	mu         sync.Mutex
	id, name   string
	console    *console.Console
	consoleKey []byte
	lim        *config.LLMLimits
	// generated is the self-signed certificate and key made for the
	// listener, until the config has them.
	generated [2]string
	applied   bool
	stopped   bool
}

// Version is the version a role announces.
var Version = version(debug.ReadBuildInfo())

// version is the version a build with bi reports.
func version(bi *debug.BuildInfo, ok bool) string {
	if ok && bi.Main.Version != "" {
		return "fit " + bi.Main.Version
	}
	return "fit"
}

// New returns the base of a role on h.
func New(h *app.Host, spec Spec) *Base {
	host, _ := os.Hostname()
	b := &Base{Spec: spec, H: h, Log: h.Log, Router: mesh.NewRouter(), Pool: pool.New(Workers, Queue),
		started: time.Now().Unix(), hostname: host}
	b.Mesh = mesh.New(mesh.Options{Self: b.Self, AcceptRoles: config.LLMPeerRoles(spec.Role), Engine: h.Engine, Log: h.Log, Filter: b.Allows}, b.Router)
	mesh.RegisterAdmin(b.Router, b.Mesh, b.EditMesh)
	b.registerSettings()
	b.setupTokens()
	b.module = &console.Module{ID: "llm." + spec.Role, Routes: b.routes}
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
	b.lim = c.Limits
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
		listen = listenConfig(c.Listen)
		if err := b.selfSigned(&listen); err != nil {
			b.Log.Warnf("making the listener's self-signed certificate: %v", err)
		}
	}
	peers := make([]model.DialReq, 0, len(c.Peers))
	for _, p := range c.Peers {
		peers = append(peers, dialReq(p))
	}
	// The tokens before the listener, so that a listener with tokens alone
	// says so only when it has none.
	b.Mesh.Tokens().Set(c.Tokens)
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
			Config: cfg, Cookie: "fit_llm_" + b.Role, Pages: workbench.Pages(), Log: b.Log,
			Routes: b.routes, Modules: func() []*console.Module { return []*console.Module{b.module} },
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

// routes adds the role's API to a console.
func (b *Base) routes(api *console.API) {
	webui.Register(api, webui.Options{Router: b.Router, Pool: b.Pool, Me: b.me})
}

// Module returns the role's module, which the workbench shows.
func (b *Base) Module() *console.Module { return b.module }

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
	b.Mesh.Tokens().Stop()
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
			var l config.LLMListen
			if err := json.Unmarshal(raw, &l); err != nil {
				return err
			}
			lc := listenConfig(&l)
			s.Listen = &lc
		}
		if raw, ok := m["peers"]; ok {
			var ps []config.LLMPeer
			if err := json.Unmarshal(raw, &ps); err != nil {
				return err
			}
			for _, p := range ps {
				s.Peers = append(s.Peers, dialReq(p))
			}
		}
		if err := fn(&s); err != nil {
			return err
		}
		if s.Listen == nil {
			delete(m, "listen")
		} else {
			l := s.Listen
			m["listen"], _ = json.Marshal(config.LLMListen{Address: l.Addr, Password: l.Password, NodePassword: l.NodePassword,
				ProxyPassword: l.ProxyPassword, HubPassword: l.HubPassword, CertFile: l.CertFile, KeyFile: l.KeyFile,
				SelfSigned: l.SelfSigned, Cert: l.Cert, Key: l.Key})
		}
		peers := make([]config.LLMPeer, 0, len(s.Peers))
		for _, p := range s.Peers {
			peers = append(peers, config.LLMPeer{Address: p.Addr, Password: p.Password, Token: p.Token, Role: p.Role, Pin: p.Pin,
				CAFile: p.CAFile, ServerName: p.ServerName})
		}
		return Set(m, "peers", peers)
	})
}

// listenConfig is the mesh's listener of l.
func listenConfig(l *config.LLMListen) model.ListenConfig {
	return model.ListenConfig{Addr: l.Address, Password: l.Password, NodePassword: l.NodePassword,
		ProxyPassword: l.ProxyPassword, HubPassword: l.HubPassword, CertFile: l.CertFile, KeyFile: l.KeyFile,
		SelfSigned: l.SelfSigned, Cert: l.Cert, Key: l.Key}
}

// dialReq is the mesh's dial target of p.
func dialReq(p config.LLMPeer) model.DialReq {
	return model.DialReq{Addr: p.Address, Password: p.Password, Token: p.Token, Role: p.Role, Pin: p.Pin, CAFile: p.CAFile,
		ServerName: p.ServerName}
}

// ListenCertFiles is the certificate and key files l has its listener read,
// and the list files of the role's own access a, which fit run's
// -watch-certs has applied again when they change.
func ListenCertFiles(l *config.LLMListen, a *config.Access) []string {
	out := access.Files(a)
	if l == nil || l.CertFile == "" {
		return out
	}
	return append(out, l.CertFile, l.KeyFile)
}

// ApplyAccess compiles a, the role's own access, and has the role's
// listeners go by it, then by the global access, for the connections they
// accept from now on; those open stay. A role calls it first in every
// Apply, its member changed or not, for the global access may have
// changed; its error fails the Apply, and the role goes on by the access it
// had.
func (b *Base) ApplyAccess(a *config.Access) error {
	p, err := b.H.Access(a)
	if err != nil {
		return fmt.Errorf("access: %w", err)
	}
	b.gate.Set(p)
	return nil
}

// Allows reports whether the role's access serves peer: the Filter of the
// role's bindings.
func (b *Base) Allows(peer netip.AddrPort) bool { return b.gate.Allows(peer) }

// selfSigned gives a self_signed listener without a certificate one: made
// once, used at once, and kept so that it outlives a restart and the pins of
// the peers stay good: without admin.store in a file beside the config
// (app.Host.KeptFile), made there the first time, and otherwise written
// into the config, which the store keeps. Until the config has it, the same
// one is used.
func (b *Base) selfSigned(l *model.ListenConfig) error {
	b.mu.Lock()
	if !l.SelfSigned || l.CertFile != "" || l.Cert != "" {
		if l.Cert != "" {
			b.generated = [2]string{}
		}
		b.mu.Unlock()
		return nil
	}
	made := b.generated[0] == ""
	kept := ""
	if made {
		hosts := []string{"localhost"}
		if b.hostname != "" {
			hosts = append(hosts, b.hostname)
		}
		if host, _, err := net.SplitHostPort(l.Addr); err == nil && host != "" && host != "localhost" && host != b.hostname {
			hosts = append(hosts, host)
		}
		var c, k []byte
		var err error
		var madeKept bool
		if kept = b.H.KeptFile("pem"); kept != "" {
			c, k, madeKept, err = certpin.Keep(kept, "fit llm "+b.Role, hosts...)
			if err != nil && c != nil {
				// Not written: kept in the config, as with a store, which
				// keeps it only as long as fit runs.
				b.Log.Warnf("the listener's self-signed certificate cannot be kept in %s, so it changes when fit restarts, and the peers' pins of it fail: %v", kept, err)
				kept, err = "", nil
			}
		} else {
			c, k, err = certpin.SelfSigned("fit llm "+b.Role, hosts...)
		}
		if err != nil {
			b.mu.Unlock()
			return err
		}
		b.generated = [2]string{string(c), string(k)}
		if kept != "" {
			verb := "the listener's self-signed certificate is"
			if madeKept {
				verb = "made the listener's self-signed certificate,"
			}
			b.Log.Infof("%s kept in %s", verb, kept)
		}
	}
	cert, key := b.generated[0], b.generated[1]
	b.mu.Unlock()
	l.Cert, l.Key = cert, key
	if made && kept == "" {
		b.H.Edit(func(cur []byte) ([]byte, error) {
			return EditJSON(cur, func(m map[string]json.RawMessage) error {
				var lm map[string]json.RawMessage
				if err := json.Unmarshal(m["listen"], &lm); err != nil || lm == nil {
					return nil
				}
				var have string
				var self bool
				_ = json.Unmarshal(lm["cert"], &have)
				_ = json.Unmarshal(lm["self_signed"], &self)
				if have != "" || !self {
					return nil
				}
				lm["cert"], _ = json.Marshal(cert)
				lm["key"], _ = json.Marshal(key)
				m["listen"], _ = json.Marshal(lm)
				return nil
			})
		}, func(_ []string, err error) {
			if err != nil {
				b.Log.Warnf("keeping the listener's self-signed certificate in the config: %v", err)
			}
		})
	}
	return nil
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
