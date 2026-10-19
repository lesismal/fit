package upstream

import (
	"sync"
	"sync/atomic"
)

// Load balancing policies, as config.Upstream's load_balancing names them.
const (
	RoundRobin         = "round_robin"
	WeightedRoundRobin = "weighted_round_robin"
	LeastConn          = "least_conn"
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
}

// Active returns how many requests to b are under way.
func (b *Backend) Active() int64 { return b.active.Load() }

// Balancer picks the backend for the next request. Pick runs once a request
// and must not allocate or block for long.
type Balancer interface {
	Pick() *Backend
}

// NewBalancer returns the balancer policy names, over backends, which it
// keeps. An empty policy is round robin, or weighted round robin when any
// backend's weight is not 1.
func NewBalancer(policy string, backends []*Backend) Balancer {
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
	default:
		return &roundRobin{backends: backends}
	}
}

// roundRobin takes the backends in turn, without a lock.
type roundRobin struct {
	backends []*Backend
	next     atomic.Uint64
}

func (r *roundRobin) Pick() *Backend {
	n := uint64(len(r.backends))
	return r.backends[(r.next.Add(1)-1)%n]
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

func (w *weightedRoundRobin) Pick() *Backend {
	w.mu.Lock()
	defer w.mu.Unlock()
	var best *Backend
	total := 0
	for _, b := range w.backends {
		b.current += b.Weight
		total += b.Weight
		if best == nil || b.current > best.current {
			best = b
		}
	}
	best.current -= total
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

func (l *leastConn) Pick() *Backend {
	n := len(l.backends)
	start := int((l.next.Add(1) - 1) % uint64(n))
	best := l.backends[start]
	bestActive := best.active.Load()
	for i := 1; i < n; i++ {
		b := l.backends[(start+i)%n]
		active := b.active.Load()
		// active/weight < bestActive/bestWeight, without dividing.
		if active*int64(best.Weight) < bestActive*int64(b.Weight) {
			best, bestActive = b, active
		}
	}
	return best
}
