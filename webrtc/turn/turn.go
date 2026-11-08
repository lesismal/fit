// Package turn is WebRTC's TURN server role, webrtc.turn in fit's config:
// fib's TURN server (webrtc/turn/turnserver) on the listeners' engine, with
// its users, secret, relay addresses, quotas and peer rules from the config.
// See docs/decisions/029-webrtc.md.
//
// The server listens on one UDP binding of the engine, all of the member's
// UDP addresses, and on a TCP binding and a TLS one for listen_tcp and
// listen_tls; each allocation's relayed socket is a binding of its own,
// which fib's server opens on goroutines of its own, so that no fib worker
// waits (fib's A56). What a reload may change in place is changed in place:
// the users and the secret (the core's SetAuth), the lifetimes and quotas
// (SetLimits), the peer rules and the bandwidth (read through an atomic
// pointer on every request) and the TLS certificates (read on every
// handshake). A change of what fib's server is made with, its addresses,
// realm, relay addresses, nonce secret, Multipath TCP or stream bounds,
// starts a new server: beside the old one, which drains for the listeners'
// drain timeout while its allocations run out, or, when the two share an
// address, after the old one has closed.
//
// The member's access decides which client addresses the server serves,
// asked by fib as it accepts a TCP or TLS connection or meets a new UDP
// client (fib.Binding.Filter), before a Connection is made: the policy, the
// member's list and then the global one, is set again by every Apply,
// whether or not the server starts again, and a server draining goes by it
// too. The clients already served are not asked again; peers, which the
// relayed sockets reach, are the peer rules'.
package turn

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lesismal/fib"
	fibtls "github.com/lesismal/fib/tls"
	"github.com/lesismal/fib/webrtc/stun"
	fibturn "github.com/lesismal/fib/webrtc/turn"
	"github.com/lesismal/fib/webrtc/turn/turnserver"

	"github.com/lesismal/fit/access"
	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/bindcheck"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/observe"
	"github.com/lesismal/fit/tlsmgr"
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
		// The access's list files, read when the role applies; the
		// certificates are watched by runtime.Compile.
		CertFiles: func(c *config.Config) []string { return c.WebRTC.TURN.Access.Files() },
	})
}

// Server is the role: a TURN server that moves from config to config.
type Server struct {
	engine *fib.Engine
	host   *app.Host
	log    *logs.Logger

	// rules is what the server reads on every request, swapped whole;
	// certs the certificates of listen_tls, read on every handshake.
	rules   atomic.Pointer[rules]
	certs   atomic.Pointer[tlsmgr.Static]
	limiter limiter
	// access is the policy the listening bindings' Filter asks, every
	// server's, set by every Apply.
	access access.Gate

	// mu guards cur, draining and stopped, and orders Apply and Stop.
	mu       sync.Mutex
	cur      *running
	draining map[*running]*drain
	stopped  bool

	// live is every server running, cur and those draining, for the peer
	// check and the scrapes, which do not wait for an Apply. liveMu orders
	// its changes, and guards retired: what the servers no longer running
	// counted, which their counters carry on from.
	liveMu  sync.Mutex
	live    atomic.Pointer[[]*running]
	retired struct{ unrelayed, unsent uint64 }

	toPeer, fromPeer atomic.Uint64 // bytes relayed
	dropped          atomic.Uint64 // datagrams over user_bandwidth
	restarts         atomic.Uint64
	ev               eventMeters

	// drainTimeout, when set, stands in for the host's, for tests.
	drainTimeout time.Duration
}

// running is a fib TURN server serving one member's static settings.
type running struct {
	key  string
	cfg  *config.TURNServer
	auth fibturn.AuthFunc
	srv  *turnserver.Server
	core *fibturn.Server
	// relayIP and publicIP are the server's own addresses, which its peer
	// check allows on its relayed ports alone.
	relayIP, publicIP netip.Addr
	bindings          []int
	udp, tcp, tls     []net.Addr
}

// drain is a server being shut down beside its successor.
type drain struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// rules is what the server reads on every request.
type rules struct {
	allowed []config.PeerRule
	denied  []config.PeerRule
	rate    int64
}

// New returns the role, which its first Apply starts.
func New(h *app.Host) *Server {
	s := &Server{engine: h.Engine, host: h, log: h.Log, draining: map[*running]*drain{}}
	s.limiter.init()
	s.live.Store(&[]*running{})
	return s
}

// Addrs returns the UDP addresses the server listens on, or nil while it
// does not run.
func (s *Server) Addrs() []net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == nil {
		return nil
	}
	return slices.Clone(s.cur.udp)
}

// StreamAddrs returns the TCP and the TLS addresses the server listens on.
func (s *Server) StreamAddrs() (tcp, tls []net.Addr) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == nil {
		return nil, nil
	}
	return slices.Clone(s.cur.tcp), slices.Clone(s.cur.tls)
}

// Allocations is the number of allocations the current server holds, not
// counting those of servers draining.
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
	auth, err := newAuth(cfg)
	if err != nil {
		return err
	}
	policy, err := s.host.Access(cfg.Access)
	if err != nil {
		return fmt.Errorf("access: %w", err)
	}
	var certs *tlsmgr.Static
	if cfg.TLS != nil {
		if certs, err = tlsmgr.LoadStatic(cfg.TLS.Config()); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return app.ErrStopped
	}
	key := staticKey(cfg)
	if s.cur != nil && s.cur.key == key {
		s.access.Set(policy)
		s.rules.Store(r)
		s.certs.Store(certs)
		s.cur.core.SetLimits(limits(cfg))
		s.cur.core.SetAuth(auth)
		s.cur.cfg, s.cur.auth = cfg, auth
		return nil
	}
	// A server draining on an address the new one needs ends now.
	for d, dr := range s.draining {
		select {
		case <-dr.done:
			delete(s.draining, d)
			continue
		default:
		}
		if d.sharesAddr(cfg) {
			dr.cancel()
			<-dr.done
			delete(s.draining, d)
		}
	}
	old := s.cur
	if err := checkFree(cfg, old); err != nil {
		return err
	}
	prevRules, prevCerts, prevAccess := s.rules.Load(), s.certs.Load(), s.access.Policy()
	s.rules.Store(r)
	s.certs.Store(certs)
	s.access.Set(policy)
	restore := func() {
		if prevRules != nil {
			s.rules.Store(prevRules)
			s.certs.Store(prevCerts)
			s.access.Set(prevAccess)
		}
	}
	if old != nil && old.sharesAddr(cfg) {
		// The new server needs the old one's addresses: close it first,
		// and start it again if the new one cannot start.
		s.closeRunning(old)
		s.cur = nil
		next, err := s.start(cfg, key, auth)
		if err != nil {
			restore()
			if again, err2 := s.start(old.cfg, old.key, old.auth); err2 == nil {
				s.cur = again
			} else {
				s.log.Errorf("the server as it was could not start again either: %v", err2)
			}
			return err
		}
		s.cur = next
		s.restarts.Add(1)
		s.log.Warnf("listens on %s, relaying from %s; the server started again on the same addresses for a change of its realm, relay addresses, nonce secret, Multipath TCP or stream bounds, ending its allocations", s.cur.describe(), relayFrom(cfg))
		return nil
	}
	next, err := s.start(cfg, key, auth)
	if err != nil {
		restore()
		return err
	}
	s.cur = next
	if old == nil {
		s.log.Infof("listens on %s, relaying from %s", s.cur.describe(), relayFrom(cfg))
		return nil
	}
	s.restarts.Add(1)
	drainFor := s.drainTimeout
	if drainFor <= 0 {
		drainFor = s.host.DrainTimeout()
	}
	s.log.Infof("listens on %s, relaying from %s; the server it replaced drains its %d allocations for up to %s", s.cur.describe(), relayFrom(cfg), old.srv.Allocations(), drainFor)
	s.drainRunning(old, drainFor)
	return nil
}

// Stop ends the server and those draining: their allocations are freed and
// their bindings removed.
func (s *Server) Stop(ctx context.Context) {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	if s.cur != nil {
		s.closeRunning(s.cur)
		s.cur = nil
	}
	drains := make([]*drain, 0, len(s.draining))
	for d, dr := range s.draining {
		dr.cancel()
		drains = append(drains, dr)
		delete(s.draining, d)
	}
	s.mu.Unlock()
	for _, dr := range drains {
		select {
		case <-dr.done:
		case <-ctx.Done():
			return
		}
	}
}

func (s *Server) start(cfg *config.TURNServer, key string, auth fibturn.AuthFunc) (*running, error) {
	relayIP, _ := netip.ParseAddr(cfg.RelayIP)
	tc := turnserver.Config{
		ServerConfig: fibturn.ServerConfig{
			Realm:       cfg.Realm,
			Auth:        auth,
			Software:    Software,
			Limits:      limits(cfg),
			OnRelay:     s.onRelay,
			OnEvent:     s.onEvent,
			PeerAllowed: s.peerAllowed,
		},
		RelayIP:           relayIP.Unmap(),
		PortMin:           uint16(cfg.PortMin),
		PortMax:           uint16(cfg.PortMax),
		MaxStreamMessage:  cfg.StreamMessage(),
		StreamIdleTimeout: cfg.StreamIdle(),
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
	r := &running{key: key, cfg: cfg, auth: auth, srv: srv, core: srv.Core(), relayIP: tc.RelayIP, publicIP: tc.PublicIP}
	// Live before it serves, so that the peer checks of every server see
	// its relayed addresses.
	s.addLive(r)
	fail := func(err error) (*running, error) {
		s.closeRunning(r)
		return nil, err
	}
	i, err := s.engine.AddBinding(fib.Binding{Network: "udp", Addrs: slices.Clone(cfg.Listen), Handler: srv, Filter: s.access.Allows})
	if err != nil {
		return fail(fmt.Errorf("listen on %s: %w", strings.Join(cfg.Listen, ", "), err))
	}
	r.bindings = append(r.bindings, i)
	if r.udp, err = s.engine.BindingAddrs(i); err != nil {
		return fail(err)
	}
	srv.Start(s.engine, i)
	if len(cfg.ListenTCP) > 0 {
		i, err := s.engine.AddBinding(fib.Binding{Network: "tcp", Addrs: slices.Clone(cfg.ListenTCP), Handler: srv.StreamHandler(), MultipathTCP: cfg.MPTCP, Filter: s.access.Allows})
		if err != nil {
			return fail(fmt.Errorf("listen_tcp on %s: %w", strings.Join(cfg.ListenTCP, ", "), err))
		}
		r.bindings = append(r.bindings, i)
		if r.tcp, err = s.engine.BindingAddrs(i); err != nil {
			return fail(err)
		}
	}
	if len(cfg.ListenTLS) > 0 {
		h := fibtls.NewServer(tlsmgr.StreamConfig(s.getCertificate), srv.StreamHandler())
		i, err := s.engine.AddBinding(fib.Binding{Network: "tcp", Addrs: slices.Clone(cfg.ListenTLS), Handler: h, MultipathTCP: cfg.MPTCP, Filter: s.access.Allows})
		if err != nil {
			return fail(fmt.Errorf("listen_tls on %s: %w", strings.Join(cfg.ListenTLS, ", "), err))
		}
		r.bindings = append(r.bindings, i)
		if r.tls, err = s.engine.BindingAddrs(i); err != nil {
			return fail(err)
		}
	}
	return r, nil
}

// closeRunning closes r's server at once, freeing its allocations and their
// bindings, and removes its listening bindings.
func (s *Server) closeRunning(r *running) {
	r.srv.Close()
	s.finish(r)
}

// drainRunning shuts r down beside its successor, on a goroutine of its
// own: r takes no new allocation while those it holds run out, for drainFor
// at most, and then closes. s.mu is held.
func (s *Server) drainRunning(r *running, drainFor time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), drainFor)
	d := &drain{cancel: cancel, done: make(chan struct{})}
	s.draining[r] = d
	go func() {
		defer close(d.done)
		defer cancel()
		if err := r.srv.Shutdown(ctx); errors.Is(err, context.DeadlineExceeded) {
			s.log.Infof("the server on %s drained for %s; its allocations left ended", r.describe(), drainFor)
		}
		s.finish(r)
	}()
}

// finish removes r's listening bindings and its place among the live
// servers, keeping what it counted.
func (s *Server) finish(r *running) {
	for _, i := range r.bindings {
		if err := s.engine.RemoveBinding(i); err != nil && !errors.Is(err, net.ErrClosed) {
			s.log.Warnf("removing a listening binding: %v", err)
		}
	}
	st := r.core.Stats()
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	s.retired.unrelayed += st.Dropped
	s.retired.unsent += r.srv.Unsent()
	live := slices.DeleteFunc(slices.Clone(*s.live.Load()), func(o *running) bool { return o == r })
	s.live.Store(&live)
}

func (s *Server) addLive(r *running) {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	live := append(slices.Clone(*s.live.Load()), r)
	s.live.Store(&live)
}

// checkFree fails when another socket holds one of cfg's addresses that the
// running server old does not (see package bindcheck): UDP for listen, TCP
// for listen_tcp and listen_tls.
func checkFree(cfg *config.TURNServer, old *running) error {
	try := func(network, a string, held bool) error {
		if held {
			return nil
		}
		return bindcheck.Free(network, a)
	}
	for _, a := range cfg.Listen {
		if err := try("udp", a, old != nil && old.holds(a, false)); err != nil {
			return err
		}
	}
	for _, a := range slices.Concat(cfg.ListenTCP, cfg.ListenTLS) {
		if err := try("tcp", a, old != nil && old.holds(a, true)); err != nil {
			return err
		}
	}
	return nil
}

// holds reports whether r listens on address, as its member writes it or
// as it is bound, over TCP if stream and UDP otherwise.
func (r *running) holds(address string, stream bool) bool {
	if _, port, err := net.SplitHostPort(address); err == nil && port == "0" {
		return false // a port of the kernel's, never r's
	}
	listen, bound := r.cfg.Listen, r.udp
	if stream {
		listen, bound = slices.Concat(r.cfg.ListenTCP, r.cfg.ListenTLS), slices.Concat(r.tcp, r.tls)
	}
	if slices.Contains(listen, address) {
		return true
	}
	for _, a := range bound {
		if a.String() == address {
			return true
		}
	}
	return false
}

// sharesAddr reports whether r listens on one of cfg's addresses.
func (r *running) sharesAddr(cfg *config.TURNServer) bool {
	return slices.ContainsFunc(cfg.Listen, func(a string) bool { return r.holds(a, false) }) ||
		slices.ContainsFunc(slices.Concat(cfg.ListenTCP, cfg.ListenTLS), func(a string) bool { return r.holds(a, true) })
}

// describe lists r's addresses for a log line.
func (r *running) describe() string {
	s := addrList(r.udp)
	if len(r.tcp) > 0 {
		s += ", tcp " + addrList(r.tcp)
	}
	if len(r.tls) > 0 {
		s += ", tls " + addrList(r.tls)
	}
	return s
}

// staticKey identifies what fib's server is made with: two members with
// the same key are served by the same server, their other settings changed
// in place. mptcp is in it because it is fixed when a binding is made, as
// the addresses, realm, relay addresses, nonce secret and stream bounds
// are: changing it needs a new server, never a setting on the old one.
func staticKey(c *config.TURNServer) string {
	return fmt.Sprintf("%q %q %q %q %q %q %d-%d %q %d %v %v", c.Listen, c.ListenTCP, c.ListenTLS, c.Realm, c.RelayIP, c.PublicIP,
		c.PortMin, c.PortMax, c.NonceSecret, c.StreamMessage(), c.StreamIdle(), c.MPTCP)
}

// limits are the lifetimes and quotas of c, which a reload changes in
// place; zero lifetimes are fib's defaults, which are fit's.
func limits(c *config.TURNServer) fibturn.Limits {
	return fibturn.Limits{
		DefaultLifetime:       c.DefaultLifetime.Std(),
		MaxLifetime:           c.MaxLifetime.Std(),
		NonceLifetime:         c.NonceLifetime.Std(),
		MaxAllocations:        c.MaxAllocs(),
		MaxAllocationsPerUser: c.MaxAllocsPerUser(),
		MaxPermissions:        c.MaxPerms(),
		MaxChannels:           c.MaxChans(),
	}
}

func newRules(c *config.TURNServer) (*rules, error) {
	r := &rules{rate: c.Bandwidth()}
	for _, f := range []struct {
		name string
		in   []string
		out  *[]config.PeerRule
	}{{"allowed_peers", c.AllowedPeers, &r.allowed}, {"denied_peers", c.DeniedPeers, &r.denied}} {
		for _, p := range f.in {
			pr, ok := config.ParsePeerRule(p)
			if !ok {
				return nil, fmt.Errorf("%s: %q is not an address, an address and port, or a prefix", f.name, p)
			}
			*f.out = append(*f.out, pr)
		}
	}
	return r, nil
}

// newAuth returns the AuthFunc of c's credentials: a user of the config's,
// then the secret's time-limited ones.
func newAuth(c *config.TURNServer) (fibturn.AuthFunc, error) {
	keys := make(map[string][]byte, len(c.Users))
	for _, u := range c.Users {
		if u.Password != "" {
			keys[u.Name] = stun.LongTermKey(u.Name, c.Realm, u.Password)
			continue
		}
		k := u.KeyBytes()
		if k == nil {
			return nil, fmt.Errorf("user %q: the key is not 32 hex digits", u.Name)
		}
		keys[u.Name] = k
	}
	var secret fibturn.AuthFunc
	if c.Secret != "" {
		secret = fibturn.EphemeralAuth([]byte(c.Secret), nil)
	}
	realm := c.Realm
	return func(username, r string) ([]byte, bool) {
		if r != realm {
			return nil, false
		}
		if k, ok := keys[username]; ok {
			return k, true
		}
		if secret != nil {
			return secret(username, r)
		}
		return nil, false
	}, nil
}

// getCertificate gives a TLS client's handshake listen_tls's certificate.
func (s *Server) getCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	certs := s.certs.Load()
	if certs == nil {
		return nil, errors.New("webrtc.turn: no certificate")
	}
	return certs.GetCertificate(hello)
}

// peerAllowed is the server's PeerAllowed, asked with port 0 for a
// permission and with the peer's port for a channel and every datagram.
// The servers' own addresses, relay_ip and public_ip, are allowed on the
// ports of their relayed addresses, so that two clients relay to each
// other, and on ports allowed_peers names with them: never on the others,
// fit's own listeners among them, whatever the prefixes say. Other peers
// go by allowed_peers first, then denied_peers and fib's default denial of
// internal addresses.
func (s *Server) peerAllowed(peer netip.AddrPort) bool {
	ip, port := peer.Addr().Unmap(), peer.Port()
	r := s.rules.Load()
	live := *s.live.Load()
	if slices.ContainsFunc(live, func(l *running) bool { return ip == l.relayIP || ip == l.publicIP }) {
		if port == 0 {
			return true
		}
		for _, l := range live {
			if l.core.HoldsRelay(netip.AddrPortFrom(ip, port)) ||
				ip == l.relayIP && l.publicIP.IsValid() && l.core.HoldsRelay(netip.AddrPortFrom(l.publicIP, port)) {
				return true
			}
		}
		return r != nil && slices.ContainsFunc(r.allowed, func(a config.PeerRule) bool {
			return a.Port == port && a.Prefix.Contains(ip)
		})
	}
	if r != nil {
		for _, a := range r.allowed {
			if a.Prefix.Contains(ip) && (a.Port == 0 || port == 0 || a.Port == port) {
				return true
			}
		}
		for _, d := range r.denied {
			if d.Prefix.Contains(ip) && (d.Port == 0 || d.Port == port) {
				return false
			}
		}
	}
	return !fibturn.DeniedByDefault(ip)
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

// refusedCodes are the error codes fit_turn_refused_total names; any other
// is counted as other.
var refusedCodes = [...]int{400, 403, 420, 437, 441, 442, 443, 486, 508}

// eventMeters count fib's events.
type eventMeters struct {
	allocated, refreshed, authFailed atomic.Uint64
	freed                            [4]atomic.Uint64 // by fibturn.FreeReason
	refused                          [len(refusedCodes) + 1]atomic.Uint64
}

// onEvent is the servers' OnEvent: it counts each event, and logs it at
// debug level. It runs on the goroutine that saw the event, a fib worker
// among them, and does not wait: a line is written only when the webrtc
// logger is at debug.
func (s *Server) onEvent(e fibturn.Event) {
	m := &s.ev
	switch e.Kind {
	case fibturn.EventAllocated:
		m.allocated.Add(1)
		s.log.Debugf("allocated %s for %q, from %s over %s, for %s", e.Relay, e.User, e.Tuple.Client, e.Tuple.Transport, e.Lifetime)
	case fibturn.EventRefreshed:
		m.refreshed.Add(1)
		s.log.Debugf("refreshed %s for %q, for %s", e.Relay, e.User, e.Lifetime)
	case fibturn.EventFreed:
		if int(e.Reason) < len(m.freed) {
			m.freed[e.Reason].Add(1)
		}
		s.log.Debugf("freed %s of %q, %s, having relayed %d bytes to peers and %d to the client", e.Relay, e.User, e.Reason, e.ToPeer.Bytes, e.ToClient.Bytes)
	case fibturn.EventRefused:
		i := slices.Index(refusedCodes[:], e.Code)
		if i < 0 {
			i = len(refusedCodes)
		}
		m.refused[i].Add(1)
		s.log.Debugf("refused a %s of %q from %s with %d", e.Method, e.User, e.Tuple.Client, e.Code)
	case fibturn.EventAuthFailed:
		m.authFailed.Add(1)
		s.log.Debugf("credentials of %q from %s failed with %d", e.User, e.Tuple.Client, e.Code)
	}
}

// stats sums what the live servers hold, and what every server counted.
func (s *Server) stats() (st fibturn.Stats, unsent uint64, servers int) {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	live := *s.live.Load()
	st.Dropped, unsent = s.retired.unrelayed, s.retired.unsent
	for _, l := range live {
		ls := l.core.Stats()
		st.Allocations += ls.Allocations
		st.Pending += ls.Pending
		st.Permissions += ls.Permissions
		st.Channels += ls.Channels
		st.Dropped += ls.Dropped
		unsent += l.srv.Unsent()
	}
	return st, unsent, len(live)
}

// WriteMetrics writes the server's metrics.
func (s *Server) WriteMetrics(w observe.MetricWriter) {
	st, unsent, servers := s.stats()
	gauge := func(name, help string, v int) {
		w.Family(name, "gauge", help)
		w.Sample(name, float64(v))
	}
	counter := func(name, help string, v uint64) {
		w.Family(name, "counter", help)
		w.Sample(name, float64(v))
	}
	gauge("fit_turn_allocations", "Allocations the TURN server holds, those of a server draining and expired ones the next sweep frees included.", st.Allocations)
	gauge("fit_turn_pending_allocations", "Allocations waiting for their relayed address to be bound.", st.Pending)
	gauge("fit_turn_permissions", "Permissions the allocations hold.", st.Permissions)
	gauge("fit_turn_channels", "Channels the allocations have bound.", st.Channels)
	gauge("fit_turn_draining_servers", "TURN servers a reload replaced that drain their allocations.", max(servers-1, 0))
	w.Family("fit_turn_relayed_bytes_total", "counter", "Bytes the TURN server relayed, by direction: to peers, from clients, or from peers, to clients.")
	w.Sample("fit_turn_relayed_bytes_total", float64(s.toPeer.Load()), "direction", "to_peer")
	w.Sample("fit_turn_relayed_bytes_total", float64(s.fromPeer.Load()), "direction", "from_peer")
	w.Family("fit_turn_dropped_total", "counter", "Datagrams the TURN server dropped, by reason: a username past its user_bandwidth, or no connection to send them on, one that could not be opened or a setup queue full.")
	w.Sample("fit_turn_dropped_total", float64(s.dropped.Load()), "reason", "bandwidth")
	w.Sample("fit_turn_dropped_total", float64(unsent), "reason", "unsent")
	counter("fit_turn_unrelayed_total", "Datagrams the TURN server did nothing with: malformed, without an allocation, a permission or a channel, refused by the peer rules, or past user_bandwidth.", st.Dropped)
	counter("fit_turn_allocated_total", "Allocations made.", s.ev.allocated.Load())
	counter("fit_turn_refreshed_total", "Allocations refreshed.", s.ev.refreshed.Load())
	w.Family("fit_turn_freed_total", "counter", "Allocations ended, by reason: a client's delete, its lifetime run out, or its server or TCP or TLS connection closed.")
	for _, why := range []fibturn.FreeReason{fibturn.FreedDeleted, fibturn.FreedExpired, fibturn.FreedClosed} {
		w.Sample("fit_turn_freed_total", float64(s.ev.freed[why].Load()), "reason", why.String())
	}
	w.Family("fit_turn_refused_total", "counter", "Requests refused after their credentials passed, by error code: 403 a peer the rules refuse, 486 max_allocations_per_user, 508 max_allocations, max_permissions, max_channels or a drain, and the rest.")
	for i := range s.ev.refused {
		code := "other"
		if i < len(refusedCodes) {
			code = strconv.Itoa(refusedCodes[i])
		}
		w.Sample("fit_turn_refused_total", float64(s.ev.refused[i].Load()), "code", code)
	}
	counter("fit_turn_auth_failures_total", "Requests whose credentials failed: a wrong user, password or realm, or incomplete ones.", s.ev.authFailed.Load())
	counter("fit_turn_peers_refused_total", "Permissions and channels refused for a peer the rules refuse: denied_peers, the default denial of internal addresses, or fit's own address on a port that is not a relayed one.", s.ev.refused[slices.Index(refusedCodes[:], 403)].Load())
	counter("fit_turn_restarts_total", "Times a config's change of the TURN server's addresses, realm, relay addresses, nonce secret, Multipath TCP or stream bounds started a new server.", s.restarts.Load())
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
