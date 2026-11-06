package mcp

import (
	"strings"
	"time"

	"github.com/lesismal/fit/app/console"
	"github.com/lesismal/fit/config"
)

// Routes adds the MCP page's endpoints to the assistant's module, under
// /api/m/ai/: GET /api/mcp, the server's state and the user's tokens (every
// user's for a user who applies); POST /api/mcp/tokens, a token made, shown
// once; DELETE /api/mcp/tokens/{id}, one revoked.
func (s *Server) Routes(api *console.API) {
	api.Handle("GET", "/api/mcp", s.page)
	api.Handle("POST", "/api/mcp/tokens", s.create)
	api.HandlePrefix("DELETE", "/api/mcp/tokens/", s.revoke)
}

// tokenView is a token as the page shows it: never its hash.
type tokenView struct {
	ID       string   `json:"id"`
	User     string   `json:"user"`
	Name     string   `json:"name"`
	Level    string   `json:"level"`
	Created  int64    `json:"created"`
	Expires  int64    `json:"expires"`
	Expired  bool     `json:"expired"`
	Allow    []string `json:"allow,omitempty"`
	LastUsed int64    `json:"last_used,omitempty"`
	LastAddr string   `json:"last_addr,omitempty"`
	// Effective is the level its calls have now: its own, within its
	// user's.
	Effective string `json:"effective"`
}

// mcpView is what the page shows of the server: on or off, its settings
// with their defaults, where it is, and the tokens.
type mcpView struct {
	Enabled   bool              `json:"enabled"`
	Settings  map[string]any    `json:"settings"`
	Endpoint  string            `json:"endpoint"`
	Proposals string            `json:"proposals"`
	Store     bool              `json:"store"`
	User      string            `json:"user"`
	Level     string            `json:"level"`
	All       bool              `json:"all"`
	Levels    []string          `json:"levels"`
	Versions  []string          `json:"versions"`
	Tokens    []tokenView       `json:"tokens"`
	Defaults  map[string]string `json:"defaults"`
}

func (s *Server) view(user string) mcpView {
	set := s.a.Settings()
	m := set.MCP
	level := set.Level(user)
	all := level == config.AssistantApply
	allow := []string{}
	if m != nil {
		allow = append(allow, m.Allow...)
	}
	v := mcpView{Enabled: m != nil, Endpoint: strings.TrimSuffix(s.o.URL(), "/") + Path, Proposals: s.proposalsURL(),
		Store: s.o.Store != nil, User: user, Level: level, All: all, Levels: tokenLevels, Versions: supportedVersions, Tokens: []tokenView{},
		Settings: map[string]any{"allow": allow, "max_tokens_per_user": m.TokensPerUser(), "max_token_days": m.TokenDays(),
			"max_sessions": m.Sessions(), "session_idle_timeout": m.SessionIdle().String(), "max_request_bytes": m.RequestBytes(),
			"max_concurrent_calls": m.ConcurrentCalls(), "max_calls_per_minute": m.CallsPerMinute()}}
	now := s.now().UnixMilli()
	for _, t := range s.tokens.view(user, all) {
		v.Tokens = append(v.Tokens, tokenView{ID: t.ID, User: t.User, Name: t.Name, Level: t.Level, Created: t.Created, Expires: t.Expires,
			Expired: t.expired(now), Allow: t.Allow, LastUsed: t.LastUsed, LastAddr: t.LastAddr, Effective: effective(t.Level, set.Level(t.User))})
	}
	return v
}

func (s *Server) page(r *console.Request) { _ = r.JSON(200, s.view(r.User)) }

// createdBody is a token made: the token itself, shown this once, and how
// it is listed.
type createdBody struct {
	Token string    `json:"token"`
	View  tokenView `json:"view"`
}

// create answers POST /api/mcp/tokens: a token for the user, at the level
// asked (never above the user's), kept in the store on the pool.
func (s *Server) create(r *console.Request) {
	var n newToken
	if !r.Decode(&n) {
		return
	}
	set := s.a.Settings()
	level := set.Level(r.User)
	if msg := n.check(set.MCP); msg != "" {
		_ = console.Error(r.Context, 400, msg)
		return
	}
	switch {
	case level == config.AssistantOff:
		_ = console.Error(r.Context, 403, "the assistant is off for "+r.User)
		return
	case n.Level == config.AssistantPropose && effective(n.Level, level) != config.AssistantPropose:
		_ = console.Error(r.Context, 403, r.User+"'s level is "+level+": a token of the level propose needs the level propose or apply")
		return
	}
	user := r.User
	r.Go(func() {
		t, raw, status, msg := s.tokens.add(user, n, set.MCP.TokensPerUser())
		if status != 0 {
			_ = console.Error(r.Context, status, msg)
			return
		}
		if err := s.tokens.write(); err != nil {
			s.tokens.remove(t.ID, user, false)
			_ = console.Error(r.Context, 500, "saving the token: "+err.Error())
			return
		}
		r.Logf("made the MCP token %q (%s, until %s)", t.Name, t.Level, time.UnixMilli(t.Expires).UTC().Format(time.DateOnly))
		_ = r.JSON(200, createdBody{Token: raw, View: tokenView{ID: t.ID, User: t.User, Name: t.Name, Level: t.Level, Created: t.Created,
			Expires: t.Expires, Allow: t.Allow, Effective: effective(t.Level, level)}})
	})
}

// revoke answers DELETE /api/mcp/tokens/{id}: the user's own token, or, for
// a user who applies, anyone's, removed from the store; its sessions end,
// and its next request is refused.
func (s *Server) revoke(r *console.Request) {
	id := strings.TrimPrefix(r.Path, "/api/mcp/tokens/")
	anyone := s.a.Settings().Level(r.User) == config.AssistantApply
	user := r.User
	r.Go(func() {
		t := s.tokens.remove(id, user, anyone)
		if t == nil {
			_ = console.Error(r.Context, 404, "no token "+id+" of yours")
			return
		}
		s.forget(t.ID)
		if err := s.tokens.write(); err != nil {
			// Revoked here all the same: refused until fit restarts.
			_ = console.Error(r.Context, 500, "the token is refused from now on, but the store could not be written, so it comes back when fit restarts: "+err.Error())
			return
		}
		if t.User != user {
			r.Logf("revoked the MCP token %q of %q", t.Name, t.User)
		} else {
			r.Logf("revoked the MCP token %q", t.Name)
		}
		_ = r.JSON(200, map[string]any{"revoked": t.ID})
	})
}
