// Package uplink is a node's or client's connection to a tunnel server: a
// pool of links that keep dialing until they connect, register each time
// they connect, and dial again once they lose the server. The uplink pings
// the server to measure the link, over the registered link the server
// numbered lowest. Each tunnel goes over one link of the pool throughout, so
// its frames keep their order, and closes when that link loses the server.
//
// An uplink runs on goroutines of its own, one for each link of its pool
// and one that pings, which wait on dials and calls; nothing of it blocks a
// fib worker. Every dial and call has its timeout from the config.
package uplink

import (
	"errors"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	fib "github.com/lesismal/fib"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/tunnel/internal/bodycrypt"
	"github.com/lesismal/fit/tunnel/internal/latency"
	"github.com/lesismal/fit/tunnel/internal/link"
	"github.com/lesismal/fit/tunnel/internal/proto"
	"github.com/lesismal/fit/tunnel/internal/transport"
	"github.com/lesismal/fit/tunnel/internal/tunnel"

	"github.com/lesismal/fit/tunnel/internal/transport/quic"

	// Servers may be reached over WebSocket.
	_ "github.com/lesismal/fit/tunnel/internal/transport/ws"
)

// Dials is the addresses a link tries, in order: the endpoints not
// disabled, a quic one's with how its certificate is checked.
func Dials(cfg *config.TunnelUplink) []string {
	var out []string
	for _, e := range cfg.Endpoints {
		if e.Disabled || e.Address == "" {
			continue
		}
		u := e.Address
		if e.Protocol() == config.TunnelQUIC {
			u = quic.DialURL(strings.TrimPrefix(u, "quic://"), cfg.QUICPin, cfg.QUICInsecure)
		}
		out = append(out, u)
	}
	return out
}

// Same reports whether a and b are the same uplink, so that one moved from
// a to b keeps its links: everything but its name, which the role keys it
// by, is compared.
func Same(a, b *config.TunnelUplink) bool {
	return a.Password == b.Password && a.BodyPassword == b.BodyPassword && a.QUICPin == b.QUICPin &&
		a.QUICInsecure == b.QUICInsecure && a.Pool() == b.Pool() && a.Dial() == b.Dial() && a.Call() == b.Call() &&
		a.Retry() == b.Retry() && a.Ping() == b.Ping() && a.PingWait() == b.PingWait() &&
		slices.EqualFunc(a.Endpoints, b.Endpoints, func(x, y config.TunnelEndpoint) bool {
			return x.Address == y.Address && x.Disabled == y.Disabled
		})
}

// The states of an uplink.
const (
	StateConnecting = "connecting"
	StateRegistered = "registered"
	StateFailed     = "failed"
	StateStopped    = "stopped"
)

// Status is an uplink as a status page shows it.
type Status struct {
	Name    string    `json:"name"`
	Addr    string    `json:"addr"`
	State   string    `json:"state"`
	Error   string    `json:"error,omitempty"`
	Server  string    `json:"server,omitempty"`
	PeerID  uint32    `json:"peer_id,omitempty"`
	Since   time.Time `json:"since,omitempty"`
	Tunnels int       `json:"tunnels"`
	// RTT is the round trip to the server, as the pings measure it.
	RTT latency.Stats `json:"rtt"`
	// BodyEncrypted reports whether frame bodies are encrypted.
	BodyEncrypted bool `json:"body_encrypted"`
	// PoolSize is how many connections the uplink makes, Pool how many of
	// them are registered now, and Places the places the server gave those
	// among the connections of the peer's name, lowest first.
	PoolSize int   `json:"pool_size"`
	Pool     int   `json:"pool"`
	Places   []int `json:"places"`
	// Links are the registered connections, by their places, with the
	// protocol each one took, and Protos how many took each.
	Links  []LinkStatus   `json:"links"`
	Protos map[string]int `json:"protos"`
	// Endpoints, QUICPin and QUICInsecure are the settings, for the web
	// page to edit, and HasPassword and HasBodyPassword whether the
	// passwords are set, which the page does not get.
	Endpoints       []EndpointView `json:"endpoints"`
	QUICPin         string         `json:"quic_pin,omitempty"`
	QUICInsecure    bool           `json:"quic_insecure,omitempty"`
	HasPassword     bool           `json:"has_password"`
	HasBodyPassword bool           `json:"has_body_password"`
}

// EndpointView is an endpoint as the pages show it.
type EndpointView struct {
	Protocol string `json:"protocol"`
	Address  string `json:"address"`
	Enabled  bool   `json:"enabled"`
}

// LinkStatus is a registered connection of an uplink.
type LinkStatus struct {
	Place int    `json:"place"`
	Proto string `json:"proto"`
	Addr  string `json:"addr"`
}

// Uplink is the connection to one server.
type Uplink struct {
	// Name is the server's, as the role names it.
	Name    string
	Config  *config.TunnelUplink
	Tunnels *tunnel.Table
	// OnRegistered, if set, runs each time the uplink registers its first
	// connection, after it had none.
	OnRegistered func(*Uplink)
	// OnPing, if set, adds to each ping what else the peer measured.
	OnPing func(*proto.PingReq)
	// OnPong, if set, gets the server's answer to each ping.
	OnPong func(*Uplink, *proto.PingRsp)

	engine  *fib.Engine
	log     *logs.Logger
	info    func() proto.Info
	router  *link.Router
	coder   *bodycrypt.Coder
	seq     atomic.Uint32
	rtt     latency.Meter
	pinging atomic.Bool

	mu sync.Mutex
	// pool is every link open, with the address it was dialed on, and conns
	// those registered, with what their registrations were answered with.
	pool    map[*link.Link]string
	conns   map[*link.Link]proto.RegisterRsp
	state   string
	lastErr string
	server  string
	since   time.Time
	stopped bool
	stop    chan struct{}
}

// New is an uplink to the server cfg names, which registers as what info
// returns, its tunnels bounded by limit. setup registers the methods the
// server may call on it.
func New(engine *fib.Engine, cfg *config.TunnelUplink, limit *tunnel.Limit, log *logs.Logger, info func() proto.Info, setup func(u *Uplink, r *link.Router)) *Uplink {
	u := &Uplink{Name: cfg.ServerName(), Config: cfg, Tunnels: tunnel.NewTable(limit), engine: engine, log: log, info: info, pool: map[*link.Link]string{}, conns: map[*link.Link]proto.RegisterRsp{}, state: StateConnecting, stop: make(chan struct{})}
	if cfg.BodyPassword != "" {
		coder, err := bodycrypt.New(cfg.BodyPassword)
		if err != nil {
			u.state, u.lastErr = StateFailed, err.Error()
		}
		u.coder = coder
	}
	r := link.NewRouter()
	r.Log = log
	u.Tunnels.Register(r)
	r.HandleNotify(proto.MethodKick, func(_ *link.Link, body []byte) {
		u.fail("kicked by the server: "+string(body), true)
	}, true)
	if setup != nil {
		setup(u, r)
	}
	u.router = r
	return u
}

// Start dials the pool's links to the server, each of which goes on dialing
// until it connects, and again whenever it loses the server, until the
// uplink stops.
func (u *Uplink) Start() {
	u.mu.Lock()
	failed := u.state == StateFailed
	u.mu.Unlock()
	if failed {
		// The config is wrong, which dialing again does not fix.
		return
	}
	go u.pingLoop()
	for range u.Config.Pool() {
		go u.keep()
	}
}

// keep keeps a link of the pool to the server: it dials, registers, and
// once the link closes dials again, an interval after each failure or
// loss, until the uplink stops.
func (u *Uplink) keep() {
	for {
		if l, err := u.dial(); err != nil {
			u.fail(err.Error(), false)
		} else {
			u.register(l)
			<-l.Done()
			u.lost(l)
		}
		t := time.NewTimer(u.Config.Retry())
		select {
		case <-u.stop:
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// dial opens a link to the server, unless the uplink stopped: on the first
// of its addresses that it connects to, in the order of their priority.
func (u *Uplink) dial() (*link.Link, error) {
	dials := Dials(u.Config)
	if len(dials) == 0 {
		return nil, errors.New("no address to dial")
	}
	var errs []string
	for _, addr := range dials {
		var l *link.Link
		_, err := transport.Dial(u.engine, addr, u.Config.Dial(), func(c transport.Conn) transport.Handler {
			l = link.New(c, u.router, u.coder)
			return l
		})
		shown, _, _ := strings.Cut(addr, "?")
		if err != nil {
			errs = append(errs, shown+": "+err.Error())
			select {
			case <-u.stop:
				return nil, ErrStopped
			default:
			}
			continue
		}
		u.mu.Lock()
		defer u.mu.Unlock()
		if u.stopped {
			_ = l.Close()
			return nil, ErrStopped
		}
		u.pool[l] = shown
		if len(errs) > 0 {
			u.log.Infof("server %s: fell back to %s, after %s", u.Name, shown, strings.Join(errs, "; "))
		}
		return l, nil
	}
	return nil, errors.New(strings.Join(errs, "; "))
}

// ErrStopped is the error of a dial of an uplink that stopped.
var ErrStopped = errors.New("tunnel: uplink stopped")

// register authenticates and registers on l, which has just connected,
// and closes it if that fails.
func (u *Uplink) register(l *link.Link) {
	req := proto.RegisterReq{Info: u.info()}
	proto.Sign(&req, u.Config.Password)
	var rsp proto.RegisterRsp
	if err := l.Call(proto.MethodRegister, &req, &rsp, u.Config.Call()); err != nil {
		u.fail("register: "+err.Error(), false)
		u.log.Warnf("server %s: registering failed: %v", u.Name, err)
		_ = l.Close()
		return
	}
	u.mu.Lock()
	if _, ok := u.pool[l]; u.stopped || !ok {
		u.mu.Unlock()
		return
	}
	select {
	case <-l.Done():
		u.mu.Unlock()
		return
	default:
	}
	first := len(u.conns) == 0
	u.conns[l] = rsp
	u.state, u.lastErr, u.server = StateRegistered, "", rsp.Server
	if first {
		u.since = time.Now()
	}
	f := u.OnRegistered
	u.mu.Unlock()
	u.log.Debugf("server %s: registered with %q as peer %d, link %d in place %d", u.Name, rsp.Server, rsp.Peer, rsp.Conn, rsp.Index)
	if !first {
		return
	}
	u.rtt.Reset()
	if f != nil {
		f(u)
	}
	// Measure the link at once, rather than an interval from now.
	go u.ping()
}

// lost records that l lost the server, and closes the tunnels it carried.
func (u *Uplink) lost(l *link.Link) {
	u.mu.Lock()
	delete(u.pool, l)
	_, was := u.conns[l]
	delete(u.conns, l)
	none := len(u.conns) == 0
	if none && u.state == StateRegistered {
		u.state = StateConnecting
	}
	u.mu.Unlock()
	if !was {
		return
	}
	if none {
		u.rtt.Reset()
	}
	u.Tunnels.CloseLink(l, "server connection lost")
}

// fail records why the uplink is not registered: always, or only while
// none of its connections is.
func (u *Uplink) fail(msg string, always bool) {
	u.mu.Lock()
	if !u.stopped && (always || len(u.conns) == 0) {
		u.state, u.lastErr = StateFailed, msg
	}
	u.mu.Unlock()
}

// pingLoop pings the server every PingInterval until the uplink stops.
func (u *Uplink) pingLoop() {
	t := time.NewTicker(u.Config.Ping())
	defer t.Stop()
	for {
		select {
		case <-u.stop:
			return
		case <-t.C:
			u.ping()
		}
	}
}

// ping measures a round trip to the server, telling it what the uplink
// and OnPing measured before.
func (u *Uplink) ping() {
	if !u.pinging.CompareAndSwap(false, true) {
		return
	}
	defer u.pinging.Store(false)
	c, err := u.Client()
	if err != nil {
		return
	}
	req := proto.PingReq{RTT: u.rtt.Stats()}
	if u.OnPing != nil {
		u.OnPing(&req)
	}
	var rsp proto.PingRsp
	start := time.Now()
	err = c.Call(proto.MethodPing, &req, &rsp, u.Config.PingWait())
	d := time.Since(start)
	if now, _ := u.Client(); now != c {
		// The connection the ping went over is gone, which says nothing of
		// the link.
		return
	}
	if err != nil {
		u.rtt.Lost()
		return
	}
	u.rtt.Add(d)
	if u.OnPong != nil {
		u.OnPong(u, &rsp)
	}
}

// RTT is the round trip to the server.
func (u *Uplink) RTT() latency.Stats { return u.rtt.Stats() }

// ErrNotRegistered is the error of a call on an uplink that is not
// registered.
var ErrNotRegistered = errors.New("tunnel: not registered with the server")

// linksLocked is the registered links, by the places the server gave
// them. Callers hold mu.
func (u *Uplink) linksLocked() []*link.Link {
	links := make([]*link.Link, 0, len(u.conns))
	for c := range u.conns {
		links = append(links, c)
	}
	sort.Slice(links, func(i, j int) bool { return u.conns[links[i]].Index < u.conns[links[j]].Index })
	return links
}

// Client is the registered link the server numbered lowest, which the
// uplink calls the server over.
func (u *Uplink) Client() (*link.Link, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	links := u.linksLocked()
	if u.stopped || len(links) == 0 {
		return nil, ErrNotRegistered
	}
	return links[0], nil
}

// NextTunnelID is the ID of a new tunnel, and the registered link of the
// pool that carries it: the one that carries the fewest tunnels, the links
// taking turns among those that carry as few.
func (u *Uplink) NextTunnelID() (uint64, *link.Link, error) {
	load := u.Tunnels.Load()
	u.mu.Lock()
	defer u.mu.Unlock()
	links := u.linksLocked()
	if u.stopped || len(links) == 0 {
		return 0, nil, ErrNotRegistered
	}
	seq := u.seq.Add(1)
	link := links[int(seq%uint32(len(links)))]
	for _, c := range links {
		if load[c] < load[link] {
			link = c
		}
	}
	if load[link] >= proto.MaxTunnelsPerLink {
		return 0, nil, ErrLinksFull
	}
	return proto.TunnelID(u.conns[link].Conn, seq), link, nil
}

// ErrLinksFull is the error of a tunnel that every link of the pool has no
// room for: each carries proto.MaxTunnelsPerLink.
var ErrLinksFull = errors.New("tunnel: every link to the server carries as many tunnels as it may; a larger pool_size carries more")

// Call calls method on the server.
func (u *Uplink) Call(method proto.Method, req, rsp any) error {
	c, err := u.Client()
	if err != nil {
		return err
	}
	return c.Call(method, req, rsp, u.Config.Call())
}

// Stop closes the connections and their tunnels, and dials no more.
func (u *Uplink) Stop() {
	u.mu.Lock()
	if u.stopped {
		u.mu.Unlock()
		return
	}
	u.stopped = true
	u.state = StateStopped
	close(u.stop)
	pool := make([]*link.Link, 0, len(u.pool))
	for l := range u.pool {
		pool = append(pool, l)
	}
	u.mu.Unlock()
	u.Tunnels.CloseAll("stopped")
	for _, l := range pool {
		_ = l.Close()
	}
}

// Status is the uplink's state.
func (u *Uplink) Status() Status {
	u.mu.Lock()
	defer u.mu.Unlock()
	cfg := u.Config
	st := Status{Name: u.Name, State: u.state, Error: u.lastErr, Server: u.server, Since: u.since, Tunnels: u.Tunnels.Len(), RTT: u.rtt.Stats(), BodyEncrypted: cfg.BodyPassword != "", PoolSize: cfg.Pool(), Places: []int{},
		Links: []LinkStatus{}, Protos: map[string]int{}, Endpoints: []EndpointView{}, QUICPin: cfg.QUICPin, QUICInsecure: cfg.QUICInsecure, HasPassword: cfg.Password != "", HasBodyPassword: cfg.BodyPassword != ""}
	for _, e := range cfg.Endpoints {
		st.Endpoints = append(st.Endpoints, EndpointView{Protocol: e.Protocol(), Address: e.Address, Enabled: !e.Disabled})
	}
	if d := Dials(cfg); len(d) > 0 {
		st.Addr, _, _ = strings.Cut(d[0], "?")
	}
	for i, c := range u.linksLocked() {
		rsp := u.conns[c]
		addr := u.pool[c]
		if i == 0 {
			st.PeerID = rsp.Peer
			// The address the uplink calls the server over.
			st.Addr = addr
		}
		st.Places = append(st.Places, rsp.Index)
		proto := transport.Scheme(addr)
		st.Links = append(st.Links, LinkStatus{Place: rsp.Index, Proto: proto, Addr: addr})
		st.Protos[proto]++
	}
	st.Pool = len(st.Places)
	return st
}
