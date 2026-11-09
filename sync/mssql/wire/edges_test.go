package mssqlwire

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"net"
	"strings"
	"testing"
	"time"
)

// bufConn is a connection whose next message is b, already read.
func bufConn(t *testing.T, b []byte) *Conn {
	t.Helper()
	p1, p2 := net.Pipe()
	t.Cleanup(func() { p1.Close(); p2.Close() })
	c := &Conn{cfg: Config{MaxValueBytes: 1 << 20}, nc: p1, raw: p1}
	c.msg = *bytesReader(b)
	c.msg.c = c
	return c
}

// run reads a message of tokens to its end.
func run(t *testing.T, b []byte) (*response, error) {
	c := bufConn(t, b)
	r := &response{c: c, begun: true}
	err := r.drain()
	return r, err
}

func TestUUIDAndCollation(t *testing.T) {
	u, err := ParseUUID("6f9619ff8b86d011b42d00c04fc964ff")
	if err != nil || u.String() != "6F9619FF-8B86-D011-B42D-00C04FC964FF" {
		t.Fatal(u, err)
	}
	for _, s := range []string{"x", strings.Repeat("g", 32)} {
		if _, err := ParseUUID(s); err == nil {
			t.Fatal(s)
		}
	}
	for coll, cp := range map[Collation]int{
		{0x09, 0x04, 0xd0, 0x04, 0}: 65001, {0x09, 0x04, 0xd0, 0x00, 52}: 1252, {0x09, 0x04, 0xd0, 0x00, 30}: 0,
		{0x09, 0x04, 0xd0, 0x00, 0}: 1252, {0x04, 0x08, 0xd0, 0x00, 0}: 0,
	} {
		if coll.CodePage() != cp {
			t.Errorf("%x: %d, want %d", coll, coll.CodePage(), cp)
		}
	}
	if (Collation{0x09, 0x04, 0xd0, 0, 0}).LCID() != 0x409 {
		t.Fatal("LCID")
	}
	chinese := Collation{0x04, 0x08, 0xd0, 0x00, 0}
	if v := decodeChars(chinese, []byte{0xc4, 0xe3}); string(v.([]byte)) != "\xc4\xe3" {
		t.Fatal(v)
	}
	if v := decodeChars(chinese, []byte("ascii")); v != "ascii" {
		t.Fatal(v)
	}
	latin := Collation{0x09, 0x04, 0xd0, 0x00, 52}
	if _, err := encodeChars(latin, "日"); err == nil {
		t.Fatal("日 in 1252")
	}
	if _, err := encodeChars(chinese, "é"); err == nil {
		t.Fatal("é in an unknown code page")
	}
	if b, err := encodeChars(chinese, "ok"); err != nil || string(b) != "ok" {
		t.Fatal(b, err)
	}
	c := Column{Flags: 0x31}
	if !c.Nullable() || !c.Identity() || !c.Computed() {
		t.Fatal("flags")
	}
}

func TestTypeNames(t *testing.T) {
	for decl, name := range map[string]string{
		"bit": "bit", "tinyint": "tinyint", "smallint": "smallint", "int": "int", "bigint": "bigint", "real": "real", "float": "float",
		"money": "money", "smallmoney": "smallmoney", "datetime": "datetime", "smalldatetime": "smalldatetime", "date": "date",
		"uniqueidentifier": "uniqueidentifier", "xml": "xml", "decimal": "decimal(18,0)", "numeric(10, 3)": "numeric(10,3)",
		"time(3)": "time(3)", "datetime2": "datetime2(7)", "datetimeoffset(0)": "datetimeoffset(0)", "char(10)": "char(10)",
		"VARCHAR(MAX)": "varchar(max)", "binary": "binary(1)", "varbinary(20)": "varbinary(20)", "nchar(5)": "nchar(5)", "nvarchar(max)": "nvarchar(max)",
	} {
		c, err := ParseType(decl, Collation{})
		if err != nil || c.TypeName() != name {
			t.Errorf("%s: %s %v", decl, c.TypeName(), err)
		}
	}
	for _, bad := range []string{"int(3)", "decimal(40)", "decimal(5,6)", "varchar(9000)", "char(max)", "time(8)", "nvarchar(4001)", "geography", "varchar(1", "decimal(1,2,3)", "time(1,2)", "char(1,2)"} {
		if _, err := ParseType(bad, Collation{}); err == nil {
			t.Errorf("%s: no error", bad)
		}
	}
	for c, name := range map[*Column]string{
		{Type: TypeInt1}: "tinyint", {Type: TypeBit}: "bit", {Type: TypeInt2}: "smallint", {Type: TypeInt4}: "int", {Type: TypeInt8}: "bigint",
		{Type: TypeFloat4}: "real", {Type: TypeFloat8}: "float", {Type: TypeFloatN, Size: 4}: "real", {Type: TypeMoney}: "money",
		{Type: TypeMoney4}: "smallmoney", {Type: TypeMoneyN, Size: 4}: "smallmoney", {Type: TypeDateTime}: "datetime",
		{Type: TypeDateTime4}: "smalldatetime", {Type: TypeDateTimeN, Size: 4}: "smalldatetime", {Type: TypeDecimal, Precision: 5, Scale: 1}: "decimal(5,1)",
		{Type: TypeNumeric, Precision: 5}: "numeric(5,0)", {Type: TypeChar, Size: 3}: "char(3)", {Type: TypeVarChar, Size: 3}: "varchar(3)",
		{Type: TypeBinary, Size: 2}: "binary(2)", {Type: TypeVarBinary, Size: 2}: "varbinary(2)", {Type: TypeNText}: "ntext", {Type: TypeImage}: "image",
		{Type: TypeVariant}: "sql_variant", {Type: TypeUDT, UDTName: "geography"}: "geography", {Type: 0x99}: "type 0x99", {Type: TypeNChar, Size: 0}: "nchar(1)",
	} {
		if c.TypeName() != name {
			t.Errorf("%x: %s, want %s", c.Type, c.TypeName(), name)
		}
	}
}

func TestTypeInfoMore(t *testing.T) {
	// xml with a schema collection, a CLR type, the legacy types, a
	// sql_variant and an unknown type.
	xml := []byte{TypeXML, 1}
	xml = appendUSVarchar(appendBVarchar(appendBVarchar(xml, "db"), "dbo"), "coll")
	r := bytesReader(xml)
	var c Column
	if err := r.readTypeInfo(&c, true); err != nil || c.Size != -1 {
		t.Fatal(err)
	}
	for _, b := range [][]byte{{TypeVarChar, 10}, {TypeVariant, 0x10, 0x1f, 0, 0}} {
		var c Column
		if err := bytesReader(b).readTypeInfo(&c, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := bytesReader([]byte{0x99}).readTypeInfo(&c, false); err == nil {
		t.Fatal("an unknown type")
	}
	if err := bytesReader([]byte{TypeTimeN, 9}).readTypeInfo(&c, false); err == nil {
		t.Fatal("a scale of 9")
	}
	// Every prefix of a type and value is an error.
	cols, in, _ := allTypes()
	cols = append(cols, Column{Type: TypeTimeN, Scale: 1, Size: 3}, Column{Type: TypeDateTimeN, Size: 4}, Column{Type: TypeMoneyN, Size: 4})
	in = append(in, time.Second, time.Date(2000, 1, 1, 23, 59, 45, 0, time.UTC), 1)
	for i := range cols {
		val, err := appendValue(nil, &cols[i], in[i])
		if err != nil {
			continue
		}
		full := append(appendTypeInfo(nil, &cols[i], true), val...)
		if len(full) > 600 {
			full = full[:600]
		}
		for n := 0; n < len(full); n++ {
			r := bytesReader(full[:n])
			var c Column
			err := r.readTypeInfo(&c, true)
			if err == nil {
				_, err = r.readValue(&c, 1<<30)
			}
			if err == nil {
				t.Fatalf("%s: a prefix of %d bytes read", cols[i].TypeName(), n)
			}
		}
	}
	// xml with a schema, cut short.
	for n := 0; n < len(xml); n++ {
		var c Column
		if bytesReader(xml[:n]).readTypeInfo(&c, false) == nil {
			t.Fatalf("xml cut at %d read", n)
		}
	}
}

func TestValueBounds(t *testing.T) {
	text := &Column{Type: TypeText, Size: math.MaxInt32}
	b, _ := appendValue(nil, text, "0123456789")
	if _, err := bytesReader(b).readValue(text, 5); err == nil {
		t.Fatal("text past the bound")
	}
	v := []byte{0x20, 0, 0, 0}
	if _, err := bytesReader(v).readValue(&Column{Type: TypeVariant}, 5); err == nil {
		t.Fatal("variant past the bound")
	}
	plp := &Column{Type: TypeNVarChar, Size: -1}
	known := binary.LittleEndian.AppendUint64(nil, 100)
	if _, err := bytesReader(known).readValue(plp, 50); err == nil {
		t.Fatal("PLP past the bound")
	}
	b, _ = appendValue(nil, plp, strings.Repeat("x", 100))
	if _, err := bytesReader(b).readValue(plp, 50); err == nil {
		t.Fatal("PLP chunks past the bound")
	}
	// A PLP of a known length.
	k := binary.LittleEndian.AppendUint64(nil, 2)
	k = binary.LittleEndian.AppendUint32(k, 2)
	k = append(k, 'a', 0, 0, 0, 0, 0)
	if v, err := bytesReader(k).readValue(plp, 50); err != nil || v != "a" {
		t.Fatal(v, err)
	}
	// Values of the wrong length.
	for _, c := range []*Column{{Type: TypeIntN}, {Type: TypeBitN}, {Type: TypeFloatN}, {Type: TypeMoneyN}, {Type: TypeDecimalN}, {Type: TypeDateTimeN},
		{Type: TypeDateN}, {Type: TypeTimeN}, {Type: TypeDateTime2N}, {Type: TypeDateTimeOffset}, {Type: TypeGUID}, {Type: TypeNVarChar}} {
		if _, err := convertOwned(c, []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19}); err == nil {
			t.Errorf("%s of 19 bytes", c.TypeName())
		}
	}
	if v, err := convertOwned(&Column{Type: TypeNull}, nil); v != nil || err != nil {
		t.Fatal(v, err)
	}
	if _, err := convertOwned(&Column{Type: 0x99}, []byte{1}); err == nil {
		t.Fatal("an unknown type")
	}
}

func TestVariants(t *testing.T) {
	mk := func(base byte, props []byte, val []byte) []byte {
		return append(append([]byte{base, byte(len(props))}, props...), val...)
	}
	coll := []byte{0x09, 0x04, 0xd0, 0x00, 0x34}
	day := []byte{0, 0, 0}
	for _, tc := range []struct {
		b    []byte
		want any
	}{
		{mk(TypeInt4, nil, []byte{7, 0, 0, 0}), int64(7)},
		{mk(TypeDecimalN, []byte{5, 2}, []byte{1, 0x39, 0x30, 0, 0}), Decimal("123.45")},
		{mk(TypeDateTime2N, []byte{0}, append([]byte{1, 0, 0}, day...)), dayZero.Add(time.Second)},
		{mk(TypeBigVarChar, append([]byte{10, 0}, coll...), []byte("ab")), "ab"},
		{mk(TypeBigVarBinary, []byte{10, 0}, []byte{1}), []byte{1}},
	} {
		v, err := variant(tc.b)
		if err != nil || !equalValue(v, tc.want) {
			t.Errorf("%x: %#v %v", tc.b, v, err)
		}
	}
	for _, bad := range [][]byte{{1}, {TypeInt4, 3, 0}, mk(TypeDecimalN, []byte{5}, nil), mk(TypeTimeN, nil, nil), mk(TypeNVarChar, []byte{1}, nil), mk(TypeInt4, nil, []byte{1})} {
		if _, err := variant(bad); err == nil {
			t.Errorf("%x: no error", bad)
		}
	}
	// One read as a column value.
	b := binary.LittleEndian.AppendUint32(nil, 6)
	b = append(b, mk(TypeInt4, nil, []byte{9, 0, 0, 0})...)
	if v, err := bytesReader(b).readValue(&Column{Type: TypeVariant}, 100); err != nil || v != int64(9) {
		t.Fatal(v, err)
	}
	if v, err := bytesReader([]byte{0, 0, 0, 0}).readValue(&Column{Type: TypeVariant}, 100); err != nil || v != nil {
		t.Fatal(v, err)
	}
}

func equalValue(a, b any) bool {
	if x, ok := a.([]byte); ok {
		y, ok := b.([]byte)
		return ok && string(x) == string(y)
	}
	if x, ok := a.(time.Time); ok {
		y, ok := b.(time.Time)
		return ok && x.Equal(y)
	}
	return a == b
}

func TestEncodeRefusals(t *testing.T) {
	coll := Collation{0x09, 0x04, 0xd0, 0x00, 0x34}
	for _, tc := range []struct {
		c Column
		v any
	}{
		{Column{Type: TypeInt4, Size: 4}, nil},
		{Column{Type: TypeVariant}, 1},
		{Column{Type: TypeIntN, Size: 1}, 256},
		{Column{Type: TypeIntN, Size: 4}, "1"},
		{Column{Type: TypeBitN, Size: 1}, 2},
		{Column{Type: TypeFloatN, Size: 8}, "1"},
		{Column{Type: TypeMoneyN, Size: 8}, "x"},
		{Column{Type: TypeMoneyN, Size: 4}, 1e9},
		{Column{Type: TypeMoneyN, Size: 8}, "1e30"},
		{Column{Type: TypeMoneyN, Size: 8}, "99999999999999999999"},
		{Column{Type: TypeDecimalN, Precision: 3, Size: 5}, "1000"},
		{Column{Type: TypeDecimalN, Precision: 3, Size: 5}, struct{}{}},
		{Column{Type: TypeDateTimeN, Size: 8}, "now"},
		{Column{Type: TypeDateTimeN, Size: 8}, time.Date(1700, 1, 1, 0, 0, 0, 0, time.UTC)},
		{Column{Type: TypeDateTimeN, Size: 4}, time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)},
		{Column{Type: TypeDateN, Size: 3}, 1},
		{Column{Type: TypeDateN, Size: 3}, time.Time{}.AddDate(-1, 0, 0)},
		{Column{Type: TypeTimeN, Size: 5, Scale: 7}, 1},
		{Column{Type: TypeDateTime2N, Size: 8, Scale: 7}, 1},
		{Column{Type: TypeDateTimeOffset, Size: 10, Scale: 7}, 1},
		{Column{Type: TypeDateTimeOffset, Size: 10, Scale: 7}, time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)},
		{Column{Type: TypeGUID, Size: 16}, 1},
		{Column{Type: TypeGUID, Size: 16}, "nope"},
		{Column{Type: TypeBigVarChar, Size: 10, Collation: coll}, 1},
		{Column{Type: TypeBigVarChar, Size: 2, Collation: coll}, "toolong"},
		{Column{Type: TypeNVarChar, Size: 10}, 1},
		{Column{Type: TypeNVarChar, Size: 10}, []byte{0xff}},
		{Column{Type: TypeBigVarBinary, Size: 10}, 1},
		{Column{Type: 0x99}, 1},
	} {
		if _, err := appendValue(nil, &tc.c, tc.v); err == nil {
			t.Errorf("%s %#v: no error", tc.c.TypeName(), tc.v)
		}
	}
	// Some accepted on the way.
	for _, tc := range []struct {
		c Column
		v any
	}{
		{Column{Type: TypeGUID, Size: 16}, [16]byte{1}},
		{Column{Type: TypeBigVarChar, Size: 10, Collation: coll}, Decimal("1.5")},
		{Column{Type: TypeBigVarBinary, Size: 10}, "s"},
		{Column{Type: TypeTimeN, Size: 5, Scale: 7}, 24*time.Hour - 1},
		{Column{Type: TypeDateTimeN, Size: 8}, time.Date(2000, 1, 1, 23, 59, 59, 999000000, time.UTC)},
		{Column{Type: TypeDateTimeN, Size: 4}, time.Date(2000, 1, 1, 23, 59, 45, 0, time.UTC)},
		{Column{Type: TypeDecimalN, Precision: 22, Scale: 2, Size: 13}, uint64(math.MaxUint64)},
		{Column{Type: TypeDecimalN, Precision: 20, Scale: 2, Size: 13}, float32(1.25)},
		{Column{Type: TypeDecimalN, Precision: 20, Scale: 2, Size: 13}, "+1.25"},
		{Column{Type: TypeFloatN, Size: 4}, 1},
	} {
		if _, err := appendValue(nil, &tc.c, tc.v); err != nil {
			t.Errorf("%s %#v: %v", tc.c.TypeName(), tc.v, err)
		}
	}
	for _, v := range []any{int(1), int8(1), int16(1), int32(1), int64(1), uint(1), uint8(1), uint16(1), uint32(1), uint64(1), true, false} {
		if _, ok := toInt(v); !ok {
			t.Errorf("%T", v)
		}
	}
	if _, ok := toInt(uint64(math.MaxUint64)); ok {
		t.Fatal("max uint64 as an int64")
	}
	if _, ok := toInt(uint(math.MaxUint64)); ok {
		t.Fatal("max uint as an int64")
	}
	if _, err := toScaled("", 2, 10); err == nil {
		t.Fatal("empty")
	}
	if _, err := typeOf(struct{}{}); err == nil {
		t.Fatal("a struct")
	}
	if c, _ := typeOf(time.Duration(0)); c.Type != TypeTimeN {
		t.Fatal(c)
	}
	if ucs2Len("a𝄞") != 3 || len(appendBVarchar(nil, strings.Repeat("x", 300))) != 511 {
		t.Fatal("ucs2")
	}
	if abs(-3) != 3 {
		t.Fatal("abs")
	}
}

func TestTokenEdges(t *testing.T) {
	col := Column{Name: "n", Type: TypeIntN, Size: 4}
	rv := tReturnValue(t, "@o", Column{Type: TypeIntN, Size: 4}, 5)
	skip16 := func(tok byte) []byte { return append([]byte{tok, 2, 0}, 1, 2) }
	sess := []byte{tokSessionState, 2, 0, 0, 0, 1, 2}
	all := [][]byte{
		tEnv(envDatabase, bvarchar("db2")), tEnv(envPacketSize, bvarchar("8192")), tEnv(envPacketSize, bvarchar("99")), tEnv(2, bvarchar("us_english")),
		tEnv(envTxEnded, []byte{0}), tColMeta(col), skip16(tokOrder), skip16(tokTabName), skip16(tokColInfo), sess,
		tRow(t, []Column{col}, 1), tInfo(1, "info"), rv, tLoginAck(), tFeatureAck(), tDone(tokDoneInProc, doneCount, 1), tReturnStatus(1), tDone(tokDone, doneCount, 2),
	}
	var full []byte
	for _, b := range all {
		full = append(full, b...)
	}
	r, err := run(t, full)
	if err != nil || r.c.database != "db2" || r.c.packetSize != 8192 || r.outputs["@o"] != int64(5) || r.affected != 3 {
		t.Fatal(err, r.c.database, r.c.packetSize, r.outputs, r.affected)
	}
	for n := 1; n < len(full); n++ {
		if _, err := run(t, full[:n]); err == nil {
			t.Fatalf("a prefix of %d read", n)
		}
	}
	for _, bad := range [][]byte{
		{0x01},                                   // an unknown token
		{tokEnvChange, 1, 0, 1},                  // too short
		{tokEnvChange, 3, 0, envDatabase, 5, 0},  // its value cut
		{tokEnvChange, 3, 0, envCollation, 5, 0}, // its bytes cut
		{tokError, 2, 0, 1, 2},                   // too short
		append([]byte{tokError, 10, 0}, 1, 0, 0, 0, 1, 16, 9, 0, 0, 0),               // its text cut
		append([]byte{tokError, 9, 0}, 1, 0, 0, 0, 1, 16, 0, 0, 3),                   // its server cut
		append(append([]byte{tokError, 12, 0}, 1, 0, 0, 0, 1, 16, 0, 0, 0, 0), 0, 0), // its line cut
		append(tColMeta(col)[:3], 0, 0, 0, 0, 0, 0, 0x99),                            // an unknown type
		append(rv[:12], 0x99),                            // a return value of an unknown type
		append(append(rv[:12], TypeIntN, 4, 3), 1, 2, 3), // a value of the wrong size
		append(tColMeta(Column{Name: "s", Type: TypeIntN, Size: 4}), tokRow, 3, 1, 2, 3), // a row value of the wrong size
	} {
		if _, err := run(t, bad); err == nil {
			t.Errorf("%x: no error", bad)
		}
	}
}

func TestBrokenPaths(t *testing.T) {
	f := newFake(t)
	f.handle = func(s *session, typ byte, body []byte) {
		sql, _ := batchSQL(body)
		switch sql {
		case "short packet":
			s.nc.Write([]byte{pktReply, statusEOM, 0, 4, 0, 0, 1, 0})
		case "close":
			s.nc.Close()
		default:
			s.reply(tDone(tokDone, 0, 0))
		}
	}
	f.start()
	ctx := context.Background()
	c := dial(t, f)
	if _, err := c.Exec(ctx, "short packet"); err == nil || c.Err() == nil {
		t.Fatal(err)
	}
	for _, call := range []func() error{
		func() error { _, err := c.Exec(ctx, "x"); return err },
		func() error { _, err := c.Query(ctx, "x"); return err },
		func() error { _, err := c.Call(ctx, "p"); return err },
		func() error { _, _, err := c.QueryAll(ctx, "x"); return err },
		func() error { _, err := c.BulkInsert(ctx, "t", []string{"a"}, BulkOptions{}); return err },
	} {
		if call() == nil {
			t.Fatal("a broken connection took a call")
		}
	}
	c = dial(t, f)
	rows, err := c.Query(ctx, "close")
	if err == nil {
		rows.Close()
		t.Fatal("no error")
	}
	// A request cut short by its context closes the connection.
	c = dial(t, f)
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	w := c.newWriter(pktSQLBatch)
	w.ctx = cctx
	if _, err := w.Write(make([]byte, 10000)); !errors.Is(err, context.Canceled) || c.Err() == nil {
		t.Fatal(err)
	}
	// Writes to a closed connection fail.
	c = dial(t, f)
	c.nc.Close()
	if err := c.write([]byte{1}); err == nil {
		t.Fatal("a write to a closed connection")
	}
	// The handshake wrapper passes through.
	c = dial(t, f)
	h := &handshakeConn{c: c, rd: c.rd, done: true}
	if h.LocalAddr() == nil || h.RemoteAddr() == nil || h.SetDeadline(time.Time{}) != nil || h.SetReadDeadline(time.Time{}) != nil || h.SetWriteDeadline(time.Time{}) != nil {
		t.Fatal("handshakeConn")
	}
	if _, err := h.Write([]byte{pktAttention, statusEOM, 0, 8, 0, 0, 1, 0}); err != nil {
		t.Fatal(err)
	}
	h.Close()
	if _, err := h.Read(make([]byte, 1)); err == nil {
		t.Fatal("a read from a closed connection")
	}
	h.done = false
	if _, err := h.Write([]byte{1}); err == nil {
		t.Fatal("a write to a closed connection")
	}
}

func TestPreloginEdges(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply []byte
	}{
		{"cut option", []byte{1, 0, 6}},
		{"option past the end", []byte{1, 0, 50, 0, 1, 0xff}},
		{"too large", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ln, _ := net.Listen("tcp", "127.0.0.1:0")
			defer ln.Close()
			go func() {
				nc, err := ln.Accept()
				if err != nil {
					return
				}
				defer nc.Close()
				s := &session{f: &fake{replySize: 4096}, nc: nc, rd: nc}
				s.readMsg()
				if tc.reply == nil {
					big := make([]byte, 4000)
					for i := 0; i < 20; i++ {
						s.sendPart(pktReply, big, false)
					}
					return
				}
				s.send(pktReply, tc.reply)
				s.readMsg()
			}()
			cfg := Config{Addr: ln.Addr().String(), Encrypt: EncryptOff, Timeout: 2 * time.Second, MaxValueBytes: 10000}
			if _, err := Dial(context.Background(), cfg); err == nil {
				t.Fatal("no error")
			}
		})
	}
}

func TestRowsEdges(t *testing.T) {
	f := newFake(t)
	col := Column{Name: "n", Type: TypeIntN, Size: 4}
	f.handle = func(s *session, typ byte, body []byte) {
		sql, _ := batchSQL(body)
		switch sql {
		case "back to back":
			// Two result sets with no DONE between them.
			s.reply(tColMeta(col), tRow(t, []Column{col}, 1), tColMeta(col), tRow(t, []Column{col}, 2), tDone(tokDone, doneCount, 2))
		case "cut in rows":
			s.sendPart(pktReply, append(tColMeta(col), tRow(t, []Column{col}, 1)...), false)
			s.nc.Close()
		case "cut before rows":
			s.sendPart(pktReply, tInfo(1, "x"), false)
			s.nc.Close()
		case "no rows":
			s.reply(tDone(tokDone, doneCount, 0))
		}
	}
	f.start()
	ctx := context.Background()
	c := dial(t, f)
	rows, err := c.Query(ctx, "back to back")
	if err != nil {
		t.Fatal(err)
	}
	var got []any
	for {
		for rows.Next() {
			got = append(got, rows.Values()[0])
		}
		if !rows.NextResultSet() {
			break
		}
	}
	if len(got) != 2 || rows.Err() != nil {
		t.Fatal(got, rows.Err())
	}
	rows, err = c.Query(ctx, "no rows")
	if err != nil || rows.Next() || rows.NextResultSet() || rows.Columns() != nil {
		t.Fatal(err)
	}
	rows, err = c.Query(ctx, "cut in rows")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
	}
	if rows.Err() == nil || rows.Close() == nil {
		t.Fatal("no error")
	}
	c = dial(t, f)
	if _, err := c.Query(ctx, "cut before rows"); err == nil {
		t.Fatal("no error")
	}
	// Close on a broken connection.
	c = dial(t, f)
	rows, _ = c.Query(ctx, "back to back")
	rows.Next()
	c.fail(errors.New("gone"))
	if rows.Close() == nil {
		t.Fatal("no error")
	}
}
