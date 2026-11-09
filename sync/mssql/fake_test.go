package syncmssql

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	mssqlwire "github.com/lesismal/fit/sync/mssql/wire"
)

// fake is a SQL Server for the source's queries: tables of an int key id
// and a value v, CDC's capture instances, change rows and transactions,
// and change tracking's versions.
type fake struct {
	mu        sync.Mutex
	cdc, ct   bool
	snapIso   bool
	tz        int64
	tables    map[string]*fakeTable
	instances []*fakeInst
	mapping   []fakeTx
	ddls      []fakeDDL
	max       uint64
	minLSN    uint64
	now       time.Time
	ctVersion int64
	minValid  int64
	// fail fails the queries holding a key, once each when once.
	fail    map[string]error
	once    bool
	// skip lets that many of a failing key's queries pass first.
	skip map[string]int
	// noDB answers sys.databases with no row.
	noDB    bool
	queries []string
	txs     []string
}

type fakeTable struct {
	schema, name string
	cols         []string
	pk           []string
	rows         map[int64][]any
	ct           bool
	// ctChanges are change tracking's: a key's last change and version.
	ctChanges map[int64]fakeCT
}

type fakeCT struct {
	op string
	v  int64
}

type fakeInst struct {
	table   *fakeTable
	name    string
	start   uint64
	created time.Time
	index   string
	cols    []string
	changes []fakeChange
}

type fakeChange struct {
	lsn, seq uint64
	op       int64
	row      []any
}

type fakeTx struct {
	lsn uint64
	at  time.Time
}

type fakeDDL struct {
	lsn   uint64
	table string
	cmd   string
}

func newFake() *fake {
	return &fake{cdc: true, snapIso: true, tables: map[string]*fakeTable{}, fail: map[string]error{}, skip: map[string]int{}, now: time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)}
}

func lsnBytes(n uint64) []byte {
	b := make([]byte, 10)
	binary.BigEndian.PutUint64(b[2:], n)
	return b
}

func lsnNum(v any) uint64 {
	b, _ := v.([]byte)
	if len(b) != 10 {
		return 0
	}
	return binary.BigEndian.Uint64(b[2:])
}

// table adds a table of id and v, rows ids 1 to n.
func (f *fake) table(name string, n int) *fakeTable {
	t := &fakeTable{schema: "dbo", name: name, cols: []string{"id", "v"}, pk: []string{"id"}, rows: map[int64][]any{}, ctChanges: map[int64]fakeCT{}}
	for i := 1; i <= n; i++ {
		t.rows[int64(i)] = []any{int64(i), fmt.Sprint("r", i)}
	}
	f.tables["dbo."+name] = t
	return t
}

// capture adds a capture instance of t, starting at the next LSN.
func (f *fake) capture(t *fakeTable, name string) *fakeInst {
	in := &fakeInst{table: t, name: name, start: f.max + 1, created: f.now, cols: t.cols}
	f.now = f.now.Add(time.Second)
	f.instances = append(f.instances, in)
	return in
}

// commit commits a transaction of changes, each of an instance: op and
// row.
func (f *fake) commit(changes ...any) uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.max++
	l := f.max
	f.mapping = append(f.mapping, fakeTx{lsn: l, at: f.now})
	f.now = f.now.Add(time.Second)
	for i := 0; i+2 < len(changes); i += 3 {
		in, op, row := changes[i].(*fakeInst), changes[i+1].(int64), changes[i+2].([]any)
		seq := uint64(i/3 + 1)
		if op == cdcUpdateAfter {
			seq--
		}
		in.changes = append(in.changes, fakeChange{lsn: l, seq: seq, op: op, row: row})
		switch op {
		case cdcInsert, cdcUpdateAfter:
			in.table.rows[row[0].(int64)] = row
		case cdcDelete:
			delete(in.table.rows, row[0].(int64))
		}
	}
	return l
}

func (f *fake) dial(context.Context, mssqlwire.Config) (conn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failing("dial"); err != nil {
		return nil, err
	}
	return &fakeConn{f: f}, nil
}

func (f *fake) failing(q string) error {
	for k, err := range f.fail {
		if strings.Contains(q, k) {
			if f.skip[k] > 0 {
				f.skip[k]--
				continue
			}
			if f.once {
				delete(f.fail, k)
			}
			return err
		}
	}
	return nil
}

type fakeConn struct {
	f      *fake
	closed bool
}

func (c *fakeConn) Close() error { c.closed = true; return nil }

func (c *fakeConn) Begin(_ context.Context, iso string) error {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	c.f.txs = append(c.f.txs, "begin "+iso)
	return c.f.failing("BEGIN")
}

func (c *fakeConn) Commit(context.Context) error {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	c.f.txs = append(c.f.txs, "commit")
	return c.f.failing("COMMIT")
}

func (c *fakeConn) Rollback(context.Context) error {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	c.f.txs = append(c.f.txs, "rollback")
	return nil
}

func (c *fakeConn) Each(ctx context.Context, fn func([]mssqlwire.Column, []any) error, sql string, args ...any) error {
	cols, rows, err := c.QueryAll(ctx, sql, args...)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if err := fn(cols, r); err != nil {
			return err
		}
	}
	return nil
}

var (
	topRE  = regexp.MustCompile(`TOP \((\d+)\)`)
	instRE = regexp.MustCompile(`\[fn_cdc_get_all_changes_([^\]]+)\]`)
	fromRE = regexp.MustCompile(`FROM \[([^\]]+)\]\.\[([^\]]+)\]`)
	ctRE   = regexp.MustCompile(`CHANGES \[([^\]]+)\]\.\[([^\]]+)\]`)
)

func cols(names ...string) []mssqlwire.Column {
	out := make([]mssqlwire.Column, len(names))
	for i, n := range names {
		out[i] = mssqlwire.Column{Name: n, Type: mssqlwire.TypeNVarChar}
		if n == "id" {
			out[i].Type = mssqlwire.TypeIntN
		}
	}
	return out
}

func argOf(a any) any {
	if p, ok := a.(mssqlwire.Param); ok {
		return p.Value
	}
	return a
}

func (c *fakeConn) QueryAll(_ context.Context, sql string, args ...any) ([]mssqlwire.Column, [][]any, error) {
	f := c.f
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, sql)
	if err := f.failing(sql); err != nil {
		return nil, nil, err
	}
	top := 1 << 30
	if m := topRE.FindStringSubmatch(sql); m != nil {
		top, _ = strconv.Atoi(m[1])
	}
	switch {
	case strings.HasPrefix(sql, "SELECT is_cdc_enabled"):
		if f.noDB {
			return nil, nil, nil
		}
		iso := int64(0)
		if f.snapIso {
			iso = 1
		}
		return nil, [][]any{{f.cdc, iso, f.tz}}, nil
	case strings.Contains(sql, "sys.change_tracking_databases"):
		if f.ct {
			return nil, [][]any{{int64(1)}}, nil
		}
		return nil, nil, nil
	case strings.Contains(sql, "FROM cdc.change_tables ct JOIN"):
		var rows [][]any
		for _, in := range f.instances {
			var idx any
			if in.index != "" {
				idx = in.index
			}
			rows = append(rows, []any{in.table.schema, in.table.name, in.name, lsnBytes(in.start), in.created, idx})
		}
		return nil, rows, nil
	case strings.HasPrefix(sql, "SELECT capture_instance"):
		var rows [][]any
		for _, in := range f.instances {
			rows = append(rows, []any{in.name})
		}
		return nil, rows, nil
	case strings.Contains(sql, "cdc.captured_columns"):
		for _, in := range f.instances {
			if args[0] == "cdc."+quoteName(in.name+"_CT") {
				var rows [][]any
				for _, col := range in.cols {
					rows = append(rows, []any{col})
				}
				return nil, rows, nil
			}
		}
		return nil, nil, nil
	case strings.Contains(sql, "sys.change_tracking_tables"):
		var rows [][]any
		var names []string
		for ns, t := range f.tables {
			if t.ct {
				names = append(names, ns)
			}
		}
		sort.Strings(names)
		for _, ns := range names {
			rows = append(rows, []any{f.tables[ns].schema, f.tables[ns].name})
		}
		return nil, rows, nil
	case strings.Contains(sql, "FROM sys.columns WHERE"):
		t := f.byFrom(args[0])
		var rows [][]any
		for _, col := range t.cols {
			rows = append(rows, []any{col})
		}
		return nil, rows, nil
	case strings.Contains(sql, "FROM sys.indexes"):
		t := f.byFrom(args[0])
		var rows [][]any
		if strings.Contains(sql, "is_primary_key") {
			for _, k := range t.pk {
				rows = append(rows, []any{k})
			}
		} else {
			rows = append(rows, []any{"id"})
		}
		return nil, rows, nil
	case strings.HasPrefix(sql, "SELECT sys.fn_cdc_get_max_lsn()"):
		var max any
		if f.max > 0 {
			max = lsnBytes(f.max)
		}
		return nil, [][]any{{max, f.now}}, nil
	case strings.HasPrefix(sql, "SELECT CHANGE_TRACKING_CURRENT_VERSION()"):
		return nil, [][]any{{f.ctVersion, f.now}}, nil
	case strings.HasPrefix(sql, "SELECT CHANGE_TRACKING_MIN_VALID_VERSION"):
		return nil, [][]any{{f.minValid}}, nil
	case strings.HasPrefix(sql, "SELECT MIN(start_lsn)"):
		if f.minLSN == 0 {
			return nil, [][]any{{nil}}, nil
		}
		return nil, [][]any{{lsnBytes(f.minLSN)}}, nil
	case strings.Contains(sql, "FROM cdc.lsn_time_mapping WHERE"):
		lo, hi := lsnNum(argOf(args[0])), lsnNum(argOf(args[1]))
		var rows [][]any
		for _, tx := range f.mapping {
			if tx.lsn > lo && tx.lsn <= hi && len(rows) < top {
				rows = append(rows, []any{lsnBytes(tx.lsn), tx.at})
			}
		}
		return nil, rows, nil
	case strings.Contains(sql, "fn_cdc_get_all_changes_"):
		return f.changes(sql, top, args)
	case strings.Contains(sql, "cdc.ddl_history"):
		lo, hi := lsnNum(argOf(args[0])), lsnNum(argOf(args[1]))
		var rows [][]any
		for _, d := range f.ddls {
			if d.lsn > lo && d.lsn <= hi {
				rows = append(rows, []any{lsnBytes(d.lsn), f.now, d.cmd, "dbo", d.table})
			}
		}
		return nil, rows, nil
	case strings.Contains(sql, "CHANGETABLE"):
		return f.ctChanges(sql, top, args)
	case strings.HasPrefix(sql, "SELECT "):
		return f.snapshot(sql, top, args)
	}
	return nil, nil, fmt.Errorf("fake: no answer to %q", sql)
}

func (f *fake) byFrom(a any) *fakeTable {
	s, _ := a.(string)
	m := fromRE.FindStringSubmatch("FROM " + s)
	if m == nil {
		return &fakeTable{}
	}
	t := f.tables[m[1]+"."+m[2]]
	if t == nil {
		return &fakeTable{}
	}
	return t
}

// keyArg is the @k1 of a keyset query, -1 without one.
func keyArg(args []any) int64 {
	for _, a := range args {
		if p, ok := a.(mssqlwire.Param); ok && p.Name == "@k1" {
			n, _ := p.Value.(int64)
			return n
		}
	}
	return -1 << 62
}

func (f *fake) snapshot(sql string, top int, args []any) ([]mssqlwire.Column, [][]any, error) {
	m := fromRE.FindStringSubmatch(sql)
	if m == nil {
		return nil, nil, fmt.Errorf("fake: no table in %q", sql)
	}
	t := f.tables[m[1]+"."+m[2]]
	after := keyArg(args)
	var ids []int64
	for id := range t.rows {
		if id > after {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var rows [][]any
	for _, id := range ids {
		if len(rows) < top {
			rows = append(rows, t.rows[id])
		}
	}
	return cols(t.cols...), rows, nil
}

func (f *fake) changes(sql string, top int, args []any) ([]mssqlwire.Column, [][]any, error) {
	name := instRE.FindStringSubmatch(sql)[1]
	var in *fakeInst
	for _, i := range f.instances {
		if i.name == name {
			in = i
		}
	}
	lo, hi := lsnNum(argOf(args[0])), lsnNum(argOf(args[1]))
	if lo < in.start {
		return nil, nil, errors.New("fake: error 313, the LSN is out of the instance's range")
	}
	var seq uint64
	if len(args) > 2 {
		seq = lsnNum(argOf(args[2]))
	}
	var rows [][]any
	var lastLSN, lastSeq uint64
	for _, ch := range in.changes {
		if ch.lsn < lo || ch.lsn > hi || ch.seq <= seq && len(args) > 2 {
			continue
		}
		if len(rows) >= top && (ch.lsn != lastLSN || ch.seq != lastSeq) {
			break
		}
		rows = append(rows, append([]any{lsnBytes(ch.lsn), lsnBytes(ch.seq), ch.op}, ch.row...))
		lastLSN, lastSeq = ch.lsn, ch.seq
	}
	return cols(append([]string{"__$start_lsn", "__$seqval", "__$operation"}, in.cols...)...), rows, nil
}

func (f *fake) ctChanges(sql string, top int, args []any) ([]mssqlwire.Column, [][]any, error) {
	m := ctRE.FindStringSubmatch(sql)
	t := f.tables[m[1]+"."+m[2]]
	v, cur := args[0].(int64), args[1].(int64)
	after := keyArg(args)
	var ids []int64
	for id, ch := range t.ctChanges {
		if ch.v > v && ch.v <= cur && id > after {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var rows [][]any
	for _, id := range ids {
		if len(rows) >= top {
			break
		}
		r := []any{t.ctChanges[id].op, id}
		if row, ok := t.rows[id]; ok {
			r = append(r, row...)
		} else {
			r = append(r, make([]any, len(t.cols))...)
		}
		rows = append(rows, r)
	}
	return cols(append([]string{"SYS_CHANGE_OPERATION", "id"}, t.cols...)...), rows, nil
}

// ctChange records a change tracking change of t's row id.
func (f *fake) ctChange(t *fakeTable, op string, row []any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ctVersion++
	id := row[0].(int64)
	t.ctChanges[id] = fakeCT{op: op, v: f.ctVersion}
	if op == "D" {
		delete(t.rows, id)
	} else {
		t.rows[id] = row
	}
}

// newSource provisions a source of params on f.
func newSource(t *testing.T, f *fake, params map[string]any) *Source {
	t.Helper()
	s, err := provision(t, params)
	if err != nil {
		t.Fatal(err)
	}
	s.dial = f.dial
	return s
}

func provision(t *testing.T, params map[string]any) (*Source, error) {
	t.Helper()
	p := map[string]any{"username": "sa", "database": "shop", "encrypt": "off"}
	for k, v := range params {
		p[k] = v
	}
	b, _ := json.Marshal(p)
	v, err := config.ParseValue(b, "params")
	if err != nil {
		t.Fatal(err)
	}
	m, err := module.Load(fitsync.Sources, config.ModuleRef{Module: "mssql", Params: v}, nil)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { module.Cleanup(m) })
	return m.(*Source), nil
}

// run runs s to its end, from, as a job when once, and returns its
// records.
func run(t *testing.T, s *Source, from fitsync.Position, once bool) ([]*fitsync.Record, error) {
	t.Helper()
	var recs []*fitsync.Record
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := s.Run(ctx, &fitsync.Run{From: from, Once: once, Emit: func(r *fitsync.Record) error {
		recs = append(recs, r)
		return nil
	}, Start: func(fitsync.Position) error { return nil }})
	return recs, err
}

// recString is a record as op ns key row tx flags, and its position.
func recString(r *fitsync.Record) string {
	key, _ := fitsync.AppendPlain(nil, r.Key)
	row := r.After
	if r.Op == fitsync.OpDelete {
		row = r.Before
	}
	val, _ := fitsync.AppendPlain(nil, row)
	s := r.Op.String() + " " + r.Namespace + " " + string(key) + " " + string(val)
	if r.Op == fitsync.OpUpdate && !r.Before.IsNull() {
		b, _ := fitsync.AppendPlain(nil, r.Before)
		s += " was " + string(b)
	}
	if r.Tx.ID != "" {
		s += " tx"
	}
	if r.Tx.First {
		s += " first"
	}
	if r.Tx.Last {
		s += " last"
	}
	if r.Position != nil {
		var p position
		json.Unmarshal(r.Position, &p)
		switch {
		case p.Snapshot != nil:
			s += fmt.Sprintf(" @snap %s %s", p.Snapshot.Table, p.Snapshot.Key)
		case p.LSN != "":
			s += fmt.Sprintf(" @%d", lsnNum(p.LSN.bytes()))
		default:
			s += fmt.Sprintf(" @v%d", p.Version)
		}
	}
	return s
}

func checkRecs(t *testing.T, got []*fitsync.Record, want ...string) {
	t.Helper()
	var g []string
	for _, r := range got {
		g = append(g, recString(r))
	}
	if strings.Join(g, "\n") != strings.Join(want, "\n") {
		t.Fatalf("records:\n%s\nwant:\n%s", strings.Join(g, "\n"), strings.Join(want, "\n"))
	}
}
