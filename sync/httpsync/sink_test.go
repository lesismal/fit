package httpsync

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
)

// receiver is an endpoint that keeps what it is sent, and answers as a test
// says.
type receiver struct {
	mu      sync.Mutex
	reqs    []sent
	answer  func(n int) int // the status of the nth request, from 1; 0 takes it
	stall   func(n int) bool
	release chan struct{}
	srv     *httptest.Server
}

type sent struct {
	key, ctype, auth, tenant, method string
	body                             []byte
	status                           int
}

func newReceiver(t *testing.T) *receiver {
	rv := &receiver{release: make(chan struct{})}
	rv.srv = httptest.NewServer(rv)
	t.Cleanup(func() { close(rv.release); rv.srv.Close() })
	return rv
}

func (rv *receiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	rv.mu.Lock()
	n := len(rv.reqs) + 1
	st := 0
	if rv.answer != nil {
		st = rv.answer(n)
	}
	stall := rv.stall != nil && rv.stall(n)
	if st == 0 {
		st = http.StatusOK
	}
	rv.reqs = append(rv.reqs, sent{key: r.Header.Get("Idempotency-Key"), ctype: r.Header.Get("Content-Type"),
		auth: r.Header.Get("Authorization"), tenant: r.Header.Get("X-Tenant"), method: r.Method, body: body, status: st})
	rv.mu.Unlock()
	if stall {
		select {
		case <-rv.release:
		case <-r.Context().Done():
		}
		return
	}
	w.WriteHeader(st)
	fmt.Fprintf(w, "status %d", st)
}

func (rv *receiver) requests() []sent {
	rv.mu.Lock()
	defer rv.mu.Unlock()
	return append([]sent(nil), rv.reqs...)
}

func loadSink(t *testing.T, params string) *Sink {
	t.Helper()
	v, err := loadSinkErr(params)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { module.Cleanup(v) })
	return v.(*Sink)
}

func loadSinkErr(params string) (any, error) {
	src := fmt.Sprintf(`{"sync": {"state_dir": "x", "pipelines": [{"name": "p", "source": {"module": "generate"}, "sinks": [{"name": "s", "sink": {"module": "http", "params": %s}}]}]}}`, params)
	c, err := config.Load([]byte(src))
	if err != nil {
		return nil, err
	}
	return module.Load(fitsync.Sinks, c.Sync.Pipelines[0].Sinks[0].Sink, nil)
}

func recs(n int) []*fitsync.Record {
	var out []*fitsync.Record
	for i := range n {
		out = append(out, &fitsync.Record{Namespace: "t", Op: fitsync.OpInsert,
			After:    fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(int64(i))}),
			Position: fitsync.Position(fmt.Sprintf(`{"n":%d}`, i))})
	}
	return out
}

// TestSinkRetriesKeepKey checks a pipeline's retries, after a 503 and after
// a timeout, send the same body with the same idempotency key, and that
// every record arrives.
func TestSinkRetriesKeepKey(t *testing.T) {
	rv := newReceiver(t)
	rv.answer = func(n int) int {
		if n == 1 {
			return http.StatusServiceUnavailable
		}
		return 0
	}
	rv.stall = func(n int) bool { return n == 2 }
	t.Setenv("HTTPSYNC_TOKEN", "s3cret")
	src := fmt.Sprintf(`{"sync": {"state_dir": %q, "pipelines": [{"name": "p", "mode": "once",
		"retry": {"max_attempts": 10, "backoff": "1ms", "max_backoff": "1ms"},
		"source": {"module": "generate", "params": {"records": 30}},
		"sinks": [{"name": "h", "sink": {"module": "http", "params": {"url": %q, "timeout": "200ms",
			"bearer_token_env": "HTTPSYNC_TOKEN", "headers": {"X-Tenant": "acme"}}},
			"batch": {"max_records": 30, "max_wait": "1h"}}]}]}}`, t.TempDir(), rv.srv.URL+"/ingest")
	c, err := config.Load([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), hang)
	defer cancel()
	st, err := fitsync.RunPipeline(ctx, c.Sync, "p", fitsync.Options{})
	if err != nil || st.State != "finished" {
		t.Fatalf("%v %+v", err, st)
	}
	reqs := rv.requests()
	if len(reqs) != 3 {
		t.Fatalf("%d requests", len(reqs))
	}
	for i, r := range reqs {
		if r.key == "" || r.key != reqs[0].key || string(r.body) != string(reqs[0].body) {
			t.Errorf("request %d: key %q, the first's %q", i, r.key, reqs[0].key)
		}
		if r.auth != "Bearer s3cret" || r.tenant != "acme" || r.ctype != "application/x-ndjson" || r.method != http.MethodPost {
			t.Errorf("request %d: %+v", i, r)
		}
	}
	sc := bufio.NewScanner(strings.NewReader(string(reqs[2].body)))
	n := 0
	for sc.Scan() {
		var row map[string]any
		if err := json.Unmarshal(sc.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		n++
	}
	if n != 30 {
		t.Errorf("%d records", n)
	}
}

// TestSinkSplits checks a batch past max_request_bytes goes in several JSON
// arrays, in order, each with a key of its own, and a request failing past
// the first names its first record.
func TestSinkSplits(t *testing.T) {
	rv := newReceiver(t)
	s := loadSink(t, fmt.Sprintf(`{"url": %q, "format": "json", "form": "record", "max_request_bytes": 400, "method": "PUT"}`, rv.srv.URL))
	batch := recs(10)
	if err := s.Write(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	reqs := rv.requests()
	if len(reqs) < 2 {
		t.Fatalf("%d requests", len(reqs))
	}
	var got []int
	keys := map[string]bool{}
	for _, r := range reqs {
		if len(r.body) > 400 || r.ctype != "application/json" || r.method != http.MethodPut {
			t.Errorf("%d bytes, %+v", len(r.body), r)
		}
		var arr []struct {
			After struct {
				Map struct {
					ID struct {
						Int int `json:"int"`
					} `json:"id"`
				} `json:"map"`
			} `json:"after"`
		}
		if err := json.Unmarshal(r.body, &arr); err != nil {
			t.Fatalf("%v: %s", err, r.body)
		}
		for _, a := range arr {
			got = append(got, a.After.Map.ID.Int)
		}
		keys[r.key] = true
	}
	if fmt.Sprint(got) != "[0 1 2 3 4 5 6 7 8 9]" || len(keys) != len(reqs) {
		t.Errorf("%v, %d keys for %d requests", got, len(keys), len(reqs))
	}

	// The second request fails: those of the first are written.
	first := len(reqs)
	rv.answer = func(n int) int {
		if n == first+2 {
			return http.StatusBadGateway
		}
		return 0
	}
	err := s.Write(context.Background(), batch)
	var be *fitsync.BatchError
	if !errors.As(err, &be) || be.Index == 0 || fitsync.IsPermanent(err) {
		t.Fatalf("%v", err)
	}
	// Written again from that record, the request has the key it had.
	failed := rv.requests()[first+1]
	if err := s.Write(context.Background(), batch[be.Index:]); err != nil {
		t.Fatal(err)
	}
	if again := rv.requests()[first+2]; again.key != failed.key {
		t.Errorf("key %q, then %q", failed.key, again.key)
	}
}

// TestSinkPermanent checks a 4xx fails for good, a 429 and a 408 not, and
// that a record too large fails alone.
func TestSinkPermanent(t *testing.T) {
	rv := newReceiver(t)
	s := loadSink(t, fmt.Sprintf(`{"url": %q, "max_request_bytes": 100}`, rv.srv.URL))
	for st, permanent := range map[int]bool{400: true, 404: true, 301: true, 408: false, 429: false, 500: false} {
		rv.answer = func(int) int { return st }
		err := s.Write(context.Background(), recs(1))
		if err == nil || fitsync.IsPermanent(err) != permanent {
			t.Errorf("%d: %v", st, err)
		}
	}
	rv.answer = nil
	big := recs(3)
	big[1].After = fitsync.Map(fitsync.Field{Name: "v", Value: fitsync.String(strings.Repeat("x", 200))})
	err := s.Write(context.Background(), big)
	var be *fitsync.BatchError
	if !errors.As(err, &be) || be.Index != 1 || !fitsync.IsPermanent(err) {
		t.Fatalf("%v", err)
	}
}

// TestIdempotencyKey checks the key follows the positions and the body.
func TestIdempotencyKey(t *testing.T) {
	a, b := recs(2), recs(2)
	if IdempotencyKey(a, []byte("x")) != IdempotencyKey(b, []byte("x")) {
		t.Error("the same records, two keys")
	}
	b[1].Position = fitsync.Position(`{"n":9}`)
	if IdempotencyKey(a, []byte("x")) == IdempotencyKey(b, []byte("x")) || IdempotencyKey(a, []byte("x")) == IdempotencyKey(a, []byte("y")) {
		t.Error("different records, one key")
	}
	if k := IdempotencyKey(a, nil); len(k) != 32 {
		t.Errorf("%q", k)
	}
}

// TestSinkBasicAuthAndCodec checks basic authentication and a codec's body.
func TestSinkBasicAuthAndCodec(t *testing.T) {
	rv := newReceiver(t)
	t.Setenv("HTTPSYNC_PASSWORD", "pw")
	s := loadSink(t, fmt.Sprintf(`{"url": %q, "username": "fit", "password_env": "HTTPSYNC_PASSWORD",
		"codec": {"module": "csv"}, "content_type": "text/csv", "idempotency_header": "X-Dedup"}`, rv.srv.URL))
	if err := s.Write(context.Background(), recs(2)); err != nil {
		t.Fatal(err)
	}
	r := rv.requests()[0]
	if r.auth != "Basic Zml0OnB3" || r.ctype != "text/csv" || string(r.body) != "0\n1\n" || r.key != "" {
		t.Errorf("%+v %q", r, r.body)
	}
}

// TestSinkProvisionRefuses checks the params the sink refuses.
func TestSinkProvisionRefuses(t *testing.T) {
	t.Setenv("HTTPSYNC_SET", "x")
	for params, want := range map[string]string{
		`{"url": "ftp://h/x"}`: "url",
		`{"url": "http://h", "headers": {"Authorization": "x"}}`:                   "headers",
		`{"url": "http://h", "headers": {"Idempotency-Key": "x"}}`:                 "headers",
		`{"url": "http://h", "headers": {"Bad Name": "x"}}`:                        "headers",
		`{"url": "http://h", "username": "u"}`:                                     "username",
		`{"url": "http://h", "bearer_token_env": "HTTPSYNC_UNSET"}`:                "not set",
		`{"url": "http://h", "bearer_token_env": "HTTPSYNC_SET", "username": "u"}`: "not both",
		`{"url": "http://h", "content_type": "text/csv"}`:                          "codec",
		`{"url": "http://h", "codec": {"module": "csv"}, "format": "json"}`:        "not both",
		`{"url": "http://h", "cert_file": "/x"}`:                                   "together",
		`{"url": "http://h", "idempotency_header": "a b"}`:                         "idempotency_header",
	} {
		if _, err := loadSinkErr(params); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", params, err)
		}
	}
}

// TestSinkStopsAtCancel checks a write returns once its context ends, with
// the request still unanswered.
func TestSinkStopsAtCancel(t *testing.T) {
	rv := newReceiver(t)
	rv.stall = func(int) bool { return true }
	s := loadSink(t, fmt.Sprintf(`{"url": %q}`, rv.srv.URL))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Write(ctx, recs(1)) }()
	for len(rv.requests()) == 0 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("%v", err)
		}
	case <-time.After(hang):
		t.Fatal("the write did not return")
	}
}
