package mysqlwire

import (
	"context"
	"fmt"
)

// Exec runs query and returns the result of its last statement. With
// args, the query is prepared, executed once with them and closed; without
// them it is sent as text.
func (c *Conn) Exec(ctx context.Context, query string, args ...any) (Result, error) {
	rows, err := c.Query(ctx, query, args...)
	if err != nil {
		return Result{}, err
	}
	err = rows.Close()
	return rows.res, err
}

// Query runs query and returns its rows, which must be read to their end
// or closed before the next call. With args, the query is prepared,
// executed with them and closed when the rows are.
func (c *Conn) Query(ctx context.Context, query string, args ...any) (*Rows, error) {
	if len(args) > 0 {
		st, err := c.Prepare(ctx, query)
		if err != nil {
			return nil, err
		}
		return st.query(ctx, args, true)
	}
	if err := c.begin(ctx); err != nil {
		return nil, err
	}
	b := c.startCommand(comQuery)
	b = append(b, query...)
	if err := c.writePacket(b); err != nil {
		return nil, c.fail(err)
	}
	return c.startRows(false, nil)
}

// Begin starts a transaction.
func (c *Conn) Begin(ctx context.Context) error {
	_, err := c.Exec(ctx, "START TRANSACTION")
	return err
}

// Commit commits the transaction.
func (c *Conn) Commit(ctx context.Context) error {
	_, err := c.Exec(ctx, "COMMIT")
	return err
}

// Rollback rolls the transaction back.
func (c *Conn) Rollback(ctx context.Context) error {
	_, err := c.Exec(ctx, "ROLLBACK")
	return err
}

// Rows is a call's result: one or more result sets, each of rows or of a
// Result alone. Next reads a row; Values are valid until the next call of
// Next, the []byte among them pointing into the connection's buffer.
type Rows struct {
	c         *Conn
	binary    bool
	cols      []Column
	vals      []any
	res       Result
	err       error
	inSet     bool // reading the rows of a result set
	ended     bool // the call's last result set is read
	closeStmt *Stmt
}

// startRows reads the first result of a call begun and sent; st, when
// set, is closed when the call ends.
func (c *Conn) startRows(binary bool, st *Stmt) (*Rows, error) {
	r := &Rows{c: c, binary: binary, closeStmt: st}
	c.rows = r
	r.readHeader()
	r.settle()
	if r.err != nil {
		return nil, r.err
	}
	return r, nil
}

// settle ends the call once its last result is read.
func (r *Rows) settle() {
	if r.c.rows == r && r.ended && !r.inSet {
		r.finish()
	}
}

// readHeader reads what starts a result: an OK, an ERR or the count of
// columns and their definitions.
func (r *Rows) readHeader() {
	c := r.c
	r.cols = r.cols[:0]
	r.inSet = false
	p, err := c.readPacket()
	if err != nil {
		r.err = c.fail(err)
		return
	}
	switch first(p) {
	case 0x00:
		r.res, r.err = c.parseOK(p)
		r.ended = r.res.Status&StatusMoreResultsExist == 0
		return
	case 0xff:
		r.err = c.parseErr(p)
		r.ended = true
		return
	case 0xfb:
		// LOAD DATA LOCAL INFILE, which the client did not offer: an empty
		// file, and the server's answer to it.
		if err := c.writePacket(c.startPacket()); err != nil {
			r.err = c.fail(err)
			return
		}
		r.readHeader()
		if r.err == nil {
			r.err = fmt.Errorf("mysql: LOAD DATA LOCAL is not supported")
		}
		r.inSet, r.ended = false, true
		return
	}
	rr := rd{b: p}
	n, _ := rr.lenEnc()
	if rr.bad || n == 0 || n > 4096 {
		r.err = c.fail(fmt.Errorf("%w: column count", ErrMalformed))
		return
	}
	if r.cols, r.err = c.readColumns(int(n), r.cols[:0]); r.err != nil {
		return
	}
	r.inSet = true
	r.res = Result{}
}

// readColumns reads n column definitions and, without DEPRECATE_EOF, the
// EOF after them.
func (c *Conn) readColumns(n int, cols []Column) ([]Column, error) {
	for range n {
		p, err := c.readPacket()
		if err != nil {
			return nil, c.fail(err)
		}
		col, ok := parseColumn(p)
		if !ok {
			return nil, c.fail(fmt.Errorf("%w: column definition", ErrMalformed))
		}
		cols = append(cols, col)
	}
	if c.caps&capDeprecateEOF == 0 {
		p, err := c.readPacket()
		if err != nil {
			return nil, c.fail(err)
		}
		if first(p) != 0xfe {
			return nil, c.fail(fmt.Errorf("%w: no EOF after the columns", ErrMalformed))
		}
	}
	return cols, nil
}

// Columns are the current result set's columns; none for a Result alone.
func (r *Rows) Columns() []Column { return r.cols }

// Values are the current row's values, by column.
func (r *Rows) Values() []any { return r.vals }

// Result is the current result set's: what a statement without rows
// answered, or the status and warnings at the end of the rows.
func (r *Rows) Result() Result { return r.res }

// Err is the error that ended the rows, if any.
func (r *Rows) Err() error { return r.err }

// Next reads the next row of the current result set.
func (r *Rows) Next() bool {
	if !r.inSet || r.err != nil {
		return false
	}
	c := r.c
	if c.broken != nil {
		r.err, r.inSet = c.broken, false
		return false
	}
	p, err := c.readPacket()
	if err != nil {
		r.err = c.fail(err)
		r.inSet = false
		return false
	}
	if len(p) == 0 {
		r.err = c.fail(fmt.Errorf("%w: empty row", ErrMalformed))
		r.inSet = false
		return false
	}
	switch {
	case p[0] == 0xfe && (len(p) < 9 || c.caps&capDeprecateEOF != 0 && len(p) < maxPayload):
		r.inSet = false
		r.res, r.err = c.parseOK(p)
		r.ended = r.res.Status&StatusMoreResultsExist == 0
		r.settle()
		return false
	case p[0] == 0xff:
		r.inSet = false
		r.err = c.parseErr(p)
		r.ended = true
		r.settle()
		return false
	}
	if r.binary {
		r.err = r.binaryRow(p)
	} else {
		r.err = r.textRow(p)
	}
	if r.err != nil {
		r.inSet = false
		r.err = c.fail(r.err)
		return false
	}
	return true
}

func (r *Rows) textRow(p []byte) error {
	rr := rd{b: p}
	r.vals = r.vals[:0]
	for i := range r.cols {
		b := rr.lenEncBytes()
		if rr.bad {
			return fmt.Errorf("%w: text row", ErrMalformed)
		}
		v, err := textValue(&r.cols[i], b)
		if err != nil {
			return fmt.Errorf("%w: column %s: %v", ErrMalformed, r.cols[i].Name, err)
		}
		r.vals = append(r.vals, v)
	}
	if len(rr.b) != 0 {
		return fmt.Errorf("%w: text row longer than its columns", ErrMalformed)
	}
	return nil
}

func (r *Rows) binaryRow(p []byte) error {
	if p[0] != 0x00 {
		return fmt.Errorf("%w: binary row header 0x%x", ErrMalformed, p[0])
	}
	rr := rd{b: p[1:]}
	nulls := rr.take((len(r.cols) + 7 + 2) / 8)
	r.vals = r.vals[:0]
	for i := range r.cols {
		bit := i + 2
		if nulls != nil && nulls[bit/8]&(1<<(bit%8)) != 0 {
			r.vals = append(r.vals, nil)
			continue
		}
		v := binaryValue(&r.cols[i], &rr)
		if rr.bad {
			return fmt.Errorf("%w: binary row, column %s", ErrMalformed, r.cols[i].Name)
		}
		r.vals = append(r.vals, v)
	}
	if rr.bad || len(rr.b) != 0 {
		return fmt.Errorf("%w: binary row longer than its columns", ErrMalformed)
	}
	return nil
}

// NextResultSet moves to the next result of the call, reading what is
// left of the current one; false when there is none, or on an error.
func (r *Rows) NextResultSet() bool {
	for r.inSet && r.Next() {
	}
	if r.err != nil || r.ended {
		return false
	}
	r.readHeader()
	r.settle()
	return r.err == nil
}

// finish ends the call: the connection is free, and a statement prepared
// for the rows alone is closed.
func (r *Rows) finish() {
	c := r.c
	c.rows = nil
	if st := r.closeStmt; st != nil {
		r.closeStmt = nil
		if err := st.close(); err != nil && r.err == nil {
			r.err = err
		}
	}
	c.end()
}

// Close reads the rows and results left, ending the call, and returns
// the error that ended them.
func (r *Rows) Close() error {
	for r.err == nil && !(r.ended && !r.inSet) {
		if r.inSet {
			for r.Next() {
			}
			continue
		}
		r.readHeader()
	}
	r.settle()
	return r.err
}
