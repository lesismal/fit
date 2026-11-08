package syncpostgres

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	fitsync "github.com/lesismal/fit/sync"
	pgwire "github.com/lesismal/fit/sync/postgres/wire"
)

// pgoutput's messages (protocol 1 and 2), as the stream's XLogData carry
// them: https://www.postgresql.org/docs/current/protocol-logicalrep-message-formats.html.

// msgReader reads a message's fields, big-endian; a short message sets bad.
type msgReader struct {
	b   []byte
	bad bool
}

func (r *msgReader) take(n int) []byte {
	if r.bad || len(r.b) < n {
		r.bad = true
		return make([]byte, n)
	}
	v := r.b[:n]
	r.b = r.b[n:]
	return v
}

func (r *msgReader) u8() byte    { return r.take(1)[0] }
func (r *msgReader) u16() uint16 { return binary.BigEndian.Uint16(r.take(2)) }
func (r *msgReader) u32() uint32 { return binary.BigEndian.Uint32(r.take(4)) }
func (r *msgReader) u64() uint64 { return binary.BigEndian.Uint64(r.take(8)) }

func (r *msgReader) cstr() string {
	for i, c := range r.b {
		if c == 0 {
			s := string(r.b[:i])
			r.b = r.b[i+1:]
			return s
		}
	}
	r.bad = true
	return ""
}

// pgTime reads a timestamp of the protocol: microseconds since 2000-01-01.
func pgTime(us uint64) time.Time {
	return time.UnixMicro(int64(us) + 946684800000000).UTC()
}

// relCol is a column of a relation message.
type relCol struct {
	name string
	oid  uint32
	// ident says the column is in the replica identity.
	ident bool
}

// relation is a table as a relation message describes it, with the columns
// its records' keys are made of.
type relation struct {
	ns    string // schema.table
	ident byte   // d, n, f or i
	cols  []relCol
	key   []int
}

// tupleCol is a column of a tuple: its kind (n null, u an unchanged TOASTed
// value not sent, t text, b binary) and its data.
type tupleCol struct {
	kind byte
	data []byte
}

func (r *msgReader) tuple() []tupleCol {
	n := int(r.u16())
	if r.bad {
		return nil
	}
	cols := make([]tupleCol, 0, min(n, len(r.b)))
	for i := 0; i < n && !r.bad; i++ {
		c := tupleCol{kind: r.u8()}
		if c.kind == 't' || c.kind == 'b' {
			c.data = r.take(int(r.u32()))
		}
		cols = append(cols, c)
	}
	return cols
}

var errShort = errors.New("postgres: a pgoutput message cut short")

// decoder turns pgoutput's messages into records, a transaction at a time:
// it holds back a transaction's latest record until the next, or the
// commit, says whether it is the last.
type decoder struct {
	rels map[uint32]*relation
	// pk are the primary keys' columns by namespace, from the catalog: a
	// table of REPLICA IDENTITY FULL is keyed by them, not by every column.
	pk map[string][]string
	// from is the checkpoint: a transaction committed before it was read.
	from pgwire.LSN
	emit func(*fitsync.Record) error

	// The transaction being read.
	inTx    bool
	skip    bool
	xid     uint32
	at      time.Time
	n       int
	pending *fitsync.Record

	// Streamed transactions, by xid, and the one a stream's segment is of.
	spools   map[uint32]*spool
	cur      *spool
	spoolDir string

	// onCommit is told the end of each transaction read and whether it had
	// records.
	onCommit func(end pgwire.LSN, emitted bool)
}

// handle takes one XLogData's message.
func (d *decoder) handle(msg []byte) error {
	if len(msg) == 0 {
		return errShort
	}
	t := msg[0]
	if d.cur != nil && t != 'S' && t != 'E' && t != 'c' && t != 'A' {
		return d.cur.add(msg)
	}
	r := &msgReader{b: msg[1:]}
	var err error
	switch t {
	case 'B':
		final, ts, xid := pgwire.LSN(r.u64()), r.u64(), r.u32()
		if r.bad {
			return errShort
		}
		d.begin(xid, final, pgTime(ts))
		return nil
	case 'C':
		r.u8()
		r.u64()
		end, ts := pgwire.LSN(r.u64()), r.u64()
		if r.bad {
			return errShort
		}
		d.at = pgTime(ts)
		return d.commit(end)
	case 'S':
		xid := r.u32()
		r.u8()
		if r.bad {
			return errShort
		}
		if d.cur = d.spools[xid]; d.cur == nil {
			if d.cur, err = newSpool(d.spoolDir, xid); err != nil {
				return err
			}
			d.spools[xid] = d.cur
		}
		return nil
	case 'E':
		d.cur = nil
		return nil
	case 'c':
		xid := r.u32()
		r.u8()
		commit, end, ts := pgwire.LSN(r.u64()), pgwire.LSN(r.u64()), r.u64()
		if r.bad {
			return errShort
		}
		return d.streamCommit(xid, commit, end, pgTime(ts))
	case 'A':
		xid, sub := r.u32(), r.u32()
		if r.bad {
			return errShort
		}
		if sp := d.spools[xid]; sp != nil {
			if sub == xid {
				sp.close()
				delete(d.spools, xid)
			} else {
				sp.aborted[sub] = true
			}
		}
		return nil
	}
	return d.change(t, r, false, nil)
}

func (d *decoder) begin(xid uint32, final pgwire.LSN, at time.Time) {
	d.inTx, d.xid, d.at, d.n, d.pending = true, xid, at, 0, nil
	// A transaction committed before the checkpoint was read; the server
	// starts at the slot's confirmed position, which may be earlier.
	d.skip = final < d.from
}

// commit ends the transaction: its last record goes with the position.
func (d *decoder) commit(end pgwire.LSN) error {
	p := d.pending
	d.inTx, d.pending = false, nil
	if p != nil {
		p.Tx.Last = true
		p.Position = lsnPosition(end)
		if err := d.emit(p); err != nil {
			return err
		}
	}
	d.onCommit(end, p != nil)
	return nil
}

// change takes a relation, type, origin, insert, update, delete, truncate
// or logical message; streamed ones begin with their (sub)transaction's
// xid, which aborted says to drop.
func (d *decoder) change(t byte, r *msgReader, streamed bool, aborted map[uint32]bool) error {
	switch t {
	case 'O': // origin
		return nil
	case 'R', 'Y', 'I', 'U', 'D', 'T', 'M':
	default:
		return fmt.Errorf("postgres: an unknown pgoutput message %q", t)
	}
	if streamed {
		if xid := r.u32(); aborted[xid] {
			return nil
		}
	}
	switch t {
	case 'R':
		return d.relation(r)
	case 'Y', 'M': // a type's name; a logical message (not asked for)
		return nil
	case 'T':
		n := int(r.u32())
		opts := r.u8()
		if r.bad || n > len(r.b)/4 {
			return errShort
		}
		for range n {
			rel, err := d.rel(r.u32())
			if err != nil {
				return err
			}
			stmt := "TRUNCATE " + rel.ns
			if opts&2 != 0 {
				stmt += " RESTART IDENTITY"
			}
			if opts&1 != 0 {
				stmt += " CASCADE"
			}
			if err := d.add(&fitsync.Record{Namespace: rel.ns, Op: fitsync.OpDDL,
				After: fitsync.Map(fitsync.Field{Name: "statement", Value: fitsync.String(stmt)},
					fitsync.Field{Name: "truncate", Value: fitsync.Bool(true)})}); err != nil {
				return err
			}
		}
		return nil
	}
	rel, err := d.rel(r.u32())
	if err != nil {
		return err
	}
	var old, new []tupleCol
	var oldKind byte
	switch t {
	case 'I':
		if r.u8() != 'N' {
			return d.bad(t)
		}
		new = r.tuple()
	case 'U':
		k := r.u8()
		if k == 'K' || k == 'O' {
			oldKind, old = k, r.tuple()
			k = r.u8()
		}
		if k != 'N' {
			return d.bad(t)
		}
		new = r.tuple()
	case 'D':
		oldKind = r.u8()
		if oldKind != 'K' && oldKind != 'O' {
			return d.bad(t)
		}
		old = r.tuple()
	}
	if r.bad || (new != nil && len(new) != len(rel.cols)) || (old != nil && len(old) != len(rel.cols)) {
		return d.bad(t)
	}
	before, beforeKey, err := rel.row(old, oldKind == 'K', nil)
	if err != nil {
		return err
	}
	full := old
	if oldKind != 'O' {
		full = nil
	}
	after, afterKey, err := rel.row(new, false, full)
	if err != nil {
		return err
	}
	switch t {
	case 'I':
		return d.add(&fitsync.Record{Namespace: rel.ns, Op: fitsync.OpInsert, Key: afterKey, After: after})
	case 'D':
		return d.add(&fitsync.Record{Namespace: rel.ns, Op: fitsync.OpDelete, Key: beforeKey, Before: before})
	}
	if old != nil && !beforeKey.IsNull() && !beforeKey.Equal(afterKey) {
		// The key changed: the old row goes, the new one comes, so that a
		// sink writing by key does not keep both.
		if err := d.add(&fitsync.Record{Namespace: rel.ns, Op: fitsync.OpDelete, Key: beforeKey, Before: before}); err != nil {
			return err
		}
		return d.add(&fitsync.Record{Namespace: rel.ns, Op: fitsync.OpInsert, Key: afterKey, After: after})
	}
	return d.add(&fitsync.Record{Namespace: rel.ns, Op: fitsync.OpUpdate, Key: afterKey, Before: before, After: after})
}

func (d *decoder) bad(t byte) error {
	return fmt.Errorf("postgres: a malformed pgoutput message %q", t)
}

func (d *decoder) rel(oid uint32) (*relation, error) {
	rel := d.rels[oid]
	if rel == nil {
		return nil, fmt.Errorf("postgres: a change of relation %d before its relation message", oid)
	}
	return rel, nil
}

// relation reads a relation message.
func (d *decoder) relation(r *msgReader) error {
	oid := r.u32()
	schema, name := r.cstr(), r.cstr()
	rel := &relation{ns: schema + "." + name, ident: r.u8()}
	n := int(r.u16())
	if r.bad || n > len(r.b) {
		return errShort
	}
	for range n {
		flags := r.u8()
		c := relCol{name: r.cstr(), oid: r.u32(), ident: flags&1 != 0}
		r.u32() // typmod
		rel.cols = append(rel.cols, c)
	}
	if r.bad {
		return errShort
	}
	// The primary key keys a table, or, without one, or with REPLICA
	// IDENTITY USING INDEX, the replica identity's columns.
	if pk := d.pk[rel.ns]; len(pk) > 0 && rel.ident != 'i' {
		for _, name := range pk {
			for i, c := range rel.cols {
				if c.name == name {
					rel.key = append(rel.key, i)
				}
			}
		}
		if len(rel.key) != len(pk) {
			rel.key = nil
		}
	}
	if rel.key == nil {
		for i, c := range rel.cols {
			if c.ident {
				rel.key = append(rel.key, i)
			}
		}
	}
	d.rels[oid] = rel
	return nil
}

// row makes a tuple's row and key: a key tuple's row holds the identity's
// columns alone; an unchanged TOASTed value is taken from old when it has
// it, and left out otherwise.
func (rel *relation) row(tup []tupleCol, keyOnly bool, old []tupleCol) (row, key fitsync.Value, err error) {
	if tup == nil {
		return fitsync.Null(), fitsync.Null(), nil
	}
	fields := make([]fitsync.Field, 0, len(tup))
	vals := make([]fitsync.Value, len(tup))
	have := make([]bool, len(tup))
	for i, c := range tup {
		col := rel.cols[i]
		if keyOnly && !col.ident {
			continue
		}
		if c.kind == 'u' && old != nil && old[i].kind != 'u' {
			c = old[i]
		}
		switch c.kind {
		case 'n':
			vals[i] = fitsync.Null()
		case 't':
			vals[i] = decodeText(col.oid, string(c.data))
		case 'u':
			continue
		default:
			return fitsync.Value{}, fitsync.Value{}, fmt.Errorf("postgres: a column of %s in a format %q not asked for", rel.ns, c.kind)
		}
		have[i] = true
		fields = append(fields, fitsync.Field{Name: col.name, Value: vals[i]})
	}
	if len(rel.key) == 0 {
		return fitsync.Map(fields...), fitsync.Null(), nil
	}
	kf := make([]fitsync.Field, 0, len(rel.key))
	for _, i := range rel.key {
		if !have[i] {
			return fitsync.Map(fields...), fitsync.Null(), nil
		}
		kf = append(kf, fitsync.Field{Name: rel.cols[i].name, Value: vals[i]})
	}
	return fitsync.Map(fields...), fitsync.Map(kf...), nil
}

// add hands on the transaction's previous record and holds r back.
func (d *decoder) add(r *fitsync.Record) error {
	if !d.inTx {
		return errors.New("postgres: a change outside a transaction")
	}
	if d.skip {
		return nil
	}
	r.Source, r.CommitTime = "postgres", d.at
	r.Tx = fitsync.Tx{ID: strconv.FormatUint(uint64(d.xid), 10), First: d.n == 0}
	d.n++
	if p := d.pending; p != nil {
		d.pending = r
		return d.emit(p)
	}
	d.pending = r
	return nil
}

// streamCommit reads a streamed transaction from its spool, as one
// transaction.
func (d *decoder) streamCommit(xid uint32, commit, end pgwire.LSN, at time.Time) error {
	sp := d.spools[xid]
	delete(d.spools, xid)
	d.begin(xid, commit, at)
	if sp != nil {
		defer sp.close()
		if err := sp.each(func(msg []byte) error {
			return d.change(msg[0], &msgReader{b: msg[1:]}, true, sp.aborted)
		}); err != nil {
			return err
		}
	}
	return d.commit(end)
}

// close removes the spools left.
func (d *decoder) close() {
	for xid, sp := range d.spools {
		sp.close()
		delete(d.spools, xid)
	}
	d.cur = nil
}

// spool keeps a streamed transaction's messages in a file until its commit
// or abort, as the server sends them before it knows which.
type spool struct {
	f       *os.File
	w       *bufio.Writer
	aborted map[uint32]bool
}

func newSpool(dir string, xid uint32) (*spool, error) {
	f, err := os.CreateTemp(dir, fmt.Sprintf("fit-sync-pg-%d-*", xid))
	if err != nil {
		return nil, fmt.Errorf("postgres: a streamed transaction's spool: %w", err)
	}
	return &spool{f: f, w: bufio.NewWriterSize(f, 64<<10), aborted: map[uint32]bool{}}, nil
}

func (sp *spool) add(msg []byte) error {
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(msg)))
	sp.w.Write(n[:])
	if _, err := sp.w.Write(msg); err != nil {
		return fmt.Errorf("postgres: a streamed transaction's spool: %w", err)
	}
	return nil
}

// each reads the messages back, in order.
func (sp *spool) each(fn func([]byte) error) error {
	if err := sp.w.Flush(); err != nil {
		return fmt.Errorf("postgres: a streamed transaction's spool: %w", err)
	}
	if _, err := sp.f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("postgres: a streamed transaction's spool: %w", err)
	}
	br := bufio.NewReaderSize(sp.f, 64<<10)
	var n [4]byte
	var buf []byte
	for {
		if _, err := io.ReadFull(br, n[:]); err == io.EOF {
			return nil
		} else if err != nil {
			return fmt.Errorf("postgres: a streamed transaction's spool: %w", err)
		}
		size := int(binary.BigEndian.Uint32(n[:]))
		if cap(buf) < size {
			buf = make([]byte, size)
		}
		buf = buf[:size]
		if _, err := io.ReadFull(br, buf); err != nil || size == 0 {
			return fmt.Errorf("postgres: a streamed transaction's spool cut short: %v", err)
		}
		if err := fn(buf); err != nil {
			return err
		}
	}
}

func (sp *spool) close() {
	sp.f.Close()
	os.Remove(sp.f.Name())
}
