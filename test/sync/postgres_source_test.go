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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	_ "github.com/lesismal/fit/sync/postgres"
	pgwire "github.com/lesismal/fit/sync/postgres/wire"
)

// pgSrcFixture is a source postgres's tables, publication and slot on one
// server, removed at the test's end.
type pgSrcFixture struct {
	s                 pgServer
	c                 *pgwire.Conn
	suffix, pub, slot string
}

func newPGSrcFixture(t *testing.T, s pgServer, tables ...string) *pgSrcFixture {
	t.Helper()
	f := &pgSrcFixture{s: s, c: s.connect(t, nil)}
	f.suffix = s.version + "_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	f.pub, f.slot = "srcpub_"+f.suffix, "srcslot_"+f.suffix
	// Large transactions are streamed (14 and later) or spilled (13) past
	// 64kB of changes.
	pgExec(t, f.c, "ALTER SYSTEM SET logical_decoding_work_mem = '64kB'")
	pgExec(t, f.c, "SELECT pg_reload_conf()")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		c, err := pgwire.Connect(ctx, s.config())
		if err != nil {
			t.Log(err)
			return
		}
		defer c.Close()
		// The source's walsender may take a moment to go.
		for range 100 {
			if _, err := c.Exec(ctx, "SELECT pg_drop_replication_slot(slot_name) FROM pg_replication_slots WHERE slot_name = $1", f.slot); err == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		c.Exec(ctx, "DROP PUBLICATION IF EXISTS "+f.pub)
		for _, tb := range tables {
			c.Exec(ctx, "DROP TABLE IF EXISTS "+f.table(tb))
		}
	})
	return f
}

func (f *pgSrcFixture) table(name string) string { return name + "_" + f.suffix }

func (f *pgSrcFixture) params(extra map[string]any) map[string]any {
	p := map[string]any{"address": f.s.addr, "user": os.Getenv("FIT_SYNC_PG_USER"), "password_env": "FIT_TEST_SYNC_PG_PASSWORD",
		"database": "fit", "ca_file": filepath.Join(os.Getenv("FIT_SYNC_PG_CA_DIR"), f.s.version+".pem"),
		"publication": f.pub, "slot": f.slot, "create": true, "status_interval": "100ms", "snapshot": "never"}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

func (f *pgSrcFixture) pipeline(t *testing.T, stateDir string, p map[string]any) *config.Sync {
	t.Helper()
	// The same value for every server, so os.Setenv beside t.Parallel.
	os.Setenv("FIT_TEST_SYNC_PG_PASSWORD", os.Getenv("FIT_SYNC_PG_PASSWORD"))
	p["name"] = "p"
	b, _ := json.Marshal(map[string]any{"sync": map[string]any{"state_dir": stateDir, "pipelines": []any{p}}})
	cfg, err := config.Load(b)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Sync
}

func (f *pgSrcFixture) source(t *testing.T, params map[string]any) fitsync.Source {
	t.Helper()
	cfg := f.pipeline(t, t.TempDir(), map[string]any{"source": map[string]any{"module": "postgres", "params": f.params(params)},
		"sinks": []any{map[string]any{"name": "s", "sink": map[string]any{"module": "memory"}}}})
	src, err := module.Load(fitsync.Sources, cfg.Pipelines[0].Source, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { module.Cleanup(src) })
	return src.(fitsync.Source)
}

// confirmedFlush is the slot's confirmed_flush_lsn.
func (f *pgSrcFixture) confirmedFlush(t *testing.T) pgwire.LSN {
	t.Helper()
	got := pgOne(t, f.c, "SELECT confirmed_flush_lsn::text FROM pg_replication_slots WHERE slot_name = $1", f.slot)
	if got == nil || got[0] == nil {
		return 0
	}
	l, err := pgwire.ParseLSN(got[0].(string))
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// pgSrcRun runs a source on a goroutine, keeping what it emits.
type pgSrcRun struct {
	mu   sync.Mutex
	recs []*fitsync.Record
	stop context.CancelFunc
	done chan error
}

func runPGSource(src fitsync.Source, from fitsync.Position) *pgSrcRun {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute) // a hang guard
	r := &pgSrcRun{stop: cancel, done: make(chan error, 1)}
	go func() {
		r.done <- src.Run(ctx, &fitsync.Run{From: from, Emit: func(rec *fitsync.Record) error {
			r.mu.Lock()
			r.recs = append(r.recs, rec)
			r.mu.Unlock()
			return nil
		}})
	}()
	return r
}

// wait waits for n records.
func (r *pgSrcRun) wait(t *testing.T, n int) []*fitsync.Record {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute) // a hang guard
	for {
		r.mu.Lock()
		got := append([]*fitsync.Record(nil), r.recs...)
		r.mu.Unlock()
		if len(got) >= n {
			return got
		}
		select {
		case err := <-r.done:
			t.Fatalf("the source stopped after %d of %d records: %v", len(got), n, err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d records", len(got), n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (r *pgSrcRun) end(t *testing.T) error {
	t.Helper()
	r.stop()
	return <-r.done
}

// pgTxs splits records into their transactions, checking the boundaries:
// one ID each, First on the first, Last and the position on the last only.
func pgTxs(t *testing.T, recs []*fitsync.Record) [][]*fitsync.Record {
	t.Helper()
	var out [][]*fitsync.Record
	var cur []*fitsync.Record
	var last pgwire.LSN
	for i, r := range recs {
		if r.Tx.First != (len(cur) == 0) || r.Tx.ID == "" || (len(cur) > 0 && r.Tx.ID != cur[0].Tx.ID) || r.Source != "postgres" {
			t.Fatalf("record %d: %+v after %d of its transaction", i, r.Tx, len(cur))
		}
		cur = append(cur, r)
		if (r.Position != nil) != r.Tx.Last {
			t.Fatalf("record %d: last %v with position %s", i, r.Tx.Last, r.Position)
		}
		if r.Tx.Last {
			l := pgPosLSN(t, r.Position)
			if l <= last {
				t.Fatalf("record %d: the position %v after %v", i, l, last)
			}
			last = l
			out = append(out, cur)
			cur = nil
		}
	}
	if cur != nil {
		t.Fatalf("a transaction of %d records without its last", len(cur))
	}
	return out
}

func pgPosLSN(t *testing.T, p fitsync.Position) pgwire.LSN {
	t.Helper()
	var v struct{ LSN string }
	if err := json.Unmarshal(p, &v); err != nil {
		t.Fatal(err)
	}
	l, err := pgwire.ParseLSN(v.LSN)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func pgRecString(r *fitsync.Record) string {
	key, _ := r.Key.AppendJSON(nil)
	ns, _, _ := strings.Cut(strings.TrimPrefix(r.Namespace, "public."), "_")
	return ns + " " + r.Op.String() + " " + string(key)
}

// TestPostgresSourceChanges follows changes across tables and transactions,
// a key changed, a TOASTed value unchanged, REPLICA IDENTITY FULL, a
// truncate and a large transaction with a savepoint rolled back; the slot
// is told only what is confirmed; a restart from a checkpoint reads on
// after it, and a broken connection and a server restart lose nothing.
func TestPostgresSourceChanges(t *testing.T) {
	eachPG(t, func(t *testing.T, s pgServer) {
		f := newPGSrcFixture(t, s, "a", "b", "quiet")
		a, b, quiet := f.table("a"), f.table("b"), f.table("quiet")
		pgExec(t, f.c, "CREATE TABLE "+a+" (id int PRIMARY KEY, v text, big text, at timestamptz, n numeric, tags int[], doc jsonb)")
		pgExec(t, f.c, "CREATE TABLE "+b+" (k int, v text)")
		pgExec(t, f.c, "ALTER TABLE "+b+" REPLICA IDENTITY FULL")
		pgExec(t, f.c, "CREATE TABLE "+quiet+" (id int PRIMARY KEY)")
		src := f.source(t, map[string]any{"tables": []string{a, "public." + b}})
		run := runPGSource(src, nil)
		// The slot is made by the run: wait for it to be made before writing,
		// as a slot has what comes after its consistent point alone.
		deadline := time.Now().Add(time.Minute)
		for pgOne(t, f.c, "SELECT count(*) FROM pg_replication_slots WHERE slot_name = $1 AND active AND confirmed_flush_lsn IS NOT NULL", f.slot)[0] != int64(1) {
			if time.Now().After(deadline) {
				t.Fatal("no slot")
			}
			time.Sleep(10 * time.Millisecond)
		}
		start := f.confirmedFlush(t)

		// md5s compress little: the value is stored out of line, TOASTed.
		big := pgOne(t, f.c, "SELECT string_agg(md5(g::text), '') FROM generate_series(1, 2000) g")[0].(string)
		run1 := func(sql string) {
			t.Helper()
			if _, err := f.c.SimpleQuery(pgCtx(t), sql); err != nil {
				t.Fatalf("%s: %v", sql, err)
			}
		}
		run1("BEGIN; INSERT INTO " + a + " VALUES (1, 'one', '" + big + "', '2026-11-08 10:00:00.5+08', 12.50, '{1,NULL,3}', '{\"x\": 1}'), (2, 'two', NULL, NULL, NULL, NULL, NULL), (3, 'three', NULL, NULL, NULL, NULL, NULL); INSERT INTO " + b + " VALUES (1, 'x'); COMMIT")
		run1("UPDATE " + a + " SET v = 'uno' WHERE id = 1")
		run1("UPDATE " + a + " SET id = 10 WHERE id = 2")
		run1("BEGIN; DELETE FROM " + a + " WHERE id = 3; UPDATE " + b + " SET v = 'y'; COMMIT")
		run1("TRUNCATE " + b)
		run1("BEGIN; INSERT INTO " + a + " SELECT g, 'bulk' FROM generate_series(1000, 20999) g; SAVEPOINT s; INSERT INTO " + a + " SELECT g, 'gone' FROM generate_series(50000, 59999) g; ROLLBACK TO s; INSERT INTO " + a + " SELECT g, 'bulk' FROM generate_series(21000, 30999) g; COMMIT")
		recs := run.wait(t, 4+1+2+3+1+30000)
		txs := pgTxs(t, recs)
		if len(recs) != 30011 || len(txs) != 6 {
			var sizes []string
			for _, tx := range txs {
				sizes = append(sizes, strconv.Itoa(len(tx))+":"+pgRecString(tx[0])+".."+pgRecString(tx[len(tx)-1]))
			}
			t.Fatalf("%d records in %d transactions: %s", len(recs), len(txs), strings.Join(sizes, ", "))
		}
		var head []string
		for _, tx := range txs[:5] {
			for _, r := range tx {
				head = append(head, pgRecString(r))
			}
		}
		want := `a insert {"map":{"id":{"int":1}}}|a insert {"map":{"id":{"int":2}}}|a insert {"map":{"id":{"int":3}}}|b insert {"map":{"k":{"int":1},"v":"x"}}|` +
			`a update {"map":{"id":{"int":1}}}|a delete {"map":{"id":{"int":2}}}|a insert {"map":{"id":{"int":10}}}|` +
			`a delete {"map":{"id":{"int":3}}}|b delete {"map":{"k":{"int":1},"v":"x"}}|b insert {"map":{"k":{"int":1},"v":"y"}}|b ddl null`
		if got := strings.Join(head, "|"); got != want {
			t.Fatalf("records\n%s\nwant\n%s", got, want)
		}
		one := txs[0][0].After
		if v, _ := one.Get("big"); v.Text() != big {
			t.Error("the TOASTed value")
		}
		if v, _ := one.Get("at"); !v.AsTime().Equal(time.Date(2026, 11, 8, 2, 0, 0, 500e6, time.UTC)) || v.Kind() != fitsync.KindTime {
			t.Errorf("at %v", v)
		}
		if v, _ := one.Get("n"); v.Kind() != fitsync.KindDecimal || v.Text() != "12.50" {
			t.Errorf("n %v", v)
		}
		if v, _ := one.Get("tags"); len(v.Elems()) != 3 || v.Elems()[0].AsInt() != 1 || !v.Elems()[1].IsNull() {
			t.Errorf("tags %v", v)
		}
		if v, _ := one.Get("doc"); v.Kind() != fitsync.KindJSON {
			t.Errorf("doc %v", v)
		}
		upd := txs[1][0]
		if _, ok := upd.After.Get("big"); ok {
			t.Error("an unchanged TOASTed value is in the update")
		}
		if v, _ := upd.After.Get("v"); v.Text() != "uno" || !upd.Before.IsNull() {
			t.Errorf("the update %v %v", upd.After, upd.Before)
		}
		if v, _ := txs[3][1].Before.Get("v"); v.Text() != "x" {
			t.Errorf("REPLICA IDENTITY FULL's before %v", txs[3][1].Before)
		}
		if v, _ := txs[4][0].After.Get("statement"); !strings.HasPrefix(v.Text(), "TRUNCATE public.") {
			t.Errorf("the truncate %v", txs[4][0].After)
		}
		for i, r := range txs[5] {
			id, _ := r.Key.Get("id")
			if r.Op != fitsync.OpInsert || id.AsInt() != int64(1000+i) {
				t.Fatalf("the large transaction's record %d: %s", i, pgRecString(r))
			}
		}

		if f.c.ServerVersion() >= 140000 {
			waitPG(t, "the large transaction to be streamed", func() bool {
				got := pgOne(t, f.c, "SELECT stream_txns FROM pg_stat_replication_slots WHERE slot_name = $1", f.slot)
				return got != nil && got[0].(int64) > 0
			})
		}

		// Nothing confirmed: the slot stays where it was made.
		time.Sleep(500 * time.Millisecond)
		if got := f.confirmedFlush(t); got != start {
			t.Fatalf("the slot moved to %v from %v before any checkpoint", got, start)
		}
		pos := func(i int) fitsync.Position { return txs[i][len(txs[i])-1].Position }
		confirmer := src.(fitsync.Confirmer)
		confirmer.Confirm(pos(2))
		waitPG(t, "the slot to take the checkpoint", func() bool { return f.confirmedFlush(t) == pgPosLSN(t, pos(2)) })
		time.Sleep(500 * time.Millisecond)
		if got := f.confirmedFlush(t); got != pgPosLSN(t, pos(2)) {
			t.Fatalf("the slot at %v past the checkpoint %v", got, pgPosLSN(t, pos(2)))
		}

		// A restart from the checkpoint reads what comes after it, from the
		// fourth transaction on.
		if err := run.end(t); !errors.Is(err, context.Canceled) {
			t.Fatalf("the run ended with %v", err)
		}
		run = runPGSource(src, pos(2))
		again := pgTxs(t, run.wait(t, 3+1+30000))
		if len(again) != 3 || pgRecString(again[0][0]) != pgRecString(txs[3][0]) || string(again[2][len(again[2])-1].Position) != string(pos(5)) {
			t.Fatalf("after a restart, %d transactions from %s", len(again), pgRecString(again[0][0]))
		}

		// Everything confirmed, writes to a table not published move the
		// slot on: nothing is owed before them.
		confirmer.Confirm(pos(5))
		// Past it at once when the server says it has read on.
		waitPG(t, "the slot to take the checkpoint", func() bool { return f.confirmedFlush(t) >= pgPosLSN(t, pos(5)) })
		atCheckpoint := f.confirmedFlush(t)
		run1("INSERT INTO " + quiet + " VALUES (1)")
		waitPG(t, "the slot to pass unpublished writes", func() bool { return f.confirmedFlush(t) > atCheckpoint })

		// The connection broken, the source fails, and a run from the
		// checkpoint misses nothing written meanwhile.
		pgExec(t, f.c, "SELECT pg_terminate_backend(active_pid) FROM pg_replication_slots WHERE slot_name = $1", f.slot)
		select {
		case err := <-run.done:
			if err == nil {
				t.Fatal("the run ended without an error on a broken connection")
			}
		case <-time.After(time.Minute):
			t.Fatal("the run did not see its connection broken")
		}
		run1("INSERT INTO " + a + " VALUES (100, 'while away')")
		if prefix := os.Getenv("FIT_SYNC_PG_PREFIX"); prefix != "" {
			// A server restart, when the test knows the containers.
			if out, err := exec.Command("docker", "restart", prefix+s.version).CombinedOutput(); err != nil {
				t.Fatalf("docker restart: %v %s", err, out)
			}
			waitPG(t, "the server to come back", func() bool {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				c, err := pgwire.Connect(ctx, s.config())
				if err == nil {
					f.c = c
				}
				return err == nil
			})
			run1("INSERT INTO " + a + " VALUES (101, 'after the restart')")
		} else {
			run1("INSERT INTO " + a + " VALUES (101, 'no restart')")
		}
		run = runPGSource(src, pos(5))
		last := pgTxs(t, run.wait(t, 2))
		if len(last) != 2 || pgRecString(last[0][0]) != "a insert "+`{"map":{"id":{"int":100}}}` || pgRecString(last[1][0]) != "a insert "+`{"map":{"id":{"int":101}}}` {
			t.Fatalf("after the break: %d transactions", len(last))
		}
		run.end(t)
		t.Logf("checked, the server restarted: %v", os.Getenv("FIT_SYNC_PG_PREFIX") != "")
	})
}

func waitPG(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute) // a hang guard
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestPostgresSourceAcrossKills runs a pipeline from the source to a memory
// sink while transactions are written, killing it at random points as
// kill -9 would and starting it again from its checkpoint: every
// transaction comes whole, none is lost.
func TestPostgresSourceAcrossKills(t *testing.T) {
	eachPG(t, func(t *testing.T, s pgServer) {
		f := newPGSrcFixture(t, s, "k")
		k := f.table("k")
		pgExec(t, f.c, "CREATE TABLE "+k+" (id int PRIMARY KEY, tx int, v text)")
		seed := uint64(time.Now().UnixNano())
		t.Logf("seed %d", seed)
		rnd := rand.New(rand.NewPCG(seed, 9))
		store := "pg-kills-" + f.suffix
		mem := fitsync.MemoryStore(store)
		mem.Reset()
		cfg := f.pipeline(t, t.TempDir(), map[string]any{"checkpoint_interval": "1ms",
			"queue":  map[string]any{"max_records": 20 + rnd.IntN(200)},
			"source": map[string]any{"module": "postgres", "params": f.params(map[string]any{"tables": []string{k}})},
			"sinks": []any{map[string]any{"name": "m", "batch": map[string]any{"max_records": 1 + rnd.IntN(50), "max_wait": "1ms"},
				"sink": map[string]any{"module": "memory", "params": map[string]any{"store": store}}}}})

		const txs = 300
		var wrote atomic.Int64
		var writing sync.WaitGroup
		started := make(chan struct{})
		writing.Go(func() {
			<-started
			c, err := pgwire.Connect(context.Background(), s.config())
			if err != nil {
				t.Error(err)
				return
			}
			defer c.Close()
			id := 0
			for n := range txs {
				var b strings.Builder
				b.WriteString("BEGIN;")
				for range 1 + n%5 {
					fmt.Fprintf(&b, "INSERT INTO %s VALUES (%d, %d, 'v');", k, id, n)
					id++
				}
				b.WriteString("COMMIT")
				if _, err := c.SimpleQuery(context.Background(), b.String()); err != nil {
					t.Error(err)
					return
				}
				wrote.Store(int64(n + 1))
				time.Sleep(time.Millisecond)
			}
		})

		points := []fitsync.Point{fitsync.CrashEmitted, fitsync.CrashDispatched, fitsync.CrashWritten,
			fitsync.CrashAcknowledged, fitsync.CrashCheckpointWritten, fitsync.CrashCheckpointSaved}
		kills := 0
		for round := 0; wrote.Load() < txs || round < 12; round++ {
			at, nth := points[rnd.IntN(len(points))], int32(1+rnd.IntN(100))
			var seen atomic.Int32
			rt := fitsync.NewRuntime(fitsync.Options{Crash: func(_ string, p fitsync.Point) bool {
				return p == at && seen.Add(1) == nth
			}})
			if err := rt.Apply(cfg); err != nil {
				t.Fatal(err)
			}
			if round == 0 {
				// The slot is made by the first run; writes start after it.
				waitPG(t, "the slot", func() bool {
					return pgOne(t, f.c, "SELECT count(*) FROM pg_replication_slots WHERE slot_name = $1 AND confirmed_flush_lsn IS NOT NULL", f.slot)[0] == int64(1)
				})
				close(started)
			}
			deadline := time.Now().Add(time.Duration(100+rnd.IntN(400)) * time.Millisecond)
			for time.Now().Before(deadline) && redisState(rt) != "killed" {
				time.Sleep(time.Millisecond)
			}
			if redisState(rt) == "killed" {
				kills++
			}
			rt.Close(context.Background())
		}
		writing.Wait()
		rt := fitsync.NewRuntime(fitsync.Options{})
		if err := rt.Apply(cfg); err != nil {
			t.Fatal(err)
		}
		total := 0
		for n := range txs {
			total += 1 + n%5
		}
		seen := func() map[int64]bool {
			ids := map[int64]bool{}
			for _, r := range mem.Records() {
				id, _ := r.Key.Get("id")
				ids[id.AsInt()] = true
			}
			return ids
		}
		waitPG(t, "every row", func() bool { return len(seen()) == total })
		rt.Close(context.Background())
		recs := mem.Records()
		t.Logf("%d transactions, %d records written for %d rows, across %d kills", txs, len(recs), total, kills)
	})
}
