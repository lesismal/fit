package fibrest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lesismal/fib"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
)

// connect makes a Connection's client, failing the test on its errors.
func connect(t *testing.T, p Connection) *Client {
	t.Helper()
	c, errs := Connect(&module.Context{Params: config.Value{}}, p)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	t.Cleanup(c.Close)
	return c
}

func TestConnectErrors(t *testing.T) {
	t.Setenv("FIBREST_EMPTY", "")
	for _, tc := range []struct {
		p    Connection
		want string
	}{
		{Connection{}, "no URLs"},
		{Connection{URLs: []string{"ftp://x"}}, "not an http or https URL"},
		{Connection{URLs: []string{"http://x"}, Username: "a:b"}, "no ':'"},
		{Connection{URLs: []string{"http://x"}, Username: "u", BearerTokenEnv: "T"}, "not both"},
		{Connection{URLs: []string{"http://x"}, BearerTokenEnv: "1bad"}, "not an environment variable's name"},
		{Connection{URLs: []string{"http://x"}, PasswordEnv: "P"}, "needs username"},
		{Connection{URLs: []string{"http://x"}, Username: "u", PasswordEnv: "FIBREST_EMPTY"}, "not set"},
		{Connection{URLs: []string{"http://x"}, Headers: map[string]string{"a b": "x"}, HeaderField: "headers"}, "not a header field"},
		{Connection{URLs: []string{"http://x"}, Headers: map[string]string{"Authorization": "x"}, HeaderField: "headers"}, "the connector's to set"},
	} {
		_, errs := Connect(&module.Context{Params: config.Value{}}, tc.p)
		if !strings.Contains(errs.Error(), tc.want) {
			t.Errorf("%+v: %v, want %q", tc.p, errs, tc.want)
		}
	}
}

// TestFailover sends requests through a URL that refuses connections and
// one that answers 503 to a server that answers: the next in turn is
// tried, and the one that answered is used first after.
func TestFailover(t *testing.T) {
	var hits atomic.Int32
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		b, _ := io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(map[string]string{"auth": r.Header.Get("Authorization"), "x": r.Header.Get("X-Fixed"),
			"path": r.URL.Path, "body": string(b)})
	}))
	defer good.Close()
	busy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer busy.Close()
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := "http://" + ln.Addr().String()
	ln.Close()
	t.Setenv("FIBREST_PW", "secret")
	c := connect(t, Connection{URLs: []string{dead, busy.URL, good.URL + "/base/"}, Username: "u", PasswordEnv: "FIBREST_PW",
		Headers: map[string]string{"X-Fixed": "1"}})
	ctx := context.Background()
	resp, err := c.Do(ctx, Request{Method: http.MethodPost, Path: "/p", Body: []byte("hi")})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	_ = json.Unmarshal(resp.Body, &got)
	if got["auth"] != "Basic dTpzZWNyZXQ=" || got["x"] != "1" || got["path"] != "/base/p" || got["body"] != "hi" {
		t.Fatalf("%v", got)
	}
	if !strings.HasPrefix(good.URL, "http://"+resp.Host) || c.Base().Host != strings.TrimPrefix(dead, "http://") {
		t.Fatalf("answered by %s", resp.Host)
	}
	if _, err := c.Do(ctx, Request{Method: http.MethodGet}); err != nil || hits.Load() != 2 {
		t.Fatalf("%v, %d hits", err, hits.Load())
	}
	// Every URL down: the last status, or the last error.
	c2 := connect(t, Connection{URLs: []string{busy.URL}})
	if resp, err := c2.Do(ctx, Request{Method: http.MethodGet}); err != nil || resp.Status != 503 {
		t.Fatalf("%v %v", resp, err)
	}
	c3 := connect(t, Connection{URLs: []string{dead}, BearerTokenEnv: "FIBREST_PW"})
	if _, err := c3.Do(ctx, Request{Method: http.MethodGet}); err == nil {
		t.Fatal("a dead server answered")
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.Do(cctx, Request{Method: http.MethodGet}); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
	none := &Client{}
	if _, err := none.Do(ctx, Request{}); err == nil || none.Base() != nil {
		t.Fatal("a client of no URL sent")
	}
}

// TestRedirects follows a frontend's redirect to a backend, with the
// credentials and the body, as far as the request allows.
func TestRedirects(t *testing.T) {
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_, _ = io.WriteString(w, r.Header.Get("Authorization")+" "+string(b)+" "+r.URL.Path)
	}))
	defer be.Close()
	var loc atomic.Value
	loc.Store(be.URL + "/api/load")
	fe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", loc.Load().(string))
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer fe.Close()
	c := connect(t, Connection{URLs: []string{fe.URL}, Username: "root"})
	ctx := context.Background()
	resp, err := c.Do(ctx, Request{Method: http.MethodPut, Path: "/api/load", Body: []byte("rows"), Redirects: MaxRedirects})
	if err != nil || resp.Status != 200 || string(resp.Body) != "Basic cm9vdDo= rows /api/load" {
		t.Fatalf("%v %v", resp, err)
	}
	if resp, err := c.Do(ctx, Request{Method: http.MethodPut, Body: []byte("rows")}); err != nil || resp.Status != http.StatusTemporaryRedirect {
		t.Fatalf("%v %v", resp, err)
	}
	loc.Store(fe.URL) // a loop: the last redirect is the answer
	if resp, err := c.Do(ctx, Request{Method: http.MethodPut, Body: []byte("rows"), Redirects: 10}); err != nil || resp.Status != http.StatusTemporaryRedirect {
		t.Fatalf("%v %v", resp, err)
	}
	loc.Store("ftp://elsewhere")
	if _, err := c.Do(ctx, Request{Method: http.MethodPut, Body: []byte("rows"), Redirects: 1}); err == nil || !strings.Contains(err.Error(), "not an http") {
		t.Fatalf("%v", err)
	}
}

func TestIsStatus(t *testing.T) {
	err := StatusError("the push", &Response{Status: 400, Body: []byte("bad")})
	if !IsStatus(err, 413, 400) || IsStatus(err, 500) || IsStatus(errors.New("x"), 400) || err.Error() != "the push answered 400: bad" {
		t.Fatal(err)
	}
}

// TestOwnEngineFails: an engine of the package's own that cannot be made
// is the request's error.
func TestOwnEngineFails(t *testing.T) {
	old := newEngine
	defer func() { newEngine = old }()
	newEngine = func(fib.Config, ...fib.Binding) (*fib.Engine, error) { return nil, errors.New("no engine") }
	c := connect(t, Connection{URLs: []string{"http://127.0.0.1:1"}})
	if _, err := c.Do(context.Background(), Request{Method: http.MethodGet}); err == nil || err.Error() != "no engine" {
		t.Fatalf("%v", err)
	}
}
