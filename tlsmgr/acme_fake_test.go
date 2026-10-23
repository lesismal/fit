package tlsmgr

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/certtest"
)

// faultyStorage is a Storage whose calls fail as fault says: fault is given
// the call (load, store, delete, lock, unlock) and its key, and the call
// fails with what it returns, or goes on when it returns nil.
type faultyStorage struct {
	Storage
	shared bool
	fault  func(op, key string) error
}

func (s *faultyStorage) Shared() bool { return s.shared }

func (s *faultyStorage) check(op, key string) error {
	if s.fault == nil {
		return nil
	}
	return s.fault(op, key)
}

func (s *faultyStorage) Load(ctx context.Context, key string) ([]byte, error) {
	if err := s.check("load", key); err != nil {
		return nil, err
	}
	return s.Storage.Load(ctx, key)
}

func (s *faultyStorage) Store(ctx context.Context, key string, value []byte) error {
	if err := s.check("store", key); err != nil {
		return err
	}
	return s.Storage.Store(ctx, key, value)
}

func (s *faultyStorage) Delete(ctx context.Context, key string) error {
	if err := s.check("delete", key); err != nil {
		return err
	}
	return s.Storage.Delete(ctx, key)
}

func (s *faultyStorage) Lock(ctx context.Context, name string) error {
	if err := s.check("lock", name); err != nil {
		return err
	}
	return s.Storage.Lock(ctx, name)
}

func (s *faultyStorage) Unlock(ctx context.Context, name string) error {
	if err := s.check("unlock", name); err != nil {
		_ = s.Storage.Unlock(ctx, name)
		return err
	}
	return s.Storage.Unlock(ctx, name)
}

var errFault = errors.New("injected fault")

// fakeACME returns an unstarted ACME of ca, on st, or on a directory of t's
// when st is nil.
func fakeACME(t *testing.T, ca *fakeCA, c *config.ACME, st Storage, l *logs) *ACME {
	t.Helper()
	c.Directory, c.CAFile = ca.directory(), ca.caFile
	if st == nil {
		st = &FileStorage{Root: t.TempDir()}
	}
	a, err := newACME(c, st, l.logf)
	if err != nil {
		t.Fatal(err)
	}
	a.retryBase, a.retryMax = 20*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { a.Close() })
	return a
}

// TestFakeCA obtains a certificate from the fake CA through HTTP-01, as
// TestPebble does from Pebble, with the scheduler and a worker.
func TestFakeCA(t *testing.T) {
	ca := newFakeCA(t)
	var l logs
	a := fakeACME(t, ca, &config.ACME{Email: "ops@fit.test"}, nil, &l)
	ca.set(func(f *fakeCA) {
		f.check = func(typ, token, _ string) error {
			if _, ok := a.HTTPChallenge(token); !ok {
				return errors.New("no answer to " + token)
			}
			return nil
		}
	})
	a.start()
	a.Manage([]string{"www.fit.test"})
	eventually(t, 10*time.Second, "a certificate from the fake CA", func() bool { return a.Certificate("www.fit.test") != nil })
	c := a.Certificate("www.fit.test")
	if _, err := c.Leaf.Verify(x509.VerifyOptions{DNSName: "www.fit.test", Roots: ca.ca.Pool()}); err != nil {
		t.Error(err)
	}
	if got := ca.acceptedTypes(); len(got) != 1 || got[0] != config.ChallengeHTTP01 {
		t.Errorf("challenges accepted: %v", got)
	}
	if _, ok := a.HTTPChallenge("tok-0"); ok {
		t.Error("the challenge's answer is kept once it is over")
	}
	ctx := context.Background()
	if _, err := a.storage.Load(ctx, a.certKey("www.fit.test")); err != nil {
		t.Errorf("the certificate was not stored: %v", err)
	}
	if n := l.count("obtained a certificate"); n != 1 {
		t.Errorf("%d certificates obtained", n)
	}
}

// TestFakeCAChallenges answers each type of challenge, on a storage of its
// own and on a shared one, which holds each answer while its challenge is
// under way.
func TestFakeCAChallenges(t *testing.T) {
	for _, shared := range []bool{false, true} {
		for _, typ := range []string{config.ChallengeHTTP01, config.ChallengeTLSALPN01} {
			t.Run(typ, func(t *testing.T) {
				ca := newFakeCA(t)
				var l logs
				st := &faultyStorage{Storage: &FileStorage{Root: t.TempDir()}, shared: shared}
				a := fakeACME(t, ca, &config.ACME{Challenges: []string{typ}}, st, &l)
				ctx := context.Background()
				ca.set(func(f *fakeCA) {
					f.check = func(typ, token, name string) error {
						key := a.httpChallengeKey(token)
						if typ == config.ChallengeTLSALPN01 {
							if a.ALPNChallenge(name) == nil {
								return errors.New("no certificate")
							}
							key = a.alpnChallengeKey(name)
						} else if _, ok := a.HTTPChallenge(token); !ok {
							return errors.New("no answer")
						}
						if _, err := st.Storage.Load(ctx, key); (err == nil) != shared {
							return errors.New("shared, or not shared, by mistake: " + key)
						}
						return nil
					}
				})
				cert, _, err := a.obtain(ctx, "c.fit.test", 0)
				if err != nil {
					t.Fatal(err)
				}
				if cert.Leaf.VerifyHostname("c.fit.test") != nil {
					t.Error("a certificate for another name")
				}
				if got := ca.acceptedTypes(); len(got) != 1 || got[0] != typ {
					t.Errorf("challenges accepted: %v", got)
				}
				if a.ALPNChallenge("c.fit.test") != nil {
					t.Error("the challenge's certificate is kept once it is over")
				}
				if keys, _ := st.Storage.List(ctx, "challenges"); len(keys) != 0 {
					t.Errorf("challenges left in the storage: %v", keys)
				}
			})
		}
	}
}

// TestFakeCASharedFaults has the shared storage refuse the challenges'
// answers: the challenges are still answered from memory, and the failures
// reported.
func TestFakeCASharedFaults(t *testing.T) {
	ca := newFakeCA(t)
	var l logs
	st := &faultyStorage{Storage: &FileStorage{Root: t.TempDir()}, shared: true, fault: func(op, key string) error {
		if strings.HasPrefix(key, "challenges/") {
			return errFault
		}
		return nil
	}}
	a := fakeACME(t, ca, &config.ACME{}, st, &l)
	if _, _, err := a.obtain(context.Background(), "s.fit.test", 0); err != nil {
		t.Fatal(err)
	}
	if l.count("keeping the challenge's answer") != 1 || l.count("removing the challenge's answer") != 1 {
		t.Errorf("the storage's failures not reported: %q", l.lines)
	}
}

func TestFakeCAChallengeChoice(t *testing.T) {
	ctx := context.Background()
	// The CA offers only the second type: it is taken.
	ca := newFakeCA(t)
	ca.set(func(f *fakeCA) { f.offer = []string{config.ChallengeTLSALPN01} })
	var l logs
	a := fakeACME(t, ca, &config.ACME{}, nil, &l)
	if _, _, err := a.obtain(ctx, "a.fit.test", 0); err != nil {
		t.Fatal(err)
	}
	if got := ca.acceptedTypes(); len(got) != 1 || got[0] != config.ChallengeTLSALPN01 {
		t.Errorf("challenges accepted: %v", got)
	}

	// None that is answered.
	ca = newFakeCA(t)
	ca.set(func(f *fakeCA) { f.offer = []string{"dns-01"} })
	a = fakeACME(t, ca, &config.ACME{}, nil, &l)
	if _, _, err := a.obtain(ctx, "a.fit.test", 0); err == nil || !strings.Contains(err.Error(), "offers none") {
		t.Errorf("no challenge answered: %v", err)
	}

	// An authorization valid already needs no challenge.
	ca = newFakeCA(t)
	ca.set(func(f *fakeCA) { f.valid = true })
	a = fakeACME(t, ca, &config.ACME{}, nil, &l)
	if _, _, err := a.obtain(ctx, "a.fit.test", 0); err != nil {
		t.Fatal(err)
	}
	if got := ca.acceptedTypes(); len(got) != 0 {
		t.Errorf("challenges accepted for a valid authorization: %v", got)
	}
}

// TestFakeCAFailures has each step of obtaining a certificate fail.
func TestFakeCAFailures(t *testing.T) {
	for _, tt := range []struct {
		fail, issue, want string
	}{
		{fail: "account", want: "registering the account"},
		{fail: "order", want: "ordering"},
		{fail: "authz", want: "authorization"},
		{fail: "accept", want: "accepting the challenge"},
		{fail: "authz-invalid", want: "the challenge"},
		{fail: "order-invalid", want: "waiting for the order"},
		{fail: "finalize", want: "finalizing the order"},
		{fail: "cert", want: "finalizing the order"},
		{issue: "garbage", want: "the CA's certificate"},
		{issue: "wrong-name", want: "the CA's certificate"},
	} {
		t.Run(tt.fail+tt.issue, func(t *testing.T) {
			ca := newFakeCA(t)
			ca.set(func(f *fakeCA) { f.fail[tt.fail], f.issue = true, tt.issue })
			var l logs
			a := fakeACME(t, ca, &config.ACME{}, nil, &l)
			_, _, err := a.obtain(context.Background(), "f.fit.test", 0)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("%v, want %q", err, tt.want)
			}
		})
	}
}

// storedCert returns a certificate for name as the storage keeps it.
func storedCert(t *testing.T, name string) []byte {
	t.Helper()
	issuer, err := certtest.NewCA("fit test CA")
	if err != nil {
		t.Fatal(err)
	}
	c, err := issuer.Issue(t.TempDir(), "", name)
	if err != nil {
		t.Fatal(err)
	}
	certPEM, _ := os.ReadFile(c.CertFile)
	keyPEM, _ := os.ReadFile(c.KeyFile)
	return append(certPEM, keyPEM...)
}

func TestEnsureStorage(t *testing.T) {
	ctx := context.Background()
	ca := newFakeCA(t)

	// The lock cannot be taken.
	var l logs
	st := &faultyStorage{Storage: &FileStorage{Root: t.TempDir()}}
	a := fakeACME(t, ca, &config.ACME{}, st, &l)
	st.fault = func(op, key string) error {
		if op == "lock" {
			return errFault
		}
		return nil
	}
	if _, err := a.ensure(ctx, "e.fit.test", 0, nil, nil); err == nil || !strings.Contains(err.Error(), "lock") {
		t.Errorf("no lock: %v", err)
	}
	if _, err := a.account(ctx); err == nil || !strings.Contains(err.Error(), "lock") {
		t.Errorf("no lock for the account: %v", err)
	}

	// Another instance stores one while this one waits for the lock: it is
	// taken, and the CA is not asked.
	data := storedCert(t, "e.fit.test")
	st.fault = func(op, key string) error {
		if op == "lock" && strings.HasPrefix(key, "issue/") {
			if err := st.Storage.Store(ctx, a.certKey("e.fit.test"), data); err != nil {
				t.Error(err)
			}
		}
		return nil
	}
	if _, err := a.ensure(ctx, "e.fit.test", 0, nil, nil); err != nil {
		t.Fatal(err)
	}
	if ca.issued.Load() != 0 {
		t.Error("the CA was asked for a certificate another instance stored")
	}

	// The certificate cannot be stored, nor the lock given back: it is
	// served all the same, and both reported.
	st.fault = func(op, key string) error {
		if op == "store" && strings.HasPrefix(key, "certificates/") || op == "unlock" {
			return errFault
		}
		return nil
	}
	if _, err := a.ensure(ctx, "f.fit.test", 0, nil, nil); err != nil {
		t.Fatal(err)
	}
	if l.count("storing the certificate") != 1 || l.count("giving back the storage's lock") == 0 {
		t.Errorf("the storage's failures not reported: %q", l.lines)
	}

	// A stored certificate that cannot be used, and a storage that cannot
	// be read, are reported, and another obtained.
	st.fault = nil
	if err := st.Store(ctx, a.certKey("g.fit.test"), []byte("not PEM")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ensure(ctx, "g.fit.test", 0, nil, nil); err != nil {
		t.Fatal(err)
	}
	if l.count("g.fit.test: the stored certificate cannot be used") == 0 {
		t.Errorf("an unusable certificate not reported: %q", l.lines)
	}
	st.fault = func(op, key string) error {
		if op == "load" && strings.HasPrefix(key, "certificates/") {
			return errFault
		}
		return nil
	}
	if _, err := a.ensure(ctx, "h.fit.test", 0, nil, nil); err != nil {
		t.Fatal(err)
	}
	if l.count("h.fit.test: loading the certificate") == 0 {
		t.Errorf("a failed load not reported: %q", l.lines)
	}
}

func TestAccountKey(t *testing.T) {
	ctx := context.Background()
	x25519, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(x25519)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name   string
		stored []byte
		fault  func(op, key string) error
		want   string
	}{
		{name: "not PEM", stored: []byte("garbage"), want: "is not PEM"},
		{name: "not PKCS8", stored: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("x")}), want: "the account key in"},
		{name: "cannot sign", stored: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), want: "cannot sign"},
		{name: "load fails", fault: func(op, key string) error {
			if op == "load" {
				return errFault
			}
			return nil
		}, want: "loading the account key"},
		{name: "store fails", fault: func(op, key string) error {
			if op == "store" {
				return errFault
			}
			return nil
		}, want: "storing the account key"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ca := newFakeCA(t)
			var l logs
			st := &faultyStorage{Storage: &FileStorage{Root: t.TempDir()}}
			a := fakeACME(t, ca, &config.ACME{}, st, &l)
			if tt.stored != nil {
				if err := st.Store(ctx, a.accountKey(), tt.stored); err != nil {
					t.Fatal(err)
				}
			}
			st.fault = tt.fault
			if _, err := a.account(ctx); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("%v, want %q", err, tt.want)
			}
		})
	}
}

func TestNewACMEErrors(t *testing.T) {
	dir := t.TempDir()
	notPEM := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(notPEM, []byte("no certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []*config.ACME{
		{CAFile: filepath.Join(dir, "missing.pem")},
		{CAFile: notPEM},
		{SharedStorage: &config.SharedStorage{}},
	} {
		if a, err := NewACME(c, nil, nil); err == nil {
			a.Close()
			t.Errorf("%+v: no error", c)
		}
	}

	// A shared storage made from the config is closed with the ACME.
	a, err := NewACME(&config.ACME{SharedStorage: &config.SharedStorage{Redis: &config.RedisStorage{Address: "127.0.0.1:1"}}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !a.Shared() || a.closer == nil {
		t.Error("the shared storage is not the ACME's")
	}
	if err := a.Close(); err != nil {
		t.Error(err)
	}
	if err := a.Close(); err != nil {
		t.Error("closed twice:", err)
	}
}

func TestStorageRoot(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	a, err := newACME(nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if fst, ok := a.storage.(*FileStorage); !ok || fst.Root != filepath.Join(data, "fit") {
		t.Errorf("storage %#v, want %s", a.storage, filepath.Join(data, "fit"))
	}

	// With no home, the storage fails when it is first used.
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	t.Setenv("home", "")
	a, err = newACME(nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, ok := a.storage.(brokenStorage)
	if !ok {
		t.Skipf("a home directory without $HOME: %#v", a.storage)
	}
	ctx := context.Background()
	if _, err := b.Load(ctx, "k"); err != b.err {
		t.Error("Load", err)
	}
	if _, err := b.List(ctx, "k"); err != b.err {
		t.Error("List", err)
	}
	for name, err := range map[string]error{"Store": b.Store(ctx, "k", nil), "Delete": b.Delete(ctx, "k"),
		"Lock": b.Lock(ctx, "k"), "Unlock": b.Unlock(ctx, "k")} {
		if err != b.err {
			t.Error(name, err)
		}
	}
}

// TestManageOnDemand lists a name taken on demand, which then counts as the
// config's, and leaves out another, which stays.
func TestManageOnDemand(t *testing.T) {
	var l logs
	a := testACME(t, &config.ACME{Directory: "https://127.0.0.1:1/dir", OnDemand: &config.OnDemand{Ask: "http://127.0.0.1:1/"}}, &l)
	used := func() *atomic.Int64 { u := &atomic.Int64{}; u.Store(time.Now().UnixNano()); return u }
	a.names["listed.fit.test"] = &managed{name: "listed.fit.test", onDemand: true}
	a.names["kept.fit.test"] = &managed{name: "kept.fit.test", onDemand: true}
	a.onDemand.used.Store("listed.fit.test", used())
	a.onDemand.used.Store("kept.fit.test", used())
	a.onDemand.names = 2
	a.Manage([]string{"listed.fit.test"})
	if m := a.names["listed.fit.test"]; m == nil || m.onDemand {
		t.Error("a listed name is still on demand")
	}
	if m := a.names["kept.fit.test"]; m == nil || !m.onDemand {
		t.Error("a name on demand was dropped")
	}
	if a.onDemand.names != 1 {
		t.Errorf("%d names counted on demand", a.onDemand.names)
	}

	// A name on demand that is not in the used map is not dropped as idle.
	a.onDemand.used.Delete("kept.fit.test")
	a.mu.Lock()
	a.dropIdle(time.Now().Add(365 * 24 * time.Hour))
	a.mu.Unlock()
	if a.names["kept.fit.test"] == nil {
		t.Error("a name with no record of use was dropped")
	}
}

// TestACMEStaples has ACME keep the OCSP responses of the certificates it
// obtains, give them back for names it drops, and renew one revoked.
func TestACMEStaples(t *testing.T) {
	r := newResponder(t)
	ca := newFakeCA(t)
	// The responder answers for the CA's certificates.
	ca.set(func(f *fakeCA) { f.ocsp, f.ca = r.srv.URL, r.ca })
	var l logs
	a := fakeACME(t, ca, &config.ACME{}, nil, &l)
	o := testOCSP(t, &l)
	a.UseOCSP(o)
	a.start()
	a.Manage([]string{"a.fit.test", "b.fit.test"})
	eventually(t, 10*time.Second, "both certificates", func() bool {
		return a.Certificate("a.fit.test") != nil && a.Certificate("b.fit.test") != nil
	})
	sts := *a.staples.Load()
	if sts["a.fit.test"] == nil || sts["b.fit.test"] == nil {
		t.Fatalf("staples: %v", sts)
	}
	eventually(t, 10*time.Second, "a stapled response", func() bool { return len(a.Certificate("a.fit.test").OCSPStaple) != 0 })

	// Revoked: renewed at once.
	first := a.Certificate("a.fit.test").Leaf.SerialNumber
	a.renewRevoked("a.fit.test", first)
	a.renewRevoked("gone.fit.test", first)
	eventually(t, 10*time.Second, "the revoked certificate replaced", func() bool {
		c := a.Certificate("a.fit.test")
		return c != nil && c.Leaf.SerialNumber.Cmp(first) != 0
	})

	// Dropped: its staple is given back.
	a.Manage([]string{"a.fit.test"})
	if sts := *a.staples.Load(); sts["b.fit.test"] != nil || sts["a.fit.test"] == nil {
		t.Errorf("staples once b.fit.test is dropped: %v", sts)
	}

	// A certificate that names no responder has no staple.
	self := keyCert(t, false, "a.fit.test")
	a.mu.Lock()
	a.staple("a.fit.test", self)
	a.mu.Unlock()
	if (*a.staples.Load())["a.fit.test"] != nil {
		t.Error("a staple for a certificate without a responder")
	}
}

func TestHTTPChallengeAnswer(t *testing.T) {
	a := &ACME{}
	a.tokens.Store("tok", "tok.thumb")
	if answer, ok := a.HTTPChallenge("tok"); !ok || answer != "tok.thumb" {
		t.Errorf("%q %v", answer, ok)
	}
}

func TestSharedLookupFaults(t *testing.T) {
	var l logs
	st := &faultyStorage{Storage: &FileStorage{Root: t.TempDir()}, shared: true, fault: func(op, key string) error {
		if op == "load" {
			return errFault
		}
		return nil
	}}
	a := sharedACME(t, &config.ACME{Directory: "https://127.0.0.1:1/dir"}, st, &l)
	if _, ok := lookup(t, a, "tok"); ok {
		t.Error("a failed lookup answered")
	}
	if a.sharedALPNChallenge("a.fit.test") != nil {
		t.Error("a failed load answered")
	}
	if l.count("looking up a challenge") != 2 {
		t.Errorf("the failures not reported: %q", l.lines)
	}
	// A name that makes another key, or none, is not looked up.
	for _, bad := range []string{"", "A.fit.test", "a/b"} {
		if a.sharedALPNChallenge(bad) != nil {
			t.Errorf("%q answered", bad)
		}
	}
	// A challenge's certificate that does not parse.
	st.fault = nil
	if err := st.Store(context.Background(), a.alpnChallengeKey("b.fit.test"), []byte("garbage")); err != nil {
		t.Fatal(err)
	}
	if a.sharedALPNChallenge("b.fit.test") != nil || l.count("the shared challenge's certificate") != 1 {
		t.Errorf("a challenge's certificate that does not parse: %q", l.lines)
	}
	if _, err := encodeCert(&tls.Certificate{Certificate: [][]byte{{1}}}); err == nil {
		t.Error("a certificate without a key encoded")
	}
}
