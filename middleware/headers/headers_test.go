package headers

import (
	"maps"
	stdhttp "net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/fibtest"
	"github.com/lesismal/fit/module"
)

func load(t *testing.T, params string) (module.Middleware, error) {
	t.Helper()
	v, err := config.ParseValue([]byte(`{"module": "headers", "params": `+params+`}`), "middleware")
	if err != nil {
		t.Fatal(err)
	}
	var ref config.ModuleRef
	if err := v.Decode(&ref); err != nil {
		return nil, err
	}
	m, err := module.Load(module.Middlewares, ref, nil)
	if err != nil {
		return nil, err
	}
	return m.(module.Middleware), nil
}

// fields renders h as sorted "Name: v1,v2" lines.
func fields(h stdhttp.Header) string {
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(h)) {
		b.WriteString(k + ": " + strings.Join(h[k], ",") + "\n")
	}
	return b.String()
}

func TestHeaders(t *testing.T) {
	for _, tt := range []struct {
		name, params string
		in           stdhttp.Header // the request's, and the response the handler writes
		wantRequest  string
		wantResponse string
	}{
		{
			name:         "set replaces, add appends, delete removes",
			params:       `{"request": {"set": {"x-a": "1"}, "add": {"X-B": ["2", "3"]}, "delete": ["X-C"]}}`,
			in:           stdhttp.Header{"X-A": {"old", "older"}, "X-B": {"1"}, "X-C": {"gone"}, "X-D": {"kept"}},
			wantRequest:  "X-A: 1\nX-B: 1,2,3\nX-D: kept\n",
			wantResponse: "X-A: old,older\nX-B: 1\nX-C: gone\nX-D: kept\n",
		},
		{
			name:         "the response",
			params:       `{"response": {"set": {"Server": "fit", "Cache-Control": "no-store"}, "add": {"Vary": ["Accept"]}, "delete": ["X-Powered-By"]}}`,
			in:           stdhttp.Header{"X-Powered-By": {"php"}, "Vary": {"Origin"}},
			wantRequest:  "Vary: Origin\nX-Powered-By: php\n",
			wantResponse: "Cache-Control: no-store\nServer: fit\nVary: Origin,Accept\n",
		},
		{
			name:         "deletion by prefix, then set",
			params:       `{"response": {"delete": ["x-debug-*"], "set": {"X-Debug-Kept": "yes"}}}`,
			in:           stdhttp.Header{"X-Debug-A": {"1"}, "X-Debug-B": {"2"}, "X-Debugger": {"3"}},
			wantRequest:  "X-Debug-A: 1\nX-Debug-B: 2\nX-Debugger: 3\n",
			wantResponse: "X-Debug-Kept: yes\nX-Debugger: 3\n",
		},
		{
			name:         "delete everything",
			params:       `{"response": {"delete": ["*"]}}`,
			in:           stdhttp.Header{"X-A": {"1"}, "Content-Type": {"text/plain"}},
			wantRequest:  "Content-Type: text/plain\nX-A: 1\n",
			wantResponse: "",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m, err := load(t, tt.params)
			if err != nil {
				t.Fatal(err)
			}
			var gotRequest string
			h := m.Wrap(fibhttp.HandlerFunc(func(c *fibhttp.Context) {
				gotRequest = fields(c.Request.Header)
				_ = c.WriteResponse(fibhttp.Response{StatusCode: 200, Header: tt.in.Clone()})
			}))
			for range 2 { // values set are not shared between requests
				r := httptest.NewRequest("GET", "/", nil)
				r.Header = tt.in.Clone()
				res := fibtest.ServeRequest(h, r).Response
				if gotRequest != tt.wantRequest {
					t.Errorf("request:\n%s\nwant:\n%s", gotRequest, tt.wantRequest)
				}
				got := res.Header.Clone()
				delete(got, "Content-Length")
				if fields(got) != tt.wantResponse {
					t.Errorf("response:\n%s\nwant:\n%s", fields(got), tt.wantResponse)
				}
				for _, v := range res.Header {
					if len(v) > 0 {
						v[0] = "changed"
					}
				}
			}
		})
	}
}

func TestResponseOfAnyKind(t *testing.T) {
	m, err := load(t, `{"response": {"set": {"X-Fit": "1"}}}`)
	if err != nil {
		t.Fatal(err)
	}
	for name, h := range map[string]fibhttp.HandlerFunc{
		"Respond": func(c *fibhttp.Context) { _ = c.Respond(404, "text/plain", []byte("no")) },
		"Write":   func(c *fibhttp.Context) { c.Header().Set("X-Other", "1"); _, _ = c.Write([]byte("hi")) },
	} {
		res := fibtest.Serve(m.Wrap(h), "GET", "/").Response
		if res.Header.Get("X-Fit") != "1" {
			t.Errorf("%s: X-Fit not set: %v", name, res.Header)
		}
	}
}

func TestParamErrors(t *testing.T) {
	for params, want := range map[string]string{
		`{}`:              "request or response changes are needed",
		`{"request": {}}`: "request or response changes are needed",
		`{"request": {"set": {"Bad Name": "x"}}}`:   `"Bad Name" is not a valid header name`,
		`{"response": {"add": {"X-A": ["a\nb"]}}}`:  "the value of X-A holds a line break or NUL",
		`{"response": {"delete": ["X:*"]}}`:         `"X:*" is not a valid header name or prefix`,
		`{"response": {"delete": [""]}}`:            `"" is not a valid header name`,
		`{"request": {"set": {"X-A": "a\u0000b"}}}`: "holds a line break or NUL",
	} {
		_, err := load(t, params)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v; want %q", params, err, want)
		}
	}
}
