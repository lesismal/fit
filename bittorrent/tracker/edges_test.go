package tracker

import (
	"bytes"
	"container/heap"
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lesismal/fib/bittorrent/metainfo"
	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/fibtest"
	"github.com/lesismal/fit/logs"
)

// logBuffer is a log's output, written from the role's goroutines.
type logBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// TestApplyEdges applies members that cannot be applied, each leaving the
// role as it was, then ones that change it in place, as its log says, and
// one after Stop.
func TestApplyEdges(t *testing.T) {
	e := startEngine(t)
	var out logBuffer
	root, err := logs.New(nil, &out, &out)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	// No Upstream: the role is keyed by its engine.
	r := New(&app.Host{ID: ID, Engine: e, Log: root.Logger("bittorrent")})
	t.Cleanup(func() { r.Stop(context.Background()) })

	if err := r.Apply(nil); err == nil || !strings.Contains(err.Error(), "no member") {
		t.Fatalf("a nil member: %v", err)
	}
	if err := r.Apply("tracker"); err == nil || !strings.Contains(err.Error(), "no member") {
		t.Fatalf("a member of another type: %v", err)
	}
	missing := filepath.Join(t.TempDir(), "missing.txt")
	if err := r.Apply(load(t, `{"access": {"allow_files": ["`+missing+`"]}}`, "").BitTorrent.Tracker); err == nil || !strings.HasPrefix(err.Error(), "access:") {
		t.Fatalf("an access file missing: %v", err)
	}
	if err := r.Apply(load(t, `{"allow_files": ["`+missing+`"]}`, "").BitTorrent.Tracker); err == nil || !strings.HasPrefix(err.Error(), "allow-list:") {
		t.Fatalf("an allow file missing: %v", err)
	}
	// A shared store made, then let go: the port does not bind.
	bad := `{"listen": "192.0.2.1:0", "store": {"redis": {"address": "127.0.0.1:1"}, "request_timeout": "200ms"}}`
	if err := r.Apply(load(t, bad, "").BitTorrent.Tracker); err == nil || !strings.Contains(err.Error(), "listen on 192.0.2.1:0") {
		t.Fatalf("a port that does not bind: %v", err)
	}
	// None of them started it.
	h := hashOf("edges")
	if _, err := r.Torrent(h); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Torrent before a start: %v", err)
	}
	if l, err := r.ListTorrents(0, 10, nil); !errors.Is(err, ErrNotRunning) || l.Torrents == nil || len(l.Torrents) != 0 {
		t.Fatalf("ListTorrents before a start: %+v %v", l, err)
	}
	if _, err := r.Stats(); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Stats before a start: %v", err)
	}
	if Lookup(e) != nil {
		t.Fatal("a role that did not start is registered")
	}
	// Datagrams while it does not run are dropped.
	r.udp.OnPriorityData(nil, []byte("x"))
	r.udp.OnData(nil, []byte("x"))

	// A shared store, then none: a new store, which the log tells.
	if err := r.Apply(load(t, `{"listen": "127.0.0.1:0", "store": {"redis": {"address": "127.0.0.1:1"}, "request_timeout": "200ms"}}`, "").BitTorrent.Tracker); err != nil {
		t.Fatal(err)
	}
	if Lookup(e) != r || !r.UDPAddr().IsValid() {
		t.Fatalf("started: %v %v", Lookup(e), r.UDPAddr())
	}
	if err := r.Apply(load(t, `{"listen": "127.0.0.1:0"}`, "").BitTorrent.Tracker); err != nil {
		t.Fatal(err)
	}
	// Bounds of the store in place.
	if err := r.Apply(load(t, `{"listen": "127.0.0.1:0", "max_torrents": 7}`, "").BitTorrent.Tracker); err != nil {
		t.Fatal(err)
	}
	if st, err := r.Stats(); err != nil || st.MaxTorrents != 7 {
		t.Fatalf("stats %+v %v", st, err)
	}
	logged := out.String()
	for _, want := range []string{"udp on 127.0.0.1:", "this instance's swarms start anew", "the store's bounds changed in place"} {
		if !strings.Contains(logged, want) {
			t.Errorf("the log lacks %q:\n%s", want, logged)
		}
	}
	// A tracker with passkeys whose policy the media_auth role has not
	// applied.
	pol := `"media_auth": [{"name": "bt", "secret": "0123456789abcdef0123456789", "users": [{"name": "alice", "password": "alice-password-1", "play": ["*"]}]}]`
	if err := r.Apply(load(t, `{"auth": "bt"}`, pol).BitTorrent.Tracker); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Passkey("alice"); !errors.Is(err, errNoPolicy) {
		t.Fatalf("a passkey without the policy: %v", err)
	}
	// A console that cannot listen: its port is taken.
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	if err := r.Apply(load(t, `{"console": {"address": "`+taken.Addr().String()+`", "users": [{"username": "admin", "password_hash": "`+hashPassword(t, "console-password")+`"}]}}`, "").BitTorrent.Tracker); err == nil || !strings.HasPrefix(err.Error(), "console:") {
		t.Fatalf("a console that does not bind: %v", err)
	}
	r.Stop(context.Background())
	if err := r.Apply(load(t, `{}`, "").BitTorrent.Tracker); !errors.Is(err, app.ErrStopped) {
		t.Fatalf("Apply after Stop: %v", err)
	}
	if Lookup(e) != nil {
		t.Fatal("a stopped role is registered")
	}
}

// TestRoleServeHTTPNotRunning serves an announce through a role that has
// not started: 503, from an address taken from the request alone.
func TestRoleServeHTTPNotRunning(t *testing.T) {
	r := New(&app.Host{ID: ID, Engine: startEngine(t)})
	req, _ := http.NewRequest("GET", "http://fit/announce?info_hash=x", nil)
	req.RequestURI = "/announce?info_hash=x"
	req.RemoteAddr = "[::ffff:127.0.0.1]:4000"
	if ap := remoteOf(&fibhttp.Context{Request: req}); ap.String() != "127.0.0.1:4000" {
		t.Fatalf("the remote of a v4-mapped address: %v", ap)
	}
	rec := &fibtest.Recorder{}
	r.ServeHTTP(fibhttp.NewStreamContext(nil, req, rec))
	if !rec.Wait(hang) {
		t.Fatal("no answer")
	}
	if rec.Response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status %d", rec.Response.StatusCode)
	}
}

// TestAllowListEdges reads allow-lists past their bounds and malformed.
func TestAllowListEdges(t *testing.T) {
	a, b := hashOf("allow a"), hashOf("allow b")
	// The same info hash twice is listed once.
	l, err := loadAllow(&config.BitTorrentTracker{Allow: []string{a.String(), a.String(), b.String()}})
	if err != nil || len(l.list) != 2 || l.list[0] != a || l.list[1] != b {
		t.Fatalf("%+v %v", l, err)
	}
	if _, err := loadAllow(&config.BitTorrentTracker{Allow: []string{a.String(), "zz"}}); err == nil || !strings.Contains(err.Error(), `allow[1]: "zz"`) {
		t.Fatalf("a bad entry: %v", err)
	}

	// A full list takes no more, from the config or a file.
	full := &allowList{set: map[metainfo.InfoHash]struct{}{}, list: make([]metainfo.InfoHash, config.MaxBTTrackerAllow)}
	if err := full.add(a); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("past the bound: %v", err)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "allow.txt")
	if err := os.WriteFile(file, []byte("# list\n"+a.String()+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := full.readFile(file); err == nil || !strings.Contains(err.Error(), "allow.txt:2: more than") {
		t.Fatalf("a file past the bound: %v", err)
	}

	// A line longer than the scanner takes.
	long := filepath.Join(dir, "long.txt")
	if err := os.WriteFile(long, bytes.Repeat([]byte("a"), 70<<10), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAllow(&config.BitTorrentTracker{AllowFiles: []string{long}}); err == nil || !strings.Contains(err.Error(), "long.txt: ") {
		t.Fatalf("a long line: %v", err)
	}
	// A file past the bound of a list file, sparse.
	big := filepath.Join(dir, "big.txt")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(65<<20 + 1); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if _, err := loadAllow(&config.BitTorrentTracker{AllowFiles: []string{big}}); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("a file too large: %v", err)
	}
}

// TestSwarmHeap pops a max-heap of swarms largest first.
func TestSwarmHeap(t *testing.T) {
	var h swarmHeap
	for _, s := range []string{"b", "a", "c"} {
		heap.Push(&h, swarm{h: hashOf(s)})
	}
	for _, want := range []string{"c", "b", "a"} {
		if got := heap.Pop(&h).(swarm); got.h != hashOf(want) {
			t.Fatalf("popped %v, want %s", got.h, want)
		}
	}
	if h.Len() != 0 {
		t.Fatal(h.Len())
	}
}

// TestConsoleAfterAllowList pages an allow-list's torrents by after
// through the console: 400.
func TestConsoleAfterAllowList(t *testing.T) {
	e := startEngine(t)
	h := hashOf("console after")
	r := startRole(t, e, load(t, `{"allow": ["`+h.String()+`"], "console": {"address": "127.0.0.1:0", "users": [{"username": "admin", "password_hash": "`+hashPassword(t, "console-password")+`"}]}}`, ""))
	get := login(t, r.console.URL(), "admin", "console-password")
	if body := get("/api/torrents?after=" + h.String()); !strings.Contains(body, "after pages a public tracker") {
		t.Fatalf("after with an allow-list: %s", body)
	}
}
