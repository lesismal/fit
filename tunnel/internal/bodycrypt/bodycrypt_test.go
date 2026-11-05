package bodycrypt

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"os/exec"
	"strings"
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
	a, err := ca.Dialer().Session(ra, rb)
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
	// An attacker on the path of two links gives each the other's random
	// bytes the other way round, so that both have the same two: a frame
	// one dialer sealed still does not open at the other dialer, nor one
	// a listener sealed at the other listener.
	t.Run("crossed links", func(t *testing.T) {
		cd, err := New("pw")
		if err != nil {
			t.Fatal(err)
		}
		x, y := Random(), Random()
		d1, _ := cd.Dialer().Session(x, y)
		d2, _ := cd.Dialer().Session(y, x)
		l1, _ := cd.Session(y, x)
		l2, _ := cd.Session(x, y)
		if _, err := open(d2, seal(d1, []byte("x"), header), header); err != ErrOpen {
			t.Fatalf("a dialer's frame at another dialer: %v", err)
		}
		if _, err := open(l2, seal(l1, []byte("x"), header), header); err != ErrOpen {
			t.Fatalf("a listener's frame at another listener: %v", err)
		}
		// Each link's own two sides still agree.
		d1, _ = cd.Dialer().Session(x, y)
		d2, _ = cd.Dialer().Session(y, x)
		l1, _ = cd.Session(y, x)
		l2, _ = cd.Session(x, y)
		if got, err := open(l1, seal(d1, []byte("x"), header), header); err != nil || string(got) != "x" {
			t.Fatalf("d1 to l1: %q, %v", got, err)
		}
		if got, err := open(d2, seal(l2, []byte("y"), header), header); err != nil || string(got) != "y" {
			t.Fatalf("l2 to d2: %q, %v", got, err)
		}
	})
	t.Run("both sides listeners", func(t *testing.T) {
		cd, err := New("pw")
		if err != nil {
			t.Fatal(err)
		}
		x, y := Random(), Random()
		a, _ := cd.Session(x, y)
		b, _ := cd.Session(y, x)
		if _, err := open(b, seal(a, []byte("x"), header), header); err != ErrOpen {
			t.Fatalf("two listeners: %v", err)
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

// TestSessionFIPSOnly runs itself again in FIPS 140-only mode, where GCM
// under nonces it did not make is refused: the coder still derives, and the
// session fails with GCM's error rather than making a link it cannot seal.
func TestSessionFIPSOnly(t *testing.T) {
	if os.Getenv("BODYCRYPT_FIPS_CHILD") == "1" {
		cd, err := New("pw")
		if err != nil {
			t.Fatalf("New in FIPS 140-only mode: %v", err)
		}
		s, err := cd.Session(Random(), Random())
		if err == nil || s != nil || !strings.Contains(err.Error(), "FIPS 140-only") {
			t.Fatalf("Session in FIPS 140-only mode: %v, %v", s, err)
		}
		return
	}
	args := []string{"-test.run=^TestSessionFIPSOnly$", "-test.count=1"}
	if f := flag.Lookup("test.gocoverdir"); f != nil && f.Value.String() != "" {
		// What the run covers is merged into this one's profile.
		args = append(args, "-test.gocoverdir="+f.Value.String())
	}
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), "BODYCRYPT_FIPS_CHILD=1", "GODEBUG=fips140=only")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the FIPS 140-only run failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "PASS") {
		t.Fatalf("the FIPS 140-only run did not pass:\n%s", out)
	}
}
