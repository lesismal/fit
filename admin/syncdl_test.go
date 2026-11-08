package admin

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	fitsync "github.com/lesismal/fit/sync"
)

// testSyncDeadLetters drives the dead letters' endpoints through call: a
// record a sink fails is kept, listed, replayed once its cause is fixed,
// and reaches the sink once.
func testSyncDeadLetters(t *testing.T, call syncCaller, cs *Configs) {
	fitsync.Unregister(cs.env.Engine, fitsync.Lookup(cs.env.Engine))
	call.json(t, "GET", "/sync/pipelines/p/dead-letters", "", 404)
	call.json(t, "POST", "/sync/pipelines/p/dead-letters/replay", `{"all": true}`, 404)
	call.json(t, "DELETE", "/sync/pipelines/p/dead-letters?all=1", "", 404)

	store := fitsync.MemoryStore(t.Name() + "-dl")
	t.Cleanup(store.Reset)
	var failing atomic.Bool
	failing.Store(true)
	is3 := func(r *fitsync.Record) bool { v, _ := r.Key.Get("id"); return v.AsInt() == 3 }
	store.SetHook(func(_ context.Context, batch []*fitsync.Record) error {
		for i, r := range batch {
			if failing.Load() && is3(r) {
				return &fitsync.BatchError{Index: i, Err: fitsync.Permanent(errors.New("a bad row"))}
			}
		}
		return nil
	})
	syncFixture(t, cs,
		fmt.Sprintf(`{"name": "p", "on_error": "dead_letter", "source": {"module": "generate", "params": {"records": 10}},
			"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}, "batch": {"max_wait": "1ms"}}],
			"dead_letter": {"name": "dl", "sink": {"module": "dead_letters"}}}`, t.Name()+"-dl"),
		fmt.Sprintf(`{"name": "plain", "source": {"module": "generate", "params": {"records": 1}},
			"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}}]}`, t.Name()+"-plain"))
	t.Cleanup(fitsync.MemoryStore(t.Name() + "-plain").Reset)
	waitSync(t, "the pipeline", func() bool {
		return call.json(t, "GET", "/sync/pipelines/p", "", 200)["state"] == "finished"
	})
	st := call.json(t, "GET", "/sync/pipelines/p", "", 200)
	if dl := st["dead_letters"].(map[string]any); dl["written"] != 1.0 || dl["store"].(map[string]any)["kept"] != 1.0 {
		t.Fatalf("%v", st)
	}
	if s := st["sinks"].([]any)[0].(map[string]any); s["records"] != 9.0 || s["failed"] != 1.0 {
		t.Fatalf("%v", s)
	}
	page := call.json(t, "GET", "/sync/pipelines/p/dead-letters?after=0&limit=10", "", 200)
	letters := page["letters"].([]any)
	if len(letters) != 1 || letters[0].(map[string]any)["at"] != "sink m" {
		t.Fatalf("%v", page)
	}
	id := letters[0].(map[string]any)["id"].(float64)
	call.json(t, "GET", "/sync/pipelines/p/dead-letters?after=x", "", 400)
	call.json(t, "GET", "/sync/pipelines/p/dead-letters?limit=0", "", 400)
	call.json(t, "GET", "/sync/pipelines/plain/dead-letters", "", 404)
	call.json(t, "GET", "/sync/pipelines/none/dead-letters", "", 404)

	call.json(t, "POST", "/sync/pipelines/p/dead-letters/replay", `{}`, 400)
	call.json(t, "POST", "/sync/pipelines/p/dead-letters/replay", `{"all": true, "ids": [1]}`, 400)
	call.json(t, "POST", "/sync/pipelines/p/dead-letters/replay", `{`, 400)
	call.json(t, "POST", "/sync/pipelines/plain/dead-letters/replay", `{"all": true}`, 404)
	r := call.json(t, "POST", "/sync/pipelines/p/dead-letters/replay", `{"all": true}`, 200)
	if len(r["replayed"].([]any)) != 0 || len(r["failed"].([]any)) != 1 {
		t.Fatalf("%v", r)
	}
	failing.Store(false)
	r = call.json(t, "POST", "/sync/pipelines/p/dead-letters/replay", fmt.Sprintf(`{"ids": [%d]}`, int(id)), 200)
	if len(r["replayed"].([]any)) != 1 {
		t.Fatalf("%v", r)
	}
	n := 0
	for _, rec := range store.Records() {
		if is3(rec) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("record 3 written %d times", n)
	}

	call.json(t, "DELETE", "/sync/pipelines/p/dead-letters", "", 400)
	call.json(t, "DELETE", "/sync/pipelines/p/dead-letters?all=1&id=1", "", 400)
	call.json(t, "DELETE", "/sync/pipelines/p/dead-letters?id=x", "", 400)
	call.json(t, "DELETE", "/sync/pipelines/plain/dead-letters?all=1", "", 404)
	if d := call.json(t, "DELETE", "/sync/pipelines/p/dead-letters?id=1&id=2", "", 200); d["dropped"] != 0.0 {
		t.Fatalf("%v", d)
	}
	if d := call.json(t, "DELETE", "/sync/pipelines/p/dead-letters?all=1", "", 200); d["dropped"] != 0.0 {
		t.Fatalf("%v", d)
	}
}
