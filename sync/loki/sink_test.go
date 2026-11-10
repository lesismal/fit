package syncloki

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klauspost/compress/s2"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	"github.com/lesismal/fit/sync/internal/fibrest"
	"github.com/lesismal/fit/sync/internal/pb"
)

func init() {
	fibrest.BackoffFirst, fibrest.BackoffMost = time.Millisecond, 2*time.Millisecond
}

// pushed is an entry a fake Loki took.
type pushed struct {
	tenant, stream, line string
	ts                   time.Time
	meta                 map[string]string
}

// fakeLoki is a Loki push API that decodes what it is sent, in either
// encoding; reject, when set, answers a push with a status instead.
type fakeLoki struct {
	mu      sync.Mutex
	entries []pushed
	pushes  int
	types   []string
	reject  func(es []pushed) int
	srv     *httptest.Server
}

func newFakeLoki(t *testing.T) *fakeLoki {
	f := &fakeLoki{}
	f.srv = httptest.NewServer(f)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeLoki) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Scope-OrgID") == "nobody" {
		w.WriteHeader(401)
		return
	}
	if r.URL.Path != pushPath || r.Method != http.MethodPost {
		w.WriteHeader(404)
		return
	}
	body, _ := io.ReadAll(r.Body)
	tenant := r.Header.Get("X-Scope-OrgID")
	var es []pushed
	var err error
	ct := r.Header.Get("Content-Type")
	if ct == "application/json" {
		es, err = decodeJSON(body, tenant)
	} else {
		es, err = decodeProto(body, tenant)
	}
	if err != nil {
		w.WriteHeader(500)
		_, _ = io.WriteString(w, err.Error())
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pushes++
	f.types = append(f.types, ct)
	if f.reject != nil {
		if st := f.reject(es); st != 0 {
			w.WriteHeader(st)
			_, _ = io.WriteString(w, "refused")
			return
		}
	}
	f.entries = append(f.entries, es...)
	w.WriteHeader(204)
}

func decodeProto(body []byte, tenant string) ([]pushed, error) {
	raw, err := s2.Decode(nil, body)
	if err != nil {
		return nil, err
	}
	var out []pushed
	for len(raw) > 0 {
		f, rest, err := pb.Next(raw)
		if err != nil {
			return nil, err
		}
		raw = rest
		var labels string
		for st := f.Bytes; len(st) > 0; {
			g, rest, err := pb.Next(st)
			if err != nil {
				return nil, err
			}
			st = rest
			if g.Num == 1 {
				labels = string(g.Bytes)
				continue
			}
			e := pushed{tenant: tenant, stream: labels, meta: map[string]string{}}
			for in := g.Bytes; len(in) > 0; {
				h, rest, err := pb.Next(in)
				if err != nil {
					return nil, err
				}
				in = rest
				switch h.Num {
				case 1:
					var sec, nsec int64
					for ts := h.Bytes; len(ts) > 0; {
						k, rest, _ := pb.Next(ts)
						ts = rest
						if k.Num == 1 {
							sec = int64(k.Value)
						} else {
							nsec = int64(k.Value)
						}
					}
					e.ts = time.Unix(sec, nsec)
				case 2:
					e.line = string(h.Bytes)
				case 3:
					n, rest, _ := pb.Next(h.Bytes)
					v, _, _ := pb.Next(rest)
					e.meta[string(n.Bytes)] = string(v.Bytes)
				}
			}
			out = append(out, e)
		}
	}
	return out, nil
}

func decodeJSON(body []byte, tenant string) ([]pushed, error) {
	var req struct {
		Streams []struct {
			Stream map[string]string `json:"stream"`
			Values [][]any           `json:"values"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	var out []pushed
	for _, st := range req.Streams {
		var keys []string
		for k := range st.Stream {
			keys = append(keys, k)
		}
		var parts []string
		for _, k := range sortStrings(keys) {
			parts = append(parts, k+`="`+escape(st.Stream[k])+`"`)
		}
		for _, v := range st.Values {
			ns, _ := strconv.ParseInt(v[0].(string), 10, 64)
			e := pushed{tenant: tenant, stream: "{" + strings.Join(parts, ", ") + "}", line: v[1].(string), ts: time.Unix(0, ns), meta: map[string]string{}}
			if len(v) > 2 {
				for k, x := range v[2].(map[string]any) {
					e.meta[k] = x.(string)
				}
			}
			out = append(out, e)
		}
	}
	return out, nil
}

func sortStrings(s []string) []string {
	for i := range s {
		for j := i + 1; j < len(s); j++ {
			if s[j] < s[i] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
	return s
}

func (f *fakeLoki) got() []pushed {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]pushed(nil), f.entries...)
}

func loadSink(t *testing.T, params map[string]any) (*Sink, error) {
	t.Helper()
	b, _ := json.Marshal(params)
	v, err := config.ParseValue(b, "params")
	if err != nil {
		t.Fatal(err)
	}
	m, err := module.Load(fitsync.Sinks, config.ModuleRef{Module: "loki", Params: v}, nil)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { module.Cleanup(m) })
	return m.(*Sink), nil
}

func fakeSink(t *testing.T, f *fakeLoki, params map[string]any) *Sink {
	t.Helper()
	p := map[string]any{"urls": []string{f.srv.URL}}
	for k, v := range params {
		p[k] = v
	}
	s, err := loadSink(t, p)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// line is a log line record of the file source, at second n when n > 0.
func line(ns string, n int, text string) *fitsync.Record {
	r := &fitsync.Record{Source: "file", Namespace: ns, Op: fitsync.OpMessage, Position: fitsync.Position(fmt.Sprintf(`{"n":%d}`, n)),
		After: fitsync.Map(fitsync.Field{Name: "line", Value: fitsync.String(text)},
			fitsync.Field{Name: "level", Value: fitsync.String([]string{"info", "warn"}[n%2])},
			fitsync.Field{Name: "user", Value: fitsync.String("u" + strconv.Itoa(n))})}
	if n > 0 {
		r.CommitTime = time.Unix(1_700_000_000+int64(n), 0)
	}
	return r
}

func TestPushEncodings(t *testing.T) {
	for _, enc := range []string{"protobuf", "json"} {
		t.Run(enc, func(t *testing.T) {
			f := newFakeLoki(t)
			s := fakeSink(t, f, map[string]any{"encoding": enc, "tenant": "team-a",
				"labels":              map[string]any{"job": "app", "level": "{level}", "empty": "{nothing}"},
				"structured_metadata": map[string]any{"user": "{user}", "none": "{nothing}", "lv": "{level}"}})
			batch := []*fitsync.Record{line("/a.log", 2, `first "quoted"`), line("/a.log", 1, "second"), line("/a.log", 3, "third")}
			if err := s.Write(context.Background(), batch); err != nil {
				t.Fatal(err)
			}
			got := f.got()
			if len(got) != 3 {
				t.Fatalf("%+v", got)
			}
			// The info stream's lines in time order, then the warn stream's.
			want := []string{`{job="app", level="info"}|first "quoted"|2|u2`, `{job="app", level="warn"}|second|1|u1`, `{job="app", level="warn"}|third|3|u3`}
			for i, e := range got {
				s := fmt.Sprintf("%s|%s|%d|%s", e.stream, e.line, e.ts.Unix()-1_700_000_000, e.meta["user"])
				if s != want[i] || e.tenant != "team-a" || len(e.meta) != 2 {
					t.Errorf("%d: %s %v, want %s", i, s, e.meta, want[i])
				}
			}
			// The test pushes a stream without entries, in the encoding.
			if err := s.Test(context.Background()); err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.entries) != 3 || f.pushes != 2 || f.types[0] != f.types[1] {
				t.Fatal(len(f.entries), f.pushes, f.types)
			}
		})
	}
}

// TestDefaultsAndTimes pushes lines without times, a nanosecond apart, the
// default labels, the codec json and a timestamp field.
func TestDefaultsAndTimes(t *testing.T) {
	f := newFakeLoki(t)
	s := fakeSink(t, f, nil)
	before := time.Now()
	if err := s.Write(context.Background(), []*fitsync.Record{line("/b.log", 0, "x"), line("/b.log", 0, "x")}); err != nil {
		t.Fatal(err)
	}
	got := f.got()
	if len(got) != 2 || got[0].stream != `{job="fit", namespace="/b.log"}` || !got[1].ts.After(got[0].ts) || got[0].ts.Before(before) || got[0].line != "x" {
		t.Fatalf("%+v", got)
	}
	f2 := newFakeLoki(t)
	s2 := fakeSink(t, f2, map[string]any{"codec": map[string]any{"module": "json"}, "timestamp_field": "at", "tenant_field": "team",
		"labels": map[string]any{"ns": "{$namespace}"}})
	r := &fitsync.Record{Namespace: "n", Op: fitsync.OpInsert, CommitTime: time.Unix(5, 0),
		After: fitsync.Map(fitsync.Field{Name: "at", Value: fitsync.Int(1_700_000_000_123)}, fitsync.Field{Name: "team", Value: fitsync.String("b")})}
	r2 := &fitsync.Record{Namespace: "n", Op: fitsync.OpInsert, CommitTime: time.Unix(6, 0), After: fitsync.Map(fitsync.Field{Name: "at", Value: fitsync.String("no")})}
	if err := s2.Write(context.Background(), []*fitsync.Record{r, r2}); err != nil {
		t.Fatal(err)
	}
	got = f2.got()
	if len(got) != 2 || got[0].ts.UnixMilli() != 1_700_000_000_123 || got[0].tenant != "b" || got[0].line != `{"at":1700000000123,"team":"b"}` ||
		got[1].ts.Unix() != 6 || got[1].tenant != "" {
		t.Fatalf("%+v", got)
	}
	if f2.pushes != 2 {
		t.Fatalf("%d pushes, want one a tenant", f2.pushes)
	}
}

// TestSplitRefused finds the line Loki refuses in a batch: the lines
// before it pushed, it failing for good, the push bounded by
// max_request_bytes.
func TestSplitRefused(t *testing.T) {
	f := newFakeLoki(t)
	f.reject = func(es []pushed) int {
		for _, e := range es {
			if e.line == "bad" {
				return 400
			}
		}
		return 0
	}
	s := fakeSink(t, f, map[string]any{"max_request_bytes": 200})
	var batch []*fitsync.Record
	for i := 1; i <= 12; i++ {
		text := "ok" + strconv.Itoa(i)
		if i == 9 {
			text = "bad"
		}
		batch = append(batch, line("/c.log", i, text))
	}
	err := s.Write(context.Background(), batch)
	var be *fitsync.BatchError
	if !errors.As(err, &be) || be.Index != 8 || !fitsync.IsPermanent(err) {
		t.Fatalf("%v", err)
	}
	if got := f.got(); len(got) != 8 {
		t.Fatalf("%d lines pushed, want 8", len(got))
	}
	// The bad line first: none pushed. A push of more than 3 lines
	// refused: the halves go.
	f.entries = nil
	batch[0].After = fitsync.Map(fitsync.Field{Name: "line", Value: fitsync.String("bad")})
	if err := s.Write(context.Background(), batch[:4]); !errors.As(err, &be) || be.Index != 0 || len(f.got()) != 0 {
		t.Fatalf("%v, %d pushed", err, len(f.got()))
	}
	f.reject = func(es []pushed) int {
		if len(es) > 3 {
			return 413
		}
		return 0
	}
	if err := s.Write(context.Background(), batch[1:5]); err != nil || len(f.got()) != 4 {
		t.Fatalf("%v, %d pushed", err, len(f.got()))
	}
	// A refusal other than for the lines is the write's error.
	f.reject = func([]pushed) int { return 401 }
	if err := s.Write(context.Background(), batch[1:2]); !errors.As(err, &be) || fitsync.IsPermanent(err) || !strings.Contains(err.Error(), "401") {
		t.Fatalf("%v", err)
	}
	// A 5xx that lasts is the write's error, not a permanent one.
	f.reject = func([]pushed) int { return 503 }
	err = s.Write(context.Background(), batch[:2])
	if !errors.As(err, &be) || be.Index != 0 || fitsync.IsPermanent(err) {
		t.Fatalf("%v", err)
	}
	// A line larger than a request fails at once.
	big := line("/c.log", 1, strings.Repeat("x", 300))
	if err := s.Write(context.Background(), []*fitsync.Record{big}); !errors.As(err, &be) || !fitsync.IsPermanent(err) {
		t.Fatalf("%v", err)
	}
}

func TestRecordErrors(t *testing.T) {
	f := newFakeLoki(t)
	s := fakeSink(t, f, map[string]any{"labels": map[string]any{"l": "{level}"}, "tenant_field": "team"})
	var be *fitsync.BatchError
	noLabel := &fitsync.Record{Op: fitsync.OpMessage, After: fitsync.Map(fitsync.Field{Name: "line", Value: fitsync.String("x")})}
	if err := s.Write(context.Background(), []*fitsync.Record{noLabel}); !errors.As(err, &be) || !strings.Contains(err.Error(), "labels are all empty") {
		t.Fatalf("%v", err)
	}
	badTenant := &fitsync.Record{Op: fitsync.OpMessage, After: fitsync.Map(fitsync.Field{Name: "level", Value: fitsync.String("x")}, fitsync.Field{Name: "team", Value: fitsync.String("a|b")})}
	if err := s.Write(context.Background(), []*fitsync.Record{badTenant}); !errors.As(err, &be) || !strings.Contains(err.Error(), "tenant") {
		t.Fatalf("%v", err)
	}
	sj := fakeSink(t, f, map[string]any{"codec": map[string]any{"module": "json"}})
	nan := &fitsync.Record{Op: fitsync.OpMessage, After: fitsync.Map(fitsync.Field{Name: "f", Value: fitsync.Float(nanValue())})}
	if err := sj.Write(context.Background(), []*fitsync.Record{nan}); !errors.As(err, &be) || !fitsync.IsPermanent(err) {
		t.Fatalf("%v", err)
	}
	st := fakeSink(t, f, map[string]any{"tenant": "nobody"})
	if err := st.Test(context.Background()); err == nil {
		t.Fatal("a refused tenant passed the test")
	}
	down := fakeSink(t, f, nil)
	f.srv.Close()
	if err := down.Test(context.Background()); err == nil {
		t.Fatal("a closed server passed the test")
	}
}

func nanValue() float64 {
	var z float64
	return z / z
}

func TestProvisionErrors(t *testing.T) {
	for _, tc := range []struct {
		p    map[string]any
		want string
	}{
		{map[string]any{"urls": []string{"http://x"}, "labels": map[string]any{"9bad": "x"}}, "not a label's name"},
		{map[string]any{"urls": []string{"http://x"}, "labels": map[string]any{"__name": "x"}}, "not a label's name"},
		{map[string]any{"urls": []string{"http://x"}, "labels": map[string]any{"a": "{"}}, "not closed"},
		{map[string]any{"urls": []string{"http://x"}, "tenant": "a|b"}, "no line breaks"},
		{map[string]any{"urls": []string{"http://x"}, "codec": map[string]any{"module": "nothing"}}, "nothing"},
		{map[string]any{"urls": []string{"ftp://x"}}, "not an http"},
	} {
		if _, err := loadSink(t, tc.p); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: %v, want %q", tc.p, err, tc.want)
		}
	}
}

func TestLoadCodec(t *testing.T) {
	// A module of the codecs' namespace that is no codec.
	module.Register(module.Info{ID: fitsync.Codecs + ".loki_not_a_codec", New: func() any { return new(struct{}) }, Doc: "Not a codec, for a test."})
	ref := config.ModuleRef{Module: "loki_not_a_codec"}
	if _, err := loadCodec(&module.Context{}, &ref); err == nil || !strings.Contains(err.Error(), "is not a codec") {
		t.Fatalf("%v", err)
	}
}
