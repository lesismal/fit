// Package proxy is the LLM feature's proxy role, llm.proxy in fit's config:
// it links to hubs, often a public one, and to nodes directly, keeps a
// catalog of the models they offer, and serves the model APIs agents speak
// (OpenAI, Anthropic, Gemini, Ollama, Cohere) on an address of its own, to
// agents on its machine or, on a LAN address with TLS, its LAN, behind API
// keys, forwarding each call byte for byte over the mesh. See docs/llm.md.
package proxy

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

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/llm/internal/mesh"
	"github.com/lesismal/fit/llm/internal/model"
	"github.com/lesismal/fit/llm/internal/role"
)

//go:embed ui
var uiFS embed.FS

func init() {
	app.Register(app.Info{
		ID: "llm.proxy",
		Member: func(c *config.Config) any {
			if c.LLM == nil || c.LLM.Proxy == nil {
				return nil
			}
			return c.LLM.Proxy
		},
		New: func(h *app.Host) (app.Role, error) { return New(h), nil },
	})
}

// Proxy local-only methods (the proxy's own pages).
const (
	MethodProxyModels     = "/proxy/models"      // -> []CatalogModel
	MethodProxyStats      = "/proxy/stats"       // -> StatsResp
	MethodProxyKeysList   = "/proxy/keys/list"   // -> []KeyView
	MethodProxyKeysCreate = "/proxy/keys/create" // KeyCreateReq -> KeyCreateResp (the full key, shown once)
	MethodProxyKeysDelete = "/proxy/keys/delete" // KeyIDReq
	MethodProxyProfileSet = "/proxy/profile/set" // model.ProfileReq (name only)
)

// Proxy is a running proxy.
type Proxy struct {
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

// New returns a proxy on h, started by its first Apply.
func New(h *app.Host) *Proxy {
	pages, _ := fs.Sub(uiFS, "ui")
	c := &Proxy{cat: newCatalog(), keys: &keyStore{}, stats: newStats(), maxAttempts: 4}
	c.Base = role.New(h, role.Spec{Role: model.RoleProxy, Pages: pages, Me: c.me})
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
	mesh.Handle(r, MethodProxyModels, nil, 0, func(context.Context, model.Identity, *struct{}) (any, error) {
		return c.cat.list(), nil
	})
	mesh.Handle(r, MethodProxyStats, nil, 0, func(context.Context, model.Identity, *struct{}) (any, error) {
		return c.stats.snapshot(), nil
	})
	mesh.Handle(r, MethodProxyKeysList, nil, 0, func(context.Context, model.Identity, *struct{}) (any, error) {
		return c.keys.list(), nil
	})
	mesh.Handle(r, MethodProxyKeysCreate, nil, role.EditTimeout, c.createKey)
	mesh.Handle(r, MethodProxyKeysDelete, nil, role.EditTimeout, c.deleteKey)
	mesh.Handle(r, MethodProxyProfileSet, nil, role.EditTimeout, func(ctx context.Context, _ model.Identity, req *model.ProfileReq) (any, error) {
		name := strings.TrimSpace(req.Name)
		if name == "" || utf8.RuneCountInString(name) > 64 {
			return nil, mesh.BadRequest("name must have 1 to 64 characters")
		}
		return struct{}{}, c.SetName(ctx, name)
	})
	return c
}

func (c *Proxy) me() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.api == nil {
		return nil
	}
	return map[string]any{"api_url": c.api.url()}
}

// API returns the URL of the agent API, or "" when there is none.
func (c *Proxy) API() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.api == nil {
		return ""
	}
	return c.api.url()
}

// Apply moves the proxy to member, a *config.LLMProxy: its keys, its
// API, and the base's identity, mesh and pages.
func (c *Proxy) Apply(member any) error {
	m, ok := member.(*config.LLMProxy)
	if !ok || m == nil {
		return errors.New("llm.proxy: no member")
	}
	c.keys.set(m.APIKeys)
	if err := c.applyAPI(m.API); err != nil {
		return err
	}
	return c.Base.Apply(role.Common{ID: m.ID, Name: m.Name, Console: m.Console, Listen: m.Listen, Peers: m.Peers, Limits: m.Limits})
}

// applyAPI starts, changes or stops the agent API.
func (c *Proxy) applyAPI(cfg *config.LLMProxyAPI) error {
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

// Stop ends the proxy: its API stops, and its links drain until ctx ends,
// for 10s at most.
func (c *Proxy) Stop(ctx context.Context) {
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
func (c *Proxy) peerUp(p model.PeerConn) {
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

// editKeys changes the keys of the proxy's member, and waits for the
// config to be applied.
func (c *Proxy) editKeys(ctx context.Context, fn func(keys []config.LLMProxyKey) ([]config.LLMProxyKey, error)) error {
	return c.Edit(ctx, func(m map[string]json.RawMessage) error {
		var keys []config.LLMProxyKey
		if raw, ok := m["api_keys"]; ok {
			if err := json.Unmarshal(raw, &keys); err != nil {
				return err
			}
		}
		keys, err := fn(keys)
		if err != nil {
			return err
		}
		if len(keys) > config.MaxLLMKeys {
			return mesh.BadRequest("at most %d keys", config.MaxLLMKeys)
		}
		return role.Set(m, "api_keys", keys)
	})
}

func (c *Proxy) createKey(ctx context.Context, _ model.Identity, req *KeyCreateReq) (any, error) {
	k, secret, err := newKey(req.Name)
	if err != nil {
		return nil, err
	}
	if err := c.editKeys(ctx, func(keys []config.LLMProxyKey) ([]config.LLMProxyKey, error) {
		return append(keys, k), nil
	}); err != nil {
		return nil, err
	}
	c.Log.Infof("api key %s (%s) created", k.ID, k.Name)
	return KeyCreateResp{KeyView: KeyView{ID: k.ID, Name: k.Name, Prefix: k.Prefix, CreatedAt: k.CreatedAt}, Key: secret}, nil
}

func (c *Proxy) deleteKey(ctx context.Context, _ model.Identity, req *KeyIDReq) (any, error) {
	err := c.editKeys(ctx, func(keys []config.LLMProxyKey) ([]config.LLMProxyKey, error) {
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
