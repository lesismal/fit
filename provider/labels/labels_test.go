package labels

import (
	"encoding/json"
	"reflect"
	"testing"
)

var kind = Kind{Label: "label", Owner: "container"}

func TestParse(t *testing.T) {
	l, err := Parse(map[string]string{
		"other":                       "not ours",
		"fit.enable":                  "true",
		"fit.port":                    "8080",
		"fit.routes.r.path_regexp":    "^/a, ^/b",
		"fit.routes.r.method":         "GET,POST",
		"fit.services.s.listeners":    "tcp",
		"fit.services.s.server_names": "a.example, b.example",
		"fit.services.s.port":         "5432",
	}, "fit.", []string{"enable"}, kind)
	if err != nil {
		t.Fatal(err)
	}
	if l.Port != 8080 || l.Ports() != 1 {
		t.Errorf("port %d, ports %d", l.Port, l.Ports())
	}
	r := l.Routes["r"]
	if r == nil || !reflect.DeepEqual(r.Match.PathRegexp, []string{"^/a", "^/b"}) || !reflect.DeepEqual(r.Match.Method, []string{"GET", "POST"}) {
		t.Errorf("route: %+v", r)
	}
	s := l.Services["s"]
	if s == nil || !reflect.DeepEqual(s.ServerNames, []string{"a.example", "b.example"}) || s.Port() != 5432 {
		t.Errorf("service: %+v", s)
	}
	part := Part(map[string]*Group{"db": {Set: l, Addrs: []string{"10.0.0.1:8080"}, SvcAddrs: map[string][]string{"s": {"10.0.0.1:5432"}}}})
	var got map[string]any
	if err := json.Unmarshal(part, &got); err != nil {
		t.Fatal(err)
	}
	if len(got["upstreams"].([]any)) != 2 || len(got["routes"].([]any)) != 1 || len(got["services"].([]any)) != 1 {
		t.Errorf("part: %s", part)
	}
}

func TestParseRefused(t *testing.T) {
	for _, tc := range []struct {
		labels map[string]string
		want   string
	}{
		{map[string]string{"fit.port": "0"}, `label fit.port: "0" is not a port`},
		{map[string]string{"fit.port": "http"}, `label fit.port: "http" is not a port`},
		{map[string]string{"fit.upstream": "[]"}, "label fit.upstream: must be a JSON object of an upstream's settings"},
		{map[string]string{"fit.upstream": "null"}, "label fit.upstream: must be a JSON object of an upstream's settings"},
		{map[string]string{"fit.routes.r": "x"}, "label fit.routes.r: is not fit.routes.<name>.<setting>, with a name of letters, digits, '_' and '-'"},
		{map[string]string{"fit.routes._r.path": "/"}, "label fit.routes._r.path: is not fit.routes.<name>.<setting>, with a name of letters, digits, '_' and '-'"},
		{map[string]string{"fit.services.s": "x"}, "label fit.services.s: is not fit.services.<name>.<setting>, with a name of letters, digits, '_' and '-'"},
		{map[string]string{"fit.services.s.port": "70000"}, `label fit.services.s.port: "70000" is not a port`},
		{map[string]string{"fit.services.s.colour": "red"}, `label fit.services.s.colour: a service has no setting "colour"`},
		{map[string]string{"fit.bogus": "x"}, "label fit.bogus: is not a label fit reads"},
		{map[string]string{"fit.routes.r.priority": "high"}, `label fit.routes.r.priority: "high" is not a whole number`},
		{map[string]string{"fit.routes.r.handler": "[]"}, "label fit.routes.r.handler: must be a JSON object of a handler, as in the config"},
		{map[string]string{"fit.routes.r.handler": "null"}, "label fit.routes.r.handler: must be a JSON object of a handler, as in the config"},
	} {
		_, err := Parse(tc.labels, "fit.", nil, kind)
		if err == nil || err.Error() != tc.want {
			t.Errorf("%v: %v, want %q", tc.labels, err, tc.want)
		}
	}
}
