// Package hub is the hub role: it keeps the table of its nodes (what they push), aggregates their models for its
// clients, relays the clients' model calls to the least loaded node serving the model (retrying elsewhere while no
// byte was relayed), and lets its console manage the nodes that allow it.
//
// Package hub 是 hub 角色：维护其节点表（节点推送的信息），为其 client 汇总节点的模型，把 client 的模型调用中继给提供该模型、
// 负载最低的节点（尚未中继任何字节时换节点重试），并让控制台代管允许代管的节点。
package hub

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/lesismal/fit/agent/internal/mesh"
	"github.com/lesismal/fit/agent/internal/model"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
)

// coalesce is how long model changes are collected before one MethodHubModelsUpdate push.
// coalesce 合并模型变化、发出一次 MethodHubModelsUpdate 推送前的等待时间。
const coalesce = 200 * time.Millisecond

// infoTimeout bounds the MethodNodeInfo call made when a node connects.
// infoTimeout 节点连接时调用 MethodNodeInfo 的时限。
const infoTimeout = 10 * time.Second

// node is one entry of the node table.
// node 节点表中的一项。
type node struct {
	info     model.NodeInfo
	started  int64 // StartedAt of the process that sent info / 发送 info 的进程的 StartedAt
	lastSeen int64
}

// Hub is the hub role's state.
// Hub hub 角色的状态。
type Hub struct {
	m        *mesh.Mesh
	self     func() model.Identity
	log      *logs.Logger
	maxNodes int

	mu       sync.Mutex
	nodes    map[string]*node
	models   model.HubModels
	inflight map[string]int // streams this hub relays to each node / 本 hub 正在中继到各节点的流
	rr       int
	timer    *time.Timer // pending coalesced flush / 待执行的合并刷新
	closed   bool
}

// newHub creates a Hub on m; call register before the mesh links.
func newHub(m *mesh.Mesh, self func() model.Identity, log *logs.Logger) *Hub {
	h := &Hub{m: m, self: self, log: log, maxNodes: config.DefaultAgentMaxNodes, nodes: map[string]*node{}, inflight: map[string]int{}}
	h.models = model.HubModels{Models: []model.HubModel{}, Seq: time.Now().UnixMilli()}
	return h
}

func (h *Hub) setMaxNodes(n int) {
	h.mu.Lock()
	h.maxNodes = n
	h.pruneLocked()
	h.mu.Unlock()
}

// pruneLocked forgets the offline nodes seen longest ago while the table is
// over its bound. h.mu is held.
func (h *Hub) pruneLocked() {
	for len(h.nodes) > h.maxNodes {
		var oldest string
		var at int64
		for id, n := range h.nodes {
			if h.m.Online(id) {
				continue
			}
			if oldest == "" || n.lastSeen < at {
				oldest, at = id, n.lastSeen
			}
		}
		if oldest == "" {
			return // online nodes are bounded by the links
		}
		delete(h.nodes, oldest)
	}
}

// register adds the hub's methods and pushes to r and wires the mesh peer events.
func (h *Hub) register(r *mesh.Router) {
	clients := []string{model.RoleClient}
	mesh.Handle(r, model.MethodHubModels, clients, 0, func(context.Context, model.Identity, *struct{}) (any, error) {
		return h.Models(), nil
	})
	mesh.HandleStream(r, model.MethodHubProxy, clients, h.proxy)
	mesh.Handle(r, model.MethodHubNodes, nil, 0, func(context.Context, model.Identity, *struct{}) (any, error) {
		return h.Nodes(), nil
	})
	mesh.Handle(r, model.MethodHubNodeCall, nil, nodeCallTimeout, h.nodeCall)
	mesh.HandleStream(r, model.MethodHubNodeStream, nil, h.nodeStream)

	h.m.OnPush(model.MethodNodeUpdate, []string{model.RoleNode}, func(caller model.Identity, msg json.RawMessage) {
		var info model.NodeInfo
		if err := json.Unmarshal(msg, &info); err != nil {
			h.log.Warnf("bad node update from %s: %v", caller.ID, err)
			return
		}
		h.apply(caller, info)
	})
	h.m.OnPeerUp(h.peerUp)
	h.m.OnPeerDown(h.peerDown)
}

// Close stops the pending flush.
// Close 停止待执行的刷新。
func (h *Hub) close() {
	h.mu.Lock()
	h.closed = true
	if h.timer != nil {
		h.timer.Stop()
	}
	h.mu.Unlock()
}

// ---- Node table ----
// ---- 节点表 ----

func (h *Hub) peerUp(pc model.PeerConn) {
	switch pc.Role {
	case model.RoleNode:
		ctx, cancel := context.WithTimeout(context.Background(), infoTimeout)
		defer cancel()
		info, err := mesh.Call[model.NodeInfo](ctx, h.m, pc.ID, model.MethodNodeInfo, nil)
		if err != nil {
			h.log.Warnf("node info of %s failed: %v", pc.ID, err)
			h.touch(pc.Identity)
			return
		}
		h.apply(pc.Identity, info)
	case model.RoleClient:
		if err := h.m.Push(pc.ID, model.MethodHubModelsUpdate, h.Models()); err != nil {
			h.log.Debugf("models push to %s failed: %v", pc.ID, err)
		}
	}
}

func (h *Hub) peerDown(pc model.PeerConn) {
	if pc.Role != model.RoleNode || h.m.Online(pc.ID) {
		return
	}
	h.mu.Lock()
	if n := h.nodes[pc.ID]; n != nil {
		n.lastSeen = time.Now().Unix()
	}
	h.mu.Unlock()
	h.changed()
}

// touch makes sure a connected node has an entry even when its info could not be read.
// touch 确保已连接的节点即使读取信息失败也有表项。
func (h *Hub) touch(id model.Identity) {
	h.mu.Lock()
	n := h.nodes[id.ID]
	isNew := n == nil
	if isNew {
		h.nodes[id.ID] = &node{info: model.NodeInfo{ID: id.ID, Name: id.Name}, lastSeen: time.Now().Unix()}
		h.pruneLocked()
	}
	h.mu.Unlock()
}

// apply records a NodeInfo from the node caller. Within one process run (same StartedAt) a Seq not above the
// current one is stale and dropped; a new StartedAt means the node restarted, so its Seq starts over.
// apply 记录来自节点 caller 的 NodeInfo。同一次进程运行内（StartedAt 相同）Seq 不大于当前值的是旧信息，丢弃；
// StartedAt 变化表示节点已重启，其 Seq 重新开始。
func (h *Hub) apply(caller model.Identity, info model.NodeInfo) {
	info.ID = caller.ID
	if info.Name == "" {
		info.Name = caller.Name
	}
	h.mu.Lock()
	n := h.nodes[caller.ID]
	if n == nil {
		n = &node{}
		h.nodes[caller.ID] = n
		h.pruneLocked()
	}
	if n.started == caller.StartedAt && n.info.Seq != 0 && info.Seq <= n.info.Seq {
		h.mu.Unlock()
		return
	}
	n.info, n.started, n.lastSeen = info, caller.StartedAt, time.Now().Unix()
	h.mu.Unlock()
	h.changed()
}

// Nodes lists the node table for the console: online nodes first, then by name.
// Nodes 为控制台列出节点表：在线节点在前，再按名称排序。
func (h *Hub) Nodes() []model.HubNode {
	conns := map[string][]model.PeerConn{}
	for _, p := range h.m.Peers() {
		if p.Role == model.RoleNode {
			conns[p.ID] = append(conns[p.ID], p)
		}
	}
	now := time.Now().Unix()
	h.mu.Lock()
	out := make([]model.HubNode, 0, len(h.nodes))
	for id, n := range h.nodes {
		hn := model.HubNode{Info: n.info, Online: h.m.Online(id), Conns: conns[id], LastSeen: n.lastSeen}
		if hn.Online {
			hn.LastSeen = now
		}
		out = append(out, hn)
	}
	h.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Online != out[j].Online {
			return out[i].Online
		}
		if out[i].Info.Name != out[j].Info.Name {
			return out[i].Info.Name < out[j].Info.Name
		}
		return out[i].Info.ID < out[j].Info.ID
	})
	return out
}

// ---- Model aggregation ----
// ---- 模型汇总 ----

// changed schedules a coalesced recompute (and push) of the models.
// changed 安排一次合并的模型重新计算（及推送）。
func (h *Hub) changed() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || h.timer != nil {
		return
	}
	h.timer = time.AfterFunc(coalesce, func() {
		h.mu.Lock()
		h.timer = nil
		h.mu.Unlock()
		h.Models()
	})
}

// aggregate builds the model list of the online nodes from the table (h.mu held).
// aggregate 由节点表构建在线节点的模型列表（持有 h.mu）。
func (h *Hub) aggregate() []model.HubModel {
	type key struct{ name, api string }
	idx := map[key]*model.HubModel{}
	for id, n := range h.nodes {
		if !h.m.Online(id) {
			continue
		}
		for _, mv := range n.info.Models {
			if mv.State == model.ModelDisabled {
				continue
			}
			k := key{mv.Name, mv.API}
			hm := idx[k]
			if hm == nil {
				hm = &model.HubModel{Name: mv.Name, API: mv.API}
				idx[k] = hm
			}
			if !slices.Contains(hm.Nodes, id) {
				hm.Nodes = append(hm.Nodes, id)
			}
			if mv.Ready() {
				hm.Ready++
			}
		}
	}
	out := make([]model.HubModel, 0, len(idx))
	for _, hm := range idx {
		sort.Strings(hm.Nodes)
		out = append(out, *hm)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].API < out[j].API
	})
	return out
}

// Models recomputes the model list; when its content changed, Seq increases and every client gets it pushed.
// Models 重新计算模型列表；内容变化时 Seq 递增，并推送给每个 client。
func (h *Hub) Models() model.HubModels {
	h.mu.Lock()
	list := h.aggregate()
	if reflect.DeepEqual(list, h.models.Models) {
		cur := h.models
		h.mu.Unlock()
		cur.Hub = h.self().ID
		return cur
	}
	h.models = model.HubModels{Hub: h.self().ID, Models: list, Seq: max(h.models.Seq+1, time.Now().UnixMilli())}
	cur := h.models
	h.mu.Unlock()
	for _, c := range h.m.PeersOf(model.RoleClient) {
		if err := h.m.Push(c.ID, model.MethodHubModelsUpdate, cur); err != nil {
			h.log.Debugf("models push to %s failed: %v", c.ID, err)
		}
	}
	return cur
}
