// Package headers has the headers middleware, which sets, adds and deletes
// fields of the request before the handler sees it, and of the response
// before it is sent.
package headers

import (
	"maps"
	stdhttp "net/http"
	"slices"
	"strings"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/module"
)

func init() {
	module.Register(module.Info{ID: module.Middlewares + ".headers", New: func() any { return new(Headers) }, Doc: "Sets, adds and deletes request and response header fields."})
}

// Headers changes the request's and the response's header fields.
type Headers struct {
	Request  *Ops `json:"request,omitempty" doc:"Changes to the request's fields, made before the handler, and the upstream a proxy sends the request to, sees them."`
	Response *Ops `json:"response,omitempty" doc:"Changes to the response's fields, made just before it is sent, whichever handler wrote it."`

	request, response ops
	// onHeader applies response to a response; a method value made once,
	// so that registering it costs no allocation.
	onHeader func(int, stdhttp.Header)
}

// Ops are changes to a header: deletions first, then fields set, then
// fields added.
type Ops struct {
	Set    map[string]string   `json:"set,omitempty" doc:"Fields to set, each replacing any of the same name."`
	Add    map[string][]string `json:"add,omitempty" doc:"Values to add to fields, after any already there."`
	Delete []string            `json:"delete,omitempty" doc:"Fields to delete: names, or a prefix ending in \"*\", as in \"X-Debug-*\"."`
}

// ops are Ops compiled, with names canonical.
type ops struct {
	delete   []string // canonical names
	prefixes []string // canonical prefixes, of deletions ending in "*"
	set      []field
	add      []field
}

type field struct {
	name   string
	values []string
}

func (o ops) empty() bool {
	return len(o.delete)+len(o.prefixes)+len(o.set)+len(o.add) == 0
}

// apply makes the changes to h. Each request gets values of its own, since
// what reads a header may change them in place.
func (o *ops) apply(h stdhttp.Header) {
	for _, name := range o.delete {
		delete(h, name)
	}
	for _, prefix := range o.prefixes {
		for name := range h {
			if strings.HasPrefix(name, prefix) {
				delete(h, name)
			}
		}
	}
	for _, f := range o.set {
		h[f.name] = append([]string(nil), f.values...)
	}
	for _, f := range o.add {
		h[f.name] = append(h[f.name], f.values...)
	}
}

// Provision checks the names and values and compiles them.
func (hs *Headers) Provision(ctx *module.Context) error {
	var err error
	if hs.request, err = compile(ctx, "request", hs.Request); err != nil {
		return err
	}
	if hs.response, err = compile(ctx, "response", hs.Response); err != nil {
		return err
	}
	if hs.request.empty() && hs.response.empty() {
		return ctx.Errorf("", "request or response changes are needed")
	}
	hs.onHeader = func(_ int, h stdhttp.Header) { hs.response.apply(h) }
	return nil
}

func compile(ctx *module.Context, where string, o *Ops) (ops, error) {
	var c ops
	if o == nil {
		return c, nil
	}
	for _, name := range o.Delete {
		if prefix, ok := strings.CutSuffix(name, "*"); ok {
			if prefix != "" && !validName(prefix) {
				return c, ctx.Errorf(where, "%q is not a valid header name or prefix", name)
			}
			c.prefixes = append(c.prefixes, stdhttp.CanonicalHeaderKey(prefix))
			continue
		}
		if !validName(name) {
			return c, ctx.Errorf(where, "%q is not a valid header name", name)
		}
		c.delete = append(c.delete, stdhttp.CanonicalHeaderKey(name))
	}
	for _, name := range slices.Sorted(maps.Keys(o.Set)) {
		if err := check(ctx, where, name, o.Set[name]); err != nil {
			return c, err
		}
		c.set = append(c.set, field{stdhttp.CanonicalHeaderKey(name), []string{o.Set[name]}})
	}
	for _, name := range slices.Sorted(maps.Keys(o.Add)) {
		for _, v := range o.Add[name] {
			if err := check(ctx, where, name, v); err != nil {
				return c, err
			}
		}
		c.add = append(c.add, field{stdhttp.CanonicalHeaderKey(name), o.Add[name]})
	}
	return c, nil
}

func check(ctx *module.Context, where, name, value string) error {
	if !validName(name) {
		return ctx.Errorf(where, "%q is not a valid header name", name)
	}
	if strings.ContainsAny(value, "\r\n\x00") {
		return ctx.Errorf(where, "the value of %s holds a line break or NUL", name)
	}
	return nil
}

// Wrap returns next behind the changes.
func (hs *Headers) Wrap(next module.Handler) module.Handler {
	return fibhttp.HandlerFunc(func(c *fibhttp.Context) {
		if !hs.request.empty() {
			hs.request.apply(c.Request.Header)
		}
		if !hs.response.empty() {
			c.OnHeader(hs.onHeader)
		}
		next.ServeHTTP(c)
	})
}

// validName reports whether name is an HTTP token.
func validName(name string) bool {
	if name == "" {
		return false
	}
	for i := range len(name) {
		c := name[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			continue
		}
		if !strings.ContainsRune("!#$%&'*+-.^_`|~", rune(c)) {
			return false
		}
	}
	return true
}
