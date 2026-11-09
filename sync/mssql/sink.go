package syncmssql

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	mssqlwire "github.com/lesismal/fit/sync/mssql/wire"
)

func init() {
	module.Register(module.Info{ID: fitsync.Sinks + ".mssql", New: func() any { return new(Sink) },
		Doc: "Writes records to SQL Server (2016 and later): inserts, updates and messages as upserts by the record's key, with MERGE (or UPDATE then INSERT), deletes as DELETE by the key, each run of a batch's records for one table and one set of columns one statement; a run of a snapshot's inserts is bulk loaded into a temporary table and merged from it. Each source transaction is written as one of the server's (transactions: source), or several together (transactions: batch), so that the table never holds half of one, and checkpoint sink writes the checkpoint in the same transaction, for exactly once. Writing the same records again leaves the same rows. DDL records are reported, or stop the pipeline (ddl: stop)."})
}

// The defaults of the sink's params.
const (
	DefaultCheckpointTable = "_fit_sync_checkpoints"
	DefaultBulkRows        = 1000
	// maxParams bounds a statement's parameters, below the 2,100 an RPC
	// takes.
	maxParams = 2000
	// maxRows bounds the rows of one statement, as a table value
	// constructor's.
	maxRows = 1000
	// stageTable is the session's temporary table bulk loads go through.
	stageTable = "#fit_stage"
)

// Sink is the sink mssql.
type Sink struct {
	Address         string          `json:"address,omitempty" default:"\"127.0.0.1:1433\"" doc:"The server's host and port: SQL Server 2016 or later. 127.0.0.1:1433 when left out."`
	Username        string          `json:"username" fit:"required" doc:"The SQL login, which needs SELECT, INSERT, UPDATE and DELETE on the tables written (and ALTER, with identity_insert)."`
	PasswordEnv     string          `json:"password_env,omitempty" doc:"The environment variable holding username's password, read when the pipeline loads; none when left out."`
	Database        string          `json:"database,omitempty" doc:"The database the tables are in; the login's default when left out."`
	Encrypt         string          `json:"encrypt,omitempty" enum:"on,strict,off" default:"\"on\"" doc:"How the connection is encrypted: on, TLS negotiated in TDS's pre-login, refused by a server that cannot; strict, TDS 8's TLS first (SQL Server 2022); off, none, the password and rows in clear. The server's certificate is checked against the system's roots or ca_file's; the check cannot be turned off. on when left out."`
	CAFile          string          `json:"ca_file,omitempty" doc:"A PEM file of the certificates the server's is checked against, in place of the system's roots."`
	ServerName      string          `json:"server_name,omitempty" doc:"The name the server's certificate must have; address's host when left out."`
	Timeout         config.Duration `json:"timeout,omitempty" default:"\"60s\"" doc:"How long connecting, and each wait for the server, may take. 60s when left out or 0."`
	Table           string          `json:"table,omitempty" default:"\"{namespace}\"" doc:"The table a record goes to: text with {namespace}, the record's namespace; its parts, split at dots, are schema.table or db.schema.table (a MySQL source's shop.orders is the schema shop's table orders). The table is not made: it is the user's, with a primary key or unique index on the record's key columns. {namespace} when left out."`
	Upsert          string          `json:"upsert,omitempty" enum:"merge,update_insert" default:"\"merge\"" doc:"How a row is written: merge, a MERGE ... WITH (HOLDLOCK) of the run's rows; update_insert, an UPDATE by the key and, when it found no row, an INSERT, for those who keep MERGE out of their servers. merge when left out."`
	IdentityInsert  bool            `json:"identity_insert,omitempty" doc:"Write the rows' values in the tables' IDENTITY columns (SET IDENTITY_INSERT ON around each statement), as a copy of another database's keys needs; the login then needs ALTER on the tables. false when left out."`
	Transactions    string          `json:"transactions,omitempty" enum:"source,batch" default:"\"source\"" doc:"How records are grouped in the server's transactions: source, each source transaction as one, and the records without one (a snapshot's) of a batch together; batch, a batch's records up to the last that ends a source transaction in one, and those after it, a part of one source transaction, in one held open into the next batch. Either way a source transaction is never committed in part. source when left out."`
	DDL             string          `json:"ddl,omitempty" enum:"report,stop" default:"\"report\"" doc:"What a ddl record does: report, logged and not applied; stop, fails, so that on_error takes it, which with on_error stop stops the pipeline before it, for a person to change the table. report when left out."`
	Checkpoint      string          `json:"checkpoint,omitempty" enum:"file,sink" default:"\"file\"" doc:"Where the pipeline's checkpoint is kept: file, in the pipeline's file alone, so that a crash between a commit and the file's next checkpoint writes the records after it again (to the same rows); sink, also in checkpoint_table, written in the same transaction as the records it covers and read back when the pipeline starts, in preference to the file, so that no record is written twice: exactly once, while every sink of the pipeline keeps it. file when left out."`
	CheckpointTable string          `json:"checkpoint_table,omitempty" default:"\"_fit_sync_checkpoints\"" doc:"With checkpoint sink, the table the checkpoint is kept in, a row a pipeline, made when missing (which needs CREATE TABLE); schema.table names its schema, the login's default otherwise. _fit_sync_checkpoints when left out."`
	BulkRows        int             `json:"bulk_rows,omitempty" min:"0" default:"1000" doc:"A run of at least this many inserts without a source transaction (a snapshot's, a job's) is bulk loaded (INSERT BULK) into a temporary table and merged from it into the table, rather than written as statements of parameters. A number larger than any batch turns it off. 1000 when left out or 0."`

	cfg   mssqlwire.Config
	dial  func(context.Context, mssqlwire.Config) (sinkConn, error)
	table []tablePart
	batch bool
	stop  bool
	merge bool
	log   *logs.Logger

	// ckTable is checkpoint_table, quoted; pipeline and lineage are what
	// each commit writes in it, once ReadCheckpoint has set them.
	ckTable           string
	pipeline, lineage string

	conn sinkConn
	// types are the tables' columns' types, by quoted table and column,
	// which a NULL parameter is sent as: the server converts no NULL of
	// nvarchar, a parameter's type when it has no value, to varbinary.
	types map[string]map[string]string
	// tx is the server's transaction as the sink writes it; acked is it as
	// the last Write left it, every record in it acknowledged, which a new
	// connection writes again.
	tx, acked txState
}

type tablePart struct {
	text      string
	namespace bool
}

// sinkConn is what the sink asks of a connection: *mssqlwire.Conn through
// sinkWire, or a fake in the tests.
type sinkConn interface {
	Exec(ctx context.Context, sql string, args ...any) (int64, error)
	QueryAll(ctx context.Context, sql string, args ...any) ([]mssqlwire.Column, [][]any, error)
	Begin(ctx context.Context, isolation string) error
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
	// Bulk starts a bulk load of table's columns.
	Bulk(ctx context.Context, table string, columns []string) (bulkLoad, error)
	Err() error
	Close() error
}

// bulkLoad is a bulk load under way.
type bulkLoad interface {
	WriteRow(values ...any) error
	Close() (int64, error)
}

type sinkWire struct{ *mssqlwire.Conn }

func (c sinkWire) Bulk(ctx context.Context, table string, columns []string) (bulkLoad, error) {
	return c.BulkInsert(ctx, table, columns, mssqlwire.BulkOptions{TabLock: true})
}

func dialSink(ctx context.Context, cfg mssqlwire.Config) (sinkConn, error) {
	c, err := mssqlwire.Dial(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return sinkWire{c}, nil
}

// txState is an open transaction of the server's.
type txState struct {
	open bool
	// unit is the source transaction it holds, with transactions source.
	unit string
	// ops are what it holds, in order.
	ops []*op
	// cur is the source transaction of the last record written, and
	// curEnded whether the record ended it.
	cur      string
	curEnded bool
	// pos is the position of the last record written that ends a source
	// transaction, which the commit writes as the checkpoint.
	pos fitsync.Position
}

// op is a statement's part: a row upserted or a key deleted.
type op struct {
	// rec is the record's index in its batch.
	rec   int
	table string
	del   bool
	// cols are the row's columns, or the key's, and vals their values;
	// keys are the key's columns of an upsert.
	cols, keys []string
	vals       []any
	// key is the key's values, to tell two ops of one row apart.
	key string
	// bulk says the op may be bulk loaded: an insert without a source
	// transaction.
	bulk bool
}

func (o *op) same(p *op) bool {
	return o.table == p.table && o.del == p.del && o.bulk == p.bulk && slices.Equal(o.cols, p.cols) && slices.Equal(o.keys, p.keys)
}

// item is a record as the sink writes it.
type item struct {
	ops  []*op
	err  error
	unit string
	// ends says the record ends its source transaction, or has none.
	ends bool
	// pos is the record's position when it ends one.
	pos fitsync.Position
}

// Provision checks the params and reads the password and CA file; nothing
// is sent until the first write.
func (s *Sink) Provision(ctx *module.Context) error {
	encrypt := s.Encrypt
	if encrypt == "" {
		encrypt = mssqlwire.EncryptOn
	}
	cfg, errs := WireConfig(ctx, ConnParams{Address: s.Address, Username: s.Username, PasswordEnv: s.PasswordEnv, Database: s.Database,
		Encrypt: encrypt, CAFile: s.CAFile, ServerName: s.ServerName, Timeout: s.Timeout})
	s.cfg = cfg
	if s.dial == nil {
		s.dial = dialSink
	}
	tmpl := s.Table
	if tmpl == "" {
		tmpl = "{namespace}"
	}
	s.table = nil
	for rest := tmpl; rest != ""; {
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
	s.batch = s.Transactions == "batch"
	s.stop = s.DDL == "stop"
	s.merge = s.Upsert != "update_insert"
	if s.KeepsCheckpoint() {
		ck := s.CheckpointTable
		if ck == "" {
			ck = DefaultCheckpointTable
		}
		q, err := quoteTable(ck)
		if err != nil {
			errs = append(errs, ctx.Params.Field("checkpoint_table").Errorf("%v", err))
		}
		s.ckTable = q
	}
	s.log = logs.Named(config.LoggerSync)
	if len(errs) == 0 {
		return nil
	}
	return errs
}

func (s *Sink) bulkRows() int {
	if s.BulkRows <= 0 {
		return DefaultBulkRows
	}
	return s.BulkRows
}

// quoteTable quotes a table's name, its parts split at dots, at most three
// (db.schema.table).
func quoteTable(name string) (string, error) {
	parts := strings.Split(name, ".")
	if len(parts) > 3 {
		return "", fmt.Errorf("%q has more than three parts", name)
	}
	for i, p := range parts {
		if p == "" {
			return "", fmt.Errorf("%q has an empty part", name)
		}
		parts[i] = quoteName(p)
	}
	return strings.Join(parts, "."), nil
}

// Cleanup closes the connection, which rolls back a transaction left open:
// its records are written again from the checkpoint, which is never inside
// it.
func (s *Sink) Cleanup() {
	if s.conn != nil {
		s.conn.Close()
		s.conn = nil
	}
}

// KeepsCheckpoint reports whether checkpoint is sink.
func (s *Sink) KeepsCheckpoint() bool { return s.Checkpoint == "sink" }

// ReadCheckpoint makes checkpoint_table when missing and reads the
// pipeline's row; each commit from then on writes the position of its last
// record that ends a source transaction there, with lineage. What the
// sink held from an earlier run, a source transaction open, is dropped:
// the run starts before it.
func (s *Sink) ReadCheckpoint(ctx context.Context, pipeline, lineage string) (fitsync.Position, string, error) {
	if !s.KeepsCheckpoint() {
		return nil, "", nil
	}
	s.Cleanup()
	s.tx, s.acked = txState{}, txState{}
	if err := s.connect(ctx); err != nil {
		return nil, "", err
	}
	if _, err := s.conn.Exec(ctx, "IF OBJECT_ID(@p1, N'U') IS NULL CREATE TABLE "+s.ckTable+" ([pipeline] nvarchar(191) NOT NULL PRIMARY KEY, "+
		"[lineage] nvarchar(64) NOT NULL, [position] nvarchar(max) NOT NULL, [updated] datetime2(6) NOT NULL)", s.ckTable); err != nil {
		s.Cleanup()
		return nil, "", err
	}
	_, rows, err := s.conn.QueryAll(ctx, "SELECT [lineage], [position] FROM "+s.ckTable+" WHERE [pipeline] = @p1", pipeline)
	if err != nil {
		s.Cleanup()
		return nil, "", err
	}
	s.pipeline, s.lineage = pipeline, lineage
	if len(rows) == 0 || len(rows[0]) < 2 {
		return nil, "", nil
	}
	return fitsync.Position(fmt.Sprint(rows[0][1])), fmt.Sprint(rows[0][0]), nil
}

// Test connects and logs in on a connection of its own, and closes it.
func (s *Sink) Test(ctx context.Context) error {
	c, err := s.dial(ctx, s.cfg)
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = c.Exec(ctx, "SELECT 1")
	return err
}

func (s *Sink) tableOf(r *fitsync.Record) (string, error) {
	var b strings.Builder
	for _, p := range s.table {
		if p.namespace {
			b.WriteString(r.Namespace)
		} else {
			b.WriteString(p.text)
		}
	}
	return quoteTable(b.String())
}

// build makes r's item, the record at index i of its batch.
func (s *Sink) build(i int, r *fitsync.Record) item {
	it := item{unit: r.Tx.ID, ends: r.Tx.ID == "" || r.Tx.Last}
	if it.ends {
		it.pos = r.Position
	}
	switch r.Op {
	case fitsync.OpDDL:
		what, _ := fitsync.AppendPlain(nil, r.After)
		if len(what) > 200 {
			what = append(what[:200], "..."...)
		}
		if s.stop {
			it.err = fmt.Errorf("a DDL of %s, which ddl stop does not go past: %s", r.Namespace, what)
			return it
		}
		s.log.Infof("sync mssql: a DDL of %s is not applied: %s", r.Namespace, what)
		return it
	case fitsync.OpInsert, fitsync.OpUpdate, fitsync.OpMessage, fitsync.OpDelete:
	default:
		it.err = fmt.Errorf("a record of operation %s", r.Op)
		return it
	}
	table, err := s.tableOf(r)
	if err != nil {
		it.err = fmt.Errorf("the record's table: %w", err)
		return it
	}
	if r.Key.Kind() != fitsync.KindMap || len(r.Key.Fields()) == 0 {
		it.err = fmt.Errorf("the key is a %s, not a map of columns to write by", r.Key.Kind())
		return it
	}
	if r.Op == fitsync.OpDelete {
		o, err := keyOp(i, table, r.Key)
		if err != nil {
			it.err = fmt.Errorf("a delete: %w", err)
			return it
		}
		it.ops = []*op{o}
		return it
	}
	if r.After.Kind() != fitsync.KindMap || len(r.After.Fields()) == 0 {
		it.err = fmt.Errorf("an %s's row is a %s, not a map of columns", r.Op, r.After.Kind())
		return it
	}
	fields := r.After.Fields()
	up := &op{rec: i, table: table, cols: make([]string, 0, len(fields)), vals: make([]any, 0, len(fields)),
		bulk: r.Op == fitsync.OpInsert && r.Tx.ID == ""}
	for _, f := range fields {
		v, err := arg(f.Value)
		if err != nil {
			it.err = fmt.Errorf("column %s: %w", f.Name, err)
			return it
		}
		up.cols = append(up.cols, f.Name)
		up.vals = append(up.vals, v)
	}
	var key strings.Builder
	for _, f := range r.Key.Fields() {
		v, ok := r.After.Get(f.Name)
		if !ok {
			it.err = fmt.Errorf("the row has no key column %s", f.Name)
			return it
		}
		if v.IsNull() {
			it.err = fmt.Errorf("the key's %s is null", f.Name)
			return it
		}
		up.keys = append(up.keys, f.Name)
		key.WriteString(fitsync.TextOf(v) + "\x00")
	}
	up.key = key.String()
	// An update that changed the key deletes the row of the key it had.
	if r.Op == fitsync.OpUpdate && r.Before.Kind() == fitsync.KindMap {
		var old []fitsync.Field
		changed := false
		for _, f := range r.Key.Fields() {
			v, ok := r.Before.Get(f.Name)
			if !ok {
				old = nil
				break
			}
			old = append(old, fitsync.Field{Name: f.Name, Value: v})
			cur, _ := r.After.Get(f.Name)
			changed = changed || !v.Equal(cur)
		}
		if changed && len(old) > 0 {
			o, err := keyOp(i, table, fitsync.Map(old...))
			if err != nil {
				it.err = fmt.Errorf("an update's key as it was: %w", err)
				return it
			}
			it.ops = append(it.ops, o)
		}
	}
	it.ops = append(it.ops, up)
	return it
}

// keyOp makes the delete of key's row.
func keyOp(i int, table string, key fitsync.Value) (*op, error) {
	o := &op{rec: i, table: table, del: true}
	var k strings.Builder
	for _, f := range key.Fields() {
		if f.Value.IsNull() {
			return nil, fmt.Errorf("the key's %s is null", f.Name)
		}
		v, err := arg(f.Value)
		if err != nil {
			return nil, fmt.Errorf("the key's %s: %w", f.Name, err)
		}
		o.cols = append(o.cols, f.Name)
		o.vals = append(o.vals, v)
		k.WriteString(fitsync.TextOf(f.Value) + "\x00")
	}
	o.key = k.String()
	return o, nil
}

// arg is v as the client's parameter: decimals as Decimal, a uint past
// bigint's as a decimal, a date as date, times as datetimeoffset (which a
// datetime2 column takes as its wall clock, UTC here), JSON, arrays and
// maps as their JSON text.
func arg(v fitsync.Value) (any, error) {
	switch v.Kind() {
	case fitsync.KindNull:
		return nil, nil
	case fitsync.KindBool:
		return v.AsBool(), nil
	case fitsync.KindInt:
		return v.AsInt(), nil
	case fitsync.KindUint:
		if u := v.AsUint(); u <= math.MaxInt64 {
			return int64(u), nil
		}
		return mssqlwire.Decimal(fitsync.TextOf(v)), nil
	case fitsync.KindFloat:
		f := v.AsFloat()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("%v has no column type to go in", f)
		}
		return f, nil
	case fitsync.KindDecimal:
		return mssqlwire.Decimal(v.Text()), nil
	case fitsync.KindString:
		return v.Text(), nil
	case fitsync.KindBytes:
		return []byte(v.Text()), nil
	case fitsync.KindTime:
		return v.AsTime().UTC(), nil
	case fitsync.KindDate:
		return mssqlwire.Param{Value: v.AsTime(), Type: "date"}, nil
	}
	b, err := fitsync.AppendPlain(nil, v)
	return string(b), err
}

// plain is a parameter's value as a bulk load's row takes it.
func plain(v any) any {
	if p, ok := v.(mssqlwire.Param); ok {
		return p.Value
	}
	return v
}

// permanent tells a server's error that writing the statement again
// cannot cure: a value the column refuses, a constraint, a table or column
// missing, a statement refused. A deadlock, a lock or snapshot conflict, a
// timeout and a connection's errors pass.
func permanent(err error) bool {
	var re *rowError
	if errors.As(err, &re) {
		return true
	}
	var se *mssqlwire.Error
	if !errors.As(err, &se) {
		return false
	}
	switch se.Number {
	case 102, 156, 207, 208, 213, 220, 241, 242, 245, 295, 515, 544, 547, 2601, 2627, 2628, 4145, 8114, 8115, 8152, 8169, 9455, 9809, 1934:
		return true
	}
	return false
}

// rowError is a bulk load's row the client could not send.
type rowError struct{ err error }

func (e *rowError) Error() string { return e.err.Error() }
func (e *rowError) Unwrap() error { return e.err }

// badRecord is a record whose statement the server refused for good.
type badRecord struct {
	rec int
	err error
}

func (e *badRecord) Error() string { return e.err.Error() }

// Write writes the batch in order, in the server's transactions as
// transactions says. It returns nil once every record is written, in a
// transaction committed or, when the batch ends inside a source
// transaction, still open, which the next Write goes on with (or writes
// again on a new connection, should this one fail); a BatchError at a
// record the server refuses for good, those before it committed; any
// other error once the connection is closed, for the batch to be written
// again whole.
func (s *Sink) Write(ctx context.Context, batch []*fitsync.Record) error {
	items := make([]item, len(batch))
	for i, r := range batch {
		items[i] = s.build(i, r)
	}
	if err := s.connect(ctx); err != nil {
		return err
	}
	lastEnd := -1
	for i := range items {
		if items[i].ends {
			lastEnd = i
		}
	}
	var g []*op
	keys := map[string]bool{}
	startPos := s.tx.pos
	refuse := func(i int, err error) error {
		s.tx.pos = startPos
		for j := i - 1; j >= 0; j-- {
			if items[j].pos != nil {
				s.tx.pos = items[j].pos
				break
			}
		}
		return s.refuse(ctx, i, err)
	}
	fail := func(err error) error {
		var br *badRecord
		if errors.As(err, &br) {
			return refuse(br.rec, br.err)
		}
		s.reset()
		return err
	}
	flush := func() error {
		err := s.flush(ctx, g)
		g = g[:0]
		clear(keys)
		return err
	}
	for i := range items {
		it := &items[i]
		if it.err != nil {
			if err := flush(); err != nil {
				return fail(err)
			}
			return refuse(i, it.err)
		}
		if !s.batch && s.tx.open && it.unit != s.tx.unit {
			if err := flush(); err != nil {
				return fail(err)
			}
			if err := s.commit(ctx); err != nil {
				return fail(err)
			}
		}
		if s.tx.curEnded || it.unit != s.tx.cur || it.unit == "" {
			s.tx.cur = it.unit
		}
		s.tx.curEnded = it.ends
		for _, o := range it.ops {
			if !s.tx.open {
				if err := s.conn.Begin(ctx, ""); err != nil {
					return fail(err)
				}
				s.tx.open, s.tx.unit = true, it.unit
			}
			// A group writes a row once: a key again starts the next.
			if len(g) > 0 && (!g[0].same(o) || keys[o.key]) {
				if err := flush(); err != nil {
					return fail(err)
				}
			}
			g = append(g, o)
			keys[o.key] = true
		}
		if it.pos != nil {
			s.tx.pos = it.pos
		}
		if !s.tx.open {
			continue
		}
		if it.ends && (s.batch && i == lastEnd || !s.batch && it.unit != "") {
			if err := flush(); err != nil {
				return fail(err)
			}
			if err := s.commit(ctx); err != nil {
				return fail(err)
			}
		}
	}
	if err := flush(); err != nil {
		return fail(err)
	}
	if s.tx.open && !s.batch && len(items) > 0 && items[len(items)-1].unit == "" {
		// Records without a source transaction, a run of them at the
		// batch's end.
		if err := s.commit(ctx); err != nil {
			return fail(err)
		}
	}
	s.acked = s.tx
	s.acked.ops = slices.Clone(s.tx.ops)
	return nil
}

// refuse commits what is written before record i, which the server
// refuses for good, and returns the BatchError that says so: the records
// before it acknowledged, even a part of a source transaction, which
// on_error stop writes again from the checkpoint.
func (s *Sink) refuse(ctx context.Context, i int, err error) error {
	if s.tx.open {
		if cerr := s.commit(ctx); cerr != nil {
			s.reset()
			return cerr
		}
	}
	s.acked = txState{}
	s.tx = txState{}
	return &fitsync.BatchError{Index: i, Err: fitsync.Permanent(err)}
}

// connect makes the connection when there is none or it broke, and writes
// again what the transaction the last Write left open held.
func (s *Sink) connect(ctx context.Context) error {
	if s.conn != nil && s.conn.Err() == nil {
		return nil
	}
	s.Cleanup()
	c, err := s.dial(ctx, s.cfg)
	if err != nil {
		return err
	}
	// An error rolls the whole transaction back, never part of it; what
	// the server leaves of it after one is asked (XACT_STATE).
	if _, err := c.Exec(ctx, "SET XACT_ABORT ON"); err != nil {
		c.Close()
		return err
	}
	s.conn = c
	s.types = map[string]map[string]string{}
	s.tx = s.acked
	s.tx.open = false
	s.tx.ops = nil
	if !s.acked.open {
		return nil
	}
	if err := s.reopen(ctx, s.acked.ops); err != nil {
		s.reset()
		return err
	}
	return nil
}

// reopen begins a transaction and writes ops in it again.
func (s *Sink) reopen(ctx context.Context, ops []*op) error {
	if err := s.conn.Begin(ctx, ""); err != nil {
		return err
	}
	s.tx.open = true
	for i := 0; i < len(ops); {
		j := i + 1
		seen := map[string]bool{ops[i].key: true}
		for j < len(ops) && ops[i].same(ops[j]) && !seen[ops[j].key] {
			seen[ops[j].key] = true
			j++
		}
		if err := s.exec(ctx, ops[i:j]); err != nil {
			return err
		}
		i = j
	}
	s.tx.ops = slices.Clone(ops)
	return nil
}

// reset closes the connection after an error, which rolls back the
// transaction open: the next Write connects again and writes what was
// acknowledged in it again.
func (s *Sink) reset() {
	s.Cleanup()
	s.tx = s.acked
}

func (s *Sink) commit(ctx context.Context) error {
	if s.pipeline != "" && s.tx.pos != nil {
		if _, err := s.conn.Exec(ctx, "MERGE INTO "+s.ckTable+" WITH (HOLDLOCK) AS [t] USING (VALUES (@p1, @p2, @p3, @p4)) AS [s] ([pipeline], [lineage], [position], [updated]) "+
			"ON [t].[pipeline] = [s].[pipeline] WHEN MATCHED THEN UPDATE SET [t].[lineage] = [s].[lineage], [t].[position] = [s].[position], [t].[updated] = [s].[updated] "+
			"WHEN NOT MATCHED THEN INSERT ([pipeline], [lineage], [position], [updated]) VALUES ([s].[pipeline], [s].[lineage], [s].[position], [s].[updated]);",
			s.pipeline, s.lineage, string(s.tx.pos), mssqlwire.Param{Value: time.Now().UTC(), Type: "datetime2(6)"}); err != nil {
			return err
		}
	}
	if err := s.conn.Commit(ctx); err != nil {
		return err
	}
	cur, curEnded := s.tx.cur, s.tx.curEnded
	s.tx = txState{cur: cur, curEnded: curEnded}
	return nil
}

// recover makes the transaction whole again after a statement failed in
// it: when the server rolled it back, or doomed it (XACT_ABORT), it is
// begun again and its ops written again.
func (s *Sink) recover(ctx context.Context) error {
	_, rows, err := s.conn.QueryAll(ctx, "SELECT XACT_STATE()")
	if err != nil {
		return err
	}
	if len(rows) == 1 && len(rows[0]) == 1 && rows[0][0] == int64(1) {
		return nil
	}
	if err := s.conn.Rollback(ctx); err != nil {
		return err
	}
	return s.reopen(ctx, s.tx.ops)
}

// flush writes g, ops of one table and columns, each row once; on an
// error the server gives for good, it writes them one at a time to find
// the record at fault.
func (s *Sink) flush(ctx context.Context, g []*op) error {
	if len(g) == 0 {
		return nil
	}
	err := s.exec(ctx, g)
	if err == nil {
		s.tx.ops = append(s.tx.ops, g...)
		return nil
	}
	if !permanent(err) {
		return err
	}
	if err := s.recover(ctx); err != nil {
		return err
	}
	if len(g) > 1 {
		for k := range g {
			if err = s.exec(ctx, g[k:k+1]); err != nil {
				if !permanent(err) {
					return err
				}
				if rerr := s.recover(ctx); rerr != nil {
					return rerr
				}
				return &badRecord{rec: g[k].rec, err: err}
			}
			s.tx.ops = append(s.tx.ops, g[k])
		}
		return nil
	}
	return &badRecord{rec: g[0].rec, err: err}
}

// exec runs ops of one table and columns, each row once: upserts as MERGEs
// (or UPDATEs and INSERTs) of up to maxRows rows, a large run of inserts
// through a bulk load, deletes as DELETEs.
func (s *Sink) exec(ctx context.Context, ops []*op) error {
	o := ops[0]
	if o.bulk && len(ops) >= s.bulkRows() {
		return s.bulk(ctx, ops)
	}
	per := max(min(maxRows, maxParams/len(o.cols)), 1)
	for len(ops) > 0 {
		n := min(len(ops), per)
		var sql string
		args := make([]any, 0, n*len(o.cols))
		for _, p := range ops[:n] {
			for i, v := range p.vals {
				if v == nil {
					t, err := s.typeOf(ctx, o.table, o.cols[i])
					if err != nil {
						return err
					}
					v = mssqlwire.Param{Type: t}
				}
				args = append(args, v)
			}
		}
		switch {
		case o.del:
			sql = deleteSQL(o.table, o.cols, n)
		case s.merge:
			sql = mergeSQL(o.table, o.cols, o.keys, valuesOf(len(o.cols), n))
		default:
			sql = updateInsertSQL(o.table, o.cols, o.keys, n)
		}
		if s.IdentityInsert && !o.del {
			sql = "SET IDENTITY_INSERT " + o.table + " ON; " + sql + " SET IDENTITY_INSERT " + o.table + " OFF;"
		}
		if _, err := s.conn.Exec(ctx, sql, args...); err != nil {
			return err
		}
		ops = ops[n:]
	}
	return nil
}

// typeOf returns the type of table's column, read once a connection.
func (s *Sink) typeOf(ctx context.Context, table, col string) (string, error) {
	cols, ok := s.types[table]
	if !ok {
		cs, _, err := s.conn.QueryAll(ctx, "SELECT TOP 0 * FROM "+table)
		if err != nil {
			return "", err
		}
		cols = map[string]string{}
		for i := range cs {
			cols[strings.ToLower(cs[i].Name)] = cs[i].TypeName()
		}
		s.types[table] = cols
	}
	if t, ok := cols[strings.ToLower(col)]; ok {
		return t, nil
	}
	// A column the table lacks: the statement fails saying so.
	return "nvarchar(1)", nil
}

// bulk loads ops into the session's temporary table, of the table's
// columns (an IDENTITY column there a plain one), and merges it into the
// table.
func (s *Sink) bulk(ctx context.Context, ops []*op) error {
	o := ops[0]
	cols := quoteAll(o.cols)
	list := strings.Join(cols, ", ")
	if _, err := s.conn.Exec(ctx, "IF OBJECT_ID(N'tempdb.."+stageTable+"') IS NOT NULL DROP TABLE "+stageTable+"; "+
		"SELECT TOP 0 "+list+" INTO "+stageTable+" FROM "+o.table+" UNION ALL SELECT TOP 0 "+list+" FROM "+o.table); err != nil {
		return err
	}
	b, err := s.conn.Bulk(ctx, stageTable, o.cols)
	if err != nil {
		return err
	}
	vals := make([]any, len(o.cols))
	for _, p := range ops {
		for i, v := range p.vals {
			vals[i] = plain(v)
		}
		if err := b.WriteRow(vals...); err != nil {
			b.Close()
			return &rowError{err}
		}
	}
	if _, err := b.Close(); err != nil {
		return err
	}
	var sql string
	if s.merge {
		sql = mergeSQL(o.table, o.cols, o.keys, stageTable)
	} else {
		sql = updateInsertStageSQL(o.table, o.cols, o.keys)
	}
	if s.IdentityInsert {
		sql = "SET IDENTITY_INSERT " + o.table + " ON; " + sql + " SET IDENTITY_INSERT " + o.table + " OFF;"
	}
	_, err = s.conn.Exec(ctx, sql+" DROP TABLE "+stageTable+";")
	return err
}

func quoteAll(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = quoteName(n)
	}
	return out
}

// valuesOf is a table value constructor of n rows of cols parameters.
func valuesOf(cols, n int) string {
	var b strings.Builder
	b.WriteString("(VALUES ")
	p := 1
	for r := range n {
		if r > 0 {
			b.WriteString(", ")
		}
		b.WriteByte('(')
		for c := range cols {
			if c > 0 {
				b.WriteString(", ")
			}
			b.WriteString("@p" + strconv.Itoa(p))
			p++
		}
		b.WriteByte(')')
	}
	b.WriteByte(')')
	return b.String()
}

// on is the join of the target [t] and the source [s] on keys.
func on(keys []string) string {
	parts := make([]string, len(keys))
	for i, k := range keys {
		q := quoteName(k)
		parts[i] = "[t]." + q + " = [s]." + q
	}
	return strings.Join(parts, " AND ")
}

// mergeSQL merges source, a table value constructor or a table, of cols
// into table by keys.
func mergeSQL(table string, cols, keys []string, source string) string {
	q := quoteAll(cols)
	var b strings.Builder
	b.WriteString("MERGE INTO " + table + " WITH (HOLDLOCK) AS [t] USING " + source + " AS [s]")
	if strings.HasPrefix(source, "(VALUES") {
		// A table value constructor's columns are named; a table's are
		// its own.
		b.WriteString(" (" + strings.Join(q, ", ") + ")")
	}
	b.WriteString(" ON " + on(keys))
	var set []string
	for i, c := range cols {
		if !slices.Contains(keys, c) {
			set = append(set, "[t]."+q[i]+" = [s]."+q[i])
		}
	}
	if len(set) > 0 {
		b.WriteString(" WHEN MATCHED THEN UPDATE SET " + strings.Join(set, ", "))
	}
	src := make([]string, len(q))
	for i, c := range q {
		src[i] = "[s]." + c
	}
	b.WriteString(" WHEN NOT MATCHED THEN INSERT (" + strings.Join(q, ", ") + ") VALUES (" + strings.Join(src, ", ") + ");")
	return b.String()
}

// updateInsertSQL writes n rows of cols, each an UPDATE by keys and, when
// it found no row, an INSERT.
func updateInsertSQL(table string, cols, keys []string, n int) string {
	q := quoteAll(cols)
	var b strings.Builder
	p := 1
	for range n {
		var set, where []string
		params := make([]string, len(cols))
		for i, c := range cols {
			params[i] = "@p" + strconv.Itoa(p)
			p++
			if slices.Contains(keys, c) {
				where = append(where, q[i]+" = "+params[i])
			} else {
				set = append(set, q[i]+" = "+params[i])
			}
		}
		ins := "INSERT INTO " + table + " (" + strings.Join(q, ", ") + ") VALUES (" + strings.Join(params, ", ") + ");"
		if len(set) == 0 {
			b.WriteString("IF NOT EXISTS (SELECT 1 FROM " + table + " WHERE " + strings.Join(where, " AND ") + ") " + ins + " ")
			continue
		}
		b.WriteString("UPDATE " + table + " SET " + strings.Join(set, ", ") + " WHERE " + strings.Join(where, " AND ") + "; IF @@ROWCOUNT = 0 " + ins + " ")
	}
	return strings.TrimSpace(b.String())
}

// updateInsertStageSQL writes the staged rows into table: an UPDATE of the
// rows there, then an INSERT of the others.
func updateInsertStageSQL(table string, cols, keys []string) string {
	q := quoteAll(cols)
	var set []string
	for i, c := range cols {
		if !slices.Contains(keys, c) {
			set = append(set, q[i]+" = [s]."+q[i])
		}
	}
	src := make([]string, len(q))
	for i, c := range q {
		src[i] = "[s]." + c
	}
	var b strings.Builder
	if len(set) > 0 {
		b.WriteString("UPDATE [t] SET " + strings.Join(set, ", ") + " FROM " + table + " AS [t] JOIN " + stageTable + " AS [s] ON " + on(keys) + "; ")
	}
	b.WriteString("INSERT INTO " + table + " (" + strings.Join(q, ", ") + ") SELECT " + strings.Join(src, ", ") + " FROM " + stageTable +
		" AS [s] WHERE NOT EXISTS (SELECT 1 FROM " + table + " AS [t] WHERE " + on(keys) + ");")
	return b.String()
}

// deleteSQL deletes n keys of cols.
func deleteSQL(table string, cols []string, n int) string {
	q := quoteAll(cols)
	var b strings.Builder
	b.WriteString("DELETE FROM " + table + " WHERE ")
	p := 1
	for r := range n {
		if r > 0 {
			b.WriteString(" OR ")
		}
		b.WriteByte('(')
		for i, c := range q {
			if i > 0 {
				b.WriteString(" AND ")
			}
			b.WriteString(c + " = @p" + strconv.Itoa(p))
			p++
		}
		b.WriteByte(')')
	}
	return b.String()
}
