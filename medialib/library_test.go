package medialib

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fib"
	"github.com/lesismal/fib/media/mp4"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/observe"
)

// TestTable reads a track's sample table on the pool once, for every call
// that waits for it, keeps it within max_table_bytes, and drops it when its
// file changes.
func TestTable(t *testing.T) {
	dir := copyLib(t)
	_, l := open(t, config.MediaLibrary{Name: "lib", Roots: []string{dir}})
	cat := scanned(t, l, 1)
	bbb := cat.Item("films/bbb").Renditions[1]

	// Many at once: one read, every caller called.
	var wg sync.WaitGroup
	tabs := make([]*mp4.Table, 50)
	for i := range tabs {
		wg.Go(func() {
			tb, err := get(t, func(fn func(*mp4.Table, error)) { l.Table(bbb, 0, fn) })
			if err != nil {
				t.Error(err)
			}
			tabs[i] = tb
		})
	}
	wg.Wait()
	for _, tb := range tabs {
		if tb != tabs[0] || tb.Len() != 25 {
			t.Fatalf("the tables differ, or have %d samples", tb.Len())
		}
	}
	// Kept: called at once, on this goroutine.
	called := false
	l.Table(bbb, 0, func(tb *mp4.Table, err error) { called = tb == tabs[0] && err == nil })
	if !called {
		t.Error("a table kept is not handed over at once")
	}
	if st := l.Tables(); st.Tables != 1 || st.Bytes != tabs[0].Bytes() || st.Max != config.DefaultMediaMaxTableBytes {
		t.Errorf("tables: %+v", st)
	}
	// Counted: the first call read it, the last found it kept, those
	// between waited for the read or found it kept.
	if st := l.Tables(); st.Hits+st.Misses != 51 || st.Hits < 1 || st.Misses < 1 {
		t.Errorf("lookups: %+v", st)
	}

	// Bounded: room for one table; the least recently used goes.
	audio, err := get(t, func(fn func(*mp4.Table, error)) { l.Table(bbb, 1, fn) })
	if err != nil || audio.Len() != 48 {
		t.Fatal(err)
	}
	l.tables.resize(tabs[0].Bytes() + 100)
	if st := l.Tables(); st.Tables != 1 || st.Bytes != audio.Bytes() {
		t.Errorf("after a resize: %+v", st)
	}
	if _, err := get(t, func(fn func(*mp4.Table, error)) { l.Table(bbb, 0, fn) }); err != nil {
		t.Fatal(err)
	}
	if st := l.Tables(); st.Tables != 1 || st.Bytes != tabs[0].Bytes() {
		t.Errorf("after the video again: %+v", st)
	}
	l.tables.resize(10)
	if _, err := get(t, func(fn func(*mp4.Table, error)) { l.Table(bbb, 0, fn) }); !errors.Is(err, ErrTooLarge) {
		t.Errorf("past max_table_bytes: %v", err)
	}
	l.tables.resize(config.DefaultMediaMaxTableBytes)

	// What cannot be read.
	broken := cat.Item("broken").Renditions[0]
	for _, c := range []struct {
		r     *Rendition
		track int
	}{{broken, 0}, {bbb, 2}, {bbb, -1}} {
		if _, err := get(t, func(fn func(*mp4.Table, error)) { l.Table(c.r, c.track, fn) }); !errors.Is(err, ErrNotServed) {
			t.Errorf("track %d of %s: %v", c.track, c.r.Path, err)
		}
	}

	// The file changed since the scan: refused, and its tables dropped.
	if _, err := get(t, func(fn func(*mp4.Table, error)) { l.Table(bbb, 1, fn) }); err != nil {
		t.Fatal(err)
	}
	touch(t, filepath.Join(dir, "films", "bbb.mp4"))
	if _, err := get(t, func(fn func(*Handle, error)) { l.Open(&bbb.File, fn) }); !errors.Is(err, ErrChanged) {
		t.Errorf("a file changed: %v", err)
	}
	if st := l.Tables(); st.Tables != 0 || st.Bytes != 0 {
		t.Errorf("the tables of a file changed: %+v", st)
	}
	if _, err := get(t, func(fn func(*mp4.Table, error)) { l.Table(bbb, 0, fn) }); !errors.Is(err, ErrChanged) {
		t.Errorf("the table of a file changed: %v", err)
	}
	// The next scan has it as it is, and keeps none of the old.
	cat = rescan(t, l)
	bbb = cat.Item("films/bbb").Renditions[1]
	if _, err := get(t, func(fn func(*mp4.Table, error)) { l.Table(bbb, 0, fn) }); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "films", "bbb.mp4")); err != nil {
		t.Fatal(err)
	}
	rescan(t, l)
	if st := l.Tables(); st.Tables != 0 {
		t.Errorf("the tables of a file removed: %+v", st)
	}
	if _, err := get(t, func(fn func(*Handle, error)) { l.Open(&bbb.File, fn) }); !errors.Is(err, ErrChanged) {
		t.Errorf("a file removed: %v", err)
	}
}

// TestTableBusy answers ErrBusy when the pool refuses a table's read, or
// tables being read hold max_table_bytes, or too many wait for one.
func TestTableBusy(t *testing.T) {
	dir := copyLib(t)
	_, l := open(t, config.MediaLibrary{Name: "lib", Roots: []string{dir}})
	cat := scanned(t, l, 1)
	bbb := cat.Item("films/bbb").Renditions[1]

	// A pool whose one worker is held.
	release := make(chan struct{})
	pool := NewPool(1, 1, time.Minute)
	t.Cleanup(func() { pool.Close(); pool.Wait() })
	held := make(chan struct{})
	pool.Submit(func() { close(held); <-release }, nil)
	<-held
	l.pool.Store(pool)

	results := make(chan error, maxTableWaiters+2)
	for range maxTableWaiters + 1 {
		l.Table(bbb, 0, func(_ *mp4.Table, err error) { results <- err })
	}
	if err := <-results; !errors.Is(err, ErrBusy) {
		t.Errorf("one past maxTableWaiters: %v", err)
	}
	// The queue holds the read of track 0: track 1 is refused.
	if _, err := get(t, func(fn func(*mp4.Table, error)) { l.Table(bbb, 1, fn) }); !errors.Is(err, ErrBusy) {
		t.Errorf("a pool that refuses: %v", err)
	}
	// Room for the table being read alone.
	l.tables.resize(l.Tables().Bytes)
	if _, err := get(t, func(fn func(*mp4.Table, error)) { l.Table(bbb, 1, fn) }); !errors.Is(err, ErrBusy) {
		t.Errorf("max_table_bytes held by tables being read: %v", err)
	}
	l.tables.resize(config.DefaultMediaMaxTableBytes)
	// Dropped while it is read: those waiting get it, and it is not kept.
	l.tables.drop(bbb.abs)
	close(release)
	for range maxTableWaiters {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if st := l.Tables(); st.Tables != 0 || st.Bytes != 0 {
		t.Errorf("a table dropped while read: %+v", st)
	}

	// A job that waits past file_timeout.
	slow := NewPool(1, 4, time.Nanosecond)
	t.Cleanup(func() { slow.Close(); slow.Wait() })
	l.pool.Store(slow)
	if _, err := get(t, func(fn func(*mp4.Table, error)) { l.Table(bbb, 0, fn) }); !errors.Is(err, ErrBusy) {
		t.Errorf("a read past file_timeout: %v", err)
	}
	if st := slow.Stats(); st.Expired != 1 {
		t.Errorf("stats: %+v", st)
	}
}

// TestSubtitles serves a WebVTT file as it is, and an SRT file turned into
// WebVTT.
func TestSubtitles(t *testing.T) {
	dir := copyLib(t)
	_, l := open(t, config.MediaLibrary{Name: "lib", Roots: []string{dir}})
	cat := scanned(t, l, 1)
	subs := cat.Item("films/bbb").Subtitles
	srt, err := get(t, func(fn func([]byte, error)) { l.Subtitle(subs[0], fn) })
	if err != nil {
		t.Fatal(err)
	}
	want := "WEBVTT\n\n00:00:00.100 --> 00:00:00.900\nHello\n\n2\n00:00:01.000 --> 00:00:02.500\nWorld --&gt; on\n"
	if string(srt) != want {
		t.Errorf("the SRT file as WebVTT:\n%q\nwant\n%q", srt, want)
	}
	vtt, err := get(t, func(fn func([]byte, error)) { l.Subtitle(subs[1], fn) })
	if err != nil || string(vtt) != "WEBVTT\n\n00:00.100 --> 00:00.900\nBonjour\n" {
		t.Errorf("the WebVTT file: %q, %v", vtt, err)
	}
	zh, err := get(t, func(fn func([]byte, error)) { l.Subtitle(cat.Item("sintel").Subtitles[0], fn) })
	if err != nil || !strings.Contains(string(zh), "00:00:00.000 --> 00:00:01.000\n你好") {
		t.Errorf("sintel's subtitle: %q, %v", zh, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "films", "bbb.en.srt"), []byte("no cues"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := get(t, func(fn func([]byte, error)) { l.Subtitle(subs[0], fn) }); !errors.Is(err, ErrChanged) {
		t.Errorf("a subtitle changed: %v", err)
	}
	s := rescan(t, l).Item("films/bbb").Subtitles[0]
	if _, err := get(t, func(fn func([]byte, error)) { l.Subtitle(s, fn) }); err == nil || !strings.Contains(err.Error(), "not an SRT file") {
		t.Errorf("an SRT file of no cue: %v", err)
	}

	for _, c := range []struct{ format, in, want string }{
		{"vtt", "\xef\xbb\xbfWEBVTT", "WEBVTT"},
		{"vtt", "WEBVTT - a title\n", "WEBVTT - a title\n"},
		{"vtt", "WEBVTTX\n", ""},
		{"vtt", "1\n00:00:01,000 --> 00:00:02,000\n", ""},
		{"srt", "junk\r1\r00:00:01.5 --> 00:00:02.25\rtext\r", "WEBVTT\n\n00:00:01.500 --> 00:00:02.250\ntext\n"},
		{"ass", "x", ""},
	} {
		got, err := ToWebVTT(c.format, []byte(c.in))
		if string(got) != c.want || (err != nil) != (c.want == "") {
			t.Errorf("%s %q: %q, %v; want %q", c.format, c.in, got, err, c.want)
		}
	}
}

// TestPool runs jobs on bounded goroutines: a full queue refuses, a job
// past the timeout expires, and a closed pool refuses and runs what it had
// queued.
func TestPool(t *testing.T) {
	p := NewPool(1, 1, time.Minute)
	release := make(chan struct{})
	ran := make(chan string, 4)
	if !p.Submit(func() { ran <- "first"; <-release }, nil) {
		t.Fatal("refused")
	}
	<-ran
	if !p.Submit(func() { ran <- "queued" }, nil) {
		t.Fatal("the queue's one place refused")
	}
	if p.Submit(func() {}, nil) {
		t.Error("a full queue took a job")
	}
	if st := p.Stats(); st.Workers != 1 || st.Queue != 1 || st.Queued != 1 || st.Busy != 1 || st.Refused != 1 {
		t.Errorf("stats: %+v", st)
	}
	p.Close()
	p.Close()
	if p.Submit(func() {}, nil) {
		t.Error("a closed pool took a job")
	}
	close(release)
	p.Wait()
	if got := <-ran; got != "queued" {
		t.Errorf("the job queued before Close: %q", got)
	}

	q := NewPool(0, 0, -1)
	expired := make(chan bool, 2)
	q.Submit(func() { expired <- false }, func() { expired <- true })
	if !<-expired {
		t.Error("a job past the timeout ran")
	}
	q.Submit(func() {}, nil)
	q.Close()
	q.Wait()
	if st := q.Stats(); st.Expired != 2 || st.Workers != 1 || st.Queue != 1 {
		t.Errorf("stats: %+v", st)
	}
}

// TestOnPool runs a scan's jobs at most max_probes at once, tries a job
// the pool refuses again, and gives up on it once the library is removed.
func TestOnPool(t *testing.T) {
	dir := t.TempDir()
	_, l := open(t, config.MediaLibrary{Name: "lib", Roots: []string{dir}, MaxProbes: 3})
	scanned(t, l, 1)
	pool := NewPool(8, 2, time.Minute)
	t.Cleanup(func() { pool.Close(); pool.Wait() })
	l.pool.Store(pool)
	sc := &scanner{l: l, s: l.set.Load()}
	var now, most atomic.Int64
	var ran atomic.Int64
	sc.onPool(40, func(int) {
		n := now.Add(1)
		for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
		}
		time.Sleep(time.Millisecond)
		now.Add(-1)
		ran.Add(1)
	}, func(int) { t.Error("lost") })
	if ran.Load() != 40 || most.Load() > 3 || most.Load() < 2 {
		t.Errorf("ran %d, at most %d at once", ran.Load(), most.Load())
	}
	if st := pool.Stats(); st.Refused == 0 {
		t.Logf("the pool refused none: %+v", st)
	}

	// Removed while the pool refuses: the jobs left are lost.
	full := NewPool(1, 1, time.Minute)
	release := make(chan struct{})
	t.Cleanup(func() { close(release); full.Close(); full.Wait() })
	holding := make(chan struct{})
	full.Submit(func() { close(holding); <-release }, nil)
	<-holding
	if !full.Submit(func() {}, nil) {
		t.Fatal("the queue's place refused")
	}
	l.pool.Store(full)
	var lost atomic.Int64
	go func() {
		time.Sleep(20 * time.Millisecond)
		l.remove()
	}()
	sc.onPool(3, func(int) { t.Error("ran") }, func(int) { lost.Add(1) })
	if lost.Load() != 3 {
		t.Errorf("lost %d", lost.Load())
	}
}

// TestReload changes a library in place: the same *Library, scanned again
// when its roots or patterns change, its pool replaced when its bounds do;
// and one a reload leaves out is removed, its methods refused.
func TestReload(t *testing.T) {
	dir := copyLib(t)
	c := config.MediaLibrary{Name: "lib", Roots: []string{dir}}
	key, l := open(t, c, config.MediaLibrary{Name: "other", Roots: []string{t.TempDir()}})
	other := Lookup(key, "other")
	scanned(t, l, 1)
	scanned(t, other, 1)
	pool := l.Pool()

	// Unchanged: nothing happens.
	if err := Apply(key, []config.MediaLibrary{c, {Name: "other", Roots: other.Roots()}}, nil); err != nil {
		t.Fatal(err)
	}
	if Lookup(key, "lib") != l || l.Pool() != pool || l.Status().Pending {
		t.Error("an unchanged library changed")
	}

	// New patterns: scanned again, in place.
	idle(t, l)
	scans := l.Status().Scans
	c.Exclude = []string{"clips"}
	c.FileWorkers, c.ScanInterval = 2, config.Duration(time.Hour)
	if err := Apply(key, []config.MediaLibrary{c}, nil); err != nil {
		t.Fatal(err)
	}
	if Lookup(key, "lib") != l || l.Pool() == pool || l.Pool().Stats().Workers != 2 {
		t.Error("the library was not changed in place")
	}
	cat := scanned(t, l, scans+1)
	if cat.Item("clips/late") != nil {
		t.Error("an excluded directory is listed")
	}
	pool.Wait() // the pool replaced ends

	// Removed: refused, and its scanner ends.
	if Lookup(key, "other") != nil || !other.Removed() {
		t.Fatal("a library left out is not removed")
	}
	<-other.done
	if other.Rescan() {
		t.Error("a removed library rescans")
	}
	if _, err := get(t, func(fn func(*Handle, error)) { other.Open(&File{}, fn) }); !errors.Is(err, ErrRemoved) {
		t.Errorf("Open: %v", err)
	}
	if _, err := other.OpenFile(&File{}); !errors.Is(err, ErrRemoved) {
		t.Errorf("OpenFile: %v", err)
	}
	if _, err := get(t, func(fn func(*mp4.Table, error)) { other.Table(&Rendition{}, 0, fn) }); !errors.Is(err, ErrRemoved) {
		t.Errorf("Table: %v", err)
	}
	if _, err := get(t, func(fn func([]byte, error)) { other.Subtitle(&Subtitle{}, fn) }); !errors.Is(err, ErrRemoved) {
		t.Errorf("Subtitle: %v", err)
	}
	if other.Submit(func() {}, nil) {
		t.Error("Submit on a removed library")
	}
	other.remove()

	// Configs that do not compile change nothing.
	for _, bad := range [][]config.MediaLibrary{
		{c, c},
		{{Name: "x"}},
		{{Name: "x", Roots: []string{" "}}},
		{{Name: "x", Roots: []string{dir}, Patterns: []string{"["}}},
	} {
		if err := Apply(key, bad, nil); err == nil {
			t.Errorf("%+v applied", bad)
		}
	}
	if Lookup(key, "lib") != l || l.Removed() {
		t.Error("a config refused changed the libraries")
	}
	if err := Apply(key, nil, nil); err != nil || Lookup(key, "lib") != nil || !l.Removed() {
		t.Errorf("no libraries: %v", err)
	}
}

// TestReloadRace applies configs while the library is scanned and read,
// under the race detector.
func TestReloadRace(t *testing.T) {
	dir := copyLib(t)
	key, l := open(t, config.MediaLibrary{Name: "lib", Roots: []string{dir}})
	scanned(t, l, 1)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			cat := l.Catalog()
			for _, it := range cat.Items {
				for _, r := range it.Renditions {
					done := make(chan struct{})
					l.Table(r, 0, func(*mp4.Table, error) { close(done) })
					<-done
				}
			}
			l.Status()
			l.Rescan()
		}
	})
	for i := range 20 {
		c := config.MediaLibrary{Name: "lib", Roots: []string{dir}, MaxTableBytes: int64(1000 + i*100), FileWorkers: 1 + i%3}
		if i%2 == 0 {
			c.Exclude = []string{"clips"}
		}
		if err := Apply(key, []config.MediaLibrary{c}, nil); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	close(stop)
	wg.Wait()
	if Lookup(key, "lib") != l {
		t.Error("the library was replaced")
	}
}

// TestRole runs the libraries as fit does, through the app's role: made,
// changed and stopped with the configs applied, with metrics.
func TestRole(t *testing.T) {
	dir := copyLib(t)
	upstream := new(fib.Engine)
	m := app.NewManager(nil, upstream, nil, nil)
	t.Cleanup(func() { m.Stop(context.Background()) })
	cfg := func(names ...string) *config.Config {
		c := &config.Config{}
		for _, n := range names {
			c.MediaLibrary = append(c.MediaLibrary, config.MediaLibrary{Name: n, Roots: []string{dir}})
		}
		return c
	}
	if err := m.Apply(cfg("a", "b")); err != nil {
		t.Fatal(err)
	}
	a := Lookup(upstream, "a")
	if a == nil || Lookup(upstream, "b") == nil || len(Libraries(upstream)) != 2 || Libraries(upstream)[0] != a {
		t.Fatal("the role made no libraries")
	}
	scanned(t, a, 1)
	scanned(t, Lookup(upstream, "b"), 1)
	var w observe.TextWriter
	m.WriteMetrics(&w)
	for _, want := range []string{
		`fit_media_library_items{library="a",state="ok"} 4`, `fit_media_library_items{library="a",state="error"} 1`,
		`fit_media_library_scans_total{library="b"} 1`, `fit_media_library_probes_total{library="a",result="error"} 1`,
		`fit_media_library_pool_jobs{library="a",state="queued"} 0`, `fit_media_library_pool_refused_total{library="a",reason="expired"} 0`,
		`fit_media_library_catalog_bytes{library="a"}`, `fit_media_library_table_bytes{library="a"} 0`,
		`fit_media_library_table_lookups_total{library="a",result="hit"} 0`, `fit_media_library_table_lookups_total{library="b",result="miss"} 0`,
	} {
		if !strings.Contains(string(w.B), want) {
			t.Errorf("metrics lack %s:\n%s", want, w.B)
		}
	}
	if err := m.Apply(cfg("a")); err != nil {
		t.Fatal(err)
	}
	if Lookup(upstream, "a") != a || Lookup(upstream, "b") != nil {
		t.Error("a reload did not keep a and remove b")
	}
	bad := cfg("a")
	bad.MediaLibrary[0].Patterns = []string{"["}
	if err := m.Apply(bad); err == nil {
		t.Error("a library that does not compile applied")
	}
	if err := m.Apply(&config.Config{}); err != nil {
		t.Fatal(err)
	}
	if Lookup(upstream, "a") != nil || !a.Removed() {
		t.Error("the role stopped and left a library")
	}
	if err := (&role{}).Apply(42); err == nil {
		t.Error("a member of another type applied")
	}
}
