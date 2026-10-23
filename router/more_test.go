package router

import "testing"

func TestHostOnlyUnclosed(t *testing.T) {
	if got := hostOnly("[::1"); got != "[::1" {
		t.Errorf("hostOnly(%q) = %q", "[::1", got)
	}
}

// TestQueryHexEscapes: escapes in the query, in either case, match the
// byte they stand for.
func TestQueryHexEscapes(t *testing.T) {
	m := Query(map[string][]string{"v": {"a/b"}})
	for raw, want := range map[string]bool{"v=a%2fb": true, "v=a%2Fb": true, "v=a%2gb": false} {
		r := req("example.com", "/")
		r.URL.RawQuery = raw
		if got := m.Match(r); got != want {
			t.Errorf("%s: %v, want %v", raw, got, want)
		}
	}
}

// TestSameBucket: routes for one host and path are tried best first, in
// whatever order they were given.
func TestSameBucket(t *testing.T) {
	post := must(Methods("POST"), nil)
	r := New([]Route[string]{
		{Paths: []string{"/a"}, Matchers: []Matcher{post}, Value: "post"},
		{Paths: []string{"/a"}, Value: "any"},
	})
	check(t, r, [][3]string{{"example.com", "/a", "any"}})
	p := req("example.com", "/a")
	p.Method = "POST"
	if got, ok := r.Match(p); !ok || got != "post" {
		t.Errorf("POST /a: %q %v", got, ok)
	}
}

// TestPathTwice: a route that lists a path twice is matched as once.
func TestPathTwice(t *testing.T) {
	r := New([]Route[string]{{Paths: []string{"/a", "/a"}, Matchers: []Matcher{must(Methods("GET"), nil)}, Value: "a"}})
	check(t, r, [][3]string{{"example.com", "/a", "a"}, {"example.com", "/b", ""}})
}
