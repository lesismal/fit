package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/cluster"
)

// TestServeClusterCLI is I150's done-when against three real members: fit
// cluster lists the members, their states and groups through the manager's
// Admin API, shows their diffs against a template, drives a rolling rollout
// of it end to end and aborts a canary; the manager's series of a member
// match the member's own counters; a merged tail follows the three
// members' logs.
func TestServeClusterCLI(t *testing.T) {
	defer func(s func(time.Duration)) { clusterSleep = s }(clusterSleep)
	clusterSleep = func(time.Duration) { time.Sleep(50 * time.Millisecond) }
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "token"), "member-token-0123456789\n")
	writeFile(t, filepath.Join(dir, "mtoken"), "manager-token-0123456789\n")
	own := `"metrics": {}, "listeners": [{"name": "web", "address": "127.0.0.1:0"}], "routes": [{"name": "own", "match": {"path": ["/own"]}, "handler": {"module": "respond", "params": {"body": "own"}}}]}`
	members := map[string]string{}
	webs := map[string]string{}
	for _, n := range []string{"m1", "m2", "m3"} {
		cfg := filepath.Join(dir, n+".json")
		writeFile(t, cfg, `{"admin": {"address": "127.0.0.1:0", "token_file": `+js(filepath.Join(dir, "token"))+`}, `+own)
		s := startServe(t, "-c", cfg, "-watch-certs=false")
		out := s.out.waitFor(t, "fit: admin API on", 1)
		members[n] = "http://" + adminRE.FindStringSubmatch(out)[1]
		webs[n] = "http://" + listenerRE.FindStringSubmatch(out)[1]
	}
	mhash, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	manager := filepath.Join(dir, "manager.json")
	writeFile(t, manager, `{"admin": {"address": "127.0.0.1:0", "token_file": `+js(filepath.Join(dir, "mtoken"))+`,
"store": {"path": `+js(filepath.Join(dir, "m.store"))+`, "password": "s"},
"console": {"address": "127.0.0.1:0", "users": [{"username": "admin", "password_hash": `+js(string(mhash))+`}]},
"cluster": {"poll_interval": "1s"}}, "listeners": [], "routes": []}`)
	s := startServe(t, "-c", manager, "-watch-certs=false")
	out := s.out.waitFor(t, "fit: console on http://127.0.0.1:", 1)
	base := strings.TrimSuffix(consoleRE.FindStringSubmatch(out)[1], "/")
	// fit cluster reads where the Admin API is from a bootstrap config; the
	// manager's listens on a port of its own choosing, read back.
	cliCfg := filepath.Join(dir, "cli.json")
	writeFile(t, cliCfg, `{"admin": {"address": `+js(adminRE.FindStringSubmatch(out)[1])+`, "token_file": `+js(filepath.Join(dir, "mtoken"))+`}}`)
	fit := func(args ...string) (int, string, string) {
		t.Helper()
		full := append([]string{"cluster"}, args[0])
		if args[0] == "rollout" {
			full, args = append(full, args[1]), args[1:]
		}
		full = append(append(full, "-c", cliCfg), args[1:]...)
		return runFit(full...)
	}

	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, Timeout: 60 * time.Second}
	var csrf string
	do := func(method, url, body string, header ...string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, url, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Fit-Csrf", csrf)
		for i := 0; i+1 < len(header); i += 2 {
			req.Header.Set(header[i], header[i+1])
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(data)
	}
	code, body := do("POST", base+"/api/login", `{"username":"admin","password":"pw"}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var sess struct{ CSRF string }
	_ = json.Unmarshal([]byte(body), &sess)
	csrf = sess.CSRF
	api := func(method, path, body string, want int) string {
		t.Helper()
		code, got := do(method, base+"/api/m/cluster"+path, body)
		if code != want {
			t.Fatalf("%s %s: %d %s", method, path, code, got)
		}
		return got
	}
	for _, n := range []string{"m1", "m2", "m3"} {
		api("POST", "/nodes", `{"name":`+js(n)+`,"labels":{"tier":"edge"},"url":`+js(members[n])+`,"access":"admin_api","token":"member-token-0123456789"}`, 201)
	}
	api("POST", "/groups", `{"name":"edge","selector":"tier=edge"}`, 201)
	api("POST", "/templates", `{"name":"edge-api","groups":["edge"],"config":{
"routes":[{"name":"api","match":{"path":["/api"]},"handler":{"module":"respond","params":{"body":"api of ${node.name}"}}}]}}`, 201)

	// The members, their states once every one is up, the groups.
	if code, stdout, stderr := fit("nodes"); code != 0 || !strings.Contains(stdout, "m3") || !strings.Contains(stdout, "tier=edge") {
		t.Fatalf("nodes: %d %s %s", code, stdout, stderr)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		code, stdout, _ := fit("state")
		if code == 0 && strings.Count(stdout, " up ") == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the members are not up: %s", stdout)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if code, stdout, _ := fit("groups"); code != 0 || !strings.Contains(stdout, "m1,m2,m3") {
		t.Errorf("groups: %d %s", code, stdout)
	}
	// Every member lacks the template's route.
	if code, stdout, stderr := fit("diff", "-template", "edge-api"); code != 1 || strings.Count(stdout, ": out_of_sync") != 3 || !strings.Contains(stdout, "routes[api]") {
		t.Fatalf("diff: %d %s %s", code, stdout, stderr)
	}

	// A rolling rollout, a member a batch, end to end.
	code, stdout, stderr := fit("rollout", "start", "-template", "edge-api", "-mode", "rolling", "-batch-size", "1", "-gate-wait", "100ms", "-yes", "-wait")
	if code != 0 || !strings.Contains(stdout, ": done") || strings.Count(stdout, "routes[api]") < 3 {
		t.Fatalf("rollout start: %d %s %s", code, stdout, stderr)
	}
	id := regexp.MustCompile(`rollout ([0-9a-f]+) \(`).FindStringSubmatch(stdout)[1]
	for _, n := range []string{"m1", "m2", "m3"} {
		if got := get(t, webs[n]+"/api"); got != "api of "+n {
			t.Errorf("%s answers %q", n, got)
		}
	}
	if code, stdout, _ := fit("diff", "-node", "m1,m2,m3"); code != 0 || strings.Count(stdout, ": in_sync") != 3 {
		t.Errorf("diff after: %d %s", code, stdout)
	}
	if code, stdout, _ := fit("rollout", "status", id); code != 0 || !strings.Contains(stdout, "done") {
		t.Errorf("status: %d %s", code, stdout)
	}
	if code, stdout, _ := fit("rollout", "list"); code != 0 || !strings.Contains(stdout, id) {
		t.Errorf("list: %d %s", code, stdout)
	}
	audit := api("GET", "/audit", "", 200)
	if !strings.Contains(audit, `"user":"Admin API"`) || !strings.Contains(audit, `"action":"rollout.start"`) {
		t.Errorf("the audit log: %s", audit)
	}

	// A canary waits for its go, and is aborted.
	api("PUT", "/templates/edge-api", `{"name":"edge-api","groups":["edge"],"config":{
"routes":[{"name":"api","match":{"path":["/api"]},"handler":{"module":"respond","params":{"body":"api v2 of ${node.name}"}}}]}}`, 200)
	code, stdout, stderr = fit("rollout", "start", "-template", "edge-api", "-mode", "canary", "-canary", "1", "-gate-wait", "100ms", "-yes", "-wait")
	if code != 0 || !strings.Contains(stdout, "waiting") || !strings.Contains(stdout, "fit cluster rollout go") {
		t.Fatalf("canary: %d %s %s", code, stdout, stderr)
	}
	canary := regexp.MustCompile(`rollout go ([0-9a-f]+)`).FindStringSubmatch(stdout)[1]
	if code, stdout, stderr := fit("rollout", "abort", canary); code != 0 || !strings.Contains(stdout, "aborted") {
		t.Errorf("abort: %d %s %s", code, stdout, stderr)
	}
	if code, _, _ := fit("rollout", "status", canary, "-json"); code != 1 {
		t.Errorf("an aborted rollout's status exits %d", code)
	}

	// The series match the members' own counters: once the manager has
	// polled after the requests, its counters are the member's.
	for range 7 {
		get(t, webs["m2"]+"/api")
	}
	var m2own struct {
		Requests cluster.RequestTotals `json:"requests"`
	}
	_, raw := do("GET", members["m2"]+"/status", "", "Authorization", "Bearer member-token-0123456789")
	if err := json.Unmarshal([]byte(raw), &m2own); err != nil || m2own.Requests.Total < 7 {
		t.Fatalf("m2's own /status: %s", raw)
	}
	var mb cluster.MetricsBody
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("waited too long for %s", what)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	waitFor("the manager's counters of m2", func() bool {
		_, stdout, _ := fit("metrics", "-node", "m2", "-json")
		mb = cluster.MetricsBody{}
		_ = json.Unmarshal([]byte(stdout), &mb)
		return len(mb.Nodes) == 1 && mb.Nodes[0].Counters != nil && mb.Nodes[0].Counters.Requests != nil &&
			mb.Nodes[0].Counters.Requests.Total == m2own.Requests.Total
	})
	if got := mb.Nodes[0].Counters.Requests.Classes["2xx"]; got != m2own.Requests.Classes["2xx"] {
		t.Errorf("2xx: the manager %d, the member %d", got, m2own.Requests.Classes["2xx"])
	}
	if code, stdout, _ := fit("metrics", "-group", "edge"); code != 0 || !strings.Contains(stdout, "group edge") {
		t.Errorf("metrics of the group: %d %s", code, stdout)
	}

	// A merged tail of the three members: each one's log, by time.
	code, stdout, stderr = fit("logs", "-node", "m1,m2,m3", "-json")
	var tail cluster.TailBody
	if err := json.Unmarshal([]byte(stdout), &tail); code != 0 || err != nil {
		t.Fatalf("logs: %d %s %s", code, stdout, stderr)
	}
	seen := map[string]bool{}
	for i, r := range tail.Records {
		seen[r.Node] = true
		if i > 0 && r.Time.Before(tail.Records[i-1].Time) {
			t.Errorf("not merged by time at %d: %+v", i, tail.Records)
		}
	}
	if len(seen) != 3 {
		t.Errorf("the tail has the logs of %v", seen)
	}
	defer func(n int) { clusterFollowRounds = n }(clusterFollowRounds)
	clusterFollowRounds = 2
	if code, stdout, _ := fit("logs", "-node", "m1,m2,m3", "-f", "-level", "info", "-limit", "5", "-logger", "admin", "-contains", "admin"); code != 0 || !strings.Contains(stdout, " m1 INFO admin:") {
		t.Errorf("logs -f: %d %s", code, stdout)
	}

	// No member serves a certificate.
	if code, stdout, _ := fit("certs", "-within", "30d"); code != 0 || !strings.Contains(stdout, "no member serves a certificate") {
		t.Errorf("certs: %d %s", code, stdout)
	}
	if code := s.stop(t); code != 0 {
		t.Fatalf("stop: %d %q", code, s.err.String())
	}
	if code, _, stderr := fit("nodes"); code != 1 || stderr == "" {
		t.Errorf("a manager stopped: %d %s", code, stderr)
	}
}
