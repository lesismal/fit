package syncfile

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/lesismal/fit/logs"
)

// The connection tests of the file source and sink (I188b, fitsync.Tester):
// they look at the file system as the connectors will use it.

// maxTestFiles is the most files the source's test opens.
const maxTestFiles = 1000

// Test checks that the source can read what its paths name: the directory
// each path or glob starts from (its longest leading part without a glob
// character) is there and can be listed, and each regular file the globs
// match now opens for reading, up to the first 1000. A glob matching no
// file passes, since the source waits for its files.
func (s *Source) Test(ctx context.Context) error {
	opened := 0
	for _, p := range s.Paths {
		if err := listable(globRoot(p)); err != nil {
			return fmt.Errorf("file: %s: %w", p, err)
		}
		// Provision refused a malformed pattern, Glob's only error.
		matches, _ := filepath.Glob(p)
		for _, m := range matches {
			if err := ctx.Err(); err != nil {
				return err
			}
			if opened == maxTestFiles {
				return nil
			}
			// Only regular files: opening a FIFO would wait for a writer.
			if fi, err := os.Stat(m); err != nil || !fi.Mode().IsRegular() {
				continue
			}
			f, err := os.Open(m)
			if err != nil {
				return fmt.Errorf("file: %w", err)
			}
			f.Close()
			opened++
		}
	}
	return nil
}

// globRoot returns the directory a glob starts from: its longest leading
// directory without *, ? or [.
func globRoot(p string) string {
	d := filepath.Dir(p)
	for strings.ContainsAny(d, "*?[") {
		d = filepath.Dir(d)
	}
	return d
}

// listable reports whether dir is a directory that can be listed.
func listable(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Readdirnames(1); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// Test checks that the sink can write its path, writing no record: no other
// file sink and none of fit's logs writes it; a file already there is a
// regular file that opens for appending; and, when there is none or rotate
// makes new files beside it, its directory takes a new file, made and
// removed at once (.<name>.fit-test-*).
func (s *Sink) Test(ctx context.Context) error {
	key := filepath.Clean(s.Path)
	pathsMu.Lock()
	other := paths[key]
	pathsMu.Unlock()
	if other != nil && other != s {
		return fmt.Errorf("file: another sync file sink writes %s", s.Path)
	}
	s.mu.Lock()
	mine := s.file != nil
	s.mu.Unlock()
	if !mine && logs.DefaultFiles().File(key) != nil {
		return fmt.Errorf("file: fit's log writes %s: a sync file sink needs a file of its own", s.Path)
	}
	fi, err := os.Lstat(key)
	switch {
	case err == nil && !fi.Mode().IsRegular():
		return fmt.Errorf("file: %s is not a regular file (%s)", s.Path, fi.Mode().Type())
	case err == nil:
		f, err := os.OpenFile(key, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			return fmt.Errorf("file: %w", err)
		}
		f.Close()
		if s.Rotate == nil {
			return nil
		}
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("file: %w", err)
	}
	f, err := os.CreateTemp(filepath.Dir(key), "."+filepath.Base(key)+".fit-test-*")
	if err != nil {
		return fmt.Errorf("file: the directory of %s takes no new file: %w", s.Path, err)
	}
	f.Close()
	return os.Remove(f.Name())
}
