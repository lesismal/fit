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
// goes through fib's SendFile, a piece at a time (see transfer), so that
// nothing waits for the disk or for the client. It reports whether SendFile
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
	t := &transfer{c: c, f: f, offset: w.offset, left: w.n, piece: transferPiece, stall: stall}
	r := c.Request
	if r.ProtoMajor == 1 && r.TLS == nil {
		// sendfile(2): a piece is queued, not read, so it may be larger.
		t.piece = sendfilePiece
	}
	t.next()
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

// The pieces a transfer hands SendFile. A piece copied through writes is
// fib's own read size, so that SendFile never waits within one; one sent by
// sendfile(2) is only queued, and may be larger.
const (
	transferPiece = 64 << 10
	sendfilePiece = 256 << 10
)

// transfer sends a range of a file through SendFile, a piece at a time,
// and waits between pieces, without a goroutine, for the client to take the
// last: once more than the response's limit is queued, it goes on from
// OnWritable, under a write deadline of the stall timeout, as the proxy's
// downloads do. So a client that stops reading loses its response after
// the stall timeout, and one that reads slowly keeps it. SendFile alone
// would wait for the client without a bound; one piece at a time keeps
// every wait the transfer's own, until fib's SendFile can bound them (A16 in
// fib-api-requests).
type transfer struct {
	c            *fibhttp.Context
	f            *os.File
	offset, left int64
	piece        int64
	stall        time.Duration
	n            int64 // the piece under way
}

func (t *transfer) next() {
	t.n = min(t.left, t.piece)
	t.c.SendFile(t.f, t.offset, t.n, t.sent)
}

// sent runs, on a goroutine of fib's, once SendFile has handed a piece to
// the connection.
func (t *transfer) sent(err error) {
	if err != nil {
		t.end()
		return
	}
	t.offset += t.n
	t.left -= t.n
	if t.left == 0 {
		t.end()
		return
	}
	if setDeadline(t.c, time.Now().Add(t.stall)) != nil {
		t.end()
		return
	}
	if t.c.OnWritable(t.writable) {
		return
	}
	// Room already.
	t.writable(nil)
}

// writable goes on once the client has room, or ends the response when it
// went, or stopped reading for the stall timeout.
func (t *transfer) writable(err error) {
	if err != nil || setDeadline(t.c, time.Time{}) != nil {
		t.end()
		return
	}
	t.next()
}

// end lets the file and the request go.
func (t *transfer) end() {
	t.f.Close()
	t.c.Release()
}

// setDeadline sets c's write deadline, which a Context serving a stream
// outside fib may not take; the transfer then goes on without one.
func setDeadline(c *fibhttp.Context, d time.Time) error {
	err := c.SetWriteDeadline(d)
	if errors.Is(err, stdhttp.ErrNotSupported) {
		return nil
	}
	return err
}
