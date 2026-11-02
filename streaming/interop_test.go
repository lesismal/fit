package streaming

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// The ffmpeg interop checks: ffmpeg publishes over RTMP, RTSP and SRT with
// valid and invalid credentials, and plays every stream over RTMP, RTSP,
// SRT, HTTP-FLV and HLS. They run only with FIT_MEDIA_INTEROP set, as the
// Linux container of scripts/streaming-interop.sh sets it, with an ffmpeg
// that has libx264 and libsrt; there a missing ffmpeg fails them.

func requireFFmpeg(t *testing.T) {
	t.Helper()
	if os.Getenv("FIT_MEDIA_INTEROP") == "" {
		t.Skip("FIT_MEDIA_INTEROP is not set; scripts/streaming-interop.sh runs these checks in a container with ffmpeg")
	}
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Fatalf("%s not found: %v", name, err)
		}
	}
}

// ffmpegPublish starts ffmpeg publishing a test pattern with a tone,
// encoded as args say, to the output options and URL out, for d.
func ffmpegPublish(ctx context.Context, d time.Duration, out ...string) *exec.Cmd {
	args := []string{"-hide_banner", "-loglevel", "error", "-re",
		"-f", "lavfi", "-i", "testsrc=size=320x240:rate=25",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", fmt.Sprint(d.Seconds()),
		"-c:v", "libx264", "-preset", "veryfast", "-tune", "zerolatency", "-g", "25", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "64k"}
	return exec.CommandContext(ctx, "ffmpeg", append(args, out...)...)
}

// ffmpegPlay plays in (input options and URL) for d and returns the video
// frames it decoded, and its output when it failed.
func ffmpegPlay(ctx context.Context, d time.Duration, in ...string) (int, string, error) {
	args := append([]string{"-hide_banner", "-loglevel", "error"}, in...)
	args = append(args, "-t", fmt.Sprint(d.Seconds()), "-map", "0:v:0", "-f", "framecrc", "-")
	ctx, cancel := context.WithTimeout(ctx, d+20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	frames := 0
	sc := bufio.NewScanner(&stdout)
	for sc.Scan() {
		if line := sc.Text(); line != "" && !strings.HasPrefix(line, "#") {
			frames++
		}
	}
	return frames, stderr.String(), err
}

// TestInterop is the matrix: each ingest, with valid credentials, played
// over each egress with valid credentials, and without credentials; and
// each ingest with invalid credentials, which never publishes.
func TestInterop(t *testing.T) {
	requireFFmpeg(t)
	f := startFit(t, doc(t, `{
	  "rtmp": {"listen": "127.0.0.1:0", "auth": "live"},
	  "rtsp": {"listen": "127.0.0.1:0", "auth": "live"},
	  "srt": {"listen": "127.0.0.1:0", "auth": "live"}
	}`))
	rtmpAddr, rtspAddr, srtAddr := f.addr(t, "rtmp"), f.addr(t, "rtsp"), f.addr(t, "srt")
	httpAddr := strings.TrimPrefix(f.base, "http://")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type ingest struct {
		name, path string
		out        []string
	}
	valid := []ingest{
		{"rtmp-token", "live/rtmp", []string{"-f", "flv", "rtmp://" + rtmpAddr + "/live/rtmp?token=" + pubToken}},
		{"rtsp-digest", "live/alice", []string{"-f", "rtsp", "-rtsp_transport", "tcp", "rtsp://alice:alice-pw@" + rtspAddr + "/live/alice"}},
		{"rtsp-token", "live/rtsp", []string{"-f", "rtsp", "rtsp://" + rtspAddr + "/live/rtsp?token=" + pubToken}},
		{"srt-token", "live/srt", []string{"-f", "mpegts", "srt://" + srtAddr + "?streamid=#!::r=live/srt,m=publish,s=" + pubToken}},
	}
	invalid := []ingest{
		{"rtmp-none", "live/x1", []string{"-f", "flv", "rtmp://" + rtmpAddr + "/live/x1"}},
		{"rtmp-viewer-token", "live/x2", []string{"-f", "flv", "rtmp://" + rtmpAddr + "/live/x2?token=" + viewToken}},
		{"rtsp-wrong-password", "live/alice2", []string{"-f", "rtsp", "-rtsp_transport", "tcp", "rtsp://alice:wrong@" + rtspAddr + "/live/alice2"}},
		{"rtsp-none", "live/x3", []string{"-f", "rtsp", "-rtsp_transport", "tcp", "rtsp://" + rtspAddr + "/live/x3"}},
		{"srt-none", "live/x4", []string{"-f", "mpegts", "srt://" + srtAddr + "?streamid=#!::r=live/x4,m=publish"}},
		{"srt-viewer-token", "live/x5", []string{"-f", "mpegts", "srt://" + srtAddr + "?streamid=#!::r=live/x5,m=publish,s=" + viewToken}},
	}
	type egress struct {
		name string
		in   func(path string, creds bool) []string
	}
	tok := func(creds bool, sep string) string {
		if creds {
			return sep + "token=" + viewToken
		}
		return ""
	}
	egresses := []egress{
		{"rtmp", func(p string, c bool) []string { return []string{"-i", "rtmp://" + rtmpAddr + "/" + p + tok(c, "?")} }},
		{"rtsp-tcp", func(p string, c bool) []string {
			return []string{"-rtsp_transport", "tcp", "-i", "rtsp://" + rtspAddr + "/" + p + tok(c, "?")}
		}},
		{"rtsp-digest", func(p string, c bool) []string {
			u := "rtsp://" + rtspAddr + "/" + p
			if c {
				u = "rtsp://alice:alice-pw@" + rtspAddr + "/" + p
			}
			return []string{"-rtsp_transport", "tcp", "-i", u}
		}},
		{"rtsp-udp-falls-back", func(p string, c bool) []string { return []string{"-i", "rtsp://" + rtspAddr + "/" + p + tok(c, "?")} }},
		{"srt", func(p string, c bool) []string {
			return []string{"-i", "srt://" + srtAddr + "?streamid=" + p + tok(c, "?")}
		}},
		{"http-flv", func(p string, c bool) []string {
			return []string{"-i", "http://" + httpAddr + "/flv/" + p + ".flv" + tok(c, "?")}
		}},
		{"hls-fmp4", func(p string, c bool) []string {
			return []string{"-i", "http://" + httpAddr + "/hls/" + p + "/index.m3u8" + tok(c, "?")}
		}},
		{"hls-ts", func(p string, c bool) []string {
			return []string{"-i", "http://" + httpAddr + "/hls/" + p + "/index.m3u8?format=ts" + tok(c, "&")}
		}},
	}

	// The invalid publishers fail, and their streams never appear.
	var wg sync.WaitGroup
	for _, in := range invalid {
		wg.Go(func() {
			cmd := ffmpegPublish(ctx, 5*time.Second, in.out...)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Errorf("%s: ffmpeg published: %s", in.name, out)
			}
			if _, ok := f.role().Hub().Stream(in.path); ok {
				t.Errorf("%s: the stream %s exists", in.name, in.path)
			}
			t.Logf("%-20s refused: %s", in.name, firstLine(string(out)))
		})
	}
	wg.Wait()

	// The valid ones publish for the whole matrix.
	for _, in := range valid {
		cmd := ffmpegPublish(ctx, 120*time.Second, in.out...)
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		deadline := time.Now().Add(20 * time.Second)
		for {
			if st, ok := f.role().Hub().Stream(in.path); ok && st.Publishing {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: the stream %s did not appear: %s", in.name, in.path, out.String())
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	time.Sleep(3 * time.Second) // a segment of HLS, and a GOP in the caches

	type result struct {
		cell   string
		frames int
		err    string
	}
	var mu sync.Mutex
	var results []result
	for _, in := range valid {
		for _, eg := range egresses {
			for _, creds := range []bool{true, false} {
				wg.Go(func() {
					frames, stderr, err := ffmpegPlay(ctx, 4*time.Second, eg.in(in.path, creds)...)
					cell := fmt.Sprintf("%s -> %s (credentials %v)", in.name, eg.name, creds)
					r := result{cell: cell, frames: frames}
					switch {
					case creds && (err != nil || frames < 40):
						r.err = fmt.Sprintf("%v, %d frames: %s", err, frames, strings.TrimSpace(stderr))
					case !creds && (err == nil && frames > 0):
						r.err = fmt.Sprintf("played %d frames without credentials", frames)
					}
					mu.Lock()
					results = append(results, r)
					mu.Unlock()
				})
			}
		}
	}
	wg.Wait()
	for _, r := range results {
		if r.err != "" {
			t.Errorf("%s: %s", r.cell, r.err)
		} else {
			t.Logf("%-50s ok, %d frames", r.cell, r.frames)
		}
	}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
