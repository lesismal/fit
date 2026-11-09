package syncdoris

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	fitsync "github.com/lesismal/fit/sync"
	mysqlwire "github.com/lesismal/fit/sync/mysql/wire"
)

// fakeTable is a table a fake connection answers the source's queries of:
// rows of id (BIGINT), v (VARCHAR), at (DATETIME) and del (TINYINT),
// ordered by at then id; it reads the bounds of a page's WHERE back from
// the query.
type fakeTable struct {
	mu      sync.Mutex
	rows    [][]any
	queries []string
	fail    func(q string) error
	dials   int
	dialErr error
}

var fakeCols = []mysqlwire.Column{
	{Name: "id", Type: mysqlwire.TypeLongLong},
	{Name: "v", Type: mysqlwire.TypeVarString, Collation: 45},
	{Name: "at", Type: mysqlwire.TypeDateTime},
	{Name: "del", Type: mysqlwire.TypeTiny},
}

func (f *fakeTable) add(id int64, v string, at int, del bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := int64(0)
	if del {
		d = 1
	}
	f.rows = append(f.rows, []any{id, []byte(v), mysqlwire.FromTime(time.Unix(int64(at), 0).UTC()), d})
}

type fakeConn struct{ f *fakeTable }

func (f *fakeTable) dial(context.Context, mysqlwire.Config) (conn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dials++
	if f.dialErr != nil {
		return nil, f.dialErr
	}
	return fakeConn{f}, nil
}

func (c fakeConn) Close() error { return nil }

var (
	pageRe  = regexp.MustCompile(`LIMIT (\d+)$`)
	afterRe = regexp.MustCompile(`\(\x60at\x60 > '([^']+)'\)(?: OR \(\x60at\x60 = '[^']+' AND \x60id\x60 > (-?\d+)\))?`)
	idRe    = regexp.MustCompile(`WHERE \(\x60id\x60 > (-?\d+)\)`)
)

func (c fakeConn) QueryAll(_ context.Context, q string) ([]mysqlwire.Column, [][]any, error) {
	f := c.f
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, q)
	if f.fail != nil {
		if err := f.fail(q); err != nil {
			return nil, nil, err
		}
	}
	rows := append([][]any(nil), f.rows...)
	at := func(r []any) time.Time { return r[2].(mysqlwire.DateTime).Time(time.UTC) }
	sort.Slice(rows, func(i, j int) bool {
		if !at(rows[i]).Equal(at(rows[j])) {
			return at(rows[i]).Before(at(rows[j]))
		}
		return rows[i][0].(int64) < rows[j][0].(int64)
	})
	switch {
	case strings.HasPrefix(q, "SELECT MAX("):
		if len(rows) == 0 {
			return fakeCols[2:3], [][]any{{nil}}, nil
		}
		return fakeCols[2:3], [][]any{{rows[len(rows)-1][2]}}, nil
	case strings.HasPrefix(q, "SELECT 1"), strings.HasSuffix(q, "LIMIT 0"):
		return nil, nil, nil
	}
	limit := len(rows)
	if m := pageRe.FindStringSubmatch(q); m != nil {
		limit, _ = strconv.Atoi(m[1])
	}
	var out [][]any
	for _, r := range rows {
		if m := afterRe.FindStringSubmatch(q); m != nil {
			b, _ := time.Parse(dateTimeLayout, m[1])
			if at(r).Before(b) {
				continue
			}
			if at(r).Equal(b) {
				if m[2] == "" {
					continue
				}
				if id, _ := strconv.ParseInt(m[2], 10, 64); r[0].(int64) <= id {
					continue
				}
			}
		} else if m := idRe.FindStringSubmatch(q); m != nil {
			if id, _ := strconv.ParseInt(m[1], 10, 64); r[0].(int64) <= id {
				continue
			}
		}
		out = append(out, r)
		if len(out) == limit {
			break
		}
	}
	return fakeCols, out, nil
}

func fakeSource(t *testing.T, f *fakeTable, params map[string]any) *Source {
	t.Helper()
	p := map[string]any{"username": "root", "table": "shop.orders", "key": []string{"id"}}
	for k, v := range params {
		p[k] = v
	}
	m, err := loadMod(t, fitsync.Sources, p)
	if err != nil {
		t.Fatal(err)
	}
	s := m.(*Source)
	s.dial = f.dial
	return s
}

// collect runs src until it has emitted n records, or ends.
func collect(t *testing.T, src fitsync.Source, from fitsync.Position, once bool, n int) ([]*fitsync.Record, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var got []*fitsync.Record
	stop := errors.New("enough")
	err := src.Run(ctx, &fitsync.Run{From: from, Once: once, Emit: func(r *fitsync.Record) error {
		got = append(got, r)
		if len(got) == n {
			return stop
		}
		return nil
	}})
	if errors.Is(err, stop) {
		err = nil
	}
	return got, err
}

func idsOf(recs []*fitsync.Record) string {
	var out []string
	for _, r := range recs {
		id, _ := r.Key.Get("id")
		out = append(out, fmt.Sprintf("%s:%d", r.Op, id.AsInt()))
	}
	return strings.Join(out, ",")
}

// TestSourceResumes reads by the cursor in pages, stops, resumes from a
// record's position with no gap or repeat, and polls up rows written
// since, a delete among them.
func TestSourceResumes(t *testing.T) {
	f := &fakeTable{}
	for i := range int64(20) {
		f.add(i, "v", int(i/4), false)
	}
	s := fakeSource(t, f, map[string]any{"cursor": "at", "deleted_column": "del", "page_rows": 3, "poll_interval": "10ms", "namespace": "orders"})
	first, err := collect(t, s, nil, false, 9)
	if err != nil || len(first) != 9 {
		t.Fatal(err, len(first))
	}
	if first[0].Namespace != "orders" || first[0].Source != "doris" || first[0].CommitTime.Unix() != 0 {
		t.Fatalf("%+v", first[0])
	}
	rest, err := collect(t, s, first[8].Position, true, 0)
	if err != nil || len(rest) != 11 {
		t.Fatal(err, len(rest))
	}
	all := append(first, rest...)
	for i, r := range all {
		if id, _ := r.Key.Get("id"); id.AsInt() != int64(i) {
			t.Fatalf("record %d is id %d", i, id.AsInt())
		}
	}
	go func() {
		time.Sleep(30 * time.Millisecond)
		f.add(3, "gone", 10, true)
		f.add(100, "new", 10, false)
	}()
	more, err := collect(t, s, all[19].Position, false, 2)
	if err != nil || idsOf(more) != "delete:3,insert:100" {
		t.Fatal(err, idsOf(more))
	}
	if v, _ := more[0].Before.Get("v"); v.Text() != "gone" || !more[0].After.IsNull() {
		t.Fatalf("%+v", more[0])
	}
	// Ended by its context while it polls.
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	if err := s.Run(ctx, &fitsync.Run{From: more[1].Position, Emit: func(*fitsync.Record) error { return nil }}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// TestSourceKeyOnly reads by the key alone, once, and ends.
func TestSourceKeyOnly(t *testing.T) {
	f := &fakeTable{}
	for i := range int64(5) {
		f.add(i, "v", 0, false)
	}
	s := fakeSource(t, f, map[string]any{"table": "", "query": "SELECT * FROM shop.orders WHERE v = 'v';", "page_rows": 2})
	got, err := collect(t, s, nil, false, 0)
	if err != nil || idsOf(got) != "insert:0,insert:1,insert:2,insert:3,insert:4" || got[0].Namespace != "doris" {
		t.Fatal(err, idsOf(got))
	}
	if q := f.queries[0]; q != "SELECT * FROM (SELECT * FROM shop.orders WHERE v = 'v') AS `__fit` ORDER BY `id` LIMIT 2" {
		t.Fatal(q)
	}
	if q := f.queries[1]; !strings.Contains(q, "WHERE (`id` > 1)") {
		t.Fatal(q)
	}
}

// TestSourceSnapshotNever starts past the greatest cursor value.
func TestSourceSnapshotNever(t *testing.T) {
	f := &fakeTable{}
	f.add(1, "old", 5, false)
	s := fakeSource(t, f, map[string]any{"cursor": "at", "snapshot": "never", "poll_interval": "5ms"})
	go func() {
		time.Sleep(20 * time.Millisecond)
		f.add(2, "new", 6, false)
	}()
	got, err := collect(t, s, nil, false, 1)
	if err != nil || idsOf(got) != "insert:2" {
		t.Fatal(err, idsOf(got))
	}
	empty := &fakeTable{}
	s = fakeSource(t, empty, map[string]any{"cursor": "at", "snapshot": "never"})
	if got, err := collect(t, s, nil, true, 0); err != nil || len(got) != 0 {
		t.Fatal(err, got)
	}
}

func TestSourceErrors(t *testing.T) {
	f := &fakeTable{}
	f.add(1, "v", 1, false)
	s := fakeSource(t, f, map[string]any{"cursor": "at"})
	for _, p := range []string{`{`, `{"after":["1","2","3"]}`, `{"after":["1; DROP TABLE x"]}`} {
		if _, err := collect(t, s, fitsync.Position(p), true, 0); err == nil {
			t.Errorf("%s: no error", p)
		}
	}
	f.fail = func(q string) error {
		if strings.HasPrefix(q, "SELECT MAX") {
			return errors.New("max failed")
		}
		return nil
	}
	never := fakeSource(t, f, map[string]any{"cursor": "at", "snapshot": "never"})
	if _, err := collect(t, never, nil, true, 0); err == nil || !strings.Contains(err.Error(), "max failed") {
		t.Fatal(err)
	}
	f.fail = func(q string) error { return errors.New("query failed") }
	if _, err := collect(t, s, nil, true, 0); err == nil || !strings.Contains(err.Error(), "query failed") {
		t.Fatal(err)
	}
	if err := s.Test(context.Background()); err == nil {
		t.Fatal("a failing query passed the test")
	}
	f.fail = func(q string) error {
		if strings.HasSuffix(q, "LIMIT 0") {
			return errors.New("no table")
		}
		return nil
	}
	if err := s.Test(context.Background()); err == nil || err.Error() != "no table" {
		t.Fatal(err)
	}
	f.fail = nil
	if err := s.Test(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.dialErr = errors.New("refused")
	if _, err := collect(t, s, nil, true, 0); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatal(err)
	}
	if err := s.Test(context.Background()); err == nil {
		t.Fatal("a refused dial passed the test")
	}
	f.dialErr = nil
	// An emit's error ends the run.
	stop := errors.New("stop")
	if err := s.Run(context.Background(), &fitsync.Run{Once: true, Emit: func(*fitsync.Record) error { return stop }}); !errors.Is(err, stop) {
		t.Fatal(err)
	}
	// A row without the key's column, or with a null one.
	bad := fakeSource(t, f, map[string]any{"key": []string{"nothing"}})
	if _, err := collect(t, bad, nil, true, 0); err == nil || !strings.Contains(err.Error(), "no column nothing") {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.rows = [][]any{{nil, []byte("v"), mysqlwire.FromTime(time.Unix(1, 0)), int64(0)}}
	f.mu.Unlock()
	if _, err := collect(t, s, nil, true, 0); err == nil || !strings.Contains(err.Error(), "null") {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.rows = [][]any{{int64(1), []byte("v"), mysqlwire.FromTime(time.Unix(1, 0)), int64(0), int64(9)}}
	f.mu.Unlock()
	if _, err := collect(t, s, nil, true, 0); err == nil || !strings.Contains(err.Error(), "values for") {
		t.Fatal(err)
	}
}

func TestSourceProvisionErrors(t *testing.T) {
	for _, tc := range []struct {
		p    map[string]any
		want string
	}{
		{map[string]any{"username": "u", "key": []string{"id"}}, "either table or query"},
		{map[string]any{"username": "u", "table": "t", "query": "q", "key": []string{"id"}}, "either table or query"},
		{map[string]any{"username": "u", "table": "t", "key": []string{}}, "no key columns"},
		{map[string]any{"username": "u", "table": "t", "key": []string{"at"}, "cursor": "at"}, "the cursor"},
		{map[string]any{"username": "u", "table": "t", "key": []string{"id"}, "snapshot": "never"}, "needs a cursor"},
		{map[string]any{"username": "u", "table": "t", "key": []string{"id"}, "address": "nohost"}, "host and port"},
	} {
		if _, err := loadMod(t, fitsync.Sources, tc.p); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: %v, want %q", tc.p, err, tc.want)
		}
	}
	m, err := loadMod(t, fitsync.Sources, map[string]any{"username": "u", "table": "t", "key": []string{"id"}})
	if err != nil {
		t.Fatal(err)
	}
	s := m.(*Source)
	if s.cfg.Addr != DefaultAddress || s.pageRows() != DefaultPageRows || s.pollInterval() != DefaultPollInterval {
		t.Fatal(s.cfg.Addr, s.pageRows(), s.pollInterval())
	}
}

// TestWireConn goes through the real client against a small server.
func TestWireConn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	c, err := dialWire(ctx, mysqlwire.Config{Addr: miniServer(t), User: "u"})
	if err != nil {
		t.Fatal(err)
	}
	cols, rows, err := c.QueryAll(ctx, "SELECT 'row'")
	if err != nil || len(cols) != 1 || len(rows) != 2 || string(rows[0][0].([]byte)) != "row" {
		t.Fatal(cols, rows, err)
	}
	_ = c.Close()
	if _, _, err := c.QueryAll(ctx, "SELECT 'row'"); err == nil {
		t.Fatal("a closed connection answered")
	}
	if _, err := dialWire(ctx, mysqlwire.Config{Addr: "127.0.0.1:1", User: "u"}); err == nil {
		t.Fatal("nothing listening answered")
	}
}

func TestValues(t *testing.T) {
	for _, tc := range []struct {
		v    fitsync.Value
		want string
	}{
		{fitsync.Bool(true), "TRUE"},
		{fitsync.Bool(false), "FALSE"},
		{fitsync.Int(-3), "-3"},
		{fitsync.Uint(3), "3"},
		{fitsync.Decimal("1.50"), "1.50"},
		{fitsync.Float(2.5), "2.5"},
		{fitsync.Time(time.Date(2026, 1, 2, 3, 4, 5, 600000, time.FixedZone("x", 3600))), "'2026-01-02 02:04:05.0006'"},
		{fitsync.Date(2026, 1, 2), "'2026-01-02'"},
		{fitsync.String("it's \\ \x00\n\r"), `'it\'s \\ \0\n\r'`},
		{fitsync.Bytes([]byte("b")), "'b'"},
	} {
		got, err := literal(tc.v)
		if err != nil || got != tc.want {
			t.Errorf("%v: %q %v, want %q", tc.v, got, err, tc.want)
		}
		if !checkLiteral(got) {
			t.Errorf("%q does not check", got)
		}
	}
	for _, v := range []fitsync.Value{fitsync.Null(), fitsync.Float(math.NaN()), fitsync.Array()} {
		if _, err := literal(v); err == nil {
			t.Errorf("%v: no error", v)
		}
	}
	for _, s := range []string{"", "'a", "'a'b'", `'a\'`, "1;", "x"} {
		if checkLiteral(s) {
			t.Errorf("%q checks", s)
		}
	}
	if QuoteName("a`b.c") != "`a``b`.`c`" || QuoteName("t") != "`t`" {
		t.Fatal(QuoteName("a`b.c"))
	}
	for _, tc := range []struct {
		v    fitsync.Value
		want bool
	}{{fitsync.Bool(true), true}, {fitsync.Int(0), false}, {fitsync.Uint(2), true}, {fitsync.String("1"), true}, {fitsync.String("true"), true},
		{fitsync.String("0"), false}, {fitsync.Decimal("0.0"), false}, {fitsync.Null(), false}} {
		if truthy(tc.v) != tc.want {
			t.Errorf("truthy(%v)", tc.v)
		}
	}
	if b, err := appendJSON(nil, fitsync.Float(1), time.UTC); err != nil || string(b) != "1" {
		t.Fatal(string(b), err)
	}
	if b, err := appendJSON(nil, fitsync.Date(2026, 1, 2), time.UTC); err != nil || string(b) != `"2026-01-02"` {
		t.Fatal(string(b), err)
	}
}
