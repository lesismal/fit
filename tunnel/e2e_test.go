package tunnel_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/admin"
	"github.com/lesismal/fit/admin/store"
	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/listener"
	"github.com/lesismal/fit/observe"
	"github.com/lesismal/fit/runtime"
	"github.com/lesismal/fit/tunnel/internal/tunneltest"

	_ "github.com/lesismal/fit/tunnel/hub"
	_ "github.com/lesismal/fit/tunnel/node"
	_ "github.com/lesismal/fit/tunnel/proxy"
)

func TestMain(m *testing.M) {
	// The store's key derivation, cheap for tests.
	store.DefaultParams = store.Params{Time: 1, MemoryKiB: 64, Threads: 1}
	os.Exit(m.Run())
}

// proc is a fit process: its config, listeners' engine and roles.
type proc struct {
	t       *testing.T
	configs *admin.Configs
	apps    *app.Manager
	server  *listener.Server
	rt      *runtime.Runtime
	stopped bool
}

// startFit runs doc as fit runs a config, its roles' pages writing back
// into it, and into st when that is not nil.
func startFit(t *testing.T, doc string, st *store.Store) *proc {
	t.Helper()
	data := []byte(doc)
	c, err := config.Load(data)
	if err != nil {
		t.Fatalf("config: %v\n%s", err, doc)
	}
	env := runtime.Env{Metrics: observe.NewMetrics()}
	g, err := runtime.Compile(c, env)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	rt := new(runtime.Runtime)
	rt.Swap(g)
	s, err := listener.Start(rt)
	if err != nil {
		t.Fatal(err)
	}
	p := &proc{t: t, rt: rt, server: s}
	source := admin.SourceFile
	if st != nil {
		source = admin.SourceStore
	}
	p.configs = admin.NewConfigs(rt, s, env, c, data, source, 16)
	if st != nil {
		p.configs.UseStore(nil, st.Write)
	}
	p.apps = app.NewManager(s.Engine(), nil, nil, p.configs.EditMember)
	if err := p.apps.Apply(c); err != nil {
		p.apps.Stop(context.Background())
		_ = s.Close()
		rt.Close()
		t.Fatalf("apps: %v", err)
	}
	p.configs.UseApps(p.apps)
	t.Cleanup(p.stop)
	return p
}

func (p *proc) stop() {
	if p.stopped {
		return
	}
	p.stopped = true
	p.apps.Stop(context.Background())
	_ = p.server.Close()
	p.rt.Close()
}

// member returns the running config's member at path, as JSON.
func (p *proc) member(path ...string) json.RawMessage {
	var v any
	_ = json.Unmarshal(p.configs.Current().Data(), &v)
	for _, k := range path {
		m, _ := v.(map[string]any)
		v = m[k]
	}
	data, _ := json.Marshal(v)
	return data
}

// freeAddr is a local address nothing listens on.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

// freeUDPAddr is a local UDP address nothing listens on.
func freeUDPAddr(t *testing.T) string {
	t.Helper()
	l, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.LocalAddr().String()
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
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

// page is a logged-in browser of a role's console.
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

// do sends body, as JSON, and decodes the answer into out.
func (p *page) do(method, path string, body, out any) (int, string) {
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
		return 0, err.Error()
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if out != nil {
		_ = json.Unmarshal(data, out)
	}
	return resp.StatusCode, string(data)
}

// post posts body and fails the test unless it is answered 200.
func (p *page) post(path string, body any) string {
	p.t.Helper()
	code, s := p.do("POST", path, body, nil)
	if code != 200 {
		p.t.Fatalf("POST %s: %d %s", path, code, s)
	}
	return s
}

// status gets /api/status into out.
func (p *page) status(out any) {
	p.t.Helper()
	if code, s := p.do("GET", "/api/status", nil, out); code != 200 {
		p.t.Fatalf("status: %d %s", code, s)
	}
}

// Views of the pages' status, as much as the tests read.
type hubStatus struct {
	Listen    []string `json:"listen"`
	Listeners []struct {
		Protocol, Address, URL, Error string
	} `json:"listeners"`
	QUICFingerprint string `json:"quic_fingerprint"`
	Socket          string `json:"socket"`
	Peers           []struct {
		ID   uint32 `json:"id"`
		Info struct {
			Name     string `json:"name"`
			Type     string `json:"type"`
			Services []struct {
				Name, Address string
			} `json:"services"`
		} `json:"info"`
		Conns int `json:"conns"`
	} `json:"peers"`
	Tunnels []struct {
		ID      string `json:"id"`
		Node    string `json:"node"`
		Service string `json:"service"`
	} `json:"tunnels"`
}

type uplinkStatus struct {
	Name, State, Error string
	Pool               int `json:"pool"`
	Tunnels            int `json:"tunnels"`
}

type nodeStatus struct {
	Services []struct{ Name, Address string } `json:"services"`
	Hubs     []uplinkStatus                   `json:"hubs"`
	Tunnels  []struct {
		ID string `json:"id"`
	} `json:"tunnels"`
}

type proxyStatus struct {
	Hubs []struct {
		uplinkStatus
		Nodes []struct {
			Name     string                  `json:"name"`
			Services []struct{ Name string } `json:"services"`
		} `json:"nodes"`
	} `json:"hubs"`
	Forwards []struct{ Listen, Addr, Error string } `json:"forwards"`
	Tunnels  []struct {
		ID string `json:"id"`
	} `json:"tunnels"`
}

// hub is one fit running a hub, a node and a proxy, all on its engine,
// the node reaching services echo, banner and stall.
type hub struct {
	proc                        *proc
	hub, node, proxy            *page
	echo, stall, banner         string
	stalled                     <-chan net.Conn
	echoListen, stallListen     string
	hubCon, nodeCon, cliCon     string
	bodyPassword, protocol, url string
}

type hubOpts struct {
	protocol     string
	bodyPassword string
}

// uplinkJSON is a hub member of a node or proxy, to url.
func uplinkJSON(url, password, body string) string {
	extra := ""
	if body != "" {
		extra = fmt.Sprintf(`, "body_password": %q`, body)
	}
	if strings.HasPrefix(url, "quic://") {
		extra += `, "quic_insecure": true`
	}
	return fmt.Sprintf(`{"name": "hub", "endpoints": [{"address": %q}], "password": %q, "pool_size": 2, "retry_interval": "100ms", "ping_interval": "500ms"%s}`, url, password, extra)
}

func startHub(t *testing.T, o hubOpts) *hub {
	t.Helper()
	h := &hub{protocol: o.protocol, bodyPassword: o.bodyPassword}
	h.echo = tunneltest.Echo(t, "")
	h.banner = tunneltest.Echo(t, "hello from the service\n")
	h.stalled, h.stall = tunneltest.Stall(t)
	h.echoListen, h.stallListen = freeAddr(t), freeAddr(t)
	h.hubCon, h.nodeCon, h.cliCon = freeAddr(t), freeAddr(t), freeAddr(t)
	var listen string
	switch o.protocol {
	case "quic":
		listen = freeUDPAddr(t)
		h.url = "quic://" + listen
	case "ws":
		listen = freeAddr(t)
		h.url = "ws://" + listen + "/tunnel"
	default:
		listen = freeAddr(t)
		h.url = listen
	}
	body := ""
	if o.bodyPassword != "" {
		body = fmt.Sprintf(`, "body_password": %q`, o.bodyPassword)
	}
	doc := fmt.Sprintf(`{"tunnel": {
 "hub": {"name": "hub", "password": "pw"%s, "listeners": [{"protocol": %q, "address": %q}], "console": %s},
 "node": {"name": "lab", "hubs": [%s], "services": [{"name": "echo", "address": %q}, {"name": "banner", "address": %q}, {"name": "stall", "address": %q}], "console": %s},
 "proxy": {"name": "me", "hubs": [%s], "forwards": [{"listen": %q, "node": "lab", "service": "echo"}, {"listen": %q, "node": "lab", "service": "stall"}], "console": %s}
}}`, body, protocolOf(o.protocol), listen, consoleJSON(h.hubCon),
		uplinkJSON(h.url, "pw", o.bodyPassword), h.echo, h.banner, h.stall, consoleJSON(h.nodeCon),
		uplinkJSON(h.url, "pw", o.bodyPassword), h.echoListen, h.stallListen, consoleJSON(h.cliCon))
	h.proc = startFit(t, doc, nil)
	h.hub, h.node, h.proxy = login(t, h.hubCon), login(t, h.nodeCon), login(t, h.cliCon)
	h.waitNode(t, "lab", "echo")
	return h
}

func protocolOf(p string) string {
	if p == "" {
		return "tcp"
	}
	return p
}

// waitNode waits for the proxy to hear of node's service.
func (h *hub) waitNode(t *testing.T, node, service string) {
	t.Helper()
	waitFor(t, "the proxy to hear of "+node+"/"+service, func() bool {
		var st proxyStatus
		h.proxy.status(&st)
		for _, s := range st.Hubs {
			for _, n := range s.Nodes {
				for _, sv := range n.Services {
					if n.Name == node && sv.Name == service {
						return true
					}
				}
			}
		}
		return false
	})
}

func echoOnce(t *testing.T, addr, msg string) {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(c, msg); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != msg {
		t.Fatalf("%q, %v", buf, err)
	}
}

func TestTunnel(t *testing.T) {
	for _, o := range []hubOpts{{protocol: "tcp"}, {protocol: "ws"}, {protocol: "quic"}, {protocol: "tcp", bodyPassword: "aes"}} {
		name := o.protocol
		if o.bodyPassword != "" {
			name += "+body"
		}
		t.Run(name, func(t *testing.T) { testTunnel(t, o) })
	}
}

func testTunnel(t *testing.T, o hubOpts) {
	h := startHub(t, o)

	t.Run("echo", func(t *testing.T) { echoOnce(t, h.echoListen, "ping") })

	// Far more than a tunnel's window goes each way at once, while the
	// writer does not read, so the windows have to pace it.
	t.Run("large transfer", func(t *testing.T) {
		const size = 16 << 20
		data := make([]byte, size)
		_, _ = rand.Read(data)
		var wg sync.WaitGroup
		for range 2 {
			wg.Go(func() {
				c, err := net.Dial("tcp", h.echoListen)
				if err != nil {
					t.Error(err)
					return
				}
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(90 * time.Second))
				go func() { _, _ = c.Write(data) }()
				got := make([]byte, size)
				if _, err := io.ReadFull(c, got); err != nil {
					t.Error(err)
					return
				}
				if !bytes.Equal(got, data) {
					t.Error("the echo differs")
				}
			})
		}
		wg.Wait()
	})

	// A service that does not read holds back the proxy that writes to
	// it, with no more than a window and the sockets' buffers between them.
	t.Run("slow service", func(t *testing.T) {
		c, err := net.Dial("tcp", h.stallListen)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		chunk := make([]byte, 64<<10)
		written := 0
		_ = c.SetWriteDeadline(time.Now().Add(2 * time.Second))
		for written < 256<<20 {
			n, err := c.Write(chunk)
			written += n
			if err != nil {
				break
			}
		}
		if written >= 256<<20 {
			t.Fatalf("wrote %d MiB to a service that reads nothing", written>>20)
		}
		t.Logf("held back after %.1f MiB", float64(written)/(1<<20))
		svc := <-h.stalled
		defer svc.Close()
		_ = c.SetWriteDeadline(time.Time{})
		_ = svc.SetReadDeadline(time.Now().Add(30 * time.Second))
		if n, err := io.CopyN(io.Discard, svc, int64(written)); err != nil {
			t.Fatalf("the service read %d of %d: %v", n, written, err)
		}
	})

	// A proxy that does not read holds back the service.
	t.Run("slow proxy", func(t *testing.T) {
		c, err := net.Dial("tcp", h.stallListen)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		_, _ = c.Write([]byte("x"))
		svc := <-h.stalled
		defer svc.Close()
		chunk := make([]byte, 64<<10)
		written := 0
		_ = svc.SetWriteDeadline(time.Now().Add(2 * time.Second))
		for written < 256<<20 {
			n, err := svc.Write(chunk)
			written += n
			if err != nil {
				break
			}
		}
		if written >= 256<<20 {
			t.Fatalf("the service wrote %d MiB to a proxy that reads nothing", written>>20)
		}
		_ = c.SetReadDeadline(time.Now().Add(30 * time.Second))
		if n, err := io.CopyN(io.Discard, c, int64(written)); err != nil {
			t.Fatalf("the proxy read %d of %d: %v", n, written, err)
		}
	})

	// A service that goes away mid-transfer closes the proxy's end.
	t.Run("cut service", func(t *testing.T) {
		c, err := net.Dial("tcp", h.stallListen)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		_, _ = c.Write(make([]byte, 1<<20))
		svc := <-h.stalled
		_, _ = io.CopyN(io.Discard, svc, 1000)
		_ = svc.Close()
		_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
		if _, err := io.Copy(io.Discard, c); err != nil && !isReset(err) {
			t.Fatalf("the proxy's end: %v", err)
		}
	})

	// A proxy that goes away mid-transfer closes the node's end, and the
	// hub's route.
	t.Run("cut proxy", func(t *testing.T) {
		c, err := net.Dial("tcp", h.echoListen)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = c.Write(make([]byte, 4<<20))
		waitFor(t, "the tunnel at the node", func() bool {
			var st nodeStatus
			h.node.status(&st)
			return len(st.Tunnels) == 1
		})
		_ = c.Close()
		waitFor(t, "the node's end to close", func() bool {
			var st nodeStatus
			h.node.status(&st)
			return len(st.Tunnels) == 0
		})
		waitFor(t, "the hub's route to go", func() bool {
			var st hubStatus
			h.hub.status(&st)
			return len(st.Tunnels) == 0
		})
	})

	t.Run("the hub's pages close a tunnel", func(t *testing.T) {
		c, err := net.Dial("tcp", h.echoListen)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		echoConn(t, c, "hi")
		var id string
		waitFor(t, "the tunnel at the hub", func() bool {
			var st hubStatus
			h.hub.status(&st)
			for _, r := range st.Tunnels {
				id = r.ID
				return true
			}
			return false
		})
		h.hub.post("/api/tunnels/close", map[string]string{"id": id})
		_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
		if n, err := c.Read(make([]byte, 8)); err != io.EOF && !isReset(err) {
			t.Fatalf("read %d, %v after the hub closed the tunnel", n, err)
		}
	})

	t.Run("the hub's pages set the node's services", func(t *testing.T) {
		var st hubStatus
		h.hub.status(&st)
		var id uint32
		for _, p := range st.Peers {
			if p.Info.Type == "node" {
				id = p.ID
			}
		}
		services := []map[string]string{{"name": "echo", "address": h.echo}, {"name": "banner", "address": h.banner}, {"name": "stall", "address": h.stall}, {"name": "more", "address": h.echo}}
		h.hub.post("/api/peers/services", map[string]any{"id": id, "services": services})
		// Written back into the node's member of the config.
		if m := h.proc.member("tunnel", "node", "services"); !strings.Contains(string(m), `"more"`) {
			t.Fatalf("the node's services in the config: %s", m)
		}
		h.waitNode(t, "lab", "more")
		// A service that is not host:port is refused, and the config kept.
		code, body := h.hub.do("POST", "/api/peers/services", map[string]any{"id": id, "services": []map[string]string{{"name": "bad", "address": "nowhere"}}}, nil)
		if code == 200 {
			t.Fatalf("a bad service was taken: %s", body)
		}
		if m := h.proc.member("tunnel", "node", "services"); !strings.Contains(string(m), `"more"`) {
			t.Fatalf("the node's services in the config: %s", m)
		}
	})

	t.Run("the proxy's pages add a forward", func(t *testing.T) {
		listen := freeAddr(t)
		h.proxy.post("/api/forwards", map[string]string{"listen": listen, "node": "lab", "service": "banner", "hub": "hub"})
		if m := h.proc.member("tunnel", "proxy", "forwards"); !strings.Contains(string(m), listen) {
			t.Fatalf("the forwards in the config: %s", m)
		}
		c, err := net.Dial("tcp", listen)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(10 * time.Second))
		want := "hello from the service\n"
		buf := make([]byte, len(want))
		if _, err := io.ReadFull(c, buf); err != nil || string(buf) != want {
			t.Fatalf("%q, %v", buf, err)
		}
		// Twice on one address is refused.
		if code, _ := h.proxy.do("POST", "/api/forwards", map[string]string{"listen": listen, "node": "lab", "service": "echo"}, nil); code == 200 {
			t.Fatal("two forwards on one address")
		}
		h.proxy.post("/api/forwards/delete", map[string]string{"listen": listen})
		if m := h.proc.member("tunnel", "proxy", "forwards"); strings.Contains(string(m), listen) {
			t.Fatalf("the forwards in the config: %s", m)
		}
	})

	// A change to the config that leaves the links alone keeps the tunnels
	// open.
	t.Run("an edit keeps the links", func(t *testing.T) {
		c, err := net.Dial("tcp", h.echoListen)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		echoConn(t, c, "before")
		h.proxy.post("/api/meta", map[string]string{"site": "lab"})
		h.node.post("/api/services", []map[string]string{{"name": "echo", "address": h.echo}, {"name": "stall", "address": h.stall}, {"name": "banner", "address": h.banner}, {"name": "more", "address": h.echo}, {"name": "again", "address": h.echo}})
		echoConn(t, c, "after")
	})
}

func echoConn(t *testing.T, c net.Conn, msg string) {
	t.Helper()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(c, msg); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != msg {
		t.Fatalf("%q, %v", buf, err)
	}
	_ = c.SetDeadline(time.Time{})
}

func isReset(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "reset") || strings.Contains(err.Error(), "closed"))
}

// The hub's pages add, change and remove listeners, written into the
// config; a quic listener gets a certificate the hub makes and keeps
// there; the node's pages move it to quic, pinned.
func TestHubPages(t *testing.T) {
	h := startHub(t, hubOpts{protocol: "tcp"})
	udp := freeUDPAddr(t)
	h.hub.post("/api/listeners", map[string]string{"protocol": "quic", "address": udp})
	waitFor(t, "the quic certificate in the config", func() bool {
		return strings.Contains(string(h.proc.member("tunnel", "hub", "quic")), "BEGIN CERTIFICATE")
	})
	var st hubStatus
	h.hub.status(&st)
	if st.QUICFingerprint == "" || len(st.Listeners) != 2 {
		t.Fatalf("status %+v", st)
	}
	for _, l := range st.Listeners {
		if l.Error != "" {
			t.Fatalf("listener %+v", l)
		}
	}
	// The node moves to the quic listener, pinned to the certificate.
	h.node.post("/api/hubs/update", map[string]any{"name": "hub", "hub": map[string]any{
		"name": "hub", "endpoints": []map[string]any{{"protocol": "quic", "address": udp, "enabled": true}}, "quic_pin": st.QUICFingerprint, "pool_size": 2}})
	if m := string(h.proc.member("tunnel", "node", "hubs")); !strings.Contains(m, "quic://"+udp) || !strings.Contains(m, `"password":"pw"`) || !strings.Contains(m, "retry_interval") {
		t.Fatalf("the node's hubs in the config: %s", m)
	}
	waitFor(t, "the node over quic", func() bool {
		var ns nodeStatus
		h.node.status(&ns)
		return len(ns.Hubs) == 1 && ns.Hubs[0].State == "registered"
	})
	echoOnce(t, h.echoListen, "over quic")

	// A listener that cannot listen is shown with why.
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	code, body := h.hub.do("POST", "/api/listeners", map[string]string{"protocol": "tcp", "address": busy.Addr().String()}, nil)
	if code != 200 || !strings.Contains(body, "warnings") {
		t.Fatalf("a listener on a busy address: %d %s", code, body)
	}
	h.hub.status(&st)
	failed := false
	for _, l := range st.Listeners {
		failed = failed || l.Address == busy.Addr().String() && l.Error != ""
	}
	if !failed {
		t.Fatalf("listeners %+v", st.Listeners)
	}
	h.hub.post("/api/listeners/delete", map[string]string{"protocol": "tcp", "address": busy.Addr().String()})
	// Two of one address are refused.
	if code, _ := h.hub.do("POST", "/api/listeners", map[string]string{"protocol": "quic", "address": udp}, nil); code == 200 {
		t.Fatal("a second listener on one address")
	}
	// A password changed keeps the links registered.
	h.hub.post("/api/settings", map[string]any{"body_password": "", "socket_address": ""})
	h.hub.status(&st)
	nodes := 0
	for _, p := range st.Peers {
		if p.Info.Type == "node" {
			nodes++
		}
	}
	if nodes != 1 {
		t.Fatalf("peers %+v", st.Peers)
	}
	// Without the session's CSRF token, nothing changes.
	saved := h.hub.csrf
	h.hub.csrf = ""
	if code, _ := h.hub.do("POST", "/api/listeners/delete", map[string]string{"protocol": "quic", "address": udp}, nil); code != 403 {
		t.Fatalf("without the CSRF token: %d", code)
	}
	h.hub.csrf = saved
}

// What the pages change is kept in the store, and read back from there
// when fit starts again.
func TestRestartFromStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fit.store")
	st, data, err := store.Open(path, "store-password")
	if err != nil || data != nil {
		t.Fatal(data, err)
	}
	listen, con := freeAddr(t), freeAddr(t)
	echo := tunneltest.Echo(t, "")
	doc := fmt.Sprintf(`{"tunnel": {"hub": {"name": "hub", "password": "pw", "listeners": [{"protocol": "tcp", "address": %q}]},
 "node": {"name": "lab", "hubs": [%s], "console": %s}}}`, listen, uplinkJSON(listen, "pw", ""), consoleJSON(con))
	if err := st.Write([]byte(doc)); err != nil {
		t.Fatal(err)
	}
	p := startFit(t, doc, st)
	pg := login(t, con)
	pg.post("/api/services", []map[string]string{{"name": "echo", "address": echo, "description": "kept"}})
	pg.post("/api/hubs", map[string]any{"name": "other", "endpoints": []map[string]any{{"protocol": "tcp", "address": freeAddr(t), "enabled": true}}, "password": "x"})
	p.stop()

	st2, data, err := store.Open(path, "store-password")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"kept"`) || !strings.Contains(string(data), `"other"`) {
		t.Fatalf("the store holds %s", data)
	}
	startFit(t, string(data), st2)
	pg = login(t, con)
	waitFor(t, "the node to register with its services", func() bool {
		var ns nodeStatus
		pg.status(&ns)
		return len(ns.Services) == 1 && ns.Services[0].Address == echo && len(ns.Hubs) == 2 && ns.Hubs[0].State == "registered"
	})
}

// A proxy's web proxy serves the hub's console through its encrypted
// console socket.
func TestWebProxy(t *testing.T) {
	sock, proxy, con := freeAddr(t), freeAddr(t), freeAddr(t)
	doc := fmt.Sprintf(`{"tunnel": {"hub": {"name": "hub", "password": "pw", "console": %s, "console_socket": {"address": %q, "password": "sock"}},
 "proxy": {"name": "me", "web_proxies": [{"name": "hub", "listen": %q, "url": "ws://%s/", "password": "sock"}]}}}`, consoleJSON(con), sock, proxy, sock)
	startFit(t, doc, nil)
	var body string
	waitFor(t, "the page through the proxy", func() bool {
		resp, err := http.Get("http://" + proxy + "/")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		body = string(data)
		return resp.StatusCode == 200
	})
	if !strings.Contains(body, "tunnel hub") {
		t.Fatalf("the page: %s", body)
	}
	// The console's login works through it too, and so its API.
	pg := login(t, proxy)
	var st hubStatus
	pg.status(&st)
	// A large static file arrives whole.
	resp, err := pg.c.Get("http://" + proxy + "/static/base.css")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || len(data) < 20000 {
		t.Fatalf("base.css: %d, %d bytes", resp.StatusCode, len(data))
	}
	// A wrong password gets nothing.
	other := freeAddr(t)
	doc2 := fmt.Sprintf(`{"tunnel": {"proxy": {"name": "x", "web_proxies": [{"listen": %q, "url": "ws://%s/", "password": "wrong"}]}}}`, other, sock)
	startFit(t, doc2, nil)
	c := &http.Client{Timeout: 5 * time.Second}
	if resp, err := c.Get("http://" + other + "/"); err == nil {
		resp.Body.Close()
		t.Fatalf("a wrong password got %s", resp.Status)
	}
}

// A node with the wrong password, or the wrong body password, does not
// register, and says why.
func TestWrongPasswords(t *testing.T) {
	listen := freeAddr(t)
	for _, tc := range []struct {
		name, password, body, want string
	}{
		{"password", "nope", "", "authentication failed"},
		{"body password", "pw", "other", "closed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			con := freeAddr(t)
			doc := fmt.Sprintf(`{"tunnel": {"hub": {"name": "hub", "password": "pw", "body_password": "aes", "listeners": [{"protocol": "tcp", "address": %q}]},
 "node": {"name": "lab", "hubs": [%s], "console": %s}}}`, listen, uplinkJSON(listen, tc.password, tc.body), consoleJSON(con))
			if tc.body == "" {
				doc = strings.Replace(doc, `, "body_password": "aes"`, "", 1)
			}
			p := startFit(t, doc, nil)
			pg := login(t, con)
			var ns nodeStatus
			waitFor(t, "the node to fail", func() bool {
				pg.status(&ns)
				return len(ns.Hubs) == 1 && ns.Hubs[0].State == "failed" && strings.Contains(ns.Hubs[0].Error, tc.want)
			})
			p.stop()
		})
	}
}

// Limits hold: past max_tunnels, a proxy's connection is closed at once.
func TestMaxTunnels(t *testing.T) {
	listen, fwd, con := freeAddr(t), freeAddr(t), freeAddr(t)
	echo := tunneltest.Echo(t, "")
	doc := fmt.Sprintf(`{"tunnel": {"hub": {"name": "hub", "password": "pw", "listeners": [{"protocol": "tcp", "address": %q}]},
 "node": {"name": "lab", "hubs": [%s], "services": [{"name": "echo", "address": %q}]},
 "proxy": {"name": "me", "max_tunnels": 2, "hubs": [%s], "forwards": [{"listen": %q, "node": "lab", "service": "echo"}], "console": %s}}}`,
		listen, uplinkJSON(listen, "pw", ""), echo, uplinkJSON(listen, "pw", ""), fwd, consoleJSON(con))
	startFit(t, doc, nil)
	h := &hub{proxy: login(t, con)}
	h.waitNode(t, "lab", "echo")
	var conns []net.Conn
	for range 2 {
		c, err := net.Dial("tcp", fwd)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		echoConn(t, c, "x")
		conns = append(conns, c)
	}
	c, err := net.Dial("tcp", fwd)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, _ = c.Write([]byte("y"))
	if n, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatalf("a third tunnel read %d", n)
	}
	_ = conns[0].Close()
	waitFor(t, "room for a tunnel", func() bool {
		c, err := net.Dial("tcp", fwd)
		if err != nil {
			return false
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(2 * time.Second))
		_, _ = c.Write([]byte("z"))
		_, err = c.Read(make([]byte, 1))
		return err == nil
	})
}
