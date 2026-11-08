package tracker

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fib/bittorrent/metainfo"
	fibtracker "github.com/lesismal/fib/bittorrent/tracker"
	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/mediaauth"
)

// TestRoleThroughManager starts and stops the role as fit does, by the
// configs the app Manager applies.
func TestRoleThroughManager(t *testing.T) {
	e := startEngine(t)
	m := app.NewManager(e, e, nil, nil)
	defer m.Stop(context.Background())
	if err := m.Apply(load(t, `{"listen": "127.0.0.1:0"}`, "")); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(m.Running(), ID) {
		t.Fatalf("running %v", m.Running())
	}
	r := Lookup(e)
	if r == nil || r.UDPAddr().Port() == 0 {
		t.Fatalf("role %v", r)
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
	if _, err := r.Stats(); err != ErrNotRunning {
		t.Fatalf("stats of a stopped role: %v", err)
	}
}

// TestAnnounceScrapeHTTPAndUDP has peers announce one torrent over HTTP and
// UDP, find each other, complete and stop, and scrapes it over both and
// through the role's statistics.
func TestAnnounceScrapeHTTPAndUDP(t *testing.T) {
	e := startEngine(t)
	r := startRole(t, e, load(t, `{"listen": "127.0.0.1:0", "interval": "10m", "min_interval": "1m"}`, ""))
	base := serveHTTP(t, e)
	cl := newClient(t, e, time.Second, 8)
	hu, uu := base+"/announce", udpURL(r, "/announce")
	h := hashOf("swarm")

	resp := mustAnnounce(t, cl, hu, h, 1, 100, fibtracker.EventStarted)
	if resp.Interval != 10*time.Minute || resp.MinInterval != time.Minute || len(resp.Peers) != 0 {
		t.Fatalf("first announce: %+v", resp)
	}
	resp = mustAnnounce(t, cl, uu, h, 2, 100, fibtracker.EventStarted)
	if resp.Interval != 10*time.Minute || len(resp.Peers) != 1 || resp.Peers[0].Addr.Port() != 20001 {
		t.Fatalf("UDP announce: %+v", resp)
	}
	resp = mustAnnounce(t, cl, hu, h, 3, 0, fibtracker.EventStarted)
	if resp.Complete != 1 || resp.Incomplete != 2 || len(resp.Peers) != 2 {
		t.Fatalf("seeder's announce: %+v", resp)
	}
	// Peer 1 completes, peer 2 stops.
	mustAnnounce(t, cl, hu, h, 1, 0, fibtracker.EventCompleted)
	mustAnnounce(t, cl, uu, h, 2, 0, fibtracker.EventStopped)

	for _, u := range []string{hu, uu} {
		files, err := cl.Scrape(ctx(t), u, []metainfo.InfoHash{h, hashOf("nobody")})
		if err != nil {
			t.Fatal(u, err)
		}
		if st := files[h]; st.Complete != 2 || st.Incomplete != 0 || st.Downloaded != 1 {
			t.Fatalf("scrape over %s: %+v", u, files)
		}
	}
	ts, err := r.Torrent(h)
	if err != nil || !ts.Tracked || ts.Seeders != 2 || ts.Leechers != 0 || ts.Completed != 1 || ts.InfoHash != h.String() {
		t.Fatalf("torrent stats %+v %v", ts, err)
	}
	if ts, _ := r.Torrent(hashOf("nobody")); ts.Tracked {
		t.Fatalf("an unknown torrent is tracked: %+v", ts)
	}
	st, err := r.Stats()
	if err != nil || st.Torrents != 1 || st.Peers != 2 || st.Announces != 5 || st.Scrapes != 2 || st.Private || st.UDP == "" {
		t.Fatalf("stats %+v %v", st, err)
	}
	// Without an allow-list, Torrents lists none.
	if list, total, err := r.Torrents(0, 0); err != nil || len(list) != 0 || total != 0 {
		t.Fatalf("torrents %v %d %v", list, total, err)
	}
	// A path that is neither an announce nor a scrape.
	res, err := http.Get(base + "/other")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("other path: %d", res.StatusCode)
	}
}

// TestHandlerWithoutRole answers 503 while no role runs on the engine.
func TestHandlerWithoutRole(t *testing.T) {
	e := startEngine(t)
	base := serveHTTP(t, e)
	res, err := http.Get(base + "/announce?info_hash=x")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status %d", res.StatusCode)
	}
}

// TestBounds checks the store's bounds and max_numwant.
func TestBounds(t *testing.T) {
	e := startEngine(t)
	r := startRole(t, e, load(t, `{"max_torrents": 2, "max_peers_per_torrent": 3, "max_peers": 5, "max_numwant": 2}`, ""))
	cl := newClient(t, e, time.Second, 8)
	u := serveHTTP(t, e) + "/announce"

	a, b := hashOf("a"), hashOf("b")
	for i := 1; i <= 4; i++ {
		resp := mustAnnounce(t, cl, u, a, i, 100, fibtracker.EventStarted)
		if len(resp.Peers) > 2 {
			t.Fatalf("announce %d got %d peers past max_numwant", i, len(resp.Peers))
		}
	}
	// The fourth peer of a was past max_peers_per_torrent: kept out.
	if ts, _ := r.Torrent(a); ts.Leechers != 3 {
		t.Fatalf("torrent a: %+v", ts)
	}
	mustAnnounce(t, cl, u, b, 10, 100, fibtracker.EventStarted)
	mustAnnounce(t, cl, u, b, 11, 100, fibtracker.EventStarted)
	// Past max_peers (5) in all: the third peer of b gets peers but is not
	// kept.
	resp := mustAnnounce(t, cl, u, b, 12, 100, fibtracker.EventStarted)
	if len(resp.Peers) != 2 {
		t.Fatalf("a peer past max_peers got %d peers", len(resp.Peers))
	}
	if st, _ := r.Stats(); st.Peers != 5 || st.Torrents != 2 || st.MaxTorrents != 2 || st.MaxPeers != 5 {
		t.Fatalf("stats %+v", st)
	}
	_, err := announce(t, cl, u, hashOf("c"), 20, 100, fibtracker.EventStarted)
	refusedWith(t, err, "tracker is full")
}

// TestAccess refuses a client address on UDP before a connection is made
// and on HTTP with a failure reason, and lets it in again after a reload.
func TestAccess(t *testing.T) {
	e := startEngine(t)
	deny := `{"listen": "127.0.0.1:0", "access": {"deny": ["127.0.0.1"]}}`
	r := startRole(t, e, load(t, deny, ""))
	hu := serveHTTP(t, e) + "/announce"
	cl := newClient(t, e, 200*time.Millisecond, -1)
	h := hashOf("access")

	_, err := announce(t, cl, hu, h, 1, 100, fibtracker.EventStarted)
	refusedWith(t, err, "refused")
	c, cancel := context.WithTimeout(context.Background(), time.Second)
	_, err = cl.Announce(c, udpURL(r, "/announce"), &fibtracker.AnnounceRequest{InfoHash: h, PeerID: peerID(2), Port: 1, Left: 1})
	cancel()
	if err == nil {
		t.Fatal("a refused address announced over UDP")
	}
	if st, _ := r.Stats(); st.Refused["access"] != 1 || st.Announces != 0 {
		t.Fatalf("stats %+v", st)
	}
	if err := r.Apply(load(t, `{"listen": "127.0.0.1:0"}`, "").BitTorrent.Tracker); err != nil {
		t.Fatal(err)
	}
	mustAnnounce(t, cl, hu, h, 1, 100, fibtracker.EventStarted)
	ucl := newClient(t, e, time.Second, 8)
	mustAnnounce(t, ucl, udpURL(r, "/announce"), h, 2, 100, fibtracker.EventStarted)
}

// TestReloadInPlace changes the config of a running tracker: the interval
// keeps the swarms, a new listen moves UDP, a taken port leaves the one
// before, and new store bounds start the swarms anew.
func TestReloadInPlace(t *testing.T) {
	e := startEngine(t)
	r := startRole(t, e, load(t, `{"listen": "127.0.0.1:0"}`, ""))
	hu := serveHTTP(t, e) + "/announce"
	cl := newClient(t, e, time.Second, 8)
	h := hashOf("reload")
	mustAnnounce(t, cl, hu, h, 1, 100, fibtracker.EventStarted)
	old := r.UDPAddr()

	apply := func(js string) error { return r.Apply(load(t, js, "").BitTorrent.Tracker) }
	if err := apply(`{"listen": "127.0.0.1:0", "interval": "5m"}`); err != nil {
		t.Fatal(err)
	}
	resp := mustAnnounce(t, cl, hu, h, 2, 100, fibtracker.EventStarted)
	if resp.Interval != 5*time.Minute || len(resp.Peers) != 1 {
		t.Fatalf("after the interval changed: %+v", resp)
	}
	if r.UDPAddr() != old {
		t.Fatalf("UDP moved from %v to %v", old, r.UDPAddr())
	}

	// A port taken by something else: the reload fails, all as it was.
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	if err := apply(`{"listen": "` + pc.LocalAddr().String() + `", "interval": "7m"}`); err == nil {
		t.Fatal("a taken port was bound")
	}
	if r.UDPAddr() != old {
		t.Fatalf("after a failed reload UDP is at %v, was %v", r.UDPAddr(), old)
	}
	resp = mustAnnounce(t, cl, udpURL(r, "/announce"), h, 3, 100, fibtracker.EventStarted)
	if resp.Interval != 5*time.Minute || len(resp.Peers) != 2 {
		t.Fatalf("after a failed reload: %+v", resp)
	}

	// The port it has, named: it stays there; then another port.
	if err := apply(`{"listen": "` + old.String() + `", "interval": "5m"}`); err != nil {
		t.Fatal(err)
	}
	if r.UDPAddr() != old {
		t.Fatalf("UDP at %v, was %v", r.UDPAddr(), old)
	}
	if err := apply(`{"listen": "127.0.0.1:0", "interval": "5m"}`); err != nil {
		t.Fatal(err)
	}
	if r.UDPAddr() == old || r.UDPAddr().Port() == 0 {
		t.Fatalf("UDP did not move from %v", old)
	}
	resp = mustAnnounce(t, cl, udpURL(r, "/announce"), h, 3, 100, fibtracker.EventStarted)
	if len(resp.Peers) != 2 {
		t.Fatalf("on the new port: %+v", resp)
	}

	// No UDP, then a new store.
	if err := apply(`{"interval": "5m"}`); err != nil {
		t.Fatal(err)
	}
	if r.UDPAddr().IsValid() {
		t.Fatalf("UDP still at %v", r.UDPAddr())
	}
	if ts, _ := r.Torrent(h); ts.Leechers != 3 {
		t.Fatalf("swarm lost with UDP: %+v", ts)
	}
	if err := apply(`{"interval": "5m", "max_torrents": 10}`); err != nil {
		t.Fatal(err)
	}
	if ts, _ := r.Torrent(h); ts.Tracked {
		t.Fatalf("a new store kept the swarm: %+v", ts)
	}
}

// TestAllowList refuses torrents the allow-list lacks, reads its files
// again when applied, and keeps the list before when a file is bad.
func TestAllowList(t *testing.T) {
	e := startEngine(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "allow.txt")
	a, b, c := hashOf("allowed a"), hashOf("allowed b"), hashOf("allowed c")
	if err := os.WriteFile(file, []byte("# torrents\n"+b.String()+"  # b\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	js := `{"listen": "127.0.0.1:0", "allow": ["` + a.String() + `"], "allow_files": ["` + file + `"]}`
	cfg := load(t, js, "")
	r := startRole(t, e, cfg)
	if files := app.CertFiles(cfg); !slices.Contains(files, file) {
		t.Fatalf("the allow file is not watched: %v", files)
	}
	hu := serveHTTP(t, e) + "/announce"
	cl := newClient(t, e, time.Second, 8)

	mustAnnounce(t, cl, hu, a, 1, 100, fibtracker.EventStarted)
	mustAnnounce(t, cl, udpURL(r, "/announce"), b, 2, 100, fibtracker.EventStarted)
	_, err := announce(t, cl, hu, c, 3, 100, fibtracker.EventStarted)
	refusedWith(t, err, "unregistered torrent")
	_, err = announce(t, cl, udpURL(r, "/announce"), c, 3, 100, fibtracker.EventStarted)
	refusedWith(t, err, "unregistered torrent")
	files, err := cl.Scrape(ctx(t), hu, []metainfo.InfoHash{a, c})
	if err != nil || files[a].Incomplete != 1 || len(files) != 1 {
		t.Fatalf("scrape %v %v", files, err)
	}
	list, total, err := r.Torrents(0, 0)
	if err != nil || total != 2 || len(list) != 2 || list[0].InfoHash != a.String() || list[0].Leechers != 1 || list[1].Leechers != 1 {
		t.Fatalf("torrents %+v %d %v", list, total, err)
	}
	if list, _, _ := r.Torrents(1, 5); len(list) != 1 || list[0].InfoHash != b.String() {
		t.Fatalf("torrents from 1: %+v", list)
	}

	// The file changes, as the watcher sees it, and the config is applied
	// again: c is allowed, b no more.
	if err := os.WriteFile(file, []byte(c.String()+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.Apply(load(t, js, "").BitTorrent.Tracker); err != nil {
		t.Fatal(err)
	}
	mustAnnounce(t, cl, hu, c, 3, 100, fibtracker.EventStarted)
	_, err = announce(t, cl, hu, b, 4, 100, fibtracker.EventStarted)
	refusedWith(t, err, "unregistered torrent")

	// A bad line refuses the list, and the one before stays.
	if err := os.WriteFile(file, []byte(c.String()+"\nnot a hash\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.Apply(load(t, js, "").BitTorrent.Tracker); err == nil || !strings.Contains(err.Error(), "allow.txt:2") {
		t.Fatalf("a bad allow file: %v", err)
	}
	mustAnnounce(t, cl, hu, c, 5, 100, fibtracker.EventStarted)
	if st, _ := r.Stats(); st.Allowed != 2 || st.Refused["unregistered"] < 3 {
		t.Fatalf("stats %+v", st)
	}
	// A file that is missing refuses the list too.
	if err := r.Apply(load(t, `{"allow_files": ["`+filepath.Join(dir, "missing")+`"]}`, "").BitTorrent.Tracker); err == nil {
		t.Fatal("a missing allow file was taken")
	}
}

// TestPrivatePasskeys has a private tracker take the passkeys of its
// media_auth policy, users' and tokens', in the path and the query, over
// HTTP and UDP, and refuse unknown ones and torrents a grant lacks.
func TestPrivatePasskeys(t *testing.T) {
	e := startEngine(t)
	a, b := hashOf("private a"), hashOf("private b")
	auth := `"media_auth": [{"name": "bt", "secret": "0123456789abcdef0123456789",
		"users": [{"name": "alice", "password": "alice-password-1", "play": ["bittorrent/*"]},
		          {"name": "bob", "password": "bob-password-123", "play": ["live/*"]}],
		"tokens": [{"name": "all", "token": "token-all-0123456789", "play": ["bittorrent/*"]},
		           {"name": "one", "token": "token-one-0123456789", "play": ["` + Stream(a) + `"]}]}]`
	r := startRole(t, e, load(t, `{"listen": "127.0.0.1:0", "auth": "bt"}`, auth))
	base := serveHTTP(t, e)
	cl := newClient(t, e, time.Second, 8)

	alice, err := r.Passkey("alice")
	if err != nil || len(alice) != 32 {
		t.Fatalf("alice's passkey %q %v", alice, err)
	}
	if _, err := r.Passkey("nobody"); err == nil {
		t.Fatal("a passkey for nobody")
	}
	bob, _ := r.Passkey("bob")

	mustAnnounce(t, cl, base+"/"+alice+"/announce", a, 1, 100, fibtracker.EventStarted)
	mustAnnounce(t, cl, base+"/announce?passkey=token-all-0123456789", b, 2, 100, fibtracker.EventStarted)
	mustAnnounce(t, cl, base+"/token-one-0123456789/announce", a, 3, 100, fibtracker.EventStarted)
	mustAnnounce(t, cl, udpURL(r, "/"+alice+"/announce"), b, 4, 100, fibtracker.EventStarted)

	for _, c := range []struct{ url, want string }{
		{base + "/announce", "a passkey is required"},
		{base + "/0123456789abcdef0123456789abcdef/announce", "unknown passkey"},
		{base + "/announce?passkey=wrong", "unknown passkey"},
		{base + "/" + bob + "/announce", "may not use this torrent"},
		{base + "/token-one-0123456789/announce", "may not use this torrent"},
		{udpURL(r, "/announce"), "a passkey is required"},
		{udpURL(r, "/wrong-passkey/announce"), "unknown passkey"},
	} {
		_, err := announce(t, cl, c.url, b, 9, 100, fibtracker.EventStarted)
		refusedWith(t, err, c.want)
	}
	// Scrapes too.
	files, err := cl.Scrape(ctx(t), base+"/"+alice+"/announce", []metainfo.InfoHash{a, b})
	if err != nil || files[a].Incomplete != 2 || files[b].Incomplete != 2 {
		t.Fatalf("scrape %v %v", files, err)
	}
	_, err = cl.Scrape(ctx(t), base+"/"+bob+"/announce", []metainfo.InfoHash{a})
	refusedWith(t, err, "may not use this torrent")
	if st, _ := r.Stats(); !st.Private || st.Refused["passkey"] != 8 || st.Announces != 4 {
		t.Fatalf("stats %+v", st)
	}

	// Public again, by a reload in place: no passkey needed.
	if err := r.Apply(load(t, `{"listen": "127.0.0.1:0"}`, auth).BitTorrent.Tracker); err != nil {
		t.Fatal(err)
	}
	mustAnnounce(t, cl, base+"/announce", b, 9, 100, fibtracker.EventStarted)
	if _, err := r.Passkey("alice"); err == nil {
		t.Fatal("a public tracker handed out a passkey")
	}
}

// TestPrivateWithoutPolicy fails closed while the policy is not loaded.
func TestPrivateWithoutPolicy(t *testing.T) {
	e := startEngine(t)
	c := load(t, `{"auth": "bt"}`, `"media_auth": [{"name": "bt", "tokens": [{"token": "token-all-0123456789", "play": ["*"]}]}]`)
	// The role, but not media_auth's policies.
	r := New(&app.Host{ID: ID, Engine: e, Upstream: e})
	if err := r.Apply(c.BitTorrent.Tracker); err != nil {
		t.Fatal(err)
	}
	defer r.Stop(context.Background())
	cl := newClient(t, e, time.Second, 8)
	u := serveHTTP(t, e) + "/announce?passkey=token-all-0123456789"
	_, err := announce(t, cl, u, hashOf("x"), 1, 100, fibtracker.EventStarted)
	refusedWith(t, err, "not available")

	// The policy comes, as media_auth applies after the role: found at
	// the next check. Removed, and made again: found again.
	if err := mediaauth.Apply(e, c.MediaAuth); err != nil {
		t.Fatal(err)
	}
	defer mediaauth.Remove(e)
	mustAnnounce(t, cl, u, hashOf("x"), 1, 100, fibtracker.EventStarted)
	mediaauth.Remove(e)
	_, err = announce(t, cl, u, hashOf("x"), 1, 100, fibtracker.EventStarted)
	refusedWith(t, err, "not available")
	if err := mediaauth.Apply(e, c.MediaAuth); err != nil {
		t.Fatal(err)
	}
	mustAnnounce(t, cl, u, hashOf("x"), 1, 100, fibtracker.EventStarted)
}

// TestRedact keeps passkeys out of the RequestURI the access log writes.
func TestRedact(t *testing.T) {
	for _, c := range []struct {
		in, want string
		path     bool
	}{
		{"/abc/announce?info_hash=x", "/REDACTED/announce?info_hash=x", true},
		{"/t/abc/scrape", "/t/REDACTED/scrape", true},
		{"/abc/announce?info_hash=x", "/abc/announce?info_hash=x", false},
		{"/announce?info_hash=x&passkey=abc&port=1", "/announce?info_hash=x&passkey=REDACTED&port=1", false},
		{"/announce", "/announce", true},
		{"/x/other", "/x/other", true},
	} {
		got, _ := redacted(c.in, c.path)
		if got != c.want {
			t.Errorf("redacted(%q, %v) = %q, want %q", c.in, c.path, got, c.want)
		}
	}
}

// TestConsoleRoutes reads the statistics through the role's console.
func TestConsoleRoutes(t *testing.T) {
	e := startEngine(t)
	h := hashOf("console")
	js := `{"allow": ["` + h.String() + `"], "auth": "bt", "console": {"address": "127.0.0.1:0", "users": [{"username": "admin", "password_hash": "` + hashPassword(t, "console-password") + `"}]}}`
	auth := `"media_auth": [{"name": "bt", "secret": "0123456789abcdef0123456789", "users": [{"name": "alice", "password": "alice-password-1", "play": ["*"]}]}]`
	r := startRole(t, e, load(t, js, auth))
	cl := newClient(t, e, time.Second, 8)
	pk, _ := r.Passkey("alice")
	mustAnnounce(t, cl, serveHTTP(t, e)+"/"+pk+"/announce", h, 1, 0, fibtracker.EventStarted)

	get := login(t, r.console.URL(), "admin", "console-password")
	for path, want := range map[string]string{
		"/api/stats":                  `"announces":1`,
		"/api/torrents":               `"seeders":1`,
		"/api/torrents/" + h.String(): `"tracked":true`,
		"/api/torrents/zz":            "not an info hash",
		"/api/passkey?user=alice":     pk,
		"/api/passkey?user=bob":       "no user",
	} {
		if body := get(path); !strings.Contains(body, want) {
			t.Errorf("%s: %s, want %q in it", path, body, want)
		}
	}
}

// hashPassword returns pw's bcrypt hash, for a console user.
func hashPassword(t *testing.T, pw string) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return string(h)
}

// login logs in to the console at base and returns a getter of its paths'
// bodies.
func login(t *testing.T, base, user, pw string) func(path string) string {
	t.Helper()
	base = strings.TrimSuffix(base, "/")
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar, Timeout: hang}
	res, err := hc.Post(base+"/api/login", "application/json", strings.NewReader(`{"username":"`+user+`","password":"`+pw+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if body := readAll(t, res); res.StatusCode != http.StatusOK {
		t.Fatalf("login: %d %s", res.StatusCode, body)
	}
	return func(path string) string {
		res, err := hc.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		return readAll(t, res)
	}
}

// readAll reads and closes a response's body.
func readAll(t *testing.T, res *http.Response) string {
	t.Helper()
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
