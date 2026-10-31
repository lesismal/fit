package common

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/lesismal/fit/config"
)

// ServerReq is a server a node or client links to, as its pages send it to
// add or change one. A password left empty stays as it was.
type ServerReq struct {
	Name         string        `json:"name"`
	Endpoints    []EndpointReq `json:"endpoints"`
	Password     string        `json:"password"`
	BodyPassword string        `json:"body_password"`
	PoolSize     int           `json:"pool_size"`
	QUICPin      string        `json:"quic_pin"`
	QUICInsecure bool          `json:"quic_insecure"`
}

// EndpointReq is one of a server's addresses, as the pages send it.
type EndpointReq struct {
	Protocol string `json:"protocol"`
	Address  string `json:"address"`
	Enabled  bool   `json:"enabled"`
}

// address is the endpoint's address with its scheme: host:port for tcp.
func (e EndpointReq) address() string {
	a := strings.TrimSpace(e.Address)
	if a == "" || strings.Contains(a, "://") {
		return a
	}
	switch e.Protocol {
	case config.TunnelWS:
		if !strings.Contains(a, "/") {
			a += config.DefaultTunnelWSPath
		}
		return "ws://" + a
	case config.TunnelQUIC:
		return "quic://" + a
	}
	return a
}

// uplinkName is an uplink's name as config.TunnelUplink.ServerName has it,
// of the JSON of one.
func uplinkName(raw json.RawMessage) string {
	var u struct {
		Name      string `json:"name"`
		Endpoints []struct {
			Address string `json:"address"`
		} `json:"endpoints"`
	}
	_ = json.Unmarshal(raw, &u)
	c := config.TunnelUplink{Name: u.Name}
	for _, e := range u.Endpoints {
		c.Endpoints = append(c.Endpoints, config.TunnelEndpoint{Address: e.Address})
	}
	return c.ServerName()
}

// EditServer changes the member's servers: it adds req when name is "", and
// otherwise changes the one named name to req, keeping what req leaves out:
// its passwords when empty, unless clearBody, and every setting the pages do
// not show.
func EditServer(m Member, name string, req ServerReq, clearBody bool) error {
	var servers []json.RawMessage
	if err := m.Get("servers", &servers); err != nil {
		return err
	}
	i := -1
	if name != "" {
		i = slices.IndexFunc(servers, func(raw json.RawMessage) bool { return uplinkName(raw) == name })
		if i < 0 {
			return BadRequest("no server named " + name)
		}
	}
	u := Member{}
	if i >= 0 {
		if err := json.Unmarshal(servers[i], &u); err != nil {
			return err
		}
	}
	var eps []map[string]any
	for _, e := range req.Endpoints {
		if a := e.address(); a != "" {
			ep := map[string]any{"address": a}
			if !e.Enabled {
				ep["disabled"] = true
			}
			eps = append(eps, ep)
		}
	}
	if len(eps) == 0 {
		return BadRequest("a server needs an address")
	}
	set := func(k string, v any) error { return u.Set(k, v) }
	if err := set("name", strings.TrimSpace(req.Name)); err != nil {
		return err
	}
	if err := set("endpoints", eps); err != nil {
		return err
	}
	if req.Password != "" {
		if err := set("password", req.Password); err != nil {
			return err
		}
	}
	switch {
	case req.BodyPassword != "":
		if err := set("body_password", req.BodyPassword); err != nil {
			return err
		}
	case clearBody:
		delete(u, "body_password")
	}
	if err := set("pool_size", req.PoolSize); err != nil {
		return err
	}
	if err := set("quic_pin", strings.TrimSpace(req.QUICPin)); err != nil {
		return err
	}
	if err := set("quic_insecure", req.QUICInsecure); err != nil {
		return err
	}
	if _, ok := u["password"]; !ok {
		return BadRequest("a server needs a password")
	}
	data, err := json.Marshal(u)
	if err != nil {
		return err
	}
	newName := uplinkName(data)
	for j, raw := range servers {
		if j != i && uplinkName(raw) == newName {
			return BadRequest("a server named " + newName + " exists")
		}
	}
	if i >= 0 {
		servers[i] = data
	} else {
		servers = append(servers, data)
	}
	return m.Set("servers", servers)
}

// DeleteServer removes the server named name from the member's servers.
func DeleteServer(m Member, name string) error {
	var servers []json.RawMessage
	if err := m.Get("servers", &servers); err != nil {
		return err
	}
	i := slices.IndexFunc(servers, func(raw json.RawMessage) bool { return uplinkName(raw) == name })
	if i < 0 {
		return BadRequest("no server named " + name)
	}
	return m.Set("servers", slices.Delete(servers, i, i+1))
}
