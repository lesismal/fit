package synckafka

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"sync"
	"time"

	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
)

func init() {
	module.Register(module.Info{ID: fitsync.Codecs + ".debezium", New: func() any { return new(Debezium) },
		Doc: "Debezium's JSON change events, with or without the schema envelope, as Debezium's connectors and TiCDC (protocol debezium) write them: op c and r an insert, u an update, d a delete, of source's db.table (schema.table when source has a schema), committed at source.ts_ms; with a schema, values are typed by it (bytes from base64, Kafka Connect's Decimal, Debezium's dates, times and timestamps, JSON), without one as plain JSON. The message's key, Debezium's key event, is the record's key. Encoded, a record is an event without a schema."})
}

// Debezium is the codec debezium.
type Debezium struct{}

type dbzField struct {
	Field      string            `json:"field"`
	Type       string            `json:"type"`
	Name       string            `json:"name"`
	Parameters map[string]string `json:"parameters"`
	Fields     []dbzField        `json:"fields"`
}

type dbzPayload struct {
	Before json.RawMessage `json:"before"`
	After  json.RawMessage `json:"after"`
	Op     string          `json:"op"`
	TsMs   int64           `json:"ts_ms"`
	Source struct {
		DB       string `json:"db"`
		Schema   string `json:"schema"`
		Table    string `json:"table"`
		TsMs     int64  `json:"ts_ms"`
		CommitTs uint64 `json:"commit_ts"`
	} `json:"source"`
	Transaction *struct {
		ID string `json:"id"`
	} `json:"transaction"`
}

// unwrap returns the payload and its schema of a message with the schema
// envelope (an object of schema and payload alone), or the message itself
// without one.
func unwrap(data []byte) (json.RawMessage, *dbzField, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, nil, err
	}
	p, ok := m["payload"]
	if _, hasSchema := m["schema"]; !ok || len(m) > 2 || len(m) == 2 && !hasSchema {
		return data, nil, nil
	}
	var s *dbzField
	if raw := m["schema"]; len(raw) > 0 && string(raw) != "null" {
		s = new(dbzField)
		if err := json.Unmarshal(raw, s); err != nil {
			return nil, nil, err
		}
	}
	return p, s, nil
}

// DecodeKey reads Debezium's key event: the key's columns.
func (c *Debezium) DecodeKey(key []byte) (fitsync.Value, error) {
	payload, schema, err := unwrap(key)
	if err != nil {
		return fitsync.Null(), err
	}
	return typed(payload, schema)
}

// Decode makes an event's record.
func (c *Debezium) Decode(data []byte, emit func(*fitsync.Record)) error {
	payload, schema, err := unwrap(data)
	if err != nil {
		return err
	}
	var p dbzPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return err
	}
	r := &fitsync.Record{Namespace: p.Source.DB + "." + p.Source.Table}
	if p.Source.Schema != "" {
		r.Namespace = p.Source.Schema + "." + p.Source.Table
	}
	switch p.Op {
	case "c", "r":
		r.Op = fitsync.OpInsert
	case "u":
		r.Op = fitsync.OpUpdate
	case "d":
		r.Op = fitsync.OpDelete
	default:
		return fmt.Errorf("debezium: an event of op %q", p.Op)
	}
	var before, after *dbzField
	if schema != nil {
		for i := range schema.Fields {
			switch schema.Fields[i].Field {
			case "before":
				before = &schema.Fields[i]
			case "after":
				after = &schema.Fields[i]
			}
		}
	}
	if r.Before, err = typed(p.Before, before); err != nil {
		return err
	}
	if r.After, err = typed(p.After, after); err != nil {
		return err
	}
	ts := p.Source.TsMs
	if ts == 0 {
		ts = p.TsMs
	}
	if ts > 0 {
		r.CommitTime = time.UnixMilli(ts).UTC()
	}
	switch {
	case p.Source.CommitTs > 0:
		r.Tx.ID = strconv.FormatUint(p.Source.CommitTs, 10)
	case p.Transaction != nil:
		r.Tx.ID = p.Transaction.ID
	}
	emit(r)
	return nil
}

// typed reads a JSON value as plain JSON, then types it by its schema.
func typed(raw json.RawMessage, schema *dbzField) (fitsync.Value, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return fitsync.Null(), nil
	}
	v, err := fitsync.ParsePlain(raw)
	if err != nil {
		return v, fmt.Errorf("debezium: %w", err)
	}
	if schema == nil {
		return v, nil
	}
	return byField(v, schema), nil
}

// byField types v by the schema's field f.
func byField(v fitsync.Value, f *dbzField) fitsync.Value {
	if v.IsNull() {
		return v
	}
	if v.Kind() == fitsync.KindMap && len(f.Fields) > 0 {
		fs := make([]fitsync.Field, len(v.Fields()))
		for i, x := range v.Fields() {
			fs[i] = x
			for j := range f.Fields {
				if f.Fields[j].Field == x.Name {
					fs[i].Value = byField(x.Value, &f.Fields[j])
					break
				}
			}
		}
		return fitsync.Map(fs...)
	}
	n := v.AsInt()
	switch f.Name {
	case "io.debezium.time.Date":
		d := time.Unix(n*86400, 0).UTC()
		return fitsync.Date(d.Year(), d.Month(), d.Day())
	case "io.debezium.time.Timestamp", "org.apache.kafka.connect.data.Timestamp":
		return fitsync.Time(time.UnixMilli(n).UTC())
	case "io.debezium.time.MicroTimestamp":
		return fitsync.Time(time.UnixMicro(n).UTC())
	case "io.debezium.time.NanoTimestamp":
		return fitsync.Time(time.Unix(0, n).UTC())
	case "io.debezium.time.ZonedTimestamp":
		if t, err := time.Parse(time.RFC3339Nano, v.Text()); err == nil {
			return fitsync.Time(t)
		}
		return v
	case "io.debezium.time.Time", "org.apache.kafka.connect.data.Time":
		return clock(time.Duration(n) * time.Millisecond)
	case "io.debezium.time.MicroTime":
		return clock(time.Duration(n) * time.Microsecond)
	case "io.debezium.time.NanoTime":
		return clock(time.Duration(n))
	case "io.debezium.data.Json":
		return fitsync.JSON([]byte(v.Text()))
	case "org.apache.kafka.connect.data.Decimal":
		b, err := base64.StdEncoding.DecodeString(v.Text())
		if err != nil {
			return v
		}
		scale, _ := strconv.Atoi(f.Parameters["scale"])
		return fitsync.Decimal(decimalOf(b, scale))
	case "io.debezium.data.Bits":
		b, err := base64.StdEncoding.DecodeString(v.Text())
		if err != nil || len(b) > 8 {
			return v
		}
		var u uint64
		for i := len(b) - 1; i >= 0; i-- {
			u = u<<8 | uint64(b[i])
		}
		return fitsync.Uint(u)
	}
	if f.Type == "bytes" {
		if b, err := base64.StdEncoding.DecodeString(v.Text()); err == nil {
			return fitsync.Bytes(b)
		}
	}
	return v
}

// clock writes a time of day, or a MySQL TIME, as [-]HH:MM:SS[.ffffff].
func clock(d time.Duration) fitsync.Value {
	sign := ""
	if d < 0 {
		sign, d = "-", -d
	}
	us := int64(d / time.Microsecond)
	s := fmt.Sprintf("%s%02d:%02d:%02d", sign, us/3600e6, us/60e6%60, us/1e6%60)
	if f := us % 1e6; f != 0 {
		s += fmt.Sprintf(".%06d", f)
	}
	return fitsync.String(s)
}

// decimalOf writes the big-endian two's complement unscaled value b at
// scale as a decimal's text.
func decimalOf(b []byte, scale int) string {
	n := new(big.Int).SetBytes(b)
	if len(b) > 0 && b[0]&0x80 != 0 {
		n.Sub(n, new(big.Int).Lsh(big.NewInt(1), uint(len(b))*8))
	}
	s := n.String()
	if scale <= 0 {
		return s
	}
	neg := s[0] == '-'
	if neg {
		s = s[1:]
	}
	for len(s) <= scale {
		s = "0" + s
	}
	s = s[:len(s)-scale] + "." + s[len(s)-scale:]
	if neg {
		s = "-" + s
	}
	return s
}

// Encode appends each record as a Debezium event without a schema, its
// rows as plain JSON: {"after":...,"before":...,"op":...,"source":{...},
// "ts_ms":...}, the members in the order encoding/json writes a map's. An
// event is written by hand into a pooled buffer, then appended to b, byte
// for byte what encodeMaps writes; a record the hand-written path is unsure
// of (a JSON document that does not marshal, a decimal that is not a JSON
// number, a float JSON cannot hold, an operation with no event) is written
// by encodeMaps itself, errors and all.
func (c *Debezium) Encode(b []byte, recs []*fitsync.Record) ([]byte, error) {
	sp := scratch.Get().(*[]byte)
	defer scratch.Put(sp)
	for _, r := range recs {
		ev, ok := appendDebezium((*sp)[:0], r)
		if cap(ev) <= 64<<10 {
			*sp = ev[:0]
		}
		if ok {
			b = append(b, ev...)
			continue
		}
		var err error
		if b, err = encodeMaps(b, r); err != nil {
			return b, err
		}
	}
	return b, nil
}

// scratch holds the buffers Encode writes an event into.
var scratch = sync.Pool{New: func() any { b := make([]byte, 0, 512); return &b }}

// appendDebezium appends r's event, or reports it could not.
func appendDebezium(b []byte, r *fitsync.Record) ([]byte, bool) {
	var op string
	switch r.Op {
	case fitsync.OpInsert, fitsync.OpMessage:
		op = "c"
	case fitsync.OpUpdate:
		op = "u"
	case fitsync.OpDelete:
		op = "d"
	default:
		return b, false
	}
	var ok bool
	b = append(b, `{"after":`...)
	if b, ok = appendPlainFast(b, r.After); !ok {
		return b, false
	}
	b = append(b, `,"before":`...)
	if b, ok = appendPlainFast(b, r.Before); !ok {
		return b, false
	}
	b = append(b, `,"op":"`...)
	b = append(b, op...)
	db, table := splitNamespace(r.Namespace)
	ms := r.CommitTime.UnixMilli()
	if r.CommitTime.IsZero() {
		ms = 0
	}
	b = append(b, `","source":{"connector":"fit","db":`...)
	b = fitsync.AppendJSONString(b, db)
	b = append(b, `,"table":`...)
	b = fitsync.AppendJSONString(b, table)
	b = append(b, `,"ts_ms":`...)
	b = strconv.AppendInt(b, ms, 10)
	b = append(b, `},"ts_ms":`...)
	b = strconv.AppendInt(b, ms, 10)
	return append(b, '}'), true
}

// appendPlainFast appends v as plain JSON as fitsync.AppendPlain writes it
// and encoding/json then rewrites it as a json.RawMessage, or reports it
// could not.
func appendPlainFast(b []byte, v fitsync.Value) ([]byte, bool) {
	switch v.Kind() {
	case fitsync.KindNull:
		return append(b, "null"...), true
	case fitsync.KindBool:
		return strconv.AppendBool(b, v.AsBool()), true
	case fitsync.KindInt:
		return strconv.AppendInt(b, v.AsInt(), 10), true
	case fitsync.KindUint:
		return strconv.AppendUint(b, v.AsUint(), 10), true
	case fitsync.KindFloat:
		f := v.AsFloat()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return b, false
		}
		return strconv.AppendFloat(b, f, 'g', -1, 64), true
	case fitsync.KindDecimal:
		if !jsonNumber(v.Text()) {
			return b, false
		}
		return append(b, v.Text()...), true
	case fitsync.KindString:
		return fitsync.AppendJSONString(b, v.Text()), true
	case fitsync.KindBytes:
		b = append(b, '"')
		b = base64.StdEncoding.AppendEncode(b, []byte(v.Text()))
		return append(b, '"'), true
	case fitsync.KindTime:
		b = append(b, '"')
		b = v.AsTime().AppendFormat(b, time.RFC3339Nano)
		return append(b, '"'), true
	case fitsync.KindDate:
		b = append(b, '"')
		b = v.AsTime().AppendFormat(b, time.DateOnly)
		return append(b, '"'), true
	case fitsync.KindArray:
		b = append(b, '[')
		for i, e := range v.Elems() {
			if i > 0 {
				b = append(b, ',')
			}
			var ok bool
			if b, ok = appendPlainFast(b, e); !ok {
				return b, false
			}
		}
		return append(b, ']'), true
	case fitsync.KindMap:
		b = append(b, '{')
		for i, f := range v.Fields() {
			if i > 0 {
				b = append(b, ',')
			}
			b = append(fitsync.AppendJSONString(b, f.Name), ':')
			var ok bool
			if b, ok = appendPlainFast(b, f.Value); !ok {
				return b, false
			}
		}
		return append(b, '}'), true
	}
	// A JSON document, whose strings may hold what encoding/json escapes
	// on its way out (<, >, &, U+2028, U+2029), goes through encoding/json
	// as encodeMaps sends it; AppendPlain fails a kind it does not know.
	j, err := fitsync.AppendPlain(nil, v)
	if err == nil {
		j, err = json.Marshal(json.RawMessage(j))
	}
	if err != nil {
		return b, false
	}
	return append(b, j...), true
}

// jsonNumber reports whether s is a number as JSON's grammar has one.
func jsonNumber(s string) bool {
	i := 0
	digits := func() bool {
		j := i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		return i > j
	}
	if i < len(s) && s[i] == '-' {
		i++
	}
	if i < len(s) && s[i] == '0' {
		i++
	} else if i == len(s) || s[i] < '1' || s[i] > '9' || !digits() {
		return false
	}
	if i < len(s) && s[i] == '.' {
		i++
		if !digits() {
			return false
		}
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			i++
		}
		if !digits() {
			return false
		}
	}
	return i == len(s)
}

// encodeMaps appends r's event as the codec first wrote it, as maps through
// encoding/json: Encode falls back to it, and the tests hold the two to the
// same bytes. A record whose rows do not marshal appends nothing, as
// before.
func encodeMaps(b []byte, r *fitsync.Record) ([]byte, error) {
	var op string
	switch r.Op {
	case fitsync.OpInsert, fitsync.OpMessage:
		op = "c"
	case fitsync.OpUpdate:
		op = "u"
	case fitsync.OpDelete:
		op = "d"
	default:
		return b, fmt.Errorf("debezium: a %s record has no event", r.Op)
	}
	before, err := plainJSON(r.Before)
	if err != nil {
		return b, err
	}
	after, err := plainJSON(r.After)
	if err != nil {
		return b, err
	}
	if r.Before.IsNull() {
		before = []byte("null")
	}
	if r.After.IsNull() {
		after = []byte("null")
	}
	db, table := splitNamespace(r.Namespace)
	ms := r.CommitTime.UnixMilli()
	if r.CommitTime.IsZero() {
		ms = 0
	}
	src, _ := json.Marshal(map[string]any{"connector": "fit", "db": db, "table": table, "ts_ms": ms})
	ev, _ := json.Marshal(map[string]any{"before": json.RawMessage(before), "after": json.RawMessage(after), "op": op, "ts_ms": ms,
		"source": json.RawMessage(src)})
	return append(b, ev...), nil
}
