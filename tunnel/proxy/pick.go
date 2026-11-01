package proxy

import (
	"math"

	"github.com/lesismal/fit/tunnel/internal/latency"
)

// Path is a way to a node: through a hub, which carries Tunnels of the
// proxy's tunnels already.
type Path struct {
	// Hub is the round trip between the proxy and the hub, Node the
	// one between the hub and the node.
	Hub, Node latency.Stats
	Tunnels   int
}

// latencyFloor is added to every path's round trip, so that paths of a few
// milliseconds, within a host or a LAN, count as about as fast.
const latencyFloor = 10.0

// lossCost is how much a path's cost grows by for each part of its pings
// lost: a path that loses one ping in ten costs 1.5 times as much.
const lossCost = 5.0

// Cost is how slow each path is: its round trip, proxy to hub to node,
// plus latencyFloor, grown by its loss. A link not measured yet counts as
// the slowest one of its kind that was, or as 0 when none was.
func Cost(paths []Path) []float64 {
	var worstHub, worstNode float64
	for _, p := range paths {
		if p.Hub.Known() {
			worstHub = max(worstHub, p.Hub.Avg)
		}
		if p.Node.Known() {
			worstNode = max(worstNode, p.Node.Avg)
		}
	}
	out := make([]float64, len(paths))
	for i, p := range paths {
		rtt := worstHub + worstNode
		if p.Hub.Known() {
			rtt += p.Hub.Avg - worstHub
		}
		if p.Node.Known() {
			rtt += p.Node.Avg - worstNode
		}
		loss := 1 - (1-p.Hub.Loss)*(1-p.Node.Loss)
		out[i] = (rtt + latencyFloor) * (1 + lossCost*loss)
	}
	return out
}

// Weights is the share of the tunnels each path should carry: in inverse
// proportion to the square of its cost, so that a path twice as slow gets a
// quarter as many tunnels. They add up to 1.
func Weights(paths []Path) []float64 {
	cost := Cost(paths)
	out := make([]float64, len(paths))
	var sum float64
	for i, c := range cost {
		out[i] = 1 / (c * c)
		sum += out[i]
	}
	for i := range out {
		out[i] /= sum
	}
	return out
}

// Choose is the index of the path a new tunnel takes: the one that carries
// the fewest tunnels for its weight once it has the new one too, which
// brings the tunnels the paths carry toward their Weights. A tie goes to
// the faster path, then to the first.
func Choose(paths []Path) int {
	w, cost := Weights(paths), Cost(paths)
	best, bestLoad := 0, math.Inf(1)
	for i, p := range paths {
		load := float64(p.Tunnels+1) / w[i]
		if load < bestLoad*(1-1e-9) || load <= bestLoad*(1+1e-9) && cost[i] < cost[best] {
			best, bestLoad = i, load
		}
	}
	return best
}
