package cluster

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
)

// adminAPI serves the manager's Admin API endpoints, and a client of them.
type adminAPI struct {
	t   *testing.T
	url string
}

func (f *fixture) adminAPI() *adminAPI {
	srv := httptest.NewServer(f.m.AdminAPI())
	f.t.Cleanup(srv.Close)
	return &adminAPI{t: f.t, url: srv.URL}
}

func (a *adminAPI) do(method, path, body, contentType string) (int, string) {
	a.t.Helper()
	req, _ := http.NewRequest(method, a.url+path, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data)
}

// TestAdminAPI checks the manager's endpoints in its Admin API, for fit
// cluster: reads, and rollouts planned, read and steered as the Admin
// API's user, a cluster-wide admin whatever the roles say, refusals at the
// console module's statuses.
func TestAdminAPI(t *testing.T) {
	f, us := rolesFixture(t, config.Cluster{})
	im := newInsightMember(t)
	im.log(time.Now(), "warn", "from c")
	if _, err := f.m.Add("ada", tokenInput("c", im.srv.URL)); err != nil {
		t.Fatal(err)
	}
	f.waitUp("c")
	a := f.adminAPI()
	const ct = "application/json"
	for path, want := range map[string]string{
		"/cluster":                    `"rollouts"`,
		"/cluster/nodes":              `"name":"c"`,
		"/cluster/state":              `"interval_ms"`,
		"/cluster/groups":             `"name":"prod"`,
		"/cluster/drift":              `"node":"c"`,
		"/cluster/metrics?node=c":     `"series"`,
		"/cluster/certificates":       `"nodes"`,
		"/cluster/logs?node=c":        `from c`,
		"/cluster/rollouts":           `"max_active"`,
		"/cluster/metrics?points=999": `points`,
	} {
		code, body := a.do("GET", path, "", "")
		if !strings.Contains(body, want) || (code != 200) != strings.Contains(path, "999") {
			t.Errorf("GET %s: %d %s", path, code, body)
		}
	}
	if code, body := a.do("POST", "/cluster/sync", `{"nodes":["c"]}`, ct); code != 200 || !strings.Contains(body, `"node":"c"`) {
		t.Errorf("sync: %d %s", code, body)
	}
	for _, c := range []struct {
		method, path, body, ct string
		want                   int
	}{
		{"GET", "/cluster/zz", "", "", 404},
		{"GET", "/cluster/logs", "", "", 400},
		{"GET", "/cluster/logs?node=c&limit=x", "", "", 400},
		{"GET", "/cluster/metrics?node=zz", "", "", 404},
		{"POST", "/cluster/sync", `{"nodes":["zz"]}`, ct, 404},
		{"POST", "/cluster/sync", `{}`, "text/plain", 415},
		{"POST", "/cluster/sync", `{`, ct, 400},
		{"POST", "/cluster/sync", `"` + strings.Repeat("x", MaxBodyBytes) + `"`, ct, 413},
		{"POST", "/cluster/rollouts", `{"kind":"nope"}`, ct, 400},
		{"POST", "/cluster/rollouts", `{"kind":"template","template":"zz"}`, ct, 404},
		{"GET", "/cluster/rollouts/r9", "", "", 404},
		{"DELETE", "/cluster/rollouts/r9", "", "", 404},
		{"PUT", "/cluster/rollouts/r9", "", "", 405},
		{"POST", "/cluster/rollouts/r9/start", `{"strategy":{"mode":"all"}}`, ct, 404},
		{"POST", "/cluster/rollouts/r9/start", `{`, ct, 400},
		{"POST", "/cluster/rollouts/r9/abort", "", "", 404},
		{"POST", "/cluster/rollouts/r9/explode", "", "", 404},
	} {
		if code, body := a.do(c.method, c.path, c.body, c.ct); code != c.want {
			t.Errorf("%s %s: %d %s, want %d", c.method, c.path, code, body, c.want)
		}
	}

	// A sync rollout of c, which nothing manages: planned as the Admin
	// API's user, though the roles name no such user, and audited so.
	code, body := a.do("POST", "/cluster/rollouts", `{"kind":"sync","nodes":["c"]}`, ct)
	var ro Rollout
	if err := json.Unmarshal([]byte(body), &ro); code != 202 || err != nil || ro.PlannedBy != AdminAPIUser {
		t.Fatalf("plan: %d %s", code, body)
	}
	waitFor(t, "the plan", func() bool { r, _ := f.m.RolloutByID(ro.ID); return r.State != RolloutPlanning })
	if code, body := a.do("GET", "/cluster/rollouts/"+ro.ID, "", ""); code != 200 || !strings.Contains(body, `"id":"`+ro.ID+`"`) {
		t.Errorf("read: %d %s", code, body)
	}
	if code, body := a.do("POST", "/cluster/rollouts/"+ro.ID+"/start", `{"strategy":{"mode":"all"}}`, ct); code != 409 {
		t.Errorf("start with nothing to apply: %d %s", code, body)
	}
	if code, body := a.do("DELETE", "/cluster/rollouts/"+ro.ID, "", ""); code != 200 {
		t.Errorf("discard: %d %s", code, body)
	}
	audit := f.m.Audit()
	if e := audit[len(audit)-1]; e.User != AdminAPIUser {
		t.Errorf("the audit's last entry: %+v", e)
	}

	// No console user is the Admin API.
	if err := f.m.Allow(AdminAPIUser, Need{Role: RoleAdmin, Cluster: true}); err != nil {
		t.Errorf("the Admin API: %v", err)
	}
	if f.m.AssistantCap(AdminAPIUser) != config.AssistantRead {
		t.Errorf("an assistant for a console user named as the Admin API")
	}
	if why := f.m.ConsoleGate(AdminAPIUser, "POST", "/api/m/admin/config", ""); why == "" {
		t.Error("a console user named as the Admin API changes the manager")
	}
	if why := f.m.ConsoleGate(AdminAPIUser, "GET", "/api/m/admin/config", ""); why != "" {
		t.Errorf("a read: %s", why)
	}
	_ = us
	g := f.serveUsers(AdminAPIUser)
	if code, body, _ := g[AdminAPIUser].do("GET", "/api/m/cluster/nodes", ""); code != 403 || !strings.Contains(body, "Admin API") {
		t.Errorf("a console user named as the Admin API: %d %s", code, body)
	}
}

// TestAdminAPIClosed: a manager closed answers its Admin API's changes 503.
func TestAdminAPIClosed(t *testing.T) {
	f := newFixture(t, config.Cluster{})
	a := f.adminAPI()
	f.m.Close()
	if code, body := a.do("POST", "/cluster/rollouts/r1/abort", "", ""); code != 503 {
		t.Errorf("%d %s", code, body)
	}
}
