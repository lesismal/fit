// Package tracker is fit's BitTorrent tracker role, bittorrent.tracker in
// fit's config: fib's tracker (github.com/lesismal/fib/bittorrent/tracker)
// over HTTP, through the routes whose handler is bt_tracker, and over UDP,
// on the role's own listen, both on one bounded store in memory, on the
// listeners' fib engine; with a store block, on a store the trackers of
// several instances share (package sharedstore), which answers from memory
// too. See docs/decisions/037-bittorrent.md and docs/bittorrent.md.
//
// Nothing here blocks a fib worker: an announce or a scrape is served on
// the worker that read it, by fib's tracker, whose store takes a short lock
// of its shard; the hooks it asks, the allow-list, the passkeys and the
// access, are lookups in what Apply compiled. The allow-list's files are
// read by Apply, on the control plane.
//
// A reload changes in place the allow-list, the passkeys' policy, the
// access, the console and the store's bounds (max_torrents,
// max_peers_per_torrent, max_peers, peer_ttl, through fib's
// MemoryStore.SetConfig), keeping the swarms. The interval, min_interval,
// max_numwant and max_scrape make a new fib tracker on the same store. A
// listen change moves the UDP binding, and a port that cannot be bound
// leaves the one before. A change of the store block makes a new store,
// this instance's swarms starting anew.
package tracker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/lesismal/fib"
	"github.com/lesismal/fib/bittorrent/metainfo"
	fibtracker "github.com/lesismal/fib/bittorrent/tracker"

	"github.com/lesismal/fit/access"
	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/app/console"
	"github.com/lesismal/fit/app/roleconsole"
	"github.com/lesismal/fit/bittorrent/tracker/sharedstore"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/mediaauth"
)

// ID is the role's, and its member's path in the config.
const ID = "bittorrent.tracker"

func member(c *config.Config) *config.BitTorrentTracker {
	if c.BitTorrent == nil {
		return nil
	}
	return c.BitTorrent.Tracker
}

func init() {
	app.Register(app.Info{
		ID:     ID,
		Member: func(c *config.Config) any { return member(c) },
		New:    func(h *app.Host) (app.Role, error) { return New(h), nil },
		// The allow-list's and the access's files, read when the role
		// applies: a change of one applies the config again.
		CertFiles: func(c *config.Config) []string { return member(c).Files() },
	})
}

// The roles, by the engine the routes' modules are given (app.Host.Upstream,
// module.Context.Engine), swapped whole so that a request finds its role
// without a lock.
var (
	rolesMu sync.Mutex
	roles   atomic.Pointer[map[*fib.Engine]*Role]
)

func register(key *fib.Engine, r *Role) {
	rolesMu.Lock()
	defer rolesMu.Unlock()
	m := map[*fib.Engine]*Role{}
	if old := roles.Load(); old != nil {
		for k, v := range *old {
			m[k] = v
		}
	}
	if r == nil {
		delete(m, key)
	} else {
		m[key] = r
	}
	roles.Store(&m)
}

// unregister removes r, if it is still engine's role.
func unregister(key *fib.Engine, r *Role) {
	if Lookup(key) == r {
		register(key, nil)
	}
}

// Lookup returns the tracker role of engine, or nil.
func Lookup(engine *fib.Engine) *Role {
	if m := roles.Load(); m != nil {
		return (*m)[engine]
	}
	return nil
}

// view is what the statistics report of the config applied.
type view struct {
	udp                   string
	maxTorrents, maxPeers int
}

// swarms is the role's store: fib's MemoryStore, or a *sharedstore.Store.
// Announce, Scrape, Config, Torrents and Peers never wait; Range may, and
// is called off fib's workers.
type swarms interface {
	fibtracker.Store
	Range(func(metainfo.InfoHash, fibtracker.Stats) bool)
	SetConfig(fibtracker.MemoryStoreConfig)
	Config() fibtracker.MemoryStoreConfig
	Torrents() int
	Peers() int
}

var (
	_ swarms = (*fibtracker.MemoryStore)(nil)
	_ swarms = (*sharedstore.Store)(nil)
)

// server is a fib tracker and its store.
type server struct {
	srv   *fibtracker.Server
	store swarms
}

// newSwarms makes the store cfg asks for, bounded by scfg: a shared one
// with a store block, which reads its TLS files now and connects on its
// own goroutine, or one in memory.
func newSwarms(cfg *config.BitTorrentTracker, scfg fibtracker.MemoryStoreConfig, log *logs.Logger) (swarms, error) {
	st := cfg.Store
	if st == nil {
		return fibtracker.NewMemoryStore(scfg), nil
	}
	s, err := sharedstore.NewRedis(st.Redis, st.KeyPrefix(), sharedstore.Config{
		Bounds: scfg, Refresh: st.RefreshOr(), Timeout: st.Timeout(),
		Queue: st.QueueOr(), CacheTorrents: st.Torrents(), CachePeers: st.Peers(),
	}, log)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	return s, nil
}

// closeSwarms stops a shared store's goroutine; a store in memory has none.
func closeSwarms(s swarms) {
	if ss, ok := s.(*sharedstore.Store); ok {
		ss.Close()
	}
}

// sharedKey is the store block, a change of which makes a new store.
func sharedKey(cfg *config.BitTorrentTracker) string {
	if cfg.Store == nil {
		return ""
	}
	b, _ := json.Marshal(cfg.Store)
	return string(b)
}

// The reasons a request is refused, counted in Stats.
const (
	refusedUnregistered = iota
	refusedPasskey
	refusedAccess
	numRefusals
)

var refusalNames = [numRefusals]string{"unregistered", "passkey", "access"}

// Role is the bittorrent.tracker role.
type Role struct {
	host   *app.Host
	engine *fib.Engine
	key    *fib.Engine
	log    *logs.Logger

	// gate is the client addresses served, the UDP binding's Filter and
	// bt_tracker's check, set by every Apply.
	gate    access.Gate
	console *roleconsole.Console
	// secret keys the UDP connection IDs of every fib tracker the role
	// makes, so that a client's ID outlives a reload.
	secret []byte

	// cur is the tracker serving now; nil while the role does not run.
	cur atomic.Pointer[server]
	// allow is the allow-list; nil tracks every torrent.
	allow atomic.Pointer[allowList]
	// auth is the passkeys' policy; nil for a public tracker.
	auth atomic.Pointer[guard]
	// udp hands the UDP binding's datagrams to the tracker of now.
	udp udpHandler

	announces, scrapes atomic.Uint64
	refused            [numRefusals]atomic.Uint64
	// view is what Stats reports of the config, set by every Apply.
	view atomic.Pointer[view]

	// mu orders Apply and Stop, and guards what follows.
	mu        sync.Mutex
	stopped   bool
	cfg       *config.BitTorrentTracker
	storeKey  string
	serverKey string
	sharedKey string
	bind      int // the UDP binding's index, or -1
	listen    string
	udpAddr   netip.AddrPort
}

// New returns the role, which its first Apply starts.
func New(h *app.Host) *Role {
	r := &Role{host: h, engine: h.Engine, key: h.Upstream, log: h.Log, bind: -1, secret: newSecret()}
	if r.key == nil {
		r.key = h.Engine
	}
	r.udp.r = r
	r.console = roleconsole.New(h, ID, r.routes)
	return r
}

// Module returns the role's module of the workbench.
func (r *Role) Module() *console.Module { return r.console.Module() }

// storeKey is the store's bounds, set in place when they change.
func storeKey(cfg *config.BitTorrentTracker) string {
	return fmt.Sprint(cfg.Torrents(), "|", cfg.PeersPerTorrent(), "|", cfg.Peers(), "|", cfg.TTL())
}

// serverKey is what fib's Server takes only when it is made.
func serverKey(cfg *config.BitTorrentTracker) string {
	return fmt.Sprint(cfg.IntervalOr(), "|", cfg.MinInterval.Std(), "|", cfg.NumWant(), "|", cfg.Scrape())
}

// Apply moves the role to member, a *config.BitTorrentTracker. An error
// leaves the role as it was.
func (r *Role) Apply(member any) error {
	cfg, ok := member.(*config.BitTorrentTracker)
	if !ok || cfg == nil {
		return errors.New("bittorrent.tracker: no member")
	}
	policy, err := r.host.Access(cfg.Access)
	if err != nil {
		return fmt.Errorf("access: %w", err)
	}
	allow, err := loadAllow(cfg)
	if err != nil {
		return fmt.Errorf("allow-list: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return app.ErrStopped
	}
	// The access before a new binding takes anyone; the UDP port first of
	// the rest, so that a port that cannot be bound leaves all as it was.
	sk, vk, shk := storeKey(cfg), serverKey(cfg), sharedKey(cfg)
	scfg := fibtracker.MemoryStoreConfig{MaxTorrents: cfg.Torrents(), MaxPeersPerTorrent: cfg.PeersPerTorrent(),
		MaxPeers: cfg.Peers(), PeerTTL: cfg.TTL()}
	cur := r.cur.Load()
	// A new store, when the role starts or its store block changed, made
	// before anything changes, so that one that cannot be made (a TLS file
	// that does not read) leaves all as it was.
	var fresh swarms
	if cur == nil || shk != r.sharedKey {
		if fresh, err = newSwarms(cfg, scfg, r.log); err != nil {
			return err
		}
	}
	oldPolicy := r.gate.Policy()
	r.gate.Set(policy)
	if err := r.applyListen(cfg.Listen); err != nil {
		r.gate.Set(oldPolicy)
		if fresh != nil {
			closeSwarms(fresh)
		}
		return err
	}
	r.allow.Store(allow)
	if cfg.Auth == "" {
		r.auth.Store(nil)
	} else {
		// The role media_auth may apply after this one: the policy is
		// found now, and again at each check while it is not. Found
		// anew at every Apply, for a policy removed and made again.
		r.auth.Store(&guard{key: r.key, name: cfg.Auth, p: mediaauth.Lookup(r.key, cfg.Auth)})
	}
	switch {
	case fresh != nil:
		r.cur.Store(&server{srv: r.newServer(cfg, fresh), store: fresh})
		if cur != nil {
			// Requests already in the old store's hands finish there;
			// what it had queued for the shared storage is let go.
			closeSwarms(cur.store)
			if r.log != nil {
				r.log.Infof("the store changed: this instance's swarms start anew")
			}
		}
	case vk != r.serverKey:
		if sk != r.storeKey {
			cur.store.SetConfig(scfg)
		}
		r.cur.Store(&server{srv: r.newServer(cfg, cur.store), store: cur.store})
	case sk != r.storeKey:
		// The bounds and the peers' TTL in place: the swarms stay. Lowered
		// bounds drop nothing; new torrents and peers wait for room.
		cur.store.SetConfig(scfg)
	}
	if cur != nil && fresh == nil && sk != r.storeKey && r.log != nil {
		r.log.Infof("the store's bounds changed in place, the swarms kept")
	}
	r.storeKey, r.serverKey, r.sharedKey = sk, vk, shk
	r.cfg = cfg
	v := &view{maxTorrents: cfg.Torrents(), maxPeers: cfg.Peers()}
	if r.udpAddr.IsValid() {
		v.udp = r.udpAddr.String()
	}
	r.view.Store(v)
	register(r.key, r)
	if err := r.console.Apply(cfg.Console); err != nil {
		return fmt.Errorf("console: %w", err)
	}
	return nil
}

// newServer makes fib's tracker of cfg on store, its hooks the role's.
func (r *Role) newServer(cfg *config.BitTorrentTracker, store fibtracker.Store) *fibtracker.Server {
	return fibtracker.NewServer(fibtracker.ServerConfig{
		Store:         store,
		Interval:      cfg.IntervalOr(),
		MinInterval:   cfg.MinInterval.Std(),
		MaxNumWant:    cfg.NumWant(),
		MaxScrape:     cfg.Scrape(),
		AllowInfoHash: r.allowed,
		Authorize:     r.authorize,
		Secret:        r.secret,
	})
}

// applyListen moves the UDP binding to listen, "" for none. A new address
// that cannot be bound leaves the binding before. r.mu is held.
func (r *Role) applyListen(listen string) error {
	if listen == r.listen && (listen == "") == (r.bind < 0) {
		return nil
	}
	if listen == "" {
		r.closeUDP()
		return nil
	}
	old, oldAddr := r.bind, r.udpAddr
	i, addr, err := r.bindUDP(listen)
	if err != nil && old >= 0 && samePort(listen, oldAddr) {
		// The old binding's own port, on its host or another: free it,
		// and take it back, where it was bound, on failure.
		_ = r.engine.RemoveBinding(old)
		r.bind, r.udpAddr = -1, netip.AddrPort{}
		if i, addr, err = r.bindUDP(listen); err != nil {
			if j, back, berr := r.bindUDP(oldAddr.String()); berr == nil {
				r.bind, r.udpAddr = j, back
			} else if r.log != nil {
				r.log.Errorf("the UDP port before, %s, could not be bound again either: %v", oldAddr, berr)
			}
		}
		old = -1
	}
	if err != nil {
		return fmt.Errorf("listen on %s: %w", listen, err)
	}
	if old >= 0 {
		_ = r.engine.RemoveBinding(old)
	}
	r.bind, r.listen, r.udpAddr = i, listen, addr
	if r.log != nil {
		r.log.Infof("udp on %s", addr)
	}
	return nil
}

// samePort reports whether listen names the port of ap.
func samePort(listen string, ap netip.AddrPort) bool {
	_, port, err := net.SplitHostPort(listen)
	return err == nil && ap.IsValid() && port == strconv.Itoa(int(ap.Port()))
}

// bindUDP adds a UDP binding on listen and returns its index and address.
func (r *Role) bindUDP(listen string) (int, netip.AddrPort, error) {
	i, err := r.engine.AddBinding(fib.Binding{Network: "udp", Addr: listen, Handler: &r.udp, Filter: r.gate.Allows})
	if err != nil {
		return -1, netip.AddrPort{}, err
	}
	var addr netip.AddrPort
	if addrs, err := r.engine.BindingAddrs(i); err == nil && len(addrs) > 0 {
		addr, _ = netip.ParseAddrPort(addrs[0].String())
	}
	return i, addr, nil
}

// closeUDP removes the UDP binding. r.mu is held.
func (r *Role) closeUDP() {
	if r.bind >= 0 {
		_ = r.engine.RemoveBinding(r.bind)
	}
	r.bind, r.listen, r.udpAddr = -1, "", netip.AddrPort{}
}

// Stop removes the UDP binding and the console, and forgets the swarms.
func (r *Role) Stop(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return
	}
	r.stopped = true
	r.closeUDP()
	if cur := r.cur.Swap(nil); cur != nil {
		closeSwarms(cur.store)
	}
	r.console.Stop(ctx)
	unregister(r.key, r)
}

// UDPAddr returns where the tracker takes UDP requests, or the zero
// address without listen.
func (r *Role) UDPAddr() netip.AddrPort {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.udpAddr
}

// udpHandler is the UDP binding's fib.Handler: it hands datagrams to the
// fib tracker of now, so that a new one takes over without a new binding.
type udpHandler struct{ r *Role }

// OnOpen is part of fib.Handler.
func (h *udpHandler) OnOpen(*fib.Connection) {}

// OnPriorityData is part of fib.Handler.
func (h *udpHandler) OnPriorityData(*fib.Connection, []byte) {}

// OnClose is part of fib.Handler.
func (h *udpHandler) OnClose(*fib.Connection, error) {}

// OnData answers a datagram.
func (h *udpHandler) OnData(c *fib.Connection, b []byte) { h.OnDatagrams(c, [][]byte{b}) }

// OnDatagrams answers a client's datagrams; while the role does not run
// they are dropped.
func (h *udpHandler) OnDatagrams(c *fib.Connection, dgrams [][]byte) {
	if s := h.r.cur.Load(); s != nil {
		s.srv.UDP().OnDatagrams(c, dgrams)
	}
}
