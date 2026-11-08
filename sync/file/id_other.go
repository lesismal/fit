//go:build !unix && !windows

package syncfile

import (
	"hash/fnv"
	"os"
)

// Without a device and inode, a file is known by its path's hash and its
// fingerprint: a rename is a new file.

func pathID(path string) fileID {
	h := fnv.New64a()
	h.Write([]byte(path))
	return fileID{0, h.Sum64()}
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
	return pathID(path), fi.Size(), nil
}

// fileIDOf returns the identity and size of an open file.
func fileIDOf(f *os.File) (fileID, int64, error) {
	fi, err := f.Stat()
	if err != nil {
		return fileID{}, 0, err
	}
	return pathID(f.Name()), fi.Size(), nil
}

// openRead opens path to be read.
func openRead(path string) (*os.File, error) { return os.Open(path) }
