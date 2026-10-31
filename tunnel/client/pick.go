package client

import (
	"math"

	"github.com/lesismal/fit/tunnel/internal/latency"
)

// Path is a way to a node: through a server, which carries Tunnels of the
// client's tunnels already.
type Path struct {
	// Server is the round trip between the client and the server, Node the
	// one between the server and the node.
	Server, Node latency.Stats
	Tunnels      int
}

// latencyFloor is added to every path's round trip, so that paths of a few
// milliseconds, within a host or a LAN, count as about as fast.
const latencyFloor = 10.0

// lossCost is how much a path's cost grows by for each part of its pings
// lost: a path that loses one ping in ten costs 1.5 times as much.
const lossCost = 5.0

// Cost is how slow each path is: its round trip, client to server to node,
// plus latencyFloor, grown by its loss. A link not measured yet counts as
// the slowest one of its kind that was, or as 0 when none was.
func Cost(paths []Path) []float64 {
	var worstServer, worstNode float64
	for _, p := range paths {
		if p.Server.Known() {
			worstServer = max(worstServer, p.Server.Avg)
		}
		if p.Node.Known() {
			worstNode = max(worstNode, p.Node.Avg)
		}
	}
	out := make([]float64, len(paths))
	for i, p := range paths {
		rtt := worstServer + worstNode
		if p.Server.Known() {
			rtt += p.Server.Avg - worstServer
		}
		if p.Node.Known() {
			rtt += p.Node.Avg - worstNode
		}
		loss := 1 - (1-p.Server.Loss)*(1-p.Node.Loss)
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
