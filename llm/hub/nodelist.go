package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/llm/internal/model"
	"github.com/lesismal/fit/llm/internal/role"
)

// The pace of the node list's pushes: a change goes out after coalesce, but
// no sooner than listGap after the last push, and the traffic and latency,
// which change all the time, every listRefresh at most.
const (
	listGap     = time.Second
	listRefresh = 5 * time.Second
	// maxListGPUs bounds the GPUs a node of the list shows.
	maxListGPUs = 16
)

// summary is the node n as the list shows it.
func summary(n model.HubNode, traffic model.NodeTraffic) model.NodeSummary {
	hw := n.Info.HW
	hw.Disks, hw.IPs = nil, nil
	if len(hw.GPUs) > maxListGPUs {
		hw.GPUs = hw.GPUs[:maxListGPUs]
	}
	ms := n.Info.Models
	if len(ms) > model.MaxNodeListModels {
		ms = ms[:model.MaxNodeListModels]
	}
	s := model.NodeSummary{ID: n.Info.ID, Name: n.Info.Name, Description: n.Info.Description, Online: n.Online,
		LastSeen: n.LastSeen, HW: hw, Models: ms, ModelsTotal: len(n.Info.Models), Inflight: n.Info.Inflight,
		Jobs: n.Info.Jobs, Traffic: traffic}
	if s.Models == nil {
		s.Models = []model.ModelView{}
	}
	for _, c := range n.Conns {
		s.Links = append(s.Links, model.NodeLink{Direction: c.Direction, ConnectedAt: c.ConnectedAt, Draining: c.Draining, Link: c.Link})
	}
	return s
}

// encodeList encodes nodes within model.MaxNodeListBytes, leaving out the
// nodes at the end, offline ones first as Nodes sorts them, while it is
// larger.
func encodeList(nodes []model.NodeSummary) ([]model.NodeSummary, []byte) {
	for {
		b, _ := json.Marshal(nodes)
		if len(b) <= model.MaxNodeListBytes || len(nodes) == 0 {
			return nodes, b
		}
		nodes = nodes[:len(nodes)*3/4]
	}
}

// NodeList returns the list of the hub's nodes for its proxies. When it
// changed since the last one, its Seq increases and every proxy gets it
// pushed.
// NodeList 返回供 proxy 使用的 hub 节点列表。自上次以来有变化时 Seq 递增，并推送给每个 proxy。
func (h *Hub) NodeList() model.HubNodeList {
	traffic := map[string]model.NodeTraffic{}
	for _, r := range h.m.Routes().Snapshot() {
		t := traffic[r.To]
		t.Calls += r.Calls
		t.Errors += r.Errors
		t.Inflight += r.Inflight
		t.ReqBytes += r.ReqBytes
		t.RespBytes += r.RespBytes
		traffic[r.To] = t
	}
	all := h.Nodes()
	nodes := make([]model.NodeSummary, 0, len(all))
	for _, n := range all {
		nodes = append(nodes, summary(n, traffic[n.Info.ID]))
	}
	nodes, enc := encodeList(nodes)
	h.mu.Lock()
	if bytes.Equal(enc, h.listEnc) && len(all) == h.list.Total {
		cur := h.list
		h.mu.Unlock()
		cur.Hub = h.self().ID
		return cur
	}
	h.list = model.HubNodeList{Hub: h.self().ID, Nodes: nodes, Total: len(all), Seq: max(h.list.Seq+1, time.Now().UnixMilli())}
	h.listEnc, h.listAt = enc, time.Now()
	cur := h.list
	h.mu.Unlock()
	for _, c := range h.m.PeersOf(model.RoleProxy) {
		if err := h.m.Push(c.ID, model.MethodHubNodeListUpdate, cur); err != nil {
			h.log.Debugf("node list push to %s failed: %v", c.ID, err)
		}
	}
	return cur
}

// listChanged schedules a push of the node list: after coalesce, and no
// sooner than listGap after the last one.
// listChanged 安排一次节点列表推送：coalesce 之后，且距上次推送不少于 listGap。
func (h *Hub) listChanged() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || h.listTimer != nil {
		return
	}
	wait := max(coalesce, time.Until(h.listAt.Add(listGap)))
	h.listTimer = time.AfterFunc(wait, func() {
		h.mu.Lock()
		h.listTimer = nil
		h.mu.Unlock()
		h.NodeList()
	})
}

// refresh pushes the node list's traffic and latency every listRefresh,
// while proxies are linked, and arranges its next run.
func (h *Hub) refresh() {
	if len(h.m.PeersOf(model.RoleProxy)) > 0 {
		h.listChanged()
	}
	h.mu.Lock()
	if !h.closed {
		h.tick.Reset(listRefresh)
	}
	h.mu.Unlock()
}

// settings returns the hub's own settings.
func (h *Hub) settings() model.HubSettings {
	h.mu.Lock()
	defer h.mu.Unlock()
	return model.HubSettings{MaxNodes: h.maxNodes}
}

// setSettings writes the hub's own settings into its member.
func (r *Role) setSettings(ctx context.Context, _ model.Identity, req *model.HubSettings) (any, error) {
	n, err := role.Count("max_nodes", req.MaxNodes, config.DefaultLLMMaxNodes)
	if err != nil {
		return nil, err
	}
	if err := r.Edit(ctx, func(m map[string]json.RawMessage) error { return role.Set(m, "max_nodes", n) }); err != nil {
		return nil, err
	}
	return r.hub.settings(), nil
}
