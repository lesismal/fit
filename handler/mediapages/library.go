package mediapages

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	stdhttp "net/http"

	"github.com/lesismal/fib"
	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/mediaauth"
	"github.com/lesismal/fit/medialib"
	"github.com/lesismal/fit/module"
	"github.com/lesismal/fit/streaming/channels"
)

// The library page's bounds and defaults.
const (
	DefaultVOD        = "/vod/{library}/{id}/index.m3u8"
	MaxLibraries      = 16
	DefaultPageItems  = 60
	MaxPageItems      = 500
	maxQueryBytes     = 128
	maxListedChannels = 1000
	playSessionBytes  = 12
	libraryRetryAfter = "1"
)

// library is what the library and player pages serve, compiled.
type library struct {
	names    []string        // the libraries listed, in the config's order
	has      map[string]bool // and as a set
	vod      string          // an item's playlist, {library} and {id} in it
	channels bool            // list the streaming role's channels, played from hls
	hls      string
	engine   *fib.Engine
}

// channelList returns the channels of the streaming role whose libraries
// are engine's; a variable for the tests.
var channelList = defaultChannelList

func defaultChannelList(engine *fib.Engine) []channels.ChannelStatus {
	m := channels.Lookup(engine)
	if m == nil {
		return nil
	}
	return m.Channels()
}

// now is the clock playback URLs expire by, for the tests to move.
var now = time.Now

// provisionLibrary checks libraries, vod and channels, and returns the
// library pages' part of the page's config; nil when they are off. vod's
// origin, when it is on another server, joins origins.
func (p *Pages) provisionLibrary(ctx *module.Context, origins map[string]bool) (map[string]any, error) {
	if len(p.Libraries) == 0 && !p.Channels {
		if p.VOD != "" {
			return nil, ctx.Errorf("vod", "vod is the URL of the libraries' items: name libraries too")
		}
		return nil, nil
	}
	if len(p.Libraries) > MaxLibraries {
		return nil, ctx.Errorf("libraries", "%d libraries: at most %d", len(p.Libraries), MaxLibraries)
	}
	l := &library{has: map[string]bool{}, vod: orDefault(p.VOD, DefaultVOD), channels: p.Channels, hls: p.HLS, engine: ctx.Engine()}
	for i, name := range p.Libraries {
		switch {
		case ctx.MediaLibrary(name) == nil:
			return nil, ctx.Params.Field("libraries").Index(i).Errorf("there is no media_library named %q", name)
		case l.has[name]:
			return nil, ctx.Params.Field("libraries").Index(i).Errorf("%q is listed twice", name)
		}
		l.has[name] = true
		l.names = append(l.names, name)
	}
	if len(l.names) > 0 {
		o, err := vodTemplate(l.vod)
		if err != nil {
			return nil, ctx.Errorf("vod", "%v", err)
		}
		if o != "" {
			origins[o] = true
		}
	}
	if p.Channels && p.HLS == "" {
		return nil, ctx.Errorf("channels", "channels are played from hls: set hls, as /hls/{stream}/index.m3u8")
	}
	p.lib = l
	names := l.names
	if names == nil {
		names = []string{}
	}
	return map[string]any{"libraries": names, "channels": l.channels}, nil
}

// vodTemplate checks vod's URL template, and returns its origin when it is
// on another server: a path or an http or https URL whose last segment, a
// playlist's name, follows {id}/.
func vodTemplate(v string) (string, error) {
	i := strings.LastIndexByte(v, '/')
	if len(v) > 512 || strings.ContainsAny(v, " \t\r\n\x00<>\"'?#") || i < 0 || !strings.HasSuffix(v[:i+1], "{id}/") ||
		v[i+1:] == "" || strings.Contains(v[i+1:], "{") || strings.Count(v, "{id}") != 1 {
		return "", errBadVOD(v)
	}
	if strings.HasPrefix(v, "/") && !strings.HasPrefix(v, "//") {
		return "", nil
	}
	u, err := url.Parse(strings.NewReplacer("{library}", "l", "{id}", "i").Replace(v))
	if err != nil || u.Host == "" || u.Scheme != "http" && u.Scheme != "https" {
		return "", errBadVOD(v)
	}
	return u.Scheme + "://" + u.Host, nil
}

type errBadVOD string

func (e errBadVOD) Error() string {
	return strconv.Quote(string(e)) + " is not a path or an http or https URL of at most 512 characters ending in {id}/ and a playlist's name, as /vod/{library}/{id}/index.m3u8"
}

// playlist returns the URL of an item's playlist, and its directory, where
// its poster is.
func (l *library) playlist(lib, id string) (u, dir string) {
	u = strings.NewReplacer("{library}", lib, "{id}", id).Replace(l.vod)
	return u, u[:strings.LastIndexByte(u, '/')+1]
}

// viewer is who a request of the library API is and what it may play.
type viewer struct {
	subject string
	grant   mediaauth.Grant
	all     bool // playing is public, or there is no policy
}

func (v *viewer) may(stream string) bool { return v.all || v.grant.Allows(mediaauth.Play, stream) }

// whois finds the viewer of c's request and calls next with it, or answers
// the request as mediaauth refuses it: without credentials, 401. A user's
// password, a bcrypt hash to check, is checked on mediaauth's pool while
// the request waits, retained.
func (p *Pages) whois(c *fibhttp.Context, next func(*viewer)) {
	if p.auth.Public(mediaauth.Play) {
		next(&viewer{all: true})
		return
	}
	creds := mediaauth.FromHTTP(c.Request)
	finish := func(subject string, g mediaauth.Grant, err error) {
		if err != nil {
			mediaauth.WriteError(c, err)
			return
		}
		next(&viewer{subject: subject, grant: g})
	}
	if creds.User == "" {
		p.auth.Whois(creds, finish)
		return
	}
	var res struct {
		subject string
		grant   mediaauth.Grant
		err     error
	}
	run := c.Async(func() {
		if c.Err() == nil {
			finish(res.subject, res.grant, res.err)
		}
	})
	p.auth.Whois(creds, func(subject string, g mediaauth.Grant, err error) {
		res.subject, res.grant, res.err = subject, g, err
		run()
	})
}

// libraryAPI serves api/library, the libraries and channels the viewer may
// play, and api/library/<name>, a page of a library's items.
func (p *Pages) libraryAPI(c *fibhttp.Context, rest string) {
	if p.lib == nil {
		plain(c, stdhttp.StatusNotFound, "the pages have no library", nil)
		return
	}
	if c.Request.Method != stdhttp.MethodGet {
		plain(c, stdhttp.StatusMethodNotAllowed, "the library takes GET", stdhttp.Header{"Allow": {"GET"}})
		return
	}
	name, sub := strings.CutPrefix(rest, "/")
	if rest != "" && (!sub || !p.lib.has[name]) {
		plain(c, stdhttp.StatusNotFound, "no such library", nil)
		return
	}
	p.whois(c, func(v *viewer) {
		if rest == "" {
			p.overview(c, v)
			return
		}
		p.items(c, v, name)
	})
}

type libraryJSON struct {
	Name    string `json:"name"`
	Ready   bool   `json:"ready"`
	Scanned int64  `json:"scanned,omitempty"`
}

type channelJSON struct {
	Name  string `json:"name"`
	State string `json:"state"`
	Now   string `json:"now,omitempty"`
	Next  string `json:"next,omitempty"`
}

// overview answers the libraries, and the channels the viewer may play.
func (p *Pages) overview(c *fibhttp.Context, v *viewer) {
	out := struct {
		Subject   string        `json:"subject,omitempty"`
		Libraries []libraryJSON `json:"libraries"`
		Channels  []channelJSON `json:"channels"`
	}{Subject: v.subject, Libraries: []libraryJSON{}, Channels: []channelJSON{}}
	for _, name := range p.lib.names {
		lj := libraryJSON{Name: name}
		if cat := p.catalog(name); cat != nil {
			lj.Ready, lj.Scanned = true, cat.Scanned.Unix()
		}
		out.Libraries = append(out.Libraries, lj)
	}
	if p.lib.channels {
		for _, ch := range channelList(p.lib.engine) {
			if len(out.Channels) == maxListedChannels {
				break
			}
			if !v.may(ch.Name) {
				continue
			}
			cj := channelJSON{Name: ch.Name, State: ch.State}
			if ch.Now != nil {
				cj.Now = ch.Now.Title
			}
			if ch.Next != nil {
				cj.Next = ch.Next.Title
			}
			out.Channels = append(out.Channels, cj)
		}
	}
	writeJSON(c, stdhttp.StatusOK, out, nil)
}

// catalog returns a library's catalog once it has been scanned, or nil.
func (p *Pages) catalog(name string) *medialib.Catalog {
	l := medialib.Lookup(p.lib.engine, name)
	if l == nil || l.Removed() {
		return nil
	}
	if cat := l.Catalog(); cat != nil && !cat.Scanned.IsZero() {
		return cat
	}
	return nil
}

type itemJSON struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Duration   float64  `json:"duration"`
	Width      int      `json:"width,omitempty"`
	Height     int      `json:"height,omitempty"`
	Renditions []string `json:"renditions"`
	Audio      int      `json:"audio,omitempty"`
	Subtitles  int      `json:"subtitles,omitempty"`
	Poster     string   `json:"poster,omitempty"`
}

// items answers a page of a library's items the viewer may play, those
// that can be served, in ID order: offset and limit (DefaultPageItems, at
// most MaxPageItems), and q, matched without case in the ID or the title.
func (p *Pages) items(c *fibhttp.Context, v *viewer, name string) {
	cat := p.catalog(name)
	if cat == nil {
		plain(c, stdhttp.StatusServiceUnavailable, "the library has not been scanned yet", stdhttp.Header{"Retry-After": {libraryRetryAfter}})
		return
	}
	q := c.Request.URL.Query()
	offset, err1 := intParam(q.Get("offset"), 0)
	limit, err2 := intParam(q.Get("limit"), DefaultPageItems)
	search := strings.ToLower(q.Get("q"))
	if err1 != nil || err2 != nil || limit < 1 || limit > MaxPageItems || len(search) > maxQueryBytes {
		plain(c, stdhttp.StatusBadRequest, "offset is 0 or more, limit 1 to "+strconv.Itoa(MaxPageItems)+", and q at most "+strconv.Itoa(maxQueryBytes)+" bytes", nil)
		return
	}
	out := struct {
		Library string     `json:"library"`
		Total   int        `json:"total"`
		Offset  int        `json:"offset"`
		Items   []itemJSON `json:"items"`
	}{Library: name, Offset: offset, Items: []itemJSON{}}
	prefix := "vod/" + name + "/"
	for _, it := range cat.Items {
		if it.Error != "" || !v.may(prefix+it.ID) ||
			search != "" && !strings.Contains(strings.ToLower(it.ID), search) && !strings.Contains(strings.ToLower(it.Title), search) {
			continue
		}
		out.Total++
		if out.Total <= offset || len(out.Items) == limit {
			continue
		}
		out.Items = append(out.Items, p.item(name, it))
	}
	writeJSON(c, stdhttp.StatusOK, out, nil)
}

// item is an item as the library page shows it.
func (p *Pages) item(lib string, it *medialib.Item) itemJSON {
	j := itemJSON{ID: it.ID, Title: it.Title, Duration: it.Duration.Seconds(), Renditions: []string{},
		Audio: len(it.Audio), Subtitles: len(it.Subtitles)}
	for _, r := range it.Renditions {
		if r.Error != "" {
			continue
		}
		if r.Width*r.Height > j.Width*j.Height {
			j.Width, j.Height = r.Width, r.Height
		}
		j.Renditions = append(j.Renditions, r.Name)
	}
	if it.Poster != nil {
		_, dir := p.lib.playlist(lib, it.ID)
		j.Poster = dir + "poster" + strings.ToLower(path.Ext(it.Poster.Path))
	}
	return j
}

func intParam(s string, d int) (int, error) {
	if s == "" {
		return d, nil
	}
	n, err := strconv.Atoi(s)
	if err == nil && n < 0 {
		err = strconv.ErrRange
	}
	return n, err
}

// playRequest is what api/play is asked: an item of a library, or a
// channel.
type playRequest struct {
	Library string `json:"library"`
	ID      string `json:"id"`
	Channel string `json:"channel"`
}

// play answers api/play: the URL the player plays an item or a channel
// from, for the viewer. With the policy's url_signing, and playing not
// public, it is a playback URL signed for the viewer, its address and a
// session of its own, good for url_signing's ttl, whose playlists hand the
// player a grant to play on; otherwise the plain URL, which the login's
// cookie, or the page's token, lets the player in to.
func (p *Pages) play(c *fibhttp.Context) {
	if p.lib == nil {
		plain(c, stdhttp.StatusNotFound, "the pages have no library", nil)
		return
	}
	if c.Request.Method != stdhttp.MethodPost {
		plain(c, stdhttp.StatusMethodNotAllowed, "play takes POST", stdhttp.Header{"Allow": {"POST"}})
		return
	}
	readBody(c, p.settings.maxMessage, func(body []byte) {
		var in playRequest
		if err := json.Unmarshal(body, &in); err != nil || (in.Channel == "") == (in.Library == "" || in.ID == "") {
			plain(c, stdhttp.StatusBadRequest, `the body must be JSON: {"library", "id"} or {"channel"}`, nil)
			return
		}
		p.whois(c, func(v *viewer) { p.playFor(c, v, &in) })
	})
}

func (p *Pages) playFor(c *fibhttp.Context, v *viewer, in *playRequest) {
	var stream, u, title, kind string
	if in.Channel != "" {
		if !p.lib.channels || !p.channel(in.Channel) {
			plain(c, stdhttp.StatusNotFound, "no such channel", nil)
			return
		}
		stream, title, kind = in.Channel, in.Channel, "channel"
		u = strings.ReplaceAll(p.lib.hls, "{stream}", in.Channel)
	} else {
		if !p.lib.has[in.Library] {
			plain(c, stdhttp.StatusNotFound, "no such library", nil)
			return
		}
		cat := p.catalog(in.Library)
		if cat == nil {
			plain(c, stdhttp.StatusServiceUnavailable, "the library has not been scanned yet", stdhttp.Header{"Retry-After": {libraryRetryAfter}})
			return
		}
		it := cat.Item(in.ID)
		if it == nil || it.Error != "" {
			plain(c, stdhttp.StatusNotFound, "no such item", nil)
			return
		}
		stream, title, kind = "vod/"+in.Library+"/"+it.ID, it.Title, "vod"
		u, _ = p.lib.playlist(in.Library, it.ID)
	}
	if !v.may(stream) {
		mediaauth.WriteError(c, mediaauth.ErrForbidden)
		return
	}
	out := struct {
		URL     string `json:"url"`
		Expires int64  `json:"expires,omitempty"`
		Signed  bool   `json:"signed"`
		Title   string `json:"title"`
		Kind    string `json:"kind"`
	}{URL: u, Title: title, Kind: kind}
	if !v.all && p.auth.URLSigning() {
		pathOf := u
		if pu, err := url.Parse(u); err == nil {
			pathOf = pu.Path
		}
		res, ok := mediaauth.ResourceOf(pathOf)
		if !ok {
			plain(c, stdhttp.StatusInternalServerError, "the URL "+strconv.Quote(pathOf)+" is not one a playback URL is signed for", nil)
			return
		}
		expiry := now().Add(p.auth.URLTTL())
		query, err := p.auth.SignURL(res, mediaauth.Viewer{Subject: v.subject, Address: p.auth.ViewerAddr(c.Request), Session: newSession()}, expiry, "")
		if err != nil {
			plain(c, stdhttp.StatusServiceUnavailable, "the playback URL could not be signed: "+err.Error(), stdhttp.Header{"Retry-After": {libraryRetryAfter}})
			return
		}
		sep := "?"
		if strings.Contains(u, "?") {
			sep = "&"
		}
		out.URL, out.Expires, out.Signed = u+sep+query, expiry.Unix(), true
	}
	writeJSON(c, stdhttp.StatusOK, out, nil)
}

// channel reports whether the streaming role has a channel named name.
func (p *Pages) channel(name string) bool {
	for _, ch := range channelList(p.lib.engine) {
		if ch.Name == name {
			return true
		}
	}
	return false
}

// newSession returns a new playback session's ID: random bytes, raw URL
// base64.
func newSession() string {
	var b [playSessionBytes]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}
