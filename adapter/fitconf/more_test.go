package fitconf

import (
	"strings"
	"testing"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
)

// kinds is the params of a module registered for these tests only, with
// the kinds of fields no module of fit's has: a config.Value, and lists of
// lists and of maps.
type kinds struct {
	V config.Value     `json:"v,omitzero"`
	L [][]string       `json:"l,omitempty"`
	M []map[string]int `json:"m,omitempty"`
}

func init() {
	module.Register(module.Info{ID: module.Handlers + ".fitconf_test_kinds", New: func() any { return new(kinds) }})
}

func TestMoreConvert(t *testing.T) {
	big := strings.Repeat("1", 70)
	for _, c := range []struct{ name, fit, json string }{
		{"a comment at the end of the file", "admin {}\n# the end", `{"admin": {}}`},
		{"escapes", "admin {\n\taddress \"a\\rb\\qc\\{d\"\n}\n", `{"admin": {"address": "a\rb\\qc{d"}}`},
		{"fields of no known type", "bogus {\n\tflag\n\tbig " + big + "\n\tnone null\n}\n", `{"bogus": {"flag": true, "big": "` + big + `", "none": null}}`},
		{"a Value, lists of lists and of maps", "route {\n\thandler fitconf_test_kinds {\n\t\tv { a 1 }\n\t\tl a b\n\t\tl c\n\t\tm { x 1 }\n\t}\n}\n",
			`{"routes": [{"handler": {"module": "fitconf_test_kinds", "params": {"v": {"a": 1}, "l": [["a", "b"], "c"], "m": [{"x": 1}]}}}]}`},
	} {
		r, err := Adapt([]byte(c.fit))
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if !same(t, r.JSON, []byte(c.json)) {
			t.Errorf("%s:\n got %s\nwant %s", c.name, r.JSON, c.json)
		}
	}
}

func TestMoreErrors(t *testing.T) {
	words := strings.Repeat("\n", maxTokens)
	for _, c := range []struct{ fit, want string }{
		{"bogus 1\nbogus 2\n", "2:1: bogus is given twice"},
		{"bogus x {}\n", "1:7: bogus takes either arguments or a block"},
		{"bogus {\n\ta 1\n\ta 2\n}\n", "3:2: a is given twice"},
		{"bogus {\n\ta x {}\n}\n", "2:4: a takes either arguments or a block"},
		{"route {\n\tmiddleware\n\thandler respond\n}\n", "2:2: middleware takes the name of a module"},
		{"route {\n\tmatch { header x }\n\thandler respond\n}\n", "2:17: header takes a block of keys and their values, not arguments"},
		{"route {\n\tmatch {\n\t\theader {\n\t\t\tX {}\n\t\t}\n\t}\n\thandler respond\n}\n", `4:4: "X" takes a list of values, not a block`},
		{"route {\n\thandler respond {\n\t\theaders {\n\t\t\tX a b\n\t\t}\n\t}\n}\n", "4:8: X takes one value; quote one that holds spaces"},
		{"route {\n\thandler respond {\n\t\tbogus x {}\n\t}\n}\n", "3:9: bogus takes either arguments or a block"},
		{"site a.example {\n\troute {\n\t\tmatch { method GET }\n\t\tmatch { method POST }\n\t\thandler respond\n\t}\n}\n", "4:3: a route's match is given twice"},
		{"admin \"{$X\"\n", "1:7: {$ is not closed with }"},
		{"admin {\n\taddress \"{$}\"\n}\n", "2:10: {$} names no environment variable"},
		{"admin {\n\taddress {$X\n}\n", "2:10: {$ is not closed with }"},
		{"admin {\n\taddress `a`b\n}\n", "a quoted word must end"},
		{"route {\n\thandler fitconf_test_kinds {\n\t\tl x {}\n\t}\n}\n", "3:5: l takes either arguments or a block"},
		{"a {\n{\n}\n}\n", "2:1: a block needs a directive before its {"},
		{strings.Repeat("a {\n", maxDepth+1), "blocks nested more than 64 deep"},
		{words + "\n", "more than 1048576 words"},
		{words + ";", "more than 1048576 words"},
		{words + `"x"`, "more than 1048576 words"},
	} {
		_, err := Adapt([]byte(c.fit))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%.60q: %v, want %q", c.fit, err, c.want)
		}
	}
}

func TestFromJSONErrors(t *testing.T) {
	deep := `{"bogus": ` + strings.Repeat("[", 300) + strings.Repeat("]", 300) + `}`
	for _, c := range []struct{ json, want string }{
		{"", "JSON: EOF"},
		{"{} 1", "JSON: more after the value"},
		{"[]", "JSON: a config is an object"},
		{"{1: 2}", "JSON: "},
		{`{"a": }`, "JSON: "},
		{`{"a": [1, }`, "JSON: "},
		{deep, "JSON: nested too deep"},
		{`{"bogus": [1]}`, "bogus: a list of fewer than two values"},
		{`{"bogus": [{}, 1]}`, "bogus: an object is not a value an argument holds"},
		{`{"bogus": {"a": [1]}}`, "a: a list of fewer than two values"},
		{`{"listeners": [1]}`, "listeners[0]: an object, not a number"},
		{`{"listeners": ["x"]}`, "listeners[0]: an object, not a string"},
		{`{"listeners": [true]}`, "listeners[0]: an object, not a boolean"},
		{`{"listeners": [null]}`, "listeners[0]: an object, not null"},
		{`{"listeners": [[]]}`, "listeners[0]: an object, not a list"},
		{`{"listeners": [{"name": "a", "bogus": [1]}]}`, "bogus: a list of fewer than two values"},
		{`{"listeners": [{"name": "a", "protocols": [{}]}]}`, "protocols: an object is not a value an argument holds"},
		{`{"listeners": [{"name": "a", "address": {}}]}`, "address: an object is not a value an argument holds"},
		{`{"routes": [{"match": {"header": {"X": [{}]}}, "handler": {"module": "respond"}}]}`, "X: an object is not a value"},
		{`{"routes": [{"middleware": [1], "handler": {"module": "respond"}}]}`, "middleware: a module is an object, not a number"},
		{`{"routes": [{"handler": {}}]}`, "handler: a module needs its name, in module"},
		{`{"routes": [{"handler": {"module": "respond", "x": 1}}]}`, "handler: a module has a module and params, not x"},
		{`{"routes": [{"handler": {"module": "respond", "params": 1}}]}`, "handler: a module's params are an object, not a number"},
		{`{"routes": [{"handler": {"module": "nope", "params": {"a": [1]}}}]}`, "a: a list of fewer than two values"},
		{`{"routes": [{"handler": {"module": "fitconf_test_kinds", "params": {"l": [["a"]]}}}]}`, "l: a list of "},
	} {
		if _, err := FromJSON([]byte(c.json)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%.60q: %v, want %q", c.json, err, c.want)
		}
	}
}

// TestFromJSONMore writes what TestRoundTrip's configs do not have, and
// reads it back.
func TestFromJSONMore(t *testing.T) {
	for _, src := range []string{
		`{"admin": null, "bogus": {"n": null, "t": true, "f": false, "s": "x", "l": [1, "2"], "o": {}}}`,
		`{"limits": {"max_conns": "x"}}`,
		`{"routes": [{"handler": {"module": "nope", "params": {"a": 1, "b": {"c": "d"}}}}]}`,
		`{"routes": [{"handler": {"module": "fitconf_test_kinds", "params": {"v": {"a": 1, "b": [1, 2]}}}}]}`,
	} {
		out, err := FromJSON([]byte(src))
		if err != nil {
			t.Errorf("%s: %v", src, err)
			continue
		}
		r, err := Adapt(out)
		if err != nil {
			t.Errorf("%s: %v\n%s", src, err, out)
			continue
		}
		want := strings.Replace(src, `"admin": null, `, "", 1)
		if !same(t, r.JSON, []byte(want)) {
			t.Errorf("%s: the round trip changed it:\n%s\n%s", src, out, r.JSON)
		}
	}

	// A value of the wrong kind is written as it is, for the config's check
	// to report once read back.
	for _, src := range []string{`{"admin": 1}`, `{"listeners": 2}`, `{"routes": [{"match": {"header": 1}}]}`} {
		if _, err := FromJSON([]byte(src)); err != nil {
			t.Errorf("%s: %v", src, err)
		}
	}
}
