package turn

import (
	"sync"
	"time"
)

// maxBuckets bounds the limiter's buckets. A bucket exists only for a
// username that relayed lately, and a full bucket is the same as none, so
// buckets that have refilled are dropped first; past the bound, a username
// with no bucket has its datagrams dropped until one refills.
const maxBuckets = 1 << 16

// sweepEvery is how often the limiter drops the buckets that have refilled.
const sweepEvery = 10 * time.Second

// limiter is a token bucket for each username and direction, holding a
// second's worth of the rate.
type limiter struct {
	mu        sync.Mutex
	buckets   map[bucketKey]*bucket
	lastSweep time.Time
}

type bucketKey struct {
	user   string
	toPeer bool
}

type bucket struct {
	tokens float64
	last   time.Time
}

func (l *limiter) init() { l.buckets = make(map[bucketKey]*bucket) }

// allow takes n bytes from user's bucket for the direction, at rate bytes a
// second, and reports whether it held them.
func (l *limiter) allow(user string, toPeer bool, n int, rate int64, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.lastSweep) >= sweepEvery {
		l.sweep(rate, now)
	}
	k := bucketKey{user, toPeer}
	b := l.buckets[k]
	if b == nil {
		if len(l.buckets) >= maxBuckets {
			if l.sweep(rate, now); len(l.buckets) >= maxBuckets {
				return false
			}
		}
		b = &bucket{tokens: float64(rate), last: now}
		l.buckets[k] = b
	} else if d := now.Sub(b.last); d > 0 {
		b.tokens = min(float64(rate), b.tokens+d.Seconds()*float64(rate))
		b.last = now
	}
	if b.tokens < float64(n) {
		return false
	}
	b.tokens -= float64(n)
	return true
}

// sweep drops the buckets that have refilled by now. l.mu is held.
func (l *limiter) sweep(rate int64, now time.Time) {
	l.lastSweep = now
	for k, b := range l.buckets {
		if b.tokens+now.Sub(b.last).Seconds()*float64(rate) >= float64(rate) {
			delete(l.buckets, k)
		}
	}
}
