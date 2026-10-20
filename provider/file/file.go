// Package file watches a config file and applies it each time it changes.
//
// The watcher polls the file's size and modification time, on a goroutine
// of its own in the control plane, rather than asking the operating system
// for events: it needs no dependency, works the same everywhere, and sees a
// file an editor replaces by renaming another over it as well as one written
// in place. A change is applied once the file has stopped changing for the
// debounce time, and only when its contents differ from what was applied
// last, so that an editor's several writes make one reload and a touch none.
package file

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
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
	// MaxBytes bounds the file read; DefaultMaxBytes when 0.
	MaxBytes int64
}

// Watcher applies a config file each time it changes.
type Watcher struct {
	path string
	opts Options
	// apply applies a config, as admin.Configs.Apply does; report hears
	// every error, the watcher's own and apply's.
	apply  func([]byte) error
	report func(error)

	mu    sync.Mutex
	last  [sha256.Size]byte // the contents applied last, or found at the start; under mu
	seen  stamp             // the file as last looked at
	since time.Time         // when seen last changed
	dirty bool              // seen changed and the change is not handled yet
	gone  bool              // the file could not be read, and that was reported
}

// stamp is what a look at the file tells without reading it.
type stamp struct {
	size    int64
	modTime time.Time
	ok      bool
}

// New returns a watcher of the file at path, which is running current, the
// contents it was started with. apply applies a config; report, which may
// be nil, hears every error. Neither is called by more than one goroutine
// at once.
func New(path string, current []byte, opts Options, apply func([]byte) error, report func(error)) *Watcher {
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
	// seen starts empty rather than as the file looks now: the file may
	// have changed since current was read from it, and a stamp taken now
	// would hide that change for good, as a same-sized edit within the file
	// system's timestamp granularity, or before this look, leaves nothing
	// newer to see. So the watcher starts as if the file had just changed,
	// and the contents decide, which for an unchanged file costs one read
	// and no apply.
	return &Watcher{path: path, opts: opts, apply: apply, report: report, last: sha256.Sum256(current),
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

// Applied records that data, the file's contents, was applied another way,
// as SIGHUP applies it, so that the watcher does not apply it again. It is
// safe to call beside Run.
func (w *Watcher) Applied(data []byte) {
	sum := sha256.Sum256(data)
	w.mu.Lock()
	w.last = sum
	w.mu.Unlock()
}

func (w *Watcher) look() stamp {
	fi, err := os.Stat(w.path)
	if err != nil {
		return stamp{}
	}
	return stamp{size: fi.Size(), modTime: fi.ModTime(), ok: true}
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
	data, err := w.read()
	if err != nil {
		if !w.gone {
			w.gone = true
			w.report(err)
		}
		return
	}
	w.gone = false
	sum := sha256.Sum256(data)
	w.mu.Lock()
	same := sum == w.last
	// Recorded whether or not it applies: a broken config is reported once,
	// not at every tick, and the next edit is tried afresh.
	w.last = sum
	w.mu.Unlock()
	if same {
		return // touched, or changed and changed back
	}
	if err := w.apply(data); err != nil {
		w.report(err)
	}
}

// read reads the file, refusing one larger than MaxBytes.
func (w *Watcher) read() ([]byte, error) {
	f, err := os.Open(w.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var b bytes.Buffer
	n, err := io.Copy(&b, io.LimitReader(f, w.opts.MaxBytes+1))
	if err != nil {
		return nil, err
	}
	if n > w.opts.MaxBytes {
		return nil, fmt.Errorf("%s: %w, %d bytes", w.path, ErrTooLarge, w.opts.MaxBytes)
	}
	return b.Bytes(), nil
}

// ErrTooLarge is what a file past MaxBytes gives.
var ErrTooLarge = errors.New("the config file is larger than the most the watcher reads")
