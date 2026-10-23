package proxy

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/listener"
	"github.com/lesismal/fit/observe"
	"github.com/lesismal/fit/runtime"
	"github.com/lesismal/fit/upstream"
)

// echo is what the echo backend saw of a request.
type echo struct {
	Method     string
	RequestURI string
	Host       string
	Header     stdhttp.Header
	Body       string
	Proto      string
}

func echoBackend(t *testing.T, proto string) string {
	t.Helper()
	return serveBackend(t, proto, stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Backend", "echo")
		w.WriteHeader(stdhttp.StatusCreated)
		_ = json.NewEncoder(w).Encode(echo{r.Method, r.RequestURI, r.Host, r.Header, string(body), r.Proto})
	}))
}

// startFit serves doc, with %s standing for the upstream's address list, on
// a listener of its own, with upstream connections dialed through an engine
// of their own, as fit run does. It returns the listener's base URL.
func startFit(t testing.TB, doc string, addrs ...string) string {
	t.Helper()
	return startFitLog(t, doc, nil, addrs...)
}

// startFitLog is startFit with an access log.
func startFitLog(t testing.TB, doc string, log *observe.AccessLog, addrs ...string) string {
	t.Helper()
	quoted := make([]string, len(addrs))
	for i, a := range addrs {
		quoted[i] = `"` + a + `"`
	}
	if len(addrs) > 0 {
		doc = withBackendFields(doc, addrs[0])
	}
	c, err := config.Load([]byte(strings.Replace(doc, "%s", strings.Join(quoted, ", "), 1)))
	if err != nil {
		t.Fatal(err)
	}
	engine, stop, err := upstream.StartEngine()
	if err != nil {
		t.Fatal(err)
	}
	g, err := runtime.Compile(c, runtime.Env{Engine: engine, AccessLog: log})
	if err != nil {
		stop()
		t.Fatal(err)
	}
	rt := new(runtime.Runtime)
	rt.Swap(g)
	s, err := listener.Start(rt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = s.Close()
		rt.Close()
		stop()
	})
	return "http://" + s.Addrs()[0].Addr.String()
}

// config1 is a config with one listener, one upstream and a proxy route
// with proxyParams. Its body limit is raised past the streaming tests'
// uploads, as a deployment that takes large uploads raises it.
func config1(proxyParams string) string {
	return `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0", "protocols": ["http/1.1", "h2c"]}],
  "limits": {"max_body_bytes": 4294967296},
  "upstreams": [{"name": "app", "addresses": [%s]}],
  "routes": [{"handler": {"module": "proxy", "params": ` + proxyParams + `}}]
}`
}

func client(h2c bool) *stdhttp.Client {
	tr := &stdhttp.Transport{Protocols: new(stdhttp.Protocols)}
	if h2c {
		tr.Protocols.SetUnencryptedHTTP2(true)
	} else {
		tr.Protocols.SetHTTP1(true)
	}
	return &stdhttp.Client{Transport: tr, Timeout: 5 * time.Second}
}

func send(t *testing.T, c *stdhttp.Client, req *stdhttp.Request) (*stdhttp.Response, echo) {
	t.Helper()
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var e echo
	data, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(data, &e); err != nil {
		t.Fatalf("%d %q: %v", resp.StatusCode, data, err)
	}
	return resp, e
}

func TestForwarding(t *testing.T) {
	eachProto(t, func(t *testing.T, proto string) {
		base := startFit(t, config1(`{"upstream": "app"}`), echoBackend(t, proto))
		for _, h2c := range []bool{false, true} {
			c := client(h2c)
			body := strings.Repeat("payload ", 1000)
			req, _ := stdhttp.NewRequest("PUT", base+"/a%2Fb/c?x=1&y=%20z", strings.NewReader(body))
			req.Host = "app.example.com"
			req.Header.Set("X-Custom", "kept")
			req.Header.Add("X-Multi", "1")
			req.Header.Add("X-Multi", "2")
			resp, e := send(t, c, req)
			if resp.StatusCode != 201 || resp.Header.Get("X-Backend") != "echo" || resp.Header.Get("Content-Type") != "application/json" {
				t.Fatalf("h2c=%v: response %d %v", h2c, resp.StatusCode, resp.Header)
			}
			if e.Method != "PUT" || e.RequestURI != "/a%2Fb/c?x=1&y=%20z" || e.Host != "app.example.com" || e.Body != body ||
				e.Proto != protoOf(proto) || e.Header.Get("X-Custom") != "kept" || strings.Join(e.Header["X-Multi"], ",") != "1,2" {
				t.Fatalf("h2c=%v: upstream saw %+v", h2c, e)
			}
			if ua := e.Header.Get("User-Agent"); ua != "Go-http-client/1.1" && ua != "Go-http-client/2.0" {
				t.Errorf("h2c=%v: User-Agent %q, want the client's", h2c, ua)
			}
		}
		// No body, and no User-Agent: none is made up.
		req, _ := stdhttp.NewRequest("GET", base+"/", nil)
		req.Header.Set("User-Agent", "")
		if _, e := send(t, client(false), req); e.Body != "" || e.Header.Get("Content-Length") != "" || e.Header["User-Agent"] != nil {
			t.Fatalf("upstream saw %+v", e)
		}
	})
}

func TestHopByHopRequest(t *testing.T) {
	eachProto(t, func(t *testing.T, proto string) {
		base := startFit(t, config1(`{"upstream": "app"}`), echoBackend(t, proto))
		req, _ := stdhttp.NewRequest("GET", base+"/", nil)
		req.Header.Set("Connection", "X-Hop, keep-alive")
		req.Header.Set("X-Hop", "1")
		req.Header.Set("Keep-Alive", "timeout=5")
		req.Header.Set("Proxy-Connection", "keep-alive")
		req.Header.Set("Proxy-Authorization", "Basic eDp5")
		req.Header.Set("Te", "trailers")
		req.Header.Set("Upgrade", "foo")
		req.Header.Set("X-End", "kept")
		_, e := send(t, client(false), req)
		for _, name := range []string{"X-Hop", "Keep-Alive", "Proxy-Connection", "Proxy-Authorization", "Te", "Upgrade"} {
			if v, ok := e.Header[name]; ok {
				t.Errorf("%s reached the upstream: %q", name, v)
			}
		}
		if e.Header.Get("X-End") != "kept" {
			t.Errorf("X-End lost: %v", e.Header)
		}
	})
}

func TestHopByHopResponse(t *testing.T) {
	// A raw backend, so that its response carries exactly the fields written.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				br := bufio.NewReader(conn)
				for {
					req, err := stdhttp.ReadRequest(br)
					if err != nil {
						return
					}
					_, _ = io.Copy(io.Discard, req.Body)
					_, _ = io.WriteString(conn, "HTTP/1.1 200 OK\r\nConnection: X-Hop\r\nX-Hop: 1\r\nKeep-Alive: timeout=5\r\n"+
						"Proxy-Authenticate: Basic\r\nTrailer: X-T\r\nUpgrade: foo\r\nX-End: kept\r\nContent-Length: 2\r\n\r\nok")
				}
			}()
		}
	}()
	base := startFit(t, config1(`{"upstream": "app"}`), ln.Addr().String())
	for _, h2c := range []bool{false, true} {
		resp, err := client(h2c).Get(base + "/")
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || string(body) != "ok" || resp.Header.Get("X-End") != "kept" {
			t.Fatalf("h2c=%v: %d %q %v", h2c, resp.StatusCode, body, resp.Header)
		}
		for _, name := range []string{"X-Hop", "Keep-Alive", "Proxy-Authenticate", "Trailer", "Upgrade"} {
			if v, ok := resp.Header[name]; ok {
				t.Errorf("h2c=%v: %s reached the client: %q", h2c, name, v)
			}
		}
	}
}

func TestForwardedUntrusted(t *testing.T) {
	eachProto(t, func(t *testing.T, proto string) {
		base := startFit(t, config1(`{"upstream": "app"}`), echoBackend(t, proto))
		req, _ := stdhttp.NewRequest("GET", base+"/", nil)
		req.Host = "site.example"
		req.Header.Set("X-Forwarded-For", "203.0.113.9")
		req.Header.Set("X-Forwarded-Proto", "https")
		req.Header.Set("X-Forwarded-Host", "evil.example")
		req.Header.Set("Forwarded", "for=203.0.113.9")
		_, e := send(t, client(false), req)
		want := map[string]string{
			"X-Forwarded-For":   "127.0.0.1",
			"X-Forwarded-Proto": "http",
			"X-Forwarded-Host":  "site.example",
			"Forwarded":         "for=127.0.0.1;host=site.example;proto=http",
		}
		for name, v := range want {
			if got := strings.Join(e.Header[name], " | "); got != v {
				t.Errorf("%s = %q, want %q", name, got, v)
			}
		}
	})
}

func TestForwardedTrusted(t *testing.T) {
	eachProto(t, func(t *testing.T, proto string) {
		base := startFit(t, config1(`{"upstream": "app", "trusted_proxies": ["10.0.0.0/8", "127.0.0.1"]}`), echoBackend(t, proto))
		req, _ := stdhttp.NewRequest("GET", base+"/", nil)
		req.Host = "site.example"
		req.Header.Set("X-Forwarded-For", "203.0.113.9")
		req.Header.Set("X-Forwarded-Proto", "https")
		req.Header.Set("X-Forwarded-Host", "public.example")
		req.Header.Set("Forwarded", `for=203.0.113.9;proto=https`)
		_, e := send(t, client(false), req)
		want := map[string]string{
			"X-Forwarded-For":   "203.0.113.9, 127.0.0.1",
			"X-Forwarded-Proto": "https",
			"X-Forwarded-Host":  "public.example",
			"Forwarded":         "for=203.0.113.9;proto=https, for=127.0.0.1;host=site.example;proto=http",
		}
		for name, v := range want {
			if got := strings.Join(e.Header[name], " | "); got != v {
				t.Errorf("%s = %q, want %q", name, got, v)
			}
		}
		// A trusted proxy that sends none of them gets them set.
		req, _ = stdhttp.NewRequest("GET", base+"/", nil)
		req.Host = "site.example"
		_, e = send(t, client(false), req)
		if e.Header.Get("X-Forwarded-For") != "127.0.0.1" || e.Header.Get("X-Forwarded-Proto") != "http" ||
			e.Header.Get("X-Forwarded-Host") != "site.example" {
			t.Errorf("upstream saw %v", e.Header)
		}
	})
}

func TestHostHeader(t *testing.T) {
	eachProto(t, func(t *testing.T, proto string) {
		base := startFit(t, config1(`{"upstream": "app", "host_header": "internal.svc:8080"}`), echoBackend(t, proto))
		req, _ := stdhttp.NewRequest("GET", base+"/", nil)
		req.Host = "site.example"
		if _, e := send(t, client(false), req); e.Host != "internal.svc:8080" || e.Header.Get("X-Forwarded-Host") != "site.example" {
			t.Fatalf("upstream saw host %q, %v", e.Host, e.Header)
		}
	})
}

func TestBadGateway(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	base := startFit(t, config1(`{"upstream": "app"}`), addr)
	for _, h2c := range []bool{false, true} {
		resp, err := client(h2c).Get(base + "/")
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 502 || string(body) != "502 Bad Gateway\n" {
			t.Fatalf("h2c=%v: %d %q", h2c, resp.StatusCode, body)
		}
	}
}

func TestManyRequests(t *testing.T) {
	eachProto(t, func(t *testing.T, proto string) {
		base := startFit(t, config1(`{"upstream": "app"}`), echoBackend(t, proto))
		errs := make(chan error, 64)
		for i := range 64 {
			go func() {
				req, _ := stdhttp.NewRequest("POST", fmt.Sprintf("%s/n/%d", base, i), strings.NewReader(fmt.Sprint(i)))
				resp, err := client(i%2 == 1).Do(req)
				if err != nil {
					errs <- err
					return
				}
				defer resp.Body.Close()
				var e echo
				if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
					errs <- err
					return
				}
				if e.RequestURI != fmt.Sprintf("/n/%d", i) || e.Body != fmt.Sprint(i) {
					errs <- fmt.Errorf("request %d: upstream saw %+v", i, e)
					return
				}
				errs <- nil
			}()
		}
		for range 64 {
			if err := <-errs; err != nil {
				t.Error(err)
			}
		}
	})
}

func TestForwardedElement(t *testing.T) {
	tests := []struct {
		ip    string
		host  string
		proto string
		want  string
	}{
		{"192.0.2.1", "a.example", "http", "for=192.0.2.1;host=a.example;proto=http"},
		{"2001:db8::1", "a.example:8443", "https", `for="[2001:db8::1]";host="a.example:8443";proto=https`},
		{"", "", "http", "for=unknown;proto=http"},
	}
	for _, tt := range tests {
		var ip netip.Addr
		if tt.ip != "" {
			ip = netip.MustParseAddr(tt.ip)
		}
		if got := string(appendForwardedElement(nil, ip, ipString(ip), tt.host, tt.proto)); got != tt.want {
			t.Errorf("%s %s: %s, want %s", tt.ip, tt.host, got, tt.want)
		}
	}
	for remote, want := range map[string]string{
		"192.0.2.1:80":          "192.0.2.1",
		"[::ffff:192.0.2.1]:80": "192.0.2.1",
		"[2001:DB8::1]:80":      "2001:db8::1",
		"[fe80::1%eth0]:80":     "fe80::1",
		"10.0.0.7":              "10.0.0.7",
		"not an address:80":     "",
	} {
		if ip, s := clientAddr(remote); s != want || s != "" && ip.String() != want {
			t.Errorf("%s: %v %q, want %q", remote, ip, s, want)
		}
	}
	if n := testing.AllocsPerRun(100, func() { clientAddr("192.0.2.1:80") }); n != 0 {
		t.Errorf("an IPv4 address takes %v allocations", n)
	}
}

func TestConfigErrors(t *testing.T) {
	tests := []struct {
		params string
		want   string
	}{
		{`{"upstream": "nope"}`, `routes[0].handler.params.upstream: there is no upstream named "nope"`},
		{`{"upstream": "app", "trusted_proxies": ["10.0.0.0/8", "nope"]}`, `routes[0].handler.params.trusted_proxies[1]: "nope" is not an IP address or a network, as in "10.0.0.1" or "10.0.0.0/8"`},
		{`{"upstream": "app", "host_header": "a b"}`, `routes[0].handler.params.host_header: "a b" is not a host`},
		{`{}`, `routes[0].handler.params.upstream: upstream or upstreams is needed`},
	}
	for _, tt := range tests {
		c, err := config.Load([]byte(strings.Replace(config1(tt.params), "%s", `"127.0.0.1:1"`, 1)))
		if err != nil {
			t.Fatal(err)
		}
		_, err = runtime.Compile(c, runtime.Env{})
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: %v, want %s", tt.params, err, tt.want)
		}
	}
}

func TestHead(t *testing.T) {
	eachProto(t, func(t *testing.T, proto string) {
		addr := serveBackend(t, proto, stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			w.Header().Set("Content-Length", "11")
			if r.Method != "HEAD" {
				_, _ = io.WriteString(w, "hello world")
			}
		}))
		base := startFit(t, config1(`{"upstream": "app"}`), addr)
		for _, h2c := range []bool{false, true} {
			req, _ := stdhttp.NewRequest("HEAD", base+"/", nil)
			resp, err := client(h2c).Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != 200 || resp.ContentLength != 11 {
				t.Errorf("h2c=%v: HEAD gave %d with Content-Length %d, want the upstream's 11", h2c, resp.StatusCode, resp.ContentLength)
			}
		}
	})
}

// lockedBuffer collects access log lines.
type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func TestAccessLog(t *testing.T) {
	var out lockedBuffer
	log := observe.NewAccessLogTo(&out, 10*time.Millisecond, 0)
	backend := echoBackend(t, "http/1.1")
	base := startFitLog(t, `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0", "protocols": ["http/1.1", "h2c"]}],
  "upstreams": [{"name": "app", "addresses": [%s]}],
  "routes": [{"name": "api", "match": {"path": ["/api/*"]}, "handler": {"module": "proxy", "params": {"upstream": "app"}}}]
}`, log, backend)
	for _, h2c := range []bool{false, true} {
		req, _ := stdhttp.NewRequest("POST", base+"/api/x?q=1", strings.NewReader("hello"))
		req.Header.Set("User-Agent", "logtest")
		resp, err := client(h2c).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		resp, err = client(h2c).Get(base + "/nowhere")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	// A line is logged as its response finishes on the server, which may be
	// after the client has the response, and one logged after Close is
	// dropped: the log is closed once every line is out.
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		out.mu.Lock()
		n := strings.Count(out.b.String(), "\n")
		out.mu.Unlock()
		if n >= 4 {
			break
		}
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	out.mu.Lock()
	lines := strings.Split(strings.TrimSuffix(out.b.String(), "\n"), "\n")
	out.mu.Unlock()
	if len(lines) != 4 {
		t.Fatalf("%d lines: %q", len(lines), lines)
	}
	// The lines' order is not the requests'.
	protos := map[string]int{}
	for i, line := range lines {
		var got map[string]any
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		protos[fmt.Sprint(got["proto"], " ", got["uri"])]++
		if got["listener"] != "web" || got["client"] != "127.0.0.1" {
			t.Errorf("line %d: %v", i, got)
		}
		if got["uri"] == "/api/x?q=1" {
			if got["route"] != "api" || got["method"] != "POST" || got["status"] != 201.0 ||
				got["bytes_in"] != 5.0 || got["bytes_out"].(float64) < 10 || got["upstream"] != backend ||
				got["upstream_duration"].(float64) <= 0 || got["user_agent"] != "logtest" ||
				got["duration"].(float64) < got["upstream_duration"].(float64) {
				t.Errorf("proxied line %d: %v", i, got)
			}
		} else if got["status"] != 404.0 || got["uri"] != "/nowhere" || got["route"] != nil || got["upstream"] != nil {
			t.Errorf("404 line %d: %v", i, got)
		}
	}
	for _, k := range []string{"HTTP/1.1 /api/x?q=1", "HTTP/1.1 /nowhere", "HTTP/2.0 /api/x?q=1", "HTTP/2.0 /nowhere"} {
		if protos[k] != 1 {
			t.Errorf("lines by protocol and URI: %v", protos)
			break
		}
	}
}

func TestPassiveHealth(t *testing.T) {
	eachProto(t, func(t *testing.T, proto string) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		refused := ln.Addr().String()
		ln.Close()
		base := startFit(t, config1(`{"upstream": "app"}`), echoBackend(t, proto), refused)
		codes := map[int]int{}
		for range 20 {
			resp, err := client(false).Get(base + "/")
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			codes[resp.StatusCode]++
		}
		// The first request to the refused address fails, and takes it out.
		if codes[502] != 1 || codes[201] != 19 {
			t.Fatalf("statuses: %v", codes)
		}
	})
}

func ipString(ip netip.Addr) string {
	if !ip.IsValid() {
		return ""
	}
	return ip.String()
}
