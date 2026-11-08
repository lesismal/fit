package bittorrent

import (
	"bytes"
	"context"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fib"
	fibbt "github.com/lesismal/fib/bittorrent"
	"github.com/lesismal/fib/bittorrent/bencode"
	"github.com/lesismal/fib/bittorrent/metainfo"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
)

// fibPeer is a peer as fib reports it.
type fibPeer = fibbt.PeerInfo

// hang bounds every wait of these tests: a guard against a hang, never a
// measure of speed, so it is generous for slow CI runners.
const hang = 90 * time.Second

// startEngine starts an engine the roles share, as fit's listeners' is.
func startEngine(t *testing.T) *fib.Engine {
	t.Helper()
	cfg := fib.DefaultConfig()
	cfg.LogSettings = false
	e, err := fib.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- e.Run() }()
	t.Cleanup(func() {
		e.Stop()
		<-done
		_ = e.Close()
	})
	return e
}

// clientJSON is a member for tests: on loopback, no DHT, no LSD, a short
// save interval; extra is more of its fields.
func clientJSON(dataDir, downloadDir, extra string) string {
	s := `{"listen": "127.0.0.1:0", "data_dir": ` + strconv.Quote(dataDir) + `, "dht": false, "save_interval": "50ms"`
	if downloadDir != "" {
		s += `, "download_dir": ` + strconv.Quote(downloadDir)
	}
	if extra != "" {
		s += ", " + extra
	}
	return s + "}"
}

// member loads the config of a bittorrent.client member.
func member(t *testing.T, js string) *config.BitTorrentClient {
	t.Helper()
	c, err := config.Load([]byte(`{"bittorrent": {"client": ` + js + `}}`))
	if err != nil {
		t.Fatal(err)
	}
	return c.BitTorrent.Client
}

// readCounter counts the reads of the storages it wraps.
type readCounter struct {
	mu    sync.Mutex
	reads int
}

func (rc *readCounter) total() int {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.reads
}

type countingStorage struct {
	fibbt.Storage
	rc *readCounter
}

func (s *countingStorage) ReadAt(file int, p []byte, off int64) (int, error) {
	s.rc.mu.Lock()
	s.rc.reads++
	s.rc.mu.Unlock()
	return s.Storage.ReadAt(file, p, off)
}

func (s *countingStorage) Stat(file int) (int64, time.Time, error) {
	return s.Storage.(fibbt.StatStorage).Stat(file)
}

func (s *countingStorage) Remove() error { return s.Storage.(fibbt.StorageRemover).Remove() }

func (rc *readCounter) wrap(o fibbt.StorageOpener) fibbt.StorageOpener {
	return func(info *metainfo.Info) (fibbt.Storage, error) {
		st, err := o(info)
		if err != nil {
			return nil, err
		}
		return &countingStorage{st, rc}, nil
	}
}

// startRole starts a role on e with the member js, its storage wrapped by
// rc when not nil, and stops it when the test ends.
func startRole(t *testing.T, e *fib.Engine, js string, rc *readCounter) *Role {
	t.Helper()
	r := New(&app.Host{ID: ID, Engine: e, Upstream: e})
	if rc != nil {
		r.wrap = rc.wrap
	}
	if err := r.Apply(member(t, js)); err != nil {
		r.Stop(context.Background())
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Stop(context.Background()) })
	return r
}

// payload writes a file of n random bytes as dir/name and returns its
// .torrent file's bytes.
func payload(t *testing.T, dir, name string, n int) ([]byte, []byte) {
	t.Helper()
	data := make([]byte, n)
	rnd := rand.New(rand.NewPCG(uint64(n), 7))
	for i := range data {
		data[i] = byte(rnd.Uint32())
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	b := metainfo.Builder{Version: metainfo.Hybrid, PieceLength: 32 << 10}
	mi, err := b.BuildPath(p)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := bencode.Marshal(mi)
	if err != nil {
		t.Fatal(err)
	}
	return raw, data
}

// seeder starts a role seeding the file of torrent in dir.
func seeder(t *testing.T, e *fib.Engine, dir string, torrent []byte, extra string) (*Role, string) {
	t.Helper()
	r := startRole(t, e, clientJSON(t.TempDir(), dir, extra), nil)
	s, err := r.Add(AddRequest{Torrent: torrent})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the seeder to have every piece", func() bool {
		tor, err := r.Torrent(s.ID)
		return err == nil && tor.HaveAll()
	})
	return r, s.ID
}

// peerAddr is where r takes peers, on loopback.
func peerAddr(r *Role) string { return "127.0.0.1:" + strconv.Itoa(int(r.Addr().Port())) }

// waitFor waits for cond, polling, failing the test after hang.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(hang)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitDone waits for the torrent id of r to download.
func waitDone(t *testing.T, r *Role, id string) {
	t.Helper()
	tor, err := r.Torrent(id)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-tor.Done():
	case <-time.After(hang):
		s, _ := r.Get(id)
		t.Fatalf("the download did not finish: %+v", s)
	}
}

// sameFile fails the test unless the file at p holds want.
func sameFile(t *testing.T, p string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s differs: %d bytes, want %d", p, len(got), len(want))
	}
}

// freePort returns a port that TCP and UDP are both free on, on loopback,
// as far as can be told.
func freePort(t *testing.T) int {
	t.Helper()
	for range 20 {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := l.Addr().(*net.TCPAddr).Port
		u, err := net.ListenPacket("udp", "127.0.0.1:"+strconv.Itoa(port))
		_ = l.Close()
		if err == nil {
			_ = u.Close()
			return port
		}
	}
	t.Fatal("no free port")
	return 0
}
