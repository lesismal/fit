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

// TestServeClusterSwitcher opens a member's own pages through the manager
// (I143), as the node switcher does: by the member's console and by its
// Admin API's token, its config is read and saved with If-Match, a stale
// ETag is refused as the member refuses it, its schema is the member's, and
// each save is in the manager's audit log with its user; nothing of the
// member's session reaches the browser.
func TestServeClusterSwitcher(t *testing.T) {
	dir := t.TempDir()
	hash, _ := bcrypt.GenerateFromPassword([]byte("member-pw"), bcrypt.MinCost)
	writeFile(t, filepath.Join(dir, "token"), "member-token-0123456789\n")
	const rest = `"listeners": [{"name": "web", "address": "127.0.0.1:0"}], "routes": [{"name": "r", "handler": {"module": "respond"}}]}`
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
	base := strings.TrimSuffix(consoleRE.FindStringSubmatch(out)[1], "/")

	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, Timeout: 30 * time.Second}
	var csrf string
	do := func(method, path, body string, header ...string) (int, string, http.Header) {
		t.Helper()
		req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
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
	code, body, _ := do("POST", "/api/login", `{"username":"admin","password":"pw"}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var sess struct{ CSRF string }
	_ = json.Unmarshal([]byte(body), &sess)
	csrf = sess.CSRF
	if code, body, _ := do("POST", "/api/m/cluster/nodes", `{"name":"by-password","url":`+js(memberConsole)+`,"access":"console","username":"ops","password":"member-pw"}`); code != 201 {
		t.Fatalf("%d %s", code, body)
	}
	if code, body, _ := do("POST", "/api/m/cluster/nodes", `{"name":"by-token","url":`+js(memberAdmin)+`,"access":"admin_api","token":"member-token-0123456789"}`); code != 201 {
		t.Fatalf("%d %s", code, body)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		_, body, _ = do("GET", "/api/m/cluster/state", "")
		if strings.Count(body, `"status":"up"`) == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the members are not up: %s", body)
		}
		time.Sleep(50 * time.Millisecond)
	}

	for i, name := range []string{"by-password", "by-token"} {
		nb := "/api/n/" + name
		code, body, _ := do("GET", nb+"/workbench", "")
		var wb struct {
			Modules []string
			Member  struct {
				Name     string
				ReadOnly bool `json:"read_only"`
			}
		}
		if code != 200 || json.Unmarshal([]byte(body), &wb) != nil || wb.Member.Name != name || wb.Member.ReadOnly || len(wb.Modules) == 0 || wb.Modules[0] != "admin" {
			t.Fatalf("%s: %d %s", name, code, body)
		}
		code, body, h := do("GET", nb+"/m/admin/schema", "")
		if code != 200 || !strings.Contains(body, `"$defs"`) || h.Get("Fit-Schema-From") != "" {
			t.Fatalf("%s: %d %.100s %v", name, code, body, h)
		}
		code, body, h = do("GET", nb+"/m/admin/config", "")
		etag := h.Get("ETag")
		if code != 200 || etag == "" || h.Get("Fit-Config-Version") == "" || h.Get("Set-Cookie") != "" {
			t.Fatalf("%s: %d %s %v", name, code, body, h)
		}
		var cfg map[string]any
		_ = json.Unmarshal([]byte(body), &cfg)
		cfg["routes"] = []any{map[string]any{"name": "r", "handler": map[string]any{"module": "respond", "params": map[string]any{"body": name}}}}
		data, _ := json.Marshal(cfg)
		if code, body, _ := do("PUT", nb+"/m/admin/config?dry_run=1", string(data), "If-Match", etag); code != 200 || !strings.Contains(body, `"dry_run":true`) {
			t.Fatalf("%s: %d %s", name, code, body)
		}
		code, body, h = do("PUT", nb+"/m/admin/config", string(data), "If-Match", etag)
		if code != 200 || h.Get("Fit-Config-Version") != []string{"2", "3"}[i] {
			t.Fatalf("%s: %d %s %v", name, code, body, h)
		}
		if code, body, _ := do("PUT", nb+"/m/admin/config", string(data), "If-Match", etag); code != 412 {
			t.Fatalf("%s: a stale ETag: %d %s", name, code, body)
		}
		// The member runs what was saved.
		if code, body, _ := do("GET", nb+"/m/admin/config/routes/0/handler/params/body", ""); code != 200 || strings.TrimSpace(body) != js(name) {
			t.Errorf("%s: %d %s", name, code, body)
		}
	}

	// The audit log is written beside the answers: wait for it.
	var got []string
	for deadline := time.Now().Add(30 * time.Second); len(got) < 4 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		_, body, _ = do("GET", "/api/m/cluster/audit", "")
		var audit struct {
			Entries []struct{ User, Action, Node, Detail string }
		}
		_ = json.Unmarshal([]byte(body), &audit)
		got = got[:0]
		for _, e := range audit.Entries {
			if e.Action == "node.api" {
				got = append(got, e.User+" "+e.Node+" "+e.Detail)
			}
		}
	}
	want := []string{"admin by-password PUT /config: 200, config version 2", "admin by-password PUT /config: 412",
		"admin by-token PUT /config: 200, config version 3", "admin by-token PUT /config: 412"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("audit:\n%s", strings.Join(got, "\n"))
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
