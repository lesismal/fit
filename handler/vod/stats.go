package vod

import (
	"slices"
	"strings"

	"github.com/lesismal/fib"
	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/observe"
)

// What the vod handlers served, for the Admin API's /media/stats and the
// metrics: per store, the requests its handlers took, the bytes of the
// bodies they handed to the connections (blobs whole or in part, and the
// ranges of files handed to SendFile), and its cache's lookups, a hit for
// what was found built and a miss for what was built or waited for. The
// counters are atomic, a few adds a request; a store's live as long as its
// params do, so they start again from 0 with a reload that changes them.

// sendBlob serves b, counting the bytes it hands over.
func (v *VOD) sendBlob(c *fibhttp.Context, b *blob) {
	v.st.sent.Add(uint64(serveBlob(c, b)))
}

// LibraryStats is what the vod handlers of one library served, their
// stores summed.
type LibraryStats struct {
	Library  string `json:"library"`
	Requests uint64 `json:"requests"`
	// Bytes is the bytes of the bodies handed to the connections.
	Bytes uint64     `json:"bytes"`
	Cache CacheCount `json:"cache"`
}

// CacheCount is what the caches of a library's vod handlers hold, and how
// often a lookup found what it asked for built (Hits) or not (Misses).
type CacheCount struct {
	Entries int    `json:"entries"`
	Bytes   int64  `json:"bytes"`
	Max     int64  `json:"max"`
	Hits    uint64 `json:"hits"`
	Misses  uint64 `json:"misses"`
}

// Stats returns what the vod handlers of engine served, one entry a
// library, sorted by its name; none when no vod route runs.
func Stats(engine *fib.Engine) []LibraryStats {
	storesMu.Lock()
	list := make([]*store, 0, len(stores))
	for k, st := range stores {
		if k.engine == engine {
			list = append(list, st)
		}
	}
	storesMu.Unlock()
	by := map[string]*LibraryStats{}
	for _, st := range list {
		s := by[st.key.library]
		if s == nil {
			s = &LibraryStats{Library: st.key.library}
			by[st.key.library] = s
		}
		cs := st.cache.stats()
		s.Requests += st.requests.Load()
		s.Bytes += st.sent.Load()
		s.Cache.Entries += cs.Entries
		s.Cache.Bytes += cs.Bytes
		s.Cache.Max += cs.Max
		s.Cache.Hits += st.cache.hits.Load()
		s.Cache.Misses += st.cache.misses.Load()
	}
	out := make([]LibraryStats, 0, len(by))
	for _, s := range by {
		out = append(out, *s)
	}
	slices.SortFunc(out, func(a, b LibraryStats) int { return strings.Compare(a.Library, b.Library) })
	return out
}

// WriteMetrics writes the metrics of engine's vod handlers, by library.
func WriteMetrics(w observe.MetricWriter, engine *fib.Engine) {
	all := Stats(engine)
	if len(all) == 0 {
		return
	}
	w.Family("fit_vod_requests_total", "counter", "Requests the vod handlers of a library took.")
	for _, s := range all {
		w.Sample("fit_vod_requests_total", float64(s.Requests), "library", s.Library)
	}
	w.Family("fit_vod_sent_bytes_total", "counter", "Bytes of response bodies the vod handlers of a library handed to the connections: playlists, segments, subtitles, and the ranges of files sent.")
	for _, s := range all {
		w.Sample("fit_vod_sent_bytes_total", float64(s.Bytes), "library", s.Library)
	}
	w.Family("fit_vod_cache_lookups_total", "counter", "Lookups of the vod handlers' caches of a library, by whether what was asked for was built (hit) or was built or waited for (miss).")
	for _, s := range all {
		w.Sample("fit_vod_cache_lookups_total", float64(s.Cache.Hits), "library", s.Library, "result", "hit")
		w.Sample("fit_vod_cache_lookups_total", float64(s.Cache.Misses), "library", s.Library, "result", "miss")
	}
	w.Family("fit_vod_cache_bytes", "gauge", "The memory the vod handlers' caches of a library hold, as reckoned for max_cache_bytes.")
	for _, s := range all {
		w.Sample("fit_vod_cache_bytes", float64(s.Cache.Bytes), "library", s.Library)
	}
}
