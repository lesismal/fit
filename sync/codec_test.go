package fitsync

import (
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
)

func decodeAll(t *testing.T, c Codec, line string) []*Record {
	t.Helper()
	var recs []*Record
	if err := c.Decode([]byte(line), func(r *Record) { recs = append(recs, r) }); err != nil {
		t.Fatalf("%q: %v", line, err)
	}
	return recs
}

func encode(t *testing.T, c Codec, recs ...*Record) string {
	t.Helper()
	b, err := c.Encode(nil, recs)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestLineCodec(t *testing.T) {
	c := &LineCodec{}
	recs := decodeAll(t, c, "hello world")
	if len(recs) != 1 || recs[0].Op != OpMessage || TextOf(must(recs[0].After.Get("line"))) != "hello world" {
		t.Fatalf("%+v", recs)
	}
	other := &Record{Op: OpInsert, After: Map(Field{"id", Int(1)})}
	if got := encode(t, c, recs[0], other); got != "hello world\n"+`{"op":"insert","after":{"map":{"id":{"int":1}}}}`+"\n" {
		t.Fatalf("%q", got)
	}
	if got := encode(t, &LineCodec{Field: "id"}, other); got != "1\n" {
		t.Fatalf("%q", got)
	}
}

func must(v Value, _ bool) Value { return v }

func TestJSONCodec(t *testing.T) {
	c := &JSONCodec{}
	recs := decodeAll(t, c, `{"a":1,"b":-2,"c":18446744073709551615,"d":1.5,"e":"x","f":true,"g":null,"h":[1,{"i":2}],"j":1e400}`)
	after := recs[0].After
	want := []struct {
		name string
		kind Kind
	}{{"a", KindInt}, {"b", KindInt}, {"c", KindUint}, {"d", KindFloat}, {"e", KindString}, {"f", KindBool},
		{"g", KindNull}, {"h", KindArray}, {"j", KindDecimal}}
	for i, w := range want {
		if f := after.Fields()[i]; f.Name != w.name || f.Value.Kind() != w.kind {
			t.Errorf("field %d: %s %s, want %s %s", i, f.Name, f.Value.Kind(), w.name, w.kind)
		}
	}
	for _, bad := range []string{`[1]`, `{"a":1} {}`, `{"a":`, `nope`} {
		if err := c.Decode([]byte(bad), func(*Record) {}); err == nil {
			t.Errorf("%q decoded", bad)
		}
	}
	if got := encode(t, c, &Record{Op: OpDelete, Before: Map(Field{"id", Int(7)}, Field{"at", Date(2026, 11, 8)})}); got != `{"id":7,"at":"2026-11-08"}`+"\n" {
		t.Fatalf("%q", got)
	}
	// The record form keeps everything but the position.
	rc := &JSONCodec{Form: "record"}
	in := GenerateRecord("", 5, 0, 1)
	line := encode(t, rc, in)
	out := decodeAll(t, rc, strings.TrimSuffix(line, "\n"))[0]
	if out.Position != nil || out.Op != in.Op || !out.After.Equal(in.After) || !out.Key.Equal(in.Key) {
		t.Fatalf("%+v", out)
	}
	if err := rc.Decode([]byte("{"), func(*Record) {}); err == nil {
		t.Fatal("a bad record decoded")
	}
}

func TestCSVCodec(t *testing.T) {
	c := &CSVCodec{Columns: []string{"a", "b"}}
	recs := decodeAll(t, c, `1,"x,y",3`)
	fs := recs[0].After.Fields()
	if len(fs) != 3 || fs[0].Name != "a" || TextOf(fs[1].Value) != "x,y" || fs[2].Name != "c3" {
		t.Fatalf("%+v", fs)
	}
	r := &Record{Op: OpInsert, After: Map(Field{"b", String("q\"r")}, Field{"a", Float(1.5)}, Field{"z", Time(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))})}
	if got := encode(t, c, r); got != "1.5,\"q\"\"r\"\n" {
		t.Fatalf("%q", got)
	}
	if got := encode(t, &CSVCodec{}, r); got != "\"q\"\"r\",1.5,2026-01-02T03:04:05Z\n" {
		t.Fatalf("%q", got)
	}
	// A codec is a module, its comma checked.
	for comma, ok := range map[string]bool{";": true, "\t": true, `"`: false, "ab": false} {
		ref := config.ModuleRef{Module: "csv", Params: mustParams(t, `{"comma":`+quote(comma)+`}`)}
		v, err := module.Load(Codecs, ref, nil)
		if (err == nil) != ok {
			t.Errorf("comma %q: %v", comma, err)
		}
		if ok {
			recs := decodeAll(t, v.(Codec), "a"+comma+"b")
			if len(recs[0].After.Fields()) != 2 {
				t.Errorf("comma %q: %+v", comma, recs[0].After)
			}
		}
	}
	if err := c.Decode([]byte(`"open`), func(*Record) {}); err == nil {
		t.Fatal("a bad row decoded")
	}
}

func quote(s string) string {
	b := appendString(nil, s)
	return string(b)
}

func mustParams(t *testing.T, params string) config.Value {
	t.Helper()
	c, err := config.Load([]byte(`{"sync": {"state_dir": "x", "pipelines": [{"name": "p", "source": {"module": "generate", "params": ` + params + `}, "sinks": [{"name": "s", "sink": {"module": "memory"}}]}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	return c.Sync.Pipelines[0].Source.Params
}
