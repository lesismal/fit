// Package mediaauth is fit's media auth: the named policies of the config's
// media_auth, which say who may publish and play which streams, and the
// checks the handlers and roles that take media make against them (whip,
// whep, webrtc_proxy; the streaming role and the media pages). See
// docs/decisions/030-media-auth-and-pages.md.
//
// A check is synchronous and cheap, and never blocks a worker: a static
// token is looked up by its SHA-256, a signed token's HMAC is checked and
// its claims read, a plaintext password is compared in constant time.
// The one slow check, a bcrypt password_hash, runs on a small bounded pool
// of the package's own (Check, Login, Guard), and a password found right is
// remembered, as a keyed SHA-256, so that the next check of it is at once;
// Allow reports ErrPending for it instead of waiting.
//
// Policies are found by name per engine (the engine a module's Context and
// an app.Host's Upstream name), as webrtc.media's server is: a *Policy
// stays the same across reloads and changes in place, so that a handler,
// or a role, that holds one sees what the config now says. The role
// media_auth moves them with every config applied.
package mediaauth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lesismal/fib"
	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/config"
)

// Action is what a request asks to do with a stream.
type Action uint8

// The actions.
const (
	Publish Action = iota + 1
	Play
)

// String returns the action's name in the config: publish or play.
func (a Action) String() string {
	switch a {
	case Publish:
		return "publish"
	case Play:
		return "play"
	}
	return "unknown"
}

// ParseAction returns the action named s, publish or play.
func ParseAction(s string) (Action, bool) {
	switch s {
	case "publish":
		return Publish, true
	case "play":
		return Play, true
	}
	return 0, false
}

// Grant is what a credential may do: the stream patterns it may publish and
// play, with * matching any run of characters.
type Grant struct {
	Publish []string `json:"pub,omitempty"`
	Play    []string `json:"play,omitempty"`
}

// Allows reports whether g lets a take place on stream.
func (g Grant) Allows(a Action, stream string) bool {
	var list []string
	switch a {
	case Publish:
		list = g.Publish
	case Play:
		list = g.Play
	}
	for _, p := range list {
		if Match(p, stream) {
			return true
		}
	}
	return false
}

// Match reports whether stream matches pattern, whose * matches any run of
// characters, '/' among them, and whose other characters match themselves.
func Match(pattern, stream string) bool {
	// The usual linear wildcard match: on a mismatch, go back to the last
	// star and let it take one character more.
	p, s := 0, 0
	star, mark := -1, 0
	for s < len(stream) {
		switch {
		case p < len(pattern) && pattern[p] == '*':
			star, mark = p, s
			p++
		case p < len(pattern) && pattern[p] == stream[s]:
			p++
			s++
		case star >= 0:
			p = star + 1
			mark++
			s = mark
		default:
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

// The errors of a check. Status maps them to an HTTP status.
var (
	// ErrNoCredentials is a request with no credentials for an action
	// the policy does not make public: 401, with a challenge.
	ErrNoCredentials = errors.New("mediaauth: credentials are needed")
	// ErrBadCredentials is an unknown, expired or tampered token, or a
	// wrong user or password: 401.
	ErrBadCredentials = errors.New("mediaauth: the credentials are not valid")
	// ErrForbidden is a valid credential that may not take the action on
	// the stream: 403.
	ErrForbidden = errors.New("mediaauth: the credentials do not allow this")
	// ErrPending is Allow's answer for a password checked against a bcrypt
	// hash it has not found right before: Check, Login and Guard check it on
	// the package's pool.
	ErrPending = errors.New("mediaauth: the password needs a bcrypt check")
	// ErrBusy is a bcrypt check the pool had no room or time for: 503.
	ErrBusy = errors.New("mediaauth: too many password checks are waiting")
	// ErrUnavailable is a policy the running config no longer has, or does
	// not have yet: 503.
	ErrUnavailable = errors.New("mediaauth: the policy is not loaded")
)

// Status returns the HTTP status for a check's error: 0 for nil, 401 for
// missing or bad credentials, 403 for a forbidden action, 503 otherwise.
func Status(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, ErrNoCredentials), errors.Is(err, ErrBadCredentials):
		return 401
	case errors.Is(err, ErrForbidden):
		return 403
	}
	return 503
}

// TokenPrefix starts every token fit signs, telling it from a static one.
const TokenPrefix = "fm1."

// MaxTokenBytes is the longest token looked at; a longer one is bad.
const MaxTokenBytes = 4096

// Policy is a named media_auth policy. Its methods may be called from any
// goroutine; a nil *Policy lets everything through, as a handler without
// auth does.
type Policy struct {
	name string
	// key signs tokens when the config has no secret; it lasts as long as
	// the process does.
	key [32]byte
	// mac keys what is remembered of bcrypt passwords found right.
	mac [32]byte

	st atomic.Pointer[state]
	// applied says the role has set st from a config applied, after which
	// Provision leaves it to the role.
	applied bool
}

// state is a policy as compiled from one config, swapped whole.
type state struct {
	removed bool
	public  [3]bool // by Action
	tokens  map[[32]byte]*token
	users   map[string]*user
	decoy   *user // compared with for an unknown user, so that it takes as long
	signKey []byte
	ttl     time.Duration
}

type token struct {
	subject string
	grant   Grant
}

type user struct {
	name     string
	password string   // plaintext, for Digest; empty with hash
	sum      [32]byte // SHA-256 of password
	hash     []byte   // bcrypt
	grant    Grant
	good     *atomic.Pointer[[32]byte] // the keyed SHA-256 of the password last found right
}

func newPolicy(name string) *Policy {
	p := &Policy{name: name}
	_, _ = rand.Read(p.key[:])
	_, _ = rand.Read(p.mac[:])
	return p
}

// New returns a policy made from cfg alone, outside any engine's registry:
// for fit media-token, and tests. Its key without a secret is its own.
func New(cfg *config.MediaAuth) (*Policy, error) {
	p := newPolicy(cfg.Name)
	st, err := p.compile(cfg, nil)
	if err != nil {
		return nil, err
	}
	p.st.Store(st)
	return p, nil
}

// compile makes cfg's state for p, keeping what old, p's state until now,
// remembers of passwords whose hashes are the same.
func (p *Policy) compile(cfg *config.MediaAuth, old *state) (*state, error) {
	st := &state{tokens: map[[32]byte]*token{}, users: map[string]*user{}, ttl: cfg.TTL()}
	for _, a := range cfg.Public {
		if act, ok := ParseAction(a); ok {
			st.public[act] = true
		} else {
			return nil, fmt.Errorf("public: %q is not publish or play", a)
		}
	}
	for i, t := range cfg.Tokens {
		var sum [32]byte
		switch {
		case t.Token != "":
			sum = sha256.Sum256([]byte(t.Token))
		case t.HashBytes() != nil:
			copy(sum[:], t.HashBytes())
		default:
			return nil, fmt.Errorf("tokens[%d]: set token or token_sha256", i)
		}
		subject := "token"
		if t.Name != "" {
			subject = "token:" + t.Name
		}
		if err := checkPatterns(t.Publish, t.Play); err != nil {
			return nil, fmt.Errorf("tokens[%d]: %w", i, err)
		}
		st.tokens[sum] = &token{subject: subject, grant: Grant{Publish: t.Publish, Play: t.Play}}
	}
	for i, u := range cfg.Users {
		if err := checkPatterns(u.Publish, u.Play); err != nil {
			return nil, fmt.Errorf("users[%d]: %w", i, err)
		}
		x := &user{name: u.Name, password: u.Password, grant: Grant{Publish: u.Publish, Play: u.Play}}
		switch {
		case u.Password != "":
			x.sum = sha256.Sum256([]byte(u.Password))
		case u.PasswordHash != "":
			if _, err := bcrypt.Cost([]byte(u.PasswordHash)); err != nil {
				return nil, fmt.Errorf("users[%d]: password_hash is not a bcrypt hash: %v", i, err)
			}
			x.hash = []byte(u.PasswordHash)
			x.good = new(atomic.Pointer[[32]byte])
			if old != nil {
				if o := old.users[u.Name]; o != nil && o.good != nil && string(o.hash) == u.PasswordHash {
					x.good = o.good
				}
			}
		default:
			return nil, fmt.Errorf("users[%d]: set password or password_hash", i)
		}
		if st.users[u.Name] != nil {
			return nil, fmt.Errorf("users[%d]: %q is listed twice", i, u.Name)
		}
		st.users[u.Name] = x
	}
	st.decoy = &user{}
	for _, u := range st.users {
		if u.hash != nil {
			// An unknown user's password goes to the pool too, so that a
			// refusal takes as long whether the user exists or not.
			st.decoy = &user{hash: decoyHash(), good: new(atomic.Pointer[[32]byte])}
			break
		}
	}
	if cfg.Secret != "" {
		m := hmac.New(sha256.New, []byte(cfg.Secret))
		m.Write([]byte("fit media token\x00" + cfg.Name))
		st.signKey = m.Sum(nil)
	} else {
		st.signKey = p.key[:]
	}
	return st, nil
}

func checkPatterns(lists ...[]string) error {
	for _, l := range lists {
		for _, s := range l {
			if !config.ValidMediaPattern(s) {
				return fmt.Errorf("%q is not a stream pattern", s)
			}
		}
	}
	return nil
}

// Name returns the policy's name.
func (p *Policy) Name() string { return p.name }

// state returns p's state, or nil when it is not loaded or removed.
func (p *Policy) state() *state {
	st := p.st.Load()
	if st == nil || st.removed {
		return nil
	}
	return st
}

// Public reports whether anyone may take a without credentials. A nil
// policy makes every action public.
func (p *Policy) Public(a Action) bool {
	if p == nil {
		return true
	}
	st := p.state()
	return st != nil && a < Action(len(st.public)) && st.public[a]
}

// TTL returns how long the tokens the policy issues are good for when their
// issuer names no lifetime.
func (p *Policy) TTL() time.Duration {
	if st := p.state(); st != nil {
		return st.ttl
	}
	return config.DefaultMediaTokenTTL
}

// now is the clock tokens are checked against, for tests to move.
var now = time.Now

// Allow checks whether c may take a on stream, without waiting: it returns
// the subject the request is authenticated as (a user's name, token or
// token:<name> for a static token, a signed token's subject; "" for an
// action the policy makes public), or an error Status maps to an HTTP
// status. A token is checked before a user and password. A password a
// bcrypt hash must check, that was not found right before, gives
// ErrPending: Check does that check off the caller's goroutine.
func (p *Policy) Allow(a Action, stream string, c Credentials) (subject string, err error) {
	if p == nil {
		return "", nil
	}
	st := p.state()
	if st == nil {
		return "", ErrUnavailable
	}
	if a < Action(len(st.public)) && st.public[a] {
		return "", nil
	}
	var g Grant
	switch {
	case c.Token != "":
		if subject, g, err = p.token(st, c.Token); err != nil {
			return "", err
		}
	case c.User != "":
		u, err := p.password(st, c.User, c.Password)
		if err != nil {
			return "", err
		}
		subject, g = u.name, u.grant
	default:
		return "", ErrNoCredentials
	}
	if !g.Allows(a, stream) {
		return subject, ErrForbidden
	}
	return subject, nil
}

// token checks a token: one fit signed, or a static one.
func (p *Policy) token(st *state, tok string) (string, Grant, error) {
	if len(tok) > MaxTokenBytes {
		return "", Grant{}, ErrBadCredentials
	}
	if strings.HasPrefix(tok, TokenPrefix) {
		cl, err := verify(st.signKey, tok)
		if err != nil {
			return "", Grant{}, err
		}
		return cl.Subject, cl.Grant, nil
	}
	// Looked up by its SHA-256: how long the lookup takes says nothing of
	// the token.
	t := st.tokens[sha256.Sum256([]byte(tok))]
	if t == nil {
		return "", Grant{}, ErrBadCredentials
	}
	return t.subject, t.grant, nil
}

// password checks a user's password, at once where it can: ErrPending for
// a bcrypt hash that has not found it right before.
func (p *Policy) password(st *state, name, password string) (*user, error) {
	u := st.users[name]
	known := u != nil
	if !known {
		u = st.decoy
	}
	if u.hash == nil {
		sum := sha256.Sum256([]byte(password))
		if subtle.ConstantTimeCompare(sum[:], u.sum[:]) != 1 || !known {
			return nil, ErrBadCredentials
		}
		return u, nil
	}
	mac := p.remembered(password)
	if good := u.good.Load(); good != nil && subtle.ConstantTimeCompare(good[:], mac[:]) == 1 {
		return u, nil
	}
	return u, ErrPending
}

// remembered is what a user's record keeps of a password found right: a
// SHA-256 keyed with the policy's own key, which no one could check
// passwords against.
func (p *Policy) remembered(password string) [32]byte {
	m := hmac.New(sha256.New, p.mac[:])
	m.Write([]byte(password))
	var out [32]byte
	m.Sum(out[:0])
	return out
}

// Check is Allow, with a bcrypt check done on the package's pool: done is
// called once, before Check returns when no bcrypt check is needed, or
// later on the pool's goroutine. The pool is bounded: a check it has no
// room or time for gets ErrBusy.
func (p *Policy) Check(a Action, stream string, c Credentials, done func(subject string, err error)) {
	subject, err := p.Allow(a, stream, c)
	if !errors.Is(err, ErrPending) {
		done(subject, err)
		return
	}
	p.slow(c.User, c.Password, func(u *user, err error) {
		switch {
		case err != nil:
			done("", err)
		case !u.grant.Allows(a, stream):
			done(u.name, ErrForbidden)
		default:
			done(u.name, nil)
		}
	})
}

// Login checks a user's password, for a page's login: done is given the
// user's name and grant, or ErrBadCredentials; as Check, it is called
// before Login returns or on the pool.
func (p *Policy) Login(name, password string, done func(subject string, g Grant, err error)) {
	if p == nil {
		done("", Grant{}, ErrUnavailable)
		return
	}
	st := p.state()
	if st == nil {
		done("", Grant{}, ErrUnavailable)
		return
	}
	u, err := p.password(st, name, password)
	switch {
	case err == nil:
		done(u.name, u.grant, nil)
	case errors.Is(err, ErrPending):
		p.slow(name, password, func(u *user, err error) {
			if err != nil {
				done("", Grant{}, err)
				return
			}
			done(u.name, u.grant, nil)
		})
	default:
		done("", Grant{}, err)
	}
}

// slow checks name's password against its bcrypt hash on the pool, and
// remembers it when right.
func (p *Policy) slow(name, password string, done func(*user, error)) {
	checks.submit(func(late bool) {
		if late {
			done(nil, ErrBusy)
			return
		}
		st := p.state()
		if st == nil {
			done(nil, ErrUnavailable)
			return
		}
		u := st.users[name]
		hash := []byte(nil)
		if u != nil {
			hash = u.hash
		}
		if hash == nil {
			// An unknown user, or one whose password is no longer hashed
			// after a reload: the same work all the same.
			_ = bcrypt.CompareHashAndPassword(decoyHash(), []byte(password))
			if u != nil {
				if _, err := p.password(st, name, password); err == nil {
					done(u, nil)
					return
				}
			}
			done(nil, ErrBadCredentials)
			return
		}
		if bcrypt.CompareHashAndPassword(hash, []byte(password)) != nil {
			done(nil, ErrBadCredentials)
			return
		}
		mac := p.remembered(password)
		u.good.Store(&mac)
		done(u, nil)
	})
}

// decoyHash is checked against for a user with no hash, so that a refusal
// takes as long either way: a bcrypt hash of a random password, at the
// default cost.
var decoyHash = sync.OnceValue(func() []byte {
	var b [16]byte
	_, _ = rand.Read(b[:])
	h, _ := bcrypt.GenerateFromPassword([]byte(base64.RawURLEncoding.EncodeToString(b[:])), bcrypt.DefaultCost)
	return h
})

// Password returns the plaintext password of user, for RTSP's Digest, and
// whether the policy has one: a user with a password_hash has none.
func (p *Policy) Password(name string) (string, bool) {
	if p == nil {
		return "", false
	}
	st := p.state()
	if st == nil {
		return "", false
	}
	u := st.users[name]
	if u == nil || u.password == "" {
		return "", false
	}
	return u.password, true
}

// HasUsers reports whether the policy has users who could log in.
func (p *Policy) HasUsers() bool {
	st := p.state()
	return st != nil && len(st.users) > 0
}

// claims are what a signed token says.
type claims struct {
	Subject string `json:"sub"`
	Expiry  int64  `json:"exp"`
	Grant
}

// Issue returns a token signed with the policy's key: good for subject to
// take g's actions on g's streams until expiry, now and ttl, the policy's
// token_ttl when ttl is not positive. The subject is at most 128 bytes,
// with no control characters, and the patterns those of the config.
func (p *Policy) Issue(subject string, g Grant, ttl time.Duration) (tok string, expiry time.Time, err error) {
	st := p.state()
	if st == nil {
		return "", time.Time{}, ErrUnavailable
	}
	if subject == "" || len(subject) > 128 || strings.ContainsFunc(subject, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", time.Time{}, fmt.Errorf("mediaauth: %q is not a subject: 1 to 128 bytes with no control characters", subject)
	}
	if err := checkPatterns(g.Publish, g.Play); err != nil {
		return "", time.Time{}, fmt.Errorf("mediaauth: %w", err)
	}
	if ttl <= 0 {
		ttl = st.ttl
	}
	expiry = now().Add(ttl).Truncate(time.Second)
	body, err := json.Marshal(claims{Subject: subject, Expiry: expiry.Unix(),
		Grant: Grant{Publish: slices.Clone(g.Publish), Play: slices.Clone(g.Play)}})
	if err != nil {
		return "", time.Time{}, err
	}
	payload := base64.RawURLEncoding.EncodeToString(body)
	if len(TokenPrefix)+len(payload)+44 > MaxTokenBytes {
		return "", time.Time{}, errors.New("mediaauth: the grant is too long for a token")
	}
	return TokenPrefix + payload + "." + sign(st.signKey, payload), expiry, nil
}

func sign(key []byte, payload string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(TokenPrefix))
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// verify checks a signed token's MAC and expiry, and returns its claims.
func verify(key []byte, tok string) (*claims, error) {
	rest := tok[len(TokenPrefix):]
	dot := strings.IndexByte(rest, '.')
	if dot < 0 {
		return nil, ErrBadCredentials
	}
	payload, mac := rest[:dot], rest[dot+1:]
	if !hmac.Equal([]byte(mac), []byte(sign(key, payload))) {
		return nil, ErrBadCredentials
	}
	body, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return nil, ErrBadCredentials
	}
	var cl claims
	if err := json.Unmarshal(body, &cl); err != nil || cl.Subject == "" {
		return nil, ErrBadCredentials
	}
	if now().Unix() >= cl.Expiry {
		return nil, ErrBadCredentials
	}
	return &cl, nil
}

// registry is the policies by engine and name.
var (
	regMu    sync.Mutex
	registry = map[*fib.Engine]map[string]*Policy{}
)

// get returns the policy of key named name, made empty when there is none.
// regMu is held.
func get(key *fib.Engine, name string) *Policy {
	m := registry[key]
	if m == nil {
		m = map[string]*Policy{}
		registry[key] = m
	}
	p := m[name]
	if p == nil {
		p = newPolicy(name)
		m[name] = p
	}
	return p
}
