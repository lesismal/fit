package kubernetes

import (
	"cmp"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/lesismal/fit/config"
)

const gatewayGroup = "gateway.networking.k8s.io"

// objects are the objects of each kind the informers hold.
type objects struct {
	namespaces                      []*Namespace
	classes                         []*GatewayClass
	gateways                        []*Gateway
	routes                          []*HTTPRoute
	grpcRoutes                      []*GRPCRoute
	tlsRoutes, tcpRoutes, udpRoutes []*L4Route
	grants                          []*ReferenceGrant
	services                        []*Service
	slices                          []*EndpointSlice
	secrets                         []*Secret
	ingresses                       []*Ingress
	ingressClasses                  []*IngressClass
}

// snapshot is what the informers hold, at one moment.
type snapshot struct {
	objects
	namespace map[string]*Namespace       // by name
	service   map[string]*Service         // by namespace/name
	endpoints map[string][]*EndpointSlice // by namespace/service
	secret    map[string]*Secret          // by namespace/name
}

func newSnapshot(o objects) *snapshot {
	s := &snapshot{objects: o, namespace: map[string]*Namespace{}, service: map[string]*Service{},
		endpoints: map[string][]*EndpointSlice{}, secret: map[string]*Secret{}}
	for _, n := range o.namespaces {
		s.namespace[n.Metadata.Name] = n
	}
	for _, sv := range o.services {
		s.service[sv.Metadata.key()] = sv
	}
	for _, es := range o.slices {
		if svc := es.Metadata.Labels["kubernetes.io/service-name"]; svc != "" {
			k := es.Metadata.Namespace + "/" + svc
			s.endpoints[k] = append(s.endpoints[k], es)
		}
	}
	for _, sec := range o.secrets {
		if sec.Type == "kubernetes.io/tls" {
			s.secret[sec.Metadata.key()] = sec
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
		TLS       *outTLS  `json:"tls,omitempty"`
	}
	outTLS struct {
		Certificates []outCert `json:"certificates"`
	}
	outCert struct {
		CertFile string `json:"cert_file"`
		KeyFile  string `json:"key_file"`
	}
	outUpstream struct {
		Name      string   `json:"name"`
		Addresses []string `json:"addresses"`
		Weights   []int    `json:"weights,omitempty"`
		Protocol  string   `json:"protocol,omitempty"`
	}
	outService struct {
		Name        string   `json:"name"`
		Listeners   []string `json:"listeners"`
		Upstream    string   `json:"upstream"`
		ServerNames []string `json:"server_names,omitempty"`
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
		RemoteIP   []string            `json:"remote_ip,omitempty"`
	}
)

// The protocol families of a port: what the Gateway API lets listeners on
// one port share, and what one listener of fit's serves.
const (
	familyHTTP = "http" // HTTP
	familyTLS  = "tls"  // HTTPS and TLS passed through, told apart by server name
	familyTCP  = "tcp"
	familyUDP  = "udp"
)

// fitListener is a listener of fit's: the Gateway listeners on one port,
// of one family.
type fitListener struct {
	name, address string
	port          int
	family        string
	certs         []outCert // HTTPS listeners' certificates
	services      int       // the services that take it
	// What its connections go to: the route that takes those naming no
	// server, every one on tcp and udp, and the route that takes each
	// server name.
	fallback string
	names    map[string]string
}

// out returns the listener's element of the part, or nil when it would
// serve nothing: a listener of tcp or udp needs a service.
func (fl *fitListener) out() *outListener {
	o := &outListener{Name: fl.name, Address: fl.address}
	switch fl.family {
	case familyHTTP:
		o.Protocols = []string{"http/1.1", "h2c"}
	case familyTLS:
		if len(fl.certs) > 0 {
			o.Protocols, o.TLS = []string{"http/1.1", "h2"}, &outTLS{Certificates: fl.certs}
		}
		if fl.services > 0 {
			o.Protocols = append(o.Protocols, "tcp")
		}
	case familyTCP, familyUDP:
		if fl.services > 0 {
			o.Protocols = []string{fl.family}
		}
	}
	if len(o.Protocols) == 0 {
		return nil
	}
	return o
}

// translator turns a snapshot into a part.
type translator struct {
	opts     *config.KubernetesProvider
	provider string
	s        *snapshot
	// certDir is where certificate files are written; "" when there is
	// nowhere, and HTTPS listeners are not served.
	certDir string

	problems  []string
	listeners map[string]*fitListener // by name
	ports     map[int]string          // the family each TCP port serves
	upstreams map[string]*outUpstream
	routes    []*outRoute
	services  []*outService
	// certs are the certificate files the part names, by path: each a
	// Secret's chain and key.
	certs map[string][]byte

	// For the statuses: fit's Gateways, in order and by namespace/name, and
	// the routes in the namespaces watched, as each one is being and has
	// been translated.
	gws    []*gateway
	byKey  map[string]*gateway
	states []*routeState
	cur    *routeState
}

func newTranslator(provider string, opts *config.KubernetesProvider, s *snapshot, certDir string) *translator {
	return &translator{opts: opts, provider: provider, s: s, certDir: certDir, listeners: map[string]*fitListener{},
		ports: map[int]string{}, upstreams: map[string]*outUpstream{}, certs: map[string][]byte{}, byKey: map[string]*gateway{}}
}

// translate turns s into a part, and says why it left out what it did; see
// translator.run.
func translate(provider string, opts *config.KubernetesProvider, s *snapshot) (part []byte, problems []string) {
	t := newTranslator(provider, opts, s, opts.CertificatesDir)
	return t.run(), t.problems
}

// run makes the part: a listener for each port of fit's Gateways'
// listeners, routes for their HTTPRoutes' and GRPCRoutes' rules, services
// for their TLSRoutes, TCPRoutes and UDPRoutes, and upstreams for the
// Services those send to. The part is the same bytes for the same objects.
func (t *translator) run() []byte {
	t.gateways()
	for _, r := range byCreation(t.s.routes, func(r *HTTPRoute) *meta { return &r.Metadata }) {
		if t.watched(r.Metadata.Namespace) {
			t.httpRoute(r)
		}
	}
	for _, r := range byCreation(t.s.grpcRoutes, func(r *GRPCRoute) *meta { return &r.Metadata }) {
		if t.watched(r.Metadata.Namespace) {
			t.grpcRoute(r)
		}
	}
	for _, k := range []struct {
		kind   string
		routes []*L4Route
	}{{"TLSRoute", t.s.tlsRoutes}, {"TCPRoute", t.s.tcpRoutes}, {"UDPRoute", t.s.udpRoutes}} {
		for _, r := range byCreation(k.routes, func(r *L4Route) *meta { return &r.Metadata }) {
			if t.watched(r.Metadata.Namespace) {
				t.l4Route(k.kind, r)
			}
		}
	}
	t.ingresses()

	var out struct {
		Listeners []*outListener `json:"listeners,omitempty"`
		Upstreams []*outUpstream `json:"upstreams,omitempty"`
		Routes    []*outRoute    `json:"routes,omitempty"`
		Services  []*outService  `json:"services,omitempty"`
	}
	fls := slices.Collect(maps.Values(t.listeners))
	slices.SortFunc(fls, func(a, b *fitListener) int {
		return cmp.Or(cmp.Compare(a.port, b.port), strings.Compare(a.name, b.name))
	})
	for _, fl := range fls {
		if o := fl.out(); o != nil {
			out.Listeners = append(out.Listeners, o)
		}
	}
	for _, name := range sortedKeys(t.upstreams) {
		out.Upstreams = append(out.Upstreams, t.upstreams[name])
	}
	out.Routes, out.Services = t.routes, t.services
	part, _ := json.Marshal(out)
	return part
}

// byCreation returns objs oldest first, as the Gateway API orders routes
// and Gateways that conflict, then by namespace and name.
func byCreation[T any](objs []*T, metaOf func(*T) *meta) []*T {
	out := slices.Clone(objs)
	slices.SortFunc(out, func(a, b *T) int {
		ma, mb := metaOf(a), metaOf(b)
		return cmp.Or(strings.Compare(ma.CreationTimestamp, mb.CreationTimestamp),
			strings.Compare(ma.Namespace, mb.Namespace), strings.Compare(ma.Name, mb.Name))
	})
	return out
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

// badRef reports a backend of the route being translated that cannot be
// resolved, which its status says.
func (t *translator) badRef(where string, err error) {
	t.problem("%s: %v", where, err)
	var re *refError
	if t.cur != nil && t.cur.unresolved == "" && errors.As(err, &re) {
		t.cur.unresolved, t.cur.unresolvedMsg = re.reason, re.msg
	}
}

// leftOut reports a part of the route being translated that fit does not
// serve, which its status says.
func (t *translator) leftOut(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	t.problems = append(t.problems, msg)
	if t.cur != nil && t.cur.partial == "" {
		t.cur.partial = msg
	}
}

func (t *translator) watched(ns string) bool {
	return len(t.opts.Namespaces) == 0 || slices.Contains(t.opts.Namespaces, ns)
}

// gateway is one of fit's Gateways.
type gateway struct {
	g         *Gateway
	listeners []*gwListener
}

// gwListener is one of a Gateway's listeners, and its state.
type gwListener struct {
	l   Listener
	fit string // the name of fit's listener serving it; "" when none does
	// kinds are the kinds of routes it takes.
	kinds    []RouteGroupKind
	attached int // routes attached
	// Its conditions: why it is not accepted, its conflict, and why its
	// references are not resolved; "" for none.
	refused, refusedMsg       string
	conflict, conflictMsg     string
	unresolved, unresolvedMsg string
}

// gateways finds fit's Gateways: those of GatewayClasses whose
// controllerName is fit's, in the namespaces watched. It makes a listener
// of fit's for each port, and family, of their listeners.
func (t *translator) gateways() {
	classes := map[string]bool{}
	for _, c := range t.s.classes {
		if c.Spec.ControllerName == t.opts.Controller() {
			classes[c.Metadata.Name] = true
		}
	}
	for _, g := range byCreation(t.s.gateways, func(g *Gateway) *meta { return &g.Metadata }) {
		if !classes[g.Spec.GatewayClassName] || !t.watched(g.Metadata.Namespace) {
			continue
		}
		gw := &gateway{g: g}
		for _, l := range g.Spec.Listeners {
			gw.listeners = append(gw.listeners, t.listener(g, l))
		}
		t.gws = append(t.gws, gw)
		t.byKey[g.Metadata.key()] = gw
	}
}

// The route kinds of each protocol.
var protocolKinds = map[string][]string{
	"HTTP":  {"HTTPRoute", "GRPCRoute"},
	"HTTPS": {"HTTPRoute", "GRPCRoute"},
	"TLS":   {"TLSRoute"},
	"TCP":   {"TCPRoute"},
	"UDP":   {"UDPRoute"},
}

func ptr[T any](v T) *T { return &v }

// listener takes listener l of Gateway g: on fit's listener of its port and
// family, unless it is not valid or conflicts with one before it.
func (t *translator) listener(g *Gateway, l Listener) *gwListener {
	gl := &gwListener{l: l}
	where := fmt.Sprintf("gateway %s: listener %s", g.Metadata.key(), l.Name)
	refuse := func(reason, format string, args ...any) *gwListener {
		gl.refused, gl.refusedMsg = reason, fmt.Sprintf(format, args...)
		t.problem("%s: %s", where, gl.refusedMsg)
		return gl
	}
	kinds, ok := protocolKinds[l.Protocol]
	if !ok {
		return refuse("UnsupportedProtocol", "protocol %s is not supported", l.Protocol)
	}
	for _, k := range kinds {
		if a := l.AllowedRoutes; a == nil || len(a.Kinds) == 0 || slices.ContainsFunc(a.Kinds, func(rk RouteGroupKind) bool {
			return deref(rk.Group, gatewayGroup) == gatewayGroup && rk.Kind == k
		}) {
			gl.kinds = append(gl.kinds, RouteGroupKind{Group: ptr(gatewayGroup), Kind: k})
		}
	}
	if a := l.AllowedRoutes; a != nil {
		for _, rk := range a.Kinds {
			if deref(rk.Group, gatewayGroup) != gatewayGroup || !slices.Contains(kinds, rk.Kind) {
				gl.unresolved, gl.unresolvedMsg = "InvalidRouteKinds", fmt.Sprintf("a %s listener takes no %s", l.Protocol, rk.Kind)
				t.problem("%s: %s", where, gl.unresolvedMsg)
				break
			}
		}
	}
	if l.Port < 1 || l.Port > 65535 {
		return refuse("PortUnavailable", "%d is not a port", l.Port)
	}
	mode := ""
	if l.TLS != nil {
		mode = deref(l.TLS.Mode, "Terminate")
	}
	family := familyTLS
	switch l.Protocol {
	case "HTTP":
		family = familyHTTP
	case "HTTPS":
		if mode == "Passthrough" {
			return refuse("UnsupportedValue", "an HTTPS listener terminates TLS; a TLS listener passes it through")
		}
	case "TLS":
		if mode != "Passthrough" {
			return refuse("UnsupportedValue", "fit passes the connections of TLS listeners through (tls.mode Passthrough), and does not terminate them yet")
		}
	case "TCP":
		family = familyTCP
	case "UDP":
		family = familyUDP
	}
	var certs []outCert
	if l.Protocol == "HTTPS" {
		if certs = t.certificates(g, gl, where); len(certs) == 0 {
			return gl
		}
	}
	fl, other := t.portListener(l.Port, family)
	if fl == nil {
		gl.conflict, gl.conflictMsg = "ProtocolConflict", fmt.Sprintf("port %d serves %s already", l.Port, other)
		t.problem("%s: %s", where, gl.conflictMsg)
		return gl
	}
	fl.addCerts(certs)
	gl.fit = fl.name
	return gl
}

// portListener returns fit's listener of family on port, made if need be;
// or nil and the family the port serves already, when it is another.
func (t *translator) portListener(port int, family string) (*fitListener, string) {
	name := t.provider + "-" + strconv.Itoa(port)
	if family == familyUDP {
		name += "-udp"
	} else if f, ok := t.ports[port]; ok && f != family {
		return nil, f
	} else {
		t.ports[port] = family
	}
	fl := t.listeners[name]
	if fl == nil {
		fl = &fitListener{name: name, address: net.JoinHostPort(t.opts.ListenHost, strconv.Itoa(port)), port: port,
			family: family, names: map[string]string{}}
		t.listeners[name] = fl
	}
	return fl, ""
}

func (fl *fitListener) addCerts(certs []outCert) {
	for _, c := range certs {
		if !slices.Contains(fl.certs, c) {
			fl.certs = append(fl.certs, c)
		}
	}
}

// maxCertBytes bounds a certificate file, as fit's TLS does when it reads
// one (tlsmgr.MaxCertFileBytes).
const maxCertBytes = 1 << 20

// certificates returns the certificate files of HTTPS listener gl of
// Gateway g, from the Secrets it names, and gives the files to write. It
// says why it leaves out those it does.
func (t *translator) certificates(g *Gateway, gl *gwListener, where string) []outCert {
	bad := func(reason, format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		t.problem("%s: %s", where, msg)
		if gl.unresolved == "" {
			gl.unresolved, gl.unresolvedMsg = reason, msg
		}
	}
	l := gl.l
	if l.TLS == nil || len(l.TLS.CertificateRefs) == 0 {
		bad("InvalidCertificateRef", "an HTTPS listener needs a certificateRef")
		return nil
	}
	if t.certDir == "" {
		bad("InvalidCertificateRef", "there is no directory to write certificates to")
		return nil
	}
	var out []outCert
	for i, ref := range l.TLS.CertificateRefs {
		if deref(ref.Group, "") != "" || deref(ref.Kind, "Secret") != "Secret" {
			bad("InvalidCertificateRef", "certificateRef %d: %s/%s is not a Secret", i, deref(ref.Group, ""), deref(ref.Kind, "Secret"))
			continue
		}
		ns := deref(ref.Namespace, g.Metadata.Namespace)
		if ns != g.Metadata.Namespace && !t.granted("Gateway", g.Metadata.Namespace, ns, "Secret", ref.Name) {
			bad("RefNotPermitted", "certificateRef %d: secret %s/%s is in another namespace, and no ReferenceGrant there allows it", i, ns, ref.Name)
			continue
		}
		c, err := t.secretCert(ns, ref.Name)
		if err != nil {
			bad("InvalidCertificateRef", "certificateRef %d: %v", i, err)
			continue
		}
		out = append(out, c)
	}
	return out
}

// secretCert returns the certificate file of the Secret ns/name, of type
// kubernetes.io/tls, and gives the file to write. t.certDir must be set.
func (t *translator) secretCert(ns, name string) (outCert, error) {
	sec := t.s.secret[ns+"/"+name]
	if sec == nil {
		return outCert{}, fmt.Errorf("there is no secret %s/%s of type kubernetes.io/tls", ns, name)
	}
	crt, key := sec.Data["tls.crt"], sec.Data["tls.key"]
	if _, err := tls.X509KeyPair(crt, key); err != nil {
		return outCert{}, fmt.Errorf("secret %s/%s: %v", ns, name, err)
	}
	data := slices.Concat(crt, []byte("\n"), key)
	if len(data) > maxCertBytes {
		return outCert{}, fmt.Errorf("secret %s/%s is larger than %d bytes", ns, name, maxCertBytes)
	}
	sum := sha256.Sum256(data)
	path := filepath.Join(t.certDir, fmt.Sprintf("%s.%s.%x.pem", ns, name, sum[:6]))
	t.certs[path] = data
	return outCert{CertFile: path, KeyFile: path}, nil
}

// routeState is a route being translated, and what its status says.
type routeState struct {
	kind    string // HTTPRoute, GRPCRoute, TLSRoute, TCPRoute or UDPRoute
	m       *meta
	refs    []ParentRef
	status  *RouteStatus
	parents []*parentState // for each parentRef to one of fit's Gateways
	// Why a backend is not resolved, and the first part of the route fit
	// leaves out; "" for none.
	unresolved, unresolvedMsg string
	partial                   string
}

// parentState is whether one of fit's Gateways takes a route.
type parentState struct {
	ref         ParentRef
	accepted    bool
	reason, msg string // why not
}

// begin starts translating a route.
func (t *translator) begin(kind string, m *meta, refs []ParentRef, status *RouteStatus) {
	t.cur = &routeState{kind: kind, m: m, refs: refs, status: status}
	t.states = append(t.states, t.cur)
}

// attachment is where a route's rules are served: fit's listeners, for the
// host names hosts, or any host when hosts is nil.
type attachment struct {
	listeners []string
	hosts     []string
}

// attach attaches the route being translated, of the host names hostnames,
// to the listeners of fit's Gateways its parentRefs name and take it, and
// records whether each one does. It returns where the route is served, with
// listeners serving the same host names together, in a fixed order; none
// when it is not fit's or no listener takes it.
func (t *translator) attach(hostnames []string) []*attachment {
	st := t.cur
	key := st.m.key()
	l4 := st.kind != "HTTPRoute" && st.kind != "GRPCRoute"
	// The hosts each fit listener serves the route for; nil for any.
	hosts := map[string][]string{}
	anyHost := map[string]bool{}
	for _, ref := range st.refs {
		if deref(ref.Group, gatewayGroup) != gatewayGroup || deref(ref.Kind, "Gateway") != "Gateway" {
			continue
		}
		gw := t.byKey[deref(ref.Namespace, st.m.Namespace)+"/"+ref.Name]
		if gw == nil {
			continue // not fit's
		}
		ps := &parentState{ref: ref}
		st.parents = append(st.parents, ps)
		named, allowed, hostOK, taken := false, false, false, ""
		for _, gl := range gw.listeners {
			l := gl.l
			if ref.SectionName != nil && *ref.SectionName != l.Name || ref.Port != nil && *ref.Port != l.Port {
				continue
			}
			named = true
			if gl.fit == "" || !t.allowed(gl, gw.g, st) {
				continue
			}
			allowed = true
			var lh *string
			if l.Protocol != "TCP" && l.Protocol != "UDP" {
				lh = l.Hostname
			}
			hs, ok := intersect(lh, hostnames)
			if !ok {
				continue
			}
			hostOK = true
			if l4 {
				if other := t.claim(gl.fit, hs, st.kind+" "+key); other != "" {
					taken = other
					continue
				}
			}
			gl.attached++
			ps.accepted = true
			if hs == nil {
				anyHost[gl.fit] = true
			} else {
				hosts[gl.fit] = append(hosts[gl.fit], hs...)
			}
		}
		switch {
		case ps.accepted:
		case !named:
			ps.reason, ps.msg = "NoMatchingParent", "the Gateway has no listener of the section name or port given"
		case !allowed:
			ps.reason, ps.msg = "NotAllowedByListeners", "no listener of the Gateway takes it: their protocols, states, or allowed routes' namespaces and kinds leave it out"
		case !hostOK:
			ps.reason, ps.msg = "NoMatchingListenerHostname", "no listener's host name matches the route's"
		default:
			ps.reason, ps.msg = "NotAllowedByListeners", "the listener carries "+taken+" already"
		}
	}
	if len(st.parents) == 0 {
		return nil
	}
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
		t.problem("%s %s: no listener of its Gateways takes it: their sections, ports, allowed routes or host names leave it out", strings.ToLower(st.kind), key)
		return nil
	}
	var out []*attachment
	for _, k := range sortedKeys(groups) {
		out = append(out, groups[k])
	}
	return out
}

// claim gives fit's listener name's connections for the server names hosts,
// or those naming none when hosts is nil, to the route owner, and returns
// "", or the route that has them already.
func (t *translator) claim(name string, hosts []string, owner string) string {
	fl := t.listeners[name]
	if hosts == nil {
		if fl.fallback != "" && fl.fallback != owner {
			return fl.fallback
		}
		fl.fallback = owner
		return ""
	}
	for _, h := range hosts {
		if other := fl.names[strings.ToLower(h)]; other != "" && other != owner {
			return other
		}
	}
	for _, h := range hosts {
		fl.names[strings.ToLower(h)] = owner
	}
	return ""
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

// allowed reports whether listener gl of gateway g takes the route st: its
// kinds, and its allowedRoutes' namespaces.
func (t *translator) allowed(gl *gwListener, g *Gateway, st *routeState) bool {
	if !slices.ContainsFunc(gl.kinds, func(k RouteGroupKind) bool { return k.Kind == st.kind }) {
		return false
	}
	from := "Same"
	var sel *LabelSelector
	if a := gl.l.AllowedRoutes; a != nil && a.Namespaces != nil {
		from = cmp.Or(a.Namespaces.From, "Same")
		sel = a.Namespaces.Selector
	}
	switch from {
	case "All":
		return true
	case "Selector":
		ns := t.s.namespace[st.m.Namespace]
		if ns == nil || sel == nil {
			return false
		}
		return selects(sel, ns.Metadata.Labels)
	default:
		return st.m.Namespace == g.Metadata.Namespace
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

// httpRoute translates r: each match of each rule is a route of fit's on
// the listeners r is attached to.
func (t *translator) httpRoute(r *HTTPRoute) {
	t.begin("HTTPRoute", &r.Metadata, r.Spec.ParentRefs, &r.Status)
	groups := t.attach(r.Spec.Hostnames)
	if len(groups) == 0 {
		return
	}
	for i, rule := range r.Spec.Rules {
		where := fmt.Sprintf("httproute %s: rule %d", r.Metadata.key(), i)
		backend := t.backends(where, rule.BackendRefs, false)
		matches := rule.Matches
		if len(matches) == 0 {
			matches = []HTTPRouteMatch{{}}
		}
		for j, m := range matches {
			for gi, at := range groups {
				name := fmt.Sprintf("%s.%s.r%d.m%d", r.Metadata.Namespace, r.Metadata.Name, i, j)
				if len(groups) > 1 {
					name += fmt.Sprintf(".g%d", gi)
				}
				if out := t.match(fmt.Sprintf("%s: match %d", where, j), m, rule, backend, at); out != nil {
					out.Name = name
					t.routes = append(t.routes, out)
				}
			}
		}
	}
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

// backends resolves the backendRefs of the rule at where, of the route
// being translated; h2c makes their upstreams speak HTTP/2 in the clear,
// as gRPC's do.
func (t *translator) backends(where string, refs []HTTPBackendRef, h2c bool) ruleBackends {
	var b ruleBackends
	invalid := false
	for k, ref := range refs {
		w := deref(ref.Weight, 1)
		if w == 0 {
			continue
		}
		if len(ref.Filters) > 0 {
			t.leftOut("%s: backend %d: filters on a backend are not supported yet; they are left out", where, k)
		}
		name, ok, err := t.upstream(ref.BackendObjectRef, "TCP", h2c)
		switch {
		case err != nil:
			t.badRef(fmt.Sprintf("%s: backend %d", where, k), err)
			invalid = true
		case ok:
			b.splits = append(b.splits, split{name, min(w, 1000)})
		}
	}
	switch {
	case len(b.splits) > 0:
	case invalid || len(refs) == 0:
		b.status = 500
	default:
		b.status = 503
	}
	return b
}

// refError is a backend that cannot be resolved, with the reason a route's
// ResolvedRefs condition gives.
type refError struct{ reason, msg string }

func (e *refError) Error() string { return e.msg }

func badRef(reason, format string, args ...any) error {
	return &refError{reason: reason, msg: fmt.Sprintf(format, args...)}
}

// upstream makes the upstream of the Service port ref names, for the route
// being translated, and returns its name; ok is false when it has no
// endpoints ready. proto is the port's protocol, TCP or UDP; h2c makes the
// upstream speak HTTP/2 in the clear.
func (t *translator) upstream(ref BackendObjectRef, proto string, h2c bool) (name string, ok bool, err error) {
	from := t.cur.m.Namespace
	if deref(ref.Group, "") != "" || deref(ref.Kind, "Service") != "Service" {
		return "", false, badRef("InvalidKind", "%s/%s is not a Service", deref(ref.Group, ""), deref(ref.Kind, "Service"))
	}
	ns := deref(ref.Namespace, from)
	if ns != from && !t.granted(t.cur.kind, from, ns, "Service", ref.Name) {
		return "", false, badRef("RefNotPermitted", "service %s/%s is in another namespace, and no ReferenceGrant there allows it", ns, ref.Name)
	}
	if ref.Port == nil {
		return "", false, badRef("UnsupportedValue", "service %s/%s: a port is needed", ns, ref.Name)
	}
	svc := t.s.service[ns+"/"+ref.Name]
	if svc == nil {
		return "", false, badRef("BackendNotFound", "service %s/%s does not exist", ns, ref.Name)
	}
	var sp *ServicePort
	for i := range svc.Spec.Ports {
		if svc.Spec.Ports[i].Port == *ref.Port && cmp.Or(svc.Spec.Ports[i].Protocol, "TCP") == proto {
			sp = &svc.Spec.Ports[i]
		}
	}
	if sp == nil {
		return "", false, badRef("BackendNotFound", "service %s/%s has no %s port %d", ns, ref.Name, proto, *ref.Port)
	}
	name = fmt.Sprintf("%s.%s.%d", ns, ref.Name, sp.Port)
	appH2C := deref(sp.AppProtocol, "") == "kubernetes.io/h2c"
	switch {
	case proto == "UDP":
		name += ".udp"
	case h2c && !appH2C:
		name += ".h2c"
	}
	if _, done := t.upstreams[name]; done {
		return name, true, nil
	}
	addrs := t.addresses(svc, sp, proto)
	if len(addrs) == 0 {
		return name, false, nil
	}
	u := &outUpstream{Name: name, Addresses: addrs}
	if proto == "TCP" && (h2c || appH2C) {
		u.Protocol = "h2c"
	}
	t.upstreams[name] = u
	return name, true, nil
}

// addresses returns where svc's port sp, of protocol proto, is reached: its
// cluster IP, with backends cluster_ip, or else its ready endpoints; an
// ExternalName's name.
func (t *translator) addresses(svc *Service, sp *ServicePort, proto string) []string {
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
	for _, es := range t.s.endpoints[svc.Metadata.key()] {
		target := 0
		for _, p := range es.Ports {
			if deref(p.Name, "") == sp.Name && deref(p.Protocol, "TCP") == proto && p.Port != nil {
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

// granted reports whether a ReferenceGrant in namespace to lets objects of
// the Gateway API's kind fromKind in namespace from refer to the core
// object of kind toKind named name.
func (t *translator) granted(fromKind, from, to, toKind, name string) bool {
	for _, g := range t.s.grants {
		if g.Metadata.Namespace != to {
			continue
		}
		okFrom := slices.ContainsFunc(g.Spec.From, func(f struct {
			Group     string `json:"group"`
			Kind      string `json:"kind"`
			Namespace string `json:"namespace"`
		}) bool {
			return f.Group == gatewayGroup && f.Kind == fromKind && f.Namespace == from
		})
		okTo := slices.ContainsFunc(g.Spec.To, func(tt struct {
			Group string  `json:"group"`
			Kind  string  `json:"kind"`
			Name  *string `json:"name,omitempty"`
		}) bool {
			return tt.Group == "" && tt.Kind == toKind && (tt.Name == nil || *tt.Name == name)
		})
		if okFrom && okTo {
			return true
		}
	}
	return false
}

// match translates m, the match at where of rule, into a route of fit's on
// attachment at, or nil, saying why, when it cannot.
func (t *translator) match(where string, m HTTPRouteMatch, rule HTTPRouteRule, b ruleBackends, at *attachment) *outRoute {
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
			t.leftOut("%s: path %q does not start with '/'", where, pathValue)
			return nil
		}
		om.Path, pathClass = []string{pathValue}, 3
	case "PathPrefix":
		if !strings.HasPrefix(pathValue, "/") {
			t.leftOut("%s: path %q does not start with '/'", where, pathValue)
			return nil
		}
		prefix = strings.TrimRight(pathValue, "/")
		if prefix != "" {
			om.Path = []string{prefix, prefix + "/*"}
		}
		pathClass = 2
	case "RegularExpression":
		if _, err := regexp.Compile(pathValue); err != nil {
			t.leftOut("%s: path %q is not a regular expression fit takes: %v", where, pathValue, err)
			return nil
		}
		om.PathRegexp, pathClass = []string{"^(?:" + pathValue + ")$"}, 1
	default:
		t.leftOut("%s: path type %s is not supported", where, pathType)
		return nil
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
	for _, q := range m.QueryParams {
		if deref(q.Type, "Exact") != "Exact" {
			t.leftOut("%s: query parameter %s: match type %s is not supported yet", where, q.Name, deref(q.Type, ""))
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

	t.handle(where, out, rule.Filters, b, pathType, prefix)
	return out
}

// handle gives out the middleware of filters and a handler sending to b,
// for a match of pathType whose prefix, for PathPrefix, is prefix.
func (t *translator) handle(where string, out *outRoute, filters []HTTPRouteFilter, b ruleBackends, pathType, prefix string) {
	reqHeaders, respHeaders := map[string]any{}, map[string]any{}
	proxyParams := map[string]any{}
	fail := 0
	for _, f := range filters {
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
				t.leftOut("%s: URLRewrite: %v", where, err)
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
				t.leftOut("%s: RequestRedirect: %v", where, err)
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
				t.leftOut("%s: only one RequestMirror is supported; the others are left out", where)
				continue
			}
			name, ok, err := t.upstream(rm.BackendRef, "TCP", t.cur.kind == "GRPCRoute")
			if err != nil {
				t.badRef(where+": RequestMirror", fmt.Errorf("%w; no copies are sent", err))
				continue
			}
			if !ok {
				t.problem("%s: RequestMirror: it has no endpoints ready; no copies are sent", where)
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
			t.leftOut("%s: filter %s is not supported; the rule answers 500", where, f.Type)
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
