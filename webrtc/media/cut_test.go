package media

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fib/webrtc"
)

// A publisher cut, its peer gone silent, is ended at ice_timeout, and its
// viewers with it.
func TestPublisherCut(t *testing.T) {
	f := startFit(t, doc(loopbackMedia))
	pubEngine, viewEngine := startEngine(t), startEngine(t)
	pub := newPublisher(t, pubEngine)
	if resp, body := pub.signal(t, f.base+"/whip/cut"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	pub.waitConnected(t)
	pub.send()
	v := newViewer(t, viewEngine)
	if resp, body := v.signal(t, f.base+"/whep/cut"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	v.waitPackets(t, 20)

	pub.halt()
	stopEngine(pubEngine)
	v.waitEnded(t)
	eventually(t, "no sessions", func() bool { p, v := f.media().Sessions(); return p == 0 && v == 0 })
	if m := metrics(f.media()); !strings.Contains(m, `reason="failed"} 1`) || !strings.Contains(m, `reason="publisher_left"} 1`) {
		t.Fatalf("metrics:\n%s", m)
	}
}

// A viewer cut does not hold the publisher or the other viewers: they go
// on, and its session ends at ice_timeout. A viewer that floods key frame
// requests reaches the publisher at most once a key_frame_interval.
func TestViewerCutAndFlood(t *testing.T) {
	f := startFit(t, doc(`{"listen": "127.0.0.1:0", "ice_timeout": "2s", "key_frame_interval": "500ms"}`))
	clients, cutEngine := startEngine(t), startEngine(t)
	pub := newPublisher(t, clients)
	if resp, body := pub.signal(t, f.base+"/whip/s"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	pub.waitConnected(t)
	pub.send()
	good, cut := newViewer(t, clients), newViewer(t, cutEngine)
	for _, v := range []*viewer{good, cut} {
		if resp, body := v.signal(t, f.base+"/whep/s"); resp.StatusCode != http.StatusCreated {
			t.Fatalf("%d %s", resp.StatusCode, body)
		}
		v.waitPackets(t, 20)
	}
	stopEngine(cutEngine)
	eventually(t, "the cut viewer's end", func() bool { _, v := f.media().Sessions(); return v == 1 })
	good.waitPackets(t, 50)
	if p, _ := f.media().Sessions(); p != 1 {
		t.Fatal("the publisher is gone")
	}

	// A flood of PLIs for 1.2 seconds: three at most pass, one each 500ms.
	time.Sleep(600 * time.Millisecond)
	before := pub.keyFrames.Load()
	tr := good.videoTrack.Load()
	for end := time.Now().Add(1200 * time.Millisecond); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		_ = tr.RequestKeyFrame()
	}
	time.Sleep(200 * time.Millisecond)
	if got := pub.keyFrames.Load() - before; got < 1 || got > 3 {
		t.Fatalf("%d key frame requests reached the publisher", got)
	}
	if f.media().m.keyFramesHeld.Load() < 50 {
		t.Fatalf("%d requests held back", f.media().m.keyFramesHeld.Load())
	}
}

// A session whose peer never connects ends at connect_timeout.
func TestConnectTimeout(t *testing.T) {
	f := startFit(t, doc(`{"listen": "127.0.0.1:0", "connect_timeout": "500ms"}`))
	clients := startEngine(t)
	p := newPeer(t, clients)
	if _, err := p.pc.AddTransceiver(webrtc.MediaKindVideo, webrtc.DirectionSendOnly); err != nil {
		t.Fatal(err)
	}
	tr := webrtc.NewTrackLocal(webrtc.Codec{MimeType: "video/VP8", ClockRate: 90000}, "", "")
	if _, err := p.pc.AddTrack(tr); err != nil {
		t.Fatal(err)
	}
	offer, err := webrtc.Offer(t.Context(), p.pc, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The answer is never set, so the peer never checks.
	if resp, body := post(t, f.base+"/whip/late", "application/sdp", offer.SDP); resp.StatusCode != http.StatusCreated {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	eventually(t, "the session's end", func() bool { p, _ := f.media().Sessions(); return p == 0 })
	if m := metrics(f.media()); !strings.Contains(m, `reason="connect_timeout"} 1`) {
		t.Fatalf("metrics:\n%s", m)
	}
	if resp, body := newPublisher(t, clients).signal(t, f.base+"/whip/late"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("the name after the timeout: %d %s", resp.StatusCode, body)
	}
}

// A reload of limits keeps the sessions; one of public_ips starts the media
// server again, ending them; one without webrtc.media stops it.
func TestReload(t *testing.T) {
	f := startFit(t, doc(loopbackMedia))
	clients := startEngine(t)
	pub := newPublisher(t, clients)
	if resp, body := pub.signal(t, f.base+"/whip/r"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	pub.waitConnected(t)
	pub.send()
	v := newViewer(t, clients)
	if resp, body := v.signal(t, f.base+"/whep/r"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	v.waitPackets(t, 20)
	srv := f.media()

	f.reload(t, doc(`{"listen": "127.0.0.1:0", "ice_timeout": "2s", "max_viewers": 1}`))
	if f.media() != srv {
		t.Fatal("the role was made again")
	}
	v.waitPackets(t, 30)
	if resp, _ := newViewer(t, clients).signal(t, f.base+"/whep/r"); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("max_viewers after the reload: %d", resp.StatusCode)
	}

	f.reload(t, doc(`{"listen": "127.0.0.1:0", "public_ips": ["127.0.0.1"], "ice_timeout": "2s"}`))
	pub.waitEnded(t)
	v.waitEnded(t)
	if p, vs := srv.Sessions(); p != 0 || vs != 0 {
		t.Fatalf("sessions after a restart: %d %d", p, vs)
	}
	if m := metrics(srv); !strings.Contains(m, "fit_webrtc_media_restarts_total 1") || !strings.Contains(m, `reason="reload"} 2`) {
		t.Fatalf("metrics:\n%s", m)
	}
	pub2 := newPublisher(t, clients)
	if resp, body := pub2.signal(t, f.base+"/whip/r"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("after the restart: %d %s", resp.StatusCode, body)
	}
	pub2.waitConnected(t)

	f.reload(t, `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
		"routes": [{"match": {"path": ["/whip/*"]}, "handler": {"module": "whip"}}]}`)
	pub2.waitEnded(t)
	if f.media() != nil {
		t.Fatal("the role runs without its member")
	}
	if resp, _ := post(t, f.base+"/whip/r", "application/sdp", "v=0"); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("without webrtc.media: %d", resp.StatusCode)
	}
}
