package synctest

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	_ "github.com/lesismal/fit/sync/mssql"
	mssqlwire "github.com/lesismal/fit/sync/mssql/wire"
	_ "github.com/lesismal/fit/sync/mysql"
	mysqlwire "github.com/lesismal/fit/sync/mysql/wire"
)

// mssqlSinkParams are the sink mssql's params for s, with extra.
func mssqlSinkParams(t *testing.T, s mssqlServer, extra map[string]any) map[string]any {
	t.Setenv("FIT_TEST_SYNC_MSSQL_PASSWORD", s.password)
	p := map[string]any{"address": s.addr, "username": "sa", "password_env": "FIT_TEST_SYNC_MSSQL_PASSWORD", "database": "fit_sync",
		"ca_file": os.Getenv("FIT_SYNC_MSSQL_CA"), "server_name": "localhost"}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

func mssqlSink(t *testing.T, s mssqlServer, extra map[string]any) fitsync.Sink {
	t.Helper()
	cfg := loadSync(t, t.TempDir(), map[string]any{"name": "p", "source": map[string]any{"module": "generate"},
		"sinks": []any{map[string]any{"name": "s", "sink": map[string]any{"module": "mssql", "params": mssqlSinkParams(t, s, extra)}}}})
	m, err := module.Load(fitsync.Sinks, cfg.Sync.Pipelines[0].Sinks[0].Sink, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { module.Cleanup(m) })
	return m.(fitsync.Sink)
}

// mssqlIDV reads a table of id and v as JSONCompactEachRow's lines, as
// changes' rows are, sorted.
func mssqlIDV(t *testing.T, c *mssqlwire.Conn, table string) []string {
	t.Helper()
	_, rows, err := c.QueryAll(context.Background(), "SELECT id, v FROM "+table)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range rows {
		out = append(out, fmt.Sprintf(`[%d,"%s"]`, r[0], r[1]))
	}
	sort.Strings(out)
	return out
}

// TestMSSQLSinkConverges writes inserts, updates and deletes, then replays
// them from the middle and from the start in batches of other sizes: the
// table holds each id's last state, with MERGE and with UPDATE then
// INSERT, the snapshot's run of inserts bulk loaded or not.
func TestMSSQLSinkConverges(t *testing.T) {
	eachMSSQL(t, func(t *testing.T, s mssqlServer) {
		c := s.dial(t, mssqlwire.EncryptOn)
		for _, tc := range []struct {
			name   string
			params map[string]any
		}{
			{"merge", map[string]any{}},
			{"update_insert", map[string]any{"upsert": "update_insert"}},
			{"merge_bulk", map[string]any{"bulk_rows": 10}},
			{"update_insert_bulk", map[string]any{"upsert": "update_insert", "bulk_rows": 10}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				table := fmt.Sprintf("dbo.sink_%s_%d", tc.name, time.Now().UnixNano())
				mssqlExec(t, c, "CREATE TABLE "+table+" (id bigint NOT NULL PRIMARY KEY, v nvarchar(20) NULL)")
				t.Cleanup(func() { c.Exec(context.Background(), "DROP TABLE "+table) })
				recs, want := changes(table)
				sink := mssqlSink(t, s, tc.params)
				if err := sink.(fitsync.Tester).Test(hangGuard(t)); err != nil {
					t.Fatal(err)
				}
				writeAll(t, sink, recs, 37)
				writeAll(t, sink, recs[len(recs)/2:], 23)
				writeAll(t, sink, recs, 101)
				writeAll(t, sink, recs[len(recs)/3:], 37)
				if got := mssqlIDV(t, c, table); strings.Join(got, "\n") != strings.Join(want, "\n") {
					t.Fatalf("%d rows, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
				}
			})
		}
	})
}

// TestMSSQLSinkBadRow writes a batch with one row the table refuses: only
// that record fails, for good, the rows before it written, in statements
// and in a bulk load.
func TestMSSQLSinkBadRow(t *testing.T) {
	eachMSSQL(t, func(t *testing.T, s mssqlServer) {
		c := s.dial(t, mssqlwire.EncryptOn)
		for _, bulk := range []int{1000, 5} {
			t.Run(fmt.Sprint("bulk_rows_", bulk), func(t *testing.T) {
				table := fmt.Sprintf("dbo.sink_bad_%d", time.Now().UnixNano())
				mssqlExec(t, c, "CREATE TABLE "+table+" (id bigint NOT NULL PRIMARY KEY, v nvarchar(5) NOT NULL)")
				t.Cleanup(func() { c.Exec(context.Background(), "DROP TABLE "+table) })
				var recs []*fitsync.Record
				for i := range 10 {
					v := fitsync.String(fmt.Sprint(i))
					if i == 6 {
						v = fitsync.String("far too long")
					}
					recs = append(recs, &fitsync.Record{Namespace: table, Op: fitsync.OpInsert, Key: fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(int64(i))}),
						After: fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(int64(i))}, fitsync.Field{Name: "v", Value: v})})
				}
				err := mssqlSink(t, s, map[string]any{"bulk_rows": bulk}).Write(hangGuard(t), recs)
				var be *fitsync.BatchError
				if !errors.As(err, &be) || be.Index != 6 || !fitsync.IsPermanent(err) {
					t.Fatalf("%v", err)
				}
				if got := mssqlIDV(t, c, table); len(got) != 6 {
					t.Fatalf("%v, want 6 rows", got)
				}
			})
		}
	})
}

// TestMSSQLSinkTypes copies generate's records, of every value type, with
// updates and deletes, through a pipeline killed at random points and
// started again from its checkpoint, IDENTITY values written as they are:
// the table ends with each row as the records leave it.
func TestMSSQLSinkTypes(t *testing.T) {
	eachMSSQL(t, func(t *testing.T, s mssqlServer) {
		seed := uint64(time.Now().UnixNano())
		t.Logf("seed %d", seed)
		rnd := rand.New(rand.NewPCG(seed, 21))
		c := s.dial(t, mssqlwire.EncryptOn)
		table := fmt.Sprintf("dbo.sink_types_%d", time.Now().UnixNano())
		mssqlExec(t, c, "CREATE TABLE "+table+" (id bigint IDENTITY(1,1) NOT NULL PRIMARY KEY, name nvarchar(40), [count] bigint, score float, "+
			"price decimal(20, 2), even bit, [blob] varbinary(10), [at] datetime2(3), [day] date, doc nvarchar(max), tags nvarchar(max), [none] nvarchar(10))")
		t.Cleanup(func() { c.Exec(context.Background(), "DROP TABLE "+table) })
		const total = 2000
		cfg := loadSync(t, t.TempDir(), map[string]any{"name": "p", "mode": "once", "on_error": "stop", "checkpoint_interval": "1ms",
			"source": map[string]any{"module": "generate", "params": map[string]any{"records": total, "tx_size": 1 + rnd.IntN(20), "namespace": table}},
			"sinks": []any{map[string]any{"name": "ms", "batch": map[string]any{"max_records": 1 + rnd.IntN(300), "max_wait": "1ms"},
				"sink": map[string]any{"module": "mssql", "params": mssqlSinkParams(t, s, map[string]any{"identity_insert": true,
					"transactions": []string{"source", "batch"}[rnd.IntN(2)], "upsert": []string{"merge", "update_insert"}[rnd.IntN(2)]})}}}})
		kills := runKilled(t, cfg, rnd, 8)
		want := 0
		for n := range total {
			if n%7 != 6 {
				want++
			}
		}
		_, rows, err := c.QueryAll(hangGuard(t), "SELECT COUNT(*) FROM "+table)
		if err != nil || rows[0][0] != int64(want) {
			t.Fatalf("%v rows, want %d: %v", rows, want, err)
		}
		_, rows, err = c.QueryAll(hangGuard(t), "SELECT name, [count], score, price, even, [blob], [at], [day], doc, tags, [none] FROM "+table+" WHERE id = 12")
		if err != nil || len(rows) != 1 {
			t.Fatal(rows, err)
		}
		got := fmt.Sprintf("%v|%s|%s|%v", rows[0][:6], rows[0][6].(time.Time).UTC().Format(time.RFC3339Nano), rows[0][7].(time.Time).Format(time.DateOnly), rows[0][8:])
		if want := `[record-12 36 3 12.12 true [12 0 255]]|2023-11-14T22:13:32.012Z|2026-01-13|[{"n":12} ["a",2] <nil>]`; got != want {
			t.Fatalf("%q, want %q", got, want)
		}
		t.Logf("%d records across %d kills", total, kills)
	})
}

// TestMSSQLSinkExactlyOnce: a pipeline into SQL Server with checkpoint
// sink, killed at random points, writes every record once, by a trigger
// that counts each row inserted; with checkpoint file it writes some
// twice.
func TestMSSQLSinkExactlyOnce(t *testing.T) {
	eachMSSQL(t, func(t *testing.T, s mssqlServer) {
		c := s.dial(t, mssqlwire.EncryptOn)
		for _, ck := range []string{"sink", "file"} {
			t.Run(ck, func(t *testing.T) {
				suffix := fmt.Sprint(time.Now().UnixNano())
				dst, writes, ckTable := "dbo.xo_dst_"+suffix, "dbo.xo_writes_"+suffix, "dbo.xo_ck_"+suffix
				mssqlExec(t, c, "CREATE TABLE "+dst+" (id bigint NOT NULL PRIMARY KEY, seq bigint NOT NULL)")
				mssqlExec(t, c, "CREATE TABLE "+writes+" (seq bigint NOT NULL)")
				mssqlExec(t, c, "CREATE TRIGGER "+strings.Replace(dst, "dst", "trg", 1)+" ON "+dst+" AFTER INSERT, UPDATE AS INSERT INTO "+writes+" SELECT seq FROM inserted")
				t.Cleanup(func() {
					c.Exec(context.Background(), "DROP TABLE "+dst+"; DROP TABLE "+writes+"; DROP TABLE IF EXISTS "+ckTable)
				})
				// Counted at snapshot isolation, which waits for no lock a
				// transaction held open across batches has.
				cc := s.dial(t, mssqlwire.EncryptOn)
				mssqlExec(t, cc, "SET TRANSACTION ISOLATION LEVEL SNAPSHOT")
				count := func() (int64, int64, int64) {
					_, rows, err := cc.QueryAll(context.Background(), "SELECT (SELECT COUNT(*) FROM "+dst+"), COUNT(*), COUNT(DISTINCT seq) FROM "+writes)
					if err != nil || len(rows) != 1 {
						t.Fatalf("%v %v", rows, err)
					}
					return rows[0][0].(int64), rows[0][1].(int64), rows[0][2].(int64)
				}
				for _, tx := range []string{"source", "batch"} {
					t.Run(tx, func(t *testing.T) {
						mssqlExec(t, c, "DELETE FROM "+dst+"; DELETE FROM "+writes+"; DROP TABLE IF EXISTS "+ckTable)
						exactlyOnce(t, "once", ck, 2000, map[string]any{"module": "mssql",
							"params": mssqlSinkParams(t, s, map[string]any{"table": dst, "transactions": tx, "checkpoint": ck, "checkpoint_table": ckTable})}, count)
					})
				}
			})
		}
	})
}

// TestMSSQLSinkFromMSSQL copies a table of 500 rows written to while its
// snapshot is read, then its changes, from the source mssql to another
// table through the sink mssql, the pipeline killed at random points: the
// copy ends equal to the table.
func TestMSSQLSinkFromMSSQL(t *testing.T) {
	eachMSSQL(t, func(t *testing.T, s mssqlServer) {
		seed := uint64(time.Now().UnixNano())
		t.Logf("seed %d", seed)
		rnd := rand.New(rand.NewPCG(seed, 23))
		c := s.dial(t, mssqlwire.EncryptOn)
		suffix := fmt.Sprint(time.Now().UnixNano())
		src, dst := "ms_src_"+suffix, "ms_dst_"+suffix
		mssqlExec(t, c, "CREATE TABLE dbo."+src+" "+mssqlLoadColumns)
		mssqlExec(t, c, "CREATE TABLE dbo."+dst+" "+mssqlLoadColumns)
		t.Cleanup(func() { c.Exec(context.Background(), "DROP TABLE dbo."+src+"; DROP TABLE dbo."+dst) })
		mssqlCapture(t, s, c, src)
		w := &mssqlWriter{table: src, rnd: rand.New(rand.NewPCG(seed, 7))}
		w.fill(t, c, 500)
		pipeline := map[string]any{"name": "p", "on_error": "stop", "checkpoint_interval": "1ms",
			"retry":  map[string]any{"max_attempts": 1000, "backoff": "50ms", "max_backoff": "1s"},
			"queue":  map[string]any{"max_records": 20 + rnd.IntN(300)},
			"source": map[string]any{"module": "mssql", "params": mssqlSourceParams(t, s, map[string]any{"tables": []string{"dbo." + src}, "batch_size": 50})},
			"sinks": []any{map[string]any{"name": "ms", "batch": map[string]any{"max_records": 1 + rnd.IntN(60), "max_wait": "1ms"},
				"sink": map[string]any{"module": "mssql", "params": mssqlSinkParams(t, s, map[string]any{"table": "dbo." + dst,
					"checkpoint": []string{"file", "sink"}[rnd.IntN(2)], "checkpoint_table": "dbo.ck_" + suffix, "bulk_rows": 1 + rnd.IntN(100)})}}}}
		t.Cleanup(func() { c.Exec(context.Background(), "DROP TABLE IF EXISTS dbo.ck_"+suffix) })
		cfg := loadSync(t, t.TempDir(), pipeline).Sync

		wc := s.dial(t, mssqlwire.EncryptOff)
		done := make(chan error, 1)
		go func() { done <- w.write(context.Background(), wc, 300) }()
		kills := 0
		for range 8 {
			if crashRuntime(t, cfg, rnd, 3*time.Second) {
				kills++
			}
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		rt := fitsync.NewRuntime(fitsync.Options{})
		if err := rt.Apply(cfg); err != nil {
			t.Fatal(err)
		}
		defer rt.Close(context.Background())
		deadline := time.Now().Add(3 * time.Minute)
		for {
			want, got := mssqlRows(t, c, src), mssqlRows(t, c, dst)
			if fmt.Sprint(want) == fmt.Sprint(got) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%d rows, %d copied: %+v", len(want), len(got), rt.Status())
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Logf("%d rows after %d transactions, equal across %d kills", len(w.ids), w.n, kills)
	})
}

// TestMSSQLSinkFromMySQL copies a MySQL table written to by a mixed load
// to SQL Server, the pipeline killed at random points: the copy ends equal
// to the table. It needs a MySQL server of test/sync/mysql.sh too, and
// skips without one.
func TestMSSQLSinkFromMySQL(t *testing.T) {
	ms := mssqlServers(t)[0]
	if os.Getenv("FIT_SYNC_MYSQL") == "" {
		t.Skip("no MySQL: FIT_SYNC_MYSQL is not set (test/sync/mysql.sh sets it)")
	}
	my := mysqlServers(t)[0]
	seed := uint64(time.Now().UnixNano())
	t.Logf("seed %d (MySQL %s)", seed, my.name)
	rnd := rand.New(rand.NewPCG(seed, 25))
	mc := my.root(t)
	mysqlDB(t, mc, "fit_tomssql")
	mustExec(t, mc, "CREATE TABLE t "+fmt.Sprintf(loadColumns, ""))
	w := &mysqlWriter{table: "fit_tomssql.t", rnd: rand.New(rand.NewPCG(seed, 7)), rows: map[int64]bool{}}
	mysqlFill(t, mc, "fit_tomssql.t", 300, w)
	c := ms.dial(t, mssqlwire.EncryptOn)
	dst := fmt.Sprintf("dbo.from_mysql_%d", time.Now().UnixNano())
	mssqlExec(t, c, "CREATE TABLE "+dst+" (id bigint NOT NULL PRIMARY KEY, v nvarchar(64) NOT NULL, n decimal(10,2), at datetime2(6), j nvarchar(max), b varbinary(16))")
	t.Cleanup(func() { c.Exec(context.Background(), "DROP TABLE "+dst) })
	pipeline := map[string]any{"name": "p", "on_error": "stop", "checkpoint_interval": "1ms",
		"retry":  map[string]any{"max_attempts": 1000, "backoff": "50ms", "max_backoff": "1s"},
		"queue":  map[string]any{"max_records": 20 + rnd.IntN(300)},
		"source": map[string]any{"module": "mysql", "params": mysqlSourceParams(t, my, map[string]any{"tables": []string{"fit_tomssql.t"}, "batch_size": 50})},
		"sinks": []any{map[string]any{"name": "ms", "batch": map[string]any{"max_records": 1 + rnd.IntN(60), "max_wait": "1ms"},
			"sink": map[string]any{"module": "mssql", "params": mssqlSinkParams(t, ms, map[string]any{"table": dst,
				"transactions": []string{"source", "batch"}[rnd.IntN(2)]})}}}}
	cfg := loadSync(t, t.TempDir(), pipeline).Sync

	wc := my.root(t)
	done := make(chan error, 1)
	wctx := hangGuard(t)
	go func() { done <- w.writeErr(wctx, wc, 300) }()
	kills := 0
	for range 6 {
		if crashRuntime(t, cfg, rnd, 2*time.Second) {
			kills++
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	rt := fitsync.NewRuntime(fitsync.Options{})
	if err := rt.Apply(cfg); err != nil {
		t.Fatal(err)
	}
	defer rt.Close(context.Background())
	read := func() (map[int64]string, map[int64]string) {
		want := map[int64]string{}
		rows, err := mc.Query(hangGuard(t), "SELECT id, v, n, at, b FROM fit_tomssql.t")
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			v := rows.Values()
			at, _ := v[3].(mysqlwire.DateTime)
			b, _ := v[4].([]byte)
			want[v[0].(int64)] = fmt.Sprintf("%d|%s|%s|%s|%x", v[0], v[1], v[2], at.Time(time.UTC).Format(time.RFC3339Nano), b)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		got := map[int64]string{}
		_, ms, err := c.QueryAll(hangGuard(t), "SELECT id, v, n, at, b FROM "+dst)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range ms {
			at, _ := r[3].(time.Time)
			b, _ := r[4].([]byte)
			n := ""
			if r[2] != nil {
				n = string(r[2].(mssqlwire.Decimal))
			}
			got[r[0].(int64)] = fmt.Sprintf("%d|%s|%s|%s|%x", r[0], r[1], n, at.UTC().Format(time.RFC3339Nano), b)
		}
		return want, got
	}
	deadline := time.Now().Add(3 * time.Minute)
	for {
		want, got := read()
		if fmt.Sprint(want) == fmt.Sprint(got) {
			break
		}
		if time.Now().After(deadline) {
			var diff []string
			for id, w := range want {
				if got[id] != w {
					diff = append(diff, fmt.Sprintf("%d: %q, copied %q", id, w, got[id]))
				}
			}
			sort.Strings(diff)
			t.Fatalf("%d rows, %d copied, %d differ: %s; %+v", len(want), len(got), len(diff), strings.Join(diff[:min(10, len(diff))], "; "), rt.Status())
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Logf("%d rows of %d transactions, equal across %d kills", len(w.rows), w.n, kills)
}
