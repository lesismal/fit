package binlog

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"math"
	"strings"
	"testing"
	"time"

	fitsync "github.com/lesismal/fit/sync"
)

func TestEventTypeString(t *testing.T) {
	if QueryEvent.String() != "Query" || EventType(99).String() != "type 99" {
		t.Fatal(QueryEvent, EventType(99))
	}
	if Insert.String() != "insert" || Update.String() != "update" || Delete.String() != "delete" {
		t.Fatal("kinds")
	}
}

func TestFormatDescription(t *testing.T) {
	d := NewDecoder()
	if d.FormatDescription() != nil {
		t.Fatal("a description before one")
	}
	ev := mustDecode(t, d, fde("8.0.40-log", 19, postHeaders(), ChecksumCRC32))
	fd := ev.Body.(*FormatDescription)
	if fd.ServerVersion != "8.0.40-log" || fd.Checksum != ChecksumCRC32 || fd.MariaDB() || d.FormatDescription() != fd ||
		len(fd.PostHeaderLengths) != 42 {
		t.Fatalf("%+v", fd)
	}
	// Checksums off: the CRC's bytes are there, not checked.
	d = NewDecoder()
	b := fde("10.11.6-MariaDB-log", 19, postHeaders(), ChecksumOff)
	b[len(b)-1] ^= 0xff
	fd = mustDecode(t, d, b).Body.(*FormatDescription)
	if !fd.MariaDB() || fd.Checksum != ChecksumOff || d.checksum {
		t.Fatalf("%+v", fd)
	}
	// A version before checksums.
	fd = mustDecode(t, d, fde("5.5.62", 19, postHeaders(), 0)).Body.(*FormatDescription)
	if fd.Checksum != ChecksumUndefined || len(fd.PostHeaderLengths) != 42 {
		t.Fatalf("%+v", fd)
	}
	for _, c := range []struct {
		name string
		b    []byte
		err  error
	}{
		{"bad crc", func() []byte {
			b := fde("8.0.40", 19, postHeaders(), ChecksumCRC32)
			b[30] ^= 1
			return b
		}(), ErrChecksum},
		{"version 3", func() []byte {
			b := fde("5.5.62", 19, nil, 0)
			b[HeaderSize] = 3
			return b
		}(), ErrMalformed},
		{"short", event(FormatDescriptionEvent, make([]byte, 10), false), ErrMalformed},
		{"no checksum", event(FormatDescriptionEvent, cat(le(4, 2), []byte("8.0.40"), make([]byte, 44), le(0, 4), []byte{19, 1}), false), ErrMalformed},
	} {
		if _, err := NewDecoder().Decode(c.b); !errors.Is(err, c.err) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	for v, want := range map[string]bool{"5.6.0": false, "5.6.1": true, "8.4.3": true, "5.5.5-10.11.6-MariaDB": true,
		"5.2.1-MariaDB": false, "10.6": true, "x": false} {
		if hasChecksumAlg(v) != want {
			t.Errorf("hasChecksumAlg(%q)", v)
		}
	}
}

func TestDecodeErrors(t *testing.T) {
	d := newDecoder(t)
	xid := event(XIDEvent, le(42, 8), true)
	if ev := mustDecode(t, d, xid); ev.Body.(*XID).XID != 42 || ev.Type != XIDEvent || ev.Size != uint32(len(xid)) {
		t.Fatalf("%+v", ev)
	}
	bad := append([]byte(nil), xid...)
	bad[HeaderSize] ^= 1
	long := event(XIDEvent, make([]byte, 100), true)
	short := event(XIDEvent, nil, false)
	for _, c := range []struct {
		name string
		b    []byte
		err  error
	}{
		{"header", xid[:10], ErrMalformed},
		{"size", xid[:len(xid)-1], ErrMalformed},
		{"crc", bad, ErrChecksum},
		{"no room for crc", short, ErrMalformed},
		{"xid", event(XIDEvent, le(1, 2), true), ErrMalformed},
		{"rotate", event(RotateEvent, le(1, 2), true), ErrMalformed},
	} {
		if _, err := d.Decode(c.b); !errors.Is(err, c.err) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	d.MaxEventSize = 50
	if _, err := d.Decode(long); !errors.Is(err, ErrTooLarge) {
		t.Error(err)
	}
	// A header longer than 19 bytes, from the format description.
	d = NewDecoder()
	mustDecode(t, d, fde("8.0.40", 21, postHeaders(), ChecksumOff))
	b := event(XIDEvent, cat([]byte{9, 9}, le(5, 8)), false)
	if ev := mustDecode(t, d, b); ev.Body.(*XID).XID != 5 {
		t.Fatalf("%+v", ev.Body)
	}
	if _, err := d.Decode(event(XIDEvent, nil, false)); !errors.Is(err, ErrMalformed) {
		t.Error(err)
	}
	// Checksums turned on before a description.
	d = NewDecoder()
	d.SetChecksum(true)
	ev := mustDecode(t, d, event(RotateEvent, cat(le(4, 8), []byte("binlog.000002")), true))
	if r := ev.Body.(*Rotate); r.Position != 4 || r.NextFile != "binlog.000002" {
		t.Fatalf("%+v", r)
	}
}

func TestQueryEvents(t *testing.T) {
	d := newDecoder(t)
	vars := []byte{0, 0, 0, 0, 0}
	body := cat(le(7, 4), le(2, 4), []byte{4}, le(0, 2), le(uint64(len(vars)), 2), vars, []byte("shop\x00"), []byte("ALTER TABLE t ADD c INT"))
	q := mustDecode(t, d, event(QueryEvent, body, true)).Body.(*Query)
	if q.ThreadID != 7 || q.ExecTime != 2 || q.Schema != "shop" || q.SQL != "ALTER TABLE t ADD c INT" || !bytes.Equal(q.StatusVars, vars) {
		t.Fatalf("%+v", q)
	}
	if _, err := d.Decode(event(QueryEvent, body[:12], true)); !errors.Is(err, ErrMalformed) {
		t.Error(err)
	}
	// MariaDB's compressed query.
	sql := strings.Repeat("INSERT INTO t VALUES (1); ", 20)
	cbody := cat(le(7, 4), le(2, 4), []byte{0}, le(0, 2), le(0, 2), []byte{0}, compress([]byte(sql)))
	q = mustDecode(t, d, event(MariaQueryCompressedEvent, cbody, true)).Body.(*Query)
	if q.SQL != sql {
		t.Fatalf("%q", q.SQL)
	}
	z := compress([]byte(sql))
	for name, c := range map[string][]byte{
		"header":    {0x10, 1, 2},
		"lenlen":    {0x87},
		"short len": {0x82, 1},
		"zlib":      {0x81, 5, 1, 2, 3},
		"shorter":   cat([]byte{0x82, 0x10, 0}, z[3:]),
		"longer":    cat([]byte{0x81, 4}, z[3:]),
		"too large": {0x84, 0x7f, 0xff, 0xff, 0xff},
	} {
		b := cat(le(7, 4), le(2, 4), []byte{0}, le(0, 2), le(0, 2), []byte{0}, c)
		if _, err := d.Decode(event(MariaQueryCompressedEvent, b, true)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	// A post-header without the status variables' length (binlog v3's).
	d = NewDecoder()
	ph := postHeaders()
	ph[QueryEvent-1] = 11
	mustDecode(t, d, fde("8.0.40", 19, ph, ChecksumOff))
	q = mustDecode(t, d, event(QueryEvent, cat(le(7, 4), le(2, 4), []byte{0}, le(0, 2), []byte{0}, []byte("BEGIN")), false)).Body.(*Query)
	if q.SQL != "BEGIN" {
		t.Fatalf("%+v", q)
	}
}

func TestSmallEvents(t *testing.T) {
	d := newDecoder(t)
	if r := mustDecode(t, d, event(RowsQueryEvent, cat([]byte{3}, []byte("DELETE")), true)).Body.(*RowsQuery); r.SQL != "DELETE" {
		t.Fatal(r)
	}
	if _, err := d.Decode(event(RowsQueryEvent, nil, true)); !errors.Is(err, ErrMalformed) {
		t.Error(err)
	}
	if r := mustDecode(t, d, event(MariaAnnotateRowsEvent, []byte("UPDATE t"), true)).Body.(*RowsQuery); r.SQL != "UPDATE t" {
		t.Fatal(r)
	}
	b := event(HeartbeatEvent, []byte("binlog.000003"), true)
	binary.LittleEndian.PutUint32(b[13:], 1234)
	binary.LittleEndian.PutUint32(b[len(b)-4:], crc32.ChecksumIEEE(b[:len(b)-4]))
	if h := mustDecode(t, d, b).Body.(*Heartbeat); h.LogFile != "binlog.000003" || h.Position != 1234 {
		t.Fatalf("%+v", h)
	}
	if u := mustDecode(t, d, event(IntvarEvent, []byte{1, 2, 3}, true)).Body.(*Unknown); !bytes.Equal(u.Data, []byte{1, 2, 3}) {
		t.Fatal(u)
	}
	if c := mustDecode(t, d, event(MariaBinlogCheckpointEvent, cat(le(9, 4), []byte("bin.00001")), true)).Body.(*BinlogCheckpoint); c.File != "bin.00001" {
		t.Fatal(c)
	}
	if _, err := d.Decode(event(MariaBinlogCheckpointEvent, cat(le(90, 4), []byte("bin")), true)); !errors.Is(err, ErrMalformed) {
		t.Error(err)
	}
	if _, err := d.Decode(event(HeartbeatEventV2, []byte{0x02}, true)); !errors.Is(err, ErrMalformed) {
		t.Error(err)
	}
}

// varlenU is MySQL's serialization of an unsigned integer.
func varlenU(v uint64) []byte {
	for n := 1; n <= 8; n++ {
		if v < 1<<(7*n) {
			x := v<<n | (1<<(n-1) - 1)
			return le(x, n)
		}
	}
	return cat([]byte{0xff}, le(v, 8))
}

func varlenS(v int64) []byte { return varlenU(uint64(v<<1) ^ uint64(v>>63)) }

// msg is a serialized message of fields, with last the last field a
// reader must know.
func msg(last uint64, fields ...[]byte) []byte {
	b := cat(varlenU(last), cat(fields...))
	n := 1 + len(b) + 1
	if len(varlenU(uint64(n))) > 1 {
		n++
	}
	return cat(varlenU(1), varlenU(uint64(n)), b)
}

func TestSerialization(t *testing.T) {
	for _, v := range []uint64{0, 1, 127, 128, 1 << 20, 1<<56 - 1, 1 << 56, math.MaxUint64} {
		got, n := readVarlen(varlenU(v))
		if got != v || n != len(varlenU(v)) {
			t.Errorf("%d: %d %d", v, got, n)
		}
	}
	if _, n := readVarlen([]byte{0x03}); n != 0 {
		t.Error("short")
	}
	if _, n := readVarlen(nil); n != 0 {
		t.Error("empty")
	}
	d := newDecoder(t)
	hb := msg(1, varlenU(0), lstrVar("binlog.000009"), varlenU(1), varlenU(1<<40), varlenU(5), varlenU(3))
	h := mustDecode(t, d, event(HeartbeatEventV2, hb, true)).Body.(*Heartbeat)
	if h.LogFile != "binlog.000009" || h.Position != 1<<40 {
		t.Fatalf("%+v", h)
	}
	// A field it does not know, which it must.
	if _, err := d.Decode(event(HeartbeatEventV2, msg(5, varlenU(5), varlenU(3)), true)); !errors.Is(err, ErrUnsupported) {
		t.Error(err)
	}
	for name, m := range map[string][]byte{
		"string": msg(1, varlenU(0), varlenU(50), []byte("x")),
		"int":    msg(1, varlenU(1), []byte{0x03}),
		"size":   cat(varlenU(1), varlenU(200), varlenU(0)),
		"header": cat(varlenU(1), varlenU(2)),
	} {
		if _, err := d.Decode(event(HeartbeatEventV2, m, true)); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: %v", name, err)
		}
	}
	m := &message{b: []byte{1}}
	if m.fixed(4) != nil || m.err == nil {
		t.Error("fixed")
	}
	m = &message{b: []byte{1, 2}}
	if !bytes.Equal(m.fixed(2), []byte{1, 2}) {
		t.Error("fixed")
	}
}

func lstrVar(s string) []byte { return cat(varlenU(uint64(len(s))), []byte(s)) }

func TestGTIDs(t *testing.T) {
	d := newDecoder(t)
	sid := [16]byte{0x07, 0xbf, 0x9a, 0x8d, 0xdb, 0x83, 0x11, 0xf1, 0xbf, 0x7e, 2, 0x42, 0xac, 0x11, 0, 0x0a}
	// 5.6's: flags, SID and GNO.
	g := mustDecode(t, d, event(GTIDEvent, cat([]byte{1}, sid[:], le(9, 8)), true)).Body.(*GTID)
	if g.String() != "07bf9a8d-db83-11f1-bf7e-0242ac11000a:9" || g.Flags != 1 || g.Anonymous {
		t.Fatalf("%v", g)
	}
	// 8.0's, with every part.
	ts := uint64(1794150211367378)
	body := cat([]byte{0}, sid[:], le(10, 8), []byte{2}, le(3, 8), le(4, 8), le(ts|1<<55, 7), le(ts-5, 7),
		[]byte{0xfc}, le(300, 2), le(80040|1<<31, 4), le(50700, 4), le(77, 8))
	g = mustDecode(t, d, event(AnonymousGTIDEvent, body, true)).Body.(*GTID)
	if !g.Anonymous || g.LastCommitted != 3 || g.SequenceNumber != 4 || g.ImmediateCommitTimestamp != ts ||
		g.OriginalCommitTimestamp != ts-5 || g.TransactionLength != 300 || g.ImmediateServerVersion != 80040 ||
		g.OriginalServerVersion != 50700 || g.CommitGroupTicket != 77 {
		t.Fatalf("%+v", g)
	}
	for name, b := range map[string][]byte{
		"short": cat([]byte{0}, sid[:8]),
		"clock": cat([]byte{0}, sid[:], le(10, 8), []byte{1}, le(3, 8), le(4, 8)),
		"lc":    cat([]byte{0}, sid[:], le(10, 8), []byte{2}, le(3, 4)),
	} {
		if _, err := d.Decode(event(GTIDEvent, b, true)); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// 8.3's tagged GTID, with the optional fields.
	var sidf []byte
	for _, c := range sid {
		sidf = append(sidf, varlenU(uint64(c))...)
	}
	tagged := msg(9, varlenU(0), varlenU(1), varlenU(1), sidf, varlenU(2), varlenS(5), varlenU(3), lstrVar("fit"),
		varlenU(4), varlenS(12), varlenU(5), varlenS(16), varlenU(6), varlenU(ts), varlenU(7), varlenU(ts-1),
		varlenU(8), varlenU(310), varlenU(9), varlenU(80413), varlenU(10), varlenU(80013), varlenU(11), varlenU(9),
		varlenU(12), varlenU(1))
	g = mustDecode(t, d, event(GTIDTaggedEvent, tagged, true)).Body.(*GTID)
	if g.String() != "07bf9a8d-db83-11f1-bf7e-0242ac11000a:fit:5" || g.Flags != 1 || g.LastCommitted != 12 ||
		g.SequenceNumber != 16 || g.OriginalCommitTimestamp != ts-1 || g.OriginalServerVersion != 80013 || g.CommitGroupTicket != 9 {
		t.Fatalf("%+v", g)
	}
	if _, err := d.Decode(event(GTIDTaggedEvent, []byte{2}, true)); !errors.Is(err, ErrMalformed) {
		t.Error(err)
	}
	if _, err := d.Decode(event(GTIDTaggedEvent, msg(1, varlenU(2), []byte{0x03}), true)); !errors.Is(err, ErrMalformed) {
		t.Error(err)
	}
	// Previous GTIDs, untagged and tagged.
	prev := cat(le(1, 8), sid[:], le(2, 8), le(1, 8), le(6, 8), le(7, 8), le(8, 8))
	p := mustDecode(t, d, event(PreviousGTIDsEvent, prev, true)).Body.(*PreviousGTIDs)
	if p.String() != "07bf9a8d-db83-11f1-bf7e-0242ac11000a:1-5:7" {
		t.Fatal(p)
	}
	tprev := cat([]byte{1}, le(2, 6), []byte{1}, sid[:], varlenU(0), le(1, 8), le(1, 8), le(3, 8),
		sid[:], lstrVar("fit"), le(1, 8), le(1, 8), le(2, 8))
	p = mustDecode(t, d, event(PreviousGTIDsEvent, tprev, true)).Body.(*PreviousGTIDs)
	if p.String() != "07bf9a8d-db83-11f1-bf7e-0242ac11000a:1-2,07bf9a8d-db83-11f1-bf7e-0242ac11000a:fit:1" {
		t.Fatal(p)
	}
	for name, b := range map[string][]byte{
		"count":     cat(le(5, 8), sid[:]),
		"intervals": cat(le(1, 8), sid[:], le(9, 8), le(1, 8)),
		"interval":  cat(le(1, 8), sid[:], le(1, 8), le(1, 8), le(1, 4)),
		"tag":       cat([]byte{1}, le(1, 6), []byte{1}, sid[:], varlenU(40), make([]byte, 40)),
		"head":      le(1, 4),
	} {
		if _, err := d.Decode(event(PreviousGTIDsEvent, b, true)); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if FormatUUID([16]byte{}) != "00000000-0000-0000-0000-000000000000" {
		t.Error(FormatUUID([16]byte{}))
	}
}

func TestMariaDBGTIDs(t *testing.T) {
	d := newDecoder(t)
	b := event(MariaGTIDEvent, cat(le(31, 8), le(2, 4), []byte{MariaFlagStandalone}, make([]byte, 6)), true)
	binary.LittleEndian.PutUint32(b[5:], 3)
	binary.LittleEndian.PutUint32(b[len(b)-4:], crc32.ChecksumIEEE(b[:len(b)-4]))
	g := mustDecode(t, d, b).Body.(*MariaGTID)
	if g.String() != "2-3-31" || !g.Standalone() || g.FlagsExtra != 0 {
		t.Fatalf("%+v", g)
	}
	full := cat(le(32, 8), le(0, 4), []byte{MariaFlagGroupCommitID | MariaFlagPreparedXA}, le(99, 8),
		le(1, 4), []byte{2, 1}, []byte("abc"), []byte{5})
	g = mustDecode(t, d, event(MariaGTIDEvent, full, true)).Body.(*MariaGTID)
	if g.CommitID != 99 || g.XAFormat != 1 || string(g.XAGtrid) != "ab" || string(g.XABqual) != "c" || g.FlagsExtra != 5 || g.Standalone() {
		t.Fatalf("%+v", g)
	}
	if _, err := d.Decode(event(MariaGTIDEvent, le(1, 8), true)); !errors.Is(err, ErrMalformed) {
		t.Error(err)
	}
	l := mustDecode(t, d, event(MariaGTIDListEvent, cat(le(2|1<<28, 4), le(0, 4), le(1, 4), le(5, 8), le(1, 4), le(1, 4), le(6, 8)), true)).Body.(*MariaGTIDList)
	if l.Flags != 1 || len(l.GTIDs) != 2 || l.GTIDs[1].String() != "1-1-6" {
		t.Fatalf("%+v", l)
	}
	if _, err := d.Decode(event(MariaGTIDListEvent, cat(le(9, 4), le(0, 4)), true)); !errors.Is(err, ErrMalformed) {
		t.Error(err)
	}
}

func TestPayload(t *testing.T) {
	d := newDecoder(t)
	tm := event(TableMapEvent, tmBody(3, "db", "t", []byte{TypeLong}, nil, nil), false)
	rows := event(WriteRowsEventV2, rowsBody(3, true, false, 1, cat([]byte{0}, le(5, 4))), false)
	xid := event(XIDEvent, le(9, 8), false)
	inner := cat(tm, rows, xid)
	head := func(size, comp int) []byte {
		return cat([]byte{1}, lenenc(len(lenenc(size))), lenenc(size), []byte{2}, lenenc(len(lenenc(comp))), lenenc(comp),
			[]byte{3}, lenenc(len(lenenc(len(inner)))), lenenc(len(inner)), []byte{0})
	}
	p := mustDecode(t, d, event(TransactionPayload, cat(head(len(inner), CompressionNone), inner), true)).Body.(*Payload)
	if len(p.Events) != 3 || p.UncompressedSize != uint64(len(inner)) || p.Events[1].Body.(*Rows).Rows[0].After[0].AsInt() != 5 {
		t.Fatalf("%+v", p)
	}
	if !d.checksum {
		t.Fatal("checksums left off")
	}
	if _, err := d.Decode(event(TransactionPayload, cat(head(len(inner), CompressionZSTD), inner), true)); !errors.Is(err, ErrUnsupported) {
		t.Error(err)
	}
	for name, b := range map[string][]byte{
		"size":   cat(head(len(inner)+1, CompressionNone), inner),
		"header": {1, 5},
		"inner":  cat(head(10, CompressionNone), inner[:10]),
		"fde":    cat(head(len(xid), CompressionNone), func() []byte { x := append([]byte(nil), xid...); x[4] = byte(FormatDescriptionEvent); return x }()),
		"bad":    cat(head(len(xid), CompressionNone), func() []byte { x := append([]byte(nil), xid...); x[9] = 100; return x }()),
		"rows":   cat(head(len(rows), CompressionNone), func() []byte { x := append([]byte(nil), rows...); x[HeaderSize] = 9; return x }()),
	} {
		if _, err := d.Decode(event(TransactionPayload, b, true)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestTableMap(t *testing.T) {
	d := newDecoder(t)
	types := []byte{TypeLong, TypeVarChar, TypeString, TypeString, TypeString, TypeGeometry, TypeBlob, TypeNewDecimal, TypeLong, TypeEnum}
	meta := cat(le(40, 2), []byte{TypeString, 40}, []byte{TypeEnum, 1}, []byte{TypeSet, 1}, []byte{4}, []byte{2}, []byte{10, 2}, []byte{TypeEnum, 2})
	tlv := func(t byte, v []byte) []byte { return cat([]byte{t}, lenenc(len(v)), v) }
	opt := cat(
		tlv(MetaSignedness, []byte{0b0010_0000}),
		tlv(MetaDefaultCharset, cat(lenenc(255), lenenc(1), lenenc(63))),
		tlv(MetaEnumAndSetDefaultCharset, cat(lenenc(8), lenenc(0), lenenc(45))),
		tlv(MetaColumnName, cat(lstr("id"), lstr("v"), lstr("c"), lstr("e"), lstr("s"), lstr("g"), lstr("b"), lstr("d"), lstr("u"), lstr("e2"))),
		tlv(MetaSetStrValue, cat(lenenc(2), lstr("x"), lstr("y"))),
		tlv(MetaEnumStrValue, cat(lenenc(1), lstr("a"), lenenc(0))),
		tlv(MetaGeometryType, lenenc(1)),
		tlv(MetaPrimaryKeyWithPrefix, cat(lenenc(0), lenenc(0), lenenc(1), lenenc(10))),
		tlv(MetaColumnVisibility, []byte{0b1011_1111, 0b1100_0000}),
		tlv(99, []byte{1, 2}),
	)
	tm := mustDecode(t, d, event(TableMapEvent, tmBody(1<<40, "shop", "orders", types, meta, opt), true)).Body.(*TableMap)
	c := tm.Columns
	if tm.TableID != 1<<40 || tm.Schema != "shop" || tm.Table != "orders" || len(c) != 10 || d.Table(1<<40) != tm {
		t.Fatalf("%+v", tm)
	}
	if c[0].Name != "id" || c[2].Type != TypeString || c[3].Type != TypeEnum || c[4].Type != TypeSet ||
		c[1].Charset != 255 || c[2].Charset != 63 || c[3].Charset != 45 || c[4].Charset != 8 || !c[8].Unsigned || c[0].Unsigned ||
		c[4].SetValues[1] != "y" || c[3].EnumValues[0] != "a" || len(c[9].EnumValues) != 0 || c[5].GeometryType != 1 ||
		c[7].Meta != 10<<8|2 || !c[1].Invisible || c[0].Invisible || !c[9].Nullable {
		t.Fatalf("%+v", c)
	}
	if len(tm.PrimaryKey) != 2 || tm.PrimaryKey[1] != 1 || tm.PrimaryKeyPrefix[1] != 10 || tm.Metadata&(1<<MetaColumnName) == 0 {
		t.Fatalf("%+v", tm)
	}
	// Column charsets, a simple key, and MariaDB's GEOMETRY among the
	// character columns.
	opt2 := cat(tlv(MetaColumnCharset, cat(lenenc(33), lenenc(63), lenenc(28))), tlv(MetaEnumAndSetColumnCharset, cat(lenenc(8), lenenc(8), lenenc(8))),
		tlv(MetaSimplePrimaryKey, lenenc(0)))
	c = mustDecode(t, d, event(TableMapEvent, tmBody(2, "s", "t", types, meta, opt2), true)).Body.(*TableMap).Columns
	if c[1].Charset != 33 || c[6].Charset != 28 || c[5].Charset != 0 || c[9].Charset != 8 {
		t.Fatalf("%+v", c)
	}
	dm := NewDecoder()
	mustDecode(t, dm, fde("10.11.6-MariaDB-log", 19, postHeaders(), ChecksumOff))
	opt3 := tlv(MetaColumnCharset, cat(lenenc(33), lenenc(63), lenenc(63), lenenc(28)))
	c = mustDecode(t, dm, event(TableMapEvent, tmBody(2, "s", "t", types, meta, opt3), false)).Body.(*TableMap).Columns
	if c[5].Charset != 63 || c[6].Charset != 28 {
		t.Fatalf("%+v", c)
	}
	// A CHAR of more than 255 bytes: its length's high bits in the type
	// byte.
	if typ, n := stringType(TypeString, 0xce<<8|0xfc); typ != TypeString || n != 1020 {
		t.Fatal(typ, n)
	}
	for name, b := range map[string][]byte{
		"short":     tmBody(1, "s", "t", types, meta, nil)[:12],
		"columns":   cat(le(1, 6), le(0, 2), []byte{1, 's', 0, 1, 't', 0}, lenenc(5000)),
		"meta":      tmBody(1, "s", "t", types, meta[:3], nil),
		"nulls":     tmBody(1, "s", "t", types, meta, nil)[:len(tmBody(1, "s", "t", types, meta, nil))-1],
		"tlv":       tmBody(1, "s", "t", types, meta, []byte{1, 9}),
		"sign bits": tmBody(1, "s", "t", types, meta, tlv(MetaSignedness, nil)),
		"vis bits":  tmBody(1, "s", "t", types, meta, tlv(MetaColumnVisibility, []byte{1})),
		"charset":   tmBody(1, "s", "t", types, meta, tlv(MetaDefaultCharset, cat(lenenc(1), lenenc(9), lenenc(1)))),
		"names":     tmBody(1, "s", "t", types, meta, tlv(MetaColumnName, lstr("id"))),
		"set":       tmBody(1, "s", "t", types, meta, tlv(MetaSetStrValue, cat(lenenc(9), lstr("x")))),
		"pk":        tmBody(1, "s", "t", types, meta, tlv(MetaSimplePrimaryKey, lenenc(20))),
		"enum cs":   tmBody(1, "s", "t", types, meta, tlv(MetaEnumAndSetColumnCharset, lenenc(8))),
		"geometry":  tmBody(1, "s", "t", types, meta, tlv(MetaGeometryType, nil)),
	} {
		if _, err := d.Decode(event(TableMapEvent, b, true)); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A table ID of four bytes, from an old format description.
	d = NewDecoder()
	ph := postHeaders()
	ph[TableMapEvent-1] = 6
	ph[WriteRowsEventV1-1] = 6
	mustDecode(t, d, fde("8.0.40", 19, ph, ChecksumOff))
	tm = mustDecode(t, d, event(TableMapEvent, cat(le(9, 4), tmBody(9, "s", "t", []byte{TypeTiny}, nil, nil)[6:]), false)).Body.(*TableMap)
	if tm.TableID != 9 || tm.Schema != "s" {
		t.Fatalf("%+v", tm)
	}
	r := mustDecode(t, d, event(WriteRowsEventV1, cat(le(9, 4), rowsBody(9, false, false, 1, []byte{0, 0xfe})[6:]), false)).Body.(*Rows)
	if r.Rows[0].After[0].AsInt() != -2 {
		t.Fatalf("%+v", r)
	}
	// Past MaxTables, the maps are forgotten.
	d.MaxTables = 2
	for id := range uint64(3) {
		mustDecode(t, d, event(TableMapEvent, cat(le(id, 4), tmBody(id, "s", "t", []byte{TypeTiny}, nil, nil)[6:]), false))
	}
	if d.Table(0) != nil || d.Table(2) == nil {
		t.Fatal("tables kept")
	}
}

func TestRows(t *testing.T) {
	d := newDecoder(t)
	mustDecode(t, d, event(TableMapEvent, tmBody(5, "db", "t", []byte{TypeLong, TypeVarChar}, le(20, 2), nil), true))
	// An update, with a column not in the after image (MINIMAL).
	body := cat(le(5, 6), le(0, 2), le(4, 2), []byte{1, 2}, lenenc(2), []byte{3}, []byte{2},
		[]byte{0}, le(1, 4), lstr("a"), []byte{0}, lstr("b"),
		[]byte{2}, le(2, 4), []byte{0}, lstr("c"))
	r := mustDecode(t, d, event(UpdateRowsEventV2, body, true)).Body.(*Rows)
	if r.Kind != Update || !bytes.Equal(r.Extra, []byte{1, 2}) || len(r.Rows) != 2 || r.PresentAfter[0] ||
		r.Rows[0].Before[1].Text() != "a" || r.Rows[0].After[1].Text() != "b" || !r.Rows[0].After[0].IsNull() ||
		!r.Rows[1].Before[1].IsNull() || r.Rows[1].After[1].Text() != "c" {
		t.Fatalf("%+v", r)
	}
	r = mustDecode(t, d, event(DeleteRowsEventV1, rowsBody(5, false, false, 2, cat([]byte{2}, le(7, 4))), true)).Body.(*Rows)
	if r.Kind != Delete || r.Rows[0].Before[0].AsInt() != 7 || r.Rows[0].After != nil {
		t.Fatalf("%+v", r)
	}
	// The dummy event that ends a statement.
	r = mustDecode(t, d, event(WriteRowsEventV2, cat(le(dummyTableID, 6), le(1, 2), le(2, 2), lenenc(0)), true)).Body.(*Rows)
	if r.Table != nil || len(r.Rows) != 0 {
		t.Fatalf("%+v", r)
	}
	// MariaDB's compressed rows.
	r = mustDecode(t, d, event(MariaUpdateRowsCompressedV1, rowsBody(5, false, true, 2,
		compress(cat([]byte{0}, le(1, 4), lstr("x"), []byte{0}, le(1, 4), lstr("y")))), true)).Body.(*Rows)
	if r.Rows[0].After[1].Text() != "y" {
		t.Fatalf("%+v", r)
	}
	for name, c := range map[string]struct {
		t    EventType
		body []byte
		err  error
	}{
		"no table":   {WriteRowsEventV2, rowsBody(6, true, false, 2, []byte{0}), ErrNoTableMap},
		"columns":    {WriteRowsEventV2, rowsBody(5, true, false, 3, []byte{0}), ErrMalformed},
		"header":     {WriteRowsEventV2, le(5, 6), ErrMalformed},
		"bitmap":     {UpdateRowsEventV2, cat(le(5, 6), le(0, 2), le(2, 2), lenenc(2), []byte{3}), ErrMalformed},
		"null bits":  {WriteRowsEventV2, cat(le(5, 6), le(0, 2), le(2, 2), lenenc(2), []byte{0}, []byte{1}), ErrMalformed},
		"value":      {WriteRowsEventV2, rowsBody(5, true, false, 2, cat([]byte{0}, le(1, 2))), ErrMalformed},
		"compressed": {MariaWriteRowsCompressedV1, rowsBody(5, false, false, 2, []byte{0x10}), ErrMalformed},
	} {
		if _, err := d.Decode(event(c.t, c.body, true)); !errors.Is(err, c.err) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// MaxRowsSize bounds the values of an event.
	d.MaxRowsSize = 100
	many := bytes.Repeat(cat([]byte{0}, le(1, 4), lstr("x")), 10)
	if _, err := d.Decode(event(WriteRowsEventV2, rowsBody(5, true, false, 2, many), true)); !errors.Is(err, ErrTooLarge) {
		t.Error(err)
	}
}

func TestValues(t *testing.T) {
	d := newDecoder(t)
	unsigned := func(c *Column) { c.Unsigned = true }
	binaryCS := func(c *Column) { c.Charset = CharsetBinary }
	textCS := func(c *Column) { c.Charset = 45 }
	named := func(c *Column) { c.EnumValues, c.SetValues = []string{"a", "b"}, []string{"x", "y", "z"} }
	ts := time.Date(2024, 2, 29, 12, 34, 56, 0, time.UTC)
	dt := func(y, mo, dd, h, mi, s, us int) []byte {
		v := uint64((y*13+mo)<<22|dd<<17|h<<12|mi<<6|s) + 0x8000000000
		return cat([]byte{byte(v >> 32), byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}, fracBytes(us, 6))
	}
	for _, c := range []struct {
		name string
		typ  byte
		meta []byte
		data []byte
		set  func(*Column)
		want fitsync.Value
	}{
		{"tiny", TypeTiny, nil, []byte{0x80}, nil, fitsync.Int(-128)},
		{"tiny unsigned", TypeTiny, nil, []byte{0x80}, unsigned, fitsync.Uint(128)},
		{"short", TypeShort, nil, le(0xfffe, 2), nil, fitsync.Int(-2)},
		{"int24", TypeInt24, nil, le(0x800000, 3), nil, fitsync.Int(-8388608)},
		{"long", TypeLong, nil, le(0xffffffff, 4), unsigned, fitsync.Uint(4294967295)},
		{"longlong", TypeLongLong, nil, le(math.MaxUint64, 8), nil, fitsync.Int(-1)},
		{"float", TypeFloat, []byte{4}, le(uint64(math.Float32bits(0.1)), 4), nil, fitsync.Float(0.1)},
		{"double", TypeDouble, []byte{8}, le(math.Float64bits(1e300), 8), nil, fitsync.Float(1e300)},
		{"decimal", TypeNewDecimal, []byte{5, 2}, []byte{0x80, 0x0c, 0x22}, nil, fitsync.Decimal("12.34")},
		{"decimal neg", TypeNewDecimal, []byte{20, 10}, negate(cat([]byte{0x80}, be(1, 4), be(1, 4), be(5, 1))), nil, fitsync.Decimal("-1.0000000015")},
		{"decimal int", TypeNewDecimal, []byte{18, 0}, cat([]byte{0x80, 0, 0, 1}, be(2, 4)), nil, fitsync.Decimal("1000000002")},
		{"year", TypeYear, nil, []byte{124}, nil, fitsync.Int(2024)},
		{"year zero", TypeYear, nil, []byte{0}, nil, fitsync.Int(0)},
		{"date", TypeDate, nil, le(2024<<9|2<<5|29, 3), nil, fitsync.Date(2024, 2, 29)},
		{"newdate zero", TypeNewDate, nil, le(0, 3), nil, fitsync.String("0000-00-00")},
		{"time", TypeTime, nil, le(uint64(0x1000000-123456), 3), nil, fitsync.String("-12:34:56")},
		{"time positive", TypeTime, nil, le(10203, 3), nil, fitsync.String("01:02:03")},
		{"datetime", TypeDateTime, nil, le(20240229123456, 8), nil, fitsync.Time(ts)},
		{"datetime zero", TypeDateTime, nil, le(0, 8), nil, fitsync.String("0000-00-00 00:00:00")},
		{"timestamp", TypeTimestamp, nil, le(uint64(ts.Unix()), 4), nil, fitsync.Time(ts)},
		{"timestamp zero", TypeTimestamp, nil, le(0, 4), nil, fitsync.String("0000-00-00 00:00:00")},
		{"timestamp2", TypeTimestamp2, []byte{6}, cat(be(uint64(ts.Unix()), 4), be(5, 3)), nil, fitsync.Time(ts.Add(5 * time.Microsecond))},
		{"timestamp2 zero", TypeTimestamp2, []byte{2}, cat(be(0, 4), []byte{0}), nil, fitsync.String("0000-00-00 00:00:00.00")},
		{"datetime2", TypeDateTime2, []byte{6}, dt(2024, 2, 29, 12, 34, 56, 7), nil, fitsync.Time(ts.Add(7 * time.Microsecond))},
		{"datetime2 zero", TypeDateTime2, []byte{0}, dt(0, 0, 0, 0, 0, 0, 0)[:5], nil, fitsync.String("0000-00-00 00:00:00")},
		{"time2 fsp5", TypeTime2, []byte{5}, cat(be(0x800000+(1<<12|2<<6|3), 3), be(450000, 3)), nil, fitsync.String("01:02:03.45000")},
		{"time2 fsp6 neg", TypeTime2, []byte{6}, timeNeg6(), nil, fitsync.String("-00:00:01.500000")},
		{"time2 fsp4 neg", TypeTime2, []byte{4}, timeNeg4(), nil, fitsync.String("-00:00:01.5000")},
		{"time2 fsp4", TypeTime2, []byte{4}, cat(be(0x800000+1, 3), be(5000, 2)), nil, fitsync.String("00:00:01.5000")},
		{"bit", TypeBit, []byte{5, 1}, []byte{0x01, 0x23}, nil, fitsync.Uint(0x123)},
		{"enum", TypeString, []byte{TypeEnum, 1}, []byte{2}, named, fitsync.String("b")},
		{"enum zero", TypeString, []byte{TypeEnum, 1}, []byte{0}, named, fitsync.String("")},
		{"enum past", TypeString, []byte{TypeEnum, 2}, le(9, 2), named, fitsync.Uint(9)},
		{"enum unnamed", TypeEnum, []byte{TypeEnum, 1}, []byte{2}, nil, fitsync.Uint(2)},
		{"set", TypeString, []byte{TypeSet, 1}, []byte{5}, named, fitsync.String("x,z")},
		{"set unnamed", TypeSet, []byte{TypeSet, 1}, []byte{5}, nil, fitsync.Uint(5)},
		{"char", TypeString, []byte{TypeString, 10}, lstr("ab"), nil, fitsync.String("ab")},
		{"binary", TypeString, []byte{TypeString, 4}, cat([]byte{2}, []byte{0, 1}), binaryCS, fitsync.Bytes([]byte{0, 1})},
		{"long char", TypeString, []byte{0xce, 0xfc}, cat(le(2, 2), []byte("ab")), nil, fitsync.String("ab")},
		{"varchar", TypeVarChar, le(300, 2), cat(le(3, 2), []byte("abc")), nil, fitsync.String("abc")},
		{"var_string", TypeVarString, le(20, 2), lstr("abc"), nil, fitsync.String("abc")},
		{"blob", TypeBlob, []byte{2}, cat(le(2, 2), []byte{1, 2}), nil, fitsync.Bytes([]byte{1, 2})},
		{"text", TypeMediumBlob, []byte{3}, cat(le(2, 3), []byte("hi")), textCS, fitsync.String("hi")},
		{"geometry", TypeGeometry, []byte{4}, cat(le(1, 4), []byte{9}), nil, fitsync.Bytes([]byte{9})},
		{"vector", TypeVector, []byte{4}, cat(le(4, 4), le(uint64(math.Float32bits(1)), 4)), nil, fitsync.Bytes(le(uint64(math.Float32bits(1)), 4))},
		{"json", TypeJSON, []byte{4}, cat(le(uint64(len(jdoc(true))), 4), jdoc(true)), nil, fitsync.JSON([]byte("true"))},
		{"json empty", TypeJSON, []byte{4}, le(0, 4), nil, fitsync.JSON([]byte("null"))},
		{"null type", TypeNull, nil, nil, nil, fitsync.Null()},
	} {
		got, err := decodeValue(t, d, c.typ, c.meta, c.data, c.set)
		if err != nil || !got.Equal(c.want) {
			t.Errorf("%s: %v %v, want %v", c.name, got, err, c.want)
		}
	}
	// DATETIME in the decoder's zone.
	d.Location = time.FixedZone("x", 3600)
	got, _ := decodeValue(t, d, TypeDateTime, nil, le(20240229123456, 8), nil)
	if !got.AsTime().Equal(time.Date(2024, 2, 29, 12, 34, 56, 0, d.Location)) {
		t.Error(got)
	}
	for _, c := range []struct {
		name string
		typ  byte
		meta []byte
		data []byte
		err  error
	}{
		{"old decimal", TypeDecimal, nil, []byte{1}, ErrUnsupported},
		{"decimal meta", TypeNewDecimal, []byte{70, 2}, []byte{1}, ErrMalformed},
		{"decimal short", TypeNewDecimal, []byte{10, 2}, []byte{1}, ErrMalformed},
		{"blob meta", TypeBlob, []byte{0}, []byte{1}, ErrMalformed},
		{"blob short", TypeBlob, []byte{1}, []byte{5, 1}, ErrMalformed},
		{"set size", TypeString, []byte{TypeSet, 9}, []byte{1}, ErrMalformed},
		{"datetime2 neg", TypeDateTime2, []byte{0}, []byte{0, 0, 0, 0, 1}, ErrMalformed},
		{"time2 short", TypeTime2, []byte{0}, []byte{0}, ErrMalformed},
		{"time2 frac", TypeTime2, []byte{3}, []byte{0x80, 0, 0, 1}, ErrMalformed},
		{"tiny", TypeTiny, nil, nil, ErrMalformed},
		{"float", TypeFloat, []byte{4}, []byte{1}, ErrMalformed},
		{"double", TypeDouble, []byte{8}, []byte{1}, ErrMalformed},
		{"timestamp2", TypeTimestamp2, []byte{3}, []byte{1, 2, 3, 4, 5}, ErrMalformed},
		{"enum", TypeString, []byte{TypeEnum, 2}, []byte{1}, ErrMalformed},
		{"set", TypeString, []byte{TypeSet, 2}, []byte{1}, ErrMalformed},
		{"json", TypeJSON, []byte{4}, cat(le(1, 4), []byte{0x77}), ErrMalformed},
		{"json short", TypeJSON, []byte{4}, le(5, 4), ErrMalformed},
	} {
		if _, err := decodeValue(t, d, c.typ, c.meta, c.data, nil); !errors.Is(err, c.err) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	if fraction(5, 9) != "" || readFrac(&rd{b: []byte{1}}, 4) != 0 || readFrac(&rd{b: []byte{1}}, 6) != 0 || readFrac(&rd{}, 0) != 0 {
		t.Error("fractions")
	}
	if _, err := decodeDecimal(&rd{b: []byte{1}}, 10, 20); !errors.Is(err, ErrMalformed) {
		t.Error(err)
	}
}

func be(v uint64, n int) []byte {
	b := le(v, n)
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return b
}

func fracBytes(us, fsp int) []byte {
	switch fsp {
	case 1, 2:
		return []byte{byte(us / 10000)}
	case 3, 4:
		return be(uint64(us/100), 2)
	case 5, 6:
		return be(uint64(us), 3)
	}
	return nil
}

func negate(b []byte) []byte {
	for i := range b {
		b[i] = ^b[i]
	}
	return b
}

// timeNeg6 is TIME(6) -00:00:01.5, as MySQL packs it.
func timeNeg6() []byte {
	v := uint64(int64(0x800000000000) - (1<<24 + 500000))
	return be(v, 6)
}

// timeNeg4 is TIME(4) -00:00:01.5: the integer part rounded up, the
// fraction below it.
func timeNeg4() []byte {
	ip := int64(-2)
	frac := int64(0x10000 - 5000)
	return cat(be(uint64(ip+0x800000), 3), be(uint64(frac), 2))
}

func TestJSON(t *testing.T) {
	for _, c := range []struct {
		name string
		doc  []byte
		want string
	}{
		{"object", jdoc(jobj{{"a", int16(-1)}, {"bb", "x\"\\\n\r\t\b\f\x01é"}, {"c", nil}, {"dd", false}}), `{"a": -1, "bb": "x\"\\\n\r\t\b\f\u0001é", "c": null, "dd": false}`},
		{"array", jdoc(jarr{ju16(65535), ji32(-5), ju32(5), int64(-7), uint64(math.MaxUint64), 1.5, jarr{}, jobj{}}), `[65535, -5, 5, -7, 18446744073709551615, 1.5, [], {}]`},
		{"large", jdoc(jlg{jobj{{"k", ji32(-100000)}, {"u", ju32(100000)}, {"s", "v"}}}), `{"k": -100000, "u": 100000, "s": "v"}`},
		{"large array", jdoc(jlg{jarr{true, int16(3)}}), `[true, 3]`},
		{"decimal", jdoc(opq{TypeNewDecimal, []byte{5, 2, 0x80, 0x0c, 0x22}}), `12.34`},
		{"date", jdoc(opq{TypeDate, le(packedDateTime(2024, 2, 29, 0, 0, 0, 0), 8)}), `"2024-02-29"`},
		{"datetime", jdoc(opq{TypeDateTime, le(packedDateTime(2024, 2, 29, 3, 4, 5, 500000), 8)}), `"2024-02-29 03:04:05.500000"`},
		{"time", jdoc(opq{TypeTime, le(negTime, 8)}), `"-01:02:03.000000"`},
		{"blob", jdoc(opq{TypeBlob, []byte{1, 2, 3}}), `"base64:type252:AQID"`},
		{"bad utf8", jdoc("\xff"), "\"�\""},
		{"long string", jdoc(strings.Repeat("y", 200)), `"` + strings.Repeat("y", 200) + `"`},
	} {
		n, err := parseJSON(c.doc)
		if err != nil || n.String() != c.want {
			t.Errorf("%s: %v %v, want %s", c.name, n, err, c.want)
		}
	}
	deep := jdoc(true)
	for range maxJSONDepth + 1 {
		deep = jdoc(jarr{jraw(deep)})
	}

	shared := jdoc(strings.Repeat("z", 100))
	// Every entry of an array at the same string: the text would grow
	// past what the bytes can mean.
	n := 200
	big := cat([]byte{jsonSmallArray}, le(uint64(n), 2), le(uint64(4+3*n+len(shared)-1), 2))
	for range n {
		big = cat(big, []byte{jsonString}, le(uint64(4+3*n), 2))
	}
	big = append(big, shared[1:]...)
	for _, c := range []struct {
		name string
		doc  []byte
	}{
		{"empty", nil},
		{"type", []byte{0x33}},
		{"literal", []byte{jsonLiteral, 9}},
		{"short", []byte{jsonInt16, 1}},
		{"short u16", []byte{jsonUint16, 1}},
		{"short i32", []byte{jsonInt32, 1}},
		{"short u32", []byte{jsonUint32, 1}},
		{"short i64", []byte{jsonInt64, 1}},
		{"short u64", []byte{jsonUint64, 1}},
		{"short double", []byte{jsonDouble, 1}},
		{"nan", cat([]byte{jsonDouble}, le(math.Float64bits(math.NaN()), 8))},
		{"string", []byte{jsonString, 5, 'a'}},
		{"string length", []byte{jsonString, 0x80, 0x80, 0x80, 0x80, 0x80, 1}},
		{"string length end", []byte{jsonString, 0x80}},
		{"literal short", []byte{jsonLiteral}},
		{"opaque", []byte{jsonOpaque}},
		{"opaque length", []byte{jsonOpaque, TypeBlob, 9}},
		{"opaque decimal", jdoc(opq{TypeNewDecimal, []byte{5}})},
		{"opaque date", jdoc(opq{TypeDate, []byte{5}})},
		{"container", []byte{jsonSmallArray, 1}},
		{"count", cat([]byte{jsonSmallArray}, le(9, 2), le(4, 2))},
		{"key", cat([]byte{jsonSmallObject}, le(1, 2), le(12, 2), le(50, 2), le(1, 2), []byte{jsonLiteral, 0, 0})},
		{"offset", cat([]byte{jsonSmallArray}, le(1, 2), le(7, 2), []byte{jsonString}, le(50, 2))},
		{"inner", cat([]byte{jsonSmallArray}, le(1, 2), le(7, 2), []byte{jsonLiteral}, le(9, 2))},
		{"deep", deep},
		{"budget", big},
	} {
		if _, err := parseJSON(c.doc); err == nil {
			t.Errorf("%s: no error", c.name)
		}
	}
}

// jraw is a document already encoded, inside another.
type jraw []byte

func packedDateTime(y, mo, d, h, mi, s, us int) uint64 {
	ymd := (y*13+mo)<<5 | d
	hms := h<<12 | mi<<6 | s
	return uint64((ymd<<17|hms)<<24 | us)
}

func TestPartialJSON(t *testing.T) {
	d := newDecoder(t)
	mustDecode(t, d, event(TableMapEvent, tmBody(8, "db", "js", []byte{TypeLong, TypeJSON}, []byte{4}, nil), true))
	before := jdoc(jobj{{"a", int16(1)}, {"b", jarr{true, false}}, {"c", "x"}, {"k y", int16(2)}})
	diff := func(op byte, path string, v any) []byte {
		b := cat([]byte{op}, lstr(path))
		if op != jsonDiffRemove {
			b = cat(b, lstr(string(jdoc(v))))
		}
		return b
	}
	diffs := cat(diff(jsonDiffReplace, "$.a", int16(5)), diff(jsonDiffInsert, "$.bb", "n"), diff(jsonDiffRemove, "$.b[0]", nil),
		diff(jsonDiffInsert, "$.b[9]", int16(3)), diff(jsonDiffReplace, `$."k y"`, jarr{}), diff(jsonDiffInsert, "$.b[0]", nil))
	row := func(diffs []byte) []byte {
		return cat([]byte{0}, le(1, 4), le(uint64(len(before)), 4), before,
			[]byte{1, 1}, []byte{0}, le(1, 4), le(uint64(len(diffs)), 4), diffs)
	}
	ev := event(PartialUpdateRowsEvent, rowsBody(8, true, true, 2, row(diffs)), true)
	r := mustDecode(t, d, ev).Body.(*Rows)
	if got := r.Rows[0].After[1].Text(); got != `{"a": 5, "b": [null, false, 3], "c": "x", "bb": "n", "k y": []}` {
		t.Fatal(got)
	}
	// A partial update without the value before it (binlog_row_image
	// MINIMAL), and changes that do not apply.
	noBefore := cat(le(8, 6), le(0, 2), le(2, 2), lenenc(2), []byte{1}, []byte{3},
		[]byte{0}, le(1, 4), []byte{1, 1}, []byte{0}, le(1, 4), le(uint64(len(diffs)), 4), diffs)
	if _, err := d.Decode(event(PartialUpdateRowsEvent, noBefore, true)); !errors.Is(err, ErrUnsupported) {
		t.Error(err)
	}
	for name, ds := range map[string][]byte{
		"short":          le(9, 2),
		"op":             diff(7, "$.a", nil),
		"value":          cat([]byte{jsonDiffReplace}, lstr("$.a"), lenenc(0)),
		"bad value":      diff(jsonDiffReplace, "$.a", nil)[:5],
		"bad value doc":  cat([]byte{jsonDiffReplace}, lstr("$.a"), lstr("\x33")),
		"path":           diff(jsonDiffReplace, "a", int16(1)),
		"missing":        diff(jsonDiffReplace, "$.zz", int16(1)),
		"missing parent": diff(jsonDiffReplace, "$.zz.y", int16(1)),
		"remove missing": diff(jsonDiffRemove, "$.zz", nil),
		"insert root":    diff(jsonDiffInsert, "$", int16(1)),
		"insert index":   diff(jsonDiffInsert, "$[0]", int16(1)),
	} {
		b := cat([]byte{0}, le(1, 4), le(uint64(len(before)), 4), before, []byte{1, 1}, []byte{0}, le(1, 4), le(uint64(len(ds)), 4), ds)
		if name == "short" {
			b = cat([]byte{0}, le(1, 4), le(uint64(len(before)), 4), before, []byte{1, 1}, []byte{0}, le(1, 4), ds)
		}
		if _, err := d.Decode(event(PartialUpdateRowsEvent, rowsBody(8, true, true, 2, b), true)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	// A bad document before.
	bad := cat([]byte{0}, le(1, 4), le(1, 4), []byte{0x33}, []byte{1, 1}, []byte{0}, le(1, 4), le(uint64(len(diffs)), 4), diffs)
	if _, err := d.Decode(event(PartialUpdateRowsEvent, rowsBody(8, true, true, 2, bad), true)); err == nil {
		t.Error("a bad document before")
	}
	// The whole document replaced.
	doc, err := applyDiff(&jnode{kind: 's', text: "1"}, jsonDiffReplace, "$", &jnode{kind: 's', text: "2"})
	if err != nil || doc.String() != "2" {
		t.Fatal(doc, err)
	}
	for _, p := range []string{"$.a.b[3]", `$."a\"b".c`, "$[ 2 ]"} {
		if _, err := parsePath(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	for _, p := range []string{"$.\"a", "$[x]", "$[1", "$x", `$."\q"`} {
		if _, err := parsePath(p); err == nil {
			t.Errorf("%s: no error", p)
		}
	}
}

func TestReader(t *testing.T) {
	file := cat(Magic, fde("8.0.40", 19, postHeaders(), ChecksumCRC32), event(XIDEvent, le(1, 8), true))
	r, err := NewReader(bytes.NewReader(file))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := r.Next(); err != nil {
			t.Fatal(err)
		}
	}
	if r.Position() != int64(len(file)) {
		t.Fatal(r.Position())
	}
	if _, err := r.Next(); err != io.EOF {
		t.Fatal(err)
	}
	if _, err := NewReader(bytes.NewReader([]byte{1, 2})); err == nil {
		t.Error("short magic")
	}
	if _, err := NewReader(bytes.NewReader([]byte{1, 2, 3, 4})); !errors.Is(err, ErrMalformed) {
		t.Error(err)
	}
	big := event(UserVarEvent, make([]byte, 2<<20), true)
	for name, c := range map[string]struct {
		b   []byte
		err error
	}{
		"truncated header": {cat(Magic, []byte{1, 2, 3}), io.ErrUnexpectedEOF},
		"truncated event":  {cat(Magic, event(XIDEvent, le(1, 8), true)[:25]), io.ErrUnexpectedEOF},
		"small":            {cat(Magic, make([]byte, HeaderSize)), ErrMalformed},
		"decode":           {cat(Magic, event(XIDEvent, le(1, 2), false)), ErrMalformed},
		"large":            {cat(Magic, big), ErrTooLarge},
		"claims more":      {cat(Magic, event(XIDEvent, le(1, 8), false)[:9], le(60<<20, 4), make([]byte, 100)), io.ErrUnexpectedEOF},
		"encrypted":        {cat(Magic, event(MariaStartEncryptionEvent, []byte{1}, false)), ErrUnsupported},
	} {
		r, _ := NewReader(bytes.NewReader(c.b))
		if name == "large" {
			r.MaxEventSize = 1 << 20
		}
		if _, err := r.Next(); !errors.Is(err, c.err) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A large event, then a small one: the buffer is let go.
	r, _ = NewReader(bytes.NewReader(cat(Magic, big, event(XIDEvent, le(1, 8), false))))
	r.SetChecksum(true)
	if _, err := r.Next(); err != nil {
		t.Fatal(err)
	}
	r.SetChecksum(false)
	if _, err := r.Next(); err != nil || cap(r.buf) > 1<<20 {
		t.Fatal(err, cap(r.buf))
	}
}

var negTimeInt = -int64((1<<12 | 2<<6 | 3) << 24)

var negTime = uint64(negTimeInt)

func TestLenEnc(t *testing.T) {
	for _, c := range []struct {
		b    []byte
		n    uint64
		null bool
		bad  bool
	}{
		{[]byte{5}, 5, false, false},
		{[]byte{0xfb}, 0, true, false},
		{[]byte{0xfc, 1, 2}, 0x201, false, false},
		{[]byte{0xfd, 1, 2, 3}, 0x30201, false, false},
		{cat([]byte{0xfe}, le(1<<40, 8)), 1 << 40, false, false},
		{[]byte{0xff}, 0, false, true},
	} {
		r := rd{b: c.b}
		n, null := r.lenEnc()
		if n != c.n || null != c.null || r.bad != c.bad {
			t.Errorf("%x: %d %v %v", c.b, n, null, r.bad)
		}
	}
	r := rd{b: []byte{9, 1}}
	if r.lenEncBytes() != nil || !r.bad {
		t.Error("lenEncBytes")
	}
	r = rd{b: []byte{1, 2, 3}}
	if r.u56() != 0 || !r.bad {
		t.Error("u56")
	}
}
