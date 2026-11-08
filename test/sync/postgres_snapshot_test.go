package synctest

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	fitsync "github.com/lesismal/fit/sync"
	pgwire "github.com/lesismal/fit/sync/postgres/wire"
)

// pgSnapText is a value as the tests compare rows: ints in decimal, NULL,
// and the text of the rest.
func pgSnapText(v fitsync.Value) string {
	switch v.Kind() {
	case fitsync.KindNull:
		return "NULL"
	case fitsync.KindInt:
		return strconv.FormatInt(v.AsInt(), 10)
	}
	return v.Text()
}

// pgConverge applies records by key, as a sink writing by key does, and
// returns each namespace's rows as text, sorted.
func pgConverge(recs []*fitsync.Record) map[string][]string {
	state := map[string]map[string]string{}
	for _, r := range recs {
		ns, _, _ := strings.Cut(strings.TrimPrefix(r.Namespace, "public."), "_")
		if state[ns] == nil {
			state[ns] = map[string]string{}
		}
		key, _ := r.Key.AppendJSON(nil)
		switch r.Op {
		case fitsync.OpInsert, fitsync.OpUpdate:
			var cols []string
			for _, f := range r.After.Fields() {
				cols = append(cols, pgSnapText(f.Value))
			}
			state[ns][string(key)] = strings.Join(cols, "|")
		case fitsync.OpDelete:
			delete(state[ns], string(key))
		}
	}
	out := map[string][]string{}
	for ns, rows := range state {
		for _, r := range rows {
			out[ns] = append(out[ns], r)
		}
		slices.Sort(out[ns])
	}
	return out
}

// pgTableRows is a table's rows as pgConverge has them.
func pgTableRows(t *testing.T, c *pgwire.Conn, table string, cols ...string) []string {
	t.Helper()
	var exprs []string
	for _, col := range cols {
		exprs = append(exprs, "coalesce("+col+"::text, 'NULL')")
	}
	res, err := c.SimpleQuery(pgCtx(t), "SELECT concat_ws('|', "+strings.Join(exprs, ", ")+") FROM "+table)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range res[0].Rows {
		out = append(out, string(r[0]))
	}
	slices.Sort(out)
	return out
}

// TestPostgresSnapshotExact reads a table's snapshot while rows are
// inserted, through a slot made with the snapshot exported and through a
// temporary slot beside a slot that exists: every row comes exactly once,
// from the snapshot or from the stream, never both, none missed.
func TestPostgresSnapshotExact(t *testing.T) {
	eachPG(t, func(t *testing.T, s pgServer) {
		for _, existing := range []bool{false, true} {
			t.Run(fmt.Sprintf("existing slot %v", existing), func(t *testing.T) {
				f := newPGSrcFixture(t, s, "x")
				x := f.table("x")
				pgExec(t, f.c, "CREATE TABLE "+x+" (id int PRIMARY KEY, v text)")
				pgExec(t, f.c, "INSERT INTO "+x+" SELECT g, 'old' FROM generate_series(1, 3000) g")
				if existing {
					pgExec(t, f.c, "CREATE PUBLICATION "+f.pub+" FOR TABLE "+x)
					pgExec(t, f.c, "SELECT pg_create_logical_replication_slot($1, 'pgoutput')", f.slot)
				}
				var stop atomic.Bool
				var next atomic.Int64
				next.Store(3001)
				var writing sync.WaitGroup
				writing.Go(func() {
					c, err := pgwire.Connect(context.Background(), s.config())
					if err != nil {
						t.Error(err)
						return
					}
					defer c.Close()
					for !stop.Load() {
						id := next.Add(1) - 1
						if _, err := c.SimpleQuery(context.Background(), fmt.Sprintf("INSERT INTO %s VALUES (%d, 'new')", x, id)); err != nil {
							t.Error(err)
							return
						}
					}
				})
				src := f.source(t, map[string]any{"tables": []string{x}, "snapshot": "initial", "chunk_rows": 400, "snapshot_parallel": 3})
				run := runPGSource(src, nil)
				run.wait(t, 3000)
				time.Sleep(200 * time.Millisecond)
				stop.Store(true)
				writing.Wait()
				total := int(next.Load() - 1)
				run.wait(t, total)
				time.Sleep(200 * time.Millisecond) // anything more would be a duplicate
				run.mu.Lock()
				recs := append([]*fitsync.Record(nil), run.recs...)
				run.mu.Unlock()
				run.end(t)
				seen := map[int64]int{}
				snapshot := 0
				for _, r := range recs {
					id, _ := r.Key.Get("id")
					seen[id.AsInt()]++
					if r.Tx.ID == "" {
						snapshot++
					}
				}
				if len(recs) != total || len(seen) != total {
					t.Fatalf("%d records of %d ids for %d rows", len(recs), len(seen), total)
				}
				t.Logf("%d rows: %d from the snapshot, %d from the stream", total, snapshot, total-snapshot)
			})
		}
	})
}

// TestPostgresSnapshotConverges reads tables with a primary key, a
// composite one and none (REPLICA IDENTITY FULL) while they are written to,
// through the runtime into a memory sink; the run is killed part way, so
// the next resumes the snapshot after its exported one is gone and reads
// the unfinished tables again; the sink ends equal to the tables.
func TestPostgresSnapshotConverges(t *testing.T) {
	eachPG(t, func(t *testing.T, s pgServer) {
		f := newPGSrcFixture(t, s, "a", "b", "c")
		a, b, c := f.table("a"), f.table("b"), f.table("c")
		pgExec(t, f.c, "CREATE TABLE "+a+" (id int PRIMARY KEY, v text, n numeric)")
		pgExec(t, f.c, "CREATE TABLE "+b+" (k int, v text)")
		pgExec(t, f.c, "ALTER TABLE "+b+" REPLICA IDENTITY FULL")
		pgExec(t, f.c, "CREATE TABLE "+c+" (k1 text, k2 int, v text, PRIMARY KEY (k1, k2))")
		pgExec(t, f.c, "INSERT INTO "+a+" SELECT g, 'a' || g, g * 1.5 FROM generate_series(1, 5000) g")
		pgExec(t, f.c, "INSERT INTO "+b+" SELECT g, 'b' FROM generate_series(1, 50) g")
		pgExec(t, f.c, "INSERT INTO "+c+" SELECT 'k' || (g % 7), g, 'c' FROM generate_series(1, 1200) g")

		seed := uint64(time.Now().UnixNano())
		t.Logf("seed %d", seed)
		var stop atomic.Bool
		var writing sync.WaitGroup
		writing.Go(func() {
			rnd := rand.New(rand.NewPCG(seed, 3))
			conn, err := pgwire.Connect(context.Background(), s.config())
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			next := 10000
			for !stop.Load() {
				var sql string
				switch rnd.IntN(6) {
				case 0:
					sql = fmt.Sprintf("UPDATE %s SET v = 'u%d', n = n + 1 WHERE id = %d", a, rnd.IntN(1000), 1+rnd.IntN(5000))
				case 1:
					sql = fmt.Sprintf("DELETE FROM %s WHERE id = %d", a, 1+rnd.IntN(5000))
				case 2:
					next++
					sql = fmt.Sprintf("INSERT INTO %s VALUES (%d, 'i', NULL)", a, next)
				case 3:
					sql = fmt.Sprintf("UPDATE %s SET v = 'u%d' WHERE k = %d", b, rnd.IntN(1000), 1+rnd.IntN(50))
				case 4:
					sql = fmt.Sprintf("UPDATE %s SET v = 'u%d' WHERE k2 = %d", c, rnd.IntN(1000), 1+rnd.IntN(1200))
				default:
					sql = fmt.Sprintf("UPDATE %s SET id = %d WHERE id = %d", a, 20000+rnd.IntN(100000), 1+rnd.IntN(5000))
				}
				if _, err := conn.SimpleQuery(context.Background(), sql); err != nil && !strings.Contains(err.Error(), "23505") {
					t.Error(err)
					return
				}
				time.Sleep(time.Duration(rnd.IntN(500)) * time.Microsecond)
			}
		})

		store := "pg-snapshot-" + f.suffix
		mem := fitsync.MemoryStore(store)
		mem.Reset()
		cfg := f.pipeline(t, t.TempDir(), map[string]any{"checkpoint_interval": "1ms",
			"source": map[string]any{"module": "postgres", "params": f.params(map[string]any{"tables": []string{a, b, c},
				"snapshot": "initial", "chunk_rows": 600, "snapshot_parallel": 3})},
			"sinks": []any{map[string]any{"name": "m", "batch": map[string]any{"max_records": 50, "max_wait": "1ms"},
				"sink": map[string]any{"module": "memory", "params": map[string]any{"store": store}}}}})

		var saved atomic.Int32
		rt := fitsync.NewRuntime(fitsync.Options{Crash: func(_ string, p fitsync.Point) bool {
			return p == fitsync.CrashCheckpointSaved && saved.Add(1) == 5
		}})
		if err := rt.Apply(cfg); err != nil {
			t.Fatal(err)
		}
		waitPG(t, "the kill", func() bool { return redisState(rt) == "killed" })
		rt.Close(context.Background())
		t.Logf("killed after %d records", len(mem.Records()))

		rt = fitsync.NewRuntime(fitsync.Options{})
		if err := rt.Apply(cfg); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
		stop.Store(true)
		writing.Wait()
		want := map[string][]string{"a": pgTableRows(t, f.c, a, "id", "v", "n"), "b": pgTableRows(t, f.c, b, "k", "v"), "c": pgTableRows(t, f.c, c, "k1", "k2", "v")}
		var got map[string][]string
		deadline := time.Now().Add(3 * time.Minute) // a hang guard
		for {
			got = pgConverge(mem.Records())
			if slices.Equal(got["a"], want["a"]) && slices.Equal(got["b"], want["b"]) && slices.Equal(got["c"], want["c"]) {
				break
			}
			if time.Now().After(deadline) {
				for _, ns := range []string{"a", "b", "c"} {
					t.Logf("%s: %d rows in the sink, %d in the table", ns, len(got[ns]), len(want[ns]))
				}
				t.Fatal("the sink differs from the tables")
			}
			time.Sleep(50 * time.Millisecond)
		}
		rt.Close(context.Background())
		t.Logf("%d records for %d rows", len(mem.Records()), len(want["a"])+len(want["b"])+len(want["c"]))
	})
}

// TestPostgresSnapshotJob splits a job's tables into chunks by key and
// reads them: every row once.
func TestPostgresSnapshotJob(t *testing.T) {
	eachPG(t, func(t *testing.T, s pgServer) {
		f := newPGSrcFixture(t, s, "j", "k")
		j, k := f.table("j"), f.table("k")
		pgExec(t, f.c, "CREATE TABLE "+j+" (a text, b int, PRIMARY KEY (a, b))")
		pgExec(t, f.c, "CREATE TABLE "+k+" (v int)")
		pgExec(t, f.c, "INSERT INTO "+j+" SELECT 'x' || (g % 13), g FROM generate_series(1, 4000) g")
		pgExec(t, f.c, "INSERT INTO "+k+" SELECT g FROM generate_series(1, 10) g")
		src := f.source(t, map[string]any{"tables": []string{j, k}, "snapshot": "initial", "chunk_rows": 700})
		ctx := pgCtx(t)
		plan, err := src.(fitsync.Chunker).Chunks(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(plan) != 7 {
			t.Fatalf("the plan %+v", plan)
		}
		seen := map[string]int{}
		for _, ch := range plan {
			err := src.(fitsync.Chunker).ReadChunk(ctx, ch, nil, func(r *fitsync.Record) error {
				after, _ := r.After.AppendJSON(nil)
				seen[string(after)]++
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		if len(seen) != 4010 {
			t.Fatalf("%d rows read", len(seen))
		}
		for row, n := range seen {
			if n != 1 {
				t.Fatalf("%s read %d times", row, n)
			}
		}
		// A chunk resumed after its first key.
		n := 0
		from := fitsync.Position(`{"key":` + string(plan[1].Lo) + `}`)
		if err := src.(fitsync.Chunker).ReadChunk(ctx, plan[1], from, func(*fitsync.Record) error { n++; return nil }); err != nil || n != 699 {
			t.Fatal(n, err)
		}
	})
}
