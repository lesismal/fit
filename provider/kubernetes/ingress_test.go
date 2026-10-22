package kubernetes

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
)

// ingressWorld is base's Services, fit's IngressClass and another's, and
// no Gateways.
func ingressWorld(t *testing.T, defaultClass bool) *world {
	w := base(t)
	w.gateways = nil
	ann := ""
	if defaultClass {
		ann = `, "annotations": {"ingressclass.kubernetes.io/is-default-class": "true"}`
	}
	w.ingressClasses = []*IngressClass{
		obj[IngressClass](t, `{"metadata": {"name": "fit"`+ann+`}, "spec": {"controller": "lesismal.github.io/fit"}}`),
		obj[IngressClass](t, `{"metadata": {"name": "nginx"}, "spec": {"controller": "k8s.io/ingress-nginx"}}`),
	}
	return w
}

func ingress(t *testing.T, name, created, annotations, spec string) *Ingress {
	return obj[Ingress](t, `{"metadata": {"namespace": "default", "name": "`+name+`", "creationTimestamp": "`+created+`", "annotations": {`+
		annotations+`}}, "spec": `+spec+`}`)
}

// ingressPart translates w with ingress set as in.
func ingressPart(t *testing.T, w *world, in *config.KubernetesIngress) (*translator, part) {
	t.Helper()
	tr := newTranslator("k8s", &config.KubernetesProvider{Ingress: in}, w.snapshot(), "/certs")
	data := tr.run()
	var p part
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatalf("%v: %s", err, data)
	}
	checkPart(t, data)
	return tr, p
}

func TestIngress(t *testing.T) {
	crt, key := testCert(t, "shop.example.com")
	w := ingressWorld(t, true)
	w.secrets = []*Secret{obj[Secret](t, secretDoc("default", "shop-tls", crt, key))}
	w.ingresses = []*Ingress{
		ingress(t, "shop", "2026-01-01T00:00:00Z", `"fit.lesismal.github.io/ssl-redirect": "true", "fit.lesismal.github.io/limit-rps": "10",
  "fit.lesismal.github.io/proxy-body-size": "1m", "fit.lesismal.github.io/request-timeout": "30s"`, `{"ingressClassName": "fit",
  "tls": [{"hosts": ["shop.example.com"], "secretName": "shop-tls"}],
  "rules": [{"host": "shop.example.com", "http": {"paths": [
    {"path": "/api", "pathType": "Prefix", "backend": {"service": {"name": "api", "port": {"number": 8080}}}},
    {"path": "/health", "pathType": "Exact", "backend": {"service": {"name": "web", "port": {"name": "http"}}}}]}}]}`),
		ingress(t, "legacy", "2026-01-02T00:00:00Z", `"kubernetes.io/ingress.class": "fit", "fit.lesismal.github.io/rewrite-target": "/v2",
  "fit.lesismal.github.io/backend-protocol": "GRPC", "fit.lesismal.github.io/allow-source-range": "10.0.0.0/8, 192.168.1.1"`, `{
  "rules": [{"host": "*.example.com", "http": {"paths": [{"path": "/", "pathType": "ImplementationSpecific", "backend": {"service": {"name": "api", "port": {"number": 8080}}}}]}}]}`),
		// No class: fit's is the default.
		ingress(t, "defaulted", "2026-01-03T00:00:00Z", ``, `{"defaultBackend": {"service": {"name": "web", "port": {"number": 80}}},
  "tls": [{"secretName": "missing"}]}`),
		ingress(t, "theirs", "2026-01-04T00:00:00Z", ``, `{"ingressClassName": "nginx", "defaultBackend": {"service": {"name": "web", "port": {"number": 80}}}}`),
		ingress(t, "broken", "2026-01-05T00:00:00Z", `"fit.lesismal.github.io/limit-rps": "fast", "fit.lesismal.github.io/unknown": "x",
  "fit.lesismal.github.io/proxy-body-size": "lots"`, `{"ingressClassName": "fit",
  "rules": [{"host": "b.example.com", "http": {"paths": [
    {"path": "/x", "pathType": "Exact", "backend": {"service": {"name": "missing", "port": {"number": 80}}}},
    {"path": "/r", "pathType": "Prefix", "backend": {"resource": {"kind": "Bucket", "name": "b"}}},
    {"path": "/p", "pathType": "Exact", "backend": {"service": {"name": "web", "port": {"name": "nope"}}}},
    {"path": "/q", "pathType": "Regex", "backend": {"service": {"name": "web", "port": {"number": 80}}}}]}}]}`),
		ingress(t, "closed", "2026-01-06T00:00:00Z", `"fit.lesismal.github.io/allow-source-range": "10.0.0.0/8, nope"`, `{"ingressClassName": "fit",
  "rules": [{"host": "c.example.com", "http": {"paths": [{"path": "/", "pathType": "Prefix", "backend": {"service": {"name": "web", "port": {"number": 80}}}}]}}]}`),
	}
	tr, p := ingressPart(t, w, &config.KubernetesIngress{})

	if got := toJSON(p.Listeners); !strings.HasPrefix(got, `[{"name":"k8s-80","address":":80","protocols":["http/1.1","h2c"]},{"name":"k8s-443","address":":443","protocols":["http/1.1","h2"],"tls":{"certificates":[{"cert_file":"/certs/default.shop-tls.`) {
		t.Errorf("listeners: %s", got)
	}
	want := []string{
		"default.shop.ing.r0.p0", "default.shop.ing.r0.p0.redirect", "default.shop.ing.r0.p1", "default.shop.ing.r0.p1.redirect",
		"default.legacy.ing.r0.p0", "default.legacy.ing.r0.p0.deny",
		"default.defaulted.ing.default",
		"default.broken.ing.r0.p0", "default.broken.ing.r0.p1", "default.broken.ing.r0.p2",
		"default.closed.ing.r0.p0",
	}
	if got := p.names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("routes: %v", got)
	}
	route := func(name string) (r struct {
		Name, Listeners, Match, Middleware, Handler string
		Priority                                    int
	}) {
		x := p.Routes[p.route(t, name)]
		r.Name, r.Listeners, r.Match, r.Priority = x.Name, toJSON(x.Listeners), toJSON(x.Match), x.Priority
		r.Middleware, r.Handler = toJSON(x.Middleware), toJSON(x.Handler)
		return r
	}

	// shop: HTTPS for its TLS host, HTTP redirected there; limits, timeout.
	r := route("default.shop.ing.r0.p0")
	if r.Listeners != `["k8s-443"]` || r.Match != `{"host":["shop.example.com"],"path":["/api","/api/*"]}` ||
		r.Middleware != `[{"module":"rate_limit","params":{"rate":10}},{"module":"body_limit","params":{"max_bytes":1048576}}]` ||
		r.Handler != `{"module":"proxy","params":{"timeout":"30s","upstream":"default.api.8080"}}` {
		t.Errorf("shop p0: %+v", r)
	}
	rd := route("default.shop.ing.r0.p0.redirect")
	if rd.Listeners != `["k8s-80"]` || rd.Match != r.Match || rd.Middleware != `[{"module":"redirect","params":{"status":308,"to":"https://shop.example.com{uri}"}}]` {
		t.Errorf("shop p0's redirect: %+v", rd)
	}
	if exact := route("default.shop.ing.r0.p1"); exact.Priority <= r.Priority || exact.Handler != `{"module":"proxy","params":{"timeout":"30s","upstream":"default.web.80"}}` {
		t.Errorf("shop p1, by the port's name: %+v", exact)
	}

	// legacy: any path of a wildcard host, from the ranges allowed, the
	// others refused; rewritten, over h2c.
	r = route("default.legacy.ing.r0.p0")
	deny := route("default.legacy.ing.r0.p0.deny")
	if r.Listeners != `["k8s-80"]` || r.Match != `{"host":["*.example.com"],"remote_ip":["10.0.0.0/8","192.168.1.1"]}` ||
		r.Middleware != `[{"module":"rewrite","params":{"add_prefix":"/v2"}}]` || r.Handler != `{"module":"proxy","params":{"upstream":"default.api.8080.h2c"}}` ||
		deny.Match != `{"host":["*.example.com"]}` || deny.Handler != `{"module":"respond","params":{"status":403}}` || deny.Priority >= r.Priority {
		t.Errorf("legacy: %+v\n%+v", r, deny)
	}

	// defaulted: what nothing else takes, on HTTP: its Secret is missing.
	if d := route("default.defaulted.ing.default"); d.Listeners != `["k8s-80"]` || d.Match != "null" || d.Priority != 0 {
		t.Errorf("defaulted: %+v", d)
	}

	// broken: 503s; closed: 403 for all, its range not parsing.
	for _, name := range []string{"default.broken.ing.r0.p0", "default.broken.ing.r0.p1", "default.broken.ing.r0.p2"} {
		if r := route(name); r.Handler != `{"module":"respond","params":{"status":503}}` || r.Middleware != "null" {
			t.Errorf("%s: %+v", name, r)
		}
	}
	if r := route("default.closed.ing.r0.p0"); r.Handler != `{"module":"respond","params":{"status":403}}` {
		t.Errorf("closed: %+v", r)
	}
	hasProblems(t, tr.problems,
		"ingress default/defaulted: tls 0: there is no secret default/missing of type kubernetes.io/tls",
		`ingress default/broken: annotation fit.lesismal.github.io/limit-rps: "fast" is not a rate of at least 0.001; it is left out`,
		`ingress default/broken: annotation fit.lesismal.github.io/proxy-body-size: "lots" is not a size, as 8m; it is left out`,
		"ingress default/broken: annotation fit.lesismal.github.io/unknown is not one fit knows",
		"ingress default/broken: rule 0: path 0: service default/missing does not exist; it answers 503",
		"ingress default/broken: rule 0: path 1: only a Service is supported as a backend; it answers 503",
		`ingress default/broken: rule 0: path 2: service default/web has no port named "nope"; it answers 503`,
		`ingress default/broken: rule 0: path 3: path type "Regex" is not supported`,
		`ingress default/closed: annotation fit.lesismal.github.io/allow-source-range: "nope" is not an address or CIDR range; every request is refused`,
	)

	// Without a default class, an Ingress naming none is not fit's; without
	// ingress, none is.
	w.ingressClasses = ingressWorld(t, false).ingressClasses
	if _, p := ingressPart(t, w, &config.KubernetesIngress{}); strings.Contains(strings.Join(p.names(), " "), "defaulted") {
		t.Errorf("no default class: %v", p.names())
	}
	if _, p := ingressPart(t, w, nil); len(p.Routes) != 0 || len(p.Listeners) != 0 {
		t.Errorf("no ingress: %v", p.names())
	}
}

func TestIngressPorts(t *testing.T) {
	crt, key := testCert(t, "shop.example.com")
	w := ingressWorld(t, false)
	w.secrets = []*Secret{obj[Secret](t, secretDoc("default", "shop-tls", crt, key))}
	w.ingresses = []*Ingress{ingress(t, "shop", "", `"fit.lesismal.github.io/ssl-redirect": "true"`, `{"ingressClassName": "fit",
  "tls": [{"hosts": ["shop.example.com", "*.shop.example.com"], "secretName": "shop-tls"}],
  "rules": [{"host": "shop.example.com", "http": {"paths": [{"pathType": "Prefix", "backend": {"service": {"name": "api", "port": {"number": 8080}}}}]}},
            {"host": "*.shop.example.com", "http": {"paths": [{"pathType": "Prefix", "backend": {"service": {"name": "api", "port": {"number": 8080}}}}]}}]}`)}

	// Other ports: the redirect names the HTTPS port; a wildcard host is not
	// redirected, its port unknown.
	tr, p := ingressPart(t, w, &config.KubernetesIngress{HTTPPort: 8080, HTTPSPort: 8443})
	if got := p.Routes[p.route(t, "default.shop.ing.r0.p0.redirect")].Middleware; len(got) != 1 ||
		string(got[0]) != `{"module":"redirect","params":{"status":308,"to":"https://shop.example.com:8443{uri}"}}` {
		t.Errorf("redirect: %s", got)
	}
	if got := toJSON(p.Routes[p.route(t, "default.shop.ing.r1.p0")].Listeners); got != `["k8s-8080","k8s-8443"]` {
		t.Errorf("the wildcard host: %s", got)
	}
	hasProblems(t, tr.problems, "ingress default/shop: rule 1: path 0: ssl-redirect: a wildcard or any host is redirected only from port 80 to 443")

	// A Gateway's TCP listener holds the HTTPS port: HTTP alone, unredirected.
	w.gateways = []*Gateway{obj[Gateway](t, `{"metadata": {"namespace": "default", "name": "l4"}, "spec": {"gatewayClassName": "fit", "listeners": [
  {"name": "tcp", "port": 443, "protocol": "TCP"}]}}`)}
	tr, p = ingressPart(t, w, &config.KubernetesIngress{})
	if got := toJSON(p.Routes[p.route(t, "default.shop.ing.r0.p0")].Listeners); got != `["k8s-80"]` {
		t.Errorf("HTTPS port taken: %s", got)
	}
	hasProblems(t, tr.problems, "ingress default/shop: port 443 serves tcp already: its rules are not served over HTTPS")
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{"0": 0, "100": 100, "8k": 8 << 10, "1m": 1 << 20, "2G": 2 << 30} {
		if got, err := parseSize(in); err != nil || got != want {
			t.Errorf("%s: %d, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "-1", "1t", "m", "99999999999g"} {
		if _, err := parseSize(in); err == nil {
			t.Errorf("%s: no error", in)
		}
	}
}

// TestProviderIngress runs the provider with ingress against the fake API
// server: Ingresses and IngressClasses are watched, and served.
func TestProviderIngress(t *testing.T) {
	shorten(t)
	a := newAPIServer(t)
	a.put(kindIngressClasses.path, `{"metadata": {"name": "fit"}, "spec": {"controller": "lesismal.github.io/fit"}}`)
	a.put(kindServices.path, `{"metadata": {"namespace": "default", "name": "web"}, "spec": {"ports": [{"port": 80}]}}`)
	a.put(kindSlices.path, `{"metadata": {"namespace": "default", "name": "web-1", "labels": {"kubernetes.io/service-name": "web"}},
  "endpoints": [{"addresses": ["10.1.0.1"]}], "ports": [{"port": 8080}]}`)
	p := New("k8s", &config.KubernetesProvider{Kubeconfig: a.kubeconfig(t), Ingress: &config.KubernetesIngress{HTTPPort: 8000, HTTPSPort: 8443}}, nil)
	got := make(parts, 8)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Run(ctx, got.update)
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("Run did not return")
		}
	}()
	if first := got.next(t); len(first.Routes) != 0 {
		t.Fatalf("at the start: %+v", first)
	}
	a.put(kindIngresses.path, `{"metadata": {"namespace": "default", "name": "i"}, "spec": {"ingressClassName": "fit",
  "rules": [{"http": {"paths": [{"path": "/", "pathType": "Prefix", "backend": {"service": {"name": "web", "port": {"number": 80}}}}]}}]}}`)
	next := got.next(t)
	if len(next.Routes) != 1 || toJSON(next.Routes[0].Listeners) != `["k8s-8000"]` || next.Routes[0].Handler.Params["upstream"] != "default.web.80" {
		t.Fatalf("after an Ingress: %+v", next)
	}
}
