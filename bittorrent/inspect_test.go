package bittorrent

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fib"
	"github.com/lesismal/fib/bittorrent/bencode"
	"github.com/lesismal/fib/bittorrent/metainfo"
	"github.com/lesismal/fib/bittorrent/tracker"
	fibhttp "github.com/lesismal/fib/http"
)

// A recheck runs in place (I210, fib A88): the seed is checking, keeps its
// leecher connected, the same torrent of fib's and its upload counter, and
// seeds again with its resume data written anew.
func TestRecheckKeepsPeers(t *testing.T) {
	e := startEngine(t)
	dl := t.TempDir()
	torrent, data := payload(t, dl, "movie.bin", 1<<20)
	rc := &readCounter{}
	dataDir := t.TempDir()
	r := startRole(t, e, clientJSON(dataDir, dl, ""), rc)
	st, err := r.Add(AddRequest{Torrent: torrent})
	if err != nil {
		t.Fatal(err)
	}
	resume := filepath.Join(dataDir, resumeDir, st.ID+".resume")
	waitFor(t, "the seed's resume data", func() bool {
		tor, err := r.Torrent(st.ID)
		_, ferr := os.Stat(resume)
		return err == nil && tor.HaveAll() && ferr == nil
	})

	ldl := t.TempDir()
	l := startRole(t, e, clientJSON(t.TempDir(), ldl, ""), nil)
	ls, err := l.Add(AddRequest{Torrent: torrent, Peers: []string{peerAddr(r)}, Limits: &TorrentLimits{DownloadRate: 64 << 10}})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the leecher downloading", func() bool {
		p, err := r.Peers(st.ID)
		s, _ := l.Get(ls.ID)
		return err == nil && len(p) == 1 && s.PiecesHave > 0
	})
	tor, _ := r.Torrent(st.ID)
	peers, _ := r.Peers(st.ID)
	before, _ := r.Get(st.ID)
	reads := rc.total()

	rc.delay.Store(int64(20 * time.Millisecond))
	if err := r.Recheck(st.ID); err != nil {
		t.Fatal(err)
	}
	if s, _ := r.Get(st.ID); s.State != "checking" {
		t.Fatalf("state %q during the check", s.State)
	}
	if _, err := os.Stat(resume); err == nil {
		t.Fatal("the resume data from before the check is still there")
	}
	rc.delay.Store(0)
	waitFor(t, "seeding after the check", func() bool {
		s, err := r.Get(st.ID)
		return err == nil && s.State == "seeding" && s.PiecesHave == s.Pieces
	})
	if rc.total() <= reads {
		t.Fatal("the check read nothing")
	}
	if now, _ := r.Torrent(st.ID); now != tor {
		t.Fatal("the torrent was added again")
	}
	after, _ := r.Get(st.ID)
	if after.Uploaded < before.Uploaded || after.Uploaded == 0 {
		t.Fatalf("uploaded %d, was %d", after.Uploaded, before.Uploaded)
	}
	if p, _ := r.Peers(st.ID); len(p) != 1 || p[0].Addr != peers[0].Addr || p[0].ID != peers[0].ID {
		t.Fatalf("peers %+v, were %+v", p, peers)
	}
	if fs := tor.Stats(); fs.HashFails != 0 {
		t.Fatalf("hash fails %d", fs.HashFails)
	}
	waitFor(t, "the resume data again", func() bool { _, err := os.Stat(resume); return err == nil })

	// The leecher finishes from it.
	if err := l.SetTorrentLimits(ls.ID, TorrentLimits{}); err != nil {
		t.Fatal(err)
	}
	waitDone(t, l, ls.ID)
	sameFile(t, filepath.Join(ldl, "movie.bin"), data)
}

// A recheck of a file changed behind the client's back loses the pieces
// that fail, which are downloaded again from the swarm.
func TestRecheckCorrupt(t *testing.T) {
	e := startEngine(t)
	sdir := t.TempDir()
	torrent, data := payload(t, sdir, "movie.bin", 400000)
	s, _ := seeder(t, e, sdir, torrent, "")
	ldl := t.TempDir()
	l := startRole(t, e, clientJSON(t.TempDir(), ldl, ""), nil)
	ls, err := l.Add(AddRequest{Torrent: torrent, Peers: []string{peerAddr(s)}})
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, l, ls.ID)
	p := filepath.Join(ldl, "movie.bin")
	f, err := os.OpenFile(p, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte{^data[100000]}, 100000); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte{^data[300000]}, 300000); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	tor, _ := l.Torrent(ls.ID)
	if err := l.Recheck(ls.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the pieces lost and downloaded again", func() bool {
		st, err := l.Get(ls.ID)
		return err == nil && tor.Stats().HashFails >= 2 && st.State == "seeding" && st.PiecesHave == st.Pieces
	})
	sameFile(t, p, data)
	if now, _ := l.Torrent(ls.ID); now != tor {
		t.Fatal("the torrent was added again")
	}
}

// runTracker runs fib's tracker of cfg on its own engine, and returns its
// HTTP announce URL.
func runTracker(t *testing.T, cfg tracker.ServerConfig) string {
	t.Helper()
	srv := tracker.NewServer(cfg)
	ecfg := fib.DefaultConfig()
	ecfg.LogSettings = false
	engine, err := fib.New(ecfg, fib.Binding{Network: "tcp4", Addr: "127.0.0.1:0", Handler: fibhttp.NewHandler(srv)})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- engine.Run() }()
	t.Cleanup(func() { engine.Stop(); <-done; _ = engine.Close() })
	ta, _ := engine.BindingAddrs(0)
	return "http://" + ta[0].String() + "/announce"
}

// Trackers reports each tracker's announces (I210, fib A87): one that
// answers with its interval, counts, scrape and next announce under both
// hashes of a hybrid, one that refuses with its reason, one nobody answers
// at; the torrent's sources; a web seed that fails with its error.
func TestTrackersState(t *testing.T) {
	okURL := runTracker(t, tracker.ServerConfig{Interval: time.Hour})
	refuseURL := runTracker(t, tracker.ServerConfig{AllowInfoHash: func(metainfo.InfoHash) bool { return false }})
	deadURL := "http://127.0.0.1:1/announce"

	e := startEngine(t)
	dl := t.TempDir()
	if err := os.WriteFile(filepath.Join(dl, "movie.bin"), make([]byte, 100000), 0o644); err != nil {
		t.Fatal(err)
	}
	b := metainfo.Builder{Version: metainfo.Hybrid, PieceLength: 16 << 10, Trackers: [][]string{{refuseURL}, {deadURL}, {okURL}}}
	mi, err := b.BuildPath(filepath.Join(dl, "movie.bin"))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := bencode.Marshal(mi)
	r := startRole(t, e, clientJSON(t.TempDir(), dl, ""), nil)
	st, err := r.Add(AddRequest{Torrent: raw})
	if err != nil {
		t.Fatal(err)
	}
	find := func(list []TrackerStatus, u string) TrackerStatus {
		for _, ts := range list {
			if ts.URL == u {
				return ts
			}
		}
		t.Fatalf("no tracker %s in %+v", u, list)
		return TrackerStatus{}
	}
	// An announce made while the torrent was still checking its data
	// reports it a leecher; fib announces again once the check ends (see
	// fib's announceLoop), so the answering tracker is read once that
	// announce is done and counts the torrent a seeder, under both hashes.
	var tr Trackers
	waitFor(t, "every tracker tried, the answering one scraped and told of the seed", func() bool {
		var err error
		tr, err = r.Trackers(st.ID)
		if err != nil || len(tr.Trackers) != 3 {
			return false
		}
		ok, dead, refused := find(tr.Trackers, okURL), find(tr.Trackers, deadURL), find(tr.Trackers, refuseURL)
		return ok.Scraped != nil && ok.V2 != nil && ok.V2.Scraped != nil && !dead.Updating && dead.Fails > 0 && !refused.Updating && refused.Fails > 0 &&
			!ok.Updating && ok.Seeders == 1 && !ok.V2.Updating && ok.V2.Seeders == 1
	})
	ok, dead, refused := find(tr.Trackers, okURL), find(tr.Trackers, deadURL), find(tr.Trackers, refuseURL)
	if refused.Tier != 0 || dead.Tier != 1 || ok.Tier != 2 {
		t.Fatalf("tiers %d %d %d", refused.Tier, dead.Tier, ok.Tier)
	}
	if !refused.Refused || !strings.Contains(refused.Error, "unregistered") || refused.LastAnnounce == nil || refused.Seeders != -1 || refused.Downloaded != -1 {
		t.Fatalf("the refusing tracker %+v", refused.Announces)
	}
	if dead.Refused || dead.Error == "" || dead.Peers != 0 || dead.Seeders != -1 {
		t.Fatalf("the unreachable tracker %+v", dead.Announces)
	}
	for _, a := range []Announces{ok.Announces, *ok.V2} {
		if a.Error != "" || a.Fails != 0 || a.LastAnnounce == nil || a.Interval != 3600 || a.Seeders != 1 || a.Leechers != 0 || a.Downloaded != 0 {
			t.Fatalf("the answering tracker %+v", a)
		}
		if a.NextAnnounce == nil || !a.NextAnnounce.After(time.Now().Add(50*time.Minute)) {
			t.Fatalf("next announce %v", a.NextAnnounce)
		}
	}
	if len(tr.Sources) != 6 || tr.Sources[0].Source != "tracker" || !tr.Sources[0].Enabled || tr.Sources[1].Source != "dht" || tr.Sources[1].Enabled ||
		tr.Sources[4].Source != "manual" || !tr.Sources[4].Enabled {
		t.Fatalf("sources %+v", tr.Sources)
	}
	// Paused: no next announce.
	if err := r.Pause(st.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "no next announce while paused", func() bool {
		tr, _ := r.Trackers(st.ID)
		return find(tr.Trackers, okURL).NextAnnounce == nil
	})

	// A web seed answering 404 to a leecher.
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	wb := metainfo.Builder{Version: metainfo.V1, PieceLength: 16 << 10, WebSeeds: []string{srv.URL + "/", "ftp://example.invalid/"}}
	wmi, err := wb.BuildPath(filepath.Join(dl, "movie.bin"))
	if err != nil {
		t.Fatal(err)
	}
	wraw, _ := bencode.Marshal(wmi)
	l := startRole(t, e, clientJSON(t.TempDir(), t.TempDir(), ""), nil)
	ws, err := l.Add(AddRequest{Torrent: wraw})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a web seed failure", func() bool {
		tr, err := l.Trackers(ws.ID)
		return err == nil && len(tr.WebSeeds) == 1 && tr.WebSeeds[0].Fails > 0 && tr.WebSeeds[0].Active == 0
	})
	tr, _ = l.Trackers(ws.ID)
	if w := tr.WebSeeds[0]; w.URL != srv.URL+"/" || !strings.Contains(w.Error, "404") || w.Dropped {
		t.Fatalf("web seed %+v", w)
	}
}
