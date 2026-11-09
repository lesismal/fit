package config

import (
	"encoding/hex"
	"strings"
	"time"
)

// The defaults and bounds of BitTorrentTracker. The interval, peer TTL,
// numwant and scrape bounds are fib's tracker's defaults; the store's
// bounds are fit's, well below fib's (a million torrents, two million
// peers), so that a tracker left at its defaults stays within some 80 MiB.
const (
	DefaultBTTrackerInterval           = 30 * time.Minute
	DefaultBTTrackerPeerTTL            = time.Hour
	DefaultBTTrackerMaxTorrents        = 100000
	DefaultBTTrackerMaxPeersPerTorrent = 10000
	DefaultBTTrackerMaxPeers           = 500000
	DefaultBTTrackerMaxNumWant         = 200
	DefaultBTTrackerMaxScrape          = 64

	// MaxBTTrackerTorrents, MaxBTTrackerPeersPerTorrent and MaxBTTrackerPeers
	// are the most the store's bounds may be: fib's defaults.
	MaxBTTrackerTorrents        = 1 << 20
	MaxBTTrackerPeersPerTorrent = 1 << 16
	MaxBTTrackerPeers           = 1 << 22
	// MaxBTTrackerNumWant and MaxBTTrackerScrape bound max_numwant and
	// max_scrape.
	MaxBTTrackerNumWant = 1000
	MaxBTTrackerScrape  = 1000
	// MaxBTTrackerAllow is the most info hashes the allow-list may hold,
	// inline and from its files together.
	MaxBTTrackerAllow = 1 << 20
)

// BitTorrentTracker is the bittorrent.tracker role: fib's BitTorrent tracker,
// over HTTP through the routes' bt_tracker handler and over UDP on its own
// listen, on one store in memory.
type BitTorrentTracker struct {
	Listen             string                  `json:"listen,omitempty" doc:"The host and port the tracker answers UDP announces and scrapes on (BEP 15), as \":6969\". Announces over HTTP and HTTPS come through a route whose handler is bt_tracker, on fit's listeners. No UDP when left out. A change moves the UDP port, keeping the swarms."`
	Interval           Duration                `json:"interval,omitempty" default:"\"30m\"" doc:"How often clients are told to announce. 30m when left out. A change applies at once, keeping the swarms."`
	MinInterval        Duration                `json:"min_interval,omitempty" doc:"The \"min interval\" of HTTP answers: how soon a client may announce again of its own accord. None when left out. At most interval. A change applies at once, keeping the swarms."`
	PeerTTL            Duration                `json:"peer_ttl,omitempty" default:"\"1h\"" doc:"How long a peer is kept after its last announce, longer than interval so that a peer announcing a little late is not forgotten; a torrent left without peers is forgotten, its completed count with it. 1h when left out. A change applies in place, keeping the swarms; a shorter one expires the peers kept past it."`
	MaxTorrents        int                     `json:"max_torrents,omitempty" min:"0" max:"1048576" default:"100000" doc:"The most torrents tracked at once; an announce of one more is refused with \"tracker is full\". 100000 when left out or 0. A change applies in place, keeping the swarms; lowered, it drops none and refuses new torrents until the store is back under it."`
	MaxPeersPerTorrent int                     `json:"max_peers_per_torrent,omitempty" min:"0" max:"65536" default:"10000" doc:"The most peers kept of one torrent; a peer past it still gets peers but is not handed out. 10000 when left out or 0. A change applies in place, keeping the swarms; lowered, it drops no peer."`
	MaxPeers           int                     `json:"max_peers,omitempty" min:"0" max:"4194304" default:"500000" doc:"The most peers kept of all torrents together, each about 150 bytes; a peer past it still gets peers but is not handed out. 500000 when left out or 0. A change applies in place, keeping the swarms; lowered, it drops no peer."`
	MaxNumWant         int                     `json:"max_numwant,omitempty" min:"0" max:"1000" default:"200" doc:"The most peers an announce gets, whatever its numwant asks; 50 when it asks for none. A UDP answer is also kept within 1400 bytes. 200 when left out or 0."`
	MaxScrape          int                     `json:"max_scrape,omitempty" min:"0" max:"1000" default:"64" doc:"The most torrents one HTTP scrape may name; a UDP scrape is bounded to 74 by its packet. A scrape naming none, a full scrape, gets nothing. 64 when left out or 0."`
	Auth               string                  `json:"auth,omitempty" doc:"The media_auth policy whose passkeys the tracker takes, which makes it private: an announce or scrape must carry a passkey, in its path as /<passkey>/announce or as ?passkey=, that is one of the policy's tokens (static, or signed by fit) or a user's passkey, derived from the policy's secret and the user's name, and whose grant plays bittorrent/<info hash in hex> (play [\"bittorrent/*\"] for every torrent). Others are refused with a failure reason. Public when left out: anyone may announce."`
	Allow              []string                `json:"allow,omitempty" doc:"The info hashes the tracker tracks, in hex: 40 digits for a v1 hash, or 64 for a v2 one, tracked by its first 20 bytes as v2 clients announce it. With any here or in allow_files, announces of other torrents are refused with \"unregistered torrent\", and scrapes of them get nothing. Every torrent when both are left out."`
	AllowFiles         []string                `json:"allow_files,omitempty" doc:"Files of more allowed info hashes: one on each line, as allow has them, with blank lines and what follows # left out. Read when the config is applied, and read again when one changes; each at most 64 MiB, and at most 1048576 info hashes in all."`
	Access             *Access                 `json:"access,omitempty" doc:"The client addresses the tracker serves, deciding before the global access: a UDP datagram from an address it refuses is dropped before a connection is made, and an HTTP announce or scrape through bt_tracker from one is answered with a failure reason. A change applies at once. Every address when left out, unless the global access says otherwise."`
	Console            *RoleConsole            `json:"console,omitempty" doc:"The tracker's web pages: its statistics, read-only. None when left out."`
	Store              *BitTorrentTrackerStore `json:"store,omitempty" doc:"A store of the swarms that the trackers of several fit instances share, so that they answer as one tracker: each keeps its own peers in memory, as without it, and shares them through the store, answering an announce with peers of every instance and a scrape with the counts of all. Each instance's swarms alone, in its memory, when left out. A change of it, or adding or removing it, makes a new store: this instance's swarms start anew in memory (clients announce again within their interval), the shared ones staying in the store. The bounds max_torrents, max_peers_per_torrent and max_peers apply to the shared store too, to all instances' peers together."`

	At Value `json:"-" fit:"self"`
}

// The defaults and bounds of BitTorrentTrackerStore.
const (
	DefaultBTTrackerStorePrefix        = "fit/bittorrent/tracker/"
	DefaultBTTrackerStoreTimeout       = 2 * time.Second
	DefaultBTTrackerStoreRefresh       = 5 * time.Second
	DefaultBTTrackerStoreQueue         = 65536
	DefaultBTTrackerStoreCacheTorrents = 4096
	DefaultBTTrackerStoreCachePeers    = 100
	MaxBTTrackerStoreQueue             = 1 << 20
	MaxBTTrackerStoreCacheTorrents     = 1 << 20
	MaxBTTrackerStoreCachePeers        = 1000
)

// BitTorrentTrackerStore is the store of swarms that trackers on several
// instances of fit share: Redis, the one kind for now.
type BitTorrentTrackerStore struct {
	Redis          *RedisStorage `json:"redis,omitempty" doc:"Share the swarms through Redis 6.2 or later (or Valkey), a standalone server or a primary (not Redis Cluster), with Lua scripts, each announce one script run, on Redis's clock. Its maxmemory-policy must not evict keys (noeviction): the store bounds them instead."`
	Prefix         string        `json:"prefix,omitempty" default:"\"fit/bittorrent/tracker/\"" doc:"What the store's keys start with, as fit/bittorrent/tracker/t/<info hash>/s; the trackers that answer as one share it. fit/bittorrent/tracker/ when left out."`
	RequestTimeout Duration      `json:"request_timeout,omitempty" default:"\"2s\"" doc:"How long each round trip to the store may take, connecting included; past it the store counts as unreachable, and is tried again after a back-off. 2s when left out."`
	Refresh        Duration      `json:"refresh,omitempty" default:"\"5s\"" doc:"How often the peers of the torrents this instance serves are read from the store again, and so how late an instance learns of another's peers. Read peers are used for three times this, then only the instance's own. 5s when left out; at least 100ms, and shorter than interval."`
	Queue          int           `json:"queue,omitempty" min:"0" max:"1048576" default:"65536" doc:"The most announces waiting to be written to the store, one per peer and torrent, a later announce of a peer taking its earlier one's place; past it announces are still answered but not shared, and counted as dropped. 65536 when left out or 0."`
	CacheTorrents  int           `json:"cache_torrents,omitempty" min:"0" max:"1048576" default:"4096" doc:"The most torrents whose shared peers this instance keeps a sample of; a torrent past it is answered with this instance's own peers. A torrent leaves when nobody has announced or scraped it here for peer_ttl; scrapes alone may fill three quarters. 4096 when left out or 0."`
	CachePeers     int           `json:"cache_peers,omitempty" min:"0" max:"1000" default:"100" doc:"How many seeders and how many leechers of a torrent's shared peers, picked at random, each sample keeps, about 60 bytes each; announces get peers from it. 100 when left out or 0."`

	At Value `json:"-" fit:"self"`
}

// KeyPrefix returns prefix, or its default.
func (s *BitTorrentTrackerStore) KeyPrefix() string {
	if s.Prefix == "" {
		return DefaultBTTrackerStorePrefix
	}
	return s.Prefix
}

// Timeout returns request_timeout, or its default.
func (s *BitTorrentTrackerStore) Timeout() time.Duration {
	return durationOr(s.RequestTimeout, DefaultBTTrackerStoreTimeout)
}

// RefreshOr returns refresh, or its default.
func (s *BitTorrentTrackerStore) RefreshOr() time.Duration {
	return durationOr(s.Refresh, DefaultBTTrackerStoreRefresh)
}

// QueueOr returns queue, or its default.
func (s *BitTorrentTrackerStore) QueueOr() int {
	return int(orDefault(int64(s.Queue), DefaultBTTrackerStoreQueue))
}

// Torrents returns cache_torrents, or its default.
func (s *BitTorrentTrackerStore) Torrents() int {
	return int(orDefault(int64(s.CacheTorrents), DefaultBTTrackerStoreCacheTorrents))
}

// Peers returns cache_peers, or its default.
func (s *BitTorrentTrackerStore) Peers() int {
	return int(orDefault(int64(s.CachePeers), DefaultBTTrackerStoreCachePeers))
}

// IntervalOr returns interval, or its default.
func (t *BitTorrentTracker) IntervalOr() time.Duration {
	return durationOr(t.Interval, DefaultBTTrackerInterval)
}

// TTL returns peer_ttl, or its default.
func (t *BitTorrentTracker) TTL() time.Duration {
	return durationOr(t.PeerTTL, DefaultBTTrackerPeerTTL)
}

// Torrents returns max_torrents, or its default.
func (t *BitTorrentTracker) Torrents() int {
	return int(orDefault(int64(t.MaxTorrents), DefaultBTTrackerMaxTorrents))
}

// PeersPerTorrent returns max_peers_per_torrent, or its default.
func (t *BitTorrentTracker) PeersPerTorrent() int {
	return int(orDefault(int64(t.MaxPeersPerTorrent), DefaultBTTrackerMaxPeersPerTorrent))
}

// Peers returns max_peers, or its default.
func (t *BitTorrentTracker) Peers() int {
	return int(orDefault(int64(t.MaxPeers), DefaultBTTrackerMaxPeers))
}

// NumWant returns max_numwant, or its default.
func (t *BitTorrentTracker) NumWant() int {
	return int(orDefault(int64(t.MaxNumWant), DefaultBTTrackerMaxNumWant))
}

// Scrape returns max_scrape, or its default.
func (t *BitTorrentTracker) Scrape() int {
	return int(orDefault(int64(t.MaxScrape), DefaultBTTrackerMaxScrape))
}

// Files returns allow_files and the access's list files, for the role to
// have them watched (app.Info.CertFiles).
func (t *BitTorrentTracker) Files() []string {
	if t == nil {
		return nil
	}
	return append(append([]string(nil), t.AllowFiles...), t.Access.Files()...)
}

// ParseInfoHash reads an info hash of a tracker's allow-list: 40 hex digits,
// a v1 hash, or 64, a v2 hash, cut to its first 20 bytes as trackers key it
// (BEP 52). Spaces around it are ignored.
func ParseInfoHash(s string) ([20]byte, bool) {
	var h [20]byte
	s = strings.TrimSpace(s)
	if len(s) != 40 && len(s) != 64 {
		return h, false
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return h, false
	}
	copy(h[:], b)
	return h, true
}

func (c *Config) checkBitTorrentTracker(add func(Value, string, ...any)) {
	if c.BitTorrent == nil || c.BitTorrent.Tracker == nil {
		return
	}
	t := c.BitTorrent.Tracker
	if t.Listen != "" {
		if _, msg := checkAddress(t.Listen); msg != "" {
			add(t.At.Field("listen"), "%s", msg)
		}
	}
	// Negative durations are refused as they are decoded.
	if t.MinInterval.Std() > t.IntervalOr() {
		add(t.At.Field("min_interval"), "min_interval %s is longer than the interval, %s", t.MinInterval.Std(), t.IntervalOr())
	}
	if t.TTL() <= t.IntervalOr() {
		add(t.At.Field("peer_ttl"), "peer_ttl %s is not longer than the interval, %s: every peer would be forgotten before it announces again", t.TTL(), t.IntervalOr())
	}
	if t.Auth != "" && c.MediaAuthPolicy(t.Auth) == nil {
		add(t.At.Field("auth"), "there is no media_auth policy named %q", t.Auth)
	}
	if len(t.Allow) > MaxBTTrackerAllow {
		add(t.At.Field("allow"), "%d info hashes: at most %d", len(t.Allow), MaxBTTrackerAllow)
	}
	for i, s := range t.Allow {
		if _, ok := ParseInfoHash(s); !ok {
			add(t.At.Field("allow").Index(i), "%q is not an info hash: 40 hex digits (v1) or 64 (v2)", s)
		}
	}
	for i, f := range t.AllowFiles {
		if strings.TrimSpace(f) == "" {
			add(t.At.Field("allow_files").Index(i), "a file name may not be empty")
		}
	}
	t.Access.check(add)
	checkRoleConsole(t.Console, add)
	if st := t.Store; st != nil {
		if st.Redis == nil {
			add(st.At, "set redis: the one kind of shared tracker store")
		} else {
			checkRedisStorage(st.Redis, add)
		}
		if r := st.RefreshOr(); r < 100*time.Millisecond {
			add(st.At.Field("refresh"), "%s is too short: at least 100ms", r)
		} else if r >= t.IntervalOr() {
			add(st.At.Field("refresh"), "refresh %s is not shorter than the interval, %s", r, t.IntervalOr())
		}
		if d := st.Timeout(); d > time.Minute {
			add(st.At.Field("request_timeout"), "%s is too long: at most 1m", d)
		}
	}
}
