package observe

import (
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"
)

func TestHistogram(t *testing.T) {
	var h Histogram
	for _, d := range []time.Duration{0, 5 * time.Millisecond, 6 * time.Millisecond, time.Second, 11 * time.Second, -time.Second} {
		h.Observe(0, d)
	}
	s := h.Snapshot()
	// 0, 5ms and -1s fall in the first bucket (le 0.005), 6ms in the
	// second, 1s in le 1, and 11s past every bound.
	want := [len(DurationBuckets)]uint64{3, 4, 4, 4, 4, 4, 4, 5, 5, 5, 5}
	if s.Buckets != want || s.Count != 6 {
		t.Errorf("buckets %v, count %d; want %v, 6", s.Buckets, s.Count, want)
	}
	if got, want := s.Sum, 12.011; got < want-1e-9 || got > want+1e-9 {
		t.Errorf("sum %v, want %v", got, want)
	}
}

func TestRequestMetricsConcurrent(t *testing.T) {
	var m RequestMetrics
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			for i := range 1000 {
				m.Observe(uintptr(g)*64, []int{200, 404, 503, 101, 302, 0, 999}[i%7], time.Millisecond)
			}
		})
	}
	wg.Wait()
	// 0 counts as 1xx and 999 as 5xx: every request is counted somewhere.
	codes := m.Codes()
	want := [len(StatusClasses)]uint64{2288, 1144, 1144, 1144, 2280}
	if codes != want {
		t.Errorf("codes %v, want %v", codes, want)
	}
	if d := m.Duration(); d.Count != 8000 || d.Buckets[0] != 8000 {
		t.Errorf("duration %+v", d)
	}
}

func TestMetricsRegistry(t *testing.T) {
	m := NewMetrics()
	a := m.Requests("web", "api")
	b := m.Requests("web", "api")
	if a != b {
		t.Fatal("the same route got two RequestMetrics")
	}
	c := m.Requests("admin", "")
	a.Observe(0, 200, time.Millisecond)
	if all := m.AllRequests(); len(all) != 2 || all[0] != c || all[1] != a {
		t.Fatalf("all: %v", all)
	}
	m.Release(a)
	if all := m.AllRequests(); len(all) != 2 {
		t.Fatalf("released while still taken: %v", all)
	}
	m.Release(b)
	if all := m.AllRequests(); len(all) != 1 || all[0] != c {
		t.Fatalf("not released: %v", all)
	}
	// Taken again, it starts afresh.
	if n := m.Requests("web", "api").Codes()[1]; n != 0 {
		t.Errorf("a route taken again kept %d requests", n)
	}
}

func TestTextWriter(t *testing.T) {
	var w TextWriter
	w.Family("x_total", "counter", "Things\\counted,\nby kind.")
	w.Sample("x_total", 3, "kind", `a"b\c`+"\nd")
	w.Sample("x_total", 1.5e20)
	w.Sample("x_total", 0.25, "a", "1", "b", "2")
	var h Histogram
	h.Observe(0, 20*time.Millisecond)
	w.Family("d_seconds", "histogram", "Durations.")
	w.Histogram("d_seconds", h.Snapshot(), "route", "r")
	want := `# HELP x_total Things\\counted,\nby kind.
# TYPE x_total counter
x_total{kind="a\"b\\c\nd"} 3
x_total 1.5e+20
x_total{a="1",b="2"} 0.25
# HELP d_seconds Durations.
# TYPE d_seconds histogram
d_seconds_bucket{route="r",le="0.005"} 0
d_seconds_bucket{route="r",le="0.01"} 0
d_seconds_bucket{route="r",le="0.025"} 1
d_seconds_bucket{route="r",le="0.05"} 1
d_seconds_bucket{route="r",le="0.1"} 1
d_seconds_bucket{route="r",le="0.25"} 1
d_seconds_bucket{route="r",le="0.5"} 1
d_seconds_bucket{route="r",le="1"} 1
d_seconds_bucket{route="r",le="2.5"} 1
d_seconds_bucket{route="r",le="5"} 1
d_seconds_bucket{route="r",le="10"} 1
d_seconds_bucket{route="r",le="+Inf"} 1
d_seconds_sum{route="r"} 0.02
d_seconds_count{route="r"} 1
`
	if got := string(w.B); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
		for i, l := range strings.Split(got, "\n") {
			if w := strings.Split(want, "\n"); i < len(w) && l != w[i] {
				t.Errorf("line %d: %q, want %q", i+1, l, w[i])
				break
			}
		}
	}
}

// BenchmarkRequestMetrics counts requests to one route from every core at
// once.
func BenchmarkRequestMetrics(b *testing.B) {
	var m RequestMetrics
	b.RunParallel(func(pb *testing.PB) {
		// A hint of each goroutine's own, as a Context is a core's.
		hint := uintptr(unsafe.Pointer(new([64]byte)))
		for pb.Next() {
			m.Observe(hint, 200, 3*time.Millisecond)
		}
	})
}

func TestShardSizes(t *testing.T) {
	var h Histogram
	var m RequestMetrics
	if s := unsafe.Sizeof(h.shards[0]); s != 128 {
		t.Errorf("a histogram's shard is %d bytes", s)
	}
	if s := unsafe.Sizeof(m.shards[0]); s != 192 {
		t.Errorf("a route's shard is %d bytes", s)
	}
}
