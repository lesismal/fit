package config

import (
	"net/http"
	"strings"
	"time"
)

// Telemetry is where fit sends its traces and metrics, over OTLP/HTTP, and
// which requests it traces.
type Telemetry struct {
	Endpoint    string         `json:"endpoint,omitempty" default:"\"http://127.0.0.1:4318\"" doc:"The OTLP/HTTP receiver: http:// or https:// and a host and port, as of an OpenTelemetry Collector; traces go to its /v1/traces and metrics to /v1/metrics, as JSON. http://127.0.0.1:4318 when left out."`
	HeadersFile string         `json:"headers_file,omitempty" doc:"A file of header fields sent with each export, one \"Name: value\" a line, as an API key a receiver asks for; read when fit starts. None when left out."`
	CAFile      string         `json:"ca_file,omitempty" doc:"A PEM file with the CAs the receiver's certificate is verified with, over https; the system's when left out."`
	CertFile    string         `json:"cert_file,omitempty" doc:"A PEM file with the client certificate sent over https; with key_file."`
	KeyFile     string         `json:"key_file,omitempty" doc:"A PEM file with cert_file's private key."`
	ServiceName string         `json:"service_name,omitempty" default:"\"fit\"" doc:"The service.name of the resource traces and metrics are about. fit when left out."`
	Timeout     Duration       `json:"timeout,omitempty" default:"\"10s\"" doc:"How long an export may take, connecting included; a batch not sent by then is dropped and counted. 10s when left out."`
	Traces      *TracesExport  `json:"traces,omitempty" doc:"Trace requests: a server span for each one sampled, and its context sent on to upstreams in traceparent. No traces when left out."`
	Metrics     *MetricsExport `json:"metrics,omitempty" doc:"Export the metrics the Admin API's /metrics serves, requests counted included, every interval. No metrics over OTLP when left out."`

	At Value `json:"-" fit:"self"`
}

// TracesExport says which requests are traced, and how spans wait to be
// exported.
type TracesExport struct {
	SampleRatio   float64  `json:"sample_ratio,omitempty" min:"0" max:"1" doc:"The share of requests traced among those whose caller did not decide: from 0 to 1. A request whose traceparent says its caller sampled it is always traced, and one that says it did not is never. 0, only those a caller sampled, when left out."`
	MaxQueueBytes int64    `json:"max_queue_bytes,omitempty" min:"0" default:"4194304" doc:"The most bytes of spans waiting to be exported, besides the batch being sent; spans past it are dropped and counted. 4 MiB when left out or 0."`
	Interval      Duration `json:"interval,omitempty" default:"\"5s\"" doc:"How often spans are exported, and sooner when 256 KiB of them wait. 5s when left out."`

	At Value `json:"-" fit:"self"`
}

// MetricsExport says how often metrics are exported.
type MetricsExport struct {
	Interval Duration `json:"interval,omitempty" default:"\"30s\"" doc:"How often the metrics are exported. 30s when left out."`

	At Value `json:"-" fit:"self"`
}

// The defaults of Telemetry.
const (
	DefaultTelemetryEndpoint    = "http://127.0.0.1:4318"
	DefaultTelemetryServiceName = "fit"
	DefaultTracesMaxQueueBytes  = 4 << 20
)

// The defaults of Telemetry's durations.
var (
	DefaultTelemetryTimeout = Duration(10 * time.Second)
	DefaultTracesInterval   = Duration(5 * time.Second)
	DefaultMetricsInterval  = Duration(30 * time.Second)
)

// URL, Service and ExportTimeout return t's settings, their defaults when
// left out.
func (t *Telemetry) URL() string {
	if t.Endpoint == "" {
		return DefaultTelemetryEndpoint
	}
	return strings.TrimSuffix(t.Endpoint, "/")
}

func (t *Telemetry) Service() string {
	if t.ServiceName == "" {
		return DefaultTelemetryServiceName
	}
	return t.ServiceName
}

func (t *Telemetry) ExportTimeout() time.Duration {
	if t.Timeout <= 0 {
		return DefaultTelemetryTimeout.Std()
	}
	return t.Timeout.Std()
}

// QueueBytes and Every return t's settings, their defaults when left out.
func (t *TracesExport) QueueBytes() int64 {
	return orDefault(t.MaxQueueBytes, DefaultTracesMaxQueueBytes)
}

func (t *TracesExport) Every() time.Duration {
	if t.Interval <= 0 {
		return DefaultTracesInterval.Std()
	}
	return t.Interval.Std()
}

// Every returns m's interval, its default when left out.
func (m *MetricsExport) Every() time.Duration {
	if m.Interval <= 0 {
		return DefaultMetricsInterval.Std()
	}
	return m.Interval.Std()
}

func checkTelemetry(t *Telemetry, add func(Value, string, ...any)) {
	checkEndpoint(t.At.Field("endpoint"), t.Endpoint, add)
	checkTLSFiles(t.At, t.CertFile, t.KeyFile, add)
	if t.Traces == nil && t.Metrics == nil {
		add(t.At, "set traces, metrics or both: telemetry exports nothing without them")
	}
}

// ParseHeaders reads a headers_file's contents: one "Name: value" a line,
// blank lines and lines starting with # passed over.
func ParseHeaders(data string) (http.Header, error) {
	h := http.Header{}
	for i, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		name = strings.TrimSpace(name)
		if !ok || name == "" || strings.ContainsAny(name, " \t") {
			return nil, &Error{Line: i + 1, Msg: "not a header field: \"Name: value\""}
		}
		h.Add(name, strings.TrimSpace(value))
	}
	return h, nil
}
