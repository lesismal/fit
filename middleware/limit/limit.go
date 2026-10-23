// Package limit has the middleware that bound what a route takes:
// rate_limit, requests a second by token bucket, keyed by the client's
// address, a header or the route; concurrency_limit, requests under way at
// once; and body_limit, the size of a request's body.
package limit

import (
	"hash/maphash"
	"math"
	stdhttp "net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/module"
	"github.com/lesismal/fit/router"
)

func init() {
	module.Register(module.Info{ID: module.Middlewares + ".rate_limit", New: func() any { return new(Rate) }, Doc: "Limits the rate of requests, by client address, header field or route, allowing a burst."})
	module.Register(module.Info{ID: module.Middlewares + ".concurrency_limit", New: func() any { return new(Concurrency) }, Doc: "Limits how many of a route's requests are under way at once."})
	module.Register(module.Info{ID: module.Middlewares + ".body_limit", New: func() any { return new(Body) }, Doc: "Limits the size of request bodies, answering 413 past it."})
}

// Rate limits how fast requests may come, with a token bucket for each key:
// a bucket holds up to burst tokens, refills at rate tokens a second, and a
// request takes one, or is answered 429 Too Many Requests, with Retry-After,
// when there is none. The buckets are kept in memory, in shards with a lock
// each, at most max_keys of them: a new key past that evicts an old one,
// one whose bucket is full if it finds one among a few it looks at, which
// costs nothing, and otherwise one that is not, which forgets what that key
// had taken.
type Rate struct {
	Rate           float64  `json:"rate" fit:"required" doc:"How many requests a second each key may make, on average; at least 0.001."`
	Burst          int      `json:"burst,omitempty" min:"0" doc:"How many requests a key may make at once, after it has been idle: the bucket's size. The rate, rounded up, when left out."`
	Key            string   `json:"key,omitempty" default:"\"ip\"" doc:"What requests are counted by: ip, the client's address; route, every request of the route together; or header:Name, a header's value, with a request without it counted by ip. ip when left out."`
	TrustedProxies []string `json:"trusted_proxies,omitempty" doc:"The addresses (\"10.0.0.1\") and networks (\"10.0.0.0/8\") of proxies in front of fit: for a request that comes from one, the client's address is the last in X-Forwarded-For that is not one of them. None when left out, and the client is the connection's peer."`
	MaxKeys        int      `json:"max_keys,omitempty" min:"0" default:"100000" doc:"The most keys kept at once, which bounds the memory the buckets take: under 100 bytes each, and up to 128 more for a header; 100000 when left out or 0."`
	Status         int      `json:"status,omitempty" default:"429" doc:"The status of a request refused; 429 when left out."`

	clock    func() int64 // nanoseconds; tests set it
	buckets  *buckets
	header   string // canonical, with key header:Name
	perRoute bool
	trusted  []netip.Prefix
}

// shards is how many shards the buckets are kept in.
const shards = 64

// buckets are the token buckets, by key.
type buckets struct {
	shards [shards]bucketShard
	// rate is in nanotokens a nanosecond (a token is 1e9 nanotokens, so
	// that refills add up exactly), burst in nanotokens.
	rate        float64
	burst       int64
	maxPerShard int
}

type bucketShard struct {
	mu sync.Mutex
	m  map[bucketKey]bucket
	_  [40]byte // keep shards on cache lines of their own
}

// bucketKey is a client's address, a header's value, or the hash of a
// longer value than maxKeyLen; the address keeps keys by address from
// allocating.
type bucketKey struct {
	addr netip.Addr
	s    string
	h    uint64
}

// maxKeyLen bounds the header values kept as keys, so that what the buckets
// keep stays bounded too; a longer one is kept by its hash.
const maxKeyLen = 128

// keySeed seeds the hash of long keys, at random, so that a client cannot
// choose values that collide.
var keySeed = maphash.MakeSeed()

type bucket struct {
	tokens int64 // nanotokens
	last   int64 // when tokens was last brought up to date
}

// token is a token in nanotokens.
const token = 1e9

// Provision checks the params and makes the buckets.
func (rl *Rate) Provision(ctx *module.Context) error {
	if rl.Rate < 0.001 {
		return ctx.Errorf("rate", "%v is below 0.001", rl.Rate)
	}
	burst := rl.Burst
	if burst == 0 {
		burst = int(rl.Rate + 0.999)
	}
	switch key := rl.Key; {
	case key == "" || key == "ip":
	case key == "route":
		rl.perRoute = true
	case strings.HasPrefix(key, "header:") && validName(key[len("header:"):]):
		rl.header = stdhttp.CanonicalHeaderKey(key[len("header:"):])
	default:
		return ctx.Errorf("key", "%q is not ip, route or header:Name", key)
	}
	for i, s := range rl.TrustedProxies {
		p, err := router.ParsePrefix(s)
		if err != nil {
			return ctx.Params.Field("trusted_proxies").Index(i).Errorf("%v", err)
		}
		rl.trusted = append(rl.trusted, p)
	}
	if rl.Status == 0 {
		rl.Status = stdhttp.StatusTooManyRequests
	} else if rl.Status < 400 || rl.Status > 599 {
		return ctx.Errorf("status", "%d is not an error status", rl.Status)
	}
	maxKeys := rl.MaxKeys
	if maxKeys == 0 {
		maxKeys = 100000
	}
	b := &buckets{rate: rl.Rate, burst: int64(burst) * token, maxPerShard: max(1, maxKeys/shards)}
	for i := range b.shards {
		b.shards[i].m = make(map[bucketKey]bucket)
	}
	rl.buckets = b
	if rl.clock == nil {
		start := time.Now()
		rl.clock = func() int64 { return int64(time.Since(start)) }
	}
	return nil
}

// Wrap returns next behind the limit.
func (rl *Rate) Wrap(next module.Handler) module.Handler {
	return fibhttp.HandlerFunc(func(c *fibhttp.Context) {
		wait, ok := rl.buckets.take(rl.key(c.Request), rl.clock())
		if !ok {
			// Retry-After is in whole seconds, rounded up.
			secs := (wait + int64(time.Second) - 1) / int64(time.Second)
			_ = c.WriteResponse(fibhttp.Response{
				StatusCode: rl.Status,
				Header:     stdhttp.Header{"Retry-After": {strconv.FormatInt(secs, 10)}},
			})
			return
		}
		next.ServeHTTP(c)
	})
}

// key returns what r is counted by.
func (rl *Rate) key(r *stdhttp.Request) bucketKey {
	if rl.perRoute {
		return bucketKey{}
	}
	if rl.header != "" {
		if v := r.Header[rl.header]; len(v) > 0 {
			if len(v[0]) > maxKeyLen {
				return bucketKey{h: maphash.String(keySeed, v[0])}
			}
			return bucketKey{s: v[0]}
		}
	}
	return bucketKey{addr: clientIP(r, rl.trusted)}
}

// take takes a token from key's bucket at now, in nanoseconds. When there is
// none, it returns how long until there is.
func (b *buckets) take(key bucketKey, now int64) (wait int64, ok bool) {
	sh := &b.shards[key.hash()%shards]
	sh.mu.Lock()
	defer sh.mu.Unlock()
	bk, found := sh.m[key]
	if !found {
		if len(sh.m) >= b.maxPerShard {
			b.evict(sh, now)
		}
		bk = bucket{tokens: b.burst, last: now}
	}
	bk.tokens = b.refill(bk, now)
	bk.last = now
	if bk.tokens >= token {
		bk.tokens -= token
		ok = true
	} else {
		wait = int64(math.Ceil(float64(token-bk.tokens) / b.rate))
	}
	sh.m[key] = bk
	return wait, ok
}

// refill returns bk's tokens at now, up to the burst.
func (b *buckets) refill(bk bucket, now int64) int64 {
	add := float64(now-bk.last) * b.rate
	if add >= float64(b.burst-bk.tokens) {
		return b.burst
	}
	return bk.tokens + int64(add)
}

// evictLooks is how many keys evict looks at for one whose bucket is full.
const evictLooks = 8

// evict removes a key from sh: the first of a few whose bucket is full by
// now, or else the first of them. Go's maps start each range at random.
func (b *buckets) evict(sh *bucketShard, now int64) {
	var victim bucketKey
	n := 0
	for k, bk := range sh.m {
		if n == 0 {
			victim = k
		}
		if b.refill(bk, now) == b.burst {
			victim = k
			break
		}
		if n++; n == evictLooks {
			break
		}
	}
	delete(sh.m, victim)
}

// hash spreads keys over the shards: FNV-1a over the address's bytes or the
// string.
func (k bucketKey) hash() uint32 {
	h := uint32(2166136261)
	if k.h != 0 {
		return uint32(k.h)
	}
	if k.s != "" {
		for i := 0; i < len(k.s); i++ {
			h = (h ^ uint32(k.s[i])) * 16777619
		}
		return h
	}
	a := k.addr.As16()
	for _, c := range a {
		h = (h ^ uint32(c)) * 16777619
	}
	return h
}

// clientIP returns the client's address: the connection's peer, or, when
// that is a trusted proxy, the last address in X-Forwarded-For that is not
// one. It does not allocate.
func clientIP(r *stdhttp.Request, trusted []netip.Prefix) netip.Addr {
	ip := peer(r.RemoteAddr)
	if len(trusted) == 0 || !isTrusted(ip, trusted) {
		return ip
	}
	xff := r.Header["X-Forwarded-For"]
	for i := len(xff) - 1; i >= 0; i-- {
		list := xff[i]
		for list != "" {
			j := strings.LastIndexByte(list, ',')
			item := strings.TrimSpace(list[j+1:])
			list = list[:max(j, 0)]
			a, err := netip.ParseAddr(item)
			if err != nil {
				return ip // a list that does not parse is not believed past here
			}
			if a = a.Unmap().WithZone(""); !isTrusted(a, trusted) {
				return a
			}
			ip = a
		}
	}
	return ip
}

func isTrusted(ip netip.Addr, trusted []netip.Prefix) bool {
	for _, p := range trusted {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// peer parses the address of a RemoteAddr, "ip:port" or a bare IP; it is
// the zero Addr when there is none, which is a key like any other.
func peer(s string) netip.Addr {
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap().WithZone("")
	}
	a, _ := netip.ParseAddr(s)
	return a.Unmap().WithZone("")
}

// Concurrency limits how many of the route's requests are under way at
// once; one past it is answered at once, 429 Too Many Requests by default,
// rather than kept waiting. A request is under way until its response is
// sent, or its client has gone.
type Concurrency struct {
	Max    int `json:"max" fit:"required" min:"1" doc:"The most requests under way at once."`
	Status int `json:"status,omitempty" default:"429" doc:"The status of a request refused; 429 when left out, or 503 as nginx's limit_conn answers."`

	inFlight atomic.Int64
}

// Provision checks the params.
func (cl *Concurrency) Provision(ctx *module.Context) error {
	if cl.Status == 0 {
		cl.Status = stdhttp.StatusTooManyRequests
	} else if cl.Status < 400 || cl.Status > 599 {
		return ctx.Errorf("status", "%d is not an error status", cl.Status)
	}
	return nil
}

// InFlight returns how many requests are under way.
func (cl *Concurrency) InFlight() int { return int(cl.inFlight.Load()) }

// Wrap returns next behind the limit.
func (cl *Concurrency) Wrap(next module.Handler) module.Handler {
	return fibhttp.HandlerFunc(func(c *fibhttp.Context) {
		if cl.inFlight.Add(1) > int64(cl.Max) {
			cl.inFlight.Add(-1)
			_ = c.Respond(cl.Status, "", nil)
			return
		}
		// The request ends one of three ways: the handler returns without
		// retaining it, which settles it; its response is sent; or its
		// client goes first. OnFinish and OnCancel are set before the
		// handler runs, since one that answers elsewhere may be done before
		// it returns; fib runs the handler's OnCancel before this one.
		// Whichever comes first gives the place back.
		s := &slot{cl: cl}
		c.OnFinish(s.finish)
		c.OnCancel(s.cancel)
		next.ServeHTTP(c)
		if !c.Retained() {
			s.release()
		}
	})
}

// slot is one request's place under the limit.
type slot struct {
	cl   *Concurrency
	done atomic.Bool
}

// release gives the place back, once.
func (s *slot) release() {
	if s.done.CompareAndSwap(false, true) {
		s.cl.inFlight.Add(-1)
	}
}

// finish gives the place back once the response is sent.
func (s *slot) finish(int, stdhttp.Header, int64) { s.release() }

// cancel gives the place back once the client has gone.
func (s *slot) cancel(error) { s.release() }

// Body limits the size of a request's body: one whose Content-Length is
// past the limit, or that has brought more already, is answered 413 Content
// Too Large before the handler sees it, and one of no declared length that
// grows past it is ended with fib's ErrBodyTooLarge, which the handler's
// OnBody sees, and answered 413 unless the handler has begun a response.
type Body struct {
	MaxBytes int64 `json:"max_bytes" fit:"required" min:"0" doc:"The most bytes a request's body may have."`
}

// Wrap returns next behind the limit.
func (bl *Body) Wrap(next module.Handler) module.Handler {
	return fibhttp.HandlerFunc(func(c *fibhttp.Context) {
		if c.LimitBody(bl.MaxBytes) != nil {
			return // fib has answered
		}
		next.ServeHTTP(c)
	})
}

// validName reports whether name is an HTTP token.
func validName(name string) bool {
	if name == "" {
		return false
	}
	for i := range len(name) {
		c := name[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			continue
		}
		if !strings.ContainsRune("!#$%&'*+-.^_`|~", rune(c)) {
			return false
		}
	}
	return true
}
