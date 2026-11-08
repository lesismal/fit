package httpsync

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
)

// loadSource loads an http source with params, as a pipeline would.
func loadSource(t *testing.T, params string) *Source {
	t.Helper()
	v, err := loadSourceErr(params)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { module.Cleanup(v) })
	return v.(*Source)
}

func loadSourceErr(params string) (any, error) {
	src := fmt.Sprintf(`{"sync": {"state_dir": "x", "pipelines": [{"name": "p", "source": {"module": "http", "params": %s}, "sinks": [{"name": "m", "sink": {"module": "memory"}}]}]}}`, params)
	c, err := config.Load([]byte(src))
	if err != nil {
		return nil, err
	}
	return module.Load(fitsync.Sources, c.Sync.Pipelines[0].Source, nil)
}

// runSource runs s under the pipeline name with emit, and returns a stop
// that cancels it and waits for Run to return.
func runSource(t *testing.T, s *Source, name string, emit func(*fitsync.Record) error) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = s.Run(ctx, &fitsync.Run{Pipeline: name, Emit: emit})
	}()
	deadline := time.Now().Add(hang)
	for Lookup(name) != s {
		if time.Now().After(deadline) {
			t.Fatal("the source never registered")
		}
		time.Sleep(time.Millisecond)
	}
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(hang):
			t.Fatal("Run did not return")
		}
	}
}

// TestSourceRunAgain checks a Run called again, as after the source fails,
// answers 503 what the earlier run emitted and was not acknowledged, and
// that an acknowledgement of the earlier run's position answers nothing.
func TestSourceRunAgain(t *testing.T) {
	s := loadSource(t, `{}`)
	var last atomic.Pointer[fitsync.Record]
	stop := runSource(t, s, "again", func(r *fitsync.Record) error { last.Store(r); return nil })
	out, err := offer(t, s, `{"id": 1}`, "")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(hang)
	for last.Load() == nil {
		if time.Now().After(deadline) {
			t.Fatal("nothing emitted")
		}
		time.Sleep(time.Millisecond)
	}
	stop()
	stop = runSource(t, s, "again", func(*fitsync.Record) error { return nil })
	defer stop()
	if o := await(t, out); o.Status != http.StatusServiceUnavailable || !strings.Contains(o.Err, "restarted") {
		t.Fatalf("%+v", o)
	}
	// The earlier run's position, and positions that are not the
	// source's, answer nothing and break nothing.
	s.Acknowledged(last.Load().Position)
	s.Acknowledged(fitsync.Position(`not json`))
}

// TestSourceStopsWithInbox checks a stop while a body waits to be read, and
// one is held in Emit, answers both 503.
func TestSourceStopsWithInbox(t *testing.T) {
	s := loadSource(t, `{}`)
	entered := make(chan struct{})
	stop := runSource(t, s, "inbox", func(*fitsync.Record) error {
		close(entered)
		return errors.New("stopping")
	})
	first, _ := offer(t, s, `{"id": 1}`, "")
	<-entered
	if o := await(t, first); o.Status != http.StatusServiceUnavailable {
		t.Fatalf("held in Emit: %+v", o)
	}
	stop()

	// Run returned at Emit's error: a body offered now finds it stopped.
	if _, err := offer(t, s, `{"id": 2}`, ""); !errors.Is(err, ErrBusy) {
		t.Fatalf("%v", err)
	}

	// A body in the inbox when Run returns is answered then.
	s2 := loadSource(t, `{}`)
	release := make(chan struct{})
	entered2 := make(chan struct{}, 1)
	stop2 := runSource(t, s2, "inbox2", func(*fitsync.Record) error {
		entered2 <- struct{}{}
		<-release
		return errors.New("stopping")
	})
	held, _ := offer(t, s2, `{"id": 1}`, "")
	<-entered2
	waiting, err := offer(t, s2, `{"id": 2}`, "")
	if err != nil {
		t.Fatal(err)
	}
	close(release)
	stop2()
	for _, out := range []chan Outcome{held, waiting} {
		if o := await(t, out); o.Status != http.StatusServiceUnavailable {
			t.Errorf("%+v", o)
		}
	}

	// What was emitted and not acknowledged when the pipeline stops is
	// answered by Cleanup.
	s3, err := loadSourceErr(`{}`)
	if err != nil {
		t.Fatal(err)
	}
	src3 := s3.(*Source)
	emitted := make(chan struct{}, 1)
	stop3 := runSource(t, src3, "inbox3", func(*fitsync.Record) error { emitted <- struct{}{}; return nil })
	kept, _ := offer(t, src3, `{"id": 1}`, "")
	<-emitted
	stop3()
	module.Cleanup(src3)
	if o := await(t, kept); o.Status != http.StatusServiceUnavailable || !strings.Contains(o.Err, "stopped") {
		t.Errorf("emitted: %+v", o)
	}
}

// TestSourceTakenOver checks a second source running under the same
// pipeline name takes it over, the first refusing bodies, and that the
// first's end leaves the second registered.
func TestSourceTakenOver(t *testing.T) {
	a, b := loadSource(t, `{}`), loadSource(t, `{}`)
	stopA := runSource(t, a, "over", func(*fitsync.Record) error { return nil })
	stopB := runSource(t, b, "over", func(*fitsync.Record) error { return nil })
	defer stopB()
	if _, err := offer(t, a, `{}`, ""); !errors.Is(err, ErrBusy) {
		t.Errorf("the first: %v", err)
	}
	stopA()
	if Lookup("over") != b {
		t.Error("the second is no longer found")
	}
}

// TestSourceDecodeEdges checks a codec's failure by line and by body, a
// last line without a newline, and a Content-Type that does not parse.
func TestSourceDecodeEdges(t *testing.T) {
	s := loadSource(t, `{"codec": {"module": "json"}}`)
	recs, err := s.decode(&request{body: []byte("{\"a\": 1}\n{\"a\": 2}")})
	if err != nil || len(recs) != 2 {
		t.Fatalf("%d %v", len(recs), err)
	}
	if _, err := s.decode(&request{body: []byte("{\"a\": 1}\nnope\n")}); err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Errorf("%v", err)
	}
	b := loadSource(t, `{"codec": {"module": "json"}, "framing": "body"}`)
	if _, err := b.decode(&request{body: []byte("{\"a\": 1}\n{\"a\": 2}\n")}); err == nil {
		t.Error("two objects as one body")
	}
	if jsonType("application/json; =") || jsonType("text/csv") || !jsonType("application/problem+json") {
		t.Error("jsonType")
	}
}

// TestSourceProvisionCodec checks a codec that does not load fails the
// source.
func TestSourceProvisionCodec(t *testing.T) {
	if _, err := loadSourceErr(`{"codec": {"module": "csv", "params": {"comma": "ab"}}}`); err == nil || !strings.Contains(err.Error(), "comma") {
		t.Errorf("%v", err)
	}
	if _, err := loadSinkErr(`{"url": "http://h", "codec": {"module": "csv", "params": {"comma": "ab"}}}`); err == nil || !strings.Contains(err.Error(), "comma") {
		t.Errorf("%v", err)
	}
}

// writeCert writes a self-signed certificate for 127.0.0.1 and its key, and
// returns their files.
func writeCert(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "client"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kder, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kder}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

// TestSinkTLS checks a write to an HTTPS endpoint checked against ca_file,
// presenting a client certificate, and one refused without the CA.
func TestSinkTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(srv.Close)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	cert, key := writeCert(t)
	s := loadSink(t, fmt.Sprintf(`{"url": %q, "ca_file": %q, "cert_file": %q, "key_file": %q, "server_name": "example.com"}`, srv.URL, ca, cert, key))
	if err := s.Write(context.Background(), recs(1)); err != nil {
		t.Fatal(err)
	}
	plain := loadSink(t, fmt.Sprintf(`{"url": %q, "timeout": "5s"}`, srv.URL))
	if err := plain.Write(context.Background(), recs(1)); err == nil || fitsync.IsPermanent(err) {
		t.Errorf("an unknown CA: %v", err)
	}
}

// TestSinkProvisionFiles checks TLS files that are missing or hold no PEM,
// and values that would break a header.
func TestSinkProvisionFiles(t *testing.T) {
	notPEM := filepath.Join(t.TempDir(), "x.pem")
	if err := os.WriteFile(notPEM, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HTTPSYNC_BROKEN", "a\nb")
	for params, want := range map[string]string{
		`{"url": "http://h", "ca_file": "/nonexistent/ca.pem"}`:                              "ca_file",
		fmt.Sprintf(`{"url": "http://h", "ca_file": %q}`, notPEM):                            "no PEM",
		fmt.Sprintf(`{"url": "http://h", "cert_file": %q, "key_file": %q}`, notPEM, notPEM):  "cert_file",
		`{"url": "http://h", "headers": {"X-A": "a\nb"}}`:                                    "line break",
		`{"url": "http://h", "headers": {"": "a"}}`:                                          "valid header",
		`{"url": "http://h", "bearer_token_env": "1BAD"}`:                                    "environment variable",
		`{"url": "http://h", "bearer_token_env": "HTTPSYNC_BROKEN"}`:                         "line break",
		`{"url": "http://h", "username": "a:b", "password_env": "HTTPSYNC_BROKEN"}`:          "no ':'",
		`{"url": "http://h", "codec": {"module": "line"}, "content_type": "text/x\r\nX: y"}`: "line break",
	} {
		if _, err := loadSinkErr(params); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", params, err)
		}
	}
}

// TestSinkWriteEdges checks an empty batch sends nothing, a record that
// cannot be encoded fails alone after those before it are sent, the
// failure of those before it is the write's, and an answer's long body is
// clipped in the error.
func TestSinkWriteEdges(t *testing.T) {
	rv := newReceiver(t)
	s := loadSink(t, fmt.Sprintf(`{"url": %q, "max_request_bytes": 100}`, rv.srv.URL))
	if err := s.Write(context.Background(), nil); err != nil || len(rv.requests()) != 0 {
		t.Fatalf("%v, %d requests", err, len(rv.requests()))
	}
	bad := recs(3)
	bad[1].After = fitsync.Map(fitsync.Field{Name: "v", Value: fitsync.Float(math.NaN())})
	err := s.Write(context.Background(), bad)
	var be *fitsync.BatchError
	if !errors.As(err, &be) || be.Index != 1 || !fitsync.IsPermanent(err) || len(rv.requests()) != 1 {
		t.Fatalf("%v, %d requests", err, len(rv.requests()))
	}

	// The records before the bad one, or the large one, fail to send.
	rv.answer = func(int) int { return http.StatusServiceUnavailable }
	if err := s.Write(context.Background(), bad); err == nil || errors.As(err, &be) {
		t.Errorf("before a bad record: %v", err)
	}
	big := recs(3)
	big[1].After = fitsync.Map(fitsync.Field{Name: "v", Value: fitsync.String(strings.Repeat("x", 200))})
	if err := s.Write(context.Background(), big); err == nil || errors.As(err, &be) || fitsync.IsPermanent(err) {
		t.Errorf("before a large record: %v", err)
	}

	long := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, strings.Repeat("e", 1000), http.StatusBadRequest)
	}))
	t.Cleanup(long.Close)
	s = loadSink(t, fmt.Sprintf(`{"url": %q}`, long.URL))
	err = s.Write(context.Background(), recs(1))
	if err == nil || !strings.HasSuffix(err.Error(), "...") || len(err.Error()) > 400 {
		t.Errorf("%v", err)
	}
}

// TestSinkInJob checks a job writes through several writers of the sink at
// once, every record arriving.
func TestSinkInJob(t *testing.T) {
	rv := newReceiver(t)
	src := fmt.Sprintf(`{"sync": {"state_dir": %q, "pipelines": [{"name": "p", "mode": "once", "job": {"max_parallel": 4},
		"source": {"module": "generate", "params": {"records": 200, "chunk_records": 20}},
		"sinks": [{"name": "h", "sink": {"module": "http", "params": {"url": %q}}, "batch": {"max_records": 10, "max_wait": "1ms"}}]}]}}`,
		t.TempDir(), rv.srv.URL)
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
	n := 0
	for _, r := range rv.requests() {
		n += strings.Count(string(r.body), "\n")
	}
	if n < 200 {
		t.Errorf("%d records", n)
	}
	var _ fitsync.ParallelSink = (*Sink)(nil)
	(&Sink{}).ParallelWrites()
}

// TestConnTest sends HEAD with the headers and credentials and no body,
// passing on 2xx and on 4xx but those refusing credentials; the source has
// nothing to test.
func TestConnTest(t *testing.T) {
	ctx := context.Background()
	if err := (&Source{}).Test(ctx); err != nil {
		t.Fatal(err)
	}
	rv := newReceiver(t)
	t.Setenv("FIT_TEST_HTTP_TOKEN", "tok")
	s := loadSink(t, fmt.Sprintf(`{"url": %q, "headers": {"X-Tenant": "t1"}, "bearer_token_env": "FIT_TEST_HTTP_TOKEN"}`, rv.srv.URL))
	for _, c := range []struct {
		status int
		want   string
	}{
		{200, ""}, {405, ""}, {404, ""},
		{401, "answered HEAD 401: the credentials are refused"},
		{403, "answered HEAD 403"},
		{407, "answered HEAD 407"},
		{302, "a redirect"},
		{503, "answered HEAD 503"},
	} {
		rv.mu.Lock()
		rv.answer = func(int) int { return c.status }
		rv.mu.Unlock()
		err := s.Test(ctx)
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%d: %v, want %q", c.status, err, c.want)
		}
	}
	for _, r := range rv.requests() {
		if r.method != http.MethodHead || len(r.body) != 0 || r.key != "" || r.auth != "Bearer tok" || r.tenant != "t1" {
			t.Fatalf("sent %+v", r)
		}
	}
	rv.mu.Lock()
	rv.answer, rv.stall = nil, func(int) bool { return true }
	rv.mu.Unlock()
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if err := s.Test(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("stalled: %v", err)
	}
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	if err := loadSink(t, fmt.Sprintf(`{"url": %q}`, down.URL)).Test(ctx); err == nil || !strings.Contains(err.Error(), strings.TrimPrefix(down.URL, "http://")) {
		t.Errorf("down: %v", err)
	}
}
