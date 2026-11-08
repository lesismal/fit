package synctest

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	mrand "math/rand/v2"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	_ "github.com/lesismal/fit/sync/mongo"
	mongowire "github.com/lesismal/fit/sync/mongo/wire"
)

// mongoSourceParams are the source mongo's params for the set at uri, its
// password moved to an environment variable, with extra added.
func mongoSourceParams(t *testing.T, uri string, extra map[string]any) map[string]any {
	t.Helper()
	scheme, rest, _ := strings.Cut(uri, "://")
	at := strings.LastIndexByte(rest, '@')
	user, pass, _ := strings.Cut(rest[:at], ":")
	pass = strings.ReplaceAll(pass, "%21", "!")
	t.Setenv("FIT_TEST_SYNC_MONGO_PASSWORD", pass)
	p := map[string]any{"uri": scheme + "://" + user + "@" + rest[at+1:], "password_env": "FIT_TEST_SYNC_MONGO_PASSWORD"}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

// mongoPipeline is a config of one pipeline from the source mongo to the
// memory store named store.
func mongoPipeline(t *testing.T, stateDir, store string, params map[string]any, extra map[string]any) *config.Sync {
	t.Helper()
	p := map[string]any{"name": "p", "source": map[string]any{"module": "mongo", "params": params},
		"sinks": []any{map[string]any{"name": "s", "batch": map[string]any{"max_records": 50, "max_wait": "1ms"}, "sink": map[string]any{"module": "memory", "params": map[string]any{"store": store, "max_records": 1000000}}}}}
	for k, v := range extra {
		p[k] = v
	}
	b, _ := json.Marshal(map[string]any{"sync": map[string]any{"state_dir": stateDir, "pipelines": []any{p}}})
	cfg, err := config.Load(b)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Sync
}

func mongoSource(t *testing.T, params map[string]any) fitsync.Source {
	t.Helper()
	cfg := mongoPipeline(t, t.TempDir(), "unused", params, nil)
	m, err := module.Load(fitsync.Sources, cfg.Pipelines[0].Source, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { module.Cleanup(m) })
	return m.(fitsync.Source)
}

// running is a source run on a goroutine of its own, its records on a
// channel.
type running struct {
	recs   chan *fitsync.Record
	cancel context.CancelFunc
	done   chan error
}

func runSource(src fitsync.Source, from fitsync.Position, once bool) *running {
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{recs: make(chan *fitsync.Record, 100000), cancel: cancel, done: make(chan error, 1)}
	go func() {
		r.done <- src.Run(ctx, &fitsync.Run{From: from, Once: once, Emit: func(rec *fitsync.Record) error {
			select {
			case r.recs <- rec:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}})
	}()
	return r
}

// take returns the next n records, failing after a hang guard.
func (r *running) take(t *testing.T, n int) []*fitsync.Record {
	t.Helper()
	var out []*fitsync.Record
	guard := time.After(2 * time.Minute)
	for len(out) < n {
		select {
		case rec := <-r.recs:
			out = append(out, rec)
		case err := <-r.done:
			t.Fatalf("the source stopped after %d of %d records: %v", len(out), n, err)
		case <-guard:
			t.Fatalf("%d of %d records", len(out), n)
		}
	}
	return out
}

func (r *running) stop(t *testing.T) error {
	t.Helper()
	r.cancel()
	select {
	case err := <-r.done:
		return err
	case <-time.After(time.Minute):
		t.Fatal("the source did not stop")
		return nil
	}
}

func recID(r *fitsync.Record) string {
	id, _ := r.Key.Get("_id")
	b, _ := fitsync.AppendPlain(nil, id)
	return string(b)
}

func mongoInsert(t *testing.T, ctx context.Context, c *mongowire.Client, db, coll string, docs ...mongowire.D) {
	t.Helper()
	a := make(mongowire.A, len(docs))
	for i, d := range docs {
		a[i] = d
	}
	mongoRun(t, ctx, c, db, mongowire.Doc("insert", coll, "documents", a, "writeConcern", mongowire.Doc("w", "majority")))
}

// TestMongoSourceStream reads a snapshot of two collections, then their
// changes: inserts, updates, replaces, deletes and a transaction's, each
// as a record of its kind; a restart from the last position reads only what
// came after it, a dropped collection is reported, and a stream of one
// collection dropped says a snapshot is needed.
func TestMongoSourceStream(t *testing.T) {
	for name, uri := range mongoSets(t) {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute) // a hang guard
			defer cancel()
			c := mongoClient(t, uri, nil)
			db := "fit_src_" + name
			mongoRun(t, ctx, c, db, mongowire.Doc("dropDatabase", 1))
			var docs []mongowire.D
			for i := range 40 {
				docs = append(docs, mongowire.Doc("_id", i, "n", i))
			}
			docs = append(docs, mongowire.Doc("_id", "s1", "n", -1)) // after the numbers, in _id order
			mongoInsert(t, ctx, c, db, "a", docs...)
			mongoInsert(t, ctx, c, db, "b", mongowire.Doc("_id", 1, "v", "b1"), mongowire.Doc("_id", 2, "v", "b2"))

			params := mongoSourceParams(t, uri, map[string]any{"database": db, "collections": []string{"b", "a"}, "batch_size": 7})
			// The connection params the sink takes too (I183b): the user in
			// username, TLS in tls and ca_file rather than in uri.
			scheme, rest, _ := strings.Cut(params["uri"].(string), "://")
			user, hosts, _ := strings.Cut(rest, "@")
			params["uri"], params["username"] = scheme+"://"+hosts, user
			if ca := os.Getenv("FIT_SYNC_MONGO_CA"); ca != "" {
				params["tls"], params["ca_file"] = true, ca
			}
			src := mongoSource(t, params)
			if err := src.(fitsync.Tester).Test(ctx); err != nil {
				t.Fatal(err)
			}
			run := runSource(src, nil, false)
			snap := run.take(t, 43)
			var got []string
			for _, r := range snap {
				if r.Op != fitsync.OpInsert {
					t.Fatalf("a snapshot's %s", r.Op)
				}
				got = append(got, r.Namespace+"/"+recID(r))
			}
			if got[0] != db+".a/0" || got[40] != db+`.a/"s1"` || got[41] != db+".b/1" || got[42] != db+".b/2" {
				t.Fatalf("the snapshot in the order %v", got)
			}
			if p := string(snap[42].Position); !strings.HasPrefix(p, `{"token":`) {
				t.Fatalf("the snapshot's last position %s", p)
			}
			if p := string(snap[3].Position); !strings.Contains(p, `"ns":"`+db+`.a"`) || !strings.Contains(p, `"id":{"int":3}`) {
				t.Fatalf("a snapshot's position %s", p)
			}

			mongoInsert(t, ctx, c, db, "a", mongowire.Doc("_id", 100, "n", 100))
			mongoRun(t, ctx, c, db, mongowire.Doc("update", "a", "updates", mongowire.A{
				mongowire.Doc("q", mongowire.Doc("_id", 1), "u", mongowire.Doc("$set", mongowire.Doc("n", 1001))),
				mongowire.Doc("q", mongowire.Doc("_id", 2), "u", mongowire.Doc("n", 2002, "replaced", true)),
			}))
			mongoRun(t, ctx, c, db, mongowire.Doc("delete", "a", "deletes", mongowire.A{mongowire.Doc("q", mongowire.Doc("_id", 3), "limit", 1)}))
			// A transaction of three writes in two collections.
			uuid := make([]byte, 16)
			rand.Read(uuid)
			lsid := mongowire.Doc("id", mongowire.Binary{Subtype: 4, Data: uuid})
			tx := func(cmd mongowire.D, first bool) {
				cmd = append(cmd, mongowire.Doc("lsid", lsid, "txnNumber", int64(1), "autocommit", false)...)
				if first {
					cmd = append(cmd, mongowire.Doc("startTransaction", true)...)
				}
				mongoRun(t, ctx, c, db, cmd)
			}
			tx(mongowire.Doc("insert", "a", "documents", mongowire.A{mongowire.Doc("_id", 101)}), true)
			tx(mongowire.Doc("insert", "b", "documents", mongowire.A{mongowire.Doc("_id", 3, "v", "b3")}), false)
			tx(mongowire.Doc("delete", "b", "deletes", mongowire.A{mongowire.Doc("q", mongowire.Doc("_id", 1), "limit", 1)}), false)
			mongoRun(t, ctx, c, "admin", mongowire.Doc("commitTransaction", 1, "lsid", lsid, "txnNumber", int64(1), "autocommit", false))
			mongoInsert(t, ctx, c, db, "b", mongowire.Doc("_id", 4))

			changes := run.take(t, 8)
			var seen []string
			for _, r := range changes {
				seen = append(seen, fmt.Sprintf("%s %s/%s first=%v last=%v", r.Op, r.Namespace[len(db)+1:], recID(r), r.Tx.First, r.Tx.Last))
				if r.CommitTime.IsZero() || !strings.HasPrefix(string(r.Position), `{"token":`) {
					t.Errorf("record %s: commit time %v, position %s", recID(r), r.CommitTime, r.Position)
				}
			}
			want := []string{"insert a/100 first=false last=false", "update a/1 first=false last=false", "update a/2 first=false last=false",
				"delete a/3 first=false last=false", "insert a/101 first=true last=false", "insert b/3 first=false last=false",
				"delete b/1 first=false last=true", "insert b/4 first=false last=false"}
			if strings.Join(seen, "\n") != strings.Join(want, "\n") {
				t.Fatalf("the changes:\n%s\nwant:\n%s", strings.Join(seen, "\n"), strings.Join(want, "\n"))
			}
			if n, _ := changes[1].After.Get("n"); n.AsInt() != 1001 {
				t.Errorf("the update's after %v", changes[1].After)
			}
			if r, _ := changes[2].After.Get("replaced"); !r.AsBool() {
				t.Errorf("the replace's after %v", changes[2].After)
			}
			if changes[4].Tx.ID == "" || changes[4].Tx.ID != changes[6].Tx.ID || changes[7].Tx.ID != "" {
				t.Errorf("the transaction's IDs %q %q %q", changes[4].Tx.ID, changes[6].Tx.ID, changes[7].Tx.ID)
			}
			if err := run.stop(t); !errors.Is(err, context.Canceled) {
				t.Fatalf("stopped: %v", err)
			}

			// A restart from the last position reads only what came after.
			mongoInsert(t, ctx, c, db, "a", mongowire.Doc("_id", 200))
			run = runSource(src, changes[7].Position, false)
			if r := run.take(t, 1)[0]; recID(r) != "200" {
				t.Fatalf("after the restart: %s %s", r.Op, recID(r))
			}
			mongoRun(t, ctx, c, db, mongowire.Doc("drop", "b"))
			if r := run.take(t, 1)[0]; r.Op != fitsync.OpDDL || r.Namespace != db+".b" {
				t.Fatalf("the drop: %s %s %v", r.Op, r.Namespace, r.After)
			}
			run.stop(t)

			// A stream of one collection is invalidated by its drop.
			one := mongoSource(t, mongoSourceParams(t, uri, map[string]any{"database": db, "collections": []string{"a"}, "snapshot": "never"}))
			run = runSource(one, nil, false)
			time.Sleep(500 * time.Millisecond) // the stream opened before the drop
			mongoRun(t, ctx, c, db, mongowire.Doc("drop", "a"))
			for {
				select {
				case r := <-run.recs:
					if r.Op != fitsync.OpDDL {
						t.Fatalf("before the invalidate: %s", r.Op)
					}
					continue
				case err := <-run.done:
					if err == nil || !strings.Contains(err.Error(), "snapshot is needed") {
						t.Fatalf("after the drop: %v", err)
					}
				case <-time.After(2 * time.Minute):
					t.Fatal("no invalidate")
				}
				break
			}
			mongoRun(t, ctx, c, db, mongowire.Doc("dropDatabase", 1))
		})
	}
}

// TestMongoSourceFailover writes documents while the primary steps down:
// the source reads every one, opening its stream again on the new
// primary, or, as the runtime would, restarting from its last position.
func TestMongoSourceFailover(t *testing.T) {
	for name, uri := range mongoSets(t) {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			c := mongoClient(t, uri, func(o *mongowire.Options) { o.ServerSelectionTimeout = 2 * time.Minute })
			db := "fit_src_failover_" + name
			mongoRun(t, ctx, c, db, mongowire.Doc("dropDatabase", 1))
			mongoRun(t, ctx, c, db, mongowire.Doc("create", "c"))
			old, err := c.Primary(ctx)
			if err != nil {
				t.Fatal(err)
			}
			src := mongoSource(t, mongoSourceParams(t, uri, map[string]any{"database": db}))
			const total = 300
			seen := map[string]bool{}
			var last fitsync.Position
			run := runSource(src, nil, false)
			var writing sync.WaitGroup
			writing.Go(func() {
				for i := 0; i < total; i++ {
					for {
						_, err := c.Run(ctx, db, mongowire.Doc("insert", "c", "documents", mongowire.A{mongowire.Doc("_id", i)}, "writeConcern", mongowire.Doc("w", "majority")))
						var ce *mongowire.CommandError
						if err == nil || errors.As(err, &ce) && ce.Code == 11000 {
							break
						}
						if ctx.Err() != nil {
							return
						}
						time.Sleep(100 * time.Millisecond)
					}
					if i == total/3 {
						if _, err := c.Run(ctx, "admin", mongowire.Doc("replSetStepDown", 30, "secondaryCatchUpPeriodSecs", 15)); err != nil && !mongowire.IsNetError(err) {
							t.Errorf("step down: %v", err)
						}
					}
				}
			})
			restarts := 0
			guard := time.After(4 * time.Minute)
			for len(seen) < total {
				select {
				case r := <-run.recs:
					seen[recID(r)] = true
					last = r.Position
				case err := <-run.done:
					t.Logf("the source stopped, restarting from its last position: %v", err)
					restarts++
					run = runSource(src, last, false)
				case <-guard:
					t.Fatalf("%d of %d documents read", len(seen), total)
				}
			}
			writing.Wait()
			run.stop(t)
			now, err := c.Primary(ctx)
			if err != nil || now == old {
				t.Fatalf("primary %s, was %s: %v", now, old, err)
			}
			t.Logf("%d documents read across the failover from %s to %s, %d restarts", total, old, now, restarts)
			mongoRun(t, ctx, c, db, mongowire.Doc("dropDatabase", 1))
		})
	}
}

// TestMongoSourceAcrossKills copies a collection changing under it, by
// inserts, updates, replaces and deletes, some in transactions, into a
// memory store, the pipeline killed at random points and started again
// from its checkpoint, during the snapshot and the stream: the store's
// records, applied in order, end as the collection is.
func TestMongoSourceAcrossKills(t *testing.T) {
	for name, uri := range mongoSets(t) {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
			defer cancel()
			seed := uint64(time.Now().UnixNano())
			t.Logf("seed %d", seed)
			rnd := mrand.New(mrand.NewPCG(seed, 9))
			c := mongoClient(t, uri, nil)
			db := "fit_src_kills_" + name
			mongoRun(t, ctx, c, db, mongowire.Doc("dropDatabase", 1))
			var docs []mongowire.D
			for i := range 2000 {
				docs = append(docs, mongowire.Doc("_id", i, "v", 0, "pad", strings.Repeat("x", i%50)))
			}
			mongoInsert(t, ctx, c, db, "c", docs...)
			store := "mongo-kills-" + name
			fitsync.MemoryStore(store).Reset()
			cfg := mongoPipeline(t, t.TempDir(), store,
				mongoSourceParams(t, uri, map[string]any{"database": db, "batch_size": 1 + rnd.IntN(300), "max_await": "50ms"}),
				map[string]any{"checkpoint_interval": "1ms", "queue": map[string]any{"max_records": 20 + rnd.IntN(200)}})
			if err := fitsync.Check(cfg); err != nil {
				t.Fatal(err)
			}

			const ops = 3000
			var wrote atomic.Int64
			var writing sync.WaitGroup
			writing.Go(func() {
				r := mrand.New(mrand.NewPCG(seed, 10))
				for n := 1; n <= ops; n++ {
					id := r.IntN(2500)
					var cmd mongowire.D
					switch r.IntN(4) {
					case 0:
						cmd = mongowire.Doc("delete", "c", "deletes", mongowire.A{mongowire.Doc("q", mongowire.Doc("_id", id), "limit", 1)})
					case 1:
						cmd = mongowire.Doc("update", "c", "updates", mongowire.A{mongowire.Doc("q", mongowire.Doc("_id", id), "u", mongowire.Doc("_id", id, "v", n, "replaced", true), "upsert", true)})
					default:
						cmd = mongowire.Doc("update", "c", "updates", mongowire.A{mongowire.Doc("q", mongowire.Doc("_id", id), "u", mongowire.Doc("$set", mongowire.Doc("v", n)), "upsert", true)})
					}
					if _, err := c.Run(ctx, db, cmd); err != nil {
						t.Error(err)
						return
					}
					wrote.Store(int64(n))
					time.Sleep(time.Millisecond)
				}
			})

			points := []fitsync.Point{fitsync.CrashEmitted, fitsync.CrashDispatched, fitsync.CrashWritten,
				fitsync.CrashAcknowledged, fitsync.CrashCheckpointWritten, fitsync.CrashCheckpointSaved}
			kills := 0
			for round := 0; wrote.Load() < ops || round < 20; round++ {
				at, nth := points[rnd.IntN(len(points))], int32(1+rnd.IntN(300))
				var hits atomic.Int32
				rt := fitsync.NewRuntime(fitsync.Options{Crash: func(_ string, p fitsync.Point) bool {
					return p == at && hits.Add(1) == nth
				}})
				if err := rt.Apply(cfg); err != nil {
					t.Fatal(err)
				}
				deadline := time.Now().Add(time.Duration(100+rnd.IntN(400)) * time.Millisecond)
				for time.Now().Before(deadline) && redisState(rt) != "killed" {
					time.Sleep(time.Millisecond)
				}
				if redisState(rt) == "killed" {
					kills++
				}
				rt.Close(context.Background())
			}
			writing.Wait()

			want := map[string]string{}
			cur, err := c.Cursor(ctx, db, mongowire.Doc("find", "c"), mongowire.CursorOptions{})
			if err != nil {
				t.Fatal(err)
			}
			for {
				d, err := cur.Next(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if d == nil {
					break
				}
				v, _ := d.Value()
				id, _ := v.Get("_id")
				want[fmt.Sprint(id.AsInt())] = d.String()
			}
			rt := fitsync.NewRuntime(fitsync.Options{})
			if err := rt.Apply(cfg); err != nil {
				t.Fatal(err)
			}
			defer rt.Close(context.Background())
			var got map[string]string
			deadline := time.Now().Add(3 * time.Minute) // a hang guard
			for {
				got = map[string]string{}
				for _, r := range fitsync.MemoryStore(store).Records() {
					switch r.Op {
					case fitsync.OpDelete:
						delete(got, recID(r))
					case fitsync.OpInsert, fitsync.OpUpdate:
						b, _ := fitsync.AppendPlain(nil, r.After)
						got[recID(r)] = string(b)
					}
				}
				if mapsEqual(got, want) {
					break
				}
				if time.Now().After(deadline) {
					var diff []string
					for k, v := range want {
						if got[k] != v {
							diff = append(diff, fmt.Sprintf("%s: %q, want %q", k, got[k], v))
						}
					}
					for k := range got {
						if _, ok := want[k]; !ok {
							diff = append(diff, k+": not in the collection")
						}
					}
					slices.Sort(diff)
					t.Fatalf("%d differences, %d documents: %s", len(diff), len(want), strings.Join(diff[:min(10, len(diff))], "; "))
				}
				time.Sleep(20 * time.Millisecond)
			}
			t.Logf("%d documents, %d changes, %d records kept across %d kills", len(want), ops, len(fitsync.MemoryStore(store).Records()), kills)
			fitsync.MemoryStore(store).Reset()
			mongoRun(t, ctx, c, db, mongowire.Doc("dropDatabase", 1))
		})
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// TestMongoSourceJob splits a collection into ranges of _id and reads them:
// every document once.
func TestMongoSourceJob(t *testing.T) {
	for name, uri := range mongoSets(t) {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			c := mongoClient(t, uri, nil)
			db := "fit_src_job_" + name
			mongoRun(t, ctx, c, db, mongowire.Doc("dropDatabase", 1))
			var docs []mongowire.D
			const n = 5000
			for i := range n {
				docs = append(docs, mongowire.Doc("_id", fmt.Sprintf("k%05d", i)))
			}
			mongoInsert(t, ctx, c, db, "c", docs...)
			mongoInsert(t, ctx, c, db, "d", mongowire.Doc("_id", 1))
			src := mongoSource(t, mongoSourceParams(t, uri, map[string]any{"database": db, "chunk_documents": 700}))
			plan, err := src.(fitsync.Chunker).Chunks(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(plan) < 4 || plan[len(plan)-1].Namespace != db+".d" {
				t.Fatalf("the plan %+v", plan)
			}
			seen := map[string]int{}
			for _, ch := range plan {
				err := src.(fitsync.Chunker).ReadChunk(ctx, ch, nil, func(r *fitsync.Record) error {
					seen[r.Namespace+"/"+recID(r)]++
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			if len(seen) != n+1 {
				t.Fatalf("%d documents read, want %d", len(seen), n+1)
			}
			for k, v := range seen {
				if v != 1 {
					t.Fatalf("%s read %d times", k, v)
				}
			}
			t.Logf("%d chunks", len(plan))
			mongoRun(t, ctx, c, db, mongowire.Doc("dropDatabase", 1))
		})
	}
}
