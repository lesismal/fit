package syncmysql

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	fitsync "github.com/lesismal/fit/sync"
	mysqlwire "github.com/lesismal/fit/sync/mysql/wire"
)

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute) // a hang guard
	t.Cleanup(cancel)
	return ctx
}

func idKey(id int64) fitsync.Value {
	return fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(id)})
}

func row(id int64, v string) fitsync.Value {
	return fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(id)}, fitsync.Field{Name: "v", Value: fitsync.String(v)})
}

func rec(table string, op fitsync.Op, id int64, v string) *fitsync.Record {
	r := &fitsync.Record{Namespace: table, Op: op, Key: idKey(id)}
	if op == fitsync.OpDelete {
		r.Before = row(id, v)
	} else {
		r.After = row(id, v)
	}
	return r
}

// inTx marks recs as one source transaction.
func inTx(id string, recs ...*fitsync.Record) []*fitsync.Record {
	for i, r := range recs {
		r.Tx = fitsync.Tx{ID: id, First: i == 0, Last: i == len(recs)-1}
	}
	return recs
}

func want(t *testing.T, d *fakeDB, table string, rows ...string) {
	t.Helper()
	got := d.rows(table)
	if strings.Join(got, ";") != strings.Join(rows, ";") {
		t.Fatalf("%s holds\n%s\nwant\n%s", table, strings.Join(got, "\n"), strings.Join(rows, "\n"))
	}
}

func writeAll(t *testing.T, s *Sink, recs []*fitsync.Record, size int) {
	t.Helper()
	for i := 0; i < len(recs); i += size {
		if err := s.Write(ctxT(t), recs[i:min(i+size, len(recs))]); err != nil {
			t.Fatal(err)
		}
	}
}

// changes is a stream of transactions over two tables: inserts, updates,
// an update of a key, deletes, and the rows it leaves in t.
func changes() ([]*fitsync.Record, []string) {
	var recs []*fitsync.Record
	state := map[int64]string{}
	n := 0
	for i := range int64(60) {
		recs = append(recs, inTx(fmt.Sprint("a", i), rec("db.t", fitsync.OpInsert, i, "v1"), rec("db.u", fitsync.OpInsert, i, "u"))...)
		state[i] = "v1"
	}
	for i := range int64(60) {
		var tx []*fitsync.Record
		switch {
		case i%10 == 0:
			tx = append(tx, rec("db.t", fitsync.OpDelete, i, "v1"), rec("db.t", fitsync.OpInsert, i, "v3"))
			state[i] = "v3"
		case i%7 == 0:
			// The key changes from i to 1000+i.
			r := rec("db.t", fitsync.OpUpdate, 1000+i, "moved")
			r.Before = row(i, "v1")
			tx = append(tx, r)
			delete(state, i)
			state[1000+i] = "moved"
		case i%5 == 0:
			tx = append(tx, rec("db.t", fitsync.OpDelete, i, "v1"))
			delete(state, i)
		case i%2 == 0:
			tx = append(tx, rec("db.t", fitsync.OpUpdate, i, "v2"), rec("db.u", fitsync.OpDelete, i, "u"))
			state[i] = "v2"
		default:
			continue
		}
		n++
		recs = append(recs, inTx(fmt.Sprint("b", i), tx...)...)
	}
	var rows []string
	for id, v := range state {
		rows = append(rows, fmt.Sprintf("id=%d,v=%s", id, v))
	}
	sortStrings(rows)
	return recs, rows
}

func sortStrings(s []string) {
	for i := range s {
		for j := i + 1; j < len(s); j++ {
			if s[j] < s[i] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
}

// TestConverges writes a stream in batches of several sizes, then replays
// it from its middle and its start: the tables hold each key's last state,
// in either mode.
func TestConverges(t *testing.T) {
	for _, mode := range []string{"source", "batch"} {
		t.Run(mode, func(t *testing.T) {
			d := newFakeDB(map[string][]string{"db.t": {"id"}, "db.u": {"id"}})
			s := fakeSink(t, d, map[string]any{"transactions": mode})
			recs, rows := changes()
			writeAll(t, s, recs, 7)
			writeAll(t, s, recs[len(recs)/2:], 13)
			writeAll(t, s, recs, 50)
			want(t, d, "db.t", rows...)
			if got := len(d.rows("db.u")); got != 40 {
				t.Errorf("db.u has %d rows", got)
			}
			if d.dials != 1 {
				t.Errorf("%d dials", d.dials)
			}
		})
	}
}

// TestSourceTransactions: a source transaction across two batches is
// committed once its last record is written, and records without one are
// committed at the batch's end.
func TestSourceTransactions(t *testing.T) {
	d := newFakeDB(map[string][]string{"t": {"id"}})
	s := fakeSink(t, d, nil)
	tx := inTx("x", rec("t", fitsync.OpInsert, 1, "a"), rec("t", fitsync.OpInsert, 2, "b"), rec("t", fitsync.OpInsert, 3, "c"))
	plain := rec("t", fitsync.OpInsert, 9, "z")
	if err := s.Write(ctxT(t), []*fitsync.Record{plain, tx[0], tx[1]}); err != nil {
		t.Fatal(err)
	}
	want(t, d, "t", "id=9,v=z")
	if err := s.Write(ctxT(t), tx[2:]); err != nil {
		t.Fatal(err)
	}
	want(t, d, "t", "id=1,v=a", "id=2,v=b", "id=3,v=c", "id=9,v=z")
	// Records without a transaction at a batch's end.
	if err := s.Write(ctxT(t), []*fitsync.Record{rec("t", fitsync.OpDelete, 9, "z"), rec("t", fitsync.OpUpdate, 1, "A")}); err != nil {
		t.Fatal(err)
	}
	want(t, d, "t", "id=1,v=A", "id=2,v=b", "id=3,v=c")
	st := strings.Join(d.statements(), "\n")
	if strings.Count(st, "COMMIT") != 3 || strings.Count(st, "START TRANSACTION") != 3 {
		t.Errorf("statements:\n%s", st)
	}
}

// TestBatchTransactions: with transactions batch, a batch's transactions
// go in one of the server's, committed at the last record that ends one;
// the part of a transaction after it is held open into the next batch.
func TestBatchTransactions(t *testing.T) {
	d := newFakeDB(map[string][]string{"t": {"id"}})
	s := fakeSink(t, d, map[string]any{"transactions": "batch"})
	a := inTx("a", rec("t", fitsync.OpInsert, 1, "a"))
	b := inTx("b", rec("t", fitsync.OpInsert, 2, "b"), rec("t", fitsync.OpInsert, 3, "b"))
	c := inTx("c", rec("t", fitsync.OpInsert, 4, "c"), rec("t", fitsync.OpInsert, 5, "c"))
	if err := s.Write(ctxT(t), []*fitsync.Record{a[0], b[0], b[1], c[0]}); err != nil {
		t.Fatal(err)
	}
	want(t, d, "t", "id=1,v=a", "id=2,v=b", "id=3,v=b")
	if err := s.Write(ctxT(t), c[1:]); err != nil {
		t.Fatal(err)
	}
	want(t, d, "t", "id=1,v=a", "id=2,v=b", "id=3,v=b", "id=4,v=c", "id=5,v=c")
	if n := strings.Count(strings.Join(d.statements(), "\n"), "COMMIT"); n != 2 {
		t.Errorf("%d commits", n)
	}
}

// TestParallelWriters writes a job's batches from several goroutines at
// once, as a job's writers do: each Write on a connection of its own, the
// spare ones kept for the next, every row written, and Cleanup closing
// them all.
func TestParallelWriters(t *testing.T) {
	var _ fitsync.ParallelSink = (*Sink)(nil)
	for _, mode := range []string{"source", "batch"} {
		t.Run(mode, func(t *testing.T) {
			d := newFakeDB(map[string][]string{"t": {"id"}})
			s := fakeSink(t, d, map[string]any{"transactions": mode})
			const writers, batches, size = 4, 25, 20
			var wg sync.WaitGroup
			errs := make(chan error, writers)
			for w := range writers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for b := range batches {
						var batch []*fitsync.Record
						for i := range size {
							batch = append(batch, rec("t", fitsync.OpInsert, int64((w*batches+b)*size+i), "v"))
						}
						if err := s.Write(ctxT(t), batch); err != nil {
							errs <- err
							return
						}
					}
				}()
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				t.Fatal(err)
			}
			if n := len(d.rows("t")); n != writers*batches*size {
				t.Fatalf("%d rows", n)
			}
			if d.dials < 1 || d.dials > writers {
				t.Fatalf("%d dials", d.dials)
			}
			st := strings.Join(d.statements(), "\n")
			if n := strings.Count(st, "COMMIT"); n != writers*batches {
				t.Errorf("%d commits", n)
			}
			s.Cleanup()
			d.mu.Lock()
			defer d.mu.Unlock()
			for i, c := range d.conns {
				if !c.closed {
					t.Errorf("connection %d left open", i)
				}
			}
			if len(s.spare) != 0 || s.busy {
				t.Errorf("%d spare writers, busy %v", len(s.spare), s.busy)
			}
		})
	}
}

// TestReconnectWritesOpenAgain breaks the connection while a source
// transaction is open across batches: the next write connects again and
// writes the acknowledged part again before going on, so that nothing the
// pipeline was told is written is lost.
func TestReconnectWritesOpenAgain(t *testing.T) {
	d := newFakeDB(map[string][]string{"t": {"id"}})
	s := fakeSink(t, d, nil)
	tx := inTx("x", rec("t", fitsync.OpInsert, 1, "a"), rec("t", fitsync.OpInsert, 2, "b"), rec("t", fitsync.OpDelete, 1, "a"), rec("t", fitsync.OpInsert, 3, "c"))
	if err := s.Write(ctxT(t), tx[:2]); err != nil {
		t.Fatal(err)
	}
	broke := false
	d.fail = func(q string, args []any) error {
		if strings.HasPrefix(q, "DELETE") && !broke {
			broke = true
			return errBroken
		}
		return nil
	}
	if err := s.Write(ctxT(t), tx[2:]); !errors.Is(err, errBroken) {
		t.Fatalf("%v", err)
	}
	want(t, d, "t")
	if err := s.Write(ctxT(t), tx[2:]); err != nil {
		t.Fatal(err)
	}
	want(t, d, "t", "id=2,v=b", "id=3,v=c")
	if d.dials != 2 {
		t.Errorf("%d dials", d.dials)
	}
}

// TestBadRecord: a row the server refuses for good in a multi-row INSERT
// fails that record alone, permanently; those before it are committed.
func TestBadRecord(t *testing.T) {
	d := newFakeDB(map[string][]string{"t": {"id"}})
	s := fakeSink(t, d, nil)
	recs := inTx("x", rec("t", fitsync.OpInsert, 1, "a"), rec("t", fitsync.OpInsert, 2, "b"), rec("t", fitsync.OpInsert, 3, "BAD"), rec("t", fitsync.OpInsert, 4, "d"))
	err := s.Write(ctxT(t), recs)
	var be *fitsync.BatchError
	if !errors.As(err, &be) || be.Index != 2 || !fitsync.IsPermanent(err) {
		t.Fatalf("%v", err)
	}
	want(t, d, "t", "id=1,v=a", "id=2,v=b")
	if err := s.Write(ctxT(t), recs[3:]); err != nil {
		t.Fatal(err)
	}
	want(t, d, "t", "id=1,v=a", "id=2,v=b", "id=4,v=d")
	// Alone in its statement, and with nothing open.
	err = s.Write(ctxT(t), []*fitsync.Record{rec("t", fitsync.OpInsert, 5, "BAD")})
	if !errors.As(err, &be) || be.Index != 0 || !fitsync.IsPermanent(err) {
		t.Fatalf("%v", err)
	}
	// A table that is not there.
	err = s.Write(ctxT(t), []*fitsync.Record{rec("none", fitsync.OpInsert, 5, "x")})
	if !errors.As(err, &be) || be.Index != 0 || !fitsync.IsPermanent(err) {
		t.Fatalf("%v", err)
	}
}

// TestRefusedThenPasses: a statement refused whose rows all go in one at
// a time is written.
func TestRefusedThenPasses(t *testing.T) {
	d := newFakeDB(map[string][]string{"t": {"id"}})
	s := fakeSink(t, d, nil)
	once := false
	d.fail = func(q string, args []any) error {
		if strings.HasPrefix(q, "INSERT") && len(args) > 2 && !once {
			once = true
			return &mysqlwire.Error{Code: 1406, State: "22001", Message: "too long"}
		}
		return nil
	}
	if err := s.Write(ctxT(t), []*fitsync.Record{rec("t", fitsync.OpInsert, 1, "a"), rec("t", fitsync.OpInsert, 2, "b")}); err != nil {
		t.Fatal(err)
	}
	want(t, d, "t", "id=1,v=a", "id=2,v=b")
	// A passing error while finding the record at fault.
	d.fail = func(q string, args []any) error {
		if strings.HasPrefix(q, "INSERT") {
			if len(args) > 2 {
				return &mysqlwire.Error{Code: 1406, State: "22001", Message: "too long"}
			}
			return errBroken
		}
		return nil
	}
	if err := s.Write(ctxT(t), []*fitsync.Record{rec("t", fitsync.OpInsert, 3, "a"), rec("t", fitsync.OpInsert, 4, "b")}); !errors.Is(err, errBroken) {
		t.Fatal(err)
	}
}

// TestRecordErrors: records the sink cannot write fail for good, at their
// index, those before them committed.
func TestRecordErrors(t *testing.T) {
	cases := []struct {
		r    *fitsync.Record
		want string
	}{
		{&fitsync.Record{Namespace: "t", Op: fitsync.OpDelete, Key: fitsync.Int(1)}, "not a map"},
		{&fitsync.Record{Namespace: "t", Op: fitsync.OpDelete, Key: fitsync.Map(fitsync.Field{Name: "id"})}, "is null"},
		{&fitsync.Record{Namespace: "t", Op: fitsync.OpDelete, Key: fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Float(math.NaN())})}, "no column type"},
		{&fitsync.Record{Namespace: "t", Op: fitsync.OpInsert}, "not a map of columns"},
		{&fitsync.Record{Namespace: "t", Op: fitsync.OpInsert, After: fitsync.Map(fitsync.Field{Name: "f", Value: fitsync.Float(math.Inf(1))})}, "column f"},
		{&fitsync.Record{Namespace: "", Op: fitsync.OpInsert, After: row(1, "a")}, "table is empty"},
		{&fitsync.Record{Namespace: "t", Op: 99}, "operation"},
		{&fitsync.Record{Namespace: "t", Op: fitsync.OpUpdate, Key: idKey(2), After: row(2, "b"),
			Before: fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Float(math.NaN())})}, "key as it was"},
	}
	for _, c := range cases {
		d := newFakeDB(map[string][]string{"t": {"id"}})
		s := fakeSink(t, d, nil)
		err := s.Write(ctxT(t), []*fitsync.Record{rec("t", fitsync.OpInsert, 1, "a"), c.r})
		var be *fitsync.BatchError
		if !errors.As(err, &be) || be.Index != 1 || !fitsync.IsPermanent(err) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v, want %q", err, c.want)
		}
		want(t, d, "t", "id=1,v=a")
	}
}

// TestDDL: reported and skipped, or failed for good with ddl stop.
func TestDDL(t *testing.T) {
	ddl := &fitsync.Record{Namespace: "db.t", Op: fitsync.OpDDL, After: fitsync.String("ALTER TABLE t ADD c INT" + strings.Repeat(" ", 300))}
	d := newFakeDB(map[string][]string{"t": {"id"}})
	s := fakeSink(t, d, nil)
	recs := inTx("x", rec("t", fitsync.OpInsert, 1, "a"), ddl)
	if err := s.Write(ctxT(t), recs); err != nil {
		t.Fatal(err)
	}
	want(t, d, "t", "id=1,v=a")
	s = fakeSink(t, d, map[string]any{"ddl": "stop"})
	err := s.Write(ctxT(t), []*fitsync.Record{rec("t", fitsync.OpInsert, 2, "b"), {Namespace: "db.t", Op: fitsync.OpDDL, After: fitsync.String("DROP")}})
	var be *fitsync.BatchError
	if !errors.As(err, &be) || be.Index != 1 || !fitsync.IsPermanent(err) || !strings.Contains(err.Error(), "DROP") {
		t.Fatal(err)
	}
	want(t, d, "t", "id=1,v=a", "id=2,v=b")
}

// TestTiDB: against TiDB, AUTO_RANDOM columns take the source's values,
// and a transaction past max_transaction_bytes is committed in parts.
func TestTiDB(t *testing.T) {
	d := newFakeDB(map[string][]string{"t": {"id"}})
	d.version = "8.0.11-TiDB-v8.5.0"
	s := fakeSink(t, d, nil)
	if err := s.Write(ctxT(t), []*fitsync.Record{rec("t", fitsync.OpInsert, 1, "a")}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(d.statements(), "\n"), "allow_auto_random_explicit_insert") || s.maxTx != DefaultTiDBTransactionBytes {
		t.Errorf("%v, %d", d.statements(), s.maxTx)
	}
	s = fakeSink(t, d, map[string]any{"max_transaction_bytes": 20})
	big := inTx("big", rec("t", fitsync.OpInsert, 2, "bbbbbbbbbbbbbbbbbbbb"), rec("t", fitsync.OpInsert, 3, "c"), rec("t", fitsync.OpInsert, 4, "d"))
	if err := s.Write(ctxT(t), big[:2]); err != nil {
		t.Fatal(err)
	}
	// The first record alone passed the bound: committed in part.
	want(t, d, "t", "id=1,v=a", "id=2,v=bbbbbbbbbbbbbbbbbbbb")
	if err := s.Write(ctxT(t), big[2:]); err != nil {
		t.Fatal(err)
	}
	want(t, d, "t", "id=1,v=a", "id=2,v=bbbbbbbbbbbbbbbbbbbb", "id=3,v=c", "id=4,v=d")
	// With transactions batch, at a transaction's end past the bound.
	s = fakeSink(t, d, map[string]any{"max_transaction_bytes": 30, "transactions": "batch"})
	recs := append(inTx("p", rec("t", fitsync.OpInsert, 5, "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")), inTx("q", rec("t", fitsync.OpInsert, 6, "f"), rec("t", fitsync.OpInsert, 7, "g"))...)
	if err := s.Write(ctxT(t), recs[:2]); err != nil {
		t.Fatal(err)
	}
	if got := len(d.rows("t")); got != 5 {
		t.Errorf("%d rows", got)
	}
}

// TestFailures: errors of the connection, its setup and the transaction's
// statements are passing ones, the batch written again whole.
func TestFailures(t *testing.T) {
	boom := errors.New("boom")
	for _, at := range []string{"dial", "SET time_zone = '+00:00'", "START TRANSACTION", "COMMIT", "INSERT", "DELETE"} {
		t.Run(at, func(t *testing.T) {
			d := newFakeDB(map[string][]string{"t": {"id"}})
			s := fakeSink(t, d, nil)
			d.fail = func(q string, args []any) error {
				if strings.HasPrefix(q, at) {
					return boom
				}
				return nil
			}
			recs := append(inTx("x", rec("t", fitsync.OpInsert, 1, "a"), rec("t", fitsync.OpDelete, 2, "a")), rec("t", fitsync.OpInsert, 3, "c"))
			if err := s.Write(ctxT(t), recs); !errors.Is(err, boom) || fitsync.IsPermanent(err) {
				t.Fatalf("%v", err)
			}
			d.fail = nil
			if err := s.Write(ctxT(t), recs); err != nil {
				t.Fatal(err)
			}
			want(t, d, "t", "id=1,v=a", "id=3,v=c")
		})
	}
}

// TestFailuresOpen: errors while a transaction is open across batches,
// at the commit of a run, at the replay and at a refusal's commit.
func TestFailuresOpen(t *testing.T) {
	boom := errors.New("boom")
	tx := inTx("x", rec("t", fitsync.OpInsert, 1, "a"), rec("t", fitsync.OpInsert, 2, "b"))
	plain := rec("t", fitsync.OpInsert, 3, "c")
	for _, at := range []string{"START TRANSACTION", "INSERT", "COMMIT", "refuse"} {
		t.Run(at, func(t *testing.T) {
			d := newFakeDB(map[string][]string{"t": {"id"}})
			s := fakeSink(t, d, nil)
			if err := s.Write(ctxT(t), tx[:1]); err != nil {
				t.Fatal(err)
			}
			s.conn.(*fakeConn).broken = errBroken
			n := 0
			d.fail = func(q string, args []any) error {
				if at == "refuse" && q == "COMMIT" || strings.HasPrefix(q, at) && n == 0 {
					n++
					return boom
				}
				return nil
			}
			batch := []*fitsync.Record{plain, tx[1]}
			if at == "refuse" {
				batch = []*fitsync.Record{{Namespace: "t", Op: 99}}
			}
			if err := s.Write(ctxT(t), batch); !errors.Is(err, boom) {
				t.Fatalf("%v", err)
			}
			d.fail = nil
			if at == "refuse" {
				return
			}
			if err := s.Write(ctxT(t), batch); err != nil {
				t.Fatal(err)
			}
			want(t, d, "t", "id=1,v=a", "id=2,v=b", "id=3,v=c")
		})
	}
	// A record without a transaction after an open one commits it first,
	// and a commit failing there is a passing error.
	d := newFakeDB(map[string][]string{"t": {"id"}})
	s := fakeSink(t, d, nil)
	if err := s.Write(ctxT(t), tx[:1]); err != nil {
		t.Fatal(err)
	}
	d.fail = func(q string, args []any) error {
		if q == "COMMIT" {
			return boom
		}
		return nil
	}
	if err := s.Write(ctxT(t), []*fitsync.Record{plain}); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	// A refusal's flush failing.
	d.fail = func(q string, args []any) error {
		if strings.HasPrefix(q, "INSERT") {
			return boom
		}
		return nil
	}
	if err := s.Write(ctxT(t), []*fitsync.Record{plain, {Namespace: "t", Op: 99}}); !errors.Is(err, boom) {
		t.Fatal(err)
	}
}

// TestKeys: composite keys delete through row constructors, and a delete
// of many keys goes in several statements.
func TestKeys(t *testing.T) {
	d := newFakeDB(map[string][]string{"db.k": {"a", "b"}, "db.m": {"id"}})
	s := fakeSink(t, d, map[string]any{"table": "db.{$namespace}"})
	key := func(a, b int64) fitsync.Value {
		return fitsync.Map(fitsync.Field{Name: "a", Value: fitsync.Int(a)}, fitsync.Field{Name: "b", Value: fitsync.Int(b)})
	}
	var recs []*fitsync.Record
	for i := range int64(3) {
		recs = append(recs, &fitsync.Record{Namespace: "k", Op: fitsync.OpInsert, Key: key(i, i), After: key(i, i)})
	}
	recs = append(recs, &fitsync.Record{Namespace: "k", Op: fitsync.OpDelete, Key: key(1, 1)}, &fitsync.Record{Namespace: "k", Op: fitsync.OpDelete, Key: key(2, 2)})
	for i := range int64(2500) {
		recs = append(recs, rec("m", fitsync.OpInsert, i, "x"))
	}
	for i := range int64(2499) {
		recs = append(recs, rec("m", fitsync.OpDelete, i, "x"))
	}
	if err := s.Write(ctxT(t), recs); err != nil {
		t.Fatal(err)
	}
	want(t, d, "db.k", "a=0,b=0")
	want(t, d, "db.m", "id=2499,v=x")
	st := strings.Join(d.statements(), "\n")
	if !strings.Contains(st, "DELETE FROM `db`.`k` WHERE (`a`,`b`) IN ((?,?),(?,?))") || strings.Count(st, "DELETE FROM `db`.`m`") != 3 {
		t.Errorf("statements:\n%s", st)
	}
}

func TestArgs(t *testing.T) {
	at := time.Date(2026, 11, 8, 1, 2, 3, 4000, time.FixedZone("x", 3600))
	for _, c := range []struct {
		v    fitsync.Value
		want any
	}{
		{fitsync.Null(), nil},
		{fitsync.Bool(true), true},
		{fitsync.Int(-1), int64(-1)},
		{fitsync.Uint(1), uint64(1)},
		{fitsync.Float(1.5), 1.5},
		{fitsync.Decimal("1.50"), "1.50"},
		{fitsync.String("s"), "s"},
		{fitsync.Bytes([]byte{}), []byte{}},
		{fitsync.Time(at), at},
		{fitsync.Date(2026, 1, 2), time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
		{fitsync.JSON([]byte(`{"a":1}`)), json.RawMessage(`{"a":1}`)},
		{fitsync.Array(fitsync.Int(1), fitsync.String("x")), json.RawMessage(`[1,"x"]`)},
		{fitsync.Map(fitsync.Field{Name: "a", Value: fitsync.Bool(false)}), json.RawMessage(`{"a":false}`)},
	} {
		got, err := arg(c.v)
		if err != nil || fmt.Sprintf("%#v", got) != fmt.Sprintf("%#v", c.want) {
			t.Errorf("%v: %#v, %v", c.v.Kind(), got, err)
		}
		if b, ok := got.([]byte); ok && b == nil {
			t.Error("empty bytes as NULL")
		}
	}
	if argSize("ab") != 3 || argSize([]byte("a")) != 2 || argSize(json.RawMessage("1")) != 2 || argSize(int64(1)) != 8 {
		t.Error("argSize")
	}
	if quoteName("a`b") != "`a``b`" || quoteName("d.t") != "`d`.`t`" {
		t.Error("quoteName")
	}
}

func TestPermanent(t *testing.T) {
	for _, c := range []struct {
		err  error
		want bool
	}{
		{errors.New("x"), false},
		{&mysqlwire.Error{Code: 1213, State: "40001"}, false},
		{&mysqlwire.Error{Code: 1205, State: "HY000"}, false},
		{&mysqlwire.Error{Code: 9007, State: "HY000"}, false},
		{&mysqlwire.Error{Code: 1366, State: "HY000"}, true},
		{&mysqlwire.Error{Code: 1062, State: "23000"}, true},
		{&mysqlwire.Error{Code: 1146, State: "42S02"}, true},
		{&mysqlwire.Error{Code: 1264, State: "22003"}, true},
		{&mysqlwire.Error{Code: 1040, State: "08004"}, false},
		{fmt.Errorf("w: %w", &mysqlwire.Error{Code: 1048, State: "23000"}), true},
	} {
		if got := permanent(c.err); got != c.want {
			t.Errorf("%v: %v", c.err, got)
		}
	}
}

// TestDialAndTest goes through the client to a fake server: the
// connection test, and a write's connection and its setup.
func TestDialAndTest(t *testing.T) {
	addr := fakeServer(t, "8.4.3")
	s := loadSink(t, map[string]any{"username": "u", "address": addr, "timeout": "30s"})
	if err := s.Test(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	if err := s.connect(ctxT(t)); err != nil || s.conn.ServerVersion() != "8.4.3" {
		t.Fatal(err)
	}
	if err := s.connect(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	// Rows go through the client's text inserts, which ask the server's
	// max_allowed_packet first: this server answers OK, not a row.
	if _, err := s.conn.InsertRows(ctxT(t), "INSERT INTO t (a) VALUES", [][]any{{1}}, ""); err == nil || !strings.Contains(err.Error(), "max_allowed_packet") {
		t.Fatal(err)
	}
	s.ParallelWrites()
	// Nothing listening.
	s = loadSink(t, map[string]any{"username": "u", "address": "127.0.0.1:1"})
	if err := s.Test(ctxT(t)); err == nil {
		t.Fatal("no error")
	}
	d := newFakeDB(nil)
	s = fakeSink(t, d, nil)
	d.fail = func(q string, args []any) error {
		if q == "SELECT 1" {
			return errBroken
		}
		return nil
	}
	if err := s.Test(ctxT(t)); !errors.Is(err, errBroken) {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, name string, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestProvision(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "x"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	kder, _ := x509.MarshalECPrivateKey(key)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder})
	ca, cert, certKey := writeFile(t, "ca.pem", certPEM), writeFile(t, "c.pem", certPEM), writeFile(t, "k.pem", keyPEM)
	rk, _ := rsa.GenerateKey(rand.Reader, 1024)
	pub, _ := x509.MarshalPKIXPublicKey(&rk.PublicKey)
	pubFile := writeFile(t, "pub.pem", pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub}))
	junk := writeFile(t, "junk.pem", []byte("junk"))
	t.Setenv("FIT_TEST_MYSQL_PW", "pw")

	s := loadSink(t, map[string]any{"username": "u", "password_env": "FIT_TEST_MYSQL_PW", "database": "db", "tls": true, "ca_file": ca,
		"cert_file": cert, "key_file": certKey, "server_name": "db.internal", "tls_rsa": true, "server_public_key_file": pubFile})
	if s.cfg.Password != "pw" || s.cfg.TLS == nil || s.cfg.TLS.RootCAs == nil || len(s.cfg.TLS.Certificates) != 1 ||
		s.cfg.TLS.ServerName != "db.internal" || len(s.cfg.TLS.CipherSuites) == 0 || s.cfg.ServerPublicKey == nil || s.cfg.Addr != DefaultAddress {
		t.Errorf("%+v", s.cfg)
	}
	for _, c := range []struct {
		params map[string]any
		want   string
	}{
		{map[string]any{}, "username"},
		{map[string]any{"username": "u", "address": "nohost"}, "not a host and port"},
		{map[string]any{"username": "u", "password_env": "a-b"}, "not an environment variable"},
		{map[string]any{"username": "u", "password_env": "FIT_TEST_MYSQL_UNSET"}, "not set"},
		{map[string]any{"username": "u", "ca_file": ca}, "needs tls"},
		{map[string]any{"username": "u", "tls_rsa": true}, "needs tls"},
		{map[string]any{"username": "u", "tls": true, "ca_file": "/none"}, "ca_file"},
		{map[string]any{"username": "u", "tls": true, "ca_file": junk}, "no PEM"},
		{map[string]any{"username": "u", "tls": true, "cert_file": cert}, "go together"},
		{map[string]any{"username": "u", "tls": true, "cert_file": junk, "key_file": junk}, "cert_file"},
		{map[string]any{"username": "u", "server_public_key_file": junk}, "server_public_key_file"},
		{map[string]any{"username": "u", "table": "{db}.t"}, "brace"},
		{map[string]any{"username": "u", "transactions": "none"}, "transactions"},
	} {
		_, err := load(t, c.params)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v, want %q", c.params, err, c.want)
		}
	}
}
