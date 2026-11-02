package streaming

import (
	"bytes"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fib/media/av"
	"github.com/lesismal/fib/media/rtmp"

	"github.com/lesismal/fit/observe"
)

// recorder is a hub.Sink that counts what it hears.
type recorder struct {
	tracks, packets atomic.Int64
	closed          atomic.Bool
}

func (r *recorder) OnTracks([]*av.Track) { r.tracks.Add(1) }
func (r *recorder) OnPacket(*av.Packet)  { r.packets.Add(1) }
func (r *recorder) OnClose(error)        { r.closed.Store(true) }

// RTMP publishes and plays by the credentials in the stream name's query:
// none, a token that may not, a user's password, and a password_hash user,
// who is refused once while the hash is checked and passes after.
func TestRTMPAuth(t *testing.T) {
	f := startFit(t, doc(t, allListeners))
	e := startEngine(t)
	url := "rtmp://" + f.addr(t, "rtmp")

	if _, err := publishRTMP(t, e, url+"/live/cam", time.Second); rtmpCode(err) != "NetStream.Publish.Unauthorized" {
		t.Fatalf("no credentials: %v", err)
	}
	if _, err := publishRTMP(t, e, url+"/live/cam?token="+viewToken, time.Second); rtmpCode(err) != "NetStream.Publish.Unauthorized" {
		t.Fatalf("a token that may not publish: %v", err)
	}
	if _, err := publishRTMP(t, e, url+"/live/cam?token=not-a-token-at-all", time.Second); rtmpCode(err) != "NetStream.Publish.Unauthorized" {
		t.Fatalf("a bad token: %v", err)
	}
	if _, err := publishRTMP(t, e, url+"/live/cam?token="+pubToken, 5*time.Second); err != nil {
		t.Fatalf("the publishing token: %v", err)
	}
	if _, err := publishRTMP(t, e, url+"/live/alice?user=alice&pass=alice-pw", time.Second); err != nil {
		t.Fatalf("alice: %v", err)
	}
	if _, err := publishRTMP(t, e, url+"/live/other?user=alice&pass=alice-pw", time.Second); rtmpCode(err) != "NetStream.Publish.Unauthorized" {
		t.Fatalf("alice on a stream she may not publish: %v", err)
	}
	// bob's password is checked with bcrypt off the worker: refused once,
	// then let in at once.
	if _, err := publishRTMP(t, e, url+"/live/bob?user=bob&pass=bob-pw", time.Second); rtmpCode(err) != "NetStream.Publish.Unauthorized" ||
		!strings.Contains(err.Error(), "try again") {
		t.Fatalf("bob's first publish: %v", err)
	}
	var err error
	waitFor(t, "bob's password to be checked", func() bool {
		_, err = publishRTMP(t, e, url+"/live/bob?user=bob&pass=bob-pw", time.Second)
		return err == nil
	})
	if _, err := publishRTMP(t, e, url+"/live/bob2?user=bob&pass=wrong", time.Second); err == nil {
		t.Fatal("bob with a wrong password")
	}

	play := func(path string) (*recorder, error) {
		r := &recorder{}
		ready := make(chan error, 1)
		cc, err := rtmp.Play(e, url+path, rtmp.ClientConfig{}, r, func(_ *rtmp.ClientConn, err error) { ready <- err })
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cc.Close() })
		select {
		case err := <-ready:
			return r, err
		case <-time.After(waitTimeout):
			t.Fatal("the play was not answered")
			return nil, nil
		}
	}
	if _, err := play("/live/cam"); rtmpCode(err) != "NetStream.Play.Failed" {
		t.Fatalf("a play without credentials: %v", err)
	}
	if _, err := play("/live/cam?token=" + pubToken); rtmpCode(err) != "NetStream.Play.Failed" {
		t.Fatalf("a play with a token that may only publish: %v", err)
	}
	r, err := play("/live/cam?token=" + viewToken)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the player's packets", func() bool { return r.packets.Load() > 10 })

	// The refusals are counted, and the streams.
	var w observe.TextWriter
	f.role().WriteMetrics(&w)
	m := string(w.B)
	for _, want := range []string{
		`fit_streaming_streams{state="publishing"} 3`,
		`fit_streaming_players 1`,
		`fit_streaming_auth_refused_total{protocol="rtmp",reason="unauthorized"} 3`,
		`fit_streaming_auth_refused_total{protocol="rtmp",reason="forbidden"} 3`,
		`fit_streaming_auth_refused_total{protocol="rtmp",reason="unavailable"} `,
	} {
		if !strings.Contains(m, want) {
			t.Errorf("no %s in\n%s", want, m)
		}
	}
}

// HTTP-FLV plays with a token in the query, a bearer token, Basic or the
// cookie, 401 without, 403 for credentials that may not, 404 for a path
// that names no stream.
func TestHTTPFLVAuth(t *testing.T) {
	f := startFit(t, doc(t, allListeners))
	e := startEngine(t)
	if _, err := publishRTMP(t, e, "rtmp://"+f.addr(t, "rtmp")+"/live/cam?token="+pubToken, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	resp, _ := get(t, f.base+"/flv/live/cam.flv", 0, nil)
	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("Www-Authenticate") != `Bearer realm="fit"` {
		t.Fatalf("no credentials: %d %v", resp.StatusCode, resp.Header)
	}
	if resp, _ = get(t, f.base+"/flv/live/cam.flv", 0, bearer(pubToken)); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a token that may only publish: %d", resp.StatusCode)
	}
	for name, set := range map[string]func(*http.Request){
		"query":  nil,
		"bearer": bearer(viewToken),
		"basic":  func(r *http.Request) { r.SetBasicAuth("alice", "alice-pw") },
		"cookie": func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "fit_media_token", Value: viewToken}) },
	} {
		u := f.base + "/flv/live/cam.flv"
		if set == nil {
			u += "?token=" + viewToken
		}
		resp, body := get(t, u, 3, set)
		if resp.StatusCode != http.StatusOK || !bytes.Equal(body, []byte("FLV")) {
			t.Fatalf("%s: %d %q", name, resp.StatusCode, body)
		}
	}
	if resp, _ = get(t, f.base+"/flv/live/../cam.flv", 0, nil); resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a path with ..: %d", resp.StatusCode)
	}
	if resp, _ = get(t, f.base+"/flv/live/none.flv?token="+viewToken, 0, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a stream without a publisher: %d", resp.StatusCode)
	}
}

// HLS: a playlist fetched with a query token names everything with it,
// and every request is checked.
func TestHLSAuth(t *testing.T) {
	f := startFit(t, doc(t, allListeners))
	e := startEngine(t)
	if _, err := publishRTMP(t, e, "rtmp://"+f.addr(t, "rtmp")+"/live/cam?token="+pubToken, 8*time.Second); err != nil {
		t.Fatal(err)
	}
	if resp, _ := get(t, f.base+"/hls/live/cam/index.m3u8", 1<<16, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no credentials: %d", resp.StatusCode)
	}
	resp, body := get(t, f.base+"/hls/live/cam/index.m3u8?token="+viewToken, 1<<16, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the playlist: %d %s", resp.StatusCode, body)
	}
	media := ""
	for _, line := range strings.Split(string(body), "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			media = line
		}
	}
	if !strings.Contains(media, "token="+viewToken) {
		t.Fatalf("the multivariant playlist names %q:\n%s", media, body)
	}
	resp, body = get(t, f.base+"/hls/live/cam/"+media, 1<<16, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the media playlist: %d %s", resp.StatusCode, body)
	}
	var seg, mp4 string
	for _, line := range strings.Split(string(body), "\n") {
		if line != "" && !strings.HasPrefix(line, "#") && seg == "" {
			seg = line
		}
		if strings.HasPrefix(line, "#EXT-X-MAP:") {
			mp4 = line
		}
	}
	if !strings.Contains(seg, "token="+viewToken) || !strings.Contains(mp4, "token="+viewToken) {
		t.Fatalf("the media playlist names %q and %q:\n%s", seg, mp4, body)
	}
	if resp, _ = get(t, f.base+"/hls/live/cam/"+seg, 1<<20, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("a segment with the token: %d", resp.StatusCode)
	}
	plain, _, _ := strings.Cut(seg, "?")
	if resp, _ = get(t, f.base+"/hls/live/cam/"+plain, 1<<20, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a segment without: %d", resp.StatusCode)
	}
	if resp, _ = get(t, f.base+"/hls/live/cam/"+plain, 1<<20, bearer(viewToken)); resp.StatusCode != http.StatusOK {
		t.Fatalf("a segment with a bearer token: %d", resp.StatusCode)
	}
}

func TestWithToken(t *testing.T) {
	in := "#EXTM3U\n#EXT-X-MAP:URI=\"init-1.mp4\"\n#EXTINF:2.0,\nseg-1.m4s\r\n#EXT-X-PRELOAD-HINT:TYPE=PART,URI=\"part-2.0.m4s\"\nmedia.m3u8?format=ts\nseg-2.m4s?token=x\n"
	want := "#EXTM3U\n#EXT-X-MAP:URI=\"init-1.mp4?token=a%2Bb\"\n#EXTINF:2.0,\nseg-1.m4s?token=a%2Bb\r\n#EXT-X-PRELOAD-HINT:TYPE=PART,URI=\"part-2.0.m4s?token=a%2Bb\"\nmedia.m3u8?format=ts&token=a%2Bb\nseg-2.m4s?token=x\n"
	if got := string(withToken([]byte(in), "a+b")); got != want {
		t.Fatalf("got\n%q\nwant\n%q", got, want)
	}
}

func TestValidStream(t *testing.T) {
	for p, want := range map[string]bool{
		"live/cam": true, "cam": true, "a.b/c_d-e~f": true,
		"": false, "/cam": false, "cam/": false, "live//cam": false, "live/../cam": false, "..": false,
		"live/cam?x": false, strings.Repeat("a", 257): false,
	} {
		if validStream(p) != want {
			t.Errorf("%q: %v", p, !want)
		}
	}
}
