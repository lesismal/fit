package upstream

import (
	"crypto/sha256"
	"encoding/hex"
	"math/rand/v2"
	stdhttp "net/http"
	"net/textproto"
	"slices"
	"strconv"
	"strings"

	"github.com/lesismal/fit/config"
)

// random takes a backend at random, each as likely as its weight. Without a
// lock: math/rand/v2's functions are safe for concurrent use.
type random struct {
	backends []*Backend
	weighted bool
}

func newRandom(backends []*Backend) *random {
	r := &random{backends: backends}
	for _, b := range backends {
		if b.Weight != 1 {
			r.weighted = true
		}
	}
	return r
}

func (r *random) Pick(*stdhttp.Request) *Backend {
	var now int64
	if !r.weighted {
		n := len(r.backends)
		first := rand.IntN(n)
		for i := range n {
			if b := r.backends[(first+i)%n]; b.up(&now) {
				return b
			}
		}
		return r.backends[first]
	}
	if b := r.pickWeighted(&now, true); b != nil {
		return b
	}
	return r.pickWeighted(&now, false)
}

// pickWeighted draws by weight over the backends that are up or, without
// healthy, over all of them. It returns nil when none is up.
func (r *random) pickWeighted(now *int64, healthy bool) *Backend {
	total := 0
	for _, b := range r.backends {
		if !healthy || b.up(now) {
			total += b.Weight
		}
	}
	if total == 0 {
		return nil
	}
	n := rand.IntN(total)
	for _, b := range r.backends {
		if healthy && !b.up(now) {
			continue
		}
		if n -= b.Weight; n < 0 {
			return b
		}
	}
	return nil
}

// p2c is the power of two choices: of two backends drawn at random, the one
// with fewer requests under way for its weight. It spreads load nearly as
// well as least_conn without reading every backend's count, and without
// sending a burst to the one that happens to be least busy.
type p2c struct {
	backends []*Backend
}

func (p *p2c) Pick(*stdhttp.Request) *Backend {
	n := len(p.backends)
	if n == 1 {
		return p.backends[0]
	}
	i, j := rand.IntN(n), rand.IntN(n-1)
	if j >= i {
		j++
	}
	var now int64
	// A choice that is down is drawn again from those up, other than the
	// other choice; when none is up, health is ignored.
	iUp, jUp := p.backends[i].up(&now), p.backends[j].up(&now)
	if !iUp {
		if k := p.draw(&now, j); k >= 0 {
			i, iUp = k, true
		}
	}
	if !jUp {
		if k := p.draw(&now, i); k >= 0 {
			j, jUp = k, true
		}
	}
	switch {
	case iUp && !jUp:
		return p.backends[i]
	case jUp && !iUp:
		return p.backends[j]
	}
	return p.lessBusy(p.backends[i], p.backends[j])
}

// draw returns a backend that is up, other than not, at random: the first
// up from a random start. It returns -1 when there is none.
func (p *p2c) draw(now *int64, not int) int {
	n := len(p.backends)
	first := rand.IntN(n)
	for i := range n {
		k := (first + i) % n
		if k != not && p.backends[k].up(now) {
			return k
		}
	}
	return -1
}

func (p *p2c) lessBusy(a, b *Backend) *Backend {
	// a.active/a.Weight <= b.active/b.Weight, without dividing.
	if a.active.Load()*int64(b.Weight) <= b.active.Load()*int64(a.Weight) {
		return a
	}
	return b
}

// pointsPerWeight is how many points on the ring each unit of a backend's
// weight gets, as nginx's consistent hash does.
const pointsPerWeight = 160

// hashRing is consistent hashing: each backend has pointsPerWeight × its
// weight points on a ring of 64-bit hashes, placed by its address alone,
// and a request goes to the backend of the first point at or after its
// key's hash, or, when that backend is down, of the next point whose backend
// is up. Adding a backend adds its points and takes only the keys that land
// before them; removing one moves only its own keys.
type hashRing struct {
	policy string
	key    string // the header, canonical, or the cookie
	points []ringPoint
	// fallback takes requests without a key.
	fallback roundRobin
}

type ringPoint struct {
	hash    uint64
	backend *Backend
}

func newHash(policy, key string, backends []*Backend) *hashRing {
	h := &hashRing{policy: policy, key: key, fallback: roundRobin{backends: backends}}
	if policy == HeaderHash {
		h.key = textproto.CanonicalMIMEHeaderKey(key)
	}
	for _, b := range backends {
		for i := range pointsPerWeight * b.Weight {
			h.points = append(h.points, ringPoint{hash: hashString(b.Address + "#" + strconv.Itoa(i)), backend: b})
		}
	}
	slices.SortFunc(h.points, func(a, b ringPoint) int {
		switch {
		case a.hash < b.hash:
			return -1
		case a.hash > b.hash:
			return 1
		}
		return strings.Compare(a.backend.Address, b.backend.Address)
	})
	return h
}

func (h *hashRing) Pick(r *stdhttp.Request) *Backend {
	sum, ok := h.keyHash(r)
	if !ok {
		return h.fallback.Pick(r)
	}
	return h.lookup(sum)
}

// lookup returns the backend for a key whose hash is sum.
func (h *hashRing) lookup(sum uint64) *Backend {
	n := len(h.points)
	lo, hi := 0, n
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if h.points[mid].hash < sum {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	var now int64
	for i := range n {
		if b := h.points[(lo+i)%n].backend; b.up(&now) {
			return b
		}
	}
	return h.points[lo%n].backend
}

// keyHash returns the hash of r's key, and false when r has none.
func (h *hashRing) keyHash(r *stdhttp.Request) (uint64, bool) {
	if r == nil {
		return 0, false
	}
	var key string
	switch h.policy {
	case IPHash:
		key = hostOf(r.RemoteAddr)
	case URIHash:
		if r.URL == nil {
			// A TCP service's connection, which has no URI.
			return 0, false
		}
		s := fnvAdd(fnvOffset, r.URL.Path)
		if r.URL.RawQuery != "" {
			s = fnvAdd(fnvAdd(s, "?"), r.URL.RawQuery)
		}
		return mix(s), true
	case HeaderHash:
		if vs := r.Header[h.key]; len(vs) > 0 {
			key = vs[0]
		}
	case CookieHash:
		key = cookieValue(r.Header, h.key)
	}
	if key == "" {
		return 0, false
	}
	return hashString(key), true
}

// hostOf returns the host of "host:port", or addr when it has no port.
func hostOf(addr string) string {
	i := strings.LastIndexByte(addr, ':')
	if i < 0 || strings.IndexByte(addr[i:], ']') >= 0 {
		return addr
	}
	return strings.TrimSuffix(strings.TrimPrefix(addr[:i], "["), "]")
}

// cookieValue returns the value of the cookie name in h's Cookie fields, or
// "", without allocating.
func cookieValue(h stdhttp.Header, name string) string {
	for _, line := range h["Cookie"] {
		for line != "" {
			var part string
			part, line, _ = strings.Cut(line, ";")
			k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
			if ok && k == name {
				return strings.Trim(v, `"`)
			}
		}
	}
	return ""
}

// FNV-1a, 64 bits, finished with MurmurHash3's mix so that keys that differ
// in a byte or two land far apart on the ring.
const (
	fnvOffset = 14695981039346656037
	fnvPrime  = 1099511628211
)

func fnvAdd(h uint64, s string) uint64 {
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= fnvPrime
	}
	return h
}

func mix(h uint64) uint64 {
	h ^= h >> 33
	h *= 0xff51afd7ed558ccd
	h ^= h >> 33
	h *= 0xc4ceb9fe1a85ec53
	h ^= h >> 33
	return h
}

func hashString(s string) uint64 { return mix(fnvAdd(fnvOffset, s)) }

// sticky is an upstream's sticky cookie: the backend each value names, and
// the Set-Cookie field that names each backend, made once.
type sticky struct {
	name      string
	byID      map[string]*Backend
	ids       map[*Backend]string
	setCookie map[*Backend]string
}

func newSticky(c *config.StickyCookie, backends []*Backend) *sticky {
	s := &sticky{name: c.Name, byID: map[string]*Backend{}, ids: map[*Backend]string{}, setCookie: map[*Backend]string{}}
	if s.name == "" {
		s.name = config.DefaultStickyCookieName
	}
	path := c.Path
	if path == "" {
		path = "/"
	}
	attrs := "; Path=" + path
	if c.MaxAge > 0 {
		attrs += "; Max-Age=" + strconv.FormatInt(int64(c.MaxAge.Std().Seconds()), 10)
	}
	attrs += "; HttpOnly; SameSite=Lax"
	if c.Secure {
		attrs += "; Secure"
	}
	for _, b := range backends {
		id := StickyID(b.Address)
		s.byID[id] = b
		s.ids[b] = id
		s.setCookie[b] = s.name + "=" + id + attrs
	}
	return s
}

// StickyID is the sticky cookie's value for the backend at address: the
// first 64 bits of its SHA-256, in hex.
func StickyID(address string) string {
	sum := sha256.Sum256([]byte(address))
	return hex.EncodeToString(sum[:8])
}

// pick returns the backend r's cookie names, if it is up.
func (s *sticky) pick(r *stdhttp.Request) *Backend {
	if r == nil {
		return nil
	}
	b := s.byID[cookieValue(r.Header, s.name)]
	if b == nil {
		return nil
	}
	var now int64
	if !b.up(&now) {
		return nil
	}
	return b
}
