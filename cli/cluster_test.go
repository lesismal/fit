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

// TestServeCluster runs a manager and a member: the member is added through
// the manager's console by its console's password and by its Admin API's
// token, and both show it up; no credential is answered.
func TestServeCluster(t *testing.T) {
	dir := t.TempDir()
	hash, _ := bcrypt.GenerateFromPassword([]byte("member-pw"), bcrypt.MinCost)
	writeFile(t, filepath.Join(dir, "token"), "member-token-0123456789\n")
	const rest = `"listeners": [{"name": "web", "address": "127.0.0.1:0"}], "routes": [{"handler": {"module": "respond"}}]}`
	member := filepath.Join(dir, "member.json")
	writeFile(t, member, `{"admin": {"address": "127.0.0.1:0", "token_file": `+js(filepath.Join(dir, "token"))+`,
"console": {"address": "127.0.0.1:0", "users": [{"username": "ops", "password_hash": `+js(string(hash))+`}]}}, `+rest)
	ms := startServe(t, "-c", member, "-watch-certs=false")
	out := ms.out.waitFor(t, "fit: console on http://127.0.0.1:", 1)
	memberConsole := strings.TrimSuffix(consoleRE.FindStringSubmatch(out)[1], "/")
	memberAdmin := "http://" + adminRE.FindStringSubmatch(out)[1]

	mhash, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	manager := filepath.Join(dir, "manager.json")
	writeFile(t, manager, `{"admin": {"address": "127.0.0.1:0", "store": {"path": `+js(filepath.Join(dir, "m.store"))+`, "password": "s"},
"console": {"address": "127.0.0.1:0", "users": [{"username": "admin", "password_hash": `+js(string(mhash))+`}]},
"cluster": {"poll_interval": "1s"}}, `+rest)
	s := startServe(t, "-c", manager, "-watch-certs=false")
	out = s.out.waitFor(t, "fit: console on http://127.0.0.1:", 1)
	if !strings.Contains(out, "fit: managing 0 members (admin.cluster)") {
		t.Errorf("%s", out)
	}
	base := strings.TrimSuffix(consoleRE.FindStringSubmatch(out)[1], "/")

	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, Timeout: 30 * time.Second}
	var csrf string
	do := func(method, path, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Fit-Csrf", csrf)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(data)
	}
	code, body := do("POST", "/api/login", `{"username":"admin","password":"pw"}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var sess struct{ CSRF string }
	_ = json.Unmarshal([]byte(body), &sess)
	csrf = sess.CSRF
	if code, body := do("GET", "/api/workbench", ""); !strings.Contains(body, `"cluster"`) {
		t.Fatalf("%d %s", code, body)
	}
	if code, body := do("POST", "/api/m/cluster/nodes", `{"name":"by-password","url":`+js(memberConsole)+`,"access":"console","username":"ops","password":"member-pw"}`); code != 201 {
		t.Fatalf("%d %s", code, body)
	}
	if code, body := do("POST", "/api/m/cluster/nodes", `{"name":"by-token","url":`+js(memberAdmin)+`,"access":"admin_api","token":"member-token-0123456789"}`); code != 201 {
		t.Fatalf("%d %s", code, body)
	}
	var state struct {
		Nodes []struct {
			Name, Status string
			Node         *struct{ ID string }
		}
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		_, body = do("GET", "/api/m/cluster/state", "")
		_ = json.Unmarshal([]byte(body), &state)
		if len(state.Nodes) == 2 && state.Nodes[0].Status == "up" && state.Nodes[1].Status == "up" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the members are not up: %s", body)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if state.Nodes[0].Node.ID == "" || state.Nodes[0].Node.ID != state.Nodes[1].Node.ID {
		t.Errorf("one member, two IDs: %s", body)
	}
	for _, secret := range []string{"member-pw", "member-token"} {
		if strings.Contains(body, secret) || strings.Contains(s.out.String(), secret) || strings.Contains(s.err.String(), secret) {
			t.Errorf("%q answered or logged", secret)
		}
	}
	if code := s.stop(t); code != 0 {
		t.Fatalf("stop: %d %q", code, s.err.String())
	}
	if code := ms.stop(t); code != 0 {
		t.Fatalf("stop: %d %q", code, ms.err.String())
	}
}
