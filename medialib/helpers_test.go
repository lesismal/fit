package medialib

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lesismal/fib"

	"github.com/lesismal/fit/config"
)

// copyLib copies testdata/lib to a new directory, which the test may change,
// and returns it.
func copyLib(t testing.TB) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.Walk("testdata/lib", func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel("testdata/lib", p)
		to := filepath.Join(dst, rel)
		if fi.IsDir() {
			return os.MkdirAll(to, 0o755)
		}
		return copyFile(p, to)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(to)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// open applies libs on an engine of the test's own, and returns the
// library named by the first; it is removed when the test ends, and its
// goroutines waited for.
func open(t testing.TB, libs ...config.MediaLibrary) (*fib.Engine, *Library) {
	t.Helper()
	key := new(fib.Engine)
	if err := Apply(key, libs, nil); err != nil {
		t.Fatal(err)
	}
	all := Libraries(key)
	t.Cleanup(func() {
		Remove(key)
		for _, l := range all {
			<-l.done
			l.Pool().Wait()
		}
	})
	return key, Lookup(key, libs[0].Name)
}

// scanned waits until l has ended n scans in all, and returns its catalog.
func scanned(t testing.TB, l *Library, n uint64) *Catalog {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		st := l.Status()
		if st.Scans >= n && !st.Scanning {
			return l.Catalog()
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: %d scans after 20s, want %d: %+v", l.name, st.Scans, n, st)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// rescan waits until l is idle, so that the next scan to end starts after
// what the test changed, scans l again and waits for that scan.
func rescan(t testing.TB, l *Library) *Catalog {
	t.Helper()
	idle(t, l)
	before := l.Status().Scans
	l.Rescan()
	return scanned(t, l, before+1)
}

// idle waits until l is neither scanning nor has a scan waiting.
func idle(t testing.TB, l *Library) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for st := l.Status(); st.Scanning || st.Pending; st = l.Status() {
		if time.Now().After(deadline) {
			t.Fatalf("%s: still scanning after 20s: %+v", l.name, st)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// touch sets the time of the file at p a little later, so that a scan sees
// it changed whatever the file system's clock.
func touch(t testing.TB, p string) {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	later := fi.ModTime().Add(3 * time.Second)
	if err := os.Chtimes(p, later, later); err != nil {
		t.Fatal(err)
	}
}

// get calls a method that calls back, and returns what it gave.
func get[T any](t testing.TB, call func(func(T, error))) (T, error) {
	t.Helper()
	type res struct {
		v   T
		err error
	}
	ch := make(chan res, 1)
	call(func(v T, err error) { ch <- res{v, err} })
	select {
	case r := <-ch:
		return r.v, r.err
	case <-time.After(20 * time.Second):
		t.Fatal("no callback after 20s")
	}
	var zero T
	return zero, nil
}
