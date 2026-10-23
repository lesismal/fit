package file

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// recorder keeps what a watcher applied and reported.
type recorder struct {
	mu      sync.Mutex
	applied []string
	errs    []error
	refuse  string // a config apply refuses
}

func (r *recorder) apply(files []File) error {
	var data []byte
	for _, f := range files {
		data = append(data, f.Data...)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if string(data) == r.refuse {
		return errors.New("refused: " + r.refuse)
	}
	r.applied = append(r.applied, string(data))
	return nil
}

func (r *recorder) report(err error) {
	r.mu.Lock()
	r.errs = append(r.errs, err)
	r.mu.Unlock()
}

func (r *recorder) state() ([]string, []error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.applied...), append([]error(nil), r.errs...)
}

// waitFor waits until cond holds of the recorder's state.
func (r *recorder) waitFor(t *testing.T, what string, cond func(applied []string, errs []error) bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		applied, errs := r.state()
		if cond(applied, errs) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: applied %q, errors %v", what, applied, errs)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func start(t *testing.T, initial string, opts Options) (string, *recorder, *Watcher) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fit.json")
	write(t, path, initial)
	r := &recorder{}
	w := New([]string{path}, []File{{path, []byte(initial)}}, opts, r.apply, r.report)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return path, r, w
}

// write writes data to path, with a modification time of its own, since a
// file system may keep only seconds.
func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(time.Duration(len(data)) * time.Second)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
}

var fast = Options{Interval: 10 * time.Millisecond, Debounce: 50 * time.Millisecond}

func TestApplyOnChange(t *testing.T) {
	path, r, _ := start(t, "v1", fast)
	time.Sleep(100 * time.Millisecond)
	if applied, _ := r.state(); len(applied) != 0 {
		t.Fatalf("applied %q with no change", applied)
	}
	write(t, path, "v22")
	r.waitFor(t, "an edit", func(a []string, _ []error) bool { return len(a) == 1 && a[0] == "v22" })

	// A file an editor renames into place.
	tmp := path + ".tmp"
	write(t, tmp, "v333")
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	r.waitFor(t, "a rename", func(a []string, _ []error) bool { return len(a) == 2 && a[1] == "v333" })

	// Touched, its contents the same: nothing applied.
	stamp := time.Now().Add(time.Hour)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if applied, errs := r.state(); len(applied) != 2 || len(errs) != 0 {
		t.Fatalf("after a touch: applied %q, errors %v", applied, errs)
	}
}

// TestEditBeforeWatching: an edit made after the running config was read
// and before the watcher started, as fit run's listeners are up before its
// watcher, is applied, though the file shows no change once watched.
func TestEditBeforeWatching(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fit.json")
	write(t, path, "v2") // the same size as "v1", and not touched again
	r := &recorder{}
	w := New([]string{path}, []File{{path, []byte("v1")}}, fast, r.apply, r.report)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	r.waitFor(t, "the edit", func(a []string, _ []error) bool { return len(a) == 1 && a[0] == "v2" })
}

// TestDebounce: a file written several times in a row is applied once, as
// it ends up.
func TestDebounce(t *testing.T) {
	path, r, _ := start(t, "v1", Options{Interval: 10 * time.Millisecond, Debounce: 150 * time.Millisecond})
	for _, v := range []string{"a2", "a33", "a444", "a5555"} {
		write(t, path, v)
		time.Sleep(30 * time.Millisecond)
	}
	r.waitFor(t, "the last write", func(a []string, _ []error) bool { return len(a) == 1 })
	time.Sleep(200 * time.Millisecond)
	if applied, _ := r.state(); len(applied) != 1 || applied[0] != "a5555" {
		t.Fatalf("applied %q, want the last write once", applied)
	}
}

// TestBrokenEdit: an edit that does not apply is reported once and the
// watcher goes on; the next edit applies.
func TestBrokenEdit(t *testing.T) {
	path, r, _ := start(t, "v1", fast)
	r.mu.Lock()
	r.refuse = "broken!"
	r.mu.Unlock()
	write(t, path, "broken!")
	r.waitFor(t, "the broken edit", func(_ []string, e []error) bool { return len(e) == 1 })
	time.Sleep(150 * time.Millisecond)
	if applied, errs := r.state(); len(applied) != 0 || len(errs) != 1 || !strings.Contains(errs[0].Error(), "refused") {
		t.Fatalf("applied %q, errors %v", applied, errs)
	}
	write(t, path, "fixed")
	r.waitFor(t, "the fix", func(a []string, _ []error) bool { return len(a) == 1 && a[0] == "fixed" })
}

// TestFileGoneAndBack: a file that disappears is reported once; when it
// comes back, it is applied.
func TestFileGoneAndBack(t *testing.T) {
	path, r, _ := start(t, "v1", fast)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	r.waitFor(t, "the file gone", func(_ []string, e []error) bool { return len(e) == 1 })
	time.Sleep(150 * time.Millisecond)
	if _, errs := r.state(); len(errs) != 1 || !errors.Is(errs[0], os.ErrNotExist) {
		t.Fatalf("errors %v", errs)
	}
	write(t, path, "back")
	r.waitFor(t, "the file back", func(a []string, _ []error) bool { return len(a) == 1 && a[0] == "back" })
}

func TestTooLarge(t *testing.T) {
	path, r, _ := start(t, "v1", Options{Interval: 10 * time.Millisecond, Debounce: 50 * time.Millisecond, MaxBytes: 8})
	write(t, path, "123456789")
	r.waitFor(t, "a file too large", func(_ []string, e []error) bool { return len(e) == 1 && errors.Is(e[0], ErrTooLarge) })
	write(t, path, "1234")
	r.waitFor(t, "a file small enough", func(a []string, _ []error) bool { return len(a) == 1 })
}

// TestApplied: contents applied another way are not applied again.
func TestApplied(t *testing.T) {
	path, r, w := start(t, "v1", fast)
	write(t, path, "by-sighup")
	w.Applied([]File{{path, []byte("by-sighup")}})
	time.Sleep(200 * time.Millisecond)
	if applied, _ := r.state(); len(applied) != 0 {
		t.Fatalf("applied %q again", applied)
	}
}

// TestDirectory: the files a directory stands for are found at each look,
// so that a file added, changed or taken out is applied.
func TestDirectory(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.conf")
	write(t, a, "A")
	expand := func(paths []string) ([]string, error) {
		var files []string
		for _, p := range paths {
			es, err := os.ReadDir(p)
			if err != nil {
				return nil, err
			}
			for _, e := range es {
				files = append(files, filepath.Join(p, e.Name()))
			}
		}
		return files, nil
	}
	r := &recorder{}
	w := New([]string{dir}, []File{{a, []byte("A")}}, Options{Interval: 10 * time.Millisecond, Debounce: 50 * time.Millisecond, Expand: expand}, r.apply, r.report)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	write(t, filepath.Join(dir, "b.conf"), "B")
	r.waitFor(t, "a file added", func(a []string, _ []error) bool { return len(a) == 1 && a[0] == "AB" })
	if err := os.Remove(a); err != nil {
		t.Fatal(err)
	}
	r.waitFor(t, "a file taken out", func(a []string, _ []error) bool { return len(a) == 2 && a[1] == "B" })
}
