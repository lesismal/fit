//go:build !windows

package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	stdhttp "net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// phpImage is the PHP-FPM the fastcgi handler is tested against.
const phpImage = "php:8.3-fpm-alpine"

var phpScripts = map[string]string{
	"hello.php": `<?php echo "hello from ", PHP_SAPI;`,
	"info.php": `<?php header('Content-Type: application/json');
echo json_encode([
  'script' => $_SERVER['SCRIPT_NAME'], 'filename' => $_SERVER['SCRIPT_FILENAME'],
  'path_info' => $_SERVER['PATH_INFO'] ?? '', 'query' => $_GET, 'method' => $_SERVER['REQUEST_METHOD'],
  'uri' => $_SERVER['REQUEST_URI'], 'header' => $_SERVER['HTTP_X_TEST'] ?? '', 'host' => $_SERVER['HTTP_HOST'] ?? '',
  'remote' => $_SERVER['REMOTE_ADDR'] ?? '', 'proxy' => $_SERVER['HTTP_PROXY'] ?? '',
]);`,
	"post.php":     `<?php echo $_POST['name'] ?? '', '|', strlen(file_get_contents('php://input')), '|', md5(file_get_contents('php://input'));`,
	"status.php":   `<?php http_response_code(418); header('X-Teapot: yes'); setcookie('a', 'b'); echo 'teapot';`,
	"redirect.php": `<?php header('Location: /hello.php');`,
	"big.php":      `<?php for ($i = 0; $i < 1024; $i++) { echo str_repeat('x', 4096); }`,
	"index.php":    `<?php echo "front ", $_SERVER['REQUEST_URI'];`,
}

// TestPHPFPM serves PHP scripts from PHP-FPM through the fastcgi handler:
// its parameters, path info and query, POST forms and bodies, status,
// fields and cookies, redirects, a large response, and a front controller
// through try_files.
func TestPHPFPM(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, src := range phpScripts {
		if err := os.WriteFile(filepath.Join(root, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The scripts are at the same path in the container as here, so that
	// SCRIPT_FILENAME and try_files agree.
	fpm := startServerWith(t, phpImage, 9000, []string{"-v", root + ":" + root + ":ro"})
	// One listener: the handler's address is not one.
	_, addrs := startFitWant(t, fmt.Sprintf(`{"listeners": [{"name": "web", "address": "127.0.0.1:0", "protocols": ["http/1.1", "h2c"]}],
  "routes": [{"handler": {"module": "fastcgi", "params": {"address": %q, "root": %q,
    "try_files": ["{path}", "{path}/index.php", "/index.php"]}}}]}`, fpm, root), 1)
	base := "http://" + addrs["web"]

	for _, h2c := range []bool{false, true} {
		c := client(h2c)
		c.CheckRedirect = func(*stdhttp.Request, []*stdhttp.Request) error { return stdhttp.ErrUseLastResponse }
		fetch := func(req *stdhttp.Request) (*stdhttp.Response, string) {
			t.Helper()
			resp, err := c.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			return resp, string(body)
		}
		// PHP-FPM takes a moment to start.
		deadline := time.Now().Add(30 * time.Second)
		for {
			req, _ := stdhttp.NewRequest("GET", base+"/hello.php", nil)
			resp, body := fetch(req)
			if resp.StatusCode == 200 {
				if body != "hello from fpm-fcgi" {
					t.Fatalf("hello: %q", body)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("PHP-FPM did not answer: %d %s", resp.StatusCode, body)
			}
			time.Sleep(200 * time.Millisecond)
		}

		req, _ := stdhttp.NewRequest("GET", base+"/info.php/a/b?x=1&y=two", nil)
		req.Header.Set("X-Test", "yes")
		req.Header.Set("Proxy", "http://evil.test")
		_, body := fetch(req)
		var info map[string]any
		if err := json.Unmarshal([]byte(body), &info); err != nil {
			t.Fatalf("%v: %s", err, body)
		}
		for k, want := range map[string]any{
			"script": "/info.php", "filename": root + "/info.php", "path_info": "/a/b", "method": "GET",
			"uri": "/info.php/a/b?x=1&y=two", "header": "yes", "host": addrs["web"], "remote": "127.0.0.1", "proxy": "",
		} {
			if info[k] != want {
				t.Errorf("h2c %v: %s = %v, want %v", h2c, k, info[k], want)
			}
		}
		if q, _ := info["query"].(map[string]any); q["x"] != "1" || q["y"] != "two" {
			t.Errorf("query: %v", info["query"])
		}

		form := url.Values{"name": {"fit"}}.Encode()
		req, _ = stdhttp.NewRequest("POST", base+"/post.php", strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if _, body := fetch(req); !strings.HasPrefix(body, "fit|"+fmt.Sprint(len(form))+"|") {
			t.Errorf("a form: %q", body)
		}
		upload := strings.Repeat("0123456789", 300000) // 3 MB
		req, _ = stdhttp.NewRequest("POST", base+"/post.php", strings.NewReader(upload))
		req.Header.Set("Content-Type", "application/octet-stream")
		if _, body := fetch(req); !strings.HasPrefix(body, "|3000000|") {
			t.Errorf("an upload: %q", body)
		}

		req, _ = stdhttp.NewRequest("GET", base+"/status.php", nil)
		resp, body := fetch(req)
		if resp.StatusCode != 418 || resp.Header.Get("X-Teapot") != "yes" || !strings.HasPrefix(resp.Header.Get("Set-Cookie"), "a=b") || body != "teapot" {
			t.Errorf("status: %d %v %q", resp.StatusCode, resp.Header, body)
		}
		req, _ = stdhttp.NewRequest("GET", base+"/redirect.php", nil)
		if resp, _ := fetch(req); resp.StatusCode != 302 || resp.Header.Get("Location") != "/hello.php" {
			t.Errorf("redirect: %d %q", resp.StatusCode, resp.Header.Get("Location"))
		}
		req, _ = stdhttp.NewRequest("GET", base+"/big.php", nil)
		if _, body := fetch(req); len(body) != 4<<20 {
			t.Errorf("big: %d bytes", len(body))
		}
		req, _ = stdhttp.NewRequest("GET", base+"/blog/post?id=7", nil)
		if _, body := fetch(req); body != "front /blog/post?id=7" {
			t.Errorf("front controller: %q", body)
		}
	}
}
