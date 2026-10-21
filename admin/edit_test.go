package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lesismal/fit/config"
)

func TestPointer(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"/", []string{""}},
		{"/a/b", []string{"a", "b"}},
		{"/a~1b/c~0d/~01", []string{"a/b", "c~d", "~1"}},
	} {
		got, err := parsePointer(tt.in)
		if err != nil || fmt.Sprint(got) != fmt.Sprint(tt.want) || (got == nil) != (tt.want == nil) {
			t.Errorf("parsePointer(%q) = %q %v", tt.in, got, err)
		}
		if tt.in != "" && pointerString(got) != tt.in {
			t.Errorf("pointerString(%q) = %q", got, pointerString(got))
		}
	}
	for _, bad := range []string{"a", "/a~", "/a~2"} {
		if _, err := parsePointer(bad); err == nil {
			t.Errorf("parsePointer(%q) succeeded", bad)
		}
	}
}

func TestEditTree(t *testing.T) {
	doc := `{"z": 1.50, "a": [1, 2, 3], "o": {"k": "<&>", "n": null}, "e": {}, "f": []}`
	root, err := decodeJSON([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	// Order, numbers and characters are kept.
	const want = "{\n  \"z\": 1.50,\n  \"a\": [\n    1,\n    2,\n    3\n  ],\n  \"o\": {\n    \"k\": \"<&>\",\n    \"n\": null\n  },\n  \"e\": {},\n  \"f\": []\n}\n"
	if got := string(encodeJSON(root)); got != want {
		t.Fatalf("encoded:\n%s\nwant:\n%s", got, want)
	}
	edit := func(f func(any) (any, error)) string {
		t.Helper()
		r, _ := decodeJSON([]byte(doc))
		r, err := f(r)
		if err != nil {
			return "error: " + err.Error()
		}
		var v any
		_ = json.Unmarshal(encodeJSON(r), &v)
		var out strings.Builder
		e := json.NewEncoder(&out)
		e.SetEscapeHTML(false)
		_ = e.Encode(v)
		return strings.TrimSpace(out.String())
	}
	tok := func(p string) []string { ts, _ := parsePointer(p); return ts }
	v := func(s string) any { x, _ := decodeJSON([]byte(s)); return x }
	for _, tt := range []struct {
		name string
		f    func(any) (any, error)
		want string
	}{
		{"put member", func(r any) (any, error) { return put(r, tok("/o/k"), v(`"x"`)) }, `{"a":[1,2,3],"e":{},"f":[],"o":{"k":"x","n":null},"z":1.5}`},
		{"add member", func(r any) (any, error) { return put(r, tok("/e/new"), v(`true`)) }, `{"a":[1,2,3],"e":{"new":true},"f":[],"o":{"k":"<&>","n":null},"z":1.5}`},
		{"put element", func(r any) (any, error) { return put(r, tok("/a/1"), v(`9`)) }, `{"a":[1,9,3],"e":{},"f":[],"o":{"k":"<&>","n":null},"z":1.5}`},
		{"append", func(r any) (any, error) { return put(r, tok("/f/-"), v(`"x"`)) }, `{"a":[1,2,3],"e":{},"f":["x"],"o":{"k":"<&>","n":null},"z":1.5}`},
		{"past the end", func(r any) (any, error) { return put(r, tok("/a/3"), v(`9`)) }, `error: /a/3: index 3 is past the end of an array of 3`},
		{"leading zero", func(r any) (any, error) { return lookup(r, tok("/a/01")) }, `error: /a/01: "01" is not an array index`},
		{"no parent", func(r any) (any, error) { return put(r, tok("/x/y"), v(`1`)) }, `error: /x: there is no member "x"`},
		{"into a scalar", func(r any) (any, error) { return put(r, tok("/z/y"), v(`1`)) }, `error: /z/y: /z is neither an object nor an array`},
		{"remove member", func(r any) (any, error) { return remove(r, tok("/o")) }, `{"a":[1,2,3],"e":{},"f":[],"z":1.5}`},
		{"remove element", func(r any) (any, error) { return remove(r, tok("/a/0")) }, `{"a":[2,3],"e":{},"f":[],"o":{"k":"<&>","n":null},"z":1.5}`},
		{"remove missing", func(r any) (any, error) { return remove(r, tok("/q")) }, `error: /q: there is no member "q"`},
		{"remove root", func(r any) (any, error) { return remove(r, nil) }, `error: the config itself cannot be deleted`},
		{"merge", func(r any) (any, error) { return mergePatch(r, v(`{"o": {"k": null, "m": 2}, "z": [1]}`)), nil }, `{"a":[1,2,3],"e":{},"f":[],"o":{"m":2,"n":null},"z":[1]}`},
	} {
		if got := edit(tt.f); got != tt.want {
			t.Errorf("%s: %s\nwant %s", tt.name, got, tt.want)
		}
	}
	for _, bad := range []string{``, `{`, `{"a": 1, "a": 2}`, `1 2`} {
		if _, err := decodeJSON([]byte(bad)); err == nil {
			t.Errorf("decodeJSON(%q) succeeded", bad)
		}
	}
}

// send makes a request to the API, with If-Match when ifMatch is set.
func (f *fixture) send(t *testing.T, method, path, contentType, body, ifMatch string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(method, f.api+path, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	return f.do(t, req)
}

func TestEditByPointer(t *testing.T) {
	f := start(t, doc("v1"), &config.Admin{})
	resp, body := f.get(t, "/config/routes/0/handler/params/body")
	if resp.StatusCode != 200 || body != "\"v1\"\n" || resp.Header.Get("ETag") != f.configs.Current().ETag {
		t.Fatalf("GET a value: %d %q %v", resp.StatusCode, body, resp.Header)
	}
	if resp, _ := f.get(t, "/config/routes/3"); resp.StatusCode != 404 {
		t.Fatalf("GET a missing value: %d", resp.StatusCode)
	}

	// PUT a value.
	resp, body = f.send(t, "PUT", "/config/routes/0/handler/params/body", "application/json", `"v2"`, "")
	if resp.StatusCode != 200 || resp.Header.Get(VersionHeader) != "2" || f.served(t) != "v2" {
		t.Fatalf("PUT: %d %s, serving %q", resp.StatusCode, body, f.served(t))
	}
	// PATCH merges.
	resp, body = f.send(t, "PATCH", "/config/routes/0/handler/params", "application/merge-patch+json", `{"body": "v3", "status": 201}`, "")
	if resp.StatusCode != 200 || f.served(t) != "v3" {
		t.Fatalf("PATCH: %d %s, serving %q", resp.StatusCode, body, f.served(t))
	}
	// Append a route, then delete the first.
	resp, body = f.send(t, "PUT", "/config/routes/-", "application/json", `{"name": "second", "handler": {"module": "respond", "params": {"body": "v4"}}}`, "")
	if resp.StatusCode != 200 {
		t.Fatalf("PUT -: %d %s", resp.StatusCode, body)
	}
	resp, body = f.send(t, "DELETE", "/config/routes/0", "", "", "")
	if resp.StatusCode != 200 || f.served(t) != "v4" {
		t.Fatalf("DELETE: %d %s, serving %q", resp.StatusCode, body, f.served(t))
	}
	_, body = f.get(t, "/config")
	const want = `{
  "listeners": [
    {
      "name": "web",
      "address": "127.0.0.1:0"
    }
  ],
  "routes": [
    {
      "name": "second",
      "handler": {
        "module": "respond",
        "params": {
          "body": "v4"
        }
      }
    }
  ]
}
`
	if body != want {
		t.Fatalf("GET /config:\n%s\nwant:\n%s", body, want)
	}
	// PUT of the whole config keeps it as written.
	resp, _ = f.send(t, "PUT", "/config", "application/json", doc("v5"), "")
	if _, got := f.get(t, "/config"); resp.StatusCode != 200 || got != doc("v5") {
		t.Fatalf("PUT /config: %d %q", resp.StatusCode, got)
	}
}

// TestEditsRefused: edits that leave the config invalid, or cannot be made,
// change nothing.
func TestEditsRefused(t *testing.T) {
	f := start(t, doc("v1"), &config.Admin{})
	for _, tt := range []struct {
		method, path, contentType, body string
		status                          int
		want                            string
	}{
		{"PATCH", "/config/listeners/0", "application/merge-patch+json", `{"address": "bogus"}`, 400,
			`{"path":"listeners[0].address","line":5,"column":18,"message":"\"bogus\" is not a host and port, as in \":8080\" or \"127.0.0.1:8080\""}`},
		{"PUT", "/config/routes/0/handler/module", "application/json", `"nope"`, 400, `there is no module \"nope\"`},
		{"DELETE", "/config/listeners", "", "", 400, `"path":"listeners","message":"at least one listener is needed, or a route that matches a host name for HTTPS by default"`},
		{"PUT", "/config/routes/0/bogus", "application/json", `1`, 400, `"path":"routes[0].bogus","line":16,"column":7,"message":"unknown field"`},
		{"PUT", "/config/routes/0", "application/json", `{`, 400, `the body is not JSON`},
		{"PUT", "/config/nope/x", "application/json", `1`, 404, `there is no member \"nope\"`},
		{"PATCH", "/config/routes/5", "application/json", `{}`, 404, `index 5 is past the end`},
		{"DELETE", "/config", "", "", 400, `the config itself cannot be deleted`},
		{"PUT", "/config/routes/0", "text/plain", `{}`, 415, `application/json`},
		{"GET", "/config/a~2b", "", "", 400, `not a JSON pointer`},
	} {
		resp, body := f.send(t, tt.method, tt.path, tt.contentType, tt.body, "")
		if resp.StatusCode != tt.status || !strings.Contains(body, tt.want) {
			t.Errorf("%s %s %s: %d %s", tt.method, tt.path, tt.body, resp.StatusCode, body)
		}
	}
	if v := f.configs.Current(); v.Number != 1 || string(v.Data()) != doc("v1") || f.served(t) != "v1" {
		t.Fatalf("after refused edits: version %d %q", v.Number, v.Data())
	}
}

func TestIfMatch(t *testing.T) {
	f := start(t, doc("v1"), &config.Admin{})
	etag := f.configs.Current().ETag
	if resp, _ := f.send(t, "GET", "/config", "", "", ""); resp.Header.Get("ETag") != etag {
		t.Fatalf("ETag %q, want %q", resp.Header.Get("ETag"), etag)
	}
	req, _ := http.NewRequest("GET", f.api+"/config", nil)
	req.Header.Set("If-None-Match", etag)
	if resp, _ := f.do(t, req); resp.StatusCode != 304 {
		t.Fatalf("If-None-Match: %d", resp.StatusCode)
	}

	resp, body := f.send(t, "PUT", "/config/routes/0/handler/params/body", "application/json", `"v2"`, etag)
	if resp.StatusCode != 200 {
		t.Fatalf("PUT with the right ETag: %d %s", resp.StatusCode, body)
	}
	next := resp.Header.Get("ETag")
	if next == etag || next != f.configs.Current().ETag {
		t.Fatalf("ETag after the change %q, before %q", next, etag)
	}
	// The ETag is stale now: every kind of change is refused.
	for _, r := range [][3]string{
		{"PUT", "/config/routes/0/handler/params/body", `"v3"`},
		{"PATCH", "/config/routes/0", `{}`},
		{"DELETE", "/config/routes/0", ``},
		{"POST", "/load", doc("v3")},
		{"POST", "/rollback/1", ``},
	} {
		resp, body := f.send(t, r[0], r[1], "application/json", r[2], etag)
		if resp.StatusCode != 412 || !strings.Contains(body, "If-Match") {
			t.Errorf("%s %s with a stale ETag: %d %s", r[0], r[1], resp.StatusCode, body)
		}
	}
	if resp, body := f.send(t, "POST", "/load", "application/json", doc("v3"), "*"); resp.StatusCode != 200 {
		t.Errorf("If-Match *: %d %s", resp.StatusCode, body)
	}
	if resp, body := f.send(t, "POST", "/load", "application/json", doc("v4"), `"other", `+f.configs.Current().ETag); resp.StatusCode != 200 {
		t.Errorf("If-Match with a list: %d %s", resp.StatusCode, body)
	}
	if f.configs.Current().Number != 4 {
		t.Fatalf("version %d", f.configs.Current().Number)
	}
}

// TestConcurrentWriters: writers that read the config, change it and write
// it back with If-Match each either apply their change on top of all the
// others or are told to read again; and writers without If-Match lose
// nothing either, since each change is made to the config before it.
func TestConcurrentWriters(t *testing.T) {
	// Its 16 clients are max_conns' default: a connection a client has let
	// go keeps its slot until the server sees it close, so one dialed
	// meanwhile would be cut off, which TestConnectionLimit tests, not this.
	f := start(t, doc("v0"), &config.Admin{History: 100, MaxConns: 64})
	const n = 8
	var wg sync.WaitGroup
	var conflicts atomic.Int64
	for i := range n {
		wg.Go(func() {
			for {
				resp, current := f.get(t, "/config")
				var c struct{ Routes []json.RawMessage }
				if err := json.Unmarshal([]byte(current), &c); err != nil {
					t.Error(err)
					return
				}
				routes := append(c.Routes, json.RawMessage(fmt.Sprintf(`{"name": "a%d", "priority": -1, "handler": {"module": "respond"}}`, i)))
				data, _ := json.Marshal(routes)
				r, body := f.send(t, "PUT", "/config/routes", "application/json", string(data), resp.Header.Get("ETag"))
				switch r.StatusCode {
				case 200:
					return
				case 412:
					conflicts.Add(1)
				default:
					t.Errorf("writer %d: %d %s", i, r.StatusCode, body)
					return
				}
			}
		})
		wg.Go(func() {
			r, body := f.send(t, "PUT", "/config/routes/-", "application/json", fmt.Sprintf(`{"name": "b%d", "priority": -1, "handler": {"module": "respond"}}`, i), "")
			if r.StatusCode != 200 {
				t.Errorf("appender %d: %d %s", i, r.StatusCode, body)
			}
		})
	}
	wg.Wait()
	_, current := f.get(t, "/config")
	for i := range n {
		for _, name := range []string{fmt.Sprint("a", i), fmt.Sprint("b", i)} {
			if strings.Count(current, `"name": "`+name+`"`) != 1 {
				t.Errorf("route %s is not in the config once", name)
			}
		}
	}
	if v := f.configs.Current().Number; v != 2*n+1 {
		t.Errorf("version %d, want %d", v, 2*n+1)
	}
	t.Logf("%d conflicts", conflicts.Load())
}

func TestRollback(t *testing.T) {
	f := start(t, doc("v1"), &config.Admin{History: 3})
	for _, body := range []string{"v2", "v3"} {
		if resp, out := f.load(t, doc(body)); resp.StatusCode != 200 {
			t.Fatalf("load %s: %d %s", body, resp.StatusCode, out)
		}
	}
	resp, body := f.send(t, "POST", "/rollback/1", "", "", "")
	if resp.StatusCode != 200 || resp.Header.Get(VersionHeader) != "4" || f.served(t) != "v1" {
		t.Fatalf("rollback to 1: %d %s, serving %q", resp.StatusCode, body, f.served(t))
	}
	cur := f.configs.Current()
	v1, _ := f.configs.Version(1)
	if v1 != nil || string(cur.Data()) != doc("v1") || cur.Source != SourceRollback {
		// Version 1 has left the history of three, but its config is back.
		t.Fatalf("after the rollback: v1 kept %v, current %q from %s", v1 != nil, cur.Data(), cur.Source)
	}
	if v2, _ := f.configs.Version(2); v2 == nil || cur.ETag != f.configs.Versions()[2].ETag {
		t.Fatal("history after the rollback")
	}
	// Version 1 is no longer kept, so it cannot be rolled back to again.
	if resp, body := f.send(t, "POST", "/rollback/1", "", "", ""); resp.StatusCode != 404 || !strings.Contains(body, "version 1 is not kept") {
		t.Fatalf("rollback to a version gone: %d %s", resp.StatusCode, body)
	}
	if resp, _ := f.send(t, "POST", "/rollback/x", "", "", ""); resp.StatusCode != 404 {
		t.Fatalf("rollback to x: %d", resp.StatusCode)
	}
	// A rollback to the version it follows.
	resp, _ = f.send(t, "POST", "/rollback/3", "", "", cur.ETag)
	if resp.StatusCode != 200 || f.served(t) != "v3" {
		t.Fatalf("rollback to 3: %d, serving %q", resp.StatusCode, f.served(t))
	}
	_, out := f.get(t, "/versions")
	var vs struct {
		Versions []*Version `json:"versions"`
	}
	_ = json.Unmarshal([]byte(out), &vs)
	if len(vs.Versions) != 3 || vs.Versions[2].Number != 5 || vs.Versions[2].Source != SourceRollback || vs.Versions[2].ETag == "" {
		t.Fatalf("GET /versions: %s", out)
	}
}
