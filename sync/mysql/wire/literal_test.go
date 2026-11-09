package mysqlwire

import (
	"encoding/json"
	"errors"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAppendLiteral(t *testing.T) {
	loc := time.FixedZone("x", 3600)
	c := &Conn{loc: loc}
	at := time.Date(2026, 11, 9, 10, 4, 5, 123456789, time.UTC)
	for _, tc := range []struct {
		v    any
		want string
	}{
		{nil, "NULL"},
		{true, "1"},
		{false, "0"},
		{int(-1), "-1"},
		{int8(-8), "-8"},
		{int16(-16), "-16"},
		{int32(-32), "-32"},
		{int64(math.MinInt64), "-9223372036854775808"},
		{uint(1), "1"},
		{uint8(8), "8"},
		{uint16(16), "16"},
		{uint32(32), "32"},
		{uint64(math.MaxUint64), "18446744073709551615"},
		// Floats with an exponent, as DOUBLEs: 0.1 alone would be a
		// DECIMAL.
		{0.1, "1e-01"},
		{float32(0.1), "1.0000000149011612e-01"},
		{-1.5e300, "-1.5e+300"},
		{0.0, "0e+00"},
		{"", "''"},
		{"a'b\"c\\d\x00e\nf\rg\x1ah", `'a\'b\"c\\d\0e\nf\rg\Zh'`},
		{"中文 ok", "'中文 ok'"},
		{[]byte(nil), "NULL"},
		{[]byte{0, 'x', 0xff, '\''}, "'\\0x\xff\\''"},
		{json.RawMessage(nil), "NULL"},
		{json.RawMessage(`{"a":"it's"}`), `'{\"a\":\"it\'s\"}'`},
		{at, "'2026-11-09 11:04:05.123456'"},
		{time.Date(2026, 11, 9, 23, 0, 0, 0, time.UTC), "'2026-11-10'"},
		{DateTime{}, "'0000-00-00'"},
		{DateTime{Year: 2026, Month: 1, Day: 2}, "'2026-01-02'"},
		{DateTime{Year: 2026, Month: 1, Day: 2, Second: 3}, "'2026-01-02 00:00:03'"},
		{DateTime{Year: 2026, Month: 1, Day: 2, Microsecond: 7}, "'2026-01-02 00:00:00.000007'"},
		{time.Duration(0), "'00:00:00'"},
		{-(26*time.Hour + 3*time.Minute + 4*time.Second + 5*time.Microsecond), "'-26:03:04.000005'"},
		{838 * time.Hour, "'838:00:00'"},
	} {
		got, err := c.appendLiteral([]byte("x"), tc.v)
		if err != nil || string(got) != "x"+tc.want {
			t.Errorf("%T %v: %s, %v; want %s", tc.v, tc.v, got, err, tc.want)
		}
	}
	// Under NO_BACKSLASH_ESCAPES only the quote is doubled.
	c.status = StatusNoBackslashEscapes
	for v, want := range map[any]string{
		"a'b\"c\\d\x00e''": "'a''b\"c\\d\x00e'''''",
		"plain":            "'plain'",
		"'":                "''''",
	} {
		if got, _ := c.appendLiteral(nil, v); string(got) != want {
			t.Errorf("%q: %s, want %s", v, got, want)
		}
	}
	if got, _ := c.appendLiteral(nil, []byte(`\'`)); string(got) != `'\'''` {
		t.Errorf("%s", got)
	}
	for _, v := range []any{math.NaN(), math.Inf(1), float32(math.Inf(-1)), struct{}{}} {
		if _, err := c.appendLiteral(nil, v); err == nil {
			t.Errorf("%v: no error", v)
		}
	}
}

// unquote reads a quoted literal as MySQL's lexer does.
func unquote(t *testing.T, s string, noBackslash bool) string {
	t.Helper()
	if len(s) < 2 || s[0] != '\'' || s[len(s)-1] != '\'' {
		t.Fatalf("not quoted: %q", s)
	}
	s = s[1 : len(s)-1]
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '\'':
			if i+1 >= len(s) || s[i+1] != '\'' {
				t.Fatalf("a lone quote in %q", s)
			}
			b.WriteByte('\'')
			i++
		case s[i] == '\\' && !noBackslash:
			if i+1 >= len(s) {
				t.Fatalf("a backslash at the end of %q", s)
			}
			i++
			switch s[i] {
			case '0':
				b.WriteByte(0)
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 'Z':
				b.WriteByte(0x1a)
			case 'b':
				b.WriteByte('\b')
			case 't':
				b.WriteByte('\t')
			default:
				b.WriteByte(s[i])
			}
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// TestQuotedReadsBack: any bytes, quoted either way, read back as
// themselves, and the literal ends at its last byte.
func TestQuotedReadsBack(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	alphabet := []byte("ab'\"\\\x00\n\r\x1a\t\b%_\xe4\xb8\xad\xff")
	for range 5000 {
		s := make([]byte, r.IntN(24))
		for i := range s {
			s[i] = alphabet[r.IntN(len(alphabet))]
		}
		for _, nb := range []bool{false, true} {
			q := appendQuoted(nil, string(s), nb)
			if got := unquote(t, string(q), nb); got != string(s) {
				t.Fatalf("%q (no backslash %v): %q read back as %q", s, nb, q, got)
			}
		}
	}
}

func TestAsciiSafe(t *testing.T) {
	for _, c := range []uint8{45, 46, 224, 247, 255, 33, 83, 192, 223, 5, 8, 47, 94, 11, 65, 63} {
		if !asciiSafe(c) {
			t.Errorf("%d", c)
		}
	}
	for _, c := range []uint8{28, 87, 1, 84, 13, 95, 248, 249, 24} { // gbk, big5, sjis, cp932, gb18030
		if asciiSafe(c) {
			t.Errorf("%d", c)
		}
	}
}

// insertFake answers text INSERTs: each statement's rows counted by its
// "(" and checked against a prefix; a statement whose first row's first
// value is -1 is refused.
func insertFake(t *testing.T, maxAllowed int, got *[]string) func(f *fake) {
	return func(f *fake) {
		f.login(fakeCaps | capDeprecateEOF)
		for {
			cmd, body := f.cmd()
			if cmd != comQuery {
				if cmd == comStmtPrepare {
					f.prepareOK(1, strings.Count(string(body), "?"), nil)
					continue
				}
				if cmd == comStmtExecute {
					*got = append(*got, "execute")
					f.ok(1, 0, 0)
					continue
				}
				if cmd == comStmtClose {
					continue
				}
				return
			}
			q := string(body)
			switch {
			case strings.HasPrefix(q, "SELECT @@max_allowed_packet"):
				f.columns([]Column{{Name: "m", Type: TypeLongLong}})
				f.textRow([]byte(strconv.Itoa(maxAllowed)))
				f.end(0)
			case strings.HasPrefix(q, "SET sql_mode"):
				f.ok(0, 0, StatusAutocommit|StatusNoBackslashEscapes)
			case strings.HasPrefix(q, "INSERT INTO t (a, b) VALUES (-1,"):
				f.fail(1062, "23000", "Duplicate")
			case strings.HasPrefix(q, "INSERT INTO t (a, b) VALUES ("):
				if len(q)+1 > maxAllowed {
					t.Errorf("a statement of %d bytes, past %d", len(q)+1, maxAllowed)
				}
				n := strings.Count(q, "),(") + 1
				rest := q[len("INSERT INTO t (a, b) VALUES "):]
				*got = append(*got, strconv.Itoa(n)+":"+rest[:min(len(rest), 12)])
				f.ok(uint64(n), uint64(len(*got)), StatusAutocommit)
			default:
				return
			}
		}
	}
}

func TestInsertRowsText(t *testing.T) {
	var got []string
	addr := serve(t, insertFake(t, 2000, &got))
	c := dialT(t, Config{Addr: addr, User: "u"})
	ctx := ctxT(t)
	if res, err := c.InsertRowsText(ctx, "INSERT", nil, ""); err != nil || res != (Result{}) {
		t.Fatal(res, err)
	}
	// 30 rows of about 100 bytes in statements of at most 2,000 - 1,024
	// bytes: 9 rows each, the tail counted.
	var rows [][]any
	for i := range 30 {
		rows = append(rows, []any{i, strings.Repeat("x", 90)})
	}
	res, err := c.InsertRowsText(ctx, "INSERT INTO t (a, b) VALUES", rows, "ON DUPLICATE KEY UPDATE b = b")
	if err != nil || res.AffectedRows != 30 || res.LastInsertID != 1 || res.Status != StatusAutocommit {
		t.Fatal(res, err)
	}
	if s := strings.Join(got, " "); s != "9:(0,'xxxxxxxx 9:(9,'xxxxxxxx 9:(18,'xxxxxxx 3:(27,'xxxxxxx" {
		t.Fatal(s)
	}
	// A row larger than a statement goes alone, for the server to refuse.
	got = nil
	if _, err := c.InsertRowsText(ctx, "INSERT INTO t (a, b) VALUES", [][]any{{1, strings.Repeat("y", 1000)}, {2, "z"}}, ""); err != nil {
		t.Fatal(err)
	}
	if s := strings.Join(got, " "); s != "1:(1,'yyyyyyyy 1:(2,'z')" {
		t.Fatal(s)
	}
	// Failures: a statement refused, rows of different lengths, rows of
	// nothing, a value with no literal; the connection stays usable.
	var se *Error
	if _, err := c.InsertRowsText(ctx, "INSERT INTO t (a, b) VALUES", [][]any{{-1, "x"}}, ""); !errors.As(err, &se) || se.Code != 1062 {
		t.Fatal(err)
	}
	if _, err := c.InsertRowsText(ctx, "INSERT", [][]any{{1, 2}, {1}}, ""); err == nil {
		t.Fatal("rows of different lengths")
	}
	if _, err := c.InsertRowsText(ctx, "INSERT", [][]any{{}}, ""); err == nil {
		t.Fatal("rows of nothing")
	}
	if _, err := c.InsertRowsText(ctx, "INSERT INTO t (a, b) VALUES", [][]any{{1, "a"}, {2, struct{}{}}}, ""); err == nil || !strings.Contains(err.Error(), "row 2, value 2") {
		t.Fatal(err)
	}
	// NO_BACKSLASH_ESCAPES, as the server's status says.
	if _, err := c.Exec(ctx, "SET sql_mode = 'NO_BACKSLASH_ESCAPES'"); err != nil {
		t.Fatal(err)
	}
	got = nil
	if _, err := c.InsertRowsText(ctx, "INSERT INTO t (a, b) VALUES", [][]any{{1, `\'`}}, ""); err != nil {
		t.Fatal(err)
	}
	if s := strings.Join(got, " "); s != `1:(1,'\''')` {
		t.Fatal(s)
	}
	// A broken connection.
	c.Close()
	if _, err := c.InsertRowsText(ctx, "INSERT INTO t (a, b) VALUES", [][]any{{1, "a"}}, ""); err == nil {
		t.Fatal("closed")
	}
}

// TestInsertRowsTextFallsBack: on a connection whose character set may
// hide a quote in a character, the rows go as parameters.
func TestInsertRowsTextFallsBack(t *testing.T) {
	var got []string
	addr := serve(t, insertFake(t, 1<<20, &got))
	c := dialT(t, Config{Addr: addr, User: "u", Collation: 28}) // gbk_chinese_ci
	if _, err := c.InsertRowsText(ctxT(t), "INSERT INTO t (a, b) VALUES", [][]any{{1, "a"}}, ""); err != nil {
		t.Fatal(err)
	}
	if s := strings.Join(got, " "); s != "execute" {
		t.Fatal(s)
	}
}

// TestInsertRowsTextAllowedPacket: max_allowed_packet not read fails the
// call.
func TestInsertRowsTextAllowedPacket(t *testing.T) {
	addr := serve(t, func(f *fake) {
		f.login(fakeCaps | capDeprecateEOF)
		f.cmd()
		f.fail(1227, "42000", "denied")
	})
	c := dialT(t, Config{Addr: addr, User: "u"})
	if _, err := c.InsertRowsText(ctxT(t), "INSERT INTO t (a, b) VALUES", [][]any{{1, "a"}}, ""); err == nil {
		t.Fatal("no error")
	}
}

// BenchmarkInsertRowsText writes a batch of 1,000 rows of sysbench's
// shape, an id, an int, 120 and 60 characters, to a server that answers
// OK, as text: the statement's encoding and one round trip.
func BenchmarkInsertRowsText(b *testing.B) {
	benchInsert(b, func(c *Conn, rows [][]any) error {
		_, err := c.InsertRowsText(b.Context(), "INSERT INTO t (a, b) VALUES", rows, "ON DUPLICATE KEY UPDATE b = b")
		return err
	})
}

// BenchmarkInsertRowsPrepared is the same batch as a prepared statement:
// prepare, execute and close.
func BenchmarkInsertRowsPrepared(b *testing.B) {
	benchInsert(b, func(c *Conn, rows [][]any) error {
		_, err := c.InsertRows(b.Context(), "INSERT INTO t (a, b) VALUES", rows, "ON DUPLICATE KEY UPDATE b = b")
		return err
	})
}

func benchInsert(b *testing.B, insert func(*Conn, [][]any) error) {
	var got []string
	addr := serve(&testing.T{}, func(f *fake) {
		f.login(fakeCaps | capDeprecateEOF)
		for {
			cmd, body := f.cmd()
			switch cmd {
			case comQuery:
				if strings.HasPrefix(string(body), "SELECT") {
					f.columns([]Column{{Name: "m", Type: TypeLongLong}})
					f.textRow([]byte("67108864"))
					f.end(0)
					continue
				}
				f.ok(1000, 0, 0)
			case comStmtPrepare:
				f.prepareOK(1, strings.Count(string(body), "?"), nil)
			case comStmtExecute:
				f.ok(1000, 0, 0)
			case comStmtClose:
			default:
				return
			}
		}
	})
	_ = got
	c, err := Dial(b.Context(), Config{Addr: addr, User: "u"})
	if err != nil {
		b.Fatal(err)
	}
	defer c.Close()
	rows := make([][]any, 1000)
	for i := range rows {
		rows[i] = []any{int64(i), int64(i * 7), strings.Repeat(strconv.Itoa(i%10), 120), strings.Repeat("p", 60)}
	}
	b.ReportAllocs()
	for b.Loop() {
		if err := insert(c, rows); err != nil {
			b.Fatal(err)
		}
	}
}
