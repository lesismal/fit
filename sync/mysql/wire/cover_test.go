package mysqlwire

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

var errWrite = errors.New("fake: write refused")

// failWrites is a connection whose writes fail after the first after.
type failWrites struct {
	net.Conn
	after int
}

func (f *failWrites) Write(b []byte) (int, error) {
	if f.after == 0 {
		return 0, errWrite
	}
	f.after--
	return f.Conn.Write(b)
}

// breakWrites makes c's writes fail after the first after.
func breakWrites(c *Conn, after int) { c.raw.Conn = &failWrites{Conn: c.raw.Conn, after: after} }

// TestCommandWritesFail: each command whose packet cannot be written
// breaks the connection with the write's error, and later calls fail.
func TestCommandWritesFail(t *testing.T) {
	serveIdle := func(f *fake) {
		f.login(fakeCaps | capDeprecateEOF)
		for {
			f.cmd()
		}
	}
	for name, call := range map[string]func(c *Conn) error{
		"ping":    func(c *Conn) error { return c.Ping(ctxT(t)) },
		"prepare": func(c *Conn) error { _, err := c.Prepare(ctxT(t), "SELECT 1"); return err },
		// A packet past one payload: its first header, then its data.
		"long header": func(c *Conn) error { _, err := c.Exec(ctxT(t), strings.Repeat("x", maxPayload)); return err },
	} {
		t.Run(name, func(t *testing.T) {
			c := dialT(t, Config{Addr: serve(t, serveIdle), User: "u"})
			breakWrites(c, 0)
			if err := call(c); !errors.Is(err, errWrite) {
				t.Fatal(err)
			}
			if c.Err() == nil || !errors.Is(c.Ping(ctxT(t)), errWrite) {
				t.Fatal(c.Err())
			}
			if err := c.StartBinlog(ctxT(t), ComBinlogDump, nil); !errors.Is(err, errWrite) {
				t.Fatal(err)
			}
		})
	}
	t.Run("long data", func(t *testing.T) {
		c := dialT(t, Config{Addr: serve(t, serveIdle), User: "u"})
		breakWrites(c, 1)
		if _, err := c.Exec(ctxT(t), strings.Repeat("x", maxPayload)); !errors.Is(err, errWrite) {
			t.Fatal(err)
		}
	})
}

// TestStatementWritesFail: an execute that cannot be written breaks the
// connection; the statements of a broken connection fail to execute and
// to close.
func TestStatementWritesFail(t *testing.T) {
	addr := serve(t, func(f *fake) {
		f.login(fakeCaps | capDeprecateEOF)
		f.cmd()
		f.prepareOK(1, 1, nil)
		f.cmd()
		f.prepareOK(2, 1, nil)
		for {
			f.cmd()
		}
	})
	c := dialT(t, Config{Addr: addr, User: "u"})
	a, err := c.Prepare(ctxT(t), "SELECT ?")
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.Prepare(ctxT(t), "SELECT ?")
	if err != nil {
		t.Fatal(err)
	}
	breakWrites(c, 0)
	if _, err := a.Exec(ctxT(t), 1); !errors.Is(err, errWrite) {
		t.Fatal(err)
	}
	if _, err := b.Exec(ctxT(t), 1); !errors.Is(err, errWrite) {
		t.Fatal(err)
	}
	if err := b.Close(ctxT(t)); !errors.Is(err, errWrite) {
		t.Fatal(err)
	}
}

// TestLargeParameters: an execute's parameters past keepBuffer go, and
// the buffer they took is not kept for the next one.
func TestLargeParameters(t *testing.T) {
	big := strings.Repeat("p", 2*keepBuffer)
	got := make(chan int, 2)
	addr := serve(t, func(f *fake) {
		f.login(fakeCaps | capDeprecateEOF)
		f.cmd()
		f.prepareOK(1, 1, nil)
		for range 2 {
			_, body := f.cmd()
			_, vals := f.execute(body, 1)
			b, _ := vals[0].([]byte)
			got <- len(b)
			f.ok(1, 0, StatusAutocommit)
		}
		for {
			f.cmd()
		}
	})
	c := dialT(t, Config{Addr: addr, User: "u"})
	st, err := c.Prepare(ctxT(t), "INSERT INTO t VALUES (?)")
	if err != nil {
		t.Fatal(err)
	}
	if res, err := st.Exec(ctxT(t), big); err != nil || res.AffectedRows != 1 {
		t.Fatal(res, err)
	}
	if n := <-got; n != len(big) {
		t.Fatal(n)
	}
	if c.pvals != nil || c.ptypes != nil {
		t.Fatalf("a buffer of %d kept", cap(c.pvals))
	}
	if _, err := st.Exec(ctxT(t), "small"); err != nil {
		t.Fatal(err)
	}
	if n := <-got; n != 5 || c.pvals == nil {
		t.Fatal(n)
	}
}

// TestQueryWithArgsCloseFails: the statement a query with arguments
// prepares is closed after its rows; a close that cannot be written is
// the rows' error.
func TestQueryWithArgsCloseFails(t *testing.T) {
	addr := serve(t, func(f *fake) {
		f.login(fakeCaps | capDeprecateEOF)
		f.cmd()
		f.prepareOK(1, 1, nil)
		f.cmd()
		f.ok(0, 0, StatusAutocommit)
		for {
			f.cmd()
		}
	})
	c := dialT(t, Config{Addr: addr, User: "u"})
	breakWrites(c, 2) // the prepare and the execute
	rows, err := c.Query(ctxT(t), "DO ?", 1)
	if err == nil {
		err = rows.Close()
	}
	if !errors.Is(err, errWrite) || c.Err() == nil {
		t.Fatal(err)
	}
}

// TestLoadDataLocalWriteFails: the empty file answering LOAD DATA LOCAL
// cannot be written.
func TestLoadDataLocalWriteFails(t *testing.T) {
	addr := serve(t, func(f *fake) {
		f.login(fakeCaps | capDeprecateEOF)
		f.cmd()
		f.write(append([]byte{0xfb}, "/etc/passwd"...))
		for {
			f.read()
		}
	})
	c := dialT(t, Config{Addr: addr, User: "u"})
	breakWrites(c, 1)
	_, err := c.Exec(ctxT(t), "LOAD DATA LOCAL INFILE '/etc/passwd' INTO TABLE t")
	if !errors.Is(err, errWrite) {
		t.Fatal(err)
	}
}

// TestInsertWritesFail: text inserts whose statement cannot be written,
// and prepared inserts whose prepare the server refuses.
func TestInsertWritesFail(t *testing.T) {
	var got []string
	c := dialT(t, Config{Addr: serve(t, insertFake(t, 1<<20, &got)), User: "u"})
	if _, err := c.InsertRowsText(ctxT(t), "INSERT INTO t (a, b) VALUES", [][]any{{1, "a"}}, ""); err != nil {
		t.Fatal(err)
	}
	breakWrites(c, 0) // max_allowed_packet is known by now
	if _, err := c.InsertRowsText(ctxT(t), "INSERT INTO t (a, b) VALUES", [][]any{{2, "b"}}, ""); !errors.Is(err, errWrite) {
		t.Fatal(err)
	}

	addr := serve(t, func(f *fake) {
		f.login(fakeCaps | capDeprecateEOF)
		f.cmd()
		f.columns([]Column{{Name: "m", Type: TypeLongLong}})
		f.textRow([]byte("64000000"))
		f.end(0)
		f.cmd()
		f.fail(1146, "42S02", "Table 'db.t' doesn't exist")
		for {
			f.cmd()
		}
	})
	c = dialT(t, Config{Addr: addr, User: "u"})
	_, err := c.InsertRows(ctxT(t), "INSERT INTO t (a) VALUES", [][]any{{json.RawMessage(`{"a":1}`)}}, "")
	var se *Error
	if !errors.As(err, &se) || se.Code != 1146 || c.Err() != nil {
		t.Fatal(err)
	}
	if n := rowSize([]any{json.RawMessage(`[1]`), "ab", []byte("c"), time.Duration(0), 7}); n != 5*11+3+2+1+4 {
		t.Fatal(n)
	}
}

// TestMoreResultsClosed: rows closed with results left read them all,
// OKs and result sets, and leave the connection usable.
func TestMoreResultsClosed(t *testing.T) {
	addr := serve(t, func(f *fake) {
		f.login(fakeCaps | capDeprecateEOF)
		f.cmd()
		f.ok(1, 0, StatusAutocommit|StatusMoreResultsExist)
		f.columns([]Column{{Name: "a", Type: TypeVarString}})
		f.textRow([]byte("x"))
		f.textRow([]byte("y"))
		f.end(StatusAutocommit | StatusMoreResultsExist)
		f.ok(2, 0, StatusAutocommit)
		f.cmd()
		f.ok(0, 0, StatusAutocommit)
		for {
			f.cmd()
		}
	})
	c := dialT(t, Config{Addr: addr, User: "u"})
	rows, err := c.Query(ctxT(t), "CALL p()")
	if err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Ping(ctxT(t)); err != nil {
		t.Fatal(err)
	}
}

// TestPacketReadsCut: a packet past one payload cut in its middle, and a
// packet whose bytes stop coming until the timeout.
func TestPacketReadsCut(t *testing.T) {
	addr := serve(t, func(f *fake) {
		f.login(fakeCaps)
		f.cmd()
		_, _ = f.nc.Write(append([]byte{0xff, 0xff, 0xff, 1}, make([]byte, 100)...))
	})
	c := dialT(t, Config{Addr: addr, User: "u"})
	if err := c.Ping(ctxT(t)); err == nil || !strings.Contains(err.Error(), "unexpected EOF") {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	addr = serve(t, func(f *fake) {
		f.login(fakeCaps)
		f.cmd()
		_, _ = f.nc.Write([]byte{100, 0, 0, 1, 0, 0})
		<-stop
	})
	c = dialT(t, Config{Addr: addr, User: "u", Timeout: 2 * time.Second}) // long enough for the login on a slow runner
	err := c.Ping(ctxT(t))
	close(stop)
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatal(err)
	}
}

// TestCancelledCallWrites: once a call's context is done, every read and
// write of the connection fails at once.
func TestCancelledCallWrites(t *testing.T) {
	addr := serve(t, func(f *fake) {
		f.login(fakeCaps)
		for {
			f.cmd()
		}
	})
	c := dialT(t, Config{Addr: addr, User: "u"})
	ctx, cancel := context.WithCancel(context.Background())
	if err := c.begin(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	for guard := time.Now(); ; time.Sleep(time.Millisecond) {
		c.raw.mu.Lock()
		done := c.raw.cancelled
		c.raw.mu.Unlock()
		if done {
			break
		}
		if time.Since(guard) > time.Minute {
			t.Fatal("the context's end did not reach the connection")
		}
	}
	if _, err := c.raw.Write([]byte{1, 0, 0, 0, comPing}); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatal(err)
	}
	c.end()
}

// TestLoginErrors: data sent past the greeting before TLS, a plugin the
// client cannot answer, and passwords too long for the server's RSA key.
func TestLoginErrors(t *testing.T) {
	t.Run("data after the greeting", func(t *testing.T) {
		addr := serve(t, func(f *fake) {
			// The greeting and a stray packet in one write.
			real := f.nc
			var buf bufWriter
			f.nc = &buf
			f.greet("8.4.3", fakeCaps|capSSL, pluginNative)
			f.write([]byte("stray"))
			f.nc = real
			_, _ = real.Write(buf.b)
			// The client may answer before it sees the stray packet: what
			// it sends is not checked, only read until it hangs up.
			_, _ = io.Copy(io.Discard, f.br)
		})
		_, err := Dial(ctxT(t), Config{Addr: addr, User: "u", TLS: clientTLS(t)})
		if !errors.Is(err, ErrMalformed) || !strings.Contains(err.Error(), "after the greeting") {
			t.Fatal(err)
		}
	})
	t.Run("sha256 without a key", func(t *testing.T) {
		addr := serve(t, func(f *fake) {
			f.greet("8.4.3", fakeCaps, pluginSHA256)
			f.read()
		})
		_, err := Dial(ctxT(t), Config{Addr: addr, User: "u", Password: "pw"})
		if err == nil || !strings.Contains(err.Error(), "needs ServerPublicKey") {
			t.Fatal(err)
		}
	})
	long := strings.Repeat("p", 300)
	t.Run("sha256 key retrieved", func(t *testing.T) {
		addr := serve(t, func(f *fake) {
			f.greet("8.4.3", fakeCaps, pluginSHA256)
			f.response()
			f.write(append([]byte{1}, rsaPEM(t)...))
			f.read()
		})
		_, err := Dial(ctxT(t), Config{Addr: addr, User: "u", Password: long, AllowPublicKeyRetrieval: true})
		if err == nil || !strings.Contains(err.Error(), "too long") {
			t.Fatal(err)
		}
	})
	t.Run("caching full authentication", func(t *testing.T) {
		addr := serve(t, func(f *fake) {
			f.greet("8.4.3", fakeCaps, pluginCachingSHA2)
			f.response()
			f.write([]byte{1, cachingFullAuth})
			f.read()
		})
		_, err := Dial(ctxT(t), Config{Addr: addr, User: "u", Password: long, ServerPublicKey: &testRSA(t).PublicKey})
		if err == nil || !strings.Contains(err.Error(), "too long") {
			t.Fatal(err)
		}
	})
}

// bufWriter is a net.Conn that keeps what is written.
type bufWriter struct {
	net.Conn
	b []byte
}

func (w *bufWriter) Write(p []byte) (int, error) {
	w.b = append(w.b, p...)
	return len(p), nil
}
