//go:build !windows

package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestLogFileRotation runs fit with a small max_bytes for its own log, makes
// it reload a few times, and checks the file turned over and that no line
// was split or lost across the rotation.
func TestLogFileRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fit.log")
	pathJSON, _ := json.Marshal(path)
	doc := `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "log": {"output": ` + string(pathJSON) + `, "rotate": {"max_bytes": 300, "compress": false, "max_age": "0s", "max_total_bytes": 0}},
  "routes": [{"name": "hello", "match": {"path": ["/hello"]}, "handler": {"module": "respond", "params": {"body": "hi"}}}]
}`
	cfg := filepath.Join(t.TempDir(), "fit.json")
	if err := os.WriteFile(cfg, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(fitBin, "run", "-c", cfg)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()

	// The log opens before the listeners, so the file is the sign that fit
	// is up.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fit did not open its log")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for range 6 {
		if err := cmd.Process.Signal(syscall.SIGHUP); err != nil {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("fit exited: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	rotated, lines := 0, 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if e.Name() != "fit.log" {
			rotated++
		}
		if len(data) == 0 {
			t.Errorf("%s is empty", e.Name())
			continue
		}
		if !strings.HasSuffix(string(data), "\n") {
			t.Errorf("%s does not end with a whole line: %q", e.Name(), data)
		}
		lines += strings.Count(string(data), "\n")
	}
	if rotated == 0 {
		t.Fatalf("no rotation after six reloads: %v", names(entries))
	}
	if lines < 10 {
		t.Errorf("%d lines across the files: %v", lines, names(entries))
	}
}

// names returns the names of dir's entries, for a failure's message.
func names(entries []os.DirEntry) []string {
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}
