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

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	_ "github.com/lesismal/fit/sync/doris"
	mysqlwire "github.com/lesismal/fit/sync/mysql/wire"
)

// olapServer is a Doris or StarRocks the olap family's tests use: its
// Stream Load URL (a frontend's, or a backend's) and its MySQL port, from
// FIT_SYNC_DORIS_URL and FIT_SYNC_DORIS_ADDR, FIT_SYNC_STARROCKS_URL and
// FIT_SYNC_STARROCKS_ADDR (test/sync/olap.sh), user root without a
// password.
type olapServer struct {
	flavor, url, addr string
}

func olapServers(t *testing.T) []olapServer {
	t.Helper()
	var out []olapServer
	for _, f := range []string{"doris", "starrocks"} {
		up := strings.ToUpper(f)
		u, a := os.Getenv("FIT_SYNC_"+up+"_URL"), os.Getenv("FIT_SYNC_"+up+"_ADDR")
		if u != "" && a != "" {
			out = append(out, olapServer{flavor: f, url: u, addr: a})
		}
	}
	if len(out) == 0 {
		if os.Getenv("FIT_SYNC_INTEROP") == "required" {
			t.Fatal("neither FIT_SYNC_DORIS_URL nor FIT_SYNC_STARROCKS_URL is set")
		}
		t.Skip("no Doris or StarRocks: FIT_SYNC_DORIS_URL and FIT_SYNC_STARROCKS_URL are not set (test/sync/olap.sh sets them)")
	}
	return out
}

func eachOLAP(t *testing.T, f func(t *testing.T, s olapServer)) {
	for _, s := range olapServers(t) {
		t.Run(s.flavor, func(t *testing.T) { f(t, s) })
	}
}

func (s olapServer) conn(t *testing.T) *mysqlwire.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	c, err := mysqlwire.Dial(ctx, mysqlwire.Config{Addr: s.addr, User: "root", Location: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if _, err := c.Exec(ctx, "CREATE DATABASE IF NOT EXISTS fit_sync_test"); err != nil {
		t.Fatal(err)
	}
	return c
}

// table makes fit_sync_test.name afresh, of the unique (Doris) or primary
// (StarRocks) key model on id, or the duplicate model with dup.
func (s olapServer) table(t *testing.T, c *mysqlwire.Conn, name, cols string, dup bool) string {
	t.Helper()
	full := "fit_sync_test." + name
	model := "UNIQUE KEY(id)"
	if s.flavor == "starrocks" {
		model = "PRIMARY KEY(id)"
	}
	props := `"replication_num" = "1"`
	if dup {
		model = "DUPLICATE KEY(id)"
	} else if s.flavor == "doris" {
		props += `, "enable_unique_key_merge_on_write" = "true"`
	}
	mustExec(t, c, "DROP TABLE IF EXISTS "+full)
	mustExec(t, c, fmt.Sprintf("CREATE TABLE %s (%s) %s DISTRIBUTED BY HASH(id) BUCKETS 2 PROPERTIES (%s)", full, cols, model, props))
	return full
}

// rows returns the rows of query, each as its values joined by |, sorted.
func olapRows(t *testing.T, c *mysqlwire.Conn, query string) []string {
	t.Helper()
	rows, err := c.Query(hangGuard(t), query)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	var out []string
	for rows.Next() {
		var parts []string
		for _, v := range rows.Values() {
			switch x := v.(type) {
			case []byte:
				parts = append(parts, string(x))
			case nil:
				parts = append(parts, "NULL")
			default:
				parts = append(parts, fmt.Sprint(x))
			}
		}
		out = append(out, strings.Join(parts, "|"))
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func (s olapServer) sink(t *testing.T, extra map[string]any) fitsync.Sink {
	t.Helper()
	// Labels outlive a table dropped and made again: each sink's are its
	// own.
	p := map[string]any{"urls": []string{s.url}, "username": "root", "flavor": s.flavor, "label_prefix": fmt.Sprintf("t%d", time.Now().UnixNano())}
	for k, v := range extra {
		p[k] = v
	}
	return loadSinkModule(t, "doris", p)
}

// TestDorisSinkDedup loads a batch twice into a duplicate model table, as a
// pipeline does when it crashes after the load and before the checkpoint:
// the server keeps it once. A batch cut differently is a new load.
func TestDorisSinkDedup(t *testing.T) {
	eachOLAP(t, func(t *testing.T, s olapServer) {
		c := s.conn(t)
		table := s.table(t, c, "dedup", "id BIGINT, v VARCHAR(20)", true)
		recs, _ := changes(table)
		recs = recs[:100]
		sink := s.sink(t, nil)
		if err := sink.(fitsync.Tester).Test(hangGuard(t)); err != nil {
			t.Fatal(err)
		}
		writeAll(t, sink, recs, 100)
		writeAll(t, sink, recs, 100)
		if got := olapRows(t, c, "SELECT COUNT(*) FROM "+table); got[0] != "100" {
			t.Fatalf("%v rows, want 100", got)
		}
		writeAll(t, sink, recs[:50], 50)
		if got := olapRows(t, c, "SELECT COUNT(*) FROM "+table); got[0] != "150" {
			t.Fatalf("%v rows after a batch cut differently, want 150", got)
		}
	})
}

// TestDorisSinkConverges loads inserts, updates and deletes, then replays
// them from the middle and from the start in batches of other sizes, as
// JSON and as CSV: the table holds each id's last state.
func TestDorisSinkConverges(t *testing.T) {
	eachOLAP(t, func(t *testing.T, s olapServer) {
		c := s.conn(t)
		for _, format := range []string{"json", "csv"} {
			t.Run(format, func(t *testing.T) {
				table := s.table(t, c, "converge_"+format, "id BIGINT NOT NULL, v VARCHAR(20)", false)
				recs, want := changes(table)
				sink := s.sink(t, map[string]any{"format": format})
				writeAll(t, sink, recs, 37)
				writeAll(t, sink, recs[len(recs)/2:], 23)
				writeAll(t, sink, recs, 101)
				writeAll(t, sink, recs[len(recs)/3:], 37)
				got := olapRows(t, c, "SELECT id, v FROM "+table)
				var w []string
				for _, row := range want { // [id,"v"]
					id, v, _ := strings.Cut(strings.Trim(row, "[]"), ",")
					w = append(w, id+"|"+strings.Trim(v, `"`))
				}
				sort.Strings(w)
				if strings.Join(got, "\n") != strings.Join(w, "\n") {
					t.Fatalf("%d rows, want %d:\n%s", len(got), len(w), strings.Join(got, "\n"))
				}
			})
		}
	})
}

// TestDorisSinkPartial updates only the columns a record has.
func TestDorisSinkPartial(t *testing.T) {
	eachOLAP(t, func(t *testing.T, s olapServer) {
		c := s.conn(t)
		table := s.table(t, c, "partial", "id BIGINT NOT NULL, v VARCHAR(20), w VARCHAR(20)", false)
		full := s.sink(t, nil)
		var recs []*fitsync.Record
		for i := range int64(10) {
			recs = append(recs, &fitsync.Record{Namespace: table, Op: fitsync.OpInsert, Position: fitsync.Position(fmt.Sprintf(`{"n":%d}`, i+1)),
				After: fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(i)}, fitsync.Field{Name: "v", Value: fitsync.String("v")}, fitsync.Field{Name: "w", Value: fitsync.String("w")})})
		}
		writeAll(t, full, recs, 10)
		partial := s.sink(t, map[string]any{"partial_columns": true, "label_prefix": fmt.Sprintf("p%d", time.Now().UnixNano())})
		var ups []*fitsync.Record
		for i := range int64(5) {
			ups = append(ups, &fitsync.Record{Namespace: table, Op: fitsync.OpUpdate, Position: fitsync.Position(fmt.Sprintf(`{"n":%d}`, i+1)),
				After: fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(i * 2)}, fitsync.Field{Name: "w", Value: fitsync.String("W")})})
		}
		writeAll(t, partial, ups, 5)
		got := olapRows(t, c, "SELECT id, v, w FROM "+table+" WHERE w = 'W'")
		if len(got) != 5 || got[0] != "0|v|W" {
			t.Fatalf("%v", got)
		}
	})
}

// TestDorisSinkBadRow loads a batch with a row the table refuses: only that
// record fails, for good, the rows before it loaded.
func TestDorisSinkBadRow(t *testing.T) {
	eachOLAP(t, func(t *testing.T, s olapServer) {
		c := s.conn(t)
		table := s.table(t, c, "bad", "id BIGINT NOT NULL, v INT NOT NULL", true)
		var recs []*fitsync.Record
		for i := range 10 {
			v := fitsync.Int(int64(i))
			if i == 6 {
				v = fitsync.Null()
			}
			recs = append(recs, &fitsync.Record{Namespace: table, Op: fitsync.OpInsert, Position: fitsync.Position(fmt.Sprintf(`{"n":%d}`, i+1)),
				After: fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(int64(i))}, fitsync.Field{Name: "v", Value: v})})
		}
		err := s.sink(t, nil).Write(context.Background(), recs)
		var be *fitsync.BatchError
		if !errors.As(err, &be) || be.Index != 6 || !fitsync.IsPermanent(err) {
			t.Fatalf("%v", err)
		}
		if got := olapRows(t, c, "SELECT COUNT(*) FROM "+table); got[0] != "6" {
			t.Fatalf("%v rows, want 6", got)
		}
	})
}

// TestDorisSource reads a table by a cursor with many rows on each cursor
// value, in small pages, stops half way, resumes from the last record's
// position without a gap or a repeat, then polls up rows written since.
func TestDorisSource(t *testing.T) {
	eachOLAP(t, func(t *testing.T, s olapServer) {
		c := s.conn(t)
		table := s.table(t, c, "source", "id BIGINT NOT NULL, v VARCHAR(20), updated_at DATETIME, deleted BOOLEAN", false)
		var vals []string
		for i := range 100 {
			vals = append(vals, fmt.Sprintf("(%d, 'v%d', '2026-01-01 00:00:%02d', false)", i, i, i/10))
		}
		mustExec(t, c, "INSERT INTO "+table+" VALUES "+strings.Join(vals, ","))
		params := map[string]any{"address": s.addr, "username": "root", "table": table, "key": []string{"id"}, "cursor": "updated_at",
			"deleted_column": "deleted", "page_rows": 7, "poll_interval": "100ms"}
		cfg := loadSync(t, t.TempDir(), map[string]any{"name": "p", "source": map[string]any{"module": "doris", "params": params},
			"sinks": []any{map[string]any{"name": "s", "sink": map[string]any{"module": "memory"}}}})
		src := loadSourceModule(t, cfg)
		first := collect(t, src, nil, false, 45)
		rest := collect(t, src, first[len(first)-1].Position, true, 0)
		all := append(first, rest...)
		if len(all) != 100 {
			t.Fatalf("%d records, want 100", len(all))
		}
		seen := map[int64]bool{}
		for _, r := range all {
			id, _ := r.Key.Get("id")
			if seen[id.AsInt()] {
				t.Fatalf("id %d twice", id.AsInt())
			}
			seen[id.AsInt()] = true
		}
		mustExec(t, c, "INSERT INTO "+table+" VALUES (5, 'gone', '2026-01-02 00:00:00', true), (500, 'new', '2026-01-02 00:00:00', false)")
		more := collect(t, src, all[len(all)-1].Position, false, 2)
		if got := ids(more); strings.Join(got, ",") != "delete:5,insert:500" {
			t.Fatalf("%v", got)
		}
	})
}

// loadSourceModule loads the source of cfg's pipeline.
func loadSourceModule(t *testing.T, cfg *config.Config) fitsync.Source {
	t.Helper()
	s, err := module.Load(fitsync.Sources, cfg.Sync.Pipelines[0].Source, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { module.Cleanup(s) })
	return s.(fitsync.Source)
}

// TestDorisAcrossKills copies generate's records, of every value type and
// with updates and deletes, into a table through a pipeline killed at
// random points and started again from its checkpoint: the table ends
// with every row as the records leave it.
func TestDorisAcrossKills(t *testing.T) {
	eachOLAP(t, func(t *testing.T, s olapServer) {
		seed := uint64(time.Now().UnixNano())
		t.Logf("seed %d", seed)
		rnd := rand.New(rand.NewPCG(seed, 13))
		c := s.conn(t)
		table := s.table(t, c, "kills", "id BIGINT NOT NULL, name VARCHAR(40), `count` BIGINT, score DOUBLE, price DECIMAL(20, 2), even BOOLEAN, "+
			"`blob` VARCHAR(40), `at` DATETIME, `day` DATE, doc JSON, tags JSON, `none` VARCHAR(10)", false)
		const total = 3000
		cfg := loadSync(t, t.TempDir(), map[string]any{"name": "p", "mode": "once", "on_error": "stop", "checkpoint_interval": "1ms",
			"source": map[string]any{"module": "generate", "params": map[string]any{"records": total, "tx_size": 1 + rnd.IntN(20), "namespace": table}},
			"sinks": []any{map[string]any{"name": "olap", "batch": map[string]any{"max_records": 1 + rnd.IntN(300), "max_wait": "1ms"},
				"sink": map[string]any{"module": "doris", "params": map[string]any{"urls": []string{s.url}, "username": "root", "flavor": s.flavor,
					"format": []string{"json", "csv"}[rnd.IntN(2)], "label_prefix": fmt.Sprintf("k%d", seed)}}}}})
		kills := runKilled(t, cfg, rnd, 8)
		want := 0
		for n := range total {
			if n%7 != 6 {
				want++
			}
		}
		if got := olapRows(t, c, "SELECT COUNT(*) FROM "+table); got[0] != fmt.Sprint(want) {
			t.Fatalf("%v rows, want %d", got, want)
		}
		got := olapRows(t, c, "SELECT id, name, `count`, score, price, even, `blob`, `at`, `day`, `none` FROM "+table+" WHERE id = 12")
		if len(got) != 1 || !strings.HasPrefix(got[0], "12|record-12|36|3|12.12|1|DAD/|2023-11-14 22:13:32") || !strings.HasSuffix(got[0], "|NULL") || !strings.Contains(got[0], "|2026-01-13") {
			t.Fatalf("%v", got)
		}
		t.Logf("%d records across %d kills", total, kills)
	})
}
