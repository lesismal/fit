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
}

// The role's metrics go with fit's.
var _ app.Metered = (*Role)(nil)
