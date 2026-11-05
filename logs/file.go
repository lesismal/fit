package logs

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/filepool"
)

// What a file sink holds and when it looks at its directory again.
const (
	// QueueBytes is the most a sink takes from the loggers writing to it
	// and has not yet written: past it a write is refused, and the caller
	// counts it, rather than waiting for a slow disk.
	QueueBytes = 1 << 20
	// queueMessages bounds the buffers in the queue as well as bytes; one
	// buffer is one write.
	queueMessages = 256
	// sweepEvery is how often the sink removes rotated files that passed
	// their age limit even though nothing rotated.
	sweepEvery = 10 * time.Minute
	// compressWorkers and compressQueue bound the compressions at once.
	compressWorkers = 1
	compressQueue   = 16
	compressTimeout = time.Minute
	// maxRotatedNames bounds the names a sweep deletes at once, so that a
	// directory of files the sink did not make cannot hold it long.
	maxRotatedNames = 10000
	// maxBackupNames bounds the names a rotation tries, the time's and its
	// -1, -2 and so on, before it gives up and keeps writing the file.
	maxBackupNames = 1000
	// rotateRetry is how long a size or interval rotation that failed waits
	// before it is tried again, so that a name that cannot be made does not
	// cost every write a try and a warning.
	rotateRetry = time.Minute
	// fileMode is the mode a log file is created with: its owner writes it,
	// its group may read it, and others may not, since access logs hold
	// addresses, URLs and user names. A file already there keeps its mode, a
	// file a rotation starts takes the mode of the one it replaces, and a
	// compressed file the mode of the file it compresses.
	fileMode os.FileMode = 0o640
	// appendFlags open a log file to be appended to, created when missing.
	appendFlags = os.O_WRONLY | os.O_APPEND | os.O_CREATE
)

// ErrClosed is returned by a write to a file sink that was closed.
var ErrClosed = errors.New("logs: the file is closed")

// ErrQueueFull is returned by a write the sink's queue has no room for; the
// write is counted as dropped and not waited for.
var ErrQueueFull = errors.New("logs: the file's queue is full")

// rotatedSuffix is what follows a rotated file's name: the time of the
// rotation in UTC, a number when that name was taken, and .gz when it was
// compressed, as in fit.log.2026-11-05T04-00-00.000.gz.
var rotatedSuffix = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2}\.\d{3}(-\d+)?(\.gz)?$`)

// rotate is the resolved settings of one file output. The zero value
// rotates nothing, which is what a file output that names no rotate keeps.
type rotate struct {
	maxBytes      int64
	interval      time.Duration
	maxAge        time.Duration
	maxFiles      int
	maxTotalBytes int64
	compress      bool
}

// rotateOf resolves a config's rotate, or nil for none.
func rotateOf(r *config.Rotate) *rotate {
	if r == nil {
		return nil
	}
	return &rotate{
		maxBytes:      r.MaxBytesOrDefault(),
		interval:      r.IntervalOrDefault(),
		maxAge:        r.MaxAgeOrDefault(),
		maxFiles:      r.MaxFilesOrDefault(),
		maxTotalBytes: r.MaxTotalBytesOrDefault(),
		compress:      r.CompressOrDefault(),
	}
}

// equal reports whether two settings rotate and keep files alike.
func (r *rotate) equal(o *rotate) bool {
	if r == nil || o == nil {
		return r == o
	}
	return *r == *o
}

// bounded reports whether settings bound anything at all.
func (r *rotate) bounded() bool {
	return r != nil && (r.maxBytes > 0 || r.interval > 0 || r.maxAge > 0 || r.maxFiles > 0 || r.maxTotalBytes > 0)
}

// Files is the set of log files the process has open: one sink per path,
// shared by every logger writing there and by the access log. A sink writes
// on a goroutine of its own, so that no logger and no fib worker touches the
// disk; rotation, removal of old files and compression happen there and on
// internal/filepool.
type Files struct {
	mu    sync.Mutex
	files map[string]*File // by cleaned path
	// pool runs the compressions; it is made when the first rotated file
	// is compressed, so that a log without compress starts no goroutines.
	pool     atomic.Pointer[filepool.Pool]
	poolOnce sync.Once
	now      func() time.Time
	every    time.Duration
}

// NewFiles returns an empty set of log files.
func NewFiles() *Files {
	return &Files{
		files: map[string]*File{},
		now:   time.Now,
		every: sweepEvery,
	}
}

// compressPool returns the pool the compressions run on, making it the first
// time.
func (fs *Files) compressPool() *filepool.Pool {
	if p := fs.pool.Load(); p != nil {
		return p
	}
	fs.poolOnce.Do(func() {
		fs.pool.Store(filepool.New(compressWorkers, compressQueue, compressTimeout))
	})
	return fs.pool.Load()
}

// Open returns the sink of the file at path, opening it if this is the first
// logger to write there, and adding a reference. Every Open is released with
// Release. rot may be nil, which rotates nothing. zone returns the zone the
// file's intervals turn in, and is read as the file is written.
func (fs *Files) Open(path string, rot *config.Rotate, zone func() *time.Location) (*File, error) {
	key := filepath.Clean(path)
	resolved := rotateOf(rot)
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if f := fs.files[key]; f != nil {
		if !f.rot.Load().equal(resolved) {
			return nil, fmt.Errorf("logs: file %q is open with other rotation settings: a file has one rotate", path)
		}
		f.refs++
		return f, nil
	}
	f, err := openFile(fs, key, resolved, zone)
	if err != nil {
		return nil, err
	}
	f.refs = 1
	fs.files[key] = f
	return f, nil
}

// Release drops one reference to f, closing it and its file when the last
// logger letting go of it does, and reports what closing the file failed
// with.
func (fs *Files) Release(f *File) error {
	if f == nil {
		return nil
	}
	fs.mu.Lock()
	f.refs--
	last := f.refs <= 0
	if last {
		delete(fs.files, f.path)
	}
	fs.mu.Unlock()
	if last {
		return f.close()
	}
	return nil
}

// File returns the sink of path, when the process has it open.
func (fs *Files) File(path string) *File {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.files[filepath.Clean(path)]
}

// List returns a snapshot of every file sink, for the workbench.
func (fs *Files) List() []Info {
	fs.mu.Lock()
	files := make([]*File, 0, len(fs.files))
	for _, f := range fs.files {
		files = append(files, f)
	}
	fs.mu.Unlock()
	infos := make([]Info, 0, len(files))
	for _, f := range files {
		infos = append(infos, f.info())
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Path < infos[j].Path })
	return infos
}

// Rotate rotates the file at path now, as the workbench's Rotate now does.
func (fs *Files) Rotate(path string) error {
	f := fs.File(path)
	if f == nil {
		return fmt.Errorf("no log file is open at %q", path)
	}
	return f.rotateNow()
}

// Cleanup removes the files past the sink's retention now, or reports the
// ones it would remove when dryRun is set, as the workbench's Clean up now
// does.
func (fs *Files) Cleanup(path string, dryRun bool) ([]Removed, error) {
	f := fs.File(path)
	if f == nil {
		return nil, fmt.Errorf("no log file is open at %q", path)
	}
	return f.cleanupNow(dryRun)
}

// File is one log file: its queue, its writer goroutine, and the file it
// currently writes. It is safe for concurrent use.
type File struct {
	path  string
	dir   string
	base  string
	files *Files
	zone  func() *time.Location
	now   func() time.Time

	rot atomic.Pointer[rotate]

	// q holds what wait to be written; queued counts their bytes, so that a
	// slow disk costs at most QueueBytes, whatever the writers' pace.
	q       chan []byte
	queued  atomic.Int64
	dropped atomic.Int64
	lastErr atomic.Pointer[string]

	// ctl runs a rotation or a cleanup on the writer's goroutine, where the
	// file's own state is.
	ctl chan *control

	// rmu orders a write against Close: a write holds it for reading while
	// it queues, so that the queue cannot be closed under it.
	rmu    sync.RWMutex
	closed bool

	// mu guards refs and the compressed files in flight.
	mu          sync.Mutex
	refs        int
	compressing map[string]bool

	// status is what the writer last wrote, for the workbench.
	statusMu    sync.Mutex
	current     string
	currentSize int64

	done chan struct{}

	// file, size, periodStart and buf belong to the writer goroutine.
	file        *os.File
	size        int64
	periodStart time.Time
	// retryAt is when a size or interval rotation that failed is tried again.
	retryAt time.Time
	// closeErr is what closing the file failed with; the writer sets it
	// before done is closed.
	closeErr error
}

// control is an operation the writer goroutine runs.
type control struct {
	rotate  bool
	cleanup bool
	dryRun  bool
	removed []Removed
	err     error
	done    chan struct{}
}

// openFile opens the sink of a path. It is called with fs.mu held.
func openFile(fs *Files, path string, rot *rotate, zone func() *time.Location) (*File, error) {
	f := &File{
		path:        path,
		dir:         filepath.Dir(path),
		base:        filepath.Base(path),
		files:       fs,
		zone:        zone,
		now:         fs.now,
		q:           make(chan []byte, queueMessages),
		ctl:         make(chan *control),
		compressing: map[string]bool{},
		done:        make(chan struct{}),
	}
	f.rot.Store(rot)
	fh, info, err := openRegular(path, appendFlags, fileMode)
	if err != nil {
		return nil, err
	}
	if mode, ok := othersMayWrite(f.dir); ok {
		Named(config.LoggerEngine).Warnf("log %s: its directory %s is writable by users other than its owner (%v); "+
			"fit does not follow links there, but such a user can still remove or rename the log's files", path, f.dir, mode)
	}
	f.file = fh
	f.size = info.Size()
	if rot != nil && rot.interval > 0 {
		f.periodStart = periodStart(fs.now(), rot.interval, zone())
	}
	f.current = path
	f.currentSize = f.size
	go f.run()
	return f, nil
}

// SetRotate changes how the file is rotated and kept, as a reload does. The
// writer takes the new settings at its next write; a file open when an
// interval is turned on turns at the first write past a period boundary.
func (f *File) SetRotate(cfg *config.Rotate) {
	f.rot.Store(rotateOf(cfg))
}

// run writes what the loggers queue, rotates the file when its size or
// interval says so, removes old files, and compresses rotated ones.
func (f *File) run() {
	defer close(f.done)
	t := time.NewTicker(f.files.every)
	defer t.Stop()
	f.sweep()
	for {
		select {
		case b, ok := <-f.q:
			if !ok {
				f.closeFile()
				return
			}
			f.queued.Add(-int64(len(b)))
			f.writeBuffer(b)
			f.putBuffer(b)
		case c := <-f.ctl:
			f.drain()
			switch {
			case c.rotate:
				c.err = f.rotateNowOnWriter()
			case c.cleanup:
				c.removed, c.err = f.retention(c.dryRun)
			}
			close(c.done)
		case <-t.C:
			f.sweep()
		}
	}
}

// drain writes what is queued, in order, before a control operation runs, so
// that a rotation now or a cleanup now sees the file as the loggers made it.
func (f *File) drain() {
	for {
		select {
		case b, ok := <-f.q:
			if !ok {
				return
			}
			f.queued.Add(-int64(len(b)))
			f.writeBuffer(b)
			f.putBuffer(b)
		default:
			return
		}
	}
}

// writeBuffer writes one buffer, rotating the file first when the write
// would pass its size or a period ended.
func (f *File) writeBuffer(b []byte) {
	if f.file == nil {
		f.dropped.Add(1)
		return
	}
	if f.needRotate(len(b), f.now()) {
		f.rotate(f.now())
		if f.file == nil {
			f.dropped.Add(1)
			return
		}
	}
	n, err := f.file.Write(b)
	f.size += int64(n)
	f.statusMu.Lock()
	f.currentSize = f.size
	f.statusMu.Unlock()
	if err != nil {
		f.setErr(fmt.Errorf("write %s: %w", f.path, err))
	}
}

// needRotate reports whether a write of n bytes rotates the file.
func (f *File) needRotate(n int, now time.Time) bool {
	rot := f.rot.Load()
	if rot == nil || now.Before(f.retryAt) {
		return false
	}
	if rot.maxBytes > 0 && f.size > 0 && f.size+int64(n) > rot.maxBytes {
		return true
	}
	if rot.interval > 0 {
		start := periodStart(now, rot.interval, f.zone())
		if f.periodStart.IsZero() {
			f.periodStart = start
			return false
		}
		if !start.Equal(f.periodStart) {
			return true
		}
	}
	return false
}

// rotate renames the open file out of the way, starts a new one and, when
// asked, compresses the old one in the background. A rotation that fails
// keeps the file it has, says why, and is not tried again by size or
// interval for rotateRetry.
func (f *File) rotate(now time.Time) error {
	if f.file == nil {
		return nil
	}
	rot := f.rot.Load()
	if rot == nil {
		return nil
	}
	if f.size == 0 {
		// Nothing to rotate yet: the period starts again here.
		if rot.interval > 0 {
			f.periodStart = periodStart(now, rot.interval, f.zone())
		}
		return nil
	}
	backup, err := f.backupName(now)
	if err != nil {
		err = fmt.Errorf("rotate %s: %w", f.path, err)
		f.setErr(err)
		f.retryAt = now.Add(rotateRetry)
		return err
	}
	// The file as it is held: the new file takes its mode, and the
	// compression reads this file and no other that a name may lead to.
	held, statErr := f.file.Stat()
	perm := fileMode
	if statErr == nil {
		perm = held.Mode().Perm()
	}
	if err := f.file.Close(); err != nil {
		f.setErr(fmt.Errorf("rotate %s: %w", f.path, err))
	}
	f.file = nil
	if err := os.Rename(f.path, backup); err != nil {
		err = fmt.Errorf("rotate %s: %w", f.path, err)
		f.setErr(err)
		f.retryAt = now.Add(rotateRetry)
		// Keep appending to the file that could not be renamed.
		if fh, _, e := openRegular(f.path, appendFlags, perm); e != nil {
			f.setErr(e)
		} else {
			f.file = fh
		}
		return err
	}
	if rot.compress {
		if statErr != nil {
			f.setErr(fmt.Errorf("compress %s: %w", backup, statErr))
		} else {
			f.startCompress(backup, held)
		}
	}
	fh, _, err := openRegular(f.path, appendFlags, perm)
	if err != nil {
		f.setErr(err)
		return err
	}
	f.file = fh
	f.statusMu.Lock()
	f.current = f.path
	f.currentSize = 0
	f.statusMu.Unlock()
	if rot.interval > 0 {
		f.periodStart = periodStart(now, rot.interval, f.zone())
	}
	f.retention(false)
	return nil
}

// backupName returns the name the current file is renamed to: the path, a
// dot and the time in UTC, with -1, -2 and so on when the name is taken. It
// fails when a name cannot be looked up, too long for the file system or in
// a directory it may not read, and when maxBackupNames names are taken.
func (f *File) backupName(now time.Time) (string, error) {
	stamp := now.UTC().Format("2006-01-02T15-04-05.000")
	for i := 0; i < maxBackupNames; i++ {
		name := filepath.Join(f.dir, f.base+"."+stamp)
		if i > 0 {
			name += "-" + itoa(i)
		}
		free, err := nameFree(name)
		if err != nil {
			return "", err
		}
		if free {
			return name, nil
		}
	}
	return "", fmt.Errorf("the %d names after %s.%s are taken", maxBackupNames, f.base, stamp)
}

// nameFree reports whether neither a rotated file nor what its compression
// writes is at name.
func nameFree(name string) (bool, error) {
	for _, n := range [...]string{name, name + ".gz", name + ".gz.tmp"} {
		_, err := os.Lstat(n)
		if err == nil {
			return false, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	return true, nil
}

// openRegular opens path with flag, creating it with perm when flag says so,
// never through a symbolic link at its last element, and only when it is a
// regular file with no other hard link, so that no name planted in the log's
// directory can lead fit to write, truncate or read another file.
func openRegular(path string, flag int, perm os.FileMode) (*os.File, os.FileInfo, error) {
	fh, err := openNoFollow(path, flag, perm)
	if err != nil {
		return nil, nil, err
	}
	fi, err := fh.Stat()
	if err == nil {
		err = checkFile(fi)
	}
	if err != nil {
		fh.Close()
		return nil, nil, fmt.Errorf("open %s: %w", path, err)
	}
	return fh, fi, nil
}

// closeFile closes the file; the queue is empty by then.
func (f *File) closeFile() {
	if f.file != nil {
		if err := f.file.Close(); err != nil {
			f.setErr(err)
			f.closeErr = err
		}
		f.file = nil
	}
}

// Write queues p to be written. It never waits for the disk: a full queue
// refuses it, ErrQueueFull, and the caller counts the loss.
func (f *File) Write(p []byte) (int, error) {
	f.rmu.RLock()
	defer f.rmu.RUnlock()
	if f.closed {
		return 0, ErrClosed
	}
	if len(p) == 0 {
		return 0, nil
	}
	if f.queued.Load()+int64(len(p)) > QueueBytes {
		f.dropped.Add(1)
		return 0, ErrQueueFull
	}
	b := f.getBuffer(len(p))
	copy(b, p)
	f.queued.Add(int64(len(p)))
	select {
	case f.q <- b:
		return len(p), nil
	default:
		f.queued.Add(-int64(len(p)))
		f.putBuffer(b)
		f.dropped.Add(1)
		return 0, ErrQueueFull
	}
}

// Close stops the sink: what is queued is written, then the file is closed.
// Writes after Close are refused.
func (f *File) close() error {
	f.rmu.Lock()
	if f.closed {
		f.rmu.Unlock()
		return nil
	}
	f.closed = true
	close(f.q)
	f.rmu.Unlock()
	<-f.done
	return f.closeErr
}

// rotateNow rotates the file on the writer's goroutine.
func (f *File) rotateNow() error {
	return f.runCtl(&control{rotate: true})
}

// rotateNowOnWriter is the rotation a control runs, after the queue is
// written out.
func (f *File) rotateNowOnWriter() error {
	return f.rotate(f.now())
}

// cleanupNow removes what retention says on the writer's goroutine.
func (f *File) cleanupNow(dryRun bool) ([]Removed, error) {
	c := &control{cleanup: true, dryRun: dryRun, done: make(chan struct{})}
	if err := f.sendCtl(c); err != nil {
		return nil, err
	}
	<-c.done
	return c.removed, c.err
}

// runCtl runs one control operation on the writer's goroutine.
func (f *File) runCtl(c *control) error {
	c.done = make(chan struct{})
	if err := f.sendCtl(c); err != nil {
		return err
	}
	<-c.done
	return c.err
}

// sendCtl hands c to the writer, unless the sink is closed.
func (f *File) sendCtl(c *control) error {
	f.rmu.RLock()
	defer f.rmu.RUnlock()
	if f.closed {
		return ErrClosed
	}
	f.ctl <- c
	return nil
}

// Removed is a rotated file a cleanup removed, or would remove.
type Removed struct {
	Name    string
	Size    int64
	ModTime time.Time
}

// rotatedFile is a rotated file a sweep found.
type rotatedFile struct {
	name string
	size int64
	mod  time.Time
}

// retention removes the rotated files past the settings' age, count and
// total size, oldest first, and returns them; dryRun only reports them. It
// is run on the writer goroutine.
func (f *File) retention(dryRun bool) ([]Removed, error) {
	rot := f.rot.Load()
	if !rot.bounded() {
		return nil, nil
	}
	if rot.maxAge <= 0 && rot.maxFiles <= 0 && rot.maxTotalBytes <= 0 {
		return nil, nil
	}
	entries, err := os.ReadDir(f.dir)
	if err != nil {
		f.setErr(fmt.Errorf("list %s: %w", f.dir, err))
		return nil, err
	}
	rotated := make([]rotatedFile, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if name == f.base || !f.isRotated(name) || f.isCompressing(name) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		rotated = append(rotated, rotatedFile{name: name, size: info.Size(), mod: info.ModTime()})
		if len(rotated) > maxRotatedNames {
			break
		}
	}
	sort.Slice(rotated, func(i, j int) bool { return rotated[i].mod.After(rotated[j].mod) })

	remove := make([]Removed, 0, len(rotated))
	now := f.now()
	kept := rotated[:0]
	if rot.maxAge > 0 {
		for _, rf := range rotated {
			if now.Sub(rf.mod) > rot.maxAge {
				remove = append(remove, Removed{Name: rf.name, Size: rf.size, ModTime: rf.mod})
				continue
			}
			kept = append(kept, rf)
		}
	} else {
		kept = append(kept, rotated...)
	}
	if rot.maxFiles > 0 && len(kept) > rot.maxFiles {
		for _, rf := range kept[rot.maxFiles:] {
			remove = append(remove, Removed{Name: rf.name, Size: rf.size, ModTime: rf.mod})
		}
		kept = kept[:rot.maxFiles]
	}
	if rot.maxTotalBytes > 0 {
		var total int64
		for _, rf := range kept {
			total += rf.size
		}
		for len(kept) > 0 && total > rot.maxTotalBytes {
			last := kept[len(kept)-1]
			total -= last.size
			remove = append(remove, Removed{Name: last.name, Size: last.size, ModTime: last.mod})
			kept = kept[:len(kept)-1]
		}
	}
	if dryRun {
		return remove, nil
	}
	for _, rf := range remove {
		if err := os.Remove(filepath.Join(f.dir, rf.Name)); err != nil && !os.IsNotExist(err) {
			f.setErr(fmt.Errorf("remove %s: %w", rf.Name, err))
		}
	}
	return remove, nil
}

// sweep runs retention on the writer's goroutine.
func (f *File) sweep() {
	if _, err := f.retention(false); err != nil {
		f.setErr(err)
	}
}

// isRotated reports whether name is one a rotation made from this file.
func (f *File) isRotated(name string) bool {
	n := strings.TrimPrefix(name, f.base+".")
	return n != name && rotatedSuffix.MatchString(n)
}

// isCompressing reports whether name is being compressed.
func (f *File) isCompressing(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.compressing[filepath.Join(f.dir, name)]
}

// startCompress gzips a rotated file on the file pool, keeping the plain
// file when it fails. held is the file as the writer held it.
func (f *File) startCompress(name string, held os.FileInfo) {
	f.mu.Lock()
	f.compressing[name] = true
	f.mu.Unlock()
	run := func() { f.compress(name, held) }
	expire := func() {
		f.setErr(fmt.Errorf("compress %s: the file pool is busy; the file is kept as it is", name))
		f.finishCompress(name)
	}
	if !f.files.compressPool().Submit(run, expire) {
		expire()
	}
}

// finishCompress forgets a file being compressed.
func (f *File) finishCompress(name string) {
	f.mu.Lock()
	delete(f.compressing, name)
	f.mu.Unlock()
}

// compress writes name's gzip to name.gz and removes the plain file. It
// reads name only when it is still the file the writer held, held, and
// writes only a file it creates, so that a link or another file put at
// either name is neither read nor written through; the plain file is kept
// otherwise. The gzip takes held's mode.
func (f *File) compress(name string, held os.FileInfo) {
	defer f.finishCompress(name)
	in, fi, err := openRegular(name, os.O_RDONLY, 0)
	if err == nil && !os.SameFile(fi, held) {
		in.Close()
		err = errors.New("the name no longer holds the file rotated")
	}
	if err != nil {
		f.setErr(fmt.Errorf("compress %s: %w", name, err))
		return
	}
	tmp := name + ".gz.tmp"
	// O_EXCL: a new file or none. One already there, a link included, is
	// not written through, and the plain file is kept.
	out, err := openNoFollow(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		in.Close()
		f.setErr(fmt.Errorf("compress %s: %w", name, err))
		return
	}
	modeErr := out.Chmod(held.Mode().Perm())
	gz := gzip.NewWriter(out)
	_, cpErr := io.Copy(gz, in)
	gzErr := gz.Close()
	outErr := out.Close()
	inErr := in.Close()
	if err := errors.Join(modeErr, cpErr, gzErr, outErr, inErr); err != nil {
		os.Remove(tmp)
		f.setErr(fmt.Errorf("compress %s: %w", name, err))
		return
	}
	if err := os.Rename(tmp, name+".gz"); err != nil {
		os.Remove(tmp)
		f.setErr(fmt.Errorf("compress %s: %w", name, err))
		return
	}
	if err := os.Remove(name); err != nil && !os.IsNotExist(err) {
		f.setErr(fmt.Errorf("compress %s: %w", name, err))
	}
}

// Info returns a snapshot of the sink for the workbench.
func (f *File) Info() Info { return f.info() }

// info returns a snapshot of the sink for the workbench.
func (f *File) info() Info {
	f.statusMu.Lock()
	current, size := f.current, f.currentSize
	f.statusMu.Unlock()
	info := Info{Path: f.path, Current: current, CurrentSize: size, Dropped: f.dropped.Load()}
	if p := f.lastErr.Load(); p != nil {
		info.LastError = *p
	}
	entries, err := os.ReadDir(f.dir)
	if err != nil {
		return info
	}
	for _, e := range entries {
		name := e.Name()
		if !f.isRotated(name) || f.isCompressing(name) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		info.Files = append(info.Files, FileInfo{Name: name, Size: fi.Size(), Modified: fi.ModTime(), Current: false})
	}
	sort.Slice(info.Files, func(i, j int) bool { return info.Files[i].Modified.After(info.Files[j].Modified) })
	if current != "" {
		if fi, err := os.Stat(current); err == nil {
			info.Files = append([]FileInfo{{Name: filepath.Base(current), Size: fi.Size(), Modified: fi.ModTime(), Current: true}}, info.Files...)
		} else {
			info.Files = append([]FileInfo{{Name: filepath.Base(current), Size: size, Current: true}}, info.Files...)
		}
	}
	return info
}

// Info is a file sink as the workbench shows it.
type Info struct {
	Path        string
	Current     string
	CurrentSize int64
	Dropped     int64
	LastError   string
	Files       []FileInfo // the current file first, then the rotated ones, newest first
}

// FileInfo is one file of a sink.
type FileInfo struct {
	Name     string
	Size     int64
	Modified time.Time
	Current  bool
}

// setErr remembers the last error of the sink, for the workbench, and says
// it in the engine log.
func (f *File) setErr(err error) {
	if err == nil {
		return
	}
	msg := err.Error()
	f.lastErr.Store(&msg)
	Named(config.LoggerEngine).Warnf("log %s: %v", f.path, err)
}

// getBuffer returns a buffer with room for n bytes, from the pool of
// buffers written before when it has one.
func (f *File) getBuffer(n int) []byte {
	if b := getPooledBuffer(); cap(b) >= n {
		return b[:n]
	} else {
		putPooledBuffer(b[:0])
		return make([]byte, n)
	}
}

func (f *File) putBuffer(b []byte) { putPooledBuffer(b[:0]) }

var bufferPool = sync.Pool{New: func() any { b := make([]byte, 0, 64<<10); return &b }}

func getPooledBuffer() []byte  { return *bufferPool.Get().(*[]byte) }
func putPooledBuffer(b []byte) { bufferPool.Put(&b) }

// periodStart returns the start of the period now is in, of length d, in
// loc: the midnight its day starts at, plus whole periods from there — so
// 24h turns at midnight and 1h on the hour. d must be under a day or a whole
// number of days, which config validation ensures.
func periodStart(now time.Time, d time.Duration, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.Local
	}
	t := now.In(loc)
	y, m, day := t.Date()
	midnight := time.Date(y, m, day, 0, 0, 0, 0, loc)
	if d <= 0 {
		return midnight
	}
	if d >= 24*time.Hour {
		days := int64(d / (24 * time.Hour))
		// Group days from 1970-01-01 in the zone, by civil date, so that a
		// day is a day whatever its length.
		n := time.Date(y, m, day, 0, 0, 0, 0, time.UTC).Unix() / 86400
		start := (n / days) * days
		y2, m2, d2 := time.Unix(start*86400, 0).UTC().Date()
		return time.Date(y2, m2, d2, 0, 0, 0, 0, loc)
	}
	since := t.Sub(midnight)
	return midnight.Add(since / d * d)
}

// itoa is strconv.Itoa without the import, for names.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	n := len(b)
	for i > 0 {
		n--
		b[n] = byte('0' + i%10)
		i /= 10
	}
	return string(b[n:])
}
