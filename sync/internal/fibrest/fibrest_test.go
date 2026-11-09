package fibrest

import (
	"context"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fib"

	fitsync "github.com/lesismal/fit/sync"
)

func init() { BackoffFirst, BackoffMost = time.Millisecond, 5*time.Millisecond }

func newClient(t *testing.T, p Params) *Client {
	t.Helper()
	c, errs := New(nil, p)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	t.Cleanup(c.Close)
	return c
}

// TestDo sends a request with the client's and the request's headers to
// the base URL's path, and reads the answer.
func TestDo(t *testing.T) {
	var got *http.Request
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		b := make([]byte, 100)
		n, _ := r.Body.Read(b)
		body = string(b[:n])
		w.Header().Set("X-A", "1")
		w.WriteHeader(201)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	c := newClient(t, Params{URL: srv.URL + "/base/", Header: http.Header{"Authorization": {"Token x"}}})
	if c.Base().Path != "/base" {
		t.Fatal(c.Base())
	}
	resp, err := c.Do(context.Background(), Request{Method: "POST", Path: "/p", Query: "a=b", Header: http.Header{"X-B": {"2"}}, Body: []byte("hello")})
	if err != nil || resp.Status != 201 || string(resp.Body) != "ok" || resp.Header.Get("X-A") != "1" {
		t.Fatal(resp, err)
	}
	if got.URL.Path != "/base/p" || got.URL.RawQuery != "a=b" || got.Header.Get("Authorization") != "Token x" || got.Header.Get("X-B") != "2" || body != "hello" {
		t.Fatal(got.URL, got.Header, body)
	}
	if _, err := c.Do(context.Background(), Request{Method: "GET", Path: "/p"}); err != nil || got.ContentLength != 0 {
		t.Fatal(err, got.ContentLength)
	}
	if _, err := c.Do(context.Background(), Request{Method: "BAD METHOD", Path: "/p"}); err == nil {
		t.Fatal("a bad method")
	}
}

// TestDoOnEngine uses the engine it is given, and leaves it running.
func TestDoOnEngine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	cfg := fib.DefaultConfig()
	cfg.LogSettings = false
	e, err := fib.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = e.Run(); close(done) }()
	defer func() { e.Stop(); <-done; _ = e.Close() }()
	c, errs := New(e, Params{URL: srv.URL})
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if resp, err := c.Do(context.Background(), Request{Method: "GET"}); err != nil || resp.Status != 200 {
		t.Fatal(resp, err)
	}
	c.Close()
	c.Close()
}

// TestDoErrors: a connection refused, a context ended.
func TestDoErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(200 * time.Millisecond) }))
	defer srv.Close()
	c := newClient(t, Params{URL: srv.URL, Timeout: time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := c.Do(ctx, Request{Method: "GET"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if _, err := c.DoRetry(ctx, Request{Method: "GET"}, 3); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	dead := newClient(t, Params{URL: "http://127.0.0.1:1"})
	if _, err := dead.Do(context.Background(), Request{Method: "GET"}); err == nil || !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Fatal(err)
	}
	if _, err := dead.DoRetry(context.Background(), Request{Method: "GET"}, 2); err == nil {
		t.Fatal("no error")
	}
}

// TestDoRetry tries again on 429 and 5xx, with Retry-After, up to its
// attempts, and as r.Retry says.
func TestDoRetry(t *testing.T) {
	var n atomic.Int32
	statuses := []int{429, 503, 200}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(n.Add(1)) - 1
		st := statuses[min(i, len(statuses)-1)]
		if st == 429 {
			w.Header().Set("Retry-After", "0")
		}
		w.WriteHeader(st)
	}))
	defer srv.Close()
	c := newClient(t, Params{URL: srv.URL})
	if resp, err := c.DoRetry(context.Background(), Request{Method: "GET"}, 0); err != nil || resp.Status != 200 || n.Load() != 3 {
		t.Fatal(resp, err, n.Load())
	}
	n.Store(0)
	statuses = []int{500}
	if resp, err := c.DoRetry(context.Background(), Request{Method: "GET"}, 2); err != nil || resp.Status != 500 || n.Load() != 2 {
		t.Fatal(resp, err, n.Load())
	}
	n.Store(0)
	if resp, err := c.DoRetry(context.Background(), Request{Method: "GET", Retry: func(*Response) bool { return false }}, 4); err != nil || resp.Status != 500 || n.Load() != 1 {
		t.Fatal(resp, err, n.Load())
	}
	n.Store(0)
	statuses = []int{400, 200}
	if resp, err := c.DoRetry(context.Background(), Request{Method: "GET", Retry: func(r *Response) bool { return r.Status == 400 }}, 4); err != nil || resp.Status != 200 || n.Load() != 2 {
		t.Fatal(resp, err, n.Load())
	}
	// A cancel while waiting between tries.
	n.Store(0)
	statuses = []int{503}
	BackoffFirst, BackoffMost = time.Second, time.Second
	defer func() { BackoffFirst, BackoffMost = time.Millisecond, 5*time.Millisecond }()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.DoRetry(ctx, Request{Method: "GET"}, 5); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestHelpers(t *testing.T) {
	for st, want := range map[int]bool{200: false, 400: false, 408: true, 429: true, 500: true, 503: true} {
		if Transient(st) != want {
			t.Errorf("%d", st)
		}
	}
	if d, ok := retryAfter(" 3 "); !ok || d != 3*time.Second {
		t.Error(d)
	}
	if _, ok := retryAfter("Wed, 21 Oct 2015 07:28:00 GMT"); ok {
		t.Error("a date")
	}
	if _, ok := retryAfter("-1"); ok {
		t.Error("-1")
	}
	if Backoff(0) != BackoffFirst || Backoff(100) != BackoffMost {
		t.Error(Backoff(0), Backoff(100))
	}
	if Clip([]byte("ab")) != "ab" || len(Clip(make([]byte, 400))) != 303 {
		t.Error("clip")
	}
	if err := StatusError("x", &Response{Status: 404, Body: []byte("no")}); err.Error() != "x answered 404: no" {
		t.Error(err)
	}
	err := Refused(errors.New("bad"))
	if !IsRefused(err) || IsRefused(errors.New("bad")) || err.Error() != "bad" || errors.Unwrap(err).Error() != "bad" {
		t.Error(err)
	}
	if (FieldError{"f", errors.New("e")}).Error() != "f: e" {
		t.Error("field error")
	}
}

func TestSecret(t *testing.T) {
	t.Setenv("FIBREST_OK", "v")
	t.Setenv("FIBREST_NL", "a\nb")
	if v, fe := Secret("f", "FIBREST_OK"); fe != nil || v != "v" {
		t.Fatal(v, fe)
	}
	for _, name := range []string{"1BAD", "FIBREST_UNSET", "FIBREST_NL"} {
		if _, fe := Secret("f", name); fe == nil || fe.Field != "f" {
			t.Error(name)
		}
	}
}

// TestNewErrors: bad URLs and TLS files.
func TestNewErrors(t *testing.T) {
	dir := t.TempDir()
	notPEM := filepath.Join(dir, "x.pem")
	_ = os.WriteFile(notPEM, []byte("x"), 0o600)
	for _, c := range []struct {
		p     Params
		field string
	}{
		{Params{URL: "ftp://x"}, "url"},
		{Params{URL: "http://x?a=b"}, "url"},
		{Params{URL: "http://u@x"}, "url"},
		{Params{URL: "http://x", CAFile: filepath.Join(dir, "none")}, "ca_file"},
		{Params{URL: "http://x", CAFile: notPEM}, "ca_file"},
		{Params{URL: "http://x", CertFile: notPEM}, "cert_file"},
		{Params{URL: "http://x", CertFile: notPEM, KeyFile: notPEM}, "cert_file"},
	} {
		_, errs := New(nil, c.p)
		if len(errs) != 1 || errs[0].Field != c.field {
			t.Errorf("%+v: %v", c.p, errs)
		}
	}
}

// TestTLS checks a server's certificate against ca_file, or against the
// one pinned.
func TestTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	der := srv.Certificate().Raw
	ca := filepath.Join(t.TempDir(), "ca.pem")
	_ = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	c := newClient(t, Params{URL: srv.URL, CAFile: ca, ServerName: "example.com"})
	if resp, err := c.Do(context.Background(), Request{Method: "GET"}); err != nil || resp.Status != 200 {
		t.Fatal(resp, err)
	}
	pinned := newClient(t, Params{URL: srv.URL, PinnedCert: der})
	if resp, err := pinned.Do(context.Background(), Request{Method: "GET"}); err != nil || resp.Status != 200 {
		t.Fatal(resp, err)
	}
	other := newClient(t, Params{URL: srv.URL, PinnedCert: []byte("other")})
	if _, err := other.Do(context.Background(), Request{Method: "GET"}); err == nil {
		t.Fatal("a certificate not pinned")
	}
	if err := other.tls.VerifyPeerCertificate(nil, nil); err == nil {
		t.Fatal("no certificate")
	}
	none := newClient(t, Params{URL: srv.URL})
	if _, err := none.Do(context.Background(), Request{Method: "GET"}); err == nil {
		t.Fatal("an unknown CA")
	}
}

// TestWriteFinding writes the records whole, then alone to find the one
// refused.
func TestWriteFinding(t *testing.T) {
	if err := WriteFinding(context.Background(), 0, nil); err != nil {
		t.Fatal(err)
	}
	refuse := map[int]bool{}
	var calls [][2]int
	write := func(_ context.Context, lo, hi int) error {
		calls = append(calls, [2]int{lo, hi})
		for i := lo; i < hi; i++ {
			if refuse[i] {
				return Refused(errors.New("refused"))
			}
		}
		return nil
	}
	if err := WriteFinding(context.Background(), 5, write); err != nil || len(calls) != 1 {
		t.Fatal(err, calls)
	}
	refuse[3], calls = true, nil
	err := WriteFinding(context.Background(), 5, write)
	var be *fitsync.BatchError
	if !errors.As(err, &be) || be.Index != 3 || !fitsync.IsPermanent(err) || len(calls) != 5 {
		t.Fatal(err, calls)
	}
	if err := WriteFinding(context.Background(), 1, func(context.Context, int, int) error { return Refused(errors.New("r")) }); !errors.As(err, &be) || be.Index != 0 || !fitsync.IsPermanent(err) {
		t.Fatal(err)
	}
	// Refused together, taken alone.
	n := 0
	err = WriteFinding(context.Background(), 3, func(_ context.Context, lo, hi int) error {
		n++
		if hi-lo > 1 {
			return Refused(errors.New("too much"))
		}
		return nil
	})
	if err != nil || n != 4 {
		t.Fatal(err, n)
	}
	// Another error alone: the pipeline's to retry, at its record.
	err = WriteFinding(context.Background(), 3, func(_ context.Context, lo, hi int) error {
		if hi-lo > 1 {
			return Refused(errors.New("too much"))
		}
		if lo == 2 {
			return errors.New("down")
		}
		return nil
	})
	if !errors.As(err, &be) || be.Index != 2 || fitsync.IsPermanent(err) {
		t.Fatal(err)
	}
	if err := WriteFinding(context.Background(), 3, func(context.Context, int, int) error { return errors.New("down") }); err == nil || errors.As(err, &be) {
		t.Fatal(err)
	}
}

func TestSleep(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if Sleep(ctx, time.Hour) {
		t.Fatal("slept")
	}
	if !Sleep(context.Background(), time.Millisecond) {
		t.Fatal("did not sleep")
	}
}
