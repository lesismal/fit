package static

import (
	"errors"
	"io"
	"io/fs"
	stdhttp "net/http"
	"os"
	"time"

	fibhttp "github.com/lesismal/fib/http"
)

// send answers the request with the file f, which info describes, as
// ServeContent would: status, validators, conditions and a range. The body
// goes through fib's SendFileStall, so that nothing waits for the disk or
// for the client, and a client that takes nothing of it for stall loses
// it, while one that reads slowly keeps it. It reports whether SendFile
// took the response over, closing f and releasing the request when it is
// done; otherwise f is closed, and the response complete. It runs on the
// file pool, with the request retained.
func send(c *fibhttp.Context, f *os.File, name string, info fs.FileInfo, stall time.Duration) bool {
	w := &rangeWriter{c: c, src: &offsetFile{f: f}}
	stdhttp.ServeContent(w, c.Request, name, info.ModTime(), w.src)
	if !w.taken {
		// HEAD, 304, 412, 416: no body, or ServeContent's short one.
		f.Close()
		if len(w.small) > 0 {
			_, _ = c.Write(w.small)
		}
		return false
	}
	c.SendFileStall(f, w.offset, w.n, stall, func(error) {
		f.Close()
		c.Release()
	})
	return true
}

// rangeWriter is the ResponseWriter ServeContent answers through. The
// header and status go to the Context; the body, which ServeContent copies
// from the file with io.CopyN, is not copied but taken as a range of the
// file, to send with SendFile; and what it writes itself, an error's short
// text, is kept to write once it is done.
type rangeWriter struct {
	c         *fibhttp.Context
	src       *offsetFile
	taken     bool
	offset, n int64
	small     []byte
}

// maxSmall bounds what rangeWriter keeps of a body ServeContent writes
// itself.
const maxSmall = 4 << 10

func (w *rangeWriter) Header() stdhttp.Header { return w.c.Header() }

func (w *rangeWriter) WriteHeader(status int) { w.c.WriteHeader(status) }

func (w *rangeWriter) Write(p []byte) (int, error) {
	if len(w.small)+len(p) > maxSmall {
		return 0, errors.New("static: response body past what is kept")
	}
	w.small = append(w.small, p...)
	return len(p), nil
}

// ReadFrom takes the range io.CopyN asks for, which ServeContent has
// sought to, without reading it.
func (w *rangeWriter) ReadFrom(r io.Reader) (int64, error) {
	lr, ok := r.(*io.LimitedReader)
	if !ok || lr.R != w.src || w.taken {
		return io.Copy(writerOnly{w}, r)
	}
	w.taken, w.offset, w.n = true, w.src.offset, lr.N
	return lr.N, nil
}

// writerOnly hides rangeWriter's ReadFrom from io.Copy, which would
// otherwise call it back.
type writerOnly struct{ io.Writer }

// offsetFile is the file as ServeContent reads it: to sniff its type, and
// to seek to the range it sends, whose start Seek records.
type offsetFile struct {
	f      *os.File
	offset int64
}

func (o *offsetFile) Read(p []byte) (int, error) {
	n, err := o.f.ReadAt(p, o.offset)
	o.offset += int64(n)
	if err == io.EOF && n > 0 {
		err = nil
	}
	return n, err
}

func (o *offsetFile) Seek(offset int64, whence int) (int64, error) {
	n, err := o.f.Seek(offset, whence)
	if err == nil {
		o.offset = n
	}
	return n, err
}
