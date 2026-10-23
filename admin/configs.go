// Package admin is fit's control plane for config: the path every new config
// takes to the running server, the history of the configs applied, and the
// Admin API that reads and replaces them.
//
// Nothing here runs on a fib worker. Configs are applied on the goroutine of
// whoever asks, one at a time, and the Admin API is served by net/http on
// goroutines of its own; see docs/decisions/005-admin-api.md.
package admin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/listener"
	"github.com/lesismal/fit/provider"
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
// path whatever their source: SIGHUP, the Admin API, the file provider, the
// watcher of certificate files and providers. It keeps the configs it
// applied, numbered by the versions the runtime gave their graphs.
//
// What runs is the base config, the one the file and the Admin API write,
// with what each provider provides merged into it, by package provider's
// rules: the base config wins over every provider, and a provider added
// earlier over those added later.
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

	// providers are the names of the providers, in their order of
	// precedence, and parts what each provided last that was applied;
	// both under mu.
	providers []string
	parts     map[string]*provider.Part

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
	// baseSource is where data came from: the source of the last change to
	// the base config, or of the version a rollback took it from.
	baseSource string
	// effective is the config that ran: data with what providers provided
	// merged into it, data itself when they provided nothing. sources and
	// shadowed say where its parts came from and what was left out.
	effective []byte
	sources   []provider.Source
	shadowed  []provider.Shadowed
}

// Data returns the base config as it was written: without what providers
// provided. The caller must not change it.
func (v *Version) Data() []byte { return v.data }

// Effective returns the config that ran: the base config with what
// providers provided merged into it. The caller must not change it.
func (v *Version) Effective() []byte { return v.effective }

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
	// Deprecated are the names the config uses that were renamed, each
	// with its path and its position in the config applied.
	Deprecated config.Errors
}

// NewConfigs returns the Configs of server, whose runtime rt is running
// first, compiled from data, as its current graph. Compiling a config takes
// env, and keep is how many configs, the running one included, are kept to
// be read back.
func NewConfigs(rt *runtime.Runtime, server *listener.Server, env runtime.Env, first *config.Config, data []byte, source string, keep int) *Configs {
	cs := &Configs{rt: rt, server: server, env: env, first: first, keep: max(keep, 1), parts: map[string]*provider.Part{}}
	var version uint64
	if g := rt.Current(); g != nil {
		version = g.Version
		g.Release()
	}
	m, err := provider.Merge(data, source, nil)
	if err != nil {
		m = &provider.Merged{Data: data}
	}
	cs.record(version, data, source, source, m)
	return cs
}

func (cs *Configs) record(version uint64, data []byte, source, baseSource string, m *provider.Merged) {
	sum := sha256.Sum256(data)
	cs.history = append(cs.history, &Version{Number: version, Applied: time.Now(), Source: source, Bytes: len(data),
		ETag: `"` + hex.EncodeToString(sum[:12]) + `"`, data: data, baseSource: baseSource,
		effective: m.Data, sources: m.Sources, shadowed: m.Shadowed})
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
	return cs.update(source, ifMatch, func(cur *Version) ([]byte, string, error) {
		data, err := edit(cur)
		return data, source, err
	})
}

// update is Update with edit saying where the base config it gives came
// from.
func (cs *Configs) update(source, ifMatch string, edit func(cur *Version) (data []byte, baseSource string, err error)) (Result, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cur := cs.history[len(cs.history)-1]
	if ifMatch != "" && !etagMatches(ifMatch, cur.ETag) {
		return Result{}, ErrChanged
	}
	data, baseSource, err := edit(cur)
	if err != nil {
		return Result{}, err
	}
	cs.reloads.Add(1)
	r, c, m, err := cs.apply(data, baseSource, cs.parts, source)
	if err != nil {
		cs.failures.Add(1)
		return Result{}, err
	}
	cs.record(r.Version, data, source, baseSource, m)
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
	if !sameJSON(cs.first.Telemetry, c.Telemetry) {
		r.Warnings = append(r.Warnings, "telemetry changed; the change takes effect when fit restarts")
	}
	if !sameProviders(cs.first.Providers, c.Providers) {
		r.Warnings = append(r.Warnings, "providers changed; the change takes effect when fit restarts")
	}
	return r, nil
}

// apply merges parts into data, the base config, whose source is
// baseSource, and loads, compiles and applies the result. Errors are
// reported where they were written: in the base config, or in what a
// provider provided. cs.mu must be held.
func (cs *Configs) apply(data []byte, baseSource string, parts map[string]*provider.Part, source string) (Result, *config.Config, *provider.Merged, error) {
	var ordered []*provider.Part
	for _, name := range cs.providers {
		if p := parts[name]; p != nil {
			ordered = append(ordered, p)
		}
	}
	m := &provider.Merged{Data: data}
	if json.Valid(data) {
		merged, err := provider.Merge(data, baseSource, ordered)
		switch {
		case err == nil:
			m = merged
		case len(ordered) > 0:
			return Result{}, nil, nil, err
		}
		// With nothing to merge, a base config that cannot take parts
		// is loaded as it is, for its own errors.
	}
	c, err := config.Load(m.Data)
	if err != nil {
		return Result{}, nil, nil, m.Errors(err)
	}
	env := cs.env
	env.Previous = cs.rt.Current()
	g, err := runtime.Compile(c, env)
	if env.Previous != nil {
		env.Previous.Release()
	}
	if err != nil {
		return Result{}, nil, nil, m.Errors(err)
	}
	added, removed, err := cs.server.Reload(g)
	if err != nil {
		return Result{}, nil, nil, err
	}
	// The runtime holds g now, and only Apply swaps graphs while fit runs,
	// so g is still current.
	var deprecated config.Errors
	if ws := c.Warnings(); len(ws) > 0 {
		deprecated = config.AsErrors(m.Errors(ws))
	}
	return Result{Version: g.Version, Source: source, Added: added, Removed: removed, Deprecated: deprecated}, c, m, nil
}

// Reloads returns how many configs were tried, from loading on, since fit
// started, and how many of them failed; the config fit started with is not
// counted.
func (cs *Configs) Reloads() (tried, failed uint64) { return cs.reloads.Load(), cs.failures.Load() }

// Reapply applies the running config again, as a new version, so that what
// it loads from files, its certificates, is loaded again.
func (cs *Configs) Reapply(source string) (Result, error) {
	return cs.update(source, "", func(cur *Version) ([]byte, string, error) { return cur.data, cur.baseSource, nil })
}

// Rollback applies again the config applied as version n, as a new version.
// It gives a *RequestError with status 404 when n is no longer kept.
func (cs *Configs) Rollback(n uint64, ifMatch string) (Result, error) {
	return cs.update(SourceRollback, ifMatch, func(*Version) ([]byte, string, error) {
		// update holds the lock.
		if v := cs.find(n); v != nil {
			return v.data, v.baseSource, nil
		}
		return nil, "", notFound("version %d is not kept", n)
	})
}

// providerName is what a provider's name must look like.
var providerName = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,62}$`)

// AddProvider adds a provider, named name, whose parts Provide applies. A
// provider added earlier wins over those added later. The names of the
// base config's sources, file, admin, rollback and certificates, are
// taken.
func (cs *Configs) AddProvider(name string) error {
	if !providerName.MatchString(name) {
		return fmt.Errorf("admin: provider name %q: lower-case letters, digits, '.', '_' and '-', starting with a letter or digit, at most 63", name)
	}
	if slices.Contains([]string{SourceFile, SourceAdmin, SourceRollback, SourceCertificates}, name) {
		return fmt.Errorf("admin: provider name %q is a source of the base config", name)
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if slices.Contains(cs.providers, name) {
		return fmt.Errorf("admin: provider %q is added already", name)
	}
	cs.providers = append(cs.providers, name)
	return nil
}

// Provide applies data, all that the provider name provides now, as
// provider.Parse reads it, merged into the running base config, as a new
// version whose source is name. On any failure the running config stays,
// and so does what the provider provided before; errors in data are
// config.Errors with positions in data. Data the same as the provider's
// last is not applied again. Provide keeps data, which the caller must not
// change afterwards.
func (cs *Configs) Provide(name string, data []byte) (Result, error) {
	p, err := provider.Parse(name, data)
	if err != nil {
		return Result{}, err
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if !slices.Contains(cs.providers, name) {
		return Result{}, fmt.Errorf("admin: no provider %q was added", name)
	}
	cur := cs.history[len(cs.history)-1]
	if old := cs.parts[name]; old != nil && bytes.Equal(old.Data(), p.Data()) {
		return Result{Version: cur.Number, ETag: cur.ETag, Source: name}, nil
	}
	parts := make(map[string]*provider.Part, len(cs.parts)+1)
	for k, v := range cs.parts {
		parts[k] = v
	}
	parts[name] = p
	cs.reloads.Add(1)
	r, _, m, err := cs.apply(cur.data, cur.baseSource, parts, name)
	if err != nil {
		cs.failures.Add(1)
		return Result{}, err
	}
	cs.parts = parts
	cs.record(r.Version, cur.data, name, cur.baseSource, m)
	r.ETag = cur.ETag
	return r, nil
}

// Effective is the running config as it runs, and where its parts came
// from: GET /config/effective's answer.
type Effective struct {
	Version uint64 `json:"version"`
	// ETag is the base config's.
	ETag string `json:"etag"`
	// Precedence lists the sources, the base config's first, from the one
	// that wins to the one that loses.
	Precedence []string        `json:"precedence"`
	Config     json.RawMessage `json:"config"`
	// Sources says where each member of Config came from, element by
	// element in its arrays.
	Sources []provider.Source `json:"sources"`
	// Shadowed lists what providers provided and Config leaves out,
	// because a source before them took its name.
	Shadowed []provider.Shadowed `json:"shadowed,omitempty"`
}

// Effective returns the running config as it runs.
func (cs *Configs) Effective() Effective {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	v := cs.history[len(cs.history)-1]
	return Effective{Version: v.Number, ETag: v.ETag, Precedence: append([]string{v.baseSource}, cs.providers...),
		Config: v.effective, Sources: v.sources, Shadowed: v.shadowed}
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

func sameProviders(a, b []config.Provider) bool { return sameJSON(a, b) }

func sameJSON(a, b any) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return bytes.Equal(ja, jb)
}

func sameACME(a, b *config.ACME) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Directory == b.Directory && a.Email == b.Email && a.CAFile == b.CAFile && a.Storage == b.Storage &&
		a.MaxConcurrent == b.MaxConcurrent && a.Timeout == b.Timeout && a.RequestTimeout == b.RequestTimeout &&
		slices.Equal(a.ChallengeTypes(), b.ChallengeTypes()) && sameOnDemand(a.OnDemand, b.OnDemand) &&
		sameJSON(a.SharedStorage, b.SharedStorage)
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
