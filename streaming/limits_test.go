package streaming

import (
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fib/media/rtmp"
)

// playRTMP plays url into a recorder, and returns it or the refusal.
func playRTMP(t *testing.T, f *fit, path string) (*recorder, *rtmp.ClientConn, error) {
	t.Helper()
	e := startEngine(t)
	r := &recorder{}
	ready := make(chan error, 1)
	cc, err := rtmp.Play(e, "rtmp://"+f.addr(t, "rtmp")+path, rtmp.ClientConfig{}, r, func(_ *rtmp.ClientConn, err error) { ready <- err })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cc.Close() })
	select {
	case err := <-ready:
		return r, cc, err
	case <-time.After(waitTimeout):
		t.Fatal("the play was not answered")
		return nil, nil, nil
	}
}

// The limits: connections past max_conns are closed as they open,
// publishers past max_streams and players past max_players refused.
func TestLimits(t *testing.T) {
	f := startFit(t, doc(t, `{"max_streams": 1, "max_players": 1,
	  "rtmp": {"listen": "127.0.0.1:0", "max_conns": 3}}`))
	e := startEngine(t)
	url := "rtmp://" + f.addr(t, "rtmp")
	if _, err := publishRTMP(t, e, url+"/live/a", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	f.waitLive(t, "live/a")
	if _, err := publishRTMP(t, e, url+"/live/b", time.Second); err == nil {
		t.Fatal("a second stream past max_streams")
	}
	r, _, err := playRTMP(t, f, "/live/a")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the player's packets", func() bool { return r.packets.Load() > 0 })
	if _, _, err := playRTMP(t, f, "/live/a"); err == nil {
		t.Fatal("a second player past max_players")
	}
	// Two connections are open, the publisher and the player: one more
	// fits, the next is closed at once.
	waitFor(t, "the refused connections to close", func() bool { return f.role().rtmp.limit.n.Load() == 2 })
	keep, err := net.Dial("tcp", f.addr(t, "rtmp"))
	if err != nil {
		t.Fatal(err)
	}
	defer keep.Close()
	waitFor(t, "the third connection", func() bool { return f.role().rtmp.limit.n.Load() == 3 })
	over, err := net.Dial("tcp", f.addr(t, "rtmp"))
	if err != nil {
		t.Fatal(err)
	}
	defer over.Close()
	_ = over.SetReadDeadline(time.Now().Add(waitTimeout))
	if _, err := over.Read(make([]byte, 1)); err == nil {
		t.Fatal("a connection past max_conns was answered")
	}
}

// A publisher cut midway ends its stream and its HTTP-FLV player; a player
// that stops reading holds up neither the publisher nor the other players;
// a handshake that never comes is closed.
func TestCutConnections(t *testing.T) {
	f := startFit(t, doc(t, `{"rtmp": {"listen": "127.0.0.1:0", "handshake_timeout": "300ms"}}`))
	e := startEngine(t)
	cc, err := publishRTMP(t, e, "rtmp://"+f.addr(t, "rtmp")+"/live/cam", 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	f.waitLive(t, "live/cam")
	// A player that never reads, its window full.
	stuck, err := net.Dial("tcp", strings.TrimPrefix(f.base, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer stuck.Close()
	if tc, ok := stuck.(*net.TCPConn); ok {
		_ = tc.SetReadBuffer(4096)
	}
	_, _ = io.WriteString(stuck, "GET /flv/live/cam.flv?token="+viewToken+" HTTP/1.1\r\nHost: x\r\n\r\n")
	r, _, err := playRTMP(t, f, "/live/cam")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the player's packets", func() bool { return r.packets.Load() > 50 })

	resp, err := http.Get(f.base + "/flv/live/cam.flv?token=" + viewToken)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("the HTTP-FLV player: %d", resp.StatusCode)
	}
	ended := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, resp.Body)
		ended <- err
	}()
	// Cut the publisher's connection, with nothing said.
	if c := cc.Conn(); c != nil {
		_ = c.Close()
	}
	select {
	case <-ended:
	case <-time.After(waitTimeout):
		t.Fatal("the HTTP-FLV player outlived its publisher")
	}
	waitFor(t, "the stream to go", func() bool {
		_, ok := f.role().Hub().Stream("live/cam")
		return !ok
	})
	waitFor(t, "the RTMP player to end", r.closed.Load)

	// A client that connects and says nothing is closed after the
	// handshake timeout.
	idle, err := net.Dial("tcp", f.addr(t, "rtmp"))
	if err != nil {
		t.Fatal(err)
	}
	defer idle.Close()
	_ = idle.SetReadDeadline(time.Now().Add(waitTimeout))
	start := time.Now()
	if _, err := io.Copy(io.Discard, idle); err != nil {
		t.Fatalf("the idle connection: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("the idle connection was not closed in time")
	}
}

// A reload changes a listener's auth in place, its connections kept;
// another change of the listener starts it again, closing them; a change
// of the hub's limits ends every stream; leaving the role out stops it and
// its routes answer 503.
func TestReload(t *testing.T) {
	addr := freePort(t)
	f := startFit(t, doc(t, `{"rtmp": {"listen": "`+addr+`"}, "webrtc": {"listen": "127.0.0.1:0"}}`))
	e := startEngine(t)
	url := "rtmp://" + addr
	cc, err := publishRTMP(t, e, url+"/live/cam", 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	f.waitLive(t, "live/cam")
	closed := func() bool {
		_, ok := f.role().Hub().Stream("live/cam")
		return !ok
	}

	// auth on the same address: the publisher stays, a new one is checked.
	f.reload(t, doc(t, `{"rtmp": {"listen": "`+addr+`", "auth": "live"}, "webrtc": {"listen": "127.0.0.1:0"}}`))
	time.Sleep(100 * time.Millisecond)
	if closed() {
		t.Fatal("a change of auth ended the stream")
	}
	if _, err := publishRTMP(t, e, url+"/live/two", time.Second); rtmpCode(err) != "NetStream.Publish.Unauthorized" {
		t.Fatalf("after auth was set: %v", err)
	}
	if _, _, err := playRTMP(t, f, "/live/cam?token="+viewToken); err != nil {
		t.Fatalf("a player after auth was set: %v", err)
	}

	// Another field: the listener starts again on its address.
	f.reload(t, doc(t, `{"rtmp": {"listen": "`+addr+`", "auth": "live", "idle_timeout": "5s"}, "webrtc": {"listen": "127.0.0.1:0"}}`))
	waitFor(t, "the restarted listener to close the publisher", closed)
	if f.addr(t, "rtmp") != addr {
		t.Fatalf("the listener moved: %s", f.addr(t, "rtmp"))
	}
	if _, err := publishRTMP(t, e, url+"/live/cam?token="+pubToken, 20*time.Second); err != nil {
		t.Fatalf("a publisher on the restarted listener: %v", err)
	}
	_ = cc

	// The hub's limits: everything starts again.
	hub := f.role().Hub()
	f.reload(t, doc(t, `{"max_streams": 8, "rtmp": {"listen": "`+addr+`", "auth": "live", "idle_timeout": "5s"}, "webrtc": {"listen": "127.0.0.1:0"}}`))
	if f.role().Hub() == hub {
		t.Fatal("the hub was kept")
	}
	waitFor(t, "the stream to end", closed)
	if _, err := publishRTMP(t, e, url+"/live/cam?token="+pubToken, 20*time.Second); err != nil {
		t.Fatalf("a publisher on the new hub: %v", err)
	}
	f.waitLive(t, "live/cam")
	resp, _ := get(t, f.base+"/flv/live/cam.flv?token="+viewToken, 3, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("HTTP-FLV of the new hub: %d", resp.StatusCode)
	}

	// The role left out: gone, and its routes answer 503.
	f.reload(t, `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
	  "media_auth": [`+livePolicy(t)+`],
	  "routes": [{"match": {"path": ["/flv/*"]}, "handler": {"module": "httpflv", "params": {"prefix": "/flv/", "auth": "live"}}}]}`)
	if f.role() != nil {
		t.Fatal("the role still runs")
	}
}

// Without the role, the handlers answer 503; stream_whip without
// streaming.webrtc too.
func TestNotRunning(t *testing.T) {
	f := startFit(t, doc(t, `{"rtmp": {"listen": "127.0.0.1:0"}}`))
	req, _ := http.NewRequest("POST", f.base+"/whip/live/x", strings.NewReader("v=0\r\n"))
	req.Header.Set("Content-Type", "application/sdp")
	req.Header.Set("Authorization", "Bearer "+pubToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 503 {
		t.Fatalf("stream_whip without streaming.webrtc: %d", resp.StatusCode)
	}
	f.role().Stop(t.Context())
	for _, path := range []string{"/flv/live/cam.flv", "/hls/live/cam/index.m3u8"} {
		if resp, _ := get(t, f.base+path, 0, nil); resp.StatusCode != 503 {
			t.Fatalf("%s of a stopped role: %d", path, resp.StatusCode)
		}
	}
}

// freePort returns a TCP address on loopback that was free a moment ago.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}
