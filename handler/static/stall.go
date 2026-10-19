package static

import (
	"errors"
	"io"
	stdhttp "net/http"

	"time"

	"github.com/lesismal/fib/bufferpool"
	fibhttp "github.com/lesismal/fib/http"
)

// stallWriter is the ResponseWriter ServeContent writes a file through: each
// write that may wait for the client is bounded by the stall timeout, so a
// client that stops reading loses its response, its HTTP/1 connection
// closed or its HTTP/2 or HTTP/3 stream reset, rather than keeping the
// handler's goroutine waiting.
type stallWriter struct {
	c     *fibhttp.Context
	stall time.Duration
}

func (s *Static) writer(c *fibhttp.Context) *stallWriter {
	return &stallWriter{c: c, stall: s.stall}
}

func (w *stallWriter) Header() stdhttp.Header { return w.c.Header() }

func (w *stallWriter) WriteHeader(status int) { w.c.WriteHeader(status) }

// Write writes p under a deadline of the stall timeout from now, lifted once
// it is written, so that only the time it waits for the client counts.
func (w *stallWriter) Write(p []byte) (int, error) {
	if err := w.c.SetWriteDeadline(time.Now().Add(w.stall)); err != nil && !deadlineUnsupported(err) {
		return 0, err
	}
	n, err := w.c.Write(p)
	if err == nil {
		_ = w.c.SetWriteDeadline(time.Time{})
	}
	return n, err
}

// ReadFrom sends a file. A plain HTTP/1 connection gets it through fib's
// Context.ReadFrom, which hands it to SendFile: the kernel sends it as the
// client takes it, and nothing waits. Anything else, over TLS, HTTP/2 or
// HTTP/3, fib would copy through writes that wait; the copy goes through
// Write here instead, so that each of those waits is bounded.
func (w *stallWriter) ReadFrom(src io.Reader) (int64, error) {
	r := w.c.Request
	if r.ProtoMajor == 1 && r.TLS == nil {
		return w.c.ReadFrom(src)
	}
	buf := bufferpool.Get(64 << 10)
	defer bufferpool.Put(buf)
	return io.CopyBuffer(writerOnly{w}, src, buf)
}

// deadlineUnsupported reports whether err only says the Context takes no
// deadline, as one serving a stream outside fib does; the write then goes
// ahead without one.
func deadlineUnsupported(err error) bool {
	return errors.Is(err, stdhttp.ErrNotSupported) || errors.Is(err, fibhttp.ErrResponseWritten)
}

// writerOnly hides stallWriter's ReadFrom from io.CopyBuffer, which would
// otherwise call it back.
type writerOnly struct{ io.Writer }

var _ io.ReaderFrom = (*stallWriter)(nil)
