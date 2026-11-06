package medialib

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// Handle is a file of a library opened for reading, as the catalog has it.
// Its reads block: they run on the library's pool.
type Handle struct {
	f *os.File
	// Size is the file's size.
	Size int64
}

// ReadAt reads len(p) bytes at off, as io.ReaderAt does.
func (h *Handle) ReadAt(p []byte, off int64) (int, error) { return h.f.ReadAt(p, off) }

// Close closes the file.
func (h *Handle) Close() error { return h.f.Close() }

// openFile opens f: refused with ErrOutside when its path now leads out of
// its root and follow is false, with ErrChanged when it is gone or is not
// as the catalog has it, of the same size and time. It blocks.
func openFile(f *File, follow bool) (*Handle, error) {
	target, err := filepath.EvalSymlinks(f.abs)
	if err != nil {
		return nil, notFound(err)
	}
	if !follow && !inside(f.real, target) {
		return nil, ErrOutside
	}
	file, err := os.Open(f.abs)
	if err != nil {
		return nil, notFound(err)
	}
	fi, err := file.Stat()
	switch {
	case err != nil:
	case !fi.Mode().IsRegular():
		err = errors.New("not a regular file")
	case fi.Size() != f.Size || !fi.ModTime().Equal(f.ModTime):
		err = ErrChanged
	}
	if err != nil {
		file.Close()
		return nil, err
	}
	return &Handle{f: file, Size: fi.Size()}, nil
}

// notFound is ErrChanged for a file that is gone, and err otherwise.
func notFound(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return ErrChanged
	}
	return err
}

// Open opens f, a file of the library's catalog, on the library's pool, and
// calls fn there with it, which fn closes, or with the error: ErrChanged
// for a file not as the catalog has it, which the library's sample tables
// of it are dropped for, ErrOutside for one that leads out of its root,
// ErrBusy when the pool is, and ErrRemoved once the library is. fn is
// called once: on the pool, where it may read the file, block, and must
// not wait for a fib worker; or at once, on the caller's goroutine, with
// ErrBusy or ErrRemoved when the pool does not take the job.
func (l *Library) Open(f *File, fn func(*Handle, error)) {
	l.async(func() { fn(l.OpenFile(f)) }, func(err error) { fn(nil, err) })
}

// OpenFile opens f at once, as Open does, and blocks: it is for work
// already on the library's pool.
func (l *Library) OpenFile(f *File) (*Handle, error) {
	if l.removed.Load() {
		return nil, ErrRemoved
	}
	h, err := openFile(f, l.set.Load().follow)
	if errors.Is(err, ErrChanged) {
		l.tables.drop(f.abs)
	}
	return h, err
}

// async runs run on the library's pool, or calls fail with ErrRemoved once
// the library is removed, or ErrBusy when the pool does not take run or
// gives it up.
func (l *Library) async(run func(), fail func(error)) {
	switch {
	case l.removed.Load():
		fail(ErrRemoved)
	case !l.pool.Load().Submit(run, func() { fail(ErrBusy) }):
		fail(ErrBusy)
	}
}

// Submit runs run on the library's pool, or reports false when the pool
// refuses it, its queue full or the library removed; expire, if not nil,
// runs in place of run for a job that waited past file_timeout. Code that
// serves a library's files, its segments, reads them in such jobs.
func (l *Library) Submit(run, expire func()) bool {
	return !l.removed.Load() && l.pool.Load().Submit(run, expire)
}
