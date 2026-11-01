package mesh

import (
	"context"
	"log/slog"
	"time"

	"github.com/lesismal/fit/llm/internal/model"
)

// PeersResp builds the reply of model.MethodPeers.
func (m *Mesh) PeersResp() model.PeersResp {
	return model.PeersResp{Self: m.Self(), Listen: m.Listen(), Conns: m.Peers(), Targets: m.Targets()}
}

// Status builds the reply of model.MethodStatus.
func (m *Mesh) Status() model.MeshStatus {
	return model.MeshStatus{Self: m.Self(), Links: m.Peers(), Routes: m.routes.Snapshot(), At: time.Now().UnixMilli()}
}

// Settings are what of the mesh a role's config holds: the listener and the
// dial targets, passwords included.
type Settings struct {
	Listen *model.ListenConfig
	Peers  []model.DialReq
}

// Editor changes the role's config with fn, which is given the mesh's
// settings as the config has them, and waits until the config changed has
// been applied, or ctx ends; Configure then has moved the mesh. It is how a
// change made from the pages is written back: the config is the one source
// of the settings, and the mesh follows it.
type Editor func(ctx context.Context, fn func(s *Settings) error) error

// RegisterAdmin adds the mesh management methods (MethodPeers, MethodStatus,
// MethodDial, MethodUndial, MethodKick, MethodListen) to r, for model.RoleLocal only (the
// role's own pages). Every change goes through edit, into the config; Kick,
// which drops links without changing anything, is the one that acts at once.
// A request that is not valid changes nothing.
func RegisterAdmin(r *Router, m *Mesh, edit Editor) {
	Handle(r, model.MethodPeers, nil, 0, func(context.Context, model.Identity, *struct{}) (any, error) {
		return m.PeersResp(), nil
	})
	Handle(r, model.MethodStatus, nil, 0, func(context.Context, model.Identity, *struct{}) (any, error) {
		return m.Status(), nil
	})
	Handle(r, model.MethodDial, nil, 0, func(ctx context.Context, _ model.Identity, req *model.DialReq) (any, error) {
		addr, err := ValidateAddr(req.Addr, true)
		if err != nil {
			return nil, err
		}
		if err := m.ValidateRole(req.Role); err != nil {
			return nil, err
		}
		err = edit(ctx, func(s *Settings) error {
			for i, p := range s.Peers {
				if p.Addr == addr {
					if req.Password != "" {
						s.Peers[i].Password = req.Password
					}
					s.Peers[i].Role = req.Role
					return nil
				}
			}
			if req.Password == "" {
				return BadRequest("password required")
			}
			if len(s.Peers) >= MaxTargets {
				return BadRequest("at most %d dial targets", MaxTargets)
			}
			s.Peers = append(s.Peers, model.DialReq{Addr: addr, Password: req.Password, Role: req.Role})
			return nil
		})
		if err != nil {
			return nil, err
		}
		return m.PeersResp(), nil
	})
	Handle(r, model.MethodUndial, nil, 0, func(ctx context.Context, _ model.Identity, req *model.UndialReq) (any, error) {
		err := edit(ctx, func(s *Settings) error {
			for i, p := range s.Peers {
				if p.Addr == req.Addr {
					s.Peers = append(s.Peers[:i:i], s.Peers[i+1:]...)
					return nil
				}
			}
			return Errorf(model.CodeNotFound, "no dial target %s", req.Addr)
		})
		if err != nil {
			return nil, err
		}
		return m.PeersResp(), nil
	})
	Handle(r, model.MethodKick, nil, 0, func(_ context.Context, _ model.Identity, req *model.KickReq) (any, error) {
		n := m.Kick(req.ID)
		m.log(slog.LevelInfo, "mesh: peer kicked", "id", req.ID, "conns", n)
		return map[string]int{"closed": n}, nil
	})
	Handle(r, model.MethodListen, nil, 0, func(ctx context.Context, _ model.Identity, req *model.ListenReq) (any, error) {
		cfg := *req
		if cfg.Addr != "" {
			addr, err := ValidateAddr(cfg.Addr, false)
			if err != nil {
				return nil, err
			}
			cfg.Addr = addr
		}
		err := edit(ctx, func(s *Settings) error {
			if cfg.Addr == "" {
				s.Listen = nil
				return nil
			}
			if cfg.Password == "" && s.Listen != nil {
				cfg.Password = s.Listen.Password
			}
			if cfg.Password == "" {
				return BadRequest("a listener needs a password")
			}
			c := cfg
			s.Listen = &c
			return nil
		})
		if err != nil {
			return nil, err
		}
		if e := m.Listen().Error; e != "" {
			// The setting is kept, as written; the pages show why it does
			// not listen.
			return nil, Errorf(model.CodeInternal, "%s", e)
		}
		return m.PeersResp(), nil
	})
}

// MaxTargets bounds the dial targets of a mesh, as config.MaxLLMPeers does.
const MaxTargets = 64
