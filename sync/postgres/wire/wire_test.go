package pgwire

import (
	"encoding/binary"
	"errors"
	"io"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The RFC 7677 exchange, with the client's nonce fixed.
func TestSCRAMVector(t *testing.T) {
	s := &scram{password: "pencil", gs2: "n,,"}
	first := s.clientFirst("user", "rOprNGfwEbeRWgbNEkqO")
	if string(first) != "n,,n=user,r=rOprNGfwEbeRWgbNEkqO" {
		t.Fatalf("client first %q", first)
	}
	final, err := s.serverFirst([]byte("r=rOprNGfwEbeRWgbNEkqO%hvYDpWUa2RaTCAfuxFIlj)hNlF$k0,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=4096"))
	if err != nil {
		t.Fatal(err)
	}
	want := "c=biws,r=rOprNGfwEbeRWgbNEkqO%hvYDpWUa2RaTCAfuxFIlj)hNlF$k0,p=dHzbZapWIk4jUhN+Ute9ytag9zjfMHgsqmmiz7AndVQ="
	if string(final) != want {
		t.Fatalf("client final %q, want %q", final, want)
	}
	if err := s.serverFinal([]byte("v=6rriTRBi23WpRR/wtup+mMhUZUn/dB5nLTJRsjl95G4=")); err != nil {
		t.Fatal(err)
	}
	if err := s.serverFinal([]byte("v=AAAATRBi23WpRR/wtup+mMhUZUn/dB5nLTJRsjl95G4=")); err == nil {
		t.Fatal("a wrong server signature was accepted")
	}
	if _, err := s.serverFirst([]byte("r=other,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=4096")); err == nil {
		t.Fatal("a nonce not extending the client's was accepted")
	}
	if _, err := s.serverFirst([]byte("r=rOprNGfwEbeRWgbNEkqOx,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=999999999")); err == nil {
		t.Fatal("unbounded iterations were accepted")
	}
}

func TestMD5(t *testing.T) {
	addr := fakeServer(t, func(f *fakeConn) {
		f.startup()
		f.send('R', u32(5), []byte{1, 2, 3, 4})
	})
	_, err := Connect(ctx(t), Config{Addr: addr, User: "u", Password: "p", SSLMode: SSLDisable})
	if !errors.Is(err, ErrMD5Refused) {
		t.Fatalf("MD5 not refused: %v", err)
	}

	got := make(chan string, 1)
	addr = fakeServer(t, func(f *fakeConn) {
		f.startup()
		f.send('R', u32(5), []byte{1, 2, 3, 4})
		_, b := f.read()
		got <- strings.TrimRight(string(b), "\x00")
		f.ready()
		f.read()
	})
	c, err := Connect(ctx(t), Config{Addr: addr, User: "u", Password: "p", SSLMode: SSLDisable, AllowMD5: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if p := <-got; p != md5Password("u", "p", []byte{1, 2, 3, 4}) || !strings.HasPrefix(p, "md5") {
		t.Fatalf("MD5 password %q", p)
	}
	if c.ServerVersion() != 160004 || c.BackendPID() != 42 {
		t.Fatalf("version %d, pid %d", c.ServerVersion(), c.BackendPID())
	}
}

func TestCleartextAndTLSRefused(t *testing.T) {
	addr := fakeServer(t, func(f *fakeConn) {
		f.startup()
		f.send('R', u32(3))
	})
	if _, err := Connect(ctx(t), Config{Addr: addr, User: "u", SSLMode: SSLDisable}); !errors.Is(err, ErrCleartextRefused) {
		t.Fatalf("cleartext not refused: %v", err)
	}
	addr = fakeServer(t, func(f *fakeConn) {
		f.startup() // the SSLRequest
		f.nc.Write([]byte{'N'})
	})
	if _, err := Connect(ctx(t), Config{Addr: addr, User: "u"}); !errors.Is(err, ErrTLSRefused) {
		t.Fatalf("no TLS not refused: %v", err)
	}
	if _, err := Connect(ctx(t), Config{Addr: addr, SSLMode: SSLDisable, ChannelBinding: ChannelBindingRequire}); err == nil {
		t.Fatal("channel binding without TLS accepted")
	}
}

// A server that closes the connection in the middle of a result is an
// error, not a hang.
func TestCloseMidResult(t *testing.T) {
	addr := fakeServer(t, func(f *fakeConn) {
		f.startup()
		f.ready()
		for {
			typ, _ := f.read()
			if typ == 'S' {
				break
			}
		}
		f.send('1')
		f.send('t', u16(0))
		f.send('T', u16(1), []byte("n\x00"), u32(0), u16(0), u32(OIDInt4), u16(4), u32(0xFFFFFFFF), u16(0))
		f.send('Z', []byte{'I'})
		for {
			typ, _ := f.read()
			if typ == 'S' {
				break
			}
		}
		f.send('2')
		f.send('D', u16(1), u32(4), u32(7))
		// and the connection closes
	})
	c, err := Connect(ctx(t), Config{Addr: addr, User: "u", SSLMode: SSLDisable})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := c.Query(ctx(t), "SELECT n FROM t")
	if err != nil {
		t.Fatal(err)
	}
	if !rows.Next() || rows.Values()[0] != int64(7) {
		t.Fatalf("first row: %v %v", rows.Values(), rows.Err())
	}
	if rows.Next() {
		t.Fatal("a row after the close")
	}
	if err := rows.Err(); err == nil || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err %v", err)
	}
	if c.Err() == nil {
		t.Fatal("the connection is not broken")
	}
}

func TestErrorKeepsConnection(t *testing.T) {
	addr := fakeServer(t, func(f *fakeConn) {
		f.startup()
		f.ready()
		f.read()
		f.send('E', []byte("SERROR\x00VERROR\x00C42P01\x00Mrelation \"x\" does not exist\x00\x00"))
		f.send('Z', []byte{'I'})
		f.read()
		f.send('T', u16(1), []byte("one\x00"), u32(0), u16(0), u32(OIDInt4), u16(4), u32(0xFFFFFFFF), u16(0))
		f.send('D', u16(1), u32(1), []byte("1"))
		f.send('C', []byte("SELECT 1\x00"))
		f.send('Z', []byte{'I'})
		f.read()
	})
	c, err := Connect(ctx(t), Config{Addr: addr, User: "u", SSLMode: SSLDisable})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.SimpleQuery(ctx(t), "SELECT * FROM x")
	var pe *Error
	if !errors.As(err, &pe) || pe.Code != "42P01" {
		t.Fatalf("err %v", err)
	}
	res, err := c.SimpleQuery(ctx(t), "SELECT 1")
	if err != nil || len(res) != 1 || string(res[0].Rows[0][0]) != "1" || res[0].Tag != "SELECT 1" {
		t.Fatalf("%v %+v", err, res)
	}
}

func numeric(weight int16, sign uint16, dscale uint16, digits ...uint16) []byte {
	b := append(u16(uint16(len(digits))), u16(uint16(weight))...)
	b = append(append(b, u16(sign)...), u16(dscale)...)
	for _, d := range digits {
		b = append(b, u16(d)...)
	}
	return b
}

func TestDecodeNumeric(t *testing.T) {
	for _, c := range []struct {
		b    []byte
		want string
	}{
		{numeric(1, 0, 3, 1, 2345, 6780), "12345.678"},
		{numeric(-1, 0x4000, 4, 12), "-0.0012"},
		{numeric(0, 0, 0), "0"},
		{numeric(0, 0, 2), "0.00"},
		{numeric(2, 0, 0, 1), "100000000"},
		{numeric(0, 0, 10, 3, 1415, 9265, 3589), "3.1415926535"},
		{numeric(0, 0xC000, 0), "NaN"},
		{numeric(0, 0xF000, 0), "-Infinity"},
	} {
		got, err := decodeNumeric(c.b)
		if err != nil || got != c.want {
			t.Errorf("%v: %q %v, want %q", c.b, got, err, c.want)
		}
	}
	if _, err := decodeNumeric([]byte{0, 5, 0}); err == nil {
		t.Error("a short numeric decoded")
	}
}

func TestDecodeArray(t *testing.T) {
	// int4[2][2] = {{1,2},{3,NULL}}
	b := append(u32(2), u32(1)...)
	b = append(b, u32(OIDInt4)...)
	b = append(append(append(b, u32(2)...), u32(1)...), append(u32(2), u32(1)...)...)
	for _, v := range []uint32{1, 2, 3} {
		b = append(append(b, u32(4)...), u32(v)...)
	}
	b = append(b, u32(0xFFFFFFFF)...)
	got, err := decodeBinary(1007, b)
	want := []any{[]any{int64(1), int64(2)}, []any{int64(3), nil}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := decodeBinary(1007, b[:len(b)-6]); err == nil {
		t.Fatal("a short array decoded")
	}
	if got, err := decodeBinary(1007, append(u32(0), make([]byte, 8)...)); err != nil || !reflect.DeepEqual(got, []any{}) {
		t.Fatalf("empty array %v %v", got, err)
	}
}

func TestDecodeTimes(t *testing.T) {
	b := binary.BigEndian.AppendUint64(nil, uint64(int64(86400_000_000+1)))
	got, _ := decodeBinary(OIDTimestamptz, b)
	if got != time.Date(2000, 1, 2, 0, 0, 0, 1000, time.UTC) {
		t.Fatalf("timestamptz %v", got)
	}
	got, _ = decodeBinary(OIDTimestamp, binary.BigEndian.AppendUint64(nil, math.MaxInt64))
	if got != Infinity {
		t.Fatalf("infinity %v", got)
	}
	got, _ = decodeBinary(OIDDate, u32(uint32(0xFFFFFFFF))) // -1 day
	if got != time.Date(1999, 12, 31, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("date %v", got)
	}
}

func TestParams(t *testing.T) {
	for _, c := range []struct {
		oid  uint32
		v    any
		want string
	}{
		{0, []any{`a"b`, nil, []byte{1}, `c\d`}, `{"a\"b",NULL,"\\x01","c\\d"}`},
		{0, [][]int{{1, 2}, {3, 4}}, `{{"1","2"},{"3","4"}}`},
		{0, math.Inf(-1), "-Infinity"},
		{0, 0.1, "0.1"},
		{0, uint64(math.MaxUint64), "18446744073709551615"},
		{OIDDate, time.Date(2024, 2, 29, 23, 0, 0, 0, time.UTC), "2024-02-29"},
		{OIDTimestamp, time.Date(2024, 2, 29, 23, 0, 0, 5000, time.UTC), "2024-02-29 23:00:00.000005"},
		{OIDTimestamptz, time.Date(2024, 2, 29, 23, 0, 0, 0, time.FixedZone("", 3600)), "2024-02-29 23:00:00+01:00"},
		{0, 90 * time.Second, "90000000 microseconds"},
		{0, Numeric("1.50"), "1.50"},
	} {
		got, notNull, err := appendParam(nil, c.oid, c.v)
		if err != nil || !notNull || string(got) != c.want {
			t.Errorf("%v: %q %v %v, want %q", c.v, got, notNull, err, c.want)
		}
	}
	var p *int
	if _, notNull, err := appendParam(nil, 0, p); notNull || err != nil {
		t.Error("a nil pointer is not NULL")
	}
	if _, _, err := appendParam(nil, 0, struct{}{}); err == nil {
		t.Error("a struct was accepted")
	}
}

func TestCopyRow(t *testing.T) {
	got, err := AppendCopyRow(nil, int64(1), "a\tb\nc\\", nil, []byte{0xff}, true)
	if err != nil || string(got) != "1\ta\\tb\\nc\\\\\t\\N\t\\\\xff\tt\n" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestLSN(t *testing.T) {
	l, err := ParseLSN("16/B374D848")
	if err != nil || l != 0x16B374D848 || l.String() != "16/B374D848" {
		t.Fatalf("%v %v", l, err)
	}
	if _, err := ParseLSN("G/1"); err == nil {
		t.Fatal("a bad LSN parsed")
	}
	if _, err := ParseLSN("16B374D848"); err == nil {
		t.Fatal("a bad LSN parsed")
	}
}

func TestQuote(t *testing.T) {
	if QuoteIdentifier(`a"b`) != `"a""b"` || QuoteLiteral(`it's`) != `'it''s'` || QuoteLiteral(`a\b`) != `E'a\\b'` {
		t.Fatal("quoting")
	}
	if RowsAffected("INSERT 0 5") != 5 || RowsAffected("BEGIN") != -1 || RowsAffected("SELECT x") != -1 {
		t.Fatal("RowsAffected")
	}
}
