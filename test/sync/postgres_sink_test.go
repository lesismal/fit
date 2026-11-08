package synctest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	pgsink "github.com/lesismal/fit/sync/postgres/sink"
	pgwire "github.com/lesismal/fit/sync/postgres/wire"
)

func init() {
	module.Register(module.Info{ID: fitsync.Sources + ".pgsink_mixed", New: func() any { return new(pgSinkMixed) },
		Doc: "A mixed load for the postgres sink's tests: inserts, updates and deletes of a few keys, in transactions."})
}

// pgSinkMixed makes Total records of a mixed load over 37 keys of two
// columns, in transactions of three: each n an upsert of the key n%37 with
// v = n, or, for n%5 == 4, its delete.
type pgSinkMixed struct {
	Total int64 `json:"total"`
}

func pgSinkMixedRecord(n, total int64) *fitsync.Record {
	k := n % 37
	key := fitsync.Map(fitsync.Field{Name: "a", Value: fitsync.Int(k)}, fitsync.Field{Name: "b", Value: fitsync.String(fmt.Sprintf("k%d", k%3))})
	r := &fitsync.Record{Namespace: "public.mixed", Key: key, Position: fitsync.Position(fmt.Sprintf(`{"n":%d}`, n+1)),
		Tx: fitsync.Tx{ID: fmt.Sprintf("tx-%d", n/3), First: n%3 == 0, Last: n%3 == 2 || n == total-1}}
	switch {
	case n%5 == 4:
		r.Op, r.Before = fitsync.OpDelete, key
	case n%5 == 1:
		r.Op = fitsync.OpUpdate
	default:
		r.Op = fitsync.OpInsert
	}
	if r.Op != fitsync.OpDelete {
		r.After = fitsync.Map(append(key.Fields(), fitsync.Field{Name: "v", Value: fitsync.Int(n)},
			fitsync.Field{Name: "at", Value: fitsync.Time(time.Unix(1_700_000_000+n, 0).UTC())})...)
	}
	return r
}

func (m *pgSinkMixed) Run(ctx context.Context, r *fitsync.Run) error {
	var n int64
	if r.From != nil {
		var p struct{ N int64 }
		if err := json.Unmarshal(r.From, &p); err != nil {
			return err
		}
		n = p.N
	}
	for ; n < m.Total; n++ {
		if err := r.Emit(pgSinkMixedRecord(n, m.Total)); err != nil {
			return err
		}
	}
	return nil
}

// pgSinkParams are the sink's params for s, over TLS with SCRAM.
func pgSinkParams(s pgServer, more map[string]any) map[string]any {
	p := map[string]any{"address": s.addr, "user": os.Getenv("FIT_SYNC_PG_USER"), "password_env": "FIT_SYNC_PG_PASSWORD",
		"database": "fit", "ca_file": filepath.Join(os.Getenv("FIT_SYNC_PG_CA_DIR"), s.version+".pem")}
	for k, v := range more {
		p[k] = v
	}
	return p
}

func pgSinkLoad(t *testing.T, params map[string]any) *pgsink.Sink {
	t.Helper()
	b, _ := json.Marshal(params)
	v, err := config.ParseValue(b, "params")
	if err != nil {
		t.Fatal(err)
	}
	m, err := module.Load(fitsync.Sinks, config.ModuleRef{Module: "postgres", Params: v}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { module.Cleanup(m) })
	return m.(*pgsink.Sink)
}

// pgSum is the checksum of a table's rows, in key order.
func pgSum(t *testing.T, c *pgwire.Conn, table, order string) string {
	t.Helper()
	row := pgOne(t, c, fmt.Sprintf("SELECT count(*)::text || ':' || coalesce(md5(string_agg(x::text, ',' ORDER BY %s)), '') FROM %s x", order, table))
	return fmt.Sprint(row[0])
}

// TestPostgresSinkTypes writes generate's records, of every value type,
// inserted and copied, to tables of the matching PostgreSQL types: the
// same rows both ways, written again the same, and the values as the
// docs' mapping says.
func TestPostgresSinkTypes(t *testing.T) {
	eachPG(t, func(t *testing.T, s pgServer) {
		c := s.connect(t, nil)
		pgExec(t, c, "DROP SCHEMA IF EXISTS sinktypes CASCADE")
		pgExec(t, c, "CREATE SCHEMA sinktypes")
		for _, name := range []string{"inserted", "copied"} {
			pgExec(t, c, `CREATE TABLE sinktypes.`+name+` (id bigint PRIMARY KEY, name text, count numeric(20), score float8,
				price numeric(12,2), even boolean, blob bytea, at timestamptz, day date, doc jsonb, tags text[], none text)`)
		}
		const total = 500
		var recs []*fitsync.Record
		for n := range int64(total) {
			recs = append(recs, fitsync.GenerateRecord("records", n, total, 1))
		}
		for name, copy := range map[string]string{"inserted": "never", "copied": "always"} {
			sink := pgSinkLoad(t, pgSinkParams(s, map[string]any{"table": "sinktypes." + name, "copy": copy}))
			if err := sink.Test(pgCtx(t)); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				for i := 0; i < total; i += 100 {
					if err := sink.Write(pgCtx(t), recs[i:i+100]); err != nil {
						t.Fatalf("%s: %v", name, err)
					}
				}
			}
		}
		a, b := pgSum(t, c, "sinktypes.inserted", "id"), pgSum(t, c, "sinktypes.copied", "id")
		if a != b || a[:4] != "429:" { // every seventh a delete of a key never written
			t.Fatalf("inserted %s, copied %s", a, b)
		}
		got := pgOne(t, c, `SELECT name, count::text, score, price::text, even, encode(blob, 'hex'), at, day::text, doc::text, tags::text, none
			FROM sinktypes.copied WHERE id = 260`)
		want := []any{"record-260", "780", 65.0, "260.60", true, "0401ff", time.Unix(1_700_000_260, 260_000_000).UTC(), "2026-09-18", `{"n": 260}`, "{a,0}", nil}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("row 260:\n%v\n%v", got, want)
		}
		// A value the column refuses fails its record, the rows before it
		// written.
		sink := pgSinkLoad(t, pgSinkParams(s, map[string]any{"table": "sinktypes.inserted"}))
		badRec := fitsync.GenerateRecord("records", 1000, 0, 1)
		badRec.Op, badRec.After = fitsync.OpInsert, fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(1000)}, fitsync.Field{Name: "even", Value: fitsync.String("maybe")})
		err := sink.Write(pgCtx(t), []*fitsync.Record{fitsync.GenerateRecord("records", 1001, 0, 1), badRec})
		var be *fitsync.BatchError
		if !errors.As(err, &be) || be.Index != 1 || !fitsync.IsPermanent(err) {
			t.Fatal(err)
		}
		if got := pgOne(t, c, "SELECT count(*) FROM sinktypes.inserted WHERE id >= 1000"); fmt.Sprint(got) != "[1]" {
			t.Error(got)
		}
		// A table missing is tried again, not failed for good.
		missing := pgSinkLoad(t, pgSinkParams(s, map[string]any{"table": "sinktypes.nope"}))
		if err := missing.Write(pgCtx(t), recs[:1]); err == nil || fitsync.IsPermanent(err) {
			t.Error(err)
		}
	})
}

// TestPostgresSinkAcrossKills runs a mixed load of upserts and deletes of
// few keys, in transactions, through pipelines killed at every crash point
// and started again from their checkpoints, writing a table with each
// transactions mode: each ends equal by checksum to the load applied in
// order.
func TestPostgresSinkAcrossKills(t *testing.T) {
	eachPG(t, func(t *testing.T, s pgServer) {
		c := s.connect(t, nil)
		const total = 2000
		pgExec(t, c, "DROP SCHEMA IF EXISTS sinkkills CASCADE")
		pgExec(t, c, "CREATE SCHEMA sinkkills")
		const cols = "(a bigint, b text, v bigint, at timestamptz, PRIMARY KEY (a, b))"
		pgExec(t, c, "CREATE TABLE sinkkills.expected "+cols)
		state := map[int64]*fitsync.Record{}
		for n := range int64(total) {
			r := pgSinkMixedRecord(n, total)
			a, _ := r.Key.Get("a")
			if r.Op == fitsync.OpDelete {
				delete(state, a.AsInt())
			} else {
				state[a.AsInt()] = r
			}
		}
		for k, r := range state {
			b, _ := r.After.Get("b")
			v, _ := r.After.Get("v")
			at, _ := r.After.Get("at")
			pgExec(t, c, "INSERT INTO sinkkills.expected VALUES ($1, $2, $3, $4)", k, b.Text(), v.AsInt(), at.AsTime())
		}
		want := pgSum(t, c, "sinkkills.expected", "a, b")
		for _, mode := range []string{"batch", "source"} {
			table := "sinkkills." + mode
			pgExec(t, c, "CREATE TABLE "+table+" "+cols)
			p := map[string]any{"name": "p", "checkpoint_interval": "1ms", "queue": map[string]any{"max_records": 60},
				"source": map[string]any{"module": "pgsink_mixed", "params": map[string]any{"total": total}},
				"sinks": []any{map[string]any{"name": "pg", "batch": map[string]any{"max_records": 25, "max_wait": "2ms"},
					"sink": map[string]any{"module": "postgres", "params": pgSinkParams(s, map[string]any{"table": table,
						"transactions": mode, "copy_min_rows": 8})}}}}
			b, _ := json.Marshal(map[string]any{"sync": map[string]any{"state_dir": t.TempDir(), "pipelines": []any{p}}})
			cfg, err := config.Load(b)
			if err != nil {
				t.Fatal(err)
			}
			points := []fitsync.Point{fitsync.CrashEmitted, fitsync.CrashDispatched, fitsync.CrashWritten,
				fitsync.CrashAcknowledged, fitsync.CrashCheckpointWritten, fitsync.CrashCheckpointSaved}
			for round := range 12 {
				var seen atomic.Int32
				at, nth := points[round%len(points)], int32(1+round*9)
				rt := fitsync.NewRuntime(fitsync.Options{Crash: func(_ string, p fitsync.Point) bool { return p == at && seen.Add(1) == nth }})
				if err := rt.Apply(cfg.Sync); err != nil {
					t.Fatal(err)
				}
				time.Sleep(100 * time.Millisecond)
				rt.Close(context.Background())
			}
			rt := fitsync.NewRuntime(fitsync.Options{})
			if err := rt.Apply(cfg.Sync); err != nil {
				t.Fatal(err)
			}
			var got string
			deadline := time.Now().Add(3 * time.Minute) // a hang guard
			for got = pgSum(t, c, table, "a, b"); got != want && time.Now().Before(deadline); got = pgSum(t, c, table, "a, b") {
				time.Sleep(50 * time.Millisecond)
			}
			rt.Close(context.Background())
			if got != want {
				t.Errorf("%s: the table's checksum %s, want %s", mode, got, want)
			}
		}
	})
}
