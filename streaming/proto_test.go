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

// do sends a request and returns the response's status and header.
func (r *rtspConn) do(method, url string, hdr ...string) (int, textproto.MIMEHeader) {
	r.t.Helper()
	return r.doBody(method, url, "", hdr...)
}

// doBody is do with a body.
func (r *rtspConn) doBody(method, url, body string, hdr ...string) (int, textproto.MIMEHeader) {
	r.t.Helper()
	r.cseq++
	req := method + " " + url + " RTSP/1.0\r\nCSeq: " + strconv.Itoa(r.cseq) + "\r\n"
	for i := 0; i+1 < len(hdr); i += 2 {
		req += hdr[i] + ": " + hdr[i+1] + "\r\n"
	}
	if body != "" {
		req += "Content-Length: " + strconv.Itoa(len(body)) + "\r\n"
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

// RTSP: what a session plays or records is checked by its own stream, not
// by the URLs of its requests. fib's server sets a session up for the
// stream a SETUP's URL names, described or not, and PLAY plays that
// session whatever its URL: a SETUP of a stream the client may not play is
// refused, so that a PLAY of a URL its token may play cannot play it; a
// PLAY or RECORD without credentials is refused even of a session set up
// with them.
func TestRTSPSessionStream(t *testing.T) {
	f := startFit(t, doc(t, allListeners))
	e := startEngine(t)
	for _, p := range []string{"live/a", "live/secret"} {
		if _, err := publishRTMP(t, e, "rtmp://"+f.addr(t, "rtmp")+"/"+p+"?token="+pubToken, 20*time.Second); err != nil {
			t.Fatal(err)
		}
		f.waitLive(t, p)
	}
	addr := f.addr(t, "rtsp")
	base := "rtsp://" + addr + "/"
	tcp := []string{"Transport", "RTP/AVP/TCP;unicast;interleaved=0-1"}

	// The attack: SETUP of live/secret without credentials, then PLAY of
	// live/a, which the token may play, in that session.
	c := dialRTSP(t, addr)
	code, h := c.do("SETUP", base+"live/secret/trackID=0", tcp...)
	if code != 403 {
		t.Fatalf("a SETUP of live/secret without credentials: %d", code)
	}
	sess, _, _ := strings.Cut(h.Get("Session"), ";")
	if code, _ := c.do("PLAY", base+"live/a?token="+viewAToken, "Session", sess); code != 454 {
		t.Fatalf("a PLAY of live/a in the session of live/secret: %d", code)
	}
	// The same after a DESCRIBE of live/a, the token on the SETUP, in its
	// query before or after the control.
	c = dialRTSP(t, addr)
	if code, _ := c.do("DESCRIBE", base+"live/a?token="+viewAToken); code != 200 {
		t.Fatalf("a DESCRIBE of live/a with its token: %d", code)
	}
	for _, u := range []string{base + "live/secret/trackID=0?token=" + viewAToken, base + "live/secret?token=" + viewAToken + "/trackID=0"} {
		if code, _ := c.do("SETUP", u, tcp...); code != 403 {
			t.Fatalf("a SETUP of %s: %d", u, code)
		}
	}
	if code, _ := c.do("PLAY", base+"live/a?token="+viewAToken); code != 454 {
		t.Fatalf("a PLAY with no session: %d", code)
	}

	// The token plays live/a as players do, by the Content-Base.
	play := func(withToken bool) (*rtspConn, int) {
		c := dialRTSP(t, addr)
		code, h := c.do("DESCRIBE", base+"live/a?token="+viewAToken)
		cb := h.Get("Content-Base")
		if code != 200 || cb == "" {
			t.Fatalf("DESCRIBE: %d %q", code, cb)
		}
		code, h = c.do("SETUP", cb+"trackID=0", tcp...)
		sess, _, _ := strings.Cut(h.Get("Session"), ";")
		if code != 200 || sess == "" {
			t.Fatalf("SETUP: %d", code)
		}
		u := cb
		if !withToken {
			u = base + "live/a"
		}
		code, _ = c.do("PLAY", u, "Session", sess)
		return c, code
	}
	c, code = play(true)
	if code != 200 {
		t.Fatalf("PLAY: %d", code)
	}
	if b, err := c.r.ReadByte(); err != nil || b != '$' {
		t.Fatalf("no RTP after PLAY: %v %q", err, b)
	}
	// A PLAY without credentials: the session's stream is checked as its
	// subscription starts, after the 200, and the connection closed.
	c, _ = play(false)
	if n, err := io.Copy(io.Discard, c.r); err != nil || n != 0 {
		t.Fatalf("a PLAY without credentials got %d bytes: %v", n, err)
	}

	// RECORD publishes the session ANNOUNCE made, checked with RECORD's
	// credentials.
	c = dialRTSP(t, addr)
	sdp := "v=0\r\no=- 0 0 IN IP4 127.0.0.1\r\ns=x\r\nc=IN IP4 0.0.0.0\r\nt=0 0\r\n" +
		"m=audio 0 RTP/AVP 8\r\na=rtpmap:8 PCMA/8000\r\na=control:streamid=0\r\n"
	if code, _ := c.doBody("ANNOUNCE", base+"live/rec?token="+pubToken, sdp, "Content-Type", "application/sdp"); code != 200 {
		t.Fatalf("ANNOUNCE: %d", code)
	}
	code, h = c.do("SETUP", base+"live/rec?token="+pubToken+"/streamid=0", "Transport", "RTP/AVP/TCP;unicast;interleaved=0-1;mode=record")
	sess, _, _ = strings.Cut(h.Get("Session"), ";")
	if code != 200 {
		t.Fatalf("SETUP to record: %d", code)
	}
	if code, _ := c.do("RECORD", base+"live/rec", "Session", sess); code != 403 {
		t.Fatalf("a RECORD without credentials: %d", code)
	}
	if code, _ := c.do("RECORD", base+"live/rec?token="+viewToken, "Session", sess); code != 403 {
		t.Fatalf("a RECORD with a token that may only play: %d", code)
	}
	if code, _ := c.do("RECORD", base+"live/rec?token="+pubToken, "Session", sess); code != 200 {
		t.Fatalf("a RECORD with the token: %d", code)
	}
}

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
