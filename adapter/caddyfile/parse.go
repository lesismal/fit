package caddyfile

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// token is a word of a Caddyfile: a bare word, a quoted string, a heredoc,
// or one of the braces that open and close blocks.
type token struct {
	text   string
	quoted bool
	line   int
	// nl says a line ended between the token before it and this one.
	nl bool
}

// brace reports whether t opens ("{") or closes ("}") a block.
func (t token) brace(b string) bool { return !t.quoted && t.text == b }

// directive is one line of a block, with the block it opens, if any: a
// site's directive, a directive's subdirective, or a global option.
type directive struct {
	name    string
	args    []string
	line    int
	block   []*directive
	isBlock bool
}

// serverBlock is a block at the top of a Caddyfile: the global options (no
// keys), a snippet, or a site, with its addresses as keys.
type serverBlock struct {
	keys []string
	line int
	dirs []*directive
}

// The bounds on what a Caddyfile may hold.
const (
	maxTokens  = 200000
	maxNesting = 64
	maxImports = 1000
)

// lex splits a Caddyfile into its tokens.
func lex(src string) ([]token, error) {
	var toks []token
	line, nl := 1, true
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == '\n':
			line++
			nl = true
			i++
			continue
		case c == ' ' || c == '\t' || c == '\r':
			i++
			continue
		case c == '\\' && i+1 < len(src) && (src[i+1] == '\n' || src[i+1] == '\r' && i+2 < len(src) && src[i+2] == '\n'):
			// A line continued on the next.
			for src[i] != '\n' {
				i++
			}
			line++
			i++
			continue
		case c == '#':
			for i < len(src) && src[i] != '\n' {
				i++
			}
			continue
		}
		if len(toks) >= maxTokens {
			return nil, fmt.Errorf("line %d: more than %d tokens", line, maxTokens)
		}
		t := token{line: line, nl: nl}
		nl = false
		switch {
		case c == '"':
			i++
			var b strings.Builder
			for {
				if i >= len(src) {
					return nil, fmt.Errorf("line %d: a quoted string that does not end", t.line)
				}
				d := src[i]
				i++
				if d == '"' {
					break
				}
				if d == '\\' && i < len(src) && src[i] == '"' {
					d = '"'
					i++
				}
				if d == '\n' {
					line++
				}
				b.WriteByte(d)
			}
			t.text, t.quoted = b.String(), true
		case c == '`':
			end := strings.IndexByte(src[i+1:], '`')
			if end < 0 {
				return nil, fmt.Errorf("line %d: a quoted string that does not end", t.line)
			}
			t.text, t.quoted = src[i+1:i+1+end], true
			line += strings.Count(t.text, "\n")
			i += end + 2
		case strings.HasPrefix(src[i:], "<<") && heredocMarker.MatchString(src[i+2:]):
			text, n, lines, err := heredoc(src[i:], line)
			if err != nil {
				return nil, err
			}
			t.text, t.quoted = text, true
			line += lines
			i += n
		default:
			start := i
			for i < len(src) && src[i] != ' ' && src[i] != '\t' && src[i] != '\r' && src[i] != '\n' {
				i++
			}
			t.text = src[start:i]
		}
		t.text = expandEnv(t.text)
		toks = append(toks, t)
	}
	return toks, nil
}

var heredocMarker = regexp.MustCompile(`^[A-Za-z0-9_-]+\r?\n`)

// heredoc reads a heredoc, <<MARKER, the lines after it, and MARKER on a
// line of its own, whose indentation is taken off every line. It returns
// the text, how many bytes it took and how many lines.
func heredoc(src string, line int) (string, int, int, error) {
	eol := strings.IndexByte(src, '\n')
	marker := strings.TrimRight(src[2:eol], "\r")
	lines := strings.Split(src[eol+1:], "\n")
	for n, l := range lines {
		l = strings.TrimRight(l, "\r")
		indent := strings.TrimRight(l, " \t")
		if !strings.HasSuffix(indent, marker) || strings.TrimLeft(indent, " \t") != marker {
			continue
		}
		indent = indent[:len(indent)-len(marker)]
		body := make([]string, 0, n)
		for _, b := range lines[:n] {
			b = strings.TrimRight(b, "\r")
			if b != "" && !strings.HasPrefix(b, indent) {
				return "", 0, 0, fmt.Errorf("line %d: a heredoc line that is not indented as its closing %s", line, marker)
			}
			body = append(body, strings.TrimPrefix(b, indent))
		}
		size := eol + 1
		for _, b := range lines[:n+1] {
			size += len(b) + 1
		}
		size--
		if size > len(src) {
			size = len(src)
		}
		return strings.Join(body, "\n"), size, n + 1, nil
	}
	return "", 0, 0, fmt.Errorf("line %d: a heredoc that does not end with %s", line, marker)
}

var envPlaceholder = regexp.MustCompile(`\{\$([A-Za-z_][A-Za-z0-9_]*)(?::([^}]*))?\}`)

// expandEnv replaces {$NAME} and {$NAME:default} with the environment's
// values, as Caddy does when it reads a Caddyfile.
func expandEnv(s string) string {
	if !strings.Contains(s, "{$") {
		return s
	}
	return envPlaceholder.ReplaceAllStringFunc(s, func(m string) string {
		sub := envPlaceholder.FindStringSubmatch(m)
		if v, ok := os.LookupEnv(sub[1]); ok {
			return v
		}
		return sub[2]
	})
}

// parse reads a Caddyfile into its server blocks, with snippets imported.
func parse(src []byte) ([]*serverBlock, []string, error) {
	toks, err := lex(string(src))
	if err != nil {
		return nil, nil, err
	}
	p := &parser{snippets: map[string][]token{}}
	if toks, err = p.takeSnippets(toks); err != nil {
		return nil, nil, err
	}
	if toks, err = p.expand(toks, 0); err != nil {
		return nil, nil, err
	}
	p.toks = toks
	blocks, err := p.blocks()
	return blocks, p.warnings, err
}

type parser struct {
	toks     []token
	pos      int
	snippets map[string][]token
	imports  int
	warnings []string
}

func (p *parser) warn(line int, format string, args ...any) {
	p.warnings = append(p.warnings, fmt.Sprintf("line %d: %s", line, fmt.Sprintf(format, args...)))
}

// takeSnippets takes the snippets, (name) { ... } at the top, out of toks.
func (p *parser) takeSnippets(toks []token) ([]token, error) {
	var out []token
	for i := 0; i < len(toks); {
		t := toks[i]
		if t.nl && !t.quoted && len(t.text) > 2 && t.text[0] == '(' && t.text[len(t.text)-1] == ')' &&
			i+1 < len(toks) && toks[i+1].brace("{") && !toks[i+1].nl {
			end, err := matchBrace(toks, i+1)
			if err != nil {
				return nil, err
			}
			name := t.text[1 : len(t.text)-1]
			if _, ok := p.snippets[name]; ok {
				return nil, fmt.Errorf("line %d: snippet %s is defined twice", t.line, name)
			}
			p.snippets[name] = toks[i+2 : end]
			i = end + 1
			continue
		}
		// Only blocks at the top hold snippets: skip over the others.
		if t.brace("{") {
			end, err := matchBrace(toks, i)
			if err != nil {
				return nil, err
			}
			out = append(out, toks[i:end+1]...)
			i = end + 1
			continue
		}
		out = append(out, t)
		i++
	}
	return out, nil
}

// matchBrace returns the index of the "}" that closes the "{" at open.
func matchBrace(toks []token, open int) (int, error) {
	depth := 0
	for i := open; i < len(toks); i++ {
		switch {
		case toks[i].brace("{"):
			if depth++; depth > maxNesting {
				return 0, fmt.Errorf("line %d: blocks nested too deep", toks[i].line)
			}
		case toks[i].brace("}"):
			if depth--; depth == 0 {
				return i, nil
			}
		}
	}
	return 0, fmt.Errorf("line %d: a block that does not end", toks[open].line)
}

var snippetArg = regexp.MustCompile(`\{args(?:\[(\d+|:)\]|\.(\d+))\}`)

// expand replaces each import of a snippet with the snippet's tokens, its
// {args[N]} replaced with the import's arguments.
func (p *parser) expand(toks []token, depth int) ([]token, error) {
	if depth > 10 {
		return nil, fmt.Errorf("snippets import each other too deep")
	}
	var out []token
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		lineStart := t.nl || i > 0 && toks[i-1].brace("{")
		if !lineStart || t.quoted || t.text != "import" {
			out = append(out, t)
			continue
		}
		var args []string
		j := i + 1
		for ; j < len(toks) && !toks[j].nl && !toks[j].brace("{") && !toks[j].brace("}"); j++ {
			args = append(args, toks[j].text)
		}
		i = j - 1
		if len(args) == 0 {
			return nil, fmt.Errorf("line %d: import names no snippet", t.line)
		}
		body, ok := p.snippets[args[0]]
		if !ok {
			p.warn(t.line, "import %s: skipped: importing files is not converted; only snippets defined in the Caddyfile are", args[0])
			continue
		}
		if p.imports++; p.imports > maxImports {
			return nil, fmt.Errorf("line %d: more than %d imports", t.line, maxImports)
		}
		args = args[1:]
		sub := make([]token, len(body))
		for k, b := range body {
			b.text = snippetArg.ReplaceAllStringFunc(b.text, func(m string) string {
				g := snippetArg.FindStringSubmatch(m)
				n := g[1] + g[2]
				if n == ":" {
					return strings.Join(args, " ")
				}
				idx, _ := strconv.Atoi(n)
				if idx < len(args) {
					return args[idx]
				}
				return ""
			})
			sub[k] = b
		}
		if len(sub) > 0 {
			sub[0].nl = true
		}
		if len(out)+len(sub) > maxTokens {
			return nil, fmt.Errorf("line %d: more than %d tokens once snippets are imported", t.line, maxTokens)
		}
		sub, err := p.expand(sub, depth+1)
		if err != nil {
			return nil, err
		}
		out = append(out, sub...)
		// What follows the import starts a line of its own.
		if j < len(toks) {
			toks[j].nl = true
		}
	}
	return out, nil
}

func (p *parser) peek() (token, bool) {
	if p.pos >= len(p.toks) {
		return token{}, false
	}
	return p.toks[p.pos], true
}

// blocks reads the server blocks: the global options, if they come first,
// and the sites. A Caddyfile of one site may leave out its braces.
func (p *parser) blocks() ([]*serverBlock, error) {
	var out []*serverBlock
	for {
		t, ok := p.peek()
		if !ok {
			return out, nil
		}
		b := &serverBlock{line: t.line}
		if t.brace("{") {
			if len(out) > 0 {
				return nil, fmt.Errorf("line %d: a block with no site address; global options come first", t.line)
			}
			p.pos++
			dirs, err := p.directives(1)
			if err != nil {
				return nil, err
			}
			b.dirs = dirs
			out = append(out, b)
			continue
		}
		if t.brace("}") {
			return nil, fmt.Errorf("line %d: an unexpected \"}\"", t.line)
		}
		// The keys: the addresses on the line, and on the lines after it
		// while one ends with a comma.
		more := false
		for first := true; ; first = false {
			t, ok := p.peek()
			if !ok || t.brace("{") || t.brace("}") || !first && t.nl && !more {
				break
			}
			p.pos++
			more = strings.HasSuffix(t.text, ",")
			for k := range strings.SplitSeq(t.text, ",") {
				if k != "" {
					b.keys = append(b.keys, k)
				}
			}
		}
		if t, ok := p.peek(); ok && t.brace("{") && !t.nl {
			p.pos++
			dirs, err := p.directives(1)
			if err != nil {
				return nil, err
			}
			b.dirs = dirs
			out = append(out, b)
			continue
		}
		// A site without braces: the rest of the Caddyfile is its own.
		for _, o := range out {
			if len(o.keys) > 0 {
				return nil, fmt.Errorf("line %d: site %s has no braces, which only a Caddyfile of one site may leave out", b.line, strings.Join(b.keys, ", "))
			}
		}
		dirs, err := p.directives(0)
		if err != nil {
			return nil, err
		}
		b.dirs = dirs
		return append(out, b), nil
	}
}

// directives reads the lines of a block, up to the "}" that closes it, or
// the end of the Caddyfile at depth 0.
func (p *parser) directives(depth int) ([]*directive, error) {
	if depth > maxNesting {
		return nil, fmt.Errorf("blocks nested too deep")
	}
	var ds []*directive
	for {
		t, ok := p.peek()
		if !ok {
			if depth > 0 {
				line := 1
				if len(p.toks) > 0 {
					line = p.toks[len(p.toks)-1].line
				}
				return nil, fmt.Errorf("line %d: a block that does not end", line)
			}
			return ds, nil
		}
		p.pos++
		if t.brace("}") {
			if depth == 0 {
				return nil, fmt.Errorf("line %d: an unexpected \"}\"", t.line)
			}
			return ds, nil
		}
		if t.brace("{") {
			return nil, fmt.Errorf("line %d: a block with no directive before it", t.line)
		}
		d := &directive{name: t.text, line: t.line}
		for {
			a, ok := p.peek()
			if !ok || a.nl || a.brace("}") {
				break
			}
			p.pos++
			if a.brace("{") {
				d.isBlock = true
				block, err := p.directives(depth + 1)
				if err != nil {
					return nil, err
				}
				d.block = block
				break
			}
			d.args = append(d.args, a.text)
		}
		ds = append(ds, d)
	}
}
