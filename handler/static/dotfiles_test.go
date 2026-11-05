package static

import (
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/lesismal/fit/internal/dotfile"
)

// TestDotfiles checks that dotfiles and what lies in dot directories are
// answered 404 by default, through every way the handler finds a file, but
// for /.well-known/, and that dotfiles allow serves them.
func TestDotfiles(t *testing.T) {
	root := tree(t, map[string]string{
		".git/config":                  "[core]",
		".env":                         "SECRET=1",
		".htpasswd":                    "u:x",
		"a/.env":                       "SECRET=2",
		"a/b.txt":                      "b",
		"a/.dir/c.txt":                 "c",
		".well-known/acme-challenge/x": "token",
		".well-known/security.txt":     "Contact: x",
		".well-known/sub/.hidden":      "h",
		"site/.well-known/x":           "nested",
		"app.js":                       "js",
		"app.js.gz":                    "gz",
		".app.js":                      "dot",
		".app.js.gz":                   "dotgz",
		"spa/index.html":               "spa",
		"listed/a.txt":                 "a",
		"listed/.secret":               "s",
		"listed/.hiddendir/x":          "x",
	})
	s := mustLoad(t, root, `, "precompressed": ["gzip"]`)
	for _, target := range []string{
		"/.git/config", "/.git/", "/.git", "/.env", "/.htpasswd", "/a/.env", "/a/.dir/c.txt", "/a/.dir/",
		"/.app.js", "/.well-known/sub/.hidden", "/site/.well-known/x", "/.env?x=1",
	} {
		for _, method := range []string{"GET", "HEAD"} {
			got := get(s, method, target, map[string]string{"Accept-Encoding": "gzip"}).Response
			if got.StatusCode != 404 {
				t.Errorf("%s %s: %d, want 404", method, target, got.StatusCode)
			}
			if strings.Contains(string(got.Body), "SECRET") || strings.Contains(string(got.Body), "[core]") {
				t.Errorf("%s %s leaks %q", method, target, got.Body)
			}
		}
	}
	for target, want := range map[string]string{
		"/.well-known/acme-challenge/x": "token",
		"/.well-known/security.txt":     "Contact: x",
		"/a/b.txt":                      "b",
		"/app.js":                       "js",
	} {
		if got := get(s, "GET", target, nil).Response; got.StatusCode != 200 || string(got.Body) != want {
			t.Errorf("GET %s: %d %q, want %q", target, got.StatusCode, got.Body, want)
		}
	}
	// A precompressed twin of a file that is served is served too.
	if got := get(s, "GET", "/app.js", map[string]string{"Accept-Encoding": "gzip"}).Response; string(got.Body) != "gz" {
		t.Errorf("twin: %d %q", got.StatusCode, got.Body)
	}
	// ".." is still refused with 400, the same under deny.
	if got := get(s, "GET", "/a/../.env", nil).Response; got.StatusCode != 400 {
		t.Errorf("..: %d", got.StatusCode)
	}

	// strip_prefix: the path under the root is what is looked at.
	p := mustLoad(t, root, `, "strip_prefix": "/static"`)
	if got := get(p, "GET", "/static/.env", nil).Response; got.StatusCode != 404 {
		t.Errorf("strip_prefix dotfile: %d %q", got.StatusCode, got.Body)
	}
	if got := get(p, "GET", "/static/a/b.txt", nil).Response; got.StatusCode != 200 {
		t.Errorf("strip_prefix ok: %d", got.StatusCode)
	}

	// try_files: a request for a dotfile falls through to the fallback,
	// never to the dotfile.
	spa := mustLoad(t, root, `, "try_files": ["{path}", "{path}/", "/spa/index.html"]`)
	for _, target := range []string{"/.env", "/a/.env", "/.git/config"} {
		if got := get(spa, "GET", target, nil).Response; got.StatusCode != 404 {
			t.Errorf("try_files %s: %d %q", target, got.StatusCode, got.Body)
		}
	}
	if got := get(spa, "GET", "/route", nil).Response; got.StatusCode != 200 || string(got.Body) != "spa" {
		t.Errorf("try_files fallback: %d %q", got.StatusCode, got.Body)
	}
	if got := get(spa, "GET", "/.well-known/acme-challenge/x", nil).Response; string(got.Body) != "token" {
		t.Errorf("try_files .well-known: %d %q", got.StatusCode, got.Body)
	}

	// A listing leaves dotfiles out, and a dot directory is not listed.
	b := mustLoad(t, root, `, "browse": true`)
	got := get(b, "GET", "/listed/", nil).Response
	if got.StatusCode != 200 || !strings.Contains(string(got.Body), "a.txt") ||
		strings.Contains(string(got.Body), "secret") || strings.Contains(string(got.Body), "hiddendir") {
		t.Errorf("listing: %d %q", got.StatusCode, got.Body)
	}
	if got := get(b, "GET", "/listed/.hiddendir/", nil).Response; got.StatusCode != 404 {
		t.Errorf("listing a dot directory: %d %q", got.StatusCode, got.Body)
	}

	// allow serves them as before.
	a := mustLoad(t, root, `, "dotfiles": "allow", "precompressed": ["gzip"]`)
	for target, want := range map[string]string{
		"/.git/config": "[core]", "/.env": "SECRET=1", "/a/.env": "SECRET=2", "/a/.dir/c.txt": "c",
		"/.well-known/sub/.hidden": "h", "/.well-known/acme-challenge/x": "token",
	} {
		if got := get(a, "GET", target, nil).Response; got.StatusCode != 200 || string(got.Body) != want {
			t.Errorf("allow GET %s: %d %q, want %q", target, got.StatusCode, got.Body, want)
		}
	}
	if got := get(a, "GET", "/.app.js", map[string]string{"Accept-Encoding": "gzip"}).Response; string(got.Body) != "dotgz" {
		t.Errorf("allow twin: %d %q", got.StatusCode, got.Body)
	}
	// Listings leave dotfiles out under allow too.
	ab := mustLoad(t, root, `, "dotfiles": "allow", "browse": true`)
	if got := get(ab, "GET", "/listed/", nil).Response; strings.Contains(string(got.Body), "secret") {
		t.Errorf("allow listing: %q", got.Body)
	}
	explicit := mustLoad(t, root, `, "dotfiles": "deny"`)
	if got := get(explicit, "GET", "/.env", nil).Response; got.StatusCode != 404 {
		t.Errorf("explicit deny: %d", got.StatusCode)
	}
}

// TestDotfilesShortNames checks that, where files have 8.3 short names, a
// dotfile's (.git as GIT~1) is answered 404 under deny, with try_files too,
// as a dotfile is, and is served elsewhere, and under allow. The tree holds the short
// names as names of their own, which on Windows stand for the aliases NTFS
// would give .git and .htpasswd.
func TestDotfilesShortNames(t *testing.T) {
	root := tree(t, map[string]string{
		"GIT~1/config":   "[core]",
		"HTPASS~1":       "u:x",
		"a/b.txt":        "b",
		"spa/index.html": "spa",
	})
	s := mustLoad(t, root, "")
	if s.hideShortNames != dotfile.ShortNames {
		t.Fatalf("hideShortNames %v on %s", s.hideShortNames, goruntime.GOOS)
	}
	spa := mustLoad(t, root, `, "try_files": ["{path}", "/spa/index.html"]`)
	if !dotfile.ShortNames {
		// Elsewhere they are names as others are.
		if got := get(s, "GET", "/GIT~1/config", nil).Response; got.StatusCode != 200 {
			t.Errorf("GET /GIT~1/config off Windows: %d", got.StatusCode)
		}
		s = mustLoad(t, root, "")
		spa = mustLoad(t, root, `, "try_files": ["{path}", "/spa/index.html"]`)
		s.hideShortNames, spa.hideShortNames = true, true
	}
	// The short names alone: on Windows those written with what it strips
	// are also aliases, refused 400 before they are looked at.
	s.refuseAliases, spa.refuseAliases = false, false
	for _, target := range []string{"/GIT~1/config", "/git~1/config", "/GIT~1./config", "/GIT~1%20/config", "/HTPASS~1", "/HTPASS~1::$DATA", "/GIT~1/"} {
		got := get(s, "GET", target, nil).Response
		if got.StatusCode != 404 || strings.Contains(string(got.Body), "[core]") || strings.Contains(string(got.Body), "u:x") {
			t.Errorf("GET %s: %d %q, want 404", target, got.StatusCode, got.Body)
		}
		if got := get(spa, "GET", target, nil).Response; got.StatusCode != 404 {
			t.Errorf("try_files %s: %d %q, want 404", target, got.StatusCode, got.Body)
		}
	}
	if got := get(s, "GET", "/a/b.txt", nil).Response; got.StatusCode != 200 || string(got.Body) != "b" {
		t.Errorf("GET /a/b.txt: %d %q", got.StatusCode, got.Body)
	}
	a := mustLoad(t, root, `, "dotfiles": "allow"`)
	if a.hideShortNames {
		t.Error("allow hides short names")
	}
	if got := get(a, "GET", "/GIT~1/config", nil).Response; got.StatusCode != 200 || string(got.Body) != "[core]" {
		t.Errorf("allow GET /GIT~1/config: %d %q", got.StatusCode, got.Body)
	}
	if dotfile.ShortNames {
		if _, err := load(t, `{"root": `+quote(root)+`, "try_files": ["{path}", "/GIT~1/config"]}`); err == nil ||
			!strings.Contains(err.Error(), "8.3 short name") {
			t.Errorf("try_files naming a short name: %v", err)
		}
	}
}

// TestDotfilesConfig checks that index files and try_files that name a
// dotfile are refused under deny and taken under allow.
func TestDotfilesConfig(t *testing.T) {
	root := tree(t, map[string]string{"d/.index.html": "dotindex", "d/x": "x", ".app/index.html": "app"})
	for _, tt := range []struct{ params, want string }{
		{`, "index": [".index.html"]`, `handler.params.index: ".index.html" starts with a dot`},
		{`, "try_files": ["{path}", "/.app/index.html"]`, `handler.params.try_files[1]: "/.app/index.html" names a path`},
		{`, "try_files": ["{path}/.htaccess"]`, `handler.params.try_files[0]`},
		{`, "dotfiles": "hide"`, `handler.params.dotfiles: "hide" is not one of "deny", "allow"`},
	} {
		_, err := load(t, `{"root": `+quote(root)+tt.params+`}`)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: %v", tt.params, err)
		}
	}
	// {path}.html and /.well-known/ fallbacks are no dotfiles.
	mustLoad(t, root, `, "try_files": ["{path}.html", "/.well-known/fallback"]`)
	s := mustLoad(t, root, `, "dotfiles": "allow", "index": [".index.html"], "try_files": ["{path}", "/.app/index.html"]`)
	if got := get(s, "GET", "/d/", nil).Response; string(got.Body) != "dotindex" {
		t.Errorf("allow dot index: %d %q", got.StatusCode, got.Body)
	}
	if got := get(s, "GET", "/nope", nil).Response; string(got.Body) != "app" {
		t.Errorf("allow dot try_files: %d %q", got.StatusCode, got.Body)
	}
}

// TestWindowsAliases checks that, where the system opens a file by names
// not its own, a request for x.php::$DATA, x.php. or "x.php " is answered
// 400 and never with x.php, nor is a try_files candidate or an index file
// of that kind served, nor a device name opened, whatever dotfiles says,
// and that the files' own names are served as before. Elsewhere such
// names are names as others are.
func TestWindowsAliases(t *testing.T) {
	files := map[string]string{
		"x.php":          "<?php secret",
		"file.txt":       "text",
		"sub/a.txt":      "a",
		"spa/index.html": "spa",
	}
	if !dotfile.Aliases {
		// The aliases as names of their own, which on Windows stand for
		// what it would open for them.
		for _, name := range []string{"x.php::$DATA", "x.php.", "x.php ", "sub./a.txt", "nul.txt", "CON.php"} {
			files[name] = "<?php secret"
		}
	}
	root := tree(t, files)
	s := mustLoad(t, root, "")
	if s.refuseAliases != dotfile.Aliases {
		t.Fatalf("refuseAliases %v on %s", s.refuseAliases, goruntime.GOOS)
	}
	if !dotfile.Aliases {
		// Elsewhere a colon or a trailing dot is part of a name.
		named := tree(t, map[string]string{"a:b.txt": "colon", "c.txt.": "dot"})
		other := mustLoad(t, named, "")
		for target, want := range map[string]string{"/a:b.txt": "colon", "/c.txt.": "dot"} {
			if got := get(other, "GET", target, nil).Response; got.StatusCode != 200 || string(got.Body) != want {
				t.Errorf("GET %s off Windows: %d %q", target, got.StatusCode, got.Body)
			}
		}
	}
	spa := mustLoad(t, root, `, "try_files": ["{path}", "/spa/index.html"]`)
	allow := mustLoad(t, root, `, "dotfiles": "allow", "precompressed": ["gzip"]`)
	s.refuseAliases, spa.refuseAliases, allow.refuseAliases = true, true, true // as on Windows
	for _, target := range []string{
		"/x.php::$DATA", "/x.php:stream", "/x.php%3A%3A$DATA", "/file.txt::$DATA", "/sub::$INDEX_ALLOCATION/a.txt",
		"/x.php.", "/x.php%20", "/x.php.%20.", "/sub./a.txt", "/sub%20/a.txt", "/x.ph%3F", "/x.php%01",
		"/NUL", "/nul.txt", "/CON.php", "/com1", "/LPT1.txt", "/sub/AUX", "/CONIN$",
	} {
		for name, h := range map[string]*Static{"plain": s, "try_files": spa, "allow": allow} {
			got := get(h, "GET", target, map[string]string{"Accept-Encoding": "gzip"}).Response
			if got.StatusCode != 400 || strings.Contains(string(got.Body), "secret") || strings.Contains(string(got.Body), "text") {
				t.Errorf("%s GET %s: %d %q, want 400", name, target, got.StatusCode, got.Body)
			}
		}
	}
	for target, want := range map[string]string{"/x.php": "<?php secret", "/file.txt": "text", "/sub/a.txt": "a", "/spa/": "spa"} {
		if got := get(s, "GET", target, nil).Response; got.StatusCode != 200 || string(got.Body) != want {
			t.Errorf("GET %s: %d %q", target, got.StatusCode, got.Body)
		}
	}
	// A try_files candidate made of an alias is passed over.
	spa.TryFiles = []string{"{path}.", "{path}::$DATA", "/spa/index.html"}
	if got := get(spa, "GET", "/x.php", nil).Response; got.StatusCode != 200 || string(got.Body) != "spa" {
		t.Errorf("try_files {path}. for /x.php: %d %q", got.StatusCode, got.Body)
	}
	if dotfile.Aliases {
		for _, extra := range []string{`, "index": ["index.html."]`, `, "index": ["NUL"]`, `, "try_files": ["{path}::$DATA"]`, `, "try_files": ["{path}", "/CON.html"]`} {
			if _, err := load(t, `{"root": `+quote(root)+extra+`}`); err == nil || !strings.Contains(err.Error(), "Windows") {
				t.Errorf("%s: %v", extra, err)
			}
		}
	}
}
