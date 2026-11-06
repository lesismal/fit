package admin

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lesismal/fit/admin/store"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/managed"
)

// TestManaged: what a manager owns here is kept, in memory without a store
// and in the store with one, answered with whether each part changed here
// since the manager saw it in sync, guarded by its ETag, and forgotten.
func TestManaged(t *testing.T) {
	f := start(t, `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "routes": [{"name": "api", "handler": {"module": "respond", "params": {"body": "a"}}}], "log": {"level": "info"}}`, &config.Admin{})
	send := func(method, body string, header ...string) (*http.Response, string) {
		t.Helper()
		req, _ := http.NewRequest(method, f.api+"/node/managed", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		for i := 0; i+1 < len(header); i += 2 {
			req.Header.Set(header[i], header[i+1])
		}
		return f.do(t, req)
	}
	resp, body := f.get(t, "/node/managed")
	if resp.StatusCode != 200 || !strings.Contains(body, `"kept":"memory"`) || !strings.Contains(body, `"paths":[]`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	empty := resp.Header.Get("ETag")
	route, _, _ := managed.Lookup(f.configs.Current().Data(), "routes", "api")
	rec := managed.Record{Manager: "mgr", ManagerHost: "h", Paths: []managed.Path{
		{Member: "routes", Name: "api", Template: "t", Hash: managed.Hash(route)},
		{Member: "log", Template: "t", Hash: strings.Repeat("0", 64)},
		{Member: "upstreams", Name: "gone", Template: "t"},
	}}
	data, _ := json.Marshal(rec)
	if resp, body := send("PUT", string(data), "If-Match", `"stale"`); resp.StatusCode != 412 {
		t.Fatalf("a stale If-Match: %d %s", resp.StatusCode, body)
	}
	resp, body = send("PUT", string(data), "If-Match", empty)
	if resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	for _, want := range []string{`"manager":"mgr"`, `"path":"log","present":true,"changed":true`,
		`"path":"routes[api]","present":true,"changed":false`, `"path":"upstreams[gone]","present":false,"changed":false`} {
		if !strings.Contains(body, want) {
			t.Errorf("%s lacks %s", body, want)
		}
	}
	if resp.Header.Get("ETag") == empty || resp.Header.Get("ETag") != rec.ETag() {
		t.Errorf("ETag %q", resp.Header.Get("ETag"))
	}
	// The route changed here: marked changed.
	if r, b := f.load(t, `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "routes": [{"name": "api", "handler": {"module": "respond", "params": {"body": "b"}}}]}`); r.StatusCode != 200 {
		t.Fatal(b)
	}
	if _, body := f.get(t, "/node/managed"); !strings.Contains(body, `"path":"routes[api]","present":true,"changed":true`) ||
		!strings.Contains(body, `"path":"log","present":false,"changed":true`) {
		t.Errorf("%s", body)
	}
	for _, bad := range []string{`{`, `{"paths":[{"member":"admin"}]}`, `{"paths":[{"member":"routes"}]}`, `{"unknown":1}`} {
		if resp, body := send("PUT", bad); resp.StatusCode != 400 {
			t.Errorf("%s: %d %s", bad, resp.StatusCode, body)
		}
	}
	if resp, body := send("PUT", `{"paths":[]}`+strings.Repeat(" ", managed.MaxRecordBytes)); resp.StatusCode != 413 {
		t.Errorf("too large: %d %s", resp.StatusCode, body)
	}
	if resp, body := send("DELETE", ""); resp.StatusCode != 200 || !strings.Contains(body, `"paths":[]`) || resp.Header.Get("ETag") != empty {
		t.Errorf("forgotten: %d %s", resp.StatusCode, body)
	}

	// With a store, kept there, and read back from it.
	dir := t.TempDir()
	st, err := store.Create(filepath.Join(dir, "fit.store"), "secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Write([]byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	f.configs.UseSealer(st)
	if _, body := f.get(t, "/node/managed"); !strings.Contains(body, `"kept":"store"`) || !strings.Contains(body, `"paths":[]`) {
		t.Errorf("%s", body)
	}
	if resp, body := send("PUT", string(data)); resp.StatusCode != 200 || !strings.Contains(body, `"kept":"store"`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	var kept managed.Record
	if err := json.Unmarshal(st.Section(managed.Section), &kept); err != nil || len(kept.Paths) != 3 || kept.Manager != "mgr" {
		t.Errorf("the section: %+v %v", kept, err)
	}
	// A section that holds no record reads as none.
	_ = st.SetSection(managed.Section, []byte(`{"paths":[{"member":"admin"}]}`))
	if rec, kept := f.configs.Managed(); len(rec.Paths) != 0 || kept != ManagedInStore {
		t.Errorf("%+v %s", rec, kept)
	}
	// The capability is listed.
	if _, body := f.get(t, "/node"); !strings.Contains(body, `"`+CapManaged+`"`) {
		t.Errorf("%s", body)
	}
	if err := f.configs.SetManaged(managed.Record{Paths: []managed.Path{{Member: "x y"}}}); err == nil {
		t.Error("a bad record kept")
	}
	if resp, body := send("PUT", "x", "Content-Type", "text/plain"); resp.StatusCode != 415 {
		t.Errorf("text: %d %s", resp.StatusCode, body)
	}
	// A store that cannot be written: 500, and the record as it was.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if resp, body := send("DELETE", ""); resp.StatusCode != 500 && os.Geteuid() != 0 && runtime.GOOS != "windows" {
		t.Errorf("a store that cannot be written: %d %s", resp.StatusCode, body)
	}
}
