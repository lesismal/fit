package media

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// A publisher's packets reach every viewer as it sent them; a viewer's key
// frame request reaches the publisher; DELETE ends a viewer, and the
// publisher's DELETE ends the stream and its viewers.
func TestPublishAndView(t *testing.T) {
	f := startFit(t, doc(loopbackMedia))
	clients := startEngine(t)

	pub := newPublisher(t, clients)
	resp, body := pub.signal(t, f.base+"/whip/cam1")
	if resp.StatusCode != http.StatusCreated || resp.Header.Get("Content-Type") != "application/sdp" ||
		!strings.HasPrefix(pub.location, "/whip/cam1/") || !strings.Contains(body, "a=ice-lite") {
		t.Fatalf("%d %v %s", resp.StatusCode, resp.Header, body)
	}
	pub.waitConnected(t)
	pub.send()

	const n = 3
	viewers := make([]*viewer, n)
	for i := range viewers {
		v := newViewer(t, clients)
		if resp, body := v.signal(t, f.base+"/whep/cam1"); resp.StatusCode != http.StatusCreated {
			t.Fatalf("viewer %d: %d %s", i, resp.StatusCode, body)
		}
		if !strings.HasPrefix(v.location, "/whep/cam1/") {
			t.Fatalf("Location %q", v.location)
		}
		viewers[i] = v
	}
	for _, v := range viewers {
		v.waitConnected(t)
		v.waitPackets(t, 30)
	}
	if p, v := f.media().Sessions(); p != 1 || v != n {
		t.Fatalf("sessions: %d publishers, %d viewers", p, v)
	}

	// A viewer's PLI reaches the publisher.
	eventually(t, "the publisher's first key frame request", func() bool { return pub.keyFrames.Load() > 0 })
	time.Sleep(50 * time.Millisecond)
	before := pub.keyFrames.Load()
	if err := viewers[0].videoTrack.Load().RequestKeyFrame(); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the viewer's PLI at the publisher", func() bool { return pub.keyFrames.Load() > before })

	// DELETE of a viewer: 200, then 404.
	if code := request(t, "DELETE", f.base+viewers[0].location); code != http.StatusOK {
		t.Fatalf("DELETE: %d", code)
	}
	if code := request(t, "DELETE", f.base+viewers[0].location); code != http.StatusNotFound {
		t.Fatalf("DELETE again: %d", code)
	}
	viewers[0].waitEnded(t)
	viewers[1].waitPackets(t, 20)
	// A viewer's session URL is not the publisher's.
	if code := request(t, "DELETE", f.base+"/whip/cam1/"+viewers[1].location[len("/whep/cam1/"):]); code != http.StatusNotFound {
		t.Fatalf("DELETE of a viewer through whip: %d", code)
	}

	// The publisher's DELETE ends the stream and its viewers.
	if code := request(t, "DELETE", f.base+pub.location); code != http.StatusOK {
		t.Fatalf("DELETE publisher: %d", code)
	}
	for _, v := range viewers[1:] {
		v.waitEnded(t)
	}
	eventually(t, "no sessions", func() bool { p, v := f.media().Sessions(); return p == 0 && v == 0 })
	m := metrics(f.media())
	for _, want := range []string{
		`fit_webrtc_media_sessions_total{role="publisher"} 1`,
		`fit_webrtc_media_sessions_total{role="viewer"} 3`,
		`fit_webrtc_media_sessions_ended_total{reason="delete"} 2`,
		`fit_webrtc_media_sessions_ended_total{reason="publisher_left"} 2`,
		`fit_webrtc_media_streams 0`,
	} {
		if !strings.Contains(m, want) {
			t.Errorf("metrics lack %s:\n%s", want, m)
		}
	}
	// The stream is gone: a viewer finds nothing, a publisher may take it.
	if resp, _ := newViewer(t, clients).signal(t, f.base+"/whep/cam1"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a viewer of an ended stream: %d", resp.StatusCode)
	}
	if resp, body := newPublisher(t, clients).signal(t, f.base+"/whip/cam1"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("publishing again: %d %s", resp.StatusCode, body)
	}
}
