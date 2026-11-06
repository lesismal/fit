package mediaauth

import (
	"errors"
	stdhttp "net/http"
	"net/url"
	"strings"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/observe"
)

// CookieName is the cookie a token may come in, as the media pages set it.
const CookieName = "fit_media_token"

// Credentials are what a request says it is: a token, or a user and a
// password.
type Credentials struct {
	Token    string
	User     string
	Password string
}

// Empty reports whether c holds no credentials.
func (c Credentials) Empty() bool { return c.Token == "" && c.User == "" }

// FromHTTP returns the credentials of an HTTP request, the first of:
// Authorization: Bearer, Authorization: Basic, the token query parameter,
// and the fit_media_token cookie. A token taken from the query is replaced
// in r.RequestURI by REDACTED, so that the access log, which writes the
// request's URI, never holds it; r.URL keeps it.
func FromHTTP(r *stdhttp.Request) Credentials {
	if h := r.Header.Get("Authorization"); h != "" {
		scheme, rest, _ := strings.Cut(h, " ")
		rest = strings.TrimSpace(rest)
		switch {
		case strings.EqualFold(scheme, "Bearer") && rest != "":
			return Credentials{Token: rest}
		case strings.EqualFold(scheme, "Basic"):
			if u, pw, ok := r.BasicAuth(); ok && u != "" {
				return Credentials{User: u, Password: pw}
			}
		}
	}
	if r.URL != nil && strings.Contains(r.URL.RawQuery, "token=") {
		if t := r.URL.Query().Get("token"); t != "" {
			Redact(r)
			return Credentials{Token: t}
		}
	}
	if ck, err := r.Cookie(CookieName); err == nil && ck.Value != "" {
		return Credentials{Token: ck.Value}
	}
	return Credentials{}
}

// Redact replaces the value of every token and sig parameter of r's
// RequestURI by REDACTED, so that the access log, which writes it, never
// holds a token or a signature, whatever the request is answered; r.URL
// keeps them. A handler that takes credentials in the query calls it before
// it answers anything; Guard, GuardResource and FromHTTP do too. It
// allocates only when there is one to replace.
func Redact(r *stdhttp.Request) {
	if needsRedaction(r.RequestURI) {
		r.RequestURI = redact(r.RequestURI)
	}
}

// needsRedaction reports whether uri's query has a token or sig parameter
// whose value is not REDACTED.
func needsRedaction(uri string) bool {
	_, query, ok := strings.Cut(uri, "?")
	for ok && query != "" {
		var kv string
		kv, query, _ = strings.Cut(query, "&")
		if k, v, _ := strings.Cut(kv, "="); (k == "token" || k == ParamSignature) && v != "REDACTED" {
			return true
		}
	}
	return false
}

// redact replaces the value of every token and sig parameter in uri's
// query by REDACTED.
func redact(uri string) string {
	path, query, ok := strings.Cut(uri, "?")
	if !ok {
		return uri
	}
	b := make([]byte, 0, len(uri)+8)
	b = append(b, path...)
	b = append(b, '?')
	for i := 0; query != "" || i == 0; i++ {
		var kv string
		kv, query, _ = strings.Cut(query, "&")
		if i > 0 {
			b = append(b, '&')
		}
		switch k, _, _ := strings.Cut(kv, "="); k {
		case "token", ParamSignature:
			b = append(b, k...)
			b = append(b, "=REDACTED"...)
		default:
			b = append(b, kv...)
		}
	}
	return string(b)
}

// GuardResource is Guard for playing resource, the path prefix of an item
// or a stream as ResourceOf gives it, whose stream name is stream. With
// the policy's url_signing, a request whose query has a signature is let
// in by that signed URL, or grant, alone, checked against resource, or
// refused 403 (503 past max_tracked_sessions), and its signature replaced
// by REDACTED in the request's RequestURI, as a token is, so that the
// access log never holds it; and, with url_signing's cookie, a request with
// the cookie GrantCookie is let in by a grant of it that holds. Any other
// request is checked as Guard checks it. A refusal carries cors, the
// handler's own, as Access-Control-Allow-Origin when it is not empty, so
// that a player on another origin sees why. next gets what let the request in,
// for the playlists it serves to hand on; it is called before
// GuardResource returns but for a bcrypt check, as Guard's.
func (p *Policy) GuardResource(c *fibhttp.Context, stream, resource, cors string, next func(Access)) {
	r := c.Request
	if p == nil {
		next(Access{req: r})
		return
	}
	st := p.state()
	if st != nil && st.url != nil && !st.public[Play] {
		if strings.Contains(r.URL.RawQuery, ParamSignature+"=") {
			Redact(r)
			var g grant
			if !g.parse(r.URL.RawQuery) {
				p.refuseURL(c, urlErrors[reasonSignature], cors)
				return
			}
			p.letIn(c, st, resource, cors, &g, next)
			return
		}
		if st.url.cookie && p.cookieGrant(c, st, resource, cors, next) {
			return
		}
	}
	p.guard(c, Play, stream, cors, nil, next)
}

// letIn checks g, from the query of c's request or a cookie, against
// resource, and lets the request in, or refuses it.
func (p *Policy) letIn(c *fibhttp.Context, st *state, resource, cors string, g *grant, next func(Access)) {
	t := now()
	k, err := st.verify(c.Request, resource, g, t)
	if err == nil && g.sid != "" {
		err = p.sess.touch(g.sub, g.sid, resource, c.Request.RemoteAddr, g.exp, t, st.url)
	}
	if err != nil {
		p.refuseURL(c, err, cors)
		return
	}
	observe.Authenticate(c).User = g.sub
	next(Access{Subject: g.sub, p: p, req: c.Request, resource: resource, g: *g, key: k, byURL: true})
}

// cookieGrant lets c's request in by a GrantCookie that holds for resource,
// and reports whether it answered the request: a request with no such
// cookie, or none that holds while it carries other credentials, goes on
// to Guard; one whose cookies all fail, with nothing else, is refused for
// the last one's reason.
func (p *Policy) cookieGrant(c *fibhttp.Context, st *state, resource, cors string, next func(Access)) bool {
	r := c.Request
	var last error
	t := now()
	for _, line := range r.Header["Cookie"] {
		for line != "" {
			var part string
			part, line, _ = strings.Cut(line, ";")
			name, value, _ := strings.Cut(strings.TrimSpace(part), "=")
			if name != GrantCookie {
				continue
			}
			var g grant
			if !g.parse(value) {
				last = urlErrors[reasonSignature]
				continue
			}
			if _, err := st.verify(r, resource, &g, t); err != nil {
				last = err
				continue
			}
			p.letIn(c, st, resource, cors, &g, next)
			return true
		}
	}
	if last == nil || r.Header.Get("Authorization") != "" || strings.Contains(r.URL.RawQuery, "token=") {
		return false
	}
	p.refuseURL(c, last, cors)
	return true
}

// refuseURL counts a refused URL and answers it.
func (p *Policy) refuseURL(c *fibhttp.Context, err error, cors string) {
	p.countRefusal(err)
	writeError(c, err, cors)
}

// FromQuery returns the credentials of a URL's query, for protocols that
// carry no header, as RTMP's and SRT's URLs: token, or user and pass. A
// query that does not parse gives what parsed of it.
func FromQuery(query string) Credentials {
	q, _ := url.ParseQuery(strings.TrimPrefix(query, "?"))
	if t := q.Get("token"); t != "" {
		return Credentials{Token: t}
	}
	if u := q.Get("user"); u != "" {
		return Credentials{User: u, Password: q.Get("pass")}
	}
	return Credentials{}
}

// Guard checks an HTTP request against the policy for a on stream, its
// credentials as FromHTTP finds them, and calls next with the subject when
// it may go on. Otherwise it answers the request itself, as WriteError
// does. A bcrypt check runs on the package's pool while the request waits,
// retained, and next is then called there; otherwise next is called before
// Guard returns. A nil policy calls next at once with "".
func (p *Policy) Guard(c *fibhttp.Context, a Action, stream string, next func(subject string)) {
	if p == nil {
		next("")
		return
	}
	p.guard(c, a, stream, "", next, nil)
}

// guard is Guard, its refusals sent with cors as Access-Control-Allow-Origin
// when it is not empty, so that a player on another origin reads them; it
// calls next, or, when that is nil, nextAccess with the Access of the
// subject, which spares GuardResource a closure of its own.
func (p *Policy) guard(c *fibhttp.Context, a Action, stream, cors string, next func(subject string), nextAccess func(Access)) {
	creds := FromHTTP(c.Request)
	subject, err := p.Allow(a, stream, creds)
	if !errors.Is(err, ErrPending) {
		p.finish(c, subject, err, cors, next, nextAccess)
		return
	}
	var result struct {
		subject string
		err     error
	}
	run := c.Async(func() {
		if c.Err() != nil {
			return // the client has gone
		}
		p.finish(c, result.subject, result.err, cors, next, nextAccess)
	})
	p.Check(a, stream, creds, func(subject string, err error) {
		result.subject, result.err = subject, err
		run()
	})
}

func (p *Policy) finish(c *fibhttp.Context, subject string, err error, cors string, next func(string), nextAccess func(Access)) {
	if err != nil {
		writeError(c, err, cors)
		return
	}
	if subject != "" {
		observe.Authenticate(c).User = subject
	}
	if next != nil {
		next(subject)
		return
	}
	nextAccess(Access{Subject: subject, p: p, req: c.Request})
}

// The challenges of a 401: a bearer token, said to be invalid when one was
// sent (RFC 6750 section 3).
var (
	challenge        = []string{`Bearer realm="fit"`}
	challengeInvalid = []string{`Bearer realm="fit", error="invalid_token"`}
)

// WriteError answers a request a check refused, with the status Status
// gives for err and a plain text message: a 401 with a WWW-Authenticate
// challenge for a bearer token, a 403, or a 503 with Retry-After.
func WriteError(c *fibhttp.Context, err error) { writeError(c, err, "") }

// writeError is WriteError with cors, when not empty, as the response's
// Access-Control-Allow-Origin.
func writeError(c *fibhttp.Context, err error, cors string) {
	status := Status(err)
	hdr := stdhttp.Header{"Content-Type": {"text/plain; charset=utf-8"}}
	if cors != "" {
		hdr["Access-Control-Allow-Origin"] = []string{cors}
	}
	var msg string
	var ue *URLError
	switch {
	case errors.As(err, &ue):
		msg = "the playback URL is refused: " + ue.Reason
	case errors.Is(err, ErrSessionsFull):
		hdr["Retry-After"] = []string{"1"}
		msg = "too many playback sessions are tracked"
	case errors.Is(err, ErrNoCredentials):
		hdr["Www-Authenticate"] = challenge
		msg = "credentials are needed: a bearer token, or a user and password"
	case errors.Is(err, ErrBadCredentials):
		hdr["Www-Authenticate"] = challengeInvalid
		msg = "the credentials are not valid"
	case errors.Is(err, ErrForbidden):
		msg = "the credentials do not allow this"
	case errors.Is(err, ErrBusy):
		hdr["Retry-After"] = []string{"1"}
		msg = "too many password checks are waiting"
	default:
		hdr["Retry-After"] = []string{"1"}
		msg = "the media_auth policy is not loaded"
	}
	_ = c.WriteResponse(fibhttp.Response{StatusCode: status, Header: hdr, Body: []byte(msg + "\n")})
}
