package syncpgsink

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	pgwire "github.com/lesismal/fit/sync/postgres/wire"
)

func init() {
	module.Register(module.Info{ID: fitsync.Sinks + ".postgres", New: func() any { return new(Sink) },
		Doc: "Writes records to PostgreSQL 13 and later: inserts and updates as INSERT ... ON CONFLICT (key) DO UPDATE, deletes as DELETE by key, each run of a batch's records for one table and one set of columns a multi-row statement, a run of many upserts copied (COPY) into a temporary table and merged; each batch in one transaction, or, with transactions source, each source transaction in one. Writing the same records again leaves the same rows. A record the server refuses for good fails, and on_error takes it."})
}

// The defaults of the sink's params.
const (
	DefaultCopyMinRows      = 100
	DefaultMaxStatementRows = 1000
	// maxParams is the most parameters a statement may have.
	maxParams = 65535
)

// Sink is the sink postgres.
type Sink struct {
	Address          string          `json:"address,omitempty" default:"\"127.0.0.1:5432\"" doc:"The server's host and port. 127.0.0.1:5432 when left out."`
	User             string          `json:"user" fit:"required" doc:"The user to connect as, which needs INSERT, UPDATE and DELETE on the tables written, and TEMPORARY on the database for COPY."`
	PasswordEnv      string          `json:"password_env,omitempty" doc:"The environment variable holding user's password, read when the pipeline loads; none when left out. SCRAM-SHA-256 is used; MD5 and cleartext passwords are refused."`
	Database         string          `json:"database,omitempty" doc:"The database; the user's name when left out, as the server has it."`
	SSLMode          string          `json:"sslmode,omitempty" enum:"disable,require,verify-ca,verify-full" default:"\"verify-full\"" doc:"TLS: verify-full, the server's certificate and its name checked; verify-ca, the certificate only; require, TLS without a check; disable, none. There is no prefer: a connection does not fall back to plain text. verify-full when left out."`
	CAFile           string          `json:"ca_file,omitempty" doc:"A PEM file of the certificates the server's is checked against, in place of the system's roots, for a CA of its own."`
	CertFile         string          `json:"cert_file,omitempty" doc:"A PEM file of a client certificate, and the chain after it, presented to a server that asks for one; needs key_file."`
	KeyFile          string          `json:"key_file,omitempty" doc:"The PEM file of cert_file's private key."`
	ServerName       string          `json:"server_name,omitempty" doc:"The name the server's certificate must have, and the TLS server name sent; address's host when left out."`
	ChannelBinding   string          `json:"channel_binding,omitempty" enum:"prefer,require,disable" default:"\"prefer\"" doc:"SCRAM's channel binding to the TLS connection: prefer, when the server offers it; require, refusing a server that does not authenticate with it; disable. prefer when left out."`
	Timeout          config.Duration `json:"timeout,omitempty" default:"\"60s\"" doc:"How long connecting and writing one batch may take; a write that takes longer breaks its connection and is tried again. 60s when left out or 0."`
	Table            string          `json:"table,omitempty" default:"\"{namespace}\"" doc:"The table a record goes to: text with {namespace}, the record's namespace; split at its first dot into the schema and the table (shop.orders is the table orders of the schema shop), a name without a dot found by the search_path. The table is not made: it must have a primary key or unique index on the key's columns. {namespace} when left out."`
	KeyColumns       []string        `json:"key_columns,omitempty" doc:"The columns of the key the upserts conflict on and the deletes delete by, their values taken from the record's key, or from its row. The record's key's fields when left out, so that the key must then be a map of columns."`
	Transactions     string          `json:"transactions,omitempty" enum:"batch,source" default:"\"batch\"" doc:"What a transaction of the sink's holds: batch, each batch written; source, each of the source's transactions, its records held across batches until its last arrives, so that a reader never sees part of one. A transaction with a record the server refuses for good is then not written at all; on_error takes the record refused, or the first of the batch for one held. batch when left out."`
	Copy             string          `json:"copy,omitempty" enum:"auto,always,never" default:"\"auto\"" doc:"When a run of upserts is copied: COPY into a temporary table, then one INSERT ... SELECT ... ON CONFLICT DO UPDATE from it, faster for many rows, as a snapshot's: auto, a run of copy_min_rows or more; always; never. auto when left out."`
	CopyMinRows      int             `json:"copy_min_rows,omitempty" min:"0" default:"100" doc:"With copy auto, the fewest rows of a run that is copied. 100 when left out or 0."`
	MaxStatementRows int             `json:"max_statement_rows,omitempty" min:"0" max:"65535" default:"1000" doc:"The most rows of one INSERT or DELETE (and fewer when its parameters would pass PostgreSQL's 65535); a longer run is written in several. 1000 when left out or 0."`

	pool   pool
	table  []tablePart
	source bool

	// mu holds a write with transactions source, whose held records are the
	// sink's.
	mu sync.Mutex
	// held are the built records of a source transaction whose last record
	// has not come yet, acknowledged and not written.
	held []item
	// drop is a source transaction the server refused, whose records are
	// acknowledged without being written until its last.
	drop string

	copies atomic.Uint64
}

type tablePart struct {
	text      string
	namespace bool
}

// Provision checks the params and reads the password and TLS files; nothing
// is sent until the first write.
func (s *Sink) Provision(ctx *module.Context) error {
	cfg, errs := s.connConfig(ctx)
	s.pool.cfg = cfg
	tmpl := s.Table
	if tmpl == "" {
		tmpl = "{namespace}"
	}
	if strings.ContainsAny(strings.ReplaceAll(tmpl, "{namespace}", ""), "{}") {
		errs = append(errs, ctx.Params.Field("table").Errorf("%q has a brace other than {namespace}'s", tmpl))
	}
	for rest := tmpl; rest != ""; {
		i := strings.Index(rest, "{namespace}")
		if i < 0 {
			s.table = append(s.table, tablePart{text: rest})
			break
		}
		s.table = append(s.table, tablePart{text: rest[:i]}, tablePart{namespace: true})
		rest = rest[i+len("{namespace}"):]
	}
	seen := map[string]bool{}
	for _, k := range s.KeyColumns {
		switch {
		case k == "":
			errs = append(errs, ctx.Params.Field("key_columns").Errorf("a column without a name"))
		case seen[k]:
			errs = append(errs, ctx.Params.Field("key_columns").Errorf("%s is named twice", k))
		}
		seen[k] = true
	}
	s.source = s.Transactions == "source"
	if len(errs) == 0 {
		return nil
	}
	return errs
}

// ParallelWrites marks the sink as one a job may write through several
// writers at once, each on a connection of its own: a key goes to one
// writer. A job's records have no transactions, so transactions source
// holds nothing there.
func (s *Sink) ParallelWrites() {}

// Cleanup closes the connections.
func (s *Sink) Cleanup() { s.pool.close() }

// Test connects, authenticates and runs SELECT 1.
func (s *Sink) Test(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, s.timeout())
	defer cancel()
	c, err := s.pool.get(ctx)
	if err != nil {
		return err
	}
	_, err = c.SimpleQuery(ctx, "SELECT 1")
	s.pool.put(c)
	return err
}

func (s *Sink) timeout() time.Duration {
	if s.Timeout <= 0 {
		return DefaultTimeout
	}
	return s.Timeout.Std()
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

// item is a record as the sink writes it.
type item struct {
	rec *fitsync.Record
	// table is the table's quoted name; del says the record is a delete.
	table string
	del   bool
	// cols are an upsert's columns, quoted, and vals their values; keys
	// are the key's columns, quoted, and kvals its values, which are a
	// delete's parameters.
	cols, keys  []string
	vals, kvals []any
	// sig is what a statement's records share: the table, the operation
	// and the columns; id is the key's values, by which a statement holds
	// a key once.
	sig, id string
	// skip says the record is not written (DDL), dropped that it belongs
	// to a source transaction the server refused.
	skip, dropped bool
	err           error
}

// build makes r's item.
func (s *Sink) build(r *fitsync.Record) item {
	it := item{rec: r}
	switch r.Op {
	case fitsync.OpDDL:
		it.skip = true
		return it
	case fitsync.OpInsert, fitsync.OpUpdate, fitsync.OpMessage:
	case fitsync.OpDelete:
		it.del = true
	default:
		it.err = fmt.Errorf("a record of operation %s", r.Op)
		return it
	}
	name := s.tableOf(r)
	if name == "" || strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".") {
		it.err = fmt.Errorf("the record's table %q is not a table's name", name)
		return it
	}
	it.table = QuoteTable(name)
	row := r.After
	if it.del || row.IsNull() {
		row = r.Before
	}
	if !it.del && row.Kind() != fitsync.KindMap {
		it.err = fmt.Errorf("a %s's row is a %s, not a map of columns", r.Op, row.Kind())
		return it
	}
	if err := s.key(&it, r, row); err != nil {
		it.err = err
		return it
	}
	var id []byte
	for _, v := range it.kvals {
		id = fmt.Appendf(id, "%T:%v\x00", v, v)
	}
	it.id = string(id)
	if it.del {
		it.sig = it.table + "\x00D\x00" + strings.Join(it.keys, ",")
		return it
	}
	inRow := map[string]bool{}
	for _, f := range row.Fields() {
		p, err := Param(f.Value)
		if err != nil {
			it.err = fmt.Errorf("column %s: %w", f.Name, err)
			return it
		}
		q := pgwire.QuoteIdentifier(f.Name)
		if inRow[q] {
			it.err = fmt.Errorf("the row has the column %s twice", f.Name)
			return it
		}
		inRow[q] = true
		it.cols = append(it.cols, q)
		it.vals = append(it.vals, p)
	}
	for i, k := range it.keys {
		if !inRow[k] {
			it.cols = append(it.cols, k)
			it.vals = append(it.vals, it.kvals[i])
		}
	}
	it.sig = it.table + "\x00U\x00" + strings.Join(it.keys, ",") + "\x00" + strings.Join(it.cols, ",")
	return it
}

// key sets it's key columns and values: key_columns' from the record's key
// or from row, or the key's fields.
func (s *Sink) key(it *item, r *fitsync.Record, row fitsync.Value) error {
	add := func(name string, v fitsync.Value) error {
		p, err := Param(v)
		if err != nil {
			return fmt.Errorf("key column %s: %w", name, err)
		}
		it.keys = append(it.keys, pgwire.QuoteIdentifier(name))
		it.kvals = append(it.kvals, p)
		return nil
	}
	if len(s.KeyColumns) == 0 {
		if r.Key.Kind() != fitsync.KindMap || len(r.Key.Fields()) == 0 {
			return fmt.Errorf("the record's key is a %s, not a map of columns: set key_columns", r.Key.Kind())
		}
		for _, f := range r.Key.Fields() {
			if err := add(f.Name, f.Value); err != nil {
				return err
			}
		}
		return nil
	}
	for _, name := range s.KeyColumns {
		v, ok := r.Key.Get(name)
		if !ok && len(s.KeyColumns) == 1 && r.Key.Kind() != fitsync.KindMap && !r.Key.IsNull() {
			v, ok = r.Key, true
		}
		if !ok {
			v, ok = row.Get(name)
		}
		if !ok {
			return fmt.Errorf("the record has no key column %s, in its key or its row", name)
		}
		if err := add(name, v); err != nil {
			return err
		}
	}
	return nil
}

// Write writes the batch: with transactions batch in one transaction, or,
// when the server refuses a record for good, the records before it in one
// and a BatchError for it; with transactions source, each source
// transaction whole in one, those ending in the batch committed and the
// one still open held for the next.
func (s *Sink) Write(ctx context.Context, batch []*fitsync.Record) error {
	if s.source {
		s.mu.Lock()
		defer s.mu.Unlock()
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout())
	defer cancel()
	c, err := s.pool.get(ctx)
	if err != nil {
		return err
	}
	defer s.pool.put(c)
	items := make([]item, len(batch))
	for i, r := range batch {
		items[i] = s.build(r)
	}
	if s.source {
		return s.writeSource(ctx, c, items)
	}
	end := len(items)
	for i := range items {
		if items[i].err != nil {
			end = i
			break
		}
	}
	if bad, err := s.commit(ctx, c, items[:end], false); err != nil {
		if bad >= 0 {
			return &fitsync.BatchError{Index: bad, Err: fitsync.Permanent(err)}
		}
		return err
	}
	if end < len(items) {
		return &fitsync.BatchError{Index: end, Err: fitsync.Permanent(items[end].err)}
	}
	return nil
}

// writeSource writes items with transactions source.
func (s *Sink) writeSource(ctx context.Context, c *pgwire.Conn, items []item) error {
	held, drop := s.held, s.drop
	if len(items) > 0 {
		r := items[0].rec
		if len(held) > 0 && (r.Tx.First || r.Tx.ID == "" || r.Tx.ID != held[0].rec.Tx.ID) {
			// The held transaction's last record is not coming: the
			// pipeline went back to its checkpoint, or on_error took it.
			held = nil
		}
		for i := range items {
			r := items[i].rec
			if drop == "" || r.Tx.ID != drop || r.Tx.First {
				drop = ""
				break
			}
			items[i].dropped = true
			if r.Tx.Last {
				drop = ""
				break
			}
		}
	}
	nh := len(held)
	list := append(append(make([]item, 0, nh+len(items)), held...), items...)
	end := len(list)
	for i := nh; i < len(list); i++ {
		if list[i].err != nil && !list[i].dropped {
			end = i
			break
		}
	}
	cut := end
	if end < len(list) {
		cut = txStart(list, end)
	}
	// After the last record that ends a transaction.
	for cut > 0 && !boundary(list[cut-1]) {
		cut--
	}
	bad, err := s.commit(ctx, c, list[:cut], true)
	switch {
	case err != nil && bad < 0:
		return err
	case err != nil:
		s.held, s.drop = nil, ""
		idx := max(bad-nh, 0)
		return s.refuse(list, nh, idx, err)
	case end < len(list):
		s.held, s.drop = nil, ""
		return s.refuse(list, nh, end-nh, list[end].err)
	}
	s.held, s.drop = append([]item(nil), list[cut:]...), drop
	return nil
}

// refuse fails items[idx] (list[nh+idx]) for good, for err: its source
// transaction is not written, and its records after it are dropped.
func (s *Sink) refuse(list []item, nh, idx int, err error) error {
	r := list[nh+idx].rec
	if r.Tx.ID != "" {
		err = fmt.Errorf("%w; the source transaction %s is not written", err, r.Tx.ID)
		if !r.Tx.Last {
			s.drop = r.Tx.ID
		}
	}
	return &fitsync.BatchError{Index: idx, Err: fitsync.Permanent(err)}
}

// boundary reports whether a source transaction ends at it.
func boundary(it item) bool { return it.dropped || it.rec.Tx.ID == "" || it.rec.Tx.Last }

// txStart returns where the source transaction of list[i] starts in list.
func txStart(list []item, i int) int {
	id := list[i].rec.Tx.ID
	if id == "" {
		return i
	}
	for i > 0 && !list[i].rec.Tx.First && list[i-1].rec.Tx.ID == id {
		i--
	}
	return i
}

// commit writes items in one transaction. When the server refuses a
// record for good, it finds which, writing them one at a time, and commits
// those before it (with whole, those before its source transaction),
// returning its index and the server's error; bad is -1 for any other
// error, when nothing is written.
func (s *Sink) commit(ctx context.Context, c *pgwire.Conn, items []item, whole bool) (bad int, err error) {
	n := 0
	for _, it := range items {
		if !it.skip && !it.dropped {
			n++
		}
	}
	if n == 0 {
		return -1, nil
	}
	if err := s.exec(ctx, c, "BEGIN"); err != nil {
		return -1, err
	}
	if err := s.run(ctx, c, items); err != nil {
		s.rollback(ctx, c)
		if !Permanent(err) && !twice(err) {
			return -1, err
		}
		return s.oneByOne(ctx, c, items, whole)
	}
	return -1, s.exec(ctx, c, "COMMIT")
}

// oneByOne writes items one at a time in a transaction, each after a
// savepoint (with whole, each source transaction), to find the one the
// server refuses for good; what was written before its savepoint is
// committed.
func (s *Sink) oneByOne(ctx context.Context, c *pgwire.Conn, items []item, whole bool) (int, error) {
	if err := s.exec(ctx, c, "BEGIN"); err != nil {
		return -1, err
	}
	for i := range items {
		if !whole || i == 0 || items[i].rec.Tx.ID == "" || items[i].rec.Tx.First || items[i].rec.Tx.ID != items[i-1].rec.Tx.ID {
			if err := s.exec(ctx, c, "SAVEPOINT fit_sync"); err != nil {
				s.rollback(ctx, c)
				return -1, err
			}
		}
		err := s.run(ctx, c, items[i:i+1])
		if err == nil {
			continue
		}
		if !Permanent(err) {
			s.rollback(ctx, c)
			return -1, err
		}
		if e := s.exec(ctx, c, "ROLLBACK TO SAVEPOINT fit_sync"); e != nil {
			s.rollback(ctx, c)
			return -1, e
		}
		if e := s.exec(ctx, c, "COMMIT"); e != nil {
			return -1, e
		}
		return i, err
	}
	// Together they failed, one at a time they did not: written.
	return -1, s.exec(ctx, c, "COMMIT")
}

func (s *Sink) exec(ctx context.Context, c *pgwire.Conn, sql string) error {
	_, err := c.Exec(ctx, sql)
	return err
}

func (s *Sink) rollback(ctx context.Context, c *pgwire.Conn) {
	if c.Err() == nil {
		c.Exec(ctx, "ROLLBACK")
	}
}

// Permanent reports whether err is the server's refusal of the data, which
// trying again cannot cure: a data exception (SQLSTATE class 22: a value
// the column's type cannot read, out of range, too long) or an integrity
// constraint's violation (class 23: not null, foreign key, unique, check).
// Every other error, a table or column missing among them, is tried again.
func Permanent(err error) bool {
	var pe *pgwire.Error
	if !errors.As(err, &pe) {
		return false
	}
	return strings.HasPrefix(pe.Code, "22") || strings.HasPrefix(pe.Code, "23")
}

// twice reports whether err is an upsert's refusal to change a row twice
// (SQLSTATE 21000), as two keys the sink tells apart and the server does
// not (1.0 and 1.00) make: written one at a time, they do not.
func twice(err error) bool {
	var pe *pgwire.Error
	return errors.As(err, &pe) && pe.Code == "21000"
}

// run writes items in order: each run of records with the same table,
// operation and columns one statement, of at most max_statement_rows rows,
// holding each key once (an upsert's last row, as the key would end).
func (s *Sink) run(ctx context.Context, c *pgwire.Conn, items []item) error {
	for i := 0; i < len(items); {
		if items[i].skip || items[i].dropped {
			i++
			continue
		}
		first := &items[i]
		perRow := len(first.cols)
		if first.del {
			perRow = len(first.keys)
		}
		limit := s.MaxStatementRows
		if limit <= 0 {
			limit = DefaultMaxStatementRows
		}
		limit = max(min(limit, maxParams/perRow), 1)
		var group []*item
		at := map[string]int{}
		j := i
		for ; j < len(items) && len(group) < limit; j++ {
			it := &items[j]
			if it.skip || it.dropped {
				continue
			}
			if it.sig != first.sig {
				break
			}
			if k, ok := at[it.id]; ok {
				if !it.del {
					group[k] = it
				}
				continue
			}
			at[it.id] = len(group)
			group = append(group, it)
		}
		var err error
		if !first.del && (s.Copy == "always" || s.Copy != "never" && len(group) >= s.copyMin()) {
			err = s.copyIn(ctx, c, group)
		} else {
			err = s.statement(ctx, c, group)
		}
		if err != nil {
			return err
		}
		i = j
	}
	return nil
}

func (s *Sink) copyMin() int {
	if s.CopyMinRows <= 0 {
		return DefaultCopyMinRows
	}
	return s.CopyMinRows
}

// statement writes group as one multi-row INSERT ... ON CONFLICT, or one
// DELETE.
func (s *Sink) statement(ctx context.Context, c *pgwire.Conn, group []*item) error {
	first := group[0]
	var b strings.Builder
	var args []any
	// tuple writes vals' parameters, in parentheses when there are several
	// or paren says.
	tuple := func(vals []any, paren bool) {
		paren = paren || len(vals) > 1
		if paren {
			b.WriteByte('(')
		}
		for k, v := range vals {
			if k > 0 {
				b.WriteByte(',')
			}
			args = append(args, v)
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(len(args)))
		}
		if paren {
			b.WriteByte(')')
		}
	}
	if first.del {
		b.WriteString("DELETE FROM " + first.table + " WHERE ")
		if len(first.keys) > 1 {
			b.WriteString("(" + strings.Join(first.keys, ",") + ")")
		} else {
			b.WriteString(first.keys[0])
		}
		b.WriteString(" IN (")
		for k, it := range group {
			if k > 0 {
				b.WriteByte(',')
			}
			tuple(it.kvals, false)
		}
		b.WriteByte(')')
	} else {
		b.WriteString("INSERT INTO " + first.table + " (" + strings.Join(first.cols, ",") + ") VALUES ")
		for k, it := range group {
			if k > 0 {
				b.WriteByte(',')
			}
			tuple(it.vals, true)
		}
		b.WriteString(onConflict(first))
	}
	_, err := c.Exec(ctx, b.String(), args...)
	return err
}

// onConflict is an upsert's ON CONFLICT clause: the columns not of the key
// set from the row, or nothing done when every column is the key's.
func onConflict(it *item) string {
	keys := map[string]bool{}
	for _, k := range it.keys {
		keys[k] = true
	}
	var set []string
	for _, col := range it.cols {
		if !keys[col] {
			set = append(set, col+" = EXCLUDED."+col)
		}
	}
	clause := " ON CONFLICT (" + strings.Join(it.keys, ",") + ") DO "
	if len(set) == 0 {
		return clause + "NOTHING"
	}
	return clause + "UPDATE SET " + strings.Join(set, ", ")
}

// copyIn writes group, upserts, through COPY: into a temporary table of
// the columns, dropped at the transaction's end, and from it into the
// table with INSERT ... SELECT ... ON CONFLICT.
func (s *Sink) copyIn(ctx context.Context, c *pgwire.Conn, group []*item) error {
	first := group[0]
	tmp := pgwire.QuoteIdentifier("fit_sync_copy_" + strconv.FormatUint(s.copies.Add(1), 10))
	cols := strings.Join(first.cols, ",")
	if err := s.exec(ctx, c, "CREATE TEMPORARY TABLE "+tmp+" ON COMMIT DROP AS SELECT "+cols+" FROM "+first.table+" WITH NO DATA"); err != nil {
		return err
	}
	w, err := c.CopyIn(ctx, "COPY "+tmp+" ("+cols+") FROM STDIN")
	if err != nil {
		return err
	}
	var row []byte
	for _, it := range group {
		if row, err = pgwire.AppendCopyRow(row[:0], it.vals...); err == nil {
			_, err = w.Write(row)
		}
		if err != nil {
			w.Abort(err.Error())
			return err
		}
	}
	if _, err := w.Close(); err != nil {
		return err
	}
	return s.exec(ctx, c, "INSERT INTO "+first.table+" ("+cols+") SELECT "+cols+" FROM "+tmp+onConflict(first))
}
