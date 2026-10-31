package media

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lesismal/fib"
	"github.com/lesismal/fib/webrtc/ice"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/observe"
)

// ProxyID is the proxy role's ID, and its member's path in the config.
const ProxyID = "webrtc.proxy"

// The bounds the proxy keeps without a setting of their own.
const (
	// maxPaths bounds the peer addresses one session relays: a peer checks
	// from each of its candidates, and an address change or a restart adds
	// one.
	maxPaths = 8
	// maxUfrags bounds the ufrags a session keeps, its answer's and its ICE
	// restarts', the oldest forgotten first.
	maxUfrags = 4
	// maxQueued bounds the datagrams a relay holds while its socket toward
	// the media server opens; ICE and DTLS send theirs again.
	maxQueued = 8
	// dialTimeout bounds opening that socket.
	dialTimeout = 5 * time.Second
)

func init() {
	app.Register(app.Info{
		ID: ProxyID,
		Member: func(c *config.Config) any {
			if c.WebRTC == nil || c.WebRTC.Proxy == nil {
				return nil
			}
			return c.WebRTC.Proxy
		},
		New: func(h *app.Host) (app.Role, error) { return NewProxy(h), nil },
	})
}

// proxies are the proxy roles running, by the engine the routes' modules
// are given, as servers are.
var proxies sync.Map // *fib.Engine -> *Proxy

// LookupProxy returns the proxy role the modules of engine use, or nil.
func LookupProxy(engine *fib.Engine) *Proxy {
	if p, ok := proxies.Load(engine); ok {
		return p.(*Proxy)
	}
	return nil
}

// Proxy is the role webrtc.proxy: the UDP port the webrtc_proxy handler's
// answers name, the sessions it proxied, by fit's ID and by their media
// servers' ufrags, and the relays of their peers' addresses.
//
// The handler records each session with Add once its media server has
// answered; a peer's first datagram from a new address must then be a STUN
// Binding request whose USERNAME starts with one of the session's ufrags
// and whose MESSAGE-INTEGRITY its ice-pwd signs, and the relay it opens
// dials the media server's address from a UDP socket of its own, so that
// the media server sees each peer address as a peer of its own, a
// peer-reflexive candidate, and its replies come back to the relay that
// sent for them. Everything else that comes first is dropped.
type Proxy struct {
	engine, key *fib.Engine
	log         *logs.Logger

	set atomic.Pointer[proxySettings]

	applyMu sync.Mutex
	// mu guards what follows; it is never held while a connection is
	// called.
	mu       sync.Mutex
	port     *proxyPort
	stopped  bool
	sessions map[string]*proxySession
	ufrags   map[string]*route
	relays   int
	// reserved counts the offers under way, which max_sessions counts.
	reserved int

	m proxyMeters
}

type proxySettings struct {
	maxSessions, maxRelays int
	sessionIdle, idle      time.Duration
	public                 []netip.Addr
}

// proxyPort is the binding the relays' peers reach.
type proxyPort struct {
	listen  string
	binding int
	bound   []netip.AddrPort
}

// proxySession is a session a media server answered through fit.
type proxySession struct {
	ID string
	// Backend is the upstream address that answered, and Location the
	// path of the session's URL there.
	Backend, Location string
	ufrags            []string
	relays            map[*relay]struct{}
	timer             *time.Timer
	gone              bool
}

// route is what a media server's ufrag leads to.
type route struct {
	s     *proxySession
	pwd   []byte
	media netip.AddrPort
}

// Cred is a media server's ICE credentials, from its answer, and the
// address its peers are relayed to.
type Cred struct {
	Ufrag, Pwd string
	Media      netip.AddrPort
}

// NewProxy returns the role, which its first Apply starts.
func NewProxy(h *app.Host) *Proxy {
	return &Proxy{engine: h.Engine, key: h.Upstream, log: h.Log,
		sessions: map[string]*proxySession{}, ufrags: map[string]*route{}}
}

// Apply moves the role to member, a *config.WebRTCProxy.
func (p *Proxy) Apply(member any) error {
	cfg, ok := member.(*config.WebRTCProxy)
	if !ok || cfg == nil {
		return errors.New("webrtc.proxy: no member")
	}
	st := &proxySettings{maxSessions: cfg.Sessions(), maxRelays: cfg.Relays(),
		sessionIdle: cfg.SessionIdle(), idle: cfg.Idle()}
	for _, s := range cfg.PublicIPs {
		a, err := netip.ParseAddr(s)
		if err != nil {
			return fmt.Errorf("public_ips: %w", err)
		}
		st.public = append(st.public, a.Unmap())
	}
	p.applyMu.Lock()
	defer p.applyMu.Unlock()
	p.mu.Lock()
	old, stopped := p.port, p.stopped
	p.mu.Unlock()
	if stopped {
		return app.ErrStopped
	}
	if old != nil && old.listen == cfg.Listen {
		p.set.Store(st)
		return nil
	}
	// The new port is bound before the old one goes, unless it is the old
	// one's: then the old one goes first.
	if err := checkFree(cfg.Listen); err != nil {
		if old == nil {
			return err
		}
		p.closePort(old, false)
		old = nil
		if err := checkFree(cfg.Listen); err != nil {
			return err
		}
	}
	port, err := p.bind(cfg.Listen)
	if err != nil {
		return err
	}
	if old != nil {
		p.closePort(old, false)
	}
	p.set.Store(st)
	p.mu.Lock()
	p.port = port
	p.mu.Unlock()
	proxies.Store(p.key, p)
	if p.m.applied.Swap(true) {
		p.m.restarts.Add(1)
		p.log.Warnf("the media proxy moved to %s, ending its sessions", port.bound[0])
	} else {
		p.log.Infof("media proxy on %s", port.bound[0])
	}
	return nil
}

func (p *Proxy) bind(listen string) (*proxyPort, error) {
	i, err := p.engine.AddBinding(fib.Binding{Network: "udp", Addr: listen, Handler: portHandler{p}})
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", listen, err)
	}
	port := &proxyPort{listen: listen, binding: i}
	addrs, err := p.engine.BindingAddrs(i)
	for _, a := range addrs {
		if ua, ok := a.(*net.UDPAddr); ok {
			port.bound = append(port.bound, ua.AddrPort())
		}
	}
	if err != nil || len(port.bound) == 0 {
		_ = p.engine.RemoveBinding(i)
		return nil, fmt.Errorf("listen on %s: no address", listen)
	}
	return port, nil
}

// Stop ends every relay and forgets every session.
func (p *Proxy) Stop(context.Context) {
	p.applyMu.Lock()
	defer p.applyMu.Unlock()
	p.mu.Lock()
	old := p.port
	p.stopped = true
	p.mu.Unlock()
	proxies.CompareAndDelete(p.key, p)
	if old != nil {
		p.closePort(old, true)
	}
}

// closePort forgets every session, and with them their relays, and removes
// the binding, closing the peers' connections on it.
func (p *Proxy) closePort(old *proxyPort, stop bool) {
	p.mu.Lock()
	var relays []*relay
	for _, s := range p.sessions {
		relays = append(relays, p.forgetLocked(s, endProxyReload)...)
	}
	p.port = nil
	p.mu.Unlock()
	for _, r := range relays {
		r.close()
	}
	if err := p.engine.RemoveBinding(old.binding); err != nil && !stop {
		p.log.Warnf("closing the media proxy's port: %v", err)
	}
}

// Addrs returns the addresses the proxied answers name, or nil while the
// role has no port.
func (p *Proxy) Addrs() []netip.AddrPort {
	p.mu.Lock()
	port := p.port
	p.mu.Unlock()
	st := p.set.Load()
	if port == nil || st == nil {
		return nil
	}
	var out []netip.AddrPort
	add := func(a netip.Addr, portNum uint16) {
		ap := netip.AddrPortFrom(a.Unmap(), portNum)
		for _, o := range out {
			if o == ap {
				return
			}
		}
		out = append(out, ap)
	}
	for _, b := range port.bound {
		ip := b.Addr().Unmap()
		switch {
		case len(st.public) > 0:
			for _, a := range st.public {
				add(a, b.Port())
			}
		case ip.IsUnspecified():
			hosts, _ := ice.HostAddrs()
			for _, h := range hosts {
				if !ip.Is4() || h.Is4() {
					add(h, b.Port())
				}
			}
		default:
			add(ip, b.Port())
		}
	}
	return out
}

// Reserve counts an offer under way against max_sessions, and reports false
// when it would pass it; a true needs its Unreserve, or an Add.
func (p *Proxy) Reserve() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.port == nil || len(p.sessions)+p.reserved >= p.set.Load().maxSessions {
		p.m.refused.Add(1)
		return false
	}
	p.reserved++
	return true
}

// Unreserve gives back a Reserve whose offer made no session.
func (p *Proxy) Unreserve() {
	p.mu.Lock()
	p.reserved--
	p.mu.Unlock()
}

// Add records a session that backend answered, its URL there location,
// with its answer's credentials, in place of a Reserve, and returns its ID
// in fit; it fails when the role has stopped or moved since the Reserve, or
// a ufrag is another session's.
func (p *Proxy) Add(backend, location string, creds []Cred) (string, error) {
	var b [16]byte
	_, _ = rand.Read(b[:])
	s := &proxySession{ID: hex.EncodeToString(b[:]), Backend: backend, Location: location, relays: map[*relay]struct{}{}}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reserved--
	if p.port == nil {
		return "", errors.New("webrtc.proxy is not running")
	}
	if err := p.addUfragsLocked(s, creds); err != nil {
		return "", err
	}
	p.sessions[s.ID] = s
	p.m.sessions.Add(1)
	p.m.answered.Add(1)
	s.timer = time.AfterFunc(p.set.Load().sessionIdle, func() { p.expire(s) })
	return s.ID, nil
}

// Restart adds the credentials of an ICE restart to the session id.
func (p *Proxy) Restart(id string, creds []Cred) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.sessions[id]
	if s == nil {
		return errors.New("no such session")
	}
	return p.addUfragsLocked(s, creds)
}

func (p *Proxy) addUfragsLocked(s *proxySession, creds []Cred) error {
	for _, c := range creds {
		if r := p.ufrags[c.Ufrag]; r != nil && r.s != s {
			return fmt.Errorf("the ufrag %q is another session's", c.Ufrag)
		}
	}
	var keep netip.AddrPort
	if len(s.ufrags) > 0 {
		if r := p.ufrags[s.ufrags[len(s.ufrags)-1]]; r != nil {
			keep = r.media
		}
	}
	for _, c := range creds {
		if !c.Media.IsValid() {
			// A restart whose answer names no candidate keeps the address.
			c.Media = keep
		}
		if _, ok := p.ufrags[c.Ufrag]; !ok {
			s.ufrags = append(s.ufrags, c.Ufrag)
		}
		p.ufrags[c.Ufrag] = &route{s: s, pwd: []byte(c.Pwd), media: c.Media}
	}
	for len(s.ufrags) > maxUfrags {
		delete(p.ufrags, s.ufrags[0])
		s.ufrags = s.ufrags[1:]
	}
	return nil
}

// Session returns the session id, or nil.
func (p *Proxy) Session(id string) *proxySession {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sessions[id]
}

// End forgets the session id and closes its relays, for its DELETE.
func (p *Proxy) End(id string) {
	p.mu.Lock()
	s := p.sessions[id]
	var relays []*relay
	if s != nil {
		relays = p.forgetLocked(s, endProxyDelete)
	}
	p.mu.Unlock()
	for _, r := range relays {
		r.close()
	}
}

// expire forgets s when nothing has been relayed for it since its timer
// was set.
func (p *Proxy) expire(s *proxySession) {
	p.mu.Lock()
	if !s.gone && len(s.relays) == 0 {
		p.forgetLocked(s, endProxyExpired)
	}
	p.mu.Unlock()
}

// forgetLocked forgets s, and returns its relays for the caller to close
// once p.mu is released.
func (p *Proxy) forgetLocked(s *proxySession, why int) []*relay {
	if s.gone {
		return nil
	}
	s.gone = true
	s.timer.Stop()
	delete(p.sessions, s.ID)
	for _, u := range s.ufrags {
		if r := p.ufrags[u]; r != nil && r.s == s {
			delete(p.ufrags, u)
		}
	}
	p.m.sessions.Add(-1)
	p.m.ended[why].Add(1)
	relays := make([]*relay, 0, len(s.relays))
	for r := range s.relays {
		relays = append(relays, r)
	}
	return relays
}

// Stats returns the sessions and relays the role holds, and the ufrags
// it routes by.
func (p *Proxy) Stats() (sessions, relays, ufrags int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.sessions), p.relays, len(p.ufrags)
}

// proxyMeters are the role's metrics.
type proxyMeters struct {
	sessions, relays    atomic.Int64
	answered, refused   atomic.Uint64
	opened              atomic.Uint64
	toBackend, toClient atomic.Uint64
	restarts            atomic.Uint64
	applied             atomic.Bool
	dropped             [numDrops]atomic.Uint64
	ended               [numProxyEnds]atomic.Uint64
}

// Why a peer's first datagram was dropped, for
// fit_webrtc_proxy_dropped_total.
const (
	dropNotSTUN = iota
	dropUnknown
	dropIntegrity
	dropLimit
	numDrops
)

var dropNames = [numDrops]string{"not_stun", "unknown_ufrag", "integrity", "limit"}

// Why a proxied session was forgotten.
const (
	endProxyDelete = iota
	endProxyExpired
	endProxyReload
	numProxyEnds
)

var proxyEndNames = [numProxyEnds]string{"delete", "expired", "reload"}

// WriteMetrics writes the role's metrics.
func (p *Proxy) WriteMetrics(w observe.MetricWriter) {
	m := &p.m
	w.Family("fit_webrtc_proxy_sessions", "gauge", "Sessions proxied to media servers, kept by their ufrags.")
	w.Sample("fit_webrtc_proxy_sessions", float64(m.sessions.Load()))
	w.Family("fit_webrtc_proxy_relays", "gauge", "Peer addresses relayed to their media servers, each with a socket of its own.")
	w.Sample("fit_webrtc_proxy_relays", float64(m.relays.Load()))
	w.Family("fit_webrtc_proxy_sessions_total", "counter", "Sessions a media server answered through fit.")
	w.Sample("fit_webrtc_proxy_sessions_total", float64(m.answered.Load()))
	w.Family("fit_webrtc_proxy_refused_total", "counter", "Offers answered 503 for max_sessions, or while the proxy does not run.")
	w.Sample("fit_webrtc_proxy_refused_total", float64(m.refused.Load()))
	w.Family("fit_webrtc_proxy_sessions_ended_total", "counter", "Sessions forgotten, by reason: a DELETE, nothing relayed for session_timeout, or the port moved or stopped.")
	for i, name := range proxyEndNames {
		w.Sample("fit_webrtc_proxy_sessions_ended_total", float64(m.ended[i].Load()), "reason", name)
	}
	w.Family("fit_webrtc_proxy_relays_total", "counter", "Relays opened for peers' STUN requests.")
	w.Sample("fit_webrtc_proxy_relays_total", float64(m.opened.Load()))
	w.Family("fit_webrtc_proxy_dropped_total", "counter", "First datagrams of new peer addresses dropped, by reason: not a STUN Binding request, a ufrag no session has, a MESSAGE-INTEGRITY its ice-pwd does not sign, or past max_relays or a session's 8.")
	for i, name := range dropNames {
		w.Sample("fit_webrtc_proxy_dropped_total", float64(m.dropped[i].Load()), "reason", name)
	}
	w.Family("fit_webrtc_proxy_relayed_bytes_total", "counter", "Bytes relayed, by direction.")
	w.Sample("fit_webrtc_proxy_relayed_bytes_total", float64(m.toBackend.Load()), "direction", "to_backend")
	w.Sample("fit_webrtc_proxy_relayed_bytes_total", float64(m.toClient.Load()), "direction", "to_client")
	w.Family("fit_webrtc_proxy_restarts_total", "counter", "Times a change of listen moved the port, ending its sessions.")
	w.Sample("fit_webrtc_proxy_restarts_total", float64(m.restarts.Load()))
}

var (
	_ app.Role    = (*Proxy)(nil)
	_ app.Metered = (*Proxy)(nil)
)
