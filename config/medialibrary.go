package config

import (
	"path"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strings"
	"time"
)

// The defaults and bounds of MediaLibrary.
const (
	DefaultMediaScanInterval = 10 * time.Minute
	// MinMediaScanInterval is the shortest scan_interval may be.
	MinMediaScanInterval        = 10 * time.Second
	DefaultMediaMaxItems        = 20000
	DefaultMediaMaxFiles        = 200000
	DefaultMediaMaxCatalogBytes = 64 << 20
	DefaultMediaMaxProbes       = 4
	DefaultMediaMaxSidecarBytes = 64 << 10
	// MaxMediaSidecarBytes is the most max_sidecar_bytes may be.
	MaxMediaSidecarBytes          = 1 << 20
	DefaultMediaMaxSubtitleBytes  = 2 << 20
	DefaultMediaMaxTableBytes     = 128 << 20
	DefaultMediaFileQueue         = 4096
	DefaultMediaFileTimeout       = 10 * time.Second
	minMediaFileWorkers           = 16
	defaultMediaFileWorkersPerCPU = 4
)

// The defaults and bounds of a MediaLibrary's uploads.
const (
	DefaultMediaMaxUploadBytes = 32 << 30
	DefaultMediaMaxUploads     = 4
	DefaultMediaMinFreeBytes   = 1 << 30
	DefaultMediaUploadExpiry   = 24 * time.Hour
	// MinMediaUploadExpiry is the shortest upload_expiry may be.
	MinMediaUploadExpiry = time.Minute
)

// MediaLibrary is a named library of media files: directories scanned into a
// catalog of items, each a title with its renditions, audio, subtitles and
// poster, which the media handlers serve. See
// docs/decisions/032-media-library.md and docs/media-library.md.
type MediaLibrary struct {
	Name             string   `json:"name" fit:"required" doc:"The library's name, which handlers and the Admin API's /media/library/<name> refer to it by. Letters, digits, '_', '.' and '-'."`
	Roots            []string `json:"roots" fit:"required" doc:"The directories whose files the library holds, scanned with their subdirectories; an item's id is its path under its root. A root may not lie inside another. A root missing when scanned is reported, and the others are scanned."`
	Patterns         []string `json:"files,omitempty" doc:"The files looked at, by pattern: a pattern without '/' matches a file's name, as *.mp4, and one with '/' its path under the root, as films/**/*.mp4, where * matches within a name and ** any run of directories. Every file when left out; names starting with '.' are never looked at."`
	Exclude          []string `json:"exclude,omitempty" doc:"Files and directories left out, by pattern, as files writes them, as trailers or **/extras/**; a directory left out is not walked. None when left out."`
	FollowSymlinks   bool     `json:"follow_symlinks,omitempty" doc:"Follow symbolic links that lead out of the library's roots. A link that stays within its root is followed either way; one that leads out is refused, and reported, when left out or false."`
	ScanInterval     Duration `json:"scan_interval,omitempty" default:"\"10m\"" doc:"How often the roots are scanned again, at least 10s; a scan also runs when the library is made, when the config changes its roots, patterns or bounds, and on the Admin API's POST /media/library/<name>/rescan. A file whose size and time are unchanged is not probed again. 10m when left out."`
	MaxItems         int      `json:"max_items,omitempty" min:"0" default:"20000" doc:"The most items the catalog holds; past it the rest are left out, and the scan says so. 20000 when left out or 0."`
	MaxFiles         int      `json:"max_files,omitempty" min:"0" default:"200000" doc:"The most files and directories a scan looks at, across the roots; past it the scan stops there, and says so. 200000 when left out or 0."`
	MaxCatalogBytes  int64    `json:"max_catalog_bytes,omitempty" min:"0" default:"67108864" doc:"The most memory the catalog takes, as reckoned from its items' paths, titles, tracks and codec configurations; past it the rest of the items are left out, and the scan says so. 64 MiB when left out or 0."`
	MaxProbes        int      `json:"max_probes,omitempty" min:"0" default:"4" doc:"The most files probed at once on the library's file pool, so that a scan leaves the pool's other workers to the files being served. 4 when left out or 0."`
	MaxSidecarBytes  int64    `json:"max_sidecar_bytes,omitempty" min:"0" max:"1048576" default:"65536" doc:"The largest sidecar, <name>.json, read; a larger one is reported and left out. At most 1 MiB. 64 KiB when left out or 0."`
	MaxSubtitleBytes int64    `json:"max_subtitle_bytes,omitempty" min:"0" default:"2097152" doc:"The largest subtitle file served, .vtt or .srt turned into WebVTT; a larger one is listed with its error and not served. 2 MiB when left out or 0."`
	MaxTableBytes    int64    `json:"max_table_bytes,omitempty" min:"0" default:"134217728" doc:"The most memory the sample tables read from the files take, kept for the files being served; past it the least recently used are dropped, and a table larger than all of it is refused. 128 MiB when left out or 0."`
	FileWorkers      int      `json:"file_workers,omitempty" min:"0" doc:"The goroutines of the library's file pool, which probe, read sample tables and read the files' bytes, so that no fib worker waits on the disk: all the disk is asked to do at once. 4 a CPU, and at least 16, when left out or 0."`
	FileQueue        int      `json:"file_queue,omitempty" min:"0" default:"4096" doc:"The most jobs that wait for the file pool; one more is refused, as a request answered 503. 4096 when left out or 0."`
	FileTimeout      Duration `json:"file_timeout,omitempty" default:"\"10s\"" doc:"How long a job may wait for the file pool before it is given up, as a request answered 503. 10s when left out."`
	Secret           string   `json:"secret,omitempty" doc:"The secret, at least 16 bytes, the AES-128 keys of encrypted HLS are derived from: a key for each item of vod with encrypt, and for each channel with encrypt, rotated every key_rotation segments. Changing it changes every key, so that segments encrypted before, kept by a CDN, no longer play: purge them. Or secret_file, one of the two. None when left out: nothing of the library can be encrypted."`
	SecretFile       string   `json:"secret_file,omitempty" doc:"A file holding the secret the AES-128 keys of encrypted HLS are derived from, read each time the config is applied. At least 16 bytes, its line breaks at the end aside. Or secret, one of the two. None when left out."`
	UploadDir        string   `json:"upload_dir,omitempty" doc:"A directory inside one of the roots, or a root itself, that files uploaded from the workbench, or the Admin API's /media/library/<name>/uploads, are written into: in pieces, resumable after a cut connection, to a temporary file named .fit-upload-*, renamed into place once whole and probed by the scan that follows. Made when missing. None when left out: no uploads; the files of the library are still renamed, moved and deleted from the workbench."`
	MaxUploadBytes   int64    `json:"max_upload_bytes,omitempty" min:"0" default:"34359738368" doc:"The largest file uploaded; a larger one is refused 413 before any of it is written. 32 GiB when left out or 0."`
	MaxUploads       int      `json:"max_uploads,omitempty" min:"0" max:"1024" default:"4" doc:"The most uploads under way at once, begun and not yet whole; one more is refused 429 until one ends, or is cancelled, or expires. 4 when left out or 0."`
	MinFreeBytes     int64    `json:"min_free_bytes,omitempty" min:"0" default:"1073741824" doc:"The disk space left free beside the uploads under way: an upload whose bytes, with those of the others still to come, would leave less free on upload_dir's disk is refused 507 before any of it is written. 1 GiB when left out or 0."`
	UploadExpiry     Duration `json:"upload_expiry,omitempty" default:"\"24h\"" doc:"How long an upload not written to is kept before it is given up and its temporary file removed, at the next scan; at least 1m. An upload is kept across restarts until then. 24h when left out."`

	At Value `json:"-" fit:"self"`
}

// Interval returns scan_interval, or its default.
func (m *MediaLibrary) Interval() time.Duration {
	return durationOr(m.ScanInterval, DefaultMediaScanInterval)
}

// Items returns max_items, or its default.
func (m *MediaLibrary) Items() int {
	return int(orDefault(int64(m.MaxItems), DefaultMediaMaxItems))
}

// Files returns max_files, or its default.
func (m *MediaLibrary) Files() int {
	return int(orDefault(int64(m.MaxFiles), DefaultMediaMaxFiles))
}

// CatalogBytes returns max_catalog_bytes, or its default.
func (m *MediaLibrary) CatalogBytes() int64 {
	return orDefault(m.MaxCatalogBytes, DefaultMediaMaxCatalogBytes)
}

// Probes returns max_probes, or its default.
func (m *MediaLibrary) Probes() int {
	return int(orDefault(int64(m.MaxProbes), DefaultMediaMaxProbes))
}

// SidecarBytes returns max_sidecar_bytes, or its default.
func (m *MediaLibrary) SidecarBytes() int64 {
	return orDefault(m.MaxSidecarBytes, DefaultMediaMaxSidecarBytes)
}

// SubtitleBytes returns max_subtitle_bytes, or its default.
func (m *MediaLibrary) SubtitleBytes() int64 {
	return orDefault(m.MaxSubtitleBytes, DefaultMediaMaxSubtitleBytes)
}

// TableBytes returns max_table_bytes, or its default.
func (m *MediaLibrary) TableBytes() int64 {
	return orDefault(m.MaxTableBytes, DefaultMediaMaxTableBytes)
}

// Workers returns file_workers, or its default: 4 a CPU, and at least 16.
func (m *MediaLibrary) Workers() int {
	return int(orDefault(int64(m.FileWorkers), int64(max(minMediaFileWorkers, defaultMediaFileWorkersPerCPU*goruntime.GOMAXPROCS(0)))))
}

// Queue returns file_queue, or its default.
func (m *MediaLibrary) Queue() int {
	return int(orDefault(int64(m.FileQueue), DefaultMediaFileQueue))
}

// Timeout returns file_timeout, or its default.
func (m *MediaLibrary) Timeout() time.Duration {
	return durationOr(m.FileTimeout, DefaultMediaFileTimeout)
}

// UploadBytes returns max_upload_bytes, or its default.
func (m *MediaLibrary) UploadBytes() int64 {
	return orDefault(m.MaxUploadBytes, DefaultMediaMaxUploadBytes)
}

// Uploads returns max_uploads, or its default.
func (m *MediaLibrary) Uploads() int {
	return int(orDefault(int64(m.MaxUploads), DefaultMediaMaxUploads))
}

// FreeBytes returns min_free_bytes, or its default.
func (m *MediaLibrary) FreeBytes() int64 {
	return orDefault(m.MinFreeBytes, DefaultMediaMinFreeBytes)
}

// Expiry returns upload_expiry, or its default.
func (m *MediaLibrary) Expiry() time.Duration {
	return durationOr(m.UploadExpiry, DefaultMediaUploadExpiry)
}

// Encryptable reports whether the library has a secret to derive the keys
// of encrypted HLS from.
func (m *MediaLibrary) Encryptable() bool { return m.Secret != "" || m.SecretFile != "" }

// MediaLibraryNamed returns the library of c named name, or nil.
func (c *Config) MediaLibraryNamed(name string) *MediaLibrary {
	for i := range c.MediaLibrary {
		if c.MediaLibrary[i].Name == name {
			return &c.MediaLibrary[i]
		}
	}
	return nil
}

// ValidMediaPathPattern reports whether p is a pattern of a media library's
// files or exclude: not empty, its parts split at '/' each either ** or
// a pattern path.Match takes.
func ValidMediaPathPattern(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") {
		return false
	}
	for part := range strings.SplitSeq(p, "/") {
		if part == "**" {
			continue
		}
		if part == "" {
			return false
		}
		if _, err := path.Match(part, ""); err != nil {
			return false
		}
	}
	return true
}

func (c *Config) checkMediaLibrary(add func(Value, string, ...any)) {
	for i := range c.MediaLibrary {
		m := &c.MediaLibrary[i]
		switch {
		case !namePattern.MatchString(m.Name):
			add(m.At.Field("name"), "%q is not a valid name: use letters, digits, '_', '.' and '-', starting with a letter or digit", m.Name)
		case slices.IndexFunc(c.MediaLibrary, func(o MediaLibrary) bool { return o.Name == m.Name }) < i:
			add(m.At.Field("name"), "another media_library is already named %q", m.Name)
		}
		if len(m.Roots) == 0 {
			add(m.At.Field("roots"), "a library needs at least one root")
		}
		clean := make([]string, len(m.Roots))
		for j, r := range m.Roots {
			if strings.TrimSpace(r) == "" {
				add(m.At.Field("roots").Index(j), "a root may not be empty")
				continue
			}
			clean[j] = filepath.Clean(r)
			if abs, err := filepath.Abs(clean[j]); err == nil {
				clean[j] = abs
			}
			for k := range j {
				switch {
				case clean[k] == "":
				case clean[k] == clean[j]:
					add(m.At.Field("roots").Index(j), "%q is listed twice", r)
				case within(clean[k], clean[j]), within(clean[j], clean[k]):
					add(m.At.Field("roots").Index(j), "%q and %q lie one inside the other: the files would be listed twice", m.Roots[k], r)
				}
			}
		}
		for _, f := range []struct {
			name string
			list []string
		}{{"files", m.Patterns}, {"exclude", m.Exclude}} {
			for j, p := range f.list {
				if !ValidMediaPathPattern(p) {
					add(m.At.Field(f.name).Index(j), "%q is not a pattern: names and *, ? and [...] as path.Match takes them, and ** for any run of directories, split at '/', not starting with one", p)
				}
			}
		}
		switch {
		case m.Secret != "" && m.SecretFile != "":
			add(m.At.Field("secret_file"), "set secret or secret_file, one of the two")
		case m.Secret != "" && len(m.Secret) < MinMediaSecret:
			add(m.At.Field("secret"), "the secret is %d bytes: use at least %d", len(m.Secret), MinMediaSecret)
		}
		if d := m.UploadExpiry.Std(); d != 0 && d < MinMediaUploadExpiry {
			add(m.At.Field("upload_expiry"), "upload_expiry is %s: at least %s", d, MinMediaUploadExpiry)
		}
		if m.UploadDir != "" {
			dir, err := filepath.Abs(filepath.Clean(m.UploadDir))
			if err != nil || strings.TrimSpace(m.UploadDir) == "" || !slices.ContainsFunc(clean, func(r string) bool { return r != "" && (r == dir || within(r, dir)) }) {
				add(m.At.Field("upload_dir"), "%q is not inside a root of the library: uploads are written into one of its roots", m.UploadDir)
			}
		}
		if d := m.ScanInterval.Std(); d != 0 && d < MinMediaScanInterval {
			add(m.At.Field("scan_interval"), "scan_interval is %s: at least %s", d, MinMediaScanInterval)
		}
	}
}

// within reports whether p lies inside dir, both clean absolute paths.
func within(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
