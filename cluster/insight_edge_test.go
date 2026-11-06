package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
)

// TestInsightBounds: a member's samples are a ring of SeriesPoints, read
// oldest first; its certificates and request classes are bounded; a poll
// without /status keeps nothing.
func TestInsightBounds(t *testing.T) {
	n := &node{}
	t0 := time.Unix(1000, 0)
	n.observe(t0, &poller{})
	if len(n.ins.samples) != 0 {
		t.Fatal("a poll without /status was kept")
	}
	st := &statusAnswer{Requests: &requestCounts{Total: 1, Classes: map[string]uint64{}}}
	for i := range 10 {
		st.Requests.Classes[string(rune('a'+i))] = 1
	}
	for range MaxMemberCerts + 3 {
		st.Certificates = append(st.Certificates, struct {
			Listener  string    `json:"listener"`
			Names     []string  `json:"names"`
			Issuer    string    `json:"issuer"`
			Serial    string    `json:"serial"`
			NotBefore time.Time `json:"not_before"`
			NotAfter  time.Time `json:"not_after"`
		}{Listener: "web"})
	}
	for i := range SeriesPoints + 5 {
		n.observe(t0.Add(time.Duration(i)*time.Second), &poller{status: st})
	}
	got := n.ins.ordered()
	if len(got) != SeriesPoints || got[0].at != t0.Add(5*time.Second).UnixMilli() || got[len(got)-1].at != t0.Add((SeriesPoints+4)*time.Second).UnixMilli() {
		t.Fatalf("%d samples, from %d to %d", len(got), got[0].at, got[len(got)-1].at)
	}
	if len(n.ins.certs) != MaxMemberCerts || len(n.ins.last.Requests.Classes) != 8 || n.ins.certs[0].Names == nil {
		t.Errorf("%d certificates, %d classes", len(n.ins.certs), len(n.ins.last.Requests.Classes))
	}

	var v []Value
	if err := json.Unmarshal([]byte(`[null, 1.5]`), &v); err != nil || v[0].OK || !v[1].OK || v[1].V != 1.5 {
		t.Errorf("%+v %v", v, err)
	}
	if err := json.Unmarshal([]byte(`["x"]`), &v); err == nil {
		t.Error("a string is a Value")
	}
}

// errBody is a request body that cannot be read.
type errBody struct{}

func (errBody) Read([]byte) (int, error) { return 0, errors.New("cut") }

// TestAdminAPIEdges: the Admin API's refusals: a body cut, an answer that
// does not encode, errors by kind, the reads of members all taken, the
// syncs all under way and a client gone.
func TestAdminAPIEdges(t *testing.T) {
	f := newFixture(t, config.Cluster{})
	h := f.m.AdminAPI()
	req := httptest.NewRequest("POST", "/cluster/sync", errBody{})
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Errorf("a body cut: %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	apiJSON(rec, 200, func() {})
	if rec.Code != 500 {
		t.Errorf("an answer that does not encode: %d", rec.Code)
	}
	for err, want := range map[error]int{errReadOnly("ro"): 409, errBusy("busy"): 429, errors.New("other"): 500, errConflict("c"): 409} {
		rec = httptest.NewRecorder()
		apiError(rec, err)
		if rec.Code != want {
			t.Errorf("%v: %d, want %d", err, rec.Code, want)
		}
	}
	im := newInsightMember(t)
	f.add(tokenInput("a", im.srv.URL))
	for range MaxReads {
		f.m.reads <- struct{}{}
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/cluster/logs?node=a", nil))
	for range MaxReads {
		<-f.m.reads
	}
	if rec.Code != 429 {
		t.Errorf("the reads taken: %d %s", rec.Code, rec.Body)
	}
	sync := func(ctx func(*http.Request) *http.Request) int {
		req := httptest.NewRequest("POST", "/cluster/sync", strings.NewReader(`{"nodes":["a"]}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, ctx(req))
		return rec.Code
	}
	f.m.tpl.mu.Lock()
	f.m.tpl.syncs = MaxSyncs
	f.m.tpl.mu.Unlock()
	if code := sync(func(r *http.Request) *http.Request { return r }); code != 429 {
		t.Errorf("the syncs under way: %d", code)
	}
	f.m.tpl.mu.Lock()
	f.m.tpl.syncs = 0
	f.m.tpl.mu.Unlock()
	// A client gone before the member is read is answered what was read.
	im.set(func(im *insightMember) { im.caps = []string{"node"} })
	if code := sync(func(r *http.Request) *http.Request {
		ctx, cancel := contextWithCancel(r)
		cancel()
		return r.WithContext(ctx)
	}); code != 200 {
		t.Errorf("a client gone: %d", code)
	}
	if f.m.polledCaps("zz") != nil {
		t.Error("the capabilities of no member")
	}
}

// TestTailEdges: a member that answers what is not its log, too much of
// it, or an error not in JSON.
func TestTailEdges(t *testing.T) {
	f := newFixture(t, config.Cluster{})
	var answer string
	var status int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/node" {
			_, _ = w.Write([]byte(`{"id":"x","capabilities":["node","logs.recent"]}`))
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(srv.Close)
	f.add(tokenInput("a", srv.URL))
	f.waitUp("a")
	tail := func() TailBody {
		ch := make(chan TailBody, 1)
		f.m.TailLogs(TailQuery{Nodes: []string{"a"}}, func(b TailBody, _ error) { ch <- b })
		return <-ch
	}
	status, answer = 502, "<html>bad gateway</html>"
	if b := tail(); b.Nodes[0].Error == nil || !strings.Contains(b.Nodes[0].Error.Message, "<html>bad gateway") {
		t.Errorf("%+v", b.Nodes[0])
	}
	status, answer = 200, `{"records":`
	if b := tail(); b.Nodes[0].Error == nil || b.Nodes[0].Error.Kind != KindBadAnswer {
		t.Errorf("%+v", b.Nodes[0])
	}
	recs := make([]logs.Record, MaxTailLimit+5)
	for i := range recs {
		recs[i] = logs.Record{Seq: uint64(i + 1), Message: "m"}
	}
	data, _ := json.Marshal(map[string]any{"records": recs, "last": len(recs)})
	status, answer = 200, string(data)
	if b := tail(); len(b.Records) != MaxTailLimit || b.Records[0].Seq != 6 {
		t.Errorf("%d records, the first %d", len(b.Records), b.Records[0].Seq)
	}
	if _, err := tailQuery(map[string][]string{"limit": {"x"}}); err == nil {
		t.Error("limit x")
	}
}

func contextWithCancel(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithCancel(r.Context())
}
