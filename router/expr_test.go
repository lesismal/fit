package router

import (
	stdhttp "net/http"
	"net/url"
	"strings"
	"testing"
)

func TestParseExpr(t *testing.T) {
	for _, tt := range []struct {
		in, want string
	}{
		{`Host("a.com")`, `Host("a.com")`},
		{"Host( `a.com` ,\"b.com\" )", `Host("a.com", "b.com")`},
		{`Host("a") && Path("/x") || Method("GET")`, `Host("a") && Path("/x") || Method("GET")`},
		{`Host("a") || Path("/x") && Method("GET")`, `Host("a") || Path("/x") && Method("GET")`},
		{`(Host("a") || Path("/x")) && Method("GET")`, `(Host("a") || Path("/x")) && Method("GET")`},
		{`Host("a") && (Path("/x") && Method("GET"))`, `Host("a") && (Path("/x") && Method("GET"))`},
		{`(Host("a") && Path("/x")) && Method("GET")`, `Host("a") && Path("/x") && Method("GET")`},
		{`Host("a") && (Path("/x") || Method("GET"))`, `Host("a") && (Path("/x") || Method("GET"))`},
		{`!Host("a") && !!Path("/")`, `!Host("a") && !!Path("/")`},
		{`((Host("a")))`, `Host("a")`},
		{`!(Host("a") || Host("b"))`, `!(Host("a") || Host("b"))`},
		{`a()`, ``},
		{`Header("X-A") && Header("X-B", "1", "2") && Query("q") && RemoteIP("10.0.0.0/8") && PathRegexp("^/a$") && PathPrefix("/api/")`,
			`Header("X-A") && Header("X-B", "1", "2") && Query("q") && RemoteIP("10.0.0.0/8") && PathRegexp("^/a$") && PathPrefix("/api/")`},
		{`Path("/a\"bé")`, `Path("/a\"bé")`},
	} {
		e, err := ParseExpr(tt.in)
		if tt.want == "" {
			if err == nil {
				t.Errorf("ParseExpr(%s) = %s; want an error", tt.in, e)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseExpr(%s): %v", tt.in, err)
			continue
		}
		if got := e.String(); got != tt.want {
			t.Errorf("ParseExpr(%s) = %s; want %s", tt.in, got, tt.want)
		}
	}
}

func TestParseExprErrors(t *testing.T) {
	for in, want := range map[string]string{
		``:                                     `at byte 1: expected a matcher, ! or (, found the end`,
		`Host("a") &&`:                         `at byte 13: expected a matcher, ! or (, found the end`,
		`Host("a") & Path("/")`:                `at byte 11: expected &&`,
		`Host("a") Path("/")`:                  `at byte 11: expected && or ||, found Path`,
		`(Host("a")`:                           `at byte 11: expected ) or an operator, found the end`,
		`Host("a"))`:                           `at byte 10: expected && or ||, found ")"`,
		`Nope("a")`:                            `at byte 1: there is no matcher Nope`,
		`Host`:                                 `at byte 5: expected ( after Host, found the end`,
		`Host()`:                               `at byte 1: Host needs at least one argument`,
		`Host("a",)`:                           `at byte 10: expected a string, found ")"`,
		`Host("a" "b")`:                        `at byte 10: expected , or ), found "b"`,
		`Host(a)`:                              `at byte 6: expected a string, found a`,
		`Host("a)`:                             `at byte 6: the string is not closed`,
		"Host(`a\n`)":                          `at byte 6: the string is not closed`,
		`Host("\q")`:                           `at byte 6: "\q" is not a valid string`,
		`Host("a") # x`:                        `at byte 11: unexpected '#'`,
		`Host("*")`:                            `at byte 1: Host: "*" is not a host or a "*." wildcard`,
		`Host("a.*.com")`:                      `at byte 1: Host: "a.*.com" is not a host or a "*." wildcard`,
		`Host("")`:                             `at byte 1: Host: "" is not a host or a "*." wildcard`,
		`Path("x")`:                            `at byte 1: Path: "x" does not start with '/'`,
		`Method("GET") && PathPrefix("")`:      `at byte 18: PathPrefix: "" does not start with '/'`,
		`RemoteIP("10.0.0.0/33")`:              `at byte 1: RemoteIP: "10.0.0.0/33" is not an IP address or a CIDR prefix`,
		`PathRegexp("(")`:                      "at byte 1: PathRegexp: \"(\" is not a valid regular expression: error parsing regexp: missing closing ): `(`",
		strings.Repeat("!", 200) + `Host("a")`: `at byte 100: the expression nests deeper than 100`,
		strings.Repeat("(", 200) + `Host("a")`: `at byte 100: the expression nests deeper than 100`,
		`Header("")`:                           `at byte 1: Header: the header's name is empty`,
		`Query("", "1")`:                       `at byte 1: Query: the query parameter's name is empty`,
	} {
		_, err := ParseExpr(in)
		if err == nil || err.Error() != want {
			t.Errorf("ParseExpr(%s): %v; want %s", in, err, want)
		}
	}
	if _, err := ParseExpr(`Host("` + strings.Repeat("a", maxExpr) + `")`); err == nil {
		t.Error("an expression longer than maxExpr parsed")
	}
	// A long chain is not deep: it parses, and prints back without
	// parentheses.
	long := strings.Repeat(`Method("GET") && `, 1000) + `Method("GET")`
	if e, err := ParseExpr(long); err != nil || e.String() != long {
		t.Errorf("a chain of 1001 matchers: %v", err)
	}
}

func TestExprMatch(t *testing.T) {
	e, err := ParseExpr(`(Host("example.com", "*.example.org") && (PathPrefix("/api/") || Path("/health"))) || (Method("POST") && !Header("X-Skip") && Query("v", "2")) || RemoteIP("10.0.0.0/8")`)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		method, host, target, remote, skip string
		want                               bool
	}{
		{"GET", "example.com", "/api/x", "192.0.2.1:1", "", true},
		{"GET", "EXAMPLE.com.:8080", "/health", "192.0.2.1:1", "", true},
		{"GET", "www.example.org", "/api/", "192.0.2.1:1", "", true},
		{"GET", "example.org", "/api/", "192.0.2.1:1", "", false},
		{"GET", "a.b.example.org", "/api/", "192.0.2.1:1", "", false},
		{"GET", "example.com", "/apix", "192.0.2.1:1", "", false},
		{"POST", "other", "/?v=2", "192.0.2.1:1", "", true},
		{"POST", "other", "/?v=2", "192.0.2.1:1", "1", false},
		{"POST", "other", "/?v=3", "192.0.2.1:1", "", false},
		{"GET", "other", "/", "10.9.9.9:1", "", true},
	} {
		u, _ := url.Parse(tt.target)
		r := &stdhttp.Request{Method: tt.method, Host: tt.host, URL: u, Header: stdhttp.Header{}, RemoteAddr: tt.remote}
		if tt.skip != "" {
			r.Header.Set("X-Skip", tt.skip)
		}
		if got := e.Match(r); got != tt.want {
			t.Errorf("%s %s%s from %s, skip %q: %v; want %v", tt.method, tt.host, tt.target, tt.remote, tt.skip, got, tt.want)
		}
	}
}

func TestExprAllocs(t *testing.T) {
	e, err := ParseExpr(`(Host("example.com", "*.example.org") && (PathPrefix("/api/") || Path("/health"))) || (Method("POST") && !Header("X-Skip") && Query("v", "2")) || RemoteIP("10.0.0.0/8")`)
	if err != nil {
		t.Fatal(err)
	}
	r := &stdhttp.Request{Method: "POST", Host: "Www.Example.org:443", URL: &url.URL{Path: "/x", RawQuery: "a=1&v=2"}, Header: stdhttp.Header{}, RemoteAddr: "192.0.2.1:1"}
	if !e.Match(r) {
		t.Fatal("no match")
	}
	if n := testing.AllocsPerRun(100, func() { e.Match(r) }); n != 0 {
		t.Errorf("Match allocates %v times", n)
	}
}

// FuzzParseExpr checks that the parser never panics, and that what it
// parses prints back as an expression that parses to the same.
func FuzzParseExpr(f *testing.F) {
	for _, s := range []string{
		`Host("a.com") && PathPrefix("/api")`,
		`!(Method("GET", "HEAD") || Header("X-A", "1")) && Query("q")`,
		"Path(`/a`) || RemoteIP(\"10.0.0.0/8\", \"::1\")",
		`PathRegexp("^/u/[0-9]+$")`,
		`((Host("*.a.b")))`,
		`Host("a" && `,
		`"\x00"`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		e, err := ParseExpr(s)
		if err != nil {
			return
		}
		again, err := ParseExpr(e.String())
		if err != nil {
			t.Fatalf("%q parsed to %s, which does not parse: %v", s, e, err)
		}
		if again.String() != e.String() {
			t.Fatalf("%q parsed to %s, which parses to %s", s, e, again)
		}
	})
}
