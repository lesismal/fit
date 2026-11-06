package nginx

import (
	"fmt"
	"strings"
)

// directive is one nginx directive: its name, its arguments with their
// quotes taken off, the line it starts on, and its block, when it has one.
type directive struct {
	name  string
	args  []string
	line  int
	block []*directive
	// isBlock says it ended with a block rather than a semicolon.
	isBlock bool
}

// The bounds on what a config may hold.
const (
	maxDirectives = 100000
	maxNesting    = 64
)

// parse reads an nginx config into its directives.
func parse(data []byte) ([]*directive, error) {
	p := &parser{src: string(data), line: 1}
	ds, err := p.block(0)
	if err != nil {
		return nil, err
	}
	if p.pos < len(p.src) {
		return nil, fmt.Errorf("line %d: an unexpected \"}\"", p.line)
	}
	return ds, nil
}

type parser struct {
	src   string
	pos   int
	line  int
	count int
}

// token is a word, a quoted string, or one of ; { }.
type token struct {
	text   string
	quoted bool
	line   int
}

func (p *parser) errorf(line int, format string, args ...any) error {
	return fmt.Errorf("line %d: %s", line, fmt.Sprintf(format, args...))
}

// next returns the next token, or false at the end.
func (p *parser) next() (token, bool, error) {
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch {
		case c == '\n':
			p.line++
			p.pos++
		case c == ' ' || c == '\t' || c == '\r':
			p.pos++
		case c == '#':
			for p.pos < len(p.src) && p.src[p.pos] != '\n' {
				p.pos++
			}
		case c == ';' || c == '{' || c == '}':
			p.pos++
			return token{text: string(c), line: p.line}, true, nil
		case c == '"' || c == '\'':
			start := p.line
			p.pos++
			var b strings.Builder
			for {
				if p.pos >= len(p.src) {
					return token{}, false, p.errorf(start, "a quoted string that does not end")
				}
				d := p.src[p.pos]
				p.pos++
				if d == c {
					break
				}
				if d == '\\' && p.pos < len(p.src) {
					e := p.src[p.pos]
					p.pos++
					switch e {
					case '"', '\'', '\\':
						d = e
					case 'n':
						d = '\n'
					case 't':
						d = '\t'
					default:
						b.WriteByte('\\')
						d = e
					}
				}
				if d == '\n' {
					p.line++
				}
				b.WriteByte(d)
			}
			return token{text: b.String(), quoted: true, line: start}, true, nil
		default:
			start := p.pos
			for p.pos < len(p.src) {
				d := p.src[p.pos]
				if d == ' ' || d == '\t' || d == '\r' || d == '\n' || d == ';' || d == '{' || d == '}' {
					break
				}
				if d == '\\' && p.pos+1 < len(p.src) {
					p.pos++
				}
				// A variable in braces, ${name}, holds its brace.
				if d == '$' && p.pos+1 < len(p.src) && p.src[p.pos+1] == '{' {
					if end := strings.IndexByte(p.src[p.pos:], '}'); end > 0 {
						p.pos += end
					}
				}
				p.pos++
			}
			return token{text: p.src[start:p.pos], line: p.line}, true, nil
		}
	}
	return token{}, false, nil
}

// skipLua passes over the Lua code of d, an OpenResty *_by_lua_block
// directive, to the "}" that ends it: braces in Lua's strings, long
// brackets and comments do not count.
func (p *parser) skipLua(d *directive) error {
	depth := 1
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch {
		case c == '\n':
			p.line++
			p.pos++
		case c == '{':
			depth++
			p.pos++
		case c == '}':
			p.pos++
			if depth--; depth == 0 {
				return nil
			}
		case c == '-' && strings.HasPrefix(p.src[p.pos:], "--"):
			p.pos += 2
			if p.longBracket() {
				continue
			}
			for p.pos < len(p.src) && p.src[p.pos] != '\n' {
				p.pos++
			}
		case c == '[' && p.longBracket():
		case c == '"' || c == '\'':
			p.pos++
			// A string ends at its quote, or, unended, at the line's end.
			for p.pos < len(p.src) && p.src[p.pos] != c && p.src[p.pos] != '\n' {
				if p.src[p.pos] == '\\' && p.pos+1 < len(p.src) && p.src[p.pos+1] != '\n' {
					p.pos++
				}
				p.pos++
			}
			if p.pos < len(p.src) && p.src[p.pos] == c {
				p.pos++
			}
		default:
			p.pos++
		}
	}
	return p.errorf(d.line, "%s: a block of Lua that does not end", d.name)
}

// longBracket passes over a Lua long bracket, [[...]] or [==[...]==], at
// the parser's place, and reports whether there was one.
func (p *parser) longBracket() bool {
	s := p.src[p.pos:]
	if !strings.HasPrefix(s, "[") {
		return false
	}
	level := 1
	for level < len(s) && s[level] == '=' {
		level++
	}
	if level >= len(s) || s[level] != '[' {
		return false
	}
	end := "]" + strings.Repeat("=", level-1) + "]"
	i := strings.Index(s[level+1:], end)
	if i < 0 {
		p.line += strings.Count(s, "\n")
		p.pos = len(p.src)
		return true
	}
	body := s[:level+1+i+len(end)]
	p.line += strings.Count(body, "\n")
	p.pos += len(body)
	return true
}

// block reads directives until the "}" that ends the block, or the end of
// the config at depth 0.
func (p *parser) block(depth int) ([]*directive, error) {
	if depth > maxNesting {
		return nil, p.errorf(p.line, "blocks nested too deep")
	}
	var ds []*directive
	for {
		t, ok, err := p.next()
		if err != nil {
			return nil, err
		}
		if !ok {
			if depth > 0 {
				return nil, p.errorf(p.line, "a block that does not end")
			}
			return ds, nil
		}
		if !t.quoted && t.text == "}" {
			if depth == 0 {
				p.pos--
				return ds, nil
			}
			return ds, nil
		}
		if !t.quoted && (t.text == ";" || t.text == "{") {
			return nil, p.errorf(t.line, "a %q with no directive before it", t.text)
		}
		if p.count++; p.count > maxDirectives {
			return nil, p.errorf(t.line, "more than %d directives", maxDirectives)
		}
		d := &directive{name: t.text, line: t.line}
		for {
			a, ok, err := p.next()
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, p.errorf(d.line, "%s does not end with \";\"", d.name)
			}
			if !a.quoted && a.text == ";" {
				break
			}
			if !a.quoted && a.text == "{" {
				d.isBlock = true
				if strings.HasSuffix(d.name, "_by_lua_block") {
					// OpenResty's Lua: code, not directives, kept out.
					if err := p.skipLua(d); err != nil {
						return nil, err
					}
					break
				}
				if d.block, err = p.block(depth + 1); err != nil {
					return nil, err
				}
				break
			}
			if !a.quoted && a.text == "}" {
				return nil, p.errorf(a.line, "%s does not end with \";\"", d.name)
			}
			d.args = append(d.args, a.text)
		}
		ds = append(ds, d)
	}
}
