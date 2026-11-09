package bittorrent

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	fibbt "github.com/lesismal/fib/bittorrent"
	"github.com/lesismal/fib/bittorrent/metainfo"

	"github.com/lesismal/fit/config"
)

// The client's state, in data_dir:
//
//	torrents.json          the list, in queue order: each torrent's source,
//	                       directory, category, own limits, file
//	                       priorities, whether it is paused and whether
//	                       it completed; and the alternative rates' mode
//	torrents/<id>.torrent  its .torrent file, once its info is known
//	resume/<id>.resume     its resume data (fib's Torrent.ResumeData)
//
// Each file is written to a temporary file, synced and renamed into place,
// so that a crash leaves the old file or the new one whole. They are read
// and written by Apply, Stop, the role's API and its saver, never on a fib
// worker.

// StateVersion is the version of torrents.json's format; a file of a later
// version is refused rather than misread.
const StateVersion = 1

// The bounds of what the state files may hold when read back.
const (
	// MaxStateBytes bounds torrents.json.
	MaxStateBytes = 64 << 20
	// MaxResumeBytes bounds a torrent's resume data: its bitfield and a
	// size and time for each file.
	MaxResumeBytes = 64 << 20
	// MaxTorrentBytes bounds a .torrent file, fib's metainfo bound.
	MaxTorrentBytes = metainfo.MaxFileSize
	// MaxMagnetBytes bounds a magnet link.
	MaxMagnetBytes = 64 << 10
)

const (
	stateFile  = "torrents.json"
	torrentDir = "torrents"
	resumeDir  = "resume"
)

// stateDoc is torrents.json.
type stateDoc struct {
	Version int       `json:"version"`
	Saved   time.Time `json:"saved"`
	// AltSpeed is the API's say on the alternative rates: "on", "off", or
	// "" for their schedules.
	AltSpeed string       `json:"alt_speed,omitempty"`
	Torrents []stateEntry `json:"torrents"`
}

// stateEntry is a torrent in torrents.json.
type stateEntry struct {
	// ID is the info hash the client knows the torrent by, in hex: the v1
	// hash, or the v2 hash truncated to 20 bytes for a v2-only torrent.
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	// Magnet is the link the torrent was added from, if it was; its
	// .torrent file, once written, is used in its place.
	Magnet     string    `json:"magnet,omitempty"`
	Dir        string    `json:"dir"`
	Priorities []int     `json:"priorities,omitempty"`
	Sequential bool      `json:"sequential,omitempty"`
	Paused     bool      `json:"paused,omitempty"`
	Added      time.Time `json:"added"`
	// Temporary marks a torrent bt_stream added from a magnet link on the
	// fly: removed with its data after its last stream, and at start.
	Temporary bool `json:"temporary,omitempty"`
	// Category is the category it was added in or moved to, if any.
	Category string `json:"category,omitempty"`
	// Limits are its own limits, over its category's.
	Limits *TorrentLimits `json:"limits,omitempty"`
	// Completed says it had every piece it wants when last looked at, so
	// that a restart does not take it for a new completion.
	Completed bool `json:"completed,omitempty"`
	// Peers are the addresses it was added with (AddRequest.Peers), given
	// to it again at each start: a cluster's distribution names its peers
	// so, a private torrent having no other way to find them (I206).
	Peers []string `json:"peers,omitempty"`
}

// maxKeptPeers and maxPeerLen bound the peers a torrent keeps in the
// state file.
const (
	maxKeptPeers = 128
	maxPeerLen   = 256
)

// keptPeers is peers as the state file keeps them: at most maxKeptPeers,
// each at most maxPeerLen bytes, the rest left out.
func keptPeers(peers []string) []string {
	var out []string
	for _, p := range peers {
		if len(out) == maxKeptPeers {
			break
		}
		if p != "" && len(p) <= maxPeerLen {
			out = append(out, p)
		}
	}
	return out
}

// record is a torrent of the role: what the state keeps of it, and the
// fib torrent while a client runs it.
type record struct {
	id         string
	name       string
	magnet     string
	dir        string
	priorities []int
	sequential bool
	paused     bool
	added      time.Time
	temporary  bool
	category   string
	limits     TorrentLimits
	completed  bool
	peers      []string // the addresses it was added with, kept

	// held is why the role keeps the torrent paused, when it does, beside
	// paused (the API's or its seed limit's): holdStart until the role's
	// next look at the queue, holdQueue past max_active_downloads or
	// max_active_seeds, holdDisk short of disk space, with holdMsg.
	held    hold
	holdMsg string
	// applied are the fib limits last given its torrent.
	applied fibbt.TorrentLimits

	// mi is the torrent's metainfo, nil for a magnet link's until its info
	// arrives; torrentSaved says its .torrent file is written.
	mi           *metainfo.MetaInfo
	torrentSaved bool
	// resume is the resume data last saved or read, given to the next
	// client that adds the torrent; savedGen is the torrent's ResumeGen
	// then, and genKnown says it was taken from the running torrent.
	resume   []byte
	savedGen uint64
	genKnown bool
	// resumeDirty asks the next save for resume data whatever ResumeGen
	// says, after a pause or a stop.
	resumeDirty bool

	// err is why the torrent could not be added to the client: it stays
	// listed, and saved, until removed.
	err error
	t   *fibbt.Torrent

	// lastState is the state the last look published (events.go); acctT
	// is the fib torrent whose counts acctDown, acctUp and acctFails are,
	// as the role's totals took them last (metrics.go).
	lastState                   string
	acctT                       *fibbt.Torrent
	acctDown, acctUp, acctFails int64
}

func (r *record) entry() stateEntry {
	e := stateEntry{ID: r.id, Name: r.name, Magnet: r.magnet, Dir: r.dir, Priorities: r.priorities,
		Sequential: r.sequential, Paused: r.paused, Added: r.added, Category: r.category, Completed: r.completed,
		Temporary: r.temporary, Peers: r.peers}
	if !r.limits.IsZero() {
		l := r.limits
		e.Limits = &l
	}
	return e
}

// idOf returns the ID of a torrent of info hashes v1 and v2.
func idOf(v1 metainfo.InfoHash, v2 metainfo.InfoHashV2) string {
	if !v1.IsZero() {
		return hex.EncodeToString(v1[:])
	}
	t := v2.Truncate()
	return hex.EncodeToString(t[:])
}

// parseID returns the 20 bytes of an ID, which must be 40 lowercase hex
// digits.
func parseID(id string) ([20]byte, bool) {
	var b [20]byte
	if len(id) != 40 {
		return b, false
	}
	for _, c := range id {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return b, false
		}
	}
	_, err := hex.Decode(b[:], []byte(id))
	return b, err == nil
}

// ErrState is the error of a state that cannot be read: the role does not
// start on it rather than overwrite it.
var ErrState = errors.New("bittorrent: the state cannot be read")

// loadState reads the state in dir: the records, in the order of the list,
// none when there is no state yet, and the alternative rates' mode. The temporary files a crash left are
// removed. A list that does not read, is larger than MaxStateBytes, is of
// a later version or lists more than MaxBTTorrents torrents is an error
// wrapping ErrState. A torrent whose own files do not read is kept with
// its error, so that it stays listed until removed.
func loadState(dir string) ([]*record, string, error) {
	for _, d := range []string{dir, filepath.Join(dir, torrentDir), filepath.Join(dir, resumeDir)} {
		if olds, _ := filepath.Glob(filepath.Join(d, ".*.tmp-*")); len(olds) > 0 {
			for _, o := range olds {
				_ = os.Remove(o)
			}
		}
	}
	path := filepath.Join(dir, stateFile)
	data, err := readBounded(path, MaxStateBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("%w: %s: %w", ErrState, path, err)
	}
	var doc stateDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, "", fmt.Errorf("%w: %s: %w", ErrState, path, err)
	}
	if doc.Version < 1 || doc.Version > StateVersion {
		return nil, "", fmt.Errorf("%w: %s is of version %d, which this fit does not read", ErrState, path, doc.Version)
	}
	if len(doc.Torrents) > config.MaxBTTorrents {
		return nil, "", fmt.Errorf("%w: %s lists %d torrents, more than %d", ErrState, path, len(doc.Torrents), config.MaxBTTorrents)
	}
	switch doc.AltSpeed {
	case "", AltSpeedOn, AltSpeedOff:
	default:
		return nil, "", fmt.Errorf("%w: %s: alt_speed is %q, not on, off or empty", ErrState, path, doc.AltSpeed)
	}
	recs := make([]*record, 0, len(doc.Torrents))
	seen := map[string]bool{}
	for i, e := range doc.Torrents {
		if _, ok := parseID(e.ID); !ok {
			return nil, "", fmt.Errorf("%w: %s: torrent %d has the ID %q, not 40 lowercase hex digits", ErrState, path, i, e.ID)
		}
		if seen[e.ID] {
			return nil, "", fmt.Errorf("%w: %s lists %s twice", ErrState, path, e.ID)
		}
		seen[e.ID] = true
		if !filepath.IsAbs(e.Dir) {
			return nil, "", fmt.Errorf("%w: %s: torrent %s has the directory %q, which is not absolute", ErrState, path, e.ID, e.Dir)
		}
		rec := &record{id: e.ID, name: e.Name, magnet: e.Magnet, dir: e.Dir, priorities: e.Priorities,
			sequential: e.Sequential, paused: e.Paused, added: e.Added, category: e.Category, completed: e.Completed,
			temporary: e.Temporary, peers: keptPeers(e.Peers)}
		if e.Limits != nil {
			rec.limits = *e.Limits
		}
		rec.mi, rec.err = loadTorrent(dir, e.ID)
		rec.torrentSaved = rec.mi != nil
		if rec.mi == nil && rec.err == nil {
			if e.Magnet == "" {
				rec.err = errors.New("neither its .torrent file nor a magnet link is kept")
			} else if len(e.Magnet) > MaxMagnetBytes {
				rec.err = fmt.Errorf("its magnet link is longer than %d bytes", MaxMagnetBytes)
			}
		}
		if rec.err == nil {
			// Resume data that does not read is left out: the torrent is
			// checked in full, as fib does with resume data it cannot use.
			rec.resume, _ = readBounded(filepath.Join(dir, resumeDir, e.ID+".resume"), MaxResumeBytes)
		}
		recs = append(recs, rec)
	}
	return recs, doc.AltSpeed, nil
}

// loadTorrent reads the .torrent file of id, nil when there is none.
func loadTorrent(dir, id string) (*metainfo.MetaInfo, error) {
	data, err := readBounded(filepath.Join(dir, torrentDir, id+".torrent"), MaxTorrentBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("its .torrent file: %w", err)
	}
	mi, err := metainfo.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("its .torrent file: %w", err)
	}
	if got := idOf(mi.Info.InfoHashV1(), mi.Info.InfoHashV2()); got != id {
		return nil, fmt.Errorf("its .torrent file is of torrent %s", got)
	}
	return mi, nil
}

// readBounded reads the file at path, refusing one larger than max.
func readBounded(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Size() > max {
		return nil, fmt.Errorf("%d bytes, larger than %d", fi.Size(), max)
	}
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("larger than %d bytes", max)
	}
	return data, nil
}

// saveList writes torrents.json with recs and the alternative rates' mode.
func saveList(dir string, recs []*record, altSpeed string) error {
	doc := stateDoc{Version: StateVersion, Saved: time.Now().UTC(), AltSpeed: altSpeed, Torrents: make([]stateEntry, 0, len(recs))}
	for _, r := range recs {
		doc.Torrents = append(doc.Torrents, r.entry())
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(dir, stateFile, append(data, '\n'))
}

// writeAtomic writes data as dir/name: to a temporary file, synced, then
// renamed over the last, so that a crash leaves the old file or the new
// one whole.
func writeAtomic(dir, name string, data []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+name+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, filepath.Join(dir, name))
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// The rename is made durable by syncing the directory, where the
	// system can; Windows cannot, and its rename is durable as it is.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// removeFiles removes the .torrent file and the resume data of id.
func removeFiles(dir, id string) error {
	var errs []error
	for _, p := range []string{filepath.Join(dir, torrentDir, id+".torrent"), filepath.Join(dir, resumeDir, id+".resume")} {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
