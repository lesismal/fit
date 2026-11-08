package pgwire

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSimpleQueryMessages(t *testing.T) {
	var notices []string
	addr := fakeServer(t, func(f *fakeConn) {
		f.startup()
		f.ready()
		f.read()
		f.send('N', cs("SNOTICE"), cs("C00000"), cs("Mhello"), []byte{0})
		f.send('A', u32(1), cs("chan"), cs("payload"))
		f.send('S', cs("TimeZone"), cs("UTC"))
		f.rowDesc("a", OIDText, "b", OIDInt4)
		f.dataRow([]byte("x"), nil)
		f.send('C', cs("SELECT 1"))
		f.send('I')
		f.send('C', cs("CREATE TABLE"))
		f.send('H', []byte{0}, u16(0))
		f.send('d', []byte("data"))
		f.send('c')
		f.send('C', cs("COPY 1"))
		f.z()
		// A COPY FROM STDIN through SimpleQuery is refused with CopyFail.
		f.read()
		f.send('G', []byte{0}, u16(0))
		if typ, b := f.read(); typ != 'f' || !strings.Contains(string(b), "CopyIn") {
			f.t.Errorf("%c %q", typ, b)
		}
		f.errResp("57014", "COPY from stdin failed")
		f.z()
		// An empty query.
		f.read()
		f.send('I')
		f.z()
		f.until('X')
	})
	c, err := Connect(ctx(t), Config{Addr: addr, User: "u", SSLMode: SSLDisable, OnNotice: func(e *Error) { notices = append(notices, e.Message) }})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	res, err := c.SimpleQuery(ctx(t), "many")
	if err != nil || len(res) != 3 || res[0].Tag != "SELECT 1" || string(res[0].Rows[0][0]) != "x" || res[0].Rows[0][1] != nil ||
		res[1].Tag != "CREATE TABLE" || res[2].Tag != "COPY 1" || res[0].Fields[1].TypeOID != OIDInt4 {
		t.Fatalf("%v %+v", err, res)
	}
	if len(notices) != 1 || notices[0] != "hello" || c.ParameterStatus("TimeZone") != "UTC" {
		t.Fatalf("notices %v", notices)
	}
	var pe *Error
	if _, err := c.SimpleQuery(ctx(t), "COPY t FROM STDIN"); !errors.As(err, &pe) || pe.Code != "57014" {
		t.Fatalf("%v", err)
	}
	if tag, err := c.Exec(ctx(t), ""); err != nil || tag != "" {
		t.Fatalf("%q %v", tag, err)
	}
}

// Malformed and unexpected messages break the connection with an error.
func TestMalformedMessages(t *testing.T) {
	for _, c := range []struct {
		name   string
		run    func(c *Conn) error
		script func(f *fakeConn)
		err    string
	}{
		{"row description", simple, func(f *fakeConn) { f.read(); f.send('T', []byte{0}) }, "malformed RowDescription"},
		{"row description field", simple, func(f *fakeConn) { f.read(); f.send('T', u16(1), cs("a")) }, "malformed RowDescription"},
		{"data row without description", simple, func(f *fakeConn) { f.read(); f.dataRow([]byte("1")) }, "unexpected"},
		{"data row", simple, func(f *fakeConn) {
			f.read()
			f.rowDesc("a", OIDText)
			f.send('D', u16(1), u32(10), []byte("x"))
		}, "malformed DataRow"},
		{"unknown message", simple, func(f *fakeConn) { f.read(); f.send('?') }, "unexpected"},
		{"too large", simple, func(f *fakeConn) { f.read(); f.send('C', make([]byte, 2000)) }, "beyond the bound"},
		{"cut header", simple, func(f *fakeConn) { f.read(); f.nc.Write([]byte{'C', 0}); f.nc.Close() }, "unexpected EOF"},
		{"cut body", simple, func(f *fakeConn) { f.read(); f.nc.Write([]byte{'C', 0, 0, 0, 10, 'x'}); f.nc.Close() }, "unexpected EOF"},
		{"param description", query, func(f *fakeConn) { f.until('S'); f.send('1'); f.send('t', u16(2), u32(1)) }, "malformed ParameterDescription"},
		{"prepare row description", query, func(f *fakeConn) { f.until('S'); f.send('1'); f.send('T', []byte{0}) }, "malformed RowDescription"},
		{"prepare unexpected", query, func(f *fakeConn) { f.until('S'); f.send('D') }, "unexpected"},
		{"bind unexpected", query, func(f *fakeConn) { f.prepared(nil, "a", OIDInt4); f.until('S'); f.send('D') }, "unexpected"},
		{"rows unexpected", query, func(f *fakeConn) { f.prepared(nil, "a", OIDInt4); f.until('S'); f.send('2'); f.send('?') }, "unexpected"},
		{"rows short", query, func(f *fakeConn) {
			f.prepared(nil, "a", OIDInt4)
			f.until('S')
			f.send('2')
			f.dataRow()
		}, "malformed DataRow"},
		{"rows split", query, func(f *fakeConn) {
			f.prepared(nil, "a", OIDInt4)
			f.until('S')
			f.send('2')
			f.send('D', []byte{0})
		}, "malformed DataRow"},
		{"rows NULL length", query, func(f *fakeConn) {
			f.prepared(nil, "a", OIDInt4)
			f.until('S')
			f.send('2')
			f.send('D', u16(1), []byte{0})
		}, "malformed DataRow"},
	} {
		t.Run(c.name, func(t *testing.T) {
			addr := fakeServer(t, func(f *fakeConn) {
				f.startup()
				f.ready()
				c.script(f)
				f.until('X')
			})
			conn, err := Connect(ctx(t), Config{Addr: addr, User: "u", SSLMode: SSLDisable, MaxMessageBytes: 1000})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			err = c.run(conn)
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Fatalf("err %v, want %q", err, c.err)
			}
			if conn.Err() == nil {
				t.Fatal("the connection is not broken")
			}
		})
	}
}

func simple(c *Conn) error {
	_, err := c.SimpleQuery(ctx(nil), "q")
	return err
}

func query(c *Conn) error {
	rows, err := c.Query(ctx(nil), "q")
	if err != nil {
		return err
	}
	return rows.Close()
}

func TestSimpleQueryBudget(t *testing.T) {
	c := sessionCfg(t, Config{MaxMessageBytes: 100}, func(f *fakeConn) {
		f.read()
		f.rowDesc("a", OIDText)
		for range 5 {
			f.dataRow(make([]byte, 40))
		}
		f.send('C', cs("SELECT 5"))
		f.z()
		f.until('X')
	})
	if _, err := c.SimpleQuery(ctx(t), "q"); err == nil || !strings.Contains(err.Error(), "beyond") {
		t.Fatalf("%v", err)
	}
	if c.Err() != nil {
		t.Fatal("the connection broke")
	}
}

func TestExtendedQuery(t *testing.T) {
	c := session(t, func(f *fakeConn) {
		// Exec with arguments: a NoData statement.
		f.prepared([]uint32{OIDInt4, OIDBytea, OIDText})
		f.until('S')
		f.send('2')
		f.send('C', cs("INSERT 0 1"))
		f.z()
		// A query whose rows end in an error, after a row that fails to decode.
		f.prepared(nil, "n", OIDNumeric, "s", OIDInterval)
		f.until('S')
		f.send('2')
		f.dataRow([]byte{1}, []byte("1 day"))
		f.dataRow([]byte{1}, []byte("1 day"))
		f.errResp("22012", "division by zero")
		f.z()
		// Rows with every closing message, read whole.
		f.prepared(nil, "a", OIDInt4, "s", OIDInterval)
		f.until('S')
		f.send('2')
		f.dataRow(u32(5), []byte("1 day"))
		f.dataRow(nil, nil)
		f.send('s')
		f.send('C', cs("SELECT 2"))
		f.z()
		// A Bind refused.
		f.prepared([]uint32{OIDInt4})
		f.until('S')
		f.errResp("22P02", "invalid input syntax")
		f.z()
		// A Parse refused.
		f.until('S')
		f.errResp("42601", "syntax error")
		f.z()
		// A named statement closed.
		f.prepared([]uint32{OIDInt4}, "a", OIDInt4)
		f.until('S')
		f.send('3')
		f.z()
		f.until('X')
	})
	if tag, err := c.Exec(ctx(t), "INSERT", 1, []byte{1}, []byte("t")); err != nil || tag != "INSERT 0 1" {
		t.Fatalf("%q %v", tag, err)
	}
	rows, err := c.Query(ctx(t), "SELECT n, s")
	if err != nil {
		t.Fatal(err)
	}
	if rows.Next() || rows.Err() == nil || !strings.Contains(rows.Err().Error(), `column "n"`) {
		t.Fatalf("decode error %v", rows.Err())
	}
	rows, err = c.Query(ctx(t), "SELECT a, s")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows.Fields()) != 2 || rows.Fields()[0].Format != 1 || rows.Fields()[1].Format != 0 {
		t.Fatalf("%+v", rows.Fields())
	}
	var got [][]any
	for rows.Next() {
		got = append(got, append([]any{}, rows.Values()...))
		if len(rows.RawValues()) != 2 {
			t.Fatal("raw values")
		}
	}
	if rows.Close() != nil || rows.Tag() != "SELECT 2" || len(got) != 2 || got[0][0] != int64(5) || got[0][1] != "1 day" || got[1][0] != nil {
		t.Fatalf("%v %q %v", got, rows.Tag(), rows.Err())
	}
	if rows.Next() {
		t.Fatal("a row after the end")
	}
	var pe *Error
	if _, err := c.Exec(ctx(t), "SELECT $1", "x"); !errors.As(err, &pe) || pe.Code != "22P02" {
		t.Fatalf("%v", err)
	}
	if _, err := c.Query(ctx(t), "SELEC"); !errors.As(err, &pe) || pe.Code != "42601" {
		t.Fatalf("%v", err)
	}
	st, err := c.Prepare(ctx(t), "s1", "SELECT $1")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Params()) != 1 || len(st.Fields()) != 1 {
		t.Fatalf("%v %v", st.Params(), st.Fields())
	}
	if _, err := st.Query(ctx(t)); err == nil {
		t.Fatal("too few arguments")
	}
	if _, err := st.Query(ctx(t), struct{}{}); err == nil || !strings.Contains(err.Error(), "$1") {
		t.Fatalf("%v", err)
	}
	if err := st.Close(ctx(t)); err != nil {
		t.Fatal(err)
	}
	if c.Err() != nil {
		t.Fatal(c.Err())
	}
}

func TestBusyAndBroken(t *testing.T) {
	c := session(t, func(f *fakeConn) {
		f.prepared(nil, "a", OIDInt4)
		f.until('S')
		f.send('2')
		f.dataRow(u32(1))
		f.send('C', cs("SELECT 1"))
		f.z()
		f.until('X')
	})
	rows, err := c.Query(ctx(t), "SELECT 1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.SimpleQuery(ctx(t), "x"); !errors.Is(err, ErrBusy) {
		t.Fatalf("%v", err)
	}
	rows.Close()
	done, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.SimpleQuery(done, "x"); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
	if c.Err() != nil {
		t.Fatal("a context done before the call broke the connection")
	}
	// A write that fails breaks the connection.
	c.nc.Close()
	if _, err := c.SimpleQuery(ctx(t), "x"); err == nil || c.Err() == nil {
		t.Fatalf("%v %v", err, c.Err())
	}
	if _, err := c.Prepare(ctx(t), "", "x"); err == nil {
		t.Fatal("a broken connection prepared")
	}
	st := &Stmt{c: c}
	if _, err := st.Query(ctx(t)); err == nil {
		t.Fatal("a broken connection queried")
	}
	if err := st.Close(ctx(t)); err == nil {
		t.Fatal("a broken connection closed a statement")
	}
}

func TestBrokenWrites(t *testing.T) {
	for name, run := range map[string]func(c *Conn) error{
		"prepare": func(c *Conn) error { _, err := c.Prepare(ctx(nil), "", "x"); return err },
		"bind": func(c *Conn) error {
			st := &Stmt{c: c}
			_, err := st.Query(ctx(nil))
			return err
		},
		"close":   func(c *Conn) error { return (&Stmt{c: c}).Close(ctx(nil)) },
		"copy in": func(c *Conn) error { _, err := c.CopyIn(ctx(nil), "x"); return err },
		"copy to": func(c *Conn) error { _, err := c.CopyTo(ctx(nil), "x", nil); return err },
		"start replication": func(c *Conn) error {
			c.cfg.Replication = true
			return c.StartReplication(ctx(nil), "s", 0)
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := session(t, func(f *fakeConn) { f.until('X') })
			c.nc.(interface{ CloseWrite() error }).CloseWrite()
			if err := run(c); err == nil {
				t.Fatal("no error")
			}
		})
	}
}

// A context that ends breaks the connection, returns the context's error,
// and sends a CancelRequest with the backend's key.
func TestContextEnds(t *testing.T) {
	cancels := make(chan [2]uint32, 1)
	addr := fakeServerOpts(t, fakeOpts{cancels: cancels}, func(f *fakeConn) {
		f.startup()
		f.ready()
		f.read() // the query, never answered
		f.read() // until the client closes
	})
	c, err := Connect(ctx(t), Config{Addr: addr, User: "u", SSLMode: SSLDisable})
	if err != nil {
		t.Fatal(err)
	}
	cx, cancel := context.WithTimeout(ctx(t), 50*time.Millisecond)
	defer cancel()
	_, err = c.SimpleQuery(cx, "SELECT pg_sleep(100)")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err %v", err)
	}
	if c.Err() == nil {
		t.Fatal("the connection is not broken")
	}
	if got := <-cancels; got != [2]uint32{42, 7} {
		t.Fatalf("cancel %v", got)
	}
	if _, err := c.SimpleQuery(ctx(t), "SELECT 1"); err == nil {
		t.Fatal("a broken connection ran a query")
	}
	// Without a backend key there is nothing to cancel; a server that is
	// gone is not an error.
	(&Conn{}).cancelRequest()
	(&Conn{pid: 1, addr: "127.0.0.1:1"}).cancelRequest()
}

func TestErrorFields(t *testing.T) {
	var b []byte
	for _, f := range "VSCMDHPWstcdn" {
		b = append(b, byte(f))
		b = append(b, cs(string(f)+"v")...)
	}
	e := parseError(append(b, 0))
	want := Error{"Vv", "Cv", "Mv", "Dv", "Hv", "Pv", "Wv", "sv", "tv", "cv", "dv", "nv"}
	if *e != want || !strings.Contains(e.Error(), "Dv") {
		t.Fatalf("%+v", e)
	}
	if e := parseError([]byte("Mno end")); e.Message != "" {
		t.Fatalf("%+v", e)
	}
	if e := parseError(append(cs("Sonly"), 0)); e.Severity != "only" || strings.Contains(e.Error(), ": :") {
		t.Fatalf("%+v", e)
	}
}

// sessionCfg is session with a config of the test's.
func sessionCfg(t *testing.T, cfg Config, script func(f *fakeConn)) *Conn {
	t.Helper()
	addr := fakeServer(t, func(f *fakeConn) {
		f.startup()
		f.ready()
		script(f)
	})
	cfg.Addr, cfg.User, cfg.SSLMode = addr, "u", SSLDisable
	c, err := Connect(ctx(t), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}
