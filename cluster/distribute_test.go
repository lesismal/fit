package cluster

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/admin"
	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/bittorrent"
	"github.com/lesismal/fit/config"
	_ "github.com/lesismal/fit/handler/respond"
	"github.com/lesismal/fit/internal/pebbletest"
	"github.com/lesismal/fit/listener"
	"github.com/lesismal/fit/observe"
	"github.com/lesismal/fit/runtime"
)

// btNode is a fit with the bittorrent.client role and its Admin API, on an
// engine of its own, which a manager reaches by the token fakeToken.
type btNode struct {
	url  string
	role *bittorrent.Role
	dl   string
}

// startBTNode starts a member whose client has the category models; extra
// adds to the client's member, as a publish entry.
func startBTNode(t *testing.T, extra string) *btNode {
	t.Helper()
	e := testEngine(t)
	dl := t.TempDir()
	js := `{"listen": "127.0.0.1:0", "data_dir": ` + strconv.Quote(t.TempDir()) + `, "download_dir": ` + strconv.Quote(dl) +
		`, "dht": false, "save_interval": "50ms", "categories": [{"name": "models", "dir": "models"}]` + extra + `}`
	c, err := config.Load([]byte(`{"bittorrent": {"client": ` + js + `}}`))
	if err != nil {
		t.Fatal(err)
	}
	role := bittorrent.New(&app.Host{ID: bittorrent.ID, Engine: e, Upstream: e})
	if err := role.Apply(c.BitTorrent.Client); err != nil {
		role.Stop(context.Background())
		t.Fatal(err)
	}
	t.Cleanup(func() { role.Stop(context.Background()) })
	data := []byte(`{"listeners": [{"name": "web", "address": "127.0.0.1:0"}], "routes": [{"handler": {"module": "respond", "params": {"body": "x"}}}]}`)
	doc, err := config.Load(data)
	if err != nil {
		t.Fatal(err)
	}
	env := runtime.Env{Metrics: observe.NewMetrics(), Engine: e}
	g, err := runtime.Compile(doc, env)
	if err != nil {
		t.Fatal(err)
	}
	rt := new(runtime.Runtime)
	rt.Swap(g)
	srv, err := listener.Start(rt)
	if err != nil {
		t.Fatal(err)
	}
	tok := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tok, []byte(fakeToken), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &config.Admin{Address: "127.0.0.1:0", TokenFile: tok}
	api, err := admin.Start(a, admin.NewConfigs(rt, srv, env, doc, data, admin.SourceFile, a.HistoryLen()), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = api.Shutdown(context.Background())
		_ = srv.Close()
		rt.Close()
	})
	return &btNode{url: "http://" + api.Addr().String(), role: role, dl: dl}
}

// startSource starts a member that publishes dir, privately or not, with a
// file of size bytes in it, and waits for its torrent to seed.
func startSource(t *testing.T, private bool, size int) (*btNode, []byte) {
	t.Helper()
	dir := t.TempDir()
	content := make([]byte, size)
	_, _ = rand.Read(content)
	file := filepath.Join(dir, "model.bin")
	if err := os.WriteFile(file, content, 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(file, old, old)
	n := startBTNode(t, `, "publish": [{"name": "rel", "dir": `+strconv.Quote(dir)+`, "check_interval": "50ms", "private": `+strconv.FormatBool(private)+`}]`)
	waitFor(t, "the source to seed", func() bool {
		st := n.role.Publishing([]string{"rel"})[0]
		return len(st.Torrents) == 1 && st.Torrents[0].State == "seeding"
	})
	return n, content
}

// distFixture is a manager whose distributions poll fast.
func distFixture(t *testing.T) *fixture {
	t.Helper()
	old := distPoll
	distPoll = 50 * time.Millisecond
	t.Cleanup(func() { distPoll = old })
	return newFixture(t, config.Cluster{})
}

// waitDist waits for the distribution id to finish.
func (f *fixture) waitDist(id string) Dist {
	f.t.Helper()
	var d Dist
	waitFor(f.t, "the distribution "+id, func() bool {
		var ok bool
		d, ok = f.m.DistByID(id)
		return ok && d.Finished
	})
	return d
}

func targetOf(t *testing.T, d Dist, name string) DistTarget {
	t.Helper()
	for _, x := range d.Targets {
		if x.Node == name {
			return x
		}
	}
	t.Fatalf("no target %s in %+v", name, d)
	return DistTarget{}
}

// TestDistribute spreads a privately published file from one member to two
// others, in-process fits on loopback: each downloads it whole into its
// category's directory, the torrent is removed from them at the end with
// their files kept, and the distribution is in the audit log. A third
// target, a member without the client role, fails alone. Then again with
// keep_seeding, through the module's API: the targets keep seeding.
func TestDistribute(t *testing.T) {
	src, content := startSource(t, true, 3<<20+12345)
	a, b := startBTNode(t, ""), startBTNode(t, "")
	bare := newFakeMember(t, false)
	bare.set(func(f *fakeMember) { f.caps, f.bt = btCaps, `{"client":null,"tracker":null}` })
	f := distFixture(t)
	f.add(tokenInput("src", src.url))
	f.add(tokenInput("a", a.url))
	f.add(tokenInput("b", b.url))
	f.add(tokenInput("bare", bare.srv.URL))
	for _, n := range []string{"src", "a", "b", "bare"} {
		f.waitUp(n)
	}
	d, err := f.m.StartDist("tester", &DistRequest{Source: "src", Publish: "rel", Members: []string{"a", "b", "bare", "src"}, Category: "models"})
	if err != nil {
		t.Fatal(err)
	}
	if d.State != DistPreparing || len(d.Targets) != 3 || d.Counts.Pending != 3 {
		t.Fatalf("started %+v", d)
	}
	d = f.waitDist(d.ID)
	if d.State != DistFailed || d.Counts.Done != 2 || d.Counts.Failed != 1 || d.Torrent == nil || d.Torrent.Size != int64(len(content)) ||
		d.SourcePeer != src.role.Addr().String() {
		t.Fatalf("ended %+v", d)
	}
	if x := targetOf(t, d, "bare"); x.Status != TargetFailed || x.Error == nil || x.Error.Kind != KindNoClient {
		t.Fatalf("bare %+v", x)
	}
	for _, n := range []*btNode{a, b} {
		got, err := os.ReadFile(filepath.Join(n.dl, "models", "model.bin"))
		if err != nil || !bytes.Equal(got, content) {
			t.Fatalf("the file on a target: %v, %d bytes", err, len(got))
		}
		if l := n.role.List(); len(l) != 0 {
			t.Fatalf("the torrent stays on a target: %+v", l)
		}
	}
	for _, name := range []string{"a", "b"} {
		if x := targetOf(t, d, name); x.Status != TargetDone || !x.Removed || x.Progress != 1 || x.Peer == "" || x.Adopted {
			t.Fatalf("%s %+v", name, x)
		}
	}
	var entry AuditEntry
	waitFor(t, "the audit entry", func() bool {
		for _, e := range f.m.Audit() {
			if e.Op == d.ID {
				entry = e
				return true
			}
		}
		return false
	})
	if entry.Action != ActionDistribute || entry.Node != "src" || len(entry.Outcomes) != 3 || entry.Outcome != AuditPartial {
		t.Fatalf("audit %+v", entry)
	}

	// Again with keep_seeding, through the module: a and b have it (their
	// files are where the torrent puts them), check it and keep seeding.
	s := f.serve()
	code, body, _ := s.do("POST", "/api/m/cluster/distributions", js(map[string]any{"source": "src", "publish": "rel",
		"torrent": "model.bin", "members": []string{"a", "b"}, "category": "models", "keep_seeding": true}))
	if code != 202 {
		t.Fatalf("POST distributions: %d %s", code, body)
	}
	var started Dist
	_ = json.Unmarshal([]byte(body), &started)
	d = f.waitDist(started.ID)
	if d.State != DistDone || d.Counts.Done != 2 {
		t.Fatalf("keep_seeding: %+v", d)
	}
	for _, n := range []*btNode{a, b} {
		l := n.role.List()
		if len(l) != 1 || l[0].State != "seeding" {
			t.Fatalf("not seeding: %+v", l)
		}
	}
	code, body, _ = s.do("GET", "/api/m/cluster/distributions", "")
	if code != 200 || !strings.Contains(body, started.ID) || !strings.Contains(body, `"max_running":2`) {
		t.Fatalf("GET distributions: %d %s", code, body)
	}
	if code, body, _ = s.do("GET", "/api/m/cluster/distributions/"+started.ID, ""); code != 200 || !strings.Contains(body, `"state":"done"`) {
		t.Fatalf("GET distribution: %d %s", code, body)
	}
	if code, _, _ = s.do("GET", "/api/m/cluster/distributions/nope", ""); code != 404 {
		t.Fatalf("an unknown distribution: %d", code)
	}
	if code, _, _ = s.do("POST", "/api/m/cluster/distributions/"+started.ID+"/cancel", ""); code != 409 {
		t.Fatalf("cancelling one ended: %d", code)
	}

	// A third time: the targets have the torrent already, so it is
	// adopted, followed and never removed.
	d, err = f.m.StartDist("tester", &DistRequest{Source: "src", Publish: "rel", Members: []string{"a"}, Category: "models"})
	if err != nil {
		t.Fatal(err)
	}
	d = f.waitDist(d.ID)
	if x := targetOf(t, d, "a"); d.State != DistDone || !x.Adopted || x.Removed || len(a.role.List()) != 1 {
		t.Fatalf("adopted: %+v", d)
	}

	// The view sees the members' clients.
	defer func(v time.Duration) { btViewTTL = v }(btViewTTL)
	btViewTTL = 0
	ch := make(chan BTView, 1)
	f.m.BitTorrent(func(v BTView) { ch <- v })
	v := <-ch
	if v.Totals.Clients != 3 || v.Totals.Running != 3 || v.Totals.Published != 1 || v.Totals.Torrents != 2 || v.Distributions[DistDone] != 2 {
		t.Fatalf("view %+v", v.Totals)
	}
}

// TestDistributeCancelStall: a target that cannot reach the source makes
// no progress; one distribution is cancelled, and the torrent and what was
// written of it leave the target; another fails its target as stalled.
func TestDistributeCancelStall(t *testing.T) {
	src, _ := startSource(t, true, 1<<20)
	a, b := startBTNode(t, ""), startBTNode(t, "")
	f := distFixture(t)
	f.add(tokenInput("src", src.url))
	f.add(tokenInput("a", a.url))
	f.add(tokenInput("b", b.url))
	for _, n := range []string{"src", "a", "b"} {
		f.waitUp(n)
	}
	// The source is given to the targets at a port where nothing listens.
	nowhere := "127.0.0.1:" + strconv.Itoa(pebbletest.FreePort(t))
	d, err := f.m.StartDist("tester", &DistRequest{Source: "src", Publish: "rel", Members: []string{"a"}, PeerHosts: map[string]string{"src": nowhere}})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a to download", func() bool {
		x, _ := f.m.DistByID(d.ID)
		return x.Counts.Active == 1 && x.Targets[0].Status == TargetDownloading && len(a.role.List()) == 1
	})
	if _, err := f.m.CancelDist("tester", "nope"); !errors.As(err, new(errNoDist)) {
		t.Fatal(err)
	}
	if _, err := f.m.CancelDist("tester", d.ID); err != nil {
		t.Fatal(err)
	}
	d = f.waitDist(d.ID)
	if x := targetOf(t, d, "a"); d.State != DistCancelled || x.Status != TargetCancelled || !x.Removed || !strings.Contains(d.Reason, "tester") {
		t.Fatalf("cancelled %+v", d)
	}
	if l := a.role.List(); len(l) != 0 {
		t.Fatalf("the torrent stays: %+v", l)
	}

	d, err = f.m.StartDist("tester", &DistRequest{Source: "src", Publish: "rel", Members: []string{"b"}, StallTimeoutMs: 1000,
		PeerHosts: map[string]string{"src": "127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	// "127.0.0.1" alone takes the source client's port: b reaches it, and
	// is done; then a stall, the source's port being one where nothing
	// listens.
	if d = f.waitDist(d.ID); d.State != DistDone {
		t.Fatalf("by host: %+v", d)
	}
	d, err = f.m.StartDist("tester", &DistRequest{Source: "src", Publish: "rel", Members: []string{"a"}, StallTimeoutMs: 1000,
		PeerHosts: map[string]string{"src": nowhere}})
	if err != nil {
		t.Fatal(err)
	}
	d = f.waitDist(d.ID)
	if x := targetOf(t, d, "a"); d.State != DistFailed || x.Status != TargetFailed || x.Error == nil || x.Error.Kind != KindStalled || !x.Removed {
		t.Fatalf("stalled %+v", d)
	}
}

// TestDistributeSourceRefused: a source whose entry is not private, one
// with no such entry or torrent, and one without the client fail the
// distribution before any target adds anything.
func TestDistributeSourceRefused(t *testing.T) {
	public, _ := startSource(t, false, 64<<10)
	a := startBTNode(t, "")
	bare := newFakeMember(t, false)
	bare.set(func(f *fakeMember) {
		f.caps, f.bt = btCaps, `{"client":{"running":false,"torrents":0,"states":{},"publish":[]},"tracker":null}`
	})
	f := distFixture(t)
	f.add(tokenInput("public", public.url))
	f.add(tokenInput("a", a.url))
	f.add(tokenInput("bare", bare.srv.URL))
	for _, n := range []string{"public", "a", "bare"} {
		f.waitUp(n)
	}
	for _, c := range []struct {
		req  DistRequest
		kind string
		msg  string
	}{
		{DistRequest{Source: "public", Publish: "rel"}, KindNotPrivate, "not private"},
		{DistRequest{Source: "public", Publish: "nope"}, KindRefused, "no publish entry"},
		{DistRequest{Source: "public", Publish: "rel", Torrent: "other.bin"}, KindRefused, "no torrent"},
		{DistRequest{Source: "bare", Publish: "rel"}, KindNoClient, "runs no bittorrent.client"},
	} {
		c.req.Members = []string{"a"}
		d, err := f.m.StartDist("tester", &c.req)
		if err != nil {
			t.Fatal(err)
		}
		d = f.waitDist(d.ID)
		x := targetOf(t, d, "a")
		if d.State != DistFailed || d.Error == nil || d.Error.Kind != c.kind || !strings.Contains(d.Reason, c.msg) || x.Status != TargetFailed || x.Removed {
			t.Fatalf("%+v: %+v", c.req, d)
		}
	}
	if l := a.role.List(); len(l) != 0 {
		t.Fatalf("a target added: %+v", l)
	}
}

// TestDistributeBounds: what StartDist refuses before it starts, and the
// bounds of targets, distributions at once and a member's one at a time.
func TestDistributeBounds(t *testing.T) {
	f := distFixture(t)
	slow := newFakeMember(t, false)
	slow.set(func(f *fakeMember) { f.caps, f.delay = btCaps, 300*time.Millisecond })
	f.add(tokenInput("src", slow.srv.URL))
	var many []string
	for i := range MaxDistTargets + 1 {
		name := "m" + strconv.Itoa(i)
		f.add(tokenInput(name, downURL(t)))
		many = append(many, name)
	}
	for _, c := range []struct {
		req   DistRequest
		field string
	}{
		{DistRequest{Publish: "rel", Members: []string{"m1"}}, "source"},
		{DistRequest{Source: "src", Members: []string{"m1"}}, "publish"},
		{DistRequest{Source: "src", Publish: "a/b", Members: []string{"m1"}}, "publish"},
		{DistRequest{Source: "src", Publish: "rel", Members: []string{"src"}}, "members"},
		{DistRequest{Source: "src", Publish: "rel"}, "members"},
		{DistRequest{Source: "src", Publish: "rel", Members: many}, "members"},
		{DistRequest{Source: "src", Publish: "rel", Members: []string{"m1"}, StallTimeoutMs: 10}, "stall_timeout_ms"},
		{DistRequest{Source: "src", Publish: "rel", Members: []string{"m1"}, PeerHosts: map[string]string{"m2": "h"}}, "peer_hosts"},
		{DistRequest{Source: "src", Publish: "rel", Members: []string{"m1"}, PeerHosts: map[string]string{"m1": "a b"}}, "peer_hosts"},
		{DistRequest{Source: "src", Publish: "rel", Members: []string{"m1"}, PeerHosts: map[string]string{"m1": "h:99999"}}, "peer_hosts"},
		{DistRequest{Source: "src", Publish: "rel", Members: []string{"m1"}, Description: strings.Repeat("x", 300)}, "description"},
	} {
		_, err := f.m.StartDist("tester", &c.req)
		var fe *FieldError
		if !errors.As(err, &fe) || fe.Field != c.field {
			t.Fatalf("%+v: %v", c.req, err)
		}
	}
	if _, err := f.m.StartDist("tester", &DistRequest{Source: "none", Publish: "rel", Members: []string{"m1"}}); !errors.As(err, new(errNotFound)) {
		t.Fatal(err)
	}
	if _, err := f.m.StartDist("tester", &DistRequest{Source: "src", Publish: "rel", Members: []string{"none"}}); !errors.As(err, new(errNotFound)) {
		t.Fatal(err)
	}
	// The source answers slowly: the distributions stay preparing while
	// the bounds are tried.
	d1, err := f.m.StartDist("tester", &DistRequest{Source: "src", Publish: "rel", Members: []string{"m1", "m2"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.StartDist("tester", &DistRequest{Source: "src", Publish: "rel", Members: []string{"m2"}}); !errors.As(err, new(errConflict)) {
		t.Fatalf("a busy target: %v", err)
	}
	if _, err := f.m.StartDist("tester", &DistRequest{Source: "src", Publish: "rel", Members: []string{"m3"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.StartDist("tester", &DistRequest{Source: "src", Publish: "rel", Members: []string{"m4"}}); !errors.As(err, new(errBusy)) {
		t.Fatalf("past %d at once: %v", MaxRunningDists, err)
	}
	s := f.serve()
	if code, body, _ := s.do("POST", "/api/m/cluster/distributions", js(map[string]any{"source": "src", "publish": "rel", "members": []string{"m4"}})); code != 429 {
		t.Fatalf("POST past the bound: %d %s", code, body)
	}
	if code, body, _ := s.do("POST", "/api/m/cluster/distributions", js(map[string]any{"source": "src", "members": []string{"m4"}})); code != 400 {
		t.Fatalf("POST without publish: %d %s", code, body)
	}
	if code, body, _ := s.do("POST", "/api/m/cluster/distributions/"+d1.ID+"/cancel", ""); code != 200 {
		t.Fatalf("cancel: %d %s", code, body)
	}
	if code, _, _ := s.do("POST", "/api/m/cluster/distributions/"+d1.ID+"/stop", ""); code != 404 {
		t.Fatalf("an unknown action: %d", code)
	}
	if d := f.waitDist(d1.ID); d.State != DistCancelled || d.Counts.Cancelled != 2 {
		t.Fatalf("cancelled while preparing: %+v", d)
	}
	// The members are free again.
	if _, err := f.m.StartDist("tester", &DistRequest{Source: "src", Publish: "rel", Members: []string{"m2"}}); err != nil {
		t.Fatal(err)
	}
}

// TestDistKept: past MaxKeptDists, the oldest ended go.
func TestDistKept(t *testing.T) {
	m := &Manager{}
	for i := range MaxKeptDists + 3 {
		m.dist.list = append(m.dist.list, &Dist{ID: strconv.Itoa(i), Finished: i != 1})
	}
	m.trimDistsLocked()
	if len(m.dist.list) != MaxKeptDists || m.dist.list[0].ID != "1" || m.dist.list[1].ID != "4" {
		t.Fatalf("%d kept, first %s %s", len(m.dist.list), m.dist.list[0].ID, m.dist.list[1].ID)
	}
}

// TestPeerAddr: how a member's client is reached.
func TestPeerAddr(t *testing.T) {
	f := newFixture(t, config.Cluster{})
	f.add(tokenInput("n", "http://127.0.0.1:2019"))
	for _, c := range []struct{ listen, host, want string }{
		{"10.0.0.5:6881", "", "10.0.0.5:6881"},
		{"0.0.0.0:6881", "", "127.0.0.1:6881"},
		{"[::]:6881", "", "127.0.0.1:6881"},
		{"0.0.0.0:6881", "bt.example", "bt.example:6881"},
		{"0.0.0.0:6881", "bt.example:7000", "bt.example:7000"},
		{"", "bt.example:7000", "bt.example:7000"},
		{"0.0.0.0:6881", "[2001:db8::1]", "[2001:db8::1]:6881"},
	} {
		got, err := f.m.peerAddr("n", c.listen, c.host)
		if err != nil || got != c.want {
			t.Errorf("%q %q: %q %v, want %q", c.listen, c.host, got, err, c.want)
		}
	}
	for _, c := range []struct{ listen, host string }{{"", ""}, {"", "bt.example"}, {"bad", ""}} {
		if _, err := f.m.peerAddr("n", c.listen, c.host); err == nil {
			t.Errorf("%q %q: no error", c.listen, c.host)
		}
	}
}
