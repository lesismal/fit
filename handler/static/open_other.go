//go:build !unix

package static

import (
	"errors"
	"io"
	"os"

	"github.com/lesismal/fib/bufferpool"
)

// openFile opens the file at full for reading, for SendFile.
func openFile(full string) (*os.File, error) { return os.Open(full) }

// readSmall reads the whole file at full, of size bytes when it was looked
// at, into a buffer of package bufferpool. It returns nil, and no error, for
// a file that is not of size bytes any more, which the caller then serves as
// it is.
func readSmall(full string, size int64) ([]byte, error) {
	f, err := os.Open(full)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// One byte more tells a file that grew.
	buf := bufferpool.Get(int(size) + 1)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		bufferpool.Put(buf)
		return nil, err
	}
	if int64(n) != size {
		bufferpool.Put(buf)
		return nil, nil
	}
	return buf[:n], nil
}
