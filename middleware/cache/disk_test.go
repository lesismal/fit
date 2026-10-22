package cache

import (
	"fmt"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/internal/fibtest"
	"github.com/lesismal/fit/internal/filepool"
	"github.com/lesismal/fit/module"
)

// diskOf returns zone's disk tier.
func diskOf(t *testing.T, zone string) *disk {
	t.Helper()
	zones.Lock()
	defer zones.Unlock()
	s := zones.m[zone]
	if s == nil || s.disk.Load() == nil {
		t.Fatalf("zone %q has no disk", zone)
	}
	return s.disk.Load()
}

// waitFor waits up to 5 seconds for cond.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("no %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitReady waits for d's index to be read back.
func waitReady(t *testing.T, d *disk) {
	t.Helper()
	select {
	case <-d.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("the index was not read back")
	}
}

func diskParams(dir, extra string) string {
	return fmt.Sprintf(`[{"module": "cache", "params": {"zone": "disk", "disk": {"path": %q}%s}}]`, dir, extra)
}

// TestDiskRestart checks that what a zone keeps on disk is found again by
// the next process's, through the index read back from the directory, and
// kept in memory once read.
func TestDiskRestart(t *testing.T) {
	dir := t.TempDir()
	c := clients()["http/1.1"]
	base, stop := start(t, diskParams(dir, ""))
	paths := []string{"/fresh", "/s-maxage", "/stream"}
	for _, p := range paths {
		get(t, c, "GET", base+p, "Host", "cache.test")
	}
	get(t, c, "GET", base+"/vary", "Accept-Language", "en", "Host", "cache.test")
	get(t, c, "GET", base+"/vary", "Accept-Language", "fr", "Host", "cache.test")
	d := diskOf(t, "disk")
	waitFor(t, "files written", func() bool { n, _ := d.stats(); return n == 5 })
	stop()
	waitFor(t, "the zone given back", func() bool {
		zones.Lock()
		defer zones.Unlock()
		_, kept := zones.m["disk"]
		return !kept
	})
	resetHits()
	t.Cleanup(resetHits)

	base, stop = start(t, diskParams(dir, ""))
	defer stop()
	d = diskOf(t, "disk")
	waitReady(t, d)
	if n, size := d.stats(); n != 5 || size <= 64<<10 {
		t.Fatalf("read back %d responses, %d bytes", n, size)
	}
	for _, p := range paths {
		r := get(t, c, "GET", base+p, "Host", "cache.test")
		if !strings.HasSuffix(r.cacheStatus(), "; detail=disk") || r.status != 200 {
			t.Errorf("%s: %d %q", p, r.status, r.cacheStatus())
		}
		if p == "/stream" && (r.body != long[:64<<10] || r.header.Get("Content-Length") != "65536") {
			t.Errorf("/stream: %d bytes, length %q", len(r.body), r.header.Get("Content-Length"))
		}
		if p == "/fresh" && (r.body != "/fresh 1" || r.header.Get("Etag") != `"v1"` || r.header.Get("Age") == "") {
			t.Errorf("/fresh: %q %v", r.body, r.header)
		}
		if r := get(t, c, "GET", base+p, "Host", "cache.test"); !strings.HasPrefix(r.cacheStatus(), "fit; hit") || strings.Contains(r.cacheStatus(), "disk") {
			t.Errorf("%s again: %q, want a hit in memory", p, r.cacheStatus())
		}
	}
	for _, lang := range []string{"en", "fr"} {
		r := get(t, c, "GET", base+"/vary", "Accept-Language", lang, "Host", "cache.test")
		if !strings.HasSuffix(r.body, lang) || !strings.HasSuffix(r.cacheStatus(), "detail=disk") {
			t.Errorf("/vary %s: %q %q", lang, r.body, r.cacheStatus())
		}
	}
	if r := get(t, c, "GET", base+"/fresh", "If-None-Match", `"v1"`, "Host", "cache.test"); r.status != 304 {
		t.Errorf("a precondition: %d", r.status)
	}
	for _, p := range append(paths, "/vary") {
		if n := hitsOf(p); n != 0 {
			t.Errorf("%s: the origin was asked %d times", p, n)
		}
	}
}

// TestDiskPromotion checks a zone whose memory holds less than its disk:
// what memory evicts is read back from disk.
func TestDiskPromotion(t *testing.T) {
	dir := t.TempDir()
	base := serve(t, diskParams(dir, `, "max_size": 1048576, "max_entry_size": 65536`))
	c := clients()["h2c"]
	const n = 64 // 64 KB each: four times what memory holds
	for i := range n {
		get(t, c, "GET", fmt.Sprintf("%s/stream?%d", base, i))
	}
	d := diskOf(t, "disk")
	waitFor(t, "files written", func() bool { k, _ := d.stats(); return k == n })
	fromDisk := 0
	for i := range n {
		r := get(t, c, "GET", fmt.Sprintf("%s/stream?%d", base, i))
		if !strings.HasPrefix(r.cacheStatus(), "fit; hit") || len(r.body) != 64<<10 {
			t.Fatalf("%d: %q, %d bytes", i, r.cacheStatus(), len(r.body))
		}
		if strings.HasSuffix(r.cacheStatus(), "detail=disk") {
			fromDisk++
		}
	}
	if fromDisk < n/2 || hitsOf("/stream") != n {
		t.Errorf("%d of %d from disk, the origin asked %d times", fromDisk, n, hitsOf("/stream"))
	}
}

// TestDiskNeverBlocks checks that a disk that hangs holds up no worker: the
// cache's ServeHTTP returns at once, and the request goes on as a miss once
// the pool's timeout has passed.
func TestDiskNeverBlocks(t *testing.T) {
	stuck := make(chan struct{})
	pool := filepool.New(1, 4, 300*time.Millisecond)
	dir := t.TempDir()
	m := &Cache{Zone: "hung", Disk: &Disk{Path: dir}}
	if err := m.Provision(&module.Context{}); err != nil {
		t.Fatal(err)
	}
	defer m.Cleanup()
	d := m.store.disk.Load()
	waitReady(t, d)
	var origin int
	h := m.Wrap(fibhttp.HandlerFunc(func(c *fibhttp.Context) {
		origin++
		_ = c.WriteResponse(fibhttp.Response{StatusCode: 200, Header: stdhttp.Header{"Cache-Control": {"max-age=60"}}, Body: []byte("body")})
	}))
	if rec := fibtest.Serve(h, "GET", "http://example.com/x"); rec.Response.StatusCode != 200 {
		t.Fatal(rec.Response.StatusCode)
	}
	waitFor(t, "the file written", func() bool { n, _ := d.stats(); return n == 1 })
	// Only the disk holds it now.
	sh := m.store.shard("http://example.com/x")
	sh.mu.Lock()
	sh.drop(sh.items["http://example.com/x"])
	sh.mu.Unlock()

	d.pool = pool
	pool.Hook = func() { <-stuck }
	// Cleanups run last first: the disk comes back, and what it was asked
	// to write is done, before the directory goes.
	t.Cleanup(func() {
		close(stuck)
		deadline := time.Now().Add(5 * time.Second)
		for d.pending.Load() != 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
	})
	for i := range 2 {
		rec := &fibtest.Recorder{}
		started := time.Now()
		fibhttp.Serve(h, fibhttp.NewStreamContext(nil, httptest.NewRequest("GET", "http://example.com/x", nil), rec))
		if took := time.Since(started); took > 100*time.Millisecond {
			t.Errorf("%d: ServeHTTP took %v", i, took)
		}
		if !rec.Wait(5 * time.Second) {
			t.Fatalf("%d: no answer", i)
		}
		if got := rec.Response.Header.Get("Cache-Status"); rec.Response.StatusCode != 200 || got != "fit; fwd=uri-miss; stored" {
			t.Errorf("%d: %d %q", i, rec.Response.StatusCode, got)
		}
		sh.mu.Lock()
		sh.drop(sh.items["http://example.com/x"])
		sh.mu.Unlock()
	}
	if origin != 3 {
		t.Errorf("the origin answered %d times", origin)
	}
}

// TestDiskBrokenFiles checks that reading the index back removes what is not
// a whole file of the cache's, and clears writes cut short; and that a file
// gone from under its entry leaves the request to the origin.
func TestDiskBrokenFiles(t *testing.T) {
	dir := t.TempDir()
	c := clients()["http/1.1"]
	base, stop := start(t, diskParams(dir, ""))
	get(t, c, "GET", base+"/fresh", "Host", "cache.test")
	get(t, c, "GET", base+"/expires", "Host", "cache.test")
	d := diskOf(t, "disk")
	waitFor(t, "files written", func() bool { n, _ := d.stats(); return n == 2 })
	stop()
	resetHits()
	t.Cleanup(resetHits)

	var files []string
	_ = filepath.WalkDir(dir, func(p string, e os.DirEntry, _ error) error {
		if !e.IsDir() {
			files = append(files, p)
		}
		return nil
	})
	if len(files) != 2 {
		t.Fatalf("%d files", len(files))
	}
	// One cut short; junk beside it, and in tmp.
	data, _ := os.ReadFile(files[0])
	if err := os.WriteFile(files[0], data[:len(data)-3], 0o600); err != nil {
		t.Fatal(err)
	}
	junk := filepath.Join(filepath.Dir(files[1]), filepath.Base(filepath.Dir(files[1]))+"junk")
	_ = os.WriteFile(junk, []byte("not a cache file"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "tmp", "w123"), []byte("half"), 0o600)

	base, stop = start(t, diskParams(dir, ""))
	defer stop()
	d = diskOf(t, "disk")
	waitReady(t, d)
	if n, _ := d.stats(); n != 1 {
		t.Errorf("%d responses read back, want 1", n)
	}
	for _, f := range []string{files[0], junk, filepath.Join(dir, "tmp", "w123")} {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Errorf("%s was not removed: %v", f, err)
		}
	}
	// The whole one is gone from under its entry.
	if err := os.Remove(files[1]); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/fresh", "/expires"} {
		if r := get(t, c, "GET", base+p, "Host", "cache.test"); r.status != 200 || !strings.HasPrefix(r.cacheStatus(), "fit; fwd=uri-miss") {
			t.Errorf("%s: %d %q", p, r.status, r.cacheStatus())
		}
	}
}

// TestDiskInvalidate checks that a request that changes a resource removes
// its response from disk too, so that a restart does not bring it back, and
// that a write queued before the removal is not kept.
func TestDiskInvalidate(t *testing.T) {
	dir := t.TempDir()
	c := clients()["http/1.1"]
	base, stop := start(t, diskParams(dir, ""))
	get(t, c, "GET", base+"/fresh", "Host", "cache.test")
	get(t, c, "GET", base+"/s-maxage", "Host", "cache.test")
	d := diskOf(t, "disk")
	waitFor(t, "files written", func() bool { n, _ := d.stats(); return n == 2 })
	get(t, c, "DELETE", base+"/fresh", "Host", "cache.test")
	waitFor(t, "the file removed", func() bool { n, _ := d.stats(); return n == 1 })
	// A write queued before a removal is dropped when it lands.
	e := &entry{status: 200, header: stdhttp.Header{}, body: []byte("old"), responseTime: clock(), lifetime: time.Minute}
	seq := d.seq.Add(1)
	de, err := d.writeFile("http://late/", e)
	if err != nil {
		t.Fatal(err)
	}
	d.remove("http://late/")
	d.add("http://late/", de, seq)
	d.drain()
	if _, err := os.Stat(filepath.Join(dir, de.file)); !os.IsNotExist(err) {
		t.Errorf("a write queued before its removal was kept: %v", err)
	}
	stop()
	resetHits()
	t.Cleanup(resetHits)

	base, stop = start(t, diskParams(dir, ""))
	defer stop()
	waitReady(t, diskOf(t, "disk"))
	if r := get(t, c, "GET", base+"/fresh", "Host", "cache.test"); r.cacheStatus() != "fit; fwd=uri-miss; stored" {
		t.Errorf("/fresh after its DELETE: %q", r.cacheStatus())
	}
	if r := get(t, c, "GET", base+"/s-maxage", "Host", "cache.test"); !strings.HasSuffix(r.cacheStatus(), "detail=disk") {
		t.Errorf("/s-maxage: %q", r.cacheStatus())
	}
}

// TestDiskBounds checks max_entries and max_size: the least recently used
// responses go, and their files with them.
func TestDiskBounds(t *testing.T) {
	dir := t.TempDir()
	d, err := newDisk(&Disk{Path: dir, MaxEntries: 10, MaxSize: 1 << 20}, filepool.New(2, 64, time.Second), &tagIndex{m: map[string]map[string]int{}})
	if err != nil {
		t.Fatal(err)
	}
	waitReady(t, d)
	body := make([]byte, 10<<10)
	for i := range 30 {
		d.write(fmt.Sprintf("/k%d", i), &entry{status: 200, header: stdhttp.Header{}, body: body, responseTime: clock(), lifetime: time.Minute})
		waitFor(t, "a write", func() bool { return d.pending.Load() == 0 })
	}
	d.drain()
	files := func() int {
		n := 0
		_ = filepath.WalkDir(dir, func(p string, e os.DirEntry, _ error) error {
			if !e.IsDir() {
				n++
			}
			return nil
		})
		return n
	}
	if n, _ := d.stats(); n != 10 || files() != 10 {
		t.Errorf("max_entries 10: %d entries, %d files", n, files())
	}
	if d.get("/k29", nil) == nil || d.get("/k0", nil) != nil {
		t.Error("not the least recently used evicted")
	}
	d.setLimits(&Disk{MaxEntries: 100, MaxSize: 50 << 10})
	d.write("/big", &entry{status: 200, header: stdhttp.Header{}, body: body, responseTime: clock(), lifetime: time.Minute})
	waitFor(t, "a write", func() bool { return d.pending.Load() == 0 })
	d.drain()
	if n, size := d.stats(); size > 50<<10 || n != 4 || files() != 4 {
		t.Errorf("max_size 50 KB: %d entries, %d bytes, %d files", n, size, files())
	}
}

// TestDiskZones checks that two zones cannot share a directory, and that a
// zone whose disk moves takes the new directory.
func TestDiskZones(t *testing.T) {
	dir := t.TempDir()
	a := &Cache{Zone: "a", Disk: &Disk{Path: dir}}
	if err := a.Provision(&module.Context{}); err != nil {
		t.Fatal(err)
	}
	defer a.Cleanup()
	b := &Cache{Zone: "b", Disk: &Disk{Path: dir + "/"}}
	if err := b.Provision(&module.Context{}); err == nil || !strings.Contains(err.Error(), `zone "a"`) {
		t.Errorf("a second zone in the same directory: %v", err)
	}
	other := t.TempDir()
	moved := &Cache{Zone: "a", Disk: &Disk{Path: other}}
	if err := moved.Provision(&module.Context{}); err != nil {
		t.Fatal(err)
	}
	defer moved.Cleanup()
	if a.store.disk.Load().dir != other {
		t.Error("the zone kept its old directory")
	}
	none := &Cache{Zone: "a"}
	if err := none.Provision(&module.Context{}); err != nil {
		t.Fatal(err)
	}
	defer none.Cleanup()
	if a.store.disk.Load() != nil {
		t.Error("the zone kept a disk its last config dropped")
	}
}
