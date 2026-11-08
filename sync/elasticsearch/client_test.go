package syncelastic

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	fitsync "github.com/lesismal/fit/sync"
)

func TestConnRefuses(t *testing.T) {
	t.Setenv("FIT_TEST_ES_PASSWORD", "pw")
	dir := t.TempDir()
	junk := filepath.Join(dir, "junk.pem")
	if err := os.WriteFile(junk, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ params, want string }{
		{`{"urls": ["http://x"], "username": "a:b", "password_env": "FIT_TEST_ES_PASSWORD"}`, "no ':'"},
		{fmt.Sprintf(`{"urls": ["http://x"], "ca_file": %q}`, junk), "holds no PEM certificate"},
		{fmt.Sprintf(`{"urls": ["http://x"], "cert_file": %q, "key_file": %q}`, junk, junk), "cert_file"},
		{`{"urls": ["http://x"], "time_zone": "Nowhere/Zone"}`, "Nowhere/Zone"},
	} {
		if _, err := loadErr(c.params); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.params, err, c.want)
		}
	}
}

// TestConnRoot reads what the root endpoint answers that is not a
// cluster's.
func TestConnRoot(t *testing.T) {
	status, body := 0, ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("FIT_TEST_ES_KEY", "a2V5")
	s := load(t, fmt.Sprintf(`{"urls": [%q], "api_key_env": "FIT_TEST_ES_KEY"}`, srv.URL))
	long := strings.Repeat("x", 400)
	for _, c := range []struct {
		status     int
		body, want string
	}{
		{401, "no", "answered 401 (an OpenSearch cluster, which has no API keys, answers so to one): no"},
		{500, long, "answered 500: " + long[:300] + "..."},
		{200, "<html>", "is not Elasticsearch's or OpenSearch's: <html>"},
		{200, `{"version": {}}`, "is not Elasticsearch's"},
	} {
		status, body = c.status, c.body
		if _, err := s.conn.Info(context.Background()); err == nil || !strings.HasSuffix(err.Error(), c.want) && !strings.Contains(err.Error(), c.want) {
			t.Errorf("%d %.20s: %v, want %q", c.status, c.body, err, c.want)
		}
	}
}

// TestConnStops ends requests whose context ends, waiting or not yet sent,
// and fails a request every URL fails to answer.
func TestConnStops(t *testing.T) {
	release := make(chan struct{})
	hang := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(hang.Close)
	t.Cleanup(func() { close(release) })
	s := load(t, fmt.Sprintf(`{"urls": [%q], "timeout": "1h"}`, hang.URL))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := s.conn.Do(ctx, http.MethodGet, "/", "", nil, ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("waiting: %v", err)
	}
	if _, err := s.conn.Do(ctx, http.MethodGet, "/", "", nil, ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("ended before: %v", err)
	}
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	s = load(t, fmt.Sprintf(`{"urls": [%q]}`, closed.URL))
	host := strings.TrimPrefix(closed.URL, "http://")
	if _, err := s.conn.Do(context.Background(), http.MethodGet, "/", "", nil, ""); err == nil || !strings.HasPrefix(err.Error(), host+": ") {
		t.Errorf("no URL answers: %v", err)
	}
}

// TestBuildRefuses writes records the sink cannot make an item of: each is
// refused for good, without a request.
func TestBuildRefuses(t *testing.T) {
	f := newFake(t)
	s := load(t, fmt.Sprintf(`{"urls": [%q], "max_request_bytes": 300}`, f.srv.URL))
	routed := load(t, fmt.Sprintf(`{"urls": [%q], "routing": "r"}`, f.srv.URL))
	noKey := rec(fitsync.OpUpdate, 1, "a")
	noKey.Key = fitsync.Null()
	longID := rec(fitsync.OpInsert, 1, "a")
	longID.Key = fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.String(strings.Repeat("i", MaxIDBytes+1))})
	notMap := rec(fitsync.OpInsert, 1, "a")
	notMap.After = fitsync.String("x")
	nan := rec(fitsync.OpInsert, 1, "a")
	nan.After = fitsync.Map(fitsync.Field{Name: "f", Value: fitsync.Float(math.NaN())})
	big := rec(fitsync.OpInsert, 1, strings.Repeat("v", 400))
	for _, c := range []struct {
		s    *Sink
		r    *fitsync.Record
		want string
	}{
		{s, &fitsync.Record{Namespace: "n", Op: fitsync.Op(99)}, "a record of operation"},
		{s, noKey, "has no key to make the _id of"},
		{s, longID, "more than 512"},
		{s, notMap, "not a map to make a document of"},
		{s, nan, "NaN"},
		{s, big, "more than max_request_bytes"},
		{routed, rec(fitsync.OpInsert, 1, "a"), "no r to route by"},
	} {
		err := c.s.Write(context.Background(), []*fitsync.Record{c.r})
		var be *fitsync.BatchError
		if !errors.As(err, &be) || be.Index != 0 || !fitsync.IsPermanent(err) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v, want %q", err, c.want)
		}
	}
	if f.bulks != 0 {
		t.Errorf("%d bulks sent", f.bulks)
	}
	// A delete routed by its key.
	del := rec(fitsync.OpDelete, 1, "a")
	del.Key = fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(1)}, fitsync.Field{Name: "r", Value: fitsync.String("shard7")})
	if err := routed.Write(context.Background(), []*fitsync.Record{del}); err != nil || f.bulks != 1 {
		t.Errorf("a delete routed by its key: %v, %d bulks", err, f.bulks)
	}
}

// TestBulkFails answers bulks the sink cannot use: each fails the batch at
// its first record, to be sent again.
func TestBulkFails(t *testing.T) {
	status, body := 0, ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			fmt.Fprint(w, `{"version": {"number": "8.19.3"}}`)
			return
		}
		if status == 0 { // the connection dropped
			c, _, _ := w.(http.Hijacker).Hijack()
			c.Close()
			return
		}
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	s := load(t, fmt.Sprintf(`{"urls": [%q]}`, srv.URL))
	for _, c := range []struct {
		status     int
		body, want string
	}{
		{413, "too large", "_bulk answered 413: too large: lower max_request_bytes"},
		{200, "nope", "_bulk's answer"},
		{200, `{"errors": false, "items": []}`, "_bulk answered 0 items for 2"},
		{0, "", strings.TrimPrefix(srv.URL, "http://")},
	} {
		status, body = c.status, c.body
		err := s.Write(context.Background(), []*fitsync.Record{rec(fitsync.OpInsert, 1, "a"), rec(fitsync.OpInsert, 2, "b")})
		var be *fitsync.BatchError
		if !errors.As(err, &be) || be.Index != 0 || fitsync.IsPermanent(err) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%d %s: %v, want %q", c.status, c.body, err, c.want)
		}
	}
}

// TestConnTest asks the root endpoint each time, not what Info kept, and
// the source counts its index too.
func TestConnTest(t *testing.T) {
	f := newSearch(t, false)
	s := load(t, fmt.Sprintf(`{"urls": [%q]}`, f.srv.URL))
	src := loadSource(t, fmt.Sprintf(`{"urls": [%q], "index": "idx"}`, f.srv.URL))
	ctx := context.Background()
	if err := s.Test(ctx); err != nil {
		t.Fatalf("sink: %v", err)
	}
	if err := src.Test(ctx); err != nil {
		t.Fatalf("source: %v", err)
	}
	f.mu.Lock()
	f.fail = func(r *http.Request) int {
		if r.URL.Path == "/" {
			return 401
		}
		return 0
	}
	f.mu.Unlock()
	if err := s.Test(ctx); err == nil || !strings.Contains(err.Error(), "answered 401") {
		t.Errorf("sink, refused: %v", err)
	}
	if err := src.Test(ctx); err == nil || !strings.Contains(err.Error(), "answered 401") {
		t.Errorf("source, refused: %v", err)
	}
	for _, c := range []struct {
		status int
		want   string
	}{{404, ""}, {403, "counting idx: 403"}} {
		f.mu.Lock()
		f.fail = func(r *http.Request) int {
			if strings.HasSuffix(r.URL.Path, "/_count") {
				return c.status
			}
			return 0
		}
		f.mu.Unlock()
		err := src.Test(ctx)
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("count %d: %v, want %q", c.status, err, c.want)
		}
	}
}
