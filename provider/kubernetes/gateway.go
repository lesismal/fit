package kubernetes

import (
	"cmp"
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/lesismal/fit/config"
)

const gatewayGroup = "gateway.networking.k8s.io"

// snapshot is what the informers hold, at one moment.
type snapshot struct {
	namespaces map[string]*Namespace
	classes    []*GatewayClass
	gateways   []*Gateway
	routes     []*HTTPRoute
	grants     []*ReferenceGrant
	services   map[string]*Service         // by namespace/name
	slices     map[string][]*EndpointSlice // by namespace/service
}

func newSnapshot(namespaces []*Namespace, classes []*GatewayClass, gateways []*Gateway, routes []*HTTPRoute,
	grants []*ReferenceGrant, services []*Service, slices []*EndpointSlice) *snapshot {
	s := &snapshot{namespaces: map[string]*Namespace{}, classes: classes, gateways: gateways, routes: routes, grants: grants,
		services: map[string]*Service{}, slices: map[string][]*EndpointSlice{}}
	for _, n := range namespaces {
		s.namespaces[n.Metadata.Name] = n
	}
	for _, sv := range services {
		s.services[sv.Metadata.key()] = sv
	}
	for _, es := range slices {
		if svc := es.Metadata.Labels["kubernetes.io/service-name"]; svc != "" {
			k := es.Metadata.Namespace + "/" + svc
			s.slices[k] = append(s.slices[k], es)
		}
	}
	return s
}

// The JSON of the part's elements.
type (
	outListener struct {
		Name      string   `json:"name"`
		Address   string   `json:"address"`
		Protocols []string `json:"protocols"`
	}
	outUpstream struct {
		Name      string   `json:"name"`
		Addresses []string `json:"addresses"`
		Protocol  string   `json:"protocol,omitempty"`
	}
	outRoute struct {
		Name       string            `json:"name"`
		Listeners  []string          `json:"listeners"`
		Priority   int               `json:"priority,omitempty"`
		Match      *outMatch         `json:"match,omitempty"`
		Middleware []json.RawMessage `json:"middleware,omitempty"`
		Handler    json.RawMessage   `json:"handler"`
	}
	outMatch struct {
		Host       []string            `json:"host,omitempty"`
		Path       []string            `json:"path,omitempty"`
		PathRegexp []string            `json:"path_regexp,omitempty"`
		Method     []string            `json:"method,omitempty"`
		Header     map[string][]string `json:"header,omitempty"`
		Query      map[string][]string `json:"query,omitempty"`
	}
)

// translator turns a snapshot into a part.
type translator struct {
	opts     *config.KubernetesProvider
	provider string
	s        *snapshot

	problems  []string
	listeners map[int]*outListener
	upstreams map[string]*outUpstream
	routes    []*outRoute
}

// translate turns s into a part: a listener for each port of fit's
// Gateways' HTTP listeners, upstreams for the Services their HTTPRoutes
// send to, and routes for those HTTPRoutes' rules. It says why it left out
// what it did. The part is the same bytes for the same objects.
func translate(provider string, opts *config.KubernetesProvider, s *snapshot) (part []byte, problems []string) {
	t := &translator{opts: opts, provider: provider, s: s, listeners: map[int]*outListener{}, upstreams: map[string]*outUpstream{}}
	gateways := t.gateways()
	routes := slices.Clone(s.routes)
	slices.SortFunc(routes, func(a, b *HTTPRoute) int {
		return cmp.Or(strings.Compare(a.Metadata.CreationTimestamp, b.Metadata.CreationTimestamp),
			strings.Compare(a.Metadata.Namespace, b.Metadata.Namespace), strings.Compare(a.Metadata.Name, b.Metadata.Name))
	})
	for _, r := range routes {
		if t.watched(r.Metadata.Namespace) {
			t.route(r, gateways)
		}
	}

	var out struct {
		Listeners []*outListener `json:"listeners,omitempty"`
		Upstreams []*outUpstream `json:"upstreams,omitempty"`
		Routes    []*outRoute    `json:"routes,omitempty"`
	}
	for _, port := range sortedKeys(t.listeners) {
		out.Listeners = append(out.Listeners, t.listeners[port])
	}
	for _, name := range sortedKeys(t.upstreams) {
		out.Upstreams = append(out.Upstreams, t.upstreams[name])
	}
	out.Routes = t.routes
	part, _ = json.Marshal(out)
	return part, t.problems
}

func sortedKeys[K cmp.Ordered, V any](m map[K]V) []K {
	keys := make([]K, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func (t *translator) problem(format string, args ...any) {
	t.problems = append(t.problems, fmt.Sprintf(format, args...))
}

func (t *translator) watched(ns string) bool {
	return len(t.opts.Namespaces) == 0 || slices.Contains(t.opts.Namespaces, ns)
}

// gateway is one of fit's Gateways, by namespace/name.
type gateway struct {
	g *Gateway
	// listeners are its HTTP listeners, with the name of fit's listener
	// on each one's port.
	listeners []gwListener
}

type gwListener struct {
	l   Listener
	fit string
}

// gateways returns fit's Gateways: those of GatewayClasses whose
// controllerName is fit's, in the namespaces watched. It makes a listener
// for each port of their HTTP listeners.
func (t *translator) gateways() map[string]*gateway {
	classes := map[string]bool{}
	for _, c := range t.s.classes {
		if c.Spec.ControllerName == t.opts.Controller() {
			classes[c.Metadata.Name] = true
		}
	}
	gws := slices.Clone(t.s.gateways)
	slices.SortFunc(gws, func(a, b *Gateway) int { return strings.Compare(a.Metadata.key(), b.Metadata.key()) })
	out := map[string]*gateway{}
	for _, g := range gws {
		if !classes[g.Spec.GatewayClassName] || !t.watched(g.Metadata.Namespace) {
			continue
		}
		gw := &gateway{g: g}
		for _, l := range g.Spec.Listeners {
			if l.Protocol != "HTTP" {
				t.problem("gateway %s: listener %s: protocol %s is not served yet", g.Metadata.key(), l.Name, l.Protocol)
				continue
			}
			if l.Port < 1 || l.Port > 65535 {
				t.problem("gateway %s: listener %s: %d is not a port", g.Metadata.key(), l.Name, l.Port)
				continue
			}
			fl := t.listeners[l.Port]
			if fl == nil {
				fl = &outListener{Name: t.provider + "-" + strconv.Itoa(l.Port),
					Address: net.JoinHostPort(t.opts.ListenHost, strconv.Itoa(l.Port)), Protocols: []string{"http/1.1", "h2c"}}
				t.listeners[l.Port] = fl
			}
			gw.listeners = append(gw.listeners, gwListener{l: l, fit: fl.Name})
		}
		out[g.Metadata.key()] = gw
	}
	return out
}

// attachment is where a route's rules are served: fit's listeners, for the
// host names hosts, or any host when hosts is nil.
type attachment struct {
	listeners []string
	hosts     []string
}

// route translates r, attached to gateways.
func (t *translator) route(r *HTTPRoute, gateways map[string]*gateway) {
	key := r.Metadata.key()
	// The hosts each fit listener serves r for; nil for any.
	hosts := map[string][]string{}
	anyHost := map[string]bool{}
	ours := false
	for _, ref := range r.Spec.ParentRefs {
		if deref(ref.Group, gatewayGroup) != gatewayGroup || deref(ref.Kind, "Gateway") != "Gateway" {
			continue
		}
		gw := gateways[deref(ref.Namespace, r.Metadata.Namespace)+"/"+ref.Name]
		if gw == nil {
			continue // not fit's
		}
		ours = true
		for _, gl := range gw.listeners {
			l := gl.l
			if ref.SectionName != nil && *ref.SectionName != l.Name || ref.Port != nil && *ref.Port != l.Port {
				continue
			}
			if !t.allowed(l, gw.g, r) {
				continue
			}
			hs, ok := intersect(l.Hostname, r.Spec.Hostnames)
			if !ok {
				continue
			}
			if hs == nil {
				anyHost[gl.fit] = true
			} else {
				hosts[gl.fit] = append(hosts[gl.fit], hs...)
			}
		}
	}
	if !ours {
		return
	}
	// Listeners serving the same hosts share routes.
	groups := map[string]*attachment{}
	for _, fl := range sortedKeys(union(hosts, anyHost)) {
		var hs []string
		if !anyHost[fl] {
			hs = slices.Compact(slices.Sorted(slices.Values(hosts[fl])))
		}
		k := strings.Join(hs, ",")
		if groups[k] == nil {
			groups[k] = &attachment{hosts: hs}
		}
		groups[k].listeners = append(groups[k].listeners, fl)
	}
	if len(groups) == 0 {
		t.problem("httproute %s: no listener of its Gateways takes it: their sections, ports, allowed routes or host names leave it out", key)
		return
	}
	gkeys := sortedKeys(groups)
	for i, rule := range r.Spec.Rules {
		backend := t.backends(r, i, rule)
		matches := rule.Matches
		if len(matches) == 0 {
			matches = []HTTPRouteMatch{{}}
		}
		for j, m := range matches {
			for gi, gk := range gkeys {
				name := fmt.Sprintf("%s.%s.r%d.m%d", r.Metadata.Namespace, r.Metadata.Name, i, j)
				if len(gkeys) > 1 {
					name += fmt.Sprintf(".g%d", gi)
				}
				if out := t.match(r, i, j, m, rule, backend, groups[gk]); out != nil {
					out.Name = name
					t.routes = append(t.routes, out)
				}
			}
		}
	}
}

func union(a map[string][]string, b map[string]bool) map[string]bool {
	out := map[string]bool{}
	for k := range a {
		out[k] = true
	}
	for k := range b {
		out[k] = true
	}
	return out
}

func deref[T any](p *T, def T) T {
	if p == nil {
		return def
	}
	return *p
}

// allowed reports whether listener l of gateway g takes route r: its
// allowedRoutes' namespaces and kinds.
func (t *translator) allowed(l Listener, g *Gateway, r *HTTPRoute) bool {
	from := "Same"
	var sel *LabelSelector
	if a := l.AllowedRoutes; a != nil {
		if a.Namespaces != nil {
			from = cmp.Or(a.Namespaces.From, "Same")
			sel = a.Namespaces.Selector
		}
		if len(a.Kinds) > 0 && !slices.ContainsFunc(a.Kinds, func(k struct {
			Group *string `json:"group,omitempty"`
			Kind  string  `json:"kind"`
		}) bool {
			return deref(k.Group, gatewayGroup) == gatewayGroup && k.Kind == "HTTPRoute"
		}) {
			return false
		}
	}
	switch from {
	case "All":
		return true
	case "Selector":
		ns := t.s.namespaces[r.Metadata.Namespace]
		if ns == nil || sel == nil {
			return false
		}
		return selects(sel, ns.Metadata.Labels)
	default:
		return r.Metadata.Namespace == g.Metadata.Namespace
	}
}

// selects reports whether sel selects labels.
func selects(sel *LabelSelector, labels map[string]string) bool {
	for k, v := range sel.MatchLabels {
		if got, ok := labels[k]; !ok || got != v {
			return false
		}
	}
	for _, e := range sel.MatchExpressions {
		v, has := labels[e.Key]
		switch e.Operator {
		case "In":
			if !has || !slices.Contains(e.Values, v) {
				return false
			}
		case "NotIn":
			if has && slices.Contains(e.Values, v) {
				return false
			}
		case "Exists":
			if !has {
				return false
			}
		case "DoesNotExist":
			if has {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// intersect returns the host names a route of hostnames is served for on
// a listener of hostname listener: nil for any, and false for none.
func intersect(listener *string, hostnames []string) ([]string, bool) {
	l := deref(listener, "")
	switch {
	case l == "" && len(hostnames) == 0:
		return nil, true
	case l == "":
		return hostnames, true
	case len(hostnames) == 0:
		return []string{l}, true
	}
	var out []string
	for _, h := range hostnames {
		switch {
		case hostMatches(l, h):
			out = append(out, h)
		case hostMatches(h, l):
			out = append(out, l)
		}
	}
	return out, len(out) > 0
}

// hostMatches reports whether the host name pattern p, maybe a wildcard,
// takes h: equal, or h below p's domain for "*.".
func hostMatches(p, h string) bool {
	if p == h {
		return true
	}
	if suffix, ok := strings.CutPrefix(p, "*"); ok {
		return strings.HasSuffix(h, suffix) && len(h) > len(suffix) && !strings.HasPrefix(h, "*")
	}
	return false
}

// ruleBackends is where a rule's requests go.
type ruleBackends struct {
	splits []split
	// status answers the requests when splits is empty: 500 for backends
	// that are not valid, 503 for valid ones with no endpoints ready.
	status int
}

type split struct {
	upstream string
	weight   int
}

// backends resolves rule i's backendRefs of route r.
func (t *translator) backends(r *HTTPRoute, i int, rule HTTPRouteRule) ruleBackends {
	var b ruleBackends
	invalid := false
	for k, ref := range rule.BackendRefs {
		w := deref(ref.Weight, 1)
		if w == 0 {
			continue
		}
		if len(ref.Filters) > 0 {
			t.problem("httproute %s: rule %d: backend %d: filters on a backend are not supported yet; they are left out", r.Metadata.key(), i, k)
		}
		name, ok, err := t.upstream(r, ref.BackendObjectRef)
		switch {
		case err != nil:
			t.problem("httproute %s: rule %d: backend %d: %v", r.Metadata.key(), i, k, err)
			invalid = true
		case ok:
			b.splits = append(b.splits, split{name, min(w, 1000)})
		}
	}
	switch {
	case len(b.splits) > 0:
	case invalid || len(rule.BackendRefs) == 0:
		b.status = 500
	default:
		b.status = 503
	}
	return b
}

// upstream makes the upstream of the Service port ref names, for route r,
// and returns its name; ok is false when it has no endpoints ready.
func (t *translator) upstream(r *HTTPRoute, ref BackendObjectRef) (name string, ok bool, err error) {
	if deref(ref.Group, "") != "" || deref(ref.Kind, "Service") != "Service" {
		return "", false, fmt.Errorf("%s/%s is not a Service", deref(ref.Group, ""), deref(ref.Kind, "Service"))
	}
	ns := deref(ref.Namespace, r.Metadata.Namespace)
	if ns != r.Metadata.Namespace && !t.granted(r.Metadata.Namespace, ns, ref.Name) {
		return "", false, fmt.Errorf("service %s/%s is in another namespace, and no ReferenceGrant there allows it", ns, ref.Name)
	}
	if ref.Port == nil {
		return "", false, fmt.Errorf("service %s/%s: a port is needed", ns, ref.Name)
	}
	svc := t.s.services[ns+"/"+ref.Name]
	if svc == nil {
		return "", false, fmt.Errorf("service %s/%s does not exist", ns, ref.Name)
	}
	var sp *ServicePort
	for i := range svc.Spec.Ports {
		if svc.Spec.Ports[i].Port == *ref.Port && cmp.Or(svc.Spec.Ports[i].Protocol, "TCP") == "TCP" {
			sp = &svc.Spec.Ports[i]
		}
	}
	if sp == nil {
		return "", false, fmt.Errorf("service %s/%s has no TCP port %d", ns, ref.Name, *ref.Port)
	}
	name = fmt.Sprintf("%s.%s.%d", ns, ref.Name, sp.Port)
	if _, done := t.upstreams[name]; done {
		return name, true, nil
	}
	addrs := t.addresses(svc, sp)
	if len(addrs) == 0 {
		return name, false, nil
	}
	u := &outUpstream{Name: name, Addresses: addrs}
	if deref(sp.AppProtocol, "") == "kubernetes.io/h2c" {
		u.Protocol = "h2c"
	}
	t.upstreams[name] = u
	return name, true, nil
}

// addresses returns where svc's port sp is reached: its cluster IP, with
// backends cluster_ip, or else its ready endpoints; an ExternalName's name.
func (t *translator) addresses(svc *Service, sp *ServicePort) []string {
	port := strconv.Itoa(sp.Port)
	if svc.Spec.Type == "ExternalName" {
		if svc.Spec.ExternalName == "" {
			return nil
		}
		return []string{net.JoinHostPort(svc.Spec.ExternalName, port)}
	}
	if t.opts.BackendMode() == config.KubernetesBackendsClusterIP && svc.Spec.ClusterIP != "" && svc.Spec.ClusterIP != "None" {
		return []string{net.JoinHostPort(svc.Spec.ClusterIP, port)}
	}
	var out []string
	for _, es := range t.s.slices[svc.Metadata.key()] {
		target := 0
		for _, p := range es.Ports {
			if deref(p.Name, "") == sp.Name && deref(p.Protocol, "TCP") == "TCP" && p.Port != nil {
				target = *p.Port
			}
		}
		if target == 0 {
			continue
		}
		for _, e := range es.Endpoints {
			if !deref(e.Conditions.Ready, true) || len(e.Addresses) == 0 {
				continue
			}
			out = append(out, net.JoinHostPort(e.Addresses[0], strconv.Itoa(target)))
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// granted reports whether a ReferenceGrant in namespace to lets HTTPRoutes
// of namespace from refer to its Service name.
func (t *translator) granted(from, to, name string) bool {
	for _, g := range t.s.grants {
		if g.Metadata.Namespace != to {
			continue
		}
		okFrom := slices.ContainsFunc(g.Spec.From, func(f struct {
			Group     string `json:"group"`
			Kind      string `json:"kind"`
			Namespace string `json:"namespace"`
		}) bool {
			return f.Group == gatewayGroup && f.Kind == "HTTPRoute" && f.Namespace == from
		})
		okTo := slices.ContainsFunc(g.Spec.To, func(tt struct {
			Group string  `json:"group"`
			Kind  string  `json:"kind"`
			Name  *string `json:"name,omitempty"`
		}) bool {
			return tt.Group == "" && tt.Kind == "Service" && (tt.Name == nil || *tt.Name == name)
		})
		if okFrom && okTo {
			return true
		}
	}
	return false
}

// match translates match j of rule i of route r into a route of fit's on
// attachment at, or nil, saying why, when it cannot.
func (t *translator) match(r *HTTPRoute, i, j int, m HTTPRouteMatch, rule HTTPRouteRule, b ruleBackends, at *attachment) *outRoute {
	where := fmt.Sprintf("httproute %s: rule %d: match %d", r.Metadata.key(), i, j)
	out := &outRoute{Listeners: at.listeners}
	om := &outMatch{Host: at.hosts}

	pathType, pathValue := "PathPrefix", "/"
	if m.Path != nil {
		pathType, pathValue = deref(m.Path.Type, "PathPrefix"), deref(m.Path.Value, "/")
	}
	pathClass, prefix := 0, ""
	switch pathType {
	case "Exact":
		if !strings.HasPrefix(pathValue, "/") {
			t.problem("%s: path %q does not start with '/'", where, pathValue)
			return nil
		}
		om.Path, pathClass = []string{pathValue}, 3
	case "PathPrefix":
		if !strings.HasPrefix(pathValue, "/") {
			t.problem("%s: path %q does not start with '/'", where, pathValue)
			return nil
		}
		prefix = strings.TrimRight(pathValue, "/")
		if prefix != "" {
			om.Path = []string{prefix, prefix + "/*"}
		}
		pathClass = 2
	case "RegularExpression":
		if _, err := regexp.Compile(pathValue); err != nil {
			t.problem("%s: path %q is not a regular expression fit takes: %v", where, pathValue, err)
			return nil
		}
		om.PathRegexp, pathClass = []string{"^(?:" + pathValue + ")$"}, 1
	default:
		t.problem("%s: path type %s is not supported", where, pathType)
		return nil
	}
	for _, h := range m.Headers {
		if deref(h.Type, "Exact") != "Exact" {
			t.problem("%s: header %s: match type %s is not supported yet", where, h.Name, deref(h.Type, ""))
			return nil
		}
		if om.Header == nil {
			om.Header = map[string][]string{}
		}
		om.Header[h.Name] = []string{h.Value}
	}
	for _, q := range m.QueryParams {
		if deref(q.Type, "Exact") != "Exact" {
			t.problem("%s: query parameter %s: match type %s is not supported yet", where, q.Name, deref(q.Type, ""))
			return nil
		}
		if om.Query == nil {
			om.Query = map[string][]string{}
		}
		om.Query[q.Name] = []string{q.Value}
	}
	if m.Method != nil {
		om.Method = []string{*m.Method}
	}
	if om.Host != nil || om.Path != nil || om.PathRegexp != nil || om.Method != nil || om.Header != nil || om.Query != nil {
		out.Match = om
	}

	// The Gateway API's precedence, as one priority: the most specific
	// host, then an exact path, the longest prefix, a method, the most
	// headers, the most query parameters. Equal ones keep the order of
	// the routes' creation, which the part is written in.
	hostClass := 0
	for _, h := range at.hosts {
		c := 1
		if !strings.HasPrefix(h, "*") {
			c = 2
		}
		hostClass = max(hostClass, c)
	}
	out.Priority = hostClass*100_000_000 + pathClass*10_000_000 + min(len(pathValue), 9999)*1000 +
		min(len(m.Headers), 16)*30 + min(len(m.QueryParams), 16)
	if m.Method != nil {
		out.Priority += 500
	}

	// Filters, then the backends.
	reqHeaders, respHeaders := map[string]any{}, map[string]any{}
	proxyParams := map[string]any{}
	fail := 0
	for _, f := range rule.Filters {
		switch f.Type {
		case "RequestHeaderModifier":
			if f.RequestHeaderModifier != nil {
				headerOps(reqHeaders, f.RequestHeaderModifier)
			}
		case "ResponseHeaderModifier":
			if f.ResponseHeaderModifier != nil {
				headerOps(respHeaders, f.ResponseHeaderModifier)
			}
		case "URLRewrite":
			rw := f.URLRewrite
			if rw == nil {
				continue
			}
			if rw.Hostname != nil {
				proxyParams["host_header"] = *rw.Hostname
			}
			if mw, err := rewrite(rw.Path, pathType, prefix); err != nil {
				t.problem("%s: URLRewrite: %v", where, err)
				fail = 500
			} else if mw != nil {
				out.Middleware = append(out.Middleware, mw)
			}
		case "RequestRedirect":
			if f.RequestRedirect == nil {
				continue
			}
			mw, err := redirect(f.RequestRedirect, pathType, prefix)
			if err != nil {
				t.problem("%s: RequestRedirect: %v", where, err)
				fail = 500
				continue
			}
			out.Middleware = append(out.Middleware, mw)
		case "RequestMirror":
			rm := f.RequestMirror
			if rm == nil {
				continue
			}
			if _, has := proxyParams["mirror"]; has {
				t.problem("%s: only one RequestMirror is supported; the others are left out", where)
				continue
			}
			name, ok, err := t.upstream(r, rm.BackendRef)
			if err != nil || !ok {
				if err == nil {
					err = fmt.Errorf("it has no endpoints ready")
				}
				t.problem("%s: RequestMirror: %v; no copies are sent", where, err)
				continue
			}
			mirror := map[string]any{"upstream": name}
			switch {
			case rm.Percent != nil:
				mirror["percent"] = *rm.Percent
			case rm.Fraction != nil && deref(rm.Fraction.Denominator, 100) > 0:
				mirror["percent"] = 100 * float64(rm.Fraction.Numerator) / float64(deref(rm.Fraction.Denominator, 100))
			}
			proxyParams["mirror"] = mirror
		default:
			t.problem("%s: filter %s is not supported; the rule answers 500", where, f.Type)
			fail = 500
		}
	}
	if len(reqHeaders)+len(respHeaders) > 0 {
		params := map[string]any{}
		if len(reqHeaders) > 0 {
			params["request"] = reqHeaders
		}
		if len(respHeaders) > 0 {
			params["response"] = respHeaders
		}
		mw, _ := json.Marshal(map[string]any{"module": "headers", "params": params})
		out.Middleware = append([]json.RawMessage{mw}, out.Middleware...)
	}
	switch {
	case fail != 0:
		out.Middleware = nil
		out.Handler = respond(fail)
	case len(b.splits) == 0:
		out.Handler = respond(b.status)
	default:
		if len(b.splits) == 1 {
			proxyParams["upstream"] = b.splits[0].upstream
		} else {
			var ups []map[string]any
			for _, s := range b.splits {
				ups = append(ups, map[string]any{"upstream": s.upstream, "weight": s.weight})
			}
			proxyParams["upstreams"] = ups
		}
		out.Handler, _ = json.Marshal(map[string]any{"module": "proxy", "params": proxyParams})
	}
	return out
}

func respond(status int) json.RawMessage {
	h, _ := json.Marshal(map[string]any{"module": "respond", "params": map[string]any{"status": status}})
	return h
}

// headerOps adds f's changes to ops, the params of one side of the
// headers middleware.
func headerOps(ops map[string]any, f *HTTPHeaderFilter) {
	if len(f.Set) > 0 {
		set, _ := ops["set"].(map[string]string)
		if set == nil {
			set = map[string]string{}
		}
		for _, h := range f.Set {
			set[h.Name] = h.Value
		}
		ops["set"] = set
	}
	if len(f.Add) > 0 {
		add, _ := ops["add"].(map[string][]string)
		if add == nil {
			add = map[string][]string{}
		}
		for _, h := range f.Add {
			add[h.Name] = append(add[h.Name], h.Value)
		}
		ops["add"] = add
	}
	if len(f.Remove) > 0 {
		del, _ := ops["delete"].([]string)
		ops["delete"] = append(del, f.Remove...)
	}
}

// rewrite returns the rewrite middleware for a URLRewrite's path p, on a
// match of pathType whose prefix, for PathPrefix, is prefix; nil for no
// change.
func rewrite(p *HTTPPathModifier, pathType, prefix string) (json.RawMessage, error) {
	if p == nil {
		return nil, nil
	}
	params := map[string]any{}
	switch p.Type {
	case "ReplaceFullPath":
		params["regexp"] = "^.*$"
		params["replacement"] = strings.ReplaceAll(deref(p.ReplaceFullPath, "/"), "$", "$$")
	case "ReplacePrefixMatch":
		if pathType != "PathPrefix" {
			return nil, fmt.Errorf("ReplacePrefixMatch needs a PathPrefix match")
		}
		if prefix != "" {
			params["strip_prefix"] = []string{prefix}
		}
		if add := strings.TrimRight(deref(p.ReplacePrefixMatch, "/"), "/"); add != "" {
			params["add_prefix"] = add
		}
		if len(params) == 0 {
			return nil, nil
		}
	default:
		return nil, fmt.Errorf("path type %q is not supported", p.Type)
	}
	mw, _ := json.Marshal(map[string]any{"module": "rewrite", "params": params})
	return mw, nil
}

// redirect returns the redirect middleware for a RequestRedirect, on a
// match of pathType whose prefix, for PathPrefix, is prefix.
func redirect(rd *HTTPRequestRedirect, pathType, prefix string) (json.RawMessage, error) {
	scheme := deref(rd.Scheme, "{scheme}")
	host := "{host}"
	switch {
	case rd.Hostname != nil && rd.Port != nil:
		host = net.JoinHostPort(*rd.Hostname, strconv.Itoa(*rd.Port))
	case rd.Hostname != nil:
		host = *rd.Hostname
	case rd.Port != nil:
		return nil, fmt.Errorf("a port without a hostname is not supported yet")
	}
	params := map[string]any{"status": deref(rd.StatusCode, 302)}
	path := "{uri}"
	if p := rd.Path; p != nil {
		switch p.Type {
		case "ReplaceFullPath":
			path = deref(p.ReplaceFullPath, "/")
		case "ReplacePrefixMatch":
			if pathType != "PathPrefix" {
				return nil, fmt.Errorf("ReplacePrefixMatch needs a PathPrefix match")
			}
			params["regexp"] = "^" + regexp.QuoteMeta(prefix) + "(/.*)?$"
			path = strings.TrimRight(deref(p.ReplacePrefixMatch, "/"), "/") + "$1"
		default:
			return nil, fmt.Errorf("path type %q is not supported", p.Type)
		}
	}
	params["to"] = scheme + "://" + host + path
	mw, _ := json.Marshal(map[string]any{"module": "redirect", "params": params})
	return mw, nil
}
