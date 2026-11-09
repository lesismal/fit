package kafkawire

import (
	"bytes"
	"testing"
)

func TestLZ4Block(t *testing.T) {
	src := bytes.Repeat([]byte("pulsar block "), 10000)
	b := LZ4Block(nil, src)
	if len(b) >= len(src) {
		t.Fatalf("%d bytes of %d", len(b), len(src))
	}
	out, err := LZ4Unblock(nil, b, len(src))
	if err != nil || !bytes.Equal(out, src) {
		t.Fatalf("%v", err)
	}
	if _, err := LZ4Unblock(nil, b, len(src)-1); err == nil {
		t.Fatal("no limit")
	}
}
