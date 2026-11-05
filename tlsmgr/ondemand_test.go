package tlsmgr

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/pebbletest"
)

// asker is an ask URL that allows the names it is told, and counts what it
// is asked.
type asker struct {
	srv *httptest.Server

	mu      sync.Mutex
	allowed map[string]bool
	asked   map[string]int
}

func newAsker(t *testing.T, allowed ...string) *asker {
	t.Helper()
	a := &asker{allowed: map[string]bool{}, asked: map[string]int{}}
	for _, n := range allowed {
		a.allowed[n] = true
	}
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("domain")
		a.mu.Lock()
		a.asked[name]++
		ok := a.allowed[name]
		a.mu.Unlock()
		if !ok {
			http.Error(w, "no", http.StatusForbidden)
		}
	}))
	t.Cleanup(a.srv.Close)
	return a
}

func (a *asker) count(name string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.asked[name]
}

// hangingCA is a CA whose directory never answers, so that names taken on
// stay pending.
func hangingCA(t *testing.T) (directory, caFile string) {
	t.Helper()
	release := make(chan struct{})
	ca := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(func() { close(release); ca.Close() })
	caFile = t.TempDir() + "/ca.pem"
	writePEM(t, caFile, ca.Certificate())
	return ca.URL + "/dir", caFile
}

// TestOnDemandAllowed has several handshakes at once ask for a name the ask
// URL allows: one certificate is obtained from Pebble, all of them get it,
// and the ask URL is asked once.
func TestOnDemandAllowed(t *testing.T) {
	port := pebbletest.FreePort(t)
	p := pebbletest.Start(t, pebbletest.Options{HTTPPort: port})
	ask := newAsker(t, "ok.fit.test")
	var l logLines
	a := testACME(t, &config.ACME{Directory: p.Directory, CAFile: p.CAFile, Challenges: []string{"http-01"},
		OnDemand: &config.OnDemand{Ask: ask.srv.URL + "/check?key=1", Timeout: config.Duration(30 * time.Second)}}, &l)
	serveHTTP01(t, a, port)
	a.start()
	t.Cleanup(func() { l.mu.Lock(); t.Log(strings.Join(l.lines, "\n")); l.mu.Unlock() })
	certs := NewCerts(nil, a, nil, true)

	var wg sync.WaitGroup
	got := make([]*struct {
		serial string
		err    error
	}, 4)
	for i := range got {
		wg.Go(func() {
			c, err := certs.GetCertificate(hello("ok.fit.test"))
			got[i] = &struct {
				serial string
				err    error
			}{err: err}
			if c != nil {
				got[i].serial = c.Leaf.SerialNumber.String()
			}
		})
	}
	wg.Wait()
	for i, g := range got {
		if g.err != nil || g.serial != got[0].serial {
			t.Errorf("handshake %d: %+v, first %+v", i, g, got[0])
		}
	}
	if n := ask.count("ok.fit.test"); n != 1 {
		t.Errorf("asked %d times", n)
	}
	if n := l.count("obtained"); n != 1 {
		t.Errorf("%d certificates obtained", n)
	}
	// From memory now, without asking; and a reload that lists no names
	// keeps it.
	a.Manage(nil)
	if c, err := certs.GetCertificate(hello("OK.fit.test.")); err != nil || c.Leaf.SerialNumber.String() != got[0].serial {
		t.Errorf("again: %v, %v", c, err)
	}
	if n := ask.count("ok.fit.test"); n != 1 {
		t.Errorf("asked %d times", n)
	}
}

func TestOnDemandRefused(t *testing.T) {
	dir, caFile := hangingCA(t)
	ask := newAsker(t)
	var l logLines
	a := testACME(t, &config.ACME{Directory: dir, CAFile: caFile, OnDemand: &config.OnDemand{Ask: ask.srv.URL}}, &l)
	var now atomic.Pointer[time.Time]
	start := time.Now()
	now.Store(&start)
	a.now = func() time.Time { return *now.Load() }
	a.start()
	certs := NewCerts(nil, a, nil, true)
	for range 3 {
		if _, err := certs.GetCertificate(hello("no.fit.test")); !errors.Is(err, ErrOnDemandRefused) {
			t.Fatalf("a refused name: %v", err)
		}
	}
	if n := ask.count("no.fit.test"); n != 1 {
		t.Errorf("a refused name was asked about %d times within a minute", n)
	}
	later := start.Add(refusedFor + time.Second)
	now.Store(&later)
	if _, err := certs.GetCertificate(hello("no.fit.test")); !errors.Is(err, ErrOnDemandRefused) || ask.count("no.fit.test") != 2 {
		t.Errorf("after a minute: %v, asked %d times", err, ask.count("no.fit.test"))
	}
	a.mu.Lock()
	n := len(a.names)
	a.mu.Unlock()
	if n != 0 {
		t.Errorf("a refused name was taken on")
	}

	// An ask URL that does not answer refuses, but is asked again next time.
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	b := testACME(t, &config.ACME{Directory: dir, CAFile: caFile, OnDemand: &config.OnDemand{Ask: down.URL}}, &l)
	b.start()
	for range 2 {
		if _, err := NewCerts(nil, b, nil, true).GetCertificate(hello("x.fit.test")); err == nil || errors.Is(err, ErrOnDemandRefused) {
			t.Errorf("an ask URL that does not answer: %v", err)
		}
	}
	b.onDemand.mu.Lock()
	cached := len(b.onDemand.refused)
	b.onDemand.mu.Unlock()
	if cached != 0 {
		t.Error("a name was remembered as refused without an answer")
	}
}

func TestOnDemandRateLimited(t *testing.T) {
	dir, caFile := hangingCA(t)
	ask := newAsker(t, "a.fit.test", "b.fit.test")
	var l logLines
	a := testACME(t, &config.ACME{Directory: dir, CAFile: caFile, OnDemand: &config.OnDemand{Ask: ask.srv.URL,
		Rate: 1, Interval: config.Duration(time.Hour), Timeout: config.Duration(100 * time.Millisecond)}}, &l)
	var now atomic.Pointer[time.Time]
	start := time.Now()
	now.Store(&start)
	a.now = func() time.Time { return *now.Load() }
	a.start()
	certs := NewCerts(nil, a, nil, true)
	// Taken on, but the CA never answers: the handshake gives up.
	begin := time.Now()
	if _, err := certs.GetCertificate(hello("a.fit.test")); !errors.Is(err, ErrOnDemandTimeout) {
		t.Fatalf("the first name: %v", err)
	}
	if d := time.Since(begin); d > 2*time.Second {
		t.Errorf("the handshake waited %v, past the timeout", d)
	}
	if _, err := certs.GetCertificate(hello("b.fit.test")); !errors.Is(err, ErrOnDemandRateLimited) {
		t.Fatalf("the second within the hour: %v", err)
	}
	if n := ask.count("b.fit.test"); n != 0 {
		t.Errorf("a name past the rate was asked about %d times", n)
	}
	later := start.Add(time.Hour + time.Second)
	now.Store(&later)
	if _, err := certs.GetCertificate(hello("b.fit.test")); !errors.Is(err, ErrOnDemandTimeout) {
		t.Errorf("an hour later: %v", err)
	}
}

func TestOnDemandMaxNames(t *testing.T) {
	dir, caFile := hangingCA(t)
	ask := newAsker(t, "a.fit.test", "b.fit.test")
	var l logLines
	a := testACME(t, &config.ACME{Directory: dir, CAFile: caFile, OnDemand: &config.OnDemand{Ask: ask.srv.URL,
		MaxNames: 1, Timeout: config.Duration(50 * time.Millisecond)}}, &l)
	a.start()
	certs := NewCerts(nil, a, nil, true)
	if _, err := certs.GetCertificate(hello("a.fit.test")); !errors.Is(err, ErrOnDemandTimeout) {
		t.Fatalf("the first name: %v", err)
	}
	if _, err := certs.GetCertificate(hello("b.fit.test")); !errors.Is(err, ErrOnDemandTooMany) {
		t.Fatalf("past max_names: %v", err)
	}
}

// TestOnDemandDropped has a CA that cuts every connection: the name taken on
// is dropped once its first certificate fails, the handshake waiting for it
// hears why, and it no longer counts against max_names.
func TestOnDemandDropped(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	ask := newAsker(t, "a.fit.test")
	var l logLines
	a := testACME(t, &config.ACME{Directory: "https://" + ln.Addr().String() + "/dir",
		OnDemand: &config.OnDemand{Ask: ask.srv.URL, MaxNames: 1, Timeout: config.Duration(5 * time.Second)}}, &l)
	a.start()
	certs := NewCerts(nil, a, nil, true)
	_, err = certs.GetCertificate(hello("a.fit.test"))
	if err == nil || errors.Is(err, ErrOnDemandTimeout) {
		t.Fatalf("a name whose certificate fails: %v", err)
	}
	a.mu.Lock()
	n := len(a.names)
	a.mu.Unlock()
	a.onDemand.mu.Lock()
	counted := a.onDemand.names
	a.onDemand.mu.Unlock()
	if n != 0 || counted != 0 || l.count("dropped") != 1 {
		t.Errorf("%d names kept, %d counted, %d dropped", n, counted, l.count("dropped"))
	}
}

func TestOnDemandNames(t *testing.T) {
	dir, caFile := hangingCA(t)
	ask := newAsker(t)
	var l logLines
	a := testACME(t, &config.ACME{Directory: dir, CAFile: caFile, OnDemand: &config.OnDemand{Ask: ask.srv.URL}}, &l)
	a.start()
	for _, name := range []string{"localhost", "127.0.0.1", "a..fit.test", "*.fit.test", "-a.fit.test", "a_b.fit.test",
		strings.Repeat("a", 64) + ".test"} {
		if _, err := a.OnDemand(name); !errors.Is(err, errOnDemandBadName) {
			t.Errorf("%q: %v", name, err)
		}
	}
	ask.mu.Lock()
	n := len(ask.asked)
	ask.mu.Unlock()
	if n != 0 {
		t.Errorf("bad names were asked about: %v", ask.asked)
	}

	// A name the files have a certificate for is served from them, and a
	// client that names none gets the files' first, without asking.
	files := keyCert(t, false, "files.fit.test")
	certs := NewCerts(NewStatic(files), a, nil, true)
	for _, name := range []string{"files.fit.test", ""} {
		if c, err := certs.GetCertificate(hello(name)); err != nil || c != files {
			t.Errorf("%q: %v, %v", name, c, err)
		}
	}
	if n := ask.count("files.fit.test"); n != 0 {
		t.Error("a name the files have was asked about")
	}

	// Without acme.on_demand, nothing is obtained.
	b := testACME(t, &config.ACME{Directory: dir, CAFile: caFile}, &l)
	if _, err := NewCerts(nil, b, nil, true).GetCertificate(hello("x.fit.test")); !errors.Is(err, errOnDemandOff) {
		t.Errorf("without on_demand: %v", err)
	}
}

// TestOnDemandIdle takes two names on from certificates in the storage, with
// a CA that is not there, and has a handshake ask for one of them again
// before idle_timeout: the other is dropped once it is idle that long, its
// certificate no longer served or listed, and no longer counted against
// max_names; the next handshake for it takes it on again from the storage,
// asking the ask URL again.
func TestOnDemandIdle(t *testing.T) {
	ask := newAsker(t, "idle.fit.test", "busy.fit.test", "third.fit.test")
	var l logLines
	a := testACME(t, &config.ACME{Directory: "https://127.0.0.1:1/dir", OnDemand: &config.OnDemand{Ask: ask.srv.URL,
		MaxNames: 2, IdleTimeout: config.Duration(time.Hour), Timeout: config.Duration(5 * time.Second)}}, &l)
	for _, name := range []string{"idle.fit.test", "busy.fit.test"} {
		storeIssued(t, a, name)
	}
	var now atomic.Pointer[time.Time]
	start := time.Now()
	now.Store(&start)
	a.now = func() time.Time { return *now.Load() }
	at := func(d time.Duration) {
		t := start.Add(d)
		now.Store(&t)
		a.poke()
	}
	a.start()
	certs := NewCerts(nil, a, nil, true)
	for _, name := range []string{"idle.fit.test", "busy.fit.test"} {
		if _, err := certs.GetCertificate(hello(name)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := certs.GetCertificate(hello("third.fit.test")); !errors.Is(err, ErrOnDemandTooMany) {
		t.Fatalf("past max_names: %v", err)
	}
	counted := func() int {
		a.onDemand.mu.Lock()
		defer a.onDemand.mu.Unlock()
		return a.onDemand.names
	}

	at(40 * time.Minute)
	if _, err := certs.GetCertificate(hello("busy.fit.test")); err != nil {
		t.Fatal(err)
	}
	at(59 * time.Minute)
	if l.count("dropped") != 0 || a.Certificate("idle.fit.test") == nil {
		t.Fatal("a name was dropped before its idle_timeout")
	}
	at(61 * time.Minute)
	eventually(t, 5*time.Second, "idle.fit.test dropped", func() bool { return l.count("idle.fit.test: on demand: no handshake") == 1 })
	if a.Certificate("idle.fit.test") != nil || a.Certificate("busy.fit.test") == nil {
		t.Error("the idle name is still served, or the busy one is not")
	}
	if got := certs.Certificates(); len(got) != 1 || got[0].Leaf.VerifyHostname("busy.fit.test") != nil {
		t.Errorf("Certificates lists %d, want busy.fit.test's alone", len(got))
	}
	if n := counted(); n != 1 {
		t.Errorf("%d names counted, want 1", n)
	}
	if err := a.onDemand.room(a.now(), false); err != nil {
		t.Errorf("no room after a name was dropped: %v", err)
	}

	// Asked for again, it is taken on from the storage, with the ask URL's
	// yes.
	if c, err := certs.GetCertificate(hello("idle.fit.test")); err != nil || c.Leaf.VerifyHostname("idle.fit.test") != nil {
		t.Fatalf("taken on again: %v", err)
	}
	if n := ask.count("idle.fit.test"); n != 2 {
		t.Errorf("asked %d times, want 2", n)
	}
	// busy.fit.test was last asked for at 40 minutes.
	at(101 * time.Minute)
	eventually(t, 5*time.Second, "busy.fit.test dropped", func() bool { return l.count("busy.fit.test: on demand: no handshake") == 1 })
	if n := counted(); n != 1 {
		t.Errorf("%d names counted, want 1", n)
	}
}

// TestOnDemandIdleTimer checks that the scheduler wakes for an idle name by
// itself, on the real clock.
func TestOnDemandIdleTimer(t *testing.T) {
	ask := newAsker(t, "idle.fit.test")
	var l logLines
	a := testACME(t, &config.ACME{Directory: "https://127.0.0.1:1/dir", OnDemand: &config.OnDemand{Ask: ask.srv.URL,
		IdleTimeout: config.Duration(100 * time.Millisecond)}}, &l)
	storeIssued(t, a, "idle.fit.test")
	a.start()
	if _, err := a.OnDemand("idle.fit.test"); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "idle.fit.test dropped", func() bool { return a.Certificate("idle.fit.test") == nil })
}

// TestOnDemandAskBounds has handshakes for random names, as an attacker
// sends them, ask an ask URL that refuses them all: at most maxAsks are
// asked at once, the one past them fails at once without asking, and no
// more than askBurst are asked at once, then one each askEvery, whatever the
// answers were.
func TestOnDemandAskBounds(t *testing.T) {
	dir, caFile := hangingCA(t)
	release := make(chan struct{})
	var inFlight, asked atomic.Int32
	ask := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		inFlight.Add(1)
		defer inFlight.Add(-1)
		select {
		case <-release:
		case <-r.Context().Done():
		}
		http.Error(w, "no", http.StatusForbidden)
	}))
	defer ask.Close()
	var l logLines
	a := testACME(t, &config.ACME{Directory: dir, CAFile: caFile, OnDemand: &config.OnDemand{Ask: ask.URL}}, &l)
	if tr := a.onDemand.http.Transport.(*http.Transport); tr.MaxConnsPerHost != maxAsks {
		t.Errorf("the ask URL's connections bounded at %d", tr.MaxConnsPerHost)
	}
	a.start()
	certs := NewCerts(nil, a, nil, true)
	errs := make(chan error, maxAsks)
	for i := range maxAsks {
		go func() {
			_, err := certs.GetCertificate(hello(fmt.Sprintf("held%d.fit.test", i)))
			errs <- err
		}()
	}
	eventually(t, 5*time.Second, "every ask under way", func() bool { return inFlight.Load() == maxAsks })
	begin := time.Now()
	if _, err := certs.GetCertificate(hello("past.fit.test")); !errors.Is(err, ErrOnDemandBusy) {
		t.Errorf("past maxAsks: %v", err)
	}
	if d := time.Since(begin); d > time.Second {
		t.Errorf("a handshake past maxAsks waited %v", d)
	}
	close(release)
	for range maxAsks {
		if err := <-errs; !errors.Is(err, ErrOnDemandRefused) {
			t.Errorf("a held ask: %v", err)
		}
	}
	if n := asked.Load(); n != maxAsks {
		t.Errorf("%d asks, want %d", n, maxAsks)
	}

	// The rate, with the clock stopped.
	b := testACME(t, &config.ACME{Directory: dir, CAFile: caFile, OnDemand: &config.OnDemand{Ask: ask.URL}}, &l)
	var now atomic.Pointer[time.Time]
	start := time.Now()
	now.Store(&start)
	b.now = func() time.Time { return *now.Load() }
	b.start()
	certs = NewCerts(nil, b, nil, true)
	asked.Store(0)
	busy := 0
	for i := range 200 {
		_, err := certs.GetCertificate(hello(fmt.Sprintf("r%d.fit.test", i)))
		if errors.Is(err, ErrOnDemandBusy) {
			busy++
		} else if !errors.Is(err, ErrOnDemandRefused) {
			t.Fatalf("a random name: %v", err)
		}
	}
	if n := asked.Load(); n != askBurst || busy != 200-askBurst {
		t.Errorf("%d asked and %d busy, want %d asked", n, busy, askBurst)
	}
	later := start.Add(askEvery)
	now.Store(&later)
	if _, err := certs.GetCertificate(hello("again.fit.test")); !errors.Is(err, ErrOnDemandRefused) {
		t.Errorf("an ask once askEvery passed: %v", err)
	}
	if _, err := certs.GetCertificate(hello("again2.fit.test")); !errors.Is(err, ErrOnDemandBusy) {
		t.Errorf("a second ask within askEvery: %v", err)
	}
}
