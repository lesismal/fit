package proxy

import (
	"bufio"
	"io"
	"net"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/runtime"
)

// retryConfig is a config whose route proxies to its upstream with params,
// which are proxy params beside upstream.
func retryConfig(upstreamFields, params string) string {
	return `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "upstreams": [{"name": "app", "addresses": [%s]` + upstreamFields + `}],
  "routes": [{"handler": {"module": "proxy", "params": {"upstream": "app"` + params + `}}}]
}`
}

// countingServer answers every request with status and its body, counting
// them.
func countingServer(t *testing.T, status int, body string) (string, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	s := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		hits.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(s.Close)
	return s.Listener.Addr().String(), &hits
}

// refused returns an address nothing listens on.
func refused(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

// resetter reads each request whole, then closes its connection without an
// answer, counting the requests.
func resetter(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	var hits atomic.Int32
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				req, err := stdhttp.ReadRequest(bufio.NewReader(c))
				if err != nil {
					return
				}
				_, _ = io.Copy(io.Discard, req.Body)
				hits.Add(1)
			}()
		}
	}()
	return l.Addr().String(), &hits
}

func do(t *testing.T, method, target, body string) (int, string) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, _ := stdhttp.NewRequest(method, target, rd)
	resp, err := client(false).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(got)
}

// TestRetryConnectFailure checks that a request whose connection could not
// be made goes to the next backend, whatever its method.
func TestRetryConnectFailure(t *testing.T) {
	ok, hits := countingServer(t, 200, "ok")
	// Every request needs its retry: the budget allows one each.
	base := startFit(t, retryConfig(`, "max_fails": 0`, `, "retry": {"budget_ratio": 1}`), refused(t), ok)
	for i := range 10 {
		for _, method := range []string{"GET", "POST"} {
			if code, body := do(t, method, base+"/", "payload"); code != 200 || body != "ok" {
				t.Fatalf("%s %d: %d %q", method, i, code, body)
			}
		}
	}
	if n := hits.Load(); n != 20 {
		t.Errorf("the live backend got %d requests, want 20", n)
	}
}

// TestRetryNotResentOnceSent checks that a request of a method that is not
// idempotent, whose body reached the backend, is not sent again, and that
// an idempotent one is, up to attempts.
func TestRetryNotResentOnceSent(t *testing.T) {
	addr, hits := resetter(t)
	base := startFit(t, retryConfig(`, "max_fails": 0`, `, "retry": {"attempts": 3}`), addr)
	if code, _ := do(t, "POST", base+"/", "charge the card"); code != 502 {
		t.Errorf("POST: %d, want 502", code)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("the POST reached the backend %d times, want once", n)
	}
	hits.Store(0)
	if code, _ := do(t, "PUT", base+"/", "the same each time"); code != 502 {
		t.Errorf("PUT: %d, want 502", code)
	}
	// fib's client sends an idempotent request again once itself when its
	// connection closes under it, so each attempt may reach it twice.
	if n := hits.Load(); n < 4 || n > 8 {
		t.Errorf("the PUT reached the backend %d times, want 4 attempts", n)
	}
}

// TestRetryStatuses checks that statuses retry idempotent requests only, on
// the next backend.
func TestRetryStatuses(t *testing.T) {
	bad, badHits := countingServer(t, 503, "busy")
	good, goodHits := countingServer(t, 200, "ok")
	base := startFit(t, retryConfig(``, `, "retry": {"statuses": [503]}`), bad, good)
	for i := range 10 {
		if code, body := do(t, "GET", base+"/", ""); code != 200 || body != "ok" {
			t.Fatalf("GET %d: %d %q", i, code, body)
		}
	}
	if b, g := badHits.Load(), goodHits.Load(); g != 10 || b < 4 {
		t.Errorf("503 backend %d, 200 backend %d", b, g)
	}
	badHits.Store(0)
	n503 := 0
	for range 10 {
		if code, _ := do(t, "POST", base+"/", "x"); code == 503 {
			n503++
		}
	}
	if b := badHits.Load(); int(b) != n503 || n503 == 0 {
		t.Errorf("POST: %d answered 503, the 503 backend saw %d", n503, b)
	}
}

// TestRetryBudget checks that retries stop at the budget: a fifth of the
// requests, with none a second besides.
func TestRetryBudget(t *testing.T) {
	bad, hits := countingServer(t, 503, "busy")
	base := startFit(t, retryConfig(``, `, "retry": {"statuses": [503], "attempts": 2, "budget_ratio": 0.2, "budget_min_per_second": 0}`), bad)
	const requests = 50
	for range requests {
		if code, _ := do(t, "GET", base+"/", ""); code != 503 {
			t.Fatalf("%d, want 503", code)
		}
	}
	if retries := hits.Load() - requests; retries < 5 || retries > 10 {
		t.Errorf("%d retries for %d requests, want at most 10", retries, requests)
	}
}

// TestRouteTimeout checks the route's timeout: before the response began,
// 504; once it is under way, the response is cut short.
func TestRouteTimeout(t *testing.T) {
	s := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		if r.URL.Path == "/stream" {
			w.Header().Set("Content-Length", "1000000")
			_, _ = io.WriteString(w, strings.Repeat("x", 100000))
			w.(stdhttp.Flusher).Flush()
		}
		select {
		case <-time.After(5 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer s.Close()
	base := startFit(t, retryConfig(``, `, "timeout": "300ms"`), s.Listener.Addr().String())
	start := time.Now()
	if code, _ := do(t, "GET", base+"/slow", ""); code != 504 || time.Since(start) > 2*time.Second {
		t.Errorf("slow: %d after %v", code, time.Since(start))
	}
	start = time.Now()
	resp, err := client(false).Get(base + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	n, err := io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if err == nil || n >= 1000000 || time.Since(start) > 2*time.Second {
		t.Errorf("stream: %d bytes, %v, after %v", n, err, time.Since(start))
	}
}

func TestRetryParams(t *testing.T) {
	if _, err := compileDoc(t, strings.Replace(retryConfig(``, `, "retry": {"statuses": [404]}`), "%s", `"127.0.0.1:1"`, 1)); err == nil || !strings.Contains(err.Error(), "404 is not a 5xx status") {
		t.Errorf("statuses: %v", err)
	}
	if _, err := compileDoc(t, strings.Replace(retryConfig(``, `, "retry": {"budget_ratio": 2}`), "%s", `"127.0.0.1:1"`, 1)); err == nil {
		t.Error("budget_ratio 2: no error")
	}
}

// compileDoc loads and compiles doc with no engine, for its errors.
func compileDoc(t *testing.T, doc string) (*runtime.Graph, error) {
	t.Helper()
	c, err := config.Load([]byte(doc))
	if err != nil {
		return nil, err
	}
	return runtime.Compile(c, runtime.Env{})
}
