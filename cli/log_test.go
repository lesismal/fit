package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestServeLog runs fit with its log in a file, as JSON, at warn but for
// the server logger, and reloads it at other levels.
func TestServeLog(t *testing.T) {
	dir := t.TempDir()
	cfg, logFile, accessFile := filepath.Join(dir, "fit.json"), filepath.Join(dir, "fit.log"), filepath.Join(dir, "access.log")
	doc := func(level, engine, access string) string {
		return `{"log": {"level": "` + level + `", "levels": {"server": "info", "engine": "` + engine + `"}, "output": ` + js(logFile) + `, "format": "json"},
"access_log": {"output": ` + js(accessFile) + `, "flush_interval": "10ms", "level": "` + access + `"},
"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
"routes": [{"match": {"path": ["/ok"]}, "handler": {"module": "respond"}}]}`
	}
	writeFile(t, cfg, doc("warn", "info", "warn"))
	s := startServe(t, "-c", cfg, "-watch-certs=false")
	lines := waitLog(t, logFile, func(ms []map[string]any) bool { return find(ms, "server", "listener \"web\" on ") != nil })
	if s.out.String() != "" || s.err.String() != "" {
		t.Errorf("written past the log: %q %q", s.out.String(), s.err.String())
	}
	l := find(lines, "server", "listener \"web\" on ")
	if l["level"] != "info" || l["time"] == nil {
		t.Errorf("listener line %v", l)
	}
	addr := strings.Fields(l["msg"].(string))[3]

	// The access log writes the 404, not the 200.
	for _, path := range []string{"/ok", "/missing"} {
		resp, err := http.Get("http://" + addr + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	access := waitFile(t, accessFile, `"status":404`)
	if strings.Contains(access, `"status":200`) {
		t.Errorf("the access log at warn wrote a 200: %s", access)
	}

	// A reload sets the levels: the engine's off but for errors, the access
	// log's to every request. The output's change waits for a restart.
	writeFile(t, cfg, strings.Replace(doc("warn", "error", "info"), `"format": "json"`, `"format": "text"`, 1))
	s.reload <- struct{}{}
	lines = waitLog(t, logFile, func(ms []map[string]any) bool { return find(ms, "server", "(config version 2)") != nil })
	if w := find(lines, "server", "log's output or format changed"); w == nil || w["level"] != "warn" {
		t.Errorf("no warning of the format's change in %v", lines)
	}
	resp, err := http.Get("http://" + addr + "/ok")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	waitFile(t, accessFile, `"status":200`)
	if code := s.stop(t); code != 0 {
		t.Fatalf("exit %d", code)
	}
	lines = readLog(t, logFile)
	if find(lines, "server", "stopped") == nil {
		t.Errorf("no stop in %v", lines)
	}
}

// find returns the first message of logger whose msg holds substr, or nil.
func find(ms []map[string]any, logger, substr string) map[string]any {
	for _, m := range ms {
		if m["logger"] == logger && strings.Contains(m["msg"].(string), substr) {
			return m
		}
	}
	return nil
}

func readLog(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, _ := os.ReadFile(path)
	var ms []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		ms = append(ms, m)
	}
	return ms
}

// waitLog waits until the JSON log at path is as ok says, and returns its
// messages.
func waitLog(t *testing.T, path string, ok func([]map[string]any) bool) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		ms := readLog(t, path)
		if ok(ms) {
			return ms
		}
		if time.Now().After(deadline) {
			t.Fatalf("log: %v", ms)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitFile waits until the file at path holds want, and returns it.
func waitFile(t *testing.T, path, want string) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		data, _ := os.ReadFile(path)
		if strings.Contains(string(data), want) {
			return string(data)
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s lacks %q: %s", path, want, data)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
