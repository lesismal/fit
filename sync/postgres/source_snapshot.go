package syncpostgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
	fitsync "github.com/lesismal/fit/sync"
	pgwire "github.com/lesismal/fit/sync/postgres/wire"
)

// The snapshot: the tables read whole, in chunks by key, each chunk in a
// REPEATABLE READ transaction that takes an exported snapshot with SET
// TRANSACTION SNAPSHOT, several at once; the stream then starts at the
// point the snapshot was exported at, so that what the snapshot has and
// what the stream brings meet exactly. A job reads the same chunks through
// fitsync.Chunker, each in a transaction of its own.

// tableInfo is a table as the snapshot reads it.
type tableInfo struct {
	ns, schema, name string
	// cols are the columns read, in the table's order, as pgoutput sends
	// them: generated ones are left out, and a publication's column list
	// keeps its own.
	cols []string
	// key are the record's key columns, as the stream keys the table's
	// changes: the primary key, or the replica identity's.
	key []int
	// order are the columns the table is read in the order of and split
	// by: a unique key without nulls; nil reads it whole, in one chunk.
	order []int
	// filter is the publication's row filter (15+).
	filter string
}

// newTableInfo makes a table's from the catalog: its replica identity
// (d, n, f or i), primary key, replica identity index and columns, and the
// publication's column list and row filter.
func newTableInfo(ns, ident string, pk, ri, cols []string, pub pubTable) *tableInfo {
	t := &tableInfo{ns: ns, filter: pub.filter}
	t.schema, t.name = splitTable(ns)
	for _, c := range cols {
		if len(pub.cols) == 0 || slices.Contains(pub.cols, c) {
			t.cols = append(t.cols, c)
		}
	}
	index := func(names []string) []int {
		var out []int
		for _, n := range names {
			i := slices.Index(t.cols, n)
			if i < 0 {
				return nil
			}
			out = append(out, i)
		}
		return out
	}
	// As the stream's relation: the identity index's columns, in the
	// table's order, with USING INDEX; the primary key otherwise; every
	// column with FULL and no primary key.
	switch {
	case ident == "i" && len(ri) > 0:
		t.order = index(ri)
		t.key = slices.Sorted(slices.Values(t.order))
	case len(pk) > 0:
		t.key = index(pk)
		t.order = t.key
	case ident == "f":
		for i := range t.cols {
			t.key = append(t.key, i)
		}
	}
	return t
}

func (t *tableInfo) from() string {
	return pgwire.QuoteIdentifier(t.schema) + "." + pgwire.QuoteIdentifier(t.name)
}

func (t *tableInfo) columns(idx []int) string {
	q := make([]string, len(idx))
	for i, c := range idx {
		q[i] = pgwire.QuoteIdentifier(t.cols[c])
	}
	return strings.Join(q, ", ")
}

func (t *tableInfo) orderCols() string { return t.columns(t.order) }

// where is the table's row filter and the bounds of the order's key: lo
// and after are lower bounds, included and not, hi an upper one, not
// included; each a JSON array of the key's columns as text.
func (t *tableInfo) where(lo, hi, after json.RawMessage) (string, error) {
	var conds []string
	if t.filter != "" {
		conds = append(conds, "("+t.filter+")")
	}
	for _, b := range []struct {
		v  json.RawMessage
		op string
	}{{lo, ">="}, {hi, "<"}, {after, ">"}} {
		if b.v == nil {
			continue
		}
		var vals []string
		if err := json.Unmarshal(b.v, &vals); err != nil || len(vals) != len(t.order) {
			return "", fmt.Errorf("postgres: %s is not a key of %s", b.v, t.ns)
		}
		lits := make([]string, len(vals))
		for i, v := range vals {
			lits[i] = pgwire.QuoteLiteral(v)
		}
		// Literals of no type take the columns' types; the comparison of
		// rows is the order's.
		conds = append(conds, "("+t.orderCols()+") "+b.op+" ("+strings.Join(lits, ", ")+")")
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), nil
}

// record makes a row's record, and the order's key of it as text.
func (t *tableInfo) record(fields []pgwire.Field, row [][]byte) (*fitsync.Record, json.RawMessage) {
	vals := make([]fitsync.Value, len(row))
	fs := make([]fitsync.Field, len(row))
	for i, v := range row {
		vals[i] = fitsync.Null()
		if v != nil {
			vals[i] = decodeText(fields[i].TypeOID, string(v))
		}
		fs[i] = fitsync.Field{Name: t.cols[i], Value: vals[i]}
	}
	rec := &fitsync.Record{Source: "postgres", Namespace: t.ns, Op: fitsync.OpInsert, Key: fitsync.Null(), After: fitsync.Map(fs...)}
	if len(t.key) > 0 {
		kf := make([]fitsync.Field, len(t.key))
		for i, c := range t.key {
			kf[i] = fitsync.Field{Name: t.cols[c], Value: vals[c]}
		}
		rec.Key = fitsync.Map(kf...)
	}
	var key json.RawMessage
	if t.order != nil {
		k := make([]string, len(t.order))
		for i, c := range t.order {
			k[i] = string(row[c])
		}
		key, _ = json.Marshal(k)
	}
	return rec, key
}

func (s *Source) chunkRows() int64 {
	if s.ChunkRows <= 0 {
		return DefaultChunkRows
	}
	return s.ChunkRows
}

func (s *Source) parallel() int {
	if s.SnapshotParallel <= 0 {
		return DefaultParallel
	}
	return s.SnapshotParallel
}

// planTable splits a table into chunks of chunk_rows rows by its order's
// key, at most room of them: the bounds are the keys of every
// chunk_rows-th row, found in one pass over the key's index.
func (s *Source) planTable(ctx context.Context, c sqlConn, t *tableInfo, room int) ([]fitsync.Chunk, error) {
	where, _ := t.where(nil, nil, nil)
	rows, err := query(ctx, c, "SELECT count(*) FROM "+t.from()+where)
	if err != nil {
		return nil, fmt.Errorf("postgres: counting %s: %w", t.ns, err)
	}
	var n int64
	if len(rows) == 1 && len(rows[0]) == 1 {
		n, _ = strconv.ParseInt(string(rows[0][0]), 10, 64)
	}
	per := max(s.chunkRows(), (n+int64(room)-1)/int64(max(room, 1)))
	if t.order == nil || n <= per {
		return []fitsync.Chunk{{Namespace: t.ns, Rows: n}}, nil
	}
	cols := t.orderCols()
	rows, err = query(ctx, c, "SELECT "+cols+" FROM (SELECT "+cols+", row_number() OVER (ORDER BY "+cols+") AS fit_rn FROM "+t.from()+where+
		") fit_s WHERE fit_rn % "+strconv.FormatInt(per, 10)+" = 1 AND fit_rn > 1 ORDER BY "+cols)
	if err != nil {
		return nil, fmt.Errorf("postgres: splitting %s: %w", t.ns, err)
	}
	var plan []fitsync.Chunk
	var lo json.RawMessage
	for _, r := range rows {
		k := make([]string, len(r))
		for i, v := range r {
			k[i] = string(v)
		}
		hi, _ := json.Marshal(k)
		plan = append(plan, fitsync.Chunk{Namespace: t.ns, Lo: lo, Hi: hi, Rows: per})
		lo = hi
	}
	return append(plan, fitsync.Chunk{Namespace: t.ns, Lo: lo, Rows: max(n-per*int64(len(plan)), 0)}), nil
}

// fetchBytes is about how much one FETCH brings, below the connection's
// bound of 64 MiB on a query's rows.
const fetchBytes = 16 << 20

// errSnapshotGone is an exported snapshot no longer there: its connection
// has ended.
var errSnapshotGone = errors.New("postgres: the exported snapshot is gone with the connection that exported it")

// readChunk reads chunk ch of t, after the key after when it is set, in a
// REPEATABLE READ transaction, in the exported snapshot snap when it is
// set; each record goes to each with its order's key.
func readChunk(ctx context.Context, c sqlConn, snap string, t *tableInfo, ch fitsync.Chunk, after json.RawMessage,
	each func(*fitsync.Record, json.RawMessage) error) error {
	if t.order == nil {
		after = nil // read whole, again
	}
	where, err := t.where(ch.Lo, ch.Hi, after)
	if err != nil {
		return err
	}
	order := ""
	if t.order != nil {
		order = " ORDER BY " + t.orderCols()
	}
	if _, err := c.SimpleQuery(ctx, "BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY"); err != nil {
		return err
	}
	if snap != "" {
		if _, err := c.SimpleQuery(ctx, "SET TRANSACTION SNAPSHOT "+pgwire.QuoteLiteral(snap)); err != nil {
			var pe *pgwire.Error
			if errors.As(err, &pe) && pe.Code == "22023" {
				return fmt.Errorf("%w: %v", errSnapshotGone, err)
			}
			return err
		}
	}
	all := make([]int, len(t.cols))
	for i := range all {
		all[i] = i
	}
	if _, err := c.SimpleQuery(ctx, "DECLARE fit_snapshot NO SCROLL CURSOR FOR SELECT "+t.columns(all)+" FROM "+t.from()+where+order); err != nil {
		return fmt.Errorf("postgres: reading %s: %w", t.ns, err)
	}
	n := 100
	for {
		res, err := c.SimpleQuery(ctx, "FETCH "+strconv.Itoa(n)+" FROM fit_snapshot")
		if err != nil {
			return fmt.Errorf("postgres: reading %s: %w", t.ns, err)
		}
		if len(res) == 0 || len(res[0].Rows) == 0 {
			break
		}
		size := 0
		for _, row := range res[0].Rows {
			if len(row) != len(t.cols) || len(res[0].Fields) != len(t.cols) {
				return fmt.Errorf("postgres: reading %s: a row of %d columns for %d", t.ns, len(row), len(t.cols))
			}
			for _, v := range row {
				size += len(v) + 4
			}
			if err := each(t.record(res[0].Fields, row)); err != nil {
				return err
			}
		}
		// As many rows as fit in fetchBytes at the size of these, 1 to
		// 1000.
		n = int(min(max(int64(fetchBytes)*int64(len(res[0].Rows))/int64(max(size, 1)), 1), 1000))
	}
	_, err = c.SimpleQuery(ctx, "COMMIT")
	return err
}

// exporter holds a snapshot exported at a point of the WAL: the run's own
// connection, idle, having made the slot (conn nil), or a connection of its
// own holding a temporary slot.
type exporter struct {
	id   string
	lsn  pgwire.LSN
	conn replConn
	// plan is the snapshot's, kept with a held exporter.
	plan  []snapChunk
	timer *time.Timer
}

func (e *exporter) close() {
	if e.conn != nil {
		e.conn.Close()
	}
}

// holdFor is how long an exporter is kept after a run fails, for the next.
var holdFor = 5 * time.Minute

// export takes a new snapshot: a temporary slot, made on a connection of
// its own, exports it at its consistent point; the slot goes with the
// connection.
func (s *Source) export(ctx context.Context) (*exporter, error) {
	c, err := dialSource(ctx, s.cfg)
	if err != nil {
		return nil, err
	}
	var b [6]byte
	rand.Read(b[:])
	slot, err := c.CreateReplicationSlot(ctx, "fit_snapshot_"+hex.EncodeToString(b[:]),
		pgwire.SlotOptions{Temporary: true, Plugin: "pgoutput", Snapshot: pgwire.SnapshotExport})
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("postgres: a temporary slot for the snapshot: %w", err)
	}
	return &exporter{id: slot.SnapshotName, lsn: slot.ConsistentPoint, conn: c}, nil
}

// hold keeps e for the next run, for holdFor at most.
func (s *Source) hold(e *exporter) {
	s.mu.Lock()
	old := s.held
	s.held = e
	e.timer = time.AfterFunc(holdFor, func() {
		s.mu.Lock()
		if s.held == e {
			s.held = nil
			e.close()
		}
		s.mu.Unlock()
	})
	s.mu.Unlock()
	if old != nil {
		old.close()
	}
}

// takeHeld returns the exporter held, no longer held.
func (s *Source) takeHeld() *exporter {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.held
	s.held = nil
	if e != nil {
		e.timer.Stop()
	}
	return e
}

func (s *Source) dropHeld() {
	if e := s.takeHeld(); e != nil {
		e.close()
	}
}

// Cleanup closes an exporter held.
func (s *Source) Cleanup() { s.dropHeld() }

// snapshotState is a snapshot's progress in the position: the exported
// snapshot, the tables in the order read with their chunks' counts, the
// chunks done, and where those under way are.
type snapshotState struct {
	ID     string      `json:"id"`
	Tables []snapTable `json:"tables"`
	Done   string      `json:"done,omitempty"`
	At     []chunkAt   `json:"at,omitempty"`
}

type snapTable struct {
	Name   string `json:"name"`
	Chunks int    `json:"chunks"`
}

type chunkAt struct {
	Chunk int             `json:"chunk"`
	Key   json.RawMessage `json:"key"`
}

// snapChunk is a chunk of the snapshot's plan.
type snapChunk struct {
	t *tableInfo
	c fitsync.Chunk
}

// formatRanges writes the indexes set, as "0-41,43".
func formatRanges(set []bool) string {
	var b strings.Builder
	for i := 0; i < len(set); i++ {
		if !set[i] {
			continue
		}
		j := i
		for j+1 < len(set) && set[j+1] {
			j++
		}
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.Itoa(i))
		if j > i {
			b.WriteString("-" + strconv.Itoa(j))
		}
		i = j
	}
	return b.String()
}

// parseRanges reads formatRanges's, of n indexes.
func parseRanges(s string, n int) ([]bool, error) {
	set := make([]bool, n)
	if s == "" {
		return set, nil
	}
	for part := range strings.SplitSeq(s, ",") {
		a, b, isRange := strings.Cut(part, "-")
		lo, err1 := strconv.Atoi(a)
		hi, err2 := lo, error(nil)
		if isRange {
			hi, err2 = strconv.Atoi(b)
		}
		if err1 != nil || err2 != nil || lo < 0 || hi < lo || hi >= n {
			return nil, fmt.Errorf("postgres: the snapshot's chunks done %q", s)
		}
		for i := lo; i <= hi; i++ {
			set[i] = true
		}
	}
	return set, nil
}

// unfinished returns the tables of st not every chunk of which is done.
func (st *snapshotState) unfinished() ([]string, error) {
	n := 0
	for _, t := range st.Tables {
		n += max(t.Chunks, 0)
	}
	done, err := parseRanges(st.Done, n)
	if err != nil {
		return nil, err
	}
	var out []string
	i := 0
	for _, t := range st.Tables {
		if slices.Contains(done[i:i+max(t.Chunks, 0)], false) || t.Chunks <= 0 {
			out = append(out, t.Name)
		}
		i += max(t.Chunks, 0)
	}
	return out, nil
}

// snapshot reads the tables before the stream, and returns where the
// stream starts. A new snapshot is the slot's just made, or a temporary
// slot's: the stream starts at its point, exactly after it. A snapshot
// under way resumes in its exported snapshot when this fit still holds it
// (the run before failed); otherwise the tables it had not finished are
// read again from a new one, and the stream starts at the first one's
// point, replaying over them the changes between the two.
func (s *Source) snapshot(ctx context.Context, r *fitsync.Run, st *setup, from pgwire.LSN, prev *snapshotState) (pgwire.LSN, error) {
	held := s.takeHeld()
	var exp *exporter
	var plan []snapChunk
	var done []bool
	at := map[int]json.RawMessage{}
	if prev != nil && held != nil && held.id == prev.ID {
		exp, plan = held, held.plan
		var err error
		if done, err = parseRanges(prev.Done, len(plan)); err != nil {
			held.close()
			return 0, err
		}
		for _, a := range prev.At {
			if a.Chunk >= 0 && a.Chunk < len(plan) {
				at[a.Chunk] = a.Key
			}
		}
	} else {
		if held != nil {
			held.close()
		}
		tables := st.names
		if prev != nil {
			left, err := prev.unfinished()
			if err != nil {
				return 0, err
			}
			tables = nil
			for _, t := range left {
				if st.tables[t] != nil {
					tables = append(tables, t)
				}
			}
			if len(tables) > 0 {
				logs.Named(config.LoggerSync).Warnf("sync postgres: the snapshot %s is gone with the run that exported it: %s read again from a new snapshot, and the changes since %v replayed over them, which sinks writing by key converge on",
					prev.ID, strings.Join(tables, ", "), from)
			}
		}
		if len(tables) == 0 {
			if prev == nil && st.slot != nil {
				from = st.slot.ConsistentPoint
			}
			return from, nil
		}
		switch {
		case prev == nil && st.slot != nil && st.slot.SnapshotName != "":
			exp = &exporter{id: st.slot.SnapshotName, lsn: st.slot.ConsistentPoint}
		default:
			var err error
			if exp, err = s.export(ctx); err != nil {
				return 0, err
			}
		}
		if prev == nil {
			from = exp.lsn
		}
		var err error
		if plan, err = s.planSnapshot(ctx, exp.id, tables, st); err != nil {
			exp.close()
			return 0, err
		}
		done = make([]bool, len(plan))
	}
	sr := &snapRun{plan: plan, done: done, at: at, lsn: from, id: exp.id, emit: r.Emit}
	for i, c := range plan {
		if i == 0 || plan[i-1].t != c.t {
			sr.tables = append(sr.tables, snapTable{Name: c.t.ns})
		}
		sr.tables[len(sr.tables)-1].Chunks++
	}
	// A new snapshot's start, none of it read, is the pipeline's
	// checkpoint before its first record: a run killed before the next
	// reads the tables again from another snapshot and streams from this
	// one's point, replaying the changes between the two over it, rather
	// than from the other's, which would lose them.
	err := r.SaveStart(sr.position())
	if err != nil {
		exp.close()
		return 0, err
	}
	err = sr.run(ctx, s, s.parallel())
	switch {
	case err == nil:
		exp.close()
		return from, nil
	case exp.conn != nil && !errors.Is(err, errSnapshotGone):
		exp.plan = plan
		s.hold(exp)
	default:
		exp.close()
	}
	return 0, err
}

// planSnapshot plans the tables' chunks in the exported snapshot snap.
func (s *Source) planSnapshot(ctx context.Context, snap string, tables []string, st *setup) ([]snapChunk, error) {
	c, err := dialSQL(ctx, s.cfg)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	if _, err := c.SimpleQuery(ctx, "BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY; SET TRANSACTION SNAPSHOT "+pgwire.QuoteLiteral(snap)); err != nil {
		return nil, err
	}
	var plan []snapChunk
	for _, name := range tables {
		t := st.tables[name]
		chunks, err := s.planTable(ctx, c, t, fitsync.MaxChunks)
		if err != nil {
			return nil, err
		}
		for _, ch := range chunks {
			plan = append(plan, snapChunk{t: t, c: ch})
		}
	}
	_, err = c.SimpleQuery(ctx, "COMMIT")
	return plan, err
}

// positionEvery is how often a record of a chunk carries the snapshot's
// position, besides each chunk's last.
const positionEvery = 100

// snapRun reads a snapshot's chunks, several at once, each record handed
// on once the next of its chunk is read, so that a chunk's last carries a
// position saying the chunk is done, and the snapshot's very last one the
// stream's start alone.
type snapRun struct {
	mu     sync.Mutex
	plan   []snapChunk
	done   []bool
	at     map[int]json.RawMessage
	next   int
	since  int
	lsn    pgwire.LSN
	id     string
	tables []snapTable
	emit   func(*fitsync.Record) error
}

func (sr *snapRun) run(ctx context.Context, s *Source, parallel int) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	var once sync.Once
	var first error
	for range min(parallel, len(sr.plan)) {
		wg.Go(func() {
			var c sqlConn
			defer func() {
				if c != nil {
					c.Close()
				}
			}()
			for {
				i, after, ok := sr.take()
				if !ok {
					return
				}
				err := error(nil)
				if c == nil {
					c, err = dialSQL(ctx, s.cfg)
				}
				if err == nil {
					err = sr.read(ctx, c, i, after)
				}
				if err != nil {
					once.Do(func() { first = err })
					cancel()
					return
				}
			}
		})
	}
	wg.Wait()
	return first
}

func (sr *snapRun) take() (int, json.RawMessage, bool) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	for sr.next < len(sr.plan) && sr.done[sr.next] {
		sr.next++
	}
	if sr.next == len(sr.plan) {
		return 0, nil, false
	}
	i := sr.next
	sr.next++
	return i, sr.at[i], true
}

func (sr *snapRun) read(ctx context.Context, c sqlConn, i int, after json.RawMessage) error {
	var held *fitsync.Record
	var heldKey json.RawMessage
	err := readChunk(ctx, c, sr.id, sr.plan[i].t, sr.plan[i].c, after, func(rec *fitsync.Record, key json.RawMessage) error {
		if held != nil {
			if err := sr.pass(i, held, heldKey, false); err != nil {
				return err
			}
		}
		held, heldKey = rec, key
		return nil
	})
	if err != nil {
		return err
	}
	return sr.pass(i, held, nil, true)
}

// pass emits rec, of chunk i, its last when last is true (nil when the
// chunk had none), with the position when it is due. Emitting under the
// lock keeps the positions in the order of the records.
func (sr *snapRun) pass(i int, rec *fitsync.Record, key json.RawMessage, last bool) error {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	if last {
		sr.done[i] = true
		delete(sr.at, i)
	} else if key != nil {
		sr.at[i] = key
	}
	if rec == nil {
		return nil
	}
	if sr.since++; last || sr.since >= positionEvery {
		rec.Position, sr.since = sr.position(), 0
	}
	return sr.emit(rec)
}

// position is the snapshot's as of now; the stream's start alone once
// every chunk is done.
func (sr *snapRun) position() fitsync.Position {
	p := sourcePosition{LSN: sr.lsn.String()}
	if slices.Contains(sr.done, false) {
		st := &snapshotState{ID: sr.id, Tables: sr.tables, Done: formatRanges(sr.done)}
		for _, i := range slices.Sorted(maps.Keys(sr.at)) {
			st.At = append(st.At, chunkAt{Chunk: i, Key: sr.at[i]})
		}
		p.Snapshot = st
	}
	b, _ := json.Marshal(p)
	return b
}

// chunkPosition is a job's chunk's position: the order's key of its last
// record.
type chunkPosition struct {
	Key json.RawMessage `json:"key"`
}

// Chunks plans a job's snapshot: the publication's tables (or tables, with
// no publication), each split into chunks of chunk_rows rows by its key.
// None with snapshot never, whose job reads the changes up to the server's
// end.
func (s *Source) Chunks(ctx context.Context) ([]fitsync.Chunk, error) {
	if s.Snapshot == "never" {
		return nil, nil
	}
	c, err := dialSQL(ctx, s.cfg)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	st, err := s.jobTables(ctx, c, true)
	if err != nil {
		return nil, err
	}
	var plan []fitsync.Chunk
	for i, name := range st.names {
		room := fitsync.MaxChunks - len(plan) - (len(st.names) - i - 1)
		chunks, err := s.planTable(ctx, c, st.tables[name], room)
		if err != nil {
			return nil, err
		}
		plan = append(plan, chunks...)
	}
	return plan, nil
}

// jobTables reads the tables a job reads, kept for its chunks; again reads
// them anew.
func (s *Source) jobTables(ctx context.Context, c sqlConn, again bool) (*setup, error) {
	s.mu.Lock()
	tables := s.tables
	s.mu.Unlock()
	if tables != nil && !again {
		return &setup{tables: tables}, nil
	}
	st, err := s.publication(ctx, c, false, true)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.tables = st.tables
	s.mu.Unlock()
	return st, nil
}

// ReadChunk reads a job's chunk in a transaction of its own, after from's
// key when from is set.
func (s *Source) ReadChunk(ctx context.Context, ch fitsync.Chunk, from fitsync.Position, emit func(*fitsync.Record) error) error {
	var after json.RawMessage
	if from != nil {
		var p chunkPosition
		if err := json.Unmarshal(from, &p); err != nil {
			return fmt.Errorf("postgres: the chunk's position %s: %w", from, err)
		}
		after = p.Key
	}
	c, err := dialSQL(ctx, s.cfg)
	if err != nil {
		return err
	}
	defer c.Close()
	st, err := s.jobTables(ctx, c, false)
	if err != nil {
		return err
	}
	t := st.tables[ch.Namespace]
	if t == nil {
		return fmt.Errorf("postgres: the job's table %s is no longer to be read", ch.Namespace)
	}
	return readChunk(ctx, c, "", t, ch, after, func(rec *fitsync.Record, key json.RawMessage) error {
		if key != nil {
			rec.Position, _ = json.Marshal(chunkPosition{Key: key})
		}
		return emit(rec)
	})
}
