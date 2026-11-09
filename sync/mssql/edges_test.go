package syncmssql

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	fitsync "github.com/lesismal/fit/sync"
	mssqlwire "github.com/lesismal/fit/sync/mssql/wire"
)

// richFake is a table with rows, a window of transactions of several rows,
// a DDL and updates.
func richFake() (*fake, *fakeInst) {
	f := newFake()
	tb := f.table("t", 2)
	in := f.capture(tb, "dbo_t")
	return f, in
}

func richChanges(f *fake, in *fakeInst) {
	f.commit(in, int64(cdcInsert), row2(3, "c"), in, int64(cdcInsert), row2(4, "d"))
	f.ddls = append(f.ddls, fakeDDL{lsn: f.max, table: "t", cmd: "x"})
	f.commit(in, int64(cdcUpdateBefore), row2(3, "c"), in, int64(cdcUpdateAfter), row2(3, "C"), in, int64(cdcDelete), row2(4, "d"))
	f.commit(in, int64(cdcInsert), row2(5, "e"))
}

// TestEmitErrors stops the run at each record's emit, in a window and in
// a lone transaction's pages, and with change tracking.
func TestEmitErrors(t *testing.T) {
	for _, rows := range []int{1000, 1} {
		for after := 0; after < 12; after++ {
			f, in := richFake()
			s := newSource(t, f, map[string]any{"window_rows": rows})
			n := 0
			err := s.Run(context.Background(), &fitsync.Run{Once: true, Emit: func(*fitsync.Record) error {
				if n == 2 {
					richChanges(f, in)
				}
				if n == after {
					return errors.New("stopped")
				}
				n++
				return nil
			}})
			if err == nil && after < n {
				t.Fatal(rows, after, "no error")
			}
		}
	}
	for after := 0; after < 4; after++ {
		f := newFake()
		f.ct = true
		tb := f.table("t", 0)
		tb.ct = true
		f.ctChange(tb, "I", row2(1, "a"))
		f.ctChange(tb, "I", row2(2, "b"))
		s := newSource(t, f, map[string]any{"mode": "change_tracking", "snapshot": "never"})
		n := 0
		err := s.Run(context.Background(), &fitsync.Run{From: position{}.marshal(), Once: true, Emit: func(*fitsync.Record) error {
			if n == after {
				return errors.New("stopped")
			}
			n++
			return nil
		}})
		if err == nil && after < 2 {
			t.Fatal(after, "no error")
		}
	}
}

// TestLaterErrors fails queries after their first calls.
func TestLaterErrors(t *testing.T) {
	for _, tc := range []struct {
		key  string
		skip int
		once bool
		mode string
	}{
		{"fn_cdc_get_max_lsn", 1, true, ""},
		{"fn_cdc_get_max_lsn", 1, false, ""},
		{"fn_cdc_get_max_lsn", 2, true, ""},
		{"SELECT capture_instance", 1, false, ""},
		{"fn_cdc_get_all_changes_", 1, true, ""},
		{"CHANGE_TRACKING_CURRENT_VERSION", 1, true, "change_tracking"},
		{"CHANGE_TRACKING_CURRENT_VERSION", 1, false, "change_tracking"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			f, in := richFake()
			f.ct = true
			f.tables["dbo.t"].ct = true
			richChanges(f, in)
			f.ctChange(f.tables["dbo.t"], "I", row2(9, "z"))
			f.fail[tc.key] = errors.New("fake: " + tc.key)
			f.skip[tc.key] = tc.skip
			params := map[string]any{"poll_interval": "1ms", "window_rows": 1, "snapshot": "never"}
			if tc.mode != "" {
				params["mode"] = tc.mode
			}
			s := newSource(t, f, params)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var from fitsync.Position = position{LSN: zeroLSN}.marshal()
			if tc.mode != "" {
				from = position{}.marshal()
			}
			err := s.Run(ctx, &fitsync.Run{From: from, Once: tc.once, Emit: func(*fitsync.Record) error { return nil }})
			if err == nil || !strings.Contains(err.Error(), "fake: ") {
				t.Fatal(err)
			}
		})
	}
	// Rediscovering fails; so does an added table's snapshot.
	for _, key := range []string{"SELECT is_cdc_enabled", "FROM [dbo].[b]"} {
		f := newFake()
		a := f.table("a", 0)
		f.capture(a, "dbo_a")
		s := newSource(t, f, map[string]any{"snapshot": "never", "poll_interval": "1ms"})
		step := 0
		s.sleep = func(ctx context.Context, d time.Duration) error {
			step++
			if step == 1 {
				f.now = f.now.Add(time.Minute)
				f.capture(f.table("b", 1), "dbo_b")
				f.fail[key] = errors.New("fake: rediscover")
			}
			return ctx.Err()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := s.Run(ctx, &fitsync.Run{From: position{LSN: zeroLSN, Since: f.now}.marshal(), Emit: func(*fitsync.Record) error { return nil }})
		cancel()
		if err == nil || !strings.Contains(err.Error(), "rediscover") {
			t.Fatal(key, err)
		}
	}
}

// TestJobEnd stops a job at the highest LSN there was when it started,
// changes committed while it reads left to the next run.
func TestJobEnd(t *testing.T) {
	f, in := richFake()
	s := newSource(t, f, map[string]any{"snapshot": "never"})
	f.commit(in, int64(cdcInsert), row2(7, "g"))
	n := 0
	err := s.Run(context.Background(), &fitsync.Run{From: position{LSN: zeroLSN}.marshal(), Once: true, Emit: func(*fitsync.Record) error {
		n++
		f.commit(in, int64(cdcInsert), row2(int64(100+n), "late"))
		return nil
	}})
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
}

// TestKeysAndTables: the keys param, a key column CDC does not capture,
// a snapshot resumed past its first tables, an instance younger than the
// head, no row in sys.databases, a tracked table left out.
func TestKeysAndTables(t *testing.T) {
	f := newFake()
	a := f.table("a", 2)
	f.capture(a, "dbo_a")
	b := f.table("b", 2)
	ib := f.capture(b, "dbo_b")
	ib.created = f.now.Add(time.Hour)
	s := newSource(t, f, map[string]any{"keys": map[string]any{"dbo.a": []string{"v"}, "dbo.b": []string{"missing"}}})
	recs, err := run(t, s, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	checkRecs(t, recs,
		`insert dbo.a {"v":"r1"} {"id":1,"v":"r1"} @snap dbo.a {"map":{"v":"r1"}}`,
		`insert dbo.a {"v":"r2"} {"id":2,"v":"r2"} @snap dbo.a {"map":{"v":"r2"}}`,
		`insert dbo.b null {"id":1,"v":"r1"} @snap dbo.b null`,
		`insert dbo.b null {"id":2,"v":"r2"} @0`)
	// The head's time moved to the young instance's.
	if !strings.Contains(string(recs[3].Position), "09:00") {
		t.Fatal(string(recs[3].Position))
	}
	from := position{LSN: zeroLSN, Snapshot: &snapPos{Table: "dbo.b"}}.marshal()
	if recs, err = run(t, s, from, true); err != nil || len(recs) != 2 {
		t.Fatal(recs, err)
	}
	f.noDB = true
	if _, err := run(t, s, nil, true); err == nil || !strings.Contains(err.Error(), "sys.databases") {
		t.Fatal(err)
	}
	g := newFake()
	g.ct = true
	for _, name := range []string{"in", "out"} {
		g.table(name, 0).ct = true
	}
	g.ctChange(g.tables["dbo.out"], "I", row2(1, "x"))
	s = newSource(t, g, map[string]any{"mode": "change_tracking", "snapshot": "never", "exclude": []string{"dbo.out"}})
	if recs, err := run(t, s, position{}.marshal(), true); err != nil || len(recs) != 0 {
		t.Fatal(recs, err)
	}
}

// TestWireConn runs queries through the client against a server that
// answers them in TDS.
func TestWireConn(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		nc, err := ln.Accept()
		if err != nil {
			return
		}
		defer nc.Close()
		send := func(b []byte) {
			h := []byte{4, 1, 0, 0, 0, 0, 1, 0}
			binary.BigEndian.PutUint16(h[2:], uint16(len(b)+8))
			nc.Write(append(h, b...))
		}
		read := func() byte {
			var h [8]byte
			if _, err := io.ReadFull(nc, h[:]); err != nil {
				return 0
			}
			io.ReadFull(nc, make([]byte, binary.BigEndian.Uint16(h[2:])-8))
			return h[0]
		}
		done := func(status byte, n byte) []byte { return []byte{0xfd, status, 0, 0xc1, 0, n, 0, 0, 0, 0, 0, 0, 0} }
		read()
		send([]byte{0, 0, 11, 0, 6, 1, 0, 17, 0, 1, 0xff, 16, 0, 0, 0, 0, 0, 2})
		read()
		send(append([]byte{0xad, 10, 0, 1, 0x74, 0, 0, 4, 0, 16, 0, 0, 0}, done(0, 0)...))
		result := []byte{0x81, 1, 0, 0, 0, 0, 0, 0, 0, 0x26, 4, 1, 'n', 0, 0xd1, 4, 7, 0, 0, 0, 0xd1, 4, 8, 0, 0, 0}
		result = append(result, done(0x10, 2)...)
		for {
			switch read() {
			case 0:
				return
			case 6:
				send(done(0x20, 0))
			default:
				send(result)
			}
		}
	}()
	c, err := dialWire(context.Background(), mssqlwire.Config{Addr: ln.Addr().String(), Encrypt: mssqlwire.EncryptOff, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()
	var got []any
	if err := c.Each(ctx, func(cols []mssqlwire.Column, vals []any) error { got = append(got, vals[0]); return nil }, "SELECT n"); err != nil || len(got) != 2 {
		t.Fatal(got, err)
	}
	if err := c.Each(ctx, func([]mssqlwire.Column, []any) error { return errors.New("enough") }, "SELECT n"); err == nil || err.Error() != "enough" {
		t.Fatal(err)
	}
	ln.Close()
	c.Close()
	if err := c.Each(ctx, func([]mssqlwire.Column, []any) error { return nil }, "SELECT n"); err == nil {
		t.Fatal("a query on a closed connection")
	}
}
