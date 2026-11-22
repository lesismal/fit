package static

import (
	"io/fs"
	"path/filepath"
	"syscall"
	"time"
)

// openStat opens the file at full and stats it through its descriptor, as
// nginx does a file it does not hold open, for a lookup of a request path
// the cache does not know: an open and an fstat, which the file is then
// read or sent from, where an lstat before them walks the path once more.
// The open does not follow a symbolic link, which ok false hands back to
// statLink, nor wait for a writer, as a named pipe's would: O_NONBLOCK, which
// reads and sendfile of a regular file do not heed. It reports ok false,
// having opened nothing, for an error other than a path that names nothing,
// or a permission it lacks to read a file it may stat, which statLink tells
// apart as before. A file that is not a regular one is closed at once.
//
// The FileInfo and the file are made in spare, which newLookup made with
// the lookup they belong to, in one allocation.
func openStat(full string, spare *lookupSpare) (fs.FileInfo, sendable, bool, error) {
	var fd int
	var err error
	for {
		fd, err = syscall.Open(full, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != syscall.EINTR {
			break
		}
	}
	switch err {
	case nil:
	case syscall.ENOENT, syscall.ENOTDIR:
		return nil, nil, true, &fs.PathError{Op: "open", Path: full, Err: err}
	default:
		return nil, nil, false, nil
	}
	info := &spare.info
	info.name = filepath.Base(full)
	if err := syscall.Fstat(fd, &info.st); err != nil {
		syscall.Close(fd)
		return nil, nil, false, nil
	}
	if info.st.Mode&syscall.S_IFMT != syscall.S_IFREG {
		syscall.Close(fd)
		return info, nil, true, nil
	}
	f := &spare.file
	f.fd = fd
	return info, f, true, nil
}

// lookupSpare is room for what openStat makes, allocated with the lookup
// it makes them for: a lookup the cache does not hold costs one allocation
// for the three.
type lookupSpare struct {
	info statInfo
	file rawFile
}

type lookupWithSpare struct {
	lookup
	spare lookupSpare
}

// newLookup returns a lookup and the room openStat makes its stat and file
// in.
func newLookup() (*lookup, *lookupSpare) {
	l := new(lookupWithSpare)
	return &l.lookup, &l.spare
}

// statInfo is the fs.FileInfo of an fstat, as os.File.Stat makes it, but
// without an os.File, which openRead's descriptor does not have.
type statInfo struct {
	name string
	st   syscall.Stat_t
}

func (i *statInfo) Name() string { return i.name }

func (i *statInfo) Size() int64 { return i.st.Size }

func (i *statInfo) Mode() fs.FileMode {
	m := fs.FileMode(i.st.Mode & 0o777)
	switch i.st.Mode & syscall.S_IFMT {
	case syscall.S_IFBLK:
		m |= fs.ModeDevice
	case syscall.S_IFCHR:
		m |= fs.ModeDevice | fs.ModeCharDevice
	case syscall.S_IFDIR:
		m |= fs.ModeDir
	case syscall.S_IFIFO:
		m |= fs.ModeNamedPipe
	case syscall.S_IFLNK:
		m |= fs.ModeSymlink
	case syscall.S_IFSOCK:
		m |= fs.ModeSocket
	}
	if i.st.Mode&syscall.S_ISGID != 0 {
		m |= fs.ModeSetgid
	}
	if i.st.Mode&syscall.S_ISUID != 0 {
		m |= fs.ModeSetuid
	}
	if i.st.Mode&syscall.S_ISVTX != 0 {
		m |= fs.ModeSticky
	}
	return m
}

func (i *statInfo) ModTime() time.Time { return time.Unix(i.st.Mtim.Unix()) }

func (i *statInfo) IsDir() bool { return i.st.Mode&syscall.S_IFMT == syscall.S_IFDIR }

func (i *statInfo) Sys() any { return &i.st }
