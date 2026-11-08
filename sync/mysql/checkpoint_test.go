package syncmysql

import (
	"errors"
	"strings"
	"testing"

	fitsync "github.com/lesismal/fit/sync"
)

const ckTable = "_fit_sync_checkpoints"

// at gives recs positions: each record's index after base.
func at(base int, recs ...*fitsync.Record) []*fitsync.Record {
	for i, r := range recs {
		r.Position = fitsync.Position(`{"n":` + string(rune('0'+base+i)) + `}`)
	}
	return recs
}

// kept returns the checkpoint's row of pipeline p.
func kept(t *testing.T, d *fakeDB) string {
	t.Helper()
	rows := d.rows(ckTable)
	if len(rows) == 0 {
		return ""
	}
	if len(rows) != 1 {
		t.Fatalf("%v", rows)
	}
	// The row's time changes; the rest is what counts.
	i := strings.Index(rows[0], ",updated=")
	return rows[0][:i]
}

// TestCheckpointInTransaction: with checkpoint sink, each commit writes the
// position of its last record that ends a source transaction, in the same
// transaction; one held open is not; ReadCheckpoint reads it back and
// drops what was held open.
func TestCheckpointInTransaction(t *testing.T) {
	for _, mode := range []string{"source", "batch"} {
		t.Run(mode, func(t *testing.T) {
			d := newFakeDB(map[string][]string{"t": {"id"}})
			s := fakeSink(t, d, map[string]any{"checkpoint": "sink", "database": "db", "transactions": mode})
			if !s.KeepsCheckpoint() {
				t.Fatal("keeps none")
			}
			pos, lin, err := s.ReadCheckpoint(ctxT(t), "p", "L1")
			if err != nil || pos != nil || lin != "" {
				t.Fatalf("%s %s %v", pos, lin, err)
			}
			tx1 := at(0, inTx("a", rec("t", fitsync.OpInsert, 1, "a"), rec("t", fitsync.OpInsert, 2, "b"))...)
			tx2 := at(2, inTx("b", rec("t", fitsync.OpInsert, 3, "c"), rec("t", fitsync.OpInsert, 4, "d"))...)
			if err := s.Write(ctxT(t), append(tx1, tx2[0])); err != nil {
				t.Fatal(err)
			}
			want(t, d, "t", "id=1,v=a", "id=2,v=b")
			if got := kept(t, d); got != `lineage=L1,pipeline=p,position={"n":1}` {
				t.Fatalf("%s", got)
			}
			// A new run drops tx2's first record, held open.
			pos, lin, err = s.ReadCheckpoint(ctxT(t), "p", "L2")
			if err != nil || string(pos) != `{"n":1}` || lin != "L1" {
				t.Fatalf("%s %s %v", pos, lin, err)
			}
			if err := s.Write(ctxT(t), tx2[1:]); err != nil {
				t.Fatal(err)
			}
			want(t, d, "t", "id=1,v=a", "id=2,v=b", "id=4,v=d")
			if got := kept(t, d); got != `lineage=L2,pipeline=p,position={"n":3}` {
				t.Fatalf("%s", got)
			}
			// A DDL's position goes with the next commit.
			ddl := at(4, &fitsync.Record{Namespace: "t", Op: fitsync.OpDDL, After: fitsync.String("ALTER TABLE t")})
			if err := s.Write(ctxT(t), append(ddl, at(5, rec("t", fitsync.OpInsert, 5, "e"))...)); err != nil {
				t.Fatal(err)
			}
			if got := kept(t, d); got != `lineage=L2,pipeline=p,position={"n":5}` {
				t.Fatalf("%s", got)
			}
		})
	}
}

// TestCheckpointRefused: a refused record commits what was written before
// it, with the position of the last that ends a source transaction.
func TestCheckpointRefused(t *testing.T) {
	d := newFakeDB(map[string][]string{"t": {"id"}})
	s := fakeSink(t, d, map[string]any{"checkpoint": "sink", "checkpoint_table": "db." + ckTable})
	if _, _, err := s.ReadCheckpoint(ctxT(t), "p", "L"); err != nil {
		t.Fatal(err)
	}
	recs := at(0, rec("t", fitsync.OpInsert, 1, "a"), rec("t", fitsync.OpInsert, 2, "BAD"))
	var be *fitsync.BatchError
	if err := s.Write(ctxT(t), recs); !errors.As(err, &be) || be.Index != 1 {
		t.Fatalf("%v", err)
	}
	rows := d.rows("db." + ckTable)
	if len(rows) != 1 || !strings.Contains(rows[0], `position={"n":0}`) {
		t.Fatalf("%v", rows)
	}
}

// TestCheckpointFailures: a failure reading the checkpoint, or writing it
// at a commit, is the call's error, and the next call goes on.
func TestCheckpointFailures(t *testing.T) {
	boom := errors.New("boom")
	for _, stage := range []string{"dial", "CREATE", "SELECT", "INSERT INTO `_fit"} {
		t.Run(stage, func(t *testing.T) {
			d := newFakeDB(map[string][]string{"t": {"id"}})
			s := fakeSink(t, d, map[string]any{"checkpoint": "sink", "database": "db"})
			d.fail = func(q string, args []any) error {
				if strings.HasPrefix(q, stage) {
					return boom
				}
				return nil
			}
			recs := at(0, rec("t", fitsync.OpInsert, 1, "a"))
			_, _, err := s.ReadCheckpoint(ctxT(t), "p", "L")
			if err == nil {
				err = s.Write(ctxT(t), recs)
			}
			if !errors.Is(err, boom) {
				t.Fatalf("%v", err)
			}
			d.fail = nil
			if _, _, err := s.ReadCheckpoint(ctxT(t), "p", "L"); err != nil {
				t.Fatal(err)
			}
			if err := s.Write(ctxT(t), recs); err != nil {
				t.Fatal(err)
			}
			if got := kept(t, d); got != `lineage=L,pipeline=p,position={"n":0}` {
				t.Fatalf("%s", got)
			}
		})
	}
}

// TestCheckpointOff: checkpoint file reads nothing and writes no row; a
// checkpoint_table with no database is refused.
func TestCheckpointOff(t *testing.T) {
	d := newFakeDB(map[string][]string{"t": {"id"}})
	s := fakeSink(t, d, nil)
	if s.KeepsCheckpoint() {
		t.Fatal("keeps")
	}
	if pos, lin, err := s.ReadCheckpoint(ctxT(t), "p", "L"); pos != nil || lin != "" || err != nil || d.dials != 0 {
		t.Fatalf("%s %s %v %d", pos, lin, err, d.dials)
	}
	writeAll(t, s, at(0, rec("t", fitsync.OpInsert, 1, "a")), 1)
	if rows := d.rows(ckTable); len(rows) != 0 {
		t.Fatalf("%v", rows)
	}
	if _, err := load(t, map[string]any{"username": "u", "checkpoint": "sink"}); err == nil || !strings.Contains(err.Error(), "checkpoint_table") {
		t.Fatalf("%v", err)
	}
}

// TestQueryRow: the client's connection answers QueryRow, none for a
// statement of no rows, and a statement's error.
func TestQueryRow(t *testing.T) {
	addr := fakeServer(t, "8.4.3")
	s := loadSink(t, map[string]any{"username": "u", "address": addr})
	if err := s.connect(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	if row, err := s.conn.QueryRow(ctxT(t), "SELECT 1"); err != nil || row != nil {
		t.Fatalf("%v %v", row, err)
	}
	if row, err := s.conn.QueryRow(ctxT(t), "SELECT 'row'"); err != nil || len(row) != 1 || text(row[0]) != "row" {
		t.Fatalf("%v %v", row, err)
	}
	if text(7) != "7" {
		t.Fatal(text(7))
	}
	s.conn.Close()
	if _, err := s.conn.QueryRow(ctxT(t), "SELECT 1"); err == nil {
		t.Fatal("a closed connection")
	}
}
