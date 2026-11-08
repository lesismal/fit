package bittorrent

import (
	"context"
	"errors"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fib/bittorrent/metainfo"

	"github.com/lesismal/fit/app"
)

// writeData writes n random bytes of seed as dir/rel and returns them.
func writeData(t *testing.T, dir, rel string, n int, seed uint64) []byte {
	t.Helper()
	data := make([]byte, n)
	rnd := rand.New(rand.NewPCG(seed, uint64(n)))
	for i := range data {
		data[i] = byte(rnd.Uint32())
	}
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return data
}

// publishJSON is a member publishing the entries entries, in JSON.
func publishJSON(dataDir, entries string) string {
	return clientJSON(dataDir, "", `"publish": [`+entries+`]`)
}

// entryJSON is a publish entry name of dir with a short check interval;
// extra is more of its fields.
func entryJSON(name, dir, extra string) string {
	s := `{"name": ` + strconv.Quote(name) + `, "dir": ` + strconv.Quote(dir) + `, "check_interval": "50ms"`
	if extra != "" {
		s += ", " + extra
	}
	return s + "}"
}

// entry returns the status of the publish entry name of r.
func entry(t *testing.T, r *Role, name string) PublishStatus {
	t.Helper()
	l := r.Publishing([]string{name})
	if len(l) != 1 {
		t.Fatalf("publishing %v", l)
	}
	return l[0]
}

// waitSeeding waits for the entry name of r to seed n torrents and have no
// file pending, and returns its status.
func waitSeeding(t *testing.T, r *Role, name string, n int) PublishStatus {
	t.Helper()
	var st PublishStatus
	waitFor(t, "the entry to seed "+strconv.Itoa(n)+" torrents", func() bool {
		st = entry(t, r, name)
		if len(st.Torrents) != n || st.Pending != 0 || st.Scanned.IsZero() {
			return false
		}
		for _, tr := range st.Torrents {
			if tr.State != "seeding" {
				return false
			}
		}
		return true
	})
	return st
}

// byPath returns the torrent of path in st.
func byPath(t *testing.T, st PublishStatus, path string) PublishedTorrent {
	t.Helper()
	for _, tr := range st.Torrents {
		if tr.Path == path {
			return tr
		}
	}
	t.Fatalf("no torrent of %q in %+v", path, st)
	return PublishedTorrent{}
}

// leech downloads the published torrent id of r from r's seed alone, into
// a role of its own, and returns where.
func leech(t *testing.T, r *Role, id string) (*Role, string) {
	t.Helper()
	raw, _, _, ok := r.PublishedTorrent(nil, id)
	if !ok {
		t.Fatalf("no torrent %s", id)
	}
	dl := t.TempDir()
	l := startRole(t, r.engine, clientJSON(t.TempDir(), dl, ""), nil)
	s, err := l.Add(AddRequest{Torrent: raw, Peers: []string{peerAddr(r)}})
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, l, s.ID)
	return l, dl
}

// TestPublishSeedAlone publishes a directory a torrent a file and
// downloads each from the role's seed alone, the published files left as
// they were.
func TestPublishSeedAlone(t *testing.T) {
	e := startEngine(t)
	dir := t.TempDir()
	a := writeData(t, dir, "a.bin", 300<<10+7, 1)
	b := writeData(t, dir, "sub/b b.bin", 70<<10, 2)
	r := startRole(t, e, publishJSON(t.TempDir(), entryJSON("pub", dir, "")), nil)
	st := waitSeeding(t, r, "pub", 2)
	ta, tb := byPath(t, st, "a.bin"), byPath(t, st, "sub/b b.bin")
	if ta.Name != "a.bin" || tb.Name != "b b.bin" || ta.Size != int64(len(a)) || ta.InfoHash == "" || ta.InfoHashV2 == "" ||
		!strings.HasPrefix(ta.Magnet, "magnet:?") {
		t.Fatalf("torrents %+v", st.Torrents)
	}
	// Hybrid, as published.
	raw, _, magnet, _ := r.PublishedTorrent([]string{"pub"}, ta.ID)
	mi, err := metainfo.Parse(raw)
	if err != nil || !mi.Info.HasV1() || !mi.Info.HasV2() || magnet != ta.Magnet {
		t.Fatalf("torrent %v %v", mi, err)
	}
	_, dl := leech(t, r, ta.ID)
	sameFile(t, filepath.Join(dl, "a.bin"), a)
	_, dl = leech(t, r, tb.ID)
	sameFile(t, filepath.Join(dl, "b b.bin"), b)
	sameFile(t, filepath.Join(dir, "a.bin"), a)
	sameFile(t, filepath.Join(dir, "sub", "b b.bin"), b)
	// The torrents are the role's own, not in its list.
	if l := r.List(); len(l) != 0 {
		t.Fatalf("listed %v", l)
	}
	if _, _, _, ok := r.PublishedTorrent([]string{"other"}, ta.ID); ok {
		t.Fatal("found in an entry it is not of")
	}
}

// TestPublishDir publishes a directory as one torrent and downloads it.
func TestPublishDir(t *testing.T) {
	e := startEngine(t)
	dir := filepath.Join(t.TempDir(), "share")
	a := writeData(t, dir, "x/a.bin", 100<<10+3, 3)
	b := writeData(t, dir, "z.bin", 40<<10, 4)
	writeData(t, dir, ".hidden", 10, 5)
	if err := os.Symlink(filepath.Join(dir, "z.bin"), filepath.Join(dir, "link.bin")); err != nil {
		t.Fatal(err)
	}
	r := startRole(t, e, publishJSON(t.TempDir(), entryJSON("d", dir, `"mode": "whole_dir"`)), nil)
	st := waitSeeding(t, r, "d", 1)
	tr := st.Torrents[0]
	if tr.Path != "" || tr.Name != "share" || tr.Files != 2 || tr.Size != int64(len(a)+len(b)) {
		t.Fatalf("torrent %+v", tr)
	}
	_, dl := leech(t, r, tr.ID)
	sameFile(t, filepath.Join(dl, "share", "x", "a.bin"), a)
	sameFile(t, filepath.Join(dl, "share", "z.bin"), b)
	for _, n := range []string{".hidden", "link.bin"} {
		if _, err := os.Lstat(filepath.Join(dl, "share", n)); err == nil {
			t.Fatalf("%s published", n)
		}
	}

	// A file added replaces the torrent.
	writeData(t, dir, "y.bin", 1000, 6)
	waitFor(t, "a new torrent of the directory", func() bool {
		st := entry(t, r, "d")
		return len(st.Torrents) == 1 && st.Torrents[0].ID != tr.ID && st.Torrents[0].Files == 3 && st.Torrents[0].State == "seeding"
	})
}

// TestPublishRestartWithoutHashing restarts a role, which takes its
// published torrents from data_dir, building none and reading none of
// their data.
func TestPublishRestartWithoutHashing(t *testing.T) {
	e := startEngine(t)
	dir, data := t.TempDir(), t.TempDir()
	writeData(t, dir, "a.bin", 200<<10, 7)
	writeData(t, dir, "b.bin", 50<<10, 8)
	js := publishJSON(data, entryJSON("pub", dir, ""))
	r := New(&app.Host{ID: ID, Engine: e, Upstream: e})
	if err := r.Apply(member(t, js)); err != nil {
		t.Fatal(err)
	}
	first := waitSeeding(t, r, "pub", 2)
	if n := r.pub.builds.Load(); n != 2 {
		t.Fatalf("%d builds", n)
	}
	r.Stop(context.Background())
	for _, tr := range first.Torrents {
		if _, err := os.Stat(filepath.Join(data, publishDir, "pub", tr.ID+".resume")); err != nil {
			t.Fatalf("no resume data: %v", err)
		}
	}

	rc := &readCounter{}
	r2 := startRole(t, e, js, rc)
	again := waitSeeding(t, r2, "pub", 2)
	if n := r2.pub.builds.Load(); n != 0 {
		t.Fatalf("%d builds after the restart", n)
	}
	if n := rc.total(); n != 0 {
		t.Fatalf("%d reads of the data after the restart", n)
	}
	for i := range again.Torrents {
		if again.Torrents[i].ID != first.Torrents[i].ID {
			t.Fatalf("torrents %+v, before %+v", again.Torrents, first.Torrents)
		}
	}

	// A reload of the entry's bounds starts it again, building nothing.
	if err := r2.Apply(member(t, publishJSON(data, entryJSON("pub", dir, `"max_files": 10`)))); err != nil {
		t.Fatal(err)
	}
	waitSeeding(t, r2, "pub", 2)
	if n := r2.pub.builds.Load(); n != 0 {
		t.Fatalf("%d builds after a reload", n)
	}

	// An entry taken out of the config stops its torrents and drops its
	// state, leaving the files.
	r2.mu.Lock()
	tor := r2.pub.entries["pub"].items["a.bin"].t
	r2.mu.Unlock()
	if err := r2.Apply(member(t, publishJSON(data, ""))); err != nil {
		t.Fatal(err)
	}
	select {
	case <-tor.Removed():
	case <-time.After(hang):
		t.Fatal("the torrent still runs")
	}
	if l := r2.Publishing(nil); len(l) != 0 {
		t.Fatalf("publishing %+v", l)
	}
	if _, err := os.Stat(filepath.Join(data, publishDir, "pub")); !os.IsNotExist(err) {
		t.Fatalf("its state is left: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.bin")); err != nil {
		t.Fatal(err)
	}
}

// TestPublishFollowsDisk changes, removes and adds files: a changed file
// gets a new torrent and the old one stops; a removed one's stops.
func TestPublishFollowsDisk(t *testing.T) {
	e := startEngine(t)
	dir := t.TempDir()
	writeData(t, dir, "a.bin", 64<<10, 9)
	writeData(t, dir, "b.bin", 64<<10, 10)
	r := startRole(t, e, publishJSON(t.TempDir(), entryJSON("pub", dir, "")), nil)
	st := waitSeeding(t, r, "pub", 2)
	old := byPath(t, st, "a.bin")
	oldTor, ok := r.Client().Torrent(mustHash(t, old.InfoHash))
	if !ok {
		t.Fatal("the old torrent is not in the client")
	}

	// Changed in place, the same size.
	a2 := writeData(t, dir, "a.bin", 64<<10, 11)
	var fresh PublishedTorrent
	waitFor(t, "a new torrent of the changed file", func() bool {
		st := entry(t, r, "pub")
		for _, tr := range st.Torrents {
			if tr.Path == "a.bin" && tr.ID != old.ID && tr.State == "seeding" {
				fresh = tr
				return true
			}
		}
		return false
	})
	select {
	case <-oldTor.Removed():
	case <-time.After(hang):
		t.Fatal("the old torrent still runs")
	}
	if _, _, _, ok := r.PublishedTorrent(nil, old.ID); ok {
		t.Fatal("the old torrent is still served")
	}
	_, dl := leech(t, r, fresh.ID)
	sameFile(t, filepath.Join(dl, "a.bin"), a2)

	// Removed, and added.
	b := byPath(t, entry(t, r, "pub"), "b.bin")
	if err := os.Remove(filepath.Join(dir, "b.bin")); err != nil {
		t.Fatal(err)
	}
	writeData(t, dir, "c.bin", 1000, 12)
	waitFor(t, "b.bin's torrent gone and c.bin's seeding", func() bool {
		st := entry(t, r, "pub")
		return len(st.Torrents) == 2 && slices.ContainsFunc(st.Torrents, func(tr PublishedTorrent) bool {
			return tr.Path == "c.bin" && tr.State == "seeding"
		}) && !slices.ContainsFunc(st.Torrents, func(tr PublishedTorrent) bool { return tr.Path == "b.bin" })
	})
	if _, ok := r.Client().Torrent(mustHash(t, b.InfoHash)); ok {
		t.Fatal("the removed file's torrent is still in the client")
	}
}

// mustHash parses a v1 info hash in hex.
func mustHash(t *testing.T, s string) [20]byte {
	t.Helper()
	b, ok := parseID(s)
	if !ok {
		t.Fatalf("hash %q", s)
	}
	return b
}

// TestPublishBounds checks max_files, max_total_bytes, include and
// exclude, empty files, and a directory past its bounds in mode whole_dir.
func TestPublishBounds(t *testing.T) {
	e := startEngine(t)
	dir := t.TempDir()
	for i, n := range []string{"1.iso", "2.iso", "3.iso", "4.txt", "skip/5.iso"} {
		writeData(t, dir, n, 20<<10, uint64(20+i))
	}
	writeData(t, dir, "empty.iso", 0, 30)
	r := startRole(t, e, publishJSON(t.TempDir(),
		entryJSON("files", dir, `"max_files": 2, "files": ["*.iso"], "exclude": ["skip/**"]`)+", "+
			entryJSON("bytes", dir, `"max_total_bytes": 50000, "piece_length": 65536, "files": ["*.iso"], "exclude": ["skip"]`)+", "+
			entryJSON("whole", dir, `"mode": "whole_dir", "max_files": 3`)), nil)
	st := waitSeeding(t, r, "files", 2)
	if byPath(t, st, "1.iso").ID == "" || byPath(t, st, "2.iso").ID == "" {
		t.Fatal()
	}
	if !problem(st, "over max_files") || !problem(st, "empty.iso: empty") {
		t.Fatalf("problems %v", st.Problems)
	}
	st = waitSeeding(t, r, "bytes", 2)
	if !problem(st, "over max_total_bytes") {
		t.Fatalf("problems %v", st.Problems)
	}
	waitFor(t, "the directory refused", func() bool {
		st := entry(t, r, "whole")
		return !st.Scanned.IsZero() && problem(st, "not published")
	})
	if st := entry(t, r, "whole"); len(st.Torrents) != 0 {
		t.Fatalf("published %+v", st.Torrents)
	}
}

func problem(st PublishStatus, s string) bool {
	return slices.ContainsFunc(st.Problems, func(p string) bool { return strings.Contains(p, s) })
}

// TestPublishDuplicatesAndErrors checks two files of one torrent, a
// directory that is not there, and a build that fails, which is not tried
// again until the file changes.
func TestPublishDuplicatesAndErrors(t *testing.T) {
	e := startEngine(t)
	dir := t.TempDir()
	a := writeData(t, dir, "x/same.bin", 30<<10, 40)
	if err := os.MkdirAll(filepath.Join(dir, "y"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "y", "same.bin"), a, 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "gone")
	r := startRole(t, e, publishJSON(t.TempDir(), entryJSON("dup", dir, "")+", "+entryJSON("gone", missing, "")), nil)
	waitFor(t, "the duplicate reported", func() bool {
		st := entry(t, r, "dup")
		if len(st.Torrents) != 2 {
			return false
		}
		y := byPath(t, st, "y/same.bin")
		return byPath(t, st, "x/same.bin").State == "seeding" && y.State == "error" && strings.Contains(y.Error, "x/same.bin")
	})
	waitFor(t, "the missing directory reported", func() bool { return entry(t, r, "gone").Error != "" })

	// The original removed, the duplicate seeds in its place.
	if err := os.Remove(filepath.Join(dir, "x", "same.bin")); err != nil {
		t.Fatal(err)
	}
	st := waitSeeding(t, r, "dup", 1)
	if byPath(t, st, "y/same.bin").Error != "" {
		t.Fatalf("%+v", st)
	}

	if os.Geteuid() == 0 {
		return // root reads a file whatever its mode
	}
	// A file that cannot be read fails its build, which is not tried
	// again until the file changes.
	p := filepath.Join(dir, "z.bin")
	writeData(t, dir, "z.bin", 1000, 41)
	if err := os.Chmod(p, 0); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the failed build reported", func() bool { return problem(entry(t, r, "dup"), "z.bin") })
	builds := r.pub.builds.Load()
	scanned := entry(t, r, "dup").Scanned
	waitFor(t, "two more walks", func() bool {
		st := entry(t, r, "dup")
		if st.Scanned.Sub(scanned) < 200*time.Millisecond {
			return false
		}
		return problem(st, "z.bin")
	})
	if n := r.pub.builds.Load(); n != builds {
		t.Fatalf("built again %d times", n-builds)
	}
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "z.bin seeding once readable", func() bool {
		return slices.ContainsFunc(entry(t, r, "dup").Torrents, func(tr PublishedTorrent) bool {
			return tr.Path == "z.bin" && tr.State == "seeding"
		})
	})
}

// TestPublishWebSeedURLs checks the web seeds put in torrents, a file's
// URL in mode per_file and the directory's parent in mode whole_dir.
func TestPublishWebSeedURLs(t *testing.T) {
	e := startEngine(t)
	dir := filepath.Join(t.TempDir(), "pub")
	writeData(t, dir, "sub/a b.bin", 20<<10, 50)
	r := startRole(t, e, publishJSON(t.TempDir(),
		entryJSON("f", dir, `"web_seed": "http://example.com/files", "announce": ["http://example.com/announce", "udp://example.com:6969"], "private": true`)+", "+
			entryJSON("d", dir, `"mode": "whole_dir", "web_seed": "http://example.com/x/pub/"`)), nil)
	for name, want := range map[string]string{"f": "http://example.com/files/sub/a%20b.bin", "d": "http://example.com/x/"} {
		st := waitSeeding(t, r, name, 1)
		raw, _, _, _ := r.PublishedTorrent([]string{name}, st.Torrents[0].ID)
		mi, err := metainfo.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if len(mi.URLList) != 1 || mi.URLList[0] != want {
			t.Fatalf("%s: web seeds %v, want %s", name, mi.URLList, want)
		}
		if name == "f" && (!mi.Info.Private || mi.Announce != "http://example.com/announce" || len(mi.AnnounceList) != 2) {
			t.Fatalf("trackers %q %q private %v", mi.Announce, mi.AnnounceList, mi.Info.Private)
		}
	}
}

// TestPublishReadOnly checks a published torrent's storage writes
// nothing.
func TestPublishReadOnly(t *testing.T) {
	st := readOnlyStorage{}
	if n, err := st.WriteAt(0, nil, 0); n != 0 || err != nil {
		t.Fatal(n, err)
	}
	if _, err := st.WriteAt(0, []byte("x"), 0); !errors.Is(err, errReadOnly) {
		t.Fatal(err)
	}
}
