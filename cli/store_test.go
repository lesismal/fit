package cli

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lesismal/fit/admin/store"
)

func init() {
	// Cheap enough for tests; the store's header records them.
	store.DefaultParams = store.Params{Time: 1, MemoryKiB: 64, Threads: 1}
}

// TestServeStore runs fit from a store: seeded from the bootstrap file,
// written on each change from the Admin API, read again on SIGHUP and after
// a restart, with admin never in it.
func TestServeStore(t *testing.T) {
	dir := t.TempDir()
	cfg, path := filepath.Join(dir, "fit.json"), filepath.Join(dir, "fit.store")
	bootAdmin := `"admin": {"address": "127.0.0.1:0", "store": {"path": ` + js(path) + `, "password": "s3cret"}}`
	writeFile(t, cfg, `{`+bootAdmin+`,
"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
"routes": [{"handler": {"module": "respond", "params": {"body": "one"}}}]}`)
	s := startServe(t, "-c", cfg, "-watch-certs=false")
	out := s.out.waitFor(t, "fit: admin API on ", 1)
	if !strings.Contains(out, "is kept in the store "+path) {
		t.Fatalf("seeded: %q %q", out, s.err.String())
	}
	adminAddr := adminRE.FindStringSubmatch(out)[1]
	data := exportStore(t, cfg)
	if !strings.Contains(data, `"one"`) || strings.Contains(data, "admin") || strings.Contains(data, "s3cret") {
		t.Fatalf("the seed: %s", data)
	}

	// GET /config shows no admin; a config with admin is refused; one
	// without is applied and saved.
	if got := get(t, "http://"+adminAddr+"/config"); strings.Contains(got, "admin") {
		t.Errorf("GET /config: %s", got)
	}
	two := `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}], "routes": [{"handler": {"module": "respond", "params": {"body": "two"}}}]}`
	if code, body := post(t, "http://"+adminAddr+"/load", `{`+bootAdmin+`,`+two[1:]); code != http.StatusBadRequest || !strings.Contains(body, "admin is set in the config file fit started with") {
		t.Errorf("a config with admin: %d %s", code, body)
	}
	if code, body := post(t, "http://"+adminAddr+"/load", two); code != http.StatusOK {
		t.Fatalf("load: %d %s", code, body)
	}
	if data := exportStore(t, cfg); data != two {
		t.Errorf("saved: %s", data)
	}

	// SIGHUP reads the store again, as written by fit store import.
	three := filepath.Join(dir, "three.json")
	writeFile(t, three, strings.Replace(two, "two", "three", 1))
	if code := run([]string{"store", "import", "-c", cfg, three}, io.Discard, os.Stderr); code != 0 {
		t.Fatalf("import: %d", code)
	}
	s.reload <- struct{}{}
	s.out.waitFor(t, "fit: reloaded the store "+path, 1)
	if got := get(t, "http://"+adminAddr+"/config"); !strings.Contains(got, `"three"`) {
		t.Errorf("after SIGHUP: %s", got)
	}
	if code := s.stop(t); code != 0 {
		t.Fatalf("stop: %d %q", code, s.err.String())
	}

	// After a restart, the store wins over the bootstrap file's routes.
	s = startServe(t, "-c", cfg, "-watch-certs=false")
	out = s.out.waitFor(t, "fit: admin API on ", 1)
	if !strings.Contains(s.err.String(), "sets listeners, routes beside admin; left out") {
		t.Errorf("no warning of what the store wins over: %q", s.err.String())
	}
	if got := get(t, "http://"+adminRE.FindStringSubmatch(out)[1]+"/config"); !strings.Contains(got, `"three"`) {
		t.Errorf("after a restart: %s", got)
	}
	if code := s.stop(t); code != 0 {
		t.Fatal(code)
	}

	// A wrong password stops fit from starting; -watch is refused.
	writeFile(t, cfg, `{"admin": {"store": {"path": `+js(path)+`, "password": "wrong"}}}`)
	if s := startServe(t, "-c", cfg); s.stop(t) != 1 || !strings.Contains(s.err.String(), "does not open with the password") {
		t.Errorf("a wrong password: %q", s.err.String())
	}
	writeFile(t, cfg, `{`+bootAdmin+`}`)
	if s := startServe(t, "-c", cfg, "-watch"); s.stop(t) != 1 || !strings.Contains(s.err.String(), "-watch watches config files") {
		t.Errorf("-watch: %q", s.err.String())
	}
}

func TestStoreCommand(t *testing.T) {
	dir := t.TempDir()
	cfg, path := filepath.Join(dir, "fit.json"), filepath.Join(dir, "fit.store")
	writeFile(t, cfg, `{"admin": {"store": {"path": `+js(path)+`, "password_env": "FIT_TEST_STORE"}}}`)
	t.Setenv("FIT_TEST_STORE", "pw")
	var stderr bytes.Buffer
	if code := run([]string{"store", "export", "-c", cfg}, io.Discard, &stderr); code != 1 || !strings.Contains(stderr.String(), "there is no store") {
		t.Errorf("export of no store: %d %q", code, stderr.String())
	}
	bad := filepath.Join(dir, "bad.json")
	for doc, want := range map[string]string{
		`{"listeners": []}`: "at least one listener",
		`{"admin": {}, "listeners": [{"name": "web", "address": ":0"}], "routes": [{"handler": {"module": "respond"}}]}`: "admin stays in the bootstrap file",
		`{"listeners": [{"name": "web", "address": ":0"}], "routes": [{"handler": {"module": "nope"}}]}`:                 "nope",
	} {
		writeFile(t, bad, doc)
		stderr.Reset()
		if code := run([]string{"store", "import", "-c", cfg, bad}, io.Discard, &stderr); code != 1 || !strings.Contains(stderr.String(), want) {
			t.Errorf("import %s: %d %q", doc, code, stderr.String())
		}
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("a refused import wrote the store")
	}

	// The password changes: export, change it, import.
	good := filepath.Join(dir, "good.json")
	writeFile(t, good, `{"listeners": [{"name": "web", "address": ":0"}], "routes": [{"handler": {"module": "respond"}}]}`)
	if code := run([]string{"store", "import", "-c", cfg, good}, io.Discard, os.Stderr); code != 0 {
		t.Fatal(code)
	}
	before := exportStore(t, cfg)
	t.Setenv("FIT_TEST_STORE", "new")
	stderr.Reset()
	if code := run([]string{"store", "export", "-c", cfg}, io.Discard, &stderr); code != 1 || !strings.Contains(stderr.String(), "does not open") {
		t.Errorf("export with a new password: %d %q", code, stderr.String())
	}
	writeFile(t, good, before)
	if code := run([]string{"store", "import", "-c", cfg, good}, io.Discard, os.Stderr); code != 0 {
		t.Fatal(code)
	}
	if after := exportStore(t, cfg); after != before {
		t.Errorf("after a new password: %s", after)
	}
	if code := run([]string{"store"}, io.Discard, io.Discard); code != 2 {
		t.Errorf("no subcommand: %d", code)
	}
}

func exportStore(t *testing.T, cfg string) string {
	t.Helper()
	var out, stderr bytes.Buffer
	if code := run([]string{"store", "export", "-c", cfg}, &out, &stderr); code != 0 {
		t.Fatalf("export: %d %q", code, stderr.String())
	}
	return out.String()
}

func get(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}

func post(t *testing.T, url, body string) (int, string) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(got)
}
