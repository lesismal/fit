package bodycrypt

import (
	"bytes"
	"errors"
	"testing"
)

func TestNewEmptyPassword(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Fatal("an empty password made a coder")
	}
}

// sessions is the two sides of one link whose coders are of passwords pa
// and pb.
func sessions(t *testing.T, pa, pb string) (*Session, *Session) {
	t.Helper()
	ca, err := New(pa)
	if err != nil {
		t.Fatal(err)
	}
	cb, err := New(pb)
	if err != nil {
		t.Fatal(err)
	}
	ra, rb := Random(), Random()
	a, err := ca.Session(ra, rb)
	if err != nil {
		t.Fatal(err)
	}
	b, err := cb.Session(rb, ra)
	if err != nil {
		t.Fatal(err)
	}
	return a, b
}

func seal(s *Session, plain, header []byte) []byte {
	buf := make([]byte, len(plain), len(plain)+Overhead)
	copy(buf, plain)
	return s.SealInPlace(buf, header)
}

func open(s *Session, sealed, header []byte) ([]byte, error) {
	return s.OpenInPlace(append([]byte(nil), sealed...), header)
}

func TestSealOpen(t *testing.T) {
	a, b := sessions(t, "pw", "pw")
	header := []byte("hdr")
	for i, plain := range [][]byte{[]byte("hello body"), nil, bytes.Repeat([]byte("x"), 1<<20)} {
		sealed := seal(a, plain, header)
		if len(sealed) != Overhead+len(plain) {
			t.Fatalf("%d: sealed length %d", i, len(sealed))
		}
		if got, err := open(b, sealed, header); err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("%d: %d bytes, %v", i, len(got), err)
		}
		reply := seal(b, plain, header)
		if got, err := open(a, reply, header); err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("%d: reply %d bytes, %v", i, len(got), err)
		}
	}
}

// What does not open under the next number fails, and leaves the number
// where it was.
func TestOpenRefuses(t *testing.T) {
	header := []byte("hdr")
	t.Run("replayed", func(t *testing.T) {
		a, b := sessions(t, "pw", "pw")
		f := seal(a, []byte("SET k v"), header)
		if _, err := open(b, f, header); err != nil {
			t.Fatal(err)
		}
		if _, err := open(b, f, header); err != ErrOpen {
			t.Fatalf("replayed: %v", err)
		}
	})
	t.Run("reordered", func(t *testing.T) {
		a, b := sessions(t, "pw", "pw")
		f0, f1 := seal(a, []byte("0"), header), seal(a, []byte("1"), header)
		if _, err := open(b, f1, header); err != ErrOpen {
			t.Fatalf("second first: %v", err)
		}
		if _, err := open(b, f0, header); err != nil {
			t.Fatalf("the first after a failure: %v", err)
		}
	})
	t.Run("reflected", func(t *testing.T) {
		a, _ := sessions(t, "pw", "pw")
		if _, err := open(a, seal(a, []byte("x"), header), header); err != ErrOpen {
			t.Fatalf("reflected: %v", err)
		}
	})
	t.Run("another link", func(t *testing.T) {
		a, _ := sessions(t, "pw", "pw")
		_, b2 := sessions(t, "pw", "pw")
		if _, err := open(b2, seal(a, []byte("x"), header), header); err != ErrOpen {
			t.Fatalf("another link: %v", err)
		}
	})
	t.Run("another password", func(t *testing.T) {
		a, b := sessions(t, "pw", "other")
		if _, err := open(b, seal(a, []byte("x"), header), header); err != ErrOpen {
			t.Fatalf("another password: %v", err)
		}
	})
	t.Run("another header", func(t *testing.T) {
		a, b := sessions(t, "pw", "pw")
		if _, err := open(b, seal(a, []byte("x"), header), []byte("other")); err != ErrOpen {
			t.Fatalf("another header: %v", err)
		}
	})
	t.Run("short", func(t *testing.T) {
		_, b := sessions(t, "pw", "pw")
		if _, err := b.OpenInPlace(make([]byte, Overhead-1), header); err != ErrOpen {
			t.Fatalf("a short body: %v", err)
		}
	})
}

func TestSessionHellos(t *testing.T) {
	cd, err := New("pw")
	if err != nil {
		t.Fatal(err)
	}
	r := Random()
	if _, err := cd.Session(r, r); !errors.Is(err, ErrReflected) {
		t.Fatalf("own hello: %v", err)
	}
	if _, err := cd.Session(r, r[:RandomSize-1]); err == nil {
		t.Fatal("a short hello made a session")
	}
	if bytes.Equal(Random(), Random()) {
		t.Fatal("two hellos alike")
	}
}
