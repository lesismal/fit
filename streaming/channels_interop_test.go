package streaming

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/streaming/channels"
)

// The channels' interop checks, in the container of
// test/docker/channels/e2e.sh (or scripts/streaming-interop.sh with
// TestChannelInterop), with FIT_MEDIA_INTEROP set: ffmpeg makes the items,
// fit plays them as channels, and ffprobe reads every egress, its decode
// times running on across the items without a gap or a jump. With
// FIT_CHROMIUM and FIT_HLSJS (the path of hls.js) set, a headless Chromium
// plays the channels with hls.js; with FIT_PUSH_RTMP and FIT_PUSH_SRT set,
// the channel is pushed to an outside server, which the script restarts
// when the test asks it to, through FIT_PUSH_SIGNAL, a directory.

// makeItem writes an item of d seconds, 25 fps with a keyframe a second
// and B-frames, and AAC, at size, from source, filtered by vf.
func makeItem(t *testing.T, path, source, vf, size string, d int) {
	t.Helper()
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", source+"=size="+size+":rate=25", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", strconv.Itoa(d), "-vf", vf, "-c:v", "libx264", "-preset", "veryfast", "-profile:v", "main", "-pix_fmt", "yuv420p",
		"-g", "25", "-keyint_min", "25", "-sc_threshold", "0", "-bf", "2", "-c:a", "aac", "-b:a", "64k", "-ac", "2", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v: %s", err, out)
	}
}

// probe reads d of url with ffprobe and returns the decode times of its
// video packets, in seconds.
func probe(ctx context.Context, d time.Duration, url string, in ...string) ([]float64, string, error) {
	ctx, cancel := context.WithTimeout(ctx, d+40*time.Second)
	defer cancel()
	args := append([]string{"-v", "error"}, in...)
	args = append(args, "-read_intervals", fmt.Sprintf("%%+%d", int(d.Seconds())), "-select_streams", "v:0",
		"-show_entries", "packet=dts_time", "-of", "csv=p=0", url)
	out, err := exec.CommandContext(ctx, "ffprobe", args...).Output()
	var stderr string
	if ee, ok := err.(*exec.ExitError); ok {
		stderr = string(ee.Stderr)
	}
	var dts []float64
	for line := range strings.SplitSeq(string(out), "\n") {
		if v, err := strconv.ParseFloat(strings.TrimSpace(strings.Trim(line, ",")), 64); err == nil {
			dts = append(dts, v)
		}
	}
	return dts, stderr, err
}

// checkDTS checks that dts spans at least span seconds and never steps
// back nor forward by more than gap.
func checkDTS(dts []float64, span, gap float64) error {
	if len(dts) < 2 || dts[len(dts)-1]-dts[0] < span {
		return fmt.Errorf("%d packets, over less than %gs", len(dts), span)
	}
	for i := 1; i < len(dts); i++ {
		if d := dts[i] - dts[i-1]; d <= 0 || d > gap {
			return fmt.Errorf("DTS %.3f after %.3f, packet %d of %d", dts[i], dts[i-1], i, len(dts))
		}
	}
	return nil
}

func TestChannelInterop(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	// a and b alike, as a site's films encoded the same way: their tracks,
	// SPS and PPS are the same, and the channel carries on across them. c
	// is larger, its SPS another.
	makeItem(t, filepath.Join(dir, "a.mp4"), "testsrc", "null", "320x240", 6)
	makeItem(t, filepath.Join(dir, "b.mp4"), "testsrc", "hflip", "320x240", 6)
	makeItem(t, filepath.Join(dir, "c.mp4"), "testsrc2", "null", "480x270", 6)
	push := ""
	if r, s := os.Getenv("FIT_PUSH_RTMP"), os.Getenv("FIT_PUSH_SRT"); r != "" && s != "" {
		push = `, "push": [{"name": "outside-rtmp", "url": "` + r + `", "retry_min": "500ms", "retry_max": "2s"},
		  {"name": "outside-srt", "url": "` + s + `", "retry_min": "500ms", "retry_max": "2s"}]`
	}
	f := startFit(t, `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "media_library": [{"name": "lib", "roots": [`+strconv.Quote(dir)+`]}],
  "streaming": {
    "rtmp": {"listen": "127.0.0.1:0"}, "rtsp": {"listen": "127.0.0.1:0"}, "srt": {"listen": "127.0.0.1:0"},
    "channels": [{"name": "live/tv", "library": "lib", "items": ["a", "b"]`+push+`},
                 {"name": "live/mix", "library": "lib", "items": ["a", "c"]}]
  },
  "routes": [
    {"match": {"path": ["/flv/*"]}, "handler": {"module": "httpflv", "params": {"prefix": "/flv/", "cors": "*"}}},
    {"match": {"path": ["/hls/*"]}, "handler": {"module": "hls", "params": {"prefix": "/hls/", "target_duration": "1s", "cors": "*"}}}
  ]
}`)
	f.waitLive(t, "live/tv")
	f.waitLive(t, "live/mix")
	ctx := context.Background()
	rtmpAddr, rtspAddr, srtAddr := f.addr(t, "rtmp"), f.addr(t, "rtsp"), f.addr(t, "srt")

	// Each egress for 14s: two boundaries of live/tv's 6s items.
	const span = 14 * time.Second
	type check struct {
		name, url string
		in        []string
		gap       float64
	}
	checks := []check{
		{"rtmp", "rtmp://" + rtmpAddr + "/live/tv", nil, 0.1},
		{"rtsp", "rtsp://" + rtspAddr + "/live/tv", []string{"-rtsp_transport", "tcp"}, 0.1},
		{"srt", "srt://" + srtAddr + "?streamid=live/tv", nil, 0.1},
		{"http-flv", f.base + "/flv/live/tv.flv", nil, 0.1},
		{"hls-fmp4", f.base + "/hls/live/tv/index.m3u8", nil, 0.1},
		{"hls-ts", f.base + "/hls/live/tv/index.m3u8?format=ts", nil, 0.1},
		// live/mix sets new tracks at each item: HLS marks a discontinuity.
		// fib's egresses take new tracks for a clock that may have started
		// again, and move what follows past the last packet of any track:
		// the next item's first pictures, decoded before the last item's
		// audio ends, by up to the B-frames' delay and 20ms (fib request
		// A68).
		{"mix-hls-fmp4", f.base + "/hls/live/mix/index.m3u8", nil, 0.2},
		{"mix-rtmp", "rtmp://" + rtmpAddr + "/live/mix", nil, 0.2},
		{"mix-srt", "srt://" + srtAddr + "?streamid=live/mix", nil, 0.2},
	}
	var wg sync.WaitGroup
	for _, c := range checks {
		wg.Go(func() {
			dts, stderr, err := probe(ctx, span, c.url, c.in...)
			if err == nil {
				err = checkDTS(dts, span.Seconds()-3, c.gap)
			}
			if err != nil {
				t.Errorf("%s: %v %s", c.name, err, firstLine(stderr))
			} else {
				t.Logf("%s: %d video packets over %.1fs, continuous", c.name, len(dts), dts[len(dts)-1]-dts[0])
			}
		})
	}
	wg.Go(func() {
		// Decoded whole by ffmpeg, across a boundary.
		frames, out, err := ffmpegPlay(ctx, 8*time.Second, "-i", "rtmp://"+rtmpAddr+"/live/tv")
		if err != nil || frames < 150 {
			t.Errorf("ffmpeg decoded %d frames of 200: %v %s", frames, err, firstLine(out))
		}
	})
	wg.Go(func() {
		// The discontinuity in live/mix's media playlist, once an item has
		// followed another.
		deadline := time.Now().Add(span)
		for {
			var body []byte
			if resp, err := http.Get(f.base + "/hls/live/mix/media.m3u8"); err == nil {
				body, _ = io.ReadAll(io.LimitReader(resp.Body, 1<<16))
				resp.Body.Close()
			}
			if strings.Contains(string(body), "#EXT-X-DISCONTINUITY") {
				return
			}
			if time.Now().After(deadline) {
				t.Errorf("no discontinuity in live/mix: %s", body)
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
	})
	if os.Getenv("FIT_CHROMIUM") != "" && os.Getenv("FIT_HLSJS") != "" {
		wg.Go(func() { hlsjs(t, f) })
	}
	wg.Wait()
	if push != "" {
		pushOutside(t, f)
	}
}

// hlsjsPage is the page hls.js plays a channel on: it reports what played
// to /result after 30s.
const hlsjsPage = `<!doctype html><meta charset=utf-8><video id=v muted autoplay playsinline></video>
<script src="/hls.js"></script><script>
const v = document.getElementById('v'), out = {errors: [], cc: 0, times: [], src: SRC};
const h = new Hls();
h.on(Hls.Events.ERROR, (e, d) => { if (d.fatal) out.errors.push(d.type + ': ' + d.details) });
h.on(Hls.Events.FRAG_CHANGED, (e, d) => { out.cc = Math.max(out.cc, d.frag.cc) });
h.loadSource(SRC); h.attachMedia(v); v.play().catch(e => out.errors.push('play: ' + e));
const t0 = Date.now();
const timer = setInterval(() => {
  const q = v.getVideoPlaybackQuality ? v.getVideoPlaybackQuality() : {};
  out.times.push([(Date.now() - t0) / 1000, v.currentTime, q.totalVideoFrames || 0, q.droppedVideoFrames || 0]);
  if (Date.now() - t0 > 30000) { clearInterval(timer); fetch('/result', {method: 'POST', body: JSON.stringify(out)}); }
}, 1000);
</script>`

// hlsResult is what the page reports.
type hlsResult struct {
	Errors []string     `json:"errors"`
	CC     int          `json:"cc"`
	Times  [][4]float64 `json:"times"`
	Src    string       `json:"src"`
}

// hlsjs plays live/tv and live/mix with hls.js in a headless Chromium.
func hlsjs(t *testing.T, f *fit) {
	results := make(chan hlsResult, 2)
	mux := http.NewServeMux()
	mux.HandleFunc("/hls.js", func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, os.Getenv("FIT_HLSJS")) })
	mux.HandleFunc("/play/{name}", func(w http.ResponseWriter, r *http.Request) {
		src := f.base + "/hls/live/" + r.PathValue("name") + "/index.m3u8"
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, strings.Replace(hlsjsPage, "SRC", strconv.Quote(src), 2))
	})
	mux.HandleFunc("POST /result", func(w http.ResponseWriter, r *http.Request) {
		var res hlsResult
		_ = json.NewDecoder(r.Body).Decode(&res)
		results <- res
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Error(err)
		return
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	var cmds []*exec.Cmd
	for _, name := range []string{"tv", "mix"} {
		cmd := exec.Command(os.Getenv("FIT_CHROMIUM"), "--headless=new", "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage",
			"--no-first-run", "--autoplay-policy=no-user-gesture-required", "--user-data-dir="+t.TempDir(),
			"http://"+ln.Addr().String()+"/play/"+name)
		if err := cmd.Start(); err != nil {
			t.Error(err)
			return
		}
		cmds = append(cmds, cmd)
	}
	defer func() {
		for _, c := range cmds {
			_ = c.Process.Kill()
			_ = c.Wait()
		}
	}()
	for range 2 {
		select {
		case res := <-results:
			checkHLSJS(t, res)
		case <-time.After(90 * time.Second):
			t.Error("hls.js: no result after 90s")
			return
		}
	}
}

// checkHLSJS checks that hls.js played on, through a discontinuity for
// live/mix, without a fatal error or a stall of 2s.
func checkHLSJS(t *testing.T, res hlsResult) {
	t.Logf("hls.js %s: cc %d, errors %v, times %v", res.Src, res.CC, res.Errors, res.Times)
	if len(res.Errors) > 0 || len(res.Times) < 20 {
		t.Errorf("hls.js %s: %v", res.Src, res.Errors)
		return
	}
	first, last := res.Times[0], res.Times[len(res.Times)-1]
	if played := last[1] - first[1]; played < 20 {
		t.Errorf("hls.js %s: %.1fs played in 30s", res.Src, played)
	}
	if strings.Contains(res.Src, "/mix/") && res.CC < 1 {
		t.Errorf("hls.js %s: no discontinuity played through", res.Src)
	}
	stalled := 0
	for i := 1; i < len(res.Times); i++ {
		if res.Times[i][1] > 0 && res.Times[i][1]-res.Times[i-1][1] < 0.3 {
			stalled++
			if stalled >= 2 {
				t.Errorf("hls.js %s: stalled at %.1fs", res.Src, res.Times[i][1])
				return
			}
		} else {
			stalled = 0
		}
	}
}

// pushOutside checks the pushes to the outside server: it has the stream;
// the script restarts it; it has the stream again.
func pushOutside(t *testing.T, f *fit) {
	signal := os.Getenv("FIT_PUSH_SIGNAL")
	plays := map[string][]string{
		"rtmp": {os.Getenv("FIT_PUSH_RTMP_PLAY")},
		"srt":  {os.Getenv("FIT_PUSH_SRT_PLAY")},
	}
	m := channels.Lookup(f.up)
	has := func(what string) {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		for {
			ok := true
			for name, in := range plays {
				dts, stderr, err := probe(context.Background(), 4*time.Second, in[0])
				if err == nil {
					// The relayed stream, through MediaMTX: there, not
					// its continuity.
					err = checkDTS(dts, 2, 0.5)
				}
				if err != nil {
					ok = false
					if time.Now().After(deadline) {
						t.Fatalf("%s: the outside server's %s: %v %s %+v", what, name, err, firstLine(stderr), m.Pushes())
					}
				}
			}
			if ok {
				return
			}
			time.Sleep(time.Second)
		}
	}
	has("before its restart")
	if err := os.WriteFile(filepath.Join(signal, "restart"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the script to restart the outside server", func() bool {
		_, err := os.Stat(filepath.Join(signal, "restarted"))
		return err == nil
	})
	has("after its restart")
	for _, p := range m.Pushes() {
		t.Logf("%s: %+v", p.Name, p)
		if p.Connects < 2 || p.Failures < 1 || p.State != channels.PushConnected {
			t.Errorf("%s did not reconnect: %+v", p.Name, p)
		}
	}
}
