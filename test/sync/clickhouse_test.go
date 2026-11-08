package synctest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	_ "github.com/lesismal/fit/sync/clickhouse"
)

// clickhouse is the server the ClickHouse tests use, from FIT_SYNC_CH_URL,
// FIT_SYNC_CH_USER (default when unset) and FIT_SYNC_CH_PASSWORD.
type clickhouse struct {
	url, user, password, db string
}

func clickhouseServer(t *testing.T) *clickhouse {
	t.Helper()
	u := os.Getenv("FIT_SYNC_CH_URL")
	if u == "" {
		if os.Getenv("FIT_SYNC_INTEROP") == "required" {
			t.Fatal("FIT_SYNC_CH_URL is not set")
		}
		t.Skip("no ClickHouse: FIT_SYNC_CH_URL is not set (test/sync/run.sh sets it)")
	}
	c := &clickhouse{url: u, user: os.Getenv("FIT_SYNC_CH_USER"), password: os.Getenv("FIT_SYNC_CH_PASSWORD"), db: "fit_sync_test"}
	if c.user == "" {
		c.user = "default"
	}
	c.exec(t, "CREATE DATABASE IF NOT EXISTS "+c.db)
	return c
}

// exec runs sql and returns the answer, failing the test on an error.
func (c *clickhouse) exec(t *testing.T, sql string) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, c.url+"/?"+url.Values{"date_time_output_format": {"iso"}}.Encode(), strings.NewReader(sql))
	req.SetBasicAuth(c.user, c.password)
	resp, err := (&http.Client{Timeout: time.Minute}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: %d %s", sql, resp.StatusCode, b)
	}
	return string(b)
}

// table makes db.name afresh with the columns and engine given.
func (c *clickhouse) table(t *testing.T, name, def string) string {
	t.Helper()
	full := c.db + "." + name
	c.exec(t, "DROP TABLE IF EXISTS "+full+" SYNC")
	c.exec(t, "CREATE TABLE "+full+" "+def)
	return full
}

// rows returns the rows of sql, one line each, sorted.
func (c *clickhouse) rows(t *testing.T, sql string) []string {
	t.Helper()
	out := strings.Split(strings.ReplaceAll(strings.TrimSpace(c.exec(t, sql+" FORMAT JSONCompactEachRow")), ", ", ","), "\n")
	if len(out) == 1 && out[0] == "" {
		return nil
	}
	sort.Strings(out)
	return out
}

func (c *clickhouse) params(extra map[string]any) map[string]any {
	p := map[string]any{"urls": []string{c.url}, "username": c.user}
	if c.password != "" {
		p["password_env"] = "FIT_TEST_SYNC_CH_PASSWORD"
	}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

// pipeline loads a config of one pipeline, as the tests' modules come from.
func (c *clickhouse) pipeline(t *testing.T, p map[string]any) *config.Sync {
	t.Helper()
	t.Setenv("FIT_TEST_SYNC_CH_PASSWORD", c.password)
	p["name"] = "p"
	b, _ := json.Marshal(map[string]any{"sync": map[string]any{"state_dir": t.TempDir(), "pipelines": []any{p}}})
	cfg, err := config.Load(b)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Sync
}

func (c *clickhouse) sink(t *testing.T, params map[string]any) fitsync.Sink {
	t.Helper()
	cfg := c.pipeline(t, map[string]any{"source": map[string]any{"module": "generate"},
		"sinks": []any{map[string]any{"name": "s", "sink": map[string]any{"module": "clickhouse", "params": c.params(params)}}}})
	s, err := module.Load(fitsync.Sinks, cfg.Pipelines[0].Sinks[0].Sink, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { module.Cleanup(s) })
	return s.(fitsync.Sink)
}

func (c *clickhouse) source(t *testing.T, params map[string]any) fitsync.Source {
	t.Helper()
	cfg := c.pipeline(t, map[string]any{"source": map[string]any{"module": "clickhouse", "params": c.params(params)},
		"sinks": []any{map[string]any{"name": "s", "sink": map[string]any{"module": "memory"}}}})
	s, err := module.Load(fitsync.Sources, cfg.Pipelines[0].Source, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { module.Cleanup(s) })
	return s.(fitsync.Source)
}

// chRec is the n-th change of a stream: an op on id with value v, committed
// at n seconds, its position n.
func chRec(table string, n int, op fitsync.Op, id int64, v string) *fitsync.Record {
	r := &fitsync.Record{Namespace: table, Op: op, Key: fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(id)}),
		CommitTime: time.Unix(1_700_000_000+int64(n), 0), Position: fitsync.Position(fmt.Sprintf(`{"n":%d}`, n))}
	row := fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(id)}, fitsync.Field{Name: "v", Value: fitsync.String(v)})
	if op == fitsync.OpDelete {
		r.Before = row
	} else {
		r.After = row
	}
	return r
}

// changes returns a stream of inserts, updates and deletes of 200 ids, and
// the rows it leaves, as JSONCompactEachRow lines of id and v.
func changes(table string) ([]*fitsync.Record, []string) {
	var recs []*fitsync.Record
	want := map[int64]string{}
	add := func(op fitsync.Op, id int64, v string) {
		recs = append(recs, chRec(table, len(recs)+1, op, id, v))
		if op == fitsync.OpDelete {
			delete(want, id)
		} else {
			want[id] = v
		}
	}
	for i := range int64(200) {
		add(fitsync.OpInsert, i, "v1")
	}
	for i := range int64(200) {
		switch {
		case i%10 == 0:
			add(fitsync.OpDelete, i, "v1")
			add(fitsync.OpInsert, i, "v3")
		case i%5 == 0:
			add(fitsync.OpDelete, i, "v1")
		case i%2 == 0:
			add(fitsync.OpUpdate, i, "v2")
		}
	}
	var rows []string
	for id, v := range want {
		rows = append(rows, fmt.Sprintf(`[%d,"%s"]`, id, v))
	}
	sort.Strings(rows)
	return recs, rows
}

func writeAll(t *testing.T, s fitsync.Sink, recs []*fitsync.Record, size int) {
	t.Helper()
	for i := 0; i < len(recs); i += size {
		if err := s.Write(context.Background(), recs[i:min(i+size, len(recs))]); err != nil {
			t.Fatal(err)
		}
	}
}

const replacing = "(id Int64, v String, _version UInt64, _deleted UInt8) ENGINE = ReplacingMergeTree(_version, _deleted) ORDER BY id SETTINGS non_replicated_deduplication_window = 1000"

// TestClickHouseDedup writes a batch twice, as a pipeline does when it
// crashes after the write and before the checkpoint: the server stores it
// once. A batch cut differently is a new insert.
func TestClickHouseDedup(t *testing.T) {
	c := clickhouseServer(t)
	table := c.table(t, "dedup", replacing)
	recs, _ := changes(table)
	recs = recs[:100]
	s := c.sink(t, nil)
	writeAll(t, s, recs, 100)
	writeAll(t, s, recs, 100)
	if got := c.rows(t, "SELECT count() FROM "+table); len(got) != 1 || got[0] != "[100]" {
		t.Fatalf("%v rows, want 100", got)
	}
	writeAll(t, s, recs[:50], 50)
	if got := c.rows(t, "SELECT count() FROM "+table); got[0] != "[150]" {
		t.Fatalf("%v rows after a batch cut differently, want 150", got)
	}
}

// TestClickHouseConverges writes inserts, updates and deletes, then replays
// them from the middle and from the start in batches of other sizes: read
// with FINAL, the table holds each id's last state, for flagged deletes and
// for lightweight ones.
func TestClickHouseConverges(t *testing.T) {
	c := clickhouseServer(t)
	for _, mode := range []string{"flag", "delete"} {
		t.Run(mode, func(t *testing.T) {
			table := c.table(t, "converge_"+mode, replacing)
			recs, want := changes(table)
			s := c.sink(t, map[string]any{"deletes": mode})
			writeAll(t, s, recs, 37)
			writeAll(t, s, recs[len(recs)/2:], 23)
			writeAll(t, s, recs, 101)
			writeAll(t, s, recs[len(recs)/3:], 37)
			got := c.rows(t, "SELECT id, v FROM "+table+" FINAL")
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Fatalf("%d rows, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
			}
		})
	}
}

// TestClickHouseBadRow writes a batch with one row the table cannot take:
// only that record fails, for good, the rows before it written.
func TestClickHouseBadRow(t *testing.T) {
	c := clickhouseServer(t)
	table := c.table(t, "bad", "(id Int64, v UInt32, _version UInt64, _deleted UInt8) ENGINE = ReplacingMergeTree(_version, _deleted) ORDER BY id SETTINGS non_replicated_deduplication_window = 1000")
	var recs []*fitsync.Record
	for i := range 10 {
		v := fmt.Sprint(i)
		if i == 4 {
			v = "not a number"
		}
		recs = append(recs, chRec(table, i+1, fitsync.OpInsert, int64(i), v))
	}
	err := c.sink(t, nil).Write(context.Background(), recs)
	var be *fitsync.BatchError
	if !errors.As(err, &be) || be.Index != 4 || !fitsync.IsPermanent(err) {
		t.Fatalf("%v", err)
	}
	if got := c.rows(t, "SELECT count() FROM "+table); got[0] != "[4]" {
		t.Errorf("%v rows, want 4", got)
	}
}

// collect runs src until it has emitted n records, or ends, and returns
// them.
func collect(t *testing.T, src fitsync.Source, from fitsync.Position, once bool, n int) []*fitsync.Record {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute) // a hang guard
	defer cancel()
	var got []*fitsync.Record
	stop := errors.New("enough")
	err := src.Run(ctx, &fitsync.Run{From: from, Once: once, Emit: func(r *fitsync.Record) error {
		got = append(got, r)
		if len(got) == n {
			return stop
		}
		return nil
	}})
	if err != nil && !errors.Is(err, stop) {
		t.Fatalf("after %d records: %v", len(got), err)
	}
	return got
}

func ids(recs []*fitsync.Record) []string {
	var out []string
	for _, r := range recs {
		id, _ := r.Key.Get("id")
		out = append(out, fmt.Sprintf("%s:%d", r.Op, id.AsInt()))
	}
	return out
}

// TestClickHouseSourceResumes reads a table by a cursor with many rows on
// each cursor value, in small pages, stops half way, resumes from the last
// record's position without a gap or a repeat, then polls up rows written
// since, deletes among them.
func TestClickHouseSourceResumes(t *testing.T) {
	c := clickhouseServer(t)
	table := c.table(t, "src", "(id Int64, at DateTime64(3, 'UTC'), v String, gone Bool) ENGINE = MergeTree ORDER BY id")
	c.exec(t, "INSERT INTO "+table+" SELECT number, toDateTime64('2026-01-01 00:00:00', 3, 'UTC') + intDiv(number, 7), toString(number), false FROM numbers(1000)")
	src := c.source(t, map[string]any{"table": table, "key": []string{"id"}, "cursor": "at", "deleted_column": "gone", "page_rows": 33, "poll_interval": "50ms"})
	first := collect(t, src, nil, false, 500)
	rest := collect(t, src, first[len(first)-1].Position, true, 0)
	all := append(first, rest...)
	if len(all) != 1000 {
		t.Fatalf("%d records, want 1000", len(all))
	}
	for i, r := range all {
		if id, _ := r.Key.Get("id"); id.AsInt() != int64(i) || r.Op != fitsync.OpInsert {
			t.Fatalf("record %d is %s of %v", i, r.Op, id)
		}
	}
	if at := all[999].CommitTime; !at.Equal(time.Date(2026, 1, 1, 0, 2, 22, 0, time.UTC)) {
		t.Errorf("the commit time %v, not the cursor's", at)
	}
	v, _ := all[3].After.Get("v")
	if v.Text() != "3" {
		t.Errorf("v %v", v)
	}
	// Rows written later, one of them on the last cursor value read, and
	// one a delete.
	c.exec(t, "INSERT INTO "+table+" VALUES (1000, toDateTime64('2026-01-01 00:02:22', 3, 'UTC'), 'late', false), (1001, now64(3), 'new', false), (5, now64(3), 'gone', true)")
	more := collect(t, src, all[999].Position, false, 3)
	if got := strings.Join(ids(more), ","); got != "insert:1000,delete:5,insert:1001" && got != "insert:1000,insert:1001,delete:5" {
		t.Errorf("polled %s", got)
	}
}

// TestClickHouseSnapshotNever starts past the rows there are.
func TestClickHouseSnapshotNever(t *testing.T) {
	c := clickhouseServer(t)
	table := c.table(t, "never", "(id Int64, at UInt64) ENGINE = MergeTree ORDER BY id")
	c.exec(t, "INSERT INTO "+table+" SELECT number, number FROM numbers(10)")
	src := c.source(t, map[string]any{"table": table, "key": []string{"id"}, "cursor": "at", "snapshot": "never", "poll_interval": "50ms"})
	done := make(chan []*fitsync.Record)
	go func() { done <- collect(t, src, nil, false, 1) }()
	// Rows are written until the source reads one, as it may read the
	// greatest cursor after the first of them: none of the first ten.
	for id := 10; ; id++ {
		c.exec(t, fmt.Sprintf("INSERT INTO %s VALUES (%d, %d)", table, id, id))
		select {
		case got := <-done:
			if id, _ := got[0].Key.Get("id"); len(got) != 1 || id.AsInt() < 10 {
				t.Errorf("read %v", ids(got))
			}
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// TestClickHouseJob copies a table through a job of chunks, a query its
// source, into a ReplacingMergeTree with deletes flagged, then a second
// job after more rows: the copy holds every row once.
func TestClickHouseJob(t *testing.T) {
	c := clickhouseServer(t)
	from := c.table(t, "job_from", "(id Int64, k String, v String) ENGINE = MergeTree ORDER BY (k, id)")
	to := c.table(t, "job_to", "(id Int64, k String, v String, _version UInt64, _deleted UInt8) ENGINE = ReplacingMergeTree(_version, _deleted) ORDER BY (k, id) SETTINGS non_replicated_deduplication_window = 1000")
	c.exec(t, "INSERT INTO "+from+" SELECT number, toString(number % 13), concat('v', toString(number)) FROM numbers(25000)")
	cfg := c.pipeline(t, map[string]any{"mode": "once", "on_error": "stop", "job": map[string]any{"max_parallel": 4},
		"source": map[string]any{"module": "clickhouse", "params": c.params(map[string]any{
			"query": "SELECT * FROM " + from + " WHERE id % 1000 != 999", "key": []string{"k", "id"}, "chunk_rows": 2000, "page_rows": 500, "namespace": "job"})},
		"sinks": []any{map[string]any{"name": "ch", "batch": map[string]any{"max_records": 300, "max_wait": "10ms"},
			"sink": map[string]any{"module": "clickhouse", "params": c.params(map[string]any{"table": to})}}}})
	st, err := fitsync.RunPipeline(context.Background(), cfg, "p", fitsync.Options{})
	if err != nil || st.State != "finished" || st.Job == nil || st.Job.Failed != 0 || len(st.Job.Chunks) != 13 {
		t.Fatalf("%v: %+v", err, st)
	}
	if got := c.rows(t, "SELECT count(), uniqExact(id), sum(id) FROM "+to+" FINAL"); got[0] != "[24975,24975,312162525]" {
		t.Errorf("copied %v", got)
	}
}
