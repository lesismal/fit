package bodycrypt

import (
	"bytes"
	"testing"
)

func key(b byte) []byte { return bytes.Repeat([]byte{b}, KeySize) }

// seal seals plain with d, as package link does.
func seal(t *testing.T, d *Direction, plain, header []byte) []byte {
	t.Helper()
	buf := make([]byte, len(plain), len(plain)+Overhead)
	copy(buf, plain)
	out, err := d.SealInPlace(buf, header)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Frames open in order, under their direction's key and header alone: a
// replayed, reordered or reflected frame does not open.
func TestSession(t *testing.T) {
	if _, err := NewDirection(key(1)[:16]); err == nil {
		t.Fatal("a short key")
	}
	if _, err := NewSession(key(1), nil); err == nil {
		t.Fatal("no receiving key")
	}
	a, err := NewSession(key(1), key(2))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewSession(key(2), key(1))
	header := []byte("hdr")
	f0 := seal(t, a.Send, []byte("zero"), header)
	f1 := seal(t, a.Send, []byte("one"), header)
	if len(f0) != len("zero")+Overhead {
		t.Fatalf("sealed length %d", len(f0))
	}
	// Reordered: frame 1 first.
	if _, err := b.Recv.OpenInPlace(bytes.Clone(f1), header); err != ErrOpen {
		t.Fatalf("a reordered frame: %v", err)
	}
	b, _ = NewSession(key(2), key(1))
	if got, err := b.Recv.OpenInPlace(bytes.Clone(f0), header); err != nil || string(got) != "zero" {
		t.Fatalf("%q, %v", got, err)
	}
	// Replayed: frame 0 again.
	if _, err := b.Recv.OpenInPlace(bytes.Clone(f0), header); err != ErrOpen {
		t.Fatalf("a replayed frame: %v", err)
	}
	// Reflected: a's own frame back to a.
	r := seal(t, a.Send, []byte("two"), header)
	if _, err := a.Recv.OpenInPlace(bytes.Clone(r), header); err != ErrOpen {
		t.Fatalf("a reflected frame: %v", err)
	}
	// Another header, or a short body.
	c, _ := NewSession(key(2), key(1))
	if _, err := c.Recv.OpenInPlace(bytes.Clone(f0), []byte("other")); err != ErrOpen {
		t.Fatalf("another header: %v", err)
	}
	if _, err := c.Recv.OpenInPlace(f0[:Overhead-1], header); err != ErrOpen {
		t.Fatalf("a short body: %v", err)
	}
	// The counter's end.
	d, _ := NewDirection(key(3))
	d.n = ^uint64(0)
	if _, err := d.SealInPlace(make([]byte, 0, Overhead), nil); err == nil {
		t.Fatal("sealed past the counter's end")
	}
	if _, err := d.OpenInPlace(make([]byte, Overhead), nil); err == nil {
		t.Fatal("opened past the counter's end")
	}
}
