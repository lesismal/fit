package synctest

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	_ "github.com/lesismal/fit/sync/postgres/sink"
)

func init() {
	module.Register(module.Info{ID: fitsync.Sources + ".seqload", New: func() any { return new(seqLoad) },
		Doc: "A test's load of inserts of distinct keys, in transactions of one to four, each row's seq its record's number."})
}

// seqLoad makes Total inserts into Namespace, record n of key n and seq n,
// in transactions of one to four records, each record with a position (a
// transaction's last's is the one resumed after); resumed after the
// checkpoint's record. In mode once it ends at its end; otherwise it waits.
type seqLoad struct {
	Total     int64  `json:"total"`
	Namespace string `json:"namespace"`
}

func seqLoadRecords(ns string, total int64) []*fitsync.Record {
	var recs []*fitsync.Record
	for n := int64(0); n < total; {
		size := min(1+n%4, total-n)
		id := fmt.Sprint("tx", n)
		for i := range size {
			k := n + i
			key := fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(k)})
			r := &fitsync.Record{Namespace: ns, Op: fitsync.OpInsert, Key: key,
				After:    fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(k)}, fitsync.Field{Name: "seq", Value: fitsync.Int(k)}),
				Position: fitsync.Position(fmt.Sprintf(`{"n":%d}`, k+1))}
			if size > 1 {
				r.Tx = fitsync.Tx{ID: id, First: i == 0, Last: i == size-1}
			}
			recs = append(recs, r)
		}
		n += size
	}
	return recs
}

func (l *seqLoad) Run(ctx context.Context, r *fitsync.Run) error {
	var start int64
	if r.From != nil {
		var p struct{ N int64 }
		if err := json.Unmarshal(r.From, &p); err != nil {
			return err
		}
		start = p.N
	}
	for _, rec := range seqLoadRecords(l.Namespace, l.Total)[start:] {
		if err := r.Emit(rec); err != nil {
			return err
		}
	}
	if r.Once {
		return nil
	}
	<-ctx.Done()
	return ctx.Err()
}

// exactlyOnce runs a seqload of total records through a pipeline of the
// sink sink, in mode, killed at random points (after the sink's commit
// and before the file's checkpoint among them) and started again each
// time, then once to its end. count returns how many rows the sink's table
// has, and how many writes its trigger counted, all and distinct. With
// checkpoint sink, every record is written once: as many writes as rows,
// none twice; with checkpoint file, none is lost, and the writes made
// twice are reported.
func exactlyOnce(t *testing.T, mode, checkpoint string, total int64, sink map[string]any, count func() (rows, writes, distinct int64)) {
	seed := uint64(time.Now().UnixNano())
	rnd := rand.New(rand.NewPCG(seed, 5))
	t.Logf("seed %d", seed)
	pipeline := map[string]any{"name": "p", "mode": mode, "on_error": "stop", "checkpoint_interval": "1ms",
		"queue":  map[string]any{"max_records": 10 + rnd.IntN(60)},
		"source": map[string]any{"module": "seqload", "params": map[string]any{"total": total, "namespace": "x"}},
		"sinks": []any{map[string]any{"name": "db", "batch": map[string]any{"max_records": 1 + rnd.IntN(20), "max_wait": "1ms"},
			"sink": sink}}}
	b, _ := json.Marshal(map[string]any{"sync": map[string]any{"state_dir": t.TempDir(), "pipelines": []any{pipeline}}})
	cfg, err := config.Load(b)
	if err != nil {
		t.Fatal(err)
	}
	points := []fitsync.Point{fitsync.CrashWritten, fitsync.CrashAcknowledged, fitsync.CrashCheckpointWritten,
		fitsync.CrashEmitted, fitsync.CrashDispatched, fitsync.CrashCheckpointSaved}
	kills := 0
	for round := 0; round < 12; round++ {
		at, nth := points[round%len(points)], int32(1+rnd.IntN(80))
		var seen atomic.Int32
		rt := fitsync.NewRuntime(fitsync.Options{Crash: func(_ string, p fitsync.Point) bool { return p == at && seen.Add(1) == nth }})
		if err := rt.Apply(cfg.Sync); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(3 * time.Minute) // a hang guard
		for {
			st := redisState(rt)
			if st == "killed" {
				kills++
				break
			}
			if rows, _, _ := count(); rows == total && (mode != "once" || st == "finished") {
				break
			}
			if st == "stopped" || time.Now().After(deadline) {
				t.Fatalf("the pipeline is %s: %+v", st, rt.Status())
			}
			time.Sleep(5 * time.Millisecond)
		}
		rt.Close(context.Background())
	}
	// To the end.
	rt := fitsync.NewRuntime(fitsync.Options{})
	if err := rt.Apply(cfg.Sync); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Minute) // a hang guard
	for rows, _, _ := count(); rows < total; rows, _, _ = count() {
		if time.Now().After(deadline) {
			t.Fatalf("%d rows of %d: %+v", rows, total, rt.Status())
		}
		time.Sleep(10 * time.Millisecond)
	}
	rt.Close(context.Background())
	rows, writes, distinct := count()
	if rows != total || distinct != total {
		t.Fatalf("%d rows and %d records written, of %d", rows, distinct, total)
	}
	if checkpoint == "sink" && writes != total {
		t.Fatalf("checkpoint sink: %d writes of %d records, %d twice, across %d kills", writes, total, writes-total, kills)
	}
	t.Logf("checkpoint %s: %d records, %d written twice, across %d kills", checkpoint, total, writes-total, kills)
}

// TestMySQLExactlyOnce: a pipeline into each MySQL and MariaDB server with
// checkpoint sink, killed at random points, writes every record once, by a
// trigger that counts each row written; with checkpoint file it writes
// some twice. TiDB has no triggers to count with.
func TestMySQLExactlyOnce(t *testing.T) {
	eachMySQL(t, func(t *testing.T, s mysqlServer) {
		m := mysqlTarget{name: s.name, addr: s.addr, password: s.password, srv: s}
		c := m.conn(t)
		for _, ck := range []string{"sink", "file"} {
			t.Run(ck, func(t *testing.T) {
				db := "fit_xo_" + ck
				mysqlDB(t, c, db)
				mustExec(t, c, "CREATE TABLE dst (id BIGINT NOT NULL PRIMARY KEY, seq BIGINT NOT NULL)")
				mustExec(t, c, "CREATE TABLE writes (seq BIGINT NOT NULL)")
				mustExec(t, c, "CREATE TRIGGER dst_writes BEFORE INSERT ON dst FOR EACH ROW INSERT INTO writes VALUES (NEW.seq)")
				count := func() (int64, int64, int64) {
					rows, err := c.Query(hangGuard(t), "SELECT (SELECT COUNT(*) FROM dst), COUNT(*), COUNT(DISTINCT seq) FROM writes")
					if err != nil {
						t.Fatal(err)
					}
					var out []int64
					for rows.Next() {
						for _, v := range rows.Values() {
							var n int64
							fmt.Sscan(fmt.Sprint(v), &n)
							out = append(out, n)
						}
					}
					if err := rows.Close(); err != nil || len(out) != 3 {
						t.Fatalf("%v %v", out, err)
					}
					return out[0], out[1], out[2]
				}
				for _, tx := range []string{"source", "batch"} {
					t.Run(tx, func(t *testing.T) {
						mustExec(t, c, "DELETE FROM dst")
						mustExec(t, c, "DELETE FROM writes")
						mustExec(t, c, "DROP TABLE IF EXISTS _fit_sync_checkpoints")
						exactlyOnce(t, "once", ck, 3000, map[string]any{"module": "mysql",
							"params": m.params(t, map[string]any{"table": db + ".dst", "database": db, "transactions": tx, "checkpoint": ck})}, count)
					})
				}
			})
		}
	})
}

// TestPostgresExactlyOnce: the same into each PostgreSQL server, as a
// stream, runs of upserts copied (COPY) as well as inserted.
func TestPostgresExactlyOnce(t *testing.T) {
	eachPG(t, func(t *testing.T, s pgServer) {
		c := s.connect(t, nil)
		for _, ck := range []string{"sink", "file"} {
			t.Run(ck, func(t *testing.T) {
				schema := "fit_xo_" + ck
				pgExec(t, c, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
				pgExec(t, c, "CREATE SCHEMA "+schema)
				pgExec(t, c, "CREATE TABLE "+schema+".dst (id bigint PRIMARY KEY, seq bigint NOT NULL)")
				pgExec(t, c, "CREATE TABLE "+schema+".writes (seq bigint NOT NULL)")
				pgExec(t, c, "CREATE FUNCTION "+schema+".count_write() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN INSERT INTO "+schema+
					".writes VALUES (NEW.seq); RETURN NEW; END$$")
				pgExec(t, c, "CREATE TRIGGER count_write BEFORE INSERT ON "+schema+".dst FOR EACH ROW EXECUTE FUNCTION "+schema+".count_write()")
				count := func() (int64, int64, int64) {
					row := pgOne(t, c, "SELECT (SELECT count(*) FROM "+schema+".dst)::text || ',' || count(*)::text || ',' || count(DISTINCT seq)::text FROM "+schema+".writes")
					var n [3]int64
					fmt.Sscan(strings.ReplaceAll(fmt.Sprint(row[0]), ",", " "), &n[0], &n[1], &n[2])
					return n[0], n[1], n[2]
				}
				exactlyOnce(t, "stream", ck, 3000, map[string]any{"module": "postgres",
					"params": pgSinkParams(s, map[string]any{"table": schema + ".dst", "checkpoint": ck, "checkpoint_table": schema + "._fit_sync_checkpoints",
						"transactions": "source", "copy_min_rows": 3})}, count)
			})
		}
	})
}
