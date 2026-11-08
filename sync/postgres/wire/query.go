package pgwire

import (
	"context"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
)

// Field describes a column of a result.
type Field struct {
	Name     string
	TableOID uint32
	Column   int16
	TypeOID  uint32
	TypeSize int16
	TypeMod  int32
	Format   int16 // 0 text, 1 binary
}

// Result is one statement's result from the simple query protocol: its
// columns, its rows as text (nil for NULL), and its command tag.
type Result struct {
	Fields []Field
	Rows   [][][]byte
	Tag    string
}

// RowsAffected is the count at the end of the command tag ("INSERT 0 5",
// "UPDATE 3", "COPY 10"), or -1.
func RowsAffected(tag string) int64 {
	i := strings.LastIndexByte(tag, ' ')
	if i < 0 {
		return -1
	}
	n, err := strconv.ParseInt(tag[i+1:], 10, 64)
	if err != nil {
		return -1
	}
	return n
}

func parseFields(b []byte) ([]Field, bool) {
	if len(b) < 2 {
		return nil, false
	}
	n := int(binary.BigEndian.Uint16(b))
	b = b[2:]
	fs := make([]Field, n)
	for i := range fs {
		name, rest, ok := cstring(b)
		if !ok || len(rest) < 18 {
			return nil, false
		}
		fs[i] = Field{
			Name:     name,
			TableOID: binary.BigEndian.Uint32(rest),
			Column:   int16(binary.BigEndian.Uint16(rest[4:])),
			TypeOID:  binary.BigEndian.Uint32(rest[6:]),
			TypeSize: int16(binary.BigEndian.Uint16(rest[10:])),
			TypeMod:  int32(binary.BigEndian.Uint32(rest[12:])),
			Format:   int16(binary.BigEndian.Uint16(rest[16:])),
		}
		b = rest[18:]
	}
	return fs, true
}

// splitRow splits a DataRow into its values (nil for NULL), appending to
// dst; the values refer to b.
func splitRow(dst [][]byte, b []byte) ([][]byte, bool) {
	if len(b) < 2 {
		return dst, false
	}
	n := int(binary.BigEndian.Uint16(b))
	b = b[2:]
	for range n {
		if len(b) < 4 {
			return dst, false
		}
		l := int(int32(binary.BigEndian.Uint32(b)))
		b = b[4:]
		if l < 0 {
			dst = append(dst, nil)
			continue
		}
		if l > len(b) {
			return dst, false
		}
		dst = append(dst, b[:l:l])
		b = b[l:]
	}
	return dst, true
}

// SimpleQuery runs sql, which may hold several statements, with the simple
// query protocol, and returns each statement's result with its rows as
// text. It keeps every row, up to MaxMessageBytes in all: it is meant for
// commands and small answers (SHOW, the replication commands); Query
// streams rows.
func (c *Conn) SimpleQuery(ctx context.Context, sql string) ([]Result, error) {
	exit, err := c.enter(ctx)
	if err != nil {
		return nil, err
	}
	res, err := c.simpleQuery(sql)
	return res, exit(err)
}

func (c *Conn) simpleQuery(sql string) ([]Result, error) {
	i := c.begin('Q')
	c.wbuf = append(append(c.wbuf, sql...), 0)
	c.end(i)
	if err := c.flush(); err != nil {
		return nil, err
	}
	var (
		out    []Result
		cur    *Result
		qerr   error
		budget = c.cfg.MaxMessageBytes
	)
	for {
		t, body, err := c.recv()
		if err != nil {
			return nil, err
		}
		switch t {
		case 'T':
			fs, ok := parseFields(body)
			if !ok {
				return nil, c.protoErr("RowDescription")
			}
			out = append(out, Result{Fields: fs})
			cur = &out[len(out)-1]
		case 'D':
			if cur == nil {
				return nil, c.unexpected(t, "a simple query")
			}
			budget -= len(body)
			if budget < 0 {
				if qerr == nil {
					qerr = fmt.Errorf("postgres: a simple query's rows beyond %d bytes", c.cfg.MaxMessageBytes)
				}
				continue
			}
			vals, ok := splitRow(nil, append([]byte(nil), body...))
			if !ok {
				return nil, c.protoErr("DataRow")
			}
			cur.Rows = append(cur.Rows, vals)
		case 'C':
			tag, _, _ := cstring(body)
			if cur == nil {
				out = append(out, Result{})
				cur = &out[len(out)-1]
			}
			cur.Tag = tag
			cur = nil
		case 'I':
			cur = nil
		case 'E':
			if qerr == nil {
				qerr = parseError(body)
			}
		case 'G': // COPY FROM STDIN: refused here
			i := c.begin('f')
			c.wbuf = append(append(c.wbuf, "use CopyIn for COPY FROM STDIN"...), 0)
			c.end(i)
			if err := c.flush(); err != nil {
				return nil, err
			}
		case 'H', 'd', 'c': // COPY TO STDOUT: discarded
		case 'Z':
			if len(body) > 0 {
				c.txStatus = body[0]
			}
			return out, qerr
		default:
			return nil, c.unexpected(t, "a simple query")
		}
	}
}

// Exec runs one statement and returns its command tag. Without arguments
// it uses the simple query protocol (and sql may hold several statements,
// the last one's tag returned); with them, the extended one.
func (c *Conn) Exec(ctx context.Context, sql string, args ...any) (string, error) {
	if len(args) == 0 {
		res, err := c.SimpleQuery(ctx, sql)
		if err != nil {
			return "", err
		}
		if len(res) == 0 {
			return "", nil
		}
		return res[len(res)-1].Tag, nil
	}
	rows, err := c.Query(ctx, sql, args...)
	if err != nil {
		return "", err
	}
	for rows.Next() {
	}
	err = rows.Close()
	return rows.Tag(), err
}

// Stmt is a prepared statement.
type Stmt struct {
	c      *Conn
	name   string
	params []uint32
	fields []Field
}

// Params are the types of the statement's parameters.
func (s *Stmt) Params() []uint32 { return s.params }

// Fields are the statement's result columns, nil when it returns none.
func (s *Stmt) Fields() []Field { return s.fields }

// Prepare parses sql as a statement named name ("" for the unnamed one,
// which the next Prepare or Query replaces), and describes it.
func (c *Conn) Prepare(ctx context.Context, name, sql string) (*Stmt, error) {
	exit, err := c.enter(ctx)
	if err != nil {
		return nil, err
	}
	st, err := c.prepare(name, sql)
	return st, exit(err)
}

func (c *Conn) prepare(name, sql string) (*Stmt, error) {
	i := c.begin('P')
	c.wbuf = append(append(c.wbuf, name...), 0)
	c.wbuf = append(append(c.wbuf, sql...), 0)
	c.wbuf = append(c.wbuf, 0, 0)
	c.end(i)
	i = c.begin('D')
	c.wbuf = append(append(append(c.wbuf, 'S'), name...), 0)
	c.end(i)
	i = c.begin('S')
	c.end(i)
	if err := c.flush(); err != nil {
		return nil, err
	}
	st := &Stmt{c: c, name: name}
	for {
		t, body, err := c.recv()
		if err != nil {
			return nil, err
		}
		switch t {
		case '1', 'n':
		case 't':
			if len(body) < 2 || len(body) < 2+4*int(binary.BigEndian.Uint16(body)) {
				return nil, c.protoErr("ParameterDescription")
			}
			n := int(binary.BigEndian.Uint16(body))
			st.params = make([]uint32, n)
			for j := range st.params {
				st.params[j] = binary.BigEndian.Uint32(body[2+4*j:])
			}
		case 'T':
			fs, ok := parseFields(body)
			if !ok {
				return nil, c.protoErr("RowDescription")
			}
			st.fields = fs
		case 'E':
			return nil, c.drain(parseError(body))
		case 'Z':
			if len(body) > 0 {
				c.txStatus = body[0]
			}
			for j := range st.fields {
				if binaryOID(st.fields[j].TypeOID) {
					st.fields[j].Format = 1
				}
			}
			return st, nil
		default:
			return nil, c.unexpected(t, "Parse")
		}
	}
}

// Close deallocates the statement.
func (s *Stmt) Close(ctx context.Context) error {
	c := s.c
	exit, err := c.enter(ctx)
	if err != nil {
		return err
	}
	i := c.begin('C')
	c.wbuf = append(append(append(c.wbuf, 'S'), s.name...), 0)
	c.end(i)
	i = c.begin('S')
	c.end(i)
	if err = c.flush(); err == nil {
		err = c.drain(nil)
	}
	return exit(err)
}

// Query prepares sql as the unnamed statement and runs it with args: two
// round trips, the first learning the parameters' and the columns' types.
func (c *Conn) Query(ctx context.Context, sql string, args ...any) (*Rows, error) {
	st, err := c.Prepare(ctx, "", sql)
	if err != nil {
		return nil, err
	}
	return st.Query(ctx, args...)
}

// Query runs the statement with args and streams its rows. The rows must be
// read to the end or closed before the connection is used again.
func (s *Stmt) Query(ctx context.Context, args ...any) (*Rows, error) {
	c := s.c
	if len(args) != len(s.params) {
		return nil, fmt.Errorf("postgres: %d arguments for %d parameters", len(args), len(s.params))
	}
	exit, err := c.enter(ctx)
	if err != nil {
		return nil, err
	}
	start := len(c.wbuf)
	i := c.begin('B')
	c.wbuf = append(c.wbuf, 0) // the unnamed portal
	c.wbuf = append(append(c.wbuf, s.name...), 0)
	c.wbuf = binary.BigEndian.AppendUint16(c.wbuf, uint16(len(args)))
	for j, a := range args {
		f := uint16(0)
		if _, ok := a.([]byte); ok && s.params[j] == OIDBytea {
			f = 1
		}
		c.wbuf = binary.BigEndian.AppendUint16(c.wbuf, f)
	}
	c.wbuf = binary.BigEndian.AppendUint16(c.wbuf, uint16(len(args)))
	for j, a := range args {
		at := len(c.wbuf)
		c.wbuf = append(c.wbuf, 0, 0, 0, 0)
		var notNull bool
		c.wbuf, notNull, err = appendParam(c.wbuf, s.params[j], a)
		if err != nil {
			c.wbuf = c.wbuf[:start]
			return nil, exit(fmt.Errorf("%w (parameter $%d)", err, j+1))
		}
		if notNull {
			binary.BigEndian.PutUint32(c.wbuf[at:], uint32(len(c.wbuf)-at-4))
		} else {
			c.wbuf = c.wbuf[:at]
			c.wbuf = binary.BigEndian.AppendUint32(c.wbuf, 0xFFFFFFFF)
		}
	}
	c.wbuf = binary.BigEndian.AppendUint16(c.wbuf, uint16(len(s.fields)))
	for _, f := range s.fields {
		c.wbuf = binary.BigEndian.AppendUint16(c.wbuf, uint16(f.Format))
	}
	c.end(i)
	i = c.begin('E')
	c.wbuf = append(c.wbuf, 0)
	c.wbuf = binary.BigEndian.AppendUint32(c.wbuf, 0)
	c.end(i)
	i = c.begin('S')
	c.end(i)
	if err := c.flush(); err != nil {
		return nil, exit(err)
	}
	t, body, err := c.recv()
	if err != nil {
		return nil, exit(err)
	}
	switch t {
	case '2':
	case 'E':
		return nil, exit(c.drain(parseError(body)))
	default:
		return nil, exit(c.unexpected(t, "Bind"))
	}
	c.busy = true
	return &Rows{c: c, fields: s.fields, exit: exit}, nil
}

// Rows is a statement's result, read a row at a time.
type Rows struct {
	c      *Conn
	fields []Field
	exit   func(error) error
	raw    [][]byte
	vals   []any
	tag    string
	err    error
	done   bool
}

// Fields are the result's columns.
func (r *Rows) Fields() []Field { return r.fields }

// Next reads the next row; false at the end or on an error (Err).
func (r *Rows) Next() bool {
	if r.done {
		return false
	}
	c := r.c
	for {
		t, body, err := c.recv()
		if err != nil {
			r.finish(err)
			return false
		}
		switch t {
		case 'D':
			if r.err != nil {
				continue
			}
			var ok bool
			r.raw, ok = splitRow(r.raw[:0], body)
			if !ok || len(r.raw) != len(r.fields) {
				r.finish(c.protoErr("DataRow"))
				return false
			}
			r.vals = r.vals[:0]
			for j, v := range r.raw {
				if v == nil {
					r.vals = append(r.vals, nil)
					continue
				}
				f := &r.fields[j]
				if f.Format == 1 {
					x, err := decodeBinary(f.TypeOID, v)
					if err != nil {
						r.err = fmt.Errorf("%w (column %q)", err, f.Name)
						break
					}
					r.vals = append(r.vals, x)
				} else {
					r.vals = append(r.vals, string(v))
				}
			}
			if r.err != nil {
				continue
			}
			return true
		case 'C':
			r.tag, _, _ = cstring(body)
		case 'I', 's', 'n':
		case 'E':
			if r.err == nil {
				r.err = parseError(body)
			}
		case 'Z':
			if len(body) > 0 {
				c.txStatus = body[0]
			}
			r.finish(nil)
			return false
		default:
			r.finish(c.unexpected(t, "rows"))
			return false
		}
	}
}

func (r *Rows) finish(err error) {
	if r.done {
		return
	}
	r.done = true
	r.c.busy = false
	if err != nil && r.err == nil {
		r.err = err
	}
	r.err = r.exit(r.err)
}

// Values are the row's values, typed as decodeBinary says for the types
// read in binary and strings for the others; valid until the next Next.
func (r *Rows) Values() []any { return r.vals }

// RawValues are the row's values as the server sent them (nil for NULL),
// valid until the next Next.
func (r *Rows) RawValues() [][]byte { return r.raw }

// Err is the error that ended the rows, if any.
func (r *Rows) Err() error { return r.err }

// Tag is the command tag, once the rows are read.
func (r *Rows) Tag() string { return r.tag }

// Close reads the rest of the rows and returns Err.
func (r *Rows) Close() error {
	for r.Next() {
	}
	return r.err
}
