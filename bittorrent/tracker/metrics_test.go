package tracker

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fib/bittorrent/metainfo"
	fibtracker "github.com/lesismal/fib/bittorrent/tracker"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/observe"
)

// TestMetrics checks the tracker's metrics after announces, a scrape and
// a refusal, and that a role that does not run writes none.
func TestMetrics(t *testing.T) {
	e := startEngine(t)
	a, c := hashOf("metric a"), hashOf("metric c")
	r := startRole(t, e, load(t, `{"listen": "127.0.0.1:0", "allow": ["`+a.String()+`"], "max_peers": 1000}`, ""))
	hu := serveHTTP(t, e) + "/announce"
	cl := newClient(t, e, time.Second, 8)
	mustAnnounce(t, cl, hu, a, 1, 100, fibtracker.EventStarted)
	mustAnnounce(t, cl, udpURL(r, "/announce"), a, 2, 0, fibtracker.EventStarted)
	_, err := announce(t, cl, hu, c, 3, 100, fibtracker.EventStarted)
	refusedWith(t, err, "unregistered torrent")
	if _, err := cl.Scrape(ctx(t), hu, []metainfo.InfoHash{a}); err != nil {
		t.Fatal(err)
	}
	var m app.Metered = r
	w := &observe.TextWriter{}
	m.WriteMetrics(w)
	text := string(w.B)
	for _, want := range []string{
		"fit_bittorrent_tracker_torrents 1\n", "fit_bittorrent_tracker_peers 2\n", "fit_bittorrent_tracker_max_peers 1000\n",
		"fit_bittorrent_tracker_allowed 1\n", "fit_bittorrent_tracker_announces_total 2\n", "fit_bittorrent_tracker_scrapes_total 1\n",
		`fit_bittorrent_tracker_refused_total{reason="unregistered"} 1` + "\n", `fit_bittorrent_tracker_refused_total{reason="passkey"} 0` + "\n",
		"# TYPE fit_bittorrent_tracker_announces_total counter\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in\n%s", want, text)
		}
	}
	r.Stop(context.Background())
	w = &observe.TextWriter{}
	r.WriteMetrics(w)
	if len(w.B) != 0 {
		t.Fatalf("a stopped tracker wrote %s", w.B)
	}
}
