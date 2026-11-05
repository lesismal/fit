package router

import (
	"net/url"
	"testing"
)

func TestNormalizePath(t *testing.T) {
	for _, tc := range []struct {
		target    string
		ok        bool
		path, raw string
	}{
		{"/", true, "/", ""},
		{"/a/b", true, "/a/b", ""},
		{"/a/b/", true, "/a/b/", ""},
		{"/./admin/secret", true, "/admin/secret", ""},
		{"/x/../admin/secret", true, "/admin/secret", ""},
		{"//admin/secret", true, "/admin/secret", ""},
		{"/admin//secret", true, "/admin/secret", ""},
		{"/x/%2e%2e/admin/secret", true, "/admin/secret", ""},
		{"/x/%2E%2E/admin/secret", true, "/admin/secret", ""},
		{"/x/.%2E/admin/secret", true, "/admin/secret", ""},
		{"/x/%2e./admin/secret", true, "/admin/secret", ""},
		{"/%2e/admin", true, "/admin", ""},
		{"/../admin", true, "/admin", ""},
		{"/../../..", true, "/", ""},
		{"/a/b/..", true, "/a/", ""},
		{"/a/b/.", true, "/a/b/", ""},
		{"/a/./b/../c//d/", true, "/a/c/d/", ""},
		{"/a/...", true, "/a/...", ""},
		{"/a/..b/.c", true, "/a/..b/.c", ""},
		{"/.well-known/acme", true, "/.well-known/acme", ""},
		// Other escapes stay as written.
		{"/a%2Fb", true, "/a/b", "/a%2Fb"},
		{"/x/../a%2Fb/%7e", true, "/a/b/~", "/a%2Fb/%7e"},
		{"/.x/a%2Fb", true, "/.x/a/b", "/.x/a%2Fb"},
		{"/x/%2e%2e/c%20d", true, "/c d", ""},
		// Encoded slashes that would make dot or empty segments.
		{"/x/..%2Fadmin/secret", false, "", ""},
		{"/x/%2e%2e%2Fadmin", false, "", ""},
		{"/%2Fadmin/secret", false, "", ""},
		{"/a/%2F/b", false, "", ""},
		{"/a%2Fb/../c", false, "", ""},
		{"/x/%2e%2e/a%2Fb/../c", false, "", ""},
		// Not a path to clean.
		{"*", true, "*", ""},
	} {
		u, err := url.ParseRequestURI(tc.target)
		if err != nil {
			t.Fatal(err)
		}
		if ok := NormalizePath(u); ok != tc.ok {
			t.Errorf("NormalizePath(%q) = %v, want %v", tc.target, ok, tc.ok)
			continue
		}
		if tc.ok && (u.Path != tc.path || u.RawPath != tc.raw) {
			t.Errorf("NormalizePath(%q): path %q raw %q, want %q %q", tc.target, u.Path, u.RawPath, tc.path, tc.raw)
		}
	}
	// A path with nothing to clean costs nothing.
	u := &url.URL{Path: "/api/v1/users/42", RawQuery: "x=1"}
	if n := testing.AllocsPerRun(100, func() { NormalizePath(u) }); n != 0 {
		t.Errorf("NormalizePath allocates %v times", n)
	}
}

func TestEscapedDot(t *testing.T) {
	for s, want := range map[string]int{
		".": segmentDot, "%2e": segmentDot, "%2E": segmentDot,
		"..": segmentDotDot, ".%2e": segmentDotDot, "%2E.": segmentDotDot, "%2e%2E": segmentDotDot,
		"": segmentOther, "...": segmentOther, "%2e%2e%2e": segmentOther, "%2": segmentOther, "%2f": segmentOther, "a": segmentOther,
	} {
		if got := escapedDot(s); got != want {
			t.Errorf("escapedDot(%q) = %d, want %d", s, got, want)
		}
	}
}

func BenchmarkNormalizePath(b *testing.B) {
	u := &url.URL{Path: "/api/v1/users/42"}
	for b.Loop() {
		NormalizePath(u)
	}
}
