package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"unicode/utf8"
)

// maxDepth bounds how deeply a document may nest, so that a hostile one
// cannot exhaust the stack of the recursive parser and decoder.
const maxDepth = 256

type kind uint8

const (
	kindNull kind = iota
	kindBool
	kindNumber
	kindString
	kindArray
	kindObject
)

func (k kind) String() string {
	switch k {
	case kindNull:
		return "null"
	case kindBool:
		return "a boolean"
	case kindNumber:
		return "a number"
	case kindString:
		return "a string"
	case kindArray:
		return "an array"
	default:
		return "an object"
	}
}

// node is a parsed JSON value that remembers where it was in its document,
// and keeps an object's members in the order they were written.
type node struct {
	kind    kind
	src     *source
	off     int // where the value starts; -1 when it has no position
	b       bool
	s       string // a string's value, or a number's text
	elems   []*node
	members []member
}

type member struct {
	key    string
	keyOff int
	val    *node
}

func (n *node) member(key string) *node {
	for i := range n.members {
		if n.members[i].key == key {
			return n.members[i].val
		}
	}
	return nil
}

// pos returns the 1-based line and column off is at, or zeros when there is
// no position.
func (n *node) pos(off int) (line, col int) {
	if n == nil || n.src == nil || off < 0 {
		return 0, 0
	}
	return n.src.pos(off)
}

type source struct {
	data []byte

	// warnings are what decoding the document, or the module params in
	// it, found deprecated; modules may decode their params concurrently.
	mu       sync.Mutex
	warnings Errors
}

// warn records w once.
func (s *source) warn(w *Error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.warnings {
		if *x == *w {
			return
		}
	}
	s.warnings = append(s.warnings, w)
}

func (s *source) pos(off int) (line, col int) {
	if off > len(s.data) {
		off = len(s.data)
	}
	before := s.data[:off]
	line = bytes.Count(before, []byte{'\n'}) + 1
	start := bytes.LastIndexByte(before, '\n') + 1
	return line, utf8.RuneCount(before[start:]) + 1
}

// parse parses data, which must hold exactly one JSON value. Unlike
// encoding/json it refuses an object with the same key twice, rather than
// silently keeping the last.
func parse(data []byte) (*node, error) {
	p := &parser{src: &source{data: data}, dec: json.NewDecoder(bytes.NewReader(data))}
	p.dec.UseNumber()
	tok, off, err := p.next()
	if err != nil {
		return nil, p.syntaxError(err)
	}
	root, err := p.value(tok, off, 0)
	if err != nil {
		return nil, err
	}
	if _, off, err := p.next(); err != io.EOF {
		if err != nil {
			return nil, p.syntaxError(err)
		}
		return nil, p.errorAt(off, "unexpected data after the top-level value")
	}
	return root, nil
}

type parser struct {
	src *source
	dec *json.Decoder
}

// next returns the next token and the offset it starts at. The decoder's
// offset is the end of the token before, so the separators between the two
// are skipped to find the start.
func (p *parser) next() (json.Token, int, error) {
	off := int(p.dec.InputOffset())
	for off < len(p.src.data) {
		switch p.src.data[off] {
		case ' ', '\t', '\r', '\n', ',', ':':
			off++
			continue
		}
		break
	}
	tok, err := p.dec.Token()
	return tok, off, err
}

func (p *parser) value(tok json.Token, off, depth int) (*node, error) {
	if depth > maxDepth {
		return nil, p.errorAt(off, fmt.Sprintf("nested more than %d levels deep", maxDepth))
	}
	n := &node{src: p.src, off: off}
	switch tok := tok.(type) {
	case nil:
		n.kind = kindNull
	case bool:
		n.kind, n.b = kindBool, tok
	case json.Number:
		n.kind, n.s = kindNumber, string(tok)
	case string:
		n.kind, n.s = kindString, tok
	case json.Delim:
		if tok == '[' {
			n.kind = kindArray
			for p.dec.More() {
				tok, off, err := p.next()
				if err != nil {
					return nil, p.syntaxError(err)
				}
				elem, err := p.value(tok, off, depth+1)
				if err != nil {
					return nil, err
				}
				n.elems = append(n.elems, elem)
			}
		} else {
			n.kind = kindObject
			for p.dec.More() {
				tok, keyOff, err := p.next()
				if err != nil {
					return nil, p.syntaxError(err)
				}
				key := tok.(string) // the decoder allows nothing else here
				if n.member(key) != nil {
					return nil, p.errorAt(keyOff, fmt.Sprintf("duplicate key %q", key))
				}
				tok, off, err := p.next()
				if err != nil {
					return nil, p.syntaxError(err)
				}
				val, err := p.value(tok, off, depth+1)
				if err != nil {
					return nil, err
				}
				n.members = append(n.members, member{key: key, keyOff: keyOff, val: val})
			}
		}
		if _, _, err := p.next(); err != nil { // the closing ] or }
			return nil, p.syntaxError(err)
		}
	}
	return n, nil
}

func (p *parser) errorAt(off int, msg string) *Error {
	line, col := p.src.pos(off)
	return &Error{Line: line, Col: col, Msg: msg}
}

func (p *parser) syntaxError(err error) *Error {
	var syntax *json.SyntaxError
	switch {
	case errors.As(err, &syntax) && syntax.Error() == "unexpected end of JSON input":
		return p.errorAt(len(p.src.data), "invalid JSON: unexpected end of input")
	case errors.As(err, &syntax):
		// The offset is just past the byte that broke the syntax.
		return p.errorAt(max(int(syntax.Offset)-1, 0), "invalid JSON: "+syntax.Error())
	case err == io.EOF || errors.Is(err, io.ErrUnexpectedEOF):
		return p.errorAt(len(p.src.data), "invalid JSON: unexpected end of input")
	default:
		return p.errorAt(int(p.dec.InputOffset()), "invalid JSON: "+err.Error())
	}
}
