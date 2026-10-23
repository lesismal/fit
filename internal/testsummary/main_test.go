package main

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
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

// A timeout's panic goes to the tests still running, which never get a fail
// action of their own; the annotation must still hold it.
func TestAnnotateTimeout(t *testing.T) {
	in := `{"Action":"run","Package":"p","Test":"TestA"}
{"Action":"output","Package":"p","Test":"TestA","Output":"=== RUN   TestA\n"}
{"Action":"run","Package":"p","Test":"TestA/sub"}
{"Action":"output","Package":"p","Test":"TestA/sub","Output":"=== RUN   TestA/sub\n"}
{"Action":"output","Package":"p","Test":"TestA/sub","Output":"panic: test timed out after 2s\n"}
{"Action":"output","Package":"p","Test":"TestA/sub","Output":"goroutine 7 [sleep]:\n"}
{"Action":"output","Package":"p","Test":"TestB","Output":"--- PASS: TestB\n"}
{"Action":"pass","Package":"p","Test":"TestB"}
{"Action":"output","Package":"p","Output":"FAIL\tp\t2.0s\n"}
{"Action":"fail","Package":"p"}
`
	var out strings.Builder
	if err := run(strings.NewReader(in), &out, true); err != nil {
		t.Fatal(err)
	}
	want := "::error title=go test p::=== RUN   TestA/sub%0Apanic: test timed out after 2s%0Agoroutine 7 [sleep]:%0AFAIL\tp\t2.0s%0A\n"
	if out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
}

// A race found after a test passed fails the package with no test failing;
// the annotation holds the whole output, where the race is.
func TestAnnotateNoTestFailed(t *testing.T) {
	in := `{"Action":"output","Package":"p","Test":"TestA","Output":"=== RUN   TestA\n"}
{"Action":"output","Package":"p","Test":"TestA","Output":"--- PASS: TestA\n"}
{"Action":"pass","Package":"p","Test":"TestA"}
{"Action":"output","Package":"p","Test":"TestA","Output":"WARNING: DATA RACE\n"}
{"Action":"output","Package":"p","Output":"FAIL\n"}
{"Action":"fail","Package":"p"}
`
	var out strings.Builder
	if err := run(strings.NewReader(in), &out, true); err != nil {
		t.Fatal(err)
	}
	want := "::error title=go test p::=== RUN   TestA%0A--- PASS: TestA%0AWARNING: DATA RACE%0AFAIL%0A\n"
	if out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
}

func TestAnnotateKeepsTheStart(t *testing.T) {
	long := "panic: test timed out" + strings.Repeat("x", 2*maxMessage)
	in := `{"Action":"output","Package":"p","Output":"` + long + `"}
{"Action":"fail","Package":"p"}
`
	var out strings.Builder
	if err := run(strings.NewReader(in), &out, true); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); !strings.HasPrefix(s, "::error title=go test p::panic: test timed out") {
		t.Fatalf("got %q", s[:60])
	}
}

func TestReadError(t *testing.T) {
	var out strings.Builder
	if err := run(iotest.ErrReader(errors.New("broken")), &out, false); err == nil || err.Error() != "broken" {
		t.Fatalf("got %v", err)
	}
}

// TestMainCommand runs the command itself, on a file for its input and its output.
func TestMainCommand(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in")
	if err := os.WriteFile(in, []byte(events), 0o644); err != nil {
		t.Fatal(err)
	}
	stdin, err := os.Open(in)
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := os.Create(filepath.Join(dir, "out"))
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	oldIn, oldOut, oldArgs, oldFlags := os.Stdin, os.Stdout, os.Args, flag.CommandLine
	defer func() { os.Stdin, os.Stdout, os.Args, flag.CommandLine = oldIn, oldOut, oldArgs, oldFlags }()
	os.Stdin, os.Stdout, os.Args = stdin, stdout, []string{"testsummary", "-annotate"}
	flag.CommandLine = flag.NewFlagSet("testsummary", flag.ContinueOnError)
	main()
	got, err := os.ReadFile(stdout.Name())
	if err != nil {
		t.Fatal(err)
	}
	if want := "::error title=go test p::    a_test.go:9: 50%25 wrong%0AFAIL%0A\n"; string(got) != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
