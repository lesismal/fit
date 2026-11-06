package medialib

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fib/media/av"
	"github.com/lesismal/fib/media/mp4"

	"github.com/lesismal/fit/config"
)

// TestOddFiles lists a file of a codec the library does not serve, and a
// file cut short in its media data, whose table is refused.
func TestOddFiles(t *testing.T) {
	dir := t.TempDir()
	if err := copyFile(filepath.Join("testdata", "odd", "mjpeg.mp4"), filepath.Join(dir, "mjpeg.mp4")); err != nil {
		t.Fatal(err)
	}
	// bbb.360p.mp4 has its moov at the front: cut in its media data, it
	// opens, and its tables point past its end.
	data, err := os.ReadFile(filepath.Join("testdata", "lib", "films", "bbb.360p.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cut.mp4"), data[:len(data)/2], 0o644); err != nil {
		t.Fatal(err)
	}
	_, l := open(t, config.MediaLibrary{Name: "odd", Roots: []string{dir}})
	cat := scanned(t, l, 1)
	if it := cat.Item("mjpeg"); it == nil || !strings.HasPrefix(it.Error, "no H.264, H.265 or AV1 video") || it.Renditions[0].Tracks[0].Codec != "unknown" {
		t.Errorf("mjpeg: %+v", it)
	}
	cut := cat.Item("cut")
	if cut == nil || cut.Error != "" {
		t.Fatalf("cut: %+v", cut)
	}
	if _, err := get(t, func(fn func(*mp4.Table, error)) { l.Table(cut.Renditions[0], 0, fn) }); !errors.Is(err, ErrNotServed) || !errors.Is(err, mp4.ErrSampleData) {
		t.Errorf("the table of a file cut short: %v", err)
	}
	if l.Name() != "odd" {
		t.Error(l.Name())
	}
}

// TestDescribe tells tracks without an edit list, a movie without a
// duration, and the kinds and codecs of tracks.
func TestDescribe(t *testing.T) {
	m := &mp4.File{Tracks: []*mp4.Track{
		{Handler: "vide", Duration: 2 * time.Second, Track: &av.Track{Codec: av.CodecH264, Width: 16, Height: 8}},
		{Handler: "soun", Duration: 3 * time.Second, Track: &av.Track{Codec: av.CodecAAC}},
		{Handler: "text", Track: &av.Track{}},
		{Handler: "hint", Track: &av.Track{}},
	}}
	r := &Rendition{File: File{Size: 3000}}
	describe(r, m)
	if r.Error != "" || r.Duration != 3*time.Second || r.Bitrate != 8000 || r.Width != 16 || r.Codecs != "mp4a.40.2" {
		t.Errorf("%+v", r)
	}
	var kinds []string
	for _, tr := range r.Tracks {
		kinds = append(kinds, tr.Kind+"/"+tr.Codec)
	}
	if strings.Join(kinds, " ") != "video/h264 audio/aac subtitle/unknown other/unknown" {
		t.Errorf("tracks: %v", kinds)
	}
	for c, want := range map[av.CodecID]string{av.CodecH265: "h265", av.CodecAV1: "av1", av.CodecOpus: "opus", av.CodecMP3: "mp3", av.CodecPCMU: "pcmu", av.CodecPCMA: "pcma", av.CodecID(99): "unknown"} {
		if got := codecName(c); got != want {
			t.Errorf("codecName(%v) = %q", c, got)
		}
	}
	for err, want := range map[error]string{
		mp4.ErrNoMoov: "not an MP4 file", mp4.ErrFragmented: "a fragmented MP4 file", mp4.ErrTooLarge: "past the reader's limits",
		mp4.ErrMalformed: "a malformed MP4 file", errors.New("other"): "other",
	} {
		if got := probeError(err); !strings.Contains(got, want) {
			t.Errorf("probeError(%v) = %q", err, got)
		}
	}
	if long := pathID(strings.Repeat("a", 100) + " b"); !strings.HasPrefix(long, strings.Repeat("a", 64)+"-") || !ValidID(long) {
		t.Errorf("a long name: %q", long)
	}
}

// TestOpenFile refuses what is not a regular file, and tells a file gone
// from one that cannot be read.
func TestOpenFile(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openFile(&File{abs: dir, real: dir}, false); err == nil || err.Error() != "not a regular file" {
		t.Errorf("a directory: %v", err)
	}
	if _, err := openFile(&File{abs: filepath.Join(dir, "none"), real: dir}, false); !errors.Is(err, ErrChanged) {
		t.Errorf("a file gone: %v", err)
	}
	other := errors.New("other")
	if notFound(other) != other || notFound(fs.ErrNotExist) != ErrChanged {
		t.Error("notFound")
	}
}

// TestInterval scans every scan_interval, and takes a new interval at once.
func TestInterval(t *testing.T) {
	key, l := open(t, config.MediaLibrary{Name: "lib", Roots: []string{t.TempDir()}, ScanInterval: config.Duration(time.Hour)})
	scanned(t, l, 1)
	for range 3 {
		// The second fills the channel; the third finds it full.
		l.apply(&settings{roots: l.set.Load().roots, interval: 20 * time.Millisecond, probes: 1, tableBytes: 1, workers: l.Pool().Stats().Workers, queue: l.Pool().Stats().Queue, timeout: config.DefaultMediaFileTimeout, maxItems: 1, maxFiles: 10, catalogBytes: 1 << 20})
	}
	scanned(t, l, 4)
	_ = key
}

// TestScanFails covers a scan whose pool gives up every job, one removed
// while it probes, a directory that cannot be read, and roots past
// max_files.
func TestScanFails(t *testing.T) {
	dir := copyLib(t)
	_, l := open(t, config.MediaLibrary{Name: "lib", Roots: []string{dir}})
	scanned(t, l, 1)

	// Every job waits past the timeout: sidecars and probes are lost.
	expiring := NewPool(1, 64, -1)
	t.Cleanup(func() { expiring.Close(); expiring.Wait() })
	l.pool.Store(expiring)
	touch(t, filepath.Join(dir, "films", "bbb.mp4"))
	cat := rescan(t, l)
	if r := cat.Item("films/bbb").Renditions[1]; r.Error != ErrBusy.Error() || !r.transient {
		t.Errorf("a probe the pool gave up: %+v", r)
	}
	if !strings.Contains(problemText(cat), "films/sintel.json: the sidecar: "+ErrBusy.Error()) {
		t.Errorf("a sidecar the pool gave up: %s", problemText(cat))
	}
	if cat.Item("films/bbb").Renditions[0].Error != "" {
		t.Error("an unchanged file was probed again")
	}

	// A sidecar removed after the walk, a file swapped for a link out of
	// its root: each job first does it.
	if runtime.GOOS != "windows" {
		out := filepath.Join(t.TempDir(), "out.mp4")
		if err := copyFile(filepath.Join(dir, "clips", "late.mp4"), out); err != nil {
			t.Fatal(err)
		}
		hooked := NewPool(1, 64, time.Minute)
		t.Cleanup(func() { hooked.Close(); hooked.Wait() })
		var once sync.Once
		hook := func() {
			once.Do(func() {
				_ = os.Remove(filepath.Join(dir, "films", "sintel.json"))
				late := filepath.Join(dir, "clips", "late.mp4")
				fi, _ := os.Stat(late)
				_ = os.Remove(late)
				_ = os.Symlink(out, late)
				_ = os.Chtimes(out, fi.ModTime(), fi.ModTime())
			})
		}
		hooked.hook.Store(&hook)
		l.pool.Store(hooked)
		touch(t, filepath.Join(dir, "clips", "late.mp4"))
		cat = rescan(t, l)
		if !strings.Contains(problemText(cat), "films/sintel.json: the sidecar: "+ErrChanged.Error()) {
			t.Errorf("a sidecar removed while scanned: %s", problemText(cat))
		}
		if r := cat.Item("clips/late").Renditions[0]; r.Error != ErrOutside.Error() {
			t.Errorf("a file swapped for a link out of its root: %+v", r)
		}
	}

	// Removed while it probes: the scan ends without a catalog.
	stopping := NewPool(1, 64, time.Minute)
	t.Cleanup(func() { stopping.Close(); stopping.Wait() })
	remove := func() { l.remove() }
	stopping.hook.Store(&remove)
	l.pool.Store(stopping)
	touch(t, filepath.Join(dir, "films", "bbb.360p.mp4"))
	before := l.Status().Scans
	idle(t, l)
	l.Rescan()
	<-l.done
	if l.Status().Scans != before {
		t.Error("a scan stopped by a removal ended")
	}

	// Two roots past max_files: the second is not walked.
	_, f := open(t, config.MediaLibrary{Name: "files", Roots: []string{filepath.Join(dir, "films"), filepath.Join(dir, "clips")}, MaxFiles: 3})
	if cat := scanned(t, f, 1); !strings.Contains(cat.Truncated, "max_files") || len(cat.Items) != 0 {
		t.Errorf("max_files: %+v", cat)
	}

	// A directory that cannot be read.
	if runtime.GOOS != "windows" && os.Getuid() != 0 {
		locked := filepath.Join(dir, "films", "subs")
		if err := os.Chmod(locked, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
		_, d := open(t, config.MediaLibrary{Name: "locked", Roots: []string{dir}})
		if cat := scanned(t, d, 1); !strings.Contains(problemText(cat), "films/subs: open") {
			t.Errorf("a directory that cannot be read: %s", problemText(cat))
		}
	}
}

func problemText(cat *Catalog) string {
	var b strings.Builder
	for _, p := range cat.Problems {
		b.WriteString(p.Path + ": " + p.Error + "\n")
	}
	return b.String()
}
