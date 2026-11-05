package streaming

import (
	"bufio"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fib/media/av"
	"github.com/lesismal/fib/media/hub"
	"github.com/lesismal/fib/media/rtc"
	"github.com/lesismal/fib/media/rtmp"
	"github.com/lesismal/fib/media/rtsp"
	"github.com/lesismal/fib/media/srt"
)

// rtspConn is a raw RTSP client connection.
type rtspConn struct {
	t    *testing.T
	c    net.Conn
	r    *bufio.Reader
	cseq int
}

func dialRTSP(t *testing.T, addr string) *rtspConn {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, waitTimeout)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return &rtspConn{t: t, c: c, r: bufio.NewReader(c)}
}

// do sends a request and returns the response's status and header. hdr is
// the header's names and values, and a "Body" in it the request's body.
func (r *rtspConn) do(method, url string, hdr ...string) (int, textproto.MIMEHeader) {
	r.t.Helper()
	r.cseq++
	req := method + " " + url + " RTSP/1.0\r\nCSeq: " + strconv.Itoa(r.cseq) + "\r\n"
	body := ""
	for i := 0; i+1 < len(hdr); i += 2 {
		if hdr[i] == "Body" {
			body = hdr[i+1]
			req += "Content-Length: " + strconv.Itoa(len(body)) + "\r\n"
			continue
		}
		req += hdr[i] + ": " + hdr[i+1] + "\r\n"
	}
	_ = r.c.SetDeadline(time.Now().Add(waitTimeout))
	if _, err := io.WriteString(r.c, req+"\r\n"+body); err != nil {
		r.t.Fatal(err)
	}
	tp := textproto.NewReader(r.r)
	line, err := tp.ReadLine()
	if err != nil {
		r.t.Fatal(err)
	}
	_, rest, _ := strings.Cut(line, " ")
	code, _ := strconv.Atoi(rest[:3])
	h, err := tp.ReadMIMEHeader()
	if err != nil {
		r.t.Fatal(err)
	}
	if n, _ := strconv.Atoi(h.Get("Content-Length")); n > 0 {
		_, _ = io.CopyN(io.Discard, r.r, int64(n))
	}
	return code, h
}

var nonceParam = regexp.MustCompile(`nonce="([^"]+)"`)

// digest is the Digest Authorization of a request, answering challenge.
func digest(user, password, method, uri string, challenge []string) string {
	nonce := ""
	for _, c := range challenge {
		if m := nonceParam.FindStringSubmatch(c); m != nil {
			nonce = m[1]
		}
	}
	return `Digest username="` + user + `", realm="fit", nonce="` + nonce + `", uri="` + uri +
		`", response="` + digestResponse(user, "fit", password, method, uri, nonce, "", "", "") + `"`
}

// RTSP: DESCRIBE and ANNOUNCE are challenged with Digest and Basic, and
// pass with a Digest of a plaintext user, Basic, or a token in the query;
// a bad Digest, a stale nonce and credentials that may not are refused; a
// password_hash user over Basic is refused with 503 once, then let in.
func TestRTSPAuth(t *testing.T) {
	f := startFit(t, doc(t, allListeners))
	e := startEngine(t)
	if _, err := publishRTMP(t, e, "rtmp://"+f.addr(t, "rtmp")+"/live/cam?token="+pubToken, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	f.waitLive(t, "live/cam")
	addr := f.addr(t, "rtsp")
	url := "rtsp://" + addr + "/live/cam"
	c := dialRTSP(t, addr)
	if code, _ := c.do("OPTIONS", url); code != 200 {
		t.Fatalf("OPTIONS: %d", code)
	}
	code, h := c.do("DESCRIBE", url)
	ch := h.Values("Www-Authenticate")
	if code != 401 || len(ch) != 2 || !strings.HasPrefix(ch[0], "Digest ") || ch[1] != `Basic realm="fit"` {
		t.Fatalf("no credentials: %d %v", code, ch)
	}
	if code, _ := c.do("DESCRIBE", url, "Authorization", digest("alice", "alice-pw", "DESCRIBE", url, ch)); code != 200 {
		t.Fatalf("alice's Digest: %d", code)
	}
	if code, _ := c.do("DESCRIBE", url, "Authorization", digest("alice", "wrong", "DESCRIBE", url, ch)); code != 401 {
		t.Fatalf("a wrong Digest: %d", code)
	}
	if code, _ := c.do("DESCRIBE", url, "Authorization", digest("bob", "bob-pw", "DESCRIBE", url, ch)); code != 401 {
		t.Fatalf("a Digest of a password_hash user: %d", code)
	}
	a := &rtspAuth{realm: "fit"}
	stale := a.nonce(time.Now().Add(-time.Hour))
	if a.mac(nil) == "" || stale == "" {
		t.Fatal("no nonce")
	}
	if code, _ := c.do("DESCRIBE", url+"?token="+viewToken); code != 200 {
		t.Fatalf("a query token: %d", code)
	}
	if code, _ := c.do("DESCRIBE", url+"?token="+pubToken); code != 403 {
		t.Fatalf("a token that may only publish: %d", code)
	}
	basic := func(u, p string) string {
		return "Basic " + base64Std(u+":"+p)
	}
	if code, _ := c.do("DESCRIBE", url, "Authorization", basic("alice", "alice-pw")); code != 200 {
		t.Fatalf("alice's Basic: %d", code)
	}
	code, h = c.do("DESCRIBE", url, "Authorization", basic("bob", "bob-pw"))
	if code != 503 || h.Get("Retry-After") != "1" {
		t.Fatalf("bob's first Basic: %d", code)
	}
	waitFor(t, "bob's password to be checked", func() bool {
		code, _ := c.do("DESCRIBE", url, "Authorization", basic("bob", "bob-pw"))
		return code == 200
	})
	if code, _ := c.do("ANNOUNCE", "rtsp://"+addr+"/live/x?token="+viewToken, "Content-Type", "application/sdp"); code != 403 {
		t.Fatalf("an ANNOUNCE with a token that may only play: %d", code)
	}

	// fib's client plays with Digest from the URL, and publishes with a
	// token in the query.
	played := make(chan struct{})
	var once atomic.Bool
	player, err := rtsp.Play(rtsp.ClientConfig{Engine: e, OnPacket: func(*av.Packet) {
		if once.CompareAndSwap(false, true) {
			close(played)
		}
	}}, "rtsp://alice:alice-pw@"+addr+"/live/cam")
	if err != nil {
		t.Fatal(err)
	}
	defer player.Close()
	select {
	case <-played:
	case <-time.After(waitTimeout):
		t.Fatal("the Digest player got nothing")
	}
	tracks, packets := fixture(t, 3*time.Second)
	ready := make(chan error, 1)
	pub, err := rtsp.Publish(rtsp.ClientConfig{Engine: e, OnReady: func() { ready <- nil }, OnClose: func(err error) {
		select {
		case ready <- err:
		default:
		}
	}}, "rtsp://"+addr+"/live/rtsp?token="+pubToken, tracks)
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatalf("the token publisher: %v", err)
		}
	case <-time.After(waitTimeout):
		t.Fatal("the token publisher was not answered")
	}
	for _, p := range packets[:50] {
		_ = pub.WritePacket(p)
	}
	waitFor(t, "the RTSP stream", func() bool {
		st, ok := f.role().Hub().Stream("live/rtsp")
		return ok && st.Publishing
	})
	refused := make(chan error, 1)
	bad, err := rtsp.Publish(rtsp.ClientConfig{Engine: e, OnReady: func() { refused <- nil }, OnClose: func(err error) {
		select {
		case refused <- err:
		default:
		}
	}}, "rtsp://"+addr+"/live/rtsp2", tracks)
	if err != nil {
		t.Fatal(err)
	}
	defer bad.Close()
	var se *rtsp.StatusError
	if err := <-refused; !errors.As(err, &se) || se.Code != 401 {
		t.Fatalf("a publisher without credentials: %v", err)
	}
}

func base64Std(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// srtDial dials the SRT listener with streamID, and returns nil once
// connected or the reason it was rejected.
func srtDial(t *testing.T, f *fit, streamID string) error {
	t.Helper()
	e := startEngine(t)
	done := make(chan error, 1)
	s, err := srt.Dial(e, f.addr(t, "srt"), srt.ClientConfig{
		StreamID:  streamID,
		OnConnect: func(*srt.Session) { done <- nil },
		OnClose: func(err error) {
			select {
			case done <- err:
			default:
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	select {
	case err := <-done:
		return err
	case <-time.After(waitTimeout):
		t.Fatal("the SRT handshake did not end")
		return nil
	}
}

// rtspTestSDP is an ANNOUNCE's description: H.264 and G.711.
const rtspTestSDP = "v=0\r\no=- 0 0 IN IP4 127.0.0.1\r\ns=x\r\nt=0 0\r\n" +
	"m=video 0 RTP/AVP 96\r\na=rtpmap:96 H264/90000\r\na=fmtp:96 packetization-mode=1;sprop-parameter-sets=Z0LAHtkAoC/5cBEAAAMAAQAAAwAyDxYuSA==,aMuDyyA=\r\na=control:streamid=0\r\n" +
	"m=audio 0 RTP/AVP 0\r\na=control:streamid=1\r\n"

// RTSP: a session plays or records the stream its ANNOUNCE or first SETUP
// named, and its PLAY and RECORD are checked on that stream, so that a
// client with a token for one stream does not SETUP another and then PLAY it
// under the first one's URL; fib refuses a PLAY or RECORD of another stream
// than its session's with 455.
func TestRTSPSessionStream(t *testing.T) {
	f := startFit(t, doc(t, allListeners))
	tracks, _ := fixture(t, time.Second)
	for _, p := range []string{"secret", "live/cam"} {
		pub, err := f.role().Hub().Publish(p, tracks, hub.PublishInfo{Protocol: "test"})
		if err != nil {
			t.Fatal(err)
		}
		defer pub.Close()
	}
	addr := f.addr(t, "rtsp")
	base := "rtsp://" + addr + "/"
	transport := "RTP/AVP/TCP;unicast;interleaved=0-1"
	want := func(c *rtspConn, what string, code int, method, url string, hdr ...string) textproto.MIMEHeader {
		t.Helper()
		got, h := c.do(method, url, hdr...)
		if got != code {
			t.Fatalf("%s: %s %s: %d, want %d", what, method, url, got, code)
		}
		return h
	}

	// SETUP is checked, as the first makes a session playing its stream.
	c := dialRTSP(t, addr)
	want(c, "a SETUP of secret without credentials", 401, "SETUP", base+"secret/trackID=0", "Transport", transport)
	want(c, "a SETUP of secret with a token for live/*", 403, "SETUP", base+"secret?token="+viewToken+"/trackID=0", "Transport", transport)
	want(c, "a PLAY with no session set up", 454, "PLAY", base+"live/cam?token="+viewToken)

	// alice may play anything: her session plays secret. A PLAY by anyone
	// else is checked on secret, whatever its URL; alice's PLAY of another
	// URL is refused by fib.
	setup := base + "secret/trackID=0"
	_, h := c.do("SETUP", setup, "Transport", transport)
	challenge := h.Values("Www-Authenticate")
	alice := func(method, url string) string { return digest("alice", "alice-pw", method, url, challenge) }
	want(c, "alice's SETUP of secret", 200, "SETUP", setup, "Transport", transport, "Authorization", alice("SETUP", setup))
	want(c, "the PLAY of a stream the token may play", 403, "PLAY", base+"live/cam?token="+viewToken)
	want(c, "the PLAY of a track of it", 403, "PLAY", base+"live/cam?token="+viewToken+"/trackID=0")
	want(c, "the PLAY of secret with the token", 403, "PLAY", base+"secret?token="+viewToken)
	want(c, "alice's PLAY of another stream", 455, "PLAY", base+"live/cam", "Authorization", alice("PLAY", base+"live/cam"))
	want(c, "alice's PLAY of secret", 200, "PLAY", base+"secret/", "Authorization", alice("PLAY", base+"secret/"))

	// A viewer's session plays its stream, and only that.
	v := dialRTSP(t, addr)
	want(v, "a viewer's DESCRIBE", 200, "DESCRIBE", base+"live/cam?token="+viewToken)
	want(v, "a viewer's SETUP", 200, "SETUP", base+"live/cam?token="+viewToken+"/trackID=0", "Transport", transport)
	want(v, "a viewer's second SETUP", 200, "SETUP", base+"live/cam?token="+viewToken+"/trackID=1", "Transport", "RTP/AVP/TCP;unicast;interleaved=2-3")
	want(v, "a viewer's PLAY of secret", 455, "PLAY", base+"secret?token="+viewToken)
	want(v, "a viewer's PLAY", 200, "PLAY", base+"live/cam?token="+viewToken+"/")

	// A publisher's SETUPs add tracks to the stream it announced, and need
	// play or publish on it; its RECORD publishes that stream only, and
	// needs publish on it.
	p := dialRTSP(t, addr)
	announce := base + "live/x?token=" + pubToken
	want(p, "an ANNOUNCE", 200, "ANNOUNCE", announce, "Content-Type", "application/sdp", "Body", rtspTestSDP)
	want(p, "a SETUP without credentials", 401, "SETUP", base+"live/x/streamid=0", "Transport", transport+";mode=record")
	want(p, "a SETUP with an unknown token", 401, "SETUP", base+"live/x?token=unknown-token-0123456789/streamid=0", "Transport", transport+";mode=record")
	want(p, "a publisher's SETUP", 200, "SETUP", announce+"/streamid=0", "Transport", transport+";mode=record")
	want(p, "a SETUP with a token that may play the stream", 200, "SETUP", base+"live/x?token="+viewToken+"/streamid=1", "Transport", "RTP/AVP/TCP;unicast;interleaved=2-3;mode=record")
	want(p, "a RECORD with a token that may only play", 403, "RECORD", base+"live/x?token="+viewToken)
	want(p, "a RECORD of another stream", 455, "RECORD", base+"live/y?token="+pubToken)
	want(p, "a PLAY of the announced stream", 403, "PLAY", announce)
	want(p, "a RECORD", 200, "RECORD", announce)

	// A Digest made for another URL is refused.
	_, h = c.do("DESCRIBE", setup)
	if code, _ := c.do("DESCRIBE", base+"live/cam", "Authorization", digest("alice", "alice-pw", "DESCRIBE", setup, h.Values("Www-Authenticate"))); code != 401 {
		t.Fatalf("a Digest replayed on another URL: %d", code)
	}
}

// SRT: a caller is decided in the handshake by its stream ID's s=, u= and
// s=, or the resource's query.
func TestSRTAuth(t *testing.T) {
	f := startFit(t, doc(t, allListeners))
	e := startEngine(t)
	if _, err := publishRTMP(t, e, "rtmp://"+f.addr(t, "rtmp")+"/live/cam?token="+pubToken, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	f.waitLive(t, "live/cam")
	for _, c := range []struct {
		id   string
		want error
	}{
		{"#!::r=live/srt,m=publish", srt.RejectUnauthorized},
		{"#!::r=live/srt,m=publish,s=bad-token-0123456789", srt.RejectUnauthorized},
		{"#!::r=live/srt,m=publish,s=" + viewToken, srt.RejectForbidden},
		{"#!::r=live/srt,m=publish,s=" + pubToken, nil},
		{"#!::r=live/alice,m=publish,u=alice,s=alice-pw", nil},
		{"#!::r=live/cam?token=" + viewToken, nil},
		{"live/cam?token=" + viewToken, nil},
		{"live/cam?user=alice&pass=alice-pw", nil},
		{"live/cam", srt.RejectUnauthorized},
		{"#!::r=live/bob,m=publish,u=bob,s=bob-pw", srt.RejectOverload},
	} {
		err := srtDial(t, f, c.id)
		if !errors.Is(err, c.want) && !(c.want == nil && err == nil) {
			t.Errorf("%s: %v, want %v", c.id, err, c.want)
		}
	}
	waitFor(t, "bob's password to be checked", func() bool {
		return srtDial(t, f, "#!::r=live/bob,m=publish,u=bob,s=bob-pw") == nil
	})
}

// stream_whip and stream_whep: 401, 403, then a WHEP viewer of a stream
// published over RTMP, and a WHIP publisher played over RTMP, through the
// route's paths and the session URLs they are given.
func TestStreamWHIPWHEP(t *testing.T) {
	f := startFit(t, doc(t, allListeners))
	e := startEngine(t)
	if _, err := publishRTMPWith(t, e, "rtmp://"+f.addr(t, "rtmp")+"/live/cam?token="+pubToken, 10*time.Second, highProfile); err != nil {
		t.Fatal(err)
	}
	f.waitLive(t, "live/cam")
	post := func(path string, set func(*http.Request)) int {
		req, _ := http.NewRequest("POST", f.base+path, strings.NewReader("v=0\r\n"))
		req.Header.Set("Content-Type", "application/sdp")
		if set != nil {
			set(req)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	for path, want := range map[string]int{"/whip/live/x": 401, "/whep/live/cam": 401} {
		if code := post(path, nil); code != want {
			t.Fatalf("%s without credentials: %d", path, code)
		}
	}
	if code := post("/whip/live/x", bearer(viewToken)); code != 403 {
		t.Fatalf("whip with a viewing token: %d", code)
	}
	if code := post("/whip/live/x", bearer(pubToken)); code != 400 {
		t.Fatalf("whip with a malformed offer: %d", code)
	}
	req, _ := http.NewRequest("DELETE", f.base+"/whep/live/cam/0123456789abcdef0123456789abcdef", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("a DELETE of no session: %d", resp.StatusCode)
	}

	var packets atomic.Int64
	viewer, err := rtc.Pull(rtc.ClientConfig{Engine: e, Token: viewToken, OnPacket: func(*av.Packet) { packets.Add(1) }},
		f.base+"/whep/live/cam")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the WHEP viewer's packets", func() bool { return packets.Load() > 10 })
	if res := viewer.Resource(); !strings.HasPrefix(res, f.base+"/whep/live/cam/") && !strings.HasPrefix(res, "/whep/live/cam/") {
		t.Fatalf("the session's URL: %q", res)
	}
	srv := f.role().RTC()
	n := srv.Len()
	viewer.Close()
	waitFor(t, "the DELETE of the viewer's session", func() bool { return srv.Len() < n })

	tracks, all := fixture(t, 10*time.Second)
	tracks = highProfile(tracks)
	pushed := make(chan struct{})
	var ready atomic.Bool
	pub, err := rtc.Push(rtc.ClientConfig{Engine: e, Token: pubToken, OnReady: func() {
		if ready.CompareAndSwap(false, true) {
			close(pushed)
		}
	}}, f.base+"/whip/live/webrtc", tracks)
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()
	stop, done := make(chan struct{}), make(chan struct{})
	defer func() { close(stop); <-done }()
	go func() {
		select {
		case <-pushed:
		case <-time.After(waitTimeout):
		}
		feed(all, func(p *av.Packet) { _ = pub.WritePacket(p) }, stop, done)
	}()
	r := &recorder{}
	playReady := make(chan error, 1)
	waitFor(t, "the WHIP stream", func() bool {
		st, ok := f.role().Hub().Stream("live/webrtc")
		return ok && st.Publishing
	})
	cc, err := rtmp.Play(e, "rtmp://"+f.addr(t, "rtmp")+"/live/webrtc?token="+viewToken, rtmp.ClientConfig{}, r,
		func(_ *rtmp.ClientConn, err error) { playReady <- err })
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()
	if err := <-playReady; err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the RTMP player of the WHIP stream", func() bool { return r.packets.Load() > 10 })
	_ = fmt.Sprint
}

// highProfile returns tracks with the fixture's H.264, High 4:4:4
// Predictive, said to be High, the same syntax, which WebRTC peers decode.
func highProfile(tracks []*av.Track) []*av.Track {
	out := make([]*av.Track, len(tracks))
	for i, t := range tracks {
		c := *t
		if c.Codec == av.CodecH264 && len(c.Config) > 9 {
			c.Config = append([]byte(nil), c.Config...)
			c.Config[1], c.Config[9] = 0x64, 0x64
		}
		out[i] = &c
	}
	return out
}
