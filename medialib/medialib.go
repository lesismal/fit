// Package medialib is fit's media library: the named libraries of the
// config's media_library, each a set of directories scanned into a catalog
// of items, a title each with its renditions, audio, subtitles and poster,
// for the handlers that serve them as video on demand and the channels that
// play them as live streams. See docs/decisions/032-media-library.md and
// docs/media-library.md.
//
// Nothing here blocks a fib worker. A library scans on a goroutine of its
// own, one scan at a time, and reads its files on a bounded file pool of its
// own: probing each media file with fib's media/mp4 (a few kilobytes read,
// whatever its length), reading sidecars and subtitles, and the sample
// tables and bytes of the files served. A method that touches the disk
// takes a callback, called from the pool. The catalog, the sample tables
// kept and the work queued are each bounded.
//
// Libraries are found by name per engine (the engine a module's Context and
// an app.Host's Upstream name), as media_auth's policies are: a *Library
// stays the same across reloads and changes in place, so that whoever holds
// one sees what the config now says. The role media_library moves them with
// every config applied.
package medialib

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/lesismal/fib"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/observe"
)

// ID is the role's, and its member's path in the config.
const ID = "media_library"

func init() {
	app.Register(app.Info{
		ID: ID,
		Member: func(c *config.Config) any {
			if len(c.MediaLibrary) == 0 {
				return nil
			}
			return c.MediaLibrary
		},
		New: func(h *app.Host) (app.Role, error) { return &role{key: h.Upstream, log: h.Log}, nil },
	})
}

// role moves the libraries of its engine to each config applied.
type role struct {
	key *fib.Engine
	log *logs.Logger
}

func (r *role) Apply(member any) error {
	list, ok := member.([]config.MediaLibrary)
	if !ok {
		return fmt.Errorf("media_library: a member of type %T", member)
	}
	return Apply(r.key, list, r.log)
}

func (r *role) Stop(context.Context) { Remove(r.key) }

// WriteMetrics writes the metrics of the engine's libraries.
func (r *role) WriteMetrics(w observe.MetricWriter) { WriteMetrics(w, Libraries(r.key)) }

var (
	regMu    sync.Mutex
	registry = map[*fib.Engine]map[string]*Library{}
)

// Lookup returns the library named name of engine, or nil when the running
// config has none of that name.
func Lookup(engine *fib.Engine, name string) *Library {
	regMu.Lock()
	defer regMu.Unlock()
	return registry[engine][name]
}

// Libraries returns the libraries of engine, sorted by name.
func Libraries(engine *fib.Engine) []*Library {
	regMu.Lock()
	defer regMu.Unlock()
	out := make([]*Library, 0, len(registry[engine]))
	for _, l := range registry[engine] {
		out = append(out, l)
	}
	slices.SortFunc(out, func(a, b *Library) int { return strings.Compare(a.name, b.name) })
	return out
}

// Apply moves engine's libraries to list: each is changed in place, or made
// and scanned, and those list leaves out are removed, so that whoever still
// holds one is refused with ErrRemoved. A library whose roots, patterns or
// bounds change is scanned again. log, which may be nil, hears what scans
// find wrong. An error, a library that does not compile, changes nothing.
func Apply(engine *fib.Engine, list []config.MediaLibrary, log *logs.Logger) error {
	regMu.Lock()
	defer regMu.Unlock()
	sets := make(map[string]*settings, len(list))
	var errs []error
	for i := range list {
		c := &list[i]
		if sets[c.Name] != nil {
			errs = append(errs, fmt.Errorf("%q is listed twice", c.Name))
			continue
		}
		s, err := compile(c)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", c.Name, err))
			continue
		}
		sets[c.Name] = s
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	libs := registry[engine]
	for name, l := range libs {
		if sets[name] == nil {
			l.remove()
		}
	}
	next := make(map[string]*Library, len(sets))
	for name, s := range sets {
		if l := libs[name]; l != nil {
			l.apply(s)
			next[name] = l
			continue
		}
		next[name] = newLibrary(name, s, log)
	}
	if len(next) == 0 {
		delete(registry, engine)
	} else {
		registry[engine] = next
	}
	return nil
}

// Remove removes every library of engine.
func Remove(engine *fib.Engine) {
	regMu.Lock()
	defer regMu.Unlock()
	for _, l := range registry[engine] {
		l.remove()
	}
	delete(registry, engine)
}

// WriteMetrics writes the metrics of libs.
func WriteMetrics(w observe.MetricWriter, libs []*Library) {
	w.Family("fit_media_library_items", "gauge", "Items of a library's catalog, by whether they can be served (ok) or are listed with an error.")
	for _, l := range libs {
		cat := l.Catalog()
		errs := 0
		for _, it := range cat.Items {
			if it.Error != "" {
				errs++
			}
		}
		w.Sample("fit_media_library_items", float64(len(cat.Items)-errs), "library", l.name, "state", "ok")
		w.Sample("fit_media_library_items", float64(errs), "library", l.name, "state", "error")
	}
	w.Family("fit_media_library_catalog_bytes", "gauge", "The memory a library's catalog takes, as reckoned for max_catalog_bytes.")
	for _, l := range libs {
		w.Sample("fit_media_library_catalog_bytes", float64(l.Catalog().Bytes), "library", l.name)
	}
	w.Family("fit_media_library_scans_total", "counter", "Scans of a library ended.")
	for _, l := range libs {
		w.Sample("fit_media_library_scans_total", float64(l.scans.Load()), "library", l.name)
	}
	w.Family("fit_media_library_probes_total", "counter", "Media files a library's scans probed, by whether the probe found an error.")
	for _, l := range libs {
		errs := l.probeErrors.Load()
		w.Sample("fit_media_library_probes_total", float64(l.probed.Load()-errs), "library", l.name, "result", "ok")
		w.Sample("fit_media_library_probes_total", float64(errs), "library", l.name, "result", "error")
	}
	w.Family("fit_media_library_table_bytes", "gauge", "The memory of the sample tables a library keeps, bounded by max_table_bytes.")
	for _, l := range libs {
		w.Sample("fit_media_library_table_bytes", float64(l.Tables().Bytes), "library", l.name)
	}
	w.Family("fit_media_library_table_lookups_total", "counter", "Sample tables a library was asked for, by whether it kept the table (hit) or not (miss): read from the file, a read under way waited for, or refused.")
	for _, l := range libs {
		ts := l.Tables()
		w.Sample("fit_media_library_table_lookups_total", float64(ts.Hits), "library", l.name, "result", "hit")
		w.Sample("fit_media_library_table_lookups_total", float64(ts.Misses), "library", l.name, "result", "miss")
	}
	w.Family("fit_media_library_pool_jobs", "gauge", "Jobs of a library's file pool, by whether they wait (queued) or run (busy).")
	for _, l := range libs {
		st := l.Pool().Stats()
		w.Sample("fit_media_library_pool_jobs", float64(st.Queued), "library", l.name, "state", "queued")
		w.Sample("fit_media_library_pool_jobs", float64(st.Busy), "library", l.name, "state", "busy")
	}
	w.Family("fit_media_library_pool_refused_total", "counter", "Jobs a library's file pool refused, its queue full (refused), or gave up after file_timeout in its queue (expired), each a request answered 503 or a probe tried again.")
	for _, l := range libs {
		st := l.Pool().Stats()
		w.Sample("fit_media_library_pool_refused_total", float64(st.Refused), "library", l.name, "reason", "refused")
		w.Sample("fit_media_library_pool_refused_total", float64(st.Expired), "library", l.name, "reason", "expired")
	}
}
