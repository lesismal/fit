package router

import (
	"fmt"
	stdhttp "net/http"
	"net/url"
	"testing"
)

func TestHostOnly(t *testing.T) {
	for in, want := range map[string]string{
		"example.com":      "example.com",
		"example.com:8080": "example.com",
		"127.0.0.1:80":     "127.0.0.1",
		"[::1]:8080":       "::1",
		"[::1]":            "::1",
		"::1":              "::1",
		"":                 "",
	} {
		if got := hostOnly(in); got != want {
			t.Errorf("hostOnly(%q) = %q, want %q", in, got, want)
		}
	}
}

// route is a shorthand for a route whose value is its name.
func route(name string, priority int, hosts, paths []string) Route[string] {
	return Route[string]{Hosts: hosts, Paths: paths, Priority: priority, Value: name}
}

func list(s ...string) []string { return s }

// req is a GET request for host and path, the way fib makes one.
func req(host, path string) *stdhttp.Request {
	return &stdhttp.Request{Method: "GET", Host: host, URL: &url.URL{Path: path}, Header: stdhttp.Header{}, RemoteAddr: "192.0.2.1:1234"}
}

// check matches each of cases against r: want "" means no route.
func check(t *testing.T, r *Router[string], cases [][3]string) {
	t.Helper()
	for _, c := range cases {
		host, path, want := c[0], c[1], c[2]
		got, ok := r.Match(req(host, path))
		if ok != (want != "") || got != want {
			t.Errorf("Match(%q, %q) = %q, %v; want %q", host, path, got, ok, want)
		}
	}
}

func TestHost(t *testing.T) {
	r := New([]Route[string]{
		route("exact", 0, list("example.com"), nil),
		route("wild", 0, list("*.example.com"), nil),
		route("upper", 0, list("Mixed.Example.ORG"), nil),
		route("v6", 0, list("[::1]"), nil),
		route("v4", 0, list("127.0.0.1"), nil),
	})
	check(t, r, [][3]string{
		{"example.com", "/", "exact"},
		{"EXAMPLE.com:8080", "/", "exact"},
		{"example.com.", "/", "exact"},
		{"www.example.com", "/", "wild"},
		{"WWW.Example.COM", "/", "wild"},
		{"a.b.example.com", "/", ""},
		{".example.com", "/", ""},
		{"wwwexample.com", "/", ""},
		{"example.org", "/", ""},
		{"mixed.example.org", "/", "upper"},
		{"[::1]:8080", "/", "v6"},
		{"::1", "/", "v6"},
		{"127.0.0.1:80", "/", "v4"},
		{"", "/", ""},
	})
}

// TestDeepWildcard checks "**." hosts: one label or more, below an exact
// host and a "*." wildcard, the longest suffix first, and only where their
// paths match.
func TestDeepWildcard(t *testing.T) {
	r := New([]Route[string]{
		route("deep", 0, list("**.example.com"), nil),
		route("deeper", 0, list("**.b.example.com"), list("/b/*")),
		route("wild", 0, list("*.example.com"), list("/w")),
		route("exact", 0, list("a.b.example.com"), list("/e")),
		route("any", 0, nil, nil),
	})
	check(t, r, [][3]string{
		{"x.example.com", "/", "deep"},
		{"x.y.z.example.com", "/", "deep"},
		{"X.Y.Example.COM", "/", "deep"},
		{"example.com", "/", "any"},
		{"x.example.org", "/", "any"},
		{"a.b.example.com", "/b/1", "deeper"},
		{"A.B.Example.com", "/b/1", "deeper"},
		{"a.b.example.com", "/", "deep"}, // deeper's path does not match
		{"x.example.com", "/w", "wild"},
		{"x.y.example.com", "/w", "deep"}, // * is one label
		{"a.b.example.com", "/e", "exact"},
	})
	// Without "**." hosts, nothing is looked for: a host of many labels
	// costs no more.
	if r := New([]Route[string]{route("wild", 0, list("*.example.com"), nil)}); len(r.deep) != 0 {
		t.Error("deep nodes without deep hosts")
	}
}

func TestPath(t *testing.T) {
	r := New([]Route[string]{
		route("hello", 0, nil, list("/hello", "/hi")),
		route("api", 0, nil, list("/api/*")),
	})
	check(t, r, [][3]string{
		{"", "/hello", "hello"},
		{"", "/hi", "hello"},
		{"", "/hello/", ""},
		{"", "/HELLO", ""},
		{"", "/api/", "api"},
		{"", "/api/v1/users", "api"},
		{"", "/api", ""},
		{"", "/", ""},
	})
	check(t, New([]Route[string]{route("root", 0, nil, list("/*"))}), [][3]string{
		{"", "/", "root"},
		{"", "/anything", "root"},
	})
}

func TestNoRoutes(t *testing.T) {
	check(t, New[string](nil), [][3]string{{"example.com", "/", ""}})
}

func TestOrder(t *testing.T) {
	for _, tt := range []struct {
		name   string
		routes []Route[string]
		cases  [][3]string
	}{
		{
			name: "priority beats specificity",
			routes: []Route[string]{
				route("specific", 0, list("example.com"), list("/a")),
				route("broad", 1, nil, nil),
			},
			cases: [][3]string{{"example.com", "/a", "broad"}},
		},
		{
			name: "a lower priority still serves what the higher does not",
			routes: []Route[string]{
				route("high", 5, list("example.com"), list("/a")),
				route("low", -5, nil, nil),
			},
			cases: [][3]string{
				{"example.com", "/a", "high"},
				{"example.com", "/b", "low"},
				{"other.com", "/a", "low"},
			},
		},
		{
			name: "exact host beats wildcard beats any",
			routes: []Route[string]{
				route("any", 0, nil, list("/a")),
				route("wild", 0, list("*.example.com"), nil),
				route("exact", 0, list("www.example.com"), nil),
			},
			cases: [][3]string{
				{"www.example.com", "/a", "exact"},
				{"api.example.com", "/a", "wild"},
				{"other.com", "/a", "any"},
			},
		},
		{
			name: "host beats path",
			routes: []Route[string]{
				route("path", 0, nil, list("/api/v1")),
				route("host", 0, list("example.com"), nil),
			},
			cases: [][3]string{
				{"example.com", "/api/v1", "host"},
				{"other.com", "/api/v1", "path"},
			},
		},
		{
			name: "exact path beats longest prefix beats shorter prefix beats none",
			routes: []Route[string]{
				route("none", 0, nil, nil),
				route("root", 0, nil, list("/*")),
				route("api", 0, nil, list("/api/*")),
				route("v1", 0, nil, list("/api/v1/*")),
				route("users", 0, nil, list("/api/v1/users")),
			},
			cases: [][3]string{
				{"", "/api/v1/users", "users"},
				{"", "/api/v1/users/1", "v1"},
				{"", "/api/v2", "api"},
				{"", "/x", "root"},
			},
		},
		{
			name: "ties go to the route written first",
			routes: []Route[string]{
				route("first", 0, list("example.com"), list("/a", "/b/*")),
				route("second", 0, list("example.com"), list("/a", "/b/*")),
				route("third", 0, list("*.example.com"), nil),
				route("fourth", 0, list("*.EXAMPLE.com"), nil),
			},
			cases: [][3]string{
				{"example.com", "/a", "first"},
				{"example.com", "/b/c", "first"},
				{"www.example.com", "/", "third"},
			},
		},
		{
			name: "a later route with a higher priority replaces an earlier one",
			routes: []Route[string]{
				route("first", 0, nil, list("/a", "/b/*")),
				route("second", 1, nil, list("/a", "/b/*")),
			},
			cases: [][3]string{
				{"", "/a", "second"},
				{"", "/b/c", "second"},
			},
		},
		{
			name: "a shorter prefix with a higher priority beats a longer one",
			routes: []Route[string]{
				route("long", 0, nil, list("/a/b/*")),
				route("short", 1, nil, list("/a/*")),
				route("exact", 0, nil, list("/a/b/c")),
			},
			cases: [][3]string{
				{"", "/a/b/c", "short"},
				{"", "/a/b/d", "short"},
			},
		},
		{
			name: "each pattern of a route is matched on its own",
			routes: []Route[string]{
				route("mixed", 0, list("example.com", "*.example.org"), list("/x", "/y/*")),
				route("fallback", -1, nil, nil),
			},
			cases: [][3]string{
				{"example.com", "/x", "mixed"},
				{"www.example.org", "/y/z", "mixed"},
				{"example.org", "/x", "fallback"},
				{"example.com", "/z", "fallback"},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			check(t, New(tt.routes), tt.cases)
		})
	}
}

func TestMatchAllocs(t *testing.T) {
	routes := []Route[*int]{
		{Hosts: list("*.example.com"), Paths: list("/api/*")},
		{Hosts: list("example.com"), Paths: list("/hello", "/static/*"), Priority: 1},
		{Paths: list("/*")},
	}
	for i := range 100 {
		routes = append(routes, Route[*int]{Hosts: list(fmt.Sprintf("h%d.example.net", i)), Paths: list(fmt.Sprintf("/p%d/*", i))})
	}
	for i := range routes {
		routes[i].Value = new(int)
	}
	r := New(routes)
	for _, c := range [][2]string{
		{"example.com", "/hello"},
		{"EXAMPLE.com:8080", "/static/css/site.css"},
		{"www.Example.com", "/api/v1"},
		{"h42.example.net", "/p42/x"},
		{"[::1]:8080", "/nothing"},
	} {
		rq := req(c[0], c[1])
		if n := testing.AllocsPerRun(100, func() { r.Match(rq) }); n != 0 {
			t.Errorf("Match(%q, %q) allocates %v times", c[0], c[1], n)
		}
	}
}

func BenchmarkMatch(b *testing.B) {
	var routes []Route[int]
	for i := range 100 {
		routes = append(routes, Route[int]{
			Hosts: list(fmt.Sprintf("h%d.example.com", i)),
			Paths: list(fmt.Sprintf("/api/v%d/*", i), fmt.Sprintf("/p%d", i)),
			Value: i,
		})
	}
	routes = append(routes, Route[int]{Hosts: list("*.example.com"), Paths: list("/*"), Value: -1})
	r := New(routes)
	rq := req("H50.example.com:8080", "/api/v50/users/1")
	b.ReportAllocs()
	for b.Loop() {
		r.Match(rq)
	}
}
