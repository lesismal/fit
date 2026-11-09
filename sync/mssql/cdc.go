package syncmssql

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	fitsync "github.com/lesismal/fit/sync"
	mssqlwire "github.com/lesismal/fit/sync/mssql/wire"
)

// CDC's operations.
const (
	cdcDelete       = 1
	cdcInsert       = 2
	cdcUpdateBefore = 3
	cdcUpdateAfter  = 4
)

// tx is a transaction of lsn_time_mapping: its commit LSN and time.
type tx struct {
	lsn lsn
	at  time.Time
}

// change is a row of a change table, or a DDL of ddl_history.
type change struct {
	lsn lsn
	seq string
	op  int64
	tab int
	row []fitsync.Field
	ddl string
	at  time.Time
}

// errTooMany says a window holds more changed rows than window_rows.
var errTooMany = errors.New("mssql: too many rows for one window")

// follow reads the changes after from, a window of transactions at a time,
// until ctx ends, or, in a job, until it reaches the highest LSN there was
// when it started.
func (rd *reader) follow(ctx context.Context, from position) error {
	at, since := from.LSN, from.Since
	var end lsn
	if rd.r.Once {
		l, err := rd.maxLSN(ctx)
		if err != nil {
			return err
		}
		end = l
	}
	known, err := rd.captured(ctx)
	if err != nil {
		return err
	}
	for {
		if err := rd.checkKept(ctx, at); err != nil {
			return err
		}
		head, err := rd.maxLSN(ctx)
		if err != nil {
			return err
		}
		if end != "" && head > end {
			head = end
		}
		txs, err := rd.window(ctx, at, head, rd.s.windowTransactions())
		if err != nil {
			return err
		}
		if len(txs) > 0 {
			if at, err = rd.read(ctx, at, txs, since); err != nil {
				return err
			}
			continue
		}
		if rd.r.Once {
			return nil
		}
		// Caught up: tables CDC began to capture are read whole, then
		// followed.
		now, err := rd.captured(ctx)
		if err != nil {
			return err
		}
		if now != known {
			_, tables, err := rd.s.discover(ctx, rd.c)
			if err != nil {
				return err
			}
			rd.tables, known = tables, now
			if len(rd.newTables(since)) > 0 {
				p, err := rd.snapshot(ctx, position{LSN: at, Since: since}, &snapPos{Added: true})
				if err != nil {
					return err
				}
				since = p.Since
				continue
			}
		}
		if err := rd.s.sleep(ctx, rd.s.pollInterval()); err != nil {
			return err
		}
	}
}

// maxLSN is CDC's highest LSN.
func (rd *reader) maxLSN(ctx context.Context) (lsn, error) {
	p, err := rd.head(ctx)
	return p.LSN, err
}

// captured is the names of CDC's capture instances: when they change, a
// table may have been added, or an instance to a table.
func (rd *reader) captured(ctx context.Context) (string, error) {
	_, rows, err := rd.c.QueryAll(ctx, "SELECT capture_instance FROM cdc.change_tables ORDER BY capture_instance")
	if err != nil {
		return "", err
	}
	names := make([]string, len(rows))
	for i, r := range rows {
		names[i] = str(r[0])
	}
	return strings.Join(names, ","), nil
}

// checkKept fails when CDC's cleanup removed changes after at: the oldest
// transaction it keeps is after it.
func (rd *reader) checkKept(ctx context.Context, at lsn) error {
	if at == zeroLSN {
		return nil
	}
	_, rows, err := rd.c.QueryAll(ctx, "SELECT MIN(start_lsn) FROM cdc.lsn_time_mapping")
	if err != nil {
		return err
	}
	if oldest := lsnOf(rows[0][0]); oldest != "" && oldest > at {
		return fmt.Errorf("mssql: CDC keeps changes from %s, past the position %s: %w", oldest, at, errNeedSnapshot)
	}
	return nil
}

// window reads up to n transactions after at, to head at most.
func (rd *reader) window(ctx context.Context, at, head lsn, n int) ([]tx, error) {
	if head <= at {
		return nil, nil
	}
	_, rows, err := rd.c.QueryAll(ctx, fmt.Sprintf("SELECT TOP (%d) start_lsn, tran_end_time FROM cdc.lsn_time_mapping WHERE start_lsn > @p1 AND start_lsn <= @p2 ORDER BY start_lsn", n),
		at.param(), head.param())
	if err != nil {
		return nil, err
	}
	txs := make([]tx, len(rows))
	for i, r := range rows {
		txs[i].lsn = lsnOf(r[0])
		txs[i].at = rd.serverTime(r[1])
	}
	return txs, nil
}

// serverTime is a datetime of the server's clock as the time it is.
func (rd *reader) serverTime(v any) time.Time {
	t, ok := v.(time.Time)
	if !ok {
		return time.Time{}
	}
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), rd.srv.zone).UTC()
}

// read hands on the changes of txs, after at, in order, and returns the
// last one's LSN: the window whole when its rows fit in window_rows, its
// first half when they do not, a lone transaction's in pages.
func (rd *reader) read(ctx context.Context, at lsn, txs []tx, since time.Time) (lsn, error) {
	for {
		to := txs[len(txs)-1].lsn
		changes, err := rd.gather(ctx, at, to, rd.s.windowRows())
		if errors.Is(err, errTooMany) {
			if len(txs) == 1 {
				return to, rd.readLone(ctx, txs[0], since)
			}
			txs = txs[:len(txs)/2]
			continue
		}
		if err != nil {
			return at, err
		}
		ddls, err := rd.ddl(ctx, at, to)
		if err != nil {
			return at, err
		}
		changes = append(changes, ddls...)
		sort.SliceStable(changes, func(i, j int) bool {
			a, b := &changes[i], &changes[j]
			if a.lsn != b.lsn {
				return a.lsn < b.lsn
			}
			if (a.ddl != "") != (b.ddl != "") {
				return a.ddl != ""
			}
			if a.seq != b.seq {
				return a.seq < b.seq
			}
			return a.op < b.op
		})
		times := make(map[lsn]time.Time, len(txs))
		for _, t := range txs {
			times[t.lsn] = t.at
		}
		e := &emitter{r: rd.r, since: since}
		for i := 0; i < len(changes); i++ {
			c := &changes[i]
			if c.ddl != "" {
				if err := e.flush(); err != nil {
					return at, err
				}
				rec := &fitsync.Record{Source: "mssql", Namespace: rd.tables[c.tab].ns(), Op: fitsync.OpDDL, CommitTime: c.at,
					After: fitsync.Map(fitsync.Field{Name: "statement", Value: fitsync.String(c.ddl)})}
				if err := rd.r.Emit(rec); err != nil {
					return at, err
				}
				continue
			}
			var next *change
			if i+1 < len(changes) {
				next = &changes[i+1]
			}
			rec, pair := rd.record(c, next)
			if pair {
				i++
			}
			rec.CommitTime = times[c.lsn]
			if err := e.push(rec, c.lsn); err != nil {
				return at, err
			}
		}
		if err := e.flush(); err != nil {
			return at, err
		}
		return to, nil
	}
}

// record makes a change a record, an update of its before and after rows
// when next is its after: pair says it took next.
func (rd *reader) record(c, next *change) (*fitsync.Record, bool) {
	t := rd.tables[c.tab]
	rec := &fitsync.Record{Source: "mssql", Namespace: t.ns()}
	switch c.op {
	case cdcDelete:
		rec.Op, rec.Key, rec.Before = fitsync.OpDelete, t.keyOf(c.row), fitsync.Map(c.row...)
	case cdcInsert:
		rec.Op, rec.Key, rec.After = fitsync.OpInsert, t.keyOf(c.row), fitsync.Map(c.row...)
	case cdcUpdateBefore:
		if next != nil && next.op == cdcUpdateAfter && next.lsn == c.lsn && next.seq == c.seq && next.tab == c.tab {
			rec.Op, rec.Key, rec.Before, rec.After = fitsync.OpUpdate, t.keyOf(next.row), fitsync.Map(c.row...), fitsync.Map(next.row...)
			return rec, true
		}
		// A before with no after: the row as it was, kept.
		rec.Op, rec.Key, rec.After = fitsync.OpUpdate, t.keyOf(c.row), fitsync.Map(c.row...)
	default:
		rec.Op, rec.Key, rec.After = fitsync.OpUpdate, t.keyOf(c.row), fitsync.Map(c.row...)
	}
	return rec, false
}

// segment is the part of a window a capture instance holds.
type segment struct {
	in     *instance
	lo, hi lsn
}

// segments are the capture instances of t to read for the LSNs after at
// to to: each holds those from its start to the next one's.
func (t *table) segments(at, to lsn) []segment {
	var out []segment
	for i, in := range t.instances {
		lo, hi := at.next(), to
		if in.start > lo {
			lo = in.start
		}
		if i+1 < len(t.instances) {
			if nxt := t.instances[i+1].start.prev(); nxt < hi {
				hi = nxt
			}
		}
		if lo <= hi {
			out = append(out, segment{in: in, lo: lo, hi: hi})
		}
	}
	return out
}

// changesQuery is the query of a capture instance's changes, up to n rows
// and the rest of the last one's sequence number, from @p1 to @p2, after
// the sequence number @p3 when seq.
func changesQuery(in *instance, n int, seq bool) string {
	q := fmt.Sprintf("SELECT TOP (%d) WITH TIES __$start_lsn, __$seqval, __$operation, %s FROM cdc.%s(@p1, @p2, N'all update old')",
		n, list("", in.cols), quoteName("fn_cdc_get_all_changes_"+in.name))
	if seq {
		q += " WHERE __$seqval > @p3"
	}
	return q + " ORDER BY __$start_lsn, __$seqval"
}

// scan reads a change table's row.
func scan(tab int, cols []mssqlwire.Column, vals []any) change {
	c := change{lsn: lsnOf(vals[0]), tab: tab, row: row(cols[3:], vals[3:])}
	seq, _ := vals[1].([]byte)
	c.seq = hex.EncodeToString(seq)
	c.op, _ = vals[2].(int64)
	return c
}

// gather reads every table's changes after at to to, failing with
// errTooMany past limit rows.
func (rd *reader) gather(ctx context.Context, at, to lsn, limit int) ([]change, error) {
	var out []change
	for ti, t := range rd.tables {
		for _, sg := range t.segments(at, to) {
			err := rd.c.Each(ctx, func(cols []mssqlwire.Column, vals []any) error {
				if len(out) >= limit {
					return errTooMany
				}
				out = append(out, scan(ti, cols, vals))
				return nil
			}, changesQuery(sg.in, limit+1, false), sg.lo.param(), sg.hi.param())
			if err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// readLone hands on one transaction's changes, a table at a time, in pages
// of window_rows (an update's two rows never split), for a transaction
// too large to hold.
func (rd *reader) readLone(ctx context.Context, t tx, since time.Time) error {
	e := &emitter{r: rd.r, since: since}
	n := rd.s.windowRows()
	for ti, tb := range rd.tables {
		for _, sg := range tb.segments(t.lsn.prev(), t.lsn) {
			seq := ""
			for {
				var page []change
				err := rd.c.Each(ctx, func(cols []mssqlwire.Column, vals []any) error {
					page = append(page, scan(ti, cols, vals))
					return nil
				}, changesQuery(sg.in, n, true), t.lsn.param(), t.lsn.param(), mssqlwire.Param{Value: hexBytes(seq), Type: "binary(10)"})
				if err != nil {
					return err
				}
				sort.SliceStable(page, func(i, j int) bool {
					if page[i].seq != page[j].seq {
						return page[i].seq < page[j].seq
					}
					return page[i].op < page[j].op
				})
				for i := 0; i < len(page); i++ {
					var next *change
					if i+1 < len(page) {
						next = &page[i+1]
					}
					rec, pair := rd.record(&page[i], next)
					if pair {
						i++
					}
					rec.CommitTime = t.at
					if err := e.push(rec, t.lsn); err != nil {
						return err
					}
				}
				if len(page) < n {
					break
				}
				seq = page[len(page)-1].seq
			}
		}
	}
	return e.flush()
}

func hexBytes(s string) []byte {
	b, _ := hex.DecodeString(s)
	if len(b) == 0 {
		return make([]byte, 10)
	}
	return b
}

// ddl reads the DDL CDC saw on the tables after at to to.
func (rd *reader) ddl(ctx context.Context, at, to lsn) ([]change, error) {
	_, rows, err := rd.c.QueryAll(ctx, `SELECT ddl_lsn, ddl_time, ddl_command, OBJECT_SCHEMA_NAME(source_object_id), OBJECT_NAME(source_object_id)
FROM cdc.ddl_history WHERE ddl_lsn > @p1 AND ddl_lsn <= @p2 ORDER BY ddl_lsn`, at.param(), to.param())
	if err != nil {
		return nil, err
	}
	var out []change
	for _, r := range rows {
		ns := str(r[3]) + "." + str(r[4])
		for ti, t := range rd.tables {
			if t.ns() == ns {
				out = append(out, change{lsn: lsnOf(r[0]), tab: ti, ddl: str(r[2]), at: rd.serverTime(r[1])})
				break
			}
		}
	}
	return out, nil
}

// emitter hands records on a transaction at a time, holding each until
// the next tells whether it ends its transaction.
type emitter struct {
	r     *fitsync.Run
	since time.Time
	held  *fitsync.Record
	lsn   lsn
	n     int
}

// push hands on the record held, if any, and holds rec, of the transaction
// at l.
func (e *emitter) push(rec *fitsync.Record, l lsn) error {
	if e.held != nil && e.lsn != l {
		if err := e.flush(); err != nil {
			return err
		}
	}
	if e.held != nil {
		e.held.Tx = fitsync.Tx{ID: string(e.lsn), First: e.n == 1}
		if err := e.r.Emit(e.held); err != nil {
			return err
		}
	}
	e.held, e.lsn = rec, l
	e.n++
	return nil
}

// flush hands on the record held as its transaction's last, with the
// position after it.
func (e *emitter) flush() error {
	if e.held == nil {
		return nil
	}
	rec := e.held
	if e.n > 1 {
		rec.Tx = fitsync.Tx{ID: string(e.lsn), Last: true}
	}
	rec.Position = position{LSN: e.lsn, Since: e.since}.marshal()
	e.held, e.n = nil, 0
	return e.r.Emit(rec)
}
