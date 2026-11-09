package syncpostgres

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	fitsync "github.com/lesismal/fit/sync"
	pgwire "github.com/lesismal/fit/sync/postgres/wire"
)

// A minimal backend: it reads the startup message, sends its parameters
// back on a channel, and answers AuthenticationOk and ReadyForQuery.
func startupServer(t *testing.T) (string, <-chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got := make(chan string, 4)
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer nc.Close()
				nc.SetDeadline(time.Now().Add(time.Minute)) // a hang guard
				br := bufio.NewReader(nc)
				var n uint32
				if binary.Read(br, binary.BigEndian, &n) != nil || n < 8 {
					return
				}
				b := make([]byte, n-4)
				if _, err := io.ReadFull(br, b); err != nil {
					return
				}
				got <- strings.ReplaceAll(string(b[4:]), "\x00", " ")
				nc.Write([]byte{'R', 0, 0, 0, 8, 0, 0, 0, 0, 'Z', 0, 0, 0, 5, 'I'})
				io.Copy(io.Discard, br)
			}()
		}
	}()
	return ln.Addr().String(), got
}

func TestRealDialers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute) // a hang guard
	defer cancel()
	addr, params := startupServer(t)
	cfg := pgwire.Config{Addr: addr, User: "u", Database: "d", SSLMode: pgwire.SSLDisable, Replication: true}

	c, err := realDialSource(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if p := <-params; !strings.Contains(p, "replication database") {
		t.Fatalf("source startup %q", p)
	}
	// The snapshot's connection drops replication from the same config.
	s, err := realDialSQL(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if p := <-params; strings.Contains(p, "replication") || !strings.Contains(p, "database d") {
		t.Fatalf("sql startup %q", p)
	}

	// Nothing listening.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := ln.Addr().String()
	ln.Close()
	if _, err := realDialSQL(ctx, pgwire.Config{Addr: dead, SSLMode: pgwire.SSLDisable}); err == nil {
		t.Fatal("dialed nothing")
	}
}

// realDialSQL is dialSQL before tests replace it.
var realDialSQL = dialSQL

func TestPrepareSkipsShortRows(t *testing.T) {
	// Rows of too few columns from the catalog are not tables.
	cat := newSrcCatalog()
	f := newSrcFake(func(sql string) ([]pgwire.Result, error) {
		res, err := cat.answer(sql)
		switch {
		case strings.HasPrefix(sql, "SELECT schemaname"):
			res[0].Rows = append(res[0].Rows, [][]byte{[]byte("public")})
		case strings.HasPrefix(sql, "SELECT n.nspname"):
			res[0].Rows = append(res[0].Rows, [][]byte{[]byte("public"), []byte("x")})
		}
		return res, err
	})
	st, err := srcMust(t, nil).prepare(context.Background(), f, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.tables) != 2 || st.tables["public.t"] == nil || st.tables["public.f"] == nil {
		t.Fatalf("tables %v", st.tables)
	}
}

func TestDecoderEmitErrors(t *testing.T) {
	stop := errors.New("stop")
	newDecoder := func() *decoder {
		d := &decoder{rels: map[uint32]*relation{}, pk: map[string][]string{}, spools: map[uint32]*spool{}, spoolDir: t.TempDir(),
			emit: func(*fitsync.Record) error { return stop }, onCommit: func(pgwire.LSN, bool) {}}
		if err := d.handle(msgRel(1, "t", 'd', "id")); err != nil {
			t.Fatal(err)
		}
		return d
	}
	ins := newMsg('I').u32(1).u8('N').tuple("1", "x", nil)
	for _, c := range []struct {
		name string
		msg  srcMsg
	}{
		// Each record after the first sends the one before it on: the
		// emit's error comes back from the message that made the second.
		{"truncate", newMsg('T').u32(1).u8(0).u32(1)},
		{"key change", newMsg('U').u32(1).u8('K').tuple("1", nil, nil).u8('N').tuple("2", "y", nil)},
	} {
		d := newDecoder()
		for _, m := range []srcMsg{msgBegin(1, 1), ins} {
			if err := d.handle(m); err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
		}
		if err := d.handle(c.msg); err != stop {
			t.Errorf("%s: %v, want the emit's error", c.name, err)
		}
		d.close()
	}

	// A tuple with no column count.
	d := newDecoder()
	d.handle(msgBegin(1, 1))
	if err := d.handle(newMsg('I').u32(1).u8('N')); err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("err %v", err)
	}
	d.close()
}

func TestSpoolFailures(t *testing.T) {
	d := &decoder{rels: map[uint32]*relation{}, pk: map[string][]string{}, spools: map[uint32]*spool{}, spoolDir: t.TempDir(),
		emit: func(*fitsync.Record) error { return nil }, onCommit: func(pgwire.LSN, bool) {}}
	defer d.close()
	d.handle(msgRel(1, "t", 'd', "id"))
	commit := func(xid uint32) error {
		return d.handle(newMsg('c').u32(xid).u8(0).u64(9).u64(10).u64(srcTS))
	}

	// A spool whose file is gone before anything was buffered: the seek
	// back fails.
	if err := d.handle(newMsg('S').u32(1).u8(1)); err != nil {
		t.Fatal(err)
	}
	d.spools[1].f.Close()
	if err := commit(1); err == nil || !strings.Contains(err.Error(), "spool") {
		t.Fatalf("seek: %v", err)
	}

	// A message too large for the buffer once the file is gone.
	d.handle(newMsg('S').u32(2).u8(1))
	d.spools[2].f.Close()
	big := newMsg('I').xid(2).u32(1).u8('N').tuple(strings.Repeat("x", 70<<10), "x", nil)
	if err := d.handle(big); err == nil || !strings.Contains(err.Error(), "spool") {
		t.Fatalf("write: %v", err)
	}

	// A spool cut inside a length word.
	d.handle(newMsg('S').u32(3).u8(1))
	d.handle(newMsg('I').xid(3).u32(1).u8('N').tuple("1", "x", nil))
	sp := d.spools[3]
	sp.w.Flush()
	sp.f.Truncate(2)
	if err := commit(3); err == nil || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("short length: %v", err)
	}
}

// sqlEdit is a snapshot connection whose answers a test changes.
type sqlEdit struct {
	sqlConn
	edit func(sql string, res []pgwire.Result, err error) ([]pgwire.Result, error)
}

func (c sqlEdit) SimpleQuery(ctx context.Context, sql string) ([]pgwire.Result, error) {
	res, err := c.sqlConn.SimpleQuery(ctx, sql)
	return c.edit(sql, res, err)
}

func TestSnapshotMoreErrors(t *testing.T) {
	ctx := context.Background()
	params := map[string]any{"snapshot": "initial", "chunk_rows": 10}
	editSQL := func(t *testing.T, edit func(sql string, res []pgwire.Result, err error) ([]pgwire.Result, error)) {
		inner := dialSQL
		dialSQL = func(ctx context.Context, cfg pgwire.Config) (sqlConn, error) {
			c, err := inner(ctx, cfg)
			if err != nil {
				return nil, err
			}
			return sqlEdit{c, edit}, nil
		}
		t.Cleanup(func() { dialSQL = inner })
	}
	for _, c := range []struct {
		name string
		edit func(t *testing.T, db *snapDB, main *srcFake)
		want string
	}{
		{"set refused", func(t *testing.T, db *snapDB, main *srcFake) {
			// A reader's SET TRANSACTION fails for another reason than a
			// snapshot gone: that error, not errSnapshotGone.
			editSQL(t, func(sql string, res []pgwire.Result, err error) ([]pgwire.Result, error) {
				if strings.HasPrefix(sql, "SET TRANSACTION SNAPSHOT") {
					return nil, &pgwire.Error{Severity: "ERROR", Code: "25001", Message: "a transaction is under way"}
				}
				return res, err
			})
		}, "a transaction is under way"},
		{"row width", func(t *testing.T, db *snapDB, main *srcFake) {
			editSQL(t, func(sql string, res []pgwire.Result, err error) ([]pgwire.Result, error) {
				if strings.HasPrefix(sql, "FETCH") && len(res) > 0 {
					res[0].Fields = res[0].Fields[:2]
				}
				return res, err
			})
		}, "columns for 3"},
		{"plan begin", func(t *testing.T, db *snapDB, main *srcFake) { db.fail = "; SET TRANSACTION SNAPSHOT" }, "query failed"},
		{"export slot", func(t *testing.T, db *snapDB, main *srcFake) {
			inner := dialSource
			dialSource = func(ctx context.Context, cfg pgwire.Config) (replConn, error) {
				c, err := inner(ctx, cfg)
				if f, ok := c.(*srcFake); ok && f != main {
					f.slotErr = errors.New("no slots left")
				}
				return c, err
			}
			t.Cleanup(func() { dialSource = inner })
		}, "a temporary slot for the snapshot: no slots left"},
	} {
		t.Run(c.name, func(t *testing.T) {
			db := newSnapDB(30)
			main := newSrcFake(db.cat.answer)
			main.msgs <- errors.New("the stream broke")
			db.useFakes(t, main)
			c.edit(t, db, main)
			s := srcMust(t, params)
			defer s.Cleanup()
			err := s.Run(ctx, &fitsync.Run{Emit: func(*fitsync.Record) error { return nil }})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("%v, want %q", err, c.want)
			}
			if errors.Is(err, errSnapshotGone) {
				t.Fatalf("%v: not a snapshot gone", err)
			}
		})
	}

	// A held exporter that is not the checkpoint's goes; a new snapshot is
	// read.
	db0 := newSnapDB(3)
	main0 := newSrcFake(db0.cat.answer)
	main0.msgs <- errors.New("the stream broke")
	db0.useFakes(t, main0)
	s0 := srcMust(t, params)
	defer s0.Cleanup()
	other := newSrcFake(nil)
	s0.hold(&exporter{id: "other", conn: other})
	n := 0
	if err := s0.Run(ctx, &fitsync.Run{Emit: func(*fitsync.Record) error { n++; return nil }}); err == nil || err.Error() != "the stream broke" || n != 6 {
		t.Fatalf("%v, %d records", err, n)
	}
	<-other.closed

	// The held exporter is the checkpoint's, but its chunks done do not
	// fit its plan: the run fails and the exporter goes.
	db := newSnapDB(5)
	main := newSrcFake(db.cat.answer)
	db.useFakes(t, main)
	s := srcMust(t, params)
	defer s.Cleanup()
	held := newSrcFake(nil)
	s.hold(&exporter{id: "x", conn: held, plan: []snapChunk{{}}})
	from := fitsync.Position(`{"lsn":"0/1","snapshot":{"id":"x","tables":[{"name":"public.t","chunks":1}],"done":"5"}}`)
	if err := s.Run(ctx, &fitsync.Run{From: from, Emit: func(*fitsync.Record) error { return nil }}); err == nil || !strings.Contains(err.Error(), "chunks done") {
		t.Fatal(err)
	}
	<-held.closed
}
