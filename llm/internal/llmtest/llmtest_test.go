package agenttest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"

	_ "github.com/lesismal/fit/agent/hub"
	_ "github.com/lesismal/fit/handler/respond"
)

// fatalTB is a testing.TB whose Fatal ends only the goroutine it runs on,
// so that a test can check what a helper fails with.
type fatalTB struct {
	testing.TB
	mu  sync.Mutex
	msg string
}

func (f *fatalTB) Helper() {}

func (f *fatalTB) Fatal(args ...any) { f.fail(fmt.Sprint(args...)) }

func (f *fatalTB) Fatalf(format string, args ...any) { f.fail(fmt.Sprintf(format, args...)) }

func (f *fatalTB) fail(msg string) {
	f.mu.Lock()
	f.msg = msg
	f.mu.Unlock()
	goruntime.Goexit()
}

// fatalOf runs fn with a fatalTB and returns what it failed with, "" for
// nothing.
func fatalOf(t *testing.T, fn func(tb testing.TB)) string {
	t.Helper()
	f := &fatalTB{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn(f)
	}()
	<-done
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.msg
}

func web(addr string) string {
	return `{"listeners": [{"name": "web", "address": "` + addr + `"}], "routes": [{"handler": {"module": "respond"}}]}`
}

func TestStart(t *testing.T) {
	f := Start(t, web("127.0.0.1:0"), "")
	if !strings.Contains(string(f.Current()), `"web"`) || f.Role("hub") != nil {
		t.Fatalf("current = %s, hub = %v", f.Current(), f.Role("hub"))
	}
	f.Stop()
	f.Stop() // and again, as the cleanup does

	// A store keeps the config: the second start ignores its base.
	path := filepath.Join(t.TempDir(), "fit.store")
	f = Start(t, web("127.0.0.1:0"), path)
	if f.Store == nil {
		t.Fatal("no store")
	}
	f.Stop()
	f = Start(t, `{"not": "read"}`, path)
	if !strings.Contains(string(f.Current()), `"web"`) {
		t.Fatalf("kept config = %s", f.Current())
	}
}

func TestConsole(t *testing.T) {
	if c := Console(); !strings.Contains(c, `"address": "127.0.0.1:0"`) || !strings.Contains(c, `"username": "`+User+`"`) {
		t.Fatalf("console = %s", c)
	}
}

func TestStartFails(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	garbage := filepath.Join(t.TempDir(), "garbage.store")
	if err := os.WriteFile(garbage, []byte("not a store"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, base, store, want string
	}{
		{"bad JSON", "{", "", "JSON"},
		{"compile", `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
			"routes": [{"handler": {"module": "nope"}}]}`, "", `no module "nope"`},
		{"listen", web(busy.Addr().String()), "", "listen"},
		{"apply", `{"agent": {"hub": {"console": {"address": "127.0.0.1:0",
			"users": [{"username": "a", "password_hash": "` + hash + `"}], "cert_file": "/missing/c", "key_file": "/missing/k"}}}}`, "", "console"},
		{"store", web("127.0.0.1:0"), garbage, "store"},
		{"too big for the store", strings.Repeat(" ", 64<<20+1), filepath.Join(t.TempDir(), "big.store"), "larger"},
	} {
		t.Run(c.name, func(t *testing.T) {
			msg := fatalOf(t, func(tb testing.TB) { Start(tb, c.base, c.store) })
			if !strings.Contains(msg, c.want) {
				t.Fatalf("failed with %q, want %q", msg, c.want)
			}
		})
	}
}

func TestBrowser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/login":
			if r.Header.Get("Content-Type") != "application/json" {
				w.WriteHeader(400)
				return
			}
			_, _ = io.WriteString(w, `{"csrf": "tok"}`)
		case "/api/rpc/ok":
			if r.Header.Get("X-Fit-Csrf") != "tok" {
				w.WriteHeader(403)
				return
			}
			_, _ = io.WriteString(w, `{"n": 1}`)
		default:
			w.WriteHeader(409)
			_, _ = io.WriteString(w, `{"error": "taken", "code": 3001}`)
		}
	}))
	defer srv.Close()
	b := Login(t, srv.URL+"/")
	var out struct{ N int }
	if err := b.RPC("/ok", map[string]int{"a": 1}, &out); err != nil || out.N != 1 {
		t.Fatalf("ok: %+v %v", out, err)
	}
	if err := b.RPC("/ok", nil, nil); err != nil {
		t.Fatalf("ok without out: %v", err)
	}
	err := b.RPC("/taken", nil, nil)
	if CodeOf(err) != 3001 || err.Error() != "taken" {
		t.Fatalf("taken: %v", err)
	}
	if err := b.WithoutCSRF().RPC("/ok", nil, nil); CodeOf(err) != 0 || err.(*RPCError).Status != 403 {
		t.Fatalf("without CSRF: %v", err)
	}
	if CodeOf(errors.New("plain")) != 0 || CodeOf(nil) != 0 {
		t.Fatal("a plain error has a code")
	}

	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(401) }))
	defer down.Close()
	if msg := fatalOf(t, func(tb testing.TB) { Login(tb, down.URL) }); !strings.Contains(msg, "login: 401") {
		t.Fatalf("failed login: %q", msg)
	}
	gone := down.URL
	down.Close()
	if msg := fatalOf(t, func(tb testing.TB) { Login(tb, gone) }); msg == "" {
		t.Fatal("no failure from a closed server")
	}
}

func TestEventually(t *testing.T) {
	t.Parallel()
	n := 0
	Eventually(t, "the third try", func() bool { n++; return n == 3 })
	if msg := fatalOf(t, func(tb testing.TB) { Eventually(tb, "never", func() bool { return false }) }); msg != "timeout waiting for never" {
		t.Fatalf("failed with %q", msg)
	}
}

// post calls the mock, with ctx.
func post(ctx context.Context, t *testing.T, url, key, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func call(t *testing.T, url, key, body string) (int, string) {
	t.Helper()
	resp := post(context.Background(), t, url, key, body)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestMockLLM(t *testing.T) {
	m := StartMockLLM(t)
	base := m.BaseURL("a")

	get := func(key string) int {
		req, _ := http.NewRequest("GET", base+"/models", nil)
		req.Header.Set("X-Api-Key", key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if get("sk-a") != 200 || get("sk-b") != 401 {
		t.Fatal("models: the key is not checked")
	}
	m.Down("a", true)
	if get("sk-a") != 503 {
		t.Fatal("models: a tag down answers")
	}
	if status, _ := call(t, base+"/chat/completions", "sk-a", `{}`); status != 503 {
		t.Fatalf("call to a tag down: %d", status)
	}
	m.Down("a", false)
	if st := m.Stats("a"); st.Checks != 3 || st.Calls != 1 || st.Down {
		t.Fatalf("stats = %+v", st)
	}

	for _, c := range []struct {
		path, key, body string
		status          int
		want            string
	}{
		{"/chat/completions", "sk-a", `{`, 400, "not JSON"},
		{"/chat/completions", "sk-x", `{}`, 401, "bad key"},
		{"/chat/completions", "sk-a", `{"model": "m"}`, 200, `"content":"echo from a"`},
		{"/messages", "sk-a", `{"model": "m"}`, 200, `"type":"message"`},
		{"/messages/count_tokens", "sk-a", `{}`, 200, `"input_tokens":7`},
		{"/embeddings", "sk-a", `{"model": "e"}`, 200, `"embedding":[0.1,0.2]`},
		{"/chat/completions", "sk-a", `{"stream": true, "chunk_ms": 1}`, 200, "echo from a #2\"},\"index\":0}],\"id\":\"chatcmpl-mock\",\"model\":\"\",\"object\":\"chat.completion.chunk\"}\n\ndata: [DONE]\n\n"},
		{"/messages", "sk-a", `{"stream": true, "chunks": 1}`, 200, "event: content_block_delta\n"},
		{"/messages", "sk-a", `{"stream": true, "chunks": 1, "chunk_ms": 1}`, 200, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"},
		{"/raw", "sk-a", `{"big_bytes": 40000, "chunk_ms": 1}`, 200, strings.Repeat("z", 40000)},
	} {
		status, body := call(t, base+c.path, c.key, c.body)
		if status != c.status || !strings.Contains(body, c.want) {
			t.Errorf("%s %s: %d %.200q", c.path, c.body, status, body)
		}
	}
	if last := m.Last("a"); last == nil || last.Path != "raw" || last.Header["authorization"] != "Bearer sk-a" {
		t.Fatalf("last = %+v", last)
	}
	if m.Last("never") != nil {
		t.Fatal("a tag never called has a last call")
	}
}

// TestMockLLMCanceled: a caller going away while the mock sleeps, streams,
// or sends a large body, in pieces or as fast as it goes, is counted.
func TestMockLLMCanceled(t *testing.T) {
	m := StartMockLLM(t)
	for i, c := range []struct {
		path, body string
		read       int // bytes to read before going away; 0: go once the call is seen
	}{
		{"/chat/completions", `{"sleep_ms": 10000}`, 0},
		{"/chat/completions", `{"stream": true, "chunk_ms": 10000}`, 6},
		{"/raw", `{"big_bytes": 1048576, "chunk_ms": 10000}`, 1024},
		{"/raw", `{"big_bytes": 1073741824}`, 1024},
	} {
		tag := fmt.Sprint("c", i)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			req, _ := http.NewRequestWithContext(ctx, "POST", m.BaseURL(tag)+c.path, strings.NewReader(c.body))
			req.Header.Set("Authorization", "Bearer sk-"+tag)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return
			}
			defer resp.Body.Close()
			if _, err := io.ReadFull(resp.Body, make([]byte, c.read)); err == nil {
				cancel()
			}
		}()
		if c.read == 0 {
			Eventually(t, "the call", func() bool { return m.Stats(tag).Calls == 1 })
			cancel()
		}
		<-done
		cancel()
		Eventually(t, c.body+" counted as canceled", func() bool { return m.Stats(tag).Canceled == 1 })
	}
}

// TestMockLLMMore: a short sleep, and a body cut on purpose.
func TestMockLLMMore(t *testing.T) {
	m := StartMockLLM(t)
	if status, body := call(t, m.BaseURL("a")+"/chat/completions", "sk-a", `{"sleep_ms": 1}`); status != 200 || !strings.Contains(body, "echo from a") {
		t.Fatalf("sleep: %d %s", status, body)
	}
	resp := post(context.Background(), t, m.BaseURL("a")+"/raw", "sk-a", `{"big_bytes": 1000000, "cut_after": 1}`)
	n, err := io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if err == nil || n >= 1000000 {
		t.Fatalf("cut body: %d bytes, %v", n, err)
	}
	// A large body sent whole, the mock done before the test ends.
	resp = post(context.Background(), t, m.BaseURL("a")+"/raw", "sk-a", `{"big_bytes": 40000}`)
	n, err = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if err != nil || n != 40000 {
		t.Fatalf("whole body: %d bytes, %v", n, err)
	}
}
