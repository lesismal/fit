// Package node is the tunnel's node role, tunnel.node in fit's config.
// A node links to one or more tunnel hubs, registers with
// the services it reaches, and opens the TCP connections to those services
// that the tunnels the hubs route to it ask for, and to no other
// address. Its console pages show its hubs and tunnels and change which
// hubs it links to, over which protocols, and which services it reaches;
// a hub's pages may set its services too. Every such change is written
// back into the node's member of the config, and applied from there.
//
// Everything runs on the listeners' fib engine: the links and tunnels are
// served on its workers, and what waits, a dial to a service or a call, on
// the links' bounded goroutines and the uplinks' own.
package node

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lesismal/fib"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/tunnel/internal/common"
	"github.com/lesismal/fit/tunnel/internal/latency"
	"github.com/lesismal/fit/tunnel/internal/link"
	"github.com/lesismal/fit/tunnel/internal/proto"
	"github.com/lesismal/fit/tunnel/internal/tunnel"
	"github.com/lesismal/fit/tunnel/internal/uplink"
)

// ID is the role's, and its member's path in the config.
const ID = "tunnel.node"

func init() {
	app.Register(app.Info{
		ID: ID,
		Member: func(c *config.Config) any {
			if c.Tunnel == nil {
				return nil
			}
			return c.Tunnel.Node
		},
		New: func(h *app.Host) (app.Role, error) { return New(h), nil },
	})
}

// Node is the running role.
type Node struct {
	h     *app.Host
	log   *logs.Logger
	limit *tunnel.Limit

	cfg atomic.Pointer[config.TunnelNode]

	// amu is held for a whole Apply or Stop.
	amu     sync.Mutex
	started bool
	stopped bool
	console *common.Console
	quit    chan struct{}

	mu      sync.Mutex
	uplinks []*uplink.Uplink
	// serviceRTT measures, by service name, the connects to the services.
	serviceRTT map[string]*latency.Meter
}

// New returns the role, which its first Apply starts.
func New(h *app.Host) *Node {
	n := &Node{h: h, log: h.Log, limit: tunnel.NewLimit(config.DefaultTunnelTunnels), serviceRTT: map[string]*latency.Meter{}, quit: make(chan struct{})}
	n.console = common.NewConsole(h.Engine, "tunnel.node", h.Log, n.api)
	return n
}

// Config is the member the node runs.
func (n *Node) Config() *config.TunnelNode { return n.cfg.Load() }

// Apply moves the node to member, a *config.TunnelNode. A hub whose
// settings are unchanged keeps its links and tunnels; one that changed links
// anew, and its tunnels close. The hubs kept are told of new services
// and meta. A console that does not start fails the first Apply, and is
// returned beside the rest applied after.
func (n *Node) Apply(member any) error {
	cfg, ok := member.(*config.TunnelNode)
	if !ok || cfg == nil {
		return errors.New("tunnel.node: no member")
	}
	n.amu.Lock()
	defer n.amu.Unlock()
	if n.stopped {
		return app.ErrStopped
	}
	var consoleErr error
	if err := n.console.Apply(cfg.Console); err != nil {
		if !n.started {
			return fmt.Errorf("console: %w", err)
		}
		consoleErr = fmt.Errorf("console: %w", err)
	}
	prev := n.cfg.Swap(cfg)
	n.limit.SetMax(cfg.Tunnels())
	// A new name is a new peer to the hubs: every link registers anew.
	renamed := prev != nil && prev.NodeName() != cfg.NodeName()
	n.mu.Lock()
	old := map[string]*uplink.Uplink{}
	for _, u := range n.uplinks {
		old[u.Name] = u
	}
	var start, kept, stop []*uplink.Uplink
	next := make([]*uplink.Uplink, 0, len(cfg.Hubs))
	for i := range cfg.Hubs {
		sc := &cfg.Hubs[i]
		if u := old[sc.HubName()]; u != nil && !renamed && uplink.Same(u.Config, sc) {
			delete(old, u.Name)
			kept = append(kept, u)
			next = append(next, u)
			continue
		}
		u := n.newUplink(sc)
		start = append(start, u)
		next = append(next, u)
	}
	for _, u := range old {
		stop = append(stop, u)
	}
	n.uplinks = next
	// What was measured of a service goes with it, and a service whose
	// address changes is measured anew.
	was, will := proto.Info{}, n.Info()
	if prev != nil {
		was = infoOf(prev)
	}
	for name := range n.serviceRTT {
		o, _ := was.FindService(name)
		if s, ok := will.FindService(name); !ok || s.Addr != o.Addr {
			delete(n.serviceRTT, name)
		}
	}
	n.mu.Unlock()
	for _, u := range stop {
		u.Stop()
	}
	for _, u := range start {
		u.Start()
	}
	if prev != nil && !renamed && !sameInfo(was, will) {
		for _, u := range kept {
			go n.update(u, will)
		}
	}
	if !n.started {
		n.started = true
		go n.sweep()
	}
	return consoleErr
}

// sameInfo reports whether a and b tell the hubs the same.
func sameInfo(a, b proto.Info) bool {
	return slices.Equal(a.Services, b.Services) && maps.Equal(a.Meta, b.Meta)
}

// update tells u's hub what the node is now.
func (n *Node) update(u *uplink.Uplink, info proto.Info) {
	if err := u.Call(proto.MethodUpdate, &info, nil); err != nil && !errors.Is(err, uplink.ErrNotRegistered) {
		n.log.Warnf("hub %s: telling it the node's services: %v", u.Name, err)
	}
}

// sweep closes the tunnels idle past idle_timeout, until the node stops.
func (n *Node) sweep() {
	for {
		idle := n.Config().Idle()
		t := time.NewTimer(common.SweepEvery(idle))
		select {
		case <-n.quit:
			t.Stop()
			return
		case <-t.C:
			for _, u := range n.Uplinks() {
				u.Tunnels.CloseIdle(idle)
			}
		}
	}
}

// Stop closes every link, with its tunnels, and the console.
func (n *Node) Stop(ctx context.Context) {
	n.amu.Lock()
	defer n.amu.Unlock()
	if n.stopped {
		return
	}
	n.stopped = true
	close(n.quit)
	n.mu.Lock()
	uplinks := n.uplinks
	n.uplinks = nil
	n.mu.Unlock()
	for _, u := range uplinks {
		u.Stop()
	}
	n.console.Stop(ctx)
}

// infoOf is what a node of cfg tells the hubs.
func infoOf(cfg *config.TunnelNode) proto.Info {
	info := proto.NewInfo(cfg.NodeName(), proto.TypeNode)
	info.Services = make([]proto.Service, 0, len(cfg.Services))
	for _, s := range cfg.Services {
		info.Services = append(info.Services, proto.Service{Name: s.Name, Addr: s.Address, Desc: s.Description})
	}
	info.Meta = cfg.Meta
	return info
}

// Info is what the node tells hubs about itself.
func (n *Node) Info() proto.Info {
	if cfg := n.Config(); cfg != nil {
		return infoOf(cfg)
	}
	return proto.Info{}
}

// ServiceRTT is, by service name, how long the connects to the services
// took. A service the node has not connected to is missing.
func (n *Node) ServiceRTT() map[string]latency.Stats {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make(map[string]latency.Stats, len(n.serviceRTT))
	for name, m := range n.serviceRTT {
		out[name] = m.Stats()
	}
	return out
}

// serviceMeter is the meter of the connects to the service named name.
func (n *Node) serviceMeter(name string) *latency.Meter {
	n.mu.Lock()
	defer n.mu.Unlock()
	m := n.serviceRTT[name]
	if m == nil {
		m = &latency.Meter{}
		n.serviceRTT[name] = m
	}
	return m
}

func (n *Node) newUplink(sc *config.TunnelUplink) *uplink.Uplink {
	u := uplink.New(n.h.Engine, sc, n.limit, n.log, n.Info, n.setupUplink)
	// Hubs tell proxies how fast the node reaches its services.
	u.OnPing = func(req *proto.PingReq) { req.ServiceRTT = n.ServiceRTT() }
	return u
}

// setupUplink registers the methods a hub calls on the node. They run
// on the link's goroutines, which may wait.
func (n *Node) setupUplink(u *uplink.Uplink, r *link.Router) {
	r.HandleCall(proto.MethodNodeInfo, func(*link.Link, []byte) (any, error) {
		info := n.Info()
		return &info, nil
	}, false)
	r.HandleCall(proto.MethodNodeSetServices, func(_ *link.Link, body []byte) (any, error) {
		var services []proto.Service
		if err := link.Decode(body, &services); err != nil {
			return nil, err
		}
		if err := n.setServices(services, u.Config.Call()); err != nil {
			return nil, err
		}
		info := n.Info()
		return &info, nil
	}, false)
	r.HandleCall(proto.MethodTunnelOpen, func(l *link.Link, body []byte) (any, error) {
		var req proto.TunnelOpenReq
		if err := link.Decode(body, &req); err != nil {
			return nil, err
		}
		rsp, err := n.openTunnel(u, l, &req)
		if err != nil {
			n.log.Warnf("tunnel %d to service %q for proxy %q: %v", req.ID, req.Service, req.Proxy, err)
			return nil, err
		}
		return rsp, nil
	}, false)
}

// setServices writes services, which a hub set, into the node's member
// of the config, and waits for the config to apply, for at most wait.
func (n *Node) setServices(services []proto.Service, wait time.Duration) error {
	list := make([]config.TunnelService, 0, len(services))
	for _, s := range services {
		list = append(list, config.TunnelService{Name: s.Name, Address: s.Addr, Description: s.Desc})
	}
	done := make(chan error, 1)
	n.h.Edit(func(cur []byte) ([]byte, error) {
		return common.EditJSON(cur, func(m common.Member) error { return m.Set("services", list) })
	}, func(_ []string, err error) { done <- err })
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case err := <-done:
		return err
	case <-t.C:
		return errors.New("tunnel: the node's config took too long to change")
	}
}

// openTunnel connects tunnel req.ID to the service it names, and waits for
// that, on the link's goroutine.
func (n *Node) openTunnel(u *uplink.Uplink, l *link.Link, req *proto.TunnelOpenReq) (*proto.TunnelOpenRsp, error) {
	cfg := n.Config()
	info := n.Info()
	svc, ok := info.FindService(req.Service)
	if !ok {
		return nil, fmt.Errorf("tunnel: no service %q", req.Service)
	}
	if u.Tunnels.Get(req.ID) != nil {
		return nil, tunnel.ErrTaken
	}
	meter := n.serviceMeter(svc.Name)
	start := time.Now()
	var rmu sync.Mutex
	var refused error
	handler := &tunnel.Handler{Open: func(c *fib.Connection) *tunnel.Endpoint {
		d := time.Since(start)
		meter.Add(d)
		e := tunnel.New(u.Tunnels, l, req.ID, true)
		e.Node, e.Service, e.Addr, e.Peer = info.Name, svc.Name, svc.Addr, req.Proxy
		e.ConnectRTT = latency.Ms(d)
		if err := u.Tunnels.Add(e); err != nil {
			rmu.Lock()
			refused = err
			rmu.Unlock()
			return nil
		}
		return e
	}}
	dialed := make(chan error, 1)
	err := n.h.Engine.DialWithHandler("tcp", svc.Addr, cfg.Dial(), handler, func(c *fib.Connection, err error) {
		if err == nil && c.Attachment() == nil {
			rmu.Lock()
			err = refused
			rmu.Unlock()
			if err == nil {
				err = tunnel.ErrFull
			}
		}
		dialed <- err
	})
	if err == nil {
		err = <-dialed
	}
	if err != nil {
		meter.Lost()
		return nil, err
	}
	n.log.Debugf("tunnel %d open: proxy %q to service %q at %s", req.ID, req.Proxy, svc.Name, svc.Addr)
	return &proto.TunnelOpenRsp{ID: req.ID, Addr: svc.Addr}, nil
}

// Uplinks is the node's connections to hubs.
func (n *Node) Uplinks() []*uplink.Uplink {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.uplinks)
}

func (n *Node) uplink(name string) *uplink.Uplink {
	for _, u := range n.Uplinks() {
		if u.Name == name {
			return u
		}
	}
	return nil
}

// TunnelView is a tunnel as the pages show it.
type TunnelView struct {
	Hub string `json:"hub"`
	tunnel.Snapshot
}

// Tunnels is every open tunnel.
func (n *Node) Tunnels() []TunnelView {
	out := []TunnelView{}
	for _, u := range n.Uplinks() {
		for _, s := range u.Tunnels.List() {
			out = append(out, TunnelView{Hub: u.Name, Snapshot: s})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}
