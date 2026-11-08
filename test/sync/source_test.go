package synctest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
)

// sourceDocs is how many documents the snapshot test reads:
// FIT_SYNC_SOURCE_DOCS, 100,000 when unset (CI); the plan's million is
// FIT_SYNC_SOURCE_DOCS=1000000 test/sync/run.sh.
func sourceDocs(t *testing.T) int {
	if s := os.Getenv("FIT_SYNC_SOURCE_DOCS"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			t.Fatalf("FIT_SYNC_SOURCE_DOCS=%q", s)
		}
		return n
	}
	return 100000
}

// bulk writes documents to index with _bulk, n at a time, sending again
// what the cluster is too busy for.
func (c *cluster) bulk(t *testing.T, index string, ids []string, docs []string) {
	t.Helper()
	const n = 5000
	for i := 0; i < len(ids); i += n {
		pending := make([]int, 0, n)
		for j := i; j < min(i+n, len(ids)); j++ {
			pending = append(pending, j)
		}
		for len(pending) > 0 {
			var b strings.Builder
			for _, j := range pending {
				fmt.Fprintf(&b, `{"index":{"_index":%q,"_id":%q}}`+"\n%s\n", index, ids[j], docs[j])
			}
			var r struct {
				Errors bool
				Items  []map[string]struct {
					Status int
					Error  json.RawMessage
				}
			}
			req, _ := http.NewRequest(http.MethodPost, c.url+"/_bulk", strings.NewReader(b.String()))
			req.SetBasicAuth(c.user, c.password)
			req.Header.Set("Content-Type", "application/x-ndjson")
			resp, err := c.http.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			err = json.NewDecoder(resp.Body).Decode(&r)
			resp.Body.Close()
			if resp.StatusCode == http.StatusTooManyRequests {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			if err != nil || resp.StatusCode != http.StatusOK {
				t.Fatalf("_bulk: %d %v", resp.StatusCode, err)
			}
			if len(r.Items) != len(pending) {
				t.Fatalf("_bulk answered %d items for %d", len(r.Items), len(pending))
			}
			var busy []int
			for k, it := range r.Items {
				for _, res := range it {
					if res.Status == http.StatusTooManyRequests {
						busy = append(busy, pending[k])
					} else if res.Status > 299 {
						t.Fatalf("_bulk item: %d %s", res.Status, res.Error)
					}
				}
			}
			if pending = busy; len(busy) > 0 {
				time.Sleep(50 * time.Millisecond) // only the items refused for being busy, again
			}
		}
	}
	c.call(t, http.MethodPost, "/"+index+"/_refresh", "", nil)
}

// source loads the source elasticsearch with params added to the
// cluster's URL, CA and credentials.
func (c *cluster) source(t *testing.T, params map[string]any) fitsync.Source {
	t.Helper()
	t.Setenv("FIT_TEST_SYNC_PASSWORD", c.password)
	p := map[string]any{"urls": []string{c.url}, "ca_file": c.ca, "username": c.user, "password_env": "FIT_TEST_SYNC_PASSWORD"}
	for k, v := range params {
		p[k] = v
	}
	b, _ := json.Marshal(map[string]any{"sync": map[string]any{"state_dir": "x", "pipelines": []any{map[string]any{"name": "p",
		"source": map[string]any{"module": "elasticsearch", "params": p}, "sinks": []any{map[string]any{"name": "s", "sink": map[string]any{"module": "memory"}}}}}}})
	cfg, err := config.Load(b)
	if err != nil {
		t.Fatal(err)
	}
	s, err := module.Load(fitsync.Sources, cfg.Sync.Pipelines[0].Source, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { module.Cleanup(s) })
	return s.(fitsync.Source)
}

// openPITs counts the points in time and scrolls open.
func (c *cluster) openPITs(t *testing.T) int {
	var r struct {
		Nodes map[string]struct {
			Indices struct {
				Search struct {
					PointInTimeCurrent int `json:"point_in_time_current"`
					ScrollCurrent      int `json:"scroll_current"`
				} `json:"search"`
			} `json:"indices"`
		} `json:"nodes"`
	}
	c.call(t, http.MethodGet, "/_nodes/stats/indices/search", "", &r)
	n := 0
	for _, node := range r.Nodes {
		n += node.Indices.Search.PointInTimeCurrent + node.Indices.Search.ScrollCurrent
	}
	return n
}

func idOf(r *fitsync.Record) string { return r.Key.Fields()[0].Value.Text() }

// TestElasticsearchSourceSnapshot bulk loads sourceDocs documents and reads
// them through a point in time: each once, OpenSearch's in the _id order
// it pages by, and the point in time closed at the end.
func TestElasticsearchSourceSnapshot(t *testing.T) {
	n := sourceDocs(t)
	for _, c := range clusters(t) {
		t.Run(c.name, func(t *testing.T) {
			index := "fit-sync-src-snapshot"
			c.call(t, http.MethodDelete, "/"+index+"?ignore_unavailable=true", "", nil)
			c.call(t, http.MethodPut, "/"+index, `{"settings": {"number_of_shards": 2, "number_of_replicas": 0, "refresh_interval": "-1"}}`, nil)
			ids, docs := make([]string, n), make([]string, n)
			for i := range n {
				ids[i], docs[i] = fmt.Sprintf("doc-%07d", i), fmt.Sprintf(`{"seq":%d,"price":%d.5,"name":"n%d"}`, i, i, i)
			}
			start := time.Now()
			c.bulk(t, index, ids, docs)
			t.Logf("%d documents loaded in %v", n, time.Since(start).Round(time.Millisecond))
			s := c.source(t, map[string]any{"index": index, "page_size": 5000})
			seen := make([]bool, n)
			count, last := 0, ""
			start = time.Now()
			err := s.Run(context.Background(), &fitsync.Run{Once: true, Emit: func(r *fitsync.Record) error {
				seq, _ := r.After.Get("seq")
				i := int(seq.AsInt())
				if seq.Kind() != fitsync.KindInt || i < 0 || i >= n || seen[i] {
					return fmt.Errorf("document %s: seq %v, seen before %v", idOf(r), seq, i >= 0 && i < n && seen[i])
				}
				if price, _ := r.After.Get("price"); price.Kind() != fitsync.KindFloat {
					return fmt.Errorf("price is a %v", price.Kind())
				}
				if c.name == "opensearch" && idOf(r) <= last {
					return fmt.Errorf("%s after %s: not in the _id order", idOf(r), last)
				}
				seen[i], last = true, idOf(r)
				count++
				return nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			if count != n {
				t.Fatalf("%d documents read, want %d", count, n)
			}
			t.Logf("%d documents read in %v", n, time.Since(start).Round(time.Millisecond))
			if open := c.openPITs(t); open != 0 {
				t.Errorf("%d points in time and scrolls left open", open)
			}
		})
	}
}

// TestElasticsearchSourcePolls reads a snapshot during which documents are
// changed, many to one cursor value, then polls: the changes are read once
// each; a restart from the last checkpoint reads only what came after it.
func TestElasticsearchSourcePolls(t *testing.T) {
	for _, c := range clusters(t) {
		t.Run(c.name, func(t *testing.T) {
			index := "fit-sync-src-poll"
			c.call(t, http.MethodDelete, "/"+index+"?ignore_unavailable=true", "", nil)
			c.call(t, http.MethodPut, "/"+index, `{"settings": {"number_of_shards": 2, "number_of_replicas": 0},
				"mappings": {"properties": {"u": {"type": "date"}, "v": {"type": "keyword"}}}}`, nil)
			base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			doc := func(at time.Time, v string) string {
				return fmt.Sprintf(`{"u":%q,"v":%q}`, at.Format(time.RFC3339Nano), v)
			}
			var ids, docs []string
			for i := range 2000 {
				ids, docs = append(ids, fmt.Sprintf("d%04d", i)), append(docs, doc(base.Add(time.Duration(i/10)*time.Second), "v1"))
			}
			c.bulk(t, index, ids, docs)

			// 120 documents changed, and 5 written, at one time, while
			// the snapshot is read.
			t1 := base.Add(time.Hour)
			var changed []string
			var changes []string
			for i := 0; i < 2000; i += 16 {
				changed, changes = append(changed, fmt.Sprintf("d%04d", i)), append(changes, doc(t1, "v2"))
			}
			for i := range 5 {
				changed, changes = append(changed, fmt.Sprintf("new%d", i)), append(changes, doc(t1, "v2"))
			}
			s := c.source(t, map[string]any{"index": index, "cursor": "u", "page_size": 50, "poll_interval": "100ms"})
			var mu sync.Mutex
			var recs []*fitsync.Record
			var last fitsync.Position
			mid, written := make(chan struct{}), make(chan struct{})
			want := map[string]bool{}
			for _, id := range changed {
				want[id] = true
			}
			done := make(chan struct{})
			emit := func(r *fitsync.Record) error {
				mu.Lock()
				recs = append(recs, r)
				if r.Position != nil {
					last = r.Position
				}
				got := 0
				for _, x := range recs[min(len(recs), 2000):] {
					if want[idOf(x)] && x.After.Fields()[1].Value.Text() == "v2" {
						got++
					}
				}
				n := len(recs)
				mu.Unlock()
				if n == 100 {
					close(mid)
					<-written
				}
				if got >= len(want) {
					select {
					case <-done:
					default:
						close(done)
					}
				}
				return nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			runErr := make(chan error, 1)
			go func() { runErr <- s.Run(ctx, &fitsync.Run{Emit: emit}) }()
			select {
			case <-mid:
			case err := <-runErr:
				t.Fatalf("the source ended before the snapshot's 100th record: %v", err)
			}
			c.bulk(t, index, changed, changes)
			close(written)
			select {
			case <-done:
				cancel()
				if err := <-runErr; !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case err := <-runErr:
				t.Fatalf("the source ended before polling read every change: %v", err)
			}
			mu.Lock()
			snapshot, polled := recs[:2000], recs[2000:]
			checkpoint := last
			mu.Unlock()
			seen := map[string]int{}
			for _, r := range snapshot {
				seen[idOf(r)]++
				if r.Op != fitsync.OpInsert {
					t.Fatalf("a snapshot record is an %v", r.Op)
				}
			}
			if len(seen) != 2000 {
				t.Fatalf("the snapshot read %d documents", len(seen))
			}
			polledOnce := map[string]int{}
			for _, r := range polled {
				id := idOf(r)
				if want[id] {
					polledOnce[id]++
				}
				wantOp := fitsync.OpUpdate
				if strings.HasPrefix(id, "new") {
					wantOp = fitsync.OpInsert
				}
				if want[id] && r.Op != wantOp {
					t.Errorf("%s polled as %v", id, r.Op)
				}
				if want[id] && !r.CommitTime.Equal(t1) {
					t.Errorf("%s's commit time %v", id, r.CommitTime)
				}
			}
			for id, k := range polledOnce {
				if k != 1 {
					t.Errorf("%s polled %d times", id, k)
				}
			}
			t.Logf("the snapshot read 2000 documents, polling %d, %d of them changed during the snapshot; checkpoint %.80s", len(polled), len(polledOnce), checkpoint)

			// Written after the checkpoint: three more at t1, one later.
			c.bulk(t, index, []string{"late0", "late1", "late2", "later"},
				[]string{doc(t1, "v3"), doc(t1, "v3"), doc(t1, "v3"), doc(t1.Add(time.Second), "v3")})
			var again []string
			err := s.Run(context.Background(), &fitsync.Run{From: checkpoint, Once: true, Emit: func(r *fitsync.Record) error {
				again = append(again, idOf(r))
				return nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(sortedIDs(again), ",") != "late0,late1,late2,later" {
				t.Errorf("after the restart: %v", again)
			}
		})
	}
}

func sortedIDs(ids []string) []string {
	out := append([]string(nil), ids...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// TestElasticsearchSourceJob copies an index to another with a job whose
// snapshot is read in slices, four at a time: every document is copied,
// with its _id.
func TestElasticsearchSourceJob(t *testing.T) {
	for _, c := range clusters(t) {
		t.Run(c.name, func(t *testing.T) {
			from, to := "fit-sync-src-job", "fit-sync-src-job-copy"
			for _, index := range []string{from, to} {
				c.call(t, http.MethodDelete, "/"+index+"?ignore_unavailable=true", "", nil)
			}
			const n = 20000
			ids, docs := make([]string, n), make([]string, n)
			for i := range n {
				ids[i], docs[i] = fmt.Sprintf("j%05d", i), fmt.Sprintf(`{"seq":%d,"tags":["a","t%d"],"o":{"x":1.5,"n":null},"at":"2026-11-08T00:00:00Z"}`, i, i%7)
			}
			c.bulk(t, from, ids, docs)
			t.Setenv("FIT_TEST_SYNC_PASSWORD", c.password)
			conn := map[string]any{"urls": []string{c.url}, "ca_file": c.ca, "username": c.user, "password_env": "FIT_TEST_SYNC_PASSWORD"}
			src, sink := map[string]any{"index": from, "chunk_documents": 3000}, map[string]any{"index": to}
			for k, v := range conn {
				src[k], sink[k] = v, v
			}
			b, _ := json.Marshal(map[string]any{"sync": map[string]any{"state_dir": t.TempDir(), "pipelines": []any{map[string]any{
				"name": "copy", "mode": "once", "on_error": "stop", "job": map[string]any{"max_parallel": 4},
				"source": map[string]any{"module": "elasticsearch", "params": src},
				"sinks": []any{map[string]any{"name": "es", "batch": map[string]any{"max_records": 500, "max_wait": "10ms"},
					"sink": map[string]any{"module": "elasticsearch", "params": sink}}}}}}})
			cfg, err := config.Load(b)
			if err != nil {
				t.Fatal(err)
			}
			st, err := fitsync.RunPipeline(context.Background(), cfg.Sync, "copy", fitsync.Options{SourceAttempts: 3})
			if err != nil || st.State != "finished" || st.Job == nil || st.Job.Failed != 0 || st.Job.ChunksDone != 7 {
				t.Fatalf("%v: %+v", err, st)
			}
			if got := c.count(t, to); got != n {
				t.Errorf("%d documents copied, want %d", got, n)
			}
			var r struct {
				Source json.RawMessage `json:"_source"`
			}
			c.call(t, http.MethodGet, "/"+to+"/_doc/j01234", "", &r)
			if string(r.Source) != `{"seq":1234,"tags":["a","t2"],"o":{"x":1.5,"n":null},"at":"2026-11-08T00:00:00Z"}` {
				t.Errorf("j01234 copied as %s", r.Source)
			}
			if open := c.openPITs(t); open != 0 {
				t.Errorf("%d points in time and scrolls left open", open)
			}
		})
	}
}
