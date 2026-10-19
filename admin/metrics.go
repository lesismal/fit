package admin

import (
	"crypto/tls"
	"net/http"
	"runtime/metrics"

	"github.com/lesismal/fit/observe"
)

// certificateLister is a tlsmgr.Manager that can say which certificates it
// serves, as tlsmgr.Static can.
type certificateLister interface {
	Certificates() []*tls.Certificate
}

// goMetrics are the Go runtime's metrics a scrape reads, and what they are
// written as.
var goMetrics = []struct{ name, metric, help string }{
	{"/sched/goroutines:goroutines", "go_goroutines", "Goroutines that currently exist."},
	{"/memory/classes/total:bytes", "go_memory_total_bytes", "Memory mapped by the Go runtime."},
	{"/memory/classes/heap/objects:bytes", "go_memory_heap_objects_bytes", "Memory taken by live objects and dead ones not yet swept."},
}

// metrics serves the metrics in Prometheus's text format.
func (s *Server) metrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", observe.TextContentType)
	_, _ = w.Write(s.configs.WriteMetrics(nil))
}

// WriteMetrics appends fit's metrics, in Prometheus's text format, to b:
// requests by listener and route; upstreams and their backends; the
// listeners' connections; config reloads; certificates; the access log; and
// a few of the Go runtime's.
func (cs *Configs) WriteMetrics(b []byte) []byte {
	w := &observe.TextWriter{B: b}
	if m := cs.env.Metrics; m != nil {
		all := m.AllRequests()
		w.Family("fit_http_requests_total", "counter", "Requests answered, by listener, route and the class of their status.")
		for _, r := range all {
			for i, n := range r.Codes() {
				if n > 0 {
					w.Sample("fit_http_requests_total", float64(n), "listener", r.Listener, "route", r.Route, "code", observe.StatusClasses[i])
				}
			}
		}
		w.Family("fit_http_request_duration_seconds", "histogram", "How long requests took, from when the handler was called to when the response was handed to the connection.")
		for _, r := range all {
			w.Histogram("fit_http_request_duration_seconds", r.Duration(), "listener", r.Listener, "route", r.Route)
		}
	}

	if g := cs.rt.Current(); g != nil {
		pools := g.Upstreams()
		w.Family("fit_upstream_requests_in_flight", "gauge", "Requests to an upstream that are not over, body included.")
		for _, p := range pools {
			w.Sample("fit_upstream_requests_in_flight", float64(p.InFlight()), "upstream", p.Name())
		}
		w.Family("fit_upstream_response_duration_seconds", "histogram", "How long an upstream took to send a response's header, or to fail without one.")
		for _, p := range pools {
			w.Histogram("fit_upstream_response_duration_seconds", p.ResponseTimes(), "upstream", p.Name())
		}
		w.Family("fit_upstream_backend_requests_total", "counter", "Requests to a backend that are over, by whether the backend failed them.")
		for _, p := range pools {
			for _, b := range p.Backends() {
				done, failed := b.Requests()
				w.Sample("fit_upstream_backend_requests_total", float64(done-failed), "upstream", p.Name(), "backend", b.Address, "result", "success")
				w.Sample("fit_upstream_backend_requests_total", float64(failed), "upstream", p.Name(), "backend", b.Address, "result", "failure")
			}
		}
		w.Family("fit_upstream_backend_requests_in_flight", "gauge", "Requests to a backend that are not over, body included.")
		for _, p := range pools {
			for _, b := range p.Backends() {
				w.Sample("fit_upstream_backend_requests_in_flight", float64(b.Active()), "upstream", p.Name(), "backend", b.Address)
			}
		}
		w.Family("fit_upstream_backend_up", "gauge", "Whether a backend is in rotation (1) or out of it, for failing requests or its health check (0).")
		for _, p := range pools {
			for _, b := range p.Backends() {
				up := 1.0
				if b.Down() {
					up = 0
				}
				w.Sample("fit_upstream_backend_up", up, "upstream", p.Name(), "backend", b.Address)
			}
		}

		w.Family("fit_tls_certificate_not_after_seconds", "gauge", "When a certificate a listener serves expires, in seconds since the Unix epoch.")
		for _, l := range g.Listeners {
			lister, ok := l.Certificates.(certificateLister)
			if !ok {
				continue
			}
			for _, c := range lister.Certificates() {
				if c.Leaf == nil {
					continue
				}
				name := c.Leaf.Subject.CommonName
				if len(c.Leaf.DNSNames) > 0 {
					name = c.Leaf.DNSNames[0]
				}
				w.Sample("fit_tls_certificate_not_after_seconds", float64(c.Leaf.NotAfter.Unix()),
					"listener", l.Name, "name", name, "serial", c.Leaf.SerialNumber.Text(16))
			}
		}
		g.Release()
	}

	st := cs.server.Stats()
	w.Family("fit_connections_open", "gauge", "Client connections open on the listeners.")
	w.Sample("fit_connections_open", float64(st.OpenConns))
	w.Family("fit_connections_refused_total", "counter", "Client connections closed as they were accepted, for limits.max_conns.")
	w.Sample("fit_connections_refused_total", float64(st.ConnsRefused))
	w.Family("fit_pending_bytes", "gauge", "Bytes queued for clients that their connections have not taken, which limits.max_pending_bytes bounds.")
	w.Sample("fit_pending_bytes", float64(st.PendingBytes))
	w.Family("fit_reads_paused_total", "counter", "Times a connection's reads were paused: for its own queued output (watermark), or for limits.max_pending_bytes (budget).")
	w.Sample("fit_reads_paused_total", float64(st.ReadsPausedByWatermark), "reason", "watermark")
	w.Sample("fit_reads_paused_total", float64(st.ReadsPausedByBudget), "reason", "budget")

	cur := cs.Current()
	tried, failed := cs.Reloads()
	w.Family("fit_config_reloads_total", "counter", "Configs applied after the first, by SIGHUP, the file watcher or the Admin API, by result.")
	w.Sample("fit_config_reloads_total", float64(tried-failed), "result", "success")
	w.Sample("fit_config_reloads_total", float64(failed), "result", "failure")
	w.Family("fit_config_version", "gauge", "The version of the running config.")
	w.Sample("fit_config_version", float64(cur.Number))
	w.Family("fit_config_last_applied_timestamp_seconds", "gauge", "When the running config was applied, in seconds since the Unix epoch.")
	w.Sample("fit_config_last_applied_timestamp_seconds", float64(cur.Applied.UnixMilli())/1e3)

	if log := cs.env.AccessLog; log != nil {
		w.Family("fit_access_log_dropped_total", "counter", "Access log lines dropped because the log's buffer was full.")
		w.Sample("fit_access_log_dropped_total", float64(log.Dropped()))
	}

	samples := make([]metrics.Sample, len(goMetrics))
	for i, m := range goMetrics {
		samples[i].Name = m.name
	}
	metrics.Read(samples)
	for i, m := range goMetrics {
		if samples[i].Value.Kind() != metrics.KindUint64 {
			continue
		}
		w.Family(m.metric, "gauge", m.help)
		w.Sample(m.metric, float64(samples[i].Value.Uint64()))
	}
	return w.B
}
