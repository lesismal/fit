package admin

import (
	"net/http"
	"slices"
	"strconv"
	"sync/atomic"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
)

// CapLogsRecent: GET /logs/recent, the latest messages of fit's own log
// from its ring, after a sequence number, which a manager tails across its
// members (I150).
const CapLogsRecent = "logs.recent"

// MaxRecentLogs bounds the messages GET /logs/recent answers at once.
const MaxRecentLogs = logs.RingSize

// recentLogsBody is GET /logs/recent's answer: the messages, oldest first,
// the sequence number of the latest the ring holds, which the next request
// asks after, and the ring's size.
type recentLogsBody struct {
	Records  []logs.Record `json:"records"`
	Last     uint64        `json:"last"`
	RingSize int           `json:"ring_size"`
}

// recentLogs answers GET /logs/recent?logger=&level=&contains=&limit=&after=:
// the latest messages of fit's log the filter lets through, at most limit
// (100 by default, MaxRecentLogs at most), those after the sequence number
// after when it is given. fit's log keeps them in memory, a fixed ring:
// nothing is read from a file.
func (s *Server) recentLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := logs.Filter{Logger: q.Get("logger"), Level: q.Get("level"), Contains: q.Get("contains")}
	if f.Level != "" && !slices.Contains(config.Levels, f.Level) {
		writeError(w, http.StatusBadRequest, "level is debug, info, warn or error, not "+strconv.Quote(f.Level))
		return
	}
	if len(f.Contains) > 256 || len(f.Logger) > 64 {
		writeError(w, http.StatusBadRequest, "contains is 256 bytes at most, and logger 64")
		return
	}
	for _, p := range []struct {
		name string
		dst  func(uint64)
		max  uint64
	}{
		{"limit", func(n uint64) { f.Limit = int(n) }, MaxRecentLogs},
		{"after", func(n uint64) { f.After = n }, 1<<63 - 1},
	} {
		v := q.Get(p.name)
		if v == "" {
			continue
		}
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil || n > p.max {
			writeError(w, http.StatusBadRequest, p.name+" is a whole number, at most "+strconv.FormatUint(p.max, 10))
			return
		}
		p.dst(n)
	}
	body := recentLogsBody{Records: []logs.Record{}, RingSize: logs.RingSize}
	if l := s.configs.env.Log; l != nil {
		body.Last = l.Last()
		if recs := l.Recent(f); recs != nil {
			body.Records = recs
		}
	}
	writeJSON(w, http.StatusOK, body)
}

// clusterHandler is what serves /cluster/ on the Admin API: the manager's
// endpoints for fit cluster (I150), set by SetCluster once the manager
// runs; nil on a fit that manages no members.
type clusterHandler struct{ h http.Handler }

// SetCluster serves h at /cluster and under /cluster/ on this Admin API,
// behind its token, as every endpoint is: the endpoints a manager of other
// fits offers its command line. A nil h takes them away.
func (s *Server) SetCluster(h http.Handler) {
	if h == nil {
		s.cluster.Store(nil)
		return
	}
	s.cluster.Store(&clusterHandler{h: h})
}

// clusterAPI answers /cluster and /cluster/...: through the manager's
// handler, or 404 on a fit that manages no members.
func (s *Server) clusterAPI(w http.ResponseWriter, r *http.Request) {
	c := s.cluster.Load()
	if c == nil {
		writeError(w, http.StatusNotFound, "this fit manages no members: admin.cluster is not set")
		return
	}
	c.h.ServeHTTP(w, r)
}

// clusterSlot is the Server's field SetCluster sets.
type clusterSlot = atomic.Pointer[clusterHandler]
