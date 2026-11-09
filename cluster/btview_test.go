package cluster

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
)

// btCaps are a fake member's capabilities with the BitTorrent summary.
var btCaps = []string{"cache", "config.check", "config.reload", "config.versions", "console", "node", "status", "bittorrent", capBTSummary}

const (
	btClientA = `{"client":{"running":true,"listen":"127.0.0.1:6881","torrents":3,"states":{"downloading":2,"seeding":1},` +
		`"download_rate":100,"upload_rate":40,"downloaded":1000,"uploaded":400,"size":5000,"completed":2500,"peers":7,"errors":1,` +
		`"publish":[{"name":"rel","torrents":2,"pending":0}]},"tracker":null}`
	btClientB = `{"client":{"running":false,"torrents":1,"states":{"stopped":1},"publish":[]},` +
		`"tracker":{"torrents":4,"peers":9,"announces":12,"scrapes":3,"private":true,"allowed":0,"max_torrents":100,"max_peers":1000}}`
)

// TestBTViewFakes: the view reads each member's summary and sums them; a
// member without the roles, one older than the view (by its capabilities,
// or a 404), and one down are each said so; a second read within the TTL
// reads no member again; the view is served at /api/m/cluster/bittorrent.
func TestBTViewFakes(t *testing.T) {
	f := newFixture(t, config.Cluster{})
	a, b, none, old, old404 := newFakeMember(t, false), newFakeMember(t, false), newFakeMember(t, false), newFakeMember(t, false), newFakeMember(t, false)
	a.set(func(f *fakeMember) { f.caps, f.bt = btCaps, btClientA })
	b.set(func(f *fakeMember) { f.caps, f.bt = btCaps, btClientB })
	none.set(func(f *fakeMember) { f.caps, f.bt = btCaps, `{"client":null,"tracker":null}` })
	old404.set(func(f *fakeMember) { f.caps = btCaps })
	f.add(tokenInput("a", a.srv.URL))
	f.add(consoleInput("b", b.srv.URL))
	f.add(tokenInput("none", none.srv.URL))
	f.add(tokenInput("old", old.srv.URL))
	f.add(tokenInput("old404", old404.srv.URL))
	f.add(tokenInput("gone", downURL(t)))
	for _, n := range []string{"a", "b", "none", "old", "old404"} {
		f.waitUp(n)
	}
	f.waitState("gone", "down", func(s State) bool { return s.Status == StatusDown })
	defer func(d time.Duration) { btViewTTL = d }(btViewTTL)
	btViewTTL = time.Hour

	ch := make(chan BTView, 1)
	f.m.BitTorrent(func(v BTView) { ch <- v })
	v := <-ch
	by := map[string]BTMember{}
	for _, x := range v.Members {
		by[x.Name] = x
	}
	if len(v.Members) != 6 {
		t.Fatalf("%d members: %+v", len(v.Members), v.Members)
	}
	if s := by["a"].Summary; s == nil || s.Client == nil || s.Client.Torrents != 3 || s.Client.Listen != "127.0.0.1:6881" || by["a"].Status != StatusUp || by["a"].Read.IsZero() {
		t.Fatalf("a: %+v", by["a"])
	}
	if s := by["b"].Summary; s == nil || s.Client.Running || s.Tracker == nil || s.Tracker.Peers != 9 {
		t.Fatalf("b: %+v", by["b"])
	}
	if s := by["none"].Summary; s == nil || s.Client != nil || s.Tracker != nil {
		t.Fatalf("none: %+v", by["none"])
	}
	for _, n := range []string{"old", "old404"} {
		if e := by[n].Error; e == nil || e.Kind != KindUnsupported || by[n].Summary != nil {
			t.Fatalf("%s: %+v", n, by[n])
		}
	}
	if e := by["gone"].Error; e == nil || by["gone"].Status != StatusDown || !strings.Contains(e.Message, "down") {
		t.Fatalf("gone: %+v", by["gone"])
	}
	tt := v.Totals
	if tt.Members != 6 || tt.Read != 3 || tt.Clients != 2 || tt.Running != 1 || tt.Trackers != 1 || tt.Torrents != 4 ||
		tt.States["downloading"] != 2 || tt.States["stopped"] != 1 || tt.DownloadRate != 100 || tt.UploadRate != 40 ||
		tt.Downloaded != 1000 || tt.Peers != 7 || tt.Errors != 1 || tt.Published != 2 || tt.TrackerPeers != 9 || tt.Announces != 12 {
		t.Fatalf("totals %+v", tt)
	}
	old.mu.Lock()
	oldReads := old.btReads
	old.mu.Unlock()
	a.mu.Lock()
	reads := a.btReads
	a.mu.Unlock()
	if oldReads != 0 {
		t.Fatalf("a member without the capability was read %d times", oldReads)
	}

	// Within the TTL, through the module: no member is read again.
	s := f.serve()
	code, body, _ := s.do("GET", "/api/m/cluster/bittorrent", "")
	if code != 200 {
		t.Fatalf("GET bittorrent: %d %s", code, body)
	}
	var got BTView
	if err := json.Unmarshal([]byte(body), &got); err != nil || len(got.Members) != 6 || got.Totals.Torrents != 4 {
		t.Fatalf("%v %s", err, body)
	}
	a.mu.Lock()
	again := a.btReads
	a.mu.Unlock()
	if again != reads {
		t.Fatalf("a was read again within the TTL: %d, then %d", reads, again)
	}

	// Past it, read again; requests at once share one read.
	btViewTTL = 0
	done := make(chan BTView, 3)
	for range 3 {
		f.m.BitTorrent(func(v BTView) { done <- v })
	}
	for range 3 {
		<-done
	}
	a.mu.Lock()
	after := a.btReads
	a.mu.Unlock()
	if after-again < 1 || after-again > 3 {
		t.Fatalf("reads after the TTL: %d", after-again)
	}
}

// TestBTViewClip: a member's summary is cut to the manager's bounds.
func TestBTViewClip(t *testing.T) {
	s := &BTSummary{Client: &BTClient{Listen: strings.Repeat("x", 200), States: map[string]int{}}, Tracker: &BTTracker{Refused: map[string]uint64{}}}
	for i := range maxBTStates + 1 {
		s.Client.States[string(rune('a'+i))] = 1
	}
	for i := range maxBTRefused + 1 {
		s.Tracker.Refused[string(rune('a'+i))] = 1
	}
	for range maxBTPublish + 3 {
		s.Client.Publish = append(s.Client.Publish, BTPublish{Name: strings.Repeat("n", 300)})
	}
	s.clipped()
	if len(s.Client.Listen) > 70 || len(s.Client.States) != 0 || s.Client.States == nil || len(s.Client.Publish) != maxBTPublish ||
		len(s.Client.Publish[0].Name) > MaxNameBytes*2+4 || s.Tracker.Refused != nil {
		t.Fatalf("%+v", s.Client)
	}
	var empty BTSummary
	empty.Client = &BTClient{}
	empty.clipped()
	if empty.Client.Publish == nil || empty.Client.States == nil {
		t.Fatal("nil lists")
	}
}

// TestBTViewAssistant: cluster_bittorrent reads the view of the members
// named, with the distributions; the read tools' member argument reaches
// a member's BitTorrent reads, never its passkeys, events or changes.
func TestBTViewAssistant(t *testing.T) {
	f := newFixture(t, config.Cluster{})
	a, b := newFakeMember(t, false), newFakeMember(t, false)
	a.set(func(f *fakeMember) { f.caps, f.bt = btCaps, btClientA })
	b.set(func(f *fakeMember) { f.caps, f.bt = btCaps, btClientB })
	f.add(tokenInput("a", a.srv.URL))
	f.add(tokenInput("b", b.srv.URL))
	f.waitUp("a")
	f.waitUp("b")
	ctx := t.Context()
	out, err := f.m.Assistant().BitTorrent(ctx, "tester", "", []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		View          BTView `json:"view"`
		Distributions []Dist `json:"distributions"`
	}
	if err := json.Unmarshal(out, &got); err != nil || len(got.View.Members) != 1 || got.View.Members[0].Name != "a" ||
		got.View.Totals.Torrents != 3 || got.Distributions == nil {
		t.Fatalf("%v %s", err, out)
	}
	if _, err := f.m.Assistant().BitTorrent(ctx, "tester", "", []string{"nope"}); err == nil {
		t.Fatal("an unknown member")
	}
	for _, p := range []string{"/bittorrent/summary", "/bittorrent/torrents?state=seeding", "/bittorrent/torrents/" + strings.Repeat("ab", 20) + "/peers",
		"/bittorrent/alt_speed", "/bittorrent/tracker/stats", "/bittorrent/tracker/torrents?limit=5", "/bittorrent/publish/rel"} {
		if _, _, _, _, err := f.m.Assistant().MemberCall(ctx, "tester", "a", "GET", p, nil); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	for _, p := range []string{"/bittorrent/tracker/passkey?user=x", "/bittorrent/events", "/bittorrent/torrents/x/files"} {
		if _, _, _, _, err := f.m.Assistant().MemberCall(ctx, "tester", "a", "GET", p, nil); err == nil {
			t.Errorf("%s was read", p)
		}
	}
	if _, _, _, _, err := f.m.Assistant().MemberCall(ctx, "tester", "a", "POST", "/bittorrent/torrents", nil); err == nil {
		t.Error("a POST went through")
	}
}

// TestBTAdminAPI: the view and the distributions in the manager's Admin
// API, acting as its user.
func TestBTAdminAPI(t *testing.T) {
	f := distFixture(t)
	a := newFakeMember(t, false)
	a.set(func(f *fakeMember) { f.caps, f.bt = btCaps, btClientA })
	f.add(tokenInput("a", a.srv.URL))
	f.add(tokenInput("b", downURL(t)))
	f.waitUp("a")
	api := f.adminAPI()
	const ct = "application/json"
	if code, body := api.do("GET", "/cluster", "", ""); code != 200 || !strings.Contains(body, `"distributions"`) {
		t.Fatalf("%d %s", code, body)
	}
	if code, body := api.do("GET", "/cluster/bittorrent", "", ""); code != 200 || !strings.Contains(body, `"torrents":3`) {
		t.Fatalf("%d %s", code, body)
	}
	code, body := api.do("POST", "/cluster/distributions", `{"source": "a", "publish": "rel", "members": ["b"]}`, ct)
	if code != 202 {
		t.Fatalf("%d %s", code, body)
	}
	var d Dist
	_ = json.Unmarshal([]byte(body), &d)
	if d.User != AdminAPIUser {
		t.Fatalf("%+v", d)
	}
	f.waitDist(d.ID)
	if code, body := api.do("GET", "/cluster/distributions/"+d.ID, "", ""); code != 200 || !strings.Contains(body, `"finished":true`) {
		t.Fatalf("%d %s", code, body)
	}
	if code, body := api.do("GET", "/cluster/distributions", "", ""); code != 200 || !strings.Contains(body, d.ID) {
		t.Fatalf("%d %s", code, body)
	}
	if code, _ := api.do("GET", "/cluster/distributions/nope", "", ""); code != 404 {
		t.Fatalf("%d", code)
	}
	if code, _ := api.do("POST", "/cluster/distributions/"+d.ID+"/cancel", "", ""); code != 409 {
		t.Fatalf("%d", code)
	}
	if code, _ := api.do("POST", "/cluster/distributions/nope/cancel", "", ""); code != 404 {
		t.Fatalf("%d", code)
	}
	if code, _ := api.do("POST", "/cluster/distributions", `{"publish": "rel"}`, ct); code != 400 {
		t.Fatalf("%d", code)
	}
}

// TestDistRoles: a distribution needs operator on its source and every
// target, to start and to cancel; any role reads the view and the list.
func TestDistRoles(t *testing.T) {
	f, us := rolesFixture(t, config.Cluster{})
	a2 := consoleInput("a2", downURL(t))
	a2.Labels = map[string]string{"env": "prod"}
	if _, err := f.m.Add("ada", a2); err != nil {
		t.Fatal(err)
	}
	body := js(map[string]any{"source": "a", "publish": "rel", "members": []string{"a2"}})
	if code, got, _ := us["pat"].do("POST", "/api/m/cluster/distributions", body); code != 403 {
		t.Fatalf("a viewer: %d %s", code, got)
	}
	if code, got, _ := us["opal"].do("POST", "/api/m/cluster/distributions", js(map[string]any{"source": "a", "publish": "rel", "members": []string{"b"}})); code != 403 {
		t.Fatalf("an operator of prod to b: %d %s", code, got)
	}
	code, got, _ := us["opal"].do("POST", "/api/m/cluster/distributions", body)
	if code != 202 {
		t.Fatalf("an operator of prod: %d %s", code, got)
	}
	var d Dist
	_ = json.Unmarshal([]byte(got), &d)
	if code, got, _ := us["vic"].do("POST", "/api/m/cluster/distributions/"+d.ID+"/cancel", ""); code != 403 && code != 409 {
		t.Fatalf("a viewer cancels: %d %s", code, got)
	}
	for _, p := range []string{"/api/m/cluster/distributions", "/api/m/cluster/distributions/" + d.ID} {
		if code, got, _ := us["vic"].do("GET", p, ""); code != 200 {
			t.Fatalf("%s: %d %s", p, code, got)
		}
	}
	f.waitDist(d.ID)
}
