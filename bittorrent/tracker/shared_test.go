package tracker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fib/bittorrent/metainfo"
	fibtracker "github.com/lesismal/fib/bittorrent/tracker"

	"github.com/lesismal/fit/bittorrent/tracker/sharedstore"
	"github.com/lesismal/fit/observe"
	"github.com/lesismal/fit/sync/redis/wire"
)

// startRedis runs Redis in Docker with a password and returns its address
// and a file holding the password, skipping the test without Docker.
func startRedis(t *testing.T) (addr, pwFile string) {
	t.Helper()
	const image = "redis:8.2.2"
	if runtime.GOOS == "windows" || os.Getenv("FIT_E2E_DOCKER") == "0" {
		t.Skip("no Docker here")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("no Docker daemon: %v", err)
	}
	if exec.Command("docker", "image", "inspect", image).Run() != nil {
		if out, err := exec.Command("docker", "pull", image).CombinedOutput(); err != nil {
			t.Fatalf("docker pull: %v\n%s", err, out)
		}
	}
	out, err := exec.Command("docker", "run", "-d", "--rm", "-p", "127.0.0.1::6379", image,
		"redis-server", "--requirepass", "s3cret").CombinedOutput()
	if err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", id).Run() })
	out, err = exec.Command("docker", "port", id, "6379/tcp").CombinedOutput()
	if err != nil {
		t.Fatalf("docker port: %v\n%s", err, out)
	}
	addr, _, _ = strings.Cut(strings.TrimSpace(string(out)), "\n")
	pwFile = filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(pwFile, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "Redis to start", func() bool {
		c, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		conn, err := wire.Dial(c, wire.Options{Addr: addr, Password: "s3cret", Protocol: 2})
		if err != nil {
			return false
		}
		defer conn.Close()
		v, err := conn.Do(c, "PING")
		return err == nil && v.Str == "PONG"
	})
	return addr, pwFile
}

// sharedMember is a tracker member on the Redis at addr under prefix.
func sharedMember(addr, pwFile, prefix, more string) string {
	return fmt.Sprintf(`{"listen": "127.0.0.1:0", "store": {"redis": {"address": %q, "password_file": %q}, "prefix": %q, "refresh": "100ms"}%s}`,
		addr, pwFile, prefix, more)
}

// TestSharedStore: two tracker roles, on engines of their own as two fit
// instances would be, sharing one Redis, answer as one tracker: a leecher
// announcing to one over UDP gets a seeder that announced to the other over
// HTTP, and the other way round; scrapes, statistics, the list and the
// metrics count both. A reload without the store block goes back to
// memory.
func TestSharedStore(t *testing.T) {
	addr, pw := startRedis(t)
	member := sharedMember(addr, pw, "test/"+t.Name()+"/", "")
	e1, e2 := startEngine(t), startEngine(t)
	r1 := startRole(t, e1, load(t, member, ""))
	r2 := startRole(t, e2, load(t, member, ""))
	u1 := serveHTTP(t, e1) + "/announce"
	cl := newClient(t, e1, time.Second, 8)
	h := hashOf("shared store")

	mustAnnounce(t, cl, u1, h, 1, 0, fibtracker.EventStarted)
	hasPeer := func(resp *fibtracker.AnnounceResponse, i int) bool {
		for _, p := range resp.Peers {
			if p.Addr.Port() == uint16(20000+i) {
				return true
			}
		}
		return false
	}
	waitFor(t, "the leecher on r2 to get the seeder on r1", func() bool {
		return hasPeer(mustAnnounce(t, cl, udpURL(r2, "/announce"), h, 2, 100, fibtracker.EventNone), 1)
	})
	waitFor(t, "the seeder on r1 to get the leecher on r2", func() bool {
		return hasPeer(mustAnnounce(t, cl, u1, h, 1, 0, fibtracker.EventNone), 2)
	})
	waitFor(t, "a scrape of r2 to count both", func() bool {
		files, err := cl.Scrape(ctx(t), udpURL(r2, "/announce"), []metainfo.InfoHash{h})
		return err == nil && files[h].Complete == 1 && files[h].Incomplete == 1
	})
	waitFor(t, "r1's statistics to count both", func() bool {
		st, err := r1.Stats()
		return err == nil && st.Store != nil && st.Store.Reachable && st.Torrents == 1 && st.Peers == 2 && st.Store.LocalPeers == 1
	})
	l, err := r2.ListTorrents(0, 10, nil)
	if err != nil || l.Total != 1 || len(l.Torrents) != 1 || l.Torrents[0].Seeders != 1 || l.Torrents[0].Leechers != 1 {
		t.Fatalf("r2's list %+v %v", l, err)
	}
	w := &observe.TextWriter{}
	r1.WriteMetrics(w)
	for _, want := range []string{"fit_bittorrent_tracker_store_reachable 1\n", "fit_bittorrent_tracker_peers 2\n", "fit_bittorrent_tracker_store_written_total "} {
		if !strings.Contains(string(w.B), want) {
			t.Errorf("no %q in\n%s", want, w.B)
		}
	}

	// A new prefix makes a new shared store; no store block, one in memory.
	old := r1.cur.Load().store
	if err := r1.Apply(load(t, sharedMember(addr, pw, "test/"+t.Name()+"/other/", ""), "").BitTorrent.Tracker); err != nil {
		t.Fatal(err)
	}
	if cur := r1.cur.Load().store; cur == old {
		t.Fatal("a new prefix kept the store")
	}
	if err := r1.Apply(load(t, `{"listen": "127.0.0.1:0"}`, "").BitTorrent.Tracker); err != nil {
		t.Fatal(err)
	}
	if _, ok := r1.cur.Load().store.(*fibtracker.MemoryStore); !ok {
		t.Fatal("no store block kept the shared store")
	}
	if st, _ := r1.Stats(); st.Store != nil {
		t.Fatalf("statistics of a store in memory %+v", st)
	}
}

// TestSharedStoreUnreachable: a tracker whose shared store cannot be
// reached answers from memory and says so; a store that cannot be made
// leaves the role as it was.
func TestSharedStoreUnreachable(t *testing.T) {
	e := startEngine(t)
	r := startRole(t, e, load(t, `{"store": {"redis": {"address": "127.0.0.1:1"}, "request_timeout": "200ms", "refresh": "100ms"}}`, ""))
	u := serveHTTP(t, e) + "/announce"
	cl := newClient(t, e, time.Second, 8)
	h := hashOf("unreachable")
	mustAnnounce(t, cl, u, h, 1, 0, fibtracker.EventStarted)
	if resp := mustAnnounce(t, cl, u, h, 2, 100, fibtracker.EventStarted); len(resp.Peers) != 1 {
		t.Fatalf("not answered from memory: %+v", resp)
	}
	waitFor(t, "the store to be unreachable", func() bool {
		st, _ := r.Stats()
		return st.Store != nil && !st.Store.Reachable && st.Store.Error != "" && st.Torrents == 1 && st.Peers == 2
	})
	store := r.cur.Load().store
	bad := `{"store": {"redis": {"address": "127.0.0.1:1", "tls": true, "ca_file": "` + filepath.Join(t.TempDir(), "missing.pem") + `"}}}`
	if err := r.Apply(load(t, bad, "").BitTorrent.Tracker); err == nil || !strings.Contains(err.Error(), "store") {
		t.Fatalf("a missing CA file: %v", err)
	}
	if r.cur.Load().store != store {
		t.Fatal("a store that could not be made replaced the one before")
	}
	if _, ok := store.(*sharedstore.Store); !ok {
		t.Fatalf("store %T", store)
	}
}
