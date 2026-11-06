package vod

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/medialib"
)

// The ffmpeg interop checks: ffmpeg plays every item of a library that
// test/docker/vod/genlib.sh makes, over both containers, and seeks into it;
// ffprobe finds every segment valid, starting on a keyframe with its audio
// within a frame of its video; and the files themselves play through Range.
// They run only with FIT_MEDIA_INTEROP set, with ffmpeg and ffprobe on the
// PATH, as test/docker/vod/e2e.sh runs them in a Linux container; there a
// missing ffmpeg fails them. Nothing is measured.

func requireFFmpeg(t *testing.T) {
	t.Helper()
	if os.Getenv("FIT_MEDIA_INTEROP") == "" {
		t.Skip("FIT_MEDIA_INTEROP is not set; test/docker/vod/e2e.sh runs these checks in a container with ffmpeg")
	}
	for _, name := range []string{"ffmpeg", "ffprobe", "sh"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Fatalf("%s not found: %v", name, err)
		}
	}
}

// run runs a command and returns its stdout, failing the test with its
// stderr when it fails or, with strict, says anything there.
func run(t *testing.T, strict bool, name string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err != nil || strict && strings.TrimSpace(stderr.String()) != "" {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, stderr.String())
	}
	return stdout.String()
}

// probeStreams returns, for each stream of in, its codec_type and
// start_time, and its video frame rate.
func probeStreams(t *testing.T, in string) (start map[string]float64, frame float64) {
	t.Helper()
	start = map[string]float64{}
	out := run(t, true, "ffprobe", "-v", "error", "-show_entries", "stream=codec_type,start_time,avg_frame_rate", "-of", "csv=p=0", in)
	for line := range strings.Lines(out) {
		f := strings.Split(strings.TrimSpace(line), ",")
		if len(f) < 3 {
			continue
		}
		v, _ := strconv.ParseFloat(f[2], 64)
		if _, ok := start[f[0]]; !ok {
			start[f[0]] = v
		}
		if f[0] == "video" && frame == 0 {
			if n, d, ok := strings.Cut(f[1], "/"); ok {
				nf, _ := strconv.ParseFloat(n, 64)
				df, _ := strconv.ParseFloat(d, 64)
				if nf > 0 {
					frame = df / nf
				}
			}
		}
	}
	return start, frame
}

// countFrames returns the video frames ffmpeg decodes of in, and the first
// one's time; strict fails the test on anything ffmpeg says.
func countFrames(t *testing.T, strict bool, args ...string) (int, float64) {
	t.Helper()
	out := run(t, strict, "ffmpeg", append(append([]string{"-hide_banner", "-v", "error"}, args...), "-map", "0:v:0", "-f", "framecrc", "-")...)
	frames, first := 0, math.NaN()
	tb := 0.0
	for line := range strings.Lines(out) {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#tb 0:") {
			n, d, _ := strings.Cut(strings.TrimSpace(strings.TrimPrefix(line, "#tb 0:")), "/")
			nf, _ := strconv.ParseFloat(n, 64)
			df, _ := strconv.ParseFloat(d, 64)
			tb = nf / df
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if frames == 0 {
			f := strings.Split(line, ",")
			if len(f) > 2 {
				pts, _ := strconv.ParseFloat(strings.TrimSpace(f[2]), 64)
				first = pts * tb
			}
		}
		frames++
	}
	return frames, first
}

// TestInterop is the matrix: each item of the generated library in each
// container, played, sought into and probed segment by segment.
func TestInterop(t *testing.T) {
	requireFFmpeg(t)
	root := t.TempDir()
	script, err := filepath.Abs("../../test/docker/vod/genlib.sh")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	run(t, false, "sh", script, root)
	t.Logf("library made in %v", time.Since(start).Round(time.Millisecond))
	f := startVOD(t, root, "", `{"library": "films", "segment_duration": "4s"}`, "")
	ver := strings.SplitN(run(t, true, "ffmpeg", "-hide_banner", "-version"), "\n", 2)[0]
	t.Log(ver)
	work := t.TempDir()
	for _, it := range f.lib.Catalog().Items {
		if it.Error != "" {
			t.Fatalf("%s: %s", it.ID, it.Error)
		}
		base := f.base + "/vod/films/" + it.ID + "/"
		for _, c := range []string{"fmp4", "ts"} {
			master := base + "index." + c + ".m3u8"
			t.Run(it.ID+"/"+c, func(t *testing.T) {
				if resp, _ := f.get(t, "/vod/films/"+it.ID+"/index."+c+".m3u8"); resp.StatusCode == 404 {
					if c == "ts" && strings.Contains(it.Renditions[0].Codecs, "av01") {
						t.Skip("MPEG-TS does not carry AV1")
					}
					t.Fatal("no playlist")
				}
				// The whole of it plays, every stream, without an error.
				run(t, true, "ffmpeg", "-hide_banner", "-v", "error", "-i", master, "-map", "0:v?", "-map", "0:a?", "-f", "null", "-")
				list := append(refs(it.Renditions, "v"), refs(it.Audio, "a")...)
				for _, ref := range list {
					media := base + ref.ref + "." + c + ".m3u8"
					src := filepath.Join(ref.root, filepath.FromSlash(ref.path))
					srcStart, frame := probeStreams(t, src)
					gotStart, _ := probeStreams(t, media)
					// Audio and video start as the file has them, within a
					// frame of each other.
					if v, ok := srcStart["video"]; ok {
						if a, ok := srcStart["audio"]; ok {
							want, got := a-v, gotStart["audio"]-gotStart["video"]
							if math.Abs(want-got) > frame {
								t.Errorf("%s: audio starts %.3fs after video, the file %.3fs", ref.ref, got, want)
							}
						}
						// Every frame plays, and a seek plays on from
						// there, wherever this ffmpeg seeks in HLS at all:
						// some (7.1) play nothing after -ss in fMP4 HLS, or
						// in H.265 over MPEG-TS, their own hls muxer's
						// included, which is checked first.
						want, _ := countFrames(t, true, "-i", src)
						got, _ := countFrames(t, true, "-i", media)
						if got != want {
							t.Errorf("%s: %d frames played, the file has %d", ref.ref, got, want)
						}
						seekTo := fmt.Sprint(math.Floor(it.Duration.Seconds() * 0.55))
						if !ffmpegSeeks(t, work, src, c, seekTo) {
							t.Logf("%s: this ffmpeg plays nothing after a seek in its own %s HLS of the file: not checked", ref.ref, c)
						} else if n, first := countFrames(t, true, "-ss", seekTo, "-i", media, "-t", "1"); n == 0 || math.IsNaN(first) {
							t.Errorf("%s: nothing played after a seek to %ss", ref.ref, seekTo)
						}
					}
					checkSegments(t, f, work, it.ID, ref.ref, c, frame)
				}
			})
		}
		// The files themselves, through Range.
		for _, ref := range append(refs(it.Renditions, "v"), refs(it.Audio, "a")...) {
			ext := strings.TrimPrefix(filepath.Ext(ref.path), ".")
			run(t, true, "ffprobe", "-v", "error", "-show_entries", "format=duration", base+ref.ref+"."+ext)
		}
	}
}

// ffmpegSeeks reports whether ffmpeg plays on after a seek to seekTo in its
// own HLS of src, cut with its hls muxer into segments of container c.
func ffmpegSeeks(t *testing.T, work, src, c, seekTo string) bool {
	t.Helper()
	dir, err := os.MkdirTemp(work, "ref")
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"-hide_banner", "-v", "error", "-i", src, "-c", "copy", "-f", "hls", "-hls_time", "4", "-hls_playlist_type", "vod"}
	if c == "fmp4" {
		args = append(args, "-hls_segment_type", "fmp4")
	}
	list := filepath.Join(dir, "index.m3u8")
	run(t, true, "ffmpeg", append(args, list)...)
	// At two places, one past a segment's start: an ffmpeg that plays on
	// after one only, by chance, does not seek in HLS.
	at, _ := strconv.ParseFloat(seekTo, 64)
	for _, ss := range []string{seekTo, fmt.Sprint(at + 1.5)} {
		if n, _ := countFrames(t, false, "-ss", ss, "-i", list, "-t", "1"); n == 0 {
			return false
		}
	}
	return true
}

// fileRefT is a rendition or audio file of an item: its name in URLs, as
// v0, and where it is.
type fileRefT struct{ ref, root, path string }

func refs(list []*medialib.Rendition, kind string) []fileRefT {
	var out []fileRefT
	for i, r := range list {
		if r.Error == "" {
			out = append(out, fileRefT{ref: kind + strconv.Itoa(i), root: r.Root, path: r.Path})
		}
	}
	return out
}

// checkSegments fetches each segment of a media playlist, and has ffprobe
// read it as a player starting there would: an fMP4 segment after its
// initialization section, an MPEG-TS one alone. ffprobe must find nothing
// wrong; a segment with video must start with a keyframe, and its audio
// within a frame of its video.
func checkSegments(t *testing.T, f *fit, work, id, ref, c string, frame float64) {
	t.Helper()
	base := "/vod/films/" + id + "/"
	uris, _, initURI := segmentURIs(t, f.mustGet(t, base+ref+"."+c+".m3u8", 200))
	var init []byte
	if initURI != "" {
		init = f.mustGet(t, base+initURI, 200)
	}
	for n, u := range uris {
		seg := append(append([]byte{}, init...), f.mustGet(t, base+u, 200)...)
		p := filepath.Join(work, strings.ReplaceAll(id, "/", "_")+"-"+ref+"-"+strconv.Itoa(n)+"."+c)
		if err := os.WriteFile(p, seg, 0o644); err != nil {
			t.Fatal(err)
		}
		out := run(t, true, "ffprobe", "-v", "error", "-show_entries", "packet=codec_type,pts_time,flags", "-of", "csv=p=0", p)
		var firstVideo, firstAudio = math.NaN(), math.NaN()
		videoKey := false
		for line := range strings.Lines(out) {
			fs := strings.Split(strings.TrimSpace(line), ",")
			if len(fs) < 3 {
				continue
			}
			pts, err := strconv.ParseFloat(fs[1], 64)
			if err != nil {
				continue
			}
			switch {
			case fs[0] == "video" && math.IsNaN(firstVideo):
				firstVideo, videoKey = pts, strings.HasPrefix(fs[2], "K")
			case fs[0] == "audio" && math.IsNaN(firstAudio):
				firstAudio = pts
			}
		}
		if !math.IsNaN(firstVideo) && !videoKey {
			t.Errorf("%s segment %d starts on a frame that is not a keyframe", ref, n)
		}
		if !math.IsNaN(firstVideo) && !math.IsNaN(firstAudio) && math.Abs(firstAudio-firstVideo) > frame+0.001 {
			t.Errorf("%s segment %d: audio at %.3f, video at %.3f, past a frame", ref, n, firstAudio, firstVideo)
		}
	}
	t.Logf("%s.%s: %d segments valid", ref, c, len(uris))
}
