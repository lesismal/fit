package cli

import (
	"errors"
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

func TestHashPasswordTerminal(t *testing.T) {
	defer func(r io.Reader, raw func() (func(), bool, error)) { stdin, rawTerminal = r, raw }(stdin, rawTerminal)
	restored := 0
	rawTerminal = func() (func(), bool, error) { return func() { restored++ }, true, nil }

	// Typed: "s3x", Backspace, "cré", Enter; then a Ctrl-U'd attempt, an
	// arrow key and "s3cré", Enter.
	stdin = strings.NewReader("s3x\x7fcré\rjunk\x15\x1b[As3cré\r")
	code, stdout, stderr := runFit("hash-password", "-cost", "4")
	if code != 0 {
		t.Fatalf("%d %q %q", code, stdout, stderr)
	}
	if hash := strings.TrimSuffix(stdout, "\n"); bcrypt.CompareHashAndPassword([]byte(hash), []byte("s3cré")) != nil {
		t.Fatalf("%q is not the hash of the password", hash)
	}
	want := "Password: ***\b \b***\r\n" + "Confirm password: ****" + strings.Repeat("\b \b", 4) + "*****\r\n"
	if stderr != want {
		t.Errorf("prompts %q, want %q", stderr, want)
	}
	if restored != 1 {
		t.Errorf("terminal restored %d times, want 1", restored)
	}

	for _, tt := range []struct{ input, err string }{
		{"a\rb\r", "do not match"},
		{"a\x03", "interrupted"},
		{"a\rb", "interrupted"},
		{"\r", "empty"},
	} {
		stdin = strings.NewReader(tt.input)
		code, _, stderr := runFit("hash-password", "-cost", "4")
		if code != 1 || !strings.Contains(stderr, tt.err) {
			t.Errorf("%q: %d %q, want 1 and %q", tt.input, code, stderr, tt.err)
		}
	}

	rawTerminal = func() (func(), bool, error) { return nil, true, errors.New("no raw mode") }
	if code, _, stderr := runFit("hash-password"); code != 1 || !strings.Contains(stderr, "no raw mode") {
		t.Errorf("raw mode failing: %d %q", code, stderr)
	}
}
