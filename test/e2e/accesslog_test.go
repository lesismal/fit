//go:build !windows

package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAccessLogFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.log")
	pathJSON, _ := json.Marshal(path)
	f, addrs := startFit(t, `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "access_log": {"output": `+string(pathJSON)+`, "flush_interval": "1h"},
  "routes": [{"name": "hello", "match": {"path": ["/hello"]}, "handler": {"module": "respond", "params": {"body": "hi"}}}]
}`)
	for _, p := range []string{"/hello", "/missing"} {
		resp, err := client(false).Get("http://" + addrs["web"] + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	// The lines wait for the interval, an hour, or for the shutdown.
	if code, out := f.stop(t); code != 0 {
		t.Fatalf("stop: exit %d, output %q", code, out)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("%d lines: %q", len(lines), data)
	}
	seen := map[string]float64{}
	for _, line := range lines {
		var got map[string]any
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		seen[got["uri"].(string)] = got["status"].(float64)
	}
	if seen["/hello"] != 200 || seen["/missing"] != 404 {
		t.Fatalf("lines: %q", lines)
	}
}
