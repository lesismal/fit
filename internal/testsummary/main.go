// Command testsummary reads the events of go test -json from its input. By
// default it writes their output back as go test would have, so CI's log
// reads as before. With -annotate it writes a GitHub Actions error
// annotation for each package that failed, holding the output of its failed
// tests and its own output (a timeout's goroutine dump, a race reported
// outside a test): a job's log needs a sign-in to read, its annotations do
// not.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// event is one line of go test -json (see go doc test2json).
type event struct {
	Action  string
	Package string
	Test    string
	Output  string
}

// maxMessage keeps an annotation under GitHub's limit for one message; the
// end of a failure's output is the part kept, as it holds the cause.
const maxMessage = 60000

func main() {
	annotate := flag.Bool("annotate", false, "write an error annotation for each failed package")
	flag.Parse()
	if err := run(os.Stdin, os.Stdout, *annotate); err != nil {
		fmt.Fprintln(os.Stderr, "testsummary:", err)
		os.Exit(1)
	}
}

func run(in io.Reader, out io.Writer, annotate bool) error {
	type key struct{ pkg, test string }
	output := map[key]*strings.Builder{}
	failed := map[key]bool{}
	var order []key

	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	w := bufio.NewWriter(out)
	defer w.Flush()
	for sc.Scan() {
		var e event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			// go test writes build errors as plain text.
			if !annotate {
				fmt.Fprintln(w, sc.Text())
				w.Flush()
			}
			continue
		}
		k := key{e.Package, e.Test}
		switch e.Action {
		case "output":
			if !annotate {
				w.WriteString(e.Output)
				if strings.HasSuffix(e.Output, "\n") {
					w.Flush()
				}
				continue
			}
			b := output[k]
			if b == nil {
				b = &strings.Builder{}
				output[k] = b
				order = append(order, k)
			}
			b.WriteString(e.Output)
		case "fail":
			failed[k] = true
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if !annotate {
		return nil
	}

	// A test that failed because a subtest did has nothing of its own to
	// show; only the leaves, and each package's own output, are kept.
	leaf := func(k key) bool {
		for f := range failed {
			if f.pkg == k.pkg && strings.HasPrefix(f.test, k.test+"/") {
				return false
			}
		}
		return true
	}
	var pkgs []string
	for k := range failed {
		if k.test == "" {
			pkgs = append(pkgs, k.pkg)
		}
	}
	sort.Strings(pkgs)
	for _, pkg := range pkgs {
		var msg strings.Builder
		for _, k := range order {
			if k.pkg != pkg || (k.test != "" && (!failed[k] || !leaf(k))) {
				continue
			}
			msg.WriteString(output[k].String())
		}
		s := msg.String()
		if len(s) > maxMessage {
			s = "...\n" + s[len(s)-maxMessage:]
		}
		fmt.Fprintf(w, "::error title=go test %s::%s\n", pkg, escape(s))
	}
	return nil
}

// escape encodes s as a workflow command's data.
func escape(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A").Replace(s)
}
