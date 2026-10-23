// Package docs holds fit's documentation; its test checks that the
// Markdown across the repository holds together.
package docs

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode"
)

var (
	fence      = regexp.MustCompile("(?s)```.*?```")
	inlineCode = regexp.MustCompile("`[^`\n]*`")
	link       = regexp.MustCompile(`\]\(([^)\s]+)\)`)
	heading    = regexp.MustCompile(`(?m)^#{1,6} (.*)$`)
	anchorTag  = regexp.MustCompile(`<a id="([^"]+)"`)
	textLink   = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	scheme     = regexp.MustCompile(`^[a-z]+:`)
)

// TestLinks checks every relative link in the repository's Markdown: the
// file it names exists, and so does the heading or anchor after its #,
// as GitHub makes them.
func TestLinks(t *testing.T) {
	root := ".."
	anchors := map[string]map[string]bool{}
	anchorsOf := func(path string) map[string]bool {
		if a, ok := anchors[path]; ok {
			return a
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		a := map[string]bool{}
		seen := map[string]int{}
		for _, m := range heading.FindAllStringSubmatch(fence.ReplaceAllString(string(data), ""), -1) {
			s := slug(textLink.ReplaceAllString(strings.ReplaceAll(m[1], "`", ""), "$1"))
			if n, ok := seen[s]; ok {
				seen[s] = n + 1
				s = fmt.Sprintf("%s-%d", s, n+1)
			} else {
				seen[s] = 0
			}
			a[s] = true
		}
		for _, m := range anchorTag.FindAllStringSubmatch(string(data), -1) {
			a[m[1]] = true
		}
		anchors[path] = a
		return a
	}

	checked := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".claude", "node_modules", "testdata":
				return filepath.SkipDir
			}
			// The reference's chapters link within the page they are made
			// into, which is checked.
			if filepath.ToSlash(path) == "../schema/doc" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".md") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := inlineCode.ReplaceAllString(fence.ReplaceAllString(string(data), ""), "")
		for _, m := range link.FindAllStringSubmatch(text, -1) {
			target := m[1]
			if scheme.MatchString(target) {
				continue
			}
			checked++
			file, frag, _ := strings.Cut(target, "#")
			dest := path
			if file != "" {
				dest = filepath.Join(filepath.Dir(path), file)
			}
			if _, err := os.Stat(dest); err != nil {
				t.Errorf("%s: %s: no such file", path, target)
				continue
			}
			if frag != "" && strings.HasSuffix(dest, ".md") && !anchorsOf(dest)[frag] {
				t.Errorf("%s: %s: no such heading or anchor", path, target)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 100 {
		t.Fatalf("only %d links checked", checked)
	}
}

// slug is the anchor GitHub gives a heading: lower case, letters, digits,
// '-' and '_' kept, spaces made '-', everything else dropped.
func slug(h string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(h)) {
		switch {
		case r == ' ':
			b.WriteRune('-')
		case r == '-', r == '_', unicode.IsLetter(r), unicode.IsDigit(r):
			b.WriteRune(r)
		}
	}
	return b.String()
}
