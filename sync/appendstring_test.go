package fitsync

import (
	"bytes"
	"encoding/json"
	"testing"
)

// appendString writes what json.Marshal does, on its fast path for plain
// ASCII and off it.
func TestAppendString(t *testing.T) {
	for _, s := range []string{"", "shop.items", "a b ~", "q\"", `b\s`, "<", ">", "&", "\n", "\x7f", "é", " ", "\xff"} {
		want, _ := json.Marshal(s)
		if got := AppendJSONString([]byte("x"), s); !bytes.Equal(got, append([]byte("x"), want...)) {
			t.Errorf("%q: %s, want %s", s, got, want)
		}
	}
}

func BenchmarkAppendString(b *testing.B) {
	buf := make([]byte, 0, 64)
	b.ReportAllocs()
	for b.Loop() {
		buf = appendString(buf[:0], "shop.items")
	}
}
