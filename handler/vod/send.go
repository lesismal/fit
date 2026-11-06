package vod

import (
	"bytes"
	"errors"
	"io"
	stdhttp "net/http"
	"os"
	"sync/atomic"
	"time"

	fibhttp "github.com/lesismal/fib/http"
)

// What follows answers with a file of the library, or with a blob the cache
// keeps, as net/http's ServeContent would: status, validators, conditions
// and a range. It is the static handler's way (handler/static/send.go),
// kept here rather than shared, as the two may part.

// sendFile answers the request with f, its header set, as ServeContent
// would. The body goes through fib's SendFileStall, so that nothing waits
// for the disk or for the client. It reports whether SendFile took the
// response over, calling done once it has; otherwise the response is
// complete, and done is not called. It runs on the file pool.
func sendFile(c *fibhttp.Context, f *os.File, name string, modTime time.Time, stall time.Duration, sent *atomic.Uint64, done func()) bool {
	w := &rangeWriter{c: c, src: &offsetFile{f: f}}
	stdhttp.ServeContent(w, c.Request, name, modTime, w.src)
	if !w.taken {
		// HEAD, 304, 412, 416: no body, or ServeContent's short one.
		if len(w.small) > 0 {
			sent.Add(uint64(len(w.small)))
			_, _ = c.Write(w.small)
		}
		return false
	}
	// What is handed to SendFile is counted, whether or not the client
	// takes all of it.
	sent.Add(uint64(w.n))
	c.SendFileStall(f, w.offset, w.n, stall, func(error) { done() })
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
		return 0, errors.New("vod: response body past what is kept")
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

// offsetFile is the file as ServeContent reads it: to seek to the range it
// sends, whose start Seek records. The Content-Type is set beforehand, so
// that it is not sniffed.
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

// serveBlob answers with b: as it is, with the header it keeps, for a
// request without conditions or a range, and otherwise as ServeContent
// answers it. It returns the bytes of the body it handed to the
// connection.
func serveBlob(c *fibhttp.Context, b *blob) int {
	r := c.Request
	if !hasConditions(r) {
		// The header is shared by every response b goes out in; fib does
		// not change a header it is handed.
		_ = c.WriteResponse(fibhttp.Response{StatusCode: stdhttp.StatusOK, Header: b.header, Body: b.data})
		return len(b.data)
	}
	w := &gathered{header: make(stdhttp.Header, len(b.header)+2)}
	for k, v := range b.header {
		w.header[k] = v
	}
	stdhttp.ServeContent(w, r, "", b.modTime, bytes.NewReader(b.data))
	_ = c.WriteResponse(fibhttp.Response{StatusCode: w.status, Header: w.header, Body: w.body.Bytes()})
	return w.body.Len()
}

// hasConditions reports whether r asks for anything but the whole
// response: a range, or a condition on its validators.
func hasConditions(r *stdhttp.Request) bool {
	for _, key := range [...]string{"Range", "If-Modified-Since", "If-None-Match", "If-Match", "If-Unmodified-Since", "If-Range"} {
		if _, ok := r.Header[key]; ok {
			return true
		}
	}
	return false
}

// gathered is a ResponseWriter that keeps what it is given. What
// ServeContent writes of a blob is no larger than the blob, but for a
// multipart body's few part headers.
type gathered struct {
	header stdhttp.Header
	status int
	body   bytes.Buffer
}

func (g *gathered) Header() stdhttp.Header { return g.header }

func (g *gathered) WriteHeader(status int) {
	if g.status == 0 {
		g.status = status
	}
}

func (g *gathered) Write(p []byte) (int, error) {
	if g.status == 0 {
		g.status = stdhttp.StatusOK
	}
	return g.body.Write(p)
}
