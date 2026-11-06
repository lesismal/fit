package common

import (
	"context"
	"errors"
	stdhttp "net/http"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/app/console"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/linktoken"
)

// TokenStatus is the HTTP status of an error of a token manager's.
func TokenStatus(err error) int {
	var ce config.Errors
	var e *config.Error
	switch {
	case errors.Is(err, linktoken.ErrNotFound):
		return stdhttp.StatusNotFound
	case errors.Is(err, linktoken.ErrBadRequest), errors.As(err, &ce), errors.As(err, &e):
		return stdhttp.StatusBadRequest
	case errors.Is(err, app.ErrBusy), errors.Is(err, app.ErrStopped), errors.Is(err, context.DeadlineExceeded):
		return stdhttp.StatusServiceUnavailable
	}
	return stdhttp.StatusInternalServerError
}

// tokenIDReq names a token, with its new expiry for /api/tokens/expiry.
type tokenIDReq struct {
	ID string `json:"id"`
	linktoken.ExpiryReq
}

// TokenRoutes adds the API of a listener's link tokens to a role's pages:
//
//	GET  /api/tokens         the tokens, with their last use, members and links
//	POST /api/tokens         issue one: {name, note, expires_at | ttl}, answered
//	                         with the token, shown this once
//	POST /api/tokens/expiry  {id, expires_at | ttl}; neither, never
//	POST /api/tokens/revoke  {id}: its links close at once
//
// The changes wait for the config's apply, on the console's pool.
func TokenRoutes(api *console.API, m *linktoken.Manager) {
	api.Handle("GET", "/api/tokens", func(r *console.Request) {
		_ = r.JSON(stdhttp.StatusOK, map[string]any{"tokens": m.List()})
	})
	answer := func(r *console.Request, v any, err error) {
		if err != nil {
			Fail(r, TokenStatus(err), err.Error())
			return
		}
		_ = r.JSON(stdhttp.StatusOK, v)
	}
	api.Handle("POST", "/api/tokens", func(r *console.Request) {
		var req linktoken.IssueReq
		if !r.Decode(&req) {
			return
		}
		r.Logf("issues a link token named %q", req.Name)
		r.Go(func() {
			is, err := m.Issue(context.Background(), req)
			answer(r, is, err)
		})
	})
	api.Handle("POST", "/api/tokens/expiry", func(r *console.Request) {
		var req tokenIDReq
		if !r.Decode(&req) {
			return
		}
		r.Logf("changes the expiry of link token %s", req.ID)
		r.Go(func() {
			v, err := m.SetExpiry(context.Background(), req.ID, req.ExpiryReq)
			answer(r, v, err)
		})
	})
	api.Handle("POST", "/api/tokens/revoke", func(r *console.Request) {
		var req tokenIDReq
		if !r.Decode(&req) {
			return
		}
		r.Logf("revokes link token %s", req.ID)
		r.Go(func() {
			err := m.Revoke(context.Background(), req.ID)
			answer(r, map[string]bool{"ok": true}, err)
		})
	})
}
