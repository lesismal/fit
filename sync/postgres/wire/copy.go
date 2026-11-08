package pgwire

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

// copyFlushBytes is how much a CopyIn gathers before it writes.
const copyFlushBytes = 64 << 10

// CopyIn is a COPY ... FROM STDIN in progress.
type CopyIn struct {
	c    *Conn
	exit func(error) error
	open int // where the open CopyData message's length goes, or -1
	row  []byte
	done bool
}

// CopyIn starts sql, a COPY ... FROM STDIN, and returns its writer: Write
// the data in the COPY's format (or WriteRow for its text format) and Close
// to finish, or Abort. The connection is busy until then.
func (c *Conn) CopyIn(ctx context.Context, sql string) (*CopyIn, error) {
	exit, err := c.enter(ctx)
	if err != nil {
		return nil, err
	}
	i := c.begin('Q')
	c.wbuf = append(append(c.wbuf, sql...), 0)
	c.end(i)
	if err := c.flush(); err != nil {
		return nil, exit(err)
	}
	for {
		t, body, err := c.recv()
		if err != nil {
			return nil, exit(err)
		}
		switch t {
		case 'G':
			c.busy = true
			return &CopyIn{c: c, exit: exit, open: -1}, nil
		case 'E':
			return nil, exit(c.drain(parseError(body)))
		case 'Z':
			return nil, exit(errors.New("postgres: the statement is not a COPY FROM STDIN"))
		case 'T', 'D', 'C', 'I':
		default:
			return nil, exit(c.unexpected(t, "COPY FROM STDIN"))
		}
	}
}

// Write sends p as COPY data.
func (w *CopyIn) Write(p []byte) (int, error) {
	if w.done {
		return 0, errors.New("postgres: the copy is over")
	}
	c := w.c
	n := len(p)
	for len(p) > 0 {
		if w.open < 0 {
			w.open = c.begin('d')
		}
		k := min(len(p), copyFlushBytes)
		c.wbuf = append(c.wbuf, p[:k]...)
		p = p[k:]
		if len(c.wbuf) >= copyFlushBytes {
			if err := w.flush(); err != nil {
				return 0, err
			}
		}
	}
	return n, nil
}

func (w *CopyIn) flush() error {
	if w.open >= 0 {
		w.c.end(w.open)
		w.open = -1
	}
	if err := w.c.flush(); err != nil {
		w.finish(err)
		return w.c.broken
	}
	return nil
}

// WriteRow sends one row in COPY's text format (the default format, with
// tabs and \N); values are written as query parameters are, []byte as
// bytea's hex.
func (w *CopyIn) WriteRow(values ...any) error {
	var err error
	w.row, err = AppendCopyRow(w.row[:0], values...)
	if err != nil {
		return err
	}
	_, err = w.Write(w.row)
	return err
}

// Close ends the data and returns the rows the server copied.
func (w *CopyIn) Close() (int64, error) {
	if w.done {
		return 0, errors.New("postgres: the copy is over")
	}
	c := w.c
	if w.open >= 0 {
		c.end(w.open)
		w.open = -1
	}
	i := c.begin('c')
	c.end(i)
	if err := c.flush(); err != nil {
		return 0, w.finish(err)
	}
	var tag string
	var qerr error
	for {
		t, body, err := c.recv()
		if err != nil {
			return 0, w.finish(err)
		}
		switch t {
		case 'C':
			tag, _, _ = cstring(body)
		case 'E':
			if qerr == nil {
				qerr = parseError(body)
			}
		case 'Z':
			if len(body) > 0 {
				c.txStatus = body[0]
			}
			if err := w.finish(qerr); err != nil {
				return 0, err
			}
			return RowsAffected(tag), nil
		default:
			return 0, w.finish(c.unexpected(t, "COPY FROM STDIN"))
		}
	}
}

// Abort ends the copy with a CopyFail; the server rolls the COPY back.
func (w *CopyIn) Abort(reason string) error {
	if w.done {
		return nil
	}
	c := w.c
	if w.open >= 0 { // drop the unsent data
		c.wbuf = c.wbuf[:0]
		w.open = -1
	}
	i := c.begin('f')
	c.wbuf = append(append(c.wbuf, reason...), 0)
	c.end(i)
	if err := c.flush(); err != nil {
		return w.finish(err)
	}
	err := c.drain(nil)
	var pe *Error
	if errors.As(err, &pe) {
		err = nil // the server's answer to CopyFail
	}
	return w.finish(err)
}

func (w *CopyIn) finish(err error) error {
	if w.done {
		return err
	}
	w.done = true
	w.c.busy = false
	return w.exit(err)
}

// CopyFrom runs a COPY ... FROM STDIN with the data read from r, and
// returns the rows copied.
func (c *Conn) CopyFrom(ctx context.Context, sql string, r io.Reader) (int64, error) {
	w, err := c.CopyIn(ctx, sql)
	if err != nil {
		return 0, err
	}
	if _, err := io.CopyBuffer(w, r, make([]byte, copyFlushBytes)); err != nil {
		if w.c.Err() == nil {
			w.Abort(err.Error())
		} else {
			w.finish(err)
		}
		return 0, err
	}
	return w.Close()
}

// CopyTo runs a COPY ... TO STDOUT and writes its data to dst, returning
// the rows copied. An error from dst ends the connection, since the
// server's data cannot be stopped otherwise.
func (c *Conn) CopyTo(ctx context.Context, sql string, dst io.Writer) (int64, error) {
	exit, err := c.enter(ctx)
	if err != nil {
		return 0, err
	}
	i := c.begin('Q')
	c.wbuf = append(append(c.wbuf, sql...), 0)
	c.end(i)
	if err := c.flush(); err != nil {
		return 0, exit(err)
	}
	var tag string
	var qerr error
	for {
		t, body, err := c.recv()
		if err != nil {
			return 0, exit(err)
		}
		switch t {
		case 'H', 'c':
		case 'd':
			if _, err := dst.Write(body); err != nil {
				c.fail(fmt.Errorf("postgres: COPY TO: %w", err))
				return 0, exit(c.broken)
			}
		case 'C':
			tag, _, _ = cstring(body)
		case 'E':
			if qerr == nil {
				qerr = parseError(body)
			}
		case 'G':
			i := c.begin('f')
			c.wbuf = append(append(c.wbuf, "not a COPY TO STDOUT"...), 0)
			c.end(i)
			if err := c.flush(); err != nil {
				return 0, exit(err)
			}
		case 'Z':
			if len(body) > 0 {
				c.txStatus = body[0]
			}
			if qerr != nil {
				return 0, exit(qerr)
			}
			return RowsAffected(tag), exit(nil)
		case 'T', 'D', 'I':
		default:
			return 0, exit(c.unexpected(t, "COPY TO STDOUT"))
		}
	}
}

// AppendCopyRow appends a row in COPY's text format: values separated by
// tabs, \N for NULL, a newline at the end, backslashes, tabs, newlines and
// carriage returns escaped; []byte as bytea's hex.
func AppendCopyRow(dst []byte, values ...any) ([]byte, error) {
	var tmp []byte
	for i, v := range values {
		if i > 0 {
			dst = append(dst, '\t')
		}
		if b, ok := v.([]byte); ok && b != nil {
			tmp = append(append(tmp[:0], `\x`...), hex.EncodeToString(b)...)
		} else {
			var notNull bool
			var err error
			tmp, notNull, err = appendParam(tmp[:0], 0, v)
			if err != nil {
				return dst, fmt.Errorf("%w (column %d)", err, i+1)
			}
			if !notNull {
				dst = append(dst, `\N`...)
				continue
			}
		}
		for _, c := range tmp {
			switch c {
			case '\\':
				dst = append(dst, '\\', '\\')
			case '\t':
				dst = append(dst, '\\', 't')
			case '\n':
				dst = append(dst, '\\', 'n')
			case '\r':
				dst = append(dst, '\\', 'r')
			default:
				dst = append(dst, c)
			}
		}
	}
	return append(dst, '\n'), nil
}
