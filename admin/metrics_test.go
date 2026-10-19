package admin

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	_ "github.com/lesismal/fit/handler/proxy"
	"github.com/lesismal/fit/internal/certtest"
	"github.com/lesismal/fit/observe"
	"github.com/lesismal/fit/runtime"
	"github.com/lesismal/fit/upstream"
)

// sampleLine is a sample in Prometheus's text format: a name, labels, and a
// value.
var sampleLine = regexp.MustCompile(`^([a-zA-Z_:][a-zA-Z0-9_:]*)(\{(?:[a-zA-Z_][a-zA-Z0-9_]*="(?:[^"\\]|\\.)*",?)*\})? (\S+)$`)

// parseMetrics checks text as Prometheus's text format, as far as fit
// writes it: each family's HELP and TYPE once, before its samples, which
// follow one another; no sample twice; values that parse. It returns the
// samples by name and labels, as written.
func parseMetrics(t *testing.T, text string) map[string]float64 {
	t.Helper()
	samples := map[string]float64{}
	typed := map[string]string{}
	var family string
	for i, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "# HELP "):
			name := strings.Fields(line)[2]
			if _, ok := typed[name]; ok {
				t.Errorf("line %d: family %s again", i+1, name)
			}
			typed[name], family = "", name
		case strings.HasPrefix(line, "# TYPE "):
			f := strings.Fields(line)
			if len(f) != 4 || f[2] != family {
				t.Errorf("line %d: TYPE without its HELP: %q", i+1, line)
				continue
			}
			typed[family] = f[3]
		default:
			m := sampleLine.FindStringSubmatch(line)
			if m == nil {
				t.Errorf("line %d: not a sample: %q", i+1, line)
				continue
			}
			name := m[1]
			if typed[family] == "histogram" {
				name = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(name, "_bucket"), "_sum"), "_count")
			}
			if name != family {
				t.Errorf("line %d: a sample of %s within family %s", i+1, m[1], family)
			}
			key := m[1] + m[2]
			if _, ok := samples[key]; ok {
				t.Errorf("line %d: %s twice", i+1, key)
			}
			v, err := strconv.ParseFloat(m[3], 64)
			if err != nil {
				t.Errorf("line %d: value %q", i+1, m[3])
			}
			samples[key] = v
		}
	}
	return samples
}

func (f *fixture) scrape(t *testing.T) map[string]float64 {
	t.Helper()
	resp, body := f.get(t, "/metrics")
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != observe.TextContentType {
		t.Fatalf("scrape: %s, %s\n%s", resp.Status, resp.Header.Get("Content-Type"), body)
	}
	return parseMetrics(t, body)
}

// waitMetrics scrapes until every sample in want has its value, which a
// request counts once its response has gone, maybe after the client has it.
func (f *fixture) waitMetrics(t *testing.T, want map[string]float64) map[string]float64 {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := f.scrape(t)
		var missing []string
		for k, v := range want {
			if g, ok := got[k]; !ok || g != v {
				missing = append(missing, fmt.Sprintf("%s = %v, want %v", k, g, v))
			}
		}
		if len(missing) == 0 {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("metrics:\n%s", strings.Join(missing, "\n"))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestMetrics(t *testing.T) {
	backend := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "from the backend")
	})}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = backend.Serve(ln) }()
	t.Cleanup(func() { _ = backend.Close() })
	// An address nothing listens on.
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
	cert, err := certtest.Write(t.TempDir(), "a.example")
	if err != nil {
		t.Fatal(err)
	}
	doc := func(body string) string {
		return fmt.Sprintf(`{
  "admin": {"address": "127.0.0.1:0"},
  "metrics": {},
  "listeners": [
    {"name": "web", "address": "127.0.0.1:0"},
    {"name": "secure", "address": "127.0.0.1:0", "tls": {"certificates": [{"cert_file": %q, "key_file": %q}]}}
  ],
  "upstreams": [{"name": "back", "addresses": [%q, %q], "max_fails": 100}],
  "routes": [
    {"name": "hello", "listeners": ["web"], "match": {"path": ["/hello"]}, "handler": {"module": "respond", "params": {"body": %q}}},
    {"listeners": ["web"], "match": {"path": ["/api/*"]}, "handler": {"module": "proxy", "params": {"upstream": "back"}}}
  ]
}`, cert.CertFile, cert.KeyFile, ln.Addr().String(), dead, body)
	}
	metrics := observe.NewMetrics()
	f := startEnv(t, doc("v1"), &config.Admin{}, runtime.Env{Engine: engine, Metrics: metrics})
	web := "http://" + f.server.Addrs()[0].Addr.String()
	get := func(path string, want int) {
		t.Helper()
		resp, err := f.client.Get(web + path)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("%s: %d, want %d", path, resp.StatusCode, want)
		}
	}
	for range 3 {
		get("/hello", 200)
	}
	get("/nowhere", 404)
	// Round robin: one to the backend, one to the address nothing listens on.
	get("/api/a", 200)
	get("/api/b", 502)

	// A reload that keeps the routes keeps their counts; a broken one fails.
	if resp, body := f.load(t, doc("v2")); resp.StatusCode != 200 {
		t.Fatalf("load: %s %s", resp.Status, body)
	}
	if resp, _ := f.load(t, `{"listeners": [{"name": "web"}]}`); resp.StatusCode != 400 {
		t.Fatalf("broken load: %s", resp.Status)
	}
	get("/hello", 200)

	live := ln.Addr().String()
	got := f.waitMetrics(t, map[string]float64{
		`fit_http_requests_total{listener="web",route="hello",code="2xx"}`:                                                           4,
		`fit_http_requests_total{listener="web",route="",code="4xx"}`:                                                                1,
		`fit_http_requests_total{listener="web",route="#1",code="2xx"}`:                                                              1,
		`fit_http_requests_total{listener="web",route="#1",code="5xx"}`:                                                              1,
		`fit_http_request_duration_seconds_count{listener="web",route="hello"}`:                                                      4,
		`fit_http_request_duration_seconds_bucket{listener="web",route="hello",le="+Inf"}`:                                           4,
		`fit_http_request_duration_seconds_count{listener="secure",route=""}`:                                                        0,
		`fit_upstream_response_duration_seconds_count{upstream="back"}`:                                                              2,
		`fit_upstream_requests_in_flight{upstream="back"}`:                                                                           0,
		`fit_upstream_backend_requests_total{upstream="back",backend="` + live + `",result="success"}`:                               1,
		`fit_upstream_backend_requests_total{upstream="back",backend="` + live + `",result="failure"}`:                               0,
		`fit_upstream_backend_requests_total{upstream="back",backend="` + dead + `",result="failure"}`:                               1,
		`fit_upstream_backend_requests_in_flight{upstream="back",backend="` + live + `"}`:                                            0,
		`fit_upstream_backend_up{upstream="back",backend="` + live + `"}`:                                                            1,
		`fit_tls_certificate_not_after_seconds{listener="secure",name="a.example",serial="` + cert.Leaf.SerialNumber.Text(16) + `"}`: float64(cert.Leaf.NotAfter.Unix()),
		`fit_config_reloads_total{result="success"}`:                                                                                 1,
		`fit_config_reloads_total{result="failure"}`:                                                                                 1,
		`fit_config_version`:                      2,
		`fit_reads_paused_total{reason="budget"}`: 0,
	})
	for _, name := range []string{"fit_connections_open", "fit_connections_refused_total", "fit_pending_bytes",
		"fit_config_last_applied_timestamp_seconds", "go_goroutines", "go_memory_total_bytes"} {
		if _, ok := got[name]; !ok {
			t.Errorf("no %s", name)
		}
	}
	if applied := got["fit_config_last_applied_timestamp_seconds"]; time.Since(time.Unix(int64(applied), 0)) > time.Minute {
		t.Errorf("last applied %v", applied)
	}

	// A route a reload removes is no longer scraped, once the graphs that
	// had it are gone.
	if resp, body := f.load(t, strings.Replace(doc("v3"), `"name": "hello"`, `"name": "greeting"`, 1)); resp.StatusCode != 200 {
		t.Fatalf("load: %s %s", resp.Status, body)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		got = f.scrape(t)
		_, old := got[`fit_http_request_duration_seconds_count{listener="web",route="hello"}`]
		_, renamed := got[`fit_http_request_duration_seconds_count{listener="web",route="greeting"}`]
		if !old && renamed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after renaming the route: hello there %v, greeting there %v", old, renamed)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestMetricsOff checks that a config without metrics counts no requests,
// and that the rest is still scraped.
func TestMetricsOff(t *testing.T) {
	f := start(t, doc("x"), &config.Admin{})
	if f.served(t) != "x" {
		t.Fatal("not served")
	}
	got := f.scrape(t)
	for k := range got {
		if strings.HasPrefix(k, "fit_http_") {
			t.Errorf("%s scraped without metrics", k)
		}
	}
	if _, ok := got["fit_config_version"]; !ok {
		t.Error("no fit_config_version")
	}
}
