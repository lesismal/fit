package vod

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fib/media/av"
	"github.com/lesismal/fib/media/mp4"

	"github.com/lesismal/fit/medialib"
)

// TestContainerTS checks container ts: index.m3u8 is MPEG-TS's.
func TestContainerTS(t *testing.T) {
	f := startVOD(t, copyLib(t), "", `{"library": "films", "container": "ts"}`, "")
	if b := string(f.mustGet(t, "/vod/films/films/movie/index.m3u8", 200)); !strings.Contains(b, "v0.ts.m3u8") {
		t.Errorf("%s", b)
	}
}

// TestNotCarried checks what the library lists and HLS does not carry
// here: MP3, a broken rendition, and a subtitle past its bound.
func TestNotCarried(t *testing.T) {
	root := copyLib(t)
	if err := os.WriteFile(filepath.Join(root, "films", "movie.4k.mp4"), []byte("not an mp4 file at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := startVOD(t, root, `, "max_subtitle_bytes": 10`, `{"library": "films"}`, "")
	f.mustGet(t, "/vod/films/music/old/index.m3u8", 404)
	f.mustGet(t, "/vod/films/music/old/a0.init.mp4", 404)
	f.mustGet(t, "/vod/films/music/old/a0.fmp4.m3u8", 404)
	f.mustGet(t, "/vod/films/music/old/a0.0.m4s", 404)
	b := string(f.mustGet(t, "/vod/films/films/movie/index.m3u8", 200))
	if strings.Contains(b, "SUBTITLES") || strings.Count(b, "EXT-X-STREAM-INF") != 2 {
		t.Errorf("a broken rendition or a subtitle past its bound listed:\n%s", b)
	}
	f.mustGet(t, "/vod/films/films/movie/s0.vtt", 404)
}

// TestVTTKept checks a subtitle served twice is read once.
func TestVTTKept(t *testing.T) {
	f := startVOD(t, copyLib(t), "", `{"library": "films"}`, "")
	a := f.mustGet(t, "/vod/films/films/movie/s0.vtt", 200)
	if b := f.mustGet(t, "/vod/films/films/movie/s0.vtt", 200); !bytes.Equal(a, b) {
		t.Error("the subtitle changed")
	}
}

// TestPoolGivesUp warms the library's tables, and then has the pool give up
// the work the handler queues on it, past file_timeout in its queue, or
// refuse it with its queue full: 503 for a plan and for a segment.
func TestPoolGivesUp(t *testing.T) {
	root := copyLib(t)
	f := startVOD(t, root, `, "file_workers": 1, "file_queue": 2, "file_timeout": "100ms"`, `{"library": "films"}`, "")
	// The tables and the plan of v0, kept.
	f.mustGet(t, "/vod/films/films/movie/v0.fmp4.m3u8", 200)
	doc := func(params string) string {
		return `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "media_library": [` + libConf(root, `, "file_workers": 1, "file_queue": 2, "file_timeout": "100ms"`) + `],
  "routes": [{"match": {"path": ["/vod/*"]}, "handler": {"module": "vod", "params": ` + params + `}}]}`
	}
	// settle waits until the request has been queued past the timeout, or
	// refused.
	var refused uint64
	settle := func(t *testing.T, f *fit, full bool) {
		if !full {
			time.Sleep(250 * time.Millisecond)
			return
		}
		deadline := time.Now().Add(10 * time.Second)
		for f.lib.Pool().Stats().Refused == refused {
			if time.Now().After(deadline) {
				t.Fatal("the request was never refused")
			}
			time.Sleep(time.Millisecond)
		}
	}
	for _, full := range []bool{false, true} {
		release := block(t, f.lib)
		refused = f.lib.Pool().Stats().Refused
		if full && !fill(f.lib, 2) {
			t.Fatal("the queue refused")
		}
		// The segment: its plan is kept; its build waits past the timeout,
		// or is refused.
		done := make(chan int, 1)
		go func() {
			resp, _, err := fetch("GET", f.base+"/vod/films/films/movie/v0.1.m4s")
			if err != nil {
				done <- 0
				return
			}
			done <- resp.StatusCode
		}()
		settle(t, f, full)
		release()
		if st := <-done; st != 503 {
			t.Errorf("segment, queue full %v: %d", full, st)
		}
		// The plan: a reload of another segment_duration has none kept,
		// while the library keeps the tables.
		if full {
			f.swap(t, doc(`{"library": "films", "segment_duration": "3s"}`))
		} else {
			f.swap(t, doc(`{"library": "films", "segment_duration": "2s"}`))
		}
		release = block(t, f.lib)
		refused = f.lib.Pool().Stats().Refused
		if full && !fill(f.lib, 2) {
			t.Fatal("the queue refused")
		}
		go func() {
			resp, _, err := fetch("GET", f.base+"/vod/films/films/movie/v0.fmp4.m3u8")
			if err != nil {
				done <- 0
				return
			}
			done <- resp.StatusCode
		}()
		settle(t, f, full)
		release()
		if st := <-done; st != 503 {
			t.Errorf("plan, queue full %v: %d", full, st)
		}
	}
}

// fill queues n jobs that do nothing on l's pool, and reports whether it
// took them all.
func fill(l *medialib.Library, n int) bool {
	for range n {
		if !l.Submit(func() {}, nil) {
			return false
		}
	}
	return true
}

// TestChangedWithoutTables changes a file whose plan is kept and whose
// tables the library has dropped for others: the segment's tables fail,
// 404.
func TestChangedWithoutTables(t *testing.T) {
	root := copyLib(t)
	// movie.mp4's tables take some 6.5 KB, hevc.mp4's 4.5 KB.
	f := startVOD(t, root, `, "max_table_bytes": 7000`, `{"library": "films", "segment_duration": "2s"}`, "")
	f.mustGet(t, "/vod/films/films/movie/v1.fmp4.m3u8", 200)
	// Others' tables push movie.mp4's out.
	f.mustGet(t, "/vod/films/clips/hevc/v0.fmp4.m3u8", 200)
	if st := f.lib.Tables(); st.Bytes > 7000 {
		t.Fatalf("%+v", st)
	}
	movie := filepath.Join(root, "films", "movie.mp4")
	later := time.Now().Add(5 * time.Second)
	if err := os.Chtimes(movie, later, later); err != nil {
		t.Fatal(err)
	}
	f.mustGet(t, "/vod/films/films/movie/v1.1.m4s", 404)
	// And a playlist not yet built of a changed file.
	audio := filepath.Join(root, "films", "movie.audio-fr.m4a")
	if err := os.Chtimes(audio, later, later); err != nil {
		t.Fatal(err)
	}
	f.mustGet(t, "/vod/films/films/movie/a0.ts.m3u8", 404)
}

// failing is a ReaderAt that fails.
type failing struct{}

func (failing) ReadAt([]byte, int64) (int, error) { return 0, errors.New("the disk failed") }

// TestBuildEdges checks segment building where the file fails to read, a
// track has no samples in a segment, and MPEG-TS carries none of the
// tracks.
func TestBuildEdges(t *testing.T) {
	vt, at, file := tablesOf(t, "films/movie.mp4")
	p, err := buildPlan(vt, at, 2*time.Second, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	src := &source{vt: vt, at: at, vtrack: av1Track(t, "films/movie.mp4", 0), atrack: av1Track(t, "films/movie.mp4", 1)}
	in, err := buildInit(src.tracks())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := buildFMP4(failing{}, p, 0, src, in); err == nil || !strings.Contains(err.Error(), "the disk failed") {
		t.Errorf("fMP4 of a failing disk: %v", err)
	}
	if _, err := buildTS(failing{}, p, 0, src); err == nil {
		t.Error("MPEG-TS of a failing disk")
	}
	if l := collect(vt, 5, 5); len(l.s) != 0 {
		t.Error("samples of an empty range")
	}
	if tf := trackFragment(1, &samples{}, newClock(vt, 90000), true); len(tf.Samples) != 0 {
		t.Error("a fragment of no samples")
	}
	if _, err := buildTS(bytes.NewReader(file), p, 0, &source{vt: vt, vtrack: &av.Track{Codec: av.CodecAV1}}); err == nil {
		t.Error("MPEG-TS of AV1")
	}
	// Video samples that are not AVCC.
	bad := make([]byte, len(file))
	for i := range bad {
		bad[i] = 0xff
	}
	if _, err := buildTS(bytes.NewReader(bad), p, 0, &source{vt: vt, vtrack: src.vtrack}); err == nil {
		t.Error("MPEG-TS of video that is not AVCC")
	}
}

// TestAudioPastVideo cuts a file whose audio goes on long after its video:
// the audio past it goes in segments of its own, within maxBytes.
func TestAudioPastVideo(t *testing.T) {
	vt, _, _ := tablesOf(t, "clips/silent.mp4")
	_, at, _ := tablesOf(t, "films/movie.mp4")
	p, err := buildPlan(vt, at, 2*time.Second, 3000)
	if err != nil {
		t.Fatal(err)
	}
	extra := 0
	for n, s := range p.segs {
		if int(s.v) == vt.Len() {
			extra++
			if from, to := p.videoRange(n, vt); from != to {
				t.Errorf("segment %d past the video has video %d-%d", n, from, to)
			}
		}
		if s.bytes > 3000+2000 {
			t.Errorf("segment %d of %d bytes", n, s.bytes)
		}
	}
	if extra == 0 {
		t.Fatalf("no segment of the audio past the video: %+v", p.segs)
	}
	if p.end < 5*time.Second {
		t.Errorf("ends at %v", p.end)
	}
	var mt *mp4.Table = at
	if from, to := p.audioRange(len(p.segs)-1, mt); to != at.Len() || from >= to {
		t.Errorf("the last segment's audio %d-%d", from, to)
	}
}
