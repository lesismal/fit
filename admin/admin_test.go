package admin

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	_ "github.com/lesismal/fit/handler/respond"
	"github.com/lesismal/fit/listener"
	"github.com/lesismal/fit/observe"
	"github.com/lesismal/fit/runtime"
)

// doc is a config with one listener on a free port, answering body.
func doc(body string) string {
	return `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "routes": [{"handler": {"module": "respond", "params": {"body": "` + body + `"}}}]}`
}

// fixture is a running server with its Configs and Admin API.
type fixture struct {
	configs *Configs
	server  *listener.Server
	admin   *Server
	api     string // the Admin API's base URL
	client  *http.Client
}

// start runs doc with the Admin API a, whose address is replaced by a free
// loopback port when it has none, counting requests as fit run does.
func start(t *testing.T, doc string, a *config.Admin) *fixture {
	t.Helper()
	return startEnv(t, doc, a, runtime.Env{Metrics: observe.NewMetrics()})
}

// startEnv is start with env for compiling configs.
func startEnv(t *testing.T, doc string, a *config.Admin, env runtime.Env) *fixture {
	t.Helper()
	data := []byte(doc)
	c, err := config.Load(data)
	if err != nil {
		t.Fatal(err)
	}
	g, err := runtime.Compile(c, env)
	if err != nil {
		t.Fatal(err)
	}
	rt := new(runtime.Runtime)
	rt.Swap(g)
	s, err := listener.Start(rt)
	if err != nil {
		t.Fatal(err)
	}
	if a.Address == "" {
		a.Address = "127.0.0.1:0"
	}
	f := &fixture{configs: NewConfigs(rt, s, env, c, data, SourceFile, a.HistoryLen()), server: s,
		client: &http.Client{Timeout: 10 * time.Second}}
	if f.admin, err = Start(a, f.configs, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = f.admin.Shutdown(context.Background())
		_ = s.Close()
		rt.Close()
	})
	f.api = "http://" + f.admin.Addr().String()
	return f
}

func (f *fixture) do(t *testing.T, req *http.Request) (*http.Response, string) {
	t.Helper()
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(body)
}

func (f *fixture) load(t *testing.T, config string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", f.api+"/load", strings.NewReader(config))
	req.Header.Set("Content-Type", "application/json")
	return f.do(t, req)
}

func (f *fixture) get(t *testing.T, path string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", f.api+path, nil)
	return f.do(t, req)
}

// served returns what the server's listener answers.
func (f *fixture) served(t *testing.T) string {
	t.Helper()
	resp, err := f.client.Get("http://" + f.server.Addrs()[0].Addr.String() + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return string(body)
}

func TestLoadAndReadBack(t *testing.T) {
	f := start(t, doc("v1"), &config.Admin{})
	if resp, body := f.get(t, "/config"); resp.StatusCode != 200 || body != doc("v1") || resp.Header.Get(VersionHeader) != "1" {
		t.Fatalf("GET /config: %d %v %q", resp.StatusCode, resp.Header, body)
	}
	resp, body := f.load(t, doc("v2"))
	if resp.StatusCode != 200 || resp.Header.Get(VersionHeader) != "2" || strings.TrimSpace(body) != `{"version":2,"etag":`+strconv.Quote(resp.Header.Get("ETag"))+`}` {
		t.Fatalf("POST /load: %d %v %s", resp.StatusCode, resp.Header, body)
	}
	if got := f.served(t); got != "v2" {
		t.Fatalf("served %q after the load", got)
	}
	if resp, body := f.get(t, "/config"); body != doc("v2") || resp.Header.Get(VersionHeader) != "2" {
		t.Fatalf("GET /config after the load: %v %q", resp.Header, body)
	}
	if _, body := f.get(t, "/versions/1"); body != doc("v1") {
		t.Fatalf("GET /versions/1: %q", body)
	}
	if resp, _ := f.get(t, "/versions/7"); resp.StatusCode != 404 {
		t.Fatalf("GET /versions/7: %d", resp.StatusCode)
	}
	if resp, _ := f.get(t, "/load"); resp.StatusCode != 405 {
		t.Fatalf("GET /load: %d", resp.StatusCode)
	}
}

func TestLoadRefused(t *testing.T) {
	f := start(t, doc("v1"), &config.Admin{MaxConfigBytes: 1024})
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()

	for _, tt := range []struct {
		name, contentType, body string
		status                  int
		want                    string
	}{
		{"invalid", "application/json", `{"listeners": [{"name": "web", "address": "127.0.0.1:0", "bogus": 1}]}`, 400,
			`{"error":"the config was not applied; the running config stays","errors":[{"path":"listeners[0].bogus","line":1,"column":58,"message":"unknown field"}]}`},
		{"not compiled", "application/json", `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}], "routes": [{"handler": {"module": "nope"}}]}`, 400,
			`there is no module \"nope\"`},
		{"not bound", "application/json", `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}, {"name": "b", "address": "` + taken.Addr().String() + `"}]}`, 500,
			`address already in use`},
		{"not JSON", "text/plain", doc("v2"), 415, `application/json`},
		{"too large", "application/json", strings.Repeat(" ", 1025) + doc("v2"), 413, `the body is larger than max_config_bytes, 1024`},
	} {
		req, _ := http.NewRequest("POST", f.api+"/load", strings.NewReader(tt.body))
		req.Header.Set("Content-Type", tt.contentType)
		resp, body := f.do(t, req)
		if resp.StatusCode != tt.status || !strings.Contains(body, tt.want) {
			t.Errorf("%s: %d %s", tt.name, resp.StatusCode, body)
		}
	}
	// A body too large that does not say its length is cut off at the limit.
	req, _ := http.NewRequest("POST", f.api+"/load", io.MultiReader(strings.NewReader(strings.Repeat(" ", 2048)), strings.NewReader(doc("v2"))))
	req.Header.Set("Content-Type", "application/json")
	if resp, body := f.do(t, req); resp.StatusCode != 413 || req.ContentLength != 0 {
		t.Errorf("chunked, too large: %d %s", resp.StatusCode, body)
	}

	if got := f.served(t); got != "v1" {
		t.Fatalf("served %q", got)
	}
	if v := f.configs.Current(); v.Number != 1 || len(f.configs.Versions()) != 1 {
		t.Fatalf("version %d, %d kept", v.Number, len(f.configs.Versions()))
	}
}

func TestToken(t *testing.T) {
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := start(t, doc("v1"), &config.Admin{TokenFile: file})
	for _, tt := range []struct {
		auth   string
		status int
	}{{"", 401}, {"Bearer wrong", 401}, {"Basic s3cret", 401}, {"Bearer s3cret", 200}} {
		req, _ := http.NewRequest("GET", f.api+"/config", nil)
		req.Header.Set("Authorization", tt.auth)
		// With a token, the Host field may be anything.
		req.Host = "fit.example.com"
		resp, _ := f.do(t, req)
		if resp.StatusCode != tt.status {
			t.Errorf("Authorization %q: %d", tt.auth, resp.StatusCode)
		}
		if tt.status == 401 && resp.Header.Get("WWW-Authenticate") == "" {
			t.Errorf("Authorization %q: no WWW-Authenticate", tt.auth)
		}
	}

	if err := os.WriteFile(file, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(&config.Admin{Address: "127.0.0.1:0", TokenFile: file}, f.configs, nil); err == nil || !strings.Contains(err.Error(), "is empty") {
		t.Fatalf("empty token file: %v", err)
	}
}

// TestBrowsersRefused checks what a web page could send: a request with
// Origin, and one to a name that resolves to 127.0.0.1 after DNS
// rebinding.
func TestBrowsersRefused(t *testing.T) {
	f := start(t, doc("v1"), &config.Admin{})
	for _, tt := range []struct {
		host, origin string
		status       int
	}{
		{"", "", 200},
		{"localhost:2019", "", 200},
		{"[::1]:2019", "", 200},
		{"evil.example.com:2019", "", 403},
		{"", "https://evil.example.com", 403},
		{"", "null", 403},
	} {
		req, _ := http.NewRequest("GET", f.api+"/config", nil)
		if tt.host != "" {
			req.Host = tt.host
		}
		if tt.origin != "" {
			req.Header.Set("Origin", tt.origin)
		}
		if resp, body := f.do(t, req); resp.StatusCode != tt.status {
			t.Errorf("Host %q Origin %q: %d %s", tt.host, tt.origin, resp.StatusCode, body)
		}
	}
}

func TestHistory(t *testing.T) {
	f := start(t, doc("v1"), &config.Admin{History: 2})
	for _, body := range []string{"v2", "v3"} {
		if resp, out := f.load(t, doc(body)); resp.StatusCode != 200 {
			t.Fatalf("load %s: %d %s", body, resp.StatusCode, out)
		}
	}
	_, out := f.get(t, "/versions")
	var vs struct {
		Current  uint64     `json:"current"`
		Versions []*Version `json:"versions"`
	}
	if err := json.Unmarshal([]byte(out), &vs); err != nil {
		t.Fatal(err)
	}
	if vs.Current != 3 || len(vs.Versions) != 2 || vs.Versions[0].Number != 2 || vs.Versions[1].Source != SourceAdmin || vs.Versions[1].Bytes != len(doc("v3")) {
		t.Fatalf("GET /versions: %s", out)
	}
	if resp, _ := f.get(t, "/versions/1"); resp.StatusCode != 404 {
		t.Fatalf("GET /versions/1, no longer kept: %d", resp.StatusCode)
	}
}

// TestConcurrentLoads applies configs from many clients at once: each is
// applied whole, one after another, with a version of its own.
func TestConcurrentLoads(t *testing.T) {
	f := start(t, doc("v0"), &config.Admin{History: 100})
	const n = 8
	var wg sync.WaitGroup
	versions := make([]string, n)
	for i := range n {
		wg.Go(func() {
			resp, body := f.load(t, doc(fmt.Sprint("c", i)))
			if resp.StatusCode != 200 {
				t.Errorf("load %d: %d %s", i, resp.StatusCode, body)
			}
			versions[i] = resp.Header.Get(VersionHeader)
		})
	}
	wg.Wait()
	seen := map[string]bool{}
	for _, v := range versions {
		if seen[v] {
			t.Fatalf("version %s given twice: %v", v, versions)
		}
		seen[v] = true
	}
	cur := f.configs.Current()
	if cur.Number != n+1 || len(f.configs.Versions()) != n+1 {
		t.Fatalf("current version %d, %d kept", cur.Number, len(f.configs.Versions()))
	}
	if got, want := f.served(t), strings.TrimSuffix(strings.SplitAfter(string(cur.Data()), `"body": "`)[1], `"}}}]}`); got != want {
		t.Fatalf("served %q, but the current config answers %q", got, want)
	}
}

// TestSlowAndCutClients: a client that stops sending its config is cut off
// when the read timeout passes, and one whose connection is cut midway
// applies nothing; neither holds the API up for others.
func TestSlowAndCutClients(t *testing.T) {
	defer func(d time.Duration) { readTimeout = d }(readTimeout)
	readTimeout = 500 * time.Millisecond
	f := start(t, doc("v1"), &config.Admin{})
	addr := f.admin.Addr().String()
	partial := func() net.Conn {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(c, "POST /load HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(doc("v2")), doc("v2")[:20])
		return c
	}

	slow := partial()
	defer slow.Close()
	cut := partial()
	_ = cut.Close()

	// Others are served meanwhile.
	if resp, _ := f.get(t, "/config"); resp.StatusCode != 200 {
		t.Fatalf("GET /config beside a slow client: %d", resp.StatusCode)
	}
	_ = slow.SetReadDeadline(time.Now().Add(5 * time.Second))
	began := time.Now()
	resp, err := http.ReadResponse(bufio.NewReader(slow), nil)
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Fatalf("slow client: %d", resp.StatusCode)
		}
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("the slow client was not cut off")
	}
	if waited := time.Since(began); waited > 2*time.Second {
		t.Fatalf("the slow client was cut off after %v", waited)
	}
	if f.served(t) != "v1" || f.configs.Current().Number != 1 {
		t.Fatal("a config that did not arrive whole was applied")
	}
}

// TestConnectionLimit: connections past max_conns are closed at once, and
// once some go, new ones are served again.
func TestConnectionLimit(t *testing.T) {
	f := start(t, doc("v1"), &config.Admin{MaxConns: 2})
	addr := f.admin.Addr().String()
	var held []net.Conn
	for range 2 {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, c)
	}
	// The server has accepted the two once they answer.
	for _, c := range held {
		fmt.Fprint(c, "GET /config HTTP/1.1\r\nHost: localhost\r\n\r\n")
		resp, err := http.ReadResponse(bufio.NewReader(c), nil)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	extra, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	_ = extra.SetReadDeadline(time.Now().Add(5 * time.Second))
	if n, err := extra.Read(make([]byte, 1)); err == nil || n > 0 {
		t.Fatalf("a connection past the limit was kept: %d %v", n, err)
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("a connection past the limit was left open")
	}
	extra.Close()

	for _, c := range held {
		c.Close()
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		c := &http.Client{Timeout: time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
		resp, err := c.Get(f.api + "/config")
		if err == nil {
			resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not served once the connections went: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestUnixSocket(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("Unix sockets are tested on Unix")
	}
	// A short path: a socket's path is limited to about 100 bytes.
	dir, err := os.MkdirTemp("", "fit")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "admin.sock")

	// A socket left by a process that is gone is replaced.
	stale, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	stale.Close()

	f := start(t, doc("v1"), &config.Admin{Address: config.AdminUnixPrefix + path})
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket: %v %v", fi, err)
	}
	c := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return new(net.Dialer).DialContext(ctx, "unix", path)
	}}}
	// Over a socket, the Host field is not checked.
	resp, err := c.Get("http://fit.example.com/config")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != doc("v1") {
		t.Fatalf("GET /config over the socket: %d %q", resp.StatusCode, body)
	}

	// One that is in use is not.
	if _, err := Start(&config.Admin{Address: config.AdminUnixPrefix + path}, f.configs, nil); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("a socket in use: %v", err)
	}
}

func TestWarnings(t *testing.T) {
	f := start(t, doc("v1"), &config.Admin{})
	r, err := f.configs.Apply([]byte(`{"listeners": [{"name": "web", "address": "127.0.0.1:0"}], "admin": {"address": "127.0.0.1:1"}, "access_log": {}}`), SourceAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(r.Warnings, "\n") != "access_log changed; the change takes effect when fit restarts\nadmin changed; the change takes effect when fit restarts" {
		t.Fatalf("warnings: %q", r.Warnings)
	}
}
