package node

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

	"github.com/lesismal/fib"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/tunnel/internal/link"
	"github.com/lesismal/fit/tunnel/internal/proto"
	"github.com/lesismal/fit/tunnel/internal/transport"
	"github.com/lesismal/fit/tunnel/internal/tunneltest"
)

// A Node is made on the host of tunneltest's role, which runs nothing: it
// changes its member through the env's editor, as fit's admin.Configs
// would.
func init() { tunneltest.RegisterHostRole() }

// env is a Node on an engine of its own, with the member its pages and
// hubs edit.
type env struct {
	t *testing.T
	e *fib.Engine
	n *Node

	mu     sync.Mutex
	member string
	// hold, when not nil, holds each edit until it closes.
	hold chan struct{}
}

// newEnv returns a Node, not applied yet.
func newEnv(t *testing.T) *env {
	t.Helper()
	v := &env{t: t, e: tunneltest.Engine(t), member: "{}"}
	m := app.NewManager(v.e, nil, nil, v.edit)
	if err := m.Apply(&config.Config{}); err != nil {
		t.Fatal(err)
	}
	v.n = New(tunneltest.Host(t, v.e))
	t.Cleanup(func() {
		v.release()
		v.n.Stop(context.Background())
		m.Stop(context.Background())
	})
	return v
}

func load(member string) (*config.TunnelNode, error) {
	c, err := config.Load([]byte(`{"tunnel": {"node": ` + member + `}}`))
	if err != nil {
		return nil, err
	}
	return c.Tunnel.Node, nil
}

// apply applies member and keeps it as the config's.
func (v *env) apply(member string) error {
	v.t.Helper()
	cfg, err := load(member)
	if err != nil {
		v.t.Fatalf("config: %v\n%s", err, member)
	}
	v.setMember(member)
	return v.n.Apply(cfg)
}

func (v *env) setMember(member string) {
	v.mu.Lock()
	v.member = member
	v.mu.Unlock()
}

func (v *env) current() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.member
}

// holdEdits holds the edits from now on, until release.
func (v *env) holdEdits() {
	v.mu.Lock()
	v.hold = make(chan struct{})
	v.mu.Unlock()
}

func (v *env) release() {
	v.mu.Lock()
	if v.hold != nil {
		close(v.hold)
		v.hold = nil
	}
	v.mu.Unlock()
}

// edit is the Manager's editor: it changes the member and applies it, an
// Apply's error a warning.
func (v *env) edit(_ string, _ []string, edit func([]byte) ([]byte, error)) ([]string, error) {
	v.mu.Lock()
	hold := v.hold
	v.mu.Unlock()
	if hold != nil {
		<-hold
	}
	out, err := edit([]byte(v.current()))
	if err != nil {
		return nil, err
	}
	cfg, err := load(string(out))
	if err != nil {
		return nil, err
	}
	v.setMember(string(out))
	if err := v.n.Apply(cfg); err != nil {
		return []string{err.Error()}, nil
	}
	return nil, nil
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// busyAddr is a TCP address something listens on until the test ends.
func busyAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l.Addr().String()
}

// hub is a tunnel hub that registers whatever links to it, and
// hands the tests its links to call the node over.
type hub struct {
	t     *testing.T
	l     transport.Listener
	links chan *link.Link

	mu sync.Mutex
	// update answers the node's updates, or ok when nil.
	update  func(proto.Info) error
	updates chan proto.Info
	conn    uint32
}

func newHub(t *testing.T, e *fib.Engine) *hub {
	t.Helper()
	s := &hub{t: t, links: make(chan *link.Link, 16), updates: make(chan proto.Info, 16)}
	r := link.NewRouter()
	r.HandleCall(proto.MethodRegister, func(l *link.Link, body []byte) (any, error) {
		var req proto.RegisterReq
		if err := link.Decode(body, &req); err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.conn++
		rsp := &proto.RegisterRsp{Peer: 1, Conn: s.conn, Hub: "fake"}
		s.mu.Unlock()
		proto.Prove(&req, rsp, "pw")
		// The tests call the node over the link once the answer has gone.
		return &link.Then{Rsp: rsp, After: func() {
			select {
			case s.links <- l:
			default:
			}
		}}, nil
	}, false)
	r.HandleCall(proto.MethodPing, func(*link.Link, []byte) (any, error) { return &proto.PingRsp{}, nil }, true)
	r.HandleCall(proto.MethodUpdate, func(_ *link.Link, body []byte) (any, error) {
		var info proto.Info
		_ = link.Decode(body, &info)
		s.mu.Lock()
		f := s.update
		s.mu.Unlock()
		select {
		case s.updates <- info:
		default:
		}
		if f != nil {
			return nil, f(info)
		}
		return "ok", nil
	}, false)
	for _, m := range []proto.Method{proto.MethodTunnelData, proto.MethodTunnelAck, proto.MethodTunnelClose} {
		r.HandleNotify(m, func(*link.Link, []byte) {}, true)
	}
	l, err := transport.Listen(e, "127.0.0.1:0", transport.ListenOptions{}, func(c transport.Conn) transport.Handler {
		return link.New(c, r, nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	s.l = l
	return s
}

// link waits for a link of the node to register.
func (s *hub) link() *link.Link {
	s.t.Helper()
	select {
	case l := <-s.links:
		return l
	case <-time.After(10 * time.Second):
		s.t.Fatal("timed out waiting for the node to register")
		return nil
	}
}

// uplinkJSON is a hub member of a node, to the fake hub s.
func (s *hub) uplinkJSON(extra string) string {
	return fmt.Sprintf(`{"name": "hub", "endpoints": [{"address": %q}], "password": "pw", "pool_size": 1, "retry_interval": "100ms", "ping_interval": "200ms"%s}`, s.l.URL(), extra)
}

var hashOnce = sync.OnceValue(func() string {
	h, _ := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	return string(h)
})

// consoleJSON is a console member on addr with the user admin, password
// secret.
func consoleJSON(addr string) string {
	return fmt.Sprintf(`{"address": %q, "users": [{"username": "admin", "password_hash": %q}]}`, addr, hashOnce())
}

// page is a logged-in browser of the console.
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
	waitFor(t, "the console at "+addr, func() bool {
		code, _ := p.do("POST", "/api/login", map[string]string{"username": "admin", "password": "secret"}, &s)
		return code == 200
	})
	p.csrf = s.CSRF
	return p
}

// do sends body, as JSON or, a string, as it is, and decodes the answer
// into out.
func (p *page) do(method, path string, body, out any) (int, string) {
	var r io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		r = strings.NewReader(b)
	default:
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
		return 0, err.Error()
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if out != nil {
		_ = json.Unmarshal(data, out)
	}
	return resp.StatusCode, string(data)
}

// expect posts body and fails the test unless it is answered code.
func (p *page) expect(code int, path string, body any) string {
	p.t.Helper()
	got, s := p.do("POST", path, body, nil)
	if got != code {
		p.t.Fatalf("POST %s: %d %s, want %d", path, got, s, code)
	}
	return s
}
