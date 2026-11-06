package static

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/lesismal/fib/filewatch"
)

// replaceKeepingStamp puts data in the place of the file at full, with the
// size and modification time of the file it replaces, as rsync -t or cp -p
// leave a new release's files.
func replaceKeepingStamp(t *testing.T, full, data string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no inode or status change time to tell such a file by")
	}
	info, err := os.Stat(full)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(data)) != info.Size() {
		t.Fatalf("%q is not as long as %s", data, full)
	}
	tmp := filepath.Join(filepath.Dir(full), "tmp-replace")
	if err := os.WriteFile(tmp, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(tmp, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, full); err != nil {
		t.Fatal(err)
	}
}

func body(s *Static, target string) string {
	return string(get(s, "GET", target, nil).Response.Body)
}

func eventually(t *testing.T, s *Static, target, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := body(s, target)
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: still %q, want %q", target, got, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// check_interval is 5s when left out; 0 looks at the disk for every request,
// with no watch, so that the next request after a file is replaced, by one
// of the same size and modification time, has the new file.
func TestCheckIntervalZero(t *testing.T) {
	root := tree(t, map[string]string{"a.txt": "AAAA"})
	if s := mustLoad(t, root, ""); s.cache.check != DefaultCheckInterval {
		t.Fatalf("default check %v", s.cache.check)
	} else {
		s.Cleanup()
	}
	s := mustLoad(t, root, `, "check_interval": "0s"`)
	defer s.Cleanup()
	if s.cache.check != 0 || s.watch != nil {
		t.Fatalf("check %v, watch %v", s.cache.check, s.watch)
	}
	if got := body(s, "/a.txt"); got != "AAAA" {
		t.Fatalf("got %q", got)
	}
	replaceKeepingStamp(t, filepath.Join(root, "a.txt"), "BBBB")
	if got := body(s, "/a.txt"); got != "BBBB" {
		t.Fatalf("the next request after a replacement got %q", got)
	}
}

// Past check_interval, a file replaced by one of the same size and
// modification time is told by its stamp and served anew, with no watch.
func TestCheckIntervalSameStamp(t *testing.T) {
	root := tree(t, map[string]string{"a.txt": "AAAA"})
	s := mustLoad(t, root, `, "check_interval": "100ms"`)
	s.Cleanup() // no watch: only the interval tells
	s.watch = nil
	if got := body(s, "/a.txt"); got != "AAAA" {
		t.Fatalf("got %q", got)
	}
	replaceKeepingStamp(t, filepath.Join(root, "a.txt"), "BBBB")
	time.Sleep(150 * time.Millisecond)
	if got := body(s, "/a.txt"); got != "BBBB" {
		t.Fatalf("past the interval: got %q", got)
	}
}

// event forgets the lookups a change may have made stale, and no others.
func TestCacheEvent(t *testing.T) {
	root := tree(t, map[string]string{
		"a.txt": "a", "a.css": "css", "a.css.br": "br", "d/index.html": "d", "d/e/f.txt": "f", "dx.txt": "dx",
	})
	s := mustLoad(t, root, `, "check_interval": "1h", "precompressed": ["br"]`)
	s.Cleanup()
	targets := []string{"/a.txt", "/a.css", "/d/", "/d/e/f.txt", "/dx.txt", "/nope/x.txt"}
	load := func() {
		for _, target := range targets {
			get(s, "GET", target, nil)
		}
	}
	known := func() map[string]bool {
		s.cache.mu.RLock()
		defer s.cache.mu.RUnlock()
		m := map[string]bool{}
		for name := range s.cache.lookups {
			m[name] = true
		}
		return m
	}
	load()
	if got := known(); len(got) != len(targets) {
		t.Fatalf("known %v", got)
	}
	for _, tt := range []struct {
		ev      filewatch.Event
		dropped []string
	}{
		{filewatch.Event{Name: "a.txt"}, []string{"/a.txt"}},
		{filewatch.Event{Name: "a.css.br"}, []string{"/a.css"}},
		{filewatch.Event{Name: "d/index.htm"}, []string{"/d/"}},
		{filewatch.Event{Name: "d/e/g.txt"}, nil},
		{filewatch.Event{Name: "d", Dir: true}, []string{"/d/", "/d/e/f.txt"}},
		{filewatch.Event{Name: "nope", Dir: true}, []string{"/nope/x.txt"}},
		{filewatch.Event{All: true}, targets},
	} {
		load()
		s.cache.event(tt.ev)
		got := known()
		for _, target := range targets {
			dropped := !got[target]
			want := false
			for _, d := range tt.dropped {
				want = want || d == target
			}
			if dropped != want {
				t.Errorf("%+v: %s dropped %v, want %v", tt.ev, target, dropped, want)
			}
		}
		s.cache.mu.RLock()
		for dep, names := range s.cache.index {
			for name := range names {
				if s.cache.lookups[name] == nil {
					t.Errorf("%+v: index %q holds %s, which is not kept", tt.ev, dep, name)
				}
			}
		}
		s.cache.mu.RUnlock()
	}
	// A lookup that began before a change is not kept.
	l := s.resolve("a.txt")
	s.cache.event(filewatch.Event{Name: "other"})
	s.cache.keep("/a.txt", l)
	if known()["/a.txt"] {
		t.Fatal("a lookup that began before a change was kept")
	}
}

func newWatched(t *testing.T, root, extra string) *Static {
	t.Helper()
	s := mustLoad(t, root, `, "check_interval": "1h"`+extra)
	t.Cleanup(s.Cleanup)
	if s.watch == nil {
		t.Skip("no watch here")
	}
	return s
}

// Under a watch, with a long check_interval, a change shows at once: a file
// replaced by one of the same size and time, one created or removed, an
// index file coming before another, a directory swapped for another.
func TestWatchFollowsTheDisk(t *testing.T) {
	root := tree(t, map[string]string{
		"a.txt": "AAAA", "docs/index.html": "html", "assets/css/a.css": "v1", "next/css/a.css": "v2",
	})
	s := newWatched(t, root, `, "index": ["index.htm", "index.html"]`)
	eventually(t, s, "/a.txt", "AAAA")
	replaceKeepingStamp(t, filepath.Join(root, "a.txt"), "BBBB")
	eventually(t, s, "/a.txt", "BBBB")

	eventually(t, s, "/new.txt", "404 Not Found\n")
	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	eventually(t, s, "/new.txt", "new")
	if err := os.Remove(filepath.Join(root, "new.txt")); err != nil {
		t.Fatal(err)
	}
	eventually(t, s, "/new.txt", "404 Not Found\n")

	eventually(t, s, "/docs/", "html")
	if err := os.WriteFile(filepath.Join(root, "docs", "index.htm"), []byte("htm"), 0o644); err != nil {
		t.Fatal(err)
	}
	eventually(t, s, "/docs/", "htm")

	eventually(t, s, "/assets/css/a.css", "v1")
	if err := os.Rename(filepath.Join(root, "assets"), filepath.Join(root, "old")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "next"), filepath.Join(root, "assets")); err != nil {
		t.Fatal(err)
	}
	eventually(t, s, "/assets/css/a.css", "v2")
}

// A root reached through a "current" link is served from the release the
// link points at once it is pointed elsewhere.
func TestWatchRootLinkSwap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links need privileges on Windows")
	}
	base := tree(t, map[string]string{"r1/a.txt": "r1", "r2/a.txt": "r2"})
	current := filepath.Join(base, "current")
	if err := os.Symlink("r1", current); err != nil {
		t.Fatal(err)
	}
	s := newWatched(t, current, "")
	eventually(t, s, "/a.txt", "r1")
	tmp := filepath.Join(base, "tmp")
	if err := os.Symlink("r2", tmp); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, current); err != nil {
		t.Fatal(err)
	}
	eventually(t, s, "/a.txt", "r2")
}

// Cleanup stops the watch.
func TestCleanupStopsTheWatch(t *testing.T) {
	s := newWatched(t, t.TempDir(), "")
	s.Cleanup()
	if s.watch.Watch("") {
		t.Fatal("the watch still watches after Cleanup")
	}
}
