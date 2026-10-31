package client

import (
	"embed"
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

//go:embed web
var web embed.FS

// pages are the console's files: the client's page and the shared ones.
var pages = common.Pages(web, "web")

// ServiceView is a node's service, as the pages show and set it.
type ServiceView struct {
	Name        string `json:"name"`
	Address     string `json:"address"`
	Description string `json:"description,omitempty"`
}

// NodeView is a node of a server, as the pages show it.
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
	// the server, from 0 to 1, by Weights.
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

// ServerView is a server and its nodes, as the pages show them.
type ServerView struct {
	uplink.Status
	Nodes []NodeView `json:"nodes"`
}

// Status is the client's state, as the pages show it.
type Status struct {
	Name       string            `json:"name"`
	Version    string            `json:"version"`
	Meta       map[string]string `json:"meta"`
	Servers    []ServerView      `json:"servers"`
	Forwards   []ForwardView     `json:"forwards"`
	WebProxies []WebProxyView    `json:"web_proxies"`
	Tunnels    []TunnelView      `json:"tunnels"`
	Open       int               `json:"open"`
	MaxTunnels int               `json:"max_tunnels"`
}

// Status is the client's state.
func (c *Client) Status() Status {
	cfg := c.Config()
	st := Status{Name: cfg.ClientName(), Version: proto.Version, Meta: cfg.Meta, Servers: []ServerView{}, Forwards: c.Forwards(), WebProxies: c.WebProxies(),
		Tunnels: c.Tunnels(), Open: c.limit.Open(), MaxTunnels: cfg.Tunnels()}
	if st.Meta == nil {
		st.Meta = map[string]string{}
	}
	// The paths to each node, to weigh them against each other.
	paths := map[string][]Path{}
	for _, u := range c.Uplinks() {
		v := ServerView{Status: u.Status(), Nodes: []NodeView{}}
		if list := c.Nodes(u); list != nil && v.State == uplink.StateRegistered {
			for _, p := range list.Nodes {
				v.Nodes = append(v.Nodes, viewNode(p))
				paths[p.Info.Name] = append(paths[p.Info.Name], Path{Server: v.RTT, Node: p.RTT})
			}
		}
		st.Servers = append(st.Servers, v)
	}
	weights := map[string][]float64{}
	for name, ps := range paths {
		weights[name] = Weights(ps)
	}
	// The servers in the same order again, so each takes its nodes' weights.
	for i := range st.Servers {
		for j := range st.Servers[i].Nodes {
			p := &st.Servers[i].Nodes[j]
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
	Server  string `json:"server"`
}

// api is the console's API. Each handler runs on a fib worker: a call to a
// server goes to the console's pool with Request.Go, and a change to the
// config is made with common.Edit, off the worker.
func (c *Client) api(api *console.API) {
	api.Handle("GET", "/api/status", func(r *console.Request) { _ = r.JSON(stdhttp.StatusOK, c.Status()) })
	api.Handle("POST", "/api/servers", func(r *console.Request) {
		var req common.ServerReq
		if !r.Decode(&req) {
			return
		}
		r.Logf("adds server %q", req.Name)
		common.Edit(c.h, r, func(m common.Member) error { return common.EditServer(m, "", req, false) }, nil)
	})
	api.Handle("POST", "/api/servers/update", func(r *console.Request) {
		var req struct {
			Name              string           `json:"name"`
			Server            common.ServerReq `json:"server"`
			ClearBodyPassword bool             `json:"clear_body_password"`
		}
		if !r.Decode(&req) {
			return
		}
		r.Logf("changes server %q", req.Name)
		common.Edit(c.h, r, func(m common.Member) error {
			return common.EditServer(m, req.Name, req.Server, req.ClearBodyPassword)
		}, nil)
	})
	api.Handle("POST", "/api/servers/delete", func(r *console.Request) {
		var req struct {
			Name string `json:"name"`
		}
		if !r.Decode(&req) {
			return
		}
		r.Logf("removes server %q", req.Name)
		common.Edit(c.h, r, func(m common.Member) error { return common.DeleteServer(m, req.Name) }, nil)
	})
	api.Handle("POST", "/api/nodes/refresh", func(r *console.Request) {
		var req struct {
			Server string `json:"server"`
		}
		if !r.Decode(&req) {
			return
		}
		u := c.uplink(req.Server)
		if u == nil {
			common.Fail(r, stdhttp.StatusNotFound, "no such server")
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
			_ = r.JSON(stdhttp.StatusOK, map[string]any{"server": list.Server, "nodes": nodes})
		})
	})
	api.Handle("POST", "/api/nodes/services", func(r *console.Request) {
		var req struct {
			Server   string        `json:"server"`
			Node     string        `json:"node"`
			Services []ServiceView `json:"services"`
		}
		if !r.Decode(&req) {
			return
		}
		u := c.uplink(req.Server)
		if u == nil {
			common.Fail(r, stdhttp.StatusNotFound, "no such server")
			return
		}
		services := make([]proto.Service, 0, len(req.Services))
		for _, v := range req.Services {
			services = append(services, proto.Service{Name: strings.TrimSpace(v.Name), Addr: strings.TrimSpace(v.Address), Desc: strings.TrimSpace(v.Description)})
		}
		r.Logf("sets the services of node %q through server %q", req.Node, req.Server)
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
		if s := strings.TrimSpace(req.Server); s != "" {
			f["server"] = s
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
			Server string `json:"server"`
			ID     string `json:"id"`
		}
		if !r.Decode(&req) {
			return
		}
		id, err := strconv.ParseUint(req.ID, 10, 64)
		if err != nil {
			common.Fail(r, stdhttp.StatusBadRequest, "the id is not a tunnel's")
			return
		}
		u := c.uplink(req.Server)
		if u == nil {
			common.Fail(r, stdhttp.StatusNotFound, "no such server")
			return
		}
		e := u.Tunnels.Get(id)
		if e == nil {
			common.Fail(r, stdhttp.StatusNotFound, "no such tunnel")
			return
		}
		e.Close("closed from the client's console")
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
