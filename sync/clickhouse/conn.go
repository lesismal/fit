// Package syncclickhouse is sync's ClickHouse: the sink clickhouse, which
// inserts records as JSONEachRow with a deduplication token, writing
// updates and deletes as rows of a ReplacingMergeTree, and the source
// clickhouse, which reads a table or a query by its key and then polls it
// by a cursor column. Both speak ClickHouse's HTTP interface through fib's
// asynchronous HTTP client, on the role's engine, or on an engine of the
// package's own when the pipeline runs without one (fit sync run). See
// docs/decisions/036-sync.md.
package syncclickhouse

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

// The defaults of the connection's params.
const (
	DefaultTimeout          = 60 * time.Second
	DefaultMaxResponseBytes = 64 << 20
)

// ConnParams are a connector's params for reaching the server: its URLs,
// credentials, TLS and database. The sink and the source declare them as
// their own params, with the same names and docs, and hand them to NewConn.
type ConnParams struct {
	URLs                                  []string
	Username, PasswordEnv, Database       string
	CAFile, CertFile, KeyFile, ServerName string
	Timeout                               config.Duration
}

// Conn is a connection to the server: the client and the URL in use. It is
// safe for use by several goroutines.
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
	cur     atomic.Int64
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// NewConn checks the params, reads the password and the TLS files; nothing
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
	password := ""
	if c.PasswordEnv != "" {
		if !envName.MatchString(c.PasswordEnv) {
			add("password_env", "%q is not an environment variable's name", c.PasswordEnv)
		} else if password = os.Getenv(c.PasswordEnv); password == "" {
			add("password_env", "the environment variable %s is not set, or empty", c.PasswordEnv)
		}
		if c.Username == "" {
			add("password_env", "password_env needs username")
		}
	}
	if strings.ContainsAny(c.Username, ":\r\n") {
		add("username", "a username has no ':' or line breaks")
	}
	if c.Username != "" {
		c.auth = "Basic " + base64.StdEncoding.EncodeToString([]byte(c.Username+":"+password))
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

// Test runs SELECT 1 in the database, if any (I188b): the server answers,
// the credentials are taken, and the database is there.
func (c *Conn) Test(ctx context.Context) error {
	b, err := c.Do(ctx, url.Values{"query": {"SELECT 1"}}, nil)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(b)) != "1" {
		return fmt.Errorf("SELECT 1 answered %q: not ClickHouse", clip(b))
	}
	return nil
}

// ServerError is the server's answer to a query that failed: its HTTP
// status, and ClickHouse's error code and message.
type ServerError struct {
	Status  int
	Code    int
	Message string
}

func (e *ServerError) Error() string {
	return fmt.Sprintf("ClickHouse answered %d: %s", e.Status, clip([]byte(e.Message)))
}

var atRow = regexp.MustCompile(`\(at row (\d+)\)`)

// Row returns the row of the data an error names, counting from 1, or 0.
func (e *ServerError) Row() int {
	m := atRow.FindStringSubmatch(e.Message)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// dataCodes are ClickHouse's errors about the data sent rather than the
// server or the table, which sending the same data again cannot cure.
var dataCodes = map[int]bool{
	6:   true, // CANNOT_PARSE_TEXT
	26:  true, // CANNOT_PARSE_QUOTED_STRING
	27:  true, // CANNOT_PARSE_INPUT_ASSERTION_FAILED
	38:  true, // CANNOT_PARSE_DATE
	41:  true, // CANNOT_PARSE_DATETIME
	53:  true, // TYPE_MISMATCH
	69:  true, // ARGUMENT_OUT_OF_BOUND
	70:  true, // CANNOT_CONVERT_TYPE
	72:  true, // CANNOT_PARSE_NUMBER
	117: true, // INCORRECT_DATA
	131: true, // TOO_LARGE_STRING_SIZE
	349: true, // CANNOT_INSERT_NULL_IN_ORDINARY_COLUMN
}

// DataError reports whether the error is about the data sent: a value the
// server cannot parse or convert, or a row it names.
func (e *ServerError) DataError() bool { return dataCodes[e.Code] || e.Row() > 0 }

// unavailable is a node's answer that sends the request to the next.
func unavailable(status int) bool {
	return status == stdhttp.StatusBadGateway || status == stdhttp.StatusServiceUnavailable || status == stdhttp.StatusGatewayTimeout
}

// Do posts body to the server with the URL's query q (the SQL in q's query,
// or the body itself, and settings): to the URL that answered last, then to
// each other in turn while one fails to answer (a connection's error, a
// timeout, 502, 503 or 504). It returns the answer's body, a *ServerError
// for a status other than 200, or the last error. It waits for the answer,
// so it is called on a pipeline's goroutine, never a fib worker's.
func (c *Conn) Do(ctx context.Context, q url.Values, body []byte) ([]byte, error) {
	cl, err := c.httpClient()
	if err != nil {
		return nil, err
	}
	if c.Database != "" && q.Get("database") == "" {
		q.Set("database", c.Database)
	}
	query := q.Encode()
	var lastErr error
	start := int(c.cur.Load())
	for i := range c.urls {
		n := (start + i) % len(c.urls)
		status, header, b, err := c.send(ctx, cl, c.urls[n], query, body)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		switch {
		case err != nil:
			lastErr = fmt.Errorf("%s: %w", c.urls[n].Host, err)
			continue
		case status == stdhttp.StatusOK:
			c.cur.Store(int64(n))
			return b, nil
		}
		code, _ := strconv.Atoi(header.Get("X-ClickHouse-Exception-Code"))
		lastErr = &ServerError{Status: status, Code: code, Message: strings.TrimSpace(string(b))}
		if !unavailable(status) {
			c.cur.Store(int64(n))
			return nil, lastErr
		}
	}
	c.cur.Store(int64((start + 1) % len(c.urls)))
	return nil, lastErr
}

func (c *Conn) send(ctx context.Context, cl *fibhttp.Client, base *url.URL, query string, body []byte) (int, stdhttp.Header, []byte, error) {
	u := *base
	u.Path += "/"
	u.RawQuery = query
	req, err := stdhttp.NewRequestWithContext(ctx, stdhttp.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	if c.auth != "" {
		req.Header.Set("Authorization", c.auth)
	}
	type result struct {
		status int
		header stdhttp.Header
		body   []byte
		err    error
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
		done <- result{status: r.StatusCode, header: r.Header, body: bytes.Clone(b)}
	})
	select {
	case res := <-done:
		return res.status, res.header, res.body, res.err
	case <-ctx.Done():
		return 0, nil, nil, ctx.Err()
	}
}

// clip returns the start of an answer's body, for an error.
func clip(b []byte) string {
	const n = 500
	if len(b) > n {
		return string(b[:n]) + "..."
	}
	return string(b)
}

// QuoteName quotes a table's name for SQL: db.table as `db`.`table`, each
// part in backquotes with a backquote or backslash escaped by a backslash.
func QuoteName(name string) string {
	db, table, ok := strings.Cut(name, ".")
	if !ok {
		return quoteIdent(name)
	}
	return quoteIdent(db) + "." + quoteIdent(table)
}

func quoteIdent(s string) string {
	return "`" + strings.NewReplacer("\\", "\\\\", "`", "\\`").Replace(s) + "`"
}

// quoteString quotes s as a SQL string literal.
func quoteString(s string) string {
	var b strings.Builder
	b.WriteByte('\'')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\\', '\'':
			b.WriteByte('\\')
			b.WriteByte(c)
		case 0:
			b.WriteString(`\0`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('\'')
	return b.String()
}
