package btstream

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	fibbt "github.com/lesismal/fib/bittorrent"
	"github.com/lesismal/fib/bittorrent/metainfo"
	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/bittorrent"
)

// TestRequestEdges answers requests refused before a stream starts: a path
// whose ID is not one, and magnet links too long, malformed, or of v2 alone.
func TestRequestEdges(t *testing.T) {
	c := makeContent(t, 100000, 32<<10)
	f := startFit(t, `{"auth": "site", "magnet": true, "info_timeout": "200ms"}`, authConf)
	h := f.handler(t)
	pub := []string{"Authorization", "Bearer " + pubToken}

	f.must(t, "GET", "/bt/abc/0", http.StatusNotFound, pub...)
	long := "magnet:?xt=urn:btih:" + c.id + "&dn=" + strings.Repeat("a", bittorrent.MaxMagnetBytes)
	if r := f.must(t, "GET", "/bt/"+c.id+"/0?magnet="+url.QueryEscape(long), http.StatusBadRequest, pub...); !strings.Contains(string(r.body), "longer than") {
		t.Fatalf("a long magnet link: %q", r.body)
	}
	if r := f.must(t, "GET", "/bt/"+c.id+"/0?magnet=nonsense", http.StatusBadRequest, pub...); !strings.Contains(string(r.body), "not a magnet link") {
		t.Fatalf("not a magnet link: %q", r.body)
	}

	// A magnet link of the v2 hash alone names the torrent by that hash
	// truncated; no peer has it, so its info does not come.
	m := &metainfo.Magnet{InfoHashV2: c.mi.Info.InfoHashV2()}
	id := c.mi.Info.InfoHashV2().Truncate().String()
	if r := f.must(t, "GET", "/bt/"+id+"/0?magnet="+url.QueryEscape(m.String()), http.StatusGatewayTimeout, pub...); !strings.Contains(string(r.body), "info_timeout") {
		t.Fatalf("a v2 magnet link: %q", r.body)
	}
	if s, err := f.role.Get(id); err != nil || !s.Temporary || s.ID != id {
		t.Fatalf("the v2 torrent: %+v %v", s, err)
	}
	waitFor(t, "the stream to end", func() bool { return h.Streams() == 0 })
}

// TestTooManyTorrents adds by magnet link to a role holding max_torrents.
func TestTooManyTorrents(t *testing.T) {
	c := makeContent(t, 100000, 32<<10)
	other := makeContent(t, 50000, 32<<10)
	f := startFit(t, `{"auth": "site", "magnet": true}`, authConf)
	f.role.Stop(context.Background())
	f.role = startRole(t, f.engine, `{"listen": "127.0.0.1:0", "data_dir": `+strconv.Quote(t.TempDir())+`, "download_dir": `+
		strconv.Quote(f.dl)+`, "dht": false, "limits": {"max_torrents": 1}}`)
	if _, err := f.role.Add(bittorrent.AddRequest{Torrent: other.torrent, Paused: true}); err != nil {
		t.Fatal(err)
	}
	r := f.must(t, "GET", "/bt/"+c.id+"/0?magnet="+url.QueryEscape(c.mi.Magnet().String()), http.StatusServiceUnavailable,
		"Authorization", "Bearer "+pubToken)
	if !strings.Contains(string(r.body), "max_torrents") || r.resp.Header.Get("Retry-After") != "30" {
		t.Fatalf("%q %v", r.body, r.resp.Header)
	}
}

// TestPublicPlayAdd plays a public policy's torrents without credentials,
// but adds one only with publish: 401 with a challenge without them, and
// with a password checked by bcrypt off the worker, the stream.
func TestPublicPlayAdd(t *testing.T) {
	c := makeContent(t, 100000, 32<<10)
	hash, err := bcrypt.GenerateFromPassword([]byte("bob-password-1"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	auth := `[{"name": "site", "public": ["play"], "users": [{"name": "bob", "password_hash": "` + string(hash) + `", "publish": ["bt/*"]}]}]`
	f := startFit(t, `{"auth": "site", "magnet": true}`, auth)
	path := magnetPath(c, "show/ep1.mp4", seed(t, c))
	r := f.must(t, "GET", path, http.StatusUnauthorized)
	if !strings.Contains(string(r.body), "publish on bt/"+c.id) || !strings.HasPrefix(r.resp.Header.Get("Www-Authenticate"), "Bearer") {
		t.Fatalf("%q %v", r.body, r.resp.Header)
	}
	if _, err := f.role.Get(c.id); !errors.Is(err, bittorrent.ErrNotFound) {
		t.Fatalf("a refused request added the torrent: %v", err)
	}
	f.must(t, "GET", path, http.StatusUnauthorized, "Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("bob:wrong-password")))
	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte("bob:bob-password-1"))
	r = f.must(t, "GET", path, http.StatusOK, "Authorization", basic)
	if string(r.body) != string(c.video) {
		t.Fatalf("the file differs: %d bytes", len(r.body))
	}
	// Held now: played without credentials.
	f.must(t, "GET", "/bt/"+c.id+"/show/ep1.mp4", http.StatusPartialContent, "Range", rangeHdr(0, 9))
}

// TestLeaveDuringCheck has a client leave while the bcrypt check of its
// password, which may add the torrent, runs: the stream ends, and adds
// nothing.
func TestLeaveDuringCheck(t *testing.T) {
	c := makeContent(t, 100000, 32<<10)
	// A cost that keeps the check running well past the client's leaving.
	hash, err := bcrypt.GenerateFromPassword([]byte("bob-password-1"), 12)
	if err != nil {
		t.Fatal(err)
	}
	auth := `[{"name": "site", "public": ["play"], "users": [{"name": "bob", "password_hash": "` + string(hash) + `", "publish": ["bt/*"]}]}]`
	f := startFit(t, `{"auth": "site", "magnet": true}`, auth)
	h := f.handler(t)
	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte("bob:bob-password-1"))
	conn, _ := f.rawGet(t, "/bt/"+c.id+"/0?magnet="+url.QueryEscape(c.mi.Magnet().String()), 0, "Authorization", basic)
	waitFor(t, "the stream", func() bool { return h.Streams() == 1 })
	_ = conn.Close()
	waitFor(t, "the stream to end", func() bool { return h.Streams() == 0 })
	if _, err := f.role.Get(c.id); !errors.Is(err, bittorrent.ErrNotFound) {
		t.Fatalf("a stream whose client left added the torrent: %v", err)
	}
}

// TestInfoWaitEnds ends streams waiting for a magnet link's info: one by
// the torrent's removal, 503, and one by its client leaving.
func TestInfoWaitEnds(t *testing.T) {
	c := makeContent(t, 100000, 32<<10)
	f := startFit(t, `{"auth": "site", "magnet": true}`, authConf)
	h := f.handler(t)
	path := "/bt/" + c.id + "/0?magnet=" + url.QueryEscape(c.mi.Magnet().String())
	auth := []string{"Authorization", "Bearer " + pubToken}

	got := make(chan result, 1)
	go func() { got <- fetch("GET", f.base+path, auth...) }()
	waitFor(t, "the stream to wait for the info", func() bool {
		_, err := f.role.Get(c.id)
		return err == nil && h.Streams() == 1 && streamsOf(f.role, c.id) == 1
	})
	if err := f.role.Remove(c.id, true); err != nil {
		t.Fatal(err)
	}
	r := <-got
	if r.err != nil || r.resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(r.body), "removed") {
		t.Fatalf("%+v %q", r, r.body)
	}
	waitFor(t, "the stream to end", func() bool { return h.Streams() == 0 })

	conn, _ := f.rawGet(t, path, 0, auth...)
	waitFor(t, "the stream to wait for the info again", func() bool { return h.Streams() == 1 && streamsOf(f.role, c.id) == 1 })
	_ = conn.Close()
	waitFor(t, "the stream to end with its client", func() bool { return h.Streams() == 0 && streamsOf(f.role, c.id) == 0 })

	// A peer with the info comes while a stream waits for it.
	go func() { got <- fetch("GET", f.base+path, auth...) }()
	waitFor(t, "a stream to wait for the info", func() bool { return h.Streams() == 1 && streamsOf(f.role, c.id) == 1 })
	tor, err := f.role.Torrent(c.id)
	if err != nil {
		t.Fatal(err)
	}
	tor.AddPeers(seed(t, c))
	r = <-got
	if r.err != nil || r.resp.StatusCode != http.StatusOK || string(r.body) != string(c.video) {
		t.Fatalf("%+v, %d bytes", r.err, len(r.body))
	}
}

// TestTemporaryFileRefused asks for a second file of a temporary torrent
// that would take it past max_temporary_bytes: 507, and the file's
// priority set back.
func TestTemporaryFileRefused(t *testing.T) {
	c := makeContent(t, 100000, 32<<10)
	f := startFit(t, `{"auth": "site", "magnet": true}`, authConf)
	h := f.handler(t)
	auth := []string{"Authorization", "Bearer " + pubToken}
	f.must(t, "GET", magnetPath(c, "show/ep1.mp4", seed(t, c)), http.StatusOK, auth...)
	// The bound is what it wants now: the notes would take it past.
	waitFor(t, "the stream to end", func() bool { return h.Streams() == 0 })
	h.maxTempBytes = f.role.TemporaryBytes()
	notes := f.fileIndex(t, c.id, "notes.txt")
	r := f.must(t, "GET", "/bt/"+c.id+"/"+strconv.Itoa(notes), http.StatusInsufficientStorage, auth...)
	if !strings.Contains(string(r.body), "max_temporary_bytes") {
		t.Fatalf("%q", r.body)
	}
	s, err := f.role.Get(c.id)
	if err != nil || !s.Temporary {
		t.Fatalf("%+v %v", s, err)
	}
	for _, x := range s.Files {
		want := fibbt.PriorityNone
		if strings.HasSuffix(x.Path, "/ep1.mp4") {
			want = fibbt.PriorityNormal
		}
		if x.Priority != want {
			t.Fatalf("file %s has priority %d, want %d", x.Path, x.Priority, want)
		}
	}
}

// TestReadFailures has reads fail: past read_timeout once the response
// began, which is cut short, and by the torrent's removal before it did,
// 503.
func TestReadFailures(t *testing.T) {
	c := makeContent(t, 600000, 32<<10)
	f := startFit(t, `{"read_timeout": "300ms"}`, "")
	h := f.handler(t)
	addPartial(t, f, c)
	path := "/bt/" + c.id + "/show/ep1.mp4"
	// Begun, then a piece past what the disk holds does not come.
	r := fetch("GET", f.base+path)
	if r.err == nil || r.resp == nil || r.resp.StatusCode != http.StatusOK || len(r.body) >= len(c.video) || len(r.body) < 64<<10 {
		t.Fatalf("a response cut short: %v, %d bytes", r.err, len(r.body))
	}
	if string(r.body) != string(c.video[:len(r.body)]) {
		t.Fatal("what came differs")
	}
	waitFor(t, "the stream to end", func() bool { return h.Streams() == 0 })

	// Not begun: the torrent is removed while a read waits.
	f2 := startFit(t, `{}`, "")
	h2 := f2.handler(t)
	if _, err := f2.role.Add(bittorrent.AddRequest{Torrent: c.torrent}); err != nil {
		t.Fatal(err)
	}
	got := make(chan result, 1)
	go func() { got <- fetch("GET", f2.base+path, "Range", "bytes=500000-") }()
	waitFor(t, "the stream to wait for its piece", func() bool { return h2.Streams() == 1 && streamsOf(f2.role, c.id) == 1 })
	time.Sleep(50 * time.Millisecond) // into its read, most likely; before it, the answer is the same
	if err := f2.role.Remove(c.id, true); err != nil {
		t.Fatal(err)
	}
	r = <-got
	if r.err != nil || r.resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(r.body), "removed") {
		t.Fatalf("%+v %q", r, r.body)
	}
	waitFor(t, "the stream to end", func() bool { return h2.Streams() == 0 })
}

// addPartial adds c to f's role with its first 256 KiB on the disk
// already, the rest not, and waits for them to be checked.
func addPartial(t *testing.T, f *fit, c *content) {
	t.Helper()
	part := make([]byte, len(c.video))
	copy(part, c.video[:256<<10])
	if err := os.MkdirAll(filepath.Join(f.dl, "show"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dl, "show", "ep1.mp4"), part, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.role.Add(bittorrent.AddRequest{Torrent: c.torrent}); err != nil {
		t.Fatal(err)
	}
	tor, err := f.role.Torrent(c.id)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the check of the data on the disk", func() bool {
		s, err := f.role.Get(c.id)
		return err == nil && s.State != "checking" && tor.HavePiece(7)
	})
}

// TestCutWhileReadWaits cuts a client off once it has taken all the disk
// holds of its file, while the stream's read waits for the next piece: the
// stream ends.
func TestCutWhileReadWaits(t *testing.T) {
	c := makeContent(t, 600000, 32<<10)
	f := startFit(t, `{}`, "")
	h := f.handler(t)
	addPartial(t, f, c)
	conn, br := f.rawGet(t, "/bt/"+c.id+"/show/ep1.mp4", 0)
	resp := readHead(t, conn, br)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	head := make([]byte, 256<<10)
	if _, err := io.ReadFull(resp.Body, head); err != nil {
		t.Fatal(err)
	}
	if string(head) != string(c.video[:len(head)]) {
		t.Fatal("what came differs")
	}
	waitFor(t, "all the disk holds handed on", func() bool { return h.sent.Load() == int64(len(head)) })
	_ = conn.Close()
	waitFor(t, "the stream to end", func() bool { return h.Streams() == 0 && streamsOf(f.role, c.id) == 0 })
}

// TestDrainWhileWaitingForClient drains a stream waiting for a slow client
// to take what it has queued: once the client reads, the stream ends
// rather than reading on, and the response is cut short.
func TestDrainWhileWaitingForClient(t *testing.T) {
	const size = 8 << 20
	c := makeContent(t, size, 256<<10)
	f := startFit(t, `{}`, "")
	h := f.handler(t)
	if _, err := f.role.Add(bittorrent.AddRequest{Torrent: c.torrent, Peers: []string{seed(t, c)}}); err != nil {
		t.Fatal(err)
	}
	tor, _ := f.role.Torrent(c.id)
	select {
	case <-tor.Done():
	case <-time.After(hang):
		t.Fatal("the download did not finish")
	}
	conn, br := f.rawGet(t, "/bt/"+c.id+"/show/ep1.mp4", 16<<10)
	resp := readHead(t, conn, br)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if _, err := io.ReadFull(resp.Body, make([]byte, 64<<10)); err != nil {
		t.Fatal(err)
	}
	// The client reads nothing more: the stream waits for it.
	last, still := h.sent.Load(), 0
	for still < 50 {
		time.Sleep(10 * time.Millisecond)
		if n := h.sent.Load(); n != last {
			last, still = n, 0
		} else {
			still++
		}
	}
	if last >= size {
		t.Fatalf("the stream handed on all %d bytes to a client that read 64 KiB", last)
	}
	h.Drain()
	rest, err := io.ReadAll(resp.Body)
	if n := int64(64<<10 + len(rest)); n >= size {
		t.Fatalf("a drained stream sent the whole file: %d bytes", n)
	}
	// The connection ends short of the Content-Length: the client can
	// tell the file was cut, not that it is shorter.
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("reading the cut body: %v", err)
	}
	waitFor(t, "the stream to end", func() bool { return h.Streams() == 0 })
}

// TestDiskReadFails reads a piece had whose file was cut short on the disk
// since: 500, the response not begun.
func TestDiskReadFails(t *testing.T) {
	c := makeContent(t, 600000, 32<<10)
	f := startFit(t, `{}`, "")
	addPartial(t, f, c)
	if err := os.Truncate(filepath.Join(f.dl, "show", "ep1.mp4"), 0); err != nil {
		t.Fatal(err)
	}
	r := f.must(t, "GET", "/bt/"+c.id+"/show/ep1.mp4", http.StatusInternalServerError, "Range", rangeHdr(0, 9))
	if !strings.Contains(string(r.body), "reading the torrent") {
		t.Fatalf("%q", r.body)
	}
	waitFor(t, "the stream to end", func() bool { return f.handler(t).Streams() == 0 })
}

// TestPickFileEdges picks no file for a selector of other characters than
// digits that no path matches.
func TestPickFileEdges(t *testing.T) {
	for _, sel := range []string{"", "1a", "x", "1234567890"} {
		if f := pickFile(nil, request{sel: sel}); f != nil {
			t.Errorf("%q: %v", sel, f)
		}
	}
	if f := pickFile(nil, request{largest: true}); f != nil {
		t.Errorf("the largest of none: %v", f)
	}
}

// TestHoldEdges drives the streams' holds directly: an acquire waits for a
// removal under way, or gives up with its context; a release or a linger of
// what is not held does nothing.
func TestHoldEdges(t *testing.T) {
	k := key{id: "hold-edges"}
	ch := make(chan struct{})
	tempMu.Lock()
	temps[k] = &held{removing: ch}
	tempMu.Unlock()
	t.Cleanup(func() {
		tempMu.Lock()
		delete(temps, k)
		tempMu.Unlock()
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if acquire(ctx, k) {
		t.Fatal("acquired with its context ended, during a removal")
	}
	done := make(chan bool, 1)
	go func() { done <- acquire(context.Background(), k) }()
	select {
	case <-done:
		t.Fatal("acquired during a removal")
	case <-time.After(20 * time.Millisecond):
	}
	tempMu.Lock()
	temps[k].removing = nil
	close(ch)
	tempMu.Unlock()
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("not acquired once the removal ended")
		}
	case <-time.After(hang):
		t.Fatal("the acquire did not return")
	}
	if n := streamsOf(nil, k.id); n != 1 {
		t.Fatalf("%d streams", n)
	}
	// A linger superseded since does nothing.
	linger(k, 0)
	if n := streamsOf(nil, k.id); n != 1 {
		t.Fatalf("%d streams after a stale linger", n)
	}

	none := key{id: "hold-edges-none"}
	release(none, time.Hour)
	linger(none, 1)
	if n := streamsOf(nil, none.id); n != 0 {
		t.Fatalf("%d streams of what is not held", n)
	}
	tempMu.Lock()
	_, left := temps[none]
	tempMu.Unlock()
	if left {
		t.Fatal("a release of what is not held holds it")
	}
}
