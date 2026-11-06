package admin

import (
	"fmt"
	"net/http"
	"net/http/pprof"
	"runtime"
	"strconv"
	"sync"

	"github.com/lesismal/fit/config"
)

// pprofHandler serves Go's runtime profiles at /debug/pprof/, as
// net/http/pprof does, while the running config has pprof, and 404
// otherwise. It is mounted on the Admin API's own server only, behind its
// guard, and not on the console's: a CPU profile or a trace holds its
// connection for as long as it samples, which the Admin API's goroutine per
// connection, its write timeout and its max_conns bound, and which the
// console's pool is not for.
func (s *Server) pprofHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := s.configs.CurrentConfig()
		if c == nil || c.Pprof == nil {
			writeError(w, http.StatusNotFound, "profiles are off: set pprof in the config to serve them")
			return
		}
		// Only symbol takes a body. Elsewhere net/http/pprof reads seconds
		// from the query alone then, which is checked here.
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.URL.Path != "/debug/pprof/symbol" {
			w.Header().Set("Allow", "GET, HEAD")
			writeError(w, http.StatusMethodNotAllowed, "profiles are read with GET")
			return
		}
		// A CPU profile and a trace sample for seconds, and a delta profile
		// waits as long, while net/http/pprof stretches the write deadline
		// to match: pprof.max_duration bounds how long one holds its
		// connection.
		if v := r.URL.Query().Get("seconds"); v != "" {
			sec, err := strconv.ParseFloat(v, 64)
			// NaN and infinities are refused too, as more than the limit.
			if limit := c.Pprof.MaxSeconds(); err == nil && !(sec <= limit.Seconds()) {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("seconds, %s, is more than pprof.max_duration, %s", v, limit))
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

// profileRates are the block profile rate and the mutex profile fraction
// fit last set, so that a config that does not change them does not set
// them again.
var profileRates struct {
	sync.Mutex
	block, mutex int
}

// applyProfiling sets the runtime's sampling of blocking and of mutex
// contention to what p says, none when p is nil.
func applyProfiling(p *config.Pprof) {
	var block, mutex int
	if p != nil {
		block, mutex = p.BlockProfileRate, p.MutexProfileFraction
	}
	profileRates.Lock()
	defer profileRates.Unlock()
	if block != profileRates.block {
		runtime.SetBlockProfileRate(block)
		profileRates.block = block
	}
	if mutex != profileRates.mutex {
		runtime.SetMutexProfileFraction(mutex)
		profileRates.mutex = mutex
	}
}
