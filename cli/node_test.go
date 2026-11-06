package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// nodeOf reads the Admin API's GET /node of the fit serve runs.
func nodeOf(t *testing.T, s *served) (id, kept, fitVersion string) {
	t.Helper()
	out := s.out.waitFor(t, "fit: admin API on ", 1)
	var n struct {
		ID         string `json:"id"`
		IDKept     string `json:"id_kept"`
		FitVersion string `json:"fit_version"`
	}
	body := get(t, "http://"+adminRE.FindStringSubmatch(out)[1]+"/node")
	if err := json.Unmarshal([]byte(body), &n); err != nil || n.ID == "" {
		t.Fatalf("GET /node: %s", body)
	}
	return n.ID, n.IDKept, n.FitVersion
}

// TestServeNodeID: GET /node answers the same node ID across restarts,
// kept beside the config without a store and in the store with one, which
// takes over the ID the file had.
func TestServeNodeID(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "fit.json")
	rest := `"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
"routes": [{"handler": {"module": "respond", "params": {"body": "one"}}}]}`
	writeFile(t, cfg, `{"admin": {"address": "127.0.0.1:0"}, `+rest)

	s := startServe(t, "-c", cfg, "-watch-certs=false")
	id, kept, version := nodeOf(t, s)
	if s.stop(t) != 0 || kept != "file" || version == "" {
		t.Fatalf("first run: %q %q %q", id, kept, version)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "fit.node-id")); err != nil || strings.TrimSpace(string(data)) != id {
		t.Fatalf("fit.node-id: %q %v", data, err)
	}
	s = startServe(t, "-c", cfg, "-watch-certs=false")
	if again, kept, _ := nodeOf(t, s); s.stop(t) != 0 || again != id || kept != "file" {
		t.Fatalf("a restart: %q %q, want %q", again, kept, id)
	}

	// With a store: the same ID, kept in the store from then on.
	path := filepath.Join(dir, "fit.store")
	writeFile(t, cfg, `{"admin": {"address": "127.0.0.1:0", "store": {"path": `+js(path)+`, "password": "s3cret"}}, `+rest)
	s = startServe(t, "-c", cfg, "-watch-certs=false")
	if again, kept, _ := nodeOf(t, s); s.stop(t) != 0 || again != id || kept != "store" {
		t.Fatalf("with a store: %q %q, want %q", again, kept, id)
	}
	if err := os.Remove(filepath.Join(dir, "fit.node-id")); err != nil {
		t.Fatal(err)
	}
	s = startServe(t, "-c", cfg, "-watch-certs=false")
	if again, kept, _ := nodeOf(t, s); s.stop(t) != 0 || again != id || kept != "store" {
		t.Fatalf("the store alone after a restart: %q %q, want %q", again, kept, id)
	}

	// Nowhere to keep it: a warning, and an ID for this run.
	ro := filepath.Join(dir, "gone", "fit.json")
	if err := os.MkdirAll(filepath.Dir(ro), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, ro, `{"admin": {"address": "127.0.0.1:0"}, `+rest)
	if err := os.Mkdir(filepath.Join(dir, "gone", "fit.node-id"), 0o700); err != nil {
		t.Fatal(err)
	}
	s = startServe(t, "-c", ro, "-watch-certs=false")
	if other, kept, _ := nodeOf(t, s); s.stop(t) != 0 || other == id || kept != "memory" || !strings.Contains(s.err.String(), "the node ID could not be kept") {
		t.Fatalf("nowhere to keep it: %q %q %q", other, kept, s.err.String())
	}
}
