package caddyfile

import (
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// outMatch is a route's match, its fields in the order the native config
// has them. nil, or one with no field set, matches every request.
type outMatch struct {
	Host       []string            `json:"host,omitempty"`
	Path       []string            `json:"path,omitempty"`
	PathRegexp []string            `json:"path_regexp,omitempty"`
	Method     []string            `json:"method,omitempty"`
	Header     map[string][]string `json:"header,omitempty"`
	Query      map[string][]string `json:"query,omitempty"`
	RemoteIP   []string            `json:"remote_ip,omitempty"`
	Expr       string              `json:"expr,omitempty"`
}

func (m *outMatch) empty() bool {
	return m == nil || len(m.Host) == 0 && len(m.Path) == 0 && len(m.PathRegexp) == 0 && len(m.Method) == 0 &&
		len(m.Header) == 0 && len(m.Query) == 0 && len(m.RemoteIP) == 0 && m.Expr == ""
}

func (m *outMatch) clone() *outMatch {
	if m == nil {
		return &outMatch{}
	}
	c := *m
	c.Host, c.Path, c.PathRegexp = slices.Clone(m.Host), slices.Clone(m.Path), slices.Clone(m.PathRegexp)
	c.Method, c.RemoteIP = slices.Clone(m.Method), slices.Clone(m.RemoteIP)
	c.Header, c.Query = cloneValues(m.Header), cloneValues(m.Query)
	return &c
}

func cloneValues(v map[string][]string) map[string][]string {
	if v == nil {
		return nil
	}
	c := make(map[string][]string, len(v))
	for k, vs := range v {
		c[k] = append([]string{}, vs...)
	}
	return c
}

// sameMatch reports whether a and b match the same requests, as written.
func sameMatch(a, b *outMatch) bool {
	if a.empty() || b.empty() {
		return a.empty() == b.empty()
	}
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// intersect returns a match of the requests both a and b match, and false
// when none can match both. What the native fields cannot hold of both is
// added to the match's expression.
func intersect(a, b *outMatch) (*outMatch, bool) {
	r := a.clone()
	if b.empty() {
		return r, true
	}
	var extra []string
	if len(b.Host) > 0 {
		switch {
		case len(r.Host) == 0:
			r.Host = slices.Clone(b.Host)
		case sameSet(r.Host, b.Host):
		case hasWildcard(r.Host) || hasWildcard(b.Host):
			extra = append(extra, call("Host", b.Host...))
		default:
			var both []string
			for _, h := range r.Host {
				if slices.ContainsFunc(b.Host, func(g string) bool { return strings.EqualFold(g, h) }) {
					both = append(both, h)
				}
			}
			if len(both) == 0 {
				return nil, false
			}
			r.Host = both
		}
	}
	if len(b.Path) > 0 {
		if len(r.Path) == 0 {
			r.Path = slices.Clone(b.Path)
		} else {
			var both []string
			for _, p := range r.Path {
				for _, q := range b.Path {
					if x, ok := intersectPath(p, q); ok && !slices.Contains(both, x) {
						both = append(both, x)
					}
				}
			}
			if len(both) == 0 {
				return nil, false
			}
			r.Path = both
		}
	}
	if len(b.PathRegexp) > 0 {
		switch {
		case len(r.PathRegexp) == 0:
			r.PathRegexp = slices.Clone(b.PathRegexp)
		case !sameSet(r.PathRegexp, b.PathRegexp):
			extra = append(extra, call("PathRegexp", b.PathRegexp...))
		}
	}
	if len(b.Method) > 0 {
		if len(r.Method) == 0 {
			r.Method = slices.Clone(b.Method)
		} else {
			var both []string
			for _, m := range r.Method {
				if slices.Contains(b.Method, m) {
					both = append(both, m)
				}
			}
			if len(both) == 0 {
				return nil, false
			}
			r.Method = both
		}
	}
	var ok bool
	if r.Header, ok = intersectValues(r.Header, b.Header, true); !ok {
		return nil, false
	}
	if r.Query, ok = intersectValues(r.Query, b.Query, false); !ok {
		return nil, false
	}
	if len(b.RemoteIP) > 0 {
		switch {
		case len(r.RemoteIP) == 0:
			r.RemoteIP = slices.Clone(b.RemoteIP)
		case !sameSet(r.RemoteIP, b.RemoteIP):
			extra = append(extra, call("RemoteIP", b.RemoteIP...))
		}
	}
	if b.Expr != "" && b.Expr != r.Expr {
		extra = append(extra, b.Expr)
	}
	if len(extra) > 0 {
		r.Expr = and(append([]string{r.Expr}, extra...)...)
	}
	return r, true
}

// intersectPath returns the paths, exact or a prefix ending in "*", that
// both p and q match, if there are any, as one path.
func intersectPath(p, q string) (string, bool) {
	pp, qp := strings.HasSuffix(p, "*"), strings.HasSuffix(q, "*")
	switch {
	case !pp && !qp:
		return p, p == q
	case !pp:
		return p, strings.HasPrefix(p, q[:len(q)-1])
	case !qp:
		return q, strings.HasPrefix(q, p[:len(p)-1])
	}
	a, b := p[:len(p)-1], q[:len(q)-1]
	switch {
	case strings.HasPrefix(a, b):
		return p, true
	case strings.HasPrefix(b, a):
		return q, true
	}
	return "", false
}

// intersectValues intersects headers (with names that ignore case) or
// query parameters: a name both have takes the values both allow, an
// empty list allowing any.
func intersectValues(a, b map[string][]string, fold bool) (map[string][]string, bool) {
	if len(b) == 0 {
		return a, true
	}
	if a == nil {
		a = map[string][]string{}
	}
	for k, vs := range b {
		key := k
		if fold {
			for ak := range a {
				if strings.EqualFold(ak, k) {
					key = ak
				}
			}
		}
		have, ok := a[key]
		switch {
		case !ok || len(have) == 0:
			a[key] = append([]string{}, vs...)
		case len(vs) == 0:
		default:
			var both []string
			for _, v := range have {
				if slices.Contains(vs, v) {
					both = append(both, v)
				}
			}
			if len(both) == 0 {
				return nil, false
			}
			a[key] = both
		}
	}
	return a, true
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, x := range a {
		if !slices.Contains(b, x) {
			return false
		}
	}
	return true
}

func hasWildcard(hosts []string) bool {
	return slices.ContainsFunc(hosts, func(h string) bool { return strings.HasPrefix(h, "*") })
}

// call writes a call of one of the expression's matchers.
func call(name string, args ...string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = strconv.Quote(a)
	}
	return name + "(" + strings.Join(q, ", ") + ")"
}

// and joins expressions with &&, those with || in parentheses.
func and(exprs ...string) string {
	var parts []string
	for _, e := range exprs {
		if e == "" {
			continue
		}
		if strings.Contains(e, "||") && !(strings.HasPrefix(e, "(") && strings.HasSuffix(e, ")")) {
			e = "(" + e + ")"
		}
		parts = append(parts, e)
	}
	return strings.Join(parts, " && ")
}

// expr writes m as an expression, or "" for a match of every request.
func expr(m *outMatch) string {
	if m.empty() {
		return ""
	}
	var terms []string
	if len(m.Host) > 0 {
		terms = append(terms, call("Host", m.Host...))
	}
	if len(m.Path) > 0 {
		var exact, prefix []string
		for _, p := range m.Path {
			if s, ok := strings.CutSuffix(p, "*"); ok {
				prefix = append(prefix, s)
			} else {
				exact = append(exact, p)
			}
		}
		switch {
		case len(exact) > 0 && len(prefix) > 0:
			terms = append(terms, "("+call("Path", exact...)+" || "+call("PathPrefix", prefix...)+")")
		case len(exact) > 0:
			terms = append(terms, call("Path", exact...))
		default:
			terms = append(terms, call("PathPrefix", prefix...))
		}
	}
	if len(m.PathRegexp) > 0 {
		terms = append(terms, call("PathRegexp", m.PathRegexp...))
	}
	if len(m.Method) > 0 {
		terms = append(terms, call("Method", m.Method...))
	}
	for _, k := range sortedKeys(m.Header) {
		terms = append(terms, call("Header", append([]string{k}, m.Header[k]...)...))
	}
	for _, k := range sortedKeys(m.Query) {
		terms = append(terms, call("Query", append([]string{k}, m.Query[k]...)...))
	}
	if len(m.RemoteIP) > 0 {
		terms = append(terms, call("RemoteIP", m.RemoteIP...))
	}
	if m.Expr != "" {
		terms = append(terms, m.Expr)
	}
	return and(terms...)
}

func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// privateRanges are what Caddy's private_ranges stands for.
var privateRanges = []string{"192.168.0.0/16", "172.16.0.0/12", "10.0.0.0/8", "127.0.0.1/8", "fd00::/8", "::1"}

// ranges replaces private_ranges with the ranges it stands for.
func ranges(args []string) []string {
	var out []string
	for _, a := range args {
		if a == "private_ranges" {
			out = append(out, privateRanges...)
		} else {
			out = append(out, a)
		}
	}
	return out
}

// simplePath reports whether a path of Caddy's is one fit's path matcher
// takes as it is: exact, or a prefix ending in "*".
func simplePath(p string) bool {
	return strings.HasPrefix(p, "/") && !strings.Contains(strings.TrimSuffix(p, "*"), "*")
}

// pathRegexp writes a path of Caddy's, whose "*" stands for anything, as a
// regular expression.
func pathRegexp(p string) string {
	parts := strings.Split(p, "*")
	for i, s := range parts {
		parts[i] = regexp.QuoteMeta(s)
	}
	return "^" + strings.Join(parts, ".*") + "$"
}

// paths puts Caddy's paths in m: as paths when fit takes them all as they
// are, and as regular expressions otherwise.
func setPaths(m *outMatch, paths []string) {
	if len(paths) == 0 {
		return
	}
	if !slices.ContainsFunc(paths, func(p string) bool { return !simplePath(p) }) {
		m.Path = paths
		return
	}
	var res []string
	for _, p := range paths {
		res = append(res, pathRegexp(p))
	}
	if len(m.PathRegexp) > 0 {
		m.Expr = and(m.Expr, call("PathRegexp", res...))
		return
	}
	m.PathRegexp = res
}

// matcherTypes are the matchers a named matcher may use, those converted
// and those not.
var matcherTypes = map[string]bool{
	"path": true, "path_regexp": true, "host": true, "method": true, "header": true, "query": true,
	"remote_ip": true, "client_ip": true, "not": true, "expression": true, "protocol": true, "file": true,
	"header_regexp": true, "vars": true, "vars_regexp": true, "tls": true,
}

// namedMatcher converts a named matcher's definition, @name with its
// matchers on its line or in its block. A block after a matcher's name,
// as in @name not { ... }, is that matcher's. It returns why not, when it
// cannot.
func namedMatcher(d *directive) (*outMatch, string) {
	lines := d.block
	if !d.isBlock || len(d.args) > 0 {
		if len(d.args) == 0 {
			return nil, "it has no matchers"
		}
		if !matcherTypes[d.args[0]] {
			return nil, "a CEL expression is not converted"
		}
		lines = []*directive{{name: d.args[0], args: d.args[1:], line: d.line, block: d.block, isBlock: d.isBlock}}
	}
	return matcherSet(lines)
}

// matcherSet converts the lines of a named matcher, all of which a request
// must match.
func matcherSet(lines []*directive) (*outMatch, string) {
	m := &outMatch{}
	var paths []string
	var exprs []string
	for _, l := range lines {
		switch l.name {
		case "path":
			if len(l.args) == 0 {
				return nil, "path with no paths"
			}
			for _, p := range l.args {
				if strings.Contains(p, "%") || strings.Contains(p, "{") {
					return nil, "a path with escapes or placeholders is not converted"
				}
				if !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "*") {
					return nil, "path " + p + " starts with neither / nor *"
				}
			}
			paths = append(paths, l.args...)
		case "path_regexp":
			re := ""
			switch len(l.args) {
			case 1:
				re = l.args[0]
			case 2:
				re = l.args[1]
			default:
				return nil, "path_regexp takes a name and a regular expression"
			}
			if _, err := regexp.Compile(re); err != nil {
				return nil, "Go's regular expressions do not take " + strconv.Quote(re)
			}
			m.PathRegexp = append(m.PathRegexp, re)
		case "host":
			if len(l.args) == 0 {
				return nil, "host with no names"
			}
			for _, h := range l.args {
				if strings.Contains(h, "{") || strings.Count(h, "*") > 1 || strings.Contains(h, "*") && !strings.HasPrefix(h, "*.") {
					return nil, "host " + h + " is not converted: fit takes names and a leading \"*.\""
				}
				m.Host = append(m.Host, strings.ToLower(h))
			}
		case "method":
			if len(l.args) == 0 {
				return nil, "method with no methods"
			}
			for _, x := range l.args {
				m.Method = append(m.Method, strings.ToUpper(x))
			}
		case "header":
			if len(l.args) == 0 {
				return nil, "header with no field"
			}
			name := l.args[0]
			if neg, ok := strings.CutPrefix(name, "!"); ok {
				exprs = append(exprs, "!"+call("Header", neg))
				continue
			}
			if m.Header == nil {
				m.Header = map[string][]string{}
			}
			vals := m.Header[name]
			if vals == nil {
				vals = []string{}
			}
			for _, v := range l.args[1:] {
				if v == "*" {
					continue
				}
				if strings.ContainsAny(v, "*{") {
					return nil, "a header value with wildcards or placeholders is not converted"
				}
				vals = append(vals, v)
			}
			m.Header[name] = vals
		case "query":
			if len(l.args) == 0 {
				return nil, "query with no parameters"
			}
			for _, a := range l.args {
				k, v, _ := strings.Cut(a, "=")
				if k == "" || strings.Contains(v, "{") {
					return nil, "query " + a + " is not converted"
				}
				if m.Query == nil {
					m.Query = map[string][]string{}
				}
				if m.Query[k] == nil {
					m.Query[k] = []string{}
				}
				if v != "*" && v != "" {
					m.Query[k] = append(m.Query[k], v)
				}
			}
		case "remote_ip", "client_ip":
			args := l.args
			if len(args) > 0 && args[0] == "forwarded" {
				return nil, "remote_ip forwarded is not converted: fit matches the connection's address"
			}
			if len(args) == 0 {
				return nil, l.name + " with no ranges"
			}
			m.RemoteIP = append(m.RemoteIP, ranges(args)...)
		case "not":
			sub := l.block
			if !l.isBlock {
				if len(l.args) == 0 {
					return nil, "not with no matcher"
				}
				sub = []*directive{{name: l.args[0], args: l.args[1:], line: l.line}}
			}
			n, why := matcherSet(sub)
			if why != "" {
				return nil, why
			}
			e := expr(n)
			if e == "" {
				return nil, "not with no matcher"
			}
			exprs = append(exprs, "!("+e+")")
		case "expression":
			return nil, "CEL expressions are not converted"
		default:
			return nil, "the " + l.name + " matcher is not converted"
		}
	}
	setPaths(m, paths)
	if len(exprs) > 0 {
		m.Expr = and(append([]string{m.Expr}, exprs...)...)
	}
	return m, ""
}
