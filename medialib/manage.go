package medialib

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// The errors of the library's changes of its files.
var (
	// ErrNoItem is the error of an item the catalog does not hold.
	ErrNoItem = errors.New("medialib: no such item in the catalog")
	// ErrExists is the error of a change whose file, or ID, is taken.
	ErrExists = errors.New("medialib: a file is there already, or the ID is taken")
	// ErrNotMedia is the error of a file the library does not look at,
	// by its name: not a media file, subtitle, picture or sidecar.
	ErrNotMedia = errors.New("medialib: not a media file, subtitle, picture or sidecar")
	// ErrNoChange is the error of a rename or move to where the item is.
	ErrNoChange = errors.New("medialib: the item is there already")
	// ErrBadSidecar is the error of a sidecar that would not be read: its
	// error says why.
	ErrBadSidecar = errors.New("medialib: not a sidecar the library reads")
)

// Change is what a change of a library's files did: its files, by their
// paths under their root, from where to where; From alone for a file
// removed, To alone for one written.
type Change struct {
	Library string `json:"library"`
	// Op is rename, move, delete, sidecar or upload.
	Op string `json:"op"`
	// Item is the ID of the item changed, and ID its ID once the scan
	// that follows lists it; "" for one deleted.
	Item  string       `json:"item,omitempty"`
	ID    string       `json:"id,omitempty"`
	Root  string       `json:"root"`
	Files []FileChange `json:"files"`
}

// FileChange is a file a change moved, removed or wrote.
type FileChange struct {
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

// The operations of a Change.
const (
	OpRename  = "rename"
	OpMove    = "move"
	OpDelete  = "delete"
	OpSidecar = "sidecar"
	OpUpload  = "upload"
)

// plan is a change worked out from the catalog, before the disk is touched.
type plan struct {
	op   string
	item *Item
	root *root
	id   string // the item's ID once done
	// moves are the files renamed, or removed when to is "".
	moves []move
	// sideOld is the sidecar's path the item has now, for a rewrite.
	sideOld string
}

type move struct {
	f     *File
	to    string // its path under the root once done
	abs   string // the path it is opened by now
	toAbs string
}

// item returns the item id and its root, or ErrNoItem or ErrRemoved.
func (l *Library) item(id string) (*Item, *root, error) {
	if l.removed.Load() {
		return nil, nil, ErrRemoved
	}
	it := l.Item(id)
	if it == nil {
		return nil, nil, ErrNoItem
	}
	r := l.set.Load().rootOf(it.Root)
	if r == nil {
		// A reload removed its root: the next scan drops it.
		return nil, nil, ErrNoItem
	}
	return it, r, nil
}

// itemDir and itemBase are where an item's files are named after.
func itemDir(it *Item) string  { return relDir(it.Path) }
func itemBase(it *Item) string { return path.Base(it.Path) }

// RenameItem renames the item id to name in its directory: each of its
// files there named after it, as bbb.720p.mp4, bbb.en.srt, bbb.jpg and
// bbb.json, renamed name.720p.mp4 and so on; files a sidecar names by
// other names keep them, and the sidecar is written again where it names a
// file renamed. The item keeps its ID when its sidecar names one, and
// otherwise takes the one of its new path. It runs on the library's pool,
// and calls fn there, or at once with an error found before, as Open does;
// see Change for what fn is given. The catalog drops the item at once, and
// a scan lists it again.
func (l *Library) RenameItem(id, name string, fn func(*Change, error)) {
	it, r, err := l.item(id)
	if err == nil {
		err = checkBase(name)
	}
	if err != nil {
		fn(nil, err)
		return
	}
	if name == itemBase(it) {
		fn(nil, ErrNoChange)
		return
	}
	p := &plan{op: OpRename, item: it, root: r}
	dir, base := itemDir(it), itemBase(it)
	renamed := map[string]string{}
	for _, f := range it.files() {
		to := f.Path
		if fd, fname := relDir(f.Path), path.Base(f.Path); fd == dir && (strings.HasPrefix(fname, base+".") || strings.HasPrefix(fname, base+"-poster.")) {
			to = path.Join(dir, name+fname[len(base):])
			if _, err := cleanRel(to, false); err != nil {
				fn(nil, err)
				return
			}
			renamed[f.Path] = to
		}
		p.moves = append(p.moves, move{f: f, to: to})
	}
	p.id = l.idAfter(it, path.Join(dir, name))
	if it.Sidecar != nil {
		p.sideOld = renamed[it.Sidecar.Path]
	}
	l.change(p, func(p *plan) ([]FileChange, error) { return nil, p.rewriteRefs(renamed) }, fn)
}

// MoveItem moves the item id, every file of it, to dir, a slash path under
// its root, "" for the root itself, each file keeping its path relative to
// the item's directory, so that what its sidecar names stays named; the
// directories are made when missing. It runs as RenameItem does.
func (l *Library) MoveItem(id, dir string, fn func(*Change, error)) {
	it, r, err := l.item(id)
	if err == nil {
		dir, err = cleanRel(dir, true)
	}
	if err != nil {
		fn(nil, err)
		return
	}
	from := itemDir(it)
	if dir == from {
		fn(nil, ErrNoChange)
		return
	}
	p := &plan{op: OpMove, item: it, root: r}
	for _, f := range it.files() {
		rel := f.Path
		if from != "" {
			rel = strings.TrimPrefix(f.Path, from+"/")
		}
		to := path.Join(dir, rel)
		if _, err := cleanRel(to, false); err != nil {
			fn(nil, err)
			return
		}
		p.moves = append(p.moves, move{f: f, to: to})
	}
	p.id = l.idAfter(it, path.Join(dir, itemBase(it)))
	l.change(p, nil, fn)
}

// DeleteItem removes every file of the item id: its renditions, audio,
// subtitles, poster and sidecar. It runs as RenameItem does; a file that
// cannot be removed stops it, and the Change says what was removed before.
func (l *Library) DeleteItem(id string, fn func(*Change, error)) {
	it, r, err := l.item(id)
	if err != nil {
		fn(nil, err)
		return
	}
	p := &plan{op: OpDelete, item: it, root: r}
	for _, f := range it.files() {
		p.moves = append(p.moves, move{f: f})
	}
	l.change(p, nil, fn)
}

// idAfter is the ID the item will have at rel: its sidecar's, or rel's.
func (l *Library) idAfter(it *Item, rel string) string {
	if it.named {
		return it.ID
	}
	return pathID(rel)
}

// checkBase checks a new name of an item: a name, which a scan does not
// read as a rendition's label or a poster's.
func checkBase(name string) error {
	if !validName(name) || len(name) > maxNameBytes-32 {
		return ErrBadPath
	}
	if b, label := splitLabel(name); label != "" || b != name || strings.HasSuffix(name, "-poster") || kindOfName(name) != kindNone {
		return fmt.Errorf("%w: %q reads as a file's name, a rendition's label or a poster's", ErrBadPath, name)
	}
	return nil
}

// change does p on the pool, one change at a time, then drops the item
// from the catalog and asks for a scan. after, when not nil, runs once
// the files have moved, and undoes them all when it fails.
func (l *Library) change(p *plan, after func(*plan) ([]FileChange, error), fn func(*Change, error)) {
	l.async(func() {
		l.fileMu.Lock()
		c, err := l.do(p, after)
		l.fileMu.Unlock()
		if c != nil && len(c.Files) > 0 {
			l.forget(p.item.ID)
			l.Rescan()
		}
		fn(c, err)
	}, func(err error) { fn(nil, err) })
}

// do does p, and blocks.
func (l *Library) do(p *plan, after func(*plan) ([]FileChange, error)) (*Change, error) {
	if l.removed.Load() {
		return nil, ErrRemoved
	}
	c := &Change{Library: l.name, Op: p.op, Item: p.item.ID, ID: p.id, Root: p.root.conf, Files: []FileChange{}}
	follow := l.set.Load().follow
	for i := range p.moves {
		m := &p.moves[i]
		if err := checkSource(m.f, follow); err != nil {
			return nil, fmt.Errorf("%s: %w", m.f.Path, err)
		}
		m.abs = m.f.abs
		if m.to == "" || m.to == m.f.Path {
			continue
		}
		var err error
		if m.toAbs, err = writable(p.root, m.to); err != nil {
			return nil, fmt.Errorf("%s: %w", m.to, err)
		}
		if fi, err := os.Lstat(m.toAbs); err == nil {
			// The file itself, on a system that folds case, is renamed
			// in place.
			if src, err2 := os.Lstat(m.abs); err2 != nil || !os.SameFile(fi, src) {
				return nil, fmt.Errorf("%s: %w", m.to, ErrExists)
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	if p.op == OpDelete {
		for _, m := range p.moves {
			if err := os.Remove(m.abs); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return c, fmt.Errorf("%s: %w", m.f.Path, err)
			}
			l.tables.drop(m.abs)
			c.Files = append(c.Files, FileChange{From: m.f.Path})
		}
		return c, nil
	}
	var done []move
	undo := func() {
		for _, m := range slices.Backward(done) {
			_ = os.Rename(m.toAbs, m.abs)
		}
	}
	for _, m := range p.moves {
		if m.to == "" || m.to == m.f.Path {
			continue
		}
		err := os.MkdirAll(filepath.Dir(m.toAbs), 0o755)
		if err == nil {
			err = os.Rename(m.abs, m.toAbs)
		}
		if err != nil {
			undo()
			return nil, fmt.Errorf("%s: %w", m.f.Path, err)
		}
		done = append(done, m)
	}
	var extra []FileChange
	if after != nil {
		var err error
		if extra, err = after(p); err != nil {
			undo()
			return nil, err
		}
	}
	for _, m := range done {
		l.tables.drop(m.abs)
		c.Files = append(c.Files, FileChange{From: m.f.Path, To: m.to})
	}
	c.Files = append(c.Files, extra...)
	return c, nil
}

// checkSource checks that f is as the catalog has it, and that its
// directory lies inside its root. It blocks.
func checkSource(f *File, follow bool) error {
	fi, err := os.Lstat(f.abs)
	if err != nil {
		return notFound(err)
	}
	if fi.Mode()&fs.ModeSymlink == 0 && (fi.Size() != f.Size || !fi.ModTime().Equal(f.ModTime)) {
		return ErrChanged
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(f.abs))
	if err != nil {
		return notFound(err)
	}
	if !inside(f.real, dir) && !follow {
		return ErrOutside
	}
	return nil
}

// rewriteRefs writes the item's sidecar again, at its new path, with the
// files renamed (by their old paths under the root) named anew; it writes
// nothing when it names none of them.
func (p *plan) rewriteRefs(renamed map[string]string) error {
	if p.sideOld == "" {
		return nil
	}
	abs := p.root.path(p.sideOld)
	data, err := os.ReadFile(abs)
	if err != nil {
		return err
	}
	sd, err := parseSidecar(data)
	if err != nil {
		return err
	}
	from, to := itemDir(p.item), relDir(p.sideOld)
	changed := false
	fix := func(ref *string) {
		if *ref == "" {
			return
		}
		if rel, ok := underDir(from, *ref); ok {
			if n, ok := renamed[rel]; ok {
				if to != "" {
					n = strings.TrimPrefix(n, to+"/")
				}
				*ref = n
				changed = true
			}
		}
	}
	fix(&sd.Poster)
	for i := range sd.Renditions {
		fix(&sd.Renditions[i].File)
	}
	for i := range sd.Audio {
		fix(&sd.Audio[i].File)
	}
	for i := range sd.Subtitles {
		fix(&sd.Subtitles[i].File)
	}
	if !changed {
		return nil
	}
	return writeSidecarFile(abs, sd)
}

// encodeSidecar writes sd as a sidecar file holds it.
func encodeSidecar(sd *Sidecar) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	// A Sidecar always encodes.
	_ = enc.Encode(sd)
	return b.Bytes()
}

// writeSidecarFile writes sd to abs whole or not at all: to a temporary
// file beside it, synced, then renamed over it.
func writeSidecarFile(abs string, sd *Sidecar) error {
	return writeAtomic(abs, encodeSidecar(sd))
}

// writeAtomic writes data to abs through a temporary file beside it, which
// a scan does not look at.
func writeAtomic(abs string, data []byte) error {
	var rnd [8]byte
	_, _ = rand.Read(rnd[:])
	tmp := filepath.Join(filepath.Dir(abs), ".fit-tmp-"+hex.EncodeToString(rnd[:]))
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, abs)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

// forget takes the item id out of the catalog at once, and its sample
// tables with it, until a scan that starts later lists it as it is.
func (l *Library) forget(id string) {
	l.mu.Lock()
	l.edits++
	l.forgotten[id] = l.edits
	cat := l.cat.Load().without([]string{id})
	l.cat.Store(cat)
	l.mu.Unlock()
	l.tables.keep(cat)
}

// SidecarView is an item's sidecar as the workbench edits it: the sidecar
// the item has, or, without one, the one that lists what its names group,
// and the files under the item's directory it may name.
type SidecarView struct {
	// Exists says whether the item has a sidecar; Path is where it is or
	// would be, under the root, and Modified when it was written.
	Exists   bool      `json:"exists"`
	Path     string    `json:"path"`
	Modified time.Time `json:"modified,omitzero"`
	Sidecar  *Sidecar  `json:"sidecar"`
	// Files are the media files, subtitles and pictures under the item's
	// directory, relative to it, at most MaxSidecarFiles of them.
	Files []string `json:"files"`
}

// MaxSidecarFiles is the most files a SidecarView lists.
const MaxSidecarFiles = 1000

// ReadSidecar reads the sidecar of the item id on the pool, or makes one of
// what the item's files are named, and calls fn there, as Open does.
func (l *Library) ReadSidecar(id string, fn func(*SidecarView, error)) {
	it, r, err := l.item(id)
	if err != nil {
		fn(nil, err)
		return
	}
	l.async(func() { fn(l.readSidecar(it, r)) }, func(err error) { fn(nil, err) })
}

func (l *Library) readSidecar(it *Item, r *root) (*SidecarView, error) {
	s := l.set.Load()
	dir := itemDir(it)
	v := &SidecarView{Path: path.Join(dir, itemBase(it)+".json"), Files: []string{}}
	if it.Sidecar != nil {
		h, err := openFile(it.Sidecar, s.follow)
		if err != nil {
			return nil, err
		}
		defer h.Close()
		if h.Size > s.sidecarBytes {
			return nil, ErrTooLarge
		}
		data := make([]byte, h.Size)
		if _, err := h.ReadAt(data, 0); err != nil {
			return nil, err
		}
		sd, err := parseSidecar(data)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrBadSidecar, err)
		}
		v.Exists, v.Path, v.Modified, v.Sidecar = true, it.Sidecar.Path, it.Sidecar.ModTime, sd
	} else {
		v.Sidecar = draftSidecar(it)
	}
	v.Files = listFiles(r.path(dir), MaxSidecarFiles)
	return v, nil
}

// draftSidecar is the sidecar of what it's files are named: its title and
// files with their names and languages.
func draftSidecar(it *Item) *Sidecar {
	dir := itemDir(it)
	rel := func(p string) string {
		if dir == "" {
			return p
		}
		return strings.TrimPrefix(p, dir+"/")
	}
	sd := &Sidecar{Title: it.Title}
	if it.Poster != nil {
		sd.Poster = rel(it.Poster.Path)
	}
	for _, r := range it.Renditions {
		sd.Renditions = append(sd.Renditions, SidecarFile{File: rel(r.Path), Name: r.Name, Language: r.Language, Default: r.Default})
	}
	for _, r := range it.Audio {
		sd.Audio = append(sd.Audio, SidecarFile{File: rel(r.Path), Name: r.Name, Language: r.Language, Default: r.Default})
	}
	for _, s := range it.Subtitles {
		sd.Subtitles = append(sd.Subtitles, SidecarSubtitle{File: rel(s.Path), Name: s.Name, Language: s.Language, Default: s.Default, Forced: s.Forced})
	}
	return sd
}

// listFiles lists the media files, subtitles and pictures under dir, at
// most n, as slash paths relative to it, sorted, skipping names that start
// with '.', MaxDepth deep. It blocks.
func listFiles(dir string, n int) []string {
	out := []string{}
	var walk func(abs, rel string, depth int)
	walk = func(abs, rel string, depth int) {
		ents, err := os.ReadDir(abs)
		if err != nil {
			return
		}
		for _, e := range ents {
			name := e.Name()
			if len(out) >= n || strings.HasPrefix(name, ".") {
				continue
			}
			p := path.Join(rel, name)
			switch k := kindOfName(name); {
			case e.IsDir() && depth < MaxDepth:
				walk(filepath.Join(abs, name), p, depth+1)
			case e.Type().IsRegular() && k != kindNone && k != kindSidecar:
				out = append(out, p)
			}
		}
	}
	walk(dir, "", 0)
	slices.Sort(out)
	return out
}

// WriteSidecar writes sd as the sidecar of the item id, replacing the one it
// has, or as <name>.json beside its files when it has none: whole or not
// at all, on the pool, then calls fn there, as RenameItem does. sd is
// checked as a scan reads it: no more than max_sidecar_bytes, every file it
// names under its directory, there, and of its kind, and an ID of its own
// that no other item has. modified, when not zero, is the time of the
// sidecar the change was made from: one written since, or a sidecar
// written where the item had none, is refused with ErrChanged.
func (l *Library) WriteSidecar(id string, sd *Sidecar, modified time.Time, fn func(*Change, error)) {
	it, r, err := l.item(id)
	if err != nil {
		fn(nil, err)
		return
	}
	data := encodeSidecar(sd)
	s := l.set.Load()
	var parsed *Sidecar
	switch parsed, err = parseSidecar(data); {
	case err != nil:
		err = fmt.Errorf("%w: %v", ErrBadSidecar, err)
	case int64(len(data)) > s.sidecarBytes:
		err = fmt.Errorf("%w: %d bytes, past max_sidecar_bytes, %d", ErrTooLarge, len(data), s.sidecarBytes)
	case parsed.ID != "" && parsed.ID != it.ID && l.Item(parsed.ID) != nil:
		err = fmt.Errorf("%w: the item %s has the ID %q", ErrExists, l.Item(parsed.ID).Path, parsed.ID)
	case !modified.IsZero() && (it.Sidecar == nil || !it.Sidecar.ModTime.Equal(modified)):
		err = ErrChanged
	}
	if err != nil {
		fn(nil, err)
		return
	}
	target := path.Join(itemDir(it), itemBase(it)+".json")
	if it.Sidecar != nil {
		target = it.Sidecar.Path
	}
	id2 := pathID(it.Path)
	if parsed.ID != "" {
		id2 = parsed.ID
	}
	p := &plan{op: OpSidecar, item: it, root: r, id: id2}
	l.change(p, func(p *plan) ([]FileChange, error) {
		if err := checkRefs(r, relDir(target), parsed); err != nil {
			return nil, err
		}
		abs, err := writable(r, target)
		if err != nil {
			return nil, err
		}
		if it.Sidecar != nil {
			if err := checkSource(it.Sidecar, s.follow); err != nil {
				return nil, err
			}
		} else if _, err := os.Lstat(abs); err == nil {
			return nil, fmt.Errorf("%s: %w", target, ErrExists)
		}
		if err := writeAtomic(abs, data); err != nil {
			return nil, err
		}
		return []FileChange{{To: target}}, nil
	}, fn)
}

// checkRefs checks that each file sd names, relative to dir under r, is
// there, inside r, and of the kind it is named as. It blocks.
func checkRefs(r *root, dir string, sd *Sidecar) error {
	real, err := filepath.EvalSymlinks(r.abs)
	if err != nil {
		return err
	}
	check := func(field, file string, want kind) error {
		rel, ok := underDir(dir, file)
		if !ok {
			return fmt.Errorf("%w: %s %q leads out of the sidecar's directory", ErrBadSidecar, field, file)
		}
		if k := kindOfName(rel); k != want {
			return fmt.Errorf("%w: %s %q is not a %s", ErrBadSidecar, field, file, kindNames[want])
		}
		t, err := filepath.EvalSymlinks(r.path(rel))
		if err != nil {
			return fmt.Errorf("%w: %s %q is not there", ErrBadSidecar, field, file)
		}
		if fi, err := os.Stat(t); err != nil || !fi.Mode().IsRegular() || !inside(real, t) {
			return fmt.Errorf("%w: %s %q is not a file inside the root", ErrBadSidecar, field, file)
		}
		return nil
	}
	if sd.Poster != "" {
		if err := check("poster", sd.Poster, kindImage); err != nil {
			return err
		}
	}
	for _, list := range [][]SidecarFile{sd.Renditions, sd.Audio} {
		for _, m := range list {
			if err := check("file", m.File, kindMedia); err != nil {
				return err
			}
		}
	}
	for _, st := range sd.Subtitles {
		if err := check("file", st.File, kindSubtitle); err != nil {
			return err
		}
	}
	return nil
}

// MaxDirs is the most directories Dirs lists.
const MaxDirs = 2000

// Dirs lists the directories under the root the config writes as root, as
// slash paths, sorted, those that start with '.' and those exclude leaves
// out skipped, and symbolic links not followed, at most MaxDirs of them:
// where an item may be moved to. It reads them on the pool and calls fn
// there, as Open does; ErrNoItem for a root the library does not have.
func (l *Library) Dirs(root string, fn func([]string, error)) {
	s := l.set.Load()
	r := s.rootOf(root)
	switch {
	case l.removed.Load():
		fn(nil, ErrRemoved)
		return
	case r == nil:
		fn(nil, fmt.Errorf("%w: the library has no root %q", ErrNoItem, root))
		return
	}
	l.async(func() {
		out := []string{}
		var walk func(abs, rel string, depth int)
		walk = func(abs, rel string, depth int) {
			ents, err := os.ReadDir(abs)
			if err != nil {
				return
			}
			for _, e := range ents {
				p := path.Join(rel, e.Name())
				if len(out) >= MaxDirs || !e.IsDir() || strings.HasPrefix(e.Name(), ".") || anyMatch(s.exclude, p) {
					continue
				}
				out = append(out, p)
				if depth+1 < MaxDepth {
					walk(filepath.Join(abs, e.Name()), p, depth+1)
				}
			}
		}
		walk(r.abs, "", 0)
		slices.Sort(out)
		fn(out, nil)
	}, func(err error) { fn(nil, err) })
}
