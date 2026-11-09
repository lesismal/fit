package config

import (
	"net/url"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// The defaults and bounds of BitTorrentPublish.
const (
	// MaxBTPublish is the most publish entries a client may have.
	MaxBTPublish = 64
	// DefaultBTPublishMaxFiles is max_files when left out.
	DefaultBTPublishMaxFiles = 1000
	// MaxBTPublishFiles is the most max_files may be.
	MaxBTPublishFiles = 100000
	// DefaultBTPublishMaxTotalBytes is max_total_bytes when left out: 1 TiB.
	DefaultBTPublishMaxTotalBytes = 1 << 40
	// DefaultBTPublishMaxHashing is max_hashing when left out.
	DefaultBTPublishMaxHashing = 1
	// MaxBTPublishHashing is the most max_hashing may be.
	MaxBTPublishHashing = 16
	// DefaultBTPublishCheckInterval is check_interval when left out.
	DefaultBTPublishCheckInterval = 10 * time.Second
	// MinBTPublishPieceLength and MaxBTPublishPieceLength bound
	// piece_length, a power of two between them.
	MinBTPublishPieceLength = 16 << 10
	MaxBTPublishPieceLength = 64 << 20
)

// The modes of a publish entry.
const (
	// BTPublishFiles makes a torrent of each file.
	BTPublishFiles = "per_file"
	// BTPublishDir makes one torrent of the whole directory.
	BTPublishDir = "whole_dir"
)

// BitTorrentPublish is a directory the BitTorrent client publishes: built
// into hybrid torrents, seeded in place, and served as .torrent files and
// magnet links by the bt_publish handler, following the disk as it changes.
type BitTorrentPublish struct {
	Name          string   `json:"name" fit:"required" doc:"The entry's name, which the bt_publish handler and the state in data_dir know it by. Letters, digits, '_', '.' and '-'."`
	Dir           string   `json:"dir" fit:"required" doc:"The directory published. Its files are seeded where they are, never written to, moved or deleted: a torrent whose file changes is replaced by a new one. Files and directories whose names start with '.' and symbolic links are left out."`
	Mode          string   `json:"mode,omitempty" enum:"per_file,whole_dir" default:"\"per_file\"" doc:"per_file makes a single-file torrent of each file below dir; whole_dir makes one torrent of the whole directory, named as its last element, which any change of a file below it replaces. per_file when left out."`
	WebSeed       string   `json:"web_seed,omitempty" doc:"The http or https URL at which dir is served, as by a static route whose root is dir, put in every torrent as its web seed (BEP 19): a file's own URL in mode per_file, and in mode whole_dir the URL's parent, so the URL's last element must be dir's last element then. None when left out."`
	Announce      []string `json:"announce,omitempty" doc:"Tracker URLs, http, https or udp, put in every torrent, each a tier of its own, tried in order: as fit's own bt_tracker route. None when left out: peers find the torrents by the DHT, unless private."`
	Private       bool     `json:"private,omitempty" doc:"Mark the torrents private (BEP 27): clients find peers only through the trackers of announce, not the DHT or peer exchange. A change makes new torrents, of other info hashes."`
	PieceLength   int64    `json:"piece_length,omitempty" min:"0" max:"67108864" doc:"The torrents' piece length, a power of two from 16 KiB to 64 MiB. Picked for each torrent's size when left out or 0: the smallest of at least 16 KiB that makes fewer than 2048 pieces, up to 16 MiB. A change makes new torrents."`
	Include       []string `json:"files,omitempty" doc:"The files published, by pattern: a pattern without '/' matches a file's name, as *.iso, and one with '/' its path under dir, as releases/**/*.tar.gz, where * matches within a name and ** any run of directories. Every file when left out."`
	Exclude       []string `json:"exclude,omitempty" doc:"Files and directories left out, by pattern, as files writes them, as *.part or **/tmp/**; a directory left out is not walked. None when left out."`
	CheckInterval Duration `json:"check_interval,omitempty" default:"\"10s\"" doc:"How often dir is walked again for files added, changed or removed, as static files follow the disk. A file is told by its size, modification and status change times and inode; one new or changed is built into a torrent once it has not changed for a check_interval, so that a file still being copied is not hashed; a file removed or changed stops its torrent at once. 10s when left out or 0."`
	MaxFiles      int      `json:"max_files,omitempty" min:"0" max:"100000" default:"1000" doc:"The most files published: in mode per_file the first by path are, the rest are left out and reported; in mode whole_dir a directory with more is not published, and is reported. 1000 when left out or 0."`
	MaxTotalBytes int64    `json:"max_total_bytes,omitempty" min:"0" default:"1099511627776" doc:"The most bytes published, the files' sizes together: past it, in mode per_file, the files after are left out, and in mode whole_dir the directory is not published; either is reported. 1 TiB when left out or 0."`
	MaxHashing    int      `json:"max_hashing,omitempty" min:"0" max:"16" default:"1" doc:"How many torrents are built at once, each reading and hashing its files on a goroutine of the entry's own, never on a fib worker. 1 when left out or 0."`

	At Value `json:"-" fit:"self"`
}

// PublishMode returns mode, or its default.
func (p *BitTorrentPublish) PublishMode() string {
	if p.Mode == "" {
		return BTPublishFiles
	}
	return p.Mode
}

// Interval returns check_interval, or its default.
func (p *BitTorrentPublish) Interval() time.Duration {
	return durationOr(p.CheckInterval, DefaultBTPublishCheckInterval)
}

// Files returns max_files, or its default.
func (p *BitTorrentPublish) Files() int {
	return int(orDefault(int64(p.MaxFiles), DefaultBTPublishMaxFiles))
}

// TotalBytes returns max_total_bytes, or its default.
func (p *BitTorrentPublish) TotalBytes() int64 {
	return orDefault(p.MaxTotalBytes, DefaultBTPublishMaxTotalBytes)
}

// Hashing returns max_hashing, or its default.
func (p *BitTorrentPublish) Hashing() int {
	return int(orDefault(int64(p.MaxHashing), DefaultBTPublishMaxHashing))
}

func checkBitTorrentPublish(b *BitTorrentClient, add func(Value, string, ...any)) {
	if len(b.Publish) > MaxBTPublish {
		add(b.At.Field("publish"), "%d publish entries, more than %d", len(b.Publish), MaxBTPublish)
	}
	for i := range b.Publish {
		p := &b.Publish[i]
		switch {
		case !namePattern.MatchString(p.Name):
			add(p.At.Field("name"), "%q is not a valid name: use letters, digits, '_', '.' and '-', starting with a letter or digit", p.Name)
		case slices.IndexFunc(b.Publish, func(o BitTorrentPublish) bool { return o.Name == p.Name }) < i:
			add(p.At.Field("name"), "another publish entry is already named %q", p.Name)
		}
		dir := ""
		if strings.TrimSpace(p.Dir) == "" {
			add(p.At.Field("dir"), "dir may not be empty")
		} else if abs, err := filepath.Abs(p.Dir); err != nil {
			add(p.At.Field("dir"), "%v", err)
		} else {
			dir = abs
			if filepath.Dir(abs) == abs {
				add(p.At.Field("dir"), "%q is the root of a file system, which is not published", p.Dir)
			}
		}
		if p.WebSeed != "" {
			u, err := url.Parse(p.WebSeed)
			switch {
			case err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "":
				add(p.At.Field("web_seed"), "%q is not an http or https URL", p.WebSeed)
			case u.RawQuery != "" || u.Fragment != "":
				add(p.At.Field("web_seed"), "%q has a query or a fragment, which a web seed's URL may not: the paths of the files are appended to it", p.WebSeed)
			case p.PublishMode() == BTPublishDir && dir != "" && path.Base(strings.TrimSuffix(u.Path, "/")) != filepath.Base(dir):
				add(p.At.Field("web_seed"), "in mode whole_dir the URL's last element must be the directory's, %q: clients ask the URL's parent for <name>/<path>, the torrent being named as the directory", filepath.Base(dir))
			}
		}
		for j, a := range p.Announce {
			u, err := url.Parse(a)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "udp") || u.Host == "" {
				add(p.At.Field("announce").Index(j), "%q is not an http, https or udp tracker URL", a)
			}
		}
		if l := p.PieceLength; l != 0 && (l < MinBTPublishPieceLength || l&(l-1) != 0) {
			add(p.At.Field("piece_length"), "%d is not a power of two from 16 KiB to 64 MiB", l)
		}
		for _, f := range []struct {
			name string
			list []string
		}{{"files", p.Include}, {"exclude", p.Exclude}} {
			for j, pat := range f.list {
				if !ValidMediaPathPattern(pat) {
					add(p.At.Field(f.name).Index(j), "%q is not a pattern: names and *, ? and [...] as path.Match takes them, and ** for any run of directories, split at '/', not starting with one", pat)
				}
			}
		}
	}
}
