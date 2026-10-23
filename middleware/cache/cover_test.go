package cache

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	stdhttp "net/http"
	"net/http/httptest"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/internal/fibtest"
	"github.com/lesismal/fit/internal/filepool"
	"github.com/lesismal/fit/module"
)

// provisioned returns a cache of zone, given back when the test ends.
func provisioned(t *testing.T, m *Cache) *Cache {
	t.Helper()
	if err := m.Provision(&module.Context{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Cleanup)
	return m
}

// answering is an origin that answers each request with header and body.
func answering(header stdhttp.Header, body string, count *int) fibhttp.Handler {
	return fibhttp.HandlerFunc(func(c *fibhttp.Context) {
		if count != nil {
			*count++
		}
		h := header.Clone()
		h.Set("Date", clock().UTC().Format(stdhttp.TimeFormat))
		_ = c.WriteResponse(fibhttp.Response{StatusCode: 200, Header: h, Body: []byte(body)})
	})
}

func newContext(r *stdhttp.Request) *fibhttp.Context {
	return fibhttp.NewStreamContext(nil, r, &fibtest.Recorder{})
}

func TestSemantics(t *testing.T) {
	if d := parseDirectives([]string{"proxy-revalidate"}); !d.proxyRevalidate {
		t.Error("proxy-revalidate")
	}
	if d := requestDirectives(stdhttp.Header{"Pragma": {"x"}}); d.noCache {
		t.Error("Pragma: x is no no-cache")
	}
	if d := requestDirectives(stdhttp.Header{"Pragma": {"No-Cache"}}); !d.noCache {
		t.Error("Pragma: No-Cache")
	}
	if l, explicit := freshness(stdhttp.Header{"Expires": {}}, parseDirectives(nil), 200, time.Now(), 0); l != 0 || !explicit {
		t.Errorf("an empty Expires: %v %v", l, explicit)
	}
	for _, tc := range []struct {
		lines []string
		want  string
	}{
		{[]string{"gzip, br", "zstd"}, "gzip,br,zstd"},
		{[]string{" gzip"}, "gzip"},
		{[]string{"gzip,br"}, "gzip,br"},
	} {
		if got := varyValue(stdhttp.Header{"Accept-Encoding": tc.lines}, "Accept-Encoding"); got != tc.want {
			t.Errorf("%q: %q", tc.lines, got)
		}
	}
	for _, tc := range []struct {
		h        stdhttp.Header
		etag, lm string
		want     bool
	}{
		{stdhttp.Header{"If-None-Match": {`"a"`}}, "", "", false},
		{stdhttp.Header{"If-Modified-Since": {"yesterday"}}, "", "Mon, 02 Jan 2006 15:04:05 GMT", false},
		{stdhttp.Header{"If-Modified-Since": {"Mon, 02 Jan 2006 15:04:05 GMT"}}, "", "then", false},
	} {
		if got := notModified(tc.h, tc.etag, tc.lm); got != tc.want {
			t.Errorf("%v: %v", tc.h, got)
		}
	}
	if (&entry{varyNames: []string{"A"}, varyValues: []string{"x"}}).sameVariant(&entry{}) {
		t.Error("variants of different fields")
	}
	if !(fetchResult{status: 200, failed: true}).erred() || (fetchResult{status: 200}).erred() {
		t.Error("erred")
	}
	if unsafeMethod("OPTIONS") || unsafeMethod("TRACE") || !unsafeMethod("PATCH") {
		t.Error("unsafeMethod")
	}
	for _, tc := range []struct {
		h    stdhttp.Header
		want bool
	}{
		{stdhttp.Header{"Trailer": {"X"}}, true},
		{stdhttp.Header{stdhttp.TrailerPrefix + "Fit-Error": {"x"}}, true},
		{stdhttp.Header{"X": {"y"}}, false},
	} {
		if hasTrailer(tc.h) != tc.want {
			t.Errorf("hasTrailer(%v)", tc.h)
		}
	}
}

// TestFetchStream checks the stream a fetch of the cache's own answers
// into: it takes everything, and an end cut short fails it.
func TestFetchStream(t *testing.T) {
	s := &fetchStream{}
	if s.WriteInterim(103, nil) != nil || s.Push(nil, "/", nil) == nil || s.BeginResponse(nil, 200, nil) != nil ||
		s.WriteBody(nil, true) != nil || s.NotifyWritable(nil) {
		t.Error("the stream refused what it takes")
	}
	if above, err := s.WriteBodyNoWait([]byte("x")); above || err != nil {
		t.Error(above, err)
	}
	if s.status.Load() != 200 {
		t.Errorf("status %d", s.status.Load())
	}
	_ = s.EndResponse(nil, false)
	if !s.failed.Load() {
		t.Error("an end cut short")
	}
	s.failed.Store(false)
	s.Abandon()
	if !s.failed.Load() {
		t.Error("abandoned")
	}
}

// TestCapture checks a capture that gives up: past max_entry_bytes with no
// declared length, and past a quarter of the store held by captures.
func TestCapture(t *testing.T) {
	c := newContext(httptest.NewRequest("GET", "/", nil))
	s := newStore("capture", 1<<20)
	f := &capture{store: s, c: c, key: "k", e: &entry{status: 200}, want: -1, limit: 10}
	if p, err := f.Filter(nil); p != nil || err != nil {
		t.Error("an empty write")
	}
	if p, _ := f.Filter(make([]byte, 20)); len(p) != 20 || !f.failed {
		t.Error("past the limit")
	}
	if p, _ := f.Filter(make([]byte, 5)); len(p) != 5 {
		t.Error("after giving up")
	}
	if p, err := f.Close(); p != nil || err != nil {
		t.Error("closed after giving up")
	}
	if e, _ := s.get("k", nil); e != nil {
		t.Error("kept after giving up")
	}
	s = newStore("capture", 64)
	f = &capture{store: s, c: c, key: "k", e: &entry{status: 200}, want: -1, limit: 100}
	if _, _ = f.Filter(make([]byte, 20)); !f.failed || s.capturing.Load() != 0 {
		t.Errorf("past a quarter of the store: failed %v, %d held", f.failed, s.capturing.Load())
	}
	x := &miss{c: c, storable: true, ranged: true}
	if x.capture(206, stdhttp.Header{}) != nil || x.capture(200, stdhttp.Header{"Content-Range": {"bytes 0-1/2"}}) != nil {
		t.Error("a part of a ranged response kept")
	}
}

// TestFreshen checks a 304 that is not for the stale response, by its ETag
// or Last-Modified, and one whose fields forbid keeping it.
func TestFreshen(t *testing.T) {
	m := provisioned(t, &Cache{Zone: "freshen"})
	c := newContext(httptest.NewRequest("GET", "/", nil))
	lm := "Mon, 02 Jan 2006 15:04:05 GMT"
	for _, tc := range []struct {
		old  *entry
		h    stdhttp.Header
		kept bool
	}{
		{&entry{status: 200, header: stdhttp.Header{}}, stdhttp.Header{"Etag": {`"a"`}}, true},
		{&entry{status: 200, header: stdhttp.Header{}, etag: `"b"`}, stdhttp.Header{"Etag": {`"a"`}}, true},
		{&entry{status: 200, header: stdhttp.Header{}, lastModified: lm}, stdhttp.Header{"Last-Modified": {"Tue, 03 Jan 2006 15:04:05 GMT"}}, true},
		{&entry{status: 200, header: stdhttp.Header{}, lastModified: lm}, stdhttp.Header{"Last-Modified": {lm}, "Cache-Control": {"no-store"}}, false},
	} {
		m.store.put("k", tc.old)
		x := &miss{m: m, c: c, key: "k", stale: tc.old, requestTime: clock()}
		x.freshen(tc.h)
		if e, _ := m.store.get("k", nil); (e == tc.old) != tc.kept {
			t.Errorf("%v: kept %v", tc.h, e == tc.old)
		}
	}
}

// TestWaitEnded checks a request that comes to wait for a fetch that has
// ended, and one that finds as many waiting as may.
func TestWaitEnded(t *testing.T) {
	m := provisioned(t, &Cache{Zone: "wait"})
	origin := answering(stdhttp.Header{"Cache-Control": {"max-age=60"}}, "origin", nil)
	for _, f := range []*fetch{
		{key: "http://example.com/a", done: true, result: fetchResult{status: 200}},
		{key: "http://example.com/a", waiters: make([]*waiter, maxWaiters)},
	} {
		h := fibhttp.HandlerFunc(func(c *fibhttp.Context) {
			x := &miss{m: m, c: c, key: f.key, requestTime: clock(), req: requestDirectives(c.Request.Header)}
			m.wait(f, x, origin)
		})
		if rec := fibtest.Serve(h, "GET", "http://example.com/a"); rec.Response.StatusCode != 200 || string(rec.Response.Body) != "origin" {
			t.Errorf("%d %q", rec.Response.StatusCode, rec.Response.Body)
		}
	}
	// A revalidation of a response no longer the one kept, and one past
	// the zone's background fetches.
	e := &entry{status: 200, header: stdhttp.Header{}, lifetime: time.Minute, responseTime: clock()}
	r := httptest.NewRequest("GET", "http://example.com/b", nil)
	m.revalidate("http://example.com/b", e, r, origin)
	m.store.put("http://example.com/b", e)
	m.store.background.Store(maxBackground)
	m.revalidate("http://example.com/b", e, r, origin)
	if m.store.background.Load() != maxBackground {
		t.Errorf("%d background fetches", m.store.background.Load())
	}
	m.store.background.Store(0)
	if f, first := m.store.lock("http://example.com/b", clock(), time.Minute); !first {
		t.Errorf("a fetch left under way: %+v", f)
	}
}

// TestWaiterGone checks a request whose client goes while it waits for
// another's fetch.
func TestWaiterGone(t *testing.T) {
	base := serve(t, cacheOnly)
	c := clients()["h2c"]
	url := base + "/late?delay=500ms"
	first := make(chan reply, 1)
	go func() { first <- get(t, c, "GET", url) }()
	time.Sleep(100 * time.Millisecond) // for the first to fetch
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := stdhttp.NewRequestWithContext(ctx, "GET", url, nil)
	done := make(chan struct{})
	go func() {
		if resp, err := c.Do(req); err == nil {
			resp.Body.Close()
		}
		close(done)
	}()
	time.Sleep(100 * time.Millisecond) // for it to wait
	cancel()
	<-done
	if r := <-first; r.status != 200 {
		t.Errorf("the fetch: %d", r.status)
	}
	if hitsOf("/late") != 1 {
		t.Errorf("%d fetches", hitsOf("/late"))
	}
}

// TestTLSKeys checks that a request over TLS has its own key, and that one
// that changes a resource over TLS invalidates it.
func TestTLSKeys(t *testing.T) {
	m := provisioned(t, &Cache{Zone: "tls"})
	var n int
	h := m.Wrap(fibhttp.HandlerFunc(func(c *fibhttp.Context) {
		if c.Request.Method == "POST" {
			_ = c.WriteResponse(fibhttp.Response{StatusCode: 201, Header: stdhttp.Header{"Location": {"/other"}}})
			return
		}
		n++
		_ = c.WriteResponse(fibhttp.Response{StatusCode: 200, Header: stdhttp.Header{"Cache-Control": {"max-age=60"}}, Body: []byte("x")})
	}))
	req := func(method string) *stdhttp.Request {
		r := httptest.NewRequest(method, "https://example.com/a", nil)
		r.TLS = &tls.ConnectionState{}
		return r
	}
	fibtest.ServeRequest(h, req("GET"))
	if rec := fibtest.ServeRequest(h, req("GET")); !strings.HasPrefix(rec.Response.Header.Get("Cache-Status"), "fit; hit") {
		t.Errorf("over TLS: %q", rec.Response.Header.Get("Cache-Status"))
	}
	if e, _ := m.store.get("https://example.com/a", nil); e == nil {
		t.Error("not kept under https://")
	}
	fibtest.ServeRequest(h, req("POST"))
	if e, _ := m.store.get("https://example.com/a", nil); e != nil {
		t.Error("not invalidated over TLS")
	}
	if rec := fibtest.ServeRequest(h, httptest.NewRequest("OPTIONS", "http://example.com/a", nil)); rec.Response.StatusCode != 200 || n != 2 {
		t.Errorf("OPTIONS: %d, %d fetches", rec.Response.StatusCode, n)
	}
}

// diskCache returns a cache of zone with a disk tier in dir, its index read
// back.
func diskCache(t *testing.T, zone, dir string) (*Cache, *disk) {
	t.Helper()
	m := provisioned(t, &Cache{Zone: zone, Disk: &Disk{Path: dir}})
	d := m.store.disk.Load()
	waitReady(t, d)
	return m, d
}

// memoryDrop drops key from memory alone.
func memoryDrop(m *Cache, key string) {
	sh := m.store.shard(key)
	sh.mu.Lock()
	if it := sh.items[key]; it != nil {
		sh.drop(it)
	}
	sh.mu.Unlock()
}

// TestDiskStale checks responses only the disk holds once they are stale:
// one that allows stale-while-revalidate answered from disk, one that does
// not sent on.
func TestDiskStale(t *testing.T) {
	start := time.Now()
	at(t, start, 0)
	m, d := diskCache(t, "diskstale", t.TempDir())
	var n int
	plain := m.Wrap(answering(stdhttp.Header{"Cache-Control": {"max-age=10"}}, "plain", &n))
	swr := m.Wrap(answering(stdhttp.Header{"Cache-Control": {"max-age=10, stale-while-revalidate=60"}}, "swr", &n))
	fibtest.Serve(plain, "GET", "http://example.com/plain")
	fibtest.Serve(swr, "GET", "http://example.com/swr")
	waitFor(t, "files written", func() bool { c, _ := d.stats(); return c == 2 })
	memoryDrop(m, "http://example.com/plain")
	memoryDrop(m, "http://example.com/swr")
	at(t, start, 20*time.Second)
	if rec := fibtest.Serve(plain, "GET", "http://example.com/plain"); rec.Response.Header.Get("Cache-Status") != "fit; fwd=stale; stored" {
		t.Errorf("stale on disk: %q", rec.Response.Header.Get("Cache-Status"))
	}
	if rec := fibtest.Serve(swr, "GET", "http://example.com/swr"); !strings.HasSuffix(rec.Response.Header.Get("Cache-Status"), "detail=stale-while-revalidate") {
		t.Errorf("stale-while-revalidate on disk: %q", rec.Response.Header.Get("Cache-Status"))
	}
	waitFor(t, "the background fetch", func() bool { return m.store.background.Load() == 0 })
	if n != 4 {
		t.Errorf("%d fetches", n)
	}
	// A 304 freshens a response on disk too.
	e := &entry{status: 200, header: stdhttp.Header{}, body: []byte("old"), responseTime: clock(), lifetime: time.Minute}
	m.store.put("http://example.com/r", e)
	fresh := &entry{status: 200, header: stdhttp.Header{}, body: []byte("old"), responseTime: clock(), lifetime: 2 * time.Minute}
	m.store.replace("http://example.com/r", e, fresh)
	waitFor(t, "the freshened file", func() bool { return d.get("http://example.com/r", nil) != nil })
}

// TestDiskRefused checks a disk pool that takes nothing: the request goes
// on, and writes and removals are dropped.
func TestDiskRefused(t *testing.T) {
	m, d := diskCache(t, "diskrefused", t.TempDir())
	var n int
	h := m.Wrap(answering(stdhttp.Header{"Cache-Control": {"max-age=60"}}, "x", &n))
	fibtest.Serve(h, "GET", "http://example.com/x")
	waitFor(t, "the file written", func() bool { c, _ := d.stats(); return c == 1 })
	memoryDrop(m, "http://example.com/x")
	d.pool = filepool.New(0, 0, time.Second)
	if rec := fibtest.Serve(h, "GET", "http://example.com/x"); rec.Response.StatusCode != 200 || n != 2 {
		t.Errorf("%d, %d fetches", rec.Response.StatusCode, n)
	}
	d.remove("http://example.com/x")
	d.pending.Store(maxPendingWrites)
	d.write("http://example.com/y", &entry{status: 200, body: []byte("y")})
	if d.pending.Load() != maxPendingWrites {
		t.Errorf("%d pending", d.pending.Load())
	}
	d.pending.Store(0)
}

// TestDiskAdd checks the index: an older response does not replace a
// newer one, a variant's new file trashes its old one, and past
// maxVariants the oldest goes.
func TestDiskAdd(t *testing.T) {
	d, err := newDisk(&Disk{Path: t.TempDir()}, filepool.New(2, 64, time.Second), &tagIndex{m: map[string]map[string]int{}})
	if err != nil {
		t.Fatal(err)
	}
	waitReady(t, d)
	now := time.Now()
	de := func(file string, at time.Time, vary string) *diskEntry {
		e := &entry{responseTime: at}
		if vary != "" {
			e.varyNames, e.varyValues = []string{"A"}, []string{vary}
		}
		return &diskEntry{meta: e, file: file, size: 10}
	}
	d.add("k", de("new", now, ""), 0)
	d.add("k", de("old", now.Add(-time.Second), ""), 0)
	if got := d.get("k", nil); got == nil || got.file != "new" {
		t.Errorf("an older response replaced a newer: %+v", got)
	}
	d.add("k", de("newer", now.Add(time.Second), ""), 0)
	d.mu.Lock()
	trash := append([]string(nil), d.trash...)
	d.mu.Unlock()
	if len(trash) != 1 || trash[0] != "new" {
		t.Errorf("trash %q", trash)
	}
	for i := range maxVariants + 1 {
		d.add("v", de("v"+string(rune('a'+i)), now, string(rune('a'+i))), 0)
	}
	if n, _ := d.stats(); n != 1+maxVariants {
		t.Errorf("%d entries", n)
	}
	if d.get("v", stdhttp.Header{"A": {"a"}}) != nil {
		t.Error("the oldest variant kept")
	}
}

// TestDiskFiles checks files that cannot be written, read or read back.
func TestDiskFiles(t *testing.T) {
	dir := t.TempDir()
	d, err := newDisk(&Disk{Path: dir}, filepool.New(2, 64, time.Second), &tagIndex{m: map[string]map[string]int{}})
	if err != nil {
		t.Fatal(err)
	}
	waitReady(t, d)
	e := func(string) *entry {
		return &entry{status: 200, header: stdhttp.Header{"A": {"b"}}, body: []byte("body"), responseTime: time.Now(), lifetime: time.Minute}
	}
	if _, err := d.writeFile("big", &entry{header: stdhttp.Header{"A": {strings.Repeat("x", maxMeta)}}}); err == nil {
		t.Error("fields past maxMeta written")
	}
	// Its directory is a file.
	name := fileName("blocked", e("blocked"))
	if err := os.WriteFile(filepath.Join(dir, filepath.Dir(name)), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := d.writeFile("blocked", e("blocked")); err == nil {
		t.Error("written under a file")
	}
	// Its place is a directory.
	name = fileName("taken", e("taken"))
	if err := os.MkdirAll(filepath.Join(dir, name, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := d.writeFile("taken", e("taken")); err == nil {
		t.Error("written over a directory")
	}
	// A good one, then copies of it under names not its own, and junk.
	good, err := d.writeFile("good", e("good"))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, good.file))
	sub := filepath.Dir(good.file)
	misnamed := filepath.Join(dir, sub, filepath.Base(sub)+"misnamed")
	_ = os.WriteFile(misnamed, data, 0o600)
	junk := filepath.Join(dir, sub, filepath.Base(sub)+"junk")
	_ = os.WriteFile(junk, []byte("junk"), 0o600)
	if _, err := d.read(&diskEntry{file: filepath.Join(sub, filepath.Base(junk))}); err == nil {
		t.Error("junk read")
	}
	// No tmp to write through.
	if err := os.RemoveAll(filepath.Join(dir, "tmp")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.writeFile("notmp", e("notmp")); err == nil {
		t.Error("written with no tmp")
	}
	_ = os.MkdirAll(filepath.Join(dir, "tmp"), 0o700)

	d2, err := newDisk(&Disk{Path: dir}, filepool.New(2, 64, time.Second), &tagIndex{m: map[string]map[string]int{}})
	if err != nil {
		t.Fatal(err)
	}
	waitReady(t, d2)
	if n, _ := d2.stats(); n != 1 {
		t.Errorf("%d read back", n)
	}
	for _, f := range []string{misnamed, junk} {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Errorf("%s kept: %v", f, err)
		}
	}
	// A disk under a file.
	file := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(file, nil, 0o600)
	if _, err := newDisk(&Disk{Path: filepath.Join(file, "cache")}, nil, nil); err == nil {
		t.Error("a disk under a file")
	}
	m := &Cache{Zone: "underfile", Disk: &Disk{Path: filepath.Join(file, "cache")}}
	if err := m.Provision(&module.Context{}); err == nil {
		m.Cleanup()
		t.Error("a zone's disk under a file")
	}
}

// fileOf makes a file's bytes: its header, saying n bytes of metadata,
// then rest.
func fileOf(magic string, n uint32, rest string) []byte {
	b := make([]byte, fileHead)
	copy(b, magic)
	b[4] = fileVersion
	binary.BigEndian.PutUint32(b[8:], n)
	return append(b, rest...)
}

func TestParseFile(t *testing.T) {
	meta := `{"key":"k","status":200,"header":{},"body_len":2}`
	for name, data := range map[string][]byte{
		"short":         []byte("FITC"),
		"magic":         fileOf("NOPE", 2, "{}"),
		"meta too long": fileOf(fileMagic, maxMeta+1, "{}"),
		"meta cut":      fileOf(fileMagic, 100, "{}"),
		"json":          fileOf(fileMagic, 2, "{]"),
		"body cut":      fileOf(fileMagic, uint32(len(meta)), meta+"x"),
		"vary":          fileOf(fileMagic, uint32(len(`{"vary_names":["A"]}`)), `{"vary_names":["A"]}`),
	} {
		if _, _, err := parseFile(data); err == nil {
			t.Errorf("%s: parsed", name)
		}
		path := filepath.Join(t.TempDir(), "f")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := readMeta(path); err == nil {
			t.Errorf("%s: its metadata read", name)
		}
	}
	if _, _, err := parseFile(fileOf(fileMagic, uint32(len(meta)), meta+"ok")); err != nil {
		t.Error(err)
	}
	if _, _, err := readMeta(filepath.Join(t.TempDir(), "none")); err == nil {
		t.Error("no file")
	}
}

// TestZonesSorted checks Zones with more than one zone, and a zone taken
// again with the same disk.
func TestZonesSorted(t *testing.T) {
	dir := t.TempDir()
	provisioned(t, &Cache{Zone: "zb", Disk: &Disk{Path: dir}})
	provisioned(t, &Cache{Zone: "zb", Disk: &Disk{Path: dir, MaxEntries: 5}})
	provisioned(t, &Cache{Zone: "za"})
	var names []string
	for _, z := range Zones() {
		names = append(names, z.Zone)
	}
	if i, j := strings.Index(strings.Join(names, ","), "za"), strings.Index(strings.Join(names, ","), "zb"); i < 0 || j < i {
		t.Errorf("zones %q", names)
	}
	zones.Lock()
	d := zones.m["zb"].disk.Load()
	zones.Unlock()
	if d.maxEntries.Load() != 5 {
		t.Errorf("max_entries %d", d.maxEntries.Load())
	}
}
