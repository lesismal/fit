package syncpostgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	fitsync "github.com/lesismal/fit/sync"
	pgwire "github.com/lesismal/fit/sync/postgres/wire"
)

// snapDB is a fake server's tables for the snapshot: public.t keyed by an
// int id, the first column; public.f with no key. Rows are text.
type snapDB struct {
	mu     sync.Mutex
	cat    *srcCatalog
	rows   map[string][][]string
	snaps  map[string]bool
	sqls   []string
	fail   string // a query containing it fails
	failAt int    // the failAt-th FETCH fails, from 1
	failIs string // a query that is it fails
	fetchN int
	dials  int
	open   int
}

func newSnapDB(n int) *snapDB {
	db := &snapDB{cat: newSrcCatalog(), rows: map[string][][]string{}, snaps: map[string]bool{}}
	for i := 1; i <= n; i++ {
		db.rows["public.t"] = append(db.rows["public.t"], []string{strconv.Itoa(i), "v" + strconv.Itoa(i), "\x00"})
	}
	for i := 1; i <= 3; i++ {
		db.rows["public.f"] = append(db.rows["public.f"], []string{strconv.Itoa(100 + i), "f", `{"x": 1}`})
	}
	return db
}

var snapFields = []pgwire.Field{{Name: "id", TypeOID: pgwire.OIDInt4}, {Name: "v", TypeOID: pgwire.OIDText}, {Name: "doc", TypeOID: pgwire.OIDJSONB}}

// sqlFake is a connection to a snapDB.
type sqlFake struct {
	db     *snapDB
	cursor [][]string
	closed bool
}

func (db *snapDB) dial(context.Context, pgwire.Config) (sqlConn, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.fail == "dial" {
		return nil, errors.New("dial refused")
	}
	db.dials++
	db.open++
	return &sqlFake{db: db}, nil
}

func (db *snapDB) queries() string {
	db.mu.Lock()
	defer db.mu.Unlock()
	return strings.Join(db.sqls, "\n")
}

func (c *sqlFake) ServerVersion() int { return 160000 }
func (c *sqlFake) Close() error {
	c.db.mu.Lock()
	if !c.closed {
		c.closed = true
		c.db.open--
	}
	c.db.mu.Unlock()
	return nil
}

var (
	snapIDRe    = regexp.MustCompile(`SET TRANSACTION SNAPSHOT '([^']*)'`)
	snapTableRe = regexp.MustCompile(`FROM "public"\."(\w+)"`)
	snapCondRe  = regexp.MustCompile(`\("id"\) (>=|<|>) \('(\d+)'\)`)
	snapEveryRe = regexp.MustCompile(`fit_rn % (\d+) = 1`)
	snapFetchRe = regexp.MustCompile(`^FETCH (\d+) FROM fit_snapshot$`)
)

func (c *sqlFake) SimpleQuery(_ context.Context, sql string) ([]pgwire.Result, error) {
	db := c.db
	db.mu.Lock()
	defer db.mu.Unlock()
	db.sqls = append(db.sqls, sql)
	if db.fail != "" && strings.Contains(sql, db.fail) || sql == db.failIs {
		return nil, errors.New("query failed: " + db.fail)
	}
	if m := snapIDRe.FindStringSubmatch(sql); m != nil && !db.snaps[m[1]] {
		return nil, &pgwire.Error{Severity: "ERROR", Code: "22023", Message: "invalid snapshot identifier"}
	}
	table := func() string {
		m := snapTableRe.FindStringSubmatch(sql)
		if m == nil {
			return ""
		}
		return "public." + m[1]
	}
	switch {
	case strings.HasPrefix(sql, "BEGIN"), sql == "COMMIT", strings.HasPrefix(sql, "SET TRANSACTION"):
		return nil, nil
	case strings.HasPrefix(sql, "SELECT count(*)"):
		return srcRows([]string{strconv.Itoa(len(db.rows[table()]))}), nil
	case snapEveryRe.MatchString(sql):
		every, _ := strconv.Atoi(snapEveryRe.FindStringSubmatch(sql)[1])
		var out [][]string
		for i, r := range db.rows[table()] {
			if (i+1)%every == 1 && i > 0 {
				out = append(out, []string{r[0]})
			}
		}
		return srcRows(out...), nil
	case strings.HasPrefix(sql, "DECLARE fit_snapshot"):
		c.cursor = nil
		for _, r := range db.rows[table()] {
			id, _ := strconv.Atoi(r[0])
			ok := true
			for _, m := range snapCondRe.FindAllStringSubmatch(sql, -1) {
				b, _ := strconv.Atoi(m[2])
				switch m[1] {
				case ">=":
					ok = ok && id >= b
				case "<":
					ok = ok && id < b
				case ">":
					ok = ok && id > b
				}
			}
			if ok {
				c.cursor = append(c.cursor, r)
			}
		}
		return nil, nil
	case snapFetchRe.MatchString(sql):
		if db.fetchN++; db.failAt > 0 && db.fetchN == db.failAt {
			return nil, errors.New("fetch failed")
		}
		n, _ := strconv.Atoi(snapFetchRe.FindStringSubmatch(sql)[1])
		n = min(n, len(c.cursor))
		res := srcRows(c.cursor[:n]...)
		res[0].Fields = snapFields
		c.cursor = c.cursor[n:]
		return res, nil
	}
	return db.cat.answer(sql)
}

// snapCatalog is a catalog of public.t keyed by id and public.f with no
// key, REPLICA IDENTITY FULL.
func (db *snapDB) useFakes(t *testing.T, main *srcFake) *[]*srcFake {
	t.Helper()
	var exports []*srcFake
	oldSrc, oldSQL := dialSource, dialSQL
	first := true
	dialSource = func(context.Context, pgwire.Config) (replConn, error) {
		if first {
			first = false
			return main, nil
		}
		db.mu.Lock()
		defer db.mu.Unlock()
		if db.fail == "export" {
			return nil, errors.New("export refused")
		}
		f := newSrcFake(nil)
		f.point = pgwire.LSN(0x50 + 0x10*len(exports))
		f.onExport = func(id string) {
			db.mu.Lock()
			db.snaps[id] = true
			db.mu.Unlock()
		}
		exports = append(exports, f)
		return f, nil
	}
	dialSQL = db.dial
	t.Cleanup(func() { dialSource, dialSQL = oldSrc, oldSQL })
	main.onExport = func(id string) {
		db.mu.Lock()
		db.snaps[id] = true
		db.mu.Unlock()
	}
	return &exports
}

// snapRec is a record as the tests compare it.
func snapRec(r *fitsync.Record) string {
	key, _ := r.Key.AppendJSON(nil)
	after, _ := r.After.AppendJSON(nil)
	s := r.Namespace + " " + r.Op.String() + " " + string(key) + " " + string(after)
	if r.Tx.ID != "" {
		s += " tx " + r.Tx.ID
	}
	return s
}

func snapPos(t *testing.T, p fitsync.Position) sourcePosition {
	t.Helper()
	var sp sourcePosition
	if err := json.Unmarshal(p, &sp); err != nil {
		t.Fatal(err)
	}
	return sp
}

// TestSnapshotNewSlot reads the snapshot of a slot just made, in chunks
// in parallel, then streams from its consistent point.
func TestSnapshotNewSlot(t *testing.T) {
	db := newSnapDB(25)
	db.cat.slot, db.cat.pub = false, false
	main := newSrcFake(db.cat.answer)
	main.point = 0x40
	db.useFakes(t, main)
	s := srcMust(t, map[string]any{"snapshot": "initial", "create": true, "tables": []string{"t", "f"}, "chunk_rows": 10, "snapshot_parallel": 2})
	main.msgs <- msgRel(1, "t", 'd', "id").data()
	main.msgs <- msgBegin(0x100, 10).data()
	main.msgs <- newMsg('I').u32(1).u8('N').tuple("26", "new", nil).data()
	main.msgs <- msgCommit(0x110).data()
	run := srcRun(s, nil, false)
	run.wait(t, 29)
	run.mu.Lock()
	recs := append([]*fitsync.Record(nil), run.recs...)
	run.mu.Unlock()
	if err := run.end(); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(main.created) != 1 || main.created[0].Snapshot != pgwire.SnapshotExport || main.start != 0x40 {
		t.Fatalf("created %+v, started at %v", main.created, main.start)
	}
	seen := map[string]bool{}
	var last sourcePosition
	for i, r := range recs[:28] {
		if r.Tx.ID != "" || r.Source != "postgres" || r.Op != fitsync.OpInsert {
			t.Fatalf("record %d: %+v", i, r)
		}
		seen[snapRec(r)] = true
		if r.Position != nil {
			last = snapPos(t, r.Position)
		}
	}
	if len(seen) != 28 || !seen[`public.t insert {"map":{"id":{"int":7}}} {"map":{"id":{"int":7},"v":"v7","doc":null}}`] ||
		!seen[`public.f insert {"map":{"id":{"int":101},"v":"f","doc":{"json":{"x":1}}}} {"map":{"id":{"int":101},"v":"f","doc":{"json":{"x":1}}}}`] {
		t.Fatalf("%v", seen)
	}
	if string(recs[27].Position) != `{"lsn":"0/40"}` || last.Snapshot != nil {
		t.Fatalf("the snapshot's last position %s", recs[27].Position)
	}
	if got := snapRec(recs[28]); got != `public.t insert {"map":{"id":{"int":26}}} {"map":{"id":{"int":26},"v":"new","doc":null}} tx 10` {
		t.Fatal(got)
	}
	q := db.queries()
	// public.f (keyless) whole, public.t in three chunks, each in the
	// snapshot exported.
	if strings.Count(q, "SET TRANSACTION SNAPSHOT 'snap-"+"slot'") != 5 || !strings.Contains(q, `("id") >= ('11') AND ("id") < ('21')`) {
		t.Fatalf("queries\n%s", q)
	}
	if db.open != 0 {
		t.Fatalf("%d connections left open", db.open)
	}
}

// TestSnapshotResume fails a snapshot of a temporary slot half way: the
// next run resumes in the snapshot held, and one after the exporter is gone
// reads the unfinished tables again from a new one.
func TestSnapshotResume(t *testing.T) {
	db := newSnapDB(30)
	main := newSrcFake(db.cat.answer)
	exports := db.useFakes(t, main)
	s := srcMust(t, map[string]any{"snapshot": "initial", "chunk_rows": 10, "snapshot_parallel": 1})
	var got []*fitsync.Record
	stop := errors.New("the sink failed")
	emitN := func(n int) func(*fitsync.Record) error {
		return func(r *fitsync.Record) error {
			if len(got) == n {
				return stop
			}
			got = append(got, r)
			return nil
		}
	}
	// public.f's 3, then 12 of public.t: two chunks, the second under way.
	if err := s.Run(context.Background(), &fitsync.Run{Emit: emitN(15)}); err != stop {
		t.Fatal(err)
	}
	if len(*exports) != 1 || s.held == nil || s.held.id != "snap-"+s.held.id[5:] {
		t.Fatalf("exports %d, held %+v", len(*exports), s.held)
	}
	var from fitsync.Position
	for _, r := range got {
		if r.Position != nil {
			from = r.Position
		}
	}
	pos := snapPos(t, from)
	if pos.LSN != "0/50" || pos.Snapshot == nil || pos.Snapshot.ID != s.held.id || len(pos.Snapshot.Tables) != 2 {
		t.Fatalf("%s", from)
	}
	// Resumed in the snapshot held: on after the last position's.
	main2 := newSrcFake(db.cat.answer)
	db.useFakes(t, main2)
	got = nil
	if err := s.Run(context.Background(), &fitsync.Run{From: from, Emit: emitN(5)}); err != stop {
		t.Fatal(err)
	}
	if len(*exports) != 1 {
		t.Fatalf("a new export: %d", len(*exports))
	}
	firstID, _ := got[0].Key.Get("id")
	if firstID.AsInt() != 11 && firstID.AsInt() <= 10 {
		t.Fatalf("resumed at %v", firstID)
	}
	// The exporter gone: public.f is done, public.t read again whole from a
	// new snapshot; the stream starts at the first one's point.
	s.dropHeld()
	main3 := newSrcFake(db.cat.answer)
	exports3 := db.useFakes(t, main3)
	got = nil
	main3.msgs <- errors.New("the stream broke")
	if err := s.Run(context.Background(), &fitsync.Run{From: from, Emit: emitN(1000)}); err == nil || err.Error() != "the stream broke" {
		t.Fatal(err)
	}
	if len(*exports3) != 1 || len(got) != 30 || main3.start != 0x50 {
		t.Fatalf("exports %d, %d records, started at %v", len(*exports3), len(got), main3.start)
	}
	for _, r := range got {
		if r.Namespace != "public.t" {
			t.Fatalf("read again: %s", snapRec(r))
		}
	}
	if string(got[29].Position) != `{"lsn":"0/50"}` {
		t.Fatalf("%s", got[29].Position)
	}
	// A checkpoint whose snapshot has every table done starts the stream.
	main4 := newSrcFake(db.cat.answer)
	exports4 := db.useFakes(t, main4)
	main4.msgs <- errors.New("the stream broke")
	done := fitsync.Position(`{"lsn":"0/50","snapshot":{"id":"x","tables":[{"name":"public.t","chunks":2}],"done":"0-1"}}`)
	if err := s.Run(context.Background(), &fitsync.Run{From: done, Emit: emitN(1000)}); err == nil || len(*exports4) != 0 || main4.start != 0x50 {
		t.Fatal(err, len(*exports4), main4.start)
	}
	if db.open != 0 {
		t.Fatalf("%d connections left open", db.open)
	}
}

// TestSnapshotErrors covers what fails while a snapshot is read.
func TestSnapshotErrors(t *testing.T) {
	ctx := context.Background()
	params := map[string]any{"snapshot": "initial", "chunk_rows": 10}
	for _, c := range []struct {
		name string
		edit func(db *snapDB)
		from fitsync.Position
		want string
		held bool
	}{
		{"export refused", func(db *snapDB) { db.fail = "export" }, nil, "export refused", false},
		{"dial refused", func(db *snapDB) { db.fail = "dial" }, nil, "dial refused", false},
		{"count fails", func(db *snapDB) { db.fail = "count(*)" }, nil, "counting public.f", false},
		{"split fails", func(db *snapDB) { db.fail = "fit_rn" }, nil, "splitting public.t", false},
		{"declare fails", func(db *snapDB) { db.fail = "DECLARE" }, nil, "reading public.", true},
		{"fetch fails", func(db *snapDB) { db.failAt = 1 }, nil, "fetch failed", true},
		{"begin fails", func(db *snapDB) { db.failIs = "BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY" }, nil, "query failed", true},
		{"commit fails", func(db *snapDB) { db.fail = "COMMIT" }, nil, "COMMIT", false},
		{"bad done", nil, fitsync.Position(`{"lsn":"0/1","snapshot":{"id":"x","tables":[{"name":"public.t","chunks":1}],"done":"5"}}`), "chunks done", false},
	} {
		db := newSnapDB(30)
		if c.edit != nil {
			c.edit(db)
		}
		main := newSrcFake(db.cat.answer)
		main.msgs <- errors.New("the stream broke")
		db.useFakes(t, main)
		s := srcMust(t, params)
		err := s.Run(ctx, &fitsync.Run{From: c.from, Emit: func(*fitsync.Record) error { return nil }})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
		if (s.held != nil) != c.held {
			t.Errorf("%s: held %v", c.name, s.held != nil)
		}
		s.Cleanup()
	}

	// An exporter whose snapshot is gone is not held.
	db := newSnapDB(5)
	db.useFakes(t, newSrcFake(db.cat.answer))
	s := srcMust(t, params)
	db.snaps = map[string]bool{}
	db.mu.Lock()
	db.fail = ""
	db.mu.Unlock()
	orig := dialSQL
	dialSQL = func(ctx context.Context, cfg pgwire.Config) (sqlConn, error) {
		db.mu.Lock()
		for k := range db.snaps {
			// The planning sees it; the readers do not.
			if strings.Contains(strings.Join(db.sqls, " "), "fit_snapshot") || strings.Contains(strings.Join(db.sqls, " "), "count(*)") {
				delete(db.snaps, k)
			}
		}
		db.mu.Unlock()
		return orig(ctx, cfg)
	}
	if err := s.Run(ctx, &fitsync.Run{Emit: func(*fitsync.Record) error { return nil }}); !errors.Is(err, errSnapshotGone) || s.held != nil {
		t.Fatalf("%v, held %v", err, s.held != nil)
	}

	// A held exporter expires.
	old := holdFor
	holdFor = time.Millisecond
	defer func() { holdFor = old }()
	f := newSrcFake(nil)
	s.hold(&exporter{id: "a", conn: f})
	s.hold(&exporter{id: "b", conn: newSrcFake(nil)}) // the first closed
	<-f.closed
	waitFor(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.held == nil })
}

// TestSnapshotJob plans and reads a job's chunks, each in a transaction
// of its own.
func TestSnapshotJob(t *testing.T) {
	ctx := context.Background()
	db := newSnapDB(25)
	db.useFakes(t, newSrcFake(db.cat.answer))
	s := srcMust(t, map[string]any{"snapshot": "initial", "chunk_rows": 10})
	plan, err := s.Chunks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(plan)
	if string(b) != `[{"namespace":"public.f","rows":3},{"namespace":"public.t","hi":["11"],"rows":10},{"namespace":"public.t","lo":["11"],"hi":["21"],"rows":10},{"namespace":"public.t","lo":["21"],"rows":5}]` {
		t.Fatal(string(b))
	}
	var got []string
	var last fitsync.Position
	emit := func(r *fitsync.Record) error {
		got = append(got, snapRec(r))
		last = r.Position
		return nil
	}
	if err := s.ReadChunk(ctx, plan[2], fitsync.Position(`{"key":["15"]}`), emit); err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 || !strings.HasPrefix(got[0], `public.t insert {"map":{"id":{"int":16}}}`) || string(last) != `{"key":["20"]}` {
		t.Fatal(got, string(last))
	}
	got = nil
	// A keyless table's chunk is read whole, from wherever.
	if err := s.ReadChunk(ctx, plan[0], fitsync.Position(`{"key":["1"]}`), emit); err != nil || len(got) != 3 || last != nil {
		t.Fatal(err, got)
	}
	if strings.Contains(db.queries(), "SET TRANSACTION SNAPSHOT") {
		t.Fatal("a job's chunk took a snapshot")
	}
	for _, c := range []struct {
		ch   fitsync.Chunk
		from fitsync.Position
		want string
	}{
		{plan[1], fitsync.Position(`x`), "the chunk's position"},
		{plan[1], fitsync.Position(`{"key":[1]}`), "is not a key of public.t"},
		{fitsync.Chunk{Namespace: "public.gone"}, nil, "no longer to be read"},
	} {
		if err := s.ReadChunk(ctx, c.ch, c.from, emit); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v, want %q", c.ch, err, c.want)
		}
	}
	db.fail = "dial"
	if err := s.ReadChunk(ctx, plan[1], nil, emit); err == nil {
		t.Fatal("dialed")
	}
	if _, err := s.Chunks(ctx); err == nil {
		t.Fatal("dialed")
	}
	db.fail = "count(*)"
	if _, err := s.Chunks(ctx); err == nil {
		t.Fatal("counted")
	}
	db.fail = "SELECT pubupdate"
	if _, err := s.Chunks(ctx); err == nil {
		t.Fatal("no publication read")
	}
	s.tables = nil
	if err := s.ReadChunk(ctx, plan[1], nil, emit); err == nil {
		t.Fatal("no publication read")
	}
	db.fail = ""

	// With no publication, tables; snapshot never plans none.
	db.cat.pub, db.cat.idents = false, db.cat.idents[:1]
	s = srcMust(t, map[string]any{"snapshot": "initial", "tables": []string{"t"}})
	if plan, err := s.Chunks(ctx); err != nil || len(plan) != 1 || plan[0].Namespace != "public.t" || plan[0].Rows != 25 {
		t.Fatal(plan, err)
	}
	if plan, err := srcMust(t, nil).Chunks(ctx); plan != nil || err != nil {
		t.Fatal(plan, err)
	}
}

// TestSnapshotTables covers how tables are read: keys, column lists, row
// filters and the progress's ranges.
func TestSnapshotTables(t *testing.T) {
	ti := newTableInfo("s.u", "i", []string{"id"}, []string{"b", "a"}, []string{"a", "b", "c", "id"}, pubTable{cols: []string{"a", "b", "id"}, filter: "a > 1"})
	if fmt.Sprint(ti.cols, ti.key, ti.order) != "[a b id] [0 1] [1 0]" || ti.from() != `"s"."u"` {
		t.Fatal(ti.cols, ti.key, ti.order)
	}
	if w, err := ti.where(json.RawMessage(`["1","2"]`), nil, nil); err != nil || w != ` WHERE (a > 1) AND ("b", "a") >= ('1', '2')` {
		t.Fatal(w, err)
	}
	if _, err := ti.where(json.RawMessage(`["1"]`), nil, nil); err == nil {
		t.Fatal("a short key")
	}
	if ti = newTableInfo("public.f", "f", nil, nil, []string{"a", "b"}, pubTable{}); fmt.Sprint(ti.key, ti.order) != "[0 1] []" {
		t.Fatal(ti.key, ti.order)
	}
	if ti = newTableInfo("public.d", "d", nil, nil, []string{"a"}, pubTable{}); ti.key != nil || ti.order != nil {
		t.Fatal(ti.key)
	}
	if ti = newTableInfo("public.k", "d", []string{"x"}, nil, []string{"a"}, pubTable{}); ti.key != nil {
		t.Fatal("a key of a column not read")
	}
	rec, key := newTableInfo("public.d", "d", nil, nil, []string{"a"}, pubTable{}).record(snapFields[:1], [][]byte{nil})
	if !rec.Key.IsNull() || key != nil || snapRec(rec) != `public.d insert null {"map":{"a":null}}` {
		t.Fatal(snapRec(rec))
	}

	for _, c := range []struct {
		set  []bool
		want string
	}{{nil, ""}, {[]bool{true, true, false, true}, "0-1,3"}, {[]bool{false, true, true, true}, "1-3"}} {
		if got := formatRanges(c.set); got != c.want {
			t.Errorf("%v: %q", c.set, got)
		}
		if back, err := parseRanges(c.want, len(c.set)); err != nil || formatRanges(back) != c.want {
			t.Errorf("%q: %v %v", c.want, back, err)
		}
	}
	for _, bad := range []string{"x", "1-x", "3-1", "9", "-1"} {
		if _, err := parseRanges(bad, 5); err == nil {
			t.Errorf("%q read", bad)
		}
	}
	st := &snapshotState{Tables: []snapTable{{"a", 2}, {"b", 1}, {"c", 0}}, Done: "0-1"}
	if left, err := st.unfinished(); err != nil || fmt.Sprint(left) != "[b c]" {
		t.Fatal(left, err)
	}
	st.Done = "x"
	if _, err := st.unfinished(); err == nil {
		t.Fatal("bad ranges")
	}

	// The publication's column lists and row filters, from 15.
	cat := newSrcCatalog()
	f := newSrcFake(func(sql string) ([]pgwire.Result, error) {
		if strings.HasPrefix(sql, "SELECT schemaname, tablename, attnames, rowfilter") {
			return srcRows([]string{"public", "t", "{id,v}", "(id > 5)"}, []string{"public", "f", "\x00", "\x00"}), nil
		}
		return cat.answer(sql)
	})
	setup, err := srcMust(t, nil).publication(context.Background(), f, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if tt := setup.tables["public.t"]; fmt.Sprint(tt.cols) != "[id v]" || tt.filter != "(id > 5)" || fmt.Sprint(setup.names) != "[public.f public.t]" {
		t.Fatalf("%+v", tt)
	}
	cat.idents[0][4] = "["
	if _, err := srcMust(t, nil).publication(context.Background(), f, false, false); err == nil || !strings.Contains(err.Error(), "replica identity") {
		t.Fatal(err)
	}
	if (&Source{ChunkRows: 7, SnapshotParallel: 3}).chunkRows() != 7 || (&Source{SnapshotParallel: 3}).parallel() != 3 {
		t.Fatal("params")
	}
}
