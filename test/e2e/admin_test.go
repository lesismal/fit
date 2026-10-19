//go:build !windows

package e2e

import (
	"encoding/json"
	"io"
	stdhttp "net/http"
	"strings"
	"testing"
	"time"
)

func adminConfig(body string) string {
	return `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "routes": [{"handler": {"module": "respond", "params": {"body": "` + body + `"}}}],
  "admin": {"address": "127.0.0.1:0", "history": 3},
  "metrics": {}
}`
}

func adminDo(t *testing.T, method, url, body string) (*stdhttp.Response, string) {
	t.Helper()
	req, err := stdhttp.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client(false).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(data)
}

func get(t *testing.T, url string) string {
	t.Helper()
	resp, body := adminDo(t, "GET", url, "")
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s: %d %s", url, resp.StatusCode, body)
	}
	return body
}

// TestAdminLoad loads a config through the Admin API, reads it back, and
// sees the server move to it; then SIGHUP takes the file's config back.
func TestAdminLoad(t *testing.T) {
	first := adminConfig("v1")
	f, addrs := startFit(t, first)
	api := "http://" + addrs["admin"]
	web := "http://" + addrs["web"] + "/"

	if got := get(t, api+"/config"); got != first {
		t.Fatalf("GET /config = %q, want the file", got)
	}
	if got := get(t, web); got != "v1" {
		t.Fatalf("before: %q", got)
	}

	// The same listener, which is kept, and one more.
	second := strings.Replace(adminConfig("v2"), `"address": "127.0.0.1:0"}]`,
		`"address": "127.0.0.1:0"}, {"name": "extra", "address": "127.0.0.1:0"}]`, 1)
	resp, body := adminDo(t, "POST", api+"/load", second)
	if resp.StatusCode != 200 {
		t.Fatalf("POST /load: %d %s", resp.StatusCode, body)
	}
	var loaded struct {
		Version uint64 `json:"version"`
		Added   []struct {
			Name, Address string
		} `json:"listeners_added"`
	}
	if err := json.Unmarshal([]byte(body), &loaded); err != nil {
		t.Fatal(err)
	}
	if loaded.Version != 2 || len(loaded.Added) != 1 || loaded.Added[0].Name != "extra" || resp.Header.Get("Fit-Config-Version") != "2" {
		t.Fatalf("POST /load: %s %v", body, resp.Header)
	}
	f.waitLine(t, `fit: listener "extra" on `)
	f.waitLine(t, "fit: loaded a config from the admin API (config version 2)")

	if got := get(t, api+"/config"); got != second {
		t.Fatalf("GET /config = %q, want what was loaded", got)
	}
	if got := get(t, web); got != "v2" {
		t.Fatalf("after: %q", got)
	}
	if got := get(t, "http://"+loaded.Added[0].Address+"/"); got != "v2" {
		t.Fatalf("the added listener: %q", got)
	}
	if got := get(t, api+"/versions/1"); got != first {
		t.Fatalf("GET /versions/1 = %q", got)
	}

	// A broken config is refused with its errors, and nothing changes.
	resp, body = adminDo(t, "POST", api+"/load", `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}], "routes": [{"handler": {"module": "nope"}}]}`)
	if resp.StatusCode != 400 || !strings.Contains(body, `there is no module \"nope\"`) {
		t.Fatalf("broken config: %d %s", resp.StatusCode, body)
	}
	if got := get(t, web); got != "v2" {
		t.Fatalf("after the broken config: %q", got)
	}

	// SIGHUP applies the file again, through the same path.
	f.reload(t, first)
	f.waitLine(t, "fit: reloaded")
	if got := get(t, api+"/config"); got != first {
		t.Fatalf("GET /config after SIGHUP = %q", got)
	}
	var versions struct {
		Current  uint64 `json:"current"`
		Versions []struct {
			Version uint64 `json:"version"`
			Source  string `json:"source"`
		} `json:"versions"`
	}
	if err := json.Unmarshal([]byte(get(t, api+"/versions")), &versions); err != nil {
		t.Fatal(err)
	}
	if versions.Current != 3 || len(versions.Versions) != 3 || versions.Versions[1].Source != "admin" || versions.Versions[2].Source != "file" {
		t.Fatalf("GET /versions: %+v", versions)
	}
	if got := get(t, web); got != "v1" {
		t.Fatalf("after SIGHUP: %q", got)
	}

	// fit run counts requests, and the Admin API serves the counts with
	// the reloads: four requests to web answered 200, two reloads that
	// applied and one that failed.
	// A request is counted once its response has gone, which may be just
	// after the client has it.
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		metrics := get(t, api+"/metrics")
		var missing []string
		for _, want := range []string{
			`fit_http_requests_total{listener="web",route="#0",code="2xx"} 4`,
			`fit_config_reloads_total{result="success"} 2`,
			`fit_config_reloads_total{result="failure"} 1`,
			"fit_config_version 3",
			"# TYPE fit_http_request_duration_seconds histogram",
		} {
			if !strings.Contains(metrics, want+"\n") {
				missing = append(missing, want)
			}
		}
		if len(missing) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET /metrics has no %q:\n%s", missing, metrics)
		}
	}

	code, out := f.stop(t)
	if code != 0 {
		t.Fatalf("stop: exit %d %q", code, out)
	}
}
