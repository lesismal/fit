package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runFit(args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestUsage(t *testing.T) {
	if code, _, stderr := runFit(); code != 2 || !strings.Contains(stderr, "Usage:") {
		t.Fatalf("no args: %d %q", code, stderr)
	}
	if code, stdout, _ := runFit("help"); code != 0 || !strings.Contains(stdout, "validate") {
		t.Fatalf("help: %d %q", code, stdout)
	}
	if code, _, stderr := runFit("bogus"); code != 2 || !strings.Contains(stderr, `unknown command "bogus"`) {
		t.Fatalf("bogus: %d %q", code, stderr)
	}
}

func TestVersion(t *testing.T) {
	code, stdout, _ := runFit("version")
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if code != 0 || len(lines) != 3 || !strings.HasPrefix(lines[0], "fit ") || !strings.HasPrefix(lines[1], "fib ") || !strings.HasPrefix(lines[2], "go") {
		t.Fatalf("version: %d %q", code, stdout)
	}
	// The fib linked in is the one go.mod requires.
	if lines[1] == "fib unknown" {
		t.Fatalf("fib's version is unknown: %q", stdout)
	}
}

func TestValidate(t *testing.T) {
	if code, stdout, stderr := runFit("validate", "-c", "../../examples/hello.json"); code != 0 || stdout != "../../examples/hello.json: valid\n" {
		t.Fatalf("example: %d %q %q", code, stdout, stderr)
	}

	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	doc := `{"listeners": [],
"routes": [{"handler": {"module": "respond", "params": {"status": 101}}}]}`
	if err := os.WriteFile(bad, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runFit("validate", "-c", bad)
	if want := bad + ": listeners: at least one listener is needed\n"; code != 1 || stderr != want {
		t.Fatalf("bad config: %d %q, want %q", code, stderr, want)
	}

	doc = `{"listeners": [{"name": "web", "address": ":0"}],
"routes": [{"handler": {"module": "respond", "params": {"status": 101}}}]}`
	if err := os.WriteFile(bad, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = runFit("validate", "-c", bad)
	if want := bad + ":2:67: routes[0].handler.params.status: 101 is an informational status, which cannot be a final response\n"; code != 1 || stderr != want {
		t.Fatalf("bad module params: %d %q, want %q", code, stderr, want)
	}

	if code, _, stderr := runFit("validate", "-c", filepath.Join(dir, "missing.json")); code != 1 || !strings.Contains(stderr, "missing.json") {
		t.Fatalf("missing file: %d %q", code, stderr)
	}
	if code, _, _ := runFit("validate", "extra"); code != 2 {
		t.Fatalf("extra argument: %d", code)
	}
}

func TestSchema(t *testing.T) {
	code, stdout, _ := runFit("schema")
	var s map[string]any
	if code != 0 || json.Unmarshal([]byte(stdout), &s) != nil || s["title"] != "fit config" {
		t.Fatalf("schema: %d %.200q", code, stdout)
	}
}
