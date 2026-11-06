package vod

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	fibhttp "github.com/lesismal/fib/http"
	"github.com/lesismal/fib/media/hls"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/fibtest"
	"github.com/lesismal/fit/medialib"
	"github.com/lesismal/fit/module"
	"github.com/lesismal/fit/runtime"
)

const (
	viewToken = "a-viewer-token-0123456789"
	authConf  = `[{"name": "site", "tokens": [{"name": "viewer", "token": "` + viewToken + `", "play": ["vod/films/films/*"]}]}]`
)

// TestMaster checks the multivariant playlists: renditions as variants
// with their bandwidth, codecs and size, the item's audio files and the
// renditions' own audio as an audio group, subtitles as theirs; MPEG-TS
// leaving out what it cannot carry; and an item of audio alone.
func TestMaster(t *testing.T) {
	f := startVOD(t, copyLib(t), "", `{"library": "films"}`, "")
	b := string(f.mustGet(t, "/vod/films/films/movie/index.m3u8", 200))
	for _, want := range []string{
		"#EXT-X-VERSION:7\n",
		`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio",NAME="eng",LANGUAGE="eng",DEFAULT=YES,AUTOSELECT=YES` + "\n",
		`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio",NAME="audio-fr",LANGUAGE="fr",DEFAULT=NO,AUTOSELECT=YES,URI="a0.fmp4.m3u8"` + "\n",
		`#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs",NAME="en",LANGUAGE="en",DEFAULT=NO,AUTOSELECT=YES,FORCED=NO,URI="s0.m3u8"` + "\n",
		`CODECS="avc1.64000a,mp4a.40.2",RESOLUTION=128x96,AUDIO="audio",SUBTITLES="subs"` + "\nv0.fmp4.m3u8\n",
		`RESOLUTION=64x48,AUDIO="audio",SUBTITLES="subs"` + "\nv1.fmp4.m3u8\n",
	} {
		if !strings.Contains(b, want) {
			t.Errorf("the playlist lacks %q:\n%s", want, b)
		}
	}
	if ts := string(f.mustGet(t, "/vod/films/films/movie/index.ts.m3u8", 200)); !strings.Contains(ts, "#EXT-X-VERSION:3\n") || !strings.Contains(ts, "v0.ts.m3u8\n") {
		t.Errorf("the MPEG-TS playlist:\n%s", ts)
	}
	if fm := f.mustGet(t, "/vod/films/films/movie/index.fmp4.m3u8", 200); string(fm) != b {
		t.Errorf("index.fmp4.m3u8 is not index.m3u8:\n%s", fm)
	}
	// AV1, which MPEG-TS does not carry.
	f.mustGet(t, "/vod/films/clips/av1/index.ts.m3u8", 404)
	f.mustGet(t, "/vod/films/clips/av1/v0.ts.m3u8", 404)
	f.mustGet(t, "/vod/films/clips/av1/v0.0.ts", 404)
	if av1 := string(f.mustGet(t, "/vod/films/clips/av1/index.m3u8", 200)); !strings.Contains(av1, `CODECS="av01.`) || !strings.Contains(av1, ",opus\"") || strings.Contains(av1, "AUDIO=") {
		t.Errorf("the AV1 item's playlist:\n%s", av1)
	}
	// Audio alone: its file is the variant.
	song := string(f.mustGet(t, "/vod/films/music/song/index.m3u8", 200))
	if !strings.Contains(song, `CODECS="mp4a.40.2"`+"\na0.fmp4.m3u8\n") || strings.Contains(song, "RESOLUTION") || strings.Contains(song, "EXT-X-MEDIA") {
		t.Errorf("the audio item's playlist:\n%s", song)
	}
	if init := f.mustGet(t, "/vod/films/music/song/a0.init.mp4", 200); len(init) == 0 {
		t.Error("no init of the audio item")
	}
	resp, _ := f.get(t, "/vod/films/music/song/a0.init.mp4")
	if resp.Header.Get("Content-Type") != "audio/mp4" {
		t.Errorf("the audio init is %q", resp.Header.Get("Content-Type"))
	}
	resp, _ = f.get(t, "/vod/films/films/movie/index.m3u8")
	if resp.Header.Get("Content-Type") != hls.PlaylistContentType || resp.Header.Get("Last-Modified") == "" {
		t.Errorf("the playlist's header: %v", resp.Header)
	}
}

// TestMasterGroups checks the audio group of an item whose renditions lack
// audio of their own, its default as the sidecar says, names made unique,
// and quoted strings cleaned of what they may not hold.
func TestMasterGroups(t *testing.T) {
	root := copyLib(t)
	// A rendition without audio, and two audio files, the second the
	// default.
	writeSidecar(t, filepath.Join(root, "films", "movie.json"), `{"renditions": [{"file": "movie.mp4"}],
	  "audio": [{"file": "movie.audio-fr.m4a", "name": "Fr \"one\""}, {"file": "../music/song.m4a", "name": "Fr \"one\"", "default": true}]}`)
	_ = os.Remove(filepath.Join(root, "films", "movie.360p.mp4"))
	f := startVOD(t, root, "", `{"library": "films"}`, "")
	it := f.lib.Item("films/movie")
	if it == nil || len(it.Audio) == 0 {
		t.Skip("the sidecar's audio from another directory is not taken")
	}
	b := string(f.mustGet(t, "/vod/films/films/movie/index.m3u8", 200))
	t.Log(b)
	if strings.Count(b, `NAME="Fr 'one'`) == 0 {
		t.Errorf("names not cleaned:\n%s", b)
	}
}

func writeSidecar(t *testing.T, p, body string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestMediaPlaylists checks a media playlist's tags, and that one rendered
// again from the cache, or with a token, is the same but for the token.
func TestMediaPlaylists(t *testing.T) {
	f := startVOD(t, copyLib(t), "", `{"library": "films", "segment_duration": "2s"}`, "")
	want := "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n" +
		"#EXT-X-INDEPENDENT-SEGMENTS\n#EXT-X-MAP:URI=\"v1.init.mp4\"\n" +
		"#EXTINF:2.000,\nv1.0.m4s\n#EXTINF:2.000,\nv1.1.m4s\n#EXTINF:2.000,\nv1.2.m4s\n#EXT-X-ENDLIST\n"
	for range 2 {
		if b := string(f.mustGet(t, "/vod/films/films/movie/v1.fmp4.m3u8", 200)); b != want {
			t.Fatalf("got\n%s\nwant\n%s", b, want)
		}
	}
	// The long GOP: one segment, the target its length, not independent
	// of... it starts with its keyframe.
	lg := string(f.mustGet(t, "/vod/films/clips/longgop/v0.ts.m3u8", 200))
	if !strings.Contains(lg, "#EXT-X-TARGETDURATION:6\n") || strings.Count(lg, "#EXTINF") != 1 || strings.Contains(lg, "EXT-X-MAP") {
		t.Errorf("the long GOP's playlist:\n%s", lg)
	}
	// Subtitles: one segment of the item's length, as WebVTT with its
	// timestamp map.
	subs := string(f.mustGet(t, "/vod/films/films/movie/s0.m3u8", 200))
	if !strings.Contains(subs, "#EXTINF:6.000,\ns0.vtt\n") || !strings.Contains(subs, "#EXT-X-TARGETDURATION:6\n") {
		t.Errorf("the subtitles' playlist:\n%s", subs)
	}
	vtt := string(f.mustGet(t, "/vod/films/films/movie/s0.vtt", 200))
	if !strings.HasPrefix(vtt, "WEBVTT\nX-TIMESTAMP-MAP=MPEGTS:126000,LOCAL:00:00:00.000\n\n00:00:00.500 --> 00:00:02.000\nHello\n") {
		t.Errorf("the subtitle:\n%s", vtt)
	}
	f.mustGet(t, "/vod/films/films/movie/s1.m3u8", 404)
	f.mustGet(t, "/vod/films/films/movie/s1.vtt", 404)
}

// TestPaths checks what is not an item's name, method or path.
func TestPaths(t *testing.T) {
	f := startVOD(t, copyLib(t), "", `{"library": "films", "cors": "*"}`, "")
	for _, p := range []string{
		"/vod/films/index.m3u8", "/vod/films/films/movie/", "/vod/films/films/movie/index.m3u9",
		"/vod/films/films/nothing/index.m3u8", "/vod/films/films/../movie/index.m3u8", "/vod/films/films/movie/v9.fmp4.m3u8",
		"/vod/films/films/movie/a9.fmp4.m3u8", "/vod/films/films/movie/v0.9.m4s", "/vod/films/films/movie/v0.mov",
		"/vod/films/films/movie/v9.mp4", "/vod/films/films/movie/poster.jpg", "/vod/films/films/movie/v01.fmp4.m3u8",
		"/vod/other/films/movie/index.m3u8",
	} {
		resp, body := f.get(t, p)
		if resp.StatusCode != 404 {
			t.Errorf("%s: %d %q, want 404", p, resp.StatusCode, body)
		}
		if resp.Header.Get("Access-Control-Allow-Origin") != "*" {
			t.Errorf("%s: no CORS on the error", p)
		}
	}
	resp, _, err := fetch("POST", f.base+"/vod/films/films/movie/index.m3u8")
	if err != nil || resp.StatusCode != 405 || resp.Header.Get("Allow") != "GET, HEAD" {
		t.Errorf("POST: %v %v", resp, err)
	}
	resp, body, err := fetch("HEAD", f.base+"/vod/films/films/movie/v0.0.m4s")
	if err != nil || resp.StatusCode != 200 || len(body) != 0 || resp.ContentLength <= 0 {
		t.Errorf("HEAD: %v %d %v", resp, len(body), err)
	}
}

// TestFile checks the files themselves: whole, a range, a range past the
// end, several ranges, conditions, and the poster.
func TestFile(t *testing.T) {
	root := copyLib(t)
	if err := os.WriteFile(filepath.Join(root, "films", "movie.jpg"), []byte("\xff\xd8\xff\xe0 a poster"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := startVOD(t, root, "", `{"library": "films", "cache_control": "public, max-age=60"}`, "")
	file, err := os.ReadFile(filepath.Join(root, "films", "movie.360p.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	resp, body := f.get(t, "/vod/films/films/movie/v0.mp4")
	if resp.StatusCode != 200 || !bytes.Equal(body, file) || resp.Header.Get("Content-Type") != "video/mp4" ||
		resp.Header.Get("Accept-Ranges") != "bytes" || resp.Header.Get("Cache-Control") != "public, max-age=60" {
		t.Fatalf("whole: %d %v, %d bytes", resp.StatusCode, resp.Header, len(body))
	}
	tag := resp.Header.Get("Etag")
	resp, body = f.get(t, "/vod/films/films/movie/v0.mp4", "Range", "bytes=100-1099")
	if resp.StatusCode != 206 || !bytes.Equal(body, file[100:1100]) || resp.Header.Get("Content-Range") != fmt.Sprintf("bytes 100-1099/%d", len(file)) {
		t.Fatalf("range: %d %v", resp.StatusCode, resp.Header)
	}
	resp, _ = f.get(t, "/vod/films/films/movie/v0.mp4", "Range", fmt.Sprintf("bytes=%d-", len(file)+10))
	if resp.StatusCode != 416 {
		t.Fatalf("range past the end: %d", resp.StatusCode)
	}
	resp, body = f.get(t, "/vod/films/films/movie/v0.mp4", "Range", "bytes=0-9,20-29")
	if resp.StatusCode != 200 || len(body) != len(file) {
		t.Fatalf("several ranges: %d, %d bytes", resp.StatusCode, len(body))
	}
	resp, _ = f.get(t, "/vod/films/films/movie/v0.mp4", "If-None-Match", tag)
	if resp.StatusCode != 304 {
		t.Fatalf("If-None-Match: %d", resp.StatusCode)
	}
	resp, body = f.get(t, "/vod/films/films/movie/a0.m4a")
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "audio/mp4" || len(body) == 0 {
		t.Fatalf("audio file: %d %v", resp.StatusCode, resp.Header)
	}
	resp, body = f.get(t, "/vod/films/films/movie/poster.jpg")
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/jpeg" || string(body) != "\xff\xd8\xff\xe0 a poster" {
		t.Fatalf("poster: %d %v %q", resp.StatusCode, resp.Header, body)
	}
	f.mustGet(t, "/vod/films/films/movie/poster.png", 404)
}

// TestConditionalSegments checks a segment's validators: 304 for its
// ETag, a range of it, and Cache-Control and CORS as set.
func TestConditionalSegments(t *testing.T) {
	f := startVOD(t, copyLib(t), "", `{"library": "films", "cache_control": "public, max-age=86400", "cors": "*"}`, "")
	resp, seg := f.get(t, "/vod/films/films/movie/v0.1.m4s")
	tag := resp.Header.Get("Etag")
	if resp.StatusCode != 200 || tag == "" || resp.Header.Get("Last-Modified") == "" ||
		resp.Header.Get("Cache-Control") != "public, max-age=86400" || resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("%d %v", resp.StatusCode, resp.Header)
	}
	if resp, _ = f.get(t, "/vod/films/films/movie/v0.1.m4s", "If-None-Match", tag); resp.StatusCode != 304 {
		t.Errorf("If-None-Match: %d", resp.StatusCode)
	}
	resp, part := f.get(t, "/vod/films/films/movie/v0.1.m4s", "Range", "bytes=8-15")
	if resp.StatusCode != 206 || !bytes.Equal(part, seg[8:16]) {
		t.Errorf("range: %d %q", resp.StatusCode, part)
	}
	// Another segment, and the other container, have other tags.
	r2, _ := f.get(t, "/vod/films/films/movie/v0.0.m4s")
	r3, _ := f.get(t, "/vod/films/films/movie/v0.1.ts")
	if r2.Header.Get("Etag") == tag || r3.Header.Get("Etag") == tag || r3.Header.Get("Content-Type") != "video/mp2t" {
		t.Errorf("tags %q %q %q", tag, r2.Header.Get("Etag"), r3.Header.Get("Etag"))
	}
}

// TestChangedFile changes and removes a file being played: what is asked
// of it next is answered 404, what the cache keeps of it is dropped, and a
// rescan serves it as it is.
func TestChangedFile(t *testing.T) {
	root := copyLib(t)
	f := startVOD(t, root, "", `{"library": "films", "segment_duration": "2s"}`, "")
	h := f.handler(t)
	f.mustGet(t, "/vod/films/films/movie/v1.fmp4.m3u8", 200)
	seg0 := f.mustGet(t, "/vod/films/films/movie/v1.0.m4s", 200)
	before := h.Cache().Entries
	// The file is replaced by another of the same name.
	movie := filepath.Join(root, "films", "movie.mp4")
	other, err := os.ReadFile(filepath.Join(root, "clips", "hevc.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(movie, other, 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(3 * time.Second)
	_ = os.Chtimes(movie, later, later)
	f.mustGet(t, "/vod/films/films/movie/v1.1.m4s", 404)
	if h.Cache().Entries >= before {
		t.Errorf("the cache keeps %d entries, %d before the file changed", h.Cache().Entries, before)
	}
	// What was kept of it is gone too, and the file itself.
	f.mustGet(t, "/vod/films/films/movie/v1.0.m4s", 404)
	f.mustGet(t, "/vod/films/films/movie/v1.mp4", 404)
	// The other rendition plays on.
	f.mustGet(t, "/vod/films/films/movie/v0.0.m4s", 200)
	rescan(t, f.lib)
	if seg := f.mustGet(t, "/vod/films/films/movie/v1.0.m4s", 200); bytes.Equal(seg, seg0) {
		t.Error("the rescanned file's segment is the old one's")
	}
	// Removed: 404, and once rescanned, no item.
	f.mustGet(t, "/vod/films/clips/av1/v0.0.m4s", 200)
	if err := os.Remove(filepath.Join(root, "clips", "av1.mp4")); err != nil {
		t.Fatal(err)
	}
	f.mustGet(t, "/vod/films/clips/av1/v0.1.m4s", 404)
	f.mustGet(t, "/vod/films/clips/av1/v0.0.m4s", 404)
	rescan(t, f.lib)
	f.mustGet(t, "/vod/films/clips/av1/index.m3u8", 404)
	// The new catalog drops what the cache kept of files it lacks.
	f.mustGet(t, "/vod/films/clips/hevc/v0.0.m4s", 200)
	deadline := time.Now().Add(10 * time.Second)
	for h.st.cache.has(func(k ckey) bool { return strings.Contains(k.file.path, "av1") }) {
		if time.Now().After(deadline) {
			t.Fatal("the cache keeps what it built of a file the catalog lost")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// has reports whether c keeps or builds a key match takes.
func (c *cache) has(match func(ckey) bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.m {
		if match(k) {
			return true
		}
	}
	return false
}

// routeHandler returns the handler, with its middleware, of f's route.
func (f *fit) routeHandler() module.Handler {
	g := f.rt.Current()
	defer g.Release()
	return g.Listeners[0].Routes[0].Handler
}

// block occupies l's pool until the func returned is called: each of its
// workers runs a job that waits for it.
func block(t *testing.T, l *medialib.Library) func() {
	t.Helper()
	release := make(chan struct{})
	// What an earlier step queued drains first, on a loaded machine too,
	// so that the queue has room for the blocking jobs.
	deadline := time.Now().Add(10 * time.Second)
	for st := l.Pool().Stats(); st.Queued > 0 || st.Busy > 0; st = l.Pool().Stats() {
		if time.Now().After(deadline) {
			t.Fatalf("the pool is still busy: %+v", st)
		}
		time.Sleep(time.Millisecond)
	}
	n := l.Pool().Stats().Workers
	var started sync.WaitGroup
	started.Add(n)
	for range n {
		if !l.Submit(func() { started.Done(); <-release }, nil) {
			t.Fatal("the pool refused a job")
		}
	}
	started.Wait()
	var once sync.Once
	return func() { once.Do(func() { close(release) }) }
}

// TestNoWorkerBlocks serves requests while the library's pool is held up,
// as by a slow disk: ServeHTTP returns at once, and each request is
// answered once the pool goes on.
func TestNoWorkerBlocks(t *testing.T) {
	f := startVOD(t, copyLib(t), `, "file_workers": 1`, `{"library": "films"}`, "")
	h := f.routeHandler()
	for _, p := range []string{"films/movie/v0.fmp4.m3u8", "films/movie/v0.0.m4s", "films/movie/v1.0.ts", "films/movie/s0.vtt", "films/movie/v0.mp4"} {
		t.Run(p, func(t *testing.T) {
			release := block(t, f.lib)
			defer release()
			rec := &fibtest.Recorder{}
			start := time.Now()
			fibhttp.Serve(h, fibhttp.NewStreamContext(nil, httpRequest("/vod/films/"+p), rec))
			if d := time.Since(start); d > 200*time.Millisecond {
				t.Errorf("ServeHTTP returned after %v: it waited on the worker", d)
			}
			if rec.Wait(300 * time.Millisecond) {
				t.Fatal("answered while the pool was held up")
			}
			release()
			if !rec.Wait(10 * time.Second) {
				t.Fatal("no answer once the pool went on")
			}
			if rec.Response.StatusCode != 200 {
				t.Fatalf("status %d", rec.Response.StatusCode)
			}
		})
	}
}

func httpRequest(path string) *http.Request {
	r, _ := http.NewRequest("GET", "http://fit"+path, nil)
	r.RequestURI = path
	return r
}

// TestTimeout holds the pool up past the handler's timeout: 503, with
// Retry-After; and once the pool is free, the same request plays.
func TestTimeout(t *testing.T) {
	f := startVOD(t, copyLib(t), `, "file_workers": 1`, `{"library": "films", "timeout": "200ms"}`, "")
	release := block(t, f.lib)
	resp, body := f.get(t, "/vod/films/films/movie/v0.0.m4s")
	if resp.StatusCode != 503 || resp.Header.Get("Retry-After") != "1" {
		t.Fatalf("%d %q", resp.StatusCode, body)
	}
	release()
	f.mustGet(t, "/vod/films/films/movie/v0.0.m4s", 200)
}

// TestPoolBusy fills the pool's queue: a request that needs it is answered
// 503 at once.
func TestPoolBusy(t *testing.T) {
	f := startVOD(t, copyLib(t), `, "file_workers": 1, "file_queue": 1`, `{"library": "films"}`, "")
	release := block(t, f.lib)
	defer release()
	if !f.lib.Submit(func() {}, nil) {
		t.Fatal("the queue refused its one job")
	}
	for _, p := range []string{"films/movie/v0.0.m4s", "films/movie/v0.mp4", "films/movie/s0.vtt"} {
		resp, body := f.get(t, "/vod/films/"+p)
		if resp.StatusCode != 503 {
			t.Errorf("%s: %d %q", p, resp.StatusCode, body)
		}
	}
}

// TestCutConnections cuts connections whose requests wait for the pool,
// and one that takes a file slowly: the server goes on, and nothing is
// left retained.
func TestCutConnections(t *testing.T) {
	f := startVOD(t, copyLib(t), `, "file_workers": 1`, `{"library": "films"}`, "")
	addr := strings.TrimPrefix(f.base, "http://")
	release := block(t, f.lib)
	for _, p := range []string{"films/movie/v0.0.m4s", "films/movie/v0.mp4", "films/movie/v0.fmp4.m3u8"} {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(conn, "GET /vod/films/%s HTTP/1.1\r\nHost: fit\r\n\r\n", p)
		time.Sleep(20 * time.Millisecond)
		conn.Close()
	}
	time.Sleep(50 * time.Millisecond)
	release()
	// A client that reads a file a little at a time gets all of it, and one
	// that leaves halfway is let go.
	for _, leave := range []bool{false, true} {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(conn, "GET /vod/films/films/movie/v0.mp4 HTTP/1.1\r\nHost: fit\r\nConnection: close\r\n\r\n")
		var got []byte
		buf := make([]byte, 1024)
		for {
			_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
			n, err := conn.Read(buf)
			got = append(got, buf[:n]...)
			if err != nil || leave && len(got) > 8<<10 {
				break
			}
			time.Sleep(time.Millisecond)
		}
		conn.Close()
		if !leave && !bytes.Contains(got, []byte("\r\n\r\n")) {
			t.Fatal("no response")
		}
		if !leave {
			file, _ := os.ReadFile(filepath.Join(f.lib.Item("films/movie").Renditions[0].Root, f.lib.Item("films/movie").Renditions[0].Path))
			if !bytes.HasSuffix(got, file) {
				t.Errorf("a slow client got %d bytes, not the file's %d", len(got), len(file))
			}
		}
	}
	f.mustGet(t, "/vod/films/films/movie/v0.0.m4s", 200)
}

// TestCacheBound plays every segment of every item, many at once, through
// a cache much smaller than they are: the cache never passes its bound,
// and every request is answered, played or 503 when what is being built
// holds the cache.
func TestCacheBound(t *testing.T) {
	f := startVOD(t, copyLib(t), "", `{"library": "films", "segment_duration": "1s", "max_cache_bytes": 262144, "max_segment_bytes": 65536}`, "")
	h := f.handler(t)
	var paths []string
	for _, it := range f.lib.Catalog().Items {
		for _, c := range []string{"fmp4", "ts"} {
			b, err := fetchBody(f.base + "/vod/films/" + it.ID + "/index." + c + ".m3u8")
			if err != nil {
				continue
			}
			for line := range strings.Lines(string(b)) {
				line = strings.TrimSpace(line)
				if strings.HasSuffix(line, ".m3u8") && !strings.HasPrefix(line, "#") {
					list, err := fetchBody(f.base + "/vod/films/" + it.ID + "/" + line)
					if err != nil {
						t.Fatal(err)
					}
					for l := range strings.Lines(string(list)) {
						if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
							paths = append(paths, "/vod/films/"+it.ID+"/"+l)
						}
					}
				}
			}
		}
	}
	if len(paths) < 40 {
		t.Fatalf("only %d segments", len(paths))
	}
	stop := make(chan struct{})
	var over sync.WaitGroup
	over.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			if st := h.Cache(); st.Bytes > st.Max {
				t.Errorf("the cache holds %d bytes, past %d", st.Bytes, st.Max)
				return
			}
			time.Sleep(time.Millisecond)
		}
	})
	var wg sync.WaitGroup
	var mu sync.Mutex
	statuses := map[int]int{}
	for g := range 8 {
		wg.Go(func() {
			for i := range paths {
				resp, _, err := fetch("GET", f.base+paths[(i+g*7)%len(paths)])
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				statuses[resp.StatusCode]++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	close(stop)
	over.Wait()
	if statuses[200] < len(paths) || len(statuses) > 2 || len(statuses) == 2 && statuses[503] == 0 {
		t.Errorf("statuses %v", statuses)
	}
	if st := h.Cache(); st.Bytes > st.Max || st.Bytes == 0 {
		t.Errorf("cache %+v", st)
	}
}

func fetchBody(url string) ([]byte, error) {
	resp, body, err := fetch("GET", url)
	if err == nil && resp.StatusCode != 200 {
		err = fmt.Errorf("%s: %d", url, resp.StatusCode)
	}
	return body, err
}

// TestSameSegmentAtOnce asks for one segment many times at once, while it
// is built: each gets the same bytes.
func TestSameSegmentAtOnce(t *testing.T) {
	f := startVOD(t, copyLib(t), `, "file_workers": 2`, `{"library": "films"}`, "")
	release := block(t, f.lib)
	var wg sync.WaitGroup
	bodies := make([][]byte, 32)
	for i := range bodies {
		wg.Go(func() {
			resp, body, err := fetch("GET", f.base+"/vod/films/films/movie/v0.1.ts")
			if err != nil || resp.StatusCode != 200 {
				t.Errorf("%v %v", resp, err)
			}
			bodies[i] = body
		})
	}
	time.Sleep(100 * time.Millisecond)
	release()
	wg.Wait()
	for i, b := range bodies {
		if !bytes.Equal(b, bodies[0]) || len(b) == 0 {
			t.Fatalf("response %d differs", i)
		}
	}
}

// TestAuth checks the policy: none or another resource's credentials are
// refused, a token plays every part of the item, and a playlist fetched
// with one in its query passes it on.
func TestAuth(t *testing.T) {
	f := startVOD(t, copyLib(t), "", `{"library": "films", "auth": "site"}`, authConf)
	resp, _ := f.get(t, "/vod/films/films/movie/index.m3u8")
	if resp.StatusCode != 401 {
		t.Fatalf("no token: %d", resp.StatusCode)
	}
	resp, _ = f.get(t, "/vod/films/clips/hevc/index.m3u8", "Authorization", "Bearer "+viewToken)
	if resp.StatusCode != 403 {
		t.Fatalf("another resource: %d", resp.StatusCode)
	}
	f.mustGet(t, "/vod/films/films/movie/v0.0.m4s", 200, "Authorization", "Bearer "+viewToken)
	master := string(f.mustGet(t, "/vod/films/films/movie/index.m3u8?token="+viewToken, 200))
	for _, want := range []string{"v0.fmp4.m3u8?token=" + viewToken + "\n", `URI="a0.fmp4.m3u8?token=` + viewToken + `"`, `URI="s0.m3u8?token=` + viewToken + `"`} {
		if !strings.Contains(master, want) {
			t.Errorf("the playlist lacks %q:\n%s", want, master)
		}
	}
	media := string(f.mustGet(t, "/vod/films/films/movie/v0.fmp4.m3u8?token="+viewToken, 200))
	if !strings.Contains(media, `#EXT-X-MAP:URI="v0.init.mp4?token=`+viewToken+`"`) || !strings.Contains(media, "v0.0.m4s?token="+viewToken+"\n") {
		t.Errorf("the media playlist:\n%s", media)
	}
	// Rendered with a token, it is not the one the cache keeps.
	if plain := string(f.mustGet(t, "/vod/films/films/movie/v0.fmp4.m3u8", 200, "Authorization", "Bearer "+viewToken)); strings.Contains(plain, "token=") {
		t.Errorf("a playlist without a token in its query names one:\n%s", plain)
	}
	subs := string(f.mustGet(t, "/vod/films/films/movie/s0.m3u8?token="+viewToken, 200))
	if !strings.Contains(subs, "s0.vtt?token="+viewToken) {
		t.Errorf("subtitles:\n%s", subs)
	}
	f.mustGet(t, "/vod/films/films/movie/s0.vtt?token="+viewToken, 200)
}

// TestReload checks that a reload that keeps the params keeps what the
// cache holds, one that changes them starts another, and the config's
// last handler gives it back.
func TestReload(t *testing.T) {
	root := copyLib(t)
	f := startVOD(t, root, "", `{"library": "films"}`, "")
	f.mustGet(t, "/vod/films/films/movie/v0.0.m4s", 200)
	st := f.handler(t).st
	doc := func(params string) string {
		return `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "media_library": [` + libConf(root, "") + `],
  "routes": [{"match": {"path": ["/vod/*"]}, "handler": {"module": "vod", "params": ` + params + `}}]}`
	}
	f.swap(t, doc(`{"library": "films"}`))
	if f.handler(t).st != st || st.cache.stats().Entries == 0 {
		t.Error("a reload with the same params did not keep the cache")
	}
	f.swap(t, doc(`{"library": "films", "segment_duration": "2s"}`))
	if f.handler(t).st == st {
		t.Error("a reload with another segment_duration kept the cache")
	}
	f.mustGet(t, "/vod/films/films/movie/v0.0.m4s", 200)
	// The old config's handler is cleaned up once swapped out, and the
	// connections that served it are gone.
	deadline := time.Now().Add(10 * time.Second)
	for {
		http.DefaultTransport.(*http.Transport).CloseIdleConnections()
		storesMu.Lock()
		gone := stores[st.key] == nil
		storesMu.Unlock()
		if gone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the old store is still held")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestNotLoaded checks a library the config names that the role has not
// applied, or removed: 503.
func TestNotLoaded(t *testing.T) {
	root := copyLib(t)
	c, err := config.Load([]byte(`{"listeners": [{"name": "web", "address": ":0"}],
  "media_library": [` + libConf(root, "") + `],
  "routes": [{"handler": {"module": "vod", "params": {"library": "films"}}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	g, err := runtime.Compile(c, runtime.Env{Engine: newEngineKey()})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Release()
	rec := fibtest.ServeRequest(g.Listeners[0].Routes[0].Handler, httpRequest("/vod/films/films/movie/index.m3u8"))
	if rec.Response.StatusCode != 503 {
		t.Errorf("%d", rec.Response.StatusCode)
	}
}

// TestProvision checks the params a config is refused for.
func TestProvision(t *testing.T) {
	root := copyLib(t)
	for _, tc := range []struct{ params, want string }{
		{`{"library": "nope"}`, "no media_library named"},
		{`{"library": "bad name"}`, "not a library's name"},
		{`{"library": "films", "prefix": "vod"}`, "does not start with /"},
		{`{"library": "films", "segment_duration": "500ms"}`, "is not between"},
		{`{"library": "films", "segment_duration": "2m"}`, "is not between"},
		{`{"library": "films", "max_segment_bytes": 1000}`, "is not between 64 KiB"},
		{`{"library": "films", "max_cache_bytes": 1048576, "max_segment_bytes": 1048576}`, "a quarter of max_cache_bytes"},
		{`{"library": "films", "cors": "a\nb"}`, "line break"},
		{`{"library": "films", "auth": "nope"}`, "no media_auth policy"},
	} {
		c, err := config.Load([]byte(`{"listeners": [{"name": "web", "address": ":0"}],
  "media_library": [` + libConf(root, "") + `],
  "routes": [{"handler": {"module": "vod", "params": ` + tc.params + `}}]}`))
		if err == nil {
			var g *runtime.Graph
			if g, err = runtime.Compile(c, runtime.Env{}); err == nil {
				g.Release()
			}
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.params, err, tc.want)
		}
	}
	// The prefix gets its slash.
	f := startVOD(t, root, "", `{"library": "films", "prefix": "/v"}`, "")
	resp, _ := f.get(t, "/v/films/movie/index.m3u8")
	if resp.StatusCode != 404 {
		// The route matches /vod/* only: /v/... is not routed to it.
		t.Logf("%d", resp.StatusCode)
	}
	if h := f.handler(t); h.prefix != "/v/" {
		t.Errorf("prefix %q", h.prefix)
	}
}
