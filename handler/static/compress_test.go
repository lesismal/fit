package static

import (
	"bytes"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"
	"github.com/lesismal/fib/filewatch"

	"github.com/lesismal/fit/internal/fibtest"
	"github.com/lesismal/fit/internal/filepool"
)

// text is n bytes of JavaScript-like text that compresses as such text does.
func text(n int) string {
	r := rand.New(rand.NewPCG(3, 4))
	words := strings.Fields("function return const let var if else for while this value index length push call apply")
	var b strings.Builder
	for b.Len() < n {
		fmt.Fprintf(&b, "%s v%d=%d;", words[r.IntN(len(words))], r.IntN(1000), r.IntN(1e6))
	}
	return b.String()[:n]
}

// noise is n bytes that do not compress.
func noise(n int) string {
	r := rand.New(rand.NewPCG(5, 6))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.UintN(256))
	}
	return string(b)
}

// unpack decompresses body by encoding.
func unpack(t *testing.T, encoding string, body []byte) string {
	t.Helper()
	var r io.Reader
	switch encoding {
	case "gzip":
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r = zr
	case "br":
		r = brotli.NewReader(bytes.NewReader(body))
	case "zstd":
		zr, err := zstd.NewReader(bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer zr.Close()
		r = zr
	default:
		return string(body)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// until gets target with header until its response's Content-Encoding is
// encoding, and returns that response.
func until(t *testing.T, s *Static, target, encoding string, header map[string]string) *fibtest.Recorder {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		rec := get(s, "GET", target, header)
		if rec.Response.Header.Get("Content-Encoding") == encoding {
			return rec
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s %v: still %q", target, header, rec.Response.Header.Get("Content-Encoding"))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestCompressCopies checks that a file kept in memory goes as it is the
// first time a client asks for an encoding, and compressed from then on, one
// copy for each encoding, which decompresses to the file and has its own
// ETag; and the files, requests and encodings that get the file as it is.
func TestCompressCopies(t *testing.T) {
	js := text(20000)
	root := tree(t, map[string]string{
		"app.js": js, "small.txt": "short", "pic.png": text(20000), "noise.txt": noise(20000),
		"twin.js": js, "twin.js.gz": "gzip-twin",
	})
	s := mustLoad(t, root, `, "compress": ["br", "zstd", "gzip"], "precompressed": ["gzip"]`)
	identity := get(s, "GET", "/app.js", nil).Response
	if identity.Header.Get("Content-Encoding") != "" || string(identity.Body) != js || identity.Header.Get("Vary") != "Accept-Encoding" {
		t.Fatalf("without Accept-Encoding: %v", identity.Header)
	}
	for _, tt := range []struct{ accept, encoding string }{
		{"gzip", "gzip"},
		{"gzip, br, zstd", "br"}, // equal q: the first listed
		{"br;q=0.5, zstd", "zstd"},
	} {
		header := map[string]string{"Accept-Encoding": tt.accept}
		got := until(t, s, "/app.js", tt.encoding, header).Response
		if body := unpack(t, tt.encoding, got.Body); body != js || len(got.Body) >= len(js)/2 {
			t.Errorf("%q: %d bytes, %d decompressed", tt.accept, len(got.Body), len(body))
		}
		tag := got.Header.Get("Etag")
		if tag != strings.TrimSuffix(identity.Header.Get("Etag"), `"`)+"-"+tt.encoding+`"` ||
			got.Header.Get("Accept-Ranges") != "" || got.Header.Get("Vary") != "Accept-Encoding" ||
			got.Header.Get("Content-Type") != "text/javascript; charset=utf-8" {
			t.Errorf("%q: %v", tt.accept, got.Header)
		}
		header["If-None-Match"] = tag
		if got := get(s, "GET", "/app.js", header).Response; got.StatusCode != 304 {
			t.Errorf("%q: If-None-Match on the copy: %d", tt.accept, got.StatusCode)
		}
	}
	// The copies count in the cache's bytes.
	s.cache.mu.RLock()
	e := s.cache.entries[filepath.Join(s.root, "app.js")]
	size, total := e.size, s.cache.bytes
	s.cache.mu.RUnlock()
	packed := int64(0)
	for i := range e.packs {
		packed += int64(len(e.packs[i].Load().data))
	}
	if size != int64(len(js))+packed || total < size {
		t.Errorf("entry of %d bytes, %d of them copies; cache %d", size, packed, total)
	}

	// A range is of the file as it is.
	got := get(s, "GET", "/app.js", map[string]string{"Accept-Encoding": "gzip", "Range": "bytes=0-3"}).Response
	if got.StatusCode != 206 || string(got.Body) != js[:4] || got.Header.Get("Content-Encoding") != "" {
		t.Errorf("a range: %d %v", got.StatusCode, got.Header)
	}
	// A precompressed twin goes ahead of a copy.
	if got := get(s, "GET", "/twin.js", map[string]string{"Accept-Encoding": "gzip"}).Response; string(got.Body) != "gzip-twin" {
		t.Errorf("the twin: %v %q", got.Header, got.Body)
	}
	// Small files, images and files that do not compress go as they are.
	for _, name := range []string{"/small.txt", "/pic.png", "/noise.txt"} {
		for range 3 {
			got := get(s, "GET", name, map[string]string{"Accept-Encoding": "gzip"}).Response
			if got.Header.Get("Content-Encoding") != "" || got.Header.Get("Vary") != "Accept-Encoding" {
				t.Errorf("%s: %v", name, got.Header)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	// The job that finds noise not worth compressing runs on the file pool:
	// it is marked once that job is done.
	if e = waitPacked(t, s, filepath.Join(s.root, "noise.txt"), 2); e == nil || e.packs[2].Load() != unpacked {
		t.Errorf("noise: not marked unpacked")
	}
	// Refused, nothing compresses.
	if got := get(s, "GET", "/app.js", map[string]string{"Accept-Encoding": "gzip;q=0, identity"}).Response; got.Header.Get("Content-Encoding") != "" {
		t.Errorf("refused: %v", got.Header)
	}
}

// TestCompressBounds checks a copy the cache has no room for, a file the
// cache does not keep, and a copy whose job the file pool refuses, which a
// later request makes.
func TestCompressBounds(t *testing.T) {
	js := text(20000)
	root := tree(t, map[string]string{"app.js": js, "big.js": text(50000)})
	// Room for the file, not its copy.
	s := mustLoad(t, root, `, "compress": ["gzip"], "max_cache_bytes": 20100`)
	accept := map[string]string{"Accept-Encoding": "gzip"}
	for range 3 {
		if got := get(s, "GET", "/app.js", accept).Response; got.Header.Get("Content-Encoding") != "" || string(got.Body) != js {
			t.Fatalf("no room: %v", got.Header)
		}
		time.Sleep(20 * time.Millisecond)
	}
	full := filepath.Join(s.root, "app.js")
	if e := waitPacked(t, s, full, 0); e == nil || e.packs[0].Load() != unpacked || s.cache.bytes != int64(len(js)) {
		t.Errorf("no room: kept a copy")
	}
	// Too large to keep: from disk, never compressed by the handler.
	for range 2 {
		if got := get(s, "GET", "/big.js", accept).Response; got.Header.Get("Content-Encoding") != "" {
			t.Errorf("not kept: %v", got.Header)
		}
	}

	// A pool that takes nothing refuses the job; the next request asks
	// again.
	s = mustLoad(t, root, `, "compress": ["gzip"]`)
	get(s, "GET", "/app.js", nil) // kept
	e := s.cache.current(full, fileStamp(t, full))
	pool := s.pool
	s.pool = filepool.New(0, 0, time.Second)
	if got := get(s, "GET", "/app.js", accept).Response; got.Header.Get("Content-Encoding") != "" || e.packs[0].Load() != nil {
		t.Fatalf("refused job: %v, state %p", got.Header, e.packs[0].Load())
	}
	s.pool = pool
	until(t, s, "/app.js", "gzip", accept)

	// A copy made of an entry the cache no longer holds is not kept.
	if s.cache.keepPacked(full, &entry{}, &entry{size: 1}) {
		t.Error("kept a copy of an entry not held")
	}
}

// waitPacked returns the entry of the file at full once its i-th copy is
// settled, made or not kept, which the file pool's job does in its time;
// or as it is after 5 seconds.
func waitPacked(t *testing.T, s *Static, full string, i int) *entry {
	t.Helper()
	e := s.cache.current(full, fileStamp(t, full))
	for deadline := time.Now().Add(5 * time.Second); e != nil && time.Now().Before(deadline); {
		if p := e.packs[i].Load(); p != nil && p != packing {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	return e
}

// fileStamp is the stamp of the file at full.
func fileStamp(t *testing.T, full string) filewatch.Stamp {
	t.Helper()
	info, err := os.Stat(full)
	if err != nil {
		t.Fatal(err)
	}
	return filewatch.StampOf(info)
}

func TestWindowBits(t *testing.T) {
	for _, tt := range [][4]int{{1, 10, 24, 10}, {1024, 10, 24, 10}, {1025, 10, 24, 11}, {1 << 20, 10, 24, 20}, {1 << 30, 10, 24, 24}} {
		if got := windowBits(tt[0], tt[1], tt[2]); got != tt[3] {
			t.Errorf("windowBits(%d) = %d, want %d", tt[0], got, tt[3])
		}
	}
}
