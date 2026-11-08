package mysqlwire

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// prepareOK answers a prepare with n parameters and cols.
func (f *fake) prepareOK(id uint32, n int, cols []Column) {
	b := []byte{0}
	b = binary.LittleEndian.AppendUint32(b, id)
	b = binary.LittleEndian.AppendUint16(b, uint16(len(cols)))
	b = binary.LittleEndian.AppendUint16(b, uint16(n))
	f.write(append(b, 0, 0, 0))
	if n > 0 {
		for range n {
			f.write(colDef(Column{Name: "?", Type: TypeVarString}))
		}
		f.eof()
	}
	if len(cols) > 0 {
		for _, c := range cols {
			f.write(colDef(c))
		}
		f.eof()
	}
}

// execute reads an execute's statement ID and its parameters' values, as
// binaryValue reads them.
func (f *fake) execute(body []byte, n int) (uint32, []any) {
	r := rd{b: body}
	id := r.u32()
	r.take(5)
	if n == 0 {
		return id, nil
	}
	nulls := r.take((n + 7) / 8)
	if r.u8() != 1 {
		f.t.Error("fake: types not bound")
	}
	types := r.take(2 * n)
	vals := make([]any, n)
	for i := range n {
		if nulls[i/8]&(1<<(i%8)) != 0 {
			continue
		}
		col := Column{Type: types[2*i]}
		if types[2*i+1]&0x80 != 0 {
			col.Flags = FlagUnsigned
		}
		vals[i] = binaryValue(&col, &r)
	}
	if r.bad || len(r.b) != 0 {
		f.t.Errorf("fake: a malformed execute (%v, %d left)", r.bad, len(r.b))
	}
	return id, vals
}

// binaryRow sends a binary row of vals, encoded as parameters are.
func (f *fake) binaryRow(cols []Column, vals ...any) {
	b := []byte{0}
	nulls := make([]byte, (len(cols)+9)/8)
	var data []byte
	for i, v := range vals {
		if v == nil {
			nulls[(i+2)/8] |= 1 << ((i + 2) % 8)
			continue
		}
		switch cols[i].Type {
		case TypeTiny:
			data = append(data, byte(v.(int64)))
		case TypeShort, TypeYear:
			data = binary.LittleEndian.AppendUint16(data, uint16(v.(int64)))
		case TypeLong, TypeInt24:
			data = binary.LittleEndian.AppendUint32(data, uint32(v.(int64)))
		default:
			_, data, _, _ = appendParam(nil, data, v, time.UTC)
		}
	}
	f.write(append(append(b, nulls...), data...))
}

func TestPreparedStatements(t *testing.T) {
	cols := []Column{
		{Name: "ti", Type: TypeTiny}, {Name: "s", Type: TypeShort}, {Name: "y", Type: TypeYear},
		{Name: "l", Type: TypeLong}, {Name: "bi", Type: TypeLongLong, Flags: FlagUnsigned},
		{Name: "f", Type: TypeFloat}, {Name: "d", Type: TypeDouble}, {Name: "v", Type: TypeVarString},
		{Name: "dt", Type: TypeDateTime}, {Name: "tm", Type: TypeTime}, {Name: "n", Type: TypeBlob},
	}
	row := []any{int64(-1), int64(-300), int64(2026), int64(-70000), uint64(1 << 63), float32(0.5), 1e100,
		[]byte("v"), DateTime{2026, 11, 8, 1, 2, 3, 4}, -time.Minute, nil}
	args := []any{nil, true, -1, uint16(2), float32(1.5), 2.5, "s", []byte{1}, DateTime{Year: 1}, time.Second,
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	for _, deprecate := range []bool{false, true} {
		caps := uint32(fakeCaps)
		if deprecate {
			caps |= capDeprecateEOF
		}
		closed := make(chan uint32, 10)
		addr := serve(t, func(f *fake) {
			f.login(caps)
			for {
				cmd, body := f.cmd()
				switch cmd {
				case comStmtPrepare:
					switch q := string(body); q {
					case "SELECT ROW":
						f.prepareOK(1, len(args), cols)
					case "INSERT ?":
						f.prepareOK(2, 1, nil)
					case "BAD":
						f.fail(1064, "42000", "syntax")
					case "WEIRD":
						f.write([]byte{7})
					case "SHORT":
						f.write([]byte{0, 1})
					case "CLOSE":
						f.prepareOK(3, 1, nil)
						f.nc.Close()
						return
					default:
						t.Errorf("prepare %q", q)
					}
				case comStmtExecute:
					id, vals := f.execute(body, map[uint32]int{1: len(args), 2: 1}[binary.LittleEndian.Uint32(body)])
					switch id {
					case 1:
						want := []any{nil, int64(1), int64(-1), uint64(2), float32(1.5), 2.5, []byte("s"), []byte{1},
							DateTime{Year: 1}, time.Second, DateTime{Year: 2026, Month: 1, Day: 1}}
						if !reflect.DeepEqual(vals, want) {
							t.Errorf("args %#v", vals)
						}
						f.write([]byte{byte(len(cols))})
						for _, c := range cols {
							f.write(colDef(c))
						}
						f.eof()
						f.binaryRow(cols, row...)
						f.binaryRow(cols, row...)
						f.end(0)
					case 2:
						if vals[0] == int64(0) {
							f.fail(1062, "23000", "Duplicate entry")
						} else {
							f.ok(1, 9, 0)
						}
					}
				case comStmtReset:
					f.ok(0, 0, 0)
				case comStmtClose:
					closed <- binary.LittleEndian.Uint32(body)
				default:
					return
				}
			}
		})
		c := dialT(t, Config{Addr: addr, User: "u"})
		ctx := ctxT(t)
		st, err := c.Prepare(ctx, "SELECT ROW")
		if err != nil {
			t.Fatal(err)
		}
		if st.NumParams() != len(args) || len(st.Columns()) != len(cols) || st.ID() != 1 {
			t.Fatal(st.NumParams(), st.Columns())
		}
		for range 2 {
			rows, err := st.Query(ctx, args...)
			if err != nil {
				t.Fatal(err)
			}
			n := 0
			for rows.Next() {
				if !reflect.DeepEqual(rows.Values(), row) {
					t.Fatalf("row %#v", rows.Values())
				}
				n++
			}
			if n != 2 || rows.Close() != nil {
				t.Fatal(n, rows.Err())
			}
		}
		if _, err := st.Exec(ctx, 1); err == nil || !strings.Contains(err.Error(), "1 arguments for 11") {
			t.Fatal(err)
		}
		if _, err := st.Exec(ctx, append(args[:10:10], struct{}{})...); err == nil || !strings.Contains(err.Error(), "argument 11") {
			t.Fatal(err)
		}
		if err := st.Reset(ctx); err != nil {
			t.Fatal(err)
		}
		if err := st.Close(ctx); err != nil || st.Close(ctx) != nil {
			t.Fatal(err)
		}
		if _, err := st.Exec(ctx, args...); err == nil {
			t.Fatal("a closed statement")
		}
		if id := <-closed; id != 1 {
			t.Fatal(id)
		}
		// Exec and Query with arguments prepare, run and close.
		res, err := c.Exec(ctx, "INSERT ?", 5)
		if err != nil || res.AffectedRows != 1 || res.LastInsertID != 9 {
			t.Fatal(res, err)
		}
		if id := <-closed; id != 2 {
			t.Fatal(id)
		}
		var se *Error
		if _, err := c.Exec(ctx, "INSERT ?", 0); !errors.As(err, &se) || se.Code != 1062 {
			t.Fatal(err)
		}
		if id := <-closed; id != 2 {
			t.Fatal(id)
		}
		if _, err := c.Exec(ctx, "INSERT ?", 1, 2); err == nil {
			t.Fatal("two arguments for one")
		}
		if id := <-closed; id != 2 {
			t.Fatal(id)
		}
		if _, err := c.Exec(ctx, "INSERT ?", struct{}{}); err == nil {
			t.Fatal("a struct")
		}
		if id := <-closed; id != 2 {
			t.Fatal(id)
		}
		if _, err := c.Exec(ctx, "BAD", 1); !errors.As(err, &se) {
			t.Fatal(err)
		}
		if _, err := c.Prepare(ctx, "WEIRD"); err == nil || !strings.Contains(err.Error(), "answer 0x7") {
			t.Fatal(err)
		}
	}
}

func TestPreparedBroken(t *testing.T) {
	cases := []struct {
		name   string
		script func(f *fake)
		want   string
	}{
		{"short", func(f *fake) { f.write([]byte{0, 1}) }, "prepare OK"},
		{"closed", func(f *fake) { f.nc.Close() }, "EOF"},
		{"closed in the params", func(f *fake) {
			b := binary.LittleEndian.AppendUint32([]byte{0}, 1)
			f.write(append(b, 0, 0, 1, 0, 0, 0, 0))
			f.nc.Close()
		}, "EOF"},
		{"closed in the columns", func(f *fake) {
			b := binary.LittleEndian.AppendUint32([]byte{0}, 1)
			f.write(append(b, 1, 0, 0, 0, 0, 0, 0))
			f.nc.Close()
		}, "EOF"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			addr := serve(t, func(f *fake) {
				f.login(fakeCaps)
				f.cmd()
				tc.script(f)
			})
			c := dialT(t, Config{Addr: addr, User: "u"})
			_, err := c.Prepare(ctxT(t), "SELECT ?")
			if err == nil || !strings.Contains(err.Error(), tc.want) || c.Err() == nil {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
	// Binary rows that do not parse.
	for name, row := range map[string][]byte{
		"header": {1, 0, 0},
		"short":  {0, 0},
		"long":   {0, 0, 1, 0, 0, 0, 9},
		"value":  {0, 0, 1},
	} {
		t.Run(name, func(t *testing.T) {
			addr := serve(t, func(f *fake) {
				f.login(fakeCaps)
				f.cmd()
				f.prepareOK(1, 0, nil)
				f.cmd()
				f.write([]byte{1})
				f.write(colDef(Column{Name: "l", Type: TypeLong}))
				f.eof()
				f.write(row)
				time.Sleep(50 * time.Millisecond)
			})
			c := dialT(t, Config{Addr: addr, User: "u"})
			st, err := c.Prepare(ctxT(t), "SELECT 1")
			if err != nil {
				t.Fatal(err)
			}
			rows, err := st.Query(ctxT(t))
			if err != nil {
				t.Fatal(err)
			}
			if rows.Next() || !errors.Is(rows.Err(), ErrMalformed) {
				t.Fatal(rows.Err())
			}
		})
	}
	// A statement's close that cannot be written.
	addr := serve(t, func(f *fake) {
		f.login(fakeCaps)
		f.cmd()
		f.prepareOK(1, 1, nil)
		f.cmd()
		f.ok(0, 0, 0)
	})
	c := dialT(t, Config{Addr: addr, User: "u"})
	st, err := c.Prepare(ctxT(t), "SELECT ?")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Exec(ctxT(t), 1); err != nil {
		t.Fatal(err)
	}
	c.raw.Conn.Close()
	if err := st.Close(ctxT(t)); err == nil {
		t.Fatal("a close on a closed socket")
	}
	if _, err := st.Exec(context.Background(), 1); err == nil {
		t.Fatal("an execute on a broken connection")
	}
	if err := st.Close(ctxT(t)); err != nil {
		t.Fatal("a second close", err)
	}
}

func TestInsertRows(t *testing.T) {
	type exec struct {
		rows int
		vals []any
	}
	var got []exec
	prepared := map[uint32]int{}
	const maxAllowed = "64000000"
	addr := serve(t, func(f *fake) {
		f.login(fakeCaps | capDeprecateEOF)
		next := uint32(1)
		for {
			cmd, body := f.cmd()
			switch cmd {
			case comQuery:
				// What the fake saw, for the test to check without
				// sharing its memory.
				switch string(body) {
				case "OPEN":
					f.ok(uint64(len(prepared)), 0, 0)
				case "GOT":
					var s []string
					for _, e := range got {
						s = append(s, fmt.Sprintf("%d:%v", e.rows, e.vals[0]))
					}
					got = nil
					b := []byte{0, 0, 0, 0, 0, 0, 0}
					f.write(append(b, strings.Join(s, " ")...))
				default:
					f.columns([]Column{{Name: "m", Type: TypeLongLong}})
					f.textRow([]byte(maxAllowed))
					f.end(0)
				}
			case comStmtPrepare:
				q := string(body)
				if !strings.HasPrefix(q, "INSERT INTO t (a, b) VALUES (?,?)") || !strings.HasSuffix(q, " ON DUPLICATE KEY UPDATE b = b") {
					t.Errorf("prepare %.80q", q)
				}
				n := strings.Count(q, "?")
				prepared[next] = n
				f.prepareOK(next, n, nil)
				next++
			case comStmtExecute:
				id, vals := f.execute(body, prepared[binary.LittleEndian.Uint32(body)])
				got = append(got, exec{prepared[id] / 2, vals})
				if vals[0] == int64(-1) {
					f.fail(1062, "23000", "Duplicate")
					continue
				}
				f.ok(uint64(prepared[id]/2), uint64(len(got)), 0)
			case comStmtClose:
				delete(prepared, binary.LittleEndian.Uint32(body))
			default:
				return
			}
		}
	})
	c := dialT(t, Config{Addr: addr, User: "u"})
	ctx := ctxT(t)
	if res, err := c.InsertRows(ctx, "INSERT", nil, ""); err != nil || res != (Result{}) {
		t.Fatal(res, err)
	}
	// 40,000 rows of two placeholders: 32,767 rows a statement at most;
	// rows of 10 KB: about 9 a statement in 100,000 bytes.
	var rows [][]any
	for i := range 40000 {
		rows = append(rows, []any{i, "v"})
	}
	res, err := c.InsertRows(ctx, "INSERT INTO t (a, b) VALUES", rows, "ON DUPLICATE KEY UPDATE b = b")
	if err != nil || res.AffectedRows != 40000 || res.LastInsertID != 1 {
		t.Fatal(res, err)
	}
	seen := func() string {
		res, err := c.Exec(ctx, "GOT")
		if err != nil {
			t.Fatal(err)
		}
		return res.Info
	}
	open := func() uint64 {
		res, err := c.Exec(ctx, "OPEN")
		if err != nil {
			t.Fatal(err)
		}
		return res.AffectedRows
	}
	if s := seen(); s != "32767:0 7233:32767" {
		t.Fatal(s)
	}
	rows = rows[:0]
	c.maxAllow = 100000
	for i := range 30 {
		rows = append(rows, []any{i, strings.Repeat("x", 10000)})
	}
	if _, err := c.InsertRows(ctx, "INSERT INTO t (a, b) VALUES", rows, "ON DUPLICATE KEY UPDATE b = b"); err != nil {
		t.Fatal(err)
	}
	if s := seen(); s != "9:0 9:9 9:18 3:27" {
		t.Fatal(s)
	}
	if n := open(); n != 0 {
		t.Fatalf("%d statements left open", n)
	}
	// Failures: a statement refused, rows of different lengths, rows of
	// nothing, a value of an unknown type.
	var se *Error
	if _, err := c.InsertRows(ctx, "INSERT INTO t (a, b) VALUES", [][]any{{-1, "x"}}, "ON DUPLICATE KEY UPDATE b = b"); !errors.As(err, &se) {
		t.Fatal(err)
	}
	if _, err := c.InsertRows(ctx, "INSERT", [][]any{{1, 2}, {1}}, ""); err == nil {
		t.Fatal("rows of different lengths")
	}
	if _, err := c.InsertRows(ctx, "INSERT", [][]any{{}}, ""); err == nil {
		t.Fatal("rows of nothing")
	}
	if _, err := c.InsertRows(ctx, "INSERT INTO t (a, b) VALUES", [][]any{{1, struct{}{}}}, "ON DUPLICATE KEY UPDATE b = b"); err == nil {
		t.Fatal("a struct")
	}
	if n := open(); n != 0 {
		t.Fatalf("%d statements left open", n)
	}
	if rowSize([]any{time.Second, []byte("ab"), "abc", nil}) != 4*11+4+2+3 {
		t.Fatal(rowSize([]any{time.Second}))
	}
}

func TestMaxAllowedPacket(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script func(f *fake)
		want   string
	}{
		{"none", func(f *fake) {
			f.columns([]Column{{Name: "m", Type: TypeVarString}})
			f.textRow([]byte("x"))
			f.end(0)
		}, "not read"},
		{"unsigned", func(f *fake) {
			f.columns([]Column{{Name: "m", Type: TypeLongLong, Flags: FlagUnsigned}})
			f.textRow([]byte("1024"))
			f.end(0)
		}, ""},
		{"refused", func(f *fake) { f.fail(1227, "42000", "denied") }, "denied"},
		{"broken", func(f *fake) {
			f.columns([]Column{{Name: "m", Type: TypeLongLong}})
			f.textRow([]byte("1024"))
			f.nc.Close()
		}, "EOF"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addr := serve(t, func(f *fake) {
				f.login(fakeCaps)
				f.cmd()
				tc.script(f)
				f.cmd()
			})
			c := dialT(t, Config{Addr: addr, User: "u"})
			n, err := c.MaxAllowedPacket(ctxT(t))
			if tc.want == "" {
				if err != nil || n != 1024 {
					t.Fatal(n, err)
				}
				if n, _ := c.MaxAllowedPacket(ctxT(t)); n != 1024 {
					t.Fatal("not kept")
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
			if _, err := c.InsertRows(ctxT(t), "INSERT", [][]any{{1}}, ""); err == nil {
				t.Fatal("rows inserted without max_allowed_packet")
			}
		})
	}
}
