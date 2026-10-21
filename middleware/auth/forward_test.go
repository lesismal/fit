package auth

import (
	"context"
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	_ "github.com/lesismal/fit/handler/respond"
	"github.com/lesismal/fit/module"
	"github.com/lesismal/fit/runtime"
)

func init() {
	module.Register(module.Info{ID: module.Handlers + ".test_fields", New: func() any { return new(fieldsHandler) }})
}

// fieldsHandler answers with the fields forward_auth copies.
type fieldsHandler struct{}

func (fieldsHandler) ServeHTTP(c *fibhttp.Context) {
	h := c.Request.Header
	_ = c.Respond(200, "text/plain", []byte("user="+h.Get("X-Auth-User")+" seen="+h.Get("X-Auth-Seen")))
}

// authService is an auth service for forward_auth's tests, which answers by
// the request's Authorization field.
type authService struct {
	*httptest.Server
	cancelled atomic.Int32 // requests whose client went before the answer
}

func newAuthService(t *testing.T) *authService {
	a := &authService{}
	a.Server = httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		seen := strings.Join([]string{r.Method, r.URL.RequestURI(), r.Header.Get("X-Forwarded-Method"), r.Header.Get("X-Forwarded-Proto"),
			r.Header.Get("X-Forwarded-Host"), r.Header.Get("X-Forwarded-Uri"), r.Header.Get("X-Forwarded-For"), r.Header.Get("X-Custom")}, " ")
		switch auth := r.Header.Get("Authorization"); auth {
		case "Bearer good":
			w.Header().Set("X-Auth-User", "alice")
			w.Header().Set("X-Auth-Seen", seen)
			w.Header().Set("X-Not-Copied", "1")
			_, _ = io.WriteString(w, "ignored")
		case "Bearer nouser":
			w.WriteHeader(204)
		case "Bearer slow", "Bearer hang":
			d := 500 * time.Millisecond
			if auth == "Bearer hang" {
				d = 10 * time.Second
			}
			select {
			case <-time.After(d):
				w.Header().Set("X-Auth-User", "slow")
			case <-r.Context().Done():
				a.cancelled.Add(1)
			}
		case "Bearer big":
			w.WriteHeader(401)
			_, _ = io.WriteString(w, strings.Repeat("x", 200<<10))
		default:
			w.Header().Set("Location", "/login")
			w.Header().Set("Set-Cookie", "next=/p")
			w.WriteHeader(302)
			_, _ = io.WriteString(w, "go log in")
		}
	}))
	t.Cleanup(a.Close)
	return a
}

func forwardDoc(authAddr, timeout string) string {
	return fmt.Sprintf(`{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "upstreams": [{"name": "auth", "addresses": [%q]}],
  "routes": [
    {"match": {"path": ["/fast"]}, "handler": {"module": "respond", "params": {"body": "fast"}}},
    {"middleware": [{"module": "forward_auth", "params": {"upstream": "auth", "uri": "/check?app=1",
        "copy_headers": ["x-auth-user", "X-Auth-Seen"], "timeout": %q}}],
      "handler": {"module": "test_fields"}}
  ]
}`, authAddr, timeout)
}

type answer struct {
	code   int
	header stdhttp.Header
	body   string
}

func fetch(t *testing.T, ctx context.Context, method, url, auth string, header ...string) (answer, error) {
	t.Helper()
	req, _ := stdhttp.NewRequestWithContext(ctx, method, url, nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	for i := 0; i < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	client := &stdhttp.Client{Timeout: 10 * time.Second, CheckRedirect: func(*stdhttp.Request, []*stdhttp.Request) error { return stdhttp.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return answer{}, err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return answer{res.StatusCode, res.Header, string(body)}, nil
}

func TestForwardAuth(t *testing.T) {
	a := newAuthService(t)
	base := "http://" + serve(t, forwardDoc(a.Listener.Addr().String(), "300ms")).Addr.String()
	ctx := context.Background()

	got, err := fetch(t, ctx, "POST", base+"/p?q=1", "Bearer good", "X-Custom", "c", "Host", "x")
	host := strings.TrimPrefix(base, "http://")
	want := "user=alice seen=GET /check?app=1 POST http " + host + " /p?q=1 127.0.0.1 c"
	if err != nil || got.code != 200 || got.body != want {
		t.Errorf("allowed: %v %d %q, want %q", err, got.code, got.body, want)
	}

	// Fields the client sends in copy_headers' names are not believed.
	got, err = fetch(t, ctx, "GET", base+"/p", "Bearer nouser", "X-Auth-User", "mallory")
	if err != nil || got.code != 200 || got.body != "user= seen=" {
		t.Errorf("allowed with no user: %v %d %q", err, got.code, got.body)
	}

	got, err = fetch(t, ctx, "GET", base+"/p", "Bearer bad")
	if err != nil || got.code != 302 || got.header.Get("Location") != "/login" || got.header.Get("Set-Cookie") != "next=/p" || got.body != "go log in" {
		t.Errorf("denied: %v %+v", err, got)
	}
	got, err = fetch(t, ctx, "GET", base+"/p", "Bearer big")
	if err != nil || got.code != 401 || got.body != "" {
		t.Errorf("denied with a large body: %v %d %d bytes", err, got.code, len(got.body))
	}
	got, err = fetch(t, ctx, "GET", base+"/p", "Bearer hang")
	if err != nil || got.code != 504 {
		t.Errorf("past the timeout: %v %d", err, got.code)
	}
}

func TestForwardAuthUnreachable(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	base := "http://" + serve(t, forwardDoc(addr, "5s")).Addr.String()
	if got, err := fetch(t, context.Background(), "GET", base+"/p", "Bearer good"); err != nil || got.code != 502 {
		t.Errorf("no auth service: %v %d", err, got.code)
	}
}

// TestForwardAuthClientGone checks that the subrequest is abandoned when
// the client goes.
func TestForwardAuthClientGone(t *testing.T) {
	a := newAuthService(t)
	base := "http://" + serve(t, forwardDoc(a.Listener.Addr().String(), "30s")).Addr.String()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := fetch(t, ctx, "GET", base+"/p", "Bearer hang"); err == nil {
		t.Fatal("answered")
	}
	deadline := time.Now().Add(5 * time.Second)
	for a.cancelled.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the auth service's request was not abandoned")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestForwardAuthSlowService sends many requests at once to a route whose
// auth service takes half a second, more than the server has workers, and
// checks that another route answers at once meanwhile: the waiting requests
// hold no worker.
func TestForwardAuthSlowService(t *testing.T) {
	a := newAuthService(t)
	base := "http://" + serve(t, forwardDoc(a.Listener.Addr().String(), "10s")).Addr.String()
	const waiting = 128
	var wg sync.WaitGroup
	var allowed atomic.Int32
	start := time.Now()
	for range waiting {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got, err := fetch(t, context.Background(), "GET", base+"/p", "Bearer slow"); err == nil && got.code == 200 && got.body == "user=slow seen=" {
				allowed.Add(1)
			}
		}()
	}
	time.Sleep(100 * time.Millisecond) // the slow ones are waiting
	for i := range 10 {
		t0 := time.Now()
		got, err := fetch(t, context.Background(), "GET", base+"/fast", "")
		if d := time.Since(t0); err != nil || got.code != 200 || d > 250*time.Millisecond {
			t.Errorf("fast request %d: %v %d after %v", i, err, got.code, d)
		}
	}
	wg.Wait()
	if n := allowed.Load(); n != waiting {
		t.Errorf("%d of %d slow requests let in", n, waiting)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("the slow requests took %v: they were not waited for at once", d)
	}
}

func TestForwardAuthParams(t *testing.T) {
	for _, tt := range []struct{ params, err string }{
		{`{"upstream": "nope"}`, `there is no upstream named "nope"`},
		{`{"upstream": "auth", "uri": "check"}`, "not a path"},
		{`{"upstream": "auth", "uri": "http://a/check"}`, "not a path"},
		{`{"upstream": "auth", "request_headers": ["a b"]}`, `request_headers: "a b" is not a field name`},
		{`{"upstream": "auth", "copy_headers": ["x:y"]}`, `copy_headers: "x:y" is not a field name`},
	} {
		c, err := config.Load([]byte(`{"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "upstreams": [{"name": "auth", "addresses": ["127.0.0.1:1"]}],
  "routes": [{"middleware": [{"module": "forward_auth", "params": ` + tt.params + `}], "handler": {"module": "respond"}}]}`))
		if err == nil {
			_, err = runtime.Compile(c, runtime.Env{})
		}
		if err == nil || !strings.Contains(err.Error(), tt.err) {
			t.Errorf("%s: %v, want %q", tt.params, err, tt.err)
		}
	}
}
