package config

import (
	"strings"
	"testing"
)

type testParams struct {
	Status  int               `json:"status,omitempty" min:"100" max:"599"`
	Kind    string            `json:"kind" fit:"required" enum:"a,b"`
	Headers map[string]string `json:"headers,omitempty"`
	Weight  float64           `json:"weight,omitempty" min:"0"`
	Limit   uint8             `json:"limit,omitempty"`
	Nested  *struct {
		On bool `json:"on"`
	} `json:"nested,omitempty"`
	Extra Value `json:"extra,omitzero"`
}

func TestValueDecode(t *testing.T) {
	v, err := ParseValue([]byte(`{"status": 201, "kind": "b", "headers": {"X-A": "1"}, "weight": 0.5, "limit": 7, "nested": {"on": true}, "extra": [1, "two"]}`), "handler.params")
	if err != nil {
		t.Fatal(err)
	}
	var p testParams
	if err := v.Decode(&p); err != nil {
		t.Fatal(err)
	}
	if p.Status != 201 || p.Kind != "b" || p.Headers["X-A"] != "1" || p.Weight != 0.5 || p.Limit != 7 || !p.Nested.On {
		t.Fatalf("decoded %+v", p)
	}
	if out, _ := p.Extra.MarshalJSON(); string(out) != `[1,"two"]` || p.Extra.Path() != "handler.params.extra" {
		t.Fatalf("extra = %s at %q", out, p.Extra.Path())
	}
}

func TestValueDecodeErrors(t *testing.T) {
	v, err := ParseValue([]byte(`{"status": 99, "kind": "c", "headers": {"X-A": 1}, "weight": -1, "limit": 300, "nested": {"on": "yes"}}`), "p")
	if err != nil {
		t.Fatal(err)
	}
	var p testParams
	err = v.Decode(&p)
	want := strings.Join([]string{
		`1:12: p.status: 99 is less than the minimum 100`,
		`1:24: p.kind: "c" is not one of "a", "b"`,
		`1:48: p.headers["X-A"]: expected a string, got a number`,
		`1:62: p.weight: -1 is less than the minimum 0`,
		`1:75: p.limit: 300 is not a non-negative integer in range for uint8`,
		`1:97: p.nested.on: expected a boolean, got a string`,
	}, "\n")
	if err == nil || err.Error() != want {
		t.Fatalf("errors:\n%v\nwant:\n%s", err, want)
	}
}

func TestAbsentValueDecodesAsEmptyObject(t *testing.T) {
	c, err := Load([]byte(`{"listeners": [{"name": "a", "address": ":1"}],
"routes": [{"handler": {"module": "m"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	var p testParams
	err = c.Routes[0].Handler.Params.Decode(&p)
	if err == nil || err.Error() != `2:24: routes[0].handler.params.kind: missing required field` {
		t.Fatalf("error = %v", err)
	}
	var empty struct{}
	if err := (Value{}).Decode(&empty); err != nil {
		t.Fatalf("zero Value: %v", err)
	}
}

func TestValueNavigation(t *testing.T) {
	v, err := ParseValue([]byte("{\n \"a\": [10, {\"b\": true}]\n}"), "")
	if err != nil {
		t.Fatal(err)
	}
	b := v.Field("a").Index(1).Field("b")
	if line, col := b.Pos(); b.IsZero() || b.Path() != "a[1].b" || line != 2 || col != 18 {
		t.Fatalf("b: zero=%v path=%q at %d:%d", b.IsZero(), b.Path(), line, col)
	}
	missing := v.Field("a").Index(5).Field("c")
	if line, col := missing.Pos(); !missing.IsZero() || missing.Path() != "a[5].c" || line != 2 || col != 7 {
		t.Fatalf("missing: zero=%v path=%q at %d:%d", missing.IsZero(), missing.Path(), line, col)
	}
	if e := missing.Errorf("bad %d", 1); e.Error() != "2:7: a[5].c: bad 1" {
		t.Fatalf("Errorf = %q", e)
	}
}

func TestValueMarshal(t *testing.T) {
	const doc = `{"z":1,"a":[true,false,null],"s":"<\"é\">\n","n":-1.5e3,"o":{}}`
	v, err := ParseValue([]byte(doc), "")
	if err != nil {
		t.Fatal(err)
	}
	out, _ := v.MarshalJSON()
	if string(out) != `{"z":1,"a":[true,false,null],"s":"<\"é\">\n","n":-1.5e3,"o":{}}` {
		t.Fatalf("marshalled %s", out)
	}
	if out, _ := (Value{}).MarshalJSON(); string(out) != "null" {
		t.Fatalf("zero Value marshalled %s", out)
	}
}

func TestParseValueError(t *testing.T) {
	_, err := ParseValue([]byte(`{"a": }`), "params")
	if err == nil || err.Error() != `1:7: params: invalid JSON: missing value after object key` {
		t.Fatalf("error = %v", err)
	}
}

func TestAsErrors(t *testing.T) {
	if AsErrors(nil) != nil {
		t.Fatal("nil")
	}
	e := &Error{Msg: "x"}
	if got := AsErrors(e); len(got) != 1 || got[0] != e {
		t.Fatal("*Error")
	}
	if got := AsErrors(Errors{e, e}); len(got) != 2 {
		t.Fatal("Errors")
	}
	if got := AsErrors(errString("plain")); got[0].Msg != "plain" {
		t.Fatal("other")
	}
}

type errString string

func (e errString) Error() string { return string(e) }

type renamedParams struct {
	MaxBytes int64 `json:"max_bytes,omitempty" renamed:"max_size,size"`
}

// TestRenamed checks that a field's old names still decode, each with a
// warning at its position, and that a field given under two names is an
// error.
func TestRenamed(t *testing.T) {
	v, err := ParseValue([]byte("{\n  \"max_size\": 10\n}"), "params")
	if err != nil {
		t.Fatal(err)
	}
	var p renamedParams
	if err := v.Decode(&p); err != nil || p.MaxBytes != 10 {
		t.Fatalf("%+v %v", p, err)
	}
	// Decoding again warns once.
	if err := v.Decode(&p); err != nil {
		t.Fatal(err)
	}
	ws := v.n.src.warnings
	want := `2:3: params.max_size: deprecated: renamed "max_bytes", which takes the same value; the old name goes in config version 2`
	if len(ws) != 1 || ws[0].Error() != want {
		t.Fatalf("warnings %v, want %s", ws, want)
	}

	for _, doc := range []string{`{"max_size": 1, "max_bytes": 2}`, `{"max_bytes": 2, "max_size": 1}`, `{"max_size": 1, "size": 2}`} {
		v, _ := ParseValue([]byte(doc), "params")
		err := v.Decode(&p)
		if err == nil || !strings.Contains(err.Error(), `give only "max_bytes"`) {
			t.Errorf("%s: %v", doc, err)
		}
	}
}

// TestSchemaVersion checks that a config written for another version of
// the schema is refused, and one for this version, or for none, loads.
func TestSchemaVersion(t *testing.T) {
	const rest = `"listeners": [{"name": "web", "address": ":0"}], "routes": [{"handler": {"module": "respond"}}]`
	for _, doc := range []string{`{` + rest + `}`, `{"schema_version": 1, ` + rest + `}`} {
		c, err := Load([]byte(doc))
		if err != nil {
			t.Fatalf("%s: %v", doc, err)
		}
		if ws := c.Warnings(); len(ws) != 0 {
			t.Fatalf("%s: warnings %v", doc, ws)
		}
	}
	if _, err := Load([]byte(`{"schema_version": 2, ` + rest + `}`)); err == nil || !strings.Contains(err.Error(), "schema_version: 2 is greater than the maximum 1") {
		t.Fatalf("version 2: %v", err)
	}
}
