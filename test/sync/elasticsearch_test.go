// Package synctest holds sync's interop tests against real servers, which
// run.sh starts in Docker. Without the servers' environment variables they
// skip, so that go test ./... needs no Docker; FIT_SYNC_INTEROP=required
// makes them fail instead, as CI sets it.
package synctest

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	_ "github.com/lesismal/fit/sync/elasticsearch"
)

// cluster is a server the tests write to.
type cluster struct {
	name, url, ca, user, password string
	http                          *http.Client
}

func clusters(t *testing.T) []*cluster {
	var cs []*cluster
	for _, c := range []struct{ name, env, user string }{{"elasticsearch", "ES", "elastic"}, {"opensearch", "OS", "admin"}} {
		u := os.Getenv("FIT_SYNC_" + c.env + "_URL")
		if u == "" {
			if os.Getenv("FIT_SYNC_INTEROP") == "required" {
				t.Fatalf("FIT_SYNC_%s_URL is not set", c.env)
			}
			continue
		}
		cl := &cluster{name: c.name, url: u, ca: os.Getenv("FIT_SYNC_" + c.env + "_CA"), user: c.user, password: os.Getenv("FIT_SYNC_" + c.env + "_PASSWORD")}
		pool := x509.NewCertPool()
		pem, err := os.ReadFile(cl.ca)
		if err != nil || !pool.AppendCertsFromPEM(pem) {
			t.Fatalf("the CA %s: %v", cl.ca, err)
		}
		cl.http = &http.Client{Timeout: time.Minute, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
		cs = append(cs, cl)
	}
	if len(cs) == 0 {
		t.Skip("no cluster: FIT_SYNC_ES_URL and FIT_SYNC_OS_URL are not set (test/sync/run.sh sets them)")
	}
	return cs
}

// call sends a request as the cluster's admin and decodes the answer into
// out, failing the test on a status past 299.
func (c *cluster) call(t *testing.T, method, path, body string, out any) {
	t.Helper()
	req, _ := http.NewRequest(method, c.url+path, strings.NewReader(body))
	req.SetBasicAuth(c.user, c.password)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode > 299 {
		t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
	}
}

// docs returns the index's documents by _id.
func (c *cluster) docs(t *testing.T, index string) map[string]string {
	t.Helper()
	c.call(t, http.MethodPost, "/"+index+"/_refresh", "", nil)
	var r struct {
		Hits struct {
			Hits []struct {
				ID     string          `json:"_id"`
				Source json.RawMessage `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	c.call(t, http.MethodPost, "/"+index+"/_search", `{"size": 10000, "query": {"match_all": {}}}`, &r)
	out := map[string]string{}
	for _, h := range r.Hits.Hits {
		var b bytes.Buffer
		_ = json.Compact(&b, h.Source)
		out[h.ID] = b.String()
	}
	return out
}

func (c *cluster) count(t *testing.T, index string) int {
	t.Helper()
	c.call(t, http.MethodPost, "/"+index+"/_refresh", "", nil)
	var r struct{ Count int }
	c.call(t, http.MethodGet, "/"+index+"/_count", "", &r)
	return r.Count
}

// rejected returns how many writes the cluster's write thread pools have
// refused (429).
func (c *cluster) rejected(t *testing.T) int {
	var r struct {
		Nodes map[string]struct {
			ThreadPool map[string]struct{ Rejected int } `json:"thread_pool"`
		}
	}
	c.call(t, http.MethodGet, "/_nodes/stats/thread_pool", "", &r)
	n := 0
	for _, node := range r.Nodes {
		n += node.ThreadPool["write"].Rejected
	}
	return n
}

// sink loads the sink elasticsearch with params added to the cluster's
// URL, CA and credentials.
func (c *cluster) sink(t *testing.T, params map[string]any) fitsync.Sink {
	t.Helper()
	t.Setenv("FIT_TEST_SYNC_PASSWORD", c.password)
	p := map[string]any{"urls": []string{c.url}, "ca_file": c.ca, "username": c.user, "password_env": "FIT_TEST_SYNC_PASSWORD"}
	for k, v := range params {
		if v == nil {
			delete(p, k)
		} else {
			p[k] = v
		}
	}
	b, _ := json.Marshal(map[string]any{"sync": map[string]any{"state_dir": "x", "pipelines": []any{map[string]any{"name": "p",
		"source": map[string]any{"module": "generate"}, "sinks": []any{map[string]any{"name": "s", "sink": map[string]any{"module": "elasticsearch", "params": p}}}}}}})
	cfg, err := config.Load(b)
	if err != nil {
		t.Fatal(err)
	}
	s, err := module.Load(fitsync.Sinks, cfg.Sync.Pipelines[0].Sinks[0].Sink, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { module.Cleanup(s) })
	return s.(fitsync.Sink)
}

func rec(ns string, op fitsync.Op, id int64, v string) *fitsync.Record {
	r := &fitsync.Record{Namespace: ns, Op: op, Key: fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(id)})}
	row := fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(id)}, fitsync.Field{Name: "v", Value: fitsync.String(v)})
	if op == fitsync.OpDelete {
		r.Before = row
	} else {
		r.After = row
	}
	return r
}

func write(t *testing.T, s fitsync.Sink, recs []*fitsync.Record, size int) {
	t.Helper()
	for i := 0; i < len(recs); i += size {
		if err := s.Write(context.Background(), recs[i:min(i+size, len(recs))]); err != nil {
			t.Fatal(err)
		}
	}
}

// TestElasticsearchConverges writes inserts, updates and deletes of the
// same documents, then replays them from the middle and from the start, as
// a pipeline does after a crash: the index ends as the changes say.
func TestElasticsearchConverges(t *testing.T) {
	for _, c := range clusters(t) {
		t.Run(c.name, func(t *testing.T) {
			index := "fit-sync-converge"
			c.call(t, http.MethodDelete, "/"+index+"?ignore_unavailable=true", "", nil)
			var recs []*fitsync.Record
			want := map[string]string{}
			for i := range int64(200) {
				recs = append(recs, rec(index, fitsync.OpInsert, i, "v1"))
				want[fmt.Sprint(i)] = fmt.Sprintf(`{"id":%d,"v":"v1"}`, i)
			}
			for i := range int64(200) {
				switch {
				case i%10 == 0:
					recs = append(recs, rec(index, fitsync.OpDelete, i, "v1"), rec(index, fitsync.OpInsert, i, "v3"))
					want[fmt.Sprint(i)] = fmt.Sprintf(`{"id":%d,"v":"v3"}`, i)
				case i%5 == 0:
					recs = append(recs, rec(index, fitsync.OpDelete, i, "v1"))
					delete(want, fmt.Sprint(i))
				case i%2 == 0:
					recs = append(recs, rec(index, fitsync.OpUpdate, i, "v2"))
					want[fmt.Sprint(i)] = fmt.Sprintf(`{"id":%d,"v":"v2"}`, i)
				}
			}
			s := c.sink(t, nil)
			write(t, s, recs, 37)
			write(t, s, recs[len(recs)/2:], 23)
			write(t, s, recs, 101)
			got := c.docs(t, index)
			if len(got) != len(want) {
				t.Fatalf("%d documents, want %d", len(got), len(want))
			}
			for id, doc := range want {
				if got[id] != doc {
					t.Errorf("document %s: %s, want %s", id, got[id], doc)
				}
			}
		})
	}
}

// TestElasticsearchBadDocument writes a bulk with one document its index's
// mapping refuses: only that one fails, permanently, at its record.
func TestElasticsearchBadDocument(t *testing.T) {
	for _, c := range clusters(t) {
		t.Run(c.name, func(t *testing.T) {
			index := "fit-sync-bad"
			c.call(t, http.MethodDelete, "/"+index+"?ignore_unavailable=true", "", nil)
			c.call(t, http.MethodPut, "/"+index, `{"mappings": {"properties": {"id": {"type": "integer"}, "v": {"type": "integer"}}}}`, nil)
			var recs []*fitsync.Record
			for i := range int64(10) {
				v := fmt.Sprint(i)
				if i == 4 {
					v = "not a number"
				}
				recs = append(recs, rec(index, fitsync.OpInsert, i, v))
			}
			err := c.sink(t, nil).Write(context.Background(), recs)
			var be *fitsync.BatchError
			if !errors.As(err, &be) || be.Index != 4 || !fitsync.IsPermanent(err) || !strings.Contains(err.Error(), "parsing") {
				t.Fatalf("%v", err)
			}
			if n := c.count(t, index); n != 9 {
				t.Errorf("%d documents, want 9", n)
			}
		})
	}
}

// TestElasticsearchBackpressure runs a job of eight writers against a
// cluster with one write thread and a queue of one, which refuses bulks
// with 429: the pipeline slows down and writes every record.
func TestElasticsearchBackpressure(t *testing.T) {
	for _, c := range clusters(t) {
		t.Run(c.name, func(t *testing.T) {
			index := "fit-sync-backpressure"
			c.call(t, http.MethodDelete, "/"+index+"?ignore_unavailable=true", "", nil)
			c.call(t, http.MethodPut, "/"+index, `{"mappings": {"dynamic": false}}`, nil)
			before := c.rejected(t)
			t.Setenv("FIT_TEST_SYNC_PASSWORD", c.password)
			const records = 20000
			b, _ := json.Marshal(map[string]any{"sync": map[string]any{"state_dir": t.TempDir(), "pipelines": []any{map[string]any{
				"name": "bp", "mode": "once", "on_error": "stop", "job": map[string]any{"max_parallel": 8},
				"source": map[string]any{"module": "generate", "params": map[string]any{"records": records, "chunk_records": 500}},
				"sinks": []any{map[string]any{"name": "es", "batch": map[string]any{"max_records": 100, "max_wait": "10ms"},
					"sink": map[string]any{"module": "elasticsearch", "params": map[string]any{"urls": []string{c.url}, "ca_file": c.ca,
						"username": c.user, "password_env": "FIT_TEST_SYNC_PASSWORD", "index": index}}}}}}}})
			cfg, err := config.Load(b)
			if err != nil {
				t.Fatal(err)
			}
			st, err := fitsync.RunPipeline(context.Background(), cfg.Sync, "bp", fitsync.Options{})
			if err != nil || st.State != "finished" || st.Job == nil || st.Job.Failed != 0 {
				t.Fatalf("%v: %+v", err, st)
			}
			deletes := records / 7 // generate's every seventh record deletes a document never written
			if n := c.count(t, index); n != records-deletes {
				t.Errorf("%d documents, want %d", n, records-deletes)
			}
			if after := c.rejected(t); after == before {
				t.Errorf("the cluster refused no write (%d rejected): no 429 was tested", after)
			} else {
				t.Logf("the cluster refused %d writes with 429", after-before)
			}
		})
	}
}

// TestElasticsearchAPIKey writes with an Elasticsearch API key; OpenSearch,
// which has none, is refused one.
func TestElasticsearchAPIKey(t *testing.T) {
	for _, c := range clusters(t) {
		t.Run(c.name, func(t *testing.T) {
			index := "fit-sync-apikey"
			key := "a2V5OnNlY3JldA=="
			if c.name == "elasticsearch" {
				var r struct{ Encoded string }
				c.call(t, http.MethodPost, "/_security/api_key", `{"name": "fit-sync-test", "expiration": "1h"}`, &r)
				key = r.Encoded
			}
			t.Setenv("FIT_TEST_SYNC_API_KEY", key)
			s := c.sink(t, map[string]any{"username": nil, "password_env": nil, "api_key_env": "FIT_TEST_SYNC_API_KEY"})
			err := s.Write(context.Background(), []*fitsync.Record{rec(index, fitsync.OpInsert, 1, "k")})
			if c.name == "opensearch" {
				if err == nil || !strings.Contains(err.Error(), "no API keys") {
					t.Errorf("%v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := c.docs(t, index)["1"]; got != `{"id":1,"v":"k"}` {
				t.Errorf("document %s", got)
			}
		})
	}
}
