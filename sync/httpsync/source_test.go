package httpsync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	fitsync "github.com/lesismal/fit/sync"
)

// hang bounds every wait of the tests: a guard against a hang, not a
// measure of speed.
const hang = 2 * time.Minute

// startPipeline runs a pipeline named name of an http source with params
// into the memory store named name, and returns its runtime and source.
func startPipeline(t *testing.T, name, params, extra string) (*fitsync.Runtime, *Source) {
	t.Helper()
	fitsync.MemoryStore(name).Reset()
	src := fmt.Sprintf(`{"sync": {"state_dir": %q, "pipelines": [{"name": %q,
		"source": {"module": "http", "params": %s},
		"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}, "batch": {"max_records": 100, "max_wait": "1ms"}}]%s}]}}`,
		t.TempDir(), name, params, name, extra)
	c, err := config.Load([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	rt := fitsync.NewRuntime(fitsync.Options{})
	if err := rt.Apply(c.Sync); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rt.Close(context.Background()) })
	return rt, waitSource(t, name)
}

// waitSource waits for the pipeline's source to run.
func waitSource(t *testing.T, name string) *Source {
	t.Helper()
	deadline := time.Now().Add(hang)
	for {
		if s := Lookup(name); s != nil {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatalf("the source of %s never ran", name)
		}
		time.Sleep(time.Millisecond)
	}
}

// offer offers body and returns the channel its outcome comes on.
func offer(t *testing.T, s *Source, body, ct string) (chan Outcome, error) {
	t.Helper()
	out := make(chan Outcome, 1)
	err := s.Offer([]byte(body), ct, func(o Outcome) { out <- o })
	return out, err
}

func await(t *testing.T, out chan Outcome) Outcome {
	t.Helper()
	select {
	case o := <-out:
		return o
	case <-time.After(hang):
		t.Fatal("no answer")
		return Outcome{}
	}
}

// TestSourceAnswersOnceWritten checks a request is answered 200 only once
// the sink has its records, in JSON, NDJSON and arrays alike.
func TestSourceAnswersOnceWritten(t *testing.T) {
	_, s := startPipeline(t, "src-written", `{"namespace": "events"}`, "")
	store := fitsync.MemoryStore("src-written")
	release := make(chan struct{})
	store.SetHook(func(ctx context.Context, _ []*fitsync.Record) error {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	})
	out, err := offer(t, s, `{"id": 1}`+"\n"+`[{"id": 2}, {"id": 3}]`+"\n", "application/x-ndjson")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case o := <-out:
		t.Fatalf("answered %+v before the sink wrote", o)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if o := await(t, out); o.Status != http.StatusOK || o.Records != 3 {
		t.Fatalf("%+v", o)
	}
	recs := store.Records()
	if len(recs) != 3 {
		t.Fatalf("%d records", len(recs))
	}
	for i, r := range recs {
		id, _ := r.After.Get("id")
		if r.Namespace != "events" || r.Source != "http" || fitsync.TextOf(id) != fmt.Sprint(i+1) || r.CommitTime.IsZero() {
			t.Errorf("record %d: %+v", i, r)
		}
		if r.Tx.ID == "" || r.Tx.First != (i == 0) || r.Tx.Last != (i == 2) || (r.Position != nil) != (i == 2) {
			t.Errorf("record %d: tx %+v position %s", i, r.Tx, r.Position)
		}
	}
	store.SetHook(nil)
	out, _ = offer(t, s, `{"id": 4}`, "application/json; charset=utf-8")
	if o := await(t, out); o.Status != http.StatusOK || o.Records != 1 {
		t.Fatalf("%+v", o)
	}
	out, _ = offer(t, s, ``, "")
	if o := await(t, out); o.Status != http.StatusOK || o.Records != 0 {
		t.Fatalf("an empty body: %+v", o)
	}
}

// TestSourceRefuses checks bad bodies are answered 400 and write nothing,
// and other media types 415.
func TestSourceRefuses(t *testing.T) {
	_, s := startPipeline(t, "src-refuses", `{}`, "")
	for _, body := range []string{`{"id": 1} {"id": `, `[1, 2]`, `"text"`, `{"id": 1}` + "\n" + `nope`} {
		out, err := offer(t, s, body, "application/json")
		if err != nil {
			t.Fatal(err)
		}
		if o := await(t, out); o.Status != http.StatusBadRequest || o.Err == "" {
			t.Errorf("%q: %+v", body, o)
		}
	}
	if _, err := offer(t, s, `a,b`, "text/csv"); !errors.Is(err, ErrMediaType) {
		t.Errorf("text/csv: %v", err)
	}
	if n := len(fitsync.MemoryStore("src-refuses").Records()); n != 0 {
		t.Errorf("%d records written", n)
	}
}

// TestSourceCodec checks a codec reads each line, or the whole body.
func TestSourceCodec(t *testing.T) {
	_, s := startPipeline(t, "src-codec", `{"codec": {"module": "csv", "params": {"columns": ["id", "name"]}}}`, "")
	out, err := offer(t, s, "1,ann\r\n\n2,bob\n", "text/csv")
	if err != nil {
		t.Fatal(err)
	}
	if o := await(t, out); o.Status != http.StatusOK || o.Records != 2 {
		t.Fatalf("%+v", o)
	}
	recs := fitsync.MemoryStore("src-codec").Records()
	if len(recs) != 2 {
		t.Fatalf("%d records", len(recs))
	}
	if v, _ := recs[1].After.Get("name"); fitsync.TextOf(v) != "bob" {
		t.Errorf("%+v", recs[1])
	}

	_, s = startPipeline(t, "src-body", `{"codec": {"module": "line"}, "framing": "body"}`, "")
	out, _ = offer(t, s, "one\ntwo\n", "")
	if o := await(t, out); o.Status != http.StatusOK || o.Records != 1 {
		t.Fatalf("%+v", o)
	}
	if v, _ := fitsync.MemoryStore("src-body").Records()[0].After.Get("line"); fitsync.TextOf(v) != "one\ntwo\n" {
		t.Errorf("%q", fitsync.TextOf(v))
	}
}

// TestSourceRecordForm checks form record keeps a record's operation, key
// and namespace.
func TestSourceRecordForm(t *testing.T) {
	_, s := startPipeline(t, "src-form", `{"form": "record"}`, "")
	out, _ := offer(t, s, `{"namespace": "shop.orders", "op": "delete", "key": {"map": {"id": {"int": 7}}}}`, "")
	if o := await(t, out); o.Status != http.StatusOK {
		t.Fatalf("%+v", o)
	}
	r := fitsync.MemoryStore("src-form").Records()[0]
	if r.Op != fitsync.OpDelete || r.Namespace != "shop.orders" {
		t.Errorf("%+v", r)
	}
}

// TestSourceFull checks that requests past max_pending_requests, or past
// the pending bytes, are refused at once while the sink is stalled, and
// that those taken are answered once it goes on.
func TestSourceFull(t *testing.T) {
	_, s := startPipeline(t, "src-full", `{"max_pending_requests": 3, "max_pending_bytes": 1000}`, "")
	store := fitsync.MemoryStore("src-full")
	release := make(chan struct{})
	store.SetHook(func(ctx context.Context, _ []*fitsync.Record) error {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	})
	var outs []chan Outcome
	for i := range 3 {
		out, err := offer(t, s, fmt.Sprintf(`{"id": %d}`, i), "")
		if err != nil {
			t.Fatal(err)
		}
		outs = append(outs, out)
	}
	if _, err := offer(t, s, `{"id": 9}`, ""); !errors.Is(err, ErrBusy) {
		t.Fatalf("a fourth request: %v", err)
	}
	close(release)
	for _, out := range outs {
		if o := await(t, out); o.Status != http.StatusOK {
			t.Fatalf("%+v", o)
		}
	}
	// Room again, but not for a body past the bytes beside another.
	store.SetHook(nil)
	out, err := offer(t, s, `{"id": 10}`, "")
	if err != nil {
		t.Fatal(err)
	}
	await(t, out)
	if n := len(store.Records()); n != 4 {
		t.Errorf("%d records", n)
	}
}

// TestSourceBytesBound checks a body past max_pending_bytes is refused while
// another waits to be read.
func TestSourceBytesBound(t *testing.T) {
	s := &Source{MaxPendingRequests: 10, MaxPendingBytes: 100, inbox: make(chan *request, 10), running: true}
	if err := s.Offer([]byte(strings.Repeat(" ", 90)), "", func(Outcome) {}); err != nil {
		t.Fatal(err)
	}
	if err := s.Offer([]byte(strings.Repeat(" ", 20)), "", func(Outcome) {}); !errors.Is(err, ErrBusy) {
		t.Fatalf("%v", err)
	}
	if err := s.Offer([]byte(strings.Repeat(" ", 10)), "", func(Outcome) {}); err != nil {
		t.Fatal(err)
	}
}

// TestSourceStopAnswers checks the pipeline's stop answers a request it has
// not read 503, and that no source is found once it stopped.
func TestSourceStopAnswers(t *testing.T) {
	rt, s := startPipeline(t, "src-stop", `{}`, "")
	store := fitsync.MemoryStore("src-stop")
	store.SetHook(func(ctx context.Context, _ []*fitsync.Record) error {
		<-ctx.Done()
		return ctx.Err()
	})
	first, _ := offer(t, s, `{"id": 1}`, "")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	rt.Close(ctx)
	if o := await(t, first); o.Status != http.StatusServiceUnavailable {
		t.Errorf("%+v", o)
	}
	if Lookup("src-stop") != nil {
		t.Error("the source is still found")
	}
	if _, err := offer(t, s, `{"id": 2}`, ""); !errors.Is(err, ErrBusy) {
		t.Errorf("%v", err)
	}
}

// TestSourceSkippedAnswered checks a record on_error skips still lets its
// request be answered 200, as the pipeline's policy has it.
func TestSourceSkippedAnswered(t *testing.T) {
	_, s := startPipeline(t, "src-skip", `{}`, `, "on_error": "skip", "retry": {"max_attempts": 1}`)
	fitsync.MemoryStore("src-skip").SetHook(func(context.Context, []*fitsync.Record) error { return errors.New("down") })
	out, _ := offer(t, s, `{"id": 1}`, "")
	if o := await(t, out); o.Status != http.StatusOK {
		t.Errorf("%+v", o)
	}
}

// TestSourceProvision checks framing needs a codec.
func TestSourceProvision(t *testing.T) {
	src := `{"sync": {"state_dir": "x", "pipelines": [{"name": "p", "source": {"module": "http", "params": {"framing": "body"}}, "sinks": [{"name": "m", "sink": {"module": "memory"}}]}]}}`
	c, err := config.Load([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if err := fitsync.Check(c.Sync); err == nil || !strings.Contains(err.Error(), "framing") {
		t.Errorf("%v", err)
	}
}
