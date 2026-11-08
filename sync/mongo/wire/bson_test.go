package mongowire

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	fitsync "github.com/lesismal/fit/sync"
)

func mustMarshal(t testing.TB, d D) Raw {
	t.Helper()
	r, err := Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// allTypes is a document of every BSON type.
func allTypes(t testing.TB) Raw {
	id, _ := ParseObjectID("65a1b2c3d4e5f60718293a4b")
	dec, _ := ParseDecimal128("-12.3400")
	nan, _ := ParseDecimal128("NaN")
	inner := mustMarshal(t, D{{"x", int32(1)}})
	return mustMarshal(t, D{
		{"double", 1.5},
		{"string", "héllo\x00there"},
		{"doc", D{{"a", int64(1)}, {"b", D{{"c", "deep"}}}}},
		{"array", A{int32(1), "two", D{{"three", 3.0}}, A{}}},
		{"bytes", []byte{0, 1, 2}},
		{"uuid", Binary{Subtype: 4, Data: bytes.Repeat([]byte{7}, 16)}},
		{"oldbin", Binary{Subtype: 2, Data: []byte("old")}},
		{"undefined", Undefined{}},
		{"oid", id},
		{"true", true},
		{"false", false},
		{"date", time.UnixMilli(1_700_000_000_123).UTC()},
		{"before1970", time.UnixMilli(-1_234_567).UTC()},
		{"null", nil},
		{"regex", Regex{Pattern: "^a.*b$", Options: "im"}},
		{"dbptr", RawValue{Type: TypeDBPointer, Data: append(appendString(nil, "db.coll"), id[:]...)}},
		{"code", RawValue{Type: TypeJavaScript, Data: appendString(nil, "function(){}")}},
		{"symbol", RawValue{Type: TypeSymbol, Data: appendString(nil, "sym")}},
		{"codews", RawValue{Type: TypeCodeWithScope, Data: func() []byte {
			b := binary.LittleEndian.AppendUint32(nil, 0)
			b = appendString(b, "x+1")
			b = append(b, inner...)
			binary.LittleEndian.PutUint32(b, uint32(len(b)))
			return b
		}()}},
		{"int32", int32(-7)},
		{"ts", Timestamp{T: 1_700_000_000, I: 3}},
		{"int64", int64(math.MinInt64)},
		{"dec", dec},
		{"decnan", nan},
		{"min", MinKey{}},
		{"max", MaxKey{}},
		{"raw", inner},
	})
}

func TestBSONRoundTrip(t *testing.T) {
	r := allTypes(t)
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	v, err := r.Value()
	if err != nil {
		t.Fatal(err)
	}
	get := func(k string) fitsync.Value {
		x, ok := v.Get(k)
		if !ok {
			t.Fatalf("no %s in %s", k, r)
		}
		return x
	}
	if x := get("double"); x.Kind() != fitsync.KindFloat || x.AsFloat() != 1.5 {
		t.Errorf("double: %v", x)
	}
	if x := get("string"); x.Text() != "héllo\x00there" {
		t.Errorf("string: %q", x.Text())
	}
	if x := get("int32"); x.Kind() != fitsync.KindInt || x.AsInt() != -7 {
		t.Errorf("int32: %v", x)
	}
	if x := get("dec"); x.Kind() != fitsync.KindDecimal || x.Text() != "-12.3400" {
		t.Errorf("dec: %v %q", x.Kind(), x.Text())
	}
	if x := get("date"); !x.AsTime().Equal(time.UnixMilli(1_700_000_000_123)) {
		t.Errorf("date: %v", x.AsTime())
	}
	if x := get("bytes"); x.Kind() != fitsync.KindBytes || x.Text() != "\x00\x01\x02" {
		t.Errorf("bytes: %v", x)
	}
	if x, _ := get("oid").Get("$oid"); x.Text() != "65a1b2c3d4e5f60718293a4b" {
		t.Errorf("oid: %v", get("oid"))
	}
	if x, _ := get("uuid").Get("$binary"); x.Fields()[1].Value.Text() != "04" {
		t.Errorf("uuid: %v", x)
	}
	if x, _ := get("decnan").Get("$numberDecimal"); x.Text() != "NaN" {
		t.Errorf("decnan: %v", get("decnan"))
	}
	if x, _ := get("codews").Get("$scope"); x.Kind() != fitsync.KindMap {
		t.Errorf("codews: %v", get("codews"))
	}

	// Written back, the value is the same document, byte for byte but for
	// the int32s, which are written as int64s.
	back, err := MarshalValue(v)
	if err != nil {
		t.Fatal(err)
	}
	v2, err := back.Value()
	if err != nil {
		t.Fatal(err)
	}
	if !v.Equal(v2) {
		t.Fatalf("round trip:\n%s\n%s", r, back)
	}
	types := map[string]byte{}
	_ = back.Each(func(k string, e RawValue) error { types[k] = e.Type; return nil })
	_ = r.Each(func(k string, e RawValue) error {
		want := e.Type
		if want == TypeInt32 {
			want = TypeInt64
		}
		if types[k] != want {
			t.Errorf("%s: written as type %#x, read as %#x", k, types[k], want)
		}
		return nil
	})
	if s := r.String(); !strings.Contains(s, `"$oid":"65a1b2c3d4e5f60718293a4b"`) {
		t.Errorf("String: %s", s)
	}
}

func TestRecordValuesToBSON(t *testing.T) {
	v := fitsync.Map(
		fitsync.Field{Name: "u", Value: fitsync.Uint(math.MaxUint64)},
		fitsync.Field{Name: "small", Value: fitsync.Uint(5)},
		fitsync.Field{Name: "d", Value: fitsync.Date(2024, 2, 29)},
		fitsync.Field{Name: "t", Value: fitsync.Time(time.Date(2024, 1, 2, 3, 4, 5, 999_999_999, time.FixedZone("x", 3600)))},
		fitsync.Field{Name: "j", Value: fitsync.JSON([]byte(`{"a":[1,2.5,"x",null,true,{"b":12345678901234567890123}],"z":-3}`))},
		fitsync.Field{Name: "js", Value: fitsync.JSON([]byte(`"just a string"`))},
		fitsync.Field{Name: "dec", Value: fitsync.Decimal("123456789012345678901234567890.1234")},
	)
	r, err := MarshalValue(v)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := r.Value()
	if x, _ := got.Get("u"); x.Kind() != fitsync.KindDecimal || x.Text() != "18446744073709551615" {
		t.Errorf("u: %v %q", x.Kind(), x.Text())
	}
	if x, _ := got.Get("small"); x.Kind() != fitsync.KindInt || x.AsInt() != 5 {
		t.Errorf("small: %v", x)
	}
	if x, _ := got.Get("d"); !x.AsTime().Equal(time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("d: %v", x.AsTime())
	}
	if x, _ := got.Get("t"); !x.AsTime().Equal(time.Date(2024, 1, 2, 2, 4, 5, 999_000_000, time.UTC)) {
		t.Errorf("t: %v", x.AsTime())
	}
	if s := r.String(); !strings.Contains(s, `"j":{"a":[1,2.5,"x",null,true,{"b":12345678901234567890123}],"z":-3}`) || !strings.Contains(s, `"js":"just a string"`) {
		t.Errorf("json: %s", s)
	}
	if x, _ := got.Get("dec"); x.Text() != "123456789012345678901234567890.1234" {
		t.Errorf("dec: %q", x.Text())
	}
	for _, bad := range []fitsync.Value{
		fitsync.Map(fitsync.Field{Name: "x", Value: fitsync.Decimal("1234567890123456789012345678901234567")}),
		fitsync.Map(fitsync.Field{Name: "x\x00", Value: fitsync.Int(1)}),
		fitsync.Map(fitsync.Field{Name: "x", Value: fitsync.JSON([]byte(`{"a":`))}),
		fitsync.Int(1),
	} {
		if _, err := MarshalValue(bad); err == nil {
			t.Errorf("%v written", bad)
		}
	}
}

func TestDecimal128(t *testing.T) {
	for _, c := range []struct {
		in, out string
		h, l    uint64
	}{
		{"1", "1", 0x3040000000000000, 1},
		{"0", "0", 0x3040000000000000, 0},
		{"-0", "-0", 0xB040000000000000, 0},
		{"-1", "-1", 0xB040000000000000, 1},
		{"0.1", "0.1", 0x303E000000000000, 1},
		{"-12.3400", "-12.3400", 0xB038000000000000, 123400},
		{"1E+3", "1E+3", 0x3046000000000000, 1},
		{"1.0E+3", "1.0E+3", 0x3044000000000000, 10},
		{"1000", "1000", 0x3040000000000000, 1000},
		{"0.000001", "0.000001", 0x3034000000000000, 1},
		{"0.0000001", "1E-7", 0x3032000000000000, 1},
		{"9.999999999999999999999999999999999E+6144", "9.999999999999999999999999999999999E+6144", 0x5FFFED09BEAD87C0, 0x378D8E63FFFFFFFF},
		{"1E-6176", "1E-6176", 0, 1},
		{"1E+6144", "1.000000000000000000000000000000000E+6144", 0x5FFE314DC6448D93, 0x38C15B0A00000000},
		{"0E+7000", "0E+6111", 0x5FFE000000000000, 0},
		{"10E-6177", "1E-6176", 0, 1},
		{"NaN", "NaN", 0x7C00000000000000, 0},
		{"Infinity", "Infinity", 0x7800000000000000, 0},
		{"-Inf", "-Infinity", 0xF800000000000000, 0},
	} {
		d, err := ParseDecimal128(c.in)
		if err != nil {
			t.Errorf("%s: %v", c.in, err)
			continue
		}
		if d.H != c.h || d.L != c.l {
			t.Errorf("%s: %#x %#x, want %#x %#x", c.in, d.H, d.L, c.h, c.l)
		}
		if s := d.String(); s != c.out {
			t.Errorf("%s: written %s, want %s", c.in, s, c.out)
		}
	}
	for _, bad := range []string{"", "-", "1..2", "abc", "1E", "12345678901234567890123456789012345", "1E-6177", "1E+6145", "1e99999999"} {
		if _, err := ParseDecimal128(bad); err == nil {
			t.Errorf("%q read", bad)
		}
	}
	// A non-canonical coefficient reads as zero.
	if s := (Decimal128{H: 0x6C10000000000000}).String(); s != "0" {
		t.Errorf("non-canonical: %s", s)
	}
}

func TestMalformed(t *testing.T) {
	good := mustMarshal(t, D{{"a", "x"}, {"b", D{{"c", int32(1)}}}})
	for i := range len(good) {
		if err := Raw(good[:i]).Validate(); err == nil {
			t.Errorf("cut at %d: valid", i)
		}
	}
	bad := bytes.Clone(good)
	bad[4] = 0x42 // an unknown type
	if err := Raw(bad).Validate(); !errors.Is(err, ErrMalformed) {
		t.Errorf("unknown type: %v", err)
	}
	// Deep nesting is refused, not recursed into without end.
	deep := D{{"x", int32(1)}}
	for range maxDepth + 5 {
		deep = D{{"x", deep}}
	}
	r, err := Marshal(deep)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Validate(); err == nil {
		t.Error("deep: valid")
	}
	if _, err := r.Value(); err == nil {
		t.Error("deep: read")
	}
	if _, err := Marshal(D{{"x", struct{}{}}}); err == nil {
		t.Error("a struct written")
	}
}

func FuzzBSON(f *testing.F) {
	f.Add([]byte(allTypes(f)))
	f.Add([]byte(mustMarshal(f, D{{"a", A{D{{"b", nil}}}}})))
	f.Add([]byte{5, 0, 0, 0, 0})
	f.Fuzz(func(t *testing.T, b []byte) {
		r := Raw(b)
		v, err := r.Value()
		if verr := r.Validate(); (verr == nil) != (err == nil) && len(b) >= 5 && int(binary.LittleEndian.Uint32(b)) == len(b) {
			t.Fatalf("Validate %v, Value %v", verr, err)
		}
		_ = r.Each(func(string, RawValue) error { return nil })
		_ = r.Keys()
		if err != nil {
			return
		}
		// Written and read back, a value is stable: the second writing is
		// the first's. (The first may differ from b: an int32 is written
		// as an int64, and a document shaped as a tagged value as its type.)
		w1, err := MarshalValue(v)
		if err != nil {
			if strings.Contains(err.Error(), "tagged") {
				return
			}
			t.Fatalf("writing %s: %v", r, err)
		}
		v1, err := w1.Value()
		if err != nil {
			t.Fatalf("reading what was written: %v", err)
		}
		w2, err := MarshalValue(v1)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(w1, w2) {
			t.Fatalf("unstable:\n%x\n%x", w1, w2)
		}
	})
}
