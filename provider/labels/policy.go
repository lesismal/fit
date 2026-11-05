package labels

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/lesismal/fit/module"
	"github.com/lesismal/fit/provider/internal/vet"
)

// DefaultMiddleware are the middleware the routes labels write may use
// whatever the policy: each acts on the route's own requests alone, and
// reads nothing of the host's.
var DefaultMiddleware = []string{"headers", "rewrite", "redirect", "compress", "rate_limit", "concurrency_limit", "body_limit", "basic_auth", "circuit_breaker"}

// Policy is what the routes labels write may do, and where fit sends for
// them: whoever writes the labels is trusted no further than the
// provider's settings say.
type Policy struct {
	// AllowedHosts are the host names routes may match: exact, "*." and a
	// domain for any name below it, or "*" for any host, and routes that
	// match every host. None, when empty: no route is taken.
	AllowedHosts []string
	// AllowedModules are the handlers and middleware routes may use, with
	// any params, besides the proxy to their own upstream and
	// DefaultMiddleware.
	AllowedModules []string
	// MaxPriority is the highest priority a route may have.
	MaxPriority int
	// AllowLocal takes upstream addresses that are local, as vet.Local
	// tells them.
	AllowLocal bool
}

// Local reports whether p leaves out an upstream address of host, a host
// name or an IP address.
func (p *Policy) Local(host string) bool { return !p.AllowLocal && vet.Local(host) }

// Restrict checks s, the labels of a source of the upstream name, against
// p and as the config fit loads will check what they write. It leaves out
// of s the routes that break p, or that the config would refuse, saying why
// of each; and gives an error, for the whole source to be left out, when
// its upstream's settings or its services would be refused, which would
// otherwise have the provider's whole part refused.
func (s *Set) Restrict(name string, p *Policy) (problems []string, err error) {
	settings := map[string]json.RawMessage{}
	for k, v := range s.Upstream {
		settings[k] = v
	}
	settings["name"], _ = json.Marshal(name)
	settings["addresses"] = json.RawMessage(`["10.0.0.1:1"]`)
	u, _ := json.Marshal(settings)
	if err := vet.Upstream(u); err != nil {
		return nil, fmt.Errorf("the upstream's settings: %v", err)
	}
	for _, key := range SortedKeys(s.Services) {
		if sv := s.Services[key]; len(sv.ServerNames) > 0 {
			if err := vet.ServerNames(sv.ServerNames); err != nil {
				return nil, fmt.Errorf("service %s: %v", key, err)
			}
		}
	}
	c := vet.NewChecker(name)
	for _, key := range SortedKeys(s.Routes) {
		if err := p.check(s.Routes[key].out(name, key), name, c); err != nil {
			problems = append(problems, fmt.Sprintf("route %s: %v; it is left out", key, err))
			delete(s.Routes, key)
		}
	}
	return problems, nil
}

// check checks r, a route of the upstream name, against p, and with c as
// the config will.
func (p *Policy) check(r *Route, name string, c *vet.Checker) error {
	var hosts []string
	if r.Match != nil {
		hosts = r.Match.Host
	}
	if !slices.Contains(p.AllowedHosts, "*") {
		if len(hosts) == 0 {
			return fmt.Errorf("it matches every host, which takes \"*\" in the provider's allowed_hosts")
		}
		for _, h := range hosts {
			if !p.hostAllowed(h) {
				return fmt.Errorf("host %q is not among the provider's allowed_hosts", h)
			}
		}
	}
	if r.Priority > p.MaxPriority {
		return fmt.Errorf("priority %d is above the provider's max_priority, %d", r.Priority, p.MaxPriority)
	}
	var mws []json.RawMessage
	if r.Middle != nil {
		if err := json.Unmarshal(r.Middle, &mws); err != nil {
			return fmt.Errorf("middleware: %v", err)
		}
	}
	for i, mw := range mws {
		if m := moduleName(mw); !slices.Contains(DefaultMiddleware, m) && !slices.Contains(p.AllowedModules, m) {
			return fmt.Errorf("middleware %d: module %q is not one labels may use: the provider's allowed_modules would list it", i, m)
		}
	}
	switch m := moduleName(r.Handler); {
	case slices.Contains(p.AllowedModules, m):
	case m != "proxy":
		return fmt.Errorf("handler: module %q is not one labels may use: the provider's allowed_modules would list it", m)
	default:
		// The proxy may send to the source's own upstream alone: the
		// checker knows no other, and refuses any other its params name.
		if err := c.Module(module.Handlers, r.Handler); err != nil {
			return fmt.Errorf("handler: %v (labels may proxy to their own upstream, %s, alone)", err, name)
		}
	}
	data, _ := json.Marshal(r)
	return c.Route(data)
}

// hostAllowed reports whether a route may match host h, maybe one of fit's
// wildcards ("*." or "**."), by p's AllowedHosts.
func (p *Policy) hostAllowed(h string) bool {
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	for _, a := range p.AllowedHosts {
		a = strings.ToLower(strings.TrimSuffix(a, "."))
		if domain, ok := strings.CutPrefix(a, "*."); ok {
			if rest, ok := strings.CutSuffix(h, "."+domain); ok && rest != "" {
				return true
			}
		} else if h == a {
			return true
		}
	}
	return false
}

// moduleName returns the name of the module ref names, as fit's config
// reads it: "" when it names none, which loading it refuses.
func moduleName(ref json.RawMessage) string {
	var o map[string]json.RawMessage
	var name string
	if json.Unmarshal(ref, &o) != nil || json.Unmarshal(o["module"], &name) != nil {
		return ""
	}
	return name
}
