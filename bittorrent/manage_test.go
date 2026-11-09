package bittorrent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fib"
	fibbt "github.com/lesismal/fib/bittorrent"
	"github.com/lesismal/fib/bittorrent/metainfo"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/medialib"
)

// memberIn loads the config of a bittorrent.client member js, with top,
// more top-level fields, beside it.
func memberIn(t *testing.T, top, js string) *config.BitTorrentClient {
	t.Helper()
	s := `{"bittorrent": {"client": ` + js + `}`
	if top != "" {
		s += ", " + top
	}
	c, err := config.Load([]byte(s + "}"))
	if err != nil {
		t.Fatal(err)
	}
	return c.BitTorrent.Client
}

// startRoleWith starts a role on e with member m, after setup has changed
// its clock, disk or tick, and stops it when the test ends.
func startRoleWith(t *testing.T, e *fib.Engine, m *config.BitTorrentClient, setup func(*Role)) *Role {
	t.Helper()
	r := New(&app.Host{ID: ID, Engine: e, Upstream: e})
	r.tick = 10 * time.Millisecond
	if setup != nil {
		setup(r)
	}
	if err := r.Apply(m); err != nil {
		r.Stop(context.Background())
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Stop(context.Background()) })
	return r
}

// get reports the torrent id of r, failing the test when it cannot.
func get(t *testing.T, r *Role, id string) Status {
	t.Helper()
	s, err := r.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// seedMore has the seeder s seed the torrents of more files in src.
func seedMore(t *testing.T, s *Role, src string, names ...string) ([][]byte, [][]byte, []string) {
	t.Helper()
	var torrents, datas [][]byte
	var ids []string
	for i, name := range names {
		torrent, data := payload(t, src, name, 128<<10+i*4096)
		st, err := s.Add(AddRequest{Torrent: torrent})
		if err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the seeder to have "+name, func() bool {
			tor, err := s.Torrent(st.ID)
			return err == nil && tor.HaveAll()
		})
		torrents, datas, ids = append(torrents, torrent), append(datas, data), append(ids, st.ID)
	}
	return torrents, datas, ids
}

// TestCategoriesAndLimits adds torrents in categories: each downloads into
// its category's directory with the category's limits under its own, both
// changing in place by a reload or the API, and kept across a restart.
func TestCategoriesAndLimits(t *testing.T) {
	e := startEngine(t)
	src := t.TempDir()
	torrent, data := payload(t, src, "cat.bin", 256<<10)
	s, id := seeder(t, e, src, torrent, "")
	ldata, ldl, abs := t.TempDir(), t.TempDir(), t.TempDir()
	cats := func(films string) string {
		return `"categories": [{"name": "films", "limits": ` + films + `}, {"name": "abs", "dir": ` + strconv.Quote(abs) + `}, {"name": "rel", "dir": "sub/dir"}]`
	}
	l := startRoleWith(t, e, member(t, clientJSON(ldata, ldl, cats(`{"upload_rate": 1000, "seed_ratio": 5}`))), nil)

	if _, err := l.Add(AddRequest{Torrent: torrent, Category: "nope"}); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("an unknown category: %v", err)
	}
	st, err := l.Add(AddRequest{Torrent: torrent, Category: "films", Limits: &TorrentLimits{UploadRate: 500, MaxConns: 7},
		Peers: []string{peerAddr(s)}})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(ldl, "films"); st.Dir != want || st.Category != "films" {
		t.Fatalf("dir %q category %q, want %q films", st.Dir, st.Category, want)
	}
	tor, err := l.Torrent(id)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := tor.Limits(), (fibbt.TorrentLimits{UploadRate: 500, MaxConns: 7, SeedRatio: 5}); got != want {
		t.Fatalf("limits %+v, want %+v", got, want)
	}
	waitDone(t, l, id)
	sameFile(t, filepath.Join(ldl, "films", "cat.bin"), data)

	// A reload changes the category's limits in place.
	if err := l.Apply(member(t, clientJSON(ldata, ldl, cats(`{"download_rate": 3000, "seed_ratio": 6}`)))); err != nil {
		t.Fatal(err)
	}
	if got, want := tor.Limits(), (fibbt.TorrentLimits{UploadRate: 500, DownloadRate: 3000, MaxConns: 7, SeedRatio: 6}); got != want {
		t.Fatalf("limits after a reload %+v, want %+v", got, want)
	}
	// The torrent's own limits, cleared, leave the category's.
	if err := l.SetTorrentLimits(id, TorrentLimits{}); err != nil {
		t.Fatal(err)
	}
	if got, want := tor.Limits(), (fibbt.TorrentLimits{DownloadRate: 3000, SeedRatio: 6}); got != want {
		t.Fatalf("limits %+v, want %+v", got, want)
	}
	if err := l.SetTorrentLimits(id, TorrentLimits{SeedRatio: -1, SeedTime: config.Duration(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := l.SetCategory(id, "nope"); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("an unknown category: %v", err)
	}
	if err := l.SetCategory(id, "abs"); err != nil {
		t.Fatal(err)
	}
	if got, want := tor.Limits(), (fibbt.TorrentLimits{SeedRatio: -1, SeedTime: time.Hour}); got != want {
		t.Fatalf("limits in abs %+v, want %+v", got, want)
	}
	gs := get(t, l, id)
	if gs.Category != "abs" || gs.Limits == nil || gs.Limits.SeedRatio != -1 || gs.Effective == nil || gs.Dir != filepath.Join(ldl, "films") {
		t.Fatalf("status %+v", gs)
	}

	// The category and the limits are kept across a restart.
	l.Stop(context.Background())
	l2 := startRoleWith(t, e, member(t, clientJSON(ldata, ldl, cats(`{}`))), nil)
	gs = get(t, l2, id)
	if gs.Category != "abs" || gs.Limits == nil || gs.Limits.SeedTime.Std() != time.Hour || !gs.Complete {
		t.Fatalf("after a restart %+v", gs)
	}
	tor2, err := l2.Torrent(id)
	if err != nil {
		t.Fatal(err)
	}
	if got := tor2.Limits(); got.SeedRatio != -1 || got.SeedTime != time.Hour {
		t.Fatalf("limits after a restart %+v", got)
	}

	// A relative category directory stays below download_dir; one
	// leading out of it, and other mistakes, are refused by the config.
	other, _ := payload(t, t.TempDir(), "rel.bin", 64<<10)
	if st, err := l2.Add(AddRequest{Torrent: other, Category: "rel", Paused: true}); err != nil || st.Dir != filepath.Join(ldl, "sub", "dir") {
		t.Fatalf("rel: %+v %v", st, err)
	}
}

// TestCategoryConfigChecks refuses the categories, alt_speed and watch
// settings that cannot work.
func TestCategoryConfigChecks(t *testing.T) {
	base := `"listen": "127.0.0.1:0", "data_dir": "/tmp/x"`
	for name, js := range map[string]string{
		"bad name":         `"categories": [{"name": "a b"}]`,
		"twice":            `"categories": [{"name": "a"}, {"name": "a"}]`,
		"out of downloads": `"categories": [{"name": "a", "dir": "../x"}]`,
		"no library":       `"categories": [{"name": "a", "media_library": "none"}]`,
		"bad day":          `"alt_speed": {"schedules": [{"days": ["someday"], "from": "01:00", "to": "02:00"}]}`,
		"bad time":         `"alt_speed": {"schedules": [{"from": "25:00", "to": "02:00"}]}`,
		"24:00 from":       `"alt_speed": {"schedules": [{"from": "24:00", "to": "02:00"}]}`,
		"empty range":      `"alt_speed": {"schedules": [{"from": "02:00", "to": "02:00"}]}`,
		"bad zone":         `"alt_speed": {"time_zone": "Mars/Base"}`,
		"negative watch":   `"watch_interval": "-1s"`,
		"negative url":     `"url_timeout": "-1s"`,
		"watch bound":      `"watch_max_files": 10001`,
	} {
		if _, err := config.Load([]byte(`{"bittorrent": {"client": {` + base + `, ` + js + `}}}`)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	ok := `"categories": [{"name": "a", "dir": "x/y", "media_library": "lib"}], "alt_speed": {"schedules": [{"days": ["weekdays", "sun"], "from": "22:00", "to": "06:00"}, {"from": "0:00", "to": "24:00"}]}`
	if _, err := config.Load([]byte(`{"media_library": [{"name": "lib", "roots": ["/tmp/lib"]}], "bittorrent": {"client": {` + base + `, ` + ok + `}}}`)); err != nil {
		t.Fatal(err)
	}
}

// TestQueueDownloads queues downloads past max_active_downloads: the next
// starts when one finishes, a queue position moves one ahead, and the order
// is kept across a restart.
func TestQueueDownloads(t *testing.T) {
	e := startEngine(t)
	src := t.TempDir()
	first, _ := payload(t, src, "q0.bin", 128<<10)
	s, _ := seeder(t, e, src, first, "")
	torrents, datas, ids := seedMore(t, s, src, "q1.bin", "q2.bin", "q3.bin")
	ldata, ldl := t.TempDir(), t.TempDir()
	slow := `"limits": {"max_active_downloads": 1, "download_rate": 16384}`
	l := startRoleWith(t, e, member(t, clientJSON(ldata, ldl, slow)), nil)
	for _, torrent := range torrents {
		if _, err := l.Add(AddRequest{Torrent: torrent, Peers: []string{peerAddr(s)}}); err != nil {
			t.Fatal(err)
		}
	}
	if a, b, c := get(t, l, ids[0]), get(t, l, ids[1]), get(t, l, ids[2]); a.Held != "" || b.Held != "queued" || b.State != "queued" || c.Held != "queued" {
		t.Fatalf("held %q %q %q, states %q %q %q", a.Held, b.Held, c.Held, a.State, b.State, c.State)
	}
	// The last moves to the front: it runs, the first waits.
	if err := l.SetQueuePosition(ids[2], 0); err != nil {
		t.Fatal(err)
	}
	if a, c := get(t, l, ids[0]), get(t, l, ids[2]); a.Held != "queued" || c.Held != "" || c.QueuePosition != 0 || a.QueuePosition != 1 {
		t.Fatalf("after the move: first %+v, last %+v", a, c)
	}
	if err := l.SetQueuePosition("00", 0); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := l.SetQueuePosition(ids[2], -1); !errors.Is(err, ErrBadRequest) {
		t.Fatal(err)
	}
	if tor, _ := l.Torrent(ids[1]); tor.State() != fibbt.StatePaused {
		t.Fatalf("a queued torrent is %v", tor.State())
	}
	// The order is kept across a restart.
	l.Stop(context.Background())
	l = startRoleWith(t, e, member(t, clientJSON(ldata, ldl, slow)), nil)
	list := l.List()
	if len(list) != 3 || list[0].ID != ids[2] || list[1].ID != ids[0] || list[2].ID != ids[1] {
		t.Fatalf("the order after a restart: %v %v %v", list[0].ID, list[1].ID, list[2].ID)
	}
	if list[0].Held != "" || list[1].Held != "queued" || list[2].Held != "queued" {
		t.Fatalf("held after a restart: %q %q %q", list[0].Held, list[1].Held, list[2].Held)
	}
	// Unthrottled, each finishing starts the next, in order.
	if err := l.Apply(member(t, clientJSON(ldata, ldl, `"limits": {"max_active_downloads": 1}`))); err != nil {
		t.Fatal(err)
	}
	for _, i := range []int{2, 0, 1} {
		tor, err := l.Torrent(ids[i])
		if err != nil {
			t.Fatal(err)
		}
		tor.AddPeers(peerAddr(s))
		waitFor(t, "torrent "+strconv.Itoa(i)+" to start", func() bool { return get(t, l, ids[i]).Held == "" })
		waitDone(t, l, ids[i])
		sameFile(t, filepath.Join(ldl, "q"+strconv.Itoa(i+1)+".bin"), datas[i])
	}
}

// TestQueueSeeds keeps one seed running past max_active_seeds; pausing it
// starts the next.
func TestQueueSeeds(t *testing.T) {
	e := startEngine(t)
	src := t.TempDir()
	a, _ := payload(t, src, "s0.bin", 64<<10)
	b, _ := payload(t, src, "s1.bin", 64<<10+4096)
	r := startRoleWith(t, e, member(t, clientJSON(t.TempDir(), src, `"limits": {"max_active_seeds": 1}`)), nil)
	sa, err := r.Add(AddRequest{Torrent: a})
	if err != nil {
		t.Fatal(err)
	}
	sb, err := r.Add(AddRequest{Torrent: b})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "one seed running and one queued", func() bool {
		x, y := get(t, r, sa.ID), get(t, r, sb.ID)
		return x.State == "seeding" && x.Held == "" && y.Held == "queued"
	})
	if err := r.Pause(sa.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the queued seed to start", func() bool {
		y := get(t, r, sb.ID)
		return y.State == "seeding" && y.Held == ""
	})
	// Resumed, the first waits for its turn behind the running one,
	// which is behind it in the list: the list's order wins.
	if err := r.Resume(sa.ID); err != nil {
		t.Fatal(err)
	}
	if x, y := get(t, r, sa.ID), get(t, r, sb.ID); x.Held != "" || y.Held != "queued" {
		t.Fatalf("after resume: %q %q", x.Held, y.Held)
	}
	if err := r.Remove(sa.ID, false); err != nil {
		t.Fatal(err)
	}
	if y := get(t, r, sb.ID); y.Held != "" {
		t.Fatalf("after remove: %q", y.Held)
	}
}

// TestDiskShort holds a download that would leave less than min_free_bytes
// free, starts it once the space is there, and pauses it when the space
// runs out while it downloads.
func TestDiskShort(t *testing.T) {
	e := startEngine(t)
	src := t.TempDir()
	torrent, data := payload(t, src, "disk.bin", 2<<20)
	s, id := seeder(t, e, src, torrent, "")
	var free atomic.Int64
	free.Store(1 << 20)
	ldata, ldl := t.TempDir(), t.TempDir()
	js := func(rate int) string {
		return clientJSON(ldata, ldl, `"limits": {"min_free_bytes": 1048576, "download_rate": `+strconv.Itoa(rate)+`}`)
	}
	l := startRoleWith(t, e, member(t, js(65536)), func(r *Role) {
		r.diskFree = func(string) (uint64, bool) { return uint64(free.Load()), true }
	})
	st, err := l.Add(AddRequest{Torrent: torrent, Peers: []string{peerAddr(s)}})
	if err != nil {
		t.Fatal(err)
	}
	if st.Held != "disk" || !strings.Contains(st.Error, "short of disk space") || !strings.Contains(st.Error, "min_free_bytes") {
		t.Fatalf("short of space: %+v", st)
	}
	free.Store(100 << 20)
	waitFor(t, "the download to start", func() bool { return get(t, l, id).Held == "" })
	tor, err := l.Torrent(id)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a piece", func() bool { return tor.Stats().PiecesHave > 0 })
	free.Store(512 << 10)
	start := time.Now()
	var last Status
	logged := false
	waitFor(t, "the download to pause", func() bool {
		last = get(t, l, id)
		if time.Since(start) > 20*time.Second && !logged {
			logged = true
			t.Logf("%+v", last)
		}
		return last.Held == "disk" && strings.Contains(last.Error, "short of disk space") && last.State == "paused"
	})
	free.Store(100 << 20)
	if err := l.Apply(member(t, js(0))); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the download to start again", func() bool { return get(t, l, id).Held == "" })
	tor.AddPeers(peerAddr(s))
	waitDone(t, l, id)
	sameFile(t, filepath.Join(ldl, "disk.bin"), data)
	// Seeding takes no space: it is not held, whatever is free.
	free.Store(0)
	time.Sleep(50 * time.Millisecond)
	if s := get(t, l, id); s.Held != "" || s.State != "seeding" {
		t.Fatalf("a seed short of space: %+v", s)
	}
}

// TestMediaLibraryRescan completes a download of a category naming a
// media library whose root is its directory: the file appears in the
// library's catalog, scanned once for the completion, not again after a
// restart.
func TestMediaLibraryRescan(t *testing.T) {
	e := startEngine(t)
	src := t.TempDir()
	torrent, _ := payload(t, src, "show.mp4", 256<<10)
	s, id := seeder(t, e, src, torrent, "")
	ldata, ldl, root := t.TempDir(), t.TempDir(), t.TempDir()
	top := `"media_library": [{"name": "lib", "roots": [` + strconv.Quote(root) + `], "scan_interval": "1h"}]`
	if err := medialib.Apply(e, []config.MediaLibrary{{Name: "lib", Roots: []string{root}, ScanInterval: config.Duration(time.Hour)}}, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { medialib.Remove(e) })
	lib := medialib.Lookup(e, "lib")
	waitFor(t, "the first scan", func() bool { return lib.Status().Scans > 0 })
	js := clientJSON(ldata, ldl, `"categories": [{"name": "tv", "dir": `+strconv.Quote(root)+`, "media_library": "lib"}]`)
	var scans atomic.Int32
	setup := func(r *Role) {
		r.rescan = func(name string) bool {
			if name == "lib" {
				scans.Add(1)
			}
			return r.rescanLibrary(name)
		}
	}
	l := startRoleWith(t, e, memberIn(t, top, js), setup)
	if _, err := l.Add(AddRequest{Torrent: torrent, Category: "tv", Peers: []string{peerAddr(s)}}); err != nil {
		t.Fatal(err)
	}
	waitDone(t, l, id)
	waitFor(t, "the file in the catalog", func() bool {
		for _, it := range lib.Catalog().Items {
			if strings.Contains(it.Path, "show") {
				return true
			}
		}
		return false
	})
	if n := scans.Load(); n != 1 {
		t.Fatalf("%d scans asked for", n)
	}
	if !get(t, l, id).Complete {
		t.Fatal("not complete")
	}
	l.Stop(context.Background())
	l2 := startRoleWith(t, e, memberIn(t, top, js), setup)
	waitFor(t, "the torrent to seed again", func() bool { return get(t, l2, id).State == "seeding" })
	time.Sleep(50 * time.Millisecond)
	if n := scans.Load(); n != 1 {
		t.Fatalf("%d scans asked for after a restart", n)
	}
}

// fakeClock is a clock tests set.
type fakeClock struct{ ns atomic.Int64 }

func (c *fakeClock) set(t time.Time) { c.ns.Store(t.UnixNano()) }
func (c *fakeClock) now() time.Time  { return time.Unix(0, c.ns.Load()) }

func TestScheduleHolds(t *testing.T) {
	a := &config.BitTorrentAltSpeed{Schedules: []config.BitTorrentSchedule{
		{Days: []string{"mon"}, From: "22:00", To: "06:00"},
		{Days: []string{"weekend"}, From: "09:30", To: "24:00"},
	}}
	sh, _ := time.LoadLocation("Asia/Shanghai")
	// 2026-11-09 is a Monday.
	day := func(d, h, m int) time.Time { return time.Date(2026, 11, 9+d, h, m, 0, 0, time.UTC) }
	for _, tt := range []struct {
		at   time.Time
		loc  *time.Location
		want bool
	}{
		{day(0, 21, 59), time.UTC, false},
		{day(0, 22, 0), time.UTC, true},
		{day(0, 23, 59), time.UTC, true},
		{day(1, 0, 0), time.UTC, true},
		{day(1, 5, 59), time.UTC, true},
		{day(1, 6, 0), time.UTC, false},
		{day(1, 22, 30), time.UTC, false}, // Tuesday is not listed
		{day(-1, 23, 0), time.UTC, true},  // Sunday 23:00, the weekend range
		{day(0, 0, 30), time.UTC, false},  // Monday 00:30: Sunday's range ended at 24:00
		{day(5, 9, 29), time.UTC, false},
		{day(5, 9, 30), time.UTC, true},
		{day(0, 14, 0), sh, true},   // 22:00 in Shanghai
		{day(0, 13, 59), sh, false}, // 21:59 in Shanghai
	} {
		if got := scheduleHolds(a, tt.loc, tt.at); got != tt.want {
			t.Errorf("at %v in %v: %v, want %v", tt.at, tt.loc, got, tt.want)
		}
	}
}

// TestAltSpeedSchedule switches the client's rates by alt_speed's schedule
// on a fake clock, and by the API, whose choice survives a restart.
func TestAltSpeedSchedule(t *testing.T) {
	e := startEngine(t)
	clock := &fakeClock{}
	day := func(d, h, m int) time.Time { return time.Date(2026, 11, 9+d, h, m, 0, 0, time.UTC) }
	clock.set(day(0, 21, 59))
	ldata := t.TempDir()
	js := clientJSON(ldata, "", `"limits": {"upload_rate": 1000000, "download_rate": 2000000},
		"alt_speed": {"upload_rate": 1000, "download_rate": 2000, "time_zone": "utc",
			"schedules": [{"days": ["mon"], "from": "22:00", "to": "06:00"}]}`)
	setup := func(r *Role) { r.now = clock.now }
	r := startRoleWith(t, e, member(t, js), setup)
	rates := func(up, down int) func() bool {
		return func() bool {
			l := r.Client().Limits()
			return l.UploadRate == up && l.DownloadRate == down
		}
	}
	waitFor(t, "the normal rates", rates(1000000, 2000000))
	for _, step := range []struct {
		at  time.Time
		alt bool
	}{{day(0, 22, 0), true}, {day(1, 5, 59), true}, {day(1, 6, 0), false}, {day(1, 22, 0), false}, {day(7, 23, 0), true}} {
		clock.set(step.at)
		if step.alt {
			waitFor(t, "the alternative rates at "+step.at.String(), rates(1000, 2000))
		} else {
			waitFor(t, "the normal rates at "+step.at.String(), rates(1000000, 2000000))
		}
	}
	if st, err := r.AltSpeed(); err != nil || !st.Active || st.Mode != AltSpeedAuto || !st.Configured || st.UploadRate != 1000 {
		t.Fatalf("%+v %v", st, err)
	}
	if err := r.SetAltSpeed(AltSpeedOff); err != nil {
		t.Fatal(err)
	}
	if !rates(1000000, 2000000)() {
		t.Fatal("off: not the normal rates")
	}
	if err := r.SetAltSpeed("sometimes"); !errors.Is(err, ErrBadRequest) {
		t.Fatal(err)
	}
	// The choice survives a restart.
	r.Stop(context.Background())
	r = startRoleWith(t, e, member(t, js), setup)
	if st, _ := r.AltSpeed(); st.Mode != AltSpeedOff || st.Active {
		t.Fatalf("after a restart %+v", st)
	}
	if err := r.SetAltSpeed(AltSpeedAuto); err != nil {
		t.Fatal(err)
	}
	if !rates(1000, 2000)() {
		t.Fatal("auto: not the alternative rates")
	}
	clock.set(day(2, 12, 0))
	if err := r.SetAltSpeed(AltSpeedOn); err != nil {
		t.Fatal(err)
	}
	if !rates(1000, 2000)() {
		t.Fatal("on: not the alternative rates")
	}
	// A reload without alt_speed leaves the normal rates.
	if err := r.Apply(member(t, clientJSON(ldata, "", `"limits": {"upload_rate": 1000000, "download_rate": 2000000}`))); err != nil {
		t.Fatal(err)
	}
	if !rates(1000000, 2000000)() {
		t.Fatal("without alt_speed: not the normal rates")
	}
}

// TestP2PDenyFileClosesPeer denies the seeder through a PeerGuardian .p2p
// deny file: the role refilters and the connected peer is closed.
func TestP2PDenyFileClosesPeer(t *testing.T) {
	e := startEngine(t)
	src := t.TempDir()
	torrent, _ := payload(t, src, "p2p.bin", 2<<20)
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
	list := filepath.Join(t.TempDir(), "level1.p2p")
	if err := os.WriteFile(list, []byte("# list\nLoopback, Inc:127.0.0.1-127.0.0.1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := l.Apply(member(t, clientJSON(ldata, ldl, `"limits": {"download_rate": 65536}, "access": {"deny_files": [`+strconv.Quote(list)+`]}`))); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the denied peer to be closed", func() bool { return len(tor.Peers()) == 0 })
	// A .dat line of a level above 127 does not deny.
	dat := filepath.Join(t.TempDir(), "ipfilter.dat")
	if err := os.WriteFile(dat, []byte("127.000.000.001 - 127.000.000.001 , 200 , not blocked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := l.Apply(member(t, clientJSON(ldata, ldl, `"access": {"deny_files": [`+strconv.Quote(dat)+`]}`))); err != nil {
		t.Fatal(err)
	}
	tor.AddPeers(peerAddr(s))
	waitDone(t, l, id)
}

// magnetOf returns the magnet link of torrent.
func magnetOf(t *testing.T, torrent []byte) string {
	t.Helper()
	mi, err := metainfo.Parse(torrent)
	if err != nil {
		t.Fatal(err)
	}
	return mi.Magnet().String()
}
