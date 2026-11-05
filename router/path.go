package router

import (
	"bytes"
	"net/url"
	"strings"
)

// NormalizePath brings u's path to the one routes are matched against and the
// upstream is sent, and reports false for a path that has none. It runs before
// Match, for every request on every protocol, so that a route cannot be
// stepped around with a path that reads as another once an upstream or a file
// system resolves it: "/./admin", "/x/../admin", "//admin" and
// "/x/%2e%2e/admin" all become "/admin".
//
// Dot segments are removed as RFC 3986 section 5.2.4 removes them, ".." at
// the root staying at the root, and repeated slashes merged into one; a
// segment of "%2e" or "%2E" written for a dot counts as one. Every other
// escape in the path stays as the client wrote it: u.RawPath keeps them, and
// it is what goes upstream. A path whose encoded slashes (%2F) would make dot
// segments or empty ones once decoded, as "/x/..%2Fadmin" and "/%2Fadmin" do,
// has no form the router and an upstream that decodes them would both read
// alike, and is refused: NormalizePath reports false, and the request is
// answered 400.
//
// A path with no "/." and no "//" in it, as nearly every one is, is left as it
// is without allocating; so is a path that does not start with '/', as "*" of
// OPTIONS * and the empty path of a CONNECT.
func NormalizePath(u *url.URL) bool {
	p := u.Path
	if len(p) == 0 || p[0] != '/' || !hasDotOrEmptySegment(p) {
		return true
	}
	clean := removeDotSegments(p, plainDot)
	if u.RawPath == "" {
		// The path's escapes are url.URL's own, which hold no encoded slash
		// nor encoded dot: the decoded path is the whole story.
		if clean != p {
			u.Path = clean
		}
		return true
	}
	raw := removeDotSegments(u.RawPath, escapedDot)
	decoded, err := url.PathUnescape(raw)
	if err != nil || decoded != clean {
		return false
	}
	if raw != u.RawPath {
		// RawPath only when the default escaping would differ, as url.URL
		// keeps it.
		u.Path, u.RawPath = clean, ""
		if u.EscapedPath() != raw {
			u.RawPath = raw
		}
	}
	return true
}

// hasDotOrEmptySegment reports whether p, a decoded path, has "/." or "//" in
// it: a segment that may be a dot segment, or an empty one. An encoded dot or
// slash is decoded in p, so it is caught too.
func hasDotOrEmptySegment(p string) bool {
	for i := strings.IndexByte(p, '/'); i >= 0 && i+1 < len(p); {
		if c := p[i+1]; c == '.' || c == '/' {
			return true
		}
		j := strings.IndexByte(p[i+1:], '/')
		if j < 0 {
			return false
		}
		i += 1 + j
	}
	return false
}

// Kinds of a path segment, for removeDotSegments.
const (
	segmentOther = iota
	segmentDot
	segmentDotDot
)

// plainDot is the kind of a decoded segment.
func plainDot(s string) int {
	switch s {
	case ".":
		return segmentDot
	case "..":
		return segmentDotDot
	}
	return segmentOther
}

// escapedDot is the kind of a segment as written, in which a dot may be
// "%2e" or "%2E".
func escapedDot(s string) int {
	if len(s) > 6 {
		return segmentOther
	}
	dots := 0
	for i := 0; i < len(s); dots++ {
		switch {
		case s[i] == '.':
			i++
		case i+2 < len(s) && s[i] == '%' && s[i+1] == '2' && (s[i+2] == 'e' || s[i+2] == 'E'):
			i += 3
		default:
			return segmentOther
		}
	}
	switch dots {
	case 1:
		return segmentDot
	case 2:
		return segmentDotDot
	}
	return segmentOther
}

// removeDotSegments returns p, which starts with '/', with its dot segments
// removed and its empty segments, but a last one, dropped: "/a/./b/../c//d/"
// is "/a/c/d/". kind tells a segment's kind. A trailing "." or ".." leaves the
// path ending in '/', as RFC 3986 has it.
func removeDotSegments(p string, kind func(string) int) string {
	out := make([]byte, 1, len(p))
	out[0] = '/'
	for rest := p[1:]; ; {
		seg, next, more := strings.Cut(rest, "/")
		switch kind(seg) {
		case segmentDot:
		case segmentDotDot:
			// Drop the last segment out has, if any: out ends in '/'.
			if len(out) > 1 {
				out = out[:bytes.LastIndexByte(out[:len(out)-1], '/')+1]
			}
		default:
			if seg != "" {
				out = append(out, seg...)
				if more {
					out = append(out, '/')
				}
			}
		}
		if !more {
			break
		}
		rest = next
	}
	return string(out)
}
