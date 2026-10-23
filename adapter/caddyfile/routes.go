package caddyfile

import (
	"slices"
	"strings"
)

// route is a route being made: its match, the middleware of the blocks
// around it still to be put before its own, and its handler, whose root
// and try_files are settled once every block has had its say.
type route struct {
	match *outMatch
	pre   []mw
	mw    []mw
	h     handler
	line  int
	hint  string
	// got holds the exclusive directives this route took, by name, with
	// the scope that gave them: only the most specific of a scope applies.
	got map[string]int
}

type mw struct {
	ref  outRef
	line int
}

// handler is a route's handler: what answers, and for static files and
// FastCGI, the root and try_files the blocks give it.
type handler struct {
	ref      outRef
	line     int
	root     string
	rootSet  bool
	tryFiles []string
	trySet   bool
	tryLine  int
}

func (r *route) clone() *route {
	c := *r
	c.match = r.match.clone()
	c.pre = slices.Clone(r.pre)
	c.mw = slices.Clone(r.mw)
	c.h.ref.Params = cloneParams(r.h.ref.Params)
	c.h.tryFiles = slices.Clone(r.h.tryFiles)
	c.got = map[string]int{}
	for k, v := range r.got {
		c.got[k] = v
	}
	return &c
}

func cloneParams(p map[string]any) map[string]any {
	if p == nil {
		return nil
	}
	c := make(map[string]any, len(p))
	for k, v := range p {
		c[k] = v
	}
	return c
}

// modifier is a directive that changes the routes it covers: it adds
// middleware, or sets the handler's root or try_files.
type modifier struct {
	d     *directive
	match *outMatch
	spec  int
	order int
	add   *mw
	set   func(*handler) bool
}

// order is Caddy's order of the directives this converter knows.
var order = map[string]int{
	"root": 1, "header": 3, "request_body": 4, "redir": 5, "rewrite": 6, "uri": 7, "try_files": 8,
	"basicauth": 9, "basic_auth": 9, "forward_auth": 10, "request_header": 11, "encode": 12,
	"handle": 13, "handle_path": 14, "route": 15, "abort": 18, "error": 19, "respond": 20,
	"reverse_proxy": 21, "php_fastcgi": 22, "file_server": 23,
}

// exclusive are the directives of which, in one block, only the one with
// the most specific matcher applies to a request, as in Caddy.
var exclusive = map[string]bool{"root": true, "rewrite": true, "uri": true, "try_files": true, "redir": true}

// The bound on the routes one site may make.
const maxSiteRoutes = 2000

// siteRoutes makes a site's routes for a group of its addresses.
func (c *converter) siteRoutes(s *site, g *group) {
	base := &outMatch{}
	for _, h := range g.hosts {
		if h == "" {
			base.Host = nil
			break
		}
		if !slices.Contains(base.Host, h) {
			base.Host = append(base.Host, h)
		}
	}
	if p := g.paths[0]; p != "" {
		if slices.ContainsFunc(g.paths, func(q string) bool { return q != p }) {
			c.warn(s.block.line, "site %s: its addresses have different paths: matched by none", strings.Join(s.block.keys, ", "))
		} else {
			base.Path = []string{p}
		}
	}
	rs := c.scope(s, s.block.dirs, base, false, 0)
	if len(rs) > maxSiteRoutes {
		c.warn(s.block.line, "site %s: skipped: it makes more than %d routes", strings.Join(s.block.keys, ", "), maxSiteRoutes)
		return
	}
	for _, r := range rs {
		c.finish(s, g, r)
	}
}

// finish settles a route's handler and adds it to the config.
func (c *converter) finish(s *site, g *group, r *route) {
	h := r.h.ref
	switch h.Module {
	case "static":
		root := r.h.root
		if !r.h.rootSet {
			root = "."
			c.warn(r.h.line, "file_server with no root: Caddy serves the current directory, written as \".\"")
		}
		h.Params["root"] = root
		if len(r.h.tryFiles) > 0 {
			h.Params["try_files"] = r.h.tryFiles
		}
	case "fastcgi":
		if _, ok := h.Params["root"]; !ok {
			if !r.h.rootSet {
				c.warn(r.h.line, "php_fastcgi: skipped: it needs a root, which fit takes as an absolute path; write root")
				return
			}
			h.Params["root"] = r.h.root
		}
		if _, ok := h.Params["try_files"]; !ok && len(r.h.tryFiles) > 0 {
			h.Params["try_files"] = r.h.tryFiles
		}
	default:
		if r.h.trySet && r.hint != "default" {
			c.warn(r.h.tryLine, "try_files: applies to static files and FastCGI only, not to the %s routes it covers", h.Module)
		}
	}
	out := &outRoute{Name: c.routeName(s.name, r.hint), Listeners: g.listeners, Handler: h, line: r.line, hLine: r.h.line}
	if !r.match.empty() {
		out.Match = r.match
	}
	for _, m := range r.mw {
		out.Middleware = append(out.Middleware, m.ref)
		out.mwLines = append(out.mwLines, m.line)
	}
	c.out.Routes = append(c.out.Routes, out)
}

// scope makes the routes of a block's directives, for the requests base
// matches: a route for each handler and each handle block's routes, and
// for what none of them takes, a route answering 200 with no body, as Caddy
// does. Then the block's other directives apply to the routes they cover,
// splitting a route they cover in part. In a route block, directives keep
// the order they are written in.
func (c *converter) scope(s *site, dirs []*directive, base *outMatch, ordered bool, depth int) []*route {
	c.scopes++
	id := c.scopes
	var rs []*route
	var mods []*modifier
	type terminal struct {
		d     *directive
		order int
	}
	var terms []terminal
	for _, d := range dirs {
		if strings.HasPrefix(d.name, "@") {
			continue
		}
		switch d.name {
		case "tls", "bind", "log":
			if depth > 0 {
				c.skip(d, "it belongs in the site's block")
			}
		case "handle", "handle_path", "route", "respond", "reverse_proxy", "php_fastcgi", "file_server", "error", "abort":
			terms = append(terms, terminal{d, order[d.name]})
		case "root", "header", "request_body", "redir", "rewrite", "uri", "try_files", "basicauth", "basic_auth",
			"forward_auth", "request_header", "encode":
			if m := c.modifier(s, d); m != nil {
				mods = append(mods, m)
			}
		case "metrics":
			c.out.Metrics = &struct{}{}
			c.warn(d.line, "metrics: fit serves them on the Admin API at /metrics, not on the site")
		case "handle_errors":
			c.skip(d, "error pages are not converted")
		case "import":
			c.skip(d, "not converted")
		case "encode_gzip", "gzip":
			c.skip(d, "write encode")
		default:
			c.skip(d, "not converted")
		}
	}
	if !ordered {
		slices.SortStableFunc(terms, func(a, b terminal) int { return a.order - b.order })
	}
	for _, t := range terms {
		rs = append(rs, c.terminal(s, t.d, base, depth)...)
	}
	c.phpWithFiles(rs)
	if !slices.ContainsFunc(rs, func(r *route) bool { return sameMatch(r.match, base) }) {
		rs = append(rs, &route{match: base.clone(), h: handler{ref: outRef{Module: "respond"}}, hint: "default", got: map[string]int{}})
	}
	if !ordered {
		slices.SortStableFunc(mods, func(a, b *modifier) int {
			if a.order != b.order {
				return a.order - b.order
			}
			return b.spec - a.spec
		})
	}
	for _, m := range mods {
		for i := 0; i < len(rs); i++ {
			r := rs[i]
			if !ordered && exclusive[m.d.name] && r.got[m.d.name] == id {
				continue
			}
			both, ok := intersect(r.match, m.match)
			if !ok {
				continue
			}
			target := r
			if !sameMatch(both, r.match) {
				if len(rs) >= maxSiteRoutes {
					c.warn(m.d.line, "%s: not applied: the site makes too many routes", m.d.name)
					break
				}
				target = r.clone()
				target.match = both
				if len(both.Path) > 0 {
					target.hint = both.Path[0]
				}
				rs = slices.Insert(rs, i, target)
				i++
			}
			if m.add != nil {
				target.pre = append(target.pre, *m.add)
			}
			if m.set != nil && !m.set(&target.h) {
				continue
			}
			target.got[m.d.name] = id
		}
	}
	for _, r := range rs {
		r.mw = append(r.pre, r.mw...)
		r.pre = nil
	}
	return rs
}

// matcher takes a directive's matcher off its arguments: "*", a path, or a
// named matcher's name. With single, a lone argument is never a matcher,
// as Caddy reads root, redir, rewrite and respond. It reports false when
// the directive's matcher is one that was not converted.
func (c *converter) matcher(s *site, d *directive, single bool) (*outMatch, []string, int, bool) {
	args := d.args
	if len(args) == 0 || single && len(args) == 1 {
		return nil, args, -1, true
	}
	a := args[0]
	switch {
	case a == "*":
		return nil, args[1:], -1, true
	case strings.HasPrefix(a, "/"):
		m := &outMatch{}
		setPaths(m, []string{a})
		spec := 2 * len(a)
		if !strings.HasSuffix(a, "*") {
			spec++
		}
		return m, args[1:], spec, true
	case strings.HasPrefix(a, "@"):
		if m, ok := s.matchers[a]; ok {
			return m, args[1:], 1 << 20, true
		}
		if _, bad := s.badMatch[a]; !bad {
			c.warn(d.line, "%s: skipped: no matcher %s is defined", d.name, a)
		}
		return nil, nil, 0, false
	}
	return nil, args, -1, true
}

// terminal makes the routes of a directive that answers, or of a block.
func (c *converter) terminal(s *site, d *directive, base *outMatch, depth int) []*route {
	single := d.name == "respond"
	m, args, _, ok := c.matcher(s, d, single)
	if !ok {
		return nil
	}
	match, ok := intersect(base, m)
	if !ok {
		c.warn(d.line, "%s: skipped: its matcher matches nothing its site or block does", d.name)
		return nil
	}
	hint := ""
	if len(match.Path) > 0 {
		hint = match.Path[0]
	}
	switch d.name {
	case "handle", "route":
		if len(args) > 0 {
			c.warn(d.line, "%s: its arguments are not converted", d.name)
		}
		return c.scope(s, d.block, match, d.name == "route", depth+1)
	case "handle_path":
		if m == nil || len(m.Path) != 1 {
			c.skip(d, "handle_path takes one path")
			return nil
		}
		rs := c.scope(s, d.block, match, false, depth+1)
		prefix := strings.TrimSuffix(strings.TrimSuffix(m.Path[0], "*"), "/")
		if prefix != "" {
			strip := mw{outRef{Module: "rewrite", Params: map[string]any{"strip_prefix": []string{prefix}}}, d.line}
			for _, r := range rs {
				r.mw = append([]mw{strip}, r.mw...)
			}
		}
		return rs
	}
	h, extra, ok := c.handler(s, d, args)
	if !ok {
		return nil
	}
	h.line = d.line
	return []*route{{match: match, h: h, mw: extra, line: d.line, hint: hint, got: map[string]int{}}}
}

// phpWithFiles makes php_fastcgi share a block with file_server: fit's
// FastCGI handler takes the paths of scripts and directories, and
// file_server the rest.
func (c *converter) phpWithFiles(rs []*route) {
	for _, p := range rs {
		if p.h.ref.Module != "fastcgi" {
			continue
		}
		for _, f := range rs {
			if f.h.ref.Module != "static" || !sameMatch(f.match, p.match) {
				continue
			}
			p.match, _ = intersect(p.match, &outMatch{PathRegexp: []string{`(?i)\.php(/|$)`, `/$`}})
			delete(p.h.ref.Params, "try_files")
			p.h.ref.Params["try_files"] = []string{"{path}"}
			c.warn(p.h.line, "php_fastcgi with file_server: PHP takes the paths of scripts and directories, file_server the rest; a path that is neither a file nor a script is not sent to index.php")
			break
		}
	}
}
