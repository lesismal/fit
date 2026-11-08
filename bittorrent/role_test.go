package bittorrent

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fib/bittorrent/metainfo"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
)

// TestRoleThroughManager starts and stops the role as fit does, by the
// configs the app Manager applies.
func TestRoleThroughManager(t *testing.T) {
	e := startEngine(t)
	m := app.NewManager(e, e, nil, nil)
	defer m.Stop(context.Background())
	dir := t.TempDir()
	cfg, err := config.Load([]byte(`{"bittorrent": {"client": ` + clientJSON(dir, "", "") + `}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Apply(cfg); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(m.Running(), ID) {
		t.Fatalf("running %v", m.Running())
	}
	r := Lookup(e)
	if r == nil || !r.Addr().IsValid() || r.Addr().Port() == 0 {
		t.Fatalf("role %v at %v", r, r.Addr())
	}
	if mods := m.Modules(); len(mods) != 1 || mods[0].ID != ID {
		t.Fatalf("modules %v", mods)
	}
	empty, err := config.Load([]byte(`{"listeners": [{"name": "web", "address": "127.0.0.1:0"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Apply(empty); err != nil {
		t.Fatal(err)
	}
	if Lookup(e) != nil || slices.Contains(m.Running(), ID) {
		t.Fatal("the role runs on without its member")
	}
	if _, err := os.Stat(filepath.Join(dir, stateFile)); err != nil {
		t.Fatalf("no state saved at stop: %v", err)
	}
	if _, err := r.Add(AddRequest{Magnet: "magnet:?xt=urn:btih:" + strings.Repeat("ab", 20)}); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("add on a stopped role: %v", err)
	}
}

// TestDownloadRestartWithoutHashing downloads a torrent between two roles,
// restarts the leecher, and checks that it has every piece again without
// reading its data.
func TestDownloadRestartWithoutHashing(t *testing.T) {
	e := startEngine(t)
	src := t.TempDir()
	torrent, data := payload(t, src, "movie.bin", 1<<20+12345)
	s, _ := seeder(t, e, src, torrent, "")

	ldata, ldl := t.TempDir(), t.TempDir()
	l := startRole(t, e, clientJSON(ldata, ldl, ""), nil)
	st, err := l.Add(AddRequest{Torrent: torrent, Peers: []string{peerAddr(s)}})
	if err != nil {
		t.Fatal(err)
	}
	if st.Dir != ldl || st.Name != "movie.bin" {
		t.Fatalf("status %+v", st)
	}
	waitDone(t, l, st.ID)
	sameFile(t, filepath.Join(ldl, "movie.bin"), data)
	l.Stop(context.Background())
	for _, p := range []string{stateFile, "torrents/" + st.ID + ".torrent", "resume/" + st.ID + ".resume"} {
		if _, err := os.Stat(filepath.Join(ldata, p)); err != nil {
			t.Fatalf("%s not saved: %v", p, err)
		}
	}

	rc := &readCounter{}
	l2 := startRole(t, e, clientJSON(ldata, ldl, ""), rc)
	list := l2.List()
	if len(list) != 1 || list[0].ID != st.ID || list[0].Dir != ldl {
		t.Fatalf("restored %+v", list)
	}
	waitFor(t, "every piece after the restart", func() bool {
		tor, err := l2.Torrent(st.ID)
		return err == nil && tor.HaveAll()
	})
	if n := rc.total(); n != 0 {
		t.Fatalf("%d reads of the data after a restart with resume data", n)
	}
}

// TestMagnetSavedAsTorrent adds a magnet link, and checks that its
// .torrent file is saved once its info arrives, and restored from.
func TestMagnetSavedAsTorrent(t *testing.T) {
	e := startEngine(t)
	src := t.TempDir()
	torrent, data := payload(t, src, "song.bin", 300<<10)
	s, id := seeder(t, e, src, torrent, "")
	mi, err := metainfo.Parse(torrent)
	if err != nil {
		t.Fatal(err)
	}

	ldata, ldl := t.TempDir(), t.TempDir()
	l := startRole(t, e, clientJSON(ldata, ldl, ""), nil)
	st, err := l.Add(AddRequest{Magnet: mi.Magnet().String(), Peers: []string{peerAddr(s)}})
	if err != nil {
		t.Fatal(err)
	}
	if st.ID != id {
		t.Fatalf("the magnet link's ID %s, the torrent's %s", st.ID, id)
	}
	waitDone(t, l, id)
	sameFile(t, filepath.Join(ldl, "song.bin"), data)
	waitFor(t, "the .torrent file", func() bool {
		_, err := os.Stat(filepath.Join(ldata, torrentDir, id+".torrent"))
		return err == nil
	})
	l.Stop(context.Background())

	rc := &readCounter{}
	l2 := startRole(t, e, clientJSON(ldata, ldl, ""), rc)
	got, err := l2.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.State == "metadata" || len(got.Files) != 1 || got.Files[0].Path != "song.bin" {
		t.Fatalf("restored %+v", got)
	}
	waitFor(t, "every piece after the restart", func() bool {
		tor, err := l2.Torrent(id)
		return err == nil && tor.HaveAll()
	})
	if n := rc.total(); n != 0 {
		t.Fatalf("%d reads after a restart", n)
	}
}

// TestReloadLimitsKeepsPeer changes the limits of a role downloading over
// a slow peer, and checks that the peer stays connected and the client is
// the same one.
func TestReloadLimitsKeepsPeer(t *testing.T) {
	e := startEngine(t)
	src := t.TempDir()
	torrent, data := payload(t, src, "big.bin", 2<<20)
	s, id := seeder(t, e, src, torrent, "")

	ldata, ldl := t.TempDir(), t.TempDir()
	l := startRole(t, e, clientJSON(ldata, ldl, `"limits": {"download_rate": 131072}`), nil)
	if _, err := l.Add(AddRequest{Torrent: torrent, Peers: []string{peerAddr(s)}}); err != nil {
		t.Fatal(err)
	}
	tor, err := l.Torrent(id)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a peer sending", func() bool {
		ps := tor.Peers()
		return len(ps) > 0 && ps[0].Downloaded > 0
	})
	before := tor.Peers()[0]
	client := l.Client()

	if err := l.Apply(member(t, clientJSON(ldata, ldl, `"limits": {"download_rate": 0, "max_conns": 150, "upload_slots": 6}`))); err != nil {
		t.Fatal(err)
	}
	if l.Client() != client {
		t.Fatal("a change of limits made a new client")
	}
	if got := l.Client().Limits(); got.MaxConns != 150 || got.UploadSlots != 6 || got.DownloadRate != 0 {
		t.Fatalf("limits %+v", got)
	}
	after := tor.Peers()
	i := slices.IndexFunc(after, func(p fibPeer) bool { return p.Addr == before.Addr })
	if i < 0 || after[i].Downloaded < before.Downloaded {
		t.Fatalf("the peer was dropped: before %+v, after %+v", before, after)
	}
	waitDone(t, l, id)
	sameFile(t, filepath.Join(ldl, "big.bin"), data)
}

// TestListenChangeKeepsTorrents moves the client to another port: the
// torrents stay, resumed without reading their data.
func TestListenChangeKeepsTorrents(t *testing.T) {
	e := startEngine(t)
	src := t.TempDir()
	torrent, _ := payload(t, src, "keep.bin", 256<<10)
	rc := &readCounter{}
	data := t.TempDir()
	r := startRole(t, e, clientJSON(data, src, ""), rc)
	st, err := r.Add(AddRequest{Torrent: torrent})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the check", func() bool {
		tor, err := r.Torrent(st.ID)
		return err == nil && tor.HaveAll()
	})
	if err := r.Pause(st.ID); err != nil {
		t.Fatal(err)
	}
	old := r.Client()
	reads := rc.total()

	port := freePort(t)
	js := strings.Replace(clientJSON(data, src, ""), `"127.0.0.1:0"`, `"127.0.0.1:`+strconv.Itoa(port)+`"`, 1)
	if err := r.Apply(member(t, js)); err != nil {
		t.Fatal(err)
	}
	if r.Client() == old || int(r.Addr().Port()) != port {
		t.Fatalf("no new client on port %d: %v", port, r.Addr())
	}
	got, err := r.Get(st.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Paused || got.State != "paused" {
		t.Fatalf("after the move %+v", got)
	}
	if err := r.Resume(st.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "every piece on the new client", func() bool {
		tor, err := r.Torrent(st.ID)
		return err == nil && tor.HaveAll()
	})
	if n := rc.total() - reads; n != 0 {
		t.Fatalf("%d reads after the move", n)
	}

	// A port that cannot be had leaves the client before running.
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	busyPort := strconv.Itoa(taken.Addr().(*net.TCPAddr).Port)
	if err := r.Apply(member(t, strings.Replace(js, strconv.Itoa(port), busyPort, 1))); err == nil {
		t.Fatal("a client on a taken port")
	}
	if r.Client() == nil || int(r.Addr().Port()) != port || len(r.List()) != 1 {
		t.Fatalf("after a failed move: client %v at %v, torrents %v", r.Client(), r.Addr(), r.List())
	}
	waitFor(t, "every piece on the client made again", func() bool {
		tor, err := r.Torrent(st.ID)
		return err == nil && tor.HaveAll()
	})
}

// TestCutConnection stops the only seeder mid-download: the download goes
// on from another one.
func TestCutConnection(t *testing.T) {
	e := startEngine(t)
	src := t.TempDir()
	torrent, data := payload(t, src, "cut.bin", 2<<20)
	s1, id := seeder(t, e, src, torrent, "")

	ldata, ldl := t.TempDir(), t.TempDir()
	slow := clientJSON(ldata, ldl, `"limits": {"download_rate": 131072}`)
	l := startRole(t, e, slow, nil)
	if _, err := l.Add(AddRequest{Torrent: torrent, Peers: []string{peerAddr(s1)}}); err != nil {
		t.Fatal(err)
	}
	tor, err := l.Torrent(id)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "some data", func() bool { return tor.Stats().Downloaded > 0 })
	s1.Stop(context.Background())
	waitFor(t, "the cut peer to go", func() bool { return len(tor.Peers()) == 0 })
	if tor.HaveAll() {
		t.Fatal("done before the cut")
	}

	s2, _ := seeder(t, e, src, torrent, "")
	tor.AddPeers(peerAddr(s2))
	if err := l.Apply(member(t, clientJSON(ldata, ldl, ""))); err != nil {
		t.Fatal(err)
	}
	waitDone(t, l, id)
	sameFile(t, filepath.Join(ldl, "cut.bin"), data)
}

// TestAccessClosesPeers denies the seeder's address: the connected peer is
// closed at once.
func TestAccessClosesPeers(t *testing.T) {
	e := startEngine(t)
	src := t.TempDir()
	torrent, _ := payload(t, src, "deny.bin", 2<<20)
	s, id := seeder(t, e, src, torrent, "")
	ldata, ldl := t.TempDir(), t.TempDir()
	l := startRole(t, e, clientJSON(ldata, ldl, `"limits": {"download_rate": 65536}`), nil)
	if _, err := l.Add(AddRequest{Torrent: torrent, Peers: []string{peerAddr(s)}}); err != nil {
		t.Fatal(err)
	}
	tor, err := l.Torrent(id)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a peer", func() bool { return len(tor.Peers()) > 0 })
	client := l.Client()
	if err := l.Apply(member(t, clientJSON(ldata, ldl, `"limits": {"download_rate": 65536}, "access": {"deny": ["127.0.0.1"]}`))); err != nil {
		t.Fatal(err)
	}
	if l.Client() != client {
		t.Fatal("a change of access made a new client")
	}
	waitFor(t, "the denied peer to be closed", func() bool { return len(tor.Peers()) == 0 })
}

// TestPauseRemoveAndBounds runs the API's changes and refusals.
func TestPauseRemoveAndBounds(t *testing.T) {
	e := startEngine(t)
	src := t.TempDir()
	torrent, _ := payload(t, src, "a.bin", 100<<10)
	other, _ := payload(t, src, "b.bin", 90<<10)
	data := t.TempDir()
	js := clientJSON(data, src, `"limits": {"max_torrents": 1}`)
	r := startRole(t, e, js, nil)
	st, err := r.Add(AddRequest{Torrent: torrent, Sequential: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Add(AddRequest{Torrent: torrent}); !errors.Is(err, ErrExists) {
		t.Fatalf("added twice: %v", err)
	}
	if _, err := r.Add(AddRequest{Torrent: other}); !errors.Is(err, ErrTooMany) {
		t.Fatalf("past max_torrents: %v", err)
	}
	for _, bad := range []AddRequest{
		{},
		{Magnet: "magnet:?xt=urn:btih:zz"},
		{Torrent: []byte("not a torrent")},
		{Magnet: "magnet:?" + strings.Repeat("x", MaxMagnetBytes)},
		// A name that would lead out of download_dir: metainfo refuses it.
		{Torrent: []byte("d4:infod6:lengthi1e4:name5:../ab12:piece lengthi16384e6:pieces20:aaaaaaaaaaaaaaaaaaaaee")},
		{Torrent: []byte("d4:infod6:lengthi1e4:name2:..12:piece lengthi16384e6:pieces20:aaaaaaaaaaaaaaaaaaaaee")},
		{Torrent: torrent, Priorities: []int{9}},
	} {
		if _, err := r.Add(bad); !errors.Is(err, ErrBadRequest) {
			t.Errorf("%.60q: %v", bad.Magnet+string(bad.Torrent), err)
		}
	}
	if err := r.Pause(st.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.SetFilePriority(st.ID, 0, 7); err != nil {
		t.Fatal(err)
	}
	if err := r.SetFilePriority(st.ID, 3, 7); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("file 3 of 1: %v", err)
	}
	if err := r.Pause("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("pause of none: %v", err)
	}
	r.Stop(context.Background())

	r2 := startRole(t, e, js, nil)
	got, err := r2.Get(st.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Paused || !got.Sequential || got.State != "paused" || len(got.Files) != 1 || got.Files[0].Priority != 7 {
		t.Fatalf("after a restart %+v", got)
	}
	if err := r2.Remove(st.ID, false); err != nil {
		t.Fatal(err)
	}
	if len(r2.List()) != 0 {
		t.Fatal("removed, still listed")
	}
	if _, err := os.Stat(filepath.Join(src, "a.bin")); err != nil {
		t.Fatalf("removed without data, the data is gone: %v", err)
	}
	for _, p := range []string{"torrents/" + st.ID + ".torrent", "resume/" + st.ID + ".resume"} {
		if _, err := os.Stat(filepath.Join(data, p)); !os.IsNotExist(err) {
			t.Fatalf("%s left: %v", p, err)
		}
	}
	// With its data.
	st2, err := r2.Add(AddRequest{Torrent: other})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the check", func() bool {
		tor, err := r2.Torrent(st2.ID)
		return err == nil && tor.HaveAll()
	})
	if err := r2.Remove(st2.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(src, "b.bin")); !os.IsNotExist(err) {
		t.Fatalf("removed with data, the data stays: %v", err)
	}
}

// TestDiskError downloads into a path that cannot be written: the torrent
// reports its storage's error, and the role and its other torrents go on.
func TestDiskError(t *testing.T) {
	e := startEngine(t)
	src := t.TempDir()
	torrent, _ := payload(t, src, "e.bin", 256<<10)
	s, id := seeder(t, e, src, torrent, "")
	ldata, ldl := t.TempDir(), t.TempDir()
	// A directory where the file goes.
	if err := os.MkdirAll(filepath.Join(ldl, "e.bin", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	l := startRole(t, e, clientJSON(ldata, ldl, ""), nil)
	if _, err := l.Add(AddRequest{Torrent: torrent, Peers: []string{peerAddr(s)}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the storage error", func() bool {
		st, err := l.Get(id)
		return err == nil && st.Error != ""
	})
	if err := l.Save(); err != nil {
		t.Fatal(err)
	}
	if err := l.Remove(id, false); err != nil {
		t.Fatal(err)
	}
}

// TestConsoleRoutes reads the torrents through the role's console.
func TestConsoleRoutes(t *testing.T) {
	e := startEngine(t)
	src := t.TempDir()
	torrent, _ := payload(t, src, "c.bin", 64<<10)
	h, err := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	console := `"console": {"address": "127.0.0.1:0", "users": [{"username": "admin", "password_hash": ` + strconv.Quote(string(h)) + `}]}`
	r := startRole(t, e, clientJSON(t.TempDir(), src, console), nil)
	st, err := r.Add(AddRequest{Torrent: torrent})
	if err != nil {
		t.Fatal(err)
	}
	base := strings.TrimSuffix(r.console.URL(), "/")
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar, Timeout: hang}
	resp, err := hc.Post(base+"/api/login", "application/json", strings.NewReader(`{"username":"admin","password":"pw"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	for path, want := range map[string]string{
		"/api/torrents":                              `"id":"` + st.ID + `"`,
		"/api/torrents/" + st.ID:                     `"path":"c.bin"`,
		"/api/m/bittorrent.client/torrents/" + st.ID: `"path":"c.bin"`,
		"/api/torrents/" + strings.Repeat("0", 40):   `no such torrent`,
	} {
		resp, err := hc.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !strings.Contains(string(body), want) {
			t.Errorf("%s: %d %s", path, resp.StatusCode, body)
		}
	}
}
