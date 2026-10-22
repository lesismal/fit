//go:build !windows

package e2e

import (
	"fmt"
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// collectorImage is the OpenTelemetry Collector the telemetry is sent to.
const collectorImage = "otel/opentelemetry-collector-contrib:0.137.0"

// collectorConfig receives OTLP over HTTP and writes what it receives to a
// file, as OTLP JSON.
const collectorConfig = `receivers:
  otlp:
    protocols:
      http:
        endpoint: 0.0.0.0:4318
exporters:
  file:
    path: /out/otel.json
    flush_interval: 100ms
service:
  pipelines:
    traces:
      receivers: [otlp]
      exporters: [file]
    metrics:
      receivers: [otlp]
      exporters: [file]
`

// TestTelemetry runs fit with traces and metrics sent to an OpenTelemetry
// Collector. A request whose caller sampled it reaches the upstream with a
// traceparent naming fit's span in the caller's trace, and the collector
// gets that span, under the caller's; a request with no traceparent is not
// traced at sample ratio 0, and the upstream gets none. The collector gets
// fit's metrics too.
func TestTelemetry(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	if err := os.Mkdir(out, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(out, 0o777); err != nil { // the collector's user is not the test's
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte(collectorConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	collector := startServerWith(t, collectorImage, 4318, []string{"-v", out + ":/out", "-v", cfg + ":/etc/otelcol-contrib/config.yaml:ro"})
	endpoint := "http://" + collector
	waitServer(t, "the collector", func() bool {
		resp, err := stdhttp.Post(endpoint+"/v1/traces", "application/json", strings.NewReader(`{}`))
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == 200
	})

	var mu sync.Mutex
	var seen []string // the traceparent each request reached the backend with
	backend := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Traceparent"))
		mu.Unlock()
		io.WriteString(w, "from the backend")
	}))
	defer backend.Close()

	f, addrs := startFit(t, fmt.Sprintf(`{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "upstreams": [{"name": "back", "addresses": [%q]}],
  "routes": [{"name": "api", "handler": {"module": "proxy", "params": {"upstream": "back"}}}],
  "telemetry": {"endpoint": %q, "service_name": "fit-e2e", "traces": {"interval": "200ms"}, "metrics": {"interval": "500ms"}}
}`, strings.TrimPrefix(backend.URL, "http://"), endpoint))
	go func() {
		for range f.lines {
		}
	}()

	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	for _, tp := range []string{"00-" + traceID + "-00f067aa0ba902b7-01", ""} {
		req, _ := stdhttp.NewRequest("GET", "http://"+addrs["web"]+"/x", nil)
		if tp != "" {
			req.Header.Set("Traceparent", tp)
		}
		resp, err := client(false).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(body) != "from the backend" {
			t.Fatalf("%d %q", resp.StatusCode, body)
		}
	}
	mu.Lock()
	got := append([]string(nil), seen...)
	mu.Unlock()
	if len(got) != 2 || len(got[0]) != 55 || got[0][3:35] != traceID || got[0][36:52] == "00f067aa0ba902b7" || !strings.HasSuffix(got[0], "-01") || got[1] != "" {
		t.Fatalf("the backend got traceparents %q", got)
	}
	span := got[0][36:52]

	// The collector writes what it receives, a JSON object a line.
	deadline := time.Now().Add(30 * time.Second)
	for {
		data, _ := os.ReadFile(filepath.Join(out, "otel.json"))
		text := string(data)
		haveSpan := strings.Contains(text, `"traceId":"`+traceID+`","spanId":"`+span+`","parentSpanId":"00f067aa0ba902b7"`) &&
			strings.Contains(text, `"name":"GET api"`)
		haveMetrics := strings.Contains(text, `"name":"fit_http_requests_total"`) && strings.Contains(text, `"fit_upstream_backend_up"`)
		haveResource := strings.Contains(text, `"stringValue":"fit-e2e"`)
		if haveSpan && haveMetrics && haveResource {
			if n := strings.Count(text, `"kind":2`); n != 1 {
				t.Errorf("%d server spans; the request without a traceparent was traced", n)
			}
			return
		}
		if time.Now().After(deadline) {
			logs, _ := exec.Command("docker", "ps", "-a").CombinedOutput()
			t.Fatalf("span %v, metrics %v, resource %v; the collector wrote:\n%.3000s\nfit's stderr:\n%s\n%s", haveSpan, haveMetrics, haveResource, text, f.stderr.String(), logs)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
