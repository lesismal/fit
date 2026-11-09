package btinterop

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/lesismal/fib/bittorrent/metainfo"
)

// The tracker cases run fit's bittorrent.tracker role, private, for other
// clients: peers that find each other only through it, announcing over
// HTTP (through a bt_tracker route) and UDP (BEP 15, the passkey in BEP
// 41's URL data), with a token's passkey and a user's, which fit's Admin
// API hands out; and a client with a passkey fit does not know, refused.

const (
	trackerSecret = "0123456789abcdef0123456789abcdef"
	seedToken     = "token-seed-0123456789abcdef"
	trackerAuth   = `[{"name": "bt", "secret": "` + trackerSecret + `",
	"tokens": [{"name": "seeder", "token": "` + seedToken + `", "play": ["bittorrent/*"]}],
	"users": [{"name": "leech", "password": "leech-password-1", "play": ["bittorrent/*"]}]}]`
	trackerRoutes = `[{"match": {"path": ["/tracker/*"]}, "handler": {"module": "bt_tracker"}}]`
	trackerMember = `{"listen": "UDP", "auth": "bt", "interval": "5s", "min_interval": "1s"}`
)

type trackerStats struct {
	Announces uint64            `json:"announces"`
	Refused   map[string]uint64 `json:"refused"`
	Private   bool              `json:"private"`
}

func (f *fitProc) trackerStats(t *testing.T) trackerStats {
	t.Helper()
	var s trackerStats
	f.call(t, http.MethodGet, "/bittorrent/tracker/stats", nil, http.StatusOK, &s)
	return s
}

func (f *fitProc) passkey(t *testing.T, user string) string {
	t.Helper()
	var out struct {
		Passkey string `json:"passkey"`
	}
	f.call(t, http.MethodGet, "/bittorrent/tracker/passkey?user="+user, nil, http.StatusOK, &out)
	if len(out.Passkey) != 32 {
		t.Fatalf("passkey %q", out.Passkey)
	}
	return out.Passkey
}

// waitRefused waits for the tracker to have refused a passkey.
func (f *fitProc) waitRefused(t *testing.T, reason string) {
	t.Helper()
	deadline := time.Now().Add(hang)
	for f.trackerStats(t).Refused[reason] == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the tracker refused no %s: %+v", reason, f.trackerStats(t))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// trackerTorrents builds the same private torrent with each announce URL:
// the info, and so the info hash, is the same, only the tracker differs.
func trackerTorrents(t *testing.T, path string, v metainfo.Version, urls ...string) []*metainfo.MetaInfo {
	t.Helper()
	var out []*metainfo.MetaInfo
	for _, u := range urls {
		out = append(out, buildTorrent(t, path, v, true, u))
	}
	for _, mi := range out[1:] {
		if torrentID(mi) != torrentID(out[0]) {
			t.Fatal("the torrents differ in their info")
		}
	}
	return out
}

// TestTrackerAnacrolix: an anacrolix seeder announcing to fit over HTTP
// with a token's passkey, an anacrolix leecher over UDP with a user's,
// finding each other through fit alone; another with a bad passkey.
func TestTrackerAnacrolix(t *testing.T) {
	f := startFit(t, fitConfig{tracker: trackerMember, routes: trackerRoutes, mediaAuth: trackerAuth})
	pk := f.passkey(t, "leech")
	// v1: anacrolix 1.61 panics in its announcer (panicif.False in
	// updateTorrentInput) while it checks a hybrid torrent it announces
	// under both hashes.
	c := clientCase{name: "tracker", version: metainfo.V1}
	sdir := t.TempDir()
	files := clientFiles(t, sdir, "content")
	mis := trackerTorrents(t, filepath.Join(sdir, "content"), c.version,
		f.web+"/tracker/"+seedToken+"/announce",
		fmt.Sprintf("udp://127.0.0.1:%d/%s/announce", f.udp, pk),
		f.web+"/tracker/00000000000000000000000000000000/announce")

	seeder := newAnacrolix(t, sdir, c, anacrolixOpts{seed: true, trackers: true})
	anacrolixSeed(t, seeder, mis[0])

	cl := newAnacrolix(t, t.TempDir(), c, anacrolixOpts{trackers: true})
	at, err := cl.AddTorrent(anacrolixTorrent(t, mis[1]))
	if err != nil {
		t.Fatal(err)
	}
	at.DownloadAll()
	ctx, cancel := context.WithTimeout(context.Background(), hang)
	defer cancel()
	select {
	case <-at.Complete().On():
	case <-ctx.Done():
		t.Fatalf("anacrolix incomplete: %d of %d bytes; tracker %+v", at.BytesCompleted(), mis[1].Info.TotalLength(), f.trackerStats(t))
	}
	anacrolixCheck(t, at, files, "content")

	bad := newAnacrolix(t, t.TempDir(), c, anacrolixOpts{trackers: true})
	if _, err := bad.AddTorrent(anacrolixTorrent(t, mis[2])); err != nil {
		t.Fatal(err)
	}
	f.waitRefused(t, "passkey")
	if s := f.trackerStats(t); !s.Private || s.Announces < 2 {
		t.Fatalf("tracker stats %+v", s)
	}
}

// TestTrackerLibtorrent: a libtorrent seeder announcing to fit over HTTP
// with a token's passkey; fit's own client (in the same fit) downloading
// it, found through the tracker over UDP with a user's passkey; a
// libtorrent leecher over HTTP with the user's passkey; and libtorrent
// with a bad passkey, refused.
func TestTrackerLibtorrent(t *testing.T) {
	needLibtorrent(t)
	for name, v := range map[string]metainfo.Version{"v1": metainfo.V1, "hybrid": metainfo.Hybrid, "v2": metainfo.V2} {
		t.Run(name, func(t *testing.T) {
			ldir := t.TempDir()
			c := clientCase{name: "tracker", version: v, enc: encPrefer}
			f := startFit(t, fitConfig{client: fitClient(t, c, ldir), tracker: trackerMember, routes: trackerRoutes, mediaAuth: trackerAuth})
			pk := f.passkey(t, "leech")
			sdir := t.TempDir()
			files := clientFiles(t, sdir, "content")
			mis := trackerTorrents(t, filepath.Join(sdir, "content"), v,
				f.web+"/tracker/"+seedToken+"/announce",
				fmt.Sprintf("udp://127.0.0.1:%d/%s/announce", f.udp, pk),
				f.web+"/tracker/"+pk+"/announce",
				fmt.Sprintf("udp://127.0.0.1:%d/00000000000000000000000000000000/announce", f.udp))

			startLibtorrent(t, "seed", writeTorrent(t, mis[0]), sdir)
			st := f.add(t, map[string]any{"torrent": marshal(t, mis[1])})
			lleech := t.TempDir()
			lt := startLibtorrent(t, "leech", writeTorrent(t, mis[2]), lleech)
			f.waitComplete(t, st.ID)
			sameFiles(t, ldir, files)
			lt.wait(t, "the download through the tracker")
			sameFiles(t, lleech, files)

			startLibtorrent(t, "leech", writeTorrent(t, mis[3]), t.TempDir())
			f.waitRefused(t, "passkey")
		})
	}
}
