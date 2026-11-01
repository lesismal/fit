package bodycrypt

import (
	"bytes"
	"testing"
)

func TestNewEmptyPassword(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Fatal("an empty password made a coder")
	}
}

func TestSealOpen(t *testing.T) {
	cd, err := New("pw")
	if err != nil {
		t.Fatal(err)
	}
	header := []byte("hdr")
	plain := []byte("hello body")
	buf := make([]byte, NonceSize+len(plain), Overhead+len(plain))
	copy(buf[NonceSize:], plain)
	sealed := cd.SealInPlace(buf, header)
	if len(sealed) != Overhead+len(plain) {
		t.Fatalf("sealed length %d", len(sealed))
	}
	got, err := cd.OpenInPlace(append([]byte(nil), sealed...), header)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("%q, %v", got, err)
	}
	if _, err := cd.OpenInPlace(append([]byte(nil), sealed...), []byte("other")); err != ErrOpen {
		t.Fatalf("another header: %v", err)
	}
	other, _ := New("other")
	if _, err := other.OpenInPlace(append([]byte(nil), sealed...), header); err != ErrOpen {
		t.Fatalf("another password: %v", err)
	}
	if _, err := cd.OpenInPlace(sealed[:Overhead-1], header); err != ErrOpen {
		t.Fatalf("a short body: %v", err)
	}
}
