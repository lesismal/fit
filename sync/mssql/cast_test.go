package syncmssql

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/logs"
	fitsync "github.com/lesismal/fit/sync"
)

// TestCasts selects the columns sys.columns says are of another code page
// as nvarchar, under their own names, in the snapshot, CDC's changes and
// change tracking's, and keeps the key's order and conditions on the
// table's own columns.
func TestCasts(t *testing.T) {
	f := newFake()
	tb := f.table("t", 3)
	tb.casts = map[string]string{"v": "20"}
	in := f.capture(tb, "dbo_t")
	s := newSource(t, f, map[string]any{"tables": []string{"dbo.t"}, "batch_size": 2})
	recs, err := run(t, s, nil, true)
	if err != nil || len(recs) != 3 {
		t.Fatal(recs, err)
	}
	f.commit(in, int64(cdcInsert), row2(4, "a"))
	if _, err := run(t, s, recs[2].Position, true); err != nil {
		t.Fatal(err)
	}
	var snap, changes string
	for _, q := range f.queries {
		switch {
		case strings.HasPrefix(q, "SELECT TOP (2) ") && strings.Contains(q, "[id] > @k1"):
			snap = q
		case strings.Contains(q, "fn_cdc_get_all_changes_"):
			changes = q
		}
	}
	if want := "SELECT TOP (2) t.[id], CAST(t.[v] AS nvarchar(20)) AS [v] FROM [dbo].[t] AS t WHERE ((t.[id] > @k1)) ORDER BY t.[id]"; snap != want {
		t.Fatalf("snapshot:\n%s\nwant\n%s", snap, want)
	}
	if !strings.Contains(changes, "__$operation, [id], CAST([v] AS nvarchar(20)) AS [v] FROM") {
		t.Fatal(changes)
	}

	// Change tracking, the key cast too.
	f = newFake()
	f.cdc, f.ct = false, true
	tb = f.table("t", 1)
	tb.ct = true
	tb.casts = map[string]string{"id": "max", "v": "max"}
	f.ctChange(tb, "U", row2(1, "x"))
	s = newSource(t, f, map[string]any{"mode": "change_tracking", "snapshot": "never"})
	if _, err := run(t, s, position{Version: 0}.marshal(), true); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, q := range f.queries {
		if strings.Contains(q, "CHANGETABLE") {
			found = true
			if !strings.Contains(q, "ct.SYS_CHANGE_OPERATION, CAST(ct.[id] AS nvarchar(max)) AS [id], CAST(t.[id] AS nvarchar(max)) AS [id], CAST(t.[v] AS nvarchar(max)) AS [v] FROM") ||
				!strings.HasSuffix(q, "ORDER BY ct.[id]") {
				t.Fatal(q)
			}
		}
	}
	if !found {
		t.Fatal(f.queries)
	}
}

// TestColumnsOf reads names, lengths and lossy collations where there
// are.
func TestColumnsOf(t *testing.T) {
	cols, casts, lossy := columnsOf([][]any{{"a", nil, nil}, {"b", "10", "Latin1_General_CI_AS"}, {"c"}})
	if strings.Join(cols, ",") != "a,b,c" || len(casts) != 1 || casts["b"] != "10" || len(lossy) != 1 || lossy["b"] != "Latin1_General_CI_AS" {
		t.Fatal(cols, casts, lossy)
	}
	if !strings.Contains(castLength("c."), "COLLATIONPROPERTY(c.collation_name, 'CodePage') NOT IN (1252, 65001)") {
		t.Fatal(castLength("c."))
	}
	if q := lossyCollation("sc."); !strings.Contains(q, "COLLATIONPROPERTY(sc.collation_name, 'CodePage') NOT IN (65001,") ||
		!strings.Contains(q, "DATABASEPROPERTYEX(DB_NAME(), 'Collation')") || !strings.HasSuffix(q, "THEN sc.collation_name END") {
		t.Fatal(q)
	}
}

// logged is a logger that keeps the messages it is given.
type logged struct {
	mu   sync.Mutex
	msgs []string
}

func (l *logged) logger() *logs.Logger {
	return logs.Func(func(format string, args ...any) {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.msgs = append(l.msgs, fmt.Sprintf(format, args...))
	})
}

// count is how many messages hold s.
func (l *logged) count(s string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, m := range l.msgs {
		if strings.Contains(m, s) {
			n++
		}
	}
	return n
}

// TestLossyCollation: a column read through CDC whose collation's code
// page is neither UTF-8's nor the database's is warned of once per column
// in a run, at discovery and when a table is picked up later, and in the
// connection test's warnings; a column CDC keeps, and change tracking,
// are not (I215 decision 14).
func TestLossyCollation(t *testing.T) {
	f := newFake()
	f.collation = "Chinese_PRC_CI_AS"
	a := f.table("a", 1)
	a.lossy = map[string]string{"v": "Latin1_General_CI_AS", "id": "Latin1_General_CI_AS"}
	ina := f.capture(a, "dbo_a")
	f.table("b", 1)
	f.capture(f.tables["dbo.b"], "dbo_b")
	s := newSource(t, f, map[string]any{"tables": []string{"dbo.*"}, "snapshot": "never"})
	var l logged
	s.log = l.logger()

	ws, err := s.TestWarnings(context.Background())
	if err != nil || len(ws) != 2 || !strings.HasPrefix(ws[0], "the column id of dbo.a is of the collation Latin1_General_CI_AS, not the database's Chinese_PRC_CI_AS: ") ||
		!strings.HasPrefix(ws[1], "the column v of dbo.a ") || !strings.Contains(ws[1], "reach the sinks as ?") {
		t.Fatal(ws, err)
	}
	if err := s.Test(context.Background()); err != nil {
		t.Fatal(err)
	}
	if l.count("") != 0 {
		t.Fatal("a test logged", l.msgs)
	}
	f.fail["dial"] = errors.New("fake: refused")
	if _, err := s.TestWarnings(context.Background()); err == nil {
		t.Fatal("dialed")
	}
	delete(f.fail, "dial")
	f.fail["captured_columns"] = errors.New("fake: no columns")
	if err := s.Test(context.Background()); err == nil || !strings.Contains(err.Error(), "no columns") {
		t.Fatal(err)
	}
	delete(f.fail, "captured_columns")

	// A run warns of each once, though the tables are found again; a
	// table picked up later is warned of then.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	polls := 0
	s.sleep = func(ctx context.Context, _ time.Duration) error {
		polls++
		switch polls {
		case 1:
			f.mu.Lock()
			c := f.table("c", 1)
			c.lossy = map[string]string{"v": "Japanese_CI_AS"}
			f.capture(c, "dbo_c")
			f.mu.Unlock()
		case 2:
			f.mu.Lock()
			f.capture(a, "dbo_a2")
			f.mu.Unlock()
		case 4:
			cancel()
		}
		return ctx.Err()
	}
	f.commit(ina, int64(cdcInsert), row2(2, "x"))
	err = s.Run(ctx, &fitsync.Run{Emit: func(*fitsync.Record) error { return nil }, Start: func(fitsync.Position) error { return nil }})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if l.count("the column v of dbo.a ") != 1 || l.count("the column id of dbo.a ") != 1 || l.count("the column v of dbo.c is of the collation Japanese_CI_AS") != 1 ||
		l.count("dbo.b") != 0 || l.count("sync mssql: the column") != 3 {
		t.Fatal(l.msgs)
	}

	// Change tracking reads the rows themselves.
	f = newFake()
	f.cdc, f.ct = false, true
	tb := f.table("t", 1)
	tb.ct = true
	tb.lossy = map[string]string{"v": "Latin1_General_CI_AS"}
	s = newSource(t, f, map[string]any{"mode": "change_tracking"})
	if ws, err := s.TestWarnings(context.Background()); err != nil || len(ws) != 0 {
		t.Fatal(ws, err)
	}
}
