package admin

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lesismal/fit/config"
)

// pprofDoc is doc with admin, and pprof as given, or none when empty.
func pprofDoc(pprof string) string {
	d := `{"admin": {}, "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "routes": [{"handler": {"module": "respond", "params": {"body": "v"}}}]`
	if pprof != "" {
		d += `, "pprof": ` + pprof
	}
	return d + "}"
}

// TestPprof checks that the profiles are served only while the config has
// pprof, that the sampling rates follow every config applied, and that a
// CPU profile longer than the write timeout is refused.
func TestPprof(t *testing.T) {
	t.Cleanup(func() { applyProfiling(nil) })
	f := start(t, pprofDoc(""), &config.Admin{})
	for _, p := range []string{"/debug/pprof/", "/debug/pprof/heap", "/debug/pprof/profile?seconds=1", "/debug/pprof/trace?seconds=1"} {
		if resp, body := f.get(t, p); resp.StatusCode != 404 || !strings.Contains(body, "profiles are off") {
			t.Errorf("%s without pprof: %d %s", p, resp.StatusCode, body)
		}
	}

	if resp, body := f.load(t, pprofDoc(`{"block_profile_rate": 1, "mutex_profile_fraction": 3}`)); resp.StatusCode != 200 {
		t.Fatalf("load: %d %s", resp.StatusCode, body)
	}
	if got := runtime.SetMutexProfileFraction(-1); got != 3 {
		t.Errorf("mutex profile fraction %d, want 3", got)
	}
	if profileRates.block != 1 {
		t.Errorf("block profile rate %d, want 1", profileRates.block)
	}
	if resp, body := f.get(t, "/debug/pprof/"); resp.StatusCode != 200 || !strings.Contains(body, "goroutine") {
		t.Errorf("index: %d %.200s", resp.StatusCode, body)
	}
	for _, p := range []string{"/debug/pprof/heap", "/debug/pprof/goroutine?debug=1", "/debug/pprof/block", "/debug/pprof/mutex",
		"/debug/pprof/profile?seconds=1", "/debug/pprof/trace?seconds=1", "/debug/pprof/cmdline", "/debug/pprof/symbol"} {
		if resp, body := f.get(t, p); resp.StatusCode != 200 || body == "" {
			t.Errorf("%s: %d %.200s", p, resp.StatusCode, body)
		}
	}
	// A profile cannot sample for longer than max_duration, which bounds how
	// long it holds its connection; nor can a POST pass seconds in a body.
	for _, p := range []string{"/debug/pprof/profile?seconds=3600", "/debug/pprof/trace?seconds=61", "/debug/pprof/heap?seconds=1e9", "/debug/pprof/trace?seconds=NaN", "/debug/pprof/trace?seconds=Inf"} {
		if resp, body := f.get(t, p); resp.StatusCode != 400 || !strings.Contains(body, "max_duration") {
			t.Errorf("%s: %d %.200s", p, resp.StatusCode, body)
		}
	}
	req, _ := http.NewRequest("POST", f.api+"/debug/pprof/profile", strings.NewReader("seconds=3600"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if resp, _ := f.do(t, req); resp.StatusCode != 405 {
		t.Errorf("POST profile: %d", resp.StatusCode)
	}
	if resp, body := f.load(t, pprofDoc(`{"max_duration": "2s"}`)); resp.StatusCode != 200 {
		t.Fatalf("load: %d %s", resp.StatusCode, body)
	}
	if resp, body := f.get(t, "/debug/pprof/profile?seconds=3"); resp.StatusCode != 400 {
		t.Errorf("3s past a max_duration of 2s: %d %.200s", resp.StatusCode, body)
	}
	if resp, _ := f.get(t, "/debug/pprof/trace?seconds=0.5"); resp.StatusCode != 200 {
		t.Errorf("half a second's trace: %d", resp.StatusCode)
	}

	if resp, body := f.load(t, pprofDoc("")); resp.StatusCode != 200 {
		t.Fatalf("load: %d %s", resp.StatusCode, body)
	}
	if resp, _ := f.get(t, "/debug/pprof/heap"); resp.StatusCode != 404 {
		t.Errorf("heap after pprof left out: %d", resp.StatusCode)
	}
	if got := runtime.SetMutexProfileFraction(-1); got != 0 {
		t.Errorf("mutex profile fraction %d after pprof left out, want 0", got)
	}
	if profileRates.block != 0 {
		t.Errorf("block profile rate %d after pprof left out, want 0", profileRates.block)
	}
}

// TestPprofFirstConfig checks that the config fit starts with sets the
// sampling rates.
func TestPprofFirstConfig(t *testing.T) {
	t.Cleanup(func() { applyProfiling(nil) })
	f := start(t, pprofDoc(`{"mutex_profile_fraction": 5}`), &config.Admin{})
	if got := runtime.SetMutexProfileFraction(-1); got != 5 {
		t.Errorf("mutex profile fraction %d, want 5", got)
	}
	if resp, _ := f.get(t, "/debug/pprof/allocs"); resp.StatusCode != 200 {
		t.Errorf("allocs: %d", resp.StatusCode)
	}
}

// TestPprofToken checks that the profiles are behind the Admin API's
// token, and that the console's endpoints do not serve them.
func TestPprofToken(t *testing.T) {
	t.Cleanup(func() { applyProfiling(nil) })
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("s3cret"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := start(t, pprofDoc(`{}`), &config.Admin{TokenFile: file})
	for auth, status := range map[string]int{"": 401, "Bearer wrong": 401, "Bearer s3cret": 200} {
		req, _ := http.NewRequest("GET", f.api+"/debug/pprof/heap", nil)
		req.Header.Set("Authorization", auth)
		if resp, _ := f.do(t, req); resp.StatusCode != status {
			t.Errorf("Authorization %q: %d, want %d", auth, resp.StatusCode, status)
		}
	}

	// The console calls the API's routes, which leave the profiles out.
	inner := &Server{configs: f.configs, maxBody: 1 << 20, source: SourceConsole}
	rec := httptest.NewRecorder()
	inner.routes().ServeHTTP(rec, httptest.NewRequest("GET", "/debug/pprof/heap", nil))
	if rec.Code != 404 {
		t.Errorf("the console's routes: %d", rec.Code)
	}
}
