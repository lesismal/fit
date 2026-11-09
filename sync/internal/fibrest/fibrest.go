// Package fibrest is what sync's connectors to HTTP APIs share (the
// influxdb, tdengine, qdrant and milvus sinks, the embed transform): a
// client on fib's asynchronous HTTP client, on the pipeline's engine or on
// one of its own when the pipeline runs without one, waited on from the
// pipeline's goroutine, never a fib worker's; TLS and secrets read as the
// other connectors read them; tries again on 408, 429 and 5xx with a
// backoff; and the search for the record of a batch a server refused.
package fibrest

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	stdhttp "net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lesismal/fib"
	fibhttp "github.com/lesismal/fib/http"

	fitsync "github.com/lesismal/fit/sync"
)

// The defaults of a client.
const (
	DefaultTimeout          = 30 * time.Second
	DefaultMaxResponseBytes = 16 << 20
	DefaultAttempts         = 5
)

// The backoff between tries: doubling from the first to the most. Tests
// shorten them.
var (
	BackoffFirst = 200 * time.Millisecond
	BackoffMost  = 30 * time.Second
)

// Params are a client's.
type Params struct {
	// URL is the server's base URL, http or https, a path prefix allowed,
	// no query, fragment or user.
	URL string
	// CAFile, CertFile, KeyFile and ServerName are TLS's, as the other
	// connectors take them.
	CAFile, CertFile, KeyFile, ServerName string
	// PinnedCert, when set, is the DER of the one certificate the server
	// may present, checked in place of a chain: llm.proxy's own.
	PinnedCert []byte
	// Timeout bounds one request, from connecting to the end of its
	// answer: DefaultTimeout when 0.
	Timeout time.Duration
	// MaxResponseBytes bounds an answer's body: DefaultMaxResponseBytes
	// when 0.
	MaxResponseBytes int
	// Header is sent with every request: the credentials.
	Header stdhttp.Header
}

// FieldError is a param's error, by the param's name.
type FieldError struct {
	Field string
	Err   error
}

func (e FieldError) Error() string { return e.Field + ": " + e.Err.Error() }

// Client sends requests to one server. It is safe for use by several
// goroutines.
type Client struct {
	base    *url.URL
	tls     *tls.Config
	timeout time.Duration
	maxBody int
	header  stdhttp.Header
	engine  *fib.Engine

	mu      sync.Mutex
	client  *fibhttp.Client
	own     *fib.Engine
	ownDone chan struct{}
}

// New checks p and reads its TLS files; nothing is sent until the first
// request. The errors name the params: url, ca_file, cert_file.
func New(engine *fib.Engine, p Params) (*Client, []FieldError) {
	c := &Client{engine: engine, timeout: p.Timeout, maxBody: p.MaxResponseBytes, header: p.Header}
	if c.timeout <= 0 {
		c.timeout = DefaultTimeout
	}
	if c.maxBody <= 0 {
		c.maxBody = DefaultMaxResponseBytes
	}
	if c.header == nil {
		c.header = stdhttp.Header{}
	}
	var errs []FieldError
	add := func(field, format string, args ...any) {
		errs = append(errs, FieldError{field, fmt.Errorf(format, args...)})
	}
	u, err := url.Parse(p.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		add("url", "%q is not an http or https URL of a host, with no query, fragment or user", p.URL)
	} else {
		u.Path = strings.TrimSuffix(u.Path, "/")
		c.base = u
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
	if pin := p.PinnedCert; pin != nil {
		// The pin is the check: the certificate presented must be it.
		tc.InsecureSkipVerify = true
		tc.VerifyPeerCertificate = func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 || !bytes.Equal(raw[0], pin) {
				return errors.New("the server's certificate is not the one pinned")
			}
			return nil
		}
	}
	c.tls = tc
	return c, errs
}

// Base returns the base URL.
func (c *Client) Base() *url.URL { return c.base }

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Secret reads the environment variable name, for the param field: an
// error when the name is not one or the variable is unset or empty, or
// holds a line break or NUL.
func Secret(field, name string) (string, *FieldError) {
	if !envName.MatchString(name) {
		return "", &FieldError{field, fmt.Errorf("%q is not an environment variable's name", name)}
	}
	v := os.Getenv(name)
	if v == "" {
		return "", &FieldError{field, fmt.Errorf("the environment variable %s is not set, or empty", name)}
	}
	if strings.ContainsAny(v, "\r\n\x00") {
		return "", &FieldError{field, fmt.Errorf("the environment variable %s holds a line break or NUL", name)}
	}
	return v, nil
}

// httpClient returns the client, made at the first request, on an engine of
// its own when there is none.
func (c *Client) httpClient() (*fibhttp.Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil {
		return c.client, nil
	}
	e := c.engine
	if e == nil {
		cfg := fib.DefaultConfig()
		cfg.LogSettings = false
		own, err := fib.New(cfg)
		if err != nil {
			return nil, err
		}
		c.own, c.ownDone, e = own, make(chan struct{}), own
		go func() { _ = own.Run(); close(c.ownDone) }()
	}
	cc := fibhttp.DefaultClientConfig()
	cc.Timeout, cc.ResponseHeaderTimeout, cc.DialTimeout = c.timeout, c.timeout, min(c.timeout, 10*time.Second)
	cc.MaxConnsPerHost, cc.MaxIdleConnsPerHost = 16, 16
	cc.IdleConnTimeout = 90 * time.Second
	cc.MaxResponseBodyBytes = int64(c.maxBody)
	cc.TLSConfig = c.tls
	c.client = fibhttp.NewClient(e, cc)
	return c.client, nil
}

// Close closes the client, and stops the engine of its own.
func (c *Client) Close() {
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

// Request is a request to the server.
type Request struct {
	Method string
	// Path is put after the base URL's; Query is the raw query.
	Path, Query string
	// Header is sent beside the client's.
	Header stdhttp.Header
	Body   []byte
	// Retry, when not nil, says whether DoRetry sends the request again
	// after an answer, in place of Transient's say on its status.
	Retry func(*Response) bool
}

// Response is a request's answer.
type Response struct {
	Status int
	Header stdhttp.Header
	Body   []byte
}

// Do sends r once and waits for its answer, on the caller's goroutine,
// which must not be a fib worker's.
func (c *Client) Do(ctx context.Context, r Request) (*Response, error) {
	cl, err := c.httpClient()
	if err != nil {
		return nil, err
	}
	u := *c.base
	u.Path += r.Path
	u.RawQuery = r.Query
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
	type result struct {
		resp *Response
		err  error
	}
	done := make(chan result, 1)
	cl.Do(req, func(resp *fibhttp.ClientResponse, e error) {
		if e != nil {
			done <- result{err: e}
			return
		}
		b, _ := resp.BodyBytes()
		done <- result{resp: &Response{Status: resp.StatusCode, Header: resp.Header, Body: bytes.Clone(b)}}
	})
	select {
	case res := <-done:
		if res.err != nil {
			return nil, fmt.Errorf("%s: %w", c.base.Host, res.err)
		}
		return res.resp, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Transient reports whether an answer of status may pass when the request
// is sent again: 408, 429 and 5xx.
func Transient(status int) bool {
	return status == stdhttp.StatusRequestTimeout || status == stdhttp.StatusTooManyRequests || status >= 500
}

// DoRetry sends r, and again, up to attempts times in all (DefaultAttempts
// when 0), while it fails to be answered or is answered with a Transient
// status (or one r.Retry says to send again after), waiting a backoff between tries, or what a 429's or 503's
// Retry-After asks, up to BackoffMost. It returns the last answer, whatever
// its status, or the last error when none came.
func (c *Client) DoRetry(ctx context.Context, r Request, attempts int) (*Response, error) {
	if attempts <= 0 {
		attempts = DefaultAttempts
	}
	for round := 0; ; round++ {
		resp, err := c.Do(ctx, r)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		again := err != nil
		if err == nil {
			again = Transient(resp.Status)
			if r.Retry != nil {
				again = r.Retry(resp)
			}
		}
		if !again || round+1 >= attempts {
			return resp, err
		}
		wait := Backoff(round)
		if err == nil {
			if d, ok := retryAfter(resp.Header.Get("Retry-After")); ok {
				wait = min(d, BackoffMost)
			}
		}
		if !Sleep(ctx, wait) {
			return nil, ctx.Err()
		}
	}
}

// retryAfter reads a Retry-After of seconds.
func retryAfter(v string) (time.Duration, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 {
		return 0, false
	}
	return time.Duration(n) * time.Second, true
}

// Backoff is the wait before round's next try.
func Backoff(round int) time.Duration {
	return min(BackoffFirst<<min(round, 20), BackoffMost)
}

// Sleep waits d, or until ctx ends: false then.
func Sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// Clip returns the start of an answer's body, for an error.
func Clip(b []byte) string {
	const n = 300
	if len(b) > n {
		return string(b[:n]) + "..."
	}
	return string(b)
}

// StatusError is an answer a request did not want.
func StatusError(what string, resp *Response) error {
	return fmt.Errorf("%s answered %d: %s", what, resp.Status, Clip(resp.Body))
}

// Refused marks err as the server's refusal of what a request carried, as
// a point it cannot parse or a vector of the wrong length: sent alone, a
// record refused so is refused for good. See WriteFinding.
func Refused(err error) error { return &refusal{err} }

type refusal struct{ err error }

func (e *refusal) Error() string { return e.err.Error() }
func (e *refusal) Unwrap() error { return e.err }

// IsRefused reports whether err was marked by Refused.
func IsRefused(err error) bool {
	var r *refusal
	return errors.As(err, &r)
}

// WriteFinding writes records 0 to n of a batch with write, which writes
// those from lo to before hi in one or more requests, idempotently. When it
// fails with a refusal (Refused) of several records, the server not saying
// which of them it refused, each is written again alone, in order, to find
// it: the first refused fails as a Permanent BatchError at its index, those
// before it written; when none is refused alone (a server refusing their
// sum, not one of them), they are written. Any other error is write's, for
// the pipeline's retry.
func WriteFinding(ctx context.Context, n int, write func(ctx context.Context, lo, hi int) error) error {
	if n == 0 {
		return nil
	}
	err := write(ctx, 0, n)
	if err == nil || !IsRefused(err) {
		return err
	}
	if n == 1 {
		return &fitsync.BatchError{Index: 0, Err: fitsync.Permanent(err)}
	}
	for i := range n {
		if err := write(ctx, i, i+1); err != nil {
			if IsRefused(err) {
				err = fitsync.Permanent(err)
			}
			return &fitsync.BatchError{Index: i, Err: err}
		}
	}
	return nil
}
