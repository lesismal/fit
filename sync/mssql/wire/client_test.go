package mssqlwire

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"math"
	"net"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func dial(t *testing.T, f *fake) *Conn {
	t.Helper()
	c, err := Dial(context.Background(), f.config())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestLogin(t *testing.T) {
	f := newFake(t)
	f.start()
	var infos []string
	cfg := f.config()
	cfg.OnInfo = func(e *Error) { infos = append(infos, e.Message) }
	c, err := Dial(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Database() != "shop" || c.PacketSize() != 4096 || c.ServerVersion() != "16.0.4141" || c.Collation().CodePage() != 1252 {
		t.Fatalf("db %q packet %d version %s collation %v", c.Database(), c.PacketSize(), c.ServerVersion(), c.Collation())
	}
	if len(infos) != 1 || !strings.Contains(infos[0], "shop") {
		t.Fatalf("infos %q", infos)
	}
	if c.Err() != nil || c.InTransaction() {
		t.Fatal(c.Err())
	}
	if err1, err2 := c.Close(), c.Close(); err1 != nil || err2 != nil {
		t.Fatal("close")
	}
	if _, err := c.Exec(context.Background(), "SELECT 1"); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}

func TestLoginRefused(t *testing.T) {
	f := newFake(t)
	f.start()
	cfg := f.config()
	cfg.Password = "wrong"
	_, err := Dial(context.Background(), cfg)
	var se *Error
	if !errors.As(err, &se) || se.Number != 18456 || !strings.Contains(err.Error(), "refused the login") || !strings.Contains(err.Error(), "in proc, line 7") {
		t.Fatal(err)
	}
	f.setLogin(func(s *session, body []byte) { s.reply(tDone(tokDone, 0, 0)) })
	if _, err := Dial(context.Background(), f.config()); err == nil || !strings.Contains(err.Error(), "without a LOGINACK") {
		t.Fatal(err)
	}
	f.setLogin(func(s *session, body []byte) { s.nc.Close() })
	if _, err := Dial(context.Background(), f.config()); err == nil {
		t.Fatal("no error")
	}
}

func TestDialConfig(t *testing.T) {
	ctx := context.Background()
	for _, cfg := range []Config{{Addr: "x", PacketSize: 100}, {Addr: "x", Encrypt: "maybe"}} {
		if _, err := Dial(ctx, cfg); err == nil {
			t.Fatal("no error")
		}
	}
	// A host without a port gets 1433; nothing listens there here.
	cfg := Config{Addr: "127.0.0.1", Timeout: time.Second, Dial: func(ctx context.Context, addr string) (net.Conn, error) {
		if addr != "127.0.0.1:1433" {
			t.Errorf("addr %s", addr)
		}
		return nil, errors.New("refused")
	}}
	if _, err := Dial(ctx, cfg); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatal(err)
	}
	// A server that never answers: the login times out.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			defer c.Close()
			io := make([]byte, 1024)
			for {
				if _, err := c.Read(io); err != nil {
					return
				}
			}
		}
	}()
	start := time.Now()
	_, err := Dial(ctx, Config{Addr: ln.Addr().String(), Encrypt: EncryptOff, Timeout: 200 * time.Millisecond})
	if err == nil || time.Since(start) > 3*time.Second {
		t.Fatal(err, time.Since(start))
	}
}

func TestEncryption(t *testing.T) {
	cert, pool := testCert(t)
	for _, tc := range []struct {
		name    string
		mode    string
		enc     byte
		noEnc   bool
		strict  bool
		wantErr string
	}{
		{name: "on", mode: EncryptOn, enc: encryptOn},
		{name: "on, required", mode: "", enc: encryptReq},
		{name: "on, not supported", mode: EncryptOn, enc: encryptNotSup, wantErr: "does not offer encryption"},
		{name: "off, required", mode: EncryptOff, enc: encryptReq, wantErr: "requires encryption"},
		{name: "off, no option", mode: EncryptOff, noEnc: true, wantErr: "has no encryption"},
		{name: "strict", mode: EncryptStrict, strict: true, noEnc: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t)
			f.cert, f.enc, f.noEnc, f.strict = cert, tc.enc, tc.noEnc, tc.strict
			f.handle = func(s *session, typ byte, body []byte) {
				s.reply(tColMeta(Column{Name: "x", Type: TypeIntN, Size: 4}), tRow(t, []Column{{Type: TypeIntN, Size: 4}}, 7), tDone(tokDone, doneCount, 1))
			}
			f.start()
			cfg := f.config()
			cfg.Encrypt = tc.mode
			cfg.TLS = &tls.Config{RootCAs: pool}
			c, err := Dial(context.Background(), cfg)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatal(err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			_, rows, err := c.QueryAll(context.Background(), "SELECT 7")
			if err != nil || rows[0][0] != int64(7) {
				t.Fatal(rows, err)
			}
		})
	}
	// A certificate the roots do not hold is refused.
	f := newFake(t)
	f.cert, f.enc = cert, encryptOn
	f.start()
	cfg := f.config()
	cfg.Encrypt = EncryptOn
	if _, err := Dial(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "TLS") {
		t.Fatal(err)
	}
	f = newFake(t)
	f.cert, f.strict, f.noEnc = cert, true, true
	f.start()
	cfg = f.config()
	cfg.Encrypt = EncryptStrict
	if _, err := Dial(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "TLS") {
		t.Fatal(err)
	}
}

// allTypes are a column of each type, and a value of it, as sent and as
// read back.
func allTypes() (cols []Column, in, out []any) {
	day := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	at := time.Date(2026, 11, 10, 13, 14, 15, 123456700, time.UTC)
	zone := time.FixedZone("", -(7*3600 + 30*60))
	coll := Collation{0x09, 0x04, 0xd0, 0x00, 0x34}
	utf := Collation{0x09, 0x04, 0xd0, 0x04, 0x00}
	u, _ := ParseUUID("6F9619FF-8B86-D011-B42D-00C04FC964FF")
	long := strings.Repeat("long ünïcode 𝄞 ", 1000)
	blob := bytes.Repeat([]byte{1, 2, 3, 250}, 5000)
	add := func(c Column, v, w any) {
		cols = append(cols, c)
		in = append(in, v)
		out = append(out, w)
	}
	add(Column{Type: TypeInt1, Size: 1}, 200, int64(200))
	add(Column{Type: TypeBit, Size: 1}, true, true)
	add(Column{Type: TypeInt2, Size: 2}, int16(-300), int64(-300))
	add(Column{Type: TypeInt4, Size: 4}, int32(-70000), int64(-70000))
	add(Column{Type: TypeInt8, Size: 8}, int64(math.MinInt64), int64(math.MinInt64))
	add(Column{Type: TypeIntN, Size: 8}, uint32(9), int64(9))
	add(Column{Type: TypeIntN, Size: 4}, nil, nil)
	add(Column{Type: TypeBitN, Size: 1}, 0, false)
	add(Column{Type: TypeFloat4, Size: 4}, float32(1.5), float32(1.5))
	add(Column{Type: TypeFloat8, Size: 8}, 2.25, 2.25)
	add(Column{Type: TypeFloatN, Size: 8}, 3, 3.0)
	add(Column{Type: TypeMoney, Size: 8}, "-922337203685477.5808", Decimal("-922337203685477.5808"))
	add(Column{Type: TypeMoney4, Size: 4}, 12.34, Decimal("12.3400"))
	add(Column{Type: TypeMoneyN, Size: 8}, Decimal("0.0001"), Decimal("0.0001"))
	add(Column{Type: TypeDecimalN, Precision: 38, Scale: 10, Size: 17}, "-1234567890123456789012345678.0123456789", Decimal("-1234567890123456789012345678.0123456789"))
	add(Column{Type: TypeNumericN, Precision: 5, Scale: 2, Size: 5}, "1.005", Decimal("1.01"))
	add(Column{Type: TypeDecimalN, Precision: 18, Scale: 0, Size: 9}, int64(-5), Decimal("-5"))
	add(Column{Type: TypeDecimalN, Precision: 28, Scale: 3, Size: 13}, Decimal(".5"), Decimal("0.500"))
	add(Column{Type: TypeDateTime, Size: 8}, at, time.Date(2026, 11, 10, 13, 14, 15, 123000000, time.UTC))
	add(Column{Type: TypeDateTimeN, Size: 8}, time.Date(1899, 12, 31, 23, 59, 59, 998000000, time.UTC), time.Date(1899, 12, 31, 23, 59, 59, 997000000, time.UTC))
	add(Column{Type: TypeDateTime4, Size: 4}, time.Date(2000, 1, 1, 10, 20, 31, 0, time.UTC), time.Date(2000, 1, 1, 10, 21, 0, 0, time.UTC))
	add(Column{Type: TypeDateN, Size: 3}, day, day)
	add(Column{Type: TypeDateN, Size: 3}, time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC), time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC))
	add(Column{Type: TypeTimeN, Scale: 7, Size: 5}, 13*time.Hour+time.Nanosecond*100, 13*time.Hour+time.Nanosecond*100)
	add(Column{Type: TypeTimeN, Scale: 0, Size: 3}, at, 13*time.Hour+14*time.Minute+15*time.Second)
	add(Column{Type: TypeTimeN, Scale: 3, Size: 4}, 25*time.Hour, nil)
	add(Column{Type: TypeDateTime2N, Scale: 7, Size: 8}, at, at)
	add(Column{Type: TypeDateTime2N, Scale: 2, Size: 6}, at, time.Date(2026, 11, 10, 13, 14, 15, 120000000, time.UTC))
	add(Column{Type: TypeDateTimeOffset, Scale: 7, Size: 10}, at.In(zone), at.In(zone))
	add(Column{Type: TypeGUID, Size: 16}, u, u)
	add(Column{Type: TypeGUID, Size: 16}, "{6f9619ff-8b86-d011-b42d-00c04fc964ff}", u)
	add(Column{Type: TypeBigVarChar, Size: 100, Collation: coll}, "café €", "café €")
	add(Column{Type: TypeBigChar, Size: 4, Collation: coll}, "ab  ", "ab  ")
	add(Column{Type: TypeBigVarChar, Size: -1, Collation: utf}, long, long)
	add(Column{Type: TypeBigVarChar, Size: 10, Collation: coll}, []byte("raw"), "raw")
	add(Column{Type: TypeNVarChar, Size: 200, Collation: coll}, "日本語 𝄞", "日本語 𝄞")
	add(Column{Type: TypeNVarChar, Size: -1, Collation: coll}, long, long)
	add(Column{Type: TypeNVarChar, Size: -1, Collation: coll}, "", "")
	add(Column{Type: TypeNVarChar, Size: -1, Collation: coll}, nil, nil)
	add(Column{Type: TypeNChar, Size: 20, Collation: coll}, []byte("bytes"), "bytes")
	add(Column{Type: TypeNVarChar, Size: 40, Collation: coll}, Decimal("1.5"), "1.5")
	add(Column{Type: TypeBigVarBinary, Size: 8000}, []byte{0, 1, 2}, []byte{0, 1, 2})
	add(Column{Type: TypeBigVarBinary, Size: -1}, blob, blob)
	add(Column{Type: TypeBigBinary, Size: 8, UserType: userTypeTimestamp}, "12345678", []byte("12345678"))
	add(Column{Type: TypeXML, Size: -1}, "<a>1</a>", "<a>1</a>")
	add(Column{Type: TypeText, Size: math.MaxInt32, Collation: coll}, "text", "text")
	add(Column{Type: TypeNText, Size: math.MaxInt32 - 1, Collation: coll}, "ntext", "ntext")
	add(Column{Type: TypeImage, Size: math.MaxInt32}, []byte{9}, []byte{9})
	add(Column{Type: TypeImage, Size: math.MaxInt32}, nil, nil)
	add(Column{Type: TypeUDT, Size: -1, UDTName: "hierarchyid"}, []byte{0x58}, []byte{0x58})
	for i := range cols {
		cols[i].Name = "c" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		cols[i].Flags = 1
	}
	return cols, in, out
}

func TestTypesRoundTrip(t *testing.T) {
	cols, in, out := allTypes()
	// time(3) of 25h is refused: take it out of the row.
	for i := range cols {
		if d, ok := in[i].(time.Duration); ok && d >= 24*time.Hour {
			cols, in, out = append(cols[:i:i], cols[i+1:]...), append(in[:i:i], in[i+1:]...), append(out[:i:i], out[i+1:]...)
			break
		}
	}
	f := newFake(t)
	f.replySize = 512
	f.handle = func(s *session, typ byte, body []byte) {
		s.reply(tColMeta(cols...), tRow(t, cols, in...), tNBCRow(t, cols, in...), tDone(tokDone, doneCount, 2))
	}
	f.start()
	c := dial(t, f)
	rows, err := c.Query(context.Background(), "SELECT *")
	if err != nil {
		t.Fatal(err)
	}
	if got := rows.Columns(); len(got) != len(cols) || got[0].Name != cols[0].Name || !got[0].Nullable() {
		t.Fatalf("columns %+v", got)
	}
	n := 0
	for rows.Next() {
		n++
		for i, v := range rows.Values() {
			if !reflect.DeepEqual(v, out[i]) {
				t.Errorf("row %d %s (%s): %#v, want %#v", n, cols[i].Name, cols[i].TypeName(), v, out[i])
			}
		}
	}
	if err := rows.Close(); err != nil || n != 2 || rows.Affected() != 2 {
		t.Fatal(err, n)
	}
}

func TestParamsRoundTrip(t *testing.T) {
	f := newFake(t)
	var got []rpcParam
	var gotTx [8]byte
	f.handle = func(s *session, typ byte, body []byte) {
		var proc string
		proc, gotTx, got = parseRPC(t, body)
		if proc != "10" || typ != pktRPC {
			t.Errorf("proc %s type %d", proc, typ)
		}
		var cols []Column
		var vals []any
		for _, p := range got[2:] {
			p.col.Name = p.name
			cols = append(cols, p.col)
			vals = append(vals, p.value)
		}
		s.reply(tColMeta(cols...), tRow(t, cols, vals...), tDone(tokDoneInProc, doneCount, 1), tReturnStatus(0), tDone(tokDoneProc, doneCount, 9))
	}
	f.start()
	c := dial(t, f)
	u, _ := ParseUUID("6F9619FF-8B86-D011-B42D-00C04FC964FF")
	at := time.Date(2026, 1, 2, 3, 4, 5, 600, time.FixedZone("", 3600))
	long := strings.Repeat("x", 5000)
	args := []any{nil, true, 42, uint8(7), float32(0.5), 0.25, "s", long, []byte{1}, make([]byte, 9000), at, 90 * time.Minute, Decimal("-12.345"), u,
		Param{Name: "@named", Value: "v", Type: "varchar(10)"}, Param{Value: "2026-01-02", Type: "date"}}
	want := []any{nil, true, int64(42), int64(7), float32(0.5), 0.25, "s", long, []byte{1}, make([]byte, 9000), at, 90 * time.Minute, Decimal("-12.345"), u,
		"v", nil}
	args[len(args)-1] = Param{Value: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), Type: "date"}
	want[len(want)-1] = time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	_, rows, err := c.QueryAll(context.Background(), "SELECT @p1", args...)
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range rows[0] {
		w := want[i]
		if tm, ok := w.(time.Time); ok && !tm.Equal(v.(time.Time)) || !ok && !reflect.DeepEqual(v, w) {
			t.Errorf("arg %d: %#v, want %#v", i, v, w)
		}
	}
	if got[0].name != "@stmt" || got[0].value != "SELECT @p1" || !strings.HasPrefix(got[1].value.(string), "@p1 nvarchar(4000),@p2 bit,@p3 bigint") ||
		!strings.Contains(got[1].value.(string), "@named varchar(10),@p16 date") || got[16].name != "@named" || gotTx != [8]byte{} {
		t.Fatalf("%+v", got[:2])
	}
	if n, err := c.Exec(context.Background(), "UPDATE", 1); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	for _, bad := range []any{struct{}{}, Param{Name: "x", Value: 1}, Param{Value: 1, Type: "nope"}, Param{Value: "x", Type: "int"}, Decimal("1." + strings.Repeat("1", 39))} {
		if _, err := c.Exec(context.Background(), "X", bad); err == nil {
			t.Fatalf("%v: no error", bad)
		}
	}
}

func TestCall(t *testing.T) {
	f := newFake(t)
	f.handle = func(s *session, typ byte, body []byte) {
		proc, _, params := parseRPC(t, body)
		if proc != "dbo.p" || len(params) != 2 || !params[1].output {
			t.Errorf("%s %+v", proc, params)
		}
		col := Column{Name: "n", Type: TypeIntN, Size: 4}
		s.reply(tColMeta(col), tRow(t, []Column{col}, 1), tDone(tokDoneInProc, doneCount|doneMore, 1),
			tColMeta(col), tRow(t, []Column{col}, 2), tRow(t, []Column{col}, 3),
			tColMeta(Column{Name: "m", Type: TypeNVarChar, Size: 20}), tRow(t, []Column{{Type: TypeNVarChar, Size: 20}}, "m"),
			tDone(tokDoneInProc, doneCount|doneMore, 1),
			tReturnValue(t, "@out", Column{Type: TypeIntN, Size: 4}, 99), tReturnStatus(-3), tDone(tokDoneProc, doneCount, 3))
	}
	f.start()
	c := dial(t, f)
	rows, err := c.Call(context.Background(), "dbo.p", Param{Name: "@in", Value: 5}, Param{Name: "@out", Value: 0, Type: "int", Output: true})
	if err != nil {
		t.Fatal(err)
	}
	var sets [][]any
	for {
		var set []any
		for rows.Next() {
			set = append(set, rows.Values()[0])
		}
		sets = append(sets, set)
		if !rows.NextResultSet() {
			break
		}
	}
	if !reflect.DeepEqual(sets, [][]any{{int64(1)}, {int64(2), int64(3)}, {"m"}}) || rows.Err() != nil {
		t.Fatal(sets, rows.Err())
	}
	if st, ok := rows.ReturnStatus(); !ok || st != -3 || rows.Outputs()["@out"] != int64(99) || rows.Affected() != 2 {
		t.Fatal(st, ok, rows.Outputs(), rows.Affected())
	}
	if rows.NextResultSet() || rows.Next() || rows.Close() != nil {
		t.Fatal("after the end")
	}
	if _, err := c.Call(context.Background(), "p", Param{Name: "nope"}); err == nil {
		t.Fatal("no error")
	}
	if _, err := c.Call(context.Background(), "p", Param{Name: "@x", Value: "a", Type: "int"}); err == nil {
		t.Fatal("no error")
	}
}

func TestServerErrors(t *testing.T) {
	f := newFake(t)
	f.handle = func(s *session, typ byte, body []byte) {
		sql, _ := batchSQL(body)
		col := Column{Name: "n", Type: TypeIntN, Size: 4}
		switch sql {
		case "two errors":
			s.reply(tError(tokError, 8134, 16, "Divide by zero error encountered."), tError(tokError, 3621, 16, "The statement has been terminated."), tDone(tokDone, doneError, 0))
		case "error in rows":
			s.reply(tColMeta(col), tRow(t, []Column{col}, 1), tError(tokError, 245, 16, "Conversion failed"), tDone(tokDone, doneError, 0))
		case "fatal":
			s.reply(tError(tokError, 21, 20, "fatal"), tDone(tokDone, doneError|doneSrvError, 0))
		case "no metadata":
			s.reply(binaryLE16([]byte{tokColMetadata}, 0xffff), tDone(tokDone, 0, 0))
		default:
			s.reply(tDone(tokDone, doneCount, 3))
		}
	}
	f.start()
	c := dial(t, f)
	ctx := context.Background()
	_, err := c.Exec(ctx, "two errors")
	var se *Error
	if !errors.As(err, &se) || se.Number != 8134 || len(se.More) != 1 || se.More[0].Number != 3621 || c.Err() != nil {
		t.Fatal(err)
	}
	if _, err := c.Query(ctx, "two errors"); !errors.As(err, &se) {
		t.Fatal(err)
	}
	rows, err := c.Query(ctx, "error in rows")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
	}
	if !errors.As(rows.Err(), &se) || se.Number != 245 || !errors.As(rows.Close(), &se) {
		t.Fatal(rows.Err())
	}
	rows, err = c.Query(ctx, "no metadata")
	if err != nil || rows.Next() || rows.Columns() != nil {
		t.Fatal(err)
	}
	if n, err := c.Exec(ctx, "ok"); err != nil || n != 3 {
		t.Fatal(n, err)
	}
	if _, err := c.Exec(ctx, "fatal"); err == nil || c.Err() == nil {
		t.Fatal(err)
	}
	if _, err := c.Exec(ctx, "ok"); err == nil {
		t.Fatal("a broken connection works")
	}
}

func binaryLE16(b []byte, v uint16) []byte { return append(b, byte(v), byte(v>>8)) }

func TestTransactions(t *testing.T) {
	f := newFake(t)
	desc := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	var seen [][8]byte
	var sqls []string
	f.handle = func(s *session, typ byte, body []byte) {
		sql, tx := batchSQL(body)
		seen, sqls = append(seen, tx), append(sqls, sql)
		switch {
		case strings.HasSuffix(sql, "BEGIN TRANSACTION"):
			if strings.Contains(sql, "noop") {
				s.reply(tDone(tokDone, 0, 0))
				return
			}
			s.reply(tEnv(envBeginTx, append([]byte{8}, desc...)), tDone(tokDone, 0, 0))
		case strings.Contains(sql, "COMMIT"):
			s.reply(tEnv(envCommitTx, []byte{0}), tDone(tokDone, 0, 0))
		case strings.Contains(sql, "ROLLBACK"):
			s.reply(tEnv(envRollbackTx, []byte{0}), tDone(tokDone, 0, 0))
		default:
			s.reply(tDone(tokDone, 0, 0))
		}
	}
	f.start()
	c := dial(t, f)
	ctx := context.Background()
	if err := c.Begin(ctx, "snapshot"); err != nil || !c.InTransaction() {
		t.Fatal(err)
	}
	c.Exec(ctx, "work")
	if err := c.Commit(ctx); err != nil || c.InTransaction() {
		t.Fatal(err)
	}
	c.Begin(ctx, "")
	if err := c.Rollback(ctx); err != nil || c.InTransaction() {
		t.Fatal(err)
	}
	if sqls[0] != "SET TRANSACTION ISOLATION LEVEL SNAPSHOT; BEGIN TRANSACTION" || seen[1] != [8]byte(desc) || seen[0] != [8]byte{} || seen[3] != [8]byte{} {
		t.Fatal(sqls, seen)
	}
	if err := c.Begin(ctx, "chaos"); err == nil {
		t.Fatal("no error")
	}
	if err := c.Begin(ctx, "read committed"); err != nil {
		t.Fatal(err)
	}
	c.Rollback(ctx)
	f.setHandle(func(s *session, typ byte, body []byte) { s.reply(tDone(tokDone, 0, 0)) })
	if err := c.Begin(ctx, ""); err == nil || !strings.Contains(err.Error(), "no transaction") {
		t.Fatal(err)
	}
}

func TestCancel(t *testing.T) {
	f := newFake(t)
	col := Column{Name: "n", Type: TypeIntN, Size: 4}
	var attns atomic.Int32
	f.handle = func(s *session, typ byte, body []byte) {
		sql, _ := batchSQL(body)
		switch sql {
		case "slow":
			// Rows, then nothing until the attention.
			s.sendPart(pktReply, append(tColMeta(col), tRow(t, []Column{col}, 1)...), false)
			typ, _, err := s.readMsg()
			if err != nil || typ != pktAttention {
				return
			}
			attns.Add(1)
			s.reply(tRow(t, []Column{col}, 2), tDone(tokDone, doneAttn, 0))
		case "deaf":
			// Answers neither the query nor the attention.
			s.readMsg()
		case "late":
			// The answer whole, then the attention's in a message of its
			// own.
			time.Sleep(50 * time.Millisecond)
			s.reply(tDone(tokDone, doneCount, 1))
			if typ, _, err := s.readMsg(); err == nil && typ == pktAttention {
				attns.Add(1)
				s.reply(tDone(tokDone, doneAttn, 0))
			}
		default:
			s.reply(tColMeta(col), tRow(t, []Column{col}, 1), tRow(t, []Column{col}, 2), tDone(tokDone, doneCount, 2))
		}
	}
	f.start()
	c := dial(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, _, err := c.QueryAll(ctx, "slow")
	if !errors.Is(err, context.DeadlineExceeded) || attns.Load() != 1 || c.Err() != nil {
		t.Fatal(err, attns.Load())
	}
	// The connection goes on.
	if _, rows, err := c.QueryAll(context.Background(), "fast"); err != nil || len(rows) != 2 {
		t.Fatal(rows, err)
	}
	// Rows closed early cancel the rest.
	rows, err := c.Query(context.Background(), "slow")
	if err != nil || !rows.Next() {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil || attns.Load() != 2 {
		t.Fatal(err)
	}
	// A context already ended sends nothing.
	done, stop := context.WithCancel(context.Background())
	stop()
	if _, err := c.Exec(done, "fast"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// An attention after the answer is read before the next call.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel2()
	if _, err := c.Exec(ctx2, "late"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if _, rows, err := c.QueryAll(context.Background(), "fast"); err != nil || len(rows) != 2 {
		t.Fatal(rows, err)
	}
	// A server deaf to the attention: the connection is closed.
	ctx3, cancel3 := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel3()
	defer func(w time.Duration) { attentionWait = w }(attentionWait)
	attentionWait = 100 * time.Millisecond
	start := time.Now()
	if _, err := c.Exec(ctx3, "deaf"); !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "did not answer") || c.Err() == nil {
		t.Fatal(err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("slow", time.Since(start))
	}
}

func TestCutMidResult(t *testing.T) {
	f := newFake(t)
	col := Column{Name: "s", Type: TypeNVarChar, Size: -1}
	f.handle = func(s *session, typ byte, body []byte) {
		sql, _ := batchSQL(body)
		row := tRow(t, []Column{col}, strings.Repeat("x", 10000))
		switch sql {
		case "cut":
			s.sendPart(pktReply, append(tColMeta(col), row[:len(row)/2]...), false)
			s.nc.Close()
		case "silent":
			s.sendPart(pktReply, append(tColMeta(col), row[:100]...)[:50], false)
			// and nothing more
		}
	}
	f.start()
	ctx := context.Background()
	c := dial(t, f)
	if _, _, err := c.QueryAll(ctx, "cut"); err == nil || c.Err() == nil {
		t.Fatal(err)
	}
	cfg := f.config()
	cfg.Timeout = 200 * time.Millisecond
	c2, err := Dial(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	start := time.Now()
	if _, _, err := c2.QueryAll(ctx, "silent"); err == nil || time.Since(start) > 3*time.Second {
		t.Fatal(err)
	}
}

func TestBulk(t *testing.T) {
	f := newFake(t)
	coll := Collation{0x09, 0x04, 0xd0, 0x00, 0x34}
	cols := []Column{{Name: "id", Type: TypeIntN, Size: 4}, {Name: "name", Type: TypeNVarChar, Size: 100, Collation: coll, Flags: 1},
		{Name: "doc", Type: TypeBigVarChar, Size: -1, Collation: coll, Flags: 1}, {Name: "t", Type: TypeText, Size: math.MaxInt32, Collation: coll, Flags: 1}}
	var sqls []string
	var loaded [][]any
	var meta []Column
	f.handle = func(s *session, typ byte, body []byte) {
		if typ == pktBulk {
			r := bytesReader(body)
			resp := &response{c: &Conn{cfg: Config{MaxValueBytes: 1 << 20}}}
			resp.c.msg, resp.begun = *r, true
			for {
				tok, err := resp.step()
				if err != nil {
					t.Error(err)
					return
				}
				if tok == tokRow {
					loaded = append(loaded, append([]any(nil), resp.vals...))
				}
				if resp.final {
					break
				}
			}
			meta = resp.cols
			s.reply(tDone(tokDone, doneCount, uint64(len(loaded))))
			return
		}
		sql, _ := batchSQL(body)
		if typ == pktRPC {
			_, _, ps := parseRPC(t, body)
			sql = ps[0].value.(string)
		}
		sqls = append(sqls, sql)
		switch {
		case strings.HasPrefix(sql, "SELECT TOP 0"):
			s.reply(tColMeta(cols...), tDone(tokDone, doneCount, 0))
		case strings.HasPrefix(sql, "SELECT name, collation_name"):
			c2 := []Column{{Name: "name", Type: TypeNVarChar, Size: 256}, {Name: "collation_name", Type: TypeNVarChar, Size: 256}}
			s.reply(tColMeta(c2...), tRow(t, c2, "name", "Latin1_General_CI_AS"), tDone(tokDone, doneCount, 1))
		case strings.HasPrefix(sql, "INSERT BULK"):
			s.reply(tDone(tokDone, 0, 0))
		default:
			s.reply(tError(tokError, 1, 16, "no"), tDone(tokDone, doneError, 0))
		}
	}
	f.start()
	c := dial(t, f)
	ctx := context.Background()
	b, err := c.BulkInsert(ctx, "dbo.t", []string{"id", "name", "doc", "t"}, BulkOptions{CheckConstraints: true, FireTriggers: true, KeepNulls: true, TabLock: true, RowsPerBatch: 10, Order: []string{"id", "name desc"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Columns()) != 4 {
		t.Fatal(b.Columns())
	}
	big := strings.Repeat("y", 20000)
	if err := b.WriteRow(1, "a", big, "t"); err != nil {
		t.Fatal(err)
	}
	if err := b.WriteRow("x", nil, nil, nil); err == nil {
		t.Fatal("a bad value taken")
	}
	if err := b.WriteRow(1); err == nil {
		t.Fatal("a short row taken")
	}
	if err := b.WriteRow(2, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	n, err := b.Close()
	if err != nil || n != 2 {
		t.Fatal(n, err)
	}
	if _, err := b.Close(); err == nil || b.WriteRow(1, nil, nil, nil) == nil {
		t.Fatal("closed twice")
	}
	want := "INSERT BULK dbo.t ([id] int, [name] nvarchar(50) COLLATE Latin1_General_CI_AS, [doc] varchar(max), [t] varchar(max)) WITH (CHECK_CONSTRAINTS, FIRE_TRIGGERS, KEEP_NULLS, TABLOCK, ROWS_PER_BATCH = 10, ORDER([id], [name] DESC))"
	if sqls[0] != "SELECT TOP 0 [id], [name], [doc], [t] FROM dbo.t" || sqls[2] != want {
		t.Fatalf("%q", sqls)
	}
	if !reflect.DeepEqual(loaded, [][]any{{int64(1), "a", big, "t"}, {int64(2), nil, nil, nil}}) || len(meta) != 4 || meta[1].Name != "name" {
		t.Fatalf("%v %v", loaded, meta)
	}
	// Refusals.
	if _, err := c.BulkInsert(ctx, "dbo.t", nil, BulkOptions{}); err == nil {
		t.Fatal("no columns")
	}
	if _, err := c.BulkInsert(ctx, "dbo.t", []string{"id"}, BulkOptions{Order: []string{"id sideways"}}); err == nil {
		t.Fatal("bad order")
	}
	cols = append(cols, Column{Name: "v", Type: TypeVariant, Size: 8016})
	if _, err := c.BulkInsert(ctx, "dbo.t", []string{"id", "name", "doc", "t", "v"}, BulkOptions{}); err == nil {
		t.Fatal("sql_variant")
	}
	cols = cols[:1]
	f.handle = func(s *session, typ byte, body []byte) {
		sql, _ := batchSQL(body)
		if strings.HasPrefix(sql, "SELECT TOP 0") {
			s.reply(tColMeta(cols...), tDone(tokDone, 0, 0))
			return
		}
		s.reply(tError(tokError, 4861, 16, "no bulk"), tDone(tokDone, doneError, 0))
	}
	if _, err := c.BulkInsert(ctx, "dbo.t", []string{"id"}, BulkOptions{}); err == nil {
		t.Fatal("refused INSERT BULK")
	}
}
