package synckafka

import (
	"bytes"
	"math"
	"strings"
	"testing"
	"time"

	fitsync "github.com/lesismal/fit/sync"
)

// mapsEncode is the codec's first encoder, record by record, as Encode
// was before it was written by hand.
func mapsEncode(b []byte, recs []*fitsync.Record) ([]byte, error) {
	for _, r := range recs {
		var err error
		if b, err = encodeMaps(b, r); err != nil {
			return b, err
		}
	}
	return b, nil
}

// sameAsMaps checks Encode writes recs as encodeMaps does, and fails the
// same way.
func sameAsMaps(t testing.TB, recs ...*fitsync.Record) {
	t.Helper()
	c := &Debezium{}
	for _, prefix := range [][]byte{nil, []byte("prefix")} {
		want, werr := mapsEncode(bytes.Clone(prefix), recs)
		got, gerr := c.Encode(bytes.Clone(prefix), recs)
		if !bytes.Equal(got, want) || (werr == nil) != (gerr == nil) || werr != nil && werr.Error() != gerr.Error() {
			t.Fatalf("hand-written:\n%s %v\nmaps:\n%s %v", got, gerr, want, werr)
		}
	}
}

func f(name string, v fitsync.Value) fitsync.Field { return fitsync.Field{Name: name, Value: v} }

// encodeValues is a value of every kind, the awkward ones too.
func encodeValues() []fitsync.Value {
	tm := time.Date(2026, 11, 9, 10, 0, 0, 500, time.FixedZone("", 8*3600))
	return []fitsync.Value{
		fitsync.Null(), fitsync.Bool(true), fitsync.Bool(false),
		fitsync.Int(0), fitsync.Int(-5), fitsync.Int(math.MinInt64), fitsync.Int(math.MaxInt64),
		fitsync.Uint(0), fitsync.Uint(math.MaxUint64),
		fitsync.Float(0), fitsync.Float(-1.5), fitsync.Float(1e21), fitsync.Float(1e-7), fitsync.Float(math.SmallestNonzeroFloat64),
		fitsync.Float(math.MaxFloat64), fitsync.Float(math.Copysign(0, -1)),
		fitsync.Decimal("12.30"), fitsync.Decimal("-0.002"), fitsync.Decimal("1e5"), fitsync.Decimal("1E-5"), fitsync.Decimal("0"),
		fitsync.String(""), fitsync.String("plain"), fitsync.String(`q"b\s`), fitsync.String("<a>&b"), fitsync.String("\x00\x1f\t\n"),
		fitsync.String("中文 é"), fitsync.String("  "), fitsync.String("bad\xffutf8"), fitsync.String("\x7f"),
		fitsync.Bytes(nil), fitsync.Bytes([]byte{0, 0xff, 0xfe, '<'}),
		fitsync.Time(tm), fitsync.Time(tm.UTC()), fitsync.Time(time.Time{}), fitsync.Date(2026, 11, 9),
		fitsync.JSON([]byte(` {"a": [1, 2.5, "s<"], "b": null} `)), fitsync.JSON([]byte(`" &"`)), fitsync.JSON([]byte(`"x` + " " + `"`)),
		fitsync.JSON([]byte(`1e400`)),
		fitsync.Array(), fitsync.Array(fitsync.Int(1), fitsync.Null(), fitsync.String("x"), fitsync.Array(fitsync.Bool(true))),
		fitsync.Map(), fitsync.Map(f("n", fitsync.Map(f("deep", fitsync.Array(fitsync.Map(f("x<", fitsync.Uint(3)))))))),
	}
}

func TestDebeziumEncodeSameAsMaps(t *testing.T) {
	vals := encodeValues()
	var row []fitsync.Field
	for i, v := range vals {
		row = append(row, f("c"+strings.Repeat("x", i%3)+string(rune('a'+i%26)), v))
	}
	full := fitsync.Map(row...)
	ops := []fitsync.Op{fitsync.OpInsert, fitsync.OpUpdate, fitsync.OpDelete, fitsync.OpMessage}
	times := []time.Time{{}, time.UnixMilli(1762650000123), time.UnixMilli(-5)}
	spaces := []string{"shop.items", "nodot", "", "d.t.x", "<d>.\"t\"", "库.表"}
	for _, op := range ops {
		for _, ct := range times {
			for _, ns := range spaces {
				key := fitsync.Map(f("id", fitsync.Int(3)))
				sameAsMaps(t, &fitsync.Record{Namespace: ns, Op: op, After: full, Before: full, Key: key, CommitTime: ct})
				sameAsMaps(t, &fitsync.Record{Namespace: ns, Op: op, Before: full, Key: key, CommitTime: ct})
				sameAsMaps(t, &fitsync.Record{Namespace: ns, Op: op, After: full, CommitTime: ct})
				sameAsMaps(t, &fitsync.Record{Namespace: ns, Op: op, CommitTime: ct})
			}
		}
	}
	// Each value alone as a column and as a whole row, so the ones the
	// hand-written path hands back are seen apart from the rest.
	for _, v := range vals {
		sameAsMaps(t, &fitsync.Record{Namespace: "d.t", Op: fitsync.OpUpdate, After: fitsync.Map(f("v", v)), Before: v})
		sameAsMaps(t, &fitsync.Record{Namespace: "d.t", Op: fitsync.OpInsert, After: v})
	}
	// What the hand-written path hands to encodeMaps: a float JSON cannot
	// hold (an error), a decimal that is not a JSON number and a JSON
	// document that does not parse (the record left out), an operation
	// with no event (an error), in a batch with good records around them.
	good := &fitsync.Record{Namespace: "d.t", Op: fitsync.OpInsert, After: fitsync.Map(f("id", fitsync.Int(1)))}
	for _, bad := range []fitsync.Value{fitsync.Float(math.NaN()), fitsync.Float(math.Inf(1)), fitsync.Float(math.Inf(-1)),
		fitsync.Decimal("abc"), fitsync.Decimal("+1"), fitsync.Decimal(".5"), fitsync.Decimal("01"), fitsync.Decimal("1."),
		fitsync.Decimal("1e"), fitsync.Decimal("-"), fitsync.Decimal(""), fitsync.Decimal("1e+"),
		fitsync.JSON([]byte("{bad")), fitsync.JSON([]byte("\"\xff\"")), fitsync.JSON(nil)} {
		for _, r := range []*fitsync.Record{
			{Namespace: "d.t", Op: fitsync.OpInsert, After: fitsync.Map(f("v", bad))},
			{Namespace: "d.t", Op: fitsync.OpDelete, Before: fitsync.Array(bad)},
		} {
			sameAsMaps(t, good, r, good)
		}
	}
	sameAsMaps(t, good, &fitsync.Record{Op: fitsync.OpDDL}, good)
	sameAsMaps(t, &fitsync.Record{Op: fitsync.Op(99)})
	sameAsMaps(t)

	// A big event is not kept in the pool, and written all the same.
	sameAsMaps(t, &fitsync.Record{Op: fitsync.OpInsert, After: fitsync.Map(f("big", fitsync.String(strings.Repeat("x", 100<<10))))})
}

func TestJSONNumber(t *testing.T) {
	for s, want := range map[string]bool{"0": true, "-0": true, "12.30": true, "1e5": true, "1E+5": true, "-1.5e-3": true, "123": true,
		"": false, "-": false, "01": false, "1.": false, ".5": false, "+1": false, "1e": false, "1e+": false, "1x": false, "0x1": false,
		"--1": false, "1.e5": false, "NaN": false} {
		if jsonNumber(s) != want {
			t.Errorf("%q: %v", s, !want)
		}
	}
}

// fuzzValue makes a value of data, returning the rest of it.
func fuzzValue(data []byte, depth int) (fitsync.Value, []byte) {
	if len(data) == 0 {
		return fitsync.Null(), nil
	}
	k, data := data[0], data[1:]
	take := func() string {
		if len(data) == 0 {
			return ""
		}
		n := int(data[0]) % 12
		data = data[1:]
		n = min(n, len(data))
		s := string(data[:n])
		data = data[n:]
		return s
	}
	num := func() uint64 {
		var u uint64
		for i := 0; i < 8 && len(data) > 0; i++ {
			u = u<<8 | uint64(data[0])
			data = data[1:]
		}
		return u
	}
	switch k % 14 {
	case 0:
		return fitsync.Null(), data
	case 1:
		return fitsync.Bool(k&0x80 != 0), data
	case 2:
		return fitsync.Int(int64(num())), data
	case 3:
		return fitsync.Uint(num()), data
	case 4:
		return fitsync.Float(math.Float64frombits(num())), data
	case 5:
		return fitsync.Decimal(take()), data
	case 6, 7:
		return fitsync.String(take()), data
	case 8:
		return fitsync.Bytes([]byte(take())), data
	case 9:
		return fitsync.Time(time.Unix(int64(num()%(1<<40))-1<<39, int64(k)*7919).In(time.FixedZone("", int(k)*60-3600))), data
	case 10:
		return fitsync.Date(1970+int(k), time.Month(k%12+1), int(k%28+1)), data
	case 11:
		return fitsync.JSON([]byte(take())), data
	case 12:
		if depth > 3 {
			return fitsync.Null(), data
		}
		var elems []fitsync.Value
		for n := int(k>>4) % 4; n > 0; n-- {
			var e fitsync.Value
			e, data = fuzzValue(data, depth+1)
			elems = append(elems, e)
		}
		return fitsync.Array(elems...), data
	}
	if depth > 3 {
		return fitsync.Null(), data
	}
	var fs []fitsync.Field
	for n := int(k>>4) % 4; n > 0; n-- {
		name := take()
		var v fitsync.Value
		v, data = fuzzValue(data, depth+1)
		fs = append(fs, f(name, v))
	}
	return fitsync.Map(fs...), data
}

func FuzzDebeziumEncode(f *testing.F) {
	f.Add([]byte{13, 0x30, 3, 'a', 'b', 'c', 2, 1, 2, 3, 4, 5, 6, 7, 8})
	f.Add([]byte{5, 3, '1', 'e', '5', 11, 6, '{', '"', '<', '"', ':', '1'})
	f.Add([]byte{12, 0x30, 4, 0x7f, 0xf8, 0, 0, 0, 0, 0, 1, 9, 9, 9, 8, 1, 0xff})
	f.Fuzz(func(t *testing.T, data []byte) {
		var op byte
		if len(data) > 0 {
			op, data = data[0], data[1:]
		}
		after, data := fuzzValue(data, 0)
		before, data := fuzzValue(data, 0)
		ns := string(data[:min(len(data), 8)])
		r := &fitsync.Record{Namespace: ns, Op: []fitsync.Op{fitsync.OpInsert, fitsync.OpUpdate, fitsync.OpDelete, fitsync.OpMessage, fitsync.OpDDL}[op%5],
			After: after, Before: before}
		if op&0x80 != 0 {
			r.CommitTime = time.UnixMilli(int64(op) * 1e9)
		}
		sameAsMaps(t, r)
	})
}

// benchRecord is a row as the benchmark's shop.items has one: an update
// of a few ints, strings, a decimal and a time.
func benchRecord() *fitsync.Record {
	row := func(qty int64) fitsync.Value {
		return fitsync.Map(f("id", fitsync.Int(123456)), f("sku", fitsync.String("sku-00123456")), f("name", fitsync.String("item number 123456")),
			f("qty", fitsync.Int(qty)), f("price", fitsync.Decimal("19.99")), f("updated_at", fitsync.Time(time.UnixMicro(1762650000123456).UTC())))
	}
	return &fitsync.Record{Namespace: "shop.items", Op: fitsync.OpUpdate, Key: fitsync.Map(f("id", fitsync.Int(123456))),
		Before: row(7), After: row(8), CommitTime: time.UnixMilli(1762650000123)}
}

func BenchmarkDebeziumEncode(b *testing.B) {
	recs := []*fitsync.Record{benchRecord()}
	b.Run("maps", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := mapsEncode(nil, recs); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("hand", func(b *testing.B) {
		c := &Debezium{}
		b.ReportAllocs()
		for b.Loop() {
			if _, err := c.Encode(nil, recs); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("hand-reused", func(b *testing.B) {
		c := &Debezium{}
		var buf []byte
		b.ReportAllocs()
		for b.Loop() {
			var err error
			if buf, err = c.Encode(buf[:0], recs); err != nil {
				b.Fatal(err)
			}
		}
	})
}
