//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd || windows

package proxy

import (
	"math"
	"testing"

	"github.com/lesismal/fit/tunnel/internal/latency"
)

func rtt(avg, loss float64) latency.Stats {
	return latency.Stats{Last: avg, Avg: avg, Min: avg, Loss: loss, Samples: 1}
}

// spread has n tunnels choose among paths, one after the other, and counts
// how many each path got.
func spread(paths []Path, n int) []int {
	for range n {
		paths[Choose(paths)].Tunnels++
	}
	out := make([]int, len(paths))
	for i, p := range paths {
		out[i] = p.Tunnels
	}
	return out
}

func TestChoose(t *testing.T) {
	for _, c := range []struct {
		name  string
		paths []Path
		n     int
		want  []int
	}{
		{"unmeasured paths alternate", []Path{{}, {}}, 4, []int{2, 2}},
		{"equal paths alternate", []Path{{Hub: rtt(5, 0), Node: rtt(5, 0)}, {Hub: rtt(5, 0), Node: rtt(5, 0)}}, 5, []int{3, 2}},
		// Costs 20 and 40: weights 0.8 and 0.2.
		{"a path twice as slow gets a quarter as many", []Path{{Hub: rtt(5, 0), Node: rtt(5, 0)}, {Hub: rtt(10, 0), Node: rtt(20, 0)}}, 10, []int{8, 2}},
		{"the node's leg counts as much as the proxy's", []Path{{Hub: rtt(30, 0), Node: rtt(0, 0)}, {Hub: rtt(0, 0), Node: rtt(30, 0)}}, 4, []int{2, 2}},
		// An unmeasured leg counts as the slowest measured one.
		{"an unmeasured leg counts as the slowest", []Path{{Hub: rtt(10, 0)}, {Hub: rtt(10, 0), Node: rtt(30, 0)}, {Hub: rtt(10, 0), Node: rtt(0, 0)}}, 18, []int{2, 2, 14}},
		// A loss of 0.2 doubles the cost: weights 0.8 and 0.2 again.
		{"loss costs", []Path{{Hub: rtt(10, 0.2)}, {Hub: rtt(10, 0)}}, 10, []int{2, 8}},
		// The tunnels already open count.
		{"loaded paths wait", []Path{{Tunnels: 3}, {}}, 3, []int{3, 3}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := spread(c.paths, c.n)
			for i := range c.want {
				if got[i] != c.want[i] {
					t.Fatalf("got %v, want %v (weights %v)", got, c.want, Weights(c.paths))
				}
			}
		})
	}
}

func TestWeights(t *testing.T) {
	w := Weights([]Path{{Hub: rtt(5, 0), Node: rtt(5, 0)}, {Hub: rtt(10, 0), Node: rtt(20, 0)}})
	if math.Abs(w[0]-0.8) > 1e-9 || math.Abs(w[1]-0.2) > 1e-9 {
		t.Fatalf("%v", w)
	}
}
