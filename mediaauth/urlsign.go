package mediaauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	stdhttp "net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/lesismal/fit/config"
)

// Playback URLs that expire (url_signing, I135). The site's API, or
// media_tokens' /urls for it, signs a URL for one viewer and one resource:
// the path prefix of an item of vod, /vod/<library>/<id>/, or of a stream,
// as /hls/live/cam/. The URL carries its grant in its query,
//
//	?exp=<unix seconds>&kid=<key>&ip=<address or network>&sid=<session>&sub=<subject>&sig=<MAC>
//
// kid, ip and sid when there are, and sig, the first 16 bytes of an
// HMAC-SHA256 over the resource and the fields, in base64url, with a key
// derived from the policy's secret or the keyring's key kid. A handler
// checks it against its own resource, on the worker, with no callback and
// no allocation but for the access log's redaction.
//
// A playlist fetched with a URL hands what it names a grant of its own, the
// same fields and g=1, that lives play_grace past the later of the URL's
// expiry and the item's end: in the query of every URI, or as the cookie
// GrantCookie on the resource's path. A playlist fetched with that grant
// hands it on unchanged, so it is never renewed.

// The query parameters of a signed URL and of a grant.
const (
	ParamExpiry    = "exp"
	ParamKey       = "kid"
	ParamAddress   = "ip"
	ParamSession   = "sid"
	ParamSubject   = "sub"
	ParamGrant     = "g"
	ParamSignature = "sig"
)

// GrantCookie is the cookie a playlist hands its grant on in, with
// url_signing's cookie.
const GrantCookie = "fit_media_grant"

// The bounds of a grant's fields.
const (
	maxSubjectBytes  = 128
	MaxSessionBytes  = 64
	maxResourceBytes = 1024
	sigBytes         = 16
	sigChars         = 22 // base64url of sigBytes, unpadded
)

// URLError is a signed URL, or a grant, refused: Status gives it 403.
// Reason says why, as URLReasons lists.
type URLError struct{ Reason string }

func (e *URLError) Error() string { return "mediaauth: the playback URL is refused: " + e.Reason }

// Is makes a URLError ErrForbidden to errors.Is.
func (e *URLError) Is(target error) bool { return target == ErrForbidden }

// The reasons a URL is refused, in the order URLRefusals counts them.
const (
	reasonExpired = iota
	reasonSignature
	reasonAddress
	reasonReferer
	reasonOrigin
	reasonSessionLimit
	reasonSessionEnded
	numReasons
)

// URLReasons are the reasons a URL is refused: expired; signature (another
// resource, a field changed, a key rotated or removed, or a grant that does
// not parse); address (another viewer's address); referer and origin (not
// in the lists); session_limit (a new session past max_sessions); and
// session_ended (a session on_limit ended).
var URLReasons = [numReasons]string{"expired", "signature", "address", "referer", "origin", "session_limit", "session_ended"}

var urlErrors = func() (e [numReasons]*URLError) {
	for i, r := range URLReasons {
		e[i] = &URLError{Reason: r}
	}
	return
}()

// ErrSessionsFull is a new session past max_tracked_sessions: 503.
var ErrSessionsFull = fmt.Errorf("%w: the policy tracks as many playback sessions as it may", ErrBusy)

// ErrNoURLSigning is a URL asked of a policy without url_signing.
var ErrNoURLSigning = errors.New("mediaauth: the policy has no url_signing")

// urlKey is the key URLs are signed with, derived from a signing key: an
// HMAC's padded keys, so that a MAC takes two SHA-256 sums on the stack.
type urlKey struct{ ipad, opad [64]byte }

func newURLKey(signKey []byte) *urlKey {
	m := hmac.New(sha256.New, signKey)
	m.Write([]byte("fit media url\x00"))
	k := m.Sum(nil)
	u := &urlKey{}
	for i := range u.ipad {
		var b byte
		if i < len(k) {
			b = k[i]
		}
		u.ipad[i], u.opad[i] = b^0x36, b^0x5c
	}
	return u
}

// urlConfig is a policy's url_signing, compiled.
type urlConfig struct {
	ttl, grace, idle time.Duration
	subnet, cookie   bool
	maxSessions      int
	endOldest        bool
	tracked          int
	referers         []string
	origins          []string
	trusted          []netip.Prefix
}

func compileURL(u *config.URLSigning) (*urlConfig, error) {
	c := &urlConfig{ttl: u.URLTTL(), grace: u.Grace(), idle: u.Idle(), subnet: u.BindAddress == "subnet",
		cookie: u.Cookie, maxSessions: u.MaxSessions, endOldest: u.OnLimit == "end_oldest", tracked: u.Tracked()}
	if u.MaxSessions < 0 {
		return nil, errors.New("url_signing.max_sessions: below 0")
	}
	for _, list := range []struct {
		name string
		in   []string
		out  *[]string
	}{{"referers", u.Referers, &c.referers}, {"origins", u.Origins, &c.origins}} {
		for _, o := range list.in {
			if !config.ValidOriginPattern(o) {
				return nil, fmt.Errorf("url_signing.%s: %q is not an origin", list.name, o)
			}
			*list.out = append(*list.out, strings.ToLower(o))
		}
	}
	for _, s := range u.TrustedProxies {
		p, ok := config.ParseAddrOrPrefix(s)
		if !ok {
			return nil, fmt.Errorf("url_signing.trusted_proxies: %q is not an IP address or a network", s)
		}
		c.trusted = append(c.trusted, p)
	}
	return c, nil
}

// grant is the fields of a signed URL or of a grant, as its query has them.
type grant struct {
	exp   int64
	kid   string
	ip    string // an address, or a network, as signed
	sid   string
	sub   string
	child bool
	sig   string
}

// appendMessage appends what g's MAC is taken over, for resource.
func (g *grant) appendMessage(b []byte, resource string) []byte {
	b = append(b, "fit-url-1\n"...)
	b = append(b, resource...)
	b = append(b, '\n')
	b = strconv.AppendInt(b, g.exp, 10)
	for _, f := range [...]string{g.kid, g.ip, g.sid, g.sub} {
		b = append(b, '\n')
		b = append(b, f...)
	}
	if g.child {
		return append(b, "\n1"...)
	}
	return append(b, "\n0"...)
}

// mac returns g's MAC for resource with k, in base64url.
func (k *urlKey) mac(g *grant, resource string) [sigChars]byte {
	var buf [512]byte
	b := append(buf[:0], k.ipad[:]...)
	b = g.appendMessage(b, resource)
	inner := sha256.Sum256(b)
	var ob [64 + sha256.Size]byte
	copy(ob[:64], k.opad[:])
	copy(ob[64:], inner[:])
	outer := sha256.Sum256(ob[:])
	var out [sigChars]byte
	base64.RawURLEncoding.Encode(out[:], outer[:sigBytes])
	return out
}

// appendQuery appends g as a query, without a leading '?'.
func (g *grant) appendQuery(b []byte) []byte {
	b = append(b, ParamExpiry+"="...)
	b = strconv.AppendInt(b, g.exp, 10)
	if g.kid != "" {
		b = append(b, "&"+ParamKey+"="...)
		b = append(b, g.kid...)
	}
	if g.ip != "" {
		b = append(b, "&"+ParamAddress+"="...)
		b = append(b, g.ip...)
	}
	if g.sid != "" {
		b = append(b, "&"+ParamSession+"="...)
		b = append(b, g.sid...)
	}
	b = append(b, "&"+ParamSubject+"="...)
	b = append(b, url.QueryEscape(g.sub)...)
	if g.child {
		b = append(b, "&"+ParamGrant+"=1"...)
	}
	b = append(b, "&"+ParamSignature+"="...)
	return append(b, g.sig...)
}

// parse reads a grant from q, a query or a cookie's value of the same form:
// false when it lacks a field, has one twice, or has one that is not as a
// grant's may be. Parameters of other names are left alone. It allocates
// only for a value with escapes.
func (g *grant) parse(q string) bool {
	var seen uint8
	for q != "" {
		var kv string
		kv, q, _ = strings.Cut(q, "&")
		k, v, _ := strings.Cut(kv, "=")
		var bit uint8
		switch k {
		case ParamExpiry:
			bit = 1
		case ParamKey:
			bit = 2
		case ParamAddress:
			bit = 4
		case ParamSession:
			bit = 8
		case ParamSubject:
			bit = 16
		case ParamGrant:
			bit = 32
		case ParamSignature:
			bit = 64
		default:
			continue
		}
		if seen&bit != 0 {
			return false
		}
		seen |= bit
		if strings.ContainsAny(v, "%+") {
			u, err := url.QueryUnescape(v)
			if err != nil {
				return false
			}
			v = u
		}
		switch bit {
		case 1:
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n <= 0 {
				return false
			}
			g.exp = n
		case 2:
			g.kid = v
		case 4:
			g.ip = v
		case 8:
			g.sid = v
		case 16:
			g.sub = v
		case 32:
			if v != "1" {
				return false
			}
			g.child = true
		case 64:
			g.sig = v
		}
	}
	return seen&(1|16|64) == 1|16|64 && validSubject(g.sub) && len(g.sig) == sigChars &&
		(g.sid == "" || ValidSession(g.sid)) && (g.kid == "" || validKeyName(g.kid)) && len(g.ip) <= 64
}

// validSubject reports whether s is a subject a grant may carry: 1 to 128
// bytes with no control character.
func validSubject(s string) bool {
	if s == "" || len(s) > maxSubjectBytes {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			return false
		}
	}
	return true
}

// ValidSession reports whether s is a playback session's ID: 1 to 64
// letters, digits, '.', '_', '~' and '-'.
func ValidSession(s string) bool { return len(s) <= MaxSessionBytes && validKeyName(s) }

// validKeyName reports whether s is 1 to 64 letters, digits, '.', '_', '~'
// and '-', as a key's name and a session's ID are.
func validKeyName(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '~' || c == '-') {
			return false
		}
	}
	return true
}

// ResourceOf returns the resource a URL of path is signed for: the path up
// to its last '/', as /vod/films/films/bbb/ for
// /vod/films/films/bbb/index.m3u8, or, for an HTTP-FLV stream, the path
// without .flv and with '/', as /live/cam/ for /live/cam.flv. It reports
// false for a path that is not one a URL may be signed for.
func ResourceOf(path string) (string, bool) {
	if len(path) > maxResourceBytes || !strings.HasPrefix(path, "/") || strings.HasSuffix(path, "/") {
		return "", false
	}
	var res string
	if s, ok := strings.CutSuffix(path, ".flv"); ok && !strings.HasSuffix(s, "/") {
		res = s + "/"
	} else {
		res = path[:strings.LastIndexByte(path, '/')+1]
	}
	return res, ValidResource(res) && validPathChars(path)
}

// ValidResource reports whether r is a resource a URL may be signed for:
// a path of at least one segment ending in '/', of plain segments, with no
// empty, . or .. segment.
func ValidResource(r string) bool {
	if len(r) < 3 || len(r) > maxResourceBytes || r[0] != '/' || r[len(r)-1] != '/' || !validPathChars(r) {
		return false
	}
	for seg := range strings.SplitSeq(r[1:len(r)-1], "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// validPathChars reports whether p holds only what a path may as it is:
// RFC 3986's unreserved characters, sub-delims, ':', '@' and '/'.
func validPathChars(p string) bool {
	for i := 0; i < len(p); i++ {
		c := p[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("-._~!$&'()*+,;=:@/", c) >= 0) {
			return false
		}
	}
	return true
}

// Viewer is who a URL is signed for.
type Viewer struct {
	// Subject is who the viewer is to the site: 1 to 128 bytes with no
	// control character. URLs carry it; use an opaque ID rather than a
	// name or an address.
	Subject string
	// Address binds the URL to the viewer's address, or to its network as
	// url_signing's bind_address says; the zero Addr binds it to none.
	Address netip.Addr
	// Session is the playback session max_sessions counts, as
	// ValidSession allows; "" for none.
	Session string
}

// URLSigning reports whether the policy has url_signing.
func (p *Policy) URLSigning() bool {
	st := p.state()
	return st != nil && st.url != nil
}

// URLTTL returns how long a URL is good for when its issuer names no
// lifetime: url_signing's ttl.
func (p *Policy) URLTTL() time.Duration {
	if st := p.state(); st != nil && st.url != nil {
		return st.url.ttl
	}
	return config.DefaultURLTTL
}

// SignURL returns the query of a URL of resource for v, good until expiry,
// in whole seconds: signed with the keyring's key named key, its first when
// key is "", or with the policy's secret. resource is as ResourceOf gives
// it; the URL is that resource, a name in it, '?' and the query.
func (p *Policy) SignURL(resource string, v Viewer, expiry time.Time, key string) (string, error) {
	st := p.state()
	switch {
	case st == nil:
		return "", ErrUnavailable
	case st.url == nil:
		return "", ErrNoURLSigning
	case !ValidResource(resource):
		return "", fmt.Errorf("mediaauth: %q is not a resource: a path of plain segments ending in /", resource)
	case !validSubject(v.Subject):
		return "", fmt.Errorf("mediaauth: %q is not a subject: 1 to %d bytes with no control characters", v.Subject, maxSubjectBytes)
	case v.Session != "" && !ValidSession(v.Session):
		return "", fmt.Errorf("mediaauth: %q is not a session: 1 to %d letters, digits, '.', '_', '~' and '-'", v.Session, MaxSessionBytes)
	}
	g := grant{exp: expiry.Unix(), sid: v.Session, sub: v.Subject}
	if g.exp <= 0 {
		return "", errors.New("mediaauth: the expiry is before 1970")
	}
	k := st.urlKey
	if len(st.keys) > 0 {
		i := 0
		if key != "" {
			i = st.keyIndex(key)
			if i < 0 {
				return "", fmt.Errorf("mediaauth: no key named %q", key)
			}
		}
		k, g.kid = st.keys[i].url, st.keys[i].name
	} else if key != "" {
		return "", errors.New("mediaauth: the policy has no keyring")
	}
	if v.Address.IsValid() {
		a := v.Address.Unmap().WithZone("")
		if st.url.subnet {
			bits := 24
			if a.Is6() {
				bits = 64
			}
			pfx, _ := a.Prefix(bits)
			g.ip = pfx.String()
		} else {
			g.ip = a.String()
		}
	}
	sig := k.mac(&g, resource)
	g.sig = string(sig[:])
	return string(g.appendQuery(make([]byte, 0, 160))), nil
}

// keyIndex returns the index of the keyring's key named name, or -1.
func (st *state) keyIndex(name string) int {
	for i := range st.keys {
		if st.keys[i].name == name {
			return i
		}
	}
	return -1
}

// verify checks g, of a request r, for resource at t: its key and MAC, its
// expiry, its address and r's Referer and Origin. It returns the key the
// grant was signed with, to sign the grant it hands on.
func (st *state) verify(r *stdhttp.Request, resource string, g *grant, t time.Time) (*urlKey, error) {
	k := st.urlKey
	if len(st.keys) > 0 {
		i := st.keyIndex(g.kid)
		if i < 0 {
			return nil, urlErrors[reasonSignature]
		}
		k = st.keys[i].url
	} else if g.kid != "" {
		return nil, urlErrors[reasonSignature]
	}
	want := k.mac(g, resource)
	if len(g.sig) != sigChars || subtle.ConstantTimeCompare(want[:], []byte(g.sig)) != 1 {
		return nil, urlErrors[reasonSignature]
	}
	if t.Unix() >= g.exp {
		return nil, urlErrors[reasonExpired]
	}
	if g.ip != "" && !boundTo(g.ip, clientIP(r, st.url.trusted)) {
		return nil, urlErrors[reasonAddress]
	}
	if len(st.url.referers) > 0 {
		if ref := headerValue(r, "Referer"); ref != "" && !matchOrigin(st.url.referers, originOf(ref)) {
			return nil, urlErrors[reasonReferer]
		}
	}
	if len(st.url.origins) > 0 {
		if o := headerValue(r, "Origin"); o != "" && !matchOrigin(st.url.origins, o) {
			return nil, urlErrors[reasonOrigin]
		}
	}
	return k, nil
}

func headerValue(r *stdhttp.Request, key string) string {
	if v := r.Header[key]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// boundTo reports whether a, the viewer's address, is the address or in
// the network bound, as a grant has it.
func boundTo(bound string, a netip.Addr) bool {
	if strings.IndexByte(bound, '/') >= 0 {
		p, err := netip.ParsePrefix(bound)
		return err == nil && p.Contains(a)
	}
	b, err := netip.ParseAddr(bound)
	return err == nil && b == a
}

// originOf returns the origin of a URL, scheme://host[:port], as a
// Referer's: what is before the first '/' after the scheme's "//".
func originOf(u string) string {
	i := strings.Index(u, "://")
	if i < 0 {
		return u
	}
	if j := strings.IndexByte(u[i+3:], '/'); j >= 0 {
		return u[:i+3+j]
	}
	return u
}

// matchOrigin reports whether origin matches one of the patterns, which
// are lower case; it is compared as it is, with no case folding past ASCII.
func matchOrigin(patterns []string, origin string) bool {
	var buf [256]byte
	if len(origin) > len(buf) {
		return false
	}
	lower := buf[:len(origin)]
	for i := 0; i < len(origin); i++ {
		c := origin[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		lower[i] = c
	}
	for _, p := range patterns {
		if matchBytes(p, lower) {
			return true
		}
	}
	return false
}

// matchBytes is Match over a byte slice.
func matchBytes(pattern string, s []byte) bool {
	p, i := 0, 0
	star, mark := -1, 0
	for i < len(s) {
		switch {
		case p < len(pattern) && pattern[p] == '*':
			star, mark = p, i
			p++
		case p < len(pattern) && pattern[p] == s[i]:
			p++
			i++
		case star >= 0:
			p = star + 1
			mark++
			i = mark
		default:
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

// clientIP returns the viewer's address: the connection's peer, or, when
// that is a trusted proxy, the last address in X-Forwarded-For that is not
// one. It does not allocate.
func clientIP(r *stdhttp.Request, trusted []netip.Prefix) netip.Addr {
	ip := peer(r.RemoteAddr)
	if len(trusted) == 0 || !isTrusted(ip, trusted) {
		return ip
	}
	xff := r.Header["X-Forwarded-For"]
	for i := len(xff) - 1; i >= 0; i-- {
		list := xff[i]
		for list != "" {
			j := strings.LastIndexByte(list, ',')
			item := strings.TrimSpace(list[j+1:])
			list = list[:max(j, 0)]
			a, err := netip.ParseAddr(item)
			if err != nil {
				return ip
			}
			if a = a.Unmap().WithZone(""); !isTrusted(a, trusted) {
				return a
			}
			ip = a
		}
	}
	return ip
}

func isTrusted(ip netip.Addr, trusted []netip.Prefix) bool {
	for _, p := range trusted {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// peer parses the address of a RemoteAddr, "ip:port" or a bare IP.
func peer(s string) netip.Addr {
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap().WithZone("")
	}
	a, _ := netip.ParseAddr(s)
	return a.Unmap().WithZone("")
}

// Access is what a request GuardResource lets in was let in with: who it
// is, and the signed URL or grant, if any, which the playlists it fetches
// hand on.
type Access struct {
	// Subject is who the request is authenticated as, "" when the policy
	// makes playing public or there is none.
	Subject string

	p        *Policy
	req      *stdhttp.Request
	resource string
	g        grant
	key      *urlKey
	byURL    bool
}

// URL reports whether the request was let in by a signed URL or a grant.
func (a *Access) URL() bool { return a.byURL }

// Expiry returns when the URL or grant the request was let in by expires,
// the zero Time when it was not.
func (a *Access) Expiry() time.Time {
	if !a.byURL {
		return time.Time{}
	}
	return time.Unix(a.g.exp, 0)
}

// PassOn returns what a playlist of the resource hands the URIs it names,
// for an item whose length is length, 0 for a live stream: the query each
// URI carries ("" for none), and the value of a Set-Cookie field to send
// with the playlist ("" for none). Let in by a signed URL, that is a grant
// of its own, which lives play_grace past the later of the URL's expiry
// and now and length, in the query, or, with url_signing's cookie, in the
// cookie; let in by a grant, the same grant; let in by a token in its
// query, that token. A playlist that hands on either is the viewer's own,
// not one a shared cache may keep.
func (a *Access) PassOn(length time.Duration) (query, setCookie string) {
	if !a.byURL {
		// The token of the query, whatever let the request in, as the
		// handlers have always passed it on.
		if a.req != nil && strings.Contains(a.req.URL.RawQuery, "token=") {
			if tok := a.req.URL.Query().Get("token"); tok != "" {
				return "token=" + url.QueryEscape(tok), ""
			}
		}
		return "", ""
	}
	st := a.p.state()
	if st == nil || st.url == nil {
		return "", ""
	}
	g := a.g
	if !g.child {
		t := now()
		g.exp = max(g.exp, t.Add(length).Unix()) + int64(st.url.grace/time.Second)
		g.child = true
		sig := a.key.mac(&g, a.resource)
		g.sig = string(sig[:])
		if g.sid != "" {
			a.p.sess.extend(g.sub, g.sid, g.exp)
		}
		if st.url.cookie {
			return "", a.cookie(&g, t)
		}
	} else if st.url.cookie {
		return "", ""
	}
	return string(g.appendQuery(make([]byte, 0, 160))), ""
}

// cookie returns the Set-Cookie value of grant g on the resource's path.
func (a *Access) cookie(g *grant, t time.Time) string {
	b := make([]byte, 0, 256)
	b = append(b, GrantCookie+"="...)
	b = g.appendQuery(b)
	b = append(b, "; Path="...)
	b = append(b, a.resource...)
	b = append(b, "; Max-Age="...)
	b = strconv.AppendInt(b, max(g.exp-t.Unix(), 1), 10)
	b = append(b, "; HttpOnly"...)
	if a.req != nil && a.req.TLS != nil {
		// A player on the site's own origin fetches with the cookie from
		// another site, which only None lets it send, and None needs Secure.
		b = append(b, "; Secure; SameSite=None"...)
	} else {
		b = append(b, "; SameSite=Lax"...)
	}
	return string(b)
}

// URLRefusals returns how many signed URLs and grants the policy has
// refused, by reason, as URLReasons names them.
func (p *Policy) URLRefusals() map[string]uint64 {
	out := make(map[string]uint64, numReasons)
	for i, r := range URLReasons {
		out[r] = p.refused[i].Load()
	}
	return out
}

// refusals counts a policy's refused URLs by reason.
type refusals [numReasons]atomic.Uint64

func (p *Policy) countRefusal(err error) {
	var ue *URLError
	if errors.As(err, &ue) {
		for i, e := range urlErrors {
			if e == ue {
				p.refused[i].Add(1)
				return
			}
		}
	}
}
