package config

import (
	"fmt"
	"strings"
)

// Error is one problem found in a config, and where it was found.
type Error struct {
	// Path is where in the document the problem is, as in
	// listeners[0].protocols[1]; it is empty for the document as a whole.
	Path string
	// Line and Col are the 1-based position the problem was found at, Col
	// counted in characters. They are 0 when there is no position, as for a
	// value given in Go rather than parsed.
	Line, Col int
	// Msg says what is wrong.
	Msg string
}

func (e *Error) Error() string {
	var b strings.Builder
	if e.Line > 0 {
		fmt.Fprintf(&b, "%d:%d: ", e.Line, e.Col)
	}
	if e.Path != "" {
		b.WriteString(e.Path)
		b.WriteString(": ")
	}
	b.WriteString(e.Msg)
	return b.String()
}

// Errors is every problem found in a config, in the order they were found.
type Errors []*Error

func (es Errors) Error() string {
	lines := make([]string, len(es))
	for i, e := range es {
		lines[i] = e.Error()
	}
	return strings.Join(lines, "\n")
}

// AsErrors returns the problems err holds: err itself when it is an *Error,
// its elements when it is Errors, and otherwise err as an Error with no path.
func AsErrors(err error) Errors {
	switch err := err.(type) {
	case nil:
		return nil
	case *Error:
		return Errors{err}
	case Errors:
		return err
	default:
		return Errors{{Msg: err.Error()}}
	}
}

func joinField(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

func joinIndex(path string, i int) string {
	return fmt.Sprintf("%s[%d]", path, i)
}

func joinKey(path, key string) string {
	return fmt.Sprintf("%s[%q]", path, key)
}
