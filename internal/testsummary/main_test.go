package main

import (
	"strings"
	"testing"
)

const events = `{"Action":"run","Package":"p","Test":"TestA"}
{"Action":"output","Package":"p","Test":"TestA","Output":"=== RUN   TestA\n"}
{"Action":"output","Package":"p","Test":"TestA/sub","Output":"    a_test.go:9: 50% wrong\n"}
{"Action":"fail","Package":"p","Test":"TestA/sub"}
{"Action":"output","Package":"p","Test":"TestA","Output":"--- FAIL: TestA\n"}
{"Action":"fail","Package":"p","Test":"TestA"}
{"Action":"output","Package":"p","Test":"TestB","Output":"--- PASS: TestB\n"}
{"Action":"pass","Package":"p","Test":"TestB"}
{"Action":"output","Package":"p","Output":"FAIL\n"}
{"Action":"fail","Package":"p"}
{"Action":"output","Package":"q","Output":"ok\n"}
{"Action":"pass","Package":"q"}
build error, not JSON
`

func TestPassThrough(t *testing.T) {
	var out strings.Builder
	if err := run(strings.NewReader(events), &out, false); err != nil {
		t.Fatal(err)
	}
	want := "=== RUN   TestA\n    a_test.go:9: 50% wrong\n--- FAIL: TestA\n--- PASS: TestB\nFAIL\nok\nbuild error, not JSON\n"
	if out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
}

func TestAnnotate(t *testing.T) {
	var out strings.Builder
	if err := run(strings.NewReader(events), &out, true); err != nil {
		t.Fatal(err)
	}
	// TestA failed only through its subtest, TestB passed and q passed.
	want := "::error title=go test p::    a_test.go:9: 50%25 wrong%0AFAIL%0A\n"
	if out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
}

func TestAnnotateKeepsTheEnd(t *testing.T) {
	long := strings.Repeat("x", maxMessage) + "cause"
	in := `{"Action":"output","Package":"p","Output":"` + long + `"}
{"Action":"fail","Package":"p"}
`
	var out strings.Builder
	if err := run(strings.NewReader(in), &out, true); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); !strings.HasSuffix(s, "cause\n") || len(s) > maxMessage+100 {
		t.Fatalf("got %d bytes ending %q", len(s), s[len(s)-10:])
	}
}
