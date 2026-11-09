package sharedstore

import (
	"testing"
	"time"

	"github.com/lesismal/fib/bittorrent/metainfo"
	fibtracker "github.com/lesismal/fib/bittorrent/tracker"
)

// TestStoreEdges: an announce the instance's own store refuses is refused
// and not queued, a scrape with the cache full answers from memory, and a
// sample listing a peer twice hands it out once.
func TestStoreEdges(t *testing.T) {
	s := bareStore(Config{Bounds: fibtracker.MemoryStoreConfig{MaxTorrents: 1}, CacheTorrents: 4})
	a, b := hashOf("edges a"), hashOf("edges b")
	if _, err := s.Announce(ann(a, 1, false, fibtracker.EventStarted)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Announce(ann(b, 2, false, fibtracker.EventStarted)); err == nil {
		t.Fatal("a torrent past max_torrents was taken")
	}
	if n := s.queued(); n != 1 {
		t.Fatalf("%d queued, want only the first", n)
	}

	// The cache full: a scrape makes no entry, and answers what the
	// instance has.
	s.entries.Store(int64(s.cfg.CacheTorrents))
	if st, ok := s.Scrape(b); ok || st != (fibtracker.Stats{}) {
		t.Fatalf("a torrent never announced: %+v %v", st, ok)
	}
	if e := s.lookup(b); e != nil {
		t.Fatal("a scrape past the cache's room made an entry")
	}

	now := time.Now()
	p := ann(metainfo.InfoHash{}, 3, false, 0)
	twice := remotePeer{id: p.PeerID, v4: p.V4, expires: now.Add(time.Minute)}
	sn := &snapshot{at: now, lists: [2][]remotePeer{{twice}, {twice}}}
	res := merge(fibtracker.StoreResult{}, sn, ann(metainfo.InfoHash{}, 1, false, fibtracker.EventNone), now)
	if len(res.Peers) != 1 || res.Peers[0].ID != p.PeerID {
		t.Fatalf("a peer listed twice: %v", res.Peers)
	}
}
