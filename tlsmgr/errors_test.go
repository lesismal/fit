package tlsmgr

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ocsp"

	"github.com/lesismal/fit/config"
)

// TestOCSPGetErrors has each step of fetching a response fail.
func TestOCSPGetErrors(t *testing.T) {
	r := newResponder(t)
	cert := r.issue(t, "a.fit.test")
	answer := func(h http.HandlerFunc) string {
		srv := httptest.NewServer(h)
		t.Cleanup(srv.Close)
		return srv.URL
	}
	badKey := *r.ca.Cert
	badKey.RawSubjectPublicKeyInfo = []byte{1}
	for _, tt := range []struct {
		name   string
		url    string
		issuer *x509.Certificate
		now    time.Time
		want   string
	}{
		{name: "bad issuer", url: r.srv.URL, issuer: &badKey, want: ""},
		{name: "bad URL", url: "http://a\x7f/", want: "invalid"},
		{name: "500", url: answer(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "down", 500) }), want: "answered 500"},
		{name: "cut", url: answer(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "1000")
			_, _ = w.Write([]byte("short"))
		}), want: "EOF"},
		{name: "too large", url: answer(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(make([]byte, ocspMaxBytes+1))
		}), want: "too large"},
		{name: "garbage", url: answer(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("garbage")) }), want: "asn1"},
		{name: "expired", url: r.srv.URL, now: time.Now().Add(2 * time.Hour), want: "expired"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			o := newOCSP(nil)
			defer o.Close()
			if !tt.now.IsZero() {
				o.now = func() time.Time { return tt.now }
			}
			leaf := *cert.Leaf
			leaf.OCSPServer = []string{tt.url}
			issuer := tt.issuer
			if issuer == nil {
				issuer = r.ca.Cert
			}
			_, _, err := o.get(&Staple{leaf: &leaf, issuer: issuer})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("%v, want %q", err, tt.want)
			}
		})
	}
	if got := subject(&x509.Certificate{Subject: pkix.Name{CommonName: "cn"}}); got != "cn" {
		t.Errorf("subject: %q", got)
	}
}

func TestOCSPOddResponses(t *testing.T) {
	r := newResponder(t)
	var l logs

	// A chain whose issuer does not parse gets no staple.
	o := testOCSP(t, &l)
	cert := r.issue(t, "a.fit.test")
	bad := *cert
	bad.Certificate = [][]byte{cert.Certificate[0], {1}}
	if o.Add(&bad, nil) != nil {
		t.Error("a staple for a chain whose issuer does not parse")
	}

	// Unknown to the responder: nothing stapled.
	unknown := r.issue(t, "u.fit.test")
	r.set(func(r *responder) { r.status[unknown.Leaf.SerialNumber.String()] = ocsp.Unknown })
	st := o.Add(unknown, nil)
	eventually(t, 5*time.Second, "unknown reported", func() bool { return l.count("unknown to the responder") > 0 })
	if stapled(t, st, r) != -1 {
		t.Error("a response of unknown stapled")
	}

	// A response past its half-life when fetched is fetched again before it
	// expires.
	late := newOCSP(l.logf)
	late.now = func() time.Time { return time.Now().Add(40 * time.Minute) }
	late.start()
	t.Cleanup(func() { late.Close() })
	st = late.Add(r.issue(t, "l.fit.test"), nil)
	eventually(t, 5*time.Second, "a stapled response", func() bool { return stapled(t, st, r) == ocsp.Good })
	late.mu.Lock()
	due := st.due
	late.mu.Unlock()
	if now := late.now(); !due.After(now) || due.After(now.Add(11*time.Minute)) {
		t.Errorf("due at %s, %s from now", due, due.Sub(now))
	}
}

// TestACMEStapleRevoked has the responder say a certificate ACME obtained
// is revoked: ACME obtains another.
func TestACMEStapleRevoked(t *testing.T) {
	r := newResponder(t)
	ca := newFakeCA(t)
	ca.set(func(f *fakeCA) { f.ocsp, f.ca = r.srv.URL, r.ca })
	var l logs
	a := fakeACME(t, ca, &config.ACME{}, nil, &l)
	a.UseOCSP(testOCSP(t, &l))
	a.start()
	a.Manage([]string{"r.fit.test"})
	eventually(t, 10*time.Second, "a certificate", func() bool { return a.Certificate("r.fit.test") != nil })
	first := a.Certificate("r.fit.test").Leaf.SerialNumber
	r.set(func(r *responder) { r.status[first.String()] = ocsp.Revoked })
	a.ocsp.Load().mu.Lock()
	for _, st := range a.ocsp.Load().staples {
		st.due = time.Now()
	}
	a.ocsp.Load().mu.Unlock()
	a.ocsp.Load().poke()
	eventually(t, 10*time.Second, "the revoked certificate replaced", func() bool {
		c := a.Certificate("r.fit.test")
		return c != nil && c.Leaf.SerialNumber.Cmp(first) != 0
	})
}

func TestARIErrors(t *testing.T) {
	ctx := context.Background()
	var l logs
	a := testACME(t, &config.ACME{Directory: "https://127.0.0.1:1/dir"}, &l)
	a.http = &http.Client{Timeout: 5 * time.Second}
	serve := func(h http.HandlerFunc) string {
		srv := httptest.NewServer(h)
		t.Cleanup(srv.Close)
		return srv.URL
	}
	var v any
	for _, tt := range []struct {
		name, url, want string
	}{
		{"bad URL", "http://a\x7f/", "invalid"},
		{"404", serve(http.NotFound), "404"},
		{"cut", serve(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "1000")
			_, _ = w.Write([]byte("{"))
		}), "EOF"},
		{"too large", serve(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(make([]byte, ariMaxBytes+1)) }), "too large"},
	} {
		if err := a.getJSON(ctx, tt.url, &v, nil); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: %v, want %q", tt.name, err, tt.want)
		}
	}

	leaf := storeIssued(t, a, "ari.fit.test")
	// The CA's ARI endpoint fails, or suggests no window.
	window := map[string]any{}
	ari := serve(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "fail") {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(window)
	})
	a.ariURL, a.ariKnown = ari+"/fail", true
	if s, err := a.renewalInfo(ctx, leaf); s != nil || err == nil {
		t.Errorf("a failing endpoint: %v %v", s, err)
	}
	a.ariURL = ari + "/ok"
	if s, err := a.renewalInfo(ctx, leaf); s == nil || err == nil || !strings.Contains(err.Error(), "no valid suggested window") {
		t.Errorf("no window: %v %v", s, err)
	}

	// Asked about the certificate already, and not yet time to ask again.
	cur := &ariState{serial: leaf.SerialNumber, checkAt: time.Now().Add(time.Hour)}
	m := &managed{name: "ari.fit.test", ari: cur}
	a.ariURL = ari + "/fail"
	a.checkARI(ctx, m, leaf)
	if m.ari != cur || l.count("renewal information") != 0 {
		t.Error("asked again before it was time")
	}
}

func TestOnDemandOdds(t *testing.T) {
	dir, caFile := hangingCA(t)
	ask := newAsker(t, "a.fit.test", "listed.fit.test")
	var l logs
	a := testACME(t, &config.ACME{Directory: dir, CAFile: caFile, OnDemand: &config.OnDemand{Ask: ask.srv.URL,
		Timeout: config.Duration(300 * time.Millisecond)}}, &l)
	od := a.onDemand

	// A second handshake for a name being taken on waits for the same.
	first := make(chan error, 1)
	go func() {
		_, err := a.OnDemand("a.fit.test")
		first <- err
	}()
	eventually(t, 5*time.Second, "the name being taken on", func() bool {
		od.mu.Lock()
		defer od.mu.Unlock()
		return od.waits["a.fit.test"] != nil
	})
	if _, err := a.OnDemand("a.fit.test"); !errors.Is(err, ErrOnDemandTimeout) {
		t.Errorf("the second handshake: %v", err)
	}
	if err := <-first; !errors.Is(err, ErrOnDemandTimeout) {
		t.Errorf("the first handshake: %v", err)
	}

	// A name listed meanwhile is not counted on demand.
	a.mu.Lock()
	a.names["listed.fit.test"] = &managed{name: "listed.fit.test", due: time.Now().Add(time.Hour)}
	a.mu.Unlock()
	od.mu.Lock()
	before := od.names
	od.mu.Unlock()
	if _, err := a.OnDemand("listed.fit.test"); !errors.Is(err, ErrOnDemandTimeout) {
		t.Errorf("a listed name: %v", err)
	}
	od.mu.Lock()
	after := od.names
	od.mu.Unlock()
	if after != before {
		t.Errorf("a listed name counted on demand: %d, then %d", before, after)
	}

	// Past maxRefused names refused, they are forgotten.
	od.mu.Lock()
	for i := range maxRefused {
		od.refused[fmt.Sprintf("n%d.fit.test", i)] = time.Now().Add(time.Hour)
	}
	od.mu.Unlock()
	if err := a.allow("no.fit.test"); !errors.Is(err, ErrOnDemandRefused) {
		t.Errorf("a refused name: %v", err)
	}
	od.mu.Lock()
	n := len(od.refused)
	od.mu.Unlock()
	if n != 1 {
		t.Errorf("%d names remembered as refused", n)
	}

	// An ask URL that makes no request.
	saved := od.ask
	od.ask = &url.URL{Scheme: "http", Host: "a b"}
	if err := od.askFor("b.fit.test"); err == nil {
		t.Error("an ask URL that makes no request")
	}
	od.ask = saved

	// A run that ends with neither a certificate nor an error, a name never
	// taken on, and an ACME without on-demand TLS.
	w := &onDemandWait{done: make(chan struct{})}
	od.mu.Lock()
	od.waits["c.fit.test"] = w
	od.mu.Unlock()
	od.finish("c.fit.test", nil, nil)
	if !errors.Is(w.err, ErrOnDemandTimeout) {
		t.Errorf("a run with nothing to show: %v", w.err)
	}
	od.touch("never.fit.test", time.Now())
	if _, ok := od.idleUntil("never.fit.test"); ok {
		t.Error("a name never taken on is idle")
	}
	(*onDemand)(nil).drop("x.fit.test")
	(*onDemand)(nil).finish("x.fit.test", nil, nil)
}

func TestStaticOdds(t *testing.T) {
	if _, err := readFile(t.TempDir()); err == nil {
		t.Error("a directory read as a file")
	}
	leaf := &x509.Certificate{DNSNames: []string{"a.fit.test", "A.fit.test"}}
	s := NewStatic(&tls.Certificate{}, &tls.Certificate{Leaf: leaf})
	if got := s.exact["a.fit.test"]; len(got) != 1 || got[0] != 1 {
		t.Errorf("a name twice in one certificate: %v", got)
	}
	c := NewCerts(s, nil, nil, false)
	if c.Static() != s {
		t.Error("Static")
	}
	c.Release()
	NewCerts(nil, nil, nil, false).Release()
}

func TestAddCertsBad(t *testing.T) {
	pool := x509.NewCertPool()
	data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("garbage")})
	if err := addCerts(pool, data); err == nil {
		t.Error("a certificate that does not parse was added")
	}
}

func TestFileWatcherOdds(t *testing.T) {
	fw := NewFileWatcher(WatchOptions{}, nil, nil, nil)
	if fw.opts.Interval != DefaultWatchInterval || fw.opts.Debounce != DefaultWatchDebounce {
		t.Errorf("defaults: %+v", fw.opts)
	}
	fw.report(errors.New("heard by nobody"))

	// Run looks at the files every interval until its context ends.
	var looks atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	fw = NewFileWatcher(WatchOptions{Interval: time.Millisecond}, func() (uint64, map[string][sha256.Size]byte) {
		looks.Add(1)
		return 0, nil
	}, func() error { return nil }, nil)
	done := make(chan struct{})
	go func() { fw.Run(ctx); close(done) }()
	eventually(t, 5*time.Second, "a look", func() bool { return looks.Load() > 1 })
	cancel()
	<-done

	// A pair that failed, changed and changed back to as it failed, is not
	// tried again.
	dir := t.TempDir()
	cert := filepath.Join(dir, "a.crt")
	w := &watched{t: t, mtime: time.Now().Add(-time.Hour), files: map[string][sha256.Size]byte{cert: {}}}
	w.write(cert, "cert 1")
	w.load()
	fw = NewFileWatcher(WatchOptions{Interval: time.Second, Debounce: time.Second}, w.current, w.apply,
		func(err error) { w.reports = append(w.reports, err) })
	now := time.Now()
	step := func() {
		now = now.Add(time.Second)
		fw.tick(now)
	}
	settle := func() {
		for range 3 {
			step()
		}
	}
	settle()
	w.fail = errors.New("bad pair")
	w.write(cert, "cert F")
	settle()
	if w.applies != 1 {
		t.Fatalf("%d applies", w.applies)
	}
	w.write(cert, "cert G")
	step()
	w.write(cert, "cert F")
	settle()
	if w.applies != 1 || len(w.reports) != 1 {
		t.Errorf("tried again as it failed: %d applies, reports %v", w.applies, w.reports)
	}
}

func TestFileStorageErrors(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s := &FileStorage{Root: root}
	// A key that is a directory cannot be loaded, nor stored over, nor
	// deleted while it holds files.
	if err := s.Store(ctx, "dir/file", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(ctx, "dir"); err == nil {
		t.Error("a directory loaded")
	}
	if err := s.Store(ctx, "dir", []byte("x")); err == nil {
		t.Error("a value stored over a directory")
	}
	if entries, _ := os.ReadDir(root); len(entries) != 1 {
		t.Errorf("left behind: %v", entries)
	}
	if err := s.Delete(ctx, "dir"); err == nil {
		t.Error("a directory with files deleted")
	}
	// Under a file, nothing can be stored, listed or locked.
	if err := s.Store(ctx, "dir/file/x", []byte("x")); err == nil {
		t.Error("a value stored under a file")
	}
	under := &FileStorage{Root: filepath.Join(root, "dir", "file", "sub")}
	if _, err := under.List(ctx, ""); err == nil && runtime.GOOS != "windows" {
		t.Error("a root under a file listed")
	}
	if err := under.Lock(ctx, "l"); err == nil {
		t.Error("a lock taken under a file")
	}
	// Bad keys and names.
	if err := s.Delete(ctx, "../x"); err == nil {
		t.Error("a bad key deleted")
	}
	if _, err := s.List(ctx, "a/../b"); err == nil {
		t.Error("a bad prefix listed")
	}
	if err := s.Lock(ctx, `a\b`); err == nil {
		t.Error("a bad lock taken")
	}
	if err := s.Unlock(ctx, `a\b`); err == nil {
		t.Error("a bad lock given back")
	}
	// A context done.
	done, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.Delete(done, "dir/file"); !errors.Is(err, context.Canceled) {
		t.Errorf("Delete with a context done: %v", err)
	}
	if _, err := s.List(done, ""); !errors.Is(err, context.Canceled) {
		t.Errorf("List with a context done: %v", err)
	}
	// A lock file that becomes a directory with files cannot be given
	// back.
	if err := s.Lock(ctx, "held"); err != nil {
		t.Fatal(err)
	}
	file, _ := s.lockFile("held")
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(file, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.Unlock(ctx, "held"); err == nil {
		t.Error("a lock that is a directory given back")
	}
}
