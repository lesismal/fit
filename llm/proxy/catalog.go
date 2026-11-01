package proxy

import (
	"sort"
	"sync"
	"sync/atomic"

	"github.com/lesismal/fit/llm/internal/model"
)

// Route kinds: a directly connected node, or a hub that picks a node itself.
// 路由类型：直连的节点，或自行选择节点的 hub。
const (
	RouteNode = "node"
	RouteHub  = "hub"
)

// Route is one way to reach a model.
// Route 到达某个模型的一条路径。
type Route struct {
	Kind  string `json:"kind"` // RouteNode | RouteHub
	ID    string `json:"id"`
	Name  string `json:"name"`
	Ready bool   `json:"ready"`
	// Inflight: calls in progress on that node for the model (node), as last pushed plus the ones this proxy
	// started since.
	// Inflight：该节点上此模型的进行中调用（node），即最近推送的值加上本 proxy 之后发起的调用。
	Inflight int `json:"inflight"`
	// Nodes / ReadyNodes: how many nodes behind the hub serve the model, and how many are ready (hub).
	// Nodes / ReadyNodes：hub 背后提供该模型的节点数与就绪数（hub）。
	Nodes      int `json:"nodes,omitempty"`
	ReadyNodes int `json:"ready_nodes,omitempty"`
}

// CatalogModel is one model name and format with every route to it (MethodProxyModels).
// CatalogModel 一个模型名称与格式及其全部路由（MethodProxyModels）。
type CatalogModel struct {
	Name   string  `json:"name"`
	API    string  `json:"api"`
	Ready  bool    `json:"ready"`
	Routes []Route `json:"routes"`
}

type hubEntry struct {
	peer   model.Identity
	models model.HubModels
}

type nodeEntry struct {
	peer model.Identity
	info model.NodeInfo
}

// catalog is what the connected hubs and nodes offer.
// catalog 已连接的 hub 与节点所提供的模型。
type catalog struct {
	mu       sync.Mutex
	hubs     map[string]*hubEntry
	nodes    map[string]*nodeEntry
	inflight map[string]int // calls this proxy runs on a direct node, per node id / 本 proxy 在直连节点上的调用数，按节点 ID
	rr       atomic.Uint64
}

func newCatalog() *catalog {
	return &catalog{hubs: map[string]*hubEntry{}, nodes: map[string]*nodeEntry{}, inflight: map[string]int{}}
}

// newer reports whether a message (peer, seq) replaces the stored one: always after a restart of the peer, otherwise
// only when not older (pushes may race with the fetch at connect time).
// newer 判断消息 (peer, seq) 是否替换已存的：对端重启后总是替换，否则只在不更旧时替换（推送可能与连接时的拉取交错）。
func newer(old model.Identity, oldSeq int64, peer model.Identity, seq int64) bool {
	return old.StartedAt != peer.StartedAt || seq >= oldSeq
}

func (c *catalog) setHub(peer model.Identity, hm model.HubModels) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.hubs[peer.ID]; e != nil && !newer(e.peer, e.models.Seq, peer, hm.Seq) {
		return false
	}
	c.hubs[peer.ID] = &hubEntry{peer: peer, models: hm}
	return true
}

func (c *catalog) setNode(peer model.Identity, info model.NodeInfo) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.nodes[peer.ID]; e != nil && !newer(e.peer, e.info.Seq, peer, info.Seq) {
		return false
	}
	c.nodes[peer.ID] = &nodeEntry{peer: peer, info: info}
	return true
}

func (c *catalog) drop(id string) {
	c.mu.Lock()
	delete(c.hubs, id)
	delete(c.nodes, id)
	c.mu.Unlock()
}

// addInflight counts a call this proxy runs on direct node id.
// addInflight 记录本 proxy 在直连节点 id 上运行的调用。
func (c *catalog) addInflight(id string, d int) {
	c.mu.Lock()
	if c.inflight[id] += d; c.inflight[id] <= 0 {
		delete(c.inflight, id)
	}
	c.mu.Unlock()
}

// list returns every model with all its routes, sorted by name and API.
// list 返回全部模型及其所有路由，按名称与格式排序。
func (c *catalog) list() []CatalogModel {
	c.mu.Lock()
	defer c.mu.Unlock()
	type key struct{ name, api string }
	byKey := map[key]*CatalogModel{}
	get := func(name, api string) *CatalogModel {
		k := key{name, api}
		if byKey[k] == nil {
			byKey[k] = &CatalogModel{Name: name, API: api, Routes: []Route{}}
		}
		return byKey[k]
	}
	for id, e := range c.nodes {
		for _, m := range e.info.Models {
			cm := get(m.Name, m.API)
			r := Route{Kind: RouteNode, ID: id, Name: e.peer.Name, Ready: m.Ready(), Inflight: m.Inflight}
			cm.Routes = append(cm.Routes, r)
			cm.Ready = cm.Ready || r.Ready
		}
	}
	for id, e := range c.hubs {
		for _, m := range e.models.Models {
			cm := get(m.Name, m.API)
			r := Route{Kind: RouteHub, ID: id, Name: e.peer.Name, Ready: m.Ready > 0, Nodes: len(m.Nodes), ReadyNodes: m.Ready}
			cm.Routes = append(cm.Routes, r)
			cm.Ready = cm.Ready || r.Ready
		}
	}
	out := make([]CatalogModel, 0, len(byKey))
	for _, cm := range byKey {
		sort.Slice(cm.Routes, func(i, j int) bool {
			a, b := cm.Routes[i], cm.Routes[j]
			if a.Kind != b.Kind {
				return a.Kind == RouteNode
			}
			return a.Name+a.ID < b.Name+b.ID
		})
		out = append(out, *cm)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].API < out[j].API
	})
	return out
}

// routes returns the ready routes to name, called with protocol api (a model whose kind speaks it), in the order to
// try them: direct nodes by least inflight, then hubs round robin. known reports whether the name exists at all (any
// API, ready or not); other is an API the name is called with when it is not called with api.
//
// routes 按尝试顺序返回以接口 api 调用 name（其种类讲该接口的模型）的就绪路由：先是按在途数最少排序的直连节点，再是轮询的 hub。known 表示该名称是否存在
// （任意格式、无论是否就绪）；该名称在 api 下不存在但在其他格式下存在时，other 为那个格式。
func (c *catalog) routes(name, api string) (out []Route, known bool, other string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	note := func(mapi string) {
		known = true
		if mapi != api && other == "" {
			other = mapi
		}
	}
	sameAPI := false
	for id, e := range c.nodes {
		for _, m := range e.info.Models {
			if m.Name != name {
				continue
			}
			note(model.Protocol(m.API))
			if model.Protocol(m.API) != api {
				continue
			}
			sameAPI = true
			if m.Ready() {
				out = append(out, Route{Kind: RouteNode, ID: id, Name: e.peer.Name, Ready: true, Inflight: m.Inflight + c.inflight[id]})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Inflight != out[j].Inflight {
			return out[i].Inflight < out[j].Inflight
		}
		return out[i].ID < out[j].ID
	})
	var hubs []Route
	for id, e := range c.hubs {
		for _, m := range e.models.Models {
			if m.Name != name {
				continue
			}
			note(model.Protocol(m.API))
			if model.Protocol(m.API) != api {
				continue
			}
			sameAPI = true
			if m.Ready > 0 {
				hubs = append(hubs, Route{Kind: RouteHub, ID: id, Name: e.peer.Name, Ready: true, Nodes: len(m.Nodes), ReadyNodes: m.Ready})
			}
		}
	}
	if len(hubs) > 0 {
		sort.Slice(hubs, func(i, j int) bool { return hubs[i].ID < hubs[j].ID })
		start := int(c.rr.Add(1) % uint64(len(hubs)))
		out = append(out, hubs[start:]...)
		out = append(out, hubs[:start]...)
	}
	if sameAPI {
		other = ""
	}
	return out, known, other
}
