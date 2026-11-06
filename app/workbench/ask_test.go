package workbench

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every Ask AI spec of the pages (I128) names a built-in skill, and gives it
// only inputs that skill takes: the chat refuses a skill's unknown input.
func TestAskSkills(t *testing.T) {
	skills := builtinSkills(t)
	specs := 0
	want := map[string]bool{"explain": false, "drift-explain": false, "diagnose": false,
		"library-health": false, "channel-scheduler": false, "playback-troubleshoot": false,
		"fleet-health": false, "rollout-planner": false, "cluster-drift": false}
	spec := regexp.MustCompile(`skill: '([a-z][a-z0-9-]*)'`)
	if err := fs.WalkDir(Pages(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".js") {
			return err
		}
		src, err := fs.ReadFile(Pages(), path)
		if err != nil {
			return err
		}
		s := string(src)
		locs := spec.FindAllStringSubmatchIndex(s, -1)
		for i, loc := range locs {
			name := s[loc[2]:loc[3]]
			end := len(s)
			if i+1 < len(locs) {
				end = locs[i+1][0]
			}
			if end > loc[1]+1200 {
				end = loc[1] + 1200
			}
			takes, ok := skills[name]
			if !ok {
				t.Errorf("%s asks the skill %s, which is not built in", path, name)
				continue
			}
			specs++
			if _, ok := want[name]; ok {
				want[name] = true
			}
			body := s[loc[1]:end]
			at := strings.Index(body, "inputs:")
			if at < 0 {
				continue
			}
			for _, k := range objectKeys(body[at+len("inputs:"):]) {
				if !takes[k] {
					t.Errorf("%s gives the skill %s the input %s, which it does not take", path, name, k)
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("no page asks the skill %s", name)
		}
	}
	if specs < 10 {
		t.Errorf("%d Ask AI specs found; the pages have more", specs)
	}
}

// builtinSkills reads the inputs each built-in skill takes, from its front
// matter in assistant/skills.
func builtinSkills(t *testing.T) map[string]map[string]bool {
	t.Helper()
	files, err := filepath.Glob("../../assistant/skills/*.md")
	if err != nil || len(files) == 0 {
		t.Fatalf("the built-in skills: %v (%d files)", err, len(files))
	}
	out := map[string]map[string]bool{}
	input := regexp.MustCompile(`(?m)^  - name: ([a-z_]+)\s*$`)
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		parts := strings.SplitN(string(raw), "---", 3)
		if len(parts) < 3 {
			t.Fatalf("%s has no front matter", f)
		}
		front := parts[1]
		takes := map[string]bool{}
		if i := strings.Index(front, "\ninputs:"); i >= 0 {
			block := front[i+len("\ninputs:"):]
			if j := regexp.MustCompile(`\n[a-z_]+:`).FindStringIndex(block); j != nil {
				block = block[:j[0]]
			}
			for _, m := range input.FindAllStringSubmatch(block, -1) {
				takes[m[1]] = true
			}
		}
		out[strings.TrimSuffix(filepath.Base(f), ".md")] = takes
	}
	return out
}

// objectKeys returns the keys of the first JavaScript object literal in s,
// at its own level: { a: f(x, { b: 1 }), c: 'd' } gives a and c.
func objectKeys(s string) []string {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return nil
	}
	var keys []string
	depth, quote, expectKey := 0, byte(0), false
	for i := start; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
			continue
		case c == '\'' || c == '"':
			quote = c
		case c == '{' || c == '(' || c == '[':
			depth++
			expectKey = depth == 1 && c == '{'
			continue
		case c == '}' || c == ')' || c == ']':
			depth--
			if depth == 0 {
				return keys
			}
		case c == ',' && depth == 1:
			expectKey = true
			continue
		case expectKey && depth == 1 && (c == ' ' || c == '\n' || c == '\t'):
			continue
		case expectKey && depth == 1:
			j := i
			for j < len(s) && (s[j] == '_' || s[j] >= 'a' && s[j] <= 'z' || s[j] >= 'A' && s[j] <= 'Z' || s[j] >= '0' && s[j] <= '9') {
				j++
			}
			if j > i && j < len(s) && s[j] == ':' {
				keys = append(keys, s[i:j])
			}
			i = j - 1
		}
		expectKey = false
	}
	return keys
}

func TestObjectKeys(t *testing.T) {
	for in, want := range map[string]string{
		`{ error: [error].concat(ds.map(f)).join('\n'), path: one }`: "error path",
		`sel.route ? { route: sel.route } : null`:                    "route",
		`{ a: f(x, { b: 1 }), c: 'd,e: f' }`:                         "a c",
		`none`:                                                       "",
		`{ unclosed: 1`:                                              "unclosed",
	} {
		if got := strings.Join(objectKeys(in), " "); got != want {
			t.Errorf("objectKeys(%q) = %q, want %q", in, got, want)
		}
	}
}
