package cli

import (
	"bytes"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/admin/store"
	"github.com/lesismal/fit/config"
)

// TestAdaptToFitRefused checks that fit adapt fails on a config it cannot
// write as a .conf file: a list of one value in the params of a module this
// fit does not know, which would read back as the value alone.
func TestAdaptToFitRefused(t *testing.T) {
	f := filepath.Join(t.TempDir(), "fit.json")
	writeFile(t, f, `{"listeners": [{"name": "web", "address": ":8080"}],
  "routes": [{"handler": {"module": "elsewhere", "params": {"x": [1]}}}]}`)
	code, stdout, stderr := runFit("adapt", "-c", f, "-to", "fit")
	if code != 1 || stdout != "" || !strings.Contains(stderr, "fit adapt: ") || !strings.Contains(stderr, "fewer than two values") {
		t.Errorf("%d %q %q", code, stdout, stderr)
	}
}

func TestStorePassword(t *testing.T) {
	dir := t.TempDir()
	empty, good := filepath.Join(dir, "empty"), filepath.Join(dir, "good")
	writeFile(t, empty, " \n")
	writeFile(t, good, "pw\n")
	t.Setenv("FIT_TEST_STORE_SET", "env")
	t.Setenv("FIT_TEST_STORE_EMPTY", "")
	for _, tt := range []struct {
		name string
		s    config.Store
		want string // the password, or a part of the error
		ok   bool
	}{
		{"password", config.Store{Password: "p"}, "p", true},
		{"file", config.Store{PasswordFile: good}, "pw", true},
		{"missing file", config.Store{PasswordFile: filepath.Join(dir, "nope")}, "admin.store.password_file: ", false},
		{"empty file", config.Store{PasswordFile: empty}, "is empty", false},
		{"env", config.Store{PasswordEnv: "FIT_TEST_STORE_SET"}, "env", true},
		{"empty env", config.Store{PasswordEnv: "FIT_TEST_STORE_EMPTY"}, "$FIT_TEST_STORE_EMPTY is empty or not set", false},
		{"none", config.Store{}, "set exactly one", false},
		{"two", config.Store{Password: "p", PasswordEnv: "FIT_TEST_STORE_SET"}, "set exactly one", false},
	} {
		got, err := storePassword(&tt.s)
		if tt.ok && (err != nil || got != tt.want) || !tt.ok && (err == nil || !strings.Contains(err.Error(), tt.want)) {
			t.Errorf("%s: %q %v, want %q", tt.name, got, err, tt.want)
		}
	}
}

// TestStoreCommandErrors runs fit store with what it refuses.
func TestStoreCommandErrors(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "fit.json")
	good := filepath.Join(dir, "good.json")
	writeFile(t, good, `{"listeners": [{"name": "web", "address": ":0"}], "routes": [{"handler": {"module": "respond"}}]}`)
	storeRun := func(args ...string) (int, string) {
		var stderr bytes.Buffer
		code := run(append([]string{"store"}, args...), io.Discard, &stderr)
		return code, stderr.String()
	}
	if code, _ := storeRun("export", "-bogus"); code != 2 {
		t.Errorf("a bad flag: %d", code)
	}
	if code, _ := storeRun("export", "-c", cfg, "extra"); code != 2 {
		t.Errorf("export with an argument: %d", code)
	}
	if code, stderr := storeRun("export", "-c", filepath.Join(dir, "missing.json")); code != 1 || !strings.Contains(stderr, "fit store export") {
		t.Errorf("a missing bootstrap file: %d %q", code, stderr)
	}

	writeFile(t, cfg, `{"admin": {"address": "127.0.0.1:0"}}`)
	if code, stderr := storeRun("export", "-c", cfg); code != 1 || !strings.Contains(stderr, "sets no admin.store") {
		t.Errorf("no admin.store: %d %q", code, stderr)
	}

	path := filepath.Join(dir, "fit.store")
	writeFile(t, cfg, `{"admin": {"store": {"path": `+js(path)+`, "password": "a", "password_env": "FIT_TEST_STORE"}}}`)
	if code, stderr := storeRun("export", "-c", cfg); code != 1 || !strings.Contains(stderr, "set exactly one") {
		t.Errorf("export with two passwords: %d %q", code, stderr)
	}
	if code, stderr := storeRun("import", "-c", cfg, good); code != 1 || !strings.Contains(stderr, "set exactly one") {
		t.Errorf("import with two passwords: %d %q", code, stderr)
	}

	// A store whose directory is not there opens, as one not written yet,
	// and cannot be written.
	writeFile(t, cfg, `{"admin": {"store": {"path": `+js(filepath.Join(dir, "missing", "fit.store"))+`, "password": "a"}}}`)
	if code, stderr := storeRun("import", "-c", cfg, good); code != 1 || !strings.Contains(stderr, "fit store import: store: ") {
		t.Errorf("import into a missing directory: %d %q", code, stderr)
	}
}

// writeStore seals data into a store at path.
func writeStore(t *testing.T, path, password, data string) {
	t.Helper()
	st, err := store.Create(path, password)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Write([]byte(data)); err != nil {
		t.Fatal(err)
	}
}

// TestServeStoreErrors runs fit from stores it cannot use.
func TestServeStoreErrors(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "fit.json")
	const rest = `"listeners": [{"name": "web", "address": "127.0.0.1:0"}], "routes": [{"handler": {"module": "respond"}}]`

	// The bootstrap file's config cannot be seeded where the store's
	// directory is missing.
	writeFile(t, cfg, `{"admin": {"store": {"path": `+js(filepath.Join(dir, "missing", "fit.store"))+`, "password": "a"}}, `+rest+`}`)
	if code, _, stderr := runFit("run", "-c", cfg, "-watch-certs=false"); code != 1 || !strings.Contains(stderr, "fit: store: ") {
		t.Errorf("a store that cannot be seeded: %d %q", code, stderr)
	}

	// A store whose config does not compile is reported at the store.
	path := filepath.Join(dir, "fit.store")
	writeStore(t, path, "a", `{`+strings.Replace(rest, "respond", "nope", 1)+`}`)
	writeFile(t, cfg, `{"admin": {"store": {"path": `+js(path)+`, "password": "a"}}}`)
	if code, _, stderr := runFit("run", "-c", cfg, "-watch-certs=false"); code != 1 || !strings.Contains(stderr, path) || !strings.Contains(stderr, "nope") {
		t.Errorf("a store that does not compile: %d %q", code, stderr)
	}
}

// TestServeStoreWarningsAndReloadFails runs a store whose config has a
// deprecated name, and reads it again once it no longer opens.
func TestServeStoreWarningsAndReloadFails(t *testing.T) {
	dir := t.TempDir()
	cfg, path := filepath.Join(dir, "fit.json"), filepath.Join(dir, "fit.store")
	writeStore(t, path, "a", `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
"routes": [{"middleware": [{"module": "compress", "params": {"min_length": 512}}], "handler": {"module": "respond"}}]}`)
	writeFile(t, cfg, `{"admin": {"address": "127.0.0.1:0", "store": {"path": `+js(path)+`, "password": "a"}}}`)
	s := startServe(t, "-c", cfg, "-watch-certs=false")
	s.out.waitFor(t, "fit: admin API on ", 1)
	if !strings.Contains(s.err.String(), "min_length: deprecated") {
		t.Errorf("no warning of the store's deprecated name: %q", s.err.String())
	}
	writeFile(t, path, "not a store")
	s.reload <- struct{}{}
	s.err.waitFor(t, "reload failed; the running config stays", 1)
	if code := s.stop(t); code != 0 {
		t.Fatalf("stop: %d %q", code, s.err.String())
	}
}

func TestServeLogError(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "fit.json")
	writeFile(t, cfg, `{"log": {"output": `+js(filepath.Join(dir, "missing", "fit.log"))+`},
"listeners": [{"name": "web", "address": "127.0.0.1:0"}], "routes": [{"handler": {"module": "respond"}}]}`)
	if code, _, stderr := runFit("run", "-c", cfg); code != 1 || !strings.Contains(stderr, "fit run: log: ") {
		t.Errorf("%d %q", code, stderr)
	}
}

// TestServeAdminConsole runs fit with admin.console, and with one that
// cannot start, alone or after the admin API.
func TestServeAdminConsole(t *testing.T) {
	dir := t.TempDir()
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	hash, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	console := func(addr string) string {
		return `"console": {"address": ` + js(addr) + `, "users": [{"username": "a", "password_hash": ` + js(string(hash)) + `}]}`
	}
	const rest = `"listeners": [{"name": "web", "address": "127.0.0.1:0"}], "routes": [{"handler": {"module": "respond"}}]}`
	cfg := filepath.Join(dir, "fit.json")

	writeFile(t, cfg, `{"admin": {"address": "127.0.0.1:0", `+console("127.0.0.1:0")+`}, `+rest)
	s := startServe(t, "-c", cfg, "-watch-certs=false")
	s.out.waitFor(t, "fit: console on http://127.0.0.1:", 1)
	if code := s.stop(t); code != 0 {
		t.Fatalf("stop: %d %q", code, s.err.String())
	}

	writeFile(t, cfg, `{"admin": {"address": "127.0.0.1:0", `+console(occupied.Addr().String())+`}, `+rest)
	if code, _, stderr := runFit("run", "-c", cfg, "-watch-certs=false"); code != 1 || !strings.Contains(stderr, "fit: console: ") {
		t.Errorf("a console that cannot listen: %d %q", code, stderr)
	}

	missing := js(filepath.Join(dir, "missing", "file"))
	writeFile(t, cfg, `{"admin": {"address": "127.0.0.1:0", `+console("127.0.0.1:0")+`},
"telemetry": {"headers_file": `+missing+`, "metrics": {}}, `+rest)
	if code, _, stderr := runFit("run", "-c", cfg, "-watch-certs=false"); code != 1 || !strings.Contains(stderr, "telemetry") {
		t.Errorf("metrics that cannot start beside a console: %d %q", code, stderr)
	}
}

// TestServeRoles runs a role of the config's, and one whose start fails.
func TestServeRoles(t *testing.T) {
	dir := t.TempDir()
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	const rest = `"listeners": [{"name": "web", "address": "127.0.0.1:0"}], "routes": [{"handler": {"module": "respond"}}]}`
	cfg := filepath.Join(dir, "fit.json")

	writeFile(t, cfg, `{"tunnel": {"hub": {"password": "pw"}}, `+rest)
	s := startServe(t, "-c", cfg, "-watch-certs=false")
	s.out.waitFor(t, "fit: running tunnel.hub", 1)
	if code := s.stop(t); code != 0 {
		t.Fatalf("stop: %d %q", code, s.err.String())
	}

	writeFile(t, cfg, `{"tunnel": {"hub": {"password": "pw", "listeners": [{"protocol": "tcp", "address": `+js(occupied.Addr().String())+`}]}}, `+rest)
	if code, _, stderr := runFit("run", "-c", cfg, "-watch-certs=false"); code != 1 || !strings.Contains(stderr, "tunnel.hub") {
		t.Errorf("a role that cannot start: %d %q", code, stderr)
	}
}

func TestWriteTemplateDirErrors(t *testing.T) {
	dir := t.TempDir()
	if err := writeTemplateDir("bogus", dir); err == nil {
		t.Error("a format there is no template for")
	}
	notDir := filepath.Join(dir, "file")
	writeFile(t, notDir, "x")
	if err := writeTemplateDir("json", filepath.Join(notDir, "sub")); err == nil {
		t.Error("a directory under a file")
	}
	if os.Getuid() == 0 {
		return // root writes into a read-only directory
	}
	ro := filepath.Join(dir, "ro")
	if err := os.Mkdir(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := writeTemplateDir("json", ro); err == nil {
		t.Error("a read-only directory")
	}
}

func TestWarnWriterOfAnotherWriter(t *testing.T) {
	var b bytes.Buffer
	if w := warnWriter(&b); w != io.Writer(&b) {
		t.Errorf("warnWriter = %#v, want the writer it was given", w)
	}
}
