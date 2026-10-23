// Package rewrite has the rewrite middleware, which changes a request's path
// before the handler sees it, and the redirect middleware, which answers a
// request with a redirect.
package rewrite

import (
	stdhttp "net/http"
	"regexp"
	"strings"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/module"
)

func init() {
	module.Register(module.Info{ID: module.Middlewares + ".rewrite", New: func() any { return new(Rewrite) }, Doc: "Rewrites the request's path: strips or adds a prefix, or replaces what a regular expression matches."})
	module.Register(module.Info{ID: module.Middlewares + ".redirect", New: func() any { return new(Redirect) }, Doc: "Redirects requests whose path a regular expression matches to a URL made from it."})
}

// Rewrite changes the request's path: it strips a prefix, replaces what a
// regular expression matches, and adds a prefix, in that order. The route
// has already been picked by the path as the client sent it; the handler,
// and the upstream a proxy sends the request to, get the new one.
type Rewrite struct {
	StripPrefix []string `json:"strip_prefix,omitempty" doc:"Prefixes to take off the path: the first that the path starts with, at a segment boundary, is removed, and what is left starts with '/'. \"/api\" makes \"/api/users\" \"/users\" and \"/api\" \"/\", and leaves \"/apix\" alone."`
	Regexp      string   `json:"regexp,omitempty" doc:"A regular expression in Go's syntax; each match in the path is replaced with replacement."`
	Replacement string   `json:"replacement,omitempty" doc:"What replaces each match of regexp, in which $1 or ${name} stand for the regexp's groups."`
	AddPrefix   string   `json:"add_prefix,omitempty" doc:"A prefix put before the path, as in \"/v2\"."`

	re *regexp.Regexp
}

// Provision checks the params and compiles the regexp.
func (rw *Rewrite) Provision(ctx *module.Context) error {
	for _, p := range rw.StripPrefix {
		if !strings.HasPrefix(p, "/") {
			return ctx.Errorf("strip_prefix", "%q does not start with '/'", p)
		}
	}
	if rw.AddPrefix != "" && (!strings.HasPrefix(rw.AddPrefix, "/") || strings.HasSuffix(rw.AddPrefix, "/")) {
		return ctx.Errorf("add_prefix", "%q does not start with '/', or ends with one", rw.AddPrefix)
	}
	if rw.Regexp == "" {
		if rw.Replacement != "" {
			return ctx.Errorf("replacement", "a replacement needs a regexp")
		}
	} else {
		re, err := regexp.Compile(rw.Regexp)
		if err != nil {
			return ctx.Errorf("regexp", "%q is not a valid regular expression: %v", rw.Regexp, err)
		}
		rw.re = re
	}
	if len(rw.StripPrefix) == 0 && rw.re == nil && rw.AddPrefix == "" {
		return ctx.Errorf("", "strip_prefix, regexp or add_prefix is needed")
	}
	return nil
}

// Wrap returns next behind the rewrite.
func (rw *Rewrite) Wrap(next module.Handler) module.Handler {
	return fibhttp.HandlerFunc(func(c *fibhttp.Context) {
		if p := rw.path(c.Request.URL.Path); p != c.Request.URL.Path {
			// The path as escaped is that of the old path; the new one is
			// escaped afresh when it is written.
			c.Request.URL.Path, c.Request.URL.RawPath = p, ""
		}
		next.ServeHTTP(c)
	})
}

// path returns p rewritten.
func (rw *Rewrite) path(p string) string {
	for _, prefix := range rw.StripPrefix {
		if rest, ok := cutSegments(p, prefix); ok {
			p = rest
			break
		}
	}
	if rw.re != nil {
		p = rw.re.ReplaceAllString(p, rw.Replacement)
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
	}
	if rw.AddPrefix != "" {
		p = rw.AddPrefix + p
	}
	return p
}

// cutSegments removes prefix from p when p is prefix or goes on with a '/'
// after it, and returns what is left, which starts with '/'. A prefix that
// ends in '/' is cut as it is.
func cutSegments(p, prefix string) (string, bool) {
	rest, ok := strings.CutPrefix(p, strings.TrimSuffix(prefix, "/"))
	switch {
	case !ok:
		return p, false
	case rest == "":
		return "/", true
	case rest[0] == '/':
		return rest, true
	}
	return p, false
}

// Redirect answers requests with a redirect to a URL made from them. With a
// regexp, only requests whose path it matches are redirected, and the
// others go on to the handler.
type Redirect struct {
	To     string `json:"to" fit:"required" doc:"Where to: a URL or a path, in which {scheme}, {host} (with its port, as the client sent it), {path}, {query} (without the '?') and {uri} (the path and query) stand for the request's, and, with regexp, $1 or ${name} for its groups, as in \"https://{host}{uri}\" or \"/new/$1\"."`
	Status int    `json:"status,omitempty" default:"302" doc:"The status: 301 and 308 are permanent, 302, 303 and 307 temporary; 307 and 308 keep the method and body. 302 when left out."`
	Regexp string `json:"regexp,omitempty" doc:"A regular expression in Go's syntax that the path must match for the request to be redirected; the rest go on to the handler. Every request is redirected when left out."`

	re *regexp.Regexp
}

// Provision checks the params.
func (rd *Redirect) Provision(ctx *module.Context) error {
	switch rd.Status {
	case 0:
		rd.Status = stdhttp.StatusFound
	case 301, 302, 303, 307, 308:
	default:
		return ctx.Errorf("status", "%d is not a redirect status: use 301, 302, 303, 307 or 308", rd.Status)
	}
	if strings.ContainsAny(rd.To, "\r\n\x00") {
		return ctx.Errorf("to", "the URL holds a line break or NUL")
	}
	if rd.Regexp != "" {
		re, err := regexp.Compile(rd.Regexp)
		if err != nil {
			return ctx.Errorf("regexp", "%q is not a valid regular expression: %v", rd.Regexp, err)
		}
		rd.re = re
	}
	return nil
}

// Wrap returns next behind the redirect.
func (rd *Redirect) Wrap(next module.Handler) module.Handler {
	return fibhttp.HandlerFunc(func(c *fibhttp.Context) {
		to := rd.To
		if rd.re != nil {
			m := rd.re.FindStringSubmatchIndex(c.Request.URL.Path)
			if m == nil {
				next.ServeHTTP(c)
				return
			}
			to = string(rd.re.ExpandString(nil, to, c.Request.URL.Path, m))
		}
		_ = c.WriteResponse(fibhttp.Response{
			StatusCode: rd.Status,
			Header:     stdhttp.Header{"Location": {expand(to, c.Request)}},
		})
	})
}

// expand replaces the placeholders in s with r's.
func expand(s string, r *stdhttp.Request) string {
	if !strings.Contains(s, "{") {
		return s
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	uri := r.URL.EscapedPath()
	if r.URL.RawQuery != "" {
		uri += "?" + r.URL.RawQuery
	}
	return strings.NewReplacer(
		"{scheme}", scheme,
		"{host}", r.Host,
		"{path}", r.URL.EscapedPath(),
		"{query}", r.URL.RawQuery,
		"{uri}", uri,
	).Replace(s)
}
