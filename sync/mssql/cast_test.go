package syncmssql

import (
	"strings"
	"testing"
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

// TestColumnsOf reads names, and lengths where there are.
func TestColumnsOf(t *testing.T) {
	cols, casts := columnsOf([][]any{{"a", nil}, {"b", "10"}, {"c"}})
	if strings.Join(cols, ",") != "a,b,c" || len(casts) != 1 || casts["b"] != "10" {
		t.Fatal(cols, casts)
	}
	if !strings.Contains(castLength("c."), "COLLATIONPROPERTY(c.collation_name, 'CodePage') NOT IN (1252, 65001)") {
		t.Fatal(castLength("c."))
	}
}
