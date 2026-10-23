package file

import (
	"errors"
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	w := New([]string{"x"}, nil, Options{}, func([]File) error { return nil }, nil)
	if w.opts.Interval != DefaultInterval || w.opts.Debounce != DefaultDebounce || w.opts.MaxBytes != DefaultMaxBytes {
		t.Errorf("options: %+v", w.opts)
	}
	w.report(errors.New("heard by no one"))
}

// TestExpandFails checks that files that cannot be found are reported,
// once.
func TestExpandFails(t *testing.T) {
	var r recorder
	w := New([]string{"x"}, nil, Options{Expand: func([]string) ([]string, error) { return nil, errors.New("no such dir") }}, r.apply, r.report)
	now := time.Now().Add(time.Hour)
	w.tick(now)
	w.tick(now.Add(time.Hour))
	if applied, errs := r.state(); len(applied) != 0 || len(errs) != 1 || errs[0].Error() != "no such dir" {
		t.Errorf("applied %q, errors %v", applied, errs)
	}
}

// TestReadDirectory checks that a path that cannot be read as a file, a
// directory, is reported.
func TestReadDirectory(t *testing.T) {
	var r recorder
	w := New([]string{t.TempDir()}, nil, Options{}, r.apply, r.report)
	now := time.Now().Add(time.Hour)
	w.tick(now) // sees the directory
	w.tick(now.Add(time.Hour))
	if applied, errs := r.state(); len(applied) != 0 || len(errs) != 1 {
		t.Errorf("applied %q, errors %v", applied, errs)
	}
}
