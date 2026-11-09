package sharedstore

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/netip"
	"sync"
	"time"

	"github.com/lesismal/fib/bittorrent/metainfo"
	fibtracker "github.com/lesismal/fib/bittorrent/tracker"
)

// fakeShared is a shared storage in memory with the semantics of the Redis
// scripts, for the store's logic to be tested without Redis; each instance
// has a fakeBackend on it.
type fakeShared struct {
	mu       sync.Mutex
	down     bool // every call fails
	failing  bool // every write is answered with an error
	torrents map[metainfo.InfoHash]*fakeSwarm
	peers    int
	writes   int // write calls, for tests to wait on
}

type fakeSwarm struct {
	peers      map[fibtracker.PeerID]*fakePeer
	downloaded int
}

type fakePeer struct {
	v4, v6  netip.AddrPort
	seeder  bool
	expires time.Time
}

var errDown = errors.New("fake: the store is down")

func newFakeShared() *fakeShared {
	return &fakeShared{torrents: map[metainfo.InfoHash]*fakeSwarm{}}
}

func (f *fakeShared) setDown(down bool) {
	f.mu.Lock()
	f.down = down
	f.mu.Unlock()
}

// open opens an instance's store on f.
func (f *fakeShared) open(cfg Config) *Store { return newStore(&fakeBackend{f: f}, cfg, nil) }

type fakeBackend struct{ f *fakeShared }

func (b *fakeBackend) kind() string { return "fake" }

func (f *fakeShared) prune(h metainfo.InfoHash, sw *fakeSwarm, now time.Time) {
	for id, p := range sw.peers {
		if !now.Before(p.expires) {
			delete(sw.peers, id)
			f.peers--
		}
	}
	if len(sw.peers) == 0 {
		delete(f.torrents, h)
	}
}

func (b *fakeBackend) write(ctx context.Context, ws []*write, bd bounds, now time.Time) ([]result, error) {
	f := b.f
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return nil, errDown
	}
	f.writes++
	out := make([]result, len(ws))
	clock := time.Now()
	for i, w := range ws {
		if f.failing {
			out[i] = resultFailed
			continue
		}
		sw := f.torrents[w.h]
		if sw != nil {
			f.prune(w.h, sw, clock)
			sw = f.torrents[w.h]
		}
		var p *fakePeer
		if sw != nil {
			p = sw.peers[w.id]
		}
		if w.stopped {
			if p != nil {
				delete(sw.peers, w.id)
				f.peers--
				if len(sw.peers) == 0 {
					delete(f.torrents, w.h)
				}
			}
			continue
		}
		expires := w.at.Add(bd.ttl)
		if !clock.Before(expires) {
			continue
		}
		if p == nil {
			switch {
			case sw == nil && len(f.torrents) >= bd.torrents:
				out[i] = resultFull
				continue
			case sw != nil && len(sw.peers) >= bd.perTorrent:
				out[i] = resultSwarmFull
				continue
			case f.peers >= bd.peers:
				out[i] = resultPeersFull
				continue
			}
			if sw == nil {
				sw = &fakeSwarm{peers: map[fibtracker.PeerID]*fakePeer{}}
				f.torrents[w.h] = sw
			}
			p = &fakePeer{}
			sw.peers[w.id] = p
			f.peers++
		}
		if w.completed && w.seeder && !p.seeder {
			sw.downloaded++
		}
		p.v4, p.v6, p.seeder, p.expires = w.v4, w.v6, w.seeder, expires
	}
	return out, nil
}

func (b *fakeBackend) fetch(ctx context.Context, hs []metainfo.InfoHash, peers int, now time.Time) ([]*snapshot, error) {
	f := b.f
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return nil, errDown
	}
	out := make([]*snapshot, len(hs))
	for i, h := range hs {
		out[i] = f.sample(h, peers, now)
	}
	return out, nil
}

func (f *fakeShared) sample(h metainfo.InfoHash, peers int, now time.Time) *snapshot {
	sn := &snapshot{at: now}
	sw := f.torrents[h]
	if sw == nil {
		return sn
	}
	sn.stats.Downloaded = sw.downloaded
	for id, p := range sw.peers {
		if !now.Before(p.expires) {
			continue
		}
		k := 0
		if p.seeder {
			k = 1
			sn.stats.Complete++
		} else {
			sn.stats.Incomplete++
		}
		sn.lists[k] = append(sn.lists[k], remotePeer{id: id, v4: p.v4, v6: p.v6, expires: p.expires})
	}
	for k := range sn.lists {
		rand.Shuffle(len(sn.lists[k]), func(i, j int) { sn.lists[k][i], sn.lists[k][j] = sn.lists[k][j], sn.lists[k][i] })
		sn.lists[k] = sn.lists[k][:min(len(sn.lists[k]), peers)]
	}
	return sn
}

func (b *fakeBackend) totals(ctx context.Context) (int, int, error) {
	f := b.f
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return 0, 0, errDown
	}
	return len(f.torrents), f.peers, nil
}

func (b *fakeBackend) sweep(ctx context.Context, max int) (int, error) {
	f := b.f
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return 0, errDown
	}
	n := 0
	now := time.Now()
	for h, sw := range f.torrents {
		if n >= max {
			break
		}
		before := len(f.torrents)
		f.prune(h, sw, now)
		if len(f.torrents) < before {
			n++
		}
	}
	return n, nil
}

func (b *fakeBackend) scan(ctx context.Context, fn func(metainfo.InfoHash, fibtracker.Stats) bool) error {
	f := b.f
	f.mu.Lock()
	if f.down {
		f.mu.Unlock()
		return errDown
	}
	type item struct {
		h  metainfo.InfoHash
		st fibtracker.Stats
	}
	var items []item
	now := time.Now()
	for h := range f.torrents {
		sn := f.sample(h, 0, now)
		if sn.stats.Complete+sn.stats.Incomplete > 0 {
			items = append(items, item{h, sn.stats})
		}
	}
	f.mu.Unlock()
	for _, it := range items {
		if !fn(it.h, it.st) {
			return nil
		}
	}
	return nil
}

func (b *fakeBackend) close() {}
