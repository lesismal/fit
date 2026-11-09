package syncmssql

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	mssqlwire "github.com/lesismal/fit/sync/mssql/wire"
)

func provisionSink(t *testing.T, params map[string]any) (*Sink, error) {
	t.Helper()
	p := map[string]any{"username": "sa", "encrypt": "off"}
	for k, v := range params {
		p[k] = v
		if v == "default" {
			delete(p, k)
		}
	}
	b, _ := json.Marshal(p)
	v, err := config.ParseValue(b, "params")
	if err != nil {
		t.Fatal(err)
	}
	m, err := module.Load(fitsync.Sinks, config.ModuleRef{Module: "mssql", Params: v}, nil)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { module.Cleanup(m) })
	return m.(*Sink), nil
}

func newSink(t *testing.T, f *sinkFake, params map[string]any) *Sink {
	t.Helper()
	s, err := provisionSink(t, params)
	if err != nil {
		t.Fatal(err)
	}
	s.dial = f.dial
	return s
}

// chg is the n-th change of id: op with value v, at position n, in
// transaction tx when it is not empty (last ending it).
func chg(n int, op fitsync.Op, id int64, v string, tx string, last bool) *fitsync.Record {
	key := fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(id)})
	row := fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(id)}, fitsync.Field{Name: "v", Value: fitsync.String(v)})
	r := &fitsync.Record{Namespace: "dbo.t", Op: op, Key: key, Position: fitsync.Position(fmt.Sprintf(`{"n":%d}`, n))}
	if tx != "" {
		r.Tx = fitsync.Tx{ID: tx, Last: last}
	}
	if op == fitsync.OpDelete {
		r.Before = row
	} else {
		r.After = row
	}
	return r
}

// load is inserts of 0..29 without transactions, then transactions of
// updates, deletes and inserts again; want is what it leaves.
func load() ([]*fitsync.Record, []string) {
	var recs []*fitsync.Record
	want := map[int64]string{}
	n := 0
	add := func(op fitsync.Op, id int64, v, tx string, last bool) {
		n++
		recs = append(recs, chg(n, op, id, v, tx, last))
		if op == fitsync.OpDelete {
			delete(want, id)
		} else {
			want[id] = v
		}
	}
	for i := range int64(30) {
		add(fitsync.OpInsert, i, "v1", "", false)
	}
	for i := range int64(30) {
		tx := fmt.Sprint("tx", i)
		switch {
		case i%5 == 0:
			add(fitsync.OpDelete, i, "", tx, false)
			add(fitsync.OpInsert, i, "v3", tx, true)
		case i%3 == 0:
			add(fitsync.OpDelete, i, "", tx, true)
		default:
			add(fitsync.OpUpdate, i, "v2", tx, false)
			add(fitsync.OpUpdate, i, "v2b", tx, true)
		}
	}
	var out []string
	for id, v := range want {
		out = append(out, fmt.Sprintf("[id]:%d,[v]:%s", id, v))
	}
	sort.Strings(out)
	return recs, out
}

func writeAll(t *testing.T, s *Sink, recs []*fitsync.Record, size int) {
	t.Helper()
	for i := 0; i < len(recs); i += size {
		if err := s.Write(context.Background(), recs[i:min(i+size, len(recs))]); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSinkConverges(t *testing.T) {
	for _, tc := range []map[string]any{
		{},
		{"upsert": "update_insert"},
		{"bulk_rows": 5},
		{"bulk_rows": 5, "upsert": "update_insert"},
		{"transactions": "batch"},
		{"identity_insert": true},
	} {
		t.Run(fmt.Sprint(tc), func(t *testing.T) {
			f := newSinkFake()
			s := newSink(t, f, tc)
			recs, want := load()
			writeAll(t, s, recs, 7)
			writeAll(t, s, recs[len(recs)/2:], 11)
			writeAll(t, s, recs, 50)
			if got := f.rows("[dbo].[t]"); strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Fatalf("%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
			if err := s.Test(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestSinkTransactions: a source transaction is never committed in part,
// and one left open at a batch's end is written again on a new connection
// after the old one broke.
func TestSinkTransactions(t *testing.T) {
	f := newSinkFake()
	s := newSink(t, f, nil)
	recs := []*fitsync.Record{chg(1, fitsync.OpInsert, 1, "a", "t1", false), chg(2, fitsync.OpInsert, 2, "b", "t1", false)}
	if err := s.Write(context.Background(), recs); err != nil {
		t.Fatal(err)
	}
	if !f.inTx || len(f.rows("[dbo].[t]")) != 2 {
		t.Fatal("the transaction is not open with its rows")
	}
	s.conn.(*sinkFakeConn).broken = errors.New("gone") // the connection broke between batches
	if err := s.Write(context.Background(), []*fitsync.Record{chg(3, fitsync.OpInsert, 3, "c", "t1", true)}); err != nil {
		t.Fatal(err)
	}
	if f.inTx || len(f.rows("[dbo].[t]")) != 3 || f.dials != 2 {
		t.Fatalf("%v %v %d", f.inTx, f.rows("[dbo].[t]"), f.dials)
	}
	// A broken connection mid-batch fails it; the next Write connects
	// again and writes what was acknowledged.
	f.broken = "MERGE"
	if err := s.Write(context.Background(), []*fitsync.Record{chg(4, fitsync.OpInsert, 4, "d", "t2", false)}); err == nil {
		t.Fatal("a broken connection wrote")
	}
	if err := s.Write(context.Background(), []*fitsync.Record{chg(4, fitsync.OpInsert, 4, "d", "t2", true)}); err != nil {
		t.Fatal(err)
	}
	if len(f.rows("[dbo].[t]")) != 4 {
		t.Fatal(f.rows("[dbo].[t]"))
	}
	// The same key twice in a run is written as two statements.
	f.log = nil
	if err := s.Write(context.Background(), []*fitsync.Record{chg(5, fitsync.OpUpdate, 4, "x", "", false), chg(6, fitsync.OpUpdate, 4, "y", "", false)}); err != nil {
		t.Fatal(err)
	}
	merges := 0
	for _, l := range f.log {
		if strings.HasPrefix(l, "MERGE") {
			merges++
		}
	}
	if merges != 2 || !strings.Contains(strings.Join(f.rows("[dbo].[t]"), " "), "[v]:y") {
		t.Fatal(merges, f.rows("[dbo].[t]"))
	}
}

// TestSinkCheckpoint keeps the checkpoint in the sink's transaction, and
// reads it back.
func TestSinkCheckpoint(t *testing.T) {
	f := newSinkFake()
	s := newSink(t, f, map[string]any{"checkpoint": "sink", "checkpoint_table": "fit.ck"})
	pos, lin, err := s.ReadCheckpoint(context.Background(), "p", "L1")
	if err != nil || pos != nil || lin != "" {
		t.Fatal(pos, lin, err)
	}
	recs := []*fitsync.Record{chg(1, fitsync.OpInsert, 1, "a", "t1", false), chg(2, fitsync.OpInsert, 2, "b", "t1", true), chg(3, fitsync.OpInsert, 3, "c", "t2", false)}
	if err := s.Write(context.Background(), recs); err != nil {
		t.Fatal(err)
	}
	pos, lin, err = s.ReadCheckpoint(context.Background(), "p", "L2")
	if err != nil || string(pos) != `{"n":2}` || lin != "L1" {
		t.Fatal(string(pos), lin, err)
	}
	// The open transaction was dropped with the connection.
	if len(f.rows("[dbo].[t]")) != 2 {
		t.Fatal(f.rows("[dbo].[t]"))
	}
	f.failOn("SELECT [lineage]", errors.New("no read"), 1)
	if _, _, err := s.ReadCheckpoint(context.Background(), "p", "L2"); err == nil {
		t.Fatal("a failed read passed")
	}
	f.failOn("IF OBJECT_ID(@p1", errors.New("no create"), 1)
	if _, _, err := s.ReadCheckpoint(context.Background(), "p", "L2"); err == nil {
		t.Fatal("a failed create passed")
	}
	f.dialErr = errors.New("no dial")
	if _, _, err := s.ReadCheckpoint(context.Background(), "p", "L2"); err == nil {
		t.Fatal("a failed dial passed")
	}
	plain := newSink(t, newSinkFake(), nil)
	if pos, _, err := plain.ReadCheckpoint(context.Background(), "p", "L"); pos != nil || err != nil {
		t.Fatal(pos, err)
	}
}

// TestSinkBadRecord finds the record the server refuses, the records
// before it committed, whether the error dooms the transaction or not, in
// statements and in a bulk load.
func TestSinkBadRecord(t *testing.T) {
	for _, doom := range []bool{false, true} {
		for _, params := range []map[string]any{{}, {"bulk_rows": 3}, {"upsert": "update_insert"}} {
			t.Run(fmt.Sprint(doom, params), func(t *testing.T) {
				f := newSinkFake()
				f.doom = doom
				s := newSink(t, f, params)
				var recs []*fitsync.Record
				for i := range int64(8) {
					v := fmt.Sprint("v", i)
					if i == 5 {
						v = "bad"
					}
					recs = append(recs, chg(int(i)+1, fitsync.OpInsert, i, v, "", false))
				}
				err := s.Write(context.Background(), recs)
				var be *fitsync.BatchError
				if !errors.As(err, &be) || be.Index != 5 || !fitsync.IsPermanent(err) {
					t.Fatal(err)
				}
				if got := f.rows("[dbo].[t]"); len(got) != 5 || f.inTx {
					t.Fatal(got, f.inTx)
				}
			})
		}
	}
	// A value the bulk load cannot send is found the same way.
	f := newSinkFake()
	s := newSink(t, f, map[string]any{"bulk_rows": 2})
	recs := []*fitsync.Record{chg(1, fitsync.OpInsert, 1, "a", "", false), chg(2, fitsync.OpInsert, 2, "toolong", "", false), chg(3, fitsync.OpInsert, 3, "c", "", false)}
	f.failOn("v]:toolong", nil, 0)
	err := s.Write(context.Background(), recs)
	var be *fitsync.BatchError
	// The statements take "toolong"; the bulk load did not, and the
	// statements wrote all three.
	if err != nil || len(f.rows("[dbo].[t]")) != 3 {
		t.Fatal(err, f.rows("[dbo].[t]"))
	}
	// A bad key of a delete.
	del := chg(4, fitsync.OpDelete, 0, "", "", false)
	del.Key = fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.String("bad")})
	if err := s.Write(context.Background(), []*fitsync.Record{del}); !errors.As(err, &be) || be.Index != 0 {
		t.Fatal(err)
	}
}

// TestSinkErrors: what fails a batch to be written again, and what is
// refused at once.
func TestSinkErrors(t *testing.T) {
	ctx := context.Background()
	transient := &mssqlwire.Error{Number: 1205, Class: 13, Message: "deadlock"}
	for _, key := range []string{"BEGIN", "MERGE", "COMMIT", "SET XACT_ABORT", "SELECT XACT_STATE"} {
		t.Run(key, func(t *testing.T) {
			f := newSinkFake()
			s := newSink(t, f, nil)
			f.failOn(key, transient, 1)
			if key == "SELECT XACT_STATE" {
				f.failOn("MERGE", &mssqlwire.Error{Number: 245, Message: "bad"}, 1)
			}
			if err := s.Write(ctx, []*fitsync.Record{chg(1, fitsync.OpInsert, 1, "a", "", false)}); err == nil || fitsync.IsPermanent(err) {
				t.Fatal(err)
			}
			if err := s.Write(ctx, []*fitsync.Record{chg(1, fitsync.OpInsert, 1, "a", "", false)}); err != nil {
				t.Fatal(err)
			}
		})
	}
	f := newSinkFake()
	s := newSink(t, f, map[string]any{"ddl": "stop", "table": "x.{namespace}"})
	var be *fitsync.BatchError
	for _, r := range []*fitsync.Record{
		{Op: fitsync.OpDDL, Namespace: "dbo.t", After: fitsync.String(strings.Repeat("x", 300))},
		{Op: fitsync.Op(42), Namespace: "dbo.t"},
		{Op: fitsync.OpInsert, Namespace: "a.b.c", Key: fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(1)})},
		{Op: fitsync.OpInsert, Namespace: "t", Key: fitsync.Int(1)},
		{Op: fitsync.OpInsert, Namespace: "t", Key: fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(1)}), After: fitsync.Int(1)},
		{Op: fitsync.OpInsert, Namespace: "t", Key: fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(1)}), After: fitsync.Map(fitsync.Field{Name: "v", Value: fitsync.Int(1)})},
		{Op: fitsync.OpInsert, Namespace: "t", Key: fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(1)}), After: fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Null()})},
		{Op: fitsync.OpInsert, Namespace: "t", Key: fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(1)}), After: fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Float(math.NaN())})},
		{Op: fitsync.OpDelete, Namespace: "t", Key: fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Null()})},
		{Op: fitsync.OpDelete, Namespace: "t", Key: fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Float(math.Inf(1))})},
		{Op: fitsync.OpUpdate, Namespace: "t", Key: fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(1)}),
			After: fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(1)}), Before: fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Float(math.NaN())})},
	} {
		if err := s.Write(ctx, []*fitsync.Record{r}); !errors.As(err, &be) || be.Index != 0 || !fitsync.IsPermanent(err) {
			t.Errorf("%+v: %v", r, err)
		}
	}
	// ddl report logs and goes on.
	rep := newSink(t, f, nil)
	if err := rep.Write(ctx, []*fitsync.Record{{Op: fitsync.OpDDL, Namespace: "dbo.t", After: fitsync.String("ALTER")}}); err != nil {
		t.Fatal(err)
	}
	f.dialErr = errors.New("no dial")
	rep.conn.(*sinkFakeConn).broken = errors.New("gone")
	if err := rep.Write(ctx, []*fitsync.Record{chg(1, fitsync.OpInsert, 1, "a", "", false)}); err == nil {
		t.Fatal("wrote without a connection")
	}
	if err := rep.Test(ctx); err == nil {
		t.Fatal("tested without a connection")
	}
	// A commit that fails while refusing a record.
	g := newSinkFake()
	s2 := newSink(t, g, nil)
	g.failOn("COMMIT", transient, 1)
	bad := []*fitsync.Record{chg(1, fitsync.OpInsert, 1, "a", "", false), chg(2, fitsync.OpInsert, 2, "bad", "", false)}
	if err := s2.Write(ctx, bad); err == nil || errors.As(err, &be) {
		t.Fatal(err)
	}
	// A transient error while finding the bad record, and while making
	// the transaction whole again.
	for _, key := range []string{"@p1, @p2)) AS", "SELECT XACT_STATE"} {
		h := newSinkFake()
		h.doom = true
		s3 := newSink(t, h, nil)
		h.failOn(key, transient, -1)
		recs := []*fitsync.Record{chg(1, fitsync.OpInsert, 1, "v1", "", false), chg(2, fitsync.OpInsert, 2, "bad", "", false)}
		if err := s3.Write(ctx, recs); err == nil || errors.As(err, &be) {
			t.Fatal(key, err)
		}
	}
	// A refused record while it is written again on a new connection.
	k := newSinkFake()
	s4 := newSink(t, k, nil)
	if err := s4.Write(ctx, []*fitsync.Record{chg(1, fitsync.OpInsert, 1, "a", "t", false)}); err != nil {
		t.Fatal(err)
	}
	s4.conn.(*sinkFakeConn).broken = errors.New("gone")
	k.failOn("MERGE", transient, 1)
	if err := s4.Write(ctx, []*fitsync.Record{chg(2, fitsync.OpInsert, 2, "b", "t", true)}); err == nil {
		t.Fatal("a failed replay wrote")
	}
	// A bulk load that cannot start, or end.
	for _, key := range []string{"BULK", "IF OBJECT_ID(N'tempdb", "BULKCLOSE"} {
		b := newSinkFake()
		s5 := newSink(t, b, map[string]any{"bulk_rows": 1})
		if key == "BULKCLOSE" {
			b.broken = key
		} else {
			b.failOn(key, transient, 1)
		}
		if err := s5.Write(ctx, []*fitsync.Record{chg(1, fitsync.OpInsert, 1, "a", "", false)}); err == nil {
			t.Fatal(key, "wrote")
		}
	}
}

// TestSinkTypedNulls sends a NULL as its column's type, read once.
func TestSinkTypedNulls(t *testing.T) {
	f := newSinkFake()
	b, _ := mssqlwire.ParseType("varbinary(16)", mssqlwire.Collation{})
	b.Name = "B"
	id, _ := mssqlwire.ParseType("bigint", mssqlwire.Collation{})
	id.Name = "id"
	f.types["[dbo].[t]"] = []mssqlwire.Column{id, b}
	s := newSink(t, f, nil)
	r := func(n int) *fitsync.Record {
		return &fitsync.Record{Namespace: "dbo.t", Op: fitsync.OpInsert, Key: fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(int64(n))}),
			After: fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(int64(n))}, fitsync.Field{Name: "b", Value: fitsync.Null()}, fitsync.Field{Name: "x", Value: fitsync.Null()})}
	}
	if err := s.Write(context.Background(), []*fitsync.Record{r(1), r(2)}); err != nil {
		t.Fatal(err)
	}
	reads := 0
	for _, l := range f.log {
		if strings.HasPrefix(l, "SELECT TOP 0") {
			reads++
		}
	}
	if reads != 1 || len(f.rows("[dbo].[t]")) != 2 {
		t.Fatal(reads, f.rows("[dbo].[t]"))
	}
	if typ, _ := s.typeOf(context.Background(), "[dbo].[t]", "b"); typ != "varbinary(16)" {
		t.Fatal(typ)
	}
	f.failOn("SELECT TOP 0", errors.New("no table"), 1)
	if _, err := s.typeOf(context.Background(), "[dbo].[u]", "b"); err == nil {
		t.Fatal("no error")
	}
	if err := s.Write(context.Background(), []*fitsync.Record{{Namespace: "dbo.v", Op: fitsync.OpInsert, Key: fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(1)}),
		After: fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(1)}, fitsync.Field{Name: "b", Value: fitsync.Null()})}}); err != nil {
		t.Fatal(err)
	}
	f.failOn("SELECT TOP 0", errors.New("no table"), 1)
	if err := s.Write(context.Background(), []*fitsync.Record{{Namespace: "dbo.w", Op: fitsync.OpInsert, Key: fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(1)}),
		After: fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(1)}, fitsync.Field{Name: "b", Value: fitsync.Null()})}}); err == nil {
		t.Fatal("no error")
	}
}

// TestSinkKeyChange deletes the row of a key an update changed.
func TestSinkKeyChange(t *testing.T) {
	f := newSinkFake()
	s := newSink(t, f, nil)
	writeAll(t, s, []*fitsync.Record{chg(1, fitsync.OpInsert, 1, "a", "", false)}, 1)
	up := chg(2, fitsync.OpUpdate, 2, "a", "", false)
	up.Before = fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(1)}, fitsync.Field{Name: "v", Value: fitsync.String("a")})
	writeAll(t, s, []*fitsync.Record{up}, 1)
	if got := f.rows("[dbo].[t]"); len(got) != 1 || got[0] != "[id]:2,[v]:a" {
		t.Fatal(got)
	}
	// A before without the key's column deletes nothing.
	up = chg(3, fitsync.OpUpdate, 3, "b", "", false)
	up.Before = fitsync.Map(fitsync.Field{Name: "v", Value: fitsync.String("a")})
	writeAll(t, s, []*fitsync.Record{up}, 1)
	if got := f.rows("[dbo].[t]"); len(got) != 2 {
		t.Fatal(got)
	}
}

func TestSinkArgs(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("x", 3600))
	for _, tc := range []struct {
		v    fitsync.Value
		want any
	}{
		{fitsync.Null(), nil},
		{fitsync.Bool(true), true},
		{fitsync.Int(-1), int64(-1)},
		{fitsync.Uint(5), int64(5)},
		{fitsync.Uint(math.MaxUint64), mssqlwire.Decimal("18446744073709551615")},
		{fitsync.Float(1.5), 1.5},
		{fitsync.Decimal("1.50"), mssqlwire.Decimal("1.50")},
		{fitsync.String("s"), "s"},
		{fitsync.Time(at), at.UTC()},
		{fitsync.JSON([]byte(`{"a": 1}`)), `{"a":1}`},
		{fitsync.Array(fitsync.Int(1)), `[1]`},
	} {
		got, err := arg(tc.v)
		if err != nil || fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Errorf("%v: %v %v", tc.v, got, err)
		}
	}
	if got, _ := arg(fitsync.Bytes([]byte{1})); string(got.([]byte)) != "\x01" {
		t.Fatal(got)
	}
	d, _ := arg(fitsync.Date(2026, 1, 2))
	if p, ok := d.(mssqlwire.Param); !ok || p.Type != "date" || plain(d) == nil {
		t.Fatal(d)
	}
	if plain("x") != "x" {
		t.Fatal("plain")
	}
	if permanent(errors.New("x")) || !permanent(&rowError{errors.New("x")}) || permanent(&mssqlwire.Error{Number: 1205}) || !permanent(&mssqlwire.Error{Number: 547}) {
		t.Fatal("permanent")
	}
	if (&rowError{errors.New("x")}).Error() != "x" {
		t.Fatal("rowError")
	}
	if q, err := quoteTable("a.b.c"); err != nil || q != "[a].[b].[c]" {
		t.Fatal(q, err)
	}
	if _, err := quoteTable("a..b"); err == nil {
		t.Fatal("an empty part")
	}
}

func TestSinkProvision(t *testing.T) {
	for _, p := range []map[string]any{
		{"table": "{x}"},
		{"checkpoint": "sink", "checkpoint_table": "a.b.c.d"},
		{"address": "nohost"},
	} {
		if _, err := provisionSink(t, p); err == nil {
			t.Errorf("%v: no error", p)
		}
	}
	s, err := provisionSink(t, map[string]any{"encrypt": "default", "table": "dbo.{namespace}_copy"})
	if err != nil || s.cfg.Encrypt != mssqlwire.EncryptOn || s.bulkRows() != DefaultBulkRows {
		t.Fatal(err)
	}
	if q, err := s.tableOf(&fitsync.Record{Namespace: "t"}); err != nil || q != "[dbo].[t_copy]" {
		t.Fatal(q, err)
	}
	if _, err := dialSink(context.Background(), mssqlwire.Config{Addr: "127.0.0.1:1", Timeout: time.Second}); err == nil {
		t.Fatal("dialed nothing")
	}
}

// TestSinkWire goes through the client against a server that answers
// every batch with a result of one int column, n.
func TestSinkWire(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		nc, err := ln.Accept()
		if err != nil {
			return
		}
		defer nc.Close()
		send := func(b []byte) {
			h := []byte{4, 1, 0, 0, 0, 0, 1, 0}
			binary.BigEndian.PutUint16(h[2:], uint16(len(b)+8))
			nc.Write(append(h, b...))
		}
		read := func() byte {
			var h [8]byte
			if _, err := io.ReadFull(nc, h[:]); err != nil {
				return 0
			}
			io.ReadFull(nc, make([]byte, binary.BigEndian.Uint16(h[2:])-8))
			return h[0]
		}
		done := func(status byte, n byte) []byte { return []byte{0xfd, status, 0, 0xc1, 0, n, 0, 0, 0, 0, 0, 0, 0} }
		read()
		send([]byte{0, 0, 11, 0, 6, 1, 0, 17, 0, 1, 0xff, 16, 0, 0, 0, 0, 0, 2})
		read()
		send(append([]byte{0xad, 10, 0, 1, 0x74, 0, 0, 4, 0, 16, 0, 0, 0}, done(0, 0)...))
		result := []byte{0x81, 1, 0, 0, 0, 0, 0, 0, 0, 0x26, 4, 1, 'n', 0, 0xd1, 4, 7, 0, 0, 0}
		result = append(result, done(0x10, 1)...)
		for {
			switch read() {
			case 0:
				return
			default:
				send(result)
			}
		}
	}()
	c, err := dialSink(context.Background(), mssqlwire.Config{Addr: ln.Addr().String(), Encrypt: mssqlwire.EncryptOff, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	b, err := c.Bulk(context.Background(), "t", []string{"n"})
	if err != nil || b == nil {
		t.Fatal(err)
	}
}
