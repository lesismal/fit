//go:build unix

package syncfile

import (
	"errors"
	"os"
	"syscall"
)

// identify returns a file's device and inode, and its size.
func identify(fi os.FileInfo) (fileID, int64, error) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fileID{}, 0, errors.New("no device and inode")
	}
	return fileID{uint64(st.Dev), uint64(st.Ino)}, fi.Size(), nil //nolint:unconvert // Dev is int32 on darwin
}

// statID returns the identity and size of the regular file at path.
func statID(path string) (fileID, int64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return fileID{}, 0, err
	}
	if !fi.Mode().IsRegular() {
		return fileID{}, 0, errNotRegular
	}
	return identify(fi)
}

// fileIDOf returns the identity and size of an open file.
func fileIDOf(f *os.File) (fileID, int64, error) {
	fi, err := f.Stat()
	if err != nil {
		return fileID{}, 0, err
	}
	return identify(fi)
}

// openRead opens path to be read.
func openRead(path string) (*os.File, error) { return os.Open(path) }
