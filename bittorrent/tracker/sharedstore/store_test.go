package sharedstore

import (
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/lesismal/fib/bittorrent/metainfo"
	fibtracker "github.com/lesismal/fib/bittorrent/tracker"
)

// TestFake runs the cases against the fake shared storage.
func TestFake(t *testing.T) {
	open := func(t *testing.T) (func(Config) *Store, *fakeShared) {
		f := newFakeShared()
		return func(cfg Config) *Store {
			s := f.open(cfg)
			t.Cleanup(s.Close)
			return s
		}, f
	}
	t.Run("two trackers", func(t *testing.T) { t.Parallel(); o, _ := open(t); testTwoTrackers(t, o) })
	t.Run("bounds", func(t *testing.T) { t.Parallel(); o, _ := open(t); testSharedBounds(t, o) })
	t.Run("expiry", func(t *testing.T) { t.Parallel(); o, _ := open(t); testExpiry(t, o) })
	t.Run("range", func(t *testing.T) { t.Parallel(); o, _ := open(t); testRange(t, o) })
	t.Run("addresses", func(t *testing.T) { t.Parallel(); o, _ := open(t); testAddresses(t, o) })
	t.Run("unreachable", func(t *testing.T) {
		t.Parallel()
		o, f := open(t)
		testUnreachable(t, o, func(up bool) { f.setDown(!up) })
	})
}

// bareStore is a store without its goroutine, for its parts to be tested
// one at a time.
func bareStore(cfg Config) *Store {
	cfg = cfg.withDefaults()
	s := &Store{cfg: cfg, local: fibtracker.NewMemoryStore(cfg.Bounds), pending: map[pkey]*write{}, wake: make(chan struct{}, 1)}
	bc := s.local.Config()
	s.bounds.Store(&bc)
	for i := range s.shards {
		s.shards[i].m = map[metainfo.InfoHash]*entry{}
	}
	return s
}

// TestQueue: a peer's announces take one place in the queue, a completed
// download is kept through later announces but not through a stop, the
// queue's bound drops what is past it, and a port of 0 is not queued.
func TestQueue(t *testing.T) {
	s := bareStore(Config{Queue: 2})
	h := hashOf("queue")
	now := time.Now()
	s.enqueue(ann(h, 1, false, fibtracker.EventStarted), now)
	s.enqueue(ann(h, 1, true, fibtracker.EventCompleted), now)
	s.enqueue(ann(h, 1, true, fibtracker.EventNone), now)
	v6 := ann(h, 2, false, fibtracker.EventStarted)
	v6.V6 = netip.MustParseAddrPort("[2001:db8::2]:1")
	s.enqueue(v6, now)
	v4only := ann(h, 2, false, fibtracker.EventNone)
	s.enqueue(v4only, now)
	s.enqueue(ann(h, 3, false, fibtracker.EventStarted), now) // past the bound
	noPort := ann(h, 4, false, fibtracker.EventStarted)
	noPort.V4 = netip.AddrPort{}
	s.enqueue(noPort, now)
	if s.queued() != 2 || s.dropped.Load() != 1 {
		t.Fatalf("queued %d, dropped %d", s.queued(), s.dropped.Load())
	}
	ws := s.take(10)
	if len(ws) != 2 || ws[0].id != peerID(1) || !ws[0].completed || !ws[0].seeder || ws[1].v6 != v6.V6 || ws[1].v4 != v4only.V4 {
		t.Fatalf("taken %+v %+v", ws[0], ws[1])
	}
	if s.take(10) != nil || len(s.order) != 0 {
		t.Fatal("the queue was not emptied")
	}
	s.enqueue(ann(h, 1, true, fibtracker.EventCompleted), now)
	s.enqueue(ann(h, 1, true, fibtracker.EventStopped), now)
	if ws := s.take(1); len(ws) != 1 || ws[0].completed || !ws[0].stopped {
		t.Fatalf("a stop after a completion: %+v", ws[0])
	}
}

// TestRequeue: writes a failed round trip took go back, unless a later
// announce took their place (keeping their completion), they expired, or
// the queue is full.
func TestRequeue(t *testing.T) {
	s := bareStore(Config{Queue: 2, Bounds: fibtracker.MemoryStoreConfig{PeerTTL: time.Minute}})
	h := hashOf("requeue")
	now := time.Now()
	s.enqueue(ann(h, 1, true, fibtracker.EventCompleted), now)
	s.enqueue(ann(h, 2, false, fibtracker.EventStarted), now.Add(-2*time.Minute))
	ws := s.take(2)
	s.enqueue(ann(h, 1, true, fibtracker.EventNone), now)
	s.requeue(ws, now)
	if s.queued() != 1 || s.dropped.Load() != 1 {
		t.Fatalf("queued %d, dropped %d", s.queued(), s.dropped.Load())
	}
	if ws := s.take(2); len(ws) != 1 || !ws[0].completed {
		t.Fatalf("the later write lost the completion: %+v", ws)
	}
	// A full queue drops what comes back.
	s.enqueue(ann(h, 3, false, fibtracker.EventStarted), now)
	ws = s.take(1)
	s.enqueue(ann(h, 4, false, fibtracker.EventStarted), now)
	s.enqueue(ann(h, 5, false, fibtracker.EventStarted), now)
	s.requeue(ws, now)
	if s.queued() != 2 || s.dropped.Load() != 2 {
		t.Fatalf("queued %d, dropped %d", s.queued(), s.dropped.Load())
	}
}

// TestCache: scrapes fill three quarters of the cache, announces all of
// it; torrents unused for the TTL leave it, and due returns those whose
// sample is missing or old, at most max.
func TestCache(t *testing.T) {
	s := bareStore(Config{CacheTorrents: 4, Refresh: time.Second, Bounds: fibtracker.MemoryStoreConfig{PeerTTL: time.Minute}})
	now := time.Now()
	for i := range 3 {
		if s.entry(hashOf("scraped "+string(rune('a'+i))), false, now) == nil {
			t.Fatalf("scrape %d found no room", i)
		}
	}
	if s.entry(hashOf("scraped d"), false, now) != nil {
		t.Fatal("scrapes filled more than three quarters")
	}
	if s.entry(hashOf("scraped a"), false, now) == nil {
		t.Fatal("a torrent already cached was refused")
	}
	if s.entry(hashOf("announced"), true, now) == nil || s.entry(hashOf("announced 2"), true, now) != nil {
		t.Fatal("announces did not fill the cache to its bound")
	}
	if s.entries.Load() != 4 {
		t.Fatalf("%d entries", s.entries.Load())
	}
	// A sample read now is not due; the others are, two at most.
	s.lookup(hashOf("announced")).snap.Store(&snapshot{at: now})
	if due := s.due(now, 2); len(due) != 2 {
		t.Fatalf("due %v", due)
	}
	if due := s.due(now, 10); len(due) != 3 || slices.Contains(due, hashOf("announced")) {
		t.Fatalf("due %v", due)
	}
	if due := s.due(now.Add(time.Second), 10); len(due) != 4 {
		t.Fatalf("due a refresh later %v", due)
	}
	// Unused for the TTL, they leave.
	s.entry(hashOf("announced"), true, now.Add(time.Minute))
	if due := s.due(now.Add(time.Minute+time.Second), 10); len(due) != 1 || s.entries.Load() != 1 {
		t.Fatalf("after the TTL: due %v, %d entries", due, s.entries.Load())
	}
}

// TestMerge: the sample's peers first, never the peer itself nor an expired
// one, a seeder getting leechers only; then the instance's own that the
// sample lacks, up to numwant; the larger counts.
func TestMerge(t *testing.T) {
	now := time.Now()
	rp := func(i int, ttl time.Duration) remotePeer {
		a := ann(metainfo.InfoHash{}, i, false, 0)
		return remotePeer{id: a.PeerID, v4: a.V4, expires: now.Add(ttl)}
	}
	sn := &snapshot{at: now, stats: fibtracker.Stats{Complete: 2, Incomplete: 3, Downloaded: 1}, lists: [2][]remotePeer{
		{rp(1, time.Minute), rp(2, time.Minute), rp(3, -time.Second)}, // leechers, 3 expired
		{rp(4, time.Minute), rp(5, time.Minute)},
	}}
	local := fibtracker.StoreResult{Stats: fibtracker.Stats{Complete: 1, Incomplete: 4},
		Peers: []fibtracker.Peer{{ID: peerID(2), Addr: ann(metainfo.InfoHash{}, 2, false, 0).V4}, {ID: peerID(6), Addr: ann(metainfo.InfoHash{}, 6, false, 0).V4}}}

	a := ann(metainfo.InfoHash{}, 1, false, fibtracker.EventNone)
	res := merge(local, sn, a, now)
	if got := ids(res.Peers); !slices.Equal(got, []fibtracker.PeerID{peerID(2), peerID(4), peerID(5), peerID(6)}) || len(res.Peers) != 4 {
		t.Fatalf("a leecher got %v (%d)", got, len(res.Peers))
	}
	if res.Stats != (fibtracker.Stats{Complete: 2, Incomplete: 4, Downloaded: 1}) {
		t.Fatalf("stats %+v", res.Stats)
	}
	a.Seeder = true
	res = merge(fibtracker.StoreResult{}, sn, a, now)
	if got := ids(res.Peers); !slices.Equal(got, []fibtracker.PeerID{peerID(2)}) {
		t.Fatalf("a seeder got %v", got)
	}
	a.Seeder, a.NumWant = false, 2
	if res := merge(local, sn, a, now); len(res.Peers) != 2 {
		t.Fatalf("numwant 2: %v", res.Peers)
	}
	a.NumWant = 0
	if res := merge(local, sn, a, now); len(res.Peers) != 2 || res.Peers[1].ID != peerID(6) {
		t.Fatalf("numwant 0 keeps fib's answer: %v", res.Peers)
	}
	a.NumWant, a.WantV4 = 10, false
	if res := merge(local, sn, a, now); len(res.Peers) != 2 {
		t.Fatalf("no family wanted keeps fib's answer: %v", res.Peers)
	}
}

// TestStale: a sample older than three refreshes is not used while the
// shared storage cannot be read; Range falls back to the instance's own
// torrents then.
func TestStale(t *testing.T) {
	f := newFakeShared()
	a, b := f.open(fastConfig()), f.open(fastConfig())
	defer a.Close()
	defer b.Close()
	h := hashOf("stale")
	mustAnnounce(t, a, ann(h, 1, true, fibtracker.EventStarted))
	waitWritten(t, a, 1)
	waitUntil(t, "b to hand out a's peer", func() bool {
		return len(mustAnnounce(t, b, ann(h, 2, false, fibtracker.EventNone)).Peers) == 1
	})
	f.setDown(true)
	defer f.setDown(false)
	waitUntil(t, "b to stop using its old sample", func() bool {
		return len(mustAnnounce(t, b, ann(h, 2, false, fibtracker.EventNone)).Peers) == 0
	})
	var listed []metainfo.InfoHash
	b.Range(func(h metainfo.InfoHash, _ fibtracker.Stats) bool { listed = append(listed, h); return true })
	if len(listed) != 1 || listed[0] != h {
		t.Fatalf("Range while down: %v", listed)
	}
	// A stop of a torrent with no entry is answered too.
	mustAnnounce(t, b, ann(hashOf("never seen"), 3, false, fibtracker.EventStopped))
}

// TestFailed: writes the storage answers with an error are counted, and the
// store stays reachable.
func TestFailed(t *testing.T) {
	f := newFakeShared()
	f.failing = true
	s := f.open(fastConfig())
	defer s.Close()
	mustAnnounce(t, s, ann(hashOf("failed"), 1, false, fibtracker.EventStarted))
	waitWritten(t, s, 1)
	if st := s.Status(); st.Failed != 1 || st.Written != 0 || st.Kind != "fake" {
		t.Fatalf("%+v", st)
	}
	waitUntil(t, "reachable", func() bool { return s.Status().Reachable })
	s.Close()
	s.Close()
}

// TestManyWrites: more announces than a cycle writes are all written.
func TestManyWrites(t *testing.T) {
	f := newFakeShared()
	s := f.open(Config{Refresh: time.Hour, Timeout: time.Second, Bounds: fibtracker.MemoryStoreConfig{MaxPeersPerTorrent: 10000}})
	defer s.Close()
	f.setDown(true)
	n := writeBatch*writeRounds + 100
	h := hashOf("many")
	for i := range n {
		mustAnnounce(t, s, ann(h, i, false, fibtracker.EventStarted))
	}
	f.setDown(false)
	waitWritten(t, s, uint64(n))
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.peers != n {
		t.Fatalf("%d peers shared", f.peers)
	}
}
