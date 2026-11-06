package vod

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	fibhttp "github.com/lesismal/fib/http"
	"github.com/lesismal/fib/media/av"
	"github.com/lesismal/fib/media/fmp4"
	"github.com/lesismal/fib/media/mp4"

	"github.com/lesismal/fit/internal/fibtest"
	"github.com/lesismal/fit/medialib"
)

func TestParseName(t *testing.T) {
	for _, tc := range []struct {
		s    string
		want name
		ok   bool
	}{
		{"index.m3u8", name{kind: kindMaster}, true},
		{"index.ts.m3u8", name{kind: kindMaster, c: tsC}, true},
		{"index.fmp4.m3u8", name{kind: kindMaster, c: fmp4C}, true},
		{"index.mp4.m3u8", name{}, false},
		{"index.ts.m3u9", name{kind: kindMaster, c: tsC}, false},
		{"v3.fmp4.m3u8", name{kind: kindMedia, c: fmp4C, i: 3}, true},
		{"a12.ts.m3u8", name{kind: kindMedia, c: tsC, audio: true, i: 12}, true},
		{"v0.dash.m3u8", name{kind: kindMedia}, false},
		{"v0.init.mp4", name{kind: kindInit, c: fmp4C}, true},
		{"v0.start.mp4", name{kind: kindInit, c: fmp4C}, false},
		{"v1.17.m4s", name{kind: kindSegment, c: fmp4C, i: 1, n: 17}, true},
		{"a0.0.ts", name{kind: kindSegment, c: tsC, audio: true}, true},
		{"v0.017.ts", name{kind: kindSegment, c: tsC}, false},
		{"v0.x.ts", name{kind: kindSegment, c: tsC}, false},
		{"v0.99999999.ts", name{kind: kindSegment, c: tsC, n: 99999999}, false},
		{"v0.1.mpd", name{}, false},
		{"s2.m3u8", name{kind: kindSubtitles, i: 2}, true},
		{"s2.vtt", name{kind: kindVTT, i: 2}, true},
		{"s2.srt", name{}, false},
		{"s0.init.mp4", name{}, false},
		{"v4.mov", name{kind: kindFile, i: 4, ext: "mov"}, true},
		{"a1.m4a", name{kind: kindFile, audio: true, i: 1, ext: "m4a"}, true},
		{"v1.exe", name{}, false},
		{"poster.webp", name{kind: kindPoster, ext: "webp"}, true},
		{"poster.", name{kind: kindPoster}, false},
		{"x1.m3u8", name{}, false},
		{"v.m3u8", name{}, false},
		{"v", name{}, false},
		{"a.b.c.d", name{}, false},
		{"", name{}, false},
	} {
		got, ok := parseName(tc.s)
		if ok != tc.ok || ok && got != tc.want {
			t.Errorf("%q: %+v %v, want %+v %v", tc.s, got, ok, tc.want, tc.ok)
		}
	}
	// What the playlists write parses back.
	for _, s := range []string{string(appendMediaName(nil, true, 3, tsC)), string(appendInitName(nil, false, 2)),
		string(appendSegmentName(nil, false, 1, 9, fmp4C)), string(appendSegmentName(nil, true, 0, 4, tsC))} {
		if _, ok := parseName(s); !ok {
			t.Errorf("%q does not parse", s)
		}
	}
}

func TestHelpers(t *testing.T) {
	for _, tc := range []struct{ a, b, c, want int64 }{
		{10000, 90000, 12800, 70312}, {-1, 90000, 12800, -8}, {-12800, 90000, 12800, -90000}, {0, 3, 7, 0},
		{math.MaxInt64 / 2, 4, 2, math.MaxInt64 - 1},
	} {
		if got := mulDivFloor(tc.a, tc.b, tc.c); got != tc.want {
			t.Errorf("%d*%d/%d = %d, want %d", tc.a, tc.b, tc.c, got, tc.want)
		}
	}
	// Past 64 bits it does not panic.
	_ = mulDivFloor(math.MaxInt64, math.MaxInt64, 3)
	for in, want := range map[string]string{
		"WEBVTT":                               "WEBVTT\n" + string(timestampMap) + "\n",
		"WEBVTT\r\n\r\n1\r\n":                  "WEBVTT\r\n" + string(timestampMap) + "\n\r\n1\r\n",
		"WEBVTT\nX-TIMESTAMP-MAP=LOCAL:0\n\nx": "WEBVTT\nX-TIMESTAMP-MAP=LOCAL:0\n\nx",
	} {
		if got := string(withTimestampMap([]byte(in))); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	for ext, want := range map[string]string{"mp4": "video/mp4", "m4v": "video/mp4", "m4a": "audio/mp4", "mov": "video/quicktime",
		"jpg": "image/jpeg", "jpeg": "image/jpeg", "png": "image/png", "webp": "image/webp", "gif": "application/octet-stream"} {
		if got := fileType(ext); got != want {
			t.Errorf("%s: %s", ext, got)
		}
	}
	for s, want := range map[string]bool{"films": true, "a.b_c-1": true, "": false, "a b": false, strings.Repeat("x", 129): false, "é": false} {
		if validName(s) != want {
			t.Errorf("validName(%q) is not %v", s, want)
		}
	}
	if nameOr("main", "und") != "main" || nameOr("main", "") != "main" || nameOr("main", "fr") != "fr" {
		t.Error("nameOr")
	}
	if quoted(`a "b"`+"\r\n") != "a 'b'  " || quoted("plain") != "plain" {
		t.Error("quoted")
	}
	if contentTypeOf([]*av.Track{{Codec: av.CodecAAC}}) != "audio/mp4" || contentTypeOf([]*av.Track{{Codec: av.CodecAAC}, {Codec: av.CodecH264}}) != "video/mp4" {
		t.Error("contentTypeOf")
	}
	if c := container(0); c.String() != "fmp4" || tsC.String() != "ts" {
		t.Error("container names")
	}
	if !strings.Contains(string(renderSubtitles(1, 0, "token=x")), "#EXTINF:0.001,\ns1.vtt?token=x\n") {
		t.Errorf("%s", renderSubtitles(1, 0, "token=x"))
	}
	if _, err := buildInit([]*av.Track{{Codec: av.CodecH264, Config: []byte{1}}}); err == nil {
		t.Error("an init of a broken H.264 config")
	}
}

// TestCache checks the cache's bookkeeping: what it keeps, builds once,
// drops, and refuses.
func TestCache(t *testing.T) {
	c := newCache(4096)
	f := fileID{root: "r", path: "p"}
	k := func(n int32) ckey { return ckey{file: f, what: whatSegment, n: n} }
	// A build that ends later, and waiters for it.
	var later job
	keep := func(j job) { later = j }
	var got []any
	for range 3 {
		c.get(k(0), keep, func(v any, err error) { got = append(got, v) })
	}
	if !later.hold(100) {
		t.Fatal("no room")
	}
	later.done("zero", 100, nil)
	if len(got) != 3 || got[2] != "zero" {
		t.Fatalf("waiters got %v", got)
	}
	if v, ok := c.peek(k(0)); !ok || v != "zero" {
		t.Fatal("not kept")
	}
	// Too many waiters.
	c.get(k(1), keep, func(any, error) {})
	for range maxWaiters - 1 {
		c.get(k(1), nil, func(any, error) {})
	}
	var werr error
	c.get(k(1), nil, func(_ any, err error) { werr = err })
	if !errors.Is(werr, errWaiters) {
		t.Fatalf("%v", werr)
	}
	// Dropped while it is built: it holds nothing more, those waiting get
	// it, and it is not kept.
	c.drop(f)
	if !later.hold(1 << 20) {
		t.Fatal("a job dropped refused")
	}
	later.done("one", 100, nil)
	if _, ok := c.peek(k(1)); ok || c.stats().Bytes != 0 {
		t.Fatalf("kept what was dropped while built: %+v", c.stats())
	}
	// An error is not kept.
	c.get(k(2), func(j job) { j.done(nil, 0, io.EOF) }, func(_ any, err error) { werr = err })
	if werr != io.EOF || c.stats().Bytes != 0 {
		t.Fatal(werr, c.stats())
	}
	// Past the bound: what is built holds it, until it is done; a job
	// that cannot hold what it needs is refused, and the least recently
	// used go for one that can.
	c.put(k(7), "seven", 500)
	c.get(k(3), keep, func(any, error) {})
	if !later.hold(3000) {
		t.Fatal("no room for 3000")
	}
	held := later
	c.get(k(4), func(j job) {
		if j.hold(1000) {
			t.Error("held past the bound")
		}
		j.done(nil, 0, errFull)
	}, func(_ any, err error) { werr = err })
	if !errors.Is(werr, errFull) {
		t.Fatal(werr)
	}
	if _, ok := c.peek(k(7)); ok {
		t.Error("the least recently used was kept while a job wanted its room")
	}
	// Built larger than it can hold: given, not kept.
	held.done("big", 8000, nil)
	if _, ok := c.peek(k(3)); ok {
		t.Fatalf("%+v", c.stats())
	}
	c2 := newCache(4096)
	c2.put(k(5), "five", 1000)
	c2.put(k(5), "again", 1000)
	c2.put(k(6), "huge", 5000)
	if v, _ := c2.peek(k(5)); v != "five" {
		t.Fatal(v)
	}
	if _, ok := c2.peek(k(6)); ok {
		t.Fatal("kept past the bound")
	}
	// A cache full of jobs refuses another at once.
	c3 := newCache(entryBytes)
	c3.get(k(0), keep, func(any, error) {})
	c3.get(k(1), nil, func(_ any, err error) { werr = err })
	if !errors.Is(werr, errFull) {
		t.Fatal(werr)
	}
	// A catalog without the file drops it.
	c2.keep(&medialib.Catalog{})
	if c2.stats().Entries != 0 {
		t.Fatalf("%+v", c2.stats())
	}
}

// TestCacheKeepsListed checks keep against a catalog's files, subtitles
// among them.
func TestCacheKeepsListed(t *testing.T) {
	f := startVOD(t, copyLib(t), "", `{"library": "films"}`, "")
	h := f.handler(t)
	f.mustGet(t, "/vod/films/films/movie/s0.vtt", 200)
	f.mustGet(t, "/vod/films/films/movie/v0.0.m4s", 200)
	n := h.Cache().Entries
	h.st.cache.keep(f.lib.Catalog())
	if h.Cache().Entries != n {
		t.Errorf("keep dropped what the catalog lists: %d of %d", h.Cache().Entries, n)
	}
}

// tablesOf reads the tables of the test library's file rel.
func tablesOf(t *testing.T, rel string) (vt, at *mp4.Table, file []byte) {
	t.Helper()
	vt, at, _, file = fileTables(t, filepath.Join("testdata", "lib", rel))
	return vt, at, file
}

// TestPlanCuts checks where plans cut: at the keyframe nearest the target,
// at an earlier keyframe or none at all past maxBytes, and audio every d.
func TestPlanCuts(t *testing.T) {
	vt, at, file := tablesOf(t, "films/movie.mp4")
	starts := func(p *plan) []float64 {
		var out []float64
		for _, s := range p.segs {
			out = append(out, math.Round(s.start.Seconds()*100)/100)
		}
		return out
	}
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{2400 * time.Millisecond, "[0 2 4]"},
		{2600 * time.Millisecond, "[0 3]"},
		{time.Second, "[0 1 2 3 4 5]"},
		{10 * time.Second, "[0]"},
	} {
		p, err := buildPlan(vt, at, tc.d, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprint(starts(p)); got != tc.want {
			t.Errorf("d %v: starts %v, want %s", tc.d, starts(p), tc.want)
		}
		if !p.independent {
			t.Errorf("d %v: not independent", tc.d)
		}
	}
	// Past maxBytes: cut at the keyframe before, so more segments than d
	// alone makes, each starting with a keyframe.
	p, err := buildPlan(vt, at, 4*time.Second, 6000)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.segs) < 3 || !p.independent {
		t.Errorf("a byte bound cut %d segments, independent %v", len(p.segs), p.independent)
	}
	for n, s := range p.segs {
		if int64(s.bytes) > 6000+maxSample(vt)+maxSample(at)*20 {
			t.Errorf("segment %d of %d bytes", n, s.bytes)
		}
	}
	// One keyframe: cut where the bound is reached, not on a keyframe.
	lv, la, lfile := tablesOf(t, "clips/longgop.mp4")
	p, err = buildPlan(lv, la, 2*time.Second, 4000)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.segs) < 3 || p.independent || !p.segs[0].key {
		t.Fatalf("long GOP: %d segments, independent %v", len(p.segs), p.independent)
	}
	for n := range p.segs {
		if p.duration(n) <= 0 {
			t.Errorf("segment %d lasts %v", n, p.duration(n))
		}
	}
	// Its segments build, and the one not on a keyframe says so.
	src := &source{vt: lv, at: la, vtrack: av1Track(t, "clips/longgop.mp4", 0), atrack: av1Track(t, "clips/longgop.mp4", 1)}
	in, err := buildInit(src.tracks())
	if err != nil {
		t.Fatal(err)
	}
	for n := range p.segs {
		b, err := buildFMP4(bytes.NewReader(lfile), p, n, src, in)
		if err != nil {
			t.Fatal(err)
		}
		ini, _ := fmp4.ParseInit(in.data)
		_, pkts, _, err := ini.ParseFragment(b)
		if err != nil || len(pkts) == 0 || pkts[0].Keyframe != p.segs[n].key && pkts[0].Track == 0 {
			t.Fatalf("segment %d: %v", n, err)
		}
		if _, err := buildTS(bytes.NewReader(lfile), p, n, src); err != nil {
			t.Fatal(err)
		}
	}
	// A file cut short: its samples are not there.
	if _, err := buildTS(bytes.NewReader(lfile[:len(lfile)/2]), p, len(p.segs)-1, src); !errors.Is(err, medialib.ErrChanged) {
		t.Errorf("a short file: %v", err)
	}
	// Audio alone, every d; and past maxBytes.
	_, sat, _ := tablesOf(t, "music/song.m4a")
	p, err = buildPlan(nil, sat, 2*time.Second, 1<<20)
	if err != nil || len(p.segs) != 3 || p.target != 2 {
		t.Fatalf("audio: %v %+v", err, p)
	}
	p, err = buildPlan(nil, sat, 2*time.Second, 2000)
	if err != nil || len(p.segs) < 6 {
		t.Fatalf("audio past maxBytes: %v %d segments", err, len(p.segs))
	}
	_ = file
}

func maxSample(tb *mp4.Table) int64 {
	var m int64
	for _, s := range tb.Samples(0) {
		m = max(m, int64(s.Size))
	}
	return m
}

// av1Track returns track i of the test library's file rel, as av has it.
func av1Track(t *testing.T, rel string, i int) *av.Track {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "lib", rel))
	if err != nil {
		t.Fatal(err)
	}
	m, err := mp4.Open(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	return m.Tracks[i].Track
}

// TestMasterWithoutMainAudio checks an item whose rendition has no audio
// of its own: its audio files are the group, the one the sidecar says is
// the default, their names made unique and quoted strings cleaned.
func TestMasterWithoutMainAudio(t *testing.T) {
	root := copyLib(t)
	for _, n := range []string{"silent.fr.m4a", "silent.de.m4a"} {
		data, err := os.ReadFile(filepath.Join(root, "films", "movie.audio-fr.m4a"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "clips", n), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeSidecar(t, filepath.Join(root, "clips", "silent.json"), `{"renditions": [{"file": "silent.mp4"}],
	  "audio": [{"file": "silent.fr.m4a", "name": "Fr \"one\"", "language": "fr"}, {"file": "silent.de.m4a", "name": "Fr \"one\"", "language": "de", "default": true}]}`)
	f := startVOD(t, root, "", `{"library": "films"}`, "")
	b := string(f.mustGet(t, "/vod/films/clips/silent/index.m3u8", 200))
	for _, want := range []string{
		`NAME="Fr 'one'",LANGUAGE="fr",DEFAULT=NO,AUTOSELECT=YES,URI="a0.fmp4.m3u8"`,
		`NAME="Fr 'one' (2)",LANGUAGE="de",DEFAULT=YES,AUTOSELECT=YES,URI="a1.fmp4.m3u8"`,
		`CODECS="avc1.64000a,mp4a.40.2",RESOLUTION=64x48,AUDIO="audio"`,
	} {
		if !strings.Contains(b, want) {
			t.Errorf("the playlist lacks %q:\n%s", want, b)
		}
	}
	if strings.Contains(b, "URI") && strings.Count(b, "TYPE=AUDIO") != 2 {
		t.Errorf("a main audio for a rendition without:\n%s", b)
	}
	// Its playlist and segments carry video alone.
	f.mustGet(t, "/vod/films/clips/silent/v0.0.ts", 200)
	f.mustGet(t, "/vod/films/clips/silent/v0.0.m4s", 200)
}

// TestFail checks the statuses work that failed is answered with.
func TestFail(t *testing.T) {
	v := &VOD{CORS: "*", st: &store{cache: newCache(1 << 20)}}
	for err, want := range map[error]int{
		medialib.ErrChanged: 404, medialib.ErrRemoved: 404, medialib.ErrNotServed: 404, medialib.ErrOutside: 404,
		medialib.ErrBusy: 503, errFull: 503, errWaiters: 503, errNoKeyframe: 500, medialib.ErrTooLarge: 500,
	} {
		rec := &fibtest.Recorder{}
		c := fibhttp.NewStreamContext(nil, httpRequest("/x"), rec)
		v.fail(c, &medialib.File{Path: "p"}, err)
		if !rec.Wait(time.Second) || rec.Response.StatusCode != want || rec.Response.Header.Get("Access-Control-Allow-Origin") != "*" {
			t.Errorf("%v: %d, want %d", err, rec.Response.StatusCode, want)
		}
	}
}

// TestSendHelpers checks rangeWriter's limit on what it keeps, its copy of
// a reader that is not its file's range, and offsetFile at the end of the
// file, as the static handler's do.
func TestSendHelpers(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	o := &offsetFile{f: f}
	buf := make([]byte, 4)
	if n, err := o.Read(buf); n != 4 || err != nil {
		t.Fatal(n, err)
	}
	if _, err := o.Seek(8, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if n, err := o.Read(buf); n != 2 || err != nil {
		t.Fatal(n, err)
	}
	if n, err := o.Read(buf); n != 0 || err != io.EOF {
		t.Fatal(n, err)
	}
	w := &rangeWriter{src: o}
	if _, err := w.Write(make([]byte, maxSmall)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("x")); err == nil {
		t.Fatal("a body past maxSmall was kept")
	}
	w = &rangeWriter{src: o}
	if n, err := w.ReadFrom(strings.NewReader("abc")); n != 3 || err != nil || string(w.small) != "abc" {
		t.Fatal(n, err, w.small)
	}
	g := &gathered{header: http.Header{}}
	if _, err := g.Write([]byte("x")); err != nil || g.status != 200 {
		t.Fatal(g.status)
	}
}

// TestQueryWithoutToken checks a token query parameter left empty: no
// token is passed on.
func TestQueryWithoutToken(t *testing.T) {
	f := startVOD(t, copyLib(t), "", `{"library": "films", "auth": "site"}`, authConf)
	b := string(f.mustGet(t, "/vod/films/films/movie/index.m3u8?token=", 200, "Authorization", "Bearer "+viewToken))
	if strings.Contains(b, "token=") {
		t.Errorf("%s", b)
	}
}

// TestManyAtOnce plays every part of an item from many clients at once,
// under the race detector, with a pool and cache small enough to be
// contended.
func TestManyAtOnce(t *testing.T) {
	f := startVOD(t, copyLib(t), `, "file_workers": 2, "max_table_bytes": 65536`, `{"library": "films", "segment_duration": "1s"}`, "")
	var paths []string
	for _, c := range []string{"fmp4", "ts"} {
		for _, r := range []string{"v0", "v1", "a0"} {
			paths = append(paths, "/vod/films/films/movie/"+r+"."+c+".m3u8")
			for n := range 6 {
				ext := ".m4s"
				if c == "ts" {
					ext = ".ts"
				}
				paths = append(paths, "/vod/films/films/movie/"+r+"."+strconvI(n)+ext)
			}
		}
	}
	var wg sync.WaitGroup
	for g := range 16 {
		wg.Go(func() {
			for i := range paths {
				p := paths[(i+g*5)%len(paths)]
				resp, _, err := fetch("GET", f.base+p)
				if err != nil || resp.StatusCode != 200 && resp.StatusCode != 503 {
					t.Errorf("%s: %v %v", p, resp, err)
				}
			}
		})
	}
	wg.Wait()
}

func strconvI(n int) string { return string(rune('0' + n)) }
