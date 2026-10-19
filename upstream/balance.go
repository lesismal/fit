package upstream

import (
	"math"
	stdhttp "net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Load balancing policies, as config.Upstream's load_balancing names them.
const (
	RoundRobin         = "round_robin"
	WeightedRoundRobin = "weighted_round_robin"
	LeastConn          = "least_conn"
	Random             = "random"
	P2C                = "p2c"
	IPHash             = "ip_hash"
	URIHash            = "uri_hash"
	HeaderHash         = "header_hash"
	CookieHash         = "cookie_hash"
)

// Backend is one of an upstream's addresses.
type Backend struct {
	Address string
	Weight  int
	// active counts the requests to the backend that are not over, body
	// included: from Pool.Pick to Pool.Done.
	active atomic.Int64
	// current is smooth weighted round robin's running weight, guarded by
	// the balancer's lock.
	current int

	// downUntil is when, in Unix nanoseconds, a backend out of rotation may
	// take requests again: math.MaxInt64 while its probes say it is down,
	// and otherwise passiveUntil; 0 for one that is up. Picking reads only
	// it, and the clock only when it is set.
	downUntil atomic.Int64
	// failing says fails may be above zero, so that a success need not take
	// the lock to find it is not.
	failing atomic.Bool
	mu      sync.Mutex
	fails   int
	// windowEnd is when the failures counted so far stop counting.
	windowEnd int64
	// passiveUntil is when a backend that failed too many requests may take
	// requests again, or 0, and probedDown says its active health check
	// took it out of rotation.
	passiveUntil int64
	probedDown   atomic.Bool // written under mu
}

// Active returns how many requests to b are under way.
func (b *Backend) Active() int64 { return b.active.Load() }

// Down reports whether b is out of rotation, for having failed too many
// requests or its probes.
func (b *Backend) Down() bool {
	d := b.downUntil.Load()
	return d != 0 && time.Now().UnixNano() < d
}

// up reports whether b may take a request. now is the time in Unix
// nanoseconds, read the first time a pick needs it and kept in *now.
func (b *Backend) up(now *int64) bool {
	d := b.downUntil.Load()
	if d == 0 {
		return true
	}
	if *now == 0 {
		*now = time.Now().UnixNano()
	}
	return *now >= d
}

// failed counts a failure of b. maxFails of them within window take b out
// of rotation for window, as nginx's max_fails and fail_timeout do.
func (b *Backend) failed(maxFails int, window time.Duration) {
	if maxFails <= 0 {
		return
	}
	now := time.Now().UnixNano()
	b.mu.Lock()
	defer b.mu.Unlock()
	if now >= b.windowEnd {
		b.fails, b.windowEnd = 0, now+int64(window)
	}
	b.fails++
	b.failing.Store(true)
	if b.fails >= maxFails {
		b.passiveUntil = now + int64(window)
		b.fails, b.windowEnd = 0, 0
		b.storeDown()
	}
}

// storeDown sets downUntil from the passive and the active state. b.mu must
// be held.
func (b *Backend) storeDown() {
	if b.probedDown.Load() {
		b.downUntil.Store(math.MaxInt64)
	} else {
		b.downUntil.Store(b.passiveUntil)
	}
}

// ProbedDown reports whether b's active health check has taken it out of
// rotation.
func (b *Backend) ProbedDown() bool { return b.probedDown.Load() }

func (b *Backend) setProbedDown(down bool) {
	b.mu.Lock()
	b.probedDown.Store(down)
	b.storeDown()
	b.mu.Unlock()
}

// succeeded clears b's failures: one success is enough to put a backend
// that has come back fully in rotation again, unless its probes keep it out.
func (b *Backend) succeeded() {
	if !b.failing.Load() && b.downUntil.Load() == 0 {
		return
	}
	b.mu.Lock()
	b.fails, b.windowEnd, b.passiveUntil = 0, 0, 0
	b.failing.Store(false)
	b.storeDown()
	b.mu.Unlock()
}

// Balancer picks the backend for a request, which may be nil when there is
// none to go by. Pick runs once a request and must not allocate or block for
// long. It passes over the backends that
// are down, unless all of them are: then health is ignored, since an
// upstream that may still answer serves better than one that surely will
// not, which is also why nginx ignores health for a single server.
type Balancer interface {
	Pick(r *stdhttp.Request) *Backend
}

// NewBalancer returns the balancer policy names, over backends, which it
// keeps; key is the header or cookie header_hash and cookie_hash hash by. An
// empty policy is round robin, or weighted round robin when any backend's
// weight is not 1.
func NewBalancer(policy string, backends []*Backend, key string) Balancer {
	if policy == "" {
		policy = RoundRobin
		for _, b := range backends {
			if b.Weight != 1 {
				policy = WeightedRoundRobin
			}
		}
	}
	switch policy {
	case WeightedRoundRobin:
		return &weightedRoundRobin{backends: backends}
	case LeastConn:
		return &leastConn{backends: backends}
	case Random:
		return newRandom(backends)
	case P2C:
		return &p2c{backends: backends}
	case IPHash, URIHash, HeaderHash, CookieHash:
		return newHash(policy, key, backends)
	default:
		return &roundRobin{backends: backends}
	}
}

// roundRobin takes the backends in turn, without a lock.
type roundRobin struct {
	backends []*Backend
	next     atomic.Uint64
}

func (r *roundRobin) Pick(*stdhttp.Request) *Backend {
	n := uint64(len(r.backends))
	first := r.next.Add(1) - 1
	var now int64
	for i := range n {
		if b := r.backends[(first+i)%n]; b.up(&now) {
			if i > 0 {
				// Move the turn past the backends passed over.
				r.next.Add(i)
			}
			return b
		}
	}
	return r.backends[first%n]
}

// weightedRoundRobin is nginx's smooth weighted round robin: each pick adds
// every backend's weight to its running weight, takes the backend with the
// highest, and takes the total back off it. A backend of weight w gets w
// picks of every total, spread out rather than in a run: weights 5, 1 and 1
// give a a b a c a a.
type weightedRoundRobin struct {
	mu       sync.Mutex
	backends []*Backend
}

func (w *weightedRoundRobin) Pick(*stdhttp.Request) *Backend {
	w.mu.Lock()
	defer w.mu.Unlock()
	var now int64
	if b := w.pick(&now, true); b != nil {
		return b
	}
	return w.pick(&now, false)
}

// pick is one round of the algorithm, over the backends that are up or,
// without healthy, over all of them. It returns nil when none is up.
func (w *weightedRoundRobin) pick(now *int64, healthy bool) *Backend {
	var best *Backend
	total := 0
	for _, b := range w.backends {
		if healthy && !b.up(now) {
			continue
		}
		b.current += b.Weight
		total += b.Weight
		if best == nil || b.current > best.current {
			best = b
		}
	}
	if best != nil {
		best.current -= total
	}
	return best
}

// leastConn takes the backend with the fewest requests under way for its
// weight. Ties go to the first found from a starting point that moves on
// with each pick, so that idle backends share the load rather than the first
// taking all of it. It reads the counts without a lock, so two picks at once
// may both see the same backend as the least busy, which only makes it a
// little busier than it should be.
type leastConn struct {
	backends []*Backend
	next     atomic.Uint64
}

func (l *leastConn) Pick(*stdhttp.Request) *Backend {
	n := uint64(len(l.backends))
	first := l.next.Add(1) - 1
	var now int64
	// The scan starts at the next backend in turn that is up, the turn moved
	// past those passed over, so that ties spread over the backends up
	// rather than going to whichever follows one that is down.
	for i := range n {
		if l.backends[(first+i)%n].up(&now) {
			if i > 0 {
				l.next.Add(i)
			}
			return l.pick(int((first+i)%n), &now, true)
		}
	}
	return l.pick(int(first%n), &now, false)
}

// pick scans from start for the least busy backend that is up or, without
// healthy, of all of them. It returns nil when none is up.
func (l *leastConn) pick(start int, now *int64, healthy bool) *Backend {
	n := len(l.backends)
	var best *Backend
	var bestActive int64
	for i := range n {
		b := l.backends[(start+i)%n]
		if healthy && !b.up(now) {
			continue
		}
		active := b.active.Load()
		// active/weight < bestActive/bestWeight, without dividing.
		if best == nil || active*int64(best.Weight) < bestActive*int64(b.Weight) {
			best, bestActive = b, active
		}
	}
	return best
}
