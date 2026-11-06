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

// TestServeClusterOps runs a manager and a member with a cache zone, and
// runs operations on the member through the manager: a purge by prefix
// takes what the member's zone keeps under it, and no more; a reload applies
// its config again; a rollback, with nothing earlier, fails, and once a
// change was applied takes the member back to the config before it; a check
// and a test read it.
func TestServeClusterOps(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "token"), "member-token-0123456789\n")
	site := func(body string) string {
		return `"listeners": [{"name": "web", "address": "127.0.0.1:0"}], "routes": [{"middleware": [{"module": "cache", "params": {"zone": "ops"}}],
"handler": {"module": "respond", "params": {"body": "` + body + `", "headers": {"Cache-Control": "max-age=600"}}}}]}`
	}
	adminPart := `{"admin": {"address": "127.0.0.1:0", "token_file": ` + js(filepath.Join(dir, "token")) + `}, `
	member := filepath.Join(dir, "member.json")
	writeFile(t, member, adminPart+site("one"))
	ms := startServe(t, "-c", member, "-watch-certs=false")
	out := ms.out.waitFor(t, "fit: admin API on ", 1)
	memberAdmin := "http://" + adminRE.FindStringSubmatch(out)[1]
	web := "http://" + listenerRE.FindStringSubmatch(out)[1]

	mhash, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	manager := filepath.Join(dir, "manager.json")
	writeFile(t, manager, `{"admin": {"address": "127.0.0.1:0", "store": {"path": `+js(filepath.Join(dir, "m.store"))+`, "password": "s"},
"console": {"address": "127.0.0.1:0", "users": [{"username": "admin", "password_hash": `+js(string(mhash))+`}]},
"cluster": {"poll_interval": "1s"}}, "listeners": [], "routes": []}`)
	s := startServe(t, "-c", manager, "-watch-certs=false")
	base := strings.TrimSuffix(consoleRE.FindStringSubmatch(s.out.waitFor(t, "fit: console on http://127.0.0.1:", 1))[1], "/")

	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, Timeout: 30 * time.Second}
	var csrf string
	do := func(method, url, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, url, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Fit-Csrf", csrf)
		req.Header.Set("Authorization", "Bearer member-token-0123456789")
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
	if code, body := do("POST", base+"/api/m/cluster/nodes", `{"name":"m1","url":`+js(memberAdmin)+`,"access":"admin_api","token":"member-token-0123456789"}`); code != 201 {
		t.Fatalf("%d %s", code, body)
	}
	type outcome struct {
		Node, Status, Detail string
		Error                *struct{ Kind, Message string }
		Result               *struct {
			Version, From uint64
			Purged        *int
		}
	}
	run := func(body string) outcome {
		t.Helper()
		code, got := do("POST", base+"/api/m/cluster/ops", body)
		if code != 202 {
			t.Fatalf("%s: %d %s", body, code, got)
		}
		var op struct {
			ID       string
			Finished bool
			Outcomes []outcome
		}
		_ = json.Unmarshal([]byte(got), &op)
		deadline := time.Now().Add(30 * time.Second)
		for !op.Finished {
			if time.Now().After(deadline) {
				t.Fatalf("%s: not finished: %s", body, got)
			}
			time.Sleep(20 * time.Millisecond)
			_, got = do("GET", base+"/api/m/cluster/ops/"+op.ID, "")
			_ = json.Unmarshal([]byte(got), &op)
		}
		if len(op.Outcomes) != 1 {
			t.Fatalf("%s", got)
		}
		return op.Outcomes[0]
	}

	// The site's responses are cached for a request without credentials.
	get := func(path string) (int, string) {
		t.Helper()
		resp, err := http.Get(web + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(data)
	}
	for _, p := range []string{"/img/a", "/img/b", "/other"} {
		if code, _ := get(p); code != 200 {
			t.Fatalf("%s: %d", p, code)
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, got := do("GET", memberAdmin+"/cache", ""); strings.Contains(got, `"zone":"ops","keys":3`) {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("the zone: %s", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if o := run(`{"op":"purge","members":["m1"],"purge":{"zone":"ops","prefixes":[` + js(web+"/img/") + `]}}`); o.Status != "done" || o.Result == nil || o.Result.Purged == nil || *o.Result.Purged != 2 {
		t.Fatalf("purge: %+v", o)
	}
	if _, got := do("GET", memberAdmin+"/cache", ""); !strings.Contains(got, `"zone":"ops","keys":1`) {
		t.Errorf("the zone after the purge: %s", got)
	}
	if o := run(`{"op":"reload","members":["m1"]}`); o.Status != "done" || o.Result.Version != 2 {
		t.Fatalf("reload: %+v", o)
	}
	if o := run(`{"op":"rollback","members":["m1"]}`); o.Status != "failed" || !strings.Contains(o.Error.Message, "no earlier config") {
		t.Fatalf("rollback with nothing earlier: %+v", o)
	}
	if code, got := do("POST", memberAdmin+"/load", `{`+site("two")); code != 200 {
		t.Fatalf("load: %d %s", code, got)
	}
	if o := run(`{"op":"rollback","members":["m1"]}`); o.Status != "done" || o.Result.From != 2 || o.Result.Version != 4 {
		t.Fatalf("rollback: %+v", o)
	}
	if code, got := get("/fresh"); code != 200 || got != "one" {
		t.Errorf("after the rollback: %d %q", code, got)
	}
	if o := run(`{"op":"check","members":["m1"]}`); o.Status != "done" {
		t.Errorf("check: %+v", o)
	}
	if o := run(`{"op":"test","members":["m1"]}`); o.Status != "done" || !strings.Contains(o.Detail, "reached in") {
		t.Errorf("test: %+v", o)
	}
	if code := s.stop(t); code != 0 {
		t.Fatalf("stop: %d %q", code, s.err.String())
	}
	if code := ms.stop(t); code != 0 {
		t.Fatalf("stop: %d %q", code, ms.err.String())
	}
}
