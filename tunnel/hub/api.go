package hub

import (
	"encoding/json"
	"fmt"
	stdhttp "net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/lesismal/fit/app/console"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/tunnel/internal/common"
	"github.com/lesismal/fit/tunnel/internal/latency"
	"github.com/lesismal/fit/tunnel/internal/proto"
)

// InfoView is what a peer told about itself, as the pages show it: a node
// is a node, and its services are services.
type InfoView struct {
	Name     string            `json:"name"`
	Type     string            `json:"type"`
	Version  string            `json:"version,omitempty"`
	Hostname string            `json:"hostname,omitempty"`
	OS       string            `json:"os,omitempty"`
	Services []ServiceView     `json:"services"`
	Meta     map[string]string `json:"meta,omitempty"`
}

// ServiceView is a node's service, as the pages show and set it.
type ServiceView struct {
	Name        string `json:"name"`
	Address     string `json:"address"`
	Description string `json:"description,omitempty"`
}

// ViewInfo is info as the pages show it.
func ViewInfo(info proto.Info) InfoView {
	v := InfoView{Name: info.Name, Type: info.Type, Version: info.Version, Hostname: info.Hostname, OS: info.OS, Meta: info.Meta, Services: []ServiceView{}}
	for _, s := range info.Services {
		v.Services = append(v.Services, ServiceView{Name: s.Name, Address: s.Addr, Description: s.Desc})
	}
	return v
}

// PeerView is a peer as the pages show it.
type PeerView struct {
	ID      uint32    `json:"id"`
	Addr    string    `json:"addr"`
	Since   time.Time `json:"since"`
	Info    InfoView  `json:"info"`
	Tunnels int       `json:"tunnels"`
	// Conns is how many links the peer has registered, and Places the
	// places they hold, lowest first.
	Conns      int                      `json:"conns"`
	Places     []int                    `json:"places"`
	RTT        latency.Stats            `json:"rtt"`
	ServiceRTT map[string]latency.Stats `json:"service_rtt,omitempty"`
}

// RouteView is a tunnel as the pages show it.
type RouteView struct {
	ID      uint64    `json:"id,string"`
	Proxy   string    `json:"proxy"`
	Node    string    `json:"node"`
	Service string    `json:"service"`
	Addr    string    `json:"addr"`
	Created time.Time `json:"created"`
	Up      int64     `json:"up"`
	Down    int64     `json:"down"`
}

// ListenerView is a listener as the pages show it: its settings, the URL
// peers dial when it is open, and why it is not; a tls, wss or quic one's
// certificate by its fingerprint, and whether it is the listener's own
// (cert_file) rather than the hub's.
type ListenerView struct {
	Protocol    string `json:"protocol"`
	Address     string `json:"address"`
	Path        string `json:"path,omitempty"`
	URL         string `json:"url,omitempty"`
	Error       string `json:"error,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	OwnCert     bool   `json:"own_cert,omitempty"`
}

// SettingsView is the settings the pages change besides the listeners, the
// passwords only as whether they are set.
type SettingsView struct {
	HasPassword       bool   `json:"has_password"`
	HasBodyPassword   bool   `json:"has_body_password"`
	HasSocketPassword bool   `json:"has_socket_password"`
	SocketAddress     string `json:"socket_address"`
}

// Status is the hub's state, as the pages show it.
type Status struct {
	Name      string         `json:"name"`
	Version   string         `json:"version"`
	Listen    []string       `json:"listen"`
	Listeners []ListenerView `json:"listeners"`
	Settings  SettingsView   `json:"settings"`
	Socket    string         `json:"socket,omitempty"`
	// Fingerprint is the hub's certificate's, which its tls, wss and quic
	// listeners without cert_file present; QUICFingerprint is its old name.
	Fingerprint     string      `json:"fingerprint"`
	QUICFingerprint string      `json:"quic_fingerprint"`
	BodyEncrypted   bool        `json:"body_encrypted"`
	Started         time.Time   `json:"started"`
	Links           int         `json:"links"`
	Peers           []PeerView  `json:"peers"`
	Tunnels         []RouteView `json:"tunnels"`
}

// Status is the hub's state.
func (s *Hub) Status() Status {
	set := s.settings()
	cfg := set.cfg
	st := Status{Name: set.name, Version: proto.Version, Listen: []string{}, Listeners: []ListenerView{}, Socket: s.SocketAddr(),
		Fingerprint: s.Fingerprint(), QUICFingerprint: s.Fingerprint(), BodyEncrypted: cfg.BodyPassword != "", Started: s.since, Peers: []PeerView{}, Tunnels: []RouteView{},
		Settings: SettingsView{HasPassword: cfg.Password != "", HasBodyPassword: cfg.BodyPassword != ""}}
	if cs := cfg.ConsoleSocket; cs != nil {
		st.Settings.SocketAddress, st.Settings.HasSocketPassword = cs.Address, cs.Password != ""
	}
	s.lmu.Lock()
	for _, l := range s.listeners {
		v := ListenerView{Protocol: l.cfg.Protocol, Address: l.cfg.Address, Path: l.cfg.Path, Error: l.err, Fingerprint: l.fp, OwnCert: l.cfg.CertFile != ""}
		if l.l != nil {
			v.URL = l.l.URL()
			st.Listen = append(st.Listen, v.URL)
		}
		st.Listeners = append(st.Listeners, v)
	}
	s.lmu.Unlock()
	counts := map[*Peer]int{}
	for _, r := range s.Routes() {
		counts[r.Proxy]++
		counts[r.Node]++
		s.mu.Lock()
		addr := r.Addr
		s.mu.Unlock()
		st.Tunnels = append(st.Tunnels, RouteView{ID: r.ID, Proxy: r.Proxy.Info().Name, Node: r.Node.Info().Name, Service: r.Service, Addr: addr,
			Created: r.Created, Up: r.up.Load(), Down: r.down.Load()})
	}
	s.mu.Lock()
	st.Links = len(s.links)
	s.mu.Unlock()
	for _, p := range s.Peers() {
		s.mu.Lock()
		places := p.placesLocked()
		s.mu.Unlock()
		lat := p.Latency()
		st.Peers = append(st.Peers, PeerView{ID: p.ID, Addr: p.Addr, Since: p.Since, Info: ViewInfo(p.Info()), Tunnels: counts[p], Conns: len(places), Places: places,
			RTT: lat.RTT, ServiceRTT: lat.ServiceRTT})
	}
	return st
}

// listenerReq is a listener as the pages send it.
type listenerReq struct {
	Protocol string `json:"protocol"`
	Address  string `json:"address"`
	Path     string `json:"path"`
}

func (l listenerReq) listener() config.TunnelListener {
	tl := config.TunnelListener{Protocol: strings.TrimSpace(l.Protocol), Address: strings.TrimSpace(l.Address), Path: strings.TrimSpace(l.Path)}
	if !tl.IsWS() || tl.Path == config.DefaultTunnelWSPath {
		tl.Path = ""
	}
	if tl.Path != "" && !strings.HasPrefix(tl.Path, "/") {
		tl.Path = "/" + tl.Path
	}
	return tl
}

// settingsReq is what the pages send to change the settings. A password
// left empty stays as it was, unless its clear says to remove it.
type settingsReq struct {
	Password            string `json:"password"`
	BodyPassword        string `json:"body_password"`
	ClearBodyPassword   bool   `json:"clear_body_password"`
	SocketPassword      string `json:"socket_password"`
	ClearSocketPassword bool   `json:"clear_socket_password"`
	// SocketAddress is the console socket's address, empty for none.
	SocketAddress string `json:"socket_address"`
}

type idReq struct {
	ID uint32 `json:"id"`
}

// routes are the console's API. Each handler runs on a fib worker: a call
// to a node goes to the console's pool with Request.Go, and a change to the
// config is made with common.Edit, off the worker.
func (s *Hub) api(api *console.API) {
	api.Handle("GET", "/api/status", func(r *console.Request) { _ = r.JSON(stdhttp.StatusOK, s.Status()) })
	api.Handle("POST", "/api/listeners", func(r *console.Request) {
		var req listenerReq
		if !r.Decode(&req) {
			return
		}
		l := req.listener()
		r.Logf("adds listener %s", l.URL())
		common.Edit(s.h, r, func(m common.Member) error {
			return editListeners(m, func(ls []config.TunnelListener) ([]config.TunnelListener, error) {
				if slices.ContainsFunc(ls, func(have config.TunnelListener) bool { return have.Key() == l.Key() }) {
					return nil, common.BadRequest("a listener on " + l.URL() + " exists")
				}
				return append(ls, l), nil
			})
		}, nil)
	})
	api.Handle("POST", "/api/listeners/update", func(r *console.Request) {
		var req struct {
			Old listenerReq `json:"old"`
			New listenerReq `json:"new"`
		}
		if !r.Decode(&req) {
			return
		}
		old, l := req.Old.listener(), req.New.listener()
		r.Logf("changes listener %s to %s", old.URL(), l.URL())
		common.Edit(s.h, r, func(m common.Member) error {
			return editListeners(m, func(ls []config.TunnelListener) ([]config.TunnelListener, error) {
				i := slices.IndexFunc(ls, func(have config.TunnelListener) bool { return have.Key() == old.Key() })
				if i < 0 {
					return nil, common.BadRequest("no listener on " + old.URL())
				}
				if old.Key() != l.Key() && slices.ContainsFunc(ls, func(have config.TunnelListener) bool { return have.Key() == l.Key() }) {
					return nil, common.BadRequest("a listener on " + l.URL() + " exists")
				}
				// The pages do not show a listener's certificate files: an
				// edit keeps them where the protocol still takes them.
				if config.TunnelTakesTLS(l.Protocol) {
					l.CertFile, l.KeyFile = ls[i].CertFile, ls[i].KeyFile
				}
				ls[i] = l
				return ls, nil
			})
		}, nil)
	})
	api.Handle("POST", "/api/listeners/delete", func(r *console.Request) {
		var req listenerReq
		if !r.Decode(&req) {
			return
		}
		l := req.listener()
		r.Logf("removes listener %s", l.URL())
		common.Edit(s.h, r, func(m common.Member) error {
			return editListeners(m, func(ls []config.TunnelListener) ([]config.TunnelListener, error) {
				i := slices.IndexFunc(ls, func(have config.TunnelListener) bool { return have.Key() == l.Key() })
				if i < 0 {
					return nil, common.BadRequest("no listener on " + l.URL())
				}
				return slices.Delete(ls, i, i+1), nil
			})
		}, nil)
	})
	api.Handle("POST", "/api/settings", func(r *console.Request) {
		var req settingsReq
		if !r.Decode(&req) {
			return
		}
		r.Logf("changes the settings")
		common.Edit(s.h, r, func(m common.Member) error { return editSettings(m, req) }, nil)
	})
	api.Handle("POST", "/api/peers/refresh", func(r *console.Request) {
		node, ok := s.nodeParam(r)
		if !ok {
			return
		}
		r.Go(func() {
			info, err := s.refreshNode(node)
			if err != nil {
				common.Fail(r, stdhttp.StatusBadGateway, err.Error())
				return
			}
			_ = r.JSON(stdhttp.StatusOK, ViewInfo(info))
		})
	})
	api.Handle("POST", "/api/peers/services", func(r *console.Request) {
		var req struct {
			ID       uint32        `json:"id"`
			Services []ServiceView `json:"services"`
		}
		if !r.Decode(&req) {
			return
		}
		node := s.peerByID(req.ID)
		if node == nil || node.Info().Type != proto.TypeNode {
			common.Fail(r, stdhttp.StatusNotFound, "no such node")
			return
		}
		services := make([]proto.Service, 0, len(req.Services))
		for _, v := range req.Services {
			services = append(services, proto.Service{Name: strings.TrimSpace(v.Name), Addr: strings.TrimSpace(v.Address), Desc: strings.TrimSpace(v.Description)})
		}
		r.Logf("sets the services of node %q", node.Info().Name)
		r.Go(func() {
			info, err := s.setServices(node, services)
			if err != nil {
				common.Fail(r, stdhttp.StatusBadGateway, err.Error())
				return
			}
			_ = r.JSON(stdhttp.StatusOK, ViewInfo(info))
		})
	})
	api.Handle("POST", "/api/peers/kick", func(r *console.Request) {
		var req idReq
		if !r.Decode(&req) {
			return
		}
		peer := s.peerByID(req.ID)
		if peer == nil {
			common.Fail(r, stdhttp.StatusNotFound, "no such peer")
			return
		}
		r.Logf("kicks %s %q", peer.Info().Type, peer.Info().Name)
		s.kick(peer, "kicked from the hub's console")
		common.OK(r)
	})
	api.Handle("POST", "/api/tunnels/close", func(r *console.Request) {
		var req struct {
			ID string `json:"id"`
		}
		if !r.Decode(&req) {
			return
		}
		id, err := strconv.ParseUint(req.ID, 10, 64)
		if err != nil {
			common.Fail(r, stdhttp.StatusBadRequest, "the id is not a tunnel's")
			return
		}
		if !s.CloseRoute(id, "closed from the hub's console") {
			common.Fail(r, stdhttp.StatusNotFound, "no such tunnel")
			return
		}
		common.OK(r)
	})
}

// nodeParam is the node the request's {"id"} names, or false once it has
// answered that there is none.
func (s *Hub) nodeParam(r *console.Request) (*Peer, bool) {
	var req idReq
	if !r.Decode(&req) {
		return nil, false
	}
	peer := s.peerByID(req.ID)
	if peer == nil || peer.Info().Type != proto.TypeNode {
		common.Fail(r, stdhttp.StatusNotFound, "no such node")
		return nil, false
	}
	return peer, true
}

// editListeners changes the member's listeners with change.
func editListeners(m common.Member, change func([]config.TunnelListener) ([]config.TunnelListener, error)) error {
	var ls []config.TunnelListener
	if err := m.Get("listeners", &ls); err != nil {
		return err
	}
	ls, err := change(ls)
	if err != nil {
		return err
	}
	return m.Set("listeners", ls)
}

// editSettings applies what the settings form sent to the member.
func editSettings(m common.Member, req settingsReq) error {
	if req.Password != "" {
		if err := m.Set("password", req.Password); err != nil {
			return err
		}
	}
	switch {
	case req.BodyPassword != "":
		if err := m.Set("body_password", req.BodyPassword); err != nil {
			return err
		}
	case req.ClearBodyPassword:
		delete(m, "body_password")
	}
	addr := strings.TrimSpace(req.SocketAddress)
	if addr == "" {
		delete(m, "console_socket")
		return nil
	}
	cs := common.Member{}
	if err := m.Get("console_socket", &cs); err != nil {
		return err
	}
	if err := cs.Set("address", addr); err != nil {
		return err
	}
	switch {
	case req.SocketPassword != "":
		if err := cs.Set("password", req.SocketPassword); err != nil {
			return err
		}
	case req.ClearSocketPassword:
		delete(cs, "password")
	}
	data, err := json.Marshal(cs)
	if err != nil {
		return fmt.Errorf("console_socket: %w", err)
	}
	m["console_socket"] = data
	return nil
}

// Module returns the hub's module, which the workbench shows.
func (s *Hub) Module() *console.Module { return s.console.Module() }
