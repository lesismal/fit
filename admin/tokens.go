package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/linktoken"
)

// The Admin API's endpoints for the link tokens the tunnel's hub and the LLM
// roles' listeners issue (decision 035), which fit token calls:
//
//	GET    /tokens               every issuing role's tokens: {"roles": {"tunnel.hub": [...]}}
//	GET    /tokens/{role}        one role's: {"tokens": [...]}
//	POST   /tokens/{role}        issue one: {name, note, expires_at | ttl}, answered
//	                             with the token, shown this once
//	PATCH  /tokens/{role}/{id}   change its expiry: {expires_at | ttl}; neither, never
//	DELETE /tokens/{role}/{id}   revoke it: its links close at once
//
// A change is a config edit of the role's member, applied as any page's is
// and recorded in the admin logger.

// tokenManager is the token manager of the running role the request names,
// or false once it has answered why there is none.
func (s *Server) tokenManager(w http.ResponseWriter, r *http.Request) (*linktoken.Manager, bool) {
	apps := s.configs.appManager()
	if apps == nil {
		writeError(w, http.StatusNotFound, "no role runs here")
		return nil, false
	}
	m, err := apps.LinkTokens(r.PathValue("role"))
	switch {
	case errors.Is(err, app.ErrNoRole):
		writeError(w, http.StatusNotFound, "no role "+r.PathValue("role")+" runs here")
		return nil, false
	case err != nil:
		writeError(w, http.StatusBadRequest, err.Error())
		return nil, false
	}
	return m, true
}

// appManager is the roles' manager, or nil.
func (cs *Configs) appManager() *app.Manager {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.apps
}

// tokenError answers err, a token manager's.
func tokenError(w http.ResponseWriter, err error) {
	var ce config.Errors
	var e *config.Error
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, linktoken.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, linktoken.ErrBadRequest), errors.As(err, &ce), errors.As(err, &e):
		status = http.StatusBadRequest
	case errors.Is(err, app.ErrBusy), errors.Is(err, app.ErrStopped), errors.Is(err, context.DeadlineExceeded):
		status = http.StatusServiceUnavailable
	}
	writeError(w, status, err.Error())
}

// readTokenBody decodes the request's JSON body into v.
func (s *Server) readTokenBody(w http.ResponseWriter, r *http.Request, v any) bool {
	data, ok := s.readBody(w, r, "application/json")
	if !ok {
		return false
	}
	if err := json.Unmarshal(data, v); err != nil {
		writeError(w, http.StatusBadRequest, "the body: "+err.Error())
		return false
	}
	return true
}

func (s *Server) listAllTokens(w http.ResponseWriter, _ *http.Request) {
	out := map[string][]linktoken.View{}
	if apps := s.configs.appManager(); apps != nil {
		for _, id := range apps.TokenRoles() {
			if m, err := apps.LinkTokens(id); err == nil {
				out[id] = m.List()
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"roles": out})
}

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) {
	m, ok := s.tokenManager(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": m.List()})
}

func (s *Server) issueToken(w http.ResponseWriter, r *http.Request) {
	m, ok := s.tokenManager(w, r)
	if !ok {
		return
	}
	var req linktoken.IssueReq
	if !s.readTokenBody(w, r, &req) {
		return
	}
	is, err := m.Issue(r.Context(), req)
	if err != nil {
		tokenError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, is)
}

func (s *Server) tokenExpiry(w http.ResponseWriter, r *http.Request) {
	m, ok := s.tokenManager(w, r)
	if !ok {
		return
	}
	var req linktoken.ExpiryReq
	if !s.readTokenBody(w, r, &req) {
		return
	}
	v, err := m.SetExpiry(r.Context(), r.PathValue("id"), req)
	if err != nil {
		tokenError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request) {
	m, ok := s.tokenManager(w, r)
	if !ok {
		return
	}
	if err := m.Revoke(r.Context(), r.PathValue("id")); err != nil {
		tokenError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
