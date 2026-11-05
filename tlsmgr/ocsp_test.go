package tlsmgr

import (
	"crypto/tls"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ocsp"

	"github.com/lesismal/fit/internal/certtest"
	"github.com/lesismal/fit/logs"
)

// responder is an OCSP responder for a test CA, whose answers a test sets.
type responder struct {
	ca  *certtest.CA
	srv *httptest.Server

	mu       sync.Mutex
	status   map[string]int // by serial number; Good when not set
	validity time.Duration  // of a response; 0 for none given
	fail     bool           // answer 500
	hold     chan struct{}  // when not nil, requests wait for it to close

	requests, inFlight, mostInFlight atomic.Int32
}

func newResponder(t *testing.T) *responder {
	t.Helper()
	ca, err := certtest.NewCA("fit test CA")
	if err != nil {
		t.Fatal(err)
	}
	r := &responder{ca: ca, status: map[string]int{}, validity: time.Hour}
	r.srv = httptest.NewServer(http.HandlerFunc(r.serve))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *responder) serve(w http.ResponseWriter, req *http.Request) {
	n := r.inFlight.Add(1)
	defer r.inFlight.Add(-1)
	for {
		m := r.mostInFlight.Load()
		if n <= m || r.mostInFlight.CompareAndSwap(m, n) {
			break
		}
	}
	r.requests.Add(1)
	r.mu.Lock()
	hold, fail, validity := r.hold, r.fail, r.validity
	r.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-req.Context().Done():
			return
		}
	}
	if fail {
		http.Error(w, "down", http.StatusInternalServerError)
		return
	}
	body, _ := io.ReadAll(req.Body)
	oreq, err := ocsp.ParseRequest(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	status, ok := r.status[oreq.SerialNumber.String()]
	r.mu.Unlock()
	if !ok {
		status = ocsp.Good
	}
	now := time.Now().Truncate(time.Second)
	tmpl := ocsp.Response{Status: status, SerialNumber: oreq.SerialNumber, ThisUpdate: now}
	if validity > 0 {
		tmpl.NextUpdate = now.Add(validity)
	}
	if status == ocsp.Revoked {
		tmpl.RevokedAt, tmpl.RevocationReason = now.Add(-time.Minute), ocsp.KeyCompromise
	}
	resp, err := ocsp.CreateResponse(r.ca.Cert, r.ca.Cert, tmpl, r.ca.Key)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/ocsp-response")
	_, _ = w.Write(resp)
}

func (r *responder) set(f func(r *responder)) {
	r.mu.Lock()
	f(r)
	r.mu.Unlock()
}

// issue makes a certificate the responder answers for, as a listener loads
// it.
func (r *responder) issue(t *testing.T, name string) *tls.Certificate {
	t.Helper()
	c, err := r.ca.Issue(t.TempDir(), r.srv.URL, name)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
	if err != nil {
		t.Fatal(err)
	}
	return &cert
}

func testOCSP(t *testing.T, l *logLines) *OCSP {
	t.Helper()
	o := newOCSP(logs.Func(l.logf))
	o.retryBase, o.retryMax = 20*time.Millisecond, 200*time.Millisecond
	o.start()
	t.Cleanup(func() { o.Close() })
	return o
}

// stapled returns the status of st's stapled response, or -1 for none.
func stapled(t *testing.T, st *Staple, r *responder) int {
	t.Helper()
	c := st.Certificate()
	if len(c.OCSPStaple) == 0 {
		return -1
	}
	resp, err := ocsp.ParseResponseForCert(c.OCSPStaple, c.Leaf, r.ca.Cert)
	if err != nil {
		t.Fatal(err)
	}
	return resp.Status
}

func TestOCSPStaple(t *testing.T) {
	r := newResponder(t)
	var l logLines
	o := testOCSP(t, &l)
	cert := r.issue(t, "a.fit.test")
	st := o.Add(cert, nil)
	if st == nil {
		t.Fatal("no staple for a certificate that names a responder")
	}
	eventually(t, 5*time.Second, "a stapled response", func() bool { return stapled(t, st, r) == ocsp.Good })
	if st.Certificate() == cert || len(cert.OCSPStaple) != 0 {
		t.Error("the certificate given was changed, rather than a copy stapled")
	}

	// A certificate that names no responder, or has no issuer, gets none.
	self := keyCert(t, false, "b.fit.test")
	if o.Add(self, nil) != nil {
		t.Error("a staple for a certificate without a responder")
	}
}

func TestOCSPRefresh(t *testing.T) {
	r := newResponder(t)
	r.set(func(r *responder) { r.validity = 2 * time.Second })
	var l logLines
	o := testOCSP(t, &l)
	st := o.Add(r.issue(t, "a.fit.test"), nil)
	eventually(t, 5*time.Second, "a stapled response", func() bool { return stapled(t, st, r) == ocsp.Good })
	first := st.Certificate().OCSPStaple
	// Fetched again halfway through its two seconds.
	eventually(t, 5*time.Second, "a refreshed response", func() bool {
		return string(st.Certificate().OCSPStaple) != string(first)
	})

	// The responder goes down: the response is kept while it is valid, and
	// dropped once it is not.
	r.set(func(r *responder) { r.fail = true })
	time.Sleep(300 * time.Millisecond)
	if stapled(t, st, r) != ocsp.Good {
		t.Error("a valid response was dropped when the responder went down")
	}
	eventually(t, 5*time.Second, "the expired response dropped", func() bool { return stapled(t, st, r) == -1 })
	if l.count("trying again") == 0 {
		t.Error("the failures were not reported")
	}
	// Back up, the response is stapled again.
	r.set(func(r *responder) { r.fail = false })
	eventually(t, 5*time.Second, "a response again", func() bool { return stapled(t, st, r) == ocsp.Good })
}

func TestOCSPRevoked(t *testing.T) {
	r := newResponder(t)
	var l logLines
	o := testOCSP(t, &l)
	cert := r.issue(t, "a.fit.test")
	r.set(func(r *responder) { r.status[cert.Leaf.SerialNumber.String()] = ocsp.Revoked })
	var heard atomic.Int32
	st := o.Add(cert, func() { heard.Add(1) })
	eventually(t, 5*time.Second, "the revocation heard", func() bool { return heard.Load() == 1 })
	if stapled(t, st, r) != -1 {
		t.Error("a revoked certificate's response was stapled")
	}
	if l.count("revoked") == 0 {
		t.Error("the revocation was not reported")
	}
}

func TestOCSPShared(t *testing.T) {
	r := newResponder(t)
	var l logLines
	o := testOCSP(t, &l)
	cert := r.issue(t, "a.fit.test")
	// The same chain loaded again, as a reload's graph loads its files.
	again := *cert
	a, b := o.Add(cert, nil), o.Add(&again, nil)
	if a != b {
		t.Fatal("the same chain got two staples")
	}
	eventually(t, 5*time.Second, "a stapled response", func() bool { return stapled(t, a, r) == ocsp.Good })
	if n := r.requests.Load(); n != 1 {
		t.Errorf("%d requests for one chain", n)
	}
	o.Release(a)
	o.mu.Lock()
	kept := len(o.staples)
	o.mu.Unlock()
	if kept != 1 {
		t.Fatal("released while still added")
	}
	o.Release(b)
	o.mu.Lock()
	kept = len(o.staples)
	o.mu.Unlock()
	if kept != 0 {
		t.Fatal("kept after its last release")
	}
}

// TestOCSPSlowResponder has a responder that holds every request: no more
// than ocspWorkers are made at once, and Close does not wait for them.
func TestOCSPSlowResponder(t *testing.T) {
	r := newResponder(t)
	hold := make(chan struct{})
	r.set(func(r *responder) { r.hold = hold })
	defer close(hold)
	var l logLines
	o := testOCSP(t, &l)
	var staples []*Staple
	for i := range 6 {
		staples = append(staples, o.Add(r.issue(t, "s"+big.NewInt(int64(i)).String()+".fit.test"), nil))
	}
	eventually(t, 5*time.Second, "the responder busy", func() bool { return r.inFlight.Load() == ocspWorkers })
	time.Sleep(200 * time.Millisecond)
	if n := r.mostInFlight.Load(); n > ocspWorkers {
		t.Errorf("%d requests at once", n)
	}
	for _, st := range staples {
		if st.Certificate() != st.base {
			t.Error("a response stapled while the responder holds every request")
		}
	}
	start := time.Now()
	o.Close()
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("Close took %v", d)
	}
}

func TestStaticStaple(t *testing.T) {
	r := newResponder(t)
	var l logLines
	o := testOCSP(t, &l)
	// The test CA's own, at a loopback address: see TestStaticStapleSSRF.
	o.TrustRoots(r.ca.Pool(), true)
	cert := r.issue(t, "a.fit.test")
	other := keyCert(t, false, "b.fit.test")
	s := NewStatic(cert, other)
	s.Staple(o)
	eventually(t, 5*time.Second, "a stapled response", func() bool {
		c, _ := s.GetCertificate(hello("a.fit.test"))
		return len(c.OCSPStaple) > 0
	})
	if c, _ := s.GetCertificate(hello("b.fit.test")); c != other {
		t.Error("a certificate without a responder is not served as it is")
	}
	s.Release()
	o.mu.Lock()
	kept := len(o.staples)
	o.mu.Unlock()
	if kept != 0 {
		t.Error("Release left the staple")
	}
}

// TestStaticStapleSSRF serves, from files, a certificate whoever wrote them
// made, such as a tenant through a Kubernetes Secret, which names a
// responder at an address inside the network: it is not asked unless the
// chain verifies against the trusted roots, and even then not at a loopback
// address, while ACME's certificates, from the operator's CA, are.
func TestStaticStapleSSRF(t *testing.T) {
	r := newResponder(t)
	cert := r.issue(t, "a.fit.test")

	// A chain no trusted root issued.
	var l logLines
	o := testOCSP(t, &l)
	s := NewStatic(cert)
	s.Staple(o)
	eventually(t, 5*time.Second, "the chain refused", func() bool { return l.count("does not verify") == 1 })
	if n := r.requests.Load(); n != 0 {
		t.Errorf("the responder of a chain no trusted root issued was asked %d times", n)
	}
	s.Release()

	// Trusted, but at a loopback address.
	var l2 logLines
	o2 := testOCSP(t, &l2)
	o2.TrustRoots(r.ca.Pool(), false)
	s2 := NewStatic(cert)
	s2.Staple(o2)
	eventually(t, 5*time.Second, "the address refused", func() bool { return l2.count("is not public") > 0 })
	if n := r.requests.Load(); n != 0 {
		t.Errorf("a responder at a loopback address was asked %d times", n)
	}
	// The same chain obtained by ACME is the operator's CA's.
	st := o2.Add(cert, nil)
	o2.mu.Lock()
	fromFiles := st.fromFiles
	o2.mu.Unlock()
	if fromFiles {
		t.Error("a chain ACME obtained still taken as from files only")
	}
	s2.Release()
	o2.Release(st)

	// A responder that redirects is not followed.
	var hops atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hops.Add(1) }))
	defer target.Close()
	redirect := httptest.NewServer(http.RedirectHandler(target.URL, http.StatusTemporaryRedirect))
	defer redirect.Close()
	leaf := *cert.Leaf
	leaf.OCSPServer = []string{redirect.URL}
	if _, _, err := o.get(&Staple{leaf: &leaf, issuer: r.ca.Cert}, o.http); err == nil || !strings.Contains(err.Error(), "307") {
		t.Errorf("a redirect: %v", err)
	}
	if hops.Load() != 0 {
		t.Error("a redirect was followed")
	}
}

func TestPublicAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		"8.8.8.8": true, "2606:4700::1111": true, "::ffff:8.8.8.8": true, "64:ff9b::808:808": true,
		"127.0.0.1": false, "::1": false, "10.0.0.5": false, "172.16.0.1": false, "192.168.1.1": false,
		"169.254.169.254": false, "fe80::1": false, "fd00::1": false, "0.0.0.0": false, "::": false,
		"224.0.0.1": false, "ff02::1": false, "100.64.0.1": false, "::ffff:127.0.0.1": false,
		"64:ff9b::a9fe:a9fe": false,
	} {
		if got := publicAddr(netip.MustParseAddr(addr)); got != want {
			t.Errorf("%s: %v", addr, got)
		}
	}
	t.Setenv("HTTPS_PROXY", "http://10.0.0.9:3128")
	t.Setenv("HTTP_PROXY", "socks5://10.0.0.9")
	// ProxyFromEnvironment reads the environment once per process, so
	// what proxyAddrs says depends on what ran first; it must at least not
	// name anything else.
	for addr := range proxyAddrs() {
		if addr != "10.0.0.9:3128" && addr != "10.0.0.9:1080" {
			t.Errorf("a proxy at %s", addr)
		}
	}
}
