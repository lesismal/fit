package syncmssql

import (
	"context"
	"fmt"
	"strings"
	"time"

	fitsync "github.com/lesismal/fit/sync"
	mssqlwire "github.com/lesismal/fit/sync/mssql/wire"
)

// row makes a result's row the fields of a record.
func row(cols []mssqlwire.Column, vals []any) []fitsync.Field {
	fields := make([]fitsync.Field, len(cols))
	for i := range cols {
		fields[i] = fitsync.Field{Name: cols[i].Name, Value: value(&cols[i], vals[i])}
	}
	return fields
}

// keyOf is the key of a table's row: a map of its key's columns, null
// without a key or when a column is missing.
func (t *table) keyOf(fields []fitsync.Field) fitsync.Value {
	if len(t.key) == 0 {
		return fitsync.Null()
	}
	key := make([]fitsync.Field, 0, len(t.key))
	for _, k := range t.key {
		found := false
		for _, f := range fields {
			if f.Name == k {
				key = append(key, f)
				found = true
				break
			}
		}
		if !found {
			return fitsync.Null()
		}
	}
	return fitsync.Map(key...)
}

// list is cols quoted, each with prefix.
func list(prefix string, cols []string) string {
	q := make([]string, len(cols))
	for i, c := range cols {
		q[i] = prefix + quoteName(c)
	}
	return strings.Join(q, ", ")
}

// selectList is cols quoted, each with prefix, those in casts selected as
// CAST(col AS nvarchar(n)) under their own names: varchar, char and text of
// a code page the client does not decode, which the server converts to
// UTF-16 (I215 decision 5).
func selectList(prefix string, cols []string, casts map[string]string) string {
	q := make([]string, len(cols))
	for i, c := range cols {
		q[i] = prefix + quoteName(c)
		if n := casts[c]; n != "" {
			q[i] = "CAST(" + q[i] + " AS nvarchar(" + n + ")) AS " + quoteName(c)
		}
	}
	return strings.Join(q, ", ")
}

// castLength is the SELECT expression, over sys.columns' columns each with
// prefix, of the length a column is cast to as nvarchar: n or max for a
// varchar, char or text column whose collation's code page is neither 1252
// nor UTF-8's, NULL for any other. A varchar(n) holds at most n characters,
// so nvarchar(n) keeps them all; past nvarchar's 4000, max.
func castLength(prefix string) string {
	return fmt.Sprintf(`CASE WHEN %[1]ssystem_type_id IN (35, 167, 175) AND %[1]scollation_name IS NOT NULL
AND COLLATIONPROPERTY(%[1]scollation_name, 'CodePage') NOT IN (1252, 65001)
THEN CASE WHEN %[1]smax_length = -1 OR %[1]smax_length > 4000 OR %[1]ssystem_type_id = 35 THEN 'max' ELSE CAST(%[1]smax_length AS varchar(4)) END END`, prefix)
}

// after is the condition that a row's key, of columns cols each with
// prefix, comes after key, and its parameters, named @k1 and on.
func after(prefix string, cols []string, key fitsync.Value) (string, []any, error) {
	var ors []string
	var args []any
	for i := range cols {
		var ands []string
		for j := 0; j <= i; j++ {
			v, ok := key.Get(cols[j])
			if !ok {
				kb, _ := key.MarshalJSON()
				return "", nil, fmt.Errorf("mssql: the key %s has no %s", kb, cols[j])
			}
			name := fmt.Sprintf("@k%d", j+1)
			op := " = "
			if j == i {
				op = " > "
			}
			ands = append(ands, prefix+quoteName(cols[j])+op+name)
			if i == len(cols)-1 {
				p, ok := paramOf(v).(mssqlwire.Param)
				if !ok {
					p = mssqlwire.Param{Value: paramOf(v)}
				}
				p.Name = name
				args = append(args, p)
			}
		}
		ors = append(ors, "("+strings.Join(ands, " AND ")+")")
	}
	return "(" + strings.Join(ors, " OR ") + ")", args, nil
}

// snapshot reads the tables, from where at says (the table being read and
// its last key), in name order, handing each row on as an insert; each
// record's position is start with the snapshot's progress, and the last
// record's start alone, so that a restart after it reads the changes.
// With at.Added it reads only the tables new since start.Since, and the
// position it returns has the youngest of them as its Since.
func (rd *reader) snapshot(ctx context.Context, start position, at *snapPos) (position, error) {
	added := at != nil && at.Added
	tables := rd.tables
	if added {
		tables = rd.newTables(start.Since)
		for _, t := range tables {
			if t.created().After(start.Since) {
				start.Since = t.created()
			}
		}
	}
	if at == nil {
		rd.s.log.Infof("sync mssql: a snapshot of %d tables, the changes to start at %s", len(tables), start.marshal())
	}
	var held *fitsync.Record
	for _, t := range tables {
		if at != nil && t.ns() < at.Table {
			continue
		}
		var from *fitsync.Value
		if at != nil && t.ns() == at.Table && at.Key != nil {
			var k fitsync.Value
			if err := k.UnmarshalJSON(at.Key); err != nil {
				return start, fmt.Errorf("mssql: the checkpoint's key %s: %w", at.Key, err)
			}
			from = &k
		}
		err := rd.readTable(ctx, t, from, func(rec *fitsync.Record) error {
			kb, _ := rec.Key.MarshalJSON()
			p := start
			p.Snapshot = &snapPos{Table: t.ns(), Key: kb, Added: added}
			if rec.Key.IsNull() {
				p.Snapshot.Key = nil
			}
			rec.Position = p.marshal()
			if held != nil {
				if err := rd.r.Emit(held); err != nil {
					return err
				}
			}
			held = rec
			return nil
		})
		if err != nil {
			return start, err
		}
	}
	if held != nil {
		held.Position = start.marshal()
		return start, rd.r.Emit(held)
	}
	return start, nil
}

// newTables are the tables whose capture instances are all younger than
// since.
func (rd *reader) newTables(since time.Time) []*table {
	var out []*table
	for _, t := range rd.tables {
		if !since.IsZero() && t.created().After(since) {
			out = append(out, t)
		}
	}
	return out
}

// readTable reads a table's rows after the key from, in key order, a
// batch at a time, each batch in a transaction of snapshot isolation where
// the database allows it, handing each on as an insert. A table without a
// key is read in one query.
func (rd *reader) readTable(ctx context.Context, t *table, from *fitsync.Value, each func(*fitsync.Record) error) error {
	n := rd.s.batchSize()
	for {
		// The table is t, so that the key's order is its columns', not
		// the casts' of the select list.
		q := "SELECT " + selectList("t.", t.cols, t.casts) + " FROM " + t.from() + " AS t"
		var args []any
		if len(t.key) > 0 {
			q = fmt.Sprintf("SELECT TOP (%d) %s FROM %s AS t", n, selectList("t.", t.cols, t.casts), t.from())
			if from != nil {
				cond, a, err := after("t.", t.key, *from)
				if err != nil {
					return err
				}
				q += " WHERE " + cond
				args = a
			}
			q += " ORDER BY " + list("t.", t.key)
		}
		got := 0
		var last fitsync.Value
		err := rd.inSnapshot(ctx, func() error {
			return rd.c.Each(ctx, func(cols []mssqlwire.Column, vals []any) error {
				fields := row(cols, vals)
				rec := &fitsync.Record{Source: "mssql", Namespace: t.ns(), Op: fitsync.OpInsert, Key: t.keyOf(fields), After: fitsync.Map(fields...)}
				got++
				last = rec.Key
				return each(rec)
			}, q, args...)
		})
		if err != nil || len(t.key) == 0 || got < n {
			return err
		}
		from = &last
	}
}

// inSnapshot runs f in a transaction of snapshot isolation, where the
// database allows it, or as it is otherwise.
func (rd *reader) inSnapshot(ctx context.Context, f func() error) error {
	if !rd.srv.snapshotIso {
		return f()
	}
	if err := rd.c.Begin(ctx, "SNAPSHOT"); err != nil {
		return err
	}
	if err := f(); err != nil {
		rd.c.Rollback(ctx)
		return err
	}
	return rd.c.Commit(ctx)
}
