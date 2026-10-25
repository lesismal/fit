//go:build !windows

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	fib "github.com/lesismal/fib"
	"github.com/lesismal/fib/http3"

	"github.com/lesismal/fit/internal/certtest"
)

// proxyWasmFilter builds plugin/wasm's Proxy-Wasm test filter name, and
// returns where.
func proxyWasmFilter(t *testing.T, name string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), name+".wasm")
	cmd := exec.Command("go", "build", "-buildmode=c-shared", "-o", out, "./"+name)
	cmd.Dir = filepath.Join("..", "..", "plugin", "wasm", "testdata", "proxywasm")
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0", "GOWORK=off", "GOFLAGS=-mod=mod")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building %s: %v\n%s", name, err, b)
	}
	return out
}

// streamingBackend answers a path ending in /<bytes> with that many bytes of "abc..."
// in 16 KB writes, each flushed, and the trailer X-Upstream.
func streamingBackend(t *testing.T) *httptest.Server {
	backend := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		n, _ := strconv.Atoi(r.URL.Path[strings.LastIndexByte(r.URL.Path, '/')+1:])
		w.Header().Set("Trailer", "X-Upstream")
		w.Header().Set("Content-Type", "text/plain")
		chunk := bytes.Repeat([]byte("abcdefghijklmnop"), 1024)
		for n > 0 {
			m := min(n, len(chunk))
			if _, err := w.Write(chunk[:m]); err != nil {
				return
			}
			w.(stdhttp.Flusher).Flush()
			n -= m
		}
		w.Header().Set("X-Upstream", "up")
	}))
	t.Cleanup(backend.Close)
	return backend
}

// TestProxyWasmResponse proxies a streamed body with a trailer through a
// Proxy-Wasm filter that upper-cases it, over HTTP/1.1, h2c, h2 and
// HTTP/3, and checks the body, the header the filter added and the
// trailer it changed.
func TestProxyWasmResponse(t *testing.T) {
	filter := proxyWasmFilter(t, "response")
	backend := streamingBackend(t)
	cert, err := certtest.Write(t.TempDir(), "a.example")
	if err != nil {
		t.Fatal(err)
	}
	f, addrs := startFit(t, `{
  "listeners": [
    {"name": "web", "address": "127.0.0.1:0", "protocols": ["http/1.1", "h2c"]},
    {"name": "tls", "address": "127.0.0.1:0", "protocols": ["http/1.1", "h2", "h3"],
      "tls": {"certificates": [{"cert_file": "`+cert.CertFile+`", "key_file": "`+cert.KeyFile+`"}]}}],
  "upstreams": [{"name": "app", "addresses": ["`+backend.Listener.Addr().String()+`"]}],
  "routes": [{"match": {"path": ["/n/*"]}, "handler": {"module": "proxy", "params": {"upstream": "app"}},
    "middleware": [{"module": "wasm", "params": {"path": "`+filter+`", "config": "upper", "instances": 2}}]}]
}`)
	tlsAddr := addrs["tls"]
	https := func(h2 bool) *stdhttp.Client {
		tr := &stdhttp.Transport{
			TLSClientConfig: &tls.Config{RootCAs: certtest.Pool(cert)},
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, network, tlsAddr)
			},
			Protocols: new(stdhttp.Protocols),
		}
		if h2 {
			tr.Protocols.SetHTTP2(true)
		} else {
			tr.Protocols.SetHTTP1(true)
		}
		return &stdhttp.Client{Transport: tr, Timeout: 10 * time.Second}
	}
	engine, err := fib.New(fib.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- engine.Run() }()
	defer func() { engine.Stop(); <-done; _ = engine.Close() }()
	cfg := http3.DefaultClientConfig()
	cfg.TLSConfig = &tls.Config{RootCAs: certtest.Pool(cert), ServerName: "a.example"}
	cfg.Timeout = 10 * time.Second
	h3 := http3.NewClient(engine, cfg)
	defer h3.Close()

	type get func(url string) (*stdhttp.Response, error)
	for _, proto := range []struct {
		name, base string
		get        get
	}{
		{"HTTP/1.1", "http://" + addrs["web"], client(false).Get},
		{"h2c", "http://" + addrs["web"], client(true).Get},
		{"h2", "https://a.example", https(true).Get},
		{"HTTP/1.1 over TLS", "https://a.example", https(false).Get},
		{"HTTP/3", "https://" + tlsAddr, func(url string) (*stdhttp.Response, error) {
			req, _ := stdhttp.NewRequest("GET", url, nil)
			return h3.Go(req).Wait()
		}},
	} {
		for _, n := range []int{5, 300 << 10} {
			resp, err := proto.get(fmt.Sprintf("%s/n/%d", proto.base, n))
			if err != nil {
				t.Fatalf("%s %d: %v", proto.name, n, err)
			}
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				t.Fatalf("%s %d: %v", proto.name, n, err)
			}
			want := strings.ToUpper(strings.Repeat("abcdefghijklmnop", n/16+1)[:n])
			if resp.StatusCode != 200 || string(body) != want || resp.Header.Get("X-Response-Code") != "200" {
				t.Fatalf("%s %d: %d, %d bytes, %v", proto.name, n, resp.StatusCode, len(body), resp.Header)
			}
			if tr := resp.Trailer; tr.Get("X-Body-Bytes") != strconv.Itoa(n) || tr.Get("X-Upstream-Seen") != "up" || tr.Get("X-Upstream") != "" {
				t.Fatalf("%s %d: trailer %v", proto.name, n, tr)
			}
		}
	}
	if code, out := f.stop(t); code != 0 {
		t.Fatalf("stop: exit %d, %q", code, out)
	}
}

// TestProxyWasmResponseSlowPeers checks that neither a client that does not
// read nor a plugin slow at each part of a body holds up the requests of
// others: a client that stops reading a large response through the filter,
// and one reading through a filter that spins 20ms a part, leave a route
// without the filter, and the filter's own route, answering at once.
func TestProxyWasmResponseSlowPeers(t *testing.T) {
	filter := proxyWasmFilter(t, "response")
	backend := streamingBackend(t)
	_, addrs := startFit(t, `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0", "protocols": ["http/1.1", "h2c"]}],
  "upstreams": [{"name": "app", "addresses": ["`+backend.Listener.Addr().String()+`"]}],
  "routes": [
    {"match": {"path": ["/plain"]}, "handler": {"module": "respond", "params": {"body": "plain"}}},
    {"match": {"path": ["/n/*"]}, "handler": {"module": "proxy", "params": {"upstream": "app"}},
      "middleware": [{"module": "wasm", "params": {"path": "`+filter+`", "config": "upper", "instances": 1}}]},
    {"match": {"path": ["/slow/*"]}, "handler": {"module": "proxy", "params": {"upstream": "app"}},
      "middleware": [{"module": "wasm", "params": {"path": "`+filter+`", "config": "slow:20", "instances": 1, "timeout": "1s"}}]}
  ]
}`)
	base := "http://" + addrs["web"]
	quick := func(what, path, want string) {
		t.Helper()
		started := time.Now()
		resp, err := client(false).Get(base + path)
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(body) != want {
			t.Fatalf("%s: %d %q", what, resp.StatusCode, body)
		}
		if took := time.Since(started); took > 2*time.Second {
			t.Fatalf("%s took %v", what, took)
		}
	}

	// A client that asks for 64 MB and reads none of it.
	stalled, err := net.Dial("tcp", addrs["web"])
	if err != nil {
		t.Fatal(err)
	}
	defer stalled.Close()
	if _, err := io.WriteString(stalled, "GET /n/67108864 HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	status, err := bufio.NewReaderSize(stalled, 64).ReadString('\n')
	if err != nil || !strings.Contains(status, " 200 ") {
		t.Fatalf("the stalled response: %q %v", status, err)
	}

	// One reading through the slow filter, as others come and go.
	slow := make(chan error, 1)
	go func() {
		resp, err := client(false).Get(base + "/slow/n/" + strconv.Itoa(512<<10))
		if err == nil {
			var body []byte
			body, err = io.ReadAll(resp.Body)
			resp.Body.Close()
			if err == nil && len(body) != 512<<10 {
				err = fmt.Errorf("%d bytes", len(body))
			}
		}
		slow <- err
	}()
	for i := range 20 {
		quick(fmt.Sprintf("plain %d", i), "/plain", "plain")
		quick(fmt.Sprintf("through the filter %d", i), "/n/5", "ABCDE")
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case err := <-slow:
		if err != nil {
			t.Fatalf("through the slow filter: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the slow filter's response did not end")
	}
}
