package runtime

import (
	stdhttp "net/http"
	"net/textproto"
	"strings"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/module"
)

// connectionFirst returns next, a route's middleware chain, behind a step
// that removes the fields the client's Connection field names, as they
// arrived, before any middleware runs (RFC 9110 section 7.6.1: they concern
// the client's connection only). Otherwise the proxy, which removes them
// once the chain has run, would take them for the client's when middleware
// had set them: a client's Connection: X-User would strip the X-User an auth
// middleware set, or a secret the headers middleware added, from the
// upstream's request.
//
// The names the proxy drops itself (Upgrade, TE, Trailer and the like) stay
// named, as an upgrade is read from them later, and so do keep-alive and
// close; the rest leave the Connection field with their fields, so that what
// the proxy reads of it then names nothing middleware may have set.
//
// A request with no Connection field, every HTTP/2 and HTTP/3 one and most
// of HTTP/1.1's, or one naming only keep-alive or close, costs a lookup.
func connectionFirst(next module.Handler) module.Handler {
	return fibhttp.HandlerFunc(func(c *fibhttp.Context) {
		h := c.Request.Header
		if connection, ok := h["Connection"]; ok && namesFields(connection) {
			dropConnectionNamed(h, connection)
		}
		next.ServeHTTP(c)
	})
}

// namesFields reports whether connection, a Connection field's values,
// names a field connectionFirst removes.
func namesFields(connection []string) bool {
	for _, value := range connection {
		for token := range strings.SplitSeq(value, ",") {
			if !keptConnectionToken(textproto.TrimString(token)) {
				return true
			}
		}
	}
	return false
}

// keptConnectionToken reports whether token, one of a Connection field's,
// stays named: it is empty, an option, or a field the proxy drops itself.
func keptConnectionToken(token string) bool {
	switch len(token) {
	case 0:
		return true
	case 2:
		return strings.EqualFold(token, "te")
	case 5:
		return strings.EqualFold(token, "close")
	case 7:
		return strings.EqualFold(token, "upgrade") || strings.EqualFold(token, "trailer")
	case 10:
		return strings.EqualFold(token, "keep-alive")
	case 16:
		return strings.EqualFold(token, "proxy-connection")
	case 17:
		return strings.EqualFold(token, "transfer-encoding")
	}
	return false
}

// dropConnectionNamed deletes from h the fields connection names but those
// keptConnectionToken keeps, and leaves Connection naming only those.
func dropConnectionNamed(h stdhttp.Header, connection []string) {
	var kept []string
	for _, value := range connection {
		for token := range strings.SplitSeq(value, ",") {
			token = textproto.TrimString(token)
			switch {
			case token == "":
			case keptConnectionToken(token):
				kept = append(kept, token)
			default:
				delete(h, textproto.CanonicalMIMEHeaderKey(token))
			}
		}
	}
	if len(kept) == 0 {
		delete(h, "Connection")
		return
	}
	h["Connection"] = []string{strings.Join(kept, ", ")}
}
