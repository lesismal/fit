package model

import (
	"bytes"
)

// framer cuts a streamed body into events: server-sent events, each its
// event name and data, or NDJSON's lines, each a JSON value with no name. A
// line, and an event's data, may be at most max bytes; what waits for the
// rest of a line is the only part of the body kept between pieces.
type framer struct {
	ndjson  bool
	max     int
	line    []byte // a line not ended yet
	name    string // the SSE event's name
	data    []byte // the SSE event's data so far
	hasData bool
	// handle is told each event; an error ends the body.
	handle func(name string, data []byte) error
}

// write takes a piece of the body, which it does not keep.
func (f *framer) write(p []byte) error {
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			if len(f.line)+len(p) > f.max {
				return ErrTooLarge
			}
			f.line = append(f.line, p...)
			return nil
		}
		line := p[:i]
		p = p[i+1:]
		if len(f.line)+len(line) > f.max {
			return ErrTooLarge
		}
		if len(f.line) > 0 {
			f.line = append(f.line, line...)
			line = f.line
		}
		err := f.lineDone(bytes.TrimSuffix(line, []byte{'\r'}))
		f.line = f.line[:0]
		if err != nil {
			return err
		}
	}
	return nil
}

// end takes the end of the body: a last line without its newline, and an
// event without its blank line, count as ended.
func (f *framer) end() error {
	if len(f.line) > 0 {
		line := f.line
		f.line = nil
		if err := f.lineDone(bytes.TrimSuffix(line, []byte{'\r'})); err != nil {
			return err
		}
	}
	if !f.ndjson {
		return f.dispatch()
	}
	return nil
}

func (f *framer) lineDone(line []byte) error {
	if f.ndjson {
		if len(bytes.TrimSpace(line)) == 0 {
			return nil
		}
		return f.handle("", line)
	}
	if len(line) == 0 {
		return f.dispatch()
	}
	if line[0] == ':' {
		return nil // a comment, as a keep-alive
	}
	field, value, _ := bytes.Cut(line, []byte{':'})
	value = bytes.TrimPrefix(value, []byte{' '})
	switch string(field) {
	case "event":
		f.name = string(value)
	case "data":
		// A line is within max already; the lines of an event together
		// must be too.
		if f.hasData {
			if len(f.data)+1+len(value) > f.max {
				return ErrTooLarge
			}
			f.data = append(f.data, '\n')
		}
		f.data = append(f.data, value...)
		f.hasData = true
	}
	return nil
}

// dispatch hands on the event the lines so far made, if it has data.
func (f *framer) dispatch() error {
	name, data, has := f.name, f.data, f.hasData
	f.name, f.data, f.hasData = "", f.data[:0], false
	if !has {
		return nil
	}
	return f.handle(name, data)
}
