package mysqlwire

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// textTypes are columns of every kind, with a text row of them and the
// values it reads as.
var textTypes = []struct {
	col  Column
	text []byte
	want any
}{
	{Column{Name: "ti", Type: TypeTiny}, []byte("-128"), int64(-128)},
	{Column{Name: "tiu", Type: TypeTiny, Flags: FlagUnsigned}, []byte("255"), uint64(255)},
	{Column{Name: "bi", Type: TypeLongLong}, []byte("-9223372036854775808"), int64(math.MinInt64)},
	{Column{Name: "biu", Type: TypeLongLong, Flags: FlagUnsigned}, []byte("18446744073709551615"), uint64(math.MaxUint64)},
	{Column{Name: "y", Type: TypeYear, Flags: FlagUnsigned}, []byte("2155"), int64(2155)},
	{Column{Name: "f", Type: TypeFloat}, []byte("-1.5"), float32(-1.5)},
	{Column{Name: "d", Type: TypeDouble}, []byte("1.25e-300"), 1.25e-300},
	{Column{Name: "dt", Type: TypeDate}, []byte("2026-11-08"), DateTime{Year: 2026, Month: 11, Day: 8}},
	{Column{Name: "dtm", Type: TypeDateTime}, []byte("2026-11-08 01:02:03.5"), DateTime{2026, 11, 8, 1, 2, 3, 500000}},
	{Column{Name: "ts", Type: TypeTimestamp}, []byte("0000-00-00 00:00:00"), DateTime{}},
	{Column{Name: "tm", Type: TypeTime}, []byte("-838:59:58.999999"), -(838*time.Hour + 59*time.Minute + 58*time.Second + 999999*time.Microsecond)},
	{Column{Name: "tm0", Type: TypeTime}, []byte("01:00:00"), time.Hour},
	{Column{Name: "de", Type: TypeNewDecimal}, []byte("-1.50"), []byte("-1.50")},
	{Column{Name: "s", Type: TypeVarString, Collation: 45}, []byte("héllo"), []byte("héllo")},
	{Column{Name: "n", Type: TypeVarString}, nil, nil},
	{Column{Name: "e", Type: TypeString}, []byte(""), []byte("")},
}

func TestTextQuery(t *testing.T) {
	for _, deprecate := range []bool{false, true} {
		caps := uint32(fakeCaps)
		if deprecate {
			caps |= capDeprecateEOF
		}
		addr := serve(t, func(f *fake) {
			f.login(caps)
			for {
				cmd, body := f.cmd()
				if cmd != comQuery {
					return
				}
				switch q := string(body); q {
				case "SELECT types":
					var cols []Column
					var row [][]byte
					for _, tt := range textTypes {
						cols = append(cols, tt.col)
						row = append(row, tt.text)
					}
					f.columns(cols)
					f.textRow(row...)
					f.textRow(row...)
					f.end(StatusAutocommit)
				case "INSERT":
					f.write([]byte("\x00\x03\x07\x02\x00\x01\x00Records: 3"))
				case "CALL":
					f.columns([]Column{{Name: "a", Type: TypeLong}})
					f.textRow([]byte("1"))
					f.end(StatusMoreResultsExist)
					f.columns([]Column{{Name: "b", Type: TypeLong}})
					f.textRow([]byte("2"))
					f.textRow([]byte("3"))
					f.end(StatusMoreResultsExist)
					f.ok(0, 0, StatusAutocommit)
				case "ERR":
					f.columns([]Column{{Name: "a", Type: TypeLong}})
					f.textRow([]byte("1"))
					f.fail(1317, "70100", "Query execution was interrupted")
				case "INFILE":
					f.write([]byte("\xfbfile.csv"))
					if p := f.read(); len(p) != 0 {
						t.Errorf("infile answer %q", p)
					}
					f.ok(0, 0, 0)
				case "START TRANSACTION":
					f.ok(0, 0, StatusInTrans)
				case "COMMIT", "ROLLBACK":
					f.ok(0, 0, StatusAutocommit)
				default:
					f.fail(1064, "42000", "syntax: "+q)
				}
			}
		})
		c := dialT(t, Config{Addr: addr, User: "u", Password: "pw"})
		ctx := ctxT(t)
		rows, err := c.Query(ctx, "SELECT types")
		if err != nil {
			t.Fatal(err)
		}
		if len(rows.Columns()) != len(textTypes) {
			t.Fatal(rows.Columns())
		}
		n := 0
		for rows.Next() {
			for i, v := range rows.Values() {
				if !reflect.DeepEqual(v, textTypes[i].want) {
					t.Errorf("%s: %#v, want %#v", textTypes[i].col.Name, v, textTypes[i].want)
				}
			}
			n++
		}
		if rows.Err() != nil || n != 2 || rows.Result().Status != StatusAutocommit {
			t.Fatal(rows.Err(), n)
		}
		if rows.NextResultSet() || rows.Close() != nil {
			t.Fatal("a second result set")
		}
		res, err := c.Exec(ctx, "INSERT")
		if err != nil || res.AffectedRows != 3 || res.LastInsertID != 7 || res.Status != 2 || res.Warnings != 1 || res.Info != "Records: 3" {
			t.Fatalf("%+v %v", res, err)
		}
		if err := c.Begin(ctx); err != nil || !c.InTransaction() {
			t.Fatal(err)
		}
		if err := c.Rollback(ctx); err != nil || c.InTransaction() {
			t.Fatal(err)
		}
		_ = c.Begin(ctx)
		if err := c.Commit(ctx); err != nil || c.InTransaction() {
			t.Fatal(err)
		}
		// Several results, read and skipped.
		rows, _ = c.Query(ctx, "CALL")
		var got []int
		for {
			k := 0
			for rows.Next() {
				k++
			}
			got = append(got, k)
			if !rows.NextResultSet() {
				break
			}
		}
		if !reflect.DeepEqual(got, []int{1, 2, 0}) || rows.Close() != nil {
			t.Fatal(got, rows.Err())
		}
		rows, _ = c.Query(ctx, "CALL")
		if _, err := c.Exec(ctx, "INSERT"); !errors.Is(err, ErrBusy) {
			t.Fatal(err)
		}
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
		if rows.Close() == nil {
			t.Fatal("rows of a closed connection")
		}
	}
}

func TestTextQueryErrors(t *testing.T) {
	addr := serve(t, func(f *fake) {
		f.login(fakeCaps)
		for {
			cmd, body := f.cmd()
			if cmd != comQuery {
				return
			}
			switch string(body) {
			case "ERR":
				f.columns([]Column{{Name: "a", Type: TypeLong}})
				f.textRow([]byte("1"))
				f.fail(1317, "70100", "interrupted")
			case "INFILE":
				f.write([]byte("\xfbfile.csv"))
				f.read()
				f.ok(0, 0, 0)
			case "BAD":
				f.fail(1064, "42000", "syntax")
			case "TX":
				f.ok(0, 0, StatusInTrans)
			default:
				f.write([]byte{0, 0, 0})
			}
		}
	})
	c := dialT(t, Config{Addr: addr, User: "u", Password: "pw"})
	ctx := ctxT(t)
	rows, err := c.Query(ctx, "ERR")
	if err != nil || !rows.Next() || rows.Next() {
		t.Fatal(err)
	}
	var se *Error
	if !errors.As(rows.Err(), &se) || se.Code != 1317 || rows.Close() == nil || c.Err() != nil {
		t.Fatal(rows.Err())
	}
	if _, err := c.Exec(ctx, "INFILE"); err == nil || !strings.Contains(err.Error(), "LOCAL") {
		t.Fatal(err)
	}
	if _, err := c.Exec(ctx, "BAD"); !errors.As(err, &se) || se.Code != 1064 {
		t.Fatal(err)
	}
	if _, err := c.Exec(ctx, "TX"); err != nil || !c.InTransaction() {
		t.Fatal(err)
	}
	if err := c.Commit(ctx); err == nil {
		t.Fatal("a short OK read") // the fake's OK has no status
	}
}

// broken runs one query against a fake answering it with script, and
// returns the rows' error, which must break the connection.
func broken(t *testing.T, caps uint32, script func(f *fake)) error {
	t.Helper()
	addr := serve(t, func(f *fake) {
		f.login(caps)
		f.cmd()
		script(f)
		time.Sleep(50 * time.Millisecond)
	})
	c := dialT(t, Config{Addr: addr, User: "u"})
	rows, err := c.Query(ctxT(t), "SELECT")
	if err == nil {
		for rows.Next() {
		}
		err = rows.Err()
		if rows.Close() != err {
			t.Error("Close and Err differ")
		}
	}
	if err == nil || c.Err() == nil {
		t.Fatalf("not broken: %v", err)
	}
	if !errors.Is(c.Ping(ctxT(t)), c.Err()) {
		t.Fatal("a broken connection pinged")
	}
	return err
}

func TestBrokenResults(t *testing.T) {
	col := []Column{{Name: "a", Type: TypeLong}}
	cases := []struct {
		name   string
		caps   uint32
		script func(f *fake)
		want   string
	}{
		{"closed mid-result", fakeCaps, func(f *fake) {
			f.columns(col)
			f.textRow([]byte("1"))
			f.nc.Close()
		}, "EOF"},
		{"closed mid-packet", fakeCaps, func(f *fake) {
			f.columns(col)
			f.nc.Write([]byte{10, 0, 0, f.seq, 1})
			f.nc.Close()
		}, "unexpected EOF"},
		{"closed before the columns", fakeCaps, func(f *fake) {
			f.write([]byte{1})
			f.nc.Close()
		}, "EOF"},
		{"closed before the header", fakeCaps, func(f *fake) { f.nc.Close() }, "EOF"},
		{"out of order", fakeCaps, func(f *fake) {
			f.seq = 5
			f.write([]byte{0, 0, 0, 0, 0})
		}, "packet 5, expected 1"},
		{"a column count of 0", fakeCaps, func(f *fake) { f.write([]byte{0xfc, 0, 0}) }, "column count"},
		{"a malformed column", fakeCaps, func(f *fake) {
			f.write([]byte{1})
			f.write([]byte{3, 'd'})
		}, "column definition"},
		{"no EOF after the columns", fakeCaps, func(f *fake) {
			f.write([]byte{1})
			f.write(colDef(col[0]))
			f.textRow([]byte("1"))
		}, "no EOF"},
		{"closed before the EOF", fakeCaps, func(f *fake) {
			f.write([]byte{1})
			f.write(colDef(col[0]))
			f.nc.Close()
		}, "EOF"},
		{"an empty row", fakeCaps, func(f *fake) {
			f.columns(col)
			f.write(nil)
		}, "empty row"},
		{"a short row", fakeCaps, func(f *fake) {
			f.columns(col)
			f.write([]byte{5, '1'})
		}, "text row"},
		{"a long row", fakeCaps, func(f *fake) {
			f.columns(col)
			f.textRow([]byte("1"), []byte("2"))
		}, "longer than its columns"},
		{"a bad integer", fakeCaps, func(f *fake) {
			f.columns(col)
			f.textRow([]byte("x"))
		}, "column a"},
		{"a bad date", fakeCaps, func(f *fake) {
			f.columns([]Column{{Name: "d", Type: TypeDate}})
			f.textRow([]byte("2026/11/08"))
		}, "column d"},
		{"a bad time", fakeCaps, func(f *fake) {
			f.columns([]Column{{Name: "t", Type: TypeTime}})
			f.textRow([]byte("1:2"))
		}, "column t"},
		{"a malformed end", fakeCaps | capDeprecateEOF, func(f *fake) {
			f.columns(col)
			f.write([]byte{0xfe, 0xfc})
		}, "OK packet"},
		{"a malformed OK", fakeCaps, func(f *fake) { f.write([]byte{0, 0xfd, 1}) }, "OK packet"},
		{"closed after an infile", fakeCaps, func(f *fake) {
			f.write([]byte("\xfbx"))
			f.read()
			f.nc.Close()
		}, "EOF"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := broken(t, tc.caps, tc.script)
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

func TestPacketsPastOnePayload(t *testing.T) {
	big := bytes.Repeat([]byte("0123456789abcdef"), maxPayload/16+100)
	exact := bytes.Repeat([]byte("x"), maxPayload-1) // with the command byte, one whole payload
	addr := serve(t, func(f *fake) {
		f.login(fakeCaps)
		for {
			cmd, body := f.cmd()
			if cmd != comQuery {
				return
			}
			if string(body) != "ROW" {
				f.ok(uint64(len(body)), 0, 0)
				continue
			}
			f.columns([]Column{{Name: "b", Type: TypeLongBlob}})
			f.textRow(big)
			f.textRow([]byte("small"))
			f.end(0)
		}
	})
	c := dialT(t, Config{Addr: addr, User: "u"})
	ctx := ctxT(t)
	res, err := c.Exec(ctx, string(exact))
	if err != nil || res.AffectedRows != uint64(len(exact)) {
		t.Fatal(res, err)
	}
	res, err = c.Exec(ctx, string(big))
	if err != nil || res.AffectedRows != uint64(len(big)) {
		t.Fatal(res, err)
	}
	rows, err := c.Query(ctx, "ROW")
	if err != nil {
		t.Fatal(err)
	}
	if !rows.Next() || !bytes.Equal(rows.Values()[0].([]byte), big) {
		t.Fatal("the big row", rows.Err())
	}
	if !rows.Next() || string(rows.Values()[0].([]byte)) != "small" || rows.Close() != nil {
		t.Fatal("the row after it", rows.Err())
	}
	if _, err := c.Exec(ctx, "x"); err != nil || cap(c.wbuf) > keepBuffer || cap(c.rbuf) > keepBuffer {
		t.Fatal(err, cap(c.wbuf))
	}
}

func TestPacketTooLarge(t *testing.T) {
	err := func() error {
		addr := serve(t, func(f *fake) {
			f.login(fakeCaps)
			f.cmd()
			f.columns([]Column{{Name: "b", Type: TypeBlob}})
			f.textRow(make([]byte, 2000))
		})
		c := dialT(t, Config{Addr: addr, User: "u", MaxPacketSize: 1000})
		rows, err := c.Query(ctxT(t), "SELECT")
		if err != nil {
			return err
		}
		rows.Next()
		return rows.Err()
	}()
	if !errors.Is(err, ErrPacketTooLarge) {
		t.Fatal(err)
	}
}

func TestTimeouts(t *testing.T) {
	silent := func(f *fake) {
		f.login(fakeCaps)
		f.cmd()
		f.read() // until the client leaves
	}
	c := dialT(t, Config{Addr: serve(t, silent), User: "u", Timeout: 100 * time.Millisecond})
	if _, err := c.Exec(context.Background(), "SELECT SLEEP(10)"); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("Timeout: %v", err)
	}
	c = dialT(t, Config{Addr: serve(t, silent), User: "u", Timeout: -1})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := c.Exec(ctx, "SELECT SLEEP(10)"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a context's deadline: %v", err)
	}
	c = dialT(t, Config{Addr: serve(t, silent), User: "u"})
	ctx, cancel = context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	if _, err := c.Exec(ctx, "SELECT SLEEP(10)"); !errors.Is(err, context.Canceled) {
		t.Fatalf("a context cancelled: %v", err)
	}
	// A context done before the call does not break the connection.
	c = dialT(t, Config{Addr: serve(t, func(f *fake) {
		f.login(fakeCaps)
		f.cmd()
		f.ok(0, 0, 0)
	}), User: "u"})
	if err := c.Ping(ctx); !errors.Is(err, context.Canceled) || c.Err() != nil {
		t.Fatal(err)
	}
	if err := c.Ping(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	// A dial cancelled.
	if _, err := Dial(ctx, Config{Addr: serve(t, func(f *fake) {}), User: "u"}); err == nil {
		t.Fatal("a cancelled dial")
	}
	// A greeting that never comes.
	_, err := Dial(context.Background(), Config{Addr: serve(t, func(f *fake) { f.read() }), Timeout: 100 * time.Millisecond})
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestWritesFail(t *testing.T) {
	addr := serve(t, func(f *fake) {
		f.login(fakeCaps)
	})
	c := dialT(t, Config{Addr: addr, User: "u"})
	c.raw.Conn.Close()
	for name, call := range map[string]func() error{
		"query":   func() error { _, err := c.Query(ctxT(t), "SELECT 1"); return err },
		"prepare": func() error { _, err := c.Prepare(ctxT(t), "SELECT 1"); return err },
		"ping":    func() error { return c.Ping(ctxT(t)) },
	} {
		if err := call(); err == nil {
			t.Fatalf("%s on a closed socket", name)
		}
	}
	if !errors.Is(c.Close(), nil) {
		t.Fatal("Close")
	}
}

func TestPingAnswers(t *testing.T) {
	for name, answer := range map[string][]byte{"unexpected": {7}, "empty": nil, "none": {}} {
		t.Run(name, func(t *testing.T) {
			addr := serve(t, func(f *fake) {
				f.login(fakeCaps)
				f.cmd()
				if name != "none" {
					f.write(answer)
				}
			})
			c := dialT(t, Config{Addr: addr, User: "u"})
			if err := c.Ping(ctxT(t)); err == nil || c.Err() == nil {
				t.Fatal(err)
			}
		})
	}
}

func TestValuesRoundTrip(t *testing.T) {
	loc := time.FixedZone("x", 3600)
	cases := []struct {
		arg  any
		col  Column
		want any
	}{
		{true, Column{Type: TypeTiny}, int64(1)},
		{false, Column{Type: TypeTiny}, int64(0)},
		{int8(-5), Column{Type: TypeLongLong}, int64(-5)},
		{int16(-5), Column{Type: TypeLongLong}, int64(-5)},
		{int32(-5), Column{Type: TypeLongLong}, int64(-5)},
		{int(-5), Column{Type: TypeLongLong}, int64(-5)},
		{int64(math.MinInt64), Column{Type: TypeLongLong}, int64(math.MinInt64)},
		{uint(5), Column{Type: TypeLongLong, Flags: FlagUnsigned}, uint64(5)},
		{uint8(5), Column{Type: TypeLongLong, Flags: FlagUnsigned}, uint64(5)},
		{uint16(5), Column{Type: TypeLongLong, Flags: FlagUnsigned}, uint64(5)},
		{uint32(5), Column{Type: TypeLongLong, Flags: FlagUnsigned}, uint64(5)},
		{uint64(math.MaxUint64), Column{Type: TypeLongLong, Flags: FlagUnsigned}, uint64(math.MaxUint64)},
		{float32(1.5), Column{Type: TypeFloat}, float32(1.5)},
		{2.5, Column{Type: TypeDouble}, 2.5},
		{"s", Column{Type: TypeString}, []byte("s")},
		{[]byte{0, 1}, Column{Type: TypeBlob}, []byte{0, 1}},
		{json.RawMessage(`{}`), Column{Type: TypeJSON}, []byte(`{}`)},
		{DateTime{}, Column{Type: TypeDateTime}, DateTime{}},
		{DateTime{Year: 2026, Month: 1, Day: 2}, Column{Type: TypeDate}, DateTime{Year: 2026, Month: 1, Day: 2}},
		{DateTime{Year: 2026, Month: 1, Day: 2, Second: 1}, Column{Type: TypeDateTime}, DateTime{Year: 2026, Month: 1, Day: 2, Second: 1}},
		{DateTime{2026, 1, 2, 3, 4, 5, 6}, Column{Type: TypeTimestamp}, DateTime{2026, 1, 2, 3, 4, 5, 6}},
		{time.Date(2026, 1, 2, 3, 4, 5, 6000, time.UTC), Column{Type: TypeDateTime}, DateTime{2026, 1, 2, 4, 4, 5, 6}},
		{time.Duration(0), Column{Type: TypeTime}, time.Duration(0)},
		{-(30*time.Hour + time.Second), Column{Type: TypeTime}, -(30*time.Hour + time.Second)},
		{time.Hour + time.Microsecond, Column{Type: TypeTime}, time.Hour + time.Microsecond},
	}
	for _, tc := range cases {
		ts, vs, null, err := appendParam(nil, nil, tc.arg, loc)
		if err != nil || null || len(ts) != 2 {
			t.Fatalf("%T: %v", tc.arg, err)
		}
		if tc.col.Type == TypeTiny || tc.col.Type == TypeLongLong || tc.col.Type == TypeFloat || tc.col.Type == TypeDouble {
			if ts[0] != tc.col.Type {
				t.Fatalf("%T: type %x", tc.arg, ts[0])
			}
		}
		r := rd{b: vs}
		if got := binaryValue(&tc.col, &r); !reflect.DeepEqual(got, tc.want) || r.bad || len(r.b) != 0 {
			t.Errorf("%T %v: %#v, want %#v", tc.arg, tc.arg, got, tc.want)
		}
	}
	for _, v := range []any{nil, []byte(nil), json.RawMessage(nil)} {
		if ts, _, null, _ := appendParam(nil, nil, v, loc); !null || ts[0] != TypeNull {
			t.Fatalf("%#v is not NULL", v)
		}
	}
	if _, _, _, err := appendParam(nil, nil, struct{}{}, loc); err == nil {
		t.Fatal("a struct parameter")
	}
	// Binary values of the narrower types, and malformed temporals.
	for _, tc := range []struct {
		col  Column
		b    []byte
		want any
	}{
		{Column{Type: TypeTiny}, []byte{0xff}, int64(-1)},
		{Column{Type: TypeTiny, Flags: FlagUnsigned}, []byte{0xff}, uint64(255)},
		{Column{Type: TypeShort}, []byte{0xff, 0xff}, int64(-1)},
		{Column{Type: TypeShort, Flags: FlagUnsigned}, []byte{0xff, 0xff}, uint64(65535)},
		{Column{Type: TypeYear}, []byte{0x6b, 0x08}, int64(2155)},
		{Column{Type: TypeLong}, []byte{0xff, 0xff, 0xff, 0xff}, int64(-1)},
		{Column{Type: TypeInt24, Flags: FlagUnsigned}, []byte{0xff, 0xff, 0xff, 0}, uint64(16777215)},
		{Column{Type: TypeLongLong}, []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, int64(-1)},
		{Column{Type: TypeNewDecimal}, []byte{4, '1', '.', '5', '0'}, []byte("1.50")},
	} {
		r := rd{b: tc.b}
		if got := binaryValue(&tc.col, &r); !reflect.DeepEqual(got, tc.want) || r.bad {
			t.Errorf("%x: %#v, want %#v", tc.b, got, tc.want)
		}
	}
	for _, tc := range []struct {
		typ byte
		b   []byte
	}{{TypeDateTime, []byte{5, 0, 0, 0, 0, 0}}, {TypeTime, []byte{5, 0, 0, 0, 0, 0}}, {TypeLongLong, []byte{1}}} {
		r := rd{b: tc.b}
		binaryValue(&Column{Type: tc.typ}, &r)
		if !r.bad {
			t.Errorf("%x read as type %x", tc.b, tc.typ)
		}
	}
}

func TestTemporalText(t *testing.T) {
	for _, s := range []string{"2026-11-08 1", "2026-1x-08", "2026-11-08 01:02:03.1234567", "2026-11-08 0x:02:03", "2026-11-08T01:02:03", "x026-11-08"} {
		if _, ok := parseDateTime([]byte(s)); ok {
			t.Errorf("%q parsed", s)
		}
	}
	for _, s := range []string{":00:00", "1:00", "1:0x:00", "1:00:00.", "1:00:00x1", "1234567890:00:00"} {
		if _, ok := parseTime([]byte(s)); ok {
			t.Errorf("%q parsed", s)
		}
	}
	d := DateTime{2026, 11, 8, 1, 2, 3, 4}
	if d.String() != "2026-11-08 01:02:03.000004" || (DateTime{Year: 1}).String() != "0001-00-00 00:00:00" || d.IsZero() || !(DateTime{}).IsZero() {
		t.Fatal(d.String())
	}
	if got := FromTime(d.Time(time.UTC)); got != d {
		t.Fatal(got)
	}
	c := Column{Collation: BinaryCollation, Flags: FlagUnsigned}
	if !c.IsBinary() || !c.Unsigned() {
		t.Fatal("flags")
	}
}

func TestPacketHelpers(t *testing.T) {
	for _, n := range []uint64{0, 250, 251, 65535, 65536, 1<<24 - 1, 1 << 24, math.MaxUint64} {
		r := rd{b: appendLenEnc(nil, n)}
		if got, null := r.lenEnc(); got != n || null || r.bad || len(r.b) != 0 {
			t.Errorf("%d: %d", n, got)
		}
	}
	for _, b := range [][]byte{{0xff}, {0xfc, 1}, {3, 'a'}} {
		r := rd{b: b}
		r.lenEncBytes()
		if !r.bad {
			t.Errorf("%x read", b)
		}
	}
	r := rd{b: []byte{0xfb}}
	if v := r.lenEncBytes(); v != nil || r.bad {
		t.Fatal("NULL")
	}
	r = rd{b: []byte{0}}
	if v := r.lenEncBytes(); v == nil || len(v) != 0 {
		t.Fatal("an empty string read as NULL")
	}
	r = rd{b: []byte("abc")}
	if string(r.nul()) != "abc" || r.u16() != 0 || r.u24() != 0 || r.u32() != 0 || r.u64() != 0 || !r.bad {
		t.Fatal("reads past the end")
	}
	b := grow(make([]byte, 2, 10), 3)
	if len(b) != 5 || cap(b) != 10 {
		t.Fatal(len(b), cap(b))
	}
	var h [4]byte
	putHeader(h[:], 0x030201, 9)
	if binary.LittleEndian.Uint32(h[:]) != 0x09030201 {
		t.Fatal(h)
	}
}
