package sharedstore

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fib/bittorrent/metainfo"
	fibtracker "github.com/lesismal/fib/bittorrent/tracker"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/sync/redis/wire"
)

// redisImage is the Redis the store is tested against, as tlsmgr's
// sharedstore's.
const redisImage = "redis:8.2.2"

// redisServer runs Redis in Docker with a password, its port on
// 127.0.0.1, and returns its config and container ID, skipping the test
// without Docker.
func redisServer(t *testing.T) (*config.RedisStorage, string) {
	t.Helper()
	if runtime.GOOS == "windows" || os.Getenv("FIT_E2E_DOCKER") == "0" {
		t.Skip("no Docker here")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("no Docker daemon: %v", err)
	}
	if exec.Command("docker", "image", "inspect", redisImage).Run() != nil {
		if out, err := exec.Command("docker", "pull", redisImage).CombinedOutput(); err != nil {
			t.Fatalf("docker pull: %v\n%s", err, out)
		}
	}
	out, err := exec.Command("docker", "run", "-d", "--rm", "-p", "127.0.0.1::6379", redisImage,
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
	addr, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	pw := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(pw, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &config.RedisStorage{Address: addr, PasswordFile: pw, DB: 3}
	o := wire.Options{Addr: addr, Password: "s3cret", Protocol: 2}
	waitUntil(t, "Redis to start", func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		conn, err := wire.Dial(ctx, o)
		if err != nil {
			return false
		}
		defer conn.Close()
		v, err := conn.Do(ctx, "PING")
		return err == nil && v.Str == "PONG"
	})
	return c, id
}

// openRedis opens a store on c under prefix, closed when the test ends.
func openRedis(t *testing.T, c *config.RedisStorage, prefix string, cfg Config) *Store {
	t.Helper()
	s, err := NewRedis(c, prefix, cfg, logs.Func(t.Logf))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

// TestRedis runs the store's cases against a real Redis, each under a
// prefix of its own.
func TestRedis(t *testing.T) {
	c, id := redisServer(t)
	open := func(t *testing.T) func(Config) *Store {
		prefix := "test/" + t.Name() + "/"
		return func(cfg Config) *Store { return openRedis(t, c, prefix, cfg) }
	}
	t.Run("two trackers", func(t *testing.T) { t.Parallel(); testTwoTrackers(t, open(t)) })
	t.Run("bounds", func(t *testing.T) { t.Parallel(); testSharedBounds(t, open(t)) })
	t.Run("expiry", func(t *testing.T) { t.Parallel(); testExpiry(t, open(t)) })
	t.Run("range", func(t *testing.T) { t.Parallel(); testRange(t, open(t)) })
	t.Run("addresses", func(t *testing.T) { t.Parallel(); testAddresses(t, open(t)) })
	// Alone, before the parallel cases run (they wait for this function to
	// return): it pauses the server.
	t.Run("unreachable", func(t *testing.T) {
		testUnreachable(t, open(t), func(up bool) {
			op := "pause"
			if up {
				op = "unpause"
			}
			if out, err := exec.Command("docker", op, id).CombinedOutput(); err != nil {
				t.Fatalf("docker %s: %v\n%s", op, err, out)
			}
		})
	})
}

// TestRedisTLSFiles: a TLS file that does not read refuses the store.
func TestRedisTLSFiles(t *testing.T) {
	dir := t.TempDir()
	if _, err := NewRedis(&config.RedisStorage{TLS: true, CAFile: filepath.Join(dir, "missing.pem")}, "p/", Config{}, nil); err == nil {
		t.Fatal("a missing CA file was taken")
	}
	bad := filepath.Join(dir, "bad.pem")
	if err := os.WriteFile(bad, []byte("not pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRedis(&config.RedisStorage{TLS: true, CAFile: bad}, "p/", Config{}, nil); err == nil {
		t.Fatal("a CA file without certificates was taken")
	}
	if _, err := NewRedis(&config.RedisStorage{TLS: true, CertFile: bad, KeyFile: bad}, "p/", Config{}, nil); err == nil {
		t.Fatal("a bad client certificate was taken")
	}
	s, err := NewRedis(&config.RedisStorage{Address: "127.0.0.1:1", TLS: true}, "p/", Config{Timeout: 100 * time.Millisecond}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Kind() != "redis" {
		t.Fatal(s.Kind())
	}
	waitUntil(t, "the store to fail", func() bool { st := s.Status(); return st.Error != "" && !st.Reachable })
}

// TestAddrsCodec round-trips the packed addresses and refuses bad ones.
func TestAddrsCodec(t *testing.T) {
	v4 := netip.MustParseAddrPort("10.1.2.3:6881")
	v6 := netip.MustParseAddrPort("[2001:db8::1]:51413")
	for _, c := range []struct{ v4, v6 netip.AddrPort }{{v4, v6}, {v4, netip.AddrPort{}}, {netip.AddrPort{}, v6}, {}} {
		a, b, ok := decodeAddrs(encodeAddrs(c.v4, c.v6))
		if !ok || a != c.v4 || b != c.v6 {
			t.Fatalf("%v %v: got %v %v %v", c.v4, c.v6, a, b, ok)
		}
	}
	for _, bad := range []string{"", "\x01abc", "\x02" + strings.Repeat("x", 17), "\x04", "\x01" + strings.Repeat("x", 7)} {
		if _, _, ok := decodeAddrs(bad); ok {
			t.Fatalf("%q decoded", bad)
		}
	}
}

// TestParseSample refuses replies that are not a sample.
func TestParseSample(t *testing.T) {
	now := time.Now()
	ints := func(n ...int64) []wire.Value {
		var out []wire.Value
		for _, x := range n {
			out = append(out, wire.Value{Kind: wire.KindInt, Int: x})
		}
		return out
	}
	for _, v := range []wire.Value{
		{Kind: wire.KindInt},
		{Kind: wire.KindArray, Elems: ints(1, 2, 3)},
		{Kind: wire.KindArray, Elems: append(ints(1, 2, 3), wire.Value{Kind: wire.KindBulk}, wire.Value{}, wire.Value{})},
	} {
		if parseSample(v, now) != nil {
			t.Fatalf("%+v parsed", v)
		}
	}
	id := strings.Repeat("i", 20)
	addr := encodeAddrs(netip.MustParseAddrPort("10.0.0.1:1"), netip.AddrPort{})
	list := wire.Value{Kind: wire.KindArray, Elems: []wire.Value{
		{Kind: wire.KindBulk, Str: id}, {Kind: wire.KindBulk, Str: "2000"}, {Kind: wire.KindBulk, Str: addr},
		{Kind: wire.KindBulk, Str: "short"}, {Kind: wire.KindBulk, Str: "2000"}, {Kind: wire.KindBulk, Str: addr},
		{Kind: wire.KindBulk, Str: id}, {Kind: wire.KindBulk, Str: "x"}, {Kind: wire.KindBulk, Str: addr},
		{Kind: wire.KindBulk, Str: id}, {Kind: wire.KindBulk, Str: "2000"}, {Kind: wire.KindBulk, Str: "\x09"},
	}}
	sn := parseSample(wire.Value{Kind: wire.KindArray, Elems: append(ints(1000, 1, 0, 4), list, wire.Value{Kind: wire.KindArray})}, now)
	if sn == nil || len(sn.lists[1]) != 1 || len(sn.lists[0]) != 0 || sn.stats.Downloaded != 4 ||
		sn.lists[1][0].expires.Sub(now) != time.Second {
		t.Fatalf("%+v", sn)
	}
}

// fastConfig refreshes often, for tests.
func fastConfig() Config {
	return Config{Refresh: 50 * time.Millisecond, Timeout: 5 * time.Second}
}

// ttlConfig is fastConfig with peers expiring in a second.
func ttlConfig() Config {
	c := fastConfig()
	c.Bounds.PeerTTL = time.Second
	return c
}

func hashOf(s string) metainfo.InfoHash {
	var h metainfo.InfoHash
	copy(h[:], s)
	return h
}

func peerID(i int) fibtracker.PeerID {
	var id fibtracker.PeerID
	copy(id[:], fmt.Sprintf("-FB0001-%012d", i))
	return id
}

// ann is peer i's announce of h from 10.0.0.i, a seeder or not.
func ann(h metainfo.InfoHash, i int, seeder bool, ev fibtracker.Event) fibtracker.StoreAnnounce {
	return fibtracker.StoreAnnounce{
		InfoHash: h, PeerID: peerID(i), V4: netip.AddrPortFrom(netip.AddrFrom4([4]byte{10, 0, byte(i >> 8), byte(i)}), 6881),
		Seeder: seeder, Event: ev, NumWant: 50, WantV4: true,
	}
}

func ids(peers []fibtracker.Peer) []fibtracker.PeerID {
	var out []fibtracker.PeerID
	for _, p := range peers {
		out = append(out, p.ID)
	}
	slices.SortFunc(out, func(a, b fibtracker.PeerID) int { return strings.Compare(string(a[:]), string(b[:])) })
	return slices.Compact(out)
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func mustAnnounce(t *testing.T, s *Store, a fibtracker.StoreAnnounce) fibtracker.StoreResult {
	t.Helper()
	res, err := s.Announce(a)
	if err != nil {
		t.Fatalf("announce of %s: %v", a.PeerID, err)
	}
	return res
}
