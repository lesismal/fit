package bittorrent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	fibbt "github.com/lesismal/fib/bittorrent"
	"github.com/lesismal/fib/bittorrent/bencode"
	"github.com/lesismal/fib/bittorrent/metainfo"
	"github.com/lesismal/fib/filewatch"

	"github.com/lesismal/fit/config"
)

// The walks of an entry's directory, the builds of its torrents, and its
// index.json. Everything here runs on the entry's own goroutines, never
// on a fib worker; what changes the entry is done under r.mu.

// pubIndexVersion is index.json's format.
const pubIndexVersion = 1

// MaxPublishIndexBytes bounds an entry's index.json when read back.
const MaxPublishIndexBytes = 64 << 20

// walkFile is a file a walk found.
type walkFile struct {
	rel   string
	stamp filewatch.Stamp
}

// walkResult is what a walk found.
type walkResult struct {
	files    []walkFile
	problems []string
	// incomplete says part of the directory could not be read: no torrent
	// is stopped for a file the walk did not find then.
	incomplete bool
	rootErr    error
	// refuse is why, in mode whole_dir, the directory is not published.
	refuse string
}

// walkLoop restores the entry's torrents from its state, then walks its
// directory every check_interval until it stops.
func (e *pubEntry) walkLoop() {
	defer e.p.wg.Done()
	e.restore()
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-e.stop:
			return
		case <-timer.C:
		}
		res := e.walk()
		if e.cancel.Load() {
			return
		}
		r := e.p.r
		r.mu.Lock()
		if e.stopped {
			r.mu.Unlock()
			return
		}
		e.reconcile(res)
		r.mu.Unlock()
		timer.Reset(e.cfg.Interval())
	}
}

// walk lists the files of the entry's directory that it publishes, in
// path order, within its bounds.
func (e *pubEntry) walk() walkResult {
	var res walkResult
	root, err := filepath.EvalSymlinks(e.dir)
	if err == nil {
		var fi os.FileInfo
		if fi, err = os.Stat(root); err == nil && !fi.IsDir() {
			err = fmt.Errorf("%s is not a directory", e.dir)
		}
	}
	if err != nil {
		res.rootErr = err
		return res
	}
	maxFiles, maxBytes := e.cfg.Files(), e.cfg.TotalBytes()
	maxVisits := 10*maxFiles + 10000
	files := e.cfg.PublishMode() == config.BTPublishFiles
	var visits, overFiles, overBytes int
	var total int64
	problem := func(format string, args ...any) {
		if len(res.problems) < maxPublishReport {
			res.problems = append(res.problems, fmt.Sprintf(format, args...))
		}
	}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if e.cancel.Load() {
			return errPublishStopped
		}
		if p == root {
			return err
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if err != nil {
			res.incomplete = true
			problem("%s: %v", rel, err)
			return nil
		}
		if visits++; visits > maxVisits {
			res.incomplete = true
			problem("the walk stopped after %d entries", maxVisits)
			return fs.SkipAll
		}
		name := d.Name()
		switch {
		case strings.HasPrefix(name, "."):
			// Hidden, as static files hide them by default.
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		case d.Type()&fs.ModeSymlink != 0:
			// Never followed: a link could publish what lies outside dir.
			return nil
		case d.IsDir():
			if anyMatch(e.exclude, rel) {
				return fs.SkipDir
			}
			return nil
		case !d.Type().IsRegular():
			return nil
		}
		if len(e.include) > 0 && !anyMatch(e.include, rel) || anyMatch(e.exclude, rel) {
			return nil
		}
		comps := strings.Split(rel, "/")
		if !utf8.ValidString(rel) || fibbt.SanitizePath(comps) != filepath.FromSlash(rel) {
			problem("%s: a name the client's storage would change, which is left out", rel)
			return nil
		}
		info, err := d.Info()
		if err != nil {
			res.incomplete = true
			problem("%s: %v", rel, err)
			return nil
		}
		if files && info.Size() == 0 {
			problem("%s: empty, which has no torrent", rel)
			return nil
		}
		if len(res.files) >= maxFiles {
			overFiles++
			return nil
		}
		if total+info.Size() > maxBytes {
			overBytes++
			return nil
		}
		total += info.Size()
		res.files = append(res.files, walkFile{rel: rel, stamp: filewatch.StampOf(info)})
		return nil
	})
	if errors.Is(err, errPublishStopped) {
		return res
	}
	if err != nil {
		res.rootErr = err
		return res
	}
	if overFiles > 0 {
		problem("%d files over max_files (%d) left out", overFiles, maxFiles)
	}
	if overBytes > 0 {
		problem("%d files over max_total_bytes (%d) left out", overBytes, maxBytes)
	}
	if !files {
		switch {
		case overFiles > 0 || overBytes > 0:
			res.refuse = "the directory is past max_files or max_total_bytes, and is not published"
		case res.incomplete:
			res.refuse = "the directory could not be read whole, and is not published as it is"
		case total == 0:
			res.refuse = "the directory has no data to publish"
		}
	}
	return res
}

// settled reports whether a file's stamp has held for check_interval: as
// the last walk saw it, or not written for that long.
func (e *pubEntry) settled(f walkFile, now time.Time) bool {
	if s, ok := e.seen[f.rel]; ok && s == f.stamp {
		return true
	}
	// The status change time, which no one sets back, or the
	// modification time where there is none.
	t := max(f.stamp.Change, f.stamp.ModTime)
	return now.Sub(time.Unix(0, t)) >= e.cfg.Interval()
}

// stampKey is a list of files' stamps, as a failed build remembers them.
func stampKey(files []pubFile) string {
	var b strings.Builder
	for _, f := range files {
		fmt.Fprintf(&b, "%s %v\n", f.Path, f.Stamp)
	}
	return b.String()
}

// reconcile takes what a walk found: the torrents whose files changed or
// went stop, and the files that have no torrent and settled are built.
// r.mu is held.
func (e *pubEntry) reconcile(res walkResult) {
	now := time.Now()
	e.problems, e.dropped = nil, 0
	for _, p := range res.problems {
		e.report("%s", p)
	}
	if res.rootErr != nil {
		// Nothing was learned: the torrents stay as they are.
		e.scanErr = res.rootErr.Error()
		e.scanned = now
		e.publish()
		return
	}
	e.scanErr = ""
	cur := make(map[string]filewatch.Stamp, len(res.files))
	for _, f := range res.files {
		cur[f.rel] = f.stamp
	}
	var jobs []*pubJob
	e.pending = 0
	if e.cfg.PublishMode() == config.BTPublishFiles {
		for key, it := range e.items {
			s, ok := cur[key]
			if (!ok && !res.incomplete) || ok && s != it.files[0].Stamp {
				e.dropItem(it)
			}
		}
		for _, f := range res.files {
			if e.items[f.rel] != nil {
				continue
			}
			files := []pubFile{{Path: f.rel, Stamp: f.stamp}}
			if fl, ok := e.failed[f.rel]; ok {
				if fl.stamps == stampKey(files) {
					e.report("%s: %s", f.rel, fl.err)
					continue
				}
				delete(e.failed, f.rel)
			}
			e.pending++
			if !e.building[f.rel] && e.settled(f, now) {
				jobs = append(jobs, &pubJob{key: f.rel, files: files})
			}
		}
	} else {
		files := make([]pubFile, len(res.files))
		for i, f := range res.files {
			files[i] = pubFile{Path: f.rel, Stamp: f.stamp}
		}
		if it := e.items[""]; it != nil && !res.incomplete && !slices.Equal(it.files, files) {
			e.dropItem(it)
		}
		switch {
		case res.refuse != "":
			e.report("%s", res.refuse)
		case e.items[""] != nil:
		default:
			if fl, ok := e.failed[""]; ok {
				if fl.stamps == stampKey(files) {
					e.report("%s", fl.err)
					break
				}
				delete(e.failed, "")
			}
			e.pending = len(files)
			all := true
			for _, f := range res.files {
				all = all && e.settled(f, now)
			}
			if !e.building[""] && all {
				jobs = append(jobs, &pubJob{key: "", files: files})
			}
		}
	}
	e.seen = cur
	promoted := map[string]string{}
	for _, it := range e.sortedItems() {
		// A duplicate whose original went seeds in its place, and the
		// others of its torrent wait on it.
		if o := e.items[it.dupOf]; it.dupOf != "" && (o == nil || o.id != it.id || o.dupOf != "") {
			if k, ok := promoted[it.id]; ok {
				it.dupOf, it.err = k, fmt.Errorf("%w: the same torrent as %s", ErrExists, k)
				continue
			}
			it.dupOf, it.err = "", nil
			promoted[it.id] = it.key
			e.dirty = true
		}
	}
	for _, it := range e.items {
		e.attach(it)
		e.saveResume(it)
	}
	for _, j := range jobs {
		e.building[j.key] = true
		select {
		case e.jobs <- j:
		default:
			delete(e.building, j.key)
		}
	}
	e.walked, e.scanned = true, now
	if e.dirty {
		e.saveIndex()
	}
	e.publish()
}

// buildLoop builds the torrents the walks ask for until the entry stops.
func (e *pubEntry) buildLoop() {
	defer e.p.wg.Done()
	for {
		select {
		case <-e.stop:
			return
		case j := <-e.jobs:
			e.build(j)
		}
	}
}

// build builds the torrent of j, then, if its files have not changed
// meanwhile, keeps it and seeds it.
func (e *pubEntry) build(j *pubJob) {
	r := e.p.r
	root, err := filepath.EvalSymlinks(e.dir)
	var mi *metainfo.MetaInfo
	if err == nil {
		e.p.builds.Add(1)
		mi, err = e.buildTorrent(root, j)
	}
	var raw []byte
	if err == nil {
		if raw, err = bencode.Marshal(mi); err == nil && len(raw) > MaxTorrentBytes {
			err = fmt.Errorf("its .torrent file is %d bytes, more than %d: set a larger piece_length", len(raw), MaxTorrentBytes)
		}
	}
	changed := false
	if err == nil {
		for _, f := range j.files {
			if _, st, serr := filewatch.Stat(filepath.Join(root, filepath.FromSlash(f.Path))); serr != nil || st != f.Stamp {
				changed = true
				break
			}
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(e.building, j.key)
	if e.stopped || errors.Is(err, errPublishStopped) || changed {
		// A file changed while it was hashed: the next walks see it, and
		// build it once it settles.
		return
	}
	for _, f := range j.files {
		if s, ok := e.seen[f.Path]; !ok || s != f.Stamp {
			return
		}
	}
	what := j.key
	if what == "" {
		what = "the directory"
	}
	if err == nil {
		id := idOf(mi.Info.InfoHashV1(), mi.Info.InfoHashV2())
		if !e.idInUse(id) {
			err = writeAtomic(e.stateDir, id+".torrent", raw)
		}
		if err == nil {
			it := &pubItem{key: j.key, id: id, files: j.files, built: time.Now().UTC().Truncate(time.Second), mi: mi, raw: raw,
				magnet: mi.Magnet().String()}
			for _, f := range j.files {
				it.size += f.Stamp.Size
			}
			for _, o := range e.items {
				if o.id == id && o.dupOf == "" && o.key != j.key {
					it.err, it.dupOf = fmt.Errorf("%w: the same torrent as %s", ErrExists, o.key), o.key
					break
				}
			}
			if old := e.items[j.key]; old != nil {
				e.dropItem(old)
			}
			e.items[j.key] = it
			e.dirty = true
			e.pending = max(0, e.pending-len(j.files))
			if it.err == nil {
				e.attach(it)
			}
			if r.log != nil {
				r.log.Infof("publish %s: %s is torrent %s", e.name, what, id)
			}
			e.publish()
			return
		}
	}
	e.failed[j.key] = pubFailure{stamps: stampKey(j.files), err: err.Error()}
	e.report("%s: %v", what, err)
	if r.log != nil {
		r.log.Warnf("publish %s: %s: %v", e.name, what, err)
	}
	e.publish()
}

// buildTorrent hashes the files of j into a hybrid torrent.
func (e *pubEntry) buildTorrent(root string, j *pubJob) (*metainfo.MetaInfo, error) {
	b := metainfo.Builder{Version: metainfo.Hybrid, PieceLength: e.cfg.PieceLength, Trackers: e.trackers,
		Private: e.cfg.Private, CreatedBy: "fit", CreationDate: time.Now()}
	if ws := e.itemWebSeed(j.key); ws != "" {
		b.WebSeeds = []string{ws}
	}
	fsys := &pubFS{root: root, cancel: &e.cancel}
	if e.cfg.PublishMode() == config.BTPublishFiles {
		return b.Build(fsys, j.key)
	}
	b.Name = filepath.Base(e.dir)
	fsys.allow = map[string]bool{".": true}
	for _, f := range j.files {
		for p := f.Path; p != "." && !fsys.allow[p]; p = path.Dir(p) {
			fsys.allow[p] = true
		}
	}
	return b.Build(fsys, ".")
}

// pubFS is the file system a torrent is built from: dir, its reads ending
// once the entry stops, and in mode whole_dir only the files the walk found.
type pubFS struct {
	root   string
	cancel *atomic.Bool
	// allow, when set, are the paths shown: the files and the directories
	// on their way.
	allow map[string]bool
}

func (f *pubFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) || f.allow != nil && !f.allow[name] {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	file, err := os.Open(filepath.Join(f.root, filepath.FromSlash(name)))
	if err != nil {
		return nil, err
	}
	return &hashingFile{File: file, cancel: f.cancel}, nil
}

// ReadDir lists a directory's entries that are shown.
func (f *pubFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if !fs.ValidPath(name) || f.allow != nil && !f.allow[name] {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	}
	list, err := os.ReadDir(filepath.Join(f.root, filepath.FromSlash(name)))
	if err != nil || f.allow == nil {
		return list, err
	}
	return slices.DeleteFunc(list, func(d fs.DirEntry) bool { return !f.allow[path.Join(name, d.Name())] }), nil
}

// hashingFile is a file being hashed, whose reads fail once the entry stops.
type hashingFile struct {
	*os.File
	cancel *atomic.Bool
}

func (f *hashingFile) Read(p []byte) (int, error) {
	if f.cancel.Load() {
		return 0, errPublishStopped
	}
	return f.File.Read(p)
}

// errReadOnly is a write to a published torrent's storage.
var errReadOnly = errors.New("bittorrent: a published torrent's files are never written")

// readOnlyStorage is a published torrent's storage: it reads the files in
// place, and refuses every write, so that a piece that fails its check is
// never downloaded over the published file.
type readOnlyStorage struct{ fibbt.Storage }

func (s readOnlyStorage) WriteAt(_ int, p []byte, _ int64) (int, error) {
	if len(p) == 0 {
		// Creating an empty file, which is there.
		return 0, nil
	}
	return 0, errReadOnly
}

func (s readOnlyStorage) Stat(file int) (int64, time.Time, error) {
	if ss, ok := s.Storage.(fibbt.StatStorage); ok {
		return ss.Stat(file)
	}
	return 0, time.Time{}, errors.ErrUnsupported
}

func readOnlyOpener(o fibbt.StorageOpener) fibbt.StorageOpener {
	return func(info *metainfo.Info) (fibbt.Storage, error) {
		st, err := o(info)
		if err != nil {
			return nil, err
		}
		return readOnlyStorage{st}, nil
	}
}

// escapePathSegment escapes a path element of a web seed's URL, as fib's
// web seed client does.
func escapePathSegment(s string) string { return url.PathEscape(s) }

// pubIndex is an entry's index.json.
type pubIndex struct {
	Version int            `json:"version"`
	Key     string         `json:"key"`
	Saved   time.Time      `json:"saved"`
	Items   []pubIndexItem `json:"items"`
}

type pubIndexItem struct {
	Key   string    `json:"key"`
	ID    string    `json:"id"`
	Files []pubFile `json:"files"`
	Built time.Time `json:"built"`
}

// saveIndex writes the entry's index.json. r.mu is held.
func (e *pubEntry) saveIndex() {
	doc := pubIndex{Version: pubIndexVersion, Key: e.key, Saved: time.Now().UTC(), Items: []pubIndexItem{}}
	for _, it := range e.sortedItems() {
		if it.err != nil {
			// A duplicate is found again by the next walk's build.
			continue
		}
		doc.Items = append(doc.Items, pubIndexItem{Key: it.key, ID: it.id, Files: it.files, Built: it.built})
	}
	data, err := json.Marshal(doc)
	if err == nil {
		err = writeAtomic(e.stateDir, "index.json", append(data, '\n'))
	}
	if err != nil {
		e.report("saving index.json: %v", err)
		if r := e.p.r; r.log != nil {
			r.log.Errorf("publish %s: saving %s: %v", e.name, filepath.Join(e.stateDir, "index.json"), err)
		}
		return
	}
	e.dirty = false
}

// restore reads the entry's torrents from its state, kept unless the walk
// that follows finds their files changed. What the state holds that the
// index does not name is removed, as is all of it when it was kept for
// other settings.
func (e *pubEntry) restore() {
	items, keep := e.readIndex()
	if list, err := os.ReadDir(e.stateDir); err == nil {
		for _, d := range list {
			n := d.Name()
			if n == "index.json" || keep[strings.TrimSuffix(strings.TrimSuffix(n, ".torrent"), ".resume")] && !strings.HasPrefix(n, ".") {
				continue
			}
			_ = os.Remove(filepath.Join(e.stateDir, n))
		}
	}
	r := e.p.r
	r.mu.Lock()
	defer r.mu.Unlock()
	if e.stopped || len(e.items) > 0 {
		return
	}
	for _, it := range items {
		e.items[it.key] = it
	}
	e.publish()
}

// readIndex reads the entry's index.json and the torrents it names, and
// the IDs to keep the files of.
func (e *pubEntry) readIndex() ([]*pubItem, map[string]bool) {
	keep := map[string]bool{}
	data, err := readBounded(filepath.Join(e.stateDir, "index.json"), MaxPublishIndexBytes)
	if err != nil {
		return nil, keep
	}
	var doc pubIndex
	if json.Unmarshal(data, &doc) != nil || doc.Version != pubIndexVersion || doc.Key != e.key {
		return nil, keep
	}
	var items []*pubItem
	seen := map[string]bool{}
	for _, x := range doc.Items {
		if _, ok := parseID(x.ID); !ok || len(x.Files) == 0 || seen[x.Key] {
			continue
		}
		if e.cfg.PublishMode() == config.BTPublishFiles && (x.Key == "" || len(x.Files) != 1 || x.Files[0].Path != x.Key) {
			continue
		}
		raw, err := readBounded(filepath.Join(e.stateDir, x.ID+".torrent"), MaxTorrentBytes)
		if err != nil {
			continue
		}
		mi, err := metainfo.Parse(raw)
		if err != nil || idOf(mi.Info.InfoHashV1(), mi.Info.InfoHashV2()) != x.ID {
			continue
		}
		seen[x.Key] = true
		it := &pubItem{key: x.Key, id: x.ID, files: x.Files, built: x.Built, mi: mi, raw: raw, magnet: mi.Magnet().String()}
		for _, f := range x.Files {
			it.size += f.Stamp.Size
		}
		if res, err := readBounded(filepath.Join(e.stateDir, x.ID+".resume"), MaxResumeBytes); err == nil {
			it.resume, it.resumeSaved = res, true
		}
		keep[x.ID] = true
		items = append(items, it)
	}
	return items, keep
}

// removeItemFiles removes the .torrent file and the resume data of id from
// an entry's state.
func removeItemFiles(dir, id string) error {
	var errs []error
	for _, p := range []string{filepath.Join(dir, id+".torrent"), filepath.Join(dir, id+".resume")} {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// removeAllUnder removes base/name, an entry's state, name a name the
// config checked.
func removeAllUnder(base, name string) error {
	if name == "" || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return fmt.Errorf("bittorrent: %q is not an entry's name", name)
	}
	return os.RemoveAll(filepath.Join(base, name))
}

// pattern is an include or exclude pattern, as a media library's: one
// without '/' matches a name, one with '/' a path under dir, ** matching
// any run of directories.
type pattern struct {
	src   string
	parts []string // nil for a pattern of a name
}

func newPattern(p string) pattern {
	if !strings.Contains(p, "/") {
		return pattern{src: p}
	}
	return pattern{src: p, parts: strings.Split(p, "/")}
}

// match reports whether p matches rel, a slash path under dir.
func (p pattern) match(rel string) bool {
	if p.parts == nil {
		ok, _ := path.Match(p.src, path.Base(rel))
		return ok
	}
	return matchParts(p.parts, strings.Split(rel, "/"))
}

func matchParts(pat, name []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for i := 0; i <= len(name); i++ {
				if matchParts(pat[1:], name[i:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		if ok, _ := path.Match(pat[0], name[0]); !ok {
			return false
		}
		pat, name = pat[1:], name[1:]
	}
	return len(name) == 0
}

func anyMatch(list []pattern, rel string) bool {
	for _, p := range list {
		if p.match(rel) {
			return true
		}
	}
	return false
}

var _ io.Reader = (*hashingFile)(nil)
