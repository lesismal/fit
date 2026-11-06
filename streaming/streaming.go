// Package streaming is fit's streaming role, streaming in fit's config:
// fib's media hub, where each stream is published once and played over
// every protocol, with listeners of the role's own on the listeners' engine
// for RTMP (fib's media/rtmp), RTSP (media/rtsp, interleaved RTP) and SRT
// (media/srt), and a UDP port for WebRTC (media/rtc). Its handlers serve the
// hub on routes: httpflv (media/httpflv), hls (media/hls), and stream_whip
// and stream_whep, which publish into it with WHIP and play from it with
// WHEP. See docs/decisions/030-media-auth-and-pages.md and docs/streaming.md.
//
// Every listener and handler may name a media_auth policy with auth, checked
// by the stream's path in the hook each protocol has for it, on the worker
// the request arrived on: RTMP's publish and play, RTSP's requests, SRT's
// handshake and the HTTP handlers' requests (mediaauth.Guard). The policy
// is read on every check, so that a reload changes it for what follows.
//
// RTMP, RTSP and SRT decide in a hook that must answer before it returns,
// and fit never holds up a worker (docs/rules.md, rule 1): a user whose
// password only a bcrypt hash checks, and whom the policy has not found
// right before, is refused there with a retry status while the hash is
// checked on mediaauth's pool, which remembers the password once it is
// right, so that the client's next attempt passes at once. Tokens and
// plaintext passwords are checked at once. The HTTP handlers wait for the
// check, the request retained.
//
// Its channels (package channels) play the items of a media library into
// the hub as live streams, and its push targets relay streams of the hub to
// outside RTMP and SRT servers.
//
// A reload changes in place what it can: each listener's auth, and the
// channels and push targets. A change of any other field of a listener
// starts that listener again, closing its connections; a change of the
// hub's limits starts everything again, ending every stream.
package streaming

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"

	"github.com/lesismal/fib"
	"github.com/lesismal/fib/media/hub"
	"github.com/lesismal/fib/media/rtc"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/mediaauth"
	"github.com/lesismal/fit/observe"
	"github.com/lesismal/fit/streaming/channels"
)

// ID is the role's, and its member's path in the config.
const ID = "streaming"

func init() {
	app.Register(app.Info{
		ID:     ID,
		Member: func(c *config.Config) any { return c.Streaming },
		New:    func(h *app.Host) (app.Role, error) { return New(h), nil },
	})
}

// The role's servers, by the engine the routes' modules are given
// (app.Host.Upstream, module.Context.Engine).
var (
	serversMu sync.Mutex
	servers   atomic.Pointer[map[*fib.Engine]*Server]
)

func register(key *fib.Engine, s *Server) {
	serversMu.Lock()
	defer serversMu.Unlock()
	m := map[*fib.Engine]*Server{}
	if old := servers.Load(); old != nil {
		for k, v := range *old {
			m[k] = v
		}
	}
	if s == nil {
		delete(m, key)
	} else {
		m[key] = s
	}
	servers.Store(&m)
}

// Lookup returns the streaming role the modules of engine use, or nil.
func Lookup(engine *fib.Engine) *Server {
	if m := servers.Load(); m != nil {
		return (*m)[engine]
	}
	return nil
}

// The protocols a listener of the role serves, which the metrics count by.
const (
	protoRTMP = iota
	protoRTSP
	protoSRT
	numProtos
)

var protoNames = [numProtos]string{"rtmp", "rtsp", "srt"}

// Server is the role: the hub and its listeners.
type Server struct {
	engine *fib.Engine
	key    *fib.Engine
	log    *logs.Logger

	// auth is each listener's policy, swapped whole by every Apply.
	auth [numProtos]atomic.Pointer[guard]
	// cur is what the handlers read: the hub and the WebRTC server.
	cur atomic.Pointer[current]

	// applyMu orders Apply and Stop; what it guards is touched by them
	// alone.
	applyMu sync.Mutex
	stopped bool
	hubKey  string
	hub     *hub.Hub
	rtmp    *tcpListener
	rtsp    *tcpListener
	srt     *srtListener
	webrtc  *rtcListener
	chans   *channels.Manager

	m meters
}

// current is what the handlers serve from.
type current struct {
	hub *hub.Hub
	rtc *rtc.Server
}

// New returns the role, which its first Apply starts.
func New(h *app.Host) *Server {
	return &Server{engine: h.Engine, key: h.Upstream, log: h.Log}
}

// Hub returns the hub, or nil while the role does not run.
func (s *Server) Hub() *hub.Hub {
	if c := s.cur.Load(); c != nil {
		return c.hub
	}
	return nil
}

// RTC returns the WebRTC server of stream_whip and stream_whep, or nil
// while the role runs without streaming.webrtc.
func (s *Server) RTC() *rtc.Server {
	if c := s.cur.Load(); c != nil {
		return c.rtc
	}
	return nil
}

// Addr returns the address a listener of the role is bound to, by its
// protocol, "rtmp", "rtsp", "srt" or "webrtc", or nil while it does not
// run: for tests and logs.
func (s *Server) Addr(proto string) net.Addr {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	switch proto {
	case "rtmp":
		if s.rtmp != nil {
			return s.rtmp.addr
		}
	case "rtsp":
		if s.rtsp != nil {
			return s.rtsp.addr
		}
	case "srt":
		if s.srt != nil {
			return s.srt.addr
		}
	case "webrtc":
		if s.webrtc != nil {
			return s.webrtc.addr
		}
	}
	return nil
}

// Apply moves the role to member, a *config.Streaming.
func (s *Server) Apply(member any) error {
	cfg, ok := member.(*config.Streaming)
	if !ok || cfg == nil {
		return errors.New("streaming: no member")
	}
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	if s.stopped {
		return app.ErrStopped
	}
	s.setAuth(cfg)
	key := fmt.Sprint(cfg.Streams(), cfg.Players(), cfg.GOPCache())
	if s.hub != nil && key != s.hubKey {
		// The hub's limits are fixed when it is made: everything starts
		// again on a new one.
		s.log.Warnf("the hub's limits changed: every stream and connection ends")
		s.m.restarts.Add(1)
		s.stopAll()
	}
	if s.hub == nil {
		s.hub = hub.New(hub.Config{
			GOPCacheBytes:           cfg.GOPCache(),
			MaxStreams:              cfg.Streams(),
			MaxSubscribersPerStream: cfg.Players(),
			OnPublish:               s.onPublish,
			OnUnpublish:             s.onUnpublish,
		})
		s.hubKey = key
		s.cur.Store(&current{hub: s.hub})
	}
	var errs []error
	if err := s.applyRTMP(cfg.RTMP); err != nil {
		errs = append(errs, fmt.Errorf("rtmp: %w", err))
	}
	if err := s.applyRTSP(cfg.RTSP); err != nil {
		errs = append(errs, fmt.Errorf("rtsp: %w", err))
	}
	if err := s.applySRT(cfg.SRT); err != nil {
		errs = append(errs, fmt.Errorf("srt: %w", err))
	}
	if err := s.applyWebRTC(cfg.WebRTC); err != nil {
		errs = append(errs, fmt.Errorf("webrtc: %w", err))
	}
	if s.chans == nil {
		s.chans = channels.New(channels.Options{Hub: s.hub, Libraries: s.key, Dial: s.key, Log: s.log})
		channels.Register(s.key, s.chans)
	}
	if err := s.chans.Apply(cfg.Channels, cfg.PushTargets()); err != nil {
		errs = append(errs, fmt.Errorf("channels: %w", err))
	}
	cur := &current{hub: s.hub}
	if s.webrtc != nil {
		cur.rtc = s.webrtc.srv
	}
	s.cur.Store(cur)
	register(s.key, s)
	return errors.Join(errs...)
}

// setAuth points each listener at its policy.
func (s *Server) setAuth(cfg *config.Streaming) {
	names := [numProtos]string{}
	if cfg.RTMP != nil {
		names[protoRTMP] = cfg.RTMP.Auth
	}
	if cfg.RTSP != nil {
		names[protoRTSP] = cfg.RTSP.Auth
	}
	if cfg.SRT != nil {
		names[protoSRT] = cfg.SRT.Auth
	}
	for i, name := range names {
		g := &guard{key: s.key, name: name}
		if name != "" {
			// The role media_auth applies before this one: the policy is
			// found now, and again at each check while it is not.
			g.p = mediaauth.Lookup(s.key, name)
		}
		s.auth[i].Store(g)
	}
}

// Stop ends every stream and closes every listener.
func (s *Server) Stop(context.Context) {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	if s.stopped {
		return
	}
	s.stopped = true
	serversMu.Lock()
	cur := servers.Load()
	mine := cur != nil && (*cur)[s.key] == s
	serversMu.Unlock()
	if mine {
		register(s.key, nil)
	}
	s.stopAll()
	s.cur.Store(nil)
}

// stopAll closes every listener, the channels and push targets, and the
// hub.
func (s *Server) stopAll() {
	if s.chans != nil {
		channels.Unregister(s.key, s.chans)
		s.chans.Stop()
		s.chans = nil
	}
	if s.rtmp != nil {
		s.rtmp.close(s.engine)
		s.rtmp = nil
	}
	if s.rtsp != nil {
		s.rtsp.close(s.engine)
		s.rtsp = nil
	}
	if s.srt != nil {
		s.srt.close(s.engine)
		s.srt = nil
	}
	if s.webrtc != nil {
		s.webrtc.close(s.log)
		s.webrtc = nil
	}
	if s.hub != nil {
		s.hub.Close()
		s.hub = nil
	}
	s.cur.Store(nil)
}

func (s *Server) onPublish(path string, info hub.PublishInfo) error {
	s.m.published.Add(1)
	s.log.Infof("%s published over %s from %s", path, info.Protocol, info.Remote)
	return nil
}

func (s *Server) onUnpublish(path string) {
	s.log.Infof("%s unpublished", path)
}

// guard is a listener's media_auth policy, by name: none when the name is
// empty. A name the running config has no policy of refuses everyone.
type guard struct {
	key  *fib.Engine
	name string
	p    *mediaauth.Policy
}

// errRetry is a refusal for a password a bcrypt hash checks, which is
// being checked: the client's next attempt passes at once if it is right.
// mediaauth.Status gives it 503.
var errRetry = errors.New("mediaauth: the password is being checked; try again")

// allow checks c for a on stream, without waiting: a bcrypt check it would
// need is started on mediaauth's pool, and errRetry returned.
func (g *guard) allow(a mediaauth.Action, stream string, c mediaauth.Credentials) (string, error) {
	if g == nil || g.name == "" {
		return "", nil
	}
	p := g.policy()
	if p == nil {
		return "", mediaauth.ErrUnavailable
	}
	subject, err := p.Allow(a, stream, c)
	if errors.Is(err, mediaauth.ErrPending) {
		p.Check(a, stream, c, func(string, error) {})
		return "", errRetry
	}
	return subject, err
}

// policy returns the guard's policy, nil for none or one not loaded.
func (g *guard) policy() *mediaauth.Policy {
	if g == nil || g.name == "" {
		return nil
	}
	if g.p != nil {
		return g.p
	}
	return mediaauth.Lookup(g.key, g.name)
}

// meters are the role's metrics.
type meters struct {
	published atomic.Uint64
	restarts  atomic.Uint64
	refused   [numProtos][numRefusals]atomic.Uint64
}

// Why a listener refused a client's credentials.
const (
	refuseUnauthorized = iota
	refuseForbidden
	refuseUnavailable
	numRefusals
)

var refusalNames = [numRefusals]string{"unauthorized", "forbidden", "unavailable"}

func (s *Server) refused(proto int, err error) {
	i := refuseUnavailable
	switch mediaauth.Status(err) {
	case 401:
		i = refuseUnauthorized
	case 403:
		i = refuseForbidden
	}
	s.m.refused[proto][i].Add(1)
}

// WriteMetrics writes the role's metrics.
func (s *Server) WriteMetrics(w observe.MetricWriter) {
	var streams, publishing, players int
	if h := s.Hub(); h != nil {
		for _, st := range h.Streams() {
			streams++
			if st.Publishing {
				publishing++
			}
			players += st.Subscribers
		}
	}
	w.Family("fit_streaming_streams", "gauge", "Streams of the hub, by whether they have a publisher.")
	w.Sample("fit_streaming_streams", float64(publishing), "state", "publishing")
	w.Sample("fit_streaming_streams", float64(streams-publishing), "state", "waiting")
	w.Family("fit_streaming_players", "gauge", "Players of the hub's streams, over every protocol; an HLS stream and format counts once.")
	w.Sample("fit_streaming_players", float64(players))
	w.Family("fit_streaming_published_total", "counter", "Streams published.")
	w.Sample("fit_streaming_published_total", float64(s.m.published.Load()))
	w.Family("fit_streaming_auth_refused_total", "counter", "Clients of the RTMP, RTSP and SRT listeners their media_auth policy refused, by protocol and reason: no or bad credentials (unauthorized), not allowed (forbidden), or the policy not loaded or a password still being checked (unavailable).")
	for p, name := range protoNames {
		for r, reason := range refusalNames {
			w.Sample("fit_streaming_auth_refused_total", float64(s.m.refused[p][r].Load()), "protocol", name, "reason", reason)
		}
	}
	w.Family("fit_streaming_restarts_total", "counter", "Times a change of the hub's limits started the role again, ending every stream.")
	w.Sample("fit_streaming_restarts_total", float64(s.m.restarts.Load()))
	channels.Lookup(s.key).WriteMetrics(w)
}
