package tlsmgr

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/pebbletest"
	"github.com/lesismal/fit/logs"
)

// sharedStorage is a FileStorage that says it is shared between hosts, as
// the stores of package sharedstore do, and whose loads may be held.
type sharedStorage struct {
	*FileStorage
	hold chan struct{} // when not nil, each Load waits for it to close
}

func (s *sharedStorage) Shared() bool { return true }

func (s *sharedStorage) Load(ctx context.Context, key string) ([]byte, error) {
	if s.hold != nil {
		select {
		case <-s.hold:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return s.FileStorage.Load(ctx, key)
}

func sharedACME(t *testing.T, c *config.ACME, st Storage, l *logLines) *ACME {
	t.Helper()
	a, err := newACME(c, st, logs.Func(l.logf))
	if err != nil {
		t.Fatal(err)
	}
	a.retryBase, a.retryMax = 20*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { a.Close() })
	return a
}

// lookup calls LookupHTTPChallenge and waits for its answer.
func lookup(t *testing.T, a *ACME, token string) (string, bool) {
	t.Helper()
	type answer struct {
		s  string
		ok bool
	}
	got := make(chan answer, 1)
	a.LookupHTTPChallenge(token, func(s string, ok bool) { got <- answer{s, ok} })
	select {
	case r := <-got:
		return r.s, r.ok
	case <-time.After(10 * time.Second):
		t.Fatal("no answer")
		return "", false
	}
}

// TestSharedPebble runs two ACMEs on one shared storage, as two instances
// on two hosts: the CA reaches only the second, which answers the first's
// HTTP-01 challenge from the storage; once the first has its certificate,
// the second, asked for the same name, takes it from the storage rather
// than obtaining another.
func TestSharedPebble(t *testing.T) {
	port := pebbletest.FreePort(t)
	p := pebbletest.Start(t, pebbletest.Options{HTTPPort: port})
	st := &sharedStorage{FileStorage: &FileStorage{Root: t.TempDir()}}
	c := &config.ACME{Directory: p.Directory, CAFile: p.CAFile, Challenges: []string{"http-01"}}
	var la, lb logLines
	a, b := sharedACME(t, c, st, &la), sharedACME(t, c, st, &lb)
	if !a.Shared() {
		t.Fatal("a shared storage's ACME is not shared")
	}
	var fromStorage atomic.Int32
	srv := &http.Server{Addr: fmt.Sprintf("127.0.0.1:%d", port), Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.URL.Path, ACMEChallengePrefix)
		if answer, ok := b.HTTPChallenge(token); ok {
			_, _ = w.Write([]byte(answer))
			return
		}
		answer, ok := lookup(t, b, token)
		if !ok {
			http.NotFound(w, r)
			return
		}
		fromStorage.Add(1)
		_, _ = w.Write([]byte(answer))
	})}
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	defer srv.Close()

	a.start()
	b.start()
	a.Manage([]string{"www.fit.test"})
	eventually(t, 30*time.Second, "the first's certificate", func() bool { return a.Certificate("www.fit.test") != nil })
	if fromStorage.Load() == 0 {
		t.Error("the CA's check was not answered from the shared storage")
	}
	b.Manage([]string{"www.fit.test"})
	eventually(t, 10*time.Second, "the second's certificate", func() bool { return b.Certificate("www.fit.test") != nil })
	if !bytes.Equal(a.Certificate("www.fit.test").Leaf.Raw, b.Certificate("www.fit.test").Leaf.Raw) {
		t.Error("the second obtained a certificate of its own")
	}
	if n := la.count("obtained a certificate") + lb.count("obtained a certificate"); n != 1 {
		t.Errorf("%d certificates obtained", n)
	}
	// The challenge's answer is gone from the storage.
	keys, err := st.List(context.Background(), "challenges")
	if err != nil || len(keys) != 0 {
		t.Errorf("challenges left in the storage: %q %v", keys, err)
	}
}

func TestLookupHTTPChallenge(t *testing.T) {
	var l logLines
	// Without a shared storage, nothing is looked up.
	plain := testACME(t, &config.ACME{Directory: "https://127.0.0.1:1/dir"}, &l)
	if plain.Shared() {
		t.Fatal("a directory's ACME is shared")
	}
	if _, ok := lookup(t, plain, "token"); ok {
		t.Error("a lookup without a shared storage")
	}

	st := &sharedStorage{FileStorage: &FileStorage{Root: t.TempDir()}}
	a := sharedACME(t, &config.ACME{Directory: "https://127.0.0.1:1/dir"}, st, &l)
	if err := st.Store(context.Background(), a.httpChallengeKey("tok_en-1"), []byte("tok_en-1.thumb")); err != nil {
		t.Fatal(err)
	}
	if answer, ok := lookup(t, a, "tok_en-1"); !ok || answer != "tok_en-1.thumb" {
		t.Errorf("a shared challenge: %q %v", answer, ok)
	}
	for _, bad := range []string{"none", "../../certificates/x", "a/b", "", strings.Repeat("a", 257)} {
		if _, ok := lookup(t, a, bad); ok {
			t.Errorf("token %q answered", bad)
		}
	}

	// Past maxSharedLookups under way, a lookup is answered at once, as
	// one with no challenge.
	st.hold = make(chan struct{})
	results := make(chan bool, maxSharedLookups)
	for range maxSharedLookups {
		a.LookupHTTPChallenge("tok_en-1", func(_ string, ok bool) { results <- ok })
	}
	eventually(t, 5*time.Second, "every slot taken", func() bool { return len(a.lookups) == maxSharedLookups })
	called := false
	a.LookupHTTPChallenge("tok_en-1", func(_ string, ok bool) { called = !ok })
	if !called {
		t.Error("a lookup past the bound waited")
	}
	close(st.hold)
	for range maxSharedLookups {
		if !<-results {
			t.Error("a held lookup found nothing")
		}
	}
}

func TestCertsSharedALPN(t *testing.T) {
	var l logLines
	st := &sharedStorage{FileStorage: &FileStorage{Root: t.TempDir()}}
	a := sharedACME(t, &config.ACME{Directory: "https://127.0.0.1:1/dir"}, st, &l)
	certs := NewCerts(nil, a, []string{"a.fit.test", "b.fit.test"}, false)
	alpn := func(name string) *tls.ClientHelloInfo {
		h := hello(name)
		h.SupportedProtos = []string{ACMEALPN}
		return h
	}
	if _, err := certs.GetCertificate(alpn("a.fit.test")); err == nil {
		t.Error("a certificate with no challenge under way")
	}
	challenge := keyCert(t, false, "a.fit.test")
	data, err := encodeCert(challenge)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, name := range []string{"a.fit.test", "c.fit.test"} {
		if err := st.Store(ctx, a.alpnChallengeKey(name), data); err != nil {
			t.Fatal(err)
		}
	}
	got, err := certs.GetCertificate(alpn("A.fit.test."))
	if err != nil || !bytes.Equal(got.Certificate[0], challenge.Certificate[0]) {
		t.Errorf("another instance's challenge: %v", err)
	}
	if _, err := certs.GetCertificate(alpn("c.fit.test")); err == nil {
		t.Error("the challenge of a name the listener does not list was answered")
	}
	// What is in memory comes first.
	mine := keyCert(t, false, "a.fit.test")
	a.alpnCerts.Store("a.fit.test", mine)
	if got, _ := certs.GetCertificate(alpn("a.fit.test")); got != mine {
		t.Error("the storage's challenge was taken over this instance's")
	}
	if err := checkName("a.fit.test/../x"); err == nil {
		t.Error("a name with a slash was taken")
	}
}

// countingStorage is a sharedStorage that counts its loads, by key.
type countingStorage struct {
	*sharedStorage
	loads sync.Map // key -> *atomic.Int64
}

func (s *countingStorage) Load(ctx context.Context, key string) ([]byte, error) {
	v, _ := s.loads.LoadOrStore(key, &atomic.Int64{})
	v.(*atomic.Int64).Add(1)
	return s.sharedStorage.Load(ctx, key)
}

func (s *countingStorage) count(key string) int64 {
	v, ok := s.loads.Load(key)
	if !ok {
		return 0
	}
	return v.(*atomic.Int64).Load()
}

// TestCertsSharedALPNOnDemand has a CA's ClientHellos, ACMEALPN alone,
// reach a listener with on-demand TLS: another instance's challenge is
// looked up in the shared storage for a listed name, a name the instance
// manages and a name the ask URL allows, and never for a name it refuses,
// nor for one that cannot be a host name; each name is asked about once a
// minute at most.
func TestCertsSharedALPNOnDemand(t *testing.T) {
	var l logLines
	ask := newAsker(t, "ok.fit.test")
	st := &countingStorage{sharedStorage: &sharedStorage{FileStorage: &FileStorage{Root: t.TempDir()}}}
	a := sharedACME(t, &config.ACME{Directory: "https://127.0.0.1:1/dir",
		OnDemand: &config.OnDemand{Ask: ask.srv.URL}}, st, &l)
	certs := NewCerts(nil, a, []string{"a.fit.test"}, true)
	alpn := func(name string) *tls.ClientHelloInfo {
		h := hello(name)
		h.SupportedProtos = []string{ACMEALPN}
		return h
	}
	names := []string{"a.fit.test", "ok.fit.test", "m.fit.test", "random-1234.fit.test", "nodot"}
	challenge := keyCert(t, false, "x.fit.test")
	data, err := encodeCert(challenge)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if err := st.Store(context.Background(), a.alpnChallengeKey(name), data); err != nil {
			t.Fatal(err)
		}
	}
	a.mu.Lock()
	a.names["m.fit.test"] = &managed{name: "m.fit.test", due: time.Now().Add(time.Hour)}
	a.mu.Unlock()

	for range 3 {
		for _, name := range []string{"a.fit.test", "ok.fit.test", "m.fit.test"} {
			got, err := certs.GetCertificate(alpn(name))
			if err != nil || !bytes.Equal(got.Certificate[0], challenge.Certificate[0]) {
				t.Errorf("%s: another instance's challenge: %v", name, err)
			}
		}
		for _, name := range []string{"random-1234.fit.test", "nodot"} {
			if _, err := certs.GetCertificate(alpn(name)); !errors.Is(err, errNoChallenge) {
				t.Errorf("%s: %v, want errNoChallenge", name, err)
			}
		}
	}
	for _, name := range names {
		want := int64(3)
		if name == "random-1234.fit.test" || name == "nodot" {
			want = 0
		}
		if n := st.count(a.alpnChallengeKey(name)); n != want {
			t.Errorf("%s: looked up %d times, want %d", name, n, want)
		}
	}
	for name, want := range map[string]int{"a.fit.test": 0, "m.fit.test": 0, "ok.fit.test": 1, "random-1234.fit.test": 1, "nodot": 0} {
		if n := ask.count(name); n != want {
			t.Errorf("%s: asked %d times, want %d", name, n, want)
		}
	}

	// Without a shared storage, nothing is asked about.
	b := testACME(t, &config.ACME{Directory: "https://127.0.0.1:1/dir",
		OnDemand: &config.OnDemand{Ask: ask.srv.URL}}, &l)
	if _, err := NewCerts(nil, b, nil, true).GetCertificate(alpn("other.fit.test")); !errors.Is(err, errNoChallenge) {
		t.Errorf("no shared storage: %v", err)
	}
	if n := ask.count("other.fit.test"); n != 0 {
		t.Errorf("asked %d times with no shared storage", n)
	}

	// An ask URL that does not answer allows nothing, and is not taken
	// for a refusal.
	c := sharedACME(t, &config.ACME{Directory: "https://127.0.0.1:1/dir",
		OnDemand: &config.OnDemand{Ask: "http://127.0.0.1:1/ask"}}, st, &l)
	if _, err := NewCerts(nil, c, nil, true).GetCertificate(alpn("ok.fit.test")); !errors.Is(err, errNoChallenge) {
		t.Errorf("no answer from the ask URL: %v", err)
	}
	if n := len(c.onDemand.refused); n != 0 {
		t.Errorf("%d names remembered as refused without an answer", n)
	}
}
