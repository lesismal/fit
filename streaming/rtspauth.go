package streaming

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/lesismal/fib/media/rtsp"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/mediaauth"
)

// rtspNonceLifetime is how long a Digest nonce is good for; a client with
// an older one is told it is stale and retries with a new one.
const rtspNonceLifetime = 5 * time.Minute

// rtspAuth checks RTSP requests against the listener's policy, in the RTSP
// server's OnRequest, which fib calls on the connection's worker: DESCRIBE
// needs play on the stream, ANNOUNCE publish; PLAY and RECORD need play and
// publish on the stream of the connection's session, which fib plays or
// records whatever their URL says (and refuses with 455 when the URL names
// another); and a SETUP play on its stream, or with a session, which a
// SETUP only adds a track to, play or publish on the session's. The rest
// belong to a session those began, or reveal nothing. Credentials are the
// Authorization header's, Digest (RFC 2617 with MD5, as RTSP 1.0 clients
// speak it) or Basic, or else a token, or user and pass, in the URL's query.
// Its nonces carry their time and a MAC of it, so that any connection checks
// any nonce with nothing kept.
type rtspAuth struct {
	s      *Server
	realm  string
	digest bool
	basic  bool
	secret [32]byte
}

func newRTSPAuth(c *config.StreamRTSP) *rtspAuth {
	a := &rtspAuth{realm: c.RealmName()}
	for _, m := range c.Methods() {
		switch m {
		case "digest":
			a.digest = true
		case "basic":
			a.basic = true
		}
	}
	// crypto/rand.Read never fails: it crashes the program instead.
	_, _ = rand.Read(a.secret[:])
	return a
}

func (a *rtspAuth) onRequest(sc *rtsp.ServerConn, req *rtsp.Request) *rtsp.Response {
	switch req.Method {
	case rtsp.MethodDescribe, rtsp.MethodPlay, rtsp.MethodAnnounce, rtsp.MethodRecord, rtsp.MethodSetup:
	default:
		return nil
	}
	g := a.s.auth[protoRTSP].Load()
	if g == nil || g.name == "" {
		return nil
	}
	path, query, ok := rtspPath(req.URL)
	if !ok {
		return nil // fib answers 400
	}
	now := time.Now()
	var creds mediaauth.Credentials
	stale := false
	if authz, ok := req.Header.Lookup("Authorization"); ok {
		var good bool
		creds, good, stale = a.verify(g.policy(), req.Method, req.URL, authz, now)
		if !good {
			a.s.refused(protoRTSP, mediaauth.ErrBadCredentials)
			return a.challenge(now, stale)
		}
	} else {
		creds = mediaauth.FromQuery(query)
	}
	check := func(action mediaauth.Action, stream string) *rtsp.Response {
		if _, err := g.allow(action, stream, creds); err != nil {
			return a.refuse(now, err)
		}
		return nil
	}
	// session is the stream of the connection's session, which a PLAY or
	// RECORD acts on; without one, fib answers them 454 and the URL's
	// stream is checked.
	session, hasSession := sc.SessionPath()
	switch req.Method {
	case rtsp.MethodDescribe:
		return check(mediaauth.Play, path)
	case rtsp.MethodAnnounce:
		return check(mediaauth.Publish, path)
	case rtsp.MethodPlay:
		if !hasSession {
			session = rtspStream(path)
		}
		return check(mediaauth.Play, session)
	case rtsp.MethodRecord:
		if !hasSession {
			session = rtspStream(path)
		}
		return check(mediaauth.Publish, session)
	}
	// SETUP. The first makes a session that plays the stream it names.
	if !hasSession {
		return check(mediaauth.Play, rtspStream(path))
	}
	// Later ones add a track to the session's stream, whatever they name:
	// a player's, which its first SETUP let in, or a publisher's, which its
	// ANNOUNCE let in. fib does not say which, and the PLAY or RECORD after
	// them is checked for what it does.
	_, err := g.allow(mediaauth.Play, session, creds)
	if err == nil {
		return nil
	}
	_, perr := g.allow(mediaauth.Publish, session, creds)
	switch {
	case perr == nil:
		return nil
	case errors.Is(perr, errRetry):
		err = perr
	}
	return a.refuse(now, err)
}

// refuse answers a request the policy refused with err.
func (a *rtspAuth) refuse(now time.Time, err error) *rtsp.Response {
	a.s.refused(protoRTSP, err)
	switch mediaauth.Status(err) {
	case 401:
		return a.challenge(now, false)
	case 403:
		return &rtsp.Response{StatusCode: rtsp.StatusForbidden}
	}
	resp := &rtsp.Response{StatusCode: rtsp.StatusServiceUnavailable}
	resp.Header.Add("Retry-After", "1")
	return resp
}

// rtspPath returns the path of an RTSP URL and its credentials' query, as
// fib's RTSP server reads them: the path without the slashes around it,
// with a "/trackID=N" ending the query moved to it; and the query up to a
// '/', after which a client resolving a control against a URL with a query
// writes the control (rtsp://h/live/cam?token=T/trackID=0).
func rtspPath(raw string) (path, query string, ok bool) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "" && !strings.EqualFold(u.Scheme, "rtsp") && !strings.EqualFold(u.Scheme, "rtsps")) {
		return "", "", false
	}
	path, query = strings.Trim(u.Path, "/"), u.RawQuery
	if i := strings.LastIndexByte(query, '/'); i >= 0 {
		if _, n := splitTrackID(query[i:]); n >= 0 {
			path += query[i:]
		}
	}
	if i := strings.IndexByte(query, '/'); i >= 0 {
		query = query[:i]
	}
	return path, query, true
}

// rtspStream is the stream of a path a SETUP, PLAY or RECORD names: the
// path without the "trackID=N" segment fib gives its tracks as controls, as
// fib compares a PLAY's or RECORD's with its session's.
func rtspStream(path string) string {
	s, _ := splitTrackID(path)
	return s
}

// splitTrackID splits a path ending in a "trackID=N" segment into the
// stream's path and N, or reports -1 when it has none, as fib's RTSP
// server does.
func splitTrackID(path string) (string, int) {
	i := strings.LastIndexByte(path, '/')
	v, ok := strings.CutPrefix(path[i+1:], "trackID=")
	if !ok {
		return path, -1
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return path, -1
	}
	if i < 0 {
		return "", n
	}
	return path[:i], n
}

// challenge is a 401 with the challenges the listener offers.
func (a *rtspAuth) challenge(now time.Time, stale bool) *rtsp.Response {
	resp := &rtsp.Response{StatusCode: rtsp.StatusUnauthorized}
	if a.digest {
		v := `Digest realm="` + a.realm + `", nonce="` + a.nonce(now) + `", algorithm=MD5`
		if stale {
			v += ", stale=TRUE"
		}
		resp.Header.Add("WWW-Authenticate", v)
	}
	if a.basic {
		resp.Header.Add("WWW-Authenticate", `Basic realm="`+a.realm+`"`)
	}
	return resp
}

func (a *rtspAuth) nonce(now time.Time) string {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(now.Unix()))
	return hex.EncodeToString(b[:]) + a.mac(b[:])
}

func (a *rtspAuth) mac(b []byte) string {
	m := hmac.New(sha256.New, a.secret[:])
	m.Write(b)
	return hex.EncodeToString(m.Sum(nil)[:12])
}

// nonceValid reports whether nonce is one of the listener's, and whether it
// is still fresh.
func (a *rtspAuth) nonceValid(nonce string, now time.Time) (ours, fresh bool) {
	if len(nonce) != 16+24 {
		return false, false
	}
	b, err := hex.DecodeString(nonce[:16])
	if err != nil || !hmac.Equal([]byte(a.mac(b)), []byte(nonce[16:])) {
		return false, false
	}
	at := time.Unix(int64(binary.BigEndian.Uint64(b)), 0)
	return true, now.Sub(at) <= rtspNonceLifetime && at.Sub(now) < time.Minute
}

// verify reads the Authorization value of a request for uri: Basic gives
// its user and password to the policy as they are; Digest is checked
// against the plaintext password of its user, and must be for uri, so that
// one captured is not replayed on another stream; it gives the user with
// it. good is false for credentials that are not right, or of a scheme not
// offered; stale for a Digest that would be right but for an expired nonce.
func (a *rtspAuth) verify(p *mediaauth.Policy, method, uri, authz string, now time.Time) (c mediaauth.Credentials, good, stale bool) {
	scheme, rest, _ := strings.Cut(strings.TrimSpace(authz), " ")
	switch {
	case strings.EqualFold(scheme, "Basic") && a.basic:
		b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(rest))
		if err != nil {
			return c, false, false
		}
		u, pw, found := strings.Cut(string(b), ":")
		if !found || u == "" {
			return c, false, false
		}
		return mediaauth.Credentials{User: u, Password: pw}, true, false
	case strings.EqualFold(scheme, "Digest") && a.digest:
		params := parseAuthParams(rest)
		user := params["username"]
		ours, fresh := a.nonceValid(params["nonce"], now)
		if !ours || params["realm"] != a.realm || user == "" || !sameURI(params["uri"], uri) {
			return c, false, false
		}
		if alg := params["algorithm"]; alg != "" && !strings.EqualFold(alg, "MD5") {
			return c, false, false
		}
		pw, known := p.Password(user)
		if !known {
			return c, false, false
		}
		want := digestResponse(user, a.realm, pw, method, params["uri"], params["nonce"], params["qop"], params["nc"], params["cnonce"])
		if subtle.ConstantTimeCompare([]byte(strings.ToLower(params["response"])), []byte(want)) != 1 {
			return c, false, false
		}
		if !fresh {
			return c, false, true
		}
		return mediaauth.Credentials{User: user, Password: pw}, true, false
	}
	return c, false, false
}

// sameURI reports whether the uri of Digest credentials names the
// Request-URI req: the same string, or the same path and query with one of
// them absolute (RFC 7616 section 3.4.6 has the server check it; a client
// may give either form), as fib's RTSP server has it.
func sameURI(uri, req string) bool {
	if uri == req {
		return true
	}
	if uri == "" {
		return false
	}
	return uriPathQuery(uri) == uriPathQuery(req)
}

// uriPathQuery is what follows the authority of an absolute rtsp, rtsps or
// rtspu URL, "/" if nothing does, and any other uri as it is.
func uriPathQuery(uri string) string {
	scheme, rest, ok := strings.Cut(uri, "://")
	if !ok || !strings.EqualFold(scheme, "rtsp") && !strings.EqualFold(scheme, "rtsps") && !strings.EqualFold(scheme, "rtspu") {
		return uri
	}
	if i := strings.IndexAny(rest, "/?"); i >= 0 {
		if rest[i] == '?' {
			return "/" + rest[i:]
		}
		return rest[i:]
	}
	return "/"
}

func md5Hex(s string) string {
	h := md5.Sum([]byte(s))
	return hex.EncodeToString(h[:])
}

// digestResponse computes the response of RFC 2617 section 3.2.2.1 with
// MD5: without qop as RFC 2069 has it, or with qop=auth.
func digestResponse(user, realm, password, method, uri, nonce, qop, nc, cnonce string) string {
	ha1 := md5Hex(user + ":" + realm + ":" + password)
	ha2 := md5Hex(method + ":" + uri)
	if qop == "" {
		return md5Hex(ha1 + ":" + nonce + ":" + ha2)
	}
	return md5Hex(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":" + qop + ":" + ha2)
}

// parseAuthParams parses the comma-separated name=value parameters of
// credentials, values quoted or not; at most 16 of them.
func parseAuthParams(s string) map[string]string {
	p := make(map[string]string)
	for s = strings.TrimSpace(s); s != "" && len(p) < 16; {
		eq := strings.IndexByte(s, '=')
		if eq < 0 {
			break
		}
		name := strings.ToLower(strings.TrimSpace(s[:eq]))
		s = strings.TrimSpace(s[eq+1:])
		var value string
		if strings.HasPrefix(s, `"`) {
			var b strings.Builder
			i := 1
			for ; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' && i+1 < len(s) {
					i++
				}
				b.WriteByte(s[i])
			}
			value = b.String()
			s = s[min(i+1, len(s)):]
		} else {
			end := strings.IndexByte(s, ',')
			if end < 0 {
				end = len(s)
			}
			value = strings.TrimSpace(s[:end])
			s = s[end:]
		}
		p[name] = value
		s = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(s), ","))
	}
	return p
}
