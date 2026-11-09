package sharedstore

import (
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/lesismal/fib/bittorrent/metainfo"
	fibtracker "github.com/lesismal/fib/bittorrent/tracker"
)

// The cases run against both a real Redis (redis_test.go) and the fake
// (store_test.go). open opens another instance's store on the same shared
// storage.

func init() {
	// Sweeps every minute would outlast the tests.
	sweepEvery = 100 * time.Millisecond
}

// waitWritten waits until s has written n announces, refused ones counted.
func waitWritten(t *testing.T, s *Store, n uint64) {
	t.Helper()
	waitUntil(t, "the announces to be written", func() bool {
		st := s.Status()
		return st.Written+st.Refused+st.Failed >= n && st.Queued == 0
	})
}

// testTwoTrackers: two instances on one store see each other's peers, a
// seeder gets leechers only, and scrapes count the peers and completed
// downloads of both.
func testTwoTrackers(t *testing.T, open func(Config) *Store) {
	a, b := open(fastConfig()), open(fastConfig())
	h := hashOf("two trackers")
	mustAnnounce(t, a, ann(h, 1, true, fibtracker.EventStarted))
	mustAnnounce(t, a, ann(h, 2, false, fibtracker.EventStarted))
	mustAnnounce(t, a, ann(h, 3, false, fibtracker.EventStarted))
	waitWritten(t, a, 3)

	// b's first answer has its own peers only; once its sample is read,
	// a's too.
	want := []fibtracker.PeerID{peerID(1), peerID(2), peerID(3)}
	waitUntil(t, "b to hand out a's peers", func() bool {
		res := mustAnnounce(t, b, ann(h, 4, false, fibtracker.EventNone))
		return slices.Equal(ids(res.Peers), want)
	})
	waitWritten(t, b, 1)
	waitUntil(t, "b to count every peer", func() bool {
		res := mustAnnounce(t, b, ann(h, 4, false, fibtracker.EventNone))
		return res.Complete == 1 && res.Incomplete == 3
	})
	// A seeder gets leechers only, of both instances.
	leechers := []fibtracker.PeerID{peerID(2), peerID(3), peerID(4)}
	waitUntil(t, "a seeder on b to get the leechers", func() bool {
		res := mustAnnounce(t, b, ann(h, 5, true, fibtracker.EventStarted))
		return slices.Equal(ids(res.Peers), leechers)
	})
	waitUntil(t, "a to scrape both instances' peers", func() bool {
		st, ok := a.Scrape(h)
		return ok && st.Complete == 2 && st.Incomplete == 3
	})
	// A completed download on a counts on b; a stopped peer leaves.
	mustAnnounce(t, a, ann(h, 2, true, fibtracker.EventCompleted))
	mustAnnounce(t, a, ann(h, 3, false, fibtracker.EventStopped))
	waitUntil(t, "b to count the completed download and the stop", func() bool {
		st, ok := b.Scrape(h)
		return ok && st.Downloaded == 1 && st.Complete == 3 && st.Incomplete == 1
	})
	waitUntil(t, "b to stop handing out the stopped peer", func() bool {
		res := mustAnnounce(t, b, ann(h, 4, false, fibtracker.EventNone))
		return slices.Equal(ids(res.Peers), []fibtracker.PeerID{peerID(1), peerID(2), peerID(5)})
	})
	waitUntil(t, "the totals", func() bool { return b.Torrents() == 1 && b.Peers() == 4 && a.Peers() == 4 })
	st := b.Status()
	if !st.Reachable || st.Error != "" || st.Torrents != 1 || st.Peers != 4 || st.LocalPeers != 2 || st.Cached != 1 {
		t.Fatalf("b's status %+v", st)
	}
}

// testSharedBounds: the bounds hold for all instances together, refused
// announces are still answered, and SetConfig raises them.
func testSharedBounds(t *testing.T, open func(Config) *Store) {
	cfg := fastConfig()
	cfg.Bounds = fibtracker.MemoryStoreConfig{MaxTorrents: 2, MaxPeersPerTorrent: 2, MaxPeers: 3}
	a, b := open(cfg), open(cfg)
	h1, h2, h3 := hashOf("bounds 1"), hashOf("bounds 2"), hashOf("bounds 3")
	mustAnnounce(t, a, ann(h1, 1, false, fibtracker.EventStarted))
	mustAnnounce(t, a, ann(h1, 2, false, fibtracker.EventStarted))
	waitWritten(t, a, 2)
	mustAnnounce(t, b, ann(h2, 3, false, fibtracker.EventStarted))
	waitWritten(t, b, 1)
	mustAnnounce(t, b, ann(h3, 4, false, fibtracker.EventStarted)) // a third torrent
	mustAnnounce(t, a, ann(h1, 5, false, fibtracker.EventStarted)) // a third peer of h1
	mustAnnounce(t, b, ann(h2, 6, false, fibtracker.EventStarted)) // a fourth peer
	waitWritten(t, a, 3)
	waitWritten(t, b, 3)
	if sa, sb := a.Status(), b.Status(); sa.Written != 2 || sa.Refused != 1 || sb.Written != 1 || sb.Refused != 2 {
		t.Fatalf("a %+v, b %+v", sa, sb)
	}
	waitUntil(t, "the totals", func() bool { return a.Torrents() == 2 && a.Peers() == 3 })
	// Raised bounds take the next ones.
	a.SetConfig(fibtracker.MemoryStoreConfig{MaxTorrents: 3, MaxPeersPerTorrent: 3, MaxPeers: 10})
	if a.Config().MaxTorrents != 3 {
		t.Fatalf("config %+v", a.Config())
	}
	mustAnnounce(t, a, ann(h3, 7, false, fibtracker.EventStarted))
	waitWritten(t, a, 4)
	if st := a.Status(); st.Written != 3 {
		t.Fatalf("after raising the bounds: %+v", st)
	}
	waitUntil(t, "the totals", func() bool { return a.Torrents() == 3 && a.Peers() == 4 })
}

// testExpiry: shared peers expire after their TTL and their torrents are
// swept.
func testExpiry(t *testing.T, open func(Config) *Store) {
	a, b := open(ttlConfig()), open(ttlConfig())
	h := hashOf("expiry")
	mustAnnounce(t, a, ann(h, 1, true, fibtracker.EventStarted))
	waitWritten(t, a, 1)
	waitUntil(t, "b to see the peer", func() bool { st, ok := b.Scrape(h); return ok && st.Complete == 1 })
	waitUntil(t, "the peer to expire", func() bool { _, ok := b.Scrape(h); return !ok })
	waitUntil(t, "the torrent to be swept", func() bool { return b.Status().Torrents == 0 && b.Status().Peers == 0 })
}

// testRange lists every instance's torrents, and stops when told.
func testRange(t *testing.T, open func(Config) *Store) {
	a, b := open(fastConfig()), open(fastConfig())
	want := map[metainfo.InfoHash]fibtracker.Stats{}
	for i := range 5 {
		h := hashOf("range " + string(rune('a'+i)))
		mustAnnounce(t, a, ann(h, 1, true, fibtracker.EventStarted))
		mustAnnounce(t, a, ann(h, 2, false, fibtracker.EventStarted))
		want[h] = fibtracker.Stats{Complete: 1, Incomplete: 1}
	}
	h := hashOf("range on b")
	mustAnnounce(t, b, ann(h, 3, false, fibtracker.EventStarted))
	want[h] = fibtracker.Stats{Incomplete: 1}
	waitWritten(t, a, 10)
	waitWritten(t, b, 1)
	got := map[metainfo.InfoHash]fibtracker.Stats{}
	a.Range(func(h metainfo.InfoHash, st fibtracker.Stats) bool { got[h] = st; return true })
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for h, st := range want {
		if got[h] != st {
			t.Fatalf("%s: got %+v, want %+v", h, got[h], st)
		}
	}
	n := 0
	b.Range(func(metainfo.InfoHash, fibtracker.Stats) bool { n++; return n < 2 })
	if n != 2 {
		t.Fatalf("Range went on after false: %d", n)
	}
}

// testAddresses: the families a peer wants, numwant, and a peer of port 0
// that is not shared.
func testAddresses(t *testing.T, open func(Config) *Store) {
	a, b := open(fastConfig()), open(fastConfig())
	h := hashOf("addresses")
	both := ann(h, 1, true, fibtracker.EventStarted)
	both.V6 = netip.MustParseAddrPort("[2001:db8::1]:6881")
	only6 := ann(h, 2, true, fibtracker.EventStarted)
	only6.V4, only6.V6 = netip.AddrPort{}, netip.MustParseAddrPort("[2001:db8::2]:6881")
	noPort := ann(h, 3, false, fibtracker.EventStarted)
	noPort.V4 = netip.AddrPort{}
	mustAnnounce(t, a, both)
	mustAnnounce(t, a, only6)
	mustAnnounce(t, a, noPort)
	waitWritten(t, a, 2)
	want6 := ann(h, 9, false, fibtracker.EventNone)
	want6.V4, want6.WantV4, want6.WantV6 = netip.AddrPort{}, false, true
	waitUntil(t, "IPv6 peers", func() bool {
		res := mustAnnounce(t, b, want6)
		return len(res.Peers) == 2 && res.Peers[0].Addr.Addr().Is6() && res.Peers[1].Addr.Addr().Is6()
	})
	want4 := ann(h, 9, false, fibtracker.EventNone)
	want4.V4 = netip.AddrPort{}
	res := mustAnnounce(t, b, want4)
	if len(res.Peers) != 1 || res.Peers[0].Addr != both.V4 {
		t.Fatalf("IPv4 peers %+v", res.Peers)
	}
	want4.WantV6, want4.NumWant = true, 1
	if res := mustAnnounce(t, b, want4); len(res.Peers) != 1 {
		t.Fatalf("numwant 1: %+v", res.Peers)
	}
	want4.NumWant = 0
	if res := mustAnnounce(t, b, want4); len(res.Peers) != 0 || res.Complete != 2 {
		t.Fatalf("numwant 0: %+v", res)
	}
}

// testUnreachable: while the shared storage cannot be reached announces are
// answered from memory and queued, and written once it is back.
func testUnreachable(t *testing.T, open func(Config) *Store, up func(bool)) {
	cfg := fastConfig()
	cfg.Timeout = 300 * time.Millisecond
	a := open(cfg)
	waitUntil(t, "the store to be reachable", func() bool { return a.Status().Reachable })
	up(false)
	restored := false
	defer func() {
		if !restored {
			up(true)
		}
	}()
	h := hashOf("unreachable")
	mustAnnounce(t, a, ann(h, 1, true, fibtracker.EventStarted))
	res := mustAnnounce(t, a, ann(h, 2, false, fibtracker.EventStarted))
	if len(res.Peers) != 1 || res.Peers[0].ID != peerID(1) {
		t.Fatalf("answered from memory: %+v", res)
	}
	waitUntil(t, "the store to be unreachable", func() bool { st := a.Status(); return !st.Reachable && st.Error != "" })
	if st := a.Status(); st.Queued != 2 || st.Written != 0 {
		t.Fatalf("while unreachable: %+v", st)
	}
	// Its own numbers while the shared ones cannot be read.
	if a.Torrents() != 1 || a.Peers() != 2 {
		t.Fatalf("totals while unreachable: %d %d", a.Torrents(), a.Peers())
	}
	up(true)
	restored = true
	waitWritten(t, a, 2)
	waitUntil(t, "the store to be reachable again", func() bool { return a.Status().Reachable })
	b := open(cfg)
	waitUntil(t, "another instance to see the peers", func() bool {
		st, ok := b.Scrape(h)
		return ok && st.Complete == 1 && st.Incomplete == 1
	})
}
