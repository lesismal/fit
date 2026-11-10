// Package syncinflux is sync's InfluxDB: the sink influxdb, which writes
// records as points of the line protocol to InfluxDB 2's /api/v2/write,
// 3's /api/v3/write_lp or 1's /write, through fib's asynchronous HTTP
// client; and the mapping of records to points, which the tdengine sink
// shares. A point is its series (measurement and tags) and its time: the
// same record written again is the same point, written over itself. See
// docs/sync.md.
package syncinflux

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	stdhttp "net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	"github.com/lesismal/fit/sync/internal/fibrest"
)

func init() {
	module.Register(module.Info{ID: fitsync.Sinks + ".influxdb", New: func() any { return new(Sink) },
		Doc: "Writes records to InfluxDB as points of the line protocol: InfluxDB 2's /api/v2/write (or 3's /api/v3/write_lp, or 1's /write), the measurement from a template, tags and fields from the row's fields, the time from a field or the commit time, gzipped, through fib's asynchronous HTTP client. A point is its series and its time, so a record written again writes the same point over itself. Deletes are left out (or fail, as deletes says); a 429 or 5xx is tried again with a backoff, and a point the server refuses fails its record, which on_error takes."})
}

// The defaults of the sink's params.
const (
	DefaultMaxRequestBytes = 4 << 20
	DefaultTimeout         = 30 * time.Second
)

// Sink is the sink influxdb.
type Sink struct {
	URL             string          `json:"url" fit:"required" doc:"The server's HTTP endpoint, as http://influxdb:8086 (InfluxDB 2), http://influxdb3:8181 (InfluxDB 3). fib's client does not follow redirects."`
	API             string          `json:"api,omitempty" enum:"v2,v3,v1" default:"\"v2\"" doc:"The write API: v2, /api/v2/write with org and bucket (InfluxDB 2, and 3's compatibility); v3, /api/v3/write_lp with database (InfluxDB 3); v1, /write with database and retention_policy (InfluxDB 1, and 2's compatibility). v2 when left out."`
	Org             string          `json:"org,omitempty" doc:"The organization of api v2, its name or ID."`
	Bucket          string          `json:"bucket,omitempty" doc:"The bucket api v2 writes to; required with it."`
	Database        string          `json:"database,omitempty" doc:"The database api v3 and v1 write to; required with them."`
	RetentionPolicy string          `json:"retention_policy,omitempty" doc:"api v1's retention policy; the database's default when left out."`
	TokenEnv        string          `json:"token_env,omitempty" doc:"The environment variable holding an API token, read when the pipeline loads: sent as Authorization: Token (v2, v1) or Bearer (v3). Not with username."`
	Username        string          `json:"username,omitempty" doc:"The user of api v1's basic authentication, with password_env."`
	PasswordEnv     string          `json:"password_env,omitempty" doc:"The environment variable holding username's password, read when the pipeline loads."`
	CAFile          string          `json:"ca_file,omitempty" doc:"A PEM file of the certificates the server's is checked against, in place of the system's roots. The check cannot be turned off."`
	CertFile        string          `json:"cert_file,omitempty" doc:"A PEM file of a client certificate, and the chain after it, presented to a server that asks for one; needs key_file."`
	KeyFile         string          `json:"key_file,omitempty" doc:"The PEM file of cert_file's private key."`
	ServerName      string          `json:"server_name,omitempty" doc:"The name the server's certificate must have, and the TLS server name sent; the URL's host when left out."`
	Timeout         config.Duration `json:"timeout,omitempty" default:"\"30s\"" doc:"How long one request may take, from connecting to the end of its answer. 30s when left out or 0."`
	Measurement     string          `json:"measurement,omitempty" default:"\"{namespace}\"" doc:"The measurement a record's point goes to: text with placeholders, {namespace} (or {$namespace}, as record templates write it) the record's namespace and {name} the row's field name (which the point still has as a field or tag); {{ is a brace. {namespace} when left out."`
	Tags            []string        `json:"tags,omitempty" doc:"The row's fields that are the point's tags, as text; a field missing, null or empty is left out of them. Tags and the measurement are the series, which with the time is the point: keep them to fields that do not change, or bounded ones. None when left out."`
	Fields          []string        `json:"fields,omitempty" doc:"The row's fields written as the point's fields; every field of the row but the tags and time_field when left out. Ints, uints, floats, decimals (as floats) and bools are written as such, any other value as a string of its text (bytes in base64, maps and arrays as JSON). Null fields are left out; a point with no field fails its record."`
	TimeField       string          `json:"time_field,omitempty" doc:"The row's field the point's time is read from: a time, a date, an int of Unix nanoseconds, or a string in RFC 3339. The record's commit time when left out, or the time it is written when the source gives none, which a replay does not repeat."`
	Precision       string          `json:"precision,omitempty" enum:"ns,us,ms,s" default:"\"ns\"" doc:"The precision of the points' times, which are cut to it: ns, us, ms or s. Two records of a series within one unit are one point. ns when left out."`
	Deletes         string          `json:"deletes,omitempty" enum:"skip,fail" default:"\"skip\"" doc:"What a delete does: skip leaves it out, counted as written (the line protocol has no deletes); fail fails it, which on_error takes. skip when left out."`
	Gzip            *bool           `json:"gzip,omitempty" default:"true" doc:"Send requests gzipped. On when left out."`
	MaxRequestBytes int64           `json:"max_request_bytes,omitempty" min:"0" default:"4194304" doc:"The most bytes of points, before gzip, one request carries; a batch larger is sent in several, in order, and a point larger fails its record. 4 MiB when left out or 0."`

	client  *fibrest.Client
	mapping *Mapping
	prec    Precision
	flavor  Flavor
	path    string
	query   string
}

// Provision checks the params and reads the secrets and TLS files; nothing
// is sent until the first write.
func (s *Sink) Provision(ctx *module.Context) error {
	var errs config.Errors
	add := func(field, format string, args ...any) {
		errs = append(errs, ctx.Params.Field(field).Errorf(format, args...))
	}
	header := stdhttp.Header{}
	if s.API == "" {
		s.API = "v2"
	}
	if s.Precision == "" {
		s.Precision = string(Nanoseconds)
	}
	s.prec = Precision(s.Precision)
	q := url.Values{}
	switch s.API {
	case "v2":
		s.path = "/api/v2/write"
		if s.Bucket == "" {
			add("bucket", "api v2 needs a bucket")
		}
		if s.Database != "" || s.RetentionPolicy != "" {
			add("database", "database and retention_policy are api v1's and v3's: v2 takes org and bucket")
		}
		q.Set("bucket", s.Bucket)
		if s.Org != "" {
			q.Set("org", s.Org)
		}
		q.Set("precision", s.Precision)
	case "v3":
		s.path = "/api/v3/write_lp"
		if s.Database == "" {
			add("database", "api v3 needs a database")
		}
		if s.Org != "" || s.Bucket != "" || s.RetentionPolicy != "" {
			add("org", "org, bucket and retention_policy are not api v3's: it takes database")
		}
		q.Set("db", s.Database)
		q.Set("precision", map[Precision]string{Nanoseconds: "nanosecond", Microseconds: "microsecond", Milliseconds: "millisecond", Seconds: "second"}[s.prec])
		q.Set("accept_partial", "false")
	default: // v1
		s.path = "/write"
		s.flavor = Influx1
		if s.Database == "" {
			add("database", "api v1 needs a database")
		}
		if s.Org != "" || s.Bucket != "" {
			add("org", "org and bucket are api v2's: v1 takes database and retention_policy")
		}
		q.Set("db", s.Database)
		if s.RetentionPolicy != "" {
			q.Set("rp", s.RetentionPolicy)
		}
		q.Set("precision", map[Precision]string{Nanoseconds: "n", Microseconds: "u", Milliseconds: "ms", Seconds: "s"}[s.prec])
	}
	s.query = q.Encode()
	switch {
	case s.TokenEnv != "" && (s.Username != "" || s.PasswordEnv != ""):
		add("token_env", "a token or a username, not both")
	case s.TokenEnv != "":
		tok, fe := fibrest.Secret("token_env", s.TokenEnv)
		if fe != nil {
			add(fe.Field, "%v", fe.Err)
		}
		scheme := "Token "
		if s.API == "v3" {
			scheme = "Bearer "
		}
		header.Set("Authorization", scheme+tok)
	case s.Username != "" && s.PasswordEnv != "":
		if s.API != "v1" {
			add("username", "username and password_env are api v1's: v2 and v3 take token_env")
		}
		pw, fe := fibrest.Secret("password_env", s.PasswordEnv)
		if fe != nil {
			add(fe.Field, "%v", fe.Err)
		}
		header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(s.Username+":"+pw)))
	case s.Username != "" || s.PasswordEnv != "":
		add("username", "username and password_env go together")
	}
	if strings.ContainsAny(s.Username, ":\r\n") {
		add("username", "a username has no ':' or line breaks")
	}
	var merrs []error
	s.mapping, merrs = NewMapping(s.Measurement, s.Tags, s.Fields, s.TimeField)
	for _, e := range merrs {
		field, msg, _ := strings.Cut(e.Error(), ": ")
		add(field, "%s", msg)
	}
	var ferrs []fibrest.FieldError
	s.client, ferrs = fibrest.New(ctx.Engine(), fibrest.Params{URL: s.URL, CAFile: s.CAFile, CertFile: s.CertFile, KeyFile: s.KeyFile,
		ServerName: s.ServerName, Timeout: s.Timeout.Std(), MaxResponseBytes: 1 << 20, Header: header})
	for _, fe := range ferrs {
		add(fe.Field, "%v", fe.Err)
	}
	if len(errs) > 0 {
		return errs
	}
	return nil
}

// ParallelWrites marks the sink as one a job may write through several
// writers at once: a point is its series and time, whatever the order.
func (s *Sink) ParallelWrites() {}

// Cleanup closes the client.
func (s *Sink) Cleanup() {
	if s.client != nil {
		s.client.Close()
	}
}

func (s *Sink) maxRequest() int {
	if s.MaxRequestBytes <= 0 {
		return DefaultMaxRequestBytes
	}
	return int(s.MaxRequestBytes)
}

// Write writes the batch's points in requests of at most max_request_bytes,
// in order. A record that cannot be a point, or that the server refuses,
// fails for good at its index, those before it written.
func (s *Sink) Write(ctx context.Context, batch []*fitsync.Record) error {
	now := time.Now()
	lines := make([][]byte, len(batch))
	bad, badErr := len(batch), error(nil)
	for i, r := range batch {
		if lines[i], badErr = s.line(r, now); badErr != nil {
			bad = i
			break
		}
	}
	err := fibrest.WriteFinding(ctx, bad, func(ctx context.Context, lo, hi int) error {
		return s.send(ctx, lines[lo:hi])
	})
	if err != nil {
		return err
	}
	if badErr != nil {
		return &fitsync.BatchError{Index: bad, Err: fitsync.Permanent(badErr)}
	}
	return nil
}

// line returns r's line, nil for a record left out.
func (s *Sink) line(r *fitsync.Record, now time.Time) ([]byte, error) {
	switch r.Op {
	case fitsync.OpDDL:
		return nil, nil
	case fitsync.OpDelete:
		if s.Deletes == "fail" {
			return nil, errors.New("a delete, which the line protocol cannot write (deletes fail)")
		}
		return nil, nil
	case fitsync.OpInsert, fitsync.OpUpdate, fitsync.OpMessage:
	default:
		return nil, fmt.Errorf("a record of operation %s", r.Op)
	}
	p, err := s.mapping.Point(r, now)
	if err != nil {
		return nil, err
	}
	b, err := AppendLine(nil, p, s.prec, s.flavor)
	if err != nil {
		return nil, err
	}
	if len(b) > s.maxRequest() {
		return nil, fmt.Errorf("the point is %d bytes, more than max_request_bytes", len(b))
	}
	return b, nil
}

// send writes lines in requests of at most max_request_bytes; nil lines
// are records left out.
func (s *Sink) send(ctx context.Context, lines [][]byte) error {
	var body []byte
	flush := func() error {
		if len(body) == 0 {
			return nil
		}
		err := s.post(ctx, body)
		body = body[:0]
		return err
	}
	for _, l := range lines {
		if len(body)+len(l) > s.maxRequest() {
			if err := flush(); err != nil {
				return err
			}
		}
		body = append(body, l...)
	}
	return flush()
}

// post sends one request of lines.
func (s *Sink) post(ctx context.Context, body []byte) error {
	h := stdhttp.Header{"Content-Type": {"text/plain; charset=utf-8"}}
	if s.Gzip == nil || *s.Gzip {
		var z bytes.Buffer
		w := gzip.NewWriter(&z)
		_, _ = w.Write(body)
		_ = w.Close()
		body = z.Bytes()
		h.Set("Content-Encoding", "gzip")
	}
	resp, err := s.client.DoRetry(ctx, fibrest.Request{Method: stdhttp.MethodPost, Path: s.path, Query: s.query, Header: h, Body: body}, 0)
	if err != nil {
		return err
	}
	switch st := resp.Status; {
	case st >= 200 && st < 300:
		return nil
	case st == stdhttp.StatusBadRequest, st == stdhttp.StatusUnprocessableEntity, st == stdhttp.StatusRequestEntityTooLarge:
		return fibrest.Refused(fibrest.StatusError("the write", resp))
	}
	return fibrest.StatusError("the write", resp)
}

// Test checks the server answers, with the sink's credentials: api v2
// lists the bucket, v3 the databases, v1 SHOW DATABASES. It writes
// nothing.
func (s *Sink) Test(ctx context.Context) error {
	var r fibrest.Request
	switch s.API {
	case "v2":
		q := url.Values{"name": {s.Bucket}}
		if s.Org != "" {
			q.Set("org", s.Org)
		}
		r = fibrest.Request{Method: stdhttp.MethodGet, Path: "/api/v2/buckets", Query: q.Encode()}
	case "v3":
		r = fibrest.Request{Method: stdhttp.MethodGet, Path: "/api/v3/configure/database", Query: "format=json"}
	default:
		r = fibrest.Request{Method: stdhttp.MethodGet, Path: "/query", Query: url.Values{"q": {"SHOW DATABASES"}}.Encode()}
	}
	resp, err := s.client.Do(ctx, r)
	if err != nil {
		return err
	}
	if resp.Status != stdhttp.StatusOK {
		return fibrest.StatusError(r.Path, resp)
	}
	if s.API == "v2" {
		var b struct {
			Buckets []struct{ Name string } `json:"buckets"`
		}
		if json.Unmarshal(resp.Body, &b) != nil || len(b.Buckets) == 0 {
			return fmt.Errorf("the bucket %s is not there, or the token cannot see it: %s", s.Bucket, fibrest.Clip(resp.Body))
		}
	}
	return nil
}

var (
	_ fitsync.ParallelSink = (*Sink)(nil)
	_ fitsync.Tester       = (*Sink)(nil)
	_ module.Cleaner       = (*Sink)(nil)
)
