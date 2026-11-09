package syncmssql

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	mssqlwire "github.com/lesismal/fit/sync/mssql/wire"
)

// sinkFake is a SQL Server for the sink's statements: tables of rows by
// key, which it reads the sink's MERGEs, UPDATE-then-INSERTs, DELETEs and
// bulk loads into, in transactions it can roll back.
type sinkFake struct {
	mu sync.Mutex
	// tables are rows by table (as quoted) and key; a row is its columns'
	// values.
	tables map[string]map[string]map[string]any
	// keyCols are each table's key's columns, as the statements name them.
	keyCols map[string][]string
	stage   []map[string]any
	// snap is the tables at the transaction's start; inTx says one is
	// open.
	snap map[string]map[string]map[string]any
	inTx bool
	// doom makes an error roll the transaction back, as XACT_ABORT does;
	// otherwise the statement alone is undone.
	doom bool
	// fail fails the statements holding a key, as many times as left,
	// for good when left is negative.
	fail map[string]failure
	// broken breaks the connection when a statement holds it.
	broken  string
	types   map[string][]mssqlwire.Column
	log     []string
	dials   int
	dialErr error
}

type failure struct {
	err  error
	left int
}

func newSinkFake() *sinkFake {
	return &sinkFake{tables: map[string]map[string]map[string]any{}, keyCols: map[string][]string{}, fail: map[string]failure{},
		types: map[string][]mssqlwire.Column{}}
}

// failOn makes statements holding key fail with err, n times (forever
// when n < 0).
func (f *sinkFake) failOn(key string, err error, n int) {
	f.mu.Lock()
	f.fail[key] = failure{err, n}
	f.mu.Unlock()
}

func (f *sinkFake) dial(context.Context, mssqlwire.Config) (sinkConn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dials++
	if f.dialErr != nil {
		return nil, f.dialErr
	}
	f.inTx, f.snap = false, nil
	return &sinkFakeConn{f: f}, nil
}

// rows returns table's rows as "key=col:val,..." lines, sorted.
func (f *sinkFake) rows(table string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.tables[table] {
		var cs []string
		for c, v := range r {
			cs = append(cs, fmt.Sprintf("%s:%v", c, v))
		}
		sort.Strings(cs)
		out = append(out, strings.Join(cs, ","))
	}
	sort.Strings(out)
	return out
}

func copyTables(t map[string]map[string]map[string]any) map[string]map[string]map[string]any {
	out := map[string]map[string]map[string]any{}
	for name, rows := range t {
		m := map[string]map[string]any{}
		for k, r := range rows {
			m[k] = maps.Clone(r)
		}
		out[name] = m
	}
	return out
}

type sinkFakeConn struct {
	f      *sinkFake
	broken error
	closed bool
}

func (c *sinkFakeConn) Err() error { return c.broken }

// Close closes the connection, which rolls back its transaction.
func (c *sinkFakeConn) Close() error {
	c.closed = true
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	if c.f.inTx {
		c.f.tables = c.f.snap
	}
	c.f.inTx, c.f.snap = false, nil
	return nil
}

func (c *sinkFakeConn) Begin(context.Context, string) error {
	f := c.f
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, "BEGIN")
	if err := c.failing("BEGIN"); err != nil {
		return err
	}
	f.snap, f.inTx = copyTables(f.tables), true
	return nil
}

func (c *sinkFakeConn) Commit(context.Context) error {
	f := c.f
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, "COMMIT")
	if err := c.failing("COMMIT"); err != nil {
		return err
	}
	f.snap, f.inTx = nil, false
	return nil
}

func (c *sinkFakeConn) Rollback(context.Context) error {
	f := c.f
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, "ROLLBACK")
	if f.inTx {
		f.tables = f.snap
	}
	f.snap, f.inTx = nil, false
	return nil
}

// failing returns the error of a failure whose key sql holds; f.mu held.
func (c *sinkFakeConn) failing(sql string) error {
	f := c.f
	if f.broken != "" && strings.Contains(sql, f.broken) {
		f.broken = ""
		c.broken = errors.New("the connection broke")
		return c.broken
	}
	for k, fl := range f.fail {
		if strings.Contains(sql, k) {
			if fl.left == 0 {
				continue
			}
			fl.left--
			f.fail[k] = fl
			return fl.err
		}
	}
	return nil
}

func plainArg(a any) any {
	if p, ok := a.(mssqlwire.Param); ok {
		return p.Value
	}
	return a
}

var (
	mergeRE  = regexp.MustCompile(`^MERGE INTO (\S+) WITH \(HOLDLOCK\) AS \[t\] USING (.*?) AS \[s\](?: \((.*?)\))? ON (.*?)(?: WHEN|;)`)
	onRE     = regexp.MustCompile(`\[t\]\.(\[[^\]]+\]) = \[s\]`)
	insertRE = regexp.MustCompile(`INSERT INTO (\S+) \((.*?)\) VALUES \((.*?)\);`)
	whereRE  = regexp.MustCompile(`(\[[^\]]+\]) = @p(\d+)`)
	deleteRE = regexp.MustCompile(`^DELETE FROM (\S+) WHERE \((.*?)\)`)
	stageRE  = regexp.MustCompile(`^INSERT INTO (\S+) \((.*?)\) SELECT .* FROM #fit_stage AS \[s\] WHERE NOT EXISTS \(SELECT 1 FROM \S+ AS \[t\] WHERE (.*?)\);`)
	identRE  = regexp.MustCompile(`^SET IDENTITY_INSERT \S+ ON; (.*) SET IDENTITY_INSERT \S+ OFF;$`)
)

func splitCols(list string) []string {
	var out []string
	for _, c := range strings.Split(list, ", ") {
		out = append(out, strings.TrimPrefix(strings.TrimSpace(c), "[s]."))
	}
	return out
}

// upsert writes a row of cols' values into table by its key's columns;
// f.mu held.
func (f *sinkFake) upsert(table string, keys []string, row map[string]any) error {
	for _, v := range row {
		if v == "bad" {
			return &mssqlwire.Error{Number: 245, Class: 16, Message: "Conversion failed"}
		}
	}
	f.keyCols[table] = keys
	var k []string
	for _, c := range keys {
		k = append(k, fmt.Sprint(row[c]))
	}
	if f.tables[table] == nil {
		f.tables[table] = map[string]map[string]any{}
	}
	key := strings.Join(k, "|")
	if old, ok := f.tables[table][key]; ok {
		maps.Copy(old, row)
		return nil
	}
	f.tables[table][key] = maps.Clone(row)
	return nil
}

func (c *sinkFakeConn) Exec(_ context.Context, sql string, args ...any) (int64, error) {
	f := c.f
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, sql)
	if c.broken != nil {
		return 0, c.broken
	}
	if err := c.failing(sql); err != nil {
		f.abort()
		return 0, err
	}
	saved := copyTables(f.tables)
	err := c.exec(sql, args)
	if err != nil {
		f.tables = saved
		f.abort()
	}
	return 0, err
}

// abort undoes the transaction when errors doom it; f.mu held.
func (f *sinkFake) abort() {
	if f.doom && f.inTx {
		f.tables, f.snap, f.inTx = f.snap, nil, false
	}
}

func (c *sinkFakeConn) exec(sql string, args []any) error {
	f := c.f
	if m := identRE.FindStringSubmatch(sql); m != nil {
		sql = m[1]
	}
	sql, dropStage := strings.CutSuffix(sql, " DROP TABLE #fit_stage;")
	defer func() {
		if dropStage {
			f.stage = nil
		}
	}()
	switch {
	case sql == "SET XACT_ABORT ON", sql == "SELECT 1", strings.HasPrefix(sql, "IF OBJECT_ID(@p1"):
		return nil
	case strings.HasPrefix(sql, "IF OBJECT_ID(N'tempdb"):
		f.stage = nil
		return nil
	case strings.HasPrefix(sql, "MERGE INTO"):
		m := mergeRE.FindStringSubmatch(sql)
		if m == nil {
			return fmt.Errorf("fake: a MERGE it cannot read: %s", sql)
		}
		var keys []string
		for _, k := range onRE.FindAllStringSubmatch(m[4], -1) {
			keys = append(keys, k[1])
		}
		if m[2] == stageTable {
			for _, r := range f.stage {
				if err := f.upsert(m[1], keys, r); err != nil {
					return err
				}
			}
			return nil
		}
		cs := splitCols(m[3])
		for i := 0; i+len(cs) <= len(args); i += len(cs) {
			row := map[string]any{}
			for j, col := range cs {
				row[col] = plainArg(args[i+j])
			}
			if err := f.upsert(m[1], keys, row); err != nil {
				return err
			}
		}
		return nil
	case strings.HasPrefix(sql, "UPDATE [t] SET"):
		// The staged rows: the UPDATE and the INSERT after it as one.
		_, ins, _ := strings.Cut(sql, "; ")
		return c.exec(ins, nil)
	case strings.HasPrefix(sql, "INSERT INTO") && strings.Contains(sql, "#fit_stage"):
		m := stageRE.FindStringSubmatch(sql)
		if m == nil {
			return fmt.Errorf("fake: an INSERT it cannot read: %s", sql)
		}
		var keys []string
		for _, k := range onRE.FindAllStringSubmatch(m[3], -1) {
			keys = append(keys, k[1])
		}
		for _, r := range f.stage {
			if err := f.upsert(m[1], keys, r); err != nil {
				return err
			}
		}
		return nil
	case strings.HasPrefix(sql, "UPDATE"), strings.HasPrefix(sql, "IF NOT EXISTS"):
		// UPDATE-then-INSERT, a row each: the INSERT's columns and
		// parameters, the key's from the WHERE before it.
		ins := insertRE.FindAllStringSubmatchIndex(sql, -1)
		prev := 0
		for _, ix := range ins {
			head := sql[prev:ix[0]]
			table, list, params := sql[ix[2]:ix[3]], sql[ix[4]:ix[5]], sql[ix[6]:ix[7]]
			prev = ix[1]
			var keys []string
			for _, w := range whereRE.FindAllStringSubmatch(head[strings.LastIndex(head, "WHERE"):], -1) {
				keys = append(keys, w[1])
			}
			row := map[string]any{}
			cs := splitCols(list)
			for j, p := range strings.Split(params, ", ") {
				n, _ := strconv.Atoi(strings.TrimPrefix(p, "@p"))
				row[cs[j]] = plainArg(args[n-1])
			}
			if err := f.upsert(table, keys, row); err != nil {
				return err
			}
		}
		return nil
	case strings.HasPrefix(sql, "DELETE FROM"):
		m := deleteRE.FindStringSubmatch(sql)
		if m == nil {
			return fmt.Errorf("fake: a DELETE it cannot read: %s", sql)
		}
		var cs []string
		for _, w := range whereRE.FindAllStringSubmatch(m[2], -1) {
			cs = append(cs, w[1])
		}
		for i := 0; i+len(cs) <= len(args); i += len(cs) {
			var k []string
			for j := range cs {
				if args[i+j] == "bad" {
					return &mssqlwire.Error{Number: 245, Class: 16, Message: "Conversion failed"}
				}
				k = append(k, fmt.Sprint(plainArg(args[i+j])))
			}
			delete(f.tables[m[1]], strings.Join(k, "|"))
		}
		return nil
	}
	return fmt.Errorf("fake: a statement it does not know: %s", sql)
}

func (c *sinkFakeConn) QueryAll(_ context.Context, sql string, args ...any) ([]mssqlwire.Column, [][]any, error) {
	f := c.f
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, sql)
	if c.broken != nil {
		return nil, nil, c.broken
	}
	if err := c.failing(sql); err != nil {
		return nil, nil, err
	}
	switch {
	case sql == "SELECT XACT_STATE()":
		if f.inTx {
			return nil, [][]any{{int64(1)}}, nil
		}
		return nil, [][]any{{int64(0)}}, nil
	case strings.HasPrefix(sql, "SELECT TOP 0 * FROM "):
		return f.types[strings.TrimPrefix(sql, "SELECT TOP 0 * FROM ")], nil, nil
	case strings.HasPrefix(sql, "SELECT [lineage], [position] FROM "):
		table, _, _ := strings.Cut(strings.TrimPrefix(sql, "SELECT [lineage], [position] FROM "), " ")
		r, ok := f.tables[table][fmt.Sprint(args[0])]
		if !ok {
			return nil, nil, nil
		}
		return nil, [][]any{{r["[lineage]"], r["[position]"]}}, nil
	}
	return nil, nil, fmt.Errorf("fake: a query it does not know: %s", sql)
}

func (c *sinkFakeConn) Bulk(_ context.Context, table string, columns []string) (bulkLoad, error) {
	f := c.f
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, "BULK "+table)
	if err := c.failing("BULK"); err != nil {
		return nil, err
	}
	return &sinkFakeBulk{f: f, cols: quoteAll(columns)}, nil
}

type sinkFakeBulk struct {
	f    *sinkFake
	cols []string
	rows []map[string]any
}

func (b *sinkFakeBulk) WriteRow(values ...any) error {
	row := map[string]any{}
	for i, v := range values {
		if v == "toolong" {
			return errors.New("mssql: the column v: a string too long")
		}
		row[b.cols[i]] = v
	}
	b.rows = append(b.rows, row)
	return nil
}

func (b *sinkFakeBulk) Close() (int64, error) {
	b.f.mu.Lock()
	defer b.f.mu.Unlock()
	if b.f.broken == "BULKCLOSE" {
		b.f.broken = ""
		return 0, errors.New("the load broke")
	}
	b.f.stage = append(b.f.stage, b.rows...)
	return int64(len(b.rows)), nil
}
