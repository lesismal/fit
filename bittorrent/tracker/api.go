package tracker

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/lesismal/fib/bittorrent/metainfo"

	"github.com/lesismal/fit/app/console"
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

// Torrents returns the swarms of the allow-list's torrents, from the
// offset'th, at most limit of them (MaxListed when limit is not positive,
// and at most MaxListed), and how many the list holds. A tracker without an
// allow-list lists none: fib's MemoryStore cannot list the torrents it
// keeps (see docs/fib-api-requests.md), only answer for a torrent named.
func (r *Role) Torrents(offset, limit int) ([]TorrentStats, int, error) {
	s := r.cur.Load()
	if s == nil {
		return nil, 0, ErrNotRunning
	}
	a := r.allow.Load()
	if a == nil {
		return []TorrentStats{}, 0, nil
	}
	if limit <= 0 || limit > MaxListed {
		limit = MaxListed
	}
	offset = min(max(offset, 0), len(a.list))
	end := min(offset+limit, len(a.list))
	out := make([]TorrentStats, 0, end-offset)
	for _, h := range a.list[offset:end] {
		out = append(out, statsOf(s, h))
	}
	return out, len(a.list), nil
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
		q.Go(func() {
			list, total, err := r.Torrents(offset, limit)
			if err != nil {
				_ = q.JSON(http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
				return
			}
			_ = q.JSON(http.StatusOK, map[string]any{"torrents": list, "total": total})
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
