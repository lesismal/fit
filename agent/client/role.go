// Package client is the agent's client role, agent.client in fit's config:
// it links to hubs, and to nodes directly, keeps a catalog of the models
// they offer, and serves an OpenAI- and Anthropic-compatible API (/v1/...)
// on an address of its own for local agents, behind API keys, forwarding
// each call byte for byte over the mesh. See docs/agent.md.
package client

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

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
		ID: "agent.client",
		Member: func(c *config.Config) any {
			if c.Agent == nil || c.Agent.Client == nil {
				return nil
			}
			return c.Agent.Client
		},
		New: func(h *app.Host) (app.Role, error) { return New(h), nil },
	})
}

// Client local-only methods (the client's own pages).
const (
	MethodClientModels     = "/client/models"      // -> []CatalogModel
	MethodClientStats      = "/client/stats"       // -> StatsResp
	MethodClientKeysList   = "/client/keys/list"   // -> []KeyView
	MethodClientKeysCreate = "/client/keys/create" // KeyCreateReq -> KeyCreateResp (the full key, shown once)
	MethodClientKeysDelete = "/client/keys/delete" // KeyIDReq
	MethodClientProfileSet = "/client/profile/set" // model.ProfileReq (name only)
)

// Client is a running client.
type Client struct {
	*role.Base
	cat   *catalog
	keys  *keyStore
	stats *stats
	// maxAttempts bounds the routes one call tries.
	maxAttempts int

	mu     sync.Mutex
	api    *apiServer
	apiKey []byte
}

// New returns a client on h, started by its first Apply.
func New(h *app.Host) *Client {
	pages, _ := fs.Sub(uiFS, "ui")
	c := &Client{cat: newCatalog(), keys: &keyStore{}, stats: newStats(), maxAttempts: 4}
	c.Base = role.New(h, role.Spec{Role: model.RoleClient, Pages: pages, Me: c.me})
	c.Register(c)
	m, r := c.Mesh, c.Router
	m.OnPeerUp(c.peerUp)
	m.OnPeerDown(func(p model.PeerConn) {
		if !m.Online(p.ID) {
			c.cat.drop(p.ID)
		}
	})
	m.OnPush(model.MethodHubModelsUpdate, []string{model.RoleHub}, func(caller model.Identity, msg json.RawMessage) {
		var hm model.HubModels
		if err := json.Unmarshal(msg, &hm); err != nil {
			c.Log.Warnf("bad hub models push from %s: %v", caller.ID, err)
			return
		}
		c.cat.setHub(caller, hm)
	})
	m.OnPush(model.MethodNodeUpdate, []string{model.RoleNode}, func(caller model.Identity, msg json.RawMessage) {
		var info model.NodeInfo
		if err := json.Unmarshal(msg, &info); err != nil {
			c.Log.Warnf("bad node info push from %s: %v", caller.ID, err)
			return
		}
		c.cat.setNode(caller, info)
	})
	mesh.Handle(r, MethodClientModels, nil, 0, func(context.Context, model.Identity, *struct{}) (any, error) {
		return c.cat.list(), nil
	})
	mesh.Handle(r, MethodClientStats, nil, 0, func(context.Context, model.Identity, *struct{}) (any, error) {
		return c.stats.snapshot(), nil
	})
	mesh.Handle(r, MethodClientKeysList, nil, 0, func(context.Context, model.Identity, *struct{}) (any, error) {
		return c.keys.list(), nil
	})
	mesh.Handle(r, MethodClientKeysCreate, nil, role.EditTimeout, c.createKey)
	mesh.Handle(r, MethodClientKeysDelete, nil, role.EditTimeout, c.deleteKey)
	mesh.Handle(r, MethodClientProfileSet, nil, role.EditTimeout, func(ctx context.Context, _ model.Identity, req *model.ProfileReq) (any, error) {
		name := strings.TrimSpace(req.Name)
		if name == "" || utf8.RuneCountInString(name) > 64 {
			return nil, mesh.BadRequest("name must have 1 to 64 characters")
		}
		return struct{}{}, c.SetName(ctx, name)
	})
	return c
}

func (c *Client) me() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.api == nil {
		return nil
	}
	return map[string]any{"api_url": c.api.url()}
}

// API returns the URL of the agent API, or "" when there is none.
func (c *Client) API() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.api == nil {
		return ""
	}
	return c.api.url()
}

// Apply moves the client to member, a *config.AgentClient: its keys, its
// API, and the base's identity, mesh and pages.
func (c *Client) Apply(member any) error {
	m, ok := member.(*config.AgentClient)
	if !ok || m == nil {
		return errors.New("agent.client: no member")
	}
	c.keys.set(m.APIKeys)
	if err := c.applyAPI(m.API); err != nil {
		return err
	}
	return c.Base.Apply(role.Common{ID: m.ID, Name: m.Name, Console: m.Console, Listen: m.Listen, Peers: m.Peers, Limits: m.Limits})
}

// applyAPI starts, changes or stops the agent API.
func (c *Client) applyAPI(cfg *config.AgentClientAPI) error {
	var k []byte
	if cfg != nil {
		k, _ = json.Marshal(cfg)
	}
	c.mu.Lock()
	same := bytes.Equal(k, c.apiKey) && (cfg == nil) == (c.api == nil)
	old := c.api
	c.mu.Unlock()
	if same {
		return nil
	}
	if old != nil {
		old.stop()
	}
	var s *apiServer
	if cfg != nil {
		var err error
		if s, err = startAPI(c, cfg); err != nil {
			c.mu.Lock()
			c.api, c.apiKey = nil, nil
			c.mu.Unlock()
			return err
		}
	}
	c.mu.Lock()
	c.api, c.apiKey = s, k
	c.mu.Unlock()
	return nil
}

// Stop ends the client: its API stops, and its links drain until ctx ends,
// for 10s at most.
func (c *Client) Stop(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	c.mu.Lock()
	s := c.api
	c.api = nil
	c.mu.Unlock()
	if s != nil {
		s.stop()
	}
	c.Base.Stop(ctx)
}

// peerUp fetches what a new hub or node offers.
func (c *Client) peerUp(p model.PeerConn) {
	ctx, cancel := context.WithTimeout(context.Background(), mesh.DefaultTimeout)
	defer cancel()
	switch p.Role {
	case model.RoleHub:
		hm, err := mesh.Call[model.HubModels](ctx, c.Mesh, p.ID, model.MethodHubModels, struct{}{})
		if err != nil {
			c.Log.Warnf("fetching the models of hub %s failed: %v", p.ID, err)
			return
		}
		if c.Mesh.Online(p.ID) {
			c.cat.setHub(p.Identity, hm)
		}
	case model.RoleNode:
		info, err := mesh.Call[model.NodeInfo](ctx, c.Mesh, p.ID, model.MethodNodeInfo, struct{}{})
		if err != nil {
			c.Log.Warnf("fetching the info of node %s failed: %v", p.ID, err)
			return
		}
		if c.Mesh.Online(p.ID) {
			c.cat.setNode(p.Identity, info)
		}
	}
}

// editKeys changes the keys of the client's member, and waits for the
// config to be applied.
func (c *Client) editKeys(ctx context.Context, fn func(keys []config.AgentClientKey) ([]config.AgentClientKey, error)) error {
	return c.Edit(ctx, func(m map[string]json.RawMessage) error {
		var keys []config.AgentClientKey
		if raw, ok := m["api_keys"]; ok {
			if err := json.Unmarshal(raw, &keys); err != nil {
				return err
			}
		}
		keys, err := fn(keys)
		if err != nil {
			return err
		}
		if len(keys) > config.MaxAgentKeys {
			return mesh.BadRequest("at most %d keys", config.MaxAgentKeys)
		}
		return role.Set(m, "api_keys", keys)
	})
}

func (c *Client) createKey(ctx context.Context, _ model.Identity, req *KeyCreateReq) (any, error) {
	k, secret, err := newKey(req.Name)
	if err != nil {
		return nil, err
	}
	if err := c.editKeys(ctx, func(keys []config.AgentClientKey) ([]config.AgentClientKey, error) {
		return append(keys, k), nil
	}); err != nil {
		return nil, err
	}
	c.Log.Infof("api key %s (%s) created", k.ID, k.Name)
	return KeyCreateResp{KeyView: KeyView{ID: k.ID, Name: k.Name, Prefix: k.Prefix, CreatedAt: k.CreatedAt}, Key: secret}, nil
}

func (c *Client) deleteKey(ctx context.Context, _ model.Identity, req *KeyIDReq) (any, error) {
	err := c.editKeys(ctx, func(keys []config.AgentClientKey) ([]config.AgentClientKey, error) {
		for i, k := range keys {
			if k.ID == req.ID {
				return append(keys[:i:i], keys[i+1:]...), nil
			}
		}
		return nil, mesh.Errorf(model.CodeNotFound, "no api key %q", req.ID)
	})
	if err != nil {
		return nil, err
	}
	c.Log.Infof("api key %s deleted", req.ID)
	return struct{}{}, nil
}
