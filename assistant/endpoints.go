package assistant

import (
	"encoding/json"
	"os"
	"slices"
	"strings"

	"github.com/lesismal/fit/app/console"
	"github.com/lesismal/fit/config"
)

// redactor returns the redactor of what the tools hand a model under
// settings s: the providers' keys, from the store and the environment,
// and data.redact's patterns.
func (a *Assistant) redactor(s *config.Assistant) *redactor {
	a.mu.Lock()
	var literals []string
	for _, k := range a.keys {
		literals = append(literals, k)
	}
	a.mu.Unlock()
	var extra []string
	if s != nil {
		for _, p := range s.Providers {
			if p.APIKeyEnv != "" {
				if k := os.Getenv(p.APIKeyEnv); k != "" {
					literals = append(literals, k)
				}
			}
		}
		if s.Data != nil {
			extra = s.Data.Redact
		}
	}
	return newRedactor(literals, extra)
}

// toolView is a tool as GET /api/tools lists it.
type toolView struct {
	Name        string          `json:"name"`
	Group       string          `json:"group"`
	Level       string          `json:"level"`
	Kind        string          `json:"kind,omitempty"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	Enabled     bool            `json:"enabled"`
	Why         string          `json:"why,omitempty"`
}

// toolsBody is GET /api/tools's answer: the user's level, the model the
// chat uses and whether it calls tools, and every tool with whether this
// user may call it under the settings in use, and why not.
type toolsBody struct {
	User     string     `json:"user"`
	Level    string     `json:"level"`
	Provider string     `json:"provider"`
	Model    string     `json:"model"`
	UseTools bool       `json:"use_tools"`
	Store    bool       `json:"store"`
	Tools    []toolView `json:"tools"`
}

func (a *Assistant) toolsHandler(r *console.Request) {
	s := a.Settings()
	level := s.Level(r.User)
	b := toolsBody{User: r.User, Level: level, UseTools: true, Store: a.o.Store != nil, Tools: []toolView{}}
	b.Provider, b.Model = s.Default()
	if p := s.Provider(b.Provider); p != nil {
		for _, m := range p.Models {
			if m.Name == b.Model {
				b.UseTools = m.CallsTools()
			}
		}
	}
	for _, t := range a.registry.Tools() {
		ok, why := t.Allowed(level, s)
		if ok && !b.UseTools {
			ok, why = false, "the model does not call tools"
		}
		b.Tools = append(b.Tools, toolView{Name: t.Name, Group: t.Group, Level: t.Level, Kind: t.Kind, Description: t.Description,
			Parameters: t.Parameters, Enabled: ok, Why: why})
	}
	_ = r.JSON(200, b)
}

// conversationsHandler answers GET /api/conversations: the user's
// conversations, the latest first.
func (a *Assistant) conversationsHandler(r *console.Request) {
	user := r.User
	r.Go(func() {
		s := a.Settings()
		a.chats.mu.Lock()
		u := a.userLocked(user)
		list := make([]convSummary, 0, len(u.convs))
		for i := len(u.convs) - 1; i >= 0; i-- {
			c := u.convs[i]
			list = append(list, convSummary{ID: c.ID, Title: c.Title, Created: c.Created, Updated: c.Updated, Messages: len(c.Messages), Busy: c.busy != nil, Skill: c.Skill})
		}
		used := u.usedToday()
		a.chats.mu.Unlock()
		_ = r.JSON(200, map[string]any{"conversations": list, "max": s.Limits.Conversations(), "max_turns": s.Limits.Turns(),
			"tokens_today": used, "max_tokens_per_day": s.Limits.TokensPerDay(), "kept": a.o.Store != nil})
	})
}

// convView is a conversation as GET /api/conversations/{id} answers it,
// with the question it waits on.
type convView struct {
	*Conversation
	Busy bool   `json:"busy"`
	Ask  *Event `json:"ask,omitempty"`
}

// conversationHandler answers GET and DELETE /api/conversations/{id}.
func (a *Assistant) conversationHandler(r *console.Request) {
	id := strings.TrimPrefix(r.Path, "/api/conversations/")
	user, method := r.User, r.Request.Method
	if method != "GET" && method != "DELETE" {
		_ = console.Error(r.Context, 405, "GET or DELETE a conversation")
		return
	}
	r.Go(func() {
		a.chats.mu.Lock()
		u := a.userLocked(user)
		c := u.conversation(id)
		if c == nil {
			a.chats.mu.Unlock()
			_ = console.Error(r.Context, 404, "no conversation "+id)
			return
		}
		if method == "DELETE" {
			if c.busy != nil {
				a.chats.mu.Unlock()
				_ = console.Error(r.Context, 409, "the conversation is answering: stop it first")
				return
			}
			u.convs = slices.DeleteFunc(u.convs, func(x *Conversation) bool { return x == c })
			p := c.Proposal
			a.chats.mu.Unlock()
			a.saveChats(user)
			if p != nil {
				a.Audit(AuditEntry{User: user, Kind: AuditDiscard, Conversation: id, Proposal: auditOf(p, nil), Outcome: OutcomeDiscarded,
					Detail: "deleted with its conversation"})
			}
			_ = r.JSON(200, map[string]any{"deleted": id})
			return
		}
		cc := *c
		cc.Messages = slices.Clone(c.Messages)
		v := convView{Conversation: &cc, Busy: c.busy != nil}
		if tc := pendingAsk(c); tc != nil && c.busy == nil {
			q, _ := parseAsk(tc.Arguments)
			v.Ask = &Event{Type: EventAsk, ID: tc.ID, Question: q.Question, Options: q.Options}
		}
		a.chats.mu.Unlock()
		_ = r.JSON(200, v)
	})
}

// stopHandler answers POST /api/chat/stop: the chat under way in the
// conversation is stopped, as when its browser leaves.
func (a *Assistant) stopHandler(r *console.Request) {
	var body struct {
		Conversation string `json:"conversation"`
	}
	if !r.Decode(&body) {
		return
	}
	a.chats.mu.Lock()
	var c *chat
	if u := a.chats.users[r.User]; u != nil {
		if conv := u.conversation(body.Conversation); conv != nil {
			c = conv.busy
		}
	}
	a.chats.mu.Unlock()
	if c != nil {
		c.stop()
	}
	_ = r.JSON(200, map[string]bool{"stopped": c != nil})
}
