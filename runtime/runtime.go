package runtime

import (
	"sync"
	"sync/atomic"
)

// Runtime holds the current graph. Its methods are safe to call from any
// goroutine; Current is lock-free, for the data plane to call per request
// or per connection.
type Runtime struct {
	current atomic.Pointer[Graph]
	mu      sync.Mutex // orders swaps, and the versions they give
	version uint64
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
// still uses it is done. A nil g leaves no current graph.
func (r *Runtime) Swap(g *Graph) {
	r.mu.Lock()
	if g != nil {
		r.version++
		g.Version = r.version
	}
	old := r.current.Swap(g)
	r.mu.Unlock()
	if old != nil {
		old.Release()
	}
}

// Close drops the current graph, as Swap(nil) does.
func (r *Runtime) Close() { r.Swap(nil) }
