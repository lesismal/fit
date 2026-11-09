package syncmysql

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	fitsync "github.com/lesismal/fit/sync"
	mysqlwire "github.com/lesismal/fit/sync/mysql/wire"
)

// The defaults of the snapshot's params.
const (
	DefaultBatchSize = 1000
	MaxBatchSize     = 100000
	DefaultChunkRows = 100000
)

// snapPos is where a snapshot is: the table being read, db.table, and the
// last key read in it, the key's map in canonical JSON.
type snapPos struct {
	Table string          `json:"table"`
	Key   json.RawMessage `json:"key,omitempty"`
}

// snapTable is a table the snapshot reads: its columns and its key's.
type snapTable struct {
	db, name string
	info     *tableInfo
	key      []int
}

func (t *snapTable) ns() string { return t.db + "." + t.name }

func (s *Source) batchSize() int {
	if s.BatchSize <= 0 {
		return DefaultBatchSize
	}
	return min(s.BatchSize, MaxBatchSize)
}

// snapshotConn connects for a snapshot: the session's times in UTC, its
// transactions REPEATABLE READ.
func (s *Source) snapshotConn(ctx context.Context) (srcConn, server, error) {
	c, err := s.dial(ctx, s.cfg)
	if err != nil {
		return nil, server{}, err
	}
	srv, err := checkServer(ctx, c)
	if err == nil {
		_, err = c.Exec(ctx, "SET SESSION time_zone = '+00:00'")
	}
	if err == nil {
		_, err = c.Exec(ctx, "SET SESSION TRANSACTION ISOLATION LEVEL REPEATABLE READ")
	}
	if err != nil {
		c.Close()
		return nil, server{}, err
	}
	return c, srv, nil
}

// snapshotTables lists the tables read, sorted, each with its columns and
// key; a table without a primary key, and none named in keys, is refused.
func (s *Source) snapshotTables(ctx context.Context, c srcConn) ([]*snapTable, error) {
	_, rows, err := c.QueryAll(ctx, "SELECT TABLE_SCHEMA, TABLE_NAME FROM information_schema.TABLES WHERE TABLE_TYPE = 'BASE TABLE'")
	if err != nil {
		return nil, fmt.Errorf("mysql: listing the tables: %w", err)
	}
	var out []*snapTable
	for _, r := range rows {
		t := &snapTable{db: str(r[0]), name: str(r[1])}
		if s.included(t.ns()) {
			out = append(out, t)
		}
	}
	slices.SortFunc(out, func(a, b *snapTable) int { return strings.Compare(a.ns(), b.ns()) })
	for _, t := range out {
		if t.info, err = readTable(ctx, c, t.db, t.name); err != nil {
			return nil, err
		}
		if names, ok := s.Keys[t.ns()]; ok {
			for _, n := range names {
				i := t.info.index(n)
				if i < 0 {
					return nil, fmt.Errorf("mysql: keys names column %q, which %s does not have", n, t.ns())
				}
				t.key = append(t.key, i)
			}
		} else {
			t.key = t.info.pk
		}
		if len(t.key) == 0 {
			return nil, fmt.Errorf("mysql: %s has no primary key, by which a snapshot reads it: name its key's columns in keys, or leave it out of tables", t.ns())
		}
	}
	return out, nil
}

// cols are the table's columns, quoted and joined; keyCols its key's.
func (t *snapTable) cols(which []int) string {
	var b strings.Builder
	for i, j := range which {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(quoteIdent(t.info.cols[j].name))
	}
	return b.String()
}

func (t *snapTable) all() []int {
	out := make([]int, len(t.info.cols))
	for i := range out {
		out[i] = i
	}
	return out
}

func (t *snapTable) from() string { return quoteIdent(t.db) + "." + quoteIdent(t.name) }

// compare is "(k1, k2) op (?, ?)" with the key's values as args.
func (t *snapTable) compare(op string, key fitsync.Value) (string, []any, error) {
	var args []any
	marks := make([]string, len(t.key))
	for i, j := range t.key {
		v, ok := key.Get(t.info.cols[j].name)
		if !ok {
			return "", nil, fmt.Errorf("mysql: the key %s has no column %s", plainOf(key), t.info.cols[j].name)
		}
		args = append(args, paramOf(v))
		marks[i] = "?"
	}
	return "(" + t.cols(t.key) + ") " + op + " (" + strings.Join(marks, ", ") + ")", args, nil
}

func plainOf(v fitsync.Value) string {
	b, _ := fitsync.AppendPlain(nil, v)
	return string(b)
}

// paramOf is a key's value as a statement's parameter.
func paramOf(v fitsync.Value) any {
	switch v.Kind() {
	case fitsync.KindInt:
		return v.AsInt()
	case fitsync.KindUint:
		return v.AsUint()
	case fitsync.KindFloat:
		return v.AsFloat()
	case fitsync.KindBytes:
		return []byte(v.Text())
	case fitsync.KindTime:
		return v.AsTime().UTC().Format("2006-01-02 15:04:05.999999")
	case fitsync.KindDate:
		return v.AsTime().Format("2006-01-02")
	case fitsync.KindNull:
		return nil
	}
	return v.Text()
}

// keyOf is a row's key, the map of its key's columns, row being the
// row's columns in the table's order.
func (t *snapTable) keyOf(row []fitsync.Field) fitsync.Value {
	f := make([]fitsync.Field, len(t.key))
	for i, j := range t.key {
		f[i] = row[j]
	}
	return fitsync.Map(f...)
}

// readRange reads table t's rows by key, from after (exclusive) or lo
// (inclusive) to before hi, nil bounds unbounded, a page of batch_size
// rows at a time, each page in a transaction of its own with a consistent
// snapshot, handing each row's record to each.
func (s *Source) readRange(ctx context.Context, c srcConn, t *snapTable, after, lo, hi *fitsync.Value, each func(*fitsync.Record) error) error {
	order := t.cols(t.key)
	ns := t.ns()
	for {
		var where []string
		var args []any
		add := func(op string, k *fitsync.Value) error {
			if k == nil {
				return nil
			}
			w, a, err := t.compare(op, *k)
			where, args = append(where, w), append(args, a...)
			return err
		}
		if after != nil {
			if err := add(">", after); err != nil {
				return err
			}
		} else if err := add(">=", lo); err != nil {
			return err
		}
		if err := add("<", hi); err != nil {
			return err
		}
		q := "SELECT " + t.cols(t.all()) + " FROM " + t.from()
		if len(where) > 0 {
			q += " WHERE " + strings.Join(where, " AND ")
		}
		q += " ORDER BY " + order + " LIMIT " + strconv.Itoa(s.batchSize())
		if _, err := c.Exec(ctx, "START TRANSACTION WITH CONSISTENT SNAPSHOT"); err != nil {
			return fmt.Errorf("mysql: reading %s: %w", t.ns(), err)
		}
		cols, rows, err := c.QueryAll(ctx, q, args...)
		if err == nil {
			_, err = c.Exec(ctx, "COMMIT")
		}
		if err != nil {
			return fmt.Errorf("mysql: reading %s: %w", t.ns(), err)
		}
		var none mysqlwire.Column
		for _, r := range rows {
			f := make([]fitsync.Field, len(r))
			for i, v := range r {
				col := &none
				if i < len(cols) {
					col = &cols[i]
				}
				f[i] = fitsync.Field{Name: t.info.cols[i].name, Value: wireValue(col, v)}
			}
			key := t.keyOf(f)
			after = &key
			rec := &fitsync.Record{Source: "mysql", Namespace: ns, Op: fitsync.OpInsert, Key: key, After: fitsync.Map(f...)}
			if err := each(rec); err != nil {
				return err
			}
		}
		if len(rows) < s.batchSize() {
			return nil
		}
	}
}

// wireValue is a value of a result as the binlog decodes the column's
// (binlog.Column says how), so that a row read by a snapshot and by the
// stream is the same record.
func wireValue(col *mysqlwire.Column, v any) fitsync.Value {
	switch x := v.(type) {
	case nil:
		return fitsync.Null()
	case int64:
		return fitsync.Int(x)
	case uint64:
		if col.Type == mysqlwire.TypeYear {
			return fitsync.Int(int64(x))
		}
		return fitsync.Uint(x)
	case float32:
		g, _ := strconv.ParseFloat(strconv.FormatFloat(float64(x), 'g', -1, 32), 64)
		return fitsync.Float(g)
	case float64:
		return fitsync.Float(x)
	case time.Duration:
		return fitsync.String(formatDuration(x, int(col.Decimals)))
	case mysqlwire.DateTime:
		fsp := int(col.Decimals)
		if col.Type == mysqlwire.TypeDate {
			if x.Year == 0 || x.Month == 0 || x.Day == 0 {
				return fitsync.String(fmt.Sprintf("%04d-%02d-%02d", x.Year, x.Month, x.Day))
			}
			return fitsync.Date(int(x.Year), time.Month(x.Month), int(x.Day))
		}
		if x.Year == 0 || x.Month == 0 || x.Day == 0 {
			return fitsync.String(fmt.Sprintf("%04d-%02d-%02d %02d:%02d:%02d%s", x.Year, x.Month, x.Day, x.Hour, x.Minute, x.Second, fraction(int(x.Microsecond), fsp)))
		}
		return fitsync.Time(x.Time(time.UTC))
	case []byte:
		switch col.Type {
		case mysqlwire.TypeNewDecimal, mysqlwire.TypeDecimal:
			return fitsync.Decimal(string(x))
		case mysqlwire.TypeJSON:
			return fitsync.JSON(x)
		case mysqlwire.TypeBit:
			var u uint64
			for _, c := range x {
				u = u<<8 | uint64(c)
			}
			return fitsync.Uint(u)
		case mysqlwire.TypeGeometry:
			return fitsync.Bytes(x)
		}
		if col.Collation == mysqlwire.BinaryCollation {
			return fitsync.Bytes(x)
		}
		return fitsync.String(string(x))
	}
	return fitsync.String(fmt.Sprint(v))
}

// fraction is a fraction of fsp digits, as MySQL writes it.
func fraction(us, fsp int) string {
	if fsp <= 0 || fsp > 6 {
		return ""
	}
	s := fmt.Sprintf("%06d", us)
	return "." + s[:fsp]
}

// formatDuration is a TIME as MySQL writes it, [-]HH:MM:SS with fsp
// digits of fraction.
func formatDuration(d time.Duration, fsp int) string {
	sign := ""
	if d < 0 {
		sign, d = "-", -d
	}
	us := int64(d / time.Microsecond)
	sec := us / 1e6
	return fmt.Sprintf("%s%02d:%02d:%02d%s", sign, sec/3600, sec/60%60, sec%60, fraction(int(us%1e6), fsp))
}

// snapshot reads every table read, on c, from where pos says (the table being
// read and its last key), in order, handing each row on as an insert; each
// record's position is the stream's start with the snapshot's progress,
// and the last record's the stream's start alone, so that a restart after
// it reads the stream.
func (s *Source) snapshot(ctx context.Context, r *fitsync.Run, c srcConn, start position, at *snapPos) error {
	tables, err := s.snapshotTables(ctx, c)
	if err != nil {
		return err
	}
	if at == nil {
		s.log.Infof("sync mysql: a snapshot of %d tables, the stream to start at %s", len(tables), start.marshal())
	}
	var held *fitsync.Record
	for _, t := range tables {
		if at != nil && t.ns() < at.Table {
			continue
		}
		var after *fitsync.Value
		if at != nil && t.ns() == at.Table && at.Key != nil {
			var k fitsync.Value
			if err := k.UnmarshalJSON(at.Key); err != nil {
				return fmt.Errorf("mysql: the checkpoint's key %s: %w", at.Key, err)
			}
			after = &k
		}
		err := s.readRange(ctx, c, t, after, nil, nil, func(rec *fitsync.Record) error {
			kb, _ := rec.Key.MarshalJSON()
			p := start
			p.Snapshot = &snapPos{Table: t.ns(), Key: kb}
			rec.Position = p.marshal()
			if held != nil {
				if err := r.Emit(held); err != nil {
					return err
				}
			}
			held = rec
			return nil
		})
		if err != nil {
			return err
		}
	}
	if held != nil {
		held.Position = start.marshal()
		return r.Emit(held)
	}
	return nil
}

// Chunks splits a job's snapshot into ranges of each table's key,
// chunk_rows rows each, bounded by the keys found every chunk_rows rows in
// key order. None with snapshot never, whose job reads the binlog to its
// end.
func (s *Source) Chunks(ctx context.Context) ([]fitsync.Chunk, error) {
	if s.Snapshot == "never" {
		return nil, nil
	}
	c, _, err := s.snapshotConn(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	tables, err := s.snapshotTables(ctx, c)
	if err != nil {
		return nil, err
	}
	if len(tables) > fitsync.MaxChunks {
		return nil, fmt.Errorf("mysql: %d tables, past the %d chunks a job may have", len(tables), fitsync.MaxChunks)
	}
	per := s.ChunkRows
	if per <= 0 {
		per = DefaultChunkRows
	}
	var plan []fitsync.Chunk
	for i, t := range tables {
		row, err := queryRow(ctx, c, "SELECT COALESCE(TABLE_ROWS, 0) FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?", t.db, t.name)
		if err != nil {
			return nil, err
		}
		rows, _ := strconv.ParseInt(str(row[0]), 10, 64)
		room := fitsync.MaxChunks - len(plan) - (len(tables) - i - 1)
		var bounds []json.RawMessage
		var lo *fitsync.Value
		for len(bounds)+1 < room {
			q := "SELECT " + t.cols(t.key) + " FROM " + t.from()
			var args []any
			if lo != nil {
				w, a, _ := t.compare(">=", *lo)
				q, args = q+" WHERE "+w, a
			}
			q += " ORDER BY " + t.cols(t.key) + " LIMIT 1 OFFSET " + strconv.FormatInt(per, 10)
			cols, found, err := c.QueryAll(ctx, q, args...)
			if err != nil {
				return nil, fmt.Errorf("mysql: splitting %s: %w", t.ns(), err)
			}
			if len(found) == 0 {
				break
			}
			f := make([]fitsync.Field, len(t.key))
			for j, k := range t.key {
				var col mysqlwire.Column
				if j < len(cols) {
					col = cols[j]
				}
				f[j] = fitsync.Field{Name: t.info.cols[k].name, Value: wireValue(&col, found[0][j])}
			}
			// The first key of the next chunk, which ends this one.
			key := fitsync.Map(f...)
			lo = &key
			kb, _ := key.MarshalJSON()
			bounds = append(bounds, kb)
		}
		for j := range len(bounds) + 1 {
			ch := fitsync.Chunk{Namespace: t.ns(), Rows: per}
			if j > 0 {
				ch.Lo = bounds[j-1]
			}
			if j < len(bounds) {
				ch.Hi = bounds[j]
			} else {
				ch.Rows = max(rows-int64(len(bounds))*per, 0)
			}
			plan = append(plan, ch)
		}
	}
	return plan, nil
}

// ReadChunk reads a chunk of a table, its keys from lo to before hi, after
// from's key when from is set.
func (s *Source) ReadChunk(ctx context.Context, ch fitsync.Chunk, from fitsync.Position, emit func(*fitsync.Record) error) error {
	value := func(b json.RawMessage) (*fitsync.Value, error) {
		if b == nil {
			return nil, nil
		}
		var v fitsync.Value
		if err := v.UnmarshalJSON(b); err != nil {
			return nil, fmt.Errorf("mysql: the chunk's bound %s: %w", b, err)
		}
		return &v, nil
	}
	lo, err := value(ch.Lo)
	if err != nil {
		return err
	}
	hi, err := value(ch.Hi)
	if err != nil {
		return err
	}
	var after *fitsync.Value
	if from != nil {
		var p snapPos
		if err := json.Unmarshal(from, &p); err != nil {
			return fmt.Errorf("mysql: the chunk's position %s: %w", from, err)
		}
		if after, err = value(p.Key); err != nil {
			return err
		}
	}
	c, _, err := s.snapshotConn(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	tables, err := s.snapshotTables(ctx, c)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(tables, func(t *snapTable) bool { return t.ns() == ch.Namespace })
	if i < 0 {
		return fmt.Errorf("mysql: the chunk's table %s is not read any more", ch.Namespace)
	}
	t := tables[i]
	head := snapPosHead(t.ns())
	return s.readRange(ctx, c, t, after, lo, hi, func(rec *fitsync.Record) error {
		rec.Position = appendSnapPos(head, rec.Key)
		return emit(rec)
	})
}

// snapPosHead is the start of a snapshot position's JSON for the table
// ns, which appendSnapPos ends with a record's key.
func snapPosHead(ns string) []byte {
	return fitsync.AppendJSONString([]byte(`{"table":`), ns)
}

// appendSnapPos is json.Marshal(snapPos{Table, Key}) for the table whose
// head snapPosHead made, made by hand: a job's every record carries one,
// and encoding/json's reflection was a twentieth of a copy's CPU.
func appendSnapPos(head []byte, key fitsync.Value) fitsync.Position {
	pos := make([]byte, len(head), len(head)+64)
	copy(pos, head)
	withKey, err := key.AppendJSON(append(pos, `,"key":`...))
	if err != nil {
		return append(pos, '}')
	}
	return append(withKey, '}')
}
