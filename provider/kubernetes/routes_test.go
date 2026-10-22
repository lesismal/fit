package kubernetes

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
)

// testCert returns a self-signed certificate for names, and its key, in
// PEM.
func testCert(t *testing.T, names ...string) (crt, key []byte) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: names, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	kd, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kd})
}

// secretDoc is a Secret of type kubernetes.io/tls, as the API server
// writes it.
func secretDoc(ns, name string, crt, key []byte) string {
	return fmt.Sprintf(`{"metadata": {"namespace": %q, "name": %q}, "type": "kubernetes.io/tls", "data": {"tls.crt": %q, "tls.key": %q}}`,
		ns, name, base64.StdEncoding.EncodeToString(crt), base64.StdEncoding.EncodeToString(key))
}

// translated runs a translator of w, with certificates in /certs.
func translated(t *testing.T, w *world) (*translator, part) {
	t.Helper()
	tr := newTranslator("k8s", &config.KubernetesProvider{}, w.snapshot(), "/certs")
	data := tr.run()
	var p part
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatalf("%v: %s", err, data)
	}
	checkPart(t, data)
	return tr, p
}

// hasProblems fails t unless each of want begins one of problems.
func hasProblems(t *testing.T, problems []string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !slices.ContainsFunc(problems, func(p string) bool { return strings.HasPrefix(p, w) }) {
			t.Errorf("no problem %q in:\n%s", w, strings.Join(problems, "\n"))
		}
	}
}

func TestHTTPS(t *testing.T) {
	crtA, keyA := testCert(t, "a.example.com")
	crtB, keyB := testCert(t, "*.b.example.com")
	w := base(t)
	w.secrets = []*Secret{
		obj[Secret](t, secretDoc("default", "a", crtA, keyA)),
		obj[Secret](t, secretDoc("certs", "b", crtB, keyB)),
		obj[Secret](t, secretDoc("default", "bad", crtA, keyB)),
	}
	w.gateways = []*Gateway{
		obj[Gateway](t, `{"metadata": {"namespace": "default", "name": "secure", "creationTimestamp": "2026-01-01T00:00:00Z"}, "spec": {"gatewayClassName": "fit", "listeners": [
  {"name": "a", "port": 443, "protocol": "HTTPS", "hostname": "a.example.com", "tls": {"certificateRefs": [{"name": "a"}]}},
  {"name": "b", "port": 443, "protocol": "HTTPS", "hostname": "*.b.example.com", "tls": {"certificateRefs": [{"name": "b", "namespace": "certs"}]}},
  {"name": "pass", "port": 443, "protocol": "TLS", "hostname": "db.example.com", "tls": {"mode": "Passthrough"}},
  {"name": "bad", "port": 8443, "protocol": "HTTPS", "tls": {"certificateRefs": [{"name": "bad"}, {"name": "missing"}, {"kind": "ConfigMap", "name": "x"}]}},
  {"name": "term", "port": 9443, "protocol": "TLS"}]}}`),
		obj[Gateway](t, `{"metadata": {"namespace": "default", "name": "late", "creationTimestamp": "2026-01-02T00:00:00Z"}, "spec": {"gatewayClassName": "fit", "listeners": [
  {"name": "plain", "port": 443, "protocol": "HTTP"}]}}`),
	}
	w.routes = []*HTTPRoute{obj[HTTPRoute](t, `{"metadata": {"namespace": "default", "name": "site"}, "spec": {
  "parentRefs": [{"name": "secure", "sectionName": "a"}], "rules": [{"backendRefs": [{"name": "api", "port": 8080}]}]}}`)}
	w.tlsRoutes = []*L4Route{obj[L4Route](t, `{"metadata": {"namespace": "default", "name": "db"}, "spec": {
  "parentRefs": [{"name": "secure"}], "rules": [{"backendRefs": [{"name": "api", "port": 8080}]}]}}`)}

	tr, p := translated(t, w)
	pemA := slices.Concat(crtA, []byte("\n"), keyA)
	sum := sha256.Sum256(pemA)
	fileA := fmt.Sprintf("/certs/default.a.%x.pem", sum[:6])
	if got := toJSON(p.Listeners); got != `[{"name":"k8s-443","address":":443","protocols":["http/1.1","h2","tcp"],"tls":{"certificates":[{"cert_file":"`+fileA+`","key_file":"`+fileA+`"}]}}]` {
		t.Errorf("listeners: %s", got)
	}
	if len(tr.certs) != 1 || string(tr.certs[fileA]) != string(pemA) {
		t.Errorf("certificate files: %v", slices.Collect(func(yield func(string) bool) {
			for k := range tr.certs {
				yield(k)
			}
		}))
	}
	if r := p.Routes[p.route(t, "default.site.r0.m0")]; toJSON(r.Listeners) != `["k8s-443"]` || toJSON(r.Match.Host) != `["a.example.com"]` {
		t.Errorf("site: %+v %+v", r, r.Match)
	}
	// The TLS route takes the passthrough listener alone, for its host name.
	if got := toJSON(p.Services); got != `[{"name":"default.db.tls","listeners":["k8s-443"],"upstream":"default.api.8080","server_names":["db.example.com"]}]` {
		t.Errorf("services: %s", got)
	}
	hasProblems(t, tr.problems,
		"gateway default/secure: listener b: certificateRef 0: secret certs/b is in another namespace, and no ReferenceGrant there allows it",
		"gateway default/secure: listener bad: certificateRef 0: secret default/bad: tls: private key does not match public key",
		"gateway default/secure: listener bad: certificateRef 1: there is no secret default/missing of type kubernetes.io/tls",
		"gateway default/secure: listener bad: certificateRef 2: /ConfigMap is not a Secret",
		"gateway default/secure: listener term: fit passes the connections of TLS listeners through",
		"gateway default/late: listener plain: port 443 serves tls already",
	)
	gl := tr.byKey["default/late"].listeners[0]
	if gl.conflict != "ProtocolConflict" || gl.fit != "" {
		t.Errorf("plain: %+v", gl)
	}
	if gl := tr.byKey["default/secure"].listeners[1]; gl.unresolved != "RefNotPermitted" || gl.fit != "" {
		t.Errorf("b: %+v", gl)
	}
	if gl := tr.byKey["default/secure"].listeners[3]; gl.unresolved != "InvalidCertificateRef" || gl.fit != "" {
		t.Errorf("bad: %+v", gl)
	}

	// A grant lets the Gateway use the other namespace's Secret.
	w.grants = []*ReferenceGrant{obj[ReferenceGrant](t, `{"metadata": {"namespace": "certs", "name": "g"}, "spec": {
  "from": [{"group": "gateway.networking.k8s.io", "kind": "Gateway", "namespace": "default"}], "to": [{"group": "", "kind": "Secret"}]}}`)}
	tr, p = translated(t, w)
	if len(p.Listeners) != 1 || len(p.Listeners[0].TLS.Certificates) != 2 || len(tr.certs) != 2 {
		t.Errorf("with a grant: %s", toJSON(p.Listeners))
	}

	// With nowhere to write certificates, HTTPS is not served.
	tr = newTranslator("k8s", &config.KubernetesProvider{}, w.snapshot(), "")
	var p2 part
	_ = json.Unmarshal(tr.run(), &p2)
	if got := toJSON(p2.Listeners); got != `[{"name":"k8s-443","address":":443","protocols":["tcp"]}]` {
		t.Errorf("no directory: %s", got)
	}
	hasProblems(t, tr.problems, "gateway default/secure: listener a: there is no directory to write certificates to")
}

func TestGRPCRoute(t *testing.T) {
	w := base(t)
	w.gateways = []*Gateway{obj[Gateway](t, `{"metadata": {"namespace": "default", "name": "g"}, "spec": {"gatewayClassName": "fit", "listeners": [
  {"name": "http", "port": 80, "protocol": "HTTP"},
  {"name": "httponly", "port": 81, "protocol": "HTTP", "allowedRoutes": {"kinds": [{"kind": "HTTPRoute"}]}}]}}`)}
	w.grpcRoutes = []*GRPCRoute{
		obj[GRPCRoute](t, `{"metadata": {"namespace": "default", "name": "echo", "creationTimestamp": "2026-01-01T00:00:00Z"}, "spec": {
  "parentRefs": [{"name": "g"}], "hostnames": ["grpc.example.com"],
  "rules": [
    {"matches": [{"method": {"service": "echo.Echo", "method": "Say"}},
                 {"method": {"service": "echo.Echo"}},
                 {"method": {"method": "Ping"}},
                 {"method": {"type": "RegularExpression", "service": "echo\\..*"}, "headers": [{"name": "x-v", "value": "2"}]}],
     "filters": [{"type": "RequestHeaderModifier", "requestHeaderModifier": {"set": [{"name": "x-a", "value": "1"}]}}],
     "backendRefs": [{"name": "api", "port": 8080}]},
    {"backendRefs": [{"name": "web", "port": 80}]},
    {"matches": [{"method": {"type": "RegularExpression", "service": "("}}], "backendRefs": [{"name": "web", "port": 80}]}]}}`),
		obj[GRPCRoute](t, `{"metadata": {"namespace": "default", "name": "only", "creationTimestamp": "2026-01-02T00:00:00Z"}, "spec": {
  "parentRefs": [{"name": "g", "sectionName": "httponly"}], "rules": [{"backendRefs": [{"name": "api", "port": 8080}]}]}}`),
	}
	tr, p := translated(t, w)
	if got := p.names(); !reflect.DeepEqual(got, []string{"default.echo.grpc.r0.m0", "default.echo.grpc.r0.m1", "default.echo.grpc.r0.m2",
		"default.echo.grpc.r0.m3", "default.echo.grpc.r1.m0"}) {
		t.Fatalf("routes: %v", got)
	}
	want := []string{
		`{"host":["grpc.example.com"],"path":["/echo.Echo/Say"]}`,
		`{"host":["grpc.example.com"],"path":["/echo.Echo/*"]}`,
		`{"host":["grpc.example.com"],"path_regexp":["^/[^/]+/Ping$"]}`,
		`{"host":["grpc.example.com"],"path_regexp":["^/(?:echo\\..*)/[^/]+$"],"header":{"x-v":["2"]}}`,
		`{"host":["grpc.example.com"]}`,
	}
	for i, r := range p.Routes {
		if got := toJSON(r.Match); got != want[i] || toJSON(r.Listeners) != `["k8s-80"]` {
			t.Errorf("%s: %s on %v, want %s", r.Name, got, r.Listeners, want[i])
		}
	}
	if !(p.Routes[0].Priority > p.Routes[1].Priority && p.Routes[1].Priority > p.Routes[2].Priority && p.Routes[2].Priority > p.Routes[4].Priority) {
		t.Errorf("priorities: %d %d %d %d", p.Routes[0].Priority, p.Routes[1].Priority, p.Routes[2].Priority, p.Routes[4].Priority)
	}
	if r := p.Routes[0]; r.Handler.Params["upstream"] != "default.api.8080.h2c" || len(r.Middleware) != 1 ||
		string(r.Middleware[0]) != `{"module":"headers","params":{"request":{"set":{"x-a":"1"}}}}` {
		t.Errorf("echo r0: %s %+v", r.Middleware, r.Handler)
	}
	// web's port is h2c already: its upstream serves HTTPRoutes and
	// GRPCRoutes alike.
	if got := toJSON(p.Upstreams); got != `[{"name":"default.api.8080.h2c","addresses":["10.0.1.1:9000"],"protocol":"h2c"},{"name":"default.web.80","addresses":["10.0.0.1:8000","10.0.0.2:8000"],"protocol":"h2c"}]` {
		t.Errorf("upstreams: %s", got)
	}
	hasProblems(t, tr.problems,
		`grpcroute default/echo: rule 2: match 0: method "" of service "(" is not a regular expression fit takes`,
		"grpcroute default/only: no listener of its Gateways takes it")
	if st := tr.states[1]; st.parents[0].accepted || st.parents[0].reason != "NotAllowedByListeners" {
		t.Errorf("only: %+v", st.parents[0])
	}
}

func TestL4Routes(t *testing.T) {
	w := base(t)
	w.gateways = []*Gateway{obj[Gateway](t, `{"metadata": {"namespace": "default", "name": "l4"}, "spec": {"gatewayClassName": "fit", "listeners": [
  {"name": "tcp", "port": 5432, "protocol": "TCP"},
  {"name": "udp", "port": 53, "protocol": "UDP"},
  {"name": "udp2", "port": 5353, "protocol": "UDP"},
  {"name": "tcp53", "port": 53, "protocol": "TCP"},
  {"name": "pass", "port": 443, "protocol": "TLS", "tls": {"mode": "Passthrough"}}]}}`)}
	w.services = append(w.services,
		obj[Service](t, `{"metadata": {"namespace": "default", "name": "dns"}, "spec": {"ports": [
  {"name": "dns", "port": 53, "protocol": "UDP"}, {"name": "dns-tcp", "port": 53, "protocol": "TCP"}]}}`),
		service(t, "default", "db", 5432, "", ""), service(t, "default", "db2", 5432, "", ""))
	w.slices = append(w.slices,
		obj[EndpointSlice](t, `{"metadata": {"namespace": "default", "name": "dns-x", "labels": {"kubernetes.io/service-name": "dns"}},
  "endpoints": [{"addresses": ["10.0.5.1"]}], "ports": [{"name": "dns", "protocol": "UDP", "port": 5353}, {"name": "dns-tcp", "protocol": "TCP", "port": 5353}]}`),
		endpoints(t, "default", "db", "", 5432, map[string]bool{"10.0.6.1": true, "10.0.6.2": true}),
		endpoints(t, "default", "db2", "", 5432, map[string]bool{"10.0.7.1": true}))
	l4 := func(ns, name, created, spec string) *L4Route {
		return obj[L4Route](t, `{"metadata": {"namespace": "`+ns+`", "name": "`+name+`", "creationTimestamp": "`+created+`"}, "spec": `+spec+`}`)
	}
	w.tcpRoutes = []*L4Route{
		l4("default", "db", "2026-01-01T00:00:00Z", `{"parentRefs": [{"name": "l4", "sectionName": "tcp"}],
  "rules": [{"backendRefs": [{"name": "db", "port": 5432, "weight": 3}, {"name": "db2", "port": 5432}]}]}`),
		l4("default", "late", "2026-01-02T00:00:00Z", `{"parentRefs": [{"name": "l4", "sectionName": "tcp"}], "rules": [{"backendRefs": [{"name": "db", "port": 5432}]}]}`),
		l4("default", "dnstcp", "2026-01-03T00:00:00Z", `{"parentRefs": [{"name": "l4", "port": 53}], "rules": [{"backendRefs": [{"name": "dns", "port": 53}]}]}`),
	}
	w.udpRoutes = []*L4Route{
		l4("default", "dns", "2026-01-01T00:00:00Z", `{"parentRefs": [{"name": "l4", "sectionName": "udp"}], "rules": [{"backendRefs": [{"name": "dns", "port": 53}]}]}`),
		l4("default", "noudp", "2026-01-02T00:00:00Z", `{"parentRefs": [{"name": "l4", "sectionName": "udp2"}], "rules": [{"backendRefs": [{"name": "db", "port": 5432}]}]}`),
	}
	w.tlsRoutes = []*L4Route{
		l4("default", "pass", "2026-01-01T00:00:00Z", `{"parentRefs": [{"name": "l4"}], "hostnames": ["db.example.com", "*.db.example.com"],
  "rules": [{"backendRefs": [{"name": "db", "port": 5432}]}, {"backendRefs": [{"name": "db2", "port": 5432}]}]}`),
		l4("default", "pass2", "2026-01-02T00:00:00Z", `{"parentRefs": [{"name": "l4"}], "hostnames": ["db.example.com"], "rules": [{"backendRefs": [{"name": "db2", "port": 5432}]}]}`),
		l4("default", "rest", "2026-01-03T00:00:00Z", `{"parentRefs": [{"name": "l4"}], "rules": [{"backendRefs": [{"name": "db2", "port": 5432}]}]}`),
	}
	tr, p := translated(t, w)
	if got := toJSON(p.Listeners); got != `[{"name":"k8s-53","address":":53","protocols":["tcp"]},{"name":"k8s-53-udp","address":":53","protocols":["udp"]},`+
		`{"name":"k8s-443","address":":443","protocols":["tcp"]},{"name":"k8s-5432","address":":5432","protocols":["tcp"]}]` {
		t.Errorf("listeners: %s", got)
	}
	if got := toJSON(p.Services); got != `[{"name":"default.pass.tls","listeners":["k8s-443"],"upstream":"default.db.5432","server_names":["*.db.example.com","db.example.com"]},`+
		`{"name":"default.rest.tls","listeners":["k8s-443"],"upstream":"default.db2.5432"},`+
		`{"name":"default.db.tcp","listeners":["k8s-5432"],"upstream":"default.db.tcp"},`+
		`{"name":"default.dnstcp.tcp","listeners":["k8s-53"],"upstream":"default.dns.53"},`+
		`{"name":"default.dns.udp","listeners":["k8s-53-udp"],"upstream":"default.dns.53.udp"}]` {
		t.Errorf("services: %s", got)
	}
	// db's two endpoints share its weight of 3, db2's one has 1.
	if got := toJSON(p.Upstreams); got != `[{"name":"default.db.5432","addresses":["10.0.6.1:5432","10.0.6.2:5432"]},`+
		`{"name":"default.db.tcp","addresses":["10.0.6.1:5432","10.0.6.2:5432","10.0.7.1:5432"],"weights":[1000,1000,667]},`+
		`{"name":"default.db2.5432","addresses":["10.0.7.1:5432"]},`+
		`{"name":"default.dns.53","addresses":["10.0.5.1:5353"]},{"name":"default.dns.53.udp","addresses":["10.0.5.1:5353"]}]` {
		t.Errorf("upstreams: %s", got)
	}
	hasProblems(t, tr.problems,
		"tlsroute default/pass: only its first rule is served; the others are left out",
		"tlsroute default/pass2: no listener of its Gateways takes it",
		"tcproute default/late: no listener of its Gateways takes it",
		"udproute default/noudp: rule 0: backend 0: service default/db has no UDP port 5432",
	)
	for _, st := range tr.states {
		if k := st.m.key(); k == "default/late" || k == "default/pass2" {
			if ps := st.parents[0]; ps.accepted || ps.reason != "NotAllowedByListeners" || !strings.HasPrefix(ps.msg, "the listener carries ") {
				t.Errorf("%s: %+v", k, ps)
			}
		}
	}
}
