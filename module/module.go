// Package module is fit's module registry, and the interfaces modules
// implement.
//
// A module is a Go type registered under a namespaced ID, such as
// http.handlers.respond. A config names a module by the last part of its ID,
// and the namespace comes from where the name appears: a route's handler is
// looked up in http.handlers, its middleware in http.middleware. The
// module's exported fields are its params, decoded from the config as
// strictly as the config itself; its unexported fields hold what it builds
// from them when it is provisioned.
//
// Modules register themselves in an init function, and a binary includes a
// module by importing its package; package modules/standard imports every
// module fit ships with.
package module

import (
	"fmt"
	stdhttp "net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/upstream"
)

// Namespaces, and the interface a module in each must implement.
const (
	// Handlers answer requests: Handler.
	Handlers = "http.handlers"
	// Middlewares wrap a route's handler: Middleware.
	Middlewares = "http.middleware"
)

// Handler answers HTTP requests. It is fib's handler: it may answer before
// it returns, or keep the Context and answer later from another goroutine,
// but it must never block the fib worker it runs on.
type Handler = fibhttp.Handler

// Middleware wraps the handler that follows it in a route. Wrap is called
// once, when the route is compiled, so the chain costs nothing to build per
// request.
type Middleware interface {
	Wrap(next Handler) Handler
}

// Matcher reports whether a request matches. Matchers run for every
// request, so Match must be cheap and must not block.
type Matcher interface {
	Match(r *stdhttp.Request) bool
}

// Upstream is one address requests can be sent to.
//
// Provisional: I14 (upstream pools) and I19 (load balancing) settle what an
// upstream and a service have to offer.
type Upstream interface {
	Address() string
}

// Service picks the upstream a request goes to, or nil when none is
// available.
//
// Provisional, like Upstream.
type Service interface {
	Select(r *stdhttp.Request) Upstream
}

// Provisioner is implemented by a module that has work to do once its params
// are decoded: checking them beyond what the decoder can, and building what
// it serves requests with.
type Provisioner interface {
	Provision(ctx *Context) error
}

// Cleaner is implemented by a module that holds something to release when
// the config it belongs to is no longer used: a goroutine, a file, a pool.
// Cleanup is called once, after the last request using the module is done.
type Cleaner interface {
	Cleanup()
}

// Drainer is implemented by a module that serves requests past its
// handler's return, as the proxy does. Drain is called when the config the
// module belongs to was swapped out the drain timeout ago and requests still
// use it: the module ends what it has under way, as soon as it can. Drain
// may run beside Cleanup, and at most once.
type Drainer interface {
	Drain()
}

// Holder is a hold on the config a module belongs to: a module whose
// requests outlive its handler's return takes one for each with Retain, and
// gives it back with Release when the request is over, so that the config
// is cleaned up, and drained, only once they are.
type Holder interface {
	Retain()
	Release()
}

// Context is what Provision is given.
type Context struct {
	// Params are the module's params as written, for errors to point into.
	Params config.Value

	env *Env
}

// Env is what the config a module belongs to provides it with.
type Env struct {
	// Upstreams are the config's upstream pools, by name.
	Upstreams map[string]*upstream.Pool
	// WriteStall bounds each wait of a response for its client to take what
	// it was sent; see config.Limits.WriteStallTimeout. Zero is the default.
	WriteStall time.Duration
	// Graph is the config the module belongs to, or nil, as in tests.
	Graph Holder
}

// Graph returns the hold a module takes on its config for a request that
// outlives its handler's return, or one that does nothing when there is
// no config, as in tests.
func (c *Context) Graph() Holder {
	if c.env == nil || c.env.Graph == nil {
		return noHolder{}
	}
	return c.env.Graph
}

type noHolder struct{}

func (noHolder) Retain()  {}
func (noHolder) Release() {}

// WriteStallTimeout returns how long a response may wait, each time, for
// its client to take what it was sent; a handler that writes a response
// piece by piece bounds each write by it.
func (c *Context) WriteStallTimeout() time.Duration {
	if c.env == nil || c.env.WriteStall <= 0 {
		return config.DefaultWriteStallTimeout.Std()
	}
	return c.env.WriteStall
}

// Upstream returns the pool of the upstream named name in the module's
// config. The pool lives as long as the module does.
func (c *Context) Upstream(name string) (*upstream.Pool, bool) {
	if c.env == nil {
		return nil, false
	}
	p, ok := c.env.Upstreams[name]
	return p, ok
}

// Errorf returns an error about the param named field, or about the params as
// a whole when field is empty, with its path and position.
func (c *Context) Errorf(field, format string, args ...any) error {
	v := c.Params
	if field != "" {
		v = v.Field(field)
	}
	return v.Errorf(format, args...)
}

// Info describes a module.
type Info struct {
	// ID is the module's namespaced ID, as in http.handlers.respond.
	ID string
	// New returns a new, zero module: a pointer to the struct its params are
	// decoded into.
	New func() any
}

// Namespace returns the namespace part of the ID, as in http.handlers.
func (i Info) Namespace() string {
	ns, _ := splitID(i.ID)
	return ns
}

// Name returns the last part of the ID, by which a config names the module.
func (i Info) Name() string {
	_, name := splitID(i.ID)
	return name
}

func splitID(id string) (ns, name string) {
	i := strings.LastIndexByte(id, '.')
	return id[:i], id[i+1:]
}

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)

var (
	mu      sync.RWMutex
	modules = map[string]Info{}
)

// Register adds a module to the registry. It panics if the ID is malformed,
// already taken, or New is nil, which are mistakes in the module's code.
func Register(info Info) {
	if !idPattern.MatchString(info.ID) {
		panic(fmt.Sprintf("module: malformed ID %q", info.ID))
	}
	if info.New == nil {
		panic(fmt.Sprintf("module: %s has no New", info.ID))
	}
	mu.Lock()
	defer mu.Unlock()
	if _, ok := modules[info.ID]; ok {
		panic(fmt.Sprintf("module: %s is registered twice", info.ID))
	}
	modules[info.ID] = info
}

// Lookup returns the module registered with id.
func Lookup(id string) (Info, bool) {
	mu.RLock()
	defer mu.RUnlock()
	info, ok := modules[id]
	return info, ok
}

// Namespace returns the modules registered directly in namespace ns, sorted
// by ID.
func Namespace(ns string) []Info {
	mu.RLock()
	defer mu.RUnlock()
	var infos []Info
	for _, info := range modules {
		if info.Namespace() == ns {
			infos = append(infos, info)
		}
	}
	slices.SortFunc(infos, func(a, b Info) int { return strings.Compare(a.ID, b.ID) })
	return infos
}

// Load creates the module ref names in namespace ns, decodes its params and
// provisions it with env, which may be nil. Its errors carry the path and
// position of what is wrong.
func Load(ns string, ref config.ModuleRef, env *Env) (any, error) {
	info, ok := Lookup(ns + "." + ref.Module)
	if !ok {
		return nil, ref.At.Field("module").Errorf("there is no module %q in %s%s", ref.Module, ns, known(ns))
	}
	m := info.New()
	if err := ref.Params.Decode(m); err != nil {
		return nil, err
	}
	if p, ok := m.(Provisioner); ok {
		if err := p.Provision(&Context{Params: ref.Params, env: env}); err != nil {
			if _, ok := err.(*config.Error); !ok {
				if _, ok := err.(config.Errors); !ok {
					err = ref.Params.Errorf("%v", err)
				}
			}
			return nil, err
		}
	}
	return m, nil
}

// known lists the modules namespace ns has, for an error about one it lacks.
func known(ns string) string {
	infos := Namespace(ns)
	if len(infos) == 0 {
		return ""
	}
	names := make([]string, len(infos))
	for i, info := range infos {
		names[i] = fmt.Sprintf("%q", info.Name())
	}
	return " (it has " + strings.Join(names, ", ") + ")"
}

// Cleanup calls m's Cleanup, if it has one.
func Cleanup(m any) {
	if c, ok := m.(Cleaner); ok {
		c.Cleanup()
	}
}
