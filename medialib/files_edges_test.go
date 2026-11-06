package medialib

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
)

// TestFileChangesRemoved: a library removed, or whose root a reload took
// away, changes nothing.
func TestFileChangesRemoved(t *testing.T) {
	dir := copyLib(t)
	key, l := open(t, config.MediaLibrary{Name: "lib", Roots: []string{dir}})
	scanned(t, l, 1)
	idle(t, l)
	// The catalog's root, gone from the settings.
	s := *l.set.Load()
	s.roots = []root{{conf: "/other", abs: "/other"}}
	old := l.set.Swap(&s)
	if _, err := change(t, func(fn func(*Change, error)) { l.DeleteItem("films/bbb", fn) }); !errors.Is(err, ErrNoItem) {
		t.Errorf("a root gone: %v", err)
	}
	l.set.Store(old)
	// Removed while a change waits for the pool.
	it, r, _ := l.item("films/bbb")
	if _, err := l.do(&plan{op: OpDelete, item: it, root: r}, nil); err != nil {
		t.Fatal(err)
	}
	Remove(key)
	if _, err := l.do(&plan{op: OpDelete, item: it, root: r}, nil); !errors.Is(err, ErrRemoved) {
		t.Errorf("do: %v", err)
	}
	for _, call := range []func(func(*Change, error)){
		func(fn func(*Change, error)) { l.RenameItem("sintel", "x", fn) },
		func(fn func(*Change, error)) { l.WriteSidecar("sintel", &Sidecar{}, time.Time{}, fn) },
	} {
		if _, err := change(t, call); !errors.Is(err, ErrRemoved) {
			t.Errorf("removed: %v", err)
		}
	}
	if _, err := get(t, func(fn func([]string, error)) { l.Dirs(dir, fn) }); !errors.Is(err, ErrRemoved) {
		t.Errorf("dirs: %v", err)
	}
	if _, err := get(t, func(fn func(*SidecarView, error)) { l.ReadSidecar("sintel", fn) }); !errors.Is(err, ErrRemoved) {
		t.Errorf("sidecar: %v", err)
	}
	if err := cancelUpload(t, l, "x"); !errors.Is(err, ErrNoUpload) {
		t.Errorf("cancel: %v", err)
	}
}

// TestFileChangesBusy: a pool that takes no job refuses the changes.
func TestFileChangesBusy(t *testing.T) {
	dir := copyLib(t)
	_, l := open(t, config.MediaLibrary{Name: "lib", Roots: []string{dir}, UploadDir: filepath.Join(dir, "up")})
	scanned(t, l, 1)
	idle(t, l)
	info, err := create(t, l, "a.mp4", 10)
	if err != nil {
		t.Fatal(err)
	}
	l.pool.Load().Close()
	for _, call := range []func(func(*Change, error)){
		func(fn func(*Change, error)) { l.DeleteItem("sintel", fn) },
		func(fn func(*Change, error)) { l.MoveItem("sintel", "x", fn) },
	} {
		if _, err := change(t, call); !errors.Is(err, ErrBusy) {
			t.Errorf("busy: %v", err)
		}
	}
	if _, err := get(t, func(fn func([]string, error)) { l.Dirs(dir, fn) }); !errors.Is(err, ErrBusy) {
		t.Errorf("dirs: %v", err)
	}
	if _, err := get(t, func(fn func(*SidecarView, error)) { l.ReadSidecar("sintel", fn) }); !errors.Is(err, ErrBusy) {
		t.Errorf("sidecar: %v", err)
	}
	// Given up all the same; the sweep would remove its files.
	if err := cancelUpload(t, l, info.ID); err != nil {
		t.Errorf("cancel: %v", err)
	}
	// One cancelled while its write is refused: dropped.
	u := &upload{id: "x", root: l.set.Load().up.root, dir: t.TempDir(), size: 10, busy: true, gone: true}
	l.uploads.mu.Lock()
	l.uploads.m["x"] = u
	l.uploads.mu.Unlock()
	(&Chunk{l: l, u: u}).end(UploadInfo{}, nil)
	if l.lookupUpload("x") != nil {
		t.Error("kept")
	}
}

// TestSidecarRead: a sidecar that does not parse, or is past the bound, is
// not edited; a sidecar named after an audio file is written anew when the
// item is renamed.
func TestSidecarRead(t *testing.T) {
	dir := copyLib(t)
	side := filepath.Join(dir, "films", "bbb.json")
	if err := os.WriteFile(side, []byte(`{"title": "B", "renditions": [{"file": "bbb.mp4"}], "audio": [{"file": "bbb.audio-fr.m4a", "language": "fr"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, l := open(t, config.MediaLibrary{Name: "lib", Roots: []string{dir}})
	scanned(t, l, 1)
	idle(t, l)
	if c, err := change(t, func(fn func(*Change, error)) { l.RenameItem("films/bbb", "b2", fn) }); err != nil || len(c.Files) != 7 {
		t.Fatalf("%+v %v", c, err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "films", "b2.json"))
	if !strings.Contains(string(data), `"b2.audio-fr.m4a"`) || !strings.Contains(string(data), `"b2.mp4"`) {
		t.Fatalf("%s", data)
	}
	rescan(t, l)
	// Changed after the scan: not parsed, or past the bound.
	if err := os.WriteFile(filepath.Join(dir, "films", "b2.json"), []byte(`{"x": 1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := get(t, func(fn func(*SidecarView, error)) { l.ReadSidecar("films/b2", fn) }); !errors.Is(err, ErrChanged) {
		t.Errorf("changed: %v", err)
	}
	it := l.Item("films/b2")
	r := l.set.Load().rootOf(it.Root)
	fi, _ := os.Stat(filepath.Join(dir, "films", "b2.json"))
	f := *it.Sidecar
	f.Size, f.ModTime = fi.Size(), fi.ModTime()
	it2 := *it
	it2.Sidecar = &f
	if _, err := l.readSidecar(&it2, r); !errors.Is(err, ErrBadSidecar) {
		t.Errorf("not a sidecar: %v", err)
	}
	s := *l.set.Load()
	s.sidecarBytes = 2
	l.set.Store(&s)
	if _, err := l.readSidecar(&it2, r); !errors.Is(err, ErrTooLarge) {
		t.Errorf("too large: %v", err)
	}
	// rewriteRefs of a sidecar gone, or broken.
	p := &plan{item: it, root: r, sideOld: "films/none.json"}
	if err := p.rewriteRefs(nil); err == nil {
		t.Error("a sidecar gone")
	}
	p.sideOld = "films/b2.json"
	if err := p.rewriteRefs(nil); err == nil {
		t.Error("a broken sidecar")
	}
	if listFiles(filepath.Join(dir, "none"), 10); len(listFiles(filepath.Join(dir, "films"), 1)) != 1 {
		t.Error("listFiles' bound")
	}
	if err := writeAtomic(filepath.Join(dir, "none", "x"), nil); err == nil {
		t.Error("writeAtomic into no directory")
	}
	if err := checkRefs(&root{abs: filepath.Join(dir, "none")}, "", &Sidecar{}); err == nil {
		t.Error("checkRefs of no root")
	}
}

// TestUploadWriteFails: a write the disk refuses, an upload whose file
// went, and one taken up again from its files, broken.
func TestUploadWriteFails(t *testing.T) {
	dir, _, l := uploadLib(t, nil)
	up := filepath.Join(dir, "uploads")
	info, _ := create(t, l, "a.mp4", 100)
	// Its file went, after a restart that closed it.
	u := l.lookupUpload(info.ID)
	u.mu.Lock()
	u.f.Close()
	u.f = nil
	u.mu.Unlock()
	if err := os.Remove(filepath.Join(up, uploadPrefix+info.ID+uploadPart)); err != nil {
		t.Fatal(err)
	}
	c, _ := l.WriteUpload(info.ID, 0)
	if r := feed(t, c, make([]byte, 10), 10, nil); r.err == nil {
		t.Errorf("no file: %+v", r)
	}
	// Shorter than its offset.
	if err := os.WriteFile(filepath.Join(up, uploadPrefix+info.ID+uploadPart), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	u.mu.Lock()
	u.offset = 50
	u.mu.Unlock()
	c, _ = l.WriteUpload(info.ID, 50)
	if r := feed(t, c, make([]byte, 10), 10, nil); !errors.Is(r.err, ErrChanged) {
		t.Errorf("shorter: %+v", r)
	}
	// A failure while a write runs: the write ends the chunk with it.
	u.mu.Lock()
	u.offset = 0
	u.mu.Unlock()
	if err := os.WriteFile(filepath.Join(up, uploadPrefix+info.ID+uploadPart), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	c, _ = l.WriteUpload(info.ID, 0)
	c.mu.Lock()
	c.busy = true
	c.mu.Unlock()
	res := make(chan error, 1)
	done := func(_ UploadInfo, err error) { res <- err }
	c.Write(nil, true, io.ErrUnexpectedEOF, func(bool) {}, done)
	c.Write([]byte("x"), false, nil, func(bool) {}, done)
	c.flush()
	if err := <-res; !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("a failure while a write runs: %v", err)
	}
	if cmpErr(io.EOF, nil) != io.EOF || cmpErr(nil, io.EOF) != io.EOF {
		t.Error("cmpErr")
	}

	// Taken up again: a .json not the upload's, of another root, or past
	// its part.
	l2dir := filepath.Join(up)
	for id, meta := range map[string]string{
		"aaaa": `{"id": "other"}`,
		"bbbb": `{"id": "bbbb", "root": "/elsewhere", "path": "uploads/x.mp4", "size": 5}`,
		"cccc": `{"id": "cccc", "root": "` + filepath.ToSlash(dir) + `", "path": "uploads/x.mp4", "size": 1}`,
		"dddd": strings.Repeat(" ", maxUploadMeta+1),
	} {
		if err := os.WriteFile(filepath.Join(l2dir, uploadPrefix+id+uploadMeta), []byte(meta), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(l2dir, uploadPrefix+id+uploadPart), []byte("abc"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(l2dir, uploadPrefix+"dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		// The root's spelling in the .json is the config's.
		return
	}
	l.readUploads(l.set.Load().up, true, time.Now())
	for _, id := range []string{"aaaa", "bbbb", "cccc", "dddd"} {
		if _, ok := l.Upload(id); ok {
			t.Errorf("%s taken up", id)
		}
	}
	if _, err := os.Stat(filepath.Join(l2dir, uploadPrefix+"cccc"+uploadPart)); err == nil {
		t.Error("a broken upload's part is kept")
	}
	if _, ok := readUploadMeta(filepath.Join(l2dir, "none"), "x"); ok {
		t.Error("readUploadMeta of no file")
	}
	if l.UploadSettings() == nil || l.UploadSettings().Dir != "uploads" {
		t.Errorf("%+v", l.UploadSettings())
	}
}
