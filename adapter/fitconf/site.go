package fitconf

import "slices"

// The directives a site holds: what its routes share, and its routes.
var siteShared = []string{"listeners", "priority", "match", "middleware"}

// expandSites replaces each site in the top-level directives with its
// routes, where it was written, so that routes keep the order they were
// written in.
//
//	site <hosts...> {
//		listeners ...      the routes' listeners, where a route sets none
//		priority <n>       their priority, where a route sets none
//		match { ... }      what every route matches too, but host and path;
//		                   a route's match setting the same field wins
//		middleware ...     middleware every route runs, before its own
//		route [<paths...>] { ... }
//	}
//
// A route's match takes its hosts from the site and its paths from its
// arguments; the rest of the route is as a top-level route's, but that it
// is named with name in its block.
func expandSites(ds []*directive) ([]*directive, error) {
	var out []*directive
	for _, d := range ds {
		if d.name.text != "site" {
			out = append(out, d)
			continue
		}
		routes, err := siteRoutes(d)
		if err != nil {
			return nil, err
		}
		out = append(out, routes...)
	}
	return out, nil
}

func siteRoutes(site *directive) ([]*directive, error) {
	if len(site.args) == 0 {
		return nil, errAt(site.name.line, site.name.col, "a site needs its host names, as in site example.com www.example.com { ... }")
	}
	if !site.hasBlock {
		return nil, errAt(site.name.line, site.name.col, "a site needs a block with its routes")
	}
	shared := map[string]*directive{}
	var middleware, routes []*directive
	for _, d := range site.block {
		switch name := d.name.text; {
		case name == "route":
			routes = append(routes, d)
		case name == "middleware":
			middleware = append(middleware, d)
		case slices.Contains(siteShared, name):
			if shared[name] != nil {
				return nil, errAt(d.name.line, d.name.col, "a site's %s is given twice", name)
			}
			if name == "match" {
				for _, m := range d.block {
					if m.name.text == "host" || m.name.text == "path" {
						return nil, errAt(m.name.line, m.name.col, "a site's routes take their hosts from the site and their paths from their own arguments")
					}
				}
			}
			shared[name] = d
		default:
			return nil, errAt(d.name.line, d.name.col, "a site holds listeners, priority, match, middleware and its routes, not %s", name)
		}
	}
	if len(routes) == 0 {
		return nil, errAt(site.name.line, site.name.col, "a site needs a route, as in route /* { handler ... }")
	}
	out := make([]*directive, 0, len(routes))
	for _, r := range routes {
		if !r.hasBlock {
			return nil, errAt(r.name.line, r.name.col, "a route needs a block with its handler")
		}
		route := &directive{name: token{text: "route", line: r.name.line, col: r.name.col}, hasBlock: true}
		match := &directive{name: token{text: "match", line: r.name.line, col: r.name.col}, hasBlock: true}
		match.block = append(match.block, &directive{name: token{text: "host", line: site.name.line, col: site.name.col}, args: site.args})
		if len(r.args) > 0 {
			match.block = append(match.block, &directive{name: token{text: "path", line: r.name.line, col: r.name.col}, args: r.args})
		}
		var own *directive
		var body, ownMiddleware []*directive
		set := map[string]bool{}
		for _, d := range r.block {
			name := d.name.text
			switch name {
			case "match":
				if own != nil {
					return nil, errAt(d.name.line, d.name.col, "a route's match is given twice")
				}
				own = d
				continue
			case "middleware":
				ownMiddleware = append(ownMiddleware, d)
				continue
			}
			set[name] = true
			body = append(body, d)
		}
		if own != nil {
			for _, m := range own.block {
				switch m.name.text {
				case "host":
					return nil, errAt(m.name.line, m.name.col, "a site's route takes its hosts from the site")
				case "path":
					if len(r.args) > 0 {
						return nil, errAt(m.name.line, m.name.col, "the route's paths are given twice, as its arguments and in its match")
					}
				}
			}
			match.block = append(match.block, own.block...)
		}
		if sm := shared["match"]; sm != nil {
			for _, m := range sm.block {
				if own != nil && slices.ContainsFunc(own.block, func(o *directive) bool { return o.name.text == m.name.text }) {
					continue // the route's own wins
				}
				match.block = append(match.block, m)
			}
		}
		route.block = append(route.block, match)
		for _, name := range []string{"listeners", "priority"} {
			if d := shared[name]; d != nil && !set[name] {
				route.block = append(route.block, d)
			}
		}
		route.block = append(route.block, middleware...)
		route.block = append(route.block, ownMiddleware...)
		route.block = append(route.block, body...)
		out = append(out, route)
	}
	return out, nil
}
