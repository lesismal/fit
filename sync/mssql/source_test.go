package syncmssql

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	fitsync "github.com/lesismal/fit/sync"
	mssqlwire "github.com/lesismal/fit/sync/mssql/wire"
)

func row2(id int64, v string) []any { return []any{id, v} }

// TestSnapshotThenChanges reads a table's snapshot in batches, then its
// changes: inserts, updates with the row before, deletes, a key changed,
// a transaction of several rows, a DDL, an update before with no after.
func TestSnapshotThenChanges(t *testing.T) {
	f := newFake()
	f.tz = 60
	tb := f.table("t", 3)
	in := f.capture(tb, "dbo_t")
	s := newSource(t, f, map[string]any{"tables": []string{"dbo.t"}, "batch_size": 2})
	recs, err := run(t, s, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	checkRecs(t, recs,
		`insert dbo.t {"id":1} {"id":1,"v":"r1"} @snap dbo.t {"map":{"id":{"int":1}}}`,
		`insert dbo.t {"id":2} {"id":2,"v":"r2"} @snap dbo.t {"map":{"id":{"int":2}}}`,
		`insert dbo.t {"id":3} {"id":3,"v":"r3"} @0`)
	from := recs[2].Position
	f.commit(in, int64(cdcInsert), row2(4, "a"))
	l := f.commit(in, int64(cdcUpdateBefore), row2(1, "r1"), in, int64(cdcUpdateAfter), row2(1, "b"),
		in, int64(cdcDelete), row2(2, "r2"),
		in, int64(cdcUpdateBefore), row2(3, "r3"), in, int64(cdcUpdateAfter), row2(30, "r3"))
	f.ddls = append(f.ddls, fakeDDL{lsn: l + 1, table: "t", cmd: "ALTER TABLE t ADD w int"}, fakeDDL{lsn: l + 1, table: "other", cmd: "x"})
	f.commit(in, int64(cdcUpdateBefore), row2(4, "a"))
	f.commit(in, int64(cdcUpdateAfter), row2(4, "c"))
	recs, err = run(t, s, from, true)
	if err != nil {
		t.Fatal(err)
	}
	checkRecs(t, recs,
		`insert dbo.t {"id":4} {"id":4,"v":"a"} @1`,
		`update dbo.t {"id":1} {"id":1,"v":"b"} was {"id":1,"v":"r1"} tx first`,
		`delete dbo.t {"id":2} {"id":2,"v":"r2"} tx`,
		`update dbo.t {"id":30} {"id":30,"v":"r3"} was {"id":3,"v":"r3"} tx last @2`,
		`ddl dbo.t null {"statement":"ALTER TABLE t ADD w int"}`,
		`update dbo.t {"id":4} {"id":4,"v":"a"} @3`,
		`update dbo.t {"id":4} {"id":4,"v":"c"} @4`)
	// The commit time is the server's, an hour east of UTC.
	if got := recs[0].CommitTime; !got.Equal(time.Date(2026, 11, 10, 7, 0, 1, 0, time.UTC)) {
		t.Fatal(got)
	}
	// Nothing after the end.
	recs, err = run(t, s, recs[len(recs)-1].Position, true)
	if err != nil || len(recs) != 0 {
		t.Fatal(recs, err)
	}
	if f.txs[0] != "begin SNAPSHOT" || f.txs[1] != "commit" {
		t.Fatal(f.txs)
	}
}

// TestResumeSnapshot goes on with a snapshot from the checkpoint's key, and
// reads a table without a key in one query.
func TestResumeSnapshot(t *testing.T) {
	f := newFake()
	f.snapIso = false
	a := f.table("a", 4)
	f.capture(a, "dbo_a")
	b := f.table("b", 2)
	b.pk = nil
	f.capture(b, "dbo_b")
	s := newSource(t, f, map[string]any{"batch_size": 3})
	from := position{LSN: zeroLSN, Snapshot: &snapPos{Table: "dbo.a", Key: []byte(`{"map":{"id":{"int":2}}}`)}}.marshal()
	recs, err := run(t, s, from, true)
	if err != nil {
		t.Fatal(err)
	}
	checkRecs(t, recs,
		`insert dbo.a {"id":3} {"id":3,"v":"r3"} @snap dbo.a {"map":{"id":{"int":3}}}`,
		`insert dbo.a {"id":4} {"id":4,"v":"r4"} @snap dbo.a {"map":{"id":{"int":4}}}`,
		`insert dbo.b null {"id":1,"v":"r1"} @snap dbo.b null`,
		`insert dbo.b null {"id":2,"v":"r2"} @0`)
	if len(f.txs) != 0 {
		t.Fatal(f.txs)
	}
	// A key without the key's columns, or that is not JSON.
	for _, key := range []string{`{"x":1}`, `{"int":1}`} {
		from = position{LSN: zeroLSN, Snapshot: &snapPos{Table: "dbo.a", Key: []byte(key)}}.marshal()
		if _, err := run(t, s, from, true); err == nil {
			t.Fatal(key, "no error")
		}
	}
}

// TestWindows reads windows of few transactions and rows: a window of too
// many rows is halved, a lone transaction of too many is read in pages,
// an update's two rows never split.
func TestWindows(t *testing.T) {
	f := newFake()
	tb := f.table("t", 0)
	in := f.capture(tb, "dbo_t")
	other := f.table("o", 0)
	io := f.capture(other, "dbo_o")
	f.commit(in, int64(cdcInsert), row2(1, "a"), io, int64(cdcInsert), row2(1, "x"))
	f.commit(in, int64(cdcInsert), row2(2, "b"))
	big := []any{}
	for i := int64(10); i < 17; i++ {
		big = append(big, in, int64(cdcInsert), row2(i, "big"))
	}
	big = append(big, in, int64(cdcUpdateBefore), row2(1, "a"), in, int64(cdcUpdateAfter), row2(1, "A"), io, int64(cdcDelete), row2(1, "x"))
	f.commit(big...)
	f.commit(in, int64(cdcInsert), row2(3, "c"))
	s := newSource(t, f, map[string]any{"snapshot": "never", "window_transactions": 3, "window_rows": 3})
	recs, err := run(t, s, position{LSN: zeroLSN}.marshal(), true)
	if err != nil {
		t.Fatal(err)
	}
	checkRecs(t, recs,
		`insert dbo.t {"id":1} {"id":1,"v":"a"} tx first`,
		`insert dbo.o {"id":1} {"id":1,"v":"x"} tx last @1`,
		`insert dbo.t {"id":2} {"id":2,"v":"b"} @2`,
		`delete dbo.o {"id":1} {"id":1,"v":"x"} tx first`,
		`insert dbo.t {"id":10} {"id":10,"v":"big"} tx`,
		`insert dbo.t {"id":11} {"id":11,"v":"big"} tx`,
		`insert dbo.t {"id":12} {"id":12,"v":"big"} tx`,
		`insert dbo.t {"id":13} {"id":13,"v":"big"} tx`,
		`insert dbo.t {"id":14} {"id":14,"v":"big"} tx`,
		`insert dbo.t {"id":15} {"id":15,"v":"big"} tx`,
		`insert dbo.t {"id":16} {"id":16,"v":"big"} tx`,
		`update dbo.t {"id":1} {"id":1,"v":"A"} was {"id":1,"v":"a"} tx last @3`,
		`insert dbo.t {"id":3} {"id":3,"v":"c"} @4`)
}

// TestInstances reads a table's two capture instances, the older up to the
// newer's start; a key from the index CDC was enabled with.
func TestInstances(t *testing.T) {
	f := newFake()
	tb := f.table("t", 0)
	tb.pk = nil
	old := f.capture(tb, "dbo_t")
	old.index = "ux"
	f.commit(old, int64(cdcInsert), row2(1, "a"))
	nu := f.capture(tb, "dbo_t2")
	nu.cols = []string{"id"}
	f.commit(old, int64(cdcInsert), row2(2, "b"), nu, int64(cdcInsert), []any{int64(2)})
	s := newSource(t, f, map[string]any{"snapshot": "never"})
	recs, err := run(t, s, position{LSN: zeroLSN}.marshal(), true)
	if err != nil {
		t.Fatal(err)
	}
	checkRecs(t, recs,
		`insert dbo.t {"id":1} {"id":1,"v":"a"} @1`,
		`insert dbo.t {"id":2} {"id":2} @2`)
}

// TestFollow polls until changes come, picks up a table CDC begins to
// capture (reading it whole first), and stops when its context ends.
func TestFollow(t *testing.T) {
	f := newFake()
	a := f.table("a", 1)
	ia := f.capture(a, "dbo_a")
	s := newSource(t, f, map[string]any{"tables": []string{"dbo.*"}, "poll_interval": "1ms"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	step := 0
	s.sleep = func(ctx context.Context, d time.Duration) error {
		step++
		switch step {
		case 1:
			f.commit(ia, int64(cdcInsert), row2(2, "b"))
		case 2:
			b := f.table("b", 2)
			ib := f.capture(b, "dbo_b")
			f.commit(ib, int64(cdcInsert), row2(3, "c"))
		case 3:
			cancel()
		}
		return ctx.Err()
	}
	var recs []*fitsync.Record
	var started fitsync.Position
	err := s.Run(ctx, &fitsync.Run{Emit: func(r *fitsync.Record) error {
		recs = append(recs, r)
		return nil
	}, Start: func(p fitsync.Position) error { started = p; return nil }})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	checkRecs(t, recs,
		`insert dbo.a {"id":1} {"id":1,"v":"r1"} @0`,
		`insert dbo.a {"id":2} {"id":2,"v":"b"} @1`,
		`insert dbo.b {"id":1} {"id":1,"v":"r1"} @snap dbo.b {"map":{"id":{"int":1}}}`,
		`insert dbo.b {"id":2} {"id":2,"v":"r2"} @snap dbo.b {"map":{"id":{"int":2}}}`,
		`insert dbo.b {"id":3} {"id":3,"v":"c"} @2`)
	if !strings.Contains(string(started), `"snapshot":{}`) {
		t.Fatal(string(started))
	}
	// The b's snapshot is not read again from the last position.
	recs, err = run(t, s, recs[len(recs)-1].Position, true)
	if err != nil || len(recs) != 0 {
		t.Fatal(recs, err)
	}
	// Resumed inside an added table's snapshot.
	p := position{LSN: lsn("00000000000000000001"), Since: time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC), Snapshot: &snapPos{Added: true}}
	recs, err = run(t, s, p.marshal(), true)
	if err != nil {
		t.Fatal(err)
	}
	checkRecs(t, recs,
		`insert dbo.b {"id":1} {"id":1,"v":"r1"} @snap dbo.b {"map":{"id":{"int":1}}}`,
		`insert dbo.b {"id":2} {"id":2,"v":"r2"} @snap dbo.b {"map":{"id":{"int":2}}}`,
		`insert dbo.b {"id":3} {"id":3,"v":"c"} @1`,
		`insert dbo.b {"id":3} {"id":3,"v":"c"} @2`)
}

// TestNoSnapshot starts from now with snapshot never, from zeros before
// CDC captured anything; snapshot always reads it again.
func TestNoSnapshot(t *testing.T) {
	f := newFake()
	tb := f.table("t", 2)
	in := f.capture(tb, "dbo_t")
	s := newSource(t, f, map[string]any{"snapshot": "never"})
	recs, err := run(t, s, nil, true)
	if err != nil || len(recs) != 0 {
		t.Fatal(recs, err)
	}
	f.commit(in, int64(cdcInsert), row2(3, "c"))
	s = newSource(t, f, map[string]any{"snapshot": "always"})
	recs, err = run(t, s, position{LSN: zeroLSN}.marshal(), true)
	if err != nil || len(recs) != 3 {
		t.Fatal(recs, err)
	}
}

// TestCleanedUp fails when CDC's cleanup removed changes after the
// position.
func TestCleanedUp(t *testing.T) {
	f := newFake()
	tb := f.table("t", 0)
	f.capture(tb, "dbo_t")
	f.minLSN = 5
	s := newSource(t, f, nil)
	_, err := run(t, s, position{LSN: lsn("00000000000000000002")}.marshal(), true)
	if !errors.Is(err, errNeedSnapshot) {
		t.Fatal(err)
	}
}

// TestChecks fails with the SQL that turns CDC or change tracking on.
func TestChecks(t *testing.T) {
	f := newFake()
	tb := f.table("t", 0)
	f.capture(tb, "dbo_t")
	f.table("plain", 0)
	ctx := context.Background()
	for _, tc := range []struct {
		params map[string]any
		setup  func()
		want   string
	}{
		{map[string]any{}, func() { f.cdc = false }, "sp_cdc_enable_db"},
		{map[string]any{"tables": []string{"dbo.plain", "dbo.t", "dbo.x*"}}, func() { f.cdc = true }, "sp_cdc_enable_table @source_schema = N'dbo', @source_name = N'plain'"},
		{map[string]any{"tables": []string{"dbo.x*"}}, nil, "no table to read"},
		{map[string]any{"mode": "change_tracking"}, nil, "CHANGE_TRACKING = ON"},
		{map[string]any{"mode": "change_tracking", "tables": []string{"dbo.plain"}}, func() { f.ct = true }, "ALTER TABLE [dbo].[plain] ENABLE CHANGE_TRACKING"},
	} {
		if tc.setup != nil {
			tc.setup()
		}
		s := newSource(t, f, tc.params)
		if err := s.Test(ctx); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: %v", tc.params, err)
		}
	}
	s := newSource(t, f, nil)
	if err := s.Test(ctx); err != nil {
		t.Fatal(err)
	}
	f.fail["dial"] = errors.New("refused")
	if err := s.Test(ctx); err == nil {
		t.Fatal("no error")
	}
}

// TestChangeTracking reads a snapshot then change tracking's changes:
// inserts, updates, deletes, a row inserted then deleted before it is read.
func TestChangeTracking(t *testing.T) {
	f := newFake()
	f.ct = true
	tb := f.table("t", 2)
	tb.ct = true
	f.ctVersion = 5
	s := newSource(t, f, map[string]any{"mode": "change_tracking", "batch_size": 2})
	recs, err := run(t, s, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	checkRecs(t, recs,
		`insert dbo.t {"id":1} {"id":1,"v":"r1"} @snap dbo.t {"map":{"id":{"int":1}}}`,
		`insert dbo.t {"id":2} {"id":2,"v":"r2"} @v5`)
	f.ctChange(tb, "I", row2(3, "c"))
	f.ctChange(tb, "U", row2(1, "a"))
	f.ctChange(tb, "D", row2(2, "r2"))
	f.ctChange(tb, "I", row2(4, "d"))
	f.ctChange(tb, "D", row2(4, "d"))
	f.ctChange(tb, "I", row2(5, "e"))
	f.mu.Lock()
	tb.ctChanges[4] = fakeCT{op: "I", v: f.ctVersion}
	f.mu.Unlock()
	recs, err = run(t, s, recs[1].Position, true)
	if err != nil {
		t.Fatal(err)
	}
	checkRecs(t, recs,
		`update dbo.t {"id":1} {"id":1,"v":"a"} tx first`,
		`delete dbo.t {"id":2} null tx`,
		`insert dbo.t {"id":3} {"id":3,"v":"c"} tx`,
		`delete dbo.t {"id":4} null tx`,
		`insert dbo.t {"id":5} {"id":5,"v":"e"} tx last @v11`)
	// A lone change is a transaction of its own.
	f.ctChange(tb, "U", row2(5, "E"))
	recs, err = run(t, s, recs[4].Position, true)
	if err != nil {
		t.Fatal(err)
	}
	checkRecs(t, recs, `update dbo.t {"id":5} {"id":5,"v":"E"} @v12`)
	// Changes cleaned up, a table without a primary key.
	f.ctChange(tb, "U", row2(5, "F"))
	f.minValid = 20
	if _, err := run(t, s, recs[0].Position, true); !errors.Is(err, errNeedSnapshot) {
		t.Fatal(err)
	}
	f.minValid = 0
	tb.pk = nil
	if _, err := run(t, s, recs[0].Position, true); err == nil || !strings.Contains(err.Error(), "no primary key") {
		t.Fatal(err)
	}
}

// TestChangeTrackingFollows polls until a change comes.
func TestChangeTrackingFollows(t *testing.T) {
	f := newFake()
	f.ct = true
	tb := f.table("t", 0)
	tb.ct = true
	s := newSource(t, f, map[string]any{"mode": "change_tracking", "snapshot": "never"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	step := 0
	s.sleep = func(ctx context.Context, d time.Duration) error {
		step++
		if step == 1 {
			f.ctChange(tb, "I", row2(1, "a"))
		} else {
			cancel()
		}
		return ctx.Err()
	}
	var recs []*fitsync.Record
	err := s.Run(ctx, &fitsync.Run{Emit: func(r *fitsync.Record) error { recs = append(recs, r); return nil }})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	checkRecs(t, recs, `insert dbo.t {"id":1} {"id":1,"v":"a"} @v1`)
}

// TestQueryErrors fails the run on each query's error.
func TestQueryErrors(t *testing.T) {
	snapKeys := []string{"dial", "SELECT is_cdc_enabled", "cdc.change_tables ct JOIN", "cdc.captured_columns", "FROM sys.indexes", "fn_cdc_get_max_lsn",
		"BEGIN", "COMMIT", "FROM [dbo].[t]"}
	streamKeys := []string{"MIN(start_lsn)", "lsn_time_mapping WHERE", "fn_cdc_get_all_changes_", "cdc.ddl_history", "SELECT capture_instance"}
	for i, k := range append(snapKeys, streamKeys...) {
		t.Run(k, func(t *testing.T) {
			f := newFake()
			tb := f.table("t", 1)
			in := f.capture(tb, "dbo_t")
			in.index = "ux"
			f.commit(in, int64(cdcInsert), row2(2, "b"))
			f.fail[k] = errors.New("fake: " + k)
			s := newSource(t, f, map[string]any{"poll_interval": "1ms"})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var from fitsync.Position
			if i >= len(snapKeys) {
				from = position{LSN: zeroLSN}.marshal()
			}
			err := s.Run(ctx, &fitsync.Run{From: from, Emit: func(*fitsync.Record) error { return nil }})
			if err == nil || !strings.Contains(err.Error(), "fake: "+k) {
				t.Fatal(err)
			}
		})
	}
	for _, k := range []string{"SELECT is_cdc_enabled", "sys.change_tracking_databases", "sys.change_tracking_tables", "FROM sys.columns", "CHANGE_TRACKING_CURRENT_VERSION",
		"CHANGE_TRACKING_MIN_VALID_VERSION", "CHANGETABLE", "FROM sys.indexes"} {
		t.Run("ct "+k, func(t *testing.T) {
			f := newFake()
			f.ct = true
			tb := f.table("t", 1)
			tb.ct = true
			f.ctChange(tb, "I", row2(2, "b"))
			f.fail[k] = errors.New("fake: " + k)
			s := newSource(t, f, map[string]any{"mode": "change_tracking", "snapshot": "never"})
			_, err := run(t, s, position{}.marshal(), true)
			if err == nil || !strings.Contains(err.Error(), "fake: "+k) {
				t.Fatal(err)
			}
		})
	}
	// An emit that fails stops the run, wherever it is.
	for _, after := range []int{0, 1, 2, 3} {
		f := newFake()
		tb := f.table("t", 2)
		in := f.capture(tb, "dbo_t")
		f.commit(in, int64(cdcInsert), row2(3, "c"), in, int64(cdcInsert), row2(4, "d"))
		f.ddls = append(f.ddls, fakeDDL{lsn: 1, table: "t", cmd: "x"})
		s := newSource(t, f, map[string]any{"window_rows": 1})
		n := 0
		err := s.Run(context.Background(), &fitsync.Run{Once: true, Emit: func(*fitsync.Record) error {
			if n == after {
				return errors.New("stopped")
			}
			n++
			return nil
		}})
		if err == nil {
			t.Fatal(after, "no error")
		}
	}
	// A start that cannot be saved.
	f := newFake()
	f.capture(f.table("t", 0), "dbo_t")
	for _, snap := range []string{"initial", "never"} {
		s := newSource(t, f, map[string]any{"snapshot": snap})
		err := s.Run(context.Background(), &fitsync.Run{Emit: func(*fitsync.Record) error { return nil },
			Start: func(fitsync.Position) error { return errors.New("no disk") }})
		if err == nil {
			t.Fatal(snap, "no error")
		}
	}
	// Checkpoints that do not read.
	s := newSource(t, f, nil)
	for _, from := range []string{`{`, `{"lsn":"zz"}`} {
		if _, err := run(t, s, fitsync.Position(from), true); err == nil {
			t.Fatal(from, "no error")
		}
	}
}

// TestProvision checks the params.
func TestProvision(t *testing.T) {
	t.Setenv("FIT_TEST_MSSQL_PASSWORD", "pw")
	for _, p := range []map[string]any{
		{"tables": []string{"nodot"}},
		{"exclude": []string{"a.[b"}},
		{"keys": map[string]any{"t": []string{"id"}}},
		{"address": "nohost"},
		{"password_env": "1bad"},
		{"password_env": "FIT_TEST_MSSQL_NONE"},
		{"ca_file": "/nonexistent"},
		{"encrypt": "on", "ca_file": "/nonexistent"},
		{"encrypt": "on", "ca_file": "fake_test.go"},
	} {
		if _, err := provision(t, p); err == nil {
			t.Errorf("%v: no error", p)
		}
	}
	s, err := provision(t, map[string]any{"password_env": "FIT_TEST_MSSQL_PASSWORD", "encrypt": "on", "timeout": "5s", "keys": map[string]any{"dbo.t": []string{"a"}},
		"exclude": []string{"dbo.x"}, "tables": []string{"dbo.*"}})
	if err != nil || s.cfg.Password != "pw" || s.cfg.Timeout != 5*time.Second || s.cfg.TLS == nil {
		t.Fatal(err)
	}
	if s.included("dbo.x") || !s.included("dbo.y") || s.included("sales.y") {
		t.Fatal("included")
	}
	if s.batchSize() != DefaultBatchSize || s.pollInterval() != DefaultPollInterval || s.windowRows() != DefaultWindowRows || s.windowTransactions() != DefaultWindowTransactions {
		t.Fatal("defaults")
	}
	s.BatchSize, s.WindowRows, s.WindowTransactions = 1<<30, 5, 6
	if s.batchSize() != MaxBatchSize || s.windowRows() != 5 || s.windowTransactions() != 6 {
		t.Fatal("bounds")
	}
	if _, err := dialWire(context.Background(), mssqlwire.Config{Addr: "127.0.0.1:1", Timeout: time.Second}); err == nil {
		t.Fatal("dialed nothing")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleep(ctx, time.Hour); err == nil {
		t.Fatal("slept")
	}
	if err := sleep(context.Background(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
}

// TestValues makes every type a record's value.
func TestValues(t *testing.T) {
	u, _ := mssqlwire.ParseUUID("6F9619FF-8B86-D011-B42D-00C04FC964FF")
	at := time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC)
	for _, tc := range []struct {
		col  mssqlwire.Column
		v    any
		want string
	}{
		{mssqlwire.Column{}, nil, `null`},
		{mssqlwire.Column{}, int64(-1), `-1`},
		{mssqlwire.Column{}, true, `true`},
		{mssqlwire.Column{}, float32(1.5), `1.5`},
		{mssqlwire.Column{}, 2.5, `2.5`},
		{mssqlwire.Column{}, mssqlwire.Decimal("1.20"), `1.20`},
		{mssqlwire.Column{Type: mssqlwire.TypeDateN}, at, `"2026-01-02"`},
		{mssqlwire.Column{}, at, `"2026-01-02T03:04:05.000000006Z"`},
		{mssqlwire.Column{Scale: 3}, 13*time.Hour + 5*time.Millisecond, `"13:00:00.005"`},
		{mssqlwire.Column{Scale: 0}, time.Hour, `"01:00:00"`},
		{mssqlwire.Column{}, u, `"6F9619FF-8B86-D011-B42D-00C04FC964FF"`},
		{mssqlwire.Column{}, "s", `"s"`},
		{mssqlwire.Column{}, []byte{1}, `"AQ=="`},
		{mssqlwire.Column{}, struct{}{}, `"{}"`},
	} {
		got, _ := fitsync.AppendPlain(nil, value(&tc.col, tc.v))
		if string(got) != tc.want {
			t.Errorf("%#v: %s, want %s", tc.v, got, tc.want)
		}
	}
	for _, tc := range []struct {
		v    fitsync.Value
		want any
	}{
		{fitsync.Null(), nil}, {fitsync.Int(1), int64(1)}, {fitsync.Bool(true), true}, {fitsync.Float(1.5), 1.5},
		{fitsync.Decimal("1.5"), mssqlwire.Decimal("1.5")}, {fitsync.String("s"), "s"},
	} {
		if got := paramOf(tc.v); got != tc.want {
			t.Errorf("%v: %#v", tc.v, got)
		}
	}
	if b, ok := paramOf(fitsync.Bytes([]byte{1})).([]byte); !ok || b[0] != 1 {
		t.Fatal("bytes")
	}
	if tm, ok := paramOf(fitsync.Time(at)).(time.Time); !ok || !tm.Equal(at) {
		t.Fatal("time")
	}
	if p, ok := paramOf(fitsync.Date(2026, 1, 2)).(mssqlwire.Param); !ok || p.Type != "date" {
		t.Fatal("date")
	}
	l := lsn("000000000000000000ff")
	if l.next() != "00000000000000000100" || l.next().prev() != l || !validLSN(string(l)) || validLSN("00") || validLSN("000000000000000000FF") {
		t.Fatal("lsn")
	}
	if (&table{}).created() != (time.Time{}) || hexBytes("")[0] != 0 {
		t.Fatal("zero")
	}
	// A date key resumes with a date parameter.
	cond, args, err := after("", []string{"d"}, fitsync.Map(fitsync.Field{Name: "d", Value: fitsync.Date(2026, 1, 2)}))
	if err != nil || cond != "(([d] > @k1))" || args[0].(mssqlwire.Param).Type != "date" || args[0].(mssqlwire.Param).Name != "@k1" {
		t.Fatal(cond, args, err)
	}
	rd := &reader{srv: server{zone: time.UTC}}
	if !rd.serverTime("x").IsZero() {
		t.Fatal("serverTime")
	}
}

// follow runs s from position at until it fails, calling the n-th step at
// the n-th poll; the context ends at the poll after the last step.
func follow(t *testing.T, s *Source, at lsn, steps ...func()) ([]*fitsync.Record, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	n := 0
	s.sleep = func(ctx context.Context, d time.Duration) error {
		if n < len(steps) {
			steps[n]()
		} else {
			cancel()
		}
		n++
		return ctx.Err()
	}
	var recs []*fitsync.Record
	err := s.Run(ctx, &fitsync.Run{From: position{LSN: at}.marshal(), Emit: func(r *fitsync.Record) error {
		recs = append(recs, r)
		return nil
	}})
	return recs, err
}

// drop removes a capture instance, as sys.sp_cdc_disable_table does.
func (f *fake) drop(in *fakeInst) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, x := range f.instances {
		if x == in {
			f.instances = append(f.instances[:i], f.instances[i+1:]...)
			return
		}
	}
}

// TestDroppedInstance fails asking for a snapshot when an older capture
// instance is dropped before its changes were read, found by its query
// failing or by the capture instances changing while caught up; a dropped
// instance a remaining one covers, or a table no longer captured, goes on.
func TestDroppedInstance(t *testing.T) {
	lsn1 := lsn("00000000000000000001")
	t.Run("read", func(t *testing.T) {
		f := newFake()
		tb := f.table("t", 0)
		old := f.capture(tb, "dbo_t")
		f.commit(old, int64(cdcInsert), row2(1, "a"))
		s := newSource(t, f, map[string]any{"poll_interval": "1ms"})
		_, err := follow(t, s, lsn1, func() {
			f.commit(old, int64(cdcInsert), row2(2, "b"))
			nu := f.capture(tb, "dbo_t2")
			f.commit(old, int64(cdcInsert), row2(3, "c"), nu, int64(cdcInsert), row2(3, "c"))
			f.drop(old)
		})
		if !errors.Is(err, errNeedSnapshot) || !strings.Contains(err.Error(), "dbo_t of dbo.t was dropped") {
			t.Fatal(err)
		}
	})
	t.Run("caught up", func(t *testing.T) {
		f := newFake()
		tb := f.table("t", 0)
		old := f.capture(tb, "dbo_t")
		f.commit(old, int64(cdcInsert), row2(1, "a"))
		s := newSource(t, f, map[string]any{"poll_interval": "1ms"})
		_, err := follow(t, s, lsn1, func() {
			// The newer instance starts past changes not yet in
			// lsn_time_mapping.
			nu := f.capture(tb, "dbo_t2")
			nu.start += 2
			f.drop(old)
		})
		if !errors.Is(err, errNeedSnapshot) {
			t.Fatal(err)
		}
	})
	t.Run("covered", func(t *testing.T) {
		f := newFake()
		tb := f.table("t", 0)
		old := f.capture(tb, "dbo_t")
		f.commit(old, int64(cdcInsert), row2(1, "a"))
		s := newSource(t, f, map[string]any{"poll_interval": "1ms"})
		// The newer instance starts right after the position: dropping the
		// older loses nothing, and the read's failure is the error.
		_, err := follow(t, s, lsn1, func() {
			nu := f.capture(tb, "dbo_t2")
			f.commit(old, int64(cdcInsert), row2(2, "b"), nu, int64(cdcInsert), row2(2, "b"))
			f.drop(old)
		})
		if errors.Is(err, errNeedSnapshot) || err == nil || !strings.Contains(err.Error(), "invalid object name") {
			t.Fatal(err)
		}
		// Found while caught up, it goes on with the newer instance.
		recs, err := follow(t, s, lsn("00000000000000000002"), func() {
			f.capture(tb, "dbo_t3")
			f.drop(f.instances[0])
		}, func() {
			f.commit(f.instances[0], int64(cdcInsert), row2(3, "c"))
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		checkRecs(t, recs, `insert dbo.t {"id":3} {"id":3,"v":"c"} @3`)
	})
	t.Run("uncaptured", func(t *testing.T) {
		f := newFake()
		a, b := f.table("a", 0), f.table("b", 0)
		ia, ib := f.capture(a, "dbo_a"), f.capture(b, "dbo_b")
		f.commit(ia, int64(cdcInsert), row2(1, "a"))
		s := newSource(t, f, map[string]any{"poll_interval": "1ms"})
		// b is no longer captured: logged, and a goes on.
		recs, err := follow(t, s, lsn1, func() {
			f.drop(ib)
		}, func() {
			f.commit(ia, int64(cdcInsert), row2(2, "b"))
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		checkRecs(t, recs, `insert dbo.a {"id":2} {"id":2,"v":"b"} @2`)
		// A failing read whose instances changed and cannot be found
		// again is its own error.
		f.fail["FROM cdc.change_tables ct JOIN"] = errors.New("fake: down")
		f.skip["FROM cdc.change_tables ct JOIN"] = 1
		_, err = follow(t, s, lsn("00000000000000000002"), func() {
			nb := f.capture(b, "dbo_b2")
			f.commit(ia, int64(cdcInsert), row2(3, "c"), nb, int64(cdcInsert), row2(3, "c"))
			f.drop(ia)
		})
		if err == nil || !strings.Contains(err.Error(), "invalid object name") {
			t.Fatal(err)
		}
	})
}

// TestLag reports the transactions behind CDC's head, counted when a window
// is full, and change tracking's versions behind.
func TestLag(t *testing.T) {
	f := newFake()
	tb := f.table("t", 0)
	in := f.capture(tb, "dbo_t")
	for i := int64(1); i <= 5; i++ {
		f.commit(in, int64(cdcInsert), row2(i, "a"))
	}
	s := newSource(t, f, map[string]any{"snapshot": "never", "window_transactions": 2})
	var lags []string
	lag := func(l fitsync.SourceLag) { lags = append(lags, fmt.Sprint(l.Behind, " ", l.Unit)) }
	ctx := context.Background()
	err := s.Run(ctx, &fitsync.Run{From: position{LSN: zeroLSN}.marshal(), Once: true, Lag: lag, Emit: func(*fitsync.Record) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	// Counted once (then not again within a second), then the windows'.
	if got := strings.Join(lags, ","); got != "5 transactions,1 transactions,0 transactions" {
		t.Fatal(got)
	}
	f.fail["SELECT COUNT_BIG(*)"] = errors.New("fake: down")
	err = s.Run(ctx, &fitsync.Run{From: position{LSN: zeroLSN}.marshal(), Once: true, Lag: lag, Emit: func(*fitsync.Record) error { return nil }})
	if err == nil || !strings.Contains(err.Error(), "fake: down") {
		t.Fatal(err)
	}

	f = newFake()
	f.ct = true
	tb = f.table("t", 1)
	tb.ct = true
	f.ctVersion = 5
	s = newSource(t, f, map[string]any{"mode": "change_tracking", "snapshot": "never"})
	lags = nil
	err = s.Run(ctx, &fitsync.Run{From: position{Version: 3}.marshal(), Once: true, Lag: lag, Emit: func(*fitsync.Record) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(lags, ","); got != "2 versions,0 versions" {
		t.Fatal(got)
	}
}
