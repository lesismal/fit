package tlsmgr

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"time"
)

// maxSharedLookups bounds the lookups of shared HTTP-01 challenges under
// way at once; a request past it is answered as one with no challenge.
const maxSharedLookups = 16

// sharedLookupTimeout bounds a lookup of a shared challenge's answer, a
// variable for tests.
var sharedLookupTimeout = 5 * time.Second

// tokenPattern is what an HTTP-01 token looks like: base64url, as RFC 8555
// has it, so that a request's path makes no other storage key.
var tokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)

// isShared reports whether s says it is shared between hosts.
func isShared(s Storage) bool {
	sh, ok := s.(interface{ Shared() bool })
	return ok && sh.Shared()
}

// Shared reports whether a's storage is shared between hosts, so that a
// challenge another instance answers may be found there.
func (a *ACME) Shared() bool { return a.shared }

func (a *ACME) httpChallengeKey(token string) string {
	return "challenges/" + a.caKey + "/http-01/" + token
}

func (a *ACME) alpnChallengeKey(name string) string {
	return "challenges/" + a.caKey + "/tls-alpn-01/" + name
}

// shareChallenge keeps a challenge's answer in the shared storage. A
// failure is reported, not returned: this instance can still answer it.
func (a *ACME) shareChallenge(ctx context.Context, key string, value []byte) {
	if err := a.storage.Store(ctx, key, value); err != nil {
		a.logf("acme: keeping the challenge's answer in the shared storage: %v", err)
	}
}

// unshareChallenge removes a challenge's answer, with a time limit of its
// own, since the challenge's context may be done.
func (a *ACME) unshareChallenge(key string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.storage.Delete(ctx, key); err != nil {
		a.logf("acme: removing the challenge's answer from the shared storage: %v", err)
	}
}

// LookupHTTPChallenge looks the answer to the HTTP-01 challenge of token up
// in the shared storage, for a challenge another instance has under way,
// and calls done with it, or with ok false when there is none. It never
// blocks: the lookup runs on a goroutine of a's, at most maxSharedLookups
// at once and each within sharedLookupTimeout, and done is called there,
// or at once when the token cannot be one or every slot is taken. Call it
// only once HTTPChallenge found no answer, and only when Shared.
func (a *ACME) LookupHTTPChallenge(token string, done func(answer string, ok bool)) {
	if !a.shared || !tokenPattern.MatchString(token) {
		done("", false)
		return
	}
	select {
	case a.lookups <- struct{}{}:
	default:
		done("", false)
		return
	}
	go func() {
		defer func() { <-a.lookups }()
		ctx, cancel := context.WithTimeout(a.ctx, sharedLookupTimeout)
		defer cancel()
		data, err := a.storage.Load(ctx, a.httpChallengeKey(token))
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				a.logf("acme: looking up a challenge in the shared storage: %v", err)
			}
			done("", false)
			return
		}
		done(string(data), true)
	}()
}

// sharedALPNChallenge returns the certificate of a TLS-ALPN-01 challenge
// another instance has under way for name, from the shared storage, or nil.
// It blocks, within sharedLookupTimeout, on fib's handshake pool.
func (a *ACME) sharedALPNChallenge(name string) *tls.Certificate {
	if !a.shared {
		return nil
	}
	if err := checkName(name); err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(a.ctx, sharedLookupTimeout)
	defer cancel()
	data, err := a.storage.Load(ctx, a.alpnChallengeKey(name))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			a.logf("acme: looking up a challenge in the shared storage: %v", err)
		}
		return nil
	}
	cert, err := tls.X509KeyPair(data, data)
	if err != nil {
		a.logf("acme: %s: the shared challenge's certificate: %v", name, err)
		return nil
	}
	return &cert
}

// checkName refuses a name that would make another storage key.
func checkName(name string) error {
	if name == "" || len(name) > 253 {
		return fmt.Errorf("bad name %q", name)
	}
	for _, c := range []byte(name) {
		if !('a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' || c == '.') {
			return fmt.Errorf("bad name %q", name)
		}
	}
	return nil
}

// encodeCert writes a certificate's chain and key as PEM, as the storage
// keeps them.
func encodeCert(cert *tls.Certificate) ([]byte, error) {
	var data []byte
	for _, der := range cert.Certificate {
		data = append(data, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		return nil, err
	}
	return append(data, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})...), nil
}
