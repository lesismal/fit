// Package syncelastic is sync's Elasticsearch and OpenSearch: the sink
// elasticsearch, which writes records with the _bulk API, and the client it
// shares with the source (I169). Both go out through fib's asynchronous
// HTTP client, on the role's engine, or on an engine of the package's own
// when the pipeline runs without one (fit sync run). See
// docs/decisions/036-sync.md.
package syncelastic

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	stdhttp "net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lesismal/fib"
	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
)

// The defaults of the connection's params.
const (
	DefaultTimeout          = 60 * time.Second
	DefaultMaxResponseBytes = 64 << 20
)

// ConnParams are a connector's params for reaching the cluster: its URLs,
// credentials and TLS. The sink and the source (I169) declare them as their
// own params, with the same names and docs, and hand them to NewConn.
type ConnParams struct {
	URLs                                  []string
	Username, PasswordEnv, APIKeyEnv      string
	CAFile, CertFile, KeyFile, ServerName string
	Timeout                               config.Duration
}

// Conn is a connection to the cluster: the client, the URL in use and what
// the cluster is. It is safe for use by several goroutines.
type Conn struct {
	ConnParams

	urls   []*url.URL
	auth   string
	tls    *tls.Config
	engine *fib.Engine

	mu      sync.Mutex
	client  *fibhttp.Client
	own     *fib.Engine
	ownDone chan struct{}
	info    *Info
	cur     atomic.Int64
}

// Info is what the cluster's root endpoint says of it.
type Info struct {
	// Distribution is elasticsearch or opensearch.
	Distribution string
	// Version is the cluster's, as 8.15.0.
	Version string
}

// OpenSearch reports whether the cluster is OpenSearch.
func (i *Info) OpenSearch() bool { return i.Distribution == "opensearch" }

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// NewConn checks the params, reads the secrets and the TLS files; nothing
// is sent until the first request.
func NewConn(ctx *module.Context, p ConnParams) (*Conn, config.Errors) {
	c := &Conn{ConnParams: p}
	var errs config.Errors
	add := func(field, format string, args ...any) {
		errs = append(errs, ctx.Params.Field(field).Errorf(format, args...))
	}
	if len(c.URLs) == 0 {
		add("urls", "no URLs")
	}
	for i, s := range c.URLs {
		u, err := url.Parse(s)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			errs = append(errs, ctx.Params.Field("urls").Index(i).Errorf("%q is not an http or https URL of a host, with no query or user", s))
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
	switch {
	case c.APIKeyEnv != "" && (c.Username != "" || c.PasswordEnv != ""):
		add("api_key_env", "an API key or a username, not both")
	case c.APIKeyEnv != "":
		c.auth = "ApiKey " + secret("api_key_env", c.APIKeyEnv)
	case c.Username != "" && c.PasswordEnv != "":
		c.auth = "Basic " + base64.StdEncoding.EncodeToString([]byte(c.Username+":"+secret("password_env", c.PasswordEnv)))
	case c.Username != "" || c.PasswordEnv != "":
		add("username", "username and password_env go together")
	}
	if strings.ContainsAny(c.Username, ":\r\n") {
		add("username", "a username has no ':' or line breaks")
	}
	tc := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: c.ServerName}
	if c.CAFile != "" {
		pem, err := os.ReadFile(c.CAFile)
		tc.RootCAs = x509.NewCertPool()
		if err != nil {
			add("ca_file", "%v", err)
		} else if !tc.RootCAs.AppendCertsFromPEM(pem) {
			add("ca_file", "%s holds no PEM certificate", c.CAFile)
		}
	}
	if (c.CertFile == "") != (c.KeyFile == "") {
		add("cert_file", "cert_file and key_file go together")
	} else if c.CertFile != "" {
		cert, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
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
	if c.Timeout <= 0 {
		return DefaultTimeout
	}
	return c.Timeout.Std()
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
		own, err := fib.New(cfg)
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

// Response is a request's answer.
type Response struct {
	Status int
	Header stdhttp.Header
	Body   []byte
}

// errUnavailable is a node's answer that sends the request to the next.
func unavailable(status int) bool {
	return status == stdhttp.StatusBadGateway || status == stdhttp.StatusServiceUnavailable || status == stdhttp.StatusGatewayTimeout
}

// Do sends a request to the cluster: to the URL that answered last, then to
// each other in turn while one fails to answer (a connection's error, a
// timeout, 502, 503 or 504). It returns the first answer, or the last
// error, or the last of those statuses when every URL gave one. It waits
// for the answer, so it is called on a pipeline's goroutine, never a fib
// worker's.
func (c *Conn) Do(ctx context.Context, method, path, query string, body []byte, contentType string) (*Response, error) {
	cl, err := c.httpClient()
	if err != nil {
		return nil, err
	}
	var last *Response
	var lastErr error
	start := int(c.cur.Load())
	for i := range c.urls {
		n := (start + i) % len(c.urls)
		resp, err := c.send(ctx, cl, c.urls[n], method, path, query, body, contentType)
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

func (c *Conn) send(ctx context.Context, cl *fibhttp.Client, base *url.URL, method, path, query string, body []byte, contentType string) (*Response, error) {
	u := *base
	u.Path += path
	u.RawQuery = query
	req, err := stdhttp.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if body == nil {
		req.Body, req.ContentLength = nil, 0
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json")
	if c.auth != "" {
		req.Header.Set("Authorization", c.auth)
	}
	type result struct {
		resp *Response
		err  error
	}
	done := make(chan result, 1)
	cl.Do(req, func(r *fibhttp.ClientResponse, e error) {
		if e != nil {
			done <- result{err: e}
			return
		}
		b, ok := r.BodyBytes()
		if !ok {
			done <- result{err: errors.New("a streamed body")}
			return
		}
		done <- result{resp: &Response{Status: r.StatusCode, Header: r.Header, Body: bytes.Clone(b)}}
	})
	select {
	case res := <-done:
		return res.resp, res.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Info asks the cluster what it is, once; it is asked again after a
// failure.
func (c *Conn) Info(ctx context.Context) (*Info, error) {
	c.mu.Lock()
	info := c.info
	c.mu.Unlock()
	if info != nil {
		return info, nil
	}
	resp, err := c.Do(ctx, stdhttp.MethodGet, "/", "", nil, "")
	if err != nil {
		return nil, err
	}
	if resp.Status != stdhttp.StatusOK {
		hint := ""
		if resp.Status == stdhttp.StatusUnauthorized && strings.HasPrefix(c.auth, "ApiKey ") {
			hint = " (an OpenSearch cluster, which has no API keys, answers so to one)"
		}
		return nil, fmt.Errorf("the root endpoint answered %d%s: %s", resp.Status, hint, clip(resp.Body))
	}
	var root struct {
		Version struct {
			Number       string `json:"number"`
			Distribution string `json:"distribution"`
		} `json:"version"`
	}
	if err := json.Unmarshal(resp.Body, &root); err != nil || root.Version.Number == "" {
		return nil, fmt.Errorf("the root endpoint's answer is not Elasticsearch's or OpenSearch's: %s", clip(resp.Body))
	}
	info = &Info{Distribution: "elasticsearch", Version: root.Version.Number}
	if root.Version.Distribution == "opensearch" {
		info.Distribution = "opensearch"
	}
	if info.OpenSearch() && strings.HasPrefix(c.auth, "ApiKey ") {
		return nil, errors.New("OpenSearch has no API keys: use username and password_env")
	}
	c.mu.Lock()
	c.info = info
	c.mu.Unlock()
	return info, nil
}

// clip returns the start of an answer's body, for an error.
func clip(b []byte) string {
	const n = 300
	if len(b) > n {
		return string(b[:n]) + "..."
	}
	return string(b)
}
