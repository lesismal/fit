package synctest

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	_ "github.com/lesismal/fit/sync/mssql"
	mssqlwire "github.com/lesismal/fit/sync/mssql/wire"
)

// mssqlSourceParams are the source mssql's params for s, with extra.
func mssqlSourceParams(t *testing.T, s mssqlServer, extra map[string]any) map[string]any {
	t.Setenv("FIT_TEST_SYNC_MSSQL_PASSWORD", s.password)
	p := map[string]any{"address": s.addr, "username": "sa", "password_env": "FIT_TEST_SYNC_MSSQL_PASSWORD", "database": "fit_sync",
		"ca_file": os.Getenv("FIT_SYNC_MSSQL_CA"), "server_name": "localhost", "poll_interval": "100ms"}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

var captureJobOnce sync.Map

// mssqlCapture turns CDC on for dbo.table, the capture job polling every
// second (the default is five).
func mssqlCapture(t *testing.T, s mssqlServer, c *mssqlwire.Conn, table string) {
	t.Helper()
	mssqlExec(t, c, "EXEC sys.sp_cdc_enable_table @source_schema = N'dbo', @source_name = @p1, @role_name = NULL", table)
	if _, done := captureJobOnce.LoadOrStore(s.addr, true); done {
		return
	}
	_, rows, err := c.QueryAll(context.Background(), "SELECT pollinginterval FROM msdb.dbo.cdc_jobs WHERE job_type = N'capture' AND database_id = DB_ID()")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 1 && rows[0][0] == int64(1) {
		return
	}
	mssqlExec(t, c, "EXEC sys.sp_cdc_change_job @job_type = N'capture', @pollinginterval = 1")
	// The job runs again with its new interval: a start refused because
	// one is pending starts it as well.
	c.Exec(context.Background(), "EXEC sys.sp_cdc_stop_job @job_type = N'capture'")
	time.Sleep(time.Second)
	c.Exec(context.Background(), "EXEC sys.sp_cdc_start_job @job_type = N'capture'")
}

const mssqlLoadColumns = "(id int PRIMARY KEY, v nvarchar(50) NULL, n decimal(10,2) NULL, at datetime2(3) NULL, b varbinary(20) NULL)"

// mssqlRow writes a row's values as the comparison does.
func mssqlRow(id int64, v any, n string, at time.Time, b []byte) string {
	return fmt.Sprintf("%d|%v|%s|%s|%x", id, v, n, at.UTC().Format(time.RFC3339Nano), b)
}

// mssqlRows reads a table's rows, by key.
func mssqlRows(t *testing.T, c *mssqlwire.Conn, table string) map[int64]string {
	t.Helper()
	_, rows, err := c.QueryAll(context.Background(), "SELECT id, v, n, at, b FROM dbo."+table)
	if err != nil {
		t.Fatal(err)
	}
	out := map[int64]string{}
	for _, r := range rows {
		at, _ := r[3].(time.Time)
		b, _ := r[4].([]byte)
		n := ""
		if r[2] != nil {
			n = string(r[2].(mssqlwire.Decimal))
		}
		v := r[1]
		if v == nil {
			v = "<nil>"
		}
		out[r[0].(int64)] = mssqlRow(r[0].(int64), v, n, at, b)
	}
	return out
}

// replay applies a store's records of namespace dbo.table to an empty
// table, by key.
func replay(recs []*fitsync.Record, table string) map[int64]string {
	out := map[int64]string{}
	for _, r := range recs {
		if r.Namespace != "dbo."+table || r.Op == fitsync.OpDDL {
			continue
		}
		id, _ := r.Key.Get("id")
		if r.Op == fitsync.OpUpdate && r.Before.Kind() == fitsync.KindMap {
			if old, ok := r.Before.Get("id"); ok && old.AsInt() != id.AsInt() {
				delete(out, old.AsInt())
			}
		}
		if r.Op == fitsync.OpDelete {
			delete(out, id.AsInt())
			continue
		}
		get := func(name string) fitsync.Value { v, _ := r.After.Get(name); return v }
		var v any = "<nil>"
		if x := get("v"); !x.IsNull() {
			v = x.Text()
		}
		out[id.AsInt()] = mssqlRow(id.AsInt(), v, get("n").Text(), get("at").AsTime(), []byte(get("b").Text()))
	}
	return out
}

// mssqlWriter writes random transactions to a table: inserts, updates,
// deletes and key changes, several in a transaction now and then.
type mssqlWriter struct {
	table string
	rnd   *rand.Rand
	next  int64
	ids   []int64
	n     int
}

func (w *mssqlWriter) stmt() string {
	at := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC).Add(time.Duration(w.rnd.IntN(1e6)) * time.Millisecond).Format("2006-01-02T15:04:05.000")
	switch k := w.rnd.IntN(10); {
	case k < 4 || len(w.ids) < 5:
		w.next++
		w.ids = append(w.ids, w.next)
		return fmt.Sprintf("INSERT INTO dbo.%s (id, v, n, at, b) VALUES (%d, N'ü-%d', %d.%02d, '%s', 0x%02x)", w.table, w.next, w.next, w.rnd.IntN(1000), w.rnd.IntN(100), at, w.rnd.IntN(256))
	case k < 7:
		id := w.ids[w.rnd.IntN(len(w.ids))]
		return fmt.Sprintf("UPDATE dbo.%s SET v = N'u-%d', n = NULL, at = '%s' WHERE id = %d", w.table, w.rnd.IntN(1e6), at, id)
	case k < 9:
		i := w.rnd.IntN(len(w.ids))
		id := w.ids[i]
		w.ids = append(w.ids[:i], w.ids[i+1:]...)
		return fmt.Sprintf("DELETE FROM dbo.%s WHERE id = %d", w.table, id)
	default:
		i := w.rnd.IntN(len(w.ids))
		w.next++
		old := w.ids[i]
		w.ids[i] = w.next
		return fmt.Sprintf("UPDATE dbo.%s SET id = %d WHERE id = %d", w.table, w.next, old)
	}
}

func (w *mssqlWriter) write(ctx context.Context, c *mssqlwire.Conn, txs int) error {
	for range txs {
		var stmts []string
		for range 1 + w.rnd.IntN(4) {
			stmts = append(stmts, w.stmt())
		}
		if _, err := c.Exec(ctx, "BEGIN TRANSACTION; "+strings.Join(stmts, "; ")+"; COMMIT TRANSACTION"); err != nil {
			return err
		}
		w.n++
	}
	return nil
}

// fill writes n rows, ids 1 to n.
func (w *mssqlWriter) fill(t *testing.T, c *mssqlwire.Conn, n int) {
	t.Helper()
	b, err := c.BulkInsert(context.Background(), "dbo."+w.table, []string{"id", "v", "n", "at", "b"}, mssqlwire.BulkOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for range n {
		w.next++
		w.ids = append(w.ids, w.next)
		if err := b.WriteRow(w.next, fmt.Sprint("fill-", w.next), mssqlwire.Decimal("1.25"), time.Date(2026, 11, 9, 1, 2, 3, 456000000, time.UTC), []byte{byte(w.next)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := b.Close(); err != nil {
		t.Fatal(err)
	}
}

// mssqlCopyConfig is a pipeline from the source mssql to a memory store.
func mssqlCopyConfig(t *testing.T, s mssqlServer, rnd *rand.Rand, store string, srcExtra map[string]any) *config.Sync {
	t.Helper()
	pipeline := map[string]any{"name": "p", "on_error": "stop", "checkpoint_interval": "1ms",
		"retry":  map[string]any{"max_attempts": 1000, "backoff": "50ms", "max_backoff": "1s"},
		"queue":  map[string]any{"max_records": 20 + rnd.IntN(300)},
		"source": map[string]any{"module": "mssql", "params": mssqlSourceParams(t, s, srcExtra)},
		"sinks": []any{map[string]any{"name": "mem", "batch": map[string]any{"max_records": 1 + rnd.IntN(60), "max_wait": "1ms"},
			"sink": map[string]any{"module": "memory", "params": map[string]any{"store": store, "max_records": 10000000}}}}}
	b, _ := json.Marshal(map[string]any{"sync": map[string]any{"state_dir": t.TempDir(), "pipelines": []any{pipeline}}})
	cfg, err := config.Load(b)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Sync
}

// waitEqual waits until the store's records replayed equal the table.
func waitEqual(t *testing.T, c *mssqlwire.Conn, store, table string, rt *fitsync.Runtime) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for {
		want, got := mssqlRows(t, c, table), replay(fitsync.MemoryStore(store).Records(), table)
		if fmt.Sprint(want) == fmt.Sprint(got) {
			return
		}
		if time.Now().After(deadline) {
			var diff []string
			for id, w := range want {
				if got[id] != w {
					diff = append(diff, fmt.Sprintf("%d: %q, copied %q", id, w, got[id]))
				}
			}
			for id, g := range got {
				if _, ok := want[id]; !ok {
					diff = append(diff, fmt.Sprintf("%d: absent, copied %q", id, g))
				}
			}
			sort.Strings(diff)
			t.Fatalf("%d rows, %d copied, %d differ: %s; %+v", len(want), len(got), len(diff), strings.Join(diff[:min(len(diff), 10)], "; "), rt.Status())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestMSSQLSourceAcrossKills reads a table of 500 rows written to while
// its snapshot is read, a page of 50 at a time, then its changes, the
// pipeline killed at random points again and again: the records replayed
// end equal to the table.
func TestMSSQLSourceAcrossKills(t *testing.T) {
	eachMSSQL(t, func(t *testing.T, s mssqlServer) {
		for _, mode := range []string{"cdc", "change_tracking"} {
			t.Run(mode, func(t *testing.T) {
				seed := uint64(time.Now().UnixNano())
				t.Logf("seed %d", seed)
				rnd := rand.New(rand.NewPCG(seed, 9))
				c := s.dial(t, mssqlwire.EncryptOn)
				table := fmt.Sprintf("kills_%d", time.Now().UnixNano())
				mssqlExec(t, c, "CREATE TABLE dbo."+table+" "+mssqlLoadColumns)
				t.Cleanup(func() { c.Exec(context.Background(), "DROP TABLE dbo."+table) })
				if mode == "cdc" {
					mssqlCapture(t, s, c, table)
				} else {
					mssqlExec(t, c, "ALTER TABLE dbo."+table+" ENABLE CHANGE_TRACKING")
				}
				w := &mssqlWriter{table: table, rnd: rand.New(rand.NewPCG(seed, 7))}
				w.fill(t, c, 500)
				store := "mssql-" + table
				defer fitsync.MemoryStore(store).Reset()
				cfg := mssqlCopyConfig(t, s, rnd, store, map[string]any{"tables": []string{"dbo." + table}, "batch_size": 50, "mode": mode,
					"window_transactions": 1 + rnd.IntN(20), "window_rows": 2 + rnd.IntN(30)})

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
				waitEqual(t, c, store, table, rt)
				t.Logf("%d rows after %d transactions, equal across %d kills", len(w.ids), w.n, kills)
			})
		}
	})
}

// TestMSSQLSourceAddedTable follows the tables of a glob, one captured
// when the pipeline starts, a second captured while it runs, with rows
// already in it: the second is read whole, then followed; a column added
// to the first is a DDL record.
func TestMSSQLSourceAddedTable(t *testing.T) {
	eachMSSQL(t, func(t *testing.T, s mssqlServer) {
		rnd := rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 3))
		c := s.dial(t, mssqlwire.EncryptOn)
		prefix := fmt.Sprintf("added%d_", time.Now().UnixNano())
		first, second := prefix+"a", prefix+"b"
		for _, tb := range []string{first, second} {
			mssqlExec(t, c, "CREATE TABLE dbo."+tb+" "+mssqlLoadColumns)
			t.Cleanup(func() { c.Exec(context.Background(), "DROP TABLE dbo."+tb) })
		}
		mssqlCapture(t, s, c, first)
		w1 := &mssqlWriter{table: first, rnd: rand.New(rand.NewPCG(1, 2))}
		w1.fill(t, c, 20)
		store := "mssql-" + prefix
		defer fitsync.MemoryStore(store).Reset()
		cfg := mssqlCopyConfig(t, s, rnd, store, map[string]any{"tables": []string{"dbo." + prefix + "*"}})
		rt := fitsync.NewRuntime(fitsync.Options{})
		if err := rt.Apply(cfg); err != nil {
			t.Fatal(err)
		}
		defer rt.Close(context.Background())
		waitEqual(t, c, store, first, rt)

		w2 := &mssqlWriter{table: second, rnd: rand.New(rand.NewPCG(3, 4))}
		w2.fill(t, c, 30)
		mssqlCapture(t, s, c, second)
		ctx := context.Background()
		if err := w2.write(ctx, c, 20); err != nil {
			t.Fatal(err)
		}
		if err := w1.write(ctx, c, 20); err != nil {
			t.Fatal(err)
		}
		waitEqual(t, c, store, second, rt)
		waitEqual(t, c, store, first, rt)
		mssqlExec(t, c, "ALTER TABLE dbo."+first+" ADD extra int NULL")
		w1.write(ctx, c, 1)
		deadline := time.Now().Add(2 * time.Minute)
		for {
			found := false
			for _, r := range fitsync.MemoryStore(store).Records() {
				if r.Op == fitsync.OpDDL && r.Namespace == "dbo."+first {
					if st, _ := r.After.Get("statement"); strings.Contains(st.Text(), "extra") {
						found = true
					}
				}
			}
			if found {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("no DDL record; %+v", rt.Status())
			}
			time.Sleep(100 * time.Millisecond)
		}
	})
}

// TestMSSQLSourceChecks: a table named that CDC does not capture fails
// the connection test, saying how to turn it on.
func TestMSSQLSourceChecks(t *testing.T) {
	eachMSSQL(t, func(t *testing.T, s mssqlServer) {
		c := s.dial(t, mssqlwire.EncryptOn)
		table := fmt.Sprintf("plain_%d", time.Now().UnixNano())
		mssqlExec(t, c, "CREATE TABLE dbo."+table+" "+mssqlLoadColumns)
		t.Cleanup(func() { c.Exec(context.Background(), "DROP TABLE dbo."+table) })
		src := mssqlSourceAt(t, s, map[string]any{"tables": []string{"dbo." + table}})
		err := src.(interface{ Test(context.Context) error }).Test(context.Background())
		if err == nil || !strings.Contains(err.Error(), "sp_cdc_enable_table") {
			t.Fatal(err)
		}
	})
}

// TestMSSQLSourceDroppedInstance: an older capture instance dropped, while
// the source runs, before it read that instance's changes up to the newer
// one's start, fails the source asking for a new snapshot, rather than
// losing them unnoticed.
func TestMSSQLSourceDroppedInstance(t *testing.T) {
	eachMSSQL(t, func(t *testing.T, s mssqlServer) {
		c := s.dial(t, mssqlwire.EncryptOn)
		table := fmt.Sprintf("dropped_%d", time.Now().UnixNano())
		mssqlExec(t, c, "CREATE TABLE dbo."+table+" "+mssqlLoadColumns)
		t.Cleanup(func() { c.Exec(context.Background(), "DROP TABLE dbo."+table) })
		mssqlCapture(t, s, c, table)
		older, newer := "dbo_"+table, table+"_2"
		src := mssqlSourceAt(t, s, map[string]any{"tables": []string{"dbo." + table}, "snapshot": "never"})
		// The source holds its first record until the older instance is
		// dropped: the changes after it are then unread.
		held, gate := make(chan struct{}), make(chan struct{})
		var once sync.Once
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		started := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			done <- src.Run(ctx, &fitsync.Run{Emit: func(*fitsync.Record) error {
				once.Do(func() { close(held); <-gate })
				return nil
			}, Start: func(fitsync.Position) error { close(started); return nil }})
		}()
		<-started
		mssqlExec(t, c, "INSERT INTO dbo."+table+" (id, v) VALUES (1, N'a')")
		select {
		case <-held:
		case err := <-done:
			t.Fatal(err)
		case <-ctx.Done():
			t.Fatal("no record")
		}
		mssqlExec(t, c, "INSERT INTO dbo."+table+" (id, v) VALUES (2, N'b')")
		mssqlExec(t, c, "EXEC sys.sp_cdc_enable_table @source_schema = N'dbo', @source_name = @p1, @role_name = NULL, @capture_instance = @p2", table, newer)
		mssqlExec(t, c, "INSERT INTO dbo."+table+" (id, v) VALUES (3, N'c')")
		// Both instances have row 3 once the capture job read it.
		for {
			_, rows, err := c.QueryAll(ctx, "SELECT COUNT(*) FROM cdc."+newer+"_CT")
			if err != nil {
				t.Fatal(err)
			}
			if rows[0][0] == int64(1) {
				break
			}
			if ctx.Err() != nil {
				t.Fatal("the capture job did not read row 3")
			}
			time.Sleep(200 * time.Millisecond)
		}
		mssqlExec(t, c, "EXEC sys.sp_cdc_disable_table @source_schema = N'dbo', @source_name = @p1, @capture_instance = @p2", table, older)
		close(gate)
		select {
		case err := <-done:
			if err == nil || !strings.Contains(err.Error(), "capture instance "+older+" of dbo."+table+" was dropped") {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("the source did not fail")
		}
	})
}

func mssqlSourceAt(t *testing.T, s mssqlServer, extra map[string]any) fitsync.Source {
	t.Helper()
	b, _ := json.Marshal(mssqlSourceParams(t, s, extra))
	v, err := config.ParseValue(b, "params")
	if err != nil {
		t.Fatal(err)
	}
	m, err := module.Load(fitsync.Sources, config.ModuleRef{Module: "mssql", Params: v}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { module.Cleanup(m) })
	return m.(fitsync.Source)
}
