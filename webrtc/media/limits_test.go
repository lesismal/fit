package media

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fib/webrtc"
	"github.com/lesismal/fib/webrtc/rtp"

	_ "github.com/lesismal/fit/webrtc/turn"
)

// The limits refuse with 503 and Retry-After, a stream taken with 409, and
// what is not an offer with 400, 413, 415 or 422; the endpoint and the
// session's URL say which methods they take.
func TestLimitsAndStatuses(t *testing.T) {
	f := startFit(t, doc(`{"listen": "127.0.0.1:0", "max_streams": 2, "max_viewers": 1, "max_peer_connections": 3, "max_sdp_bytes": 8192}`))
	clients := startEngine(t)

	if resp, body := newPublisher(t, clients).signal(t, f.base+"/whip/a"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if resp, _ := newPublisher(t, clients).signal(t, f.base+"/whip/a"); resp.StatusCode != http.StatusConflict {
		t.Fatalf("a stream published twice: %d", resp.StatusCode)
	}
	if resp, _ := newViewer(t, clients).signal(t, f.base+"/whep/nope"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a stream not published: %d", resp.StatusCode)
	}
	if resp, body := newViewer(t, clients).signal(t, f.base+"/whep/a"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	resp, _ := newViewer(t, clients).signal(t, f.base+"/whep/a")
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("a viewer past max_viewers: %d %v", resp.StatusCode, resp.Header)
	}
	if resp, body := newPublisher(t, clients).signal(t, f.base+"/whip/b"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	// Three PeerConnections now: a, its viewer, b.
	if resp, _ := newViewer(t, clients).signal(t, f.base+"/whep/b"); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("a viewer past max_peer_connections: %d", resp.StatusCode)
	}
	if resp, _ := newPublisher(t, clients).signal(t, f.base+"/whip/c"); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("a publisher past max_streams or max_peer_connections: %d", resp.StatusCode)
	}
	m := metrics(f.media())
	for _, want := range []string{`reason="viewers"} 1`, `reason="peer_connections"} 2`, `reason="conflict"} 1`, `reason="no_stream"} 1`} {
		if !strings.Contains(m, want) {
			t.Errorf("metrics lack %s:\n%s", want, m)
		}
	}

	for _, c := range []struct {
		method, path, ctype, body string
		status                    int
	}{
		{"POST", "/whip/x", "text/plain", "v=0", http.StatusUnsupportedMediaType},
		{"POST", "/whip/x", "application/sdp", strings.Repeat("a", 9000), http.StatusRequestEntityTooLarge},
		{"POST", "/whip/x", "application/sdp", "not sdp", http.StatusBadRequest},
		{"POST", "/whip/x", "application/sdp", "v=0\r\no=- 1 1 IN IP4 0.0.0.0\r\ns=-\r\nt=0 0\r\n", http.StatusUnprocessableEntity},
		{"POST", "/whip/bad%20name", "application/sdp", "v=0", http.StatusNotFound},
		{"GET", "/whip/x", "", "", http.StatusMethodNotAllowed},
		{"OPTIONS", "/whip/x", "", "", http.StatusNoContent},
		{"DELETE", "/whip/x", "", "", http.StatusMethodNotAllowed},
		{"DELETE", "/whip/x/0123456789abcdef0123456789abcdef", "", "", http.StatusNotFound},
		{"PATCH", "/whip/x/0123456789abcdef0123456789abcdef", "application/trickle-ice-sdpfrag", "a=x", http.StatusNotFound},
	} {
		req, _ := http.NewRequest(c.method, f.base+c.path, strings.NewReader(c.body))
		if c.ctype != "" {
			req.Header.Set("Content-Type", c.ctype)
		}
		if c.method == "POST" && c.body == strings.Repeat("a", 9000) {
			// Chunked, so that the limit is found as the body arrives.
			req.ContentLength = -1
		}
		resp, body := do(t, req)
		if resp.StatusCode != c.status {
			t.Errorf("%s %s: %d %s, want %d", c.method, c.path, resp.StatusCode, body, c.status)
		}
		if c.method == "OPTIONS" && resp.Header.Get("Accept-Post") != "application/sdp" {
			t.Errorf("OPTIONS: %v", resp.Header)
		}
	}

	// A session's URL: PATCH is 405, with what it takes.
	pub := newPublisher(t, clients)
	_ = f.media().End(Publisher, firstID(f.media(), "b"))
	if resp, body := pub.signal(t, f.base+"/whip/b"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	req, _ := http.NewRequest("PATCH", f.base+pub.location, strings.NewReader("a=ice-ufrag:x\r\n"))
	req.Header.Set("Content-Type", "application/trickle-ice-sdpfrag")
	resp, _ = do(t, req)
	if resp.StatusCode != http.StatusMethodNotAllowed || !strings.Contains(resp.Header.Get("Allow"), "DELETE") {
		t.Fatalf("PATCH: %d %v", resp.StatusCode, resp.Header)
	}
}

func firstID(s *Server, stream string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if str := s.streams[stream]; str != nil && str.pub != nil {
		return str.pub.id
	}
	return ""
}

// A route without webrtc.media answers 503.
func TestNoMediaServer(t *testing.T) {
	f := startFit(t, `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
		"routes": [{"match": {"path": ["/whip/*"]}, "handler": {"module": "whip", "params": {"stream": "fixed"}}}]}`)
	if resp, _ := post(t, f.base+"/whip/anything", "application/sdp", "v=0"); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("%d", resp.StatusCode)
	}
}

// A fixed stream name, ICE servers and fit's TURN server in Link headers,
// with time-limited credentials made from webrtc.turn's secret.
func TestLinksAndFixedStream(t *testing.T) {
	f := startFit(t, `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
		"routes": [{"match": {"path": ["/live"]}, "handler": {"module": "whip", "params": {"stream": "show"}}},
			{"match": {"path": ["/live/*"]}, "handler": {"module": "whip", "params": {"stream": "show"}}},
			{"match": {"path": ["/watch", "/watch/*"]}, "handler": {"module": "whep", "params": {"stream": "show"}}}],
		"webrtc": {
			"turn": {"listen": ["127.0.0.1:0"], "realm": "fit.test", "secret": "s3cret", "relay_ip": "127.0.0.1"},
			"media": {"listen": "127.0.0.1:0", "ice_servers": [{"urls": ["stun:stun.example.com:3478"]}, {"urls": ["turn:t.example.com"], "username": "u", "credential": "p"}],
				"turn_urls": ["turn:127.0.0.1:3478?transport=udp"], "turn_credential_ttl": "10m"}}}`)
	clients := startEngine(t)
	pub := newPublisher(t, clients)
	resp, body := pub.signal(t, f.base+"/live")
	if resp.StatusCode != http.StatusCreated || !strings.HasPrefix(pub.location, "/live/") {
		t.Fatalf("%d %s %s", resp.StatusCode, pub.location, body)
	}
	links := resp.Header.Values("Link")
	if len(links) != 3 || links[0] != `<stun:stun.example.com:3478>; rel="ice-server"` ||
		links[1] != `<turn:t.example.com>; rel="ice-server"; username="u"; credential="p"; credential-type="password"` {
		t.Fatalf("Link: %q", links)
	}
	m := regexp.MustCompile(`^<turn:127\.0\.0\.1:3478\?transport=udp>; rel="ice-server"; username="(\d+):fit-[0-9a-f]{16}"; credential="([^"]+)"; credential-type="password"$`).FindStringSubmatch(links[2])
	if m == nil {
		t.Fatalf("fit's TURN link: %q", links[2])
	}
	user := m[1] + links[2][strings.Index(links[2], `:fit-`):strings.Index(links[2], `"; credential=`)]
	mac := hmac.New(sha1.New, []byte("s3cret"))
	mac.Write([]byte(user))
	if base64.StdEncoding.EncodeToString(mac.Sum(nil)) != m[2] {
		t.Fatalf("the credential of %q is not the secret's", user)
	}
	if exp := time.Unix(atoi(m[1]), 0); time.Until(exp) < 9*time.Minute || time.Until(exp) > 11*time.Minute {
		t.Fatalf("expiry %v", exp)
	}
	pub.waitConnected(t)
	pub.send()
	v := newViewer(t, clients)
	if resp, body := v.signal(t, f.base+"/watch"); resp.StatusCode != http.StatusCreated || !strings.HasPrefix(v.location, "/watch/") {
		t.Fatalf("%d %s %s", resp.StatusCode, v.location, body)
	}
	v.waitPackets(t, 20)
	if code := request(t, "DELETE", f.base+pub.location); code != http.StatusOK {
		t.Fatalf("DELETE: %d", code)
	}
	v.waitEnded(t)
}

func atoi(s string) int64 {
	var n int64
	for _, c := range s {
		n = n*10 + int64(c-'0')
	}
	return n
}

// A publisher past max_bitrate has its packets dropped, and is told the
// limit with REMB.
func TestBitrate(t *testing.T) {
	f := startFit(t, doc(`{"listen": "127.0.0.1:0", "max_bitrate": 100000}`))
	clients := startEngine(t)
	pub := newPublisher(t, clients)
	remb := make(chan uint64, 4)
	pub.pc.OnRTCP(func(b []byte) {
		for rest := b; len(rest) > 0; {
			cur := rest
			h, _, r, err := rtp.NextRTCP(rest)
			if err != nil {
				return
			}
			rest = r
			if h.Type == rtp.TypePSFB && h.Count == rtp.FormatAFB {
				var m rtp.REMB
				if m.Unmarshal(cur[:len(cur)-len(r)]) == nil {
					select {
					case remb <- m.Bitrate:
					default:
					}
				}
			}
		}
	})
	if resp, body := pub.signal(t, f.base+"/whip/hd"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	pub.waitConnected(t)
	pub.send()
	select {
	case got := <-remb:
		if got > 100000 || got < 99000 {
			t.Fatalf("REMB of %d", got)
		}
	case <-time.After(waitTimeout):
		t.Fatal("no REMB")
	}
	eventually(t, "drops past max_bitrate", func() bool { return f.media().m.droppedBitrate.Load() > 0 })
}

// A viewer whose codecs are not the publisher's is refused with 422.
func TestViewerWithoutTheCodec(t *testing.T) {
	f := startFit(t, doc(`{"listen": "127.0.0.1:0"}`))
	clients := startEngine(t)
	if resp, body := newPublisher(t, clients).signal(t, f.base+"/whip/v"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{Engine: clients, ListenAddr: "127.0.0.1:0",
		Codecs: []webrtc.Codec{{MimeType: "video/AV1", ClockRate: 90000, PayloadType: 45}}})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	if _, err := pc.AddTransceiver(webrtc.MediaKindVideo, webrtc.DirectionRecvOnly); err != nil {
		t.Fatal(err)
	}
	offer, err := webrtc.Offer(t.Context(), pc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp, body := post(t, f.base+"/whep/v", "application/sdp", offer.SDP); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if _, v := f.media().Sessions(); v != 0 {
		t.Fatalf("%d viewers", v)
	}
	f.media().mu.Lock()
	pcs := f.media().pcs
	f.media().mu.Unlock()
	if pcs != 1 {
		t.Fatalf("%d PeerConnections counted, want the publisher's", pcs)
	}
}
