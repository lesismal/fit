package tlsmgr

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/certtest"
	"github.com/lesismal/fit/internal/pebbletest"
)

// logs collects what an ACME reports.
type logs struct {
	mu    sync.Mutex
	lines []string
}

func (l *logs) logf(format string, args ...any) {
	l.mu.Lock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
	l.mu.Unlock()
}

func (l *logs) count(substr string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, line := range l.lines {
		if strings.Contains(line, substr) {
			n++
		}
	}
	return n
}

// testACME returns an unstarted ACME of directory, keeping what it obtains in
// a directory of t's, with fast retries.
func testACME(t *testing.T, c *config.ACME, l *logs) *ACME {
	t.Helper()
	if c.Storage == "" {
		c.Storage = t.TempDir()
	}
	a, err := newACME(c, nil, l.logf)
	if err != nil {
		t.Fatal(err)
	}
	a.retryBase, a.retryMax = 20*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { a.Close() })
	return a
}

func eventually(t *testing.T, within time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("not within %s: %s", within, what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCAKey(t *testing.T) {
	for in, want := range map[string]string{
		"https://acme-v02.api.letsencrypt.org/directory": "acme-v02.api.letsencrypt.org-directory",
		"https://127.0.0.1:14000/dir":                    "127.0.0.1-14000-dir",
		"https://ca.example/a/../b?x":                    "ca.example-a-..-b",
		"https://.x/":                                    "ca-.x",
	} {
		if got := caKey(in); got != want {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
}

func TestBackoff(t *testing.T) {
	a := &ACME{retryBase: time.Minute, retryMax: time.Hour}
	for n, want := range map[int]time.Duration{1: time.Minute, 2: 2 * time.Minute, 4: 8 * time.Minute, 7: time.Hour, 50: time.Hour} {
		for range 20 {
			if got := a.backoff(n); got > want || got < want-want/10 {
				t.Fatalf("failure %d: %s, want %s less up to a tenth", n, got, want)
			}
		}
	}
}

func TestRenewAt(t *testing.T) {
	a := &ACME{}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	life := 90 * 24 * time.Hour
	seen := map[time.Time]bool{}
	for i := range 50 {
		leaf := &x509.Certificate{NotBefore: start, NotAfter: start.Add(life), SerialNumber: big.NewInt(int64(1000 + i*7919))}
		at := a.defaultRenewAt(leaf)
		if latest, earliest := leaf.NotAfter.Add(-life/3), leaf.NotAfter.Add(-life/3-life/50); at.After(latest) || at.Before(earliest) {
			t.Fatalf("serial %d: renewed at %s, not within [%s, %s]", leaf.SerialNumber, at, earliest, latest)
		}
		if again := a.defaultRenewAt(leaf); !again.Equal(at) {
			t.Fatalf("serial %d: %s, then %s", leaf.SerialNumber, at, again)
		}
		seen[at] = true
	}
	if len(seen) < 40 {
		t.Errorf("50 certificates renew at only %d times", len(seen))
	}
}

func TestCerts(t *testing.T) {
	file := keyCert(t, false, "files.example", "managed.example")
	static := NewStatic(file)
	managed := keyCert(t, false, "managed.example")
	a := &ACME{}
	a.certs.Store(&map[string]*tls.Certificate{"managed.example": managed})

	both := NewCerts(static, a, []string{"Managed.Example", "pending.example"}, false)
	acmeOnly := NewCerts(nil, a, []string{"managed.example", "pending.example"}, false)
	pendingFirst := NewCerts(nil, a, []string{"pending.example", "managed.example"}, false)
	for _, tt := range []struct {
		c    *Certs
		sni  string
		want *tls.Certificate
	}{
		{both, "managed.example", managed},
		{both, "MANAGED.example.", managed},
		{both, "pending.example", file}, // none obtained yet: the files'
		{both, "files.example", file},
		{both, "", file},
		{acmeOnly, "managed.example", managed},
		{acmeOnly, "", managed}, // the first name listed
		{acmeOnly, "pending.example", nil},
		{acmeOnly, "other.example", nil},
		{pendingFirst, "", nil},
	} {
		got, err := tt.c.GetCertificate(hello(tt.sni))
		if got != tt.want || (got == nil) != (err != nil) {
			t.Errorf("%q: %v, %v", tt.sni, got, err)
		}
	}
	// What the metrics list: the files', and those obtained for its names.
	if got := both.Certificates(); len(got) != 2 || got[0] != file || got[1] != managed {
		t.Errorf("Certificates: %v", got)
	}
	if got := NewCerts(nil, a, []string{"pending.example"}, false).Certificates(); len(got) != 0 {
		t.Errorf("Certificates with none obtained: %v", got)
	}
}

func TestManage(t *testing.T) {
	var l logs
	// A CA nobody listens at: every name fails, and is tried again.
	a := testACME(t, &config.ACME{Directory: "https://127.0.0.1:1/dir"}, &l)
	a.start()
	a.certs.Store(&map[string]*tls.Certificate{"a.example": keyCert(t, false, "a.example"), "b.example": keyCert(t, false, "b.example")})
	a.Manage([]string{"A.example", "b.example"})
	a.Manage([]string{"a.example"})
	if a.Certificate("b.example") != nil || a.Certificate("a.example") == nil {
		t.Fatal("a name dropped is still served, or one kept is not")
	}
	eventually(t, 5*time.Second, "a.example tried three times", func() bool { return l.count("a.example: ") >= 3 })
	if n := l.count("b.example"); n > 1 {
		t.Errorf("b.example, dropped, was tried %d times", n)
	}
}

func TestStoredCertificate(t *testing.T) {
	// A certificate in the storage that is not due is served as it is,
	// without the CA, which is not there.
	dir := t.TempDir()
	cert, err := certtest.Write(t.TempDir(), "stored.example")
	if err != nil {
		t.Fatal(err)
	}
	certPEM, _ := os.ReadFile(cert.CertFile)
	keyPEM, _ := os.ReadFile(cert.KeyFile)
	var l logs
	a := testACME(t, &config.ACME{Directory: "https://127.0.0.1:1/dir", Storage: dir}, &l)
	if err := a.storage.Store(context.Background(), a.certKey("stored.example"), append(certPEM, keyPEM...)); err != nil {
		t.Fatal(err)
	}
	a.start()
	a.Manage([]string{"stored.example"})
	eventually(t, 5*time.Second, "the stored certificate served", func() bool { return a.Certificate("stored.example") != nil })
	if got := a.Certificate("stored.example").Leaf.SerialNumber; got.Cmp(cert.Leaf.SerialNumber) != 0 {
		t.Errorf("serial %s, want the stored %s", got, cert.Leaf.SerialNumber)
	}
	if n := l.count("trying again"); n != 0 {
		t.Errorf("the CA was asked: %q", l.lines)
	}
}

// TestSlowCA checks that a CA that never answers holds up nothing but the
// work on its names: each is given up after the timeout and tried again, no
// more of them are worked on at once than max_concurrent, handshakes and
// challenges are answered meanwhile, and Close does not wait for the CA.
func TestSlowCA(t *testing.T) {
	var inFlight, most atomic.Int32
	release := make(chan struct{})
	ca := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			m := most.Load()
			if n <= m || most.CompareAndSwap(m, n) {
				break
			}
		}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer ca.Close()
	defer close(release)
	caFile := t.TempDir() + "/ca.pem"
	writePEM(t, caFile, ca.Certificate())

	var l logs
	a := testACME(t, &config.ACME{Directory: ca.URL + "/dir", CAFile: caFile, MaxConcurrent: 2,
		Timeout: config.Duration(150 * time.Millisecond), RequestTimeout: config.Duration(time.Minute)}, &l)
	a.start()
	names := []string{"a.example", "b.example", "c.example", "d.example", "e.example"}
	a.Manage(names)
	eventually(t, 5*time.Second, "every name given up at least once", func() bool {
		for _, n := range names {
			if l.count(n+": ") == 0 {
				return false
			}
		}
		return true
	})
	if m := most.Load(); m > 2 {
		t.Errorf("%d requests to the CA at once, past max_concurrent 2", m)
	}
	for _, line := range l.lines {
		if !strings.Contains(line, "context deadline exceeded") {
			t.Errorf("not the timeout: %s", line)
			break
		}
	}
	start := time.Now()
	if c, err := NewCerts(nil, a, names, false).GetCertificate(hello("a.example")); c != nil || err == nil {
		t.Error("a certificate for a name never obtained")
	}
	if _, ok := a.HTTPChallenge("token"); ok {
		t.Error("a challenge never begun")
	}
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Errorf("a handshake and a challenge took %s", d)
	}
	start = time.Now()
	a.Close()
	if d := time.Since(start); d > time.Second {
		t.Errorf("Close took %s while the CA hung", d)
	}
}

// TestCutCA checks that a CA that cuts every connection is tried again,
// later and later.
func TestCutCA(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var accepted atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			c.Close()
		}
	}()
	defer ln.Close()
	var l logs
	a := testACME(t, &config.ACME{Directory: "https://" + ln.Addr().String() + "/dir"}, &l)
	a.retryBase, a.retryMax = 20*time.Millisecond, time.Hour
	a.start()
	start := time.Now()
	a.Manage([]string{"cut.example"})
	eventually(t, 5*time.Second, "four failures", func() bool { return l.count("trying again") >= 4 })
	// The waits between the four are 20ms, 40ms and 80ms, each less up to
	// a tenth.
	if d := time.Since(start); d < 126*time.Millisecond {
		t.Errorf("four failures within %s: not backing off", d)
	}
	if accepted.Load() < 4 {
		t.Errorf("%d connections to the CA", accepted.Load())
	}
	a.mu.Lock()
	failures := a.names["cut.example"].failures
	a.mu.Unlock()
	if failures < 4 {
		t.Errorf("%d failures counted", failures)
	}
}

func writePEM(t *testing.T, file string, cert *x509.Certificate) {
	t.Helper()
	if err := os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestPebble obtains a certificate from Pebble, answering its HTTP-01
// challenge on a server of the test's, renews it when it is due, and has a
// second ACME on the same storage serve it without obtaining another.
func TestPebble(t *testing.T) {
	port := pebbletest.FreePort(t)
	p := pebbletest.Start(t, pebbletest.Options{HTTPPort: port, Validity: time.Hour})
	var l logs
	dir := t.TempDir()
	a := testACME(t, &config.ACME{Directory: p.Directory, CAFile: p.CAFile, Storage: dir, Email: "ops@fit.test"}, &l)
	// Renewed 3s after it is obtained, rather than after 40 minutes.
	a.renewAt = func(leaf *x509.Certificate) time.Time { return leaf.NotBefore.Add(3 * time.Second) }
	var answered atomic.Int32
	srv := &http.Server{Addr: fmt.Sprintf("127.0.0.1:%d", port), Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		answer, ok := a.HTTPChallenge(strings.TrimPrefix(r.URL.Path, ACMEChallengePrefix))
		if !ok || !strings.HasPrefix(r.URL.Path, ACMEChallengePrefix) {
			http.NotFound(w, r)
			return
		}
		answered.Add(1)
		_, _ = w.Write([]byte(answer))
	})}
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	defer srv.Close()

	a.start()
	a.Manage([]string{"www.fit.test"})
	t.Cleanup(func() { l.mu.Lock(); t.Log(strings.Join(l.lines, "\n")); l.mu.Unlock() })
	eventually(t, 30*time.Second, "a certificate from pebble", func() bool { return a.Certificate("www.fit.test") != nil })
	first := a.Certificate("www.fit.test")
	roots := p.Roots(t)
	verify := func(c *tls.Certificate) {
		t.Helper()
		inter := x509.NewCertPool()
		for _, der := range c.Certificate[1:] {
			ic, _ := x509.ParseCertificate(der)
			inter.AddCert(ic)
		}
		if _, err := c.Leaf.Verify(x509.VerifyOptions{DNSName: "www.fit.test", Roots: roots, Intermediates: inter}); err != nil {
			t.Fatal(err)
		}
	}
	verify(first)
	if answered.Load() == 0 {
		t.Error("no challenge was answered")
	}
	if _, err := a.storage.Load(context.Background(), a.accountKey()); err != nil {
		t.Errorf("no account key stored: %v", err)
	}

	eventually(t, 30*time.Second, "the certificate renewed", func() bool {
		c := a.Certificate("www.fit.test")
		return c != nil && c.Leaf.SerialNumber.Cmp(first.Leaf.SerialNumber) != 0
	})
	renewed := a.Certificate("www.fit.test")
	verify(renewed)

	// A second instance on the storage, with renewal far off: it serves
	// the stored certificate, the renewed one, and obtains none.
	var l2 logs
	b := testACME(t, &config.ACME{Directory: p.Directory, CAFile: p.CAFile, Storage: dir}, &l2)
	b.start()
	b.Manage([]string{"www.fit.test"})
	eventually(t, 10*time.Second, "the stored certificate", func() bool { return b.Certificate("www.fit.test") != nil })
	if got := b.Certificate("www.fit.test").Leaf.SerialNumber; got.Cmp(first.Leaf.SerialNumber) == 0 {
		t.Errorf("the second instance serves the first certificate, not a renewed one")
	}
	if n := l2.count("obtained"); n != 0 {
		t.Errorf("the second instance obtained %d certificates", n)
	}
}

// serveHTTP01 answers a's HTTP-01 challenges on 127.0.0.1:port, counting
// them, until the test ends.
func serveHTTP01(t *testing.T, a *ACME, port int) *atomic.Int32 {
	t.Helper()
	var answered atomic.Int32
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		answer, ok := a.HTTPChallenge(strings.TrimPrefix(r.URL.Path, ACMEChallengePrefix))
		if !ok || !strings.HasPrefix(r.URL.Path, ACMEChallengePrefix) {
			http.NotFound(w, r)
			return
		}
		answered.Add(1)
		_, _ = w.Write([]byte(answer))
	})}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return &answered
}

// serveTLSALPN01 has a TLS server on 127.0.0.1:port take handshakes with
// certs, as a listener does, counting those that negotiate ACMEALPN.
func serveTLSALPN01(t *testing.T, certs *Certs, port int) *atomic.Int32 {
	t.Helper()
	var answered atomic.Int32
	ln, err := tls.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port), ServerConfig([]string{"http/1.1", "h2"}, certs.GetCertificate))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				tc := c.(*tls.Conn)
				_ = tc.SetDeadline(time.Now().Add(5 * time.Second))
				if tc.Handshake() == nil && tc.ConnectionState().NegotiatedProtocol == ACMEALPN {
					answered.Add(1)
				}
			}()
		}
	}()
	return &answered
}

// TestPebbleTLSALPN obtains a certificate from Pebble through TLS-ALPN-01
// alone, answered by a TLS server whose configuration is a listener's.
func TestPebbleTLSALPN(t *testing.T) {
	tlsPort := pebbletest.FreePort(t)
	p := pebbletest.Start(t, pebbletest.Options{HTTPPort: pebbletest.FreePort(t), TLSPort: tlsPort})
	var l logs
	a := testACME(t, &config.ACME{Directory: p.Directory, CAFile: p.CAFile, Challenges: []string{"tls-alpn-01"}}, &l)
	answered := serveTLSALPN01(t, NewCerts(nil, a, []string{"alpn.fit.test"}, false), tlsPort)
	a.start()
	a.Manage([]string{"alpn.fit.test"})
	t.Cleanup(func() { l.mu.Lock(); t.Log(strings.Join(l.lines, "\n")); l.mu.Unlock() })
	eventually(t, 30*time.Second, "a certificate from pebble", func() bool { return a.Certificate("alpn.fit.test") != nil })
	if answered.Load() == 0 {
		t.Error("no tls-alpn-01 challenge was answered")
	}
	if a.ALPNChallenge("alpn.fit.test") != nil {
		t.Error("the challenge's certificate is kept once the challenge is over")
	}
}

// TestChallengeFallback has a CA check a challenge nobody answers first: the
// attempt after the failure starts with the next, which is answered.
func TestChallengeFallback(t *testing.T) {
	httpPort := pebbletest.FreePort(t)
	p := pebbletest.Start(t, pebbletest.Options{HTTPPort: httpPort})
	var l logs
	a := testACME(t, &config.ACME{Directory: p.Directory, CAFile: p.CAFile, Challenges: []string{"tls-alpn-01", "http-01"}}, &l)
	answered := serveHTTP01(t, a, httpPort)
	a.start()
	a.Manage([]string{"fallback.fit.test"})
	t.Cleanup(func() { l.mu.Lock(); t.Log(strings.Join(l.lines, "\n")); l.mu.Unlock() })
	eventually(t, 30*time.Second, "a certificate from pebble", func() bool { return a.Certificate("fallback.fit.test") != nil })
	if l.count("trying again") == 0 || answered.Load() == 0 {
		t.Errorf("failures %d, http-01 answered %d: the first attempt should fail and the next use http-01", l.count("trying again"), answered.Load())
	}
}

func TestCertsALPN(t *testing.T) {
	var l logs
	a := testACME(t, &config.ACME{Directory: "https://127.0.0.1:1/dir"}, &l)
	certs := NewCerts(nil, a, []string{"a.fit.test"}, false)
	alpn := func(name string) *tls.ClientHelloInfo {
		h := hello(name)
		h.SupportedProtos = []string{ACMEALPN}
		return h
	}
	if _, err := certs.GetCertificate(alpn("a.fit.test")); err == nil {
		t.Error("a certificate with no challenge under way")
	}
	challenge := keyCert(t, false, "a.fit.test")
	a.alpnCerts.Store("a.fit.test", challenge)
	other := keyCert(t, false, "b.fit.test")
	a.alpnCerts.Store("b.fit.test", other)
	if got, err := certs.GetCertificate(alpn("A.fit.test.")); err != nil || got != challenge {
		t.Errorf("the challenge's certificate: %v, %v", got, err)
	}
	if _, err := certs.GetCertificate(alpn("b.fit.test")); err == nil {
		t.Error("the challenge of a name the listener does not list was answered")
	}
	// A client that offers other protocols too is no CA checking a
	// challenge.
	h := hello("a.fit.test")
	h.SupportedProtos = []string{"h2", ACMEALPN}
	if got, _ := certs.GetCertificate(h); got == challenge {
		t.Error("a client offering h2 got the challenge's certificate")
	}
}
