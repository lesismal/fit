// Package media is fit's WebRTC media server, an SFU: the role webrtc.media,
// a UDP port of the listeners' engine that every PeerConnection shares (fib's
// webrtc.Mux), and the handler modules whip and whep, which publish a stream
// by name (RFC 9725) and play it (draft-ietf-wish-whep). See
// docs/decisions/029-webrtc.md and docs/webrtc.md.
//
// fit is an ICE lite agent on the Mux: its only candidates are the Mux's
// host addresses, known before any offer, so an answer is complete the moment
// it is made, and the HTTP handler answers without waiting for gathering, on
// the worker the offer arrived on. A publisher's RTP is forwarded to each of
// its viewers through a webrtc.TrackLocal, on the worker its packet arrived
// on, and its viewers' key frame requests go back to it at most once a
// key_frame_interval. Everything is bounded: PeerConnections in all, streams,
// viewers a stream, a publisher's bitrate, the offer's size, and the time a
// session may take to connect and its peer may be silent.
//
// The member's access decides which peer addresses the Mux serves, asked
// by fib as a new address's first datagram arrives (MuxConfig.Filter),
// before a Connection is made: its policy, the member's list and then the
// global one, is set again by every Apply, which keeps the Mux when listen
// and public_ips are the same; the addresses already served are not asked
// again.
//
// Sessions belong to the role, not to the routes: a reload that keeps the
// role's listen and public_ips keeps them, and applies the rest of its
// settings to what comes next; one that changes those, or leaves the role
// out, ends them.
//
// The package also puts other media servers behind fit: the handler
// webrtc_proxy passes WHIP and WHEP to an upstream of media servers, and
// the role webrtc.proxy relays their peers through one UDP port, routing a
// new peer address by the ICE ufrag of its first STUN request (proxy.go,
// relay.go and proxyhttp.go).
package media

import (
	"context"
	stdtls "crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lesismal/fib"
	fibtls "github.com/lesismal/fib/tls"
	"github.com/lesismal/fib/webrtc"

	"github.com/lesismal/fit/access"
	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/bindcheck"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/observe"
)

// ID is the role's, and its member's path in the config.
const ID = "webrtc.media"

func init() {
	app.Register(app.Info{
		ID: ID,
		Member: func(c *config.Config) any {
			if c.WebRTC == nil || c.WebRTC.Media == nil {
				return nil
			}
			// The whole webrtc member: the media server makes TURN
			// credentials from webrtc.turn's secret.
			return c.WebRTC
		},
		New: func(h *app.Host) (app.Role, error) { return New(h) },
		// The access's list files, read when the role applies.
		CertFiles: func(c *config.Config) []string { return c.WebRTC.Media.Access.Files() },
	})
}

// servers are the media servers running, by the engine the routes' modules
// are given (app.Host.Upstream, module.Context.Engine), so that a whip or
// whep handler finds the one of its own process, or test.
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

func unregister(key *fib.Engine, s *Server) {
	serversMu.Lock()
	cur := servers.Load()
	mine := cur != nil && (*cur)[key] == s
	serversMu.Unlock()
	if mine {
		register(key, nil)
	}
}

// Lookup returns the media server the modules of engine use, or nil.
func Lookup(engine *fib.Engine) *Server {
	if m := servers.Load(); m != nil {
		return (*m)[engine]
	}
	return nil
}

// Server is the role: the Mux, the sessions on it, and the streams.
type Server struct {
	engine *fib.Engine
	key    *fib.Engine
	host   *app.Host
	log    *logs.Logger
	cert   stdtls.Certificate

	// access is the policy the Mux's Filter asks, set by every Apply.
	access access.Gate

	set atomic.Pointer[settings]

	// applyMu orders Apply and Stop.
	applyMu sync.Mutex
	// mu guards everything below; it is never held while a PeerConnection
	// is called.
	mu       sync.Mutex
	cur      *running
	stopped  bool
	sessions map[string]*session
	streams  map[string]*stream
	pcs      int

	m meters
}

// running is a Mux serving one member's listen and public_ips.
type running struct {
	key string
	cfg *config.WebRTCMedia
	mux *webrtc.Mux
}

// settings is what the server reads for each new session, swapped whole.
type settings struct {
	codecs     []string // names of the codecs publishers may send
	ice        []config.ICEServer
	turnURLs   []string
	turnSecret []byte
	ttl        time.Duration
	maxPCs     int
	maxStreams int
	maxViewers int
	bitrate    int64
	sdpBytes   int
	connect    time.Duration
	iceTimeout time.Duration
	keyFrames  time.Duration
}

// New returns the role, which its first Apply starts. Its DTLS certificate,
// shared by its PeerConnections, is made here.
func New(h *app.Host) (*Server, error) {
	cert, err := fibtls.GenerateWebRTCCertificate()
	if err != nil {
		return nil, err
	}
	return &Server{engine: h.Engine, key: h.Upstream, host: h, log: h.Log, cert: cert,
		sessions: map[string]*session{}, streams: map[string]*stream{}}, nil
}

func newSettings(w *config.WebRTC) *settings {
	m := w.Media
	st := &settings{
		ice: m.ICEServers, turnURLs: m.TURNURLs, ttl: m.CredentialTTL(),
		maxPCs: m.MaxPCs(), maxStreams: m.MaxStreamCount(), maxViewers: m.MaxViewerCount(),
		bitrate: m.Bitrate(), sdpBytes: m.SDPBytes(),
		connect: m.Connect(), iceTimeout: m.ICE(), keyFrames: m.KeyFrames(),
	}
	if w.TURN != nil && w.TURN.Secret != "" {
		st.turnSecret = []byte(w.TURN.Secret)
	}
	st.codecs = m.Codecs
	if len(st.codecs) == 0 {
		st.codecs = config.MediaCodecs
	}
	return st
}

// Addrs returns the addresses the Mux advertises, or nil while the server
// does not run.
func (s *Server) Addrs() []netip.AddrPort {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == nil {
		return nil
	}
	return s.cur.mux.Addrs()
}

// Apply moves the server to member, a *config.WebRTC with Media set.
func (s *Server) Apply(member any) error {
	w, ok := member.(*config.WebRTC)
	if !ok || w == nil || w.Media == nil {
		return errors.New("webrtc.media: no member")
	}
	cfg := w.Media
	st := newSettings(w)
	policy, err := s.host.Access(cfg.Access)
	if err != nil {
		return fmt.Errorf("access: %w", err)
	}
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return app.ErrStopped
	}
	key := staticKey(cfg)
	old := s.cur
	// Before a new Mux starts, which goes by it from its first peer; one
	// that cannot start has the policy as it was put back.
	prevAccess := s.access.Policy()
	s.access.Set(policy)
	if old != nil && old.key == key {
		s.set.Store(st)
		old.cfg = cfg
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	if old == nil || old.cfg.Listen != cfg.Listen {
		if err := checkFree(cfg.Listen); err != nil {
			s.access.Set(prevAccess)
			return err
		}
	}
	var ended []*session
	if old != nil && old.cfg.Listen == cfg.Listen {
		// The new Mux needs the old one's port: end the sessions, telling
		// their peers, close the old Mux, and start it again if the new one
		// cannot start.
		s.mu.Lock()
		ended = s.forgetAllLocked()
		s.cur = nil
		s.mu.Unlock()
		closeSessions(ended)
		closeMux(s.log, old)
		next, err := s.start(cfg, key)
		if err != nil {
			s.access.Set(prevAccess)
			again, err2 := s.start(old.cfg, old.key)
			if err2 != nil {
				s.log.Errorf("the media server as it was could not start again either: %v", err2)
			}
			s.mu.Lock()
			s.cur = again
			s.mu.Unlock()
			return err
		}
		s.mu.Lock()
		s.set.Store(st)
		s.cur = next
		s.mu.Unlock()
	} else {
		next, err := s.start(cfg, key)
		if err != nil {
			s.access.Set(prevAccess)
			return err
		}
		s.mu.Lock()
		ended = s.forgetAllLocked()
		s.set.Store(st)
		s.cur = next
		s.mu.Unlock()
		closeSessions(ended)
		if old != nil {
			closeMux(s.log, old)
		}
	}
	register(s.key, s)
	addrs := s.Addrs()
	if old != nil {
		s.m.restarts.Add(1)
		s.log.Warnf("media on %s; the server started again for a change of listen or public_ips, ending %d sessions", addrList(addrs), len(ended))
	} else {
		s.log.Infof("media on %s", addrList(addrs))
	}
	return nil
}

// Stop ends every session, telling its peer, and closes the Mux.
func (s *Server) Stop(context.Context) {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	old := s.cur
	ended := s.forgetAllLocked()
	s.cur = nil
	s.mu.Unlock()
	unregister(s.key, s)
	closeSessions(ended)
	if old != nil {
		closeMux(s.log, old)
	}
}

func (s *Server) start(cfg *config.WebRTCMedia, key string) (*running, error) {
	mc := webrtc.MuxConfig{Addr: cfg.Listen, Filter: s.access.Allows}
	for _, p := range cfg.PublicIPs {
		a, err := netip.ParseAddr(p)
		if err != nil {
			return nil, fmt.Errorf("public_ips: %w", err)
		}
		mc.PublicAddrs = append(mc.PublicAddrs, a.Unmap())
	}
	mux, err := webrtc.NewMux(s.engine, mc)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", cfg.Listen, err)
	}
	return &running{key: key, cfg: cfg, mux: mux}, nil
}

// forgetAllLocked forgets every session and stream, and returns the
// sessions for the caller to close once s.mu is released.
func (s *Server) forgetAllLocked() []*session {
	ended := make([]*session, 0, len(s.sessions))
	for id, x := range s.sessions {
		if s.sessions[id] != x {
			continue
		}
		ended = append(ended, x)
		delete(s.sessions, id)
		ended = append(ended, s.forgetLocked(x, endReload)...)
	}
	clear(s.streams)
	s.m.streams.Store(0)
	s.pcs = 0
	return ended
}

func closeMux(log *logs.Logger, r *running) {
	if err := r.mux.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		log.Warnf("closing the media port: %v", err)
	}
}

// checkFree fails when another socket holds UDP address: see package
// bindcheck.
func checkFree(address string) error { return bindcheck.Free("udp", address) }

// staticKey identifies what the Mux is made with.
func staticKey(c *config.WebRTCMedia) string {
	return fmt.Sprintf("%q %q", c.Listen, c.PublicIPs)
}

func addrList(addrs []netip.AddrPort) string {
	s := make([]string, len(addrs))
	for i, a := range addrs {
		s[i] = a.String()
	}
	return strings.Join(s, ", ")
}

// meters are the server's metrics.
type meters struct {
	publishers, viewers, streams atomic.Int64
	published, viewed            atomic.Uint64
	packets, bytes               atomic.Uint64
	droppedBitrate               atomic.Uint64
	keyFramesSent, keyFramesHeld atomic.Uint64
	restarts                     atomic.Uint64
	refused                      [numRefusals]atomic.Uint64
	ended                        [numEnds]atomic.Uint64
}

// Why an offer was refused, for fit_webrtc_media_refused_total.
const (
	refusePCs = iota
	refuseStreams
	refuseViewers
	refuseConflict
	refuseNoStream
	refuseOffer
	numRefusals
)

var refusalNames = [numRefusals]string{"peer_connections", "streams", "viewers", "conflict", "no_stream", "offer"}

// Why a session ended, for fit_webrtc_media_sessions_ended_total.
const (
	endDelete = iota
	endPublisherLeft
	endFailed
	endTimeout
	endReload
	numEnds
)

var endNames = [numEnds]string{"delete", "publisher_left", "failed", "connect_timeout", "reload"}

// WriteMetrics writes the server's metrics.
func (s *Server) WriteMetrics(w observe.MetricWriter) {
	m := &s.m
	w.Family("fit_webrtc_media_peer_connections", "gauge", "PeerConnections of the WHIP and WHEP sessions, by role.")
	w.Sample("fit_webrtc_media_peer_connections", float64(m.publishers.Load()), "role", "publisher")
	w.Sample("fit_webrtc_media_peer_connections", float64(m.viewers.Load()), "role", "viewer")
	w.Family("fit_webrtc_media_streams", "gauge", "Streams published.")
	w.Sample("fit_webrtc_media_streams", float64(m.streams.Load()))
	w.Family("fit_webrtc_media_sessions_total", "counter", "Sessions answered, by role.")
	w.Sample("fit_webrtc_media_sessions_total", float64(m.published.Load()), "role", "publisher")
	w.Sample("fit_webrtc_media_sessions_total", float64(m.viewed.Load()), "role", "viewer")
	w.Family("fit_webrtc_media_refused_total", "counter", "Offers refused, by reason: a limit (peer_connections, streams, viewers), a stream published already (conflict) or not at all (no_stream), or an offer that could not be answered.")
	for i, name := range refusalNames {
		w.Sample("fit_webrtc_media_refused_total", float64(m.refused[i].Load()), "reason", name)
	}
	w.Family("fit_webrtc_media_sessions_ended_total", "counter", "Sessions ended, by reason: a DELETE, their publisher gone, ICE or DTLS failed or the peer silent, not connected in connect_timeout, or the server started again or stopped.")
	for i, name := range endNames {
		w.Sample("fit_webrtc_media_sessions_ended_total", float64(m.ended[i].Load()), "reason", name)
	}
	w.Family("fit_webrtc_media_received_packets_total", "counter", "RTP packets received from publishers and forwarded to their viewers.")
	w.Sample("fit_webrtc_media_received_packets_total", float64(m.packets.Load()))
	w.Family("fit_webrtc_media_received_bytes_total", "counter", "Bytes of the RTP packets received from publishers and forwarded.")
	w.Sample("fit_webrtc_media_received_bytes_total", float64(m.bytes.Load()))
	w.Family("fit_webrtc_media_dropped_total", "counter", "RTP packets of publishers dropped, by reason: past max_bitrate.")
	w.Sample("fit_webrtc_media_dropped_total", float64(m.droppedBitrate.Load()), "reason", "bitrate")
	w.Family("fit_webrtc_media_key_frame_requests_total", "counter", "Viewers' key frame requests, by what became of them: sent to the publisher, or held back by key_frame_interval.")
	w.Sample("fit_webrtc_media_key_frame_requests_total", float64(m.keyFramesSent.Load()), "result", "sent")
	w.Sample("fit_webrtc_media_key_frame_requests_total", float64(m.keyFramesHeld.Load()), "result", "held")
	w.Family("fit_webrtc_media_restarts_total", "counter", "Times a config's change of listen or public_ips started the media server again, ending its sessions.")
	w.Sample("fit_webrtc_media_restarts_total", float64(m.restarts.Load()))
}

var (
	_ app.Role    = (*Server)(nil)
	_ app.Metered = (*Server)(nil)
)
