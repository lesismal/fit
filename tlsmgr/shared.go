package tlsmgr

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"hash/maphash"
	"io/fs"
	"net/netip"
	"regexp"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// maxSharedLookups bounds the lookups of shared challenges, HTTP-01 and
// TLS-ALPN-01 together, under way at once; one past it is answered as one
// with no challenge, at once.
const maxSharedLookups = 16

// maxClientLookups bounds the shared HTTP-01 lookups under way for one
// client, an IPv4 address or an IPv6 /64, as near as clientSlots counters
// tell clients apart, so that one client cannot take every slot.
const (
	maxClientLookups = 4
	clientSlots      = 64
)

// Challenges in progress: an instance that keeps a challenge's answer in
// the shared storage also keeps a marker there, rewritten every
// markerRefresh while it has one under way and good for markerTTL after,
// and the others look a challenge up only while the marker is good. A
// request for a challenge then costs the storage nothing while no instance
// has an order under way, which is nearly always.
const (
	markerTTL     = 2 * time.Minute
	markerRefresh = 30 * time.Second
	// maxMisses bounds the names remembered with no TLS-ALPN-01 challenge.
	maxMisses = 1024
)

// sharedLookupTimeout bounds a lookup of a shared challenge's answer;
// gateFresh is how long an instance trusts what it last read of the marker,
// and missFor how long a name found with no TLS-ALPN-01 challenge is not
// looked up again. Variables for tests.
var (
	sharedLookupTimeout = 5 * time.Second
	gateFresh           = time.Second
	missFor             = time.Second
)

// sharedSettle is how long an instance waits, once it keeps a challenge's
// answer in the shared storage, before it asks the CA to check it: past
// gateFresh and missFor, so that no instance still holds what it read
// before the answer was there.
func sharedSettle() time.Duration { return max(gateFresh, missFor) + 250*time.Millisecond }

// tokenPattern is what an HTTP-01 token looks like: base64url, as RFC 8555
// has it, so that a request's path makes no other storage key.
var tokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)

// sharedGate is what an ACME with a shared storage knows of the challenges
// other instances have in progress, and what bounds its lookups of them.
type sharedGate struct {
	// until is the marker's expiry as last read, and checked when that
	// read started, in Unix nanoseconds; checked is 0 before the first.
	until, checked atomic.Int64
	reading        atomic.Bool // a read of the marker is under way

	seed    maphash.Seed
	clients [clientSlots]atomic.Int32 // shared HTTP-01 lookups under way

	mu sync.Mutex
	// misses holds the names a lookup found no TLS-ALPN-01 challenge
	// for, with when that lookup started.
	misses map[string]int64
}

// The states of a sharedGate, as of a time.
const (
	gateClosed = iota // no challenge in progress, as read lately
	gateOpen          // a challenge may be in progress
	gateStale         // not read lately: read it before looking up
)

func (g *sharedGate) state(now int64) int {
	if now < g.until.Load() {
		return gateOpen
	}
	if c := g.checked.Load(); c != 0 && now-c < int64(gateFresh) {
		return gateClosed
	}
	return gateStale
}

// isShared reports whether s says it is shared between hosts.
func isShared(s Storage) bool {
	sh, ok := s.(interface{ Shared() bool })
	return ok && sh.Shared()
}

// Shared reports whether a's storage is shared between hosts, so that a
// challenge another instance answers may be found there.
func (a *ACME) Shared() bool { return a.shared }

func (a *ACME) httpChallengeKey(token string) string {
	return "challenges/" + a.caKey + "/http-01/" + token
}

func (a *ACME) alpnChallengeKey(name string) string {
	return "challenges/" + a.caKey + "/tls-alpn-01/" + name
}

// markerKey is the marker of challenges in progress, outside challenges/,
// which holds only challenges under way.
func (a *ACME) markerKey() string { return "acme/" + a.caKey + "/challenges-in-progress" }

// markInProgress keeps the marker of challenges in progress good until stop
// is called, or ctx is done: written now, and again every markerRefresh.
func (a *ACME) markInProgress(ctx context.Context) (stop func()) {
	a.writeMarker(ctx)
	quit, exited := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(exited)
		t := time.NewTicker(markerRefresh)
		defer t.Stop()
		for {
			select {
			case <-quit:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				a.writeMarker(ctx)
			}
		}
	}()
	return func() {
		close(quit)
		<-exited
	}
}

// writeMarker writes the marker, good for markerTTL. It is not removed once
// the challenge is done, since another instance may have one under way; it
// runs out.
func (a *ACME) writeMarker(ctx context.Context) {
	until := a.now().Add(markerTTL).UnixNano()
	if err := a.storage.Store(ctx, a.markerKey(), strconv.AppendInt(nil, until, 10)); err != nil {
		a.log.Warnf("acme: keeping the marker of challenges in progress in the shared storage: %v", err)
		return
	}
	for {
		old := a.gate.until.Load()
		if old >= until || a.gate.until.CompareAndSwap(old, until) {
			return
		}
	}
}

// readGate reads the marker of challenges in progress, unless another read
// of it is under way, and reports whether a challenge may be in progress:
// true too when another reads it, or it cannot be read, so that a challenge
// is looked up rather than missed.
func (a *ACME) readGate(ctx context.Context) bool {
	g := &a.gate
	if !g.reading.CompareAndSwap(false, true) {
		return true
	}
	defer g.reading.Store(false)
	// Stamped with when the read started, so that what it finds is
	// trusted no longer than gateFresh past a marker written meanwhile.
	start := a.now().UnixNano()
	data, err := a.storage.Load(ctx, a.markerKey())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		a.log.Warnf("acme: reading the marker of challenges in progress from the shared storage: %v", err)
		return true
	}
	if err == nil {
		if until, perr := strconv.ParseInt(string(data), 10, 64); perr == nil {
			g.until.Store(until)
		}
	}
	g.checked.Store(start)
	return start < g.until.Load()
}

// clientSlot returns the counter of client's shared HTTP-01 lookups, for
// client as a request's RemoteAddr has it, or nil for one that is no IP
// address.
func (a *ACME) clientSlot(client string) *atomic.Int32 {
	var addr netip.Addr
	if ap, err := netip.ParseAddrPort(client); err == nil {
		addr = ap.Addr()
	} else if addr, err = netip.ParseAddr(client); err != nil {
		return nil
	}
	b := addr.Unmap().As16()
	if !addr.Unmap().Is4() {
		// An IPv6 client is its /64, which one host often has whole.
		clear(b[8:])
	}
	return &a.gate.clients[maphash.Comparable(a.gate.seed, b)%clientSlots]
}

// missed reports whether a lookup that started less than missFor before now
// found no TLS-ALPN-01 challenge for name.
func (a *ACME) missed(name string, now int64) bool {
	g := &a.gate
	g.mu.Lock()
	at, ok := g.misses[name]
	g.mu.Unlock()
	return ok && now-at < int64(missFor)
}

// miss remembers that a lookup started at at found no TLS-ALPN-01 challenge
// for name.
func (a *ACME) miss(name string, at int64) {
	g := &a.gate
	g.mu.Lock()
	if g.misses == nil || len(g.misses) >= maxMisses {
		g.misses = make(map[string]int64)
	}
	g.misses[name] = at
	g.mu.Unlock()
}

// shareChallenge keeps a challenge's answer in the shared storage. A
// failure is reported, not returned: this instance can still answer it.
func (a *ACME) shareChallenge(ctx context.Context, key string, value []byte) {
	if err := a.storage.Store(ctx, key, value); err != nil {
		a.log.Warnf("acme: keeping the challenge's answer in the shared storage: %v", err)
	}
}

// unshareChallenge removes a challenge's answer, with a time limit of its
// own, since the challenge's context may be done.
func (a *ACME) unshareChallenge(key string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.storage.Delete(ctx, key); err != nil {
		a.log.Warnf("acme: removing the challenge's answer from the shared storage: %v", err)
	}
}

// LookupHTTPChallenge looks the answer to the HTTP-01 challenge of token up
// in the shared storage, for a challenge another instance has under way,
// and calls done with it, or with ok false when there is none. client is
// the request's RemoteAddr. It never blocks: the lookup runs on a goroutine
// of a's, and done is called there, or at once when the token cannot be
// one, no instance has a challenge in progress (see markerTTL), or every
// slot is taken: maxSharedLookups in all and maxClientLookups for client's
// address, each within sharedLookupTimeout. Call it only once HTTPChallenge
// found no answer, and only when Shared.
func (a *ACME) LookupHTTPChallenge(token, client string, done func(answer string, ok bool)) {
	if !a.shared || !tokenPattern.MatchString(token) {
		done("", false)
		return
	}
	state := a.gate.state(a.now().UnixNano())
	if state == gateClosed {
		done("", false)
		return
	}
	slot := a.clientSlot(client)
	if slot != nil && slot.Add(1) > maxClientLookups {
		slot.Add(-1)
		done("", false)
		return
	}
	select {
	case a.lookups <- struct{}{}:
	default:
		if slot != nil {
			slot.Add(-1)
		}
		done("", false)
		return
	}
	go func() {
		// The slots are free again before done runs, so that one whom done
		// answers may look up again at once.
		data, err := func() ([]byte, error) {
			defer func() {
				<-a.lookups
				if slot != nil {
					slot.Add(-1)
				}
			}()
			ctx, cancel := context.WithTimeout(a.ctx, sharedLookupTimeout)
			defer cancel()
			if state == gateStale && !a.readGate(ctx) {
				return nil, fs.ErrNotExist
			}
			return a.storage.Load(ctx, a.httpChallengeKey(token))
		}()
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				a.log.Warnf("acme: looking up a challenge in the shared storage: %v", err)
			}
			done("", false)
			return
		}
		done(string(data), true)
	}()
}

// sharedALPNChallenge returns the certificate of a TLS-ALPN-01 challenge
// another instance has under way for name, from the shared storage, or nil.
// It may block, within sharedLookupTimeout, on fib's handshake pool, but
// only while some instance has a challenge in progress, for a name not
// looked up in vain within missFor, and for at most maxSharedLookups
// handshakes at once; past those it returns nil at once.
func (a *ACME) sharedALPNChallenge(name string) *tls.Certificate {
	if !a.shared {
		return nil
	}
	if err := checkName(name); err != nil {
		return nil
	}
	now := a.now().UnixNano()
	state := a.gate.state(now)
	if state == gateClosed || a.missed(name, now) {
		return nil
	}
	select {
	case a.lookups <- struct{}{}:
	default:
		return nil
	}
	defer func() { <-a.lookups }()
	ctx, cancel := context.WithTimeout(a.ctx, sharedLookupTimeout)
	defer cancel()
	if state == gateStale && !a.readGate(ctx) {
		return nil
	}
	data, err := a.storage.Load(ctx, a.alpnChallengeKey(name))
	if err != nil {
		// A failure too, so that a store in trouble is not asked again
		// at once.
		a.miss(name, now)
		if !errors.Is(err, fs.ErrNotExist) {
			a.log.Warnf("acme: looking up a challenge in the shared storage: %v", err)
		}
		return nil
	}
	cert, err := tls.X509KeyPair(data, data)
	if err != nil {
		a.log.Warnf("acme: %s: the shared challenge's certificate: %v", name, err)
		return nil
	}
	return &cert
}

// checkName refuses a name that would make another storage key.
func checkName(name string) error {
	if name == "" || len(name) > 253 {
		return fmt.Errorf("bad name %q", name)
	}
	for _, c := range []byte(name) {
		if !('a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' || c == '.') {
			return fmt.Errorf("bad name %q", name)
		}
	}
	return nil
}

// encodeCert writes a certificate's chain and key as PEM, as the storage
// keeps them.
func encodeCert(cert *tls.Certificate) ([]byte, error) {
	var data []byte
	for _, der := range cert.Certificate {
		data = append(data, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		return nil, err
	}
	return append(data, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})...), nil
}
