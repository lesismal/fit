package listener

import (
	stdhttp "net/http"
	"net/url"
)

// canonicalize puts r's path in its canonical form before the routes see it,
// so that the route matched, its middleware and its handler (the upstream a
// proxy forwards to, forward_auth's X-Forwarded-Uri, a FastCGI script, a
// static file) all go by the same path. It reports false for a path that is
// refused, which is answered 400.
//
// The canonical path has no dot-segments, removed as RFC 3986 section 5.2.4
// says, "%2E" counting as '.', and no empty segments: "//" is one '/'. A path
// whose ".." would climb above the root is refused. Only a literal '/'
// separates segments: "%2F" and "%5C" stay as they came, inside their
// segment, since an API may take a name with a slash in one segment (GitLab's
// "group%2Fproject"); but one that, decoded, makes an empty or dot-segment,
// with '/' or '\' around it, is refused, as no client has reason to send one
// and a backend that decodes it, or takes '\' for '/', would see another
// path than the routes did. Case and ';' are left alone, as nginx leaves
// them. RequestURI, which the access log writes and a FastCGI script reads
// as REQUEST_URI, follows a path that changes.
//
// A path with nothing to remove, nearly every one, is only scanned, without
// allocating.
func canonicalize(r *stdhttp.Request) bool {
	u := r.URL
	if u == nil || u.Path == "" || u.Path[0] != '/' || !hasDotOrEmptySegment(u.Path) {
		return true
	}
	escaped := u.EscapedPath()
	if escaped == "" || escaped[0] != '/' {
		return false
	}
	clean, ok := removeDotSegments(escaped)
	if !ok {
		return false
	}
	decoded, err := url.PathUnescape(clean)
	if err != nil || hasDotOrEmptySegment(decoded) {
		// What is left came encoded: "%2F", "%5C" or a '\'.
		return false
	}
	u.Path, u.RawPath = decoded, ""
	if decoded != clean {
		u.RawPath = clean
	}
	switch {
	case r.RequestURI == "":
	case r.RequestURI[0] == '/':
		r.RequestURI = u.RequestURI()
	default: // absolute-form
		r.RequestURI = u.String()
	}
	return true
}

// hasDotOrEmptySegment reports whether p, a path that starts with '/', has a
// segment that is empty (but the last, after a trailing slash), "." or "..",
// '\' separating segments as well as '/'.
func hasDotOrEmptySegment(p string) bool {
	for i := 0; i < len(p); i++ {
		if c := p[i]; c != '/' && c != '\\' {
			continue
		}
		rest := p[i+1:]
		if rest == "" {
			return false
		}
		switch rest[0] {
		case '/', '\\':
			return true
		case '.':
			if len(rest) == 1 || rest[1] == '/' || rest[1] == '\\' {
				return true
			}
			if rest[1] == '.' && (len(rest) == 2 || rest[2] == '/' || rest[2] == '\\') {
				return true
			}
		}
	}
	return false
}

// removeDotSegments returns escaped, an escaped path that starts with '/',
// without its dot-segments, in which "%2E" counts as '.', and its empty
// segments but a trailing one; and false when a ".." would climb above the
// root. A dot-segment that ends the path leaves a trailing slash, as RFC 3986
// has it: "/a/b/.." is "/a/".
func removeDotSegments(escaped string) (string, bool) {
	out := make([]byte, 1, len(escaped))
	out[0] = '/'
	rest := escaped[1:]
	for {
		seg, after, more := cutSlash(rest)
		switch dotSegment(seg) {
		case 1:
		case 2:
			if len(out) == 1 {
				return "", false
			}
			// out ends with the '/' after the segment to pop.
			i := len(out) - 2
			for out[i] != '/' {
				i--
			}
			out = out[:i+1]
		default:
			if seg != "" {
				out = append(out, seg...)
				if more {
					out = append(out, '/')
				}
			}
		}
		if !more {
			return string(out), true
		}
		rest = after
	}
}

// cutSlash cuts s at its first '/'.
func cutSlash(s string) (before, after string, found bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}

// dotSegment is 1 for the segment ".", 2 for "..", either with its dots
// written "%2E" or "%2e", and 0 for any other.
func dotSegment(seg string) int {
	n := 0
	for i := 0; i < len(seg); n++ {
		switch {
		case seg[i] == '.':
			i++
		case seg[i] == '%' && i+2 < len(seg) && seg[i+1] == '2' && (seg[i+2] == 'e' || seg[i+2] == 'E'):
			i += 3
		default:
			return 0
		}
	}
	if n > 2 {
		return 0
	}
	return n
}
