// Package breaker has the circuit_breaker middleware, which stops sending a
// route's requests on to what fails them, and tries again after a while.
package breaker

import (
	stdhttp "net/http"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
)

func init() {
	module.Register(module.Info{ID: module.Middlewares + ".circuit_breaker", New: func() any { return new(Breaker) }})
}

// State is a breaker's state.
type State int32

const (
	// Closed lets requests through, counting how they fare.
	Closed State = iota
	// Open answers every request at once, 503, until open_duration has
	// passed.
	Open
	// HalfOpen lets half_open_requests through to see whether what failed
	// has recovered: if they all fare well the breaker closes, and if one
	// fails it opens again.
	HalfOpen
)

func (s State) String() string { return [...]string{"closed", "open", "half-open"}[s] }

// Breaker is a circuit breaker on the route's requests: while it is closed
// it counts, over the last window, the requests whose responses were
// failures (5xx, or a status of statuses) and those slower than
// slow_threshold to begin; once at least min_requests were counted and the
// failures reach error_ratio, or the slow ones slow_ratio, it opens.
type Breaker struct {
	Window           config.Duration `json:"window,omitempty" default:"\"10s\"" doc:"How far back requests are counted, in ten steps; 10s when left out."`
	MinRequests      int             `json:"min_requests,omitempty" default:"20" min:"1" doc:"How many requests the window must have counted before it can open; 20 when left out."`
	ErrorRatio       float64         `json:"error_ratio,omitempty" default:"0.5" min:"0" max:"1" doc:"The share of failures in the window that opens it; 0.5 when left out."`
	Statuses         []int           `json:"statuses,omitempty" doc:"The statuses that count as failures; 500 to 599 when left out."`
	SlowThreshold    config.Duration `json:"slow_threshold,omitempty" doc:"A response that takes longer than this to begin counts as slow; none is when left out."`
	SlowRatio        float64         `json:"slow_ratio,omitempty" default:"0.5" min:"0" max:"1" doc:"With slow_threshold, the share of slow responses in the window that opens it; 0.5 when left out."`
	OpenDuration     config.Duration `json:"open_duration,omitempty" default:"\"30s\"" doc:"How long it stays open before letting requests through to try; 30s when left out."`
	HalfOpenRequests int             `json:"half_open_requests,omitempty" default:"5" min:"1" doc:"How many requests it lets through, half-open, that must all fare well for it to close; 5 when left out. Others are answered 503 meanwhile."`

	window     time.Duration
	bucket     time.Duration
	slow       time.Duration
	open       time.Duration
	isFailure  func(int) bool
	now        func() time.Time
	transition atomic.Pointer[func(from, to State)] // for tests

	mu      sync.Mutex
	state   State
	buckets [buckets]counts
	head    int       // the bucket now being counted
	headAt  time.Time // when it began
	until   time.Time // when an open breaker turns half-open
	trying  int       // half-open requests let through and not yet over
	passed  int       // half-open requests that fared well
}

const buckets = 10

type counts struct{ requests, failures, slow int }

// Provision checks the params and sets the defaults.
func (b *Breaker) Provision(ctx *module.Context) error {
	b.window = b.Window.Std()
	if b.window <= 0 {
		b.window = 10 * time.Second
	}
	b.bucket = b.window / buckets
	if b.MinRequests == 0 {
		b.MinRequests = 20
	}
	if b.ErrorRatio == 0 {
		b.ErrorRatio = 0.5
	}
	if b.SlowRatio == 0 {
		b.SlowRatio = 0.5
	}
	b.slow = b.SlowThreshold.Std()
	b.open = b.OpenDuration.Std()
	if b.open <= 0 {
		b.open = 30 * time.Second
	}
	if b.HalfOpenRequests == 0 {
		b.HalfOpenRequests = 5
	}
	for i, s := range b.Statuses {
		if s < 100 || s > 599 {
			return ctx.Params.Field("statuses").Index(i).Errorf("%d is not a status", s)
		}
	}
	if len(b.Statuses) == 0 {
		b.isFailure = func(s int) bool { return s >= 500 }
	} else {
		statuses := slices.Clone(b.Statuses)
		b.isFailure = func(s int) bool { return slices.Contains(statuses, s) }
	}
	b.now = time.Now
	b.headAt = b.now()
	return nil
}

// State returns the breaker's state now.
func (b *Breaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tick(b.now())
	return b.state
}

// Wrap returns next behind the breaker.
func (b *Breaker) Wrap(next module.Handler) module.Handler {
	return fibhttp.HandlerFunc(func(c *fibhttp.Context) {
		start := b.now()
		probe, wait, ok := b.admit(start)
		if !ok {
			_ = c.WriteResponse(fibhttp.Response{
				StatusCode: stdhttp.StatusServiceUnavailable,
				Header:     stdhttp.Header{"Retry-After": {strconv.Itoa(int((wait + time.Second - 1) / time.Second))}},
			})
			return
		}
		o := &outcome{b: b, probe: probe, start: start}
		c.OnHeader(o.header)
		c.OnCancel(o.cancel)
		next.ServeHTTP(c)
	})
}

// outcome is how one request fared, recorded once.
type outcome struct {
	b     *Breaker
	probe bool
	start time.Time
	done  atomic.Bool
}

func (o *outcome) header(status int, _ stdhttp.Header) {
	if o.done.CompareAndSwap(false, true) {
		b := o.b
		now := b.now()
		b.record(now, o.probe, b.isFailure(status), b.slow > 0 && now.Sub(o.start) > b.slow)
	}
}

// cancel ends a request whose client went before an answer: it says
// nothing of what it was sent to, but a half-open probe gives its place
// back.
func (o *outcome) cancel(error) {
	if o.done.CompareAndSwap(false, true) && o.probe {
		b := o.b
		b.mu.Lock()
		if b.state == HalfOpen {
			b.trying--
		}
		b.mu.Unlock()
	}
}

// admit decides whether a request goes through: probe says it is one a
// half-open breaker tries, and wait, for one that does not, how long until
// the breaker tries again.
func (b *Breaker) admit(now time.Time) (probe bool, wait time.Duration, ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tick(now)
	switch b.state {
	case Open:
		return false, b.until.Sub(now), false
	case HalfOpen:
		if b.trying+b.passed >= b.HalfOpenRequests {
			return false, time.Second, false
		}
		b.trying++
		return true, 0, true
	}
	return false, 0, true
}

// record counts a response.
func (b *Breaker) record(now time.Time, probe, failed, slow bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tick(now)
	switch {
	case probe && b.state == HalfOpen:
		b.trying--
		if failed || slow {
			b.setLocked(Open, now)
			return
		}
		if b.passed++; b.passed >= b.HalfOpenRequests {
			b.setLocked(Closed, now)
		}
		return
	case b.state != Closed:
		return // from before it opened
	}
	c := &b.buckets[b.head]
	c.requests++
	if failed {
		c.failures++
	}
	if slow {
		c.slow++
	}
	var sum counts
	for _, bc := range b.buckets {
		sum.requests += bc.requests
		sum.failures += bc.failures
		sum.slow += bc.slow
	}
	if sum.requests < b.MinRequests {
		return
	}
	n := float64(sum.requests)
	if float64(sum.failures)/n >= b.ErrorRatio || b.slow > 0 && float64(sum.slow)/n >= b.SlowRatio {
		b.setLocked(Open, now)
	}
}

// tick moves the window on to now, and turns an open breaker half-open
// once its time is up. b.mu is held.
func (b *Breaker) tick(now time.Time) {
	if b.state == Open && !now.Before(b.until) {
		b.setLocked(HalfOpen, now)
	}
	steps := int(now.Sub(b.headAt) / b.bucket)
	if steps <= 0 {
		return
	}
	for i := 0; i < min(steps, buckets); i++ {
		b.head = (b.head + 1) % buckets
		b.buckets[b.head] = counts{}
	}
	b.headAt = b.headAt.Add(time.Duration(steps) * b.bucket)
}

// setLocked moves the breaker to state. b.mu is held.
func (b *Breaker) setLocked(state State, now time.Time) {
	from := b.state
	b.state = state
	switch state {
	case Open:
		b.until = now.Add(b.open)
	case HalfOpen:
		b.trying, b.passed = 0, 0
	case Closed:
		b.buckets = [buckets]counts{}
	}
	if fn := b.transition.Load(); fn != nil {
		(*fn)(from, state)
	}
}
