package mysqlwire

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"
)

// StatusNoBackslashEscapes is the server status flag saying the session's
// sql_mode has NO_BACKSLASH_ESCAPES, in which a backslash in a string
// literal is itself.
const StatusNoBackslashEscapes = 0x0200

// asciiSafe reports whether a collation's character set never has the
// bytes of ', " and \ inside a character of several bytes, so that
// escaping a literal byte by byte is exact: utf8mb4, utf8mb3, latin1,
// ascii and binary. GBK, Big5, SJIS, CP932 and GB18030 have them, and
// their connections are sent parameters instead.
func asciiSafe(collation uint8) bool {
	switch {
	case collation == 45 || collation == 46 || collation >= 224 && collation <= 247 || collation == 255: // utf8mb4
	case collation == 33 || collation == 83 || collation >= 192 && collation <= 215 || collation == 223: // utf8mb3
	case collation == 5 || collation == 8 || collation == 15 || collation == 31 || collation >= 47 && collation <= 49 || collation == 94: // latin1
	case collation == 11 || collation == 65 || collation == 63: // ascii, binary
	default:
		return false
	}
	return true
}

// InsertRowsText is InsertRows with the rows' values written in the
// statements as literals and each statement sent as a text query: one
// round trip a statement, where a prepared one takes a prepare's and an
// execute's, and no statement left to close. Each value's literal stands
// for what InsertRows sends as its parameter, so that a column gets the
// same value either way: integers as numbers, floats in their shortest
// exponent form (a DOUBLE, as the binary protocol sends them), strings,
// []byte and JSON as quoted strings of the connection's character set, as
// the binary protocol sends all three (a _binary string would not go in a
// JSON column), times as DATETIME text in Config.Location, durations as
// TIME text; strings escaped as the session's sql_mode reads them (its
// NO_BACKSLASH_ESCAPES, from the server's last status). Checked against
// MySQL 5.7, 8.0 and 8.4 and MariaDB 10.11 and 11 for every column type,
// in strict mode and not: the rows read back byte for byte as InsertRows'
// (test/sync's TestMySQLTextInsertsMatchPrepared). A statement takes as
// many rows as max_allowed_packet allows. On a connection whose character
// set could
// hide a quote inside a character (a Config.Collation other than
// utf8mb4's, utf8mb3's, latin1's, ascii's or binary's) it is InsertRows.
func (c *Conn) InsertRowsText(ctx context.Context, head string, rows [][]any, tail string) (Result, error) {
	if !asciiSafe(c.cfg.Collation) {
		return c.InsertRows(ctx, head, rows, tail)
	}
	var total Result
	if len(rows) == 0 {
		return total, nil
	}
	ncols := len(rows[0])
	if ncols == 0 || ncols > MaxPlaceholders {
		return total, fmt.Errorf("mysql: rows of %d values", ncols)
	}
	for i, r := range rows {
		if len(r) != ncols {
			return total, fmt.Errorf("mysql: row %d has %d values, the first %d", i, len(r), ncols)
		}
	}
	maxPacket, err := c.MaxAllowedPacket(ctx)
	if err != nil {
		return total, err
	}
	// The command byte, and slack.
	budget := maxPacket - 1024
	first := true
	for at := 0; len(rows) > 0; {
		if err := c.begin(ctx); err != nil {
			return total, err
		}
		b := c.startCommand(comQuery)
		b = append(b, head...)
		b = append(b, ' ')
		n := 0
		for n < len(rows) {
			mark := len(b)
			if n > 0 {
				b = append(b, ',')
			}
			b = append(b, '(')
			for j, v := range rows[n] {
				if j > 0 {
					b = append(b, ',')
				}
				if b, err = c.appendLiteral(b, v); err != nil {
					c.wbuf = b
					c.end()
					return total, fmt.Errorf("%w (row %d, value %d)", err, at+n+1, j+1)
				}
			}
			b = append(b, ')')
			// The packet's payload past its 4-byte header, the tail with
			// its space.
			if n > 0 && len(b)-4+1+len(tail) > budget {
				b = b[:mark]
				break
			}
			n++
		}
		if tail != "" {
			b = append(b, ' ')
			b = append(b, tail...)
		}
		if err := c.writePacket(b); err != nil {
			return total, c.fail(err)
		}
		r, err := c.startRows(false, nil)
		if err != nil {
			return total, err
		}
		if err := r.Close(); err != nil {
			return total, err
		}
		total.AffectedRows += r.res.AffectedRows
		total.Status, total.Warnings = r.res.Status, total.Warnings+r.res.Warnings
		if first {
			total.LastInsertID = r.res.LastInsertID
			first = false
		}
		rows, at = rows[n:], at+n
	}
	return total, nil
}

// appendLiteral appends v as an SQL literal, the value InsertRows would
// send as its parameter.
func (c *Conn) appendLiteral(b []byte, v any) ([]byte, error) {
	noBackslash := c.status&StatusNoBackslashEscapes != 0
	switch v := v.(type) {
	case nil:
		return append(b, "NULL"...), nil
	case bool:
		if v {
			return append(b, '1'), nil
		}
		return append(b, '0'), nil
	case int:
		return strconv.AppendInt(b, int64(v), 10), nil
	case int8:
		return strconv.AppendInt(b, int64(v), 10), nil
	case int16:
		return strconv.AppendInt(b, int64(v), 10), nil
	case int32:
		return strconv.AppendInt(b, int64(v), 10), nil
	case int64:
		return strconv.AppendInt(b, v, 10), nil
	case uint:
		return strconv.AppendUint(b, uint64(v), 10), nil
	case uint8:
		return strconv.AppendUint(b, uint64(v), 10), nil
	case uint16:
		return strconv.AppendUint(b, uint64(v), 10), nil
	case uint32:
		return strconv.AppendUint(b, uint64(v), 10), nil
	case uint64:
		return strconv.AppendUint(b, v, 10), nil
	case float32:
		return appendFloat(b, float64(v))
	case float64:
		return appendFloat(b, v)
	case string:
		return appendQuoted(b, v, noBackslash), nil
	case []byte:
		if v == nil {
			return append(b, "NULL"...), nil
		}
		return appendQuoted(b, v, noBackslash), nil
	case json.RawMessage:
		if v == nil {
			return append(b, "NULL"...), nil
		}
		return appendQuoted(b, []byte(v), noBackslash), nil
	case time.Time:
		return appendDateTimeText(b, FromTime(v.In(c.loc))), nil
	case DateTime:
		return appendDateTimeText(b, v), nil
	case time.Duration:
		return appendTimeText(b, v), nil
	}
	return b, fmt.Errorf("mysql: a parameter of type %T", v)
}

// appendFloat writes f with an exponent, which MySQL reads as a DOUBLE
// (without one, 0.1 is a DECIMAL), in the fewest digits that read back as
// f.
func appendFloat(b []byte, f float64) ([]byte, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return b, fmt.Errorf("mysql: %v has no literal", f)
	}
	return strconv.AppendFloat(b, f, 'e', -1, 64), nil
}

// appendQuoted appends s quoted: with NO_BACKSLASH_ESCAPES the quote
// doubled alone, otherwise the characters MySQL's own escaping escapes,
// each after a backslash.
func appendQuoted[T string | []byte](b []byte, s T, noBackslash bool) []byte {
	b = append(b, '\'')
	start := 0
	if noBackslash {
		for i := 0; i < len(s); i++ {
			if s[i] == '\'' {
				b = append(b, s[start:i+1]...)
				b = append(b, '\'')
				start = i + 1
			}
		}
	} else {
		for i := 0; i < len(s); i++ {
			if e := escapes[s[i]]; e != 0 {
				b = append(b, s[start:i]...)
				b = append(b, '\\', e)
				start = i + 1
			}
		}
	}
	b = append(b, s[start:]...)
	return append(b, '\'')
}

// escapes are the letters MySQL's own escaping writes after a backslash,
// by the byte they stand for; 0 for the bytes written as they are.
var escapes = [256]byte{0: '0', '\n': 'n', '\r': 'r', 0x1a: 'Z', '\'': '\'', '"': '"', '\\': '\\'}

// appendDateTimeText writes d as text, as much of it as appendDateTime
// would send: the date alone, the seconds too, or the microseconds.
func appendDateTimeText(b []byte, d DateTime) []byte {
	b = append(b, '\'')
	b = appendDigits(b, int64(d.Year), 4)
	b = appendDigits(append(b, '-'), int64(d.Month), 2)
	b = appendDigits(append(b, '-'), int64(d.Day), 2)
	if d.Hour != 0 || d.Minute != 0 || d.Second != 0 || d.Microsecond != 0 {
		b = appendDigits(append(b, ' '), int64(d.Hour), 2)
		b = appendDigits(append(b, ':'), int64(d.Minute), 2)
		b = appendDigits(append(b, ':'), int64(d.Second), 2)
		if d.Microsecond != 0 {
			b = appendDigits(append(b, '.'), int64(d.Microsecond), 6)
		}
	}
	return append(b, '\'')
}

// appendTimeText writes d as TIME text, [-]HH:MM:SS[.ffffff], its hours
// past 24 as they are (appendTime's days and hours together).
func appendTimeText(b []byte, d time.Duration) []byte {
	b = append(b, '\'')
	if d < 0 {
		b, d = append(b, '-'), -d
	}
	us := int64(d % time.Second / time.Microsecond)
	s := int64(d / time.Second)
	b = appendDigits(b, s/3600, 2)
	b = appendDigits(append(b, ':'), s/60%60, 2)
	b = appendDigits(append(b, ':'), s%60, 2)
	if us != 0 {
		b = appendDigits(append(b, '.'), us, 6)
	}
	return append(b, '\'')
}

// appendDigits writes n, which is not negative, in at least width digits,
// zeros first.
func appendDigits(b []byte, n int64, width int) []byte {
	var tmp [20]byte
	s := strconv.AppendInt(tmp[:0], n, 10)
	for i := len(s); i < width; i++ {
		b = append(b, '0')
	}
	return append(b, s...)
}
