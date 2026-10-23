package observe

import (
	"context"
	"crypto/tls"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
)

// TestAccessLogStdOutputs opens access logs to the standard outputs.
func TestAccessLogStdOutputs(t *testing.T) {
	for output, want := range map[string]*os.File{"": os.Stdout, "stdout": os.Stdout, "stderr": os.Stderr} {
		l, err := NewAccessLog(&config.AccessLog{Output: output})
		if err != nil {
			t.Fatal(err)
		}
		if l.out != want || l.file != nil {
			t.Errorf("output %q: %v", output, l.out)
		}
		l.Close()
	}
}

func TestClaimsWithoutMore(t *testing.T) {
	if c := new(Entry).Claims(); c != nil {
		t.Fatalf("claims %v", c)
	}
}

func TestAppendFloatSpecial(t *testing.T) {
	for v, want := range map[float64]string{math.Inf(1): "+Inf", math.Inf(-1): "-Inf", math.NaN(): "NaN"} {
		if got := string(appendFloat(nil, v)); got != want {
			t.Errorf("%v: %q", v, got)
		}
	}
}

// TestExporterErrors makes exporters of telemetry they cannot use: a headers
// file that is missing or holds no headers, and a CA file that is missing.
func TestExporterErrors(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad")
	if err := os.WriteFile(bad, []byte("not a header\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []*config.Telemetry{
		{HeadersFile: filepath.Join(dir, "missing")},
		{HeadersFile: bad},
		{CAFile: filepath.Join(dir, "missing")},
	} {
		c.Traces, c.Metrics = &config.TracesExport{}, &config.MetricsExport{}
		if _, err := NewTracer(c, nil); err == nil {
			t.Errorf("NewTracer %+v succeeded", c)
		}
		if _, err := NewMetricsExporter(c, func(MetricWriter) {}, nil); err == nil {
			t.Errorf("NewMetricsExporter %+v succeeded", c)
		}
	}
	if !strings.Contains(func() string {
		_, err := NewTracer(&config.Telemetry{HeadersFile: bad, Traces: &config.TracesExport{}}, nil)
		return err.Error()
	}(), bad) {
		t.Error("the error does not name the headers file")
	}
}

// TestExportFailures exports to an endpoint no request can be made for and
// to one nothing listens on, with no logf.
func TestExportFailures(t *testing.T) {
	for _, endpoint := range []string{"http://127.0.0.1:1", "http://bad\x7fhost"} {
		m, err := NewMetricsExporter(&config.Telemetry{Endpoint: endpoint, Metrics: &config.MetricsExport{Interval: config.Duration(time.Hour)}},
			func(w MetricWriter) {
				w.Family("fit_up", "gauge", "Up.")
				w.Sample("fit_up", 1)
			}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := m.Export(context.Background()); err == nil {
			t.Errorf("%q: the export succeeded", endpoint)
		}
		m.Close()
		if m.Failed() != 2 || m.Exported() != 0 {
			t.Errorf("%q: failed %d, exported %d", endpoint, m.Failed(), m.Exported())
		}
	}
}

// TestOTLPWriterIgnores drops samples with no family before them, or whose
// value OTLP JSON cannot carry, and histograms of a family that is not one.
func TestOTLPWriterIgnores(t *testing.T) {
	w := &OTLPWriter{Now: time.Now(), Start: time.Now()}
	w.Sample("orphan", 1)
	w.Histogram("orphan", HistogramSnapshot{})
	w.Family("fit_g", "gauge", "G.")
	w.Sample("fit_g", math.NaN())
	w.Sample("fit_g", math.Inf(1))
	w.Histogram("fit_g", HistogramSnapshot{})
	if len(w.metrics) != 1 || len(w.metrics[0].Gauge.Points) != 0 {
		t.Fatalf("metrics %+v", w.metrics)
	}
}

// TestMetricsExporterTicks exports every interval.
func TestMetricsExporterTicks(t *testing.T) {
	rcv := newReceiver(t)
	m, err := NewMetricsExporter(&config.Telemetry{Endpoint: rcv.srv.URL, Metrics: &config.MetricsExport{Interval: config.Duration(10 * time.Millisecond)}},
		func(w MetricWriter) {
			w.Family("fit_up", "gauge", "Up.")
			w.Sample("fit_up", 1)
		}, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	waitFor(t, func() bool { return len(rcv.got("/v1/metrics")) >= 2 })
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestTracerTicks exports spans every interval.
func TestTracerTicks(t *testing.T) {
	rcv := newReceiver(t)
	tr, err := NewTracer(&config.Telemetry{Endpoint: rcv.srv.URL, Traces: &config.TracesExport{SampleRatio: 1, Interval: config.Duration(10 * time.Millisecond)}}, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	r := request()
	tc, _ := tr.Sample(r)
	tr.End(&Entry{Start: time.Now(), Listener: "web", more: &entryMore{trace: tc, traced: true}}, r, 200, 1)
	waitFor(t, func() bool { return tr.Exported() == 1 })
}

// TestTracerFlushesWhenFull exports, before its interval, once enough spans
// wait.
func TestTracerFlushesWhenFull(t *testing.T) {
	rcv := newReceiver(t)
	tr := tracer(t, rcv.srv.URL, 1, 0)
	r := request()
	n := 0
	for tr.queued.Load() < spanFlushBytes && n < 10000 {
		tc, _ := tr.Sample(r)
		tr.End(&Entry{Start: time.Now(), Listener: "web", more: &entryMore{trace: tc, traced: true}}, r, 200, 1)
		n++
	}
	waitFor(t, func() bool { return len(rcv.got("/v1/traces")) >= 1 })
}

// TestSpanProtocols writes the scheme and protocol version of HTTPS,
// HTTP/3 and HTTP/1.0 requests.
func TestSpanProtocols(t *testing.T) {
	for _, tc := range []struct {
		major, minor int
		tls          bool
		want         []string
	}{
		{1, 0, false, []string{`"url.scheme","value":{"stringValue":"http"}`, `"network.protocol.version","value":{"stringValue":"1.0"}`}},
		{3, 0, true, []string{`"url.scheme","value":{"stringValue":"https"}`, `"network.protocol.version","value":{"stringValue":"3"}`}},
	} {
		r := request()
		r.ProtoMajor, r.ProtoMinor = tc.major, tc.minor
		if tc.tls {
			r.TLS = &tls.ConnectionState{}
		}
		got := string(appendSpan(nil, &Entry{Start: time.Now(), Listener: "web", more: &entryMore{traced: true}}, r, 200, 0, time.Now()))
		for _, w := range tc.want {
			if !strings.Contains(got, w) {
				t.Errorf("HTTP/%d.%d: %s lacks %s", tc.major, tc.minor, got, w)
			}
		}
	}
}

func TestSampledFlagShort(t *testing.T) {
	if sampledFlag("00-4bf92f3577b34da6a3ce929d0e0e4736") {
		t.Fatal("a short traceparent sampled")
	}
}
