// Package assistant is the workbench's assistant: its settings, the model
// APIs it calls and, from here on, what it does with them. The console
// serves it as the module ai, at /api/m/ai/, only while admin.assistant is
// set: off by default, nothing is sent anywhere.
//
// The settings are admin.assistant's until the Settings page saves, and
// then the store's: the section "assistant" of admin.store keeps them with
// the providers' API keys, encrypted with the rest of the store. A key is
// written there, or read from the environment variable a provider names,
// and never handed back: the pages see whether one is set, and its last
// four characters. See docs/decisions/031-assistant.md.
//
// A chat (POST /api/chat) is a loop between the model and the tools of the
// Registry, streamed to the browser as SSE: the context is built for each
// call (prompt.go), the tools run on the console's pool against the Admin
// API in process (tools_read.go), each result redacted, bounded and fenced
// as data (redact.go), within the settings' limits (chat.go); the user's
// conversations are kept in the store (conversations.go). Skills (skills.go,
// skillstore.go) are named tasks: built in, or added on the Skills page and
// kept in the store; the system prompt lists them, and skill_load, /name or
// the panel's picker make one the conversation's, narrowing its tools.
//
// The endpoints run on fib's workers and never block one: what reads or
// writes files goes to the console's pool, and a model call is started on
// fib's asynchronous client and answered from its callback.
package assistant

import (
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"slices"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/lesismal/fib"

	"github.com/lesismal/fit/app/console"
	"github.com/lesismal/fit/assistant/model"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"

	// The converters adapt_convert and config_validate use.
	_ "github.com/lesismal/fit/adapter/caddyfile"
	_ "github.com/lesismal/fit/adapter/fitconf"
	_ "github.com/lesismal/fit/adapter/nginx"
	_ "github.com/lesismal/fit/adapter/yaml"
)

// ModuleID is the console module the assistant is served as.
const ModuleID = "ai"

// Section is the store's section the settings and keys are kept in.
const Section = "assistant"

// The bounds of the settings the pages send.
const (
	MaxSettingsBytes = 256 << 10
	MaxKeyBytes      = 4096
	// MaxTests is how many provider tests run at once; one more is
	// answered 429.
	MaxTests = 4
)

// The bounds of a provider's test, whatever its own timeouts.
var (
	testModelsWait = 15 * time.Second
	testCallWait   = 30 * time.Second
)

// Store keeps sections beside the config, as admin/store's Store does.
type Store interface {
	Section(name string) []byte
	SetSection(name string, data []byte) error
}

// Options are what an Assistant needs.
type Options struct {
	// Engine is the running engine whose connections the model calls take.
	Engine *fib.Engine
	// Settings is admin.assistant as fit started with it, which turned the
	// assistant on, and Console admin.console, whose users the settings
	// name: both change only with a restart, as all of admin does.
	Settings *config.Assistant
	Console  *config.Console
	// Config returns the running config, whose llm.proxy an llm_proxy
	// provider calls.
	Config func() *config.Config
	// Store keeps the settings and keys; nil without admin.store, and then
	// the settings are the config's and cannot be saved from the pages.
	Store Store
	// Logf says what users changed; never a key.
	Logf func(format string, args ...any)
	// Limits bound the model client; the defaults when zero.
	Limits model.Limits

	// API is the Admin API's endpoints, which the tools call in process,
	// on the console's pool, as the workbench's pages call them.
	API http.Handler
	// Schema is the config's JSON Schema, and SchemaZh, when not nil,
	// returns its descriptions' Chinese by their English: schema_describe's.
	Schema   []byte
	SchemaZh func() map[string]string
	// Validate checks a config's JSON as fit validate does, for
	// config_validate; config.Load alone when nil.
	Validate func(data []byte) error
	// Logs returns the latest lines of fit's own log, for log_tail; nil
	// when the log keeps none.
	Logs func(logs.Filter) []logs.Record
	// Tools are tools beside the read tools, which later iterations add:
	// the page tools, the propose tools.
	Tools []*Tool
	// LevelCap, when not nil, returns the highest level a user has,
	// whatever the settings give: a manager of other fits bounds the
	// assistant by the user's roles there (cluster's AssistantCap).
	LevelCap func(user string) string
}

// Assistant is the assistant of a console.
type Assistant struct {
	o      Options
	client *model.Client
	module *console.Module
	tests  chan struct{}

	mu        sync.Mutex
	raw       json.RawMessage // the settings in use, as written
	settings  *config.Assistant
	keys      map[string]string // by provider, from the store
	fromStore bool
	rev       uint64                   // counts the saves, for the pages' version
	running   int                      // the chats under way
	extra     []func(api *console.API) // routes added by Extend

	registry *Registry
	skills   skillSet
	chats    chats
	audit    auditLog
	// applyMu lets one proposal be applied at a time.
	applyMu sync.Mutex
	// subjectKey is the key playback sessions' subjects are hashed with
	// for the model, made anew each time fit starts (tools_media.go).
	subjectKey []byte
	started    time.Time
	close      sync.Once
}

// kept is the store's section.
type kept struct {
	Settings json.RawMessage   `json:"settings,omitempty"`
	Keys     map[string]string `json:"keys,omitempty"`
}

// New returns the assistant o.Settings turns on, with the settings and keys
// the store keeps when it keeps some. Settings in the store that no longer
// load, as after an upgrade that changed them, are left there and
// o.Settings are used; Logf says so.
func New(o Options) (*Assistant, error) {
	if o.Settings == nil {
		return nil, errors.New("assistant: admin.assistant is not set")
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	raw, _ := json.Marshal(o.Settings.At)
	if o.Settings.At.IsZero() {
		raw = json.RawMessage("{}") // made in code, not loaded
	}
	a := &Assistant{o: o, client: model.NewClient(o.Engine, o.Limits), tests: make(chan struct{}, MaxTests),
		raw: raw, settings: o.Settings, registry: NewRegistry(), started: time.Now(),
		chats: chats{users: map[string]*userChats{}, dirty: map[string]bool{}, wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{})}}
	a.subjectKey = make([]byte, 32)
	_, _ = rand.Read(a.subjectKey) // crypto/rand.Read never fails
	for _, t := range slices.Concat(readTools(), []*Tool{skillLoadTool()}, proposeTools(), mediaTools(), pageTools(), o.Tools) {
		if err := a.registry.Add(t); err != nil {
			return nil, err
		}
	}
	if o.Store != nil {
		if sec := o.Store.Section(Section); sec != nil {
			var k kept
			if err := json.Unmarshal(sec, &k); err != nil {
				o.Logf("assistant: the store's section %q is not JSON; it is left as it is, and admin.assistant is used", Section)
			} else {
				a.keys = k.Keys
				if k.Settings != nil {
					if s, err := config.LoadAssistant(k.Settings, o.Console); err != nil {
						o.Logf("assistant: the settings kept in the store do not load, so admin.assistant is used until they are saved again: %v", err)
					} else {
						a.raw, a.settings, a.fromStore = k.Settings, s, true
					}
				}
			}
		}
	}
	if err := a.loadSkills(); err != nil {
		return nil, err
	}
	a.loadAudit()
	a.module = &console.Module{ID: ModuleID, Routes: a.routes}
	go a.saver()
	return a, nil
}

// Module returns the console module the assistant is served as.
func (a *Assistant) Module() *console.Module { return a.module }

// Client returns the model client the assistant calls with.
func (a *Assistant) Client() *model.Client { return a.client }

// Registry returns the tools the assistant offers.
func (a *Assistant) Registry() *Registry { return a.registry }

// Close closes the model client, so that calls in flight fail, and writes
// the conversations that changed to the store.
func (a *Assistant) Close() {
	a.close.Do(func() {
		a.client.Close()
		close(a.chats.stop)
		<-a.chats.done
	})
}

// Settings returns the settings in use, which the caller must not change.
func (a *Assistant) Settings() *config.Assistant {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.settings
}

// Level returns user's level under the settings s, bounded by
// Options.LevelCap: every check of a user's level goes through it.
func (a *Assistant) Level(s *config.Assistant, user string) string {
	level := s.Level(user)
	if a.o.LevelCap == nil {
		return level
	}
	if c := a.o.LevelCap(user); levelRank(c) < levelRank(level) {
		return c
	}
	return level
}

// Provider returns the provider name of the settings in use, as a model
// call needs it, its key included. An llm_proxy provider's certificate may
// be read from its file: call it on a pool, not a fib worker.
func (a *Assistant) Provider(name string) (*model.Provider, error) {
	a.mu.Lock()
	s, keys := a.settings, a.keys
	a.mu.Unlock()
	p := s.Provider(name)
	if p == nil {
		return nil, fmt.Errorf("no provider is named %q", name)
	}
	key, _ := keyOf(p, keys)
	return a.resolve(p, key)
}

// keyOf returns p's key, and where it is from: the store, the environment,
// or "" for none.
func keyOf(p *config.AssistantProvider, keys map[string]string) (key, source string) {
	if k := keys[p.Name]; k != "" {
		return k, "store"
	}
	if p.APIKeyEnv != "" {
		if k := os.Getenv(p.APIKeyEnv); k != "" {
			return k, "env"
		}
	}
	return "", ""
}

// resolve returns p as a call needs it, with key.
func (a *Assistant) resolve(p *config.AssistantProvider, key string) (*model.Provider, error) {
	mp := &model.Provider{Name: p.Name, Kind: p.Kind, BaseURL: p.URL(), APIVersion: p.APIVersion, Key: model.Secret(key),
		Headers: p.Headers, ConnectTimeout: p.Connect(), FirstByteTimeout: p.FirstByte(), IdleTimeout: p.Idle(),
		Timeout: p.Total(), Retries: p.RetryCount()}
	if p.Kind != config.AssistantLLMProxy {
		return mp, nil
	}
	cfg := a.o.Config()
	if cfg.LLM == nil || cfg.LLM.Proxy == nil || cfg.LLM.Proxy.API == nil {
		return nil, fmt.Errorf("%s: this fit runs no llm.proxy with an api to call", p.Name)
	}
	api := cfg.LLM.Proxy.API
	host, port, err := net.SplitHostPort(api.Address)
	if err != nil {
		return nil, fmt.Errorf("%s: llm.proxy.api.address: %v", p.Name, err)
	}
	// The API is reached on loopback, whatever address it listens on.
	switch ip := net.ParseIP(host); {
	case host == "" || ip != nil && ip.IsUnspecified() && ip.To4() != nil:
		host = "127.0.0.1"
	case ip != nil && ip.IsUnspecified():
		host = "::1"
	}
	scheme := "http"
	if api.CertFile != "" {
		scheme = "https"
		if mp.PinnedCert, err = leafDER(api.CertFile); err != nil {
			return nil, fmt.Errorf("%s: llm.proxy.api.cert_file: %v", p.Name, err)
		}
	}
	mp.BaseURL = scheme + "://" + net.JoinHostPort(host, port) + "/v1"
	return mp, nil
}

// leafDER reads the first certificate of a PEM file.
func leafDER(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	for {
		var b *pem.Block
		b, data = pem.Decode(data)
		if b == nil {
			return nil, errors.New("no certificate in the file")
		}
		if b.Type == "CERTIFICATE" {
			if _, err := x509.ParseCertificate(b.Bytes); err != nil {
				return nil, err
			}
			return b.Bytes, nil
		}
	}
}

// mask shows a key's last four characters only, and those only of a key
// long enough that they give nothing away.
func mask(key string) string {
	if len(key) < 16 {
		return "••••"
	}
	return "••••" + key[len(key)-4:]
}

// keyView is what the pages see of a provider's key.
type keyView struct {
	Set    bool   `json:"set"`
	Source string `json:"source,omitempty"`
	Masked string `json:"masked,omitempty"`
	Env    string `json:"env,omitempty"`
}

// proxyView is what the pages see of llm.proxy's API, which an llm_proxy
// provider calls.
type proxyView struct {
	Address string `json:"address"`
	TLS     bool   `json:"tls"`
}

// settingsBody is GET /api/settings's answer, and PUT's.
type settingsBody struct {
	Settings json.RawMessage    `json:"settings"`
	Source   string             `json:"source"`
	Store    bool               `json:"store"`
	Version  string             `json:"version"`
	Keys     map[string]keyView `json:"keys"`
	Kinds    []string           `json:"kinds"`
	Levels   []string           `json:"levels"`
	Users    []string           `json:"users"`
	LLMProxy *proxyView         `json:"llm_proxy"`
	Defaults map[string]string  `json:"defaults"`
}

func (a *Assistant) view() settingsBody {
	a.mu.Lock()
	raw, s, keys, fromStore, rev := a.raw, a.settings, a.keys, a.fromStore, a.rev
	a.mu.Unlock()
	b := settingsBody{Settings: raw, Source: "config", Store: a.o.Store != nil, Version: strconv.FormatUint(rev, 10),
		Keys: map[string]keyView{}, Kinds: config.AssistantKinds, Levels: config.AssistantLevels, Users: []string{},
		Defaults: map[string]string{}}
	if fromStore {
		b.Source = "store"
	}
	for i := range s.Providers {
		p := &s.Providers[i]
		key, source := keyOf(p, keys)
		v := keyView{Set: key != "", Source: source, Env: p.APIKeyEnv}
		if key != "" {
			v.Masked = mask(key)
		}
		b.Keys[p.Name] = v
	}
	for _, k := range config.AssistantKinds {
		b.Defaults[k] = (&config.AssistantProvider{Kind: k}).URL()
	}
	if a.o.Console != nil {
		for _, u := range a.o.Console.Users {
			b.Users = append(b.Users, u.Username)
		}
	}
	cfg := a.o.Config()
	if cfg.LLM != nil && cfg.LLM.Proxy != nil && cfg.LLM.Proxy.API != nil {
		b.LLMProxy = &proxyView{Address: cfg.LLM.Proxy.API.Address, TLS: cfg.LLM.Proxy.API.CertFile != ""}
	}
	return b
}

func (a *Assistant) routes(api *console.API) {
	api.Handle("GET", "/api/settings", func(r *console.Request) { _ = r.JSON(200, a.view()) })
	api.Handle("PUT", "/api/settings", a.put)
	api.Handle("POST", "/api/providers/test", a.test)
	api.Handle("GET", "/api/tools", a.toolsHandler)
	api.Handle("POST", "/api/chat", a.chatHandler)
	api.Handle("POST", "/api/chat/stop", a.stopHandler)
	api.Handle("POST", "/api/chat/page", a.pageHandler)
	api.Handle("GET", "/api/conversations", a.conversationsHandler)
	api.HandlePrefix("", "/api/conversations/", a.conversationHandler)
	api.Handle("GET", "/api/proposals", a.proposalsHandler)
	api.HandlePrefix("", "/api/proposals/", a.proposalHandler)
	api.Handle("GET", "/api/audit", a.auditHandler)
	api.Handle("GET", "/api/skills", a.skillsHandler)
	api.Handle("POST", "/api/skills", a.skillsPost)
	api.HandlePrefix("", "/api/skills/", a.skillHandler)
	a.mu.Lock()
	extra := a.extra
	a.mu.Unlock()
	for _, fn := range extra {
		fn(api)
	}
}

// putBody is a change of the settings: the settings whole, the keys to set
// by provider (an empty one removed, one not named kept), and the version
// the page read, which must still be the one in use.
type putBody struct {
	Settings json.RawMessage   `json:"settings"`
	Keys     map[string]string `json:"keys"`
	Version  string            `json:"version"`
}

// errorsBody is a refused change, with each error at its path.
type errorsBody struct {
	Error  string        `json:"error"`
	Errors config.Errors `json:"errors,omitempty"`
}

// put answers PUT /api/settings: the settings are checked as the config's
// would be, and written to the store with the keys, on the console's pool.
func (a *Assistant) put(r *console.Request) {
	if a.o.Store == nil {
		_ = console.Error(r.Context, 409, "the assistant's settings are not saved: fit has no admin.store, so they are admin.assistant's in the config file, and the keys come from api_key_env")
		return
	}
	var body putBody
	if !r.Decode(&body) {
		return
	}
	if len(body.Settings) == 0 || len(body.Settings) > MaxSettingsBytes {
		_ = console.Error(r.Context, 400, fmt.Sprintf("the body must hold the settings, at most %d bytes", MaxSettingsBytes))
		return
	}
	s, err := config.LoadAssistant(body.Settings, a.o.Console)
	if err != nil {
		var errs config.Errors
		errors.As(err, &errs)
		_ = r.JSON(400, errorsBody{Error: "the settings are not valid", Errors: errs})
		return
	}
	for name, key := range body.Keys {
		switch {
		case s.Provider(name) == nil:
			_ = console.Error(r.Context, 400, fmt.Sprintf("a key for %q, which is no provider", name))
			return
		case len(key) > MaxKeyBytes || slices.ContainsFunc([]byte(key), func(c byte) bool { return c <= ' ' || c >= 0x7f }):
			_ = console.Error(r.Context, 400, fmt.Sprintf("the key of %q is not 1 to %d printable characters without spaces", name, MaxKeyBytes))
			return
		}
	}
	user := r.User
	r.Go(func() {
		if status, msg := a.save(user, body, s); status != 200 {
			_ = console.Error(r.Context, status, msg)
			return
		}
		_ = r.JSON(200, a.view())
	})
}

// save writes the settings s, as body has them, to the store with the keys
// body changes, and uses them from then on; the status says how it went.
// It writes a file: on a pool.
func (a *Assistant) save(user string, body putBody, s *config.Assistant) (status int, msg string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if body.Version != "" && body.Version != strconv.FormatUint(a.rev, 10) {
		return 412, "the settings changed since the page read them; read them again"
	}
	keys := map[string]string{}
	for name, key := range a.keys {
		if s.Provider(name) != nil {
			keys[name] = key
		}
	}
	var changed []string
	for name, key := range body.Keys {
		changed = append(changed, name)
		if key == "" {
			delete(keys, name)
		} else {
			keys[name] = key
		}
	}
	sort.Strings(changed)
	sec, _ := json.Marshal(kept{Settings: body.Settings, Keys: keys}) // strings and checked JSON
	if err := a.o.Store.SetSection(Section, sec); err != nil {
		return 500, "saving the settings: " + err.Error()
	}
	a.raw, a.settings, a.keys, a.fromStore = append(json.RawMessage(nil), body.Settings...), s, keys, true
	a.rev++
	a.o.Logf("console: %q saved the assistant's settings: %d providers; keys changed for %v", user, len(s.Providers), changed)
	return 200, ""
}

// testBody is a provider's test: its settings as the page has them, maybe
// not saved yet, a key typed and not saved (or the kept one when ""), and
// the model to call (the provider's first, or the list's, when ""), or
// none, with ModelsOnly, for the list alone.
type testBody struct {
	Provider   json.RawMessage `json:"provider"`
	Key        string          `json:"key"`
	Model      string          `json:"model"`
	ModelsOnly bool            `json:"models_only"`
}

// testResult is a test's answer: the model list and how long it took, and
// a short call to a model and how long that took, each with its error.
type testResult struct {
	OK          bool              `json:"ok"`
	Models      []model.ModelInfo `json:"models"`
	ModelsMs    int64             `json:"models_ms"`
	ModelsError string            `json:"models_error,omitempty"`
	Model       string            `json:"model,omitempty"`
	Reply       string            `json:"reply,omitempty"`
	CallMs      int64             `json:"call_ms,omitempty"`
	CallError   string            `json:"call_error,omitempty"`
	Usage       *model.Usage      `json:"usage,omitempty"`
}

// test answers POST /api/providers/test: the provider's model list, then a
// short call to a model, each bounded; the answer goes once both are over,
// and a browser that leaves cancels them.
func (a *Assistant) test(r *console.Request) {
	var body testBody
	if !r.Decode(&body) {
		return
	}
	s, err := config.LoadAssistant(append(append([]byte(`{"providers":[`), body.Provider...), ']', '}'), a.o.Console)
	if err != nil || len(s.Providers) != 1 {
		var errs config.Errors
		errors.As(err, &errs)
		_ = r.JSON(400, errorsBody{Error: "the provider is not valid", Errors: errs})
		return
	}
	if len(body.Key) > MaxKeyBytes {
		_ = console.Error(r.Context, 400, "the key is too long")
		return
	}
	select {
	case a.tests <- struct{}{}:
	default:
		_ = console.Error(r.Context, 429, "other tests are under way; try again")
		return
	}
	p := &s.Providers[0]
	key := body.Key
	if key == "" {
		a.mu.Lock()
		key, _ = keyOf(p, a.keys)
		a.mu.Unlock()
	}
	// An llm_proxy provider may read its certificate's file: on the pool.
	// The test's slot is given back by its end, or here when it did not
	// start.
	started := false
	r.GoThen(func() {
		mp, err := a.resolve(p, key)
		if err != nil {
			_ = r.JSON(200, testResult{ModelsError: err.Error(), Models: []model.ModelInfo{}})
			return
		}
		started = true
		r.Context.Retain()
		t := &providerTest{a: a, r: r, p: mp, model: body.Model, modelsOnly: body.ModelsOnly, res: testResult{Models: []model.ModelInfo{}}}
		if t.model == "" && len(p.Models) > 0 {
			t.model = p.Models[0].Name
		}
		r.Context.OnCancel(func(err error) { t.cancel(err) })
		t.models()
	}, func() {
		if !started {
			<-a.tests
		}
	})
}

// providerTest is a test under way.
type providerTest struct {
	a          *Assistant
	r          *console.Request
	p          *model.Provider
	model      string
	modelsOnly bool

	mu    sync.Mutex
	call  *model.Call
	ended bool
	res   testResult
}

func (t *providerTest) cancel(err error) {
	t.mu.Lock()
	t.ended = true
	call := t.call
	t.mu.Unlock()
	call.Cancel(err)
}

// setCall keeps the call under way, cancelling it when the test was.
func (t *providerTest) setCall(c *model.Call) {
	t.mu.Lock()
	t.call = c
	ended := t.ended
	t.mu.Unlock()
	if ended {
		c.Cancel(nil)
	}
}

func (t *providerTest) models() {
	q := *t.p
	q.FirstByteTimeout, q.Timeout = min(q.FirstByteTimeout, testModelsWait), min(q.Timeout, testModelsWait)
	start := time.Now()
	t.setCall(t.a.client.Models(&q, func(list []model.ModelInfo, err error) {
		t.res.ModelsMs = time.Since(start).Milliseconds()
		if err != nil {
			t.res.ModelsError = err.Error()
		} else {
			t.res.Models = list
			if t.model == "" && len(list) > 0 {
				t.model = list[0].ID
			}
		}
		if t.model == "" || t.modelsOnly {
			t.end()
			return
		}
		t.chat()
	}))
}

func (t *providerTest) chat() {
	q := *t.p
	q.FirstByteTimeout, q.Timeout, q.Retries = min(q.FirstByteTimeout, testCallWait), min(q.Timeout, testCallWait), 0
	t.res.Model = t.model
	req := &model.Request{Model: t.model, System: "You are being tested. Reply with the single word OK.",
		Messages: []model.Message{{Role: model.RoleUser, Content: "ping"}}, MaxTokens: 32}
	start := time.Now()
	t.setCall(t.a.client.Stream(&q, req, 64<<10, nil, func(res *model.Result, err error) {
		t.res.CallMs = time.Since(start).Milliseconds()
		if err != nil {
			t.res.CallError = err.Error()
		} else {
			t.res.Reply, t.res.Usage = clip(res.Text, 200), &res.Usage
		}
		t.end()
	}))
}

// end answers the test: it passed when its call answered, or, with no model
// to call, when the list came.
func (t *providerTest) end() {
	if t.res.Model != "" {
		t.res.OK = t.res.CallError == ""
	} else {
		t.res.OK = t.res.ModelsError == ""
	}
	_ = t.r.JSON(200, t.res)
	t.r.Context.Release()
	<-t.a.tests
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
