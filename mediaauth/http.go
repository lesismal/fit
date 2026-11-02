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
			r.RequestURI = redact(r.RequestURI)
			return Credentials{Token: t}
		}
	}
	if ck, err := r.Cookie(CookieName); err == nil && ck.Value != "" {
		return Credentials{Token: ck.Value}
	}
	return Credentials{}
}

// redact replaces the value of every token parameter in uri's query by
// REDACTED.
func redact(uri string) string {
	path, query, ok := strings.Cut(uri, "?")
	if !ok {
		return uri
	}
	parts := strings.Split(query, "&")
	for i, kv := range parts {
		if k, _, _ := strings.Cut(kv, "="); k == "token" {
			parts[i] = "token=REDACTED"
		}
	}
	return path + "?" + strings.Join(parts, "&")
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
	creds := FromHTTP(c.Request)
	subject, err := p.Allow(a, stream, creds)
	if !errors.Is(err, ErrPending) {
		finish(c, subject, err, next)
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
		finish(c, result.subject, result.err, next)
	})
	p.Check(a, stream, creds, func(subject string, err error) {
		result.subject, result.err = subject, err
		run()
	})
}

func finish(c *fibhttp.Context, subject string, err error, next func(string)) {
	if err != nil {
		WriteError(c, err)
		return
	}
	if subject != "" {
		if e := observe.EntryOf(c); e != nil {
			e.User = subject
		}
	}
	next(subject)
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
func WriteError(c *fibhttp.Context, err error) {
	status := Status(err)
	hdr := stdhttp.Header{"Content-Type": {"text/plain; charset=utf-8"}}
	var msg string
	switch {
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
