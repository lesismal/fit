package role

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	goruntime "runtime"
	"runtime/debug"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fib"
	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/llm/internal/mesh"
	"github.com/lesismal/fit/llm/internal/model"
	"github.com/lesismal/fit/llm/internal/wire"
	"github.com/lesismal/fit/upstream"
)

// testEngine is the engine the roles listen and dial on.
var testEngine = sync.OnceValue(func() *fib.Engine {
	e, _, err := upstream.StartEngine()
	if err != nil {
		panic(err)
	}
	return e
})

// The hub's member hands the tests the app.Host a Manager makes: no hub
// package is linked into this binary, so the ID is free.
var (
	hostMu   sync.Mutex
	lastHost *app.Host
)

type nopRole struct{}

func (nopRole) Apply(any) error          { return nil }
func (nopRole) Stop(ctx context.Context) {}

func init() {
	app.Register(app.Info{
		ID: "llm.hub",
		Member: func(c *config.Config) any {
			if c.LLM == nil || c.LLM.Hub == nil {
				return nil
			}
			return c.LLM.Hub
		},
		New: func(h *app.Host) (app.Role, error) {
			hostMu.Lock()
			lastHost = h
			hostMu.Unlock()
			return nopRole{}, nil
		},
	})
}

// editor is a Manager's Editor over one member, kept as JSON.
type editor struct {
	mu   sync.Mutex
	cur  []byte
	ws   []string
	err  error
	gate chan struct{} // when set, each edit waits for it
	done chan struct{} // signalled after each edit
}

func (e *editor) edit(_ string, _ []string, fn func([]byte) ([]byte, error)) ([]string, error) {
	if e.gate != nil {
		<-e.gate
	}
	defer func() { e.done <- struct{}{} }()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil {
		return e.ws, e.err
	}
	b, err := fn(e.cur)
	if err != nil {
		return nil, err
	}
	e.cur = b
	return e.ws, nil
}

func (e *editor) member() map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	m := map[string]any{}
	_ = json.Unmarshal(e.cur, &m)
	return m
}

// newBase returns a base of role on a host whose edits go to ed.
func newBase(t *testing.T, role string, ed *editor) *Base {
	t.Helper()
	ed.done = make(chan struct{}, 16)
	mgr := app.NewManager(testEngine(), nil, nil, ed.edit)
	if err := mgr.Apply(&config.Config{LLM: &config.LLM{Hub: &config.LLMHub{}}}); err != nil {
		t.Fatal(err)
	}
	hostMu.Lock()
	h := lastHost
	hostMu.Unlock()
	b := New(h, Spec{Role: role, Me: func() map[string]any { return map[string]any{"extra": 1} }})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		b.Stop(ctx)
		mgr.Stop(ctx)
	})
	return b
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for range 400 {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func waitEdit(t *testing.T, ed *editor) {
	t.Helper()
	select {
	case <-ed.done:
	case <-time.After(5 * time.Second):
		t.Fatal("no edit")
	}
}

func consoleCfg(t *testing.T) *config.RoleConsole {
	t.Helper()
	hash, _ := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	return &config.RoleConsole{Address: "127.0.0.1:0", Users: []config.ConsoleUser{{Username: "admin", PasswordHash: string(hash)}}}
}

func TestApplyWritesID(t *testing.T) {
	ed := &editor{}
	b := newBase(t, model.RoleHub, ed)
	if err := b.Apply(Common{Name: "h"}); err != nil {
		t.Fatal(err)
	}
	waitEdit(t, ed)
	id, _ := ed.member()["id"].(string)
	if !strings.HasPrefix(id, "hub-") || b.Self().ID != id || b.Self().Name != "h" {
		t.Fatalf("id written = %q, self = %+v", id, b.Self())
	}
	// A later Apply without an ID keeps it and writes nothing.
	if err := b.Apply(Common{}); err != nil {
		t.Fatal(err)
	}
	if b.Self().ID != id || b.Self().Name == "" {
		t.Fatalf("self after a second Apply = %+v", b.Self())
	}

	// The write-back failing is only logged.
	ed2 := &editor{err: errors.New("read-only")}
	b2 := newBase(t, model.RoleHub, ed2)
	if err := b2.Apply(Common{}); err != nil {
		t.Fatal(err)
	}
	waitEdit(t, ed2)
	if b2.Self().ID == "" {
		t.Fatal("no ID made")
	}
}

// Without admin.store, a self_signed listener's certificate is kept in a
// file beside the config, 0600, not written into the member: a restart
// presents the same one. A file that does not hold one leaves the listener
// without TLS, and the file as it is.
func TestSelfSignedKeptBesideConfig(t *testing.T) {
	dir := t.TempDir()
	listen := &config.LLMListen{Address: freeAddr(t), Password: "pw", SelfSigned: true}
	start := func() (*Base, *editor) {
		ed := &editor{done: make(chan struct{}, 16)}
		mgr := app.NewManager(testEngine(), nil, nil, ed.edit)
		mgr.KeepBeside(filepath.Join(dir, "fit.yaml"))
		if err := mgr.Apply(&config.Config{LLM: &config.LLM{Hub: &config.LLMHub{}}}); err != nil {
			t.Fatal(err)
		}
		hostMu.Lock()
		h := lastHost
		hostMu.Unlock()
		b := New(h, Spec{Role: model.RoleHub})
		t.Cleanup(func() {
			b.Stop(context.Background())
			mgr.Stop(context.Background())
		})
		if err := b.Apply(Common{ID: "hub-1", Listen: listen}); err != nil {
			t.Fatal(err)
		}
		return b, ed
	}
	b, ed := start()
	fp := b.Mesh.Listen().Fingerprint
	kept := filepath.Join(dir, "fit.llm.hub.pem")
	fi, err := os.Stat(kept)
	if err != nil || len(fp) != 64 {
		t.Fatalf("kept %v, fingerprint %q", err, fp)
	}
	if goruntime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	select {
	case <-ed.done:
		t.Fatalf("the member was edited: %s", ed.cur)
	case <-time.After(100 * time.Millisecond):
	}
	b.Stop(context.Background())
	b2, _ := start()
	if b2.Mesh.Listen().Fingerprint != fp {
		t.Fatalf("restarted with %s, was %s", b2.Mesh.Listen().Fingerprint, fp)
	}
	// Each start listens on the same address, which the one before must
	// leave first: it would be refused as taken.
	b2.Stop(context.Background())

	if err := os.WriteFile(kept, []byte("nothing"), 0o600); err != nil {
		t.Fatal(err)
	}
	b3, _ := start()
	if b3.Mesh.Listen().Fingerprint != "" || b3.Mesh.Listen().Error == "" {
		t.Fatalf("a bad file: %+v", b3.Mesh.Listen())
	}
	b3.Stop(context.Background())
	if data, _ := os.ReadFile(kept); string(data) != "nothing" {
		t.Fatal("the bad file was overwritten")
	}

	// Where nothing can be written, the certificate goes into the member,
	// as with a store, and the listener runs.
	dir = filepath.Join(dir, "none")
	b4, ed4 := start()
	if b4.Mesh.Listen().Fingerprint == "" {
		t.Fatalf("unwritable: %+v", b4.Mesh.Listen())
	}
	waitEdit(t, ed4)
}

func TestListenCertFiles(t *testing.T) {
	if f := ListenCertFiles(nil); f != nil {
		t.Fatal(f)
	}
	if f := ListenCertFiles(&config.LLMListen{SelfSigned: true}); f != nil {
		t.Fatal(f)
	}
	if f := ListenCertFiles(&config.LLMListen{CertFile: "c", KeyFile: "k"}); len(f) != 2 || f[0] != "c" || f[1] != "k" {
		t.Fatal(f)
	}
}

func TestApplyAfterStop(t *testing.T) {
	b := newBase(t, model.RoleHub, &editor{})
	b.Stop(context.Background())
	if err := b.Apply(Common{ID: "x"}); err == nil || err.Error() != "stopped" {
		t.Fatalf("Apply after Stop = %v", err)
	}
}

func TestApplyBadPeerOnlyWarns(t *testing.T) {
	b := newBase(t, model.RoleNode, &editor{})
	err := b.Apply(Common{ID: "node-1", Peers: []config.LLMPeer{{Address: "not an address", Role: model.RoleHub}}})
	if err != nil {
		t.Fatalf("a peer that cannot be dialed failed Apply: %v", err)
	}
}

func TestApplyNewIDKicksPeers(t *testing.T) {
	addr := freeAddr(t)
	hub := newBase(t, model.RoleHub, &editor{})
	if err := hub.Apply(Common{ID: "hub-a", Listen: &config.LLMListen{Address: addr, Password: "pw"}}); err != nil {
		t.Fatal(err)
	}
	node := newBase(t, model.RoleNode, &editor{})
	if err := node.Apply(Common{ID: "node-a", Peers: []config.LLMPeer{{Address: addr, Password: "pw", Role: model.RoleHub}}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the link", func() bool { return node.Mesh.Online("hub-a") })
	// The hub's ID changes: its peers are kicked and say hello to the new one.
	if err := hub.Apply(Common{ID: "hub-b", Listen: &config.LLMListen{Address: addr, Password: "pw"}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the link to the new ID", func() bool { return node.Mesh.Online("hub-b") && !node.Mesh.Online("hub-a") })
	if got := hub.Self().RPCAddr; got != addr {
		t.Fatalf("RPCAddr = %q, want %q", got, addr)
	}
}

func TestConsole(t *testing.T) {
	b := newBase(t, model.RoleHub, &editor{})
	cfg := consoleCfg(t)
	if err := b.Apply(Common{ID: "hub-c", Console: cfg}); err != nil {
		t.Fatal(err)
	}
	c := b.Console()
	if c == nil || b.Self().HTTPURL == "" {
		t.Fatalf("console = %v, self = %+v", c, b.Self())
	}
	// The same console again is left alone.
	if err := b.Apply(Common{ID: "hub-c", Console: cfg}); err != nil || b.Console() != c {
		t.Fatalf("same console restarted: %v", err)
	}
	// A changed one replaces it.
	cfg2 := *cfg
	cfg2.MaxSessions = 3
	if err := b.Apply(Common{ID: "hub-c", Console: &cfg2}); err != nil || b.Console() == c || b.Console() == nil {
		t.Fatalf("changed console: %v", err)
	}
	// One that cannot start leaves no console, and its error.
	bad := *cfg
	bad.CertFile, bad.KeyFile = "/nonexistent/cert.pem", "/nonexistent/key.pem"
	if err := b.Apply(Common{ID: "hub-c", Console: &bad}); err == nil || !strings.HasPrefix(err.Error(), "console:") {
		t.Fatalf("bad console = %v", err)
	}
	if b.Console() != nil {
		t.Fatal("a console is left after a failed start")
	}
	// No console at all.
	if err := b.Apply(Common{ID: "hub-c"}); err != nil || b.Console() != nil {
		t.Fatalf("no console: %v", err)
	}
	me := b.me()
	if me["extra"] != 1 || me["identity"].(model.Identity).ID != "hub-c" {
		t.Fatalf("me = %v", me)
	}
}

func TestStopWithConsole(t *testing.T) {
	b := newBase(t, model.RoleHub, &editor{})
	b.Register(b)
	if Find(b.H.Engine, model.RoleHub) != b {
		t.Fatal("not registered")
	}
	if err := b.Apply(Common{ID: "hub-s", Console: consoleCfg(t)}); err != nil {
		t.Fatal(err)
	}
	b.Stop(context.Background())
	if b.Console() != nil || Find(b.H.Engine, model.RoleHub) != nil {
		t.Fatal("Stop left the console or the registration")
	}
}

func TestEdit(t *testing.T) {
	ed := &editor{ws: []string{"a warning"}}
	b := newBase(t, model.RoleHub, ed)
	if err := b.SetName(context.Background(), "named"); err != nil {
		t.Fatal(err)
	}
	if ed.member()["name"] != "named" {
		t.Fatalf("member = %v", ed.member())
	}
	// fn's own error.
	boom := errors.New("boom")
	if err := b.Edit(context.Background(), func(map[string]json.RawMessage) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("fn error = %v", err)
	}
	// A config that does not load is the caller's fault.
	for _, cerr := range []error{&config.Error{Msg: "bad"}, config.Errors{&config.Error{Msg: "bad"}}} {
		ed.mu.Lock()
		ed.err = cerr
		ed.mu.Unlock()
		err := b.Edit(context.Background(), func(map[string]json.RawMessage) error { return nil })
		var we *wire.Error
		if !errors.As(err, &we) || we.Code != model.CodeBadRequest || !strings.Contains(we.Msg, "bad") {
			t.Fatalf("config error = %v", err)
		}
	}
	// ctx ends while the edit waits.
	ed.mu.Lock()
	ed.err = nil
	ed.mu.Unlock()
	ed.gate = make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := b.SetName(ctx, "late"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Edit past ctx = %v", err)
	}
	close(ed.gate)
}

func TestEditMesh(t *testing.T) {
	ed := &editor{cur: []byte(`{"listen":{"address":"127.0.0.1:1","password":"p"},"peers":[{"address":"h:1","password":"q","role":"hub"}]}`)}
	b := newBase(t, model.RoleNode, ed)
	var seen mesh.Settings
	err := b.EditMesh(context.Background(), func(s *mesh.Settings) error {
		seen = *s
		s.Listen = nil
		s.Peers = append(s.Peers, model.DialReq{Addr: "h:2", Role: model.RoleProxy})
		return nil
	})
	if err != nil || seen.Listen == nil || seen.Listen.Addr != "127.0.0.1:1" || len(seen.Peers) != 1 || seen.Peers[0].Password != "q" {
		t.Fatalf("EditMesh saw %+v: %v", seen, err)
	}
	m := ed.member()
	if _, ok := m["listen"]; ok || len(m["peers"].([]any)) != 2 {
		t.Fatalf("member = %v", m)
	}
	// Setting the listener writes it.
	if err := b.EditMesh(context.Background(), func(s *mesh.Settings) error {
		s.Listen = &model.ListenConfig{Addr: "127.0.0.1:2"}
		s.Peers = nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if m := ed.member(); m["listen"] == nil || m["peers"] != nil {
		t.Fatalf("member = %v", m)
	}
	// The roles' own passwords are read and written with the listener.
	ed.mu.Lock()
	ed.cur = []byte(`{"listen":{"address":"127.0.0.1:1","hub_password":"h","proxy_password":"x"}}`)
	ed.mu.Unlock()
	if err := b.EditMesh(context.Background(), func(s *mesh.Settings) error {
		if s.Listen == nil || s.Listen.HubPassword != "h" || s.Listen.ProxyPassword != "x" || s.Listen.Password != "" {
			return fmt.Errorf("listen %+v", s.Listen)
		}
		s.Listen.Addr, s.Listen.Password = "127.0.0.1:3", "p"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if l, _ := ed.member()["listen"].(map[string]any); l["hub_password"] != "h" || l["proxy_password"] != "x" || l["password"] != "p" ||
		l["node_password"] != nil || l["address"] != "127.0.0.1:3" {
		t.Fatalf("listen = %v", l)
	}
	// Members that do not decode, and fn's error.
	boom := errors.New("boom")
	for _, tc := range []struct {
		cur string
		fn  func(*mesh.Settings) error
	}{
		{`{"listen":1}`, func(*mesh.Settings) error { return nil }},
		{`{"peers":{}}`, func(*mesh.Settings) error { return nil }},
		{`{}`, func(*mesh.Settings) error { return boom }},
	} {
		ed.mu.Lock()
		ed.cur = []byte(tc.cur)
		ed.mu.Unlock()
		if err := b.EditMesh(context.Background(), tc.fn); err == nil {
			t.Fatalf("EditMesh on %s did not fail", tc.cur)
		}
	}
}

func TestEditJSONAndSet(t *testing.T) {
	keep := func(map[string]json.RawMessage) error { return nil }
	for _, cur := range []string{"", " null ", `{"a": 1}`} {
		if _, err := EditJSON([]byte(cur), keep); err != nil {
			t.Fatalf("EditJSON(%q) = %v", cur, err)
		}
	}
	if _, err := EditJSON([]byte("[1]"), keep); err == nil {
		t.Fatal("EditJSON of an array did not fail")
	}
	boom := errors.New("boom")
	if _, err := EditJSON(nil, func(map[string]json.RawMessage) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("fn error = %v", err)
	}

	m := map[string]json.RawMessage{"a": json.RawMessage("1")}
	if err := Set(m, "a", 0); err != nil || m["a"] != nil {
		t.Fatalf("Set zero = %v %v", m, err)
	}
	if err := Set(m, "b", "x"); err != nil || string(m["b"]) != `"x"` {
		t.Fatalf("Set = %v %v", m, err)
	}
	if err := Set(m, "c", make(chan int)); err == nil {
		t.Fatal("Set of a channel did not fail")
	}
}

func TestAdvertise(t *testing.T) {
	ip := hostIP()
	for _, tc := range []struct{ in, want string }{
		{"", ""},
		{"no port", "no port"},
		{"10.1.2.3:7", "10.1.2.3:7"},
		{"example.com:7", "example.com:7"},
		{":7", net.JoinHostPort(ip, "7")},
		{"0.0.0.0:7", net.JoinHostPort(ip, "7")},
		{"[::]:7", net.JoinHostPort(ip, "7")},
	} {
		if got := advertise(tc.in); got != tc.want {
			t.Errorf("advertise(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if a, b := NewID("node"), NewID("node"); a == b || !strings.HasPrefix(a, "node-") {
		t.Fatal("NewID")
	}
}

// TestVersion: a role announces the version its build recorded, and plain
// "fit" when the build recorded none.
func TestVersion(t *testing.T) {
	if got := version(&debug.BuildInfo{Main: debug.Module{Version: "v1.7.1"}}, true); got != "fit v1.7.1" {
		t.Fatalf("a versioned build: %q", got)
	}
	if got := version(&debug.BuildInfo{}, true); got != "fit" {
		t.Fatalf("a build without a version: %q", got)
	}
	if got := version(nil, false); got != "fit" {
		t.Fatalf("no build info: %q", got)
	}
}
