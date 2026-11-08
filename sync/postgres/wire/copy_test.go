package pgwire

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

// readCopy reads CopyData up to CopyDone or CopyFail, returning the data and
// the closing message's type.
func (f *fakeConn) readCopy() ([]byte, byte) {
	var data []byte
	for {
		typ, b := f.read()
		switch typ {
		case 'd':
			data = append(data, b...)
		case 'S', 'H': // Flush or Sync between, as libpq may send
		default:
			return data, typ
		}
	}
}

func TestCopyIn(t *testing.T) {
	big := bytes.Repeat([]byte("0123456789abcdef"), 10_000) // past a flush
	got := make(chan []byte, 3)
	c := session(t, func(f *fakeConn) {
		// A copy of rows written whole, and of a big write.
		f.read()
		f.rowDesc("ignored", OIDText) // skipped before CopyInResponse
		f.send('G', []byte{0}, u16(0))
		data, end := f.readCopy()
		if end != 'c' {
			f.t.Errorf("end %c", end)
		}
		got <- data
		f.send('N', cs("SNOTICE"), cs("Mfine"), []byte{0})
		f.send('C', cs("COPY 3"))
		f.z()
		// One aborted, after data was written.
		f.read()
		f.send('G', []byte{0}, u16(0))
		data, end = f.readCopy()
		if end != 'f' {
			f.t.Errorf("end %c", end)
		}
		got <- data
		f.errResp("57014", "COPY from stdin failed: no")
		f.z()
		// One the server refuses at its end.
		f.read()
		f.send('G', []byte{0}, u16(0))
		f.readCopy()
		f.errResp("22P02", "invalid input syntax")
		f.z()
		// A statement that is not a COPY, and one that fails.
		f.read()
		f.send('C', cs("SELECT 0"))
		f.z()
		f.read()
		f.errResp("42P01", "no such table")
		f.z()
		// CopyFrom of a reader.
		f.read()
		f.send('G', []byte{0}, u16(0))
		data, _ = f.readCopy()
		got <- data
		f.send('C', cs("COPY 2"))
		f.z()
		// CopyFrom of a reader that fails: aborted.
		f.read()
		f.send('G', []byte{0}, u16(0))
		f.readCopy()
		f.errResp("57014", "COPY from stdin failed")
		f.z()
		// A copy ended by an unexpected answer.
		f.read()
		f.send('G', []byte{0}, u16(0))
		f.readCopy()
		f.send('D')
		f.until('X')
	})
	w, err := c.CopyIn(ctx(t), "COPY t FROM STDIN")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteRow(1, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(big); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteRow(struct{}{}); err == nil {
		t.Fatal("a struct was written")
	}
	w.WriteRow(nil, "b")
	if _, err := c.SimpleQuery(ctx(t), "x"); !errors.Is(err, ErrBusy) {
		t.Fatalf("%v", err)
	}
	n, err := w.Close()
	if err != nil || n != 3 {
		t.Fatalf("%d %v", n, err)
	}
	if want := append(append([]byte("1\ta\n"), big...), "\\N\tb\n"...); !bytes.Equal(<-got, want) {
		t.Fatal("the data differs")
	}
	if _, err := w.Write([]byte("x")); err == nil {
		t.Fatal("a write after the end")
	}
	if _, err := w.Close(); err == nil {
		t.Fatal("a second close")
	}
	if w.Abort("late") != nil {
		t.Fatal("an abort after the end")
	}

	w, err = c.CopyIn(ctx(t), "COPY t FROM STDIN")
	if err != nil {
		t.Fatal(err)
	}
	w.WriteRow(2)
	if err := w.Abort("no"); err != nil {
		t.Fatal(err)
	}
	if data := <-got; len(data) != 0 {
		t.Fatalf("aborted data sent: %q", data)
	}

	w, _ = c.CopyIn(ctx(t), "COPY t FROM STDIN")
	var pe *Error
	if _, err := w.Close(); !errors.As(err, &pe) || pe.Code != "22P02" {
		t.Fatalf("%v", err)
	}
	if _, err := c.CopyIn(ctx(t), "SELECT 0"); err == nil || !strings.Contains(err.Error(), "not a COPY") {
		t.Fatalf("%v", err)
	}
	if _, err := c.CopyIn(ctx(t), "COPY x FROM STDIN"); !errors.As(err, &pe) || pe.Code != "42P01" {
		t.Fatalf("%v", err)
	}
	if n, err := c.CopyFrom(ctx(t), "COPY t FROM STDIN", strings.NewReader("1\n2\n")); err != nil || n != 2 || string(<-got) != "1\n2\n" {
		t.Fatalf("%d %v", n, err)
	}
	if _, err := c.CopyFrom(ctx(t), "COPY t FROM STDIN", io.MultiReader(strings.NewReader("1\n"), errReader{})); err == nil || c.Err() != nil {
		t.Fatalf("%v %v", err, c.Err())
	}
	w, _ = c.CopyIn(ctx(t), "COPY t FROM STDIN")
	if _, err := w.Close(); err == nil || c.Err() == nil {
		t.Fatalf("%v", err)
	}
	if _, err := c.CopyFrom(ctx(t), "COPY t FROM STDIN", strings.NewReader("")); err == nil {
		t.Fatal("a copy on a broken connection")
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("the source failed") }

func TestCopyInBroken(t *testing.T) {
	// The server goes away during the copy: writes, Close and Abort fail.
	for _, end := range []string{"write", "close", "abort", "copyfrom"} {
		t.Run(end, func(t *testing.T) {
			c := session(t, func(f *fakeConn) {
				f.read()
				f.send('G', []byte{0}, u16(0))
				f.nc.Close()
			})
			w, err := c.CopyIn(ctx(t), "COPY t FROM STDIN")
			if err != nil {
				t.Fatal(err)
			}
			c.nc.(interface{ CloseWrite() error }).CloseWrite()
			switch end {
			case "write":
				_, err = w.Write(make([]byte, copyFlushBytes))
			case "close":
				_, err = w.Close()
			case "abort":
				err = w.Abort("x")
			case "copyfrom":
				w.finish(nil)
				c.busy = false
				_, err = c.CopyFrom(ctx(t), "COPY", strings.NewReader("x"))
			}
			if err == nil {
				t.Fatal("no error")
			}
		})
	}
	// The source fails and the connection is gone: no abort is tried.
	c := session(t, func(f *fakeConn) {
		f.read()
		f.send('G', []byte{0}, u16(0))
		f.until('X')
	})
	if _, err := c.CopyFrom(ctx(t), "COPY", readerThen{func() { c.nc.Close() }}); err == nil {
		t.Fatal("no error")
	}
}

type readerThen struct{ fn func() }

func (r readerThen) Read(p []byte) (int, error) {
	r.fn()
	return 0, errors.New("the source failed")
}

func TestCopyTo(t *testing.T) {
	c := session(t, func(f *fakeConn) {
		f.read()
		f.send('H', []byte{0}, u16(0))
		f.send('d', []byte("1\n"))
		f.send('d', []byte("2\n"))
		f.send('c')
		f.send('C', cs("COPY 2"))
		f.z()
		// Not a COPY TO: rows are skipped, a CopyIn refused.
		f.read()
		f.rowDesc("a", OIDText)
		f.dataRow([]byte("x"))
		f.send('I')
		f.send('G', []byte{0}, u16(0))
		f.read()
		f.errResp("57014", "COPY from stdin failed")
		f.z()
		// The writer fails.
		f.read()
		f.send('H', []byte{0}, u16(0))
		f.send('d', []byte("1\n"))
		f.until('X')
	})
	var out bytes.Buffer
	if n, err := c.CopyTo(ctx(t), "COPY t TO STDOUT", &out); err != nil || n != 2 || out.String() != "1\n2\n" {
		t.Fatalf("%d %v %q", n, err, out.String())
	}
	var pe *Error
	if _, err := c.CopyTo(ctx(t), "COPY t FROM STDIN", &out); !errors.As(err, &pe) {
		t.Fatalf("%v", err)
	}
	if _, err := c.CopyTo(ctx(t), "COPY t TO STDOUT", failWriter{}); err == nil || c.Err() == nil {
		t.Fatalf("%v", err)
	}
	if _, err := c.CopyTo(ctx(t), "x", &out); err == nil {
		t.Fatal("a copy on a broken connection")
	}
	c = session(t, func(f *fakeConn) {
		f.read()
		f.send('?')
		f.until('X')
	})
	if _, err := c.CopyTo(ctx(t), "x", &out); err == nil || !strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("%v", err)
	}
	c = session(t, func(f *fakeConn) {
		f.read()
		f.send('G', []byte{0}, u16(0))
		f.nc.Close()
	})
	c.nc.(interface{ CloseWrite() error }).CloseWrite()
	if _, err := c.CopyTo(ctx(t), "x", &out); err == nil {
		t.Fatal("no error")
	}
	c = session(t, func(f *fakeConn) {
		f.read()
		f.send('?')
		f.until('X')
	})
	if _, err := c.CopyIn(ctx(t), "x"); err == nil || !strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("%v", err)
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("the disk is full") }
