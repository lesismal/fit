package pb

import (
	"bytes"
	"strings"
	"testing"
)

func TestAppendAndRead(t *testing.T) {
	var b []byte
	b = AppendUint(b, 1, 0) // nothing
	b = AppendUint(b, 1, 300)
	b = AppendInt(b, 2, -1)
	b = AppendBool(b, 3, false)
	b = AppendBool(b, 3, true)
	b = AppendFixed64(b, 4, 0)
	b = AppendDouble(b, 4, 1.5)
	b = AppendString(b, 5, "")
	b = AppendString(b, 5, "hi")
	b = AppendBytes(b, 6, nil)
	long := strings.Repeat("x", 200)
	b = AppendMessage(b, 7, func(b []byte) []byte { return AppendString(b, 1, long) })
	b = AppendMessage(b, 8, func(b []byte) []byte { return AppendString(b, 1, "s") })
	b = append(AppendTag(b, 9, Fixed32), 1, 0, 0, 0)

	type got struct {
		num, wire int
		v         uint64
		p         string
	}
	var fs []got
	for len(b) > 0 {
		f, rest, err := Next(b)
		if err != nil {
			t.Fatal(err)
		}
		fs = append(fs, got{f.Num, f.Wire, f.Value, string(f.Bytes)})
		b = rest
	}
	want := []got{{1, Varint, 300, ""}, {2, Varint, 1<<64 - 1, ""}, {3, Varint, 1, ""}, {4, Fixed64, 0x3ff8000000000000, ""},
		{5, Bytes, 0, "hi"}, {6, Bytes, 0, ""}, {7, Bytes, 0, string(AppendString(nil, 1, long))}, {8, Bytes, 0, "\x0a\x01s"}, {9, Fixed32, 1, ""}}
	if len(fs) != len(want) {
		t.Fatalf("%v", fs)
	}
	for i := range want {
		if fs[i] != want[i] {
			t.Errorf("field %d: %+v, want %+v", i, fs[i], want[i])
		}
	}
	// A body of 16384 bytes and more takes a length of three bytes.
	big := AppendMessage([]byte{0xff}, 1, func(b []byte) []byte { return append(b, bytes.Repeat([]byte{7}, 20000)...) })
	f, rest, err := Next(big[1:])
	if err != nil || len(rest) != 0 || len(f.Bytes) != 20000 || f.Bytes[0] != 7 || f.Bytes[19999] != 7 {
		t.Fatalf("%v %d %d", err, len(rest), len(f.Bytes))
	}
}

func TestMalformed(t *testing.T) {
	for _, b := range [][]byte{
		{0x80},       // a tag cut short
		{0x08, 0x80}, // a varint cut short
		{0x09, 1, 2}, // a fixed64 cut short
		{0x0d, 1},    // a fixed32 cut short
		{0x0a, 5, 1}, // bytes cut short
		{0x0a, 0x80}, // a length cut short
		{0x0b},       // a group, which is not read
	} {
		if _, _, err := Next(b); err != ErrMalformed {
			t.Errorf("% x: %v", b, err)
		}
	}
}
