package syncpostgres

import (
	"encoding/hex"
	"math"
	"strconv"
	"strings"
	"time"

	fitsync "github.com/lesismal/fit/sync"
	pgwire "github.com/lesismal/fit/sync/postgres/wire"
)

// pgoutput sends a column's value as the type's text output, read here by
// its type OID into a typed value as pgwire reads the binary forms: the
// source's session sets DateStyle ISO, TimeZone UTC, extra_float_digits 3
// and bytea_output hex, so the forms below are the ones it gets. A type not
// listed, or a value the reading refuses (a BC date, a year past 9999), is
// the server's text.

// Array types read element by element, by their elements' type.
var srcArrayElem = map[uint32]uint32{
	1000: pgwire.OIDBool, 1001: pgwire.OIDBytea, 1002: pgwire.OIDChar, 1003: pgwire.OIDName,
	1005: pgwire.OIDInt2, 1007: pgwire.OIDInt4, 1016: pgwire.OIDInt8, 1028: pgwire.OIDOid,
	1009: pgwire.OIDText, 1014: pgwire.OIDBPChar, 1015: pgwire.OIDVarchar,
	1021: pgwire.OIDFloat4, 1022: pgwire.OIDFloat8, 1231: pgwire.OIDNumeric,
	2951: pgwire.OIDUUID, 199: pgwire.OIDJSON, 3807: pgwire.OIDJSONB,
	1182: pgwire.OIDDate, 1115: pgwire.OIDTimestamp, 1185: pgwire.OIDTimestamptz,
}

// decodeText reads a column's text by its type.
func decodeText(oid uint32, s string) fitsync.Value {
	switch oid {
	case pgwire.OIDBool:
		switch s {
		case "t":
			return fitsync.Bool(true)
		case "f":
			return fitsync.Bool(false)
		}
	case pgwire.OIDInt2, pgwire.OIDInt4, pgwire.OIDInt8:
		if i, err := strconv.ParseInt(s, 10, 64); err == nil {
			return fitsync.Int(i)
		}
	case pgwire.OIDOid:
		if u, err := strconv.ParseUint(s, 10, 32); err == nil {
			return fitsync.Int(int64(u))
		}
	case pgwire.OIDFloat4, pgwire.OIDFloat8:
		switch s {
		case "NaN":
			return fitsync.Float(math.NaN())
		case "Infinity":
			return fitsync.Float(math.Inf(1))
		case "-Infinity":
			return fitsync.Float(math.Inf(-1))
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return fitsync.Float(f)
		}
	case pgwire.OIDNumeric:
		return fitsync.Decimal(s)
	case pgwire.OIDBytea:
		if b, ok := strings.CutPrefix(s, `\x`); ok {
			if d, err := hex.DecodeString(b); err == nil {
				return fitsync.Bytes(d)
			}
		}
	case pgwire.OIDJSON, pgwire.OIDJSONB:
		return fitsync.JSON([]byte(s))
	case pgwire.OIDDate:
		if t, err := time.Parse("2006-01-02", s); err == nil {
			return fitsync.Date(t.Year(), t.Month(), t.Day())
		}
	case pgwire.OIDTimestamp:
		if t, err := time.Parse("2006-01-02 15:04:05.999999999", s); err == nil {
			return fitsync.Time(t)
		}
	case pgwire.OIDTimestamptz:
		if t, ok := parseTextTimestamptz(s); ok {
			return fitsync.Time(t)
		}
	default:
		if elem, ok := srcArrayElem[oid]; ok {
			if v, ok := decodeTextArray(elem, s); ok {
				return v
			}
		}
	}
	return fitsync.String(s)
}

// parseTextTimestamptz reads "2026-11-08 10:00:00.5+00", the offset in hours,
// or hours and minutes, or hours, minutes and seconds.
func parseTextTimestamptz(s string) (time.Time, bool) {
	i := strings.LastIndexAny(s, "+-")
	if i < len("2006-01-02 15:04:05") {
		return time.Time{}, false
	}
	t, err := time.Parse("2006-01-02 15:04:05.999999999", s[:i])
	if err != nil {
		return time.Time{}, false
	}
	parts := strings.Split(s[i+1:], ":")
	off := 0
	for j, mul := range []int{3600, 60, 1} {
		if j >= len(parts) {
			break
		}
		n, err := strconv.Atoi(parts[j])
		if err != nil || len(parts[j]) != 2 || len(parts) > 3 {
			return time.Time{}, false
		}
		off += n * mul
	}
	if s[i] == '-' {
		off = -off
	}
	return t.Add(-time.Duration(off) * time.Second).In(time.FixedZone("", off)), true
}

// decodeTextArray reads an array's text, "{1,2,NULL}", "{{a,b},{c,d}}" or with
// its bounds, "[0:1]={1,2}", nested arrays for more dimensions.
func decodeTextArray(elem uint32, s string) (fitsync.Value, bool) {
	if strings.HasPrefix(s, "[") {
		i := strings.Index(s, "=")
		if i < 0 {
			return fitsync.Value{}, false
		}
		s = s[i+1:]
	}
	p := textArrayParser{s: s, elem: elem}
	v, ok := p.array()
	if !ok || p.i != len(p.s) {
		return fitsync.Value{}, false
	}
	return v, true
}

type textArrayParser struct {
	s    string
	i    int
	elem uint32
}

func (p *textArrayParser) array() (fitsync.Value, bool) {
	if p.i >= len(p.s) || p.s[p.i] != '{' {
		return fitsync.Value{}, false
	}
	p.i++
	var elems []fitsync.Value
	if p.i < len(p.s) && p.s[p.i] == '}' {
		p.i++
		return fitsync.Array(), true
	}
	for {
		if p.i >= len(p.s) {
			return fitsync.Value{}, false
		}
		var v fitsync.Value
		switch p.s[p.i] {
		case '{':
			a, ok := p.array()
			if !ok {
				return fitsync.Value{}, false
			}
			v = a
		case '"':
			str, ok := p.quoted()
			if !ok {
				return fitsync.Value{}, false
			}
			v = decodeText(p.elem, str)
		default:
			j := p.i
			for p.i < len(p.s) && p.s[p.i] != ',' && p.s[p.i] != '}' {
				p.i++
			}
			if str := p.s[j:p.i]; str == "NULL" {
				v = fitsync.Null()
			} else {
				v = decodeText(p.elem, str)
			}
		}
		elems = append(elems, v)
		if p.i >= len(p.s) {
			return fitsync.Value{}, false
		}
		switch p.s[p.i] {
		case ',':
			p.i++
		case '}':
			p.i++
			return fitsync.Array(elems...), true
		default:
			return fitsync.Value{}, false
		}
	}
}

// quoted reads a double-quoted element, its backslashes escaping the next
// byte.
func (p *textArrayParser) quoted() (string, bool) {
	p.i++
	var b strings.Builder
	for p.i < len(p.s) {
		c := p.s[p.i]
		switch c {
		case '\\':
			p.i++
			if p.i >= len(p.s) {
				return "", false
			}
			b.WriteByte(p.s[p.i])
		case '"':
			p.i++
			return b.String(), true
		default:
			b.WriteByte(c)
		}
		p.i++
	}
	return "", false
}
