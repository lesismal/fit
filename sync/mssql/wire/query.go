package mssqlwire

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Param is a parameter of a call (Call), or an argument of a query given
// by name or type (Query, Exec).
type Param struct {
	// Name is the parameter's, with its @; a query's arguments without
	// one are @p1, @p2 and on, by position.
	Name  string
	Value any
	// Type is the parameter's SQL type, as ParseType reads it; when empty
	// it comes of Value's Go type: bool bit, the integers bigint, float32
	// real, float64 float, string nvarchar(4000) or nvarchar(max), []byte
	// varbinary(8000) or varbinary(max), time.Time datetimeoffset(7),
	// time.Duration time(7), Decimal decimal(38, its scale), UUID
	// uniqueidentifier, nil nvarchar(4000).
	Type string
	// Output makes it an output parameter, whose value the call's Rows
	// has once read (Outputs).
	Output bool
}

// The id of sp_executesql among the procedures RPC names by number.
const procExecuteSQL = 10

// usable reports why the connection cannot take a call, if it cannot.
func (c *Conn) usable() error {
	if c.broken != nil {
		return c.broken
	}
	if c.closed.Load() {
		return ErrClosed
	}
	return nil
}

// appendHeaders appends ALL_HEADERS with the transaction descriptor.
func (c *Conn) appendHeaders(b []byte) []byte {
	b = binary.LittleEndian.AppendUint32(b, 22)
	b = binary.LittleEndian.AppendUint32(b, 18)
	b = binary.LittleEndian.AppendUint16(b, 2)
	b = append(b, c.tx[:]...)
	return binary.LittleEndian.AppendUint32(b, 1)
}

// call is a request on its way, and its answer.
type call struct {
	*response
	ctx   context.Context
	stop  func() bool
	fired chan struct{}
	// cancel says the caller cancelled the rest of the answer
	// (Rows.Close), not the context.
	cancel bool
	ended  bool
}

// send sends a request whose body is body, then watches ctx to cancel it
// with an attention.
func (c *Conn) send(ctx context.Context, typ byte, body []byte) (*call, error) {
	if err := c.usable(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	w := c.newWriter(typ)
	w.ctx = ctx
	if _, err := w.Write(body); err != nil {
		return nil, err
	}
	if err := w.close(); err != nil {
		return nil, err
	}
	return c.watch(ctx), nil
}

// watch starts the answer of a request sent, ctx's end sending an
// attention.
func (c *Conn) watch(ctx context.Context) *call {
	k := &call{response: &response{c: c}, ctx: ctx, fired: make(chan struct{})}
	c.attn.Store(false)
	k.stop = context.AfterFunc(ctx, func() {
		defer close(k.fired)
		c.attention()
	})
	return k
}

// attention asks the server to cancel the request under way.
func (c *Conn) attention() {
	if c.attn.Swap(true) {
		return
	}
	at := time.Now()
	c.attnAt.Store(at.UnixNano())
	c.write([]byte{pktAttention, statusEOM, 0, headerLen, 0, 0, 1, 0})
	c.nc.SetReadDeadline(at.Add(attentionWait))
}

// end ends the call: an attention sent is read to its acknowledgement,
// after which the call's error is the context's (or nil, for a
// cancellation of its own). err is the answer's, so far.
func (k *call) end(err error) error {
	if k.ended {
		return err
	}
	k.ended = true
	if !k.stop() {
		<-k.fired
	}
	c := k.c
	if !c.attn.Load() {
		return err
	}
	var rerr error
	for rerr == nil && !k.attnAcked && c.broken == nil {
		if k.final {
			k.final, k.begun = false, false
		}
		_, rerr = k.step()
	}
	c.attn.Store(false)
	if rerr == nil && c.broken != nil {
		rerr = c.broken
	}
	if rerr != nil {
		c.fail(rerr)
		if k.cancel {
			return rerr
		}
		return fmt.Errorf("%w (and the server did not answer the attention: %v)", k.ctx.Err(), rerr)
	}
	if k.cancel {
		return nil
	}
	return k.ctx.Err()
}

// drain reads the answer to its end.
func (k *call) drain() error {
	err := k.response.drain()
	return k.end(err)
}

// Exec runs sql, a batch of statements, or one with args, and returns the
// rows the statements changed. Args are values, @p1 and on, or Params.
func (c *Conn) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	k, err := c.query(ctx, sql, args)
	if err != nil {
		return 0, err
	}
	err = k.drain()
	return k.affected, err
}

// query sends sql as a batch, or through sp_executesql with args.
func (c *Conn) query(ctx context.Context, sql string, args []any) (*call, error) {
	if len(args) == 0 {
		return c.send(ctx, pktSQLBatch, appendUCS2(c.appendHeaders(nil), sql))
	}
	params := make([]Param, len(args))
	var decl []string
	for i, a := range args {
		p, ok := a.(Param)
		if !ok {
			p = Param{Value: a}
		}
		if p.Name == "" {
			p.Name = "@p" + strconv.Itoa(i+1)
		}
		params[i] = p
	}
	cols, err := c.paramColumns(params)
	if err != nil {
		return nil, err
	}
	for i, p := range params {
		d := p.Name + " " + cols[i].TypeName()
		if p.Output {
			d += " OUTPUT"
		}
		decl = append(decl, d)
	}
	head := []Param{{Name: "@stmt", Value: sql}, {Name: "@params", Value: strings.Join(decl, ",")}}
	ntext := Column{Type: TypeNVarChar, Size: -1}
	body, err := c.rpcBody("", procExecuteSQL, append(head, params...), append([]Column{ntext, ntext}, cols...))
	if err != nil {
		return nil, err
	}
	return c.send(ctx, pktRPC, body)
}

// paramColumns makes the types of params.
func (c *Conn) paramColumns(params []Param) ([]Column, error) {
	cols := make([]Column, len(params))
	for i, p := range params {
		if !strings.HasPrefix(p.Name, "@") {
			return nil, fmt.Errorf("mssql: the parameter %q has no @", p.Name)
		}
		var err error
		if p.Type != "" {
			cols[i], err = ParseType(p.Type, c.collation)
		} else {
			cols[i], err = typeOf(p.Value)
		}
		if err != nil {
			return nil, fmt.Errorf("mssql: the parameter %s: %w", p.Name, err)
		}
	}
	return cols, nil
}

// typeOf is the type a value goes as when its parameter names none.
func typeOf(v any) (Column, error) {
	switch x := v.(type) {
	case nil:
		return Column{Type: TypeNVarChar, Size: 8000}, nil
	case bool:
		return Column{Type: TypeBitN, Size: 1}, nil
	case float32:
		return Column{Type: TypeFloatN, Size: 4}, nil
	case float64:
		return Column{Type: TypeFloatN, Size: 8}, nil
	case string:
		if ucs2Len(x) > 4000 {
			return Column{Type: TypeNVarChar, Size: -1}, nil
		}
		return Column{Type: TypeNVarChar, Size: 8000}, nil
	case []byte:
		if len(x) > 8000 {
			return Column{Type: TypeBigVarBinary, Size: -1}, nil
		}
		return Column{Type: TypeBigVarBinary, Size: 8000}, nil
	case time.Time:
		return Column{Type: TypeDateTimeOffset, Scale: 7, Size: 10}, nil
	case time.Duration:
		return Column{Type: TypeTimeN, Scale: 7, Size: 5}, nil
	case Decimal:
		_, frac, _ := strings.Cut(string(x), ".")
		if len(frac) > 38 {
			return Column{}, fmt.Errorf("%s has more than 38 decimals", x)
		}
		return Column{Type: TypeDecimalN, Precision: 38, Scale: uint8(len(frac)), Size: 17}, nil
	case UUID:
		return Column{Type: TypeGUID, Size: 16}, nil
	}
	if _, ok := toInt(v); ok {
		return Column{Type: TypeIntN, Size: 8}, nil
	}
	return Column{}, fmt.Errorf("a %T is not a type the client sends", v)
}

// rpcBody makes an RPC request of a procedure, named or by id.
func (c *Conn) rpcBody(proc string, id uint16, params []Param, cols []Column) ([]byte, error) {
	b := c.appendHeaders(nil)
	if proc == "" {
		b = binary.LittleEndian.AppendUint16(b, 0xffff)
		b = binary.LittleEndian.AppendUint16(b, id)
	} else {
		b = appendUSVarchar(b, proc)
	}
	b = binary.LittleEndian.AppendUint16(b, 0)
	for i, p := range params {
		b = appendBVarchar(b, p.Name)
		status := byte(0)
		if p.Output {
			status = 1
		}
		b = append(b, status)
		b = appendTypeInfo(b, &cols[i], false)
		var err error
		if b, err = appendValue(b, &cols[i], p.Value); err != nil {
			return nil, fmt.Errorf("mssql: the parameter %s: %w", p.Name, err)
		}
	}
	return b, nil
}

// Rows is the answer of a query or a call: its result sets, read one row
// at a time. Close it, or read it to its end, before the next call.
type Rows struct {
	k   *call
	err error
	// set says the current result set has rows to read; done that the
	// answer is read whole.
	set  bool
	done bool
	// pending says a result set began where the last one ended.
	pending bool
	cols    []Column
}

// Query runs sql, a batch or one statement with args (as Exec's), and
// returns its answer at its first result set.
func (c *Conn) Query(ctx context.Context, sql string, args ...any) (*Rows, error) {
	k, err := c.query(ctx, sql, args)
	if err != nil {
		return nil, err
	}
	return k.rows()
}

// Call runs the stored procedure proc with params, and returns its answer
// at its first result set; its return status and output parameters are
// the Rows' once read.
func (c *Conn) Call(ctx context.Context, proc string, params ...Param) (*Rows, error) {
	cols, err := c.paramColumns(params)
	if err != nil {
		return nil, err
	}
	body, err := c.rpcBody(proc, 0, params, cols)
	if err != nil {
		return nil, err
	}
	k, err := c.send(ctx, pktRPC, body)
	if err != nil {
		return nil, err
	}
	return k.rows()
}

// rows reads to the answer's first result set, or its end.
func (k *call) rows() (*Rows, error) {
	r := &Rows{k: k}
	if !r.advance() && r.err != nil {
		return nil, r.err
	}
	return r, nil
}

// advance reads to the next result set's first row, true, or the answer's
// end, false.
func (r *Rows) advance() bool {
	for !r.k.final {
		tok, err := r.k.step()
		if err != nil {
			r.fail(err)
			return false
		}
		if tok == tokColMetadata && r.k.cols != nil {
			r.cols, r.set = r.k.cols, true
			return true
		}
	}
	r.finish()
	return false
}

// fail ends the answer with err.
func (r *Rows) fail(err error) {
	if !r.done {
		r.done, r.set = true, false
		r.err = r.k.end(err)
	}
}

// finish ends an answer read whole.
func (r *Rows) finish() {
	if !r.done {
		r.done, r.set = true, false
		r.err = r.k.end(r.k.err())
	}
}

// Columns are the current result set's columns.
func (r *Rows) Columns() []Column { return r.cols }

// Next reads the current result set's next row, false at its end.
func (r *Rows) Next() bool {
	for r.set {
		tok, err := r.k.step()
		if err != nil {
			r.fail(err)
			return false
		}
		switch tok {
		case tokRow, tokNBCRow:
			return true
		case tokDone, tokDoneProc, tokDoneInProc:
			r.set = false
			if r.k.final {
				r.finish()
			}
		case tokColMetadata:
			// A result set with no DONE between: the next one.
			r.set = false
			r.pending = true
		}
	}
	return false
}

// NextResultSet moves to the answer's next result set, false when there is
// none.
func (r *Rows) NextResultSet() bool {
	for r.Next() {
	}
	if r.pending {
		r.pending = false
		r.cols, r.set = r.k.cols, true
		return true
	}
	if r.done {
		return false
	}
	return r.advance()
}

// Values are the row read last, valid until the next call of Next.
func (r *Rows) Values() []any { return r.k.vals }

// Err is the answer's error, once Next returned false: the first server
// error, a broken connection, or the context's end.
func (r *Rows) Err() error { return r.err }

// Affected is the rows the statements changed, once the answer is read.
func (r *Rows) Affected() int64 { return r.k.affected }

// ReturnStatus is a procedure's return status, once the answer is read.
func (r *Rows) ReturnStatus() (int32, bool) { return r.k.status, r.k.hasStatus }

// Outputs are the output parameters by name, once the answer is read.
func (r *Rows) Outputs() map[string]any { return r.k.outputs }

// Close ends the answer, cancelling what is left of it with an attention,
// and returns its error.
func (r *Rows) Close() error {
	if r.done {
		return r.err
	}
	if r.k.c.broken == nil {
		r.k.cancel = true
		r.k.c.attention()
	}
	for !r.k.final && r.k.c.broken == nil {
		if _, err := r.k.step(); err != nil {
			break
		}
	}
	r.done, r.set = true, false
	err := r.k.err()
	if !r.k.final {
		err = r.k.c.broken
	}
	r.err = r.k.end(err)
	return r.err
}

// QueryAll runs a query and returns its first result set whole, the
// values copied.
func (c *Conn) QueryAll(ctx context.Context, sql string, args ...any) ([]Column, [][]any, error) {
	rows, err := c.Query(ctx, sql, args...)
	if err != nil {
		return nil, nil, err
	}
	var out [][]any
	for rows.Next() {
		out = append(out, append([]any(nil), rows.Values()...))
	}
	cols := rows.Columns()
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}
	return cols, out, nil
}

// The isolation levels Begin takes.
var isolations = map[string]bool{"READ UNCOMMITTED": true, "READ COMMITTED": true, "REPEATABLE READ": true, "SNAPSHOT": true, "SERIALIZABLE": true}

// Begin begins a transaction, at isolation (READ COMMITTED, SNAPSHOT, ...)
// when it is not empty; the calls after it run in it until Commit or
// Rollback.
func (c *Conn) Begin(ctx context.Context, isolation string) error {
	sql := "BEGIN TRANSACTION"
	if isolation != "" {
		if !isolations[strings.ToUpper(isolation)] {
			return fmt.Errorf("mssql: %q is not an isolation level", isolation)
		}
		sql = "SET TRANSACTION ISOLATION LEVEL " + strings.ToUpper(isolation) + "; " + sql
	}
	_, err := c.Exec(ctx, sql)
	if err == nil && !c.InTransaction() {
		return errors.New("mssql: the server began no transaction")
	}
	return err
}

// Commit commits the transaction.
func (c *Conn) Commit(ctx context.Context) error {
	_, err := c.Exec(ctx, "IF @@TRANCOUNT > 0 COMMIT TRANSACTION")
	return err
}

// Rollback rolls the transaction back.
func (c *Conn) Rollback(ctx context.Context) error {
	_, err := c.Exec(ctx, "IF @@TRANCOUNT > 0 ROLLBACK TRANSACTION")
	return err
}
