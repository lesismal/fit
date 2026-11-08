package syncs3

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fib"

	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
)

// The test codecs: counting makes len(line)%3 records of a line, with no
// key, and fails to encode a record whose line is "bad"; notcodec is a
// module of the codecs' namespace that is no codec.
func init() {
	module.Register(module.Info{ID: fitsync.Codecs + ".s3test_counting", New: func() any { return new(countingCodec) }, Doc: "A test codec."})
	module.Register(module.Info{ID: fitsync.Codecs + ".s3test_notcodec", New: func() any { return new(notCodec) }, Doc: "Not a codec."})
}

type countingCodec struct{}

func (countingCodec) Decode(data []byte, emit func(*fitsync.Record)) error {
	for i := 0; i < len(data)%3; i++ {
		emit(&fitsync.Record{Op: fitsync.OpMessage, After: fitsync.Map(fitsync.Field{Name: "line", Value: fitsync.String(fmt.Sprintf("%s#%d", data, i))})})
	}
	return nil
}

func (countingCodec) Encode(b []byte, recs []*fitsync.Record) ([]byte, error) {
	for _, r := range recs {
		v, _ := r.After.Get("line")
		if fitsync.TextOf(v) == "bad" {
			return b, errors.New("a bad line")
		}
		b = append(append(b, fitsync.TextOf(v)...), '\n')
	}
	return b, nil
}

type notCodec struct{}

// newFakeTLS is newFake over TLS, with the CA file of its certificate.
func newFakeTLS(t *testing.T) (*fakeS3, string) {
	f := newFake(t)
	f.srv.Close()
	f.srv = httptest.NewTLSServer(f)
	t.Cleanup(f.srv.Close)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	return f, ca
}

func TestProvisionRefusesMore(t *testing.T) {
	t.Setenv("FIT_TEST_S3_SECRET", "x")
	dir := t.TempDir()
	notPEM := filepath.Join(dir, "x.pem")
	os.WriteFile(notPEM, []byte("no"), 0o600)
	for _, c := range []struct{ ns, params, want string }{
		{fitsync.Sinks, `{"bucket": "logs", "region": "eu west"}`, "region"},
		{fitsync.Sinks, `{"bucket": "logs", "session_token_env": "FIT_TEST_S3_SECRET"}`, "needs access_key_id"},
		{fitsync.Sinks, `{"bucket": "logs", "access_key_id": "A/B", "secret_access_key_env": "FIT_TEST_S3_SECRET"}`, "no '/'"},
		{fitsync.Sinks, `{"bucket": "logs", "access_key_id": "AK", "secret_access_key_env": "1X"}`, "environment variable's name"},
		{fitsync.Sinks, `{"bucket": "logs", "ca_file": "` + filepath.Join(dir, "none") + `"}`, "no such file"},
		{fitsync.Sinks, `{"bucket": "logs", "ca_file": "` + notPEM + `"}`, "no PEM"},
		{fitsync.Sinks, `{"bucket": "logs", "key": "a}{position}"}`, "a } with no {"},
		{fitsync.Sinks, `{"bucket": "logs", "key": "{position}}"}`, "a } with no {"},
		{fitsync.Sinks, `{"bucket": "logs", "codec": {"module": "s3test_notcodec"}}`, "not a codec"},
		{fitsync.Sinks, `{"bucket": "logs", "codec": {"module": "nope"}}`, "nope"},
		{fitsync.Sources, `{"bucket": "B"}`, "bucket"},
		{fitsync.Sources, `{"bucket": "logs", "codec": {"module": "nope"}}`, "nope"},
	} {
		_, err := loadModule(c.ns, c.params)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.params, err, c.want)
		}
	}
}

// TestTLSAndSessionToken writes over TLS checked against a CA file, with a
// session token, through an engine given to the connector.
func TestTLSAndSessionToken(t *testing.T) {
	f, ca := newFakeTLS(t)
	t.Setenv("FIT_TEST_S3_TOKEN", "tok")
	s := loadSink(t, f.params(fmt.Sprintf(`"ca_file": %q, "session_token_env": "FIT_TEST_S3_TOKEN", "timeout": "30s", "addressing": "path"`, ca)))
	cfg := fib.DefaultConfig()
	cfg.LogSettings = false
	e, err := fib.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = e.Run(); close(done) }()
	t.Cleanup(func() { e.Stop(); <-done; _ = e.Close() })
	s.conn.engine = e
	var tok string
	f.hook = func(w http.ResponseWriter, r *http.Request, key string) bool {
		tok = r.Header.Get("X-Amz-Security-Token")
		return false
	}
	if err := s.Write(context.Background(), lines(0, 3)); err != nil {
		t.Fatal(err)
	}
	if tok != "tok" || len(f.keys()) != 1 {
		t.Errorf("token %q, %v", tok, f.keys())
	}
	s.ParallelWrites()
	if s.conn.timeout() != 30*time.Second {
		t.Error(s.conn.timeout())
	}
}

// TestSinkErrors: a server's errors, a connection refused, a context
// ended, a key too long and a record the codec fails on.
func TestSinkErrors(t *testing.T) {
	f := newFake(t)
	s := loadSink(t, f.params(""))
	long := strings.Repeat("x", 3000)
	f.hook = func(w http.ResponseWriter, r *http.Request, key string) bool {
		w.WriteHeader(500)
		w.Write([]byte(long))
		return true
	}
	err := s.Write(context.Background(), lines(0, 2))
	var e *Error
	if !errors.As(err, &e) || e.Status != 500 || e.Code != "" || !strings.HasSuffix(err.Error(), "...") || len(err.Error()) > 400 {
		t.Errorf("%v", err)
	}
	var be *fitsync.BatchError
	if errors.As(err, &be) {
		t.Error("a failure of the first object is the batch's")
	}
	f.hook = nil
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Write(ctx, lines(0, 2)); !errors.Is(err, context.Canceled) {
		t.Errorf("%v", err)
	}
	if err := s.conn.Put(context.Background(), "empty", "", nil); err != nil || f.object("empty") == nil {
		t.Errorf("%v", err)
	}

	gone := loadSink(t, `{"bucket": "logs", "endpoint": "http://127.0.0.1:1"}`)
	if err := gone.Write(context.Background(), lines(0, 1)); err == nil || !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Errorf("%v", err)
	}

	tooLong := loadSink(t, f.params(`"key": "`+strings.Repeat("k", 1100)+`{position}"`))
	if err := tooLong.Write(context.Background(), lines(0, 1)); !fitsync.IsPermanent(err) {
		t.Errorf("%v", err)
	}

	bad := loadSink(t, f.params(`"codec": {"module": "s3test_counting"}, "key": "bad/{position}"`))
	batch := lines(0, 3)
	batch[0].Position = nil // named by the position after it
	batch[1].After = fitsync.Map(fitsync.Field{Name: "line", Value: fitsync.String("bad")})
	if err := bad.Write(context.Background(), batch); !errors.As(err, &be) || be.Index != 1 || !fitsync.IsPermanent(err) {
		t.Fatalf("%v", err)
	}
	var keys []string
	for _, k := range f.keys() {
		if strings.HasPrefix(k, "bad/") {
			keys = append(keys, k)
		}
	}
	if len(keys) != 1 || string(f.object(keys[0])) != "line 0\n" {
		t.Errorf("%v", keys)
	}
	if err := bad.Write(context.Background(), batch[1:2]); !errors.As(err, &be) || be.Index != 0 {
		t.Errorf("%v", err)
	}
}

// TestSinkClock names an object of a record without a commit time by the
// time of the write.
func TestSinkClock(t *testing.T) {
	f := newFake(t)
	s := loadSink(t, f.params(`"key": "{yyyy}/{position}"`))
	r := line(1)
	r.CommitTime = time.Time{}
	if err := s.Write(context.Background(), []*fitsync.Record{r}); err != nil {
		t.Fatal(err)
	}
	if k := f.keys(); len(k) != 1 || !strings.HasPrefix(k[0], time.Now().UTC().Format("2006")+"/") {
		t.Error(k)
	}
}

// TestSourceObjectsChanging: an object gone between its listing and its
// reading is skipped, one changed fails the run, an answer of the whole
// object to a range is read from the offset, and a range past the end is
// the object's end.
func TestSourceObjectsChanging(t *testing.T) {
	f := newFake(t)
	f.objs["o/1.log"] = []byte("a\nb\n")
	f.objs["o/2.log"] = []byte("c\n")
	f.objs["o/3.log.gz"] = []byte("d\n")
	src := loadSource(t, f.params(`"prefix": "o/"`))
	f.hook = func(w http.ResponseWriter, r *http.Request, key string) bool {
		if r.Method == http.MethodGet && (key == "o/1.log" || key == "o/3.log.gz") {
			s3Error(w, 404, "NoSuchKey")
			return true
		}
		return false
	}
	if got := texts(collect(t, src, nil, 0)); fmt.Sprint(got) != "[c]" {
		t.Errorf("%q", got)
	}
	f.hook = func(w http.ResponseWriter, r *http.Request, key string) bool {
		if r.Method == http.MethodGet && key != "" {
			s3Error(w, 412, "PreconditionFailed")
			return true
		}
		return false
	}
	err := src.Run(context.Background(), &fitsync.Run{Once: true, Emit: func(*fitsync.Record) error { return nil }})
	var e *Error
	if !errors.As(err, &e) || e.Code != "PreconditionFailed" || !strings.Contains(err.Error(), "o/1.log") {
		t.Errorf("%v", err)
	}
	f.hook = nil
	delete(f.objs, "o/3.log.gz")
	f.whole = true
	from, _ := json.Marshal(position{Key: "o/1.log", Offset: 2})
	if got := texts(collect(t, src, from, 0)); fmt.Sprint(got) != "[b c]" {
		t.Errorf("%q", got)
	}
	f.whole = false
	from, _ = json.Marshal(position{Key: "o/1.log", Offset: 4})
	if got := texts(collect(t, src, from, 0)); fmt.Sprint(got) != "[c]" {
		t.Errorf("%q", got)
	}
	// A range answered empty before the object's end.
	f.hook = func(w http.ResponseWriter, r *http.Request, key string) bool {
		if r.Method == http.MethodGet && key == "o/2.log" {
			w.Header().Set("Content-Range", "bytes 0-1/2")
			w.WriteHeader(http.StatusPartialContent)
			return true
		}
		return false
	}
	from, _ = json.Marshal(position{Key: "o/1.log", Offset: 4, End: true})
	if err := src.Run(context.Background(), &fitsync.Run{From: from, Once: true, Emit: func(*fitsync.Record) error { return nil }}); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("%v", err)
	}
}

// TestSourceListing: a listing the server fails, one it answers badly, one
// truncated without a token, a server that ignores start-after, and a
// checkpoint that is not the source's.
func TestSourceListing(t *testing.T) {
	f := newFake(t)
	for i := range 5 {
		f.objs[fmt.Sprintf("l/%d", i)] = []byte(fmt.Sprintf("%d\n", i))
	}
	src := loadSource(t, f.params(`"prefix": "l/"`))
	run := func(from fitsync.Position) error {
		return src.Run(context.Background(), &fitsync.Run{From: from, Once: true, Emit: func(*fitsync.Record) error { return nil }})
	}
	for body, want := range map[string]string{
		"<nope": "the listing",
		"<ListBucketResult><IsTruncated>true</IsTruncated></ListBucketResult>": "no continuation token",
	} {
		f.hook = func(w http.ResponseWriter, r *http.Request, key string) bool {
			if key == "" {
				w.Write([]byte(body))
				return true
			}
			return false
		}
		if err := run(nil); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v, want %q", err, want)
		}
	}
	f.hook = func(w http.ResponseWriter, r *http.Request, key string) bool {
		s3Error(w, 403, "AccessDenied")
		return true
	}
	if err := run(nil); err == nil || !strings.Contains(err.Error(), "AccessDenied") {
		t.Errorf("%v", err)
	}
	f.hook = nil
	if err := run(fitsync.Position(`{"offset": 3}`)); err == nil || !strings.Contains(err.Error(), "not the source's") {
		t.Errorf("%v", err)
	}
	f.ignoreAfter = true
	from, _ := json.Marshal(position{Key: "l/2", End: true})
	if got := texts(collect(t, src, from, 0)); fmt.Sprint(got) != "[3 4]" {
		t.Errorf("%q", got)
	}
}

// TestSourceGzip: decompress gzip and none, a gzipped object that is not,
// one resumed past its end, and an empty one.
func TestSourceGzip(t *testing.T) {
	f := newFake(t)
	var zb bytes.Buffer
	zw := gzip.NewWriter(&zb)
	zw.Write([]byte("z1\nz2\n"))
	zw.Close()
	f.objs["g/a"] = zb.Bytes()
	f.objs["g/b"] = nil
	src := loadSource(t, f.params(`"prefix": "g/", "decompress": "gzip"`))
	if got := texts(collect(t, src, nil, 0)); fmt.Sprint(got) != "[z1 z2]" {
		t.Errorf("%q", got)
	}
	from, _ := json.Marshal(position{Key: "g/a", Offset: 100})
	if err := src.Run(context.Background(), &fitsync.Run{From: from, Once: true, Emit: func(*fitsync.Record) error { return nil }}); err == nil || !strings.Contains(err.Error(), "skipping to 100") {
		t.Errorf("%v", err)
	}
	f.objs["g/c"] = []byte("plain\n")
	if err := src.Run(context.Background(), &fitsync.Run{Once: true, Emit: func(*fitsync.Record) error { return nil }}); err == nil || !strings.Contains(err.Error(), "g/c") {
		t.Errorf("%v", err)
	}
	none := loadSource(t, f.params(`"prefix": "g/c", "decompress": "none"`))
	if got := texts(collect(t, none, nil, 0)); fmt.Sprint(got) != "[plain]" {
		t.Errorf("%q", got)
	}
	if none.pollInterval() != DefaultPollInterval || none.rangeBytes() != DefaultRangeBytes {
		t.Error("defaults")
	}
}

// TestSourceRecords: a codec making several records of a line, or none, a
// line it fails on, an Emit failing, and a context ended while reading.
func TestSourceRecords(t *testing.T) {
	f := newFake(t)
	f.objs["r/1"] = []byte("ab\nabc\na\n")
	src := loadSource(t, f.params(`"prefix": "r/", "codec": {"module": "s3test_counting"}, "namespace": "ns"`))
	all := collect(t, src, nil, 0)
	if got := texts(all); fmt.Sprint(got) != "[ab#0 ab#1 a#0]" {
		t.Fatalf("%q", got)
	}
	if all[0].Position != nil || all[1].Position == nil || all[1].Namespace != "ns" {
		t.Errorf("%s %s", all[0].Position, all[1].Position)
	}
	if n, ok := all[1].Key.Get("n"); !ok || fitsync.TextOf(n) != "1" {
		t.Errorf("%s", fitsync.TextOf(all[1].Key))
	}
	f.objs["r/2"] = []byte("{\nnot json\n")
	js := loadSource(t, f.params(`"prefix": "r/2", "codec": {"module": "json"}`))
	got := collect(t, js, nil, 0)
	if len(got) != 2 {
		t.Fatalf("%d records", len(got))
	}
	if e, ok := got[1].After.Get("error"); !ok || fitsync.TextOf(e) == "" {
		t.Error("no error")
	}
	stop := errors.New("stop")
	for _, at := range []int{1, 3} {
		n := 0
		err := src.Run(context.Background(), &fitsync.Run{Once: true, Emit: func(*fitsync.Record) error {
			if n++; n == at {
				return stop
			}
			return nil
		}})
		if err != stop {
			t.Errorf("at %d: %v", at, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	f.hook = func(w http.ResponseWriter, r *http.Request, key string) bool {
		if key == "r/1" {
			cancel()
			s3Error(w, 500, "InternalError")
			return true
		}
		return false
	}
	if err := src.Run(ctx, &fitsync.Run{Once: true, Emit: func(*fitsync.Record) error { return nil }}); !errors.Is(err, context.Canceled) {
		t.Errorf("%v", err)
	}
	f.hook = func(w http.ResponseWriter, r *http.Request, key string) bool {
		if key == "r/1" {
			s3Error(w, 500, "InternalError")
			return true
		}
		return false
	}
	if err := src.Run(context.Background(), &fitsync.Run{Once: true, Emit: func(*fitsync.Record) error { return nil }}); err == nil || !strings.Contains(err.Error(), "reading r/1") {
		t.Errorf("%v", err)
	}
}

func TestCanonicalQueryOdd(t *testing.T) {
	if got := canonicalQuery("b=%zz&&a"); got != "a=&b=%25zz" {
		t.Errorf("%q", got)
	}
	if (&Error{Status: 500, Message: "m"}).Error() != "S3 answered 500: m" {
		t.Error("the message")
	}
}
