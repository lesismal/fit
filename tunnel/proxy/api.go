package proxy

import (
	"encoding/json"
	stdhttp "net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/lesismal/fit/app/console"
	"github.com/lesismal/fit/tunnel/internal/common"
	"github.com/lesismal/fit/tunnel/internal/latency"
	"github.com/lesismal/fit/tunnel/internal/proto"
	"github.com/lesismal/fit/tunnel/internal/uplink"
)

// ServiceView is a node's service, as the pages show and set it.
type ServiceView struct {
	Name        string `json:"name"`
	Address     string `json:"address"`
	Description string `json:"description,omitempty"`
}

// NodeView is a node of a hub, as the pages show it.
type NodeView struct {
	Name       string                   `json:"name"`
	Version    string                   `json:"version,omitempty"`
	Hostname   string                   `json:"hostname,omitempty"`
	OS         string                   `json:"os,omitempty"`
	Services   []ServiceView            `json:"services"`
	Meta       map[string]string        `json:"meta,omitempty"`
	Addr       string                   `json:"addr"`
	RTT        latency.Stats            `json:"rtt"`
	ServiceRTT map[string]latency.Stats `json:"service_rtt,omitempty"`
	// Share is the part of the tunnels to the node that should go through
	// the hub, from 0 to 1, by Weights.
	Share float64 `json:"share"`
}

func viewNode(p proto.NodeView) NodeView {
	v := NodeView{Name: p.Info.Name, Version: p.Info.Version, Hostname: p.Info.Hostname, OS: p.Info.OS, Meta: p.Info.Meta, Addr: p.Addr,
		RTT: p.RTT, ServiceRTT: p.ServiceRTT, Services: []ServiceView{}}
	for _, s := range p.Info.Services {
		v.Services = append(v.Services, ServiceView{Name: s.Name, Address: s.Addr, Description: s.Desc})
	}
	return v
}

// HubView is a hub and its nodes, as the pages show them.
type HubView struct {
	uplink.Status
	Nodes []NodeView `json:"nodes"`
}

// Status is the proxy's state, as the pages show it.
type Status struct {
	Name       string            `json:"name"`
	Version    string            `json:"version"`
	Meta       map[string]string `json:"meta"`
	Hubs       []HubView         `json:"hubs"`
	Forwards   []ForwardView     `json:"forwards"`
	WebProxies []WebProxyView    `json:"web_proxies"`
	Tunnels    []TunnelView      `json:"tunnels"`
	Open       int               `json:"open"`
	MaxTunnels int               `json:"max_tunnels"`
}

// Status is the proxy's state.
func (c *Proxy) Status() Status {
	cfg := c.Config()
	st := Status{Name: cfg.ProxyName(), Version: proto.Version, Meta: cfg.Meta, Hubs: []HubView{}, Forwards: c.Forwards(), WebProxies: c.WebProxies(),
		Tunnels: c.Tunnels(), Open: c.limit.Open(), MaxTunnels: cfg.Tunnels()}
	if st.Meta == nil {
		st.Meta = map[string]string{}
	}
	// The paths to each node, to weigh them against each other.
	paths := map[string][]Path{}
	for _, u := range c.Uplinks() {
		v := HubView{Status: u.Status(), Nodes: []NodeView{}}
		if list := c.Nodes(u); list != nil && v.State == uplink.StateRegistered {
			for _, p := range list.Nodes {
				v.Nodes = append(v.Nodes, viewNode(p))
				paths[p.Info.Name] = append(paths[p.Info.Name], Path{Hub: v.RTT, Node: p.RTT})
			}
		}
		st.Hubs = append(st.Hubs, v)
	}
	weights := map[string][]float64{}
	for name, ps := range paths {
		weights[name] = Weights(ps)
	}
	// The hubs in the same order again, so each takes its nodes' weights.
	for i := range st.Hubs {
		for j := range st.Hubs[i].Nodes {
			p := &st.Hubs[i].Nodes[j]
			w := weights[p.Name]
			p.Share, weights[p.Name] = w[0], w[1:]
		}
	}
	return st
}

// forwardReq is a forward as the pages send it.
type forwardReq struct {
	Listen  string `json:"listen"`
	Node    string `json:"node"`
	Service string `json:"service"`
	Hub     string `json:"hub"`
}

// api is the console's API. Each handler runs on a fib worker: a call to a
// hub goes to the console's pool with Request.Go, and a change to the
// config is made with common.Edit, off the worker.
func (c *Proxy) api(api *console.API) {
	api.Handle("GET", "/api/status", func(r *console.Request) { _ = r.JSON(stdhttp.StatusOK, c.Status()) })
	api.Handle("POST", "/api/hubs", func(r *console.Request) {
		var req common.HubReq
		if !r.Decode(&req) {
			return
		}
		r.Logf("adds hub %q", req.Name)
		common.Edit(c.h, r, func(m common.Member) error { return common.EditHub(m, "", req, false) }, nil)
	})
	api.Handle("POST", "/api/hubs/update", func(r *console.Request) {
		var req struct {
			Name              string        `json:"name"`
			Hub               common.HubReq `json:"hub"`
			ClearBodyPassword bool          `json:"clear_body_password"`
		}
		if !r.Decode(&req) {
			return
		}
		r.Logf("changes hub %q", req.Name)
		common.Edit(c.h, r, func(m common.Member) error {
			return common.EditHub(m, req.Name, req.Hub, req.ClearBodyPassword)
		}, nil)
	})
	api.Handle("POST", "/api/hubs/delete", func(r *console.Request) {
		var req struct {
			Name string `json:"name"`
		}
		if !r.Decode(&req) {
			return
		}
		r.Logf("removes hub %q", req.Name)
		common.Edit(c.h, r, func(m common.Member) error { return common.DeleteHub(m, req.Name) }, nil)
	})
	api.Handle("POST", "/api/nodes/refresh", func(r *console.Request) {
		var req struct {
			Hub string `json:"hub"`
		}
		if !r.Decode(&req) {
			return
		}
		u := c.uplink(req.Hub)
		if u == nil {
			common.Fail(r, stdhttp.StatusNotFound, "no such hub")
			return
		}
		r.Go(func() {
			list, err := c.RefreshNodes(u)
			if err != nil {
				common.Fail(r, stdhttp.StatusBadGateway, err.Error())
				return
			}
			nodes := []NodeView{}
			for _, p := range list.Nodes {
				nodes = append(nodes, viewNode(p))
			}
			_ = r.JSON(stdhttp.StatusOK, map[string]any{"hub": list.Hub, "nodes": nodes})
		})
	})
	api.Handle("POST", "/api/nodes/services", func(r *console.Request) {
		var req struct {
			Hub      string        `json:"hub"`
			Node     string        `json:"node"`
			Services []ServiceView `json:"services"`
		}
		if !r.Decode(&req) {
			return
		}
		u := c.uplink(req.Hub)
		if u == nil {
			common.Fail(r, stdhttp.StatusNotFound, "no such hub")
			return
		}
		services := make([]proto.Service, 0, len(req.Services))
		for _, v := range req.Services {
			services = append(services, proto.Service{Name: strings.TrimSpace(v.Name), Addr: strings.TrimSpace(v.Address), Desc: strings.TrimSpace(v.Description)})
		}
		r.Logf("sets the services of node %q through hub %q", req.Node, req.Hub)
		r.Go(func() {
			if _, err := c.SetNodeServices(u, req.Node, services); err != nil {
				common.Fail(r, stdhttp.StatusBadGateway, err.Error())
				return
			}
			common.OK(r)
		})
	})
	api.Handle("POST", "/api/meta", func(r *console.Request) {
		var meta map[string]string
		if !r.Decode(&meta) {
			return
		}
		r.Logf("changes the meta")
		common.Edit(c.h, r, func(m common.Member) error { return m.Set("meta", meta) }, nil)
	})
	api.Handle("POST", "/api/forwards", func(r *console.Request) {
		var req forwardReq
		if !r.Decode(&req) {
			return
		}
		f := map[string]string{"listen": strings.TrimSpace(req.Listen), "node": strings.TrimSpace(req.Node), "service": strings.TrimSpace(req.Service)}
		if s := strings.TrimSpace(req.Hub); s != "" {
			f["hub"] = s
		}
		r.Logf("forwards %s to node %q service %q", f["listen"], f["node"], f["service"])
		common.Edit(c.h, r, func(m common.Member) error {
			var fs []json.RawMessage
			if err := m.Get("forwards", &fs); err != nil {
				return err
			}
			if slices.ContainsFunc(fs, func(raw json.RawMessage) bool { return listenOf(raw) == f["listen"] }) {
				return common.BadRequest(f["listen"] + " is forwarded already")
			}
			data, err := json.Marshal(f)
			if err != nil {
				return err
			}
			return m.Set("forwards", append(fs, data))
		}, nil)
	})
	api.Handle("POST", "/api/forwards/delete", func(r *console.Request) {
		var req struct {
			Listen string `json:"listen"`
		}
		if !r.Decode(&req) {
			return
		}
		r.Logf("stops forwarding %s", req.Listen)
		common.Edit(c.h, r, func(m common.Member) error {
			var fs []json.RawMessage
			if err := m.Get("forwards", &fs); err != nil {
				return err
			}
			i := slices.IndexFunc(fs, func(raw json.RawMessage) bool { return listenOf(raw) == req.Listen })
			if i < 0 {
				return common.BadRequest(req.Listen + " is not forwarded")
			}
			return m.Set("forwards", slices.Delete(fs, i, i+1))
		}, nil)
	})
	api.Handle("POST", "/api/tunnels/close", func(r *console.Request) {
		var req struct {
			Hub string `json:"hub"`
			ID  string `json:"id"`
		}
		if !r.Decode(&req) {
			return
		}
		id, err := strconv.ParseUint(req.ID, 10, 64)
		if err != nil {
			common.Fail(r, stdhttp.StatusBadRequest, "the id is not a tunnel's")
			return
		}
		u := c.uplink(req.Hub)
		if u == nil {
			common.Fail(r, stdhttp.StatusNotFound, "no such hub")
			return
		}
		e := u.Tunnels.Get(id)
		if e == nil {
			common.Fail(r, stdhttp.StatusNotFound, "no such tunnel")
			return
		}
		e.Close("closed from the proxy's console")
		common.OK(r)
	})
}

// listenOf is the listen address of a forward's JSON.
func listenOf(raw json.RawMessage) string {
	var f struct {
		Listen string `json:"listen"`
	}
	_ = json.Unmarshal(raw, &f)
	return f.Listen
}

// Module returns the proxy's module, which the workbench shows.
func (c *Proxy) Module() *console.Module { return c.console.Module() }
