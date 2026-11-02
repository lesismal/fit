package media

import (
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fib"
	"github.com/lesismal/fib/webrtc"
	"github.com/lesismal/fib/webrtc/rtp"
)

// newKindPublisher is a publisher of the kinds given, which sends nothing
// until told to.
func newKindPublisher(t *testing.T, e *fib.Engine, kinds ...webrtc.MediaKind) (*peer, map[webrtc.MediaKind]*webrtc.TrackLocal) {
	t.Helper()
	// Consent checks every 300ms, as a browser's are every few seconds
	// against the default ice_timeout of 30s.
	p := newPeerWith(t, webrtc.Configuration{Engine: e, ListenAddr: "127.0.0.1:0", ICEKeepaliveInterval: 300 * time.Millisecond})
	tracks := map[webrtc.MediaKind]*webrtc.TrackLocal{}
	for _, k := range kinds {
		c := webrtc.Codec{MimeType: "video/VP8", ClockRate: 90000}
		if k == webrtc.MediaKindAudio {
			c = webrtc.Codec{MimeType: "audio/opus", ClockRate: 48000, Channels: 2}
		}
		tr := webrtc.NewTrackLocal(c, string(k), "s")
		if _, err := p.pc.AddTrack(tr); err != nil {
			t.Fatal(err)
		}
		tracks[k] = tr
	}
	return p, tracks
}

// newKindViewer receives the kinds given, and counts the packets of each.
func newKindViewer(t *testing.T, e *fib.Engine, kinds ...webrtc.MediaKind) (*peer, map[webrtc.MediaKind]*atomic.Int64) {
	t.Helper()
	v := newPeerWith(t, webrtc.Configuration{Engine: e, ListenAddr: "127.0.0.1:0", ICEKeepaliveInterval: 300 * time.Millisecond})
	counts := map[webrtc.MediaKind]*atomic.Int64{webrtc.MediaKindAudio: new(atomic.Int64), webrtc.MediaKindVideo: new(atomic.Int64)}
	for _, k := range kinds {
		if _, err := v.pc.AddTransceiver(k, webrtc.DirectionRecvOnly); err != nil {
			t.Fatal(err)
		}
	}
	v.pc.OnTrack(func(tr *webrtc.TrackRemote) {
		c := counts[tr.Kind()]
		tr.OnRTP(func([]byte) { c.Add(1) })
	})
	return v, counts
}

func sendAudio(tr *webrtc.TrackLocal, n int) {
	for i := range n {
		a := rtp.Packet{Header: rtp.Header{PayloadType: 111, SequenceNumber: uint16(i), Timestamp: uint32(i) * 480, SSRC: 2},
			Payload: []byte("opus-frame")}
		b, _ := a.Append(nil)
		_ = tr.WriteRTP(b)
		time.Sleep(5 * time.Millisecond)
	}
}

// An audio-only publisher and a video-only one are played by viewers that
// offer both kinds, and by viewers that offer the other kind alone: the kind
// not sent is answered inactive, never refused, and what is sent arrives.
func TestOneKindPublishers(t *testing.T) {
	f := startFit(t, doc(loopbackMedia))
	clients := startEngine(t)

	pub, tracks := newKindPublisher(t, clients, webrtc.MediaKindAudio)
	if resp, body := pub.signal(t, f.base+"/whip/a"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("audio-only publisher: %d %s", resp.StatusCode, body)
	}
	pub.waitConnected(t)

	both, counts := newKindViewer(t, clients, webrtc.MediaKindVideo, webrtc.MediaKindAudio)
	resp, body := both.signal(t, f.base+"/whep/a")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("a viewer of both kinds: %d %s", resp.StatusCode, body)
	}
	if !videoInactive(body) {
		t.Fatalf("the video section is not inactive:\n%s", body)
	}
	both.waitConnected(t)
	go sendAudio(tracks[webrtc.MediaKindAudio], 200)
	eventually(t, "audio at the viewer", func() bool { return counts[webrtc.MediaKindAudio].Load() > 10 })

	// A viewer of video alone, of a publisher of audio alone: it has nothing
	// to play, which is a 422, not a 500.
	vonly, _ := newKindViewer(t, clients, webrtc.MediaKindVideo)
	if resp, body := vonly.signal(t, f.base+"/whep/a"); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a viewer of video alone: %d %s", resp.StatusCode, body)
	}

	// A publisher of both kinds, a viewer of audio alone: the video is not
	// sent to it, and the audio is.
	pub2, tracks2 := newKindPublisher(t, clients, webrtc.MediaKindAudio, webrtc.MediaKindVideo)
	if resp, body := pub2.signal(t, f.base+"/whip/av"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("publisher: %d %s", resp.StatusCode, body)
	}
	pub2.waitConnected(t)
	aonly, counts2 := newKindViewer(t, clients, webrtc.MediaKindAudio)
	if resp, body := aonly.signal(t, f.base+"/whep/av"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("a viewer of audio alone: %d %s", resp.StatusCode, body)
	}
	aonly.waitConnected(t)
	go sendAudio(tracks2[webrtc.MediaKindAudio], 200)
	eventually(t, "audio at the audio-only viewer", func() bool { return counts2[webrtc.MediaKindAudio].Load() > 10 })
}

// videoInactive reports whether an answer's video section is inactive, or
// rejected with port 0.
func videoInactive(answer string) bool {
	for _, sec := range strings.Split(answer, "m=")[1:] {
		if strings.HasPrefix(sec, "video") {
			return strings.HasPrefix(sec, "video 0 ") || strings.Contains(sec, "a=inactive")
		}
	}
	return true
}

// A publisher that sends nothing at all for longer than ice_timeout and
// connect_timeout, its microphone and camera switched off, stays, and so do
// its viewers: ICE consent keeps them, not media.
func TestSilentPublisher(t *testing.T) {
	f := startFit(t, doc(`{"listen": "127.0.0.1:0", "ice_timeout": "2s", "connect_timeout": "2s", "key_frame_interval": "10ms"}`))
	clients := startEngine(t)
	pub, tracks := newKindPublisher(t, clients, webrtc.MediaKindAudio, webrtc.MediaKindVideo)
	if resp, body := pub.signal(t, f.base+"/whip/quiet"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	pub.waitConnected(t)
	v, counts := newKindViewer(t, clients, webrtc.MediaKindVideo, webrtc.MediaKindAudio)
	if resp, body := v.signal(t, f.base+"/whep/quiet"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	v.waitConnected(t)
	time.Sleep(6 * time.Second)
	if p, n := f.media().Sessions(); p != 1 || n != 1 {
		t.Fatalf("after a silence: %d publishers, %d viewers, pub %s viewer %s\n%s", p, n, pub.pc.ConnectionState(), v.pc.ConnectionState(), metrics(f.media()))
	}
	if s := pub.pc.ConnectionState(); s != webrtc.PeerConnectionStateConnected {
		t.Fatalf("publisher %s", s)
	}
	if s := v.pc.ConnectionState(); s != webrtc.PeerConnectionStateConnected {
		t.Fatalf("viewer %s", s)
	}
	// Switched on again: the media flows.
	go sendAudio(tracks[webrtc.MediaKindAudio], 200)
	eventually(t, "audio after the silence", func() bool { return counts[webrtc.MediaKindAudio].Load() > 10 })
}
