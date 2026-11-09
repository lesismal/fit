package synctidb

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	fitsync "github.com/lesismal/fit/sync"
	synckafka "github.com/lesismal/fit/sync/kafka"
	mysqlwire "github.com/lesismal/fit/sync/mysql/wire"
)

// db is what the source asks of a connection to TiDB: *mysqlwire.Conn
// through wireDB, or a fake in the tests.
type db interface {
	Exec(ctx context.Context, query string, args ...any) (mysqlwire.Result, error)
	Query(ctx context.Context, query string, args ...any) (rows, error)
	Close() error
}

// rows is a result's rows.
type rows interface {
	Next() bool
	Values() []any
	Err() error
	Close() error
}

type wireDB struct{ *mysqlwire.Conn }

func (w wireDB) Query(ctx context.Context, query string, args ...any) (rows, error) {
	r, err := w.Conn.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return r, nil
}

func dialWire(ctx context.Context, cfg mysqlwire.Config) (db, error) {
	c, err := mysqlwire.Dial(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return wireDB{c}, nil
}

// column is a table's column.
type column struct {
	name     string
	typ      synckafka.MySQLType
	elements []string
}

// table is a table's columns and primary key.
type table struct {
	db, name string
	cols     []column
	key      []string
}

// queryAll runs a query and returns its rows' values.
func queryAll(ctx context.Context, c db, query string, args ...any) ([][]any, error) {
	rs, err := c.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	var out [][]any
	for rs.Next() {
		out = append(out, append([]any(nil), rs.Values()...))
	}
	err = rs.Err()
	if cerr := rs.Close(); err == nil {
		err = cerr
	}
	return out, err
}

func str(v any) string {
	switch x := v.(type) {
	case []byte:
		return string(x)
	case string:
		return x
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}

// schema reads a table's columns and primary key from information_schema.
func schema(ctx context.Context, c db, ns string) (*table, error) {
	dbName, name, _ := strings.Cut(ns, ".")
	t := &table{db: dbName, name: name}
	cols, err := queryAll(ctx, c, "SELECT COLUMN_NAME, COLUMN_TYPE FROM information_schema.columns WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? ORDER BY ORDINAL_POSITION", dbName, name)
	if err != nil {
		return nil, fmt.Errorf("tidb: the columns of %s: %w", ns, err)
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("tidb: no table %s", ns)
	}
	for _, r := range cols {
		ct := str(r[1])
		t.cols = append(t.cols, column{name: str(r[0]), typ: synckafka.ParseMySQLType(ct), elements: elements(ct)})
	}
	keys, err := queryAll(ctx, c, "SELECT COLUMN_NAME FROM information_schema.key_column_usage WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND CONSTRAINT_NAME = 'PRIMARY' ORDER BY ORDINAL_POSITION", dbName, name)
	if err != nil {
		return nil, fmt.Errorf("tidb: the primary key of %s: %w", ns, err)
	}
	for _, r := range keys {
		t.key = append(t.key, str(r[0]))
	}
	return t, nil
}

// elements reads an ENUM's or a SET's elements from its column type, as
// enum('a','b').
func elements(ct string) []string {
	lower := strings.ToLower(ct)
	if !strings.HasPrefix(lower, "enum(") && !strings.HasPrefix(lower, "set(") {
		return nil
	}
	inner := ct[strings.IndexByte(ct, '(')+1 : strings.LastIndexByte(ct, ')')]
	var out []string
	for _, e := range strings.Split(inner, "','") {
		out = append(out, strings.ReplaceAll(strings.Trim(e, "'"), "''", "'"))
	}
	return out
}

// seed reads the tables' schemas: for canal-json's ENUM and SET labels, and
// for the simple codec's rows that come before the changefeed's next
// bootstrap.
func (s *Source) seed(ctx context.Context, c db) error {
	for _, ns := range s.Tables {
		t, err := s.schemaOf(ctx, c, ns)
		if err != nil {
			return err
		}
		if s.simple == nil {
			continue
		}
		st := &synckafka.SimpleTable{Key: t.key}
		for _, col := range t.cols {
			st.Columns = append(st.Columns, synckafka.SimpleColumn{Name: col.name, Type: col.typ, Elements: col.elements})
		}
		s.simple.SetTable(ns, st)
	}
	return nil
}

// schemaOf reads a table's schema, and keeps it for labels.
func (s *Source) schemaOf(ctx context.Context, c db, ns string) (*table, error) {
	t, err := schema(ctx, c, ns)
	if err != nil {
		return nil, err
	}
	if s.schemas == nil {
		s.schemas = map[string]*table{}
	}
	s.schemas[ns] = t
	return t, nil
}

func quote(name string) string { return "`" + strings.ReplaceAll(name, "`", "``") + "`" }

// snapshot reads the tables at one timestamp, taken now or the
// checkpoint's, in pages by primary key, from where p says. A timestamp
// taken now, none of the tables read, is the pipeline's checkpoint before
// the first record: a run killed before the next reads the snapshot again
// at it, and the changes after it, rather than at a later one, after
// which the changes between the two would be left out.
func (s *Source) snapshot(ctx context.Context, c db, p *position, r *fitsync.Run) error {
	if _, err := c.Exec(ctx, "SET time_zone = '+00:00'"); err != nil {
		return fmt.Errorf("tidb: %w", err)
	}
	if p.TS == 0 {
		ts, err := currentTS(ctx, c)
		if err != nil {
			return err
		}
		p.TS = ts
		b, _ := json.Marshal(p)
		if err := r.SaveStart(b); err != nil {
			return err
		}
	}
	emit := r.Emit
	if _, err := c.Exec(ctx, "SET @@tidb_snapshot = '"+strconv.FormatUint(p.TS, 10)+"'"); err != nil {
		return fmt.Errorf("tidb: reading at %d (older than the GC safe point?): %w", p.TS, err)
	}
	for i := p.Snapshot.Table; i < len(s.Tables); i++ {
		var after fitsync.Value
		if i == p.Snapshot.Table && p.Snapshot.After != nil {
			if err := json.Unmarshal(*p.Snapshot.After, &after); err != nil {
				return fmt.Errorf("tidb: the checkpoint's key: %w", err)
			}
		}
		if err := s.readTable(ctx, c, i, p.TS, after, emit); err != nil {
			return err
		}
	}
	p.Snapshot = nil
	return nil
}

// currentTS reads the timestamp a transaction started now reads at.
func currentTS(ctx context.Context, c db) (uint64, error) {
	if _, err := c.Exec(ctx, "BEGIN"); err != nil {
		return 0, fmt.Errorf("tidb: %w", err)
	}
	rs, err := queryAll(ctx, c, "SELECT @@tidb_current_ts")
	c.Exec(ctx, "ROLLBACK")
	if err != nil {
		return 0, fmt.Errorf("tidb: the current timestamp: %w", err)
	}
	if len(rs) != 1 || len(rs[0]) != 1 {
		return 0, fmt.Errorf("tidb: the current timestamp: %d rows", len(rs))
	}
	ts, err := strconv.ParseUint(str(rs[0][0]), 10, 64)
	if err != nil || ts == 0 {
		return 0, fmt.Errorf("tidb: the current timestamp %q: is the server TiDB?", str(rs[0][0]))
	}
	return ts, nil
}

// readTable reads table i after the key after, page by page.
func (s *Source) readTable(ctx context.Context, c db, i int, ts uint64, after fitsync.Value, emit func(*fitsync.Record) error) error {
	ns := s.Tables[i]
	t, err := s.schemaOf(ctx, c, ns)
	if err != nil {
		return err
	}
	if len(t.key) == 0 {
		return fmt.Errorf("tidb: %s %w", ns, errNoKey)
	}
	names := make([]string, len(t.cols))
	for j, col := range t.cols {
		names[j] = quote(col.name)
	}
	keys := make([]string, len(t.key))
	marks := make([]string, len(t.key))
	for j, k := range t.key {
		keys[j], marks[j] = quote(k), "?"
	}
	from := quote(t.db) + "." + quote(t.name)
	order := " ORDER BY " + strings.Join(keys, ", ") + " LIMIT " + strconv.Itoa(s.pageRows)
	commit := tsoTime(ts)
	for {
		q := "SELECT " + strings.Join(names, ", ") + " FROM " + from
		var args []any
		if !after.IsNull() {
			q += " WHERE (" + strings.Join(keys, ", ") + ") > (" + strings.Join(marks, ", ") + ")"
			for _, k := range t.key {
				v, _ := after.Get(k)
				args = append(args, arg(v))
			}
		}
		page, err := queryAll(ctx, c, q+order, args...)
		if err != nil {
			return fmt.Errorf("tidb: reading %s: %w", ns, err)
		}
		for _, vals := range page {
			fs := make([]fitsync.Field, len(t.cols))
			for j, col := range t.cols {
				fs[j] = fitsync.Field{Name: col.name, Value: value(col, vals[j])}
			}
			row := fitsync.Map(fs...)
			key := keyOf(row, t.key)
			kj, _ := key.MarshalJSON()
			raw := json.RawMessage(kj)
			pos, _ := json.Marshal(position{TS: ts, Snapshot: &snapPos{Table: i, After: &raw}})
			rec := &fitsync.Record{Source: "tidb", Namespace: ns, Op: fitsync.OpInsert, Key: key, After: row,
				CommitTime: commit, Position: pos}
			if err := emit(rec); err != nil {
				return err
			}
			after = key
		}
		if len(page) < s.pageRows {
			break
		}
	}
	return nil
}

func keyOf(row fitsync.Value, cols []string) fitsync.Value {
	fs := make([]fitsync.Field, len(cols))
	for i, c := range cols {
		v, _ := row.Get(c)
		fs[i] = fitsync.Field{Name: c, Value: v}
	}
	return fitsync.Map(fs...)
}

func tsoTime(ts uint64) time.Time { return time.UnixMilli(int64(ts >> 18)).UTC() }

// arg makes a key's value a statement's parameter.
func arg(v fitsync.Value) any {
	switch v.Kind() {
	case fitsync.KindInt:
		return v.AsInt()
	case fitsync.KindUint:
		return v.AsUint()
	case fitsync.KindFloat:
		return v.AsFloat()
	case fitsync.KindBytes:
		return []byte(v.Text())
	case fitsync.KindTime, fitsync.KindDate:
		return mysqlwire.FromTime(v.AsTime())
	case fitsync.KindNull:
		return nil
	}
	return v.Text()
}

// value makes a column's value of what the client read, typed as the
// canal_json and ticdc_simple codecs type the column's changes.
func value(col column, v any) fitsync.Value {
	switch x := v.(type) {
	case nil:
		return fitsync.Null()
	case int64:
		return fitsync.Int(x)
	case uint64:
		return fitsync.Uint(x)
	case float32:
		f, _ := strconv.ParseFloat(strconv.FormatFloat(float64(x), 'g', -1, 32), 64)
		return fitsync.Float(f)
	case float64:
		return fitsync.Float(x)
	case mysqlwire.DateTime:
		if col.typ.Base == "date" {
			return fitsync.Date(int(x.Year), time.Month(x.Month), int(x.Day))
		}
		return fitsync.Time(x.Time(time.UTC))
	case time.Duration:
		return fitsync.String(clock(x))
	case []byte:
		if col.typ.Base == "bit" {
			var u uint64
			for _, b := range x {
				u = u<<8 | uint64(b)
			}
			return fitsync.Uint(u)
		}
		return synckafka.MySQLText(col.typ, string(x), x, time.UTC)
	}
	return fitsync.String(fmt.Sprint(v))
}

// clock writes a TIME as MySQL does, [-]HH:MM:SS[.ffffff].
func clock(d time.Duration) string {
	sign := ""
	if d < 0 {
		sign, d = "-", -d
	}
	us := int64(d / time.Microsecond)
	s := fmt.Sprintf("%s%02d:%02d:%02d", sign, us/3600e6, us/60e6%60, us/1e6%60)
	if f := us % 1e6; f != 0 {
		s += fmt.Sprintf(".%06d", f)
	}
	return s
}

// labels turns canal-json's ENUM and SET numbers into their labels, by
// the schemas the snapshot read, so that a change's row is the snapshot's.
func (s *Source) labels(rec *fitsync.Record) {
	if s.simple != nil {
		return
	}
	t := s.schemas[rec.Namespace]
	if t == nil {
		return
	}
	fix := func(row fitsync.Value) fitsync.Value {
		if row.Kind() != fitsync.KindMap {
			return row
		}
		var fs []fitsync.Field
		for i, f := range row.Fields() {
			for _, col := range t.cols {
				if col.name != f.Name || col.elements == nil || f.Value.Kind() != fitsync.KindString {
					continue
				}
				n, err := strconv.ParseUint(f.Value.Text(), 10, 64)
				if err != nil {
					break
				}
				var label string
				if col.typ.Base == "enum" {
					if n > 0 && n <= uint64(len(col.elements)) {
						label = col.elements[n-1]
					}
				} else {
					var ls []string
					for b, e := range col.elements {
						if b < 64 && n&(1<<b) != 0 {
							ls = append(ls, e)
						}
					}
					label = strings.Join(ls, ",")
				}
				if fs == nil {
					fs = append([]fitsync.Field(nil), row.Fields()...)
				}
				fs[i].Value = fitsync.String(label)
			}
		}
		if fs == nil {
			return row
		}
		return fitsync.Map(fs...)
	}
	rec.Before, rec.After = fix(rec.Before), fix(rec.After)
}
