package httpsync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	stdhttp "net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/lesismal/fib"
	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
)

func init() {
	module.Register(module.Info{ID: fitsync.Sinks + ".http", New: func() any { return new(Sink) },
		Doc: "Posts batches of records to an HTTP endpoint, as NDJSON or a JSON array (or a codec's format), through fib's asynchronous HTTP client, with the headers and authentication the config gives and an idempotency key made from the batch's records and their positions, the same each time the batch is sent again. A 5xx, 408, 429, a timeout or a connection's error is tried again by the pipeline's retry; any other answer but a 2xx fails the records for good, which on_error takes."})
}

// The defaults of the sink's params.
const (
	DefaultTimeout           = 30 * time.Second
	DefaultMaxRequestBytes   = 8 << 20
	DefaultIdempotencyHeader = "Idempotency-Key"
	maxResponseBytes         = 1 << 20
)

// Sink is the sink http.
type Sink struct {
	URL               string            `json:"url" fit:"required" doc:"The endpoint batches are sent to, http or https, as https://collector.example.com/ingest. fib's client does not follow redirects: a 3xx fails the records."`
	Method            string            `json:"method,omitempty" enum:"POST,PUT" default:"\"POST\"" doc:"The request's method. POST when left out."`
	Format            string            `json:"format,omitempty" enum:"ndjson,json" default:"\"ndjson\"" doc:"How a batch is written: ndjson, a line of JSON per record (application/x-ndjson); json, a JSON array of them (application/json). ndjson when left out; codec takes its place."`
	Form              string            `json:"form,omitempty" enum:"plain,record" default:"\"plain\"" doc:"What each record's JSON is: plain, its row (after, or before when after is null) as a plain JSON object; record, the whole record in its canonical JSON, with its operation, key, namespace and position. plain when left out."`
	Codec             *config.ModuleRef `json:"codec,omitempty" module:"sync.codecs" doc:"A codec that writes the batch's records in place of format and form, the body being what it makes of them, sent with content_type."`
	ContentType       string            `json:"content_type,omitempty" doc:"The Content-Type of a codec's body. application/octet-stream when left out; format sets its own."`
	Headers           map[string]string `json:"headers,omitempty" doc:"Header fields every request carries, as an X-Tenant. Not Authorization, which username or bearer_token_env set, nor Content-Type or the idempotency header."`
	Username          string            `json:"username,omitempty" doc:"The user for basic authentication, with password_env."`
	PasswordEnv       string            `json:"password_env,omitempty" doc:"The environment variable holding username's password, read when the pipeline loads."`
	BearerTokenEnv    string            `json:"bearer_token_env,omitempty" doc:"The environment variable holding a token sent as Authorization: Bearer, read when the pipeline loads. Not with username."`
	IdempotencyHeader string            `json:"idempotency_header,omitempty" default:"\"Idempotency-Key\"" doc:"The header field the idempotency key goes in: 32 hex digits of a SHA-256 of the request's records' origins (or positions, for a source without them) and its body, the same each time the request is sent again, so a receiver can drop a request it already took. Idempotency-Key when left out."`
	CAFile            string            `json:"ca_file,omitempty" doc:"A PEM file of the certificates the endpoint's is checked against, in place of the system's roots. The check cannot be turned off."`
	CertFile          string            `json:"cert_file,omitempty" doc:"A PEM file of a client certificate, and the chain after it, presented to an endpoint that asks for one; needs key_file."`
	KeyFile           string            `json:"key_file,omitempty" doc:"The PEM file of cert_file's private key."`
	ServerName        string            `json:"server_name,omitempty" doc:"The name the endpoint's certificate must have, and the TLS server name sent; the URL's host when left out."`
	Timeout           config.Duration   `json:"timeout,omitempty" default:"\"30s\"" doc:"How long one request may take, from connecting to the end of its answer; one that takes longer is tried again. 30s when left out or 0."`
	MaxRequestBytes   int64             `json:"max_request_bytes,omitempty" min:"0" default:"8388608" doc:"The most bytes of one request's body; a batch larger is sent in several, in order, and a record larger alone fails for good. 8 MiB when left out or 0."`

	url     *url.URL
	auth    string
	tls     *tls.Config
	engine  *fib.Engine
	codec   fitsync.Codec
	json    fitsync.JSONCodec
	ctype   string
	header  stdhttp.Header
	idemKey string

	mu      sync.Mutex
	client  *fibhttp.Client
	own     *fib.Engine
	ownDone chan struct{}
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Provision checks the params, reads the secrets and the TLS files; nothing
// is sent until the first write.
func (s *Sink) Provision(ctx *module.Context) error {
	var errs config.Errors
	add := func(field, format string, args ...any) {
		errs = append(errs, ctx.Params.Field(field).Errorf(format, args...))
	}
	u, err := url.Parse(s.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Fragment != "" || u.User != nil {
		add("url", "%q is not an http or https URL of a host, with no fragment or user", s.URL)
	}
	s.url = u
	if s.Method == "" {
		s.Method = stdhttp.MethodPost
	}
	if s.MaxRequestBytes == 0 {
		s.MaxRequestBytes = DefaultMaxRequestBytes
	}
	s.idemKey = s.IdempotencyHeader
	if s.idemKey == "" {
		s.idemKey = DefaultIdempotencyHeader
	}
	if !validToken(s.idemKey) {
		add("idempotency_header", "%q is not a valid header name", s.idemKey)
	}
	s.json.Form = s.Form
	switch {
	case s.Codec != nil:
		if s.Format != "" || s.Form != "" {
			add("codec", "a codec, or format and form, not both")
		}
		v, err := ctx.Load(fitsync.Codecs, *s.Codec)
		if err != nil {
			errs = append(errs, config.AsErrors(err)...)
		} else if c, ok := v.(fitsync.Codec); ok {
			s.codec = c
		} else {
			module.Cleanup(v)
			errs = append(errs, s.Codec.At.Field("module").Errorf("%s is not a codec", s.Codec.Module))
		}
		s.ctype = s.ContentType
		if s.ctype == "" {
			s.ctype = "application/octet-stream"
		}
	case s.ContentType != "":
		add("content_type", "content_type is a codec's: format sets its own")
	case s.Format == "json":
		s.ctype = "application/json"
	default:
		s.ctype = "application/x-ndjson"
	}
	if strings.ContainsAny(s.ctype, "\r\n\x00") {
		add("content_type", "a line break or NUL")
	}
	s.header = stdhttp.Header{}
	for name, value := range s.Headers {
		switch {
		case !validToken(name):
			add("headers", "%q is not a valid header name", name)
		case strings.ContainsAny(value, "\r\n\x00"):
			add("headers", "the value of %s holds a line break or NUL", name)
		case strings.EqualFold(name, "Authorization") || strings.EqualFold(name, "Content-Type") ||
			strings.EqualFold(name, "Content-Length") || strings.EqualFold(name, "Host") || strings.EqualFold(name, s.idemKey):
			add("headers", "%s is set by the sink", name)
		default:
			s.header.Set(name, value)
		}
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
	case s.BearerTokenEnv != "" && (s.Username != "" || s.PasswordEnv != ""):
		add("bearer_token_env", "a bearer token or a username, not both")
	case s.BearerTokenEnv != "":
		s.auth = "Bearer " + secret("bearer_token_env", s.BearerTokenEnv)
	case s.Username != "" && s.PasswordEnv != "":
		s.auth = "Basic " + base64.StdEncoding.EncodeToString([]byte(s.Username+":"+secret("password_env", s.PasswordEnv)))
	case s.Username != "" || s.PasswordEnv != "":
		add("username", "username and password_env go together")
	}
	if strings.ContainsAny(s.Username, ":\r\n") {
		add("username", "a username has no ':' or line breaks")
	}
	if strings.ContainsAny(s.auth, "\r\n\x00") {
		add("bearer_token_env", "the token holds a line break or NUL")
	}
	tc := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: s.ServerName}
	if s.CAFile != "" {
		pem, err := os.ReadFile(s.CAFile)
		tc.RootCAs = x509.NewCertPool()
		if err != nil {
			add("ca_file", "%v", err)
		} else if !tc.RootCAs.AppendCertsFromPEM(pem) {
			add("ca_file", "%s holds no PEM certificate", s.CAFile)
		}
	}
	if (s.CertFile == "") != (s.KeyFile == "") {
		add("cert_file", "cert_file and key_file go together")
	} else if s.CertFile != "" {
		cert, err := tls.LoadX509KeyPair(s.CertFile, s.KeyFile)
		if err != nil {
			add("cert_file", "%v", err)
		}
		tc.Certificates = []tls.Certificate{cert}
	}
	s.tls = tc
	s.engine = ctx.Engine()
	if len(errs) > 0 {
		module.Cleanup(s.codec)
		s.codec = nil
		return errs
	}
	return nil
}

// validToken reports whether name is an HTTP token.
func validToken(name string) bool {
	if name == "" {
		return false
	}
	for i := range len(name) {
		c := name[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0 {
			continue
		}
		return false
	}
	return true
}

// ParallelWrites marks the sink as one a job may write through several
// writers at once.
func (s *Sink) ParallelWrites() {}

// Cleanup closes the client, stops the engine of its own and lets the codec
// go.
func (s *Sink) Cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client != nil {
		s.client.Close()
		s.client = nil
	}
	if s.own != nil {
		s.own.Stop()
		<-s.ownDone
		_ = s.own.Close()
		s.own = nil
	}
	module.Cleanup(s.codec)
}

func (s *Sink) timeout() time.Duration {
	if s.Timeout <= 0 {
		return DefaultTimeout
	}
	return s.Timeout.Std()
}

// httpClient returns the client, made at the first request, on an engine of
// its own when the pipeline has none.
func (s *Sink) httpClient() (*fibhttp.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client != nil {
		return s.client, nil
	}
	e := s.engine
	if e == nil {
		cfg := fib.DefaultConfig()
		cfg.LogSettings = false
		own, err := fib.New(cfg)
		if err != nil {
			return nil, err
		}
		s.own, s.ownDone, e = own, make(chan struct{}), own
		go func() { _ = own.Run(); close(s.ownDone) }()
	}
	cc := fibhttp.DefaultClientConfig()
	t := s.timeout()
	cc.Timeout, cc.ResponseHeaderTimeout, cc.DialTimeout = t, t, min(t, 10*time.Second)
	cc.MaxConnsPerHost, cc.MaxIdleConnsPerHost = 16, 16
	cc.IdleConnTimeout = 90 * time.Second
	cc.MaxResponseBodyBytes = maxResponseBytes
	cc.TLSConfig = s.tls
	s.client = fibhttp.NewClient(e, cc)
	return s.client, nil
}

// encode appends one record as the body has it.
func (s *Sink) encode(b []byte, r *fitsync.Record) ([]byte, error) {
	if s.codec != nil {
		return s.codec.Encode(b, []*fitsync.Record{r})
	}
	return s.json.Encode(b, []*fitsync.Record{r})
}

// Write sends the batch in requests of up to max_request_bytes, in order.
// A request that fails ends the write: a *BatchError names its first record
// when those before it were sent.
func (s *Sink) Write(ctx context.Context, batch []*fitsync.Record) error {
	var body, one []byte
	start := 0
	flush := func(end int) error {
		if end == start {
			return nil
		}
		if s.codec == nil && s.Format == "json" {
			body = append(body, ']')
		}
		err := s.send(ctx, batch[start:end], body)
		if err != nil {
			if start > 0 {
				return &fitsync.BatchError{Index: start, Err: err}
			}
			return err
		}
		start, body = end, body[:0]
		return nil
	}
	for i, r := range batch {
		var err error
		one, err = s.encode(one[:0], r)
		if err != nil {
			if err := flush(i); err != nil {
				return err
			}
			return &fitsync.BatchError{Index: i, Err: fitsync.Permanent(fmt.Errorf("encoding the record: %w", err))}
		}
		if s.codec == nil && s.Format == "json" {
			one = bytes.TrimSuffix(one, []byte{'\n'})
		}
		if int64(len(one)+2) > s.MaxRequestBytes {
			if err := flush(i); err != nil {
				return err
			}
			return &fitsync.BatchError{Index: i, Err: fitsync.Permanent(fmt.Errorf("the record's %d bytes are more than max_request_bytes", len(one)))}
		}
		if i > start && int64(len(body)+len(one)+2) > s.MaxRequestBytes {
			if err := flush(i); err != nil {
				return err
			}
		}
		if s.codec == nil && s.Format == "json" {
			if i == start {
				body = append(body, '[')
			} else {
				body = append(body, ',')
			}
		}
		body = append(body, one...)
	}
	return flush(len(batch))
}

// IdempotencyKey is the key a request of recs with body carries: 32 hex
// digits of a SHA-256 of each record's origin (Record.AppendOrigin), or its
// position when it has none, length first, and the body, so a request sent
// again carries the same, and two of the same body at different positions
// differ. An origin keeps the key when a source's position does not, as a
// Kafka or Pulsar source's, which holds every partition's progress in the
// order a run read them.
func IdempotencyKey(recs []*fitsync.Record, body []byte) string {
	h := sha256.New()
	var n [8]byte
	var buf [96]byte
	for _, r := range recs {
		if o := r.AppendOrigin(buf[:0]); len(o) > 0 {
			binary.BigEndian.PutUint64(n[:], 1<<63|uint64(len(o)))
			h.Write(n[:])
			h.Write(o)
			continue
		}
		binary.BigEndian.PutUint64(n[:], uint64(len(r.Position)))
		h.Write(n[:])
		h.Write(r.Position)
	}
	binary.BigEndian.PutUint64(n[:], uint64(len(body)))
	h.Write(n[:])
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// send sends one request and waits for its answer, on the pipeline's
// goroutine.
func (s *Sink) send(ctx context.Context, recs []*fitsync.Record, body []byte) error {
	cl, err := s.httpClient()
	if err != nil {
		return err
	}
	req, err := stdhttp.NewRequestWithContext(ctx, s.Method, s.url.String(), bytes.NewReader(body))
	if err != nil {
		return fitsync.Permanent(err)
	}
	for k, v := range s.header {
		req.Header[k] = v
	}
	req.Header.Set("Content-Type", s.ctype)
	req.Header.Set(s.idemKey, IdempotencyKey(recs, body))
	if s.auth != "" {
		req.Header.Set("Authorization", s.auth)
	}
	type result struct {
		status int
		body   []byte
		err    error
	}
	done := make(chan result, 1)
	cl.Do(req, func(r *fibhttp.ClientResponse, e error) {
		if e != nil {
			done <- result{err: e}
			return
		}
		b, _ := r.BodyBytes()
		done <- result{status: r.StatusCode, body: bytes.Clone(clip(b))}
	})
	var res result
	select {
	case res = <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	switch {
	case res.err != nil:
		return fmt.Errorf("%s: %w", s.url.Host, res.err)
	case res.status >= 200 && res.status < 300:
		return nil
	case res.status >= 500, res.status == stdhttp.StatusRequestTimeout, res.status == stdhttp.StatusTooManyRequests:
		return fmt.Errorf("%s answered %d: %s", s.url.Host, res.status, res.body)
	}
	return fitsync.Permanent(fmt.Errorf("%s answered %d: %s", s.url.Host, res.status, res.body))
}

// Test sends the URL a HEAD request, with the sink's headers and
// credentials and no body, which by HTTP's rules changes nothing and which
// no receiver takes as a batch (I188b). It passes when the endpoint
// answers 2xx, or 4xx other than 401, 403 and 407: an ingest endpoint
// routing only its method may answer HEAD 404 or 405, which still says
// that the host, its TLS and the path's server are there. A redirect fails
// it, as it fails a write; so do 5xx and a connection's or TLS's error.
func (s *Sink) Test(ctx context.Context) error {
	cl, err := s.httpClient()
	if err != nil {
		return err
	}
	// The URL was parsed by Provision, so the request is made.
	req, _ := stdhttp.NewRequestWithContext(ctx, stdhttp.MethodHead, s.url.String(), nil)
	for k, v := range s.header {
		req.Header[k] = v
	}
	if s.auth != "" {
		req.Header.Set("Authorization", s.auth)
	}
	done := make(chan error, 1)
	var status int
	cl.Do(req, func(r *fibhttp.ClientResponse, e error) {
		if e == nil {
			status = r.StatusCode
		}
		done <- e
	})
	select {
	case err = <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	switch {
	case err != nil:
		return fmt.Errorf("%s: %w", s.url.Host, err)
	case status == stdhttp.StatusUnauthorized, status == stdhttp.StatusForbidden, status == stdhttp.StatusProxyAuthRequired:
		return fmt.Errorf("%s answered HEAD %d: the credentials are refused", s.url.Host, status)
	case status >= 300 && status < 400:
		return fmt.Errorf("%s answered HEAD %d: a redirect, which the sink does not follow", s.url.Host, status)
	case status >= 500:
		return fmt.Errorf("%s answered HEAD %d", s.url.Host, status)
	}
	return nil
}

// clip returns the start of an answer's body, for an error.
func clip(b []byte) []byte {
	const n = 300
	if len(b) > n {
		return append(b[:n:n], "..."...)
	}
	return b
}

var (
	_ fitsync.ParallelSink = (*Sink)(nil)
	_ module.Cleaner       = (*Sink)(nil)
)
