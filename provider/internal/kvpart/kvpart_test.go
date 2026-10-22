package kvpart

import (
	"strings"
	"testing"
)

func TestPart(t *testing.T) {
	entries := []Entry{
		{"fit/b", []byte(`{"routes": [{"name": "b", "handler": {"module": "respond"}}], "upstreams": [{"name": "u2", "addresses": ["10.0.0.2:80"]}]}`)},
		{"fit/a", []byte(`{
  "upstreams": [{"name": "u1", "addresses": ["10.0.0.1:80"]}],
  "listeners": [{"name": "l", "address": ":8080"}]
}`)},
		{"fit/", nil},                                    // a folder
		{"fit/dir/", []byte(`junk`)},                     // a folder, with a value
		{"fit/empty", []byte("  \n")},                    // empty
		{"fit/c", []byte(`{"access_log": {}}`)},          // not a part
		{"fit/d", []byte(`{"routes": [`)},                // not JSON
		{"fit/e", []byte(`{"services": {"name": "x"}}`)}, // not an array
	}
	part, problems := Part(entries, 10)
	want := `{"listeners":[{"name":"l","address":":8080"}],` +
		`"routes":[{"name":"b","handler":{"module":"respond"}}],` +
		`"upstreams":[{"name":"u1","addresses":["10.0.0.1:80"]},{"name":"u2","addresses":["10.0.0.2:80"]}]}`
	if string(part) != want {
		t.Errorf("part:\n%s\nwant:\n%s", part, want)
	}
	if len(problems) != 3 || !strings.HasPrefix(problems[0], "key fit/c: ") || !strings.Contains(problems[0], "only listeners, routes, upstreams and services") ||
		!strings.HasPrefix(problems[1], "key fit/d: ") || !strings.HasPrefix(problems[2], "key fit/e: ") {
		t.Errorf("problems: %q", problems)
	}

	// Past max, the rest by key are left out.
	part, problems = Part([]Entry{
		{"k3", []byte(`{"routes": [{"name": "c", "handler": {"module": "respond"}}]}`)},
		{"k1", []byte(`{"routes": [{"name": "a", "handler": {"module": "respond"}}]}`)},
		{"k2", []byte(`{"routes": [{"name": "b", "handler": {"module": "respond"}}]}`)},
	}, 2)
	if !strings.Contains(string(part), `"a"`) || !strings.Contains(string(part), `"b"`) || strings.Contains(string(part), `"c"`) {
		t.Errorf("part with max 2: %s", part)
	}
	if len(problems) != 1 || problems[0] != "key k3: left out: max_keys, 2, are taken" {
		t.Errorf("problems with max 2: %q", problems)
	}

	if part, problems := Part(nil, 10); string(part) != "{}" || problems != nil {
		t.Errorf("no entries: %s %q", part, problems)
	}
}
