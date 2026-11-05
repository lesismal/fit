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
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/lesismal/fib"
	"github.com/lesismal/fib/media/rtsp"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/mediaauth"
)

// rtspNonceLifetime is how long a Digest nonce is good for; a client with
// an older one is told it is stale and retries with a new one.
const rtspNonceLifetime = 5 * time.Minute

// rtspAuth checks RTSP requests against the listener's policy, in the RTSP
// server's OnRequest, which fib calls on the connection's worker: DESCRIBE
// needs play on the stream, ANNOUNCE publish. What a session plays or
// records is not the URL of its PLAY or RECORD, nor always of its SETUP:
// fib's server keeps one session on a connection, made by the first SETUP
// for the stream that SETUP's URL names (its track control taken off),
// whether or not a DESCRIBE described it, or by ANNOUNCE; PLAY and RECORD
// then act on that session, whatever their URLs say. OnRequest cannot see
// the session, so its stream is checked where fib hands it to the hub, by
// the path fib has: the hub's OnSubscribe at DESCRIBE, at a SETUP of a
// stream no DESCRIBE on the connection described, and at PLAY, and its
// OnPublish at RECORD (Server.rtspAllow). Those hooks run on the
// connection's worker within the request, after OnRequest, which leaves
// them the request's credentials under the connection's address
// (Server.rtspReqs).
//
// Credentials are the Authorization header's, Digest (RFC 2617 with MD5,
// as RTSP 1.0 clients speak it, its uri the request's) or Basic, or else a
// token, or user and pass, in the URL's query. Its nonces carry their time
// and a MAC of it, so that any connection checks any nonce with nothing
// kept.
type rtspAuth struct {
	s      *Server
	realm  string
	digest bool
	basic  bool
	secret [32]byte
}

func newRTSPAuth(c *config.StreamRTSP) (*rtspAuth, error) {
	a := &rtspAuth{realm: c.RealmName()}
	for _, m := range c.Methods() {
		switch m {
		case "digest":
			a.digest = true
		case "basic":
			a.basic = true
		}
	}
	if _, err := rand.Read(a.secret[:]); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *rtspAuth) onRequest(sc *rtsp.ServerConn, req *rtsp.Request) *rtsp.Response {
	var action mediaauth.Action
	switch req.Method {
	case rtsp.MethodDescribe:
		action = mediaauth.Play
	case rtsp.MethodAnnounce:
		action = mediaauth.Publish
	case rtsp.MethodSetup, rtsp.MethodPlay, rtsp.MethodRecord:
		// The hub's hooks check the session's stream.
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
	if authz, ok := req.Header.Lookup("Authorization"); ok {
		var good, stale bool
		creds, good, stale = a.verify(g.policy(), req.Method, req.URL, authz, now)
		if !good {
			a.s.refused(protoRTSP, mediaauth.ErrBadCredentials)
			return a.challenge(now, stale)
		}
	} else {
		creds = mediaauth.FromQuery(query)
	}
	// Every request leaves its own credentials, none included, so that no
	// request is let in on an earlier one's.
	c := sc.Conn()
	a.s.rtspReqs.Store(c.RemoteAddrPort(), &rtspRequest{c: c, creds: creds})
	if action == 0 {
		return nil
	}
	_, err := g.allow(action, path, creds)
	if err == nil {
		return nil
	}
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

// rtspRequest is the credentials of the request an RTSP connection is
// being served, for the hub's hooks.
type rtspRequest struct {
	c     *fib.Connection
	creds mediaauth.Credentials
}

// rtspAllow is the hub's check of the stream an RTSP session plays or
// records, by its path as fib's server has it, with the credentials the
// request being served on the client's connection, from remote, gave
// OnRequest: none when it gave none. fib answers a refusal with 403, or at
// PLAY, whose subscription starts once its response has gone, by closing
// the connection.
func (s *Server) rtspAllow(a mediaauth.Action, path string, remote netip.AddrPort) error {
	g := s.auth[protoRTSP].Load()
	if g == nil || g.name == "" {
		return nil
	}
	var creds mediaauth.Credentials
	if r, ok := s.rtspReqs.Load(remote); ok {
		creds = r.(*rtspRequest).creds
	}
	if _, err := g.allow(a, path, creds); err != nil {
		s.refused(protoRTSP, err)
		return err
	}
	return nil
}

// rtspHandler is the RTSP server, forgetting a connection's credentials as
// it closes.
type rtspHandler struct {
	*rtsp.Server
	s *Server
}

func (h rtspHandler) OnClose(c *fib.Connection, err error) {
	h.Server.OnClose(c, err)
	key := c.RemoteAddrPort()
	if r, ok := h.s.rtspReqs.Load(key); ok && r.(*rtspRequest).c == c {
		h.s.rtspReqs.CompareAndDelete(key, r)
	}
}

// rtspPath returns the stream's path of an RTSP URL and its query: the
// path without the slashes around it, and the query up to a '/', after
// which a client resolving a control against a URL with a query writes
// the control (rtsp://h/live/cam?token=T/trackID=0).
func rtspPath(raw string) (path, query string, ok bool) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "" && !strings.EqualFold(u.Scheme, "rtsp") && !strings.EqualFold(u.Scheme, "rtsps")) {
		return "", "", false
	}
	query = u.RawQuery
	if i := strings.IndexByte(query, '/'); i >= 0 {
		query = query[:i]
	}
	return strings.Trim(u.Path, "/"), query, true
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

// verify reads an Authorization value: Basic gives its user and password
// to the policy as they are; Digest is checked against the plaintext
// password of its user, its uri the request's as RFC 2617 has it, so that
// a response computed for one URL is not taken for another's, and gives
// the user with it. good is false for
// credentials that are not right, or of a scheme not offered; stale for a
// Digest that would be right but for an expired nonce.
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
		if !ours || params["realm"] != a.realm || user == "" || params["uri"] != uri {
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
