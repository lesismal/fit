package binlog

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"hash/crc32"
	"math"
	"testing"

	fitsync "github.com/lesismal/fit/sync"
)

// Builders of events for the unit tests.

func le(v uint64, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(v >> (8 * i))
	}
	return b
}

func cat(parts ...[]byte) []byte {
	var b []byte
	for _, p := range parts {
		b = append(b, p...)
	}
	return b
}

func lenenc(n int) []byte {
	switch {
	case n < 251:
		return []byte{byte(n)}
	case n < 1<<16:
		return cat([]byte{0xfc}, le(uint64(n), 2))
	case n < 1<<24:
		return cat([]byte{0xfd}, le(uint64(n), 3))
	}
	return cat([]byte{0xfe}, le(uint64(n), 8))
}

func lstr(s string) []byte { return cat(lenenc(len(s)), []byte(s)) }

// event is an event of type t with body, and a CRC32 when crc.
func event(t EventType, body []byte, crc bool) []byte {
	size := HeaderSize + len(body)
	if crc {
		size += 4
	}
	b := cat(le(1, 4), []byte{byte(t)}, le(1, 4), le(uint64(size), 4), le(0, 4), le(0, 2), body)
	if crc {
		b = binary.LittleEndian.AppendUint32(b, crc32.ChecksumIEEE(b))
	}
	return b
}

// postHeaders are MySQL 8's post-header lengths.
func postHeaders() []byte {
	ph := make([]byte, 42)
	ph[QueryEvent-1] = 13
	ph[RotateEvent-1] = 8
	ph[TableMapEvent-1] = 8
	for _, t := range []EventType{WriteRowsEventV1, UpdateRowsEventV1, DeleteRowsEventV1} {
		ph[t-1] = 8
	}
	for _, t := range []EventType{WriteRowsEventV2, UpdateRowsEventV2, DeleteRowsEventV2, PartialUpdateRowsEvent} {
		ph[t-1] = 10
	}
	ph[GTIDEvent-1] = 42
	return ph
}

// fde is a format description of version, with the post-header lengths
// ph, and the checksum algorithm alg (none for an old version).
func fde(version string, hl byte, ph []byte, alg byte) []byte {
	v := make([]byte, 50)
	copy(v, version)
	body := cat(le(4, 2), v, le(0, 4), []byte{hl}, ph)
	if !hasChecksumAlg(version) {
		return event(FormatDescriptionEvent, body, false)
	}
	body = append(body, alg)
	return event(FormatDescriptionEvent, body, true)
}

// newDecoder is a Decoder after a MySQL 8.0 format description with
// CRC32 checksums.
func newDecoder(t *testing.T) *Decoder {
	t.Helper()
	d := NewDecoder()
	mustDecode(t, d, fde("8.0.40", 19, postHeaders(), ChecksumCRC32))
	return d
}

func mustDecode(t *testing.T, d *Decoder, b []byte) *Event {
	t.Helper()
	ev, err := d.Decode(b)
	if err != nil {
		t.Fatalf("decode %s: %v", EventType(b[4]), err)
	}
	return ev
}

// tmBody is a table map's body: every column nullable.
func tmBody(id uint64, schema, table string, types, meta, opt []byte) []byte {
	nulls := bytes.Repeat([]byte{0xff}, (len(types)+7)/8)
	return cat(le(id, 6), le(1, 2), []byte{byte(len(schema))}, []byte(schema), []byte{0},
		[]byte{byte(len(table))}, []byte(table), []byte{0}, lenenc(len(types)), types, lenenc(len(meta)), meta, nulls, opt)
}

func allBits(n int) []byte {
	b := make([]byte, (n+7)/8)
	for i := range n {
		b[i/8] |= 1 << (i % 8)
	}
	return b
}

// rowsBody is a rows event's body of n columns, all present.
func rowsBody(id uint64, v2, update bool, n int, rows []byte) []byte {
	b := cat(le(id, 6), le(RowsStmtEnd, 2))
	if v2 {
		b = append(b, le(2, 2)...)
	}
	b = cat(b, lenenc(n), allBits(n))
	if update {
		b = append(b, allBits(n)...)
	}
	return append(b, rows...)
}

// decodeValue decodes one value of a column of type typ and meta from
// data, in a table of that column alone, after set changes its column.
func decodeValue(t *testing.T, d *Decoder, typ byte, meta []byte, data []byte, set func(*Column)) (fitsync.Value, error) {
	t.Helper()
	ev := mustDecode(t, d, event(TableMapEvent, tmBody(7, "db", "t", []byte{typ}, meta, nil), true))
	if set != nil {
		set(&ev.Body.(*TableMap).Columns[0])
	}
	e, err := d.Decode(event(WriteRowsEventV2, rowsBody(7, true, false, 1, cat([]byte{0}, data)), true))
	if err != nil {
		return fitsync.Value{}, err
	}
	return e.Body.(*Rows).Rows[0].After[0], nil
}

func compress(b []byte) []byte {
	var z bytes.Buffer
	w := zlib.NewWriter(&z)
	w.Write(b)
	w.Close()
	return cat([]byte{0x82}, []byte{byte(len(b) >> 8), byte(len(b))}, z.Bytes())
}

// MySQL's binary JSON, encoded for the tests.
type (
	kv struct {
		k string
		v any
	}
	jobj []kv
	jarr []any
	jlg  struct{ v any } // a large container
	opq  struct {
		t    byte
		data []byte
	}
	ji32 int32
	ju32 uint32
	ju16 uint16
)

// jdoc is v's binary JSON, its type byte first.
func jdoc(v any) []byte {
	t, b := jenc(v, false)
	return cat([]byte{t}, b)
}

func jvar(n int) []byte {
	var b []byte
	for {
		c := byte(n & 0x7f)
		n >>= 7
		if n == 0 {
			return append(b, c)
		}
		b = append(b, c|0x80)
	}
}

func jenc(v any, _ bool) (byte, []byte) {
	switch x := v.(type) {
	case jraw:
		return x[0], x[1:]
	case nil:
		return jsonLiteral, []byte{0}
	case bool:
		if x {
			return jsonLiteral, []byte{1}
		}
		return jsonLiteral, []byte{2}
	case int16:
		return jsonInt16, le(uint64(x), 2)
	case ju16:
		return jsonUint16, le(uint64(x), 2)
	case ji32:
		return jsonInt32, le(uint64(x), 4)
	case ju32:
		return jsonUint32, le(uint64(x), 4)
	case int64:
		return jsonInt64, le(uint64(x), 8)
	case uint64:
		return jsonUint64, le(x, 8)
	case float64:
		return jsonDouble, le(math.Float64bits(x), 8)
	case string:
		return jsonString, cat(jvar(len(x)), []byte(x))
	case opq:
		return jsonOpaque, cat([]byte{x.t}, jvar(len(x.data)), x.data)
	case jobj:
		return jcontainer(x, nil, false)
	case jarr:
		return jcontainer(nil, x, false)
	case jlg:
		if o, ok := x.v.(jobj); ok {
			return jcontainer(o, nil, true)
		}
		return jcontainer(nil, x.v.(jarr), true)
	}
	panic("jenc")
}

func jcontainer(o jobj, a jarr, large bool) (byte, []byte) {
	w := 2
	if large {
		w = 4
	}
	vals := []any(a)
	n := len(a)
	if o != nil {
		n = len(o)
		vals = nil
		for _, e := range o {
			vals = append(vals, e.v)
		}
	}
	head := 2*w + n*(1+w)
	if o != nil {
		head += n * (w + 2)
	}
	var keys, entries, data []byte
	off := head
	if o != nil {
		for _, e := range o {
			off += len(e.k)
		}
		koff := head
		var ke []byte
		for _, e := range o {
			ke = cat(ke, le(uint64(koff), w), le(uint64(len(e.k)), 2))
			koff += len(e.k)
			keys = append(keys, e.k...)
		}
		entries = ke
	}
	for _, v := range vals {
		t, b := jenc(v, large)
		inline := t == jsonLiteral || t == jsonInt16 || t == jsonUint16 || large && (t == jsonInt32 || t == jsonUint32)
		if inline {
			entries = cat(entries, []byte{t}, b, make([]byte, w-len(b)))
			continue
		}
		entries = cat(entries, []byte{t}, le(uint64(off+len(data)), w))
		data = append(data, b...)
	}
	body := cat(entries, keys, data)
	size := 2*w + len(body)
	t := byte(jsonSmallArray)
	if o != nil {
		t = jsonSmallObject
	}
	if large {
		t++
	}
	return t, cat(le(uint64(n), w), le(uint64(size), w), body)
}
