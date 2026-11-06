package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/admin/store"
	"github.com/lesismal/fit/config"
)

// TestNodeIDKept: the node ID is made once and read back the same, from the
// file beside the config without a store, and from the store with one; a
// fit given a store keeps the ID its file had; with nowhere to keep it, or a
// file that holds no ID, it lasts in memory.
func TestNodeIDKept(t *testing.T) {
	dir := t.TempDir()
	prefix := filepath.Join(dir, "fit")

	// Without a store: made, written beside the config, read back.
	id, kept, err := NodeID(nil, prefix)
	if err != nil || kept != IDInFile || !nodeID.MatchString(id) {
		t.Fatalf("first run: %q %q %v", id, kept, err)
	}
	if data, err := os.ReadFile(prefix + NodeIDSuffix); err != nil || strings.TrimSpace(string(data)) != id {
		t.Fatalf("the file: %q %v", data, err)
	}
	if id2, kept2, err := NodeID(nil, prefix); err != nil || id2 != id || kept2 != IDInFile {
		t.Fatalf("a restart: %q %q %v", id2, kept2, err)
	}

	// Given a store: the file's ID moves into it, and stays there.
	path := filepath.Join(dir, "fit.store")
	st, err := store.Create(path, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Write([]byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if id2, kept2, err := NodeID(st, prefix); err != nil || id2 != id || kept2 != IDInStore {
		t.Fatalf("into the store: %q %q %v", id2, kept2, err)
	}
	st2, _, err := store.Open(path, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if id2, kept2, err := NodeID(st2, ""); err != nil || id2 != id || kept2 != IDInStore {
		t.Fatalf("the store reopened: %q %q %v", id2, kept2, err)
	}

	// A store of its own, no file: made in it, and read back.
	st3, err := store.Create(filepath.Join(t.TempDir(), "other.store"), "secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := st3.Write([]byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	other, kept3, err := NodeID(st3, "")
	if err != nil || kept3 != IDInStore || other == id {
		t.Fatalf("a new store: %q %q %v", other, kept3, err)
	}
	if again, _, _ := NodeID(st3, ""); again != other {
		t.Fatalf("a new store, read again: %q, want %q", again, other)
	}

	// A section that holds no ID, a store with no config yet, no place, a
	// file that holds no ID, and a directory that cannot be written.
	if err := st3.SetSection(NodeSection, []byte(`{"id": "no spaces allowed"}`)); err != nil {
		t.Fatal(err)
	}
	if _, kept, err := NodeID(st3, ""); err == nil || kept != IDInMemory {
		t.Errorf("a bad section: %q %v", kept, err)
	}
	empty, err := store.Create(filepath.Join(t.TempDir(), "empty.store"), "secret")
	if err != nil {
		t.Fatal(err)
	}
	if id, kept, err := NodeID(empty, ""); err == nil || kept != IDInMemory || id == "" {
		t.Errorf("a store with no config: %q %q %v", id, kept, err)
	}
	if id, kept, err := NodeID(nil, ""); err != nil || kept != IDInMemory || id == "" {
		t.Errorf("nowhere: %q %q %v", id, kept, err)
	}
	bad := filepath.Join(t.TempDir(), "bad")
	if err := os.WriteFile(bad+NodeIDSuffix, []byte("not an id\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, kept, err := NodeID(nil, bad); err == nil || kept != IDInMemory {
		t.Errorf("a bad file: %q %v", kept, err)
	}
	if data, _ := os.ReadFile(bad + NodeIDSuffix); string(data) != "not an id\n" {
		t.Errorf("a bad file was overwritten: %q", data)
	}
	if _, kept, err := NodeID(nil, filepath.Join(t.TempDir(), "missing", "fit")); err == nil || kept != IDInMemory {
		t.Errorf("no directory to write in: %q %v", kept, err)
	}
	unreadable := filepath.Join(t.TempDir(), "dir")
	if err := os.Mkdir(unreadable+NodeIDSuffix, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, kept, err := NodeID(nil, unreadable); err == nil || kept != IDInMemory {
		t.Errorf("a directory in the file's place: %q %v", kept, err)
	}
}

// nodeAnswer is GET /node's answer, as a manager reads it.
type nodeAnswer struct {
	ID            string    `json:"id"`
	IDKept        string    `json:"id_kept"`
	Hostname      string    `json:"hostname"`
	FitVersion    string    `json:"fit_version"`
	FibVersion    string    `json:"fib_version"`
	GoVersion     string    `json:"go_version"`
	OS            string    `json:"os"`
	Started       time.Time `json:"started"`
	ConfigVersion uint64    `json:"config_version"`
	ConfigETag    string    `json:"config_etag"`
	ConfigSource  string    `json:"config_source"`
	Roles         []string  `json:"roles"`
	SchemaVersion int       `json:"schema_version"`
	SchemaDigest  string    `json:"schema_digest"`
	Capabilities  []string  `json:"capabilities"`
}

// TestNode: GET /node says who the fit is, what config runs and what the
// API offers; through the console too, where it lists the console.
func TestNode(t *testing.T) {
	f := start(t, doc("one"), &config.Admin{})
	resp, body := f.get(t, "/node")
	var n nodeAnswer
	if resp.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &n) != nil {
		t.Fatalf("GET /node: %d %s", resp.StatusCode, body)
	}
	if n.ID == "" || n.IDKept != IDInMemory || n.FitVersion == "" || n.FibVersion == "" || n.GoVersion == "" || n.OS == "" ||
		n.Started.IsZero() || n.ConfigVersion != 1 || n.ConfigETag != f.configs.Current().ETag || n.ConfigSource != SourceFile ||
		n.Roles == nil || n.SchemaVersion != config.SchemaVersion || !strings.HasPrefix(n.SchemaDigest, "sha256:") {
		t.Fatalf("GET /node: %s", body)
	}
	if resp.Header.Get("ETag") != n.ConfigETag || resp.Header.Get(VersionHeader) != "1" {
		t.Errorf("headers: %v", resp.Header)
	}
	for _, c := range []string{CapNode, CapDryRun, CapIfMatch, CapText, CapVersions} {
		if !slices.Contains(n.Capabilities, c) {
			t.Errorf("capabilities %q lack %q", n.Capabilities, c)
		}
	}
	if slices.Contains(n.Capabilities, CapConsole) || slices.Contains(n.Capabilities, CapStore) || !slices.IsSorted(n.Capabilities) {
		t.Errorf("capabilities: %q", n.Capabilities)
	}

	// Who the fit is, as fit run says; a change shows its version.
	started := time.Date(2026, 11, 6, 1, 2, 3, 0, time.UTC)
	f.configs.UseNode(Node{ID: "node-a", IDKept: IDInFile, FitVersion: "v9.9.9", Started: started})
	if r, body := f.load(t, doc("two")); r.StatusCode != http.StatusOK {
		t.Fatal(body)
	}
	_, body = f.get(t, "/node")
	n = nodeAnswer{}
	_ = json.Unmarshal([]byte(body), &n)
	if n.ID != "node-a" || n.IDKept != IDInFile || n.FitVersion != "v9.9.9" || n.FibVersion == "" || n.Hostname != f.configs.Node().Hostname ||
		!n.Started.Equal(started) || n.ConfigVersion != 2 || n.ConfigSource != SourceAdmin {
		t.Fatalf("after UseNode and a load: %s", body)
	}

	// Through the console, with a store.
	st, err := store.Create(filepath.Join(t.TempDir(), "fit.store"), "secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Write([]byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	f.configs.UseSealer(st)
	cf := &consoleFixture{fixture: f}
	cs, err := StartConsole(testEngine(t), &config.Console{Address: "127.0.0.1:0", Users: testUsers()}, &config.Admin{}, f.configs, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Shutdown(context.Background()) })
	cf.console, cf.url = cs, strings.TrimSuffix(cs.URL(), "/")
	b := cf.browser(t)
	if code := b.login("ada", "pw"); code != http.StatusOK {
		t.Fatal(code)
	}
	for _, path := range []string{"/api/node", "/api/m/admin/node"} {
		code, body, _ := b.do("GET", path, "")
		n = nodeAnswer{}
		if code != http.StatusOK || json.Unmarshal([]byte(body), &n) != nil || n.ID != "node-a" ||
			!slices.Contains(n.Capabilities, CapConsole) || !slices.Contains(n.Capabilities, CapStore) {
			t.Errorf("GET %s: %d %s", path, code, body)
		}
	}
}

// TestUseNodeKeeps: what UseNode leaves out keeps what Configs found.
func TestUseNodeKeeps(t *testing.T) {
	f := start(t, doc("one"), &config.Admin{})
	before := f.configs.Node()
	f.configs.UseNode(Node{Hostname: "h1", FibVersion: "v0.0.1"})
	n := f.configs.Node()
	if n.ID != before.ID || n.IDKept != before.IDKept || n.Hostname != "h1" || n.FitVersion != before.FitVersion ||
		n.FibVersion != "v0.0.1" || !n.Started.Equal(before.Started) {
		t.Errorf("UseNode: %+v, was %+v", n, before)
	}
}

func TestNewNodeID(t *testing.T) {
	a, b := newNodeID(), newNodeID()
	if a == b || len(a) != 36 || a[14] != '4' || !strings.ContainsAny(a[19:20], "89ab") || !nodeID.MatchString(a) {
		t.Errorf("node IDs %q %q", a, b)
	}
}
