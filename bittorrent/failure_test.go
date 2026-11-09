package bittorrent

import (
	"context"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	fibbt "github.com/lesismal/fib/bittorrent"
	"github.com/lesismal/fib/bittorrent/metainfo"
)

// I205's failure tests, beside those of I197-I203 (a cut seeder replaced,
// a storage that cannot be opened, a disk short by the free-space check, a
// restart after a download, a reload of limits, a listen change while
// paused): a disk that fills during a download, restarts and a listen
// change in the middle of several downloads, and peers cut in the middle
// of a piece or stalled while sending one.

// fullDisk makes the storages it wraps fail their writes with ENOSPC while
// full is set, as a disk that filled up does.
type fullDisk struct {
	full   atomic.Bool
	failed atomic.Int64
}

type fullStorage struct {
	fibbt.Storage
	d *fullDisk
}

func (s *fullStorage) WriteAt(file int, p []byte, off int64) (int, error) {
	if s.d.full.Load() {
		s.d.failed.Add(1)
		return 0, &pathError{syscall.ENOSPC}
	}
	return s.Storage.WriteAt(file, p, off)
}

func (s *fullStorage) Stat(file int) (int64, time.Time, error) {
	return s.Storage.(fibbt.StatStorage).Stat(file)
}

func (s *fullStorage) Remove() error { return s.Storage.(fibbt.StorageRemover).Remove() }

// pathError is what the file storage's write returns on a full disk.
type pathError struct{ err error }

func (e *pathError) Error() string { return "write: " + e.err.Error() }
func (e *pathError) Unwrap() error { return e.err }

func (d *fullDisk) wrap(o fibbt.StorageOpener) fibbt.StorageOpener {
	return func(info *metainfo.Info) (fibbt.Storage, error) {
		st, err := o(info)
		if err != nil {
			return nil, err
		}
		return &fullStorage{st, d}, nil
	}
}

// TestDiskFullMidDownload: the disk fills in the middle of a download,
// before the free-space check sees it (writes fail with ENOSPC while the
// check still finds room): the torrent keeps running with the error in its
// status, the blocks that failed are asked for again, and once the check
// sees the disk short the role pauses the torrent; with room again it
// starts by itself and the download completes, the data whole, no piece
// having failed its hash.
func TestDiskFullMidDownload(t *testing.T) {
	e := startEngine(t)
	src := t.TempDir()
	torrent, data := payload(t, src, "full.bin", 2<<20)
	s, id := seeder(t, e, src, torrent, "")
	disk := &fullDisk{}
	var free atomic.Int64
	free.Store(1 << 40)
	ldata, ldl := t.TempDir(), t.TempDir()
	l := startRoleWith(t, e, member(t, clientJSON(ldata, ldl, `"limits": {"min_free_bytes": 1048576, "download_rate": 262144}`)), func(r *Role) {
		r.wrap = disk.wrap
		r.diskFree = func(string) (uint64, bool) { return uint64(free.Load()), true }
	})
	if _, err := l.Add(AddRequest{Torrent: torrent, Peers: []string{peerAddr(s)}}); err != nil {
		t.Fatal(err)
	}
	tor, err := l.Torrent(id)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a piece", func() bool { return tor.Stats().PiecesHave > 0 })

	disk.full.Store(true)
	waitFor(t, "the write error in the status", func() bool {
		st := get(t, l, id)
		return strings.Contains(st.Error, "no space")
	})
	have := tor.Stats().PiecesHave
	if st := get(t, l, id); st.State == "paused" {
		t.Fatalf("paused by a write error alone: %+v", st)
	}

	free.Store(0)
	waitFor(t, "the role to pause the download", func() bool {
		st := get(t, l, id)
		return st.Held == "disk" && st.State == "paused"
	})

	disk.full.Store(false)
	free.Store(1 << 40)
	waitFor(t, "the download to start again", func() bool { return get(t, l, id).Held == "" })
	tor.AddPeers(peerAddr(s))
	waitDone(t, l, id)
	sameFile(t, filepath.Join(ldl, "full.bin"), data)
	st := tor.Stats()
	if st.HashFails != 0 {
		t.Fatalf("%d pieces failed their hash after the disk filled", st.HashFails)
	}
	if disk.failed.Load() == 0 || st.PiecesHave <= have {
		t.Fatalf("no write failed (%d) or nothing came after (%d, then %d)", disk.failed.Load(), have, st.PiecesHave)
	}
	if got := get(t, l, id); got.Error != "" {
		t.Fatalf("the error outlived the recovery: %+v", got)
	}
}

// TestRestartMidDownloads stops the role in the middle of three downloads
// from one seeder and starts it again on the same data_dir: every piece
// each had at the stop is had again without a read of the data (resume
// data), and the downloads complete once a peer is given again.
func TestRestartMidDownloads(t *testing.T) {
	e := startEngine(t)
	src := t.TempDir()
	torrent, data := payload(t, src, "a.bin", 1<<20)
	s, id := seeder(t, e, src, torrent, "")
	more, datas, ids := seedMore(t, s, src, "b.bin", "c.bin")
	torrents := append([][]byte{torrent}, more...)
	datas = append([][]byte{data}, datas...)
	ids = append([]string{id}, ids...)
	names := []string{"a.bin", "b.bin", "c.bin"}

	ldata, ldl := t.TempDir(), t.TempDir()
	slow := clientJSON(ldata, ldl, `"limits": {"download_rate": 131072}`)
	l := startRole(t, e, slow, nil)
	for _, tb := range torrents {
		if _, err := l.Add(AddRequest{Torrent: tb, Peers: []string{peerAddr(s)}}); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "pieces of every torrent", func() bool {
		for _, id := range ids {
			tor, err := l.Torrent(id)
			if err != nil || tor.Stats().PiecesHave == 0 {
				return false
			}
		}
		return true
	})
	had := map[string]int{}
	for _, id := range ids {
		tor, _ := l.Torrent(id)
		had[id] = tor.Stats().PiecesHave
	}
	tor0, _ := l.Torrent(ids[0])
	if tor0.HaveAll() {
		t.Fatal("the first download finished before the stop")
	}
	l.Stop(context.Background())

	rc := &readCounter{}
	l2 := startRole(t, e, slow, rc)
	for _, id := range ids {
		// A torrent restored paused shows as paused while its first check
		// takes the resume data; wait for the pieces themselves.
		waitFor(t, "the restored torrent to have its pieces again", func() bool {
			tor, err := l2.Torrent(id)
			return err == nil && tor.Stats().PiecesHave >= had[id]
		})
		waitFor(t, "the restored torrent to run", func() bool {
			st, err := l2.Get(id)
			return err == nil && st.State != "checking" && st.State != "metadata" && st.Held == ""
		})
	}
	if n := rc.total(); n != 0 {
		t.Fatalf("%d reads of the data after a restart in the middle of downloads", n)
	}
	for i, id := range ids {
		tor, _ := l2.Torrent(id)
		tor.AddPeers(peerAddr(s))
		waitDone(t, l2, id)
		sameFile(t, filepath.Join(ldl, names[i]), datas[i])
	}
}

// TestListenChangeMidDownload moves the client to another port while it
// downloads, not paused: the new client takes the torrent with its pieces,
// reading none of them, and the download completes.
func TestListenChangeMidDownload(t *testing.T) {
	e := startEngine(t)
	src := t.TempDir()
	torrent, data := payload(t, src, "move.bin", 2<<20)
	s, id := seeder(t, e, src, torrent, "")
	ldata, ldl := t.TempDir(), t.TempDir()
	js := clientJSON(ldata, ldl, `"limits": {"download_rate": 262144}`)
	rc := &readCounter{}
	l := startRole(t, e, js, rc)
	if _, err := l.Add(AddRequest{Torrent: torrent, Peers: []string{peerAddr(s)}}); err != nil {
		t.Fatal(err)
	}
	tor, err := l.Torrent(id)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a few pieces", func() bool { return tor.Stats().PiecesHave >= 4 })
	old := l.Client()
	reads := rc.total()

	port := freePort(t)
	if err := l.Apply(member(t, strings.Replace(js, `"127.0.0.1:0"`, `"127.0.0.1:`+strconv.Itoa(port)+`"`, 1))); err != nil {
		t.Fatal(err)
	}
	if l.Client() == old || int(l.Addr().Port()) != port {
		t.Fatalf("no new client on port %d: %v", port, l.Addr())
	}
	ntor, err := l.Torrent(id)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the torrent on the new client", func() bool {
		st := get(t, l, id)
		return st.State != "checking" && st.Held == ""
	})
	if n := rc.total() - reads; n != 0 {
		t.Fatalf("%d reads of the data by the new client", n)
	}
	if st := get(t, l, id); st.Paused {
		t.Fatalf("paused by the move: %+v", st)
	}
	ntor.AddPeers(peerAddr(s))
	waitDone(t, l, id)
	sameFile(t, filepath.Join(ldl, "move.bin"), data)
}

// relay is a TCP relay to a peer that can cut its connections, or stall
// them (stop forwarding, keeping them open), once a number of bytes has
// gone from the peer to the client.
type relay struct {
	ln     net.Listener
	target string
	after  int64 // bytes forwarded from the peer before acting
	stall  bool  // stall rather than cut

	mu    sync.Mutex
	conns []net.Conn
	acted atomic.Bool
}

func startRelay(t *testing.T, target string, after int64, stall bool) *relay {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := &relay{ln: ln, target: target, after: after, stall: stall}
	t.Cleanup(r.close)
	go r.accept()
	return r
}

func (r *relay) addr() string { return r.ln.Addr().String() }

func (r *relay) accept() {
	for {
		c, err := r.ln.Accept()
		if err != nil {
			return
		}
		u, err := net.Dial("tcp4", r.target)
		if err != nil {
			_ = c.Close()
			continue
		}
		r.mu.Lock()
		r.conns = append(r.conns, c, u)
		r.mu.Unlock()
		go func() { _, _ = io.Copy(u, c) }()
		go r.down(c, u)
	}
}

// down forwards the peer's bytes to the client, up to after, then cuts or
// stalls.
func (r *relay) down(c, u net.Conn) {
	n, _ := io.CopyN(c, u, r.after)
	if n < r.after {
		_ = c.Close()
		return
	}
	r.acted.Store(true)
	if r.stall {
		// Keep both open, read nothing more: the peer's sends back up and
		// the client hears nothing, as from a peer that hangs.
		return
	}
	_ = c.Close()
	_ = u.Close()
}

func (r *relay) close() {
	_ = r.ln.Close()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.conns {
		_ = c.Close()
	}
}

// TestPeerCutMidPiece: the only seeder's connection is cut in the middle
// of a piece's block (after a number of bytes no message boundary falls
// on); the half piece is kept or asked for again, never checked as it is,
// and once the seeder is reachable again the download completes whole with
// no piece failing its hash.
func TestPeerCutMidPiece(t *testing.T) {
	e := startEngine(t)
	src := t.TempDir()
	torrent, data := payload(t, src, "half.bin", 2<<20)
	s, id := seeder(t, e, src, torrent, "")
	// 300001 bytes: past the handshakes and bitfield, inside a block of a
	// piece (32 KiB pieces of two 16 KiB blocks, each block's message 13
	// bytes more).
	cut := startRelay(t, peerAddr(s), 300_001, false)
	ldata, ldl := t.TempDir(), t.TempDir()
	l := startRole(t, e, clientJSON(ldata, ldl, `"encryption": "allow", "utp": false`), nil)
	if _, err := l.Add(AddRequest{Torrent: torrent, Peers: []string{cut.addr()}}); err != nil {
		t.Fatal(err)
	}
	tor, err := l.Torrent(id)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the cut", func() bool { return cut.acted.Load() && len(tor.Peers()) == 0 })
	if tor.HaveAll() {
		t.Fatal("complete before the cut")
	}
	tor.AddPeers(peerAddr(s))
	waitDone(t, l, id)
	sameFile(t, filepath.Join(ldl, "half.bin"), data)
	if st := tor.Stats(); st.HashFails != 0 {
		t.Fatalf("%d pieces failed their hash after the cut", st.HashFails)
	}
}

// TestStalledPeerMidPiece: one of two seeders hangs in the middle of a
// piece, its connection open and silent; the download completes from the
// other well before the stalled peer is snubbed (fib's 60s), its blocks
// taken by the other at the end, and the data is whole.
func TestStalledPeerMidPiece(t *testing.T) {
	e := startEngine(t)
	src := t.TempDir()
	torrent, data := payload(t, src, "stall.bin", 2<<20)
	s1, id := seeder(t, e, src, torrent, "")
	s2, _ := seeder(t, e, src, torrent, "")
	stall := startRelay(t, peerAddr(s1), 200_003, true)
	ldata, ldl := t.TempDir(), t.TempDir()
	l := startRole(t, e, clientJSON(ldata, ldl, `"encryption": "allow", "utp": false`), nil)
	if _, err := l.Add(AddRequest{Torrent: torrent, Peers: []string{stall.addr()}}); err != nil {
		t.Fatal(err)
	}
	tor, err := l.Torrent(id)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the stall", func() bool { return stall.acted.Load() })
	start := time.Now()
	tor.AddPeers(peerAddr(s2))
	waitDone(t, l, id)
	if d := time.Since(start); d > 45*time.Second {
		t.Fatalf("the download took %v past a stalled peer", d)
	}
	sameFile(t, filepath.Join(ldl, "stall.bin"), data)
	if st := tor.Stats(); st.HashFails != 0 {
		t.Fatalf("%d pieces failed their hash", st.HashFails)
	}
}

// TestPeersKeptAcrossRestart: the peers a torrent was added with are kept
// in the state file and given to it again at the next start, which a
// private torrent of a cluster's distribution, with no other source of
// peers, needs (I206). Discovery is off here, so they are its only way.
func TestPeersKeptAcrossRestart(t *testing.T) {
	e := startEngine(t)
	src := t.TempDir()
	torrent, data := payload(t, src, "p.bin", 1<<20)
	s, id := seeder(t, e, src, torrent, "")
	ldata, ldl := t.TempDir(), t.TempDir()
	slow := clientJSON(ldata, ldl, `"limits": {"download_rate": 131072}`)
	l := startRole(t, e, slow, nil)
	if _, err := l.Add(AddRequest{Torrent: torrent, Peers: []string{peerAddr(s)}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a piece before the stop", func() bool {
		tor, err := l.Torrent(id)
		return err == nil && tor.Stats().PiecesHave > 0
	})
	l.Stop(context.Background())

	fast := clientJSON(ldata, ldl, "")
	l2 := startRole(t, e, fast, nil)
	waitDone(t, l2, id)
	sameFile(t, filepath.Join(ldl, "p.bin"), data)
}
