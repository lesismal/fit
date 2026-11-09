package syncdoris

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	fitsync "github.com/lesismal/fit/sync"
)

// dateTimeLayout is how a DATETIME is written to Doris and StarRocks: a
// space between the date and the time, microseconds when there are any.
const dateTimeLayout = "2006-01-02 15:04:05.999999"

// appendJSON appends a column's value to a JSON row: a time as a DATETIME
// in zone, a date as a DATE, anything else as its plain JSON.
func appendJSON(b []byte, v fitsync.Value, zone *time.Location) ([]byte, error) {
	switch v.Kind() {
	case fitsync.KindTime:
		return fitsync.AppendJSONString(b, v.AsTime().In(zone).Format(dateTimeLayout)), nil
	case fitsync.KindDate:
		return fitsync.AppendJSONString(b, v.AsTime().Format(time.DateOnly)), nil
	}
	return fitsync.AppendPlain(b, v)
}

// appendCSV appends a column's value to a CSV row, as the load's enclose
// (") and escape (\) read it: null as null says (Doris reads \N as null,
// StarRocks, which unescapes first, \\N), numbers and booleans bare, every
// other value in quotes, a quote or a backslash in it escaped.
func appendCSV(b []byte, v fitsync.Value, zone *time.Location, null string) ([]byte, error) {
	var s string
	switch v.Kind() {
	case fitsync.KindNull:
		return append(b, null...), nil
	case fitsync.KindBool, fitsync.KindInt, fitsync.KindUint, fitsync.KindDecimal:
		return append(b, fitsync.TextOf(v)...), nil
	case fitsync.KindFloat:
		f := v.AsFloat()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("%v is no number to load", f)
		}
		return strconv.AppendFloat(b, f, 'g', -1, 64), nil
	case fitsync.KindTime:
		s = v.AsTime().In(zone).Format(dateTimeLayout)
	case fitsync.KindDate:
		s = v.AsTime().Format(time.DateOnly)
	case fitsync.KindString:
		s = v.Text()
	default:
		p, err := fitsync.AppendPlain(nil, v)
		if err != nil {
			return nil, err
		}
		if v.Kind() == fitsync.KindBytes {
			p = p[1 : len(p)-1] // base64, without its JSON quotes
		}
		s = string(p)
	}
	b = append(b, '"')
	for i := 0; i < len(s); i++ {
		if s[i] == '"' || s[i] == '\\' {
			b = append(b, '\\')
		}
		b = append(b, s[i])
	}
	return append(b, '"'), nil
}

// literal returns v as a SQL literal Doris and StarRocks compare a column
// with: numbers and booleans bare, anything else a quoted string, a time
// as a DATETIME's text in UTC, as the source reads them.
func literal(v fitsync.Value) (string, error) {
	switch v.Kind() {
	case fitsync.KindNull:
		return "", errors.New("a null cannot order rows")
	case fitsync.KindBool:
		if v.AsBool() {
			return "TRUE", nil
		}
		return "FALSE", nil
	case fitsync.KindInt, fitsync.KindUint, fitsync.KindDecimal:
		return fitsync.TextOf(v), nil
	case fitsync.KindFloat:
		f := v.AsFloat()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return "", fmt.Errorf("%v cannot order rows", f)
		}
		return strconv.FormatFloat(f, 'g', -1, 64), nil
	case fitsync.KindTime:
		return quote(v.AsTime().UTC().Format(dateTimeLayout)), nil
	case fitsync.KindDate:
		return quote(v.AsTime().Format(time.DateOnly)), nil
	case fitsync.KindString, fitsync.KindBytes:
		return quote(v.Text()), nil
	}
	return "", fmt.Errorf("a %s cannot order rows", v.Kind())
}

// quote quotes s as a string literal, a quote and a backslash escaped by a
// backslash, as MySQL's protocol reads them on Doris and StarRocks.
func quote(s string) string {
	var b strings.Builder
	b.WriteByte('\'')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\\', '\'':
			b.WriteByte('\\')
			b.WriteByte(c)
		case 0:
			b.WriteString(`\0`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('\'')
	return b.String()
}

// checkLiteral reports whether s is a literal literal makes: a number, a
// boolean, or a quoted string whose quotes are escaped, so that a position
// read back from a checkpoint cannot carry SQL of its own.
func checkLiteral(s string) bool {
	switch {
	case s == "TRUE" || s == "FALSE":
		return true
	case len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'':
		in := s[1 : len(s)-1]
		for i := 0; i < len(in); i++ {
			switch in[i] {
			case '\\':
				if i+1 == len(in) {
					return false
				}
				i++
			case '\'':
				return false
			}
		}
		return true
	case s == "":
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c == '-' || c == '+' || c == '.' || c == 'e' || c == 'E') {
			return false
		}
	}
	return true
}

// quoteIdent quotes a name in backquotes, a backquote in it doubled.
func quoteIdent(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }

// QuoteName quotes db.table as `db`.`table`.
func QuoteName(name string) string {
	db, table, ok := strings.Cut(name, ".")
	if !ok {
		return quoteIdent(name)
	}
	return quoteIdent(db) + "." + quoteIdent(table)
}
