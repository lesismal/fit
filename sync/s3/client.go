// Package syncs3 is sync's S3 and its compatibles (MinIO, SeaweedFS, Ceph,
// R2 and the like): the sink s3, which writes batches of records as
// objects of lines, each named by its first record's commit time and
// position so that a batch written again is the same object, and the
// source s3, which reads the objects under a prefix in key order, as
// lines, resuming inside an object. Both sign their requests with AWS
// Signature Version 4, written here, and go out through fib's
// asynchronous HTTP client, on the role's engine or an engine of the
// package's own when the pipeline runs without one (fit sync run). See
// docs/decisions/036-sync.md.
package syncs3

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
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

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
)

// The defaults of the connection's params.
const (
	DefaultRegion  = "us-east-1"
	DefaultTimeout = 60 * time.Second
	// maxResponseBytes bounds an answer's body: a listing's page, an
	// error, a range of an object.
	maxResponseBytes = 80 << 20
)

// ConnParams are a connector's params for reaching its bucket. The sink and
// the source declare them as their own params, with the same names and
// docs, and hand them to NewConn.
type ConnParams struct {
	Endpoint, Region, Bucket, Addressing             string
	AccessKeyID, SecretAccessKeyEnv, SessionTokenEnv string
	CAFile, ServerName                               string
	Timeout                                          config.Duration
}

// Conn is a connection to a bucket: the client and the signer. It is safe
// for use by several goroutines.
type Conn struct {
	ConnParams

	base    *url.URL
	virtual bool
	signer  *Signer
	tls     *tls.Config
	engine  *fib.Engine

	mu      sync.Mutex
	client  *fibhttp.Client
	own     *fib.Engine
	ownDone chan struct{}
}

var (
	envName    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	bucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9.\-_]{1,62}$`)
	regionName = regexp.MustCompile(`^[A-Za-z0-9-]+$`)
)

// NewConn checks the params, reads the secrets and the CA file; nothing is
// sent until the first request.
func NewConn(ctx *module.Context, p ConnParams) (*Conn, config.Errors) {
	c := &Conn{ConnParams: p}
	var errs config.Errors
	add := func(field, format string, args ...any) {
		errs = append(errs, ctx.Params.Field(field).Errorf(format, args...))
	}
	if c.Region == "" {
		c.Region = DefaultRegion
	}
	if !regionName.MatchString(c.Region) {
		add("region", "%q is not a region's name", c.Region)
	}
	if !bucketName.MatchString(c.Bucket) {
		add("bucket", "%q is not a bucket's name: lower-case letters, digits, '.', '-' and '_', 3 to 63 of them", c.Bucket)
	}
	endpoint := c.Endpoint
	if endpoint == "" {
		endpoint = "https://s3." + c.Region + ".amazonaws.com"
	}
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || strings.Trim(u.Path, "/") != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		add("endpoint", "%q is not an http or https URL of a host, with no path, query or user", c.Endpoint)
	} else {
		u.Path = ""
		c.base = u
	}
	switch c.Addressing {
	case "":
		c.virtual = c.Endpoint == ""
	case "virtual":
		c.virtual = true
	}
	if c.virtual && strings.Contains(c.Bucket, ".") && u != nil && u.Scheme == "https" {
		add("addressing", "the bucket %q has a dot, which no certificate of the endpoint covers as a host's name: use addressing path", c.Bucket)
	}
	secret, token := "", ""
	readEnv := func(field, name string) string {
		if name == "" {
			return ""
		}
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
	secret = readEnv("secret_access_key_env", c.SecretAccessKeyEnv)
	token = readEnv("session_token_env", c.SessionTokenEnv)
	switch {
	case (c.AccessKeyID == "") != (c.SecretAccessKeyEnv == ""):
		add("access_key_id", "access_key_id and secret_access_key_env go together")
	case c.SessionTokenEnv != "" && c.AccessKeyID == "":
		add("session_token_env", "session_token_env needs access_key_id")
	case strings.ContainsAny(c.AccessKeyID, "/, \r\n"):
		add("access_key_id", "an access key's ID has no '/', ',', spaces or line breaks")
	}
	if c.AccessKeyID != "" {
		c.signer = &Signer{AccessKeyID: c.AccessKeyID, SecretAccessKey: secret, SessionToken: token, Region: c.Region, Service: "s3"}
	}
	tc := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: c.ServerName, NextProtos: []string{"http/1.1"}}
	if c.CAFile != "" {
		pem, err := os.ReadFile(c.CAFile)
		tc.RootCAs = x509.NewCertPool()
		if err != nil {
			add("ca_file", "%v", err)
		} else if !tc.RootCAs.AppendCertsFromPEM(pem) {
			add("ca_file", "%s holds no PEM certificate", c.CAFile)
		}
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
	cc.MaxResponseBodyBytes = maxResponseBytes
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

// objectURL returns the URL of key in the bucket (the bucket's own for an
// empty key), with the query q.
func (c *Conn) objectURL(key string, q url.Values) *url.URL {
	u := *c.base
	path := "/" + key
	if c.virtual {
		u.Host = c.Bucket + "." + u.Host
	} else {
		path = "/" + c.Bucket + path
	}
	if key == "" && !c.virtual {
		path = "/" + c.Bucket
	}
	u.Path, u.RawPath = path, Escape(path, false)
	if len(q) > 0 {
		u.RawQuery = EncodeQuery(q)
	}
	return &u
}

// Error is the server's answer to a request that failed: its HTTP status
// and S3's error code and message.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("S3 answered %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("S3 answered %d %s: %s", e.Status, e.Code, e.Message)
}

// response is an answer.
type response struct {
	status int
	header stdhttp.Header
	body   []byte
}

// do sends a request, signed, and waits for its answer. It returns an
// *Error for a status other than 2xx and 206. It waits, so it is called on
// a pipeline's goroutine, never a fib worker's.
func (c *Conn) do(ctx context.Context, method, key string, q url.Values, header stdhttp.Header, body []byte) (*response, error) {
	cl, err := c.httpClient()
	if err != nil {
		return nil, err
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	// The method is one of the package's own, so the request is made.
	req, _ := stdhttp.NewRequestWithContext(ctx, method, "http://x/", rd)
	req.URL = c.objectURL(key, q)
	req.Host = req.URL.Host
	for k, v := range header {
		req.Header[k] = v
	}
	if c.signer != nil {
		hash := EmptySHA256
		if body != nil {
			hash = PayloadHash(body)
		}
		c.signer.Sign(req, hash, time.Now())
	}
	type result struct {
		r   *response
		err error
	}
	done := make(chan result, 1)
	cl.Do(req, func(r *fibhttp.ClientResponse, e error) {
		if e != nil {
			done <- result{err: e}
			return
		}
		// The body is whole: the client does not stream answers.
		b, _ := r.BodyBytes()
		done <- result{r: &response{status: r.StatusCode, header: r.Header, body: bytes.Clone(b)}}
	})
	var res *response
	select {
	case d := <-done:
		if d.err != nil {
			return nil, fmt.Errorf("%s %s: %w", method, req.URL.Host, d.err)
		}
		res = d.r
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if res.status/100 == 2 {
		return res, nil
	}
	e := &Error{Status: res.status}
	var x struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	if xml.Unmarshal(res.body, &x) == nil {
		e.Code, e.Message = x.Code, x.Message
	} else {
		e.Message = clip(res.body)
	}
	return nil, e
}

// clip returns the start of an answer's body, for an error.
func clip(b []byte) string {
	const n = 300
	if len(b) > n {
		return string(b[:n]) + "..."
	}
	return string(b)
}

// Put writes an object, whole: S3 makes it visible only once all of it has
// arrived, and checks the body against its SHA-256 in the signature.
func (c *Conn) Put(ctx context.Context, key, contentType string, body []byte) error {
	h := stdhttp.Header{}
	if contentType != "" {
		h.Set("Content-Type", contentType)
	}
	if body == nil {
		body = []byte{}
	}
	_, err := c.do(ctx, stdhttp.MethodPut, key, nil, h, body)
	return err
}

// Part is a range of an object's bytes, and what the answer said of the
// object.
type Part struct {
	Data         []byte
	Size         int64 // the object's, -1 when the answer did not say
	ETag         string
	LastModified time.Time
}

// Get reads the bytes of key from off, at most n of them, and only while
// the object's ETag is etag, when it is set. A range past the object's end
// is no bytes.
func (c *Conn) Get(ctx context.Context, key, etag string, off, n int64) (Part, error) {
	h := stdhttp.Header{}
	h.Set("Range", fmt.Sprintf("bytes=%d-%d", off, off+n-1))
	if etag != "" {
		h.Set("If-Match", etag)
	}
	res, err := c.do(ctx, stdhttp.MethodGet, key, nil, h, nil)
	var e *Error
	if errors.As(err, &e) && e.Status == stdhttp.StatusRequestedRangeNotSatisfiable {
		return Part{Size: off, ETag: etag}, nil
	}
	if err != nil {
		return Part{}, err
	}
	p := Part{Data: res.body, Size: -1, ETag: res.header.Get("ETag")}
	p.LastModified, _ = stdhttp.ParseTime(res.header.Get("Last-Modified"))
	if cr := res.header.Get("Content-Range"); cr != "" {
		if i := strings.LastIndexByte(cr, '/'); i >= 0 {
			if size, err := strconv.ParseInt(cr[i+1:], 10, 64); err == nil {
				p.Size = size
			}
		}
	} else if res.status == stdhttp.StatusOK {
		// The whole object, from a server that ignores ranges.
		p.Size = int64(len(res.body))
		p.Data = res.body[min(off, p.Size):min(p.Size, off+n)]
	}
	return p, nil
}

// Object is an object as a listing has it.
type Object struct {
	Key          string    `xml:"Key"`
	Size         int64     `xml:"Size"`
	ETag         string    `xml:"ETag"`
	LastModified time.Time `xml:"LastModified"`
}

// List returns a page of the objects under prefix whose keys are past
// after, in key order, at most max of them, and the token of the next page,
// empty at the last.
func (c *Conn) List(ctx context.Context, prefix, after, token string, max int) ([]Object, string, error) {
	q := url.Values{"list-type": {"2"}, "max-keys": {strconv.Itoa(max)}}
	if prefix != "" {
		q.Set("prefix", prefix)
	}
	if token != "" {
		q.Set("continuation-token", token)
	} else if after != "" {
		q.Set("start-after", after)
	}
	res, err := c.do(ctx, stdhttp.MethodGet, "", q, nil, nil)
	if err != nil {
		return nil, "", err
	}
	var out struct {
		Contents              []Object `xml:"Contents"`
		IsTruncated           bool     `xml:"IsTruncated"`
		NextContinuationToken string   `xml:"NextContinuationToken"`
	}
	if err := xml.Unmarshal(res.body, &out); err != nil {
		return nil, "", fmt.Errorf("the listing: %v: %s", err, clip(res.body))
	}
	if !out.IsTruncated {
		out.NextContinuationToken = ""
	} else if out.NextContinuationToken == "" {
		return nil, "", errors.New("the listing is truncated with no continuation token")
	}
	return out.Contents, out.NextContinuationToken, nil
}
