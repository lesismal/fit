package syncmysql

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	fitsync "github.com/lesismal/fit/sync"
	"github.com/lesismal/fit/sync/mysql/binlog"
	mysqlwire "github.com/lesismal/fit/sync/mysql/wire"
)

// snapFake is a server with tables shop.t (id, v) of ids, and shop.nokey
// without a primary key, for snapshots.
type snapFake struct {
	*srcFake
	ids []int64
	// fail, when set, fails the queries it matches.
	fail func(q string) error
}

var limitRE = regexp.MustCompile(`LIMIT (\d+)(?: OFFSET (\d+))?$`)

func newSnapFake(ids ...int64) *snapFake {
	f := &snapFake{srcFake: newSrcFake(), ids: ids}
	intCol := mysqlwire.Column{Type: mysqlwire.TypeLongLong}
	strCol := mysqlwire.Column{Type: mysqlwire.TypeVarString, Collation: 45}
	f.queryCols = func(q string) []mysqlwire.Column {
		if strings.HasPrefix(q, "SELECT `id`, `v` FROM") {
			return []mysqlwire.Column{intCol, strCol}
		}
		return []mysqlwire.Column{intCol}
	}
	f.query = func(q string, args []any) ([][]any, error, bool) {
		if f.fail != nil {
			if err := f.fail(q); err != nil {
				return nil, err, true
			}
		}
		switch {
		case strings.Contains(q, "information_schema.TABLES WHERE TABLE_TYPE"):
			return [][]any{{[]byte("shop"), []byte("t")}, {[]byte("other"), []byte("x")}, {[]byte("mysql"), []byte("user")}}, nil, true
		case strings.Contains(q, "TABLE_ROWS"):
			return [][]any{{uint64(len(f.ids))}}, nil, true
		case strings.Contains(q, "information_schema.COLUMNS"):
			if args[1] == "nokey" || args[1] == "x" {
				return [][]any{{[]byte("a"), []byte("int"), []byte("int(11)"), nil}}, nil, true
			}
			return [][]any{{[]byte("id"), []byte("bigint"), []byte("bigint(20)"), nil}, {[]byte("v"), []byte("varchar"), []byte("varchar(9)"), []byte("utf8mb4_bin")}}, nil, true
		case strings.Contains(q, "information_schema.STATISTICS"):
			if args[1] == "t" {
				return [][]any{{[]byte("id")}}, nil, true
			}
			return nil, nil, true
		case strings.HasPrefix(q, "SELECT `id`"):
			return f.rows(q, args), nil, true
		}
		return nil, nil, false
	}
	return f
}

// rows answers a page's or a bound's query of shop.t.
func (f *snapFake) rows(q string, args []any) [][]any {
	gt, ge, lt := int64(-1<<62), int64(-1<<62), int64(1<<62)
	for _, c := range []struct {
		op string
		to *int64
	}{{"(`id`) > (?)", &gt}, {"(`id`) >= (?)", &ge}, {"(`id`) < (?)", &lt}} {
		if strings.Contains(q, c.op) {
			*c.to = args[0].(int64)
			args = args[1:]
		}
	}
	m := limitRE.FindStringSubmatch(q)
	limit, _ := strconv.Atoi(m[1])
	offset, _ := strconv.Atoi(m[2])
	var out [][]any
	for _, id := range f.ids {
		if id <= gt || id < ge || id >= lt {
			continue
		}
		if offset > 0 {
			offset--
			continue
		}
		if len(out) == limit {
			break
		}
		if strings.HasPrefix(q, "SELECT `id`, `v`") {
			out = append(out, []any{id, []byte("v" + strconv.FormatInt(id, 10))})
		} else {
			out = append(out, []any{id})
		}
	}
	return out
}

func ids(recs []*fitsync.Record) string {
	var out []string
	for _, r := range recs {
		v, _ := r.Key.Get("id")
		out = append(out, strconv.FormatInt(v.AsInt(), 10))
	}
	return strings.Join(out, ",")
}

// TestSnapshotThenStream reads the tables by key, a page at a time, then the
// binlog from the position read before them.
func TestSnapshotThenStream(t *testing.T) {
	f := newSnapFake(1, 2, 3, 4, 5)
	f.streams = [][]evItem{newStream("bin.000001", 157, "8.4.3").gtid(uuidA, 6).query("shop", "BEGIN").
		table(10, "shop", "t", fullNames, 0).rows(binlog.WriteRowsEventV2, 10, img(9, "x")).xid().items}
	s := newSource(t, f.srcFake, map[string]any{"tables": []string{"shop.*"}, "batch_size": 2, "snapshot": "initial"})
	r := startSrc(s, nil, false)
	recs := r.take(t, 6)
	r.stop(t)
	if got := ids(recs); got != "1,2,3,4,5,9" {
		t.Fatal(got)
	}
	start := `{"gtids":"` + uuidA + `:1-5","file":"bin.000001","pos":157`
	if p := string(recs[2].Position); p != start+`,"snapshot":{"table":"shop.t","key":{"map":{"id":{"int":3}}}}}` {
		t.Fatal(p)
	}
	if p := string(recs[4].Position); p != start+`}` || recs[4].Op != fitsync.OpInsert || recs[4].Tx.ID != "" {
		t.Fatal(p)
	}
	if v, _ := recs[0].After.Get("v"); v.Text() != "v1" || v.Kind() != fitsync.KindString {
		t.Fatal(recString(recs[0]))
	}
	g, _ := parseGTIDSet(uuidA + ":1-5")
	if d := f.dumpList(); len(d) != 1 || !strings.HasSuffix(string(d[0]), string(g.encode())) {
		t.Fatalf("dump %x", d)
	}
	e := f.execList()
	if !strings.Contains(e[0], "time_zone = '+00:00'") || e[1] != "SET SESSION TRANSACTION ISOLATION LEVEL REPEATABLE READ" ||
		e[2] != "START TRANSACTION WITH CONSISTENT SNAPSHOT" || e[3] != "COMMIT" || len(e) != 9 {
		t.Fatal(e)
	}

	// From the snapshot's checkpoint: the rows after its key, then the
	// stream from its start.
	f.streams = [][]evItem{newStream("bin.000001", 157, "8.4.3").items}
	r = startSrc(s, recs[2].Position, false)
	if got := ids(r.take(t, 2)); got != "4,5" {
		t.Fatal(got)
	}
	r.stop(t)

	// A checkpoint in the stream reads no snapshot, unless always.
	f.streams = [][]evItem{newStream("bin.000001", 157, "8.4.3").gtid(uuidA, 6).query("shop", "BEGIN").
		table(10, "shop", "t", fullNames, 0).rows(binlog.WriteRowsEventV2, 10, img(9, "x")).xid().items}
	r = startSrc(s, recs[4].Position, false)
	if got := ids(r.take(t, 1)); got != "9" {
		t.Fatal(got)
	}
	r.stop(t)
	always := newSource(t, f.srcFake, map[string]any{"tables": []string{"shop.t"}, "snapshot": "always"})
	f.streams = nil
	r = startSrc(always, recs[4].Position, false)
	if got := ids(r.take(t, 5)); got != "1,2,3,4,5" {
		t.Fatal(got)
	}
	r.stop(t)
	never := newSource(t, f.srcFake, map[string]any{"tables": []string{"shop.t"}, "snapshot": "never"})
	f.streams = [][]evItem{newStream("bin.000001", 157, "8.4.3").gtid(uuidA, 6).query("shop", "BEGIN").
		table(10, "shop", "t", fullNames, 0).rows(binlog.WriteRowsEventV2, 10, img(9, "x")).xid().items}
	r = startSrc(never, nil, false)
	if got := ids(r.take(t, 1)); got != "9" {
		t.Fatal(got)
	}
	r.stop(t)
	if plan, err := never.Chunks(context.Background()); err != nil || plan != nil {
		t.Fatal(plan, err)
	}
}

func TestSnapshotErrors(t *testing.T) {
	for name, c := range map[string]struct {
		params map[string]any
		from   string
		fail   func(q string) error
		exec   func(q string) error
		dial   bool
		want   string
	}{
		"no key":     {params: map[string]any{"tables": []string{"other.*"}}, want: "no primary key"},
		"keys":       {params: map[string]any{"tables": []string{"other.x"}, "keys": map[string]any{"other.x": []string{"nope"}}}, want: `"nope"`},
		"tables":     {fail: errOn("TABLE_TYPE"), want: "listing the tables"},
		"columns":    {fail: errOn("COLUMNS"), want: "columns of"},
		"page":       {fail: errOn("SELECT `id`, `v`"), want: "reading shop.t"},
		"begin":      {exec: errOn("START TRANSACTION"), want: "reading shop.t"},
		"session":    {exec: errOn("SET SESSION"), want: "cut"},
		"server":     {fail: errOn("@@GLOBAL.log_bin"), want: "cut"},
		"status":     {fail: errOn("SHOW BINARY LOG STATUS"), want: "cut"},
		"dial":       {dial: true, want: "cut"},
		"checkpoint": {from: `{"file":"f","pos":4,"snapshot":{"table":"shop.t","key":{"map":7}}}`, want: "checkpoint's key"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newSnapFake(1, 2)
			f.fail, f.execErr = c.fail, c.exec
			if c.dial {
				f.dialErr = func(int) error { return errCut }
			}
			p := map[string]any{"tables": []string{"shop.t"}, "snapshot": "initial"}
			for k, v := range c.params {
				p[k] = v
			}
			s := newSource(t, f.srcFake, p)
			var from fitsync.Position
			if c.from != "" {
				from = fitsync.Position(c.from)
			}
			err := startSrc(s, from, false).wait(t)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatal(err)
			}
			if _, err := s.Chunks(context.Background()); err == nil && !strings.Contains("status page begin checkpoint", name) {
				t.Fatal("Chunks: no error")
			}
		})
	}
}

func errOn(sub string) func(q string) error {
	return func(q string) error {
		if strings.Contains(q, sub) {
			return errCut
		}
		return nil
	}
}

// TestSnapshotChunks splits a job's snapshot by key and reads its chunks,
// one resumed after a position.
func TestSnapshotChunks(t *testing.T) {
	f := newSnapFake(1, 2, 3, 4, 5, 6, 7)
	s := newSource(t, f.srcFake, map[string]any{"tables": []string{"shop.t"}, "chunk_rows": 3, "batch_size": 2, "snapshot": "initial"})
	plan, err := s.Chunks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(plan)
	want := `[{"namespace":"shop.t","hi":{"map":{"id":{"int":4}}},"rows":3},` +
		`{"namespace":"shop.t","lo":{"map":{"id":{"int":4}}},"hi":{"map":{"id":{"int":7}}},"rows":3},` +
		`{"namespace":"shop.t","lo":{"map":{"id":{"int":7}}},"rows":1}]`
	if string(b) != want {
		t.Fatal(string(b))
	}
	var all []string
	for _, ch := range plan {
		var recs []*fitsync.Record
		if err := s.ReadChunk(context.Background(), ch, nil, func(r *fitsync.Record) error { recs = append(recs, r); return nil }); err != nil {
			t.Fatal(err)
		}
		all = append(all, ids(recs))
	}
	if strings.Join(all, "|") != "1,2,3|4,5,6|7" {
		t.Fatal(all)
	}
	var recs []*fitsync.Record
	from := fitsync.Position(`{"table":"shop.t","key":{"map":{"id":{"int":5}}}}`)
	if err := s.ReadChunk(context.Background(), plan[1], from, func(r *fitsync.Record) error { recs = append(recs, r); return nil }); err != nil {
		t.Fatal(err)
	}
	if ids(recs) != "6" || string(recs[0].Position) != `{"table":"shop.t","key":{"map":{"id":{"int":6}}}}` {
		t.Fatal(recStrings(recs))
	}
	emitErr := errors.New("stop")
	if err := s.ReadChunk(context.Background(), plan[0], nil, func(*fitsync.Record) error { return emitErr }); !errors.Is(err, emitErr) {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		ch   fitsync.Chunk
		from string
	}{
		"lo":    {ch: fitsync.Chunk{Namespace: "shop.t", Lo: json.RawMessage(`{`)}},
		"hi":    {ch: fitsync.Chunk{Namespace: "shop.t", Hi: json.RawMessage(`{`)}},
		"from":  {ch: fitsync.Chunk{Namespace: "shop.t"}, from: `{`},
		"key":   {ch: fitsync.Chunk{Namespace: "shop.t"}, from: `{"key":{"x":1}}`},
		"table": {ch: fitsync.Chunk{Namespace: "shop.gone"}},
		"col":   {ch: fitsync.Chunk{Namespace: "shop.t", Lo: json.RawMessage(`{"map":{"x":{"int":1}}}`)}},
	} {
		var p fitsync.Position
		if c.from != "" {
			p = fitsync.Position(c.from)
		}
		if err := s.ReadChunk(context.Background(), c.ch, p, func(*fitsync.Record) error { return nil }); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	f.dialErr = func(int) error { return errCut }
	if err := s.ReadChunk(context.Background(), plan[0], nil, nil); !errors.Is(err, errCut) {
		t.Fatal(err)
	}
	f.dialErr = nil
	f.fail = errOn("TABLE_ROWS")
	if _, err := s.Chunks(context.Background()); !errors.Is(err, errCut) {
		t.Fatal(err)
	}
	f.fail = errOn("OFFSET")
	if _, err := s.Chunks(context.Background()); !errors.Is(err, errCut) {
		t.Fatal(err)
	}
	f.fail = errOn("TABLE_TYPE")
	if err := s.ReadChunk(context.Background(), plan[0], nil, nil); !errors.Is(err, errCut) {
		t.Fatal(err)
	}
}

func TestWireValues(t *testing.T) {
	col := func(typ byte, dec byte, coll uint16) *mysqlwire.Column {
		return &mysqlwire.Column{Type: typ, Decimals: dec, Collation: coll}
	}
	for _, c := range []struct {
		col  *mysqlwire.Column
		v    any
		want string
	}{
		{col(mysqlwire.TypeLong, 0, 63), nil, "null"},
		{col(mysqlwire.TypeLong, 0, 63), int64(-5), "-5"},
		{col(mysqlwire.TypeLong, 0, 63), uint64(5), "5"},
		{col(mysqlwire.TypeYear, 0, 63), uint64(2024), "2024"},
		{col(mysqlwire.TypeFloat, 0, 63), float32(0.1), "0.1"},
		{col(mysqlwire.TypeDouble, 0, 63), 2.5, "2.5"},
		{col(mysqlwire.TypeTime, 3, 63), -(time.Hour + 2*time.Minute + 3*time.Second + 250*time.Millisecond), `"-01:02:03.250"`},
		{col(mysqlwire.TypeTime, 0, 63), 838 * time.Hour, `"838:00:00"`},
		{col(mysqlwire.TypeDate, 0, 63), mysqlwire.DateTime{Year: 2024, Month: 2, Day: 29}, `"2024-02-29"`},
		{col(mysqlwire.TypeDate, 0, 63), mysqlwire.DateTime{}, `"0000-00-00"`},
		{col(mysqlwire.TypeDateTime, 2, 63), mysqlwire.DateTime{Year: 2024, Month: 0, Day: 1, Microsecond: 500000}, `"2024-00-01 00:00:00.50"`},
		{col(mysqlwire.TypeTimestamp, 6, 63), mysqlwire.DateTime{Year: 2024, Month: 1, Day: 2, Hour: 3, Microsecond: 7}, `"2024-01-02T03:00:00.000007Z"`},
		{col(mysqlwire.TypeNewDecimal, 0, 63), []byte("-1.50"), "-1.50"},
		{col(mysqlwire.TypeJSON, 0, 63), []byte(`{"a": 1}`), `{"a":1}`},
		{col(mysqlwire.TypeBit, 0, 63), []byte{1, 2}, "258"},
		{col(mysqlwire.TypeGeometry, 0, 63), []byte{1}, `"AQ=="`},
		{col(mysqlwire.TypeBlob, 0, 63), []byte{0, 255}, `"AP8="`},
		{col(mysqlwire.TypeVarString, 0, 45), []byte("é"), `"é"`},
		{col(mysqlwire.TypeVarString, 0, 45), true, `"true"`},
	} {
		if got := plainOf(wireValue(c.col, c.v)); got != c.want {
			t.Errorf("%T %v: %s, want %s", c.v, c.v, got, c.want)
		}
	}
	for _, c := range []struct {
		v    fitsync.Value
		want any
	}{
		{fitsync.Int(-1), int64(-1)}, {fitsync.Uint(1), uint64(1)}, {fitsync.Float(1.5), 1.5},
		{fitsync.Bytes([]byte{1}), []byte{1}}, {fitsync.Null(), nil}, {fitsync.String("s"), "s"},
		{fitsync.Time(time.Date(2024, 1, 2, 3, 4, 5, 6000, time.FixedZone("x", 3600))), "2024-01-02 02:04:05.000006"},
		{fitsync.Date(2024, 1, 2), "2024-01-02"}, {fitsync.Decimal("1.5"), "1.5"},
	} {
		got := paramOf(c.v)
		if b, ok := got.([]byte); ok {
			got = string(b)
			c.want = string(c.want.([]byte))
		}
		if got != c.want {
			t.Errorf("%v: %#v", c.v, got)
		}
	}
}
