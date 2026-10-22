package kubernetes

import (
	"encoding/json"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/lesismal/fit/config"
)

// obj decodes doc, an object as the API server writes it, into a *T.
func obj[T any](t *testing.T, doc string) *T {
	t.Helper()
	o := new(T)
	if err := json.Unmarshal([]byte(doc), o); err != nil {
		t.Fatalf("%v: %s", err, doc)
	}
	return o
}

// world is a cluster's objects, for translate.
type world = objects

func (w *world) snapshot() *snapshot { return newSnapshot(*w) }

func service(t *testing.T, ns, name string, port int, portName string, extra string) *Service {
	return obj[Service](t, `{"metadata": {"namespace": "`+ns+`", "name": "`+name+`"}, "spec": {"clusterIP": "10.96.0.`+
		itoa(port%250)+`", "ports": [{"name": "`+portName+`", "port": `+itoa(port)+extra+`}]}}`)
}

func endpoints(t *testing.T, ns, svc, portName string, port int, ready map[string]bool) *EndpointSlice {
	var eps []string
	for _, ip := range slices.Sorted(func(yield func(string) bool) {
		for ip := range ready {
			if !yield(ip) {
				return
			}
		}
	}) {
		eps = append(eps, `{"addresses": ["`+ip+`"], "conditions": {"ready": `+map[bool]string{true: "true", false: "false"}[ready[ip]]+`}}`)
	}
	return obj[EndpointSlice](t, `{"metadata": {"namespace": "`+ns+`", "name": "`+svc+`-abc", "labels": {"kubernetes.io/service-name": "`+svc+`"}},
  "addressType": "IPv4", "endpoints": [`+strings.Join(eps, ",")+`], "ports": [{"name": "`+portName+`", "protocol": "TCP", "port": `+itoa(port)+`}]}`)
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// base is a cluster with fit's GatewayClass and another's, a Gateway of
// each, and Services with endpoints.
func base(t *testing.T) *world {
	return &world{
		namespaces: []*Namespace{
			obj[Namespace](t, `{"metadata": {"name": "default"}}`),
			obj[Namespace](t, `{"metadata": {"name": "team", "labels": {"gateway": "yes"}}}`),
			obj[Namespace](t, `{"metadata": {"name": "outside"}}`),
		},
		classes: []*GatewayClass{
			obj[GatewayClass](t, `{"metadata": {"name": "fit"}, "spec": {"controllerName": "lesismal.github.io/fit"}}`),
			obj[GatewayClass](t, `{"metadata": {"name": "other"}, "spec": {"controllerName": "example.net/other"}}`),
		},
		gateways: []*Gateway{
			obj[Gateway](t, `{"metadata": {"namespace": "default", "name": "gw"}, "spec": {"gatewayClassName": "fit", "listeners": [
  {"name": "http", "port": 80, "protocol": "HTTP", "allowedRoutes": {"namespaces": {"from": "All"}}},
  {"name": "web", "port": 8080, "protocol": "HTTP", "hostname": "*.example.com"},
  {"name": "picky", "port": 8081, "protocol": "HTTP", "allowedRoutes": {"namespaces": {"from": "Selector", "selector": {"matchLabels": {"gateway": "yes"}}}}},
  {"name": "tls", "port": 443, "protocol": "HTTPS"}]}}`),
			obj[Gateway](t, `{"metadata": {"namespace": "default", "name": "theirs"}, "spec": {"gatewayClassName": "other", "listeners": [
  {"name": "http", "port": 9999, "protocol": "HTTP"}]}}`),
		},
		services: []*Service{
			service(t, "default", "web", 80, "http", `, "appProtocol": "kubernetes.io/h2c"`),
			service(t, "default", "api", 8080, "", ""),
			service(t, "default", "empty", 80, "", ""),
			service(t, "team", "shop", 80, "http", ""),
		},
		slices: []*EndpointSlice{
			endpoints(t, "default", "web", "http", 8000, map[string]bool{"10.0.0.2": true, "10.0.0.1": true, "10.0.0.9": false}),
			endpoints(t, "default", "api", "", 9000, map[string]bool{"10.0.1.1": true}),
			endpoints(t, "default", "empty", "", 80, map[string]bool{"10.0.2.1": false}),
			endpoints(t, "team", "shop", "http", 3000, map[string]bool{"10.0.3.1": true}),
		},
	}
}

// part is a translated part, decoded.
type part struct {
	Listeners []outListener `json:"listeners"`
	Upstreams []outUpstream `json:"upstreams"`
	Services  []outService  `json:"services"`
	Routes    []struct {
		Name       string            `json:"name"`
		Listeners  []string          `json:"listeners"`
		Priority   int               `json:"priority"`
		Match      *outMatch         `json:"match"`
		Middleware []json.RawMessage `json:"middleware"`
		Handler    struct {
			Module string         `json:"module"`
			Params map[string]any `json:"params"`
		} `json:"handler"`
	} `json:"routes"`
}

func run(t *testing.T, w *world, opts *config.KubernetesProvider) (part, []byte, []string) {
	t.Helper()
	data, problems := translate("k8s", opts, w.snapshot())
	var p part
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatalf("%v: %s", err, data)
	}
	checkPart(t, data)
	return p, data, problems
}

// checkPart fails t unless fit takes the part as a config of its own; an
// empty part, which adds nothing, is not one.
func checkPart(t *testing.T, data []byte) {
	t.Helper()
	if string(data) == "{}" {
		return
	}
	if _, err := config.Load(data); err != nil {
		t.Errorf("the part is not a valid config: %v\n%s", err, data)
	}
}

func (p part) route(t *testing.T, name string) int {
	t.Helper()
	for i, r := range p.Routes {
		if r.Name == name {
			return i
		}
	}
	t.Fatalf("no route %s in %v", name, p.names())
	return -1
}

func (p part) names() []string {
	var out []string
	for _, r := range p.Routes {
		out = append(out, r.Name)
	}
	return out
}

func toJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestTranslateGateway(t *testing.T) {
	w := base(t)
	w.routes = []*HTTPRoute{
		// Attached to every listener that takes it: http (All) and web
		// (Same, with its host name).
		obj[HTTPRoute](t, `{"metadata": {"namespace": "default", "name": "main", "creationTimestamp": "2026-01-01T00:00:00Z"}, "spec": {
  "parentRefs": [{"name": "gw"}],
  "rules": [{
    "matches": [{"path": {"type": "PathPrefix", "value": "/app/"}, "headers": [{"name": "X-Env", "value": "prod"}], "method": "GET"},
                {"path": {"type": "Exact", "value": "/health"}, "queryParams": [{"name": "full", "value": "1"}]}],
    "filters": [{"type": "RequestHeaderModifier", "requestHeaderModifier": {"set": [{"name": "X-A", "value": "1"}], "add": [{"name": "X-B", "value": "2"}], "remove": ["X-C"]}},
                {"type": "ResponseHeaderModifier", "responseHeaderModifier": {"add": [{"name": "X-D", "value": "3"}]}}],
    "backendRefs": [{"name": "web", "port": 80, "weight": 3}, {"name": "api", "port": 8080}, {"name": "gone", "port": 1, "weight": 0}]
  }]}}`),
		// From another namespace, by section, with its own host names.
		obj[HTTPRoute](t, `{"metadata": {"namespace": "team", "name": "shop", "creationTimestamp": "2026-01-02T00:00:00Z"}, "spec": {
  "parentRefs": [{"namespace": "default", "name": "gw", "sectionName": "http"}, {"namespace": "default", "name": "gw", "sectionName": "picky"}],
  "hostnames": ["shop.example.com"],
  "rules": [{"matches": [{"path": {"value": "/cart"}}],
             "filters": [{"type": "URLRewrite", "urlRewrite": {"hostname": "shop.internal", "path": {"type": "ReplacePrefixMatch", "replacePrefixMatch": "/v2/cart"}}}],
             "backendRefs": [{"name": "shop", "port": 80}]}]}}`),
		// Refused by web, which takes its own namespace's only, and by
		// picky, whose selector leaves out "outside".
		obj[HTTPRoute](t, `{"metadata": {"namespace": "outside", "name": "denied"}, "spec": {
  "parentRefs": [{"namespace": "default", "name": "gw", "sectionName": "web"}, {"namespace": "default", "name": "gw", "sectionName": "picky"}],
  "rules": [{"backendRefs": [{"name": "x", "port": 80}]}]}}`),
		// Another controller's.
		obj[HTTPRoute](t, `{"metadata": {"namespace": "default", "name": "theirs"}, "spec": {"parentRefs": [{"name": "theirs"}], "rules": [{}]}}`),
		// Backends that cannot be served: another namespace's without a
		// grant, no endpoints ready, and none at all; a redirect.
		obj[HTTPRoute](t, `{"metadata": {"namespace": "default", "name": "broken", "creationTimestamp": "2026-01-03T00:00:00Z"}, "spec": {
  "parentRefs": [{"name": "gw", "port": 80}],
  "rules": [{"matches": [{"path": {"value": "/cross"}}], "backendRefs": [{"name": "shop", "namespace": "team", "port": 80}]},
            {"matches": [{"path": {"value": "/empty"}}], "backendRefs": [{"name": "empty", "port": 80}]},
            {"matches": [{"path": {"value": "/none"}}]},
            {"matches": [{"path": {"value": "/old"}}], "filters": [{"type": "RequestRedirect", "requestRedirect": {"scheme": "https", "hostname": "new.example", "statusCode": 301}}]},
            {"matches": [{"path": {"value": "/re"}, "headers": [{"type": "RegularExpression", "name": "X", "value": ".*"}]}], "backendRefs": [{"name": "web", "port": 80}]},
            {"matches": [{"path": {"value": "/ext"}}], "filters": [{"type": "ExtensionRef"}], "backendRefs": [{"name": "web", "port": 80}]}]}}`),
	}
	p, data, problems := run(t, w, &config.KubernetesProvider{})

	if got := toJSON(p.Listeners); got != `[{"name":"k8s-80","address":":80","protocols":["http/1.1","h2c"]},{"name":"k8s-8080","address":":8080","protocols":["http/1.1","h2c"]},{"name":"k8s-8081","address":":8081","protocols":["http/1.1","h2c"]}]` {
		t.Errorf("listeners: %s", got)
	}
	if got := toJSON(p.Upstreams); got != `[{"name":"default.api.8080","addresses":["10.0.1.1:9000"]},{"name":"default.web.80","addresses":["10.0.0.1:8000","10.0.0.2:8000"],"protocol":"h2c"},{"name":"team.shop.80","addresses":["10.0.3.1:3000"]}]` {
		t.Errorf("upstreams: %s", got)
	}
	wantNames := []string{
		"default.main.r0.m0.g0", "default.main.r0.m0.g1", "default.main.r0.m1.g0", "default.main.r0.m1.g1",
		"team.shop.r0.m0",
		"default.broken.r0.m0", "default.broken.r1.m0", "default.broken.r2.m0", "default.broken.r3.m0", "default.broken.r5.m0",
	}
	if got := p.names(); !reflect.DeepEqual(got, wantNames) {
		t.Fatalf("routes: %v\n%s", got, data)
	}

	// main: on http for any host, on web for *.example.com.
	r := p.Routes[p.route(t, "default.main.r0.m0.g0")]
	if toJSON(r.Listeners) != `["k8s-80"]` || r.Match.Host != nil ||
		toJSON(r.Match.Path) != `["/app","/app/*"]` || toJSON(r.Match.Header) != `{"X-Env":["prod"]}` || toJSON(r.Match.Method) != `["GET"]` {
		t.Errorf("main, m0, g0: %+v %+v", r, r.Match)
	}
	if r.Handler.Module != "proxy" || toJSON(r.Handler.Params["upstreams"]) != `[{"upstream":"default.web.80","weight":3},{"upstream":"default.api.8080","weight":1}]` {
		t.Errorf("main's handler: %+v", r.Handler)
	}
	if len(r.Middleware) != 1 || string(r.Middleware[0]) != `{"module":"headers","params":{"request":{"add":{"X-B":["2"]},"delete":["X-C"],"set":{"X-A":"1"}},"response":{"add":{"X-D":["3"]}}}}` {
		t.Errorf("main's middleware: %s", r.Middleware)
	}
	r1 := p.Routes[p.route(t, "default.main.r0.m0.g1")]
	if toJSON(r1.Listeners) != `["k8s-8080"]` || toJSON(r1.Match.Host) != `["*.example.com"]` || r1.Priority <= r.Priority {
		t.Errorf("main, m0, g1: %+v %+v", r1, r1.Match)
	}
	m1 := p.Routes[p.route(t, "default.main.r0.m1.g0")]
	if toJSON(m1.Match.Path) != `["/health"]` || toJSON(m1.Match.Query) != `{"full":["1"]}` || m1.Priority <= r.Priority {
		t.Errorf("main, m1: exact beats prefix: %+v %+v (%d against %d)", m1, m1.Match, m1.Priority, r.Priority)
	}

	// shop: on http and picky, rewritten.
	s := p.Routes[p.route(t, "team.shop.r0.m0")]
	if toJSON(s.Listeners) != `["k8s-80","k8s-8081"]` || toJSON(s.Match.Host) != `["shop.example.com"]` ||
		toJSON(s.Match.Path) != `["/cart","/cart/*"]` {
		t.Errorf("shop: %+v %+v", s, s.Match)
	}
	if len(s.Middleware) != 1 || string(s.Middleware[0]) != `{"module":"rewrite","params":{"add_prefix":"/v2/cart","strip_prefix":["/cart"]}}` ||
		s.Handler.Params["upstream"] != "team.shop.80" || s.Handler.Params["host_header"] != "shop.internal" {
		t.Errorf("shop: %s %+v", s.Middleware, s.Handler)
	}

	// broken: 500, 503, 500, a redirect, and 500 for an unknown filter.
	for name, want := range map[string]string{
		"default.broken.r0.m0": `{"module":"respond","params":{"status":500}}`,
		"default.broken.r1.m0": `{"module":"respond","params":{"status":503}}`,
		"default.broken.r2.m0": `{"module":"respond","params":{"status":500}}`,
		"default.broken.r5.m0": `{"module":"respond","params":{"status":500}}`,
	} {
		if got := toJSON(p.Routes[p.route(t, name)].Handler); got != want {
			t.Errorf("%s: %s, want %s", name, got, want)
		}
	}
	rd := p.Routes[p.route(t, "default.broken.r3.m0")]
	if len(rd.Middleware) != 1 || string(rd.Middleware[0]) != `{"module":"redirect","params":{"status":301,"to":"https://new.example{uri}"}}` {
		t.Errorf("redirect: %s", rd.Middleware)
	}

	wantProblems := []string{
		"gateway default/gw: listener tls: an HTTPS listener needs a certificateRef",
		"httproute outside/denied: no listener of its Gateways takes it: their sections, ports, allowed routes or host names leave it out",
		"httproute default/broken: rule 0: backend 0: service team/shop is in another namespace, and no ReferenceGrant there allows it",
		"httproute default/broken: rule 4: match 0: header X: match type RegularExpression is not supported yet",
		"httproute default/broken: rule 5: match 0: filter ExtensionRef is not supported; the rule answers 500",
	}
	if !reflect.DeepEqual(problems, wantProblems) {
		t.Errorf("problems:\n%s", strings.Join(problems, "\n"))
	}

	// The same objects in another order give the same part.
	for range 5 {
		shuffled := *w
		for _, s := range []any{shuffled.routes, shuffled.gateways, shuffled.services, shuffled.slices, shuffled.classes} {
			v := reflect.ValueOf(s)
			rand.Shuffle(v.Len(), reflect.Swapper(s))
		}
		if again, _ := translate("k8s", &config.KubernetesProvider{}, shuffled.snapshot()); string(again) != string(data) {
			t.Fatalf("another order gave another part:\n%s\n%s", again, data)
		}
	}

	// A ReferenceGrant lets the cross-namespace backend through.
	w.grants = []*ReferenceGrant{obj[ReferenceGrant](t, `{"metadata": {"namespace": "team", "name": "g"}, "spec": {
  "from": [{"group": "gateway.networking.k8s.io", "kind": "HTTPRoute", "namespace": "default"}],
  "to": [{"group": "", "kind": "Service", "name": "shop"}]}}`)}
	p, _, _ = run(t, w, &config.KubernetesProvider{})
	if h := p.Routes[p.route(t, "default.broken.r0.m0")].Handler; h.Module != "proxy" || h.Params["upstream"] != "team.shop.80" {
		t.Errorf("with a grant: %+v", h)
	}
}

func TestTranslateOptions(t *testing.T) {
	w := base(t)
	w.routes = []*HTTPRoute{
		obj[HTTPRoute](t, `{"metadata": {"namespace": "default", "name": "a"}, "spec": {"parentRefs": [{"name": "gw", "sectionName": "http"}],
  "rules": [{"backendRefs": [{"name": "web", "port": 80}]}]}}`),
		obj[HTTPRoute](t, `{"metadata": {"namespace": "team", "name": "b"}, "spec": {"parentRefs": [{"namespace": "default", "name": "gw", "sectionName": "http"}],
  "rules": [{"backendRefs": [{"name": "shop", "port": 80}]}]}}`),
	}
	// cluster_ip sends to the Service's address, listen_host binds there.
	p, _, _ := run(t, w, &config.KubernetesProvider{Backends: "cluster_ip", ListenHost: "127.0.0.1"})
	if got := toJSON(p.Upstreams); got != `[{"name":"default.web.80","addresses":["10.96.0.80:80"],"protocol":"h2c"},{"name":"team.shop.80","addresses":["10.96.0.80:80"]}]` {
		t.Errorf("cluster_ip: %s", got)
	}
	if p.Listeners[0].Address != "127.0.0.1:80" {
		t.Errorf("listen_host: %+v", p.Listeners)
	}
	// namespaces leaves out the others' routes.
	p, _, _ = run(t, w, &config.KubernetesProvider{Namespaces: []string{"default"}})
	if got := p.names(); !reflect.DeepEqual(got, []string{"default.a.r0.m0"}) {
		t.Errorf("namespaces: %v", got)
	}
	// Another controller name serves the other class's Gateway.
	w.routes = append(w.routes, obj[HTTPRoute](t, `{"metadata": {"namespace": "default", "name": "theirs"}, "spec": {"parentRefs": [{"name": "theirs"}], "rules": [{}]}}`))
	p, _, _ = run(t, w, &config.KubernetesProvider{ControllerName: "example.net/other"})
	if got := p.names(); !reflect.DeepEqual(got, []string{"default.theirs.r0.m0"}) || p.Listeners[0].Name != "k8s-9999" {
		t.Errorf("controller_name: %v %+v", got, p.Listeners)
	}
	// Nothing of fit's is an empty part.
	if data, _ := translate("k8s", &config.KubernetesProvider{}, (&world{}).snapshot()); string(data) != "{}" {
		t.Errorf("nothing: %s", data)
	}
}

func TestPrecedence(t *testing.T) {
	w := base(t)
	w.routes = []*HTTPRoute{obj[HTTPRoute](t, `{"metadata": {"namespace": "default", "name": "p"}, "spec": {"parentRefs": [{"name": "gw", "sectionName": "http"}],
  "hostnames": ["a.example.com"],
  "rules": [{"matches": [
    {"path": {"type": "Exact", "value": "/a"}},
    {"path": {"type": "PathPrefix", "value": "/abcdef"}},
    {"path": {"type": "PathPrefix", "value": "/abc"}, "method": "GET"},
    {"path": {"type": "PathPrefix", "value": "/abc"}, "headers": [{"name": "A", "value": "1"}, {"name": "B", "value": "1"}]},
    {"path": {"type": "PathPrefix", "value": "/abc"}, "headers": [{"name": "A", "value": "1"}], "queryParams": [{"name": "q", "value": "1"}, {"name": "r", "value": "1"}]},
    {"path": {"type": "PathPrefix", "value": "/abc"}, "queryParams": [{"name": "q", "value": "1"}]},
    {"path": {"type": "RegularExpression", "value": "/a.*"}}
  ], "backendRefs": [{"name": "web", "port": 80}]}]}}`)}
	p, _, _ := run(t, w, &config.KubernetesProvider{})
	for i := 1; i < len(p.Routes); i++ {
		if p.Routes[i-1].Priority <= p.Routes[i].Priority {
			t.Errorf("match %d (%d) does not come before match %d (%d)", i-1, p.Routes[i-1].Priority, i, p.Routes[i].Priority)
		}
	}
	// A route for a host name comes before one for a wildcard, which
	// comes before one for any host, whatever their paths.
	w.routes = []*HTTPRoute{
		obj[HTTPRoute](t, `{"metadata": {"namespace": "default", "name": "exact"}, "spec": {"parentRefs": [{"name": "gw", "sectionName": "http"}], "hostnames": ["a.example.com"], "rules": [{"backendRefs": [{"name": "web", "port": 80}]}]}}`),
		obj[HTTPRoute](t, `{"metadata": {"namespace": "default", "name": "wild"}, "spec": {"parentRefs": [{"name": "gw", "sectionName": "http"}], "hostnames": ["*.example.com"], "rules": [{"matches": [{"path": {"type": "Exact", "value": "/x"}}], "backendRefs": [{"name": "web", "port": 80}]}]}}`),
		obj[HTTPRoute](t, `{"metadata": {"namespace": "default", "name": "any"}, "spec": {"parentRefs": [{"name": "gw", "sectionName": "http"}], "rules": [{"matches": [{"path": {"type": "Exact", "value": "/xyzzy"}}], "backendRefs": [{"name": "web", "port": 80}]}]}}`),
	}
	p, _, _ = run(t, w, &config.KubernetesProvider{})
	pri := map[string]int{}
	for _, r := range p.Routes {
		pri[r.Name] = r.Priority
	}
	if !(pri["default.exact.r0.m0"] > pri["default.wild.r0.m0"] && pri["default.wild.r0.m0"] > pri["default.any.r0.m0"]) {
		t.Errorf("priorities: %v", pri)
	}
}

func TestIntersect(t *testing.T) {
	s := func(v string) *string { return &v }
	for _, tc := range []struct {
		listener *string
		route    []string
		want     []string
		ok       bool
	}{
		{nil, nil, nil, true},
		{nil, []string{"a.com"}, []string{"a.com"}, true},
		{s("a.com"), nil, []string{"a.com"}, true},
		{s("*.a.com"), []string{"x.a.com", "b.com", "y.z.a.com"}, []string{"x.a.com", "y.z.a.com"}, true},
		{s("x.a.com"), []string{"*.a.com"}, []string{"x.a.com"}, true},
		{s("a.com"), []string{"*.a.com"}, nil, false},
		{s("*.a.com"), []string{"*.a.com"}, []string{"*.a.com"}, true},
		{s("a.com"), []string{"b.com"}, nil, false},
	} {
		got, ok := intersect(tc.listener, tc.route)
		if !reflect.DeepEqual(got, tc.want) || ok != tc.ok {
			t.Errorf("intersect(%v, %v) = %v, %v; want %v, %v", deref(tc.listener, "<nil>"), tc.route, got, ok, tc.want, tc.ok)
		}
	}
}

func TestSelects(t *testing.T) {
	sel := obj[LabelSelector](t, `{"matchLabels": {"a": "1"}, "matchExpressions": [
  {"key": "b", "operator": "In", "values": ["x", "y"]}, {"key": "c", "operator": "NotIn", "values": ["z"]},
  {"key": "d", "operator": "Exists"}, {"key": "e", "operator": "DoesNotExist"}]}`)
	for _, tc := range []struct {
		labels map[string]string
		want   bool
	}{
		{map[string]string{"a": "1", "b": "x", "d": ""}, true},
		{map[string]string{"a": "1", "b": "y", "c": "w", "d": "1"}, true},
		{map[string]string{"a": "2", "b": "x", "d": ""}, false},
		{map[string]string{"a": "1", "b": "q", "d": ""}, false},
		{map[string]string{"a": "1", "b": "x", "c": "z", "d": ""}, false},
		{map[string]string{"a": "1", "b": "x"}, false},
		{map[string]string{"a": "1", "b": "x", "d": "", "e": ""}, false},
	} {
		if got := selects(sel, tc.labels); got != tc.want {
			t.Errorf("selects(%v) = %v", tc.labels, got)
		}
	}
	bad := obj[LabelSelector](t, `{"matchExpressions": [{"key": "a", "operator": "Gt"}]}`)
	if selects(bad, map[string]string{"a": "1"}) {
		t.Error("an unknown operator selected")
	}
}

func TestFilters(t *testing.T) {
	s := func(v string) *string { return &v }
	n := func(v int) *int { return &v }
	for _, tc := range []struct {
		p        *HTTPPathModifier
		pathType string
		prefix   string
		want     string
	}{
		{&HTTPPathModifier{Type: "ReplaceFullPath", ReplaceFullPath: s("/new$1")}, "Exact", "", `{"module":"rewrite","params":{"regexp":"^.*$","replacement":"/new$$1"}}`},
		{&HTTPPathModifier{Type: "ReplacePrefixMatch", ReplacePrefixMatch: s("/")}, "PathPrefix", "/api", `{"module":"rewrite","params":{"strip_prefix":["/api"]}}`},
		{&HTTPPathModifier{Type: "ReplacePrefixMatch", ReplacePrefixMatch: s("/v2/")}, "PathPrefix", "", `{"module":"rewrite","params":{"add_prefix":"/v2"}}`},
		{&HTTPPathModifier{Type: "ReplacePrefixMatch", ReplacePrefixMatch: s("/")}, "PathPrefix", "", ``},
		{nil, "PathPrefix", "", ``},
	} {
		got, err := rewrite(tc.p, tc.pathType, tc.prefix)
		if err != nil || string(got) != tc.want {
			t.Errorf("rewrite(%+v): %s %v, want %s", tc.p, got, err, tc.want)
		}
	}
	if _, err := rewrite(&HTTPPathModifier{Type: "ReplacePrefixMatch", ReplacePrefixMatch: s("/x")}, "Exact", ""); err == nil {
		t.Error("ReplacePrefixMatch on an exact match")
	}
	for _, tc := range []struct {
		rd       *HTTPRequestRedirect
		pathType string
		prefix   string
		want     string
	}{
		{&HTTPRequestRedirect{}, "PathPrefix", "", `{"module":"redirect","params":{"status":302,"to":"{scheme}://{host}{uri}"}}`},
		{&HTTPRequestRedirect{Hostname: s("b.example"), Port: n(8443), Path: &HTTPPathModifier{Type: "ReplaceFullPath", ReplaceFullPath: s("/x")}}, "Exact", "",
			`{"module":"redirect","params":{"status":302,"to":"{scheme}://b.example:8443/x"}}`},
		{&HTTPRequestRedirect{Path: &HTTPPathModifier{Type: "ReplacePrefixMatch", ReplacePrefixMatch: s("/new/")}}, "PathPrefix", "/old",
			`{"module":"redirect","params":{"regexp":"^/old(/.*)?$","status":302,"to":"{scheme}://{host}/new$1"}}`},
	} {
		got, err := redirect(tc.rd, tc.pathType, tc.prefix)
		if err != nil || string(got) != tc.want {
			t.Errorf("redirect(%+v): %s %v, want %s", tc.rd, got, err, tc.want)
		}
	}
	if _, err := redirect(&HTTPRequestRedirect{Port: n(1)}, "PathPrefix", ""); err == nil {
		t.Error("a port without a host name")
	}
}

func TestMirror(t *testing.T) {
	w := base(t)
	w.routes = []*HTTPRoute{obj[HTTPRoute](t, `{"metadata": {"namespace": "default", "name": "m"}, "spec": {"parentRefs": [{"name": "gw", "sectionName": "http"}],
  "rules": [{"filters": [{"type": "RequestMirror", "requestMirror": {"backendRef": {"name": "api", "port": 8080}, "fraction": {"numerator": 1, "denominator": 4}}},
                         {"type": "RequestMirror", "requestMirror": {"backendRef": {"name": "api", "port": 8080}}}],
             "backendRefs": [{"name": "web", "port": 80}]}]}}`)}
	p, _, problems := run(t, w, &config.KubernetesProvider{})
	if got := toJSON(p.Routes[0].Handler.Params); got != `{"mirror":{"percent":25,"upstream":"default.api.8080"},"upstream":"default.web.80"}` {
		t.Errorf("mirror: %s", got)
	}
	if !reflect.DeepEqual(problems, []string{
		"gateway default/gw: listener tls: an HTTPS listener needs a certificateRef",
		"httproute default/m: rule 0: match 0: only one RequestMirror is supported; the others are left out",
	}) {
		t.Errorf("problems: %v", problems)
	}
}
