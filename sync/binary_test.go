package fitsync

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
	"time"
)

// binaryRecords are records of every kind and part the binary form holds.
func binaryRecords() []*Record {
	full := &Record{Source: "mysql", Namespace: "shop.orders", Op: OpUpdate, Key: Map(Field{"id", Int(7)}),
		Before: Map(Field{"id", Int(7)}), After: everyKind(),
		CommitTime: time.Date(2026, 11, 9, 10, 0, 0, 5, time.FixedZone("", -5*3600-1800)),
		Tx:         Tx{ID: "gtid:1", First: true, Last: true}, Position: Position(`{"file":"b.1","pos":4}`)}
	return []*Record{full, {}, {Op: OpMessage, After: String("a line"), Tx: Tx{Last: true}},
		{Key: Array(), After: Map(), CommitTime: time.Unix(0, 0).UTC()}}
}

func TestBinaryRoundTrip(t *testing.T) {
	in := binaryRecords()
	b := AppendBatchBinary(nil, in)
	out, err := ParseBatchBinary(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(in) {
		t.Fatalf("%d records of %d", len(out), len(in))
	}
	for i := range in {
		want, _ := in[i].MarshalJSON()
		got, _ := out[i].MarshalJSON()
		if string(got) != string(want) {
			t.Errorf("record %d:\n got %s\nwant %s", i, got, want)
		}
		if !out[i].made || !out[i].Key.Equal(in[i].Key) || !out[i].After.Equal(in[i].After) || !out[i].CommitTime.Equal(in[i].CommitTime) {
			t.Errorf("record %d differs", i)
		}
	}
	// Written again, the same bytes; a BinaryBatch writes them too.
	if again := AppendBatchBinary(nil, out); !bytes.Equal(again, b) {
		t.Error("written again, other bytes")
	}
	var bb BinaryBatch
	bb.Reset(in)
	if !bytes.Equal(bb.Buf, b) {
		t.Error("a BinaryBatch writes other bytes")
	}
	// The time's zone is kept.
	if _, off := out[0].After.fields[12].Value.AsTime().Zone(); off != 8*3600 {
		t.Errorf("offset %d", off)
	}
	if out[0].After.fields[13].Value.AsTime().Location() != time.UTC {
		t.Error("UTC lost")
	}
}

// TestBinaryRecordOf checks a record a plugin hands back takes the parts
// it left as they were from the batch's record, and reads the others.
func TestBinaryRecordOf(t *testing.T) {
	in := binaryRecords()
	var bb BinaryBatch
	bb.Reset(in)

	same := *in[0]
	changed := *in[0]
	changed.Namespace = "orders"
	changed.After = Map(Field{"id", Int(8)})
	changed.CommitTime = changed.CommitTime.Add(time.Second)
	changed.Position = Position(`{"pos":5}`)
	var emitted []byte
	for i, r := range []*Record{&same, &changed, &same} {
		emitted = r.AppendBinary(binary.AppendUvarint(emitted, uint64(i)))
	}
	var r BinaryReader
	r.Reset(emitted)
	var got []*Record
	for r.More() {
		u, err := r.Uvarint()
		if err != nil {
			t.Fatal(err)
		}
		// The third is read as of record 2 of the batch, which it is not.
		rec, err := r.RecordOf(&bb, []int{0, 0, 2}[u])
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, rec)
	}
	if len(got) != 3 {
		t.Fatalf("%d records", len(got))
	}
	// Shared: the same map, not a copy.
	if &got[0].After.fields[0] != &in[0].After.fields[0] || &got[1].Key.fields[0] != &in[0].Key.fields[0] {
		t.Error("an unchanged value was read again")
	}
	if &got[0].Position[0] != &in[0].Position[0] {
		t.Error("an unchanged position was read again")
	}
	if &got[1].After.fields[0] == &in[0].After.fields[0] || got[1].Namespace != "orders" ||
		!got[1].CommitTime.Equal(changed.CommitTime) || string(got[1].Position) != `{"pos":5}` {
		t.Errorf("changed parts not read: %+v", got[1])
	}
	for i, want := range []*Record{&same, &changed, &same} {
		a, _ := want.MarshalJSON()
		b, _ := got[i].MarshalJSON()
		if string(a) != string(b) {
			t.Errorf("record %d:\n got %s\nwant %s", i, b, a)
		}
	}
	if &got[2].After.fields[0] == &in[0].After.fields[0] {
		t.Error("a record took values of another of the batch")
	}
	bb.Reset(nil)
	if bb.recs != nil || len(bb.Buf) != 1 {
		t.Error("Reset(nil) kept the records")
	}
}

func TestBinaryRefused(t *testing.T) {
	good := AppendBatchBinary(nil, binaryRecords())
	for n := range len(good) {
		if _, err := ParseBatchBinary(good[:n]); err == nil {
			t.Errorf("cut to %d bytes, read", n)
		}
	}
	if _, err := ParseBatchBinary(append(good[:len(good):len(good)], 0)); err == nil || err.Error() != "more after the batch" {
		t.Error(err)
	}
	rec := func(parts ...[]byte) []byte {
		b := []byte{1}
		for _, p := range parts {
			b = append(b, p...)
		}
		return b
	}
	str := func(s string) []byte { return appendStr(nil, s) }
	null := []byte{binaryTag}
	head := rec([]byte{byte(OpInsert), 0}, str(""), str(""))
	tail := rec(str(""), str(""), str(""))[1:]
	value := func(v ...byte) []byte {
		return append(append(append(append([]byte{}, head...), null...), null...), append(v, tail...)...)
	}
	container := func(tag byte, body ...byte) []byte {
		return append(binary.LittleEndian.AppendUint32([]byte{tag}, uint32(len(body))), body...)
	}
	for _, bad := range []struct {
		data []byte
		want string
	}{
		{rec([]byte{9, 0}, make([]byte, 10)), "op 9 is not an operation"},
		{rec([]byte{1, 4}, make([]byte, 10)), "flags 0x4"},
		{value(0x0F), "a value's tag 0xf"},
		{value(0x1D), "a value's tag 0x1d"},
		{value(binaryTag+byte(KindBool), 2), "a bool of 2"},
		{value(binaryTag + byte(KindFloat)), "cut short"},
		{value(append([]byte{binaryTag + byte(KindJSON)}, str("{")...)...), "a json value that is not JSON"},
		{value(binaryTag+byte(KindTime), 0, 0x80, 0x94, 0xeb, 0xdc, 0x03, 0), "a time out of range"},
		{value(binaryTag+byte(KindTime), 0, 0, 0x80, 0xa3, 0x15), "a time out of range"},
		{value(binaryTag+byte(KindDate), 0, 13, 1), "the date 0-13-1"},
		{value(binaryTag+byte(KindDate), 0), "the date"},
		{value(append([]byte{binaryCanonical}, str(`{"int":1`)...)...), "a value in canonical JSON"},
		{value(container(binaryTag+byte(KindArray), 2, binaryTag)...), "tag 0x0"},
		{value(container(binaryTag+byte(KindArray), 1, binaryTag, binaryTag)...), "a array's length is not what it holds"},
		{value(container(binaryTag+byte(KindArray), 1, 0x0F)...), "tag 0xf"},
		{value(binaryTag+byte(KindArray), 1, 0), ""},
		{value(container(binaryTag + byte(KindArray))...), "length is not what it holds"},
		{value(container(binaryTag + byte(KindMap))...), "length is not what it holds"},
		{value(binaryTag+byte(KindArray), 9, 0, 0, 0), "cut short"},
		{value(container(binaryTag+byte(KindMap), 2, 1, 'a', binaryTag, 1, 'a', binaryTag)...), `"a" is there twice`},
		{value(container(binaryTag+byte(KindMap), 1, 1, 'a', 0x0F)...), "a: a value's tag 0xf"},
		{value(container(binaryTag+byte(KindMap), 1, 5, 'a')...), "cut short"},
		{rec([]byte{1, 0}, str(""), str(""), null, null, null, str("yesterday")), "commit_time"},
		{rec([]byte{1, 0}, str(""), str(""), null, null, null, str(""), str(""), str("{")), "position: not JSON"},
		{[]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x02}, "past 64 bits"},
	} {
		_, err := ParseBatchBinary(bad.data)
		if err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%x: %v, want %q", bad.data, err, bad.want)
		}
	}
	// Nested deeper than a value may be.
	deep := []byte{binaryTag}
	for range maxBinaryDepth + 1 {
		deep = container(binaryTag+byte(KindArray), append([]byte{1}, deep...)...)
	}
	if _, err := ParseBatchBinary(value(deep...)); err == nil || !strings.Contains(err.Error(), "nested too deep") {
		t.Error(err)
	}
	// Many fields and long arrays come out of blocks of their own.
	many := make([]Field, readerFields)
	elems := make([]Value, readerValues)
	for i := range many {
		many[i] = Field{strings.Repeat("f", i), Int(int64(i))}
	}
	big := &Record{After: Map(many...), Before: Array(elems...)}
	out, err := ParseBatchBinary(AppendBatchBinary(nil, []*Record{big, big}))
	if err != nil || !out[1].After.Equal(big.After) || !out[1].Before.Equal(big.Before) {
		t.Errorf("%v", err)
	}
}

// TestBinaryValuesCutShort reads each kind's value cut short, as no
// array's or map's length would let it be.
func TestBinaryValuesCutShort(t *testing.T) {
	for _, f := range everyKind().Fields() {
		b := f.Value.AppendBinary(nil)
		for n := range len(b) {
			var r BinaryReader
			r.Reset(b[:n])
			if v, err := r.value(0); err == nil {
				t.Errorf("%s cut to %d bytes: %v", f.Name, n, v)
			}
		}
	}
	var r BinaryReader
	r.Reset([]byte{binaryCanonical, 4, 'n'})
	if _, err := r.value(0); err == nil {
		t.Error("canonical JSON cut short")
	}
	// An array or a map without its count.
	for _, tag := range []byte{binaryTag + byte(KindArray), binaryTag + byte(KindMap)} {
		var r BinaryReader
		r.Reset([]byte{tag, 0, 0, 0, 0})
		if _, err := r.value(0); err == nil {
			t.Errorf("%#x without its count", tag)
		}
	}
}

// TestBinaryCanonical checks a value a plugin wrote as canonical JSON is
// read as it says.
func TestBinaryCanonical(t *testing.T) {
	b := binary.AppendUvarint(nil, 1)
	b = append(b, byte(OpInsert), 0)
	b = appendStr(appendStr(b, ""), "")
	b = append(b, binaryTag, binaryTag, binaryCanonical)
	b = appendStr(b, `{"map":{"price":{"decimal":"1.20"}}}`)
	b = appendStr(appendStr(appendStr(b, ""), ""), "")
	out, err := ParseBatchBinary(b)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := out[0].After.Get("price"); v.Kind() != KindDecimal || v.Text() != "1.20" {
		t.Errorf("%v", v)
	}
}
