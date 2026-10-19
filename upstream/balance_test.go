package upstream

import (
	"strings"
	"sync"
	"testing"
)

func backends(weights ...int) []*Backend {
	bs := make([]*Backend, len(weights))
	for i, w := range weights {
		bs[i] = &Backend{Address: string(rune('a' + i)), Weight: w}
	}
	return bs
}

// counts picks n times and returns how often each backend came up, by
// address.
func counts(b Balancer, n int) map[string]int {
	got := map[string]int{}
	for range n {
		got[b.Pick().Address]++
	}
	return got
}

func TestBalancerRoundRobin(t *testing.T) {
	b := NewBalancer("", backends(1, 1, 1))
	if _, ok := b.(*roundRobin); !ok {
		t.Fatalf("the default with equal weights is %T", b)
	}
	var seq strings.Builder
	for range 6 {
		seq.WriteString(b.Pick().Address)
	}
	if seq.String() != "abcabc" {
		t.Fatalf("sequence %s", seq.String())
	}
	if got := counts(b, 3000); got["a"] != 1000 || got["b"] != 1000 || got["c"] != 1000 {
		t.Fatalf("3000 picks: %v", got)
	}
}

func TestWeightedRoundRobin(t *testing.T) {
	b := NewBalancer("", backends(5, 1, 1))
	if _, ok := b.(*weightedRoundRobin); !ok {
		t.Fatalf("the default with weights is %T", b)
	}
	// Smooth: nginx's sequence for 5, 1, 1, rather than five a's in a row.
	var seq strings.Builder
	for range 7 {
		seq.WriteString(b.Pick().Address)
	}
	if seq.String() != "aabacaa" {
		t.Fatalf("sequence %s", seq.String())
	}
	if got := counts(b, 7000); got["a"] != 5000 || got["b"] != 1000 || got["c"] != 1000 {
		t.Fatalf("7000 picks: %v", got)
	}
	b = NewBalancer(WeightedRoundRobin, backends(3, 2))
	if got := counts(b, 5000); got["a"] != 3000 || got["b"] != 2000 {
		t.Fatalf("weights 3 and 2: %v", got)
	}
}

func TestLeastConn(t *testing.T) {
	bs := backends(1, 1, 1)
	b := NewBalancer(LeastConn, bs)
	// Idle backends share the load.
	if got := counts(b, 300); got["a"] != 100 || got["b"] != 100 || got["c"] != 100 {
		t.Fatalf("idle: %v", got)
	}
	// Busy ones are passed over until the rest catch up.
	bs[0].active.Store(5)
	bs[1].active.Store(2)
	for range 3 {
		p := b.Pick()
		if p.Address != "c" {
			t.Fatalf("picked %s with a=5, b=2, c=%d", p.Address, bs[2].active.Load())
		}
		p.active.Add(1)
	}
	// Now b and c have 2 and 3: b next.
	if p := b.Pick(); p.Address != "b" {
		t.Fatalf("picked %s", p.Address)
	}

	// Weighted: a of weight 2 takes twice the requests under way.
	bs = backends(2, 1)
	b = NewBalancer(LeastConn, bs)
	for range 300 {
		b.Pick().active.Add(1)
	}
	if a, c := bs[0].active.Load(), bs[1].active.Load(); a != 200 || c != 100 {
		t.Fatalf("under way: %d and %d, want 200 and 100", a, c)
	}
}

func TestPickConcurrent(t *testing.T) {
	for _, policy := range []string{RoundRobin, WeightedRoundRobin, LeastConn} {
		bs := backends(3, 2, 1)
		b := NewBalancer(policy, bs)
		var wg sync.WaitGroup
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range 1000 {
					p := b.Pick()
					p.active.Add(1)
					p.active.Add(-1)
				}
			}()
		}
		wg.Wait()
		for _, x := range bs {
			if x.active.Load() != 0 {
				t.Fatalf("%s: %s left at %d", policy, x.Address, x.active.Load())
			}
		}
	}
}

func TestPickAllocs(t *testing.T) {
	for _, policy := range []string{RoundRobin, WeightedRoundRobin, LeastConn} {
		b := NewBalancer(policy, backends(3, 2, 1, 1))
		if n := testing.AllocsPerRun(1000, func() { b.Pick() }); n != 0 {
			t.Errorf("%s: %v allocations a pick", policy, n)
		}
	}
}

func BenchmarkPick(b *testing.B) {
	for _, policy := range []string{RoundRobin, WeightedRoundRobin, LeastConn} {
		bal := NewBalancer(policy, backends(3, 2, 1, 1))
		b.Run(policy, func(b *testing.B) {
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					bal.Pick()
				}
			})
		})
	}
}
