package syncclickhouse

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
)

func init() {
	module.Register(module.Info{ID: fitsync.Sinks + ".clickhouse", New: func() any { return new(Sink) },
		Doc: "Writes records to ClickHouse over its HTTP interface: each run of a batch's records for one table is an INSERT of JSONEachRow rows with an insert_deduplication_token made from the run's first and last positions, so a batch written again after a crash is dropped by the server. Inserts and updates are the row, with a version column; a delete is a row of the key (or the row as it was) with the version and a deleted flag set to 1, so that a ReplacingMergeTree(version, deleted) table read with FINAL holds each key's last state. deletes: delete writes them as lightweight DELETEs instead. A row the server cannot parse fails its record, which on_error takes, the rows before it written."})
}

// The defaults of the sink's params.
const (
	DefaultMaxRequestBytes = 8 << 20
	DefaultVersionColumn   = "_version"
	DefaultDeletedColumn   = "_deleted"
	// maxDeleteQuery bounds a lightweight DELETE's text, below the server's
	// max_query_size of 256 KiB.
	maxDeleteQuery = 128 << 10
)

// Sink is the sink clickhouse.
type Sink struct {
	URLs            []string          `json:"urls" fit:"required" doc:"The server's HTTP interface, as http://ch1:8123 or https://ch1:8443, each a server of the cluster or a load balancer: the one that answered last is used, and the next in turn when it fails to (a connection's error, a timeout, a 502, 503 or 504). fib's client does not follow redirects."`
	Username        string            `json:"username,omitempty" doc:"The user, sent with basic authentication; the server's default user when left out."`
	PasswordEnv     string            `json:"password_env,omitempty" doc:"The environment variable holding username's password, read when the pipeline loads; none when left out."`
	Database        string            `json:"database,omitempty" doc:"The database of a table named without one; the user's default when left out."`
	CAFile          string            `json:"ca_file,omitempty" doc:"A PEM file of the certificates the server's is checked against, in place of the system's roots, for a CA of its own. The check cannot be turned off."`
	CertFile        string            `json:"cert_file,omitempty" doc:"A PEM file of a client certificate, and the chain after it, presented to a server that asks for one; needs key_file."`
	KeyFile         string            `json:"key_file,omitempty" doc:"The PEM file of cert_file's private key."`
	ServerName      string            `json:"server_name,omitempty" doc:"The name the server's certificate must have, and the TLS server name sent; each URL's host when left out."`
	Timeout         config.Duration   `json:"timeout,omitempty" default:"\"60s\"" doc:"How long one request may take, from connecting to the end of its answer. 60s when left out or 0."`
	Settings        map[string]string `json:"settings,omitempty" doc:"ClickHouse settings sent with every query, the name to the value, as {\"async_insert\": \"1\"}; they cannot be query, database, insert_deduplication_token, insert_deduplicate or a param_."`
	Table           string            `json:"table,omitempty" default:"\"{namespace}\"" doc:"The table a record goes to: text with {namespace}, the record's namespace; db.table names the database too. The table is not made: it is the user's, a ReplacingMergeTree(_version, _deleted) ordered by the key for updates and deletes, with non_replicated_deduplication_window set unless it is replicated, so that the deduplication token works. {namespace} when left out."`
	Plain           bool              `json:"plain,omitempty" doc:"Write rows as they are, without the version and deleted columns, as for a log's MergeTree; deletes must then be delete or ignore. false when left out."`
	VersionColumn   string            `json:"version_column,omitempty" default:"\"_version\"" doc:"The UInt64 column each row's version goes in, which a ReplacingMergeTree keeps the greatest of: the record's commit time in Unix nanoseconds, 0 when it has none (a snapshot's rows, which any change then outranks), or version_field's value. _version when left out."`
	VersionField    string            `json:"version_field,omitempty" doc:"A field of the row (an int, a uint, or a time, in Unix nanoseconds) whose value is the version, for a source whose commit times can tie for one key, as a binlog's seconds; a record without it fails. The commit time when left out, or, when the source gives none, 0: a snapshot's rows never outrank a change, and rows of one version are kept as the last written."`
	DeletedColumn   string            `json:"deleted_column,omitempty" default:"\"_deleted\"" doc:"The UInt8 column set to 1 for a delete and 0 otherwise, a ReplacingMergeTree's is_deleted. _deleted when left out."`
	Deletes         string            `json:"deletes,omitempty" enum:"flag,delete,ignore" default:"\"flag\"" doc:"How deletes are written: flag, as a row with the deleted column set to 1; delete, as a lightweight DELETE of the key's rows, run in order between the inserts, which are then not deduplicated (a DELETE written again would remove a row a deduplicated insert would not bring back), so a replay stores rows again and the table is best a ReplacingMergeTree; ignore, not at all. flag when left out."`
	MaxRequestBytes int64             `json:"max_request_bytes,omitempty" min:"0" default:"8388608" doc:"The most bytes of one INSERT's rows; a batch larger is sent in several, in order, each with a token of its own, and a row larger fails its record. 8 MiB when left out or 0."`

	conn  *Conn
	table []tablePart
}

type tablePart struct {
	text      string
	namespace bool
}

var settingName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// checkSettings checks a settings param.
func checkSettings(ctx *module.Context, settings map[string]string) config.Errors {
	var errs config.Errors
	for k := range settings {
		switch {
		case !settingName.MatchString(k):
			errs = append(errs, ctx.Params.Field("settings").Errorf("%q is not a setting's name", k))
		case k == "query" || k == "database" || k == "insert_deduplication_token" || k == "insert_deduplicate" || strings.HasPrefix(k, "param_"):
			errs = append(errs, ctx.Params.Field("settings").Errorf("%s is the connector's to set", k))
		}
	}
	return errs
}

// Provision checks the params and reads the password and TLS files; nothing
// is sent until the first write.
func (s *Sink) Provision(ctx *module.Context) error {
	conn, errs := NewConn(ctx, ConnParams{URLs: s.URLs, Username: s.Username, PasswordEnv: s.PasswordEnv, Database: s.Database,
		CAFile: s.CAFile, CertFile: s.CertFile, KeyFile: s.KeyFile, ServerName: s.ServerName, Timeout: s.Timeout})
	s.conn = conn
	errs = append(errs, checkSettings(ctx, s.Settings)...)
	tmpl := s.Table
	if tmpl == "" {
		tmpl = "{namespace}"
	}
	rest := tmpl
	for rest != "" {
		i := strings.Index(rest, "{namespace}")
		if i < 0 {
			s.table = append(s.table, tablePart{text: rest})
			break
		}
		s.table = append(s.table, tablePart{text: rest[:i]}, tablePart{namespace: true})
		rest = rest[i+len("{namespace}"):]
	}
	if strings.ContainsAny(strings.ReplaceAll(tmpl, "{namespace}", ""), "{}") {
		errs = append(errs, ctx.Params.Field("table").Errorf("%q has a brace other than {namespace}'s", tmpl))
	}
	if s.VersionColumn == "" {
		s.VersionColumn = DefaultVersionColumn
	}
	if s.DeletedColumn == "" {
		s.DeletedColumn = DefaultDeletedColumn
	}
	if s.Deletes == "" {
		s.Deletes = "flag"
	}
	if s.Plain && s.Deletes == "flag" {
		errs = append(errs, ctx.Params.Field("deletes").Errorf("plain rows have no deleted column: deletes must be delete or ignore"))
	}
	if s.Plain && s.VersionField != "" {
		errs = append(errs, ctx.Params.Field("version_field").Errorf("plain rows have no version"))
	}
	if len(errs) == 0 {
		return nil
	}
	return errs
}

// ParallelWrites marks the sink as one a job may write through several
// writers at once: a key goes to one writer, and the server takes inserts
// at once.
func (s *Sink) ParallelWrites() {}

// Test runs SELECT 1 with the sink's credentials and database. It writes
// nothing, so whether they may insert into the tables is not known before
// the first batch.
func (s *Sink) Test(ctx context.Context) error { return s.conn.Test(ctx) }

// Cleanup closes the client.
func (s *Sink) Cleanup() {
	if s.conn != nil {
		s.conn.Close()
	}
}

func (s *Sink) tableOf(r *fitsync.Record) string {
	var b strings.Builder
	for _, p := range s.table {
		if p.namespace {
			b.WriteString(r.Namespace)
		} else {
			b.WriteString(p.text)
		}
	}
	return b.String()
}

func (s *Sink) maxRequest() int64 {
	if s.MaxRequestBytes <= 0 {
		return DefaultMaxRequestBytes
	}
	return s.MaxRequestBytes
}

// item is a record as the sink writes it.
type item struct {
	table string
	// row is a JSONEachRow line, ending in a newline; cond a lightweight
	// DELETE's condition on the key. Neither for a record not written.
	row, cond []byte
	skip      bool
	err       error
}

// build makes r's item.
func (s *Sink) build(r *fitsync.Record) item {
	switch r.Op {
	case fitsync.OpDDL:
		return item{skip: true}
	case fitsync.OpInsert, fitsync.OpUpdate, fitsync.OpMessage:
	case fitsync.OpDelete:
		switch s.Deletes {
		case "ignore":
			return item{skip: true}
		case "delete":
			cond, err := keyCondition(r.Key)
			if err != nil {
				return item{err: fitsync.Permanent(err)}
			}
			return item{table: s.tableOf(r), cond: cond}
		}
	default:
		return item{err: fitsync.Permanent(fmt.Errorf("a record of operation %s", r.Op))}
	}
	it := item{table: s.tableOf(r)}
	if it.table == "" {
		return item{err: fitsync.Permanent(errors.New("the record's table is empty"))}
	}
	row := r.After
	if row.IsNull() || r.Op == fitsync.OpDelete {
		row = r.Before
	}
	if row.IsNull() && r.Op == fitsync.OpDelete {
		row = r.Key
	}
	if row.Kind() != fitsync.KindMap {
		what := "row"
		if r.Op == fitsync.OpDelete {
			what = "row before, or key,"
		}
		return item{err: fitsync.Permanent(fmt.Errorf("a %s's %s is a %s, not a map of columns", r.Op, what, row.Kind()))}
	}
	if s.Plain {
		b, err := fitsync.AppendPlain(nil, row)
		if err != nil {
			return item{err: fitsync.Permanent(err)}
		}
		it.row = append(b, '\n')
	} else {
		version, err := s.version(r, row)
		if err != nil {
			return item{err: fitsync.Permanent(err)}
		}
		deleted := int64(0)
		if r.Op == fitsync.OpDelete {
			deleted = 1
		}
		fields := make([]fitsync.Field, 0, len(row.Fields())+2)
		for _, f := range row.Fields() {
			if f.Name != s.VersionColumn && f.Name != s.DeletedColumn {
				fields = append(fields, f)
			}
		}
		fields = append(fields, fitsync.Field{Name: s.VersionColumn, Value: fitsync.Uint(version)},
			fitsync.Field{Name: s.DeletedColumn, Value: fitsync.Int(deleted)})
		b, err := fitsync.AppendPlain(nil, fitsync.Map(fields...))
		if err != nil {
			return item{err: fitsync.Permanent(err)}
		}
		it.row = append(b, '\n')
	}
	if int64(len(it.row)) > s.maxRequest() {
		return item{err: fitsync.Permanent(fmt.Errorf("the row is %d bytes, more than max_request_bytes", len(it.row)))}
	}
	return it
}

// version returns a row's version: version_field's value, or the commit
// time in Unix nanoseconds, or 0 when the source gives none. A snapshot's
// rows have none: versioned 0, they never outrank a change, which the
// stream after the snapshot, replayed over it after a restart, then wins
// with; rows of one version are kept as the last written, as
// ReplacingMergeTree keeps them, so that a source of no commit times at
// all is written as it comes.
func (s *Sink) version(r *fitsync.Record, row fitsync.Value) (uint64, error) {
	if s.VersionField == "" {
		if r.CommitTime.IsZero() {
			return 0, nil
		}
		return uint64(max(r.CommitTime.UnixNano(), 0)), nil
	}
	v, ok := row.Get(s.VersionField)
	if !ok || v.IsNull() {
		v, ok = r.Key.Get(s.VersionField)
	}
	switch {
	case !ok || v.IsNull():
		return 0, fmt.Errorf("the record has no %s to version it by", s.VersionField)
	case v.Kind() == fitsync.KindUint:
		return v.AsUint(), nil
	case v.Kind() == fitsync.KindInt && v.AsInt() >= 0:
		return uint64(v.AsInt()), nil
	case v.Kind() == fitsync.KindTime || v.Kind() == fitsync.KindDate:
		return uint64(max(v.AsTime().UnixNano(), 0)), nil
	}
	return 0, fmt.Errorf("%s is a %s, not a uint, a non-negative int or a time to version a row by", s.VersionField, v.Kind())
}

// keyCondition returns a lightweight DELETE's condition on key: each field
// equal to its value, joined by AND.
func keyCondition(key fitsync.Value) ([]byte, error) {
	if key.Kind() != fitsync.KindMap || len(key.Fields()) == 0 {
		return nil, fmt.Errorf("a delete's key is a %s, not a map of columns to delete by", key.Kind())
	}
	var b []byte
	b = append(b, '(')
	for i, f := range key.Fields() {
		if i > 0 {
			b = append(b, " AND "...)
		}
		b = append(b, quoteIdent(f.Name)...)
		if f.Value.IsNull() {
			b = append(b, " IS NULL"...)
			continue
		}
		lit, err := Literal(f.Value)
		if err != nil {
			return nil, err
		}
		b = append(b, " = "...)
		b = append(b, lit...)
	}
	return append(b, ')'), nil
}

// Literal returns v as a SQL literal ClickHouse compares a column with:
// numbers bare, strings and bytes quoted, a time by
// parseDateTime64BestEffort, a date by toDate, other values as their plain
// JSON quoted.
func Literal(v fitsync.Value) (string, error) {
	switch v.Kind() {
	case fitsync.KindNull:
		return "NULL", nil
	case fitsync.KindBool, fitsync.KindInt, fitsync.KindUint, fitsync.KindDecimal:
		return fitsync.TextOf(v), nil
	case fitsync.KindFloat:
		f := v.AsFloat()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return "", fmt.Errorf("%v is no key to delete by", f)
		}
		return strconv.FormatFloat(f, 'g', -1, 64), nil
	case fitsync.KindString, fitsync.KindBytes:
		return quoteString(v.Text()), nil
	case fitsync.KindTime:
		return "parseDateTime64BestEffort(" + quoteString(v.AsTime().Format(time.RFC3339Nano)) + ", 9)", nil
	case fitsync.KindDate:
		return "toDate(" + quoteString(v.AsTime().Format(time.DateOnly)) + ")", nil
	}
	b, err := fitsync.AppendPlain(nil, v)
	if err != nil {
		return "", err
	}
	return quoteString(string(b)), nil
}

// Write writes the batch in order: each run of records for one table an
// INSERT, with a deduplication token from its first and last positions,
// and each run of lightweight deletes a DELETE. It returns nil once every
// record is written, or a BatchError at the first one that is not: those
// before it are written. A row the server cannot parse is a Permanent
// error at that row, the rows before it in its INSERT written again
// without it; any other failure is the run's first record's, retried by
// the pipeline.
func (s *Sink) Write(ctx context.Context, batch []*fitsync.Record) error {
	items := make([]item, len(batch))
	for i, r := range batch {
		items[i] = s.build(r)
	}
	limit := s.maxRequest()
	for i := 0; i < len(items); {
		it := items[i]
		switch {
		case it.err != nil:
			return &fitsync.BatchError{Index: i, Err: it.err}
		case it.skip:
			i++
			continue
		}
		j, size := i, 0
		for j < len(items) && items[j].err == nil && !items[j].skip && items[j].table == it.table && (items[j].cond != nil) == (it.cond != nil) {
			n := len(items[j].row) + len(items[j].cond) + 4
			if j > i && (it.cond == nil && int64(size+n) > limit || it.cond != nil && size+n > maxDeleteQuery) {
				break
			}
			size += n
			j++
		}
		if it.cond != nil {
			if err := s.delete(ctx, it.table, items[i:j]); err != nil {
				return &fitsync.BatchError{Index: i, Err: err}
			}
			i = j
			continue
		}
		if err := s.insert(ctx, it.table, batch[i:j], items[i:j]); err != nil {
			var se *ServerError
			if errors.As(err, &se) && se.DataError() {
				bad := se.Row() - 1
				if bad > 0 && bad < j-i {
					// The rows before the bad one again, as an INSERT of
					// their own.
					if err := s.insert(ctx, it.table, batch[i:i+bad], items[i:i+bad]); err != nil {
						return &fitsync.BatchError{Index: i, Err: err}
					}
				} else {
					bad = 0
				}
				return &fitsync.BatchError{Index: i + bad, Err: fitsync.Permanent(err)}
			}
			return &fitsync.BatchError{Index: i, Err: err}
		}
		i = j
	}
	return nil
}

// insert sends one INSERT of items' rows.
func (s *Sink) insert(ctx context.Context, table string, recs []*fitsync.Record, items []item) error {
	q := s.query("INSERT INTO " + QuoteName(table) + " FORMAT JSONEachRow")
	q.Set("date_time_input_format", "best_effort")
	if s.Deletes == "delete" {
		// A DELETE written again would remove the rows inserted after it,
		// which a deduplicated insert would not bring back.
		q.Set("insert_deduplicate", "0")
	} else if token := DedupToken(table, recs); token != "" {
		q.Set("insert_deduplication_token", token)
	}
	var body []byte
	for _, it := range items {
		body = append(body, it.row...)
	}
	_, err := s.conn.Do(ctx, q, body)
	return err
}

// delete sends one lightweight DELETE of items' keys.
func (s *Sink) delete(ctx context.Context, table string, items []item) error {
	var b []byte
	b = append(b, "DELETE FROM "...)
	b = append(b, QuoteName(table)...)
	b = append(b, " WHERE "...)
	for k, it := range items {
		if k > 0 {
			b = append(b, " OR "...)
		}
		b = append(b, it.cond...)
	}
	_, err := s.conn.Do(ctx, s.query(""), b)
	return err
}

func (s *Sink) query(sql string) url.Values {
	q := url.Values{}
	for k, v := range s.Settings {
		q.Set(k, v)
	}
	if sql != "" {
		q.Set("query", sql)
	}
	return q
}

// DedupToken returns the insert_deduplication_token of an INSERT of recs
// into table: a hash of the table, the first and the last record's
// positions and the count, so that the same records written again, after a
// crash or a retry, make the same token and the server drops them. A record
// without a position is placed by the nearest one before it with one, and
// the records between (or after it, for those before any). None when no
// record has a position.
func DedupToken(table string, recs []*fitsync.Record) string {
	first, last := anchor(recs, 0), anchor(recs, len(recs)-1)
	if first == "" || last == "" {
		return ""
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%d", table, first, last, len(recs))
	return "fit-" + hex.EncodeToString(h.Sum(nil)[:20])
}

func anchor(recs []*fitsync.Record, i int) string {
	for k := i; k >= 0; k-- {
		if len(recs[k].Position) > 0 {
			return base64.RawStdEncoding.EncodeToString(recs[k].Position) + "+" + strconv.Itoa(i-k)
		}
	}
	for k := i + 1; k < len(recs); k++ {
		if len(recs[k].Position) > 0 {
			return base64.RawStdEncoding.EncodeToString(recs[k].Position) + "-" + strconv.Itoa(k-i)
		}
	}
	return ""
}
