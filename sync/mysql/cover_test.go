package syncmysql

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	fitsync "github.com/lesismal/fit/sync"
	"github.com/lesismal/fit/sync/mysql/binlog"
	mysqlwire "github.com/lesismal/fit/sync/mysql/wire"
)

// TestWireSrc goes through the real client to a fake server: the source's
// connection reads a result's columns and rows, copied, and fails once
// closed or with nothing listening.
func TestWireSrc(t *testing.T) {
	addr := fakeServer(t, "8.4.3")
	c, err := dialSrc(ctxT(t), mysqlwire.Config{Addr: addr, User: "u"})
	if err != nil {
		t.Fatal(err)
	}
	cols, rows, err := c.QueryAll(ctxT(t), "SELECT 'row'")
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 1 || cols[0].Name != "c" || len(rows) != 1 || len(rows[0]) != 1 {
		t.Fatalf("%v %v", cols, rows)
	}
	b, ok := rows[0][0].([]byte)
	if !ok || string(b) != "row" {
		t.Fatalf("%#v", rows[0][0])
	}
	// A second query reuses the client's buffers: the first rows are copies.
	if _, _, err := c.QueryAll(ctxT(t), "SELECT 'row'"); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, []byte("row")) {
		t.Fatalf("%q", b)
	}
	// A statement answered by OK: no columns, no rows.
	cols, rows, err = c.QueryAll(ctxT(t), "DO 1")
	if err != nil || len(cols) != 0 || len(rows) != 0 {
		t.Fatalf("%v %v %v", cols, rows, err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.QueryAll(ctxT(t), "SELECT 'row'"); !errors.Is(err, mysqlwire.ErrClosed) {
		t.Fatal(err)
	}
	if _, err := dialSrc(ctxT(t), mysqlwire.Config{Addr: "127.0.0.1:1", User: "u"}); err == nil {
		t.Fatal("no error")
	}
}

func TestStr(t *testing.T) {
	for v, want := range map[any]string{nil: "", int64(-3): "-3", uint64(7): "7", "s": "s"} {
		if got := str(v); got != want {
			t.Errorf("str(%#v) = %q, want %q", v, got, want)
		}
	}
	if got := str([]byte("b")); got != "b" {
		t.Error(got)
	}
}

var errEmit = errors.New("emit refused")

// runFailing runs s until its n-th record (from 1), which the pipeline
// refuses, and returns the source's error and the records it took.
func runFailing(t *testing.T, s *Source, from fitsync.Position, n int) ([]*fitsync.Record, error) {
	t.Helper()
	var got []*fitsync.Record
	ctx := ctxT(t)
	err := s.Run(ctx, &fitsync.Run{From: from, Emit: func(rec *fitsync.Record) error {
		got = append(got, rec)
		if len(got) == n {
			return errEmit
		}
		return nil
	}, Lag: func(fitsync.SourceLag) {}})
	return got, err
}

// TestSourceEmitRefused: the pipeline refusing a record ends the source
// with its error, wherever the record is handed on.
func TestSourceEmitRefused(t *testing.T) {
	tx := func(s *stream) *stream {
		return s.gtid(uuidA, 6).query("shop", "BEGIN").table(10, "shop", "t", fullNames, 0)
	}
	inner := newStream("", 0, "")
	inner.crc = false
	inner.items = nil
	inner.query("shop", "BEGIN").table(10, "shop", "t", fullNames, 0).rows(binlog.WriteRowsEventV2, 10, img(1, "a"), img(2, "b")).xid()
	var data []byte
	for _, it := range inner.items {
		data = append(data, it.ev...)
	}
	payload := cat([]byte{1}, lenenc(len(lenenc(len(data)))), lenenc(len(data)), []byte{2, 3, 0xfc, 0xff, 0}, []byte{0}, data)
	for name, c := range map[string]struct {
		st   *stream
		n    int
		want int
	}{
		// The first of two rows, handed on when the second is read.
		"held": {tx(newStream("bin.000001", 157, "8.4.3")).rows(binlog.WriteRowsEventV2, 10, img(1, "a"), img(2, "b")).xid(), 1, 1},
		// The last row, handed on at the transaction's end.
		"last": {tx(newStream("bin.000001", 157, "8.4.3")).rows(binlog.WriteRowsEventV2, 10, img(1, "a")).xid(), 1, 1},
		// A row handed on when a DDL inside the transaction is read.
		"ddl": {tx(newStream("bin.000001", 157, "8.4.3")).rows(binlog.WriteRowsEventV2, 10, img(1, "a")).
			query("shop", "ALTER TABLE t ADD COLUMN w INT").xid(), 1, 1},
		// A row of a compressed transaction.
		"payload": {newStream("bin.000001", 157, "8.4.3").gtid(uuidA, 6).add(binlog.TransactionPayload, payload), 1, 1},
	} {
		t.Run(name, func(t *testing.T) {
			f := newSrcFake()
			f.streams = [][]evItem{c.st.items}
			got, err := runFailing(t, newSource(t, f, nil), nil, c.n)
			if !errors.Is(err, errEmit) || len(got) != c.want {
				t.Fatalf("%v after %d records", err, len(got))
			}
			if got[0].Op != fitsync.OpInsert || got[0].Namespace != "shop.t" {
				t.Fatal(recString(got[0]))
			}
		})
	}
	// The DDL inside a transaction is held, not ended there: it is the
	// transaction's last record, at its end.
	f := newSrcFake()
	f.streams = [][]evItem{tx(newStream("bin.000001", 157, "8.4.3")).rows(binlog.WriteRowsEventV2, 10, img(1, "a")).
		query("shop", "ALTER TABLE t ADD COLUMN w INT").xid().items}
	r := startSrc(newSource(t, f, nil), nil, false)
	recs := r.take(t, 2)
	r.stop(t)
	if recs[0].Tx.Last || recs[1].Op != fitsync.OpDDL || !recs[1].Tx.Last || recs[1].Tx.ID != recs[0].Tx.ID {
		t.Fatal(recStrings(recs))
	}
}

// TestSourceMariaDBFromNow starts a MariaDB stream at the server's GTID
// position, read from gtid_binlog_pos.
func TestSourceMariaDBFromNow(t *testing.T) {
	f := newSrcFake()
	f.version, f.mariadb = "11.4.2-MariaDB-log", true
	f.answers["SELECT @@GLOBAL.gtid_binlog_pos"] = [][]any{{[]byte("1-1-9,0-1-5")}}
	st := newStream("bin.000001", 300, "11.4.2-MariaDB-log").
		mariaGTID(6, 0, 0).table(10, "shop", "t", fullNames, 0).rows(binlog.WriteRowsEventV2, 10, img(1, "a")).xid()
	f.streams = [][]evItem{st.items}
	r := startSrc(newSource(t, f, nil), nil, false)
	recs := r.take(t, 1)
	r.stop(t)
	if !strings.Contains(string(recs[0].Position), `"gtids":"0-1-6,1-1-9"`) {
		t.Fatal(recString(recs[0]))
	}
	e := f.execList()
	if len(e) < 2 || !strings.Contains(e[1], "@slave_connect_state = '0-1-5,1-1-9'") {
		t.Fatal(e)
	}
	// A position that does not parse.
	f = newSrcFake()
	f.version, f.mariadb = "11.4.2-MariaDB-log", true
	f.answers["SELECT @@GLOBAL.gtid_binlog_pos"] = [][]any{{[]byte("not-a-gtid")}}
	if err := startSrc(newSource(t, f, nil), nil, false).wait(t); err == nil {
		t.Fatal("no error")
	}
}

// TestSourceStartErrors: a checkpoint inside a snapshot whose connection
// fails, and a registration the server refuses, end the source.
func TestSourceStartErrors(t *testing.T) {
	f := newSrcFake()
	f.dialErr = func(int) error { return errCut }
	err := startSrc(newSource(t, f, nil), fitsync.Position(`{"file":"bin.000001","pos":4,"snapshot":{"table":"shop.t"}}`), false).wait(t)
	if !errors.Is(err, errCut) {
		t.Fatal(err)
	}
	f = newSrcFake()
	f.cmdErr = &mysqlwire.Error{Code: 1045, Message: "denied"}
	err = startSrc(newSource(t, f, nil), nil, false).wait(t)
	var se *mysqlwire.Error
	if !errors.As(err, &se) || se.Code != 1045 {
		t.Fatal(err)
	}
	if f.dials > 2 { // the stream's and the lag's, never again
		t.Fatalf("%d dials", f.dials)
	}
	// A cut registration is the connection's: read again, then given up.
	f = newSrcFake()
	f.cmdErr = errCut
	if err := startSrc(newSource(t, f, nil), nil, false).wait(t); !errors.Is(err, errCut) {
		t.Fatal(err)
	}
	if f.dials < 1+maxRetries {
		t.Fatalf("%d dials", f.dials)
	}
}

// TestSinkFlushFails: the statements before a bad record, or before the
// next source transaction, fail to run: the error is the connection's,
// not the record's, and the batch passes once the server does.
func TestSinkFlushFails(t *testing.T) {
	boom := errors.New("boom")
	for name, recs := range map[string][]*fitsync.Record{
		"bad record":       {rec("t", fitsync.OpInsert, 1, "a"), {Namespace: "t", Op: 99}},
		"next transaction": append(inTx("x", rec("t", fitsync.OpInsert, 1, "a")), inTx("y", rec("t", fitsync.OpInsert, 2, "b"))...),
	} {
		t.Run(name, func(t *testing.T) {
			d := newFakeDB(map[string][]string{"t": {"id"}})
			s := fakeSink(t, d, nil)
			d.fail = func(q string, args []any) error {
				if strings.HasPrefix(q, "INSERT") {
					return boom
				}
				return nil
			}
			err := s.Write(ctxT(t), recs)
			if !errors.Is(err, boom) || fitsync.IsPermanent(err) {
				t.Fatal(err)
			}
			d.fail = nil
			err = s.Write(ctxT(t), recs)
			if name == "bad record" {
				var be *fitsync.BatchError
				if !errors.As(err, &be) || be.Index != 1 {
					t.Fatal(err)
				}
				want(t, d, "t", "id=1,v=a")
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want(t, d, "t", "id=1,v=a", "id=2,v=b")
		})
	}
}

// TestSinkUpdateBeforeWithoutKey: an update whose row before lacks the
// key's columns is written as the row it is, nothing deleted.
func TestSinkUpdateBeforeWithoutKey(t *testing.T) {
	d := newFakeDB(map[string][]string{"t": {"id"}})
	s := fakeSink(t, d, nil)
	writeAll(t, s, []*fitsync.Record{rec("t", fitsync.OpInsert, 1, "a")}, 1)
	up := &fitsync.Record{Namespace: "t", Op: fitsync.OpUpdate, Key: idKey(2), After: row(2, "b"),
		Before: fitsync.Map(fitsync.Field{Name: "v", Value: fitsync.String("a")})}
	writeAll(t, s, []*fitsync.Record{up}, 1)
	want(t, d, "t", "id=1,v=a", "id=2,v=b")
	for _, q := range d.statements() {
		if strings.HasPrefix(q, "DELETE") {
			t.Fatal(d.statements())
		}
	}
}
