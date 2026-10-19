package static

import (
	"strings"
	"testing"
)

func TestPrecompressed(t *testing.T) {
	root := tree(t, map[string]string{
		"app.js": "identity-js", "app.js.br": "br-js", "app.js.zst": "zstd-js", "app.js.gz": "gzip-js",
		"style.css": "identity-css", "style.css.gz": "gzip-css-0123456789",
		"plain.txt": "plain",
	})
	s := mustLoad(t, root, `, "precompressed": ["br", "zstd", "gzip"]`)
	tests := []struct {
		target, accept string
		encoding, body string
	}{
		{"/app.js", "gzip, br", "br", "br-js"}, // equal q: the first listed
		{"/app.js", "br;q=0.5, gzip", "gzip", "gzip-js"},
		{"/app.js", "zstd", "zstd", "zstd-js"},
		{"/app.js", "br;q=0, identity", "", "identity-js"},
		{"/app.js", "*", "br", "br-js"},
		{"/app.js", "*, br;q=0", "zstd", "zstd-js"},
		{"/app.js", "", "", "identity-js"},
		{"/style.css", "br, gzip", "gzip", "gzip-css-0123456789"}, // no br twin
		{"/plain.txt", "br, gzip", "", "plain"},
	}
	for _, tt := range tests {
		header := map[string]string{}
		if tt.accept != "" {
			header["Accept-Encoding"] = tt.accept
		}
		got := get(s, "GET", tt.target, header).Response
		if got.StatusCode != 200 || got.Header.Get("Content-Encoding") != tt.encoding || string(got.Body) != tt.body ||
			got.Header.Get("Vary") != "Accept-Encoding" {
			t.Errorf("%s %q: %d %v %q", tt.target, tt.accept, got.StatusCode, got.Header, got.Body)
		}
		if strings.HasPrefix(tt.target, "/app.js") && got.Header.Get("Content-Type") != "text/javascript; charset=utf-8" {
			t.Errorf("%s %q: Content-Type %q", tt.target, tt.accept, got.Header.Get("Content-Type"))
		}
	}

	// Each representation has its own ETag, and conditions apply to it.
	identity := get(s, "GET", "/app.js", nil).Response.Header.Get("Etag")
	br := get(s, "GET", "/app.js", map[string]string{"Accept-Encoding": "br"}).Response.Header.Get("Etag")
	if identity == br || br == "" {
		t.Fatalf("ETags %q and %q", identity, br)
	}
	if got := get(s, "GET", "/app.js", map[string]string{"Accept-Encoding": "br", "If-None-Match": br}).Response; got.StatusCode != 304 {
		t.Errorf("If-None-Match on the twin: %d", got.StatusCode)
	}
	got := get(s, "GET", "/style.css", map[string]string{"Accept-Encoding": "gzip", "Range": "bytes=0-3"}).Response
	if got.StatusCode != 206 || string(got.Body) != "gzip" || got.Header.Get("Content-Encoding") != "gzip" {
		t.Errorf("a range of the twin: %d %v %q", got.StatusCode, got.Header, got.Body)
	}

	// Twins too large to keep come from disk, with their length.
	s = mustLoad(t, root, `, "precompressed": ["gzip"], "max_file_bytes": 4`)
	got = get(s, "GET", "/style.css", map[string]string{"Accept-Encoding": "gzip"}).Response
	if got.StatusCode != 200 || string(got.Body) != "gzip-css-0123456789" || got.Header.Get("Content-Encoding") != "gzip" ||
		got.Header.Get("Content-Type") != "text/css; charset=utf-8" || got.Header.Get("Vary") != "Accept-Encoding" {
		t.Errorf("from disk: %d %v %q", got.StatusCode, got.Header, got.Body)
	}

	// Without precompressed, twins are files like any other.
	s = mustLoad(t, root, "")
	got = get(s, "GET", "/app.js", map[string]string{"Accept-Encoding": "br"}).Response
	if string(got.Body) != "identity-js" || got.Header.Get("Content-Encoding") != "" || got.Header.Get("Vary") != "" {
		t.Errorf("without precompressed: %v %q", got.Header, got.Body)
	}
}

func TestAcceptQ(t *testing.T) {
	for _, tt := range []struct {
		fields []string
		coding string
		want   float64
	}{
		{[]string{"gzip"}, "gzip", 1},
		{[]string{"GZIP;q=0.3"}, "gzip", 0.3},
		{[]string{"br", "gzip ; q=0.5"}, "gzip", 0.5},
		{[]string{"*;q=0.2"}, "zstd", 0.2},
		{[]string{"*;q=0.2, zstd;q=0"}, "zstd", 0},
		{[]string{"deflate"}, "gzip", 0},
		{[]string{"gzip;q=nope"}, "gzip", 1},
		{[]string{"gzip;q=2"}, "gzip", 1},
	} {
		if got := acceptQ(tt.fields, tt.coding); got != tt.want {
			t.Errorf("%q for %s: %v, want %v", tt.fields, tt.coding, got, tt.want)
		}
	}
}

func TestTryFiles(t *testing.T) {
	root := tree(t, map[string]string{
		"index.html": "app", "assets/a.js": "js", "docs/index.html": "docs", "plain/x.txt": "x",
	})
	s := mustLoad(t, root, `, "try_files": ["{path}", "{path}/", "/index.html"]`)
	for _, tt := range []struct {
		target string
		status int
		body   string
	}{
		{"/assets/a.js", 200, "js"},
		{"/docs", 200, "docs"}, // a directory, served by its index, not redirected
		{"/docs/", 200, "docs"},
		{"/some/client/route", 200, "app"},
		{"/plain/", 200, "app"}, // no index: falls through
		{"/", 200, "app"},
		{"/../secret", 400, "400 Bad Request\n"},
	} {
		got := get(s, "GET", tt.target, nil).Response
		if got.StatusCode != tt.status || string(got.Body) != tt.body || got.Header.Get("Location") != "" {
			t.Errorf("%s: %d %q %v", tt.target, got.StatusCode, got.Body, got.Header)
		}
	}
	s = mustLoad(t, root, `, "try_files": ["{path}", "/missing.html"]`)
	if got := get(s, "GET", "/nope", nil).Response; got.StatusCode != 404 {
		t.Errorf("none exists: %d", got.StatusCode)
	}
	// Browse applies to a directory try_files reaches.
	s = mustLoad(t, root, `, "try_files": ["{path}/"], "browse": true`)
	if got := get(s, "GET", "/plain", nil).Response; got.StatusCode != 200 || !strings.Contains(string(got.Body), "x.txt") {
		t.Errorf("a listing through try_files: %d %q", got.StatusCode, got.Body)
	}
}

func TestBrowse(t *testing.T) {
	root := tree(t, map[string]string{
		"files/b.txt": "bb", "files/a&b<c>.txt": "a", "files/q?x.txt": "q", "files/.hidden": "h", "files/sub/z": "z",
	})
	s := mustLoad(t, root, `, "browse": true`)
	got := get(s, "GET", "/files/", nil).Response
	body := string(got.Body)
	if got.StatusCode != 200 || got.Header.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("%d %v", got.StatusCode, got.Header)
	}
	for _, want := range []string{
		"<title>Index of /files/</title>",
		`<a href="../">../</a>`,
		`<a href="./sub/">sub/</a>`,
		`<a href="./a&amp;b%3Cc%3E.txt">a&amp;b&lt;c&gt;.txt</a>`,
		`<a href="./q%3Fx.txt">q?x.txt</a>`,
		`<a href="./b.txt">b.txt</a></td><td>2</td>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("listing lacks %s:\n%s", want, body)
		}
	}
	if strings.Contains(body, "hidden") {
		t.Errorf("listing shows a dotfile:\n%s", body)
	}
	if strings.Index(body, "sub/") > strings.Index(body, "a&amp;b") {
		t.Errorf("directories do not come first:\n%s", body)
	}
	s = mustLoad(t, root, "")
	if got := get(s, "GET", "/files/", nil).Response; got.StatusCode != 404 {
		t.Errorf("without browse: %d", got.StatusCode)
	}
}

func TestFeatureErrors(t *testing.T) {
	root := tree(t, map[string]string{"a": "a"})
	for _, tt := range []struct{ params, want string }{
		{`, "try_files": ["{path}", "index.html"]`, `handler.params.try_files[1]: "index.html" starts with neither / nor {path}`},
		{`, "precompressed": ["deflate"]`, `handler.params.precompressed[0]: "deflate" is not one of "br", "zstd", "gzip"`},
	} {
		_, err := load(t, `{"root": `+quote(root)+tt.params+`}`)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: %v", tt.params, err)
		}
	}
}
