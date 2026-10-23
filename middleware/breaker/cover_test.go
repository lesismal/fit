package breaker

import (
	"testing"
	"time"
)

// TestBreakerLateOutcome checks the outcome of a request let through while
// it was closed, which comes once it has opened: it is not counted.
func TestBreakerLateOutcome(t *testing.T) {
	b, err := load(t, `{"min_requests": 1, "open_duration": "1m"}`)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_000_000, 0)
	b.now = func() time.Time { return now }
	b.headAt = now
	b.record(now, false, true, false)
	if b.State() != Open {
		t.Fatal("not open")
	}
	before := b.buckets
	b.record(now, false, true, false)
	if b.State() != Open || b.buckets != before {
		t.Fatalf("a late outcome counted: %v %v", b.State(), b.buckets)
	}
}
