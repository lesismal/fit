package btstream

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	fibbt "github.com/lesismal/fib/bittorrent"

	"github.com/lesismal/fit/bittorrent"
)

// TestRangeWhileDownloading asks, as a browser's player does, for a range
// of a file whose pieces the role does not have: the request waits, and is
// answered once a seeder appears and the pieces arrive.
func TestRangeWhileDownloading(t *testing.T) {
	c := makeContent(t, 1<<20+777, 32<<10)
	f := startFit(t, `{}`, "")
	h := f.handler(t)
	if _, err := f.role.Add(bittorrent.AddRequest{Torrent: c.torrent}); err != nil {
		t.Fatal(err)
	}
	from, to := 700000, 800000
	got := make(chan result, 1)
	go func() {
		got <- fetch("GET", f.base+"/bt/"+c.id+"/show/ep1.mp4", "Range", rangeHdr(from, to))
	}()
	waitFor(t, "the stream to wait for its pieces", func() bool { return h.Streams() == 1 })
	select {
	case r := <-got:
		t.Fatalf("answered with no piece downloaded: %+v", r)
	default:
	}
	tor, err := f.role.Torrent(c.id)
	if err != nil {
		t.Fatal(err)
	}
	tor.AddPeers(seed(t, c))
	var r result
	select {
	case r = <-got:
	case <-time.After(hang):
		t.Fatal("the range was not answered once the seeder came")
	}
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("status %d %q", r.resp.StatusCode, r.body)
	}
	want := "bytes " + strconv.Itoa(from) + "-" + strconv.Itoa(to) + "/" + strconv.Itoa(len(c.video))
	if cr := r.resp.Header.Get("Content-Range"); cr != want {
		t.Fatalf("Content-Range %q, want %q", cr, want)
	}
	if ct, ar := r.resp.Header.Get("Content-Type"), r.resp.Header.Get("Accept-Ranges"); ct != "video/mp4" || ar != "bytes" {
		t.Fatalf("Content-Type %q, Accept-Ranges %q", ct, ar)
	}
	if !bytes.Equal(r.body, c.video[from:to+1]) {
		t.Fatalf("the range differs: %d bytes", len(r.body))
	}
	waitFor(t, "the stream to end", func() bool { return h.Streams() == 0 })
}

// TestServing checks the answers for a downloaded torrent: whole files by
// path, index and as the largest, ranges, 416, HEAD, conditions, and what
// is not found.
func TestServing(t *testing.T) {
	c := makeContent(t, 300000, 32<<10)
	f := startFit(t, `{"cache_control": "public, max-age=60", "cors": "*"}`, "")
	if _, err := f.role.Add(bittorrent.AddRequest{Torrent: c.torrent, Peers: []string{seed(t, c)}}); err != nil {
		t.Fatal(err)
	}
	base := "/bt/" + c.id + "/"
	r := f.must(t, "GET", base+"show/ep1.mp4", http.StatusOK)
	if !bytes.Equal(r.body, c.video) {
		t.Fatalf("the file differs: %d bytes", len(r.body))
	}
	if r.resp.Header.Get("Content-Length") != strconv.Itoa(len(c.video)) || r.resp.Header.Get("Cache-Control") != "public, max-age=60" ||
		r.resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("header %v", r.resp.Header)
	}
	etag := r.resp.Header.Get("Etag")
	if etag == "" {
		t.Fatal("no ETag")
	}
	notes := f.fileIndex(t, c.id, "notes.txt")
	r = f.must(t, "GET", base+strconv.Itoa(notes), http.StatusOK)
	if !bytes.Equal(r.body, c.notes) || r.resp.Header.Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("notes %q, %v", r.body, r.resp.Header)
	}
	if r = f.must(t, "GET", base, http.StatusOK); !bytes.Equal(r.body, c.video) {
		t.Fatal("the largest file is not the video")
	}
	if r = f.must(t, "GET", "/bt/"+c.id, http.StatusOK); !bytes.Equal(r.body, c.video) {
		t.Fatal("the largest file is not the video")
	}
	r = f.must(t, "GET", base+"show/ep1.mp4", http.StatusPartialContent, "Range", "bytes=-100")
	if !bytes.Equal(r.body, c.video[len(c.video)-100:]) {
		t.Fatal("the suffix range differs")
	}
	r = f.must(t, "GET", base+"show/ep1.mp4", http.StatusPartialContent, "Range", "bytes=299990-")
	if !bytes.Equal(r.body, c.video[299990:]) {
		t.Fatal("the open range differs")
	}
	r = f.must(t, "GET", base+"show/ep1.mp4", http.StatusRequestedRangeNotSatisfiable, "Range", "bytes=300000-")
	if cr := r.resp.Header.Get("Content-Range"); cr != "bytes */300000" {
		t.Fatalf("416 with Content-Range %q", cr)
	}
	r = f.must(t, "HEAD", base+"show/ep1.mp4", http.StatusOK)
	if len(r.body) != 0 || r.resp.ContentLength != int64(len(c.video)) {
		t.Fatalf("HEAD: %d bytes, Content-Length %d", len(r.body), r.resp.ContentLength)
	}
	r = f.must(t, "HEAD", base+"show/ep1.mp4", http.StatusPartialContent, "Range", rangeHdr(10, 19))
	if r.resp.ContentLength != 10 {
		t.Fatalf("ranged HEAD: Content-Length %d", r.resp.ContentLength)
	}
	f.must(t, "GET", base+"show/ep1.mp4", http.StatusNotModified, "If-None-Match", etag)
	// An If-Range of another version asks for the whole file.
	f.must(t, "GET", base+"show/ep1.mp4", http.StatusOK, "Range", rangeHdr(0, 9), "If-Range", `"other"`)
	f.must(t, "GET", base+"show/ep1.mp4", http.StatusPartialContent, "Range", rangeHdr(0, 9), "If-Range", etag)
	// Several ranges are answered with the whole file.
	f.must(t, "GET", base+"show/ep1.mp4", http.StatusOK, "Range", "bytes=0-9,20-29")

	f.must(t, "GET", base+"show/nope.mp4", http.StatusNotFound)
	f.must(t, "GET", base+"99", http.StatusNotFound)
	f.must(t, "GET", "/bt/"+c.id[:39]+"x/0", http.StatusNotFound)
	other := "0123456789abcdef0123456789abcdef01234567"
	f.must(t, "GET", "/bt/"+other+"/0", http.StatusNotFound)
	f.must(t, "POST", base+"0", http.StatusMethodNotAllowed)
	// A magnet link with magnet off.
	f.must(t, "GET", base+"0?magnet="+url.QueryEscape(c.mi.Magnet().String()), http.StatusForbidden)
}

// TestRoleNotRunning checks a request while the role does not run.
func TestRoleNotRunning(t *testing.T) {
	f := startFit(t, `{}`, "")
	f.role.Stop(context.Background())
	f.must(t, "GET", "/bt/0123456789abcdef0123456789abcdef01234567/0", http.StatusServiceUnavailable)
}

// TestSlowReader reads a large file slowly: the stream reads only as fast
// as the client takes it, so what it has handed the connection stays
// bounded while the client reads nothing, and the file arrives whole.
func TestSlowReader(t *testing.T) {
	const size = 24 << 20
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
	head := make([]byte, 64<<10)
	if _, err := io.ReadFull(resp.Body, head); err != nil {
		t.Fatal(err)
	}
	// The client reads nothing more: what the stream hands on settles.
	last, still := h.sent.Load(), 0
	for still < 50 {
		time.Sleep(10 * time.Millisecond)
		if n := h.sent.Load(); n != last {
			last, still = n, 0
		} else {
			still++
		}
	}
	t.Logf("handed on %d bytes to a client that read %d", last, len(head))
	if last >= size/2 {
		t.Fatalf("the stream handed on %d of %d bytes to a client that read 64 KiB", last, size)
	}
	rest, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(append(head, rest...), c.video) {
		t.Fatalf("the file differs: %d bytes", len(head)+len(rest))
	}
	waitFor(t, "the stream to end", func() bool { return h.Streams() == 0 })
}

// TestCutConnection cuts the client's connection while its stream waits
// for pieces, and while it streams: the stream ends, its reader closed and
// its slot given back.
func TestCutConnection(t *testing.T) {
	c := makeContent(t, 4<<20, 64<<10)
	f := startFit(t, `{}`, "")
	h := f.handler(t)
	if _, err := f.role.Add(bittorrent.AddRequest{Torrent: c.torrent}); err != nil {
		t.Fatal(err)
	}
	conn, _ := f.rawGet(t, "/bt/"+c.id+"/show/ep1.mp4", 0, "Range", "bytes=1000000-")
	waitFor(t, "the stream to wait for its pieces", func() bool { return h.Streams() == 1 && streamsOf(f.role, c.id) == 1 })
	_ = conn.Close()
	waitFor(t, "the waiting stream to end", func() bool { return h.Streams() == 0 && streamsOf(f.role, c.id) == 0 })

	tor, _ := f.role.Torrent(c.id)
	tor.AddPeers(seed(t, c))
	select {
	case <-tor.Done():
	case <-time.After(hang):
		t.Fatal("the download did not finish")
	}
	conn, br := f.rawGet(t, "/bt/"+c.id+"/show/ep1.mp4", 16<<10)
	resp := readHead(t, conn, br)
	if _, err := io.ReadFull(resp.Body, make([]byte, 100<<10)); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	waitFor(t, "the streaming stream to end", func() bool { return h.Streams() == 0 && streamsOf(f.role, c.id) == 0 })
}

// TestMaxStreams fills max_streams: the next request is answered 503, and
// one is taken again once a stream ends.
func TestMaxStreams(t *testing.T) {
	c := makeContent(t, 1<<20, 64<<10)
	f := startFit(t, `{"max_streams": 1}`, "")
	h := f.handler(t)
	if _, err := f.role.Add(bittorrent.AddRequest{Torrent: c.torrent}); err != nil {
		t.Fatal(err)
	}
	path := "/bt/" + c.id + "/show/ep1.mp4"
	conn, _ := f.rawGet(t, path, 0)
	waitFor(t, "the first stream", func() bool { return h.Streams() == 1 })
	r := f.must(t, "GET", path, http.StatusServiceUnavailable)
	if r.resp.Header.Get("Retry-After") == "" {
		t.Fatal("503 without Retry-After")
	}
	_ = conn.Close()
	waitFor(t, "the first stream to end", func() bool { return h.Streams() == 0 })
	conn, _ = f.rawGet(t, path, 0)
	waitFor(t, "a stream again", func() bool { return h.Streams() == 1 })
	_ = conn.Close()
	waitFor(t, "the stream to end", func() bool { return h.Streams() == 0 })
}

// TestReadTimeout waits past read_timeout for a piece no peer has.
func TestReadTimeout(t *testing.T) {
	c := makeContent(t, 1<<20, 64<<10)
	f := startFit(t, `{"read_timeout": "100ms"}`, "")
	if _, err := f.role.Add(bittorrent.AddRequest{Torrent: c.torrent}); err != nil {
		t.Fatal(err)
	}
	f.must(t, "GET", "/bt/"+c.id+"/show/ep1.mp4", http.StatusGatewayTimeout)
	waitFor(t, "the stream to end", func() bool { return f.handler(t).Streams() == 0 })
}

// magnetPath is the URL of file of c by its magnet link, which names peer.
func magnetPath(c *content, file, peer string) string {
	m := c.mi.Magnet()
	m.Peers = []string{peer}
	return "/bt/" + c.id + "/" + file + "?magnet=" + url.QueryEscape(m.String())
}

// TestTemporaryLifecycle adds a torrent by magnet link on the fly: it is
// temporary, downloads only the file streamed, counts against
// max_temporary, and is removed with its data temporary_ttl after its last
// stream.
func TestTemporaryLifecycle(t *testing.T) {
	c := makeContent(t, 600000, 32<<10)
	f := startFit(t, `{"auth": "site", "magnet": true, "max_temporary": 1, "temporary_ttl": "1h"}`, authConf)
	h := f.handler(t)
	peer := seed(t, c)
	pub := []string{"Authorization", "Bearer " + pubToken}

	f.must(t, "GET", magnetPath(c, "show/ep1.mp4", peer), http.StatusUnauthorized)
	f.must(t, "GET", magnetPath(c, "show/ep1.mp4", peer), http.StatusForbidden, "Authorization", "Bearer "+playToken)
	if _, err := f.role.Get(c.id); !errors.Is(err, bittorrent.ErrNotFound) {
		t.Fatalf("a refused request added the torrent: %v", err)
	}
	// A magnet link of another torrent than the path's.
	other := makeContent(t, 1000, 32<<10)
	f.must(t, "GET", "/bt/"+c.id+"/0?magnet="+url.QueryEscape(other.mi.Magnet().String()), http.StatusBadRequest, pub...)

	r := f.must(t, "GET", magnetPath(c, "show/ep1.mp4", peer), http.StatusOK, pub...)
	if !bytes.Equal(r.body, c.video) {
		t.Fatalf("the file differs: %d bytes", len(r.body))
	}
	s, err := f.role.Get(c.id)
	if err != nil || !s.Temporary {
		t.Fatalf("%+v, %v", s, err)
	}
	for _, x := range s.Files {
		want := fibbt.PriorityNone
		if x.Path == "show/ep1.mp4" {
			want = fibbt.PriorityNormal
		}
		if x.Priority != want {
			t.Fatalf("file %s has priority %d, want %d", x.Path, x.Priority, want)
		}
	}
	video := filepath.Join(f.dl, "show", "ep1.mp4")
	if _, err := os.Stat(video); err != nil {
		t.Fatal(err)
	}
	// max_temporary is 1.
	other.id = other.mi.Info.InfoHashV1().String()
	f.must(t, "GET", magnetPath(other, "0", peer), http.StatusServiceUnavailable, pub...)

	// Its last stream ends with a short linger: it is removed with its
	// data.
	waitFor(t, "the streams to end", func() bool { return h.Streams() == 0 })
	h.ttl = 50 * time.Millisecond
	f.must(t, "GET", magnetPath(c, "show/ep1.mp4", peer), http.StatusPartialContent, append(pub, "Range", rangeHdr(0, 9))...)
	waitFor(t, "the temporary torrent to be removed", func() bool {
		_, err := f.role.Get(c.id)
		return errors.Is(err, bittorrent.ErrNotFound)
	})
	if _, err := os.Stat(video); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("its data is left: %v", err)
	}
	// Asked again, it is added again.
	f.must(t, "GET", magnetPath(c, "show/ep1.mp4", peer), http.StatusPartialContent, append(pub, "Range", rangeHdr(0, 9))...)
	waitFor(t, "the temporary torrent to be removed again", func() bool {
		_, err := f.role.Get(c.id)
		return errors.Is(err, bittorrent.ErrNotFound)
	})
}

// TestTemporaryKeptWhileStreamed checks that a temporary torrent is not
// removed while a stream reads it, though its linger passed for another.
func TestTemporaryKeptWhileStreamed(t *testing.T) {
	c := makeContent(t, 8<<20, 256<<10)
	f := startFit(t, `{"auth": "site", "magnet": true, "temporary_ttl": "10ms"}`, authConf)
	h := f.handler(t)
	auth := []string{"Authorization", "Bearer " + pubToken}
	// A client that reads nothing past the header holds its stream.
	conn, br := f.rawGet(t, magnetPath(c, "show/ep1.mp4", seed(t, c)), 16<<10, auth...)
	if resp := readHead(t, conn, br); resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if s, err := f.role.Get(c.id); err != nil || !s.Temporary {
		t.Fatalf("%+v, %v", s, err)
	}
	// A second stream comes and goes; the first still holds it.
	f.must(t, "GET", "/bt/"+c.id+"/show/ep1.mp4", http.StatusPartialContent, append(auth, "Range", rangeHdr(0, 9))...)
	time.Sleep(50 * time.Millisecond)
	if _, err := f.role.Get(c.id); err != nil {
		t.Fatalf("removed while streamed: %v", err)
	}
	_ = conn.Close()
	waitFor(t, "the temporary torrent to be removed", func() bool {
		_, err := f.role.Get(c.id)
		return errors.Is(err, bittorrent.ErrNotFound) && h.Streams() == 0
	})
}

// TestTemporaryBytes refuses a torrent past max_temporary_bytes, removing
// it.
func TestTemporaryBytes(t *testing.T) {
	c := makeContent(t, 600000, 32<<10)
	f := startFit(t, `{"auth": "site", "magnet": true, "max_temporary_bytes": 1024}`, authConf)
	f.must(t, "GET", magnetPath(c, "show/ep1.mp4", seed(t, c)), http.StatusInsufficientStorage, "Authorization", "Bearer "+pubToken)
	if _, err := f.role.Get(c.id); !errors.Is(err, bittorrent.ErrNotFound) {
		t.Fatalf("the torrent past the bound is kept: %v", err)
	}
}

// TestAuth checks media_auth on playing: refused without credentials, let
// in with a token in the header or the query.
func TestAuth(t *testing.T) {
	c := makeContent(t, 100000, 32<<10)
	f := startFit(t, `{"auth": "site"}`, authConf)
	if _, err := f.role.Add(bittorrent.AddRequest{Torrent: c.torrent, Peers: []string{seed(t, c)}}); err != nil {
		t.Fatal(err)
	}
	path := "/bt/" + c.id + "/show/ep1.mp4"
	f.must(t, "GET", path, http.StatusUnauthorized)
	f.must(t, "GET", path, http.StatusUnauthorized, "Authorization", "Bearer wrong-token-0123456789")
	if r := f.must(t, "GET", path, http.StatusOK, "Authorization", "Bearer "+playToken); !bytes.Equal(r.body, c.video) {
		t.Fatal("the file differs")
	}
	f.must(t, "GET", path+"?token="+playToken, http.StatusOK)
}
