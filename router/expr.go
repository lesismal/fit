package router

import (
	"fmt"
	stdhttp "net/http"
	"strconv"
	"strings"
)

// ParseExpr compiles an expression into a Matcher. An expression combines
// matchers with && (and), || (or), ! (not) and parentheses; && binds tighter
// than ||, as in Go:
//
//	Host("example.com", "*.example.com") && (PathPrefix("/api/") || Method("POST"))
//
// Its matchers take string arguments, quoted as in Go, with "..." or `...`:
//
//   - Host(host...): one of the hosts, exact or a "*." wildcard for one
//     label, ignoring case, a port and a trailing dot;
//   - Path(path...): one of the paths, exactly;
//   - PathPrefix(prefix...): a path that starts with one of the prefixes;
//   - PathRegexp(expr...): see PathRegexp;
//   - Method(method...): see Methods;
//   - Header(name, value...): a header named name, with one of the values,
//     or with any value when none are given; see Headers;
//   - Query(key, value...): a query parameter, alike; see Query;
//   - RemoteIP(net...): see RemoteIP.
//
// An expression is compiled once, when the config is; evaluating it does not
// allocate, but for PathRegexp.
func ParseExpr(s string) (Expr, error) {
	if len(s) > maxExpr {
		return nil, fmt.Errorf("the expression is longer than %d bytes", maxExpr)
	}
	p := &parser{s: s}
	e, err := p.or()
	if err != nil {
		return nil, err
	}
	if t := p.next(); p.err != nil {
		return nil, p.err
	} else if t.kind != tokEOF {
		return nil, p.errorf("expected && or ||, found %s", t)
	}
	return e, nil
}

// Limits on an expression, so that a config cannot make the parser recurse
// without end.
const (
	maxExpr  = 64 << 10
	maxDepth = 100
)

// Expr is a parsed expression, which String prints back in a canonical form.
type Expr interface {
	Matcher
	fmt.Stringer
}

type andExpr struct{ a, b Expr }
type orExpr struct{ a, b Expr }
type notExpr struct{ e Expr }

func (e andExpr) Match(r *stdhttp.Request) bool { return e.a.Match(r) && e.b.Match(r) }
func (e orExpr) Match(r *stdhttp.Request) bool  { return e.a.Match(r) || e.b.Match(r) }
func (e notExpr) Match(r *stdhttp.Request) bool { return !e.e.Match(r) }

// String prints e with only the parentheses its structure needs: && binds
// tighter than ||, and both group to the left.
func (e andExpr) String() string {
	return paren(e.a, isOr(e.a)) + " && " + paren(e.b, isOr(e.b) || isAnd(e.b))
}
func (e orExpr) String() string  { return e.a.String() + " || " + paren(e.b, isOr(e.b)) }
func (e notExpr) String() string { return "!" + paren(e.e, isOr(e.e) || isAnd(e.e)) }

func paren(e Expr, need bool) string {
	if need {
		return "(" + e.String() + ")"
	}
	return e.String()
}

func isAnd(e Expr) bool { _, ok := e.(andExpr); return ok }
func isOr(e Expr) bool  { _, ok := e.(orExpr); return ok }

// call is a matcher with the call it was written as.
type call struct {
	Matcher
	name string
	args []string
}

func (c call) String() string {
	quoted := make([]string, len(c.args))
	for i, a := range c.args {
		quoted[i] = strconv.Quote(a)
	}
	return c.name + "(" + strings.Join(quoted, ", ") + ")"
}

// matchers are the matchers an expression may call, by name; each builds a
// Matcher from its arguments, of which there is at least one.
var matchers = map[string]func(args []string) (Matcher, error){
	"Host":       hostCall,
	"Path":       pathCall,
	"PathPrefix": prefixCall,
	"PathRegexp": func(args []string) (Matcher, error) { return PathRegexp(args...) },
	"Method":     func(args []string) (Matcher, error) { return Methods(args...), nil },
	"Header": func(args []string) (Matcher, error) {
		if args[0] == "" {
			return nil, fmt.Errorf("the header's name is empty")
		}
		return Headers(map[string][]string{args[0]: args[1:]}), nil
	},
	"Query": func(args []string) (Matcher, error) {
		if args[0] == "" {
			return nil, fmt.Errorf("the query parameter's name is empty")
		}
		return Query(map[string][]string{args[0]: args[1:]}), nil
	},
	"RemoteIP": func(args []string) (Matcher, error) { return RemoteIP(args...) },
}

type tokKind int

const (
	tokEOF tokKind = iota
	tokIdent
	tokString
	tokAnd
	tokOr
	tokNot
	tokLParen
	tokRParen
	tokComma
)

type token struct {
	kind tokKind
	text string // an identifier, or a string's value
	pos  int
}

func (t token) String() string {
	switch t.kind {
	case tokEOF:
		return "the end"
	case tokIdent:
		return t.text
	case tokString:
		return strconv.Quote(t.text)
	}
	return fmt.Sprintf("%q", [...]string{tokAnd: "&&", tokOr: "||", tokNot: "!", tokLParen: "(", tokRParen: ")", tokComma: ","}[t.kind])
}

type parser struct {
	s     string
	i     int
	tok   token
	back  bool // next returns tok again
	depth int
	err   error
}

func (p *parser) errorf(format string, args ...any) error {
	return fmt.Errorf("at byte %d: %s", p.tok.pos+1, fmt.Sprintf(format, args...))
}

// next reads the next token into p.tok and returns it; a token it cannot
// read sets p.err and reads as the end.
func (p *parser) next() token {
	if p.back {
		p.back = false
		return p.tok
	}
	for p.i < len(p.s) && strings.IndexByte(" \t\r\n", p.s[p.i]) >= 0 {
		p.i++
	}
	start := p.i
	p.tok = token{pos: start}
	if p.i >= len(p.s) {
		return p.tok
	}
	switch c := p.s[p.i]; {
	case c == '&' || c == '|':
		if p.i+1 >= len(p.s) || p.s[p.i+1] != c {
			p.err = p.errorf("expected %c%c", c, c)
			return p.tok
		}
		p.i += 2
		p.tok.kind = tokOr
		if c == '&' {
			p.tok.kind = tokAnd
		}
	case c == '!' || c == '(' || c == ')' || c == ',':
		p.i++
		p.tok.kind = [...]tokKind{'!': tokNot, '(': tokLParen, ')': tokRParen, ',': tokComma}[c]
	case c == '"' || c == '`':
		end := p.i + 1
		for end < len(p.s) && p.s[end] != c && p.s[end] != '\n' {
			if c == '"' && p.s[end] == '\\' {
				end++
			}
			end++
		}
		if end >= len(p.s) || p.s[end] != c {
			p.err = p.errorf("the string is not closed")
			return p.tok
		}
		v, err := strconv.Unquote(p.s[p.i : end+1])
		if err != nil {
			p.err = p.errorf("%s is not a valid string", p.s[p.i:end+1])
			return p.tok
		}
		p.i = end + 1
		p.tok.kind, p.tok.text = tokString, v
	case isLetter(c):
		for p.i < len(p.s) && (isLetter(p.s[p.i]) || '0' <= p.s[p.i] && p.s[p.i] <= '9') {
			p.i++
		}
		p.tok.kind, p.tok.text = tokIdent, p.s[start:p.i]
	default:
		p.err = p.errorf("unexpected %q", c)
	}
	return p.tok
}

func isLetter(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || c == '_' }

// or parses a || b || ...
func (p *parser) or() (Expr, error) {
	e, err := p.and()
	for err == nil {
		if p.next(); p.err != nil {
			return nil, p.err
		}
		if p.tok.kind != tokOr {
			p.back = true
			break
		}
		var b Expr
		if b, err = p.and(); err == nil {
			e = orExpr{e, b}
		}
	}
	return e, err
}

// and parses a && b && ...
func (p *parser) and() (Expr, error) {
	e, err := p.unary()
	for err == nil {
		if p.next(); p.err != nil {
			return nil, p.err
		}
		if p.tok.kind != tokAnd {
			p.back = true
			break
		}
		var b Expr
		if b, err = p.unary(); err == nil {
			e = andExpr{e, b}
		}
	}
	return e, err
}

// unary parses !e, (e) and calls.
func (p *parser) unary() (Expr, error) {
	if p.depth++; p.depth > maxDepth {
		return nil, p.errorf("the expression nests deeper than %d", maxDepth)
	}
	defer func() { p.depth-- }()
	t := p.next()
	if p.err != nil {
		return nil, p.err
	}
	switch t.kind {
	case tokNot:
		e, err := p.unary()
		if err != nil {
			return nil, err
		}
		return notExpr{e}, nil
	case tokLParen:
		e, err := p.or()
		if err != nil {
			return nil, err
		}
		if err := p.expect(tokRParen, "expected ) or an operator"); err != nil {
			return nil, err
		}
		return e, nil
	case tokIdent:
		return p.call(t)
	}
	return nil, p.errorf("expected a matcher, ! or (, found %s", t)
}

func (p *parser) expect(kind tokKind, msg string) error {
	t := p.next()
	if p.err != nil {
		return p.err
	}
	if t.kind != kind {
		return p.errorf("%s, found %s", msg, t)
	}
	return nil
}

// call parses the arguments of a matcher named by t and builds it.
func (p *parser) call(t token) (Expr, error) {
	build, ok := matchers[t.text]
	if !ok {
		return nil, p.errorf("there is no matcher %s", t.text)
	}
	if err := p.expect(tokLParen, "expected ( after "+t.text); err != nil {
		return nil, err
	}
	var args []string
	for {
		a := p.next()
		if p.err != nil {
			return nil, p.err
		}
		if a.kind == tokRParen && len(args) == 0 {
			break
		}
		if a.kind != tokString {
			return nil, p.errorf("expected a string, found %s", a)
		}
		args = append(args, a.text)
		sep := p.next()
		if p.err != nil {
			return nil, p.err
		}
		if sep.kind == tokRParen {
			break
		}
		if sep.kind != tokComma {
			return nil, p.errorf("expected , or ), found %s", sep)
		}
	}
	if len(args) == 0 {
		return nil, fmt.Errorf("at byte %d: %s needs at least one argument", t.pos+1, t.text)
	}
	m, err := build(args)
	if err != nil {
		return nil, fmt.Errorf("at byte %d: %s: %v", t.pos+1, t.text, err)
	}
	return call{Matcher: m, name: t.text, args: args}, nil
}

// hostCall builds Host's matcher.
func hostCall(args []string) (Matcher, error) {
	var m hostMatcher
	for _, h := range args {
		h = strings.ToLower(strings.TrimSuffix(strings.Trim(h, "[]"), "."))
		if suffix, ok := strings.CutPrefix(h, "*"); ok {
			if !strings.HasPrefix(suffix, ".") || len(suffix) < 2 || strings.Contains(suffix, "*") {
				return nil, fmt.Errorf("%q is not a host or a \"*.\" wildcard", h)
			}
			m.wild = append(m.wild, suffix)
			continue
		}
		if h == "" || strings.Contains(h, "*") {
			return nil, fmt.Errorf("%q is not a host or a \"*.\" wildcard", h)
		}
		m.exact = append(m.exact, h)
	}
	return m, nil
}

type hostMatcher struct {
	exact []string // lower-case
	wild  []string // lower-case suffixes from the dot on: ".example.com"
}

func (m hostMatcher) Match(r *stdhttp.Request) bool {
	host := strings.TrimSuffix(hostOnly(r.Host), ".")
	for _, h := range m.exact {
		if strings.EqualFold(host, h) {
			return true
		}
	}
	if i := strings.IndexByte(host, '.'); i > 0 {
		for _, w := range m.wild {
			if strings.EqualFold(host[i:], w) {
				return true
			}
		}
	}
	return false
}

func checkPaths(args []string) error {
	for _, a := range args {
		if !strings.HasPrefix(a, "/") {
			return fmt.Errorf("%q does not start with '/'", a)
		}
	}
	return nil
}

func pathCall(args []string) (Matcher, error) {
	if err := checkPaths(args); err != nil {
		return nil, err
	}
	return pathMatcher(args), nil
}

type pathMatcher []string

func (m pathMatcher) Match(r *stdhttp.Request) bool {
	for _, p := range m {
		if r.URL.Path == p {
			return true
		}
	}
	return false
}

func prefixCall(args []string) (Matcher, error) {
	if err := checkPaths(args); err != nil {
		return nil, err
	}
	return prefixMatcher(args), nil
}

type prefixMatcher []string

func (m prefixMatcher) Match(r *stdhttp.Request) bool {
	for _, p := range m {
		if strings.HasPrefix(r.URL.Path, p) {
			return true
		}
	}
	return false
}
