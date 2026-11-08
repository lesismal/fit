package cluster

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
)

// insightMember is a fit as the series, the inventory and the tails see
// it: its Admin API's GET /node, /status, whose counters grow at each read
// as a busy member's do, and /logs/recent, from a ring of its own.
type insightMember struct {
	srv *httptest.Server

	mu       sync.Mutex
	started  time.Time
	reads    int    // GET /status read
	req      uint64 // requests answered
	r4, r5   uint64
	perRead  [3]uint64 // what each read adds: requests, 4xx, 5xx
	open     int64
	pending  int64
	reloads  uint64
	certs    []map[string]any
	recs     []logs.Record
	seq      uint64
	caps     []string
	noCounts bool
	fail     int // GET /logs/recent answers this status, when not 0
}

func newInsightMember(t *testing.T) *insightMember {
	t.Helper()
	im := &insightMember{started: time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC), perRead: [3]uint64{10, 2, 1}, open: 3, pending: 100}
	im.srv = httptest.NewServer(http.HandlerFunc(im.serve))
	t.Cleanup(im.srv.Close)
	return im
}

func (im *insightMember) set(fn func(im *insightMember)) {
	im.mu.Lock()
	defer im.mu.Unlock()
	fn(im)
}

// log adds a message to the member's ring at at.
func (im *insightMember) log(at time.Time, level, msg string) {
	im.set(func(im *insightMember) {
		im.seq++
		im.recs = append(im.recs, logs.Record{Seq: im.seq, Time: at, Level: level, Logger: "server", Message: msg})
	})
}

func (im *insightMember) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+fakeToken {
		w.WriteHeader(401)
		return
	}
	im.mu.Lock()
	defer im.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	caps := im.caps
	if caps == nil {
		caps = []string{"node", "status", "status.requests", "logs.recent"}
	}
	switch r.URL.Path {
	case "/node":
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "x", "fit_version": "v1", "started": im.started, "config_version": 1,
			"config_etag": `"e"`, "roles": []string{}, "capabilities": caps})
	case "/status":
		im.reads++
		im.req += im.perRead[0]
		im.r4 += im.perRead[1]
		im.r5 += im.perRead[2]
		im.reloads++
		body := map[string]any{"reloads": map[string]any{"succeeded": im.reloads, "failed": 0},
			"connections": map[string]any{"open": im.open, "refused": 0, "pending_bytes": im.pending}, "upstreams": []any{},
			"certificates": im.certs}
		if !im.noCounts {
			body["requests"] = map[string]any{"total": im.req, "classes": map[string]uint64{"2xx": im.req - im.r4 - im.r5, "4xx": im.r4, "5xx": im.r5}}
		}
		_ = json.NewEncoder(w).Encode(body)
	case "/logs/recent":
		if im.fail != 0 {
			w.WriteHeader(im.fail)
			_, _ = w.Write([]byte(`{"error":"no logs here"}`))
			return
		}
		after, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		level := r.URL.Query().Get("level")
		if after > im.seq {
			after = 0
		}
		out := []logs.Record{}
		for _, rec := range im.recs {
			if rec.Seq > after && (level == "" || rec.Level == level) {
				out = append(out, rec)
			}
		}
		if limit > 0 && len(out) > limit {
			out = out[len(out)-limit:]
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"records": out, "last": im.seq, "ring_size": 1000})
	default:
		w.WriteHeader(404)
	}
}

// TestPoints checks a series made of polls: rates from the poll before,
// none across a restart, a gap or counters gone back, gauges as read.
func TestPoints(t *testing.T) {
	iv := 10 * time.Second
	s := []sample{
		{at: 0, started: 1, hasReq: true, req: 100, r4: 10, r5: 1, open: 5, pending: 7, ok: 2},
		{at: 10_000, started: 1, hasReq: true, req: 150, r4: 20, r5: 6, open: 6, pending: 8, ok: 3, failed: 1},
		{at: 20_000, started: 2, hasReq: true, req: 5, open: 1},              // restarted
		{at: 30_000, started: 2, hasReq: true, req: 25, open: 1},             // 2/s
		{at: 100_000, started: 2, hasReq: true, req: 95, open: 1},            // a gap
		{at: 110_000, started: 2, hasReq: false, open: 2},                    // no counts
		{at: 120_000, started: 2, hasReq: true, req: 90, open: 2, ok: 0},     // after none
		{at: 130_000, started: 2, hasReq: true, req: 80, open: 2, failed: 0}, // gone back
	}
	p := points(s, 100, iv)
	if len(p.T) != len(s) || p.Requests[0].OK {
		t.Fatalf("%+v", p)
	}
	if v := p.Requests[1]; !v.OK || v.V != 5 || p.Errors4xx[1].V != 1 || p.Errors5xx[1].V != 0.5 || p.Reloads[1].V != 1 || p.ReloadsFailed[1].V != 1 {
		t.Errorf("the second point: %+v %+v %+v", p.Requests[1], p.Errors5xx[1], p.ReloadsFailed[1])
	}
	if p.Connections[1].V != 6 || p.PendingBytes[1].V != 8 {
		t.Errorf("gauges: %+v %+v", p.Connections[1], p.PendingBytes[1])
	}
	for i, ok := range []bool{false, true, false, true, false, false, false, false} {
		if p.Requests[i].OK != ok {
			t.Errorf("point %d has a rate: %v, want %v", i, p.Requests[i].OK, ok)
		}
	}
	if p.Requests[3].V != 2 {
		t.Errorf("after the restart: %+v", p.Requests[3])
	}
	if last := points(s, 2, iv); len(last.T) != 2 || last.T[0] != 120_000 {
		t.Errorf("the last two: %v", last.T)
	}
	data, _ := json.Marshal(points(s[:2], 2, iv))
	if !strings.Contains(string(data), `"requests":[null,5]`) {
		t.Errorf("%s", data)
	}

	// A group sums its members a bucket at a time.
	a := points([]sample{{at: 1_000, open: 1}, {at: 11_000, open: 2, hasReq: true}, {at: 12_000, open: 3}}, 10, iv)
	b := points([]sample{{at: 2_000, open: 10}, {at: 21_000, open: 20}}, 10, iv)
	g := sumGroup([]Series{a, b}, iv)
	if !slices.Equal(g.T, []int64{0, 10_000, 20_000}) || !slices.Equal(g.Members, []int{2, 1, 1}) {
		t.Fatalf("%+v", g)
	}
	if g.Connections[0].V != 11 || g.Connections[1].V != 3 || g.Connections[2].V != 20 || g.Requests[0].OK {
		t.Errorf("%+v %+v", g.Connections, g.Requests)
	}
	if e := sumGroup(nil, iv); len(e.T) != 0 || e.Members == nil {
		t.Errorf("an empty group: %+v", e)
	}
}

// TestMetricsMatchMembers: the manager's series of each member are the
// member's own counters between polls, its counters as the member gives
// them; a group sums its members; the API answers them, and refuses names
// it does not know.
func TestMetricsMatchMembers(t *testing.T) {
	f := newFixture(t, config.Cluster{})
	ms := map[string]*insightMember{"a": newInsightMember(t), "b": newInsightMember(t), "c": newInsightMember(t)}
	ms["b"].set(func(im *insightMember) { im.perRead, im.open = [3]uint64{4, 0, 0}, 7 })
	ms["c"].set(func(im *insightMember) { im.noCounts = true })
	for _, n := range []string{"a", "b", "c"} {
		in := tokenInput(n, ms[n].srv.URL)
		if n != "c" {
			in.Labels = map[string]string{"tier": "edge"}
		}
		f.add(in)
	}
	if _, err := f.m.AddGroup("tester", &GroupInput{Name: "edge", Selector: "tier=edge"}); err != nil {
		t.Fatal(err)
	}
	var body MetricsBody
	waitFor(t, "three polls of each member", func() bool {
		var err error
		if body, err = f.m.Metrics(MetricsQuery{Points: SeriesPoints}); err != nil {
			t.Fatal(err)
		}
		for _, n := range body.Nodes {
			if len(n.Series.T) < 3 {
				return false
			}
		}
		return len(body.Nodes) == 3
	})
	for _, n := range body.Nodes {
		s := n.Series
		im := ms[n.Name]
		im.mu.Lock()
		per, open := im.perRead, im.open
		im.mu.Unlock()
		for i := 1; i < len(s.T); i++ {
			dt := float64(s.T[i]-s.T[i-1]) / 1000
			if n.Name == "c" {
				if s.Requests[i].OK {
					t.Errorf("c counts no requests, yet has a rate: %+v", s.Requests[i])
				}
				continue
			}
			if !s.Requests[i].OK && s.T[i]-s.T[i-1] > gapPolls*body.IntervalMs {
				continue // polls too far apart on a loaded machine: no rate, rightly
			}
			// Each poll read the member's counters once: they grew by
			// per between two points, the rate times the time between.
			if !s.Requests[i].OK || math.Abs(s.Requests[i].V*dt-float64(per[0])) > 0.01*float64(per[0])+0.01 {
				t.Errorf("%s point %d: %v a second over %.3fs, the member counted %d", n.Name, i, s.Requests[i], dt, per[0])
			}
			if math.Abs(s.Errors5xx[i].V*dt-float64(per[2])) > 0.02 {
				t.Errorf("%s point %d: 5xx %v", n.Name, i, s.Errors5xx[i])
			}
			if s.Reloads[i].V != 1 {
				t.Errorf("%s point %d: reloads %v", n.Name, i, s.Reloads[i])
			}
		}
		if s.Connections[len(s.T)-1].V != float64(open) {
			t.Errorf("%s: connections %v, the member has %d", n.Name, s.Connections, open)
		}
		if n.Counters == nil || n.Counters.Connections.PendingBytes != 100 {
			t.Fatalf("%s: counters %+v", n.Name, n.Counters)
		}
		if n.Name != "c" {
			im.mu.Lock()
			got, reqs := n.Counters.Requests, im.req
			im.mu.Unlock()
			if got == nil || got.Total > reqs || got.Total == 0 || got.Classes["5xx"]*per[0] != got.Total*per[2] {
				t.Errorf("%s: requests %+v, the member counted %d", n.Name, got, reqs)
			}
		}
	}
	if len(body.Groups) != 1 || body.Groups[0].Name != "edge" || !slices.Equal(body.Groups[0].Nodes, []string{"a", "b"}) {
		t.Fatalf("groups %+v", body.Groups)
	}
	g := body.Groups[0].Series
	last := len(g.T) - 1
	if g.Members[last] == 2 && g.Connections[last].V != 10 {
		t.Errorf("edge's connections %v", g.Connections)
	}

	// One member, the group alone, refusals.
	if one, err := f.m.Metrics(MetricsQuery{Nodes: []string{"b"}, Points: 2}); err != nil || len(one.Nodes) != 1 || len(one.Nodes[0].Series.T) != 2 || len(one.Groups) != 0 {
		t.Errorf("b: %+v %v", one, err)
	}
	if gr, err := f.m.Metrics(MetricsQuery{Groups: []string{"edge"}}); err != nil || len(gr.Nodes) != 0 || len(gr.Groups) != 1 {
		t.Errorf("edge: %+v %v", gr, err)
	}
	if _, err := f.m.Metrics(MetricsQuery{Nodes: []string{"zz"}}); err == nil {
		t.Error("an unknown member")
	}
	if _, err := f.m.Metrics(MetricsQuery{Groups: []string{"zz"}}); err == nil {
		t.Error("an unknown group")
	}

	s := f.serve()
	code, raw, _ := s.do("GET", "/api/m/cluster/metrics?node=a,b&group=edge&points=5", "")
	var api struct {
		IntervalMs int64 `json:"interval_ms"`
		Nodes      []struct {
			Name   string
			Series struct {
				T        []int64
				Requests []*float64
			}
		}
		Groups []struct{ Name string }
	}
	if err := json.Unmarshal([]byte(raw), &api); code != 200 || err != nil || len(api.Nodes) != 2 || len(api.Groups) != 1 || len(api.Nodes[0].Series.T) > 5 {
		t.Fatalf("%d %s", code, raw)
	}
	for _, q := range []string{"points=0", "points=x", "node=zz", "group=zz"} {
		if code, raw, _ := s.do("GET", "/api/m/cluster/metrics?"+q, ""); code != 400 && code != 404 {
			t.Errorf("%s: %d %s", q, code, raw)
		}
	}
}

// TestCertificateInventory: every member's certificates, the one about to
// expire first, with its names, issuer and days left; members that serve
// none are listed apart.
func TestCertificateInventory(t *testing.T) {
	f := newFixture(t, config.Cluster{})
	now := time.Now().UTC()
	cert := func(listener, name string, in time.Duration) map[string]any {
		return map[string]any{"listener": listener, "names": []string{name}, "issuer": "Test CA " + name, "serial": "0a",
			"not_before": now.Add(-time.Hour), "not_after": now.Add(in)}
	}
	ms := map[string]*insightMember{"a": newInsightMember(t), "b": newInsightMember(t), "c": newInsightMember(t)}
	ms["a"].set(func(im *insightMember) { im.certs = []map[string]any{cert("web", "a.example", 90*24*time.Hour)} })
	ms["b"].set(func(im *insightMember) {
		im.certs = []map[string]any{cert("web", "b.example", 60*24*time.Hour), cert("api", "soon.example", 3*24*time.Hour+time.Hour)}
	})
	for _, n := range []string{"a", "b", "c"} {
		f.add(tokenInput(n, ms[n].srv.URL))
	}
	var inv CertsBody
	waitFor(t, "the certificates read", func() bool {
		inv = f.m.Certificates()
		// c has no certificates: its read is waited for too, not only
		// a's and b's.
		if len(inv.Certificates) != 3 || len(inv.Nodes) != 3 {
			return false
		}
		for _, n := range inv.Nodes {
			if n.Read.IsZero() {
				return false
			}
		}
		return true
	})
	first := inv.Certificates[0]
	if first.Node != "b" || first.Names[0] != "soon.example" || first.Issuer != "Test CA soon.example" || first.DaysLeft != 3 || first.Listener != "api" || first.Status != StatusUp {
		t.Errorf("the first: %+v", first)
	}
	if inv.Certificates[1].Names[0] != "b.example" || inv.Certificates[2].Node != "a" {
		t.Errorf("the order: %+v", inv.Certificates)
	}
	if len(inv.Nodes) != 3 || inv.Nodes[2].Name != "c" || inv.Nodes[2].Count != 0 || inv.Nodes[2].Read.IsZero() || inv.Nodes[1].Count != 2 {
		t.Errorf("the members: %+v", inv.Nodes)
	}
	s := f.serve()
	if code, raw, _ := s.do("GET", "/api/m/cluster/certificates", ""); code != 200 || strings.Index(raw, "soon.example") > strings.Index(raw, "a.example") {
		t.Errorf("%d %s", code, raw)
	}
}

// TestTailLogs follows three members' logs merged by time: the latest
// first, then what came since the cursor, a member restarted from its
// start; a member down or older is reported on its own, the others read.
func TestTailLogs(t *testing.T) {
	f := newFixture(t, config.Cluster{})
	names := []string{"a", "b", "c"}
	ms := map[string]*insightMember{}
	t0 := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	for i, n := range names {
		ms[n] = newInsightMember(t)
		for j := range 3 {
			ms[n].log(t0.Add(time.Duration(3*j+i)*time.Second), "info", fmt.Sprintf("%s-%d", n, j))
		}
		f.add(tokenInput(n, ms[n].srv.URL))
	}
	for _, n := range names {
		f.waitUp(n)
	}
	tail := func(q TailQuery) TailBody {
		t.Helper()
		ch := make(chan struct{})
		var body TailBody
		var err error
		f.m.TailLogs(q, func(b TailBody, e error) { body, err = b, e; close(ch) })
		<-ch
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	msgs := func(b TailBody) string {
		var out []string
		for _, r := range b.Records {
			out = append(out, r.Message)
		}
		return strings.Join(out, " ")
	}
	b := tail(TailQuery{Nodes: names})
	if got := msgs(b); got != "a-0 b-0 c-0 a-1 b-1 c-1 a-2 b-2 c-2" {
		t.Fatalf("merged: %s", got)
	}
	if b.Records[0].Node != "a" || b.Records[1].Node != "b" || len(b.Nodes) != 3 || b.Nodes[2].Last != 3 {
		t.Errorf("%+v", b)
	}
	// Followed: only what came since.
	ms["b"].log(t0.Add(time.Minute), "warn", "b-new")
	ms["a"].log(t0.Add(2*time.Minute), "info", "a-new")
	b = tail(TailQuery{Nodes: names, Cursor: b.Cursor})
	if got := msgs(b); got != "b-new a-new" {
		t.Fatalf("followed: %s", got)
	}
	if b = tail(TailQuery{Nodes: names, Cursor: b.Cursor}); len(b.Records) != 0 {
		t.Errorf("nothing new: %s", msgs(b))
	}
	cursor := b.Cursor
	// c restarted: its ring starts over, and it is read from its start.
	ms["c"].set(func(im *insightMember) { im.recs, im.seq = nil, 0 })
	ms["c"].log(t0.Add(3*time.Minute), "error", "c-again")
	b = tail(TailQuery{Nodes: names, Cursor: cursor, Level: "error"})
	if got := msgs(b); got != "c-again" {
		t.Errorf("restarted: %s", got)
	}
	// b fails, c is an older fit, a is down: each said, nothing lost.
	ms["b"].set(func(im *insightMember) { im.fail = 500 })
	ms["c"].set(func(im *insightMember) { im.caps = []string{"node", "status"} })
	waitFor(t, "c's capabilities polled", func() bool {
		caps := f.m.polledCaps("c")
		return caps != nil && !slices.Contains(caps, capLogsRecent)
	})
	ms["a"].srv.Close()
	b = tail(TailQuery{Nodes: names, Cursor: b.Cursor, Limit: 5})
	if len(b.Records) != 0 || b.Nodes[0].Error == nil || b.Nodes[1].Error == nil || b.Nodes[1].Error.Kind != KindStatus ||
		b.Nodes[2].Error == nil || b.Nodes[2].Error.Kind != KindUnsupported {
		t.Fatalf("%+v", b.Nodes)
	}
	var next tailCursor
	if next, _ = decodeCursor(b.Cursor); next["b"] != 4 {
		t.Errorf("a failed read keeps the cursor: %v", next)
	}

	// Refused queries.
	for _, q := range []TailQuery{{}, {Nodes: []string{"zz"}}, {Nodes: names, Level: "loud"}, {Nodes: names, Limit: MaxTailLimit + 1},
		{Nodes: names, Cursor: "!!"}, {Nodes: names, Contains: strings.Repeat("x", 300)}, {Nodes: slices.Repeat([]string{"a"}, MaxTailNodes+1)}} {
		ch := make(chan error, 1)
		f.m.TailLogs(q, func(_ TailBody, err error) { ch <- err })
		if <-ch == nil {
			t.Errorf("%+v: no error", q)
		}
	}
}

// TestLogsAPI: GET /api/logs tails the members named, which the user needs
// a role on, and refuses what TailLogs refuses.
func TestLogsAPI(t *testing.T) {
	f, us := rolesFixture(t, config.Cluster{})
	im := newInsightMember(t)
	im.log(time.Now(), "info", "hello from c")
	in := tokenInput("c", im.srv.URL)
	in.Labels = map[string]string{"env": "prod"}
	if _, err := f.m.Add("ada", in); err != nil {
		t.Fatal(err)
	}
	f.waitUp("c")
	code, raw, _ := us["pat"].do("GET", "/api/m/cluster/logs?node=c", "")
	if code != 200 || !strings.Contains(raw, "hello from c") || !strings.Contains(raw, `"cursor"`) {
		t.Fatalf("%d %s", code, raw)
	}
	if code, raw, _ := us["pat"].do("GET", "/api/m/cluster/logs?node=c,b", ""); code != 403 || !strings.Contains(raw, `"b"`) {
		t.Errorf("pat on b: %d %s", code, raw)
	}
	if code, raw, _ := us["nemo"].do("GET", "/api/m/cluster/logs?node=c", ""); code != 403 {
		t.Errorf("nemo: %d %s", code, raw)
	}
	for _, q := range []string{"", "node=c&limit=0", "node=c&level=x", "node=zz"} {
		if code, raw, _ := us["ada"].do("GET", "/api/m/cluster/logs?"+q, ""); code != 400 && code != 404 {
			t.Errorf("%s: %d %s", q, code, raw)
		}
	}
	// The series and the inventory are any role's, as the states are.
	for _, p := range []string{"/api/m/cluster/metrics", "/api/m/cluster/certificates"} {
		if code, raw, _ := us["pat"].do("GET", p, ""); code != 200 {
			t.Errorf("%s: %d %s", p, code, raw)
		}
		if code, _, _ := us["nemo"].do("GET", p, ""); code != 403 {
			t.Errorf("nemo %s: %d", p, code)
		}
	}
}
