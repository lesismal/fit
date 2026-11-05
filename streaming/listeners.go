package streaming

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/lesismal/fib"
	"github.com/lesismal/fib/media/hub"
	"github.com/lesismal/fib/media/rtc"
	"github.com/lesismal/fib/media/rtmp"
	"github.com/lesismal/fib/media/rtsp"
	"github.com/lesismal/fib/media/srt"
	"github.com/lesismal/fib/webrtc"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/nettcp"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/mediaauth"
)

// tcpListener is an RTMP or RTSP listener: a TCP binding of the engine,
// behind a bound on its connections.
type tcpListener struct {
	key   string
	cfg   any // the member it serves, to start again with
	index int
	addr  net.Addr
	limit *connLimit
}

func (l *tcpListener) close(engine *fib.Engine) {
	_ = engine.RemoveBinding(l.index)
	l.limit.closeAll()
}

// bindTCP binds address to h, at most max connections at once, listening
// with Multipath TCP when mptcp is set: the member's own choice, off unless
// it asks, whatever GODEBUG says. The free-port check is always plain TCP.
func (s *Server) bindTCP(address string, h fib.Handler, max int, mptcp bool) (*tcpListener, error) {
	if err := checkFree("tcp", address); err != nil {
		return nil, err
	}
	limit := newConnLimit(h, max)
	i, err := s.engine.AddBinding(fib.Binding{Addr: address, Handler: limit, MultipathTCP: mptcp})
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", address, err)
	}
	l := &tcpListener{index: i, limit: limit}
	if addrs, err := s.engine.BindingAddrs(i); err == nil && len(addrs) > 0 {
		l.addr = addrs[0]
	}
	return l, nil
}

// restart is how every listener moves to a member: kept when its key is
// the same, else the old one closed (first, when the two share an address:
// the one it was given or the one it is bound to) and the new one started;
// when the new one cannot start, the old one is started again.
func restart[L any](old L, oldKey string, oldAddrs []string, key, addr string, isNil bool, start func() (L, error), stop func(L), again func() (L, error)) (L, error) {
	var zero L
	if !isNil && oldKey == key {
		return old, nil
	}
	if !isNil && slices.ContainsFunc(oldAddrs, func(a string) bool { return sameAddress(a, addr) }) {
		stop(old)
		next, err := start()
		if err != nil {
			back, err2 := again()
			if err2 != nil {
				return zero, errors.Join(err, fmt.Errorf("the listener as it was could not start again: %w", err2))
			}
			return back, err
		}
		return next, nil
	}
	next, err := start()
	if err != nil {
		return old, err
	}
	if !isNil {
		stop(old)
	}
	return next, nil
}

func addrString(a net.Addr) string {
	if a == nil {
		return ""
	}
	return a.String()
}

// sameAddress reports whether two listen addresses may hold the same
// port: then the old listener must go before the new one binds.
func sameAddress(a, b string) bool {
	ha, pa, err1 := net.SplitHostPort(a)
	hb, pb, err2 := net.SplitHostPort(b)
	if err1 != nil || err2 != nil || pa != pb || pa == "0" {
		return false
	}
	wild := func(h string) bool { return h == "" || h == "0.0.0.0" || h == "::" }
	return ha == hb || wild(ha) || wild(hb)
}

// checkFree fails when another socket holds address: fib binds with
// SO_REUSEPORT, and the kernel would let it share another process's port.
// A plain bind, which a reuseport socket refuses, tells. Port 0 is free.
func checkFree(network, address string) error {
	if _, port, err := net.SplitHostPort(address); err == nil && port == "0" {
		return nil
	}
	if network == "udp" {
		pc, err := net.ListenPacket("udp", address)
		if err != nil {
			return fmt.Errorf("listen on %s: %w", address, err)
		}
		return pc.Close()
	}
	ln, err := nettcp.Listen("tcp", address, false)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", address, err)
	}
	return ln.Close()
}

func (s *Server) applyRTMP(cfg *config.StreamRTMP) error {
	if cfg == nil {
		if s.rtmp != nil {
			s.rtmp.close(s.engine)
			s.rtmp = nil
		}
		return nil
	}
	start := func(c *config.StreamRTMP) func() (*tcpListener, error) {
		return func() (*tcpListener, error) {
			srv := rtmp.NewServer(rtmp.ServerConfig{
				Hub:              s.hub,
				OnPublish:        s.rtmpPublish,
				OnPlay:           s.rtmpPlay,
				HandshakeTimeout: c.Handshake(),
				IdleTimeout:      c.Idle(),
				PlayWait:         c.PlayWait.Std(),
				MaxMessageSize:   c.MessageBytes(),
			})
			l, err := s.bindTCP(c.Listen, srv, c.Conns(), c.MPTCP)
			if err != nil {
				return nil, err
			}
			l.key, l.cfg = rtmpKey(c), c
			s.log.Infof("rtmp on %s", l.addr)
			return l, nil
		}
	}
	var oldKey string
	var oldAddrs []string
	var again func() (*tcpListener, error)
	if s.rtmp != nil {
		old := s.rtmp.cfg.(*config.StreamRTMP)
		oldKey, oldAddrs, again = s.rtmp.key, []string{old.Listen, addrString(s.rtmp.addr)}, start(old)
	}
	l, err := restart(s.rtmp, oldKey, oldAddrs, rtmpKey(cfg), cfg.Listen, s.rtmp == nil,
		start(cfg), func(l *tcpListener) { l.close(s.engine) }, again)
	s.rtmp = l
	return err
}

func rtmpKey(c *config.StreamRTMP) string {
	return fmt.Sprintf("%q %d %s %s %s %d %v", c.Listen, c.Conns(), c.Handshake(), c.Idle(), c.PlayWait.Std(), c.MessageBytes(), c.MPTCP)
}

// rtmpPublish decides an RTMP publish by the credentials of the query of
// its stream name and app.
func (s *Server) rtmpPublish(_ *rtmp.ServerConn, path, query string) (hub.Sink, error) {
	if _, err := s.auth[protoRTMP].Load().allow(mediaauth.Publish, path, mediaauth.FromQuery(query)); err != nil {
		s.refused(protoRTMP, err)
		return nil, err
	}
	return nil, nil
}

func (s *Server) rtmpPlay(_ *rtmp.ServerConn, path, query string) error {
	if _, err := s.auth[protoRTMP].Load().allow(mediaauth.Play, path, mediaauth.FromQuery(query)); err != nil {
		s.refused(protoRTMP, err)
		return err
	}
	return nil
}

func (s *Server) applyRTSP(cfg *config.StreamRTSP) error {
	if cfg == nil {
		if s.rtsp != nil {
			s.rtsp.close(s.engine)
			s.rtsp = nil
		}
		return nil
	}
	start := func(c *config.StreamRTSP) func() (*tcpListener, error) {
		return func() (*tcpListener, error) {
			a, err := newRTSPAuth(c)
			if err != nil {
				return nil, err
			}
			a.s = s
			srv := rtsp.NewServer(rtsp.ServerConfig{
				Hub:            s.hub,
				OnRequest:      a.onRequest,
				SessionTimeout: c.Session(),
				IdleTimeout:    c.Idle(),
				PlayWait:       c.PlayWait.Std(),
			})
			l, err := s.bindTCP(c.Listen, &rtspHandler{Server: srv, a: a}, c.Conns(), c.MPTCP)
			if err != nil {
				return nil, err
			}
			l.key, l.cfg = rtspKey(c), c
			s.log.Infof("rtsp on %s", l.addr)
			return l, nil
		}
	}
	var oldKey string
	var oldAddrs []string
	var again func() (*tcpListener, error)
	if s.rtsp != nil {
		old := s.rtsp.cfg.(*config.StreamRTSP)
		oldKey, oldAddrs, again = s.rtsp.key, []string{old.Listen, addrString(s.rtsp.addr)}, start(old)
	}
	l, err := restart(s.rtsp, oldKey, oldAddrs, rtspKey(cfg), cfg.Listen, s.rtsp == nil,
		start(cfg), func(l *tcpListener) { l.close(s.engine) }, again)
	s.rtsp = l
	return err
}

func rtspKey(c *config.StreamRTSP) string {
	return fmt.Sprintf("%q %d %s %s %s %q %q %v", c.Listen, c.Conns(), c.Session(), c.Idle(), c.PlayWait.Std(), c.Methods(), c.RealmName(), c.MPTCP)
}

// srtListener is the SRT listener: a UDP binding of the engine.
type srtListener struct {
	key   string
	cfg   *config.StreamSRT
	index int
	addr  net.Addr
	srv   *srt.Server
}

func (l *srtListener) close(engine *fib.Engine) {
	_ = engine.RemoveBinding(l.index)
	l.srv.Close()
}

func (s *Server) applySRT(cfg *config.StreamSRT) error {
	if cfg == nil {
		if s.srt != nil {
			s.srt.close(s.engine)
			s.srt = nil
		}
		return nil
	}
	start := func(c *config.StreamSRT) func() (*srtListener, error) {
		return func() (*srtListener, error) {
			sc := srt.ServerConfig{
				Hub:             s.hub,
				Latency:         c.LatencyOr(),
				ParseStreamID:   parseStreamID,
				OnConnect:       s.srtConnect,
				MaxConns:        c.Conns(),
				PeerIdleTimeout: c.PeerIdle(),
				PlayWait:        c.PlayWait.Std(),
			}
			if c.Passphrase != "" {
				sc.Passphrase, sc.PBKeyLen = c.Passphrase, 16
			}
			srv, err := srt.NewServer(sc)
			if err != nil {
				return nil, err
			}
			if err := checkFree("udp", c.Listen); err != nil {
				return nil, err
			}
			i, err := s.engine.AddBinding(fib.Binding{Network: "udp", Addr: c.Listen, Handler: srv})
			if err != nil {
				return nil, fmt.Errorf("listen on %s: %w", c.Listen, err)
			}
			l := &srtListener{key: srtKey(c), cfg: c, index: i, srv: srv}
			if addrs, err := s.engine.BindingAddrs(i); err == nil && len(addrs) > 0 {
				l.addr = addrs[0]
			}
			s.log.Infof("srt on %s", l.addr)
			return l, nil
		}
	}
	var oldKey string
	var oldAddrs []string
	var again func() (*srtListener, error)
	if s.srt != nil {
		oldKey, oldAddrs, again = s.srt.key, []string{s.srt.cfg.Listen, addrString(s.srt.addr)}, start(s.srt.cfg)
	}
	l, err := restart(s.srt, oldKey, oldAddrs, srtKey(cfg), cfg.Listen, s.srt == nil,
		start(cfg), func(l *srtListener) { l.close(s.engine) }, again)
	s.srt = l
	return err
}

func srtKey(c *config.StreamSRT) string {
	return fmt.Sprintf("%q %d %s %q %s %s", c.Listen, c.Conns(), c.LatencyOr(), c.Passphrase, c.PeerIdle(), c.PlayWait.Std())
}

// parseStreamID is srt.ParseStreamID with the query of the access control
// syntax's resource split off, as the plain syntax has it: r=live/cam?token=T
// is the stream live/cam with the query token=T.
func parseStreamID(s string) (srt.StreamID, error) {
	id, err := srt.ParseStreamID(s)
	if err != nil {
		return id, err
	}
	if r, q, ok := strings.Cut(id.Resource, "?"); ok && id.Params != nil {
		id.Resource, id.Query = r, q
		if r == "" {
			return id, errors.New("srt: stream ID names no resource")
		}
	}
	return id, nil
}

// srtConnect decides an SRT caller by its stream ID: s= is a token, or the
// password of u=; or the resource's query has token, or user and pass.
func (s *Server) srtConnect(_ *srt.Request, id srt.StreamID) error {
	a := mediaauth.Play
	if id.Mode == srt.ModePublish {
		a = mediaauth.Publish
	}
	var c mediaauth.Credentials
	switch {
	case id.User != "" && id.Session != "":
		c = mediaauth.Credentials{User: id.User, Password: id.Session}
	case id.Session != "":
		c = mediaauth.Credentials{Token: id.Session}
	default:
		c = mediaauth.FromQuery(id.Query)
	}
	_, err := s.auth[protoSRT].Load().allow(a, id.Resource, c)
	if err == nil {
		return nil
	}
	s.refused(protoSRT, err)
	switch mediaauth.Status(err) {
	case 401:
		return srt.RejectUnauthorized
	case 403:
		return srt.RejectForbidden
	}
	return srt.RejectOverload
}

// rtcListener is the WebRTC port of stream_whip and stream_whep: a Mux,
// and the server of their sessions on it.
type rtcListener struct {
	key  string
	cfg  *config.StreamWebRTC
	mux  *webrtc.Mux
	srv  *rtc.Server
	addr net.Addr
}

// The paths of fib's WHIP and WHEP server, which the handlers map their
// routes' paths to.
const (
	rtcWHIP     = "/whip/"
	rtcWHEP     = "/whep/"
	rtcResource = "/rtc/session/"
)

func (l *rtcListener) close(log *logs.Logger) {
	l.srv.Close()
	if err := l.mux.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		log.Warnf("closing the WebRTC port: %v", err)
	}
}

func (s *Server) applyWebRTC(cfg *config.StreamWebRTC) error {
	if cfg == nil {
		if s.webrtc != nil {
			s.webrtc.close(s.log)
			s.webrtc = nil
		}
		return nil
	}
	start := func(c *config.StreamWebRTC) func() (*rtcListener, error) {
		return func() (*rtcListener, error) {
			mc := webrtc.MuxConfig{Addr: c.Listen}
			for _, p := range c.PublicIPs {
				a, err := netip.ParseAddr(p)
				if err != nil {
					return nil, fmt.Errorf("public_ips: %w", err)
				}
				mc.PublicAddrs = append(mc.PublicAddrs, a.Unmap())
			}
			if err := checkFree("udp", c.Listen); err != nil {
				return nil, err
			}
			mux, err := webrtc.NewMux(s.engine, mc)
			if err != nil {
				return nil, fmt.Errorf("listen on %s: %w", c.Listen, err)
			}
			var links []webrtc.ICEServer
			for _, is := range c.ICEServers {
				links = append(links, webrtc.ICEServer{URLs: is.URLs, Username: is.Username, Credential: is.Credential})
			}
			srv, err := rtc.NewServer(rtc.Config{
				Hub:            s.hub,
				WebRTC:         webrtc.Configuration{Mux: mux, ICELite: true},
				WHIPPrefix:     rtcWHIP,
				WHEPPrefix:     rtcWHEP,
				ResourcePrefix: rtcResource,
				MaxSessions:    c.Sessions(),
				LinkICEServers: links,
				ConnectTimeout: c.Connect(),
				PlayWait:       c.PlayWait.Std(),
				MaxOfferBytes:  c.OfferBytes(),
			})
			if err != nil {
				_ = mux.Close()
				return nil, err
			}
			l := &rtcListener{key: rtcKey(c), cfg: c, mux: mux, srv: srv}
			if as := mux.Addrs(); len(as) > 0 {
				l.addr = net.UDPAddrFromAddrPort(as[0])
			}
			s.log.Infof("webrtc on %s", c.Listen)
			return l, nil
		}
	}
	var oldKey string
	var oldAddrs []string
	var again func() (*rtcListener, error)
	if s.webrtc != nil {
		oldKey, oldAddrs, again = s.webrtc.key, []string{s.webrtc.cfg.Listen, addrString(s.webrtc.addr)}, start(s.webrtc.cfg)
	}
	l, err := restart(s.webrtc, oldKey, oldAddrs, rtcKey(cfg), cfg.Listen, s.webrtc == nil,
		start(cfg), func(l *rtcListener) { l.close(s.log) }, again)
	s.webrtc = l
	return err
}

func rtcKey(c *config.StreamWebRTC) string {
	return fmt.Sprintf("%q %q %v %d %s %s %d", c.Listen, c.PublicIPs, c.ICEServers, c.Sessions(), c.Connect(), c.PlayWait.Std(), c.OfferBytes())
}

// connLimit closes the connections past max as they open, and closes every
// one it has open when the listener goes. What a connection it closed at
// once still reads goes to the protocol's handler, which ignores a
// connection it was not told of (it has no attachment).
type connLimit struct {
	fib.Handler
	max int64
	n   atomic.Int64

	mu     sync.Mutex
	conns  map[*fib.Connection]struct{}
	closed bool
}

func newConnLimit(h fib.Handler, max int) *connLimit {
	return &connLimit{Handler: h, max: int64(max), conns: map[*fib.Connection]struct{}{}}
}

func (l *connLimit) OnOpen(c *fib.Connection) {
	if l.n.Add(1) > l.max {
		l.n.Add(-1)
		_ = c.Close()
		return
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		l.n.Add(-1)
		_ = c.Close()
		return
	}
	l.conns[c] = struct{}{}
	l.mu.Unlock()
	l.Handler.OnOpen(c)
}

func (l *connLimit) OnClose(c *fib.Connection, err error) {
	l.mu.Lock()
	_, ok := l.conns[c]
	delete(l.conns, c)
	l.mu.Unlock()
	if ok {
		l.n.Add(-1)
		l.Handler.OnClose(c, err)
	}
}

func (l *connLimit) OnShutdown(c *fib.Connection) {
	if h, ok := l.Handler.(fib.ShutdownHandler); ok {
		h.OnShutdown(c)
	}
}

func (l *connLimit) OnEngineClose(c *fib.Connection) {
	if h, ok := l.Handler.(fib.EngineCloseHandler); ok {
		h.OnEngineClose(c)
	}
}

func (l *connLimit) closeAll() {
	l.mu.Lock()
	l.closed = true
	conns := make([]*fib.Connection, 0, len(l.conns))
	for c := range l.conns {
		conns = append(conns, c)
	}
	l.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}
