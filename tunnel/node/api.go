package node

import (
	stdhttp "net/http"
	"strconv"
	"strings"

	"github.com/lesismal/fit/app/console"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/tunnel/internal/common"
	"github.com/lesismal/fit/tunnel/internal/latency"
	"github.com/lesismal/fit/tunnel/internal/proto"
	"github.com/lesismal/fit/tunnel/internal/uplink"
)

// ServiceView is a service, as the pages show and set it.
type ServiceView struct {
	Name        string `json:"name"`
	Address     string `json:"address"`
	Description string `json:"description,omitempty"`
}

// Status is the node's state, as the pages show it.
type Status struct {
	Name     string            `json:"name"`
	Version  string            `json:"version"`
	Meta     map[string]string `json:"meta"`
	Services []ServiceView     `json:"services"`
	// ServiceRTT is, by service name, how long the connects to the
	// services took.
	ServiceRTT map[string]latency.Stats `json:"service_rtt"`
	Hubs       []uplink.Status          `json:"hubs"`
	Tunnels    []TunnelView             `json:"tunnels"`
	// Open and MaxTunnels are the tunnels open, and the most there may be.
	Open       int `json:"open"`
	MaxTunnels int `json:"max_tunnels"`
}

// Status is the node's state.
func (n *Node) Status() Status {
	cfg := n.Config()
	st := Status{Name: cfg.NodeName(), Version: proto.Version, Meta: cfg.Meta, Services: []ServiceView{}, ServiceRTT: n.ServiceRTT(),
		Hubs: []uplink.Status{}, Tunnels: n.Tunnels(), Open: n.limit.Open(), MaxTunnels: cfg.Tunnels()}
	for _, s := range cfg.Services {
		st.Services = append(st.Services, ServiceView{Name: s.Name, Address: s.Address, Description: s.Description})
	}
	for _, u := range n.Uplinks() {
		st.Hubs = append(st.Hubs, u.Status())
	}
	return st
}

// api is the console's API. Each handler runs on a fib worker; a change to
// the config is made with common.Edit, off the worker.
func (n *Node) api(api *console.API) {
	api.Handle("GET", "/api/status", func(r *console.Request) { _ = r.JSON(stdhttp.StatusOK, n.Status()) })
	api.Handle("POST", "/api/hubs", func(r *console.Request) {
		var req common.HubReq
		if !r.Decode(&req) {
			return
		}
		r.Logf("adds hub %q", req.Name)
		common.Edit(n.h, r, func(m common.Member) error { return common.EditHub(m, "", req, false) }, nil)
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
		common.Edit(n.h, r, func(m common.Member) error {
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
		common.Edit(n.h, r, func(m common.Member) error { return common.DeleteHub(m, req.Name) }, nil)
	})
	api.Handle("POST", "/api/services", func(r *console.Request) {
		var req []ServiceView
		if !r.Decode(&req) {
			return
		}
		list := make([]config.TunnelService, 0, len(req))
		for _, s := range req {
			list = append(list, config.TunnelService{Name: strings.TrimSpace(s.Name), Address: strings.TrimSpace(s.Address), Description: strings.TrimSpace(s.Description)})
		}
		r.Logf("sets the services")
		common.Edit(n.h, r, func(m common.Member) error { return m.Set("services", list) }, nil)
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
		u := n.uplink(req.Hub)
		if u == nil {
			common.Fail(r, stdhttp.StatusNotFound, "no such hub")
			return
		}
		e := u.Tunnels.Get(id)
		if e == nil {
			common.Fail(r, stdhttp.StatusNotFound, "no such tunnel")
			return
		}
		e.Close("closed from the node's console")
		common.OK(r)
	})
}

// Module returns the node's module, which the workbench shows.
func (n *Node) Module() *console.Module { return n.console.Module() }
