package pgwire

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
	"time"
)

func TestDecodeBinaryTypes(t *testing.T) {
	uuid := []byte{0xa0, 0xee, 0xbc, 0x99, 0x9c, 0x0b, 0x4e, 0xf8, 0xbb, 0x6d, 0x6b, 0xb9, 0xbd, 0x38, 0x0a, 0x11}
	for _, c := range []struct {
		oid  uint32
		b    []byte
		want any
	}{
		{OIDBool, []byte{1}, true},
		{OIDBytea, []byte{1, 2}, []byte{1, 2}},
		{OIDName, []byte("n"), "n"},
		{OIDInt2, u16(0xFFFF), int64(-1)},
		{OIDInt4, u32(0xFFFFFFFE), int64(-2)},
		{OIDOid, u32(0xFFFFFFFE), int64(0xFFFFFFFE)},
		{OIDInt8, u64(5), int64(5)},
		{OIDFloat4, u32(math.Float32bits(0.5)), 0.5},
		{OIDFloat8, u64(math.Float64bits(-0.25)), -0.25},
		{OIDJSON, []byte(`{"a":1}`), JSON(`{"a":1}`)},
		{OIDJSONB, append([]byte{1}, `{"a": 1}`...), JSON(`{"a": 1}`)},
		{OIDUUID, uuid, "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"},
		{OIDDate, u32(math.MaxInt32), Infinity},
		{OIDDate, u32(0x80000000), NegInfinity},
		{OIDTimestamp, u64(1 << 63), NegInfinity},
		{OIDNumeric, numeric(0, 0xD000, 0), Numeric("Infinity")},
	} {
		got, err := decodeBinary(c.oid, c.b)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%d %x: %#v %v, want %#v", c.oid, c.b, got, err, c.want)
		}
	}
	for _, c := range []struct {
		oid uint32
		b   []byte
	}{
		{OIDBool, nil}, {OIDInt2, nil}, {OIDInt4, nil}, {OIDOid, nil}, {OIDInt8, nil}, {OIDFloat4, nil},
		{OIDFloat8, nil}, {OIDJSONB, []byte{2}}, {OIDUUID, nil}, {OIDDate, nil}, {OIDTimestamptz, nil},
		{OIDNumeric, numeric(0, 0x1234, 0)}, {OIDInterval, nil},
		// Arrays: too many dimensions, a negative one, more elements than bytes,
		// a bad element, an element past the end.
		{1007, append(append(u32(7), u32(0)...), u32(OIDInt4)...)},
		{1007, append(append(append(u32(1), u32(0)...), u32(OIDInt4)...), append(u32(0xFFFFFFFF), u32(1)...)...)},
		{1007, append(append(append(u32(1), u32(0)...), u32(OIDInt4)...), append(u32(1000), u32(1)...)...)},
		{1007, append(append(append(append(u32(2), u32(0)...), u32(OIDInt4)...), append(u32(30), u32(1)...)...), append(u32(30), u32(1)...)...)},
		{1007, append(append(append(append(u32(1), u32(0)...), u32(OIDInt4)...), append(u32(1), u32(1)...)...), append(u32(2), 0, 0)...)},
		{1007, append(append(append(append(u32(1), u32(0)...), u32(OIDInt4)...), append(u32(1), u32(1)...)...), append(u32(9), 0, 0)...)},
		{1007, []byte{0, 0}},
	} {
		if got, err := decodeBinary(c.oid, c.b); err == nil {
			t.Errorf("%d %x decoded: %#v", c.oid, c.b, got)
		}
	}
	if !binaryOID(1007) || binaryOID(OIDInterval) || !binaryOID(OIDNumeric) {
		t.Error("binaryOID")
	}
}

func TestAppendParamTypes(t *testing.T) {
	n := 7
	for _, c := range []struct {
		oid  uint32
		v    any
		want string
	}{
		{OIDBytea, []byte{1, 2}, "\x01\x02"},
		{OIDText, []byte("t"), "t"},
		{0, JSON(`{}`), "{}"},
		{0, json.RawMessage(`[1]`), "[1]"},
		{0, false, "f"},
		{0, true, "t"},
		{0, int8(-1), "-1"},
		{0, int16(-2), "-2"},
		{0, int32(-3), "-3"},
		{0, int64(-4), "-4"},
		{0, uint(1), "1"},
		{0, uint8(2), "2"},
		{0, uint16(3), "3"},
		{0, uint32(4), "4"},
		{0, float32(0.5), "0.5"},
		{0, math.NaN(), "NaN"},
		{0, math.Inf(1), "Infinity"},
		{OIDTime, time.Date(1, 1, 1, 13, 14, 15, 0, time.UTC), "13:14:15"},
		{0, &n, "7"},
		{0, [2]int{1, 2}, `{"1","2"}`},
		{0, []any{nil, (*int)(nil), []byte(nil), []string{"a"}}, `{NULL,NULL,NULL,{"a"}}`},
		{0, []any{true, 1.5}, `{"t","1.5"}`},
	} {
		got, notNull, err := appendParam(nil, c.oid, c.v)
		if err != nil || !notNull || string(got) != c.want {
			t.Errorf("%#v: %q %v %v, want %q", c.v, got, notNull, err, c.want)
		}
	}
	for _, v := range []any{nil, []byte(nil), []int(nil)} {
		if _, notNull, err := appendParam(nil, 0, v); notNull || err != nil {
			t.Errorf("%#v is not NULL", v)
		}
	}
	for _, v := range []any{[]any{struct{}{}}, [][3]byte{{1}}, [3]byte{1}, []any{[]any{struct{}{}}}} {
		if _, _, err := appendParam(nil, 0, v); err == nil {
			t.Errorf("%#v accepted", v)
		}
	}
}
