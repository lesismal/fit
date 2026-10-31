// Package turn is WebRTC's TURN server role, webrtc.turn in fit's config:
// fib's TURN server (webrtc/turn/turnserver) on the listeners' engine, with
// its users, secret, relay addresses, quotas and peer rules from the config.
// See docs/decisions/029-webrtc.md.
//
// The server listens on one UDP binding of the engine, all of the member's
// addresses, and opens each allocation's relayed socket as a binding of its
// own. What it reads on every request, the users and the secret, the peers
// allowed and denied, and the bandwidth, is swapped in place when a config
// changes it, so that the allocations held go on; a change of what fib's
// server is made with, its addresses, realm, lifetimes or allocation quotas,
// starts a new server and closes the old one, ending its allocations, until
// fib can change them in place (A58).
//
// fib's server runs its core under one mutex in the OnData of the datagram
// that asks, on a fib worker, and from there binds relayed addresses and
// opens peers with calls that wait for fib's binding lock and event loops:
// a breach of fit's rule 1 until fib's A56, bounded by max_allocations.
package turn

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lesismal/fib"
	"github.com/lesismal/fib/webrtc/stun"
	fibturn "github.com/lesismal/fib/webrtc/turn"
	"github.com/lesismal/fib/webrtc/turn/turnserver"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/observe"
)

// ID is the role's, and its member's path in the config.
const ID = "webrtc.turn"

// Software is sent as SOFTWARE in the server's responses.
const Software = "fit"

func init() {
	app.Register(app.Info{
		ID: ID,
		Member: func(c *config.Config) any {
			if c.WebRTC == nil {
				return nil
			}
			return c.WebRTC.TURN
		},
		New: func(h *app.Host) (app.Role, error) { return New(h), nil },
	})
}

// Server is the role: a TURN server that moves from config to config.
type Server struct {
	engine *fib.Engine
	log    *logs.Logger

	// rules is what the server reads on every request, swapped whole.
	rules   atomic.Pointer[rules]
	limiter limiter

	// mu guards cur and stopped, and orders Apply and Stop.
	mu      sync.Mutex
	cur     *running
	stopped bool

	toPeer, fromPeer atomic.Uint64 // bytes relayed
	dropped          atomic.Uint64 // datagrams over user_bandwidth
	peersRefused     atomic.Uint64 // permissions refused by the peer rules
	restarts         atomic.Uint64
}

// running is a fib TURN server serving one member's static settings.
type running struct {
	key     string
	cfg     *config.TURNServer
	srv     *turnserver.Server
	binding int
	addrs   []net.Addr
}

// rules is what the server reads on every request.
type rules struct {
	realm   string
	keys    map[string][]byte // a user's long-term key, by name
	secret  fibturn.AuthFunc  // nil without a secret
	allowed []netip.Prefix
	denied  []netip.Prefix
	rate    int64
}

// New returns the role, which its first Apply starts.
func New(h *app.Host) *Server {
	s := &Server{engine: h.Engine, log: h.Log}
	s.limiter.init()
	return s
}

// Addrs returns the addresses the server listens on, or nil while it does
// not run.
func (s *Server) Addrs() []net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == nil {
		return nil
	}
	return slices.Clone(s.cur.addrs)
}

// Allocations is the number of allocations the server holds.
func (s *Server) Allocations() int {
	s.mu.Lock()
	cur := s.cur
	s.mu.Unlock()
	if cur == nil {
		return 0
	}
	return cur.srv.Allocations()
}

// Apply moves the server to member, a *config.TURNServer.
func (s *Server) Apply(member any) error {
	cfg, ok := member.(*config.TURNServer)
	if !ok || cfg == nil {
		return errors.New("webrtc.turn: no member")
	}
	r, err := newRules(cfg)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return app.ErrStopped
	}
	key := staticKey(cfg)
	if s.cur != nil && s.cur.key == key {
		s.rules.Store(r)
		s.cur.cfg = cfg
		return nil
	}
	old := s.cur
	if err := s.checkFree(cfg, old); err != nil {
		return err
	}
	prev := s.rules.Load()
	s.rules.Store(r)
	if old != nil && slices.ContainsFunc(cfg.Listen, old.holds) {
		// The new server needs the old one's addresses: stop it first,
		// and start it again if the new one cannot start.
		s.stopRunning(old)
		s.cur = nil
		next, err := s.start(cfg, key)
		if err != nil {
			s.rules.Store(prev)
			if again, err2 := s.start(old.cfg, old.key); err2 == nil {
				s.cur = again
			} else {
				s.log.Errorf("the server as it was could not start again either: %v", err2)
			}
			return err
		}
		s.cur = next
	} else {
		next, err := s.start(cfg, key)
		if err != nil {
			if prev != nil {
				s.rules.Store(prev)
			}
			return err
		}
		s.cur = next
		if old != nil {
			s.stopRunning(old)
		}
	}
	if old != nil {
		s.restarts.Add(1)
		s.log.Warnf("listens on %s, relaying from %s; the server started again for a change of its addresses, realm, lifetimes or quotas, ending its allocations", addrList(s.cur.addrs), relayFrom(cfg))
	} else {
		s.log.Infof("listens on %s, relaying from %s", addrList(s.cur.addrs), relayFrom(cfg))
	}
	return nil
}

// Stop ends the server: its allocations are freed and its bindings removed.
func (s *Server) Stop(context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	s.stopped = true
	if s.cur != nil {
		s.stopRunning(s.cur)
		s.cur = nil
	}
}

func (s *Server) start(cfg *config.TURNServer, key string) (*running, error) {
	relayIP, _ := netip.ParseAddr(cfg.RelayIP)
	tc := turnserver.Config{
		ServerConfig: fibturn.ServerConfig{
			Realm:                 cfg.Realm,
			Auth:                  s.auth,
			Software:              Software,
			DefaultLifetime:       cfg.DefaultLifetime.Std(),
			MaxLifetime:           cfg.MaxLifetime.Std(),
			NonceLifetime:         cfg.NonceLifetime.Std(),
			MaxAllocations:        cfg.MaxAllocs(),
			MaxAllocationsPerUser: cfg.MaxAllocsPerUser(),
			OnRelay:               s.onRelay,
			PeerAllowed:           s.peerAllowed,
		},
		RelayIP: relayIP.Unmap(),
		PortMin: uint16(cfg.PortMin),
		PortMax: uint16(cfg.PortMax),
	}
	if cfg.NonceSecret != "" {
		tc.NonceSecret = []byte(cfg.NonceSecret)
	}
	if cfg.PublicIP != "" {
		pub, _ := netip.ParseAddr(cfg.PublicIP)
		tc.PublicIP = pub.Unmap()
	}
	srv, err := turnserver.New(tc)
	if err != nil {
		return nil, err
	}
	i, err := s.engine.AddBinding(fib.Binding{Network: "udp", Addrs: slices.Clone(cfg.Listen), Handler: srv})
	if err != nil {
		srv.Close()
		return nil, fmt.Errorf("listen on %s: %w", strings.Join(cfg.Listen, ", "), err)
	}
	addrs, err := s.engine.BindingAddrs(i)
	if err != nil {
		_ = s.engine.RemoveBinding(i)
		srv.Close()
		return nil, err
	}
	srv.Start(s.engine, i)
	return &running{key: key, cfg: cfg, srv: srv, binding: i, addrs: addrs}, nil
}

// stopRunning closes r's server, freeing its allocations and their
// bindings, and removes its listening binding.
func (s *Server) stopRunning(r *running) {
	r.srv.Close()
	if err := s.engine.RemoveBinding(r.binding); err != nil && !errors.Is(err, net.ErrClosed) {
		s.log.Warnf("removing the listening binding: %v", err)
	}
}

// checkFree fails when another socket holds one of cfg's addresses that the
// running server old does not: fib binds UDP with SO_REUSEPORT, and the
// kernel would let it share another process's port, splitting its clients
// between two servers. A plain bind, which a reuseport socket refuses,
// tells. Port 0 picks a free port.
func (s *Server) checkFree(cfg *config.TURNServer, old *running) error {
	for _, a := range cfg.Listen {
		if old != nil && old.holds(a) {
			continue
		}
		if _, port, err := net.SplitHostPort(a); err == nil && port == "0" {
			continue
		}
		pc, err := net.ListenPacket("udp", a)
		if err != nil {
			return fmt.Errorf("listen on %s: %w", a, err)
		}
		_ = pc.Close()
	}
	return nil
}

// holds reports whether r listens on address, as its member writes it or
// as it is bound.
func (r *running) holds(address string) bool {
	if slices.Contains(r.cfg.Listen, address) {
		return true
	}
	for _, a := range r.addrs {
		if a.String() == address {
			return true
		}
	}
	return false
}

// staticKey identifies what fib's server is made with: two members with
// the same key are served by the same server, their other settings swapped
// in place.
func staticKey(c *config.TURNServer) string {
	return fmt.Sprintf("%q %q %q %q %d-%d %d %d %v %v %v %q", c.Listen, c.Realm, c.RelayIP, c.PublicIP,
		c.PortMin, c.PortMax, c.MaxAllocs(), c.MaxAllocsPerUser(), c.DefaultLifetime, c.MaxLifetime, c.NonceLifetime, c.NonceSecret)
}

func newRules(c *config.TURNServer) (*rules, error) {
	r := &rules{realm: c.Realm, keys: make(map[string][]byte, len(c.Users)), rate: c.Bandwidth()}
	for _, u := range c.Users {
		if u.Password != "" {
			r.keys[u.Name] = stun.LongTermKey(u.Name, c.Realm, u.Password)
			continue
		}
		k := u.KeyBytes()
		if k == nil {
			return nil, fmt.Errorf("user %q: the key is not 32 hex digits", u.Name)
		}
		r.keys[u.Name] = k
	}
	if c.Secret != "" {
		r.secret = fibturn.EphemeralAuth([]byte(c.Secret), nil)
	}
	for _, p := range c.AllowedPeers {
		pp, ok := config.ParsePeerPrefix(p)
		if !ok {
			return nil, fmt.Errorf("allowed_peers: %q is not an address or a prefix", p)
		}
		r.allowed = append(r.allowed, pp)
	}
	for _, p := range c.DeniedPeers {
		pp, ok := config.ParsePeerPrefix(p)
		if !ok {
			return nil, fmt.Errorf("denied_peers: %q is not an address or a prefix", p)
		}
		r.denied = append(r.denied, pp)
	}
	return r, nil
}

// auth is the server's AuthFunc: a user of the config's, then the secret's
// time-limited credentials.
func (s *Server) auth(username, realm string) ([]byte, bool) {
	r := s.rules.Load()
	if r == nil || realm != r.realm {
		return nil, false
	}
	if k, ok := r.keys[username]; ok {
		return k, true
	}
	if r.secret != nil {
		return r.secret(username, realm)
	}
	return nil, false
}

// peerAllowed is the server's PeerAllowed: allowed_peers first, then
// denied_peers and fib's default denial of internal addresses.
func (s *Server) peerAllowed(ip netip.Addr) bool {
	ip = ip.Unmap()
	if r := s.rules.Load(); r != nil {
		for _, p := range r.allowed {
			if p.Contains(ip) {
				return true
			}
		}
		for _, p := range r.denied {
			if p.Contains(ip) {
				s.peersRefused.Add(1)
				return false
			}
		}
	}
	if fibturn.DeniedByDefault(ip) {
		s.peersRefused.Add(1)
		return false
	}
	return true
}

// onRelay counts each datagram relayed and drops those past user's
// bandwidth.
func (s *Server) onRelay(user string, toPeer bool, n int) bool {
	rate := int64(config.DefaultTURNUserBandwidth)
	if r := s.rules.Load(); r != nil {
		rate = r.rate
	}
	if !s.limiter.allow(user, toPeer, n, rate, time.Now()) {
		s.dropped.Add(1)
		return false
	}
	if toPeer {
		s.toPeer.Add(uint64(n))
	} else {
		s.fromPeer.Add(uint64(n))
	}
	return true
}

// WriteMetrics writes the server's metrics.
func (s *Server) WriteMetrics(w observe.MetricWriter) {
	w.Family("fit_turn_allocations", "gauge", "Allocations the TURN server holds, expired ones its next sweep frees included.")
	w.Sample("fit_turn_allocations", float64(s.Allocations()))
	w.Family("fit_turn_relayed_bytes_total", "counter", "Bytes the TURN server relayed, by direction: to peers, from clients, or from peers, to clients.")
	w.Sample("fit_turn_relayed_bytes_total", float64(s.toPeer.Load()), "direction", "to_peer")
	w.Sample("fit_turn_relayed_bytes_total", float64(s.fromPeer.Load()), "direction", "from_peer")
	w.Family("fit_turn_dropped_total", "counter", "Datagrams the TURN server dropped for a username past its user_bandwidth.")
	w.Sample("fit_turn_dropped_total", float64(s.dropped.Load()), "reason", "bandwidth")
	w.Family("fit_turn_peers_refused_total", "counter", "Peer addresses the TURN server refused a permission or channel for, by denied_peers or the default denial of internal addresses.")
	w.Sample("fit_turn_peers_refused_total", float64(s.peersRefused.Load()))
	w.Family("fit_turn_restarts_total", "counter", "Times a config's change of the TURN server's addresses, realm, lifetimes or quotas started it again, ending its allocations.")
	w.Sample("fit_turn_restarts_total", float64(s.restarts.Load()))
}

func addrList(addrs []net.Addr) string {
	s := make([]string, len(addrs))
	for i, a := range addrs {
		s[i] = a.String()
	}
	return strings.Join(s, ", ")
}

func relayFrom(c *config.TURNServer) string {
	s := c.RelayIP
	if c.PublicIP != "" {
		s += " (" + c.PublicIP + " to clients)"
	}
	if c.PortMax != 0 {
		s += fmt.Sprintf(" ports %d-%d", c.PortMin, c.PortMax)
	}
	return s
}

var (
	_ app.Role    = (*Server)(nil)
	_ app.Metered = (*Server)(nil)
)
