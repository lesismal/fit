package fitsync

import "testing"

func TestRecordIDs(t *testing.T) {
	pos := func(s string) Position { return Position(s) }
	a := &Record{Op: OpInsert, Tx: Tx{ID: "t", First: true}}
	b := &Record{Op: OpInsert, Tx: Tx{ID: "t", Last: true}, Position: pos(`{"n":2}`)}
	c := &Record{Op: OpInsert, Position: pos(`{"n":3}`)}
	c2 := &Record{Op: OpUpdate, Position: pos(`{"n":3}`)} // a transform's second record of c
	d := &Record{Op: OpInsert, Tx: Tx{ID: "u", First: true}}
	whole := RecordIDs([]*Record{a, b, c, c2, d})
	seen := map[string]bool{}
	for i, id := range whole {
		if len(id) != 32 || seen[id] {
			t.Fatalf("id %d %q: not 32 digits, or a repeat: %v", i, id, whole)
		}
		seen[id] = true
	}
	// The same records cut at a checkpoint keep their IDs.
	tail := RecordIDs([]*Record{c, c2, d})
	for i, id := range tail {
		if id != whole[i+2] {
			t.Errorf("record %d after a cut: %s, was %s", i+2, id, whole[i+2])
		}
	}
	if got := RecordIDs([]*Record{a, b}); got[0] != whole[0] || got[1] != whole[1] {
		t.Errorf("a batch ending at b: %v", got)
	}
	// No positions at all: by content and place, the same each time.
	x, y := RecordIDs([]*Record{a, d}), RecordIDs([]*Record{a, d})
	if x[0] != y[0] || x[1] != y[1] || x[0] == x[1] {
		t.Errorf("no positions: %v %v", x, y)
	}
	if len(RecordIDs(nil)) != 0 {
		t.Error("nil batch")
	}
}
