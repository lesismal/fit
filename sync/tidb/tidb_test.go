package synctidb

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	synckafka "github.com/lesismal/fit/sync/kafka"
	mysqlwire "github.com/lesismal/fit/sync/mysql/wire"
)

// fakeDB is TiDB as the snapshot sees it: one table of the columns below.
type fakeDB struct {
	rows    [][]any // id first, ascending
	ts      string
	noKey   bool
	fail    map[string]error // by a query's prefix
	queries []string
	closed  int
}

var fakeColumns = [][]any{
	{[]byte("id"), []byte("bigint(20)")}, {[]byte("e"), []byte("enum('a','b','c')")}, {[]byte("s"), []byte("set('x','y')")},
	{[]byte("bt"), []byte("bit(10)")}, {[]byte("d"), []byte("date")}, {[]byte("at"), []byte("datetime(6)")},
	{[]byte("tm"), []byte("time")}, {[]byte("f"), []byte("float")}, {[]byte("dbl"), []byte("double")},
	{[]byte("n"), []byte("decimal(5,2)")}, {[]byte("b"), []byte("varbinary(4)")}, {[]byte("u"), []byte("bigint unsigned")},
	{[]byte("o"), []byte("other")},
}

func (f *fakeDB) failing(q string) error {
	for p, err := range f.fail {
		if strings.HasPrefix(q, p) {
			return err
		}
	}
	return nil
}

func (f *fakeDB) Exec(ctx context.Context, q string, args ...any) (mysqlwire.Result, error) {
	f.queries = append(f.queries, q)
	return mysqlwire.Result{}, f.failing(q)
}

func (f *fakeDB) Query(ctx context.Context, q string, args ...any) (rows, error) {
	f.queries = append(f.queries, q)
	if err := f.failing(q); err != nil {
		return nil, err
	}
	switch {
	case strings.HasPrefix(q, "SELECT COLUMN_NAME, COLUMN_TYPE"):
		if args[1] == "none" {
			return &fakeRows{}, nil
		}
		return &fakeRows{rows: fakeColumns}, nil
	case strings.HasPrefix(q, "SELECT COLUMN_NAME FROM"):
		if f.noKey {
			return &fakeRows{}, nil
		}
		return &fakeRows{rows: [][]any{{"id"}}}, nil
	case strings.HasPrefix(q, "SELECT @@tidb_current_ts"):
		if f.ts == "two" {
			return &fakeRows{rows: [][]any{{"1"}, {"2"}}}, nil
		}
		return &fakeRows{rows: [][]any{{[]byte(f.ts)}}}, nil
	}
	limit, _ := strconv.Atoi(q[strings.LastIndex(q, " ")+1:])
	var out [][]any
	for _, r := range f.rows {
		if len(args) > 0 && r[0].(int64) <= args[0].(int64) {
			continue
		}
		if len(out) < limit {
			out = append(out, r)
		}
	}
	return &fakeRows{rows: out}, nil
}

func (f *fakeDB) Close() error {
	f.closed++
	return nil
}

type fakeRows struct {
	rows [][]any
	i    int
	err  error
}

func (r *fakeRows) Next() bool {
	if r.i >= len(r.rows) {
		return false
	}
	r.i++
	return true
}
func (r *fakeRows) Values() []any { return r.rows[r.i-1] }
func (r *fakeRows) Err() error    { return r.err }
func (r *fakeRows) Close() error  { return nil }

// fakeStream is the changes: records handed to Emit, then the end.
type fakeStream struct {
	recs  []*fitsync.Record
	from  fitsync.Position
	err   error
	tests error
}

func (s *fakeStream) Run(ctx context.Context, r *fitsync.Run) error {
	s.from = r.From
	for _, rec := range s.recs {
		c := *rec
		if err := r.Emit(&c); err != nil {
			return err
		}
	}
	return s.err
}

func (s *fakeStream) Test(context.Context) error { return s.tests }

func row(id int64) []any {
	return []any{id, []byte("b"), []byte("x,y"), []byte{0x02, 0xaa}, mysqlwire.DateTime{Year: 2024, Month: 2, Day: 3},
		mysqlwire.DateTime{Year: 2024, Month: 2, Day: 3, Hour: 4, Minute: 5, Second: 6, Microsecond: 7},
		-(12*time.Hour + 34*time.Minute + 56*time.Second + 5*time.Microsecond), float32(1.1), 2.5, []byte("1.25"), []byte{0, 1},
		uint64(9), true}
}

func load(t *testing.T, params map[string]any) (*Source, error) {
	t.Helper()
	if params["kafka"] == nil {
		params["kafka"] = map[string]any{"brokers": []string{"b:9092"}, "topics": []string{"cdc"}}
	}
	b, _ := json.Marshal(params)
	v, err := config.ParseValue(b, "params")
	if err != nil {
		t.Fatal(err)
	}
	m, err := module.Load(fitsync.Sources, config.ModuleRef{Module: "tidb", Params: v}, nil)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { module.Cleanup(m) })
	return m.(*Source), nil
}

func newSource(t *testing.T, params map[string]any, fdb *fakeDB, st *fakeStream) *Source {
	t.Helper()
	s, err := load(t, params)
	if err != nil {
		t.Fatal(err)
	}
	s.dial = func(context.Context, mysqlwire.Config) (db, error) { return fdb, nil }
	s.stream = st
	return s
}

func run(t *testing.T, s *Source, from fitsync.Position, stop int) ([]*fitsync.Record, error) {
	t.Helper()
	var out []*fitsync.Record
	stopErr := errors.New("stop")
	err := s.Run(context.Background(), &fitsync.Run{From: from, Once: true, Emit: func(r *fitsync.Record) error {
		out = append(out, r)
		if stop > 0 && len(out) == stop {
			return stopErr
		}
		return nil
	}})
	if errors.Is(err, stopErr) {
		err = nil
	}
	return out, err
}

func js(t *testing.T, v fitsync.Value) string {
	t.Helper()
	b, err := v.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func change(op fitsync.Op, ns, tx string, after fitsync.Value, pos string) *fitsync.Record {
	r := &fitsync.Record{Op: op, Namespace: ns, After: after, Tx: fitsync.Tx{ID: tx}}
	if pos != "" {
		r.Position = fitsync.Position(pos)
	}
	return r
}

func TestSnapshotThenChanges(t *testing.T) {
	db := &fakeDB{ts: "1000"}
	for i := range int64(5) {
		db.rows = append(db.rows, row(i+1))
	}
	enumRow := fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(9)}, fitsync.Field{Name: "e", Value: fitsync.String("3")},
		fitsync.Field{Name: "s", Value: fitsync.String("2")}, fitsync.Field{Name: "x", Value: fitsync.String("z")},
		fitsync.Field{Name: "e2", Value: fitsync.Int(1)})
	st := &fakeStream{recs: []*fitsync.Record{
		change(fitsync.OpInsert, "d.t", "999", enumRow, `{"offsets":{}}`),      // before the snapshot
		change(fitsync.OpInsert, "d.other", "2000", enumRow, `{"offsets":{}}`), // not kept
		change(fitsync.OpUpdate, "d.t", "2000", enumRow, `{"offsets":{"cdc":{"0":3}}}`),
		change(fitsync.OpUpdate, "d.t", "x", fitsync.String("not a row"), ""),
	}}
	s := newSource(t, map[string]any{"tables": []string{"d.t"}, "page_rows": 2, "time_zone": "UTC"}, db, st)
	recs, err := run(t, s, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 7 {
		t.Fatalf("%d records", len(recs))
	}
	first := recs[0]
	want := `{"map":{"id":{"int":1},"e":"b","s":"x,y","bt":{"uint":682},"d":{"date":"2024-02-03"},"at":{"time":"2024-02-03T04:05:06.000007Z"},"tm":"-12:34:56.000005","f":{"float":1.1},"dbl":{"float":2.5},"n":{"decimal":"1.25"},"b":{"bytes":"AAE="},"u":{"uint":9},"o":"true"}}`
	if got := js(t, first.After); got != want {
		t.Fatalf("\n%s\nwant\n%s", got, want)
	}
	if first.Source != "tidb" || first.Op != fitsync.OpInsert || js(t, first.Key) != `{"map":{"id":{"int":1}}}` || !first.CommitTime.Equal(time.UnixMilli(0)) {
		t.Fatalf("%+v", first)
	}
	var p position
	json.Unmarshal(recs[4].Position, &p)
	if p.TS != 1000 || p.Snapshot == nil || string(*p.Snapshot.After) != `{"map":{"id":{"int":5}}}` {
		t.Fatalf("%s", recs[4].Position)
	}
	ch := recs[5]
	if ch.Source != "tidb" || js(t, ch.After) != `{"map":{"id":{"int":9}},"e":"c","s":"y","x":"z","e2":{"int":1}}}` && js(t, ch.After) != `{"map":{"id":{"int":9},"e":"c","s":"y","x":"z","e2":{"int":1}}}` {
		t.Fatalf("%s", js(t, ch.After))
	}
	if string(ch.Position) != `{"ts":1000,"kafka":{"offsets":{"cdc":{"0":3}}}}` {
		t.Fatalf("%s", ch.Position)
	}
	if !strings.Contains(strings.Join(db.queries, ";"), "SET @@tidb_snapshot = '1000'") {
		t.Fatalf("%v", db.queries)
	}

	// Resumed in the snapshot: from after its key; resumed in the changes:
	// the kafka source from its position.
	recs, err = run(t, s, fitsync.Position(`{"ts":1000,"snapshot":{"table":0,"after":{"map":{"id":{"int":3}}}}}`), 0)
	if err != nil || js(t, recs[0].Key) != `{"map":{"id":{"int":4}}}` {
		t.Fatalf("%v %s", err, js(t, recs[0].Key))
	}
	recs, err = run(t, s, fitsync.Position(`{"ts":1000,"kafka":{"offsets":{"cdc":{"0":3}}}}`), 0)
	if err != nil || len(recs) != 2 || string(st.from) != `{"offsets":{"cdc":{"0":3}}}` {
		t.Fatalf("%v %d %s", err, len(recs), st.from)
	}
	// A stop in the snapshot.
	if recs, err := run(t, s, nil, 2); err != nil || len(recs) != 2 {
		t.Fatalf("%v %d", err, len(recs))
	}
}

func TestSimpleAndNever(t *testing.T) {
	db := &fakeDB{ts: "5"}
	st := &fakeStream{}
	s := newSource(t, map[string]any{"tables": []string{"d.t"}, "protocol": "simple", "snapshot": "never"}, db, st)
	if _, err := run(t, s, nil, 0); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(db.queries, ";"), "tidb_snapshot") {
		t.Fatal("a snapshot read")
	}
	// The simple codec has the table: a row decodes with its key.
	var got []*fitsync.Record
	s.simple.Decode([]byte(`{"type":"INSERT","database":"d","table":"t","commitTs":7,"data":{"id":"4","e":"1"}}`), func(r *fitsync.Record) { got = append(got, r) })
	if js(t, got[0].Key) != `{"map":{"id":{"int":4}}}` || js(t, got[0].After) != `{"map":{"id":{"int":4},"e":"a"}}` {
		t.Fatalf("%s %s", js(t, got[0].Key), js(t, got[0].After))
	}
	// No tables: no connection; every table's changes.
	db2 := &fakeDB{}
	st2 := &fakeStream{recs: []*fitsync.Record{change(fitsync.OpInsert, "x.y", "1", fitsync.Map(), "")}}
	s2 := newSource(t, map[string]any{"snapshot": "never"}, db2, st2)
	recs, err := run(t, s2, nil, 0)
	if err != nil || len(recs) != 1 || len(db2.queries) != 0 {
		t.Fatalf("%v %d %v", err, len(recs), db2.queries)
	}
	if _, err := run(t, s2, fitsync.Position(`[`), 0); err == nil {
		t.Fatal("a bad checkpoint read")
	}
}

func TestErrors(t *testing.T) {
	for _, p := range []map[string]any{
		{"tables": []string{"nodot"}},
		{"snapshot": "initial"},
		{"tables": []string{"d.t"}, "time_zone": "No/Zone"},
		{"tables": []string{"d.t"}, "kafka": map[string]any{"brokers": []string{"b:1"}, "topics": []string{"c"}, "codec": map[string]any{"module": "raw"}}},
		{"tables": []string{"d.t"}, "kafka": map[string]any{"brokers": []string{"b:1"}}},
		{"tables": []string{"d.t"}, "address": "nohost"},
	} {
		if _, err := load(t, p); err == nil {
			t.Errorf("%v loaded", p)
		}
	}
	s, err := load(t, map[string]any{"tables": []string{"d.t"}, "address": "127.0.0.1:1", "timeout": "1s"})
	if err != nil {
		t.Fatal(err)
	}
	if s.cfg.User != "root" || s.cfg.Addr != "127.0.0.1:1" {
		t.Fatalf("%+v", s.cfg)
	}
	// The real dial fails, and the test with it.
	if _, err := run(t, s, nil, 0); err == nil {
		t.Fatal("ran without TiDB")
	}
	if err := s.Test(context.Background()); err == nil {
		t.Fatal("tested without TiDB")
	}

	cases := []struct {
		db   *fakeDB
		want string
	}{
		{&fakeDB{fail: map[string]error{"SET time_zone": errors.New("tz")}}, "tz"},
		{&fakeDB{fail: map[string]error{"BEGIN": errors.New("begin")}}, "begin"},
		{&fakeDB{fail: map[string]error{"SELECT @@": errors.New("cur")}}, "cur"},
		{&fakeDB{ts: "two"}, "2 rows"},
		{&fakeDB{ts: "0"}, "is the server TiDB"},
		{&fakeDB{ts: "1", fail: map[string]error{"SET @@tidb_snapshot": errors.New("gc")}}, "GC safe point"},
		{&fakeDB{ts: "1", fail: map[string]error{"SELECT COLUMN_NAME, COLUMN_TYPE": errors.New("cols")}}, "cols"},
		{&fakeDB{ts: "1", fail: map[string]error{"SELECT COLUMN_NAME FROM": errors.New("keys")}}, "keys"},
		{&fakeDB{ts: "1", noKey: true}, "no primary key"},
		{&fakeDB{ts: "1", fail: map[string]error{"SELECT `id`": errors.New("page")}}, "page"},
	}
	for _, c := range cases {
		s := newSource(t, map[string]any{"tables": []string{"d.t"}}, c.db, &fakeStream{})
		if _, err := run(t, s, nil, 0); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v, want %s", err, c.want)
		}
	}
	s = newSource(t, map[string]any{"tables": []string{"d.none"}}, &fakeDB{ts: "1"}, &fakeStream{})
	if _, err := run(t, s, nil, 0); err == nil || !strings.Contains(err.Error(), "no table") {
		t.Fatalf("%v", err)
	}
	s = newSource(t, map[string]any{"tables": []string{"d.t"}}, &fakeDB{ts: "1"}, &fakeStream{})
	if _, err := run(t, s, fitsync.Position(`{"ts":1,"snapshot":{"table":0,"after":[}}`), 0); err == nil {
		t.Fatal("a bad key read")
	}
	if _, err := run(t, s, fitsync.Position(`{"ts":1,"snapshot":{"table":0,"after":{"x":1}}}`), 0); err == nil {
		t.Fatal("a bad key read")
	}
	s.dial = func(context.Context, mysqlwire.Config) (db, error) { return nil, errors.New("dial") }
	if _, err := run(t, s, nil, 0); err == nil {
		t.Fatal("ran without a connection")
	}
	// Test: TiDB, then Kafka.
	db := &fakeDB{}
	st := &fakeStream{tests: errors.New("kafka")}
	s = newSource(t, map[string]any{"tables": []string{"d.t"}}, db, st)
	if err := s.Test(context.Background()); err == nil || err.Error() != "kafka" || db.closed != 1 {
		t.Fatalf("%v", err)
	}
	db.fail = map[string]error{"SELECT 1": errors.New("select")}
	if err := s.Test(context.Background()); err == nil || err.Error() != "select" {
		t.Fatalf("%v", err)
	}
	// The real Kafka source is the stream by default.
	s, _ = load(t, map[string]any{"tables": []string{"d.t"}})
	if s.stream != &s.Kafka {
		t.Fatal("the stream is not the kafka source")
	}
	var _ synckafka.KeyDecoder = (*synckafka.Debezium)(nil)
}

func TestHelpers(t *testing.T) {
	if e := elements("enum('a','it''s','c')"); strings.Join(e, "|") != "a|it's|c" {
		t.Fatalf("%q", e)
	}
	if elements("int") != nil {
		t.Fatal("elements of an int")
	}
	col := column{typ: synckafka.ParseMySQLType("int")}
	if js(t, value(col, nil)) != "null" || js(t, value(col, struct{}{})) != `"{}"` {
		t.Fatal("value")
	}
	if str("s") != "s" || str(nil) != "" || str(5) != "5" {
		t.Fatal("str")
	}
	if clock(90*time.Minute) != "01:30:00" {
		t.Fatal(clock(90 * time.Minute))
	}
	for _, v := range []fitsync.Value{fitsync.Uint(1), fitsync.Float(1), fitsync.Bytes([]byte("b")), fitsync.Date(2024, 1, 1), fitsync.Null(), fitsync.String("s")} {
		arg(v)
	}
	if quote("a`b") != "`a``b`" {
		t.Fatal(quote("a`b"))
	}
	if _, err := queryAll(context.Background(), &errRows{}, "q"); err == nil {
		t.Fatal("no error from rows")
	}
	s := &Source{}
	s.labels(&fitsync.Record{Namespace: "none"})
}

type errRows struct{ fakeDB }

func (*errRows) Query(context.Context, string, ...any) (rows, error) {
	return &fakeRows{err: errors.New("rows")}, nil
}
