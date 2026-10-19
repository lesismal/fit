//go:build !windows

package e2e

import (
	"encoding/json"
	"io"
	stdhttp "net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStaticServesFiles(t *testing.T) {
	root := t.TempDir()
	small := "hello, static\n"
	large := strings.Repeat("0123456789abcdef", 1<<16) // 1 MiB, past max_file_bytes below
	for name, data := range map[string]string{"small.txt": small, "large.bin": large, "site/index.html": "<p>index</p>"} {
		full := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rootJSON, _ := json.Marshal(root)
	_, addrs := startFit(t, `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0", "protocols": ["http/1.1", "h2c"]}],
  "routes": [
    {"match": {"path": ["/files/*"]}, "handler": {"module": "static", "params": {"root": `+string(rootJSON)+`, "strip_prefix": "/files", "max_file_bytes": 65536}}}
  ]
}`)
	base := "http://" + addrs["web"] + "/files"
	for _, h2c := range []bool{false, true} {
		c := client(h2c)
		do := func(method, path string, header map[string]string) (*stdhttp.Response, string) {
			t.Helper()
			req, _ := stdhttp.NewRequest(method, base+path, nil)
			for k, v := range header {
				req.Header.Set(k, v)
			}
			resp, err := c.Do(req)
			if err != nil {
				t.Fatalf("h2c=%v %s %s: %v", h2c, method, path, err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return resp, string(body)
		}
		resp, body := do("GET", "/small.txt", nil)
		tag := resp.Header.Get("Etag")
		if resp.StatusCode != 200 || body != small || tag == "" || resp.ContentLength != int64(len(small)) {
			t.Fatalf("h2c=%v small: %d %q %v", h2c, resp.StatusCode, body, resp.Header)
		}
		if resp, body = do("HEAD", "/small.txt", nil); resp.StatusCode != 200 || body != "" || resp.ContentLength != int64(len(small)) {
			t.Errorf("h2c=%v HEAD: %d %q %d", h2c, resp.StatusCode, body, resp.ContentLength)
		}
		if resp, _ = do("GET", "/small.txt", map[string]string{"If-None-Match": tag}); resp.StatusCode != 304 {
			t.Errorf("h2c=%v If-None-Match: %d", h2c, resp.StatusCode)
		}
		if resp, body = do("GET", "/small.txt", map[string]string{"Range": "bytes=7-12"}); resp.StatusCode != 206 || body != "static" {
			t.Errorf("h2c=%v range: %d %q", h2c, resp.StatusCode, body)
		}
		if resp, body = do("GET", "/large.bin", nil); resp.StatusCode != 200 || body != large {
			t.Errorf("h2c=%v large: %d, %d bytes", h2c, resp.StatusCode, len(body))
		}
		if resp, body = do("GET", "/large.bin", map[string]string{"Range": "bytes=16-31"}); resp.StatusCode != 206 || body != "0123456789abcdef" {
			t.Errorf("h2c=%v large range: %d %q", h2c, resp.StatusCode, body)
		}
		if resp, body = do("GET", "/site/", nil); resp.StatusCode != 200 || body != "<p>index</p>" {
			t.Errorf("h2c=%v index: %d %q", h2c, resp.StatusCode, body)
		}
		noRedirect := *c
		noRedirect.CheckRedirect = func(*stdhttp.Request, []*stdhttp.Request) error { return stdhttp.ErrUseLastResponse }
		req, _ := stdhttp.NewRequest("GET", base+"/site", nil)
		if resp, err := noRedirect.Do(req); err != nil || resp.StatusCode != 301 || resp.Header.Get("Location") != "/files/site/" {
			t.Errorf("h2c=%v redirect: %v %v", h2c, err, resp)
		} else {
			resp.Body.Close()
		}
		// The client cleans "..", so the request goes out as written.
		req, _ = stdhttp.NewRequest("GET", base+"/x", nil)
		req.URL.Opaque = "/files/../../etc/passwd"
		if resp, err := c.Do(req); err != nil || resp.StatusCode != 400 {
			t.Errorf("h2c=%v traversal: %v %v", h2c, err, resp)
		} else {
			resp.Body.Close()
		}
	}
}
