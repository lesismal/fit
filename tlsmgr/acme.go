package tlsmgr

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"io/fs"
	"math/big"
	mathrand "math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/acme"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/tlsmgr/sharedstore"
)

// ACMEChallengePrefix is where a CA asks for an HTTP-01 challenge's answer:
// the path, followed by the challenge's token.
const ACMEChallengePrefix = "/.well-known/acme-challenge/"

// The bounds of ACME's backoff: a failure is tried again after retryBase,
// then twice as long each time, up to retryMax.
const (
	defaultRetryBase = time.Minute
	defaultRetryMax  = time.Hour
)

// ACME obtains certificates for the names it manages from an ACME CA,
// through the HTTP-01 or the TLS-ALPN-01 challenge, keeps them in a Storage, and renews them
// once a third of their lifetime is left. It is the control plane's: its
// work runs on goroutines of its own, a scheduler and MaxConcurrent workers,
// which may block on the CA and the storage, each with a timeout. A
// handshake asks it for a certificate (Certificate) or a TLS-ALPN-01
// challenge's (ALPNChallenge), and a request for an HTTP-01 challenge's
// answer (HTTPChallenge), and all of them read memory only.
//
// A certificate in the storage is used as it is until it is due for
// renewal, so that a restart, or another instance sharing the storage, does
// not obtain it again. Instances sharing a storage take a lock in it before
// they obtain a certificate, and look again once they hold it. With a
// shared storage, one that instances on other hosts share (acme's
// shared_storage), each challenge's answer is kept there too while it is
// under way, so that the instance the CA reaches answers it: HTTP-01 off
// the worker (LookupHTTPChallenge), TLS-ALPN-01 on the handshake pool.
type ACME struct {
	storage   Storage
	shared    bool          // storage is shared between hosts
	closer    io.Closer     // the storage, when ACME made it from the config
	lookups   chan struct{} // a slot for each lookup of a shared challenge under way
	directory string
	email     string
	caKey     string // the CA's part of the storage's keys
	http      *http.Client
	timeout   time.Duration
	workers   int
	logf      func(format string, args ...any)
	// onDemand, when the config has acme.on_demand, takes on names during
	// handshakes; see OnDemand.
	onDemand *onDemand
	// challenges are the challenge types answered, in order; an attempt
	// after n failures in a row starts with the nth, so that a type the CA
	// cannot check does not fail every attempt.
	challenges []string

	// now, renewAt, retryBase, retryMax, ariMin and ariMax are what tests
	// change; ariMin and ariMax bound the wait before asking the CA about a
	// certificate again.
	now                 func() time.Time
	renewAt             func(leaf *x509.Certificate) time.Time
	retryBase, retryMax time.Duration
	ariMin, ariMax      time.Duration

	// certs holds the certificate of each name that has one, replaced
	// whole on every change, so that a handshake reads it without a lock.
	certs atomic.Pointer[map[string]*tls.Certificate]
	// ocsp, once UseOCSP sets it, keeps the certificates' OCSP responses,
	// and staples holds, the same way as certs, each one's Staple.
	ocsp    atomic.Pointer[OCSP]
	staples atomic.Pointer[map[string]*Staple]
	// tokens holds the key authorization of each HTTP-01 challenge under
	// way, by its token, and alpnCerts the certificate of each TLS-ALPN-01
	// challenge under way, by its name.
	tokens    sync.Map
	alpnCerts sync.Map

	mu    sync.Mutex
	names map[string]*managed
	// ariURL is the CA's ARI endpoint, once ariKnown, from its directory.
	ariURL   string
	ariKnown bool
	client   *acme.Client // once the account is registered
	closed   bool

	jobs   chan *managed
	wake   chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
	done   sync.WaitGroup
}

// managed is a name ACME keeps a certificate for, and when it next works on
// it.
type managed struct {
	name     string
	due      time.Time
	failures int
	running  bool
	// revoked is the serial number of a certificate the OCSP responder
	// said is revoked, which the next run does not take from the storage.
	revoked *big.Int
	// onDemand says a handshake took the name on, not the config; Manage
	// leaves it, and it is dropped if its first certificate fails.
	onDemand bool
	// ari is what the CA's renewal information says of the certificate,
	// or nil before ACME has asked.
	ari *ariState
}

// NewACME returns an ACME that obtains certificates as c says, or with the
// defaults when c is nil, and keeps them in storage, or in c's storage
// directory when storage is nil. logf reports what it does; it may be nil.
// It manages no names until Manage is called; Close stops it.
func NewACME(c *config.ACME, storage Storage, logf func(format string, args ...any)) (*ACME, error) {
	a, err := newACME(c, storage, logf)
	if err != nil {
		return nil, err
	}
	a.start()
	return a, nil
}

// newACME is NewACME without starting, for tests to change what they change
// first.
func newACME(c *config.ACME, storage Storage, logf func(format string, args ...any)) (*ACME, error) {
	var closer io.Closer
	if storage == nil && c != nil && c.SharedStorage != nil {
		st, err := sharedstore.New(c.SharedStorage, logf)
		if err != nil {
			return nil, c.At.Field("shared_storage").Errorf("%v", err)
		}
		storage, closer = st, st
	}
	if storage == nil {
		// A process with no home to keep certificates in fails when it
		// first needs one, not when it starts.
		if root, err := storageRoot(c); err != nil {
			storage = brokenStorage{err}
		} else {
			storage = &FileStorage{Root: root}
		}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if c != nil && c.CAFile != "" {
		data, err := os.ReadFile(c.CAFile)
		if err != nil {
			return nil, c.At.Field("ca_file").Errorf("%v", err)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(data) {
			return nil, c.At.Field("ca_file").Errorf("%s holds no PEM certificate", c.CAFile)
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	a := &ACME{
		storage:   storage,
		shared:    isShared(storage),
		closer:    closer,
		lookups:   make(chan struct{}, maxSharedLookups),
		directory: c.DirectoryURL(),
		http:      &http.Client{Transport: transport, Timeout: c.HTTPTimeout()},
		timeout:   c.OrderTimeout(),
		workers:   c.Concurrency(),
		logf:      logf,
		now:       time.Now,
		retryBase: defaultRetryBase,
		retryMax:  defaultRetryMax,
		ariMin:    ariCheckMin,
		ariMax:    ariCheckMax,
		names:     map[string]*managed{},
		wake:      make(chan struct{}, 1),
	}
	a.challenges = c.ChallengeTypes()
	if c != nil && c.OnDemand != nil {
		a.onDemand = newOnDemand(c.OnDemand)
	}
	if c != nil {
		a.email = c.Email
	}
	a.renewAt = a.defaultRenewAt
	a.caKey = caKey(a.directory)
	a.jobs = make(chan *managed, a.workers)
	a.certs.Store(&map[string]*tls.Certificate{})
	a.ctx, a.cancel = context.WithCancel(context.Background())
	return a, nil
}

// start starts a's scheduler and workers.
func (a *ACME) start() {
	a.done.Add(1 + a.workers)
	go a.schedule()
	for range a.workers {
		go a.work()
	}
}

// storageRoot is where c keeps what it obtains: its storage, or
// $XDG_DATA_HOME/fit, or ~/.local/share/fit.
func storageRoot(c *config.ACME) (string, error) {
	if c != nil && c.Storage != "" {
		return c.Storage, nil
	}
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, "fit"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("acme: no storage directory: set acme.storage (%v)", err)
	}
	return filepath.Join(home, ".local", "share", "fit"), nil
}

// brokenStorage is a Storage that fails every call with err.
type brokenStorage struct{ err error }

func (s brokenStorage) Load(context.Context, string) ([]byte, error)   { return nil, s.err }
func (s brokenStorage) Store(context.Context, string, []byte) error    { return s.err }
func (s brokenStorage) Delete(context.Context, string) error           { return s.err }
func (s brokenStorage) List(context.Context, string) ([]string, error) { return nil, s.err }
func (s brokenStorage) Lock(context.Context, string) error             { return s.err }
func (s brokenStorage) Unlock(context.Context, string) error           { return s.err }

// caKey turns a directory's URL into one part of a storage key: its host and
// path, with every byte but letters, digits, '.' and '-' made '-'.
func caKey(directory string) string {
	u, err := url.Parse(directory)
	s := directory
	if err == nil {
		s = u.Host + u.Path
	}
	b := []byte(strings.Trim(s, "/"))
	for i, c := range b {
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '.' || c == '-') {
			b[i] = '-'
		}
	}
	if len(b) == 0 || b[0] == '.' {
		return "ca-" + string(b)
	}
	return string(b)
}

// Manage sets the names a keeps certificates for. A name new to it is worked
// on at once; one no longer among them is dropped, and its certificate no
// longer served, though it stays in the storage.
func (a *ACME) Manage(names []string) {
	a.mu.Lock()
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[normalizeName(n)] = true
	}
	changed := false
	for n, m := range a.names {
		switch {
		case want[n] && m.onDemand:
			// Listed now: the config's, and no longer counted on demand.
			m.onDemand = false
			a.onDemand.drop(n)
		case m.onDemand:
			want[n] = true
		case !want[n]:
			delete(a.names, n)
			changed = true
		}
	}
	for n := range want {
		if a.names[n] == nil {
			a.names[n] = &managed{name: n, due: a.now()}
		}
	}
	if changed {
		a.pruneCerts()
	}
	a.mu.Unlock()
	a.poke()
}

// pruneCerts stops serving the certificates of names a no longer manages,
// and gives back their OCSP responses. a.mu is held.
func (a *ACME) pruneCerts() {
	// The staples first, as Certificate looks there first.
	if sts := a.staples.Load(); sts != nil {
		keptStaples := make(map[string]*Staple, len(*sts))
		for n, st := range *sts {
			if a.names[n] != nil {
				keptStaples[n] = st
			} else {
				a.ocsp.Load().Release(st)
			}
		}
		a.staples.Store(&keptStaples)
	}
	certs := *a.certs.Load()
	kept := make(map[string]*tls.Certificate, len(certs))
	for n, c := range certs {
		if a.names[n] != nil {
			kept[n] = c
		}
	}
	a.certs.Store(&kept)
}

// dropIdle drops the names taken on demand that no handshake has asked for
// in on_demand's idle_timeout, as of now, and returns when the next of the
// others will be, or zero if none will. A name being worked on waits for its
// run to end. A name dropped no longer counts against max_names, and its
// certificate is no longer served but stays in the storage, for the next
// handshake that asks for it to take it on again. a.mu is held.
func (a *ACME) dropIdle(now time.Time) time.Time {
	var next time.Time
	dropped := false
	for n, m := range a.names {
		if !m.onDemand || m.running {
			continue
		}
		until, ok := a.onDemand.idleUntil(n)
		if !ok {
			continue
		}
		if until.After(now) {
			if next.IsZero() || until.Before(next) {
				next = until
			}
			continue
		}
		delete(a.names, n)
		a.onDemand.drop(n)
		dropped = true
		a.logf("acme: %s: on demand: no handshake for %s; dropped", n, a.onDemand.idle)
	}
	if dropped {
		a.pruneCerts()
	}
	return next
}

// Certificate returns the certificate a has for name, with its OCSP
// response stapled if it has one, or nil.
func (a *ACME) Certificate(name string) *tls.Certificate {
	if sts := a.staples.Load(); sts != nil {
		if st := (*sts)[name]; st != nil {
			return st.Certificate()
		}
	}
	return (*a.certs.Load())[name]
}

// UseOCSP has o keep the OCSP responses of the certificates a obtains from
// now on, and has a renew a certificate o hears is revoked. It is called
// once, before Manage.
func (a *ACME) UseOCSP(o *OCSP) {
	a.staples.Store(&map[string]*Staple{})
	a.ocsp.Store(o)
}

// renewRevoked has name's certificate, with serial number serial, replaced
// at once, by one other than it.
func (a *ACME) renewRevoked(name string, serial *big.Int) {
	a.mu.Lock()
	if m := a.names[name]; m != nil {
		m.revoked, m.due, m.failures = serial, a.now(), 0
	}
	a.mu.Unlock()
	a.poke()
}

// HTTPChallenge returns the answer to the HTTP-01 challenge of token, if one
// is under way.
func (a *ACME) HTTPChallenge(token string) (string, bool) {
	v, ok := a.tokens.Load(token)
	if !ok {
		return "", false
	}
	return v.(string), true
}

// ALPNChallenge returns the certificate that answers the TLS-ALPN-01
// challenge of name, if one is under way.
func (a *ACME) ALPNChallenge(name string) *tls.Certificate {
	v, ok := a.alpnCerts.Load(normalizeName(name))
	if !ok {
		return nil
	}
	return v.(*tls.Certificate)
}

// Close stops a: the work under way is cancelled, and Close returns once
// its goroutines have.
func (a *ACME) Close() error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	a.mu.Unlock()
	a.cancel()
	a.done.Wait()
	if a.closer != nil {
		return a.closer.Close()
	}
	return nil
}

func (a *ACME) poke() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

// schedule hands each name to the workers when it is due, as long as one is
// free, and sleeps until the next is.
func (a *ACME) schedule() {
	defer a.done.Done()
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		a.mu.Lock()
		now := a.now()
		var next time.Time
		if a.onDemand != nil {
			next = a.dropIdle(now)
		}
		for _, m := range a.names {
			if m.running {
				continue
			}
			if !m.due.After(now) {
				select {
				case a.jobs <- m:
					m.running = true
					continue
				default:
					// Every worker is busy; one that finishes pokes.
				}
			}
			if next.IsZero() || m.due.Before(next) {
				next = m.due
			}
		}
		a.mu.Unlock()
		wait := time.Hour
		if !next.IsZero() {
			wait = max(next.Sub(now), 0)
			if wait == 0 {
				// Due, but the workers are full: wait for one to poke.
				wait = time.Hour
			}
		}
		timer.Reset(wait)
		select {
		case <-a.ctx.Done():
			return
		case <-a.wake:
		case <-timer.C:
		}
	}
}

// work obtains or renews the certificates of the names handed to it.
func (a *ACME) work() {
	defer a.done.Done()
	for {
		select {
		case <-a.ctx.Done():
			return
		case m := <-a.jobs:
			a.run(m)
		}
	}
}

// run works on m's name, and sets when it is next due: when its certificate
// is due for renewal, or, after a failure, later and later.
func (a *ACME) run(m *managed) {
	ctx, cancel := context.WithTimeout(a.ctx, a.timeout)
	a.mu.Lock()
	revoked, ari := m.revoked, m.ari
	a.mu.Unlock()
	cert, err := a.ensure(ctx, m.name, m.failures, revoked, ari)
	if err == nil {
		a.checkARI(ctx, m, cert.Leaf)
	}
	cancel()
	a.mu.Lock()
	m.running = false
	if a.names[m.name] == m {
		if err == nil {
			m.failures, m.revoked = 0, nil
			m.due = a.renewAt(cert.Leaf)
			if s := m.ari; s != nil && s.serial.Cmp(cert.Leaf.SerialNumber) == 0 {
				// Earlier when the CA's window says so, and when it is time
				// to ask again.
				if !s.renewAt.IsZero() && s.renewAt.Before(m.due) {
					m.due = s.renewAt
				}
				if s.checkAt.Before(m.due) {
					m.due = s.checkAt
				}
			}
			certs := *a.certs.Load()
			if certs[m.name] != cert {
				// The staple first: Certificate looks there before certs,
				// so a certificate is never seen without its staple, nor
				// the last one's staple served in its place.
				a.staple(m.name, cert)
				next := make(map[string]*tls.Certificate, len(certs)+1)
				for n, c := range certs {
					next[n] = c
				}
				next[m.name] = cert
				a.certs.Store(&next)
			}
		} else if m.onDemand && (*a.certs.Load())[m.name] == nil {
			// A name taken on during a handshake whose first certificate
			// fails is dropped, rather than tried again for ever; the
			// next handshake for it may take it on again.
			delete(a.names, m.name)
			a.onDemand.drop(m.name)
			a.logf("acme: %s: %v; dropped", m.name, err)
		} else if a.ctx.Err() == nil {
			m.failures++
			wait := a.backoff(m.failures)
			m.due = a.now().Add(wait)
			a.logf("acme: %s: %v; trying again in %s", m.name, err, wait.Round(time.Second))
		}
	}
	// A handshake waiting for the name hears of it, whether or not the name
	// is still on demand.
	a.onDemand.finish(m.name, a.Certificate(m.name), err)
	a.mu.Unlock()
	a.poke()
}

// staple has a's OCSP, if it has one, keep name's new certificate's
// response, in place of its last one's. a.mu is held.
func (a *ACME) staple(name string, cert *tls.Certificate) {
	o := a.ocsp.Load()
	if o == nil {
		return
	}
	sts := *a.staples.Load()
	next := make(map[string]*Staple, len(sts)+1)
	for n, st := range sts {
		next[n] = st
	}
	serial := cert.Leaf.SerialNumber
	if st := o.Add(cert, func() { a.renewRevoked(name, serial) }); st != nil {
		next[name] = st
	} else {
		delete(next, name)
	}
	a.staples.Store(&next)
	o.Release(sts[name])
}

// backoff is how long to wait after the nth failure in a row: retryBase
// doubled n-1 times, up to retryMax, less up to a tenth at random.
func (a *ACME) backoff(n int) time.Duration {
	d := a.retryBase
	for i := 1; i < n && d < a.retryMax; i++ {
		d *= 2
	}
	d = min(d, a.retryMax)
	return d - time.Duration(mathrand.Int64N(int64(d)/10+1))
}

// defaultRenewAt is when leaf is due for renewal: once a third of its
// lifetime is left, and up to a fiftieth of its lifetime before that, by its
// serial number, so that certificates obtained together are not all renewed
// together, and every instance sharing it agrees on when.
func (a *ACME) defaultRenewAt(leaf *x509.Certificate) time.Time {
	life := leaf.NotAfter.Sub(leaf.NotBefore)
	h := fnv.New32a()
	h.Write(leaf.SerialNumber.Bytes())
	jitter := time.Duration(float64(life/50) * float64(h.Sum32()%1000) / 1000)
	return leaf.NotAfter.Add(-life/3 - jitter)
}

// usable reports whether cert can be served, and is not yet due for
// renewal.
func (a *ACME) usable(cert *tls.Certificate) bool {
	return cert != nil && a.now().Before(a.renewAt(cert.Leaf))
}

// The storage's keys.
func (a *ACME) certKey(name string) string { return "certificates/" + a.caKey + "/" + name + ".pem" }
func (a *ACME) accountKey() string         { return "acme/" + a.caKey + "/account.pem" }

// ensure returns a certificate for name that is not due for renewal: the
// one in the storage, or a new one, which it stores. attempt counts the
// failures before it, which pick the challenge tried first; revoked, if not
// nil, is the serial number of a certificate not to take from the storage,
// and ari, if not nil, what the CA's renewal information says.
func (a *ACME) ensure(ctx context.Context, name string, attempt int, revoked *big.Int, ari *ariState) (*tls.Certificate, error) {
	usable := func(cert *tls.Certificate) bool {
		return a.usable(cert) && (revoked == nil || cert.Leaf.SerialNumber.Cmp(revoked) != 0) &&
			!ari.due(cert.Leaf.SerialNumber, a.now())
	}
	if cert := a.load(ctx, name); usable(cert) {
		return cert, nil
	}
	lock := "issue/" + a.caKey + "/" + name
	if err := a.storage.Lock(ctx, lock); err != nil {
		return nil, fmt.Errorf("taking the storage's lock: %w", err)
	}
	defer a.unlock(lock)
	// Another instance may have renewed it while this one waited.
	if cert := a.load(ctx, name); usable(cert) {
		return cert, nil
	}
	cert, data, err := a.obtain(ctx, name, attempt)
	if err != nil {
		return nil, err
	}
	if err := a.storage.Store(ctx, a.certKey(name), data); err != nil {
		// Served all the same; the next renewal stores it again.
		a.logf("acme: %s: storing the certificate: %v", name, err)
	}
	a.logf("acme: %s: obtained a certificate valid until %s", name, cert.Leaf.NotAfter.UTC().Format(time.RFC3339))
	return cert, nil
}

// unlock gives back a storage lock, with a time limit of its own, since the
// context the lock was taken under may be done.
func (a *ACME) unlock(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.storage.Unlock(ctx, name); err != nil {
		a.logf("acme: giving back the storage's lock %s: %v", name, err)
	}
}

// load returns name's certificate from the storage, or nil when there is
// none that can be served.
func (a *ACME) load(ctx context.Context, name string) *tls.Certificate {
	data, err := a.storage.Load(ctx, a.certKey(name))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			a.logf("acme: %s: loading the certificate: %v", name, err)
		}
		return nil
	}
	cert, err := tls.X509KeyPair(data, data)
	if err != nil || cert.Leaf == nil || cert.Leaf.VerifyHostname(name) != nil {
		a.logf("acme: %s: the stored certificate cannot be used; obtaining another", name)
		return nil
	}
	return &cert
}

// account returns a client of the CA with a registered account, registering
// it, or making its key, the first time.
func (a *ACME) account(ctx context.Context) (*acme.Client, error) {
	a.mu.Lock()
	client := a.client
	a.mu.Unlock()
	if client != nil {
		return client, nil
	}
	lock := "account/" + a.caKey
	if err := a.storage.Lock(ctx, lock); err != nil {
		return nil, fmt.Errorf("taking the storage's lock: %w", err)
	}
	defer a.unlock(lock)
	key, err := a.accountSigner(ctx)
	if err != nil {
		return nil, err
	}
	client = &acme.Client{Key: key, DirectoryURL: a.directory, HTTPClient: a.http, UserAgent: "fit"}
	acct := &acme.Account{}
	if a.email != "" {
		acct.Contact = []string{"mailto:" + a.email}
	}
	if _, err := client.Register(ctx, acct, acme.AcceptTOS); err != nil && !errors.Is(err, acme.ErrAccountAlreadyExists) {
		return nil, fmt.Errorf("registering the account: %w", err)
	}
	a.mu.Lock()
	if a.client == nil {
		a.client = client
	}
	client = a.client
	a.mu.Unlock()
	return client, nil
}

// accountSigner returns the account's key from the storage, making and
// storing one if there is none. The account's lock is held.
func (a *ACME) accountSigner(ctx context.Context) (crypto.Signer, error) {
	data, err := a.storage.Load(ctx, a.accountKey())
	if err == nil {
		block, _ := pem.Decode(data)
		if block == nil {
			return nil, fmt.Errorf("the account key in %s is not PEM", a.accountKey())
		}
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("the account key in %s: %w", a.accountKey(), err)
		}
		signer, ok := key.(crypto.Signer)
		if !ok {
			return nil, fmt.Errorf("the account key in %s cannot sign", a.accountKey())
		}
		return signer, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("loading the account key: %w", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	if err := a.storage.Store(ctx, a.accountKey(), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})); err != nil {
		return nil, fmt.Errorf("storing the account key: %w", err)
	}
	return key, nil
}

// obtain orders a certificate for name, answers its challenges, and returns
// it, with its chain and key as PEM to store.
func (a *ACME) obtain(ctx context.Context, name string, attempt int) (*tls.Certificate, []byte, error) {
	client, err := a.account(ctx)
	if err != nil {
		return nil, nil, err
	}
	order, err := client.AuthorizeOrder(ctx, acme.DomainIDs(name))
	if err != nil {
		return nil, nil, fmt.Errorf("ordering: %w", err)
	}
	for _, u := range order.AuthzURLs {
		if err := a.authorize(ctx, client, u, attempt); err != nil {
			return nil, nil, err
		}
	}
	uri := order.URI
	if order, err = client.WaitOrder(ctx, uri); err != nil {
		return nil, nil, fmt.Errorf("waiting for the order: %w", err)
	}
	if order.URI == "" {
		order.URI = uri
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{DNSNames: []string{name}}, key)
	if err != nil {
		return nil, nil, err
	}
	chain, _, err := client.CreateCertFromOrder(ctx, order, csr, true)
	if err != nil {
		return nil, nil, fmt.Errorf("finalizing the order: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	var data []byte
	for _, der := range chain {
		data = append(data, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	data = append(data, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})...)
	cert, err := tls.X509KeyPair(data, data)
	if err != nil {
		return nil, nil, fmt.Errorf("the CA's certificate: %w", err)
	}
	if err := cert.Leaf.VerifyHostname(name); err != nil {
		return nil, nil, fmt.Errorf("the CA's certificate: %w", err)
	}
	return &cert, data, nil
}

// authorize answers a challenge of the authorization at u, unless it is
// valid already, and waits for the CA to check it: the first of a's types,
// starting at the attempt'th, that the CA offers.
func (a *ACME) authorize(ctx context.Context, client *acme.Client, u string, attempt int) error {
	authz, err := client.GetAuthorization(ctx, u)
	if err != nil {
		return fmt.Errorf("authorization: %w", err)
	}
	if authz.Status == acme.StatusValid {
		return nil
	}
	var chal *acme.Challenge
	for i := range a.challenges {
		typ := a.challenges[(attempt+i)%len(a.challenges)]
		for _, c := range authz.Challenges {
			if c.Type == typ {
				chal = c
				break
			}
		}
		if chal != nil {
			break
		}
	}
	if chal == nil {
		return fmt.Errorf("the CA offers none of the challenges %s for %s", strings.Join(a.challenges, ", "), authz.Identifier.Value)
	}
	switch chal.Type {
	case config.ChallengeHTTP01:
		answer, err := client.HTTP01ChallengeResponse(chal.Token)
		if err != nil {
			return err
		}
		a.tokens.Store(chal.Token, answer)
		defer a.tokens.Delete(chal.Token)
		if a.shared {
			key := a.httpChallengeKey(chal.Token)
			a.shareChallenge(ctx, key, []byte(answer))
			defer a.unshareChallenge(key)
		}
	case config.ChallengeTLSALPN01:
		name := normalizeName(authz.Identifier.Value)
		cert, err := client.TLSALPN01ChallengeCert(chal.Token, name)
		if err != nil {
			return err
		}
		a.alpnCerts.Store(name, &cert)
		defer a.alpnCerts.Delete(name)
		if a.shared {
			data, err := encodeCert(&cert)
			if err != nil {
				return err
			}
			key := a.alpnChallengeKey(name)
			a.shareChallenge(ctx, key, data)
			defer a.unshareChallenge(key)
		}
	}
	if _, err := client.Accept(ctx, chal); err != nil {
		return fmt.Errorf("accepting the challenge: %w", err)
	}
	if _, err := client.WaitAuthorization(ctx, u); err != nil {
		return fmt.Errorf("the challenge: %w", err)
	}
	return nil
}

// normalizeName is a host name as ACME and a handshake compare it: in lower
// case, without a trailing dot.
func normalizeName(name string) string {
	return strings.ToLower(strings.TrimSuffix(name, "."))
}
