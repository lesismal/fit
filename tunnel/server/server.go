// Package server is the tunnel's server role, tunnel.server in fit's
// config. Nodes and clients link to its listeners, over
// TCP, WebSocket or QUIC, and register with its password, every link of
// each one's pool; the links of one type and name, from however many
// processes, are one peer, numbered as Unix file descriptors are. The
// server routes each tunnel a client opens to the node it names, and passes
// the tunnel's frames between the two, over one link of each throughout, so
// that they keep their order. Its console pages show what is registered,
// change its listeners and passwords, query and set nodes' services, kick
// peers and close tunnels; an encrypted WebSocket, console_socket, carries
// the pages to clients that cannot reach them.
//
// Everything runs on the listeners' fib engine. Link handlers that wait,
// for a node to answer, run on the links' bounded goroutines, never on a
// fib worker; what the pages change is written back into the config with
// app.Host.Edit, and applied from there.
package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/tunnel/internal/bodycrypt"
	"github.com/lesismal/fit/tunnel/internal/common"
	"github.com/lesismal/fit/tunnel/internal/latency"
	"github.com/lesismal/fit/tunnel/internal/link"
	"github.com/lesismal/fit/tunnel/internal/proto"
	"github.com/lesismal/fit/tunnel/internal/transport"
	"github.com/lesismal/fit/tunnel/internal/transport/quic"
	"github.com/lesismal/fit/tunnel/internal/wscrypt"

	// Nodes and clients may link over WebSocket.
	_ "github.com/lesismal/fit/tunnel/internal/transport/ws"
)

// ID is the role's, and its member's path in the config.
const ID = "tunnel.server"

func init() {
	app.Register(app.Info{
		ID: ID,
		Member: func(c *config.Config) any {
			if c.Tunnel == nil {
				return nil
			}
			return c.Tunnel.Server
		},
		New: func(h *app.Host) (app.Role, error) { return New(h), nil },
	})
}

// unregisteredFrame bounds the frames of a link that has not registered:
// a registration is a few hundred bytes.
const unregisteredFrame = 64 << 10

// settings are what the server runs with, swapped whole as configs apply.
type settings struct {
	cfg      *config.TunnelServer
	name     string
	verifier *proto.Verifier
	coder    *bodycrypt.Coder
	// secret is the console socket's, nil without one.
	secret wscrypt.Secret
}

// Conn is a registered link of a peer.
type Conn struct {
	// ID is the link's number on the server, which no other link has while
	// it lasts; the tunnels opened over it carry it.
	ID uint32
	// Index is the link's place among its peer's.
	Index int
	Peer  *Peer
}

// Peer is a registered node or client.
type Peer struct {
	ID uint32
	// Addr is where the link the peer first registered came from.
	Addr  string
	Since time.Time

	// links is the peer's links, by their places among them; it changes
	// under the server's lock.
	links map[int]*link.Link

	mu   sync.Mutex
	info proto.Info
	// rtt and serviceRTT are what the peer's last ping told.
	rtt        latency.Stats
	serviceRTT map[string]latency.Stats
}

// Info is what the peer told about itself.
func (p *Peer) Info() proto.Info {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.info
}

func (p *Peer) setInfo(info proto.Info) {
	p.mu.Lock()
	p.info = info
	p.mu.Unlock()
}

// Latency is the round trip between the server and the peer and, for a
// node, how long its connects to its services took, as the peer measured
// them.
func (p *Peer) Latency() proto.NodeLatency {
	p.mu.Lock()
	defer p.mu.Unlock()
	return proto.NodeLatency{RTT: p.rtt, ServiceRTT: p.serviceRTT}
}

func (p *Peer) setLatency(req *proto.PingReq) {
	p.mu.Lock()
	p.rtt, p.serviceRTT = req.RTT, req.ServiceRTT
	p.mu.Unlock()
}

func (p *Peer) key() string {
	info := p.Info()
	return info.Type + "/" + info.Name
}

// Route is a tunnel the server passes frames along.
type Route struct {
	ID     uint64
	Client *Peer
	Node   *Peer
	// ClientLink and NodeLink are the links of Client and Node that carry
	// the tunnel.
	ClientLink, NodeLink *link.Link
	Service              string
	// Addr is the service's address, as the node reports it once the
	// tunnel is open; it changes under the server's lock.
	Addr     string
	Created  time.Time
	up, down atomic.Int64
	open     atomic.Bool
}

// listener is a listener of the config as it runs: open, or failed to open
// with err.
type listener struct {
	cfg config.TunnelListener
	// fp is the certificate a quic listener presents, by fingerprint.
	fp  string
	l   transport.Listener
	err string
}

// Server is the running role.
type Server struct {
	h      *app.Host
	log    *logs.Logger
	router *link.Router

	set atomic.Pointer[settings]

	// amu is held for a whole Apply or Stop, and guards what they change.
	amu       sync.Mutex
	started   bool
	stopped   bool
	listeners []*listener
	cert      *tls.Certificate
	certPEM   string
	fp        string
	console   *common.Console
	socket    *socket
	// generated is a certificate made for quic listeners, until the config
	// has it.
	generated [2]string

	// lmu guards what Status reads of what amu guards.
	lmu        sync.Mutex
	socketAddr string

	mu       sync.Mutex
	links    map[*link.Link]struct{}
	nextID   uint32
	nextConn uint32
	peers    map[*link.Link]*Peer
	byName   map[string]*Peer
	byID     map[uint32]*Peer
	routes   map[uint64]*Route
	since    time.Time
	closing  bool

	// epoch and nodesVersion number the node lists the server sends: see
	// proto.NodeList.
	epoch        uint64
	nodesVersion atomic.Uint64
}

// New returns the role, which its first Apply starts.
func New(h *app.Host) *Server {
	s := &Server{h: h, log: h.Log, links: map[*link.Link]struct{}{}, peers: map[*link.Link]*Peer{}, byName: map[string]*Peer{},
		byID: map[uint32]*Peer{}, routes: map[uint64]*Route{}, since: time.Now(), epoch: rand.Uint64() | 1}
	s.router = s.newRouter()
	s.console = common.NewConsole(h.Engine, "fit_tunnel_server", pages, h.Log, s.api)
	return s
}

func (s *Server) settings() *settings { return s.set.Load() }

// Apply moves the server to member, a *config.TunnelServer. Listeners,
// the console and its socket are opened anew only where they changed; the
// links registered stay, and the passwords apply to the links that open
// from now on. At the first Apply, anything that does not open fails it;
// after, a listener that does not open is shown with its error, tried
// again with the next config, and its error returned beside the rest
// applied.
func (s *Server) Apply(member any) error {
	cfg, ok := member.(*config.TunnelServer)
	if !ok || cfg == nil {
		return errors.New("tunnel.server: no member")
	}
	s.amu.Lock()
	defer s.amu.Unlock()
	if s.stopped {
		return app.ErrStopped
	}
	first := !s.started
	next := &settings{cfg: cfg, name: cfg.ServerName()}
	if cfg.BodyPassword != "" {
		c, err := bodycrypt.New(cfg.BodyPassword)
		if err != nil {
			return err
		}
		next.coder = c
	}
	if cfg.Password != "" {
		if old := s.settings(); old != nil && old.verifier != nil && old.verifier.Password == cfg.Password {
			next.verifier = old.verifier
		} else {
			next.verifier = &proto.Verifier{Password: cfg.Password}
		}
	}
	if cfg.ConsoleSocket != nil {
		if old := s.settings(); old != nil && old.secret != nil && old.cfg.SocketPassword() == cfg.SocketPassword() {
			next.secret = old.secret
		} else {
			secret, err := wscrypt.NewSecret(cfg.SocketPassword())
			if err != nil {
				return err
			}
			next.secret = secret
		}
	}
	persist, err := s.applyCert(cfg)
	if err != nil {
		return err
	}
	s.set.Store(next)

	var errs []error
	if err := s.console.Apply(cfg.Console); err != nil {
		errs = append(errs, fmt.Errorf("console: %w", err))
	}
	if err := s.applySocket(cfg.ConsoleSocket); err != nil {
		errs = append(errs, fmt.Errorf("console_socket: %w", err))
	}
	errs = append(errs, s.applyListeners(cfg.Listeners)...)
	if first && len(errs) > 0 {
		s.stopAll()
		return errors.Join(errs...)
	}
	s.started = true
	if persist != nil {
		s.writeCert(persist[0], persist[1])
	}
	return errors.Join(errs...)
}

// applyCert sets the certificate of the quic listeners: the config's, or,
// when a quic listener needs one and the config has none, one made here,
// which it returns as PEM to be written into the config.
func (s *Server) applyCert(cfg *config.TunnelServer) (persist []string, err error) {
	certPEM, keyPEM := "", ""
	if cfg.QUIC != nil {
		certPEM, keyPEM = cfg.QUIC.Cert, cfg.QUIC.Key
	}
	if certPEM == "" && slices.ContainsFunc(cfg.Listeners, func(l config.TunnelListener) bool { return l.Protocol == config.TunnelQUIC }) {
		if s.generated[0] == "" {
			c, k, err := quic.NewSelfSigned("localhost", cfg.ServerName())
			if err != nil {
				return nil, err
			}
			s.generated = [2]string{string(c), string(k)}
		}
		certPEM, keyPEM = s.generated[0], s.generated[1]
		persist = s.generated[:]
	}
	if certPEM == s.certPEM {
		return persist, nil
	}
	if certPEM == "" {
		s.lmu.Lock()
		s.cert, s.certPEM, s.fp = nil, "", ""
		s.lmu.Unlock()
		return nil, nil
	}
	cert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		return nil, fmt.Errorf("quic: %w", err)
	}
	s.lmu.Lock()
	s.cert, s.certPEM, s.fp = &cert, certPEM, quic.Fingerprint(cert)
	s.lmu.Unlock()
	return persist, nil
}

// writeCert writes a certificate made for the quic listeners into the
// config, so that it outlives a restart and its fingerprint stays the one
// nodes and clients pinned.
func (s *Server) writeCert(certPEM, keyPEM string) {
	s.h.Edit(func(cur []byte) ([]byte, error) {
		return common.EditJSON(cur, func(m common.Member) error {
			q := common.Member{}
			if err := m.Get("quic", &q); err != nil {
				return err
			}
			var have string
			if err := q.Get("cert", &have); err != nil || have != "" {
				return err
			}
			if err := q.Set("cert", certPEM); err != nil {
				return err
			}
			if err := q.Set("key", keyPEM); err != nil {
				return err
			}
			return m.Set("quic", q)
		})
	}, func(_ []string, err error) {
		if err != nil {
			s.log.Warnf("keeping the quic certificate in the config: %v", err)
			return
		}
		s.log.Infof("made a quic certificate, kept in the config: fingerprint %s", s.Fingerprint())
	})
}

// applyListeners opens the listeners the config adds, and closes those it
// leaves out; the rest stay open. It returns why each that failed did.
func (s *Server) applyListeners(want []config.TunnelListener) []error {
	s.lmu.Lock()
	fp := s.fp
	cert := s.cert
	s.lmu.Unlock()
	key := func(l config.TunnelListener) string {
		if l.Protocol == config.TunnelQUIC {
			return l.Key() + " " + fp
		}
		return l.Key()
	}
	have := map[string]*listener{}
	for _, l := range s.listeners {
		if l.l != nil {
			k := l.cfg.Key()
			if l.cfg.Protocol == config.TunnelQUIC {
				k += " " + l.fp
			}
			have[k] = l
		}
	}
	keep := map[*listener]bool{}
	for _, lc := range want {
		if l := have[key(lc)]; l != nil {
			keep[l] = true
		}
	}
	// Those left out close first, as a listener that replaces one may
	// listen where it did.
	for _, l := range s.listeners {
		if l.l != nil && !keep[l] {
			_ = l.l.Close()
			s.log.Infof("closed listener %s", l.cfg.URL())
		}
	}
	var errs []error
	next := make([]*listener, 0, len(want))
	for _, lc := range want {
		if l := have[key(lc)]; l != nil {
			l.cfg = lc
			next = append(next, l)
			continue
		}
		l := &listener{cfg: lc, fp: fp}
		q := s.settings().cfg.QUIC
		o := transport.ListenOptions{Cert: cert, Log: s.log,
			QUIC: transport.QUIC{IdleTimeout: q.Idle(), KeepAlive: q.Ping(), HandshakeTimeout: q.Handshake(), MaxHandshakes: q.Handshakes(), MaxConns: s.settings().cfg.Links()}}
		tl, err := transport.Listen(s.h.Engine, lc.URL(), o, s.accept)
		if err != nil {
			l.err = err.Error()
			errs = append(errs, fmt.Errorf("listener %s: %w", lc.URL(), err))
			s.log.Warnf("listener %s: %v", lc.URL(), err)
		} else {
			l.l = tl
			s.log.Infof("nodes and clients link on %s", tl.URL())
		}
		next = append(next, l)
	}
	s.lmu.Lock()
	s.listeners = next
	s.lmu.Unlock()
	return errs
}

// Fingerprint is the SHA-256 of the certificate the quic listeners
// present, which nodes and clients pin, or "" without one.
func (s *Server) Fingerprint() string {
	s.lmu.Lock()
	defer s.lmu.Unlock()
	return s.fp
}

// Stop closes the listeners, the console and every link, with the tunnels
// they carry.
func (s *Server) Stop(ctx context.Context) {
	s.amu.Lock()
	defer s.amu.Unlock()
	if s.stopped {
		return
	}
	s.stopped = true
	s.stopAll()
	s.console.Stop(ctx)
}

// stopAll closes what Apply opened, and every link. amu is held.
func (s *Server) stopAll() {
	for _, l := range s.listeners {
		if l.l != nil {
			_ = l.l.Close()
		}
	}
	s.lmu.Lock()
	s.listeners = nil
	s.lmu.Unlock()
	_ = s.applySocket(nil)
	s.mu.Lock()
	s.closing = true
	links := slices.Collect(maps.Keys(s.links))
	s.mu.Unlock()
	for _, l := range links {
		_ = l.Close()
	}
	s.console.Stop(context.Background())
}

func connOf(c *link.Link) *Conn {
	conn, _ := c.Value().(*Conn)
	return conn
}

func peerOf(c *link.Link) *Peer {
	if conn := connOf(c); conn != nil {
		return conn.Peer
	}
	return nil
}

var errNotRegistered = errors.New("tunnel: register first")

// accept serves a link a node or client opened, which must register within
// register_timeout; past max_links it is closed at once.
func (s *Server) accept(c transport.Conn) transport.Handler {
	set := s.settings()
	l := link.New(c, s.router, set.coder)
	l.SetMaxFrame(unregisteredFrame)
	s.mu.Lock()
	if s.closing || len(s.links) >= set.cfg.Links() {
		s.mu.Unlock()
		return nil
	}
	s.links[l] = struct{}{}
	s.mu.Unlock()
	time.AfterFunc(set.cfg.Register(), func() {
		if peerOf(l) == nil {
			_ = l.Close()
		}
	})
	return l
}

// newRouter is what the server serves nodes and clients.
func (s *Server) newRouter() *link.Router {
	r := link.NewRouter()
	r.Log = s.log
	// Only registration is served before a link has registered.
	r.Guard = func(l *link.Link, m proto.Method) error {
		if m != proto.MethodRegister && peerOf(l) == nil {
			return errNotRegistered
		}
		return nil
	}
	r.OnClose = func(l *link.Link, _ error) { s.disconnected(l) }

	r.HandleCall(proto.MethodRegister, s.register, false)
	r.HandleCall(proto.MethodUpdate, s.update, false)
	r.HandleCall(proto.MethodPing, s.ping, true)
	r.HandleCall(proto.MethodNodes, func(*link.Link, []byte) (any, error) { return s.nodeList(s.nodesVersion.Load()), nil }, false)
	r.HandleCall(proto.MethodSetNodeServices, func(_ *link.Link, body []byte) (any, error) {
		var req proto.SetServicesReq
		if err := link.Decode(body, &req); err != nil {
			return nil, err
		}
		node := s.peerByName(proto.TypeNode, req.Node)
		if node == nil {
			return nil, fmt.Errorf("tunnel: no node %q", req.Node)
		}
		info, err := s.setServices(node, req.Services)
		if err != nil {
			return nil, err
		}
		return &info, nil
	}, false)
	r.HandleCall(proto.MethodTunnelOpen, s.openTunnel, false)
	for _, m := range []proto.Method{proto.MethodTunnelData, proto.MethodTunnelAck, proto.MethodTunnelClose} {
		r.HandleNotify(m, func(l *link.Link, body []byte) { s.forward(l, m, body) }, true)
	}
	return r
}

// register registers a link of a peer's pool: with the peer of the same
// type and name, or as a new peer, in the lowest place none of the peer's
// links holds.
func (s *Server) register(c *link.Link, body []byte) (any, error) {
	var req proto.RegisterReq
	if err := link.Decode(body, &req); err != nil {
		return nil, err
	}
	if peerOf(c) != nil {
		return nil, errors.New("tunnel: already registered")
	}
	set := s.settings()
	if set.verifier == nil {
		return nil, errors.New("tunnel: the server has no registration password set yet")
	}
	if err := set.verifier.Verify(&req); err != nil {
		s.log.Warnf("refused the registration of %s %q from %s: %v", req.Info.Type, req.Info.Name, c.RemoteAddr(), err)
		return nil, err
	}
	key := req.Info.Type + "/" + req.Info.Name
	s.mu.Lock()
	peer := s.byName[key]
	created := peer == nil
	if created {
		if len(s.byID) >= set.cfg.Peers() {
			s.mu.Unlock()
			return nil, errors.New("tunnel: the server has as many peers as it takes")
		}
		peer = &Peer{Addr: c.RemoteAddr(), Since: time.Now(), links: map[int]*link.Link{}, info: req.Info}
		s.nextID++
		if s.nextID == 0 {
			s.nextID++
		}
		peer.ID = s.nextID
	} else if len(peer.links) >= set.cfg.LinksPerPeer() {
		s.mu.Unlock()
		return nil, errors.New("tunnel: the peer has as many links as the server takes")
	}
	select {
	case <-c.Done():
		// The link closed while it registered.
		s.mu.Unlock()
		return nil, link.ErrClosed
	default:
	}
	if created {
		s.byName[key] = peer
		s.byID[peer.ID] = peer
	}
	conn := &Conn{Peer: peer}
	for peer.links[conn.Index] != nil {
		conn.Index++
	}
	s.nextConn++
	if s.nextConn == 0 {
		s.nextConn++
	}
	conn.ID = s.nextConn
	c.SetValue(conn)
	peer.links[conn.Index] = c
	s.peers[c] = peer
	s.mu.Unlock()
	c.SetMaxFrame(link.MaxFrame)
	if created {
		s.log.Infof("%s %q registered from %s as peer %d", req.Info.Type, req.Info.Name, c.RemoteAddr(), peer.ID)
	}
	if created && req.Info.Type == proto.TypeNode {
		defer s.broadcastNodes()
	}
	return &proto.RegisterRsp{Peer: peer.ID, Conn: conn.ID, Index: conn.Index, Server: set.name}, nil
}

func (s *Server) update(c *link.Link, body []byte) (any, error) {
	peer := peerOf(c)
	var info proto.Info
	if err := link.Decode(body, &info); err != nil {
		return nil, err
	}
	old := peer.Info()
	if info.Name != old.Name || info.Type != old.Type {
		return nil, errors.New("tunnel: a peer cannot change its name or type")
	}
	peer.setInfo(info)
	if info.Type == proto.TypeNode {
		defer s.broadcastNodes()
	}
	return "ok", nil
}

// ping records what a peer measured, and tells a client how fast the
// nodes' links are. It is handled where the link is read, so its round trip
// includes the wait behind the tunnel frames read before it.
func (s *Server) ping(c *link.Link, body []byte) (any, error) {
	peer := peerOf(c)
	var req proto.PingReq
	if err := link.Decode(body, &req); err != nil {
		return nil, err
	}
	peer.setLatency(&req)
	var rsp proto.PingRsp
	if peer.Info().Type == proto.TypeClient {
		rsp.Nodes = map[string]proto.NodeLatency{}
		for _, p := range s.Peers() {
			if info := p.Info(); info.Type == proto.TypeNode {
				rsp.Nodes[info.Name] = p.Latency()
			}
		}
	}
	return &rsp, nil
}

// disconnected closes the tunnels a link that closed carried, and forgets
// its peer once that has no link left.
func (s *Server) disconnected(c *link.Link) {
	s.mu.Lock()
	delete(s.links, c)
	peer := s.peers[c]
	if peer == nil {
		s.mu.Unlock()
		return
	}
	delete(s.peers, c)
	for i, l := range peer.links {
		if l == c {
			delete(peer.links, i)
		}
	}
	routes := s.takeRoutesLocked(func(r *Route) bool { return r.ClientLink == c || r.NodeLink == c })
	gone := len(peer.links) == 0
	if gone {
		delete(s.byID, peer.ID)
		if s.byName[peer.key()] == peer {
			delete(s.byName, peer.key())
		}
	}
	s.mu.Unlock()
	info := peer.Info()
	if !gone {
		closeRoutes(routes, peer, "a link of "+info.Type+" "+info.Name+" closed")
		return
	}
	s.log.Infof("%s %q disconnected, closing %d tunnels", info.Type, info.Name, len(routes))
	closeRoutes(routes, peer, info.Type+" "+info.Name+" disconnected")
	if info.Type == proto.TypeNode {
		s.broadcastNodes()
	}
}

// takeRoutesLocked removes the routes match picks, and returns them.
// Callers hold mu.
func (s *Server) takeRoutesLocked(match func(*Route) bool) []*Route {
	var routes []*Route
	for id, r := range s.routes {
		if match(r) {
			r.open.Store(false)
			routes = append(routes, r)
			delete(s.routes, id)
		}
	}
	return routes
}

// closeRoutes tells the other ends of peer's routes, which the server has
// removed, that they are closed.
func closeRoutes(routes []*Route, peer *Peer, reason string) {
	for _, r := range routes {
		other := r.NodeLink
		if r.Node == peer {
			other = r.ClientLink
		}
		notifyClose(other, r.ID, reason)
	}
}

// kick tells peer why and closes its links.
func (s *Server) kick(peer *Peer, reason string) {
	for _, l := range s.peerLinks(peer) {
		_ = l.Notify(proto.MethodKick, reason)
		l.CloseAfterSend()
	}
}

// peerLinks is peer's links, by their places.
func (s *Server) peerLinks(peer *Peer) []*link.Link {
	s.mu.Lock()
	defer s.mu.Unlock()
	return peer.linksLocked()
}

// placesLocked is the places the peer's links hold, lowest first. Callers
// hold the server's lock.
func (p *Peer) placesLocked() []int {
	places := make([]int, 0, len(p.links))
	for i := range p.links {
		places = append(places, i)
	}
	sort.Ints(places)
	return places
}

// linksLocked is the peer's links, by their places. Callers hold the
// server's lock.
func (p *Peer) linksLocked() []*link.Link {
	places := p.placesLocked()
	out := make([]*link.Link, len(places))
	for k, i := range places {
		out[k] = p.links[i]
	}
	return out
}

var errGone = errors.New("tunnel: the peer has no link")

// call calls method on peer, over its link in the lowest place. It blocks,
// for at most call_timeout: it runs on a link's goroutine or the console's
// pool.
func (s *Server) call(peer *Peer, method proto.Method, req, rsp any) error {
	links := s.peerLinks(peer)
	if len(links) == 0 {
		return errGone
	}
	return links[0].Call(method, req, rsp, s.settings().cfg.Call())
}

// notifyClose closes tunnel id at the end link carries.
func notifyClose(l *link.Link, id uint64, reason string) {
	_ = l.SendTunnel(proto.MethodTunnelClose, id, []byte(reason))
}

func (s *Server) peerByName(typ, name string) *Peer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byName[typ+"/"+name]
}

func (s *Server) peerByID(id uint32) *Peer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byID[id]
}

// Peers is every registered peer, by ID.
func (s *Server) Peers() []*Peer {
	s.mu.Lock()
	peers := make([]*Peer, 0, len(s.byID))
	for _, p := range s.byID {
		peers = append(peers, p)
	}
	s.mu.Unlock()
	sort.Slice(peers, func(i, j int) bool { return peers[i].ID < peers[j].ID })
	return peers
}

// Routes is every open tunnel, by ID.
func (s *Server) Routes() []*Route {
	s.mu.Lock()
	routes := make([]*Route, 0, len(s.routes))
	for _, r := range s.routes {
		routes = append(routes, r)
	}
	s.mu.Unlock()
	sort.Slice(routes, func(i, j int) bool { return routes[i].ID < routes[j].ID })
	return routes
}

// nodeList returns the nodes as they are, as version: a list taken after a
// change is counted holds it, so that no list holds less than one of a
// lower version.
func (s *Server) nodeList(version uint64) *proto.NodeList {
	list := &proto.NodeList{Server: s.settings().name, Nodes: []proto.NodeView{}, Epoch: s.epoch, Version: version}
	for _, p := range s.Peers() {
		if info := p.Info(); info.Type == proto.TypeNode {
			list.Nodes = append(list.Nodes, proto.NodeView{Info: info, Addr: p.Addr, Since: p.Since, NodeLatency: p.Latency()})
		}
	}
	sort.Slice(list.Nodes, func(i, j int) bool { return list.Nodes[i].Info.Name < list.Nodes[j].Info.Name })
	return list
}

// broadcastNodes sends every client the nodes, after a change of them.
// Notifies are queued, not waited for.
func (s *Server) broadcastNodes() {
	list := s.nodeList(s.nodesVersion.Add(1))
	for _, p := range s.Peers() {
		if p.Info().Type == proto.TypeClient {
			if links := s.peerLinks(p); len(links) > 0 {
				_ = links[0].Notify(proto.MethodNodes, list)
			}
		}
	}
}

// refreshNode asks node for its info.
func (s *Server) refreshNode(node *Peer) (proto.Info, error) {
	var info proto.Info
	if err := s.call(node, proto.MethodNodeInfo, nil, &info); err != nil {
		return info, err
	}
	if err := s.acceptNodeInfo(node, info); err != nil {
		return info, err
	}
	return info, nil
}

// setServices has node reach services.
func (s *Server) setServices(node *Peer, services []proto.Service) (proto.Info, error) {
	if services == nil {
		services = []proto.Service{}
	}
	var info proto.Info
	if err := s.call(node, proto.MethodNodeSetServices, services, &info); err != nil {
		return info, err
	}
	if err := s.acceptNodeInfo(node, info); err != nil {
		return info, err
	}
	return info, nil
}

func (s *Server) acceptNodeInfo(node *Peer, info proto.Info) error {
	old := node.Info()
	if info.Name != old.Name || info.Type != old.Type {
		return errors.New("tunnel: the node answered with another name or type")
	}
	node.setInfo(info)
	s.broadcastNodes()
	return nil
}

// openTunnel opens the tunnel a client asks for, on the node it names. It
// waits for the node, on the link's goroutine.
func (s *Server) openTunnel(c *link.Link, body []byte) (any, error) {
	client := peerOf(c)
	var req proto.TunnelOpenReq
	if err := link.Decode(body, &req); err != nil {
		return nil, err
	}
	if client.Info().Type != proto.TypeClient {
		return nil, errors.New("tunnel: only a client opens tunnels")
	}
	if proto.TunnelConn(req.ID) != connOf(c).ID {
		return nil, errors.New("tunnel: a tunnel ID must carry its link's number")
	}
	node := s.peerByName(proto.TypeNode, req.Node)
	if node == nil {
		return nil, fmt.Errorf("tunnel: no node %q", req.Node)
	}
	info := node.Info()
	svc, ok := info.FindService(req.Service)
	if !ok {
		return nil, fmt.Errorf("tunnel: node %q has no service %q", req.Node, req.Service)
	}
	r := &Route{ID: req.ID, Client: client, Node: node, ClientLink: c, Service: req.Service, Addr: svc.Addr, Created: time.Now()}
	r.open.Store(true)
	set := s.settings()
	s.mu.Lock()
	if _, taken := s.routes[r.ID]; taken {
		s.mu.Unlock()
		return nil, errors.New("tunnel: tunnel ID in use")
	}
	if len(s.routes) >= set.cfg.Tunnels() {
		s.mu.Unlock()
		return nil, errors.New("tunnel: the server passes as many tunnels as it takes")
	}
	if s.loadLocked(c) >= proto.MaxTunnelsPerLink {
		s.mu.Unlock()
		return nil, errors.New("tunnel: the link carries as many tunnels as it may")
	}
	if r.NodeLink = s.pickLinkLocked(node); r.NodeLink == nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("tunnel: node %q disconnected", req.Node)
	}
	if s.loadLocked(r.NodeLink) >= proto.MaxTunnelsPerLink {
		s.mu.Unlock()
		return nil, fmt.Errorf("tunnel: every link of node %q carries as many tunnels as it may", req.Node)
	}
	s.routes[r.ID] = r
	s.mu.Unlock()

	req.Client = client.Info().Name
	var rsp proto.TunnelOpenRsp
	// The node's end of the tunnel goes on the link the node is asked on.
	if err := r.NodeLink.Call(proto.MethodTunnelOpen, &req, &rsp, set.cfg.Call()); err != nil {
		s.removeRoute(r)
		return nil, err
	}
	if rsp.Addr != "" {
		// The route is listed already, so the pages may be reading it.
		s.mu.Lock()
		r.Addr = rsp.Addr
		s.mu.Unlock()
	}
	if !r.open.Load() {
		// The client gave up while the node connected.
		notifyClose(r.NodeLink, r.ID, "closed while opening")
		return nil, errors.New("tunnel: tunnel closed while opening")
	}
	s.log.Debugf("tunnel %d open: client %q to node %q service %q at %s", r.ID, req.Client, req.Node, req.Service, r.Addr)
	return &rsp, nil
}

// pickLinkLocked is the link of peer a new tunnel goes on: the one that
// carries the fewest tunnels, the first of those that carry as few.
// Callers hold mu.
func (s *Server) pickLinkLocked(peer *Peer) *link.Link {
	links := peer.linksLocked()
	if len(links) == 0 {
		return nil
	}
	load := map[*link.Link]int{}
	for _, r := range s.routes {
		if r.Node == peer {
			load[r.NodeLink]++
		}
	}
	best := links[0]
	for _, l := range links[1:] {
		if load[l] < load[best] {
			best = l
		}
	}
	return best
}

// loadLocked is how many tunnels l carries. Callers hold mu.
func (s *Server) loadLocked(l *link.Link) int {
	n := 0
	for _, r := range s.routes {
		if r.ClientLink == l || r.NodeLink == l {
			n++
		}
	}
	return n
}

func (s *Server) route(id uint64) *Route {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.routes[id]
}

func (s *Server) removeRoute(r *Route) bool {
	r.open.Store(false)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.routes[r.ID] == r {
		delete(s.routes, r.ID)
		return true
	}
	return false
}

// forward passes a tunnel frame to the other end of its tunnel. What it
// queues on the other link is bounded by the tunnel's window, which the
// ends keep (see package tunnel).
func (s *Server) forward(from *link.Link, method proto.Method, body []byte) {
	id, payload, err := proto.ParseFrame(body)
	if err != nil {
		return
	}
	r := s.route(id)
	if r == nil {
		return
	}
	// Each end sends on the link that carries the tunnel, and the frames
	// go on along the other end's, in the order they came.
	var to *link.Link
	switch from {
	case r.ClientLink:
		to = r.NodeLink
		if method == proto.MethodTunnelData {
			r.up.Add(int64(len(payload)))
		}
	case r.NodeLink:
		to = r.ClientLink
		if method == proto.MethodTunnelData {
			r.down.Add(int64(len(payload)))
		}
	default:
		return
	}
	_ = to.Notify(method, body)
	if method == proto.MethodTunnelClose {
		s.removeRoute(r)
	}
}

// CloseRoute closes tunnel id at both ends.
func (s *Server) CloseRoute(id uint64, reason string) bool {
	r := s.route(id)
	if r == nil || !s.removeRoute(r) {
		return false
	}
	notifyClose(r.ClientLink, id, reason)
	notifyClose(r.NodeLink, id, reason)
	return true
}
