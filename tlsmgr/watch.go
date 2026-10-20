package tlsmgr

import (
	"context"
	"crypto/sha256"
	"os"
	"slices"
	"time"
)

// The defaults of WatchOptions.
const (
	DefaultWatchInterval = time.Second
	DefaultWatchDebounce = 500 * time.Millisecond
)

// WatchOptions say how often a FileWatcher looks at the files.
type WatchOptions struct {
	// Interval is how often the files are looked at; DefaultWatchInterval
	// when 0.
	Interval time.Duration
	// Debounce is how long the files must stay as they are before a change
	// is acted on; DefaultWatchDebounce when 0.
	Debounce time.Duration
}

// FileWatcher has the running config applied again when the certificate and
// key files it loaded change, as a tool that renews certificates outside fit
// changes them, so that the listeners serve the new ones without a reload
// by hand.
//
// It polls, as the config file's watcher does (decision 006): every
// interval it looks at each file's size and modification time, on a
// goroutine of its own in the control plane, and once they have stayed as
// they are for the debounce time, reads the files and compares them with
// what the running config loaded. Applying the config again loads them as
// any reload does, so a certificate and key that do not match, as when one
// is written and the other not yet, fail and leave the running config, and
// its certificates, as they are; the same files are not tried again until
// they change.
type FileWatcher struct {
	opts WatchOptions
	// current returns the running config's version and the files it
	// loaded, with their SHA-256, by path; apply applies the running config
	// again; report hears every error.
	current func() (version uint64, files map[string][sha256.Size]byte)
	apply   func() error
	report  func(error)

	version uint64
	want    map[string][sha256.Size]byte
	paths   []string // want's paths, sorted
	seen    []stamp  // the files as last looked at, by paths' order
	since   time.Time
	dirty   bool // seen changed, or the config did, and that is not handled yet
	// failed is the sum of the files' contents that apply failed on last,
	// when tried is set; unreadable says that a file could not be read, and
	// that was reported.
	failed     [sha256.Size]byte
	tried      bool
	unreadable bool
}

// stamp is what a look at a file tells without reading it.
type stamp struct {
	size    int64
	modTime time.Time
	ok      bool
}

// NewFileWatcher returns a FileWatcher of the files current names. current
// returns the running config's version and the files it loaded; apply
// applies the running config again; report, which may be nil, hears every
// error. None is called by more than one goroutine at once.
func NewFileWatcher(opts WatchOptions, current func() (uint64, map[string][sha256.Size]byte), apply func() error, report func(error)) *FileWatcher {
	if opts.Interval <= 0 {
		opts.Interval = DefaultWatchInterval
	}
	if opts.Debounce <= 0 {
		opts.Debounce = DefaultWatchDebounce
	}
	if report == nil {
		report = func(error) {}
	}
	return &FileWatcher{opts: opts, current: current, apply: apply, report: report}
}

// Run watches until ctx ends.
func (w *FileWatcher) Run(ctx context.Context) {
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

// tick looks at the files once, and applies the config again if they have
// changed from what it loaded and have stayed as they are for the debounce
// time.
func (w *FileWatcher) tick(now time.Time) {
	if v, files := w.current(); v != w.version {
		// A config applied since the last look, by this watcher or another
		// way. Its files are compared with what it loaded once they have
		// stayed as they are, since they may have changed after it read
		// them.
		w.version, w.want = v, files
		w.paths = w.paths[:0]
		for p := range files {
			w.paths = append(w.paths, p)
		}
		slices.Sort(w.paths)
		w.seen, w.since, w.dirty = w.look(), now, len(w.paths) > 0
		return
	}
	if len(w.paths) == 0 {
		return
	}
	if s := w.look(); !slices.Equal(s, w.seen) {
		w.seen, w.since, w.dirty = s, now, true
		return
	}
	if !w.dirty || now.Sub(w.since) < w.opts.Debounce {
		return
	}
	w.dirty = false
	all := sha256.New()
	changed := false
	for _, p := range w.paths {
		data, err := readFile(p)
		if err != nil {
			// Being replaced, perhaps; its next change is looked at
			// afresh.
			if !w.unreadable {
				w.unreadable = true
				w.report(err)
			}
			return
		}
		sum := sha256.Sum256(data)
		changed = changed || sum != w.want[p]
		all.Write([]byte(p))
		all.Write(sum[:])
	}
	w.unreadable = false
	if !changed {
		return // touched, or changed and changed back
	}
	var sum [sha256.Size]byte
	all.Sum(sum[:0])
	if w.tried && sum == w.failed {
		return // failed as they are; reported then
	}
	if err := w.apply(); err != nil {
		w.failed, w.tried = sum, true
		w.report(err)
		return
	}
	w.tried = false
}

// look returns the stamps of the files, in paths' order.
func (w *FileWatcher) look() []stamp {
	s := make([]stamp, len(w.paths))
	for i, p := range w.paths {
		if fi, err := os.Stat(p); err == nil {
			s[i] = stamp{size: fi.Size(), modTime: fi.ModTime(), ok: true}
		}
	}
	return s
}
