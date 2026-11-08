package syncclickhouse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
)

func init() {
	module.Register(module.Info{ID: fitsync.Sources + ".clickhouse", New: func() any { return new(Source) },
		Doc: "Reads a ClickHouse table, or a query's rows, over its HTTP interface: a snapshot in pages ordered by the cursor column and the key, after the last row read, then, with a cursor, polling for rows past it every poll_interval. Its position is the cursor's and the key's values of the last row read, so a restart resumes after it, rows with the same cursor value included. Every row is an insert, or a delete when deleted_column says so; a row deleted from ClickHouse is not seen. In a job (mode once) with chunk_rows, the snapshot is split into ranges of the key, read job.max_parallel at a time."})
}

// The defaults of the source's params.
const (
	DefaultPageRows     = 10000
	DefaultPollInterval = 5 * time.Second
)

// Source is the source clickhouse.
type Source struct {
	URLs          []string          `json:"urls" fit:"required" doc:"The server's HTTP interface, as http://ch1:8123 or https://ch1:8443, each a server of the cluster or a load balancer: the one that answered last is used, and the next in turn when it fails to (a connection's error, a timeout, a 502, 503 or 504)."`
	Username      string            `json:"username,omitempty" doc:"The user, sent with basic authentication; the server's default user when left out."`
	PasswordEnv   string            `json:"password_env,omitempty" doc:"The environment variable holding username's password, read when the pipeline loads; none when left out."`
	Database      string            `json:"database,omitempty" doc:"The database of a table named without one, and the query's; the user's default when left out."`
	CAFile        string            `json:"ca_file,omitempty" doc:"A PEM file of the certificates the server's is checked against, in place of the system's roots, for a CA of its own. The check cannot be turned off."`
	CertFile      string            `json:"cert_file,omitempty" doc:"A PEM file of a client certificate, and the chain after it, presented to a server that asks for one; needs key_file."`
	KeyFile       string            `json:"key_file,omitempty" doc:"The PEM file of cert_file's private key."`
	ServerName    string            `json:"server_name,omitempty" doc:"The name the server's certificate must have, and the TLS server name sent; each URL's host when left out."`
	Timeout       config.Duration   `json:"timeout,omitempty" default:"\"60s\"" doc:"How long one request may take, from connecting to the end of its answer. 60s when left out or 0."`
	Settings      map[string]string `json:"settings,omitempty" doc:"ClickHouse settings sent with every query, the name to the value, as {\"max_threads\": \"4\"}; they cannot be query, database, insert_deduplication_token, insert_deduplicate or a param_."`
	Table         string            `json:"table,omitempty" doc:"The table read, as db.table or a table of database. Either table or query."`
	Query         string            `json:"query,omitempty" doc:"A SELECT whose rows are read, as SELECT * FROM shop.orders FINAL WHERE region = 'eu': the source reads from it as a subquery, adding its own WHERE, ORDER BY and LIMIT, so it has the key's and the cursor's columns and no ORDER BY, LIMIT, FORMAT or SETTINGS of its own. Either table or query."`
	Key           []string          `json:"key" fit:"required" doc:"The columns that identify a row, in order, not Nullable: the records' key, and what rows are ordered by after the cursor, so that rows with the same cursor value are read once each."`
	Cursor        string            `json:"cursor,omitempty" doc:"A column whose value grows as rows are written or changed (an update time, a version), not Nullable, polled by: rows past the position's are read every poll_interval. A row written later with a value at or below one already read is not seen. Without it the source reads the rows once by the key and ends."`
	DeletedColumn string            `json:"deleted_column,omitempty" doc:"A column (a Bool or a number) whose true or non-zero value makes the row a delete rather than an insert, as the sink's _deleted. None when left out."`
	Namespace     string            `json:"namespace,omitempty" doc:"The records' namespace. table when left out, or clickhouse for a query."`
	Snapshot      string            `json:"snapshot,omitempty" enum:"initial,never" default:"\"initial\"" doc:"Where a pipeline without a checkpoint starts: initial, at the first row, reading what is there; never, after the greatest cursor value now there, reading only what comes after (needs cursor). initial when left out."`
	PageRows      int               `json:"page_rows,omitempty" min:"0" max:"1000000" default:"10000" doc:"How many rows one query reads. 10000 when left out or 0."`
	PollInterval  config.Duration   `json:"poll_interval,omitempty" default:"\"5s\"" doc:"How long to wait, once the rows past the cursor are read, before asking again. 5s when left out or 0."`
	ChunkRows     int64             `json:"chunk_rows,omitempty" min:"0" doc:"In a job (mode once), split the snapshot into ranges of the key of about this many rows, read job.max_parallel at a time and resumed by chunk; the next job starts from the beginning. Not split when left out, and the job reads by the cursor, the next job going on after it."`

	conn    *Conn
	inner   string
	order   []string
	cursorT bool // the cursor is a DateTime: it is the records' commit time
}

// bound is a column's value as the position keeps it: its type and its text
// as toString writes it, which a query parameter of the type reads back.
type bound struct {
	T string `json:"t"`
	V string `json:"v"`
}

// position is the source's: the values of the order's columns of the last
// row read; fewer than them (the cursor alone) when the snapshot started
// past the cursor's greatest value.
type position struct {
	After []bound `json:"after"`
}

// Provision checks the params and reads the password and TLS files; nothing
// is sent until the source runs.
func (s *Source) Provision(ctx *module.Context) error {
	conn, errs := NewConn(ctx, ConnParams{URLs: s.URLs, Username: s.Username, PasswordEnv: s.PasswordEnv, Database: s.Database,
		CAFile: s.CAFile, CertFile: s.CertFile, KeyFile: s.KeyFile, ServerName: s.ServerName, Timeout: s.Timeout})
	s.conn = conn
	errs = append(errs, checkSettings(ctx, s.Settings)...)
	switch {
	case (s.Table == "") == (s.Query == ""):
		errs = append(errs, ctx.Params.Field("table").Errorf("either table or query"))
	case s.Table != "":
		s.inner = "SELECT * FROM " + QuoteName(s.Table)
	default:
		s.inner = strings.TrimRight(strings.TrimSpace(s.Query), ";")
	}
	if len(s.Key) == 0 {
		errs = append(errs, ctx.Params.Field("key").Errorf("no key columns"))
	}
	if s.Cursor != "" {
		s.order = append(s.order, s.Cursor)
	}
	for _, k := range s.Key {
		if k == "" || k == s.Cursor {
			errs = append(errs, ctx.Params.Field("key").Errorf("%q is empty or the cursor", k))
		}
	}
	s.order = append(s.order, s.Key...)
	if s.Snapshot == "never" && s.Cursor == "" {
		errs = append(errs, ctx.Params.Field("snapshot").Errorf("never needs a cursor"))
	}
	if s.Namespace == "" {
		s.Namespace = s.Table
		if s.Namespace == "" {
			s.Namespace = "clickhouse"
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return errs
}

// Cleanup closes the client.
func (s *Source) Cleanup() {
	if s.conn != nil {
		s.conn.Close()
	}
}

func (s *Source) pageRows() int {
	if s.PageRows <= 0 {
		return DefaultPageRows
	}
	return s.PageRows
}

func (s *Source) pollInterval() time.Duration {
	if s.PollInterval <= 0 {
		return DefaultPollInterval
	}
	return s.PollInterval.Std()
}

func (s *Source) query(sql string) url.Values {
	q := url.Values{}
	for k, v := range s.Settings {
		q.Set(k, v)
	}
	for k, v := range readSettings {
		q.Set(k, v)
	}
	q.Set("query", sql)
	return q
}

// tuple returns cols, quoted, as a tuple, or the one column.
func tuple(cols []string) string {
	q := make([]string, len(cols))
	for i, c := range cols {
		q[i] = quoteIdent(c)
	}
	if len(q) == 1 {
		return q[0]
	}
	return "(" + strings.Join(q, ", ") + ")"
}

// compare adds to where cols' tuple compared by op with the bounds, as
// query parameters named from prefix: a prefix of cols when there are
// fewer bounds.
func compare(where *[]string, q url.Values, cols []string, op string, bs []bound, prefix string) {
	if len(bs) == 0 {
		return
	}
	ps := make([]string, len(bs))
	for i, b := range bs {
		name := prefix + strconv.Itoa(i)
		ps[i] = "{" + name + ":" + b.T + "}"
		q.Set("param_"+name, escapeParam(b.V))
	}
	p := ps[0]
	if len(ps) > 1 {
		p = "(" + strings.Join(ps, ", ") + ")"
	}
	*where = append(*where, tuple(cols[:len(bs)])+" "+op+" "+p)
}

// row is a row read: its columns, and the bounds of the columns it is
// ordered by.
type row struct {
	fields []fitsync.Field
	at     []bound
}

// page reads up to limit rows ordered by cols, after the bounds after, from
// lo on and before hi.
func (s *Source) page(ctx context.Context, cols []string, after, lo, hi []bound, limit int) ([]row, error) {
	var sql strings.Builder
	sql.WriteString("SELECT *")
	for i, c := range cols {
		fmt.Fprintf(&sql, ", toString(%s) AS `__fit_%d`", quoteIdent(c), i)
	}
	sql.WriteString(" FROM (" + s.inner + ") AS `__fit`")
	q := s.query("")
	var where []string
	compare(&where, q, cols, ">", after, "a")
	compare(&where, q, cols, ">=", lo, "l")
	compare(&where, q, cols, "<", hi, "h")
	if len(where) > 0 {
		sql.WriteString(" WHERE " + strings.Join(where, " AND "))
	}
	fmt.Fprintf(&sql, " ORDER BY %s LIMIT %d FORMAT JSONCompactEachRowWithNamesAndTypes", strings.Join(quoteAll(cols), ", "), limit)
	q.Set("query", sql.String())
	body, err := s.conn.Do(ctx, q, nil)
	if err != nil {
		return nil, err
	}
	return parseRows(body, cols)
}

func quoteAll(cols []string) []string {
	q := make([]string, len(cols))
	for i, c := range cols {
		q[i] = quoteIdent(c)
	}
	return q
}

// parseRows reads JSONCompactEachRowWithNamesAndTypes, the last len(cols)
// columns the order's toString.
func parseRows(body []byte, cols []string) ([]row, error) {
	lines := bytes.Split(bytes.TrimRight(body, "\n"), []byte("\n"))
	if len(lines) < 2 {
		return nil, fmt.Errorf("the answer has no names and types: %s", clip(body))
	}
	var names, types []string
	if err := errors.Join(json.Unmarshal(lines[0], &names), json.Unmarshal(lines[1], &types)); err != nil || len(names) != len(types) || len(names) < len(cols) {
		return nil, fmt.Errorf("the answer's names and types: %v: %s", err, clip(body))
	}
	n := len(names) - len(cols)
	colType := make([]string, len(cols))
	for i, c := range cols {
		k := indexOf(names[:n], c)
		if k < 0 {
			return nil, fmt.Errorf("the rows have no column %s", c)
		}
		colType[i] = unwrap(types[k])
	}
	var out []row
	for _, line := range lines[2:] {
		var vals []json.RawMessage
		if err := json.Unmarshal(line, &vals); err != nil || len(vals) != len(names) {
			return nil, fmt.Errorf("a row: %v: %s", err, clip(line))
		}
		r := row{fields: make([]fitsync.Field, n), at: make([]bound, len(cols))}
		for i := range n {
			v, err := ToValue(types[i], vals[i])
			if err != nil {
				return nil, fmt.Errorf("column %s: %w", names[i], err)
			}
			r.fields[i] = fitsync.Field{Name: names[i], Value: v}
		}
		for i := range cols {
			var text *string
			if err := json.Unmarshal(vals[n+i], &text); err != nil || text == nil {
				return nil, fmt.Errorf("column %s is null, or not text, in a row: it cannot order rows", cols[i])
			}
			r.at[i] = bound{T: colType[i], V: *text}
		}
		out = append(out, r)
	}
	return out, nil
}

func indexOf(xs []string, x string) int {
	for i, y := range xs {
		if y == x {
			return i
		}
	}
	return -1
}

// record makes a row's record, with its position.
func (s *Source) record(r row, at []bound) (*fitsync.Record, error) {
	m := fitsync.Map(r.fields...)
	key := make([]fitsync.Field, len(s.Key))
	for i, k := range s.Key {
		v, _ := m.Get(k)
		key[i] = fitsync.Field{Name: k, Value: v}
	}
	rec := &fitsync.Record{Source: "clickhouse", Namespace: s.Namespace, Op: fitsync.OpInsert, Key: fitsync.Map(key...), After: m}
	if s.DeletedColumn != "" {
		if v, ok := m.Get(s.DeletedColumn); ok && truthy(v) {
			rec.Op, rec.Before, rec.After = fitsync.OpDelete, m, fitsync.Null()
		}
	}
	if s.Cursor != "" {
		if v, ok := m.Get(s.Cursor); ok && v.Kind() == fitsync.KindTime {
			rec.CommitTime = v.AsTime()
		}
	}
	p, err := json.Marshal(position{After: at})
	if err != nil {
		return nil, err
	}
	rec.Position = p
	return rec, nil
}

func truthy(v fitsync.Value) bool {
	switch v.Kind() {
	case fitsync.KindBool:
		return v.AsBool()
	case fitsync.KindInt:
		return v.AsInt() != 0
	case fitsync.KindUint:
		return v.AsUint() != 0
	}
	return false
}

func parsePosition(p fitsync.Position, most int) ([]bound, error) {
	var pos position
	if err := json.Unmarshal(p, &pos); err != nil {
		return nil, fmt.Errorf("clickhouse: the position %s: %w", p, err)
	}
	if len(pos.After) > most {
		return nil, fmt.Errorf("clickhouse: the position %s has %d values, more than the %d columns the rows are ordered by: the cursor or the key changed", p, len(pos.After), most)
	}
	return pos.After, nil
}

// Run reads the rows past the checkpoint's, in pages, and with a cursor
// polls for more until ctx ends; without one, or in a job, it ends at the
// last row.
func (s *Source) Run(ctx context.Context, r *fitsync.Run) error {
	var after []bound
	if r.From != nil {
		var err error
		if after, err = parsePosition(r.From, len(s.order)); err != nil {
			return err
		}
	} else if s.Snapshot == "never" {
		var err error
		if after, err = s.greatestCursor(ctx); err != nil {
			return err
		}
	}
	limit := s.pageRows()
	for {
		rows, err := s.page(ctx, s.order, after, nil, nil, limit)
		if err != nil {
			return fmt.Errorf("clickhouse: %w", err)
		}
		for _, row := range rows {
			rec, err := s.record(row, row.at)
			if err != nil {
				return err
			}
			if err := r.Emit(rec); err != nil {
				return err
			}
			after = row.at
		}
		if len(rows) == limit {
			continue
		}
		if r.Once || s.Cursor == "" {
			return nil
		}
		t := time.NewTimer(s.pollInterval())
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		}
	}
}

// greatestCursor returns the bound past the greatest cursor value there
// is, or none when there are no rows.
func (s *Source) greatestCursor(ctx context.Context) ([]bound, error) {
	c := quoteIdent(s.Cursor)
	body, err := s.conn.Do(ctx, s.query(fmt.Sprintf("SELECT count(), toString(max(%s)), toTypeName(max(%s)) FROM (%s) FORMAT JSONCompactEachRow", c, c, s.inner)), nil)
	if err != nil {
		return nil, fmt.Errorf("clickhouse: the cursor's greatest value: %w", err)
	}
	var out []json.RawMessage
	if err := json.Unmarshal(body, &out); err != nil || len(out) != 3 {
		return nil, fmt.Errorf("clickhouse: the cursor's greatest value: %v: %s", err, clip(body))
	}
	var n int64
	var v, t string
	if err := errors.Join(json.Unmarshal(out[0], &n), json.Unmarshal(out[1], &v), json.Unmarshal(out[2], &t)); err != nil {
		return nil, fmt.Errorf("clickhouse: the cursor's greatest value: %v: %s", err, clip(body))
	}
	if n == 0 {
		return nil, nil
	}
	return []bound{{T: unwrap(t), V: v}}, nil
}

// Chunks plans chunk_rows' chunks of the key, none without it: each
// chunk's Lo is the key of its first row, found by skipping chunk_rows rows
// from the one before's, a query each.
func (s *Source) Chunks(ctx context.Context) ([]fitsync.Chunk, error) {
	if s.ChunkRows <= 0 {
		return nil, nil
	}
	body, err := s.conn.Do(ctx, s.query("SELECT count() FROM ("+s.inner+") FORMAT JSONCompactEachRow"), nil)
	if err != nil {
		return nil, fmt.Errorf("clickhouse: counting the rows: %w", err)
	}
	var out []json.Number
	if err := json.Unmarshal(body, &out); err != nil || len(out) != 1 {
		return nil, fmt.Errorf("clickhouse: counting the rows: %v: %s", err, clip(body))
	}
	total, _ := out[0].Int64()
	if total == 0 {
		return nil, nil
	}
	if (total+s.ChunkRows-1)/s.ChunkRows > fitsync.MaxChunks {
		return nil, fmt.Errorf("clickhouse: %d rows in chunks of %d are more than %d chunks", total, s.ChunkRows, fitsync.MaxChunks)
	}
	var plan []fitsync.Chunk
	var lo []bound
	for left := total; ; {
		next, err := s.skip(ctx, lo)
		if err != nil {
			return nil, err
		}
		c := fitsync.Chunk{Namespace: s.Namespace, Rows: min(left, s.ChunkRows)}
		if lo != nil {
			c.Lo, _ = json.Marshal(lo)
		}
		if next != nil {
			c.Hi, _ = json.Marshal(next)
		}
		plan = append(plan, c)
		left -= c.Rows
		if next == nil {
			return plan, nil
		}
		if len(plan) >= fitsync.MaxChunks {
			return nil, fmt.Errorf("clickhouse: more than %d chunks: the table grows as it is planned", fitsync.MaxChunks)
		}
		lo = next
	}
}

// skip returns the key of the row chunk_rows rows past lo's, or none.
func (s *Source) skip(ctx context.Context, lo []bound) ([]bound, error) {
	var sql strings.Builder
	sql.WriteString("SELECT ")
	for i, k := range s.Key {
		if i > 0 {
			sql.WriteString(", ")
		}
		fmt.Fprintf(&sql, "toString(%s), toTypeName(%s)", quoteIdent(k), quoteIdent(k))
	}
	sql.WriteString(" FROM (" + s.inner + ") AS `__fit`")
	q := s.query("")
	var where []string
	compare(&where, q, s.Key, ">=", lo, "l")
	if len(where) > 0 {
		sql.WriteString(" WHERE " + strings.Join(where, " AND "))
	}
	fmt.Fprintf(&sql, " ORDER BY %s LIMIT 1 OFFSET %d FORMAT JSONCompactEachRow", strings.Join(quoteAll(s.Key), ", "), s.ChunkRows)
	q.Set("query", sql.String())
	body, err := s.conn.Do(ctx, q, nil)
	if err != nil {
		return nil, fmt.Errorf("clickhouse: planning the chunks: %w", err)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, nil
	}
	var out []string
	if err := json.Unmarshal(body, &out); err != nil || len(out) != 2*len(s.Key) {
		return nil, fmt.Errorf("clickhouse: planning the chunks: %v: %s", err, clip(body))
	}
	bs := make([]bound, len(s.Key))
	for i := range bs {
		bs[i] = bound{T: unwrap(out[2*i+1]), V: out[2*i]}
	}
	return bs, nil
}

// ReadChunk reads chunk c's rows by the key, from its Lo, or after from's,
// to before its Hi.
func (s *Source) ReadChunk(ctx context.Context, c fitsync.Chunk, from fitsync.Position, emit func(*fitsync.Record) error) error {
	var lo, hi, after []bound
	if err := errors.Join(unmarshalBounds(c.Lo, &lo), unmarshalBounds(c.Hi, &hi)); err != nil {
		return fmt.Errorf("clickhouse: the chunk %s-%s: %w", c.Lo, c.Hi, err)
	}
	if from != nil {
		var err error
		if after, err = parsePosition(from, len(s.Key)); err != nil {
			return err
		}
	}
	limit := s.pageRows()
	for {
		rows, err := s.page(ctx, s.Key, after, lo, hi, limit)
		if err != nil {
			return fmt.Errorf("clickhouse: %w", err)
		}
		for _, row := range rows {
			rec, err := s.record(row, row.at)
			if err != nil {
				return err
			}
			if err := emit(rec); err != nil {
				return err
			}
			after = row.at
		}
		if len(rows) < limit {
			return nil
		}
	}
}

func unmarshalBounds(b json.RawMessage, out *[]bound) error {
	if len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, out)
}
