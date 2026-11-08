package synctest

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	_ "github.com/lesismal/fit/sync/mongo"
	mongowire "github.com/lesismal/fit/sync/mongo/wire"
)

// mongoSinkParams are the sink's params for a set's connection string: its
// password moved to an environment variable, TLS with the run's CA when
// tls is true.
func mongoSinkParams(t *testing.T, uri string, tls bool, extra map[string]any) map[string]any {
	t.Helper()
	rest, _ := strings.CutPrefix(uri, "mongodb://")
	at := strings.LastIndexByte(rest, '@')
	user, pass, _ := strings.Cut(rest[:at], ":")
	pass, _ = url.PathUnescape(pass)
	t.Setenv("FIT_TEST_SYNC_MONGO_PASSWORD", pass)
	p := map[string]any{"uri": "mongodb://" + rest[at+1:], "username": user, "password_env": "FIT_TEST_SYNC_MONGO_PASSWORD"}
	if ca := os.Getenv("FIT_SYNC_MONGO_CA"); tls && ca != "" {
		p["tls"], p["ca_file"] = true, ca
	}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

func mongoSinkConfig(t *testing.T, stateDir string, p map[string]any) *config.Sync {
	t.Helper()
	p["name"] = "p"
	b, _ := json.Marshal(map[string]any{"sync": map[string]any{"state_dir": stateDir, "pipelines": []any{p}}})
	cfg, err := config.Load(b)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Sync
}

func mongoSink(t *testing.T, params map[string]any) fitsync.Sink {
	t.Helper()
	cfg := mongoSinkConfig(t, t.TempDir(), map[string]any{"source": map[string]any{"module": "generate"},
		"sinks": []any{map[string]any{"name": "s", "sink": map[string]any{"module": "mongo", "params": params}}}})
	m, err := module.Load(fitsync.Sinks, cfg.Pipelines[0].Sinks[0].Sink, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { module.Cleanup(m) })
	return m.(fitsync.Sink)
}

// mongoDocs returns a collection's documents, sorted by _id, as plain JSON.
func mongoDocs(t *testing.T, ctx context.Context, c *mongowire.Client, db, coll string) []string {
	t.Helper()
	cur, err := c.Cursor(ctx, db, mongowire.Doc("find", coll, "sort", mongowire.Doc("_id", 1)), mongowire.CursorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer cur.Close(ctx)
	var out []string
	for {
		d, err := cur.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if d == nil {
			return out
		}
		out = append(out, d.String())
	}
}

// TestMongoSinkConverges writes inserts, updates and deletes of 100 ids in
// batches of random sizes, each batch sometimes written again from an
// earlier record, as a replay after a crash would: the collection ends as
// the changes leave it.
func TestMongoSinkConverges(t *testing.T) {
	for name, uri := range mongoSets(t) {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute) // a hang guard
			defer cancel()
			c := mongoClient(t, uri, nil)
			const db, coll = "fit_sink_test", "converge"
			_, _ = c.Run(ctx, db, mongowire.Doc("drop", coll))
			s := mongoSink(t, mongoSinkParams(t, uri, false, map[string]any{"write_concern": "majority", "journal": true}))

			seed := uint64(time.Now().UnixNano())
			t.Logf("seed %d", seed)
			rnd := rand.New(rand.NewPCG(seed, 3))
			want := map[int64]string{}
			var recs []*fitsync.Record
			for n := range 3000 {
				id := int64(rnd.IntN(100))
				key := fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(id)})
				row := fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(id)}, fitsync.Field{Name: "n", Value: fitsync.Int(int64(n))},
					fitsync.Field{Name: "price", Value: fitsync.Decimal(fmt.Sprintf("%d.%02d", n, n%100))})
				r := &fitsync.Record{Namespace: db + "." + coll, Key: key}
				switch _, ok := want[id]; {
				case ok && rnd.IntN(4) == 0:
					r.Op, r.Before = fitsync.OpDelete, row
					delete(want, id)
				case ok:
					r.Op, r.After = fitsync.OpUpdate, row
				default:
					r.Op, r.After = fitsync.OpInsert, row
				}
				if r.Op != fitsync.OpDelete {
					want[id] = fmt.Sprintf(`{"_id":%d,"id":%d,"n":%d,"price":%d.%02d}`, id, id, n, n, n%100)
				}
				recs = append(recs, r)
			}
			for i := 0; i < len(recs); {
				end := min(len(recs), i+1+rnd.IntN(200))
				if err := s.Write(ctx, recs[i:end]); err != nil {
					t.Fatal(err)
				}
				if rnd.IntN(3) == 0 {
					end = max(0, i-rnd.IntN(300)) // replayed from an earlier checkpoint
				}
				i = end
			}
			got := mongoDocs(t, ctx, c, db, coll)
			if len(got) != len(want) {
				t.Fatalf("%d documents, want %d", len(got), len(want))
			}
			for _, d := range got {
				var id struct {
					ID int64 `json:"_id"`
				}
				json.Unmarshal([]byte(d), &id)
				if want[id.ID] != d {
					t.Errorf("%s, want %s", d, want[id.ID])
				}
			}
		})
	}
}

// TestMongoSinkAcrossKills runs generate's records, of every type, into a
// collection, the pipeline killed at random points and started again from
// its checkpoint, over TLS: the collection ends as one written without a
// kill.
func TestMongoSinkAcrossKills(t *testing.T) {
	for name, uri := range mongoSets(t) {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute) // a hang guard
			defer cancel()
			c := mongoClient(t, uri, nil)
			const db = "fit_sink_kill"
			for _, coll := range []string{"ref", "killed"} {
				_, _ = c.Run(ctx, db, mongowire.Doc("drop", coll))
			}
			pipeline := func(coll string, rnd *rand.Rand) *config.Sync {
				return mongoSinkConfig(t, t.TempDir(), map[string]any{"mode": "once", "checkpoint_interval": "1ms",
					"queue":  map[string]any{"max_records": 20 + rnd.IntN(200)},
					"source": map[string]any{"module": "generate", "params": map[string]any{"records": 2000, "tx_size": 1 + rnd.IntN(9)}},
					"sinks": []any{map[string]any{"name": "m", "batch": map[string]any{"max_records": 1 + rnd.IntN(100), "max_wait": "1ms"},
						"sink": map[string]any{"module": "mongo", "params": mongoSinkParams(t, uri, true, map[string]any{"database": db, "collection": coll})}}}})
			}
			seed := uint64(time.Now().UnixNano())
			t.Logf("seed %d", seed)
			rnd := rand.New(rand.NewPCG(seed, 5))
			if _, err := fitsync.RunPipeline(ctx, pipeline("ref", rnd), "p", fitsync.Options{}); err != nil {
				t.Fatal(err)
			}

			cfg := pipeline("killed", rnd)
			points := []fitsync.Point{fitsync.CrashEmitted, fitsync.CrashDispatched, fitsync.CrashWritten,
				fitsync.CrashAcknowledged, fitsync.CrashCheckpointWritten, fitsync.CrashCheckpointSaved}
			kills := 0
			for range 12 {
				at, nth := points[rnd.IntN(len(points))], int32(1+rnd.IntN(30))
				var seen atomic.Int32
				st, err := fitsync.RunPipeline(ctx, cfg, "p", fitsync.Options{Crash: func(_ string, p fitsync.Point) bool {
					return p == at && seen.Add(1) == nth
				}})
				if err != nil {
					t.Fatal(err)
				}
				if st.State == "killed" {
					kills++
				}
			}
			if st, err := fitsync.RunPipeline(ctx, cfg, "p", fitsync.Options{}); err != nil || st.State == "killed" {
				t.Fatalf("%v (%+v)", err, st)
			}
			ref, got := mongoDocs(t, ctx, c, db, "ref"), mongoDocs(t, ctx, c, db, "killed")
			if len(ref) == 0 || strings.Join(ref, "\n") != strings.Join(got, "\n") {
				t.Fatalf("%d documents after %d kills, %d without", len(got), kills, len(ref))
			}
			t.Logf("%d documents across %d kills", len(got), kills)
		})
	}
}

// TestMongoSinkFromMongo copies a collection of every BSON type to another,
// its documents read as records with the key _id, written through the sink
// in batches, twice: the copy holds the same documents.
func TestMongoSinkFromMongo(t *testing.T) {
	for name, uri := range mongoSets(t) {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute) // a hang guard
			defer cancel()
			c := mongoClient(t, uri, nil)
			const db = "fit_sink_copy"
			for _, coll := range []string{"src", "dst"} {
				_, _ = c.Run(ctx, db, mongowire.Doc("drop", coll))
			}
			oid, _ := mongowire.ParseObjectID("65a1b2c3d4e5f60718293a4b")
			docs := []mongowire.Raw{}
			for i := range 300 {
				var id any = int64(i)
				switch i % 3 {
				case 1:
					id = fmt.Sprintf("s-%d", i)
				case 2:
					o := oid
					o[11] = byte(i)
					o[10] = byte(i >> 8)
					id = o
				}
				dec, _ := mongowire.ParseDecimal128(fmt.Sprintf("%d.10", i))
				d, err := mongowire.Marshal(mongowire.Doc("_id", id, "n", int64(i), "f", float64(i)/3, "dec", dec,
					"t", time.UnixMilli(1_700_000_000_000+int64(i)).UTC(), "b", []byte{byte(i), 0},
					"uuid", mongowire.Binary{Subtype: 4, Data: []byte("0123456789abcdef")},
					"ts", mongowire.Timestamp{T: 1_700_000_000, I: uint32(i)}, "re", mongowire.Regex{Pattern: "^a", Options: "i"},
					"sub", mongowire.Doc("a", mongowire.A{int64(1), "x", nil, mongowire.Doc("deep", true)}),
					"min", mongowire.MinKey{}, "max", mongowire.MaxKey{}, "null", nil))
				if err != nil {
					t.Fatal(err)
				}
				docs = append(docs, d)
			}
			mongoRun(t, ctx, c, db, mongowire.Doc("insert", "src"), mongowire.Sequence{ID: "documents", Docs: docs})

			s := mongoSink(t, mongoSinkParams(t, uri, true, map[string]any{"collection": "dst"}))
			var recs []*fitsync.Record
			for _, d := range mongoDocsRaw(t, ctx, c, db, "src") {
				v, err := d.Value()
				if err != nil {
					t.Fatal(err)
				}
				id, _ := v.Get("_id")
				recs = append(recs, &fitsync.Record{Namespace: db + ".src", Op: fitsync.OpInsert,
					Key: fitsync.Map(fitsync.Field{Name: "_id", Value: id}), After: v})
			}
			for range 2 {
				for i := 0; i < len(recs); i += 70 {
					if err := s.Write(ctx, recs[i:min(len(recs), i+70)]); err != nil {
						t.Fatal(err)
					}
				}
			}
			src, dst := mongoDocsRaw(t, ctx, c, db, "src"), mongoDocsRaw(t, ctx, c, db, "dst")
			if len(src) != 300 || len(dst) != 300 {
				t.Fatalf("%d documents copied of %d", len(dst), len(src))
			}
			for i := range src {
				if string(src[i]) != string(dst[i]) {
					t.Fatalf("copied as\n%s\nfrom\n%s", dst[i], src[i])
				}
			}
			// A document refused (a validator) fails its record for good.
			mongoRun(t, ctx, c, db, mongowire.Doc("collMod", "dst", "validator", mongowire.Doc("n", mongowire.Doc("$lt", int64(1000)))))
			bad := &fitsync.Record{Namespace: db + ".src", Op: fitsync.OpUpdate, Key: recs[0].Key,
				After: fitsync.Map(fitsync.Field{Name: "n", Value: fitsync.Int(5000)})}
			err := s.Write(ctx, []*fitsync.Record{recs[1], bad, recs[2]})
			var be *fitsync.BatchError
			if !asBatchError(err, &be) || be.Index != 1 || !fitsync.IsPermanent(err) {
				t.Fatalf("a document the validator refuses: %v", err)
			}
			if tt, ok := s.(fitsync.Tester); !ok || tt.Test(ctx) != nil {
				t.Error("the connection test failed")
			}
		})
	}
}

func asBatchError(err error, be **fitsync.BatchError) bool {
	b, ok := err.(*fitsync.BatchError)
	*be = b
	return ok
}

func mongoDocsRaw(t *testing.T, ctx context.Context, c *mongowire.Client, db, coll string) []mongowire.Raw {
	t.Helper()
	cur, err := c.Cursor(ctx, db, mongowire.Doc("find", coll, "sort", mongowire.Doc("_id", 1)), mongowire.CursorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer cur.Close(ctx)
	var out []mongowire.Raw
	for {
		d, err := cur.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if d == nil {
			return out
		}
		out = append(out, append(mongowire.Raw(nil), d...))
	}
}
