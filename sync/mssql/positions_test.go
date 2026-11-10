package syncmssql

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	fitsync "github.com/lesismal/fit/sync"
)

// runPositions is run, with the position-only records the source hands
// on (every one: the pipeline's gate is not here).
func runPositions(t *testing.T, s *Source, from fitsync.Position) ([]*fitsync.Record, []position, error) {
	t.Helper()
	var recs []*fitsync.Record
	var pos []position
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := s.Run(ctx, &fitsync.Run{From: from, Once: true, Emit: func(r *fitsync.Record) error {
		recs = append(recs, r)
		return nil
	}, Positions: func(f func() fitsync.Position) error {
		var p position
		if err := json.Unmarshal(f(), &p); err != nil {
			t.Fatal(err)
		}
		pos = append(pos, p)
		return nil
	}})
	return recs, pos, err
}

// TestPositionOnlyCDC: windows of none of the read tables' changes, of
// tables CDC captures and the source does not read and of tables CDC does
// not capture, hand on their end as a position-only record (I216 decision
// 12), so that a quiet table's checkpoint follows the log.
func TestPositionOnlyCDC(t *testing.T) {
	f := newFake()
	a := f.table("a", 1)
	ia := f.capture(a, "dbo_a")
	b := f.table("b", 0)
	ib := f.capture(b, "dbo_b")
	s := newSource(t, f, map[string]any{"tables": []string{"dbo.a"}, "snapshot": "never"})
	from := position{LSN: lsnOf(lsnBytes(f.commit(ia, int64(cdcInsert), row2(2, "b"))))}.marshal()
	f.commit(ib, int64(cdcInsert), row2(1, "x"))
	last := f.commit()
	recs, pos, err := runPositions(t, s, from)
	if err != nil || len(recs) != 0 {
		t.Fatal(recs, err)
	}
	if len(pos) != 1 || pos[0].LSN != lsnOf(lsnBytes(last)) {
		t.Fatalf("%+v", pos)
	}
	// Its error is the run's.
	f.commit()
	boom := errors.New("boom")
	err = s.Run(context.Background(), &fitsync.Run{From: from, Once: true, Emit: func(*fitsync.Record) error { return nil },
		Positions: func(func() fitsync.Position) error { return boom }})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
}

// TestPositionOnlyChangeTracking: versions of none of the read tables'
// changes hand on the version as a position-only record.
func TestPositionOnlyChangeTracking(t *testing.T) {
	f := newFake()
	f.ct = true
	tb := f.table("t", 0)
	tb.ct = true
	u := f.table("u", 0)
	u.ct = true
	s := newSource(t, f, map[string]any{"mode": "change_tracking", "snapshot": "never", "tables": []string{"dbo.t"}})
	f.ctChange(u, "I", row2(1, "a"))
	f.ctChange(u, "I", row2(2, "b"))
	recs, pos, err := runPositions(t, s, position{Version: 0}.marshal())
	if err != nil || len(recs) != 0 {
		t.Fatal(recs, err)
	}
	if len(pos) != 1 || pos[0].Version != 2 {
		t.Fatalf("%+v", pos)
	}
}
