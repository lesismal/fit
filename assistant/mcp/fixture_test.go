package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fib"
	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/app/console"
	"github.com/lesismal/fit/assistant"
	"github.com/lesismal/fit/config"
)

// The secrets the fake Admin API's config holds, which no tool result or
// resource may carry.
const (
	secretHash  = "$2a$10$abcdefghijklmnopqrstuuJHk7eVYBIJjZD3yR6Ftqk7r0BV9Q1Hq"
	secretToken = "tok-0f9e8d7c6b5a49382716"
	secretJWT   = "jwt-signing-secret-xyz"
)

var secrets = []string{secretHash, secretToken, secretJWT, "127.0.0.1:2019"}

// baseConfig is the config the fake Admin API serves: it loads, and holds
// secrets where the redaction must find them.
const baseConfig = `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
"routes": [{"name": "api", "match": {"path": ["/api/*"]}, "middleware": [{"module": "headers", "params": {"secret": "` + secretJWT + `", "api_token": "` + secretToken + `"}}],
  "handler": {"module": "respond", "params": {"status": 200, "body": "one"}}}],
"admin": {"address": "127.0.0.1:2019", "console": {"address": "127.0.0.1:9000", "users": [{"username": "ada", "password_hash": "` + secretHash + `"}]}}}`

// fakeAdmin is the Admin API's endpoints as the tools read them.
func fakeAdmin() http.Handler {
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, ctype, body string) {
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("Fit-Config-Version", "3")
		w.Header().Set("ETag", `"v3"`)
		_, _ = io.WriteString(w, body)
	}
	mux.HandleFunc("GET /config", func(w http.ResponseWriter, r *http.Request) { write(w, "application/json", baseConfig) })
	mux.HandleFunc("GET /config/effective", func(w http.ResponseWriter, r *http.Request) {
		write(w, "application/json", `{"version":3,"config":`+baseConfig+`,"sources":[]}`)
	})
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		write(w, "application/json", `{"version":3,"source":"file","listeners":[{"name":"web","address":":8080"}],"connections":{"open":2}}`)
	})
	mux.HandleFunc("GET /versions", func(w http.ResponseWriter, r *http.Request) {
		write(w, "application/json", `{"current":3,"versions":[{"version":2},{"version":3}]}`)
	})
	mux.HandleFunc("GET /versions/{n}", func(w http.ResponseWriter, r *http.Request) {
		switch r.PathValue("n") {
		case "2":
			write(w, "application/json", `{"listeners":[],"admin":{"console":{"users":[{"username":"ada","password_hash":"`+secretHash+`"}]}}}`)
		case "3":
			write(w, "application/json", baseConfig)
		default:
			w.WriteHeader(404)
			write(w, "application/json", `{"error":"version `+r.PathValue("n")+` is not kept"}`)
		}
	})
	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, r *http.Request) {})
	mux.HandleFunc("POST /config/render", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		write(w, "text/plain", r.URL.Query().Get("format")+" of "+string(body))
	})
	return mux
}

// memStore keeps sections in memory; fail makes its writes fail.
type memStore struct {
	mu   sync.Mutex
	sec  map[string][]byte
	fail bool
}

func (s *memStore) Section(name string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sec[name]
}

func (s *memStore) SetSection(name string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return errors.New("the disk is full")
	}
	if s.sec == nil {
		s.sec = map[string][]byte{}
	}
	s.sec[name] = append([]byte(nil), data...)
	return nil
}

func (s *memStore) setFail(on bool) {
	s.mu.Lock()
	s.fail = on
	s.mu.Unlock()
}

func engine(t testing.TB) *fib.Engine {
	t.Helper()
	e, err := fib.New(fib.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = e.Run(); close(done) }()
	t.Cleanup(func() { e.Stop(); <-done; _ = e.Close() })
	return e
}

// fixture is an assistant and its MCP server, served by a console, and a
// logged-in browser of ada's.
type fixture struct {
	t     *testing.T
	a     *assistant.Assistant
	s     *Server
	store *memStore
	url   string
	c     *http.Client
	csrf  string

	mu   sync.Mutex
	logs []string
}

// settings returns admin.assistant's settings with mcp, extra beside.
func settings(mcp, extra string) string {
	if extra != "" {
		extra = ", " + extra
	}
	return `{"users": [{"username": "ada", "level": "apply"}, {"username": "bob", "level": "read"}], "mcp": ` + mcp + extra + `}`
}

func loadConfig(t *testing.T, assistantJSON string) *config.Config {
	t.Helper()
	hash, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	cfg, err := config.Load([]byte(`{"admin": {"console": {"users": [{"username": "ada", "password_hash": "` + string(hash) +
		`"}, {"username": "bob", "password_hash": "` + string(hash) + `"}, {"username": "cy", "password_hash": "` + string(hash) +
		`"}]}, "assistant": ` + assistantJSON + `}}`))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// start serves an assistant of settings s with an MCP server, the store
// st (none when nil), and opts: funcs changing the server's options, and
// tools added to the assistant's.
func start(t *testing.T, s string, st *memStore, opts ...any) *fixture {
	t.Helper()
	cfg := loadConfig(t, s)
	f := &fixture{t: t, store: st}
	e := engine(t)
	schema, _ := os.ReadFile("../../schema/fit.schema.json")
	ao := assistant.Options{Engine: e, Settings: cfg.Admin.Assistant, Console: cfg.Admin.Console, Config: func() *config.Config { return cfg },
		Logf: f.logf, API: fakeAdmin(), Schema: schema}
	if st != nil {
		ao.Store = st
	}
	for _, op := range opts {
		if tools, ok := op.([]*assistant.Tool); ok {
			ao.Tools = append(ao.Tools, tools...)
		}
	}
	var err error
	if f.a, err = assistant.New(ao); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.a.Close)
	var c *console.Console
	o := Options{Assistant: f.a, HasUser: func(u string) bool { return u != "gone" },
		Docs: func() ([]byte, error) {
			return []byte("# Reference\n\nAll of it.\n\n## Listeners\n\nWhere fit listens.\n\n```\n## not a title\n```\n\n## Media auth\n\nWho plays.\n"), nil
		},
		URL: func() string { return c.URL() }, Logf: f.logf}
	if st != nil {
		o.Store = st
	}
	for _, op := range opts {
		if fn, ok := op.(func(*Options)); ok {
			fn(&o)
		}
	}
	f.s = New(o)
	f.a.Extend(f.s.Routes)
	rc := &config.RoleConsole{Address: "127.0.0.1:0", Users: cfg.Admin.Console.Users}
	c, err = console.Start(e, console.Options{Config: rc, Cookie: "fit_test", Logf: f.logf,
		Modules:   func() []*console.Module { return []*console.Module{f.a.Module()} },
		Endpoints: map[string]console.Handler{Path: f.s.Serve}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Stop(context.Background()) })
	f.url = strings.TrimSuffix(c.URL(), "/")
	f.c, f.csrf = f.login("ada")
	return f
}

func (f *fixture) logf(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logs = append(f.logs, format)
}

// logged returns what was logged, a line each.
func (f *fixture) logged() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.logs, "\n")
}

// login logs user in, and returns the browser and its CSRF token.
func (f *fixture) login(user string) (*http.Client, string) {
	f.t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, Timeout: 20 * time.Second}
	resp, err := c.Post(f.url+"/api/login", "application/json", strings.NewReader(`{"username":"`+user+`","password":"pw"}`))
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	var s struct{ CSRF string }
	_ = json.NewDecoder(resp.Body).Decode(&s)
	if resp.StatusCode != 200 {
		f.t.Fatalf("login %s: %d", user, resp.StatusCode)
	}
	return c, s.CSRF
}

// page calls the console's API as the browser c with csrf.
func (f *fixture) page(c *http.Client, csrf, method, path, body string) (int, string) {
	f.t.Helper()
	req, _ := http.NewRequest(method, f.url+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set(console.CSRFHeader, csrf)
	resp, err := c.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// do calls the console's API as ada.
func (f *fixture) do(method, path, body string) (int, string) {
	f.t.Helper()
	return f.page(f.c, f.csrf, method, path, body)
}

// token makes a token as the browser c, and returns it.
func (f *fixture) tokenAs(c *http.Client, csrf, body string) string {
	f.t.Helper()
	status, out := f.page(c, csrf, "POST", "/api/m/ai/mcp/tokens", body)
	if status != 200 {
		f.t.Fatalf("POST token: %d %s", status, out)
	}
	var b createdBody
	_ = json.Unmarshal([]byte(out), &b)
	return b.Token
}

// token makes a token of ada's named name at level.
func (f *fixture) token(name, level string) string {
	f.t.Helper()
	return f.tokenAs(f.c, f.csrf, `{"name":"`+name+`","level":"`+level+`","days":30}`)
}

// answer is an answer of the MCP endpoint.
type answer struct {
	status  int
	header  http.Header
	body    string
	sse     bool
	msg     map[string]any // the JSON-RPC response
	comment int            // the SSE comments before it
}

// result returns the response's result.
func (a *answer) result() map[string]any {
	r, _ := a.msg["result"].(map[string]any)
	return r
}

// errCode returns the response's error code, 0 for none.
func (a *answer) errCode() int {
	e, _ := a.msg["error"].(map[string]any)
	if e == nil {
		return 0
	}
	c, _ := e["code"].(float64)
	return int(c)
}

// text returns a tools/call result's text, its blocks joined.
func (a *answer) text() string {
	var b strings.Builder
	for _, c := range asList(a.result()["content"]) {
		m, _ := c.(map[string]any)
		s, _ := m["text"].(string)
		b.WriteString(s)
	}
	return b.String()
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

// post sends body to the endpoint with token and headers.
func (f *fixture) post(token, body string, headers map[string]string) *answer {
	f.t.Helper()
	return f.send(context.Background(), "POST", token, body, headers)
}

func (f *fixture) send(ctx context.Context, method, token, body string, headers map[string]string) *answer {
	f.t.Helper()
	req, _ := http.NewRequestWithContext(ctx, method, f.url+Path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return &answer{body: err.Error()}
	}
	defer resp.Body.Close()
	a := &answer{status: resp.StatusCode, header: resp.Header}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		a.sse = true
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 64<<10), 4<<20)
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, ":"):
				a.comment++
			case strings.HasPrefix(line, "data: "):
				a.body = strings.TrimPrefix(line, "data: ")
			}
		}
	} else {
		b, _ := io.ReadAll(resp.Body)
		a.body = string(b)
	}
	_ = json.Unmarshal([]byte(a.body), &a.msg)
	return a
}

// modern sends a request of the modern revision, its headers as a client
// sets them.
func (f *fixture) modern(token, method string, params map[string]any) *answer {
	f.t.Helper()
	body, headers := modernRequest(method, params)
	return f.post(token, body, headers)
}

func modernRequest(method string, params map[string]any) (string, map[string]string) {
	if params == nil {
		params = map[string]any{}
	}
	params["_meta"] = map[string]any{metaProtocolVersion: versionModern, "io.modelcontextprotocol/clientInfo": map[string]string{"name": "test", "version": "1"},
		"io.modelcontextprotocol/clientCapabilities": map[string]any{}}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	h := map[string]string{headerVersion: versionModern, headerMethod: method}
	if n, ok := params["name"].(string); ok {
		h[headerName] = n
	}
	if u, ok := params["uri"].(string); ok {
		h[headerName] = u
	}
	return string(b), h
}

// legacy opens a session of version v with token, and returns its ID.
func (f *fixture) legacy(token, v string) string {
	f.t.Helper()
	a := f.post(token, `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"`+v+`","capabilities":{},"clientInfo":{"name":"old","version":"1"}}}`, nil)
	if a.status != 200 || a.header.Get(headerSession) == "" {
		f.t.Fatalf("initialize: %d %s", a.status, a.body)
	}
	return a.header.Get(headerSession)
}

// inSession sends a request of a legacy session.
func (f *fixture) inSession(token, sid, v, method, params string) *answer {
	f.t.Helper()
	if params == "" {
		params = "{}"
	}
	return f.post(token, `{"jsonrpc":"2.0","id":7,"method":"`+method+`","params":`+params+`}`, map[string]string{headerSession: sid, headerVersion: v})
}

// noSecret fails the test when s holds a secret.
func noSecret(t *testing.T, what, s string) {
	t.Helper()
	for _, sec := range secrets {
		if strings.Contains(s, sec) || strings.Contains(s, strings.ReplaceAll(sec, "$", `$`)) {
			t.Errorf("%s holds the secret %q", what, sec)
		}
	}
}
