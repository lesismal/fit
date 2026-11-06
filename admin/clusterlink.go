package admin

import (
	"fmt"
	"net/http"

	"github.com/lesismal/fit/config"
)

// SourceCluster is a config a manager of many fits applied, through this
// fit's link to it (admin.cluster_member).
const SourceCluster = "cluster"

// LinkAPI returns the Admin API's endpoints for the requests a manager of
// many fits sends over this fit's link to it (admin.cluster_member), served
// in process: every endpoint of the Admin API but its dashboard and pprof,
// with nothing in front of them but the refusal the Admin API gives while a
// console user's password is temporary, since the link has authenticated
// the manager already. A config applied through it is from SourceCluster.
// Its handlers block: ServeHTTP runs off fib's workers. report, when not
// nil, is told of each config applied; logf, when not nil, of each change,
// as the console logs its users'.
func LinkAPI(a *config.Admin, configs *Configs, report func(Result), logf func(format string, args ...any)) http.Handler {
	s := &Server{configs: configs, report: report, maxBody: a.ConfigBytes(), source: SourceCluster}
	mux := s.routes()
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, _ := configs.TemporaryPassword(); user != "" {
			writeError(w, http.StatusForbidden, fmt.Sprintf("the console user %q still has its temporary password: change it on the console first", user))
			return
		}
		// What only the console sets, never a caller.
		r.Header.Del(ConsoleUserHeader)
		r.Header.Del(ConsoleHostHeader)
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		mux.ServeHTTP(sw, r)
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			return
		}
		path := r.URL.Path
		switch dry, _ := isDryRun(r); {
		case dry:
			logf("cluster: the manager: %s %s?dry_run=1: %d, checked and not applied", r.Method, path, sw.status)
		case sw.status < 300 && w.Header().Get(VersionHeader) != "":
			logf("cluster: the manager: %s %s applied config version %s", r.Method, path, w.Header().Get(VersionHeader))
		default:
			logf("cluster: the manager: %s %s: %d", r.Method, path, sw.status)
		}
	})
}

// statusWriter keeps the status a handler answered.
type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(status int) {
	if !w.wrote {
		w.status, w.wrote = status, true
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	w.wrote = true
	return w.ResponseWriter.Write(p)
}
