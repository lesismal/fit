package assistant

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/assistant/internal/fakeapi"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
)

// The secrets the fake Admin API's config holds, which no request to a
// model may carry.
const (
	secretHash  = "$2a$10$abcdefghijklmnopqrstuuJHk7eVYBIJjZD3yR6Ftqk7r0BV9Q1Hq"
	secretToken = "tok-0f9e8d7c6b5a49382716"
	secretJWT   = "jwt-signing-secret-xyz"
	secretPEM   = "-----BEGIN EC PRIVATE KEY-----\nMHcCAQEEIBzKey0123456789abcdef\n-----END EC PRIVATE KEY-----"
	secretLog   = "Bearer abcdefghijklmnop1234"
)

// baseConfig is the config the fake Admin API serves: a route to an
// upstream, and secrets in every place the redaction must find them.
var baseConfig = `{"listeners":[{"name":"web","address":":8080","protocols":["h1"]}],
"routes":[{"name":"api","match":{"path":"/api/*"},"handle":{"handler":"proxy","upstream":"api"},
 "middleware":[{"name":"jwt","params":{"secret":"` + secretJWT + `","header":"Authorization"}}]}],
"upstreams":[{"name":"api","targets":[{"address":"10.0.0.7:9000"}]}],
"llm":{"proxy":{"api":{"address":"127.0.0.1:7000","api_keys":["` + secretToken + `"]}}},
"tls":{"inline_key":` + jsonString(secretPEM) + `},
"admin":{"address":"127.0.0.1:2019","console":{"address":":9000","users":[{"username":"ada","password_hash":"` + secretHash + `"}]},
 "assistant":{"providers":[{"name":"p","kind":"openai"}]}}}`

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// fakeAdmin is the Admin API's endpoints as the tools read them.
func fakeAdmin() http.Handler {
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, ctype, body string) {
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("Fit-Config-Version", "3")
		_, _ = io.WriteString(w, body)
	}
	mux.HandleFunc("GET /config", func(w http.ResponseWriter, r *http.Request) { write(w, "application/json", baseConfig) })
	mux.HandleFunc("GET /config/effective", func(w http.ResponseWriter, r *http.Request) {
		write(w, "application/json", `{"version":3,"config":`+baseConfig+`,"sources":[]}`)
	})
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		write(w, "application/json", `{"version":3,"applied":"2026-11-06T00:00:00Z","source":"file","listeners":[{"name":"web","address":":8080"}],
"connections":{"open":2},"upstreams":[{"name":"api","in_flight":0,"backends":[{"address":"10.0.0.7:9000","up":false,"probed_down":true,"succeeded":10,"failed":7}]},
{"name":"static","in_flight":1,"backends":[{"address":"10.0.0.8:80","up":true}]}],
"certificates":[{"listener":"web","names":["example.com"],"issuer":"R3","not_after":"`+time.Now().Add(240*time.Hour).UTC().Format(time.RFC3339)+`"}]}`)
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		write(w, "text/plain", `# HELP fit_http_requests_total Requests.
# TYPE fit_http_requests_total counter
fit_http_requests_total{listener="web",route="api",code="5xx"} 7
fit_http_requests_total{listener="web",route="static",code="2xx"} 90
fit_http_request_duration_seconds_bucket{listener="web",route="api",le="0.1"} 3
fit_upstream_requests_in_flight{upstream="api"} 0
fit_label_escape{route="a\"b",x="y"} 1
go_goroutines 12
`)
	})
	mux.HandleFunc("GET /cache", func(w http.ResponseWriter, r *http.Request) {
		write(w, "application/json", `[{"zone":"z","entries":3,"hits":9,"misses":1}]`)
	})
	mux.HandleFunc("GET /config/check", func(w http.ResponseWriter, r *http.Request) {
		write(w, "application/json", `{"version":3,"differences":[{"id":"setting:log","category":"setting","fix":"reload"}],"summary":{"drift":1}}`)
	})
	mux.HandleFunc("GET /versions", func(w http.ResponseWriter, r *http.Request) {
		write(w, "application/json", `{"current":3,"versions":[{"version":2},{"version":3}]}`)
	})
	mux.HandleFunc("GET /versions/{n}", func(w http.ResponseWriter, r *http.Request) {
		switch r.PathValue("n") {
		case "2":
			write(w, "application/json", `{"listeners":[{"name":"web","address":":80"}],"routes":[],"old":true,"admin":{"console":{"users":[{"password_hash":"`+secretHash+`"}]}}}`)
		case "3":
			write(w, "application/json", baseConfig)
		default:
			w.WriteHeader(404)
			write(w, "application/json", `{"error":"version 9 is not kept"}`)
		}
	})
	mux.HandleFunc("POST /config/render", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		write(w, "text/plain", r.URL.Query().Get("format")+" of "+string(body))
	})
	return mux
}

// chatFixture is an assistant with tools, a scripted model, and a logged-in
// browser of ada's.
type chatFixture struct {
	*fixture
	model *fakeapi.Server
	store *memStore
	cfg   *config.Config
}

// testSettings are admin.assistant's settings of a chat test, with the
// scripted model's provider at base.
func testSettings(base, extra string) string {
	if extra != "" {
		extra = ", " + extra
	}
	return `{"providers": [{"name": "m", "kind": "openai_compatible", "base_url": "` + base + `", "api_key_env": "FIT_TEST_CHAT_KEY",
		"models": [{"name": "gpt-test"}]}], "data": {"send_logs": true}` + extra + `}`
}

// startChat starts an assistant whose model next answers, with settings
// extra beside the provider, and runtime the fixture's config.
func startChat(t *testing.T, extra string, next func(fakeapi.Request) fakeapi.Turn, opts ...func(*Options)) *chatFixture {
	t.Helper()
	t.Setenv("FIT_TEST_CHAT_KEY", testKey)
	m := fakeapi.StartScripted(t, testKey, next)
	cfg := loadConfig(t, testSettings(m.BaseURL("openai"), extra))
	st := &memStore{}
	f := startWith(t, cfg, st, func(o *Options) {
		o.API = fakeAdmin()
		o.Schema, _ = os.ReadFile("../schema/fit.schema.json")
		o.SchemaZh = func() map[string]string {
			return map[string]string{"How requests are matched, and what handles them.": "请求如何匹配。"}
		}
		o.Logs = testLog(t).Recent
		for _, fn := range opts {
			fn(o)
		}
	})
	return &chatFixture{fixture: f, model: m, store: st, cfg: cfg}
}

// testLog is a log with an error and a secret in its ring.
func testLog(t *testing.T) *logs.Root {
	r, err := logs.New(nil, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	r.Logger(config.LoggerServer).Errorf("upstream api: dial 10.0.0.7:9000: connection refused")
	r.Logger(config.LoggerProvider).Infof("calling with Authorization: %s", secretLog)
	return r
}

func newClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, Timeout: 30 * time.Second}
}

// sse is a chat's stream as the browser read it.
type sse struct {
	status int
	body   string
	events []Event
	end    *Event
	err    string
}

func (s *sse) types() string {
	var out []string
	for _, e := range s.events {
		out = append(out, e.Type)
	}
	return strings.Join(out, ",")
}

func (s *sse) of(typ string) []Event {
	var out []Event
	for _, e := range s.events {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

func (s *sse) text() string {
	var b strings.Builder
	for _, e := range s.of(EventText) {
		b.WriteString(e.Text)
	}
	return b.String()
}

// chat posts body to /api/chat and reads its stream to the end.
func (f *fixture) chat(body string) *sse {
	f.t.Helper()
	return f.chatCtx(context.Background(), body, nil)
}

// chatCtx posts body and reads the stream, handing each event to on.
func (f *fixture) chatCtx(ctx context.Context, body string, on func(Event)) *sse {
	f.t.Helper()
	req, _ := http.NewRequestWithContext(ctx, "POST", f.url+"/api/m/ai/chat", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Fit-Csrf", f.csrf)
	resp, err := f.c.Do(req)
	if err != nil {
		return &sse{err: err.Error()}
	}
	defer resp.Body.Close()
	out := &sse{status: resp.StatusCode}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		b, _ := io.ReadAll(resp.Body)
		out.body = string(b)
		return out
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	name := ""
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data := strings.TrimPrefix(line, "data: ")
			switch name {
			case "end":
				var e Event
				_ = json.Unmarshal([]byte(data), &e)
				out.end = &e
				return out
			case "error":
				out.err = data
				return out
			}
			var e Event
			if err := json.Unmarshal([]byte(data), &e); err != nil {
				f.t.Errorf("an event is not JSON: %s", data)
			}
			out.events = append(out.events, e)
			if on != nil {
				on(e)
			}
		}
	}
	out.err = "the stream ended without its end"
	return out
}

// conversation returns ada's conversation id as the API answers it.
func (f *fixture) conversation(id string) convWire {
	f.t.Helper()
	status, body := f.do("GET", "/api/m/ai/conversations/"+id, "")
	if status != 200 {
		f.t.Fatalf("GET conversation: %d %s", status, body)
	}
	var c convWire
	_ = json.Unmarshal([]byte(body), &c)
	return c
}

// convWire is a conversation as the API sends it.
type convWire struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Messages []Msg  `json:"messages"`
	Summary  string `json:"summary"`
	Folded   int    `json:"folded"`
	Busy     bool   `json:"busy"`
	Ask      *Event `json:"ask"`
}

// script answers each request with the next turn, the last one again past
// the end.
type script struct {
	mu    sync.Mutex
	turns []fakeapi.Turn
	n     int
}

func (s *script) next(fakeapi.Request) fakeapi.Turn {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.turns[min(s.n, len(s.turns)-1)]
	s.n++
	return t
}

// noSecrets fails t when a request to the model carried a secret.
func noSecrets(t *testing.T, reqs []fakeapi.Request) {
	t.Helper()
	for i, r := range reqs {
		for _, s := range []string{secretHash, secretToken, secretJWT, "MHcCAQEEIBzKey0123456789abcdef", "abcdefghijklmnop1234", testKey, "127.0.0.1:2019"} {
			if strings.Contains(string(r.Raw), s) || strings.Contains(string(r.Raw), jsonString(s)[1:len(jsonString(s))-1]) {
				t.Errorf("request %d carries the secret %q", i, s)
			}
		}
	}
}
