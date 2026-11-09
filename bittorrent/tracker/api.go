package tracker

import (
	"bytes"
	"container/heap"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/lesismal/fib/bittorrent/metainfo"
	fibtracker "github.com/lesismal/fib/bittorrent/tracker"

	"github.com/lesismal/fit/app/console"
	"github.com/lesismal/fit/bittorrent/tracker/sharedstore"
	"github.com/lesismal/fit/config"
)

// The role's API, for the Admin API (I203), the CLI and the pages to build
// on. Its methods take no lock a request waits for, and may be called from
// any goroutine.

// ErrNotRunning is a call while the role does not run.
var ErrNotRunning = errors.New("bittorrent.tracker: the tracker does not run")

// Stats are the tracker's totals.
type Stats struct {
	// Torrents and Peers are what the store keeps now, expired peers it has
	// not looked at yet among them.
	Torrents int `json:"torrents"`
	Peers    int `json:"peers"`
	// Announces and Scrapes count those served since the role started,
	// and Refused those refused, by reason: unregistered (an info hash
	// the allow-list lacks), passkey and access.
	Announces uint64            `json:"announces"`
	Scrapes   uint64            `json:"scrapes"`
	Refused   map[string]uint64 `json:"refused"`
	// Private says passkeys are required, Allowed how many info hashes the
	// allow-list holds (0 without one), and UDP where UDP requests are
	// taken ("" without).
	Private bool   `json:"private"`
	Allowed int    `json:"allowed"`
	UDP     string `json:"udp,omitempty"`
	// MaxTorrents and MaxPeers are the store's bounds.
	MaxTorrents int `json:"max_torrents"`
	MaxPeers    int `json:"max_peers"`
	// Store is how the store the trackers of several instances share is
	// doing, with a store block; Torrents and Peers are then the shared
	// storage's, of every instance, as last read (its own while it cannot
	// be read), and Store's local_torrents and local_peers this instance's.
	Store *sharedstore.Status `json:"store,omitempty"`
}

// TorrentStats is one torrent's swarm.
type TorrentStats struct {
	InfoHash  string `json:"info_hash"`
	Seeders   int    `json:"seeders"`
	Leechers  int    `json:"leechers"`
	Completed int    `json:"completed"`
	// Tracked says the store has the torrent: it has peers.
	Tracked bool `json:"tracked"`
}

// Stats returns the tracker's totals.
func (r *Role) Stats() (Stats, error) {
	s := r.cur.Load()
	if s == nil {
		return Stats{}, ErrNotRunning
	}
	out := Stats{
		Torrents: s.store.Torrents(), Peers: s.store.Peers(),
		Announces: r.announces.Load(), Scrapes: r.scrapes.Load(),
		Refused: make(map[string]uint64, numRefusals),
		Private: r.auth.Load() != nil,
	}
	for i, name := range refusalNames {
		out.Refused[name] = r.refused[i].Load()
	}
	if a := r.allow.Load(); a != nil {
		out.Allowed = len(a.list)
	}
	if ss, ok := s.store.(*sharedstore.Store); ok {
		st := ss.Status()
		out.Store = &st
	}
	// Not r.mu, which an Apply holds while it binds: a console's request
	// on a fib worker never waits for one.
	if v := r.view.Load(); v != nil {
		out.UDP, out.MaxTorrents, out.MaxPeers = v.udp, v.maxTorrents, v.maxPeers
	}
	return out, nil
}

// Torrent returns the swarm of the torrent h; Tracked is false when the
// store does not have it.
func (r *Role) Torrent(h metainfo.InfoHash) (TorrentStats, error) {
	s := r.cur.Load()
	if s == nil {
		return TorrentStats{}, ErrNotRunning
	}
	return statsOf(s, h), nil
}

func statsOf(s *server, h metainfo.InfoHash) TorrentStats {
	st, ok := s.store.Scrape(h)
	return TorrentStats{InfoHash: h.String(), Seeders: st.Complete, Leechers: st.Incomplete, Completed: st.Downloaded, Tracked: ok}
}

// MaxListed is the most torrents Torrents returns.
const MaxListed = 1000

// Where a list of torrents comes from: the allow-list, or the store.
const (
	SourceAllowList = "allow_list"
	SourceStore     = "store"
)

// TorrentList is a page of the tracker's torrents.
type TorrentList struct {
	Torrents []TorrentStats `json:"torrents"`
	// Total is how many there are: the allow-list's length, or the
	// torrents the store had peers of when it was looked through (past
	// after, when paged by it).
	Total int `json:"total"`
	// Source is SourceAllowList or SourceStore.
	Source string `json:"source"`
	// Next, for the store, is the info hash to page on from with after,
	// "" on the last page.
	Next string `json:"next,omitempty"`
}

// Torrents returns the swarms of the allow-list's torrents, from the
// offset'th, at most limit of them (MaxListed when limit is not positive,
// and at most MaxListed), and how many the list holds. A public tracker
// lists the torrents its store has peers of, ordered by info hash (as
// TorrentsAfter, from the offset'th).
func (r *Role) Torrents(offset, limit int) ([]TorrentStats, int, error) {
	l, err := r.ListTorrents(offset, limit, nil)
	return l.Torrents, l.Total, err
}

// ListTorrents is Torrents, with the source and, for the store, where the
// next page starts. after, when not nil, pages a public tracker's list by
// info hash instead of by offset: the torrents past it. A tracker with an
// allow-list refuses after (ErrAfterAllowList).
//
// A public tracker's list is read through fib's MemoryStore.Range, one
// shard locked at a time, keeping the offset+limit smallest info hashes
// past after: a call takes time in the torrents tracked (at most
// max_torrents) and memory in offset+limit only, so after is the way to
// page far into a large store. Each torrent's numbers are as of one moment.
// With a shared store, the list is of every instance's torrents, read from
// the shared storage (its own torrents while that cannot be read), waiting
// for it: call it off fib's workers.
func (r *Role) ListTorrents(offset, limit int, after *metainfo.InfoHash) (TorrentList, error) {
	s := r.cur.Load()
	if s == nil {
		return TorrentList{Torrents: []TorrentStats{}}, ErrNotRunning
	}
	if limit <= 0 || limit > MaxListed {
		limit = MaxListed
	}
	offset = max(offset, 0)
	if a := r.allow.Load(); a != nil {
		if after != nil {
			return TorrentList{Torrents: []TorrentStats{}, Source: SourceAllowList}, ErrAfterAllowList
		}
		offset = min(offset, len(a.list))
		end := min(offset+limit, len(a.list))
		out := make([]TorrentStats, 0, end-offset)
		for _, h := range a.list[offset:end] {
			out = append(out, statsOf(s, h))
		}
		return TorrentList{Torrents: out, Total: len(a.list), Source: SourceAllowList}, nil
	}
	keep := offset + limit
	// A max-heap of the keep smallest info hashes past after; one more
	// than a page says whether there is a next one.
	top := make(swarmHeap, 0, min(keep+1, 1024))
	total := 0
	s.store.Range(func(h metainfo.InfoHash, st fibtracker.Stats) bool {
		if after != nil && bytes.Compare(h[:], after[:]) <= 0 {
			return true
		}
		total++
		e := swarm{h: h, st: st}
		if len(top) <= keep {
			heap.Push(&top, e)
		} else if bytes.Compare(h[:], top[0].h[:]) < 0 {
			top[0] = e
			heap.Fix(&top, 0)
		}
		return true
	})
	slices.SortFunc(top, func(a, b swarm) int { return bytes.Compare(a.h[:], b.h[:]) })
	more := len(top) > keep
	if more {
		top = top[:keep]
	}
	out := make([]TorrentStats, 0, max(0, len(top)-offset))
	for _, e := range top[min(offset, len(top)):] {
		out = append(out, TorrentStats{InfoHash: e.h.String(), Seeders: e.st.Complete, Leechers: e.st.Incomplete,
			Completed: e.st.Downloaded, Tracked: true})
	}
	l := TorrentList{Torrents: out, Total: total, Source: SourceStore}
	if more && len(out) > 0 {
		l.Next = out[len(out)-1].InfoHash
	}
	return l, nil
}

// ErrAfterAllowList is ListTorrents with after on a tracker with an
// allow-list, which is paged by offset in the list's order.
var ErrAfterAllowList = errors.New("bittorrent.tracker: after pages a public tracker's torrents; a tracker with an allow-list is paged by offset")

type swarm struct {
	h  metainfo.InfoHash
	st fibtracker.Stats
}

// swarmHeap is a max-heap of swarms by info hash.
type swarmHeap []swarm

func (h swarmHeap) Len() int           { return len(h) }
func (h swarmHeap) Less(i, j int) bool { return bytes.Compare(h[i].h[:], h[j].h[:]) > 0 }
func (h swarmHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *swarmHeap) Push(x any)        { *h = append(*h, x.(swarm)) }
func (h *swarmHeap) Pop() any {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}

// Passkey returns the passkey of the user name of the tracker's media_auth
// policy, for the announce URL /<passkey>/announce.
func (r *Role) Passkey(name string) (string, error) {
	g := r.auth.Load()
	if g == nil {
		return "", errors.New("bittorrent.tracker: the tracker is public: it takes no passkeys")
	}
	p := g.policy()
	if p == nil {
		return "", errNoPolicy
	}
	return p.Passkey(name)
}

// routes adds the role's read-only API to its console: the pages of I204
// build on it.
func (r *Role) routes(api *console.API) {
	api.Handle("GET", "/api/stats", func(q *console.Request) {
		st, err := r.Stats()
		if err != nil {
			_ = q.JSON(http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
			return
		}
		_ = q.JSON(http.StatusOK, st)
	})
	api.Handle("GET", "/api/torrents", func(q *console.Request) {
		v := q.Request.URL.Query()
		offset, _ := strconv.Atoi(v.Get("offset"))
		limit, _ := strconv.Atoi(v.Get("limit"))
		var after *metainfo.InfoHash
		if a := v.Get("after"); a != "" {
			h, ok := config.ParseInfoHash(a)
			if !ok {
				_ = q.JSON(http.StatusBadRequest, map[string]string{"error": "after: not an info hash"})
				return
			}
			ih := metainfo.InfoHash(h)
			after = &ih
		}
		// Off the worker: a public tracker's list looks through the store.
		q.Go(func() {
			l, err := r.ListTorrents(offset, limit, after)
			switch {
			case errors.Is(err, ErrAfterAllowList):
				_ = q.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
			case err != nil:
				_ = q.JSON(http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
			default:
				_ = q.JSON(http.StatusOK, l)
			}
		})
	})
	api.HandlePrefix("GET", "/api/torrents/", func(q *console.Request) {
		// /api/torrents/<info hash> on the role's console, and
		// /api/m/bittorrent.tracker/torrents/<info hash> as a module.
		p := q.Request.URL.Path
		h, ok := config.ParseInfoHash(p[strings.LastIndex(p, "/torrents/")+len("/torrents/"):])
		if !ok {
			_ = q.JSON(http.StatusBadRequest, map[string]string{"error": "not an info hash: 40 hex digits (v1) or 64 (v2)"})
			return
		}
		st, err := r.Torrent(h)
		if err != nil {
			_ = q.JSON(http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
			return
		}
		_ = q.JSON(http.StatusOK, st)
	})
	api.Handle("GET", "/api/passkey", func(q *console.Request) {
		user := q.Request.URL.Query().Get("user")
		// Off the worker: a policy not found yet is looked up under
		// mediaauth's lock, which its Apply holds while it reads files.
		q.Go(func() {
			k, err := r.Passkey(user)
			if err != nil {
				_ = q.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
				return
			}
			_ = q.JSON(http.StatusOK, map[string]string{"passkey": k})
		})
	})
}
