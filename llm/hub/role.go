// Package hub is the agent's hub role, agent.hub in fit's config: nodes and
// clients link to it; it keeps the table of its nodes, from what they push,
// aggregates their models for its clients, relays the clients' model calls
// to the least loaded node that serves the model, and lets its pages manage
// the nodes that allow it. See docs/agent.md.
package hub

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"strings"
	"time"

	"github.com/lesismal/fit/agent/internal/mesh"
	"github.com/lesismal/fit/agent/internal/model"
	"github.com/lesismal/fit/agent/internal/role"
	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
)

//go:embed ui
var uiFS embed.FS

func init() {
	app.Register(app.Info{
		ID: "agent.hub",
		Member: func(c *config.Config) any {
			if c.Agent == nil || c.Agent.Hub == nil {
				return nil
			}
			return c.Agent.Hub
		},
		New: func(h *app.Host) (app.Role, error) { return New(h), nil },
	})
}

// Role is a running hub.
type Role struct {
	*role.Base
	hub *Hub
}

// New returns a hub on h, started by its first Apply.
func New(h *app.Host) *Role {
	pages, _ := fs.Sub(uiFS, "ui")
	b := role.New(h, role.Spec{Role: model.RoleHub, Pages: pages})
	r := &Role{Base: b}
	r.hub = newHub(b.Mesh, b.Self, b.Log)
	b.Register(r)
	r.hub.register(b.Router)
	mesh.Handle(b.Router, model.MethodHubProfileSet, nil, 0, func(ctx context.Context, _ model.Identity, req *model.ProfileReq) (any, error) {
		name := strings.TrimSpace(req.Name)
		if name == "" || len([]rune(name)) > 64 {
			return nil, mesh.BadRequest("the name needs 1-64 characters")
		}
		if err := r.SetName(ctx, name); err != nil {
			return nil, err
		}
		return r.Self(), nil
	})
	return r
}

// Apply moves the hub to member, a *config.AgentHub.
func (r *Role) Apply(member any) error {
	m, ok := member.(*config.AgentHub)
	if !ok || m == nil {
		return errors.New("agent.hub: no member")
	}
	r.hub.setMaxNodes(m.Nodes())
	return r.Base.Apply(role.Common{ID: m.ID, Name: m.Name, Console: m.Console, Listen: m.Listen, Peers: m.Peers, Limits: m.Limits})
}

// Stop ends the hub: its links drain until ctx ends, for 10s at most.
func (r *Role) Stop(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	r.hub.close()
	r.Base.Stop(ctx)
}

// Hub returns the hub's state, for tests.
func (r *Role) Hub() *Hub { return r.hub }
