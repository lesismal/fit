package labels

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/lesismal/fit/module"
	"github.com/lesismal/fit/provider"
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
	// AllowFiles takes settings and params that name files or directories
	// of fit's host, as tls_key_file does; FileKey tells them.
	AllowFiles bool
}

// Local reports whether p leaves out an upstream address of host, a host
// name or an IP address.
func (p *Policy) Local(host string) bool { return !p.AllowLocal && vet.Local(host) }

// FileKey reports whether key, a member of a config's object, names a file
// or directory of fit's host: ending in _file, _files or _dir, or file,
// root, directory or disk_path, but for max_ limits.
func FileKey(key string) bool {
	switch {
	case strings.HasPrefix(key, "max_"):
		return false
	case key == "file", key == "root", key == "directory", key == "disk_path":
		return true
	}
	return strings.HasSuffix(key, "_file") || strings.HasSuffix(key, "_files") || strings.HasSuffix(key, "_dir")
}

// fileKey returns the path of the first member of v, JSON, at any depth,
// that FileKey tells; "" for none.
func fileKey(v json.RawMessage, at string) string {
	var o map[string]json.RawMessage
	var a []json.RawMessage
	switch {
	case json.Unmarshal(v, &o) == nil:
		for _, k := range SortedKeys(o) {
			if FileKey(k) {
				return strings.TrimPrefix(at+"."+k, ".")
			}
			if p := fileKey(o[k], at+"."+k); p != "" {
				return p
			}
		}
	case json.Unmarshal(v, &a) == nil:
		for i, e := range a {
			if p := fileKey(e, fmt.Sprintf("%s[%d]", at, i)); p != "" {
				return p
			}
		}
	}
	return ""
}

// Scope is what the sources of one part claim, and what the rest of the
// running config has: the names of upstreams, the listeners their routes
// and services may name, and the connections each service takes. Two
// sources, or a source and another of the config's, claiming the same
// would have the provider's whole part refused.
type Scope struct {
	names     *provider.Names // nil when they are not known
	upstreams map[string]string
	claims    map[[2]string]string // (listener, server name) → the service taking it; "" for those naming none, "udp" for a udp listener
}

// NewScope returns the Scope of a part made beside names, the rest of the
// running config, or nil when that is not known: then only what the
// part's own sources claim is checked.
func NewScope(names *provider.Names) *Scope {
	sc := &Scope{names: names, upstreams: map[string]string{}, claims: map[[2]string]string{}}
	if names != nil {
		for _, sv := range names.Services {
			for _, l := range sv.Listeners {
				for _, k := range sc.keys(l, sv.ServerNames) {
					sc.claims[k] = sv.Name
				}
			}
		}
	}
	return sc
}

// keys are what a service of serverNames claims on listener l.
func (sc *Scope) keys(l string, serverNames []string) [][2]string {
	if sc.names != nil && sc.names.Listeners[l].L4 == "udp" {
		return [][2]string{{l, "udp"}}
	}
	if len(serverNames) == 0 {
		return [][2]string{{l, ""}}
	}
	var out [][2]string
	for _, sn := range serverNames {
		out = append(out, [2]string{l, strings.ToLower(sn)})
	}
	return out
}

// Restrict checks s, the labels of a source of the upstream name, against
// p and as the config fit loads will check what they write, beside what
// sc says the rest of the part and of the config have. It leaves out of s
// the routes and services that break p, or that the config would refuse,
// saying why of each; and gives an error, for the whole source to be left
// out, when its upstream's name or settings would be refused, which would
// otherwise have the provider's whole part refused. What it takes, it
// claims in sc.
func (s *Set) Restrict(name string, p *Policy, sc *Scope) (problems []string, err error) {
	if sc == nil {
		sc = NewScope(nil)
	}
	upstreams := []string{name}
	for _, key := range SortedKeys(s.Services) {
		if s.Services[key].port != 0 {
			upstreams = append(upstreams, name+"."+key)
		}
	}
	for _, u := range upstreams {
		if owner, ok := sc.upstreams[u]; ok && owner != name || sc.names != nil && sc.names.Upstreams[u] {
			return nil, fmt.Errorf("upstream %q is another's, of the config, another provider or another source", u)
		}
	}
	if !p.AllowFiles {
		if at := fileKey(mustJSON(s.Upstream), ""); at != "" {
			return nil, fmt.Errorf("the upstream's settings: %s names a file of fit's host, which the provider's allow_file_paths must allow", at)
		}
	}
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
		if err := sc.service(name+"."+key, s.Services[key]); err != nil {
			problems = append(problems, fmt.Sprintf("service %s: %v; it is left out", key, err))
			delete(s.Services, key)
		}
	}
	// The routes may name the upstreams of the source's services taken too.
	upstreams = upstreams[:1]
	for _, key := range SortedKeys(s.Services) {
		if s.Services[key].port != 0 {
			upstreams = append(upstreams, name+"."+key)
		}
	}
	c := vet.NewChecker(upstreams...)
	for _, key := range SortedKeys(s.Routes) {
		if err := p.check(s.Routes[key].out(name, key), name, c, sc); err != nil {
			problems = append(problems, fmt.Sprintf("route %s: %v; it is left out", key, err))
			delete(s.Routes, key)
		}
	}
	for _, u := range upstreams {
		sc.upstreams[u] = name
	}
	for _, key := range SortedKeys(s.Services) {
		sv := s.Services[key]
		for _, l := range sv.Listeners {
			for _, k := range sc.keys(l, sv.ServerNames) {
				sc.claims[k] = name + "." + key
			}
		}
	}
	return problems, nil
}

// service checks sv, the service named name: its server names as the
// config does, its listeners are tcp or udp listeners of the config's, and
// the connections it would take are no other service's.
func (sc *Scope) service(name string, sv *Service) error {
	if len(sv.ServerNames) > 0 {
		if err := vet.ServerNames(sv.ServerNames); err != nil {
			return err
		}
	}
	for _, l := range sv.Listeners {
		if sc.names != nil {
			nl, ok := sc.names.Listeners[l]
			switch {
			case !ok:
				return fmt.Errorf("listener %q is not one of the config's", l)
			case nl.L4 == "":
				return fmt.Errorf("listener %q serves neither tcp nor udp", l)
			case nl.L4 == "udp" && (len(sv.ServerNames) > 0 || sv.ProxyProtocol != ""):
				return fmt.Errorf("listener %q is a udp listener, which takes no server_names and sends no PROXY protocol", l)
			}
		}
		for _, k := range sc.keys(l, sv.ServerNames) {
			if other, ok := sc.claims[k]; ok && other != name {
				return fmt.Errorf("listener %q's connections%s are service %s's", l, map[bool]string{true: "", false: " for " + k[1]}[k[1] == "" || k[1] == "udp"], other)
			}
		}
	}
	if sc.names != nil && slices.ContainsFunc(sc.names.Services, func(o provider.Service) bool { return o.Name == name }) {
		return fmt.Errorf("its name is another service's")
	}
	return nil
}

func mustJSON(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}

// check checks r, a route of the upstream name, against p, and with c as
// the config will.
func (p *Policy) check(r *Route, name string, c *vet.Checker, sc *Scope) error {
	if n := sc.names; n != nil {
		for _, l := range r.Listeners {
			switch nl, ok := n.Listeners[l]; {
			case !ok:
				return fmt.Errorf("listener %q is not one of the config's", l)
			case !nl.HTTP:
				return fmt.Errorf("listener %q serves no HTTP", l)
			}
		}
		if n.Routes[r.Name] {
			return fmt.Errorf("its name, %s, is another route's", r.Name)
		}
	}
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
	if m := moduleName(r.Handler); m != "proxy" && !slices.Contains(p.AllowedModules, m) {
		return fmt.Errorf("handler: module %q is not one labels may use: the provider's allowed_modules would list it", m)
	}
	if !p.AllowFiles {
		for _, part := range []struct {
			at  string
			raw json.RawMessage
		}{{"handler", r.Handler}, {"middleware", r.Middle}} {
			if at := fileKey(part.raw, part.at); at != "" {
				return fmt.Errorf("%s names a file of fit's host, which the provider's allow_file_paths must allow", at)
			}
		}
	}
	switch m := moduleName(r.Handler); {
	case slices.Contains(p.AllowedModules, m):
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
