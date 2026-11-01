package pebbletest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/internal/certtest"
)

// stopped is a testing.TB whose Fatal and Skip are recorded, and end the
// goroutine they are called on, as testing's own do; the rest is t's.
type stopped struct {
	testing.TB
	msg     string
	skipped bool
}

func (s *stopped) Fatal(args ...any) { s.msg = fmt.Sprint(args...); runtime.Goexit() }
func (s *stopped) Fatalf(format string, args ...any) {
	s.msg = fmt.Sprintf(format, args...)
	runtime.Goexit()
}
func (s *stopped) Skipf(format string, args ...any) {
	s.msg, s.skipped = fmt.Sprintf(format, args...), true
	runtime.Goexit()
}

// stops runs fn on a goroutine of its own with a stopped of t, and returns
// what stopped it, if anything did.
func stops(t *testing.T, fn func(tb testing.TB)) *stopped {
	s := &stopped{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn(s)
	}()
	<-done
	return s
}

// TestBinaryMissing checks a test without the binaries is skipped, or
// fails when they are required.
func TestBinaryMissing(t *testing.T) {
	t.Setenv("FIT_PEBBLE_BIN", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	t.Setenv("FIT_PEBBLE_REQUIRED", "")
	if s := stops(t, func(tb testing.TB) { binary(tb, "pebble") }); !s.skipped || !strings.Contains(s.msg, "not found") {
		t.Errorf("not required: %+v", s)
	}
	if s := stops(t, func(tb testing.TB) { Start(tb, Options{}) }); !s.skipped {
		t.Errorf("Start: %+v", s)
	}
	t.Setenv("FIT_PEBBLE_REQUIRED", "1")
	if s := stops(t, func(tb testing.TB) { binary(tb, "pebble") }); s.skipped || !strings.Contains(s.msg, "FIT_PEBBLE_REQUIRED is set") {
		t.Errorf("required: %+v", s)
	}
}

// TestRunMissing checks a command that does not start fails the test.
func TestRunMissing(t *testing.T) {
	if s := stops(t, func(tb testing.TB) { run(tb, filepath.Join(t.TempDir(), "none"), nil) }); s.msg == "" || s.skipped {
		t.Errorf("%+v", s)
	}
}

// TestFreePortExhausted checks FreePort fails once every port has been
// handed out.
func TestFreePortExhausted(t *testing.T) {
	handed.Lock()
	saved := handed.ports
	handed.ports = map[int]bool{}
	for p := range 1 << 16 {
		handed.ports[p] = true
	}
	handed.Unlock()
	defer func() {
		handed.Lock()
		handed.ports = saved
		handed.Unlock()
	}()
	if s := stops(t, func(tb testing.TB) { FreePort(tb) }); !strings.Contains(s.msg, "no free port") {
		t.Errorf("%+v", s)
	}
}

// TestManagement checks the calls to Pebble's management API, against a
// server that stands in for it: their answers, and their failures.
func TestManagement(t *testing.T) {
	cert, err := certtest.Write(t.TempDir(), "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	pemRoot := "-----BEGIN CERTIFICATE-----\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/set-renewal-info/":
		case "/roots/0":
			http.ServeFile(w, r, cert.CertFile)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	p := &Pebble{mgmt: srv.URL, client: srv.Client()}
	if s := stops(t, func(tb testing.TB) { p.SetRenewalInfo(tb, []byte("cert"), time.Now(), time.Now()) }); s.msg != "" {
		t.Errorf("set-renewal-info: %s", s.msg)
	}
	if s := stops(t, func(tb testing.TB) {
		if p.Roots(tb) == nil {
			tb.Fatal("no roots")
		}
	}); s.msg != "" {
		t.Errorf("roots: %s", s.msg)
	}

	// A management API that answers what Pebble's does not.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/roots/0" {
			_, _ = w.Write([]byte(pemRoot))
			return
		}
		http.Error(w, "no", http.StatusBadRequest)
	}))
	defer bad.Close()
	p = &Pebble{mgmt: bad.URL, client: bad.Client()}
	if s := stops(t, func(tb testing.TB) { p.SetRenewalInfo(tb, []byte("cert"), time.Now(), time.Now()) }); !strings.Contains(s.msg, "400") {
		t.Errorf("set-renewal-info refused: %q", s.msg)
	}
	if s := stops(t, func(tb testing.TB) { p.Roots(tb) }); !strings.Contains(s.msg, "not PEM") {
		t.Errorf("roots not PEM: %q", s.msg)
	}

	// One whose body is cut short.
	cut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("short"))
	}))
	defer cut.Close()
	p = &Pebble{mgmt: cut.URL, client: cut.Client()}
	if s := stops(t, func(tb testing.TB) { p.Roots(tb) }); s.msg == "" || strings.Contains(s.msg, "not PEM") {
		t.Errorf("roots cut short: %q", s.msg)
	}

	// One that is gone.
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	p = &Pebble{mgmt: gone.URL, client: &http.Client{Timeout: 5 * time.Second}}
	if s := stops(t, func(tb testing.TB) { p.SetRenewalInfo(tb, nil, time.Now(), time.Now()) }); s.msg == "" {
		t.Error("set-renewal-info to no server")
	}
	if s := stops(t, func(tb testing.TB) { p.Roots(tb) }); s.msg == "" {
		t.Error("roots from no server")
	}
}

// TestNeverUp checks a Pebble that never answers fails the test, once it
// has had its time: binaries on PATH that only sleep stand in for it.
func TestNeverUp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-ins are shell scripts")
	}
	dir := t.TempDir()
	for _, name := range []string{"pebble", "pebble-challtestsrv"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexec sleep 60\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("FIT_PEBBLE_BIN", "")
	t.Setenv("PATH", dir)
	if s := stops(t, func(tb testing.TB) { Start(tb, Options{}) }); !strings.Contains(s.msg, "did not come up") {
		t.Errorf("%+v", s)
	}
}
