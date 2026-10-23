//go:build !windows

package e2e

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestConfigDirectory runs fit on a directory of config files in its
// formats, with -watch: a file added is served, one taken out is no
// longer, and a broken one is reported by its name while the running
// config stays.
func TestConfigDirectory(t *testing.T) {
	dir := t.TempDir()
	write := func(name, doc string) {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("00-base.conf", "listener web {\n\taddress 127.0.0.1:0\n\tprotocols http/1.1 h2c\n}\n")
	write("routes/a.conf", "route a {\n\tmatch { path /a }\n\thandler respond { body a }\n}\n")
	f, addrs := startFitAt(t, dir, 1, "-watch", "-watch-interval", "50ms")
	get := func(path string) string {
		resp, err := client(false).Get("http://" + addrs["web"] + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		return resp.Status + " " + string(data)
	}
	waitGet := func(path, want string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for get(path) != want {
			if time.Now().After(deadline) {
				t.Fatalf("%s: %q, want %q", path, get(path), want)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	waitGet("/a", "200 OK a")

	write("routes/b.yaml", "routes:\n  - match: {path: [/b]}\n    handler: {module: respond, params: {body: b}}\n")
	waitGet("/b", "200 OK b")

	if err := os.Remove(filepath.Join(dir, "routes/a.conf")); err != nil {
		t.Fatal(err)
	}
	waitGet("/a", "404 Not Found 404 page not found\n")

	broken := filepath.Join(dir, "routes/c.conf")
	write("routes/c.conf", "route c {\n\thandler\n}\n")
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(f.stderr.String(), broken+":2:2: handler takes the name of a module") {
		if time.Now().After(deadline) {
			t.Fatalf("the broken file was not reported: %q", f.stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := get("/b"); got != "200 OK b" {
		t.Fatalf("after the broken file: %q", got)
	}
	if code, _ := f.stop(t); code != 0 {
		t.Fatalf("stop: exit %d", code)
	}
}
