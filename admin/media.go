package admin

import (
	"cmp"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/lesismal/fit/assistant"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/handler/mediatokens"
	"github.com/lesismal/fit/handler/vod"
	"github.com/lesismal/fit/mediaauth"
	"github.com/lesismal/fit/medialib"
	"github.com/lesismal/fit/streaming/channels"
)

// The Admin API's endpoints for the workbench's media pages (I138):
//
//	POST /media/preview                     a short-lived URL of an item,
//	                                        {"library", "item"}, or of a
//	                                        channel, {"channel"}, signed for
//	                                        the console's user
//	GET  /media/auth                        every media_auth policy: its
//	                                        url_signing, keys (never their
//	                                        secrets), sessions, refusals and
//	                                        media_tokens routes
//	GET  /media/auth/{policy}/sessions      a page of its playback sessions:
//	                                        offset, limit, subject, resource
//	POST /media/auth/{policy}/sessions/end  end one: {"subject", "id"}
//	POST /media/auth/{policy}/rotate        rotate a key, {"key"}, every key,
//	                                        {"all": true}, or the single
//	                                        secret, {}, through its file
//	POST /media/auth/{policy}/urls          a test URL, as media_tokens'
//	                                        /urls signs one
//	GET  /media/stats                       the libraries, vod handlers,
//	                                        policies and channels, counted
//
// A URL these answer is the only secret they carry, short-lived, sent with
// Cache-Control: no-store and never logged.

// PreviewTTL is how long a preview's URL is good for.
const PreviewTTL = 2 * time.Minute

// The header fields the console's proxy sets on the requests it passes to
// the Admin API: the user logged in, and the host the browser addressed,
// for a preview's subject and the host of its URL. A caller with the
// Admin API's token may set them itself.
const (
	ConsoleUserHeader = assistant.ConsoleUserHeader
	ConsoleHostHeader = "Fit-Console-Host"
)

// The bounds of the media pages' answers.
const (
	defaultSessionsPage = 100
	maxSessionsPage     = 1000
	maxTestTTL          = time.Hour
	maxStatsResources   = 50
)

// mediaListener is a listener that serves HTTP, as a URL reaches it.
type mediaListener struct {
	name   string
	scheme string // http or https
	host   string // the host it listens on, "" for every address
	port   string // of the address it is bound to
}

// origin returns the origin of l's URLs: its own host when it listens on
// one, or else the host of consoleHost, the console's Host as the browser
// sent it, or else 127.0.0.1.
func (l *mediaListener) origin(consoleHost string) string {
	host := l.host
	if host == "" {
		host = hostOf(consoleHost)
	}
	if host == "" {
		host = "127.0.0.1"
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return l.scheme + "://" + host + ":" + l.port
}

// hostOf returns the host of hostport, a Host field as "example.com:9400",
// "[::1]:9400" or "10.0.0.1", without brackets; "" when it holds anything
// but letters, digits, '.', '-', ':', '[' and ']', so that nothing odd
// lands in a header or a URL.
func hostOf(hostport string) string {
	if hostport == "" || len(hostport) > 270 {
		return ""
	}
	for i := 0; i < len(hostport); i++ {
		c := hostport[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == ':' || c == '[' || c == ']') {
			return ""
		}
	}
	host := hostport
	switch {
	case strings.HasPrefix(host, "["):
		end := strings.IndexByte(host, ']')
		if end < 0 {
			return ""
		}
		host = host[1:end]
	case strings.Count(host, ":") == 1:
		host, _, _ = strings.Cut(host, ":")
	}
	if host == "" || strings.ContainsAny(host, "[]") || len(host) > 253 {
		return ""
	}
	if a, err := netip.ParseAddr(host); err == nil && a.IsUnspecified() {
		return ""
	}
	if strings.Contains(host, ":") {
		if _, err := netip.ParseAddr(host); err != nil {
			return ""
		}
	}
	return host
}

// mediaRoute is a route of the running config whose handler is vod or hls,
// with the params a URL of it needs.
type mediaRoute struct {
	name     string // its name, or #<index+1>
	module   string // vod or hls
	library  string
	prefix   string
	auth     string
	cors     string
	listener *mediaListener // nil when no listener serves HTTP for it
}

// tokenRoute is a media_tokens route of the running config.
type tokenRoute struct {
	name        string
	auth        string
	urlBase     string
	urlPrefixes []string
	maxTTL      time.Duration
}

// mediaView is what the media pages read of the running config and
// listeners.
type mediaView struct {
	cfg       *config.Config
	listeners []*mediaListener
	routes    []mediaRoute
	tokens    []tokenRoute
}

// routeName is a route's name, or its place among the routes, #1 first.
func routeName(r *config.Route, i int) string {
	if r.Name != "" {
		return r.Name
	}
	return "#" + strconv.Itoa(i+1)
}

// running returns the running config, an empty one when fit runs none.
func (cs *Configs) running() *config.Config {
	if c := cs.CurrentConfig(); c != nil {
		return c
	}
	return &config.Config{}
}

// mediaView reads the running config, its listeners and the addresses they
// are bound to. It waits for a config being applied: call it off fib's
// workers.
func (cs *Configs) mediaView() *mediaView {
	v := &mediaView{cfg: cs.running()}
	ports := map[string]string{}
	for _, a := range cs.server.Addrs() {
		if _, port, err := net.SplitHostPort(a.Addr.String()); err == nil {
			ports[a.Name] = port
		}
	}
	if g := cs.rt.Current(); g != nil {
		for _, l := range g.Listeners {
			if l.RedirectHTTPS != 0 || len(l.Protocols) > 0 && !slices.ContainsFunc(l.Protocols, func(p string) bool {
				return p == "http/1.1" || p == "h2" || p == "h2c"
			}) {
				continue
			}
			// The config checked the address: it splits.
			host, port, _ := net.SplitHostPort(l.Address)
			if p := ports[l.Name]; p != "" {
				port = p
			}
			ml := &mediaListener{name: l.Name, scheme: "http", host: hostOf(host), port: port}
			if l.Certificates != nil {
				ml.scheme = "https"
			}
			v.listeners = append(v.listeners, ml)
		}
		g.Release()
	}
	for i := range v.cfg.Routes {
		r := &v.cfg.Routes[i]
		// The params were checked when the config was compiled: they
		// decode.
		var mr mediaRoute
		switch r.Handler.Module {
		case "vod":
			p := new(vod.VOD)
			_ = r.Handler.Params.Decode(p)
			mr = mediaRoute{library: p.Library, prefix: p.Prefix, auth: p.Auth, cors: p.CORS}
			if mr.prefix == "" {
				mr.prefix = "/vod/" + p.Library + "/"
			}
		case "hls":
			// Not streaming.HLS: package streaming's tests use this one.
			var p struct{ Prefix, Auth, CORS string }
			data, _ := r.Handler.Params.MarshalJSON()
			_ = json.Unmarshal(data, &p)
			mr = mediaRoute{prefix: cmp.Or(p.Prefix, "/"), auth: p.Auth, cors: p.CORS}
		case "media_tokens":
			p := new(mediatokens.Tokens)
			_ = r.Handler.Params.Decode(p)
			tr := tokenRoute{name: routeName(r, i), auth: p.Auth, urlBase: strings.TrimRight(p.URLBase, "/"),
				urlPrefixes: p.URLPrefixes, maxTTL: p.MaxTTL.Std()}
			if tr.urlPrefixes == nil {
				tr.urlPrefixes = []string{}
			}
			if tr.maxTTL <= 0 {
				tr.maxTTL = mediatokens.DefaultMaxTTL
			}
			v.tokens = append(v.tokens, tr)
			continue
		default:
			continue
		}
		mr.name, mr.module = routeName(r, i), r.Handler.Module
		if !strings.HasSuffix(mr.prefix, "/") {
			mr.prefix += "/"
		}
		mr.listener = v.listenerFor(r.Listeners)
		v.routes = append(v.routes, mr)
	}
	return v
}

// listenerFor returns the listener a route of names serves URLs on: the
// first it names that serves HTTP, or, naming none, the first listener
// that does; nil when there is none.
func (v *mediaView) listenerFor(names []string) *mediaListener {
	for _, l := range v.listeners {
		if len(names) == 0 || l.name == names[0] {
			return l
		}
	}
	return nil
}

// route returns the first route of module serving library ("" for hls).
func (v *mediaView) route(module, library string) *mediaRoute {
	for i := range v.routes {
		if r := &v.routes[i]; r.module == module && (module != "vod" || r.library == library) {
			return r
		}
	}
	return nil
}

// routeOf returns the vod or hls route whose prefix is the longest that
// starts p, nil when none does.
func (v *mediaView) routeOf(p string) *mediaRoute {
	var best *mediaRoute
	for i := range v.routes {
		r := &v.routes[i]
		if strings.HasPrefix(p, r.prefix) && (best == nil || len(r.prefix) > len(best.prefix)) {
			best = r
		}
	}
	return best
}

// mediaOrigins is what the console's Content-Security-Policy adds for the
// media routes: the listeners that serve vod or hls routes, as of the
// graph version.
type mediaOrigins struct {
	version   uint64
	listeners []*mediaListener
}

// graphVersion returns the running graph's version, 0 for none, without
// waiting.
func (cs *Configs) graphVersion() uint64 {
	g := cs.rt.Current()
	if g == nil {
		return 0
	}
	v := g.Version
	g.Release()
	return v
}

// loadOrigins reads the media routes' listeners of the running config. It
// waits for a config being applied.
func (cs *Configs) loadOrigins() *mediaOrigins {
	o := &mediaOrigins{version: cs.graphVersion()}
	for _, r := range cs.mediaView().routes {
		if r.listener != nil && !slices.Contains(o.listeners, r.listener) {
			o.listeners = append(o.listeners, r.listener)
		}
	}
	return o
}

// sources is the console's Options.Sources: the origins of the listeners
// serving vod and hls routes, for the console's Host. It runs on a fib
// worker and never waits: the listeners are read when the console starts,
// and again on a goroutine of their own, one at a time, once the running
// graph is found changed; the responses until then have those read before.
func (cs *Console) sources(r *http.Request) []string {
	cur := cs.origins.Load()
	if cur.version != cs.configs.graphVersion() && cs.reading.CompareAndSwap(false, true) {
		go func() {
			defer cs.reading.Store(false)
			cs.origins.Store(cs.configs.loadOrigins())
		}()
	}
	out := make([]string, 0, len(cur.listeners))
	for _, l := range cur.listeners {
		if o := l.origin(r.Host); !slices.Contains(out, o) {
			out = append(out, o)
		}
	}
	return out
}

// newSession returns a new playback session's ID, as media_tokens makes
// one: 12 random bytes, raw URL base64.
func newSession() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// validSubject reports whether s may be a URL's or a token's subject: 1
// to 128 bytes with no control characters.
func validSubject(s string) bool {
	return s != "" && len(s) <= 128 && !strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

// readJSON reads a request's JSON body into v; an empty body leaves v as
// it is. It answers the request itself when it cannot.
func (s *Server) readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	data, ok := s.readBody(w, r, "application/json")
	if !ok {
		return false
	}
	if len(data) == 0 {
		return true
	}
	if err := json.Unmarshal(data, v); err != nil {
		writeError(w, http.StatusBadRequest, "the body: "+err.Error())
		return false
	}
	return true
}

// writeNoStore answers v, which holds a URL to play, kept by no cache.
func writeNoStore(w http.ResponseWriter, v any) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, v)
}

// previewRequest is a preview's body.
type previewRequest struct {
	Library string `json:"library"`
	Item    string `json:"item"`
	Channel string `json:"channel"`
}

// previewAnswer is its answer.
type previewAnswer struct {
	URL     string `json:"url"`
	Poster  string `json:"poster"`
	Expires int64  `json:"expires"`
	Subject string `json:"subject"`
	Session string `json:"session"`
	// Signed is how the URL is let in: url, a signed URL; token, a token
	// in its query; or public.
	Signed string `json:"signed"`
	Route  string `json:"route"`
	CORS   string `json:"cors"`
}

// preview answers POST /media/preview: a URL of an item's playlist, or a
// channel's, absolute, on the listener of the first vod (hls) route that
// serves it, good for PreviewTTL for the console's user.
func (s *Server) preview(w http.ResponseWriter, r *http.Request) {
	var in previewRequest
	if !s.readJSON(w, r, &in) {
		return
	}
	user := r.Header.Get(ConsoleUserHeader)
	if user == "" {
		user = "admin-api"
	}
	out := previewAnswer{Subject: "workbench:" + user}
	if !validSubject(out.Subject) {
		writeError(w, http.StatusBadRequest, "the console user's name does not make a subject: 1 to 118 bytes with no control characters")
		return
	}
	engine := s.configs.env.Engine
	v := s.configs.mediaView()
	var rt *mediaRoute
	var target, stream, poster string
	switch {
	case in.Channel != "" && in.Library == "" && in.Item == "":
		rt = v.route("hls", "")
		if rt == nil {
			writeError(w, http.StatusNotFound, "no hls route serves the channels")
			return
		}
		if m := channels.Lookup(engine); m == nil || m.Channel(in.Channel) == nil {
			writeError(w, http.StatusNotFound, "no channel named "+strconv.Quote(in.Channel)+" runs here")
			return
		}
		target, stream = rt.prefix+in.Channel+"/index.m3u8", in.Channel
	case in.Channel == "" && in.Library != "" && in.Item != "":
		rt = v.route("vod", in.Library)
		if rt == nil {
			writeError(w, http.StatusNotFound, "no vod route serves the library "+in.Library)
			return
		}
		l := medialib.Lookup(engine, in.Library)
		if l == nil || l.Catalog().Scanned.IsZero() {
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusServiceUnavailable, "the media library "+strconv.Quote(in.Library)+" is not loaded, or not scanned yet")
			return
		}
		it := l.Item(in.Item)
		if it == nil || it.Error != "" {
			writeError(w, http.StatusNotFound, "no item "+strconv.Quote(in.Item)+" the library serves")
			return
		}
		target, stream = rt.prefix+it.ID+"/index.m3u8", "vod/"+in.Library+"/"+it.ID
		if it.Poster != nil {
			poster = rt.prefix + it.ID + "/poster." + strings.ToLower(strings.TrimPrefix(path.Ext(it.Poster.Path), "."))
		}
	default:
		writeError(w, http.StatusBadRequest, `the body names an item, {"library", "item"}, or a channel, {"channel"}`)
		return
	}
	if rt.listener == nil {
		writeError(w, http.StatusNotFound, "no listener serves HTTP for the route "+rt.name)
		return
	}
	origin := rt.listener.origin(r.Header.Get(ConsoleHostHeader))
	out.Route, out.CORS = rt.name, rt.cors
	expiry := time.Now().Add(PreviewTTL)
	query := ""
	switch p := mediaauth.Lookup(engine, rt.auth); {
	case rt.auth == "":
		out.Signed = "public"
	case p == nil:
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusServiceUnavailable, "the media_auth policy "+strconv.Quote(rt.auth)+" is not loaded")
		return
	case p.Public(mediaauth.Play):
		out.Signed = "public"
	case p.URLSigning():
		res, ok := mediaauth.ResourceOf(target)
		if !ok {
			writeError(w, http.StatusBadRequest, strconv.Quote(target)+" is not a path a URL is signed for")
			return
		}
		out.Session = newSession()
		q, err := p.SignURL(res, mediaauth.Viewer{Subject: out.Subject, Session: out.Session}, expiry, "")
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "the URL cannot be signed: the policy changed")
			return
		}
		query, out.Signed = q, "url"
	default:
		tok, exp, err := p.Issue(out.Subject, mediaauth.Grant{Play: []string{stream}}, PreviewTTL)
		if err != nil {
			writeError(w, http.StatusBadRequest, "no token can be issued for "+strconv.Quote(stream))
			return
		}
		query, out.Signed, expiry = "token="+url.QueryEscape(tok), "token", exp
	}
	out.Expires = expiry.Unix()
	out.URL = origin + target
	if poster != "" {
		out.Poster = origin + poster
	}
	if query != "" {
		out.URL += "?" + query
		if out.Poster != "" {
			out.Poster += "?" + query
		}
	}
	writeNoStore(w, out)
}

// authPolicy is a policy as GET /media/auth lists it.
type authPolicy struct {
	Name        string            `json:"name"`
	URLSigning  bool              `json:"url_signing"`
	Settings    *urlSettings      `json:"settings,omitempty"`
	Keys        []authKey         `json:"keys"`
	Sessions    int               `json:"sessions"`
	Subjects    int               `json:"subjects"`
	Refusals    map[string]uint64 `json:"refusals"`
	TokenRoutes []tokenRouteJSON  `json:"token_routes"`
}

// urlSettings is a policy's url_signing as it runs, its defaults filled.
type urlSettings struct {
	TTL         string   `json:"ttl"`
	PlayGrace   string   `json:"play_grace"`
	SessionIdle string   `json:"session_idle"`
	MaxSessions int      `json:"max_sessions"`
	OnLimit     string   `json:"on_limit"`
	Cookie      bool     `json:"cookie"`
	BindAddress string   `json:"bind_address"`
	Referers    []string `json:"referers"`
	Origins     []string `json:"origins"`
}

// authKey is a key of a policy, never its secret nor its file's path: a
// policy with a single secret has one, named "".
type authKey struct {
	Name    string `json:"name"`
	Default bool   `json:"default"`
	File    bool   `json:"file"`
	// Modified is when the secret file was last written, by a rotation or
	// by hand; none without a file.
	Modified time.Time `json:"modified,omitzero"`
}

// tokenRouteJSON is a media_tokens route of a policy.
type tokenRouteJSON struct {
	Route       string   `json:"route"`
	URLBase     string   `json:"url_base"`
	URLPrefixes []string `json:"url_prefixes"`
	MaxTTL      string   `json:"max_ttl"`
}

// settingsOf returns u's settings as they run.
func settingsOf(u *config.URLSigning) *urlSettings {
	out := &urlSettings{TTL: u.URLTTL().String(), PlayGrace: u.Grace().String(), SessionIdle: u.Idle().String(),
		MaxSessions: u.MaxSessions, OnLimit: cmp.Or(u.OnLimit, "refuse"), Cookie: u.Cookie, BindAddress: cmp.Or(u.BindAddress, "ip"),
		Referers: slices.Clone(u.Referers), Origins: slices.Clone(u.Origins)}
	if out.Referers == nil {
		out.Referers = []string{}
	}
	if out.Origins == nil {
		out.Origins = []string{}
	}
	return out
}

// keysOf returns p's keys, with their files' times.
func keysOf(p *mediaauth.Policy) []authKey {
	modified := func(file string) time.Time {
		if fi, err := os.Stat(file); err == nil {
			return fi.ModTime().UTC()
		}
		return time.Time{}
	}
	names := p.Keys()
	if names == nil {
		file := p.SecretFile()
		k := authKey{Default: true, File: file != ""}
		if k.File {
			k.Modified = modified(file)
		}
		return []authKey{k}
	}
	out := make([]authKey, len(names))
	for i, n := range names {
		file, _ := p.KeyFile(n)
		out[i] = authKey{Name: n, Default: i == 0, File: file != ""}
		if out[i].File {
			out[i].Modified = modified(file)
		}
	}
	return out
}

// playing returns the sessions of ss that are playing, not ended.
func playing(ss []mediaauth.Session) []mediaauth.Session {
	return slices.DeleteFunc(ss, func(s mediaauth.Session) bool { return s.Ended })
}

// mediaAuth answers GET /media/auth.
func (s *Server) mediaAuth(w http.ResponseWriter, _ *http.Request) {
	v := s.configs.mediaView()
	out := []authPolicy{}
	for i := range v.cfg.MediaAuth {
		c := &v.cfg.MediaAuth[i]
		p := mediaauth.Lookup(s.configs.env.Engine, c.Name)
		if p == nil {
			continue
		}
		live := playing(p.Sessions())
		subjects := map[string]bool{}
		for _, ss := range live {
			subjects[ss.Subject] = true
		}
		ap := authPolicy{Name: c.Name, URLSigning: p.URLSigning(), Keys: keysOf(p), Sessions: len(live), Subjects: len(subjects),
			Refusals: p.URLRefusals(), TokenRoutes: []tokenRouteJSON{}}
		if c.URLSigning != nil {
			ap.Settings = settingsOf(c.URLSigning)
		}
		for _, t := range v.tokens {
			if t.auth == c.Name {
				ap.TokenRoutes = append(ap.TokenRoutes, tokenRouteJSON{Route: t.name, URLBase: t.urlBase, URLPrefixes: t.urlPrefixes, MaxTTL: t.maxTTL.String()})
			}
		}
		out = append(out, ap)
	}
	writeJSON(w, http.StatusOK, map[string]any{"policies": out})
}

// policy is the policy the request names, or nil once it has answered 404.
func (s *Server) policy(w http.ResponseWriter, r *http.Request) *mediaauth.Policy {
	p := mediaauth.Lookup(s.configs.env.Engine, r.PathValue("policy"))
	if p == nil {
		writeError(w, http.StatusNotFound, "no media_auth policy named "+strconv.Quote(r.PathValue("policy"))+" runs here")
	}
	return p
}

// pageOf reads a page's offset and limit from q, limit def when not given
// and at most max; it answers 400 itself when they are not in range.
func pageOf(w http.ResponseWriter, q url.Values, def, max int) (offset, limit int, ok bool) {
	offset, limit = 0, def
	for _, p := range []struct {
		name string
		to   *int
		max  int
	}{{"offset", &offset, -1}, {"limit", &limit, max}} {
		v := q.Get(p.name)
		if v == "" {
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || p.max >= 0 && (n < 1 || n > p.max) {
			writeError(w, http.StatusBadRequest, p.name+" is not a number in range")
			return 0, 0, false
		}
		*p.to = n
	}
	return offset, limit, true
}

// subjectCount is how many sessions of a subject are playing.
type subjectCount struct {
	Subject  string `json:"subject"`
	Sessions int    `json:"sessions"`
}

// authSessions answers GET /media/auth/{policy}/sessions: a page of the
// policy's sessions, those of subject alone or of resources under
// resource when asked, by subject and start, and the sessions playing of
// each subject on the page, to set against max_sessions.
func (s *Server) authSessions(w http.ResponseWriter, r *http.Request) {
	p := s.policy(w, r)
	if p == nil {
		return
	}
	q := r.URL.Query()
	offset, limit, ok := pageOf(w, q, defaultSessionsPage, maxSessionsPage)
	if !ok {
		return
	}
	subject, resource := q.Get("subject"), q.Get("resource")
	all := p.Sessions()
	page := []mediaauth.Session{}
	matched := 0
	for _, ss := range all {
		if subject != "" && ss.Subject != subject || !strings.HasPrefix(ss.Resource, resource) {
			continue
		}
		if matched >= offset && len(page) < limit {
			page = append(page, ss)
		}
		matched++
	}
	counts := []subjectCount{}
	for _, ss := range page {
		if len(counts) == 0 || counts[len(counts)-1].Subject != ss.Subject {
			counts = append(counts, subjectCount{Subject: ss.Subject})
		}
	}
	for _, ss := range all {
		// all and page are sorted by subject, so counts is too.
		if i, found := slices.BinarySearchFunc(counts, ss.Subject, func(c subjectCount, s string) int { return strings.Compare(c.Subject, s) }); found && !ss.Ended {
			counts[i].Sessions++
		}
	}
	maxSessions := 0
	if c := s.configs.running().MediaAuthPolicy(p.Name()); c != nil && c.URLSigning != nil {
		maxSessions = c.URLSigning.MaxSessions
	}
	writeJSON(w, http.StatusOK, map[string]any{"total": len(all), "matched": matched, "offset": offset, "limit": limit,
		"max_sessions": maxSessions, "page": page, "subjects": counts})
}

// endSession answers POST /media/auth/{policy}/sessions/end.
func (s *Server) endSession(w http.ResponseWriter, r *http.Request) {
	p := s.policy(w, r)
	if p == nil {
		return
	}
	var in struct {
		Subject string `json:"subject"`
		ID      string `json:"id"`
	}
	if !s.readJSON(w, r, &in) {
		return
	}
	if in.Subject == "" || in.ID == "" {
		writeError(w, http.StatusBadRequest, `the body names a session: {"subject", "id"}`)
		return
	}
	if !p.EndSession(in.Subject, in.ID) {
		writeError(w, http.StatusNotFound, "the policy has no such session playing")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ended": true})
}

// rotateKeys answers POST /media/auth/{policy}/rotate: {"key": "k1"} one
// key of the keyring, {"all": true} every key, {} the single secret, each
// written anew to its secret_file and used at once. It answers the keys
// rotated, [""] for the single secret, and when.
func (s *Server) rotateKeys(w http.ResponseWriter, r *http.Request) {
	p := s.policy(w, r)
	if p == nil {
		return
	}
	var in struct {
		Key string `json:"key"`
		All bool   `json:"all"`
	}
	if !s.readJSON(w, r, &in) {
		return
	}
	rotated, err := p.RotateFile(in.Key, in.All)
	switch {
	case errors.Is(err, mediaauth.ErrKeyChoice):
		writeError(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "mediaauth: "))
	case errors.Is(err, mediaauth.ErrNoKey):
		writeError(w, http.StatusNotFound, "no key named "+strconv.Quote(in.Key))
	case errors.Is(err, mediaauth.ErrInlineSecret):
		writeError(w, http.StatusConflict, strings.TrimPrefix(err.Error(), "mediaauth: "))
	case err != nil:
		// Neither the error nor the answer names the file.
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("the new secret was not written; keys rotated before: %q", rotated))
	default:
		writeJSON(w, http.StatusOK, map[string]any{"rotated": rotated, "since": time.Now().Unix()})
	}
}

// testURLRequest is the body of POST /media/auth/{policy}/urls.
type testURLRequest struct {
	Subject string `json:"subject"`
	Path    string `json:"path"`
	Address string `json:"address"`
	TTL     string `json:"ttl"`
	Route   string `json:"route"`
}

// testURL answers POST /media/auth/{policy}/urls: a URL signed for a
// viewer, as media_tokens' /urls signs one, through the media_tokens route
// named, whose url_prefixes, max_ttl and url_base apply, or else made
// absolute on the listener of the vod or hls route that serves the path.
func (s *Server) testURL(w http.ResponseWriter, r *http.Request) {
	p := s.policy(w, r)
	if p == nil {
		return
	}
	if !p.URLSigning() {
		writeError(w, http.StatusNotFound, "the media_auth policy has no url_signing")
		return
	}
	var in testURLRequest
	if !s.readJSON(w, r, &in) {
		return
	}
	bad := func(msg string) { writeError(w, http.StatusBadRequest, msg) }
	if !validSubject(in.Subject) {
		bad(fmt.Sprintf("%q is not a subject: 1 to 128 bytes with no control characters", in.Subject))
		return
	}
	viewer := mediaauth.Viewer{Subject: in.Subject, Session: newSession()}
	if in.Address != "" {
		a, err := netip.ParseAddr(in.Address)
		if err != nil || a.Zone() != "" {
			bad(fmt.Sprintf("%q is not an IP address", in.Address))
			return
		}
		viewer.Address = a
	}
	res, ok := mediaauth.ResourceOf(in.Path)
	if !ok {
		bad(fmt.Sprintf("%q is not a path a URL is signed for, as /vod/films/films/bbb/index.m3u8 or /live/cam.flv", in.Path))
		return
	}
	v := s.configs.mediaView()
	maxTTL, base := maxTestTTL, ""
	ttl := min(p.URLTTL(), maxTTL)
	if in.Route != "" {
		i := slices.IndexFunc(v.tokens, func(t tokenRoute) bool { return t.name == in.Route && t.auth == p.Name() })
		if i < 0 {
			writeError(w, http.StatusNotFound, "no media_tokens route "+strconv.Quote(in.Route)+" signs for the policy")
			return
		}
		t := v.tokens[i]
		if len(t.urlPrefixes) > 0 && !slices.ContainsFunc(t.urlPrefixes, func(pre string) bool { return strings.HasPrefix(in.Path, pre) }) {
			bad(fmt.Sprintf("%q starts with none of the url_prefixes the route may sign for: %s", in.Path, strings.Join(t.urlPrefixes, ", ")))
			return
		}
		maxTTL, base = t.maxTTL, t.urlBase
		ttl = min(p.URLTTL(), maxTTL)
	} else if rt := v.routeOf(in.Path); rt != nil && rt.listener != nil {
		base = rt.listener.origin(r.Header.Get(ConsoleHostHeader))
	}
	if in.TTL != "" {
		d, err := time.ParseDuration(in.TTL)
		switch {
		case err != nil || d < time.Second:
			bad(fmt.Sprintf("%q is not a duration of a second or more, as 10m", in.TTL))
			return
		case d > maxTTL:
			bad(fmt.Sprintf("the lifetime %s is longer than %s", d, maxTTL))
			return
		}
		ttl = d
	}
	expiry := time.Now().Add(ttl)
	q, err := p.SignURL(res, viewer, expiry, "")
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "the URL cannot be signed: the policy changed")
		return
	}
	writeNoStore(w, map[string]any{"url": base + in.Path + "?" + q, "expires": expiry.Unix(), "subject": in.Subject, "session": viewer.Session})
}

// The parts of GET /media/stats's answer.
type (
	statsLibrary struct {
		Name   string              `json:"name"`
		Items  int                 `json:"items"`
		Errors int                 `json:"errors"`
		Pool   medialib.PoolStats  `json:"pool"`
		Tables medialib.TableStats `json:"tables"`
	}
	statsPolicy struct {
		Name      string            `json:"name"`
		Sessions  int               `json:"sessions"`
		Refusals  map[string]uint64 `json:"refusals"`
		Resources []statsResource   `json:"resources"`
	}
	statsResource struct {
		Resource string `json:"resource"`
		Sessions int    `json:"sessions"`
	}
	statsChannel struct {
		Name    string      `json:"name"`
		Library string      `json:"library"`
		State   string      `json:"state"`
		Players int         `json:"players"`
		Push    []statsPush `json:"push"`
	}
	statsPush struct {
		Name    string `json:"name"`
		State   string `json:"state"`
		Error   string `json:"error,omitempty"`
		Bytes   uint64 `json:"bytes"`
		Bitrate int64  `json:"bitrate"`
	}
)

// mediaStats answers GET /media/stats: what the media pages chart, read
// at once, which the pages ask for again to make rates of.
func (s *Server) mediaStats(w http.ResponseWriter, _ *http.Request) {
	engine := s.configs.env.Engine
	libs := []statsLibrary{}
	for _, l := range medialib.Libraries(engine) {
		sum := summarize(l)
		libs = append(libs, statsLibrary{Name: sum.Name, Items: sum.Items, Errors: sum.Errors, Pool: sum.Pool, Tables: sum.Tables})
	}
	pols := []statsPolicy{}
	for _, p := range mediaauth.Policies(engine) {
		live := playing(p.Sessions())
		by := map[string]int{}
		for _, ss := range live {
			by[ss.Resource]++
		}
		res := make([]statsResource, 0, len(by))
		for r, n := range by {
			res = append(res, statsResource{Resource: r, Sessions: n})
		}
		slices.SortFunc(res, func(a, b statsResource) int {
			return cmp.Or(cmp.Compare(b.Sessions, a.Sessions), strings.Compare(a.Resource, b.Resource))
		})
		pols = append(pols, statsPolicy{Name: p.Name(), Sessions: len(live), Refusals: p.URLRefusals(), Resources: res[:min(len(res), maxStatsResources)]})
	}
	// The config holds at most config.MaxChannels channels, each with at
	// most config.MaxStreamPushTargets push targets.
	chs := []statsChannel{}
	if m := channels.Lookup(engine); m != nil {
		for _, c := range m.Channels() {
			sc := statsChannel{Name: c.Name, Library: c.Library, State: c.State, Players: m.Subscribers(c.Name), Push: []statsPush{}}
			for _, ps := range c.Push {
				sc.Push = append(sc.Push, statsPush{Name: ps.Name, State: ps.State, Error: ps.Error, Bytes: ps.Bytes, Bitrate: ps.Bitrate})
			}
			chs = append(chs, sc)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"time": time.Now().UnixMilli(), "libraries": libs, "vod": vod.Stats(engine),
		"policies": pols, "channels": chs})
}
