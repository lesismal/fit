package medialib

import (
	"bufio"
	"encoding/json"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
)

// probed is what ffprobe says of a media file of testdata/lib: lib.probe's
// lines.
type probed struct {
	duration float64
	streams  []map[string]string
}

func readProbe(t *testing.T) map[string]*probed {
	t.Helper()
	f, err := os.Open("testdata/lib.probe")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := map[string]*probed{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		parts := strings.Split(sc.Text(), "|")
		switch parts[0] {
		case "file":
			d, err := strconv.ParseFloat(parts[2], 64)
			if err != nil {
				t.Fatal(err)
			}
			out[parts[1]] = &probed{duration: d}
		case "stream":
			kv := map[string]string{}
			for _, p := range parts[2:] {
				k, v, _ := strings.Cut(p, "=")
				kv[k] = v
			}
			out[parts[1]].streams = append(out[parts[1]].streams, kv)
		}
	}
	return out
}

// near reports whether d is within 2ms of ffprobe's seconds, which it
// writes to the microsecond, and rounds its own way.
func near(d time.Duration, seconds string) bool {
	s, err := strconv.ParseFloat(seconds, 64)
	return err == nil && math.Abs(d.Seconds()-s) <= 0.002
}

// TestCatalog scans a library of ffmpeg-made files and finds every item,
// with the durations and codecs ffprobe gives, its renditions, audio,
// subtitles and poster grouped by their names or its sidecar, and a broken
// file listed with its error.
func TestCatalog(t *testing.T) {
	dir := copyLib(t)
	_, l := open(t, config.MediaLibrary{Name: "lib", Roots: []string{dir}})
	cat := scanned(t, l, 1)
	var ids []string
	for _, it := range cat.Items {
		ids = append(ids, it.ID)
	}
	want := []string{"broken", "clips/Av1-Opus-" + pathID("clips/Av1 Opus")[len("clips/Av1-Opus-"):], "clips/late", "films/bbb", "sintel"}
	if strings.Join(ids, " ") != strings.Join(want, " ") {
		t.Fatalf("items %q, want %q", ids, want)
	}
	if len(cat.Problems) != 0 || cat.Truncated != "" || cat.Bytes <= 0 || cat.Scanned.IsZero() {
		t.Errorf("problems %v, truncated %q, bytes %d, scanned %v", cat.Problems, cat.Truncated, cat.Bytes, cat.Scanned)
	}

	probes := readProbe(t)
	checked := 0
	for _, it := range cat.Items {
		for _, list := range [][]*Rendition{it.Renditions, it.Audio} {
			for _, r := range list {
				p := probes[r.Path]
				if p == nil {
					if r.Path != "broken.mp4" {
						t.Errorf("%s: not in lib.probe", r.Path)
					}
					continue
				}
				checked++
				if r.Error != "" {
					t.Errorf("%s: %s", r.Path, r.Error)
					continue
				}
				if math.Abs(r.Duration.Seconds()-p.duration) > 0.002 {
					t.Errorf("%s: duration %v, ffprobe %v", r.Path, r.Duration, p.duration)
				}
				var tracks []Track
				for _, tr := range r.Tracks {
					if tr.Kind == KindVideo || tr.Kind == KindAudio {
						tracks = append(tracks, tr)
					}
				}
				if len(tracks) != len(p.streams) {
					t.Errorf("%s: %d tracks, ffprobe %d streams", r.Path, len(tracks), len(p.streams))
					continue
				}
				for i, tr := range tracks {
					s := p.streams[i]
					codec := strings.Replace(s["codec_name"], "hevc", "h265", 1)
					if tr.Codec != codec || !near(tr.Start, s["start_time"]) || !near(tr.Duration, s["duration"]) {
						t.Errorf("%s track %d: %s from %v for %v, ffprobe %v", r.Path, i, tr.Codec, tr.Start, tr.Duration, s)
					}
					if tr.Kind == KindVideo && (strconv.Itoa(tr.Width) != s["width"] || strconv.Itoa(tr.Height) != s["height"]) {
						t.Errorf("%s track %d: %dx%d, ffprobe %v", r.Path, i, tr.Width, tr.Height, s)
					}
					if tr.Kind == KindAudio && (strconv.Itoa(tr.SampleRate) != s["sample_rate"] || strconv.Itoa(tr.Channels) != s["channels"]) {
						t.Errorf("%s track %d: %d Hz %d channels, ffprobe %v", r.Path, i, tr.SampleRate, tr.Channels, s)
					}
					if tr.CodecString == "" || tr.Samples == 0 || tr.TableBytes == 0 {
						t.Errorf("%s track %d: %+v", r.Path, i, tr)
					}
				}
				if r.MP4() == nil || r.Bitrate <= 0 || r.Codecs == "" {
					t.Errorf("%s: mp4 %v, bitrate %d, codecs %q", r.Path, r.MP4(), r.Bitrate, r.Codecs)
				}
			}
		}
	}
	if checked != len(probes) {
		t.Errorf("checked %d files, lib.probe has %d", checked, len(probes))
	}

	bbb := cat.Item("films/bbb")
	if bbb.Title != "bbb" || bbb.Root != dir || bbb.Path != "films/bbb" || bbb.Error != "" || bbb.Sidecar != nil {
		t.Errorf("bbb: %+v", bbb)
	}
	if len(bbb.Renditions) != 2 || bbb.Renditions[0].Name != "360p" || bbb.Renditions[0].Width != 128 || bbb.Renditions[1].Name != "" ||
		bbb.Renditions[1].Path != "films/bbb.mp4" || bbb.Renditions[0].Codecs != "avc1.64000b,mp4a.40.2" && bbb.Renditions[0].Codecs == "" {
		t.Errorf("bbb's renditions: %+v", bbb.Renditions)
	}
	if len(bbb.Audio) != 1 || bbb.Audio[0].Name != "audio-fr" || bbb.Audio[0].Language != "fr" || bbb.Audio[0].Width != 0 {
		t.Errorf("bbb's audio: %+v", bbb.Audio)
	}
	if len(bbb.Subtitles) != 2 {
		t.Fatalf("bbb's subtitles: %+v", bbb.Subtitles)
	}
	if s := bbb.Subtitles[0]; s.Path != "films/bbb.en.srt" || s.Format != "srt" || s.Language != "en" || s.Name != "en" || s.Forced || s.Error != "" {
		t.Errorf("bbb's first subtitle: %+v", s)
	}
	if s := bbb.Subtitles[1]; s.Path != "films/bbb.fr.forced.vtt" || s.Format != "vtt" || s.Language != "fr" || !s.Forced {
		t.Errorf("bbb's second subtitle: %+v", s)
	}
	if bbb.Poster == nil || bbb.Poster.Path != "films/bbb.jpg" || bbb.Duration != time.Second {
		t.Errorf("bbb's poster %+v, duration %v", bbb.Poster, bbb.Duration)
	}
	var size int64
	for _, f := range []*File{&bbb.Renditions[0].File, &bbb.Renditions[1].File, &bbb.Audio[0].File, &bbb.Subtitles[0].File, &bbb.Subtitles[1].File, bbb.Poster} {
		size += f.Size
	}
	if bbb.Size != size {
		t.Errorf("bbb's size %d, its files' %d", bbb.Size, size)
	}

	sintel := cat.Item("sintel")
	if sintel.Title != "Sintel" || sintel.Sidecar == nil || sintel.Sidecar.Path != "films/sintel.json" || sintel.Poster == nil || sintel.Poster.Path != "films/art/sintel.png" {
		t.Errorf("sintel: %+v", sintel)
	}
	if len(sintel.Renditions) != 2 || sintel.Renditions[0].Name != "hd" || sintel.Renditions[0].Tracks[0].Codec != "h265" ||
		sintel.Renditions[1].Name != "sd" || sintel.Renditions[1].Tracks[1].Codec != "opus" {
		t.Errorf("sintel's renditions: %+v", sintel.Renditions)
	}
	if len(sintel.Subtitles) != 1 || sintel.Subtitles[0].Name != "中文" || sintel.Subtitles[0].Language != "zh" || !sintel.Subtitles[0].Default {
		t.Errorf("sintel's subtitles: %+v", sintel.Subtitles)
	}

	late := cat.Item("clips/late")
	if a := late.Renditions[0].Tracks[1]; a.Start != 178*time.Millisecond || a.Duration != 832*time.Millisecond {
		t.Errorf("late's audio: %+v", a)
	}

	broken := cat.Item("broken")
	if broken.Error != "not an MP4 file, or one cut short before its moov box" || len(broken.Renditions) != 1 || broken.Renditions[0].MP4() != nil {
		t.Errorf("broken: %+v", broken)
	}
	if cat.Item("nothing") != nil || (*Catalog)(nil).Item("x") != nil || l.Item("sintel") != sintel {
		t.Error("Item")
	}
	if got := l.Roots(); len(got) != 1 || got[0] != dir {
		t.Errorf("Roots: %v", got)
	}

	// As the Admin API shows it.
	data, err := json.Marshal(sintel)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	r0 := back["renditions"].([]any)[0].(map[string]any)
	if back["id"] != "sintel" || back["duration"] != 1.0 || r0["name"] != "hd" || r0["path"] != "films/sintel-hd.mp4" ||
		r0["tracks"].([]any)[0].(map[string]any)["codec"] != "h265" || len(back["audio"].([]any)) != 0 ||
		back["subtitles"].([]any)[0].(map[string]any)["language"] != "zh" {
		t.Errorf("sintel as JSON: %s", data)
	}
}

func TestPathID(t *testing.T) {
	for _, c := range []struct{ rel, want string }{
		{"films/bbb", "films/bbb"},
		{"a_b/c~d.e-f", "a_b/c~d.e-f"},
		{"films/Big Buck Bunny", "films/Big-Buck-Bunny-"},
		{"电影/片", "x/x-"},
		{".hidden/x", "hidden/x-"},
	} {
		got := pathID(c.rel)
		if !strings.HasPrefix(got, c.want) || !ValidID(got) || got != pathID(c.rel) {
			t.Errorf("pathID(%q) = %q, want %q...", c.rel, got, c.want)
		}
		if strings.HasSuffix(c.want, "-") && len(got) != len(c.want)+8 {
			t.Errorf("pathID(%q) = %q: no hash", c.rel, got)
		}
	}
	if pathID("a b") == pathID("a-b") || pathID("a b") == pathID("a  b") {
		t.Error("two paths make one ID")
	}
	long := strings.Repeat("abcdefgh/", 40) + "x y"
	if got := pathID(long); !ValidID(got) || !strings.HasPrefix(got, "abcdefgh/") {
		t.Errorf("pathID of a long path: %q", got)
	}
	for _, id := range []string{"", ".x", "a//b", "a/", strings.Repeat("a", 129), strings.Repeat("a/", 200), "a b"} {
		if ValidID(id) {
			t.Errorf("ValidID(%q)", id)
		}
	}
}

func TestPatterns(t *testing.T) {
	for _, c := range []struct {
		pattern, rel string
		want         bool
	}{
		{"*.mp4", "a/b/c.mp4", true},
		{"*.mp4", "a/b/c.m4a", false},
		{"films/*.mp4", "films/a.mp4", true},
		{"films/*.mp4", "films/x/a.mp4", false},
		{"films/**/*.mp4", "films/a.mp4", true},
		{"films/**/*.mp4", "films/x/y/a.mp4", true},
		{"**/extras/**", "films/extras/a.mp4", true},
		{"**/extras/**", "films/extras", true},
		{"**/extras/**", "films/x/a.mp4", false},
		{"films/**", "films", true},
		{"a/b", "a", false},
	} {
		if got := newPattern(c.pattern).match(c.rel); got != c.want {
			t.Errorf("%q matches %q: %v, want %v", c.pattern, c.rel, got, c.want)
		}
	}
}
