package observe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	stdhttp "net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/apiclient"
)

// exporter posts OTLP/HTTP JSON requests to one of a receiver's paths.
type exporter struct {
	url      string
	header   stdhttp.Header
	hc       *stdhttp.Client
	timeout  time.Duration
	resource []byte // the resource the data is about, as OTLP JSON
	logf     func(format string, args ...any)

	mu   sync.Mutex
	last string // the error reported last, so that one that lasts is reported once
}

func newExporter(c *config.Telemetry, path string, logf func(format string, args ...any)) (*exporter, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	e := &exporter{url: c.URL() + path, header: stdhttp.Header{}, timeout: c.ExportTimeout(), logf: logf}
	if c.HeadersFile != "" {
		data, err := os.ReadFile(c.HeadersFile)
		if err != nil {
			return nil, err
		}
		if e.header, err = config.ParseHeaders(string(data)); err != nil {
			return nil, fmt.Errorf("%s: %w", c.HeadersFile, err)
		}
	}
	hc, err := apiclient.Client(apiclient.TLSFiles{CA: c.CAFile, Cert: c.CertFile, Key: c.KeyFile}, e.timeout, 2)
	if err != nil {
		return nil, err
	}
	e.hc = hc
	e.resource, _ = json.Marshal(map[string]any{"attributes": []any{
		attr("service.name", c.Service()),
		attr("service.version", Version),
	}})
	return e, nil
}

// Version is fit's version, written into the resource; cmd/fit sets it.
var Version = "dev"

func attr(key, value string) map[string]any {
	return map[string]any{"key": key, "value": map[string]string{"stringValue": value}}
}

// post sends body, within the export timeout. A receiver's answer other
// than 2xx is an error.
func (e *exporter) post(ctx context.Context, body []byte) error {
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()
	req, err := stdhttp.NewRequestWithContext(ctx, stdhttp.MethodPost, e.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	for k, v := range e.header {
		req.Header[k] = v
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.hc.Do(req)
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return apiclient.StatusError(resp, "POST "+e.url)
	}
	_, _ = apiclient.ReadBody(resp, 64<<10, "POST "+e.url)
	return nil
}

// report logs an export's error when it is not the one reported last, and
// that exports work again after one failed.
func (e *exporter) report(what string, err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if msg == e.last {
		return
	}
	switch {
	case err != nil:
		e.logf("telemetry: exporting %s: %v", what, err)
	case e.last != "":
		e.logf("telemetry: exporting %s works again", what)
	}
	e.last = msg
}

// MetricWriter takes metrics: a family begun with Family, then its samples.
// TextWriter writes them in Prometheus's text format, and OTLPWriter as an
// OTLP export request.
type MetricWriter interface {
	Family(name, typ, help string)
	Sample(name string, value float64, labels ...string)
	Histogram(name string, h HistogramSnapshot, labels ...string)
}

var (
	_ MetricWriter = (*TextWriter)(nil)
	_ MetricWriter = (*OTLPWriter)(nil)
)

// OTLPWriter gathers metrics as OTLP's: a counter as a cumulative,
// monotonic sum, a gauge as a gauge, and a histogram of seconds as an
// explicit-bucket histogram.
type OTLPWriter struct {
	Now, Start time.Time // when the metrics were read, and since when the cumulative ones count
	metrics    []otlpMetric
}

type otlpMetric struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Unit        string         `json:"unit,omitempty"`
	Sum         *otlpSum       `json:"sum,omitempty"`
	Gauge       *otlpGauge     `json:"gauge,omitempty"`
	Histogram   *otlpHistogram `json:"histogram,omitempty"`
}

type otlpSum struct {
	Points      []otlpPoint `json:"dataPoints"`
	Temporality int         `json:"aggregationTemporality"`
	Monotonic   bool        `json:"isMonotonic"`
}

type otlpGauge struct {
	Points []otlpPoint `json:"dataPoints"`
}

type otlpHistogram struct {
	Points      []otlpHistPoint `json:"dataPoints"`
	Temporality int             `json:"aggregationTemporality"`
}

type otlpPoint struct {
	Attributes []any   `json:"attributes,omitempty"`
	Start      string  `json:"startTimeUnixNano,omitempty"`
	Time       string  `json:"timeUnixNano"`
	Value      float64 `json:"asDouble"`
}

type otlpHistPoint struct {
	Attributes []any     `json:"attributes,omitempty"`
	Start      string    `json:"startTimeUnixNano"`
	Time       string    `json:"timeUnixNano"`
	Count      string    `json:"count"`
	Sum        float64   `json:"sum"`
	Buckets    []string  `json:"bucketCounts"`
	Bounds     []float64 `json:"explicitBounds"`
}

// cumulative is OTLP's AGGREGATION_TEMPORALITY_CUMULATIVE.
const cumulative = 2

func nanos(t time.Time) string { return strconv.FormatInt(t.UnixNano(), 10) }

func attrs(labels []string) []any {
	var a []any
	for i := 0; i+1 < len(labels); i += 2 {
		a = append(a, attr(labels[i], labels[i+1]))
	}
	return a
}

func (w *OTLPWriter) Family(name, typ, help string) {
	m := otlpMetric{Name: name, Description: help}
	switch typ {
	case "counter":
		m.Sum = &otlpSum{Points: []otlpPoint{}, Temporality: cumulative, Monotonic: true}
	case "histogram":
		m.Unit = "s"
		m.Histogram = &otlpHistogram{Points: []otlpHistPoint{}, Temporality: cumulative}
	default:
		m.Gauge = &otlpGauge{Points: []otlpPoint{}}
	}
	w.metrics = append(w.metrics, m)
}

func (w *OTLPWriter) Sample(name string, value float64, labels ...string) {
	if len(w.metrics) == 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return
	}
	m := &w.metrics[len(w.metrics)-1]
	p := otlpPoint{Attributes: attrs(labels), Time: nanos(w.Now), Value: value}
	switch {
	case m.Sum != nil:
		p.Start = nanos(w.Start)
		m.Sum.Points = append(m.Sum.Points, p)
	case m.Gauge != nil:
		m.Gauge.Points = append(m.Gauge.Points, p)
	}
}

func (w *OTLPWriter) Histogram(name string, h HistogramSnapshot, labels ...string) {
	if len(w.metrics) == 0 || w.metrics[len(w.metrics)-1].Histogram == nil {
		return
	}
	p := otlpHistPoint{Attributes: attrs(labels), Start: nanos(w.Start), Time: nanos(w.Now),
		Count: strconv.FormatUint(h.Count, 10), Sum: h.Sum, Bounds: DurationBuckets[:]}
	// The snapshot's buckets are cumulative; OTLP's each count their own.
	var prev uint64
	for _, c := range h.Buckets {
		p.Buckets = append(p.Buckets, strconv.FormatUint(c-prev, 10))
		prev = c
	}
	p.Buckets = append(p.Buckets, strconv.FormatUint(h.Count-prev, 10))
	m := &w.metrics[len(w.metrics)-1]
	m.Histogram.Points = append(m.Histogram.Points, p)
}

// MetricsExporter exports metrics over OTLP/HTTP every interval.
type MetricsExporter struct {
	exp      *exporter
	interval time.Duration
	write    func(MetricWriter)
	start    time.Time

	exported atomic.Int64
	failed   atomic.Int64

	stop chan struct{}
	done chan struct{}
	once sync.Once
}

// NewMetricsExporter starts exporting what write writes every interval of
// c's metrics; Close stops it, after a last export.
func NewMetricsExporter(c *config.Telemetry, write func(MetricWriter), logf func(format string, args ...any)) (*MetricsExporter, error) {
	exp, err := newExporter(c, "/v1/metrics", logf)
	if err != nil {
		return nil, err
	}
	m := &MetricsExporter{exp: exp, interval: c.Metrics.Every(), write: write, start: time.Now(),
		stop: make(chan struct{}), done: make(chan struct{})}
	go m.run()
	return m, nil
}

func (m *MetricsExporter) run() {
	defer close(m.done)
	tick := time.NewTicker(m.interval)
	defer tick.Stop()
	for {
		select {
		case <-m.stop:
			m.Export(context.Background())
			return
		case <-tick.C:
			m.Export(context.Background())
		}
	}
}

// Export reads the metrics and sends them at once.
func (m *MetricsExporter) Export(ctx context.Context) error {
	w := &OTLPWriter{Now: time.Now(), Start: m.start}
	m.write(w)
	body, err := json.Marshal(map[string]any{"resourceMetrics": []any{map[string]any{
		"resource":     json.RawMessage(m.exp.resource),
		"scopeMetrics": []any{map[string]any{"scope": map[string]string{"name": "fit"}, "metrics": w.metrics}},
	}}})
	if err == nil {
		err = m.exp.post(ctx, body)
	}
	m.exp.report("metrics", err)
	if err != nil {
		m.failed.Add(1)
		return err
	}
	m.exported.Add(1)
	return nil
}

// Exported and Failed count the exports that succeeded and failed.
func (m *MetricsExporter) Exported() int64 { return m.exported.Load() }
func (m *MetricsExporter) Failed() int64   { return m.failed.Load() }

// Close exports once more and stops.
func (m *MetricsExporter) Close() error {
	m.once.Do(func() { close(m.stop) })
	<-m.done
	return nil
}
