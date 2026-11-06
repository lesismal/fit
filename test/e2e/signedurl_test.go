//go:build !windows

package e2e

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/mediaauth"
)

// The access log never holds a signed URL's signature: the line of a
// request let in by one shows sig=REDACTED, and the viewer as its user.
func TestSignedURLAccessLog(t *testing.T) {
	lib, err := filepath.Abs("../../handler/vod/testdata/lib")
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "access.log")
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	const secret = "the site's secret, 16 bytes or more"
	f, addrs := startFit(t, `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "access_log": {"output": `+q(logPath)+`, "flush_interval": "1h"},
  "media_library": [{"name": "films", "roots": [`+q(lib)+`]}],
  "media_auth": [{"name": "site", "secret": `+q(secret)+`, "url_signing": {}}],
  "routes": [{"match": {"path": ["/vod/*"]}, "handler": {"module": "vod", "params": {"library": "films", "auth": "site"}}}]
}`)
	p, err := mediaauth.New(&config.MediaAuth{Name: "site", Secret: secret, URLSigning: &config.URLSigning{}})
	if err != nil {
		t.Fatal(err)
	}
	query, err := p.SignURL("/vod/films/films/movie/", mediaauth.Viewer{Subject: "viewer-7"}, time.Now().Add(time.Hour), "")
	if err != nil {
		t.Fatal(err)
	}
	sig := query[strings.Index(query, "sig=")+len("sig="):]
	url := "http://" + addrs["web"] + "/vod/films/films/movie/index.m3u8?" + query
	// The library's first scan may not have ended: 503 until it has.
	deadline := time.Now().Add(20 * time.Second)
	for {
		resp, err := client(false).Get(url)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == 200 {
			break
		}
		if resp.StatusCode != 503 || time.Now().After(deadline) {
			t.Fatalf("the signed URL: %d %s", resp.StatusCode, body)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if code, out := f.stop(t); code != 0 {
		t.Fatalf("stop: exit %d, output %q", code, out)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), sig) {
		t.Fatalf("the access log holds the signature:\n%s", data)
	}
	// The line of the request let in: the access log's shards are written
	// one after another, so it need not be the last line, after the 503s
	// of the first scan.
	var let map[string]any
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatal(err)
		}
		if m["status"] == 200.0 {
			let = m
		}
	}
	if uri, _ := let["uri"].(string); !strings.Contains(uri, "sig=REDACTED") || let["user"] != "viewer-7" {
		t.Fatalf("the line of the request let in: %v\n%s", let, data)
	}
}
