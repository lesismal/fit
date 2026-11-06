package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// TestServeClusterTemplates is I145's done-when, against two real members:
// a template of a route and an upstream attached to a group renders per
// member with its variables; the members' other routes are untouched; each
// member is told what the manager owns there; a route the template owns,
// applied and then changed on a member, is a conflict on the manager and
// marked as changed on the member; a template made from a member's config
// reproduces it; two members are compared; and the template's text tabs
// read and write .conf with its variables.
func TestServeClusterTemplates(t *testing.T) {
	dir := t.TempDir()
	hash, _ := bcrypt.GenerateFromPassword([]byte("member-pw"), bcrypt.MinCost)
	writeFile(t, filepath.Join(dir, "token"), "member-token-0123456789\n")
	own := func(body string) string {
		return `"listeners": [{"name": "web", "address": "127.0.0.1:0"}], "routes": [{"name": "own", "handler": {"module": "respond", "params": {"body": "` + body + `"}}}]}`
	}
	// m1 keeps what it is told in its store; m2, without one, in memory.
	m1 := filepath.Join(dir, "m1.json")
	writeFile(t, m1, `{"admin": {"address": "127.0.0.1:0", "store": {"path": `+js(filepath.Join(dir, "m1.store"))+`, "password": "s"},
"console": {"address": "127.0.0.1:0", "users": [{"username": "ops", "password_hash": `+js(string(hash))+`}]}}, `+own("one"))
	s1 := startServe(t, "-c", m1, "-watch-certs=false")
	out := s1.out.waitFor(t, "fit: console on http://127.0.0.1:", 1)
	m1Console := strings.TrimSuffix(consoleRE.FindStringSubmatch(out)[1], "/")
	m1Admin := "http://" + adminRE.FindStringSubmatch(out)[1]
	m2 := filepath.Join(dir, "m2.json")
	writeFile(t, m2, `{"admin": {"address": "127.0.0.1:0", "token_file": `+js(filepath.Join(dir, "token"))+`}, `+own("two"))
	s2 := startServe(t, "-c", m2, "-watch-certs=false")
	m2Admin := "http://" + adminRE.FindStringSubmatch(s2.out.waitFor(t, "fit: admin API on", 1))[1]

	mhash, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	manager := filepath.Join(dir, "manager.json")
	writeFile(t, manager, `{"admin": {"address": "127.0.0.1:0", "node_id": "the-manager", "store": {"path": `+js(filepath.Join(dir, "m.store"))+`, "password": "s"},
"console": {"address": "127.0.0.1:0", "users": [{"username": "admin", "password_hash": `+js(string(mhash))+`}]},
"cluster": {"poll_interval": "1s"}}, `+own("manager"))
	s := startServe(t, "-c", manager, "-watch-certs=false")
	base := strings.TrimSuffix(consoleRE.FindStringSubmatch(s.out.waitFor(t, "fit: console on http://127.0.0.1:", 1))[1], "/")

	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, Timeout: 60 * time.Second}
	var csrf string
	do := func(method, url, body string, header ...string) (int, string, http.Header) {
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
		return resp.StatusCode, string(data), resp.Header
	}
	api := func(method, path, body string, want int) string {
		t.Helper()
		code, got, _ := do(method, base+"/api/m/cluster"+path, body)
		if code != want {
			t.Fatalf("%s %s: %d %s", method, path, code, got)
		}
		return got
	}
	code, body, _ := do("POST", base+"/api/login", `{"username":"admin","password":"pw"}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var sess struct{ CSRF string }
	_ = json.Unmarshal([]byte(body), &sess)
	csrf = sess.CSRF
	api("POST", "/nodes", `{"name":"m1","labels":{"tier":"edge","region":"eu"},"url":`+js(m1Console)+`,"access":"console","username":"ops","password":"member-pw"}`, 201)
	api("POST", "/nodes", `{"name":"m2","labels":{"tier":"edge","region":"us"},"url":`+js(m2Admin)+`,"access":"admin_api","token":"member-token-0123456789"}`, 201)
	api("POST", "/groups", `{"name":"edge","selector":"tier=edge","variables":{"backend":"127.0.0.1:9"}}`, 201)
	api("POST", "/templates", `{"name":"edge-api","groups":["edge"],"config":{
"upstreams":[{"name":"api","addresses":["${backend}"]}],
"routes":[{"name":"api","handler":{"module":"respond","params":{"body":"api of ${node.name} in ${node.labels.region}"}}}]}}`, 201)

	type part struct{ Path, State, Reason, Template string }
	type msync struct {
		Node, State, Managed string
		Parts                []part
		Errors               []string
	}
	syncOf := func(node string) (msync, string) {
		t.Helper()
		var b struct {
			Nodes   []msync
			Pending []string
		}
		raw := api("POST", "/sync", `{"nodes":[`+js(node)+`]}`, 200)
		if err := json.Unmarshal([]byte(raw), &b); err != nil || len(b.Nodes) != 1 {
			t.Fatalf("%s %v", raw, err)
		}
		return b.Nodes[0], raw
	}
	stateOf := func(ms msync, path string) part {
		for _, p := range ms.Parts {
			if p.Path == path {
				return p
			}
		}
		t.Fatalf("%s has no part %s: %+v", ms.Node, path, ms)
		return part{}
	}
	// Both are out of sync, the route and the upstream missing, until
	// their desired configs are applied, as a rollout (I146) will.
	for _, n := range []string{"m1", "m2"} {
		// A member's console checks one login from an address at a time:
		// the poller's and the sync's may meet, and the second is told to
		// wait (rate_limited); the sync is asked again until it is read.
		ms, raw := syncOf(n)
		for deadline := time.Now().Add(15 * time.Second); ms.State == "error" && time.Now().Before(deadline); {
			time.Sleep(100 * time.Millisecond)
			ms, raw = syncOf(n)
		}
		if ms.State != "out_of_sync" || stateOf(ms, "routes[api]").Reason != "missing" || stateOf(ms, "upstreams[api]").State != "out_of_sync" || ms.Managed == "failed" {
			t.Fatalf("%s: %s", n, raw)
		}
	}
	memberAPI := map[string]func(method, path, body string, header ...string) (int, string, http.Header){
		"m1": func(method, path, body string, header ...string) (int, string, http.Header) {
			// m1's Admin API needs no token: it has none.
			return do(method, m1Admin+path, body, header...)
		},
		"m2": func(method, path, body string, header ...string) (int, string, http.Header) {
			return do(method, m2Admin+path, body, append(header, "Authorization", "Bearer member-token-0123456789")...)
		},
	}
	for _, n := range []string{"m1", "m2"} {
		var d struct {
			State      string
			ConfigETag string `json:"config_etag"`
			Desired    json.RawMessage
		}
		raw := api("GET", "/desired/"+n, "", 200)
		if err := json.Unmarshal([]byte(raw), &d); err != nil || d.ConfigETag == "" {
			t.Fatalf("%s %v", raw, err)
		}
		region := map[string]string{"m1": "eu", "m2": "us"}[n]
		// Rendered with the member's own variables; its own route kept.
		if !strings.Contains(string(d.Desired), "api of "+n+" in "+region) || !strings.Contains(string(d.Desired), `"127.0.0.1:9"`) ||
			!strings.Contains(string(d.Desired), `"name":"own"`) {
			t.Fatalf("%s: %s", n, d.Desired)
		}
		if code, got, _ := memberAPI[n]("PUT", "/config", string(d.Desired), "If-Match", d.ConfigETag); code != 200 {
			t.Fatalf("%s: %d %s", n, code, got)
		}
		ms, raw := syncOf(n)
		if ms.State != "in_sync" || ms.Managed == "failed" {
			t.Fatalf("%s: %s", n, raw)
		}
		// The member knows what the manager owns, and nothing changed.
		_, got, _ := memberAPI[n]("GET", "/node/managed", "")
		kept := map[string]string{"m1": `"kept":"store"`, "m2": `"kept":"memory"`}[n]
		if !strings.Contains(got, `"manager":"the-manager"`) || !strings.Contains(got, `"path":"routes[api]"`) || strings.Contains(got, `"changed":true`) || !strings.Contains(got, kept) {
			t.Fatalf("%s: %s", n, got)
		}
	}
	// m1's owned route changed on m1 itself: a conflict on the manager,
	// marked changed on the member; m2 is untouched.
	_, cur, h := memberAPI["m1"]("GET", "/config", "")
	changed := strings.Replace(cur, "api of m1 in eu", "changed by hand", 1)
	if code, got, _ := memberAPI["m1"]("PUT", "/config", changed, "If-Match", h.Get("ETag")); code != 200 {
		t.Fatalf("%d %s", code, got)
	}
	ms, raw := syncOf("m1")
	if p := stateOf(ms, "routes[api]"); ms.State != "conflict" || p.State != "conflict" || p.Reason != "changed_here" || stateOf(ms, "upstreams[api]").State != "in_sync" {
		t.Fatalf("%s", raw)
	}
	if _, got, _ := memberAPI["m1"]("GET", "/node/managed", ""); !strings.Contains(got, `"path":"routes[api]","present":true,"changed":true`) {
		t.Fatalf("%s", got)
	}
	// Through the console, as the member's own pages read it.
	if ms2, raw := syncOf("m2"); ms2.State != "in_sync" {
		t.Fatalf("%s", raw)
	}

	// A template made from m2's config reproduces it: attached to a group
	// of m2 alone, its parts are in sync, with their values.
	imp := api("POST", "/import", `{"node":"m2"}`, 200)
	if !strings.Contains(imp, `"path":"routes[own]","member":"routes","name":"own","ownable":true`) || !strings.Contains(imp, `"path":"listeners[web]","member":"listeners","name":"web","ownable":true`) {
		t.Fatalf("%s", imp)
	}
	api("POST", "/groups", `{"name":"just-m2","members":["m2"]}`, 201)
	tv := api("POST", "/templates", `{"name":"from-m2","groups":["just-m2"],"order":-1,"from":{"node":"m2","paths":["routes[own]","listeners[web]"]}}`, 201)
	if !strings.Contains(tv, `"path":"routes[own]"`) || !strings.Contains(tv, `"body":"two"`) {
		t.Fatalf("%s", tv)
	}
	ms, raw = syncOf("m2")
	if ms.State != "in_sync" || stateOf(ms, "routes[own]").Template != "from-m2" || stateOf(ms, "listeners[web]").State != "in_sync" {
		t.Fatalf("%s", raw)
	}
	// The two members compared, part by part.
	cmp := api("POST", "/compare", `{"a":"m1","b":"m2"}`, 200)
	for _, want := range []string{`{"path":"routes[own]","state":"differs"`, `{"path":"routes[api]","state":"differs"`, `{"path":"upstreams[api]","state":"same"`} {
		if !strings.Contains(cmp, want) {
			t.Fatalf("%s lacks %s", cmp, want)
		}
	}
	// The editor's .conf tab, with a variable.
	code, text, _ := do("POST", base+"/api/m/cluster/text/render?format=conf", `{"routes":[{"name":"x","handler":{"module":"respond","params":{"body":"${node.name}"}}}]}`)
	if code != 200 || !strings.Contains(text, "${node.name}") {
		t.Fatalf("%d %s", code, text)
	}
	req, _ := http.NewRequest("POST", base+"/api/m/cluster/text/parse?format=conf", strings.NewReader(text))
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("X-Fit-Csrf", csrf)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(parsed), `"body": "${node.name}"`) {
		t.Fatalf("%d %s", resp.StatusCode, parsed)
	}
	// The template removed: m1 is told it owns nothing now.
	api("DELETE", "/templates/edge-api", "", 200)
	deadline := time.Now().Add(30 * time.Second)
	for {
		_, got, _ := memberAPI["m1"]("GET", "/node/managed", "")
		if strings.Contains(got, `"paths":[]`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("m1 still owns: %s", got)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if audit := api("GET", "/audit", "", 200); !strings.Contains(audit, `"action":"template.add","template":"edge-api"`) || !strings.Contains(audit, `"action":"template.remove","template":"edge-api"`) {
		t.Fatalf("%s", audit)
	}
}
