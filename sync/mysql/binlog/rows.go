package binlog

import (
	"fmt"

	fitsync "github.com/lesismal/fit/sync"
)

// RowsKind is what a rows event does.
type RowsKind uint8

// The kinds of rows events.
const (
	Insert RowsKind = iota + 1
	Update
	Delete
)

func (k RowsKind) String() string {
	switch k {
	case Insert:
		return "insert"
	case Update:
		return "update"
	}
	return "delete"
}

// Rows flags.
const (
	// RowsStmtEnd marks a statement's last rows event.
	RowsStmtEnd = 0x0001
)

// dummyTableID is the table ID of the empty rows event MySQL may write to
// end a statement.
const dummyTableID = 1<<48 - 1

// Rows is a rows event: the rows one statement wrote, deleted or changed
// in one table.
type Rows struct {
	Kind    RowsKind
	TableID uint64
	// Table is the table map of TableID; nil for the dummy event that
	// ends a statement without rows.
	Table *TableMap
	Flags uint16
	// Extra is a v2 event's extra data, as written.
	Extra []byte
	// Present tells which columns the before image (of an update or a
	// delete) or the only image (of an insert) has, by index;
	// PresentAfter which an update's after image has. A column not
	// present (binlog_row_image MINIMAL or NOBLOB) is Null in Row.
	Present, PresentAfter []bool
	Rows                  []Row
}

// Row is one row of a rows event, its values by column index: Before for
// an update and a delete, After for an insert and an update. A partial
// JSON update's after value is the whole document, the before one with
// the changes applied.
type Row struct {
	Before, After []fitsync.Value
}

func rowsKind(t EventType) RowsKind {
	switch t {
	case WriteRowsEventV1, WriteRowsEventV2, MariaWriteRowsCompressedV1, MariaWriteRowsCompressedV2:
		return Insert
	case UpdateRowsEventV1, UpdateRowsEventV2, PartialUpdateRowsEvent, MariaUpdateRowsCompressedV1, MariaUpdateRowsCompressedV2:
		return Update
	}
	return Delete
}

func isV2(t EventType) bool {
	switch t {
	case WriteRowsEventV2, UpdateRowsEventV2, DeleteRowsEventV2, PartialUpdateRowsEvent,
		MariaWriteRowsCompressedV2, MariaUpdateRowsCompressedV2, MariaDeleteRowsCompressedV2:
		return true
	}
	return false
}

func isCompressed(t EventType) bool {
	return t >= MariaWriteRowsCompressedV1 && t <= MariaDeleteRowsCompressedV2
}

func (d *Decoder) decodeRows(t EventType, body []byte) (*Rows, error) {
	r := rd{b: body}
	rows := &Rows{Kind: rowsKind(t)}
	def := 8
	if isV2(t) {
		def = 10
	}
	if d.fd.postHeader(t, def) == 6 {
		rows.TableID = uint64(r.u32())
	} else {
		rows.TableID = r.uN(6)
	}
	rows.Flags = r.u16()
	if isV2(t) {
		n := int(r.u16())
		rows.Extra = append([]byte(nil), r.take(n-2)...)
	}
	n, _ := r.lenEnc()
	if r.bad {
		return nil, malformed("%s event", t)
	}
	if rows.TableID == dummyTableID && n == 0 {
		return rows, nil
	}
	tm := d.tables[rows.TableID]
	if tm == nil {
		return nil, fmt.Errorf("%w: table %d", ErrNoTableMap, rows.TableID)
	}
	rows.Table = tm
	if n != uint64(len(tm.Columns)) {
		return nil, malformed("%s event of %d columns for %s.%s of %d", t, n, tm.Schema, tm.Table, len(tm.Columns))
	}
	rows.Present = bitmap(r.take((int(n)+7)/8), int(n))
	if rows.Kind == Update {
		rows.PresentAfter = bitmap(r.take((int(n)+7)/8), int(n))
	}
	if r.bad {
		return nil, malformed("%s event", t)
	}
	data := r.rest()
	if isCompressed(t) {
		var err error
		if data, err = d.uncompress(data); err != nil {
			return nil, err
		}
	}
	dec := &rowDecoder{d: d, tm: tm, partial: t == PartialUpdateRowsEvent,
		budget: max(d.maxRows(), 2*len(body))}
	r = rd{b: data}
	for len(r.b) > 0 {
		var row Row
		var err error
		switch rows.Kind {
		case Insert:
			row.After, err = dec.image(&r, rows.Present, false)
		case Delete:
			row.Before, err = dec.image(&r, rows.Present, false)
		default:
			if row.Before, err = dec.image(&r, rows.Present, false); err == nil {
				row.After, err = dec.image(&r, rows.PresentAfter, dec.partial)
			}
		}
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", tm.Schema, tm.Table, err)
		}
		rows.Rows = append(rows.Rows, row)
	}
	return rows, nil
}

func (d *Decoder) maxRows() int {
	if d.MaxRowsSize > 0 {
		return d.MaxRowsSize
	}
	return DefaultMaxRowsSize
}

// bitmap reads n bits, the lowest first.
func bitmap(b []byte, n int) []bool {
	if len(b)*8 < n {
		return nil
	}
	m := make([]bool, n)
	for i := range m {
		m[i] = b[i/8]&(1<<(i%8)) != 0
	}
	return m
}

// rowDecoder decodes the rows of one event.
type rowDecoder struct {
	d       *Decoder
	tm      *TableMap
	partial bool
	budget  int
	// before is the raw JSON of the last before image's columns, which a
	// partial update's diffs apply to.
	before [][]byte
}

// image reads one image of a row: (after a partial update's value
// options and its partial bits) the null bits of the present columns,
// then their values.
func (dec *rowDecoder) image(r *rd, present []bool, after bool) ([]fitsync.Value, error) {
	start := len(r.b)
	cols := dec.tm.Columns
	var partialBits []bool
	if after {
		opts, _ := r.lenEnc()
		if opts&1 != 0 { // PARTIAL_JSON_UPDATES
			nj := 0
			for i, c := range cols {
				if present[i] && c.Type == TypeJSON {
					nj++
				}
			}
			partialBits = bitmap(r.take((nj+7)/8), nj)
		}
	}
	np := 0
	for _, p := range present {
		if p {
			np++
		}
	}
	nulls := bitmap(r.take((np+7)/8), np)
	if r.bad {
		return nil, malformed("a row's null bits")
	}
	keepJSON := dec.partial && !after
	if keepJSON {
		dec.before = dec.before[:0]
	}
	vals := make([]fitsync.Value, len(cols))
	dec.budget -= 32 * len(cols)
	k, j := 0, 0
	for i := range cols {
		if keepJSON {
			dec.before = append(dec.before, nil)
		}
		if !present[i] {
			continue
		}
		null := nulls[k]
		k++
		c := &cols[i]
		isPartial := false
		if c.Type == TypeJSON && partialBits != nil {
			isPartial = partialBits[j]
			j++
		}
		if null {
			continue
		}
		var v fitsync.Value
		var err error
		switch {
		case isPartial:
			v, err = dec.partialJSON(r, i)
		case keepJSON && c.Type == TypeJSON:
			var raw []byte
			v, raw, err = dec.json(r, c)
			dec.before[i] = raw
		default:
			v, err = dec.value(r, c)
		}
		if err != nil {
			return nil, fmt.Errorf("column %d (%s): %w", i, c.Name, err)
		}
		dec.budget -= len(v.Text())
		if dec.budget < 0 {
			return nil, fmt.Errorf("%w: a rows event's values past MaxRowsSize", ErrTooLarge)
		}
		vals[i] = v
	}
	if len(r.b) == start {
		return nil, malformed("an empty row")
	}
	return vals, nil
}
