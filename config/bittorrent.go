package config

import (
	"path/filepath"
	"strings"
	"time"
)

// The defaults and bounds of BitTorrentClient. The connection counts and
// upload slots are fib's client's defaults.
const (
	DefaultBTMaxConns           = 200
	DefaultBTMaxConnsPerTorrent = 50
	DefaultBTMaxHalfOpen        = 32
	DefaultBTUploadSlots        = 4
	DefaultBTMaxTorrents        = 1000
	// MaxBTTorrents is the most max_torrents may be, and the most torrents
	// a state file may list.
	MaxBTTorrents = 100000
	// MaxBTDiskWorkers is the most disk_workers may be.
	MaxBTDiskWorkers = 64
	// DefaultBTDiskQueueBytes is fib's client's bound on the received
	// blocks waiting for the disk.
	DefaultBTDiskQueueBytes = 64 << 20

	DefaultBTSaveInterval = 5 * time.Second
)

// The encryption policies of the BitTorrent client, MSE/PE.
const (
	BTEncryptionAllow   = "allow"
	BTEncryptionPrefer  = "prefer"
	BTEncryptionRequire = "require"
)

// BitTorrent is fit's BitTorrent roles: a client that downloads and seeds.
// See docs/decisions/037-bittorrent.md and docs/bittorrent.md.
type BitTorrent struct {
	Client *BitTorrentClient `json:"client,omitempty" doc:"Run a BitTorrent client, on fit's engine: one port for TCP and UDP (uTP, the DHT and UDP trackers), downloading and seeding torrents kept in data_dir across restarts. None when left out."`

	At Value `json:"-" fit:"self"`
}

// BitTorrentClient is the bittorrent.client role: fib's BitTorrent client on
// the listeners' engine, its torrents kept in files under data_dir.
type BitTorrentClient struct {
	Listen         string            `json:"listen" fit:"required" doc:"The host and port peers reach the client at, the same port for TCP and UDP (uTP, the DHT and UDP trackers share it), as in \":6881\". Port 0 picks one free for both. A change makes a new client: the torrents are saved, the client closed, and the torrents added to the new one, resuming without a check."`
	DataDir        string            `json:"data_dir" fit:"required" doc:"The directory the client's state is kept in: torrents.json, the list of torrents with their sources, directories, file priorities and whether they are paused; torrents/, their .torrent files; and resume/, their resume data, so that a restart resumes without hashing what is on disk again. Each file is written to a temporary file, synced and renamed into place. Made when missing. A state file that does not read, or is larger than its bound, keeps the role from starting, rather than being overwritten. A change makes a new client, which starts from the state in the new directory, as a restart would; the old directory is left as it was saved."`
	DownloadDir    string            `json:"download_dir,omitempty" doc:"The directory torrents added from now on download into, each torrent's files below it, their names made safe for the system; no path of a torrent leads out of it. A torrent keeps the directory it was added with when this changes. <data_dir>/downloads when left out."`
	Encryption     string            `json:"encryption,omitempty" enum:"allow,prefer,require" default:"\"prefer\"" doc:"How peer connections use Message Stream Encryption: prefer dials with it and again in plaintext when a peer refuses, accepting both; allow dials in plaintext first, accepting both; require dials and accepts only encrypted connections. prefer when left out. A change makes a new client."`
	DHT            *bool             `json:"dht,omitempty" default:"true" doc:"Find peers through the mainline DHT (BEP 5), bootstrapping from the public routers. On when left out. A change makes a new client."`
	PEX            *bool             `json:"pex,omitempty" default:"true" doc:"Exchange peers with connected peers (BEP 11). On when left out. A change makes a new client."`
	UTP            *bool             `json:"utp,omitempty" default:"true" doc:"Speak uTP (BEP 29) beside TCP: peers are dialed over uTP first, then TCP. On when left out. A change makes a new client."`
	LSD            bool              `json:"lsd,omitempty" doc:"Find peers on the local network by local service discovery (BEP 14, IPv4 multicast). Off by default. A change makes a new client."`
	DiskWorkers    int               `json:"disk_workers,omitempty" min:"0" max:"64" doc:"How many goroutines read and write the torrents' files and hash their pieces, which never happens on a fib worker. The number of CPUs, at most 8, when left out or 0. A change makes a new client."`
	DiskQueueBytes int64             `json:"disk_queue_bytes,omitempty" min:"0" default:"67108864" doc:"The most bytes of received blocks waiting to be written: past it the client stops reading from the peers that sent them until half has been written, so that a slow disk slows the peers rather than growing memory. 64 MiB when left out or 0. A change makes a new client."`
	SaveInterval   Duration          `json:"save_interval,omitempty" default:"\"5s\"" doc:"How often, at most, the state is saved when something changed: a piece verified, a torrent added, removed, paused or its metadata received. It is saved when the role stops too, and changes to the list of torrents are saved at once. 5s when left out."`
	Limits         *BitTorrentLimits `json:"limits,omitempty" doc:"The client's rates and connection, seeding and torrent limits. A change applies in place, keeping the peers connected, but those over a lowered connection limit. The defaults when left out."`
	Access         *Access           `json:"access,omitempty" doc:"The peer addresses the client deals with, deciding before the global access: a peer it refuses is closed as soon as it connects over TCP, has its uTP connection ignored, is never dialed, and as a DHT node gets no answers. Trackers and web seeds are URLs, not peers, and are not asked about. A change applies at once, closing the connected peers it now refuses. Every address when left out, unless the global access says otherwise."`
	Console        *RoleConsole      `json:"console,omitempty" doc:"The client's web pages; none when left out."`

	// Publish is I202's, in bittorrent_publish.go.
	Publish []BitTorrentPublish `json:"publish,omitempty" doc:"Directories the client publishes: each built into hybrid torrents, a torrent of each file or one of the whole directory, on goroutines of its own, and seeded where the files are, the .torrent files and magnet links served by the bt_publish handler. Each follows the disk: a file added gets a torrent, and one changed or removed stops its torrent, a changed one getting a new torrent. The torrents are kept in data_dir/publish/<name>/, so that a restart neither builds nor checks a file that did not change. None when left out."`

	At Value `json:"-" fit:"self"`
}

// BitTorrentLimits are the BitTorrent client's limits, which change while it
// runs.
type BitTorrentLimits struct {
	UploadRate         int      `json:"upload_rate,omitempty" min:"0" doc:"The most bytes per second uploaded, payload of all torrents together. Unlimited when left out or 0."`
	DownloadRate       int      `json:"download_rate,omitempty" min:"0" doc:"The most bytes per second downloaded, payload of all torrents together. Unlimited when left out or 0."`
	MaxConns           int      `json:"max_conns,omitempty" min:"0" default:"200" doc:"The most peer connections of the client at once. 200 when left out or 0."`
	MaxConnsPerTorrent int      `json:"max_conns_per_torrent,omitempty" min:"0" default:"50" doc:"The most peer connections of one torrent at once. 50 when left out or 0."`
	MaxHalfOpen        int      `json:"max_half_open,omitempty" min:"0" default:"32" doc:"The most dials to peers in flight at once. 32 when left out or 0."`
	UploadSlots        int      `json:"upload_slots,omitempty" min:"0" default:"4" doc:"How many peers a torrent uploads to at once, beside one optimistic unchoke. 4 when left out or 0."`
	SeedRatio          float64  `json:"seed_ratio,omitempty" min:"0" doc:"Pause a torrent once it has uploaded this many times its size since it was added or fit started. Seeds for ever when left out or 0."`
	SeedTime           Duration `json:"seed_time,omitempty" doc:"Pause a torrent once it has seeded this long since it was added or fit started. Seeds for ever when left out."`
	MaxTorrents        int      `json:"max_torrents,omitempty" min:"0" max:"100000" default:"1000" doc:"The most torrents the client holds, running or paused; one more is refused. Lowering it removes none. 1000 when left out or 0."`

	At Value `json:"-" fit:"self"`
}

// EncryptionMode returns encryption, or its default.
func (b *BitTorrentClient) EncryptionMode() string {
	if b.Encryption == "" {
		return BTEncryptionPrefer
	}
	return b.Encryption
}

// DHTOn reports whether the DHT is on, as it is when left out.
func (b *BitTorrentClient) DHTOn() bool { return b.DHT == nil || *b.DHT }

// PEXOn reports whether peer exchange is on, as it is when left out.
func (b *BitTorrentClient) PEXOn() bool { return b.PEX == nil || *b.PEX }

// UTPOn reports whether uTP is on, as it is when left out.
func (b *BitTorrentClient) UTPOn() bool { return b.UTP == nil || *b.UTP }

// Downloads returns download_dir, or <data_dir>/downloads.
func (b *BitTorrentClient) Downloads() string {
	if strings.TrimSpace(b.DownloadDir) != "" {
		return b.DownloadDir
	}
	return filepath.Join(b.DataDir, "downloads")
}

// DiskQueue returns disk_queue_bytes, or its default.
func (b *BitTorrentClient) DiskQueue() int64 {
	return orDefault(b.DiskQueueBytes, DefaultBTDiskQueueBytes)
}

// Save returns save_interval, or its default.
func (b *BitTorrentClient) Save() time.Duration {
	return durationOr(b.SaveInterval, DefaultBTSaveInterval)
}

// Torrents returns limits.max_torrents, or its default.
func (b *BitTorrentClient) Torrents() int {
	if b.Limits == nil {
		return DefaultBTMaxTorrents
	}
	return int(orDefault(int64(b.Limits.MaxTorrents), DefaultBTMaxTorrents))
}

func (c *Config) checkBitTorrent(add func(Value, string, ...any)) {
	if c.BitTorrent == nil || c.BitTorrent.Client == nil {
		return
	}
	b := c.BitTorrent.Client
	if _, msg := checkAddress(b.Listen); msg != "" {
		add(b.At.Field("listen"), "%s", msg)
	}
	if strings.TrimSpace(b.DataDir) == "" {
		add(b.At.Field("data_dir"), "data_dir may not be empty")
	}
	if b.SaveInterval.Std() < 0 {
		add(b.At.Field("save_interval"), "save_interval may not be negative")
	}
	if l := b.Limits; l != nil && l.SeedTime.Std() < 0 {
		add(l.At.Field("seed_time"), "seed_time may not be negative")
	}
	b.Access.check(add)
	checkRoleConsole(b.Console, add)
	checkBitTorrentPublish(b, add)
}
