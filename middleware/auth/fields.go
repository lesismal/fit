package auth

import (
	stdhttp "net/http"
	"strings"
)

// dropFields deletes from h every field the client sent under one of names,
// canonical, in any spelling that an application may read as the same field:
// in any case, and with _ for -. CGI, FastCGI and many frameworks behind them
// (PHP, WSGI, Rack) fold both into HTTP_X_USER, so a client's X_User left
// beside the X-User a middleware sets could pass for it.
//
// Nearly no request has a field with _ in its name, and for those it costs a
// look at each name; it allocates nothing.
func dropFields(h stdhttp.Header, names ...string) {
	for _, name := range names {
		delete(h, name)
	}
	for key := range h {
		if strings.IndexByte(key, '_') < 0 {
			continue
		}
		for _, name := range names {
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
