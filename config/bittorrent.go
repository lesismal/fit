package config

import (
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
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

	// DefaultBTWatchInterval is how often watch_dir is looked at.
	DefaultBTWatchInterval = 10 * time.Second
	// DefaultBTWatchMaxFiles and MaxBTWatchMaxFiles bound the files of
	// watch_dir taken in one look.
	DefaultBTWatchMaxFiles = 100
	MaxBTWatchMaxFiles     = 10000
	// DefaultBTURLTimeout bounds the download of a .torrent file by URL.
	DefaultBTURLTimeout = 30 * time.Second
	// DefaultBTMinFreeBytes is the disk space a download leaves free, as a
	// media library's uploads do.
	DefaultBTMinFreeBytes = 1 << 30
	// MaxBTCategories bounds categories.
	MaxBTCategories = 1000
	// MaxBTSchedules bounds alt_speed's schedules.
	MaxBTSchedules = 100
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
	// Tracker is the bittorrent.tracker role, in bittorrent_tracker.go.
	Tracker *BitTorrentTracker `json:"tracker,omitempty" doc:"Run a BitTorrent tracker, on fit's engine: HTTP announces and scrapes through routes whose handler is bt_tracker, UDP ones on its own listen, public or private (passkeys of a media_auth policy), with an allow-list of info hashes. Its swarms are kept in memory, bounded. None when left out."`

	At Value `json:"-" fit:"self"`
}

// BitTorrentClient is the bittorrent.client role: fib's BitTorrent client on
// the listeners' engine, its torrents kept in files under data_dir.
type BitTorrentClient struct {
	Listen         string               `json:"listen" fit:"required" doc:"The host and port peers reach the client at, the same port for TCP and UDP (uTP, the DHT and UDP trackers share it), as in \":6881\". Port 0 picks one free for both. A change makes a new client: the torrents are saved, the client closed, and the torrents added to the new one, resuming without a check."`
	DataDir        string               `json:"data_dir" fit:"required" doc:"The directory the client's state is kept in: torrents.json, the list of torrents with their sources, directories, file priorities and whether they are paused; torrents/, their .torrent files; and resume/, their resume data, so that a restart resumes without hashing what is on disk again. Each file is written to a temporary file, synced and renamed into place. Made when missing. A state file that does not read, or is larger than its bound, keeps the role from starting, rather than being overwritten. A change makes a new client, which starts from the state in the new directory, as a restart would; the old directory is left as it was saved."`
	DownloadDir    string               `json:"download_dir,omitempty" doc:"The directory torrents added from now on download into, each torrent's files below it, their names made safe for the system; no path of a torrent leads out of it. A torrent keeps the directory it was added with when this changes. <data_dir>/downloads when left out."`
	Encryption     string               `json:"encryption,omitempty" enum:"allow,prefer,require" default:"\"prefer\"" doc:"How peer connections use Message Stream Encryption: prefer dials with it and again in plaintext when a peer refuses, accepting both; allow dials in plaintext first, accepting both; require dials and accepts only encrypted connections. prefer when left out. A change makes a new client."`
	DHT            *bool                `json:"dht,omitempty" default:"true" doc:"Find peers through the mainline DHT (BEP 5), bootstrapping from the public routers. On when left out. A change makes a new client."`
	PEX            *bool                `json:"pex,omitempty" default:"true" doc:"Exchange peers with connected peers (BEP 11). On when left out. A change makes a new client."`
	UTP            *bool                `json:"utp,omitempty" default:"true" doc:"Speak uTP (BEP 29) beside TCP: peers are dialed over uTP first, then TCP. On when left out. A change makes a new client."`
	LSD            bool                 `json:"lsd,omitempty" doc:"Find peers on the local network by local service discovery (BEP 14, IPv4 multicast). Off by default. A change makes a new client."`
	DiskWorkers    int                  `json:"disk_workers,omitempty" min:"0" max:"64" doc:"How many goroutines read and write the torrents' files and hash their pieces, which never happens on a fib worker. The number of CPUs, at most 8, when left out or 0. A change makes a new client."`
	DiskQueueBytes int64                `json:"disk_queue_bytes,omitempty" min:"0" default:"67108864" doc:"The most bytes of received blocks waiting to be written: past it the client stops reading from the peers that sent them until half has been written, so that a slow disk slows the peers rather than growing memory. 64 MiB when left out or 0. A change makes a new client."`
	SaveInterval   Duration             `json:"save_interval,omitempty" default:"\"5s\"" doc:"How often, at most, the state is saved when something changed: a piece verified, a torrent added, removed, paused or its metadata received. It is saved when the role stops too, and changes to the list of torrents are saved at once. 5s when left out."`
	Limits         *BitTorrentLimits    `json:"limits,omitempty" doc:"The client's rates and connection, seeding, queueing, disk and torrent limits. A change applies in place, keeping the peers connected, but those over a lowered connection limit. The defaults when left out."`
	Categories     []BitTorrentCategory `json:"categories,omitempty" doc:"The categories a torrent may be added in: each downloads into a directory of its own, gives its torrents its own limits, and may name a media_library that is scanned again when a torrent of it completes. A torrent added with no category downloads into download_dir. A change applies in place: limits to the torrents of the category at once, a new directory to the torrents added from then on. A torrent whose category is removed keeps its directory and loses the category's limits. At most 1000."`
	WatchDir       string               `json:"watch_dir,omitempty" doc:"A directory looked at every watch_interval for .torrent files and .magnet files (a magnet link on the first line that is not blank), each added once it has kept its size and modification time across two looks, so that one being written is not taken half way: those directly in it with no category, those in a subdirectory named after a category in that category. A file added, or whose torrent is already held, is moved into its .done subdirectory; one that cannot be added, being too large, not a torrent or not a link, into .failed, beside a .error file saying why; one that may be added later (max_torrents reached, the state not written) is left to be tried again. None when left out."`
	WatchInterval  Duration             `json:"watch_interval,omitempty" default:"\"10s\"" doc:"How often watch_dir is looked at. 10s when left out."`
	WatchMaxFiles  int                  `json:"watch_max_files,omitempty" min:"0" max:"10000" default:"100" doc:"The most files of watch_dir and its category subdirectories taken in one look, the rest left for the next. 100 when left out or 0."`
	URLTimeout     Duration             `json:"url_timeout,omitempty" default:"\"30s\"" doc:"How long the download of a .torrent file by its URL may take in all, connecting, redirects (at most 5) and the body (at most 32 MiB) included; at most 4 run at once. 30s when left out."`
	AltSpeed       *BitTorrentAltSpeed  `json:"alt_speed,omitempty" doc:"Alternative rates, in place of limits' upload_rate and download_rate while one of their schedules holds, or while the API turns them on. None when left out."`
	Access         *Access              `json:"access,omitempty" doc:"The peer addresses the client deals with, deciding before the global access: a peer it refuses is closed as soon as it connects over TCP, has its uTP connection ignored, is never dialed, and as a DHT node gets no answers. Trackers and web seeds are URLs, not peers, and are not asked about. A change applies at once, closing the connected peers it now refuses. Every address when left out, unless the global access says otherwise."`
	Console        *RoleConsole         `json:"console,omitempty" doc:"The client's web pages; none when left out."`

	// RSS is I208's, in bittorrent_rss.go.
	RSS *BitTorrentRSS `json:"rss,omitempty" doc:"RSS auto-download: feeds polled on their intervals, and rules adding the torrents of the items they pick (magnet links, .torrent enclosures or links) in a category, with size bounds and each episode once. What was seen is kept in data_dir/rss.json. A change applies in place. None when left out."`

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
	MaxActiveDownloads int      `json:"max_active_downloads,omitempty" min:"0" doc:"The most torrents downloading at once: past it, torrents queue, paused by the client, in the order of the list (which the API's queue position changes), and the first of them starts when one finishes, is paused or removed. A torrent fetching a magnet link's metadata counts as downloading. No queue when left out or 0."`
	MaxActiveSeeds     int      `json:"max_active_seeds,omitempty" min:"0" doc:"The most torrents seeding at once, queued as max_active_downloads queues downloads. No queue when left out or 0."`
	MinFreeBytes       int64    `json:"min_free_bytes,omitempty" min:"0" default:"1073741824" doc:"The disk space a download leaves free: a torrent starts downloading only when its directory's disk has what it still has to download and this much more free, and one downloading pauses, with an error saying so, when less than this is left; it starts again by itself once the space is there. Where the free space cannot be told, it is not checked. 1 GiB when left out or 0."`

	At Value `json:"-" fit:"self"`
}

// BitTorrentCategory is a category of torrents.
type BitTorrentCategory struct {
	Name         string                   `json:"name" fit:"required" doc:"The category's name, which the API and watch_dir's subdirectories name it by. Letters, digits, '_', '.' and '-'."`
	Dir          string                   `json:"dir,omitempty" doc:"The directory the category's torrents download into: a relative path is below download_dir, and may not lead out of it. A torrent keeps the directory it was added with when this changes. <download_dir>/<name> when left out."`
	MediaLibrary string                   `json:"media_library,omitempty" doc:"A media_library to scan again when a torrent of the category completes, so that what it downloaded into the library's roots appears in its catalog. None when left out."`
	Limits       *BitTorrentTorrentLimits `json:"limits,omitempty" doc:"The limits of each torrent of the category, under the client's; a torrent's own limits take the place of those they set. The client's alone when left out."`

	At Value `json:"-" fit:"self"`
}

// BitTorrentTorrentLimits are a torrent's own limits, beside the client's,
// fib's TorrentLimits: zero takes the client's, and a negative count, seed
// ratio or seed time is no limit for the torrent.
type BitTorrentTorrentLimits struct {
	UploadRate   int      `json:"upload_rate,omitempty" doc:"The most bytes per second the torrent uploads, under the client's rate. The client's rate alone when left out, 0 or negative."`
	DownloadRate int      `json:"download_rate,omitempty" doc:"The most bytes per second the torrent downloads, under the client's rate. The client's rate alone when left out, 0 or negative."`
	MaxConns     int      `json:"max_conns,omitempty" doc:"The most peer connections of the torrent, in place of max_conns_per_torrent, above or below it; negative is the client's max_conns alone. max_conns_per_torrent when left out or 0."`
	UploadSlots  int      `json:"upload_slots,omitempty" doc:"How many peers the torrent uploads to at once, beside one optimistic unchoke, in place of the client's; negative unchokes every interested peer. The client's when left out or 0."`
	SeedRatio    float64  `json:"seed_ratio,omitempty" doc:"Pause the torrent once it has uploaded this many times its size, in place of the client's; negative seeds for ever. The client's when left out or 0."`
	SeedTime     Duration `json:"seed_time,omitempty" doc:"Pause the torrent once it has seeded this long, in place of the client's; negative seeds for ever. The client's when left out or 0."`

	At Value `json:"-" fit:"self"`
}

// Over returns l with the limits o sets in place of l's: what a torrent's
// own limits o make of its category's l.
func (l BitTorrentTorrentLimits) Over(o BitTorrentTorrentLimits) BitTorrentTorrentLimits {
	if o.UploadRate != 0 {
		l.UploadRate = o.UploadRate
	}
	if o.DownloadRate != 0 {
		l.DownloadRate = o.DownloadRate
	}
	if o.MaxConns != 0 {
		l.MaxConns = o.MaxConns
	}
	if o.UploadSlots != 0 {
		l.UploadSlots = o.UploadSlots
	}
	if o.SeedRatio != 0 {
		l.SeedRatio = o.SeedRatio
	}
	if o.SeedTime != 0 {
		l.SeedTime = o.SeedTime
	}
	l.At = Value{}
	return l
}

// IsZero reports whether l sets no limit.
func (l BitTorrentTorrentLimits) IsZero() bool {
	return l.UploadRate == 0 && l.DownloadRate == 0 && l.MaxConns == 0 && l.UploadSlots == 0 &&
		l.SeedRatio == 0 && l.SeedTime == 0
}

// BitTorrentAltSpeed are the BitTorrent client's alternative rates.
type BitTorrentAltSpeed struct {
	UploadRate   int                  `json:"upload_rate,omitempty" min:"0" doc:"The most bytes per second uploaded while the alternative rates hold, all torrents together. Unlimited when left out or 0."`
	DownloadRate int                  `json:"download_rate,omitempty" min:"0" doc:"The most bytes per second downloaded while the alternative rates hold, all torrents together. Unlimited when left out or 0."`
	Schedules    []BitTorrentSchedule `json:"schedules,omitempty" doc:"When the alternative rates hold: while any of these does. Only while the API turns them on when left out. At most 100."`
	TimeZone     string               `json:"time_zone,omitempty" doc:"The zone the schedules' times are in: local (the default), utc, or an IANA zone name such as Asia/Shanghai."`

	At Value `json:"-" fit:"self"`
}

// BitTorrentSchedule is a weekly time range.
type BitTorrentSchedule struct {
	Days []string `json:"days,omitempty" doc:"The days the range begins on: mon, tue, wed, thu, fri, sat, sun, weekdays (mon to fri) and weekend (sat and sun). A range that runs past midnight holds into the next day. Every day when left out."`
	From string   `json:"from" fit:"required" doc:"The time the range begins at, HH:MM, 00:00 to 23:59."`
	To   string   `json:"to" fit:"required" doc:"The time the range ends at, HH:MM, 00:01 to 24:00, not included: one earlier than from runs past midnight, as 22:00 to 06:00. It may not equal from."`

	At Value `json:"-" fit:"self"`
}

// Window returns the days a schedule begins on, by time.Weekday, and its
// times as minutes from midnight; an error says what is wrong with it.
func (s *BitTorrentSchedule) Window() (days [7]bool, from, to int, err error) {
	if len(s.Days) == 0 {
		days = [7]bool{true, true, true, true, true, true, true}
	}
	for _, d := range s.Days {
		switch strings.ToLower(strings.TrimSpace(d)) {
		case "sun":
			days[time.Sunday] = true
		case "mon":
			days[time.Monday] = true
		case "tue":
			days[time.Tuesday] = true
		case "wed":
			days[time.Wednesday] = true
		case "thu":
			days[time.Thursday] = true
		case "fri":
			days[time.Friday] = true
		case "sat":
			days[time.Saturday] = true
		case "weekdays":
			for i := time.Monday; i <= time.Friday; i++ {
				days[i] = true
			}
		case "weekend":
			days[time.Saturday], days[time.Sunday] = true, true
		default:
			return days, 0, 0, fmt.Errorf("%q is not a day: mon, tue, wed, thu, fri, sat, sun, weekdays or weekend", d)
		}
	}
	if from, err = parseClock(s.From, false); err != nil {
		return days, 0, 0, err
	}
	if to, err = parseClock(s.To, true); err != nil {
		return days, 0, 0, err
	}
	if to == 0 {
		to = 24 * 60
	}
	if from == to {
		return days, 0, 0, fmt.Errorf("from and to are both %s: the range is empty", s.From)
	}
	return days, from, to, nil
}

// parseClock parses HH:MM into minutes from midnight; 24:00 only when end
// says so.
func parseClock(s string, end bool) (int, error) {
	bad := fmt.Errorf("%q is not a time of day, HH:MM", s)
	h, m, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok || len(h) == 0 || len(h) > 2 || len(m) != 2 {
		return 0, bad
	}
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if err1 != nil || err2 != nil || hh < 0 || mm < 0 || mm > 59 || hh > 24 || hh == 24 && (mm != 0 || !end) {
		return 0, bad
	}
	return hh*60 + mm, nil
}

// Category returns the category named name, or nil.
func (b *BitTorrentClient) Category(name string) *BitTorrentCategory {
	for i := range b.Categories {
		if b.Categories[i].Name == name {
			return &b.Categories[i]
		}
	}
	return nil
}

// CategoryDir returns the directory the torrents of category c download
// into: dir, below download_dir when relative, or <download_dir>/<name>.
func (b *BitTorrentClient) CategoryDir(c *BitTorrentCategory) string {
	switch {
	case strings.TrimSpace(c.Dir) == "":
		return filepath.Join(b.Downloads(), c.Name)
	case filepath.IsAbs(c.Dir):
		return filepath.Clean(c.Dir)
	}
	return filepath.Join(b.Downloads(), c.Dir)
}

// Watch returns watch_interval, or its default.
func (b *BitTorrentClient) Watch() time.Duration {
	return durationOr(b.WatchInterval, DefaultBTWatchInterval)
}

// WatchFiles returns watch_max_files, or its default.
func (b *BitTorrentClient) WatchFiles() int {
	return int(orDefault(int64(b.WatchMaxFiles), DefaultBTWatchMaxFiles))
}

// URLFetchTimeout returns url_timeout, or its default.
func (b *BitTorrentClient) URLFetchTimeout() time.Duration {
	return durationOr(b.URLTimeout, DefaultBTURLTimeout)
}

// FreeBytes returns limits.min_free_bytes, or its default.
func (b *BitTorrentClient) FreeBytes() int64 {
	if b.Limits == nil {
		return DefaultBTMinFreeBytes
	}
	return orDefault(b.Limits.MinFreeBytes, DefaultBTMinFreeBytes)
}

// ActiveDownloads returns limits.max_active_downloads, 0 for no queue.
func (b *BitTorrentClient) ActiveDownloads() int {
	if b.Limits == nil || b.Limits.MaxActiveDownloads < 0 {
		return 0
	}
	return b.Limits.MaxActiveDownloads
}

// ActiveSeeds returns limits.max_active_seeds, 0 for no queue.
func (b *BitTorrentClient) ActiveSeeds() int {
	if b.Limits == nil || b.Limits.MaxActiveSeeds < 0 {
		return 0
	}
	return b.Limits.MaxActiveSeeds
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
	if b.WatchInterval.Std() < 0 {
		add(b.At.Field("watch_interval"), "watch_interval may not be negative")
	}
	if b.URLTimeout.Std() < 0 {
		add(b.At.Field("url_timeout"), "url_timeout may not be negative")
	}
	if len(b.Categories) > MaxBTCategories {
		add(b.At.Field("categories"), "at most %d categories", MaxBTCategories)
	}
	for i := range b.Categories {
		cat := &b.Categories[i]
		switch {
		case !namePattern.MatchString(cat.Name):
			add(cat.At.Field("name"), "%q is not a valid name: use letters, digits, '_', '.' and '-', starting with a letter or digit", cat.Name)
		case slices.IndexFunc(b.Categories, func(o BitTorrentCategory) bool { return o.Name == cat.Name }) < i:
			add(cat.At.Field("name"), "another category is already named %q", cat.Name)
		}
		if d := strings.TrimSpace(cat.Dir); d != "" && !filepath.IsAbs(d) && !filepath.IsLocal(d) {
			add(cat.At.Field("dir"), "%q leads out of download_dir: a relative dir stays below it", cat.Dir)
		}
		if cat.MediaLibrary != "" && c.MediaLibraryNamed(cat.MediaLibrary) == nil {
			add(cat.At.Field("media_library"), "no media_library is named %q", cat.MediaLibrary)
		}
	}
	if a := b.AltSpeed; a != nil {
		if _, err := LoadTimeZone(a.TimeZone); err != nil {
			add(a.At.Field("time_zone"), "%v", err)
		}
		if len(a.Schedules) > MaxBTSchedules {
			add(a.At.Field("schedules"), "at most %d schedules", MaxBTSchedules)
		}
		for i := range a.Schedules {
			if _, _, _, err := a.Schedules[i].Window(); err != nil {
				add(a.At.Field("schedules").Index(i), "%v", err)
			}
		}
	}
	b.Access.check(add)
	checkRoleConsole(b.Console, add)
	checkBitTorrentPublish(b, add)
	checkBitTorrentRSS(b, add)
}
