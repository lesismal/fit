package bittorrent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	fibbt "github.com/lesismal/fib/bittorrent"
	"github.com/lesismal/fib/bittorrent/bencode"
	"github.com/lesismal/fib/bittorrent/metainfo"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
)

// loggedRole starts a role on e with the member js whose log is kept, and
// returns the log.
func loggedRole(t *testing.T, start func(setup func(*Role)) *Role) (*Role, *syncBuffer) {
	t.Helper()
	logged := &syncBuffer{}
	root, err := logs.New(nil, logged, logged)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	r := start(func(r *Role) { r.log = root.Logger("bittorrent") })
	return r, logged
}

// withTrackers returns torrent, a .torrent file, with the trackers and
// web seeds given.
func withTrackers(t *testing.T, torrent []byte, announce string, tiers [][]string, seeds []string) []byte {
	t.Helper()
	mi, err := metainfo.Parse(torrent)
	if err != nil {
		t.Fatal(err)
	}
	mi.Announce, mi.AnnounceList, mi.URLList = announce, tiers, seeds
	raw, err := bencode.Marshal(mi)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestRoleRegistration checks the role's registration: its member and the
// access list files it is reloaded on.
func TestRoleRegistration(t *testing.T) {
	var info *app.Info
	for _, i := range app.Roles() {
		if i.ID == ID {
			info = &i
		}
	}
	if info == nil || info.CertFiles == nil {
		t.Fatal("the role is not registered")
	}
	if got := info.CertFiles(&config.Config{}); got != nil {
		t.Fatalf("no bittorrent: %v", got)
	}
	if got := info.CertFiles(&config.Config{BitTorrent: &config.BitTorrent{}}); got != nil {
		t.Fatalf("no client: %v", got)
	}
	c := &config.Config{BitTorrent: &config.BitTorrent{Client: &config.BitTorrentClient{
		Access: &config.Access{AllowFiles: []string{"/a"}, DenyFiles: []string{"/d"}}}}}
	if got := info.CertFiles(c); len(got) != 2 || got[0] != "/a" || got[1] != "/d" {
		t.Fatalf("files %v", got)
	}
	if m, ok := info.Member(c).(*config.BitTorrentClient); !ok || m != c.BitTorrent.Client {
		t.Fatalf("member %v", info.Member(c))
	}
	if m, ok := info.Member(&config.Config{}).(*config.BitTorrentClient); !ok || m != nil {
		t.Fatalf("no member %v", m)
	}
	e := startEngine(t)
	ro, err := info.New(&app.Host{ID: ID, Engine: e})
	if err != nil {
		t.Fatal(err)
	}
	r := ro.(*Role)
	if r.key != e {
		t.Fatal("a host with no upstream engine: the role is not keyed by its engine")
	}
	if r.Addr().IsValid() {
		t.Fatalf("not started, listening on %v", r.Addr())
	}
	r.Stop(context.Background())
}

// TestApplyRefused checks the members Apply refuses, and Apply after Stop.
func TestApplyRefused(t *testing.T) {
	e := startEngine(t)
	r := New(&app.Host{ID: ID, Engine: e, Upstream: e})
	defer r.Stop(context.Background())
	if err := r.Apply(nil); err == nil || !strings.Contains(err.Error(), "no member") {
		t.Fatalf("nil: %v", err)
	}
	if err := r.Apply((*config.BitTorrentClient)(nil)); err == nil || !strings.Contains(err.Error(), "no member") {
		t.Fatalf("nil member: %v", err)
	}
	if err := r.Apply("a string"); err == nil || !strings.Contains(err.Error(), "no member") {
		t.Fatalf("a string: %v", err)
	}
	dir := t.TempDir()
	missing := strconv.Quote(filepath.Join(dir, "missing.txt"))
	if err := r.Apply(member(t, clientJSON(dir, "", `"access": {"allow_files": [`+missing+`]}`))); err == nil || !strings.HasPrefix(err.Error(), "access: ") {
		t.Fatalf("a missing allow file: %v", err)
	}
	// A member made by hand, not loaded: the zone is not checked before.
	m := member(t, clientJSON(dir, "", `"alt_speed": {"upload_rate": 1}`))
	m.AltSpeed.TimeZone = "Nowhere/Atlantis"
	if err := r.Apply(m); err == nil || !strings.HasPrefix(err.Error(), "alt_speed: ") {
		t.Fatalf("an unknown zone: %v", err)
	}
	if r.client != nil {
		t.Fatal("a refused member started a client")
	}
	r.Stop(context.Background())
	if err := r.Apply(member(t, clientJSON(dir, "", ""))); !errors.Is(err, app.ErrStopped) {
		t.Fatalf("after Stop: %v", err)
	}
}

// TestStoppedRole checks what a stopped role answers: its torrents listed
// as stopped, their trackers and web seeds as their .torrent file and
// magnet link name them, and ErrNotRunning from what needs the client.
func TestStoppedRole(t *testing.T) {
	e := startEngine(t)
	src := t.TempDir()
	torrent, _ := payload(t, src, "listed.bin", 64<<10)
	torrent = withTrackers(t, torrent, "http://t0.example/announce",
		[][]string{{"http://t1.example/announce", "udp://t2.example:80"}, {"http://t3.example/announce"}},
		[]string{"http://ws1.example/", "ftp://ws2.example/"})
	alone, _ := payload(t, t.TempDir(), "alone.bin", 32<<10)
	alone = withTrackers(t, alone, "http://only.example/announce", nil, nil)
	r := startRole(t, e, clientJSON(t.TempDir(), src, ""), nil)
	st, err := r.Add(AddRequest{Torrent: torrent})
	if err != nil {
		t.Fatal(err)
	}
	al, err := r.Add(AddRequest{Torrent: alone, Paused: true})
	if err != nil {
		t.Fatal(err)
	}
	const hash = "00112233445566778899aabbccddeeff00112233"
	m, err := r.Add(AddRequest{Paused: true, Magnet: "magnet:?xt=urn:btih:" + hash + "&tr=http%3A%2F%2Fm1.example%2Fa&ws=https%3A%2F%2Fmws.example%2F&ws=ftp%3A%2F%2Fmws2.example%2F"})
	if err != nil {
		t.Fatal(err)
	}
	if m.InfoHash != hash {
		t.Fatalf("a magnet link's hash before its info: %+v", m)
	}
	r.Stop(context.Background())

	for _, s := range r.List() {
		if s.State != "stopped" {
			t.Fatalf("after Stop %+v", s)
		}
	}
	tr, err := r.Trackers(st.ID)
	if err != nil || len(tr.Trackers) != 3 || tr.Trackers[0].URL != "http://t1.example/announce" || tr.Trackers[0].Tier != 0 ||
		tr.Trackers[1].URL != "udp://t2.example:80" || tr.Trackers[1].Tier != 0 || tr.Trackers[2].Tier != 1 ||
		tr.Trackers[0].Seeders != -1 || tr.Trackers[0].Leechers != -1 || tr.Trackers[0].Downloaded != -1 ||
		len(tr.WebSeeds) != 1 || tr.WebSeeds[0].URL != "http://ws1.example/" || len(tr.Sources) != 0 {
		t.Fatalf("a stopped torrent's trackers %+v, %v", tr, err)
	}
	if tr, err := r.Trackers(al.ID); err != nil || len(tr.Trackers) != 1 || tr.Trackers[0].URL != "http://only.example/announce" || len(tr.WebSeeds) != 0 {
		t.Fatalf("announce alone %+v, %v", tr, err)
	}
	if tr, err := r.Trackers(m.ID); err != nil || len(tr.Trackers) != 1 || tr.Trackers[0].URL != "http://m1.example/a" ||
		len(tr.WebSeeds) != 1 || tr.WebSeeds[0].URL != "https://mws.example/" {
		t.Fatalf("a stopped magnet link's trackers %+v, %v", tr, err)
	}
	if p, err := r.Peers(st.ID); err != nil || len(p) != 0 {
		t.Fatalf("peers %v, %v", p, err)
	}
	if _, err := r.Torrent(st.ID); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Torrent: %v", err)
	}
	if err := r.Pause(st.ID); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Pause: %v", err)
	}
	if _, err := r.AltSpeed(); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("AltSpeed: %v", err)
	}
	if err := r.SetAltSpeed(AltSpeedOn); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("SetAltSpeed: %v", err)
	}
	if err := r.Save(); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Save: %v", err)
	}
	if _, err := r.Add(AddRequest{Torrent: alone}); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Add: %v", err)
	}
	if _, err := r.AddURL("http://127.0.0.1:1/x.torrent", AddRequest{}); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("AddURL: %v", err)
	}
	if r.Addr().IsValid() {
		t.Fatalf("stopped, listening on %v", r.Addr())
	}
	if n := r.TemporaryBytes(); n != 0 {
		t.Fatalf("temporary bytes %d", n)
	}
}

// TestListed checks the trackers and web seeds listed for a torrent not
// in the client: those of its .torrent file, by tier, then its magnet
// link's that are not there already.
func TestListed(t *testing.T) {
	mi := &metainfo.MetaInfo{AnnounceList: [][]string{{"http://a/1", "http://a/2"}, {"http://b/1"}}, URLList: []string{"http://w/1"}}
	trs, seeds := listed(mi, "magnet:?xt=urn:btih:"+strings.Repeat("ab", 20)+"&tr=http%3A%2F%2Fa%2F2&tr=http%3A%2F%2Fc%2F1&ws=http%3A%2F%2Fw%2F1&ws=http%3A%2F%2Fw%2F2")
	if len(trs) != 4 || trs[3].URL != "http://c/1" || trs[3].Tier != 3 || trs[2].Tier != 1 || len(seeds) != 2 || seeds[1] != "http://w/2" {
		t.Fatalf("trackers %+v, seeds %v", trs, seeds)
	}
	if trs, seeds := listed(&metainfo.MetaInfo{Announce: "http://only/"}, ""); len(trs) != 1 || trs[0].URL != "http://only/" || len(seeds) != 0 {
		t.Fatalf("announce alone %+v %v", trs, seeds)
	}
	if trs, seeds := listed(nil, "not a magnet link"); len(trs) != 0 || len(seeds) != 0 {
		t.Fatalf("a bad magnet link %+v %v", trs, seeds)
	}
	if trs, seeds := listed(nil, ""); trs != nil || seeds != nil {
		t.Fatalf("nothing %+v %v", trs, seeds)
	}
}

// TestAddRefused checks the .torrent files and requests Add refuses before
// it holds anything, and the directories and files it cannot write.
func TestAddRefused(t *testing.T) {
	e := startEngine(t)
	src := t.TempDir()
	torrent, _ := payload(t, src, "one.bin", 64<<10)
	data := t.TempDir()
	r := startRole(t, e, clientJSON(data, src, `"categories": [{"name": "blocked", "dir": "blocked/inner"}]`), nil)

	if _, err := r.Add(AddRequest{Torrent: make([]byte, MaxTorrentBytes+1)}); !errors.Is(err, ErrBadRequest) || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("a .torrent file too large: %v", err)
	}
	if _, err := r.Add(AddRequest{Torrent: torrent, Priorities: []int{1, 1}}); !errors.Is(err, ErrBadRequest) || !strings.Contains(err.Error(), "2 priorities for 1 files") {
		t.Fatalf("too many priorities: %v", err)
	}
	// The category's directory cannot be made: a file is in its way.
	if err := os.WriteFile(filepath.Join(src, "blocked"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Add(AddRequest{Torrent: torrent, Category: "blocked"}); err == nil {
		t.Fatal("added into a directory that cannot be made")
	}
	// The .torrent file cannot be saved: its directory is a file.
	tdir := filepath.Join(data, torrentDir)
	if err := os.RemoveAll(tdir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tdir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Add(AddRequest{Torrent: torrent}); err == nil || !strings.Contains(err.Error(), "saving its .torrent file") {
		t.Fatalf("an unsaved .torrent file: %v", err)
	}
	if len(r.List()) != 0 {
		t.Fatalf("refused, yet listed: %+v", r.List())
	}
}

// TestAddHeldByPublish checks that a .torrent file a publish entry seeds
// already is refused as held (fib's ErrTorrentExists), with nothing of it
// left in the state.
func TestAddHeldByPublish(t *testing.T) {
	e := startEngine(t)
	pub := t.TempDir()
	if err := os.WriteFile(filepath.Join(pub, "shared.bin"), []byte(strings.Repeat("shared", 9000)), 0o644); err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	r := startRole(t, e, clientJSON(data, t.TempDir(), `"publish": [`+entryJSON("p", pub, "")+`]`), nil)
	var id string
	waitFor(t, "the published torrent", func() bool {
		ps := r.Publishing(nil)
		if len(ps) == 1 && len(ps[0].Torrents) == 1 && ps[0].Torrents[0].State == "seeding" {
			id = ps[0].Torrents[0].ID
		}
		return id != ""
	})
	torrent, _, _, ok := r.PublishedTorrent(nil, id)
	if !ok {
		t.Fatal("no .torrent file of the published torrent")
	}
	if _, err := r.Add(AddRequest{Torrent: torrent}); !errors.Is(err, ErrExists) {
		t.Fatalf("a torrent published already: %v", err)
	}
	if _, err := os.Stat(filepath.Join(data, torrentDir, id+".torrent")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("its .torrent file is left: %v", err)
	}
	if len(r.List()) != 0 {
		t.Fatalf("listed %+v", r.List())
	}
}

// TestStateNotSaved checks what is reported and logged while the state
// file cannot be replaced, and that it is saved again once it can.
func TestStateNotSaved(t *testing.T) {
	e := startEngine(t)
	src := t.TempDir()
	torrent, _ := payload(t, src, "s.bin", 64<<10)
	data := t.TempDir()
	r, logged := loggedRole(t, func(setup func(*Role)) *Role {
		return startRoleWith(t, e, member(t, strings.Replace(clientJSON(data, src, ""), `"50ms"`, `"1h"`, 1)), setup)
	})
	// The state file is a directory with something in it: the rename of a
	// new one onto it fails.
	state := filepath.Join(data, stateFile)
	if err := os.Remove(state); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(state, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	st, err := r.Add(AddRequest{Torrent: torrent})
	if err != nil {
		t.Fatalf("an unsaved list fails an Add: %v", err)
	}
	if !strings.Contains(logged.String(), "saving the state in") {
		t.Fatalf("not logged:\n%s", logged.String())
	}
	if err := r.Pause(st.ID); err == nil || !strings.Contains(err.Error(), "saving the state") {
		t.Fatalf("Pause: %v", err)
	}
	if err := r.SetTemporary(st.ID, true); err == nil || !strings.Contains(err.Error(), "saving the state") {
		t.Fatalf("SetTemporary: %v", err)
	}
	// A temporary torrent added by hand is kept: the list is saved at once.
	if _, err := r.Add(AddRequest{Torrent: torrent}); err == nil || !strings.Contains(err.Error(), "saving the state") {
		t.Fatalf("a temporary torrent kept: %v", err)
	}
	if err := r.Save(); err == nil {
		t.Fatal("Save reports nothing")
	}
	if err := r.Remove(st.ID, false); err == nil || !strings.Contains(err.Error(), "saving the state") {
		t.Fatalf("Remove: %v", err)
	}
	if err := os.RemoveAll(state); err != nil {
		t.Fatal(err)
	}
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logged.String(), "is saved again") {
		t.Fatalf("not logged:\n%s", logged.String())
	}
	recs, _, err := loadState(data)
	if err != nil || len(recs) != 0 {
		t.Fatalf("saved %v, %v", recs, err)
	}
}

// TestTemporaryKeptByHand checks that adding by hand a torrent bt_stream
// added on the fly keeps it, logged, and that SetTemporary to what it is
// changes nothing.
func TestTemporaryKeptByHand(t *testing.T) {
	e := startEngine(t)
	src := t.TempDir()
	torrent, _ := payload(t, src, "k.bin", 64<<10)
	data := t.TempDir()
	r, logged := loggedRole(t, func(setup func(*Role)) *Role {
		return startRoleWith(t, e, member(t, clientJSON(data, src, "")), setup)
	})
	st, err := r.AddTemporary(AddRequest{Torrent: torrent}, 1)
	if err != nil || !st.Temporary {
		t.Fatalf("%+v %v", st, err)
	}
	if err := r.SetTemporary(st.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.AddTemporary(AddRequest{Torrent: torrent}, 1); !errors.Is(err, ErrExists) {
		t.Fatalf("twice on the fly: %v", err)
	}
	kept, err := r.Add(AddRequest{Torrent: torrent})
	if err != nil || kept.Temporary || kept.ID != st.ID {
		t.Fatalf("kept %+v %v", kept, err)
	}
	if !strings.Contains(logged.String(), "added on the fly, is kept now") {
		t.Fatalf("not logged:\n%s", logged.String())
	}
	if _, err := r.Add(AddRequest{Torrent: torrent}); !errors.Is(err, ErrExists) {
		t.Fatalf("again: %v", err)
	}
	if err := r.SetTemporary(strings.Repeat("0", 40), true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	if ok, err := r.RemoveTemporary(st.ID); ok || err != nil {
		t.Fatalf("a kept torrent removed as temporary: %v %v", ok, err)
	}
}

// TestFilePrioritiesBeforeInfo checks that a magnet link's file
// priorities are refused while its info is not known, and that a
// priority set on a torrent added with fewer priorities than files keeps
// those it was given.
func TestFilePrioritiesBeforeInfo(t *testing.T) {
	e := startEngine(t)
	src := t.TempDir()
	r := startRole(t, e, clientJSON(t.TempDir(), src, ""), nil)
	m, err := r.Add(AddRequest{Paused: true, Magnet: "magnet:?xt=urn:btih:" + strings.Repeat("cd", 20)})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SetFilePriority(m.ID, 0, fibbt.PriorityHigh); !errors.Is(err, ErrBadRequest) || !strings.Contains(err.Error(), "not known yet") {
		t.Fatalf("SetFilePriority: %v", err)
	}
	if err := r.SetFilePriorities(m.ID, []int{1}); !errors.Is(err, ErrBadRequest) || !strings.Contains(err.Error(), "not known yet") {
		t.Fatalf("SetFilePriorities: %v", err)
	}
	if err := r.Resume(m.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.Resume(m.ID); err != nil {
		t.Fatalf("resumed twice: %v", err)
	}

	// Two files, one priority given: the second is normal until set.
	dir := filepath.Join(t.TempDir(), "two")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"a.bin", "b.bin"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(strings.Repeat(n, 9000)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b := metainfo.Builder{PieceLength: 16 << 10}
	mi, err := b.BuildPath(dir)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := bencode.Marshal(mi)
	if err != nil {
		t.Fatal(err)
	}
	st, err := r.Add(AddRequest{Torrent: raw, Paused: true, Priorities: []int{fibbt.PriorityLow}})
	if err != nil {
		t.Fatal(err)
	}
	// The info's file list has a padding file between the two.
	last := len(mi.Info.FileList()) - 1
	if err := r.SetFilePriority(st.ID, last, fibbt.PriorityHigh); err != nil {
		t.Fatal(err)
	}
	s, err := r.Get(st.ID)
	if err != nil || len(s.Files) != 2 || s.Files[0].Priority != fibbt.PriorityLow || s.Files[1].Priority != fibbt.PriorityHigh || s.Files[1].Index != last {
		t.Fatalf("files %+v %v", s.Files, err)
	}
}
