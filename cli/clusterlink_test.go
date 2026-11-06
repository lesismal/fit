package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/internal/pebbletest"
)

// TestServeClusterLink runs a manager that takes links, and a member with
// no port the manager could reach, which enrolls with the token the
// manager's console made and links out: the manager polls it through the
// link; it links again after the manager restarts; removed on the manager,
// it loses its credential.
func TestServeClusterLink(t *testing.T) {
	dir := t.TempDir()
	const rest = `"listeners": [{"name": "web", "address": "127.0.0.1:0"}], "routes": [{"handler": {"module": "respond"}}]}`
	mhash, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	linkAddr := "127.0.0.1:" + strconv.Itoa(pebbletest.FreePort(t))
	manager := filepath.Join(dir, "manager.json")
	writeFile(t, manager, `{"admin": {"address": "127.0.0.1:0", "store": {"path": `+js(filepath.Join(dir, "m.store"))+`, "password": "s"},
"console": {"address": "127.0.0.1:0", "users": [{"username": "admin", "password_hash": `+js(string(mhash))+`}]},
"cluster": {"poll_interval": "1s", "link_address": `+js(linkAddr)+`}}, `+rest)
	s := startServe(t, "-c", manager, "-watch-certs=false")
	out := s.out.waitFor(t, "fit: console on http://127.0.0.1:", 1)
	if !strings.Contains(out, "fit: members link to "+linkAddr+" (certificate ") {
		t.Fatalf("%s", out)
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
	login := func() {
		t.Helper()
		code, body := do("POST", "/api/login", `{"username":"admin","password":"pw"}`)
		if code != 200 {
			t.Fatalf("%d %s", code, body)
		}
		var sess struct{ CSRF string }
		_ = json.Unmarshal([]byte(body), &sess)
		csrf = sess.CSRF
	}
	login()
	code, body := do("GET", "/api/m/cluster/link", "")
	if code != 200 || !strings.Contains(body, `"enabled":true`) {
		t.Fatalf("%d %s", code, body)
	}
	code, body = do("POST", "/api/m/cluster/enroll", `{"name":"nat-1","labels":{"site":"home"},"ttl":"1h"}`)
	if code != 201 {
		t.Fatalf("%d %s", code, body)
	}
	var e struct {
		Token      string
		ConfigLine string `json:"config_line"`
	}
	_ = json.Unmarshal([]byte(body), &e)
	if !strings.HasPrefix(e.ConfigLine, `"cluster_member": {`) {
		t.Fatalf("%s", body)
	}

	// The member: an Admin API on loopback only, no console, and the line
	// the manager gave.
	member := filepath.Join(dir, "member.json")
	writeFile(t, member, `{"admin": {"address": "127.0.0.1:0", `+e.ConfigLine+`}, `+rest)
	ms := startServe(t, "-c", member, "-watch-certs=false")
	ms.out.waitFor(t, "fit: linking to the manager at wss://", 1)

	waitState := func(what string, ok func(status string, linked bool) bool) string {
		t.Helper()
		var state struct {
			Nodes []struct {
				Name, Status string
				Link         *struct{ Linked bool }
			}
		}
		deadline := time.Now().Add(30 * time.Second)
		for {
			_, body := do("GET", "/api/m/cluster/state", "")
			_ = json.Unmarshal([]byte(body), &state)
			if len(state.Nodes) == 1 && state.Nodes[0].Link != nil && ok(state.Nodes[0].Status, state.Nodes[0].Link.Linked) {
				return body
			}
			if time.Now().After(deadline) {
				t.Fatalf("the member is not %s: %s", what, body)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	body = waitState("up", func(st string, linked bool) bool { return st == "up" && linked })
	if !strings.Contains(body, `"fit_version"`) || strings.Contains(body, e.Token) {
		t.Fatalf("%s", body)
	}
	kept := filepath.Join(dir, "member.cluster-member")
	fi, err := os.Stat(kept)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("the credential's file is %v", fi.Mode())
	}
	data, _ := os.ReadFile(kept)
	if strings.Contains(s.out.String()+s.err.String()+ms.out.String()+ms.err.String(), e.Token) || strings.Contains(s.out.String()+ms.out.String(), credOf(data)) {
		t.Error("a token or credential logged")
	}

	// The manager restarts: the member links again, with its credential.
	if code := s.stop(t); code != 0 {
		t.Fatalf("stop: %d %q", code, s.err.String())
	}
	s = startServe(t, "-c", manager, "-watch-certs=false")
	out = s.out.waitFor(t, "fit: console on http://127.0.0.1:", 1)
	base = strings.TrimSuffix(consoleRE.FindStringSubmatch(out)[1], "/")
	jar, _ = cookiejar.New(nil)
	c.Jar = jar
	login()
	waitState("up again", func(st string, linked bool) bool { return st == "up" && linked })

	// Removed on the manager, the member loses its credential.
	if code, body := do("DELETE", "/api/m/cluster/nodes/nat-1", ""); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	ms.out.waitUntil(t, "the credential revoked", func(o string) bool { return strings.Contains(plain(o), "revoked this fit's credential") })
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(kept); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the credential is still kept")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if code := ms.stop(t); code != 0 {
		t.Fatalf("stop: %d %q", code, ms.err.String())
	}
	if code := s.stop(t); code != 0 {
		t.Fatalf("stop: %d %q", code, s.err.String())
	}
}

// credOf is the secret of the credential a member keeps, or a string no
// output holds.
func credOf(data []byte) string {
	var k struct{ Credential string }
	if json.Unmarshal(data, &k) != nil || len(k.Credential) < 30 {
		return "\x00none"
	}
	return k.Credential[len(k.Credential)-20:]
}
