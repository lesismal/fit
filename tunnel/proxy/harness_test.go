//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd || windows

package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/tunnel/internal/common"
	"github.com/lesismal/fit/tunnel/internal/tunneltest"

	_ "github.com/lesismal/fit/tunnel/hub"
	_ "github.com/lesismal/fit/tunnel/node"
)

// The tests run the hub and node roles on an app.Manager, and the
// proxy beside them, on the host of tunneltest's role, which only hands
// its host over: the proxy's own registration is left out of the configs
// the manager applies, so that the test holds the Proxy.
func init() { tunneltest.RegisterHostRole() }

var hashOnce = sync.OnceValue(func() string {
	h, _ := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	return string(h)
})

func consoleJSON(addr string) string {
	return fmt.Sprintf(`{"address": %q, "users": [{"username": "admin", "password_hash": %q}]}`, addr, hashOnce())
}

func uplinkJSON(name, addr string) string {
	return fmt.Sprintf(`{"name": %q, "endpoints": [{"address": %q}], "password": "pw", "pool_size": 2, "retry_interval": "100ms", "ping_interval": "200ms"}`, name, addr)
}

// freeAddr is a local address nothing listens on, and no other test of
// the package was given.
func freeAddr(t *testing.T) string {
	t.Helper()
	return tunneltest.FreeAddr(t)
}

// nowhere is an address nothing listens on: port 1, a privileged port no
// test binds. A free port let go of could be another process's by the
// time it is dialed.
const nowhere = "127.0.0.1:1"

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); !cond(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// harness is a hub and a node on a manager, and a proxy.
type harness struct {
	t   *testing.T
	m   *app.Manager
	c   *Proxy
	log *logs.Logger

	mu  sync.Mutex
	doc map[string]any
}

// start runs the doc build returns, a config with a tunnel member, its
// proxy member run by the harness's Proxy. build picks the addresses it
// listens on with freeAddr; when one was taken before the harness bound
// it, the start is undone and build asked again, for other addresses.
func start(t *testing.T, build func() string) *harness {
	t.Helper()
	return tunneltest.Start(t, func() (*harness, error) { return tryStart(t, build()) })
}

// tryStart runs doc, and undoes what it started when it does not start.
func tryStart(t *testing.T, doc string) (_ *harness, err error) {
	t.Helper()
	h := &harness{t: t}
	if err := json.Unmarshal([]byte(doc), &h.doc); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	root, err := logs.New(nil, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	h.log = root.Logger(config.LoggerTunnel)
	// Each start has an engine of its own, which ends with the test.
	engine := tunneltest.Engine(t)
	h.m = app.NewManager(engine, nil, nil, h.edit)
	stop := func() {
		if h.c != nil {
			h.c.Stop(context.Background())
		}
		h.m.Stop(context.Background())
		_ = root.Close()
	}
	defer func() {
		if err != nil {
			stop()
		}
	}()
	proxy := cfg.Tunnel.Proxy
	cfg.Tunnel.Proxy = nil
	err = h.m.Apply(cfg)
	// The host is taken whether the rest applied or not: it is this
	// start's alone either way.
	host := *tunneltest.Host(t, engine)
	if err != nil {
		return nil, err
	}
	host.ID, host.Log = ID, h.log
	h.c = New(&host)
	if proxy != nil {
		if err := h.c.Apply(proxy); err != nil {
			return nil, err
		}
	}
	t.Cleanup(stop)
	return h, nil
}

// edit is the manager's editor: it changes the member at path of the
// harness's config, and applies the config.
func (h *harness) edit(_ string, path []string, edit func([]byte) ([]byte, error)) ([]string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	parent := h.doc
	for _, k := range path[:len(path)-1] {
		parent = parent[k].(map[string]any)
	}
	last := path[len(path)-1]
	old := parent[last]
	cur, _ := json.Marshal(old)
	out, err := edit(cur)
	if err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal(out, &v); err != nil {
		return nil, err
	}
	parent[last] = v
	data, _ := json.Marshal(h.doc)
	cfg, err := config.Load(data)
	if err != nil {
		parent[last] = old
		return nil, err
	}
	proxy := cfg.Tunnel.Proxy
	cfg.Tunnel.Proxy = nil
	if err := h.m.Apply(cfg); err != nil {
		return nil, err
	}
	if proxy != nil {
		return nil, h.c.Apply(proxy)
	}
	return nil, nil
}

// editProxy changes the proxy's member as its pages do.
func (h *harness) editProxy(change func(common.Member) error) error {
	_, err := h.edit("", []string{"tunnel", "proxy"}, func(cur []byte) ([]byte, error) { return common.EditJSON(cur, change) })
	return err
}

// waitNode waits for the proxy to hear of node's service through hub.
func (h *harness) waitNode(hub, node, service string) {
	h.t.Helper()
	waitFor(h.t, "the proxy to hear of "+node+"/"+service, func() bool {
		for _, s := range h.c.Status().Hubs {
			for _, n := range s.Nodes {
				for _, sv := range n.Services {
					if s.Name == hub && n.Name == node && sv.Name == service {
						return true
					}
				}
			}
		}
		return false
	})
}

// page is a logged-in browser of the proxy's console.
type page struct {
	t    *testing.T
	base string
	c    *http.Client
	csrf string
}

func login(t *testing.T, addr string) *page {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	p := &page{t: t, base: "http://" + addr, c: &http.Client{Jar: jar, Timeout: 30 * time.Second}}
	var s struct {
		CSRF string `json:"csrf"`
	}
	if code, body := p.do("POST", "/api/login", map[string]string{"username": "admin", "password": "secret"}, &s); code != 200 {
		t.Fatalf("login: %d %s", code, body)
	}
	p.csrf = s.CSRF
	return p
}

// do sends body, as JSON, and decodes the answer into out.
func (p *page) do(method, path string, body, out any) (int, string) {
	p.t.Helper()
	var r io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		r = bytes.NewReader(data)
	}
	req, _ := http.NewRequest(method, p.base+path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if p.csrf != "" {
		req.Header.Set("X-Fit-Csrf", p.csrf)
	}
	resp, err := p.c.Do(req)
	if err != nil {
		p.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if out != nil {
		_ = json.Unmarshal(data, out)
	}
	return resp.StatusCode, string(data)
}

// expect posts body and fails the test unless it is answered code, with a
// body that has want.
func (p *page) expect(path string, body any, code int, want string) {
	p.t.Helper()
	if got, s := p.do("POST", path, body, nil); got != code || !strings.Contains(s, want) {
		p.t.Errorf("POST %s %v: %d %s", path, body, got, s)
	}
}

// forward adds the forward body is, on a free address, and returns the
// address.
func (p *page) forward(body map[string]string) string {
	p.t.Helper()
	return tunneltest.Bind(p.t, "tcp", func(addr string) error {
		body["listen"] = addr
		code, s := p.do("POST", "/api/forwards", body, nil)
		if code == 200 && strings.Contains(s, "ok") {
			return nil
		}
		if tunneltest.IsTakenText(s) {
			// The forward is kept, with its error: it goes before
			// the next address is tried.
			p.do("POST", "/api/forwards/delete", map[string]string{"listen": addr}, nil)
		}
		return fmt.Errorf("POST /api/forwards %v: %d %s", body, code, s)
	})
}

// echoOnce sends msg through addr and reads it back.
func echoOnce(t *testing.T, addr, msg string) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(c, msg); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != msg {
		t.Fatalf("%q, %v", buf, err)
	}
	return c
}

// refused dials addr and expects the connection closed without an answer.
func refused(t *testing.T, addr string) {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		return
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	_, _ = c.Write([]byte("x"))
	if n, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatalf("%s answered %d bytes", addr, n)
	}
}
