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

func BenchmarkScan(b *testing.B) {
	for b.Loop() {
		Scan("/assets/js/app.min.3f2a9c.js")
	}
}
