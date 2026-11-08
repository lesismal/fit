package syncpgsink

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	fitsync "github.com/lesismal/fit/sync"
)

const ckName = `"_fit_sync_checkpoints"`

// positioned gives each record a position, its index after base.
func positioned(base int, recs ...*fitsync.Record) []*fitsync.Record {
	for i, r := range recs {
		r.Position = fitsync.Position(fmt.Sprintf(`{"n":%d}`, base+i))
	}
	return recs
}

// keptRow returns the checkpoint table's row of pipeline p, lineage and
// position.
func keptRow(f *fake, table string) string {
	r, ok := f.table(table)[`"p"`]
	if !ok {
		return ""
	}
	return *r[`"lineage"`] + " " + *r[`"position"`]
}

// TestCheckpointInTransaction: with checkpoint sink, each commit writes the
// position of its last record that ends a source transaction, a held
// transaction's not until its last; ReadCheckpoint reads it back and drops
// what was held.
func TestCheckpointInTransaction(t *testing.T) {
	f := newFake(t)
	s := sinkOf(t, f, map[string]any{"checkpoint": "sink", "copy": "never"})
	if !s.KeepsCheckpoint() || !s.source {
		t.Fatal("not keeping, or not by source transactions")
	}
	pos, lin, err := s.ReadCheckpoint(ctx(t), "p", "L1")
	if err != nil || pos != nil || lin != "" {
		t.Fatalf("%s %s %v", pos, lin, err)
	}
	rows := func() string { return dump(f.table(`"t"`), "id", "v") }
	batch := positioned(0, txRec(1, "a", "x", true, false), txRec(2, "b", "x", false, true), txRec(3, "c", "y", true, false))
	if err := s.Write(ctx(t), batch); err != nil {
		t.Fatal(err)
	}
	if rows() != "[1|a 2|b]" || keptRow(f, ckName) != `L1 {"n":1}` {
		t.Fatal(rows(), keptRow(f, ckName))
	}
	// A new run drops y's first record, held.
	pos, lin, err = s.ReadCheckpoint(ctx(t), "p", "L2")
	if err != nil || string(pos) != `{"n":1}` || lin != "L1" {
		t.Fatalf("%s %s %v", pos, lin, err)
	}
	if err := s.Write(ctx(t), positioned(3, txRec(4, "d", "y", false, true))); err != nil {
		t.Fatal(err)
	}
	if rows() != "[1|a 2|b 4|d]" || keptRow(f, ckName) != `L2 {"n":3}` {
		t.Fatal(rows(), keptRow(f, ckName))
	}
	// A DDL alone commits nothing; its position goes with the next.
	ddl := positioned(4, &fitsync.Record{Namespace: "t", Op: fitsync.OpDDL})
	if err := s.Write(ctx(t), ddl); err != nil || keptRow(f, ckName) != `L2 {"n":3}` {
		t.Fatal(err, keptRow(f, ckName))
	}
}

// TestCheckpointRefused: a source transaction refused leaves the
// checkpoint at the one before it, written with it.
func TestCheckpointRefused(t *testing.T) {
	f := newFake(t)
	s := sinkOf(t, f, map[string]any{"checkpoint": "sink", "checkpoint_table": "ops.ck", "copy": "never"})
	if _, _, err := s.ReadCheckpoint(ctx(t), "p", "L"); err != nil {
		t.Fatal(err)
	}
	f.setInject(bad("22001"))
	batch := positioned(0, txRec(1, "a", "x", true, true), txRec(2, "b", "y", true, false), txRec(3, "bad", "y", false, true), txRec(4, "d", "", false, false))
	var be *fitsync.BatchError
	if err := s.Write(ctx(t), batch); !errors.As(err, &be) || be.Index != 2 {
		t.Fatalf("%v", err)
	}
	if got := keptRow(f, `"ops"."ck"`); got != `L {"n":0}` {
		t.Fatal(got)
	}
	// Written one at a time after all, every record committed.
	f.setInject(nil)
	s.drop = ""
	twiceOnce := false
	f.setInject(func(sql string, args []*string) string {
		if strings.HasPrefix(sql, `INSERT INTO "t"`) && len(args) > 2 && !twiceOnce {
			twiceOnce = true
			return "21000"
		}
		return ""
	})
	if err := s.Write(ctx(t), positioned(5, txRec(5, "e", "z", true, false), txRec(6, "f", "z", false, true))); err != nil {
		t.Fatal(err)
	}
	if got := keptRow(f, `"ops"."ck"`); got != `L {"n":6}` {
		t.Fatal(got)
	}
}

// TestCheckpointFailures: a failure reading the checkpoint, or writing it
// at a commit, is the call's error, nothing committed, and the next call
// goes on.
func TestCheckpointFailures(t *testing.T) {
	for _, stage := range []string{"CREATE", "SELECT", "INSERT INTO " + ckName, "one by one"} {
		t.Run(stage, func(t *testing.T) {
			f := newFake(t)
			s := sinkOf(t, f, map[string]any{"checkpoint": "sink", "copy": "never"})
			n := 0
			f.setInject(func(sql string, args []*string) string {
				switch {
				case stage == "one by one" && strings.HasPrefix(sql, `INSERT INTO "t"`) && len(args) > 2:
					return "21000"
				case stage == "one by one" && strings.HasPrefix(sql, "INSERT INTO "+ckName):
					if n++; n == 1 {
						return "57P01"
					}
				case stage != "one by one" && strings.HasPrefix(sql, stage):
					return "57P01"
				}
				return ""
			})
			recs := positioned(0, txRec(1, "a", "", false, false), txRec(2, "b", "", false, false))
			_, _, err := s.ReadCheckpoint(ctx(t), "p", "L")
			if err == nil {
				err = s.Write(ctx(t), recs)
			}
			if err == nil || dump(f.table(`"t"`), "id", "v") != "[]" {
				t.Fatalf("%v %s", err, dump(f.table(`"t"`), "id", "v"))
			}
			if stage != "one by one" {
				f.setInject(nil)
			}
			if _, _, err := s.ReadCheckpoint(ctx(t), "p", "L"); err != nil {
				t.Fatal(err)
			}
			if err := s.Write(ctx(t), recs); err != nil {
				t.Fatal(err)
			}
			if got := keptRow(f, ckName); got != `L {"n":1}` {
				t.Fatal(got)
			}
		})
	}
	// A refusal one at a time whose checkpoint fails.
	f := newFake(t)
	s := sinkOf(t, f, map[string]any{"checkpoint": "sink", "copy": "never"})
	if _, _, err := s.ReadCheckpoint(ctx(t), "p", "L"); err != nil {
		t.Fatal(err)
	}
	f.setInject(func(sql string, args []*string) string {
		if strings.HasPrefix(sql, "INSERT INTO "+ckName) {
			return "57P01"
		}
		return bad("22001")(sql, args)
	})
	if err := s.Write(ctx(t), positioned(0, txRec(1, "a", "", false, false), txRec(2, "bad", "", false, false))); err == nil || fitsync.IsPermanent(err) {
		t.Fatalf("%v", err)
	}
	// No server.
	s, err := load(t, map[string]any{"address": "127.0.0.1:1", "user": "fit", "sslmode": "disable", "checkpoint": "sink"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ReadCheckpoint(ctx(t), "p", "L"); err == nil {
		t.Fatal("no error")
	}
}

// TestCheckpointOff: checkpoint file reads and writes nothing; batch cannot
// go with checkpoint sink.
func TestCheckpointOff(t *testing.T) {
	f := newFake(t)
	s := sinkOf(t, f, map[string]any{"copy": "never"})
	if s.KeepsCheckpoint() {
		t.Fatal("keeps")
	}
	if pos, lin, err := s.ReadCheckpoint(ctx(t), "p", "L"); pos != nil || lin != "" || err != nil || len(f.statements()) != 0 {
		t.Fatalf("%s %s %v", pos, lin, err)
	}
	if err := s.Write(ctx(t), positioned(0, txRec(1, "a", "", false, false))); err != nil || len(f.table(ckName)) != 0 {
		t.Fatal(err)
	}
	if _, err := load(t, map[string]any{"user": "u", "checkpoint": "sink", "transactions": "batch"}); err == nil || !strings.Contains(err.Error(), "transactions") {
		t.Fatalf("%v", err)
	}
}
