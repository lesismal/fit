package cluster

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestRolloutAPI drives a rollout through the module's endpoints, as the
// pages do, and each refusal.
func TestRolloutAPI(t *testing.T) {
	k := newRoFixture(t, 3)
	s := k.s
	var r Rollout
	if err := json.Unmarshal([]byte(s.must("POST", "/rollouts", `{"kind":"template","template":"api","description":"api"}`, 202)), &r); err != nil {
		t.Fatal(err)
	}
	k.waitRollout(r.ID, "planned", func(r Rollout) bool { return r.State == RolloutPlanned })
	got := s.must("GET", "/rollouts/"+r.ID, "", 200)
	if !strings.Contains(got, `"changes":[{"path":"routes[api]"`) || strings.Contains(got, `"config"`) || strings.Contains(got, fakeToken) {
		t.Fatalf("%s", got)
	}
	list := s.must("GET", "/rollouts", "", 200)
	if !strings.Contains(list, `"max_active":4`) || strings.Contains(list, `"have"`) {
		t.Fatalf("%s", list)
	}
	s.must("POST", "/rollouts", `{"kind":"nope"}`, 400)
	s.must("POST", "/rollouts", `{"kind":"template","template":"none"}`, 404)
	s.must("POST", "/rollouts", `{`, 400)
	s.must("POST", "/rollouts", `{"description":"`+strings.Repeat("x", MaxBodyBytes)+`"}`, 413)
	s.must("GET", "/rollouts/none", "", 404)
	s.must("PUT", "/rollouts/"+r.ID, "", 405)
	s.must("POST", "/rollouts/"+r.ID+"/nope", "", 404)
	s.must("POST", "/rollouts/"+r.ID+"/start", `{"strategy":{"mode":"rolling"}}`, 400)
	s.must("POST", "/rollouts/"+r.ID+"/start", `{`, 400)
	s.must("POST", "/rollouts/"+r.ID+"/start", `{"strategy":{"mode":"all"},"exclude":["`+strings.Repeat("x", MaxBodyBytes)+`"]}`, 413)
	s.must("POST", "/rollouts/"+r.ID+"/pause", "", 409)
	s.must("POST", "/rollouts/none/pause", "", 404)
	s.must("POST", "/rollouts/"+r.ID+"/start", `{"strategy":{"mode":"canary","canary":1,"gate":{"wait_ms":1}}}`, 200)
	k.waitRollout(r.ID, "waiting", func(r Rollout) bool { return r.State == RolloutWaiting })
	s.must("DELETE", "/rollouts/"+r.ID, "", 409)
	s.must("POST", "/rollouts/"+r.ID+"/go", "", 200)
	r = k.waitRollout(r.ID, "done", ended)
	if r.State != RolloutDone {
		t.Fatalf("%s %s", r.State, r.Reason)
	}
	s.must("POST", "/rollouts/"+r.ID+"/rollback", "", 200)
	k.waitRollout(r.ID, "rolled back", func(r Rollout) bool { return r.State == RolloutRolledBack })
	// A plan discarded.
	_ = json.Unmarshal([]byte(s.must("POST", "/rollouts", `{"kind":"sync"}`, 202)), &r)
	k.waitRollout(r.ID, "planned", func(r Rollout) bool { return r.State == RolloutPlanned })
	if got := s.must("DELETE", "/rollouts/"+r.ID, "", 200); !strings.Contains(got, r.ID) {
		t.Error(got)
	}
	audit := s.must("GET", "/audit", "", 200)
	for _, a := range []string{"rollout.plan", "rollout.start", "rollout.go", "rollout.end", "rollout.rollback", "rollout.discard"} {
		if !strings.Contains(audit, `"action":"`+a+`"`) {
			t.Errorf("no %s in the audit log", a)
		}
	}
	k.m.Close()
	s.must("POST", "/rollouts", `{"kind":"sync"}`, 503)
	s.must("POST", "/rollouts/"+r.ID+"/pause", "", 503)
}
