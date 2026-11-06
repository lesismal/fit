package admin

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
)

// The Admin API a manager reaches over a member's link: served in process,
// without the dashboard, refused while a console password is temporary,
// each change logged as the manager's, and the console's own headers never
// taken from the caller.
func TestLinkAPI(t *testing.T) {
	a := &config.Admin{}
	f := start(t, doc("v1"), a)
	var mu sync.Mutex
	var logs []string
	var applied []Result
	h := LinkAPI(a, f.configs, func(r Result) { mu.Lock(); applied = append(applied, r); mu.Unlock() }, func(format string, args ...any) {
		mu.Lock()
		logs = append(logs, fmt.Sprintf(format, args...))
		mu.Unlock()
	})
	do := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://cluster-link"+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(ConsoleUserHeader, "mallory")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	if w := do("GET", "/node", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"capabilities"`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := do("GET", "/dashboard/", ""); w.Code != 404 {
		t.Errorf("the dashboard: %d", w.Code)
	}
	if w := do("POST", "/load?dry_run=1", doc("v2")); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := do("POST", "/load", doc("v2")); w.Code != 200 || f.served(t) != "v2" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := do("POST", "/load", "{"); w.Code != 400 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	mu.Lock()
	got := strings.Join(logs, "\n")
	n := len(applied)
	mu.Unlock()
	for _, want := range []string{"POST /load?dry_run=1: 200, checked and not applied", "POST /load applied config version 2", "POST /load: 400"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q lacks %q", got, want)
		}
	}
	if n != 1 || f.configs.Current().Source != SourceCluster {
		t.Errorf("%d %+v", n, f.configs.Current())
	}
	f.configs.UseTemporaryPassword("ada", time.Now().Add(time.Hour))
	if w := do("GET", "/node", ""); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "temporary password") {
		t.Errorf("%d %s", w.Code, w.Body)
	}
	_ = LinkAPI(a, f.configs, nil, nil)
}
