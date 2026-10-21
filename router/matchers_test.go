package router

import (
	"fmt"
	stdhttp "net/http"
	"net/url"
	"testing"
)

func must(m Matcher, err error) Matcher {
	if err != nil {
		panic(err)
	}
	return m
}

func TestMatchers(t *testing.T) {
	get := func(mod func(r *stdhttp.Request)) *stdhttp.Request {
		r := req("example.com", "/")
		mod(r)
		return r
	}
	header := func(kv ...string) func(r *stdhttp.Request) {
		return func(r *stdhttp.Request) {
			for i := 0; i < len(kv); i += 2 {
				r.Header.Add(kv[i], kv[i+1])
			}
		}
	}
	query := func(q string) func(r *stdhttp.Request) { return func(r *stdhttp.Request) { r.URL.RawQuery = q } }
	from := func(a string) func(r *stdhttp.Request) { return func(r *stdhttp.Request) { r.RemoteAddr = a } }
	path := func(p string) func(r *stdhttp.Request) { return func(r *stdhttp.Request) { r.URL.Path = p } }
	method := func(m string) func(r *stdhttp.Request) { return func(r *stdhttp.Request) { r.Method = m } }

	for _, tt := range []struct {
		name string
		m    Matcher
		yes  []func(*stdhttp.Request)
		no   []func(*stdhttp.Request)
	}{
		{
			name: "methods",
			m:    Methods("GET", "POST"),
			yes:  []func(*stdhttp.Request){method("GET"), method("POST")},
			no:   []func(*stdhttp.Request){method("PUT"), method("get")},
		},
		{
			name: "header present",
			m:    Headers(map[string][]string{"x-env": nil}),
			yes:  []func(*stdhttp.Request){header("X-Env", ""), header("X-Env", "prod")},
			no:   []func(*stdhttp.Request){header(), header("X-Other", "prod")},
		},
		{
			name: "header values",
			m:    Headers(map[string][]string{"X-Env": {"prod", "staging"}, "Accept": nil}),
			yes: []func(*stdhttp.Request){
				header("X-Env", "prod", "Accept", "*/*"),
				header("X-Env", "dev", "X-Env", "staging", "Accept", ""),
			},
			no: []func(*stdhttp.Request){
				header("X-Env", "prod"),
				header("X-Env", "Prod", "Accept", "*/*"),
				header("X-Env", "prod,staging", "Accept", "*/*"),
			},
		},
		{
			name: "query",
			m:    Query(map[string][]string{"v": {"2", "a b"}, "debug": nil}),
			yes: []func(*stdhttp.Request){
				query("v=2&debug"),
				query("debug=0&v=1&v=a+b"),
				query("%64ebug=&v=a%20b"),
			},
			no: []func(*stdhttp.Request){
				query(""),
				query("v=2"),
				query("debug&v=3"),
				query("debug&v=2%"),
				query("debugx&v=2"),
				query("debug&v=%zz"),
			},
		},
		{
			name: "remote IP",
			m:    must(RemoteIP("10.0.0.0/8", "192.0.2.7", "2001:db8::/32", "fe80::/10")),
			yes: []func(*stdhttp.Request){
				from("10.1.2.3:80"), from("192.0.2.7:1"), from("[2001:db8::1]:443"),
				from("[::ffff:10.0.0.1]:80"), from("10.0.0.1"), from("[fe80::1%eth0]:1"),
			},
			no: []func(*stdhttp.Request){
				from("11.0.0.1:80"), from("192.0.2.8:1"), from("[2001:db9::1]:443"), from(""), from("pipe"),
			},
		},
		{
			name: "path regexp",
			m:    must(PathRegexp(`^/users/[0-9]+$`, `\.php$`)),
			yes:  []func(*stdhttp.Request){path("/users/42"), path("/a/b.php")},
			no:   []func(*stdhttp.Request){path("/users/x"), path("/users/42/x"), path("/php")},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for i, mod := range tt.yes {
				if r := get(mod); !tt.m.Match(r) {
					t.Errorf("yes[%d]: %s %s?%s from %s %v: no match", i, r.Method, r.URL.Path, r.URL.RawQuery, r.RemoteAddr, r.Header)
				}
			}
			for i, mod := range tt.no {
				if r := get(mod); tt.m.Match(r) {
					t.Errorf("no[%d]: %s %s?%s from %s %v: matches", i, r.Method, r.URL.Path, r.URL.RawQuery, r.RemoteAddr, r.Header)
				}
			}
		})
	}
}

func TestMatcherErrors(t *testing.T) {
	for _, s := range []string{"10.0.0.0/33", "10.0.0", "", "::ffff:10.0.0.0/104", "fe80::1%eth0", "a.example"} {
		if _, err := RemoteIP(s); err == nil {
			t.Errorf("RemoteIP(%q): no error", s)
		}
	}
	if _, err := PathRegexp(`(`); err == nil {
		t.Error("PathRegexp(`(`): no error")
	}
	if p, err := ParsePrefix("10.1.2.3/8"); err != nil || p.String() != "10.0.0.0/8" {
		t.Errorf("ParsePrefix(10.1.2.3/8) = %v, %v", p, err)
	}
	if p, err := ParsePrefix("::ffff:10.0.0.1"); err != nil || p.String() != "10.0.0.1/32" {
		t.Errorf("ParsePrefix(::ffff:10.0.0.1) = %v, %v", p, err)
	}
}

// mroute is a route with matchers whose value is its name.
func mroute(name string, priority int, hosts, paths []string, ms ...Matcher) Route[string] {
	r := route(name, priority, hosts, paths)
	r.Matchers = ms
	return r
}

func TestMatcherOrder(t *testing.T) {
	type c struct {
		method, host, path, want string
	}
	for _, tt := range []struct {
		name   string
		routes []Route[string]
		cases  []c
	}{
		{
			name: "a route whose matchers fail gives way to the next",
			routes: []Route[string]{
				mroute("post", 0, nil, list("/a"), Methods("POST")),
				mroute("get", 0, nil, list("/a"), Methods("GET")),
			},
			cases: []c{{"GET", "x", "/a", "get"}, {"POST", "x", "/a", "post"}, {"PUT", "x", "/a", ""}},
		},
		{
			name: "more matchers beat fewer, at equal priority and specificity",
			routes: []Route[string]{
				route("plain", 0, nil, list("/a")),
				mroute("get", 0, nil, list("/a"), Methods("GET")),
				mroute("get-v2", 0, nil, list("/a"), Methods("GET"), Headers(map[string][]string{"X-V": {"2"}})),
			},
			cases: []c{{"GET", "x", "/a", "get"}, {"POST", "x", "/a", "plain"}},
		},
		{
			name: "specificity beats matchers",
			routes: []Route[string]{
				mroute("get-prefix", 0, nil, list("/a/*"), Methods("GET")),
				route("exact", 0, nil, list("/a/b")),
				route("host", 0, list("h.com"), list("/a/*")),
			},
			cases: []c{
				{"GET", "x", "/a/b", "exact"},
				{"GET", "x", "/a/c", "get-prefix"},
				{"GET", "h.com", "/a/b", "host"},
				{"POST", "x", "/a/c", ""},
			},
		},
		{
			name: "priority beats specificity, matchers included",
			routes: []Route[string]{
				route("exact", 0, list("h.com"), list("/a")),
				mroute("get", 1, nil, nil, Methods("GET")),
			},
			cases: []c{{"GET", "h.com", "/a", "get"}, {"POST", "h.com", "/a", "exact"}},
		},
		{
			name: "a failing exact path falls back to a prefix, then to a less specific host",
			routes: []Route[string]{
				mroute("exact-post", 0, list("h.com"), list("/a/b"), Methods("POST")),
				mroute("prefix-put", 0, list("h.com"), list("/a/*"), Methods("PUT")),
				mroute("wild", 0, list("*.com"), list("/a/b"), Methods("PUT", "GET")),
				route("any", 0, nil, nil),
			},
			cases: []c{
				{"POST", "h.com", "/a/b", "exact-post"},
				{"PUT", "h.com", "/a/b", "prefix-put"},
				{"GET", "h.com", "/a/b", "wild"},
				{"DELETE", "h.com", "/a/b", "any"},
			},
		},
		{
			name: "ties go to the route written first",
			routes: []Route[string]{
				mroute("first", 0, nil, list("/a"), Methods("GET")),
				mroute("second", 0, nil, list("/a"), Methods("GET")),
			},
			cases: []c{{"GET", "x", "/a", "first"}},
		},
		{
			name: "routes after one without matchers are never tried",
			routes: []Route[string]{
				route("plain", 1, nil, list("/a")),
				mroute("never", 0, nil, list("/a"), panicMatcher{}),
			},
			cases: []c{{"GET", "x", "/a", "plain"}},
		},
		{
			name: "a lower priority route's matchers do not run once a higher one matched",
			routes: []Route[string]{
				route("high", 1, list("h.com"), list("/a/*")),
				mroute("low", 0, list("h.com"), list("/*"), panicMatcher{}),
				mroute("low-any", 0, nil, list("/a/b"), panicMatcher{}),
			},
			cases: []c{{"GET", "h.com", "/a/b", "high"}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := New(tt.routes)
			for _, c := range tt.cases {
				rq := req(c.host, c.path)
				rq.Method = c.method
				got, ok := r.Match(rq)
				if ok != (c.want != "") || got != c.want {
					t.Errorf("Match(%s %s%s) = %q, %v; want %q", c.method, c.host, c.path, got, ok, c.want)
				}
			}
		})
	}
}

type panicMatcher struct{}

func (panicMatcher) Match(*stdhttp.Request) bool { panic("ran") }

func TestMatchersAllocs(t *testing.T) {
	ip := must(RemoteIP("10.0.0.0/8", "2001:db8::/32"))
	routes := []Route[*int]{
		{Paths: list("/a"), Matchers: []Matcher{Methods("POST")}},
		{Paths: list("/a"), Matchers: []Matcher{Headers(map[string][]string{"X-Env": {"prod"}}), Query(map[string][]string{"v": {"a b"}}), ip}},
		{Paths: list("/*"), Matchers: []Matcher{Methods("PUT")}},
	}
	for i := range 50 {
		routes = append(routes, Route[*int]{Hosts: list(fmt.Sprintf("h%d.example.net", i)), Paths: list("/*"), Matchers: []Matcher{Methods("GET")}})
	}
	for i := range routes {
		routes[i].Value = new(int)
	}
	r := New(routes)
	rq := &stdhttp.Request{Method: "GET", Host: "other.example.net", URL: &url.URL{Path: "/a", RawQuery: "x=1&v=a+b"},
		Header: stdhttp.Header{"X-Env": {"prod"}}, RemoteAddr: "[2001:db8::1]:443"}
	if v, ok := r.Match(rq); !ok || v != routes[1].Value {
		t.Fatalf("Match picked the wrong route")
	}
	if n := testing.AllocsPerRun(100, func() { r.Match(rq) }); n != 0 {
		t.Errorf("Match allocates %v times", n)
	}
}
