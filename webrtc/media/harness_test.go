package media

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fib"
	"github.com/lesismal/fib/webrtc"
	"github.com/lesismal/fib/webrtc/rtp"

	"github.com/lesismal/fit/admin"
	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/listener"
	"github.com/lesismal/fit/observe"
	"github.com/lesismal/fit/runtime"
	"github.com/lesismal/fit/upstream"
)

const waitTimeout = 15 * time.Second

// fit is fit running a config with the media server, as fit runs it: the
// config compiled and served, its roles moved by an app.Manager.
type fit struct {
	configs *admin.Configs
	apps    *app.Manager
	server  *listener.Server
	up      *fib.Engine
	rt      *runtime.Runtime
	stopUp  func()
	base    string // http://127.0.0.1:port
	stopped bool
}

func startFit(t *testing.T, doc string) *fit {
	t.Helper()
	c, err := config.Load([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	f := &fit{}
	if f.up, f.stopUp, err = upstream.StartEngine(); err != nil {
		t.Fatal(err)
	}
	env := runtime.Env{Metrics: observe.NewMetrics(), Engine: f.up}
	g, err := runtime.Compile(c, env)
	if err != nil {
		t.Fatal(err)
	}
	f.rt = new(runtime.Runtime)
	f.rt.Swap(g)
	if f.server, err = listener.Start(f.rt); err != nil {
		t.Fatal(err)
	}
	f.configs = admin.NewConfigs(f.rt, f.server, env, c, []byte(doc), admin.SourceFile, 10)
	f.apps = app.NewManager(f.server.Engine(), f.up, nil, f.configs.EditMember)
	if err := f.apps.Apply(c); err != nil {
		f.stop()
		t.Fatal(err)
	}
	f.configs.UseApps(f.apps)
	t.Cleanup(f.stop)
	f.base = "http://" + f.server.Addrs()[0].Addr.String()
	return f
}

func (f *fit) stop() {
	if f.stopped {
		return
	}
	f.stopped = true
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	f.apps.Stop(ctx)
	_ = f.server.Close()
	f.rt.Close()
	f.stopUp()
}

func (f *fit) reload(t *testing.T, doc string) {
	t.Helper()
	r, err := f.configs.Apply([]byte(doc), "test")
	if err != nil {
		t.Fatalf("reload: %v %+v", err, r)
	}
}

func (f *fit) media() *Server { return Lookup(f.up) }

// doc is a config with routes /whip/* and /whep/*, and media as
// webrtc.media's JSON.
func doc(media string) string {
	return `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "routes": [
    {"match": {"path": ["/whip/*"]}, "handler": {"module": "whip"}},
    {"match": {"path": ["/whep/*"]}, "handler": {"module": "whep"}}
  ],
  "webrtc": {"media": ` + media + `}
}`
}

const loopbackMedia = `{"listen": "127.0.0.1:0", "ice_timeout": "2s", "key_frame_interval": "10ms"}`

func startEngine(t *testing.T) *fib.Engine {
	t.Helper()
	cfg := fib.DefaultConfig()
	cfg.LogSettings = false
	e, err := fib.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- e.Run() }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			e.Stop()
			<-done
			_ = e.Close()
		})
	}
	t.Cleanup(stop)
	cuts.Store(e, stop)
	return e
}

var cuts sync.Map // *fib.Engine -> func()

// stopEngine stops e and closes its sockets at once: its peers are cut,
// with nothing said.
func stopEngine(e *fib.Engine) {
	if stop, ok := cuts.Load(e); ok {
		stop.(func())()
	}
}

// post sends an offer and returns the response, its body read.
func post(t *testing.T, url, ctype, body string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", url, strings.NewReader(body))
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	return do(t, req)
}

func do(t *testing.T, req *http.Request) (*http.Response, string) {
	t.Helper()
	resp, err := (&http.Client{Timeout: waitTimeout}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func request(t *testing.T, method, url string) int {
	t.Helper()
	req, _ := http.NewRequest(method, url, nil)
	resp, _ := do(t, req)
	return resp.StatusCode
}

// peer is a client PeerConnection of fib's, on an engine and port of its
// own, as a browser is.
type peer struct {
	pc       *webrtc.PeerConnection
	location string
	state    chan webrtc.PeerConnectionState
	conn     chan struct{}
	connOnce sync.Once
}

func newPeer(t *testing.T, e *fib.Engine) *peer {
	t.Helper()
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{Engine: e, ListenAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	p := &peer{pc: pc, state: make(chan webrtc.PeerConnectionState, 16), conn: make(chan struct{})}
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		if s == webrtc.PeerConnectionStateConnected {
			p.connOnce.Do(func() { close(p.conn) })
		}
		select {
		case p.state <- s:
		default:
		}
	})
	return p
}

// signal posts p's offer to url and sets the answer; it returns the
// response, which is not 201 when the offer is refused.
func (p *peer) signal(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	offer, err := webrtc.Offer(t.Context(), p.pc, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, body := post(t, url, "application/sdp", offer.SDP)
	if resp.StatusCode != http.StatusCreated {
		return resp, body
	}
	p.location = resp.Header.Get("Location")
	if err := p.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: body}); err != nil {
		t.Fatal(err)
	}
	return resp, body
}

func (p *peer) waitConnected(t *testing.T) {
	t.Helper()
	select {
	case <-p.conn:
	case <-time.After(waitTimeout):
		t.Fatal("not connected")
	}
}

// waitEnded waits for p's PeerConnection to fail or close.
func (p *peer) waitEnded(t *testing.T) {
	t.Helper()
	deadline := time.After(waitTimeout)
	for {
		select {
		case s := <-p.state:
			if s == webrtc.PeerConnectionStateFailed || s == webrtc.PeerConnectionStateClosed {
				return
			}
		case <-deadline:
			t.Fatalf("the peer did not end: %s", p.pc.ConnectionState())
		}
	}
}

// publisher is a peer that sends a video and an audio track.
type publisher struct {
	*peer
	video, audio *webrtc.TrackLocal
	keyFrames    atomic.Int64
	stop         chan struct{}
	stopOnce     sync.Once
}

func newPublisher(t *testing.T, e *fib.Engine) *publisher {
	t.Helper()
	p := &publisher{peer: newPeer(t, e), stop: make(chan struct{})}
	p.video = webrtc.NewTrackLocal(webrtc.Codec{MimeType: "video/VP8", ClockRate: 90000}, "v", "s")
	p.audio = webrtc.NewTrackLocal(webrtc.Codec{MimeType: "audio/opus", ClockRate: 48000, Channels: 2}, "a", "s")
	vs, err := p.pc.AddTrack(p.video)
	if err != nil {
		t.Fatal(err)
	}
	vs.OnKeyFrameRequest(func() { p.keyFrames.Add(1) })
	if _, err := p.pc.AddTrack(p.audio); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.halt)
	return p
}

// send writes a video packet and an audio one every 10ms until halted:
// the video's payload is its sequence number's low byte, repeated.
func (p *publisher) send() {
	go func() {
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		var seq uint16
		for {
			select {
			case <-p.stop:
				return
			case <-tick.C:
			}
			seq++
			v := rtp.Packet{Header: rtp.Header{PayloadType: 96, SequenceNumber: seq, Timestamp: uint32(seq) * 3000, SSRC: 1, Marker: true},
				Payload: append([]byte{0x10, 0x02, 0x00, 0x9d, 0x01, 0x2a}, strings.Repeat(string(rune('A'+seq%26)), 400)...)}
			b, _ := v.Append(nil)
			_ = p.video.WriteRTP(b)
			a := rtp.Packet{Header: rtp.Header{PayloadType: 111, SequenceNumber: seq, Timestamp: uint32(seq) * 480, SSRC: 2},
				Payload: []byte("opus-frame")}
			b, _ = a.Append(nil)
			_ = p.audio.WriteRTP(b)
		}
	}()
}

func (p *publisher) halt() { p.stopOnce.Do(func() { close(p.stop) }) }

// viewer is a peer that receives audio and video, and counts the packets.
type viewer struct {
	*peer
	video, audio atomic.Int64
	videoTrack   atomic.Pointer[webrtc.TrackRemote]
	badPayload   atomic.Int64
}

func newViewer(t *testing.T, e *fib.Engine) *viewer {
	t.Helper()
	v := &viewer{peer: newPeer(t, e)}
	for _, k := range []webrtc.MediaKind{webrtc.MediaKindVideo, webrtc.MediaKindAudio} {
		if _, err := v.pc.AddTransceiver(k, webrtc.DirectionRecvOnly); err != nil {
			t.Fatal(err)
		}
	}
	v.pc.OnTrack(func(tr *webrtc.TrackRemote) {
		kind := tr.Kind()
		if kind == webrtc.MediaKindVideo {
			v.videoTrack.Store(tr)
		}
		tr.OnRTP(func(b []byte) {
			var p rtp.Packet
			if p.Unmarshal(b) != nil {
				v.badPayload.Add(1)
				return
			}
			if kind == webrtc.MediaKindVideo {
				if len(p.Payload) != 406 || p.Payload[6] != byte('A'+p.SequenceNumber%26) {
					v.badPayload.Add(1)
				}
				v.video.Add(1)
			} else {
				v.audio.Add(1)
			}
		})
	})
	return v
}

// waitPackets waits for v to have n more video and audio packets than it
// had.
func (v *viewer) waitPackets(t *testing.T, n int64) {
	t.Helper()
	vid, aud := v.video.Load()+n, v.audio.Load()+n
	eventually(t, fmt.Sprintf("%d packets", n), func() bool { return v.video.Load() >= vid && v.audio.Load() >= aud })
	if b := v.badPayload.Load(); b > 0 {
		t.Fatalf("%d packets were not what the publisher sent", b)
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func metrics(s *Server) string {
	w := &observe.TextWriter{}
	s.WriteMetrics(w)
	return string(w.B)
}
