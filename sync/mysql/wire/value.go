package mysqlwire

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"
)

// Column types.
const (
	TypeDecimal    = 0x00
	TypeTiny       = 0x01
	TypeShort      = 0x02
	TypeLong       = 0x03
	TypeFloat      = 0x04
	TypeDouble     = 0x05
	TypeNull       = 0x06
	TypeTimestamp  = 0x07
	TypeLongLong   = 0x08
	TypeInt24      = 0x09
	TypeDate       = 0x0a
	TypeTime       = 0x0b
	TypeDateTime   = 0x0c
	TypeYear       = 0x0d
	TypeVarChar    = 0x0f
	TypeBit        = 0x10
	TypeJSON       = 0xf5
	TypeNewDecimal = 0xf6
	TypeEnum       = 0xf7
	TypeSet        = 0xf8
	TypeTinyBlob   = 0xf9
	TypeMediumBlob = 0xfa
	TypeLongBlob   = 0xfb
	TypeBlob       = 0xfc
	TypeVarString  = 0xfd
	TypeString     = 0xfe
	TypeGeometry   = 0xff
)

// Column flags.
const (
	FlagNotNull  = 0x0001
	FlagPriKey   = 0x0002
	FlagUnsigned = 0x0020
	FlagBinary   = 0x0080
)

// BinaryCollation is the ID of the binary character set, which binary
// strings and blobs have.
const BinaryCollation = 63

// Column is a column of a result set, or a statement's parameter.
type Column struct {
	Schema, Table, OrgTable, Name, OrgName string
	Collation                              uint16
	Length                                 uint32
	Type                                   byte
	Flags                                  uint16
	Decimals                               byte
}

// Unsigned tells whether the column is of an unsigned integer type.
func (c *Column) Unsigned() bool { return c.Flags&FlagUnsigned != 0 }

// IsBinary tells whether a string column holds bytes, not characters.
func (c *Column) IsBinary() bool { return c.Collation == BinaryCollation }

func parseColumn(p []byte) (Column, bool) {
	r := rd{b: p}
	r.lenEncBytes() // catalog
	var c Column
	c.Schema = string(r.lenEncBytes())
	c.Table = string(r.lenEncBytes())
	c.OrgTable = string(r.lenEncBytes())
	c.Name = string(r.lenEncBytes())
	c.OrgName = string(r.lenEncBytes())
	r.lenEnc()
	c.Collation = r.u16()
	c.Length = r.u32()
	c.Type = r.u8()
	c.Flags = r.u16()
	c.Decimals = r.u8()
	return c, !r.bad
}

// DateTime is a DATE, DATETIME or TIMESTAMP as the server has it: no zone,
// and the zero date and dates with zero parts allowed.
type DateTime struct {
	Year                             uint16
	Month, Day, Hour, Minute, Second uint8
	Microsecond                      uint32
}

// IsZero tells whether d is 0000-00-00 00:00:00.
func (d DateTime) IsZero() bool { return d == DateTime{} }

// Time is d in loc; a zero date or zero parts are normalized as time.Date
// does.
func (d DateTime) Time(loc *time.Location) time.Time {
	return time.Date(int(d.Year), time.Month(d.Month), int(d.Day), int(d.Hour), int(d.Minute),
		int(d.Second), int(d.Microsecond)*1000, loc)
}

// FromTime is t's wall clock, to the microsecond (rounded down).
func FromTime(t time.Time) DateTime {
	y, mo, dd := t.Date()
	h, mi, s := t.Clock()
	return DateTime{Year: uint16(y), Month: uint8(mo), Day: uint8(dd), Hour: uint8(h), Minute: uint8(mi),
		Second: uint8(s), Microsecond: uint32(t.Nanosecond() / 1000)}
}

// String is d as the server writes a DATETIME, with six digits of
// fraction when it has one.
func (d DateTime) String() string {
	s := fmt.Sprintf("%04d-%02d-%02d %02d:%02d:%02d", d.Year, d.Month, d.Day, d.Hour, d.Minute, d.Second)
	if d.Microsecond != 0 {
		s += fmt.Sprintf(".%06d", d.Microsecond)
	}
	return s
}

// parseDateTime reads "YYYY-MM-DD[ HH:MM:SS[.ffffff]]".
func parseDateTime(b []byte) (DateTime, bool) {
	var d DateTime
	if len(b) < 10 || b[4] != '-' || b[7] != '-' {
		return d, false
	}
	y, ok1 := digits(b[0:4])
	mo, ok2 := digits(b[5:7])
	dd, ok3 := digits(b[8:10])
	if !ok1 || !ok2 || !ok3 {
		return d, false
	}
	d.Year, d.Month, d.Day = uint16(y), uint8(mo), uint8(dd)
	if len(b) == 10 {
		return d, true
	}
	if len(b) < 19 || b[10] != ' ' || b[13] != ':' || b[16] != ':' {
		return d, false
	}
	h, ok1 := digits(b[11:13])
	mi, ok2 := digits(b[14:16])
	s, ok3 := digits(b[17:19])
	if !ok1 || !ok2 || !ok3 {
		return d, false
	}
	d.Hour, d.Minute, d.Second = uint8(h), uint8(mi), uint8(s)
	us, ok := fraction(b[19:])
	d.Microsecond = us
	return d, ok
}

// parseTime reads a TIME, "[-]H+:MM:SS[.ffffff]".
func parseTime(b []byte) (time.Duration, bool) {
	neg := len(b) > 0 && b[0] == '-'
	if neg {
		b = b[1:]
	}
	i := 0
	for i < len(b) && b[i] != ':' {
		i++
	}
	if i == 0 || len(b) < i+6 || b[i+3] != ':' {
		return 0, false
	}
	h, ok1 := digits(b[:i])
	m, ok2 := digits(b[i+1 : i+3])
	s, ok3 := digits(b[i+4 : i+6])
	us, ok4 := fraction(b[i+6:])
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return 0, false
	}
	d := time.Duration(h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(s)*time.Second +
		time.Duration(us)*time.Microsecond
	if neg {
		d = -d
	}
	return d, true
}

func digits(b []byte) (int, bool) {
	if len(b) == 0 || len(b) > 9 {
		return 0, false
	}
	n := 0
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

// fraction reads "" or ".f{1,6}" as microseconds.
func fraction(b []byte) (uint32, bool) {
	if len(b) == 0 {
		return 0, true
	}
	if b[0] != '.' || len(b) < 2 || len(b) > 7 {
		return 0, false
	}
	n, ok := digits(b[1:])
	for i := len(b) - 1; i < 6; i++ {
		n *= 10
	}
	return uint32(n), ok
}

// textValue decodes a column of a text row.
func textValue(col *Column, b []byte) (any, error) {
	if b == nil {
		return nil, nil
	}
	switch col.Type {
	case TypeTiny, TypeShort, TypeInt24, TypeLong, TypeLongLong:
		if col.Unsigned() {
			return strconv.ParseUint(string(b), 10, 64)
		}
		return strconv.ParseInt(string(b), 10, 64)
	case TypeYear:
		return strconv.ParseInt(string(b), 10, 64)
	case TypeFloat:
		f, err := strconv.ParseFloat(string(b), 32)
		return float32(f), err
	case TypeDouble:
		return strconv.ParseFloat(string(b), 64)
	case TypeDate, TypeDateTime, TypeTimestamp:
		d, ok := parseDateTime(b)
		if !ok {
			return nil, fmt.Errorf("%w: %s %q", ErrMalformed, col.Name, b)
		}
		return d, nil
	case TypeTime:
		d, ok := parseTime(b)
		if !ok {
			return nil, fmt.Errorf("%w: %s %q", ErrMalformed, col.Name, b)
		}
		return d, nil
	}
	return b, nil
}

// binaryValue decodes a column of a binary row from r.
func binaryValue(col *Column, r *rd) any {
	switch col.Type {
	case TypeTiny:
		v := r.u8()
		if col.Unsigned() {
			return uint64(v)
		}
		return int64(int8(v))
	case TypeShort:
		v := r.u16()
		if col.Unsigned() {
			return uint64(v)
		}
		return int64(int16(v))
	case TypeYear:
		return int64(r.u16())
	case TypeInt24, TypeLong:
		v := r.u32()
		if col.Unsigned() {
			return uint64(v)
		}
		return int64(int32(v))
	case TypeLongLong:
		v := r.u64()
		if col.Unsigned() {
			return v
		}
		return int64(v)
	case TypeFloat:
		return math.Float32frombits(r.u32())
	case TypeDouble:
		return math.Float64frombits(r.u64())
	case TypeDate, TypeDateTime, TypeTimestamp:
		var d DateTime
		n := r.u8()
		if n >= 4 {
			d.Year, d.Month, d.Day = r.u16(), r.u8(), r.u8()
		}
		if n >= 7 {
			d.Hour, d.Minute, d.Second = r.u8(), r.u8(), r.u8()
		}
		if n >= 11 {
			d.Microsecond = r.u32()
		}
		if n != 0 && n != 4 && n != 7 && n != 11 {
			r.bad = true
		}
		return d
	case TypeTime:
		n := r.u8()
		if n == 0 {
			return time.Duration(0)
		}
		if n != 8 && n != 12 {
			r.bad = true
			return nil
		}
		neg := r.u8() == 1
		days, h, m, s := r.u32(), r.u8(), r.u8(), r.u8()
		var us uint32
		if n == 12 {
			us = r.u32()
		}
		d := (time.Duration(days)*24+time.Duration(h))*time.Hour + time.Duration(m)*time.Minute +
			time.Duration(s)*time.Second + time.Duration(us)*time.Microsecond
		if neg {
			d = -d
		}
		return d
	}
	return r.lenEncBytes()
}

// appendParam appends a parameter's type to types and its value to vals;
// null tells that it is NULL, to be set in the bitmap.
func appendParam(types, vals []byte, v any, loc *time.Location) (_, _ []byte, null bool, err error) {
	switch v := v.(type) {
	case nil:
		return append(types, TypeNull, 0), vals, true, nil
	case bool:
		b := byte(0)
		if v {
			b = 1
		}
		return append(types, TypeTiny, 0), append(vals, b), false, nil
	case int:
		return appendInt(types, vals, int64(v))
	case int8:
		return appendInt(types, vals, int64(v))
	case int16:
		return appendInt(types, vals, int64(v))
	case int32:
		return appendInt(types, vals, int64(v))
	case int64:
		return appendInt(types, vals, v)
	case uint:
		return appendUint(types, vals, uint64(v))
	case uint8:
		return appendUint(types, vals, uint64(v))
	case uint16:
		return appendUint(types, vals, uint64(v))
	case uint32:
		return appendUint(types, vals, uint64(v))
	case uint64:
		return appendUint(types, vals, v)
	case float32:
		return append(types, TypeFloat, 0), binary.LittleEndian.AppendUint32(vals, math.Float32bits(v)), false, nil
	case float64:
		return append(types, TypeDouble, 0), binary.LittleEndian.AppendUint64(vals, math.Float64bits(v)), false, nil
	case string:
		return append(types, TypeString, 0), appendLenEncBytes(vals, v), false, nil
	case []byte:
		if v == nil {
			return append(types, TypeNull, 0), vals, true, nil
		}
		return append(types, TypeString, 0), appendLenEncBytes(vals, v), false, nil
	case json.RawMessage:
		if v == nil {
			return append(types, TypeNull, 0), vals, true, nil
		}
		return append(types, TypeString, 0), appendLenEncBytes(vals, []byte(v)), false, nil
	case time.Time:
		return append(types, TypeDateTime, 0), appendDateTime(vals, FromTime(v.In(loc))), false, nil
	case DateTime:
		return append(types, TypeDateTime, 0), appendDateTime(vals, v), false, nil
	case time.Duration:
		return append(types, TypeTime, 0), appendTime(vals, v), false, nil
	}
	return types, vals, false, fmt.Errorf("mysql: a parameter of type %T", v)
}

func appendInt(types, vals []byte, v int64) ([]byte, []byte, bool, error) {
	return append(types, TypeLongLong, 0), binary.LittleEndian.AppendUint64(vals, uint64(v)), false, nil
}

func appendUint(types, vals []byte, v uint64) ([]byte, []byte, bool, error) {
	return append(types, TypeLongLong, 0x80), binary.LittleEndian.AppendUint64(vals, v), false, nil
}

func appendDateTime(b []byte, d DateTime) []byte {
	n := byte(0)
	switch {
	case d.Microsecond != 0:
		n = 11
	case d.Hour != 0 || d.Minute != 0 || d.Second != 0:
		n = 7
	case d != DateTime{}:
		n = 4
	}
	b = append(b, n)
	if n >= 4 {
		b = binary.LittleEndian.AppendUint16(b, d.Year)
		b = append(b, d.Month, d.Day)
	}
	if n >= 7 {
		b = append(b, d.Hour, d.Minute, d.Second)
	}
	if n == 11 {
		b = binary.LittleEndian.AppendUint32(b, d.Microsecond)
	}
	return b
}

func appendTime(b []byte, d time.Duration) []byte {
	if d == 0 {
		return append(b, 0)
	}
	neg := byte(0)
	if d < 0 {
		neg, d = 1, -d
	}
	us := uint32(d % time.Second / time.Microsecond)
	s := int64(d / time.Second)
	days := uint32(s / 86400)
	s %= 86400
	n := byte(8)
	if us != 0 {
		n = 12
	}
	b = append(b, n, neg)
	b = binary.LittleEndian.AppendUint32(b, days)
	b = append(b, byte(s/3600), byte(s/60%60), byte(s%60))
	if us != 0 {
		b = binary.LittleEndian.AppendUint32(b, us)
	}
	return b
}
