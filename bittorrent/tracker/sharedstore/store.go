// Package sharedstore is a store of a BitTorrent tracker's swarms that the
// trackers of several fit instances share, so that they answer as one: the
// store of bittorrent.tracker's store block. It is a fib tracker.Store (and
// has MemoryStore's Range, SetConfig, Config, Torrents and Peers, which the
// role uses).
//
// fib's tracker calls its Store on the fib worker that read the request,
// and waits for the answer, so a Store may not wait for the network. This
// one never does: each instance keeps its own peers in a fib MemoryStore,
// answering from it at once as the tracker does without a shared store, and
// a goroutine of the store's own does every round trip, within its timeout:
//
//   - each announce is queued (bounded, one entry a peer and torrent, a
//     later announce taking an earlier one's place) and written to the
//     shared storage in pipelined batches, as soon as the round trip before
//     ends;
//   - for each torrent announced or scraped here, the store reads a random
//     sample of the shared swarm's seeders and leechers, and its counts,
//     every refresh, and keeps it (bounded: cache_torrents torrents of
//     cache_peers peers of each kind);
//   - an announce is answered with peers of that sample first, then the
//     instance's own, a seeder getting leechers only, and with the larger of
//     each count, the instance's own or the shared one; a scrape alike.
//
// So an instance learns of another's peers within a refresh, and a torrent
// it has not served yet is answered from its own peers until its sample is
// read: the first announce of it here gets only those. While the shared
// storage cannot be reached the store goes on from memory: announces are
// answered with the instance's own peers and the samples younger than three
// refreshes, and queued for when it is back (past the queue, dropped and
// counted). See docs/decisions/037-bittorrent.md (I207 in the dev plan).
package sharedstore

import (
	"context"
	"math/rand/v2"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lesismal/fib/bittorrent/metainfo"
	fibtracker "github.com/lesismal/fib/bittorrent/tracker"

	"github.com/lesismal/fit/logs"
)

// Config configures a Store.
type Config struct {
	// Bounds are the bounds of the instance's own store, and of the shared
	// storage for all instances together: MaxTorrents, MaxPeersPerTorrent,
	// MaxPeers and PeerTTL. fib's defaults when zero.
	Bounds fibtracker.MemoryStoreConfig
	// Refresh is how often a torrent's sample is read again; Timeout bounds
	// each round trip.
	Refresh, Timeout time.Duration
	// Queue bounds the announces waiting to be written, CacheTorrents the
	// torrents with a sample and CachePeers the peers of each kind a sample
	// holds.
	Queue, CacheTorrents, CachePeers int
}

// The defaults of Config.
const (
	DefaultRefresh       = 5 * time.Second
	DefaultTimeout       = 2 * time.Second
	DefaultQueue         = 65536
	DefaultCacheTorrents = 4096
	DefaultCachePeers    = 100
)

func (c Config) withDefaults() Config {
	if c.Refresh <= 0 {
		c.Refresh = DefaultRefresh
	}
	if c.Timeout <= 0 {
		c.Timeout = DefaultTimeout
	}
	if c.Queue <= 0 {
		c.Queue = DefaultQueue
	}
	if c.CacheTorrents <= 0 {
		c.CacheTorrents = DefaultCacheTorrents
	}
	if c.CachePeers <= 0 {
		c.CachePeers = DefaultCachePeers
	}
	return c
}

// How the store batches its round trips.
const (
	// writeBatch announces are written in one round trip, and at most
	// writeRounds such round trips before the samples are looked at.
	writeBatch  = 512
	writeRounds = 8
	// fetchBatch samples are read in one round trip, and at most
	// fetchRounds such round trips in a cycle.
	fetchBatch  = 256
	fetchRounds = 16
	// sweepBatch torrents whose peers have all expired are removed from
	// the shared storage in a round trip, sweepRounds round trips at most
	// every sweepEvery.
	sweepBatch  = 256
	sweepRounds = 16
	// staleRefreshes is how many refreshes a sample is used for.
	staleRefreshes = 3
	// rangeTimeout bounds Range, which reads every torrent's counts.
	rangeTimeout = time.Minute
)

// sweepEvery is how often expired torrents are swept; a variable for tests
// to shorten.
var sweepEvery = time.Minute

// write is an announce waiting to be written to the shared storage.
type write struct {
	h      metainfo.InfoHash
	id     fibtracker.PeerID
	v4, v6 netip.AddrPort
	seeder bool
	// completed says one of the announces it stands for completed the
	// download; stopped says the latest stopped.
	completed, stopped bool
	// at is when its latest announce came.
	at time.Time
}

type pkey struct {
	h  metainfo.InfoHash
	id fibtracker.PeerID
}

// result is what the shared storage made of a write.
type result uint8

const (
	resultOK result = iota
	// resultFull: one torrent more than max_torrents; resultSwarmFull: a
	// peer more than max_peers_per_torrent; resultPeersFull: one more than
	// max_peers in all.
	resultFull
	resultSwarmFull
	resultPeersFull
	// resultFailed: the storage answered the write with an error.
	resultFailed
)

// remotePeer is a peer of a sample.
type remotePeer struct {
	id      fibtracker.PeerID
	v4, v6  netip.AddrPort
	expires time.Time // on this instance's clock
}

// snapshot is a sample of a torrent's shared swarm.
type snapshot struct {
	at    time.Time
	stats fibtracker.Stats
	lists [2][]remotePeer // leechers, seeders
}

// bounds are the shared storage's bounds a write is made under.
type bounds struct {
	torrents, perTorrent, peers int
	ttl                         time.Duration
}

// backend is the shared storage of one kind. write, fetch, totals and
// sweep are called by the store's goroutine alone; scan by any goroutine,
// at the same time as those.
type backend interface {
	kind() string
	// write writes ws, made under b, now being the time on this
	// instance's clock (a write's age is now less its at).
	write(ctx context.Context, ws []*write, b bounds, now time.Time) ([]result, error)
	// fetch reads a sample of each of hs, peers of each kind at most, now
	// being when it is asked, on this instance's clock.
	fetch(ctx context.Context, hs []metainfo.InfoHash, peers int, now time.Time) ([]*snapshot, error)
	// totals reads the torrents and peers kept.
	totals(ctx context.Context) (torrents, peers int, err error)
	// sweep removes up to max torrents whose peers have all expired and
	// returns how many it removed.
	sweep(ctx context.Context, max int) (int, error)
	// scan calls fn with each torrent kept that has peers, and its counts,
	// until fn returns false.
	scan(ctx context.Context, fn func(metainfo.InfoHash, fibtracker.Stats) bool) error
	close()
}

// entry is a torrent this instance serves, with its sample.
type entry struct {
	used atomic.Int64 // unix nanoseconds of its last announce or scrape here
	snap atomic.Pointer[snapshot]
}

type cacheShard struct {
	mu sync.RWMutex
	m  map[metainfo.InfoHash]*entry
}

// Status is how the store is doing, for the tracker's statistics.
type Status struct {
	// Kind is the shared storage's kind, redis.
	Kind string `json:"kind"`
	// Reachable says the last round trip to it succeeded, and Error is
	// the last one's error while it does not.
	Reachable bool   `json:"reachable"`
	Error     string `json:"error,omitempty"`
	// Torrents and Peers are what the shared storage keeps, of every
	// instance, as last read (every refresh); LocalTorrents and LocalPeers
	// what this instance keeps in memory.
	Torrents      int `json:"torrents"`
	Peers         int `json:"peers"`
	LocalTorrents int `json:"local_torrents"`
	LocalPeers    int `json:"local_peers"`
	// Queued is how many announces wait to be written, and Cached how many
	// torrents have a sample.
	Queued int `json:"queued"`
	Cached int `json:"cached"`
	// Written counts the announces written; Dropped those dropped past the
	// queue, or expired while waiting; Refused those the shared storage's
	// bounds refused (they are answered, but not shared); Failed those it
	// answered with an error.
	Written uint64 `json:"written"`
	Dropped uint64 `json:"dropped"`
	Refused uint64 `json:"refused"`
	Failed  uint64 `json:"failed"`
}

// Store is a tracker store shared through a backend. Its methods may be
// called from any goroutine; Announce and Scrape take short locks only.
type Store struct {
	cfg    Config
	local  *fibtracker.MemoryStore
	b      backend
	log    *logs.Logger
	stale  time.Duration
	bounds atomic.Pointer[fibtracker.MemoryStoreConfig]

	qmu     sync.Mutex
	pending map[pkey]*write
	order   []pkey

	shards  [16]cacheShard
	entries atomic.Int64

	wake   chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once

	reachable                         atomic.Bool
	lastErr                           atomic.Pointer[string]
	read                              atomic.Bool // totals have been read
	torrents, peers                   atomic.Int64
	written, dropped, refused, failed atomic.Uint64
}

// newStore returns a store on b and starts its goroutine.
func newStore(b backend, cfg Config, log *logs.Logger) *Store {
	cfg = cfg.withDefaults()
	s := &Store{
		cfg: cfg, local: fibtracker.NewMemoryStore(cfg.Bounds), b: b, log: log,
		stale:   staleRefreshes * cfg.Refresh,
		pending: map[pkey]*write{},
		wake:    make(chan struct{}, 1),
		done:    make(chan struct{}),
	}
	bc := s.local.Config()
	s.bounds.Store(&bc)
	for i := range s.shards {
		s.shards[i].m = map[metainfo.InfoHash]*entry{}
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	go s.run()
	return s
}

// Kind names the shared storage's kind.
func (s *Store) Kind() string { return s.b.kind() }

// Close stops the store's goroutine, cutting short a round trip in flight,
// and closes its connections. What is queued is not written; the peers this
// instance shared stay in the shared storage until they expire.
func (s *Store) Close() {
	s.once.Do(func() {
		s.cancel()
		<-s.done
		s.b.close()
	})
}

// signal wakes the store's goroutine.
func (s *Store) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Announce is part of fib's tracker.Store.
func (s *Store) Announce(a fibtracker.StoreAnnounce) (fibtracker.StoreResult, error) {
	res, err := s.local.Announce(a)
	if err != nil {
		return res, err
	}
	now := time.Now()
	s.enqueue(a, now)
	var e *entry
	if a.Event == fibtracker.EventStopped {
		e = s.lookup(a.InfoHash)
	} else {
		e = s.entry(a.InfoHash, true, now)
	}
	if e == nil {
		return res, nil
	}
	sn := e.snap.Load()
	if sn == nil || now.Sub(sn.at) > s.stale {
		return res, nil
	}
	return merge(res, sn, a, now), nil
}

// Scrape is part of fib's tracker.Store.
func (s *Store) Scrape(h metainfo.InfoHash) (fibtracker.Stats, bool) {
	st, ok := s.local.Scrape(h)
	now := time.Now()
	e := s.entry(h, false, now)
	if e == nil {
		return st, ok
	}
	if sn := e.snap.Load(); sn != nil && now.Sub(sn.at) <= s.stale {
		st = maxStats(st, sn.stats)
		ok = ok || sn.stats.Complete+sn.stats.Incomplete > 0
	}
	return st, ok
}

func maxStats(a, b fibtracker.Stats) fibtracker.Stats {
	return fibtracker.Stats{Complete: max(a.Complete, b.Complete), Incomplete: max(a.Incomplete, b.Incomplete),
		Downloaded: max(a.Downloaded, b.Downloaded)}
}

// merge answers a with the peers of sn first, then those of res, the
// instance's own, that sn lacks, and the larger counts.
func merge(res fibtracker.StoreResult, sn *snapshot, a fibtracker.StoreAnnounce, now time.Time) fibtracker.StoreResult {
	out := fibtracker.StoreResult{Stats: maxStats(res.Stats, sn.stats)}
	leechers, seeders := sn.lists[0], sn.lists[1]
	n := len(leechers)
	if !a.Seeder {
		n += len(seeders)
	}
	if n == 0 || a.NumWant <= 0 || !a.WantV4 && !a.WantV6 {
		out.Peers = res.Peers
		return out
	}
	peers := make([]fibtracker.Peer, 0, min(a.NumWant, n+len(res.Peers)))
	seen := make(map[fibtracker.PeerID]struct{}, min(a.NumWant, n))
	// A random walk of the sample, as fib's MemoryStore picks: from a
	// random start, by a random stride prime to their number.
	start, stride := rand.IntN(n), 1
	if n > 2 {
		for range 8 {
			if st := 1 + rand.IntN(n-1); gcd(st, n) == 1 {
				stride = st
				break
			}
		}
	}
	for i, k := 0, start; i < n && len(peers) < a.NumWant; i, k = i+1, (k+stride)%n {
		var p *remotePeer
		if k < len(leechers) {
			p = &leechers[k]
		} else {
			p = &seeders[k-len(leechers)]
		}
		if p.id == a.PeerID || !now.Before(p.expires) {
			continue
		}
		if _, dup := seen[p.id]; dup {
			continue
		}
		added := false
		if a.WantV4 && p.v4.IsValid() {
			peers = append(peers, fibtracker.Peer{Addr: p.v4, ID: p.id})
			added = true
		}
		if a.WantV6 && p.v6.IsValid() && len(peers) < a.NumWant {
			peers = append(peers, fibtracker.Peer{Addr: p.v6, ID: p.id})
			added = true
		}
		if added {
			seen[p.id] = struct{}{}
		}
	}
	for _, p := range res.Peers {
		if len(peers) >= a.NumWant {
			break
		}
		if _, dup := seen[p.ID]; !dup {
			peers = append(peers, p)
		}
	}
	out.Peers = peers
	return out
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// enqueue queues a for the shared storage.
func (s *Store) enqueue(a fibtracker.StoreAnnounce, now time.Time) {
	stopped := a.Event == fibtracker.EventStopped
	if !stopped && !a.V4.IsValid() && !a.V6.IsValid() {
		return // port 0: a peer that wants peers but is not to be handed out
	}
	k := pkey{a.InfoHash, a.PeerID}
	s.qmu.Lock()
	w := s.pending[k]
	switch {
	case w != nil:
		if a.V4.IsValid() {
			w.v4 = a.V4
		}
		if a.V6.IsValid() {
			w.v6 = a.V6
		}
		w.seeder, w.stopped, w.at = a.Seeder, stopped, now
		w.completed = !stopped && (w.completed || a.Event == fibtracker.EventCompleted)
	case len(s.pending) >= s.cfg.Queue:
		s.qmu.Unlock()
		s.dropped.Add(1)
		return
	default:
		s.pending[k] = &write{h: a.InfoHash, id: a.PeerID, v4: a.V4, v6: a.V6, seeder: a.Seeder,
			completed: a.Event == fibtracker.EventCompleted, stopped: stopped, at: now}
		s.order = append(s.order, k)
	}
	s.qmu.Unlock()
	s.signal()
}

// take takes up to n queued writes, oldest first.
func (s *Store) take(n int) []*write {
	s.qmu.Lock()
	defer s.qmu.Unlock()
	n = min(n, len(s.order))
	if n == 0 {
		return nil
	}
	ws := make([]*write, 0, n)
	for _, k := range s.order[:n] {
		ws = append(ws, s.pending[k])
		delete(s.pending, k)
	}
	clear(s.order[:n])
	s.order = s.order[n:]
	if len(s.order) == 0 {
		s.order = nil
	}
	return ws
}

// requeue puts back writes a failed round trip took, unless a later
// announce of the peer took their place, or they expired.
func (s *Store) requeue(ws []*write, now time.Time) {
	ttl := s.bounds.Load().PeerTTL
	s.qmu.Lock()
	defer s.qmu.Unlock()
	for _, w := range ws {
		k := pkey{w.h, w.id}
		switch later := s.pending[k]; {
		case later != nil:
			later.completed = later.completed || w.completed && !later.stopped
		case now.Sub(w.at) >= ttl || len(s.pending) >= s.cfg.Queue:
			s.dropped.Add(1)
		default:
			s.pending[k] = w
			s.order = append(s.order, k)
		}
	}
}

// queued returns how many writes wait.
func (s *Store) queued() int {
	s.qmu.Lock()
	defer s.qmu.Unlock()
	return len(s.pending)
}

func (s *Store) shard(h metainfo.InfoHash) *cacheShard {
	return &s.shards[h[1]%byte(len(s.shards))]
}

// lookup returns h's entry, or nil.
func (s *Store) lookup(h metainfo.InfoHash) *entry {
	sh := s.shard(h)
	sh.mu.RLock()
	e := sh.m[h]
	sh.mu.RUnlock()
	return e
}

// entry returns h's entry, made if the cache has room, marked used now; nil
// when it has none. A scrape makes one only while the cache is under three
// quarters full, so that scrapes alone cannot keep announced torrents out.
func (s *Store) entry(h metainfo.InfoHash, announce bool, now time.Time) *entry {
	e := s.lookup(h)
	if e == nil {
		limit := int64(s.cfg.CacheTorrents)
		if !announce {
			limit = limit * 3 / 4
		}
		for {
			n := s.entries.Load()
			if n >= limit {
				return nil
			}
			if s.entries.CompareAndSwap(n, n+1) {
				break
			}
		}
		sh := s.shard(h)
		sh.mu.Lock()
		if e = sh.m[h]; e != nil {
			s.entries.Add(-1)
		} else {
			e = &entry{}
			sh.m[h] = e
			defer s.signal()
		}
		sh.mu.Unlock()
	}
	e.used.Store(now.UnixNano())
	return e
}

// due returns the torrents whose sample is to be read, at most max, and
// forgets those nobody has announced or scraped here for the peers' TTL.
func (s *Store) due(now time.Time, max int) []metainfo.InfoHash {
	ttl := s.bounds.Load().PeerTTL
	var hs []metainfo.InfoHash
	var old []metainfo.InfoHash
	for i := range s.shards {
		sh := &s.shards[i]
		old = old[:0]
		sh.mu.RLock()
		for h, e := range sh.m {
			if now.Sub(time.Unix(0, e.used.Load())) >= ttl {
				old = append(old, h)
				continue
			}
			if len(hs) >= max {
				continue
			}
			if sn := e.snap.Load(); sn == nil || now.Sub(sn.at) >= s.cfg.Refresh {
				hs = append(hs, h)
			}
		}
		sh.mu.RUnlock()
		if len(old) > 0 {
			sh.mu.Lock()
			for _, h := range old {
				if e := sh.m[h]; e != nil && now.Sub(time.Unix(0, e.used.Load())) >= ttl {
					delete(sh.m, h)
					s.entries.Add(-1)
				}
			}
			sh.mu.Unlock()
		}
	}
	return hs
}

// run is the store's goroutine: every round trip to the shared storage.
func (s *Store) run() {
	defer close(s.done)
	tick := min(max(s.cfg.Refresh/5, 20*time.Millisecond), time.Second)
	t := time.NewTicker(tick)
	defer t.Stop()
	var retry, lastTotals, lastSweep time.Time
	var backoff time.Duration
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.wake:
		case <-t.C:
		}
		now := time.Now()
		if now.Before(retry) {
			continue
		}
		err := s.cycle(now, &lastTotals, &lastSweep)
		if s.ctx.Err() != nil {
			return
		}
		if err != nil {
			msg := err.Error()
			if old := s.lastErr.Load(); s.reachable.Load() || old == nil || *old != msg {
				if s.log != nil {
					s.log.Warnf("the shared tracker store (%s) is unreachable, answering from memory: %v", s.b.kind(), err)
				}
			}
			s.lastErr.Store(&msg)
			s.reachable.Store(false)
			backoff = min(max(2*backoff, 100*time.Millisecond), min(max(s.cfg.Refresh, time.Second), 10*time.Second))
			retry = time.Now().Add(backoff)
			continue
		}
		if !s.reachable.Load() {
			if s.lastErr.Load() != nil && s.log != nil {
				s.log.Infof("the shared tracker store (%s) is reachable again", s.b.kind())
			}
			s.reachable.Store(true)
		}
		backoff = 0
	}
}

// cycle writes what is queued, reads the samples due, and every refresh
// the totals, every minute sweeping expired torrents.
func (s *Store) cycle(now time.Time, lastTotals, lastSweep *time.Time) error {
	for round := 0; round < writeRounds; round++ {
		ws := s.take(writeBatch)
		if len(ws) == 0 {
			break
		}
		bc := s.bounds.Load()
		b := bounds{torrents: bc.MaxTorrents, perTorrent: bc.MaxPeersPerTorrent, peers: bc.MaxPeers, ttl: bc.PeerTTL}
		ctx, cancel := context.WithTimeout(s.ctx, s.cfg.Timeout)
		rs, err := s.b.write(ctx, ws, b, time.Now())
		cancel()
		if err != nil {
			s.requeue(ws, time.Now())
			return err
		}
		for _, r := range rs {
			switch r {
			case resultOK:
				s.written.Add(1)
			case resultFailed:
				s.failed.Add(1)
			default:
				s.refused.Add(1)
			}
		}
		if round == writeRounds-1 && s.queued() > 0 {
			s.signal()
		}
	}
	due := s.due(now, fetchBatch*fetchRounds)
	for len(due) > 0 {
		n := min(len(due), fetchBatch)
		ctx, cancel := context.WithTimeout(s.ctx, s.cfg.Timeout)
		snaps, err := s.b.fetch(ctx, due[:n], s.cfg.CachePeers, time.Now())
		cancel()
		if err != nil {
			return err
		}
		for i, h := range due[:n] {
			if e := s.lookup(h); e != nil && i < len(snaps) && snaps[i] != nil {
				e.snap.Store(snaps[i])
			}
		}
		due = due[n:]
	}
	if now.Sub(*lastTotals) >= s.cfg.Refresh {
		ctx, cancel := context.WithTimeout(s.ctx, s.cfg.Timeout)
		t, p, err := s.b.totals(ctx)
		cancel()
		if err != nil {
			return err
		}
		s.torrents.Store(int64(t))
		s.peers.Store(int64(p))
		s.read.Store(true)
		*lastTotals = now
	}
	if now.Sub(*lastSweep) >= sweepEvery {
		for range sweepRounds {
			ctx, cancel := context.WithTimeout(s.ctx, s.cfg.Timeout)
			n, err := s.b.sweep(ctx, sweepBatch)
			cancel()
			if err != nil {
				return err
			}
			if n < sweepBatch {
				break
			}
		}
		*lastSweep = now
	}
	return nil
}

// Range calls fn with each torrent the shared storage keeps with peers, of
// every instance, and its counts, until fn returns false; when the storage
// cannot be read before any was given, with this instance's own torrents
// (MemoryStore.Range). It reads the storage, waiting for it: call it off
// fib's workers. The torrents come in no particular order.
func (s *Store) Range(fn func(metainfo.InfoHash, fibtracker.Stats) bool) {
	ctx, cancel := context.WithTimeout(s.ctx, rangeTimeout)
	defer cancel()
	given := false
	err := s.b.scan(ctx, func(h metainfo.InfoHash, st fibtracker.Stats) bool {
		given = true
		return fn(h, st)
	})
	if err == nil {
		return
	}
	if s.log != nil {
		s.log.Warnf("listing the shared tracker store's torrents: %v", err)
	}
	if !given {
		s.local.Range(fn)
	}
}

// SetConfig changes the bounds and the peers' TTL in place, of the
// instance's own store (fib's MemoryStore.SetConfig) and of the writes to
// the shared storage from now on; peers already shared keep their expiry.
func (s *Store) SetConfig(c fibtracker.MemoryStoreConfig) {
	s.local.SetConfig(c)
	bc := s.local.Config()
	s.bounds.Store(&bc)
}

// Config returns the bounds, their defaults filled in.
func (s *Store) Config() fibtracker.MemoryStoreConfig { return s.local.Config() }

// Torrents returns how many torrents the shared storage keeps, of every
// instance, as last read; this instance's own until it has been read, and
// while it cannot be.
func (s *Store) Torrents() int {
	if s.read.Load() && s.reachable.Load() {
		return int(s.torrents.Load())
	}
	return s.local.Torrents()
}

// Peers returns how many peers the shared storage keeps, as Torrents.
func (s *Store) Peers() int {
	if s.read.Load() && s.reachable.Load() {
		return int(s.peers.Load())
	}
	return s.local.Peers()
}

// Status returns how the store is doing.
func (s *Store) Status() Status {
	st := Status{
		Kind: s.b.kind(), Reachable: s.reachable.Load(),
		Torrents: int(s.torrents.Load()), Peers: int(s.peers.Load()),
		LocalTorrents: s.local.Torrents(), LocalPeers: s.local.Peers(),
		Queued: s.queued(), Cached: int(s.entries.Load()),
		Written: s.written.Load(), Dropped: s.dropped.Load(), Refused: s.refused.Load(), Failed: s.failed.Load(),
	}
	if e := s.lastErr.Load(); e != nil && !st.Reachable {
		st.Error = *e
	}
	return st
}
