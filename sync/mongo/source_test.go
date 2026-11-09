package syncmongo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	mongowire "github.com/lesismal/fit/sync/mongo/wire"
)

func init() { retryBackoff = time.Millisecond }

// srcLoad loads the source mongo with params, or returns its errors.
func srcLoad(t *testing.T, params map[string]any) (*Source, error) {
	t.Helper()
	p := map[string]any{"name": "p", "source": map[string]any{"module": "mongo", "params": params},
		"sinks": []any{map[string]any{"name": "s", "sink": map[string]any{"module": "memory"}}}}
	b, _ := json.Marshal(map[string]any{"sync": map[string]any{"state_dir": t.TempDir(), "pipelines": []any{p}}})
	cfg, err := config.Load(b)
	if err != nil {
		return nil, err
	}
	m, err := module.Load(fitsync.Sources, cfg.Sync.Pipelines[0].Source, nil)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { module.Cleanup(m) })
	return m.(*Source), nil
}

// srcOf loads the source for the fake f, with params added.
func srcOf(t *testing.T, f *srcFake, params map[string]any) *Source {
	t.Helper()
	p := map[string]any{"uri": "mongodb://" + f.addr + "/?serverSelectionTimeoutMS=2000", "max_await": "20ms"}
	for k, v := range params {
		p[k] = v
	}
	s, err := srcLoad(t, p)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSourceProvision(t *testing.T) {
	t.Setenv("FIT_TEST_MONGO_PW", "secret")
	for _, c := range []struct {
		params map[string]any
		want   string
	}{
		{map[string]any{}, "uri or hosts"},
		{map[string]any{"uri": "http://h"}, "starts with mongodb://"},
		{map[string]any{"uri": "mongodb://u:p@h"}, "holds a password"},
		{map[string]any{"uri": "mongodb://u@h"}, "needs password_env"},
		{map[string]any{"uri": "mongodb://u@h", "password_env": "1x"}, "not an environment variable"},
		{map[string]any{"uri": "mongodb://u@h", "password_env": "FIT_TEST_MONGO_UNSET"}, "not set"},
		{map[string]any{"uri": "mongodb://h", "password_env": "FIT_TEST_MONGO_PW"}, "needs a user"},
		{map[string]any{"uri": "mongodb://h", "database": "a.b"}, "not a database"},
		{map[string]any{"uri": "mongodb://h", "collections": []string{"c"}}, "needs database"},
		{map[string]any{"uri": "mongodb://h", "database": "d", "collections": []string{"c", "c"}}, "named twice"},
		{map[string]any{"uri": "mongodb://h", "database": "d", "collections": []string{"system.views"}}, "not a collection"},
		{map[string]any{"uri": "mongodb://h", "database": "d", "collections": []string{"a$b"}}, "not a collection"},
		{map[string]any{"uri": "mongodb://h", "max_await": "5s", "timeout": "5s"}, "not below timeout"},
	} {
		_, err := srcLoad(t, c.params)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v, want %q", c.params, err, c.want)
		}
	}
	s, err := srcLoad(t, map[string]any{"uri": "mongodb://u@h1,h2/?replicaSet=rs&appName=x", "password_env": "FIT_TEST_MONGO_PW", "timeout": "3s"})
	if err != nil {
		t.Fatal(err)
	}
	if o := s.client.Options(); o.Password != "secret" || o.Timeout != 3*time.Second || o.AppName != "x" || len(o.Hosts) != 2 {
		t.Errorf("the options %+v", o)
	}
	s, err = srcLoad(t, map[string]any{"uri": "mongodb://h"})
	if err != nil {
		t.Fatal(err)
	}
	if o := s.client.Options(); o.Timeout != DefaultTimeout || o.AppName != "fit-sync" || s.batchSize() != DefaultReadBatch || s.maxAwait() != DefaultMaxAwait {
		t.Errorf("the defaults %+v", o)
	}
	s.BatchSize = MaxReadBatch + 1
	if s.batchSize() != MaxReadBatch {
		t.Errorf("batch size %d", s.batchSize())
	}
	if err := Ping(context.Background(), nil); err == nil {
		t.Error("a ping without a client")
	}
	if fd := (&Source{}).fullDocument(); fd != "updateLookup" {
		t.Errorf("fullDocument %q", fd)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if sleepCtx(ctx, time.Hour) {
		t.Error("a sleep past its context's end")
	}
}

// collect runs s from from, keeping its records, until it returns.
func collect(t *testing.T, s *Source, from fitsync.Position, once bool) ([]*fitsync.Record, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute) // a hang guard
	defer cancel()
	var recs []*fitsync.Record
	err := s.Run(ctx, &fitsync.Run{From: from, Once: once, Emit: func(r *fitsync.Record) error {
		recs = append(recs, r)
		return nil
	}})
	return recs, err
}

func idText(r *fitsync.Record) string {
	id, _ := r.Key.Get("_id")
	b, _ := fitsync.AppendPlain(nil, id)
	return string(b)
}

func summary(recs []*fitsync.Record) string {
	var out []string
	for _, r := range recs {
		s := fmt.Sprintf("%s %s/%s", r.Op, r.Namespace, idText(r))
		if r.Tx.ID != "" {
			s += fmt.Sprintf(" tx first=%v last=%v", r.Tx.First, r.Tx.Last)
		}
		out = append(out, s)
	}
	return strings.Join(out, "; ")
}

var wall = time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)

func ev(op, coll string, id any, full mongowire.D, extra ...any) mongowire.D {
	d := mongowire.Doc("operationType", op, "ns", mongowire.Doc("db", "d", "coll", coll), "documentKey", mongowire.Doc("_id", id))
	if full != nil {
		d = append(d, mongowire.E{Key: "fullDocument", Value: full})
	} else if op != "delete" {
		d = append(d, mongowire.E{Key: "fullDocument", Value: nil})
	}
	return append(d, mongowire.Doc(extra...)...)
}

func seed(f *srcFake) {
	f.set(func() {
		f.colls["d.a"] = []mongowire.D{mongowire.Doc("_id", 1, "v", "a1"), mongowire.Doc("_id", 2, "v", "a2"), mongowire.Doc("_id", "x", "v", "ax")}
		f.colls["d.b"] = []mongowire.D{mongowire.Doc("_id", 1, "v", "b1")}
		f.colls["e.c"] = []mongowire.D{mongowire.Doc("_id", 1)}
	})
}

// TestSourceSnapshotThenStream reads a database's collections, then its
// change stream from the token taken before them: each kind of event, a
// transaction's boundaries, an update passed over, and an invalidate.
func TestSourceSnapshotThenStream(t *testing.T) {
	f := newSrcFake(t)
	seed(f)
	f.emit(ev("insert", "a", 0, mongowire.Doc("_id", 0))) // before the start: not read
	s := srcOf(t, f, map[string]any{"database": "d", "full_document_before_change": "whenAvailable", "batch_size": 2})
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	recs := make(chan *fitsync.Record, 100)
	done := make(chan error, 1)
	go func() {
		done <- s.Run(ctx, &fitsync.Run{Emit: func(r *fitsync.Record) error { recs <- r; return nil }})
	}()
	take := func(n int) []*fitsync.Record {
		var out []*fitsync.Record
		for len(out) < n {
			select {
			case r := <-recs:
				out = append(out, r)
			case err := <-done:
				t.Fatalf("stopped after %s: %v", summary(out), err)
			}
		}
		return out
	}
	snap := take(4)
	if got := summary(snap); got != `insert d.a/1; insert d.a/2; insert d.a/"x"; insert d.b/1` {
		t.Fatalf("the snapshot: %s", got)
	}
	if p := string(snap[1].Position); p != `{"start":{"map":{"_data":"00000001"}},"ns":"d.a","id":{"int":2}}` {
		t.Errorf("a snapshot's position %s", p)
	}
	if p := string(snap[3].Position); p != `{"token":{"map":{"_data":"00000001"}}}` {
		t.Errorf("the snapshot's last position %s", p)
	}
	ts := mongowire.Timestamp{T: 1_700_000_000, I: 1}
	lsid := mongowire.Doc("id", mongowire.Binary{Subtype: 4, Data: []byte{1, 2}})
	f.emit(
		ev("insert", "a", 3, mongowire.Doc("_id", 3, "v", "a3"), "wallTime", wall),
		ev("update", "a", 1, mongowire.Doc("_id", 1, "v", "a1+"), "fullDocumentBeforeChange", mongowire.Doc("_id", 1, "v", "a1"), "clusterTime", ts),
		ev("update", "a", 9, nil), // deleted since: passed over
		ev("replace", "a", 2, mongowire.Doc("_id", 2, "w", 1)),
		ev("delete", "a", 3, nil),
		ev("insert", "a", 4, mongowire.Doc("_id", 4), "lsid", lsid, "txnNumber", int64(7)),
		ev("insert", "b", 5, mongowire.Doc("_id", 5), "lsid", lsid, "txnNumber", int64(7)),
		ev("insert", "b", 6, mongowire.Doc("_id", 6), "lsid", lsid, "txnNumber", int64(8)),
		ev("delete", "b", 6, nil),
		mongowire.Doc("operationType", "rename", "ns", mongowire.Doc("db", "d", "coll", "b"), "to", mongowire.Doc("db", "d", "coll", "b2")),
		mongowire.Doc("operationType", "dropDatabase", "ns", mongowire.Doc("db", "d")),
	)
	ch := take(10)
	want := "insert d.a/3; update d.a/1; update d.a/2; delete d.a/3; insert d.a/4 tx first=true last=false; insert d.b/5 tx first=false last=true; " +
		"insert d.b/6 tx first=true last=true; delete d.b/6; ddl d.b/null; ddl d/null"
	if got := summary(ch); got != want {
		t.Fatalf("the changes:\n%s\nwant\n%s", got, want)
	}
	if !ch[0].CommitTime.Equal(wall) || !ch[1].CommitTime.Equal(time.Unix(1_700_000_000, 0)) {
		t.Errorf("commit times %v %v", ch[0].CommitTime, ch[1].CommitTime)
	}
	if b, _ := ch[1].Before.Get("v"); b.Text() != "a1" {
		t.Errorf("the update's before %v", ch[1].Before)
	}
	if ch[4].Tx.ID != "0102:7" || ch[6].Tx.ID != "0102:8" {
		t.Errorf("transactions %q %q", ch[4].Tx.ID, ch[6].Tx.ID)
	}
	if to, _ := ch[8].After.Get("to"); to.Text() != "d.b2" {
		t.Errorf("the rename %v", ch[8].After)
	}
	if p := string(ch[9].Position); p != `{"token":{"map":{"_data":"00000012"}}}` {
		t.Errorf("the last position %s", p)
	}
	f.emit(mongowire.Doc("operationType", "invalidate"))
	select {
	case err := <-done:
		if !errors.Is(err, errInvalidated) {
			t.Fatalf("after an invalidate: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("no end")
	}
	// The command: a database's stream, with its documents.
	var agg mongowire.Raw
	for _, c := range f.commands() {
		if c.Keys()[0] == "aggregate" {
			agg = c
		}
	}
	if got := agg.String(); !strings.Contains(got, `"aggregate":1`) || !strings.Contains(got, `"fullDocument":"updateLookup","fullDocumentBeforeChange":"whenAvailable","resumeAfter":{"_data":"00000001"}`) ||
		!strings.Contains(got, `"$db":"d"`) || strings.Contains(got, "allChangesForCluster") {
		t.Errorf("the change stream's command %s", got)
	}
}

// TestSourceResume starts from checkpoints: a token, a snapshot under way;
// in a job it stops once the stream is caught up.
func TestSourceResume(t *testing.T) {
	f := newSrcFake(t)
	seed(f)
	f.emit(ev("insert", "a", 7, mongowire.Doc("_id", 7)), ev("insert", "a", 8, mongowire.Doc("_id", 8)))
	s := srcOf(t, f, map[string]any{"database": "d", "collections": []string{"a"}})
	recs, err := collect(t, s, fitsync.Position(`{"token":{"map":{"_data":"00000001"}}}`), true)
	if err != nil || summary(recs) != "insert d.a/8" {
		t.Fatalf("from a token: %s, %v", summary(recs), err)
	}
	recs, err = collect(t, s, fitsync.Position(`{"start":{"map":{"_data":"00000000"}},"ns":"d.a","id":{"int":1}}`), true)
	if err != nil || summary(recs) != `insert d.a/2; insert d.a/"x"; insert d.a/7; insert d.a/8` {
		t.Fatalf("from a snapshot: %s, %v", summary(recs), err)
	}
	// A collection of the snapshot before the checkpoint's is passed over.
	s2 := srcOf(t, f, map[string]any{"database": "d", "collections": []string{"a", "b"}})
	recs, err = collect(t, s2, fitsync.Position(`{"start":{"map":{"_data":"00000002"}},"ns":"d.b"}`), true)
	if err != nil || summary(recs) != "insert d.b/1" {
		t.Fatalf("from a snapshot's second collection: %s, %v", summary(recs), err)
	}
	for _, c := range []struct{ from, want string }{
		{`{`, "the checkpoint"},
		{`{}`, "neither a token"},
		{`{"token":5}`, "the checkpoint's token"},
		{`{"token":{"int":5}}`, "not a document"},
		{`{"token":{"map":{"x":{"bad":1}}}}`, "the checkpoint's token"},
		{`{"start":{"map":{}},"ns":"d.a","id":{"bad":1}}`, "the checkpoint's _id"},
		{`{"token":{"map":{"_data":"old"}}}`, "a new snapshot is needed"},
	} {
		if _, err := collect(t, s, fitsync.Position(c.from), true); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("from %s: %v, want %q", c.from, err, c.want)
		}
	}
	// snapshot never: the changes from now, of one collection.
	s3 := srcOf(t, f, map[string]any{"database": "d", "collections": []string{"a"}, "snapshot": "never"})
	recs, err = collect(t, s3, nil, true)
	if err != nil || len(recs) != 0 {
		t.Fatalf("snapshot never: %s, %v", summary(recs), err)
	}
	last := f.commands()
	if got := last[len(last)-3].String(); !strings.Contains(got, `"aggregate":"a"`) || strings.Contains(got, "resumeAfter") {
		t.Errorf("the stream of one collection: %s", got)
	}
}

// TestSourceSavesStart: the stream's start, the snapshot not begun, is
// saved before the first record; a run from it reads the snapshot whole,
// then the stream from that start. A start not saved fails the run.
func TestSourceSavesStart(t *testing.T) {
	f := newSrcFake(t)
	seed(f)
	s := srcOf(t, f, map[string]any{"database": "d", "collections": []string{"a"}})
	stop := errors.New("the sink failed")
	var starts []string
	n := 0
	err := s.Run(context.Background(), &fitsync.Run{
		Start: func(p fitsync.Position) error { starts = append(starts, string(p)); return nil },
		Emit: func(*fitsync.Record) error {
			if len(starts) != 1 {
				t.Error("a record before the start was saved")
			}
			n++
			return stop
		},
	})
	if err != stop || n != 1 || len(starts) != 1 || !strings.HasPrefix(starts[0], `{"start":{"map":{"_data":`) || strings.Contains(starts[0], `"ns"`) {
		t.Fatal(err, n, starts)
	}
	f.emit(ev("insert", "a", 7, mongowire.Doc("_id", 7)))
	recs, err := collect(t, s, fitsync.Position(starts[0]), true)
	if err != nil || summary(recs) != `insert d.a/1; insert d.a/2; insert d.a/"x"; insert d.a/7` {
		t.Fatalf("from the start: %s, %v", summary(recs), err)
	}
	broke := errors.New("the disk is full")
	err = s.Run(context.Background(), &fitsync.Run{Start: func(fitsync.Position) error { return broke },
		Emit: func(*fitsync.Record) error { t.Error("a record"); return nil }})
	if err != broke {
		t.Fatal(err)
	}
}

// TestSourceTargets opens the stream of a deployment and of several
// collections; the deployment's snapshot lists its databases.
func TestSourceTargets(t *testing.T) {
	f := newSrcFake(t)
	seed(f)
	s := srcOf(t, f, nil)
	recs, err := collect(t, s, nil, true)
	if err != nil || summary(recs) != `insert d.a/1; insert d.a/2; insert d.a/"x"; insert d.b/1; insert e.c/1` {
		t.Fatalf("the deployment: %s, %v", summary(recs), err)
	}
	var agg []string
	for _, c := range f.commands() {
		if c.Keys()[0] == "aggregate" {
			agg = append(agg, c.String())
		}
	}
	if !strings.Contains(agg[0], `"allChangesForCluster":true`) || !strings.Contains(agg[0], `"$db":"admin"`) {
		t.Errorf("the deployment's stream %s", agg[0])
	}
	f2 := newSrcFake(t)
	seed(f2)
	f2.emit(ev("insert", "a", 7, mongowire.Doc("_id", 7)))
	s = srcOf(t, f2, map[string]any{"database": "d", "collections": []string{"a", "b"}, "snapshot": "never", "full_document": "whenAvailable"})
	if _, err := collect(t, s, fitsync.Position(`{"token":{"map":{"_data":"00000000"}}}`), true); err != nil {
		t.Fatal(err)
	}
	cmds := f2.commands()
	if got := cmds[0].String(); !strings.Contains(got, `"fullDocument":"whenAvailable"`) || !strings.Contains(got, `{"$match":{"$or":[{"ns.coll":{"$in":["a","b"]}}`) {
		t.Errorf("the collections' stream %s", got)
	}
	// A database without collections: an empty snapshot, then the stream.
	s = srcOf(t, f2, map[string]any{"database": "z"})
	if recs, err := collect(t, s, nil, true); err != nil || len(recs) != 0 {
		t.Errorf("an empty database: %s, %v", summary(recs), err)
	}
}

// TestSourceStreamErrors: a cursor cut is opened again from the last token;
// errors that cannot be are returned.
func TestSourceStreamErrors(t *testing.T) {
	f := newSrcFake(t)
	f.emit(ev("insert", "a", 1, mongowire.Doc("_id", 1)), ev("insert", "a", 2, mongowire.Doc("_id", 2)))
	s := srcOf(t, f, map[string]any{"database": "d", "snapshot": "never", "batch_size": 1})
	cuts := 0
	f.set(func() {
		f.override = func(name string, cmd mongowire.Raw) any {
			if name == "getMore" && cuts < 2 {
				cuts++
				if cuts == 1 {
					return srcClose{}
				}
				return srcErr(43, "cursor not found")
			}
			return nil
		}
	})
	recs, err := collect(t, s, fitsync.Position(`{"token":{"map":{"_data":"00000000"}}}`), true)
	if err != nil || summary(recs) != "insert d.a/1; insert d.a/2" {
		t.Fatalf("across cuts: %s, %v", summary(recs), err)
	}
	// Past the retries, the error is the pipeline's.
	f.set(func() {
		f.override = func(name string, cmd mongowire.Raw) any {
			if name == "getMore" {
				return mongowire.Doc("ok", 0.0, "code", int32(1), "errmsg", "flaky", "errorLabels", mongowire.A{"ResumableChangeStreamError"})
			}
			return nil
		}
	})
	if _, err := collect(t, s, fitsync.Position(`{"token":{"map":{"_data":"00000002"}}}`), true); err == nil || !strings.Contains(err.Error(), "flaky") {
		t.Fatalf("past the retries: %v", err)
	}
	for _, c := range []struct {
		name string
		ans  any
		want string
	}{
		{"getMore", srcErr(2, "bad"), "bad"},
		{"getMore", mongowire.Doc("cursor", mongowire.Doc("id", int64(0), "ns", "d.a", "nextBatch", mongowire.A{
			mongowire.Doc("_id", mongowire.Doc("_data", "00000003"), "operationType", "insert", "ns", mongowire.Doc("db", "d", "coll", "a"), "documentKey", mongowire.Doc("_id", 3), "fullDocument", mongowire.Doc("_id", 3))})), "closed the change stream's cursor"},
		{"getMore", mongowire.Doc("cursor", mongowire.Doc("id", int64(5), "ns", "d.a", "nextBatch", mongowire.A{mongowire.Doc("operationType", "insert")})), "without its resume token"},
		{"getMore", mongowire.Doc("cursor", mongowire.Doc("id", int64(5), "ns", "d.a", "nextBatch", mongowire.A{
			mongowire.Doc("_id", mongowire.Doc("_data", "1"), "operationType", "insert", "ns", mongowire.Doc("db", "d", "coll", "a"))})), "without documentKey._id"},
		{"aggregate", srcErr(280, "fatal"), "a new snapshot is needed"},
	} {
		f.set(func() {
			f.override = func(name string, cmd mongowire.Raw) any {
				if name == c.name {
					return c.ans
				}
				return nil
			}
		})
		if _, err := collect(t, s, fitsync.Position(`{"token":{"map":{"_data":"00000002"}}}`), true); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s answering %v: %v, want %q", c.name, c.ans, err, c.want)
		}
	}
	// The start's token missing, or its command failing.
	s2 := srcOf(t, f, map[string]any{"database": "d"})
	for _, ans := range []any{
		mongowire.Doc("cursor", mongowire.Doc("id", int64(0), "ns", "d.a", "firstBatch", mongowire.A{})),
		srcErr(40573, "only supported on replica sets"),
	} {
		f.set(func() {
			f.override = func(name string, cmd mongowire.Raw) any {
				if name == "aggregate" {
					return ans
				}
				return nil
			}
		})
		if _, err := collect(t, s2, nil, true); err == nil {
			t.Errorf("a start answered %v", ans)
		}
	}
	f.set(func() { f.override = nil })

	// Emit's error, and the context's end, stop it.
	stop := errors.New("stop")
	err = s.Run(context.Background(), &fitsync.Run{From: fitsync.Position(`{"token":{"map":{"_data":"00000000"}}}`), Emit: func(*fitsync.Record) error { return stop }})
	if !errors.Is(err, stop) {
		t.Errorf("emit's error: %v", err)
	}
	f.emit(ev("insert", "a", 3, mongowire.Doc("_id", 3), "lsid", mongowire.Doc("id", mongowire.Binary{Subtype: 4, Data: []byte{1}}), "txnNumber", int64(1)),
		ev("insert", "a", 4, mongowire.Doc("_id", 4), "lsid", mongowire.Doc("id", mongowire.Binary{Subtype: 4, Data: []byte{1}}), "txnNumber", int64(1)))
	n := 0
	err = s.Run(context.Background(), &fitsync.Run{From: fitsync.Position(`{"token":{"map":{"_data":"00000002"}}}`), Once: true, Emit: func(*fitsync.Record) error {
		if n++; n == 1 {
			return stop
		}
		return nil
	}})
	if !errors.Is(err, stop) {
		t.Errorf("emit's error on a transaction's record: %v", err)
	}
	n = 0
	err = s.Run(context.Background(), &fitsync.Run{From: fitsync.Position(`{"token":{"map":{"_data":"00000002"}}}`), Once: true, Emit: func(*fitsync.Record) error {
		if n++; n == 2 {
			return stop
		}
		return nil
	}})
	if !errors.Is(err, stop) {
		t.Errorf("emit's error on a transaction's last record: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	if err := s.Run(ctx, &fitsync.Run{From: fitsync.Position(`{"token":{"map":{"_data":"00000004"}}}`), Emit: func(*fitsync.Record) error { return nil }}); !errors.Is(err, context.Canceled) {
		t.Errorf("a context's end: %v", err)
	}
	// A server gone: the selection's error, not retried past the context.
	f.ln.Close()
	ctx, cancel = context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := s.Run(ctx, &fitsync.Run{From: fitsync.Position(`{"token":{"map":{"_data":"00000004"}}}`), Emit: func(*fitsync.Record) error { return nil }}); err == nil {
		t.Error("no server, no error")
	}
}

// TestSourceSnapshotErrors: a snapshot's cursor cut is opened again after
// its last _id; other errors are returned.
func TestSourceSnapshotErrors(t *testing.T) {
	f := newSrcFake(t)
	f.set(func() {
		for i := range 5 {
			f.colls["d.a"] = append(f.colls["d.a"], mongowire.Doc("_id", i))
		}
	})
	s := srcOf(t, f, map[string]any{"database": "d", "batch_size": 2})
	cut := false
	f.set(func() {
		f.override = func(name string, cmd mongowire.Raw) any {
			if name == "getMore" && !cut {
				cut = true
				return srcErr(43, "cursor not found")
			}
			return nil
		}
	})
	recs, err := collect(t, s, nil, true)
	if err != nil || summary(recs) != "insert d.a/0; insert d.a/1; insert d.a/2; insert d.a/3; insert d.a/4" {
		t.Fatalf("across a cut: %s, %v", summary(recs), err)
	}
	var finds []string
	for _, c := range f.commands() {
		if c.Keys()[0] == "find" {
			finds = append(finds, c.String())
		}
	}
	if len(finds) != 2 || !strings.Contains(finds[1], `"min":{"_id":1}`) || !strings.Contains(finds[0], `"readConcern":{"level":"majority","afterClusterTime":{"$timestamp":{"t":5,"i":7}}}`) {
		t.Errorf("the finds %v", finds)
	}
	for _, name := range []string{"ping", "listCollections", "find", "listDatabases"} {
		f.set(func() {
			f.override = func(n string, cmd mongowire.Raw) any {
				if n == name {
					return srcErr(13, "unauthorized")
				}
				return nil
			}
		})
		s := s
		if name == "listDatabases" {
			s = srcOf(t, f, nil)
		}
		if _, err := collect(t, s, nil, true); err == nil || !strings.Contains(err.Error(), "unauthorized") {
			t.Errorf("%s failing: %v", name, err)
		}
	}
	f.set(func() { f.override = nil })
	stop := errors.New("stop")
	for _, at := range []int{1, 5} {
		n := 0
		err := s.Run(context.Background(), &fitsync.Run{Once: true, Emit: func(*fitsync.Record) error {
			if n++; n == at {
				return stop
			}
			return nil
		}})
		if !errors.Is(err, stop) {
			t.Errorf("emit's error at %d: %v", at, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	err = s.Run(ctx, &fitsync.Run{Once: true, Emit: func(*fitsync.Record) error { cancel(); return nil }})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("a context's end: %v", err)
	}
}

// TestSourceChunks splits a job's snapshot by a sample of _ids and reads the
// chunks, each once, a chunk again from its position.
func TestSourceChunks(t *testing.T) {
	f := newSrcFake(t)
	f.set(func() {
		for i := range 101 {
			f.colls["d.a"] = append(f.colls["d.a"], mongowire.Doc("_id", i))
		}
		f.colls["d.b"] = nil
	})
	s := srcOf(t, f, map[string]any{"database": "d", "chunk_documents": 30})
	ctx := context.Background()
	plan, err := s.Chunks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 5 || plan[4].Namespace != "d.b" || plan[0].Lo != nil || string(plan[0].Hi) != `{"int":25}` || string(plan[3].Lo) != `{"int":75}` || plan[3].Hi != nil || plan[0].Rows != 26 || plan[1].Rows != 25 {
		t.Fatalf("the plan %+v", plan)
	}
	seen := map[string]int{}
	for _, c := range plan {
		err := s.ReadChunk(ctx, c, nil, func(r *fitsync.Record) error {
			seen[idText(r)]++
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 101 {
		t.Fatalf("%d documents read", len(seen))
	}
	var got []string
	err = s.ReadChunk(ctx, plan[1], fitsync.Position(`{"id":{"int":40}}`), func(r *fitsync.Record) error {
		got = append(got, idText(r)+" "+string(r.Position))
		return nil
	})
	if err != nil || len(got) != 9 || got[0] != `41 {"id":{"int":41}}` {
		t.Fatalf("a chunk from its position: %v, %v", got, err)
	}
	// A collection too small to split is one chunk; snapshot never, none.
	s.ChunkDocs = 0
	if plan, err := s.Chunks(ctx); err != nil || len(plan) != 2 || plan[0].Rows != 101 {
		t.Errorf("one chunk: %+v, %v", plan, err)
	}
	s.Snapshot = "never"
	if plan, err := s.Chunks(ctx); err != nil || plan != nil {
		t.Errorf("snapshot never: %+v, %v", plan, err)
	}
	s.Snapshot = ""
	for _, c := range []struct {
		c    fitsync.Chunk
		from string
	}{
		{fitsync.Chunk{Namespace: "d.a", Lo: []byte(`{"bad":1}`)}, ""},
		{fitsync.Chunk{Namespace: "d.a", Hi: []byte(`{"bad":1}`)}, ""},
		{fitsync.Chunk{Namespace: "d.a"}, `{`},
		{fitsync.Chunk{Namespace: "d.a"}, `{"id":{"bad":1}}`},
	} {
		var from fitsync.Position
		if c.from != "" {
			from = fitsync.Position(c.from)
		}
		if err := s.ReadChunk(ctx, c.c, from, func(*fitsync.Record) error { return nil }); err == nil {
			t.Errorf("chunk %+v from %s: no error", c.c, c.from)
		}
	}
	s.ChunkDocs = 30
	for _, name := range []string{"listCollections", "count", "aggregate"} {
		f.set(func() {
			f.override = func(n string, cmd mongowire.Raw) any {
				if n == name {
					return srcErr(13, "unauthorized")
				}
				return nil
			}
		})
		if _, err := s.Chunks(ctx); err == nil || !strings.Contains(err.Error(), "unauthorized") {
			t.Errorf("%s failing: %v", name, err)
		}
	}
	f.set(func() {
		f.override = func(n string, cmd mongowire.Raw) any {
			if n == "aggregate" { // the same _id sampled over and over
				return mongowire.Doc("cursor", mongowire.Doc("id", int64(0), "ns", "d.a", "firstBatch", mongowire.A{
					mongowire.Doc("_id", 5), mongowire.Doc("_id", 5), mongowire.Doc("_id", 5), mongowire.Doc("_id", 5), mongowire.Doc("_id", 5), mongowire.Doc("_id", 5)}))
			}
			return nil
		}
	})
	if plan, err := s.Chunks(ctx); err != nil || len(plan) != 3 || string(plan[1].Lo) != `{"int":5}` {
		t.Errorf("one _id sampled: %+v, %v", plan, err)
	}
}

func TestSourceTest(t *testing.T) {
	f := newSrcFake(t)
	s := srcOf(t, f, nil)
	ctx := context.Background()
	if err := s.Test(ctx); err != nil {
		t.Fatal(err)
	}
	f.set(func() {
		f.hello = mongowire.Doc("isWritablePrimary", true, "maxWireVersion", int32(21), "msg", "isdbgrid")
	})
	if err := s.Test(ctx); err != nil {
		t.Fatalf("a mongos: %v", err)
	}
	f.set(func() { f.hello = mongowire.Doc("isWritablePrimary", true, "maxWireVersion", int32(21)) })
	if err := s.Test(ctx); err == nil || !strings.Contains(err.Error(), "standalone") {
		t.Fatalf("a standalone server: %v", err)
	}
	f.set(func() {
		f.override = func(n string, cmd mongowire.Raw) any {
			if n == "ping" {
				return srcErr(13, "unauthorized")
			}
			return nil
		}
	})
	if err := s.Test(ctx); err == nil {
		t.Fatal("a failing ping")
	}
	f.set(func() {
		f.override = func(n string, cmd mongowire.Raw) any {
			if n == "hello" {
				return srcErr(13, "unauthorized")
			}
			return nil
		}
	})
	if err := s.Test(ctx); err == nil {
		t.Fatal("a failing hello")
	}
}
