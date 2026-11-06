package hub

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

// A Hub is made on the host of tunneltest's role, which runs nothing: it
// changes its member through the env's editor, as fit's admin.Configs
// would.
func init() { tunneltest.RegisterHostRole() }

// env is a Hub on an engine of its own, with the member its pages edit.
type env struct {
	t *testing.T
	e *fib.Engine
	s *Hub
	m *app.Manager

	mu sync.Mutex
	// member is the hub's member of the config as JSON, which edits
	// change and the hub is applied from.
	member string
	edited chan error
}

// newEnv returns a Hub, not applied yet.
func newEnv(t *testing.T) *env {
	t.Helper()
	v := &env{t: t, e: tunneltest.Engine(t), member: "{}", edited: make(chan error, 64)}
	m := app.NewManager(v.e, nil, nil, v.edit)
	if err := m.Apply(&config.Config{}); err != nil {
		t.Fatal(err)
	}
	v.s, v.m = New(tunneltest.Host(t, v.e)), m
	t.Cleanup(func() {
		v.s.Stop(context.Background())
		m.Stop(context.Background())
	})
	return v
}

// start applies member, which must apply.
func newHub(t *testing.T, member string) *env {
	t.Helper()
	v := newEnv(t)
	if err := v.apply(member); err != nil {
		t.Fatal(err)
	}
	return v
}

// load decodes member as the hub's member of a config.
func load(member string) (*config.TunnelHub, error) {
	c, err := config.Load([]byte(`{"tunnel": {"hub": ` + member + `}}`))
	if err != nil {
		return nil, err
	}
	return c.Tunnel.Hub, nil
}

// apply applies member and keeps it as the config's.
func (v *env) apply(member string) error {
	v.t.Helper()
	cfg, err := load(member)
	if err != nil {
		v.t.Fatalf("config: %v\n%s", err, member)
	}
	v.mu.Lock()
	v.member = member
	v.mu.Unlock()
	return v.s.Apply(cfg)
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

// edit is the Manager's editor: it changes the member and applies it, an
// Apply's error a warning.
func (v *env) edit(_ string, _ []string, edit func([]byte) ([]byte, error)) (warnings []string, err error) {
	defer func() {
		select {
		case v.edited <- err:
		default:
		}
	}()
	out, err := edit([]byte(v.current()))
	if err != nil {
		return nil, err
	}
	cfg, err := load(string(out))
	if err != nil {
		return nil, err
	}
	v.setMember(string(out))
	if err := v.s.Apply(cfg); err != nil {
		return []string{err.Error()}, nil
	}
	return nil, nil
}

// waitEdit waits for an edit to end, and returns its error.
func (v *env) waitEdit() error {
	v.t.Helper()
	select {
	case err := <-v.edited:
		return err
	case <-time.After(10 * time.Second):
		v.t.Fatal("timed out waiting for an edit")
		return nil
	}
}

// listen is the URL of the hub's first listener.
func (v *env) listen() string {
	v.t.Helper()
	st := v.s.Status()
	if len(st.Listen) == 0 {
		v.t.Fatalf("no listener open: %+v", st.Listeners)
	}
	return st.Listen[0]
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

// note is a notify a peer received.
type note struct {
	m    proto.Method
	body []byte
}

// peer is a node or proxy that speaks the links' protocol by hand: a node
// answers the hub's calls with what its funcs return.
type peer struct {
	t     *testing.T
	hub   *Hub
	l     *link.Link
	notes chan note
	rsp   proto.RegisterRsp

	mu       sync.Mutex
	info     proto.Info
	nodeInfo func() (any, error)
	services func([]proto.Service) (any, error)
	open     func(*proto.TunnelOpenReq) (any, error)
}

// dial links a peer to addr, not registered yet.
func (v *env) dial(addr string) *peer {
	v.t.Helper()
	p := &peer{t: v.t, hub: v.s, notes: make(chan note, 256)}
	r := link.NewRouter()
	r.HandleCall(proto.MethodNodeInfo, func(*link.Link, []byte) (any, error) {
		p.mu.Lock()
		f, info := p.nodeInfo, p.info
		p.mu.Unlock()
		if f != nil {
			return f()
		}
		return &info, nil
	}, false)
	r.HandleCall(proto.MethodNodeSetServices, func(_ *link.Link, body []byte) (any, error) {
		var services []proto.Service
		if err := link.Decode(body, &services); err != nil {
			return nil, err
		}
		p.mu.Lock()
		f := p.services
		if f == nil {
			p.info.Services = services
		}
		info := p.info
		p.mu.Unlock()
		if f != nil {
			return f(services)
		}
		return &info, nil
	}, false)
	r.HandleCall(proto.MethodTunnelOpen, func(_ *link.Link, body []byte) (any, error) {
		var req proto.TunnelOpenReq
		if err := link.Decode(body, &req); err != nil {
			return nil, err
		}
		p.mu.Lock()
		f := p.open
		p.mu.Unlock()
		if f != nil {
			return f(&req)
		}
		return &proto.TunnelOpenRsp{ID: req.ID, Addr: "127.0.0.1:1"}, nil
	}, false)
	for _, m := range []proto.Method{proto.MethodNodes, proto.MethodKick, proto.MethodTunnelData, proto.MethodTunnelAck, proto.MethodTunnelClose} {
		r.HandleNotify(m, func(_ *link.Link, body []byte) {
			select {
			case p.notes <- note{m: m, body: bytes.Clone(body)}:
			default:
			}
		}, true)
	}
	if _, err := transport.Dial(v.e, addr, 5*time.Second, transport.ClientOptions{}, func(c transport.Conn) transport.Handler {
		p.l = link.New(c, r, nil)
		return p.l
	}); err != nil {
		v.t.Fatal(err)
	}
	v.t.Cleanup(func() { _ = p.l.Close() })
	return p
}

// register registers p as typ name with password.
func (p *peer) register(typ, name, password string, services ...proto.Service) error {
	info := proto.NewInfo(name, typ)
	info.Services = services
	p.mu.Lock()
	p.info = info
	p.mu.Unlock()
	req := proto.RegisterReq{Info: info}
	proto.Sign(&req, password)
	if err := p.l.Call(proto.MethodRegister, &req, &p.rsp, 5*time.Second); err != nil {
		return err
	}
	// Every answer the hub gives proves it knows the password.
	return proto.CheckProof(&req, &p.rsp, password)
}

// mustRegister registers p, which must succeed.
func (p *peer) mustRegister(typ, name string, services ...proto.Service) *peer {
	p.t.Helper()
	if err := p.register(typ, name, "pw", services...); err != nil {
		p.t.Fatalf("register %s %s: %v", typ, name, err)
	}
	p.waitReady(typ, name)
	return p
}

// waitReady waits until the hub sends on p's link: it marks a link so once
// its answer to the registration has gone out, which may be after p has read
// the answer, and until then a tunnel to p finds it disconnected.
func (p *peer) waitReady(typ, name string) {
	p.t.Helper()
	waitFor(p.t, "the hub to send on "+typ+" "+name+"'s link", func() bool {
		hp := p.hub.peerByName(typ, name)
		if hp == nil {
			return false
		}
		for _, l := range p.hub.peerLinks(hp) {
			if c := connOf(l); c != nil && c.ID == p.rsp.Conn {
				return true
			}
		}
		return false
	})
}

func (p *peer) call(m proto.Method, req, rsp any) error {
	return p.l.Call(m, req, rsp, 5*time.Second)
}

// wait waits for a notify of m, skipping others.
func (p *peer) wait(m proto.Method) note {
	p.t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case n := <-p.notes:
			if n.m == m {
				return n
			}
		case <-deadline:
			p.t.Fatalf("timed out waiting for a %v notify", m)
		}
	}
}

// waitClosed waits for p's link to close.
func (p *peer) waitClosed() {
	p.t.Helper()
	select {
	case <-p.l.Done():
	case <-time.After(10 * time.Second):
		p.t.Fatal("the link did not close")
	}
}

// fakeConn is a connection that sends nowhere, for a link the tests drive
// by hand.
type fakeConn struct{}

func (fakeConn) Send([]byte) error    { return nil }
func (fakeConn) Close() error         { return nil }
func (fakeConn) CloseAfterSend()      {}
func (fakeConn) LocalAddr() net.Addr  { return &net.TCPAddr{} }
func (fakeConn) RemoteAddr() net.Addr { return &net.TCPAddr{} }

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
