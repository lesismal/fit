package synctest

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	fitsync "github.com/lesismal/fit/sync"
	_ "github.com/lesismal/fit/sync/embed"
	_ "github.com/lesismal/fit/sync/milvus"
	_ "github.com/lesismal/fit/sync/qdrant"
)

// vecDims is the size of the tests' vectors.
const vecDims = 4

// fakeVector is the vector the fake embeddings API makes of text.
func fakeVector(text string) []float64 {
	h := sha256.Sum256([]byte(text))
	v := make([]float64, vecDims)
	for i := range v {
		v[i] = float64(h[i]) / 256
	}
	return v
}

// fakeEmbeddings is an OpenAI-compatible embeddings API making
// fakeVector's vectors; calls counts the requests.
func fakeEmbeddings(t *testing.T, calls *atomic.Int64) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req struct{ Input []string }
		if r.URL.Path != "/v1/embeddings" || json.NewDecoder(r.Body).Decode(&req) != nil {
			http.Error(w, "no", http.StatusBadRequest)
			return
		}
		var data []map[string]any
		for i, in := range req.Input {
			data = append(data, map[string]any{"index": i, "embedding": fakeVector(in)})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/v1"
}

// vecRec is a record of id with text v and its fake vector.
func vecRec(ns string, op fitsync.Op, id int64, v string, vec []float64) *fitsync.Record {
	elems := make([]fitsync.Value, len(vec))
	for i, f := range vec {
		elems[i] = fitsync.Float(f)
	}
	row := fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(id)}, fitsync.Field{Name: "v", Value: fitsync.String(v)},
		fitsync.Field{Name: "embedding", Value: fitsync.Array(elems...)})
	r := &fitsync.Record{Namespace: ns, Op: op, Key: fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(id)})}
	if op == fitsync.OpDelete {
		r.Before = row
	} else {
		r.After = row
	}
	return r
}

// vecChanges are inserts of 200 ids, then updates, deletes and inserts
// again of some, and the text each id is left with.
func vecChanges(ns string) ([]*fitsync.Record, map[int64]string) {
	var recs []*fitsync.Record
	want := map[int64]string{}
	add := func(op fitsync.Op, id int64, v string) {
		recs = append(recs, vecRec(ns, op, id, v, fakeVector(v)))
		if op == fitsync.OpDelete {
			delete(want, id)
		} else {
			want[id] = v
		}
	}
	for id := range int64(200) {
		add(fitsync.OpInsert, id, fmt.Sprint("v1-", id))
	}
	for id := range int64(200) {
		switch {
		case id%10 == 0:
			add(fitsync.OpDelete, id, fmt.Sprint("v1-", id))
			add(fitsync.OpInsert, id, fmt.Sprint("v3-", id))
		case id%5 == 0:
			add(fitsync.OpDelete, id, fmt.Sprint("v1-", id))
		case id%2 == 0:
			add(fitsync.OpUpdate, id, fmt.Sprint("v2-", id))
		}
	}
	return recs, want
}

// qdrant is the server the Qdrant tests write to.
type qdrant struct{ url string }

func qdrantServer(t *testing.T) *qdrant { return &qdrant{url: obsEnv(t, "FIT_SYNC_QDRANT_URL")} }

func (q *qdrant) call(t *testing.T, method, path, body string) []byte {
	return httpDo(t, method, q.url+path, http.Header{"Content-Type": {"application/json"}}, body)
}

// collection makes the collection name anew, of vectors of vecDims.
func (q *qdrant) collection(t *testing.T, name string) {
	req, _ := http.NewRequest(http.MethodDelete, q.url+"/collections/"+name, nil)
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
	}
	q.call(t, http.MethodPut, "/collections/"+name, fmt.Sprintf(`{"vectors": {"size": %d, "distance": "Cosine"}}`, vecDims))
}

// points returns the collection's points' payload field v, by the payload's
// id, and checks each vector is fakeVector's of it.
func (q *qdrant) points(t *testing.T, name string) map[int64]string {
	t.Helper()
	var r struct {
		Result struct {
			Points []struct {
				Payload struct {
					ID int64
					V  string
				}
				Vector []float64
			}
		}
	}
	b := q.call(t, http.MethodPost, "/collections/"+name+"/points/scroll", `{"limit": 10000, "with_payload": true, "with_vector": true}`)
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	out := map[int64]string{}
	for _, p := range r.Result.Points {
		out[p.Payload.ID] = p.Payload.V
		if !sameDirection(p.Vector, fakeVector(p.Payload.V)) {
			t.Errorf("point %d's vector %v is not of %q", p.Payload.ID, p.Vector, p.Payload.V)
		}
	}
	return out
}

// sameDirection reports whether a and b point the same way: Qdrant keeps a
// cosine collection's vectors normalized.
func sameDirection(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	var dot, na, nb float64
	for i := range a {
		dot, na, nb = dot+a[i]*b[i], na+a[i]*a[i], nb+b[i]*b[i]
	}
	return dot*dot > 0.9999*na*nb
}

// milvus is the server the Milvus tests write to.
type milvus struct{ url, token string }

func milvusServer(t *testing.T) *milvus {
	m := &milvus{url: obsEnv(t, "FIT_SYNC_MILVUS_URL"), token: os.Getenv("FIT_SYNC_MILVUS_TOKEN")}
	if m.token == "" {
		m.token = "root:Milvus"
	}
	return m
}

func (m *milvus) call(t *testing.T, path, body string) json.RawMessage {
	t.Helper()
	b := httpDo(t, http.MethodPost, m.url+path, http.Header{"Content-Type": {"application/json"}, "Authorization": {"Bearer " + m.token}}, body)
	var r struct {
		Code    int
		Message string
		Data    json.RawMessage
	}
	if err := json.Unmarshal(b, &r); err != nil || r.Code != 0 {
		t.Fatalf("%s: %s", path, b)
	}
	return r.Data
}

// collection makes the collection name anew: an Int64 primary key id, a
// vector of vecDims, and the dynamic field on, loaded.
func (m *milvus) collection(t *testing.T, name string) {
	m.call(t, "/v2/vectordb/collections/drop", fmt.Sprintf(`{"collectionName": %q}`, name))
	m.call(t, "/v2/vectordb/collections/create", fmt.Sprintf(`{"collectionName": %q, "dimension": %d, "primaryFieldName": "id",
		"idType": "Int64", "vectorFieldName": "embedding", "metricType": "L2", "autoId": false}`, name, vecDims))
}

// entities returns the collection's field v, by id, and checks each vector
// is fakeVector's of it.
func (m *milvus) entities(t *testing.T, name string) map[int64]string {
	t.Helper()
	var rows []struct {
		ID        int64
		V         string
		Embedding []float64
	}
	data := m.call(t, "/v2/vectordb/entities/query", fmt.Sprintf(`{"collectionName": %q, "filter": "id >= 0", "limit": 16384,
		"outputFields": ["id", "v", "embedding"], "consistencyLevel": "Strong"}`, name))
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatal(err)
	}
	out := map[int64]string{}
	for _, r := range rows {
		out[r.ID] = r.V
		want := fakeVector(r.V)
		for i := range want {
			if len(r.Embedding) != vecDims || float32(r.Embedding[i]) != float32(want[i]) {
				t.Errorf("entity %d's vector %v is not of %q", r.ID, r.Embedding, r.V)
				break
			}
		}
	}
	return out
}

func (m *milvus) sink(t *testing.T, params map[string]any) fitsync.Sink {
	t.Setenv("FIT_TEST_MILVUS_TOKEN", m.token)
	p := map[string]any{"url": m.url, "token_env": "FIT_TEST_MILVUS_TOKEN"}
	for k, v := range params {
		p[k] = v
	}
	return loadSink(t, "milvus", p)
}

func diffIDs(t *testing.T, got, want map[int64]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%d rows, want %d", len(got), len(want))
	}
	var bad []string
	for id, v := range want {
		if got[id] != v {
			bad = append(bad, fmt.Sprintf("%d: %q, want %q", id, got[id], v))
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Errorf("%d differ: %s", len(bad), strings.Join(bad[:min(len(bad), 10)], "; "))
	}
}

// TestQdrantConverges upserts and deletes points, then the batch again from
// its middle and from its start: the collection ends as the changes say;
// a vector of the wrong size fails for good at its record.
func TestQdrantConverges(t *testing.T) {
	q := qdrantServer(t)
	q.collection(t, "fit_converge")
	s := loadSink(t, "qdrant", map[string]any{"url": q.url, "collection": "fit_{namespace}"})
	recs, want := vecChanges("converge")
	write(t, s, recs, 37)
	write(t, s, recs[len(recs)/2:], 23)
	write(t, s, recs, 101)
	diffIDs(t, q.points(t, "fit_converge"), want)
	if err := loadSink(t, "qdrant", map[string]any{"url": q.url, "collection": "fit_converge"}).(fitsync.Tester).Test(context.Background()); err != nil {
		t.Fatal(err)
	}
	bad := []*fitsync.Record{vecRec("converge", fitsync.OpInsert, 1000, "a", fakeVector("a")), vecRec("converge", fitsync.OpInsert, 1001, "b", []float64{1, 2}),
		vecRec("converge", fitsync.OpInsert, 1002, "c", fakeVector("c"))}
	err := s.Write(context.Background(), bad)
	var be *fitsync.BatchError
	if !errors.As(err, &be) || be.Index != 1 || !fitsync.IsPermanent(err) {
		t.Fatalf("%v", err)
	}
}

// TestMilvusConverges is TestQdrantConverges's for Milvus.
func TestMilvusConverges(t *testing.T) {
	m := milvusServer(t)
	m.collection(t, "fit_converge")
	s := m.sink(t, map[string]any{"collection": "fit_{namespace}"})
	recs, want := vecChanges("converge")
	write(t, s, recs, 37)
	write(t, s, recs[len(recs)/2:], 23)
	write(t, s, recs, 101)
	diffIDs(t, m.entities(t, "fit_converge"), want)
	if err := m.sink(t, map[string]any{"collection": "fit_converge"}).(fitsync.Tester).Test(context.Background()); err != nil {
		t.Fatal(err)
	}
	bad := []*fitsync.Record{vecRec("converge", fitsync.OpInsert, 1000, "a", fakeVector("a")), vecRec("converge", fitsync.OpInsert, 1001, "b", []float64{1, 2}),
		vecRec("converge", fitsync.OpInsert, 1002, "c", fakeVector("c"))}
	err := s.Write(context.Background(), bad)
	var be *fitsync.BatchError
	if !errors.As(err, &be) || be.Index != 1 || !fitsync.IsPermanent(err) {
		t.Fatalf("%v", err)
	}
}

// TestEmbedToVectorsAcrossKills copies generate's records, inserts,
// updates and deletes, through the embed transform (against a fake
// embeddings API) into Qdrant and Milvus, killed at random points and
// started again from the checkpoint: both end with every row the records
// leave, each with the vector of its text, and the deletes gone.
func TestEmbedToVectorsAcrossKills(t *testing.T) {
	q, m := qdrantServer(t), milvusServer(t)
	q.collection(t, "fit_embed")
	m.collection(t, "fit_embed")
	var calls atomic.Int64
	api := fakeEmbeddings(t, &calls)
	t.Setenv("FIT_TEST_MILVUS_TOKEN", m.token)
	const records = 3000
	cfg := loadSync(t, t.TempDir(), map[string]any{"name": "p", "mode": "once", "on_error": "stop",
		"source":     map[string]any{"module": "generate", "params": map[string]any{"records": records, "tx_size": 10}},
		"transforms": []any{map[string]any{"module": "embed", "params": map[string]any{"url": api, "model": "fake", "fields": []string{"name"}, "into": "embedding"}}},
		"sinks": []any{
			map[string]any{"name": "q", "sink": map[string]any{"module": "qdrant", "params": map[string]any{"url": q.url, "collection": "fit_embed", "payload": []string{"id", "name"}}},
				"batch": map[string]any{"max_records": 97}},
			map[string]any{"name": "m", "sink": map[string]any{"module": "milvus", "params": map[string]any{"url": m.url, "token_env": "FIT_TEST_MILVUS_TOKEN",
				"collection": "fit_embed", "fields": []string{"name", "embedding"}}}, "batch": map[string]any{"max_records": 131}},
		}})
	seed := uint64(time.Now().UnixNano())
	t.Logf("seed %d", seed)
	kills := runKilled(t, cfg, rand.New(rand.NewPCG(seed, 21)), 8)
	t.Logf("%d kills, %d embeddings requests", kills, calls.Load())
	want := map[int64]string{}
	for n := range int64(records) {
		if n%7 != 6 {
			want[n] = fmt.Sprint("record-", n)
		}
	}
	gotQ := map[int64]string{}
	var r struct {
		Result struct {
			Points []struct {
				Payload struct {
					ID   int64
					Name string
				}
				Vector []float64
			}
		}
	}
	if err := json.Unmarshal(q.call(t, http.MethodPost, "/collections/fit_embed/points/scroll", `{"limit": 100000, "with_payload": true, "with_vector": true}`), &r); err != nil {
		t.Fatal(err)
	}
	for _, p := range r.Result.Points {
		gotQ[p.Payload.ID] = p.Payload.Name
		if !sameDirection(p.Vector, fakeVector(p.Payload.Name)) {
			t.Errorf("point %d's vector is not of %q", p.Payload.ID, p.Payload.Name)
		}
	}
	diffIDs(t, gotQ, want)
	var rows []struct {
		ID        int64
		Name      string
		Embedding []float64
	}
	data := m.call(t, "/v2/vectordb/entities/query", `{"collectionName": "fit_embed", "filter": "id >= 0", "limit": 16384,
		"outputFields": ["id", "name", "embedding"], "consistencyLevel": "Strong"}`)
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatal(err)
	}
	gotM := map[int64]string{}
	for _, row := range rows {
		gotM[row.ID] = row.Name
		if v := fakeVector(row.Name); len(row.Embedding) != vecDims || float32(row.Embedding[0]) != float32(v[0]) {
			t.Errorf("entity %d's vector is not of %q", row.ID, row.Name)
		}
	}
	diffIDs(t, gotM, want)
}
