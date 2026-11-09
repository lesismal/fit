package synctest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	_ "github.com/lesismal/fit/sync/mysql"
	mysqlwire "github.com/lesismal/fit/sync/mysql/wire"
)

// mysqlSourceParams are the source mysql's params for s, with extra.
func mysqlSourceParams(t *testing.T, s mysqlServer, extra map[string]any) map[string]any {
	t.Setenv("FIT_TEST_SYNC_MYSQL_PASSWORD", s.password)
	p := map[string]any{"address": s.addr, "username": "root", "password_env": "FIT_TEST_SYNC_MYSQL_PASSWORD",
		"tls": true, "ca_file": os.Getenv("FIT_SYNC_MYSQL_CA"), "tls_rsa": s.name == "mysql57",
		"server_id": 1000 + rand.IntN(1000000), "heartbeat": "1s"}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

func mysqlSource(t *testing.T, params map[string]any) fitsync.Source {
	t.Helper()
	b, _ := json.Marshal(params)
	v, err := config.ParseValue(b, "params")
	if err != nil {
		t.Fatal(err)
	}
	m, err := module.Load(fitsync.Sources, config.ModuleRef{Module: "mysql", Params: v}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { module.Cleanup(m) })
	return m.(fitsync.Source)
}

// dumpThreads are the IDs of the server's binlog dump threads, one per
// replica reading. (MySQL 5.7's SHOW SLAVE HOSTS sends rows shorter than
// its columns for a replica without a UUID.)
func dumpThreads(t *testing.T, c *mysqlwire.Conn) map[string]bool {
	t.Helper()
	rows, err := c.Query(hangGuard(t), "SELECT ID FROM information_schema.PROCESSLIST WHERE COMMAND LIKE 'Binlog Dump%'")
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for rows.Next() {
		ids[fmt.Sprint(rows.Values()[0])] = true
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	return ids
}

// waitReplica waits until a dump thread not among before runs: a source
// has read its start and asked for the binlog.
func waitReplica(t *testing.T, c *mysqlwire.Conn, before map[string]bool) {
	t.Helper()
	deadline := time.Now().Add(time.Minute) // a hang guard
	for time.Now().Before(deadline) {
		for id := range dumpThreads(t, c) {
			if !before[id] {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no replica reads the binlog")
}

// rootAfterRestart connects as root once the server, restarted, answers.
func (s mysqlServer) rootAfterRestart(t *testing.T) *mysqlwire.Conn {
	t.Helper()
	cfg := s.config("root", s.password)
	cfg.TLS = s.tls()
	deadline := time.Now().Add(3 * time.Minute) // a hang guard
	for {
		c, err := mysqlwire.Dial(hangGuard(t), cfg)
		if err == nil {
			t.Cleanup(func() { c.Close() })
			return c
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s does not answer after a restart: %v", s.name, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// purgeBinlogs purges the server's binlogs up to a new one, until the file
// is gone: MariaDB keeps a file crash recovery may still need.
func purgeBinlogs(t *testing.T, c *mysqlwire.Conn, file string) {
	t.Helper()
	deadline := time.Now().Add(time.Minute) // a hang guard
	for {
		mustExec(t, c, "FLUSH BINARY LOGS")
		rows, err := c.Query(hangGuard(t), "SHOW BINARY LOGS")
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for rows.Next() {
			names = append(names, fmt.Sprintf("%s", rows.Values()[0]))
		}
		rows.Close()
		if !slices.Contains(names, file) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s is not purged: %v", file, names)
		}
		mustExec(t, c, "PURGE BINARY LOGS TO '"+names[len(names)-1]+"'")
		time.Sleep(50 * time.Millisecond)
	}
}

// binlogLock serializes the tests that purge binlogs with
// TestMySQLBinlogInterop, which reads a binlog file of its own.
func binlogLock(t *testing.T) {
	t.Helper()
	p := filepath.Join(os.TempDir(), "fit-sync-mysql-binlog.lock")
	deadline := time.Now().Add(3 * time.Minute) // a lock left by a test killed is taken after it
	for {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			f.Close()
			t.Cleanup(func() { os.Remove(p) })
			return
		}
		if time.Now().After(deadline) {
			os.Remove(p)
			deadline = time.Now().Add(3 * time.Minute)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func plain(v fitsync.Value) string {
	b, _ := fitsync.AppendPlain(nil, v)
	return string(b)
}

// recLine is a record as op namespace key row, the row after, or before
// for a delete.
func recLine(r *fitsync.Record) string {
	row := r.After
	if r.Op == fitsync.OpDelete {
		row = r.Before
	}
	if r.Op == fitsync.OpDDL {
		s, _ := row.Get("statement")
		return "ddl " + r.Namespace + " " + s.Text()
	}
	return r.Op.String() + " " + r.Namespace + " " + plain(r.Key) + " " + plain(row)
}

// TestMySQLSourceStream follows each server: inserts, updates, a key
// changed and deletes across two tables, a DDL mid-stream, a restart from
// a checkpoint, and a checkpoint whose binlogs are purged.
func TestMySQLSourceStream(t *testing.T) {
	eachMySQL(t, func(t *testing.T, s mysqlServer) {
		c := s.root(t)
		mysqlDB(t, c, "fit_src1")
		mustExec(t, c, "CREATE TABLE a (id INT PRIMARY KEY, v VARCHAR(20), u INT UNSIGNED, e ENUM('x','y'), d DECIMAL(6,2), bin VARBINARY(4))")
		mustExec(t, c, "CREATE TABLE b (k1 INT, k2 VARCHAR(10), w INT, PRIMARY KEY (k1, k2))")
		params := mysqlSourceParams(t, s, map[string]any{"tables": []string{"fit_src1.*"}})
		src := mysqlSource(t, params)
		before := dumpThreads(t, c)
		run := runSource(src, nil, false)
		waitReplica(t, c, before)

		step := func(n int, want []string, stmts ...string) []*fitsync.Record {
			t.Helper()
			for _, q := range stmts {
				mustExec(t, c, q)
			}
			recs := run.take(t, n)
			for i, r := range recs {
				if got := recLine(r); got != want[i] {
					t.Fatalf("record %d: %s\nwant %s", i, got, want[i])
				}
			}
			return recs
		}
		tx1 := step(2, []string{
			`insert fit_src1.a {"id":1} {"id":1,"v":"a","u":4294967295,"e":"y","d":1.50,"bin":"AP8="}`,
			`insert fit_src1.b {"k1":1,"k2":"k"} {"k1":1,"k2":"k","w":7}`,
		}, "BEGIN", "INSERT INTO a VALUES (1, 'a', 4294967295, 'y', 1.50, 0x00ff)", "INSERT INTO b VALUES (1, 'k', 7)", "COMMIT")
		if !tx1[0].Tx.First || tx1[0].Tx.Last || !tx1[1].Tx.Last || tx1[0].Tx.ID != tx1[1].Tx.ID || tx1[1].Position == nil || tx1[0].Position != nil {
			t.Fatalf("the transaction: %+v %+v", tx1[0].Tx, tx1[1].Tx)
		}
		step(1, []string{`update fit_src1.a {"id":1} {"id":1,"v":"A","u":4294967295,"e":"y","d":1.50,"bin":"AP8="}`},
			"UPDATE a SET v = 'A' WHERE id = 1")
		tx3 := step(2, []string{
			`delete fit_src1.a {"id":1} {"id":1,"v":"A","u":4294967295,"e":"y","d":1.50,"bin":"AP8="}`,
			`insert fit_src1.a {"id":2} {"id":2,"v":"A","u":4294967295,"e":"y","d":1.50,"bin":"AP8="}`,
		}, "UPDATE a SET id = 2 WHERE id = 1")
		step(1, []string{"ddl fit_src1.a ALTER TABLE a ADD COLUMN w INT"}, "ALTER TABLE a ADD COLUMN w INT")
		step(1, []string{`insert fit_src1.a {"id":3} {"id":3,"v":"c","u":0,"e":"x","d":0.00,"bin":null,"w":9}`},
			"INSERT INTO a VALUES (3, 'c', 0, 'x', 0, NULL, 9)")
		step(1, []string{`delete fit_src1.b {"k1":1,"k2":"k"} {"k1":1,"k2":"k","w":7}`}, "DELETE FROM b WHERE k1 = 1")
		if err := run.stop(t); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}

		// Again from the key change's checkpoint: the DDL comes next.
		again := runSource(src, tx3[1].Position, false)
		if r := again.take(t, 1)[0]; r.Op != fitsync.OpDDL {
			t.Fatalf("after the checkpoint: %s", recLine(r))
		}
		again.stop(t)

		// The binlogs from the first transaction purged: a new snapshot
		// is needed.
		binlogLock(t)
		var p struct{ File string }
		json.Unmarshal(tx1[1].Position, &p)
		purgeBinlogs(t, c, p.File)
		purged := runSource(src, tx1[1].Position, false)
		select {
		case err := <-purged.done:
			if err == nil || !strings.Contains(err.Error(), "new snapshot") {
				t.Fatal(err)
			}
		case <-purged.recs:
			t.Fatal("a record from a purged binlog")
		case <-time.After(2 * time.Minute):
			t.Fatal("no error from a purged binlog")
		}
	})
}

// TestMySQLSourceTest runs the source's connection test.
func TestMySQLSourceTest(t *testing.T) {
	eachMySQL(t, func(t *testing.T, s mysqlServer) {
		src := mysqlSource(t, mysqlSourceParams(t, s, nil))
		if err := src.(fitsync.Tester).Test(hangGuard(t)); err != nil {
			t.Fatal(err)
		}
	})
}

// mysqlWriter writes a mixed load in transactions to table, inserts,
// updates, key changes and deletes over 300 keys, as the sink's test's
// load, from seed.
type mysqlWriter struct {
	table string
	rnd   *rand.Rand
	rows  map[int64]bool
	n     int
	// space is how many keys the load picks from, 300 when 0.
	space int
}

func (w *mysqlWriter) write(t *testing.T, c *mysqlwire.Conn, txs int) {
	t.Helper()
	if err := w.writeErr(hangGuard(t), c, txs); err != nil {
		t.Fatal(err)
	}
}

func (w *mysqlWriter) writeErr(ctx context.Context, c *mysqlwire.Conn, txs int) error {
	base := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	space := w.space
	if space == 0 {
		space = 300
	}
	for range txs {
		w.n++
		if _, err := c.Exec(ctx, "BEGIN"); err != nil {
			return err
		}
		for k := range 1 + w.rnd.IntN(6) {
			id := int64(w.rnd.IntN(space))
			v := fmt.Sprintf("t%d-%d", w.n, k)
			n := fmt.Sprintf("%d.%02d", w.rnd.IntN(10000), w.rnd.IntN(100))
			at := base.Add(time.Duration(w.rnd.Int64N(1e12)) * time.Microsecond)
			j := json.RawMessage(fmt.Sprintf(`{"t": %d, "k": [%d]}`, w.n, k))
			var b any
			if w.rnd.IntN(3) > 0 {
				b = []byte{byte(w.n), byte(k), 0, 0xff}
			}
			var err error
			switch r := w.rnd.IntN(10); {
			case !w.rows[id]:
				_, err = c.Exec(ctx, "INSERT INTO "+w.table+" (id, v, n, at, j, b) VALUES (?, ?, ?, ?, ?, ?)", id, v, n, at, j, b)
				w.rows[id] = true
			case r < 2:
				_, err = c.Exec(ctx, "DELETE FROM "+w.table+" WHERE id = ?", id)
				delete(w.rows, id)
			case r < 3:
				to := id
				for w.rows[to] {
					to = int64(space) + int64(w.rnd.IntN(1000))
				}
				_, err = c.Exec(ctx, "UPDATE "+w.table+" SET id = ?, v = ? WHERE id = ?", to, v, id)
				delete(w.rows, id)
				w.rows[to] = true
			default:
				_, err = c.Exec(ctx, "UPDATE "+w.table+" SET v = ?, n = ?, at = ?, j = ?, b = ? WHERE id = ?", v, n, at, j, b, id)
			}
			if err != nil {
				return err
			}
		}
		if _, err := c.Exec(ctx, "COMMIT"); err != nil {
			return err
		}
	}
	return nil
}

// TestMySQLSourceAcrossKills copies a table written to by a mixed load,
// from each server to a table of the same server, through the sink mysql:
// the pipeline is killed at random points and started again from its
// checkpoint, and, with FIT_SYNC_MYSQL_PREFIX, the server is restarted
// under it; the two tables end equal by checksum.
func TestMySQLSourceAcrossKills(t *testing.T) {
	eachMySQL(t, func(t *testing.T, s mysqlServer) {
		seed := uint64(time.Now().UnixNano())
		t.Logf("seed %d", seed)
		rnd := rand.New(rand.NewPCG(seed, 5))
		c := s.root(t)
		mysqlDB(t, c, "fit_dst")
		mustExec(t, c, "CREATE TABLE t "+fmt.Sprintf(loadColumns, ""))
		mysqlDB(t, c, "fit_src")
		mustExec(t, c, "CREATE TABLE t "+fmt.Sprintf(loadColumns, ""))
		w := &mysqlWriter{table: "fit_src.t", rnd: rand.New(rand.NewPCG(seed, 7)), rows: map[int64]bool{}}

		stateDir := t.TempDir()
		srcParams := mysqlSourceParams(t, s, map[string]any{"tables": []string{"fit_src.t"}})
		sinkParams := mysqlTarget{name: s.name, addr: s.addr, password: s.password, srv: s}.params(t, map[string]any{"table": "fit_dst.t"})
		pipeline := map[string]any{"name": "p", "on_error": "stop", "checkpoint_interval": "1ms",
			"retry":  map[string]any{"max_attempts": 1000, "backoff": "50ms", "max_backoff": "1s"},
			"queue":  map[string]any{"max_records": 20 + rnd.IntN(300)},
			"source": map[string]any{"module": "mysql", "params": srcParams},
			"sinks": []any{map[string]any{"name": "my", "batch": map[string]any{"max_records": 1 + rnd.IntN(60), "max_wait": "1ms"},
				"sink": map[string]any{"module": "mysql", "params": sinkParams}}}}
		b, _ := json.Marshal(map[string]any{"sync": map[string]any{"state_dir": stateDir, "pipelines": []any{pipeline}}})
		cfg, err := config.Load(b)
		if err != nil {
			t.Fatal(err)
		}

		// A first run, until its checkpoint holds a transaction: a
		// pipeline without a checkpoint starts from the server's now.
		rt := fitsync.NewRuntime(fitsync.Options{})
		before := dumpThreads(t, c)
		if err := rt.Apply(cfg.Sync); err != nil {
			t.Fatal(err)
		}
		waitReplica(t, c, before)
		w.write(t, c, 1)
		equal := func(guard time.Duration) bool {
			deadline := time.Now().Add(guard)
			for time.Now().Before(deadline) {
				if loadChecksum(t, c, "fit_src.t") == loadChecksum(t, c, "fit_dst.t") {
					return true
				}
				time.Sleep(20 * time.Millisecond)
			}
			return false
		}
		if !equal(time.Minute) {
			t.Fatalf("the first transaction is not copied: %+v", rt.Status())
		}
		for {
			if _, err := os.Stat(filepath.Join(stateDir, "p.json")); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		rt.Close(context.Background())

		points := []fitsync.Point{fitsync.CrashEmitted, fitsync.CrashDispatched, fitsync.CrashWritten,
			fitsync.CrashAcknowledged, fitsync.CrashCheckpointWritten, fitsync.CrashCheckpointSaved}
		kills := 0
		for round := range 8 {
			at, nth := points[rnd.IntN(len(points))], int32(1+rnd.IntN(60))
			var seen atomic.Int32
			rt := fitsync.NewRuntime(fitsync.Options{Crash: func(_ string, p fitsync.Point) bool {
				return p == at && seen.Add(1) == nth
			}})
			if err := rt.Apply(cfg.Sync); err != nil {
				t.Fatal(err)
			}
			w.write(t, c, 40)
			if round == 4 {
				if prefix := os.Getenv("FIT_SYNC_MYSQL_PREFIX"); prefix != "" {
					// The server restarted under the pipeline.
					if out, err := exec.Command("docker", "restart", prefix+"-"+s.name).CombinedOutput(); err != nil {
						t.Fatalf("docker restart: %v: %s", err, out)
					}
					c = s.rootAfterRestart(t)
					t.Log("the server restarted")
				}
			}
			deadline := time.Now().Add(2 * time.Second)
			for st := redisState(rt); st != "killed" && time.Now().Before(deadline); st = redisState(rt) {
				time.Sleep(time.Millisecond)
			}
			if redisState(rt) == "killed" {
				kills++
			}
			rt.Close(context.Background())
		}
		rt = fitsync.NewRuntime(fitsync.Options{})
		if err := rt.Apply(cfg.Sync); err != nil {
			t.Fatal(err)
		}
		defer rt.Close(context.Background())
		if !equal(3 * time.Minute) {
			t.Fatalf("the tables differ: %s and %s; %+v", loadChecksum(t, c, "fit_src.t"), loadChecksum(t, c, "fit_dst.t"), rt.Status())
		}
		t.Logf("%d rows of %d transactions, equal across %d kills", len(w.rows), w.n, kills)
	})
}

// mysqlFill writes n rows of the load's columns, ids 0 to n-1, to table.
func mysqlFill(t *testing.T, c *mysqlwire.Conn, table string, n int, w *mysqlWriter) {
	t.Helper()
	at := time.Date(2026, 11, 9, 1, 2, 3, 456000, time.UTC)
	var rows [][]any
	for id := range n {
		rows = append(rows, []any{int64(id), fmt.Sprint("fill-", id), "1.25", at, json.RawMessage(`{"fill": true}`), []byte{byte(id)}})
		w.rows[int64(id)] = true
	}
	if _, err := c.InsertRows(hangGuard(t), "INSERT INTO "+table+" (id, v, n, at, j, b) VALUES", rows, ""); err != nil {
		t.Fatal(err)
	}
}

// mysqlCopyConfig is a pipeline from the source mysql reading srcTable to
// the sink mysql writing dstTable, on server s.
func mysqlCopyConfig(t *testing.T, s mysqlServer, rnd *rand.Rand, stateDir, srcTable, dstTable string, srcExtra, extra map[string]any) *config.Sync {
	t.Helper()
	srcExtra["tables"] = []string{srcTable}
	pipeline := map[string]any{"name": "p", "on_error": "stop", "checkpoint_interval": "1ms",
		"retry":  map[string]any{"max_attempts": 1000, "backoff": "50ms", "max_backoff": "1s"},
		"queue":  map[string]any{"max_records": 20 + rnd.IntN(300)},
		"source": map[string]any{"module": "mysql", "params": mysqlSourceParams(t, s, srcExtra)},
		"sinks": []any{map[string]any{"name": "my", "batch": map[string]any{"max_records": 1 + rnd.IntN(60), "max_wait": "1ms"},
			"sink": map[string]any{"module": "mysql", "params": mysqlTarget{name: s.name, addr: s.addr, password: s.password, srv: s}.params(t, map[string]any{"table": dstTable})}}}}
	for k, v := range extra {
		pipeline[k] = v
	}
	b, _ := json.Marshal(map[string]any{"sync": map[string]any{"state_dir": stateDir, "pipelines": []any{pipeline}}})
	cfg, err := config.Load(b)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Sync
}

// crashRuntime runs cfg until the crash hook kills it at a random point,
// or for at most wait.
func crashRuntime(t *testing.T, cfg *config.Sync, rnd *rand.Rand, wait time.Duration) bool {
	t.Helper()
	points := []fitsync.Point{fitsync.CrashEmitted, fitsync.CrashDispatched, fitsync.CrashWritten,
		fitsync.CrashAcknowledged, fitsync.CrashCheckpointWritten, fitsync.CrashCheckpointSaved}
	at, nth := points[rnd.IntN(len(points))], int32(1+rnd.IntN(200))
	var seen atomic.Int32
	rt := fitsync.NewRuntime(fitsync.Options{Crash: func(_ string, p fitsync.Point) bool {
		return p == at && seen.Add(1) == nth
	}})
	if err := rt.Apply(cfg); err != nil {
		t.Fatal(err)
	}
	defer rt.Close(context.Background())
	deadline := time.Now().Add(wait)
	for st := redisState(rt); st != "killed" && st != "finished" && time.Now().Before(deadline); st = redisState(rt) {
		time.Sleep(time.Millisecond)
	}
	return redisState(rt) == "killed"
}

// TestMySQLSourceSnapshot copies a table of 3,000 rows written to while
// its snapshot is read, a page of 50 rows at a time, through the sink
// mysql, the pipeline killed at random points, inside the snapshot too:
// the copy ends equal to the table by checksum.
func TestMySQLSourceSnapshot(t *testing.T) {
	eachMySQL(t, func(t *testing.T, s mysqlServer) {
		seed := uint64(time.Now().UnixNano())
		t.Logf("seed %d", seed)
		rnd := rand.New(rand.NewPCG(seed, 9))
		c := s.root(t)
		mysqlDB(t, c, "fit_snapdst")
		mustExec(t, c, "CREATE TABLE t "+fmt.Sprintf(loadColumns, ""))
		mysqlDB(t, c, "fit_snap")
		mustExec(t, c, "CREATE TABLE t "+fmt.Sprintf(loadColumns, ""))
		w := &mysqlWriter{table: "fit_snap.t", rnd: rand.New(rand.NewPCG(seed, 7)), rows: map[int64]bool{}, space: 3000}
		mysqlFill(t, c, "fit_snap.t", 3000, w)
		cfg := mysqlCopyConfig(t, s, rnd, t.TempDir(), "fit_snap.t", "fit_snapdst.t", map[string]any{"batch_size": 50}, nil)

		// The load written meanwhile, on a connection of its own.
		wc := s.root(t)
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
		deadline := time.Now().Add(3 * time.Minute) // a hang guard
		for loadChecksum(t, c, "fit_snap.t") != loadChecksum(t, c, "fit_snapdst.t") {
			if time.Now().After(deadline) {
				t.Fatalf("the tables differ: %s and %s; %+v", loadChecksum(t, c, "fit_snap.t"), loadChecksum(t, c, "fit_snapdst.t"), rt.Status())
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Logf("%d rows after %d transactions during the snapshot, equal across %d kills", len(w.rows), w.n, kills)
	})
}

// TestMySQLSourceJob copies a table of 2,000 rows in a job, chunks of 300
// rows read four at a time, killed half way and finished on its restart.
func TestMySQLSourceJob(t *testing.T) {
	eachMySQL(t, func(t *testing.T, s mysqlServer) {
		seed := uint64(time.Now().UnixNano())
		t.Logf("seed %d", seed)
		rnd := rand.New(rand.NewPCG(seed, 11))
		c := s.root(t)
		mysqlDB(t, c, "fit_jobdst")
		mustExec(t, c, "CREATE TABLE t "+fmt.Sprintf(loadColumns, ""))
		mysqlDB(t, c, "fit_job")
		mustExec(t, c, "CREATE TABLE t "+fmt.Sprintf(loadColumns, ""))
		mysqlFill(t, c, "fit_job.t", 2000, &mysqlWriter{rows: map[int64]bool{}})
		mustExec(t, c, "ANALYZE TABLE t")
		cfg := mysqlCopyConfig(t, s, rnd, t.TempDir(), "fit_job.t", "fit_jobdst.t", map[string]any{"batch_size": 100, "chunk_rows": 300},
			map[string]any{"mode": "once", "job": map[string]any{"max_parallel": 4}})
		killed := crashRuntime(t, cfg, rnd, 3*time.Minute)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute) // a hang guard
		defer cancel()
		st, err := fitsync.RunPipeline(ctx, cfg, "p", fitsync.Options{})
		if err != nil || st.State != "finished" || st.Job == nil {
			t.Fatalf("%v: %+v", err, st)
		}
		if got, want := loadChecksum(t, c, "fit_jobdst.t"), loadChecksum(t, c, "fit_job.t"); got != want {
			t.Fatalf("the copy's checksum is %s, want %s", got, want)
		}
		t.Logf("2,000 rows in %d chunks, killed %v", len(st.Job.Chunks), killed)
	})
}
