package assistant

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fib"
	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/app/console"
	"github.com/lesismal/fit/assistant/internal/fakeapi"
	"github.com/lesismal/fit/assistant/model"
	"github.com/lesismal/fit/config"
)

const testKey = "sk-test-0123456789abcdef"

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

// fixture is an assistant served by a console, and a logged-in browser.
type fixture struct {
	t    *testing.T
	a    *Assistant
	cfg  *config.Config
	url  string
	c    *http.Client
	csrf string

	mu   sync.Mutex
	logs []string
}

// loadConfig loads a config whose admin.assistant is assistant.
func loadConfig(t *testing.T, assistant string) *config.Config {
	t.Helper()
	hash, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	cfg, err := config.Load([]byte(`{"admin": {"console": {"users": [{"username": "ada", "password_hash": "` + string(hash) +
		`"}, {"username": "bob", "password_hash": "` + string(hash) + `"}]}, "assistant": ` + assistant + `}}`))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func start(t *testing.T, cfg *config.Config, st Store) *fixture {
	t.Helper()
	return startWith(t, cfg, st)
}

// startWith starts an assistant as start does, with what opts set in its
// options.
func startWith(t *testing.T, cfg *config.Config, st Store, opts ...func(*Options)) *fixture {
	t.Helper()
	f := &fixture{t: t, cfg: cfg}
	e := engine(t)
	o := Options{Engine: e, Settings: cfg.Admin.Assistant, Console: cfg.Admin.Console, Config: func() *config.Config { return f.cfg }, Logf: f.logf}
	if st != nil {
		o.Store = st
	}
	for _, fn := range opts {
		fn(&o)
	}
	var err error
	if f.a, err = New(o); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.a.Close)
	rc := &config.RoleConsole{Address: "127.0.0.1:0", Users: cfg.Admin.Console.Users}
	c, err := console.Start(e, console.Options{Config: rc, Cookie: "fit_test", Logf: f.logf,
		Modules: func() []*console.Module { return []*console.Module{f.a.Module()} }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Stop(context.Background()) })
	f.url = strings.TrimSuffix(c.URL(), "/")
	jar, _ := cookiejar.New(nil)
	f.c = &http.Client{Jar: jar, Timeout: 20 * time.Second}
	status, body := f.do("POST", "/api/login", `{"username":"ada","password":"pw"}`)
	if status != 200 {
		t.Fatalf("login: %d %s", status, body)
	}
	var s struct{ CSRF string }
	_ = json.Unmarshal([]byte(body), &s)
	f.csrf = s.CSRF
	return f
}

func (f *fixture) logf(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logs = append(f.logs, fmt.Sprintf(format, args...))
}

func (f *fixture) logText() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.logs, "\n")
}

func (f *fixture) do(method, path, body string) (int, string) {
	f.t.Helper()
	return f.doCtx(context.Background(), method, path, body)
}

func (f *fixture) doCtx(ctx context.Context, method, path, body string) (int, string) {
	f.t.Helper()
	req, _ := http.NewRequestWithContext(ctx, method, f.url+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set(console.CSRFHeader, f.csrf)
	resp, err := f.c.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (f *fixture) settings() settingsBody {
	f.t.Helper()
	status, body := f.do("GET", "/api/m/ai/settings", "")
	if status != 200 {
		f.t.Fatalf("GET settings: %d %s", status, body)
	}
	var b settingsBody
	if err := json.Unmarshal([]byte(body), &b); err != nil {
		f.t.Fatal(err)
	}
	return b
}

func TestNewNeedsAssistant(t *testing.T) {
	if _, err := New(Options{Config: func() *config.Config { return &config.Config{} }}); err == nil {
		t.Error("an assistant without admin.assistant")
	}
}

// TestSettingsFromConfig: without a store, the settings are the config's,
// a key comes from the environment and is shown masked, and nothing can be
// saved.
func TestSettingsFromConfig(t *testing.T) {
	t.Setenv("FIT_TEST_OPENAI_KEY", testKey)
	cfg := loadConfig(t, `{"providers": [{"name": "o", "kind": "openai", "api_key_env": "FIT_TEST_OPENAI_KEY", "models": [{"name": "gpt-test"}]},
		{"name": "short", "kind": "openai", "api_key_env": "FIT_TEST_SHORT"}, {"name": "none", "kind": "ollama"}]}`)
	t.Setenv("FIT_TEST_SHORT", "sk-1")
	f := start(t, cfg, nil)
	b := f.settings()
	if b.Source != "config" || b.Store || b.Keys["o"].Source != "env" || b.Keys["o"].Masked != "••••cdef" || b.Keys["short"].Masked != "••••" ||
		b.Keys["none"].Set || len(b.Users) != 2 || b.LLMProxy != nil || b.Defaults["ollama"] != "http://127.0.0.1:11434" {
		t.Fatalf("%+v", b)
	}
	var s map[string]any
	_ = json.Unmarshal(b.Settings, &s)
	if fakeapi.Get(s, "providers", 0, "name") != "o" {
		t.Errorf("settings: %s", b.Settings)
	}
	status, body := f.do("PUT", "/api/m/ai/settings", `{"settings": {}}`)
	if status != 409 || !strings.Contains(body, "admin.store") {
		t.Errorf("a save without a store: %d %s", status, body)
	}
	if _, body := f.do("GET", "/api/m/ai/settings", ""); strings.Contains(body, testKey) {
		t.Error("the key is in the answer")
	}
	p, err := f.a.Provider("o")
	if err != nil || string(p.Key) != testKey || p.BaseURL != "https://api.openai.com/v1" {
		t.Errorf("%+v %v", p, err)
	}
	if _, err := f.a.Provider("x"); err == nil {
		t.Error("a provider that is not")
	}
	if f.a.Settings().Providers[0].Name != "o" || f.a.Client() == nil {
		t.Error("Settings")
	}
}

// TestSaveSettings: the Settings page saves the settings and keys to the
// store; a key is never in an answer or a log, and is kept until it is
// removed or its provider is; a stale page, bad settings and bad keys are
// refused.
func TestSaveSettings(t *testing.T) {
	st := &memStore{}
	f := start(t, loadConfig(t, `{}`), st)
	b := f.settings()
	if b.Source != "config" || !b.Store || b.Version != "0" {
		t.Fatalf("%+v", b)
	}
	put := func(body string) (int, string) { return f.do("PUT", "/api/m/ai/settings", body) }
	settings := `{"providers": [{"name": "a", "kind": "anthropic"}, {"name": "b", "kind": "openai"}], "users": [{"username": "bob", "level": "off"}]}`
	status, body := put(`{"settings": ` + settings + `, "keys": {"a": "` + testKey + `", "b": "sk-other-key-0000"}, "version": "0"}`)
	if status != 200 || strings.Contains(body, testKey) {
		t.Fatalf("%d %s", status, body)
	}
	b = f.settings()
	if b.Source != "store" || b.Version != "1" || b.Keys["a"].Masked != "••••cdef" || b.Keys["a"].Source != "store" || !b.Keys["b"].Set {
		t.Fatalf("%+v", b)
	}
	if f.a.Settings().Level("bob") != config.AssistantOff {
		t.Error("the saved settings are not used")
	}
	var k kept
	_ = json.Unmarshal(st.Section(Section), &k)
	if k.Keys["a"] != testKey || !strings.Contains(string(k.Settings), `"anthropic"`) {
		t.Errorf("the store: %s", st.Section(Section))
	}

	// A stale version, bad settings, bad keys, no settings.
	for _, tc := range []struct{ body, want string }{
		{`{"settings": ` + settings + `, "version": "0"}`, "412"},
		{`{"settings": {"providers": [{"name": "x", "kind": "nope"}]}}`, "admin.assistant.providers[0].kind"},
		{`{"settings": ` + settings + `, "keys": {"zz": "k"}}`, "which is no provider"},
		{`{"settings": ` + settings + `, "keys": {"a": "has space"}}`, "printable"},
		{`{"keys": {}}`, "must hold the settings"},
		{`{"settings": "` + strings.Repeat("x", MaxSettingsBytes) + `"}`, "must hold the settings"},
		{`nope`, "not the JSON"},
	} {
		status, body := put(tc.body)
		if status == 200 || !strings.Contains(fmt.Sprint(status)+body, tc.want) {
			t.Errorf("%s: %d %s", tc.body, status, body)
		}
	}

	// A key removed, a provider removed with its key, the other kept.
	status, body = put(`{"settings": {"providers": [{"name": "a", "kind": "anthropic"}]}, "keys": {"a": ""}}`)
	if status != 200 {
		t.Fatal(status, body)
	}
	k = kept{}
	_ = json.Unmarshal(st.Section(Section), &k)
	if len(k.Keys) != 0 {
		t.Errorf("keys left: %v", k.Keys)
	}
	put(`{"settings": {"providers": [{"name": "a", "kind": "anthropic"}, {"name": "c", "kind": "gemini"}]}, "keys": {"c": "` + testKey + `"}}`)
	put(`{"settings": {"providers": [{"name": "c", "kind": "gemini"}]}}`)
	if p, _ := f.a.Provider("c"); string(p.Key) != testKey {
		t.Error("a key not named was not kept")
	}

	// A write that fails changes nothing.
	st.mu.Lock()
	st.fail = true
	st.mu.Unlock()
	if status, body := put(`{"settings": {}}`); status != 500 || !strings.Contains(body, "disk is full") {
		t.Errorf("%d %s", status, body)
	}
	st.mu.Lock()
	st.fail = false
	st.mu.Unlock()
	if f.a.Settings().Provider("c") == nil {
		t.Error("a failed write changed the settings")
	}
	if strings.Contains(f.logText(), testKey) || !strings.Contains(f.logText(), `"ada" saved the assistant's settings`) {
		t.Errorf("logs: %s", f.logText())
	}

	// A new assistant reads the store.
	again, err := New(Options{Engine: engine(t), Settings: f.cfg.Admin.Assistant, Console: f.cfg.Admin.Console, Config: func() *config.Config { return f.cfg }, Store: st})
	if err != nil || again.Settings().Provider("c") == nil {
		t.Fatalf("%v", err)
	}
	again.Close()
}

// TestStoreThatDoesNotLoad: settings in the store that no longer load, or a
// section that is not JSON, leave the config's in use, and say so.
func TestStoreThatDoesNotLoad(t *testing.T) {
	cfg := loadConfig(t, `{"providers": [{"name": "cfg", "kind": "ollama"}]}`)
	for _, sec := range []string{`{"settings": {"nope": 1}, "keys": {"cfg": "k"}}`, `{`} {
		var logs []string
		a, err := New(Options{Engine: engine(t), Settings: cfg.Admin.Assistant, Console: cfg.Admin.Console, Config: func() *config.Config { return cfg }, Store: &memStore{sec: map[string][]byte{Section: []byte(sec)}},
			Logf: func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }})
		if err != nil || a.Settings().Provider("cfg") == nil || len(logs) != 1 {
			t.Errorf("%s: %v %v", sec, err, logs)
		}
		a.Close()
	}
}

// TestProviderTest: a provider is tested before it is saved, with a key
// typed on the page or the one kept: its model list, then a short call;
// the key never comes back, even when the API says it back.
func TestProviderTest(t *testing.T) {
	fake := fakeapi.Start(t, testKey)
	st := &memStore{}
	f := start(t, loadConfig(t, `{}`), st)
	test := func(body string) testResult {
		t.Helper()
		status, b := f.do("POST", "/api/m/ai/providers/test", body)
		if status != 200 || strings.Contains(b, testKey) {
			t.Fatalf("%d %s", status, b)
		}
		var r testResult
		_ = json.Unmarshal([]byte(b), &r)
		return r
	}
	for _, kind := range fakeapi.Kinds {
		if kind == "llm_proxy" {
			continue
		}
		prov := fmt.Sprintf(`{"name": "p", "kind": %q, "base_url": %q, "api_version": %q}`, kind, fake.BaseURL(kind), map[bool]string{true: "v1"}[kind == "azure_openai"])
		model := map[string]string{"anthropic": "claude-test", "gemini": "gemini-test", "ollama": "qwen-test", "azure_openai": "dep"}[kind]
		r := test(`{"provider": ` + prov + `, "key": "` + testKey + `", "model": "` + model + `"}`)
		if !r.OK || len(r.Models) == 0 || r.Reply != "The listener web is up." || r.Usage == nil {
			t.Errorf("%s: %+v", kind, r)
		}
	}
	// The kept key, the provider's first model.
	prov := `{"name": "o", "kind": "openai", "base_url": "` + fake.BaseURL("openai") + `", "models": [{"name": "gpt-test"}]}`
	if status, body := f.do("PUT", "/api/m/ai/settings", `{"settings": {"providers": [`+prov+`]}, "keys": {"o": "`+testKey+`"}}`); status != 200 {
		t.Fatal(body)
	}
	if r := test(`{"provider": ` + prov + `}`); !r.OK || r.Model != "gpt-test" {
		t.Errorf("the kept key: %+v", r)
	}
	// The list alone.
	if r := test(`{"provider": ` + prov + `, "models_only": true}`); !r.OK || r.Model != "" || len(r.Models) != 2 {
		t.Errorf("the list alone: %+v", r)
	}
	// No model known: the list's first.
	if r := test(`{"provider": {"name": "o", "kind": "openai", "base_url": "` + fake.BaseURL("openai") + `"}}`); !r.OK || r.Model != "gpt-test" {
		t.Errorf("the list's model: %+v", r)
	}
	// A wrong key: the API says it back, and it is replaced.
	wrong := "sk-wrong-key-abcdef"
	status, body := f.do("POST", "/api/m/ai/providers/test", `{"provider": {"name": "o", "kind": "openai", "base_url": "`+fake.BaseURL("openai")+`"}, "key": "`+wrong+`", "model": "gpt-test"}`)
	if status != 200 || strings.Contains(body, wrong) || !strings.Contains(body, `"ok":false`) || !strings.Contains(body, "***") {
		t.Errorf("a wrong key: %d %s", status, body)
	}
	// Neither a list nor a model.
	if r := test(`{"provider": {"name": "x", "kind": "ollama", "base_url": "http://127.0.0.1:1"}}`); r.OK || r.ModelsError == "" || r.Model != "" {
		t.Errorf("unreachable: %+v", r)
	}
	// A bad provider, a key too long.
	for _, b := range []string{`{"provider": {"name": "x", "kind": "nope"}}`, `{"provider": {"name": "x", "kind": "ollama"}, "key": "` + strings.Repeat("k", MaxKeyBytes+1) + `"}`, `{`} {
		if status, body := f.do("POST", "/api/m/ai/providers/test", b); status != 400 {
			t.Errorf("%s: %d %s", b, status, body)
		}
	}
	// An llm_proxy provider without llm.proxy.
	if r := test(`{"provider": {"name": "m", "kind": "llm_proxy"}}`); r.OK || !strings.Contains(r.ModelsError, "no llm.proxy") {
		t.Errorf("llm_proxy without llm.proxy: %+v", r)
	}
	if strings.Contains(f.logText(), testKey) || strings.Contains(f.logText(), wrong) {
		t.Error("a key in the logs")
	}
}

// TestProviderTestCancelled: tests are bounded in number, and a browser
// that leaves cancels its test's call, freeing its place.
func TestProviderTestCancelled(t *testing.T) {
	gone := make(chan struct{}, MaxTests+1)
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			gone <- struct{}{}
		case <-time.After(10 * time.Second):
		}
	}))
	t.Cleanup(slow.Close)
	f := start(t, loadConfig(t, `{}`), nil)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	body := `{"provider": {"name": "s", "kind": "ollama", "base_url": "` + slow.URL + `"}, "model": "m"}`
	for i := 0; i < MaxTests; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); f.doCtx(ctx, "POST", "/api/m/ai/providers/test", body) }()
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(f.a.tests) < MaxTests && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if status, b := f.do("POST", "/api/m/ai/providers/test", body); status != 429 {
		t.Fatalf("a test past the bound: %d %s", status, b)
	}
	cancel()
	wg.Wait()
	for i := 0; i < MaxTests; i++ {
		select {
		case <-gone:
		case <-time.After(5 * time.Second):
			t.Fatalf("the API did not see call %d go", i)
		}
	}
	deadline = time.Now().Add(5 * time.Second)
	for len(f.a.tests) > 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := len(f.a.tests); n != 0 {
		t.Errorf("%d tests still hold a place", n)
	}
}

func TestProviderTestCancelledEarly(t *testing.T) {
	pt := &providerTest{}
	pt.cancel(errors.New("gone"))
	ch := make(chan error, 1)
	c := model.NewClient(engine(t), model.Limits{})
	defer c.Close()
	call := c.Stream(&model.Provider{Name: "x", Kind: model.KindOllama, BaseURL: "http://127.0.0.1:1", Retries: 5}, &model.Request{Model: "m"}, 0, nil,
		func(_ *model.Result, err error) { ch <- err })
	pt.setCall(call)
	if err := <-ch; err == nil {
		t.Error("a call set on a cancelled test went on")
	}
	if clip("abc", 2) != "ab…" || clip("ab", 2) != "ab" {
		t.Error("clip")
	}
}

// TestLLMProxyProvider: an llm_proxy provider calls llm.proxy's API on
// loopback, whatever address it listens on, over TLS with its certificate
// pinned when it has one.
func TestLLMProxyProvider(t *testing.T) {
	fake := fakeapi.Start(t, testKey)
	tlsFake := httptest.NewTLSServer(fake.Config.Handler)
	t.Cleanup(tlsFake.Close)
	dir := t.TempDir()
	certFile := filepath.Join(dir, "cert.pem")
	pemData := append([]byte("junk\n"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte{1}})...)
	pemData = append(pemData, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tlsFake.Certificate().Raw})...)
	_ = os.WriteFile(certFile, pemData, 0o600)
	_ = os.WriteFile(filepath.Join(dir, "none.pem"), []byte("no pem"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "bad.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte{1, 2}}), 0o600)

	cfg := loadConfig(t, `{"providers": [{"name": "mesh", "kind": "llm_proxy"}]}`)
	f := start(t, cfg, &memStore{})
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(fake.URL, "http://"))
	_, tport, _ := net.SplitHostPort(strings.TrimPrefix(tlsFake.URL, "https://"))
	for _, tc := range []struct {
		api  config.LLMProxyAPI
		want string
	}{
		{config.LLMProxyAPI{Address: ":" + port}, "http://127.0.0.1:" + port + "/v1"},
		{config.LLMProxyAPI{Address: "0.0.0.0:" + port}, "http://127.0.0.1:" + port + "/v1"},
		{config.LLMProxyAPI{Address: "[::]:" + port}, "http://[::1]:" + port + "/v1"},
		{config.LLMProxyAPI{Address: "127.0.0.1:" + tport, CertFile: certFile}, "https://127.0.0.1:" + tport + "/v1"},
		{config.LLMProxyAPI{Address: "nope"}, "error"},
		{config.LLMProxyAPI{Address: ":1", CertFile: filepath.Join(dir, "missing.pem")}, "error"},
		{config.LLMProxyAPI{Address: ":1", CertFile: filepath.Join(dir, "none.pem")}, "error"},
		{config.LLMProxyAPI{Address: ":1", CertFile: filepath.Join(dir, "bad.pem")}, "error"},
	} {
		api := tc.api
		f.mu.Lock()
		f.cfg.LLM = &config.LLM{Proxy: &config.LLMProxy{API: &api}}
		f.mu.Unlock()
		p, err := f.a.Provider("mesh")
		if tc.want == "error" {
			if err == nil {
				t.Errorf("%+v: no error", tc.api)
			}
			continue
		}
		if err != nil || p.BaseURL != tc.want || (tc.api.CertFile != "") != (p.PinnedCert != nil) {
			t.Errorf("%+v: %v %v", tc.api, p, err)
		}
	}
	// Called through the pages: the proxy's key, the pinned certificate.
	api := config.LLMProxyAPI{Address: "127.0.0.1:" + tport, CertFile: certFile}
	f.cfg.LLM = &config.LLM{Proxy: &config.LLMProxy{API: &api}}
	if b := f.settings(); b.LLMProxy == nil || !b.LLMProxy.TLS {
		t.Errorf("%+v", b.LLMProxy)
	}
	status, body := f.do("POST", "/api/m/ai/providers/test", `{"provider": {"name": "mesh", "kind": "llm_proxy"}, "key": "`+testKey+`", "model": "gpt-test"}`)
	if status != 200 || !strings.Contains(body, `"ok":true`) {
		t.Errorf("%d %s", status, body)
	}
}
