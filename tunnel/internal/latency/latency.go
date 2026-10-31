// Package latency measures how fast and how reliable a link is from the
// round trips of the pings, or the connects, made over it.
package latency

import (
	"math"
	"sync"
	"time"
)

// Stats is a link's round trips, in milliseconds.
type Stats struct {
	// Last is the latest round trip.
	Last float64 `json:"last"`
	// Avg is a moving average of the round trips, weighing recent ones most.
	Avg float64 `json:"avg"`
	// Min is the shortest round trip.
	Min float64 `json:"min"`
	// Loss is a moving average of the round trips lost, from 0 to 1.
	Loss float64 `json:"loss"`
	// Samples counts the round trips measured.
	Samples int `json:"samples"`
}

// Known reports whether s has measured a round trip.
func (s Stats) Known() bool { return s.Samples > 0 }

// alpha is how much a new round trip moves the averages.
const alpha = 0.3

// Meter gathers the round trips of a link into its Stats.
type Meter struct {
	mu sync.Mutex
	s  Stats
}

// Add records a round trip of d.
func (m *Meter) Add(d time.Duration) {
	ms := Ms(d)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.s.Samples == 0 {
		m.s.Avg, m.s.Min = ms, ms
	} else {
		m.s.Avg += alpha * (ms - m.s.Avg)
		m.s.Min = min(m.s.Min, ms)
	}
	m.s.Last = ms
	m.s.Loss *= 1 - alpha
	m.s.Samples++
}

// Lost records a round trip that got no answer.
func (m *Meter) Lost() {
	m.mu.Lock()
	m.s.Loss += alpha * (1 - m.s.Loss)
	m.mu.Unlock()
}

// Reset forgets every round trip, as for a new connection.
func (m *Meter) Reset() {
	m.mu.Lock()
	m.s = Stats{}
	m.mu.Unlock()
}

// Stats is what the meter measured, rounded to microseconds and to
// thousandths of loss.
func (m *Meter) Stats() Stats {
	m.mu.Lock()
	s := m.s
	m.mu.Unlock()
	s.Avg = round(s.Avg, 1000)
	s.Loss = round(s.Loss, 1000)
	return s
}

// Ms is d in milliseconds, to the microsecond.
func Ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func round(v, unit float64) float64 { return math.Round(v*unit) / unit }
