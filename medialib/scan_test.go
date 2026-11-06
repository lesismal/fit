package medialib

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
)

// TestRescan adds, changes and removes files, and finds each change after
// a rescan; files unchanged are not probed again.
func TestRescan(t *testing.T) {
	dir := copyLib(t)
	_, l := open(t, config.MediaLibrary{Name: "lib", Roots: []string{dir}})
	cat := scanned(t, l, 1)
	if st := l.Status(); st.Probe != 8 || st.Probed != 8 || st.Reused != 0 || st.Files == 0 || st.Started.IsZero() || st.Ended.Before(st.Started) {
		t.Errorf("the first scan: %+v", st)
	}
	before := cat.Item("films/bbb").Renditions[1]

	// Added.
	if err := copyFile(filepath.Join(dir, "films", "sintel-sd.mp4"), filepath.Join(dir, "clips", "new.mp4")); err != nil {
		t.Fatal(err)
	}
	// Changed: bbb.mp4 becomes H.265.
	if err := copyFile(filepath.Join(dir, "films", "sintel-hd.mp4"), filepath.Join(dir, "films", "bbb.mp4")); err != nil {
		t.Fatal(err)
	}
	touch(t, filepath.Join(dir, "films", "bbb.mp4"))
	// Removed.
	if err := os.Remove(filepath.Join(dir, "clips", "late.mp4")); err != nil {
		t.Fatal(err)
	}
	cat = rescan(t, l)
	if st := l.Status(); st.Probe != 2 || st.Reused != 6 {
		t.Errorf("the second scan probed %d and reused %d, want 2 and 6", st.Probe, st.Reused)
	}
	if cat.Item("clips/new") == nil || cat.Item("clips/new").Renditions[0].Tracks[1].Codec != "opus" {
		t.Errorf("the file added: %+v", cat.Item("clips/new"))
	}
	if cat.Item("clips/late") != nil {
		t.Error("the file removed is still listed")
	}
	after := cat.Item("films/bbb").Renditions[1]
	if after.Tracks[0].Codec != "h265" || after.Size == before.Size || before.Tracks[0].Codec != "h264" {
		t.Errorf("the file changed: before %+v, after %+v", before.Tracks, after.Tracks)
	}
	// The old catalog is as it was.
	if before.Tracks[0].Codec != "h264" {
		t.Error("the catalog before changed")
	}

	// A file that is no longer an MP4 file is listed with its error.
	if err := os.WriteFile(filepath.Join(dir, "clips", "new.mp4"), []byte("not a film at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	cat = rescan(t, l)
	if it := cat.Item("clips/new"); it == nil || it.Error == "" || it.Renditions[0].Error != it.Error {
		t.Errorf("the file broken: %+v", it)
	}
}

// TestPathsOut refuses what leads out of a root: a sidecar's path with ..,
// an absolute one, and a symbolic link out of the root, unless
// follow_symlinks; links within the root are followed, and one that loops
// is walked once.
func TestPathsOut(t *testing.T) {
	dir := copyLib(t)
	out := t.TempDir()
	if err := copyFile(filepath.Join(dir, "films", "bbb.mp4"), filepath.Join(out, "outside.mp4")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(out, "more"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(filepath.Join(dir, "films", "bbb.mp4"), filepath.Join(out, "more", "deep.mp4")); err != nil {
		t.Fatal(err)
	}
	links := map[string]string{
		filepath.Join(dir, "clips", "inside.mp4"): filepath.Join(dir, "films", "sintel-sd.mp4"),
		filepath.Join(dir, "clips", "out.mp4"):    filepath.Join(out, "outside.mp4"),
		filepath.Join(dir, "clips", "outdir"):     filepath.Join(out, "more"),
		filepath.Join(dir, "clips", "loop"):       dir,
		filepath.Join(dir, "clips", "gone.mp4"):   filepath.Join(out, "nothing.mp4"),
	}
	for link, target := range links {
		if err := os.Symlink(target, link); err != nil {
			if runtime.GOOS == "windows" {
				t.Skipf("no symbolic links here: %v", err)
			}
			t.Fatal(err)
		}
	}
	side := `{"title": "out", "renditions": [{"file": "../../outside.mp4"}, {"file": "/etc/passwd"}, {"file": "nothing.mp4"}, {"file": "sintel.json"}], "audio": [{"file": "bbb.audio-fr.m4a"}, {"file": "bbb.audio-fr.m4a"}], "subtitles": [{"file": "x\\y.srt"}], "poster": "sintel-hd.mp4"}`
	if err := os.WriteFile(filepath.Join(dir, "films", "out.json"), []byte(side), 0o644); err != nil {
		t.Fatal(err)
	}
	_, l := open(t, config.MediaLibrary{Name: "lib", Roots: []string{dir}})
	cat := scanned(t, l, 1)
	if cat.Item("clips/inside") == nil || cat.Item("clips/inside").Error != "" {
		t.Errorf("a link within the root: %+v", cat.Item("clips/inside"))
	}
	for _, id := range []string{"clips/out", "clips/outdir/deep"} {
		if cat.Item(id) != nil {
			t.Errorf("%s, out of the root, is listed", id)
		}
	}
	if cat.Item("clips/loop/films/bbb") != nil {
		t.Error("a link back to the root is walked")
	}
	problems := map[string]string{}
	for _, p := range cat.Problems {
		problems[p.Path] += p.Error + "; "
	}
	for path, want := range map[string]string{
		"clips/out.mp4":   "a symbolic link out of the root, refused",
		"clips/outdir":    "a symbolic link out of the root, refused",
		"clips/gone.mp4":  "a symbolic link that leads nowhere",
		"films/out.json":  `"../../outside.mp4" leads out of the sidecar's directory`,
		"films/out.json ": `"/etc/passwd" leads out`,
	} {
		if !strings.Contains(problems[strings.TrimSpace(path)], want) {
			t.Errorf("%s: %q, want %q", path, problems[strings.TrimSpace(path)], want)
		}
	}
	for _, want := range []string{`"nothing.mp4" is not a file the library looks at`, `"sintel.json" is not a media file`, `"x\\y.srt" leads out`, `poster "sintel-hd.mp4" is not a picture`, `file "bbb.audio-fr.m4a" is named by a sidecar already`} {
		if !strings.Contains(problems["films/out.json"], want) {
			t.Errorf("films/out.json: %q, want %q", problems["films/out.json"], want)
		}
	}
	if it := cat.Item("films/out"); it == nil || it.Error != "" || len(it.Audio) != 1 || len(cat.Item("films/bbb").Audio) != 0 {
		t.Errorf("the sidecar's item: %+v", it)
	}

	// A file changed into a link out of its root after the scan.
	r := cat.Item("clips/inside").Renditions[0]
	if h, err := get(t, func(fn func(*Handle, error)) { l.Open(&r.File, fn) }); err != nil {
		t.Fatal(err)
	} else {
		h.Close()
	}
	link := filepath.Join(dir, "clips", "inside.mp4")
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(out, "outside.mp4"), link); err != nil {
		t.Fatal(err)
	}
	if _, err := get(t, func(fn func(*Handle, error)) { l.Open(&r.File, fn) }); !errors.Is(err, ErrOutside) {
		t.Errorf("a link changed to lead out: %v", err)
	}

	// With follow_symlinks.
	_, f := open(t, config.MediaLibrary{Name: "follow", Roots: []string{dir}, FollowSymlinks: true})
	cat = scanned(t, f, 1)
	for _, id := range []string{"clips/out", "clips/outdir/deep", "clips/inside"} {
		if it := cat.Item(id); it == nil || it.Error != "" {
			t.Errorf("%s with follow_symlinks: %+v", id, it)
		}
	}
}

// TestBounds holds each bound of a scan.
func TestBounds(t *testing.T) {
	dir := copyLib(t)
	var last *Library
	lib := func(name string, edit func(*config.MediaLibrary)) *Catalog {
		c := config.MediaLibrary{Name: name, Roots: []string{dir}}
		edit(&c)
		_, last = open(t, c)
		return scanned(t, last, 1)
	}
	if cat := lib("items", func(c *config.MediaLibrary) { c.MaxItems = 2 }); len(cat.Items) != 2 || !strings.Contains(cat.Truncated, "max_items") {
		t.Errorf("max_items: %d items, %q", len(cat.Items), cat.Truncated)
	}
	if cat := lib("files", func(c *config.MediaLibrary) { c.MaxFiles = 3 }); len(cat.Items) != 0 || !strings.Contains(cat.Truncated, "max_files") {
		t.Errorf("max_files: %d items, %q", len(cat.Items), cat.Truncated)
	}
	if cat := lib("bytes", func(c *config.MediaLibrary) { c.MaxCatalogBytes = 6000 }); len(cat.Items) == 0 || len(cat.Items) == 5 || cat.Bytes > 6000 || !strings.Contains(cat.Truncated, "max_catalog_bytes") {
		t.Errorf("max_catalog_bytes: %d items, %d bytes, %q", len(cat.Items), cat.Bytes, cat.Truncated)
	}
	if cat := lib("sidebytes", func(c *config.MediaLibrary) { c.MaxCatalogBytes = 100 }); !strings.Contains(cat.Truncated, "the sidecars pass max_catalog_bytes") {
		t.Errorf("sidecars past max_catalog_bytes: %q", cat.Truncated)
	}
	cat := lib("sidecar", func(c *config.MediaLibrary) { c.MaxSidecarBytes = 100; c.MaxSubtitleBytes = 50 })
	if len(cat.Problems) != 1 || !strings.Contains(cat.Problems[0].Error, "past max_sidecar_bytes") || cat.Item("sintel") != nil || cat.Item("films/sintel-hd") == nil {
		t.Errorf("max_sidecar_bytes: %+v", cat.Problems)
	}
	subs := cat.Item("films/bbb").Subtitles
	if !strings.Contains(subs[0].Error, "past max_subtitle_bytes") || subs[1].Error != "" {
		t.Errorf("max_subtitle_bytes: %+v", subs)
	}
	if _, err := get(t, func(fn func([]byte, error)) { last.Subtitle(subs[0], fn) }); !errors.Is(err, ErrNotServed) {
		t.Errorf("a subtitle past max_subtitle_bytes: %v", err)
	}

	// Patterns.
	cat = lib("patterns", func(c *config.MediaLibrary) {
		c.Patterns = []string{"*.mp4", "*.json", "*.srt"}
		c.Exclude = []string{"clips/**", "*-sd.mp4"}
	})
	var ids []string
	for _, it := range cat.Items {
		ids = append(ids, it.ID)
	}
	if strings.Join(ids, " ") != "broken films/bbb sintel" || len(cat.Item("films/bbb").Audio) != 0 || len(cat.Item("films/bbb").Subtitles) != 1 ||
		len(cat.Problems) != 2 || !strings.Contains(cat.Problems[0].Error, `"sintel-sd.mp4" is not a file the library looks at`) ||
		!strings.Contains(cat.Problems[1].Error, `poster "art/sintel.png" is not a file the library looks at`) {
		t.Errorf("patterns: %v, problems %+v", ids, cat.Problems)
	}

	// Problems past MaxProblems are counted.
	many := t.TempDir()
	for i := range MaxProblems + 5 {
		if err := os.WriteFile(filepath.Join(many, "s"+strconv.Itoa(i)+".json"), []byte("{"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, l := open(t, config.MediaLibrary{Name: "many", Roots: []string{many}})
	if cat := scanned(t, l, 1); len(cat.Problems) != MaxProblems || cat.MoreProblems != 5 {
		t.Errorf("%d problems and %d more", len(cat.Problems), cat.MoreProblems)
	}
}

// TestRoots reports a root that is missing, or not a directory, and scans
// the others; and an ID two roots make, once.
func TestRoots(t *testing.T) {
	dir := copyLib(t)
	other := t.TempDir()
	if err := os.MkdirAll(filepath.Join(other, "films"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(filepath.Join(dir, "films", "bbb.mp4"), filepath.Join(other, "films", "bbb.mp4")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "sd.json"), []byte(`{"id": "sintel"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(filepath.Join(dir, "films", "bbb.mp4"), filepath.Join(other, "sd.mp4")); err != nil {
		t.Fatal(err)
	}
	_, l := open(t, config.MediaLibrary{Name: "lib", Roots: []string{dir, filepath.Join(dir, "..", "missing"), filepath.Join(dir, "notes.txt"), other}})
	cat := scanned(t, l, 1)
	if len(cat.Items) != 5 || cat.Item("films/bbb").Root != dir || cat.Item("sintel").Root != dir {
		t.Errorf("%d items", len(cat.Items))
	}
	var errs []string
	for _, p := range cat.Problems {
		errs = append(errs, p.Root+": "+p.Path+": "+p.Error)
	}
	got := strings.Join(errs, "\n")
	for _, want := range []string{"missing: : the root:", "notes.txt: : the root: not a directory", `films/bbb: the ID "films/bbb" is taken by`, `sd: the ID "sintel" is taken by`} {
		if !strings.Contains(got, want) {
			t.Errorf("problems:\n%s\nwant %q", got, want)
		}
	}
}

// TestDeepAndOdd walks no deeper than MaxDepth, skips names starting with
// a dot and files of no kind it knows, and lists a sidecar that does not
// parse as a problem.
func TestDeepAndOdd(t *testing.T) {
	dir := t.TempDir()
	deep := dir
	for range MaxDepth + 1 {
		deep = filepath.Join(deep, "d")
	}
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join("testdata", "lib", "films", "bbb.mp4")
	for _, p := range []string{filepath.Join(deep, "deep.mp4"), filepath.Join(dir, ".hidden.mp4"), filepath.Join(dir, "a.mp4")} {
		if err := copyFile(src, p); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{
		"bad.json":              `{"unknown": 1}`,
		"two.json":              `{} {}`,
		"id.json":               `{"id": "../x"}`,
		"long.json":             `{"title": "` + strings.Repeat("x", 2000) + `"}`,
		"a.json":                `{"title": "A", "audio": [{"file": "a.mp4", "name": "main", "language": "en", "default": true}]}`,
		"a.unknown.vt":          "x",
		"a..en.default.sdh.srt": "x",
		"empty.json":            `{"title": "E"}`,
		"a.poster.jpg":          "x",
		"a.jpg":                 "x",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, l := open(t, config.MediaLibrary{Name: "deep", Roots: []string{dir}})
	cat := scanned(t, l, 1)
	problems := ""
	for _, p := range cat.Problems {
		problems += p.Path + ": " + p.Error + "\n"
	}
	for _, want := range []string{"deeper than 32 directories", `bad.json: the sidecar: json: unknown field "unknown"`, "two.json: the sidecar: more than one JSON value",
		`id.json: the sidecar: id "../x" is not an item's ID`, "long.json: the sidecar: a title, name or language is not UTF-8"} {
		if !strings.Contains(problems, want) {
			t.Errorf("problems:\n%s\nwant %q", problems, want)
		}
	}
	a := cat.Item("a")
	if s := a.Subtitles; len(s) != 1 || s[0].Language != "en" || !s[0].Default || s[0].Name != "sdh" {
		t.Errorf("a's subtitles: %+v", s)
	}
	if e := cat.Item("empty"); e == nil || e.Title != "E" || e.Error != "no media file: the sidecar names none, and none is named after it" {
		t.Errorf("a sidecar of nothing: %+v", e)
	}
	if len(cat.Items) != 2 || a == nil || a.Title != "A" || len(a.Audio) != 1 || len(a.Renditions) != 0 || !a.Audio[0].Default ||
		a.Audio[0].Language != "en" || a.Audio[0].Name != "main" || a.Poster == nil || a.Poster.Path != "a.jpg" || a.Duration != time.Second {
		t.Errorf("items %d, a: %+v", len(cat.Items), a)
	}
}

// TestChangingFile probes a file that changes while it is probed again,
// and lists it with that error, to be probed again at the next scan.
func TestChangingFile(t *testing.T) {
	dir := copyLib(t)
	_, l := open(t, config.MediaLibrary{Name: "lib", Roots: []string{dir}, MaxProbes: 1})
	scanned(t, l, 1)
	p := filepath.Join(dir, "films", "bbb.mp4")
	// Each job the pool runs changes the file first, as a copy under way
	// would.
	var n atomic.Int64
	pool := NewPool(2, 16, time.Minute)
	l.pool.Store(pool)
	t.Cleanup(func() { pool.Close(); pool.Wait() })
	hook := func() {
		later := time.Now().Add(time.Duration(n.Add(1)) * time.Hour)
		_ = os.Chtimes(p, later, later)
	}
	pool.hook.Store(&hook)
	touch(t, p)
	cat := rescan(t, l)
	r := cat.Item("films/bbb").Renditions[1]
	if !strings.Contains(r.Error, "changed while it was probed") || !r.transient {
		t.Errorf("a file that keeps changing: %+v", r)
	}
	pool.hook.Store(nil)
	cat = rescan(t, l)
	if r := cat.Item("films/bbb").Renditions[1]; r.Error != "" {
		t.Errorf("once it stops changing: %+v", r)
	}
}
