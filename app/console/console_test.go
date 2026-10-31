package console

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/lesismal/fib"
	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/config"
)

func startConsole(t *testing.T) (*Console, string) {
	t.Helper()
	engine, err := fib.New(fib.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = engine.Run(); close(done) }()
	t.Cleanup(func() { engine.Stop(); <-done; _ = engine.Close() })
	hash, _ := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	c, err := Start(engine, Options{
		Config: &config.RoleConsole{Address: "127.0.0.1:0", Users: []config.ConsoleUser{{Username: "admin", PasswordHash: string(hash)}}},
		Cookie: "fit_test",
		Pages:  fstest.MapFS{"index.html": {Data: []byte("<p>hi</p>")}, "static/app.js": {Data: []byte("1")}},
		Routes: func(api *API) {
			api.Handle("GET", "/api/now", func(r *Request) { _ = r.JSON(200, map[string]string{"user": r.User}) })
			api.Handle("POST", "/api/echo", func(r *Request) {
				var v map[string]any
				if !r.Decode(&v) {
					return
				}
				r.Go(func() { _ = r.JSON(200, v) })
			})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Stop(context.Background()) })
	return c, strings.TrimSuffix(c.URL(), "/")
}

type browser struct {
	t    *testing.T
	c    *http.Client
	base string
	csrf string
}

func (b *browser) do(method, path, body string) (int, string) {
	b.t.Helper()
	req, _ := http.NewRequest(method, b.base+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if b.csrf != "" {
		req.Header.Set(CSRFHeader, b.csrf)
	}
	resp, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data)
}

func TestConsole(t *testing.T) {
	_, base := startConsole(t)
	jar, _ := cookiejar.New(nil)
	b := &browser{t: t, c: &http.Client{Jar: jar, Timeout: 10 * time.Second}, base: base}

	if code, body := b.do("GET", "/", ""); code != 200 || body != "<p>hi</p>" {
		t.Errorf("index: %d %q", code, body)
	}
	if code, _ := b.do("GET", "/static/app.js", ""); code != 200 {
		t.Errorf("a static file: %d", code)
	}
	if code, _ := b.do("GET", "/nope", ""); code != 404 {
		t.Errorf("a missing file: %d", code)
	}
	if code, _ := b.do("GET", "/api/now", ""); code != 401 {
		t.Errorf("before logging in: %d", code)
	}
	if code, _ := b.do("POST", "/api/login", `{"username":"admin","password":"wrong"}`); code != 401 {
		t.Errorf("a wrong password: %d", code)
	}
	code, body := b.do("POST", "/api/login", `{"username":"admin","password":"secret"}`)
	if code != 200 {
		t.Fatalf("login: %d %s", code, body)
	}
	var s sessionBody
	_ = json.Unmarshal([]byte(body), &s)
	if code, body := b.do("GET", "/api/now", ""); code != 200 || !strings.Contains(body, `"admin"`) {
		t.Errorf("logged in: %d %s", code, body)
	}
	if code, _ := b.do("POST", "/api/echo", `{"a":1}`); code != 403 {
		t.Errorf("without the CSRF token: %d", code)
	}
	b.csrf = s.CSRF
	if code, body := b.do("POST", "/api/echo", `{"a":1}`); code != 200 || body != `{"a":1}`+"\n" && body != `{"a":1}` {
		t.Errorf("echo: %d %q", code, body)
	}
	if code, _ := b.do("POST", "/api/nope", `{}`); code != 404 {
		t.Errorf("no such endpoint: %d", code)
	}
	req, _ := http.NewRequest("GET", base+"/api/now", nil)
	req.Header.Set("Origin", "http://evil.example")
	if resp, err := b.c.Do(req); err != nil || resp.StatusCode != 403 {
		t.Errorf("another origin: %v %v", resp, err)
	} else {
		resp.Body.Close()
	}
	if code, _ := b.do("POST", "/api/logout", ""); code != 204 {
		t.Errorf("logout: %d", code)
	}
	if code, _ := b.do("GET", "/api/now", ""); code != 401 {
		t.Errorf("after logging out: %d", code)
	}
}

func TestConsoleStopClosesConnections(t *testing.T) {
	c, base := startConsole(t)
	cl := &http.Client{Timeout: 5 * time.Second}
	resp, err := cl.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	c.Stop(context.Background())
	if _, err := (&http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}).Get(base + "/"); err == nil {
		t.Error("the console still answers after Stop")
	}
}
