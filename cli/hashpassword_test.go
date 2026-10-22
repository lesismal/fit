package cli

import (
	"io"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestHashPassword(t *testing.T) {
	defer func(r io.Reader) { stdin = r }(stdin)
	stdin = strings.NewReader("s3cret\nignored\n")
	code, stdout, stderr := runFit("hash-password", "-cost", "4")
	hash := strings.TrimSuffix(stdout, "\n")
	if code != 0 || stderr != "" {
		t.Fatalf("%d %q %q", code, stdout, stderr)
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte("s3cret")) != nil {
		t.Fatalf("%q is not the hash of the password", hash)
	}
	if cost, _ := bcrypt.Cost([]byte(hash)); cost != 4 {
		t.Errorf("cost %d, want 4", cost)
	}

	for _, tt := range []struct {
		input string
		args  []string
		code  int
		err   string
	}{
		{"\n", nil, 1, "empty"},
		{"", nil, 1, "empty"},
		{strings.Repeat("x", 73), nil, 1, "72 bytes"},
		{"pw\n", []string{"pw"}, 2, "standard input"},
		{"pw\n", []string{"-cost", "99"}, 1, "cost"},
	} {
		stdin = strings.NewReader(tt.input)
		code, _, stderr := runFit(append([]string{"hash-password"}, tt.args...)...)
		if code != tt.code || !strings.Contains(stderr, tt.err) {
			t.Errorf("%q %v: %d %q, want %d and %q", tt.input, tt.args, code, stderr, tt.code, tt.err)
		}
	}
}
