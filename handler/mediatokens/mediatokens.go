// Package mediatokens is the media_tokens handler: a route a business backend
// calls to issue many time-limited media_auth tokens at once, for many
// users, groups and concurrent meetings, and to revoke every token signed
// before by rotating the policy's secret. It is deliberately not the Admin
// API: its route carries its own middleware (jwt, basic_auth, forward_auth,
// rate_limit), so a business backend never holds the Admin API's all-powerful
// token.
//
// Nothing blocks a worker: issuing a batch is pure computation on the
// policy's atomic state, and the one slow part of a revoke, writing the new
// secret to the policy's secret_file, runs on a pool of the module's own of
// one worker and a queue of one, with revoke_timeout to bound it; the answer
// is written from there through the Context's Async, so the handler returns
// at once. Every part is bounded: max_tokens a request, max_body_bytes a
// body, one revoke waiting, revoke_timeout a revoke, max_ttl a token, and the
// prefixes a token may be signed for.
package mediatokens

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	stdhttp "net/http"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/mediaauth"
	"github.com/lesismal/fit/module"
)

func init() {
	module.Register(module.Info{ID: module.Handlers + ".media_tokens", New: func() any { return new(Tokens) },
		Doc: "Issues time-limited media_auth tokens in batches, for a route a business backend calls, and revokes the tokens of one key of the policy's keyring, or of all of them, by rotating their secrets."})
}

// The defaults and bounds of the params, and the limits of a token.
const (
	DefaultPrefix        = "/media-tokens"
	DefaultMaxTTL        = 24 * time.Hour
	DefaultMaxTokens     = 100
	MinMaxTokens         = 1
	MaxMaxTokens         = 1000
	DefaultMaxBodyBytes  = 65536
	MinMaxBodyBytes      = 256
	MaxMaxBodyBytes      = 1 << 20
	DefaultRevokeTimeout = 10 * time.Second
	secretBytes          = 32
	maxSubjectBytes      = 128
	maxPatternBytes      = 256
	// retryAfter is the Retry-After of a 503, and of a 504, in seconds.
	retryAfter = "1"
)

// Tokens is the media_tokens handler.
type Tokens struct {
	Prefix        string          `json:"prefix,omitempty" default:"\"/media-tokens\"" doc:"The path the route serves under: the route must match it and what is below it, as /media-tokens and /media-tokens/*; a request outside it gets 404. /media-tokens when left out."`
	Auth          string          `json:"auth,omitempty" doc:"The media_auth policy that signs the tokens, as whip's auth names one: it must set secret_file, or a keyring whose keys each set secret_file, so that a revoke can rotate a secret file and outlive a restart."`
	Prefixes      []string        `json:"prefixes,omitempty" doc:"The stream-name prefixes this route may sign for: every publish and play pattern of a request must start with one of them, so that a token cannot be signed for a stream outside them, as mtg- for mtg-42-*. Any valid pattern when left out."`
	MaxTTL        config.Duration `json:"max_ttl,omitempty" default:"\"24h\"" doc:"The longest a request may ask a token to be good for; a longer ttl is answered 400. 24h when left out."`
	MaxTokens     int             `json:"max_tokens,omitempty" min:"1" max:"1000" default:"100" doc:"The most tokens one request may ask for; more is answered 400. 100 when left out."`
	MaxBodyBytes  int             `json:"max_body_bytes,omitempty" min:"256" max:"1048576" default:"65536" doc:"The largest request body the route takes; a larger one is answered 413. 64 KiB when left out."`
	RevokeTimeout config.Duration `json:"revoke_timeout,omitempty" default:"\"10s\"" doc:"How long a revoke may wait for the secret file's write before it is answered 504; the write, one at a time on a pool of its own, never blocks a worker. 10s when left out."`
	URLBase       string          `json:"url_base,omitempty" doc:"What the URLs <prefix>/urls answers start with, before their paths: the scheme and host viewers reach fit's vod, hls and httpflv routes at, as https://media.example.com. The paths and their queries alone when left out."`
	URLPrefixes   []string        `json:"url_prefixes,omitempty" doc:"The path prefixes <prefix>/urls may sign URLs for: every path of a request must start with one of them, as /vod/films/, so that a URL cannot be signed for a resource outside them. Any path when left out."`

	urlBase       string
	prefix        string
	auth          *mediaauth.Policy
	ring          bool   // the policy has a keyring
	defaultKey    string // the keyring's first key, when ring
	secretFile    string // the single secret_file, when not ring
	prefixes      []string
	maxTTL        time.Duration
	maxTokens     int
	maxBody       int
	revokeTimeout time.Duration
	graph         module.Holder
	pool          *pool
}

// pathPrefix is what prefix may be: a path of plain segments.
var pathPrefix = regexp.MustCompile(`^(/[A-Za-z0-9._~-]+)+$`)

// pathChars is what url_prefixes may hold.
var pathChars = regexp.MustCompile(`^[A-Za-z0-9._~/-]+$`)

// Provision checks the params, finds the policy and starts the revoke pool.
func (t *Tokens) Provision(ctx *module.Context) error {
	prefix := t.Prefix
	if prefix == "" {
		prefix = DefaultPrefix
	}
	if !pathPrefix.MatchString(prefix) {
		return ctx.Errorf("prefix", "%q is not a path of segments of letters, digits, '.', '_', '~' and '-', as /media-tokens", t.Prefix)
	}
	for i, p := range t.Prefixes {
		if !config.ValidMediaPattern(p) {
			return ctx.Params.Field("prefixes").Index(i).Errorf("%q is not a stream pattern: 1 to 256 letters, digits, '.', '_', '~', '-', '/' and '*'", p)
		}
	}
	maxTTL := t.MaxTTL.Std()
	if maxTTL <= 0 {
		maxTTL = DefaultMaxTTL
	}
	maxTokens := t.MaxTokens
	if maxTokens == 0 {
		maxTokens = DefaultMaxTokens
	}
	if maxTokens < MinMaxTokens || maxTokens > MaxMaxTokens {
		return ctx.Errorf("max_tokens", "at most %d tokens a request: %d is not between %d and %d", MaxMaxTokens, maxTokens, MinMaxTokens, MaxMaxTokens)
	}
	maxBody := t.MaxBodyBytes
	if maxBody == 0 {
		maxBody = DefaultMaxBodyBytes
	}
	if maxBody < MinMaxBodyBytes || maxBody > MaxMaxBodyBytes {
		return ctx.Errorf("max_body_bytes", "a body is at least %d and at most %d bytes", MinMaxBodyBytes, MaxMaxBodyBytes)
	}
	revokeTimeout := t.RevokeTimeout.Std()
	if revokeTimeout <= 0 {
		revokeTimeout = DefaultRevokeTimeout
	}
	if t.URLBase != "" {
		u, err := url.Parse(t.URLBase)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
			u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(t.URLBase, "?#") {
			return ctx.Errorf("url_base", "%q is not http:// or https://, a host and maybe a path, as https://media.example.com", t.URLBase)
		}
		t.urlBase = strings.TrimRight(t.URLBase, "/")
	}
	for i, p := range t.URLPrefixes {
		if !strings.HasPrefix(p, "/") || len(p) > 256 || strings.Contains(p, "..") || !pathChars.MatchString(p) {
			return ctx.Params.Field("url_prefixes").Index(i).Errorf("%q is not a path prefix, as /vod/films/", p)
		}
	}
	p, err := mediaauth.Provision(ctx, "auth", t.Auth)
	if err != nil {
		return err
	}
	if p == nil {
		return ctx.Errorf("auth", "media_tokens needs a media_auth policy with secret_file, or a keyring of secret_files: auth names none")
	}
	t.prefix, t.auth = prefix, p
	t.ring = len(p.Keys()) > 0
	if t.ring {
		for _, key := range p.Keys() {
			if file, ok := p.KeyFile(key); !ok || file == "" {
				return ctx.Errorf("auth", "media_tokens needs a secret_file for every key of media_auth %q's keyring: key %q has none", t.Auth, key)
			}
		}
		t.defaultKey = p.DefaultKey()
	} else if t.secretFile = p.SecretFile(); t.secretFile == "" {
		return ctx.Errorf("auth", "media_tokens needs a media_auth policy with secret_file, or a keyring of secret_files: %q has neither", t.Auth)
	}
	t.prefixes = t.Prefixes
	t.maxTTL, t.maxTokens, t.maxBody, t.revokeTimeout = maxTTL, maxTokens, maxBody, revokeTimeout
	t.graph = ctx.Graph()
	t.pool = newPool()
	return nil
}

// NeedsAuthentication marks the handler as one whose route must
// authenticate its callers: it checks no caller credentials itself, and
// whoever reaches it may sign tokens for any stream its prefixes allow, or
// rotate the policy's secrets.
func (t *Tokens) NeedsAuthentication() {}

// Cleanup stops the revoke pool: its worker ends once the write it holds is
// done and the queue is empty.
func (t *Tokens) Cleanup() {
	if t.pool != nil {
		t.pool.close()
	}
}

// ServeHTTP serves the batch endpoint and the revoke endpoint below the
// prefix; a path outside it is 404.
func (t *Tokens) ServeHTTP(c *fibhttp.Context) {
	switch c.Request.URL.Path {
	case t.prefix:
		if c.Request.Method != stdhttp.MethodPost {
			plain(c, stdhttp.StatusMethodNotAllowed, "the batch endpoint takes POST", stdhttp.Header{"Allow": {"POST"}})
			return
		}
		t.issue(c)
	case t.prefix + "/revoke":
		if c.Request.Method != stdhttp.MethodPost {
			plain(c, stdhttp.StatusMethodNotAllowed, "the revoke endpoint takes POST", stdhttp.Header{"Allow": {"POST"}})
			return
		}
		t.revoke(c)
	case t.prefix + "/urls":
		if c.Request.Method != stdhttp.MethodPost {
			plain(c, stdhttp.StatusMethodNotAllowed, "the urls endpoint takes POST", stdhttp.Header{"Allow": {"POST"}})
			return
		}
		t.urls(c)
	default:
		plain(c, stdhttp.StatusNotFound, "not found", nil)
	}
}

// issueRequest is a batch's request: the key of the policy's keyring to sign
// with, if any, and the tokens to issue, in order.
type issueRequest struct {
	Key    string         `json:"key"`
	Tokens []tokenRequest `json:"tokens"`
}

// tokenRequest is one token of a batch, as a request asks for it.
type tokenRequest struct {
	Subject string   `json:"subject"`
	Publish []string `json:"publish"`
	Play    []string `json:"play"`
	TTL     string   `json:"ttl"`
}

// tokenAnswer is one token of a batch, as it is answered; key is the key of
// the policy's keyring that signed it, empty for a policy with one secret.
type tokenAnswer struct {
	Token   string `json:"token"`
	Subject string `json:"subject"`
	Expires int64  `json:"expires"`
	Key     string `json:"key,omitempty"`
}

// issueAnswer is a batch's answer.
type issueAnswer struct {
	Tokens []tokenAnswer `json:"tokens"`
}

// issue takes a batch's body, checks every token of it, and answers with the
// tokens, all or none: nothing is answered until every token compiles, so a
// batch with one bad token issues none.
func (t *Tokens) issue(c *fibhttp.Context) {
	readBody(c, t.maxBody, func(body []byte) { t.issueBody(c, body) })
}

func (t *Tokens) issueBody(c *fibhttp.Context, body []byte) {
	var in issueRequest
	if err := json.Unmarshal(body, &in); err != nil {
		plain(c, stdhttp.StatusBadRequest, `the body must be JSON: {"tokens": [...]}`, nil)
		return
	}
	switch {
	case len(in.Tokens) == 0:
		plain(c, stdhttp.StatusBadRequest, "the batch is empty: ask for at least one token", nil)
		return
	case len(in.Tokens) > t.maxTokens:
		plain(c, stdhttp.StatusBadRequest, fmt.Sprintf("the batch has %d tokens: at most %d a request", len(in.Tokens), t.maxTokens), nil)
		return
	}
	key := in.Key
	if key != "" {
		if !t.ring {
			plain(c, stdhttp.StatusBadRequest, "the policy has one key: leave key out of the request", nil)
			return
		}
		if _, ok := t.auth.KeyFile(key); !ok {
			plain(c, stdhttp.StatusBadRequest, fmt.Sprintf("no key named %q", key), nil)
			return
		}
	} else if t.ring {
		key = t.defaultKey
	}
	out := make([]tokenAnswer, 0, len(in.Tokens))
	for i := range in.Tokens {
		req := &in.Tokens[i]
		ttl, msg := t.check(req)
		if msg != "" {
			plain(c, stdhttp.StatusBadRequest, fmt.Sprintf("tokens[%d]: %s", i, msg), nil)
			return
		}
		grant := mediaauth.Grant{Publish: req.Publish, Play: req.Play}
		var tok string
		var expiry time.Time
		var err error
		if t.ring {
			tok, expiry, err = t.auth.IssueKey(key, req.Subject, grant, ttl)
		} else {
			tok, expiry, err = t.auth.Issue(req.Subject, grant, ttl)
		}
		if err != nil {
			if errors.Is(err, mediaauth.ErrUnavailable) {
				plain(c, stdhttp.StatusServiceUnavailable, "the media_auth policy is not loaded", stdhttp.Header{"Retry-After": {retryAfter}})
				return
			}
			plain(c, stdhttp.StatusBadRequest, fmt.Sprintf("tokens[%d]: %v", i, err), nil)
			return
		}
		out = append(out, tokenAnswer{Token: tok, Subject: req.Subject, Expires: expiry.Unix(), Key: key})
	}
	writeIssued(c, issueAnswer{Tokens: out})
}

// check validates one token of a batch, and returns the lifetime to give it:
// the request's ttl, or the policy's token_ttl when the request omits it,
// never more than max_ttl.
func (t *Tokens) check(req *tokenRequest) (time.Duration, string) {
	if req.Subject == "" || len(req.Subject) > maxSubjectBytes ||
		strings.ContainsFunc(req.Subject, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return 0, fmt.Sprintf("%q is not a subject: 1 to %d bytes with no control characters", req.Subject, maxSubjectBytes)
	}
	if len(req.Publish) == 0 && len(req.Play) == 0 {
		return 0, "a token needs at least one publish or play pattern"
	}
	for _, list := range [][]string{req.Publish, req.Play} {
		for _, p := range list {
			if !config.ValidMediaPattern(p) {
				return 0, fmt.Sprintf("%q is not a stream pattern: 1 to %d letters, digits, '.', '_', '~', '-', '/' and '*'", p, maxPatternBytes)
			}
			if !t.allowed(p) {
				return 0, fmt.Sprintf("%q starts with none of the prefixes this route may sign for: %s", p, strings.Join(t.prefixes, ", "))
			}
		}
	}
	if req.TTL == "" {
		return t.defaultTTL(), ""
	}
	d, err := time.ParseDuration(req.TTL)
	if err != nil || d <= 0 {
		return 0, fmt.Sprintf("%q is not a positive duration, as 2h", req.TTL)
	}
	if d > t.maxTTL {
		return 0, fmt.Sprintf("the lifetime %s is longer than max_ttl %s", d, t.maxTTL)
	}
	return d, ""
}

// defaultTTL is the lifetime a token gets when its request names none: the
// policy's token_ttl, or max_ttl when that is shorter.
func (t *Tokens) defaultTTL() time.Duration {
	ttl := t.auth.TTL()
	if ttl <= 0 || ttl > t.maxTTL {
		return t.maxTTL
	}
	return ttl
}

// allowed reports whether pattern starts with one of the configured
// prefixes; with none configured, any valid pattern is allowed.
func (t *Tokens) allowed(pattern string) bool {
	if len(t.prefixes) == 0 {
		return true
	}
	for _, p := range t.prefixes {
		if strings.HasPrefix(pattern, p) {
			return true
		}
	}
	return false
}

// revokeRequest is a revoke's body: one key of the policy's keyring, or all
// of them; a policy with a single secret takes neither.
type revokeRequest struct {
	Key string `json:"key"`
	All bool   `json:"all"`
}

// revokeTarget is one secret file a revoke rotates, and the key it belongs
// to, empty for a policy with a single secret.
type revokeTarget struct {
	key  string
	file string
}

// revoke rotates the policy's secret, or one key of its keyring, or every
// key: the tokens signed with what was rotated are refused at once, by every
// handler sharing the policy, and the tokens of the keys left alone stay
// good. The handler returns at once; the answer, 200, 503, 504 or 500, is
// written from the pool through the Context's Async.
//
// A 504 says the write did not finish within revoke_timeout; its outcome is
// unknown to the caller, and the writes and rotations still end if they can,
// so that the running policy and the files agree once they have.
func (t *Tokens) revoke(c *fibhttp.Context) {
	if c.Request.ContentLength == 0 {
		t.revokeTargets(c, revokeRequest{})
		return
	}
	readBody(c, t.maxBody, func(body []byte) {
		var in revokeRequest
		if err := json.Unmarshal(body, &in); err != nil {
			plain(c, stdhttp.StatusBadRequest, `the body must be JSON: {"key": "..."} or {"all": true}, or empty`, nil)
			return
		}
		t.revokeTargets(c, in)
	})
}

// revokeTargets works out what a revoke rotates and starts the one pool job
// that writes the new secrets and rotates the keys.
func (t *Tokens) revokeTargets(c *fibhttp.Context, in revokeRequest) {
	switch {
	case t.ring && in.Key == "" && !in.All:
		plain(c, stdhttp.StatusBadRequest, "name a key to rotate, or all: true", nil)
		return
	case !t.ring && in.Key != "":
		plain(c, stdhttp.StatusBadRequest, "the policy has one key: leave key out", nil)
		return
	}
	var targets []revokeTarget
	switch {
	case t.ring && in.All:
		for _, key := range t.auth.Keys() {
			file, ok := t.auth.KeyFile(key)
			if !ok || file == "" {
				plain(c, stdhttp.StatusInternalServerError, fmt.Sprintf("no secret file for key %q", key), nil)
				return
			}
			targets = append(targets, revokeTarget{key: key, file: file})
		}
	case t.ring:
		file, ok := t.auth.KeyFile(in.Key)
		if !ok {
			plain(c, stdhttp.StatusBadRequest, fmt.Sprintf("no key named %q", in.Key), nil)
			return
		}
		targets = append(targets, revokeTarget{key: in.Key, file: file})
	default:
		targets = append(targets, revokeTarget{file: t.secretFile})
	}
	if len(targets) == 0 {
		plain(c, stdhttp.StatusInternalServerError, "the revoke has no secret to rotate", nil)
		return
	}

	fail := func(status int, msg string) answer {
		return answer{status: status, body: []byte(msg + "\n"),
			header: stdhttp.Header{"Content-Type": {"text/plain; charset=utf-8"}}}
	}
	busy := func(msg string) answer {
		a := fail(stdhttp.StatusServiceUnavailable, msg)
		a.header.Set("Retry-After", retryAfter)
		return a
	}
	// The answer is written from the pool's worker, or from the timer that
	// bounds it, so the request is held until then and the graph with it.
	t.graph.Retain()
	finished := make(chan answer, 1)
	run := c.Async(func() {
		defer t.graph.Release()
		writeAnswer(c, <-finished)
	})
	var once sync.Once
	finish := func(a answer) {
		once.Do(func() {
			finished <- a
			run()
		})
	}
	timer := time.AfterFunc(t.revokeTimeout, func() {
		finish(fail(stdhttp.StatusGatewayTimeout, "the secret files' write did not finish in time"))
	})
	// Job writes each new secret, then rotates its key; the timer, if it got
	// there first, has answered 504 and the answer here is a no-op.
	job := func() {
		timer.Stop()
		rotated := make([]string, 0, len(targets))
		for _, target := range targets {
			secret := newSecret()
			if err := writeSecret(target.file, secret); err != nil {
				finish(fail(stdhttp.StatusInternalServerError, "the secret file cannot be written"))
				return
			}
			var err error
			if target.key == "" {
				err = t.auth.Rotate(secret)
			} else {
				err = t.auth.RotateKey(target.key, secret)
			}
			if err != nil {
				if errors.Is(err, mediaauth.ErrUnavailable) {
					finish(busy("the media_auth policy is not loaded"))
				} else {
					finish(fail(stdhttp.StatusInternalServerError, "the new secret cannot be used"))
				}
				return
			}
			if target.key != "" {
				rotated = append(rotated, target.key)
			}
		}
		body, _ := json.Marshal(struct {
			Revoked bool     `json:"revoked"`
			Since   int64    `json:"since"`
			Keys    []string `json:"keys,omitempty"`
		}{true, time.Now().Unix(), rotated})
		finish(answer{status: stdhttp.StatusOK, body: body,
			header: stdhttp.Header{"Content-Type": {"application/json"}}})
	}
	if !t.pool.submit(job) {
		timer.Stop()
		finish(busy("a revoke is already waiting: try again"))
	}
}

// answer is a response written from the revoke pool or its timer.
type answer struct {
	status int
	body   []byte
	header stdhttp.Header
}

// writeAnswer writes a's response; every answer has its header.
func writeAnswer(c *fibhttp.Context, a answer) {
	a.header.Set("Cache-Control", "no-store")
	_ = c.WriteResponse(fibhttp.Response{StatusCode: a.status, Header: a.header, Body: a.body})
}

// newSecret returns a new signing secret: 32 random bytes, raw URL base64,
// at least the 16 bytes a policy's secret needs. crypto/rand's Read never
// returns an error: it crashes the program rather than give no randomness.
func newSecret() string {
	var b [secretBytes]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// readBody gathers a request's body, up to limit bytes, and calls done with
// it; a larger one is answered 413.
func readBody(c *fibhttp.Context, limit int, done func([]byte)) {
	if c.Request.ContentLength > int64(limit) {
		plain(c, stdhttp.StatusRequestEntityTooLarge, "the body is too large", nil)
		return
	}
	var buf []byte
	finished := false
	c.OnBody(func(data []byte, fin bool, err error) {
		if finished {
			return
		}
		if err != nil {
			finished = true
			return
		}
		if len(buf)+len(data) > limit {
			finished = true
			plain(c, stdhttp.StatusRequestEntityTooLarge, "the body is too large", nil)
			return
		}
		buf = append(buf, data...)
		if fin {
			finished = true
			done(buf)
		}
	})
}

// plain answers with a status and a plain text message.
func plain(c *fibhttp.Context, status int, msg string, hdr stdhttp.Header) {
	if hdr == nil {
		hdr = stdhttp.Header{}
	}
	hdr.Set("Content-Type", "text/plain; charset=utf-8")
	hdr.Set("Cache-Control", "no-store")
	_ = c.WriteResponse(fibhttp.Response{StatusCode: status, Header: hdr, Body: []byte(msg + "\n")})
}

// writeIssued answers a batch with its tokens; strings and numbers always
// marshal.
func writeIssued(c *fibhttp.Context, a issueAnswer) {
	b, _ := json.Marshal(a)
	hdr := stdhttp.Header{"Content-Type": {"application/json"}, "Cache-Control": {"no-store"}}
	_ = c.WriteResponse(fibhttp.Response{StatusCode: stdhttp.StatusOK, Header: hdr, Body: b})
}

var _ module.Cleaner = (*Tokens)(nil)
