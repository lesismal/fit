package static

import (
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/fibtest"
	"github.com/lesismal/fit/module"
)

func load(t *testing.T, params string) (*Static, error) {
	t.Helper()
	v, err := config.ParseValue([]byte(`{"module": "static", "params": `+params+`}`), "handler")
	if err != nil {
		t.Fatal(err)
	}
	var ref config.ModuleRef
	if err := v.Decode(&ref); err != nil {
		t.Fatal(err)
	}
	m, err := module.Load(module.Handlers, ref, nil)
	if err != nil {
		return nil, err
	}
	return m.(*Static), nil
}

// tree writes files, by slash-separated name, under a new directory, and
// returns it.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, data := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func mustLoad(t *testing.T, root, extra string) *Static {
	t.Helper()
	params := `{"root": ` + quote(root) + extra + `}`
	s, err := load(t, params)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func quote(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}

func get(s *Static, method, target string, header map[string]string) *fibtest.Recorder {
	r := httptest.NewRequest(method, target, nil)
	for k, v := range header {
		r.Header.Set(k, v)
	}
	return fibtest.ServeRequest(s, r)
}

func TestServe(t *testing.T) {
	root := tree(t, map[string]string{
		"hello.txt":         "hello\n",
		"style.css":         "body{}",
		"noext":             "<html><body>x</body></html>",
		"empty.txt":         "",
		"docs/index.html":   "<p>docs</p>",
		"docs/a b.txt":      "spaced",
		"nested/deep/x.js":  "1",
		"other/default.htm": "default",
		"plain/readme.txt":  "no index here",
	})
	s := mustLoad(t, root, `, "index": ["index.html", "default.htm"]`)
	tests := []struct {
		method, target string
		status         int
		contentType    string
		body           string
		location       string
	}{
		{"GET", "/hello.txt", 200, "text/plain; charset=utf-8", "hello\n", ""},
		{"HEAD", "/hello.txt", 200, "text/plain; charset=utf-8", "hello\n", ""},
		{"GET", "/style.css", 200, "text/css; charset=utf-8", "body{}", ""},
		{"GET", "/noext", 200, "text/html; charset=utf-8", "<html><body>x</body></html>", ""},
		{"GET", "/empty.txt", 200, "text/plain; charset=utf-8", "", ""},
		{"GET", "/nested/deep/x.js", 200, "text/javascript; charset=utf-8", "1", ""},
		{"GET", "/docs/a%20b.txt", 200, "text/plain; charset=utf-8", "spaced", ""},
		{"GET", "/docs/", 200, "text/html; charset=utf-8", "<p>docs</p>", ""},
		{"GET", "/other/", 200, "text/html; charset=utf-8", "default", ""},
		{"GET", "/docs", 301, "text/plain; charset=utf-8", "301 Moved Permanently\n", "/docs/"},
		{"GET", "/docs?a=1", 301, "text/plain; charset=utf-8", "301 Moved Permanently\n", "/docs/?a=1"},
		{"GET", "/plain/", 404, "text/plain; charset=utf-8", "404 Not Found\n", ""},
		{"GET", "/missing.txt", 404, "text/plain; charset=utf-8", "404 Not Found\n", ""},
		{"GET", "/hello.txt/", 404, "text/plain; charset=utf-8", "404 Not Found\n", ""},
		{"GET", "/./hello.txt", 200, "text/plain; charset=utf-8", "hello\n", ""},
		{"GET", "//hello.txt", 200, "text/plain; charset=utf-8", "hello\n", ""},
		{"POST", "/hello.txt", 405, "text/plain; charset=utf-8", "405 Method Not Allowed\n", ""},
	}
	for _, tt := range tests {
		// Twice: the second time from the cache.
		for range 2 {
			rec := get(s, tt.method, tt.target, nil)
			got := rec.Response
			if !rec.Written || got.StatusCode != tt.status || got.Header.Get("Content-Type") != tt.contentType ||
				string(got.Body) != tt.body || got.Header.Get("Location") != tt.location {
				t.Fatalf("%s %s: got %d %v %q", tt.method, tt.target, got.StatusCode, got.Header, got.Body)
			}
			if tt.status == 200 && (got.Header.Get("Etag") == "" || got.Header.Get("Last-Modified") == "" ||
				got.Header.Get("Accept-Ranges") != "bytes") {
				t.Fatalf("%s %s: validators missing: %v", tt.method, tt.target, got.Header)
			}
			if tt.status == 405 && got.Header.Get("Allow") != "GET, HEAD" {
				t.Fatalf("%s %s: Allow = %q", tt.method, tt.target, got.Header.Get("Allow"))
			}
		}
	}
}

func TestTraversal(t *testing.T) {
	outer := t.TempDir()
	if err := os.WriteFile(filepath.Join(outer, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(outer, "www")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ok.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := mustLoad(t, root, "")
	for _, target := range []string{
		"/../secret.txt",
		"/a/../../secret.txt",
		"/%2e%2e/secret.txt",
		"/..%2fsecret.txt",
		"/..%5csecret.txt",
		"/ok.txt%00",
		"/..",
	} {
		rec := get(s, "GET", target, nil)
		if rec.Response.StatusCode != 400 || strings.Contains(string(rec.Response.Body), "secret") {
			t.Errorf("%s: got %d %q", target, rec.Response.StatusCode, rec.Response.Body)
		}
	}
	// A path with a ".." segment that would stay inside is refused too.
	if rec := get(s, "GET", "/x/../ok.txt", nil); rec.Response.StatusCode != 400 {
		t.Errorf("/x/../ok.txt: got %d", rec.Response.StatusCode)
	}
	// Dots that are not a whole segment are names like any other.
	if err := os.WriteFile(filepath.Join(root, "..dots"), []byte("dots"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rec := get(s, "GET", "/..dots", nil); rec.Response.StatusCode != 200 || string(rec.Response.Body) != "dots" {
		t.Errorf("/..dots: got %d %q", rec.Response.StatusCode, rec.Response.Body)
	}
}

func TestConditional(t *testing.T) {
	root := tree(t, map[string]string{"a.txt": "0123456789"})
	modTime := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(root, "a.txt"), modTime, modTime); err != nil {
		t.Fatal(err)
	}
	for _, extra := range []string{"", `, "max_file_bytes": 4`} {
		s := mustLoad(t, root, extra)
		first := get(s, "GET", "/a.txt", nil).Response
		tag := first.Header.Get("Etag")
		lastModified := first.Header.Get("Last-Modified")
		if !strings.HasPrefix(tag, `"`) || lastModified != "Fri, 02 Jan 2026 03:04:05 GMT" {
			t.Fatalf("%s: validators %q %q", extra, tag, lastModified)
		}
		tests := []struct {
			name   string
			header map[string]string
			status int
			body   string
		}{
			{"if-none-match hit", map[string]string{"If-None-Match": tag}, 304, ""},
			{"if-none-match list", map[string]string{"If-None-Match": `"x", ` + tag}, 304, ""},
			{"if-none-match star", map[string]string{"If-None-Match": "*"}, 304, ""},
			{"if-none-match miss", map[string]string{"If-None-Match": `"other"`}, 200, "0123456789"},
			{"if-modified-since same", map[string]string{"If-Modified-Since": lastModified}, 304, ""},
			{"if-modified-since later", map[string]string{"If-Modified-Since": "Sat, 03 Jan 2026 00:00:00 GMT"}, 304, ""},
			{"if-modified-since earlier", map[string]string{"If-Modified-Since": "Thu, 01 Jan 2026 00:00:00 GMT"}, 200, "0123456789"},
			{"if-none-match wins", map[string]string{"If-None-Match": `"other"`, "If-Modified-Since": lastModified}, 200, "0123456789"},
			{"if-match miss", map[string]string{"If-Match": `"other"`}, 412, ""},
			{"if-match hit", map[string]string{"If-Match": tag}, 200, "0123456789"},
			{"if-unmodified-since earlier", map[string]string{"If-Unmodified-Since": "Thu, 01 Jan 2026 00:00:00 GMT"}, 412, ""},
			{"range", map[string]string{"Range": "bytes=2-4"}, 206, "234"},
			{"range suffix", map[string]string{"Range": "bytes=-3"}, 206, "789"},
			{"range open", map[string]string{"Range": "bytes=7-"}, 206, "789"},
			{"range unsatisfiable", map[string]string{"Range": "bytes=20-30"}, 416, ""},
			{"if-range etag hit", map[string]string{"Range": "bytes=0-1", "If-Range": tag}, 206, "01"},
			{"if-range etag miss", map[string]string{"Range": "bytes=0-1", "If-Range": `"other"`}, 200, "0123456789"},
			{"if-range date hit", map[string]string{"Range": "bytes=0-1", "If-Range": lastModified}, 206, "01"},
		}
		for _, tt := range tests {
			got := get(s, "GET", "/a.txt", tt.header).Response
			body := string(got.Body)
			if got.StatusCode == 412 || got.StatusCode == 416 {
				body = "" // ServeContent's error text
			}
			if got.StatusCode != tt.status || body != tt.body {
				t.Errorf("%s%s: got %d %q", tt.name, extra, got.StatusCode, got.Body)
			}
			if tt.status == 206 && got.Header.Get("Content-Range") == "" {
				t.Errorf("%s%s: no Content-Range", tt.name, extra)
			}
			if tt.status == 304 && got.Header.Get("Etag") != tag {
				t.Errorf("%s%s: 304 without the ETag", tt.name, extra)
			}
		}
		multi := get(s, "GET", "/a.txt", map[string]string{"Range": "bytes=0-1,5-6"}).Response
		if multi.StatusCode != 206 || !strings.HasPrefix(multi.Header.Get("Content-Type"), "multipart/byteranges") {
			t.Errorf("multiple ranges%s: got %d %v", extra, multi.StatusCode, multi.Header)
		}
	}
}

func TestChangedFile(t *testing.T) {
	root := tree(t, map[string]string{"a.txt": "one"})
	s := mustLoad(t, root, "")
	first := get(s, "GET", "/a.txt", nil).Response
	if string(first.Body) != "one" {
		t.Fatalf("got %q", first.Body)
	}
	full := filepath.Join(root, "a.txt")
	if err := os.WriteFile(full, []byte("three"), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(full, later, later); err != nil {
		t.Fatal(err)
	}
	second := get(s, "GET", "/a.txt", nil).Response
	if string(second.Body) != "three" || second.Header.Get("Etag") == first.Header.Get("Etag") {
		t.Fatalf("after a change: got %q %v", second.Body, second.Header)
	}
	if err := os.Remove(full); err != nil {
		t.Fatal(err)
	}
	if got := get(s, "GET", "/a.txt", nil).Response; got.StatusCode != 404 {
		t.Fatalf("after removal: got %d", got.StatusCode)
	}
	if s.cache.bytes != 0 || len(s.cache.entries) != 0 {
		t.Fatalf("cache still holds %d bytes in %d entries", s.cache.bytes, len(s.cache.entries))
	}
}

func TestCacheLimits(t *testing.T) {
	root := tree(t, map[string]string{"a.txt": "aaaa", "b.txt": "bbbb", "big.txt": "0123456789"})
	s := mustLoad(t, root, `, "max_file_bytes": 8, "max_cache_bytes": 6`)
	for _, name := range []string{"a.txt", "b.txt", "big.txt"} {
		for range 2 {
			got := get(s, "GET", "/"+name, nil).Response
			if got.StatusCode != 200 || len(got.Body) == 0 || got.Header.Get("Etag") == "" ||
				got.Header.Get("Content-Type") != "text/plain; charset=utf-8" {
				t.Fatalf("%s: got %d %v %q", name, got.StatusCode, got.Header, got.Body)
			}
		}
	}
	if _, ok := s.cache.entries[filepath.Join(s.root, "a.txt")]; !ok || len(s.cache.entries) != 1 || s.cache.bytes != 4 {
		t.Fatalf("cache holds %d entries, %d bytes", len(s.cache.entries), s.cache.bytes)
	}
}

func TestStripPrefix(t *testing.T) {
	root := tree(t, map[string]string{"a.txt": "a", "sub/index.html": "i"})
	s := mustLoad(t, root, `, "strip_prefix": "/assets"`)
	tests := []struct {
		target   string
		status   int
		location string
	}{
		{"/assets/a.txt", 200, ""},
		{"/assets/", 404, ""}, // the root has no index.html
		{"/assets/sub", 301, "/assets/sub/"},
		{"/assets/sub/", 200, ""},
		{"/a.txt", 404, ""},
		{"/assets/../a.txt", 400, ""},
	}
	for _, tt := range tests {
		got := get(s, "GET", tt.target, nil).Response
		if got.StatusCode != tt.status || got.Header.Get("Location") != tt.location {
			t.Errorf("%s: got %d %v", tt.target, got.StatusCode, got.Header)
		}
	}
}

func TestNoIndex(t *testing.T) {
	root := tree(t, map[string]string{"index.html": "i"})
	s := mustLoad(t, root, `, "index": []`)
	if got := get(s, "GET", "/", nil).Response; got.StatusCode != 404 {
		t.Fatalf("got %d", got.StatusCode)
	}
}

func TestPermission(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads everything")
	}
	root := tree(t, map[string]string{"locked/a.txt": "a"})
	locked := filepath.Join(root, "locked")
	if err := os.Chmod(locked, 0); err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	s := mustLoad(t, root, "")
	if got := get(s, "GET", "/locked/a.txt", nil).Response; got.StatusCode != stdhttp.StatusForbidden {
		t.Fatalf("got %d", got.StatusCode)
	}
}

func TestErrors(t *testing.T) {
	root := tree(t, map[string]string{"file.txt": "x"})
	file := filepath.Join(root, "file.txt")
	tests := []struct {
		params string
		want   string
	}{
		{`{}`, `handler.params.root: missing`},
		{`{"root": ` + quote(file) + `}`, `is not a directory`},
		{`{"root": ` + quote(filepath.Join(root, "none")) + `}`, `no such file or directory`},
		{`{"root": ` + quote(root) + `, "index": ["a/b"]}`, `handler.params.index: "a/b" is not a file name`},
		{`{"root": ` + quote(root) + `, "index": [".."]}`, `handler.params.index: ".." is not a file name`},
		{`{"root": ` + quote(root) + `, "strip_prefix": "assets"}`, `handler.params.strip_prefix: "assets" does not start with /`},
		{`{"root": ` + quote(root) + `, "max_file_bytes": -1}`, `handler.params.max_file_bytes: -1 is less than the minimum 0`},
	}
	for _, tt := range tests {
		_, err := load(t, tt.params)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: error = %v, want %s", tt.params, err, tt.want)
		}
	}
}

func BenchmarkServeCached(b *testing.B) {
	root := b.TempDir()
	if err := os.WriteFile(filepath.Join(root, "small.html"), []byte(strings.Repeat("x", 1024)), 0o644); err != nil {
		b.Fatal(err)
	}
	v, _ := config.ParseValue([]byte(`{"module": "static", "params": {"root": `+quote(root)+`}}`), "handler")
	var ref config.ModuleRef
	if err := v.Decode(&ref); err != nil {
		b.Fatal(err)
	}
	m, err := module.Load(module.Handlers, ref, nil)
	if err != nil {
		b.Fatal(err)
	}
	s := m.(*Static)
	r := httptest.NewRequest("GET", "/small.html", nil)
	b.ReportAllocs()
	for b.Loop() {
		fibtest.ServeRequest(s, r)
	}
}
