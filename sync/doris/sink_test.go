package syncdoris

import (
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
	"github.com/lesismal/fit/sync/internal/fibrest"
)

func init() {
	fibrest.BackoffFirst, fibrest.BackoffMost = time.Millisecond, 2*time.Millisecond
}

// load is a Stream Load a fake backend took.
type load struct {
	path   string
	header http.Header
	body   string
}

// fakeDoris is a frontend that redirects each load to a backend, which
// keeps the loads and refuses a label it loaded already, as Doris does;
// answer, when set, answers a load in place of that.
type fakeDoris struct {
	mu     sync.Mutex
	loads  []load
	labels map[string]bool
	answer func(l load) (int, string)
	fe, be *httptest.Server
}

func newFake(t *testing.T) *fakeDoris {
	f := &fakeDoris{labels: map[string]bool{}}
	f.be = httptest.NewServer(http.HandlerFunc(f.backend))
	f.fe = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/health" {
			_, _ = io.WriteString(w, `{"msg":"success","code":0}`)
			return
		}
		if r.Header.Get("Expect") != "100-continue" {
			_, _ = io.WriteString(w, `{"Status":"Fail","Message":"There is no 100-continue header"}`)
			return
		}
		w.Header().Set("Location", f.be.URL+r.URL.Path)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	t.Cleanup(func() { f.fe.Close(); f.be.Close() })
	return f
}

func (f *fakeDoris) backend(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	l := load{path: r.URL.Path, header: r.Header.Clone(), body: string(b)}
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") == "" {
		w.WriteHeader(401)
		return
	}
	if f.answer != nil {
		if st, out := f.answer(l); st != 0 {
			w.WriteHeader(st)
			_, _ = io.WriteString(w, out)
			return
		}
	}
	label := r.Header.Get("label")
	if label != "" && f.labels[label] {
		_, _ = io.WriteString(w, `{"Status":"Label Already Exists","ExistingJobStatus":"FINISHED","Label":"`+label+`"}`)
		return
	}
	f.labels[label] = true
	f.loads = append(f.loads, l)
	_, _ = io.WriteString(w, `{"Status":"Success","Label":"`+label+`","NumberLoadedRows":1}`)
}

func (f *fakeDoris) got() []load {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]load(nil), f.loads...)
}

func loadMod(t *testing.T, ns string, params map[string]any) (any, error) {
	t.Helper()
	b, _ := json.Marshal(params)
	v, err := config.ParseValue(b, "params")
	if err != nil {
		t.Fatal(err)
	}
	m, err := module.Load(ns, config.ModuleRef{Module: "doris", Params: v}, nil)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { module.Cleanup(m) })
	return m, nil
}

func fakeSink(t *testing.T, f *fakeDoris, params map[string]any) *Sink {
	t.Helper()
	p := map[string]any{"urls": []string{f.fe.URL}, "username": "root"}
	for k, v := range params {
		p[k] = v
	}
	m, err := loadMod(t, fitsync.Sinks, p)
	if err != nil {
		t.Fatal(err)
	}
	return m.(*Sink)
}

// rec is the n-th change: op on id with value v, at position n.
func rec(table string, n int, op fitsync.Op, id int64, v string) *fitsync.Record {
	r := &fitsync.Record{Namespace: table, Op: op, Key: fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(id)}),
		CommitTime: time.Unix(1_700_000_000+int64(n), 0), Position: fitsync.Position(fmt.Sprintf(`{"n":%d}`, n))}
	row := fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(id)}, fitsync.Field{Name: "v", Value: fitsync.String(v)},
		fitsync.Field{Name: "at", Value: fitsync.Time(time.Date(2026, 1, 2, 3, 4, 5, 6000, time.UTC))})
	if op == fitsync.OpDelete {
		r.Before = row
	} else {
		r.After = row
	}
	return r
}

func TestLoadJSON(t *testing.T) {
	f := newFake(t)
	s := fakeSink(t, f, map[string]any{"properties": map[string]any{"strict_mode": "true"}, "time_zone": "Asia/Shanghai"})
	batch := []*fitsync.Record{rec("shop.orders", 1, fitsync.OpInsert, 1, "a"), rec("shop.orders", 2, fitsync.OpDelete, 2, "b"),
		rec("shop.other", 3, fitsync.OpUpdate, 3, `c"`), {Op: fitsync.OpDDL, Namespace: "shop.orders"}}
	if err := s.Write(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	got := f.got()
	if len(got) != 2 {
		t.Fatalf("%+v", got)
	}
	l := got[0]
	if l.path != "/api/shop/orders/_stream_load" || l.header.Get("format") != "json" || l.header.Get("read_json_by_line") != "true" ||
		l.header.Get("columns") != "`id`,`v`,`at`,`__DORIS_DELETE_SIGN__`" || l.header.Get("strict_mode") != "true" ||
		!strings.HasPrefix(l.header.Get("label"), "fit_") || len(l.header.Get("label")) != 44 {
		t.Fatalf("%s %v", l.path, l.header)
	}
	want := `{"id":1,"v":"a","at":"2026-01-02 11:04:05.000006","__DORIS_DELETE_SIGN__":0}` + "\n" +
		`{"id":2,"v":"b","at":"2026-01-02 11:04:05.000006","__DORIS_DELETE_SIGN__":1}` + "\n"
	if l.body != want {
		t.Fatalf("%s\nwant\n%s", l.body, want)
	}
	if got[1].header.Get("columns") != "`id`,`v`,`at`" || !strings.Contains(got[1].body, `"v":"c\""`) {
		t.Fatalf("%v %s", got[1].header, got[1].body)
	}
	// Loaded again: refused as already loaded, and taken as done.
	if err := s.Write(context.Background(), batch); err != nil || len(f.got()) != 2 {
		t.Fatalf("%v, %d loads", err, len(f.got()))
	}
	if err := s.Test(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestLoadStarRocksAndCSV(t *testing.T) {
	f := newFake(t)
	s := fakeSink(t, f, map[string]any{"flavor": "starrocks", "partial_columns": true, "database": "shop", "table": "t_{namespace}", "label_prefix": "p-1"})
	batch := []*fitsync.Record{rec("a", 1, fitsync.OpInsert, 1, "x"), rec("a", 2, fitsync.OpDelete, 1, "x")}
	batch[1].Before = fitsync.Null()
	if err := s.Write(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	l := f.got()[0]
	if l.path != "/api/shop/t_a/_stream_load" || l.header.Get("strip_outer_array") != "true" || l.header.Get("partial_update") != "true" ||
		l.header.Get("columns") != "`id`,`v`,`at`,`__op`" || !strings.HasPrefix(l.header.Get("label"), "p-1_") {
		t.Fatalf("%v", l.header)
	}
	if want := `[{"id":1,"v":"x","at":"2026-01-02 03:04:05.000006","__op":0},{"id":1,"__op":1}]`; l.body != want {
		t.Fatalf("%s\nwant %s", l.body, want)
	}

	c := fakeSink(t, f, map[string]any{"format": "csv", "partial_columns": true})
	row := fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(7)}, fitsync.Field{Name: "s", Value: fitsync.String(`a"b\c,d`)},
		fitsync.Field{Name: "n", Value: fitsync.Null()}, fitsync.Field{Name: "f", Value: fitsync.Float(1.5)}, fitsync.Field{Name: "ok", Value: fitsync.Bool(true)},
		fitsync.Field{Name: "d", Value: fitsync.Date(2026, 3, 4)}, fitsync.Field{Name: "t", Value: fitsync.Time(time.Unix(0, 0))},
		fitsync.Field{Name: "x", Value: fitsync.Bytes([]byte{1})}, fitsync.Field{Name: "j", Value: fitsync.JSON([]byte(`{"a": 1}`))},
		fitsync.Field{Name: "dec", Value: fitsync.Decimal("2.50")})
	r := &fitsync.Record{Namespace: "db.csv", Op: fitsync.OpInsert, After: row, Position: fitsync.Position(`{"n":1}`)}
	del := &fitsync.Record{Namespace: "db.csv", Op: fitsync.OpDelete, Key: fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(8)})}
	if err := c.Write(context.Background(), []*fitsync.Record{r, del}); err != nil {
		t.Fatal(err)
	}
	l = f.got()[1]
	if l.header.Get("format") != "csv" || l.header.Get("enclose") != `"` || l.header.Get("escape") != `\` || l.header.Get("partial_columns") != "true" ||
		l.header.Get("columns") != "`id`,`s`,`n`,`f`,`ok`,`d`,`t`,`x`,`j`,`dec`,`__DORIS_DELETE_SIGN__`" {
		t.Fatalf("%v", l.header)
	}
	want := `7,"a\"b\\c,d",\N,1.5,true,"2026-03-04","1970-01-01 00:00:00","AQ==","{""a"":1}",2.50,0` + "\n" +
		`8,\N,\N,\N,\N,\N,\N,\N,\N,\N,1` + "\n"
	want = strings.Replace(want, `"{""a"":1}"`, `"{\"a\":1}"`, 1)
	if l.body != want {
		t.Fatalf("%s\nwant\n%s", l.body, want)
	}
}

// TestBadRow splits a load the server refuses for its data until it finds
// the row: those before it loaded, it failing for good.
func TestBadRow(t *testing.T) {
	f := newFake(t)
	f.answer = func(l load) (int, string) {
		if strings.Contains(l.body, `"bad"`) {
			return 200, `{"Status":"Fail","Message":"[DATA_QUALITY_ERROR]too many filtered rows","NumberTotalRows":3,"NumberFilteredRows":1,"ErrorURL":"http://be/err"}`
		}
		return 0, ""
	}
	s := fakeSink(t, f, nil)
	var batch []*fitsync.Record
	for i := 1; i <= 9; i++ {
		v := "ok"
		if i == 7 {
			v = "bad"
		}
		batch = append(batch, rec("d.t", i, fitsync.OpInsert, int64(i), v))
	}
	err := s.Write(context.Background(), batch)
	var be *fitsync.BatchError
	if !errors.As(err, &be) || be.Index != 6 || !fitsync.IsPermanent(err) || !strings.Contains(err.Error(), "http://be/err") || !strings.Contains(err.Error(), "1 of 3 rows") {
		t.Fatalf("%v", err)
	}
	n := 0
	for _, l := range f.got() {
		n += strings.Count(l.body, "\n")
	}
	if n != 6 {
		t.Fatalf("%d rows loaded, want 6", n)
	}
	// The bad row first: none loaded.
	if err := s.Write(context.Background(), batch[6:8]); !errors.As(err, &be) || be.Index != 0 {
		t.Fatalf("%v", err)
	}
	// Refused whole, but taken in halves.
	f.answer = func(l load) (int, string) {
		if strings.Count(l.body, "\n") > 2 {
			return 200, `{"Status":"Fail","Message":"Reason: parse json failed"}`
		}
		return 0, ""
	}
	if err := s.Write(context.Background(), batch[:4]); err != nil {
		t.Fatal(err)
	}
}

func TestLoadAnswers(t *testing.T) {
	f := newFake(t)
	s := fakeSink(t, f, nil)
	batch := []*fitsync.Record{rec("d.t", 1, fitsync.OpInsert, 1, "a")}
	var be *fitsync.BatchError
	for _, tc := range []struct {
		status    int
		body      string
		ok        bool
		permanent bool
		want      string
	}{
		{200, `{"Status":"Publish Timeout"}`, true, false, ""},
		{200, `{"Status":"Label Already Exists","ExistingJobStatus":"VISIBLE"}`, true, false, ""},
		{200, `{"Status":"Label Already Exists","ExistingJobStatus":"RUNNING"}`, false, false, "still running"},
		{200, `{"Status":"Fail","Message":"unknown table"}`, false, false, "unknown table"},
		{200, `{"status":"FAILED","msg":"errCode = 2, detailMessage = denied"}`, false, false, "denied"},
		{200, `not json`, false, false, "not Stream Load's"},
		{403, `denied`, false, false, "403"},
		{500, `down`, false, false, "500"},
	} {
		f.answer = func(load) (int, string) { return tc.status, tc.body }
		err := s.Write(context.Background(), batch)
		if tc.ok != (err == nil) || err != nil && (!errors.As(err, &be) || fitsync.IsPermanent(err) != tc.permanent || !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("%d %s: %v", tc.status, tc.body, err)
		}
	}
	f.answer = nil
	// A record without a position has no label: loaded again, it is
	// loaded again.
	nopos := rec("d.t", 1, fitsync.OpInsert, 1, "a")
	nopos.Position = nil
	_ = s.Write(context.Background(), []*fitsync.Record{nopos})
	_ = s.Write(context.Background(), []*fitsync.Record{nopos})
	last := f.got()[len(f.got())-1]
	if last.header.Get("label") != "" {
		t.Fatalf("a label: %v", last.header)
	}
	f.fe.Close()
	if err := s.Test(context.Background()); err == nil {
		t.Fatal("a closed server passed")
	}
}

func TestRecordErrors(t *testing.T) {
	f := newFake(t)
	s := fakeSink(t, f, map[string]any{"max_request_bytes": 200})
	var be *fitsync.BatchError
	for _, tc := range []struct {
		r    *fitsync.Record
		want string
	}{
		{&fitsync.Record{Namespace: "nodb", Op: fitsync.OpInsert, After: fitsync.Map()}, "has no database"},
		{&fitsync.Record{Namespace: "d.", Op: fitsync.OpInsert, After: fitsync.Map()}, "no table's name"},
		{&fitsync.Record{Namespace: "d.t", Op: fitsync.Op(9), After: fitsync.Map()}, "operation"},
		{&fitsync.Record{Namespace: "d.t", Op: fitsync.OpInsert, After: fitsync.Int(1)}, "not a map"},
		{&fitsync.Record{Namespace: "d.t", Op: fitsync.OpInsert, After: fitsync.Map(fitsync.Field{Name: "a,b", Value: fitsync.Int(1)})}, "cannot be named"},
		{&fitsync.Record{Namespace: "d.t", Op: fitsync.OpInsert, After: fitsync.Map(fitsync.Field{Name: "a", Value: fitsync.String(strings.Repeat("x", 300))})}, "max_request_bytes"},
		{&fitsync.Record{Namespace: "d.t", Op: fitsync.OpInsert, After: fitsync.Map(fitsync.Field{Name: "f", Value: fitsync.Float(nan())})}, "not a JSON number"},
	} {
		if err := s.Write(context.Background(), []*fitsync.Record{tc.r}); !errors.As(err, &be) || !fitsync.IsPermanent(err) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: %v, want %q", tc.r, err, tc.want)
		}
	}
	c := fakeSink(t, f, map[string]any{"format": "csv"})
	if err := c.Write(context.Background(), []*fitsync.Record{{Namespace: "d.t", Op: fitsync.OpInsert, After: fitsync.Map(fitsync.Field{Name: "f", Value: fitsync.Float(nan())})}}); !errors.As(err, &be) {
		t.Fatalf("%v", err)
	}
	if err := c.Write(context.Background(), []*fitsync.Record{{Namespace: "d.t", Op: fitsync.OpInsert, After: fitsync.Map(fitsync.Field{Name: "a", Value: fitsync.Array(fitsync.Float(nan()))})}}); !errors.As(err, &be) {
		t.Fatalf("%v", err)
	}
	ign := fakeSink(t, f, map[string]any{"deletes": "ignore"})
	if err := ign.Write(context.Background(), []*fitsync.Record{rec("d.t", 1, fitsync.OpDelete, 1, "x")}); err != nil || len(f.got()) != 0 {
		t.Fatalf("%v %d", err, len(f.got()))
	}
	// Runs are cut at max_request_bytes, each a load.
	small := fakeSink(t, f, map[string]any{"max_request_bytes": 100})
	if err := small.Write(context.Background(), []*fitsync.Record{rec("d.t", 1, fitsync.OpInsert, 1, "x"), rec("d.t", 2, fitsync.OpInsert, 2, "x")}); err != nil || len(f.got()) != 2 {
		t.Fatalf("%v %d", err, len(f.got()))
	}
}

func nan() float64 {
	var z float64
	return z / z
}

func TestSinkProvisionErrors(t *testing.T) {
	for _, tc := range []struct {
		p    map[string]any
		want string
	}{
		{map[string]any{"urls": []string{"http://x"}, "username": "u", "properties": map[string]any{"label": "x"}}, "the sink's to set"},
		{map[string]any{"urls": []string{"http://x"}, "username": "u", "properties": map[string]any{"Authorization": "x"}}, "the connector's to set"},
		{map[string]any{"urls": []string{"http://x"}, "username": "u", "label_prefix": "a b"}, "letters, digits"},
		{map[string]any{"urls": []string{"http://x"}, "username": "u", "table": "{x}"}, "brace"},
		{map[string]any{"urls": []string{"http://x"}, "username": "u", "time_zone": "Nowhere/None"}, "time_zone"},
		{map[string]any{"urls": []string{"http://x"}, "username": ""}, "no username"},
	} {
		if _, err := loadMod(t, fitsync.Sinks, tc.p); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: %v, want %q", tc.p, err, tc.want)
		}
	}
}

func TestLabel(t *testing.T) {
	a := rec("d.t", 1, fitsync.OpInsert, 1, "a")
	b := rec("d.t", 2, fitsync.OpInsert, 2, "b")
	none := &fitsync.Record{}
	l1 := Label("fit", "d", "t", []*fitsync.Record{a, b})
	if l1 != Label("fit", "d", "t", []*fitsync.Record{a, b}) || l1 == Label("fit", "d", "t", []*fitsync.Record{a}) || l1 == Label("fit", "d", "u", []*fitsync.Record{a, b}) {
		t.Fatal("labels")
	}
	if Label("fit", "d", "t", []*fitsync.Record{none}) != "" || Label("fit", "d", "t", []*fitsync.Record{none, a}) == "" || Label("fit", "d", "t", []*fitsync.Record{a, none}) == "" {
		t.Fatal("labels without positions")
	}
	// A record with an origin is labelled by it, at whatever position.
	o1, o2 := *a, *b
	o1.Origin = fitsync.Origin{Stream: "orders", N: [4]uint64{1, 7}}
	o2.Origin = fitsync.Origin{Stream: "orders", N: [4]uint64{2, 3}}
	l2 := Label("fit", "d", "t", []*fitsync.Record{&o1, &o2})
	o1.Position, o2.Position = fitsync.Position(`{"x":1}`), nil
	if l2 == l1 || l2 != Label("fit", "d", "t", []*fitsync.Record{&o1, &o2}) {
		t.Fatal("labels of origins")
	}
}
