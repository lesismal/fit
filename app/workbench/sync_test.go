package workbench

import (
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// jsStrings returns the string literals of s, a JavaScript source, in
// order, unquoted: '...' and "..." with their escapes.
func jsStrings(s string) []string {
	var out []string
	for i := 0; i < len(s); i++ {
		q := s[i]
		if q != '\'' && q != '"' {
			continue
		}
		var b strings.Builder
		j := i + 1
		for ; j < len(s) && s[j] != q; j++ {
			if s[j] == '\\' && j+1 < len(s) {
				j++
			}
			b.WriteByte(s[j])
		}
		out = append(out, b.String())
		i = j
	}
	return out
}

// pageWords returns the words of a page's WORDS, [key, en, zh] each.
func pageWords(t *testing.T, src string) map[string][2]string {
	t.Helper()
	start := strings.Index(src, "var WORDS = [")
	end := strings.Index(src[start:], "\n  ];")
	if start < 0 || end < 0 {
		t.Fatal("no WORDS")
	}
	words := map[string][2]string{}
	for _, row := range regexp.MustCompile(`\[('(?:[^'\\]|\\.)*'),\s*('(?:[^'\\]|\\.)*'),\s*('(?:[^'\\]|\\.)*')\]`).FindAllString(src[start:start+end], -1) {
		s := jsStrings(row)
		if _, dup := words[s[0]]; dup {
			t.Errorf("%s is worded twice", s[0])
		}
		words[s[0]] = [2]string{s[1], s[2]}
	}
	return words
}

// The Sync pages (I190) word everything they show in English and Chinese:
// each key they look up is in their WORDS with both, or one of the core's;
// each word is used; the placeholders of both match.
func TestSyncWords(t *testing.T) {
	raw, err := fs.ReadFile(Pages(), "sync/sync.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	core, _ := fs.ReadFile(Pages(), "core/ui.js")
	words := pageWords(t, src)
	if len(words) < 100 {
		t.Fatalf("%d words", len(words))
	}
	ph := regexp.MustCompile(`\{[a-z]+\}`)
	for k, w := range words {
		if w[0] == "" || w[1] == "" {
			t.Errorf("%s: %q %q", k, w[0], w[1])
		}
		if a, b := ph.FindAllString(w[0], -1), ph.FindAllString(w[1], -1); strings.Join(sorted(a), ",") != strings.Join(sorted(b), ",") {
			t.Errorf("%s: the placeholders differ: %v %v", k, a, b)
		}
		// A key used, as t('k'), passed to t as 'k', or built as 's.' +
		// state and 'n.' + tab.
		used := strings.Count(src, "'"+k+"'") > 1 || strings.HasPrefix(k, "s.") ||
			(strings.HasPrefix(k, "n.") && strings.Contains(src, "'"+strings.TrimPrefix(k, "n.")+"'"))
		if !used {
			t.Errorf("%s is never shown", k)
		}
	}
	for _, m := range regexp.MustCompile(`\bt\('([a-zA-Z][a-zA-Z.]*[a-zA-Z])'`).FindAllStringSubmatch(src, -1) {
		if _, ok := words[m[1]]; !ok && !strings.Contains(string(core), "'"+m[1]+"':") {
			t.Errorf("t('%s') has no words", m[1])
		}
	}
	// Every state a pipeline can be in has its words.
	for _, s := range []string{"starting", "running", "paused", "retrying", "finished", "stopped", "scheduled", "killed"} {
		if _, ok := words["s."+s]; !ok {
			t.Errorf("the state %s has no words", s)
		}
	}
	// The pages call the Admin API's sync endpoints they show.
	for _, ep := range []string{"'/sync/pipelines'", "'/checkpoint'", "'/test'", "'/dry-run'", "'/dead-letters/replay'", "'/sync/test'", "'/sync/dry-run'",
		"action: 'pause'", "action: 'resume'", "action: 'snapshot', confirm: true", "'/checkpoint?confirm=1'", "members: ['sync']"} {
		if !strings.Contains(src, ep) {
			t.Errorf("sync/sync.js does not use %s", ep)
		}
	}
}

func sorted(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return out
}

func TestJSStrings(t *testing.T) {
	if got := strings.Join(jsStrings(`['a', "b\"c", 'd\'e']`), "|"); got != `a|b"c|d'e` {
		t.Error(got)
	}
}
