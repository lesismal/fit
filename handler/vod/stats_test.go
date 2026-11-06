package vod

import (
	"strings"
	"testing"

	"github.com/lesismal/fit/observe"
)

// TestStats counts what the handlers serve: requests, the bytes of the
// bodies handed over, blobs, ranges of files and short answers alike, and
// the cache's hits and misses; Stats and WriteMetrics give them by library.
func TestStats(t *testing.T) {
	root := copyLib(t)
	f := startVOD(t, root, "", `{"library": "films"}`, "")
	if got := Stats(newEngineKey()); len(got) != 0 {
		t.Fatalf("stats of an engine without vod: %+v", got)
	}
	var none observe.TextWriter
	WriteMetrics(&none, newEngineKey())
	if len(none.B) != 0 {
		t.Fatalf("metrics of an engine without vod: %s", none.B)
	}

	master := f.mustGet(t, "/vod/films/films/movie/index.m3u8", 200)
	media := f.mustGet(t, "/vod/films/films/movie/v0.fmp4.m3u8", 200)
	seg := f.mustGet(t, "/vod/films/films/movie/v0.0.m4s", 200)
	f.mustGet(t, "/vod/films/films/movie/v0.0.m4s", 200)
	part := f.mustGet(t, "/vod/films/films/movie/v0.0.m4s", 206, "Range", "bytes=0-9")
	whole := f.mustGet(t, "/vod/films/films/movie/v0.mp4", 200)
	f.mustGet(t, "/vod/films/films/movie/v0.mp4", 416, "Range", "bytes=999999999-")
	f.mustGet(t, "/vod/films/films/none/index.m3u8", 404)
	if len(whole) == 0 {
		t.Fatal("the file is empty")
	}

	all := Stats(f.env.Engine)
	if len(all) != 1 {
		t.Fatalf("stats: %+v", all)
	}
	s := all[0]
	// The 416's short body counts as what ServeContent wrote of it.
	short := uint64(len("invalid range: failed to overlap\n"))
	want := uint64(len(master)+len(media)+2*len(seg)+len(part)+len(whole)) + short
	if s.Library != "films" || s.Requests != 8 || s.Bytes != want {
		t.Errorf("stats: %+v, want 8 requests and %d bytes", s, want)
	}
	if s.Cache.Hits < 2 || s.Cache.Misses < 2 || s.Cache.Entries == 0 || s.Cache.Bytes == 0 || s.Cache.Max != DefaultMaxCacheBytes {
		t.Errorf("the cache: %+v", s.Cache)
	}

	var w observe.TextWriter
	WriteMetrics(&w, f.env.Engine)
	for _, line := range []string{
		`fit_vod_requests_total{library="films"} 8`,
		`fit_vod_sent_bytes_total{library="films"} `,
		`fit_vod_cache_lookups_total{library="films",result="hit"} `,
		`fit_vod_cache_lookups_total{library="films",result="miss"} `,
		`fit_vod_cache_bytes{library="films"} `,
	} {
		if !strings.Contains(string(w.B), line) {
			t.Errorf("metrics lack %s:\n%s", line, w.B)
		}
	}
}
