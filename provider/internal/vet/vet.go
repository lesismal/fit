// Package vet checks what a provider is about to provide, as loading and
// compiling the merged config will check it, so that the provider can leave
// out the one element at fault, and say so, rather than have its whole part
// refused: a part that would leave the config invalid is refused as a
// whole, and the provider's part before it stays, so one bad element from
// one tenant would otherwise hold back every change from every other. It
// also tells the addresses fit must not send to for others.
package vet

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	"github.com/lesismal/fit/upstream"
)

// Local reports whether host, a host name or an IP address, maybe in
// brackets or with a trailing dot, is one fit must not send others'
// requests to: empty (which dials the host itself), localhost or a name
// under .localhost, a loopback, link-local, unspecified or 0.0.0.0/8
// address, or a name whose last label is a number, which resolvers may
// read as an IPv4 address in a form Go does not parse, as 127.1,
// 2130706433 or 0x7f000001 are. A name is not resolved: one that resolves
// to such an address is not caught.
func Local(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(strings.TrimSuffix(strings.TrimPrefix(host, "["), "]"), "."))
	if h == "" || h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	if a, err := netip.ParseAddr(h); err == nil {
		a = a.Unmap()
		return a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsInterfaceLocalMulticast() ||
			a.IsUnspecified() || a.Is4() && a.As4()[0] == 0
	}
	last := h[strings.LastIndexByte(h, '.')+1:]
	return numeric(last)
}

// numeric reports whether label is a number, in decimal, octal or hex.
func numeric(label string) bool {
	digits := label
	if rest, ok := strings.CutPrefix(label, "0x"); ok {
		digits = rest
	}
	if digits == "" {
		return label != ""
	}
	for _, c := range digits {
		if !(c >= '0' && c <= '9' || digits != label && (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// pure are the modules a Checker provisions: those that only check and
// compile their params, touching nothing outside them (no files, no disk,
// no goroutines), so that provisioning one and dropping it is harmless.
var pure = map[string]bool{
	"proxy": true, "respond": true, "headers": true, "rewrite": true, "redirect": true, "compress": true,
	"rate_limit": true, "concurrency_limit": true, "body_limit": true, "basic_auth": true, "circuit_breaker": true,
}

// A Checker checks routes as a config holding them, and upstreams of the
// names it is given, will be loaded and compiled: the route as the config
// checks it, and each of its middleware and its handler loaded and
// provisioned by its module, for the modules in pure, or for others their
// params decoded. A module this fit lacks is refused, as the config would
// be. A Checker is used by one goroutine at a time.
type Checker struct {
	upstreams map[string]*upstream.Pool
}

// NewChecker returns a Checker of routes that may send to upstreams.
func NewChecker(upstreams ...string) *Checker {
	c := &Checker{upstreams: map[string]*upstream.Pool{}}
	for _, u := range upstreams {
		c.Upstream(u)
	}
	return c
}

// Upstream adds an upstream that routes may send to. Modules are
// provisioned with no pool for it, which only their requests would use.
func (c *Checker) Upstream(name string) { c.upstreams[name] = nil }

// listener is the listener of the configs a Checker loads, in place of
// those a route names, which the config the part is merged into has.
var listener = json.RawMessage(`{"name": "vet", "address": "127.0.0.1:0", "protocols": ["http/1.1"]}`)

// Route checks route, a route as JSON, but for its listeners.
func (c *Checker) Route(route json.RawMessage) error {
	var r map[string]json.RawMessage
	if err := json.Unmarshal(route, &r); err != nil {
		return fmt.Errorf("a route must be a JSON object")
	}
	delete(r, "listeners")
	cfg, err := load(map[string]any{"listeners": []json.RawMessage{listener}, "routes": []any{r}}, "routes[0]")
	if err != nil {
		return err
	}
	rc := cfg.Routes[0]
	for i, m := range rc.Middleware {
		if err := c.module(module.Middlewares, m, fmt.Sprintf("routes[0].middleware[%d]", i)); err != nil {
			return fmt.Errorf("middleware %d: %w", i, err)
		}
	}
	if err := c.module(module.Handlers, rc.Handler, "routes[0].handler"); err != nil {
		return fmt.Errorf("handler: %w", err)
	}
	return nil
}

// Module checks ref, a module of namespace ns as JSON, as Route checks a
// route's.
func (c *Checker) Module(ns string, ref json.RawMessage) error {
	v, err := config.ParseValue(ref, "")
	if err != nil {
		return brief(err, "")
	}
	var mr config.ModuleRef
	if err := v.Decode(&mr); err != nil {
		return brief(err, "")
	}
	return c.module(ns, mr, "")
}

// module loads and provisions ref, at at in the config Route loads.
func (c *Checker) module(ns string, ref config.ModuleRef, at string) error {
	info, ok := module.Lookup(ns + "." + ref.Module)
	if !ok {
		return fmt.Errorf("there is no module %q among this fit's %s", ref.Module, ns)
	}
	if !pure[ref.Module] {
		// Its params are decoded, which touches nothing, but it is not
		// provisioned.
		return brief(ref.Params.Decode(info.New()), at)
	}
	m, err := module.Load(ns, ref, &module.Env{Upstreams: c.upstreams})
	if err != nil {
		return brief(err, at)
	}
	module.Cleanup(m)
	return nil
}

// Upstream checks upstream, an upstream as JSON, as the config does.
func Upstream(u json.RawMessage) error {
	_, err := load(map[string]any{"listeners": []json.RawMessage{listener}, "upstreams": []json.RawMessage{u}}, "upstreams[0]")
	return err
}

// ServerNames checks the server names of a service of a tcp listener, as
// the config does.
func ServerNames(names []string) error {
	_, err := load(map[string]any{
		"listeners": []any{map[string]any{"name": "vet", "address": "127.0.0.1:0", "protocols": []string{"tcp"}}},
		"upstreams": []any{map[string]any{"name": "vet", "addresses": []string{"127.0.0.1:1"}}},
		"services":  []any{map[string]any{"name": "vet", "listeners": []string{"vet"}, "upstream": "vet", "server_names": names}},
	}, "services[0]")
	return err
}

// load loads the config c, and gives its errors with their paths below at,
// the element checked, cut to what follows it.
func load(c map[string]any, at string) (*config.Config, error) {
	data, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(data)
	if err != nil {
		return nil, brief(err, at)
	}
	return cfg, nil
}

// brief writes err, config errors, without positions, which are those of
// a config the checker made, and with their paths cut below at.
func brief(err error, at string) error {
	var es config.Errors
	var e1 *config.Error
	switch {
	case errors.As(err, &es):
	case errors.As(err, &e1):
		es = config.Errors{e1}
	default:
		return err
	}
	var out []string
	for _, e := range es {
		path := strings.TrimPrefix(strings.TrimPrefix(e.Path, at), ".")
		if path == "" {
			out = append(out, e.Msg)
		} else {
			out = append(out, path+": "+e.Msg)
		}
	}
	return errors.New(strings.Join(out, "; "))
}
