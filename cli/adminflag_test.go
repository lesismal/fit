package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

var consoleRE = regexp.MustCompile(`console on (http\S+)`)

// consoleClient is a browser on the console's pages.
type consoleClient struct {
	t    *testing.T
	base string
	c    *http.Client
	csrf string
}

func newConsoleClient(t *testing.T, base string) *consoleClient {
	jar, _ := cookiejar.New(nil)
	return &consoleClient{t: t, base: strings.TrimSuffix(base, "/"), c: &http.Client{Jar: jar, Timeout: 10 * time.Second}}
}

func (b *consoleClient) do(method, path, body string) (int, string) {
	b.t.Helper()
	req, _ := http.NewRequest(method, b.base+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if b.csrf != "" {
		req.Header.Set("X-Fit-Csrf", b.csrf)
	}
	resp, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data)
}

func (b *consoleClient) login(user, password string) (int, string) {
	b.t.Helper()
	code, body := b.do("POST", "/api/login", `{"username": `+js(user)+`, "password": `+js(password)+`}`)
	var s struct{ CSRF string }
	_ = json.Unmarshal([]byte(body), &s)
	b.csrf = s.CSRF
	return code, body
}

// TestServeAdminFlag runs fit run -admin: the console with the temporary
// password admin, the store beside the config with its key file, the store
// merged onto the config file at a restart, and -c with the .store file.
func TestServeAdminFlag(t *testing.T) {
	t.Setenv(StorePasswordEnv, "")
	dir := t.TempDir()
	cfg := filepath.Join(dir, "site.json")
	path, keyPath := filepath.Join(dir, "site.store"), filepath.Join(dir, "site.store.key")
	addrs := `"admin": {"address": "127.0.0.1:0", "console": {"address": "127.0.0.1:0"}}`
	writeFile(t, cfg, `{`+addrs+`,
"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
"routes": [{"handler": {"module": "respond", "params": {"body": "file"}}}]}`)

	if s := startServe(t, "-c", cfg, "-timeout", "1m"); s.stop(t) != 2 || !strings.Contains(s.err.String(), "needs -admin") {
		t.Errorf("-timeout without -admin: %q", s.err.String())
	}
	if s := startServe(t, "-admin", "-c", cfg, "-timeout", "0s"); s.stop(t) != 2 || s.err.String() != "fit run: -timeout must be more than 0\n" {
		t.Errorf("-timeout 0: %q", s.err.String())
	}

	s := startServe(t, "-admin", "-c", cfg, "-watch-certs=false")
	out := s.out.waitFor(t, "console on ", 1)
	if !strings.Contains(s.err.String(), `the console user "admin" has the password "admin"`) {
		t.Errorf("no word of the temporary password: %q", s.err.String())
	}
	if fi, err := os.Stat(keyPath); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the key file: %v %v", fi, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the store: %v", err)
	}
	adminAddr := adminRE.FindStringSubmatch(out)[1]
	if code, body := post(t, "http://"+adminAddr+"/load", `{}`); code != http.StatusForbidden || !strings.Contains(body, "temporary password") {
		t.Errorf("the Admin API before the change: %d %s", code, body)
	}
	b := newConsoleClient(t, consoleRE.FindStringSubmatch(out)[1])
	if code, body := b.login("admin", "admin"); code != http.StatusOK || !strings.Contains(body, `"must_change_password":true`) {
		t.Fatalf("login: %d %s", code, body)
	}
	if code, _ := b.do("GET", "/api/m/admin/config", ""); code != http.StatusForbidden {
		t.Errorf("the config before the change: %d", code)
	}
	if code, body := b.do("POST", "/api/m/admin/account/password", `{"current_password": "admin", "password": "n3w"}`); code != http.StatusOK {
		t.Fatalf("the change: %d %s", code, body)
	}
	if code, body := b.do("GET", "/api/m/admin/config", ""); code != http.StatusOK || !strings.Contains(body, `"file"`) {
		t.Errorf("the config after the change: %d %s", code, body)
	}
	if got := get(t, "http://"+adminAddr+"/config"); !strings.Contains(got, `"file"`) {
		t.Errorf("the Admin API after the change: %s", got)
	}
	// A change from the console is saved to the store.
	two := `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}], "routes": [{"handler": {"module": "respond", "params": {"body": "console"}}}]}`
	if code, body := b.do("PUT", "/api/m/admin/config", two); code != http.StatusOK {
		t.Fatalf("the console's change: %d %s", code, body)
	}
	if got := exportStore(t, path); !strings.Contains(got, `"console"`) {
		t.Errorf("the store after the change: %s", got)
	}
	if code := s.stop(t); code != 0 {
		t.Fatalf("stop: %d %q", code, s.err.String())
	}

	// The file gains a member the store lacks; at a restart the store is
	// merged onto it: its routes win, the file's limits stay. The changed
	// password is kept, so none is temporary.
	writeFile(t, cfg, `{`+addrs+`,
"limits": {"max_conns": 1234},
"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
"routes": [{"handler": {"module": "respond", "params": {"body": "file"}}}]}`)
	s = startServe(t, "-admin", "-c", cfg, "-watch-certs=false")
	out = s.out.waitFor(t, "console on ", 1)
	if strings.Contains(s.err.String(), "temporary") || strings.Contains(s.err.String(), `has the password "admin"`) {
		t.Errorf("a temporary password after the change: %q", s.err.String())
	}
	// The merge is reported once the start has succeeded, after the
	// console's line.
	s.out.waitFor(t, "merged onto", 1)
	if got := get(t, "http://"+listenerRE.FindStringSubmatch(out)[1]+"/"); got != "console" {
		t.Errorf("served after the restart: %q", got)
	}
	if got := exportStore(t, path); !strings.Contains(got, "1234") || !strings.Contains(got, `"console"`) {
		t.Errorf("the store after the merge: %s", got)
	}
	b = newConsoleClient(t, consoleRE.FindStringSubmatch(out)[1])
	if code, _ := b.login("admin", "admin"); code != http.StatusUnauthorized {
		t.Errorf("the old password: %d", code)
	}
	if code, body := b.login("admin", "n3w"); code != http.StatusOK || strings.Contains(body, "must_change") {
		t.Errorf("the new password: %d %s", code, body)
	}
	if code := s.stop(t); code != 0 {
		t.Fatalf("stop: %d %q", code, s.err.String())
	}

	// -c with the .store alone: the config is the store's, opened with the
	// key file beside it.
	s = startServe(t, "-c", path, "-watch-certs=false")
	out = s.out.waitFor(t, "fit: listener ", 1)
	if got := get(t, "http://"+listenerRE.FindStringSubmatch(out)[1]+"/"); got != "console" {
		t.Errorf("served from the .store: %q", got)
	}
	if code := s.stop(t); code != 0 {
		t.Fatalf("stop: %d %q", code, s.err.String())
	}
	// Elsewhere, without the key file, $FIT_STORE_PASSWORD opens it.
	moved := filepath.Join(t.TempDir(), "moved.store")
	data, _ := os.ReadFile(path)
	key, _ := os.ReadFile(keyPath)
	writeFile(t, moved, string(data))
	var stderr bytes.Buffer
	if code := run([]string{"store", "export", "-c", moved}, io.Discard, &stderr); code != 1 || !strings.Contains(stderr.String(), "no key file") {
		t.Errorf("no key: %d %q", code, stderr.String())
	}
	t.Setenv(StorePasswordEnv, strings.TrimSpace(string(key)))
	if got := exportStore(t, moved); !strings.Contains(got, `"console"`) {
		t.Errorf("with $%s: %s", StorePasswordEnv, got)
	}
	if code, _ := run2([]string{"run", "-c", path, "-c", moved}); code != 2 && code != 1 {
		t.Errorf("two stores: %d", code)
	}
}

func run2(args []string) (int, string) {
	var stderr bytes.Buffer
	code := run(args, io.Discard, &stderr)
	return code, stderr.String()
}
