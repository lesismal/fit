package syncs3

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
)

// fakeS3 is a bucket kept in memory that checks each request's signature,
// for the tests that need no server.
type fakeS3 struct {
	t      *testing.T
	bucket string
	signer *Signer

	mu      sync.Mutex
	objs    map[string][]byte
	types   map[string]string
	puts    []string
	gets    int
	failPut func(key string) bool
	// hook answers a request in place of the bucket, when it returns true.
	hook func(w http.ResponseWriter, r *http.Request, key string) bool
	// whole answers ranged GETs 200 with the whole object; ignoreAfter
	// lists from the start whatever start-after says.
	whole, ignoreAfter bool
	srv                *httptest.Server
}

func newFake(t *testing.T) *fakeS3 {
	f := &fakeS3{t: t, bucket: "logs", objs: map[string][]byte{}, types: map[string]string{},
		signer: &Signer{AccessKeyID: "AKFIT", SecretAccessKey: "secret", Region: "us-east-1", Service: "s3"}}
	f.srv = httptest.NewServer(f)
	t.Cleanup(f.srv.Close)
	t.Setenv("FIT_TEST_S3_SECRET", "secret")
	return f
}

func s3Error(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, "<Error><Code>%s</Code><Message>%s</Message></Error>", code, code)
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	// The signature, made again from what arrived.
	at, err := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
	if err != nil {
		s3Error(w, 403, "AccessDenied")
		return
	}
	check := &http.Request{Method: r.Method, URL: r.URL, Host: r.Host, Header: http.Header{}}
	for k, v := range r.Header {
		if k != "Authorization" {
			check.Header[k] = v
		}
	}
	if PayloadHash(body) != r.Header.Get("X-Amz-Content-Sha256") {
		s3Error(w, 400, "XAmzContentSHA256Mismatch")
		return
	}
	f.signer.Sign(check, r.Header.Get("X-Amz-Content-Sha256"), at)
	if check.Header.Get("Authorization") != r.Header.Get("Authorization") {
		f.t.Errorf("signature: %s, want %s", r.Header.Get("Authorization"), check.Header.Get("Authorization"))
		s3Error(w, 403, "SignatureDoesNotMatch")
		return
	}
	path := r.URL.Path
	if !strings.HasPrefix(path, "/"+f.bucket) {
		s3Error(w, 404, "NoSuchBucket")
		return
	}
	key := strings.TrimPrefix(strings.TrimPrefix(path, "/"+f.bucket), "/")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hook != nil && f.hook(w, r, key) {
		return
	}
	switch {
	case r.Method == http.MethodPut && key != "":
		if f.failPut != nil && f.failPut(key) {
			s3Error(w, 503, "SlowDown")
			return
		}
		f.objs[key], f.types[key] = body, r.Header.Get("Content-Type")
		f.puts = append(f.puts, key)
		w.Header().Set("ETag", etag(body))
	case r.Method == http.MethodGet && key == "":
		f.list(w, r)
	case r.Method == http.MethodGet:
		f.gets++
		data, ok := f.objs[key]
		if !ok {
			s3Error(w, 404, "NoSuchKey")
			return
		}
		if m := r.Header.Get("If-Match"); m != "" && m != etag(data) {
			s3Error(w, 412, "PreconditionFailed")
			return
		}
		var lo, hi int64
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &lo, &hi); err != nil {
			s3Error(w, 400, "InvalidRange")
			return
		}
		size := int64(len(data))
		if f.whole {
			w.Write(data)
			return
		}
		if lo >= size {
			s3Error(w, 416, "InvalidRange")
			return
		}
		hi = min(hi, size-1)
		w.Header().Set("ETag", etag(data))
		w.Header().Set("Last-Modified", time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC).Format(http.TimeFormat))
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", lo, hi, size))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(data[lo : hi+1])
	default:
		s3Error(w, 405, "MethodNotAllowed")
	}
}

func etag(b []byte) string { return `"` + PayloadHash(b)[:32] + `"` }

func (f *fakeS3) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	max, _ := strconv.Atoi(q.Get("max-keys"))
	after := q.Get("start-after")
	if f.ignoreAfter {
		after = ""
	}
	if tok := q.Get("continuation-token"); tok != "" {
		after = tok
	}
	var keys []string
	for k := range f.objs {
		if strings.HasPrefix(k, q.Get("prefix")) && k > after {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	type content struct {
		Key          string
		Size         int
		ETag         string
		LastModified string
	}
	out := struct {
		XMLName               xml.Name `xml:"ListBucketResult"`
		Contents              []content
		IsTruncated           bool
		NextContinuationToken string `xml:",omitempty"`
	}{}
	if len(keys) > max {
		keys = keys[:max]
		out.IsTruncated, out.NextContinuationToken = true, keys[len(keys)-1]
	}
	for _, k := range keys {
		out.Contents = append(out.Contents, content{k, len(f.objs[k]), etag(f.objs[k]), "2026-11-08T12:00:00.000Z"})
	}
	b, _ := xml.Marshal(out)
	w.Write(b)
}

func (f *fakeS3) params(extra string) string {
	p := fmt.Sprintf(`"endpoint": %q, "bucket": %q, "access_key_id": "AKFIT", "secret_access_key_env": "FIT_TEST_S3_SECRET"`, f.srv.URL, f.bucket)
	if extra != "" {
		p += ", " + extra
	}
	return "{" + p + "}"
}

func (f *fakeS3) keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var keys []string
	for k := range f.objs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (f *fakeS3) object(key string) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.objs[key]
}

func loadModule(ns, params string) (any, error) {
	var src string
	if ns == fitsync.Sinks {
		src = fmt.Sprintf(`{"sync": {"state_dir": "x", "pipelines": [{"name": "p", "source": {"module": "generate"}, "sinks": [{"name": "s", "sink": {"module": "s3", "params": %s}}]}]}}`, params)
	} else {
		src = fmt.Sprintf(`{"sync": {"state_dir": "x", "pipelines": [{"name": "p", "source": {"module": "s3", "params": %s}, "sinks": [{"name": "s", "sink": {"module": "memory"}}]}]}}`, params)
	}
	c, err := config.Load([]byte(src))
	if err != nil {
		return nil, err
	}
	if ns == fitsync.Sinks {
		return module.Load(ns, c.Sync.Pipelines[0].Sinks[0].Sink, nil)
	}
	return module.Load(ns, c.Sync.Pipelines[0].Source, nil)
}

func loadSink(t *testing.T, params string) *Sink {
	t.Helper()
	s, err := loadModule(fitsync.Sinks, params)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { module.Cleanup(s) })
	return s.(*Sink)
}

func loadSource(t *testing.T, params string) *Source {
	t.Helper()
	s, err := loadModule(fitsync.Sources, params)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { module.Cleanup(s) })
	return s.(*Source)
}

// line is the n-th record of a stream: a line, committed at n seconds past
// 2026-11-08, its position n.
func line(n int) *fitsync.Record {
	return &fitsync.Record{Namespace: "app/web", Op: fitsync.OpMessage,
		After:      fitsync.Map(fitsync.Field{Name: "line", Value: fitsync.String(fmt.Sprintf("line %d", n))}),
		CommitTime: time.Date(2026, 11, 8, 0, 0, n, 0, time.UTC), Position: fitsync.Position(fmt.Sprintf(`{"n":%d}`, n))}
}

func lines(from, to int) []*fitsync.Record {
	var recs []*fitsync.Record
	for n := from; n < to; n++ {
		recs = append(recs, line(n))
	}
	return recs
}

func TestProvisionRefuses(t *testing.T) {
	t.Setenv("FIT_TEST_S3_SECRET", "x")
	for _, c := range []struct{ params, want string }{
		{`{"bucket": "B!"}`, "bucket"},
		{`{"bucket": "logs", "endpoint": "ftp://h"}`, "endpoint"},
		{`{"bucket": "logs", "endpoint": "http://h/path"}`, "endpoint"},
		{`{"bucket": "logs", "access_key_id": "AK"}`, "go together"},
		{`{"bucket": "logs", "access_key_id": "AK", "secret_access_key_env": "FIT_TEST_S3_UNSET"}`, "not set"},
		{`{"bucket": "a.b", "addressing": "virtual"}`, "dot"},
		{`{"bucket": "logs", "key": "{namespace}/{date}"}`, "{position}"},
		{`{"bucket": "logs", "key": "{position}/{nope}"}`, "{nope}"},
		{`{"bucket": "logs", "key": "{position"}`, "not closed"},
	} {
		_, err := loadModule(fitsync.Sinks, c.params)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.params, err, c.want)
		}
	}
}

func TestObjectURL(t *testing.T) {
	s := loadSink(t, `{"bucket": "logs", "region": "eu-west-1"}`)
	if u := s.conn.objectURL("a b/c", nil); u.String() != "https://logs.s3.eu-west-1.amazonaws.com/a%20b/c" {
		t.Error(u)
	}
	s = loadSink(t, `{"bucket": "logs", "endpoint": "http://minio:9000"}`)
	if u := s.conn.objectURL("x+y", nil); u.String() != "http://minio:9000/logs/x%2By" {
		t.Error(u)
	}
	if u := s.conn.objectURL("", nil); u.String() != "http://minio:9000/logs" {
		t.Error(u)
	}
}

// TestSinkWrittenTwice writes a batch twice, as a pipeline does when it
// crashes after the write and before the checkpoint: the same objects, with
// the same content.
func TestSinkWrittenTwice(t *testing.T) {
	f := newFake(t)
	s := loadSink(t, f.params(`"max_object_bytes": 100`))
	batch := lines(0, 20)
	if err := s.Write(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	first := f.keys()
	if len(first) < 2 {
		t.Fatalf("max_object_bytes made %d objects", len(first))
	}
	content := map[string]string{}
	var all []string
	for _, k := range first {
		content[k] = string(f.object(k))
		all = append(all, strings.Split(strings.TrimSpace(content[k]), "\n")...)
		if !strings.HasPrefix(k, "app/web/2026-11-08/20261108T0000") || !strings.HasSuffix(k, ".txt") || len(f.object(k)) > 100 {
			t.Errorf("object %s, %d bytes", k, len(f.object(k)))
		}
	}
	if len(all) != 20 || all[0] != "line 0" || all[19] != "line 19" {
		t.Errorf("the lines: %q", all)
	}
	if err := s.Write(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	if again := f.keys(); fmt.Sprint(again) != fmt.Sprint(first) {
		t.Errorf("written again: %v, first %v", again, first)
	}
	for _, k := range first {
		if string(f.object(k)) != content[k] {
			t.Errorf("%s changed", k)
		}
	}
	if f.types[first[0]] != "text/plain; charset=utf-8" {
		t.Error(f.types[first[0]])
	}
}

// TestSinkPartial fails the second object of a batch: the records of the
// first are written, which the BatchError says.
func TestSinkPartial(t *testing.T) {
	f := newFake(t)
	s := loadSink(t, f.params(`"max_object_bytes": 50, "codec": {"module": "json"}, "gzip": true`))
	n := 0
	f.failPut = func(string) bool { n++; return n == 2 }
	err := s.Write(context.Background(), lines(0, 10))
	var be *fitsync.BatchError
	if !errors.As(err, &be) || be.Index == 0 || fitsync.IsPermanent(err) {
		t.Fatalf("%v", err)
	}
	keys := f.keys()
	if len(keys) != 1 || !strings.HasSuffix(keys[0], ".jsonl.gz") || f.types[keys[0]] != "application/gzip" {
		t.Fatal(keys)
	}
	zr, err := gzip.NewReader(bytes.NewReader(f.object(keys[0])))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(zr)
	if got := strings.Count(string(b), "\n"); got != be.Index || !strings.HasPrefix(string(b), `{"line":"line 0"}`) {
		t.Errorf("%d lines for index %d: %s", got, be.Index, b)
	}
	f.failPut = nil
	if err := s.Write(context.Background(), lines(be.Index, 10)); err != nil {
		t.Fatal(err)
	}
}

// TestSinkNamespaces starts an object at each namespace, when the key
// names it, and names objects of records without positions by the nearest
// one.
func TestSinkNamespaces(t *testing.T) {
	f := newFake(t)
	s := loadSink(t, f.params(""))
	batch := lines(0, 6)
	batch[3].Namespace, batch[4].Namespace, batch[5].Namespace = "/var/log/x", "/var/log/x", "app/web"
	batch[3].Position = nil
	batch[3].CommitTime = time.Time{}
	s.nowFn = func() time.Time { return time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC) }
	if err := s.Write(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	keys := f.keys()
	if len(keys) != 3 || !strings.HasPrefix(keys[2], "var/log/x/2030-01-02/20300102T030405.000000000Z-") {
		t.Fatal(keys)
	}
	a := positionToken(batch, object{lo: 3})
	if a == positionToken(batch, object{lo: 4}) || a != positionToken(batch, object{lo: 3}) {
		t.Error("tokens")
	}
	none := []*fitsync.Record{{}, {}}
	if positionToken(none, object{data: []byte("a")}) == positionToken(none, object{data: []byte("b")}) {
		t.Error("objects without positions are named by their lines")
	}
	// A first record with an origin names the object, at whatever
	// position.
	o := *batch[3]
	o.Origin = fitsync.Origin{Stream: "orders", N: [4]uint64{0, 42}}
	b := positionToken([]*fitsync.Record{&o}, object{})
	o.Position = fitsync.Position(`{"elsewhere":1}`)
	if b == a || b != positionToken([]*fitsync.Record{&o}, object{}) {
		t.Error("an origin's token")
	}
}

// collect runs src from from until it has emitted n records (all of them
// when n is 0, in mode once), and returns them.
func collect(t *testing.T, src *Source, from fitsync.Position, n int) []*fitsync.Record {
	t.Helper()
	var got []*fitsync.Record
	stop := errors.New("enough")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute) // a hang guard
	defer cancel()
	err := src.Run(ctx, &fitsync.Run{From: from, Once: true, Emit: func(r *fitsync.Record) error {
		got = append(got, r)
		if n > 0 && len(got) == n {
			return stop
		}
		return nil
	}})
	if err != nil && err != stop {
		t.Fatal(err)
	}
	return got
}

func texts(recs []*fitsync.Record) []string {
	var out []string
	for _, r := range recs {
		v, _ := r.After.Get("line")
		out = append(out, fitsync.TextOf(v))
	}
	return out
}

// TestSourceResumes reads objects in key order, in small ranges, and
// resumes inside an object, a plain one and a gzipped one, from the
// position of a record read.
func TestSourceResumes(t *testing.T) {
	f := newFake(t)
	var zb bytes.Buffer
	zw := gzip.NewWriter(&zb)
	zw.Write([]byte("g1\ng2\ng3\ng4"))
	zw.Close()
	f.objs["in/a.log"] = []byte("a1\na2\r\na3\n")
	f.objs["in/b.log.gz"] = zb.Bytes()
	f.objs["in/c/"] = nil
	f.objs["in/d.log"] = []byte("d1\n\nd3\n")
	f.objs["other/x.log"] = []byte("x\n")
	src := loadSource(t, f.params(`"prefix": "in/", "range_bytes": 3`))
	all := collect(t, src, nil, 0)
	want := []string{"a1", "a2", "a3", "g1", "g2", "g3", "g4", "d1", "", "d3"}
	if fmt.Sprint(texts(all)) != fmt.Sprint(want) {
		t.Fatalf("%q", texts(all))
	}
	if all[0].Namespace != "in/a.log" || all[0].CommitTime.IsZero() || all[0].Source != "s3" {
		t.Errorf("%+v", all[0])
	}
	if k, _ := all[1].Key.Get("offset"); fitsync.TextOf(k) != "3" {
		t.Errorf("the key %s", fitsync.TextOf(all[1].Key))
	}
	var p position
	json.Unmarshal(all[2].Position, &p)
	if p != (position{Key: "in/a.log", Offset: 10, End: true}) {
		t.Errorf("%+v", p)
	}
	for i := 1; i < len(all); i++ {
		rest := collect(t, src, all[i-1].Position, 0)
		if fmt.Sprint(texts(rest)) != fmt.Sprint(want[i:]) {
			t.Errorf("resumed after %d: %q", i, texts(rest))
		}
	}
	// start_after, and a long line cut.
	f.objs["in/e.log"] = []byte(strings.Repeat("y", 50) + "\nz\n")
	src = loadSource(t, f.params(`"prefix": "in/", "start_after": "in/d.log", "max_line_bytes": 10, "range_bytes": 7`))
	if got := texts(collect(t, src, nil, 0)); fmt.Sprint(got) != fmt.Sprint([]string{strings.Repeat("y", 10), "z"}) || src.LinesCut() != 1 {
		t.Errorf("%q, %d cut", got, src.LinesCut())
	}
}

// TestSourcePolls lists again for keys past the last, in a stream.
func TestSourcePolls(t *testing.T) {
	f := newFake(t)
	f.objs["p/1"] = []byte("one\n")
	src := loadSource(t, f.params(`"prefix": "p/", "poll_interval": "10ms"`))
	got := make(chan string, 10)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- src.Run(ctx, &fitsync.Run{Emit: func(r *fitsync.Record) error {
			v, _ := r.After.Get("line")
			got <- fitsync.TextOf(v)
			return nil
		}})
	}()
	guard := time.After(time.Minute)
	for i, want := range []string{"one", "two"} {
		select {
		case s := <-got:
			if s != want {
				t.Fatalf("%q, want %q", s, want)
			}
		case <-guard:
			t.Fatal("no record")
		}
		if i == 0 {
			f.mu.Lock()
			f.objs["p/0"] = []byte("before\n") // before the last key: not seen
			f.objs["p/2"] = []byte("two\n")
			f.mu.Unlock()
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Error(err)
	}
	if len(got) != 0 {
		t.Errorf("more: %q", <-got)
	}
}

// TestRoundTrip writes records with the sink and reads them back with the
// source: the same lines, in order.
func TestRoundTrip(t *testing.T) {
	f := newFake(t)
	sink := loadSink(t, f.params(`"gzip": true, "max_object_bytes": 64`))
	var batch []*fitsync.Record
	for n := 0; n < 30; n++ {
		batch = append(batch, line(n))
	}
	if err := sink.Write(context.Background(), batch[:13]); err != nil {
		t.Fatal(err)
	}
	if err := sink.Write(context.Background(), batch[13:]); err != nil {
		t.Fatal(err)
	}
	src := loadSource(t, f.params(`"prefix": "app/"`))
	got := texts(collect(t, src, nil, 0))
	if fmt.Sprint(got) != fmt.Sprint(texts(batch)) {
		t.Errorf("%q", got)
	}
}
