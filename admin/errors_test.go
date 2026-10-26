package admin

import (
	"context"
	"crypto/x509/pkix"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/certtest"
	"github.com/lesismal/fit/observe"
	"github.com/lesismal/fit/runtime"
	"github.com/lesismal/fit/upstream"
)

// TestRequestsRefused: requests the API cannot take are answered with the
// status that says why, and change nothing.
func TestRequestsRefused(t *testing.T) {
	f := start(t, doc("v1"), &config.Admin{})
	for _, tt := range []struct {
		method, path, contentType, body string
		status                          int
		want                            string
	}{
		{"DELETE", "/config/a~2b", "", "", 400, `not a JSON pointer`},
		{"PUT", "/config/a~2b", "application/json", `1`, 400, `not a JSON pointer`},
		{"PATCH", "/config/a~", "application/json", `{}`, 400, `not a JSON pointer`},
		{"GET", "/versions/x", "", "", 404, `versions are numbers`},
		{"GET", "/versions/99", "", "", 404, `version 99 is not kept`},
		{"POST", "/rollback/x", "", "", 404, `versions are numbers`},
		{"POST", "/rollback/99", "", "", 404, `version 99 is not kept`},
		{"POST", "/cache/purge", "text/plain", `{"all": true}`, 415, `application/json`},
		{"POST", "/load", "", doc("v2"), 415, `application/json`},
		{"GET", "/config/nope", "", "", 404, `there is no member \"nope\"`},
		{"GET", "/config/listeners/0/name/x", "", "", 404, `is neither an object nor an array`},
		{"PATCH", "/config/nope", "application/json", `{}`, 404, `there is no member \"nope\"`},
		{"DELETE", "/config/listeners/0/name/x", "", "", 404, `is neither an object nor an array`},
		{"DELETE", "/config/routes/9", "", "", 404, `past the end`},
		{"DELETE", "/config/nope/x", "", "", 404, `there is no member \"nope\"`},
		{"PUT", "/config/routes/x", "application/json", `{}`, 404, `is not an array index`},
		{"PUT", "/config/routes/0/handler/module", "application/json", `"a" "b"`, 400, `more than one JSON value`},
	} {
		resp, body := f.send(t, tt.method, tt.path, tt.contentType, tt.body, "")
		if resp.StatusCode != tt.status || !strings.Contains(body, tt.want) {
			t.Errorf("%s %s %s: %d %s", tt.method, tt.path, tt.body, resp.StatusCode, body)
		}
	}
	if v := f.configs.Current(); v.Number != 1 || f.served(t) != "v1" {
		t.Fatalf("after refused requests: version %d", v.Number)
	}
}

// TestDecodeErrors: JSON the tree cannot be made from is refused, and
// pointers into scalars are not found.
func TestDecodeErrors(t *testing.T) {
	for _, bad := range []string{`{1: 2}`, `[1, }`, `{"a": [1, }`, `{"a": }`, `[`} {
		if _, err := decodeJSON([]byte(bad)); err == nil {
			t.Errorf("decodeJSON(%q) succeeded", bad)
		}
	}
	root, err := decodeJSON([]byte(`{"s": "x", "a": [1]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/s/x", "/s/x/y"} {
		tokens, _ := parsePointer(p)
		if _, err := lookup(root, tokens); err == nil {
			t.Errorf("lookup(%s) succeeded", p)
		}
		if _, err := put(root, tokens, nil); err == nil {
			t.Errorf("put(%s) succeeded", p)
		}
		if _, err := remove(root, tokens); err == nil {
			t.Errorf("remove(%s) succeeded", p)
		}
	}
	for _, p := range []string{"/a/x", "/a/5", "/q/x"} {
		tokens, _ := parsePointer(p)
		if _, err := remove(root, tokens); err == nil {
			t.Errorf("remove(%s) succeeded", p)
		}
	}
	// A merge patch removes a member that is not there, and makes an object
	// of what was not one.
	patch, _ := decodeJSON([]byte(`{"nope": null, "s": {"k": 1}}`))
	got := string(encodeJSON(mergePatch(root, patch)))
	if want := "{\n  \"s\": {\n    \"k\": 1\n  },\n  \"a\": [\n    1\n  ]\n}\n"; got != want {
		t.Errorf("merged:\n%s\nwant:\n%s", got, want)
	}
}

func TestStartFails(t *testing.T) {
	f := start(t, doc("v1"), &config.Admin{})
	dir := t.TempDir()
	if _, err := Start(&config.Admin{Address: "127.0.0.1:0", TokenFile: filepath.Join(dir, "missing")}, f.configs, nil); err == nil ||
		!strings.Contains(err.Error(), "token_file") {
		t.Errorf("a missing token file: %v", err)
	}
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(&config.Admin{Address: "127.0.0.1:0", TokenFile: empty}, f.configs, nil); err == nil ||
		!strings.Contains(err.Error(), "is empty") {
		t.Errorf("an empty token file: %v", err)
	}
	// An address taken already.
	if _, err := Start(&config.Admin{Address: f.admin.Addr().String()}, f.configs, nil); err == nil {
		t.Error("Start on an address in use succeeded")
	}
}

// TestUnixSocketFresh: a socket path with nothing at it is listened on.
func TestUnixSocketFresh(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("Unix sockets are tested on Unix")
	}
	dir, err := os.MkdirTemp("", "fit")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	start(t, doc("v1"), &config.Admin{Address: config.AdminUnixPrefix + filepath.Join(dir, "a.sock")})
}

// TestShutdownTimesOut: a request still under way when Shutdown's context
// ends has its connection closed, and Shutdown says so; report hears each
// config the API applies.
func TestShutdownTimesOut(t *testing.T) {
	f := start(t, doc("v1"), &config.Admin{})
	entered, release := make(chan Result, 1), make(chan struct{})
	s, err := Start(&config.Admin{Address: "127.0.0.1:0"}, f.configs, func(r Result) {
		entered <- r
		<-release
	})
	if err != nil {
		t.Fatal(err)
	}
	defer close(release)
	done := make(chan struct{})
	go func() {
		defer close(done)
		req, _ := http.NewRequest("POST", "http://"+s.Addr().String()+"/load", strings.NewReader(doc("v2")))
		req.Header.Set("Content-Type", "application/json")
		if resp, err := f.client.Do(req); err == nil {
			resp.Body.Close()
		}
	}()
	select {
	case r := <-entered:
		if r.Version != 2 {
			t.Errorf("reported %+v", r)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the load was not reported")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Shutdown(ctx); err == nil {
		t.Error("Shutdown with a request under way and its context done succeeded")
	}
	<-done
}

func TestConfigsMore(t *testing.T) {
	// A base config with comments is kept, and runs, though providers
	// cannot merge into it.
	f := start(t, "// a comment\n"+doc("v1"), &config.Admin{})
	if v := f.configs.Current(); string(v.Effective()) != string(v.Data()) {
		t.Errorf("effective %q, data %q", v.Effective(), v.Data())
	}
	if f.served(t) != "v1" {
		t.Fatal("not served")
	}

	r, err := f.configs.Reapply(SourceCertificates)
	if err != nil || r.Version != 2 || f.configs.Current().Source != SourceCertificates {
		t.Fatalf("Reapply: %+v %v", r, err)
	}

	if err := f.configs.AddProvider("p"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.configs.Provide("p", []byte(`not json`)); err == nil {
		t.Error("Provide took what is not JSON")
	}
	if _, err := f.configs.Provide("p", []byte(fakeRoute("pr", "/p", "from p"))); err != nil {
		t.Fatal(err)
	}
	// A merge patch of the whole config.
	if resp, body := f.send(t, "PATCH", "/config", "application/merge-patch+json", `{}`, ""); resp.StatusCode != 200 {
		t.Errorf("PATCH /config: %d %s", resp.StatusCode, body)
	}
	// A base config providers' parts cannot be merged into is refused.
	if _, err := f.configs.Apply([]byte(`{"listeners": {}}`), SourceAdmin); err == nil {
		t.Error("a base config with listeners not an array was applied")
	}

	// Changes to what takes effect at a restart are warned of.
	r, err = f.configs.Apply([]byte(`{"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "telemetry": {"metrics": {}}, "providers": [{"docker": {}}], "acme": {"email": "a@example.com"},
  "engine": {"workers": 64},
  "routes": [{"handler": {"module": "respond", "params": {"body": "v2"}}}]}`), SourceAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(r.Warnings, "\n"); !strings.Contains(got, "telemetry changed") || !strings.Contains(got, "providers changed") ||
		!strings.Contains(got, "acme changed") || !strings.Contains(got, "engine changed") {
		t.Errorf("warnings: %q", r.Warnings)
	}
}

func TestSame(t *testing.T) {
	al := func(out string) *config.AccessLog { return &config.AccessLog{Output: out} }
	if !sameAccessLog(al("a"), al("a")) || sameAccessLog(al("a"), al("b")) || sameAccessLog(nil, al("a")) {
		t.Error("sameAccessLog")
	}
	acme := func(email string, od *config.OnDemand) *config.ACME { return &config.ACME{Email: email, OnDemand: od} }
	if !sameACME(acme("a", nil), acme("a", nil)) || sameACME(acme("a", nil), acme("b", nil)) || sameACME(nil, acme("a", nil)) {
		t.Error("sameACME")
	}
	od := func(ask string) *config.OnDemand { return &config.OnDemand{Ask: ask} }
	if !sameACME(acme("a", od("x")), acme("a", od("x"))) || sameACME(acme("a", od("x")), acme("a", od("y"))) ||
		sameACME(acme("a", od("x")), acme("a", nil)) {
		t.Error("sameACME with on_demand")
	}
}

// TestStatusAndMetricsMore: upstreams and certificates are listed in order,
// certificates by the names they have, backends taken out are scraped as
// down, and the tracer's and the access log's counts are scraped.
func TestStatusAndMetricsMore(t *testing.T) {
	deadLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := deadLn.Addr().String()
	deadLn.Close()
	engine, stop, err := upstream.StartEngine()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	dir := t.TempDir()
	ipCert, err := certtest.Write(dir, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	dnsCert, err := certtest.Write(dir, "b.example")
	if err != nil {
		t.Fatal(err)
	}
	ca, err := certtest.NewCA("test CA")
	if err != nil {
		t.Fatal(err)
	}
	cnCert, err := ca.IssueClient(dir, pkix.Name{CommonName: "only-cn"})
	if err != nil {
		t.Fatal(err)
	}
	tracer, err := observe.NewTracer(&config.Telemetry{Endpoint: "http://" + dead, Traces: &config.TracesExport{Interval: config.Duration(time.Hour)}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tracer.Close() })
	accessLog := observe.NewAccessLogTo(io.Discard, 0, 0)
	t.Cleanup(func() { _ = accessLog.Close() })

	f := startEnv(t, fmt.Sprintf(`{
  "listeners": [
    {"name": "web", "address": "127.0.0.1:0"},
    {"name": "z", "address": "127.0.0.1:0", "tls": {"certificates": [{"cert_file": %q, "key_file": %q}]}},
    {"name": "a", "address": "127.0.0.1:0", "tls": {"certificates": [{"cert_file": %q, "key_file": %q}, {"cert_file": %q, "key_file": %q}]}}
  ],
  "upstreams": [
    {"name": "zeta", "addresses": [%q], "max_fails": 1, "fail_timeout": "1h"},
    {"name": "alpha", "addresses": [%q], "max_fails": 1, "fail_timeout": "1h"}
  ],
  "routes": [
    {"listeners": ["web"], "match": {"path": ["/z"]}, "handler": {"module": "proxy", "params": {"upstream": "zeta"}}},
    {"listeners": ["web"], "handler": {"module": "proxy", "params": {"upstream": "alpha"}}}
  ]
}`, ipCert.CertFile, ipCert.KeyFile, dnsCert.CertFile, dnsCert.KeyFile, cnCert.CertFile, cnCert.KeyFile, dead, dead),
		&config.Admin{}, runtime.Env{Engine: engine, Metrics: observe.NewMetrics(), Tracer: tracer, AccessLog: accessLog})
	if resp, err := f.client.Get("http://" + f.server.Addrs()[0].Addr.String() + "/z"); err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}

	resp, body := f.get(t, "/status")
	if resp.StatusCode != 200 {
		t.Fatalf("GET /status: %d %s", resp.StatusCode, body)
	}
	var st Status
	if err := json.Unmarshal([]byte(body), &st); err != nil {
		t.Fatal(err)
	}
	if len(st.Upstreams) != 2 || st.Upstreams[0].Name != "alpha" || st.Upstreams[1].Name != "zeta" {
		t.Errorf("upstreams: %+v", st.Upstreams)
	}
	var certs []string
	for _, c := range st.Certs {
		certs = append(certs, c.Listener+":"+strings.Join(c.Names, ","))
	}
	if got := strings.Join(certs, " "); got != "a:b.example a:only-cn z:127.0.0.1" && got != "a:only-cn a:b.example z:127.0.0.1" {
		t.Errorf("certificates: %s", got)
	}

	got := f.waitMetrics(t, map[string]float64{
		`fit_upstream_backend_up{upstream="zeta",backend="` + dead + `"}`: 0,
		`fit_trace_spans_total{result="exported"}`:                        0,
		`fit_access_log_dropped_total`:                                    0,
	})
	if _, ok := got[`fit_tls_certificate_not_after_seconds{listener="a",name="only-cn",serial="`+cnCert.Leaf.SerialNumber.Text(16)+`"}`]; !ok {
		t.Errorf("the certificate named by its common name is not scraped")
	}
}
