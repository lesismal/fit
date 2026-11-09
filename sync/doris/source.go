package syncdoris

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	syncmysql "github.com/lesismal/fit/sync/mysql"
	mysqlwire "github.com/lesismal/fit/sync/mysql/wire"
)

func init() {
	module.Register(module.Info{ID: fitsync.Sources + ".doris", New: func() any { return new(Source) },
		Doc: "Reads an Apache Doris or StarRocks table, or a query's rows, over MySQL's protocol (a frontend's query port): a snapshot in pages ordered by the cursor column and the key, after the last row read, then, with a cursor, polling for rows past it every poll_interval. Its position is the cursor's and the key's values of the last row read, so a restart resumes after it, rows with the same cursor value included. Every row is an insert, or a delete when deleted_column says so; a row deleted from the table is not seen."})
}

// The defaults of the source's params.
const (
	DefaultAddress      = "127.0.0.1:9030"
	DefaultPageRows     = 10000
	DefaultPollInterval = 5 * time.Second
)

// Source is the source doris.
type Source struct {
	Address       string          `json:"address,omitempty" default:"\"127.0.0.1:9030\"" doc:"A frontend's MySQL query port, host and port: Doris's and StarRocks's query_port. 127.0.0.1:9030 when left out."`
	Username      string          `json:"username" fit:"required" doc:"The user to read as, which needs SELECT on the table, or on what the query reads."`
	PasswordEnv   string          `json:"password_env,omitempty" doc:"The environment variable holding username's password, read when the pipeline loads; none when left out."`
	Database      string          `json:"database,omitempty" doc:"The connection's default database, of a table named without one and of the query's. None when left out."`
	TLS           bool            `json:"tls,omitempty" doc:"Connect over TLS, required: a server that does not offer it is refused. The server's certificate is checked against the system's roots or ca_file's; the check cannot be turned off."`
	CAFile        string          `json:"ca_file,omitempty" doc:"A PEM file of the certificates the server's is checked against, in place of the system's roots, for a CA of its own; needs tls."`
	CertFile      string          `json:"cert_file,omitempty" doc:"A PEM file of a client certificate, and the chain after it, presented to a server that asks for one; needs tls and key_file."`
	KeyFile       string          `json:"key_file,omitempty" doc:"The PEM file of cert_file's private key."`
	ServerName    string          `json:"server_name,omitempty" doc:"The name the server's certificate must have, and the TLS server name sent; address's host when left out."`
	Timeout       config.Duration `json:"timeout,omitempty" default:"\"60s\"" doc:"How long connecting, and each query, may take. 60s when left out or 0."`
	Table         string          `json:"table,omitempty" doc:"The table read, as db.table or a table of database. Either table or query."`
	Query         string          `json:"query,omitempty" doc:"A SELECT whose rows are read, as SELECT * FROM shop.orders WHERE region = 'eu': the source reads from it as a subquery, adding its own WHERE, ORDER BY and LIMIT, so it has the key's and the cursor's columns and no ORDER BY or LIMIT of its own. Either table or query."`
	Key           []string        `json:"key" fit:"required" doc:"The columns that identify a row, in order, never null: the records' key, and what rows are ordered by after the cursor, so that rows with the same cursor value are read once each."`
	Cursor        string          `json:"cursor,omitempty" doc:"A column whose value grows as rows are written or changed (an update time, a version), never null, polled by: rows past the position's are read every poll_interval. A row written later with a value at or below one already read is not seen. Without it the source reads the rows once by the key and ends."`
	DeletedColumn string          `json:"deleted_column,omitempty" doc:"A column (a boolean or a number) whose true or non-zero value makes the row a delete rather than an insert. None when left out."`
	Namespace     string          `json:"namespace,omitempty" doc:"The records' namespace. table when left out, or doris for a query."`
	Snapshot      string          `json:"snapshot,omitempty" enum:"initial,never" default:"\"initial\"" doc:"Where a pipeline without a checkpoint starts: initial, at the first row, reading what is there; never, after the greatest cursor value now there, reading only what comes after (needs cursor). initial when left out."`
	PageRows      int             `json:"page_rows,omitempty" min:"0" max:"1000000" default:"10000" doc:"How many rows one query reads. 10000 when left out or 0."`
	PollInterval  config.Duration `json:"poll_interval,omitempty" default:"\"5s\"" doc:"How long to wait, once the rows past the cursor are read, before asking again. 5s when left out or 0."`

	cfg   mysqlwire.Config
	dial  func(context.Context, mysqlwire.Config) (conn, error)
	inner string
	order []string
}

// conn is what the source asks of a connection: *mysqlwire.Conn through
// wireConn, or a fake in the tests.
type conn interface {
	// QueryAll runs a text query and returns its columns and rows, the
	// values copied.
	QueryAll(ctx context.Context, query string) ([]mysqlwire.Column, [][]any, error)
	Close() error
}

type wireConn struct{ *mysqlwire.Conn }

func (c wireConn) QueryAll(ctx context.Context, query string) ([]mysqlwire.Column, [][]any, error) {
	rows, err := c.Query(ctx, query)
	if err != nil {
		return nil, nil, err
	}
	var out [][]any
	for rows.Next() {
		vals := make([]any, len(rows.Values()))
		for i, v := range rows.Values() {
			if b, ok := v.([]byte); ok {
				v = bytes.Clone(b)
			}
			vals[i] = v
		}
		out = append(out, vals)
	}
	cols := append([]mysqlwire.Column(nil), rows.Columns()...)
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}
	return cols, out, nil
}

func dialWire(ctx context.Context, cfg mysqlwire.Config) (conn, error) {
	c, err := mysqlwire.Dial(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return wireConn{c}, nil
}

// position is the source's: the literals of the order's columns of the
// last row read; fewer than them (the cursor alone) when the snapshot
// started past the cursor's greatest value.
type position struct {
	After []string `json:"after"`
}

// Provision checks the params and reads the password and TLS files; nothing
// is sent until the source runs.
func (s *Source) Provision(ctx *module.Context) error {
	cfg, errs := syncmysql.WireConfig(ctx, syncmysql.ConnParams{Address: s.Address, Username: s.Username, PasswordEnv: s.PasswordEnv,
		Database: s.Database, TLS: s.TLS, CAFile: s.CAFile, CertFile: s.CertFile, KeyFile: s.KeyFile, ServerName: s.ServerName, Timeout: s.Timeout})
	if s.Address == "" {
		cfg.Addr = DefaultAddress
	}
	s.cfg = cfg
	if s.dial == nil {
		s.dial = dialWire
	}
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
			s.Namespace = "doris"
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return errs
}

// Test connects, runs SELECT 1, then the table or query with LIMIT 0, which
// reads no rows but needs it to be there and the right to select from it.
func (s *Source) Test(ctx context.Context) error {
	c, err := s.dial(ctx, s.cfg)
	if err != nil {
		return err
	}
	defer c.Close()
	if _, _, err := c.QueryAll(ctx, "SELECT 1"); err != nil {
		return err
	}
	_, _, err = c.QueryAll(ctx, "SELECT * FROM ("+s.inner+") AS `__fit` LIMIT 0")
	return err
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

// after returns the condition that a row's cols come after the literals
// lits, a prefix of cols when there are fewer of them: the tuple
// comparison spelled out, (a > x) OR (a = x AND b > y), as both systems
// take it.
func after(cols, lits []string) string {
	var or []string
	for i := range lits {
		var and []string
		for k := range i {
			and = append(and, quoteIdent(cols[k])+" = "+lits[k])
		}
		and = append(and, quoteIdent(cols[i])+" > "+lits[i])
		or = append(or, "("+strings.Join(and, " AND ")+")")
	}
	return strings.Join(or, " OR ")
}

// pageQuery is the query of a page of limit rows after the literals.
func (s *Source) pageQuery(lits []string, limit int) string {
	var b strings.Builder
	b.WriteString("SELECT * FROM (" + s.inner + ") AS `__fit`")
	if len(lits) > 0 {
		b.WriteString(" WHERE " + after(s.order, lits))
	}
	q := make([]string, len(s.order))
	for i, c := range s.order {
		q[i] = quoteIdent(c)
	}
	fmt.Fprintf(&b, " ORDER BY %s LIMIT %d", strings.Join(q, ", "), limit)
	return b.String()
}

// record makes a row's record, and the literals of its order's columns.
func (s *Source) record(cols []mysqlwire.Column, vals []any) (*fitsync.Record, []string, error) {
	fields := make([]fitsync.Field, len(vals))
	for i, v := range vals {
		if i >= len(cols) {
			return nil, nil, fmt.Errorf("doris: a row of %d values for %d columns", len(vals), len(cols))
		}
		fields[i] = fitsync.Field{Name: cols[i].Name, Value: syncmysql.WireValue(&cols[i], v)}
	}
	m := fitsync.Map(fields...)
	lits := make([]string, len(s.order))
	for i, c := range s.order {
		v, ok := m.Get(c)
		if !ok {
			return nil, nil, fmt.Errorf("doris: the rows have no column %s", c)
		}
		lit, err := literal(v)
		if err != nil {
			return nil, nil, fmt.Errorf("doris: the column %s: %w", c, err)
		}
		lits[i] = lit
	}
	key := make([]fitsync.Field, len(s.Key))
	for i, k := range s.Key {
		v, _ := m.Get(k)
		key[i] = fitsync.Field{Name: k, Value: v}
	}
	rec := &fitsync.Record{Source: "doris", Namespace: s.Namespace, Op: fitsync.OpInsert, Key: fitsync.Map(key...), After: m}
	if s.DeletedColumn != "" {
		if v, ok := m.Get(s.DeletedColumn); ok && truthy(v) {
			rec.Op, rec.Before, rec.After = fitsync.OpDelete, m, fitsync.Null()
		}
	}
	if s.Cursor != "" {
		if v, _ := m.Get(s.Cursor); v.Kind() == fitsync.KindTime {
			rec.CommitTime = v.AsTime()
		}
	}
	p, _ := json.Marshal(position{After: lits})
	rec.Position = p
	return rec, lits, nil
}

func truthy(v fitsync.Value) bool {
	switch v.Kind() {
	case fitsync.KindBool:
		return v.AsBool()
	case fitsync.KindInt:
		return v.AsInt() != 0
	case fitsync.KindUint:
		return v.AsUint() != 0
	case fitsync.KindString, fitsync.KindDecimal:
		f, err := strconv.ParseFloat(v.Text(), 64)
		return err == nil && f != 0 || strings.EqualFold(v.Text(), "true")
	}
	return false
}

func (s *Source) parsePosition(p fitsync.Position) ([]string, error) {
	var pos position
	if err := json.Unmarshal(p, &pos); err != nil {
		return nil, fmt.Errorf("doris: the position %s: %w", p, err)
	}
	if len(pos.After) > len(s.order) {
		return nil, fmt.Errorf("doris: the position %s has %d values, more than the %d columns the rows are ordered by: the cursor or the key changed", p, len(pos.After), len(s.order))
	}
	for _, l := range pos.After {
		if !checkLiteral(l) {
			return nil, fmt.Errorf("doris: the position %s holds %q, not a literal of the source's", p, l)
		}
	}
	return pos.After, nil
}

// Run reads the rows past the checkpoint's, in pages, and with a cursor
// polls for more until ctx ends; without one, or in a job, it ends at the
// last row.
func (s *Source) Run(ctx context.Context, r *fitsync.Run) error {
	var lits []string
	if r.From != nil {
		var err error
		if lits, err = s.parsePosition(r.From); err != nil {
			return err
		}
	}
	c, err := s.dial(ctx, s.cfg)
	if err != nil {
		return fmt.Errorf("doris: %w", err)
	}
	defer c.Close()
	if r.From == nil && s.Snapshot == "never" {
		if lits, err = s.greatestCursor(ctx, c); err != nil {
			return err
		}
	}
	limit := s.pageRows()
	for {
		cols, rows, err := c.QueryAll(ctx, s.pageQuery(lits, limit))
		if err != nil {
			return fmt.Errorf("doris: %w", err)
		}
		for _, row := range rows {
			rec, at, err := s.record(cols, row)
			if err != nil {
				return err
			}
			if err := r.Emit(rec); err != nil {
				return err
			}
			lits = at
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

// greatestCursor returns the literal of the greatest cursor value there
// is, or none when there are no rows.
func (s *Source) greatestCursor(ctx context.Context, c conn) ([]string, error) {
	cols, rows, err := c.QueryAll(ctx, "SELECT MAX("+quoteIdent(s.Cursor)+") FROM ("+s.inner+") AS `__fit`")
	if err != nil {
		return nil, fmt.Errorf("doris: the cursor's greatest value: %w", err)
	}
	if len(rows) != 1 || len(rows[0]) != 1 || len(cols) != 1 {
		return nil, fmt.Errorf("doris: the cursor's greatest value: %d rows", len(rows))
	}
	v := syncmysql.WireValue(&cols[0], rows[0][0])
	if v.IsNull() {
		return nil, nil
	}
	lit, err := literal(v)
	if err != nil {
		return nil, fmt.Errorf("doris: the cursor's greatest value: %w", err)
	}
	return []string{lit}, nil
}
