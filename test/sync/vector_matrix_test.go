package synctest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
)

// TestEmbedPostgresAcrossKills runs the matrix's PostgreSQL source (I192:
// a snapshot, then a load of inserts, updates, deletes, key changes and
// transactions, the pipeline killed at random points again and again)
// through the embed transform, against a fake embeddings API, into Qdrant
// and Milvus: each ends holding the source's rows, each with the vector of
// its text, the deletes gone (I221, I222). It runs in the obs family,
// which starts a PostgreSQL beside the vector stores.
func TestEmbedPostgresAcrossKills(t *testing.T) {
	q, mv := qdrantServer(t), milvusServer(t)
	pgs := pgServers(t)
	pg := pgs[len(pgs)-1]
	var calls atomic.Int64
	api := fakeEmbeddings(t, &calls)
	src := mxSource{name: "postgres" + pg.version, family: "postgres", txs: true, rekey: true,
		setup: func(t *testing.T, run string, m *mxModel) mxRun {
			r := mxPostgresSource(t, pg, run, m)
			r.transforms = append(r.transforms, map[string]any{"module": "embed", "params": map[string]any{"url": api, "model": "fake",
				"fields": []string{"v"}, "batch_records": 16}})
			return r
		}}
	t.Setenv("FIT_TEST_MILVUS_TOKEN", mv.token)
	sinks := []mxSink{
		{"qdrant", func(t *testing.T, run string) (map[string]any, func(*testing.T) mxState) {
			q.collection(t, run)
			return map[string]any{"module": "qdrant", "params": map[string]any{"url": q.url, "collection": run}},
				func(t *testing.T) mxState { return mxQdrantRows(t, q, run) }
		}},
		{"milvus", func(t *testing.T, run string) (map[string]any, func(*testing.T) mxState) {
			mv.collection(t, run)
			return map[string]any{"module": "milvus", "params": map[string]any{"url": mv.url, "token_env": "FIT_TEST_MILVUS_TOKEN", "collection": run}},
				func(t *testing.T) mxState { return mxMilvusRows(t, mv, run) }
		}},
	}
	mxRunSource(t, src, sinks, mxScale())
	t.Logf("%d embeddings requests", calls.Load())
}

// mxQdrantRows reads the collection's points as the matrix's rows, from
// their payload; a point whose vector is not its text's reads as a row of
// its own that no source holds.
func mxQdrantRows(t *testing.T, q *qdrant, name string) mxState {
	t.Helper()
	var r struct {
		Result struct {
			Points []struct {
				Payload json.RawMessage
				Vector  []float64
			}
		}
	}
	if err := json.Unmarshal(q.call(t, http.MethodPost, "/collections/"+name+"/points/scroll", `{"limit": 100000, "with_payload": true, "with_vector": true}`), &r); err != nil {
		t.Fatal(err)
	}
	st := mxState{}
	for _, p := range r.Result.Points {
		id, line := mxJSON(t, p.Payload)
		var row struct{ V string }
		_ = json.Unmarshal(p.Payload, &row)
		if !sameDirection(p.Vector, fakeVector(row.V)) {
			line = "a vector not of its text: " + line
		}
		st[id] = line
	}
	return st
}

// mxMilvusRows reads the collection's entities as the matrix's rows.
func mxMilvusRows(t *testing.T, m *milvus, name string) mxState {
	t.Helper()
	data := m.call(t, "/v2/vectordb/entities/query", fmt.Sprintf(`{"collectionName": %q, "filter": "id >= 0", "limit": 16384,
		"outputFields": ["*"], "consistencyLevel": "Strong"}`, name))
	var rows []json.RawMessage
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatal(err)
	}
	st := mxState{}
	for _, raw := range rows {
		id, line := mxJSON(t, raw)
		var row struct {
			V         string
			Embedding []float64
		}
		_ = json.Unmarshal(raw, &row)
		want := fakeVector(row.V)
		for i := range want {
			if len(row.Embedding) != vecDims || float32(row.Embedding[i]) != float32(want[i]) {
				line = "a vector not of its text: " + line
				break
			}
		}
		st[id] = line
	}
	return st
}
