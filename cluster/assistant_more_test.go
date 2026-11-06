package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
)

// certMember is a fit as a manager polls it, serving certs certificates and
// a line of its log ring, its /status and /logs/recent held hold
// milliseconds.
type certMember struct {
	srv   *httptest.Server
	certs int
	hold  atomic.Int64
}

func newCertMember(t *testing.T, certs int) *certMember {
	t.Helper()
	c := &certMember{certs: certs}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+fakeToken {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		hold := func() bool {
			select {
			case <-time.After(time.Duration(c.hold.Load()) * time.Millisecond):
				return true
			case <-r.Context().Done():
				return false
			}
		}
		switch r.URL.Path {
		case "/node":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "c", "capabilities": []string{"node", "status", "logs.recent"}})
		case "/status":
			if !hold() {
				return
			}
			var list []map[string]any
			for i := range c.certs {
				list = append(list, map[string]any{"listener": "web", "names": []string{fmt.Sprintf("h%d.example", i)}, "issuer": "R3",
					"not_after": time.Now().Add(time.Duration(i+1)*24*time.Hour + time.Hour).UTC()})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"certificates": list, "reloads": map[string]int{"failed": 1}})
		case "/logs/recent":
			if !hold() {
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"records": []map[string]any{{"seq": 7, "time": time.Now().UTC(), "level": "warn",
				"logger": "server", "message": "upstream down " + r.URL.Query().Get("limit")}}, "last": 7})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(c.srv.Close)
	return c
}

// TestAssistantInsight: the inventory is the members' certificates as
// polled, the soonest first, bounded; the series come with the metrics;
// a log tail merges the members' lines and ends with the read when a
// member is slow; members wait their turn within max_concurrency, and
// those not reached when a read ends are said so.
func TestAssistantInsight(t *testing.T) {
	f := newFixture(t, config.Cluster{MaxNodes: 8, MaxConcurrency: 1})
	many, more, slow := newCertMember(t, MaxMemberCerts), newCertMember(t, MaxMemberCerts), newCertMember(t, 1)
	f.add(tokenInput("b-many", many.srv.URL))
	f.add(tokenInput("b-more", more.srv.URL))
	f.add(tokenInput("a-slow", slow.srv.URL))
	waitFor(t, "the inventory", func() bool { return len(f.m.Certificates().Certificates) == 2*MaxMemberCerts+1 })
	a := f.m.Assistant()
	data, err := a.Certs(t.Context(), "admin", "", nil, 0)
	if err != nil || !strings.Contains(string(data), fmt.Sprintf("%d certificates; the %d that expire first", 2*MaxMemberCerts+1, assistantCerts)) {
		t.Fatalf("%v %.300s", err, data)
	}
	var c struct {
		Certificates []struct {
			Node     string
			DaysLeft int `json:"days_left"`
		}
	}
	if data, _ = a.Certs(t.Context(), "admin", "", []string{"b-many"}, 2); json.Unmarshal(data, &c) != nil || len(c.Certificates) != 2 || c.Certificates[0].DaysLeft > c.Certificates[1].DaysLeft {
		t.Fatalf("%s", data)
	}
	gone := newCertMember(t, 0)
	f.add(tokenInput("c-gone", gone.srv.URL))
	gone.srv.Close()
	if data, _ = a.Certs(t.Context(), "admin", "", []string{"c-gone"}, 0); !strings.Contains(string(data), `"not_read":[{"name":"c-gone"`) {
		t.Fatalf("%s", data)
	}
	if _, err := a.Certs(t.Context(), "admin", "", nil, -1); err == nil {
		t.Error("negative days")
	}
	// The metrics carry the series, and count the reloads that failed.
	if data, err = a.Metrics(t.Context(), "admin", "", []string{"a-slow"}); err != nil || !strings.Contains(string(data), `"reloads_failed":1`) ||
		!strings.Contains(string(data), `"series":{"interval_ms"`) {
		t.Fatalf("%v %s", err, data)
	}
	// A tail merges the members' lines.
	if data, err = a.Logs(t.Context(), "admin", "", []string{"a-slow", "b-many"}, "", "warn", "", 0); err != nil ||
		strings.Count(string(data), "upstream down 50") != 2 || strings.Contains(string(data), `"cursor":"e`) {
		t.Fatalf("%v %s", err, data)
	}
	for _, c := range []struct {
		group string
		nodes []string
	}{{"", nil}, {"", make([]string, MaxTailNodes+1)}, {"nope", nil}} {
		if _, err := a.Logs(t.Context(), "admin", c.group, c.nodes, "", "", "", 0); err == nil {
			t.Errorf("%v", c)
		}
	}
	if _, err := a.Logs(t.Context(), "admin", "", []string{"a-slow"}, "", "loud", "", 0); err == nil {
		t.Error("a level")
	}
	// a-slow holds its answers: with one call at once, b-many waits behind
	// it and is not reached either; the tail ends with its wait.
	slow.hold.Store(1500)
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	data, err = a.Metrics(ctx, "admin", "", []string{"b-many", "a-slow"})
	if err != nil || strings.Count(string(data), `"kind":"timeout"`) != 2 {
		t.Fatalf("%v %.500s", err, data)
	}
	ctx, cancel = context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	if _, err := a.Logs(ctx, "admin", "", []string{"a-slow"}, "", "", "", 0); err == nil || !strings.Contains(err.Error(), "did not come in time") {
		t.Fatal(err)
	}
}

// TestAssistantDraftMember: a draft is rendered on a member read as a sync
// reads it, with the record of what it owns, and each failure of the read
// is the member's problem.
func TestAssistantDraftMember(t *testing.T) {
	f, _, ms := newTplFixture(t)
	if _, err := f.m.AddTemplate("tester", &TemplateInput{Name: "t1", Groups: []string{"edge"},
		Config: json.RawMessage(`{"routes":[{"name":"api","handler":{"module":"proxy","params":{"to":"${backend}"}}}]}`)}); err != nil {
		t.Fatal(err)
	}
	draft := json.RawMessage(`{"routes":[{"name":"api","handler":{"module":"respond","params":{"body":"${node.name}"}}}]}`)
	read := func(name string) *syncOut {
		ch := make(chan *syncOut, 1)
		f.m.draftMember(name, "t1", draft, func(o *syncOut) { ch <- o })
		return <-ch
	}
	o := read("a")
	if o.desired == nil || !strings.Contains(string(o.desired.bytes()), `"body":"a"`) || o.sync.Note == "" || o.sync.ConfigETag == "" {
		t.Fatalf("%+v", o.sync)
	}
	if v, _ := f.m.TemplateByName("t1"); strings.Contains(string(v.Config), "respond") {
		t.Fatal("the draft was saved")
	}
	ms["a"].set(func(c *cfgMember) { c.noManaged = true })
	ms["b"].set(func(c *cfgMember) { c.cfgFail = 500 })
	ms["c"].set(func(c *cfgMember) { c.badCfg = true })
	gone := newCfgMember(t, `{}`)
	f.add(tokenInput("gone", gone.srv.URL))
	gone.srv.Close()
	for name, want := range map[string]string{"a": "/node/managed", "b": "no config", "c": "not a JSON object", "gone": KindUnreachable} {
		o := read(name)
		if o.desired != nil || o.sync.State != SyncError || o.sync.Error == nil || !strings.Contains(o.sync.Error.Kind+" "+o.sync.Error.Message, want) {
			t.Errorf("%s: %+v", name, o.sync.Error)
		}
	}
	ms["a"].set(func(c *cfgMember) { c.noManaged, c.noCap = false, true })
	if o := read("a"); o.desired == nil {
		t.Fatalf("%+v", o.sync)
	}
}

// TestAssistantListsMore: the members' alerts firing, a template's member
// not read yet, and what a patch's targets refuse.
func TestAssistantListsMore(t *testing.T) {
	k := newRoFixture(t, 2)
	a := k.m.Assistant()
	k.m.al.mu.Lock()
	if k.m.al.active == nil {
		k.m.al.active = map[string]*Alert{}
	}
	k.m.al.active["x"] = &Alert{Rule: "down-now", Severity: SeverityCritical, Node: "m1", State: "firing", Since: time.Now()}
	k.m.al.mu.Unlock()
	k.m.tpl.mu.Lock()
	k.m.tpl.last = map[string]MemberSync{}
	k.m.tpl.mu.Unlock()
	data, err := a.Templates("ann", "api")
	if err != nil || !strings.Contains(string(data), `"state":"not_read"`) {
		t.Fatalf("%v %s", err, data)
	}
	if data, err = a.Members("ann", "", "", ""); err != nil || !strings.Contains(string(data), `"alerts_firing":["down-now (critical)"]`) {
		t.Fatalf("%v %s", err, data)
	}
	for _, req := range []string{`{"kind":"patch","group":"nope","config":{"log":{}}}`, `{"kind":"patch","nodes":["x"],"config":{"log":{}}}`} {
		if _, err := a.PlanRollout(t.Context(), "ann", []byte(req)); err == nil || !strings.Contains(err.Error(), "no ") {
			t.Errorf("%s: %v", req, err)
		}
	}
}
