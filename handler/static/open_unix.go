//go:build unix

package static

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"sync/atomic"
	"syscall"

	"github.com/lesismal/fib/bufferpool"
)

// openRead opens the file at full for reading, as os.Open does but without
// the setup os.Open makes for a descriptor a poller could wait on, which a
// file cannot: on Linux two fcntl calls to make it non-blocking, an epoll_ctl
// that fails, and two more fcntl calls to make it blocking again.
func openRead(full string) (int, error) {
	for {
		fd, err := syscall.Open(full, syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
		if err == nil {
			return fd, nil
		}
		if err != syscall.EINTR {
			return -1, &fs.PathError{Op: "open", Path: full, Err: err}
		}
	}
}

// openFile opens the file at full for reading, for SendFile: openRead's
// descriptor in an os.File, which costs one fcntl more.
func openFile(full string) (*os.File, error) {
	fd, err := openRead(full)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), full), nil
}

// readSmall reads the whole file at full, of size bytes when it was looked
// at, into a buffer of package bufferpool: with an open, a read and a close.
// It returns nil, and no error, for a file that is not of size bytes any
// more, which the caller then serves as it is.
func readSmall(full string, size int64) ([]byte, error) {
	fd, err := openRead(full)
	if err != nil {
		return nil, err
	}
	defer syscall.Close(fd)
	// One byte more tells a file that grew.
	buf := bufferpool.Get(int(size) + 1)
	n := 0
	for n < len(buf) {
		m, err := syscall.Pread(fd, buf[n:], int64(n))
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			bufferpool.Put(buf)
			return nil, &fs.PathError{Op: "read", Path: full, Err: err}
		}
		n += m
		if m < len(buf)-(n-m) {
			// A short read of a regular file is its end, which saves the
			// read that would return nothing; a file system that reads
			// short elsewhere makes the size differ, and the file go as
			// it is.
			break
		}
	}
	if int64(n) != size {
		bufferpool.Put(buf)
		return nil, nil
	}
	return buf[:n], nil
}

// openSendable opens the file at full for SendFile as a rawFile: a
// descriptor and no os.File, which costs no fcntl, and which it hands over
// to the connection rather than have it duplicated.
func openSendable(full string) (sendable, error) {
	fd, err := openRead(full)
	if err != nil {
		return nil, err
	}
	return &rawFile{fd: fd}, nil
}

// rawFile is a file opened for SendFile: its descriptor, which it hands
// over to the connection once (fib.FileHandover), which then sends from it
// and closes it, or closes itself when it never was.
type rawFile struct {
	fd     int
	handed atomic.Bool
}

func (f *rawFile) ReadAt(p []byte, off int64) (int, error) {
	for {
		n, err := syscall.Pread(f.fd, p, off)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return 0, err
		}
		if n < len(p) {
			return n, io.EOF
		}
		return n, nil
	}
}

// SyscallConn lets SendFile duplicate the descriptor, for a range it sends
// in pieces before the last.
func (f *rawFile) SyscallConn() (syscall.RawConn, error) { return rawConn{f.fd}, nil }

func (f *rawFile) HandOverFD() (int, bool) {
	return f.fd, f.handed.CompareAndSwap(false, true)
}

// stands reports whether the file is still a regular file of size bytes,
// with one fstat and nothing allocated.
func (f *rawFile) stands(size int64) bool {
	var st syscall.Stat_t
	if err := syscall.Fstat(f.fd, &st); err != nil {
		return false
	}
	return st.Mode&syscall.S_IFMT == syscall.S_IFREG && st.Size == size
}

// Close closes the descriptor unless it was handed over.
func (f *rawFile) Close() error {
	if f.handed.CompareAndSwap(false, true) {
		return syscall.Close(f.fd)
	}
	return nil
}

// rawConn is the syscall.RawConn of a rawFile, which only Control serves.
type rawConn struct{ fd int }

func (c rawConn) Control(fn func(uintptr)) error { fn(uintptr(c.fd)); return nil }

func (rawConn) Read(func(uintptr) bool) error { return errRawConn }

func (rawConn) Write(func(uintptr) bool) error { return errRawConn }

var errRawConn = errors.New("static: a file's descriptor is not read or written through its RawConn")
