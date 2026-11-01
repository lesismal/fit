package model

import (
	"slices"
	"strings"
	"testing"

	"github.com/lesismal/fit/config"
)

func TestFormField(t *testing.T) {
	const b = "B"
	part := func(name, extra, value string) string {
		return "--" + b + "\r\nContent-Disposition: form-data; name=\"" + name + "\"" + extra + "\r\n\r\n" + value + "\r\n"
	}
	for _, tc := range []struct {
		body, want string
		ok         bool
	}{
		{part("model", "", "whisper-1") + "--B--\r\n", "whisper-1", true},
		// After a file whose bytes look like a header and a boundary.
		{part("file", "; filename=\"a\"", "x\r\n--Bx\r\nname=\"model\"") + part("model", "", "m") + "--B--", "m", true},
		// A preamble, and a header line in another case.
		{"preamble\r\n--B\r\ncontent-disposition: form-data; name=model\r\n\r\nm2\r\n--B--", "m2", true},
		{part("model", "", "") + "--B--", "", true},
		{part("other", "", "x") + "--B--", "", false},
		{"no boundary", "", false},
		{"x--B\r\n", "", false},
		{"--Bjunk", "", false},
		{"--B\r\nContent-Disposition: form-data; name=model\r\n", "", false},
		{"--B\r\nContent-Disposition: form-data; name=model\r\n\r\nm", "", false},
		{part("model", "", "m")[:0] + "--B\r\nContent-Disposition: attachment; name=model\r\n\r\nm\r\n--B--", "", false},
	} {
		s, e, err := FormField([]byte(tc.body), b, "model")
		if (err == nil) != tc.ok || err == nil && tc.body[s:e] != tc.want {
			t.Errorf("%q: %d %d %v", tc.body, s, e, err)
		}
	}
	for ct, want := range map[string]string{
		"multipart/form-data; boundary=abc":                        "abc",
		"Multipart/Form-Data; boundary=\"a b\"":                    "a b",
		"multipart/form-data":                                      "",
		"multipart/mixed; boundary=abc":                            "",
		"application/json":                                         "",
		"multipart/form-data; boundary=" + strings.Repeat("a", 71): "",
		"bad;;": "",
	} {
		if got := FormBoundary(ct); got != want {
			t.Errorf("FormBoundary(%q) = %q", ct, got)
		}
	}
}

func TestKinds(t *testing.T) {
	if !slices.Equal(APIs, config.LLMAPIs) {
		t.Fatalf("the kinds %v differ from the config's %v", APIs, config.LLMAPIs)
	}
	for _, api := range APIs {
		if Protocol(api) != config.LLMProtocol(api) || !slices.Contains(Protocols, Protocol(api)) {
			t.Errorf("protocol of %s", api)
		}
		for _, p := range ProxyPaths[Protocol(api)] {
			lacks := api == APIAzureOpenAI && (p == "moderations" || p == "images/variations" || p == "rerank")
			if Serves(api, p) == lacks {
				t.Errorf("%s serves %s: %v", api, p, !lacks)
			}
		}
	}
	if Serves(APIOpenAI, "messages") || Serves("nope", "chat/completions") {
		t.Error("serves a path of another protocol")
	}
}
