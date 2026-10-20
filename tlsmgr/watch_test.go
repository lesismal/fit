package tlsmgr

import (
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// watched is a running config, for a FileWatcher, with the files it loaded,
// and an apply that loads them again unless told to fail.
type watched struct {
	t       *testing.T
	version uint64
	files   map[string][sha256.Size]byte
	applies int
	fail    error
	reports []error
	mtime   time.Time
}

func (w *watched) current() (uint64, map[string][sha256.Size]byte) { return w.version, w.files }

func (w *watched) apply() error {
	w.applies++
	if w.fail != nil {
		return w.fail
	}
	w.load()
	return nil
}

// load is the config applied: it loads the files as they are now.
func (w *watched) load() {
	w.version++
	files := map[string][sha256.Size]byte{}
	for p := range w.files {
		data, err := os.ReadFile(p)
		if err != nil {
			w.t.Fatal(err)
		}
		files[p] = sha256.Sum256(data)
	}
	w.files = files
}

// write writes data to path with a modification time of its own, so that
// the change shows whatever the file system's timestamp resolution.
func (w *watched) write(path, data string) {
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		w.t.Fatal(err)
	}
	w.mtime = w.mtime.Add(time.Second)
	if err := os.Chtimes(path, w.mtime, w.mtime); err != nil {
		w.t.Fatal(err)
	}
}

func TestFileWatcher(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "a.crt"), filepath.Join(dir, "a.key")
	w := &watched{t: t, mtime: time.Now().Add(-time.Hour), files: map[string][sha256.Size]byte{cert: {}, key: {}}}
	w.write(cert, "cert 1")
	w.write(key, "key 1")
	w.load()
	fw := NewFileWatcher(WatchOptions{Interval: time.Second, Debounce: time.Second}, w.current, w.apply,
		func(err error) { w.reports = append(w.reports, err) })

	now := time.Now()
	step := func() {
		now = now.Add(time.Second)
		fw.tick(now)
	}
	// settle ticks until a change is past the debounce time.
	settle := func() {
		for range 3 {
			step()
		}
	}
	settle()
	if w.applies != 0 {
		t.Fatalf("applied %d times with nothing changed", w.applies)
	}

	// A renewal writes the certificate, then the key: one apply, once both
	// have stayed as they are.
	w.write(cert, "cert 2")
	step()
	w.write(key, "key 2")
	step()
	if w.applies != 0 {
		t.Fatal("applied before the files stayed as they are")
	}
	settle()
	if w.applies != 1 || w.version != 2 {
		t.Fatalf("after a renewal: %d applies, version %d", w.applies, w.version)
	}
	settle()
	if w.applies != 1 {
		t.Fatalf("applied again with the files loaded: %d", w.applies)
	}

	// Touched, and changed and changed back: nothing to load.
	w.write(cert, "cert 2")
	settle()
	w.write(key, "key X")
	step()
	w.write(key, "key 2")
	settle()
	if w.applies != 1 {
		t.Fatalf("applied for files as they were loaded: %d", w.applies)
	}

	// A pair that fails is reported once and not tried again until it
	// changes.
	w.fail = errors.New("private key does not match public key")
	w.write(cert, "cert 3")
	settle()
	settle()
	if w.applies != 2 || len(w.reports) != 1 {
		t.Fatalf("a failing pair: %d applies, reports %v", w.applies, w.reports)
	}
	w.fail = nil
	w.write(key, "key 3")
	settle()
	if w.applies != 3 || w.version != 3 {
		t.Fatalf("the pair fixed: %d applies, version %d", w.applies, w.version)
	}

	// A file that cannot be read, as while it is replaced, is reported once
	// and looked at again when it is back.
	if err := os.Remove(key); err != nil {
		t.Fatal(err)
	}
	settle()
	settle()
	if w.applies != 3 || len(w.reports) != 2 || !strings.Contains(w.reports[1].Error(), "a.key") {
		t.Fatalf("a file gone: %d applies, reports %v", w.applies, w.reports)
	}
	w.write(key, "key 4")
	settle()
	if w.applies != 4 || len(w.reports) != 2 {
		t.Fatalf("the file back: %d applies, reports %v", w.applies, w.reports)
	}
}

func TestFileWatcherNewConfig(t *testing.T) {
	// A config applied another way is compared with its files once they have
	// stayed as they are: they may have changed after it read them.
	dir := t.TempDir()
	cert := filepath.Join(dir, "a.crt")
	w := &watched{t: t, mtime: time.Now().Add(-time.Hour), files: map[string][sha256.Size]byte{cert: {}}}
	w.write(cert, "cert 1")
	w.load()
	fw := NewFileWatcher(WatchOptions{Interval: time.Second, Debounce: time.Second}, w.current, w.apply, nil)
	now := time.Now()
	for range 3 {
		now = now.Add(time.Second)
		fw.tick(now)
	}
	w.write(cert, "cert 2")
	w.version++ // a config applied by SIGHUP, which read "cert 1"
	for range 3 {
		now = now.Add(time.Second)
		fw.tick(now)
	}
	if w.applies != 1 {
		t.Fatalf("%d applies, want 1", w.applies)
	}

	// A config with no files: nothing is looked at.
	w.version, w.files = w.version+1, nil
	for range 3 {
		now = now.Add(time.Second)
		fw.tick(now)
	}
	if w.applies != 1 || len(fw.paths) != 0 {
		t.Fatalf("no files: %d applies, paths %v", w.applies, fw.paths)
	}
}
