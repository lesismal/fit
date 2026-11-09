// Package httpconn is the HTTP connection the second round's HTTP sinks
// share (doris, loki, otlp_logs): fib's asynchronous client, on the role's
// engine or an engine of its own when the pipeline runs without one (fit
// sync run), several URLs tried in turn, basic or bearer authentication
// from the environment, TLS against a CA of the config's, redirects
// followed when asked, and requests sent again with a backoff while the
// server answers 429 or 5xx. A call waits for its answer, so it is made on
// a pipeline's goroutine, never a fib worker's.
package httpconn

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	stdhttp "net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lesismal/fib"
	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
)

// The defaults.
const (
	DefaultTimeout          = 60 * time.Second
	DefaultMaxResponseBytes = 16 << 20
	// DefaultAttempts is how many times DoRetry sends a request the server
	// answers 429 or 5xx, or that fails to reach it, before it gives up.
	DefaultAttempts = 6
	// MaxRedirects is the most redirects a request follows.
	MaxRedirects = 3
)

// The backoff between attempts: doubling from the first to the most, or
// what a Retry-After asks for, up to the most.
var (
	BackoffFirst = 200 * time.Millisecond
	BackoffMost  = 10 * time.Second
)

// Params are a connector's params for reaching its server, declared by the
// connector as its own and handed to New.
type Params struct {
	URLs                                  []string
	Username, PasswordEnv, BearerTokenEnv string
	CAFile, CertFile, KeyFile, ServerName string
	Timeout                               config.Duration
	// Header is sent with every request.
	Header map[string]string
	// AllowPath lets a URL have a path, which requests' paths go after.
	AllowPath bool
}

// Conn is a connection to the server. It is safe for use by several
// goroutines.
type Conn struct {
	p      Params
	urls   []*url.URL
	auth   string
	header stdhttp.Header
	tls    *tls.Config
	engine *fib.Engine

	mu      sync.Mutex
	client  *fibhttp.Client
	own     *fib.Engine
	ownDone chan struct{}
	cur     atomic.Int64
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// New checks the params and reads the secrets and TLS files; nothing is
// sent until the first request.
func New(ctx *module.Context, p Params) (*Conn, config.Errors) {
	c := &Conn{p: p, header: stdhttp.Header{}}
	var errs config.Errors
	add := func(field, format string, args ...any) {
		errs = append(errs, ctx.Params.Field(field).Errorf(format, args...))
	}
	if len(p.URLs) == 0 {
		add("urls", "no URLs")
	}
	for i, s := range p.URLs {
		u, err := url.Parse(s)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil ||
			!p.AllowPath && strings.Trim(u.Path, "/") != "" {
			what := "with no path, query or user"
			if p.AllowPath {
				what = "with no query or user"
			}
			errs = append(errs, ctx.Params.Field("urls").Index(i).Errorf("%q is not an http or https URL of a host, %s", s, what))
			continue
		}
		u.Path = strings.TrimSuffix(u.Path, "/")
		c.urls = append(c.urls, u)
	}
	secret := func(field, name string) string {
		if !envName.MatchString(name) {
			add(field, "%q is not an environment variable's name", name)
			return ""
		}
		v := os.Getenv(name)
		if v == "" {
			add(field, "the environment variable %s is not set, or empty", name)
		}
		return v
	}
	if strings.ContainsAny(p.Username, ":\r\n") {
		add("username", "a username has no ':' or line breaks")
	}
	switch {
	case p.BearerTokenEnv != "" && (p.Username != "" || p.PasswordEnv != ""):
		add("bearer_token_env", "a bearer token or a username, not both")
	case p.BearerTokenEnv != "":
		c.auth = "Bearer " + secret("bearer_token_env", p.BearerTokenEnv)
	case p.PasswordEnv != "" && p.Username == "":
		add("password_env", "password_env needs username")
	case p.Username != "":
		password := ""
		if p.PasswordEnv != "" {
			password = secret("password_env", p.PasswordEnv)
		}
		c.auth = "Basic " + base64.StdEncoding.EncodeToString([]byte(p.Username+":"+password))
	}
	for k, v := range p.Header {
		if k == "" || strings.ContainsAny(k, " \t\r\n:") || strings.ContainsAny(v, "\r\n") {
			add("headers", "%q: %q is not a header field", k, v)
			continue
		}
		if strings.EqualFold(k, "Authorization") || strings.EqualFold(k, "Content-Type") || strings.EqualFold(k, "Content-Length") || strings.EqualFold(k, "Host") {
			add("headers", "%s is the connector's to set", k)
			continue
		}
		c.header.Set(k, v)
	}
	tc := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: p.ServerName}
	if p.CAFile != "" {
		pem, err := os.ReadFile(p.CAFile)
		tc.RootCAs = x509.NewCertPool()
		if err != nil {
			add("ca_file", "%v", err)
		} else if !tc.RootCAs.AppendCertsFromPEM(pem) {
			add("ca_file", "%s holds no PEM certificate", p.CAFile)
		}
	}
	if (p.CertFile == "") != (p.KeyFile == "") {
		add("cert_file", "cert_file and key_file go together")
	} else if p.CertFile != "" {
		cert, err := tls.LoadX509KeyPair(p.CertFile, p.KeyFile)
		if err != nil {
			add("cert_file", "%v", err)
		}
		tc.Certificates = []tls.Certificate{cert}
	}
	c.tls = tc
	c.engine = ctx.Engine()
	return c, errs
}

func (c *Conn) timeout() time.Duration {
	if c.p.Timeout <= 0 {
		return DefaultTimeout
	}
	return c.p.Timeout.Std()
}

// httpClient returns the client, made at the first request, on an engine of
// its own when the pipeline has none.
func (c *Conn) httpClient() (*fibhttp.Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil {
		return c.client, nil
	}
	e := c.engine
	if e == nil {
		cfg := fib.DefaultConfig()
		cfg.LogSettings = false
		own, err := newEngine(cfg)
		if err != nil {
			return nil, err
		}
		c.own, c.ownDone, e = own, make(chan struct{}), own
		go func() { _ = own.Run(); close(c.ownDone) }()
	}
	cc := fibhttp.DefaultClientConfig()
	t := c.timeout()
	cc.Timeout, cc.ResponseHeaderTimeout, cc.DialTimeout = t, t, min(t, 10*time.Second)
	cc.MaxConnsPerHost, cc.MaxIdleConnsPerHost = 16, 16
	cc.IdleConnTimeout = 90 * time.Second
	cc.MaxResponseBodyBytes = DefaultMaxResponseBytes
	cc.TLSConfig = c.tls
	c.client = fibhttp.NewClient(e, cc)
	return c.client, nil
}

// newEngine makes the engine of the package's own; the tests replace it.
var newEngine = fib.New

// Close closes the client, and stops the engine of its own.
func (c *Conn) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil {
		c.client.Close()
		c.client = nil
	}
	if c.own != nil {
		c.own.Stop()
		<-c.ownDone
		_ = c.own.Close()
		c.own = nil
	}
}

// Request is a request to the server: the path goes after a URL's.
type Request struct {
	Method, Path, Query string
	Header              stdhttp.Header
	Body                []byte
	// Redirects is how many redirects (301, 302, 303, 307, 308) to follow,
	// at most MaxRedirects, each sent again whole, with the authorization,
	// to wherever the Location names; 0 returns the redirect.
	Redirects int
}

// Response is a request's answer.
type Response struct {
	Status int
	Header stdhttp.Header
	Body   []byte
	// Host is the host that answered, after any redirect.
	Host string
}

// unavailable is a status that sends the request to the next URL.
func unavailable(status int) bool {
	return status == stdhttp.StatusBadGateway || status == stdhttp.StatusServiceUnavailable || status == stdhttp.StatusGatewayTimeout
}

// Retryable reports whether a status is one to send the request again on:
// 408, 429 or a 5xx.
func Retryable(status int) bool {
	return status == stdhttp.StatusTooManyRequests || status == stdhttp.StatusRequestTimeout || status >= 500
}

// Do sends a request: to the URL that answered last, then to each other in
// turn while one fails to answer (a connection's error, a timeout, 502, 503
// or 504). It returns the first answer, the last of those statuses when
// every URL gave one, or the last error.
func (c *Conn) Do(ctx context.Context, r *Request) (*Response, error) {
	cl, err := c.httpClient()
	if err != nil {
		return nil, err
	}
	var last *Response
	var lastErr error
	start := int(c.cur.Load())
	for i := range c.urls {
		n := (start + i) % len(c.urls)
		u := *c.urls[n]
		u.Path += r.Path
		u.RawQuery = r.Query
		resp, err := c.follow(ctx, cl, &u, r)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err == nil && !unavailable(resp.Status) {
			c.cur.Store(int64(n))
			return resp, nil
		}
		if err != nil {
			lastErr, last = fmt.Errorf("%s: %w", c.urls[n].Host, err), nil
		} else {
			last, lastErr = resp, nil
		}
	}
	c.cur.Store(int64((start + 1) % len(c.urls)))
	if last != nil {
		return last, nil
	}
	return nil, lastErr
}

// follow sends r to u, and to each redirect's Location while r allows.
func (c *Conn) follow(ctx context.Context, cl *fibhttp.Client, u *url.URL, r *Request) (*Response, error) {
	for hop := 0; ; hop++ {
		resp, err := c.send(ctx, cl, u, r)
		if err != nil {
			return nil, err
		}
		resp.Host = u.Host
		switch resp.Status {
		case stdhttp.StatusMovedPermanently, stdhttp.StatusFound, stdhttp.StatusSeeOther, stdhttp.StatusTemporaryRedirect, stdhttp.StatusPermanentRedirect:
		default:
			return resp, nil
		}
		if hop >= min(r.Redirects, MaxRedirects) {
			return resp, nil
		}
		loc := resp.Header.Get("Location")
		next, err := u.Parse(loc)
		if err != nil || (next.Scheme != "http" && next.Scheme != "https") || next.Host == "" {
			return nil, fmt.Errorf("a redirect to %q, not an http or https URL", loc)
		}
		u = next
	}
}

func (c *Conn) send(ctx context.Context, cl *fibhttp.Client, u *url.URL, r *Request) (*Response, error) {
	req, err := stdhttp.NewRequestWithContext(ctx, r.Method, u.String(), bytes.NewReader(r.Body))
	if err != nil {
		return nil, err
	}
	if r.Body == nil {
		req.Body, req.ContentLength = nil, 0
	}
	for k, v := range c.header {
		req.Header[k] = v
	}
	for k, v := range r.Header {
		req.Header[k] = v
	}
	if c.auth != "" {
		req.Header.Set("Authorization", c.auth)
	}
	type result struct {
		resp *Response
		err  error
	}
	done := make(chan result, 1)
	cl.Do(req, func(res *fibhttp.ClientResponse, e error) {
		if e != nil {
			done <- result{err: e}
			return
		}
		b, ok := res.BodyBytes()
		if !ok {
			done <- result{err: errors.New("a streamed body")}
			return
		}
		done <- result{resp: &Response{Status: res.StatusCode, Header: res.Header, Body: bytes.Clone(b)}}
	})
	select {
	case res := <-done:
		return res.resp, res.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// DoRetry is Do, sent again after a backoff while it fails to reach the
// server or the server answers a Retryable status, attempts times in all
// (DefaultAttempts when 0 or less). It returns the last answer, or the
// last error; ctx's once it ends.
func (c *Conn) DoRetry(ctx context.Context, r *Request, attempts int) (*Response, error) {
	if attempts <= 0 {
		attempts = DefaultAttempts
	}
	wait := BackoffFirst
	for n := 1; ; n++ {
		resp, err := c.Do(ctx, r)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err == nil && !Retryable(resp.Status) || n >= attempts {
			return resp, err
		}
		d := wait
		if err == nil {
			if s, perr := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); perr == nil && s >= 0 {
				d = time.Duration(s) * time.Second
			}
		}
		d = min(d, BackoffMost)
		t := time.NewTimer(d)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		}
		wait = min(wait*2, BackoffMost)
	}
}

// StatusError is an answer of a status other than the one wanted.
type StatusError struct {
	Status int
	Body   string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("the server answered %d: %s", e.Status, Clip([]byte(e.Body)))
}

// Clip returns the start of an answer's body, for an error.
func Clip(b []byte) string {
	const n = 500
	b = bytes.TrimSpace(b)
	if len(b) > n {
		return string(b[:n]) + "..."
	}
	return string(b)
}
