package proxy

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/llm/internal/mesh"
	"github.com/lesismal/fit/llm/internal/model"
	"github.com/lesismal/fit/llm/internal/role"
)

// More of the proxy's local-only methods: what its hubs and nodes are, to
// see and not to manage, and its API's settings.
const (
	MethodProxyNodes  = "/proxy/node/list" // -> NodesResp
	MethodProxyAPIGet = "/proxy/api/get"   // -> APISettings
	MethodProxyAPISet = "/proxy/api/set"   // APISettings -> APISettings
)

// HubNodes is a hub the proxy links to and the nodes it listed last.
// HubNodes proxy 连接的一个 hub 及其最近列出的节点。
type HubNodes struct {
	Hub    model.Identity      `json:"hub"`
	Online bool                `json:"online"`
	Nodes  []model.NodeSummary `json:"nodes"`
	Total  int                 `json:"total"`
	Seq    int64               `json:"seq,omitempty"`
}

// NodesResp is the reply of MethodProxyNodes: the nodes behind each hub,
// and the nodes the proxy links to directly, whose traffic is the proxy's
// own calls to them.
// NodesResp MethodProxyNodes 的应答：每个 hub 背后的节点，以及 proxy 直连的节点（其流量为 proxy 自己发给它们的调用）。
type NodesResp struct {
	Hubs   []HubNodes          `json:"hubs"`
	Direct []model.NodeSummary `json:"direct"`
}

// nodes builds NodesResp from the catalog, the links and the routes.
func (c *Proxy) nodes() NodesResp {
	peers := c.Mesh.Peers()
	links := map[string][]model.NodeLink{}
	hubs := map[string]model.Identity{}
	for _, p := range peers {
		switch p.Role {
		case model.RoleNode:
			links[p.ID] = append(links[p.ID], model.NodeLink{Direction: p.Direction, ConnectedAt: p.ConnectedAt, Draining: p.Draining, Link: p.Link})
		case model.RoleHub:
			hubs[p.ID] = p.Identity
		}
	}
	traffic := map[string]model.NodeTraffic{}
	for _, r := range c.Mesh.Routes().Snapshot() {
		t := traffic[r.To]
		t.Calls += r.Calls
		t.Errors += r.Errors
		t.Inflight += r.Inflight
		t.ReqBytes += r.ReqBytes
		t.RespBytes += r.RespBytes
		traffic[r.To] = t
	}
	out := NodesResp{Hubs: []HubNodes{}, Direct: []model.NodeSummary{}}
	c.cat.mu.Lock()
	for id, e := range c.cat.lists {
		if _, ok := hubs[id]; !ok {
			hubs[id] = e.peer
		}
	}
	for id, peer := range hubs {
		hn := HubNodes{Hub: peer, Online: c.Mesh.Online(id), Nodes: []model.NodeSummary{}}
		if e := c.cat.lists[id]; e != nil {
			hn.Nodes, hn.Total, hn.Seq = e.list.Nodes, e.list.Total, e.list.Seq
		}
		out.Hubs = append(out.Hubs, hn)
	}
	for id, e := range c.cat.nodes {
		info := e.info
		hw := info.HW
		hw.Disks, hw.IPs = nil, nil
		out.Direct = append(out.Direct, model.NodeSummary{ID: id, Name: info.Name, Description: info.Description,
			Online: c.Mesh.Online(id), LastSeen: time.Now().Unix(), HW: hw, Models: info.Models, ModelsTotal: len(info.Models),
			Inflight: info.Inflight, Jobs: info.Jobs, Links: links[id], Traffic: traffic[id]})
	}
	c.cat.mu.Unlock()
	sort.Slice(out.Hubs, func(i, j int) bool {
		if out.Hubs[i].Hub.Name != out.Hubs[j].Hub.Name {
			return out.Hubs[i].Hub.Name < out.Hubs[j].Hub.Name
		}
		return out.Hubs[i].Hub.ID < out.Hubs[j].Hub.ID
	})
	sort.Slice(out.Direct, func(i, j int) bool {
		if out.Direct[i].Name != out.Direct[j].Name {
			return out.Direct[i].Name < out.Direct[j].Name
		}
		return out.Direct[i].ID < out.Direct[j].ID
	})
	return out
}

// APISettings are the proxy's API, as its pages read and set them
// (MethodProxyAPIGet, MethodProxyAPISet): an empty Address is no API;
// durations as Go writes them, "5m"; a zero or empty field is the default.
// A non-loopback Address needs CertFile and KeyFile, as the config does.
// APISettings proxy 的 API，供其页面读取与设置：Address 为空表示没有 API；时长按 Go 的格式书写，如 "5m"；零值或空字段
// 表示默认值。非回环地址需要 CertFile 与 KeyFile，与配置的要求相同。
type APISettings struct {
	Address          string `json:"address"`
	CertFile         string `json:"cert_file"`
	KeyFile          string `json:"key_file"`
	MaxBodyBytes     int64  `json:"max_body_bytes"`
	MaxConns         int    `json:"max_conns"`
	MaxInflight      int    `json:"max_inflight"`
	FirstByteTimeout string `json:"first_byte_timeout"`
	Timeout          string `json:"timeout"`
	// URL is where the API is served now, "" when it is not (read only).
	// URL 为 API 当前的服务地址，未提供时为 ""（只读）。
	URL string `json:"url,omitempty"`
}

// apiSettings returns the proxy's API settings, their defaults filled in.
func (c *Proxy) apiSettings() APISettings {
	c.mu.Lock()
	cfg := c.apiCfg
	c.mu.Unlock()
	if cfg == nil {
		return APISettings{}
	}
	return APISettings{Address: cfg.Address, CertFile: cfg.CertFile, KeyFile: cfg.KeyFile, MaxBodyBytes: cfg.Body(),
		MaxConns: cfg.Conns(), MaxInflight: cfg.Inflight(), FirstByteTimeout: cfg.FirstByte().String(),
		Timeout: cfg.Total().String(), URL: c.API()}
}

// setAPI writes the proxy's API into its member, or takes it out for an
// empty address, and answers it as applied.
func (c *Proxy) setAPI(ctx context.Context, _ model.Identity, req *APISettings) (any, error) {
	addr := strings.TrimSpace(req.Address)
	if addr == "" {
		if err := c.Edit(ctx, func(m map[string]json.RawMessage) error { delete(m, "api"); return nil }); err != nil {
			return nil, err
		}
		return c.apiSettings(), nil
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return nil, mesh.BadRequest("the address is a host and port, as 127.0.0.1:9301")
	}
	cert, key := strings.TrimSpace(req.CertFile), strings.TrimSpace(req.KeyFile)
	switch {
	case (cert == "") != (key == ""):
		return nil, mesh.BadRequest("cert_file and key_file go together")
	case cert == "" && !loopback(host):
		return nil, mesh.BadRequest("%s is not a loopback address: set cert_file and key_file, so that keys are not sent in the clear", addr)
	case cert != "":
		// Read on the role's pool, off fib's workers.
		if _, err := tls.LoadX509KeyPair(cert, key); err != nil {
			return nil, mesh.BadRequest("the certificate: %v", err)
		}
	}
	body, err := role.Count("max_body_bytes", req.MaxBodyBytes, config.DefaultLLMMaxBodyBytes)
	if err != nil {
		return nil, err
	}
	conns, err := role.Count("max_conns", req.MaxConns, config.DefaultLLMProxyMaxConns)
	if err != nil {
		return nil, err
	}
	inflight, err := role.Count("max_inflight", req.MaxInflight, config.DefaultLLMProxyMaxInflight)
	if err != nil {
		return nil, err
	}
	first, err := role.Duration("first_byte_timeout", req.FirstByteTimeout, 0, config.DefaultLLMFirstByteTimeout.Std())
	if err != nil {
		return nil, err
	}
	total, err := role.Duration("timeout", req.Timeout, 0, config.DefaultLLMCallTimeout.Std())
	if err != nil {
		return nil, err
	}
	api := role.Object("address", addr, "cert_file", cert, "key_file", key, "max_body_bytes", body, "max_conns", conns,
		"max_inflight", inflight, "first_byte_timeout", first, "timeout", total)
	if err := c.Edit(ctx, func(m map[string]json.RawMessage) error {
		b, err := json.Marshal(api)
		m["api"] = b
		return err
	}); err != nil {
		return nil, err
	}
	return c.apiSettings(), nil
}

// loopback reports whether host is a loopback address or localhost.
func loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// registerNodes adds the methods of nodes.go.
func (c *Proxy) registerNodes() {
	mesh.Handle(c.Router, MethodProxyNodes, nil, 0, func(context.Context, model.Identity, *struct{}) (any, error) {
		return c.nodes(), nil
	})
	mesh.Handle(c.Router, MethodProxyAPIGet, nil, 0, func(context.Context, model.Identity, *struct{}) (any, error) {
		return c.apiSettings(), nil
	})
	mesh.Handle(c.Router, MethodProxyAPISet, nil, role.EditTimeout, c.setAPI)
}
