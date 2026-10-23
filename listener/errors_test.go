package listener

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	stdhttp "net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/certtest"
	"github.com/lesismal/fit/internal/fibtest"
	"github.com/lesismal/fit/observe"
	"github.com/lesismal/fit/runtime"
	"github.com/lesismal/fit/tlsmgr"
)

// unroutable is an address of TEST-NET-3, which no interface of a test
// machine has, so that binding it fails even though its port is 0.
const unroutable = "203.0.113.1:0"

// l4Service is the upstreams and services of a config whose listeners
// names serve tcp or udp: one service for them all, to a port nothing
// listens on.
func l4Service(names ...string) string {
	return `"upstreams": [{"name": "up", "addresses": ["127.0.0.1:1"]}],
  "services": [{"name": "svc", "listeners": ["` + strings.Join(names, `", "`) + `"], "upstream": "up"}]`
}

// tlsBlock is a listener's tls with cert.
func tlsBlock(cert *certtest.Cert) string {
	return fmt.Sprintf(`"tls": {"certificates": [{"cert_file": %q, "key_file": %q}]}`, cert.CertFile, cert.KeyFile)
}

// TestBindErrors starts listeners of every kind that cannot bind: on an
// address no interface has, and on a port another socket holds.
func TestBindErrors(t *testing.T) {
	cert, err := certtest.Write(t.TempDir(), "a.example", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	tcpTaken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcpTaken.Close()
	udpTaken, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udpTaken.Close()
	for _, tt := range []struct {
		name, address, protocols, extra string
	}{
		{"tcp unroutable", unroutable, `["tcp"]`, ""},
		{"tcp taken", tcpTaken.Addr().String(), `["tcp"]`, ""},
		{"udp unroutable", unroutable, `["udp"]`, ""},
		{"udp taken", udpTaken.LocalAddr().String(), `["udp"]`, ""},
		{"http unroutable", unroutable, `["http/1.1"]`, ""},
		{"h3 unroutable", unroutable, `["h3"]`, ", " + tlsBlock(cert)},
		{"h3 taken", udpTaken.LocalAddr().String(), `["h3"]`, ", " + tlsBlock(cert)},
		{"https and h3 taken", udpTaken.LocalAddr().String(), `["http/1.1", "h2", "h3"]`, ", " + tlsBlock(cert)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rt := new(runtime.Runtime)
			defer rt.Close()
			doc := fmt.Sprintf(`{"listeners": [{"name": "x", "address": %q, "protocols": %s%s}]`, tt.address, tt.protocols, tt.extra)
			if strings.Contains(tt.protocols, "tcp") || strings.Contains(tt.protocols, "udp") {
				doc += `, ` + l4Service("x")
			}
			swap(t, rt, doc+`}`)
			s, err := Start(rt)
			if err == nil {
				_ = s.Close()
				t.Fatal("Start succeeded")
			}
			if !strings.Contains(err.Error(), `listener "x"`) {
				t.Fatalf("error %v", err)
			}
		})
	}
}

// TestSniffVariants starts listeners that tell protocols apart on one port:
// a TLS connection to one with tcp and tls whose service does not take its
// name is served HTTPS by the listener itself, as it is once the listener
// has no tcp, and what is neither TLS nor HTTP is closed.
func TestSniffVariants(t *testing.T) {
	cert, err := certtest.Write(t.TempDir(), "a.example", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	rt, s := start(t, `{"listeners": [
    {"name": "tls", "address": "127.0.0.1:0", "protocols": ["http/1.1", "h2", "tcp"], `+tlsBlock(cert)+`},
    {"name": "socks", "address": "127.0.0.1:0", "protocols": ["http/1.1", "h2", "socks"], "socks": {"clients": ["127.0.0.1"]}, `+tlsBlock(cert)+`},
    {"name": "h1", "address": "127.0.0.1:0", "protocols": ["http/1.1", "tcp"]},
    {"name": "h2c", "address": "127.0.0.1:0", "protocols": ["h2c", "tcp"]}],
  "upstreams": [{"name": "up", "addresses": ["127.0.0.1:1"]}],
  "services": [{"name": "named", "listeners": ["tls"], "upstream": "up", "server_names": ["b.example"]},
    {"name": "svc", "listeners": ["h1", "h2c"], "upstream": "up"}],
  "routes": [{"handler": {"module": "respond", "params": {"body": "ok"}}}]}`)
	addr := addrOf(s, "tls")
	if _, body := get(t, tlsClient(addr, false, cert), "https://a.example/", ""); body != "ok" {
		t.Fatalf("HTTPS on a tcp listener with tls: %q", body)
	}
	if _, body := get(t, tlsClient(addrOf(s, "socks"), true, cert), "https://a.example/", ""); body != "ok" {
		t.Fatalf("HTTPS on a socks listener with tls: %q", body)
	}
	if _, body := get(t, client(false), "http://"+addrOf(s, "h1")+"/", ""); body != "ok" {
		t.Fatalf("HTTP/1.1 beside tcp: %q", body)
	}
	if _, body := get(t, client(true), "http://"+addrOf(s, "h2c")+"/", ""); body != "ok" {
		t.Fatalf("h2c beside tcp: %q", body)
	}
	// Neither TLS nor HTTP, with no service to take it: closed.
	expectClosed(t, addr, "\x00\x01\x02 not a protocol\r\n\r\n")

	// The listener's tcp is gone from the graph, its binding not.
	swap(t, rt, `{"listeners": [{"name": "tls", "address": "127.0.0.1:0", "protocols": ["http/1.1", "h2"], `+tlsBlock(cert)+`}],
  "routes": [{"handler": {"module": "respond", "params": {"body": "ok2"}}}]}`)
	if _, body := get(t, tlsClient(addr, false, cert), "https://a.example/", ""); body != "ok2" {
		t.Fatalf("HTTPS once the listener has no tcp: %q", body)
	}
	expectClosed(t, addr, "\x00\x01\x02 not a protocol\r\n\r\n")
}

// expectClosed connects to addr, writes hello, and waits for the server to
// close the connection without answering.
func expectClosed(t *testing.T, addr, hello string) {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if hello != "" {
		_, _ = c.Write([]byte(hello))
	}
	_ = c.SetReadDeadline(time.Now().Add(15 * time.Second))
	n, err := c.Read(make([]byte, 1))
	if n != 0 || err == nil || errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
		t.Fatalf("read %d bytes, %v; want the connection closed", n, err)
	}
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// TestNoGraph serves connections and requests while the runtime has no
// graph, or one without the listener: tcp connections are closed, and HTTP
// requests answered 503 or 404.
func TestNoGraph(t *testing.T) {
	rt, s := start(t, `{"listeners": [
    {"name": "tcp", "address": "127.0.0.1:0", "protocols": ["tcp"]},
    {"name": "web", "address": "127.0.0.1:0"}], `+l4Service("tcp")+`}`)
	tcp, web := addrOf(s, "tcp"), "http://"+addrOf(s, "web")+"/"

	swap(t, rt, `{"listeners": [{"name": "other", "address": "127.0.0.1:0"}]}`)
	expectClosed(t, tcp, "")
	if resp, _ := get(t, client(false), web, ""); resp.StatusCode != stdhttp.StatusNotFound {
		t.Fatalf("without the listener: %d", resp.StatusCode)
	}

	rt.Close()
	expectClosed(t, tcp, "")
	if resp, _ := get(t, client(false), web, ""); resp.StatusCode != stdhttp.StatusServiceUnavailable {
		t.Fatalf("without a graph: %d", resp.StatusCode)
	}
}

// TestReloadSharedAddressFails reloads a listener on a fixed port into one
// that also needs the port's UDP side, which another socket holds: the
// binding beside the old listener fails, the old one is stopped to bind
// alone, and that fails too, so the reload fails without the listener,
// keeping the others.
func TestReloadSharedAddressFails(t *testing.T) {
	cert, err := certtest.Write(t.TempDir(), "a.example", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	addr := udp.LocalAddr().String()
	doc := func(protocols, other string) string {
		return fmt.Sprintf(`{"listeners": [{"name": "a", "address": %q, "protocols": %s, %s}%s],
  "routes": [{"handler": {"module": "respond", "params": {"body": "ok"}}}]}`, addr, protocols, tlsBlock(cert), other)
	}
	// b, on another address, is removed by the reload too.
	rt, s := start(t, doc(`["http/1.1", "h2"]`, `, {"name": "b", "address": "127.0.0.1:0"}`))
	if _, _, err := s.Reload(compile(t, doc(`["http/1.1", "h2", "h3"]`, ""))); err == nil {
		t.Fatal("the reload succeeded")
	}
	if got := names(s.Addrs()); len(got) != 1 || got[0] != "b" {
		t.Fatalf("listeners after the failed reload: %v", got)
	}
	g := rt.Current()
	defer g.Release()
	if g.Version != 1 {
		t.Fatalf("version %d", g.Version)
	}
}

// TestReloadWithoutDrain removes a listener with no drain timeout: it stops
// at once.
func TestReloadWithoutDrain(t *testing.T) {
	_, s := start(t, reloadDoc("v1", "a 127.0.0.1:0 http/1.1", "gone 127.0.0.1:0 http/1.1"))
	s.SetDrainTimeout(0)
	gone := addrOf(s, "gone")
	if _, removed, err := s.Reload(compile(t, reloadDoc("v2", "a 127.0.0.1:0 http/1.1"))); err != nil || len(removed) != 1 {
		t.Fatalf("removed %v, %v", names(removed), err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", gone, time.Second)
		if err != nil {
			break
		}
		conn.Close()
		if time.Now().After(deadline) {
			t.Fatalf("%s still takes connections", gone)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestRedirectEdges redirects requests with no host, with an IPv6 host and
// port, and with a target in absolute form.
func TestRedirectEdges(t *testing.T) {
	httpPort, httpsPort := freePort(t), freePort(t)
	rt := new(runtime.Runtime)
	defer rt.Close()
	swap(t, rt, fmt.Sprintf(`{"auto_https": {"http_port": %d, "https_port": %d},
  "routes": [{"match": {"host": ["a.example"]}, "handler": {"module": "respond", "params": {"body": "ok"}}}]}`, httpPort, httpsPort))
	d := &dispatcher{rt: rt, name: "http"}
	for _, tt := range []struct {
		host, target string
		status       int
		location     string
	}{
		{"", "/x", stdhttp.StatusBadRequest, ""},
		{"[::1]:" + strconv.Itoa(httpPort), "/x", stdhttp.StatusPermanentRedirect, fmt.Sprintf("https://[::1]:%d/x", httpsPort)},
		{"a.example", "http://a.example/y?z=1", stdhttp.StatusPermanentRedirect, fmt.Sprintf("https://a.example:%d/y?z=1", httpsPort)},
	} {
		u, _ := url.ParseRequestURI(tt.target)
		r := &stdhttp.Request{Method: "GET", URL: u, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
			Header: stdhttp.Header{}, Host: tt.host, RemoteAddr: "192.0.2.1:4321", RequestURI: tt.target}
		rec := fibtest.ServeRequest(d, r)
		if rec.Response.StatusCode != tt.status || rec.Response.Header.Get("Location") != tt.location {
			t.Errorf("%q %s: %d %q, want %d %q", tt.host, tt.target, rec.Response.StatusCode,
				rec.Response.Header.Get("Location"), tt.status, tt.location)
		}
	}
}

// sharedStorage is an ACME storage shared between hosts, which has the
// answer of one HTTP-01 challenge, token "good".
type sharedStorage struct{ *tlsmgr.FileStorage }

func (sharedStorage) Shared() bool { return true }

func (s sharedStorage) Load(ctx context.Context, key string) ([]byte, error) {
	if strings.HasSuffix(key, "/http-01/good") {
		return []byte("good.answer"), nil
	}
	if strings.Contains(key, "/http-01/") {
		return nil, fs.ErrNotExist
	}
	return s.FileStorage.Load(ctx, key)
}

// TestACMESharedChallenge answers HTTP-01 challenges another instance has
// under way from the shared storage, and 404s for those it has not.
func TestACMESharedChallenge(t *testing.T) {
	dir := t.TempDir()
	acme, err := tlsmgr.NewACME(&config.ACME{Storage: dir}, sharedStorage{&tlsmgr.FileStorage{Root: dir}}, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	defer acme.Close()
	c, err := config.Load([]byte(`{"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "routes": [{"handler": {"module": "respond", "params": {"body": "route"}}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	g, err := runtime.Compile(c, runtime.Env{ACME: acme})
	if err != nil {
		t.Fatal(err)
	}
	rt := new(runtime.Runtime)
	rt.Swap(g)
	defer rt.Close()
	d := &dispatcher{rt: rt, name: "web"}
	for _, tt := range []struct {
		path   string
		status int
		body   string
	}{
		{tlsmgr.ACMEChallengePrefix + "good", 200, "good.answer"},
		{tlsmgr.ACMEChallengePrefix + "missing", 404, "404 page not found\n"},
		{"/other", 200, "route"},
	} {
		rec := fibtest.Serve(d, "GET", tt.path)
		if rec.Response.StatusCode != tt.status || string(rec.Response.Body) != tt.body {
			t.Errorf("%s: %d %q", tt.path, rec.Response.StatusCode, rec.Response.Body)
		}
	}
}

// TestDispatchLogMetricsTrace serves requests with the access log, the
// metrics and the tracer together, and one whose body length is unknown.
func TestDispatchLogMetricsTrace(t *testing.T) {
	var mu sync.Mutex
	var exports int
	rcv := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		exports++
		mu.Unlock()
	}))
	defer rcv.Close()
	for _, traced := range []bool{false, true} {
		t.Run(map[bool]string{false: "log+metrics", true: "log+metrics+trace"}[traced], func(t *testing.T) {
			c, err := config.Load([]byte(strings.Replace(dispatchDoc, "{", `{"admin": {}, "metrics": {},`, 1)))
			if err != nil {
				t.Fatal(err)
			}
			var buf syncBuffer
			env := runtime.Env{AccessLog: observe.NewAccessLogTo(&buf, 10*time.Millisecond, 1<<20), Metrics: observe.NewMetrics()}
			defer env.AccessLog.Close()
			if traced {
				if env.Tracer, err = observe.NewTracer(&config.Telemetry{Endpoint: rcv.URL, Traces: &config.TracesExport{SampleRatio: 1}}, t.Logf); err != nil {
					t.Fatal(err)
				}
				defer env.Tracer.Close()
			}
			g, err := runtime.Compile(c, env)
			if err != nil {
				t.Fatal(err)
			}
			rt := new(runtime.Runtime)
			rt.Swap(g)
			defer rt.Close()
			d := &dispatcher{rt: rt, name: "web"}
			for _, length := range []int64{0, -1} {
				r := &stdhttp.Request{Method: "GET", URL: &url.URL{Path: "/hello"}, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
					Header: stdhttp.Header{}, Host: "example.com", RemoteAddr: "192.0.2.1:4321", RequestURI: "/hello", ContentLength: length}
				if rec := fibtest.ServeRequest(d, r); rec.Response.StatusCode != 200 {
					t.Fatalf("status %d", rec.Response.StatusCode)
				}
			}
			env.AccessLog.Close()
			if n := strings.Count(buf.String(), "\n"); n != 2 {
				t.Errorf("%d log lines: %q", n, buf.String())
			}
			found := false
			for _, m := range env.Metrics.AllRequests() {
				if m != nil {
					found = true
				}
			}
			if !found {
				t.Error("no request metrics")
			}
		})
	}
}

// syncBuffer is a strings.Builder the access log may write to while the
// test reads it.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
