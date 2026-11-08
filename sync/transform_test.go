package fitsync

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
)

// loadTransform loads the transform name with params, or returns its error.
func loadTransform(name, params string) (Transform, error) {
	src := fmt.Sprintf(`{"sync": {"state_dir": "x", "pipelines": [{"name": "p", "source": {"module": "generate"},
		"transforms": [{"module": %q, "params": %s}], "sinks": [{"name": "m", "sink": {"module": "memory"}}]}]}}`, name, params)
	c, err := config.Load([]byte(src))
	if err != nil {
		return nil, err
	}
	v, err := module.Load(Transforms, c.Sync.Pipelines[0].Transforms[0], nil)
	if err != nil {
		return nil, err
	}
	return v.(Transform), nil
}

func mustTransform(t *testing.T, name, params string) Transform {
	t.Helper()
	tr, err := loadTransform(name, params)
	if err != nil {
		t.Fatalf("%s %s: %v", name, params, err)
	}
	return tr
}

func apply(tr Transform, r *Record) ([]*Record, error) {
	var out []*Record
	err := tr.Apply(r, func(o *Record) { out = append(out, o) })
	return out, err
}

func js(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "error: " + err.Error()
	}
	return string(b)
}

// testRow is a row with an id and a field of every kind.
func testRow() Value {
	return Map(append([]Field{{"id", Int(7)}}, everyKind().Fields()...)...)
}

// opRecords are a record of each operation, of namespace shop.orders.
func opRecords() map[Op]*Record {
	key := Map(Field{"id", Int(7)})
	at := time.Date(2026, 11, 8, 1, 2, 3, 0, time.UTC)
	mk := func(op Op, before, after Value) *Record {
		k := key
		if op == OpDDL || op == OpMessage {
			k = Null()
		}
		return &Record{Source: "mysql", Namespace: "shop.orders", Op: op, Key: k, Before: before, After: after,
			CommitTime: at, Tx: Tx{ID: "tx1", First: true, Last: true}, Position: Position(`1`)}
	}
	return map[Op]*Record{
		OpInsert:  mk(OpInsert, Null(), testRow()),
		OpUpdate:  mk(OpUpdate, Map(Field{"id", Int(7)}, Field{"string", String("old")}), testRow()),
		OpDelete:  mk(OpDelete, testRow(), Null()),
		OpDDL:     mk(OpDDL, Null(), String("ALTER TABLE orders ADD x INT")),
		OpMessage: mk(OpMessage, Null(), Map(Field{"line", String("hello")})),
	}
}

var allOps = []Op{OpInsert, OpUpdate, OpDelete, OpDDL, OpMessage}

func TestTextOf(t *testing.T) {
	for _, f := range testRow().Fields() {
		want := map[string]string{"id": "7", "null": "", "true": "true", "false": "false", "int": "-9223372036854775808",
			"uint": "18446744073709551615", "float": "-1.25e-300", "nan": "NaN", "inf": "+Inf", "-inf": "-Inf",
			"decimal": "-12345678901234567890.0100", "string": "a \"quoted\"\nline, 中文", "bytes": "\x00\x01\xfe\xff",
			"time": "2026-11-08T10:00:00.0000005+08:00", "utc": "2026-11-08T02:00:00Z", "date": "2026-02-28",
			"json": `{"a":[1,2,{"b":null}]}`, "array": `[{"int":1},"x",[],{"map":{}}]`, "map": `{"map":{"z":{"int":1},"a":{"int":2}}}`}[f.Name]
		if got := textOf(f.Value); got != want {
			t.Errorf("%s: %q, want %q", f.Name, got, want)
		}
	}
}

func TestFilter(t *testing.T) {
	recs := opRecords()
	// keeps lists, per filter, the ops kept, in the order of allOps.
	for _, tc := range []struct {
		params string
		keeps  string
	}{
		{`{"ops": ["insert", "delete"]}`, "insert delete"},
		{`{"ops": ["ddl"], "action": "drop"}`, "insert update delete message"},
		{`{"namespaces": ["shop.*"]}`, "insert update delete ddl message"},
		{`{"namespaces": ["other.*", "shop.ord?rs"]}`, "insert update delete ddl message"},
		{`{"exclude_namespaces": ["*.orders"]}`, ""},
		{`{"namespaces": ["shop.*"], "exclude_namespaces": ["shop.items"]}`, "insert update delete ddl message"},
		{`{"where": [{"field": "string", "equals": "old"}]}`, ""},
		{`{"where": [{"field": "string", "in": ["x", "a \"quoted\"\nline, 中文"]}]}`, "insert update delete"},
		{`{"where": [{"field": "line", "present": true}]}`, "message"},
		{`{"where": [{"field": "line", "present": false}]}`, "insert update delete ddl"},
		{`{"where": [{"field": "null", "present": true}]}`, ""},
		{`{"where": [{"field": "id", "equals": "7"}, {"field": "true", "equals": "true"}], "ops": ["update", "delete"]}`, "update delete"},
		{`{"where": [{"field": "id", "equals": "7"}], "action": "drop"}`, "ddl message"},
	} {
		f := mustTransform(t, "filter", tc.params)
		var kept []string
		for _, op := range allOps {
			out, err := apply(f, recs[op])
			if err != nil {
				t.Fatal(err)
			}
			if len(out) == 1 {
				if out[0] != recs[op] {
					t.Errorf("%s: a filter changed the record", tc.params)
				}
				kept = append(kept, op.String())
			}
		}
		if got := strings.Join(kept, " "); got != tc.keeps {
			t.Errorf("%s: kept %q, want %q", tc.params, got, tc.keeps)
		}
	}
	// Every kind but bytes, whose text JSON cannot hold, equals its text,
	// and is present but for null.
	r := recs[OpInsert]
	for _, fl := range r.After.Fields() {
		f := mustTransform(t, "filter", fmt.Sprintf(`{"where": [{"field": %q, "present": %v}]}`, fl.Name, !fl.Value.IsNull()))
		if out, _ := apply(f, r); len(out) != 1 {
			t.Errorf("%s: present", fl.Name)
		}
		if fl.Name == "bytes" || fl.Value.IsNull() {
			continue
		}
		f = mustTransform(t, "filter", fmt.Sprintf(`{"where": [{"field": %q, "equals": %s}]}`, fl.Name, js(textOf(fl.Value))))
		if out, _ := apply(f, r); len(out) != 1 {
			t.Errorf("%s: equals %q", fl.Name, textOf(fl.Value))
		}
	}
}

func TestRoute(t *testing.T) {
	for _, tc := range []struct{ params, ns, want string }{
		{`{"map": {"shop.orders": "orders"}}`, "shop.orders", "orders"},
		{`{"map": {"shop.items": "items"}}`, "shop.orders", "shop.orders"},
		{`{"map": {"shop.items": "items"}, "unmatched": "drop"}`, "shop.orders", ""},
		{`{"rules": [{"match": "shop.*", "to": "{first}-{rest}"}]}`, "shop.orders", "shop-orders"},
		{`{"rules": [{"match": "a.*", "to": "x"}, {"match": "*.*", "to": "idx_{last}"}]}`, "shop.orders", "idx_orders"},
		{`{"rules": [{"match": "*", "to": "{namespace}"}]}`, "logs", "logs"},
		{`{"rules": [{"match": "*", "to": "{first}/{rest}/{last}"}]}`, "logs", "logs//logs"},
		{`{"map": {"shop.orders": "o"}, "rules": [{"match": "*", "to": "x"}]}`, "shop.orders", "o"},
	} {
		rt := mustTransform(t, "route", tc.params)
		for _, op := range allOps {
			r := opRecords()[op]
			r.Namespace = tc.ns
			out, err := apply(rt, r)
			if err != nil {
				t.Fatal(err)
			}
			got := ""
			if len(out) == 1 {
				got = out[0].Namespace
			}
			if got != tc.want || r.Namespace != tc.ns {
				t.Errorf("%s %s: %q, want %q", tc.params, op, got, tc.want)
			}
		}
	}
}

func TestFields(t *testing.T) {
	r := &Record{Op: OpUpdate, Key: Map(Field{"id", Int(1)}, Field{"region", String("eu")}),
		Before: Map(Field{"id", Int(1)}, Field{"a", Int(2)}),
		After:  Map(Field{"id", Int(1)}, Field{"a", Int(3)}, Field{"meta", Map(Field{"x", Bool(true)}, Field{"y", Null()})}, Field{"b", String("s")})}
	for _, tc := range []struct{ params, before, after, key string }{
		{`{"drop": ["a", "id"]}`, `{"map":{}}`, `{"map":{"meta":{"map":{"x":true,"y":null}},"b":"s"}}`, ""},
		{`{"keep": ["a"]}`, `{"map":{"a":{"int":2}}}`, `{"map":{"a":{"int":3}}}`, ""},
		{`{"rename": {"id": "order_id", "a": "id"}}`, `{"map":{"order_id":{"int":1},"id":{"int":2}}}`, `{"map":{"order_id":{"int":1},"id":{"int":3},"meta":{"map":{"x":true,"y":null}},"b":"s"}}`,
			`{"map":{"order_id":{"int":1},"region":"eu"}}`},
		{`{"unnest": ["meta"]}`, `{"map":{"id":{"int":1},"a":{"int":2}}}`, `{"map":{"id":{"int":1},"a":{"int":3},"x":true,"y":null,"b":"s"}}`, ""},
		{`{"nest": [{"into": "meta", "fields": ["a", "b"]}]}`, `{"map":{"id":{"int":1},"meta":{"map":{"a":{"int":2}}}}}`, `{"map":{"id":{"int":1},"meta":{"map":{"x":true,"y":null,"a":{"int":3},"b":"s"}}}}`, ""},
		{`{"nest": [{"into": "n", "fields": ["zz"]}]}`, `{"map":{"id":{"int":1},"a":{"int":2}}}`, "", ""},
		{`{"set": {"region": "eu", "v": {"int": 2}, "a": null}}`, `{"map":{"id":{"int":1},"a":null,"region":"eu","v":{"int":2}}}`, `{"map":{"id":{"int":1},"a":null,"meta":{"map":{"x":true,"y":null}},"b":"s","region":"eu","v":{"int":2}}}`, ""},
		{`{"unnest": ["meta"], "rename": {"x": "flag"}, "keep": ["id", "flag"], "set": {"k": true}}`, `{"map":{"id":{"int":1},"k":true}}`, `{"map":{"id":{"int":1},"flag":true,"k":true}}`, ""},
	} {
		f := mustTransform(t, "fields", tc.params)
		out, err := apply(f, r)
		if err != nil || len(out) != 1 {
			t.Fatalf("%s: %v", tc.params, err)
		}
		if tc.after == "" {
			tc.after = js(r.After)
		}
		if tc.key == "" {
			tc.key = js(r.Key)
		}
		if got := js(out[0].Before); got != tc.before {
			t.Errorf("%s: before %s\nwant %s", tc.params, got, tc.before)
		}
		if got := js(out[0].After); got != tc.after {
			t.Errorf("%s: after %s\nwant %s", tc.params, got, tc.after)
		}
		if got := js(out[0].Key); got != tc.key {
			t.Errorf("%s: key %s\nwant %s", tc.params, got, tc.key)
		}
	}
	// Every operation: a delete keeps its key whatever fields drops.
	f := mustTransform(t, "fields", `{"drop": ["id"], "keep": []}`)
	for _, op := range allOps {
		rec := opRecords()[op]
		out, err := apply(f, rec)
		if err != nil || len(out) != 1 || !out[0].Key.Equal(rec.Key) {
			t.Fatalf("%s: %v", op, err)
		}
		if _, ok := rowOf(out[0]).Get("id"); ok {
			t.Errorf("%s: id kept", op)
		}
		if op == OpDDL && !out[0].After.Equal(rec.After) {
			t.Errorf("a ddl's statement changed")
		}
	}
	for _, tc := range []struct{ params, want string }{
		{`{"rename": {"a": "b"}}`, "renamed to b, which the row has already"},
		{`{"unnest": ["b"]}`, "a string, not a map to unnest"},
		{`{"nest": [{"into": "b", "fields": ["a"]}]}`, "a string, not a map to nest into"},
		{`{"nest": [{"into": "meta", "fields": ["x2"]}], "unnest": ["meta"]}`, ""},
	} {
		_, err := apply(mustTransform(t, "fields", tc.params), r)
		if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("%s: %v", tc.params, err)
		}
	}
	bad := &Record{Op: OpInsert, After: Map(Field{"x", Int(1)}, Field{"meta", Map(Field{"x", Int(2)})})}
	if _, err := apply(mustTransform(t, "fields", `{"unnest": ["meta"]}`), bad); err == nil || !strings.Contains(err.Error(), "in the row already") {
		t.Errorf("%v", err)
	}
	if _, err := apply(mustTransform(t, "fields", `{"nest": [{"into": "meta", "fields": ["x"]}]}`), bad); err == nil || !strings.Contains(err.Error(), "in meta already") {
		t.Errorf("%v", err)
	}
}

func TestKey(t *testing.T) {
	recs := opRecords()
	for _, f := range testRow().Fields() {
		if f.Value.IsNull() {
			continue
		}
		k := mustTransform(t, "key", fmt.Sprintf(`{"fields": [%q], "format": "value"}`, f.Name))
		for _, op := range allOps {
			out, err := apply(k, recs[op])
			switch {
			case op == OpDDL:
				if err != nil || out[0] != recs[op] {
					t.Errorf("a ddl: %v", err)
				}
			case op == OpMessage:
				if err == nil || !strings.Contains(err.Error(), "missing or null") {
					t.Errorf("a message without %s: %v", f.Name, err)
				}
			case op == OpUpdate && f.Name != "id":
				// The update's row is after, which has every field.
				fallthrough
			default:
				if err != nil || !out[0].Key.Equal(f.Value) {
					t.Errorf("%s %s: %v %s", op, f.Name, err, js(out))
				}
			}
		}
	}
	for _, tc := range []struct{ params, want string }{
		{`{"fields": ["string", "id"]}`, `{"map":{"string":"a \"quoted\"\nline, 中文","id":{"int":7}}}`},
		{`{"fields": ["id", "date", "decimal"], "format": "join"}`, `"7:2026-02-28:-12345678901234567890.0100"`},
		{`{"fields": ["id", "true"], "format": "join", "separator": "/"}`, `"7/true"`},
	} {
		out, err := apply(mustTransform(t, "key", tc.params), recs[OpInsert])
		if err != nil || js(out[0].Key) != tc.want {
			t.Errorf("%s: %v %s", tc.params, err, js(out[0].Key))
		}
	}
	// A delete whose before lacks the fields keeps its key; one with no key
	// fails, as an insert would.
	k := mustTransform(t, "key", `{"fields": ["region"]}`)
	del := recs[OpDelete]
	if out, err := apply(k, del); err != nil || out[0] != del {
		t.Errorf("a delete lost its key: %v", err)
	}
	del.Key = Null()
	if _, err := apply(k, del); err == nil {
		t.Error("a delete with no key and no fields")
	}
	if _, err := apply(k, recs[OpInsert]); err == nil {
		t.Error("an insert without the field")
	}
}

func TestCast(t *testing.T) {
	at := time.Date(2026, 11, 8, 23, 30, 0, 0, time.FixedZone("", 8*3600))
	all := []Value{Null(), Bool(true), Int(-3), Uint(3), Float(2.5), Decimal("4.00"), String("12"), Bytes([]byte("ab")),
		Time(at), Date(2026, 11, 8), JSON([]byte(`{"a":1}`)), Array(Int(1)), Map(Field{"a", Int(1)})}
	// want[to] lists, for each of all, the result's canonical JSON, or
	// "error".
	want := map[string][]string{
		"string":  {`null`, `"true"`, `"-3"`, `"3"`, `"2.5"`, `"4.00"`, `"12"`, `"ab"`, `"2026-11-08T23:30:00+08:00"`, `"2026-11-08"`, `"{\"a\":1}"`, "error", "error"},
		"int":     {`null`, `{"int":1}`, `{"int":-3}`, `{"int":3}`, "error", `{"int":4}`, `{"int":12}`, "error", "error", "error", "error", "error", "error"},
		"uint":    {`null`, `{"uint":1}`, "error", `{"uint":3}`, "error", `{"uint":4}`, `{"uint":12}`, "error", "error", "error", "error", "error", "error"},
		"float":   {`null`, `{"float":1}`, `{"float":-3}`, `{"float":3}`, `{"float":2.5}`, `{"float":4}`, `{"float":12}`, "error", "error", "error", "error", "error", "error"},
		"decimal": {`null`, "error", `{"decimal":"-3"}`, `{"decimal":"3"}`, `{"decimal":"2.5"}`, `{"decimal":"4.00"}`, `{"decimal":"12"}`, "error", "error", "error", "error", "error", "error"},
		"bool":    {`null`, `true`, "error", "error", "error", "error", "error", "error", "error", "error", "error", "error", "error"},
		"bytes":   {`null`, "error", "error", "error", "error", `{"bytes":"NC4wMA=="}`, `{"bytes":"MTI="}`, `{"bytes":"YWI="}`, "error", "error", `{"bytes":"eyJhIjoxfQ=="}`, "error", "error"},
		"time":    {`null`, "error", `{"time":"1969-12-31T23:59:57Z"}`, `{"time":"1970-01-01T00:00:03Z"}`, "error", "error", "error", "error", `{"time":"2026-11-08T23:30:00+08:00"}`, `{"time":"2026-11-08T00:00:00Z"}`, "error", "error", "error"},
		"date":    {`null`, "error", "error", "error", "error", "error", "error", "error", `{"date":"2026-11-08"}`, `{"date":"2026-11-08"}`, "error", "error", "error"},
		"json":    {`null`, `{"json":true}`, `{"json":-3}`, `{"json":3}`, `{"json":2.5}`, `{"json":4.00}`, `{"json":12}`, "error", `{"json":"2026-11-08T23:30:00+08:00"}`, `{"json":"2026-11-08"}`, `{"json":{"a":1}}`, `{"json":[1]}`, `{"json":{"a":1}}`},
	}
	for to, ws := range want {
		for i, v := range all {
			got, err := convert(v, castKinds[to])
			s := js(got)
			if err != nil {
				s = "error"
			}
			if s != ws[i] {
				t.Errorf("%s to %s: %s (%v), want %s", js(v), to, s, err, ws[i])
			}
		}
	}
	for _, tc := range []struct {
		v    Value
		to   string
		want string
	}{
		{String("true"), "bool", `true`}, {String("yes"), "bool", "error"}, {Int(0), "bool", `false`}, {Int(2), "bool", "error"},
		{String("2026-11-08T01:02:03.5Z"), "time", `{"time":"2026-11-08T01:02:03.5Z"}`}, {String("2026-11-08"), "date", `{"date":"2026-11-08"}`},
		{String("x"), "time", "error"}, {String("x"), "date", "error"}, {String("x"), "json", "error"}, {String(`[1]`), "json", `{"json":[1]}`},
		{String("-1.50"), "decimal", `{"decimal":"-1.50"}`}, {String("1e3"), "decimal", "error"}, {String("x"), "float", "error"},
		{Uint(math.MaxUint64), "int", "error"}, {Int(-1), "uint", "error"}, {Float(1e30), "int", "error"}, {Float(-2), "int", `{"int":-2}`},
		{Float(math.NaN()), "decimal", "error"}, {Float(math.Inf(1)), "json", "error"}, {String("-12.0"), "int", `{"int":-12}`},
		{String("99999999999999999999"), "int", "error"}, {String("-99999999999999999999"), "int", "error"}, {String("1.5"), "int", "error"},
		{Uint(math.MaxUint64), "time", "error"}, {Bytes([]byte{0xff}), "string", "error"}, {Bytes([]byte(`{}`)), "json", `{"json":{}}`},
		{Map(Field{"b", Bytes([]byte{1})}, Field{"n", Null()}, Field{"d", Date(2026, 1, 2)}), "json", `{"json":{"b":"AQ==","n":null,"d":"2026-01-02"}}`},
	} {
		got, err := convert(tc.v, castKinds[tc.to])
		s := js(got)
		if err != nil {
			s = "error"
		}
		if s != tc.want {
			t.Errorf("%s to %s: %s (%v), want %s", js(tc.v), tc.to, s, err, tc.want)
		}
	}
	// A record's rows and key are cast; one it cannot fails the record.
	c := mustTransform(t, "cast", `{"fields": {"id": "string", "uint": "decimal"}}`)
	for _, op := range allOps {
		rec := opRecords()[op]
		out, err := apply(c, rec)
		if err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		if v, ok := rowOf(out[0]).Get("id"); ok && v.Kind() != KindString {
			t.Errorf("%s: id %s", op, v.Kind())
		}
		if v, ok := out[0].Key.Get("id"); ok && v.Kind() != KindString {
			t.Errorf("%s: the key's id %s", op, v.Kind())
		}
		if op == OpDDL && out[0] != rec {
			t.Error("a ddl copied")
		}
	}
	if _, err := apply(mustTransform(t, "cast", `{"fields": {"string": "int"}}`), opRecords()[OpInsert]); err == nil || !strings.Contains(err.Error(), "after: field string:") {
		t.Errorf("%v", err)
	}
}

func TestMask(t *testing.T) {
	t.Setenv("FIT_TEST_MASK_KEY", "secret")
	recs := opRecords()
	mac := func(s string) string {
		h := hmac.New(sha256.New, []byte("secret"))
		h.Write([]byte(s))
		return hex.EncodeToString(h.Sum(nil))
	}
	sum := func(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
	var names []string
	for _, f := range testRow().Fields() {
		names = append(names, f.Name)
	}
	for _, tc := range []struct {
		params string
		hash   func(string) string
	}{
		{`{"fields": ` + js(names) + `, "key_env": "FIT_TEST_MASK_KEY"}`, mac},
		{`{"fields": ` + js(names) + `, "with": "sha256"}`, sum},
		{`{"fields": ` + js(names) + `, "with": "null"}`, nil},
	} {
		m := mustTransform(t, "mask", tc.params)
		for _, op := range allOps {
			rec := recs[op]
			out, err := apply(m, rec)
			if err != nil {
				t.Fatal(err)
			}
			o := out[0]
			for _, row := range [][2]Value{{rec.Before, o.Before}, {rec.After, o.After}} {
				if row[0].Kind() != KindMap {
					if !row[0].Equal(row[1]) {
						t.Errorf("%s: not a map, changed", op)
					}
					continue
				}
				for i, f := range row[0].Fields() {
					got := row[1].Fields()[i].Value
					want := Null()
					if tc.hash != nil && !f.Value.IsNull() {
						want = String(tc.hash(textOf(f.Value)))
					}
					if f.Name == "line" {
						want = f.Value
					}
					if !got.Equal(want) {
						t.Errorf("%s %s: %s, want %s", op, f.Name, js(got), js(want))
					}
				}
			}
			id, _ := o.Key.Get("id")
			if op != OpDDL && op != OpMessage && (tc.hash == nil) != id.Equal(Int(7)) {
				t.Errorf("%s: the key %s", op, js(o.Key))
			}
		}
	}
	for _, tc := range []struct{ params, want string }{
		{`{"fields": ["a"]}`, "needs key_env"},
		{`{"fields": ["a"], "key_env": "FIT_TEST_UNSET_KEY"}`, "is not set"},
		{`{"fields": ["a"], "key_env": "1X"}`, "not an environment variable"},
		{`{"fields": ["a"], "with": "sha256", "key_env": "X"}`, "key_env is for hmac_sha256"},
		{`{"fields": []}`, "no fields"},
	} {
		if _, err := loadTransform("mask", tc.params); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", tc.params, err)
		}
	}
}

func TestEnvelope(t *testing.T) {
	recs := opRecords()
	small := func(op Op) *Record {
		r := recs[op].Clone()
		r.made = false
		if !r.Before.IsNull() {
			r.Before = Map(Field{"id", Int(7)}, Field{"a", Int(1)})
		}
		if op != OpDelete && op != OpDDL && op != OpMessage {
			r.After = Map(Field{"id", Int(7)}, Field{"a", Int(2)})
		}
		return r
	}
	for _, tc := range []struct {
		params string
		want   map[Op]string // the after, the op's name after a ':', "" when dropped
	}{
		{`{}`, map[Op]string{
			OpInsert: `insert:{"map":{"id":{"int":7},"a":{"int":2}}}`,
			OpUpdate: `update:{"map":{"id":{"int":7},"a":{"int":2}}}`,
			OpDelete: `delete:{"map":{"id":{"int":7},"a":{"int":1}}}`}},
		{`{"meta": {"op": "__op", "deleted": "__deleted", "namespace": "__table", "commit_time": "ts", "tx_id": "tx", "source": "a"}, "deletes": "rewrite"}`, map[Op]string{
			OpInsert: `insert:{"map":{"id":{"int":7},"a":"mysql","__op":"insert","__table":"shop.orders","ts":{"time":"2026-11-08T01:02:03Z"},"tx":"tx1","__deleted":false}}`,
			OpUpdate: `update:{"map":{"id":{"int":7},"a":"mysql","__op":"update","__table":"shop.orders","ts":{"time":"2026-11-08T01:02:03Z"},"tx":"tx1","__deleted":false}}`,
			OpDelete: `update:{"map":{"id":{"int":7},"a":"mysql","__op":"delete","__table":"shop.orders","ts":{"time":"2026-11-08T01:02:03Z"},"tx":"tx1","__deleted":true}}`}},
		{`{"shape": "pair", "deletes": "drop", "meta": {"op": "op"}}`, map[Op]string{
			OpInsert: `insert:{"map":{"before":null,"after":{"map":{"id":{"int":7},"a":{"int":2}}},"op":"insert"}}`,
			OpUpdate: `update:{"map":{"before":{"map":{"id":{"int":7},"a":{"int":1}}},"after":{"map":{"id":{"int":7},"a":{"int":2}}},"op":"update"}}`,
			OpDelete: ``}},
		{`{"shape": "debezium"}`, map[Op]string{
			OpInsert: `insert:{"map":{"before":null,"after":{"map":{"id":{"int":7},"a":{"int":2}}},"source":{"map":{"name":"mysql","namespace":"shop.orders","db":"shop","table":"orders","ts_ms":{"int":1794099723000}}},"op":"c","ts_ms":{"int":1794099723000},"transaction":{"map":{"id":"tx1"}}}}`,
			OpUpdate: `update:{"map":{"before":{"map":{"id":{"int":7},"a":{"int":1}}},"after":{"map":{"id":{"int":7},"a":{"int":2}}},"source":{"map":{"name":"mysql","namespace":"shop.orders","db":"shop","table":"orders","ts_ms":{"int":1794099723000}}},"op":"u","ts_ms":{"int":1794099723000},"transaction":{"map":{"id":"tx1"}}}}`,
			OpDelete: `delete:{"map":{"before":{"map":{"id":{"int":7},"a":{"int":1}}},"after":null,"source":{"map":{"name":"mysql","namespace":"shop.orders","db":"shop","table":"orders","ts_ms":{"int":1794099723000}}},"op":"d","ts_ms":{"int":1794099723000},"transaction":{"map":{"id":"tx1"}}}}`}},
	} {
		e := mustTransform(t, "envelope", tc.params)
		for _, op := range allOps {
			r := small(op)
			out, err := apply(e, r)
			if err != nil {
				t.Fatalf("%s %s: %v", tc.params, op, err)
			}
			if op == OpDDL || op == OpMessage {
				if len(out) != 1 || out[0] != r {
					t.Errorf("%s: %s changed", tc.params, op)
				}
				continue
			}
			got := ""
			if len(out) == 1 {
				got = out[0].Op.String() + ":" + js(out[0].After)
				if !out[0].Before.IsNull() || !out[0].Key.Equal(r.Key) {
					t.Errorf("%s %s: before or key", tc.params, op)
				}
			}
			if got != tc.want[op] {
				t.Errorf("%s %s:\n got %s\nwant %s", tc.params, op, got, tc.want[op])
			}
		}
	}
	// A flat delete without before is its key; a row that is not a map fails.
	e := mustTransform(t, "envelope", `{"meta": {"deleted": "d"}}`)
	r := small(OpDelete)
	r.Before = Null()
	if out, err := apply(e, r); err != nil || js(out[0].After) != `{"map":{"id":{"int":7},"d":true}}` {
		t.Errorf("%v %s", err, js(out))
	}
	r = small(OpInsert)
	r.After = String("x")
	if _, err := apply(e, r); err == nil {
		t.Error("a string row")
	}
}

func TestTransformParams(t *testing.T) {
	for _, tc := range []struct{ module, params, want string }{
		{"filter", `{}`, "needs namespaces"},
		{"filter", `{"namespaces": ["["]}`, `"[" is not a glob`},
		{"filter", `{"ops": ["upsert"]}`, `"upsert" is not one of`},
		{"filter", `{"where": [{"field": "a"}]}`, "exactly one of"},
		{"filter", `{"where": [{"field": "a", "equals": "1", "present": true}]}`, "exactly one of"},
		{"filter", `{"where": [{"equals": "1"}]}`, "field"},
		{"route", `{}`, "needs map or rules"},
		{"route", `{"map": {"a": ""}}`, "an empty namespace"},
		{"route", `{"rules": [{"match": "*", "to": "{table}"}]}`, "starts none of"},
		{"route", `{"rules": [{"match": "*", "to": ""}]}`, "an empty template"},
		{"route", `{"rules": [{"match": "[", "to": "x"}]}`, "not a glob"},
		{"fields", `{}`, "needs unnest"},
		{"fields", `{"drop": ["a"], "keep": ["b"]}`, "drop or keep"},
		{"fields", `{"rename": {"a": "c", "b": "c"}}`, "both renamed to c"},
		{"fields", `{"rename": {"a": ""}}`, "an empty name"},
		{"fields", `{"nest": [{"into": "a", "fields": ["a"]}]}`, "into itself"},
		{"fields", `{"set": {"a": 5}}`, "canonical JSON"},
		{"key", `{"fields": []}`, "no fields"},
		{"key", `{"fields": ["a", "a"]}`, "named twice"},
		{"key", `{"fields": ["a", "b"], "format": "value"}`, "a single field"},
		{"cast", `{"fields": {"a": "text"}}`, `"text" is not one of`},
		{"cast", `{"fields": {}}`, "no fields"},
		{"envelope", `{"meta": {"lsn": "x"}}`, `"lsn" is not one of`},
		{"envelope", `{"meta": {"op": ""}}`, "an empty name"},
		{"envelope", `{"shape": "debezium", "meta": {"op": "o"}}`, "meta is for flat and pair"},
		{"envelope", `{"deletes": "rewrite"}`, "needs meta's deleted"},
	} {
		if _, err := loadTransform(tc.module, tc.params); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s %s: %v", tc.module, tc.params, err)
		}
	}
}

// TestKeyAfterRemoval checks a key built from a field a transform before it
// removes is refused, and one built before it is not.
func TestKeyAfterRemoval(t *testing.T) {
	t.Setenv("FIT_TEST_MASK_KEY", "secret")
	for _, tc := range []struct {
		transforms string
		want       string
	}{
		{`{"module": "fields", "params": {"drop": ["id"]}}, {"module": "key", "params": {"fields": ["id"]}}`, `the key's field "id" is gone after transform 0 (fields)`},
		{`{"module": "fields", "params": {"keep": ["a"]}}, {"module": "key", "params": {"fields": ["id"]}}`, `"id" is gone`},
		{`{"module": "fields", "params": {"rename": {"id": "x"}}}, {"module": "key", "params": {"fields": ["id"]}}`, `"id" is gone`},
		{`{"module": "fields", "params": {"nest": [{"into": "m", "fields": ["id"]}]}}, {"module": "key", "params": {"fields": ["id"]}}`, `"id" is gone`},
		{`{"module": "fields", "params": {"unnest": ["id"]}}, {"module": "key", "params": {"fields": ["id"]}}`, `"id" is gone`},
		{`{"module": "mask", "params": {"fields": ["id"], "with": "null"}}, {"module": "key", "params": {"fields": ["id"]}}`, `"id" is gone after transform 0 (mask)`},
		{`{"module": "envelope", "params": {"shape": "pair"}}, {"module": "key", "params": {"fields": ["id"]}}`, `"id" is gone`},
		{`{"module": "key", "params": {"fields": ["id"]}}, {"module": "fields", "params": {"drop": ["id"]}}`, ``},
		{`{"module": "fields", "params": {"rename": {"id": "x", "x": "id"}, "drop": ["y"], "set": {"z": 1}}}, {"module": "key", "params": {"fields": ["id"]}}`, `canonical`},
		{`{"module": "fields", "params": {"rename": {"id": "x", "x": "id"}, "drop": ["y"]}}, {"module": "key", "params": {"fields": ["id"]}}`, ``},
		{`{"module": "fields", "params": {"keep": ["a"], "set": {"id": "1"}}}, {"module": "mask", "params": {"fields": ["id"], "key_env": "FIT_TEST_MASK_KEY"}}, {"module": "envelope", "params": {}}, {"module": "key", "params": {"fields": ["id"]}}`, ``},
	} {
		c, err := config.Load([]byte(`{"sync": {"state_dir": "x", "pipelines": [{"name": "p", "source": {"module": "generate"}, "transforms": [` + tc.transforms + `], "sinks": [{"name": "m", "sink": {"module": "memory"}}]}]}}`))
		if err != nil {
			t.Fatal(err)
		}
		err = Check(c.Sync)
		if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("%s: %v", tc.transforms, err)
		}
	}
}

// chain loads the transforms of every kind, in order.
func chain(t testing.TB) []Transform {
	t.Setenv("FIT_TEST_MASK_KEY", "secret")
	var ts []Transform
	for _, m := range [][2]string{
		{"filter", `{"exclude_namespaces": ["other.*"]}`},
		{"route", `{"rules": [{"match": "shop.*", "to": "{rest}"}]}`},
		{"fields", `{"rename": {"string": "text"}, "drop": ["nan"], "set": {"region": "eu"}}`},
		{"key", `{"fields": ["id", "region"], "format": "join"}`},
		{"cast", `{"fields": {"uint": "decimal"}}`},
		{"mask", `{"fields": ["text"], "key_env": "FIT_TEST_MASK_KEY"}`},
		{"envelope", `{"meta": {"op": "__op"}}`},
	} {
		tr, err := loadTransform(m[0], m[1])
		if err != nil {
			t.Fatal(err)
		}
		ts = append(ts, tr)
	}
	return ts
}

// chainRun puts records through transforms as the pipeline's dispatch
// does, with emit made once; emitted notes each record a transform hands on.
type chainRun struct {
	ts         []Transform
	outs, next []*Record
	emit       func(*Record)
	emitted    func(*Record)
}

func newChainRun(ts []Transform, emitted func(*Record)) *chainRun {
	c := &chainRun{ts: ts, emitted: emitted}
	c.emit = func(o *Record) { c.emitted(o); c.next = append(c.next, o) }
	return c
}

func (c *chainRun) run(r *Record) ([]*Record, error) {
	c.outs = append(c.outs[:0], r)
	for _, t := range c.ts {
		c.next = c.next[:0]
		for _, in := range c.outs {
			if err := t.Apply(in, c.emit); err != nil {
				return nil, err
			}
		}
		c.outs, c.next = c.next, c.outs
	}
	return c.outs, nil
}

// TestChainCopiesOnce checks a chain of every transform copies a record of
// each operation once, and leaves the source's record as it was read.
func TestChainCopiesOnce(t *testing.T) {
	ts := chain(t)
	for _, op := range allOps {
		r := opRecords()[op]
		r.Namespace = "shop.orders"
		was := js(r)
		copies := map[*Record]bool{}
		outs, err := newChainRun(ts, func(o *Record) {
			if o != r {
				copies[o] = true
			}
		}).run(r)
		if err != nil {
			if op == OpMessage && strings.Contains(err.Error(), "missing or null") {
				continue // a message has no id to make a key of
			}
			t.Fatalf("%s: %v", op, err)
		}
		if len(copies) != 1 || len(outs) != 1 || !copies[outs[0]] {
			t.Errorf("%s: %d copies", op, len(copies))
		}
		if js(r) != was {
			t.Errorf("%s: the source's record changed", op)
		}
		if op == OpInsert && js(outs[0]) == was {
			t.Error("unchanged")
		}
	}
	// Transforms that change only the record allocate its copy alone.
	ts = []Transform{
		mustTransform(t, "filter", `{"ops": ["insert"]}`),
		mustTransform(t, "route", `{"map": {"shop.orders": "orders"}}`),
		mustTransform(t, "route", `{"map": {"orders": "o"}}`),
		mustTransform(t, "filter", `{"where": [{"field": "id", "present": true}]}`),
	}
	r := opRecords()[OpInsert]
	c := newChainRun(ts, func(*Record) {})
	var outs []*Record
	allocs := testing.AllocsPerRun(100, func() {
		outs, _ = c.run(r)
	})
	if allocs != 1 || outs[0].Namespace != "o" || r.Namespace != "shop.orders" {
		t.Errorf("%v allocations, %s", allocs, outs[0].Namespace)
	}
}

func BenchmarkChain(b *testing.B) {
	ts := chain(b)
	r := opRecords()[OpUpdate]
	c := newChainRun(ts, func(*Record) {})
	b.ReportAllocs()
	for b.Loop() {
		if _, err := c.run(r); err != nil {
			b.Fatal(err)
		}
	}
}

// TestBuiltinTransformsInAPipeline runs records of every operation through
// a pipeline of built-in transforms.
func TestBuiltinTransformsInAPipeline(t *testing.T) {
	s, sn := memStore(t)
	rt := NewRuntime(Options{})
	defer rt.Close(t.Context())
	if err := rt.Apply(syncConfig(t, t.TempDir(), fmt.Sprintf(`{"name": "p", "mode": "once",
		"source": {"module": "generate", "params": {"records": 70, "namespace": "shop.orders"}},
		"transforms": [{"module": "filter", "params": {"ops": ["delete"], "action": "drop"}},
			{"module": "route", "params": {"rules": [{"match": "shop.*", "to": "{rest}"}]}},
			{"module": "fields", "params": {"keep": ["id", "name"]}},
			{"module": "key", "params": {"fields": ["id"], "format": "value"}},
			{"module": "envelope", "params": {"meta": {"op": "op"}}}],
		"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}, "batch": {"max_wait": "1ms"}}]}`, sn))); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the end", func() bool { return state(rt, "p").State == "finished" })
	recs := s.Records()
	if len(recs) != 60 {
		t.Fatalf("%d records", len(recs))
	}
	if r := recs[5]; r.Namespace != "orders" || js(r.Key) != `{"int":5}` || js(r.After) != `{"map":{"id":{"int":5},"name":"record-5","op":"update"}}` {
		t.Errorf("%s", js(r))
	}
}
