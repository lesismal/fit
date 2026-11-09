package mysqlwire

import (
	"context"
	"encoding/binary"
	"fmt"
)

// Stmt is a prepared statement of its connection.
type Stmt struct {
	c  *Conn
	id uint32
	// nparams is the count of the parameters, whose definitions the
	// server sends and the client does not keep: InsertRows' statements
	// have thousands, and nothing reads them.
	nparams int
	cols    []Column
	closed  bool
}

// Prepare prepares query, its parameters marked by ?.
func (c *Conn) Prepare(ctx context.Context, query string) (*Stmt, error) {
	if err := c.begin(ctx); err != nil {
		return nil, err
	}
	defer c.end()
	b := c.startCommand(comStmtPrepare)
	b = append(b, query...)
	if err := c.writePacket(b); err != nil {
		return nil, c.fail(err)
	}
	p, err := c.readPacket()
	if err != nil {
		return nil, c.fail(err)
	}
	switch first(p) {
	case 0xff:
		return nil, c.parseErr(p)
	case 0x00:
	default:
		return nil, c.fail(fmt.Errorf("%w: answer 0x%x to a prepare", ErrMalformed, first(p)))
	}
	r := rd{b: p[1:]}
	st := &Stmt{c: c, id: r.u32()}
	ncols, nparams := int(r.u16()), int(r.u16())
	if r.bad {
		return nil, c.fail(fmt.Errorf("%w: prepare OK", ErrMalformed))
	}
	if nparams > 0 {
		if err = c.skipColumns(nparams); err != nil {
			return nil, err
		}
		st.nparams = nparams
	}
	if ncols > 0 {
		if st.cols, err = c.readColumns(ncols, nil); err != nil {
			return nil, err
		}
	}
	return st, nil
}

// NumParams is the count of the statement's parameters.
func (s *Stmt) NumParams() int { return s.nparams }

// Columns are the columns the statement's rows have, as the server
// described them when preparing it.
func (s *Stmt) Columns() []Column { return s.cols }

// ID is the server's ID of the statement.
func (s *Stmt) ID() uint32 { return s.id }

// Exec executes the statement with args and returns the result of its
// last result set.
func (s *Stmt) Exec(ctx context.Context, args ...any) (Result, error) {
	rows, err := s.query(ctx, args, false)
	if err != nil {
		return Result{}, err
	}
	err = rows.Close()
	return rows.res, err
}

// Query executes the statement with args and returns its rows, in the
// binary protocol.
func (s *Stmt) Query(ctx context.Context, args ...any) (*Rows, error) {
	return s.query(ctx, args, false)
}

func (s *Stmt) query(ctx context.Context, args []any, closeAfter bool) (*Rows, error) {
	c := s.c
	if s.closed {
		return nil, fmt.Errorf("mysql: statement %d is closed", s.id)
	}
	if len(args) != s.nparams {
		err := fmt.Errorf("mysql: %d arguments for %d parameters", len(args), s.nparams)
		if closeAfter {
			if e := c.begin(ctx); e == nil {
				_ = s.close()
				c.end()
			}
		}
		return nil, err
	}
	if err := c.begin(ctx); err != nil {
		return nil, err
	}
	b := c.startCommand(comStmtExecute)
	b = binary.LittleEndian.AppendUint32(b, s.id)
	b = append(b, 0)                           // no cursor
	b = binary.LittleEndian.AppendUint32(b, 1) // iterations
	if n := len(args); n > 0 {
		at := len(b)
		b = append(b, make([]byte, (n+7)/8)...)
		b = append(b, 1) // types follow
		ts, vs := c.ptypes[:0], c.pvals[:0]
		for i, a := range args {
			var null bool
			var err error
			if ts, vs, null, err = appendParam(ts, vs, a, c.loc); err != nil {
				if closeAfter {
					_ = s.close()
				}
				c.end()
				return nil, fmt.Errorf("%w (argument %d)", err, i+1)
			}
			if null {
				b[at+i/8] |= 1 << (i % 8)
			}
		}
		b = append(b, ts...)
		b = append(b, vs...)
		if cap(vs) <= keepBuffer {
			c.ptypes, c.pvals = ts, vs
		} else {
			c.ptypes, c.pvals = nil, nil
		}
	}
	if err := c.writePacket(b); err != nil {
		return nil, c.fail(err)
	}
	var st *Stmt
	if closeAfter {
		st = s
	}
	return c.startRows(true, st)
}

// Reset resets the statement's state on the server (COM_STMT_RESET).
func (s *Stmt) Reset(ctx context.Context) error {
	var id [4]byte
	binary.LittleEndian.PutUint32(id[:], s.id)
	return s.c.simple(ctx, comStmtReset, id[:])
}

// Close closes the statement on the server; COM_STMT_CLOSE has no answer.
func (s *Stmt) Close(ctx context.Context) error {
	if s.closed {
		return nil
	}
	if err := s.c.begin(ctx); err != nil {
		return err
	}
	defer s.c.end()
	return s.close()
}

func (s *Stmt) close() error {
	s.closed = true
	b := s.c.startCommand(comStmtClose)
	b = binary.LittleEndian.AppendUint32(b, s.id)
	if err := s.c.writePacket(b); err != nil {
		return s.c.fail(err)
	}
	return nil
}
