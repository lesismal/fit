package auth

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	stdhttp "net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	"github.com/lesismal/fit/observe"
)

func init() {
	module.Register(module.Info{ID: module.Middlewares + ".jwt", New: func() any { return new(JWT) }})
}

// JWT lets a request through only with a JSON Web Token whose signature
// verifies with one of its keys and whose claims hold: exp in the future,
// nbf, if any, in the past, and iss and aud as issuer and audience say. Its
// claims go to later middleware (ClaimsOf), its sub is who the request was
// authenticated as, and claims_headers copies claims onto request fields.
// A request without a valid token is answered 401 with a Bearer challenge.
//
// Keys come from PEM files, an HMAC secret file and a JWK set at a URL,
// which a goroutine of the control plane's fetches when the config is
// loaded, every jwks_refresh, and when a token names a kid the set does not
// have, as a key rotation brings: such a request waits for the fetch
// retained, as one does before the first fetch is done.
type JWT struct {
	JWKSURL        string            `json:"jwks_url,omitempty" doc:"The URL of a JWK set (RFC 7517) whose signing keys tokens are verified with."`
	JWKSRefresh    config.Duration   `json:"jwks_refresh,omitempty" default:"\"1h\"" doc:"How often the JWK set is fetched again; 1h when left out. A token naming a kid the set does not have has it fetched sooner, at most every 10s."`
	PublicKeyFiles []string          `json:"public_key_files,omitempty" doc:"PEM files of public keys or certificates (RSA, ECDSA or Ed25519) tokens are verified with."`
	HMACSecretFile string            `json:"hmac_secret_file,omitempty" doc:"A file whose contents are the secret of tokens signed with HS256, HS384 or HS512; at least 32 bytes."`
	Algorithms     []string          `json:"algorithms,omitempty" enum:"RS256,RS384,RS512,PS256,PS384,PS512,ES256,ES384,ES512,EdDSA,HS256,HS384,HS512" doc:"The algorithms tokens may be signed with; any that fits the keys when left out. none never is."`
	Issuer         string            `json:"issuer,omitempty" doc:"The iss a token must have; any when left out."`
	Audience       []string          `json:"audience,omitempty" doc:"Audiences of which a token's aud must name one; any when left out."`
	Leeway         config.Duration   `json:"leeway,omitempty" doc:"How far exp and nbf may be off, for clocks that differ; none when left out."`
	TokenFrom      string            `json:"token_from,omitempty" default:"\"header\"" doc:"Where the token is: header, after Bearer in the Authorization field; header:Name, the whole of that field; cookie:name; or query:name. header when left out."`
	ClaimsHeaders  map[string]string `json:"claims_headers,omitempty" doc:"Request fields to set to claims, as {\"X-User\": \"sub\"}: a string as it is, anything else as JSON. Any of these fields the client sent are removed, with or without the claim."`

	keys         *keySet // without a JWK set
	jwks         *jwks
	allowed      map[string]bool
	from         byte // 'a' Authorization, 'h' a header, 'c' a cookie, 'q' a query
	fromName     string
	leeway       float64
	claimHeaders []claimHeader
	graph        module.Holder
	now          func() time.Time
	verified     tokenCache
}

type claimHeader struct{ field, claim string }

// Provision loads the keys, starts fetching the JWK set, and checks the
// rest.
func (jw *JWT) Provision(ctx *module.Context) error {
	var static []*key
	for i, path := range jw.PublicKeyFiles {
		data, err := readSmall(path)
		var keys []*key
		if err == nil {
			keys, err = parsePublicKeys(data)
		}
		if err != nil {
			return ctx.Params.Field("public_key_files").Index(i).Errorf("%v", err)
		}
		static = append(static, keys...)
	}
	if jw.HMACSecretFile != "" {
		secret, err := readSmall(jw.HMACSecretFile)
		if err == nil && len(secret) < 32 {
			err = fmt.Errorf("the secret has %d bytes; at least 32 are needed", len(secret))
		}
		if err != nil {
			return ctx.Errorf("hmac_secret_file", "%v", err)
		}
		static = append(static, &key{secret: secret})
	}
	if jw.JWKSURL != "" {
		if u, err := url.Parse(jw.JWKSURL); err != nil || u.Scheme != "https" && u.Scheme != "http" || u.Host == "" {
			return ctx.Errorf("jwks_url", "%q is not an http or https URL", jw.JWKSURL)
		}
	} else if len(static) == 0 {
		return ctx.Errorf("", "a key is needed: jwks_url, public_key_files or hmac_secret_file")
	}
	jw.allowed = map[string]bool{}
	for name := range algorithms {
		jw.allowed[name] = len(jw.Algorithms) == 0 || slices.Contains(jw.Algorithms, name)
	}
	switch from := jw.TokenFrom; {
	case from == "" || from == "header":
		jw.from = 'a'
	case strings.HasPrefix(from, "header:") && validName(from[7:]):
		jw.from, jw.fromName = 'h', stdhttp.CanonicalHeaderKey(from[7:])
	case strings.HasPrefix(from, "cookie:") && validName(from[7:]):
		jw.from, jw.fromName = 'c', from[7:]
	case strings.HasPrefix(from, "query:") && len(from) > 6:
		jw.from, jw.fromName = 'q', from[6:]
	default:
		return ctx.Errorf("token_from", "%q is not header, header:Name, cookie:name or query:name", from)
	}
	for field, claim := range jw.ClaimsHeaders {
		if !validName(field) {
			return ctx.Errorf("claims_headers", "%q is not a field name", field)
		}
		if claim == "" {
			return ctx.Errorf("claims_headers", "%q names no claim", field)
		}
		jw.claimHeaders = append(jw.claimHeaders, claimHeader{stdhttp.CanonicalHeaderKey(field), claim})
	}
	slices.SortFunc(jw.claimHeaders, func(a, b claimHeader) int { return strings.Compare(a.field, b.field) })
	jw.leeway = jw.Leeway.Std().Seconds()
	jw.graph = ctx.Graph()
	jw.now = time.Now
	if jw.JWKSURL != "" {
		refresh := jw.JWKSRefresh.Std()
		if refresh <= 0 {
			refresh = defaultRefresh
		}
		jw.jwks = newJWKS(jw.JWKSURL, refresh, static)
	} else {
		jw.keys = newKeySet(static)
	}
	return nil
}

// Cleanup stops fetching the JWK set.
func (jw *JWT) Cleanup() {
	if jw.jwks != nil {
		jw.jwks.close()
	}
}

// readSmall reads a key file, of at most 1 MiB.
func readSmall(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err == nil && len(data) > 1<<20 {
		err = fmt.Errorf("%s: larger than 1 MiB", path)
	}
	return data, err
}

// Wrap returns next behind the check.
func (jw *JWT) Wrap(next module.Handler) module.Handler {
	return fibhttp.HandlerFunc(func(c *fibhttp.Context) {
		h := c.Request.Header
		for _, ch := range jw.claimHeaders {
			delete(h, ch.field)
		}
		raw, ok := jw.token(c.Request)
		if !ok {
			jw.refuse(c, false)
			return
		}
		keys := jw.keySet()
		sum := sha256.Sum256([]byte(raw))
		if v := jw.verified.get(sum, keys); v != nil {
			if jw.timely(v.claims) {
				jw.admit(c, v.claims, next)
			} else {
				jw.refuse(c, true)
			}
			return
		}
		t, err := parseToken(raw)
		if err != nil {
			jw.refuse(c, true)
			return
		}
		claims, err := jw.check(t, keys)
		if errors.Is(err, errUnknownKey) && jw.jwks != nil {
			// The set may have rotated: the request waits for a fetch.
			c.Retain()
			jw.graph.Retain()
			run := func() {
				defer jw.graph.Release()
				defer c.Release()
				if c.Err() != nil {
					return
				}
				claims, err := jw.check(t, jw.keySet())
				if err != nil {
					jw.refuse(c, true)
					return
				}
				jw.admit(c, claims, next)
			}
			if !jw.jwks.wait(run) {
				jw.graph.Release()
				c.Release()
				jw.refuse(c, true)
			}
			return
		}
		if err != nil {
			jw.refuse(c, true)
			return
		}
		jw.verified.put(sum, keys, claims)
		jw.admit(c, claims, next)
	})
}

// keySet returns the keys to verify with now, nil before a JWK set has
// been fetched.
func (jw *JWT) keySet() *keySet {
	if jw.jwks != nil {
		return jw.jwks.keys.Load()
	}
	return jw.keys
}

// token finds the request's token.
func (jw *JWT) token(r *stdhttp.Request) (string, bool) {
	var s string
	switch jw.from {
	case 'a':
		auth := r.Header.Get("Authorization")
		if len(auth) < 7 || !strings.EqualFold(auth[:7], "Bearer ") {
			return "", false
		}
		s = strings.TrimSpace(auth[7:])
	case 'h':
		s = strings.TrimSpace(r.Header.Get(jw.fromName))
	case 'c':
		cookie, err := r.Cookie(jw.fromName)
		if err != nil {
			return "", false
		}
		s = cookie.Value
	case 'q':
		s = r.URL.Query().Get(jw.fromName)
	}
	return s, s != ""
}

var (
	errUnknownKey = errors.New("no key verifies the token")
	errClaims     = errors.New("the token's claims do not hold")
)

// check verifies t's signature with keys and its claims, and returns them.
// It returns errUnknownKey when keys has no key t names, or no keys at all.
func (jw *JWT) check(t *token, keys *keySet) (map[string]any, error) {
	alg, ok := algorithms[t.header.Alg]
	if !ok || !jw.allowed[t.header.Alg] {
		return nil, errAlgorithm
	}
	if keys == nil {
		return nil, errUnknownKey
	}
	candidates, known := keys.lookup(t.header.Kid)
	verified := false
	for _, k := range candidates {
		if k.fits(t.header.Alg, alg) && k.verify(alg, t.signed, t.sig) {
			verified = true
			break
		}
	}
	if !verified {
		if !known {
			return nil, errUnknownKey
		}
		return nil, errMalformed
	}
	claims, err := t.claims()
	if err != nil {
		return nil, err
	}
	if !jw.timely(claims) {
		return nil, errClaims
	}
	if jw.Issuer != "" {
		if iss, _ := claims["iss"].(string); iss != jw.Issuer {
			return nil, errClaims
		}
	}
	if len(jw.Audience) > 0 && !jw.audienceHolds(claims["aud"]) {
		return nil, errClaims
	}
	return claims, nil
}

// timely reports whether claims hold at this time: exp is required, nbf
// is not.
func (jw *JWT) timely(claims map[string]any) bool {
	now := float64(jw.now().UnixNano()) / 1e9
	exp, ok := numericDate(claims["exp"])
	if !ok || now > exp+jw.leeway {
		return false
	}
	if v, present := claims["nbf"]; present {
		nbf, ok := numericDate(v)
		if !ok || now < nbf-jw.leeway {
			return false
		}
	}
	return true
}

// tokenCache keeps tokens that were verified, by their SHA-256, so that a
// token used again, as one is for its whole life, costs a hash and a lookup
// rather than a signature check, which takes tens of microseconds of a
// worker's CPU for RSA, ECDSA and Ed25519. An entry holds only while the
// keys are those it was verified with, so a key a JWK set drops is dropped
// here too; exp and nbf are checked on every use. It is bounded: a shard
// that is full forgets one entry at random for each it takes.
type tokenCache struct {
	shards [cacheShards]cacheShard
}

const (
	cacheShards   = 16
	cacheShardMax = 1024
)

type cacheShard struct {
	mu      sync.Mutex
	entries map[[sha256.Size]byte]*verifiedToken
}

type verifiedToken struct {
	keys   *keySet
	claims map[string]any
}

func (tc *tokenCache) get(sum [sha256.Size]byte, keys *keySet) *verifiedToken {
	if keys == nil {
		return nil
	}
	sh := &tc.shards[sum[0]%cacheShards]
	sh.mu.Lock()
	v := sh.entries[sum]
	sh.mu.Unlock()
	if v == nil || v.keys != keys {
		return nil
	}
	return v
}

func (tc *tokenCache) put(sum [sha256.Size]byte, keys *keySet, claims map[string]any) {
	sh := &tc.shards[sum[0]%cacheShards]
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if sh.entries == nil {
		sh.entries = map[[sha256.Size]byte]*verifiedToken{}
	}
	if len(sh.entries) >= cacheShardMax {
		for old := range sh.entries {
			delete(sh.entries, old)
			break
		}
	}
	sh.entries[sum] = &verifiedToken{keys: keys, claims: claims}
}

// numericDate reads a NumericDate claim.
func numericDate(v any) (float64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	f, err := n.Float64()
	return f, err == nil && !math.IsInf(f, 0) && !math.IsNaN(f)
}

// audienceHolds reports whether aud, a string or an array of them, names
// one of the audiences.
func (jw *JWT) audienceHolds(aud any) bool {
	switch aud := aud.(type) {
	case string:
		return slices.Contains(jw.Audience, aud)
	case []any:
		for _, a := range aud {
			if s, ok := a.(string); ok && slices.Contains(jw.Audience, s) {
				return true
			}
		}
	}
	return false
}

// admit lets the request through with its claims.
func (jw *JWT) admit(c *fibhttp.Context, claims map[string]any, next module.Handler) {
	e := observe.EntryOf(c)
	if e == nil {
		e = observe.Track(c)
	}
	e.SetClaims(claims)
	if sub, ok := claims["sub"].(string); ok && e.User == "" {
		e.User = sub
	}
	h := c.Request.Header
	for _, ch := range jw.claimHeaders {
		v, ok := claims[ch.claim]
		if !ok {
			continue
		}
		s, isString := v.(string)
		if !isString {
			b, _ := json.Marshal(v)
			s = string(b)
		}
		h[ch.field] = []string{headerSafe(s)}
	}
	next.ServeHTTP(c)
}

// refuse answers 401 with a Bearer challenge; invalid says a token was
// sent but did not pass.
func (jw *JWT) refuse(c *fibhttp.Context, invalid bool) {
	challenge := `Bearer realm="fit"`
	if invalid {
		challenge += `, error="invalid_token"`
	}
	_ = c.WriteResponse(fibhttp.Response{
		StatusCode: stdhttp.StatusUnauthorized,
		Header: stdhttp.Header{
			"Www-Authenticate": {challenge},
			"Content-Type":     {"text/plain; charset=utf-8"},
		},
		Body: unauthorized,
	})
}

// ClaimsOf returns the claims of the token the request c serves was let in
// with, or nil. They may be shared with other requests of the same token,
// and must not be changed.
func ClaimsOf(c *fibhttp.Context) map[string]any {
	if e := observe.EntryOf(c); e != nil {
		return e.Claims()
	}
	return nil
}
