package admin

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/certtest"
	"github.com/lesismal/fit/observe"
	"github.com/lesismal/fit/runtime"
	"github.com/lesismal/fit/upstream"
)

func TestStatus(t *testing.T) {
	backend := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "from the backend")
	})}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = backend.Serve(ln) }()
	t.Cleanup(func() { _ = backend.Close() })
	deadLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := deadLn.Addr().String()
	deadLn.Close()
	engine, stop, err := upstream.StartEngine()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	cert, err := certtest.Write(t.TempDir(), "a.example")
	if err != nil {
		t.Fatal(err)
	}
	f := startEnv(t, fmt.Sprintf(`{
  "listeners": [
    {"name": "web", "address": "127.0.0.1:0"},
    {"name": "secure", "address": "127.0.0.1:0", "tls": {"certificates": [{"cert_file": %q, "key_file": %q}]}}
  ],
  "upstreams": [{"name": "back", "addresses": [%q, %q], "max_fails": 1, "fail_timeout": "1m"}],
  "routes": [{"listeners": ["web"], "handler": {"module": "proxy", "params": {"upstream": "back"}}}]
}`, cert.CertFile, cert.KeyFile, ln.Addr().String(), dead), &config.Admin{}, runtime.Env{Engine: engine, Metrics: observe.NewMetrics()})
	web := "http://" + f.server.Addrs()[0].Addr.String()
	// Enough requests that the dead backend is tried, fails and is taken
	// out, and the live one answers.
	for range 6 {
		if resp, err := f.client.Get(web + "/"); err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}

	resp, body := f.get(t, "/status")
	if resp.StatusCode != 200 || resp.Header.Get(VersionHeader) != "1" {
		t.Fatalf("GET /status: %d %s", resp.StatusCode, body)
	}
	var st Status
	if err := json.Unmarshal([]byte(body), &st); err != nil {
		t.Fatal(err)
	}
	if st.Version != 1 || st.Source != SourceFile || st.Applied.IsZero() || len(st.Listeners) != 2 || st.Listeners[1].Name != "secure" {
		t.Errorf("status: %+v", st)
	}
	if len(st.Upstreams) != 1 || st.Upstreams[0].Name != "back" || len(st.Upstreams[0].Backends) != 2 {
		t.Fatalf("upstreams: %+v", st.Upstreams)
	}
	live, gone := st.Upstreams[0].Backends[0], st.Upstreams[0].Backends[1]
	if live.Address != ln.Addr().String() || !live.Up || live.Succeeded == 0 || live.Failed != 0 {
		t.Errorf("the live backend: %+v", live)
	}
	if gone.Address != dead || gone.Up || gone.ProbedDown || gone.Failed == 0 {
		t.Errorf("the dead backend: %+v", gone)
	}
	if len(st.Certs) != 1 || st.Certs[0].Listener != "secure" || strings.Join(st.Certs[0].Names, ",") != "a.example" ||
		st.Certs[0].Serial == "" || !st.Certs[0].NotAfter.After(st.Certs[0].NotBefore) {
		t.Errorf("certificates: %+v", st.Certs)
	}
}

func TestDashboard(t *testing.T) {
	f := start(t, doc("v1"), &config.Admin{})
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noRedirect.Get(f.api + "/dashboard")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMovedPermanently || resp.Header.Get("Location") != "/dashboard/" {
		t.Errorf("/dashboard: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}

	resp, body := f.get(t, "/dashboard/")
	if resp.StatusCode != 200 || !strings.Contains(body, "<title>fit dashboard</title>") {
		t.Fatalf("/dashboard/: %d %.200s", resp.StatusCode, body)
	}
	for k, v := range map[string]string{
		"Content-Security-Policy": dashboardCSP,
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"Content-Type":            "text/html; charset=utf-8",
	} {
		if got := resp.Header.Get(k); got != v {
			t.Errorf("%s: %q", k, got)
		}
	}
	// Every file the page names is served.
	for _, m := range regexp.MustCompile(`(?:href|src)="([^"#:]+)"`).FindAllStringSubmatch(body, -1) {
		resp, _ := f.get(t, "/dashboard/"+m[1])
		if resp.StatusCode != 200 {
			t.Errorf("%s: %d", m[1], resp.StatusCode)
		}
	}
	if resp, _ := f.get(t, "/dashboard/app.js"); !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/javascript") {
		t.Errorf("app.js: %q", resp.Header.Get("Content-Type"))
	}
	if resp, _ := f.get(t, "/dashboard/nothing.js"); resp.StatusCode != 404 {
		t.Errorf("a file that is not there: %d", resp.StatusCode)
	}
	req, _ := http.NewRequest("POST", f.api+"/dashboard/", strings.NewReader("{}"))
	if resp, _ := f.do(t, req); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST /dashboard/: %d", resp.StatusCode)
	}

	// The dashboard's own requests carry its origin, which may read but
	// not write; another origin may do neither.
	for _, tt := range []struct {
		method, path, origin string
		status               int
	}{
		{"GET", "/status", f.api, 200},
		{"GET", "/config/effective", f.api, 200},
		{"GET", "/versions", strings.Replace(f.api, "127.0.0.1", "localhost", 1), 403},
		{"GET", "/status", "http://evil.example.com", 403},
		{"POST", "/load", f.api, 403},
		{"DELETE", "/config/routes/0", f.api, 403},
	} {
		req, _ := http.NewRequest(tt.method, f.api+tt.path, nil)
		req.Header.Set("Origin", tt.origin)
		req.Header.Set("Content-Type", "application/json")
		if resp, body := f.do(t, req); resp.StatusCode != tt.status {
			t.Errorf("%s %s from %s: %d %s", tt.method, tt.path, tt.origin, resp.StatusCode, body)
		}
	}
}

func TestDashboardToken(t *testing.T) {
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := start(t, doc("v1"), &config.Admin{TokenFile: file})
	// The files hold no data, and are served without the token; the page
	// asks for it.
	for _, p := range []string{"/dashboard/", "/dashboard/app.js", "/dashboard/app.css"} {
		if resp, _ := f.get(t, p); resp.StatusCode != 200 {
			t.Errorf("%s without the token: %d", p, resp.StatusCode)
		}
	}
	if resp, _ := f.get(t, "/status"); resp.StatusCode != 401 {
		t.Errorf("/status without the token: %d", resp.StatusCode)
	}
	req, _ := http.NewRequest("GET", f.api+"/status", nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	if resp, _ := f.do(t, req); resp.StatusCode != 200 {
		t.Errorf("/status with the token: %d", resp.StatusCode)
	}
	// Writes under /dashboard/ are no way around the token.
	req, _ = http.NewRequest("PUT", f.api+"/dashboard/x", nil)
	if resp, _ := f.do(t, req); resp.StatusCode != 401 {
		t.Errorf("PUT /dashboard/x without the token: %d", resp.StatusCode)
	}
}

func TestDashboardDisabled(t *testing.T) {
	f := start(t, doc("v1"), &config.Admin{DisableDashboard: true})
	if resp, _ := f.get(t, "/dashboard/"); resp.StatusCode != 404 {
		t.Errorf("/dashboard/ when disabled: %d", resp.StatusCode)
	}
	if resp, _ := f.get(t, "/status"); resp.StatusCode != 200 {
		t.Errorf("/status when the dashboard is disabled: %d", resp.StatusCode)
	}
}

// TestDashboardReadOnly checks the dashboard's files: they send nothing but
// GETs, have no forms that post, and fetch nothing from elsewhere.
func TestDashboardReadOnly(t *testing.T) {
	err := fs.WalkDir(dashboardFiles, "dashboard", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := dashboardFiles.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(data)
		for _, m := range regexp.MustCompile(`\b(POST|PUT|PATCH|DELETE)\b`).FindAllString(text, -1) {
			t.Errorf("%s names method %s", path, m)
		}
		for _, m := range regexp.MustCompile(`method:\s*"([^"]*)"`).FindAllStringSubmatch(text, -1) {
			if m[1] != "GET" {
				t.Errorf("%s sends %s", path, m[1])
			}
		}
		if strings.Contains(strings.ToLower(text), `method="post"`) {
			t.Errorf("%s has a form that posts", path)
		}
		if regexp.MustCompile(`(?i)\b(https?:)?//[a-z0-9]`).MatchString(regexp.MustCompile(`http://www\.w3\.org/2000/svg`).ReplaceAllString(text, "")) {
			t.Errorf("%s names another origin", path)
		}
		if strings.HasSuffix(path, ".js") && strings.Contains(text, "innerHTML") {
			t.Errorf("%s writes HTML from data", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
