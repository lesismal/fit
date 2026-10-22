// Package labels reads routes, services and an upstream's settings from
// key-value labels under a prefix, as the Docker provider reads containers'
// labels and the Consul provider services' tags, and writes the part they
// make.
//
// The labels, under a prefix (fit, say):
//
//	fit.port=<port>                 the port; the source's own when left out
//	fit.upstream=<JSON object>      more of the upstream's settings, as in the config
//	fit.routes.<r>.host=<a,b>       a route named <name>.<r>, matching these hosts;
//	fit.routes.<r>.path=<a,b>       ... paths, as in the config's match
//	fit.routes.<r>.path_regexp=<a,b>
//	fit.routes.<r>.method=<a,b>
//	fit.routes.<r>.expr=<expr>
//	fit.routes.<r>.priority=<n>
//	fit.routes.<r>.listeners=<a,b>
//	fit.routes.<r>.middleware=<JSON array>
//	fit.routes.<r>.handler=<JSON object>  proxy to the upstream when left out
//	fit.services.<s>.listeners=<a,b>      a layer 4 service named <name>.<s>
//	fit.services.<s>.port=<port>          to this port, through an upstream of its own
//	fit.services.<s>.server_names=<a,b>
//	fit.services.<s>.proxy_protocol=<v1|v2>
//
// Other labels under the prefix are the caller's to read, or refused.
package labels

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var (
	// NamePattern is what the config takes as a name.
	NamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	// keyPattern is what a route's or a service's key in a label looks
	// like: a name without dots, which separate a label's parts.
	keyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
)

// Kind words what labels are called, and what has them, in errors.
type Kind struct {
	Label string // label, or tag
	Owner string // container, or service
}

// Set is what a source's labels say.
type Set struct {
	Port     int // 0 when the labels name none
	Upstream map[string]json.RawMessage
	Routes   map[string]*Route
	Services map[string]*Service
}

// Route is a route the labels write.
type Route struct {
	Name      string          `json:"name"`
	Listeners []string        `json:"listeners,omitempty"`
	Priority  int             `json:"priority,omitempty"`
	Match     *match          `json:"match,omitempty"`
	Middle    json.RawMessage `json:"middleware,omitempty"`
	Handler   json.RawMessage `json:"handler"`
}

type match struct {
	Host       []string `json:"host,omitempty"`
	Path       []string `json:"path,omitempty"`
	PathRegexp []string `json:"path_regexp,omitempty"`
	Method     []string `json:"method,omitempty"`
	Expr       string   `json:"expr,omitempty"`
}

// Service is a layer 4 service the labels write.
type Service struct {
	Name          string   `json:"name"`
	Listeners     []string `json:"listeners"`
	Upstream      string   `json:"upstream"`
	ServerNames   []string `json:"server_names,omitempty"`
	ProxyProtocol string   `json:"proxy_protocol,omitempty"`

	port int
}

// Port returns the port the service sends to, through an upstream of its
// own, or 0 when it sends to the upstream of its source.
func (s *Service) Port() int { return s.port }

// Ports counts the services of s with a port of their own.
func (s *Set) Ports() int {
	n := 0
	for _, svc := range s.Services {
		if svc.port != 0 {
			n++
		}
	}
	return n
}

// Parse reads the labels of m under prefix, which ends in '.', leaving out
// those whose rest is among own, the caller's.
func Parse(m map[string]string, prefix string, own []string, kind Kind) (*Set, error) {
	l := &Set{Routes: map[string]*Route{}, Services: map[string]*Service{}}
	for _, key := range SortedKeys(m) {
		rest, ok := strings.CutPrefix(key, prefix)
		if !ok {
			continue
		}
		value := m[key]
		bad := func(format string, args ...any) error {
			return fmt.Errorf("%s %s: "+format, append([]any{kind.Label, key}, args...)...)
		}
		switch {
		case slices.Contains(own, rest):
		case rest == "port":
			p, err := ParsePort(value)
			if err != nil {
				return nil, bad("%v", err)
			}
			l.Port = p
		case rest == "upstream":
			if err := json.Unmarshal([]byte(value), &l.Upstream); err != nil || l.Upstream == nil {
				return nil, bad("must be a JSON object of an upstream's settings")
			}
			if _, ok := l.Upstream["name"]; ok {
				return nil, bad("name is the %s's: set %sname", kind.Owner, prefix)
			}
			if _, ok := l.Upstream["addresses"]; ok {
				return nil, bad("addresses are the %ss'", kind.Owner)
			}
		case strings.HasPrefix(rest, "routes."):
			k, field, ok := strings.Cut(strings.TrimPrefix(rest, "routes."), ".")
			if !ok || !keyPattern.MatchString(k) {
				return nil, bad("is not %sroutes.<name>.<setting>, with a name of letters, digits, '_' and '-'", prefix)
			}
			r := l.Routes[k]
			if r == nil {
				r = &Route{}
				l.Routes[k] = r
			}
			if err := r.set(field, value); err != nil {
				return nil, bad("%v", err)
			}
		case strings.HasPrefix(rest, "services."):
			k, field, ok := strings.Cut(strings.TrimPrefix(rest, "services."), ".")
			if !ok || !keyPattern.MatchString(k) {
				return nil, bad("is not %sservices.<name>.<setting>, with a name of letters, digits, '_' and '-'", prefix)
			}
			s := l.Services[k]
			if s == nil {
				s = &Service{}
				l.Services[k] = s
			}
			if err := s.set(field, value); err != nil {
				return nil, bad("%v", err)
			}
		default:
			return nil, bad("is not a %s fit reads", kind.Label)
		}
	}
	for k, s := range l.Services {
		if len(s.Listeners) == 0 {
			return nil, fmt.Errorf("service %s: set %sservices.%s.listeners", k, prefix, k)
		}
	}
	return l, nil
}

func (r *Route) set(field, value string) error {
	m := r.Match
	if m == nil {
		m = &match{}
	}
	switch field {
	case "host":
		m.Host = list(value)
	case "path":
		m.Path = list(value)
	case "path_regexp":
		m.PathRegexp = list(value)
	case "method":
		m.Method = list(value)
	case "expr":
		m.Expr = value
	case "priority":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("%q is not a whole number", value)
		}
		r.Priority = n
	case "listeners":
		r.Listeners = list(value)
	case "middleware":
		var a []json.RawMessage
		if err := json.Unmarshal([]byte(value), &a); err != nil {
			return fmt.Errorf("must be a JSON array of middleware, as in the config")
		}
		r.Middle = json.RawMessage(value)
	case "handler":
		var o map[string]json.RawMessage
		if err := json.Unmarshal([]byte(value), &o); err != nil || o == nil {
			return fmt.Errorf("must be a JSON object of a handler, as in the config")
		}
		r.Handler = json.RawMessage(value)
	default:
		return fmt.Errorf("a route has no setting %q", field)
	}
	if m.Host != nil || m.Path != nil || m.PathRegexp != nil || m.Method != nil || m.Expr != "" {
		r.Match = m
	}
	return nil
}

func (s *Service) set(field, value string) error {
	switch field {
	case "listeners":
		s.Listeners = list(value)
	case "port":
		p, err := ParsePort(value)
		if err != nil {
			return err
		}
		s.port = p
	case "server_names":
		s.ServerNames = list(value)
	case "proxy_protocol":
		s.ProxyProtocol = value
	default:
		return fmt.Errorf("a service has no setting %q", field)
	}
	return nil
}

// ParsePort reads a port, 1 to 65535.
func ParsePort(s string) (int, error) {
	p, err := strconv.Atoi(s)
	if err != nil || p < 1 || p > 65535 {
		return 0, fmt.Errorf("%q is not a port", s)
	}
	return p, nil
}

// list splits a comma-separated value, leaving out empty entries.
func list(s string) []string {
	var out []string
	for v := range strings.SplitSeq(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// SortedKeys returns m's keys in order.
func SortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// Group is the sources of one name: one upstream, and the routes and
// services the first of them writes.
type Group struct {
	Set      *Set
	Addrs    []string
	SvcAddrs map[string][]string // the addresses of the services with a port of their own, by key
}

// Part writes the part groups make, by name: an upstream of each, sending
// to its addresses, its routes, proxying to it unless they name a handler,
// and its services, with upstreams of their own for those with a port. The
// part is the same for the same groups, so that an unchanged one is seen
// to be.
func Part(groups map[string]*Group) []byte {
	var out struct {
		Upstreams []map[string]json.RawMessage `json:"upstreams,omitempty"`
		Routes    []*Route                     `json:"routes,omitempty"`
		Services  []*Service                   `json:"services,omitempty"`
	}
	for _, name := range SortedKeys(groups) {
		g := groups[name]
		out.Upstreams = append(out.Upstreams, upstream(name, g.Addrs, g.Set.Upstream))
		for _, key := range SortedKeys(g.Set.Routes) {
			r := *g.Set.Routes[key]
			r.Name = name + "." + key
			if r.Handler == nil {
				r.Handler, _ = json.Marshal(map[string]any{"module": "proxy", "params": map[string]string{"upstream": name}})
			}
			out.Routes = append(out.Routes, &r)
		}
		for _, key := range SortedKeys(g.Set.Services) {
			s := *g.Set.Services[key]
			s.Name = name + "." + key
			s.Upstream = name
			if s.port != 0 {
				s.Upstream = s.Name
				out.Upstreams = append(out.Upstreams, upstream(s.Name, g.SvcAddrs[key], nil))
			}
			out.Services = append(out.Services, &s)
		}
	}
	part, _ := json.Marshal(out)
	return part
}

// upstream is an upstream of name, sending to addrs, with the other
// settings extra gives.
func upstream(name string, addrs []string, extra map[string]json.RawMessage) map[string]json.RawMessage {
	u := make(map[string]json.RawMessage, len(extra)+2)
	for k, v := range extra {
		u[k] = v
	}
	u["name"], _ = json.Marshal(name)
	u["addresses"], _ = json.Marshal(addrs)
	return u
}
