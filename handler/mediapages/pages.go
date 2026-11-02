// Package mediapages is the media_pages handler: fit's own pages for its
// media, embedded in the binary, and the API behind them. Under its prefix it
// serves an index, a room call over WHIP and WHEP, a publish page and a play
// page; a login against a media_auth policy's users, which issues a token
// signed by the policy; and a room API, through which the members of a call
// announce whether they publish and their microphone and camera are on, and
// learn the others' with Server-Sent Events. See
// docs/decisions/030-media-auth-and-pages.md.
//
// Nothing blocks a worker: the room API's state is in memory under short
// locks, its events are written with WriteNoWait and a slow browser is sent
// only the latest state once it has taken the last, and a bcrypt login runs
// on mediaauth's pool. Every part is bounded: rooms, members a room,
// watchers a room, the size of a request's body, a member's life without a
// heartbeat, and each wait for a browser to take its events.
package mediapages

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	stdhttp "net/http"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/mediaauth"
	"github.com/lesismal/fit/module"
)

func init() {
	module.Register(module.Info{ID: module.Handlers + ".media_pages", New: func() any { return new(Pages) },
		Doc: "Serves fit's media pages under a prefix: an index with a login, a room call, publish and play pages over WHIP and WHEP, and the room API through which the members of a call announce their microphone and camera."})
}

// The defaults of the params.
const (
	DefaultPrefix          = "/media"
	DefaultWHIP            = "/whip/{stream}"
	DefaultWHEP            = "/whep/{stream}"
	DefaultMaxRooms        = 64
	DefaultMaxMembers      = 16
	DefaultMaxMessageBytes = 1024
	DefaultMemberTTL       = 30 * time.Second
	DefaultStallTimeout    = 30 * time.Second
)

// Pages is the media_pages handler.
type Pages struct {
	Prefix          string             `json:"prefix,omitempty" default:"\"/media\"" doc:"The path the pages are served under, which the route must match with what is below it, as /media/* beside /media; a request outside it gets 404. /media when left out."`
	Title           string             `json:"title,omitempty" doc:"The pages' title; fit media when left out."`
	Auth            string             `json:"auth,omitempty" doc:"The media_auth policy the room API checks, and the pages' login and tokens belong to: joining a room and following it needs play on <room>-*, announcing that one publishes publish on <room>-<name>, as the WHIP and WHEP endpoints the pages use check with their own auth, which should name the same policy. Everyone may use the room API when left out, as the route's middleware allow."`
	Login           bool               `json:"login,omitempty" doc:"Show a login form for the users of the auth policy: a right password is answered with a token the policy signs, for its token_ttl, set as the HttpOnly cookie fit_media_token (SameSite=Strict, Secure over TLS), which the WHIP, WHEP and HLS requests of the pages carry, and handed to the page too. Without it the pages ask for a token instead, as fit media-token prints, when auth needs one."`
	WHIP            string             `json:"whip,omitempty" default:"\"/whip/{stream}\"" doc:"The URL streams are published to with WHIP, {stream} standing for the stream's name: a whip handler's, or stream_whip's for the streaming role, on this server or another. /whip/{stream} when left out."`
	WHEP            string             `json:"whep,omitempty" default:"\"/whep/{stream}\"" doc:"The URL streams are played from with WHEP, {stream} standing for the stream's name. /whep/{stream} when left out."`
	HLS             string             `json:"hls,omitempty" doc:"The URL of a stream's HLS playlist, {stream} standing for its name, as /hls/{stream}/index.m3u8 for the streaming role's hls handler: the play page plays it where the browser plays HLS itself, with the page's token. None when left out."`
	Links           []Link             `json:"links,omitempty" doc:"Further URLs the publish and play pages show for a stream, for tools outside the browser, as the streaming role's RTMP, RTSP, SRT and HTTP-FLV ones: {stream} stands for the stream's name, {token} for the page's token, {host} for the host the page was loaded from."`
	ICEServers      []config.ICEServer `json:"ice_servers,omitempty" doc:"STUN and TURN servers the pages' PeerConnections use, as a browser's iceServers. None when left out: fit's media server is an ICE lite agent that browsers reach directly, and tells them of its own ICE servers in its answers' Link headers."`
	MaxRooms        int                `json:"max_rooms,omitempty" min:"1" default:"64" doc:"How many rooms may have members or watchers at once; a join or a watch past it is answered 503. 64 when left out."`
	MaxMembers      int                `json:"max_members,omitempty" min:"1" max:"256" default:"16" doc:"How many members a room may have; a join past it is answered 503. A room may have twice as many watchers, its members' event streams among them. 16 when left out."`
	MaxMessageBytes int                `json:"max_message_bytes,omitempty" min:"256" max:"65536" default:"1024" doc:"The largest body a request of the room API or the login may have, in bytes; a larger one is answered 413. 1024 when left out."`
	MemberTTL       config.Duration    `json:"member_ttl,omitempty" default:"\"30s\"" doc:"How long a member stays in its room without a heartbeat: the pages send one every third of it. 30s when left out."`
	StallTimeout    config.Duration    `json:"stall_timeout,omitempty" default:"\"30s\"" doc:"How long a room's event stream may wait for its browser to take what it was sent before it is cut off. 30s when left out."`

	prefix   string
	auth     *mediaauth.Policy
	graph    module.Holder
	hub      *hub
	settings *settings
	assets   map[string]*asset
	conf     []byte // GET api/config's answer
}

// Link is a URL the pages show for a stream.
type Link struct {
	Name string `json:"name" fit:"required" doc:"What the URL is, as RTMP or SRT."`
	URL  string `json:"url" fit:"required" doc:"The URL, with {stream}, {token} and {host} filled in by the page."`
}

// settings are what the room API is bounded by, swapped whole with each
// config.
type settings struct {
	maxRooms, maxMembers, maxWatchers int
	maxMessage                        int
	memberTTL, stall                  time.Duration
}

// assets are the pages' files.
//
//go:embed web
var web embed.FS

// asset is a file of the pages, as it is answered.
type asset struct {
	resp fibhttp.Response
	etag string
}

// pathPrefix is what prefix may be: a path of plain segments.
var pathPrefix = regexp.MustCompile(`^(/[A-Za-z0-9._~-]+)+$`)

// Provision checks the params, finds the policy and builds the pages.
func (p *Pages) Provision(ctx *module.Context) error {
	p.prefix = p.Prefix
	if p.prefix == "" {
		p.prefix = DefaultPrefix
	}
	if !pathPrefix.MatchString(p.prefix) {
		return ctx.Errorf("prefix", "%q is not a path of segments of letters, digits, '.', '_', '~' and '-', as /media", p.Prefix)
	}
	if strings.ContainsAny(p.Title, "<>&\"'\r\n") || len(p.Title) > 80 {
		return ctx.Errorf("title", "a title is at most 80 characters, without <, >, &, quotes or line breaks")
	}
	whip, whep := orDefault(p.WHIP, DefaultWHIP), orDefault(p.WHEP, DefaultWHEP)
	origins := map[string]bool{}
	for _, f := range []struct{ field, v string }{{"whip", whip}, {"whep", whep}, {"hls", p.HLS}} {
		if f.v == "" {
			continue
		}
		o, err := endpoint(f.v)
		if err != nil {
			return ctx.Errorf(f.field, "%v", err)
		}
		if o != "" {
			origins[o] = true
		}
	}
	for i, l := range p.Links {
		if l.Name == "" || len(l.Name) > 40 || l.URL == "" || len(l.URL) > 512 || strings.ContainsAny(l.URL+l.Name, "\r\n\x00<>\"") {
			return ctx.Params.Field("links").Index(i).Errorf("a link has a name of at most 40 characters and a URL of at most 512, without line breaks, <, > or quotes")
		}
	}
	pol, err := mediaauth.Provision(ctx, "auth", p.Auth)
	if err != nil {
		return err
	}
	if p.Login && pol == nil {
		return ctx.Errorf("login", "a login needs auth, a media_auth policy with users")
	}
	if p.Login && len(ctx.MediaAuth(p.Auth).Users) == 0 {
		return ctx.Errorf("login", "the media_auth policy %q has no users to log in", p.Auth)
	}
	p.auth = pol
	p.graph = ctx.Graph()
	st := &settings{
		maxRooms:   orInt(p.MaxRooms, DefaultMaxRooms),
		maxMembers: orInt(p.MaxMembers, DefaultMaxMembers),
		maxMessage: orInt(p.MaxMessageBytes, DefaultMaxMessageBytes),
		memberTTL:  orDur(p.MemberTTL.Std(), DefaultMemberTTL),
		stall:      orDur(p.StallTimeout.Std(), DefaultStallTimeout),
	}
	st.maxWatchers = 2 * st.maxMembers
	if st.memberTTL < 3*time.Second {
		return ctx.Errorf("member_ttl", "%s is shorter than 3s", st.memberTTL)
	}
	p.settings = st
	p.hub = acquire(ctx, p.prefix, st)

	conf := map[string]any{
		"title":      orDefault(p.Title, "fit media"),
		"login":      p.Login,
		"auth":       pol != nil,
		"public":     map[string]bool{"publish": pol.Public(mediaauth.Publish), "play": pol.Public(mediaauth.Play)},
		"whip":       whip,
		"whep":       whep,
		"hls":        p.HLS,
		"links":      p.Links,
		"iceServers": iceServers(p.ICEServers),
		"room": map[string]any{
			"maxMembers":  st.maxMembers,
			"heartbeatMs": (st.memberTTL / 3).Milliseconds(),
			"maxMessage":  st.maxMessage,
		},
	}
	if p.conf, err = json.Marshal(conf); err != nil {
		return err
	}
	return p.buildAssets(slices.Sorted(mapsKeys(origins)))
}

// Cleanup lets go of the room hub, which ends with its last config.
func (p *Pages) Cleanup() { p.hub.release() }

// Drain ends the room event streams this config's requests still hold,
// once the config has been swapped out the drain timeout ago: the browsers
// follow their rooms again, under the config now running, which shares the
// rooms.
func (p *Pages) Drain() { p.hub.drain(p) }

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func orInt(n, d int) int {
	if n <= 0 {
		return d
	}
	return n
}

func orDur(v, d time.Duration) time.Duration {
	if v <= 0 {
		return d
	}
	return v
}

func mapsKeys(m map[string]bool) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// endpoint checks an endpoint's URL template, and returns its origin when it
// is on another server.
func endpoint(v string) (string, error) {
	if len(v) > 512 || !strings.Contains(v, "{stream}") || strings.ContainsAny(v, " \t\r\n\x00<>\"'") {
		return "", fmt.Errorf("%q is not a URL of at most 512 characters with {stream} in it", v)
	}
	if strings.HasPrefix(v, "/") && !strings.HasPrefix(v, "//") {
		return "", nil
	}
	u, err := url.Parse(strings.ReplaceAll(v, "{stream}", "s"))
	if err != nil || u.Host == "" || u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("%q is neither a path nor an http or https URL", v)
	}
	return u.Scheme + "://" + u.Host, nil
}

// iceServers is the page's RTCConfiguration.iceServers.
func iceServers(list []config.ICEServer) []map[string]any {
	out := make([]map[string]any, 0, len(list))
	for _, s := range list {
		m := map[string]any{"urls": s.URLs}
		if s.Username != "" {
			m["username"] = s.Username
		}
		if s.Credential != "" {
			m["credential"] = s.Credential
		}
		out = append(out, m)
	}
	return out
}

// buildAssets makes the pages' answers: the index, with the prefix and the
// title in it, and its script and style, each with an ETag and a Content
// Security Policy that lets them reach the endpoints' origins and nothing
// else.
func (p *Pages) buildAssets(origins []string) error {
	extra := ""
	if len(origins) > 0 {
		extra = " " + strings.Join(origins, " ")
	}
	csp := "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: blob:; media-src 'self' blob:" + extra +
		"; connect-src 'self'" + extra + "; object-src 'none'; base-uri 'self'; frame-ancestors 'none'; form-action 'self'"
	p.assets = map[string]*asset{}
	add := func(name, ctype string, body []byte) {
		sum := sha256.Sum256(body)
		etag := `"` + base64.RawURLEncoding.EncodeToString(sum[:12]) + `"`
		hdr := stdhttp.Header{
			"Content-Type":           {ctype},
			"Cache-Control":          {"no-cache"},
			"Etag":                   {etag},
			"X-Content-Type-Options": {"nosniff"},
			"Referrer-Policy":        {"no-referrer"},
		}
		if strings.HasPrefix(ctype, "text/html") {
			hdr["Content-Security-Policy"] = []string{csp}
			hdr["X-Frame-Options"] = []string{"DENY"}
			hdr["Permissions-Policy"] = []string{"camera=(self), microphone=(self), display-capture=(self)"}
		}
		p.assets[name] = &asset{etag: etag, resp: fibhttp.Response{StatusCode: stdhttp.StatusOK, Header: hdr, Body: body}}
	}
	files, err := fs.Sub(web, "web")
	if err != nil {
		return err
	}
	index, err := fs.ReadFile(files, "index.html")
	if err != nil {
		return err
	}
	index = bytes.ReplaceAll(index, []byte("{{base}}"), []byte(p.prefix+"/"))
	index = bytes.ReplaceAll(index, []byte("{{title}}"), []byte(orDefault(p.Title, "fit media")))
	add("index.html", "text/html; charset=utf-8", index)
	for _, f := range []struct{ name, ctype string }{{"app.js", "text/javascript; charset=utf-8"}, {"app.css", "text/css; charset=utf-8"}} {
		b, err := fs.ReadFile(files, f.name)
		if err != nil {
			return err
		}
		add(f.name, f.ctype, b)
	}
	return nil
}

// ServeHTTP serves the pages and their API.
func (p *Pages) ServeHTTP(c *fibhttp.Context) {
	path := c.Request.URL.Path
	if path == p.prefix {
		q := ""
		if c.Request.URL.RawQuery != "" {
			q = "?" + c.Request.URL.RawQuery
		}
		_ = c.WriteResponse(fibhttp.Response{StatusCode: stdhttp.StatusPermanentRedirect,
			Header: stdhttp.Header{"Location": {p.prefix + "/" + q}}})
		return
	}
	rest, ok := strings.CutPrefix(path, p.prefix+"/")
	if !ok {
		plain(c, stdhttp.StatusNotFound, "not found", nil)
		return
	}
	if api, ok := strings.CutPrefix(rest, "api/"); ok {
		p.api(c, api)
		return
	}
	name := rest
	switch rest {
	case "", "call", "publish", "play":
		name = "index.html"
	}
	a := p.assets[name]
	if a == nil {
		plain(c, stdhttp.StatusNotFound, "not found", nil)
		return
	}
	if m := c.Request.Method; m != stdhttp.MethodGet && m != stdhttp.MethodHead {
		plain(c, stdhttp.StatusMethodNotAllowed, "the pages take GET", stdhttp.Header{"Allow": {"GET, HEAD"}})
		return
	}
	if inm := c.Request.Header.Get("If-None-Match"); inm != "" && strings.Contains(inm, a.etag) {
		_ = c.WriteResponse(fibhttp.Response{StatusCode: stdhttp.StatusNotModified,
			Header: stdhttp.Header{"Etag": {a.etag}, "Cache-Control": {"no-cache"}}})
		return
	}
	_ = c.WriteResponse(a.resp)
}

// api serves the API below prefix/api/.
func (p *Pages) api(c *fibhttp.Context, rest string) {
	method := c.Request.Method
	switch {
	case rest == "config":
		if method != stdhttp.MethodGet {
			plain(c, stdhttp.StatusMethodNotAllowed, "config takes GET", stdhttp.Header{"Allow": {"GET"}})
			return
		}
		writeJSONBytes(c, stdhttp.StatusOK, p.conf, nil)
	case rest == "login":
		if method != stdhttp.MethodPost {
			plain(c, stdhttp.StatusMethodNotAllowed, "login takes POST", stdhttp.Header{"Allow": {"POST"}})
			return
		}
		p.login(c)
	case rest == "logout":
		if method != stdhttp.MethodPost {
			plain(c, stdhttp.StatusMethodNotAllowed, "logout takes POST", stdhttp.Header{"Allow": {"POST"}})
			return
		}
		writeJSON(c, stdhttp.StatusOK, map[string]any{}, stdhttp.Header{"Set-Cookie": {p.cookie(c, "", -1)}})
	case strings.HasPrefix(rest, "rooms/"):
		p.rooms(c, strings.TrimPrefix(rest, "rooms/"))
	default:
		plain(c, stdhttp.StatusNotFound, "no such API", nil)
	}
}

// login checks a user's password against the policy, and answers with a
// token the policy signs, set as a cookie too.
func (p *Pages) login(c *fibhttp.Context) {
	if !p.Login {
		plain(c, stdhttp.StatusNotFound, "the pages have no login", nil)
		return
	}
	readBody(c, p.settings.maxMessage, func(body []byte) {
		var in struct {
			User     string `json:"user"`
			Password string `json:"password"`
		}
		if err := json.Unmarshal(body, &in); err != nil || in.User == "" || len(in.User) > 128 || len(in.Password) > 1024 {
			plain(c, stdhttp.StatusBadRequest, "the body must be JSON: {\"user\", \"password\"}", nil)
			return
		}
		var result struct {
			subject string
			grant   mediaauth.Grant
			err     error
		}
		answer := func() {
			if c.Err() != nil {
				return
			}
			if result.err != nil {
				mediaauth.WriteError(c, result.err)
				return
			}
			tok, exp, err := p.auth.Issue(result.subject, result.grant, 0)
			if err != nil {
				plain(c, stdhttp.StatusInternalServerError, err.Error(), nil)
				return
			}
			maxAge := int(time.Until(exp).Seconds())
			writeJSON(c, stdhttp.StatusOK, map[string]any{
				"token": tok, "expires": exp.Unix(), "subject": result.subject,
				"publish": nonNil(result.grant.Publish), "play": nonNil(result.grant.Play),
			}, stdhttp.Header{"Set-Cookie": {p.cookie(c, tok, maxAge)}})
		}
		run := c.Async(answer)
		p.auth.Login(in.User, in.Password, func(subject string, g mediaauth.Grant, err error) {
			result.subject, result.grant, result.err = subject, g, err
			run()
		})
	})
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// cookie is the Set-Cookie of a token: HttpOnly, SameSite=Strict, Secure
// over TLS, for the whole site, as the endpoints are on other paths. A
// negative maxAge removes it.
func (p *Pages) cookie(c *fibhttp.Context, tok string, maxAge int) string {
	ck := &stdhttp.Cookie{Name: mediaauth.CookieName, Value: tok, Path: "/", MaxAge: maxAge,
		HttpOnly: true, SameSite: stdhttp.SameSiteStrictMode, Secure: c.Request.TLS != nil}
	return ck.String()
}

// readBody gathers a request's body, up to limit bytes, and calls done with
// it; a larger one is answered 413.
func readBody(c *fibhttp.Context, limit int, done func([]byte)) {
	if c.Request.ContentLength > int64(limit) {
		plain(c, stdhttp.StatusRequestEntityTooLarge, "the body is too large", nil)
		return
	}
	var buf []byte
	finished := false
	c.OnBody(func(data []byte, fin bool, err error) {
		if finished {
			return
		}
		if err != nil {
			finished = true
			return
		}
		if len(buf)+len(data) > limit {
			finished = true
			plain(c, stdhttp.StatusRequestEntityTooLarge, "the body is too large", nil)
			return
		}
		buf = append(buf, data...)
		if fin {
			finished = true
			done(buf)
		}
	})
}

// plain answers with a status and a plain text message.
func plain(c *fibhttp.Context, status int, msg string, hdr stdhttp.Header) {
	if hdr == nil {
		hdr = stdhttp.Header{}
	}
	hdr.Set("Content-Type", "text/plain; charset=utf-8")
	hdr.Set("Cache-Control", "no-store")
	_ = c.WriteResponse(fibhttp.Response{StatusCode: status, Header: hdr, Body: []byte(msg + "\n")})
}

func writeJSON(c *fibhttp.Context, status int, v any, hdr stdhttp.Header) {
	b, err := json.Marshal(v)
	if err != nil {
		plain(c, stdhttp.StatusInternalServerError, err.Error(), nil)
		return
	}
	writeJSONBytes(c, status, b, hdr)
}

func writeJSONBytes(c *fibhttp.Context, status int, b []byte, hdr stdhttp.Header) {
	if hdr == nil {
		hdr = stdhttp.Header{}
	}
	hdr.Set("Content-Type", "application/json")
	hdr.Set("Cache-Control", "no-store")
	_ = c.WriteResponse(fibhttp.Response{StatusCode: status, Header: hdr, Body: b})
}

var (
	_ module.Cleaner = (*Pages)(nil)
	_ module.Drainer = (*Pages)(nil)
)
