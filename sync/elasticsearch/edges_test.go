package syncelastic

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	fibhttp "github.com/lesismal/fib/http"
	fitsync "github.com/lesismal/fit/sync"
)

// dropping serves f, but drops the connection of a request drop picks.
func dropping(t *testing.T, f http.Handler, drop func(r *http.Request) bool) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if drop(r) {
			c, _, _ := w.(http.Hijacker).Hijack()
			c.Close()
			return
		}
		f.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestSourceConnectionDrops: each request the source makes fails the run
// when its connection drops, with the node's error.
func TestSourceConnectionDrops(t *testing.T) {
	f := newSearch(t, false)
	for i := range 5 {
		f.Put(fmt.Sprintf("d%d", i), int64(i), "")
	}
	var dropPath atomic.Value
	dropPath.Store("")
	url := dropping(t, f, func(r *http.Request) bool { return r.URL.Path == dropPath.Load().(string) })
	host := strings.TrimPrefix(url, "http://")

	// The cursor's mapping, at the start.
	dropPath.Store("/idx/_mapping/field/n")
	s := loadSource(t, fmt.Sprintf(`{"urls": [%q], "index": "idx", "cursor": "n", "snapshot": "none"}`, url))
	if _, err := run(s, nil, 0); err == nil || !strings.Contains(err.Error(), host) {
		t.Errorf("mapping: %v", err)
	}
	// The count a job's chunks are planned by.
	dropPath.Store("/idx/_count")
	s = loadSource(t, fmt.Sprintf(`{"urls": [%q], "index": "idx"}`, url))
	if _, err := s.Chunks(context.Background()); err == nil || !strings.Contains(err.Error(), host) {
		t.Errorf("count: %v", err)
	}
	// The point in time a snapshot reads through.
	dropPath.Store("/idx/_pit")
	if _, err := run(s, nil, 0); err == nil || !strings.Contains(err.Error(), host) {
		t.Errorf("point in time: %v", err)
	}
	dropPath.Store("")
	if g, err := run(s, nil, 0); err != nil || len(g.recs) != 5 {
		t.Errorf("after: %v, %d records", err, len(g.recs))
	}
}

// TestSourceMappingLater: while no index maps the cursor, each poll asks
// for the mapping again, and a failure then fails the run. (The client may
// try a dropped request once more, so every ask after the first drops.)
func TestSourceMappingLater(t *testing.T) {
	f := newSearch(t, false)
	var asked atomic.Int32
	url := dropping(t, f, func(r *http.Request) bool {
		return strings.HasSuffix(r.URL.Path, "/_mapping/field/n") && asked.Add(1) >= 2
	})
	f.answer = func(r *http.Request) (int, string) {
		if strings.HasSuffix(r.URL.Path, "/_mapping/field/n") {
			return 404, `{"error": "no such index"}`
		}
		return 0, ""
	}
	s := loadSource(t, fmt.Sprintf(`{"urls": [%q], "index": "idx", "cursor": "n", "snapshot": "none"}`, url))
	if _, err := run(s, nil, 0); err == nil || asked.Load() < 2 || s.ctype != "" {
		t.Fatalf("%v, mapping asked %d times, type %q", err, asked.Load(), s.ctype)
	}
}

// TestSourceHighestWithoutSort: the highest value's search answering a
// document without sort values starts polling from the lowest, as when no
// document has the cursor.
func TestSourceHighestWithoutSort(t *testing.T) {
	f := newSearch(t, false)
	for i := range 4 {
		f.Put(fmt.Sprintf("d%d", i), int64(i), "")
	}
	first := true
	f.answer = func(r *http.Request) (int, string) {
		if r.URL.Path == "/idx/_search" && first {
			first = false
			return 200, `{"_shards": {"failed": 0}, "hits": {"hits": [{"_index": "idx", "_id": "d3", "_source": {}}]}}`
		}
		return 0, ""
	}
	s := loadSource(t, fmt.Sprintf(`{"urls": [%q], "index": "idx", "cursor": "n", "snapshot": "none"}`, f.srv.URL))
	g, err := run(s, nil, 0)
	if err != nil || !slices.Equal(sorted(g.ids()), []string{"d0", "d1", "d2", "d3"}) {
		t.Fatalf("%v, %v", err, g.ids())
	}
}

// TestSourceAnswerPastClientBound: an answer past the client's 64 MiB is
// asked again for half the documents; a scroll's next page, whose size is
// fixed, fails and halves the searches after; a search of one document
// cannot ask for fewer, and fails.
func TestSourceAnswerPastClientBound(t *testing.T) {
	huge := `{"pad": "` + strings.Repeat("x", DefaultMaxResponseBytes+1024) + `"}`
	f := newSearch(t, false)
	for i := range 5 {
		f.Put(fmt.Sprintf("d%d", i), int64(i), "")
	}
	bigAt := ""
	f.answer = func(r *http.Request) (int, string) {
		if r.URL.Path == bigAt {
			bigAt = ""
			return 200, huge
		}
		return 0, ""
	}
	set := func(p string) {
		f.mu.Lock()
		bigAt, f.sizes = p, nil
		f.mu.Unlock()
	}

	// A point in time's search, its size adapting.
	s := loadSource(t, fmt.Sprintf(`{"urls": [%q], "index": "idx", "page_size": 4}`, f.srv.URL))
	set("/_search")
	g, err := run(s, nil, 0)
	if err != nil || len(g.recs) != 5 || len(f.sizes) == 0 || f.sizes[0] != 2 {
		t.Fatalf("%v, %d records, sizes %v", err, len(g.recs), f.sizes)
	}

	// A scroll's next page.
	s = loadSource(t, fmt.Sprintf(`{"urls": [%q], "index": "idx", "page_size": 2}`, f.srv.URL))
	set("/_search/scroll")
	err = s.ReadChunk(context.Background(), fitsync.Chunk{Lo: []byte(`{"slice":0,"of":1}`)}, nil, func(*fitsync.Record) error { return nil })
	if !errors.Is(err, fibhttp.ErrResponseBodyTooLarge) || s.size.Load() != 1 {
		t.Fatalf("%v, size %d", err, s.size.Load())
	}

	// One document at a time already.
	s = loadSource(t, fmt.Sprintf(`{"urls": [%q], "index": "idx", "page_size": 1}`, f.srv.URL))
	set("/_search")
	if _, err := run(s, nil, 0); !errors.Is(err, fibhttp.ErrResponseBodyTooLarge) {
		t.Fatalf("%v", err)
	}
}

// TestSinkFailsAtRefused: a bulk failing whole fails the batch at its
// first record not written, with that record's own error when the cluster
// refused it for good before, else with the bulk's.
func TestSinkFailsAtRefused(t *testing.T) {
	f := newFake(t)
	var failBulk atomic.Bool
	url := dropping(t, f, func(r *http.Request) bool { return r.URL.Path == "/_bulk" && failBulk.Load() })
	s := load(t, fmt.Sprintf(`{"urls": [%q]}`, url))
	// The root endpoint asked first, dropped: the node's error.
	rootURL := dropping(t, f, func(r *http.Request) bool { return r.URL.Path == "/" })
	sr := load(t, fmt.Sprintf(`{"urls": [%q]}`, rootURL))
	if err := sr.Write(context.Background(), []*fitsync.Record{rec(fitsync.OpInsert, 1, "a")}); err == nil || !strings.Contains(err.Error(), strings.TrimPrefix(rootURL, "http://")) {
		t.Fatalf("root: %v", err)
	}
	failBulk.Store(true)
	// An update without a key is refused before anything is sent.
	batch := []*fitsync.Record{{Namespace: "m", Op: fitsync.OpUpdate, After: row(9, "y")}, rec(fitsync.OpInsert, 1, "a")}
	err := s.Write(context.Background(), batch)
	var be *fitsync.BatchError
	if !errors.As(err, &be) || be.Index != 0 || !fitsync.IsPermanent(err) || !strings.Contains(err.Error(), "no key") {
		t.Fatalf("refused first: %v", err)
	}
	// A 503 for the first item, then the bulk sending it again fails: the
	// bulk's error, at it.
	failBulk.Store(false)
	f.item = func(id string, seen int) int {
		if id == "shop.orders/1" && seen == 1 {
			failBulk.Store(true)
			return 503
		}
		return 0
	}
	err = s.Write(context.Background(), []*fitsync.Record{rec(fitsync.OpInsert, 1, "a"), rec(fitsync.OpInsert, 2, "b")})
	if !errors.As(err, &be) || be.Index != 0 || fitsync.IsPermanent(err) || strings.Contains(err.Error(), "503") {
		t.Fatalf("sent again: %v", err)
	}
}

func TestIDTextNotJSON(t *testing.T) {
	// A key JSON cannot say is its text.
	v := fitsync.Map(fitsync.Field{Name: "f", Value: fitsync.Float(math.Inf(1))})
	if got := idText(v); got != fitsync.TextOf(v) || got == "" {
		t.Fatalf("%q", got)
	}
}
