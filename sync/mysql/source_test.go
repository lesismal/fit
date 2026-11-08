package syncmysql

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	fitsync "github.com/lesismal/fit/sync"
	"github.com/lesismal/fit/sync/mysql/binlog"
	mysqlwire "github.com/lesismal/fit/sync/mysql/wire"
)

func init() {
	retryBackoff = time.Millisecond
}

// recString is a record as op ns key after/before, its transaction's flags
// and its position.
func recString(r *fitsync.Record) string {
	key, _ := fitsync.AppendPlain(nil, r.Key)
	row := r.After
	if r.Op == fitsync.OpDelete {
		row = r.Before
	}
	val, _ := fitsync.AppendPlain(nil, row)
	s := r.Op.String() + " " + r.Namespace + " " + string(key) + " " + string(val) + " " + r.Tx.ID
	if r.Tx.First {
		s += " first"
	}
	if r.Tx.Last {
		s += " last " + string(r.Position)
	}
	return s
}

func recStrings(recs []*fitsync.Record) []string {
	var out []string
	for _, r := range recs {
		out = append(out, recString(r))
	}
	return out
}

func checkRecs(t *testing.T, got []*fitsync.Record, want ...string) {
	t.Helper()
	g := recStrings(got)
	if strings.Join(g, "\n") != strings.Join(want, "\n") {
		t.Fatalf("records:\n%s\nwant:\n%s", strings.Join(g, "\n"), strings.Join(want, "\n"))
	}
}

var fullNames = []string{"id", "v"}

func TestSourceStreamGTID(t *testing.T) {
	f := newSrcFake()
	st := newStream("bin.000001", 157, "8.4.3-log").
		gtid(uuidA, 6).query("shop", "BEGIN").table(10, "shop", "t", fullNames, 0).
		rows(binlog.WriteRowsEventV2, 10, img(1, "a"), img(2, "b")).xid().
		gtid(uuidA, 7).query("shop", "ALTER TABLE t ADD COLUMN w INT").
		gtid(uuidA, 8).query("shop", "BEGIN").table(10, "shop", "t", fullNames, 0).
		rows(binlog.UpdateRowsEventV2, 10, img(1, "a"), img(1, "A"), img(2, "b"), img(3, "b")).
		rows(binlog.DeleteRowsEventV2, 10, img(3, "b")).xid().
		gtid(uuidA, 9).query("other", "BEGIN").table(11, "other", "x", fullNames, 0).
		rows(binlog.WriteRowsEventV2, 11, img(1, "x")).xid().
		gtid(uuidA, 10).query("other", "CREATE DATABASE other2").
		gtid(uuidA, 11).query("shop", "BEGIN").table(12, "shop", "nokey", fullNames).
		rows(binlog.WriteRowsEventV2, 12, img(5, "\x00null")).query("shop", "COMMIT").
		heartbeat()
	f.streams = [][]evItem{st.items}
	s := newSource(t, f, map[string]any{"tables": []string{"shop.*"}})
	r := startSrc(s, nil, false)
	recs := r.take(t, 8)
	got := recStrings(recs)
	want := []string{
		`insert shop.t {"id":1} {"id":1,"v":"a"} ` + uuidA + `:6 first`,
		`insert shop.t {"id":2} {"id":2,"v":"b"} ` + uuidA + `:6 last`,
		`ddl shop.t null {"statement":"ALTER TABLE t ADD COLUMN w INT","database":"shop"} ` + uuidA + `:7 first last`,
		`update shop.t {"id":1} {"id":1,"v":"A"} ` + uuidA + `:8 first`,
		`delete shop.t {"id":2} {"id":2,"v":"b"} ` + uuidA + `:8`,
		`insert shop.t {"id":3} {"id":3,"v":"b"} ` + uuidA + `:8`,
		`delete shop.t {"id":3} {"id":3,"v":"b"} ` + uuidA + `:8 last`,
		`insert shop.nokey null {"id":5,"v":null} ` + uuidA + `:11 first last`,
	}
	for i := range want {
		if !strings.HasPrefix(got[i], want[i]) {
			t.Fatalf("record %d: %s\nwant %s", i, got[i], want[i])
		}
	}
	for i, gtids := range map[int]string{1: "1-6", 2: "1-7", 6: "1-8", 7: "1-11"} {
		if p := string(recs[i].Position); !strings.Contains(p, `"gtids":"`+uuidA+":"+gtids+`"`) || !strings.Contains(p, `"file":"bin.000001"`) {
			t.Fatalf("record %d's position %s", i, p)
		}
	}
	if !recs[0].CommitTime.Equal(time.UnixMicro(1700000000123456)) {
		t.Fatal(recs[0].CommitTime)
	}
	select {
	case l := <-r.lags:
		if l.Behind != 0 || l.Unit != fitsync.LagBytes {
			t.Fatal(l)
		}
	case <-time.After(time.Minute):
		t.Fatal("no lag after a heartbeat")
	}
	if err := r.stop(t); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	d := f.dumpList()
	if len(d) != 1 || d[0][0] != mysqlwire.ComBinlogDumpGTID || binary.LittleEndian.Uint16(d[0][1:]) != dumpThroughGTID ||
		binary.LittleEndian.Uint32(d[0][3:]) != 4101 {
		t.Fatalf("dump %x", d)
	}
	g, _ := parseGTIDSet(uuidA + ":1-5")
	if !strings.HasSuffix(string(d[0]), string(g.encode())) {
		t.Fatalf("dump %x", d[0])
	}
	if f.cmds[0][0] != mysqlwire.ComRegisterSlave || binary.LittleEndian.Uint32(f.cmds[0][1:]) != 4101 {
		t.Fatalf("register %x", f.cmds[0])
	}
	if e := f.execList(); !strings.Contains(e[0], "@master_heartbeat_period = 10000000000") {
		t.Fatal(e)
	}
	if f.open != 0 {
		t.Fatalf("%d connections left open", f.open)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// TestSourceInformationSchema reads names, signedness, character sets, enum
// values and keys from information_schema for table maps without them, and
// again after a DDL.
func TestSourceInformationSchema(t *testing.T) {
	f := newSrcFake()
	cols := [][]any{{[]byte("id"), []byte("int"), []byte("int(10) unsigned"), nil}, {[]byte("v"), []byte("varbinary"), []byte("varbinary(20)"), nil}}
	reads := 0
	f.query = func(q string, args []any) ([][]any, error, bool) {
		switch {
		case strings.Contains(q, "information_schema.COLUMNS"):
			reads++
			if reads == 2 {
				return cols[:1], nil, true
			}
			return cols, nil, true
		case strings.Contains(q, "information_schema.STATISTICS"):
			return [][]any{{[]byte("ID")}}, nil, true
		}
		return nil, nil, false
	}
	st := newStream("bin.000001", 157, "8.0.40").
		gtid(uuidA, 6).query("shop", "BEGIN").table(10, "shop", "t", nil).
		rows(binlog.WriteRowsEventV2, 10, img(1, "a")).xid().
		gtid(uuidA, 7).query("shop", "BEGIN").table(10, "shop", "t", nil).
		rows(binlog.WriteRowsEventV2, 10, img(2, "b")).xid().
		gtid(uuidA, 8).query("", "DROP TABLE `shop`.`t`").
		gtid(uuidA, 9).query("shop", "BEGIN").table(10, "shop", "t", nil).
		rows(binlog.WriteRowsEventV2, 10, img(3, "c")).xid()
	f.streams = [][]evItem{st.items}
	s := newSource(t, f, nil)
	r := startSrc(s, nil, false)
	recs := r.take(t, 4)
	r.stop(t)
	if v, _ := recs[0].After.Get("id"); v.Kind() != fitsync.KindUint {
		t.Fatalf("id is %v", v.Kind())
	}
	if v, _ := recs[0].After.Get("v"); v.Kind() != fitsync.KindBytes {
		t.Fatalf("v is %v", v.Kind())
	}
	if k, _ := fitsync.AppendPlain(nil, recs[1].Key); string(k) != `{"id":2}` {
		t.Fatal(string(k))
	}
	if recs[2].Op != fitsync.OpDDL || recs[2].Namespace != "shop.t" {
		t.Fatal(recString(recs[2]))
	}
	if _, ok := recs[3].After.Get("@2"); !ok || reads != 2 {
		t.Fatalf("%s after %d reads", recString(recs[3]), reads)
	}
}

func TestSourceInformationSchemaErrors(t *testing.T) {
	for name, q := range map[string]func(q string, args []any) ([][]any, error, bool){
		"columns": func(q string, args []any) ([][]any, error, bool) {
			return nil, errCut, strings.Contains(q, "COLUMNS")
		},
		"key": func(q string, args []any) ([][]any, error, bool) {
			return nil, errCut, strings.Contains(q, "STATISTICS")
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newSrcFake()
			f.query = func(qq string, args []any) ([][]any, error, bool) {
				if strings.Contains(qq, "information_schema") {
					if rows, err, ok := q(qq, args); ok {
						return rows, err, true
					}
					return nil, nil, true
				}
				return nil, nil, false
			}
			f.streams = [][]evItem{newStream("bin.000001", 157, "8.0.40").gtid(uuidA, 6).query("shop", "BEGIN").table(10, "shop", "t", nil).items}
			s := newSource(t, f, nil)
			if err := startSrc(s, nil, false).wait(t); !errors.Is(err, errCut) {
				t.Fatal(err)
			}
		})
	}
	t.Run("dial", func(t *testing.T) {
		f := newSrcFake()
		f.dialErr = func(n int) error {
			if n == 2 {
				return errCut
			}
			return nil
		}
		f.streams = [][]evItem{newStream("bin.000001", 157, "8.0.40").gtid(uuidA, 6).query("shop", "BEGIN").table(10, "shop", "t", nil).items}
		s := newSource(t, f, nil)
		if err := startSrc(s, nil, false).wait(t); !errors.Is(err, errCut) {
			t.Fatal(err)
		}
	})
}

func TestSourceKeysParam(t *testing.T) {
	f := newSrcFake()
	f.streams = [][]evItem{newStream("bin.000001", 157, "8.4.3").
		gtid(uuidA, 6).query("shop", "BEGIN").table(10, "shop", "t", fullNames, 0).
		rows(binlog.WriteRowsEventV2, 10, img(1, "a")).xid().
		gtid(uuidA, 7).query("shop", "BEGIN").table(11, "shop", "u", fullNames, 0).items}
	s := newSource(t, f, map[string]any{"keys": map[string]any{"shop.t": []string{"v"}, "shop.u": []string{"nope"}}})
	r := startSrc(s, nil, false)
	recs := r.take(t, 1)
	if k, _ := fitsync.AppendPlain(nil, recs[0].Key); string(k) != `{"v":"a"}` {
		t.Fatal(string(k))
	}
	if err := r.wait(t); err == nil || !strings.Contains(err.Error(), `"nope"`) {
		t.Fatal(err)
	}
}

// TestSourceFileResume resumes from a checkpoint's file and offset on a
// server without GTIDs, in a job: the stream ends at the binlog's end.
func TestSourceFileResume(t *testing.T) {
	f := newSrcFake()
	f.version = "5.7.44-log"
	f.answers["SELECT @@GLOBAL.gtid_mode"] = [][]any{{[]byte("OFF")}}
	st := newStream("bin.000002", 400, "5.7.44-log").
		add(binlog.AnonymousGTIDEvent, cat([]byte{0}, make([]byte, 24))).query("shop", "BEGIN").table(10, "shop", "t", fullNames, 0).
		rows(binlog.WriteRowsEventV2, 10, img(1, "a")).xid()
	st.items = append(st.items, evItem{err: io.EOF})
	f.streams = [][]evItem{st.items}
	s := newSource(t, f, nil)
	r := startSrc(s, fitsync.Position(`{"file":"bin.000002","pos":400}`), true)
	recs := r.take(t, 1)
	if err := r.wait(t); err != nil {
		t.Fatal(err)
	}
	if p := string(recs[0].Position); p != `{"file":"bin.000002","pos":`+itoa(int(st.pos))+`}` {
		t.Fatal(p)
	}
	if !strings.HasPrefix(recs[0].Tx.ID, "bin.000002:400") {
		t.Fatal(recs[0].Tx.ID)
	}
	d := f.dumpList()[0]
	if d[0] != mysqlwire.ComBinlogDump || binary.LittleEndian.Uint32(d[1:]) != 400 || binary.LittleEndian.Uint16(d[5:]) != dumpNonBlock || string(d[11:]) != "bin.000002" {
		t.Fatalf("dump %x", d)
	}
}

// TestSourceMariaDB follows MariaDB by its GTID position.
func TestSourceMariaDB(t *testing.T) {
	f := newSrcFake()
	f.version, f.mariadb = "11.4.2-MariaDB-log", true
	st := newStream("bin.000001", 300, "11.4.2-MariaDB-log").
		mariaGTID(6, 0, 0).table(10, "shop", "t", fullNames, 0).rows(binlog.WriteRowsEventV2, 10, img(1, "a")).xid().
		mariaGTID(7, 0, binlog.MariaFlagStandalone).query("shop", "CREATE TABLE u (id INT)").
		mariaGTID(3, 1, 0).table(10, "shop", "t", fullNames, 0).rows(binlog.WriteRowsEventV2, 10, img(2, "b")).xid()
	f.streams = [][]evItem{st.items}
	s := newSource(t, f, nil)
	r := startSrc(s, fitsync.Position(`{"gtids":"0-1-5","file":"bin.000001","pos":4}`), false)
	recs := r.take(t, 3)
	r.stop(t)
	for i, w := range []struct{ tx, gtids string }{{"0-1-6", "0-1-6"}, {"0-1-7", "0-1-7"}, {"1-1-3", "0-1-7,1-1-3"}} {
		if recs[i].Tx.ID != w.tx || !strings.Contains(string(recs[i].Position), `"gtids":"`+w.gtids+`"`) {
			t.Fatalf("record %d: %s", i, recString(recs[i]))
		}
	}
	if recs[1].Op != fitsync.OpDDL || recs[1].Namespace != "shop.u" {
		t.Fatal(recString(recs[1]))
	}
	e := f.execList()
	if !strings.Contains(e[0], "@mariadb_slave_capability = 4") || !strings.Contains(e[1], "@slave_connect_state = '0-1-5'") {
		t.Fatal(e)
	}
	if d := f.dumpList()[0]; d[0] != mysqlwire.ComBinlogDump || len(d) != 11 {
		t.Fatalf("dump %x", d)
	}
}
