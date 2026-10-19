package runtime

import (
	"sync"
	"sync/atomic"
	"time"
)

// Runtime holds the current graph. Its methods are safe to call from any
// goroutine; Current is lock-free, for the data plane to call per request
// or per connection.
type Runtime struct {
	current atomic.Pointer[Graph]
	mu      sync.Mutex // orders swaps, and the versions they give
	version uint64
	drain   time.Duration
}

// DefaultDrainTimeout is how long requests may keep using a graph after it
// is swapped out, unless SetDrainTimeout says otherwise.
const DefaultDrainTimeout = 30 * time.Second

// SetDrainTimeout sets how long requests may keep using a graph once it is
// swapped out: past it, the graph's modules end what they still have under
// way, as the proxy cuts off the exchanges it has open. Zero is the
// default; a negative d never drains.
func (r *Runtime) SetDrainTimeout(d time.Duration) {
	r.mu.Lock()
	r.drain = d
	r.mu.Unlock()
}

// Current returns the current graph with a reference taken for the caller,
// who must Release it when done; or nil when there is none.
func (r *Runtime) Current() *Graph {
	for {
		g := r.current.Load()
		if g == nil {
			return nil
		}
		// A graph swapped out between the load and the retain may have lost
		// its last reference; the graph that replaced it is current then.
		if g.tryRetain() {
			return g
		}
	}
}

// Swap makes g the current graph and gives it the next version number. It
// takes over the caller's reference to g, and releases the runtime's
// reference to the graph it replaces, which is cleaned up once whatever
// still uses it is done, or drained after the drain timeout. A nil g
// leaves no current graph.
func (r *Runtime) Swap(g *Graph) {
	r.mu.Lock()
	if g != nil {
		r.version++
		g.Version = r.version
	}
	old := r.current.Swap(g)
	drain := r.drain
	r.mu.Unlock()
	if drain == 0 {
		drain = DefaultDrainTimeout
	}
	if old != nil {
		old.retire(drain)
		old.Release()
	}
}

// Close drops the current graph, as Swap(nil) does.
func (r *Runtime) Close() { r.Swap(nil) }
