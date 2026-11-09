package bittorrent

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	fibbt "github.com/lesismal/fib/bittorrent"

	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/observe"
)

// syncBuffer is a buffer the log writes to from several goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// TestHubBounds checks the event hub's bounds: subscribers, a full
// buffer dropping its subscriber, the events kept for a subscriber that
// comes back, and the end of every subscription when it closes.
func TestHubBounds(t *testing.T) {
	h := newHub()
	var subs []*Subscription
	for range MaxSubscribers {
		s, replay, gap, err := h.subscribe(0)
		if err != nil || replay != nil || gap {
			t.Fatalf("%v %v %v", replay, gap, err)
		}
		subs = append(subs, s)
	}
	if _, _, _, err := h.subscribe(0); !errors.Is(err, ErrTooManySubscribers) {
		t.Fatalf("one past MaxSubscribers: %v", err)
	}
	for _, s := range subs[1:] {
		s.Close()
		s.Close() // twice is harmless
	}
	slow := subs[0]
	if h.subscribers() != 1 {
		t.Fatalf("%d subscribers", h.subscribers())
	}
	// A subscriber that takes nothing is dropped once its buffer is full,
	// and emitting never waits for it.
	fast, _, _, _ := h.subscribe(0)
	var got atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range fast.Events() {
			got.Add(1)
		}
	}()
	// One that keeps up: emitting waits for it every half buffer, so that
	// a slow machine's scheduler does not make it lag.
	sent := int64(0)
	paced := func(e Event) {
		h.emit(e)
		if sent++; sent%(SubscriberBuffer/2) == 0 {
			deadline := time.Now().Add(time.Minute)
			for got.Load() < sent && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
		}
	}
	for i := range SubscriberBuffer + 1 {
		paced(Event{Type: EventAdded, Torrent: strconv.Itoa(i)})
	}
	if !slow.Lagged() || h.dropped.Load() != 1 {
		t.Fatalf("lagged %v, dropped %d", slow.Lagged(), h.dropped.Load())
	}
	n := 0
	for range slow.Events() {
		n++
	}
	if n != SubscriberBuffer {
		t.Fatalf("the dropped subscriber had %d events", n)
	}
	slow.Close() // after it was dropped
	// One that comes back after event 10 gets the rest; one after 0 none.
	s, replay, gap, err := h.subscribe(10)
	if err != nil || gap || len(replay) != SubscriberBuffer+1-10 || replay[0].ID != 11 || replay[len(replay)-1].ID != SubscriberBuffer+1 {
		t.Fatalf("replay of %d from %d, gap %v, %v", len(replay), replay[0].ID, gap, err)
	}
	s.Close()
	// Past the last event: fit started again.
	if s, replay, gap, _ = h.subscribe(1 << 40); !gap || len(replay) != 0 {
		t.Fatalf("after the last: gap %v, %d", gap, len(replay))
	}
	s.Close()
	for range EventHistory {
		paced(Event{Type: EventState})
	}
	// Event 11 is no longer kept: a gap, and the oldest kept.
	s, replay, gap, _ = h.subscribe(10)
	if !gap || len(replay) != EventHistory || replay[0].ID != h.seq-EventHistory+1 {
		t.Fatalf("gap %v, %d from %d", gap, len(replay), replay[0].ID)
	}
	h.close()
	<-done
	if got.Load() != SubscriberBuffer+1+EventHistory || fast.Lagged() {
		t.Fatalf("the subscriber that kept up had %d events, lagged %v", got.Load(), fast.Lagged())
	}
	if _, ok := <-s.Events(); ok {
		t.Fatal("a subscription is open after close")
	}
	if _, _, _, err := h.subscribe(0); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("after close: %v", err)
	}
}

// eventWaiter reads a subscription's events.
type eventWaiter struct {
	t   *testing.T
	sub *Subscription
	got []Event
}

// wait waits for an event that match accepts, failing after hang.
func (w *eventWaiter) wait(what string, match func(Event) bool) Event {
	w.t.Helper()
	timer := time.NewTimer(hang)
	defer timer.Stop()
	for {
		select {
		case e, ok := <-w.sub.Events():
			if !ok {
				w.t.Fatalf("the subscription ended waiting for %s (lagged %v): %+v", what, w.sub.Lagged(), w.got)
			}
			w.got = append(w.got, e)
			if match(e) {
				return e
			}
		case <-timer.C:
			w.t.Fatalf("timed out waiting for %s: %+v", what, w.got)
		}
	}
}

// TestRoleEvents follows a download through the role's events: added,
// state changes, completed, paused, the alternative rates, a ban, a seed
// limit and removed, each in the log too; the subscription ends when the
// role stops.
func TestRoleEvents(t *testing.T) {
	e := startEngine(t)
	src := t.TempDir()
	torrent, _ := payload(t, src, "movie.bin", 600000)
	s, _ := seeder(t, e, src, torrent, "")

	js := clientJSON(t.TempDir(), t.TempDir(), `"alt_speed": {"upload_rate": 1000}`)
	logged := &syncBuffer{}
	root, err := logs.New(nil, logged, logged)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	l := startRoleWith(t, e, member(t, js), func(r *Role) { r.log = root.Logger("bittorrent") })
	sub, _, _, err := l.Subscribe(0)
	if err != nil {
		t.Fatal(err)
	}
	w := &eventWaiter{t: t, sub: sub}
	st, err := l.Add(AddRequest{Torrent: torrent, Peers: []string{peerAddr(s)}})
	if err != nil {
		t.Fatal(err)
	}
	added := w.wait("added", func(e Event) bool { return e.Type == EventAdded })
	if added.Torrent != st.ID || added.Name != "movie.bin" || added.ID == 0 || added.Time.IsZero() || added.State == "" {
		t.Fatalf("added %+v", added)
	}
	// Completed and the state change to seeding, in either order: a fast
	// download may report one before the other.
	completed, seeding := false, false
	w.wait("completed and seeding", func(e Event) bool {
		completed = completed || (e.Type == EventCompleted && e.Torrent == st.ID)
		seeding = seeding || (e.Type == EventState && e.State == "seeding" && e.Previous != "")
		return completed && seeding
	})
	if err := l.Pause(st.ID); err != nil {
		t.Fatal(err)
	}
	w.wait("paused", func(e Event) bool { return e.Type == EventState && e.State == "paused" && e.Previous == "seeding" })
	if err := l.SetAltSpeed(AltSpeedOn); err != nil {
		t.Fatal(err)
	}
	w.wait("the alternative rates on", func(e Event) bool { return e.Type == EventAltSpeed && e.State == "on" })
	if err := l.SetAltSpeed(AltSpeedOff); err != nil {
		t.Fatal(err)
	}
	w.wait("the alternative rates off", func(e Event) bool { return e.Type == EventAltSpeed && e.State == "off" })

	// fib's events, as its event goroutine hands them over.
	l.onEvent(fibbt.Event{Type: fibbt.EventPeerBanned, Peer: netip.MustParseAddrPort("192.0.2.7:0"), Err: errors.New("bad pieces")})
	ban := w.wait("a ban", func(e Event) bool { return e.Type == EventPeerBanned })
	if ban.Peer != "192.0.2.7" || ban.Error != "bad pieces" || l.banned.Load() != 1 {
		t.Fatalf("ban %+v, %d", ban, l.banned.Load())
	}
	tor, err := l.Torrent(st.ID)
	if err != nil {
		t.Fatal(err)
	}
	l.onEvent(fibbt.Event{Type: fibbt.EventSeedLimit, Torrent: tor})
	w.wait("the seed limit", func(e Event) bool { return e.Type == EventSeedLimit && e.Torrent == st.ID })
	l.onEvent(fibbt.Event{Type: fibbt.EventError, Torrent: tor, Err: errors.New("disk on fire")})
	w.wait("a storage error", func(e Event) bool { return e.Type == EventError && e.Error == "disk on fire" })

	if err := l.Remove(st.ID, true); err != nil {
		t.Fatal(err)
	}
	rm := w.wait("removed", func(e Event) bool { return e.Type == EventRemoved })
	if rm.Torrent != st.ID || !rm.DataDeleted {
		t.Fatalf("removed %+v", rm)
	}
	for i := 1; i < len(w.got); i++ {
		if w.got[i].ID != w.got[i-1].ID+1 {
			t.Fatalf("event IDs not in order: %+v", w.got)
		}
	}
	// A subscriber coming back after the first event gets the rest.
	again, replay, gap, err := l.Subscribe(w.got[0].ID)
	if err != nil || gap || len(replay) < len(w.got)-1 || replay[0].ID != w.got[1].ID {
		t.Fatalf("replay %d, gap %v, %v", len(replay), gap, err)
	}
	for _, want := range []string{"movie.bin) added", "movie.bin) completed", "peer 192.0.2.7 banned: bad pieces",
		"reached its seed limit", "disk on fire", "movie.bin) removed with its files", "the alternative rates hold"} {
		if !strings.Contains(logged.String(), want) {
			t.Errorf("the log has no %q:\n%s", want, logged.String())
		}
	}
	l.Stop(context.Background())
	for range sub.Events() {
	}
	if sub.Lagged() {
		t.Fatal("stopping the role: lagged")
	}
	if _, ok := <-again.Events(); ok {
		t.Fatal("a subscription is open after the role stopped")
	}
	if _, _, _, err := l.Subscribe(0); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("after stop: %v", err)
	}
}

// TestPeersTrackersRecheck reads a torrent's peers and trackers, and
// checks its data again.
func TestPeersTrackersRecheck(t *testing.T) {
	e := startEngine(t)
	dl := t.TempDir()
	torrent, _ := payload(t, dl, "movie.bin", 400000)
	rc := &readCounter{}
	dataDir := t.TempDir()
	r := startRole(t, e, clientJSON(dataDir, dl, ""), rc)
	st, err := r.Add(AddRequest{Torrent: torrent})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the check of its data", func() bool {
		tor, err := r.Torrent(st.ID)
		return err == nil && tor.HaveAll()
	})
	waitFor(t, "its resume data", func() bool {
		_, err := os.Stat(filepath.Join(dataDir, resumeDir, st.ID+".resume"))
		return err == nil
	})
	if peers, err := r.Peers(st.ID); err != nil || peers == nil || len(peers) != 0 {
		t.Fatalf("peers %v, %v", peers, err)
	}
	if _, err := r.Peers(strings.Repeat("0", 40)); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}

	// A leecher connected to it: each sees the other.
	l := startRole(t, e, clientJSON(t.TempDir(), t.TempDir(), ""), nil)
	ls, err := l.Add(AddRequest{Torrent: torrent, Peers: []string{peerAddr(r)}, Paused: false,
		Limits: &TorrentLimits{DownloadRate: 32 << 10}})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the leecher's peer", func() bool {
		p, err := l.Peers(ls.ID)
		return err == nil && len(p) == 1 && p[0].Seed && p[0].Addr != "" && len(p[0].ID) == 40
	})
	if err := l.Remove(ls.ID, true); err != nil {
		t.Fatal(err)
	}

	before := rc.total()
	if err := r.Recheck(st.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, resumeDir, st.ID+".resume")); err == nil {
		// The saver may have written it again once the check ended.
		tor, _ := r.Torrent(st.ID)
		if tor == nil || !tor.HaveAll() {
			t.Fatal("the resume data is still there during the check")
		}
	}
	waitFor(t, "the check again", func() bool {
		tor, err := r.Torrent(st.ID)
		return err == nil && tor.HaveAll()
	})
	if rc.total() <= before {
		t.Fatal("a recheck read nothing")
	}
	if s, _ := r.Get(st.ID); s.QueuePosition != 0 || s.Paused {
		t.Fatalf("after a recheck %+v", s)
	}
	if err := r.Recheck(strings.Repeat("0", 40)); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}

	// A magnet link whose info has not arrived: trackers and web seeds
	// from the link, nothing to check.
	const hash = "0123456789abcdef0123456789abcdef01234567"
	m, err := r.Add(AddRequest{Paused: true, Magnet: "magnet:?xt=urn:btih:" + hash +
		"&tr=udp%3A%2F%2Ft1.example%3A6969&tr=http%3A%2F%2Ft2.example%2Fannounce&ws=http%3A%2F%2Fws.example%2Fx"})
	if err != nil {
		t.Fatal(err)
	}
	tr, err := r.Trackers(m.ID)
	if err != nil || len(tr.Trackers) != 2 || tr.Trackers[0].URL != "udp://t1.example:6969" || tr.Trackers[1].URL != "http://t2.example/announce" ||
		tr.Trackers[0].Seeders != -1 || tr.Trackers[0].LastAnnounce != nil || tr.Trackers[0].NextAnnounce != nil ||
		len(tr.WebSeeds) != 1 || tr.WebSeeds[0].URL != "http://ws.example/x" || tr.WebSeeds[0].Fails != 0 || len(tr.Sources) == 0 {
		t.Fatalf("trackers %+v, %v", tr, err)
	}
	if err := r.Recheck(m.ID); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("a magnet without info: %v", err)
	}
	if tr, err := r.Trackers(st.ID); err != nil || len(tr.Trackers) != 0 || len(tr.WebSeeds) != 0 || tr.Sources[0].Source != "tracker" || tr.Sources[0].Enabled {
		t.Fatalf("no trackers: %+v %v", tr, err)
	}
	if _, err := r.Trackers(strings.Repeat("0", 40)); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

// metric returns the value of the sample of name with labels in text, a
// scrape, or -1 when it has none.
func metric(text, name, labels string) float64 {
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name+labels) + ` (\S+)$`)
	m := re.FindStringSubmatch(text)
	if m == nil {
		return -1
	}
	v, _ := strconv.ParseFloat(m[1], 64)
	return v
}

func scrape(r *Role) string {
	w := &observe.TextWriter{}
	r.WriteMetrics(w)
	return string(w.B)
}

// TestMetrics checks the role's metrics: bytes by category that outlive the
// torrent, states, bans, the alternative rates, the event subscribers and
// the client events dropped.
func TestMetrics(t *testing.T) {
	e := startEngine(t)
	src := t.TempDir()
	torrent, _ := payload(t, src, "movie.bin", 500000)
	s, _ := seeder(t, e, src, torrent, "")
	dl := t.TempDir()
	js := clientJSON(t.TempDir(), dl, `"categories": [{"name": "films", "dir": "films"}], "alt_speed": {"upload_rate": 1}`)
	l := startRoleWith(t, e, member(t, js), nil)
	st, err := l.Add(AddRequest{Torrent: torrent, Category: "films", Peers: []string{peerAddr(s)}})
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, l, st.ID)
	waitFor(t, "the bytes counted", func() bool {
		return metric(scrape(l), "fit_bittorrent_downloaded_bytes_total", `{category="films"}`) >= 500000
	})
	waitFor(t, "the seed's bytes counted", func() bool {
		return metric(scrape(s), "fit_bittorrent_uploaded_bytes_total", `{category=""}`) >= 500000
	})
	waitFor(t, "the torrent seeding", func() bool {
		return metric(scrape(l), "fit_bittorrent_torrents", `{category="films",state="seeding"}`) == 1
	})
	text := scrape(l)
	for _, want := range []string{"fit_bittorrent_client_running 1", `fit_bittorrent_torrents{category="films",state="downloading"} 0`,
		`fit_bittorrent_hash_failures_total{category="films"} 0`, "fit_bittorrent_peers_banned_total 0",
		"fit_bittorrent_alt_speed_active 0", "fit_bittorrent_event_subscribers 0", "fit_bittorrent_events_dropped_total 0", "# TYPE fit_bittorrent_downloaded_bytes_total counter",
		`fit_bittorrent_download_rate_bytes{category="films"}`, `fit_bittorrent_peers{category="films"}`, `fit_bittorrent_seeders{category="films"}`} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in\n%s", want, text)
		}
	}
	sub, _, _, _ := l.Subscribe(0)
	if err := l.SetAltSpeed(AltSpeedOn); err != nil {
		t.Fatal(err)
	}
	l.onEvent(fibbt.Event{Type: fibbt.EventPeerBanned, Peer: netip.MustParseAddrPort("127.0.0.1:9")})
	waitFor(t, "the alternative rates and the ban", func() bool {
		text := scrape(l)
		return metric(text, "fit_bittorrent_alt_speed_active", "") == 1 && metric(text, "fit_bittorrent_peers_banned_total", "") == 1 &&
			metric(text, "fit_bittorrent_event_subscribers", "") == 1
	})
	// Client events fib dropped: counted, and published once by the next
	// look, whatever the number of drops before it.
	l.onEvent(fibbt.Event{Type: fibbt.EventDropped, Dropped: 3})
	l.onEvent(fibbt.Event{Type: fibbt.EventDropped, Dropped: 4})
	if v := metric(scrape(l), "fit_bittorrent_events_dropped_total", ""); v != 7 {
		t.Fatalf("events dropped %v", v)
	}
	var drop Event
	timeout := time.After(hang)
	for drop.Type != EventDropped {
		select {
		case drop = <-sub.Events():
		case <-timeout:
			t.Fatal("no events_dropped event")
		}
	}
	if drop.Dropped != 7 || drop.Torrent != "" {
		t.Fatalf("event %+v", drop)
	}
	sub.Close()
	// Removed, its bytes stay counted.
	if err := l.Remove(st.ID, false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the torrent gone from the states", func() bool {
		return metric(scrape(l), "fit_bittorrent_torrents", `{category="films",state="seeding"}`) == 0
	})
	if v := metric(scrape(l), "fit_bittorrent_downloaded_bytes_total", `{category="films"}`); v < 500000 {
		t.Fatalf("the bytes of a removed torrent: %v", v)
	}
	l.Stop(context.Background())
	if v := metric(scrape(l), "fit_bittorrent_downloaded_bytes_total", `{category="films"}`); v < 500000 {
		t.Fatalf("after stop: %v", v)
	}
}

// TestAddKeepsTemporary adds a torrent bt_stream added on the fly: it is
// kept as any other.
func TestAddKeepsTemporary(t *testing.T) {
	e := startEngine(t)
	dl := t.TempDir()
	torrent, _ := payload(t, dl, "movie.bin", 100000)
	r := startRole(t, e, clientJSON(t.TempDir(), dl, ""), nil)
	tmp, err := r.AddTemporary(AddRequest{Torrent: torrent}, 1)
	if err != nil || !tmp.Temporary {
		t.Fatal(tmp, err)
	}
	st, err := r.Add(AddRequest{Torrent: torrent})
	if err != nil || st.Temporary || st.ID != tmp.ID {
		t.Fatalf("%+v %v", st, err)
	}
	if removed, err := r.RemoveTemporary(st.ID); removed || err != nil {
		t.Fatalf("removed %v, %v", removed, err)
	}
	if _, err := r.Add(AddRequest{Torrent: torrent}); !errors.Is(err, ErrExists) {
		t.Fatalf("added again: %v", err)
	}
}
