package medialib

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lesismal/fit/logs"
)

// The errors of a library's methods.
var (
	// ErrRemoved is the error of a library a reload removed, or whose role
	// stopped.
	ErrRemoved = errors.New("medialib: the library was removed")
	// ErrBusy is the error of work the library's file pool refused, its
	// queue full, or gave up, past file_timeout in its queue.
	ErrBusy = errors.New("medialib: the library's file pool is busy")
	// ErrChanged is the error of a file that is not as the catalog has it:
	// changed, replaced or removed since it was scanned. The next scan
	// lists it as it is.
	ErrChanged = errors.New("medialib: the file changed since it was scanned")
	// ErrOutside is the error of a file that leads out of its root, as a
	// symbolic link does, without follow_symlinks.
	ErrOutside = errors.New("medialib: the file leads out of its root")
	// ErrNotServed is the error of a rendition or subtitle listed with an
	// error of its own.
	ErrNotServed = errors.New("medialib: the file is listed with an error and not served")
	// ErrTooLarge is the error of a sample table larger than all of
	// max_table_bytes, or a subtitle past max_subtitle_bytes.
	ErrTooLarge = errors.New("medialib: past the library's bounds")
)

// Library is a named media library: its roots, scanned into a Catalog, and
// the file pool its files are read on. A *Library stays the same across
// reloads and changes in place, so that whoever holds one sees what the
// config now says; once a reload removes it, its methods fail with
// ErrRemoved.
//
// Its methods never block: what touches the disk runs on the library's file
// pool, and calls back from there.
type Library struct {
	name string
	log  *logs.Logger

	set    atomic.Pointer[settings]
	cat    atomic.Pointer[Catalog]
	pool   atomic.Pointer[Pool]
	tables tables

	removed atomic.Bool
	kick    chan struct{} // a scan asked for; one waits at most
	retime  chan struct{} // scan_interval changed
	stop    chan struct{}
	done    chan struct{} // closed when the scanner ends

	mu     sync.Mutex // guards status
	status Status

	scans, probed, probeErrors atomic.Uint64
}

// Status is what a library's scans are doing, and have done.
type Status struct {
	// Scanning is true while a scan runs; Pending while another is asked
	// for after it.
	Scanning bool `json:"scanning"`
	Pending  bool `json:"pending"`
	// Scans counts the scans ended.
	Scans uint64 `json:"scans"`
	// Started and Ended are the last scan's, Ended zero while it runs.
	Started time.Time `json:"started,omitzero"`
	Ended   time.Time `json:"ended,omitzero"`
	// Files is how many files and directories the scan has looked at;
	// Probe how many probes of its media files it needs, a file probed
	// again once when it changed while probed, and Probed how many of
	// those are done; Reused how many files were unchanged since the last
	// scan and not probed again.
	Files  int `json:"files"`
	Probe  int `json:"probe"`
	Probed int `json:"probed"`
	Reused int `json:"reused"`
}

func newLibrary(name string, s *settings, log *logs.Logger) *Library {
	l := &Library{name: name, log: log, kick: make(chan struct{}, 1), retime: make(chan struct{}, 1),
		stop: make(chan struct{}), done: make(chan struct{})}
	l.set.Store(s)
	l.cat.Store(&Catalog{byID: map[string]*Item{}})
	l.pool.Store(NewPool(s.workers, s.queue, s.timeout))
	l.tables.init(s.tableBytes)
	go l.run()
	l.Rescan()
	return l
}

// Name returns the library's name.
func (l *Library) Name() string { return l.name }

// Removed reports whether a reload removed the library.
func (l *Library) Removed() bool { return l.removed.Load() }

// Catalog returns the catalog of the library's last scan, empty before the
// first ends. It changes with each scan; what it returns does not.
func (l *Library) Catalog() *Catalog { return l.cat.Load() }

// Item returns the item of the library's catalog whose ID is id, or nil.
func (l *Library) Item(id string) *Item { return l.cat.Load().Item(id) }

// Pool returns the library's file pool, on which work that reads its files
// runs. It is replaced when a reload changes its bounds; the one replaced
// runs what it has queued.
func (l *Library) Pool() *Pool { return l.pool.Load() }

// Status returns what the library's scans are doing.
func (l *Library) Status() Status {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.status
	st.Pending = len(l.kick) > 0
	return st
}

// Roots returns the library's roots, as the config writes them.
func (l *Library) Roots() []string {
	s := l.set.Load()
	out := make([]string, len(s.roots))
	for i, r := range s.roots {
		out[i] = r.conf
	}
	return out
}

// Rescan asks for a scan of the library, and returns at once: false when
// one is already waiting to run, which will see what is there by then, or
// the library is removed.
func (l *Library) Rescan() bool {
	if l.removed.Load() {
		return false
	}
	select {
	case l.kick <- struct{}{}:
		return true
	default:
		return false
	}
}

// run is the library's scanner: it scans when asked and every
// scan_interval, one scan at a time, on a goroutine of its own, until the
// library is removed.
func (l *Library) run() {
	defer close(l.done)
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		timer.Reset(l.set.Load().interval)
		select {
		case <-l.stop:
			return
		case <-l.retime:
			continue
		case <-l.kick:
		case <-timer.C:
		}
		l.scan()
	}
}

// apply moves l to s, compiled from a config applied.
func (l *Library) apply(s *settings) {
	old := l.set.Swap(s)
	if old.workers != s.workers || old.queue != s.queue || old.timeout != s.timeout {
		if p := l.pool.Swap(NewPool(s.workers, s.queue, s.timeout)); p != nil {
			p.Close()
		}
	}
	l.tables.resize(s.tableBytes)
	if old.interval != s.interval {
		select {
		case l.retime <- struct{}{}:
		default:
		}
	}
	if old.scanKey() != s.scanKey() {
		l.Rescan()
	}
}

// remove ends the library: its scanner stops at its next step, its pool
// once it has run what it has queued, and its methods fail with ErrRemoved.
func (l *Library) remove() {
	if l.removed.Swap(true) {
		return
	}
	close(l.stop)
	l.pool.Load().Close()
	l.tables.clear()
}

// stopped reports whether the library was removed, for a scan to stop.
func (l *Library) stopped() bool {
	select {
	case <-l.stop:
		return true
	default:
		return false
	}
}
