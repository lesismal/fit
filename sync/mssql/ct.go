package syncmssql

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	fitsync "github.com/lesismal/fit/sync"
	mssqlwire "github.com/lesismal/fit/sync/mssql/wire"
)

// track reads change tracking's changes after from's version, every
// table's up to the current version as one transaction, until ctx ends,
// or, in a job, until it reaches the version there was when it started.
func (rd *reader) track(ctx context.Context, from position) error {
	for _, t := range rd.tables {
		if len(t.key) == 0 {
			return fmt.Errorf("mssql: %s has no primary key, which change tracking needs", t.ns())
		}
	}
	v := from.Version
	end := int64(-1)
	if rd.r.Once {
		p, err := rd.head(ctx)
		if err != nil {
			return err
		}
		end = p.Version
	}
	for {
		p, err := rd.head(ctx)
		if err != nil {
			return err
		}
		cur := p.Version
		if end >= 0 {
			cur = min(cur, end)
		}
		rd.r.ReportLag(p.Version-v, lagVersions)
		if cur <= v {
			if rd.r.Once {
				return nil
			}
			if err := rd.s.sleep(ctx, rd.s.pollInterval()); err != nil {
				return err
			}
			continue
		}
		if err := rd.trackOnce(ctx, v, cur); err != nil {
			return err
		}
		v = cur
	}
}

// trackOnce hands on the changes after version v up to cur, in snapshot
// isolation where the database allows it.
func (rd *reader) trackOnce(ctx context.Context, v, cur int64) error {
	e := &emitter{r: rd.r}
	id := lsn("ct-" + strconv.FormatInt(cur, 10))
	err := rd.inSnapshot(ctx, func() error {
		for _, t := range rd.tables {
			_, rows, err := rd.c.QueryAll(ctx, "SELECT CHANGE_TRACKING_MIN_VALID_VERSION(OBJECT_ID(@p1))", t.from())
			if err != nil {
				return err
			}
			if lowest, _ := rows[0][0].(int64); lowest > v {
				return fmt.Errorf("mssql: change tracking keeps %s's changes from version %d, past the position %d: %w", t.ns(), lowest, v, errNeedSnapshot)
			}
			if err := rd.trackTable(ctx, t, v, cur, func(rec *fitsync.Record) error { return e.push(rec, id) }); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if e.held == nil {
		return nil
	}
	rec := e.held
	if e.n > 1 {
		rec.Tx = fitsync.Tx{ID: string(id), Last: true}
	}
	rec.Position = position{Version: cur}.marshal()
	e.held = nil
	return rd.r.Emit(rec)
}

// trackTable reads a table's changed keys after version v up to cur, by
// key a batch at a time, with their rows as they are now.
func (rd *reader) trackTable(ctx context.Context, t *table, v, cur int64, each func(*fitsync.Record) error) error {
	n := rd.s.batchSize()
	var join []string
	for _, k := range t.key {
		join = append(join, "t."+quoteName(k)+" = ct."+quoteName(k))
	}
	var from *fitsync.Value
	for {
		q := fmt.Sprintf("SELECT TOP (%d) ct.SYS_CHANGE_OPERATION, %s, %s FROM CHANGETABLE(CHANGES %s, @p1) AS ct LEFT JOIN %s AS t ON %s WHERE ct.SYS_CHANGE_VERSION <= @p2",
			n, list("ct.", t.key), list("t.", t.cols), t.from(), t.from(), strings.Join(join, " AND "))
		args := []any{v, cur}
		if from != nil {
			cond, a, err := after("ct.", t.key, *from)
			if err != nil {
				return err
			}
			q += " AND " + cond
			args = append(args, a...)
		}
		q += " ORDER BY " + list("ct.", t.key)
		got := 0
		var last fitsync.Value
		err := rd.c.Each(ctx, func(cols []mssqlwire.Column, vals []any) error {
			got++
			nk := len(t.key)
			key := t.keyOf(row(cols[1:1+nk], vals[1:1+nk]))
			last = key
			rec := &fitsync.Record{Source: "mssql", Namespace: t.ns(), Key: key}
			// The row is gone when its key column, read back, is NULL.
			present := false
			for i, c := range t.cols {
				if c == t.key[0] {
					present = vals[1+nk+i] != nil
				}
			}
			op, _ := vals[0].(string)
			switch {
			case op == "D" || !present:
				rec.Op = fitsync.OpDelete
			case op == "I":
				rec.Op, rec.After = fitsync.OpInsert, fitsync.Map(row(cols[1+nk:], vals[1+nk:])...)
			default:
				rec.Op, rec.After = fitsync.OpUpdate, fitsync.Map(row(cols[1+nk:], vals[1+nk:])...)
			}
			return each(rec)
		}, q, args...)
		if err != nil || got < n {
			return err
		}
		from = &last
	}
}
