package wasm

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// syncGuest is a language the sync modules' test plugin is written in,
// built once, when a test first asks for it: Go (testdata/sync), the same
// source by TinyGo, and Rust on the fit-guest crate (testdata/rust). The
// tests run each that can be built here. A missing toolchain skips its
// guest, unless FIT_WASM_GUESTS names it (CI's WASM guests job sets
// "tinygo,rust"), when it fails the test.
type syncGuest struct {
	name  string
	tools []string // the commands it is built with
	build func(dir string) (path string, out []byte, err error)
	ready func() string // why the toolchain cannot build here, or ""

	once sync.Once
	path string
	err  string
}

var syncGuests = []*syncGuest{
	{name: "go", tools: []string{"go"}, build: func(dir string) (string, []byte, error) {
		path := filepath.Join(dir, "sync.wasm")
		cmd := exec.Command("go", "build", "-buildmode=c-shared", "-o", path, "./testdata/sync")
		cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
		out, err := cmd.CombinedOutput()
		return path, out, err
	}},
	{name: "tinygo", tools: []string{"tinygo"}, build: func(dir string) (string, []byte, error) {
		path := filepath.Join(dir, "sync.wasm")
		cmd := exec.Command("tinygo", "build", "-target=wasip1", "-buildmode=c-shared", "-o", path, "./testdata/sync")
		out, err := cmd.CombinedOutput()
		return path, out, err
	}},
	{name: "rust", tools: []string{"cargo", "rustc"}, ready: sync.OnceValue(rustReady), build: func(dir string) (string, []byte, error) {
		cmd := exec.Command("cargo", "build", "--release", "--quiet", "--target", "wasm32-wasip1", "--manifest-path", "testdata/rust/Cargo.toml")
		cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+dir)
		out, err := cmd.CombinedOutput()
		return filepath.Join(dir, "wasm32-wasip1", "release", "fit_sync_test.wasm"), out, err
	}},
}

// rustReady reports whether rustc has the wasm32-wasip1 target's library.
func rustReady() string {
	out, err := exec.Command("rustc", "--print", "sysroot").Output()
	if err != nil {
		return "rustc --print sysroot: " + err.Error()
	}
	lib := filepath.Join(strings.TrimSpace(string(out)), "lib", "rustlib", "wasm32-wasip1")
	if _, err := os.Stat(lib); err != nil {
		return "no wasm32-wasip1 target (rustup target add wasm32-wasip1)"
	}
	return ""
}

// required reports whether FIT_WASM_GUESTS names g, so that it may not be
// skipped.
func (g *syncGuest) required() bool {
	return slices.Contains(strings.Split(os.Getenv("FIT_WASM_GUESTS"), ","), g.name)
}

// plugin builds g's plugin, once, and returns where; it skips t when g's
// toolchain is not here and g is not required.
func (g *syncGuest) plugin(t testing.TB) string {
	t.Helper()
	missing := ""
	for _, tool := range g.tools {
		if _, err := exec.LookPath(tool); err != nil {
			missing = "no " + tool + " command to build the " + g.name + " plugin with"
			break
		}
	}
	if missing == "" && g.ready != nil {
		missing = g.ready()
	}
	if missing != "" {
		if g.required() {
			t.Fatalf("FIT_WASM_GUESTS names %s, but %s", g.name, missing)
		}
		t.Skip(missing)
	}
	g.once.Do(func() {
		dir, err := os.MkdirTemp("", "fit-wasm-sync-"+g.name+"-")
		if err != nil {
			g.err = err.Error()
			return
		}
		path, out, err := g.build(dir)
		if err != nil {
			g.err = err.Error() + "\n" + string(out)
			return
		}
		g.path = path
	})
	if g.err != "" {
		t.Fatalf("building the %s plugin: %s", g.name, g.err)
	}
	return g.path
}

// eachGuest runs f as a subtest for each guest, with its plugin's path.
func eachGuest(t *testing.T, f func(t *testing.T, plugin string)) {
	for _, g := range syncGuests {
		t.Run(g.name, func(t *testing.T) { f(t, g.plugin(t)) })
	}
}

// goGuest returns the Go plugin's path.
func goGuest(t testing.TB) string { return syncGuests[0].plugin(t) }
