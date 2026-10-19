package observe

import (
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Metrics are counted on the request path with atomic adds alone, spread
// over a few shards so that cores serving the same route do not take turns
// on one cache line, and summed only when scraped. The caller picks the
// shard with a hint that stays with a core for a while, such as the address
// of a request's Context, which fib keeps in pools that favour the core that
// gave it back: a shard picked at random would have every core touch every
// shard, and gain nothing.

// DurationBuckets are the upper bounds, in seconds, of fit's duration
// histograms.
var DurationBuckets = [...]float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}

// bucketNanos are DurationBuckets in nanoseconds.
var bucketNanos = func() (b [len(DurationBuckets)]int64) {
	for i, s := range DurationBuckets {
		b[i] = int64(s * 1e9)
	}
	return b
}()

// metricShards is how many shards a metric is spread over.
const (
	metricShardBits = 3
	metricShards    = 1 << metricShardBits
)

// pickShard picks a shard by hint: the top bits of its product with a large
// odd constant, so that hints a fixed stride apart, as pooled objects are,
// spread over the shards.
func pickShard(hint uintptr) int {
	return int((uint64(hint) * 0x9e3779b97f4a7c15) >> (64 - metricShardBits))
}

// histShard is a shard of a Histogram: a count per bucket, the last for
// durations past every bound, and their sum in nanoseconds.
type histShard struct {
	counts [len(DurationBuckets) + 1]atomic.Uint64
	sum    atomic.Uint64
}

// bucket returns the index of the bucket d falls in.
func bucket(d time.Duration) int {
	n := int64(d)
	for i, b := range bucketNanos {
		if n <= b {
			return i
		}
	}
	return len(bucketNanos)
}

func (s *histShard) observe(d time.Duration) {
	s.counts[bucket(d)].Add(1)
	if d > 0 {
		s.sum.Add(uint64(d))
	}
}

// HistogramSnapshot is a histogram as scraped: the cumulative count of each
// bucket of DurationBuckets, then the total, and the sum in seconds.
type HistogramSnapshot struct {
	Buckets [len(DurationBuckets)]uint64
	Count   uint64
	Sum     float64
}

func (h *HistogramSnapshot) add(s *histShard) {
	var cum uint64
	for i := range h.Buckets {
		cum += s.counts[i].Load()
		h.Buckets[i] += cum
	}
	h.Count += cum + s.counts[len(DurationBuckets)].Load()
	h.Sum += float64(s.sum.Load()) / 1e9
}

// Histogram counts durations by DurationBuckets.
type Histogram struct {
	shards [metricShards]struct {
		histShard
		_ [24]byte // 128 bytes a shard
	}
}

// Observe counts d, on the shard hint picks.
func (h *Histogram) Observe(hint uintptr, d time.Duration) { h.shards[pickShard(hint)].observe(d) }

// Snapshot returns what h has counted.
func (h *Histogram) Snapshot() HistogramSnapshot {
	var s HistogramSnapshot
	for i := range h.shards {
		s.add(&h.shards[i].histShard)
	}
	return s
}

// StatusClasses are the labels of a status's class: StatusClasses[status/100-1].
var StatusClasses = [...]string{"1xx", "2xx", "3xx", "4xx", "5xx"}

// RequestMetrics count the requests a listener's route answered, by the
// class of their status, and time them. It is shared by every graph that
// has the route on the listener, so its counts carry over reloads.
type RequestMetrics struct {
	Listener, Route string

	shards [metricShards]struct {
		histShard
		codes [len(StatusClasses)]atomic.Uint64
		_     [48]byte // 192 bytes a shard
	}
	refs int // under Metrics.mu
}

// Observe counts a request answered with status after d, on the shard hint
// picks.
func (m *RequestMetrics) Observe(hint uintptr, status int, d time.Duration) {
	s := &m.shards[pickShard(hint)]
	class := min(max(status/100-1, 0), len(StatusClasses)-1)
	s.codes[class].Add(1)
	s.observe(d)
}

// Codes returns the requests counted, by the class of their status.
func (m *RequestMetrics) Codes() (codes [len(StatusClasses)]uint64) {
	for i := range m.shards {
		for c := range codes {
			codes[c] += m.shards[i].codes[c].Load()
		}
	}
	return codes
}

// Duration returns the requests' durations.
func (m *RequestMetrics) Duration() HistogramSnapshot {
	var s HistogramSnapshot
	for i := range m.shards {
		s.add(&m.shards[i].histShard)
	}
	return s
}

// Metrics keeps the RequestMetrics of the routes the graphs in use have, for
// as long as one has them: a graph takes them when it is compiled and gives
// them back when it is cleaned up, so the counts of a route a reload keeps
// go on, and a route no graph has is no longer kept or scraped.
type Metrics struct {
	mu       sync.Mutex
	requests map[[2]string]*RequestMetrics
}

// NewMetrics returns an empty Metrics.
func NewMetrics() *Metrics { return &Metrics{requests: map[[2]string]*RequestMetrics{}} }

// Requests takes the RequestMetrics of route on listener, made if no graph
// has them; Release gives them back.
func (m *Metrics) Requests(listener, route string) *RequestMetrics {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := [2]string{listener, route}
	r := m.requests[key]
	if r == nil {
		r = &RequestMetrics{Listener: listener, Route: route}
		m.requests[key] = r
	}
	r.refs++
	return r
}

// Release gives back RequestMetrics taken with Requests.
func (m *Metrics) Release(r *RequestMetrics) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r.refs--; r.refs == 0 {
		delete(m.requests, [2]string{r.Listener, r.Route})
	}
}

// AllRequests returns the RequestMetrics kept, by listener and route.
func (m *Metrics) AllRequests() []*RequestMetrics {
	m.mu.Lock()
	all := make([]*RequestMetrics, 0, len(m.requests))
	for _, r := range m.requests {
		all = append(all, r)
	}
	m.mu.Unlock()
	slices.SortFunc(all, func(a, b *RequestMetrics) int {
		if c := strings.Compare(a.Listener, b.Listener); c != 0 {
			return c
		}
		return strings.Compare(a.Route, b.Route)
	})
	return all
}

// TextWriter writes metrics in Prometheus's text format, version 0.0.4.
// Each family is begun with Family and its samples follow it.
type TextWriter struct {
	B []byte
}

// TextContentType is the media type of what TextWriter writes.
const TextContentType = "text/plain; version=0.0.4; charset=utf-8"

// Family begins a family of metrics: its HELP and TYPE lines.
func (w *TextWriter) Family(name, typ, help string) {
	w.B = append(w.B, "# HELP "...)
	w.B = append(w.B, name...)
	w.B = append(w.B, ' ')
	for _, r := range help {
		switch r {
		case '\\':
			w.B = append(w.B, `\\`...)
		case '\n':
			w.B = append(w.B, `\n`...)
		default:
			w.B = append(w.B, string(r)...)
		}
	}
	w.B = append(w.B, "\n# TYPE "...)
	w.B = append(w.B, name...)
	w.B = append(w.B, ' ')
	w.B = append(w.B, typ...)
	w.B = append(w.B, '\n')
}

// Sample writes a sample of name with labels, given as name and value
// pairs.
func (w *TextWriter) Sample(name string, value float64, labels ...string) {
	w.B = append(w.B, name...)
	if len(labels) > 0 {
		w.B = append(w.B, '{')
		for i := 0; i+1 < len(labels); i += 2 {
			if i > 0 {
				w.B = append(w.B, ',')
			}
			w.B = append(w.B, labels[i]...)
			w.B = append(w.B, `="`...)
			for _, r := range labels[i+1] {
				switch r {
				case '\\':
					w.B = append(w.B, `\\`...)
				case '"':
					w.B = append(w.B, `\"`...)
				case '\n':
					w.B = append(w.B, `\n`...)
				default:
					w.B = append(w.B, string(r)...)
				}
			}
			w.B = append(w.B, '"')
		}
		w.B = append(w.B, '}')
	}
	w.B = append(w.B, ' ')
	w.B = appendFloat(w.B, value)
	w.B = append(w.B, '\n')
}

// Histogram writes the samples of a histogram: its buckets, its sum and
// its count.
func (w *TextWriter) Histogram(name string, h HistogramSnapshot, labels ...string) {
	withLE := append(slices.Clip(labels), "le", "")
	for i, b := range DurationBuckets {
		withLE[len(withLE)-1] = strconv.FormatFloat(b, 'g', -1, 64)
		w.Sample(name+"_bucket", float64(h.Buckets[i]), withLE...)
	}
	withLE[len(withLE)-1] = "+Inf"
	w.Sample(name+"_bucket", float64(h.Count), withLE...)
	w.Sample(name+"_sum", h.Sum, labels...)
	w.Sample(name+"_count", float64(h.Count), labels...)
}

func appendFloat(b []byte, v float64) []byte {
	switch {
	case math.IsInf(v, 1):
		return append(b, "+Inf"...)
	case math.IsInf(v, -1):
		return append(b, "-Inf"...)
	case math.IsNaN(v):
		return append(b, "NaN"...)
	}
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return strconv.AppendInt(b, int64(v), 10)
	}
	return strconv.AppendFloat(b, v, 'g', -1, 64)
}
