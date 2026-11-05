// Package dotfile tells the request paths that name a dotfile: a file or a
// directory whose name starts with a dot, as .git, .env and .htpasswd do,
// which the handlers that serve a directory hide unless their dotfiles param
// allows them.
//
// On Windows a file can also be named by its 8.3 short name, which NTFS
// makes for a long name unless the volume has them turned off: .git is also
// GIT~1, .htpasswd HTPASS~1, with no dot at the front to tell them by.
// HasShortName finds paths with a segment of that shape, which the handlers
// hide too where ShortNames says the system has them.
package dotfile

import (
	"runtime"
	"strings"
)

// WellKnown is the one dot directory that is not hidden, as the first
// segment of a path: RFC 8615's /.well-known/, which ACME's HTTP-01
// challenge and security.txt are served under.
const WellKnown = ".well-known"

// ShortNames says files may have 8.3 short names besides their own: on
// Windows. The handlers that hide dotfiles hide every path HasShortName
// finds there too, deciding when they are provisioned, so that other
// systems' requests are not looked at twice.
const ShortNames = runtime.GOOS == "windows"

// Values of the handlers' dotfiles param.
const (
	// Deny answers a request for a dotfile 404, as for a file not there. It
	// is the default.
	Deny = "deny"
	// Allow serves dotfiles as other files.
	Allow = "allow"
)

// Scan looks at the segments of p, a slash-separated request path, in one
// pass. unsafe says one of them is "..", or p holds a NUL or a backslash,
// which Windows takes as a separator: what could name a file outside the
// root, or a different file on some systems. hidden says a segment starts
// with a dot, but for "." and for WellKnown as the first. The whole of p is looked at,
// whatever is found first.
func Scan(p string) (unsafe, hidden bool) {
	first := true
	start := 0
	for i := 0; i <= len(p); i++ {
		if i < len(p) {
			switch p[i] {
			case 0, '\\':
				unsafe = true
				continue
			case '/':
			default:
				continue
			}
		}
		segment := p[start:i]
		start = i + 1
		if segment == "" || segment == "." {
			// No name: the directory the path is at.
			continue
		}
		if segment[0] == '.' {
			switch {
			case segment == "..":
				unsafe = true
			case first && segment == WellKnown:
			default:
				hidden = true
			}
		}
		first = false
	}
	return unsafe, hidden
}

// Hidden reports whether p names a dotfile, or a path in a dot directory, as
// Scan's hidden says.
func Hidden(p string) bool {
	_, hidden := Scan(p)
	return hidden
}

// HasShortName reports whether a segment of p, a slash-separated request
// path, has the shape of an 8.3 short name NTFS makes: up to eight
// characters ending in "~" and digits, as GIT~1 or GITIG~12, and an
// extension of up to three, as ENV~1.LOC. What Windows strips from a name
// before it opens one counts for nothing: the trailing dots and spaces, and
// a stream after a colon, as GIT~1. or GIT~1::$INDEX_ALLOCATION.
//
// The shape is all there is to go by, so that a file whose own name has it,
// as file~1.txt, is refused with the aliases of dotfiles: the short name a
// volume gives a file is not known until it is looked up on the disk, and a
// request for a file the server does not see, as a FastCGI application's,
// cannot be looked up at all. A short name set by hand (fsutil file
// setshortname), which need not have the shape, is not found.
func HasShortName(p string) bool {
	if strings.IndexByte(p, '~') < 0 {
		return false
	}
	for p != "" {
		segment := p
		if i := strings.IndexByte(p, '/'); i >= 0 {
			segment, p = p[:i], p[i+1:]
		} else {
			p = ""
		}
		if shortName(segment) {
			return true
		}
	}
	return false
}

// shortName reports whether segment has the shape of an 8.3 short name, as
// HasShortName says.
func shortName(segment string) bool {
	if i := strings.IndexByte(segment, ':'); i >= 0 {
		segment = segment[:i]
	}
	segment = strings.TrimRight(segment, ". ")
	name, ext, _ := strings.Cut(segment, ".")
	if len(name) > 8 || len(ext) > 3 || strings.IndexByte(ext, '.') >= 0 {
		return false
	}
	tilde := strings.LastIndexByte(name, '~')
	if tilde < 0 || tilde == len(name)-1 {
		return false
	}
	for i := tilde + 1; i < len(name); i++ {
		if name[i] < '0' || name[i] > '9' {
			return false
		}
	}
	return true
}
