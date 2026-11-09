package main

import "testing"

// TestSyncMissing counts records that never arrived, not copies of ones
// that did.
func TestSyncMissing(t *testing.T) {
	for _, c := range []struct{ records, counted, extra, want int64 }{
		{500000, 500000, 0, 0},
		{500000, 500010, 10, 0}, // ten replayed: none missing
		{500000, 499990, 0, 10},
		{500000, 500000, 10, 10}, // ten copies hide ten lost
	} {
		s := syncResult{Records: c.records, Counted: c.counted, Extra: c.extra}
		if got := s.missing(); got != c.want {
			t.Errorf("%+v: missing %d, want %d", c, got, c.want)
		}
	}
}
