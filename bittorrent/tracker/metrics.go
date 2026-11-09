package tracker

import (
	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/observe"
)

// WriteMetrics writes the tracker's metrics to w: the torrents and peers its
// store keeps and their bounds, the announces and scrapes served, the
// requests refused by reason, and the allow-list's size. It takes no lock a
// request waits for, as Stats does; nothing while the role does not run.
func (r *Role) WriteMetrics(w observe.MetricWriter) {
	st, err := r.Stats()
	if err != nil {
		return
	}
	gauge := func(name, help string, v float64) {
		w.Family(name, "gauge", help)
		w.Sample(name, v)
	}
	gauge("fit_bittorrent_tracker_torrents", "Torrents the tracker's store keeps, with the peers it has not looked at since they expired.", float64(st.Torrents))
	gauge("fit_bittorrent_tracker_peers", "Peers the tracker's store keeps, expired ones it has not looked at yet among them.", float64(st.Peers))
	gauge("fit_bittorrent_tracker_max_torrents", "The tracker's max_torrents.", float64(st.MaxTorrents))
	gauge("fit_bittorrent_tracker_max_peers", "The tracker's max_peers.", float64(st.MaxPeers))
	gauge("fit_bittorrent_tracker_allowed", "Info hashes the tracker's allow-list holds; 0 without one.", float64(st.Allowed))
	w.Family("fit_bittorrent_tracker_announces_total", "counter", "Announces the tracker served since the role started, over HTTP and UDP.")
	w.Sample("fit_bittorrent_tracker_announces_total", float64(st.Announces))
	w.Family("fit_bittorrent_tracker_scrapes_total", "counter", "Scrapes the tracker served since the role started, over HTTP and UDP.")
	w.Sample("fit_bittorrent_tracker_scrapes_total", float64(st.Scrapes))
	w.Family("fit_bittorrent_tracker_refused_total", "counter", "Requests the tracker refused since the role started, by reason: unregistered (an info hash the allow-list lacks, counted per hash), passkey and access.")
	for _, name := range refusalNames {
		w.Sample("fit_bittorrent_tracker_refused_total", float64(st.Refused[name]), "reason", name)
	}
	if ss := st.Store; ss != nil {
		up := 0.0
		if ss.Reachable {
			up = 1
		}
		gauge("fit_bittorrent_tracker_store_reachable", "1 while the tracker's shared store answers, 0 while the tracker answers from memory alone.", up)
		gauge("fit_bittorrent_tracker_store_queued", "Announces waiting to be written to the tracker's shared store.", float64(ss.Queued))
		gauge("fit_bittorrent_tracker_store_cached", "Torrents of which the tracker keeps a sample of the shared store's peers.", float64(ss.Cached))
		counter := func(name, help string, v uint64) {
			w.Family(name, "counter", help)
			w.Sample(name, float64(v))
		}
		counter("fit_bittorrent_tracker_store_written_total", "Announces written to the tracker's shared store.", ss.Written)
		counter("fit_bittorrent_tracker_store_dropped_total", "Announces not written to the tracker's shared store: past its queue, or expired while it could not be reached.", ss.Dropped)
		counter("fit_bittorrent_tracker_store_refused_total", "Announces the shared store's bounds refused: answered, but not shared.", ss.Refused)
		counter("fit_bittorrent_tracker_store_failed_total", "Announces the shared store answered with an error.", ss.Failed)
	}
}

// The role's metrics go with fit's.
var _ app.Metered = (*Role)(nil)
