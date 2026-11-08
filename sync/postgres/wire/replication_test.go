package pgwire

import (
	"encoding/binary"
	"errors"
	"strings"
	"testing"
	"time"
)

// replSession is a session with Replication set.
func replSession(t *testing.T, version string, script func(f *fakeConn)) *Conn {
	t.Helper()
	addr := fakeServer(t, func(f *fakeConn) {
		f.startup()
		f.send('R', u32(0))
		f.send('S', cs("server_version"), cs(version))
		f.send('K', u32(42), u32(7))
		f.z()
		script(f)
	})
	c, err := Connect(ctx(t), Config{Addr: addr, User: "u", SSLMode: SSLDisable, Replication: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// result answers a simple query with one row of text columns, by pairs.
func (f *fakeConn) result(cols ...string) {
	var desc []any
	var vals [][]byte
	for i := 0; i < len(cols); i += 2 {
		desc = append(desc, cols[i], OIDText)
		vals = append(vals, []byte(cols[i+1]))
	}
	f.rowDesc(desc...)
	f.dataRow(vals...)
	f.send('C', cs("SELECT 1"))
	f.z()
}

func TestReplicationCommands(t *testing.T) {
	queries := make(chan string, 10)
	q := func(f *fakeConn) {
		_, b := f.read()
		queries <- strings.TrimRight(string(b), "\x00")
	}
	c := replSession(t, "16.4", func(f *fakeConn) {
		q(f)
		f.result("systemid", "7300", "timeline", "1", "xlogpos", "0/16B3748", "dbname", "fit")
		q(f)
		f.result("slot_name", "s", "consistent_point", "0/16B3780", "snapshot_name", "00000003-00000002-1", "output_plugin", "pgoutput")
		q(f)
		f.send('C', cs("DROP_REPLICATION_SLOT"))
		f.z()
		q(f) // a bad LSN
		f.result("systemid", "7300", "timeline", "1", "xlogpos", "nope", "dbname", "fit")
		q(f)
		f.result("slot_name", "s", "consistent_point", "nope")
		q(f) // no result at all
		f.z()
		q(f)
		f.errResp("42710", "replication slot \"s\" already exists")
		f.z()
		f.until('X')
	})
	sys, err := c.IdentifySystem(ctx(t))
	if err != nil || sys != (System{SystemID: "7300", Timeline: 1, XLogPos: 0x16B3748, DBName: "fit"}) || <-queries != "IDENTIFY_SYSTEM" {
		t.Fatalf("%+v %v", sys, err)
	}
	sl, err := c.CreateReplicationSlot(ctx(t), "s", SlotOptions{Temporary: true, Snapshot: SnapshotExport})
	if err != nil || sl.SnapshotName == "" || sl.ConsistentPoint != 0x16B3780 || sl.Plugin != "pgoutput" {
		t.Fatalf("%+v %v", sl, err)
	}
	if got := <-queries; got != `CREATE_REPLICATION_SLOT "s" TEMPORARY LOGICAL "pgoutput" (SNAPSHOT 'export')` {
		t.Fatalf("%s", got)
	}
	if err := c.DropReplicationSlot(ctx(t), "s", true); err != nil || <-queries != `DROP_REPLICATION_SLOT "s" WAIT` {
		t.Fatal(err)
	}
	if _, err := c.IdentifySystem(ctx(t)); err == nil {
		t.Fatal("a bad LSN read")
	}
	<-queries
	if _, err := c.CreateReplicationSlot(ctx(t), "s", SlotOptions{}); err == nil {
		t.Fatal("a bad LSN read")
	}
	<-queries
	if sys, err := c.IdentifySystem(ctx(t)); err == nil || sys.SystemID != "" {
		t.Fatalf("no result: %+v %v", sys, err)
	}
	<-queries
	var pe *Error
	if _, err := c.CreateReplicationSlot(ctx(t), "s", SlotOptions{}); !errors.As(err, &pe) || pe.Code != "42710" {
		t.Fatalf("%v", err)
	}
	<-queries

	// Before 15, the old syntax.
	c = replSession(t, "13.18", func(f *fakeConn) {
		for range 3 {
			q(f)
			f.result("slot_name", "s", "consistent_point", "0/1")
		}
		f.until('X')
	})
	for snap, want := range map[string]string{SnapshotExport: "EXPORT_SNAPSHOT", SnapshotUse: "USE_SNAPSHOT", SnapshotNothing: "NOEXPORT_SNAPSHOT"} {
		if _, err := c.CreateReplicationSlot(ctx(t), "s", SlotOptions{Plugin: "wal2json", Snapshot: snap}); err != nil {
			t.Fatal(err)
		}
		if got := <-queries; got != `CREATE_REPLICATION_SLOT "s" LOGICAL "wal2json" `+want {
			t.Fatalf("%s", got)
		}
	}
	if _, err := c.CreateReplicationSlot(ctx(t), "s", SlotOptions{Snapshot: "later"}); err == nil {
		t.Fatal("an unknown snapshot action")
	}

	// Not a replication connection.
	n := session(t, func(f *fakeConn) { f.until('X') })
	if _, err := n.IdentifySystem(ctx(t)); err == nil {
		t.Fatal("IDENTIFY_SYSTEM without replication")
	}
	if err := n.StartReplication(ctx(t), "s", 0); err == nil {
		t.Fatal("START_REPLICATION without replication")
	}
	if _, err := n.ReceiveReplication(ctx(t)); err == nil {
		t.Fatal("a stream without START_REPLICATION")
	}
	if err := n.SendStandbyStatus(ctx(t), StandbyStatus{}); err == nil {
		t.Fatal("a status without START_REPLICATION")
	}
	if err := n.EndReplication(ctx(t)); err != nil {
		t.Fatal(err)
	}
}

func TestReplicationStream(t *testing.T) {
	status := make(chan []byte, 2)
	start := make(chan string, 1)
	now := time.Date(2026, 1, 2, 3, 4, 5, 6000, time.UTC)
	us := uint64(now.UnixMicro() - pgEpochUnixMicro)
	c := replSession(t, "17.2", func(f *fakeConn) {
		_, b := f.read()
		start <- strings.TrimRight(string(b), "\x00")
		f.send('W', []byte{0}, u16(0))
		f.send('d', []byte{'w'}, u64(0x100), u64(0x200), u64(us), []byte("Bbegin"))
		f.send('d', []byte{'k'}, u64(0x200), u64(us), []byte{1})
		f.send('d', []byte{'?'}) // skipped
		f.send('d', []byte{'k'}, u64(0x300), u64(us), []byte{0})
		for range 2 {
			if typ, b := f.read(); typ == 'd' {
				status <- b
			}
		}
		f.send('c') // the server ends the stream
		f.until('c')
		f.send('C', cs("START_REPLICATION"))
		f.z()
		// Again, ended by the client, with an error in the reply.
		f.read()
		f.send('W', []byte{0}, u16(0))
		f.until('c')
		f.send('c')
		f.errResp("XX000", "late")
		f.z()
		// Refused.
		f.read()
		f.errResp("42704", "replication slot \"x\" does not exist")
		f.z()
		// A stream that the server breaks with an error.
		f.read()
		f.send('W', []byte{0}, u16(0))
		f.errResp("57P01", "terminating connection")
		f.z()
		f.until('X')
	})
	if err := c.StartReplication(ctx(t), "s", 0x16B3780, "proto_version", "2", "publication_names", "p'1"); err != nil {
		t.Fatal(err)
	}
	if got := <-start; got != `START_REPLICATION SLOT "s" LOGICAL 0/16B3780 ("proto_version" '2', "publication_names" 'p''1')` {
		t.Fatalf("%s", got)
	}
	m, err := c.ReceiveReplication(ctx(t))
	x, ok := m.(*XLogData)
	if err != nil || !ok || x.WALStart != 0x100 || x.ServerEnd != 0x200 || !x.ServerTime.Equal(now) || string(x.Data) != "Bbegin" {
		t.Fatalf("%+v %v", m, err)
	}
	m, err = c.ReceiveReplication(ctx(t))
	if k, ok := m.(*Keepalive); err != nil || !ok || !k.ReplyRequested || k.ServerEnd != 0x200 {
		t.Fatalf("%+v %v", m, err)
	}
	if err := c.SendStandbyStatus(ctx(t), StandbyStatus{Write: 0x200, Flush: 0x180, Apply: 0x100, ReplyRequested: true}); err != nil {
		t.Fatal(err)
	}
	m, err = c.ReceiveReplication(ctx(t))
	if k, ok := m.(*Keepalive); err != nil || !ok || k.ReplyRequested || k.ServerEnd != 0x300 {
		t.Fatalf("%+v %v", m, err)
	}
	b := <-status
	if b[0] != 'r' || binary.BigEndian.Uint64(b[1:]) != 0x200 || binary.BigEndian.Uint64(b[9:]) != 0x180 ||
		binary.BigEndian.Uint64(b[17:]) != 0x100 || b[33] != 1 || len(b) != 34 {
		t.Fatalf("status %x", b)
	}
	c.SendStandbyStatus(ctx(nil), StandbyStatus{})
	<-status
	if _, err := c.ReceiveReplication(ctx(t)); !errors.Is(err, ErrReplicationEnded) {
		t.Fatalf("%v", err)
	}
	if err := c.EndReplication(ctx(t)); err != nil {
		t.Fatal(err)
	}

	if err := c.StartReplication(ctx(t), "s", 0); err != nil {
		t.Fatal(err)
	}
	var pe *Error
	if err := c.EndReplication(ctx(t)); !errors.As(err, &pe) || pe.Message != "late" {
		t.Fatalf("%v", err)
	}
	if err := c.StartReplication(ctx(t), "x", 0); !errors.As(err, &pe) || pe.Code != "42704" {
		t.Fatalf("%v", err)
	}
	if err := c.StartReplication(ctx(t), "s", 0, "odd"); err == nil {
		t.Fatal("an odd option list")
	}
	if err := c.StartReplication(ctx(t), "s", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ReceiveReplication(ctx(t)); !errors.As(err, &pe) || pe.Code != "57P01" {
		t.Fatalf("%v", err)
	}
	if c.busy || c.Err() != nil {
		t.Fatal("the connection is not ready after the stream's error")
	}
}

func TestReplicationMalformed(t *testing.T) {
	for _, c := range []struct {
		name string
		msg  func(f *fakeConn)
		err  string
	}{
		{"empty", func(f *fakeConn) { f.send('d') }, "malformed CopyData"},
		{"short xlog", func(f *fakeConn) { f.send('d', []byte{'w', 1}) }, "malformed XLogData"},
		{"short keepalive", func(f *fakeConn) { f.send('d', []byte{'k', 1}) }, "malformed PrimaryKeepalive"},
		{"unexpected", func(f *fakeConn) { f.send('D') }, "unexpected"},
		{"closed", func(f *fakeConn) { f.nc.Close() }, "EOF"},
	} {
		t.Run(c.name, func(t *testing.T) {
			conn := replSession(t, "16.4", func(f *fakeConn) {
				f.read()
				f.send('W', []byte{0}, u16(0))
				c.msg(f)
				f.until('X')
			})
			if err := conn.StartReplication(ctx(t), "s", 0); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.ReceiveReplication(ctx(t)); err == nil || !strings.Contains(err.Error(), c.err) {
				t.Fatalf("%v", err)
			}
			if _, err := conn.ReceiveReplication(ctx(t)); err == nil {
				t.Fatal("a broken stream read")
			}
			if err := conn.SendStandbyStatus(ctx(t), StandbyStatus{}); err == nil {
				t.Fatal("a status on a broken connection")
			}
			if err := conn.EndReplication(ctx(t)); err == nil {
				t.Fatal("a broken stream ended")
			}
		})
	}
	// START_REPLICATION answered with something else.
	conn := replSession(t, "16.4", func(f *fakeConn) {
		f.read()
		f.send('D')
		f.until('X')
	})
	if err := conn.StartReplication(ctx(t), "s", 0); err == nil || !strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("%v", err)
	}
	// EndReplication on a connection whose writes fail.
	conn = replSession(t, "16.4", func(f *fakeConn) {
		f.read()
		f.send('W', []byte{0}, u16(0))
		f.until('X')
	})
	conn.StartReplication(ctx(t), "s", 0)
	conn.nc.(interface{ CloseWrite() error }).CloseWrite()
	if err := conn.EndReplication(ctx(t)); err == nil {
		t.Fatal("no error")
	}
}
