// Package synctdengine is sync's TDengine: the sink tdengine, which writes
// records through taosAdapter's REST interface, as schemaless line
// protocol (/influxdb/v1/write) or as SQL INSERTs into subtables of super
// tables (/rest/sql), through fib's asynchronous HTTP client. A row is its
// subtable and its timestamp: a record written again writes the same row
// over itself. See docs/sync.md.
package synctdengine

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	stdhttp "net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	syncinflux "github.com/lesismal/fit/sync/influxdb"
	"github.com/lesismal/fit/sync/internal/fibrest"
	"github.com/lesismal/fit/sync/internal/tmpl"
)

func init() {
	module.Register(module.Info{ID: fitsync.Sinks + ".tdengine", New: func() any { return new(Sink) },
		Doc: "Writes records to TDengine 3 through taosAdapter's REST interface: as schemaless line protocol, the measurement a super table and the tags its subtable's, made as they are first written; or as SQL INSERTs into subtables of an existing super table, named by a template, through fib's asynchronous HTTP client. A row is its subtable and its timestamp, so a record written again writes the same row over itself. Deletes are left out (or fail, as deletes says); a 429 or 5xx the server does not explain is tried again with a backoff, and a row it refuses fails its record, which on_error takes."})
}

// The defaults of the sink's params.
const (
	DefaultMaxRequestBytes = 1 << 20
	DefaultTimeColumn      = "ts"
)

// Sink is the sink tdengine.
type Sink struct {
	URL             string          `json:"url" fit:"required" doc:"taosAdapter's HTTP endpoint, as http://tdengine:6041. fib's client does not follow redirects."`
	Username        string          `json:"username" fit:"required" doc:"The user, as root."`
	PasswordEnv     string          `json:"password_env" fit:"required" doc:"The environment variable holding username's password, read when the pipeline loads."`
	CAFile          string          `json:"ca_file,omitempty" doc:"A PEM file of the certificates the server's is checked against, in place of the system's roots. The check cannot be turned off."`
	CertFile        string          `json:"cert_file,omitempty" doc:"A PEM file of a client certificate, and the chain after it, presented to a server that asks for one; needs key_file."`
	KeyFile         string          `json:"key_file,omitempty" doc:"The PEM file of cert_file's private key."`
	ServerName      string          `json:"server_name,omitempty" doc:"The name the server's certificate must have, and the TLS server name sent; the URL's host when left out."`
	Timeout         config.Duration `json:"timeout,omitempty" default:"\"30s\"" doc:"How long one request may take, from connecting to the end of its answer. 30s when left out or 0."`
	Database        string          `json:"database" fit:"required" doc:"The database written to, which must exist; its precision must be precision's."`
	Mode            string          `json:"mode,omitempty" enum:"schemaless,sql" default:"\"schemaless\"" doc:"How rows are written: schemaless, as InfluxDB's line protocol, TDengine making the super tables, subtables and columns as they are first written (tags as NCHAR, ints as BIGINT, floats as DOUBLE, strings as VARCHAR; the timestamp column _ts); sql, as INSERT ... USING the super table TAGS (...) VALUES (...) into the subtable subtable names, made at its first row, the super table yours. schemaless when left out."`
	Measurement     string          `json:"measurement,omitempty" default:"\"{namespace}\"" doc:"The super table a record's row goes to: text with placeholders, {namespace} the record's namespace and {name} the row's field name; {{ is a brace. {namespace} when left out."`
	Subtable        string          `json:"subtable,omitempty" doc:"mode sql's subtable a record's row goes to: a template as measurement's, as d_{device}. When left out, t_ and the MD5, in hex, of the super table and the tags' names and values, so that a series keeps its subtable."`
	Tags            []string        `json:"tags,omitempty" doc:"The row's fields that are the subtable's tags. In mode schemaless a field missing, null or empty is left out of them; in mode sql it is NULL. Tags are fixed when a subtable is made. None when left out."`
	Fields          []string        `json:"fields,omitempty" doc:"The row's fields written as columns; every field of the row but the tags and time_field when left out. Null fields are left out. In mode schemaless ints are BIGINT, uints BIGINT UNSIGNED, floats and decimals DOUBLE, bools BOOL, and any other value a VARCHAR of its text (bytes in base64, maps and arrays as JSON)."`
	TimeField       string          `json:"time_field,omitempty" doc:"The row's field the row's timestamp is read from: a time, a date, an int of Unix nanoseconds, or a string in RFC 3339. The record's commit time when left out, or the time it is written when the source gives none, which a replay does not repeat."`
	TimeColumn      string          `json:"time_column,omitempty" default:"\"ts\"" doc:"mode sql's timestamp column, the super table's first. ts when left out."`
	Precision       string          `json:"precision,omitempty" enum:"ns,us,ms" default:"\"ms\"" doc:"The database's precision, which timestamps are cut to: ns, us or ms. Two records of a subtable within one unit are one row. ms, TDengine's default, when left out."`
	Deletes         string          `json:"deletes,omitempty" enum:"skip,fail" default:"\"skip\"" doc:"What a delete does: skip leaves it out, counted as written; fail fails it, which on_error takes. skip when left out."`
	MaxRequestBytes int64           `json:"max_request_bytes,omitempty" min:"0" default:"1048576" doc:"The most bytes of one request: lines, or an INSERT, which TDengine's maxSQLLength bounds (1 MiB by default); a batch larger is sent in several, in order, and a row larger fails its record. 1 MiB when left out or 0."`

	client   *fibrest.Client
	mapping  *syncinflux.Mapping
	subtable []tmpl.Part
	prec     syncinflux.Precision
	query    string
}

// Provision checks the params and reads the secrets and TLS files; nothing
// is sent until the first write.
func (s *Sink) Provision(ctx *module.Context) error {
	var errs config.Errors
	add := func(field, format string, args ...any) {
		errs = append(errs, ctx.Params.Field(field).Errorf(format, args...))
	}
	if s.Mode == "" {
		s.Mode = "schemaless"
	}
	if s.Precision == "" {
		s.Precision = "ms"
	}
	if s.TimeColumn == "" {
		s.TimeColumn = DefaultTimeColumn
	}
	s.prec = syncinflux.Precision(s.Precision)
	if err := checkIdent(s.Database); err != nil {
		add("database", "%v", err)
	}
	if err := checkIdent(s.TimeColumn); err != nil {
		add("time_column", "%v", err)
	}
	if strings.ContainsAny(s.Username, ":\r\n") || s.Username == "" {
		add("username", "a username is not empty, and has no ':' or line breaks")
	}
	pw, fe := fibrest.Secret("password_env", s.PasswordEnv)
	if fe != nil {
		add(fe.Field, "%v", fe.Err)
	}
	var merrs []error
	s.mapping, merrs = syncinflux.NewMapping(s.Measurement, s.Tags, s.Fields, s.TimeField)
	for _, e := range merrs {
		field, msg, _ := strings.Cut(e.Error(), ": ")
		add(field, "%s", msg)
	}
	switch s.Mode {
	case "sql":
		if s.Subtable != "" {
			var err error
			if s.subtable, err = tmpl.Parse(s.Subtable); err != nil {
				add("subtable", "%v", err)
			}
		}
	default:
		if s.Subtable != "" || s.TimeColumn != DefaultTimeColumn {
			add("subtable", "subtable and time_column are mode sql's")
		}
		s.query = url.Values{"db": {s.Database}, "precision": {s.Precision}}.Encode()
	}
	header := stdhttp.Header{"Authorization": {"Basic " + base64.StdEncoding.EncodeToString([]byte(s.Username+":"+pw))}}
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

// checkIdent refuses a name a backquote cannot quote.
func checkIdent(s string) error {
	if s == "" || strings.ContainsAny(s, "`\r\n\x00") || len(s) > 192 {
		return fmt.Errorf("%q is not a name TDengine takes quoted: empty, too long, or with a backquote or line break", s)
	}
	return nil
}

// ParallelWrites marks the sink as one a job may write through several
// writers at once: a row is its subtable and timestamp, whatever the
// order.
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

const insertHead = "INSERT INTO"

// Write writes the batch's rows in requests of at most max_request_bytes,
// in order. A record that cannot be a row, or that the server refuses,
// fails for good at its index, those before it written.
func (s *Sink) Write(ctx context.Context, batch []*fitsync.Record) error {
	now := time.Now()
	parts := make([][]byte, len(batch))
	bad, badErr := len(batch), error(nil)
	for i, r := range batch {
		if parts[i], badErr = s.part(r, now); badErr != nil {
			bad = i
			break
		}
	}
	err := fibrest.WriteFinding(ctx, bad, func(ctx context.Context, lo, hi int) error {
		return s.send(ctx, parts[lo:hi])
	})
	if err != nil {
		return err
	}
	if badErr != nil {
		return &fitsync.BatchError{Index: bad, Err: fitsync.Permanent(badErr)}
	}
	return nil
}

// part returns what r adds to a request: a line, or an INSERT's clause;
// nil for a record left out.
func (s *Sink) part(r *fitsync.Record, now time.Time) ([]byte, error) {
	switch r.Op {
	case fitsync.OpDDL:
		return nil, nil
	case fitsync.OpDelete:
		if s.Deletes == "fail" {
			return nil, errors.New("a delete, which the sink does not write (deletes fail)")
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
	var b []byte
	if s.Mode == "sql" {
		b, err = s.clause(r, p)
	} else {
		b, err = syncinflux.AppendLine(nil, p, s.prec, syncinflux.TDengine)
	}
	if err != nil {
		return nil, err
	}
	if len(b)+len(insertHead) > s.maxRequest() {
		return nil, fmt.Errorf("the row is %d bytes, more than max_request_bytes", len(b))
	}
	return b, nil
}

// clause returns r's clause of an INSERT: " db.sub USING db.stable (tags)
// TAGS (values) (ts, columns) VALUES (stamp, values)".
func (s *Sink) clause(r *fitsync.Record, p *syncinflux.Point) ([]byte, error) {
	row := syncinflux.RowOf(r)
	sub, err := s.subtableOf(r, row, p)
	if err != nil {
		return nil, err
	}
	if err := errors.Join(checkIdent(sub), checkIdent(p.Measurement)); err != nil {
		return nil, err
	}
	b := []byte(" ")
	b = appendName(b, s.Database, sub)
	b = append(b, " USING "...)
	b = appendName(b, s.Database, p.Measurement)
	if tags := s.mapping.TagNames(); len(tags) > 0 {
		b = append(b, " ("...)
		for i, t := range tags {
			if err := checkIdent(t); err != nil {
				return nil, err
			}
			if i > 0 {
				b = append(b, ", "...)
			}
			b = appendIdent(b, t)
		}
		b = append(b, ") TAGS ("...)
		for i, t := range tags {
			if i > 0 {
				b = append(b, ", "...)
			}
			v, _ := row.Get(t)
			if b, err = appendLiteral(b, v); err != nil {
				return nil, fmt.Errorf("the tag %s: %w", t, err)
			}
		}
		b = append(b, ')')
	}
	b = append(b, " ("...)
	b = appendIdent(b, s.TimeColumn)
	for _, f := range p.Fields {
		if err := checkIdent(f.Name); err != nil {
			return nil, err
		}
		b = append(b, ", "...)
		b = appendIdent(b, f.Name)
	}
	b = append(b, ") VALUES ("...)
	b = strconv.AppendInt(b, s.prec.Stamp(p.Time), 10)
	for _, f := range p.Fields {
		b = append(b, ", "...)
		if b, err = appendLiteral(b, f.Value); err != nil {
			return nil, fmt.Errorf("the field %s: %w", f.Name, err)
		}
	}
	return append(b, ')'), nil
}

// subtableOf names r's subtable: subtable's template, or t_ and the MD5 of
// the super table and the tags.
func (s *Sink) subtableOf(r *fitsync.Record, row fitsync.Value, p *syncinflux.Point) (string, error) {
	if s.subtable != nil {
		sub, err := tmpl.Expand(s.subtable, r, row)
		if err != nil {
			return "", fmt.Errorf("subtable: %w", err)
		}
		return sub, nil
	}
	h := md5.New()
	h.Write([]byte(p.Measurement))
	for _, t := range p.Tags {
		fmt.Fprintf(h, "\x00%s=%s", t.Key, t.Value)
	}
	return "t_" + hex.EncodeToString(h.Sum(nil)), nil
}

func appendIdent(b []byte, s string) []byte {
	b = append(b, '`')
	b = append(b, s...)
	return append(b, '`')
}

func appendName(b []byte, db, table string) []byte {
	b = appendIdent(b, db)
	b = append(b, '.')
	return appendIdent(b, table)
}

// appendLiteral appends v as an SQL literal: NULL, numbers and bools as
// such, a decimal as its text, any other value a quoted string of its text
// (a time in RFC 3339, bytes in base64, maps and arrays as JSON).
func appendLiteral(b []byte, v fitsync.Value) ([]byte, error) {
	switch v.Kind() {
	case fitsync.KindNull:
		return append(b, "NULL"...), nil
	case fitsync.KindBool:
		return strconv.AppendBool(b, v.AsBool()), nil
	case fitsync.KindInt:
		return strconv.AppendInt(b, v.AsInt(), 10), nil
	case fitsync.KindUint:
		return strconv.AppendUint(b, v.AsUint(), 10), nil
	case fitsync.KindFloat:
		f := v.AsFloat()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("%v is not a number TDengine takes", f)
		}
		return strconv.AppendFloat(b, f, 'g', -1, 64), nil
	case fitsync.KindDecimal:
		if _, err := strconv.ParseFloat(v.Text(), 64); err != nil {
			return nil, fmt.Errorf("the decimal %s: %w", v.Text(), err)
		}
		return append(b, v.Text()...), nil
	}
	s := fitsync.TextOf(v)
	if v.Kind() == fitsync.KindBytes {
		s = base64.StdEncoding.EncodeToString([]byte(s))
	}
	b = append(b, '\'')
	for i := range len(s) {
		switch c := s[i]; c {
		case '\'', '\\':
			b = append(b, '\\', c)
		case 0:
			return nil, errors.New("a string with NUL")
		default:
			b = append(b, c)
		}
	}
	return append(b, '\''), nil
}

// send writes parts in requests of at most max_request_bytes; nil parts
// are records left out.
func (s *Sink) send(ctx context.Context, parts [][]byte) error {
	var body []byte
	flush := func() error {
		if len(body) == 0 {
			return nil
		}
		err := s.post(ctx, body)
		body = body[:0]
		return err
	}
	for _, p := range parts {
		if p == nil {
			continue
		}
		if len(body) > 0 && len(body)+len(p) > s.maxRequest() {
			if err := flush(); err != nil {
				return err
			}
		}
		if s.Mode == "sql" && len(body) == 0 {
			body = append(body, insertHead...)
		}
		body = append(body, p...)
	}
	return flush()
}

// post sends one request.
func (s *Sink) post(ctx context.Context, body []byte) error {
	r := fibrest.Request{Method: stdhttp.MethodPost, Path: "/influxdb/v1/write", Query: s.query, Body: body,
		Header: stdhttp.Header{"Content-Type": {"text/plain; charset=utf-8"}}}
	if s.Mode == "sql" {
		r.Path, r.Query = "/rest/sql", ""
	}
	resp, err := s.do(ctx, r)
	if err != nil {
		return err
	}
	return s.answer("the write", resp)
}

// do sends r, again while the server answers 408, 429 or 5xx with no
// error of TDengine's that sending again does not cure.
func (s *Sink) do(ctx context.Context, r fibrest.Request) (*fibrest.Response, error) {
	r.Retry = func(resp *fibrest.Response) bool {
		code, _ := errorOf(resp)
		return fibrest.Transient(resp.Status) && !(code > 0 && refused(code))
	}
	return s.client.DoRetry(ctx, r, 0)
}

// answer turns an answer into the write's error: nil for success, a
// refusal for an error of what was sent.
func (s *Sink) answer(what string, resp *fibrest.Response) error {
	code, msg := errorOf(resp)
	switch {
	case code == 0 && resp.Status >= 200 && resp.Status < 300:
		return nil
	case code > 0 && refused(code):
		return fibrest.Refused(fmt.Errorf("%s: TDengine refused it (0x%x): %s", what, code, msg))
	case code > 0:
		return fmt.Errorf("%s: TDengine answered 0x%x: %s", what, code, msg)
	}
	return fibrest.StatusError(what, resp)
}

var codeInMessage = regexp.MustCompile(`^\[0x([0-9a-fA-F]+)\]\s*(.*)`)

// errorOf reads TDengine's error code from an answer: 0 for none, -1 when
// the answer is not TDengine's. /rest/sql answers 200 with {"code": n,
// "desc": ...}; the schemaless endpoint 500 with {"message": "[0xn] ..."}.
func errorOf(resp *fibrest.Response) (int, string) {
	var b struct {
		Code    json.RawMessage `json:"code"`
		Desc    string          `json:"desc"`
		Message string          `json:"message"`
	}
	if len(resp.Body) == 0 {
		if resp.Status >= 200 && resp.Status < 300 {
			return 0, ""
		}
		return -1, ""
	}
	if json.Unmarshal(resp.Body, &b) != nil {
		return -1, ""
	}
	if n, err := strconv.Atoi(string(b.Code)); err == nil {
		return n, b.Desc
	}
	if m := codeInMessage.FindStringSubmatch(b.Message); m != nil {
		n, _ := strconv.ParseInt(m[1], 16, 32)
		return int(n), m[2]
	}
	return -1, ""
}

// refused reports whether TDengine's error code is about what was sent,
// which sending it again does not cure: SQL syntax and values (0x216,
// 0x2600 to 0x26ff but a table or database that does not exist), and
// schemaless lines (0x3000 to 0x3fff).
func refused(code int) bool {
	switch {
	case code == 0x216:
		return true
	case code == 0x2603, code == 0x2662:
		return false
	case code >= 0x2600 && code <= 0x26ff, code >= 0x3000 && code <= 0x3fff:
		return true
	}
	return false
}

// Test runs SELECT SERVER_VERSION() in the database, with the sink's
// credentials: the server answers, the user is taken and the database is
// there. It writes nothing.
func (s *Sink) Test(ctx context.Context) error {
	resp, err := s.client.Do(ctx, fibrest.Request{Method: stdhttp.MethodPost, Path: "/rest/sql/" + url.PathEscape(s.Database),
		Body: []byte("SELECT SERVER_VERSION()"), Header: stdhttp.Header{"Content-Type": {"text/plain; charset=utf-8"}}})
	if err != nil {
		return err
	}
	return s.answer("SELECT SERVER_VERSION()", resp)
}

var (
	_ fitsync.ParallelSink = (*Sink)(nil)
	_ fitsync.Tester       = (*Sink)(nil)
	_ module.Cleaner       = (*Sink)(nil)
)
