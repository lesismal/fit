package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	stdhttp "net/http"
	"sync"
	"sync/atomic"
	"time"
)

// keySet is the keys a jwt middleware verifies with at a time.
type keySet struct {
	byID  map[string][]*key
	plain []*key // those without a kid, tried for every token
}

func newKeySet(keys ...[]*key) *keySet {
	s := &keySet{byID: map[string][]*key{}}
	for _, list := range keys {
		for _, k := range list {
			if k.id == "" {
				s.plain = append(s.plain, k)
			} else {
				s.byID[k.id] = append(s.byID[k.id], k)
			}
		}
	}
	return s
}

// lookup returns the keys a token with kid may be verified with, and whether
// a key has that kid.
func (s *keySet) lookup(kid string) (keys []*key, known bool) {
	if kid == "" {
		return s.plain, true
	}
	byID, known := s.byID[kid]
	if len(s.plain) == 0 {
		return byID, known
	}
	return append(append([]*key(nil), byID...), s.plain...), known
}

// parsePublicKeys reads the public keys and certificates of a PEM file.
func parsePublicKeys(data []byte) ([]*key, error) {
	var keys []*key
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			break
		}
		var pub any
		var err error
		switch block.Type {
		case "PUBLIC KEY":
			pub, err = x509.ParsePKIXPublicKey(block.Bytes)
		case "RSA PUBLIC KEY":
			pub, err = x509.ParsePKCS1PublicKey(block.Bytes)
		case "CERTIFICATE":
			var cert *x509.Certificate
			if cert, err = x509.ParseCertificate(block.Bytes); err == nil {
				pub = cert.PublicKey
			}
		default:
			continue
		}
		if err != nil {
			return nil, err
		}
		switch pub.(type) {
		case *rsa.PublicKey, *ecdsa.PublicKey, ed25519.PublicKey:
		default:
			return nil, fmt.Errorf("a %T key cannot verify a token", pub)
		}
		keys = append(keys, &key{pub: pub})
	}
	if len(keys) == 0 {
		return nil, errors.New("no PEM public key or certificate found")
	}
	return keys, nil
}

// jwk is a JSON Web Key (RFC 7517), as far as jwt reads one.
type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// parseJWKS reads the keys of a JWK set that can verify a signature. Keys
// for encryption, of another type, or that do not parse are left out, as a
// set may hold keys for others; a set with none left is an error.
func parseJWKS(data []byte) ([]*key, error) {
	var set struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.Unmarshal(data, &set); err != nil {
		return nil, err
	}
	var keys []*key
	for _, j := range set.Keys {
		if j.Use != "" && j.Use != "sig" {
			continue
		}
		if j.Alg != "" {
			if _, ok := algorithms[j.Alg]; !ok || j.Alg[0] == 'H' {
				continue
			}
		}
		if pub := j.public(); pub != nil {
			keys = append(keys, &key{id: j.Kid, alg: j.Alg, pub: pub})
		}
	}
	if len(keys) == 0 {
		return nil, errors.New("the JWK set has no key that verifies signatures")
	}
	return keys, nil
}

// public is j's public key, or nil.
func (j *jwk) public() any {
	b64 := base64.RawURLEncoding
	switch j.Kty {
	case "RSA":
		n, err1 := b64.DecodeString(j.N)
		e, err2 := b64.DecodeString(j.E)
		if err1 != nil || err2 != nil || len(n) < 256 || len(e) == 0 || len(e) > 4 {
			return nil // RSA keys below 2048 bits are refused
		}
		exp := int(new(big.Int).SetBytes(e).Int64())
		if exp < 3 || exp%2 == 0 {
			return nil
		}
		return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: exp}
	case "EC":
		var curve elliptic.Curve
		switch j.Crv {
		case "P-256":
			curve = elliptic.P256()
		case "P-384":
			curve = elliptic.P384()
		case "P-521":
			curve = elliptic.P521()
		default:
			return nil
		}
		x, err1 := b64.DecodeString(j.X)
		y, err2 := b64.DecodeString(j.Y)
		size := (curve.Params().BitSize + 7) / 8
		if err1 != nil || err2 != nil || len(x) != size || len(y) != size {
			return nil
		}
		// ParseUncompressedPublicKey refuses a point not on the curve.
		pub, err := ecdsa.ParseUncompressedPublicKey(curve, append(append([]byte{4}, x...), y...))
		if err != nil {
			return nil
		}
		return pub
	case "OKP":
		x, err := b64.DecodeString(j.X)
		if j.Crv != "Ed25519" || err != nil || len(x) != ed25519.PublicKeySize {
			return nil
		}
		return ed25519.PublicKey(x)
	}
	return nil
}

// Bounds of fetching a JWK set.
const (
	maxJWKSBytes   = 1 << 20
	jwksTimeout    = 10 * time.Second
	maxKeyWaiters  = 1024
	defaultRefresh = time.Hour
)

// minRefetch is the least time between two fetches of a JWK set that a
// token with an unknown kid asks for, so that such tokens cannot have fit
// fetch it on every request. A variable for tests.
var minRefetch = 10 * time.Second

// jwks keeps a JWK set fetched from a URL, on a goroutine of the control
// plane's: once at the start, every refresh, and when a token names a kid
// the set does not have, at most every minRefetch. Requests that wait for a
// fetch wait retained, and are verified again when it is done.
type jwks struct {
	url     string
	refresh time.Duration
	// minRefetch is the package's, when the set was made.
	minRefetch time.Duration
	client     *stdhttp.Client
	// static are the keys of the config's own, which every set has too.
	static []*key
	keys   atomic.Pointer[keySet]
	err    atomic.Pointer[error] // the last fetch's, or nil

	mu      sync.Mutex
	waiters []func()
	fetched time.Time // when the last fetch began

	ctx    context.Context
	cancel context.CancelFunc
	wake   chan struct{}
	done   chan struct{}
}

func newJWKS(url string, refresh time.Duration, static []*key) *jwks {
	j := &jwks{url: url, refresh: refresh, minRefetch: minRefetch, static: static,
		client: &stdhttp.Client{Timeout: jwksTimeout},
		wake:   make(chan struct{}, 1), done: make(chan struct{})}
	j.ctx, j.cancel = context.WithCancel(context.Background())
	go j.run()
	return j
}

// wait queues then to run once the next fetch is done, and asks for one;
// it reports false when too many wait already.
func (j *jwks) wait(then func()) bool {
	j.mu.Lock()
	if len(j.waiters) >= maxKeyWaiters {
		j.mu.Unlock()
		return false
	}
	j.waiters = append(j.waiters, then)
	j.mu.Unlock()
	select {
	case j.wake <- struct{}{}:
	default:
	}
	return true
}

// close stops the goroutine, abandoning a fetch under way, without waiting
// for it: it may be called where nothing may wait. No request waits by
// then, since each holds the config until it is done.
func (j *jwks) close() { j.cancel() }

func (j *jwks) run() {
	defer close(j.done)
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-j.ctx.Done():
			return
		case <-timer.C:
		case <-j.wake:
			j.mu.Lock()
			wait := j.minRefetch - time.Since(j.fetched)
			j.mu.Unlock()
			if wait > 0 {
				select {
				case <-j.ctx.Done():
					return
				case <-time.After(wait):
				}
			}
		}
		// Those waiting by its end, those that came during it too, are
		// verified with what it brought.
		j.fetch()
		for _, then := range j.takeWaiters() {
			then()
		}
		timer.Reset(j.refresh)
	}
}

// fetch loads the set; one that fails keeps the keys there were.
func (j *jwks) fetch() {
	j.mu.Lock()
	j.fetched = time.Now()
	j.mu.Unlock()
	keys, err := j.get()
	if err != nil {
		err = fmt.Errorf("jwt: fetching %s: %w", j.url, err)
		j.err.Store(&err)
		return
	}
	j.err.Store(nil)
	j.keys.Store(newKeySet(j.static, keys))
}

func (j *jwks) get() ([]*key, error) {
	req, err := stdhttp.NewRequestWithContext(j.ctx, stdhttp.MethodGet, j.url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := j.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != stdhttp.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxJWKSBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxJWKSBytes {
		return nil, fmt.Errorf("larger than %d bytes", maxJWKSBytes)
	}
	return parseJWKS(data)
}

func (j *jwks) takeWaiters() []func() {
	j.mu.Lock()
	defer j.mu.Unlock()
	// The fetch they asked for is the one just done.
	select {
	case <-j.wake:
	default:
	}
	waiters := j.waiters
	j.waiters = nil
	return waiters
}
