// Package proxy is the tunnel's proxy role, tunnel.proxy in fit's
// config. A proxy links to one or more tunnel hubs and
// learns the nodes they have and the services those nodes reach. For each
// forward it listens on a local address, and every connection it accepts
// there becomes a tunnel, which the hub routes to the node and the node
// connects to the service. When several hubs have the node, the tunnels
// are spread over them, more of them through the hubs whose path to the
// node is faster. Its web proxies serve hubs' console pages locally,
// through the hubs' encrypted console sockets. Its console pages show
// the hubs, nodes, forwards and tunnels, and change them; every such
// change is written back into the proxy's member of the config, and
// applied from there.
//
// Everything runs on the listeners' fib engine: forwards, web proxies,
// links and tunnels are its bindings and connections, served on its
// workers; what waits, the opening of a tunnel or a call, runs on goroutines
// of the proxy's own, at most one for each tunnel being opened.
package proxy

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
	"github.com/lesismal/fit/tunnel/internal/link"
	"github.com/lesismal/fit/tunnel/internal/proto"
	"github.com/lesismal/fit/tunnel/internal/tunnel"
	"github.com/lesismal/fit/tunnel/internal/uplink"
)

// ID is the role's, and its member's path in the config.
const ID = "tunnel.proxy"

func init() {
	app.Register(app.Info{
		ID: ID,
		Member: func(c *config.Config) any {
			if c.Tunnel == nil {
				return nil
			}
			return c.Tunnel.Proxy
		},
		New: func(h *app.Host) (app.Role, error) { return New(h), nil },
	})
}

// forward is a forward of the config and its binding: listening on addr,
// or failed to with err.
type forward struct {
	cfg   atomic.Pointer[config.TunnelForward]
	index int
	addr  string
	err   string
}

// Proxy is the running role.
type Proxy struct {
	h     *app.Host
	log   *logs.Logger
	limit *tunnel.Limit

	cfg atomic.Pointer[config.TunnelProxy]

	// amu is held for a whole Apply or Stop, and guards what they change.
	amu     sync.Mutex
	started bool
	stopped bool
	console *common.Console
	quit    chan struct{}

	mu       sync.Mutex
	uplinks  []*uplink.Uplink
	nodes    map[*uplink.Uplink]*proto.NodeList
	forwards []*forward
	proxies  []*webProxy
}

// New returns the role, which its first Apply starts.
func New(h *app.Host) *Proxy {
	c := &Proxy{h: h, log: h.Log, limit: tunnel.NewLimit(config.DefaultTunnelTunnels), nodes: map[*uplink.Uplink]*proto.NodeList{}, quit: make(chan struct{})}
	c.console = common.NewConsole(h.Engine, "tunnel.proxy", h.Log, c.api)
	return c
}

// Config is the member the proxy runs.
func (c *Proxy) Config() *config.TunnelProxy { return c.cfg.Load() }

// Apply moves the proxy to member, a *config.TunnelProxy. A hub whose
// settings are unchanged keeps its links and tunnels, and one that changed
// links anew; a forward or web proxy on the same address keeps listening,
// and takes its new settings for the connections it accepts from now on.
// At the first Apply, a forward, web proxy or console that does not open
// fails it; after, one that does not open is shown with its error, tried
// again with the next config, and its error returned beside the rest
// applied.
func (c *Proxy) Apply(member any) error {
	cfg, ok := member.(*config.TunnelProxy)
	if !ok || cfg == nil {
		return errors.New("tunnel.proxy: no member")
	}
	c.amu.Lock()
	defer c.amu.Unlock()
	if c.stopped {
		return app.ErrStopped
	}
	var errs []error
	if err := c.console.Apply(cfg.Console); err != nil {
		errs = append(errs, fmt.Errorf("console: %w", err))
	}
	prev := c.cfg.Swap(cfg)
	c.limit.SetMax(cfg.Tunnels())
	errs = append(errs, c.applyForwards(cfg.Forwards)...)
	errs = append(errs, c.applyProxies(cfg.WebProxies)...)
	if !c.started && len(errs) > 0 {
		c.stopAll(context.Background())
		return errors.Join(errs...)
	}
	renamed := prev != nil && prev.ProxyName() != cfg.ProxyName()
	c.mu.Lock()
	old := map[string]*uplink.Uplink{}
	for _, u := range c.uplinks {
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
		u := c.newUplink(sc)
		start = append(start, u)
		next = append(next, u)
	}
	for _, u := range old {
		stop = append(stop, u)
		delete(c.nodes, u)
	}
	c.uplinks = next
	c.mu.Unlock()
	for _, u := range stop {
		u.Stop()
	}
	for _, u := range start {
		u.Start()
	}
	if prev != nil && !renamed && !maps.Equal(prev.Meta, cfg.Meta) {
		info := c.Info()
		for _, u := range kept {
			go func() {
				if err := u.Call(proto.MethodUpdate, &info, nil); err != nil && !errors.Is(err, uplink.ErrNotRegistered) {
					c.log.Warnf("hub %s: telling it the proxy's meta: %v", u.Name, err)
				}
			}()
		}
	}
	if !c.started {
		c.started = true
		go c.sweep()
	}
	return errors.Join(errs...)
}

// sweep closes the tunnels idle past idle_timeout, until the proxy stops.
func (c *Proxy) sweep() {
	for {
		idle := c.Config().Idle()
		t := time.NewTimer(common.SweepEvery(idle))
		select {
		case <-c.quit:
			t.Stop()
			return
		case <-t.C:
			for _, u := range c.Uplinks() {
				u.Tunnels.CloseIdle(idle)
			}
		}
	}
}

// Stop closes the forwards, the web proxies, every link, with its
// tunnels, and the console.
func (c *Proxy) Stop(ctx context.Context) {
	c.amu.Lock()
	defer c.amu.Unlock()
	if c.stopped {
		return
	}
	c.stopped = true
	if c.started {
		close(c.quit)
	}
	c.stopAll(ctx)
}

// stopAll closes what Apply opened. amu is held.
func (c *Proxy) stopAll(ctx context.Context) {
	_ = c.applyForwards(nil)
	_ = c.applyProxies(nil)
	c.mu.Lock()
	uplinks := c.uplinks
	c.uplinks = nil
	c.mu.Unlock()
	for _, u := range uplinks {
		u.Stop()
	}
	c.console.Stop(ctx)
}

// Info is what the proxy tells hubs about itself.
func (c *Proxy) Info() proto.Info {
	cfg := c.Config()
	if cfg == nil {
		return proto.Info{}
	}
	info := proto.NewInfo(cfg.ProxyName(), proto.TypeProxy)
	info.Meta = cfg.Meta
	return info
}

func (c *Proxy) newUplink(sc *config.TunnelUplink) *uplink.Uplink {
	u := uplink.New(c.h.Engine, sc, c.limit, c.log, c.Info, func(u *uplink.Uplink, r *link.Router) {
		// The hub sends the nodes whenever they change.
		r.HandleNotify(proto.MethodNodes, func(_ *link.Link, body []byte) {
			var list proto.NodeList
			if err := link.Decode(body, &list); err == nil {
				c.setNodes(u, &list)
			}
		}, true)
	})
	// Each ping's answer has how fast the nodes' links are.
	u.OnPong = func(u *uplink.Uplink, rsp *proto.PingRsp) { c.setNodeLatency(u, rsp.Nodes) }
	u.OnRegistered = func(u *uplink.Uplink) {
		if _, err := c.RefreshNodes(u); err != nil {
			c.log.Warnf("hub %s: listing the nodes: %v", u.Name, err)
		}
	}
	return u
}

func (c *Proxy) setNodes(u *uplink.Uplink, list *proto.NodeList) {
	c.mu.Lock()
	// The answer to RefreshNodes may arrive after the notify of a later
	// change.
	if slices.Contains(c.uplinks, u) && !list.Older(c.nodes[u]) {
		c.nodes[u] = list
	}
	c.mu.Unlock()
}

// setNodeLatency records how fast the links of the nodes of u's hub
// are.
func (c *Proxy) setNodeLatency(u *uplink.Uplink, nodes map[string]proto.NodeLatency) {
	c.mu.Lock()
	defer c.mu.Unlock()
	old := c.nodes[u]
	if old == nil {
		return
	}
	// The pages may be reading the old list.
	list := &proto.NodeList{Hub: old.Hub, Nodes: slices.Clone(old.Nodes), Epoch: old.Epoch, Version: old.Version}
	for i := range list.Nodes {
		if l, ok := nodes[list.Nodes[i].Info.Name]; ok {
			list.Nodes[i].NodeLatency = l
		}
	}
	c.nodes[u] = list
}

// RefreshNodes asks the hub of u for its nodes. It blocks, for at most
// the hub's call_timeout.
func (c *Proxy) RefreshNodes(u *uplink.Uplink) (*proto.NodeList, error) {
	var list proto.NodeList
	if err := u.Call(proto.MethodNodes, nil, &list); err != nil {
		return nil, err
	}
	c.setNodes(u, &list)
	return &list, nil
}

// Nodes is what the hub of u last said its nodes were.
func (c *Proxy) Nodes(u *uplink.Uplink) *proto.NodeList {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.nodes[u]
}

// SetNodeServices has the hub of u set the services of node. It
// blocks, for at most the hub's call_timeout.
func (c *Proxy) SetNodeServices(u *uplink.Uplink, node string, services []proto.Service) (proto.Info, error) {
	var info proto.Info
	if services == nil {
		services = []proto.Service{}
	}
	err := u.Call(proto.MethodSetNodeServices, &proto.SetServicesReq{Node: node, Services: services}, &info)
	if err == nil {
		_, _ = c.RefreshNodes(u)
	}
	return info, err
}

// Uplinks is the proxy's connections to hubs.
func (c *Proxy) Uplinks() []*uplink.Uplink {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.uplinks)
}

func (c *Proxy) uplink(name string) *uplink.Uplink {
	for _, u := range c.Uplinks() {
		if u.Name == name {
			return u
		}
	}
	return nil
}

// pick is the uplink a tunnel of f goes through: the hub f names, or else
// one of the registered ones that list f's node with f's service, as Choose
// picks it.
func (c *Proxy) pick(f *config.TunnelForward) (*uplink.Uplink, error) {
	if f.Hub != "" {
		u := c.uplink(f.Hub)
		if u == nil {
			return nil, fmt.Errorf("no hub named %q", f.Hub)
		}
		return u, nil
	}
	var us []*uplink.Uplink
	var paths []Path
	for _, u := range c.Uplinks() {
		if _, err := u.Proxy(); err != nil {
			continue
		}
		list := c.Nodes(u)
		if list == nil {
			continue
		}
		for _, p := range list.Nodes {
			if p.Info.Name == f.Node {
				if _, ok := p.Info.FindService(f.Service); ok {
					us = append(us, u)
					paths = append(paths, Path{Hub: u.RTT(), Node: p.RTT, Tunnels: u.Tunnels.Len()})
				}
			}
		}
	}
	if len(us) == 0 {
		return nil, fmt.Errorf("no hub has node %q with service %q", f.Node, f.Service)
	}
	return us[Choose(paths)], nil
}

// applyForwards listens for the forwards the config adds, stops listening
// for those it leaves out, and moves the rest to their new settings. It
// returns why each that failed to listen did. amu is held.
func (c *Proxy) applyForwards(want []config.TunnelForward) []error {
	c.mu.Lock()
	have := map[string]*forward{}
	for _, f := range c.forwards {
		have[f.cfg.Load().Listen] = f
	}
	c.mu.Unlock()
	keep := map[*forward]bool{}
	for _, fc := range want {
		if f := have[fc.Listen]; f != nil && f.index >= 0 {
			keep[f] = true
		}
	}
	for _, f := range have {
		if !keep[f] && f.index >= 0 {
			_ = c.h.Engine.RemoveBinding(f.index)
			c.log.Infof("stopped forwarding %s", f.addr)
		}
	}
	var errs []error
	next := make([]*forward, 0, len(want))
	for i := range want {
		fc := &want[i]
		if f := have[fc.Listen]; f != nil && keep[f] {
			f.cfg.Store(fc)
			next = append(next, f)
			continue
		}
		f := c.listen(fc)
		if f.err != "" {
			errs = append(errs, fmt.Errorf("forward %s: %s", fc.Listen, f.err))
		}
		next = append(next, f)
	}
	c.mu.Lock()
	c.forwards = next
	c.mu.Unlock()
	return errs
}

// listen opens f's listener.
func (c *Proxy) listen(fc *config.TunnelForward) *forward {
	f := &forward{index: -1}
	f.cfg.Store(fc)
	handler := &tunnel.Handler{
		Open: func(conn *fib.Connection) *tunnel.Endpoint {
			fc := f.cfg.Load()
			u, err := c.pick(fc)
			if err != nil {
				c.log.Warnf("forward %s refused a connection: %v", fc.Listen, err)
				return nil
			}
			id, l, err := u.NextTunnelID()
			if err != nil {
				c.log.Warnf("forward %s refused a connection: hub %s: %v", fc.Listen, u.Name, err)
				return nil
			}
			e := tunnel.New(u.Tunnels, l, id, false)
			e.Node, e.Service, e.Peer = fc.Node, fc.Service, fc.Listen
			if err := u.Tunnels.Add(e); err != nil {
				c.log.Warnf("forward %s refused a connection: %v", fc.Listen, err)
				return nil
			}
			return e
		},
		Opened: func(e *tunnel.Endpoint) {
			// One goroutine for each tunnel opening, which the Limit of
			// the tunnels open bounds.
			go c.open(e, f.cfg.Load())
		},
	}
	i, err := c.h.Engine.AddBinding(fib.Binding{Addr: fc.Listen, Handler: handler})
	if err != nil {
		f.err = err.Error()
		c.log.Warnf("forward %s: %v", fc.Listen, err)
		return f
	}
	f.index = i
	if addrs, err := c.h.Engine.BindingAddrs(i); err == nil && len(addrs) > 0 {
		f.addr = addrs[0].String()
	}
	c.log.Infof("forwarding %s to node %q service %q", f.addr, fc.Node, fc.Service)
	return f
}

// open asks the hub to open e's tunnel, and lets e read its connection
// once it is open. It waits for the hub, for at most open_timeout.
func (c *Proxy) open(e *tunnel.Endpoint, fc *config.TunnelForward) {
	var rsp proto.TunnelOpenRsp
	req := proto.TunnelOpenReq{ID: e.ID, Node: fc.Node, Service: fc.Service}
	l, ok := e.Link().(*link.Link)
	if !ok {
		e.Close("no link")
		return
	}
	if err := l.Call(proto.MethodTunnelOpen, &req, &rsp, c.Config().Open()); err != nil {
		c.log.Warnf("tunnel %d to node %q service %q: %v", e.ID, fc.Node, fc.Service, err)
		e.Close("open failed: " + err.Error())
		return
	}
	e.SetAddr(rsp.Addr)
	e.SetReady()
	c.log.Debugf("tunnel %d open to node %q service %q at %s", e.ID, fc.Node, fc.Service, rsp.Addr)
}

// ForwardView is a forward as the pages show it.
type ForwardView struct {
	Listen  string `json:"listen"`
	Node    string `json:"node"`
	Service string `json:"service"`
	Hub     string `json:"hub,omitempty"`
	Addr    string `json:"addr,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Forwards is the proxy's forwards.
func (c *Proxy) Forwards() []ForwardView {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]ForwardView, len(c.forwards))
	for i, f := range c.forwards {
		fc := f.cfg.Load()
		out[i] = ForwardView{Listen: fc.Listen, Node: fc.Node, Service: fc.Service, Hub: fc.Hub, Addr: f.addr, Error: f.err}
	}
	return out
}

// TunnelView is a tunnel as the pages show it.
type TunnelView struct {
	Hub string `json:"hub"`
	tunnel.Snapshot
}

// Tunnels is every open tunnel.
func (c *Proxy) Tunnels() []TunnelView {
	out := []TunnelView{}
	for _, u := range c.Uplinks() {
		for _, s := range u.Tunnels.List() {
			out = append(out, TunnelView{Hub: u.Name, Snapshot: s})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}
