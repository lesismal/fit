// Package router picks the route that serves a request by its host and path.
//
// A route matches a request when one of its hosts matches the request's host
// and one of its paths matches the request's path; a route with no hosts
// matches every host, and one with no paths every path. Hosts are exact, as
// in "example.com", or a "*." wildcard, as in "*.example.com", which stands
// for exactly one label. Paths are exact, as in "/hello", or end in "*" to
// match a prefix, as in "/api/*". Hosts ignore case; paths do not.
//
// When several routes match, the one that serves the request is, in order:
//
//  1. the one with the highest priority;
//  2. the one whose host matched most specifically: an exact host, then a
//     wildcard, then a route with no hosts;
//  3. the one whose path matched most specifically: an exact path, then the
//     longest prefix, then a route with no paths;
//  4. the one written first.
//
// A Router is built once, by the control plane, and never changed; Match
// does not allocate.
package router

import (
	"math"
	"slices"
	"strings"
)

// Route is a route as the router sees it.
type Route[T any] struct {
	// Hosts and Paths are the route's patterns, assumed valid (config.Validate
	// checks them); empty matches everything.
	Hosts []string
	Paths []string
	// Priority orders routes before specificity does: higher first.
	Priority int
	// Value is what Match returns for the route.
	Value T
}

// Router finds the route for a host and path.
type Router[T any] struct {
	exact map[string]*node[T] // by lower-case host
	wild  map[string]*node[T] // by lower-case suffix, from the dot on: ".example.com"
	any   *node[T]            // routes with no hosts
}

// Host specificity, from least to most specific.
const (
	hostAny = iota
	hostWild
	hostExact
)

// pathExact is the specificity of an exact path; a prefix's is its length.
const pathExact = math.MaxInt

// node holds the routes that one host pattern leads to.
type node[T any] struct {
	exact  map[string]entry[T] // by path
	prefix map[string]entry[T] // by prefix, without the "*"
	// lens are the lengths of the prefixes, longest first.
	lens []int
	// maxPrefix is the highest priority of any prefix entry.
	maxPrefix int
}

type entry[T any] struct {
	value    T
	priority int
	path     int // path specificity
	order    int // position in the routes given to New
	ok       bool
}

// New builds a Router from routes. Routes that tie on priority and
// specificity are picked in the order given.
func New[T any](routes []Route[T]) *Router[T] {
	r := &Router[T]{exact: map[string]*node[T]{}, wild: map[string]*node[T]{}}
	for i, rt := range routes {
		hosts := rt.Hosts
		if len(hosts) == 0 {
			hosts = []string{""}
		}
		for _, h := range hosts {
			n := r.nodeFor(h)
			paths := rt.Paths
			if len(paths) == 0 {
				paths = []string{"*"}
			}
			for _, p := range paths {
				n.add(p, entry[T]{value: rt.Value, priority: rt.Priority, order: i, ok: true})
			}
		}
	}
	for _, n := range r.nodes() {
		n.finish()
	}
	return r
}

// nodeFor returns the node for host pattern h, creating it if needed; "" is
// the node for routes with no hosts.
func (r *Router[T]) nodeFor(h string) *node[T] {
	if h == "" {
		if r.any == nil {
			r.any = newNode[T]()
		}
		return r.any
	}
	m := r.exact
	h = strings.ToLower(strings.TrimSuffix(strings.Trim(h, "[]"), "."))
	if suffix, ok := strings.CutPrefix(h, "*"); ok {
		m, h = r.wild, suffix
	}
	n := m[h]
	if n == nil {
		n = newNode[T]()
		m[h] = n
	}
	return n
}

func (r *Router[T]) nodes() []*node[T] {
	var ns []*node[T]
	for _, n := range r.exact {
		ns = append(ns, n)
	}
	for _, n := range r.wild {
		ns = append(ns, n)
	}
	if r.any != nil {
		ns = append(ns, r.any)
	}
	return ns
}

func newNode[T any]() *node[T] {
	return &node[T]{exact: map[string]entry[T]{}, prefix: map[string]entry[T]{}, maxPrefix: math.MinInt}
}

// add adds e under path pattern p, unless the node already has an entry for
// p that always beats it.
func (n *node[T]) add(p string, e entry[T]) {
	m := n.exact
	e.path = pathExact
	if prefix, ok := strings.CutSuffix(p, "*"); ok {
		m, p, e.path = n.prefix, prefix, len(prefix)
	}
	if old, ok := m[p]; ok && !better(e, hostAny, old, hostAny) {
		return
	}
	m[p] = e
}

func (n *node[T]) finish() {
	for p, e := range n.prefix {
		n.lens = append(n.lens, len(p))
		n.maxPrefix = max(n.maxPrefix, e.priority)
	}
	slices.Sort(n.lens)
	n.lens = slices.Compact(n.lens)
	slices.Reverse(n.lens)
}

// match returns the node's best entry for path.
func (n *node[T]) match(path string) entry[T] {
	best := n.exact[path]
	for _, l := range n.lens {
		if best.ok && best.priority >= n.maxPrefix {
			// No prefix left can beat it: the ones still to try are shorter.
			break
		}
		if l > len(path) {
			continue
		}
		if e, ok := n.prefix[path[:l]]; ok && (!best.ok || better(e, hostAny, best, hostAny)) {
			best = e
		}
	}
	return best
}

// better reports whether a, matched with host specificity ah, beats b,
// matched with bh.
func better[T any](a entry[T], ah int, b entry[T], bh int) bool {
	switch {
	case a.priority != b.priority:
		return a.priority > b.priority
	case ah != bh:
		return ah > bh
	case a.path != b.path:
		return a.path > b.path
	}
	return a.order < b.order
}

// maxHost bounds the hosts Match lower-cases; a DNS name is at most 253
// bytes, so a longer host can match only routes with no hosts.
const maxHost = 255

// Match returns the value of the route for host and path, and false when no
// route matches. host is a Host header's value: a port, the brackets of an
// IPv6 address, a trailing dot and case are ignored.
func (r *Router[T]) Match(host, path string) (T, bool) {
	host = hostOnly(host)
	host = strings.TrimSuffix(host, ".")

	var exact, wild *node[T]
	if !hasUpper(host) {
		exact = r.exact[host]
		if i := strings.IndexByte(host, '.'); i > 0 {
			wild = r.wild[host[i:]]
		}
	} else if len(host) <= maxHost {
		var buf [maxHost]byte
		b := buf[:len(host)]
		for i := 0; i < len(host); i++ {
			c := host[i]
			if 'A' <= c && c <= 'Z' {
				c += 'a' - 'A'
			}
			b[i] = c
		}
		exact = r.exact[string(b)]
		if i := strings.IndexByte(host, '.'); i > 0 {
			wild = r.wild[string(b[i:])]
		}
	}

	var best entry[T]
	bestHost := hostAny
	for h, n := range [...]*node[T]{hostAny: r.any, hostWild: wild, hostExact: exact} {
		if n == nil {
			continue
		}
		if e := n.match(path); e.ok && (!best.ok || better(e, h, best, bestHost)) {
			best, bestHost = e, h
		}
	}
	return best.value, best.ok
}

func hasUpper(s string) bool {
	for i := 0; i < len(s); i++ {
		if 'A' <= s[i] && s[i] <= 'Z' {
			return true
		}
	}
	return false
}

// hostOnly strips the port from a Host header, and the brackets from an
// IPv6 address, without allocating.
func hostOnly(host string) string {
	if strings.HasPrefix(host, "[") {
		if end := strings.IndexByte(host, ']'); end > 0 {
			return host[1:end]
		}
		return host
	}
	if i := strings.LastIndexByte(host, ':'); i >= 0 && strings.IndexByte(host, ':') == i {
		return host[:i]
	}
	return host
}
