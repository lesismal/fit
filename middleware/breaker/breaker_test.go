package breaker

import (
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/fibtest"
	"github.com/lesismal/fit/module"
)

func load(t testing.TB, params string) (*Breaker, error) {
	t.Helper()
	v, err := config.ParseValue([]byte(`{"module": "circuit_breaker", "params": `+params+`}`), "middleware")
	if err != nil {
		t.Fatal(err)
	}
	var ref config.ModuleRef
	if err := v.Decode(&ref); err != nil {
		return nil, err
	}
	m, err := module.Load(module.Middlewares, ref, nil)
	if err != nil {
		return nil, err
	}
	return m.(*Breaker), nil
}

// rig is a breaker on a test clock, wrapping a handler that answers with
// the status, and takes the time, the test sets.
type rig struct {
	t           *testing.T
	b           *Breaker
	h           module.Handler
	mu          sync.Mutex
	now         time.Time
	status      int
	takes       time.Duration
	transitions []string
}

func newRig(t *testing.T, params string) *rig {
	t.Helper()
	b, err := load(t, params)
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, b: b, now: time.Unix(1_000_000, 0), status: 200}
	b.now = r.clock
	b.headAt = r.now
	record := func(from, to State) { r.transitions = append(r.transitions, from.String()+">"+to.String()) }
	b.transition.Store(&record)
	r.h = b.Wrap(fibhttp.HandlerFunc(func(c *fibhttp.Context) {
		r.mu.Lock()
		r.now = r.now.Add(r.takes)
		status := r.status
		r.mu.Unlock()
		_ = c.Respond(status, "", nil)
	}))
	return r
}

func (r *rig) clock() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.now
}

func (r *rig) advance(d time.Duration) {
	r.mu.Lock()
	r.now = r.now.Add(d)
	r.mu.Unlock()
}

// serve sends n requests answered with status and returns what the client
// got of each, counted by status.
func (r *rig) serve(n, status int) map[int]int {
	r.t.Helper()
	r.mu.Lock()
	r.status = status
	r.mu.Unlock()
	got := map[int]int{}
	for range n {
		got[fibtest.ServeRequest(r.h, httptest.NewRequest("GET", "/", nil)).Response.StatusCode]++
	}
	return got
}

func (r *rig) expect(state State, transitions ...string) {
	r.t.Helper()
	if s := r.b.State(); s != state {
		r.t.Fatalf("state %v, want %v", s, state)
	}
	if got := strings.Join(r.transitions, " "); got != strings.Join(transitions, " ") {
		r.t.Fatalf("transitions %q, want %q", got, strings.Join(transitions, " "))
	}
}

func TestBreakerTransitions(t *testing.T) {
	r := newRig(t, `{"min_requests": 10, "error_ratio": 0.5, "open_duration": "30s", "half_open_requests": 3}`)

	// Closed: failures under the ratio, or under min_requests, keep it so.
	r.serve(6, 200)
	r.serve(3, 502)
	r.expect(Closed)
	r.serve(4, 200)
	r.serve(3, 500) // 6 of 16
	r.expect(Closed)

	// Past the ratio it opens, and answers 503 at once with when to retry.
	r.serve(10, 503) // 16 of 26
	r.expect(Open, "closed>open")
	rec := fibtest.ServeRequest(r.h, httptest.NewRequest("GET", "/", nil))
	if rec.Response.StatusCode != 503 || rec.Response.Header.Get("Retry-After") != "30" {
		t.Fatalf("open: %d %v", rec.Response.StatusCode, rec.Response.Header)
	}
	if got := r.serve(5, 200); got[503] != 5 {
		t.Fatalf("open let requests through: %v", got)
	}
	r.advance(20 * time.Second)
	rec = fibtest.ServeRequest(r.h, httptest.NewRequest("GET", "/", nil))
	if rec.Response.Header.Get("Retry-After") != "10" {
		t.Fatalf("Retry-After %q after 20s", rec.Response.Header.Get("Retry-After"))
	}

	// Half-open after open_duration: a failed probe opens it again.
	r.advance(10 * time.Second)
	r.expect(HalfOpen, "closed>open", "open>half-open")
	r.serve(1, 200)
	r.serve(1, 500)
	r.expect(Open, "closed>open", "open>half-open", "half-open>open")

	// Probes that all fare well close it, and the window starts afresh.
	r.advance(30 * time.Second)
	if got := r.serve(3, 200); got[200] != 3 {
		t.Fatalf("probes: %v", got)
	}
	r.expect(Closed, "closed>open", "open>half-open", "half-open>open", "open>half-open", "half-open>closed")
	r.serve(9, 500)
	r.expect(Closed, "closed>open", "open>half-open", "half-open>open", "open>half-open", "half-open>closed")
}

// TestBreakerHalfOpenLimit checks that half-open lets only
// half_open_requests through at once, and that a probe whose client goes
// gives its place back.
func TestBreakerHalfOpenLimit(t *testing.T) {
	b, err := load(t, `{"min_requests": 1, "open_duration": "1s", "half_open_requests": 2}`)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_000_000, 0)
	b.now = func() time.Time { return now }
	b.headAt = now
	b.record(now, false, true, false)
	if b.State() != Open {
		t.Fatal("not open")
	}
	now = now.Add(time.Second)
	p1, _, ok1 := b.admit(now)
	p2, _, ok2 := b.admit(now)
	_, wait, ok3 := b.admit(now)
	if !p1 || !ok1 || !p2 || !ok2 || ok3 || wait != time.Second {
		t.Fatalf("admitted %v %v %v", ok1, ok2, ok3)
	}
	(&outcome{b: b, probe: true}).cancel(nil)
	if _, _, ok := b.admit(now); !ok {
		t.Fatal("a cancelled probe's place was not given back")
	}
}

func TestBreakerWindow(t *testing.T) {
	r := newRig(t, `{"window": "10s", "min_requests": 10}`)
	r.serve(9, 500)
	r.advance(11 * time.Second) // they leave the window
	r.serve(9, 200)
	r.serve(1, 500)
	r.expect(Closed)
	r.advance(5 * time.Second)
	r.serve(9, 500) // 10 of 19 in the last 10s
	r.expect(Open, "closed>open")
}

func TestBreakerSlowAndStatuses(t *testing.T) {
	r := newRig(t, `{"min_requests": 4, "slow_threshold": "1s", "slow_ratio": 0.5, "statuses": [429]}`)
	r.serve(4, 500) // not a failure here
	r.expect(Closed)
	r.takes = 2 * time.Second
	r.serve(4, 200)
	r.expect(Open, "closed>open") // 4 slow of 8

	r = newRig(t, `{"min_requests": 4, "statuses": [429]}`)
	r.serve(4, 429)
	r.expect(Open, "closed>open")
}

func TestBreakerParams(t *testing.T) {
	for _, tt := range []struct{ params, err string }{
		{`{"statuses": [600]}`, "600 is not a status"},
		{`{"error_ratio": 1.5}`, "error_ratio"},
		{`{"min_requests": 0}`, "less than the minimum 1"},
	} {
		_, err := load(t, tt.params)
		if tt.err == "" && err != nil || tt.err != "" && (err == nil || !strings.Contains(err.Error(), tt.err)) {
			t.Errorf("%s: %v, want %q", tt.params, err, tt.err)
		}
	}
}

// BenchmarkBreaker serves an inline handler through fibtest, bare and
// behind a closed breaker.
func BenchmarkBreaker(b *testing.B) {
	br, err := load(b, `{}`)
	if err != nil {
		b.Fatal(err)
	}
	ok := fibhttp.HandlerFunc(func(c *fibhttp.Context) { _ = c.Respond(200, "", nil) })
	for name, h := range map[string]module.Handler{"none": ok, "breaker": br.Wrap(ok)} {
		b.Run(name, func(b *testing.B) {
			r := httptest.NewRequest("GET", "/", nil)
			b.ReportAllocs()
			for b.Loop() {
				fibtest.ServeRequest(h, r)
			}
		})
	}
}
