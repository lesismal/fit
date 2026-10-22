package admin

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/lesismal/fit/middleware/cache"
)

// purgeRequest is the body of POST /cache/purge.
type purgeRequest struct {
	Zone string   `json:"zone"`
	URLs []string `json:"urls"`
	Tags []string `json:"tags"`
	All  bool     `json:"all"`
}

// cacheZones answers GET /cache: what each cache zone keeps.
func (s *Server) cacheZones(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, cache.Zones())
}

// purge answers POST /cache/purge, which removes responses a cache zone
// keeps: by URL, by tag, or all of them.
func (s *Server) purge(w http.ResponseWriter, r *http.Request) {
	data, ok := s.readBody(w, r, "application/json")
	if !ok {
		return
	}
	var req purgeRequest
	if err := json.Unmarshal(data, &req); err != nil {
		writeError(w, http.StatusBadRequest, "the body: "+err.Error())
		return
	}
	if len(req.URLs) == 0 && len(req.Tags) == 0 && !req.All {
		writeError(w, http.StatusBadRequest, "name urls, tags, or all")
		return
	}
	n, err := cache.Purge(req.Zone, req.URLs, req.Tags, req.All)
	switch {
	case errors.Is(err, cache.ErrNoZone):
		writeError(w, http.StatusNotFound, err.Error())
	case err != nil:
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeJSON(w, http.StatusOK, map[string]int{"purged": n})
	}
}
