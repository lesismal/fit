// Package cache has the cache middleware, an HTTP cache shared by a route's
// clients, as RFC 9111 has a shared cache work: it keeps the responses it
// may, for as long as they are fresh, answers from them, with the right Age,
// for every variant their Vary names, and answers conditional requests from
// them itself.
//
// A response is kept as it goes to the client, through a fib body filter
// that copies what passes, so that a response is never held back to be
// stored, and is stored only once all of it has gone. Responses are kept in
// memory, in a store of a bounded size shared by every cache of the same
// zone and kept across reloads, and, with a disk tier, on disk too, where
// they are found again when fit starts: the disk is read and written on a
// pool of goroutines of its own, and never by a fib worker.
package cache

import (
	"maps"
	stdhttp "net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
)

func init() {
	module.Register(module.Info{ID: module.Middlewares + ".cache", New: func() any { return new(Cache) }})
}

// clock is the time, which tests move.
var clock = time.Now

// Defaults of Cache's params.
const (
	DefaultZone         = "default"
	DefaultMaxSize      = 128 << 20
	DefaultMaxEntrySize = 1 << 20
)

// Cache answers requests from the responses it keeps. It keeps the responses
// to GET requests that a shared cache may keep: ones whose status it
// understands, that do not say no-store, private or no-cache, do not vary on
// everything (Vary: *), set no cookie, carry no trailers and are no larger
// than max_entry_size, with a freshness lifetime from s-maxage, max-age or
// Expires, or, for the statuses that allow one, from default_ttl or a tenth
// of the time since Last-Modified. A request with Authorization has its
// response kept only when it says public, s-maxage or must-revalidate.
//
// A fresh response answers GET and HEAD requests, as a 304 for one whose
// If-None-Match or If-Modified-Since it meets. A request's no-cache, max-age
// and min-fresh send it on, and its max-stale lets it have a stale response
// that allows it; only-if-cached gets 504 when nothing is kept. A request
// with a Range goes on. A request that changes something (POST, PUT, DELETE
// and the like) and succeeds drops what is kept for its URI, and for its
// Location and Content-Location on the same host. Every answer to GET and
// HEAD says what the cache did in Cache-Status (RFC 9211).
type Cache struct {
	Zone         string          `json:"zone,omitempty" default:"\"default\"" doc:"The store responses are kept in. Caches of the same zone, in one route or several, share it, its size and its keys; it is kept across reloads while a cache uses it. default when left out."`
	MaxSize      int64           `json:"max_size,omitempty" min:"0" default:"134217728" doc:"The most bytes the zone keeps in memory, bodies, fields and keys counted; past it the least recently used responses go. 128 MiB when left out or 0. Caches of one zone take the size of the last one loaded."`
	MaxEntrySize int64           `json:"max_entry_size,omitempty" min:"0" default:"1048576" doc:"The largest body kept; a larger response goes to its client without being kept. At most a sixteenth of max_size. 1 MiB when left out or 0."`
	DefaultTTL   config.Duration `json:"default_ttl,omitempty" doc:"How long a response is fresh for when it gives no lifetime of its own (no s-maxage, max-age or Expires) and its status allows one to be assumed (200, 203, 300, 301, 308, 404, 405, 410, 414, 501): in place of a tenth of the time since its Last-Modified, at most a day, which is used when this is left out."`
	Disk         *Disk           `json:"disk,omitempty" doc:"A disk tier for the zone: every response kept in memory is written to disk too, on a pool of goroutines of its own, and one the memory no longer holds is read back from disk, on that pool, and kept in memory again. None when left out. Caches of one zone take the disk of the last one loaded."`

	store *store
	graph module.Holder
}

// Provision checks the params and takes the zone's store.
func (m *Cache) Provision(ctx *module.Context) error {
	if m.Zone == "" {
		m.Zone = DefaultZone
	}
	if m.MaxSize == 0 {
		m.MaxSize = DefaultMaxSize
	}
	if m.MaxEntrySize == 0 {
		m.MaxEntrySize = min(DefaultMaxEntrySize, m.MaxSize/shards)
	}
	if m.MaxEntrySize > m.MaxSize/shards {
		return ctx.Errorf("max_entry_size", "is more than a sixteenth of max_size (%d)", m.MaxSize/shards)
	}
	store, err := acquire(m.Zone, m.MaxSize, m.Disk)
	if err != nil {
		return ctx.Errorf("disk", "%v", err)
	}
	m.store, m.graph = store, ctx.Graph()
	return nil
}

// Cleanup gives the zone's store back.
func (m *Cache) Cleanup() {
	if m.store != nil {
		m.store.release()
	}
}

// What the cache did with a request, as Cache-Status says it.
const (
	fwdMiss = iota
	fwdVaryMiss
	fwdStale
	fwdRequest
	fwdBypass
	fwdKinds
)

var fwdParams = [fwdKinds]string{"uri-miss", "vary-miss", "stale", "request", "bypass"}

// statusFields are the Cache-Status members of requests sent on, without
// and with "stored".
var statusFields = func() (f [fwdKinds][2]string) {
	for i, p := range fwdParams {
		f[i] = [2]string{"fit; fwd=" + p, "fit; fwd=" + p + "; stored"}
	}
	return f
}()

// Wrap returns next behind the cache.
func (m *Cache) Wrap(next module.Handler) module.Handler {
	return fibhttp.HandlerFunc(func(c *fibhttp.Context) { m.serve(c, next) })
}

func (m *Cache) serve(c *fibhttp.Context, next module.Handler) {
	r := c.Request
	if r.Method != stdhttp.MethodGet && r.Method != stdhttp.MethodHead {
		if unsafeMethod(r.Method) {
			c.OnHeader(func(status int, h stdhttp.Header) {
				if status >= 200 && status < 400 {
					m.invalidate(r, h)
				}
			})
		}
		next.ServeHTTP(c)
		return
	}
	key := keyOf(r, r.URL.RequestURI())
	now := clock()
	req := requestDirectives(r.Header)
	x := &miss{m: m, c: c, key: key, requestTime: now, status: fwdMiss, req: req}
	if _, ranged := r.Header["Range"]; ranged {
		x.status, x.ranged = fwdBypass, true
	} else if e, found := m.store.get(key, r.Header); e != nil {
		hit, status := usable(e, req, now)
		if hit {
			m.hit(c, e, e.age(now), "")
			return
		}
		x.status, x.stale = status, e
	} else {
		if found {
			x.status = fwdVaryMiss
		}
		if d := m.store.disk.Load(); d != nil {
			if de := d.get(key, r.Header); de != nil {
				hit, status := usable(de.meta, req, now)
				if hit {
					m.fromDisk(c, next, d, de, x)
					return
				}
				x.status = status
			}
		}
	}
	x.forward(next)
}

// usable reports whether e may answer a request with directives req at now,
// or else why the request goes on.
func usable(e *entry, req directives, now time.Time) (bool, int) {
	age := e.age(now)
	fresh := age < e.lifetime
	switch {
	case req.noCache || req.maxAge >= 0 && age > req.maxAge:
		return false, fwdRequest
	case fresh && (req.minFresh < 0 || e.lifetime-age >= req.minFresh):
		return true, 0
	case fresh:
		return false, fwdRequest
	case !e.noStale && (req.maxStaleAny || req.maxStale >= 0 && age-e.lifetime <= req.maxStale):
		return true, 0
	}
	return false, fwdStale
}

// forward sends the request on to next, with the hooks that keep its
// response, or answers 504 for one that is only to be answered from the
// cache.
func (x *miss) forward(next module.Handler) {
	c, r := x.c, x.c.Request
	if x.req.onlyIfCached {
		_ = c.Respond(stdhttp.StatusGatewayTimeout, "text/plain; charset=utf-8", []byte("504 Gateway Timeout\n"))
		return
	}
	x.storable = r.Method == stdhttp.MethodGet && !x.req.noStore
	_, x.authorized = r.Header["Authorization"]
	// The hooks run in the reverse of this order: the header's first.
	c.OnBodyFilter(x.filter)
	c.OnHeader(x.header)
	next.ServeHTTP(c)
}

// fromDisk answers c from de, read back from d on the disk pool, and keeps
// it in memory again. A read that fails, or does not come within the
// pool's timeout, as from a disk that hangs, leaves the request to go on as
// a miss, from wherever it is.
func (m *Cache) fromDisk(c *fibhttp.Context, next module.Handler, d *disk, de *diskEntry, x *miss) {
	c.Retain()
	m.graph.Retain()
	release := func() {
		c.Release()
		m.graph.Release()
	}
	// claimed says whether the read (1) or the timeout (2) answers.
	var claimed atomic.Int32
	miss := func() {
		x.forward(next)
		release()
	}
	timer := time.AfterFunc(d.pool.Timeout(), func() {
		if claimed.CompareAndSwap(0, 2) {
			miss()
		}
	})
	run := func() {
		if c.Err() != nil {
			if claimed.CompareAndSwap(0, 1) {
				timer.Stop()
				release()
			}
			return
		}
		e, err := d.read(de)
		if !claimed.CompareAndSwap(0, 1) {
			return
		}
		timer.Stop()
		if err != nil {
			d.drop(x.key, de)
			miss()
			return
		}
		m.store.put(x.key, e)
		m.hit(c, e, e.age(clock()), "disk")
		release()
	}
	expire := func() {
		if claimed.CompareAndSwap(0, 2) {
			timer.Stop()
			miss()
		}
	}
	if !d.pool.Submit(run, expire) && claimed.CompareAndSwap(0, 2) {
		timer.Stop()
		miss()
	}
}

// keyOf returns the key of a request's response: its scheme, host and
// target.
func keyOf(r *stdhttp.Request, target string) string {
	scheme := "http://"
	if r.TLS != nil {
		scheme = "https://"
	}
	return scheme + strings.ToLower(r.Host) + target
}

// unsafeMethod reports whether a request with method may change what its
// target holds, so that its success makes stored responses out of date.
func unsafeMethod(method string) bool {
	switch method {
	case stdhttp.MethodGet, stdhttp.MethodHead, stdhttp.MethodOptions, stdhttp.MethodTrace:
		return false
	}
	return true
}

// invalidate drops what is stored for r's target, and for the Location and
// Content-Location of its response h when they are on the same host (RFC
// 9111 section 4.4).
func (m *Cache) invalidate(r *stdhttp.Request, h stdhttp.Header) {
	m.store.remove(keyOf(r, r.URL.RequestURI()))
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	base := &url.URL{Scheme: scheme, Host: r.Host, Path: r.URL.Path}
	for _, field := range [...]string{"Location", "Content-Location"} {
		v := h.Get(field)
		if v == "" {
			continue
		}
		u, err := base.Parse(v)
		if err == nil && u.Scheme == scheme && strings.EqualFold(u.Host, r.Host) {
			m.store.remove(keyOf(r, u.RequestURI()))
		}
	}
}

// hit answers c from e, which is age old; detail, if any, says where e was.
func (m *Cache) hit(c *fibhttp.Context, e *entry, age time.Duration, detail string) {
	h := make(stdhttp.Header, len(e.header)+2)
	status, body := e.status, e.body
	if status == stdhttp.StatusOK && notModified(c.Request.Header, e.etag, e.lastModified) {
		for _, k := range notModifiedFields {
			if v, ok := e.header[k]; ok {
				h[k] = v
			}
		}
		status, body = stdhttp.StatusNotModified, nil
	} else {
		maps.Copy(h, e.header)
	}
	h["Age"] = []string{strconv.FormatInt(int64(age/time.Second), 10)}
	cs := "fit; hit; ttl=" + strconv.FormatInt(int64((e.lifetime-age)/time.Second), 10)
	if detail != "" {
		cs += "; detail=" + detail
	}
	h["Cache-Status"] = []string{cs}
	_ = c.WriteResponse(fibhttp.Response{StatusCode: status, Header: h, Body: body})
}

// miss is a request the cache sent on, and what it does with its response.
type miss struct {
	m           *Cache
	c           *fibhttp.Context
	key         string
	requestTime time.Time
	// status is what Cache-Status says, one of the fwd kinds, and stored
	// whether the response is being kept.
	status int
	stored bool
	// stale is the stored response the request did not take, freshened if
	// the 304 it is answered with is for it.
	stale *entry
	// storable is false when the request rules out keeping its response,
	// authorized when it carries Authorization, and ranged when it asked for
	// a range.
	storable, authorized, ranged bool
	// req are the request's directives.
	req directives
}

// header runs as the response's header is settled: it says what the cache
// did, and freshens the stale response a 304 is for.
func (x *miss) header(status int, h stdhttp.Header) {
	if status == stdhttp.StatusNotModified && x.stale != nil {
		x.freshen(h)
	}
	h["Cache-Status"] = append(h["Cache-Status"], statusFields[x.status][0])
}

// freshen stores the stale response again, its fields updated from the 304
// h, if h is for it: their ETags, or else their Last-Modified, are the same.
func (x *miss) freshen(h stdhttp.Header) {
	old := x.stale
	if tag := h.Get("Etag"); tag != "" {
		if old.etag == "" || !weakEqual(tag, old.etag) {
			return
		}
	} else if lm := h.Get("Last-Modified"); lm != old.lastModified {
		return
	}
	header := old.header.Clone()
	for k, v := range h {
		if !unstored[k] && !updateExcluded[k] {
			header[k] = slices.Clip(slices.Clone(v))
		}
	}
	e, ok := x.m.entryOf(old.status, header, x.c.Request.Header, x.requestTime, h.Get("Age"), x.authorized)
	if !ok {
		x.m.store.remove(x.key)
		return
	}
	e.body = old.body
	x.m.store.replace(x.key, old, e)
}

// filter runs once the response's header is settled, for a response with
// a body: it decides whether to keep the response, and returns what copies
// its body if so.
func (x *miss) filter(status int, h stdhttp.Header) fibhttp.BodyFilter {
	if !x.storable || x.c.Request.Method != stdhttp.MethodGet {
		return nil
	}
	if x.ranged && (status != stdhttp.StatusOK || h["Content-Range"] != nil) {
		return nil
	}
	want := int64(-1)
	if v := h.Get("Content-Length"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n > x.m.MaxEntrySize {
			return nil
		}
		want = n
	}
	header := make(stdhttp.Header, len(h))
	for k, v := range h {
		if !unstored[k] {
			header[k] = slices.Clip(slices.Clone(v))
		}
	}
	e, ok := x.m.entryOf(status, header, x.c.Request.Header, x.requestTime, h.Get("Age"), x.authorized)
	if !ok {
		return nil
	}
	if cs := h["Cache-Status"]; len(cs) > 0 && cs[len(cs)-1] == statusFields[x.status][0] {
		cs[len(cs)-1] = statusFields[x.status][1]
	}
	f := &capture{store: x.m.store, c: x.c, key: x.key, e: e, want: want, limit: x.m.MaxEntrySize}
	if want > 0 {
		f.buf = make([]byte, 0, want)
	}
	return f
}

// entryOf makes the entry of a response with status, header, which it
// keeps, and the Age field age, to a request with reqHeader sent at
// requestTime, or reports that a shared cache must not keep it.
func (m *Cache) entryOf(status int, header, reqHeader stdhttp.Header, requestTime time.Time, age string, authorized bool) (*entry, bool) {
	d := parseDirectives(header["Cache-Control"])
	if d.noStore || d.private || d.noCache {
		return nil, false
	}
	if authorized && !d.public && d.sMaxAge < 0 && !d.mustRevalidate {
		return nil, false
	}
	for _, field := range [...]string{"Set-Cookie", "Trailer", "Content-Range"} {
		if _, ok := header[field]; ok {
			return nil, false
		}
	}
	names, ok := varyNames(header)
	if !ok {
		return nil, false
	}
	now := clock()
	date, err := stdhttp.ParseTime(header.Get("Date"))
	if err != nil {
		date = now
	}
	lifetime, explicit := freshness(header, d, status, date, m.DefaultTTL.Std())
	if !storableStatus(status, explicit) {
		return nil, false
	}
	var ageValue time.Duration
	if age != "" {
		ageValue = delta(age)
	}
	initialAge := max(max(now.Sub(date), 0), ageValue+max(now.Sub(requestTime), 0))
	if initialAge >= lifetime {
		return nil, false
	}
	e := &entry{
		status: status, header: header,
		varyNames: names, varyValues: make([]string, len(names)),
		responseTime: now, initialAge: initialAge, lifetime: lifetime,
		noStale: d.mustRevalidate || d.proxyRevalidate || d.sMaxAge >= 0,
		etag:    header.Get("Etag"), lastModified: header.Get("Last-Modified"),
	}
	for i, name := range names {
		e.varyValues[i] = varyValue(reqHeader, name)
	}
	return e, true
}

// capture copies a response's body as it goes, and stores the response once
// all of it has gone. fib calls its methods one at a time, and ends it with
// Close, or with Abort when the response ends without its body ending.
type capture struct {
	store *store
	c     *fibhttp.Context
	key   string
	e     *entry
	// want is the declared length, or -1, and limit the most kept.
	want, limit int64
	buf         []byte
	// held is what this capture counts in its store's capturing, and failed
	// says it gave up.
	held   int64
	failed bool
}

func (f *capture) Filter(p []byte) ([]byte, error) {
	if f.failed || len(p) == 0 {
		return p, nil
	}
	n := int64(len(p))
	if int64(len(f.buf))+n > f.limit {
		f.Abort(nil)
		return p, nil
	}
	f.held += n
	if f.store.capturing.Add(n) > f.store.maxSize.Load()/4 {
		f.Abort(nil)
		return p, nil
	}
	f.buf = append(f.buf, p...)
	return p, nil
}

// Close stores the response, if all of it went: as long as it declared, and
// not cut short with an error or a trailer.
func (f *capture) Close() ([]byte, error) {
	if f.failed {
		return nil, nil
	}
	body := f.buf
	f.Abort(nil)
	if (f.want < 0 || int64(len(body)) == f.want) && f.c.Err() == nil && !hasTrailer(f.c.Header()) {
		f.e.body = body
		f.store.keep(f.key, f.e)
	}
	return nil, nil
}

// Abort gives up keeping the response, and what it held.
func (f *capture) Abort(error) {
	f.failed = true
	f.buf = nil
	f.store.capturing.Add(-f.held)
	f.held = 0
}

// hasTrailer reports whether a response written through h ends with a
// trailer, as one the proxy cuts short does.
func hasTrailer(h stdhttp.Header) bool {
	if _, ok := h["Trailer"]; ok {
		return true
	}
	for k := range h {
		if strings.HasPrefix(k, stdhttp.TrailerPrefix) {
			return true
		}
	}
	return false
}
