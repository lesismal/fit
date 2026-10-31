package latency

import (
	"testing"
	"time"
)

func TestMeter(t *testing.T) {
	var m Meter
	if m.Stats().Known() {
		t.Fatal("known before a round trip")
	}
	m.Add(10 * time.Millisecond)
	s := m.Stats()
	if !s.Known() || s.Last != 10 || s.Avg != 10 || s.Min != 10 || s.Loss != 0 {
		t.Fatalf("after 10ms: %+v", s)
	}
	m.Add(20 * time.Millisecond)
	s = m.Stats()
	if s.Last != 20 || s.Avg != 13 || s.Min != 10 || s.Samples != 2 {
		t.Fatalf("after 20ms: %+v", s)
	}
	m.Lost()
	if s = m.Stats(); s.Loss != 0.3 || s.Samples != 2 {
		t.Fatalf("after a loss: %+v", s)
	}
	m.Add(5 * time.Millisecond)
	if s = m.Stats(); s.Loss != 0.21 || s.Min != 5 {
		t.Fatalf("after 5ms: %+v", s)
	}
	m.Reset()
	if m.Stats() != (Stats{}) {
		t.Fatalf("after a reset: %+v", m.Stats())
	}
}
