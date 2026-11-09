package pgwire

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"strings"
	"testing"
)

// The server goes away after reading a request, before answering: each call
// fails with the connection's error and leaves the connection broken.
func TestServerGoneMidCall(t *testing.T) {
	for _, c := range []struct {
		name   string
		script func(f *fakeConn)
		call   func(c *Conn) error
	}{
		{"copy in", func(f *fakeConn) { f.read() }, func(c *Conn) error {
			_, err := c.CopyIn(ctx(t), "COPY t FROM STDIN")
			return err
		}},
		{"copy in close", func(f *fakeConn) {
			f.read()
			f.send('G', []byte{0}, u16(0))
			f.readCopy()
		}, func(c *Conn) error {
			w, err := c.CopyIn(ctx(t), "COPY t FROM STDIN")
			if err != nil {
				return errors.New("unexpected: " + err.Error())
			}
			if err := w.WriteRow("a", 1); err != nil {
				return errors.New("unexpected: " + err.Error())
			}
			_, err = w.Close()
			return err
		}},
		{"copy to", func(f *fakeConn) {
			f.read()
			f.send('H', []byte{0}, u16(0))
		}, func(c *Conn) error {
			_, err := c.CopyTo(ctx(t), "COPY t TO STDOUT", io.Discard)
			return err
		}},
		{"prepare", func(f *fakeConn) { f.until('S') }, func(c *Conn) error {
			_, err := c.Prepare(ctx(t), "s", "SELECT 1")
			return err
		}},
		{"query", func(f *fakeConn) {
			f.prepared(nil, "a", OIDText)
			f.until('S')
		}, func(c *Conn) error {
			st, err := c.Prepare(ctx(t), "s", "SELECT 1")
			if err != nil {
				return errors.New("unexpected: " + err.Error())
			}
			_, err = st.Query(ctx(t))
			return err
		}},
		{"error then gone", func(f *fakeConn) {
			f.read()
			f.errResp("42P01", "no such table")
		}, func(c *Conn) error {
			_, err := c.CopyIn(ctx(t), "COPY t FROM STDIN")
			return err
		}},
		{"short body", func(f *fakeConn) {
			f.read()
			f.nc.Write([]byte{'T', 0, 0, 0, 40}) // the header, no body
		}, func(c *Conn) error {
			_, err := c.SimpleQuery(ctx(t), "SELECT 1")
			return err
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			conn := session(t, c.script)
			err := c.call(conn)
			if err == nil || strings.HasPrefix(err.Error(), "unexpected: ") {
				t.Fatalf("err %v", err)
			}
			var pe *Error
			if errors.As(err, &pe) {
				// A server error before the connection went: the drain
				// reading to ReadyForQuery is what ended it.
				if conn.Err() == nil {
					t.Fatal("the connection is not broken")
				}
				return
			}
			if conn.Err() == nil {
				t.Fatalf("err %v, the connection is not broken", err)
			}
			if c.name == "short body" && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("err %v, want an unexpected EOF", err)
			}
		})
	}
}

func TestStartupGoneAfterAuth(t *testing.T) {
	addr := fakeServer(t, func(f *fakeConn) {
		f.startup()
		f.send('R', u32(0))
	})
	if _, err := Connect(ctx(t), Config{Addr: addr, User: "u", SSLMode: SSLDisable}); err == nil || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err %v", err)
	}
}

func TestReplicationGone(t *testing.T) {
	// START_REPLICATION read, then nothing.
	c := replSession(t, "16.4", func(f *fakeConn) { f.read() })
	if err := c.StartReplication(ctx(t), "s", 0); err == nil || c.Err() == nil {
		t.Fatalf("start: err %v, conn %v", err, c.Err())
	}

	// A context over before the command: refused, the connection kept.
	c = replSession(t, "16.4", func(f *fakeConn) { f.until('X') })
	cx, cancel0 := context.WithCancel(ctx(t))
	cancel0()
	if err := c.StartReplication(cx, "s", 0); !errors.Is(err, context.Canceled) || c.Err() != nil {
		t.Fatalf("start canceled: err %v, conn %v", err, c.Err())
	}

	// In the stream, the server goes after the CopyDone.
	c = replSession(t, "16.4", func(f *fakeConn) {
		f.read()
		f.send('W', []byte{0}, u16(0))
		f.until('c')
	})
	if err := c.StartReplication(ctx(t), "s", 0); err != nil {
		t.Fatal(err)
	}
	if err := c.EndReplication(ctx(t)); err == nil || c.Err() == nil {
		t.Fatalf("end: err %v, conn %v", err, c.Err())
	}

	// A context that is over refuses the receive without touching the
	// stream.
	c = replSession(t, "16.4", func(f *fakeConn) {
		f.read()
		f.send('W', []byte{0}, u16(0))
		f.until('X')
	})
	if err := c.StartReplication(ctx(t), "s", 0); err != nil {
		t.Fatal(err)
	}
	cx, cancel := context.WithCancel(ctx(t))
	cancel()
	if _, err := c.ReceiveReplication(cx); err == nil || c.Err() != nil {
		t.Fatalf("receive: err %v, conn %v", err, c.Err())
	}
}

func TestSCRAMOnlyPlusUnbindable(t *testing.T) {
	// The server offers only -PLUS over TLS with a certificate whose hash
	// cannot bind: with binding preferred, nothing is left to use.
	edCert, edRoots, _ := testCert(t, true)
	addr := fakeServerOpts(t, fakeOpts{cert: edCert}, func(f *fakeConn) {
		f.startup()
		f.scram("secret", []string{"SCRAM-SHA-256-PLUS"}, nil)
	})
	_, err := Connect(ctx(t), Config{Addr: addr, User: "u", Password: "secret", SSLMode: SSLVerifyFull,
		TLS: &tls.Config{RootCAs: edRoots}})
	if err == nil || !strings.Contains(err.Error(), "no SASL mechanism") {
		t.Fatalf("err %v", err)
	}
}

func TestMalformedValues(t *testing.T) {
	// A numeric whose digits do not match its count.
	if _, err := decodeNumeric(append(numeric(0, 0, 0, 1, 2), 0)); err == nil {
		t.Error("a numeric of a bad length decoded")
	}
	// Dimensions whose product passes the bytes there are.
	b := append(u32(2), u32(0)...)
	b = append(b, u32(OIDInt4)...)
	b = append(append(append(b, u32(6)...), u32(1)...), u32(6)...)
	b = append(b, u32(1)...)
	if _, err := decodeBinary(1007, b); err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Errorf("err %v", err)
	}
	// Elements cut short of their length words.
	b = append(u32(1), u32(0)...)
	b = append(b, u32(OIDInt4)...)
	b = append(append(b, u32(3)...), u32(1)...)
	b = append(append(b, u32(4)...), u32(9)...)
	if _, err := decodeBinary(1007, b); err == nil || !strings.Contains(err.Error(), "short") {
		t.Errorf("err %v", err)
	}
	// An element that is a pointer to a nil pointer is NULL.
	var nilp *int
	one := 1
	onep := &one
	got, notNull, err := appendParam(nil, 0, []**int{&nilp, &onep})
	if err != nil || !notNull || !bytes.Equal(got, []byte(`{NULL,"1"}`)) {
		t.Errorf("%q %v %v", got, notNull, err)
	}
}

func TestCopyRowCarriageReturn(t *testing.T) {
	got, err := AppendCopyRow(nil, "a\rb", nil)
	if err != nil || string(got) != "a\\rb\t\\N\n" {
		t.Fatalf("%q %v", got, err)
	}
}
