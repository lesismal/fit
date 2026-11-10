package synctest

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	fitsync "github.com/lesismal/fit/sync"
	mssqlwire "github.com/lesismal/fit/sync/mssql/wire"
	pgwire "github.com/lesismal/fit/sync/postgres/wire"
)

// TestMSSQLSourceQuietTable: a pipeline reading a quiet table among
// captured ones that take transactions moves its checkpoint past its last
// record with position-only records, so that a restart after CDC's
// cleanup passed that record does not ask for a new snapshot.
func TestMSSQLSourceQuietTable(t *testing.T) {
	eachMSSQL(t, func(t *testing.T, s mssqlServer) {
		c := s.dial(t, mssqlwire.EncryptOn)
		prefix := fmt.Sprintf("quiet%d_", time.Now().UnixNano())
		a, b := prefix+"a", prefix+"b"
		for _, tb := range []string{a, b} {
			mssqlExec(t, c, "CREATE TABLE dbo."+tb+" (id int PRIMARY KEY, v int NULL)")
			t.Cleanup(func() { c.Exec(context.Background(), "DROP TABLE dbo."+tb) })
			mssqlCapture(t, s, c, tb)
		}
		store := "mssql-" + prefix
		mem := fitsync.MemoryStore(store)
		t.Cleanup(mem.Reset)
		b2, _ := json.Marshal(map[string]any{"sync": map[string]any{"state_dir": t.TempDir(), "pipelines": []any{map[string]any{
			"name": "p", "checkpoint_interval": "100ms",
			"source": map[string]any{"module": "mssql", "params": mssqlSourceParams(t, s, map[string]any{"tables": []string{"dbo." + a}, "snapshot": "never"})},
			"sinks": []any{map[string]any{"name": "mem", "batch": map[string]any{"max_wait": "1ms"},
				"sink": map[string]any{"module": "memory", "params": map[string]any{"store": store}}}}}}}})
		cfg, err := config.Load(b2)
		if err != nil {
			t.Fatal(err)
		}
		rt := fitsync.NewRuntime(fitsync.Options{})
		defer rt.Close(context.Background())
		if err := rt.Apply(cfg.Sync); err != nil {
			t.Fatal(err)
		}
		wait := func(what string, cond func() bool) {
			t.Helper()
			for deadline := time.Now().Add(3 * time.Minute); !cond(); time.Sleep(50 * time.Millisecond) {
				if time.Now().After(deadline) {
					t.Fatalf("timed out waiting for %s: %+v", what, rt.Status())
				}
			}
		}
		type pos struct{ LSN string }
		lsnOf := func(p []byte) string {
			var v pos
			json.Unmarshal(p, &v)
			return v.LSN
		}
		wait("the source to run", func() bool { st := rt.Status(); return len(st) == 1 && st[0].State == "running" })
		mssqlExec(t, c, "INSERT INTO dbo."+a+" VALUES (1, 0)")
		wait("the record", func() bool { return len(mem.Records()) == 1 })
		at := lsnOf(mem.Records()[0].Position)
		mssqlExec(t, c, "INSERT INTO dbo."+b+" VALUES (1, 0)")
		wait("the checkpoint past the record", func() bool {
			mssqlExec(t, c, "UPDATE dbo."+b+" SET v = v + 1")
			cp, err := rt.Checkpoint("p")
			return err == nil && cp != nil && lsnOf(cp.Position) > at
		})
		if st := rt.Status(); st[0].Read != 1 || st[0].Sinks[0].Records != 1 {
			t.Fatalf("%+v", st[0])
		}
	})
}

// TestMySQLSourceQuietTable: a pipeline reading a quiet table while another
// table of the server takes transactions moves its checkpoint with
// position-only records (I216 decision 12), so that after the binlogs of
// its last record are purged it starts again from its checkpoint, where it
// would have asked for a new snapshot.
func TestMySQLSourceQuietTable(t *testing.T) {
	eachMySQL(t, func(t *testing.T, s mysqlServer) {
		c := s.root(t)
		mysqlDB(t, c, "fit_srcq")
		mustExec(t, c, "CREATE TABLE a (id INT PRIMARY KEY, v VARCHAR(20))")
		mustExec(t, c, "CREATE TABLE b (id INT PRIMARY KEY, v VARCHAR(20))")
		store := fmt.Sprint("quiet-", rand.Int())
		mem := fitsync.MemoryStore(store)
		t.Cleanup(mem.Reset)
		params, _ := json.Marshal(mysqlSourceParams(t, s, map[string]any{"tables": []string{"fit_srcq.a"}, "snapshot": "never"}))
		src := fmt.Sprintf(`{"sync": {"state_dir": %q, "pipelines": [{"name": "q", "checkpoint_interval": "100ms",
			"source": {"module": "mysql", "params": %s},
			"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}, "batch": {"max_wait": "1ms"}}]}]}}`, t.TempDir(), params, store)
		cfg, err := config.Load([]byte(src))
		if err != nil {
			t.Fatal(err)
		}
		start := func() *fitsync.Runtime {
			rt := fitsync.NewRuntime(fitsync.Options{})
			if err := rt.Apply(cfg.Sync); err != nil {
				t.Fatal(err)
			}
			return rt
		}
		wait := func(what string, cond func() bool) {
			t.Helper()
			for deadline := time.Now().Add(2 * time.Minute); !cond(); time.Sleep(20 * time.Millisecond) {
				if time.Now().After(deadline) {
					t.Fatalf("timed out waiting for %s", what)
				}
			}
		}
		before := dumpThreads(t, c)
		rt := start()
		checkpoint := func() string {
			cp, err := rt.Checkpoint("q")
			if err != nil || cp == nil {
				return ""
			}
			return string(cp.Position)
		}
		waitReplica(t, c, before)
		mustExec(t, c, "INSERT INTO a VALUES (1, 'a')")
		wait("the record", func() bool { return len(mem.Records()) == 1 })
		wait("its checkpoint", func() bool { return checkpoint() == string(mem.Records()[0].Position) })
		atRecord := checkpoint()

		var head struct{ File string }
		json.Unmarshal([]byte(atRecord), &head)

		// Only b changes, in binlogs after the record's: the checkpoint
		// follows them.
		binlogLock(t)
		mustExec(t, c, "FLUSH BINARY LOGS")
		mustExec(t, c, "INSERT INTO b VALUES (0, 'b')")
		var past struct{ File string }
		wait("the checkpoint past the record's binlog", func() bool {
			mustExec(t, c, "UPDATE b SET v = ? WHERE id = 0", fmt.Sprint(time.Now().UnixNano()%1e9)) // a transaction of b's each poll
			json.Unmarshal([]byte(checkpoint()), &past)
			return past.File != "" && past.File != head.File
		})
		if st := rt.Status(); st[0].Read != 1 || st[0].Sinks[0].Records != 1 {
			t.Fatalf("%+v", st[0])
		}
		rt.Close(context.Background())

		// The record's binlog purged: the pipeline goes on from its
		// checkpoint, where it would have asked for a new snapshot.
		saved := checkpoint()
		mustExec(t, c, "PURGE BINARY LOGS TO '"+past.File+"'")
		rows, err := c.Query(hangGuard(t), "SHOW BINARY LOGS")
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			if f := fmt.Sprintf("%s", rows.Values()[0]); f == head.File {
				t.Skipf("%s is not purged", f) // MariaDB may keep it for crash recovery
			}
		}
		rows.Close()
		before = dumpThreads(t, c)
		rt = start()
		waitReplica(t, c, before)
		defer rt.Close(context.Background())
		mustExec(t, c, "INSERT INTO a VALUES (2, 'b')")
		wait("the record after the purge", func() bool {
			st := rt.Status()
			if st[0].State == "retrying" || st[0].State == "stopped" {
				t.Fatalf("from %s: %s", saved, st[0].Err)
			}
			return len(mem.Records()) == 2
		})
	})
}

// TestPostgresSourceQuietTable: a pipeline reading a quiet table while
// another table takes transactions moves its checkpoint, and its slot's
// confirmed position, past its last record with position-only records:
// empty transactions before PostgreSQL 15, keepalives from 15 on.
func TestPostgresSourceQuietTable(t *testing.T) {
	eachPG(t, func(t *testing.T, s pgServer) {
		f := newPGSrcFixture(t, s, "a", "b")
		a, b := f.table("a"), f.table("b")
		pgExec(t, f.c, "CREATE TABLE "+a+" (id int PRIMARY KEY)")
		pgExec(t, f.c, "CREATE TABLE "+b+" (id int PRIMARY KEY, v int)")
		store := "pg-quiet-" + f.suffix
		mem := fitsync.MemoryStore(store)
		t.Cleanup(mem.Reset)
		cfg := f.pipeline(t, t.TempDir(), map[string]any{"checkpoint_interval": "100ms",
			"source": map[string]any{"module": "postgres", "params": f.params(map[string]any{"tables": []string{a}})},
			"sinks": []any{map[string]any{"name": "m", "batch": map[string]any{"max_wait": "1ms"},
				"sink": map[string]any{"module": "memory", "params": map[string]any{"store": store}}}}})
		rt := fitsync.NewRuntime(fitsync.Options{})
		defer rt.Close(context.Background())
		if err := rt.Apply(cfg); err != nil {
			t.Fatal(err)
		}
		waitPG(t, "the slot", func() bool {
			return pgOne(t, f.c, "SELECT count(*) FROM pg_replication_slots WHERE slot_name = $1 AND active AND confirmed_flush_lsn IS NOT NULL", f.slot)[0] == int64(1)
		})
		pgExec(t, f.c, "INSERT INTO "+a+" VALUES (1)")
		waitPG(t, "the record", func() bool { return len(mem.Records()) == 1 })
		at := pgPosLSN(t, mem.Records()[0].Position)
		pgExec(t, f.c, "INSERT INTO "+b+" VALUES (1, 0)")
		var cp pgwire.LSN
		waitPG(t, "the checkpoint past the record", func() bool {
			pgExec(t, f.c, "UPDATE "+b+" SET v = v + 1")
			c, err := rt.Checkpoint("p")
			if err != nil || c == nil || c.Position == nil {
				return false
			}
			cp = pgPosLSN(t, fitsync.Position(c.Position))
			return cp > at
		})
		waitPG(t, "the slot past the record", func() bool { return f.confirmedFlush(t) >= cp })
		if st := rt.Status(); st[0].Read != 1 || st[0].Sinks[0].Records != 1 {
			t.Fatalf("%+v", st[0])
		}
	})
}
