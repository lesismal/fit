package syncmysql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	mysqlwire "github.com/lesismal/fit/sync/mysql/wire"
)

func init() {
	module.Register(module.Info{ID: fitsync.Sinks + ".mysql", New: func() any { return new(Sink) },
		Doc: "Writes records to MySQL, MariaDB or TiDB: inserts, updates and messages as INSERT ... ON DUPLICATE KEY UPDATE of the row, deletes as DELETE by the key's columns, each run of a batch's records for one table and one set of columns a multi-row statement; each source transaction is written as one of the server's (transactions: source), or several of them together (transactions: batch), so that the table never holds half of one. Writing the same records again leaves the same rows. DDL records are reported, or stop the pipeline (ddl: stop)."})
}

// The defaults of the sink's params.
const (
	// DefaultTiDBTransactionBytes is max_transaction_bytes against TiDB
	// when left out: well below its txn-total-size-limit of 100 MiB,
	// which counts a row's indexes too.
	DefaultTiDBTransactionBytes = 16 << 20
	// maxDeleteKeys bounds the keys of one DELETE.
	maxDeleteKeys = 1000
	// DefaultCheckpointTable is checkpoint_table when left out.
	DefaultCheckpointTable = "_fit_sync_checkpoints"
)

// Sink is the sink mysql.
type Sink struct {
	Address             string          `json:"address,omitempty" default:"\"127.0.0.1:3306\"" doc:"The server's host and port: MySQL 5.7 or later, MariaDB 10.11 or later, or TiDB. 127.0.0.1:3306 when left out."`
	Username            string          `json:"username" fit:"required" doc:"The user to log in as, which needs INSERT, UPDATE and DELETE on the tables written."`
	PasswordEnv         string          `json:"password_env,omitempty" doc:"The environment variable holding username's password, read when the pipeline loads; none when left out."`
	Database            string          `json:"database,omitempty" doc:"The database of a table named without one."`
	TLS                 bool            `json:"tls,omitempty" doc:"Connect over TLS, required: a server that does not offer it is refused, never spoken to in plain text. The server's certificate is checked against the system's roots or ca_file's; the check cannot be turned off."`
	CAFile              string          `json:"ca_file,omitempty" doc:"A PEM file of the certificates the server's is checked against, in place of the system's roots, for a CA of its own; needs tls."`
	CertFile            string          `json:"cert_file,omitempty" doc:"A PEM file of a client certificate, and the chain after it, presented to a server that asks for one; needs tls and key_file."`
	KeyFile             string          `json:"key_file,omitempty" doc:"The PEM file of cert_file's private key."`
	ServerName          string          `json:"server_name,omitempty" doc:"The name the server's certificate must have, and the TLS server name sent; address's host when left out."`
	TLSRSA              bool            `json:"tls_rsa,omitempty" doc:"Offer TLS's RSA key exchange besides the default suites, for MySQL 5.7's image, which offers no other that Go has; needs tls. false when left out."`
	ServerPublicKeyFile string          `json:"server_public_key_file,omitempty" doc:"A PEM file of the server's RSA public key, with which caching_sha2_password and sha256_password send the password without TLS. Without it and without TLS, a user of those plugins logs in only through the server's cache."`
	Timeout             config.Duration `json:"timeout,omitempty" default:"\"60s\"" doc:"How long connecting, and each wait for the server in a statement, may take. 60s when left out or 0."`
	Table               string          `json:"table,omitempty" default:"\"{namespace}\"" doc:"The table a record goes to: text with {namespace}, the record's namespace; db.table names the database too. The table is not made: it is the user's, with a primary or unique key on the record's key columns, which the upserts rely on. {namespace} when left out."`
	Transactions        string          `json:"transactions,omitempty" enum:"source,batch" default:"\"source\"" doc:"How records are grouped in the server's transactions: source, each source transaction as one, and the records without one (a snapshot's) of a batch together; batch, a batch's records up to the last that ends a source transaction in one, several source transactions together, and those after it, a part of one source transaction, in one held open into the next batch. Either way a source transaction is never committed in part, unless it is larger than max_transaction_bytes. source when left out."`
	DDL                 string          `json:"ddl,omitempty" enum:"report,stop" default:"\"report\"" doc:"What a ddl record does: report, logged and not applied; stop, fails, so that on_error takes it, which with on_error stop stops the pipeline before it, for a person to change the table. report when left out."`
	Checkpoint          string          `json:"checkpoint,omitempty" enum:"file,sink" default:"\"file\"" doc:"Where the pipeline's checkpoint is kept: file, in the pipeline's file alone, so that a crash between a commit and the file's next checkpoint writes the records after it again (to the same rows); sink, also in checkpoint_table, written in the same transaction as the records it covers and read back when the pipeline starts, in preference to the file, so that no record is written twice: exactly once, while every sink of the pipeline keeps it. file when left out."`
	CheckpointTable     string          `json:"checkpoint_table,omitempty" default:"\"_fit_sync_checkpoints\"" doc:"With checkpoint sink, the table the checkpoint is kept in, a row a pipeline, made when missing (which needs CREATE on it); db.table names the database too, database's otherwise. _fit_sync_checkpoints when left out."`
	MaxTransactionBytes int64           `json:"max_transaction_bytes,omitempty" min:"0" doc:"The most bytes of values one of the server's transactions holds: past it, a transaction is committed at the next record that ends a source transaction, and a source transaction larger than this alone is committed in parts, with a warning. Against TiDB, whose transactions are bounded by txn-total-size-limit, 16 MiB when left out or 0; no bound against MySQL and MariaDB."`

	cfg   mysqlwire.Config
	dial  func(context.Context, mysqlwire.Config) (db, error)
	table []tablePart
	batch bool
	stop  bool
	log   *logs.Logger

	// ckTable is checkpoint_table, quoted; pipeline and lineage are what
	// each commit writes in it, once ReadCheckpoint has set them.
	ckTable           string
	pipeline, lineage string

	// writer is the sink's first connection, the only one of a stream;
	// a job's parallel writers take spare ones while it is busy.
	writer
	mu    sync.Mutex
	busy  bool
	spare []*writer
}

// writer is a connection of the sink's and the transaction it writes, for
// one Write at a time.
type writer struct {
	s     *Sink
	conn  db
	tidb  bool
	maxTx int64
	// tx is the server's transaction as the sink writes it; acked is it as
	// the last Write left it, every record in it acknowledged, which a new
	// connection writes again.
	tx, acked txState
}

type tablePart struct {
	text      string
	namespace bool
}

// txState is an open transaction of the server's.
type txState struct {
	open bool
	// unit is the source transaction it holds, with transactions source.
	unit string
	// bytes are the values it holds; ops what it holds, in order.
	bytes int64
	ops   []*op
	// cur is the source transaction of the last record written,
	// curBytes what that transaction holds, and curEnded whether the
	// record ended it.
	cur      string
	curBytes int64
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
	// cols are the row's columns, or the key's; vals their values.
	cols []string
	vals []any
	size int64
}

func (o *op) same(p *op) bool {
	return o.table == p.table && o.del == p.del && slices.Equal(o.cols, p.cols)
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

// Provision checks the params and reads the password and TLS files; nothing
// is sent until the first write.
func (s *Sink) Provision(ctx *module.Context) error {
	cfg, errs := wireConfig(ctx, connParams{Address: s.Address, Username: s.Username, PasswordEnv: s.PasswordEnv, Database: s.Database,
		TLS: s.TLS, CAFile: s.CAFile, CertFile: s.CertFile, KeyFile: s.KeyFile, ServerName: s.ServerName, TLSRSA: s.TLSRSA,
		ServerPublicKeyFile: s.ServerPublicKeyFile, Timeout: s.Timeout})
	s.cfg = cfg
	if s.dial == nil {
		s.dial = dialWire
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
	if s.KeepsCheckpoint() {
		ck := s.CheckpointTable
		if ck == "" {
			ck = DefaultCheckpointTable
		}
		if !strings.Contains(ck, ".") && s.Database == "" {
			errs = append(errs, ctx.Params.Field("checkpoint_table").Errorf("%s names no database, and database is not set", ck))
		}
		s.ckTable = quoteName(ck)
	}
	s.log = logs.Named(config.LoggerSync)
	s.writer.s = s
	if len(errs) == 0 {
		return nil
	}
	return errs
}

// Cleanup closes the connections, which rolls back a transaction left
// open: its records are written again from the checkpoint, which is never
// inside it. The spare writers are dropped.
func (s *Sink) Cleanup() {
	s.mu.Lock()
	spare := s.spare
	s.spare = nil
	s.mu.Unlock()
	s.writer.close()
	for _, w := range spare {
		w.close()
	}
}

// close closes the writer's connection.
func (w *writer) close() {
	if w.conn != nil {
		w.conn.Close()
		w.conn = nil
	}
}

// ParallelWrites marks the sink as one a job may write through several
// writers at once, each on a connection of its own, a key always going to
// the same one: a job's records have no source transactions, so that each
// Write commits what it writes and no writer holds a transaction into the
// next. A stream has one writer, the sink's first.
func (s *Sink) ParallelWrites() {}

// take returns a writer for a Write: the first when it is free, else a
// spare, made when there is none.
func (s *Sink) take() *writer {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.busy {
		s.busy = true
		s.writer.s = s
		return &s.writer
	}
	if n := len(s.spare); n > 0 {
		w := s.spare[n-1]
		s.spare = s.spare[:n-1]
		return w
	}
	return &writer{s: s}
}

// give returns a writer taken.
func (s *Sink) give(w *writer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if w == &s.writer {
		s.busy = false
		return
	}
	s.spare = append(s.spare, w)
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
	if _, err := s.conn.Exec(ctx, "CREATE TABLE IF NOT EXISTS "+s.ckTable+" (`pipeline` VARCHAR(191) NOT NULL PRIMARY KEY, "+
		"`lineage` VARCHAR(64) NOT NULL, `position` LONGTEXT NOT NULL, `updated` DATETIME(6) NOT NULL)"); err != nil {
		s.Cleanup()
		return nil, "", err
	}
	row, err := s.conn.QueryRow(ctx, "SELECT `lineage`, `position` FROM "+s.ckTable+" WHERE `pipeline` = ?", pipeline)
	if err != nil {
		s.Cleanup()
		return nil, "", err
	}
	s.pipeline, s.lineage = pipeline, lineage
	if len(row) < 2 {
		return nil, "", nil
	}
	return fitsync.Position(text(row[1])), text(row[0]), nil
}

// text is a column's value as text.
func text(v any) string {
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return fmt.Sprint(v)
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
		s.log.Infof("sync mysql: a DDL of %s is not applied: %s", r.Namespace, what)
		return it
	case fitsync.OpInsert, fitsync.OpUpdate, fitsync.OpMessage, fitsync.OpDelete:
	default:
		it.err = fmt.Errorf("a record of operation %s", r.Op)
		return it
	}
	table := s.tableOf(r)
	if table == "" {
		it.err = errors.New("the record's table is empty")
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
	up := &op{rec: i, table: table, cols: make([]string, 0, len(fields)), vals: make([]any, 0, len(fields))}
	for _, f := range fields {
		v, err := arg(f.Value)
		if err != nil {
			it.err = fmt.Errorf("column %s: %w", f.Name, err)
			return it
		}
		up.cols = append(up.cols, f.Name)
		up.vals = append(up.vals, v)
		up.size += argSize(v)
	}
	// An update that changed the key deletes the row of the key it had.
	if r.Op == fitsync.OpUpdate && r.Key.Kind() == fitsync.KindMap && r.Before.Kind() == fitsync.KindMap {
		var old []fitsync.Field
		changed := false
		for _, f := range r.Key.Fields() {
			v, ok := r.Before.Get(f.Name)
			if !ok {
				old = nil
				break
			}
			old = append(old, fitsync.Field{Name: f.Name, Value: v})
			changed = changed || !v.Equal(f.Value)
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
	if key.Kind() != fitsync.KindMap || len(key.Fields()) == 0 {
		return nil, fmt.Errorf("the key is a %s, not a map of columns to delete by", key.Kind())
	}
	o := &op{rec: i, table: table, del: true}
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
		o.size += argSize(v)
	}
	return o, nil
}

// arg is v as the client's parameter: times in UTC, which the session's
// time_zone is, so that a TIMESTAMP holds the instant and a DATETIME its
// UTC wall clock; decimals as their text, JSON, arrays and maps as JSON
// text.
func arg(v fitsync.Value) (any, error) {
	switch v.Kind() {
	case fitsync.KindNull:
		return nil, nil
	case fitsync.KindBool:
		return v.AsBool(), nil
	case fitsync.KindInt:
		return v.AsInt(), nil
	case fitsync.KindUint:
		return v.AsUint(), nil
	case fitsync.KindFloat:
		f := v.AsFloat()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("%v has no column type to go in", f)
		}
		return f, nil
	case fitsync.KindDecimal, fitsync.KindString:
		return v.Text(), nil
	case fitsync.KindBytes:
		return []byte(v.Text()), nil
	case fitsync.KindTime, fitsync.KindDate:
		return v.AsTime(), nil
	case fitsync.KindJSON:
		return json.RawMessage(v.Text()), nil
	}
	b, err := fitsync.AppendPlain(nil, v)
	return json.RawMessage(b), err
}

// argSize reckons what a parameter takes.
func argSize(v any) int64 {
	switch v := v.(type) {
	case string:
		return int64(len(v)) + 1
	case []byte:
		return int64(len(v)) + 1
	case json.RawMessage:
		return int64(len(v)) + 1
	}
	return 8
}

// permanent tells a server's error that writing the statement again
// cannot cure: a value the column refuses, a constraint, a table or column
// missing, a statement refused. A deadlock, a lock wait timed out, a
// conflict at commit and a connection's errors pass.
func permanent(err error) bool {
	var se *mysqlwire.Error
	if !errors.As(err, &se) {
		return false
	}
	switch se.Code {
	case 1205, 1213, 8002, 8022, 9007:
		return false
	case 1300, 1364, 1366, 1411, 3140:
		return true
	}
	return strings.HasPrefix(se.State, "22") || strings.HasPrefix(se.State, "23") || strings.HasPrefix(se.State, "42")
}

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
	w := s.take()
	defer s.give(w)
	return w.write(ctx, batch)
}

// write is Write on w.
func (w *writer) write(ctx context.Context, batch []*fitsync.Record) error {
	s := w.s
	items := make([]item, len(batch))
	for i, r := range batch {
		items[i] = s.build(i, r)
	}
	if err := w.connect(ctx); err != nil {
		return err
	}
	// With transactions batch, the server's transaction is committed at
	// the batch's last record that ends a source transaction: one left
	// open across batches holds a part of a single source transaction,
	// whose end is not acknowledged, so that the checkpoint never passes
	// a record the server could still roll back.
	lastEnd := -1
	for i := range items {
		if items[i].ends {
			lastEnd = i
		}
	}
	var g []*op
	// refuse commits what is before record i, the checkpoint at the last
	// of them that ends a source transaction, not at those after it whose
	// statements were not run yet.
	startPos := w.tx.pos
	refuse := func(i int, err error) error {
		w.tx.pos = startPos
		for j := i - 1; j >= 0; j-- {
			if items[j].pos != nil {
				w.tx.pos = items[j].pos
				break
			}
		}
		return w.refuse(ctx, i, err)
	}
	fail := func(err error) error {
		var br *badRecord
		if errors.As(err, &br) {
			return refuse(br.rec, br.err)
		}
		w.reset()
		return err
	}
	flush := func() error {
		err := w.flush(ctx, g)
		g = g[:0]
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
		if !s.batch && w.tx.open && it.unit != w.tx.unit {
			if err := flush(); err != nil {
				return fail(err)
			}
			if err := w.commit(ctx); err != nil {
				return fail(err)
			}
		}
		if w.tx.curEnded || it.unit != w.tx.cur || it.unit == "" {
			w.tx.cur, w.tx.curBytes = it.unit, 0
		}
		w.tx.curEnded = it.ends
		for _, o := range it.ops {
			if !w.tx.open {
				if _, err := w.conn.Exec(ctx, "START TRANSACTION"); err != nil {
					return fail(err)
				}
				w.tx.open, w.tx.unit = true, it.unit
			}
			if len(g) > 0 && !g[0].same(o) {
				if err := flush(); err != nil {
					return fail(err)
				}
			}
			g = append(g, o)
			w.tx.bytes += o.size
			w.tx.curBytes += o.size
		}
		if it.pos != nil {
			w.tx.pos = it.pos
		}
		if !w.tx.open {
			continue
		}
		split := w.maxTx > 0 && !it.ends && w.tx.curBytes >= w.maxTx
		ended := it.ends && (s.batch && i == lastEnd || !s.batch && it.unit != "")
		if ended || w.maxTx > 0 && it.ends && w.tx.bytes >= w.maxTx || split {
			if err := flush(); err != nil {
				return fail(err)
			}
			if split {
				s.log.Warnf("sync mysql: source transaction %s holds more than max_transaction_bytes (%d): it is committed in parts", it.unit, w.maxTx)
				w.tx.curBytes = 0
			}
			if err := w.commit(ctx); err != nil {
				return fail(err)
			}
		}
	}
	if err := flush(); err != nil {
		return fail(err)
	}
	if w.tx.open && !s.batch && len(items) > 0 && items[len(items)-1].unit == "" {
		// Records without a source transaction, a run of them at the
		// batch's end.
		if err := w.commit(ctx); err != nil {
			return fail(err)
		}
	}
	w.acked = w.tx
	w.acked.ops = slices.Clone(w.tx.ops)
	return nil
}

// refuse commits what is written before record i, which the server
// refuses for good, and returns the BatchError that says so: the records
// before it acknowledged, even a part of a source transaction, which
// on_error stop writes again from the checkpoint.
func (w *writer) refuse(ctx context.Context, i int, err error) error {
	if w.tx.open {
		if cerr := w.commit(ctx); cerr != nil {
			w.reset()
			return cerr
		}
	}
	w.acked = txState{}
	w.tx = txState{}
	return &fitsync.BatchError{Index: i, Err: fitsync.Permanent(err)}
}

// connect makes the connection when there is none or it broke, and writes
// again what the transaction the last Write left open held.
func (w *writer) connect(ctx context.Context) error {
	s := w.s
	if w.conn != nil && w.conn.Err() == nil {
		return nil
	}
	w.close()
	c, err := s.dial(ctx, s.cfg)
	if err != nil {
		return err
	}
	w.tidb = strings.Contains(c.ServerVersion(), "TiDB")
	w.maxTx = s.MaxTransactionBytes
	if w.tidb && w.maxTx <= 0 {
		w.maxTx = DefaultTiDBTransactionBytes
	}
	// A key of 0 is written as 0, not a new AUTO_INCREMENT or AUTO_RANDOM
	// value.
	setup := []string{"SET time_zone = '+00:00'", "SET sql_mode = CONCAT_WS(',', NULLIF(@@sql_mode, ''), 'NO_AUTO_VALUE_ON_ZERO')"}
	if w.tidb {
		// The source's values go in AUTO_RANDOM columns as they are.
		setup = append(setup, "SET @@session.allow_auto_random_explicit_insert = 1")
	}
	for _, q := range setup {
		if _, err := c.Exec(ctx, q); err != nil {
			c.Close()
			return err
		}
	}
	w.conn = c
	w.tx = w.acked
	w.tx.open = false
	w.tx.ops = nil
	if !w.acked.open {
		return nil
	}
	if _, err := c.Exec(ctx, "START TRANSACTION"); err != nil {
		w.reset()
		return err
	}
	w.tx.open = true
	for i := 0; i < len(w.acked.ops); {
		j := i + 1
		for j < len(w.acked.ops) && w.acked.ops[i].same(w.acked.ops[j]) {
			j++
		}
		if err := w.exec(ctx, w.acked.ops[i:j]); err != nil {
			w.reset()
			return err
		}
		i = j
	}
	w.tx.ops = slices.Clone(w.acked.ops)
	return nil
}

// reset closes the connection after an error, which rolls back the
// transaction open: the next Write connects again and writes what was
// acknowledged in it again.
func (w *writer) reset() {
	w.close()
	w.tx = w.acked
}

func (w *writer) commit(ctx context.Context) error {
	s := w.s
	if s.pipeline != "" && w.tx.pos != nil {
		row := [][]any{{s.pipeline, s.lineage, string(w.tx.pos), time.Now().UTC()}}
		if _, err := w.conn.InsertRows(ctx, "INSERT INTO "+s.ckTable+" (`pipeline`,`lineage`,`position`,`updated`) VALUES", row,
			"ON DUPLICATE KEY UPDATE `lineage`=VALUES(`lineage`),`position`=VALUES(`position`),`updated`=VALUES(`updated`)"); err != nil {
			return err
		}
	}
	if _, err := w.conn.Exec(ctx, "COMMIT"); err != nil {
		return err
	}
	cur, curBytes, curEnded := w.tx.cur, w.tx.curBytes, w.tx.curEnded
	w.tx = txState{cur: cur, curBytes: curBytes, curEnded: curEnded}
	return nil
}

// flush writes g, ops of one table and columns, as one statement or a few;
// on an error the server gives for good, the statement undone, it writes
// them one at a time to find the record at fault.
func (w *writer) flush(ctx context.Context, g []*op) error {
	if len(g) == 0 {
		return nil
	}
	err := w.exec(ctx, g)
	if err == nil {
		w.tx.ops = append(w.tx.ops, g...)
		return nil
	}
	if !permanent(err) {
		return err
	}
	if len(g) > 1 {
		for k := range g {
			if err = w.exec(ctx, g[k:k+1]); err != nil {
				if !permanent(err) {
					return err
				}
				w.tx.ops = append(w.tx.ops, g[:k]...)
				return &badRecord{rec: g[k].rec, err: err}
			}
		}
		w.tx.ops = append(w.tx.ops, g...)
		return nil
	}
	return &badRecord{rec: g[0].rec, err: err}
}

// exec runs ops of one table and columns: upserts as multi-row INSERTs,
// deletes as DELETEs of up to maxDeleteKeys keys.
func (w *writer) exec(ctx context.Context, ops []*op) error {
	o := ops[0]
	table := quoteName(o.table)
	cols := make([]string, len(o.cols))
	for i, c := range o.cols {
		cols[i] = quoteIdent(c)
	}
	if !o.del {
		rows := make([][]any, len(ops))
		for i, p := range ops {
			rows[i] = p.vals
		}
		var tail strings.Builder
		tail.WriteString("ON DUPLICATE KEY UPDATE ")
		for i, c := range cols {
			if i > 0 {
				tail.WriteByte(',')
			}
			fmt.Fprintf(&tail, "%s=VALUES(%s)", c, c)
		}
		_, err := w.conn.InsertRows(ctx, "INSERT INTO "+table+" ("+strings.Join(cols, ",")+") VALUES", rows, tail.String())
		return err
	}
	per := min(maxDeleteKeys, mysqlwire.MaxPlaceholders/len(cols))
	tuple := "(" + strings.Repeat("?,", len(cols)-1) + "?)"
	target := strings.Join(cols, ",")
	if len(cols) > 1 {
		target = "(" + target + ")"
	} else {
		tuple = "?"
	}
	for len(ops) > 0 {
		n := min(len(ops), per)
		var q strings.Builder
		q.WriteString("DELETE FROM " + table + " WHERE " + target + " IN (")
		args := make([]any, 0, n*len(cols))
		for i, p := range ops[:n] {
			if i > 0 {
				q.WriteByte(',')
			}
			q.WriteString(tuple)
			args = append(args, p.vals...)
		}
		q.WriteByte(')')
		if _, err := w.conn.Exec(ctx, q.String(), args...); err != nil {
			return err
		}
		ops = ops[n:]
	}
	return nil
}

// quoteIdent quotes a name as MySQL does, in backticks.
func quoteIdent(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

// quoteName quotes a table's name, db.table as two names.
func quoteName(name string) string {
	if db, table, ok := strings.Cut(name, "."); ok {
		return quoteIdent(db) + "." + quoteIdent(table)
	}
	return quoteIdent(name)
}
