package kubernetes

import (
	"fmt"
	"regexp"
	"strings"
)

// grpcRoute translates r: each match of each rule is a route of fit's on
// the listeners r is attached to, to the rule's backends over h2c.
func (t *translator) grpcRoute(r *GRPCRoute) {
	t.begin("GRPCRoute", &r.Metadata, r.Spec.ParentRefs, &r.Status)
	groups := t.attach(r.Spec.Hostnames)
	// A route taken only by listeners fit does not serve makes no routes
	// of fit's, but its backends are still resolved, for its status.
	if len(groups) == 0 && !t.cur.accepted() {
		return
	}
	for i, rule := range r.Spec.Rules {
		where := fmt.Sprintf("grpcroute %s: rule %d", r.Metadata.key(), i)
		backend := t.backends(where, rule.BackendRefs, true)
		matches := rule.Matches
		if len(matches) == 0 {
			matches = []GRPCRouteMatch{{}}
		}
		for j, m := range matches {
			for gi, at := range groups {
				name := fmt.Sprintf("%s.%s.grpc.r%d.m%d", r.Metadata.Namespace, r.Metadata.Name, i, j)
				if len(groups) > 1 {
					name += fmt.Sprintf(".g%d", gi)
				}
				if out := t.grpcMatch(fmt.Sprintf("%s: match %d", where, j), m, rule, backend, at); out != nil {
					out.Name = name
					t.routes = append(t.routes, out)
				}
			}
		}
	}
}

// grpcMatch translates m, the match at where of rule, into a route of
// fit's on attachment at, or nil, saying why, when it cannot. A gRPC
// method is the path /<service>/<method>.
func (t *translator) grpcMatch(where string, m GRPCRouteMatch, rule GRPCRouteRule, b ruleBackends, at *attachment) *outRoute {
	out := &outRoute{Listeners: at.listeners}
	om := &outMatch{Host: routeHosts(at.hosts)}
	service, method := "", ""
	if mm := m.Method; mm != nil {
		service, method = deref(mm.Service, ""), deref(mm.Method, "")
		switch typ := deref(mm.Type, "Exact"); typ {
		case "Exact":
			if strings.Contains(service, "/") || strings.Contains(method, "/") {
				t.leftOut("%s: a service or method holds '/'", where)
				return nil
			}
			switch {
			case service != "" && method != "":
				om.Path = []string{"/" + service + "/" + method}
			case service != "":
				om.Path = []string{"/" + service + "/*"}
			case method != "":
				om.PathRegexp = []string{"^/[^/]+/" + regexp.QuoteMeta(method) + "$"}
			}
		case "RegularExpression":
			part := func(re string) string {
				if re == "" {
					return "[^/]+"
				}
				return "(?:" + re + ")"
			}
			re := "^/" + part(service) + "/" + part(method) + "$"
			if _, err := regexp.Compile(re); err != nil {
				t.leftOut("%s: method %q of service %q is not a regular expression fit takes: %v", where, method, service, err)
				return nil
			}
			om.PathRegexp = []string{re}
		default:
			t.leftOut("%s: method match type %s is not supported", where, typ)
			return nil
		}
	}
	for _, h := range m.Headers {
		if deref(h.Type, "Exact") != "Exact" {
			t.leftOut("%s: header %s: match type %s is not supported yet", where, h.Name, deref(h.Type, ""))
			return nil
		}
		if om.Header == nil {
			om.Header = map[string][]string{}
		}
		om.Header[h.Name] = []string{h.Value}
	}
	if om.Host != nil || om.Path != nil || om.PathRegexp != nil || om.Header != nil {
		out.Match = om
	}

	// The Gateway API's precedence for GRPCRoutes, as one priority: the
	// most specific host, then the longest service, the longest method,
	// the most headers.
	hostClass := 0
	for _, h := range at.hosts {
		c := 1
		if !strings.HasPrefix(h, "*") {
			c = 2
		}
		hostClass = max(hostClass, c)
	}
	out.Priority = hostClass*100_000_000 + min(len(service), 4999)*10_000 + min(len(method), 99)*100 + min(len(m.Headers), 16)*6
	t.handle(where, out, rule.Filters, b, "", "")
	return out
}
