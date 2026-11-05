//go:build unix

package logs

import (
	"fmt"
	"os"
	"syscall"
)

// openNoFollow opens path without following a symbolic link at its last
// element: O_NOFOLLOW makes the open of a link fail, so that someone who may
// write the log's directory cannot point fit's writes, or the reads it
// compresses, at another file. O_NONBLOCK keeps a FIFO left at the name from
// holding the open: it fails, or returns at once and fails checkFile. On a
// regular file O_NONBLOCK changes nothing.
func openNoFollow(path string, flag int, perm os.FileMode) (*os.File, error) {
	return os.OpenFile(path, flag|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, perm)
}

// checkFile refuses what a log may not be written to or compressed from: not
// a regular file, or one with another hard link, which a link planted in the
// directory to a file elsewhere would have.
func checkFile(fi os.FileInfo) error {
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("not a regular file (%s)", fi.Mode().Type())
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Nlink > 1 {
		return fmt.Errorf("%d hard links, where a log file has one", st.Nlink)
	}
	return nil
}

// othersMayWrite reports whether users other than the directory's owner may
// write dir, and its mode: such a user may put links where fit's log files
// go, which fit refuses to follow, and remove or rename the files, which
// nothing in fit can stop.
func othersMayWrite(dir string) (os.FileMode, bool) {
	fi, err := os.Stat(dir)
	if err != nil {
		return 0, false
	}
	return fi.Mode().Perm(), fi.Mode().Perm()&0o022 != 0
}
