package synctest

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
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

func init() {
	module.Register(module.Info{ID: fitsync.Sources + ".mysqlload", New: func() any { return new(mysqlLoad) },
		Doc: "A test's mixed write load: transactions of inserts, updates, key changes and deletes."})
}

// mysqlLoad is a source of a mixed write load in transactions, the same
// for a seed every time it runs, resumed after the checkpoint's record.
type mysqlLoad struct {
	Txs       int    `json:"txs"`
	Seed      uint64 `json:"seed"`
	Namespace string `json:"namespace"`
}

func (l *mysqlLoad) Run(ctx context.Context, r *fitsync.Run) error {
	recs, _ := mysqlLoadRecords(l.Namespace, l.Txs, l.Seed)
	start := 0
	if r.From != nil {
		var p struct{ N int }
		if err := json.Unmarshal(r.From, &p); err != nil {
			return err
		}
		start = p.N + 1
	}
	for _, rec := range recs[start:] {
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

// loadRow is a row of the load's table.
type loadRow struct {
	v  string
	n  string
	at time.Time
	j  string
	b  []byte
}

func (w loadRow) value(id int64) fitsync.Value {
	b := fitsync.Null()
	if w.b != nil {
		b = fitsync.Bytes(w.b)
	}
	return fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(id)}, fitsync.Field{Name: "v", Value: fitsync.String(w.v)},
		fitsync.Field{Name: "n", Value: fitsync.Decimal(w.n)}, fitsync.Field{Name: "at", Value: fitsync.Time(w.at)},
		fitsync.Field{Name: "j", Value: fitsync.JSON([]byte(w.j))}, fitsync.Field{Name: "b", Value: b})
}

// mysqlLoadRecords is the load's records, each with its position, and the
// rows it leaves.
func mysqlLoadRecords(ns string, txs int, seed uint64) ([]*fitsync.Record, map[int64]loadRow) {
	rnd := rand.New(rand.NewPCG(seed, 11))
	rows := map[int64]loadRow{}
	var recs []*fitsync.Record
	key := func(id int64) fitsync.Value { return fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(id)}) }
	base := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	for t := range txs {
		var tx []*fitsync.Record
		for k := range 1 + rnd.IntN(6) {
			id := int64(rnd.IntN(300))
			w := loadRow{v: fmt.Sprintf("t%d-%d", t, k), n: fmt.Sprintf("%d.%02d", rnd.IntN(10000), rnd.IntN(100)),
				at: base.Add(time.Duration(rnd.Int64N(1e12)) * time.Microsecond), j: fmt.Sprintf(`{"t": %d, "k": [%d]}`, t, k)}
			if rnd.IntN(3) > 0 {
				w.b = []byte{byte(t), byte(k), 0, 0xff}
			}
			old, exists := rows[id]
			r := &fitsync.Record{Namespace: ns, Key: key(id), CommitTime: base.Add(time.Duration(t) * time.Second)}
			switch c := rnd.IntN(10); {
			case !exists:
				r.Op, r.After = fitsync.OpInsert, w.value(id)
				rows[id] = w
			case c < 2:
				r.Op, r.Before = fitsync.OpDelete, old.value(id)
				delete(rows, id)
			case c < 3:
				// The key changes to one not taken.
				to := id
				for _, taken := rows[to]; taken; _, taken = rows[to] {
					to = 300 + int64(rnd.IntN(1000))
				}
				r.Op, r.Key, r.Before, r.After = fitsync.OpUpdate, key(to), old.value(id), w.value(to)
				delete(rows, id)
				rows[to] = w
			default:
				r.Op, r.Before, r.After = fitsync.OpUpdate, old.value(id), w.value(id)
				rows[id] = w
			}
			tx = append(tx, r)
		}
		for i, r := range tx {
			if len(tx) > 1 {
				r.Tx = fitsync.Tx{ID: fmt.Sprint("tx", t), First: i == 0, Last: i == len(tx)-1}
			}
			r.Position = fitsync.Position(fmt.Sprintf(`{"n":%d}`, len(recs)))
			recs = append(recs, r)
		}
	}
	return recs, rows
}

// mysqlTarget is a server a sink writes to: one of FIT_SYNC_MYSQL, or
// TiDB from FIT_SYNC_TIDB (host:port, root without a password, no TLS).
type mysqlTarget struct {
	name, addr, password string
	tidb                 bool
	srv                  mysqlServer
}

func mysqlTargets(t *testing.T) []mysqlTarget {
	var out []mysqlTarget
	for _, s := range mysqlServers(t) {
		out = append(out, mysqlTarget{name: s.name, addr: s.addr, password: s.password, srv: s})
	}
	if a := os.Getenv("FIT_SYNC_TIDB"); a != "" {
		out = append(out, mysqlTarget{name: "tidb", addr: a, tidb: true})
	} else if os.Getenv("FIT_SYNC_INTEROP") == "required" {
		t.Fatal("FIT_SYNC_TIDB is not set")
	}
	return out
}

func (m mysqlTarget) conn(t *testing.T) *mysqlwire.Conn {
	t.Helper()
	if m.tidb {
		return m.srv.dial(t, mysqlwire.Config{Addr: m.addr, User: "root"})
	}
	return m.srv.root(t)
}

// params are the sink's params for m.
func (m mysqlTarget) params(t *testing.T, extra map[string]any) map[string]any {
	p := map[string]any{"address": m.addr, "username": "root"}
	if !m.tidb {
		t.Setenv("FIT_TEST_SYNC_MYSQL_PASSWORD", m.password)
		p["password_env"] = "FIT_TEST_SYNC_MYSQL_PASSWORD"
		p["tls"] = true
		p["ca_file"] = os.Getenv("FIT_SYNC_MYSQL_CA")
		p["tls_rsa"] = m.name == "mysql57"
	}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

const loadColumns = "(id BIGINT NOT NULL %s, v VARCHAR(64) NOT NULL, n DECIMAL(10,2), at DATETIME(6), j JSON, b VARBINARY(16), PRIMARY KEY (id))"

// loadChecksum is a table's rows reckoned by the server: their count and a
// sum of each one's CRC32.
func loadChecksum(t *testing.T, c *mysqlwire.Conn, table string) string {
	t.Helper()
	rows, err := c.Query(hangGuard(t), "SELECT COUNT(*), COALESCE(SUM(CRC32(CONCAT_WS('|', id, v, COALESCE(n, '-'), COALESCE(at, '-'), "+
		"COALESCE(CAST(j AS CHAR), '-'), COALESCE(HEX(b), '-')))), 0) FROM "+table)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for rows.Next() {
		for _, v := range rows.Values() {
			out = append(out, fmt.Sprintf("%v", v))
		}
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(out, ",")
}

// TestMySQLSinkAcrossKills runs a mixed write load in transactions into
// each server, the pipeline killed at random points and started again from
// its checkpoint, then run whole again from no checkpoint, a replay of
// everything: the table ends equal, by checksum, to one the rows the load
// leaves were written to directly. With transactions source and batch;
// the key is AUTO_INCREMENT, AUTO_RANDOM on TiDB, and the load writes a
// key of 0 too.
func TestMySQLSinkAcrossKills(t *testing.T) {
	for _, m := range mysqlTargets(t) {
		t.Run(m.name, func(t *testing.T) {
			for _, mode := range []string{"source", "batch"} {
				t.Run(mode, func(t *testing.T) { mysqlSinkAcrossKills(t, m, mode) })
			}
		})
	}
}

func mysqlSinkAcrossKills(t *testing.T, m mysqlTarget, mode string) {
	seed := uint64(time.Now().UnixNano())
	t.Logf("seed %d", seed)
	rnd := rand.New(rand.NewPCG(seed, 3))
	c := m.conn(t)
	db := "fit_sink_" + mode
	mysqlDB(t, c, db)
	auto := "AUTO_INCREMENT"
	if m.tidb {
		auto = "AUTO_RANDOM"
	}
	mustExec(t, c, "CREATE TABLE dst "+fmt.Sprintf(loadColumns, auto))
	mustExec(t, c, "CREATE TABLE want "+fmt.Sprintf(loadColumns, ""))
	const txs = 1500
	_, rows := mysqlLoadRecords(db+".dst", txs, seed)
	var ids []int64
	for id := range rows {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var args [][]any
	for _, id := range ids {
		w := rows[id]
		var b any
		if w.b != nil {
			b = w.b
		}
		args = append(args, []any{id, w.v, w.n, w.at, json.RawMessage(w.j), b})
	}
	if _, err := c.Exec(hangGuard(t), "SET time_zone = '+00:00'"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.InsertRows(hangGuard(t), "INSERT INTO want (id, v, n, at, j, b) VALUES", args, ""); err != nil {
		t.Fatal(err)
	}

	stateDir := t.TempDir()
	pipeline := map[string]any{"name": "p", "mode": "once", "on_error": "stop", "checkpoint_interval": "1ms",
		"queue":  map[string]any{"max_records": 20 + rnd.IntN(300)},
		"source": map[string]any{"module": "mysqlload", "params": map[string]any{"txs": txs, "seed": seed, "namespace": db + ".dst"}},
		"sinks": []any{map[string]any{"name": "my", "batch": map[string]any{"max_records": 1 + rnd.IntN(60), "max_wait": "1ms"},
			"sink": map[string]any{"module": "mysql", "params": m.params(t, map[string]any{"transactions": mode})}}}}
	b, _ := json.Marshal(map[string]any{"sync": map[string]any{"state_dir": stateDir, "pipelines": []any{pipeline}}})
	cfg, err := config.Load(b)
	if err != nil {
		t.Fatal(err)
	}
	points := []fitsync.Point{fitsync.CrashEmitted, fitsync.CrashDispatched, fitsync.CrashWritten,
		fitsync.CrashAcknowledged, fitsync.CrashCheckpointWritten, fitsync.CrashCheckpointSaved}
	kills := 0
	for range 8 {
		at, nth := points[rnd.IntN(len(points))], int32(1+rnd.IntN(60))
		var seen atomic.Int32
		rt := fitsync.NewRuntime(fitsync.Options{Crash: func(_ string, p fitsync.Point) bool {
			return p == at && seen.Add(1) == nth
		}})
		if err := rt.Apply(cfg.Sync); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(3 * time.Minute) // a hang guard
		for st := redisState(rt); st != "killed" && st != "finished"; st = redisState(rt) {
			if st == "stopped" || time.Now().After(deadline) {
				t.Fatalf("the pipeline is %s: %+v", st, rt.Status())
			}
			time.Sleep(time.Millisecond)
		}
		if redisState(rt) == "killed" {
			kills++
		}
		rt.Close(context.Background())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute) // a hang guard
	defer cancel()
	for _, again := range []bool{false, true} {
		if again {
			// A replay of everything.
			if err := os.Remove(filepath.Join(stateDir, "p.json")); err != nil {
				t.Fatal(err)
			}
		}
		st, err := fitsync.RunPipeline(ctx, cfg.Sync, "p", fitsync.Options{})
		if err != nil || st.State != "finished" {
			t.Fatalf("%v: %+v", err, st)
		}
		got, want := loadChecksum(t, c, "dst"), loadChecksum(t, c, "want")
		if got != want {
			t.Fatalf("the table's checksum is %s, want %s (replay %v)", got, want, again)
		}
	}
	t.Logf("%d rows of %d transactions, equal across %d kills and a replay", len(rows), txs, kills)
}
