package bittorrent

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	fibbt "github.com/lesismal/fib/bittorrent"
	"github.com/lesismal/fib/bittorrent/metainfo"

	"github.com/lesismal/fit/config"
)

// peStorage is a storage that cannot say its files' sizes and times.
type peStorage struct{ fibbt.Storage }

// TestPublishFSBounds: the build's file system refuses paths outside its
// root and, in mode whole_dir, those the walk did not find; reads run the
// hook; storage without Stat says so; an opener's error is passed on.
func TestPublishFSBounds(t *testing.T) {
	root := t.TempDir()
	writeData(t, root, "a/x.bin", 10, 1)
	writeData(t, root, "a/y.bin", 10, 2)
	hooked := 0
	fsys := &pubFS{root: root, hook: func() { hooked++ }, allow: map[string]bool{".": true, "a": true, "a/x.bin": true}}
	for _, name := range []string{"../x", "/abs", "a/y.bin"} {
		if _, err := fsys.Open(name); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("open %q: %v", name, err)
		}
	}
	for _, name := range []string{"..", "b"} {
		if _, err := fsys.ReadDir(name); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("readdir %q: %v", name, err)
		}
	}
	list, err := fsys.ReadDir("a")
	if err != nil || len(list) != 1 || list[0].Name() != "x.bin" {
		t.Fatalf("%v %v", list, err)
	}
	f, err := fsys.Open("a/x.bin")
	if err != nil {
		t.Fatal(err)
	}
	if b, err := io.ReadAll(f); err != nil || len(b) != 10 || hooked == 0 {
		t.Fatalf("%d bytes, %v, hooked %d", len(b), err, hooked)
	}
	_ = f.Close()
	open := &pubFS{root: root}
	if _, err := open.Open("missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	if _, err := open.ReadDir("missing"); err == nil {
		t.Fatal("a missing directory listed")
	}

	if _, _, err := (readOnlyStorage{peStorage{}}).Stat(0); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatal(err)
	}
	bad := readOnlyOpener(func(*metainfo.Info) (fibbt.Storage, error) { return nil, errors.New("no storage") })
	if _, err := bad(nil); err == nil || err.Error() != "no storage" {
		t.Fatal(err)
	}
}

// TestPublishStateNames refuses an entry name that is not one, and
// reports the files of an item that cannot be removed.
func TestPublishStateNames(t *testing.T) {
	base := t.TempDir()
	for _, n := range []string{"", ".", "..", "a/b", `a\b`} {
		if err := removeAllUnder(base, n); err == nil {
			t.Errorf("%q removed", n)
		}
	}
	dir := t.TempDir()
	id := strings.Repeat("ab", 20)
	writeData(t, dir, id+".torrent/inside", 1, 1) // a directory, not empty
	if err := removeItemFiles(dir, id); err == nil {
		t.Fatal("a directory removed as a .torrent file")
	}
	if err := removeItemFiles(t.TempDir(), id); err != nil {
		t.Fatalf("nothing to remove: %v", err)
	}
}

// TestPublishPatternParts matches ** against no directories and a path
// shorter than the pattern.
func TestPublishPatternParts(t *testing.T) {
	for _, tt := range []struct {
		pat, rel string
		want     bool
	}{
		{"a/**/c.bin", "a/c.bin", true},
		{"a/**/c.bin", "a/b/d/c.bin", true},
		{"a/**/c.bin", "a/b", false},
		{"a/b/c", "a/b", false},
		{"a/*", "a/b/c", false},
		{"*.bin", "x/y.bin", true},
	} {
		if got := newPattern(tt.pat).match(tt.rel); got != tt.want {
			t.Errorf("%q %q: %v", tt.pat, tt.rel, got)
		}
	}
}

// TestPublishIndexRead reads an entry's index.json, leaving out what does
// not hold: an index of another version or key, items of a bad ID, of no
// files, of a key not their file's, whose .torrent file is missing, does
// not parse or is of another torrent, and an item twice; the state's files
// no item names are removed.
func TestPublishIndexRead(t *testing.T) {
	e := startEngine(t)
	r := startRole(t, e, clientJSON(t.TempDir(), "", ""), nil)
	src := t.TempDir()
	torrent, _ := payload(t, src, "f.bin", 20<<10)
	other, _ := payload(t, src, "g.bin", 20<<10+1)
	mi, err := metainfo.Parse(torrent)
	if err != nil {
		t.Fatal(err)
	}
	id := idOf(mi.Info.InfoHashV1(), mi.Info.InfoHashV2())
	cfg := &config.BitTorrentPublish{Name: "p", Dir: src}
	r.mu.Lock()
	pe, err := r.pub.newEntry(cfg)
	r.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		r.mu.Lock()
		pe.halt()
		r.mu.Unlock()
	})
	state := pe.stateDir
	write := func(name string, data []byte) { mustWrite(t, filepath.Join(state, name), data) }
	files := `[{"path": "f.bin", "stamp": {"size": 1}}]`
	index := func(key string, items ...string) []byte {
		return []byte(`{"version": ` + strconv.Itoa(pubIndexVersion) + `, "key": ` + strconv.Quote(key) + `, "items": [` + strings.Join(items, ",") + `]}`)
	}
	item := func(key, id, files string) string {
		return `{"key": ` + strconv.Quote(key) + `, "id": ` + strconv.Quote(id) + `, "files": ` + files + `}`
	}

	for _, doc := range [][]byte{
		[]byte("{"),
		[]byte(`{"version": 99, "key": ` + strconv.Quote(pe.key) + `}`),
		index("another key", item("f.bin", id, files)),
	} {
		write("index.json", doc)
		if items, keep := pe.readIndex(); len(items) != 0 || len(keep) != 0 {
			t.Fatalf("%s: %v %v", doc, items, keep)
		}
	}

	badID := strings.Repeat("cd", 20)
	wrongID := strings.Repeat("ef", 20)
	missingID := strings.Repeat("01", 20)
	write(id+".torrent", torrent)
	write(id+".resume", []byte("resume"))
	write(badID+".torrent", []byte("not bencoded"))
	write(wrongID+".torrent", other)
	write("stale.torrent", []byte("x"))
	write(".hidden", []byte("x"))
	write("index.json", index(pe.key,
		item("x.bin", "NOT-AN-ID", files),
		item("f.bin", id, `[]`),
		item("", id, files),
		item("other.bin", id, files),
		item("f.bin", missingID, files),
		item("f.bin", badID, files),
		item("f.bin", wrongID, files),
		item("f.bin", id, files),
		item("f.bin", id, files),
	))
	items, keep := pe.readIndex()
	if len(items) != 1 || items[0].id != id || string(items[0].resume) != "resume" || !items[0].resumeSaved || !keep[id] || len(keep) != 1 {
		t.Fatalf("%+v %v", items, keep)
	}

	pe.restore()
	for _, n := range []string{"stale.torrent", ".hidden", badID + ".torrent", wrongID + ".torrent"} {
		if exists(filepath.Join(state, n)) {
			t.Errorf("%s kept", n)
		}
	}
	for _, n := range []string{"index.json", id + ".torrent", id + ".resume"} {
		if !exists(filepath.Join(state, n)) {
			t.Errorf("%s removed", n)
		}
	}
	r.mu.Lock()
	n := len(pe.items)
	r.mu.Unlock()
	if n != 1 {
		t.Fatalf("%d items restored", n)
	}
	if st := r.Publishing([]string{"p"}); len(st) != 1 || len(st[0].Torrents) != 1 || st[0].Torrents[0].State != "stopped" {
		t.Fatalf("%+v", st)
	}
	pe.restore() // has its items: nothing more

	// The problems past maxPublishReport are counted, not listed.
	r.mu.Lock()
	for i := range maxPublishReport + 3 {
		pe.report("problem %d", i)
	}
	pe.publish()
	r.mu.Unlock()
	if st := r.Publishing([]string{"p"})[0]; len(st.Problems) != maxPublishReport || st.Dropped != 3 {
		t.Fatalf("%d problems, %d dropped", len(st.Problems), st.Dropped)
	}

	// An index that cannot be written is reported, and kept dirty.
	r.mu.Lock()
	pe.problems = nil
	saved := pe.stateDir
	pe.stateDir = filepath.Join(state, "index.json", "below")
	pe.dirty = true
	pe.saveIndex()
	dirty, problems := pe.dirty, slices.Clone(pe.problems)
	pe.stateDir = saved
	r.mu.Unlock()
	if !dirty || len(problems) != 1 || !strings.Contains(problems[0], "saving index.json") {
		t.Fatalf("dirty %v, problems %q", dirty, problems)
	}

	// Halted twice, it publishes nothing more.
	r.mu.Lock()
	pe.halt()
	pe.halt()
	pe.problems = nil
	pe.publish()
	r.mu.Unlock()
	if st := r.Publishing([]string{"p"})[0]; len(st.Problems) != maxPublishReport {
		t.Fatalf("a stopped entry published: %+v", st)
	}
}

// TestPublishWalkEdges: a dir that is a file; hidden directories and
// names the storage would change left out; a directory that cannot be
// read reported, and in mode whole_dir refusing the torrent, as one with
// no data does; a file published by two entries seeded by one; the log;
// the entries kept across a reload and a new client.
func TestPublishWalkEdges(t *testing.T) {
	e := startEngine(t)
	dir := t.TempDir()
	writeData(t, dir, "a.bin", 20<<10, 60)
	writeData(t, dir, ".git/hidden.bin", 100, 61)
	odd := ""
	if f, err := os.Create(filepath.Join(dir, "bad\xff.bin")); err == nil {
		_, _ = f.Write([]byte("x"))
		_ = f.Close()
		odd = "bad\xff.bin"
	}
	notDir := filepath.Join(t.TempDir(), "file")
	mustWrite(t, notDir, []byte("x"))
	emptyDir := filepath.Join(t.TempDir(), "empty")
	mustWrite(t, filepath.Join(emptyDir, "zero.bin"), nil)
	lockedDir := filepath.Join(t.TempDir(), "locked")
	writeData(t, lockedDir, "ok.bin", 100, 62)
	locked := filepath.Join(lockedDir, "sub")
	writeData(t, locked, "in.bin", 100, 63)
	root := os.Geteuid() == 0
	if !root {
		if err := os.Chmod(locked, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	}

	l, logged := reLogged(t)
	entries := entryJSON("w", dir, `"files": ["*.bin"]`) + ", " + entryJSON("w2", dir, `"files": ["a.bin"]`) + ", " +
		entryJSON("file", notDir, "") + ", " + entryJSON("empty", emptyDir, `"mode": "whole_dir"`) + ", " +
		entryJSON("locked", lockedDir, `"mode": "whole_dir"`)
	dataDir := t.TempDir()
	js := func(extra string) string {
		s := publishJSON(dataDir, entries)
		return s[:len(s)-1] + extra + "}"
	}
	r := startRoleWith(t, e, member(t, js("")), func(r *Role) { r.log = l })

	waitFor(t, "the dir that is a file reported", func() bool {
		return strings.Contains(entry(t, r, "file").Error, "is not a directory")
	})
	waitFor(t, "the empty directory refused", func() bool { return problem(entry(t, r, "empty"), "has no data to publish") })
	if !root {
		waitFor(t, "the directory not read whole refused", func() bool {
			return problem(entry(t, r, "locked"), "could not be read whole")
		})
	}
	var seeding PublishedTorrent
	waitFor(t, "a.bin seeded by one entry, refused by the other", func() bool {
		var errs, seeds int
		for _, name := range []string{"w", "w2"} {
			for _, tr := range entry(t, r, name).Torrents {
				if tr.Path != "a.bin" {
					continue
				}
				switch {
				case tr.State == "seeding":
					seeds++
					seeding = tr
				case strings.Contains(tr.Error, "the client has a torrent of this info hash already"):
					errs++
				}
			}
		}
		return seeds == 1 && errs == 1
	})
	st := entry(t, r, "w")
	for _, tr := range st.Torrents {
		if strings.Contains(tr.Path, ".git") {
			t.Fatalf("a hidden file published: %+v", tr)
		}
	}
	if odd != "" && !problem(st, "a name the client's storage would change") {
		t.Errorf("the odd name not reported: %+v", st)
	}
	if !strings.Contains(logged.String(), "a.bin is torrent "+seeding.ID) {
		t.Errorf("the build not logged:\n%s", logged.String())
	}

	// A reload of the same entries keeps them as they are.
	if err := r.Apply(member(t, js(""))); err != nil {
		t.Fatal(err)
	}
	if got := entry(t, r, "w"); len(got.Torrents) != len(st.Torrents) {
		t.Fatalf("%+v", got)
	}
	// A new client: the torrents stop with the old one, and seed again with
	// the new one.
	if err := r.Apply(member(t, js(`, "disk_workers": 3`))); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a.bin seeding again", func() bool {
		for _, name := range []string{"w", "w2"} {
			for _, tr := range entry(t, r, name).Torrents {
				if tr.ID == seeding.ID && tr.State == "seeding" {
					return true
				}
			}
		}
		return false
	})
}

// TestPublishBuildFailedLogged logs a build that failed, and a whole_dir
// one that failed is not tried again until its files change.
func TestPublishBuildFailedLogged(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a file whatever its mode")
	}
	e := startEngine(t)
	dir := filepath.Join(t.TempDir(), "d")
	writeData(t, dir, "ok.bin", 100, 70)
	p := filepath.Join(dir, "no.bin")
	writeData(t, dir, "no.bin", 100, 71)
	if err := os.Chmod(p, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o644) })
	l, logged := reLogged(t)
	r := startRoleWith(t, e, member(t, publishJSON(t.TempDir(), entryJSON("d", dir, `"mode": "whole_dir"`))), func(r *Role) { r.log = l })
	waitFor(t, "the failed build reported", func() bool { return problem(entry(t, r, "d"), "the directory") })
	builds := r.pub.builds.Load()
	scanned := entry(t, r, "d").Scanned
	waitFor(t, "more walks", func() bool {
		st := entry(t, r, "d")
		return st.Scanned.Sub(scanned) >= 200*time.Millisecond && problem(st, "permission denied")
	})
	if n := r.pub.builds.Load(); n != builds {
		t.Fatalf("built again %d times", n-builds)
	}
	if !strings.Contains(logged.String(), "publish d: the directory:") {
		t.Errorf("the failure not logged:\n%s", logged.String())
	}
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	waitSeeding(t, r, "d", 1)
}

// TestWatchEdges: a watch_dir that is a file, logged once; the look's
// bound reached before the categories; what each file's outcome logs; a
// file that cannot be moved aside logged; the names past a thousand
// collisions, and a file gone before its move.
func TestWatchEdges(t *testing.T) {
	e := startEngine(t)
	l, logged := reLogged(t)
	r := startRoleWith(t, e, member(t, clientJSON(t.TempDir(), t.TempDir(), `"limits": {"max_torrents": 2}, "categories": [{"name": "cat"}]`)), func(r *Role) { r.log = l })

	notDir := filepath.Join(t.TempDir(), "file")
	mustWrite(t, notDir, []byte("x"))
	r.runWatch(&watchJob{dir: notDir, max: 1})
	r.runWatch(&watchJob{dir: notDir, max: 1})
	if !strings.Contains(r.watchErr, "is not a directory") || strings.Count(logged.String(), "is not a directory") != 1 {
		t.Fatalf("%q\n%s", r.watchErr, logged.String())
	}
	if listWatch(t.TempDir(), "", 0) != nil || listWatch(filepath.Join(notDir, "none"), "", 5) != nil {
		t.Fatal("files listed past the bound, or in no directory")
	}

	src, watch := t.TempDir(), t.TempDir()
	a, _ := payload(t, src, "ea.bin", 20<<10)
	b, _ := payload(t, src, "eb.bin", 20<<10+1)
	c, _ := payload(t, src, "ec.bin", 20<<10+2)
	mustWrite(t, filepath.Join(watch, "a.torrent"), a)
	mustWrite(t, filepath.Join(watch, "cat", "b.torrent"), b)
	job := &watchJob{dir: watch, max: 1, cats: []string{"cat"}}
	r.runWatch(job)
	r.runWatch(job)
	if !exists(filepath.Join(watch, watchDone, "a.torrent")) || !exists(filepath.Join(watch, "cat", "b.torrent")) {
		t.Fatal("the bound not kept")
	}

	// Held already, and .done not a directory: logged, the file left.
	mustWrite(t, filepath.Join(watch, "cat", watchDone), []byte("in the way"))
	mustWrite(t, filepath.Join(watch, "cat", "a2.torrent"), a)
	job = &watchJob{dir: watch, max: 10, cats: []string{"cat"}}
	r.runWatch(job)
	r.runWatch(job)
	// Bad, and .failed not a directory.
	mustWrite(t, filepath.Join(watch, watchFailed), []byte("in the way"))
	mustWrite(t, filepath.Join(watch, "bad.torrent"), []byte("not bencoded"))
	// Past max_torrents: tried again later, logged once.
	mustWrite(t, filepath.Join(watch, "c.torrent"), c)
	for range 3 {
		r.runWatch(job)
	}
	for _, want := range []string{"watch_dir: " + filepath.Join(watch, "a.torrent") + " added", "the torrent is already held",
		"was added but cannot be moved into .done", "cannot be added", "cannot be moved into .failed", "not added yet"} {
		if !strings.Contains(logged.String(), want) {
			t.Errorf("the log has no %q:\n%s", want, logged.String())
		}
	}
	if n := strings.Count(logged.String(), "not added yet"); n != 1 {
		t.Errorf("logged %d times", n)
	}

	// More than a thousand of a name moved aside: a name of its own still.
	dst := t.TempDir()
	mustWrite(t, filepath.Join(dst, watchDone, "x.torrent"), nil)
	for i := 1; i <= 1000; i++ {
		mustWrite(t, filepath.Join(dst, watchDone, "x-"+strconv.Itoa(i)+".torrent"), nil)
	}
	mustWrite(t, filepath.Join(dst, "x.torrent"), []byte("x"))
	if err := moveAside(watchFile{path: filepath.Join(dst, "x.torrent"), dir: dst}, watchDone, "why"); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(filepath.Join(dst, watchDone))
	if len(ents) != 1003 || exists(filepath.Join(dst, "x.torrent")) {
		t.Fatalf("%d entries", len(ents))
	}
	if err := moveAside(watchFile{path: filepath.Join(dst, "gone.torrent"), dir: dst}, watchDone, ""); err == nil {
		t.Fatal("a missing file moved")
	}
}

// TestPublishDuplicatesThree: of three files of one torrent, the original
// gone, the first duplicate seeds and the other waits on it.
func TestPublishDuplicatesThree(t *testing.T) {
	e := startEngine(t)
	dir := t.TempDir()
	data := writeData(t, dir, "a/same.bin", 20<<10, 80)
	for _, d := range []string{"b", "c"} {
		mustWrite(t, filepath.Join(dir, d, "same.bin"), data)
	}
	r := startRole(t, e, publishJSON(t.TempDir(), entryJSON("dup", dir, "")), nil)
	waitFor(t, "the duplicates reported", func() bool {
		st := entry(t, r, "dup")
		return len(st.Torrents) == 3 && byPath(t, st, "a/same.bin").State == "seeding" &&
			strings.Contains(byPath(t, st, "b/same.bin").Error, "a/same.bin") && strings.Contains(byPath(t, st, "c/same.bin").Error, "a/same.bin")
	})
	if err := os.Remove(filepath.Join(dir, "a", "same.bin")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "b seeding, c waiting on it", func() bool {
		st := entry(t, r, "dup")
		return len(st.Torrents) == 2 && byPath(t, st, "b/same.bin").State == "seeding" &&
			strings.Contains(byPath(t, st, "c/same.bin").Error, "b/same.bin")
	})
}

// TestPublishWalkVisits stops a walk past its bound of entries, reported,
// and publishes nothing of a whole_dir walk cut short.
func TestPublishWalkVisits(t *testing.T) {
	if testing.Short() {
		t.Skip("makes ten thousand directories")
	}
	e := startEngine(t)
	dir := filepath.Join(t.TempDir(), "many")
	for i := range 10*1 + 10000 + 1 {
		if err := os.MkdirAll(filepath.Join(dir, "d"+strconv.Itoa(i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeData(t, dir, "zz.bin", 100, 81)
	r := startRole(t, e, publishJSON(t.TempDir(), entryJSON("m", dir, `"mode": "whole_dir", "max_files": 1`)), nil)
	waitFor(t, "the walk cut short", func() bool {
		st := entry(t, r, "m")
		return problem(st, "the walk stopped after 10010 entries") && problem(st, "could not be read whole") && len(st.Torrents) == 0
	})
}
