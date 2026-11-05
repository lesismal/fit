package tlsmgr

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strings"
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
	// loads counts the loads of challenges, and markerLoads those of the
	// marker of challenges in progress.
	loads, markerLoads atomic.Int32
}

func (s *sharedStorage) Shared() bool { return true }

func (s *sharedStorage) Load(ctx context.Context, key string) ([]byte, error) {
	if strings.HasSuffix(key, "/challenges-in-progress") {
		s.markerLoads.Add(1)
	} else {
		s.loads.Add(1)
	}
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

// lookup calls LookupHTTPChallenge, for a client of its own, and waits for
// its answer.
func lookup(t *testing.T, a *ACME, token string) (string, bool) {
	return lookupFrom(t, a, token, "192.0.2.1:4321")
}

// lookupFrom is lookup for client.
func lookupFrom(t *testing.T, a *ACME, token, client string) (string, bool) {
	t.Helper()
	type answer struct {
		s  string
		ok bool
	}
	got := make(chan answer, 1)
	a.LookupHTTPChallenge(token, client, func(s string, ok bool) { got <- answer{s, ok} })
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
	a.writeMarker(context.Background())
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
		// No IP address, so no client's bound.
		a.LookupHTTPChallenge("tok_en-1", "", func(_ string, ok bool) { results <- ok })
	}
	eventually(t, 5*time.Second, "every slot taken", func() bool { return len(a.lookups) == maxSharedLookups })
	called := false
	a.LookupHTTPChallenge("tok_en-1", "", func(_ string, ok bool) { called = !ok })
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
	a.writeMarker(context.Background())
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
	// A name looked up in vain is not looked up again for missFor, which
	// the instance with the challenge waits out before the CA checks it.
	if _, err := certs.GetCertificate(alpn("a.fit.test")); err == nil {
		t.Error("a name looked up in vain was looked up again at once")
	}
	a.now = func() time.Time { return time.Now().Add(missFor) }
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

// TestSharedGate floods an instance with a shared storage, while no
// instance has a challenge in progress, with handshakes offering acme-tls/1
// alone and requests for HTTP-01 tokens: the storage is asked for none of
// them, only for the marker, at most once each gateFresh. Once another
// instance marks a challenge in progress, it is looked up within gateFresh.
func TestSharedGate(t *testing.T) {
	var l logLines
	st := &sharedStorage{FileStorage: &FileStorage{Root: t.TempDir()}}
	c := &config.ACME{Directory: "https://127.0.0.1:1/dir"}
	a, b := sharedACME(t, c, st, &l), sharedACME(t, c, st, &l)
	certs := NewCerts(nil, a, nil, true)
	h := hello("x.fit.test")
	h.SupportedProtos = []string{ACMEALPN}
	for i := range 1000 {
		h.ServerName = fmt.Sprintf("r%d.fit.test", i)
		if _, err := certs.GetCertificate(h); err == nil {
			t.Fatal("a certificate with no challenge under way")
		}
		if _, ok := lookupFrom(t, a, fmt.Sprintf("tok%d", i), fmt.Sprintf("198.51.100.%d:1", i%250)); ok {
			t.Fatal("an answer with no challenge under way")
		}
	}
	if n, m := st.loads.Load(), st.markerLoads.Load(); n != 0 || m > 2 {
		t.Errorf("%d challenges and %d markers loaded with none in progress", n, m)
	}

	// b marks a challenge in progress and keeps its answer: a, which read
	// the marker lately, finds it once gateFresh has passed.
	stop := b.markInProgress(context.Background())
	defer stop()
	if err := st.Store(context.Background(), a.httpChallengeKey("tok-b"), []byte("tok-b.thumb")); err != nil {
		t.Fatal(err)
	}
	if _, ok := lookup(t, a, "tok-b"); ok {
		t.Error("the marker read again within gateFresh")
	}
	a.now = func() time.Time { return time.Now().Add(gateFresh) }
	if answer, ok := lookup(t, a, "tok-b"); !ok || answer != "tok-b.thumb" {
		t.Errorf("a challenge marked in progress: %q %v", answer, ok)
	}
	// While it is, every lookup asks the storage, within the bounds.
	before := st.markerLoads.Load()
	for range 10 {
		lookup(t, a, "tok-none")
	}
	if st.markerLoads.Load() != before {
		t.Error("a good marker read again")
	}
	// A marker that does not parse is as none.
	if err := st.Store(context.Background(), b.markerKey(), []byte("soon")); err != nil {
		t.Fatal(err)
	}
	fresh := sharedACME(t, c, st, &l)
	if _, ok := lookup(t, fresh, "tok-b"); ok {
		t.Error("a marker that does not parse taken as good")
	}
}

// TestSharedALPNBound holds the shared storage while handshakes offering
// acme-tls/1 alone look challenges up: past maxSharedLookups, a handshake
// gets no certificate at once rather than holding a handshake worker.
func TestSharedALPNBound(t *testing.T) {
	var l logLines
	st := &sharedStorage{FileStorage: &FileStorage{Root: t.TempDir()}}
	a := sharedACME(t, &config.ACME{Directory: "https://127.0.0.1:1/dir"}, st, &l)
	a.writeMarker(context.Background())
	st.hold = make(chan struct{})
	held := make(chan *tls.Certificate, maxSharedLookups)
	for i := range maxSharedLookups {
		go func() { held <- a.sharedALPNChallenge(fmt.Sprintf("h%d.fit.test", i)) }()
	}
	eventually(t, 5*time.Second, "every slot taken", func() bool { return len(a.lookups) == maxSharedLookups })
	done := make(chan *tls.Certificate, 1)
	go func() { done <- a.sharedALPNChallenge("past.fit.test") }()
	select {
	case cert := <-done:
		if cert != nil {
			t.Error("a certificate past the bound")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a handshake past the bound waited for the storage")
	}
	close(st.hold)
	for range maxSharedLookups {
		<-held
	}
	// The names looked up in vain are not looked up again within missFor.
	before := st.loads.Load()
	for i := range maxSharedLookups {
		a.sharedALPNChallenge(fmt.Sprintf("h%d.fit.test", i))
	}
	if st.loads.Load() != before {
		t.Error("a name looked up in vain looked up again at once")
	}
	// The misses remembered are bounded.
	for i := range maxMisses + 10 {
		a.miss(fmt.Sprintf("m%d.fit.test", i), 0)
	}
	if n := len(a.gate.misses); n > maxMisses {
		t.Errorf("%d misses remembered", n)
	}
}

// TestSharedLookupPerClient holds the shared storage while one client asks
// for HTTP-01 tokens: past maxClientLookups it gets a 404 at once, and the
// slots left are another client's, the CA's among them.
func TestSharedLookupPerClient(t *testing.T) {
	var l logLines
	st := &sharedStorage{FileStorage: &FileStorage{Root: t.TempDir()}}
	a := sharedACME(t, &config.ACME{Directory: "https://127.0.0.1:1/dir"}, st, &l)
	a.writeMarker(context.Background())
	if err := st.Store(context.Background(), a.httpChallengeKey("real"), []byte("real.thumb")); err != nil {
		t.Fatal(err)
	}
	st.hold = make(chan struct{})
	results := make(chan bool, maxClientLookups+1)
	for i := range maxClientLookups {
		// One host: ports, and the addresses of one IPv6 /64, count as one.
		client := fmt.Sprintf("[2001:db8::%d]:%d", i+1, 1000+i)
		a.LookupHTTPChallenge("flood", client, func(_ string, ok bool) { results <- ok })
	}
	eventually(t, 5*time.Second, "the client's slots taken", func() bool { return len(a.lookups) == maxClientLookups })
	refused := false
	a.LookupHTTPChallenge("flood", "[2001:db8::99]:1", func(_ string, ok bool) { refused = !ok })
	if !refused || len(a.lookups) != maxClientLookups {
		t.Error("a client past its bound looked up")
	}
	// Another client, whose counter is not the flooding one's.
	other := ""
	for i := range 256 {
		c := fmt.Sprintf("203.0.113.%d:443", i)
		if a.clientSlot(c) != a.clientSlot("[2001:db8::1]:1") {
			other = c
			break
		}
	}
	ca := make(chan bool, 1)
	a.LookupHTTPChallenge("real", other, func(answer string, ok bool) { ca <- ok && answer == "real.thumb" })
	close(st.hold)
	if !<-ca {
		t.Error("another client's lookup failed")
	}
	for range maxClientLookups {
		<-results
	}
	if a.clientSlot("not an address") != nil {
		t.Error("a counter for no IP address")
	}
	if a.clientSlot("192.0.2.7") == nil || a.clientSlot("[::ffff:192.0.2.7]:1") != a.clientSlot("192.0.2.7:2") {
		t.Error("an IPv4 client's counter")
	}
	eventually(t, 5*time.Second, "the counters back to zero", func() bool {
		for i := range a.gate.clients {
			if a.gate.clients[i].Load() != 0 {
				return false
			}
		}
		return len(a.lookups) == 0
	})
}

// TestMarkInProgress keeps the marker while a challenge is in progress; a
// failure to write it is reported.
func TestMarkInProgress(t *testing.T) {
	var l logLines
	st := &faultyStorage{Storage: &FileStorage{Root: t.TempDir()}, shared: true}
	a := sharedACME(t, &config.ACME{Directory: "https://127.0.0.1:1/dir"}, st, &l)
	ctx, cancel := context.WithCancel(context.Background())
	stop := a.markInProgress(ctx)
	data, err := st.Load(context.Background(), a.markerKey())
	if err != nil || len(data) == 0 {
		t.Fatalf("the marker: %q %v", data, err)
	}
	stop()
	// Its context done, the refresher ends too.
	stop = a.markInProgress(ctx)
	cancel()
	stop()
	st.fault = func(op, key string) error { return errFault }
	a.writeMarker(context.Background())
	if l.count("keeping the marker") != 1 {
		t.Errorf("a failed write not reported: %q", l.snapshot())
	}
}
