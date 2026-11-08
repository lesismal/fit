package fitsync

import (
	"math"
	"strings"
	"testing"
	"time"
)

func everyKind() Value {
	return Map(
		Field{"null", Null()},
		Field{"true", Bool(true)},
		Field{"false", Bool(false)},
		Field{"int", Int(math.MinInt64)},
		Field{"uint", Uint(math.MaxUint64)},
		Field{"float", Float(-1.25e-300)},
		Field{"nan", Float(math.NaN())},
		Field{"inf", Float(math.Inf(1))},
		Field{"-inf", Float(math.Inf(-1))},
		Field{"decimal", Decimal("-12345678901234567890.0100")},
		Field{"string", String("a \"quoted\"\nline, 中文")},
		Field{"bytes", Bytes([]byte{0, 1, 0xfe, 0xff})},
		Field{"time", Time(time.Date(2026, 11, 8, 10, 0, 0, 500, time.FixedZone("", 8*3600)))},
		Field{"utc", Time(time.Date(2026, 11, 8, 2, 0, 0, 0, time.UTC))},
		Field{"date", Date(2026, 2, 28)},
		Field{"json", JSON([]byte(`{"a":[1,2,{"b":null}]}`))},
		Field{"array", Array(Int(1), String("x"), Array(), Map())},
		Field{"map", Map(Field{"z", Int(1)}, Field{"a", Int(2)})},
	)
}

func TestValueRoundTrip(t *testing.T) {
	v := everyKind()
	b, err := v.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var w Value
	if err := w.UnmarshalJSON(b); err != nil {
		t.Fatalf("%v in %s", err, b)
	}
	if !v.Equal(w) {
		t.Fatalf("round trip changed it:\n%s", b)
	}
	b2, _ := w.MarshalJSON()
	if string(b) != string(b2) {
		t.Fatalf("not canonical:\n%s\n%s", b, b2)
	}
	if got, _ := w.Get("map"); got.Fields()[0].Name != "z" {
		t.Fatal("a map's order was lost")
	}
	for _, f := range w.Fields() {
		if f.Value.Kind().String() == "unknown" {
			t.Fatal(f.Name)
		}
	}
}

func TestRecordRoundTrip(t *testing.T) {
	r := &Record{Source: "generate", Namespace: "db.t", Op: OpUpdate, Key: Map(Field{"id", Int(7)}),
		Before: Map(Field{"id", Int(7)}, Field{"v", String("a")}), After: everyKind(),
		CommitTime: time.Date(2026, 11, 8, 1, 2, 3, 4, time.UTC), Tx: Tx{ID: "gtid:5", First: true},
		Position: Position(`{ "file": "binlog.000001", "offset": 42 }`)}
	b, err := r.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var got Record
	if err := got.UnmarshalJSON(b); err != nil {
		t.Fatalf("%v in %s", err, b)
	}
	if got.Source != r.Source || got.Namespace != r.Namespace || got.Op != r.Op || !got.Key.Equal(r.Key) ||
		!got.Before.Equal(r.Before) || !got.After.Equal(r.After) || !got.CommitTime.Equal(r.CommitTime) || got.Tx != r.Tx ||
		string(got.Position) != `{"file":"binlog.000001","offset":42}` {
		t.Fatalf("round trip changed it: %s\n%+v", b, got)
	}
	var empty Record
	if err := empty.UnmarshalJSON([]byte(`{"op":"message","after":"a line"}`)); err != nil || empty.Op != OpMessage || empty.After.Text() != "a line" || !empty.Key.IsNull() {
		t.Fatalf("%v %+v", err, empty)
	}
}

func TestJSONRefused(t *testing.T) {
	for _, s := range []string{
		`5`, `{"int":"5"}`, `{"int":5,"uint":5}`, `{"thing":1}`, `{"map":{"a":null,"a":null}}`,
		`{"bytes":"!!"}`, `{"time":"yesterday"}`, `{"date":"2026-13-01"}`, `{"float":"Inf"}`, `[1`, `null null`,
	} {
		var v Value
		if err := v.UnmarshalJSON([]byte(s)); err == nil {
			t.Errorf("%s: no error", s)
		}
	}
	for _, s := range []string{`{"op":"upsert"}`, `{"who":1}`, `{"commit_time":"x"}`, `[]`} {
		var r Record
		if err := r.UnmarshalJSON([]byte(s)); err == nil {
			t.Errorf("%s: no error", s)
		}
	}
	if _, err := JSON([]byte("{bad")).MarshalJSON(); err == nil {
		t.Error("a bad JSON value was written")
	}
	if _, err := (&Record{Position: Position("{bad")}).MarshalJSON(); err == nil || !strings.Contains(err.Error(), "position") {
		t.Errorf("%v", err)
	}
}

func TestValueAccessors(t *testing.T) {
	if !Bool(true).AsBool() || Int(-3).AsInt() != -3 || Uint(3).AsUint() != 3 || Float(1.5).AsFloat() != 1.5 ||
		String("s").Text() != "s" || Int(1).Text() != "" || String("s").AsInt() != 0 || Int(1).AsUint() != 0 ||
		Int(1).AsFloat() != 0 || !Int(1).AsTime().IsZero() || Date(2026, 1, 2).AsTime().Day() != 2 {
		t.Fatal("an accessor is wrong")
	}
	if _, ok := Int(1).Get("x"); ok {
		t.Fatal("Get on an int")
	}
	if Time(time.Unix(0, 0).In(time.FixedZone("", 3600))).Equal(Time(time.Unix(0, 0).UTC())) {
		t.Fatal("times of different offsets are equal")
	}
	if Kind(99).String() != "unknown" || Op(9).String() != "op(9)" {
		t.Fatal("names")
	}
	r := &Record{After: everyKind()}
	if r.Size() <= recordOverhead || r.Clone().After.Kind() != KindMap {
		t.Fatal("size or clone")
	}
}
