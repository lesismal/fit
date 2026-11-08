package syncelastic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
)

// fakeSearch is a cluster of one index whose documents have a cursor n,
// searched as the source searches: points in time frozen when opened,
// sorted by _shard_doc (Elasticsearch) or _id (OpenSearch), sliced by a
// hash of the _id; cursor searches sorted by n, equal values in an order
// of their own.
type fakeSearch struct {
	mu         sync.Mutex
	opensearch bool
	version    string
	docs       []*fdoc
	pits       map[string][]*fdoc
	scrolls    map[string]*fscroll
	pitN       int
	open       int
	closed     int
	searches   int
	sizes      []int
	// fail answers a request with a status instead, when not 0.
	fail func(r *http.Request) int
	// shardFailed answers searches with a shard failed.
	shardFailed bool
	// expire makes every point in time gone.
	expire bool
	// onSearch is called, under the lock, before each search is answered.
	onSearch func(n int)
	srv      *httptest.Server
}

type fdoc struct {
	id      string
	version int64
	n       int64
	extra   string
}

func newSearch(t *testing.T, opensearch bool) *fakeSearch {
	f := &fakeSearch{opensearch: opensearch, version: "8.19.3", pits: map[string][]*fdoc{}, scrolls: map[string]*fscroll{}}
	if opensearch {
		f.version = "2.19.3"
	}
	f.srv = httptest.NewServer(f)
	t.Cleanup(f.srv.Close)
	return f
}

// put writes a document: a new one, or a new version of one there.
func (f *fakeSearch) put(id string, n int64, extra string) {
	for _, d := range f.docs {
		if d.id == id {
			d.version++
			d.n, d.extra = n, extra
			return
		}
	}
	f.docs = append(f.docs, &fdoc{id: id, version: 1, n: n, extra: extra})
}

func (f *fakeSearch) Put(id string, n int64, extra string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.put(id, n, extra)
}

func (d *fdoc) source() string {
	if d.extra != "" {
		return fmt.Sprintf(`{"n":%d,"v":"%s-%d",%s}`, d.n, d.id, d.version, d.extra)
	}
	return fmt.Sprintf(`{"n":%d,"v":"%s-%d"}`, d.n, d.id, d.version)
}

func (f *fakeSearch) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		if st := f.fail(r); st != 0 {
			http.Error(w, `{"error": "made to fail"}`, st)
			return
		}
	}
	p := r.URL.Path
	switch {
	case p == "/":
		dist := ""
		if f.opensearch {
			dist = `, "distribution": "opensearch"`
		}
		fmt.Fprintf(w, `{"version": {"number": %q%s}}`, f.version, dist)
	case strings.HasSuffix(p, "/_mapping/field/n"):
		fmt.Fprint(w, `{"idx": {"mappings": {"n": {"full_name": "n", "mapping": {"n": {"type": "long"}}}}}}`)
	case strings.HasSuffix(p, "/_count"):
		fmt.Fprintf(w, `{"count": %d}`, len(f.docs))
	case r.Method == http.MethodPost && (p == "/idx/_pit" && !f.opensearch || p == "/idx/_search/point_in_time" && f.opensearch):
		if r.URL.Query().Get("keep_alive") == "" {
			http.Error(w, "no keep_alive", 400)
			return
		}
		f.pitN++
		f.open++
		id := fmt.Sprintf("pit-%d", f.pitN)
		var frozen []*fdoc
		for _, d := range f.docs {
			c := *d
			frozen = append(frozen, &c)
		}
		f.pits[id] = frozen
		if f.opensearch {
			fmt.Fprintf(w, `{"pit_id": %q}`, id)
		} else {
			fmt.Fprintf(w, `{"id": %q}`, id)
		}
	case r.Method == http.MethodDelete && (p == "/_pit" && !f.opensearch || p == "/_search/point_in_time" && f.opensearch):
		var b struct {
			ID    string   `json:"id"`
			PitID []string `json:"pit_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		for _, id := range append(b.PitID, b.ID) {
			if _, ok := f.pits[id]; ok {
				delete(f.pits, id)
				f.closed++
			}
		}
		fmt.Fprint(w, `{"succeeded": true}`)
	case r.Method == http.MethodPost && p == "/idx/_search" && r.URL.Query().Get("scroll") != "":
		f.scrollStart(w, r)
	case p == "/_search/scroll":
		f.scrollNext(w, r)
	case r.Method == http.MethodPost && (p == "/_search" || p == "/idx/_search"):
		f.search(w, r)
	default:
		http.Error(w, "no route "+r.Method+" "+p, 400)
	}
}

type fakeQuery struct {
	Size        int                          `json:"size"`
	Pit         *struct{ ID string }         `json:"pit"`
	Sort        []map[string]json.RawMessage `json:"sort"`
	SearchAfter []json.RawMessage            `json:"search_after"`
	Slice       *struct{ ID, Max int }       `json:"slice"`
	Query       *struct {
		Bool struct {
			Filter  []map[string]map[string]json.RawMessage `json:"filter"`
			MustNot []struct {
				IDs struct{ Values []string } `json:"ids"`
			} `json:"must_not"`
		} `json:"bool"`
	} `json:"query"`
}

func (f *fakeSearch) search(w http.ResponseWriter, r *http.Request) {
	f.searches++
	if f.onSearch != nil {
		f.onSearch(f.searches)
	}
	var q fakeQuery
	if err := json.NewDecoder(r.Body).Decode(&q); err != nil || q.Size <= 0 || len(q.Sort) != 1 {
		http.Error(w, fmt.Sprintf("bad search %v", err), 400)
		return
	}
	f.sizes = append(f.sizes, q.Size)
	docs := f.docs
	tie := ""
	if q.Pit != nil {
		frozen, ok := f.pits[q.Pit.ID]
		if !ok || f.expire {
			http.Error(w, `{"error": {"type": "search_context_missing_exception"}, "status": 404}`, 404)
			return
		}
		docs, tie = frozen, "_shard_doc"
		if f.opensearch {
			tie = "_id"
		}
		if _, ok := q.Sort[0][tie]; !ok {
			http.Error(w, "the point in time's sort is not "+tie, 400)
			return
		}
	}
	type res struct {
		d    *fdoc
		sort any
	}
	var out []res
	for i, d := range docs {
		if q.Slice != nil {
			h := fnv.New32a()
			h.Write([]byte(d.id))
			if int(h.Sum32())%q.Slice.Max != q.Slice.ID {
				continue
			}
		}
		if q.Query != nil && !match(d, q.Query.Bool.Filter, q.Query.Bool.MustNot) {
			continue
		}
		var sv any = d.n
		switch tie {
		case "_shard_doc":
			sv = int64(i)
		case "_id":
			sv = d.id
		}
		out = append(out, res{d, sv})
	}
	desc := strings.Contains(string(q.Sort[0]["n"]), "desc")
	slices.SortStableFunc(out, func(a, b res) int {
		switch tie {
		case "_shard_doc":
			return int(a.sort.(int64) - b.sort.(int64))
		case "_id":
			return strings.Compare(a.d.id, b.d.id)
		}
		c := int(a.d.n - b.d.n)
		if desc {
			c = -c
		}
		if c == 0 { // equal values in an order of their own
			return strings.Compare(reverse(a.d.id), reverse(b.d.id))
		}
		return c
	})
	if len(q.SearchAfter) == 1 {
		var after any
		var a int64
		if tie == "_id" {
			var s string
			_ = json.Unmarshal(q.SearchAfter[0], &s)
			after = s
		} else {
			_ = json.Unmarshal(q.SearchAfter[0], &a)
			after = a
		}
		out = slices.DeleteFunc(out, func(x res) bool {
			if tie == "_id" {
				return x.sort.(string) <= after.(string)
			}
			return x.sort.(int64) <= after.(int64)
		})
	}
	out = out[:min(len(out), q.Size)]
	var hits []string
	for _, x := range out {
		sv, _ := json.Marshal(x.sort)
		hits = append(hits, fmt.Sprintf(`{"_index": "idx", "_id": %q, "_version": %d, "_source": %s, "sort": [%s]}`, x.d.id, x.d.version, x.d.source(), sv))
	}
	failed := 0
	if f.shardFailed {
		failed = 1
	}
	pit := ""
	if q.Pit != nil {
		pit = fmt.Sprintf(`"pit_id": %q, `, q.Pit.ID)
	}
	fmt.Fprintf(w, `{%s"timed_out": false, "_shards": {"failed": %d}, "hits": {"hits": [%s]}}`, pit, failed, strings.Join(hits, ","))
}

// fscroll is a scroll's documents not read yet.
type fscroll struct {
	docs []*fdoc
	size int
}

func (f *fakeSearch) scrollStart(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Size  int                    `json:"size"`
		Sort  []string               `json:"sort"`
		Slice *struct{ ID, Max int } `json:"slice"`
	}
	if err := json.NewDecoder(r.Body).Decode(&q); err != nil || q.Size <= 0 || len(q.Sort) != 1 || q.Sort[0] != "_doc" {
		http.Error(w, fmt.Sprintf("bad scroll %v", err), 400)
		return
	}
	f.searches++
	sc := &fscroll{size: q.Size}
	for _, d := range f.docs {
		h := fnv.New32a()
		h.Write([]byte(d.id))
		if q.Slice == nil || int(h.Sum32())%q.Slice.Max == q.Slice.ID {
			c := *d
			sc.docs = append(sc.docs, &c)
		}
	}
	f.pitN++
	f.open++
	id := fmt.Sprintf("scroll-%d", f.pitN)
	f.scrolls[id] = sc
	f.scrollPage(w, id, sc)
}

func (f *fakeSearch) scrollNext(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ScrollID any `json:"scroll_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&b)
	if r.Method == http.MethodDelete {
		ids, _ := b.ScrollID.([]any)
		for _, id := range ids {
			if s, _ := id.(string); f.scrolls[s] != nil {
				delete(f.scrolls, s)
				f.closed++
			}
		}
		fmt.Fprint(w, `{"succeeded": true}`)
		return
	}
	f.searches++
	id, _ := b.ScrollID.(string)
	sc, ok := f.scrolls[id]
	if !ok || f.expire {
		http.Error(w, `{"error": {"type": "search_context_missing_exception"}, "status": 404}`, 404)
		return
	}
	f.scrollPage(w, id, sc)
}

func (f *fakeSearch) scrollPage(w http.ResponseWriter, id string, sc *fscroll) {
	n := min(sc.size, len(sc.docs))
	var hits []string
	for _, d := range sc.docs[:n] {
		hits = append(hits, fmt.Sprintf(`{"_index": "idx", "_id": %q, "_version": %d, "_source": %s, "sort": [0]}`, d.id, d.version, d.source()))
	}
	sc.docs = sc.docs[n:]
	fmt.Fprintf(w, `{"_scroll_id": %q, "timed_out": false, "_shards": {"failed": 0}, "hits": {"hits": [%s]}}`, id, strings.Join(hits, ","))
}

func reverse(s string) string {
	b := []byte(s)
	slices.Reverse(b)
	return string(b)
}

func match(d *fdoc, filter []map[string]map[string]json.RawMessage, mustNot []struct {
	IDs struct{ Values []string } `json:"ids"`
}) bool {
	for _, c := range filter {
		for kind, body := range c {
			if kind != "range" {
				continue
			}
			var ops map[string]json.RawMessage
			_ = json.Unmarshal(body["n"], &ops)
			for op, raw := range ops {
				v, err := strconv.ParseInt(string(raw), 10, 64)
				if err != nil {
					continue // now-...: every document is old enough
				}
				switch {
				case op == "gte" && d.n < v, op == "gt" && d.n <= v, op == "lte" && d.n > v:
					return false
				}
			}
		}
	}
	for _, m := range mustNot {
		if slices.Contains(m.IDs.Values, d.id) {
			return false
		}
	}
	return true
}

func loadSource(t *testing.T, params string) *Source {
	t.Helper()
	s, err := loadSourceErr(params)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { module.Cleanup(s) })
	return s.(*Source)
}

func loadSourceErr(params string) (any, error) {
	src := fmt.Sprintf(`{"sync": {"state_dir": "x", "pipelines": [{"name": "p", "source": {"module": "elasticsearch", "params": %s}, "sinks": [{"name": "s", "sink": {"module": "memory"}}]}]}}`, params)
	c, err := config.Load([]byte(src))
	if err != nil {
		return nil, err
	}
	return module.Load(fitsync.Sources, c.Sync.Pipelines[0].Source, nil)
}

// got is what a run emitted.
type got struct {
	recs []*fitsync.Record
	// last is the last position emitted.
	last fitsync.Position
}

func (g *got) ids() []string {
	var ids []string
	for _, r := range g.recs {
		ids = append(ids, r.Key.Fields()[0].Value.Text())
	}
	return ids
}

// run runs s once from from, stopping it with an error after stop records
// when stop is past 0.
func run(s *Source, from fitsync.Position, stop int) (*got, error) {
	g := &got{}
	errStop := errors.New("stopped")
	err := s.Run(context.Background(), &fitsync.Run{From: from, Once: true, Emit: func(r *fitsync.Record) error {
		if stop > 0 && len(g.recs) == stop {
			return errStop
		}
		g.recs = append(g.recs, r)
		if r.Position != nil {
			g.last = r.Position
		}
		return nil
	}})
	if errors.Is(err, errStop) {
		err = nil
	}
	return g, err
}

func TestSourceSnapshot(t *testing.T) {
	for _, os := range []bool{false, true} {
		t.Run(fmt.Sprint("opensearch=", os), func(t *testing.T) {
			f := newSearch(t, os)
			for i := range 2500 {
				f.Put(fmt.Sprintf("d%04d", 2499-i), int64(i), "")
			}
			s := loadSource(t, fmt.Sprintf(`{"urls": [%q], "index": "idx", "page_size": 100}`, f.srv.URL))
			g, err := run(s, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			ids := g.ids()
			want := make([]string, 2500)
			for i := range want {
				if os { // by _id
					want[i] = fmt.Sprintf("d%04d", i)
				} else { // in the order written
					want[i] = fmt.Sprintf("d%04d", 2499-i)
				}
			}
			if !slices.Equal(ids, want) {
				t.Fatalf("%d documents, not in order: %v...", len(ids), ids[:min(5, len(ids))])
			}
			r := g.recs[7]
			if r.Op != fitsync.OpInsert || r.Namespace != "idx" || r.Source != "elasticsearch" {
				t.Errorf("record %+v", r)
			}
			if n, _ := r.After.Get("n"); n.Kind() != fitsync.KindInt {
				t.Errorf("n is a %v", n.Kind())
			}
			if string(g.last) != `{"done":true}` || g.recs[len(g.recs)-1].Position == nil || g.recs[0].Position != nil {
				t.Errorf("positions: last %s", g.last)
			}
			if f.open != 1 || f.closed != 1 || f.searches != 26 {
				t.Errorf("%d points in time opened, %d closed, %d searches", f.open, f.closed, f.searches)
			}
			// The snapshot read, a restart reads nothing more.
			if g, err := run(s, g.last, 0); err != nil || len(g.recs) != 0 {
				t.Errorf("%v, %d records after the end", err, len(g.recs))
			}
		})
	}
}

// TestSourcePolls reads a snapshot during which documents change, then
// polls: the changes, several at one cursor value across pages, are read
// after the snapshot, and a restart from the checkpoint reads only what is
// new.
func TestSourcePolls(t *testing.T) {
	f := newSearch(t, false)
	for i := range 50 {
		f.Put(fmt.Sprintf("d%02d", i), int64(i/10), "")
	}
	f.onSearch = func(n int) {
		if n == 3 { // the snapshot's first page
			for i := 40; i < 50; i++ {
				f.put(fmt.Sprintf("d%02d", i), 9, "")
			}
			f.put("new1", 9, "")
		}
	}
	s := loadSource(t, fmt.Sprintf(`{"urls": [%q], "index": "idx", "cursor": "n", "page_size": 3}`, f.srv.URL))
	g, err := run(s, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ids := g.ids()
	if len(ids) < 50+11 {
		t.Fatalf("%d records", len(ids))
	}
	snap, polled := ids[:50], ids[50:]
	if slices.Contains(snap, "new1") {
		t.Error("the snapshot saw a document written after its point in time")
	}
	for _, id := range append([]string{"new1"}, "d40", "d45", "d49") {
		if !slices.Contains(polled, id) {
			t.Errorf("%s was not polled: %v", id, polled)
		}
	}
	seen := map[string]int{}
	for _, r := range g.recs[50:] {
		seen[r.Key.Fields()[0].Value.Text()]++
		id := r.Key.Fields()[0].Value.Text()
		wantOp := fitsync.OpUpdate
		if id == "new1" || id < "d40" {
			wantOp = fitsync.OpInsert
		}
		if r.Op != wantOp {
			t.Errorf("%s polled as %v", id, r.Op)
		}
	}
	for id, n := range seen {
		if n > 1 {
			t.Errorf("%s polled %d times", id, n)
		}
	}
	var pos position
	_ = json.Unmarshal(g.last, &pos)
	if string(pos.Cursor) != "9" || len(pos.IDs) != 11 {
		t.Errorf("position %s", g.last)
	}
	// More at the same value and later, then a restart from the checkpoint.
	f.Put("d00", 9, "")
	f.Put("d01", 10, "")
	f.Put("d02", 10, "")
	g2, err := run(s, g.last, 0)
	if err != nil {
		t.Fatal(err)
	}
	if ids := g2.ids(); !slices.Equal(sorted(ids), []string{"d00", "d01", "d02"}) {
		t.Errorf("after the restart %v", ids)
	}
}

func sorted(s []string) []string { s = slices.Clone(s); slices.Sort(s); return s }

// TestSourceEqualValues reads more documents with one cursor value than a
// position keeps: the rest is read through a point in time, each once.
func TestSourceEqualValues(t *testing.T) {
	old := maxEqualIDs
	maxEqualIDs = 5
	t.Cleanup(func() { maxEqualIDs = old })
	for _, os := range []bool{false, true} {
		t.Run(fmt.Sprint("opensearch=", os), func(t *testing.T) {
			f := newSearch(t, os)
			for i := range 60 {
				f.Put(fmt.Sprintf("d%02d", i), 7, "")
			}
			f.Put("e", 8, "")
			s := loadSource(t, fmt.Sprintf(`{"urls": [%q], "index": "idx", "cursor": "n", "snapshot": "cursor", "page_size": 4}`, f.srv.URL))
			g, err := run(s, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			ids := g.ids()
			if len(ids) != 61 || len(sorted(slices.Compact(sorted(ids)))) != 61 || ids[60] != "e" {
				t.Fatalf("%d records: %v", len(ids), ids)
			}
			if f.open != 1 || f.closed != 1 {
				t.Errorf("%d points in time opened, %d closed", f.open, f.closed)
			}
			if string(g.last) != `{"cursor":8,"ids":["e"]}` {
				t.Errorf("position %s", g.last)
			}
		})
	}
}

// TestSourceResumes stops a run part way, as a crash would, and runs again
// from the last position emitted: every document is read, and only those
// after the position twice.
func TestSourceResumes(t *testing.T) {
	f := newSearch(t, false)
	for i := range 100 {
		f.Put(fmt.Sprintf("d%03d", i), int64(i/4), "")
	}
	s := loadSource(t, fmt.Sprintf(`{"urls": [%q], "index": "idx", "cursor": "n", "snapshot": "cursor", "page_size": 7}`, f.srv.URL))
	seen := map[string]int{}
	var from fitsync.Position
	for {
		g, err := run(s, from, 23)
		if err != nil {
			t.Fatal(err)
		}
		// What follows the last position is lost, as in a crash.
		n := len(g.recs)
		for n > 0 && g.recs[n-1].Position == nil {
			n--
		}
		for _, r := range g.recs[:n] {
			seen[r.Key.Fields()[0].Value.Text()]++
		}
		if len(g.recs) < 23 {
			break
		}
		from = g.last
	}
	if len(seen) != 100 {
		t.Fatalf("%d documents read", len(seen))
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("%s read %d times up to the positions", id, n)
		}
	}
}

func TestSourcePITGone(t *testing.T) {
	f := newSearch(t, true)
	for i := range 30 {
		f.Put(fmt.Sprintf("d%02d", i), int64(i), "")
	}
	f.onSearch = func(n int) {
		if n == 2 {
			f.expire = true
		}
	}
	s := loadSource(t, fmt.Sprintf(`{"urls": [%q], "index": "idx", "page_size": 10}`, f.srv.URL))
	if _, err := run(s, nil, 0); !errors.Is(err, errPITGone) {
		t.Fatalf("%v", err)
	}
	f.mu.Lock()
	f.expire, f.onSearch = false, nil
	f.mu.Unlock()
	g, err := run(s, nil, 0)
	if err != nil || len(g.recs) != 30 {
		t.Fatalf("%v, %d records", err, len(g.recs))
	}
	if f.open != 2 {
		t.Errorf("%d points in time opened", f.open)
	}
	// A job's scroll gone after its first page fails the chunk, and is
	// cleared.
	err = s.ReadChunk(context.Background(), fitsync.Chunk{Lo: []byte(`{"slice":0,"of":2}`)}, nil, func(*fitsync.Record) error {
		f.mu.Lock()
		f.expire = true
		f.mu.Unlock()
		return nil
	})
	if !errors.Is(err, errPITGone) || len(f.scrolls) != 0 {
		t.Errorf("%v, %d scrolls left", err, len(f.scrolls))
	}
	if err := s.ReadChunk(context.Background(), fitsync.Chunk{Lo: []byte(`{"slice":2,"of":2}`)}, nil, nil); err == nil {
		t.Error("a slice past the chunks was read")
	}
}

func TestSourceErrors(t *testing.T) {
	f := newSearch(t, false)
	f.Put("a", 1, "")
	f.shardFailed = true
	s := loadSource(t, fmt.Sprintf(`{"urls": [%q], "index": "idx"}`, f.srv.URL))
	if _, err := run(s, nil, 0); err == nil || !strings.Contains(err.Error(), "failed on 1 shards") {
		t.Errorf("a shard failed: %v", err)
	}
	f.shardFailed = false
	f.fail = func(r *http.Request) int {
		if r.URL.Path == "/_search" {
			return 400
		}
		return 0
	}
	if _, err := run(s, nil, 0); err == nil || !strings.Contains(err.Error(), "answered 400") {
		t.Errorf("a refused search: %v", err)
	}
	if f.open != f.closed {
		t.Errorf("%d points in time opened, %d closed", f.open, f.closed)
	}
	for _, c := range []struct {
		os      bool
		version string
	}{{false, "7.11.2"}, {true, "2.3.0"}, {true, "1.3.19"}} {
		old := newSearch(t, c.os)
		old.version = c.version
		s := loadSource(t, fmt.Sprintf(`{"urls": [%q], "index": "idx"}`, old.srv.URL))
		if _, err := run(s, nil, 0); err == nil || !strings.Contains(err.Error(), "no point in time") {
			t.Errorf("%s: %v", c.version, err)
		}
	}
	for _, p := range []string{`"index": "a/b"`, `"index": "_all"`, `"index": "i", "snapshot": "cursor"`, `"index": "i", "cursor": "_id"`} {
		if _, err := loadSourceErr(fmt.Sprintf(`{"urls": ["http://x"], %s}`, p)); err == nil {
			t.Errorf("%s loaded", p)
		}
	}
}

// TestSourceBusyAndFailover answers searches 429 three times, behind a
// first URL that answers 503: the source moves on and waits the 429s out.
func TestSourceBusyAndFailover(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	t.Cleanup(down.Close)
	f := newSearch(t, false)
	for i := range 20 {
		f.Put(fmt.Sprintf("d%02d", i), int64(i), "")
	}
	busy := 3
	f.fail = func(r *http.Request) int {
		if r.URL.Path == "/_search" && busy > 0 {
			busy--
			return 429
		}
		return 0
	}
	s := loadSource(t, fmt.Sprintf(`{"urls": [%q, %q], "index": "idx", "page_size": 8}`, down.URL, f.srv.URL))
	g, err := run(s, nil, 0)
	if err != nil || len(g.recs) != 20 || busy != 0 {
		t.Fatalf("%v, %d records, %d 429s left", err, len(g.recs), busy)
	}
}

func TestSourcePageBytes(t *testing.T) {
	f := newSearch(t, false)
	for i := range 200 {
		f.Put(fmt.Sprintf("d%03d", i), int64(i), `"pad":"`+strings.Repeat("x", 100)+`"`)
	}
	s := loadSource(t, fmt.Sprintf(`{"urls": [%q], "index": "idx", "page_size": 64, "max_page_bytes": 2000}`, f.srv.URL))
	g, err := run(s, nil, 0)
	if err != nil || len(g.recs) != 200 {
		t.Fatalf("%v, %d records", err, len(g.recs))
	}
	// 64 documents of about 150 bytes make 9.6KB: the size halves to 8,
	// whose 1.2KB stay under the bound, and doubles at the last pages.
	if !slices.Equal(f.sizes[:4], []int{64, 32, 16, 8}) || slices.Max(f.sizes[4:len(f.sizes)-2]) != 8 {
		t.Errorf("sizes asked for %v", f.sizes)
	}
}

func TestSourceDeletedField(t *testing.T) {
	f := newSearch(t, false)
	f.Put("a", 1, `"gone":true`)
	f.Put("b", 2, `"gone":false`)
	f.Put("c", 3, `"gone":null`)
	f.Put("d", 4, `"gone":"2026-11-08"`)
	s := loadSource(t, fmt.Sprintf(`{"urls": [%q], "index": "idx", "deleted_field": "gone"}`, f.srv.URL))
	g, err := run(s, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var ops []string
	for _, r := range g.recs {
		ops = append(ops, r.Op.String())
		if r.Op == fitsync.OpDelete && (!r.After.IsNull() || r.Before.IsNull()) {
			t.Errorf("delete %+v", r)
		}
	}
	if strings.Join(ops, ",") != "delete,insert,insert,delete" {
		t.Errorf("ops %v", ops)
	}
}

func TestSourceCursorBounds(t *testing.T) {
	s := &Source{Cursor: "u", ctype: "date_nanos"}
	b, _ := json.Marshal(s.bound(json.RawMessage("1794096000123456785"), "gte", "lte"))
	if string(b) != `{"range":{"u":{"format":"epoch_millis","gte":"1794096000123.456785","lte":"1794096000123.456785"}}}` {
		t.Errorf("date_nanos %s", b)
	}
	if at := s.cursorTime(json.RawMessage("1794096000123456785")); !at.Equal(time.Unix(0, 1794096000123456785)) {
		t.Errorf("date_nanos at %v", at)
	}
	s.ctype = "date"
	b, _ = json.Marshal(append(s.filters(), s.bound(json.RawMessage("1794096000123"), "gt")))
	if string(b) != `[{"exists":{"field":"u"}},{"range":{"u":{"lte":"now-2s"}}},{"range":{"u":{"format":"epoch_millis","gt":1794096000123}}}]` {
		t.Errorf("date %s", b)
	}
	if at := s.cursorTime(json.RawMessage("1794096000123")); !at.Equal(time.UnixMilli(1794096000123)) {
		t.Errorf("date at %v", at)
	}
	for d, want := range map[time.Duration]int{1500 * time.Millisecond: 2, 0: 1} {
		cd := config.Duration(d)
		s.Delay = &cd
		if f := s.filters(); len(f) != want || want == 2 && !strings.Contains(fmt.Sprint(f[1]), "now-2s") {
			t.Errorf("delay %v: %v", d, f)
		}
	}
	s.ctype = "long"
	b, _ = json.Marshal(append(s.filters(), s.bound(json.RawMessage("7"), "gte")))
	if string(b) != `[{"exists":{"field":"u"}},{"range":{"u":{"gte":7}}}]` || !s.cursorTime(json.RawMessage("7")).IsZero() {
		t.Errorf("long %s", b)
	}
}

// TestSourceChunks splits a job's snapshot into slices and runs it as a
// pipeline: every document is written once.
func TestSourceChunks(t *testing.T) {
	for _, os := range []bool{false, true} {
		t.Run(fmt.Sprint("opensearch=", os), func(t *testing.T) {
			f := newSearch(t, os)
			for i := range 1000 {
				f.Put(fmt.Sprintf("d%04d", i), int64(i), "")
			}
			params := fmt.Sprintf(`{"urls": [%q], "index": "idx", "chunk_documents": 300, "page_size": 50}`, f.srv.URL)
			plan, err := loadSource(t, params).Chunks(context.Background())
			if err != nil || len(plan) != 4 || plan[0].Rows+plan[1].Rows+plan[2].Rows+plan[3].Rows != 1000 || string(plan[3].Lo) != `{"slice":3,"of":4}` {
				t.Fatalf("%v: %+v", err, plan)
			}
			store := fmt.Sprintf("es-chunks-%v", os)
			fitsync.MemoryStore(store).Reset() // kept across -count runs
			src := fmt.Sprintf(`{"sync": {"state_dir": %q, "pipelines": [{"name": "job", "mode": "once", "job": {"max_parallel": 3},
				"source": {"module": "elasticsearch", "params": %s},
				"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}}]}]}}`, t.TempDir(), params, store)
			cfg, err := config.Load([]byte(src))
			if err != nil {
				t.Fatal(err)
			}
			st, err := fitsync.RunPipeline(context.Background(), cfg.Sync, "job", fitsync.Options{})
			if err != nil || st.State != "finished" || st.Job == nil || st.Job.ChunksDone != 4 {
				t.Fatalf("%v: %+v", err, st)
			}
			seen := map[string]bool{}
			for _, r := range fitsync.MemoryStore(store).Records() {
				id := r.Key.Fields()[0].Value.Text()
				if seen[id] {
					t.Errorf("%s written twice", id)
				}
				seen[id] = true
			}
			if len(seen) != 1000 || f.open != 4 || f.closed != 4 || len(f.scrolls) != 0 {
				t.Errorf("%d documents written, %d scrolls opened, %d cleared", len(seen), f.open, f.closed)
			}
			withCursor := loadSource(t, fmt.Sprintf(`{"urls": [%q], "index": "idx", "cursor": "n"}`, f.srv.URL))
			if plan, err := withCursor.Chunks(context.Background()); err != nil || plan != nil {
				t.Errorf("a cursor's job split: %v %v", plan, err)
			}
		})
	}
}
