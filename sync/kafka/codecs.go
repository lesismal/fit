package synckafka

import (
	"bytes"
	"encoding/base64"
	"strconv"
	"strings"
	"time"

	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
)

func init() {
	module.Register(module.Info{ID: fitsync.Codecs + ".raw", New: func() any { return new(Raw) },
		Doc: "A message's value as it is: a message record whose after holds it, as bytes, in the field field names (value). Encoded, a record is that field's bytes or text, or its canonical JSON when it has none, so a Kafka topic copied through raw is copied byte for byte."})
}

// KeyDecoder is a codec that also reads a message's key, as Debezium's key
// messages hold the row's key columns: the source kafka gives a record the
// key it decodes, rather than the key's text, when the record has none.
type KeyDecoder interface {
	DecodeKey(key []byte) (fitsync.Value, error)
}

// Raw is the codec raw.
type Raw struct {
	Field string `json:"field,omitempty" default:"\"value\"" doc:"The field of after that holds the value. value when left out."`
}

func (c *Raw) field() string {
	if c.Field == "" {
		return "value"
	}
	return c.Field
}

// Decode makes a message record of the value.
func (c *Raw) Decode(data []byte, emit func(*fitsync.Record)) error {
	emit(&fitsync.Record{Op: fitsync.OpMessage, After: fitsync.Map(fitsync.Field{Name: c.field(), Value: fitsync.Bytes(data)})})
	return nil
}

// Encode appends each record's field, or its canonical JSON, one after the
// other: a sink encodes one record a message.
func (c *Raw) Encode(b []byte, recs []*fitsync.Record) ([]byte, error) {
	for _, r := range recs {
		row := r.After
		if row.IsNull() {
			row = r.Before
		}
		v, ok := row.Get(c.field())
		switch {
		case ok && (v.Kind() == fitsync.KindBytes || v.Kind() == fitsync.KindString):
			b = append(b, v.Text()...)
		case ok:
			b = append(b, fitsync.TextOf(v)...)
		default:
			var err error
			if b, err = r.AppendJSON(b); err != nil {
				return b, err
			}
		}
	}
	return b, nil
}

// plainJSON writes v as plain JSON, as the json codec writes a row.
func plainJSON(v fitsync.Value) ([]byte, error) {
	var c fitsync.JSONCodec
	b, err := c.Encode(nil, []*fitsync.Record{{After: v}})
	return bytes.TrimSuffix(b, []byte{'\n'}), err
}

// MySQLType is a column's type as change formats write it: "int",
// "bigint unsigned", "decimal(10,2)", "varbinary".
type MySQLType struct {
	// Base is the type's name, lower case, without its length or
	// attributes.
	Base     string
	Unsigned bool
}

// ParseMySQLType reads a type as Canal, TiCDC and information_schema's
// column_type write it.
func ParseMySQLType(s string) MySQLType {
	s = strings.ToLower(strings.TrimSpace(s))
	t := MySQLType{Unsigned: strings.Contains(s, "unsigned")}
	if i := strings.IndexAny(s, "( "); i >= 0 {
		s = s[:i]
	}
	t.Base = s
	return t
}

// IsBinary reports whether the type holds bytes rather than text.
func (t MySQLType) IsBinary() bool {
	switch t.Base {
	case "binary", "varbinary", "blob", "tinyblob", "mediumblob", "longblob", "geometry":
		return true
	}
	return false
}

// MySQLText makes a column's value of its text, as MySQL's text protocol,
// Canal and TiCDC write it, by its type: integers as ints (uints when
// unsigned, and BIT), YEAR as an int, FLOAT and DOUBLE as floats, DECIMAL
// as a decimal, DATE as a date, DATETIME and TIMESTAMP as times in loc,
// JSON as JSON, binary types as bytes of raw, and the rest (strings, TIME,
// ENUM and SET as given) as strings. Text a type does not parse stays a
// string.
func MySQLText(t MySQLType, s string, raw []byte, loc *time.Location) fitsync.Value {
	switch t.Base {
	case "tinyint", "smallint", "mediumint", "int", "integer", "bigint":
		if t.Unsigned {
			if u, err := strconv.ParseUint(s, 10, 64); err == nil {
				return fitsync.Uint(u)
			}
		} else if i, err := strconv.ParseInt(s, 10, 64); err == nil {
			return fitsync.Int(i)
		}
	case "year":
		if i, err := strconv.ParseInt(s, 10, 64); err == nil {
			return fitsync.Int(i)
		}
	case "bit":
		if u, err := strconv.ParseUint(s, 10, 64); err == nil {
			return fitsync.Uint(u)
		}
	case "float", "double", "real":
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return fitsync.Float(f)
		}
	case "decimal", "numeric":
		if _, err := strconv.ParseFloat(s, 64); err == nil {
			return fitsync.Decimal(s)
		}
	case "date":
		if d, err := time.Parse("2006-01-02", s); err == nil {
			return fitsync.Date(d.Year(), d.Month(), d.Day())
		}
	case "datetime", "timestamp":
		if loc == nil {
			loc = time.UTC
		}
		if tm, err := time.ParseInLocation("2006-01-02 15:04:05.999999999", s, loc); err == nil {
			return fitsync.Time(tm)
		}
	case "json":
		return fitsync.JSON([]byte(s))
	}
	if t.IsBinary() {
		return fitsync.Bytes(raw)
	}
	return fitsync.String(s)
}

// latin1 turns text whose runes are bytes, as Canal's JSON writes binary
// columns (ISO-8859-1), back into the bytes.
func latin1(s string) []byte {
	b := make([]byte, 0, len(s))
	for _, r := range s {
		b = append(b, byte(r))
	}
	return b
}

// toLatin1 writes bytes as text whose runes are the bytes.
func toLatin1(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		sb.WriteRune(rune(c))
	}
	return sb.String()
}

// unbase64 decodes standard base64, or returns the text's bytes.
func unbase64(s string) []byte {
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b
	}
	return []byte(s)
}

// tsoTime is the physical part of a TiDB timestamp (TSO), as a time.
func tsoTime(ts uint64) time.Time { return time.UnixMilli(int64(ts >> 18)).UTC() }

// keyOf makes a key of a row's columns named in cols; null when cols is
// empty or the row lacks one.
func keyOf(row fitsync.Value, cols []string) fitsync.Value {
	if len(cols) == 0 {
		return fitsync.Null()
	}
	fs := make([]fitsync.Field, 0, len(cols))
	for _, c := range cols {
		v, ok := row.Get(c)
		if !ok {
			return fitsync.Null()
		}
		fs = append(fs, fitsync.Field{Name: c, Value: v})
	}
	return fitsync.Map(fs...)
}

// splitNamespace splits db.table at its first dot.
func splitNamespace(ns string) (string, string) {
	db, table, ok := strings.Cut(ns, ".")
	if !ok {
		return "", ns
	}
	return db, table
}
