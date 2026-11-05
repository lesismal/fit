//go:build !unix

package logs

import (
	"errors"
	"fmt"
	"os"
)

// openNoFollow opens path, refusing it when the name is a symbolic link or
// another reparse point. There is no O_NOFOLLOW here: the name is looked at
// after the open and must be the very file that was opened, which a link put
// there before or swapped in after would not be.
func openNoFollow(path string, flag int, perm os.FileMode) (*os.File, error) {
	fh, err := os.OpenFile(path, flag, perm)
	if err != nil {
		return nil, err
	}
	li, err := os.Lstat(path)
	if err != nil {
		fh.Close()
		return nil, err
	}
	fi, err := fh.Stat()
	if err != nil {
		fh.Close()
		return nil, err
	}
	if li.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 || !os.SameFile(li, fi) {
		fh.Close()
		return nil, &os.PathError{Op: "open", Path: path, Err: errors.New("the name is a link, not a log file")}
	}
	return fh, nil
}

// checkFile refuses what a log may not be written to or compressed from: not
// a regular file.
func checkFile(fi os.FileInfo) error {
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("not a regular file (%s)", fi.Mode().Type())
	}
	return nil
}

// othersMayWrite is false where a directory's mode bits do not say who may
// write it.
func othersMayWrite(string) (os.FileMode, bool) { return 0, false }
