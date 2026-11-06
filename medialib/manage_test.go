package medialib

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fib/media/mp4"

	"github.com/lesismal/fit/config"
)

// exists reports whether the file at rel under dir is there.
func exists(dir, rel string) bool {
	_, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(rel)))
	return err == nil
}

func change(t *testing.T, call func(func(*Change, error))) (*Change, error) {
	t.Helper()
	return get(t, call)
}

// TestRenameItem renames an item's files named after it, keeps an ID its
// sidecar names, writes the sidecar again where it names a file renamed,
// and drops the item from the catalog until the scan that lists it anew.
func TestRenameItem(t *testing.T) {
	dir := copyLib(t)
	// sintel's sidecar names a poster after the item, which is renamed with
	// it.
	if err := copyFile(filepath.Join(dir, "films", "art", "sintel.png"), filepath.Join(dir, "films", "sintel.png")); err != nil {
		t.Fatal(err)
	}
	side := filepath.Join(dir, "films", "sintel.json")
	data, _ := os.ReadFile(side)
	if err := os.WriteFile(side, []byte(strings.Replace(string(data), "art/sintel.png", "sintel.png", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	_, l := open(t, config.MediaLibrary{Name: "lib", Roots: []string{dir}})
	scanned(t, l, 1)
	idle(t, l)

	c, err := change(t, func(fn func(*Change, error)) { l.RenameItem("films/bbb", "bigbuck", fn) })
	if err != nil {
		t.Fatal(err)
	}
	if c.Op != OpRename || c.Item != "films/bbb" || c.ID != "films/bigbuck" || c.Library != "lib" || len(c.Files) != 6 {
		t.Fatalf("%+v", c)
	}
	if l.Item("films/bbb") != nil {
		t.Error("the item renamed is still in the catalog")
	}
	for _, f := range []string{"films/bigbuck.mp4", "films/bigbuck.360p.mp4", "films/bigbuck.audio-fr.m4a", "films/bigbuck.en.srt",
		"films/bigbuck.fr.forced.vtt", "films/bigbuck.jpg"} {
		if !exists(dir, f) {
			t.Errorf("%s is not there", f)
		}
	}
	cat := rescan(t, l)
	it := cat.Item("films/bigbuck")
	if it == nil || len(it.Renditions) != 2 || len(it.Audio) != 1 || len(it.Subtitles) != 2 || it.Poster == nil || cat.Item("films/bbb") != nil {
		t.Fatalf("after the scan: %+v", it)
	}

	// An ID the sidecar names is kept; the poster named after the item is
	// renamed and named anew in the sidecar.
	c, err = change(t, func(fn func(*Change, error)) { l.RenameItem("sintel", "durian", fn) })
	if err != nil || c.ID != "sintel" {
		t.Fatalf("%+v, %v", c, err)
	}
	data, err = os.ReadFile(filepath.Join(dir, "films", "durian.json"))
	if err != nil || !strings.Contains(string(data), `"poster": "durian.png"`) || !strings.Contains(string(data), `"sintel-hd.mp4"`) {
		t.Fatalf("the sidecar: %s %v", data, err)
	}
	cat = rescan(t, l)
	if it := cat.Item("sintel"); it == nil || it.Path != "films/durian" || it.Poster == nil || it.Poster.Path != "films/durian.png" || len(it.Renditions) != 2 {
		t.Fatalf("sintel: %+v, problems %v", it, cat.Problems)
	}

	// Refused.
	if err := os.WriteFile(filepath.Join(dir, "films", "taken.mp4"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		id, name string
		want     error
	}{
		{"nothing", "x", ErrNoItem},
		{"films/bigbuck", "bigbuck", ErrNoChange},
		{"films/bigbuck", "taken", ErrExists},
		{"films/bigbuck", "../x", ErrBadPath},
		{"films/bigbuck", "a.720p", ErrBadPath},
		{"films/bigbuck", "x-poster", ErrBadPath},
		{"films/bigbuck", "x.mp4", ErrBadPath},
		{"films/bigbuck", "CON", ErrBadPath},
		{"films/bigbuck", strings.Repeat("x", 240), ErrBadPath},
	} {
		if _, err := change(t, func(fn func(*Change, error)) { l.RenameItem(c.id, c.name, fn) }); !errors.Is(err, c.want) {
			t.Errorf("%s to %q: %v, want %v", c.id, c.name, err, c.want)
		}
	}
	if !exists(dir, "films/bigbuck.mp4") || !exists(dir, "films/bigbuck.360p.mp4") {
		t.Error("a refused rename moved files")
	}
	// A file changed since the scan.
	touch(t, filepath.Join(dir, "films", "bigbuck.en.srt"))
	if _, err := change(t, func(fn func(*Change, error)) { l.RenameItem("films/bigbuck", "x", fn) }); !errors.Is(err, ErrChanged) {
		t.Errorf("changed: %v", err)
	}
}

// TestMoveItem moves an item's files to another directory of its root,
// keeping their places under the item's directory.
func TestMoveItem(t *testing.T) {
	dir := copyLib(t)
	_, l := open(t, config.MediaLibrary{Name: "lib", Roots: []string{dir}})
	scanned(t, l, 1)
	idle(t, l)
	c, err := change(t, func(fn func(*Change, error)) { l.MoveItem("sintel", "archive/2026/", fn) })
	if err != nil || c.Op != OpMove || c.ID != "sintel" || len(c.Files) != 5 {
		t.Fatalf("%+v, %v", c, err)
	}
	for _, f := range []string{"archive/2026/sintel.json", "archive/2026/sintel-hd.mp4", "archive/2026/subs/sintel.zh.srt", "archive/2026/art/sintel.png"} {
		if !exists(dir, f) {
			t.Errorf("%s is not there", f)
		}
	}
	c, err = change(t, func(fn func(*Change, error)) { l.MoveItem("films/bbb", "", fn) })
	if err != nil || c.ID != "bbb" {
		t.Fatalf("%+v, %v", c, err)
	}
	cat := rescan(t, l)
	if it := cat.Item("sintel"); it == nil || it.Path != "archive/2026/sintel" || len(it.Subtitles) != 1 || it.Poster == nil {
		t.Fatalf("sintel: %+v", it)
	}
	if it := cat.Item("bbb"); it == nil || len(it.Renditions) != 2 {
		t.Fatalf("bbb: %+v", it)
	}
	for _, c := range []struct {
		id, dir string
		want    error
	}{
		{"bbb", "", ErrNoChange},
		{"bbb", "/abs", ErrBadPath},
		{"bbb", "a/../..", ErrBadPath},
		{"bbb", `a\b`, ErrBadPath},
		{"bbb", "a:b", ErrBadPath},
		{"bbb", ".hidden", ErrBadPath},
		{"bbb", strings.Repeat("a/", MaxDepth), ErrBadPath},
		{"none", "x", ErrNoItem},
	} {
		if _, err := change(t, func(fn func(*Change, error)) { l.MoveItem(c.id, c.dir, fn) }); !errors.Is(err, c.want) {
			t.Errorf("%s to %q: %v, want %v", c.id, c.dir, err, c.want)
		}
	}
	if runtime.GOOS != "windows" {
		// A directory that leads out of the root is not written to.
		out := t.TempDir()
		if err := os.Symlink(out, filepath.Join(dir, "out")); err != nil {
			t.Fatal(err)
		}
		if _, err := change(t, func(fn func(*Change, error)) { l.MoveItem("bbb", "out/x", fn) }); !errors.Is(err, ErrOutside) {
			t.Errorf("out of the root: %v", err)
		}
		if ents, _ := os.ReadDir(out); len(ents) != 0 {
			t.Errorf("written out of the root: %v", ents)
		}
		// The second file's move fails: the first is moved back.
		if err := os.MkdirAll(filepath.Join(dir, "full", "bbb.mp4"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "full", "bbb.mp4", "x"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := change(t, func(fn func(*Change, error)) { l.MoveItem("bbb", "full", fn) }); !errors.Is(err, ErrExists) {
			t.Errorf("onto a directory: %v", err)
		}
	}
	if !exists(dir, "bbb.mp4") || !exists(dir, "bbb.360p.mp4") {
		t.Error("a refused move moved files")
	}
}

// TestDeleteItem removes an item's files, and drops it and its sample
// tables at once.
func TestDeleteItem(t *testing.T) {
	dir := copyLib(t)
	_, l := open(t, config.MediaLibrary{Name: "lib", Roots: []string{dir}})
	cat := scanned(t, l, 1)
	idle(t, l)
	bbb := cat.Item("films/bbb")
	if _, err := get(t, func(fn func(*mp4.Table, error)) { l.Table(bbb.Renditions[0], 0, fn) }); err != nil {
		t.Fatal(err)
	}
	if l.Tables().Tables != 1 {
		t.Fatal("no table kept")
	}
	before := l.Catalog().Bytes
	c, err := change(t, func(fn func(*Change, error)) { l.DeleteItem("films/bbb", fn) })
	if err != nil || c.Op != OpDelete || len(c.Files) != 6 || c.Files[0].To != "" {
		t.Fatalf("%+v, %v", c, err)
	}
	if l.Item("films/bbb") != nil || l.Tables().Tables != 0 || l.Catalog().Bytes >= before || len(l.Catalog().Items) != len(cat.Items)-1 {
		t.Errorf("the catalog or the tables keep the item: %d tables", l.Tables().Tables)
	}
	if ents, _ := os.ReadDir(filepath.Join(dir, "films")); slices.ContainsFunc(ents, func(e os.DirEntry) bool { return strings.HasPrefix(e.Name(), "bbb") }) {
		t.Errorf("files left: %v", ents)
	}
	if cat := rescan(t, l); cat.Item("films/bbb") != nil || cat.Item("sintel") == nil {
		t.Error("the scan lists the item deleted")
	}
	if _, err := change(t, func(fn func(*Change, error)) { l.DeleteItem("films/bbb", fn) }); !errors.Is(err, ErrNoItem) {
		t.Errorf("deleted twice: %v", err)
	}
	// A file gone since the scan.
	if err := os.Remove(filepath.Join(dir, "films", "subs", "sintel.zh.srt")); err != nil {
		t.Fatal(err)
	}
	if _, err := change(t, func(fn func(*Change, error)) { l.DeleteItem("sintel", fn) }); !errors.Is(err, ErrChanged) {
		t.Errorf("a file gone: %v", err)
	}
}

// TestForgottenDuringScan: an item a change takes out while a scan that
// listed it runs stays out once that scan ends.
func TestForgottenDuringScan(t *testing.T) {
	dir := copyLib(t)
	_, l := open(t, config.MediaLibrary{Name: "lib", Roots: []string{dir}})
	scanned(t, l, 1)
	idle(t, l)
	l.mu.Lock()
	edits := l.edits
	l.mu.Unlock()
	l.forget("films/bbb")
	// As a scan that started before the change ends.
	cat := (&scanner{l: l, s: l.set.Load()}).run(&Catalog{})
	if cat.Item("films/bbb") == nil {
		t.Fatal("the scan does not list it")
	}
	l.mu.Lock()
	if l.forgotten["films/bbb"] <= edits {
		t.Error("not forgotten")
	}
	l.mu.Unlock()
	cat = rescan(t, l)
	l.mu.Lock()
	n := len(l.forgotten)
	l.mu.Unlock()
	if cat.Item("films/bbb") == nil || n != 0 {
		t.Errorf("a scan after the change: %v, %d forgotten", cat.Item("films/bbb") != nil, n)
	}
}

// TestSidecarEdit reads an item's sidecar, or a draft of one, and writes it
// checked.
func TestSidecarEdit(t *testing.T) {
	dir := copyLib(t)
	_, l := open(t, config.MediaLibrary{Name: "lib", Roots: []string{dir}, MaxSidecarBytes: 1024})
	scanned(t, l, 1)
	idle(t, l)
	v, err := get(t, func(fn func(*SidecarView, error)) { l.ReadSidecar("films/bbb", fn) })
	if err != nil || v.Exists || v.Path != "films/bbb.json" || v.Sidecar.Title != "bbb" || len(v.Sidecar.Renditions) != 2 ||
		len(v.Sidecar.Audio) != 1 || len(v.Sidecar.Subtitles) != 2 || v.Sidecar.Poster != "bbb.jpg" || !slices.Contains(v.Files, "art/sintel.png") ||
		slices.Contains(v.Files, "sintel.json") {
		t.Fatalf("%+v %v", v, err)
	}
	sd := v.Sidecar
	sd.Title, sd.ID = "Big Buck Bunny", "bunny"
	sd.Audio = nil
	c, err := change(t, func(fn func(*Change, error)) { l.WriteSidecar("films/bbb", sd, time.Time{}, fn) })
	if err != nil || c.Op != OpSidecar || c.ID != "bunny" || len(c.Files) != 1 || c.Files[0].To != "films/bbb.json" {
		t.Fatalf("%+v %v", c, err)
	}
	if l.Item("films/bbb") != nil {
		t.Error("the item stays in the catalog")
	}
	cat := rescan(t, l)
	it := cat.Item("bunny")
	if it == nil || it.Title != "Big Buck Bunny" || it.Sidecar == nil || len(it.Audio) != 1 {
		// The audio file left out of the sidecar is grouped by its name.
		t.Fatalf("%+v %v", it, cat.Problems)
	}
	v, err = get(t, func(fn func(*SidecarView, error)) { l.ReadSidecar("bunny", fn) })
	if err != nil || !v.Exists || v.Sidecar.ID != "bunny" || !v.Modified.Equal(it.Sidecar.ModTime) {
		t.Fatalf("%+v %v", v, err)
	}
	// Renaming it keeps the ID its sidecar now names.
	if c, err := change(t, func(fn func(*Change, error)) { l.RenameItem("bunny", "bunny2", fn) }); err != nil || c.ID != "bunny" {
		t.Fatalf("%+v %v", c, err)
	}
	cat = rescan(t, l)
	if it := cat.Item("bunny"); it == nil || it.Path != "films/bunny2" {
		t.Fatalf("%+v", it)
	}

	for _, c := range []struct {
		id   string
		sd   Sidecar
		mod  time.Time
		want error
	}{
		{"none", Sidecar{}, time.Time{}, ErrNoItem},
		{"bunny", Sidecar{ID: "sintel"}, time.Time{}, ErrExists},
		{"bunny", Sidecar{ID: "../x"}, time.Time{}, ErrBadSidecar},
		{"bunny", Sidecar{Title: strings.Repeat("x", 2000)}, time.Time{}, ErrBadSidecar},
		{"bunny", Sidecar{Title: strings.Repeat("x", 1020)}, time.Time{}, ErrTooLarge},
		{"bunny", Sidecar{}, time.Unix(1, 0), ErrChanged},
		{"bunny", Sidecar{Poster: "../x.jpg"}, time.Time{}, ErrBadSidecar},
		{"bunny", Sidecar{Poster: "bunny2.mp4"}, time.Time{}, ErrBadSidecar},
		{"bunny", Sidecar{Poster: "none.jpg"}, time.Time{}, ErrBadSidecar},
		{"bunny", Sidecar{Renditions: []SidecarFile{{File: "art"}}}, time.Time{}, ErrBadSidecar},
		{"bunny", Sidecar{Subtitles: []SidecarSubtitle{{File: "none.srt"}}}, time.Time{}, ErrBadSidecar},
		{"bunny", Sidecar{Renditions: []SidecarFile{{File: "none.mp4"}}}, time.Time{}, ErrBadSidecar},
	} {
		if _, err := change(t, func(fn func(*Change, error)) { l.WriteSidecar(c.id, &c.sd, c.mod, fn) }); !errors.Is(err, c.want) {
			t.Errorf("%+v: %v, want %v", c.sd, err, c.want)
		}
	}
	// One whose ID is its path's, written where the item has none, and a
	// file there already.
	if err := os.WriteFile(filepath.Join(dir, "clips", "late.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	rescan(t, l)
	if _, err := change(t, func(fn func(*Change, error)) { l.WriteSidecar("clips/late", &Sidecar{Title: "Late"}, time.Time{}, fn) }); !errors.Is(err, ErrExists) {
		t.Errorf("onto a file there: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "clips", "late.json")); err != nil {
		t.Fatal(err)
	}
	if c, err := change(t, func(fn func(*Change, error)) { l.WriteSidecar("clips/late", &Sidecar{Title: "Late"}, time.Time{}, fn) }); err != nil || c.ID != "clips/late" {
		t.Fatalf("%+v %v", c, err)
	}
	if it := rescan(t, l).Item("clips/late"); it == nil || it.Title != "Late" || it.Error != "" {
		t.Fatalf("%+v", it)
	}
	// A sidecar the scan reads but the editor does not: past the bound.
	if err := os.WriteFile(filepath.Join(dir, "films", "bunny2.json"), []byte(`{"id": "bunny", "title": "`+strings.Repeat("y", 900)+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	rescan(t, l)
	if _, err := get(t, func(fn func(*SidecarView, error)) { l.ReadSidecar("bunny", fn) }); err != nil {
		t.Errorf("a large sidecar: %v", err)
	}
	if _, err := get(t, func(fn func(*SidecarView, error)) { l.ReadSidecar("none", fn) }); !errors.Is(err, ErrNoItem) {
		t.Errorf("no item: %v", err)
	}
}

// TestDirs lists where an item may be moved to.
func TestDirs(t *testing.T) {
	dir := copyLib(t)
	if err := os.MkdirAll(filepath.Join(dir, ".hidden", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, l := open(t, config.MediaLibrary{Name: "lib", Roots: []string{dir}, Exclude: []string{"clips"}})
	dirs, err := get(t, func(fn func([]string, error)) { l.Dirs(dir, fn) })
	if err != nil || strings.Join(dirs, " ") != "films films/art films/subs" {
		t.Errorf("%v %v", dirs, err)
	}
	if _, err := get(t, func(fn func([]string, error)) { l.Dirs("/none", fn) }); !errors.Is(err, ErrNoItem) {
		t.Errorf("no root: %v", err)
	}
}

// TestPathChecks: the names and paths the library writes.
func TestPathChecks(t *testing.T) {
	for _, ok := range []string{"a", "a b.mp4", "中文.mp4", "com10", "con-x", "a.b.c", strings.Repeat("x", 255)} {
		if !validName(ok) {
			t.Errorf("validName(%q)", ok)
		}
	}
	for _, bad := range []string{"", ".a", "a.", "a ", "a/b", `a\b`, "a:b", "a*b", "a?b", `a"b`, "a<b", "a|b", "a\x00b", "a\x7f",
		"CON", "con.txt", "nul", "Com1.mp4", "LPT9", "conin$", strings.Repeat("x", 256)} {
		if validName(bad) {
			t.Errorf("validName(%q)", bad)
		}
	}
	for p, want := range map[string]string{"a/b/": "a/b", "a": "a"} {
		if got, err := cleanRel(p, false); err != nil || got != want {
			t.Errorf("cleanRel(%q): %q %v", p, got, err)
		}
	}
	if got, err := cleanRel("", true); err != nil || got != "" {
		t.Error("the root")
	}
	for _, bad := range []string{"", "/a", "a//b", "../a", strings.Repeat("a", 1100)} {
		if _, err := cleanRel(bad, false); err == nil {
			t.Errorf("cleanRel(%q)", bad)
		}
	}
	r := &root{abs: filepath.Join(t.TempDir(), "none")}
	if _, err := writable(r, "a"); err == nil {
		t.Error("a root that is not there")
	}
	if relDir("a") != "" || relDir("a/b") != "a" {
		t.Error("relDir")
	}
}
