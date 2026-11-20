package websockify

import (
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	fib "github.com/lesismal/fib"

	"github.com/lesismal/fit/observe"
)

// What the websockify handlers bridged, for the metrics: per bridge name,
// the WebSockets open and how requests ended, and the bytes passed each
// way. A name's counters are shared by the handlers that have it and kept
// across reloads that keep it, so they start again from 0 only once no
// config has it.

// statsKey is what a handler's counters are shared by.
type statsKey struct {
	engine *fib.Engine
	name   string
}

// stats are the counters of one statsKey.
type stats struct {
	key  statsKey
	refs int // under statsMu
	// open is the WebSockets bridged now.
	open atomic.Int64
	// bridged, refused, busy and failed count requests: those bridged; those
	// refused for their handshake, origin or token; those past max_conns;
	// and those whose target could not be reached.
	bridged, refused, busy, failed atomic.Uint64
	// toTarget and toClient are the bytes passed each way, as the target
	// sees them: decoded from base64, or before it is encoded.
	toTarget, toClient atomic.Uint64
}

var (
	statsMu  sync.Mutex
	allStats = map[statsKey]*stats{}
)

func takeStats(k statsKey) *stats {
	statsMu.Lock()
	defer statsMu.Unlock()
	st := allStats[k]
	if st == nil {
		st = &stats{key: k}
		allStats[k] = st
	}
	st.refs++
	return st
}

func giveStats(st *stats) {
	statsMu.Lock()
	defer statsMu.Unlock()
	st.refs--
	if st.refs == 0 {
		delete(allStats, st.key)
	}
}

// Stats is what the websockify handlers of one bridge name did.
type Stats struct {
	Name     string `json:"name"`
	Open     int64  `json:"open"`
	Bridged  uint64 `json:"bridged"`
	Refused  uint64 `json:"refused"`
	Busy     uint64 `json:"busy"`
	Failed   uint64 `json:"failed"`
	ToTarget uint64 `json:"to_target_bytes"`
	ToClient uint64 `json:"to_client_bytes"`
}

// AllStats returns what the websockify handlers of engine did, one entry a
// bridge name, sorted by it; none when no websockify route runs.
func AllStats(engine *fib.Engine) []Stats {
	statsMu.Lock()
	var out []Stats
	for k, st := range allStats {
		if k.engine != engine {
			continue
		}
		out = append(out, Stats{Name: k.name, Open: st.open.Load(), Bridged: st.bridged.Load(),
			Refused: st.refused.Load(), Busy: st.busy.Load(), Failed: st.failed.Load(),
			ToTarget: st.toTarget.Load(), ToClient: st.toClient.Load()})
	}
	statsMu.Unlock()
	slices.SortFunc(out, func(a, b Stats) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// WriteMetrics writes the metrics of engine's websockify handlers, by
// bridge name.
func WriteMetrics(w observe.MetricWriter, engine *fib.Engine) {
	all := AllStats(engine)
	if len(all) == 0 {
		return
	}
	w.Family("fit_websockify_connections_open", "gauge", "WebSockets a websockify handler bridges to its target now.")
	for _, s := range all {
		w.Sample("fit_websockify_connections_open", float64(s.Open), "bridge", s.Name)
	}
	w.Family("fit_websockify_requests_total", "counter", "Requests to websockify handlers, by result: bridged; refused, for their handshake, origin or token; busy, past max_conns; failed, their target not reached.")
	for _, s := range all {
		w.Sample("fit_websockify_requests_total", float64(s.Bridged), "bridge", s.Name, "result", "bridged")
		w.Sample("fit_websockify_requests_total", float64(s.Refused), "bridge", s.Name, "result", "refused")
		w.Sample("fit_websockify_requests_total", float64(s.Busy), "bridge", s.Name, "result", "busy")
		w.Sample("fit_websockify_requests_total", float64(s.Failed), "bridge", s.Name, "result", "failed")
	}
	w.Family("fit_websockify_bytes_total", "counter", "Bytes websockify handlers passed, by direction: to the target, decoded from base64 when the client sends it, and to the client, before it is encoded.")
	for _, s := range all {
		w.Sample("fit_websockify_bytes_total", float64(s.ToTarget), "bridge", s.Name, "direction", "to_target")
		w.Sample("fit_websockify_bytes_total", float64(s.ToClient), "bridge", s.Name, "direction", "to_client")
	}
}
