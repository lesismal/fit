package bittorrent

import (
	"slices"

	fibbt "github.com/lesismal/fib/bittorrent"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/observe"
)

// The role's metrics, as the Admin API's /metrics and OpenTelemetry's export
// carry them. The manager goroutine takes them every second, under r.mu, as
// it looks at the torrents, and keeps a snapshot that a scrape reads without
// a lock of the role's: a scrape never waits for an Apply or the API.
//
// fib counts a torrent's bytes and failed pieces from when its client added
// it, and forgets them when it is removed; the role adds what each torrent
// counted since the last look to totals of its category, so that the
// counters never go back while fit runs, through removals, rechecks and new
// clients alike.

// States are the states a torrent's status reports, as
// fit_bittorrent_torrents writes each.
var States = []string{"metadata", "checking", "downloading", "seeding", "queued", "paused", "stopped", "removed", "error"}

// catTotals are a category's counters.
type catTotals struct {
	down, up, hashFails int64
}

// catMetrics is a category's line of a snapshot.
type catMetrics struct {
	name string
	catTotals
	downRate, upRate float64
	peers, seeders   int
	states           [9]int // by States
}

// metricsSnap is what a scrape writes.
type metricsSnap struct {
	running   bool
	altActive bool
	cats      []catMetrics
}

// accountLocked adds what rec's torrent counted since the last look to its
// category's totals. r.mu is held.
func (r *Role) accountLocked(rec *record, st *fibbt.Stats) {
	if rec.t != rec.acctT {
		// Another torrent of fib's (a new client, a recheck): its counts
		// start at zero.
		rec.acctT, rec.acctDown, rec.acctUp, rec.acctFails = rec.t, 0, 0, 0
	}
	if rec.t == nil {
		return
	}
	var s fibbt.Stats
	if st != nil {
		s = *st
	} else {
		s = rec.t.Stats()
	}
	t := r.acct[rec.category]
	if t == nil {
		t = &catTotals{}
		r.acct[rec.category] = t
	}
	t.down += max(s.Downloaded-rec.acctDown, 0)
	t.up += max(s.Uploaded-rec.acctUp, 0)
	t.hashFails += max(int64(s.HashFails)-rec.acctFails, 0)
	rec.acctDown, rec.acctUp, rec.acctFails = s.Downloaded, s.Uploaded, int64(s.HashFails)
}

// accountAllLocked accounts every torrent, as their client closes. r.mu is
// held.
func (r *Role) accountAllLocked() {
	for _, rec := range r.recs {
		r.accountLocked(rec, nil)
	}
}

// metricsLocked accounts every torrent, keeps a snapshot of the metrics
// for scrapes, and publishes the state changes since the last look. r.mu
// is held.
func (r *Role) metricsLocked() {
	r.noteStatesLocked()
	snap := &metricsSnap{running: r.client != nil}
	if r.cfg != nil && r.client != nil {
		snap.altActive = r.altActiveLocked()
	}
	cats := map[string]*catMetrics{}
	cat := func(name string) *catMetrics {
		c := cats[name]
		if c == nil {
			c = &catMetrics{name: name}
			cats[name] = c
		}
		return c
	}
	for _, rec := range r.recs {
		c := cat(rec.category)
		if rec.t != nil {
			st := rec.t.Stats()
			r.accountLocked(rec, &st)
			c.downRate += st.DownloadRate
			c.upRate += st.UploadRate
			c.peers += st.Peers
			c.seeders += st.Seeders
		} else {
			r.accountLocked(rec, nil)
		}
		if i := slices.Index(States, rec.lastState); i >= 0 {
			c.states[i]++
		}
	}
	for name, t := range r.acct {
		cat(name).catTotals = *t
	}
	snap.cats = make([]catMetrics, 0, len(cats))
	for _, c := range cats {
		snap.cats = append(snap.cats, *c)
	}
	slices.SortFunc(snap.cats, func(a, b catMetrics) int {
		switch {
		case a.name < b.name:
			return -1
		case a.name > b.name:
			return 1
		}
		return 0
	})
	r.snap.Store(snap)
}

// WriteMetrics writes the role's metrics to w: per category ("" for the
// torrents of none), the payload bytes downloaded and uploaded, the rates,
// the peers connected, the torrents in each state and the pieces that
// failed their check; for the client, whether it runs, the peers banned,
// whether the alternative rates hold, the client events dropped and the
// event stream's subscribers;
// per publish entry, its torrents. It takes no lock of the role's.
func (r *Role) WriteMetrics(w observe.MetricWriter) {
	snap := r.snap.Load()
	if snap == nil {
		return
	}
	run := 0.0
	if snap.running {
		run = 1
	}
	w.Family("fit_bittorrent_client_running", "gauge", "Whether the BitTorrent client runs (1) or not (0), as when its port could not be taken.")
	w.Sample("fit_bittorrent_client_running", run)
	cat := func(name, typ, help string, v func(*catMetrics) float64) {
		w.Family(name, typ, help)
		for i := range snap.cats {
			w.Sample(name, v(&snap.cats[i]), "category", snap.cats[i].name)
		}
	}
	cat("fit_bittorrent_downloaded_bytes_total", "counter", "Payload bytes the torrents of a category downloaded since fit started, removed torrents' included.",
		func(c *catMetrics) float64 { return float64(c.down) })
	cat("fit_bittorrent_uploaded_bytes_total", "counter", "Payload bytes the torrents of a category uploaded since fit started, removed torrents' included.",
		func(c *catMetrics) float64 { return float64(c.up) })
	cat("fit_bittorrent_download_rate_bytes", "gauge", "Bytes per second the torrents of a category download, smoothed over seconds.",
		func(c *catMetrics) float64 { return c.downRate })
	cat("fit_bittorrent_upload_rate_bytes", "gauge", "Bytes per second the torrents of a category upload, smoothed over seconds.",
		func(c *catMetrics) float64 { return c.upRate })
	cat("fit_bittorrent_peers", "gauge", "Peer connections of the torrents of a category.",
		func(c *catMetrics) float64 { return float64(c.peers) })
	cat("fit_bittorrent_seeders", "gauge", "Peer connections of the torrents of a category to peers that have every piece.",
		func(c *catMetrics) float64 { return float64(c.seeders) })
	cat("fit_bittorrent_hash_failures_total", "counter", "Pieces of the torrents of a category that failed their check and were downloaded again, since fit started.",
		func(c *catMetrics) float64 { return float64(c.hashFails) })
	w.Family("fit_bittorrent_torrents", "gauge", "Torrents of a category in each state, as their status reports it.")
	for _, c := range snap.cats {
		for i, s := range States {
			w.Sample("fit_bittorrent_torrents", float64(c.states[i]), "category", c.name, "state", s)
		}
	}
	w.Family("fit_bittorrent_peers_banned_total", "counter", "Peers the client banned since fit started, as for sending pieces that failed their check.")
	w.Sample("fit_bittorrent_peers_banned_total", float64(r.banned.Load()))
	alt := 0.0
	if snap.altActive {
		alt = 1
	}
	w.Family("fit_bittorrent_alt_speed_active", "gauge", "Whether the alternative rates hold (1) or not (0).")
	w.Sample("fit_bittorrent_alt_speed_active", alt)
	w.Family("fit_bittorrent_event_subscribers", "gauge", "Subscribers to the role's events, as the Admin API's /bittorrent/events streams.")
	w.Sample("fit_bittorrent_event_subscribers", float64(r.events.subscribers()))
	w.Family("fit_bittorrent_events_dropped_total", "counter", "Piece and peer events the client dropped since fit started, its queue for the role being full.")
	w.Sample("fit_bittorrent_events_dropped_total", float64(r.clientDropped.Load()))
	w.Family("fit_bittorrent_event_subscribers_dropped_total", "counter", "Subscribers to the role's events dropped for not taking them fast enough.")
	w.Sample("fit_bittorrent_event_subscribers_dropped_total", float64(r.events.dropped.Load()))
	r.rss.writeMetrics(w)
	pubs := r.Publishing(nil)
	if len(pubs) > 0 {
		w.Family("fit_bittorrent_published_torrents", "gauge", "Torrents a publish entry seeds, or has built and cannot seed.")
		for _, p := range pubs {
			w.Sample("fit_bittorrent_published_torrents", float64(len(p.Torrents)), "entry", p.Name)
		}
	}
}

// The role's metrics go with fit's.
var _ app.Metered = (*Role)(nil)
