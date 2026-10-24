package fitconf

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/lesismal/fit/config"
)

// The bounds on what a file may hold, so that a hostile one cannot exhaust
// memory or the stack of the recursive parser and converter.
const (
	maxTokens = 1 << 20
	maxDepth  = 64
)

// token is a word of the file: a directive's name or one of its arguments.
type token struct {
	text string
	// file is the file it was written in: "" for the config's own when
	// it was read without its path, as Adapt reads it.
	file string
	// quoted says it was written in quotes or backquotes: always a string,
	// never a brace.
	quoted    bool
	line, col int
}

// directive is a line of the file: a name, its arguments, and the block
// after them, when there is one.
type directive struct {
	name     token
	args     []token
	block    []*directive
	hasBlock bool
}

// errAt returns an error at a place in file, as the config's own are.
func errAt(file string, line, col int, format string, args ...any) error {
	return config.Errors{{File: file, Line: line, Col: col, Msg: fmt.Sprintf(format, args...)}}
}

// errTok returns an error at where t was written.
func errTok(t token, format string, args ...any) error {
	return errAt(t.file, t.line, t.col, format, args...)
}

// item is what the lexer gives the parser: a token, the end of a
// directive (a new line or a ;), or a brace.
type item struct {
	tok  token
	kind itemKind
}

type itemKind uint8

const (
	itemWord itemKind = iota
	itemEnd
	itemOpen
	itemClose
)

// lex splits data into items. A # at the start of a word begins a comment
// to the end of the line; "double quotes" hold spaces and escapes (\", \\,
// \n, \t, \r, and \{ for a { that does not begin {$), `backquotes`
// anything as it is. A { or } on its own is a
// brace, {} is an empty block, and a ; ending a word ends the directive.
// {$NAME} and {$NAME:default}, outside backquotes, are replaced with the
// environment variable's value.
// Its tokens are of file.
func lex(file string, data []byte) ([]item, error) {
	if !utf8.Valid(data) {
		return nil, errors.New("the file is not UTF-8")
	}
	s := string(data)
	var items []item
	line, col := 1, 1
	i := 0
	advance := func(n int) {
		for _, r := range s[i : i+n] {
			if r == '\n' {
				line, col = line+1, 1
			} else {
				col++
			}
		}
		i += n
	}
	push := func(it item) error {
		if len(items) >= maxTokens {
			return errAt(file, it.tok.line, it.tok.col, "the file holds more than %d words", maxTokens)
		}
		it.tok.file = file
		items = append(items, it)
		return nil
	}
	for i < len(s) {
		c := s[i]
		switch {
		case c == '\n':
			if err := push(item{tok: token{line: line, col: col}, kind: itemEnd}); err != nil {
				return nil, err
			}
			advance(1)
		case c == ' ' || c == '\t' || c == '\r':
			advance(1)
		case c == ';':
			if err := push(item{tok: token{line: line, col: col}, kind: itemEnd}); err != nil {
				return nil, err
			}
			advance(1)
		case c == '#':
			j := strings.IndexByte(s[i:], '\n')
			if j < 0 {
				j = len(s) - i
			}
			advance(j)
		case c == '"':
			l, cl := line, col
			var b strings.Builder
			j := i + 1
			for ; j < len(s) && s[j] != '"'; j++ {
				if s[j] == '{' && strings.HasPrefix(s[j:], "{$") {
					end := strings.IndexByte(s[j:], '}')
					if end < 0 {
						return nil, errAt(file, l, cl, "{$ is not closed with }")
					}
					v, err := expandEnv(file, s[j:j+end+1], l, cl)
					if err != nil {
						return nil, err
					}
					b.WriteString(v)
					j += end
					continue
				}
				if s[j] == '\\' && j+1 < len(s) {
					j++
					switch s[j] {
					case '"', '\\', '{':
						b.WriteByte(s[j])
					case 'n':
						b.WriteByte('\n')
					case 't':
						b.WriteByte('\t')
					case 'r':
						b.WriteByte('\r')
					default:
						b.WriteByte('\\')
						b.WriteByte(s[j])
					}
					continue
				}
				b.WriteByte(s[j])
			}
			if j >= len(s) {
				return nil, errAt(file, l, cl, "a quote is not closed")
			}
			advance(j + 1 - i)
			if err := pushWord(push, token{text: b.String(), quoted: true, line: l, col: cl}, s, i, file, line, col); err != nil {
				return nil, err
			}
		case c == '`':
			l, cl := line, col
			j := strings.IndexByte(s[i+1:], '`')
			if j < 0 {
				return nil, errAt(file, l, cl, "a backquote is not closed")
			}
			text := s[i+1 : i+1+j]
			advance(j + 2)
			if err := pushWord(push, token{text: text, quoted: true, line: l, col: cl}, s, i, file, line, col); err != nil {
				return nil, err
			}
		default:
			l, cl := line, col
			j := i
			for j < len(s) && !strings.ContainsRune(" \t\r\n", rune(s[j])) {
				j++
			}
			word := s[i:j]
			advance(j - i)
			end := false
			if len(word) > 1 && strings.HasSuffix(word, ";") {
				word, end = word[:len(word)-1], true
			}
			var err error
			switch word {
			case "{":
				err = push(item{tok: token{text: word, line: l, col: cl}, kind: itemOpen})
			case "}":
				err = push(item{tok: token{text: word, line: l, col: cl}, kind: itemClose})
			case "{}":
				if err = push(item{tok: token{text: "{", line: l, col: cl}, kind: itemOpen}); err == nil {
					err = push(item{tok: token{text: "}", line: l, col: cl + 1}, kind: itemClose})
				}
			default:
				var text string
				if text, err = expandEnv(file, word, l, cl); err == nil {
					err = push(item{tok: token{text: text, line: l, col: cl}})
				}
			}
			if err == nil && end {
				err = push(item{tok: token{line: l, col: cl + len(word)}, kind: itemEnd})
			}
			if err != nil {
				return nil, err
			}
		}
	}
	return items, nil
}

// pushWord pushes a quoted word; s[i:] is what follows it, which may be a ;
// ending the directive, lexed next.
func pushWord(push func(item) error, tok token, s string, i int, file string, line, col int) error {
	if err := push(item{tok: tok}); err != nil {
		return err
	}
	if i < len(s) && !strings.ContainsRune(" \t\r\n;", rune(s[i])) {
		return errAt(file, line, col, "a quoted word must end at a space, a new line or a ;")
	}
	return nil
}

// expandEnv replaces {$NAME} and {$NAME:default} in s.
func expandEnv(file, s string, line, col int) (string, error) {
	if !strings.Contains(s, "{$") {
		return s, nil
	}
	var b strings.Builder
	for {
		i := strings.Index(s, "{$")
		if i < 0 {
			b.WriteString(s)
			return b.String(), nil
		}
		j := strings.IndexByte(s[i:], '}')
		if j < 0 {
			return "", errAt(file, line, col, "{$ is not closed with }")
		}
		name, def, hasDef := strings.Cut(s[i+2:i+j], ":")
		if name == "" {
			return "", errAt(file, line, col, "{$} names no environment variable")
		}
		v, ok := os.LookupEnv(name)
		if !ok && hasDef {
			v = def
		}
		b.WriteString(s[:i])
		b.WriteString(v)
		s = s[i+j+1:]
	}
}

// parse parses file into its directives, as written, its includes not read.
func parse(file string, data []byte) ([]*directive, error) {
	items, err := lex(file, data)
	if err != nil {
		return nil, err
	}
	return parseItems(items)
}

// parseItems parses a file's items into its directives.
func parseItems(items []item) ([]*directive, error) {
	p := &parser{items: items}
	ds, err := p.block(0)
	if err != nil {
		return nil, err
	}
	if p.i < len(p.items) {
		t := p.items[p.i].tok
		return nil, errTok(t, "a } closes no block")
	}
	return ds, nil
}

type parser struct {
	items []item
	i     int
}

// block parses directives up to a } or the end of the file, which it does
// not consume.
func (p *parser) block(depth int) ([]*directive, error) {
	var ds []*directive
	for p.i < len(p.items) {
		it := p.items[p.i]
		switch it.kind {
		case itemEnd:
			p.i++
			continue
		case itemClose:
			return ds, nil
		case itemOpen:
			return nil, errTok(it.tok, "a block needs a directive before its {")
		}
		d := &directive{name: it.tok}
		p.i++
	args:
		for p.i < len(p.items) {
			it := p.items[p.i]
			switch it.kind {
			case itemWord:
				d.args = append(d.args, it.tok)
				p.i++
			case itemEnd, itemClose:
				break args
			case itemOpen:
				if depth+1 > maxDepth {
					return nil, errTok(it.tok, "blocks nested more than %d deep", maxDepth)
				}
				p.i++
				inner, err := p.block(depth + 1)
				if err != nil {
					return nil, err
				}
				if p.i >= len(p.items) {
					return nil, errTok(it.tok, "this { is not closed")
				}
				p.i++ // the }
				d.block, d.hasBlock = inner, true
				if p.i < len(p.items) && p.items[p.i].kind == itemWord {
					t := p.items[p.i].tok
					return nil, errTok(t, "a block's } ends its directive: begin a new line or use ;")
				}
				break args
			}
		}
		ds = append(ds, d)
	}
	return ds, nil
}
