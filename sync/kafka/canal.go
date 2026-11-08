package synckafka

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
)

func init() {
	module.Register(module.Info{ID: fitsync.Codecs + ".canal_json", New: func() any { return new(CanalJSON) },
		Doc: "Canal's JSON, as Canal and TiCDC (protocol canal-json) write it: each row of a message a record of the table db.table, an INSERT an insert, an UPDATE an update whose before is the row with old's values, a DELETE a delete, keyed by pkNames, values typed by mysqlType; a DDL a ddl record holding its sql; TiCDC's watermarks nothing. With TiCDC's enable-tidb-extension, _tidb.commitTs is the records' transaction and commit time. Encoded, a record is a message of one row in the same form."})
}

// CanalJSON is the codec canal_json.
type CanalJSON struct {
	TimeZone string `json:"time_zone,omitempty" default:"\"UTC\"" doc:"The zone DATETIME and TIMESTAMP values are written in: TiCDC's changefeed's time-zone, the server's for Canal. UTC when left out."`

	loc *time.Location
}

// Provision loads the zone.
func (c *CanalJSON) Provision(ctx *module.Context) error {
	loc, err := loadZone(c.TimeZone)
	if err != nil {
		return ctx.Errorf("time_zone", "%v", err)
	}
	c.loc = loc
	return nil
}

func loadZone(name string) (*time.Location, error) {
	if name == "" {
		return time.UTC, nil
	}
	return time.LoadLocation(name)
}

// canalMessage is a Canal JSON message.
type canalMessage struct {
	Database  string            `json:"database"`
	Table     string            `json:"table"`
	PKNames   []string          `json:"pkNames"`
	IsDDL     bool              `json:"isDdl"`
	Type      string            `json:"type"`
	ES        int64             `json:"es"`
	SQL       string            `json:"sql"`
	MySQLType map[string]string `json:"mysqlType"`
	Data      json.RawMessage   `json:"data"`
	Old       json.RawMessage   `json:"old"`
	TiDB      *struct {
		CommitTs uint64 `json:"commitTs"`
	} `json:"_tidb"`
}

// Decode makes a message's records.
func (c *CanalJSON) Decode(data []byte, emit func(*fitsync.Record)) error {
	var m canalMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	base := fitsync.Record{Namespace: m.Database + "." + m.Table}
	if m.ES > 0 {
		base.CommitTime = time.UnixMilli(m.ES).UTC()
	}
	if m.TiDB != nil && m.TiDB.CommitTs > 0 {
		base.CommitTime = tsoTime(m.TiDB.CommitTs)
		base.Tx.ID = strconv.FormatUint(m.TiDB.CommitTs, 10)
	}
	if m.IsDDL {
		r := base
		r.Op = fitsync.OpDDL
		r.After = fitsync.Map(fitsync.Field{Name: "sql", Value: fitsync.String(m.SQL)}, fitsync.Field{Name: "type", Value: fitsync.String(m.Type)})
		emit(&r)
		return nil
	}
	var op fitsync.Op
	switch m.Type {
	case "INSERT":
		op = fitsync.OpInsert
	case "UPDATE":
		op = fitsync.OpUpdate
	case "DELETE":
		op = fitsync.OpDelete
	case "TIDB_WATERMARK":
		return nil
	default:
		return fmt.Errorf("canal_json: a message of type %q", m.Type)
	}
	rows, err := c.rows(m.Data, m.MySQLType)
	if err != nil {
		return err
	}
	olds, err := c.rows(m.Old, m.MySQLType)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return errors.New("canal_json: a row change without data")
	}
	for i, row := range rows {
		r := base
		r.Op = op
		switch op {
		case fitsync.OpDelete:
			r.Before = row
			r.Key = keyOf(row, m.PKNames)
		case fitsync.OpUpdate:
			r.After = row
			if i < len(olds) {
				r.Before = overlay(row, olds[i])
			}
			r.Key = keyOf(row, m.PKNames)
		default:
			r.After = row
			r.Key = keyOf(row, m.PKNames)
		}
		emit(&r)
	}
	return nil
}

// overlay returns row with old's fields in place of its own.
func overlay(row, old fitsync.Value) fitsync.Value {
	fs := make([]fitsync.Field, len(row.Fields()))
	copy(fs, row.Fields())
	for i, f := range fs {
		if v, ok := old.Get(f.Name); ok {
			fs[i].Value = v
		}
	}
	return fitsync.Map(fs...)
}

// rows reads an array of rows, each column's text typed by types.
func (c *CanalJSON) rows(raw json.RawMessage, types map[string]string) ([]fitsync.Value, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	v, err := fitsync.ParsePlain(raw)
	if err != nil {
		return nil, fmt.Errorf("canal_json: %w", err)
	}
	if v.Kind() != fitsync.KindArray {
		return nil, errors.New("canal_json: rows that are not an array")
	}
	loc := c.loc
	if loc == nil {
		loc = time.UTC
	}
	out := make([]fitsync.Value, 0, len(v.Elems()))
	for _, row := range v.Elems() {
		if row.Kind() != fitsync.KindMap {
			return nil, errors.New("canal_json: a row that is not an object")
		}
		fs := make([]fitsync.Field, len(row.Fields()))
		for i, f := range row.Fields() {
			fs[i].Name, fs[i].Value = f.Name, f.Value
			if f.Value.Kind() != fitsync.KindString {
				continue
			}
			t := ParseMySQLType(types[f.Name])
			s := f.Value.Text()
			fs[i].Value = MySQLText(t, s, latin1(s), loc)
		}
		out = append(out, fitsync.Map(fs...))
	}
	return out, nil
}

// Encode appends each record as a Canal JSON message of one row, the
// records of no row or DDL left out.
func (c *CanalJSON) Encode(b []byte, recs []*fitsync.Record) ([]byte, error) {
	loc := c.loc
	if loc == nil {
		loc = time.UTC
	}
	for _, r := range recs {
		db, table := splitNamespace(r.Namespace)
		m := map[string]any{"id": 0, "database": db, "table": table, "es": r.CommitTime.UnixMilli(), "ts": r.CommitTime.UnixMilli(),
			"sql": "", "old": nil, "sqlType": nil, "pkNames": nil}
		if r.CommitTime.IsZero() {
			m["es"], m["ts"] = 0, 0
		}
		if r.Op == fitsync.OpDDL {
			sql, _ := r.After.Get("sql")
			m["isDdl"], m["type"], m["sql"], m["data"], m["mysqlType"] = true, "QUERY", fitsync.TextOf(sql), nil, nil
			b = appendJSON(b, m)
			continue
		}
		m["isDdl"] = false
		row := r.After
		switch r.Op {
		case fitsync.OpDelete:
			m["type"], row = "DELETE", r.Before
			if row.IsNull() {
				row = r.Key
			}
		case fitsync.OpUpdate:
			m["type"] = "UPDATE"
		default:
			m["type"] = "INSERT"
		}
		if row.Kind() != fitsync.KindMap {
			return b, fmt.Errorf("canal_json: a %s record without a row", r.Op)
		}
		types := map[string]string{}
		m["data"] = []json.RawMessage{canalRow(row, types, loc)}
		if r.Op == fitsync.OpUpdate && r.Before.Kind() == fitsync.KindMap {
			m["old"] = []json.RawMessage{canalRow(r.Before, types, loc)}
		}
		m["mysqlType"] = types
		var pk []string
		for _, f := range r.Key.Fields() {
			pk = append(pk, f.Name)
		}
		if pk != nil {
			m["pkNames"] = pk
		}
		b = appendJSON(b, m)
	}
	return b, nil
}

func appendJSON(b []byte, v any) []byte {
	j, _ := json.Marshal(v) // maps of strings, numbers and nulls
	return append(b, j...)
}

// canalRow writes a row's values as Canal's text, in the row's order,
// noting each column's type in types.
func canalRow(row fitsync.Value, types map[string]string, loc *time.Location) json.RawMessage {
	out := []byte{'{'}
	for i, f := range row.Fields() {
		v := f.Value
		var t string
		var s any = fitsync.TextOf(v)
		switch v.Kind() {
		case fitsync.KindNull:
			s = nil
		case fitsync.KindBool:
			t = "tinyint"
			s = "0"
			if v.AsBool() {
				s = "1"
			}
		case fitsync.KindInt:
			t = "bigint"
		case fitsync.KindUint:
			t = "bigint unsigned"
		case fitsync.KindFloat:
			t = "double"
		case fitsync.KindDecimal:
			t = "decimal"
		case fitsync.KindBytes:
			t, s = "blob", toLatin1([]byte(v.Text()))
		case fitsync.KindTime:
			t, s = "datetime", strings.TrimSuffix(v.AsTime().In(loc).Format("2006-01-02 15:04:05.000000"), ".000000")
		case fitsync.KindDate:
			t = "date"
		case fitsync.KindJSON, fitsync.KindArray, fitsync.KindMap:
			t = "json"
			if v.Kind() != fitsync.KindJSON {
				j, _ := plainJSON(v)
				s = string(j)
			}
		default:
			t = "varchar"
		}
		if t != "" {
			types[f.Name] = t
		}
		if i > 0 {
			out = append(out, ',')
		}
		out = appendJSON(out, f.Name)
		out = append(out, ':')
		out = appendJSON(out, s)
	}
	return append(out, '}')
}
