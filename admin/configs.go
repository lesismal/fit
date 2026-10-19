// Package admin is fit's control plane for config: the path every new config
// takes to the running server, the history of the configs applied, and the
// Admin API that reads and replaces them.
//
// Nothing here runs on a fib worker. Configs are applied on the goroutine of
// whoever asks, one at a time, and the Admin API is served by net/http on
// goroutines of its own; see docs/decisions/005-admin-api.md.
package admin

import (
	"slices"
	"sync"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/listener"
	"github.com/lesismal/fit/runtime"
)

// Sources a config can come from.
const (
	SourceFile  = "file"
	SourceAdmin = "admin"
)

// Configs applies configs to a running server, one at a time and by one
// path whatever their source: SIGHUP, the Admin API and, later, the file
// provider. It keeps the configs it applied, numbered by the versions the
// runtime gave their graphs.
type Configs struct {
	rt     *runtime.Runtime
	server *listener.Server
	env    runtime.Env
	// first is the config fit started with, whose access log and Admin API
	// stay until a restart.
	first *config.Config
	keep  int

	mu      sync.Mutex // held for a whole Apply, so that applies follow one another
	history []*Version // oldest first; the last is the running config
}

// Version is a config that was applied.
type Version struct {
	// Number is the version the runtime gave the config's graph.
	Number uint64 `json:"version"`
	// Applied is when the config was applied, and Source where it came from.
	Applied time.Time `json:"applied"`
	Source  string    `json:"source"`
	// Bytes is the size of the config as written.
	Bytes int `json:"bytes"`

	data []byte
}

// Data returns the config as it was written. The caller must not change it.
func (v *Version) Data() []byte { return v.data }

// Result is what applying a config did.
type Result struct {
	Version uint64
	Source  string
	// Added and Removed are the listeners bound and those taken away.
	Added, Removed []listener.Addr
	// Warnings are changes in the config that take effect only when fit
	// restarts.
	Warnings []string
}

// NewConfigs returns the Configs of server, whose runtime rt is running
// first, compiled from data, as its current graph. Compiling a config takes
// env, and keep is how many configs, the running one included, are kept to
// be read back.
func NewConfigs(rt *runtime.Runtime, server *listener.Server, env runtime.Env, first *config.Config, data []byte, source string, keep int) *Configs {
	cs := &Configs{rt: rt, server: server, env: env, first: first, keep: max(keep, 1)}
	var version uint64
	if g := rt.Current(); g != nil {
		version = g.Version
		g.Release()
	}
	cs.record(version, data, source)
	return cs
}

func (cs *Configs) record(version uint64, data []byte, source string) {
	cs.history = append(cs.history, &Version{Number: version, Applied: time.Now(), Source: source, Bytes: len(data), data: data})
	if n := len(cs.history) - cs.keep; n > 0 {
		clear(cs.history[:n])
		cs.history = slices.Delete(cs.history, 0, n)
	}
}

// Apply loads data as a config, compiles it and moves the server to it, as
// one step: on any failure the running config stays. A config that does not
// load or compile gives config.Errors; a listener that cannot be bound gives
// the error that said so. Apply keeps data, which the caller must not change
// afterwards.
func (cs *Configs) Apply(data []byte, source string) (Result, error) {
	c, err := config.Load(data)
	if err != nil {
		return Result{}, err
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	g, err := runtime.Compile(c, cs.env)
	if err != nil {
		return Result{}, err
	}
	added, removed, err := cs.server.Reload(g)
	if err != nil {
		return Result{}, err
	}
	// The runtime holds g now, and only Apply swaps graphs while fit runs,
	// so g is still current.
	r := Result{Version: g.Version, Source: source, Added: added, Removed: removed}
	if !sameAccessLog(cs.first.AccessLog, c.AccessLog) {
		r.Warnings = append(r.Warnings, "access_log changed; the change takes effect when fit restarts")
	}
	if !sameAdmin(cs.first.Admin, c.Admin) {
		r.Warnings = append(r.Warnings, "admin changed; the change takes effect when fit restarts")
	}
	cs.record(r.Version, data, source)
	return r, nil
}

// Current returns the running config.
func (cs *Configs) Current() *Version {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.history[len(cs.history)-1]
}

// Versions returns the configs kept, oldest first.
func (cs *Configs) Versions() []*Version {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return slices.Clone(cs.history)
}

// Version returns the config applied as version n, if it is still kept.
func (cs *Configs) Version(n uint64) (*Version, bool) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	for _, v := range cs.history {
		if v.Number == n {
			return v, true
		}
	}
	return nil, false
}

func sameAccessLog(a, b *config.AccessLog) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Output == b.Output && a.FlushInterval == b.FlushInterval && a.BufferBytes == b.BufferBytes
}

func sameAdmin(a, b *config.Admin) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Addr() == b.Addr() && a.TokenFile == b.TokenFile && a.HistoryLen() == b.HistoryLen() &&
		a.ConfigBytes() == b.ConfigBytes() && a.Conns() == b.Conns()
}
