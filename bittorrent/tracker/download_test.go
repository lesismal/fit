package tracker

import (
	"bytes"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lesismal/fib"
	fibbt "github.com/lesismal/fib/bittorrent"
	"github.com/lesismal/fib/bittorrent/metainfo"
)

// TestDownloadThroughTracker has two fib BitTorrent clients find each
// other through the role alone: a private torrent (no DHT, PEX or LSD), the
// seeder announcing over HTTP through bt_tracker and the leecher over UDP,
// to a private tracker that takes their passkeys.
func TestDownloadThroughTracker(t *testing.T) {
	e := startEngine(t)
	auth := `"media_auth": [{"name": "bt", "secret": "0123456789abcdef0123456789",
		"tokens": [{"token": "token-seed-0123456789", "play": ["bittorrent/*"]}],
		"users": [{"name": "leech", "password": "leech-password-1", "play": ["bittorrent/*"]}]}]`
	r := startRole(t, e, load(t, `{"listen": "127.0.0.1:0", "auth": "bt"}`, auth))
	base := serveHTTP(t, e)
	pk, err := r.Passkey("leech")
	if err != nil {
		t.Fatal(err)
	}

	src := t.TempDir()
	data := make([]byte, 768<<10+321)
	rnd := rand.New(rand.NewPCG(1, 2))
	for i := range data {
		data[i] = byte(rnd.Uint32())
	}
	p := filepath.Join(src, "file.bin")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	build := func(tracker string) *metainfo.MetaInfo {
		b := metainfo.Builder{Version: metainfo.V1, PieceLength: 32 << 10, Private: true, Trackers: [][]string{{tracker}}}
		mi, err := b.BuildPath(p)
		if err != nil {
			t.Fatal(err)
		}
		return mi
	}
	seedMI := build(base + "/token-seed-0123456789/announce")
	leechMI := build(udpURL(r, "/"+pk+"/announce"))
	h := seedMI.Info.InfoHashV1()
	if leechMI.Info.InfoHashV1() != h {
		t.Fatal("the two .torrent files differ in their info")
	}

	seed := newBTClient(t, e, src)
	st, err := seed.AddTorrent(seedMI, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the seeder to have every piece", st.HaveAll)
	waitFor(t, "the seeder's announce", func() bool {
		ts, err := r.Torrent(h)
		return err == nil && ts.Seeders == 1
	})

	leechDir := t.TempDir()
	leech := newBTClient(t, e, leechDir)
	lt, err := leech.AddTorrent(leechMI, nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-lt.Done():
	case <-time.After(hang):
		t.Fatalf("the download did not finish: %+v", lt.Stats())
	}
	got, err := os.ReadFile(filepath.Join(leechDir, "file.bin"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("the file downloaded differs (%d bytes, %v)", len(got), err)
	}
	// The leecher announces its completion: two seeders. (fib's seeder
	// counts as a completion too: it announces started while it checks its
	// data, and completed once the check is done.)
	waitFor(t, "the leecher's completion", func() bool {
		ts, _ := r.Torrent(h)
		return ts.Seeders == 2 && ts.Leechers == 0 && ts.Completed >= 1
	})
}

// newBTClient starts a fib BitTorrent client on e, storing in dir, that
// finds peers through trackers alone.
func newBTClient(t *testing.T, e *fib.Engine, dir string) *fibbt.Client {
	t.Helper()
	c, err := fibbt.NewClient(fibbt.Config{
		DataDir: dir, ListenAddr: "127.0.0.1:0", SharedEngine: e,
		DisableDHT: true, DisablePEX: true, DisableLSD: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}
