// Package llmtest runs fit with the LLM feature's roles for tests: a config
// loaded, compiled and served as fit runs it, with admin.Configs moving the
// roles through app.Manager and writing what their pages change back,
// into an admin.store when one is given. It is for tests only.
package llmtest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fib"
	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/admin"
	"github.com/lesismal/fit/admin/store"
	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/listener"
	"github.com/lesismal/fit/llm/internal/role"
	"github.com/lesismal/fit/observe"
	"github.com/lesismal/fit/runtime"
	"github.com/lesismal/fit/upstream"
)

// StorePassword is the password of the stores Start makes.
const StorePassword = "llmtest-store-password"

// User and Password log in to the pages of the roles of Console's configs.
const (
	User     = "admin"
	Password = "llmtest-secret"
)

var hash = func() string {
	h, _ := bcrypt.GenerateFromPassword([]byte(Password), bcrypt.MinCost)
	return string(h)
}()

// Console returns a console member, on 127.0.0.1 and a port the kernel
// picks, as JSON.
func Console() string {
	return `{"address": "127.0.0.1:0", "users": [{"username": "` + User + `", "password_hash": "` + hash + `"}]}`
}

func init() {
	// Stores cost little in tests.
	store.DefaultParams = store.Params{Time: 1, MemoryKiB: 64, Threads: 1}
}

// Fit is fit running a config.
type Fit struct {
	Configs  *admin.Configs
	Apps     *app.Manager
	Server   *listener.Server
	Upstream *fib.Engine
	Store    *store.Store

	rt         *runtime.Runtime
	stopEngine func()
	stopped    bool
}

// Start runs base, a config without admin. With storePath, the config is
// kept in the store there, created when missing: a store that exists
// already gives the config, and base is ignored, as fit does.
func Start(t testing.TB, base, storePath string) *Fit {
	t.Helper()
	f := &Fit{}
	var adminJSON []byte
	data := []byte(base)
	if storePath != "" {
		adminJSON, _ = json.Marshal(map[string]any{"store": map[string]string{"path": storePath, "password": StorePassword}})
		st, kept, err := store.Open(storePath, StorePassword)
		if err != nil {
			t.Fatal(err)
		}
		if kept != nil {
			data = kept
		} else if err := st.Write(data); err != nil {
			t.Fatal(err)
		}
		f.Store = st
	}
	full := data
	if adminJSON != nil {
		full = admin.WithAdmin(data, adminJSON)
	}
	c, err := config.Load(full)
	if err != nil {
		t.Fatal(err)
	}
	if f.Upstream, f.stopEngine, err = upstream.StartEngine(); err != nil {
		t.Fatal(err)
	}
	// Registered now, so a failure below stops what has started.
	t.Cleanup(f.Stop)
	env := runtime.Env{Metrics: observe.NewMetrics(), Engine: f.Upstream}
	g, err := runtime.Compile(c, env)
	if err != nil {
		t.Fatal(err)
	}
	f.rt = new(runtime.Runtime)
	f.rt.Swap(g)
	if f.Server, err = listener.Start(f.rt); err != nil {
		t.Fatal(err)
	}
	source := admin.SourceFile
	if f.Store != nil {
		source = admin.SourceStore
	}
	f.Configs = admin.NewConfigs(f.rt, f.Server, env, c, data, source, 10)
	if f.Store != nil {
		f.Configs.UseStore(adminJSON, f.Store.Write)
	}
	f.Apps = app.NewManager(f.Server.Engine(), f.Upstream, nil, f.Configs.EditMember)
	if err := f.Apps.Apply(c); err != nil {
		t.Fatal(err)
	}
	f.Configs.UseApps(f.Apps)
	return f
}

// Stop stops the roles and the server, as fit does when it stops; it
// stops only what Start got to.
func (f *Fit) Stop() {
	if f.stopped {
		return
	}
	f.stopped = true
	if f.Apps != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		f.Apps.Stop(ctx)
	}
	if f.Server != nil {
		_ = f.Server.Close()
	}
	if f.rt != nil {
		f.rt.Close()
	}
	f.stopEngine()
}

// Current returns the running base config.
func (f *Fit) Current() []byte { return f.Configs.Current().Data() }

// Role returns the running role of the LLM feature named role (hub, node or
// proxy), as its package's type, or nil.
func (f *Fit) Role(name string) any { return role.Find(f.Server.Engine(), name) }

// Browser is a logged-in user of a role's pages.
type Browser struct {
	t    testing.TB
	c    *http.Client
	Base string
	csrf string
}

// Login logs in to the pages at base, as http://127.0.0.1:port.
func Login(t testing.TB, base string) *Browser {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	b := &Browser{t: t, c: &http.Client{Jar: jar, Timeout: 30 * time.Second}, Base: strings.TrimSuffix(base, "/")}
	status, body := b.Do("POST", "/api/login", `{"username":"`+User+`","password":"`+Password+`"}`)
	if status != 200 {
		t.Fatalf("login: %d %s", status, body)
	}
	var s struct{ CSRF string }
	_ = json.Unmarshal([]byte(body), &s)
	b.csrf = s.CSRF
	return b
}

// WithoutCSRF returns the browser, sending no CSRF token.
func (b *Browser) WithoutCSRF() *Browser {
	c := *b
	c.csrf = ""
	return &c
}

// Do sends a request with the session's cookie and CSRF token.
func (b *Browser) Do(method, path, body string) (int, string) {
	b.t.Helper()
	req, _ := http.NewRequest(method, b.Base+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if b.csrf != "" {
		req.Header.Set("X-Fit-Csrf", b.csrf)
	}
	resp, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data)
}

// RPC calls a method of the role through its pages and decodes its result
// into out, which may be nil; it returns the error the pages answered.
func (b *Browser) RPC(method string, req, out any) error {
	b.t.Helper()
	body := "{}"
	if req != nil {
		raw, _ := json.Marshal(req)
		body = string(raw)
	}
	status, resp := b.Do("POST", "/api/rpc"+method, body)
	if status != 200 {
		var e struct {
			Error string `json:"error"`
			Code  int    `json:"code"`
		}
		_ = json.Unmarshal([]byte(resp), &e)
		return &RPCError{Status: status, Code: e.Code, Msg: e.Error}
	}
	if out != nil {
		return json.Unmarshal([]byte(resp), out)
	}
	return nil
}

// RPCError is a method's failure, as the pages answered it.
type RPCError struct {
	Status, Code int
	Msg          string
}

func (e *RPCError) Error() string { return e.Msg }

// CodeOf returns the business code of an RPC's error, 0 for none.
func CodeOf(err error) int {
	var e *RPCError
	if errors.As(err, &e) {
		return e.Code
	}
	return 0
}

// Eventually waits up to 10s for cond.
func Eventually(t testing.TB, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}
