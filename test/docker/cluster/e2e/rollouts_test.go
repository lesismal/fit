//go:build e2e

package e2e

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// TestRollouts is the docker test of rollouts (I146): a manager and five
// members, m1 to m5, each a fit in a container of its own, reached by
// their Admin APIs' token, labelled into one group with a template of a
// route that proxies to each member's own upstream backend; m3's points
// where nothing listens, so the template's config loads there and answers
// 502. While requests go to every member, a rolling rollout applies in
// batches of two and stops at m3's gate, its share of 5xx, rolling m3 and
// m4 back; a canary waits for its go, and a member edited on itself after
// the plan is skipped; a manager restarted between batches shows the
// rollout paused where it stopped, and resumes it.
//
// It takes FIT_PORT (the manager's console's host port; the members' web
// ports are the next ten past it, 29452 to 29456 by default).
func TestRollouts(t *testing.T) {
	bin := os.Getenv("FIT_BIN")
	if bin == "" {
		t.Skip("FIT_BIN is not set: run test/docker/cluster/e2e.sh")
	}
	members := []string{"m1", "m2", "m3", "m4", "m5"}
	k := &cluster{t: t, dir: t.TempDir(), image: env("FIT_IMAGE", "debian:trixie"), port: env("FIT_PORT", "29441"),
		prefix: "fit-cluster-ro-e2e-", names: append([]string{"manager"}, members...)}
	k.net = k.prefix + "net"
	base, err := strconv.Atoi(k.port)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(k.dir, 0o755)
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(k.dir, "fit"), data, 0o755); err != nil {
		t.Fatal(err)
	}
	cleanup := func() {
		for _, n := range k.names {
			_ = exec.Command("docker", "rm", "-f", k.prefix+n).Run()
		}
		_ = exec.Command("docker", "network", "rm", k.net).Run()
	}
	cleanup()
	t.Cleanup(func() {
		if t.Failed() {
			t.Log(k.logs())
		}
		cleanup()
	})

	caCert, caKey := newCA(t)
	ca := string(pemCert(caCert.Raw))
	for _, name := range members {
		cert, key := leaf(t, name, caCert, caKey)
		k.write(name+".crt", cert)
		k.write(name+".key", key)
	}
	cert, key := leaf(t, "manager", nil, nil)
	k.write("manager.crt", cert)
	k.write("manager.key", key)
	k.write("token", []byte(memberToken+"\n"))
	for _, name := range members {
		backend := "127.0.0.1:8081"
		if name == "m3" {
			backend = "127.0.0.1:9" // nothing listens: the template's route answers 502 there
		}
		k.write(name+".json", []byte(`{"admin": {"address": "0.0.0.0:2019", "token_file": "/w/token", "cert_file": "/w/`+name+`.crt", "key_file": "/w/`+name+`.key"},
"metrics": {},
"listeners": [{"name": "web", "address": "0.0.0.0:8080"}, {"name": "inner", "address": "127.0.0.1:8081"}],
"upstreams": [{"name": "backend", "addresses": ["`+backend+`"]}],
"routes": [{"name": "inner", "listeners": ["inner"], "handler": {"module": "respond", "params": {"body": "backend ok"}}},
           {"name": "own", "listeners": ["web"], "handler": {"module": "respond", "params": {"body": "own"}}}]}`))
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte(managerPass), bcrypt.MinCost)
	k.write("manager.json", []byte(`{"admin": {"store": {"path": "/w/manager.store", "password": "`+storePassword+`"},
"console": {"address": "0.0.0.0:2020", "cert_file": "/w/manager.crt", "key_file": "/w/manager.key", "users": [{"username": "admin", "password_hash": "`+string(hash)+`"}]},
"cluster": {"poll_interval": "2s", "timeout": "5s", "max_concurrency": 4}},
"listeners": [{"name": "web", "address": "0.0.0.0:8080"}], "routes": [{"handler": {"module": "respond", "params": {"body": "ok"}}}]}`))

	k.docker("network", "create", k.net)
	web := map[string]string{}
	for i, n := range members {
		port := strconv.Itoa(base + 11 + i)
		web[n] = "http://127.0.0.1:" + port
		k.run(n, "127.0.0.1:"+port+":8080")
	}
	k.run("manager", "127.0.0.1:"+k.port+":2020")
	jar, _ := cookiejar.New(nil)
	k.c = &http.Client{Jar: jar, Timeout: 30 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} //nolint:gosec
	k.base = "https://127.0.0.1:" + k.port
	k.login()

	for _, n := range members {
		b, _ := json.Marshal(map[string]any{"name": n, "url": "https://" + n + ":2019", "access": "admin_api", "token": memberToken,
			"tls": map[string]string{"ca": ca}, "labels": map[string]string{"role": "edge"}})
		k.mustDo("POST", "/api/m/cluster/nodes", string(b), 201)
	}
	for _, n := range members {
		k.wait(n, "up", up)
	}
	k.mustDo("POST", "/api/m/cluster/groups", `{"name":"edge","selector":"role=edge"}`, 201)
	template := func(priority int) string {
		return `{"name":"api","groups":["edge"],"config":{"routes":[{"name":"api","listeners":["web"],"priority":` + strconv.Itoa(priority) +
			`,"match":{"path":["/api"]},"handler":{"module":"proxy","params":{"upstream":"backend"}}}]}}`
	}
	k.mustDo("POST", "/api/m/cluster/templates", template(0), 201)

	// Requests to every member's /api, all along, which the gates count.
	stop := make(chan struct{})
	var traffic sync.WaitGroup
	traffic.Add(1)
	go func() {
		defer traffic.Done()
		c := &http.Client{Timeout: 2 * time.Second}
		for {
			select {
			case <-stop:
				return
			case <-time.After(40 * time.Millisecond):
			}
			for _, u := range web {
				if resp, err := c.Get(u + "/api"); err == nil {
					_, _ = io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
				}
			}
		}
	}()
	t.Cleanup(func() { close(stop); traffic.Wait() })
	body := func(n string) string {
		resp, err := http.Get(web[n] + "/api")
		if err != nil {
			return err.Error()
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return strconv.Itoa(resp.StatusCode) + " " + string(b)
	}

	// 1. A rolling rollout in batches of two: m3's gate fails on its 5xx,
	// the rollout stops, m3 and m4 are rolled back, m5 is never applied.
	id := k.plan(`{"kind":"template","template":"api","description":"the api route"}`, 5)
	k.mustDo("POST", "/api/m/cluster/rollouts/"+id+"/start",
		`{"strategy":{"mode":"rolling","batch_size":2,"gate":{"wait_ms":2000,"max_5xx_ratio":0.5,"window_ms":5000,"min_requests":5}}}`, 200)
	r := k.waitRollout(id, "ended", func(r roView) bool { return r.ended() })
	t.Logf("rollout 1: %s: %s; %s", r.State, r.Reason, r.statuses())
	if r.State != "failed" || !strings.Contains(r.Reason, "the gate of batch 2 failed: m3") || r.status("m1") != "done" || r.status("m2") != "done" ||
		r.status("m3") != "rolled_back" || r.status("m4") != "rolled_back" || r.status("m5") != "pending" {
		t.Fatalf("rollout 1: %s %s %s", r.State, r.Reason, r.statuses())
	}
	if len(r.Batches) != 3 || r.Batches[0].State != "passed" || r.Batches[1].State != "rolled_back" || r.Batches[2].State != "pending" {
		t.Errorf("batches: %+v", r.Batches)
	}
	for n, want := range map[string]string{"m1": "200 backend ok", "m2": "200 backend ok", "m3": "200 own", "m4": "200 own", "m5": "200 own"} {
		if got := body(n); got != want {
			t.Errorf("%s answers %q, not %q", n, got, want)
		}
	}

	// m3's backend fixed on m3 itself, through the node switcher.
	k.editMember("m3", func(cfg string) string { return strings.Replace(cfg, "127.0.0.1:9", "127.0.0.1:8081", 1) })

	// 2. A canary of one waits for its go; m5, edited on itself after the
	// plan, is skipped, not overwritten.
	k.mustDo("PUT", "/api/m/cluster/templates/api", template(1), 200)
	id = k.plan(`{"kind":"template","template":"api"}`, 5)
	k.editMember("m5", func(cfg string) string { return strings.Replace(cfg, `"body": "own"`, `"body": "own, edited"`, 1) })
	k.mustDo("POST", "/api/m/cluster/rollouts/"+id+"/start", `{"strategy":{"mode":"canary","canary":1,"batch_size":2,"gate":{"wait_ms":1000}}}`, 200)
	r = k.waitRollout(id, "waiting for its go", func(r roView) bool { return r.State == "waiting" || r.ended() })
	if r.State != "waiting" || r.status("m1") != "done" || r.status("m2") != "pending" {
		t.Fatalf("rollout 2: %s %s %s", r.State, r.Reason, r.statuses())
	}
	time.Sleep(3 * time.Second)
	if r = k.rollout(id); r.State != "waiting" {
		t.Fatalf("the canary went on by itself: %s", r.State)
	}
	k.mustDo("POST", "/api/m/cluster/rollouts/"+id+"/go", "", 200)
	r = k.waitRollout(id, "ended", func(r roView) bool { return r.ended() })
	t.Logf("rollout 2: %s: %s; %s", r.State, r.Reason, r.statuses())
	if r.State != "done" || r.status("m5") != "skipped" || r.status("m3") != "done" || r.status("m4") != "done" {
		t.Fatalf("rollout 2: %s %s %s", r.State, r.Reason, r.statuses())
	}
	if got := body("m5"); got != "200 own, edited" {
		t.Errorf("m5 was overwritten: %q", got)
	}
	if got := body("m3"); got != "200 backend ok" {
		t.Errorf("m3: %q", got)
	}

	// 3. The manager restarted between batches: the rollout is paused where
	// it stopped, its members held, and resumed.
	k.mustDo("PUT", "/api/m/cluster/templates/api", template(2), 200)
	id = k.plan(`{"kind":"template","template":"api","nodes":["m1","m2"]}`, 2)
	k.mustDo("POST", "/api/m/cluster/rollouts/"+id+"/start", `{"strategy":{"mode":"rolling","batch_size":1,"pause_ms":600000,"gate":{"wait_ms":1000}}}`, 200)
	k.waitRollout(id, "between its batches", func(r roView) bool { return r.Batch == 1 && r.NextAt != "" })
	k.docker("restart", k.prefix+"manager")
	k.login()
	r = k.waitRollout(id, "paused after the restart", func(r roView) bool { return r.State != "" })
	if r.State != "paused" || r.Batch != 1 || r.status("m1") != "done" || r.status("m2") != "pending" {
		t.Fatalf("after the restart: %s %s %d %s", r.State, r.Reason, r.Batch, r.statuses())
	}
	if code, got := k.do("POST", "/api/m/cluster/ops", `{"op":"reload","members":["m2"]}`); code != 409 {
		t.Errorf("an operation on a member of a paused rollout: %d %s", code, got)
	}
	k.mustDo("POST", "/api/m/cluster/rollouts/"+id+"/resume", "", 200)
	r = k.waitRollout(id, "ended", func(r roView) bool { return r.ended() })
	if r.State != "done" || r.status("m2") != "done" {
		t.Fatalf("resumed: %s %s %s", r.State, r.Reason, r.statuses())
	}

	audit := k.mustDo("GET", "/api/m/cluster/audit", "", 200)
	for _, a := range []string{"rollout.plan", "rollout.start", "rollout.go", "rollout.resume", "rollout.end"} {
		if !strings.Contains(audit, `"action":"`+a+`"`) {
			t.Errorf("the audit log has no %s", a)
		}
	}
	logs := k.logs()
	for _, a := range k.answers {
		if strings.Contains(a, memberToken) {
			t.Errorf("an answer holds the token: %.200s", a)
		}
	}
	if strings.Contains(logs, memberToken) {
		t.Error("a log holds the token")
	}
	t.Logf("the manager's log:\n%s", logs[:strings.Index(logs, "== m1")])
}

// roView is what the test reads of a rollout.
type roView struct {
	ID      string `json:"id"`
	State   string `json:"state"`
	Reason  string `json:"reason"`
	Batch   int    `json:"batch"`
	NextAt  string `json:"next_at"`
	Batches []struct {
		Nodes []string `json:"nodes"`
		State string   `json:"state"`
	} `json:"batches"`
	Members []struct {
		Node   string `json:"node"`
		Status string `json:"status"`
	} `json:"members"`
	Counts map[string]int `json:"counts"`
}

func (r roView) ended() bool {
	switch r.State {
	case "done", "failed", "aborted", "rolled_back", "expired":
		return true
	}
	return false
}

func (r roView) status(n string) string {
	for _, x := range r.Members {
		if x.Node == n {
			return x.Status
		}
	}
	return ""
}

func (r roView) statuses() string {
	var b strings.Builder
	for _, x := range r.Members {
		fmt.Fprintf(&b, "%s=%s ", x.Node, x.Status)
	}
	return b.String()
}

func (k *cluster) rollout(id string) roView {
	k.t.Helper()
	var r roView
	if code, body := k.do("GET", "/api/m/cluster/rollouts/"+id, ""); code == 200 {
		_ = json.Unmarshal([]byte(body), &r)
	}
	return r
}

func (k *cluster) waitRollout(id, what string, cond func(roView) bool) roView {
	k.t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for {
		r := k.rollout(id)
		if cond(r) {
			return r
		}
		if time.Now().After(deadline) {
			k.t.Fatalf("the rollout %s is not %s: %+v\n%s", id, what, r, k.logs())
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// plan plans the rollout req and waits for its want members to be ready.
func (k *cluster) plan(req string, want int) string {
	k.t.Helper()
	var r roView
	_ = json.Unmarshal([]byte(k.mustDo("POST", "/api/m/cluster/rollouts", req, 202)), &r)
	r = k.waitRollout(r.ID, "planned", func(r roView) bool { return r.State != "planning" })
	if r.State != "planned" || r.Counts["ready"] != want {
		k.t.Fatalf("the plan %s: %s %+v %s", req, r.State, r.Counts, k.mustDo("GET", "/api/m/cluster/rollouts/"+r.ID, "", 200))
	}
	return r.ID
}

// editMember changes the member name's config on the member itself, as a
// person would through the node switcher, with If-Match.
func (k *cluster) editMember(name string, edit func(string) string) {
	k.t.Helper()
	path := "/api/n/" + name + "/m/admin/config"
	req, _ := http.NewRequest("GET", k.base+path, nil)
	resp, err := k.c.Do(req)
	if err != nil {
		k.t.Fatal(err)
	}
	cfg, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	etag := resp.Header.Get("ETag")
	next := edit(string(cfg))
	if resp.StatusCode != 200 || etag == "" || next == string(cfg) {
		k.t.Fatalf("%s's config: %d %q %s", name, resp.StatusCode, etag, cfg)
	}
	req, _ = http.NewRequest("PUT", k.base+path, strings.NewReader(next))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", etag)
	req.Header.Set("X-Fit-Csrf", k.csrf)
	resp, err = k.c.Do(req)
	if err != nil {
		k.t.Fatal(err)
	}
	out, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		k.t.Fatalf("editing %s: %d %s", name, resp.StatusCode, out)
	}
	k.t.Logf("%s edited on itself", name)
}
