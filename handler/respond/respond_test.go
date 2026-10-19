package respond

import (
	"testing"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/fibtest"
	"github.com/lesismal/fit/module"
)

func load(t *testing.T, params string) (*Respond, error) {
	t.Helper()
	doc := `{"module": "respond"}`
	if params != "" {
		doc = `{"module": "respond", "params": ` + params + `}`
	}
	v, err := config.ParseValue([]byte(doc), "handler")
	if err != nil {
		t.Fatal(err)
	}
	var ref config.ModuleRef
	if err := v.Decode(&ref); err != nil {
		t.Fatal(err)
	}
	m, err := module.Load(module.Handlers, ref, nil)
	if err != nil {
		return nil, err
	}
	return m.(*Respond), nil
}

func TestRespond(t *testing.T) {
	tests := []struct {
		params      string
		status      int
		contentType string
		extra       string
		body        string
	}{
		{"", 200, "", "", ""},
		{`{"body": "hello"}`, 200, "text/plain; charset=utf-8", "", "hello"},
		{`{"status": 404, "body": "{}", "headers": {"content-type": "application/json", "x-a": "1"}}`, 404, "application/json", "1", "{}"},
		{`{"status": 204}`, 204, "", "", ""},
	}
	for _, tt := range tests {
		r, err := load(t, tt.params)
		if err != nil {
			t.Fatalf("%s: %v", tt.params, err)
		}
		// Twice: the response is shared, and must come out the same.
		for range 2 {
			rec := fibtest.Serve(r, "GET", "/anything")
			got := rec.Response
			if !rec.Written || got.StatusCode != tt.status || got.Header.Get("Content-Type") != tt.contentType ||
				got.Header.Get("X-A") != tt.extra || string(got.Body) != tt.body {
				t.Fatalf("%s: got %d %v %q", tt.params, got.StatusCode, got.Header, got.Body)
			}
		}
	}
}

func TestRespondErrors(t *testing.T) {
	tests := []struct {
		params string
		want   string
	}{
		{`{"status": 600}`, `1:44: handler.params.status: 600 is greater than the maximum 599`},
		{`{"status": 101}`, `1:44: handler.params.status: 101 is an informational status, which cannot be a final response`},
		{`{"status": 304, "body": "x"}`, `1:57: handler.params.body: a 304 response cannot have a body`},
		{`{"headers": {"bad name": "1"}}`, `1:45: handler.params.headers: "bad name" is not a valid header name`},
		{`{"headers": {"X-A": "a\nb"}}`, `1:45: handler.params.headers: the value of X-A holds a line break or NUL`},
		{`{"stauts": 200}`, `1:34: handler.params.stauts: unknown field`},
	}
	for _, tt := range tests {
		_, err := load(t, tt.params)
		if err == nil || err.Error() != tt.want {
			t.Errorf("%s: error = %v, want %s", tt.params, err, tt.want)
		}
	}
}
