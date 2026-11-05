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
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/crypto/ocsp"

	"github.com/lesismal/fit/logs"
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
//
// A certificate's responder is a URL the certificate names, so a request to
// it goes where whoever made the certificate says. For a certificate ACME
// obtained, that is the CA the operator configured. A certificate from files
// may have been written from what someone else gave, such as a Kubernetes
// Secret a tenant made, so its response is fetched only when its chain
// verifies against the system's roots (or those TrustRoots gives): a public
// CA names its own responder, and nobody can have it name another. Such a
// responder is not dialled at an address that is not public, loopback,
// private, link-local or the like, unless through the proxy the environment
// sets. No fetch follows a redirect.
type OCSP struct {
	http   *http.Client // for certificates ACME obtained
	public *http.Client // for certificates from files: public addresses only
	log    *logs.Logger

	// roots, when not nil, replaces the system's roots for certificates
	// from files, and private allows their responders at any address.
	roots   atomic.Pointer[x509.CertPool]
	private atomic.Bool

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
	// verified says the chain verified against the roots, which a
	// certificate from files must before its response is fetched; set by
	// its fetches, which are one at a time.
	verified bool
	// toldUnverified says that its chain not verifying was reported.
	toldUnverified bool

	// Under OCSP.mu.
	refs int
	// fromFiles says every Add of it was for a certificate from files.
	fromFiles bool
	due       time.Time
	expires   time.Time // when current's response stops being valid
	failures  int
	running   bool
}

// Certificate returns the certificate with its latest good response stapled,
// or without one while there is none.
func (s *Staple) Certificate() *tls.Certificate {
	if c := s.current.Load(); c != nil {
		return c
	}
	return s.base
}

// NewOCSP returns an OCSP, running; log, which may be nil, hears what it
// does. Close stops it.
func NewOCSP(log *logs.Logger) *OCSP {
	o := newOCSP(log)
	o.start()
	return o
}

func newOCSP(log *logs.Logger) *OCSP {
	o := &OCSP{
		log:       log,
		now:       time.Now,
		retryBase: defaultRetryBase,
		retryMax:  defaultRetryMax,
		staples:   map[[32]byte]*Staple{},
		sem:       make(chan struct{}, ocspWorkers),
		wake:      make(chan struct{}, 1),
	}
	o.http = ocspClient(nil)
	o.public = ocspClient(o.allowAddr)
	o.ctx, o.cancel = context.WithCancel(context.Background())
	return o
}

// TrustRoots has o fetch the responses of certificates from files whose
// chain verifies against roots, in place of the system's roots, and, with
// private, from responders at any address: for an operator whose files hold
// certificates of a CA of its own, and for tests.
func (o *OCSP) TrustRoots(roots *x509.CertPool, private bool) {
	o.roots.Store(roots)
	o.private.Store(private)
}

// ocspClient returns a client of responders that follows no redirect, and,
// with allow, dials only addresses allow allows, but for the proxy the
// environment sets, which the operator chose.
func ocspClient(allow func(netip.Addr) bool) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if allow != nil {
		proxies := proxyAddrs()
		direct := &net.Dialer{Timeout: ocspTimeout, KeepAlive: 30 * time.Second}
		filtered := &net.Dialer{Timeout: ocspTimeout, KeepAlive: 30 * time.Second,
			// Called with the address dialled, once a name is resolved,
			// so that a name cannot point somewhere else.
			Control: func(_, address string, _ syscall.RawConn) error {
				ap, err := netip.ParseAddrPort(address)
				if err != nil {
					return err
				}
				if !allow(ap.Addr()) {
					return fmt.Errorf("the responder's address %s is not public", ap.Addr())
				}
				return nil
			}}
		tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			if proxies[addr] {
				return direct.DialContext(ctx, network, addr)
			}
			return filtered.DialContext(ctx, network, addr)
		}
	}
	return &http.Client{
		Transport: tr,
		Timeout:   ocspTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// proxyAddrs returns the addresses of the proxies the environment sets for
// http and https, as a transport dials them.
func proxyAddrs() map[string]bool {
	addrs := map[string]bool{}
	for _, scheme := range []string{"http", "https"} {
		u, err := http.ProxyFromEnvironment(&http.Request{URL: &url.URL{Scheme: scheme, Host: "ocsp.invalid"}})
		if err != nil || u == nil {
			continue
		}
		port := u.Port()
		if port == "" {
			port = map[string]string{"http": "80", "https": "443", "socks5": "1080", "socks5h": "1080"}[u.Scheme]
		}
		addrs[net.JoinHostPort(u.Hostname(), port)] = true
	}
	return addrs
}

// allowAddr reports whether a responder of a certificate from files may be
// dialled at addr.
func (o *OCSP) allowAddr(addr netip.Addr) bool { return o.private.Load() || publicAddr(addr) }

// nat64 is the well-known prefix of NAT64 (RFC 6052), whose addresses are
// IPv4 addresses.
var nat64 = netip.MustParsePrefix("64:ff9b::/96")

// sharedSpace is the IPv4 space carriers share (RFC 6598), as private as
// the private ranges.
var sharedSpace = netip.MustParsePrefix("100.64.0.0/10")

// publicAddr reports whether addr is a public unicast address: not
// loopback, private, link-local, multicast, unspecified or shared.
func publicAddr(addr netip.Addr) bool {
	addr = addr.Unmap()
	if nat64.Contains(addr) {
		b := addr.As16()
		addr = netip.AddrFrom4([4]byte(b[12:]))
	}
	return addr.IsGlobalUnicast() && !addr.IsPrivate() && !sharedSpace.Contains(addr)
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

// Add keeps a response for cert, which must have its Leaf set and come from
// a CA the operator chose, as ACME's do, and returns its Staple; or nil when
// cert names no responder, or its chain has no issuer to check a response
// against. revoked, which may be nil, hears that the responder says cert is
// revoked; the first Add of a chain sets it. Each Add is undone by a
// Release.
func (o *OCSP) Add(cert *tls.Certificate, revoked func()) *Staple {
	return o.add(cert, revoked, false)
}

// add is Add, for a certificate from files with fromFiles (see OCSP).
func (o *OCSP) add(cert *tls.Certificate, revoked func(), fromFiles bool) *Staple {
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
		// The chain an operator's CA issued is no less trusted for being
		// in files too.
		s.fromFiles = s.fromFiles && fromFiles
		return s
	}
	issuer, err := x509.ParseCertificate(cert.Certificate[1])
	if err != nil {
		return nil
	}
	s := &Staple{key: key, base: cert, leaf: cert.Leaf, issuer: issuer, revoked: revoked, refs: 1, fromFiles: fromFiles, due: o.now()}
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
					go o.fetch(s, s.fromFiles)
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
func (o *OCSP) fetch(s *Staple, fromFiles bool) {
	defer o.done.Done()
	var resp *ocsp.Response
	var raw []byte
	var err error
	client := o.http
	if fromFiles {
		client = o.public
		if !s.verified {
			err = o.verify(s)
			s.verified = err == nil
		}
	}
	if err == nil {
		resp, raw, err = o.get(s, client)
	}
	o.mu.Lock()
	now := o.now()
	s.running = false
	var revoked func()
	switch {
	case errors.Is(err, errUnverified):
		// Looked at again now and then, as the roots may change; said
		// once.
		if !s.toldUnverified {
			s.toldUnverified = true
			o.log.Infof("ocsp: %s: no response stapled: %v", subject(s.leaf), err)
		}
		s.due = now.Add(ocspRefresh)
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
		o.log.Warnf("ocsp: %s: %v; trying again in %s", subject(s.leaf), err, wait.Round(time.Second))
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
			o.log.Errorf("ocsp: %s: revoked at %s", subject(s.leaf), resp.RevokedAt.UTC().Format(time.RFC3339))
			revoked = s.revoked
		} else {
			o.log.Warnf("ocsp: %s: unknown to the responder", subject(s.leaf))
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

// errUnverified is why a certificate from files gets no response.
var errUnverified = errors.New("its chain does not verify against the trusted roots, and its responder is not asked")

// verify checks that s's chain, a certificate from files, verifies against
// the roots, for any use.
func (o *OCSP) verify(s *Staple) error {
	inter := x509.NewCertPool()
	for _, der := range s.base.Certificate[1:] {
		if c, err := x509.ParseCertificate(der); err == nil {
			inter.AddCert(c)
		}
	}
	_, err := s.leaf.Verify(x509.VerifyOptions{Roots: o.roots.Load(), Intermediates: inter,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}})
	if err != nil {
		return fmt.Errorf("%w (%v)", errUnverified, err)
	}
	return nil
}

// get fetches and checks s's response with client.
func (o *OCSP) get(s *Staple, client *http.Client) (*ocsp.Response, []byte, error) {
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
	hresp, err := client.Do(hreq)
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
