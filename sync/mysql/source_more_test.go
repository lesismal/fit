package syncmysql

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	fitsync "github.com/lesismal/fit/sync"
	"github.com/lesismal/fit/sync/mysql/binlog"
	mysqlwire "github.com/lesismal/fit/sync/mysql/wire"
)

func TestSourceProvisionErrors(t *testing.T) {
	f := newSrcFake()
	for name, p := range map[string]map[string]any{
		"server_id": {"server_id": 0},
		"tables":    {"tables": []string{"nodot"}},
		"exclude":   {"exclude": []string{"a.[b"}},
		"keys":      {"keys": map[string]any{"t": []string{"a"}}},
		"no key":    {"keys": map[string]any{"a.t": []string{}}},
		"heartbeat": {"heartbeat": "2m"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := provisionSource(t, f, p); err == nil {
				t.Fatal("no error")
			}
		})
	}
	s := newSource(t, f, map[string]any{"tables": []string{"shop.*", "crm.users"}, "exclude": []string{"shop.tmp_*"}})
	for ns, want := range map[string]bool{"shop.a": true, "shop.tmp_x": false, "crm.users": true, "crm.x": false, "mysql.user": false} {
		if s.included(ns) != want {
			t.Errorf("included(%s)", ns)
		}
	}
	for db, want := range map[string]bool{"shop": true, "crm": true, "x": false, "": false} {
		if s.includesDB(db) != want {
			t.Errorf("includesDB(%s)", db)
		}
	}
	all := newSource(t, f, nil)
	if all.included("mysql.user") || !all.included("a.b") || all.includesDB("sys") || !all.includesDB("a") {
		t.Fatal("the default tables")
	}
}

func TestSourceTest(t *testing.T) {
	q := "SELECT @@GLOBAL.log_bin, @@GLOBAL.binlog_format, @@GLOBAL.binlog_row_image, @@GLOBAL.binlog_checksum"
	for name, c := range map[string]struct {
		row  []any
		gtid bool
		want string
	}{
		"ok":      {[]any{int64(1), []byte("ROW"), []byte("FULL"), []byte("NONE")}, true, ""},
		"log_bin": {[]any{int64(0), []byte("ROW"), []byte("FULL"), []byte("NONE")}, true, "log_bin"},
		"format":  {[]any{int64(1), []byte("MIXED"), []byte("FULL"), []byte("NONE")}, true, "MIXED"},
		"image":   {[]any{int64(1), []byte("ROW"), []byte("MINIMAL"), []byte("NONE")}, true, "MINIMAL"},
		"gtid":    {[]any{int64(1), []byte("ROW"), []byte("FULL"), []byte("NONE")}, false, "gtid_mode"},
		"none":    {nil, true, "no row"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newSrcFake()
			f.answers[q] = [][]any{c.row}
			if c.row == nil {
				f.answers[q] = nil
			}
			if !c.gtid {
				delete(f.answers, "SELECT @@GLOBAL.gtid_mode")
			}
			err := newSource(t, f, nil).Test(context.Background())
			if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
				t.Fatal(err)
			}
		})
	}
	f := newSrcFake()
	f.dialErr = func(int) error { return errCut }
	if err := newSource(t, f, nil).Test(context.Background()); !errors.Is(err, errCut) {
		t.Fatal(err)
	}
}

// TestSourceReconnect reads a transaction again after the stream is cut
// inside it, passing over the records handed on already, then gives up
// after maxRetries failed connections.
func TestSourceReconnect(t *testing.T) {
	f := newSrcFake()
	tx := func(s *stream) *stream {
		return s.gtid(uuidA, 6).query("shop", "BEGIN").table(10, "shop", "t", fullNames, 0).
			rows(binlog.WriteRowsEventV2, 10, img(1, "a"), img(2, "b"), img(3, "c"))
	}
	f.streams = [][]evItem{
		tx(newStream("bin.000001", 157, "8.4.3")).fail(errCut).items,
		tx(newStream("bin.000001", 157, "8.4.3")).fail(errCut).items,
		tx(newStream("bin.000001", 157, "8.4.3")).xid().fail(errCut).items,
	}
	f.dialErr = func(n int) error {
		if n > 3 {
			return errCut
		}
		return nil
	}
	s := newSource(t, f, nil)
	r := startSrc(s, nil, false)
	checkRecs(t, r.take(t, 3),
		`insert shop.t {"id":1} {"id":1,"v":"a"} `+uuidA+`:6 first`,
		`insert shop.t {"id":2} {"id":2,"v":"b"} `+uuidA+`:6`,
		`insert shop.t {"id":3} {"id":3,"v":"c"} `+uuidA+`:6 last {"gtids":"`+uuidA+`:1-6","file":"bin.000001","pos":`+itoa(int(tx(newStream("bin.000001", 157, "")).xid().pos))+`}`)
	if err := r.wait(t); !errors.Is(err, errCut) {
		t.Fatal(err)
	}
	d := f.dumpList()
	g, _ := parseGTIDSet(uuidA + ":1-5")
	if len(d) != 3 || !strings.HasSuffix(string(d[2]), string(g.encode())) {
		t.Fatalf("dumps %x", d)
	}
	if f.dials != 4+maxRetries { // the lag's connection too
		t.Fatalf("%d dials", f.dials)
	}
}

func TestSourceStreamErrors(t *testing.T) {
	base := func() *stream {
		return newStream("bin.000001", 157, "8.4.3").gtid(uuidA, 6).query("shop", "BEGIN").table(10, "shop", "t", fullNames, 0)
	}
	partial := cat(le(10, 6), le(binlog.RowsStmtEnd, 2), le(2, 2), lenenc(2), []byte{1}, []byte{0}, le(1, 4))
	for name, c := range map[string]struct {
		items []evItem
		want  string
	}{
		"purged":    {[]evItem{{err: &mysqlwire.Error{Code: 1236, Message: "purged"}}}, "new snapshot"},
		"refused":   {[]evItem{{err: &mysqlwire.Error{Code: 1045, Message: "denied"}}}, "denied"},
		"malformed": {[]evItem{{ev: []byte{1, 2, 3}}}, "malformed"},
		"statement": {base().query("shop", "INSERT INTO t VALUES (1)").items, "statement-based"},
		"partial":   {base().add(binlog.WriteRowsEventV2, partial).items, "binlog_row_image"},
		"outside":   {base().xid().rows(binlog.WriteRowsEventV2, 10, img(1, "a")).items, "outside a transaction"},
		"eof":       {append(base().items, evItem{err: io.EOF}), "inside a transaction"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newSrcFake()
			f.streams = [][]evItem{c.items}
			if name == "purged" || name == "refused" || name == "malformed" {
				f.streams = [][]evItem{append(newStream("bin.000001", 157, "8.4.3").items, c.items...)}
			}
			err := startSrc(newSource(t, f, nil), nil, false).wait(t)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatal(err)
			}
			if name == "purged" && !errors.Is(err, errNeedSnapshot) {
				t.Fatal(err)
			}
		})
	}
}

// TestSourceStatements passes over the statements that are neither DDL
// nor a transaction's bounds, and ends transactions at COMMIT and ROLLBACK.
func TestSourceStatements(t *testing.T) {
	f := newSrcFake()
	dummy := cat(le(1<<48-1, 6), le(binlog.RowsStmtEnd, 2), le(2, 2), lenenc(0))
	st := newStream("bin.000001", 157, "8.4.3").
		gtid(uuidA, 6).query("shop", "BEGIN").query("shop", "SAVEPOINT a").table(10, "shop", "t", fullNames, 0).
		rows(binlog.WriteRowsEventV2, 10, img(1, "a")).query("shop", "ROLLBACK TO SAVEPOINT a").
		query("shop", "XA END 'x'").query("shop", "# a comment").query("shop", "GRANT ALL ON *.* TO u").
		query("mysql", "INSERT INTO x VALUES (1)").add(binlog.WriteRowsEventV2, dummy).
		table(11, "mysql", "x", fullNames, 0).rows(binlog.WriteRowsEventV2, 11, img(1, "a")).query("shop", "COMMIT").
		query("shop", "BEGIN").rows(binlog.WriteRowsEventV2, 10, img(2, "b")).query("shop", "ROLLBACK").
		query("shop", "TRUNCATE TABLE t").query("shop", "CREATE DATABASE x").query("", "CREATE INDEX i ON shop.t (v)")
	f.streams = [][]evItem{st.items}
	r := startSrc(newSource(t, f, nil), nil, false)
	recs := r.take(t, 5)
	r.stop(t)
	if recs[0].Op != fitsync.OpInsert || !recs[0].Tx.Last || recs[1].Op != fitsync.OpInsert || !recs[1].Tx.Last {
		t.Fatal(recStrings(recs))
	}
	for i, ns := range []string{"shop.t", "shop", "shop.t"} {
		if r := recs[2+i]; r.Op != fitsync.OpDDL || r.Namespace != ns || !r.Tx.First || !r.Tx.Last || r.Position == nil {
			t.Fatal(recString(r))
		}
	}
}

// TestSourcePayload reads a transaction compressed in a payload, and
// reports how far behind the binlog's end it is.
func TestSourcePayload(t *testing.T) {
	saved := lagInterval
	lagInterval = 0
	t.Cleanup(func() { lagInterval = saved })
	f := newSrcFake()
	f.answers["SHOW BINARY LOGS"] = [][]any{{[]byte("bin.000000"), uint64(10)}, {[]byte("bin.000001"), uint64(5000)}, {[]byte("bin.000002"), uint64(100)}, {}}
	inner := newStream("", 0, "")
	inner.crc = false
	inner.items = nil
	inner.query("shop", "BEGIN").table(10, "shop", "t", fullNames, 0).rows(binlog.WriteRowsEventV2, 10, img(1, "a")).xid()
	var data []byte
	for _, it := range inner.items {
		data = append(data, it.ev...)
	}
	body := cat([]byte{1}, lenenc(len(lenenc(len(data)))), lenenc(len(data)), []byte{2, 3, 0xfc, 0xff, 0}, []byte{0}, data)
	st := newStream("bin.000001", 157, "8.4.3").gtid(uuidA, 6).add(binlog.TransactionPayload, body)
	f.streams = [][]evItem{st.items}
	r := startSrc(newSource(t, f, nil), nil, false)
	recs := r.take(t, 1)
	checkRecs(t, recs, `insert shop.t {"id":1} {"id":1,"v":"a"} `+uuidA+`:6 first last {"gtids":"`+uuidA+`:1-6","file":"bin.000001","pos":`+itoa(int(st.pos))+`}`)
	l := <-r.lags
	if l.Behind != int64(5000-st.pos+100) {
		t.Fatal(l)
	}
	r.stop(t)

	// SHOW BINARY LOGS refused: no lag, the source goes on.
	f = newSrcFake()
	f.streams = [][]evItem{newStream("bin.000001", 157, "8.4.3").gtid(uuidA, 6).add(binlog.TransactionPayload, body).
		gtid(uuidA, 7).add(binlog.TransactionPayload, body).items}
	f.query = func(q string, args []any) ([][]any, error, bool) { return nil, errCut, q == "SHOW BINARY LOGS" }
	r = startSrc(newSource(t, f, nil), nil, false)
	r.take(t, 2)
	r.stop(t)
	f = newSrcFake()
	f.dialErr = func(n int) error {
		if n > 1 {
			return errCut
		}
		return nil
	}
	f.streams = [][]evItem{newStream("bin.000001", 157, "8.4.3").gtid(uuidA, 6).add(binlog.TransactionPayload, body).items}
	r = startSrc(newSource(t, f, nil), nil, false)
	r.take(t, 1)
	r.stop(t)
}

func TestSourceStartPositions(t *testing.T) {
	t.Run("8.0", func(t *testing.T) {
		f := newSrcFake()
		f.version = "8.0.40"
		f.answers["SHOW MASTER STATUS"] = [][]any{{[]byte("bin.000003"), uint64(9), nil, nil, []byte(uuidB + ":1-3,\n" + uuidA + ":1-2")}}
		r := startSrc(newSource(t, f, nil), nil, false)
		for len(f.dumpList()) == 0 {
			runtimeGosched()
		}
		r.stop(t)
		g, _ := parseGTIDSet(uuidA + ":1-2," + uuidB + ":1-3")
		if d := f.dumpList()[0]; !strings.HasSuffix(string(d), string(g.encode())) {
			t.Fatalf("dump %x", d)
		}
	})
	t.Run("mariadb empty", func(t *testing.T) {
		f := newSrcFake()
		f.version, f.mariadb = "10.11.6-MariaDB", true
		f.answers["SELECT @@GLOBAL.gtid_binlog_pos"] = [][]any{{[]byte("")}}
		r := startSrc(newSource(t, f, nil), nil, false)
		for len(f.dumpList()) == 0 {
			runtimeGosched()
		}
		r.stop(t)
		if d := f.dumpList()[0]; d[0] != mysqlwire.ComBinlogDump || binary.LittleEndian.Uint32(d[1:]) != 157 || string(d[11:]) != "bin.000001" {
			t.Fatalf("dump %x", d)
		}
	})
	t.Run("tagged", func(t *testing.T) {
		f := newSrcFake()
		r := startSrc(newSource(t, f, nil), fitsync.Position(`{"gtids":"`+uuidA+`:tag:1-3","file":"bin.000004","pos":200}`), false)
		for len(f.dumpList()) == 0 {
			runtimeGosched()
		}
		r.stop(t)
		if d := f.dumpList()[0]; d[0] != mysqlwire.ComBinlogDump || string(d[11:]) != "bin.000004" {
			t.Fatalf("dump %x", d)
		}
	})
	for name, c := range map[string]struct {
		from  string
		setup func(f *srcFake)
		want  string
	}{
		"json":    {from: `{`, want: "checkpoint"},
		"empty":   {from: `{}`, want: "neither"},
		"gtids":   {from: `{"gtids":"zzz"}`, want: "not a GTID set"},
		"maria":   {from: `{"gtids":"0-x"}`, setup: func(f *srcFake) { f.mariadb = true }, want: "not a MariaDB GTID position"},
		"no file": {from: `{"gtids":"` + uuidA + `:1"}`, setup: func(f *srcFake) { f.answers["SELECT @@GLOBAL.gtid_mode"] = [][]any{{[]byte("OFF")}} }, want: "names no binlog file"},
		"status":  {setup: func(f *srcFake) { delete(f.answers, "SHOW BINARY LOG STATUS") }, want: "SHOW BINARY LOG STATUS"},
		"bad set": {setup: func(f *srcFake) {
			f.answers["SHOW BINARY LOG STATUS"] = [][]any{{[]byte("f"), uint64(4), nil, nil, []byte("x")}}
		}, want: "not a GTID set"},
		"maria pos": {setup: func(f *srcFake) {
			f.mariadb = true
			f.answers["SELECT @@GLOBAL.gtid_binlog_pos"] = [][]any{{[]byte("1-2")}}
		}, want: "MariaDB"},
		"maria gone": {setup: func(f *srcFake) {
			f.mariadb = true
			delete(f.answers, "SELECT @@GLOBAL.gtid_binlog_pos")
		}, want: "gtid_binlog_pos"},
		"exec": {setup: func(f *srcFake) { f.execErr = func(string) error { return errCut } }, want: "cut"},
		"exec gtid": {from: `{"gtids":"0-1-1"}`, setup: func(f *srcFake) {
			f.mariadb = true
			f.execErr = func(q string) error {
				if strings.Contains(q, "slave_connect_state") {
					return errCut
				}
				return nil
			}
		}, want: "cut"},
		"server": {setup: func(f *srcFake) { delete(f.answers, "SELECT @@GLOBAL.gtid_mode") }, want: "gtid_mode"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newSrcFake()
			if c.setup != nil {
				c.setup(f)
			}
			var from fitsync.Position
			if c.from != "" {
				from = fitsync.Position(c.from)
			}
			err := startSrc(newSource(t, f, nil), from, false).wait(t)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatal(err)
			}
		})
	}
}

func TestGTIDSets(t *testing.T) {
	g, err := parseGTIDSet(" " + strings.ToUpper(uuidB) + ":1-3:7:5,\n" + uuidA + ":4:tag:1-2:3,\t" + uuidA + ":1-3")
	if err != nil {
		t.Fatal(err)
	}
	if s := g.String(); s != uuidA+":1-4,"+uuidA+":tag:1-3,"+uuidB+":1-3:5:7" || !g.tagged() {
		t.Fatal(s)
	}
	g.add(uuidB, 4)
	g.add(uuidB, 6)
	if s := g.String(); !strings.HasSuffix(s, uuidB+":1-7") {
		t.Fatal(s)
	}
	if e, _ := parseGTIDSet(""); len(e) != 0 || e.String() != "" || e.tagged() {
		t.Fatal(e)
	}
	u, _ := parseGTIDSet(uuidA + ":1-2:5")
	enc := u.encode()
	if binary.LittleEndian.Uint64(enc) != 1 || binary.LittleEndian.Uint64(enc[24:]) != 2 ||
		binary.LittleEndian.Uint64(enc[32:]) != 1 || binary.LittleEndian.Uint64(enc[40:]) != 3 || binary.LittleEndian.Uint64(enc[48:]) != 5 {
		t.Fatalf("%x", enc)
	}
	for _, bad := range []string{"x:1", uuidA + ":0", uuidA + ":3-2", uuidA + ":a-b", uuidA + "::1", uuidA + ":" + strings.Repeat("t", 33) + ":1",
		uuidA + ":1-9223372036854775807", "3e11fa47-71ca-11e1-9e33-c80aa942956z:1"} {
		if _, err := parseGTIDSet(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
	m, err := parseMariaPos("1-2-3, 0-1-9,")
	if err != nil || m.String() != "0-1-9,1-2-3" {
		t.Fatal(m, err)
	}
	for _, bad := range []string{"1-2", "a-1-2", "4294967296-1-1"} {
		if _, err := parseMariaPos(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestDDLTables(t *testing.T) {
	for sql, want := range map[string]string{
		"CREATE TABLE t (id INT)":                         "db.t",
		"create table if not exists `a b`.`c``d` (x int)": "a b.c`d",
		"ALTER ONLINE TABLE x.y ADD z INT":                "x.y",
		"DROP TEMPORARY TABLE IF EXISTS t1, t2":           "db.t1",
		"RENAME TABLE a TO b":                             "db.a",
		"TRUNCATE TABLE  s . t":                           "s.t",
		"CREATE UNIQUE INDEX i ON t (a)":                  "db.t",
		"DROP INDEX `i` ON `s`.`t`":                       "s.t",
		"CREATE DATABASE x":                               "",
		"CREATE VIEW v AS SELECT 1":                       "",
	} {
		got, ok := ddlTable(sql, "db")
		if got != want || ok != (want != "") {
			t.Errorf("%q: %q %v", sql, got, ok)
		}
	}
	if _, ok := ddlTable("CREATE TABLE t (x INT)", ""); ok {
		t.Error("a table without a database")
	}
	for ct, want := range map[string]string{
		"enum('a','b''c','d\\\\e')": "a|b'c|d\\e",
		"set('x')":                  "x",
		"enum":                      "",
		"enum(x)":                   "",
		"enum('a'":                  "a",
		"enum('a'x)":                "a",
	} {
		if got := strings.Join(enumValues(ct), "|"); got != want {
			t.Errorf("%s: %q", ct, got)
		}
	}
	if firstWord("BEGIN") != "BEGIN" || firstWord("/* x */ ALTER") != "#" || firstWord("ALTER TABLE") != "ALTER" {
		t.Error("firstWord")
	}
}

// TestSourceEnumsFromInformationSchema completes ENUM and SET columns.
func TestSourceEnumsFromInformationSchema(t *testing.T) {
	f := newSrcFake()
	f.query = func(q string, args []any) ([][]any, error, bool) {
		switch {
		case strings.Contains(q, "COLUMNS"):
			return [][]any{{[]byte("e"), []byte("enum"), []byte("enum('a','b')"), []byte("utf8mb4_bin")},
				{[]byte("s"), []byte("set"), []byte("set('x','y')"), []byte("utf8mb4_bin")}}, nil, true
		case strings.Contains(q, "STATISTICS"):
			return nil, nil, true
		}
		return nil, nil, false
	}
	types := []byte{binlog.TypeString, binlog.TypeString}
	meta := []byte{binlog.TypeEnum, 1, binlog.TypeSet, 1}
	tm := cat(le(10, 6), le(1, 2), []byte{4}, []byte("shop"), []byte{0}, []byte{1}, []byte("t"), []byte{0},
		lenenc(2), types, lenenc(len(meta)), meta, []byte{0})
	rows := cat(le(10, 6), le(binlog.RowsStmtEnd, 2), le(2, 2), lenenc(2), []byte{3}, []byte{0, 2, 3})
	f.streams = [][]evItem{newStream("bin.000001", 157, "5.7.44").gtid(uuidA, 6).query("shop", "BEGIN").
		add(binlog.TableMapEvent, tm).add(binlog.WriteRowsEventV2, rows).xid().items}
	r := startSrc(newSource(t, f, nil), nil, false)
	recs := r.take(t, 1)
	r.stop(t)
	if v, _ := fitsync.AppendPlain(nil, recs[0].After); string(v) != `{"e":"b","s":"x,y"}` || !recs[0].Key.IsNull() {
		t.Fatal(string(v))
	}
}

func runtimeGosched() { time.Sleep(time.Millisecond) }
