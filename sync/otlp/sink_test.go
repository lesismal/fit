package syncotlp

import (
	"compress/gzip"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	"github.com/lesismal/fit/sync/internal/fibrest"
	"github.com/lesismal/fit/sync/internal/pb"
)

func init() {
	fibrest.BackoffFirst, fibrest.BackoffMost = time.Millisecond, 2*time.Millisecond
}

// exported is a log record a fake receiver took, written as text the
// tests compare: the resource's attributes, then the record's parts.
type exported struct {
	resource string
	time     int64
	sevNum   int64
	sevText  string
	body     string
	attrs    string
	trace    string
	span     string
	observed bool
}

// fakeReceiver is an OTLP/HTTP logs receiver that decodes what it is sent,
// in either encoding; reject, when set, answers with a status instead.
type fakeReceiver struct {
	mu      sync.Mutex
	recs    []exported
	reqs    int
	reject  func([]exported) int
	answer  []byte
	answerT string
	header  http.Header
	srv     *httptest.Server
}

func newFake(t *testing.T) *fakeReceiver {
	f := &fakeReceiver{}
	f.srv = httptest.NewServer(f)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeReceiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasSuffix(r.URL.Path, "/v1/logs") {
		w.WriteHeader(404)
		return
	}
	var body []byte
	if r.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			w.WriteHeader(400)
			return
		}
		body, _ = io.ReadAll(zr)
	} else {
		body, _ = io.ReadAll(r.Body)
	}
	var recs []exported
	var err error
	if r.Header.Get("Content-Type") == "application/json" {
		recs, err = decodeJSONRequest(body)
	} else {
		recs, err = decodeProtoRequest(body)
	}
	if err != nil {
		w.WriteHeader(500)
		_, _ = io.WriteString(w, err.Error())
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs++
	f.header = r.Header.Clone()
	if f.reject != nil {
		if st := f.reject(recs); st != 0 {
			w.WriteHeader(st)
			_, _ = io.WriteString(w, "refused")
			return
		}
	}
	f.recs = append(f.recs, recs...)
	if f.answer != nil {
		w.Header().Set("Content-Type", f.answerT)
		_, _ = w.Write(f.answer)
	}
}

func (f *fakeReceiver) got() []exported {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]exported(nil), f.recs...)
}

// fields reads a message's fields.
func fields(b []byte) ([]pb.Field, error) {
	var out []pb.Field
	for len(b) > 0 {
		f, rest, err := pb.Next(b)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
		b = rest
	}
	return out, nil
}

// anyText writes an AnyValue's bytes as text: s:..., b:..., i:..., d:...,
// a[...], m{k=v,...}, x:hex.
func anyText(b []byte) string {
	fs, _ := fields(b)
	if len(fs) == 0 {
		return "null"
	}
	f := fs[0]
	switch f.Num {
	case 1:
		return "s:" + string(f.Bytes)
	case 2:
		return "b:" + strconv.FormatBool(f.Value != 0)
	case 3:
		return "i:" + strconv.FormatInt(int64(f.Value), 10)
	case 4:
		return "d:" + strconv.FormatFloat(math.Float64frombits(f.Value), 'g', -1, 64)
	case 5:
		var parts []string
		vs, _ := fields(f.Bytes)
		for _, v := range vs {
			parts = append(parts, anyText(v.Bytes))
		}
		return "a[" + strings.Join(parts, ",") + "]"
	case 6:
		return "m{" + kvText(f.Bytes, 1) + "}"
	case 7:
		return "x:" + hex.EncodeToString(f.Bytes)
	}
	return "?"
}

// kvText writes the KeyValues of field num of b as k=v, in order.
func kvText(b []byte, num int) string {
	fs, _ := fields(b)
	var parts []string
	for _, f := range fs {
		if f.Num != num {
			continue
		}
		kv, _ := fields(f.Bytes)
		var k, v string
		for _, x := range kv {
			if x.Num == 1 {
				k = string(x.Bytes)
			} else {
				v = anyText(x.Bytes)
			}
		}
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, ",")
}

func decodeProtoRequest(b []byte) ([]exported, error) {
	top, err := fields(b)
	if err != nil {
		return nil, err
	}
	var out []exported
	for _, rl := range top {
		parts, err := fields(rl.Bytes)
		if err != nil {
			return nil, err
		}
		var resource string
		for _, p := range parts {
			switch p.Num {
			case 1:
				resource = kvText(p.Bytes, 1)
			case 2:
				sl, _ := fields(p.Bytes)
				for _, x := range sl {
					if x.Num != 2 {
						continue
					}
					e := exported{resource: resource}
					rf, err := fields(x.Bytes)
					if err != nil {
						return nil, err
					}
					for _, y := range rf {
						switch y.Num {
						case 1:
							e.time = int64(y.Value)
						case 2:
							e.sevNum = int64(y.Value)
						case 3:
							e.sevText = string(y.Bytes)
						case 5:
							e.body = anyText(y.Bytes)
						case 9:
							e.trace = hex.EncodeToString(y.Bytes)
						case 10:
							e.span = hex.EncodeToString(y.Bytes)
						case 11:
							e.observed = y.Value > 0
						}
					}
					e.attrs = kvText(x.Bytes, 6)
					out = append(out, e)
				}
			}
		}
	}
	return out, nil
}

type jsonAny struct {
	String *string  `json:"stringValue"`
	Bool   *bool    `json:"boolValue"`
	Int    *string  `json:"intValue"`
	Double *float64 `json:"doubleValue"`
	Bytes  *string  `json:"bytesValue"`
	Array  *struct {
		Values []jsonAny `json:"values"`
	} `json:"arrayValue"`
	KV *struct {
		Values []jsonKV `json:"values"`
	} `json:"kvlistValue"`
}

type jsonKV struct {
	Key   string  `json:"key"`
	Value jsonAny `json:"value"`
}

func (a jsonAny) text() string {
	switch {
	case a.String != nil:
		return "s:" + *a.String
	case a.Bool != nil:
		return "b:" + strconv.FormatBool(*a.Bool)
	case a.Int != nil:
		return "i:" + *a.Int
	case a.Double != nil:
		return "d:" + strconv.FormatFloat(*a.Double, 'g', -1, 64)
	case a.Bytes != nil:
		return "x:" + *a.Bytes
	case a.Array != nil:
		var parts []string
		for _, v := range a.Array.Values {
			parts = append(parts, v.text())
		}
		return "a[" + strings.Join(parts, ",") + "]"
	case a.KV != nil:
		return "m{" + kvs(a.KV.Values) + "}"
	}
	return "null"
}

func kvs(in []jsonKV) string {
	var parts []string
	for _, kv := range in {
		parts = append(parts, kv.Key+"="+kv.Value.text())
	}
	return strings.Join(parts, ",")
}

func decodeJSONRequest(b []byte) ([]exported, error) {
	var req struct {
		ResourceLogs []struct {
			Resource struct {
				Attributes []jsonKV `json:"attributes"`
			} `json:"resource"`
			ScopeLogs []struct {
				LogRecords []struct {
					Time     string   `json:"timeUnixNano"`
					Observed string   `json:"observedTimeUnixNano"`
					SevNum   int64    `json:"severityNumber"`
					SevText  string   `json:"severityText"`
					Body     jsonAny  `json:"body"`
					Attrs    []jsonKV `json:"attributes"`
					Trace    string   `json:"traceId"`
					Span     string   `json:"spanId"`
				} `json:"logRecords"`
			} `json:"scopeLogs"`
		} `json:"resourceLogs"`
	}
	if err := json.Unmarshal(b, &req); err != nil {
		return nil, err
	}
	var out []exported
	for _, rl := range req.ResourceLogs {
		for _, sl := range rl.ScopeLogs {
			for _, r := range sl.LogRecords {
				ts, _ := strconv.ParseInt(r.Time, 10, 64)
				out = append(out, exported{resource: kvs(rl.Resource.Attributes), time: ts, sevNum: r.SevNum, sevText: r.SevText,
					body: r.Body.text(), attrs: kvs(r.Attrs), trace: r.Trace, span: r.Span, observed: r.Observed != ""})
			}
		}
	}
	return out, nil
}

func loadSink(t *testing.T, params map[string]any) (*Sink, error) {
	t.Helper()
	b, _ := json.Marshal(params)
	v, err := config.ParseValue(b, "params")
	if err != nil {
		t.Fatal(err)
	}
	m, err := module.Load(fitsync.Sinks, config.ModuleRef{Module: "otlp_logs", Params: v}, nil)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { module.Cleanup(m) })
	return m.(*Sink), nil
}

func fakeSink(t *testing.T, f *fakeReceiver, params map[string]any) *Sink {
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

func logRec(n int, level string) *fitsync.Record {
	return &fitsync.Record{Source: "file", Namespace: "/app.log", Op: fitsync.OpMessage, CommitTime: time.Unix(1_700_000_000+int64(n), 0),
		Position: fitsync.Position(fmt.Sprintf(`{"n":%d}`, n)),
		After: fitsync.Map(fitsync.Field{Name: "line", Value: fitsync.String("line " + strconv.Itoa(n))},
			fitsync.Field{Name: "level", Value: fitsync.String(level)}, fitsync.Field{Name: "host", Value: fitsync.String("h" + strconv.Itoa(n%2))})}
}

// TestExportEncodings exports records in both encodings, gzipped and not,
// under two resources.
func TestExportEncodings(t *testing.T) {
	for _, enc := range []string{"protobuf", "json"} {
		for _, comp := range []string{"gzip", "none"} {
			t.Run(enc+"-"+comp, func(t *testing.T) {
				f := newFake(t)
				s := fakeSink(t, f, map[string]any{"encoding": enc, "compression": comp, "service_name": "shop",
					"resource": map[string]any{"host.name": "{host}", "empty": "{nothing}"}, "severity_field": "level"})
				batch := []*fitsync.Record{logRec(1, "INFO"), logRec(2, "Warning"), logRec(3, "odd")}
				if err := s.Write(context.Background(), batch); err != nil {
					t.Fatal(err)
				}
				got := f.got()
				if len(got) != 3 {
					t.Fatalf("%+v", got)
				}
				var lines []string
				for _, e := range got {
					if !e.observed {
						t.Errorf("no observed time: %+v", e)
					}
					lines = append(lines, fmt.Sprintf("%s|%d|%d|%s|%s|%s", e.resource, e.time-1_700_000_000e9, e.sevNum, e.sevText, e.body, e.attrs))
				}
				sort.Strings(lines)
				want := []string{
					"service.name=s:shop,service.version=s:dev,host.name=s:h0|2000000000|13|Warning|s:line 2|fit.namespace=s:/app.log,fit.op=s:message",
					"service.name=s:shop,service.version=s:dev,host.name=s:h1|1000000000|9|INFO|s:line 1|fit.namespace=s:/app.log,fit.op=s:message",
					"service.name=s:shop,service.version=s:dev,host.name=s:h1|3000000000|0|odd|s:line 3|fit.namespace=s:/app.log,fit.op=s:message",
				}
				if strings.Join(lines, "\n") != strings.Join(want, "\n") {
					t.Fatalf("\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
				}
				if (f.header.Get("Content-Encoding") == "gzip") != (comp == "gzip") {
					t.Fatalf("Content-Encoding %q", f.header.Get("Content-Encoding"))
				}
				if err := s.Test(context.Background()); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

// TestBodyRow exports rows as typed maps, with trace and span IDs, the
// time from a field, severities as numbers.
func TestBodyRow(t *testing.T) {
	for _, enc := range []string{"protobuf", "json"} {
		t.Run(enc, func(t *testing.T) {
			f := newFake(t)
			s := fakeSink(t, f, map[string]any{"encoding": enc, "body": "row", "timestamp_field": "at", "severity_field": "sev",
				"trace_id_field": "trace", "span_id_field": "span", "attributes": map[string]any{"k": "{$key}"}})
			row := fitsync.Map(
				fitsync.Field{Name: "at", Value: fitsync.Int(1_700_000_000_000)},
				fitsync.Field{Name: "sev", Value: fitsync.Int(17)},
				fitsync.Field{Name: "trace", Value: fitsync.String("0102030405060708090a0b0c0d0e0f10")},
				fitsync.Field{Name: "span", Value: fitsync.String("0102030405060708")},
				fitsync.Field{Name: "s", Value: fitsync.String("")},
				fitsync.Field{Name: "b", Value: fitsync.Bool(false)},
				fitsync.Field{Name: "u", Value: fitsync.Uint(math.MaxUint64)},
				fitsync.Field{Name: "u2", Value: fitsync.Uint(5)},
				fitsync.Field{Name: "f", Value: fitsync.Float(0)},
				fitsync.Field{Name: "x", Value: fitsync.Bytes([]byte{1, 2})},
				fitsync.Field{Name: "d", Value: fitsync.Decimal("1.50")},
				fitsync.Field{Name: "a", Value: fitsync.Array(fitsync.Int(-1), fitsync.Null(), fitsync.Bool(true))},
				fitsync.Field{Name: "m", Value: fitsync.Map(fitsync.Field{Name: "in", Value: fitsync.Float(2.5)})},
			)
			r := &fitsync.Record{Namespace: "t", Op: fitsync.OpInsert, Key: fitsync.Int(9), After: row}
			if err := s.Write(context.Background(), []*fitsync.Record{r}); err != nil {
				t.Fatal(err)
			}
			got := f.got()
			if len(got) != 1 {
				t.Fatalf("%+v", got)
			}
			e := got[0]
			x := "x:0102"
			if enc == "json" {
				x = "x:AQI="
			}
			wantBody := "m{at=i:1700000000000,sev=i:17,trace=s:0102030405060708090a0b0c0d0e0f10,span=s:0102030405060708,s=s:,b=b:false,u=s:18446744073709551615,u2=i:5,f=d:0," +
				x + ",d=s:1.50,a=a[i:-1,null,b:true],m=m{in=d:2.5}}"
			wantBody = strings.Replace(wantBody, ","+x, ",x="+x, 1)
			if e.time != 1_700_000_000e9 || e.sevNum != 17 || e.sevText != "" || e.body != wantBody || e.attrs != "k=s:9" ||
				e.trace != "0102030405060708090a0b0c0d0e0f10" || e.span != "0102030405060708" {
				t.Fatalf("%+v\nbody %s\nwant %s", e, e.body, wantBody)
			}
		})
	}
}

// TestSplitRefused finds the record the receiver refuses, the records
// before it exported, and reads partial successes.
func TestSplitRefused(t *testing.T) {
	f := newFake(t)
	f.reject = func(rs []exported) int {
		for _, r := range rs {
			if r.body == "s:line 6" {
				return 400
			}
		}
		return 0
	}
	s := fakeSink(t, f, map[string]any{"max_request_bytes": 400, "codec": map[string]any{"module": "line"}})
	var batch []*fitsync.Record
	for i := 1; i <= 10; i++ {
		batch = append(batch, logRec(i, "info"))
	}
	err := s.Write(context.Background(), batch)
	var be *fitsync.BatchError
	if !errors.As(err, &be) || be.Index != 5 || !fitsync.IsPermanent(err) || len(f.got()) != 5 {
		t.Fatalf("%v, %d exported", err, len(f.got()))
	}
	// The bad record first; a request refused only whole.
	f.recs = nil
	if err := s.Write(context.Background(), batch[5:7]); !errors.As(err, &be) || be.Index != 0 || len(f.got()) != 0 {
		t.Fatalf("%v", err)
	}
	f.reject = func(rs []exported) int {
		if len(rs) > 2 {
			return 413
		}
		return 0
	}
	if err := s.Write(context.Background(), batch[:4]); err != nil || len(f.got()) != 4 {
		t.Fatalf("%v, %d", err, len(f.got()))
	}
	f.reject = func([]exported) int { return 401 }
	if err := s.Write(context.Background(), batch[:1]); !errors.As(err, &be) || fitsync.IsPermanent(err) {
		t.Fatalf("%v", err)
	}
	f.reject = nil

	var warned []string
	old := warnf
	defer func() { warnf = old }()
	warnf = func(format string, args ...any) { warned = append(warned, fmt.Sprintf(format, args...)) }
	ps := pb.AppendMessage(nil, 1, func(b []byte) []byte {
		b = pb.AppendInt(b, 1, 2)
		return pb.AppendString(b, 2, "too old")
	})
	f.answer, f.answerT = ps, "application/x-protobuf"
	if err := s.Write(context.Background(), batch[:3]); err != nil || len(warned) != 1 || !strings.Contains(warned[0], "rejected 2 of 3 log records: too old") {
		t.Fatalf("%v %v", err, warned)
	}
	f.answer, f.answerT = []byte(`{"partialSuccess":{"rejectedLogRecords":"1","errorMessage":"bad"}}`), "application/json"
	if err := s.Write(context.Background(), batch[:3]); err != nil || len(warned) != 2 || !strings.Contains(warned[1], "rejected 1 of 3") {
		t.Fatalf("%v %v", err, warned)
	}
	for _, a := range []struct {
		t string
		b []byte
	}{{"application/json", []byte("{")}, {"application/x-protobuf", []byte{0x80}}, {"application/x-protobuf", []byte{0x0a, 1, 0x80}},
		{"application/x-protobuf", pb.AppendString(nil, 2, "other")}} {
		if n, _ := partialSuccess(a.t, a.b); n != 0 {
			t.Errorf("%s %q: %d", a.t, a.b, n)
		}
	}
	if n, msg := partialSuccess("application/x-protobuf", nil); n != 0 || msg != "" {
		t.Fatal(n, msg)
	}
}

func TestRecordErrors(t *testing.T) {
	f := newFake(t)
	var be *fitsync.BatchError
	s := fakeSink(t, f, map[string]any{"trace_id_field": "trace", "span_id_field": "span"})
	bad := &fitsync.Record{Op: fitsync.OpInsert, After: fitsync.Map(fitsync.Field{Name: "trace", Value: fitsync.String("zz")})}
	if err := s.Write(context.Background(), []*fitsync.Record{bad}); !errors.As(err, &be) || !strings.Contains(err.Error(), "32 hex digits") {
		t.Fatalf("%v", err)
	}
	bad = &fitsync.Record{Op: fitsync.OpInsert, After: fitsync.Map(fitsync.Field{Name: "span", Value: fitsync.String("01")})}
	if err := s.Write(context.Background(), []*fitsync.Record{bad}); !errors.As(err, &be) || !strings.Contains(err.Error(), "16 hex digits") {
		t.Fatalf("%v", err)
	}
	nan := fitsync.Map(fitsync.Field{Name: "f", Value: fitsync.Float(math.NaN())})
	sj := fakeSink(t, f, map[string]any{"codec": map[string]any{"module": "json"}})
	if err := sj.Write(context.Background(), []*fitsync.Record{{Op: fitsync.OpInsert, After: nan}}); !errors.As(err, &be) || !fitsync.IsPermanent(err) {
		t.Fatalf("%v", err)
	}
	for _, enc := range []string{"json", "protobuf"} {
		sr := fakeSink(t, f, map[string]any{"body": "row", "encoding": enc})
		deep := fitsync.Map(fitsync.Field{Name: "a", Value: fitsync.Array(fitsync.Map(fitsync.Field{Name: "f", Value: fitsync.Float(math.NaN())}))})
		err := sr.Write(context.Background(), []*fitsync.Record{{Op: fitsync.OpInsert, After: deep}})
		if enc == "json" && (!errors.As(err, &be) || !fitsync.IsPermanent(err)) || enc == "protobuf" && err != nil {
			t.Fatalf("%s: %v", enc, err)
		}
	}
	small := fakeSink(t, f, map[string]any{"max_request_bytes": 10})
	if err := small.Write(context.Background(), []*fitsync.Record{logRec(1, "x")}); !errors.As(err, &be) || !strings.Contains(err.Error(), "max_request_bytes") {
		t.Fatalf("%v", err)
	}
}

// TestPaths and headers: a URL ending in /v1/logs is used as it is, and a
// headers file's headers are sent.
func TestPathsAndHeaders(t *testing.T) {
	f := newFake(t)
	hf := filepath.Join(t.TempDir(), "headers")
	_ = os.WriteFile(hf, []byte("# a key\nX-Api-Key: k1\n"), 0o600)
	s := fakeSink(t, f, map[string]any{"urls": []string{f.srv.URL + "/v1/logs/"}, "headers_file": hf})
	if err := s.Write(context.Background(), []*fitsync.Record{logRec(1, "x")}); err != nil {
		t.Fatal(err)
	}
	if f.header.Get("X-Api-Key") != "k1" || len(f.got()) != 1 {
		t.Fatalf("%v", f.header)
	}
}

func TestProvisionErrors(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "bad")
	_ = os.WriteFile(bad, []byte("no colon\n"), 0o600)
	for _, tc := range []struct {
		p    map[string]any
		want string
	}{
		{map[string]any{"urls": []string{"http://x/v1/logs", "http://y"}}, "some URLs end"},
		{map[string]any{"urls": []string{"http://x"}, "headers_file": bad}, "not a header field"},
		{map[string]any{"urls": []string{"http://x"}, "headers_file": bad + "-none"}, "no such file"},
		{map[string]any{"urls": []string{"http://x"}, "resource": map[string]any{"service.name": "x"}}, "the sink's"},
		{map[string]any{"urls": []string{"http://x"}, "attributes": map[string]any{"": "x"}}, "without a name"},
		{map[string]any{"urls": []string{"http://x"}, "attributes": map[string]any{"a": "{"}}, "not closed"},
		{map[string]any{"urls": []string{"http://x"}, "body": "row", "codec": map[string]any{"module": "json"}}, "body row has none"},
		{map[string]any{"urls": []string{"http://x"}, "codec": map[string]any{"module": "nothing"}}, "nothing"},
	} {
		if _, err := loadSink(t, tc.p); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: %v, want %q", tc.p, err, tc.want)
		}
	}
	module.Register(module.Info{ID: fitsync.Codecs + ".otlp_not_a_codec", New: func() any { return new(struct{}) }, Doc: "Not a codec, for a test."})
	ref := config.ModuleRef{Module: "otlp_not_a_codec"}
	if _, err := loadCodec(&module.Context{}, &ref); err == nil || !strings.Contains(err.Error(), "is not a codec") {
		t.Fatalf("%v", err)
	}
}
