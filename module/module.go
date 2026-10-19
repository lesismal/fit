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

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
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

// Context is what Provision is given.
type Context struct {
	// Params are the module's params as written, for errors to point into.
	Params config.Value
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
// provisions it. Its errors carry the path and position of what is wrong.
func Load(ns string, ref config.ModuleRef) (any, error) {
	info, ok := Lookup(ns + "." + ref.Module)
	if !ok {
		return nil, ref.At.Field("module").Errorf("there is no module %q in %s%s", ref.Module, ns, known(ns))
	}
	m := info.New()
	if err := ref.Params.Decode(m); err != nil {
		return nil, err
	}
	if p, ok := m.(Provisioner); ok {
		if err := p.Provision(&Context{Params: ref.Params}); err != nil {
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
