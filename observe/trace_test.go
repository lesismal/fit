package observe

import (
	"encoding/json"
	"fmt"
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
)

// receiver is an OTLP/HTTP receiver that keeps what it is sent.
type receiver struct {
	srv    *httptest.Server
	mu     sync.Mutex
	bodies map[string][][]byte // by path
	header stdhttp.Header      // of the last request
	status int                 // what it answers; 200 when 0
}

func newReceiver(t *testing.T) *receiver {
	r := &receiver{bodies: map[string][][]byte{}}
	r.srv = httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.bodies[req.URL.Path] = append(r.bodies[req.URL.Path], body)
		r.header = req.Header.Clone()
		status := r.status
		r.mu.Unlock()
		if req.Header.Get("Content-Type") != "application/json" {
			stdhttp.Error(w, "not JSON", stdhttp.StatusUnsupportedMediaType)
			return
		}
		if status != 0 {
			stdhttp.Error(w, "refused", status)
			return
		}
		io.WriteString(w, "{}")
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *receiver) got(path string) [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]byte(nil), r.bodies[path]...)
}

func TestParseTraceparent(t *testing.T) {
	const valid = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	trace, parent, sampled, ok := parseTraceparent(valid)
	if !ok || !sampled || fmt.Sprintf("%x", trace) != "4bf92f3577b34da6a3ce929d0e0e4736" || fmt.Sprintf("%x", parent) != "00f067aa0ba902b7" {
		t.Fatalf("%x %x %v %v", trace, parent, sampled, ok)
	}
	if _, _, sampled, ok := parseTraceparent(strings.TrimSuffix(valid, "01") + "00"); !ok || sampled {
		t.Errorf("not sampled: %v %v", sampled, ok)
	}
	// Flags other than sampled are passed over.
	if _, _, sampled, ok := parseTraceparent(strings.TrimSuffix(valid, "01") + "03"); !ok || !sampled {
		t.Errorf("flags 03: %v %v", sampled, ok)
	}
	// A later version is read as far as version 00 goes.
	if _, _, _, ok := parseTraceparent("01" + valid[2:] + "-what-comes-next"); !ok {
		t.Error("a later version")
	}
	for _, bad := range []string{
		"", valid[:54], valid + "-x", "00" + valid[2:] + "0",
		strings.ToUpper(valid),
		"ff" + valid[2:],
		"00-00000000000000000000000000000000-00f067aa0ba902b7-01",
		"00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01",
		"00_4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"00-4bf92f3577b34da6a3ce929d0e0e473g-00f067aa0ba902b7-01",
		"01" + valid[2:] + "x",
	} {
		if _, _, _, ok := parseTraceparent(bad); ok {
			t.Errorf("%q was taken", bad)
		}
	}
	tc := Trace{TraceID: trace, SpanID: [8]byte{1, 2, 3, 4, 5, 6, 7, 8}}
	if got := tc.Traceparent(); got != "00-4bf92f3577b34da6a3ce929d0e0e4736-0102030405060708-01" {
		t.Errorf("Traceparent: %q", got)
	}
}

func tracer(t testing.TB, endpoint string, ratio float64, maxBytes int64) *Tracer {
	t.Helper()
	tr, err := NewTracer(&config.Telemetry{Endpoint: endpoint, Traces: &config.TracesExport{SampleRatio: ratio, MaxQueueBytes: maxBytes, Interval: config.Duration(time.Hour)}}, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tr.Close() })
	return tr
}

func request(header ...string) *stdhttp.Request {
	r := &stdhttp.Request{Method: "GET", URL: &url.URL{Path: "/api/x"}, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: stdhttp.Header{}, Host: "example.com", RemoteAddr: "192.0.2.1:4321"}
	for i := 0; i+1 < len(header); i += 2 {
		r.Header[header[i]] = []string{header[i+1]}
	}
	return r
}

func TestSample(t *testing.T) {
	const parent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	none := tracer(t, "http://127.0.0.1:1", 0, 0)
	if _, ok := none.Sample(request()); ok {
		t.Error("ratio 0 sampled a request without a caller")
	}
	tc, ok := none.Sample(request("Traceparent", parent))
	if !ok || fmt.Sprintf("%x", tc.TraceID) != "4bf92f3577b34da6a3ce929d0e0e4736" || fmt.Sprintf("%x", tc.Parent) != "00f067aa0ba902b7" || tc.SpanID == [8]byte{} {
		t.Errorf("a sampled caller's request: %+v %v", tc, ok)
	}
	if _, ok := none.Sample(request("Traceparent", strings.TrimSuffix(parent, "1")+"0")); ok {
		t.Error("a caller that did not sample was not followed")
	}

	all := tracer(t, "http://127.0.0.1:1", 1, 0)
	if _, ok := all.Sample(request("Traceparent", strings.TrimSuffix(parent, "1")+"0")); ok {
		t.Error("ratio 1 sampled a request its caller did not")
	}
	a, ok1 := all.Sample(request())
	b, ok2 := all.Sample(request("Traceparent", "garbage"))
	if !ok1 || !ok2 || a.TraceID == b.TraceID || a.TraceID == [16]byte{} || a.Parent != [8]byte{} {
		t.Errorf("new traces: %+v %+v", a, b)
	}

	half := tracer(t, "http://127.0.0.1:1", 0.5, 0)
	n := 0
	for range 20000 {
		if _, ok := half.Sample(request()); ok {
			n++
		}
	}
	if n < 9000 || n > 11000 {
		t.Errorf("ratio 0.5 sampled %d of 20000", n)
	}
}

// span is a span as OTLP JSON has it, the parts the tests read.
type span struct {
	TraceID    string `json:"traceId"`
	SpanID     string `json:"spanId"`
	Parent     string `json:"parentSpanId"`
	Name       string `json:"name"`
	Kind       int    `json:"kind"`
	Start      string `json:"startTimeUnixNano"`
	End        string `json:"endTimeUnixNano"`
	Attributes []struct {
		Key   string         `json:"key"`
		Value map[string]any `json:"value"`
	} `json:"attributes"`
	Status *struct {
		Code int `json:"code"`
	} `json:"status"`
}

func (s *span) attr(key string) any {
	for _, a := range s.Attributes {
		if a.Key == key {
			for _, v := range a.Value {
				return v
			}
		}
	}
	return nil
}

func decodeSpans(t *testing.T, bodies [][]byte) (resource map[string]any, spans []span) {
	t.Helper()
	for _, body := range bodies {
		var req struct {
			ResourceSpans []struct {
				Resource   map[string]any `json:"resource"`
				ScopeSpans []struct {
					Scope map[string]any `json:"scope"`
					Spans []span         `json:"spans"`
				} `json:"scopeSpans"`
			} `json:"resourceSpans"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("not OTLP JSON: %v\n%s", err, body)
		}
		for _, rs := range req.ResourceSpans {
			resource = rs.Resource
			for _, ss := range rs.ScopeSpans {
				spans = append(spans, ss.Spans...)
			}
		}
	}
	return resource, spans
}

func TestTracerExport(t *testing.T) {
	rcv := newReceiver(t)
	headers := filepath.Join(t.TempDir(), "headers")
	if err := os.WriteFile(headers, []byte("# a key\nX-Api-Key: s3cret\n\nX-Tenant:  fit \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tr, err := NewTracer(&config.Telemetry{Endpoint: rcv.srv.URL, HeadersFile: headers, ServiceName: "edge",
		Traces: &config.TracesExport{SampleRatio: 1, Interval: config.Duration(time.Hour)}}, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	const parent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	r := request("Traceparent", parent)
	tc, _ := tr.Sample(r)
	e := &Entry{Start: time.Now().Add(-time.Millisecond), Listener: "web", Route: `api "v2"`, Upstream: "10.0.0.1:80", more: &entryMore{trace: tc, traced: true}}
	tr.End(e, r, 502, 12)
	r2 := request()
	r2.Method, r2.ProtoMajor, r2.URL.Path = "POST", 2, "/\xff"
	tc2, _ := tr.Sample(r2)
	tr.End(&Entry{Start: time.Now(), Listener: "web", more: &entryMore{trace: tc2, traced: true}}, r2, 200, 0)
	tr.Close()

	if tr.Exported() != 2 || tr.Failed() != 0 || tr.Dropped() != 0 {
		t.Errorf("exported %d, failed %d, dropped %d", tr.Exported(), tr.Failed(), tr.Dropped())
	}
	bodies := rcv.got("/v1/traces")
	if len(bodies) != 1 {
		t.Fatalf("%d exports", len(bodies))
	}
	if rcv.header.Get("X-Api-Key") != "s3cret" || rcv.header.Get("X-Tenant") != "fit" {
		t.Errorf("headers: %v", rcv.header)
	}
	resource, spans := decodeSpans(t, bodies)
	if !strings.Contains(fmt.Sprint(resource), "service.name") || !strings.Contains(fmt.Sprint(resource), "edge") {
		t.Errorf("resource: %v", resource)
	}
	if len(spans) != 2 {
		t.Fatalf("%d spans", len(spans))
	}
	// Spans are exported shard by shard, not in the order they ended.
	if spans[0].Name == "POST" {
		spans[0], spans[1] = spans[1], spans[0]
	}
	s := spans[0]
	if s.Name != "GET api \"v2\"" || s.Kind != 2 || s.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" || s.Parent != "00f067aa0ba902b7" ||
		s.SpanID != fmt.Sprintf("%x", tc.SpanID) || s.Start >= s.End || s.Status == nil || s.Status.Code != 2 {
		t.Errorf("span: %+v", s)
	}
	for k, v := range map[string]any{
		"http.request.method": "GET", "url.path": "/api/x", "url.scheme": "http", "server.address": "example.com",
		"client.address": "192.0.2.1", "network.protocol.version": "1.1", "http.response.status_code": "502",
		"http.response.body.size": "12", "fit.listener": "web", "fit.route": `api "v2"`, "fit.upstream.address": "10.0.0.1:80",
	} {
		if got := s.attr(k); got != v {
			t.Errorf("%s: %v", k, got)
		}
	}
	s = spans[1]
	if s.Name != "POST" || s.Parent != "" || s.Status != nil || s.attr("network.protocol.version") != "2" || s.attr("url.path") != "/�" || s.attr("fit.route") != nil {
		t.Errorf("the second span: %+v", s)
	}
}

func TestTracerBounds(t *testing.T) {
	rcv := newReceiver(t)
	rcv.status = 503
	var mu sync.Mutex
	var logs []string
	tr, err := NewTracer(&config.Telemetry{Endpoint: rcv.srv.URL, Traces: &config.TracesExport{SampleRatio: 1, MaxQueueBytes: 2000, Interval: config.Duration(time.Hour)}},
		func(format string, args ...any) {
			mu.Lock()
			logs = append(logs, fmt.Sprintf(format, args...))
			mu.Unlock()
		})
	if err != nil {
		t.Fatal(err)
	}
	// About 600 bytes a span: three fit, the rest are dropped.
	for range 10 {
		r := request()
		tc, _ := tr.Sample(r)
		tr.End(&Entry{Start: time.Now(), Listener: "web", more: &entryMore{trace: tc, traced: true}}, r, 200, 1)
	}
	if d := tr.Dropped(); d < 5 || d > 8 {
		t.Errorf("%d dropped", d)
	}
	tr.export()
	tr.export() // nothing waits: no request
	if n := len(rcv.got("/v1/traces")); n != 1 {
		t.Errorf("%d exports", n)
	}
	if tr.Failed() != 10-tr.Dropped() || tr.Exported() != 0 {
		t.Errorf("failed %d, exported %d", tr.Failed(), tr.Exported())
	}
	// Room again once exported, and a failure that lasts is reported once.
	for range 2 {
		r := request()
		tc, _ := tr.Sample(r)
		tr.End(&Entry{Start: time.Now(), Listener: "web", more: &entryMore{trace: tc, traced: true}}, r, 200, 1)
		tr.export()
	}
	rcv.mu.Lock()
	rcv.status = 0
	rcv.mu.Unlock()
	r := request()
	tc, _ := tr.Sample(r)
	tr.End(&Entry{Start: time.Now(), Listener: "web", more: &entryMore{trace: tc, traced: true}}, r, 200, 1)
	tr.Close()
	if tr.Exported() != 1 {
		t.Errorf("exported %d", tr.Exported())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(logs) != 2 || !strings.Contains(logs[0], "503") || !strings.Contains(logs[1], "works again") {
		t.Errorf("logs: %q", logs)
	}
}

func TestMetricsExporter(t *testing.T) {
	rcv := newReceiver(t)
	var h Histogram
	h.Observe(0, 3*time.Millisecond)
	h.Observe(0, 30*time.Millisecond)
	h.Observe(0, 20*time.Second)
	m, err := NewMetricsExporter(&config.Telemetry{Endpoint: rcv.srv.URL + "/", Metrics: &config.MetricsExport{Interval: config.Duration(time.Hour)}},
		func(w MetricWriter) {
			w.Family("fit_http_requests_total", "counter", "Requests.")
			w.Sample("fit_http_requests_total", 7, "route", "api", "code", "2xx")
			w.Family("fit_connections_open", "gauge", "Connections.")
			w.Sample("fit_connections_open", 3)
			w.Family("fit_http_request_duration_seconds", "histogram", "Durations.")
			w.Histogram("fit_http_request_duration_seconds", h.Snapshot(), "route", "api")
		}, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	m.Close()
	bodies := rcv.got("/v1/metrics")
	if len(bodies) != 1 || m.Exported() != 1 {
		t.Fatalf("%d exports, %d counted", len(bodies), m.Exported())
	}
	var req struct {
		ResourceMetrics []struct {
			ScopeMetrics []struct {
				Metrics []map[string]any `json:"metrics"`
			} `json:"scopeMetrics"`
		} `json:"resourceMetrics"`
	}
	if err := json.Unmarshal(bodies[0], &req); err != nil {
		t.Fatal(err)
	}
	ms := req.ResourceMetrics[0].ScopeMetrics[0].Metrics
	if len(ms) != 3 {
		t.Fatalf("metrics: %v", ms)
	}
	sum := ms[0]["sum"].(map[string]any)
	point := sum["dataPoints"].([]any)[0].(map[string]any)
	if ms[0]["name"] != "fit_http_requests_total" || sum["isMonotonic"] != true || sum["aggregationTemporality"] != float64(2) ||
		point["asDouble"] != float64(7) || point["startTimeUnixNano"] == nil || len(point["attributes"].([]any)) != 2 {
		t.Errorf("the counter: %v", ms[0])
	}
	if g := ms[1]["gauge"].(map[string]any)["dataPoints"].([]any)[0].(map[string]any); g["asDouble"] != float64(3) {
		t.Errorf("the gauge: %v", ms[1])
	}
	hp := ms[2]["histogram"].(map[string]any)["dataPoints"].([]any)[0].(map[string]any)
	buckets := fmt.Sprint(hp["bucketCounts"])
	if ms[2]["unit"] != "s" || hp["count"] != "3" || buckets != "[1 0 0 1 0 0 0 0 0 0 0 1]" || len(hp["explicitBounds"].([]any)) != len(DurationBuckets) {
		t.Errorf("the histogram: %v", hp)
	}
}

// BenchmarkSample is what deciding costs a request: with no tracer, a
// tracer that samples nothing (its caller did not, or it had none) and one
// that samples every request.
func BenchmarkSample(b *testing.B) {
	for _, bb := range []struct {
		name   string
		ratio  float64
		header []string
	}{
		{"off", 0, nil},
		{"off-parent-unsampled", 0, []string{"Traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00"}},
		{"on", 1, nil},
		{"on-parent", 0, []string{"Traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"}},
	} {
		b.Run(bb.name, func(b *testing.B) {
			tr := tracer(b, "http://127.0.0.1:1", bb.ratio, 0)
			r := request(bb.header...)
			b.ReportAllocs()
			for b.Loop() {
				tr.Sample(r)
			}
		})
	}
}

// BenchmarkEnd records a span, without exporting it.
func BenchmarkEnd(b *testing.B) {
	tr := tracer(b, "http://127.0.0.1:1", 1, 1<<40)
	r := request()
	tc, _ := tr.Sample(r)
	e := &Entry{Start: time.Now(), Listener: "web", Route: "api", Upstream: "10.0.0.1:80", more: &entryMore{trace: tc, traced: true}}
	b.ReportAllocs()
	for b.Loop() {
		tr.End(e, r, 200, 512)
		// Drained as an export drains them, the buffers kept.
		if tr.queued.Load() >= spanFlushBytes {
			for i := range tr.shards {
				sh := &tr.shards[i]
				sh.mu.Lock()
				tr.queued.Add(-int64(len(sh.buf)))
				sh.buf, sh.spans = sh.buf[:0], 0
				sh.mu.Unlock()
			}
		}
	}
	b.ReportMetric(float64(len(appendSpan(nil, e, r, 200, 512, time.Now()))), "B/span")
}
