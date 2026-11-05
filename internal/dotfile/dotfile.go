// Package dotfile tells the request paths that name a dotfile: a file or a
// directory whose name starts with a dot, as .git, .env and .htpasswd do,
// which the handlers that serve a directory hide unless their dotfiles param
// allows them.
package dotfile

// WellKnown is the one dot directory that is not hidden, as the first
// segment of a path: RFC 8615's /.well-known/, which ACME's HTTP-01
// challenge and security.txt are served under.
const WellKnown = ".well-known"

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
