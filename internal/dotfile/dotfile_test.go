package dotfile

import "testing"

func TestScan(t *testing.T) {
	for _, tt := range []struct {
		p              string
		unsafe, hidden bool
	}{
		{"", false, false},
		{"/", false, false},
		{"/a/b.txt", false, false},
		{"a/b.txt", false, false},
		{"/a.b/c..d", false, false},
		{"/./a", false, false},
		{"//a", false, false},
		{"/.env", false, true},
		{".env", false, true},
		{"/.git/config", false, true},
		{"/.git/", false, true},
		{"/a/.env", false, true},
		{"/a/.dir/b", false, true},
		{"/..dots", false, true},
		{"/.well-known/acme-challenge/x", false, false},
		{".well-known/x", false, false},
		{"/.well-known", false, false},
		{"/.well-known/", false, false},
		{"/./.well-known/x", false, false},
		{"/.well-known/a/.b", false, true},
		{"/a/.well-known/x", false, true},
		{"/.well-knownx", false, true},
		{"/..", true, false},
		{"/a/../b", true, false},
		{"/a/../.env", true, true},
		{"/.env/..", true, true},
		{"/a\\b", true, false},
		{"/a\x00", true, false},
		{"/.e\x00nv", true, true},
	} {
		unsafe, hidden := Scan(tt.p)
		if unsafe != tt.unsafe || hidden != tt.hidden {
			t.Errorf("Scan(%q) = %v, %v, want %v, %v", tt.p, unsafe, hidden, tt.unsafe, tt.hidden)
		}
		if Hidden(tt.p) != tt.hidden {
			t.Errorf("Hidden(%q) = %v", tt.p, !tt.hidden)
		}
	}
}

// TestHasShortName checks the 8.3 short names of dotfiles, as NTFS makes
// them, and the ways Windows lets them be written, are found on every OS,
// and that ordinary names are not.
func TestHasShortName(t *testing.T) {
	for _, p := range []string{
		"/GIT~1/config", "/git~1/config", "GIT~1", "/GIT~1/", "/a/GIT~1/HEAD",
		"/HTPASS~1", "/ENV~1.LOC", "/GITIG~12", "/GI1A2B~1", "/G~123456",
		"/GIT~1./config", "/GIT~1 /config", "/GIT~1. . /config", "/ENV~1.LOC.",
		"/GIT~1::$INDEX_ALLOCATION/config", "/HTPASS~1:stream", "/HTPASS~1::$DATA",
		"/x/file~1.txt", "/~1",
	} {
		if !HasShortName(p) {
			t.Errorf("HasShortName(%q) = false, want true", p)
		}
	}
	for _, p := range []string{
		"", "/", "/a/b.txt", "/.git/config", "/file.txt~", "/file~", "/~", "/a~b",
		"/LONGNAME~1", "/ABCDEFG~12", "/ENV~1.LOCAL", "/GIT~1x", "/GIT~1.a.b", "/GIT~x1",
		"/~user/index.html", "/a/~/b", "/backup~1~", "/GITIGN~12", "/v1.2~3.tar",
	} {
		if HasShortName(p) {
			t.Errorf("HasShortName(%q) = true, want false", p)
		}
	}
}

func BenchmarkScan(b *testing.B) {
	for b.Loop() {
		Scan("/assets/js/app.min.3f2a9c.js")
	}
}
