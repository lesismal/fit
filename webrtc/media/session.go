package media

import (
	"crypto/rand"
	stdtls "crypto/tls"
	"encoding/hex"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lesismal/fib/webrtc"
	"github.com/lesismal/fib/webrtc/rtp"
	"github.com/lesismal/fib/webrtc/sdp"
	fibturn "github.com/lesismal/fib/webrtc/turn"
)

// maxTracks bounds the media sections of a publisher forwarded: a WHIP
// client sends one audio and one video track.
const maxTracks = 8

// maxRemoteSSRCs bounds the RTP streams a publisher may send, simulcast
// layers included; only the first of each section is forwarded.
const maxRemoteSSRCs = 16

// rembInterval is how often a publisher is told max_bitrate.
const rembInterval = time.Second

// Error is an offer refused, with the HTTP status that says why.
type Error struct {
	Status int
	Msg    string
	// RetryAfter, when not zero, is sent as Retry-After, in seconds.
	RetryAfter int
}

func (e *Error) Error() string { return e.Msg }

func refuse(status int, msg string) *Error { return &Error{Status: status, Msg: msg} }

func busy(msg string) *Error {
	return &Error{Status: http.StatusServiceUnavailable, Msg: msg, RetryAfter: 5}
}

// Kind is a session's: a publisher's (WHIP) or a viewer's (WHEP).
type Kind uint8

const (
	Publisher Kind = iota
	Viewer
)

func (k Kind) String() string {
	if k == Publisher {
		return "whip"
	}
	return "whep"
}

// session is a PeerConnection of a publisher or a viewer.
type session struct {
	id     string
	kind   Kind
	stream *stream
	pc     *webrtc.PeerConnection
	timer  *time.Timer // connect_timeout, stopped once connected

	// The publisher's bitrate limit and REMB, touched from its callbacks.
	rate rateState
}

// stream is a stream by name: its publisher, the tracks it forwards, and
// its viewers.
type stream struct {
	name    string
	pub     *session
	codecs  []webrtc.Codec // one per kind, what the viewers must accept
	tracks  []*fwdTrack
	viewers map[*session]struct{}
	pending int // viewers being answered
	ended   bool
}

// fwdTrack is a media section of a publisher, forwarded to its viewers.
type fwdTrack struct {
	mid    string
	kind   webrtc.MediaKind
	local  *webrtc.TrackLocal
	remote atomic.Pointer[webrtc.TrackRemote]
	lastKF atomic.Int64 // when a key frame was last asked for, in Unix nanoseconds
}

// rateState is a token bucket of a publisher's bytes, and when it was last
// told its bitrate.
type rateState struct {
	mu     sync.Mutex
	tokens float64
	at     time.Time
	remb   time.Time
}

// Answer is an offer answered: the session's ID, and the answer's SDP.
type Answer struct {
	ID  string
	SDP string
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// pickCodecs picks, for each kind, the first codec of the offer's sending
// sections that is allowed: the one codec of that kind the publisher is
// answered with, so that what it sends is what its viewers are offered.
func pickCodecs(offer *sdp.SessionDescription, allowed []string) []webrtc.Codec {
	var picks []webrtc.Codec
	for _, md := range offer.Media {
		kind := md.Kind()
		if kind != string(webrtc.MediaKindAudio) && kind != string(webrtc.MediaKindVideo) || md.Rejected() {
			continue
		}
		if d := offer.MediaDirection(md); d != sdp.SendOnly && d != sdp.SendRecv {
			continue
		}
		if slices.ContainsFunc(picks, func(c webrtc.Codec) bool { return string(c.Kind()) == kind }) {
			continue
		}
		for _, c := range md.Codecs() {
			if slices.ContainsFunc(allowed, func(n string) bool { return strings.EqualFold(n, c.Name) }) {
				picks = append(picks, webrtc.Codec{MimeType: kind + "/" + c.Name, ClockRate: c.ClockRate, Channels: c.Channels,
					Fmtp: c.Fmtp, Feedback: c.Feedback, PayloadType: c.PayloadType})
				break
			}
		}
	}
	return picks
}

// peerConfig is the Configuration of a session's PeerConnection.
func (s *Server) peerConfig(r *running, st *settings, codecs []webrtc.Codec) webrtc.Configuration {
	return webrtc.Configuration{
		Mux:                    r.mux,
		ICELite:                true,
		Certificates:           []stdtls.Certificate{s.cert},
		Codecs:                 codecs,
		ICEDisconnectedTimeout: min(8*time.Second, st.iceTimeout),
		ICEFailedTimeout:       st.iceTimeout,
		MaxRemoteSSRCs:         maxRemoteSSRCs,
		// Data channels are not used; SCTP keeps no spare buffers.
		SCTPPoolBytes: -1,
	}
}

// answer answers offer on pc, which must not wait: an ICE lite agent's host
// candidates are gathered as soon as the answer is set.
func answer(pc *webrtc.PeerConnection, offer string, prepare func() *Error) (string, *Error) {
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer}); err != nil {
		return "", refuse(http.StatusBadRequest, "the offer: "+err.Error())
	}
	if prepare != nil {
		if e := prepare(); e != nil {
			return "", e
		}
	}
	a, err := pc.CreateAnswer()
	if err != nil {
		return "", refuse(http.StatusBadRequest, "the offer cannot be answered: "+err.Error())
	}
	if err := pc.SetLocalDescription(a); err != nil {
		return "", refuse(http.StatusInternalServerError, "the answer: "+err.Error())
	}
	select {
	case <-pc.GatheringComplete():
	default:
		return "", refuse(http.StatusInternalServerError, "the answer's candidates are not gathered")
	}
	d := pc.LocalDescription()
	if d == nil {
		return "", refuse(http.StatusServiceUnavailable, "the session closed")
	}
	return d.SDP, nil
}

// Publish answers a publisher's offer for the stream name.
func (s *Server) Publish(name, offer string) (*Answer, *Error) {
	st := s.set.Load()
	od, err := sdp.ParseString(offer)
	if err != nil {
		s.m.refused[refuseOffer].Add(1)
		return nil, refuse(http.StatusBadRequest, "the offer is not SDP: "+err.Error())
	}
	picks := pickCodecs(od, st.codecs)
	if len(picks) == 0 {
		s.m.refused[refuseOffer].Add(1)
		return nil, refuse(http.StatusUnprocessableEntity, "the offer sends no audio or video in a codec fit forwards")
	}

	s.mu.Lock()
	r, e := s.reserveLocked(st)
	if e == nil {
		switch {
		case s.streams[name] != nil:
			s.m.refused[refuseConflict].Add(1)
			e = refuse(http.StatusConflict, "the stream is published already")
		case len(s.streams) >= st.maxStreams:
			s.m.refused[refuseStreams].Add(1)
			e = busy("too many streams")
		}
		if e != nil {
			s.pcs--
		}
	}
	var str *stream
	if e == nil {
		str = &stream{name: name, codecs: picks, viewers: map[*session]struct{}{}}
		s.streams[name] = str
		s.m.streams.Add(1)
	}
	s.mu.Unlock()
	if e != nil {
		return nil, e
	}

	sess := &session{id: newID(), kind: Publisher, stream: str}
	pc, err := webrtc.NewPeerConnection(s.peerConfig(r, st, picks))
	if err != nil {
		s.unreserve(r, str, nil)
		return nil, refuse(http.StatusInternalServerError, err.Error())
	}
	sess.pc = pc
	sdpAnswer, e := answer(pc, offer, nil)
	var tracks []*fwdTrack
	if e == nil {
		for _, t := range pc.Transceivers() {
			if len(tracks) == maxTracks {
				break
			}
			d := t.CurrentDirection()
			if t.Mid() == "" || d != webrtc.DirectionRecvOnly && d != webrtc.DirectionSendRecv {
				continue
			}
			for _, c := range picks {
				if c.Kind() == t.Kind() {
					tracks = append(tracks, &fwdTrack{mid: t.Mid(), kind: t.Kind(),
						local: webrtc.NewTrackLocal(c, string(t.Kind())+"-"+t.Mid(), name)})
				}
			}
		}
		if len(tracks) == 0 {
			e = refuse(http.StatusUnprocessableEntity, "the offer sends no audio or video in a codec fit forwards")
		}
	}
	if e != nil {
		s.m.refused[refuseOffer].Add(1)
		s.unreserve(r, str, nil)
		_ = pc.Close()
		return nil, e
	}

	s.mu.Lock()
	if s.cur != r {
		// The server stopped or started again meanwhile, forgetting the
		// reservation.
		s.mu.Unlock()
		_ = pc.Close()
		return nil, busy("the media server is restarting")
	}
	str.tracks = tracks
	str.pub = sess
	s.sessions[sess.id] = sess
	s.m.publishers.Add(1)
	s.m.published.Add(1)
	s.mu.Unlock()

	s.watch(sess, st)
	pc.OnTrack(func(tr *webrtc.TrackRemote) { s.onTrack(sess, tr) })
	return &Answer{ID: sess.id, SDP: sdpAnswer}, nil
}

// View answers a viewer's offer for the stream name.
func (s *Server) View(name, offer string) (*Answer, *Error) {
	st := s.set.Load()
	if _, err := sdp.ParseString(offer); err != nil {
		s.m.refused[refuseOffer].Add(1)
		return nil, refuse(http.StatusBadRequest, "the offer is not SDP: "+err.Error())
	}
	s.mu.Lock()
	r, e := s.reserveLocked(st)
	var str *stream
	if e == nil {
		str = s.streams[name]
		switch {
		case str == nil || str.pub == nil:
			s.m.refused[refuseNoStream].Add(1)
			e = refuse(http.StatusNotFound, "the stream is not published")
		case len(str.viewers)+str.pending >= st.maxViewers:
			s.m.refused[refuseViewers].Add(1)
			e = busy("too many viewers of the stream")
		}
		if e != nil {
			s.pcs--
		} else {
			str.pending++
		}
	}
	s.mu.Unlock()
	if e != nil {
		return nil, e
	}

	sess := &session{id: newID(), kind: Viewer, stream: str}
	pc, err := webrtc.NewPeerConnection(s.peerConfig(r, st, str.codecs))
	if err != nil {
		s.unreserve(r, nil, str)
		return nil, refuse(http.StatusInternalServerError, err.Error())
	}
	sess.pc = pc
	sdpAnswer, e := answer(pc, offer, func() *Error {
		for _, ft := range str.tracks {
			snd, err := pc.AddTrack(ft.local)
			if err != nil {
				return refuse(http.StatusInternalServerError, err.Error())
			}
			snd.OnKeyFrameRequest(func() { s.keyFrame(ft) })
		}
		return nil
	})
	if e == nil && !slices.ContainsFunc(pc.Transceivers(), func(t *webrtc.Transceiver) bool {
		d := t.CurrentDirection()
		return d == webrtc.DirectionSendOnly || d == webrtc.DirectionSendRecv
	}) {
		e = refuse(http.StatusUnprocessableEntity, "the offer receives none of the stream's tracks in its codecs")
	}
	if e != nil {
		s.m.refused[refuseOffer].Add(1)
		s.unreserve(r, nil, str)
		_ = pc.Close()
		return nil, e
	}

	s.mu.Lock()
	if s.cur != r || str.ended {
		if s.cur == r {
			s.pcs--
			str.pending--
		}
		s.mu.Unlock()
		_ = pc.Close()
		return nil, refuse(http.StatusNotFound, "the stream ended")
	}
	str.pending--
	str.viewers[sess] = struct{}{}
	s.sessions[sess.id] = sess
	s.m.viewers.Add(1)
	s.m.viewed.Add(1)
	s.mu.Unlock()

	s.watch(sess, st)
	return &Answer{ID: sess.id, SDP: sdpAnswer}, nil
}

// reserveLocked counts a PeerConnection about to be made, or says why it
// may not be.
func (s *Server) reserveLocked(st *settings) (*running, *Error) {
	switch {
	case s.cur == nil:
		return nil, busy("the media server is not running")
	case s.pcs >= st.maxPCs:
		s.m.refused[refusePCs].Add(1)
		return nil, busy("too many PeerConnections")
	}
	s.pcs++
	return s.cur, nil
}

// unreserve gives back a reservation made on r that did not become a
// session: a stream reserved for a publisher, or a viewer's place in str.
// A server started again since has forgotten it already.
func (s *Server) unreserve(r *running, pub, view *stream) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur != r {
		return
	}
	s.pcs--
	if pub != nil && s.streams[pub.name] == pub {
		delete(s.streams, pub.name)
		pub.ended = true
		s.m.streams.Add(-1)
	}
	if view != nil {
		view.pending--
	}
}

// watch ends sess when its PeerConnection fails or closes, or does not
// connect within connect_timeout.
func (s *Server) watch(sess *session, st *settings) {
	var connected atomic.Bool
	// The timer is set under s.mu, which end takes before it reads it.
	s.mu.Lock()
	sess.timer = time.AfterFunc(st.connect, func() {
		if !connected.Load() {
			s.end(sess, endTimeout)
		}
	})
	s.mu.Unlock()
	sess.pc.OnConnectionStateChange(func(ps webrtc.PeerConnectionState) {
		switch ps {
		case webrtc.PeerConnectionStateConnected:
			if connected.CompareAndSwap(false, true) {
				s.mu.Lock()
				sess.timer.Stop()
				s.mu.Unlock()
				if sess.kind == Viewer {
					for _, ft := range sess.stream.tracks {
						s.keyFrame(ft)
					}
				}
			}
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			s.end(sess, endFailed)
		}
	})
	if ps := sess.pc.ConnectionState(); ps == webrtc.PeerConnectionStateFailed || ps == webrtc.PeerConnectionStateClosed {
		s.end(sess, endFailed)
	}
}

// End ends the session id of kind, as a DELETE does. It reports whether
// there was one.
func (s *Server) End(kind Kind, id string) bool {
	s.mu.Lock()
	sess := s.sessions[id]
	s.mu.Unlock()
	if sess == nil || sess.kind != kind {
		return false
	}
	return s.end(sess, endDelete)
}

// end ends sess, and the viewers of a publisher's. It reports whether sess
// was still running.
func (s *Server) end(sess *session, why int) bool {
	s.mu.Lock()
	if s.sessions[sess.id] != sess {
		s.mu.Unlock()
		return false
	}
	delete(s.sessions, sess.id)
	s.pcs--
	ended := s.forgetLocked(sess, why)
	s.mu.Unlock()
	closeSessions(append(ended, sess))
	return true
}

// forgetLocked forgets sess in its stream; a publisher's stream ends, and
// its viewers are forgotten too, and returned for the caller to close.
func (s *Server) forgetLocked(sess *session, why int) []*session {
	s.m.ended[why].Add(1)
	str := sess.stream
	if sess.kind == Viewer {
		s.m.viewers.Add(-1)
		delete(str.viewers, sess)
		return nil
	}
	s.m.publishers.Add(-1)
	if !str.ended {
		str.ended = true
		s.m.streams.Add(-1)
	}
	if s.streams[str.name] == str {
		delete(s.streams, str.name)
	}
	viewerWhy := endPublisherLeft
	if why == endReload {
		viewerWhy = endReload
	}
	var ended []*session
	for v := range str.viewers {
		if s.sessions[v.id] == v {
			delete(s.sessions, v.id)
			s.pcs--
			s.m.viewers.Add(-1)
			s.m.ended[viewerWhy].Add(1)
			ended = append(ended, v)
		}
	}
	clear(str.viewers)
	return ended
}

// closeSessions closes the PeerConnections of sessions already forgotten.
// It must not run under s.mu: closing calls their callbacks.
func closeSessions(ss []*session) {
	// Their connect timers, if still to fire, find them gone.
	for _, x := range ss {
		if x.pc != nil {
			_ = x.pc.Close()
		}
	}
}

// onTrack forwards a publisher's track, the first stream of its section.
func (s *Server) onTrack(sess *session, tr *webrtc.TrackRemote) {
	mid := tr.Transceiver().Mid()
	var ft *fwdTrack
	for _, t := range sess.stream.tracks {
		if t.mid == mid {
			ft = t
		}
	}
	if ft == nil || !ft.remote.CompareAndSwap(nil, tr) {
		// Another simulcast layer, or a section not forwarded.
		return
	}
	ft.local.SetHeaderExtensions(tr.HeaderExtensions())
	tr.OnRTP(func(pkt []byte) { s.forward(sess, ft, pkt) })
	if ft.kind == webrtc.MediaKindVideo {
		s.keyFrame(ft)
	}
}

// forward sends a publisher's packet to its viewers, within max_bitrate,
// and tells the publisher max_bitrate once a second.
func (s *Server) forward(sess *session, ft *fwdTrack, pkt []byte) {
	st := s.set.Load()
	now := time.Now()
	if !sess.rate.allow(len(pkt), st.bitrate, now) {
		s.m.droppedBitrate.Add(1)
		return
	}
	s.m.packets.Add(1)
	s.m.bytes.Add(uint64(len(pkt)))
	_ = ft.local.WriteRTP(pkt)
	if sess.rate.rembDue(now) {
		s.sendREMB(sess, uint64(st.bitrate))
	}
}

// allow takes n bytes from the bucket of rate bits a second, which holds two
// seconds' worth.
func (r *rateState) allow(n int, rate int64, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	capacity := float64(rate) / 8 * 2
	if r.at.IsZero() {
		r.tokens = capacity
	} else {
		r.tokens = min(capacity, r.tokens+now.Sub(r.at).Seconds()*float64(rate)/8)
	}
	r.at = now
	if r.tokens < float64(n) {
		return false
	}
	r.tokens -= float64(n)
	return true
}

func (r *rateState) rembDue(now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if now.Sub(r.remb) < rembInterval {
		return false
	}
	r.remb = now
	return true
}

// sendREMB tells the publisher the most it may send, as REMB feedback
// (draft-alvestrand-rmcat-remb), for the streams it sends.
func (s *Server) sendREMB(sess *session, bitrate uint64) {
	remb := rtp.REMB{SenderSSRC: 1, Bitrate: bitrate}
	for _, ft := range sess.stream.tracks {
		if r := ft.remote.Load(); r != nil {
			remb.SSRCs = append(remb.SSRCs, r.SSRC())
		}
	}
	if len(remb.SSRCs) == 0 {
		return
	}
	b, err := remb.Append(nil)
	if err == nil {
		_ = sess.pc.WriteRTCP(b)
	}
}

// keyFrame asks the publisher of ft for a key frame, unless one was asked
// for less than key_frame_interval ago.
func (s *Server) keyFrame(ft *fwdTrack) {
	if ft.kind != webrtc.MediaKindVideo {
		return
	}
	r := ft.remote.Load()
	if r == nil {
		return
	}
	now := time.Now().UnixNano()
	last := ft.lastKF.Load()
	if now-last < int64(s.set.Load().keyFrames) || !ft.lastKF.CompareAndSwap(last, now) {
		s.m.keyFramesHeld.Add(1)
		return
	}
	if r.RequestKeyFrame() == nil {
		s.m.keyFramesSent.Add(1)
	}
}

// Links returns the Link header values that tell a peer of the ICE servers
// (RFC 9725 section 4.6): ice_servers, and turn_urls with credentials made
// from webrtc.turn's secret for session id.
func (s *Server) Links(id string) []string {
	st := s.set.Load()
	if st == nil {
		return nil
	}
	var links []string
	add := func(url, user, cred string) {
		l := "<" + url + `>; rel="ice-server"`
		if user != "" || cred != "" {
			l += `; username="` + user + `"; credential="` + cred + `"; credential-type="password"`
		}
		links = append(links, l)
	}
	for _, is := range st.ice {
		for _, u := range is.URLs {
			add(u, is.Username, is.Credential)
		}
	}
	if len(st.turnURLs) > 0 && st.turnSecret != nil {
		user, pass := fibturn.EphemeralCredentials(st.turnSecret, "fit-"+id[:min(len(id), 16)], time.Now().Add(st.ttl))
		for _, u := range st.turnURLs {
			add(u, user, pass)
		}
	}
	return links
}

// Sessions returns how many sessions of each kind run.
func (s *Server) Sessions() (publishers, viewers int) {
	return int(s.m.publishers.Load()), int(s.m.viewers.Load())
}
