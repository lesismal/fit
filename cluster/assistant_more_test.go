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

// certMember is a fit as a manager polls it, serving certs certificates,
// its /status held hold milliseconds once holding is set.
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
		switch r.URL.Path {
		case "/node":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "c", "capabilities": []string{"node", "status"}})
		case "/status":
			select {
			case <-time.After(time.Duration(c.hold.Load()) * time.Millisecond):
			case <-r.Context().Done():
				return
			}
			var list []map[string]any
			for i := range c.certs {
				list = append(list, map[string]any{"listener": "web", "names": []string{fmt.Sprintf("h%d.example", i)}, "issuer": "R3",
					"not_after": time.Now().Add(time.Duration(i+1) * 24 * time.Hour).UTC()})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"certificates": list, "reloads": map[string]int{"failed": 1}})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(c.srv.Close)
	return c
}

// TestAssistantCerts: the inventory is read from every member, the
// soonest first, bounded; a member not read in time gives its nearest
// expiry as its last poll read it; members wait their turn within
// max_concurrency, and those not reached when the read ends are said so.
func TestAssistantCerts(t *testing.T) {
	f := newFixture(t, config.Cluster{MaxNodes: 8, MaxConcurrency: 1})
	many, slow := newCertMember(t, assistantCerts+1), newCertMember(t, 1)
	f.add(tokenInput("b-many", many.srv.URL))
	f.add(tokenInput("a-slow", slow.srv.URL))
	f.waitState("a-slow", "to show its certificate", func(s State) bool { return s.Certificates != nil && !s.Certificates.NearestExpiry.IsZero() })
	a := f.m.Assistant()
	data, err := a.Certs(t.Context(), "admin", "", nil, 0)
	if err != nil || !strings.Contains(string(data), fmt.Sprintf("%d certificates; the %d that expire first", assistantCerts+2, assistantCerts)) {
		t.Fatalf("%v %.300s", err, data)
	}
	var c struct {
		Certificates []struct {
			Node     string
			DaysLeft int  `json:"days_left"`
			Stale    bool `json:"from_last_poll"`
		}
		NotRead []struct{ Node string } `json:"not_read"`
	}
	if data, _ = a.Certs(t.Context(), "admin", "", []string{"b-many"}, 2); json.Unmarshal(data, &c) != nil || len(c.Certificates) != 2 || c.Certificates[0].DaysLeft > c.Certificates[1].DaysLeft {
		t.Fatalf("%s", data)
	}
	// slow holds its answer: with one call at once, many waits behind it
	// and is not reached either; slow gives its last poll's.
	slow.hold.Store(1500)
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	data, err = a.Certs(ctx, "admin", "", []string{"b-many", "a-slow"}, 0)
	c.Certificates, c.NotRead = nil, nil
	if err != nil || json.Unmarshal(data, &c) != nil || len(c.NotRead) != 2 || len(c.Certificates) != 2 || !c.Certificates[0].Stale {
		t.Fatalf("%v %.500s", err, data)
	}
	// The metrics count the reloads that failed by group.
	slow.hold.Store(0)
	if data, err = a.Metrics(t.Context(), "admin", "", []string{"a-slow"}); err != nil || !strings.Contains(string(data), `"reloads_failed":1`) {
		t.Fatalf("%v %s", err, data)
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
