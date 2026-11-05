package auth

import (
	stdhttp "net/http"
	"net/textproto"
	"strings"
)

// maxFieldDashes bounds the hyphens of a field name whose spellings with _
// are listed when it is provisioned: 2^n - 1 of them. A name with more, which
// no field in use has, is found by a look at each of a request's names.
const maxFieldDashes = 8

// fieldSet is the fields a middleware sets for the handler, with every
// spelling a client could send to pass for one of them with an application
// that reads _ as -: CGI, FastCGI and many frameworks behind them (PHP, WSGI,
// Rack) fold both into HTTP_X_USER, so a client's X_User left beside the
// X-User a middleware sets could pass for it.
//
// fib's servers, on every protocol, key a request's fields as
// textproto.CanonicalMIMEHeaderKey makes them, which keeps a name's case
// apart from its separators: X_USER, x_user and X_User all arrive as X_user.
// So a name with n hyphens has 2^n - 1 spellings with _ for one or more of
// them, each with one key, and the set lists them all when it is built:
// dropping them is a map delete each, with no look at the request's other
// fields and nothing allocated.
type fieldSet struct {
	keys []string // the names and their spellings with _, canonical
	scan []string // names with more than maxFieldDashes hyphens
}

// newFieldSet returns the set of names, which are canonical.
func newFieldSet(names ...string) fieldSet {
	var fs fieldSet
	seen := map[string]bool{}
	add := func(key string) {
		if !seen[key] {
			seen[key] = true
			fs.keys = append(fs.keys, key)
		}
	}
	for _, name := range names {
		add(name)
		var dashes []int
		for i := range len(name) {
			if name[i] == '-' {
				dashes = append(dashes, i)
			}
		}
		if len(dashes) > maxFieldDashes {
			fs.scan = append(fs.scan, name)
			continue
		}
		b := []byte(name)
		for mask := 1; mask < 1<<len(dashes); mask++ {
			for bit, at := range dashes {
				b[at] = '-'
				if mask&(1<<bit) != 0 {
					b[at] = '_'
				}
			}
			add(textproto.CanonicalMIMEHeaderKey(string(b)))
		}
	}
	return fs
}

// drop deletes from h every field the client sent under one of the set's
// names or their spellings with _.
func (fs fieldSet) drop(h stdhttp.Header) {
	for _, key := range fs.keys {
		delete(h, key)
	}
	if len(fs.scan) == 0 {
		return
	}
	for key := range h {
		if strings.IndexByte(key, '_') < 0 {
			continue
		}
		for _, name := range fs.scan {
			if sameField(key, name) {
				delete(h, key)
				break
			}
		}
	}
}

// sameField reports whether a and b name the same field once case is ignored
// and _ is read as -.
func sameField(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range len(a) {
		if foldField(a[i]) != foldField(b[i]) {
			return false
		}
	}
	return true
}

func foldField(c byte) byte {
	switch {
	case c == '_':
		return '-'
	case c >= 'A' && c <= 'Z':
		return c + 'a' - 'A'
	}
	return c
}
