package pgwire

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"time"
)

// Type OIDs of the built-in types the client knows.
const (
	OIDBool        = 16
	OIDBytea       = 17
	OIDChar        = 18
	OIDName        = 19
	OIDInt8        = 20
	OIDInt2        = 21
	OIDInt4        = 23
	OIDText        = 25
	OIDOid         = 26
	OIDJSON        = 114
	OIDXML         = 142
	OIDFloat4      = 700
	OIDFloat8      = 701
	OIDBPChar      = 1042
	OIDVarchar     = 1043
	OIDDate        = 1082
	OIDTime        = 1083
	OIDTimestamp   = 1114
	OIDTimestamptz = 1184
	OIDInterval    = 1186
	OIDTimetz      = 1266
	OIDNumeric     = 1700
	OIDUUID        = 2950
	OIDJSONB       = 3802
)

// arrayElem maps the array types whose elements are read in binary to their
// element type.
var arrayElem = map[uint32]uint32{
	1000: OIDBool, 1001: OIDBytea, 1002: OIDChar, 1003: OIDName, 1016: OIDInt8, 1005: OIDInt2,
	1007: OIDInt4, 1009: OIDText, 1028: OIDOid, 199: OIDJSON, 143: OIDXML, 1021: OIDFloat4,
	1022: OIDFloat8, 1014: OIDBPChar, 1015: OIDVarchar, 1182: OIDDate, 1115: OIDTimestamp,
	1185: OIDTimestamptz, 1231: OIDNumeric, 2951: OIDUUID, 3807: OIDJSONB,
}

// binaryOID says whether values of the type are read in the binary format;
// the others come as text and are returned as strings.
func binaryOID(oid uint32) bool {
	switch oid {
	case OIDBool, OIDBytea, OIDChar, OIDName, OIDInt8, OIDInt2, OIDInt4, OIDText, OIDOid,
		OIDJSON, OIDXML, OIDFloat4, OIDFloat8, OIDBPChar, OIDVarchar, OIDDate, OIDTimestamp,
		OIDTimestamptz, OIDNumeric, OIDUUID, OIDJSONB:
		return true
	}
	_, ok := arrayElem[oid]
	return ok
}

// Numeric is a numeric value as its decimal text, exact: "12.50", "NaN",
// "Infinity". As a parameter it is sent as it is.
type Numeric string

// JSON is a json or jsonb value as its text. As a parameter it is sent as
// it is.
type JSON []byte

// Infinite timestamps and dates are returned as these strings, since
// time.Time has no infinity.
const (
	Infinity    = "infinity"
	NegInfinity = "-infinity"
)

var pgEpoch = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

const pgEpochUnixMicro = 946684800000000

// decodeBinary turns a value in the binary format into Go: nil, bool,
// int64, float64, string, []byte, time.Time, Numeric, JSON, a UUID as its
// string, or []any for an array. It copies; nothing refers to b after.
func decodeBinary(oid uint32, b []byte) (any, error) {
	switch oid {
	case OIDBool:
		if len(b) != 1 {
			return nil, errLen(oid)
		}
		return b[0] != 0, nil
	case OIDBytea:
		return append([]byte{}, b...), nil
	case OIDChar, OIDName, OIDText, OIDBPChar, OIDVarchar, OIDXML:
		return string(b), nil
	case OIDInt2:
		if len(b) != 2 {
			return nil, errLen(oid)
		}
		return int64(int16(binary.BigEndian.Uint16(b))), nil
	case OIDInt4:
		if len(b) != 4 {
			return nil, errLen(oid)
		}
		return int64(int32(binary.BigEndian.Uint32(b))), nil
	case OIDOid:
		if len(b) != 4 {
			return nil, errLen(oid)
		}
		return int64(binary.BigEndian.Uint32(b)), nil
	case OIDInt8:
		if len(b) != 8 {
			return nil, errLen(oid)
		}
		return int64(binary.BigEndian.Uint64(b)), nil
	case OIDFloat4:
		if len(b) != 4 {
			return nil, errLen(oid)
		}
		return float64(math.Float32frombits(binary.BigEndian.Uint32(b))), nil
	case OIDFloat8:
		if len(b) != 8 {
			return nil, errLen(oid)
		}
		return math.Float64frombits(binary.BigEndian.Uint64(b)), nil
	case OIDJSON:
		return JSON(append([]byte{}, b...)), nil
	case OIDJSONB:
		if len(b) < 1 || b[0] != 1 {
			return nil, errors.New("postgres: a jsonb value of an unknown version")
		}
		return JSON(append([]byte{}, b[1:]...)), nil
	case OIDUUID:
		if len(b) != 16 {
			return nil, errLen(oid)
		}
		return formatUUID(b), nil
	case OIDDate:
		if len(b) != 4 {
			return nil, errLen(oid)
		}
		d := int32(binary.BigEndian.Uint32(b))
		switch d {
		case math.MaxInt32:
			return Infinity, nil
		case math.MinInt32:
			return NegInfinity, nil
		}
		return pgEpoch.AddDate(0, 0, int(d)), nil
	case OIDTimestamp, OIDTimestamptz:
		if len(b) != 8 {
			return nil, errLen(oid)
		}
		us := int64(binary.BigEndian.Uint64(b))
		switch us {
		case math.MaxInt64:
			return Infinity, nil
		case math.MinInt64:
			return NegInfinity, nil
		}
		return time.UnixMicro(us + pgEpochUnixMicro).UTC(), nil
	case OIDNumeric:
		s, err := decodeNumeric(b)
		return Numeric(s), err
	}
	if elem, ok := arrayElem[oid]; ok {
		return decodeArray(elem, b)
	}
	return nil, fmt.Errorf("postgres: no binary decoder for type %d", oid)
}

func errLen(oid uint32) error { return fmt.Errorf("postgres: a value of type %d of a bad length", oid) }

func formatUUID(b []byte) string {
	var s [36]byte
	hex.Encode(s[0:8], b[0:4])
	s[8] = '-'
	hex.Encode(s[9:13], b[4:6])
	s[13] = '-'
	hex.Encode(s[14:18], b[6:8])
	s[18] = '-'
	hex.Encode(s[19:23], b[8:10])
	s[23] = '-'
	hex.Encode(s[24:], b[10:16])
	return string(s[:])
}

// decodeNumeric turns numeric's binary form (base-10000 digits) into its
// decimal text with the value's scale.
func decodeNumeric(b []byte) (string, error) {
	if len(b) < 8 {
		return "", errLen(OIDNumeric)
	}
	nd := int(int16(binary.BigEndian.Uint16(b)))
	weight := int(int16(binary.BigEndian.Uint16(b[2:])))
	sign := binary.BigEndian.Uint16(b[4:])
	dscale := int(binary.BigEndian.Uint16(b[6:]))
	if nd < 0 || len(b) != 8+2*nd || dscale > 16383 {
		return "", errLen(OIDNumeric)
	}
	switch sign {
	case 0xC000:
		return "NaN", nil
	case 0xD000:
		return "Infinity", nil
	case 0xF000:
		return "-Infinity", nil
	case 0, 0x4000:
	default:
		return "", errors.New("postgres: a numeric of an unknown sign")
	}
	digit := func(i int) int {
		if i >= 0 && i < nd {
			return int(binary.BigEndian.Uint16(b[8+2*i:]))
		}
		return 0
	}
	var s []byte
	if sign == 0x4000 {
		s = append(s, '-')
	}
	if weight < 0 {
		s = append(s, '0')
	} else {
		for i := 0; i <= weight; i++ {
			if i == 0 {
				s = strconv.AppendInt(s, int64(digit(0)), 10)
			} else {
				s = appendDigit4(s, digit(i))
			}
		}
	}
	if dscale > 0 {
		s = append(s, '.')
		start := len(s)
		for i := weight + 1; len(s)-start < dscale; i++ {
			s = appendDigit4(s, digit(i))
		}
		s = s[:start+dscale]
	}
	return string(s), nil
}

func appendDigit4(s []byte, d int) []byte {
	return append(s, byte('0'+d/1000%10), byte('0'+d/100%10), byte('0'+d/10%10), byte('0'+d%10))
}

// decodeArray reads an array's binary form; more than one dimension gives
// nested []any.
func decodeArray(elem uint32, b []byte) (any, error) {
	if len(b) < 12 {
		return nil, errors.New("postgres: a short array")
	}
	ndim := int(int32(binary.BigEndian.Uint32(b)))
	if ndim == 0 {
		return []any{}, nil
	}
	if ndim < 0 || ndim > 6 || len(b) < 12+8*ndim {
		return nil, errors.New("postgres: a malformed array")
	}
	dims := make([]int, ndim)
	total := 1
	for i := range dims {
		dims[i] = int(int32(binary.BigEndian.Uint32(b[12+8*i:])))
		if dims[i] < 0 || dims[i] > len(b) {
			return nil, errors.New("postgres: a malformed array")
		}
		total *= dims[i]
		if total > len(b) {
			return nil, errors.New("postgres: a malformed array")
		}
	}
	b = b[12+8*ndim:]
	flat := make([]any, total)
	for i := range flat {
		if len(b) < 4 {
			return nil, errors.New("postgres: a short array")
		}
		n := int(int32(binary.BigEndian.Uint32(b)))
		b = b[4:]
		if n < 0 {
			continue
		}
		if n > len(b) {
			return nil, errors.New("postgres: a short array")
		}
		v, err := decodeBinary(elem, b[:n])
		if err != nil {
			return nil, err
		}
		flat[i] = v
		b = b[n:]
	}
	var nest func(d int, vals []any) []any
	nest = func(d int, vals []any) []any {
		if d == ndim-1 {
			return vals
		}
		out := make([]any, dims[d])
		step := len(vals) / max(dims[d], 1)
		for i := range out {
			out[i] = nest(d+1, vals[i*step:(i+1)*step])
		}
		return out
	}
	return nest(0, flat), nil
}

// appendParam appends a parameter's text form for a parameter of type oid
// (0 when the server infers it): nil is NULL (reported as false), strings
// and []byte as they are (bytea's []byte goes in binary, see paramFormat),
// numbers, bools, times by the parameter's type, Numeric, JSON,
// json.RawMessage, time.Duration as an interval, and slices as array
// literals.
func appendParam(dst []byte, oid uint32, v any) ([]byte, bool, error) {
	switch x := v.(type) {
	case nil:
		return dst, false, nil
	case string:
		return append(dst, x...), true, nil
	case []byte:
		if x == nil {
			return dst, false, nil
		}
		if oid == OIDBytea {
			return append(dst, x...), true, nil
		}
		return append(dst, x...), true, nil
	case Numeric:
		return append(dst, x...), true, nil
	case JSON:
		return append(dst, x...), true, nil
	case json.RawMessage:
		return append(dst, x...), true, nil
	case bool:
		if x {
			return append(dst, 't'), true, nil
		}
		return append(dst, 'f'), true, nil
	case int:
		return strconv.AppendInt(dst, int64(x), 10), true, nil
	case int8:
		return strconv.AppendInt(dst, int64(x), 10), true, nil
	case int16:
		return strconv.AppendInt(dst, int64(x), 10), true, nil
	case int32:
		return strconv.AppendInt(dst, int64(x), 10), true, nil
	case int64:
		return strconv.AppendInt(dst, x, 10), true, nil
	case uint:
		return strconv.AppendUint(dst, uint64(x), 10), true, nil
	case uint8:
		return strconv.AppendUint(dst, uint64(x), 10), true, nil
	case uint16:
		return strconv.AppendUint(dst, uint64(x), 10), true, nil
	case uint32:
		return strconv.AppendUint(dst, uint64(x), 10), true, nil
	case uint64:
		return strconv.AppendUint(dst, x, 10), true, nil
	case float32:
		return appendFloat(dst, float64(x), 32), true, nil
	case float64:
		return appendFloat(dst, x, 64), true, nil
	case time.Time:
		return appendTime(dst, oid, x), true, nil
	case time.Duration:
		return append(strconv.AppendInt(dst, x.Microseconds(), 10), " microseconds"...), true, nil
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		if rv.Kind() == reflect.Slice && rv.IsNil() {
			return dst, false, nil
		}
		var err error
		dst, err = appendArrayLiteral(dst, rv)
		return dst, true, err
	case reflect.Pointer:
		if rv.IsNil() {
			return dst, false, nil
		}
		return appendParam(dst, oid, rv.Elem().Interface())
	}
	return dst, false, fmt.Errorf("postgres: a parameter of type %T", v)
}

func appendFloat(dst []byte, f float64, bits int) []byte {
	switch {
	case math.IsNaN(f):
		return append(dst, "NaN"...)
	case math.IsInf(f, 1):
		return append(dst, "Infinity"...)
	case math.IsInf(f, -1):
		return append(dst, "-Infinity"...)
	}
	return strconv.AppendFloat(dst, f, 'g', -1, bits)
}

// appendTime writes t for a date, a timestamp (its wall clock in its own
// location) or, by default, a timestamptz (with its offset).
func appendTime(dst []byte, oid uint32, t time.Time) []byte {
	switch oid {
	case OIDDate:
		return t.AppendFormat(dst, "2006-01-02")
	case OIDTimestamp:
		return t.AppendFormat(dst, "2006-01-02 15:04:05.999999")
	case OIDTime:
		return t.AppendFormat(dst, "15:04:05.999999")
	}
	return t.AppendFormat(dst, "2006-01-02 15:04:05.999999Z07:00")
}

// appendArrayLiteral writes a slice as an array literal: {"a","b",NULL},
// nested slices as nested braces.
func appendArrayLiteral(dst []byte, rv reflect.Value) ([]byte, error) {
	if rv.Type().Elem().Kind() == reflect.Uint8 {
		return dst, errors.New("postgres: a byte array as an array parameter")
	}
	dst = append(dst, '{')
	for i := 0; i < rv.Len(); i++ {
		if i > 0 {
			dst = append(dst, ',')
		}
		e := rv.Index(i)
		for e.Kind() == reflect.Interface && !e.IsNil() {
			e = e.Elem()
		}
		if (e.Kind() == reflect.Interface || e.Kind() == reflect.Pointer) && e.IsNil() {
			dst = append(dst, "NULL"...)
			continue
		}
		if (e.Kind() == reflect.Slice || e.Kind() == reflect.Array) && e.Type().Elem().Kind() != reflect.Uint8 {
			var err error
			if dst, err = appendArrayLiteral(dst, e); err != nil {
				return dst, err
			}
			continue
		}
		var text []byte
		var err error
		if b, ok := e.Interface().([]byte); ok {
			if b == nil {
				dst = append(dst, "NULL"...)
				continue
			}
			text = append([]byte(`\x`), hex.EncodeToString(b)...)
		} else {
			var notNull bool
			text, notNull, err = appendParam(nil, 0, e.Interface())
			if err != nil {
				return dst, err
			}
			if !notNull {
				dst = append(dst, "NULL"...)
				continue
			}
		}
		dst = append(dst, '"')
		for _, c := range text {
			if c == '"' || c == '\\' {
				dst = append(dst, '\\')
			}
			dst = append(dst, c)
		}
		dst = append(dst, '"')
	}
	return append(dst, '}'), nil
}
