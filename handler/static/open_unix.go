//go:build unix

package static

import (
	"io/fs"
	"os"
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
