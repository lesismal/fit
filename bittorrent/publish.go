package bittorrent

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	fibbt "github.com/lesismal/fib/bittorrent"
	"github.com/lesismal/fib/bittorrent/metainfo"
	"github.com/lesismal/fib/filewatch"

	"github.com/lesismal/fit/config"
)

// Publishing (I202): each publish entry of the client's config is a
// directory built into hybrid torrents, a torrent of each file or one of
// the whole directory, seeded where its files are, through storage that
// never writes, and served by the bt_publish handler.
//
// An entry runs a goroutine of its own that walks the directory every
// check_interval and max_hashing goroutines that build torrents, never a
// fib worker. What they find is committed under the role's r.mu, which
// guards every entry's state; the handler reads an immutable snapshot of
// each entry, swapped atomically, and takes no lock of the role's.
//
// An entry's torrents are kept in data_dir/publish/<name>/: index.json,
// each torrent's files and their stamps (size, modification and status
// change times, inode), <id>.torrent and <id>.resume. A restart takes the
// torrents whose files still have their stamps from there, with their
// resume data, so that it neither builds nor checks them again.

// publishDir is the directory, under data_dir, of the entries' state.
const publishDir = "publish"

// maxPublishReport bounds the problems an entry's status lists.
const maxPublishReport = 20

// PublishStatus is a publish entry as the index reports it.
type PublishStatus struct {
	Name    string `json:"name"`
	Dir     string `json:"dir"`
	Mode    string `json:"mode"`
	WebSeed string `json:"web_seed,omitempty"`
	// Torrents are the entry's torrents, by path.
	Torrents []PublishedTorrent `json:"torrents"`
	// Pending counts the files found that have no torrent yet: waiting
	// for them to settle, or being built.
	Pending int `json:"pending"`
	// Scanned is when the directory was last walked; zero before the
	// first walk ends.
	Scanned time.Time `json:"scanned,omitzero"`
	// Problems are what the last walk and builds left out, and why, at
	// most 20, the rest counted in Dropped.
	Problems []string `json:"problems,omitempty"`
	Dropped  int      `json:"problems_dropped,omitempty"`
	// Error is why the entry does not publish, as a directory that cannot
	// be read.
	Error string `json:"error,omitempty"`
}

// PublishedTorrent is a torrent of a publish entry.
type PublishedTorrent struct {
	ID string `json:"id"`
	// Path is the file's path under dir in mode per_file, "" in mode whole_dir.
	Path       string    `json:"path"`
	Name       string    `json:"name"`
	Size       int64     `json:"size"`
	Files      int       `json:"files"`
	InfoHash   string    `json:"info_hash"`
	InfoHashV2 string    `json:"info_hash_v2"`
	Magnet     string    `json:"magnet"`
	Built      time.Time `json:"built"`
	// State is the torrent's state in the client: checking, seeding, or
	// stopped while no client runs it.
	State string `json:"state"`
	// Error is why it is not seeded, as another torrent the client has of
	// the same info hash.
	Error string `json:"error,omitempty"`
	// TorrentURL and MagnetURL are where the bt_publish handler serves the
	// .torrent file and the magnet link, relative to its index; set by
	// the handler.
	TorrentURL string `json:"torrent_url,omitempty"`
	MagnetURL  string `json:"magnet_url,omitempty"`
}

// publisher is the role's publish entries. Its fields but snaps are
// guarded by the role's r.mu.
type publisher struct {
	r       *Role
	entries map[string]*pubEntry
	wg      sync.WaitGroup
	// snaps are the entries' snapshots by name, for the handler.
	snaps atomic.Pointer[map[string]*pubSnap]
	// builds counts the torrents built, for tests.
	builds atomic.Int64
	// readHook, when set, runs before each read of a build, for tests.
	readHook func()
	// running says a client runs: set when one starts, cleared when it is
	// closed.
	running atomic.Bool
}

// ClientRunning reports whether the role runs a client, without a lock:
// the handler asks on a fib worker.
func (r *Role) ClientRunning() bool { return r.pub.running.Load() }

// pubSnap is an entry as the handler sees it: never changed once stored.
type pubSnap struct {
	status PublishStatus
	items  []*pubSnapItem
	byID   map[string]*pubSnapItem
}

type pubSnapItem struct {
	pt      PublishedTorrent
	torrent []byte
	t       *fibbt.Torrent
}

// pubEntry is a running publish entry.
type pubEntry struct {
	p        *publisher
	cfg      config.BitTorrentPublish
	name     string
	dir      string
	stateDir string
	// key is what of the entry its torrents depend on: index.json kept
	// under another is not used. ekey is entryKey's.
	key      string
	ekey     string
	include  []pattern
	exclude  []pattern
	webSeed  string
	trackers [][]string

	// ctx is done once the entry stops, which ends its walks and its
	// builds (Builder.BuildContext) at their next step; stop wakes the
	// walker.
	ctx    context.Context
	cancel context.CancelFunc
	stop   chan struct{}
	jobs   chan *pubJob

	// What follows is guarded by r.mu.
	stopped  bool
	items    map[string]*pubItem
	building map[string]bool
	// seen is the last walk's files by path, which tells a file that has
	// settled.
	seen map[string]filewatch.Stamp
	// failed are the builds that failed, by key, with the stamps of their
	// files then: built again only once those change.
	failed   map[string]pubFailure
	walked   bool
	pending  int
	problems []string
	dropped  int
	scanErr  string
	scanned  time.Time
	dirty    bool
}

// pubItem is a torrent of an entry.
type pubItem struct {
	key   string // the file's path in mode per_file, "" in mode whole_dir
	id    string
	files []pubFile
	size  int64
	built time.Time

	mi          *metainfo.MetaInfo
	raw         []byte
	magnet      string
	resume      []byte
	resumeSaved bool
	// seed says it was built here and has no resume data yet: it is
	// added as complete (AddOptions.Seed), its files having just been
	// hashed, so that they are not hashed a second time.
	seed bool

	t   *fibbt.Torrent
	err error
	// dupOf is the key of the entry's item of the same torrent, which
	// seeds it; this one waits, with its error, until that one goes.
	dupOf string
}

// pubFile is a file of a torrent and its stamp when it was built.
type pubFile struct {
	Path  string          `json:"path"`
	Stamp filewatch.Stamp `json:"stamp"`
}

// pubFailure is a build that failed.
type pubFailure struct {
	stamps string
	err    string
}

// pubJob is a torrent to build.
type pubJob struct {
	key   string
	files []pubFile
}

func newPublisher(r *Role) *publisher {
	p := &publisher{r: r, entries: map[string]*pubEntry{}}
	empty := map[string]*pubSnap{}
	p.snaps.Store(&empty)
	return p
}

// entryKey is what of cfg the entry depends on: a change of it makes a new
// entry, which takes the torrents of the old from data_dir.
func entryKey(cfg *config.BitTorrentPublish, dataDir string) string {
	return fmt.Sprintf("%q %q %q %q %q %v %d %q %q %d %d %d %d %q", cfg.Name, cfg.Dir, cfg.PublishMode(), cfg.WebSeed,
		cfg.Announce, cfg.Private, cfg.PieceLength, cfg.Include, cfg.Exclude, cfg.Interval(), cfg.Files(), cfg.TotalBytes(),
		cfg.Hashing(), filepath.Clean(dataDir))
}

// buildKey is what of an entry its torrents' bytes depend on, kept in
// index.json.
func buildKey(cfg *config.BitTorrentPublish, dir string) string {
	return fmt.Sprintf("v1 %q %q %q %q %v %d", dir, cfg.PublishMode(), cfg.WebSeed, cfg.Announce, cfg.Private, cfg.PieceLength)
}

// apply moves the entries to cfgs: those removed or changed stop, their
// torrents taken out of the client, and those new or changed start. r.mu
// is held.
func (p *publisher) apply(cfgs []config.BitTorrentPublish) {
	r := p.r
	want := map[string]*config.BitTorrentPublish{}
	for i := range cfgs {
		want[cfgs[i].Name] = &cfgs[i]
	}
	for name, e := range p.entries {
		cfg := want[name]
		if cfg != nil && entryKey(cfg, r.dataDir) == e.ekey {
			continue
		}
		if cfg != nil {
			// Started again: the new entry takes what this one keeps.
			e.flush()
		}
		e.halt()
		delete(p.entries, name)
		if cfg == nil {
			// Its state goes with it: what it built is fit's, never the
			// published files.
			if err := removeAllUnder(filepath.Join(r.dataDir, publishDir), name); err != nil && r.log != nil {
				r.log.Warnf("publish %s: removing its state: %v", name, err)
			}
		}
	}
	for name, cfg := range want {
		if p.entries[name] != nil {
			continue
		}
		e, err := p.newEntry(cfg)
		if err != nil {
			p.storeSnap(name, &pubSnap{status: PublishStatus{Name: name, Dir: cfg.Dir, Mode: cfg.PublishMode(), Error: err.Error()}})
			if r.log != nil {
				r.log.Errorf("publish %s: %v", name, err)
			}
			continue
		}
		p.entries[name] = e
		e.publish()
		p.wg.Add(1 + e.cfg.Hashing())
		go e.walkLoop()
		for range e.cfg.Hashing() {
			go e.buildLoop()
		}
	}
	// The snapshots of entries no longer configured go.
	snaps := maps.Clone(*p.snaps.Load())
	for name := range snaps {
		if want[name] == nil {
			delete(snaps, name)
		}
	}
	p.snaps.Store(&snaps)
}

// newEntry makes the entry of cfg, not yet started.
func (p *publisher) newEntry(cfg *config.BitTorrentPublish) (*pubEntry, error) {
	dir, err := filepath.Abs(cfg.Dir)
	if err != nil {
		return nil, err
	}
	e := &pubEntry{p: p, cfg: *cfg, name: cfg.Name, dir: filepath.Clean(dir),
		stateDir: filepath.Join(p.r.dataDir, publishDir, cfg.Name), stop: make(chan struct{}),
		jobs: make(chan *pubJob, cfg.Files()+1), items: map[string]*pubItem{}, building: map[string]bool{},
		seen: map[string]filewatch.Stamp{}, failed: map[string]pubFailure{}}
	e.ctx, e.cancel = context.WithCancel(context.Background())
	e.key, e.ekey = buildKey(cfg, e.dir), entryKey(cfg, p.r.dataDir)
	for _, s := range cfg.Include {
		e.include = append(e.include, newPattern(s))
	}
	for _, s := range cfg.Exclude {
		e.exclude = append(e.exclude, newPattern(s))
	}
	if cfg.WebSeed != "" {
		e.webSeed = cfg.WebSeed
		if !strings.HasSuffix(e.webSeed, "/") {
			e.webSeed += "/"
		}
	}
	for _, a := range cfg.Announce {
		e.trackers = append(e.trackers, []string{a})
	}
	return e, nil
}

// halt stops the entry: its goroutines end at their next step, and its
// torrents leave the client. r.mu is held.
func (e *pubEntry) halt() {
	if e.stopped {
		return
	}
	e.stopped = true
	e.cancel()
	close(e.stop)
	for _, it := range e.items {
		e.detachItem(it)
	}
}

// stopAll stops every entry and waits for their goroutines. r.mu is not
// held: they take it.
func (p *publisher) stopAll() {
	p.r.mu.Lock()
	for _, e := range p.entries {
		e.flush()
		e.halt()
	}
	p.entries = map[string]*pubEntry{}
	p.r.mu.Unlock()
	p.wg.Wait()
}

// flush saves the resume data of the torrents that have every piece and
// the index, if they changed. r.mu is held.
func (e *pubEntry) flush() {
	for _, it := range e.items {
		e.saveResume(it)
	}
	if e.dirty {
		e.saveIndex()
	}
}

// attachAll adds every entry's torrents to the client that just started.
// r.mu is held.
func (p *publisher) attachAll() {
	p.running.Store(p.r.client != nil)
	for _, e := range p.entries {
		for _, it := range e.sortedItems() {
			e.attach(it)
		}
		e.publish()
	}
}

// detachAll drops every entry's torrents of the client that was closed,
// first saving the resume data of those that have every piece. r.mu is
// held.
func (p *publisher) detachAll() {
	p.running.Store(false)
	for _, e := range p.entries {
		for _, it := range e.items {
			e.saveResume(it)
			it.t = nil
		}
		if e.dirty {
			e.saveIndex()
		}
		e.publish()
	}
}

// sortedItems returns the items by path.
func (e *pubEntry) sortedItems() []*pubItem {
	items := slices.Collect(maps.Values(e.items))
	slices.SortFunc(items, func(a, b *pubItem) int { return strings.Compare(a.key, b.key) })
	return items
}

// storeSnap stores the snapshot of the entry name. r.mu is held.
func (p *publisher) storeSnap(name string, s *pubSnap) {
	snaps := maps.Clone(*p.snaps.Load())
	snaps[name] = s
	p.snaps.Store(&snaps)
}

// publish stores the entry's snapshot. r.mu is held.
func (e *pubEntry) publish() {
	if e.stopped {
		return
	}
	s := &pubSnap{byID: map[string]*pubSnapItem{}, status: PublishStatus{Name: e.name, Dir: e.dir,
		Mode: e.cfg.PublishMode(), WebSeed: e.cfg.WebSeed, Torrents: []PublishedTorrent{}, Pending: e.pending,
		Scanned: e.scanned, Problems: slices.Clone(e.problems), Dropped: e.dropped, Error: e.scanErr}}
	for _, it := range e.sortedItems() {
		si := &pubSnapItem{t: it.t, torrent: it.raw, pt: PublishedTorrent{ID: it.id, Path: it.key, Name: it.mi.Info.Name,
			Size: it.size, Files: len(it.files), Magnet: it.magnet, Built: it.built}}
		if it.mi.Info.HasV1() {
			si.pt.InfoHash = it.mi.Info.InfoHashV1().String()
		}
		if it.mi.Info.HasV2() {
			si.pt.InfoHashV2 = it.mi.Info.InfoHashV2().String()
		}
		if it.err != nil {
			si.pt.Error = it.err.Error()
		}
		s.items = append(s.items, si)
		if it.err == nil && s.byID[it.id] == nil {
			s.byID[it.id] = si
		}
	}
	e.p.storeSnap(e.name, s)
}

// Publishing reports the publish entries named, or all of them for none,
// in the order of names, or by name; a name the config has not is
// reported with its error. It takes no lock of the role's and does not
// block: the handler calls it on a fib worker.
func (r *Role) Publishing(names []string) []PublishStatus {
	snaps := *r.pub.snaps.Load()
	if len(names) == 0 {
		names = slices.Sorted(maps.Keys(snaps))
	}
	out := make([]PublishStatus, 0, len(names))
	for _, name := range names {
		s := snaps[name]
		if s == nil {
			out = append(out, PublishStatus{Name: name, Torrents: []PublishedTorrent{}, Error: "the client has no publish entry of this name"})
			continue
		}
		st := s.status
		st.Torrents = make([]PublishedTorrent, len(s.items))
		for i, it := range s.items {
			st.Torrents[i] = it.pt
			st.Torrents[i].State = itemState(it)
		}
		out = append(out, st)
	}
	return out
}

func itemState(it *pubSnapItem) string {
	switch {
	case it.pt.Error != "":
		return "error"
	case it.t == nil:
		return "stopped"
	}
	return it.t.State().String()
}

// PublishedTorrent returns the .torrent file and the magnet link of the
// torrent id of the publish entries named, or of any for none. As
// Publishing, it takes no lock of the role's.
func (r *Role) PublishedTorrent(names []string, id string) (torrent []byte, name, magnet string, ok bool) {
	snaps := *r.pub.snaps.Load()
	look := func(s *pubSnap) bool {
		if s == nil {
			return false
		}
		if it := s.byID[id]; it != nil {
			torrent, name, magnet, ok = it.torrent, it.pt.Name, it.pt.Magnet, true
		}
		return ok
	}
	if len(names) == 0 {
		for _, n := range slices.Sorted(maps.Keys(snaps)) {
			if look(snaps[n]) {
				return
			}
		}
		return
	}
	for _, n := range names {
		if look(snaps[n]) {
			return
		}
	}
	return
}

// report adds a problem to the entry's status. r.mu is held.
func (e *pubEntry) report(format string, args ...any) {
	if len(e.problems) >= maxPublishReport {
		e.dropped++
		return
	}
	e.problems = append(e.problems, fmt.Sprintf(format, args...))
}

// itemWebSeed returns the web seed of a torrent of files.
func (e *pubEntry) itemWebSeed(key string) string {
	if e.webSeed == "" {
		return ""
	}
	if e.cfg.PublishMode() == config.BTPublishDir {
		// Clients ask <url>/<name>/<path>: the URL's parent, the torrent
		// being named as the directory, which the config checked.
		base := strings.TrimSuffix(e.webSeed, "/")
		return base[:strings.LastIndex(base, "/")+1]
	}
	parts := strings.Split(key, "/")
	for i, s := range parts {
		parts[i] = escapePathSegment(s)
	}
	return e.webSeed + strings.Join(parts, "/")
}

// storageDir is the directory a torrent's storage is below: the file's in
// mode per_file, whose torrent is named as the file, and dir's parent in
// mode whole_dir, whose torrent is named as dir.
func (e *pubEntry) storageDir(key string) string {
	if e.cfg.PublishMode() == config.BTPublishDir {
		return filepath.Dir(e.dir)
	}
	return filepath.Join(e.dir, filepath.FromSlash(path.Dir(key)))
}

// attach adds it to the client, seeding from its files through storage
// that never writes, with its resume data. r.mu is held.
func (e *pubEntry) attach(it *pubItem) {
	r := e.p.r
	if it.t != nil || r.client == nil || e.stopped || it.dupOf != "" {
		return
	}
	if it.err != nil && !errors.Is(it.err, ErrExists) {
		return
	}
	o := readOnlyOpener(fibbt.NewFileStorage(e.storageDir(it.key), fibbt.FileStorageOptions{}))
	if r.wrap != nil {
		o = r.wrap(o)
	}
	// A published torrent seeds for ever, whatever the client's seed
	// limits say. One just built is added as complete: the Builder has
	// hashed its files, and each piece is checked when first served.
	// Seed and Resume exclude each other (fib's ErrSeedResume), and the
	// resume data saved once it has every piece keeps the pieces not
	// checked yet as such.
	opts := &fibbt.AddOptions{Storage: o, Limits: fibbt.TorrentLimits{SeedRatio: -1, SeedTime: -1}}
	if len(it.resume) > 0 {
		opts.Resume = it.resume
	} else {
		opts.Seed = it.seed
	}
	t, err := r.client.AddTorrent(it.mi, opts)
	if err != nil {
		if errors.Is(err, fibbt.ErrTorrentExists) {
			it.err = fmt.Errorf("%w: the client has a torrent of this info hash already, added to it or published by another entry", ErrExists)
		} else {
			it.err = err
		}
		return
	}
	it.t, it.err = t, nil
}

// detachItem takes it out of the client, never deleting its files. r.mu
// is held.
func (e *pubEntry) detachItem(it *pubItem) {
	if it.t == nil {
		return
	}
	if err := it.t.Remove(false); err != nil && e.p.r.log != nil {
		e.p.r.log.Warnf("publish %s: removing torrent %s: %v", e.name, it.id, err)
	}
	it.t = nil
}

// dropItem takes it out of the client and of the entry, with its files in
// the entry's state. r.mu is held.
func (e *pubEntry) dropItem(it *pubItem) {
	e.detachItem(it)
	delete(e.items, it.key)
	if !e.idInUse(it.id) {
		_ = removeItemFiles(e.stateDir, it.id)
	}
	e.dirty = true
}

// idInUse reports whether an item of the entry has the torrent id.
func (e *pubEntry) idInUse(id string) bool {
	for _, it := range e.items {
		if it.id == id {
			return true
		}
	}
	return false
}

// saveResume saves the resume data of it once it has every piece, which
// it keeps from then on: its storage never writes, and a change of its
// files replaces it. r.mu is held.
func (e *pubEntry) saveResume(it *pubItem) {
	if it.t == nil || it.resumeSaved || !it.t.HaveAll() {
		return
	}
	data, err := it.t.ResumeData()
	if err != nil {
		return
	}
	if err := writeAtomic(e.stateDir, it.id+".resume", data); err != nil {
		e.report("saving the resume data of %s: %v", it.id, err)
		return
	}
	it.resume, it.resumeSaved, it.seed = data, true, false
}
