package syncclickhouse

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	fitsync "github.com/lesismal/fit/sync"
)

// The settings a query is read with: numbers bare, so that they keep their
// precision, and times in ISO 8601 in UTC, so that they need no zone.
var readSettings = map[string]string{
	"output_format_json_quote_64bit_integers": "0",
	"output_format_json_quote_64bit_floats":   "0",
	"output_format_json_quote_decimals":       "0",
	"date_time_output_format":                 "iso",
}

// unwrap returns a type without Nullable and LowCardinality around it.
func unwrap(t string) string {
	for {
		switch {
		case strings.HasPrefix(t, "Nullable(") && strings.HasSuffix(t, ")"):
			t = t[len("Nullable(") : len(t)-1]
		case strings.HasPrefix(t, "LowCardinality(") && strings.HasSuffix(t, ")"):
			t = t[len("LowCardinality(") : len(t)-1]
		default:
			return t
		}
	}
}

// base returns a type's name without its arguments: DateTime64 of
// DateTime64(3, 'UTC').
func base(t string) string {
	if i := strings.IndexByte(t, '('); i >= 0 {
		return t[:i]
	}
	return t
}

// ToValue turns a value of ClickHouse's JSON output, of the type t, into a
// Value: integers to int or uint (past 64 bits to decimal), floats to
// float, decimals to decimal, Bool to bool, Date and Date32 to date,
// DateTime and DateTime64 to time, String, FixedString, UUID, Enum and
// IP addresses to string, Array to array, Nullable's null to null, and
// anything else (Map, Tuple, JSON) to JSON.
func ToValue(t string, raw json.RawMessage) (fitsync.Value, error) {
	raw = bytes.TrimSpace(raw)
	if string(raw) == "null" {
		return fitsync.Null(), nil
	}
	t = unwrap(t)
	str := func() (string, error) {
		var s string
		err := json.Unmarshal(raw, &s)
		return s, err
	}
	// num returns a number's text, written bare or quoted.
	num := func() string {
		if len(raw) > 0 && raw[0] == '"' {
			s, _ := str()
			return s
		}
		return string(raw)
	}
	fail := func(err error) (fitsync.Value, error) {
		return fitsync.Value{}, fmt.Errorf("a %s of %s: %v", t, clip(raw), err)
	}
	switch b := base(t); b {
	case "Int8", "Int16", "Int32", "Int64":
		i, err := strconv.ParseInt(num(), 10, 64)
		if err != nil {
			return fail(err)
		}
		return fitsync.Int(i), nil
	case "UInt8", "UInt16", "UInt32", "UInt64":
		u, err := strconv.ParseUint(num(), 10, 64)
		if err != nil {
			return fail(err)
		}
		return fitsync.Uint(u), nil
	case "Int128", "Int256", "UInt128", "UInt256", "Decimal", "Decimal32", "Decimal64", "Decimal128", "Decimal256":
		return fitsync.Decimal(num()), nil
	case "Float32", "Float64", "BFloat16":
		f, err := strconv.ParseFloat(num(), 64)
		if err != nil {
			return fail(err)
		}
		return fitsync.Float(f), nil
	case "Bool":
		var v bool
		if err := json.Unmarshal(raw, &v); err != nil {
			return fail(err)
		}
		return fitsync.Bool(v), nil
	case "Date", "Date32":
		s, err := str()
		if err != nil {
			return fail(err)
		}
		d, err := time.Parse(time.DateOnly, s)
		if err != nil {
			return fail(err)
		}
		return fitsync.Date(d.Year(), d.Month(), d.Day()), nil
	case "DateTime", "DateTime64":
		s, err := str()
		if err != nil {
			return fail(err)
		}
		tm, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			return fail(err)
		}
		return fitsync.Time(tm), nil
	case "String", "FixedString", "UUID", "Enum8", "Enum16", "IPv4", "IPv6":
		s, err := str()
		if err != nil {
			return fail(err)
		}
		if b == "FixedString" {
			s = strings.TrimRight(s, "\x00")
		}
		return fitsync.String(s), nil
	case "Array":
		var elems []json.RawMessage
		if err := json.Unmarshal(raw, &elems); err != nil {
			return fail(err)
		}
		et := t[len("Array(") : len(t)-1]
		vs := make([]fitsync.Value, len(elems))
		for i, e := range elems {
			v, err := ToValue(et, e)
			if err != nil {
				return fitsync.Value{}, err
			}
			vs[i] = v
		}
		return fitsync.Array(vs...), nil
	}
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return fail(err)
	}
	return fitsync.JSON(b.Bytes()), nil
}

// escapeParam writes s in the escaped form a query parameter's value is
// parsed from.
func escapeParam(s string) string {
	if !strings.ContainsAny(s, "\\\t\n\r\x00") {
		return s
	}
	return strings.NewReplacer(`\`, `\\`, "\t", `\t`, "\n", `\n`, "\r", `\r`, "\x00", `\0`).Replace(s)
}
