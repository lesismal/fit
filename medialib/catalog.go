package medialib

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/lesismal/fib/media/av"
	"github.com/lesismal/fib/media/mp4"
)

// Catalog is what a scan found in a library: its items, by ID, and the
// problems it met. A Catalog is immutable: a scan makes a new one, and
// whoever holds the old one keeps reading it.
type Catalog struct {
	// Items are sorted by ID.
	Items []*Item
	// Problems are what the scan could not list, as a symbolic link out of
	// a root, a sidecar that does not parse or an ID taken twice, at most
	// MaxProblems of them.
	Problems []Problem
	// MoreProblems counts the problems past MaxProblems, not listed.
	MoreProblems int
	// Truncated says why the catalog lacks items the roots hold, past a
	// bound; "" when it lacks none.
	Truncated string
	// Bytes is the memory the catalog takes, as reckoned for
	// max_catalog_bytes.
	Bytes int64
	// Scanned is when the scan that made it ended; zero before the first.
	Scanned time.Time

	byID map[string]*Item
}

// MaxProblems is the most problems a catalog lists; the rest are counted.
const MaxProblems = 256

// Problem is a file or directory a scan could not list, and why.
type Problem struct {
	Root  string `json:"root,omitempty"`
	Path  string `json:"path,omitempty"`
	Error string `json:"error"`
}

// Item returns the item of c whose ID is id, or nil.
func (c *Catalog) Item(id string) *Item {
	if c == nil {
		return nil
	}
	return c.byID[id]
}

// without returns a copy of c without the items of ids.
func (c *Catalog) without(ids []string) *Catalog {
	out := *c
	out.Items = make([]*Item, 0, len(c.Items))
	out.byID = make(map[string]*Item, len(c.byID))
	for _, it := range c.Items {
		if slices.Contains(ids, it.ID) {
			out.Bytes -= it.bytes
			continue
		}
		out.Items = append(out.Items, it)
		out.byID[it.ID] = it
	}
	return &out
}

// files returns the files of it: its renditions, audio, subtitles, poster
// and sidecar.
func (it *Item) files() []*File {
	var out []*File
	for _, list := range [][]*Rendition{it.Renditions, it.Audio} {
		for _, r := range list {
			out = append(out, &r.File)
		}
	}
	for _, s := range it.Subtitles {
		out = append(out, &s.File)
	}
	for _, f := range []*File{it.Poster, it.Sidecar} {
		if f != nil {
			out = append(out, f)
		}
	}
	return out
}

// File is a file of a library: where it is and its size and time when it
// was scanned. A file found changed when it is opened is refused with
// ErrChanged.
type File struct {
	// Root is the root it was found under, as the config writes it.
	Root string `json:"root"`
	// Path is its path under the root, with '/'.
	Path string `json:"path"`
	// Size and ModTime are as the scan found them.
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modified"`

	abs  string // the path it is opened by
	real string // the root's path with its links resolved
}

// Item is an entry of a catalog: a title, with the files of its renditions,
// audio, subtitles and poster.
type Item struct {
	// ID is the item's sidecar's id, or made from its path under its root.
	ID string
	// Title is the sidecar's, or the file's name.
	Title string
	// Root and Path are its root and the path under it of its files, less
	// their rendition label and extension, as films/bbb.
	Root, Path string
	// Duration is the longest of its renditions'.
	Duration time.Duration
	// Size is its files' bytes, and ModTime the latest of their times.
	Size    int64
	ModTime time.Time
	// Renditions are its files with video, Audio those with audio alone.
	Renditions []*Rendition
	Audio      []*Rendition
	Subtitles  []*Subtitle
	// Poster and Sidecar are nil when there are none.
	Poster  *File
	Sidecar *File
	// Error says why the item cannot be served, as none of its files
	// probed right; "" when it can be.
	Error string

	bytes int64
	named bool // its ID is its sidecar's
}

// Rendition is a media file of an item, probed.
type Rendition struct {
	File
	// Name is the rendition's label, as 1080p, or the sidecar's name.
	Name string
	// Language is the sidecar's language, or its audio's.
	Language string
	// Default marks the audio a player picks first, as the sidecar says.
	Default bool
	// Duration is the file's, and Bitrate its bytes over it, in bits a
	// second.
	Duration time.Duration
	Bitrate  int64
	// Width and Height are its first video track's.
	Width, Height int
	// Codecs is the RFC 6381 codecs of its tracks, as a playlist's CODECS
	// writes them: avc1.64001f,mp4a.40.2.
	Codecs string
	Tracks []Track
	// Error says why the file cannot be served; "" when it can be.
	Error string

	mp4       *mp4.File
	transient bool // an Error that may pass: the file was changing, or the pool busy
}

// MP4 returns the file as fib's media/mp4 opened it, whose tracks Table
// reads the sample tables of; nil for a rendition with an Error.
func (r *Rendition) MP4() *mp4.File { return r.mp4 }

// Track is a track of a rendition.
type Track struct {
	// Index is the track's in the MP4's Tracks.
	Index int
	// ID is its track_ID.
	ID uint32
	// Kind is video, audio, subtitle or other.
	Kind string
	// Codec is h264, h265, av1, aac, opus, mp3, pcmu, pcma, or unknown for
	// one fit does not serve; CodecString its RFC 6381 codecs.
	Codec       string
	CodecString string
	Language    string
	Width       int
	Height      int
	SampleRate  int
	Channels    int
	// Start is when it starts to play, after its edit list's empty edits,
	// and Duration how long it plays.
	Start, Duration time.Duration
	Samples         int
	// TableBytes is the memory its sample table takes once read.
	TableBytes int64
}

// Subtitle is a subtitle file of an item, .vtt or .srt, served as WebVTT.
type Subtitle struct {
	File
	// Format is vtt or srt, as the file is written.
	Format string
	// Language is from the sidecar or the file's name (bbb.en.srt), and
	// Name too, or the language when neither says.
	Language, Name string
	// Default and Forced are from the sidecar or the file's name
	// (bbb.en.forced.srt).
	Default, Forced bool
	// Error says why it cannot be served, as past max_subtitle_bytes.
	Error string
}

// The kinds of a track.
const (
	KindVideo    = "video"
	KindAudio    = "audio"
	KindSubtitle = "subtitle"
	KindOther    = "other"
)

func kindOf(handler string) string {
	switch handler {
	case "vide":
		return KindVideo
	case "soun":
		return KindAudio
	case "subt", "text", "sbtl":
		return KindSubtitle
	}
	return KindOther
}

// codecNames are the codecs' names in the catalog.
var codecNames = map[av.CodecID]string{
	av.CodecH264: "h264", av.CodecH265: "h265", av.CodecAV1: "av1",
	av.CodecAAC: "aac", av.CodecOpus: "opus", av.CodecMP3: "mp3",
	av.CodecPCMU: "pcmu", av.CodecPCMA: "pcma",
}

func codecName(c av.CodecID) string {
	if n, ok := codecNames[c]; ok {
		return n
	}
	return "unknown"
}

// idPart is what a part of an ID may hold.
var idPart = regexp.MustCompile(`^[A-Za-z0-9_~-][A-Za-z0-9._~-]{0,127}$`)

// MaxIDLength is the longest an item's ID may be.
const MaxIDLength = 256

// ValidID reports whether id is an item's ID: parts of 1 to 128 letters,
// digits, '.', '_', '~' and '-', not starting with '.', joined by '/', 256
// bytes at most; so that it is a path of a URL as it is, and a stream name
// a media_auth pattern can match.
func ValidID(id string) bool {
	if id == "" || len(id) > MaxIDLength {
		return false
	}
	for part := range strings.SplitSeq(id, "/") {
		if !idPart.MatchString(part) {
			return false
		}
	}
	return true
}

// PathID returns the ID an item at rel, a slash path under its root
// without its extension, has when its sidecar names none.
func PathID(rel string) string { return pathID(rel) }

// pathID returns the ID of an item at rel, a slash path under its root: rel
// itself when it is an ID, and otherwise rel with every run of what an ID
// may not hold made '-', and a hash of rel after it, so that two paths
// never make one ID and a path makes the same ID every scan.
func pathID(rel string) string {
	if ValidID(rel) {
		return rel
	}
	parts := strings.Split(rel, "/")
	for i, p := range parts {
		var b strings.Builder
		dash := false
		for _, r := range p {
			if r < 0x80 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '~' || r == '.' && b.Len() > 0) {
				b.WriteRune(r)
				dash = false
			} else if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
		s := strings.TrimRight(b.String(), "-.")
		if len(s) > 64 {
			s = strings.TrimRight(s[:64], "-.")
		}
		if s == "" {
			s = "x"
		}
		parts[i] = s
	}
	sum := sha256.Sum256([]byte(rel))
	id := strings.Join(parts, "/") + "-" + hex.EncodeToString(sum[:4])
	if !ValidID(id) {
		// Too long, deep paths: the hash alone of what is past the first part.
		id = parts[0] + "/" + hex.EncodeToString(sum[:8])
	}
	return id
}

// The memory an item's parts are reckoned to take, beside their strings
// and codec configurations: Go's headers, maps and pointers, rounded up.
const (
	itemBytes      = 512
	renditionBytes = 384
	trackBytes     = 256
	subtitleBytes  = 192
	fileBytes      = 128
	editBytes      = 24
)

// reckon sets it.bytes, and returns it.
func (it *Item) reckon() int64 {
	n := int64(itemBytes + 2*len(it.ID) + len(it.Title) + len(it.Root) + len(it.Path) + len(it.Error))
	file := func(f *File) int64 {
		return int64(fileBytes + len(f.Root) + len(f.Path) + len(f.abs) + len(f.real))
	}
	for _, list := range [][]*Rendition{it.Renditions, it.Audio} {
		for _, r := range list {
			n += renditionBytes + file(&r.File) + int64(len(r.Name)+len(r.Language)+len(r.Codecs)+len(r.Error))
			n += int64(len(r.Tracks)) * trackBytes
			for _, t := range r.Tracks {
				n += int64(len(t.CodecString) + len(t.Language))
			}
			if r.mp4 != nil {
				for _, t := range r.mp4.Tracks {
					n += trackBytes + int64(len(t.Edits))*editBytes
					if t.Track != nil {
						n += int64(len(t.Track.Config))
					}
				}
			}
		}
	}
	for _, s := range it.Subtitles {
		n += subtitleBytes + file(&s.File) + int64(len(s.Language)+len(s.Name)+len(s.Error))
	}
	for _, f := range []*File{it.Poster, it.Sidecar} {
		if f != nil {
			n += file(f)
		}
	}
	it.bytes = n
	return n
}

// seconds is a duration written in JSON as seconds.
type seconds time.Duration

func (s seconds) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(s).Seconds())
}

// MarshalJSON writes the item as the Admin API shows it, its durations in
// seconds.
func (it *Item) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ID         string       `json:"id"`
		Title      string       `json:"title"`
		Root       string       `json:"root"`
		Path       string       `json:"path"`
		Duration   seconds      `json:"duration"`
		Size       int64        `json:"size"`
		ModTime    time.Time    `json:"modified"`
		Renditions []*Rendition `json:"renditions"`
		Audio      []*Rendition `json:"audio"`
		Subtitles  []*Subtitle  `json:"subtitles"`
		Poster     *File        `json:"poster,omitempty"`
		Sidecar    *File        `json:"sidecar,omitempty"`
		Error      string       `json:"error,omitempty"`
	}{it.ID, it.Title, it.Root, it.Path, seconds(it.Duration), it.Size, it.ModTime,
		nonNil(it.Renditions), nonNil(it.Audio), nonNil(it.Subtitles), it.Poster, it.Sidecar, it.Error})
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// MarshalJSON writes the rendition as the Admin API shows it.
func (r *Rendition) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		File
		Name     string  `json:"name"`
		Language string  `json:"language,omitempty"`
		Default  bool    `json:"default,omitempty"`
		Duration seconds `json:"duration"`
		Bitrate  int64   `json:"bitrate"`
		Width    int     `json:"width,omitempty"`
		Height   int     `json:"height,omitempty"`
		Codecs   string  `json:"codecs"`
		Tracks   []Track `json:"tracks"`
		Error    string  `json:"error,omitempty"`
	}{r.File, r.Name, r.Language, r.Default, seconds(r.Duration), r.Bitrate, r.Width, r.Height, r.Codecs, nonNil(r.Tracks), r.Error})
}

// MarshalJSON writes the track as the Admin API shows it.
func (t Track) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Index       int     `json:"index"`
		ID          uint32  `json:"id"`
		Kind        string  `json:"kind"`
		Codec       string  `json:"codec"`
		CodecString string  `json:"codec_string,omitempty"`
		Language    string  `json:"language,omitempty"`
		Width       int     `json:"width,omitempty"`
		Height      int     `json:"height,omitempty"`
		SampleRate  int     `json:"sample_rate,omitempty"`
		Channels    int     `json:"channels,omitempty"`
		Start       seconds `json:"start"`
		Duration    seconds `json:"duration"`
		Samples     int     `json:"samples"`
		TableBytes  int64   `json:"table_bytes"`
	}{t.Index, t.ID, t.Kind, t.Codec, t.CodecString, t.Language, t.Width, t.Height, t.SampleRate, t.Channels,
		seconds(t.Start), seconds(t.Duration), t.Samples, t.TableBytes})
}

// MarshalJSON writes the subtitle as the Admin API shows it.
func (s *Subtitle) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		File
		Format   string `json:"format"`
		Language string `json:"language,omitempty"`
		Name     string `json:"name,omitempty"`
		Default  bool   `json:"default,omitempty"`
		Forced   bool   `json:"forced,omitempty"`
		Error    string `json:"error,omitempty"`
	}{s.File, s.Format, s.Language, s.Name, s.Default, s.Forced, s.Error})
}
