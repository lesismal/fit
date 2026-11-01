package wire

import (
	"math"
	"sync"
	"time"

	"github.com/lesismal/fit/llm/internal/model"
)

// rttAlpha is how much a new round trip moves the average.
const rttAlpha = 0.3

// rttMeter gathers the round trips of a link's pings, as the tunnel's
// latency meter does: the last, a moving average weighing recent ones most,
// and the shortest.
type rttMeter struct {
	mu sync.Mutex
	s  model.RTT
	at time.Time // when the last round trip was measured
}

func (m *rttMeter) add(d time.Duration, now time.Time) {
	ms := float64(d.Microseconds()) / 1000
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.s.Samples == 0 {
		m.s.Avg, m.s.Min = ms, ms
	} else {
		m.s.Avg += rttAlpha * (ms - m.s.Avg)
		m.s.Min = min(m.s.Min, ms)
	}
	m.s.Last = ms
	m.s.Samples++
	m.at = now
}

// stats is what the meter measured, the average to the microsecond.
func (m *rttMeter) stats() model.RTT {
	m.mu.Lock()
	s := m.s
	m.mu.Unlock()
	s.Avg = math.Round(s.Avg*1000) / 1000
	return s
}

// last is when the last round trip was measured, the zero time before any.
func (m *rttMeter) last() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.at
}
