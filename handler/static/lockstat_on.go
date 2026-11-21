//go:build fit_lockstat

package static

import (
	"fmt"
	"math/bits"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// A build with the fit_lockstat tag measures how long the cache's write
// locks are waited for and held, at each place that takes one, and writes
// the distributions to standard error every lockstatEvery: for benchmarks,
// never in a release.
const (
	siteKeep = iota
	siteEvent
	siteLoad
	sites
)

var siteNames = [sites]string{"keep", "event", "load"}

const lockstatEvery = 10 * time.Second

// lockHist counts durations by power of two of nanoseconds: n[i] counts
// those of i bits, from 2^(i-1) to 2^i-1 ns.
type lockHist struct {
	n    [65]atomic.Uint64
	sum  atomic.Uint64
	high atomic.Uint64
}

func (h *lockHist) add(d int64) {
	d = max(d, 0)
	h.n[bits.Len64(uint64(d))].Add(1)
	h.sum.Add(uint64(d))
	for {
		m := h.high.Load()
		if uint64(d) <= m || h.high.CompareAndSwap(m, uint64(d)) {
			return
		}
	}
}

// summary is the count, mean, quantiles and maximum, the quantiles as the
// upper bound of their power of two.
func (h *lockHist) summary() string {
	var counts [65]uint64
	var total uint64
	for i := range counts {
		counts[i] = h.n[i].Load()
		total += counts[i]
	}
	if total == 0 {
		return "n=0"
	}
	q := func(p float64) time.Duration {
		want, seen := uint64(p*float64(total)), uint64(0)
		for i, c := range counts {
			seen += c
			if seen > want {
				return time.Duration(uint64(1)<<i - 1)
			}
		}
		return 0
	}
	return fmt.Sprintf("n=%d mean=%v p50<=%v p90<=%v p99<=%v p99.9<=%v max=%v",
		total, time.Duration(h.sum.Load()/total), q(.5), q(.9), q(.99), q(.999), time.Duration(h.high.Load()))
}

var waits, holds [sites]lockHist

var epoch = time.Now()

func lockNow() int64 { return int64(time.Since(epoch)) }

// lockWait takes mu for writing, for site, counting how long that took,
// and returns when it was taken.
func lockWait(mu *sync.RWMutex, site int) int64 {
	start := lockNow()
	mu.Lock()
	taken := lockNow()
	waits[site].add(taken - start)
	return taken
}

// lockHold lets mu go, which lockWait took at taken, for site, counting how
// long it was held.
func lockHold(mu *sync.RWMutex, site int, taken int64) {
	holds[site].add(lockNow() - taken)
	mu.Unlock()
}

func init() {
	go func() {
		for range time.Tick(lockstatEvery) {
			var b strings.Builder
			for s := range sites {
				fmt.Fprintf(&b, "lockstat %s wait %s\nlockstat %s hold %s\n", siteNames[s], waits[s].summary(), siteNames[s], holds[s].summary())
			}
			_, _ = os.Stderr.WriteString(b.String())
		}
	}()
}
