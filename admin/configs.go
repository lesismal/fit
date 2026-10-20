// Package admin is fit's control plane for config: the path every new config
// takes to the running server, the history of the configs applied, and the
// Admin API that reads and replaces them.
//
// Nothing here runs on a fib worker. Configs are applied on the goroutine of
// whoever asks, one at a time, and the Admin API is served by net/http on
// goroutines of its own; see docs/decisions/005-admin-api.md.
package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/listener"
	"github.com/lesismal/fit/runtime"
)

// Sources a config can come from.
const (
	SourceFile     = "file"
	SourceAdmin    = "admin"
	SourceRollback = "rollback"
	// SourceCertificates is the running config applied again because the
	// certificate files it loaded changed.
	SourceCertificates = "certificates"
)

// ErrChanged is what an update gets when the running config is no longer the
// one its If-Match names; the API answers it with 412.
var ErrChanged = errors.New("admin: the config has changed since the ETag given in If-Match")

// Configs applies configs to a running server, one at a time and by one
// path whatever their source: SIGHUP, the Admin API, the file provider and
// the watcher of certificate files. It keeps the configs it applied, numbered by the versions the
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

	// reloads and failures count the configs Update tried to apply, from
	// loading on, and those that failed.
	reloads, failures atomic.Uint64
}

// Version is a config that was applied.
type Version struct {
	// Number is the version the runtime gave the config's graph.
	Number uint64 `json:"version"`
	// Applied is when the config was applied, and Source where it came from.
	Applied time.Time `json:"applied"`
	Source  string    `json:"source"`
	// Bytes is the size of the config as written, and ETag a strong entity
	// tag of it, which If-Match compares.
	Bytes int    `json:"bytes"`
	ETag  string `json:"etag"`

	data []byte
}

// Data returns the config as it was written. The caller must not change it.
func (v *Version) Data() []byte { return v.data }

// Result is what applying a config did.
type Result struct {
	Version uint64
	ETag    string
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
	sum := sha256.Sum256(data)
	cs.history = append(cs.history, &Version{Number: version, Applied: time.Now(), Source: source, Bytes: len(data),
		ETag: `"` + hex.EncodeToString(sum[:12]) + `"`, data: data})
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
	return cs.Update(source, "", func(*Version) ([]byte, error) { return data, nil })
}

// Update applies the config edit makes from the running one, as Apply does,
// with no other config applied between the two: edits from many writers at
// once each start from the one before, and none is lost. When ifMatch is
// not empty, it is an If-Match field's value, and Update gives ErrChanged,
// calling nothing, unless it names the running config's ETag or is "*". An
// error from edit is returned as it is.
func (cs *Configs) Update(source, ifMatch string, edit func(cur *Version) ([]byte, error)) (Result, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cur := cs.history[len(cs.history)-1]
	if ifMatch != "" && !etagMatches(ifMatch, cur.ETag) {
		return Result{}, ErrChanged
	}
	data, err := edit(cur)
	if err != nil {
		return Result{}, err
	}
	cs.reloads.Add(1)
	r, c, err := cs.apply(data, source)
	if err != nil {
		cs.failures.Add(1)
		return Result{}, err
	}
	cs.record(r.Version, data, source)
	r.ETag = cs.history[len(cs.history)-1].ETag
	if !sameAccessLog(cs.first.AccessLog, c.AccessLog) {
		r.Warnings = append(r.Warnings, "access_log changed; the change takes effect when fit restarts")
	}
	if !sameAdmin(cs.first.Admin, c.Admin) {
		r.Warnings = append(r.Warnings, "admin changed; the change takes effect when fit restarts")
	}
	if !sameACME(cs.first.ACME, c.ACME) {
		r.Warnings = append(r.Warnings, "acme changed; the change takes effect when fit restarts")
	}
	return r, nil
}

// apply loads, compiles and applies data. cs.mu must be held.
func (cs *Configs) apply(data []byte, source string) (Result, *config.Config, error) {
	c, err := config.Load(data)
	if err != nil {
		return Result{}, nil, err
	}
	env := cs.env
	env.Previous = cs.rt.Current()
	g, err := runtime.Compile(c, env)
	if env.Previous != nil {
		env.Previous.Release()
	}
	if err != nil {
		return Result{}, nil, err
	}
	added, removed, err := cs.server.Reload(g)
	if err != nil {
		return Result{}, nil, err
	}
	// The runtime holds g now, and only Apply swaps graphs while fit runs,
	// so g is still current.
	return Result{Version: g.Version, Source: source, Added: added, Removed: removed}, c, nil
}

// Reloads returns how many configs were tried, from loading on, since fit
// started, and how many of them failed; the config fit started with is not
// counted.
func (cs *Configs) Reloads() (tried, failed uint64) { return cs.reloads.Load(), cs.failures.Load() }

// Reapply applies the running config again, as a new version, so that what
// it loads from files, its certificates, is loaded again.
func (cs *Configs) Reapply(source string) (Result, error) {
	return cs.Update(source, "", func(cur *Version) ([]byte, error) { return cur.data, nil })
}

// Rollback applies again the config applied as version n, as a new version.
// It gives a *RequestError with status 404 when n is no longer kept.
func (cs *Configs) Rollback(n uint64, ifMatch string) (Result, error) {
	return cs.Update(SourceRollback, ifMatch, func(*Version) ([]byte, error) {
		// Update holds the lock.
		if v := cs.find(n); v != nil {
			return v.data, nil
		}
		return nil, notFound("version %d is not kept", n)
	})
}

// etagMatches reports whether an If-Match field's value names etag: "*",
// or a list of entity tags one of which is etag, compared strongly.
func etagMatches(field, etag string) bool {
	for t := range strings.SplitSeq(field, ",") {
		if t = strings.TrimSpace(t); t == "*" || t == etag {
			return true
		}
	}
	return false
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
	v := cs.find(n)
	return v, v != nil
}

// find returns version n, or nil. cs.mu must be held.
func (cs *Configs) find(n uint64) *Version {
	for _, v := range cs.history {
		if v.Number == n {
			return v
		}
	}
	return nil
}

func sameAccessLog(a, b *config.AccessLog) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Output == b.Output && a.FlushInterval == b.FlushInterval && a.BufferBytes == b.BufferBytes
}

func sameACME(a, b *config.ACME) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Directory == b.Directory && a.Email == b.Email && a.CAFile == b.CAFile && a.Storage == b.Storage &&
		a.MaxConcurrent == b.MaxConcurrent && a.Timeout == b.Timeout && a.RequestTimeout == b.RequestTimeout &&
		slices.Equal(a.ChallengeTypes(), b.ChallengeTypes()) && sameOnDemand(a.OnDemand, b.OnDemand)
}

func sameOnDemand(a, b *config.OnDemand) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Ask == b.Ask && a.Limit() == b.Limit() && a.Every() == b.Every() && a.Names() == b.Names() && a.Wait() == b.Wait()
}

func sameAdmin(a, b *config.Admin) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Addr() == b.Addr() && a.TokenFile == b.TokenFile && a.HistoryLen() == b.HistoryLen() &&
		a.ConfigBytes() == b.ConfigBytes() && a.Conns() == b.Conns()
}
