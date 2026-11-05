package kubernetes

import (
	"strings"
	"testing"

	"github.com/lesismal/fit/config"

	_ "github.com/lesismal/fit/handler/proxy"
	_ "github.com/lesismal/fit/handler/respond"
	_ "github.com/lesismal/fit/middleware/headers"
	_ "github.com/lesismal/fit/middleware/limit"
	_ "github.com/lesismal/fit/middleware/rewrite"
)

// stateOf returns the state of the route kind ns/name the translator
// kept for its status.
func stateOf(t *testing.T, tr *translator, kind, key string) *routeState {
	t.Helper()
	for _, st := range tr.states {
		if st.kind == kind && st.m.key() == key {
			return st
		}
	}
	t.Fatalf("no state of %s %s", kind, key)
	return nil
}

// TestExternalName translates a tenant's ExternalName Service pointing at
// fit's own Admin API, on loopback, with an HTTPRoute for host localhost
// and an Ingress sending to it: by default no ExternalName Service is a
// backend, and the route's status says so; allowed, a local name still is
// not.
func TestExternalName(t *testing.T) {
	w := base(t)
	ext := func(name, host string) *Service {
		return obj[Service](t, `{"metadata": {"namespace": "default", "name": "`+name+`"}, "spec": {"type": "ExternalName", "externalName": "`+host+`", "ports": [{"port": 2019}]}}`)
	}
	w.services = append(w.services, ext("adm", "127.0.0.1"), ext("loc", "localhost"), ext("short", "127.1"),
		ext("meta", "169.254.169.254"), ext("dec", "2130706433"), ext("db", "db.example.net"))
	route := func(name, svc string) *HTTPRoute {
		return obj[HTTPRoute](t, `{"metadata": {"namespace": "default", "name": "`+name+`"}, "spec": {
  "parentRefs": [{"name": "gw", "sectionName": "http"}], "hostnames": ["localhost"],
  "rules": [{"filters": [{"type": "URLRewrite", "urlRewrite": {"hostname": "localhost"}}], "backendRefs": [{"name": "`+svc+`", "port": 2019}]}]}}`)
	}
	w.routes = []*HTTPRoute{route("adm", "adm"), route("loc", "loc"), route("short", "short"), route("meta", "meta"), route("dec", "dec"), route("db", "db")}
	w.ingressClasses = []*IngressClass{obj[IngressClass](t, `{"metadata": {"name": "fit"}, "spec": {"controller": "lesismal.github.io/fit"}}`)}
	w.ingresses = []*Ingress{ingress(t, "adm", "2026-01-01T00:00:00Z", ``, `{"ingressClassName": "fit",
  "rules": [{"host": "localhost", "http": {"paths": [{"path": "/", "pathType": "Prefix", "backend": {"service": {"name": "adm", "port": {"number": 2019}}}}]}}]}`)}

	for _, allow := range []bool{false, true} {
		opts := &config.KubernetesProvider{ExternalNameServices: allow, Ingress: &config.KubernetesIngress{HTTPPort: 8000}}
		tr := newTranslator("k8s", opts, w.snapshot(), "/certs")
		data := tr.run()
		checkPart(t, data)
		for _, local := range []string{"127.0.0.1:2019", "localhost:2019", "127.1:2019", "169.254.169.254:2019", "2130706433:2019"} {
			if strings.Contains(string(data), local) {
				t.Errorf("external_name_services %v: the part sends to %s:\n%s", allow, local, data)
			}
		}
		for _, name := range []string{"adm", "loc", "short", "meta", "dec"} {
			st := stateOf(t, tr, "HTTPRoute", "default/"+name)
			if st.unresolved != "UnsupportedValue" {
				t.Errorf("external_name_services %v: route %s: ResolvedRefs reason %q (%s)", allow, name, st.unresolved, st.unresolvedMsg)
			}
		}
		if got := strings.Contains(string(data), `"db.example.net:2019"`); got != allow {
			t.Errorf("external_name_services %v: db.example.net taken: %v", allow, got)
		}
		if allow {
			hasProblems(t, tr.problems,
				`httproute default/adm: rule 0: backend 0: service default/adm: externalName "127.0.0.1" is a local address`,
				`httproute default/loc: rule 0: backend 0: service default/loc: externalName "localhost" is a local address`,
				`ingress default/adm: rule 0: path 0: service default/adm: externalName "127.0.0.1" is a local address`)
		} else {
			hasProblems(t, tr.problems,
				"httproute default/db: rule 0: backend 0: service default/db is an ExternalName Service, which the provider's external_name_services must allow",
				"ingress default/adm: rule 0: path 0: service default/adm is an ExternalName Service")
		}
	}

	// An EndpointSlice written by hand at a loopback address, which the API
	// server refuses, is left out all the same.
	w = base(t)
	w.slices = append(w.slices, endpoints(t, "default", "api", "", 2019, map[string]bool{"127.0.0.1": true}))
	_, p := translated(t, w)
	for _, u := range p.Upstreams {
		if strings.Contains(toJSON(u.Addresses), "127.0.0.1") {
			t.Errorf("upstream %s sends to loopback: %v", u.Name, u.Addresses)
		}
	}
}

// TestTenantValues translates routes whose values the CRDs let through but
// fit's modules or config refuse, as a header value with a line break: the
// rule at fault answers 500, or is left out, and its status says so, while
// another tenant's route is still served and the part is still one fit
// takes, rather than fit refusing the provider's whole part.
func TestTenantValues(t *testing.T) {
	w := base(t)
	web := `"backendRefs": [{"name": "web", "port": 80}]`
	w.routes = []*HTTPRoute{
		obj[HTTPRoute](t, `{"metadata": {"namespace": "default", "name": "evil", "creationTimestamp": "2026-01-01T00:00:00Z"}, "spec": {
  "parentRefs": [{"name": "gw", "sectionName": "http"}],
  "rules": [
    {"filters": [{"type": "RequestHeaderModifier", "requestHeaderModifier": {"set": [{"name": "X-A", "value": "b\nX-Injected: 1"}]}}], `+web+`},
    {"filters": [{"type": "ResponseHeaderModifier", "responseHeaderModifier": {"add": [{"name": "Bad Name", "value": "b"}]}}], `+web+`},
    {"filters": [{"type": "URLRewrite", "urlRewrite": {"hostname": "a\r\nb"}}], `+web+`},
    {"filters": [{"type": "URLRewrite", "urlRewrite": {"path": {"type": "ReplacePrefixMatch", "replacePrefixMatch": "nope"}}}], `+web+`},
    {"filters": [{"type": "RequestRedirect", "requestRedirect": {"hostname": "a\nb"}}], `+web+`},
    {"filters": [{"type": "RequestRedirect", "requestRedirect": {"statusCode": 200}}], `+web+`},
    {"matches": [{"path": {"type": "Exact", "value": "/a*b"}}], `+web+`},
    {"matches": [{"headers": [{"name": "bad name", "value": "x"}]}], `+web+`},
    {"filters": [{"type": "RequestMirror", "requestMirror": {"backendRef": {"name": "api", "port": 8080}, "percent": 150}}], `+web+`}]}}`),
		obj[HTTPRoute](t, `{"metadata": {"namespace": "default", "name": "good", "creationTimestamp": "2026-01-02T00:00:00Z"}, "spec": {
  "parentRefs": [{"name": "gw", "sectionName": "http"}], "hostnames": ["good.example.com"], "rules": [{`+web+`}]}}`),
	}
	w.tlsRoutes = nil
	tr, p := translated(t, w)
	hasProblems(t, tr.problems,
		"httproute default/evil: rule 0: match 0: middleware 0: params.request: the value of X-A holds a line break or NUL; the rule answers 500",
		`httproute default/evil: rule 1: match 0: middleware 0: params.response: "Bad Name" is not a valid header name; the rule answers 500`,
		`httproute default/evil: rule 2: match 0: handler: params.host_header:`,
		`httproute default/evil: rule 3: match 0: middleware 0: params.add_prefix:`,
		"httproute default/evil: rule 4: match 0: middleware 0: params.to: the URL holds a line break or NUL; the rule answers 500",
		"httproute default/evil: rule 5: match 0: middleware 0: params.status: 200 is not a redirect status",
		`httproute default/evil: rule 6: match 0: match.path[0]: "/a*b" has a '*' other than at its end; it is left out`,
		`httproute default/evil: rule 7: match 0: match.header.bad name: "bad name" is not a valid header name; it is left out`,
		"httproute default/evil: rule 8: match 0: handler: params.mirror.percent: 150 is greater than the maximum 100; the rule answers 500",
	)
	for i := range 6 {
		r := p.Routes[p.route(t, "default.evil.r"+itoa(i)+".m0")]
		if r.Handler.Module != "respond" || r.Handler.Params["status"] != float64(500) || len(r.Middleware) != 0 {
			t.Errorf("rule %d: %+v", i, r)
		}
	}
	for _, name := range []string{"default.evil.r6.m0", "default.evil.r7.m0"} {
		for _, n := range p.names() {
			if n == name {
				t.Errorf("route %s, whose match fit refuses, is in the part", name)
			}
		}
	}
	if r := p.Routes[p.route(t, "default.good.r0.m0")]; r.Handler.Module != "proxy" {
		t.Errorf("good: %+v", r)
	}
	if st := stateOf(t, tr, "HTTPRoute", "default/evil"); !strings.Contains(st.partial, "the value of X-A holds a line break") {
		t.Errorf("evil's status: PartiallyInvalid %q", st.partial)
	}

	// An Ingress's source range with a zone, which a route's remote_ip
	// refuses, closes the Ingress, as any range that does not parse does.
	w = ingressWorld(t, false)
	w.ingresses = []*Ingress{ingress(t, "zone", "2026-01-01T00:00:00Z", `"fit.lesismal.github.io/allow-source-range": "fe80::1%eth0"`, `{"ingressClassName": "fit",
  "rules": [{"http": {"paths": [{"path": "/", "pathType": "Prefix", "backend": {"service": {"name": "web", "port": {"number": 80}}}}]}}]}`)}
	tr, p = ingressPart(t, w, &config.KubernetesIngress{})
	hasProblems(t, tr.problems, `ingress default/zone: annotation fit.lesismal.github.io/allow-source-range: "fe80::1%eth0" is not an address or CIDR range; every request is refused`)
	if r := p.Routes[p.route(t, "default.zone.ing.r0.p0")]; r.Handler.Module != "respond" || r.Handler.Params["status"] != float64(403) {
		t.Errorf("zone: %+v", r)
	}

	// A TLSRoute whose host name the config refuses as a server name is
	// left out.
	w = base(t)
	w.gateways = append(w.gateways, obj[Gateway](t, `{"metadata": {"namespace": "default", "name": "pass"}, "spec": {"gatewayClassName": "fit", "listeners": [
  {"name": "p", "port": 9443, "protocol": "TLS", "tls": {"mode": "Passthrough"}}]}}`))
	w.tlsRoutes = []*L4Route{obj[L4Route](t, `{"metadata": {"namespace": "default", "name": "bad"}, "spec": {"parentRefs": [{"name": "pass"}],
  "hostnames": ["bad host.example.com"], "rules": [{"backendRefs": [{"name": "api", "port": 8080}]}]}}`)}
	tr, p = translated(t, w)
	hasProblems(t, tr.problems, `tlsroute default/bad: server_names[0]: "bad host.example.com" is not a valid host name; it is left out`)
	if len(p.Services) != 0 {
		t.Errorf("services: %+v", p.Services)
	}
}
