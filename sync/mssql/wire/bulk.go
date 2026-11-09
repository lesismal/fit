package mssqlwire

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// BulkOptions are a bulk load's hints, as INSERT BULK takes them.
type BulkOptions struct {
	CheckConstraints bool
	FireTriggers     bool
	KeepNulls        bool
	TabLock          bool
	// RowsPerBatch tells the server how many rows to expect; 0 says
	// nothing.
	RowsPerBatch int
	// Order names the columns the rows are sorted by, each with ASC or DESC
	// if it is not ascending, as the table's clustered index is: the
	// server then skips its sort.
	Order []string
}

// Bulk is a bulk load under way: rows written with WriteRow go to the
// server in BULK LOAD packets as they fill, and Close ends the load. The
// connection takes no other call until Close.
type Bulk struct {
	c    *Conn
	ctx  context.Context
	cols []Column
	w    *msgWriter
	buf  []byte
	rows int64
	err  error
}

// QuoteName quotes a name as SQL Server's QUOTENAME does: in brackets, a ]
// doubled.
func QuoteName(s string) string { return "[" + strings.ReplaceAll(s, "]", "]]") + "]" }

// BulkInsert starts a bulk load of table, a name as SQL writes it
// (dbo.orders, [my db].dbo.[order]), into columns, by name; the columns'
// types are the table's, read first.
func (c *Conn) BulkInsert(ctx context.Context, table string, columns []string, opts BulkOptions) (*Bulk, error) {
	if len(columns) == 0 {
		return nil, errors.New("mssql: a bulk load of no columns")
	}
	names := make([]string, len(columns))
	for i, n := range columns {
		names[i] = QuoteName(n)
	}
	cols, _, err := c.QueryAll(ctx, "SELECT TOP 0 "+strings.Join(names, ", ")+" FROM "+table)
	if err != nil {
		return nil, err
	}
	if len(cols) != len(names) {
		return nil, fmt.Errorf("mssql: %s has %d of the %d columns named", table, len(cols), len(names))
	}
	collations := map[string]string{}
	for _, col := range cols {
		if col.Collation != (Collation{}) {
			_, rows, err := c.QueryAll(ctx, "SELECT name, collation_name FROM sys.columns WHERE object_id = OBJECT_ID(@p1) AND collation_name IS NOT NULL", table)
			if err != nil {
				return nil, err
			}
			for _, r := range rows {
				collations[fmt.Sprint(r[0])] = fmt.Sprint(r[1])
			}
			break
		}
	}
	decls := make([]string, len(cols))
	for i := range cols {
		if cols[i].Type == TypeVariant {
			return nil, fmt.Errorf("mssql: the bulk load of the sql_variant column %s is not supported", cols[i].Name)
		}
		// The server takes text, ntext, image and xml in a bulk load as
		// the max types, which it converts.
		switch cols[i].Type {
		case TypeText:
			cols[i].Type, cols[i].Size = TypeBigVarChar, -1
		case TypeNText:
			cols[i].Type, cols[i].Size = TypeNVarChar, -1
		case TypeImage:
			cols[i].Type, cols[i].Size = TypeBigVarBinary, -1
		case TypeXML:
			cols[i].Type, cols[i].Size, cols[i].Collation = TypeNVarChar, -1, c.collation
		}
		decls[i] = names[i] + " " + cols[i].TypeName()
		if coll, ok := collations[cols[i].Name]; ok && cols[i].Collation != (Collation{}) {
			decls[i] += " COLLATE " + coll
		}
	}
	var hints []string
	for _, h := range []struct {
		on   bool
		name string
	}{{opts.CheckConstraints, "CHECK_CONSTRAINTS"}, {opts.FireTriggers, "FIRE_TRIGGERS"}, {opts.KeepNulls, "KEEP_NULLS"}, {opts.TabLock, "TABLOCK"}} {
		if h.on {
			hints = append(hints, h.name)
		}
	}
	if opts.RowsPerBatch > 0 {
		hints = append(hints, "ROWS_PER_BATCH = "+strconv.Itoa(opts.RowsPerBatch))
	}
	if len(opts.Order) > 0 {
		order := make([]string, len(opts.Order))
		for i, o := range opts.Order {
			name, dir, _ := strings.Cut(strings.TrimSpace(o), " ")
			dir = strings.ToUpper(strings.TrimSpace(dir))
			if dir != "" && dir != "ASC" && dir != "DESC" {
				return nil, fmt.Errorf("mssql: the order %q is not a column and ASC or DESC", o)
			}
			order[i] = strings.TrimSpace(QuoteName(name) + " " + dir)
		}
		hints = append(hints, "ORDER("+strings.Join(order, ", ")+")")
	}
	sql := "INSERT BULK " + table + " (" + strings.Join(decls, ", ") + ")"
	if len(hints) > 0 {
		sql += " WITH (" + strings.Join(hints, ", ") + ")"
	}
	if _, err := c.Exec(ctx, sql); err != nil {
		return nil, err
	}
	b := &Bulk{c: c, ctx: ctx, cols: cols, w: c.newWriter(pktBulk)}
	b.w.ctx = ctx
	meta := binary.LittleEndian.AppendUint16([]byte{tokColMetadata}, uint16(len(cols)))
	for i := range cols {
		meta = binary.LittleEndian.AppendUint32(meta, 0)
		meta = binary.LittleEndian.AppendUint16(meta, cols[i].Flags)
		meta = appendTypeInfo(meta, &cols[i], true)
		meta = appendBVarchar(meta, cols[i].Name)
	}
	if _, err := b.w.Write(meta); err != nil {
		return nil, err
	}
	return b, nil
}

// Columns are the load's columns, as the table has them.
func (b *Bulk) Columns() []Column { return b.cols }

// WriteRow adds a row, a value for each column. A value the column cannot
// take is an error that leaves the load going, without the row; an error
// sending it ends the load and closes the connection.
func (b *Bulk) WriteRow(values ...any) error {
	if b.err != nil {
		return b.err
	}
	if len(values) != len(b.cols) {
		return fmt.Errorf("mssql: a row of %d values for %d columns", len(values), len(b.cols))
	}
	buf := append(b.buf[:0], tokRow)
	for i, v := range values {
		var err error
		if buf, err = appendValue(buf, &b.cols[i], v); err != nil {
			return fmt.Errorf("mssql: the column %s: %w", b.cols[i].Name, err)
		}
	}
	b.buf = buf
	if _, err := b.w.Write(buf); err != nil {
		b.err = err
		return err
	}
	b.rows++
	return nil
}

// Close ends the load and returns the rows the server took.
func (b *Bulk) Close() (int64, error) {
	if b.err != nil {
		return 0, b.err
	}
	b.err = errors.New("mssql: the bulk load is closed")
	done := []byte{tokDone, 0, 0, 0, 0}
	done = binary.LittleEndian.AppendUint64(done, uint64(b.rows))
	if _, err := b.w.Write(done); err != nil {
		return 0, err
	}
	if err := b.w.close(); err != nil {
		return 0, err
	}
	k := b.c.watch(b.ctx)
	err := k.drain()
	return k.affected, err
}
