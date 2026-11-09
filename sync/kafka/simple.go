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
	module.Register(module.Info{ID: fitsync.Codecs + ".ticdc_simple", New: func() any { return new(TiCDCSimple) },
		Doc: "TiCDC's simple protocol, in JSON: a table's schema from its BOOTSTRAP and DDL messages, kept, types each row's values (binary columns from base64, ENUM and SET as their labels, TIMESTAMP in its zone) and names its key, the primary key; INSERT, UPDATE and DELETE become records of db.table whose transaction and commit time are the commitTs; a DDL a ddl record of its sql; watermarks nothing. A row of a table whose schema has not come yet (a source started after its bootstrap) keeps its values as text and has no key, until the next bootstrap (send-bootstrap-interval-in-sec), unless the tidb source gave the schema. It only decodes."})
}

// TiCDCSimple is the codec ticdc_simple.
type TiCDCSimple struct {
	TimeZone string `json:"time_zone,omitempty" default:"\"UTC\"" doc:"The zone DATETIME values are written in, the changefeed's time-zone; TIMESTAMP values carry their own. UTC when left out."`

	loc    *time.Location
	tables map[string]*SimpleTable
}

// SimpleTable is a table's schema, as the simple protocol's bootstrap
// messages give it, or as a source reads it from the database.
type SimpleTable struct {
	Columns []SimpleColumn
	// Key is the primary key's columns, or a unique key's of columns that
	// are not null.
	Key []string
}

// SimpleColumn is a column of a SimpleTable.
type SimpleColumn struct {
	Name string
	Type MySQLType
	// Elements are an ENUM's or a SET's.
	Elements []string
}

// Provision loads the zone.
func (c *TiCDCSimple) Provision(ctx *module.Context) error {
	loc, err := loadZone(c.TimeZone)
	if err != nil {
		return ctx.Errorf("time_zone", "%v", err)
	}
	c.loc = loc
	return nil
}

// SetTable gives the schema of the table namespace (db.table), until a
// message gives another.
func (c *TiCDCSimple) SetTable(namespace string, t *SimpleTable) {
	if c.tables == nil {
		c.tables = map[string]*SimpleTable{}
	}
	c.tables[namespace] = t
}

type simpleMessage struct {
	Type        string          `json:"type"`
	Database    string          `json:"database"`
	Table       string          `json:"table"`
	CommitTs    uint64          `json:"commitTs"`
	SQL         string          `json:"sql"`
	Data        json.RawMessage `json:"data"`
	Old         json.RawMessage `json:"old"`
	TableSchema *struct {
		Schema  string `json:"schema"`
		Table   string `json:"table"`
		Columns []struct {
			Name     string `json:"name"`
			DataType struct {
				MySQLType string   `json:"mysqlType"`
				Unsigned  bool     `json:"unsigned"`
				Elements  []string `json:"elements"`
			} `json:"dataType"`
		} `json:"columns"`
		Indexes []struct {
			Unique   bool     `json:"unique"`
			Primary  bool     `json:"primary"`
			Nullable bool     `json:"nullable"`
			Columns  []string `json:"columns"`
		} `json:"indexes"`
	} `json:"tableSchema"`
}

// Decode makes a message's records, keeping the schemas it gives.
func (c *TiCDCSimple) Decode(data []byte, emit func(*fitsync.Record)) error {
	var m simpleMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	if ts := m.TableSchema; ts != nil {
		t := &SimpleTable{}
		for _, col := range ts.Columns {
			typ := ParseMySQLType(col.DataType.MySQLType)
			typ.Unsigned = typ.Unsigned || col.DataType.Unsigned
			t.Columns = append(t.Columns, SimpleColumn{Name: col.Name, Type: typ, Elements: col.DataType.Elements})
		}
		for _, ix := range ts.Indexes {
			if ix.Primary || ix.Unique && !ix.Nullable && t.Key == nil {
				t.Key = ix.Columns
			}
		}
		c.SetTable(ts.Schema+"."+ts.Table, t)
	}
	r := &fitsync.Record{Namespace: m.Database + "." + m.Table}
	if m.CommitTs > 0 {
		r.CommitTime, r.Tx.ID = tsoTime(m.CommitTs), strconv.FormatUint(m.CommitTs, 10)
	}
	switch m.Type {
	case "BOOTSTRAP", "WATERMARK":
		return nil
	case "INSERT":
		r.Op = fitsync.OpInsert
	case "UPDATE":
		r.Op = fitsync.OpUpdate
	case "DELETE":
		r.Op = fitsync.OpDelete
	default:
		if m.SQL == "" {
			return fmt.Errorf("ticdc_simple: a message of type %q", m.Type)
		}
		if ts := m.TableSchema; ts != nil && m.Database == "" {
			r.Namespace = ts.Schema + "." + ts.Table
		}
		r.Op = fitsync.OpDDL
		r.After = fitsync.Map(fitsync.Field{Name: "sql", Value: fitsync.String(m.SQL)}, fitsync.Field{Name: "type", Value: fitsync.String(m.Type)})
		emit(r)
		return nil
	}
	t := c.tables[r.Namespace]
	var err error
	if r.After, err = c.row(m.Data, t); err != nil {
		return err
	}
	if r.Before, err = c.row(m.Old, t); err != nil {
		return err
	}
	row := r.After
	if r.Op == fitsync.OpDelete {
		row = r.Before
	}
	if row.IsNull() {
		return fmt.Errorf("ticdc_simple: a %s without its row", m.Type)
	}
	if t != nil {
		r.Key = keyOf(row, t.Key)
	}
	emit(r)
	return nil
}

// row reads a row, typed and ordered by t when there is one.
func (c *TiCDCSimple) row(raw json.RawMessage, t *SimpleTable) (fitsync.Value, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return fitsync.Null(), nil
	}
	v, err := fitsync.ParsePlain(raw)
	if err != nil {
		return v, fmt.Errorf("ticdc_simple: %w", err)
	}
	if v.Kind() != fitsync.KindMap {
		return v, errors.New("ticdc_simple: a row that is not an object")
	}
	if t == nil {
		fs := make([]fitsync.Field, len(v.Fields()))
		for i, f := range v.Fields() {
			fs[i] = fitsync.Field{Name: f.Name, Value: c.value(nil, f.Value)}
		}
		return fitsync.Map(fs...), nil
	}
	fs := make([]fitsync.Field, 0, len(t.Columns))
	for i := range t.Columns {
		col := &t.Columns[i]
		if x, ok := v.Get(col.Name); ok {
			fs = append(fs, fitsync.Field{Name: col.Name, Value: c.value(col, x)})
		}
	}
	if len(fs) < len(v.Fields()) {
		// Columns the schema does not have yet, untyped.
		for _, f := range v.Fields() {
			if _, ok := fitsync.Map(fs...).Get(f.Name); !ok {
				fs = append(fs, fitsync.Field{Name: f.Name, Value: c.value(nil, f.Value)})
			}
		}
	}
	return fitsync.Map(fs...), nil
}

// value types a column's value by its column, nil when unknown.
func (c *TiCDCSimple) value(col *SimpleColumn, v fitsync.Value) fitsync.Value {
	loc := c.loc
	if loc == nil {
		loc = time.UTC
	}
	if v.Kind() == fitsync.KindMap {
		// A TIMESTAMP: {"location": ..., "value": ...}.
		lv, _ := v.Get("location")
		tv, _ := v.Get("value")
		zone, err := time.LoadLocation(lv.Text())
		if err != nil {
			zone = loc
		}
		if t, err := time.ParseInLocation("2006-01-02 15:04:05.999999999", tv.Text(), zone); err == nil {
			return fitsync.Time(t)
		}
		return tv
	}
	if v.Kind() != fitsync.KindString || col == nil {
		return v
	}
	s := v.Text()
	switch col.Type.Base {
	case "enum":
		if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= len(col.Elements) {
			return fitsync.String(col.Elements[n-1])
		}
		if s == "0" {
			return fitsync.String("")
		}
	case "set":
		if n, err := strconv.ParseUint(s, 10, 64); err == nil {
			var labels []string
			for i, e := range col.Elements {
				if i < 64 && n&(1<<i) != 0 {
					labels = append(labels, e)
				}
			}
			return fitsync.String(strings.Join(labels, ","))
		}
	}
	return MySQLText(col.Type, s, unbase64(s), loc)
}

// Encode is refused: the codec only decodes.
func (c *TiCDCSimple) Encode(b []byte, recs []*fitsync.Record) ([]byte, error) {
	return b, errors.New("ticdc_simple: the codec only decodes")
}
