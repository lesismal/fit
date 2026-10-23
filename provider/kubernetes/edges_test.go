package kubernetes

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
)

// TestTranslateEdges translates what the API allows and fit leaves out or
// refuses: listeners, backends, matches and filters it cannot serve, a
// GRPCRoute and a TLSRoute on listeners of several host names, and
// backends of ExternalName Services.
func TestTranslateEdges(t *testing.T) {
	w := base(t)
	w.gateways = append(w.gateways, obj[Gateway](t, `{"metadata": {"namespace": "default", "name": "edge", "creationTimestamp": "2026-01-05T00:00:00Z"},
  "spec": {"gatewayClassName": "fit", "listeners": [
  {"name": "badport", "port": 0, "protocol": "HTTP"},
  {"name": "hpass", "port": 8443, "protocol": "HTTPS", "tls": {"mode": "Passthrough"}},
  {"name": "p1", "port": 9443, "protocol": "TLS", "hostname": "a.example.com", "tls": {"mode": "Passthrough"}},
  {"name": "p2", "port": 9444, "protocol": "TLS", "hostname": "b.example.com", "tls": {"mode": "Passthrough"}},
  {"name": "p3", "port": 9445, "protocol": "TLS", "hostname": "c.example.com", "tls": {"mode": "Passthrough"}}]}}`))
	w.services = append(w.services,
		obj[Service](t, `{"metadata": {"namespace": "default", "name": "ext"}, "spec": {"type": "ExternalName", "externalName": "db.example.net", "ports": [{"port": 5432}]}}`),
		obj[Service](t, `{"metadata": {"namespace": "default", "name": "extnone"}, "spec": {"type": "ExternalName", "ports": [{"port": 5432}]}}`),
		service(t, "default", "mis", 80, "a", ""))
	w.slices = append(w.slices, endpoints(t, "default", "mis", "b", 8000, map[string]bool{"10.0.9.1": true}))
	w.grants = []*ReferenceGrant{obj[ReferenceGrant](t, `{"metadata": {"namespace": "outside", "name": "g"}, "spec": {
  "from": [{"group": "gateway.networking.k8s.io", "kind": "HTTPRoute", "namespace": "default"}], "to": [{"group": "", "kind": "Service"}]}}`)}
	web := `"backendRefs": [{"name": "web", "port": 80}]`
	w.routes = []*HTTPRoute{
		obj[HTTPRoute](t, `{"metadata": {"namespace": "default", "name": "edge", "creationTimestamp": "2026-01-01T00:00:00Z"}, "spec": {
  "parentRefs": [{"name": "gw", "sectionName": "http"}, {"group": "other.example", "kind": "Gateway", "name": "gw"}],
  "rules": [
    {"backendRefs": [{"name": "web", "port": 80, "filters": [{"type": "RequestHeaderModifier", "requestHeaderModifier": {"set": [{"name": "a", "value": "b"}]}}]}]},
    {"backendRefs": [{"group": "x.example", "kind": "Thing", "name": "web"}]},
    {"backendRefs": [{"name": "web"}]},
    {"backendRefs": [{"name": "ext", "port": 5432}]},
    {"backendRefs": [{"name": "extnone", "port": 5432}]},
    {"backendRefs": [{"name": "mis", "port": 80}]},
    {"backendRefs": [{"name": "shop", "namespace": "team", "port": 80}]},
    {"matches": [{"path": {"type": "Exact", "value": "x"}}, {"path": {"type": "PathPrefix", "value": "x"}},
                 {"path": {"type": "RegularExpression", "value": "("}}, {"path": {"type": "Weird", "value": "/"}},
                 {"queryParams": [{"type": "RegularExpression", "name": "q", "value": "x"}]}], `+web+`},
    {"filters": [{"type": "URLRewrite"}, {"type": "RequestRedirect"}, {"type": "RequestMirror"}], `+web+`},
    {"filters": [{"type": "URLRewrite", "urlRewrite": {"path": {"type": "Bad"}}}], `+web+`},
    {"filters": [{"type": "RequestRedirect", "requestRedirect": {"port": 8080}}], `+web+`},
    {"filters": [{"type": "RequestMirror", "requestMirror": {"backendRef": {"name": "web"}}}], `+web+`},
    {"filters": [{"type": "RequestMirror", "requestMirror": {"backendRef": {"name": "empty", "port": 80}}}], `+web+`},
    {"filters": [{"type": "RequestMirror", "requestMirror": {"backendRef": {"name": "api", "port": 8080}, "percent": 50}}], `+web+`}]}}`),
		// In a namespace there is no Namespace of, on a listener that
		// selects namespaces by their labels.
		obj[HTTPRoute](t, `{"metadata": {"namespace": "ghost", "name": "far", "creationTimestamp": "2026-01-02T00:00:00Z"}, "spec": {
  "parentRefs": [{"namespace": "default", "name": "gw", "sectionName": "picky"}], "rules": [{`+web+`}]}}`),
	}
	w.grpcRoutes = []*GRPCRoute{obj[GRPCRoute](t, `{"metadata": {"namespace": "default", "name": "g", "creationTimestamp": "2026-01-01T00:00:00Z"}, "spec": {
  "parentRefs": [{"name": "gw", "sectionName": "http"}, {"name": "gw", "sectionName": "web"}],
  "rules": [{"matches": [{}, {"method": {"service": "a/b"}}, {"method": {"type": "Weird", "service": "x"}},
                         {"headers": [{"type": "RegularExpression", "name": "x", "value": "y"}]}],
             "backendRefs": [{"name": "api", "port": 8080}]}]}}`)}
	l4 := func(name, spec string) *L4Route {
		return obj[L4Route](t, `{"metadata": {"namespace": "default", "name": "`+name+`", "creationTimestamp": "2026-01-01T00:00:00Z"}, "spec": `+spec+`}`)
	}
	w.tlsRoutes = []*L4Route{
		l4("multi", `{"parentRefs": [{"name": "edge", "sectionName": "p1"}, {"name": "edge", "sectionName": "p2"}],
  "rules": [{"backendRefs": [{"name": "web", "port": 80, "weight": 0}, {"name": "api", "port": 8080}, {"name": "api", "port": 8080}]}]}`),
		l4("norules", `{"parentRefs": [{"name": "edge", "sectionName": "p3"}]}`),
	}

	tr, p := translated(t, w)
	hasProblems(t, tr.problems,
		"gateway default/edge: listener badport: 0 is not a port",
		"gateway default/edge: listener hpass: an HTTPS listener terminates TLS",
		"httproute default/edge: rule 0: backend 0: filters on a backend are not supported yet",
		"httproute default/edge: rule 1: backend 0: x.example/Thing is not a Service",
		"httproute default/edge: rule 2: backend 0: service default/web: a port is needed",
		"httproute default/edge: rule 6: backend 0: service team/shop is in another namespace",
		`httproute default/edge: rule 7: match 0: path "x" does not start with '/'`,
		`httproute default/edge: rule 7: match 1: path "x" does not start with '/'`,
		`httproute default/edge: rule 7: match 2: path "(" is not a regular expression fit takes`,
		"httproute default/edge: rule 7: match 3: path type Weird is not supported",
		"httproute default/edge: rule 7: match 4: query parameter q: match type RegularExpression is not supported yet",
		"httproute default/edge: rule 9: match 0: URLRewrite: path type \"Bad\" is not supported",
		"httproute default/edge: rule 10: match 0: RequestRedirect: a port without a hostname is not supported yet",
		"httproute default/edge: rule 11: match 0: RequestMirror: service default/web: a port is needed; no copies are sent",
		"httproute default/edge: rule 12: match 0: RequestMirror: it has no endpoints ready; no copies are sent",
		"httproute ghost/far: no listener of its Gateways takes it",
		"grpcroute default/g: rule 0: match 1: a service or method holds '/'",
		"grpcroute default/g: rule 0: match 2: method match type Weird is not supported",
		"grpcroute default/g: rule 0: match 3: header x: match type RegularExpression is not supported yet",
		"tlsroute default/norules: it has no rules, and so no backends",
	)
	upstreams := map[string]outUpstream{}
	for _, u := range p.Upstreams {
		upstreams[u.Name] = u
	}
	if u := upstreams["default.ext.5432"]; toJSON(u.Addresses) != `["db.example.net:5432"]` {
		t.Errorf("an ExternalName's upstream: %+v", u)
	}
	for _, name := range []string{"default.extnone.5432", "default.mis.80"} {
		if _, ok := upstreams[name]; ok {
			t.Errorf("upstream %s, which has no addresses", name)
		}
	}
	// One TLS service on each host name's listeners, to api's endpoint,
	// named twice, once.
	if u := upstreams["default.multi.tls"]; toJSON(u.Addresses) != `["10.0.1.1:9000"]` || toJSON(u.Weights) != `[1000]` {
		t.Errorf("multi's upstream: %+v", u)
	}
	var services []string
	for _, s := range p.Services {
		services = append(services, s.Name)
	}
	if strings.Join(services, " ") != "default.multi.tls.g0 default.multi.tls.g1" {
		t.Errorf("services: %v", services)
	}
	var grpc []string
	for _, r := range p.Routes {
		if strings.HasPrefix(r.Name, "default.g.") {
			grpc = append(grpc, r.Name)
		}
	}
	if strings.Join(grpc, " ") != "default.g.grpc.r0.m0.g0 default.g.grpc.r0.m0.g1" {
		t.Errorf("grpc routes: %v", grpc)
	}
	if r := p.Routes[p.route(t, "default.edge.r13.m0")]; toJSON(r.Handler.Params["mirror"]) != `{"percent":50,"upstream":"default.api.8080"}` {
		t.Errorf("a mirror by percent: %+v", r.Handler)
	}
	if r := p.Routes[p.route(t, "default.edge.r8.m0")]; r.Handler.Params["upstream"] != "default.web.80" || len(r.Middleware) != 0 {
		t.Errorf("filters with nothing in them: %+v", r)
	}
}

// TestSecretTooLarge checks that a Secret past maxCertBytes is refused.
func TestSecretTooLarge(t *testing.T) {
	crt, key := testCert(t, "a.example.com")
	big := append(append([]byte(nil), crt...), strings.Repeat("#", maxCertBytes)...)
	w := base(t)
	w.secrets = []*Secret{obj[Secret](t, secretDoc("default", "big", big, key))}
	w.gateways = []*Gateway{obj[Gateway](t, `{"metadata": {"namespace": "default", "name": "gw"}, "spec": {"gatewayClassName": "fit", "listeners": [
  {"name": "https", "port": 443, "protocol": "HTTPS", "tls": {"certificateRefs": [{"name": "big"}]}}]}}`)}
	tr, _ := translated(t, w)
	hasProblems(t, tr.problems, "gateway default/gw: listener https: certificateRef 0: secret default/big is larger than")
}

func TestIngressEdges(t *testing.T) {
	crt, key := testCert(t, "shop.example.com")
	w := ingressWorld(t, false)
	w.secrets = []*Secret{obj[Secret](t, secretDoc("default", "shop-tls", crt, key))}
	w.ingresses = []*Ingress{
		ingress(t, "a", "2026-01-01T00:00:00Z", `"fit.lesismal.github.io/ssl-redirect": "maybe", "fit.lesismal.github.io/rewrite-target": "x",
  "fit.lesismal.github.io/backend-protocol": "HTTP", "fit.lesismal.github.io/request-timeout": "soon",
  "fit.lesismal.github.io/allow-source-range": "10.0.0.0/8, ,", "other.example/ignored": "x"`, `{"ingressClassName": "fit",
  "tls": [{"secretName": ""}, {"secretName": "shop-tls"}],
  "rules": [{"host": "x.example.com"},
            {"http": {"paths": [{"path": "x", "pathType": "Prefix", "backend": {"service": {"name": "web", "port": {"number": 80}}}},
                                {"path": "/e", "pathType": "Exact", "backend": {"service": {"name": "web", "port": {"number": 80}}}}]}}]}`),
		ingress(t, "b", "2026-01-02T00:00:00Z", `"fit.lesismal.github.io/ssl-redirect": "true", "fit.lesismal.github.io/rewrite-target": "/v2",
  "fit.lesismal.github.io/backend-protocol": "SCTP"`, `{"ingressClassName": "fit",
  "tls": [{"secretName": "shop-tls"}],
  "rules": [{"http": {"paths": [{"path": "/x", "pathType": "Exact", "backend": {"service": {"name": "web", "port": {"number": 80}}}},
                                {"path": "/y", "pathType": "Prefix", "backend": {"service": {"name": "empty", "port": {"number": 80}}}}]}}]}`),
	}
	tr, p := ingressPart(t, w, &config.KubernetesIngress{})
	hasProblems(t, tr.problems,
		`ingress default/a: annotation fit.lesismal.github.io/ssl-redirect: "maybe" is not true or false`,
		`ingress default/a: annotation fit.lesismal.github.io/rewrite-target: "x" is not a path`,
		`ingress default/a: annotation fit.lesismal.github.io/request-timeout: "soon" is not a duration`,
		`ingress default/a: tls 0: no secretName`,
		`ingress default/a: rule 1: path 0: path "x" does not start with '/'`,
		`ingress default/b: annotation fit.lesismal.github.io/backend-protocol: "SCTP" is not HTTP, H2C or GRPC`,
	)
	// a: TLS for any host, and its source range, its empty entries left
	// out.
	if r := p.Routes[p.route(t, "default.a.ing.r1.p1")]; toJSON(r.Listeners) != `["k8s-80","k8s-443"]` || toJSON(r.Match.RemoteIP) != `["10.0.0.0/8"]` {
		t.Errorf("a: %+v %+v", r, r.Match)
	}
	// b: an exact path rewritten whole; any host redirected to HTTPS; a
	// backend with no endpoints ready answers 503.
	if r := p.Routes[p.route(t, "default.b.ing.r0.p0")]; toJSON(r.Middleware) != `[{"module":"rewrite","params":{"regexp":"^.*$","replacement":"/v2"}}]` {
		t.Errorf("b p0: %s", toJSON(r.Middleware))
	}
	if r := p.Routes[p.route(t, "default.b.ing.r0.p0.redirect")]; toJSON(r.Middleware) != `[{"module":"redirect","params":{"status":308,"to":"https://{host}{uri}"}}]` {
		t.Errorf("b p0's redirect: %s", toJSON(r.Middleware))
	}
	if r := p.Routes[p.route(t, "default.b.ing.r0.p1")]; toJSON(r.Handler) != `{"module":"respond","params":{"status":503}}` {
		t.Errorf("b p1: %s", toJSON(r.Handler))
	}

	// Nowhere to write certificates: TLS is not served.
	tr = newTranslator("k8s", &config.KubernetesProvider{Ingress: &config.KubernetesIngress{}}, w.snapshot(), "")
	tr.run()
	hasProblems(t, tr.problems, "ingress default/a: tls 1: there is no directory to write certificates to")

	// A Gateway's TCP listener holds the HTTP port.
	w.gateways = []*Gateway{obj[Gateway](t, `{"metadata": {"namespace": "default", "name": "l4"}, "spec": {"gatewayClassName": "fit", "listeners": [
  {"name": "tcp", "port": 80, "protocol": "TCP"}]}}`)}
	tr, _ = ingressPart(t, w, &config.KubernetesIngress{})
	hasProblems(t, tr.problems, "ingress default/a: port 80 serves tcp already: its rules are not served over HTTP")
}

func TestStatusEdges(t *testing.T) {
	w := statusWorld(t)
	var others []string
	for range maxParents {
		others = append(others, `{"parentRef": {"name": "elsewhere"}, "controllerName": "example.net/other", "conditions": []}`)
	}
	w.routes = []*HTTPRoute{
		// Its parents full with another controller's: none of fit's fits.
		obj[HTTPRoute](t, `{"metadata": {"namespace": "default", "name": "full"}, "spec": {"parentRefs": [{"name": "gw", "sectionName": "http"}],
  "rules": [{"backendRefs": [{"name": "web", "port": 80}]}]}, "status": {"parents": [`+strings.Join(others, ",")+`]}}`),
		// No longer on fit's Gateway, and on no other's: its parents are
		// emptied.
		obj[HTTPRoute](t, `{"metadata": {"namespace": "default", "name": "gone"}, "spec": {"parentRefs": [{"name": "theirs"}], "rules": [{}]},
  "status": {"parents": [{"parentRef": {"group": "gateway.networking.k8s.io", "kind": "Gateway", "name": "gw"}, "controllerName": "lesismal.github.io/fit", "conditions": []}]}}`),
	}
	tr := newTranslator("k8s", &config.KubernetesProvider{Addresses: []string{"192.0.2.1", "lb.example.com"}}, w.snapshot(), "")
	tr.run()
	out := map[string]map[string]any{}
	for _, u := range tr.statuses(time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)) {
		var p map[string]any
		if err := json.Unmarshal(u.patch, &p); err != nil {
			t.Fatal(err)
		}
		out[u.kind+" "+u.ns+"/"+u.name] = p
	}
	if _, ok := out["httproutes default/full"]; ok {
		t.Errorf("a route whose parents are full: %v", out["httproutes default/full"])
	}
	if got := toJSON(out["httproutes default/gone"]["status"]); got != `{"parents":[]}` {
		t.Errorf("gone: %s", got)
	}
	gs, _ := out["gateways default/gw"]["status"].(map[string]any)
	if got := toJSON(gs["addresses"]); got != `[{"type":"IPAddress","value":"192.0.2.1"},{"type":"Hostname","value":"lb.example.com"}]` {
		t.Errorf("addresses: %s", got)
	}
}
