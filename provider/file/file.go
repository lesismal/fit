// Package file watches a config's files and applies them each time they
// change.
//
// The watcher polls the file's size and modification time, on a goroutine
// of its own in the control plane, rather than asking the operating system
// for events: it needs no dependency, works the same everywhere, and sees a
// file an editor replaces by renaming another over it as well as one written
// in place. A change is applied once the file has stopped changing for the
// debounce time, and only when its contents differ from what was applied
// last, so that an editor's several writes make one reload and a touch none.
// A config may be read from several files and directories; their files are
// found afresh at each look, so that a file added to a directory, or taken
// out, is a change too.
package file

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

// The defaults of Options.
const (
	DefaultInterval = time.Second
	DefaultDebounce = 500 * time.Millisecond
	// DefaultMaxBytes is the largest file read: a config past it is
	// reported, not applied.
	DefaultMaxBytes = 64 << 20
)

// Options say how often the file is looked at.
type Options struct {
	// Interval is how often the file is looked at; DefaultInterval when 0.
	Interval time.Duration
	// Debounce is how long the file must stay as it is before a change is
	// applied; DefaultDebounce when 0.
	Debounce time.Duration
	// MaxBytes bounds what is read, all the files together; DefaultMaxBytes
	// when 0.
	MaxBytes int64
	// Expand returns the files the watched paths stand for, as directories
	// stand for the config files in them; the paths as they are when nil.
	Expand func(paths []string) ([]string, error)
}

// File is a file watched, and what it held when it was read.
type File struct {
	Path string
	Data []byte
}

// Watcher applies a config's files each time they change.
type Watcher struct {
	paths []string
	opts  Options
	// apply applies a config, as admin.Configs.Apply does; report hears
	// every error, the watcher's own and apply's.
	apply  func([]File) error
	report func(error)

	mu    sync.Mutex
	last  [sha256.Size]byte // the contents applied last, or found at the start; under mu
	seen  string            // the files as last looked at
	since time.Time         // when seen last changed
	dirty bool              // seen changed and the change is not handled yet
	gone  bool              // the files could not be read, and that was reported
}

// New returns a watcher of the files paths stand for, which are running
// current, what they held when it was started. apply applies a config;
// report, which may be nil, hears every error. Neither is called by more
// than one goroutine at once.
func New(paths []string, current []File, opts Options, apply func([]File) error, report func(error)) *Watcher {
	if opts.Interval <= 0 {
		opts.Interval = DefaultInterval
	}
	if opts.Debounce <= 0 {
		opts.Debounce = DefaultDebounce
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = DefaultMaxBytes
	}
	if report == nil {
		report = func(error) {}
	}
	if opts.Expand == nil {
		opts.Expand = func(paths []string) ([]string, error) { return paths, nil }
	}
	// seen starts empty rather than as the file looks now: the file may
	// have changed since current was read from it, and a stamp taken now
	// would hide that change for good, as a same-sized edit within the file
	// system's timestamp granularity, or before this look, leaves nothing
	// newer to see. So the watcher starts as if the file had just changed,
	// and the contents decide, which for an unchanged file costs one read
	// and no apply.
	return &Watcher{paths: slices.Clone(paths), opts: opts, apply: apply, report: report, last: sum(current),
		since: time.Now(), dirty: true}
}

// Run watches until ctx ends.
func (w *Watcher) Run(ctx context.Context) {
	t := time.NewTicker(w.opts.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			w.tick(now)
		}
	}
}

// Applied records that files, what the watched files hold, were applied
// another way, as SIGHUP applies them, so that the watcher does not apply
// them again. It is safe to call beside Run.
func (w *Watcher) Applied(files []File) {
	s := sum(files)
	w.mu.Lock()
	w.last = s
	w.mu.Unlock()
}

// sum is what files hold, their names and contents, hashed.
func sum(files []File) [sha256.Size]byte {
	h := sha256.New()
	for _, f := range files {
		fmt.Fprintf(h, "%d:%s%d:", len(f.Path), f.Path, len(f.Data))
		h.Write(f.Data)
	}
	var s [sha256.Size]byte
	h.Sum(s[:0])
	return s
}

// look is what a look at the files tells without reading them: which
// they are, their sizes and modification times; empty when they cannot
// be found.
func (w *Watcher) look() string {
	files, err := w.opts.Expand(w.paths)
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, f := range files {
		fi, err := os.Stat(f)
		if err != nil {
			return ""
		}
		fmt.Fprintf(&b, "%d:%s%d:%d;", len(f), f, fi.Size(), fi.ModTime().UnixNano())
	}
	return b.String()
}

// tick looks at the file once, and applies it if it changed and has stayed
// as it is for the debounce time.
func (w *Watcher) tick(now time.Time) {
	s := w.look()
	if s != w.seen {
		w.seen, w.since, w.dirty = s, now, true
		return
	}
	if !w.dirty || now.Sub(w.since) < w.opts.Debounce {
		return
	}
	w.dirty = false
	files, err := w.read()
	if err != nil {
		if !w.gone {
			w.gone = true
			w.report(err)
		}
		return
	}
	w.gone = false
	h := sum(files)
	w.mu.Lock()
	same := h == w.last
	// Recorded whether or not it applies: a broken config is reported once,
	// not at every tick, and the next edit is tried afresh.
	w.last = h
	w.mu.Unlock()
	if same {
		return // touched, or changed and changed back
	}
	if err := w.apply(files); err != nil {
		w.report(err)
	}
}

// read reads the files, refusing more than MaxBytes in all.
func (w *Watcher) read() ([]File, error) {
	paths, err := w.opts.Expand(w.paths)
	if err != nil {
		return nil, err
	}
	files := make([]File, 0, len(paths))
	left := w.opts.MaxBytes
	for _, p := range paths {
		data, err := readFile(p, left)
		if err != nil {
			return nil, err
		}
		left -= int64(len(data))
		files = append(files, File{Path: p, Data: data})
	}
	return files, nil
}

func readFile(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var b bytes.Buffer
	n, err := io.Copy(&b, io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if n > limit {
		return nil, fmt.Errorf("%s: %w", path, ErrTooLarge)
	}
	return b.Bytes(), nil
}

// ErrTooLarge is what files past MaxBytes give.
var ErrTooLarge = errors.New("the config's files are larger than the most the watcher reads")
