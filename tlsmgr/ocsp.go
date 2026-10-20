package tlsmgr

import (
	"bytes"
	"context"
	"crypto"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ocsp"
)

// The defaults of OCSP.
const (
	// ocspWorkers is how many responses are fetched at once.
	ocspWorkers = 2
	// ocspTimeout bounds one fetch, and ocspMaxBytes the response read.
	ocspTimeout  = 10 * time.Second
	ocspMaxBytes = 64 << 10
	// ocspRefresh is how often a response with no next update is fetched
	// again.
	ocspRefresh = time.Hour
)

// OCSP staples OCSP responses to the certificates it is given: it fetches
// each certificate's response from the responder the certificate names, on
// goroutines of its own, at most ocspWorkers at once and each within
// ocspTimeout, and fetches it again halfway through its validity. A
// handshake reads the certificate with its latest good response through an
// atomic pointer (Staple.Certificate), and waits for nothing.
//
// Certificates are kept by their chain, with a count of their users, so that
// the graphs a reload makes share the response of a certificate they both
// serve, and a certificate no graph serves is dropped.
type OCSP struct {
	http *http.Client
	logf func(format string, args ...any)

	// now, retryBase and retryMax are what tests change.
	now                 func() time.Time
	retryBase, retryMax time.Duration

	mu      sync.Mutex
	staples map[[32]byte]*Staple
	closed  bool

	sem    chan struct{} // a slot per fetch under way
	wake   chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
	done   sync.WaitGroup
}

// Staple is a certificate OCSP keeps a response for.
type Staple struct {
	key    [32]byte
	base   *tls.Certificate
	leaf   *x509.Certificate
	issuer *x509.Certificate
	// current is base with the latest good response, or nil while there is
	// none that has not expired.
	current atomic.Pointer[tls.Certificate]
	// revoked, if not nil, hears that the responder says the certificate
	// is revoked.
	revoked func()

	// Under OCSP.mu.
	refs     int
	due      time.Time
	expires  time.Time // when current's response stops being valid
	failures int
	running  bool
}

// Certificate returns the certificate with its latest good response stapled,
// or without one while there is none.
func (s *Staple) Certificate() *tls.Certificate {
	if c := s.current.Load(); c != nil {
		return c
	}
	return s.base
}

// NewOCSP returns an OCSP, running; logf, which may be nil, hears what it
// does. Close stops it.
func NewOCSP(logf func(format string, args ...any)) *OCSP {
	o := newOCSP(logf)
	o.start()
	return o
}

func newOCSP(logf func(format string, args ...any)) *OCSP {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	o := &OCSP{
		http:      &http.Client{Timeout: ocspTimeout},
		logf:      logf,
		now:       time.Now,
		retryBase: defaultRetryBase,
		retryMax:  defaultRetryMax,
		staples:   map[[32]byte]*Staple{},
		sem:       make(chan struct{}, ocspWorkers),
		wake:      make(chan struct{}, 1),
	}
	o.ctx, o.cancel = context.WithCancel(context.Background())
	return o
}

func (o *OCSP) start() {
	o.done.Add(1)
	go o.schedule()
}

// Close stops o: the fetches under way are cancelled, and Close returns once
// its goroutines have. The staples it made keep their last responses.
func (o *OCSP) Close() error {
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return nil
	}
	o.closed = true
	o.mu.Unlock()
	o.cancel()
	o.done.Wait()
	return nil
}

// Add keeps a response for cert, which must have its Leaf set, and returns
// its Staple; or nil when cert names no responder, or its chain has no
// issuer to check a response against. revoked, which may be nil, hears that
// the responder says cert is revoked; the first Add of a chain sets it.
// Each Add is undone by a Release.
func (o *OCSP) Add(cert *tls.Certificate, revoked func()) *Staple {
	if cert.Leaf == nil || len(cert.Leaf.OCSPServer) == 0 || len(cert.Certificate) < 2 {
		return nil
	}
	h := sha256.New()
	for _, der := range cert.Certificate {
		h.Write(der)
	}
	var key [32]byte
	h.Sum(key[:0])
	o.mu.Lock()
	defer o.mu.Unlock()
	if s := o.staples[key]; s != nil {
		s.refs++
		return s
	}
	issuer, err := x509.ParseCertificate(cert.Certificate[1])
	if err != nil {
		return nil
	}
	s := &Staple{key: key, base: cert, leaf: cert.Leaf, issuer: issuer, revoked: revoked, refs: 1, due: o.now()}
	o.staples[key] = s
	o.poke()
	return s
}

// Release undoes an Add of s's certificate; the last one drops it.
func (o *OCSP) Release(s *Staple) {
	if s == nil {
		return
	}
	o.mu.Lock()
	if s.refs--; s.refs == 0 && o.staples[s.key] == s {
		delete(o.staples, s.key)
	}
	o.mu.Unlock()
}

func (o *OCSP) poke() {
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

// schedule starts a fetch for each certificate that is due, as long as a
// slot is free, and sleeps until the next is due.
func (o *OCSP) schedule() {
	defer o.done.Done()
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		o.mu.Lock()
		now := o.now()
		next := now.Add(time.Hour)
		for _, s := range o.staples {
			if s.running {
				continue
			}
			if !s.due.After(now) {
				select {
				case o.sem <- struct{}{}:
					s.running = true
					o.done.Add(1)
					go o.fetch(s)
					continue
				default:
					// Every slot is taken; a fetch that ends pokes.
				}
			}
			if s.due.After(now) && s.due.Before(next) {
				next = s.due
			}
		}
		o.mu.Unlock()
		timer.Reset(max(next.Sub(now), time.Millisecond))
		select {
		case <-o.ctx.Done():
			return
		case <-o.wake:
		case <-timer.C:
		}
	}
}

// fetch gets s's response, staples it if it is good, and sets when s is next
// due: halfway through the response's validity, or, after a failure, later
// and later.
func (o *OCSP) fetch(s *Staple) {
	defer o.done.Done()
	resp, raw, err := o.get(s)
	o.mu.Lock()
	now := o.now()
	s.running = false
	var revoked func()
	switch {
	case err != nil:
		if o.ctx.Err() != nil {
			break
		}
		s.failures++
		wait := backoff(o.retryBase, o.retryMax, s.failures)
		s.due = now.Add(wait)
		if s.current.Load() != nil && !s.expires.IsZero() && s.expires.Before(s.due) {
			// Looked at again when the response stapled expires, at the
			// latest, so that it is not stapled past then.
			s.due = s.expires
		}
		o.logf("ocsp: %s: %v; trying again in %s", subject(s.leaf), err, wait.Round(time.Second))
	case resp.Status == ocsp.Good:
		s.failures = 0
		c := *s.base
		c.OCSPStaple = raw
		s.current.Store(&c)
		s.expires = resp.NextUpdate
		s.due = now.Add(ocspRefresh)
		if !resp.NextUpdate.IsZero() {
			s.due = resp.ThisUpdate.Add(resp.NextUpdate.Sub(resp.ThisUpdate) / 2)
			if !s.due.After(now) {
				s.due = now.Add(min(resp.NextUpdate.Sub(now)/2, ocspRefresh))
			}
		}
	default:
		// Revoked, or unknown to the responder: nothing to staple.
		s.failures = 0
		s.current.Store(nil)
		s.due = now.Add(ocspRefresh)
		if resp.Status == ocsp.Revoked {
			o.logf("ocsp: %s: revoked at %s", subject(s.leaf), resp.RevokedAt.UTC().Format(time.RFC3339))
			revoked = s.revoked
		} else {
			o.logf("ocsp: %s: unknown to the responder", subject(s.leaf))
		}
	}
	// A response is not stapled past its validity, whatever the fetches
	// since.
	if c := s.current.Load(); c != nil && !s.expires.IsZero() && !now.Before(s.expires) {
		s.current.Store(nil)
	}
	o.mu.Unlock()
	<-o.sem
	o.poke()
	if revoked != nil {
		revoked()
	}
}

// get fetches and checks s's response.
func (o *OCSP) get(s *Staple) (*ocsp.Response, []byte, error) {
	req, err := ocsp.CreateRequest(s.leaf, s.issuer, &ocsp.RequestOptions{Hash: crypto.SHA1})
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(o.ctx, ocspTimeout)
	defer cancel()
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.leaf.OCSPServer[0], bytes.NewReader(req))
	if err != nil {
		return nil, nil, err
	}
	hreq.Header.Set("Content-Type", "application/ocsp-request")
	hresp, err := o.http.Do(hreq)
	if err != nil {
		return nil, nil, err
	}
	defer hresp.Body.Close()
	if hresp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("the responder answered %s", hresp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(hresp.Body, ocspMaxBytes+1))
	if err != nil {
		return nil, nil, err
	}
	if len(raw) > ocspMaxBytes {
		return nil, nil, errors.New("the response is too large")
	}
	resp, err := ocsp.ParseResponseForCert(raw, s.leaf, s.issuer)
	if err != nil {
		return nil, nil, err
	}
	if !resp.NextUpdate.IsZero() && !o.now().Before(resp.NextUpdate) {
		return nil, nil, errors.New("the response has expired")
	}
	return resp, raw, nil
}

// subject names a certificate in what OCSP reports.
func subject(leaf *x509.Certificate) string {
	if len(leaf.DNSNames) > 0 {
		return leaf.DNSNames[0]
	}
	return leaf.Subject.CommonName
}

// backoff is how long to wait after the nth failure in a row: base doubled
// n-1 times, up to most.
func backoff(base, most time.Duration, n int) time.Duration {
	d := base
	for i := 1; i < n && d < most; i++ {
		d *= 2
	}
	return min(d, most)
}
