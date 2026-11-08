package common

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

	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/app/console"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/tunnel/internal/tunneltest"
)

func TestModule(t *testing.T) {
	c := NewConsole(&app.Host{Engine: tunneltest.Engine(t), Log: discardLog(t)}, "tunnel.hub", nil)
	if m := c.Module(); m == nil || m.ID != "tunnel.hub" || c.cookie != "fit_tunnel_hub" {
		t.Fatalf("module %+v, cookie %q", m, c.cookie)
	}
	if o := c.options(nil); o.Modules()[0] != c.Module() {
		t.Fatal("the console's module is not the role's")
	}
}

func hash(t *testing.T, password string) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return string(h)
}

func discardLog(t *testing.T) *logs.Logger {
	t.Helper()
	root, err := logs.New(nil, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root.Logger(config.LoggerTunnel)
}

func TestConsoleApply(t *testing.T) {
	c := NewConsole(&app.Host{Engine: tunneltest.Engine(t), Log: discardLog(t)}, "tunnel.test", nil)
	defer c.Stop(context.Background())
	if c.Addr() != "" || c.URL() != "" {
		t.Fatal("an address before Apply")
	}
	h := hash(t, "pw")
	cfg := &config.RoleConsole{Address: "127.0.0.1:0", Users: []config.ConsoleUser{{Username: "admin", PasswordHash: h}}}
	err := c.Apply(cfg)
	if err != nil {
		t.Fatal(err)
	}
	first := c.Addr()
	if first == "" || !strings.Contains(c.URL(), first) {
		t.Fatalf("addr %q, url %q", first, c.URL())
	}

	// Where a config was read from is not what the console is.
	same := *cfg
	if same.At, err = config.ParseValue([]byte(`{}`), "fit.json"); err != nil {
		t.Fatal(err)
	}
	same.Users = []config.ConsoleUser{{Username: "admin", PasswordHash: h}}
	if err := c.Apply(&same); err != nil || c.Addr() != first {
		t.Fatalf("the same console moved: %v, %s", err, c.Addr())
	}

	// Another user, or another password, starts it anew.
	for _, users := range [][]config.ConsoleUser{
		{{Username: "root", PasswordHash: h}},
		{{Username: "root", PasswordHash: hash(t, "other")}},
		{{Username: "root", PasswordHash: h}, {Username: "two", PasswordHash: h}},
	} {
		next := *cfg
		next.Users = users
		if err := c.Apply(&next); err != nil {
			t.Fatal(err)
		}
		if c.Addr() == "" {
			t.Fatal("no console after a change")
		}
	}

	// A console that does not start leaves the one before running.
	bad := &config.RoleConsole{Address: "127.0.0.1:not-a-port", Users: cfg.Users}
	if err := c.Apply(bad); err == nil {
		t.Fatal("a bad address started")
	}
	if c.Addr() == "" {
		t.Fatal("the console before stopped")
	}

	if err := c.Apply(nil); err != nil || c.Addr() != "" || c.URL() != "" {
		t.Fatalf("none: %v, %q", err, c.Addr())
	}
	if err := c.Apply(nil); err != nil {
		t.Fatal(err)
	}
	// Without a console before, one that does not start leaves none.
	if err := c.Apply(bad); err == nil || c.Addr() != "" {
		t.Fatalf("%v, %q", err, c.Addr())
	}
}

func TestMember(t *testing.T) {
	m := Member{}
	var n int
	if err := m.Get("n", &n); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	for _, zero := range []any{nil, 0, "", []string{}, map[string]int{}} {
		m["z"] = json.RawMessage(`1`)
		if err := m.Set("z", zero); err != nil {
			t.Fatal(err)
		}
		if _, ok := m["z"]; ok {
			t.Errorf("%#v was kept", zero)
		}
	}
	if err := m.Set("n", 7); err != nil {
		t.Fatal(err)
	}
	if err := m.Get("n", &n); err != nil || n != 7 {
		t.Fatal(n, err)
	}
	if err := m.Set("c", make(chan int)); err == nil {
		t.Fatal("a channel was set")
	}
	var s string
	if err := m.Get("n", &s); err == nil {
		t.Fatal("a number decoded into a string")
	}
}

func TestEditJSON(t *testing.T) {
	for _, cur := range []string{"", "null"} {
		out, err := EditJSON([]byte(cur), func(m Member) error { return m.Set("a", 1) })
		if err != nil || string(out) != `{"a":1}` {
			t.Fatalf("%q: %s, %v", cur, out, err)
		}
	}
	if _, err := EditJSON([]byte(`[`), func(Member) error { return nil }); err == nil {
		t.Fatal("bad JSON was edited")
	}
	if _, err := EditJSON([]byte(`{}`), func(Member) error { return BadRequest("no") }); !errors.Is(err, ErrBadRequest) || err.Error() != "no" {
		t.Fatalf("a change that fails: %v", err)
	}
	if errors.Is(errors.New("x"), ErrBadRequest) {
		t.Fatal("any error is a bad request")
	}
}

func TestSweepEvery(t *testing.T) {
	for idle, want := range map[time.Duration]time.Duration{
		time.Second: time.Second, 40 * time.Second: 10 * time.Second, time.Hour: time.Minute,
	} {
		if got := SweepEvery(idle); got != want {
			t.Errorf("%v: %v", idle, got)
		}
	}
}

// editRole is a role that hands the test its Host.
type editRole struct{}

func (editRole) Apply(any) error          { return nil }
func (editRole) Stop(ctx context.Context) {}

var hosts = make(chan *app.Host, 1)

func init() {
	app.Register(app.Info{ID: "tunnel.hub", Member: func(c *config.Config) any {
		if c.Tunnel == nil {
			return nil
		}
		return c.Tunnel.Hub
	}, New: func(h *app.Host) (app.Role, error) {
		hosts <- h
		return editRole{}, nil
	}})
}

// Edit answers each way an edit can end.
func TestEdit(t *testing.T) {
	cur := []byte(`{"name":"s"}`)
	m := app.NewManager(nil, nil, nil, func(_ string, _ []string, edit func([]byte) ([]byte, error)) ([]string, error) {
		out, err := edit(cur)
		if err != nil {
			return nil, err
		}
		cur = out
		if strings.Contains(string(out), "warn") {
			return []string{"careful"}, nil
		}
		return nil, nil
	})
	defer m.Stop(context.Background())
	if err := m.Apply(&config.Config{Tunnel: &config.Tunnel{Hub: &config.TunnelHub{}}}); err != nil {
		t.Fatal(err)
	}
	h := <-hosts

	routes := func(api *console.API) {
		set := func(k string) func(Member) error { return func(m Member) error { return m.Set(k, true) } }
		api.Handle("GET", "/api/plain", func(r *console.Request) { Edit(h, r, set("plain"), nil) })
		api.Handle("GET", "/api/warn", func(r *console.Request) { Edit(h, r, set("warn"), nil) })
		api.Handle("GET", "/api/then", func(r *console.Request) {
			Edit(h, r, set("then"), func(ws []string) { _ = r.JSON(202, map[string]int{"warnings": len(ws)}) })
		})
		api.Handle("GET", "/api/bad", func(r *console.Request) {
			Edit(h, r, func(Member) error { return BadRequest("not so") }, nil)
		})
		api.Handle("GET", "/api/busy", func(r *console.Request) {
			Edit(h, r, func(Member) error { return app.ErrBusy }, nil)
		})
		api.Handle("GET", "/api/ok", OK)
		api.Handle("GET", "/api/fail", func(r *console.Request) { Fail(r, 409, "taken") })
	}
	c := NewConsole(&app.Host{Engine: tunneltest.Engine(t)}, "tunnel.test", routes)
	defer c.Stop(context.Background())
	if err := c.Apply(&config.RoleConsole{Address: "127.0.0.1:0", Users: []config.ConsoleUser{{Username: "admin", PasswordHash: hash(t, "pw")}}}); err != nil {
		t.Fatal(err)
	}
	base := strings.TrimSuffix(c.URL(), "/")
	jar, _ := cookiejar.New(nil)
	proxy := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	resp, err := proxy.Post(base+"/api/login", "application/json", strings.NewReader(`{"username":"admin","password":"pw"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("login: %d", resp.StatusCode)
	}

	for _, c := range []struct {
		path string
		code int
		body string
	}{
		{"/api/plain", 200, `"warnings":[]`},
		{"/api/warn", 200, `"warnings":["careful"]`},
		{"/api/then", 202, `"warnings":1`},
		{"/api/bad", 400, `not so`},
		{"/api/busy", 503, `too many`},
		{"/api/ok", 200, `"ok":true`},
		{"/api/fail", 409, `taken`},
	} {
		resp, err := proxy.Get(base + c.path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != c.code || !strings.Contains(string(body), c.body) {
			t.Errorf("%s: %d %s", c.path, resp.StatusCode, body)
		}
	}
	if want := `{"name":"s","plain":true,"then":true,"warn":true}`; string(cur) != want {
		t.Errorf("config %s", cur)
	}
}
