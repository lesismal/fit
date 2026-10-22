package build

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParseModule(t *testing.T) {
	for in, want := range map[string]Module{
		"example.com/m":            {Path: "example.com/m"},
		"example.com/m@v1.2.3":     {Path: "example.com/m", Version: "v1.2.3"},
		"example.com/m=../src/m":   {Path: "example.com/m", Dir: "../src/m"},
		"example.com/m@latest":     {Path: "example.com/m", Version: "latest"},
		"example.com/m=C:\\src\\m": {Path: "example.com/m", Dir: "C:\\src\\m"},
	} {
		if got, err := ParseModule(in); err != nil || got != want {
			t.Errorf("%s: %+v %v", in, got, err)
		}
	}
	for _, in := range []string{"", "@v1", "=dir", "example.com/m@v1=dir", "a b"} {
		if _, err := ParseModule(in); err == nil {
			t.Errorf("%q: no error", in)
		}
	}
}

func TestImports(t *testing.T) {
	got, err := Imports([]Module{{Path: "example.com/a"}, {Path: "example.com/a"}}, []string{FitModule + "/provider/standard"})
	if err != nil || !slices.Equal(got, []string{FitModule + "/modules/standard", "example.com/a"}) {
		t.Errorf("%v %v", got, err)
	}
	if _, err := Imports(nil, []string{"example.com/x"}); err == nil {
		t.Error("leaving out what is not imported")
	}
	src := string(MainSource(got))
	for _, want := range []string{"package main", `"github.com/lesismal/fit/cli"`, `_ "example.com/a"`, "cli.Main(os.Args[1:]"} {
		if !strings.Contains(src, want) {
			t.Errorf("the main package has no %s:\n%s", want, src)
		}
	}
}

// repoRoot is this checkout of fit.
func repoRoot(t *testing.T) string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("no caller")
	}
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// TestBuildWithModule builds fit from this checkout with the example
// module examples/module/hello and without the providers, and serves a
// route of the module with the binary: an external module builds into fit
// and runs. It needs the go command, and the modules fit requires, from
// the module cache or the network.
func TestBuildWithModule(t *testing.T) {
	if testing.Short() {
		t.Skip("builds fit")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go command")
	}
	root := repoRoot(t)
	dir := t.TempDir()
	bin := filepath.Join(dir, "fit")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	var log strings.Builder
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	err := Build(ctx, Options{
		Output: bin, Fit: Module{Dir: root}, Version: "test+build",
		With:    []Module{{Path: FitModule + "/examples/module/hello", Dir: filepath.Join(root, "examples", "module", "hello")}},
		Without: []string{FitModule + "/provider/standard"},
		Keep:    filepath.Join(dir, "main"), Stderr: &log,
	})
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	if out, err := exec.Command(bin, "version").CombinedOutput(); err != nil || !strings.Contains(string(out), "fit test+build") {
		t.Errorf("version: %v %s", err, out)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	cfg := filepath.Join(dir, "fit.json")
	config := fmt.Sprintf(`{"listeners": [{"name": "web", "address": %q}],
  "routes": [{"middleware": [{"module": "hello", "params": {"greeting": "from a module"}}],
    "handler": {"module": "respond", "params": {"body": "hi"}}}]}`, addr)
	if err := os.WriteFile(cfg, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(bin, "validate", "-c", cfg).CombinedOutput(); err != nil {
		t.Fatalf("validate: %v %s", err, out)
	}
	providers := filepath.Join(dir, "providers.json")
	_ = os.WriteFile(providers, []byte(`{"listeners": [{"name": "web", "address": "127.0.0.1:0"}], "admin": {"address": "127.0.0.1:0"},
  "providers": [{"docker": {}}], "routes": [{"handler": {"module": "respond"}}]}`), 0o644)
	if out, err := exec.Command(bin, "run", "-c", providers).CombinedOutput(); err == nil || !strings.Contains(string(out), "built without the docker provider") {
		t.Errorf("a provider left out: %v %s", err, out)
	}

	cmd := exec.Command(bin, "run", "-c", cfg)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := http.Get("http://" + addr + "/")
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.Header.Get("X-Hello") != "from a module" || string(body) != "hi" {
				t.Errorf("%d %q, X-Hello %q", resp.StatusCode, body, resp.Header.Get("X-Hello"))
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the build did not serve: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
