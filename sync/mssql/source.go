package syncmssql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	mssqlwire "github.com/lesismal/fit/sync/mssql/wire"
)

func init() {
	module.Register(module.Info{ID: fitsync.Sources + ".mssql", New: func() any { return new(Source) },
		Doc: "Follows SQL Server (2016 and later): a snapshot of the tables read by key without locks (in snapshot isolation where the database allows it), then their changes from the CDC change tables, polled by LSN from the checkpoint, the changes of each transaction together in the order the server made them; or, with mode change_tracking, the keys change tracking reports, their rows read back. Each row inserted, updated or deleted is a record of namespace schema.table, its key the primary key's columns (or keys'), a DDL CDC saw a ddl record. A table CDC starts to capture is read whole, then followed. A position the server no longer has (its changes cleaned up) is an error saying a new snapshot is needed; CDC or change tracking off is an error with the SQL that turns it on."})
}

// The defaults of the source's params.
const (
	DefaultBatchSize          = 1000
	MaxBatchSize              = 100000
	DefaultPollInterval       = time.Second
	DefaultWindowTransactions = 1000
	DefaultWindowRows         = 10000
)

// Source is the source mssql.
type Source struct {
	Address            string              `json:"address,omitempty" default:"\"127.0.0.1:1433\"" doc:"The server's host and port: SQL Server 2016 or later. 127.0.0.1:1433 when left out."`
	Username           string              `json:"username,omitempty" doc:"The SQL login, which needs SELECT on the tables, and on the cdc schema (db_owner, or the role CDC was enabled with) or VIEW CHANGE TRACKING on them; with auth, the Windows account (kerberos, ntlm). Needed but with auth's entra."`
	PasswordEnv        string              `json:"password_env,omitempty" doc:"The environment variable holding username's password, read when the pipeline loads; none when left out."`
	Database           string              `json:"database" fit:"required" doc:"The database whose tables are read."`
	Encrypt            string              `json:"encrypt,omitempty" enum:"on,strict,off" default:"\"on\"" doc:"How the connection is encrypted: on, TLS negotiated in TDS's pre-login, refused by a server that cannot; strict, TDS 8's TLS first (SQL Server 2022 and later; on Linux, 2025 and later); off, none, the password and rows in clear. The server's certificate is checked against the system's roots or ca_file's; the check cannot be turned off. on when left out."`
	CAFile             string              `json:"ca_file,omitempty" doc:"A PEM file of the certificates the server's is checked against, in place of the system's roots."`
	ServerName         string              `json:"server_name,omitempty" doc:"The name the server's certificate must have; address's host when left out."`
	Timeout            config.Duration     `json:"timeout,omitempty" default:"\"60s\"" doc:"How long connecting, and each wait for the server, may take. 60s when left out or 0."`
	Auth               *Auth               `json:"auth,omitempty" doc:"A login other than SQL authentication's username and password: Microsoft Entra ID (Azure SQL), or Windows authentication by Kerberos or NTLM. SQL authentication when left out."`
	Tables             []string            `json:"tables,omitempty" doc:"The tables read, as schema.table globs (* and ? within a name, [ranges]): dbo.orders, sales.*. A table named whole must have CDC (or change tracking) on, or the pipeline fails saying how to turn it on; a glob matches the tables that have it, and picks up a table CDC begins to capture later. Every table CDC (or change tracking) follows when left out."`
	Exclude            []string            `json:"exclude,omitempty" doc:"Tables left out of those tables names, as schema.table globs."`
	Keys               map[string][]string `json:"keys,omitempty" doc:"The key's columns of tables, by schema.table, in place of the primary key (or the unique index CDC was enabled with): for a table without one. A table with neither has records without a key, and its snapshot is read in one query."`
	Mode               string              `json:"mode,omitempty" enum:"cdc,change_tracking" default:"\"cdc\"" doc:"Where changes come from: cdc, the change tables, with whole rows before and after each change, every change in order; change_tracking, the keys changed since the last version, each row read back as it is then, an absent row a delete (lighter on the server, but changes between two polls are merged and rows have no before). cdc when left out."`
	Snapshot           string              `json:"snapshot,omitempty" enum:"initial,always,never" default:"\"initial\"" doc:"Whether the tables' rows are read before the changes: initial, when the pipeline has no checkpoint; always, at every start of the source, unless its checkpoint is inside a snapshot, which it goes on with; never, the changes alone, from the checkpoint or from now. The position is read first and the changes start there, so what changes during the snapshot comes again. initial when left out."`
	BatchSize          int                 `json:"batch_size,omitempty" min:"0" max:"100000" default:"1000" doc:"The rows a snapshot's query reads, by key, each in a transaction of its own. 1000 when left out or 0."`
	PollInterval       config.Duration     `json:"poll_interval,omitempty" default:"\"1s\"" doc:"How long the source waits, once it has read every change, before it looks again. 1s when left out or 0."`
	WindowTransactions int                 `json:"window_transactions,omitempty" min:"0" max:"100000" default:"1000" doc:"With cdc, the most transactions read from the change tables at once. 1000 when left out or 0."`
	WindowRows         int                 `json:"window_rows,omitempty" min:"0" max:"1000000" default:"10000" doc:"With cdc, the most changed rows held at once to put the change tables' rows in order: a window of transactions with more is read again smaller, and a single transaction with more is read in pages of this many. 10000 when left out or 0."`

	cfg  mssqlwire.Config
	dial func(context.Context, mssqlwire.Config) (conn, error)
	log  *logs.Logger
	// sleep waits between polls; tests replace it.
	sleep func(ctx context.Context, d time.Duration) error
}

// Provision checks the params and reads the password and CA file; nothing
// is sent until the source runs.
func (s *Source) Provision(ctx *module.Context) error {
	cfg, errs := WireConfig(ctx, ConnParams{Address: s.Address, Username: s.Username, PasswordEnv: s.PasswordEnv, Database: s.Database,
		Encrypt: s.Encrypt, CAFile: s.CAFile, ServerName: s.ServerName, Timeout: s.Timeout, Auth: s.Auth})
	s.cfg = cfg
	if s.dial == nil {
		s.dial = dialWire
	}
	if s.sleep == nil {
		s.sleep = sleep
	}
	s.log = logs.Named(config.LoggerSync)
	for name, globs := range map[string][]string{"tables": s.Tables, "exclude": s.Exclude} {
		for i, g := range globs {
			if _, err := path.Match(g, ""); err != nil || strings.Count(g, ".") != 1 {
				errs = append(errs, ctx.Params.Field(name).Index(i).Errorf("%q is not a schema.table glob", g))
			}
		}
	}
	for ns, cols := range s.Keys {
		if strings.Count(ns, ".") != 1 || len(cols) == 0 {
			errs = append(errs, ctx.Params.Field("keys").Errorf("%q: a key is a schema.table and its columns", ns))
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return errs
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Source) batchSize() int {
	if s.BatchSize <= 0 {
		return DefaultBatchSize
	}
	return min(s.BatchSize, MaxBatchSize)
}

func (s *Source) pollInterval() time.Duration {
	if s.PollInterval <= 0 {
		return DefaultPollInterval
	}
	return s.PollInterval.Std()
}

func (s *Source) windowTransactions() int {
	if s.WindowTransactions <= 0 {
		return DefaultWindowTransactions
	}
	return s.WindowTransactions
}

func (s *Source) windowRows() int {
	if s.WindowRows <= 0 {
		return DefaultWindowRows
	}
	return s.WindowRows
}

func (s *Source) tracking() bool { return s.Mode == "change_tracking" }

// included reports whether the source reads table ns, schema.table.
func (s *Source) included(ns string) bool {
	ok := len(s.Tables) == 0
	for _, g := range s.Tables {
		if m, _ := path.Match(g, ns); m {
			ok = true
			break
		}
	}
	for _, g := range s.Exclude {
		if m, _ := path.Match(g, ns); m {
			return false
		}
	}
	return ok
}

// Test connects and checks that CDC (or change tracking) is on for the
// database and the tables.
func (s *Source) Test(ctx context.Context) error {
	_, err := s.TestWarnings(ctx)
	return err
}

// TestWarnings tests as Test does, and returns what passes but loses data:
// the columns read through CDC whose non-ASCII characters reach the sinks
// as question marks (lossyWarning).
func (s *Source) TestWarnings(ctx context.Context) ([]string, error) {
	c, err := s.dial(ctx, s.cfg)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	srv, tables, err := s.discover(ctx, c)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, t := range tables {
		for _, col := range t.lossyColumns() {
			out = append(out, lossyWarning(srv, t, col))
		}
	}
	return out, nil
}

// lossyWarning says that CDC loses the non-ASCII characters of column col
// of table t, its collation's code page neither UTF-8's nor the
// database's (I215 decision 14).
func lossyWarning(srv server, t *table, col string) string {
	return fmt.Sprintf("the column %s of %s is of the collation %s, not the database's %s: CDC writes those of its non-ASCII characters the database's code page lacks into the change table as question marks, so they reach the sinks as ? in the changes (a snapshot reads the table and keeps them); keep the column in the database's collation or a UTF-8 one, or read the table with mode change_tracking",
		col, t.ns(), t.lossy[col], srv.collation)
}

// lossyColumns are the names of t.lossy's columns, in order.
func (t *table) lossyColumns() []string {
	cols := make([]string, 0, len(t.lossy))
	for col := range t.lossy {
		cols = append(cols, col)
	}
	sort.Strings(cols)
	return cols
}

// server is what the source learns of the database.
type server struct {
	// snapshotIso says the database allows snapshot isolation.
	snapshotIso bool
	// zone is the server's, which CDC's commit times are in.
	zone *time.Location
	// collation is the database's.
	collation string
}

// instance is a CDC capture instance of a table.
type instance struct {
	name string
	// start is its lowest LSN still in its change table; created when it
	// was made.
	start   lsn
	created time.Time
	cols    []string
	// casts are the columns read as nvarchar, and their lengths
	// (selectList).
	casts map[string]string
}

// table is a table the source reads.
type table struct {
	schema, name string
	// cols are the columns read: the newest capture instance's, or every
	// column with change tracking.
	cols []string
	// casts are the columns read as nvarchar, and their lengths
	// (selectList).
	casts map[string]string
	// lossy are the columns, of any of its capture instances, whose
	// collation's code page is neither UTF-8's nor the database's, and
	// their collations: CDC writes their non-ASCII characters as question
	// marks. None with change tracking.
	lossy map[string]string
	key   []string
	// instances are its capture instances, the oldest first.
	instances []*instance
}

func (t *table) ns() string   { return t.schema + "." + t.name }
func (t *table) from() string { return quoteName(t.schema) + "." + quoteName(t.name) }

func str(v any) string {
	s, _ := v.(string)
	return s
}

// discover checks the database and reads the tables the source reads, in
// name order.
func (s *Source) discover(ctx context.Context, c conn) (server, []*table, error) {
	var srv server
	_, rows, err := c.QueryAll(ctx, `SELECT is_cdc_enabled, snapshot_isolation_state, DATEPART(TZOFFSET, SYSDATETIMEOFFSET()),
CONVERT(nvarchar(128), DATABASEPROPERTYEX(DB_NAME(), 'Collation')) FROM sys.databases WHERE name = DB_NAME()`)
	if err != nil {
		return srv, nil, err
	}
	if len(rows) == 0 {
		return srv, nil, fmt.Errorf("mssql: the database %s is not in sys.databases", s.Database)
	}
	srv.snapshotIso = rows[0][1] == int64(1)
	off, _ := rows[0][2].(int64)
	srv.zone = time.FixedZone("", int(off)*60)
	srv.collation = str(rows[0][3])
	db := quoteName(s.Database)
	var tracked map[string]*table
	if s.tracking() {
		_, ok, err := c.QueryAll(ctx, "SELECT 1 FROM sys.change_tracking_databases WHERE database_id = DB_ID()")
		if err != nil {
			return srv, nil, err
		}
		if len(ok) == 0 {
			return srv, nil, fmt.Errorf("mssql: change tracking is off for the database %s; turn it on with: ALTER DATABASE %s SET CHANGE_TRACKING = ON (CHANGE_RETENTION = 2 DAYS, AUTO_CLEANUP = ON)", s.Database, db)
		}
		if tracked, err = s.trackedTables(ctx, c); err != nil {
			return srv, nil, err
		}
	} else {
		if rows[0][0] != true {
			return srv, nil, fmt.Errorf("mssql: CDC is off for the database %s; turn it on with: USE %s; EXEC sys.sp_cdc_enable_db (and run SQL Server Agent, whose jobs capture the changes)", s.Database, db)
		}
		if tracked, err = s.capturedTables(ctx, c); err != nil {
			return srv, nil, err
		}
	}
	// A table named whole must be followed; a glob matches the tables
	// that are, so that a table added later is picked up once it is.
	var missing []string
	for _, ns := range s.Tables {
		if strings.ContainsAny(ns, `*?[\`) || !s.included(ns) || tracked[ns] != nil {
			continue
		}
		schema, name, _ := strings.Cut(ns, ".")
		if s.tracking() {
			missing = append(missing, fmt.Sprintf("ALTER TABLE %s.%s ENABLE CHANGE_TRACKING", quoteName(schema), quoteName(name)))
		} else {
			missing = append(missing, fmt.Sprintf("EXEC sys.sp_cdc_enable_table @source_schema = N'%s', @source_name = N'%s', @role_name = NULL",
				strings.ReplaceAll(schema, "'", "''"), strings.ReplaceAll(name, "'", "''")))
		}
	}
	if len(missing) > 0 {
		what := "CDC does not capture"
		if s.tracking() {
			what = "change tracking does not follow"
		}
		return srv, nil, fmt.Errorf("mssql: %s %d of the tables named (or they do not exist); turn it on with: %s", what, len(missing), strings.Join(missing, "; "))
	}
	var out []*table
	for ns, t := range tracked {
		if cols := s.Keys[ns]; len(cols) > 0 {
			t.key = cols
		} else if t.key, err = keyColumns(ctx, c, t); err != nil {
			return srv, nil, err
		}
		out = append(out, t)
	}
	if len(out) == 0 {
		return srv, nil, errors.New("mssql: no table to read: none of the tables named is captured")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ns() < out[j].ns() })
	return srv, out, nil
}

// capturedTables reads CDC's capture instances and their columns.
func (s *Source) capturedTables(ctx context.Context, c conn) (map[string]*table, error) {
	_, rows, err := c.QueryAll(ctx, `SELECT s.name, t.name, ct.capture_instance, ct.start_lsn, ct.create_date, ct.index_name
FROM cdc.change_tables ct JOIN sys.tables t ON t.object_id = ct.source_object_id JOIN sys.schemas s ON s.schema_id = t.schema_id
ORDER BY s.name, t.name, ct.create_date`)
	if err != nil {
		return nil, err
	}
	out := map[string]*table{}
	for _, r := range rows {
		ns := str(r[0]) + "." + str(r[1])
		if !s.included(ns) {
			continue
		}
		t := out[ns]
		if t == nil {
			t = &table{schema: str(r[0]), name: str(r[1])}
			out[ns] = t
		}
		in := &instance{name: str(r[2]), start: lsnOf(r[3])}
		in.created, _ = r[4].(time.Time)
		// The change table's columns have the table's types and
		// collations; whether CDC loses a column's characters is read of
		// the table's own column (sc).
		_, cols, err := c.QueryAll(ctx, "SELECT cc.column_name, "+castLength("c.")+", "+lossyCollation("sc.")+` FROM cdc.captured_columns cc
LEFT JOIN sys.columns c ON c.object_id = cc.object_id AND c.name = cc.column_name
LEFT JOIN sys.columns sc ON sc.object_id = OBJECT_ID(@p2) AND sc.name = cc.column_name
WHERE cc.object_id = OBJECT_ID(@p1) ORDER BY cc.column_ordinal`, "cdc."+quoteName(in.name+"_CT"), t.from())
		if err != nil {
			return nil, err
		}
		var lossy map[string]string
		in.cols, in.casts, lossy = columnsOf(cols)
		for col, coll := range lossy {
			if t.lossy == nil {
				t.lossy = map[string]string{}
			}
			t.lossy[col] = coll
		}
		t.instances = append(t.instances, in)
		t.cols, t.casts = in.cols, in.casts
		if idx := str(r[5]); idx != "" && t.key == nil {
			t.key, err = indexColumns(ctx, c, t, "i.name = @p2", idx)
			if err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// trackedTables reads the tables change tracking follows, and their
// columns.
func (s *Source) trackedTables(ctx context.Context, c conn) (map[string]*table, error) {
	_, rows, err := c.QueryAll(ctx, `SELECT s.name, t.name FROM sys.change_tracking_tables ctt JOIN sys.tables t ON t.object_id = ctt.object_id
JOIN sys.schemas s ON s.schema_id = t.schema_id ORDER BY s.name, t.name`)
	if err != nil {
		return nil, err
	}
	out := map[string]*table{}
	for _, r := range rows {
		t := &table{schema: str(r[0]), name: str(r[1])}
		if !s.included(t.ns()) {
			continue
		}
		_, cols, err := c.QueryAll(ctx, "SELECT name, "+castLength("")+" FROM sys.columns WHERE object_id = OBJECT_ID(@p1) AND is_computed = 0 ORDER BY column_id", t.from())
		if err != nil {
			return nil, err
		}
		t.cols, t.casts, _ = columnsOf(cols)
		out[t.ns()] = t
	}
	return out, nil
}

// columnsOf reads rows of a column's name, when it is cast the length it
// is cast to (castLength), and when CDC loses its characters its collation
// (lossyCollation).
func columnsOf(rows [][]any) (cols []string, casts, lossy map[string]string) {
	for _, r := range rows {
		cols = append(cols, str(r[0]))
		if len(r) > 1 && str(r[1]) != "" {
			if casts == nil {
				casts = map[string]string{}
			}
			casts[str(r[0])] = str(r[1])
		}
		if len(r) > 2 && str(r[2]) != "" {
			if lossy == nil {
				lossy = map[string]string{}
			}
			lossy[str(r[0])] = str(r[2])
		}
	}
	return cols, casts, lossy
}

// keyColumns reads a table's primary key's columns, or keeps those of the
// index CDC was enabled with.
func keyColumns(ctx context.Context, c conn, t *table) ([]string, error) {
	pk, err := indexColumns(ctx, c, t, "i.is_primary_key = 1")
	if err != nil || len(pk) > 0 {
		return pk, err
	}
	return t.key, nil
}

func indexColumns(ctx context.Context, c conn, t *table, cond string, args ...any) ([]string, error) {
	_, rows, err := c.QueryAll(ctx, `SELECT c.name FROM sys.indexes i
JOIN sys.index_columns ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id
JOIN sys.columns c ON c.object_id = ic.object_id AND c.column_id = ic.column_id
WHERE i.object_id = OBJECT_ID(@p1) AND `+cond+` AND ic.key_ordinal > 0 ORDER BY ic.key_ordinal`, append([]any{t.from()}, args...)...)
	if err != nil {
		return nil, err
	}
	var cols []string
	for _, r := range rows {
		cols = append(cols, str(r[0]))
	}
	return cols, nil
}

// position is the source's checkpoint: the LSN of the last transaction
// read whole (cdc) or the change tracking version read up to.
type position struct {
	LSN     lsn   `json:"lsn,omitempty"`
	Version int64 `json:"version,omitempty"`
	// Since is when the tables read were last found, in the server's
	// clock: a table whose capture instances are all younger is new to
	// the source, which reads it whole before its changes.
	Since time.Time `json:"since,omitzero"`
	// Snapshot is where a snapshot is, while it is read; the position is
	// then where the changes start.
	Snapshot *snapPos `json:"snapshot,omitempty"`
}

// snapPos is where a snapshot is: the table being read, schema.table, and
// the last key read in it; Added says it is of the tables new since the
// position's Since, not of every table.
type snapPos struct {
	Table string          `json:"table,omitempty"`
	Key   json.RawMessage `json:"key,omitempty"`
	Added bool            `json:"added,omitempty"`
}

func (p position) marshal() fitsync.Position {
	b, _ := json.Marshal(p)
	return b
}

// errNeedSnapshot is the error of a position the server no longer has.
var errNeedSnapshot = errors.New("a new snapshot is needed (the pipeline's snapshot, or a reset of its checkpoint)")

// Run reads the snapshot, when the checkpoint is inside one or snapshot
// says so, then the changes from the checkpoint, from the snapshot's
// start, or from now.
func (s *Source) Run(ctx context.Context, r *fitsync.Run) error {
	var from *position
	if r.From != nil {
		from = new(position)
		if err := json.Unmarshal(r.From, from); err != nil {
			return fmt.Errorf("mssql: the checkpoint %s: %w", r.From, err)
		}
		if !s.tracking() && !validLSN(string(from.LSN)) {
			return fmt.Errorf("mssql: the checkpoint %s has no LSN", r.From)
		}
	}
	c, err := s.dial(ctx, s.cfg)
	if err != nil {
		return err
	}
	defer c.Close()
	rd := &reader{s: s, r: r, c: c}
	resume := from != nil && from.Snapshot != nil
	snap := !resume && (from == nil && s.Snapshot != "never" || s.Snapshot == "always")
	var head position
	if from == nil || snap {
		// Where the changes are, read before the tables are found and read,
		// so that what changes while they are comes again.
		if head, err = rd.head(ctx); err != nil {
			return err
		}
	}
	if rd.srv, rd.tables, err = s.discover(ctx, c); err != nil {
		return err
	}
	rd.warnLossy()
	for _, t := range rd.tables {
		if c := t.created(); c.After(head.Since) && !head.Since.IsZero() {
			head.Since = c
		}
	}
	switch {
	case resume:
		at := from.Snapshot
		from.Snapshot = nil
		if *from, err = rd.snapshot(ctx, *from, at); err != nil {
			return err
		}
	case snap:
		if from == nil {
			// The changes' start with the snapshot not begun is the
			// pipeline's checkpoint before its first record, as I175's.
			at := head
			at.Snapshot = &snapPos{}
			if err := r.SaveStart(at.marshal()); err != nil {
				return err
			}
		}
		if head, err = rd.snapshot(ctx, head, nil); err != nil {
			return err
		}
		from = &head
	case from == nil:
		if err := r.SaveStart(head.marshal()); err != nil {
			return err
		}
		from = &head
	}
	if s.tracking() {
		return rd.track(ctx, *from)
	}
	return rd.follow(ctx, *from)
}

// reader is a run of the source.
type reader struct {
	s      *Source
	r      *fitsync.Run
	c      conn
	srv    server
	tables []*table
	// lagAt is when the lag was last counted.
	lagAt time.Time
	// warned are the columns warnLossy logged, as schema.table.column and
	// collation.
	warned map[string]bool
}

// warnLossy logs, once per column in the run, each column of the tables
// read through CDC whose non-ASCII characters CDC writes as question
// marks (I215 decision 14).
func (rd *reader) warnLossy() {
	for _, t := range rd.tables {
		for _, col := range t.lossyColumns() {
			k := t.ns() + "." + col + " " + t.lossy[col]
			if rd.warned[k] {
				continue
			}
			if rd.warned == nil {
				rd.warned = map[string]bool{}
			}
			rd.warned[k] = true
			rd.s.log.Warnf("sync mssql: %s", lossyWarning(rd.srv, t, col))
		}
	}
}

// head is where the changes are now: CDC's highest LSN, or change
// tracking's current version, and the server's clock.
func (rd *reader) head(ctx context.Context) (position, error) {
	q := "SELECT sys.fn_cdc_get_max_lsn(), GETDATE()"
	if rd.s.tracking() {
		q = "SELECT CHANGE_TRACKING_CURRENT_VERSION(), GETDATE()"
	}
	_, rows, err := rd.c.QueryAll(ctx, q)
	if err != nil {
		return position{}, err
	}
	p := position{LSN: lsnOf(rows[0][0])}
	p.Since, _ = rows[0][1].(time.Time)
	if rd.s.tracking() {
		p.LSN = ""
		p.Version, _ = rows[0][0].(int64)
	} else if p.LSN == "" {
		p.LSN = zeroLSN
	}
	return p, nil
}

// zeroLSN is the LSN before every other.
const zeroLSN = lsn("00000000000000000000")

// created is when the table's oldest capture instance was made.
func (t *table) created() time.Time {
	if len(t.instances) == 0 {
		return time.Time{}
	}
	return t.instances[0].created
}
