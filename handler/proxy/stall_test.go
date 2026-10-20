package proxy

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	goruntime "runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	fib "github.com/lesismal/fib"
	fibhttp "github.com/lesismal/fib/http"
	"github.com/lesismal/fib/http3"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/certtest"
	"github.com/lesismal/fit/listener"
	"github.com/lesismal/fit/runtime"
	"github.com/lesismal/fit/upstream"
)

// The review of M1 found that a client that stops reading a proxied
// download held the worker writing to it, and with it the upstream
// connection, for as long as the client stayed: other requests sharing the
// connection, or waiting for one under max_conns, timed out. These tests
// reproduce what it measured, with a write stall timeout of 1s.

const (
	stallTimeout = time.Second
	stuckSize    = 200 << 20 // far past what socket buffers and windows hold
)

// stallBackend streams stuckSize bytes for /big, for as long as its client
// takes them, and answers "ok" for anything else.
func stallBackend(t *testing.T, proto string) string {
	t.Helper()
	return serveBackend(t, proto, stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		if r.URL.Path != "/big" {
			_, _ = io.WriteString(w, "ok")
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(stuckSize))
		chunk := make([]byte, 64<<10)
		for sent := 0; sent < stuckSize && r.Context().Err() == nil; sent += len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
}

// stallFit serves a proxy to the backend at addr, with the given upstream
// fields, on an HTTP/1.1 and h2c listener and an HTTP/3 one, and returns
// their addresses and the certificate the HTTP/3 one serves.
func stallFit(t *testing.T, addr, fields string) (tcpAddr, h3Addr string, cert *certtest.Cert) {
	t.Helper()
	return stallFitWith(t, addr, fields, stallTimeout)
}

// stallFitWith is stallFit with a write stall timeout of stall.
func stallFitWith(t *testing.T, addr, fields string, stall time.Duration) (tcpAddr, h3Addr string, cert *certtest.Cert) {
	t.Helper()
	cert, err := certtest.Write(t.TempDir(), "fit.example", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	doc := fmt.Sprintf(`{
  "listeners": [
    {"name": "web", "address": "127.0.0.1:0", "protocols": ["http/1.1", "h2c"]},
    {"name": "quic", "address": "127.0.0.1:0", "protocols": ["h3"], "tls": {"certificates": [{"cert_file": %q, "key_file": %q}]}}
  ],
  "limits": {"write_stall_timeout": %q},
  "upstreams": [{"name": "app", "addresses": [%%s]%s}],
  "routes": [{"handler": {"module": "proxy", "params": {"upstream": "app"}}}]
}`, cert.CertFile, cert.KeyFile, stall.String(), fields)
	doc = withBackendFields(doc, addr)
	c, err := config.Load([]byte(strings.Replace(doc, "%s", `"`+addr+`"`, 1)))
	if err != nil {
		t.Fatal(err)
	}
	engine, stop, err := upstream.StartEngine()
	if err != nil {
		t.Fatal(err)
	}
	g, err := runtime.Compile(c, runtime.Env{Engine: engine})
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
	return s.Addrs()[0].Addr.String(), s.Addrs()[1].Addr.String(), cert
}

// stuckClient asks for /big and reads none of it until done is called,
// which then reads what is left and reports how much arrived and whether the
// response ended short of its length, as it should once fit gave it up.
type stuckClient interface {
	done(t *testing.T) (n int64, cut bool)
}

// stuckHTTP1 writes the request on a raw connection and reads nothing.
type stuckHTTP1 struct{ conn net.Conn }

func newStuckHTTP1(t *testing.T, addr string) stuckClient {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if _, err := io.WriteString(conn, "GET /big HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	return &stuckHTTP1{conn}
}

// stuckIdle is how long done waits for more of the body before giving up.
// It bounds the wait for each piece, not for the whole body: under the race
// detector on a CI runner's few CPUs, the rest of a 200MB download takes
// longer than any fixed time a fast machine needs.
const stuckIdle = 5 * time.Second

func (s *stuckHTTP1) done(t *testing.T) (int64, bool) {
	var n int64
	buf := make([]byte, 256<<10)
	for {
		_ = s.conn.SetReadDeadline(time.Now().Add(stuckIdle))
		m, err := s.conn.Read(buf)
		n += int64(m)
		if err != nil {
			// Closed by fit: EOF or a reset, with the body short.
			return n, (err == io.EOF || !errors.Is(err, net.ErrClosed) && !isTimeout(err)) && n < stuckSize
		}
	}
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// stuckH2C asks over h2c and leaves the body unread.
type stuckH2C struct{ resp *stdhttp.Response }

func newStuckH2C(t *testing.T, addr string) stuckClient {
	t.Helper()
	c := client(true)
	c.Timeout = 0
	resp, err := c.Get("http://" + addr + "/big")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return &stuckH2C{resp}
}

func (s *stuckH2C) done(t *testing.T) (int64, bool) {
	var read atomic.Int64
	ch := make(chan error, 1)
	go func() {
		_, err := io.Copy(countWriter{&read}, s.resp.Body)
		ch <- err
	}()
	for last := int64(-1); ; {
		select {
		case err := <-ch:
			n := read.Load()
			return n, err != nil && n < stuckSize
		case <-time.After(stuckIdle):
		}
		if n := read.Load(); n == last {
			s.resp.Body.Close()
			<-ch
			return n, false
		} else {
			last = n
		}
	}
}

// countWriter counts what is written to it, and keeps none of it.
type countWriter struct{ n *atomic.Int64 }

func (w countWriter) Write(p []byte) (int, error) {
	w.n.Add(int64(len(p)))
	return len(p), nil
}

// stuckH3 asks over HTTP/3 with fib's client and holds its body callback
// until done, so that the client stops taking the body.
type stuckH3 struct {
	release chan struct{}
	mu      sync.Mutex
	n       int64
	err     error
	ended   chan struct{}
}

func newStuckH3(t *testing.T, addr string, cert *certtest.Cert) stuckClient {
	t.Helper()
	engine, err := fib.New(fib.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	runDone := make(chan error, 1)
	go func() { runDone <- engine.Run() }()
	cfg := http3.DefaultClientConfig()
	cfg.TLSConfig = &tls.Config{RootCAs: certtest.Pool(cert), ServerName: "fit.example"}
	cfg.StreamResponseBody = true
	c := http3.NewClient(engine, cfg)
	s := &stuckH3{release: make(chan struct{}), ended: make(chan struct{})}
	t.Cleanup(func() {
		select {
		case <-s.release:
		default:
			close(s.release)
		}
		c.Close()
		engine.Stop()
		<-runDone
		_ = engine.Close()
	})
	req, _ := stdhttp.NewRequest("GET", "https://"+addr+"/big", nil)
	var once sync.Once
	c.Do(req, func(resp *fibhttp.ClientResponse, err error) {
		if err != nil {
			s.mu.Lock()
			s.err = err
			s.mu.Unlock()
			once.Do(func() { close(s.ended) })
			return
		}
		resp.OnBody(func(data []byte, fin bool, err error) {
			<-s.release // the client is not reading
			s.mu.Lock()
			s.n += int64(len(data))
			if err != nil {
				s.err = err
			}
			s.mu.Unlock()
			if fin || err != nil {
				once.Do(func() { close(s.ended) })
			}
		})
	})
	return s
}

func (s *stuckH3) done(t *testing.T) (int64, bool) {
	close(s.release)
	for last := int64(-1); ; {
		select {
		case <-s.ended:
		case <-time.After(stuckIdle):
			s.mu.Lock()
			n := s.n
			s.mu.Unlock()
			if n == last {
				return n, false
			}
			last = n
			continue
		}
		break
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n, s.err != nil && s.n < stuckSize
}

// others makes n requests for /small, one after another, over HTTP/1.1, and
// reports the slowest.
func others(t *testing.T, addr string, n int) time.Duration {
	t.Helper()
	var slowest time.Duration
	for i := range n {
		c := client(false)
		c.Timeout = 10 * time.Second
		start := time.Now()
		resp, err := c.Get("http://" + addr + "/small")
		if err != nil {
			t.Fatalf("request %d of the others: %v after %v", i, err, time.Since(start))
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || string(body) != "ok" {
			t.Fatalf("request %d of the others: %d %q", i, resp.StatusCode, body)
		}
		slowest = max(slowest, time.Since(start))
	}
	return slowest
}

func startStuck(t *testing.T, proto, tcpAddr, h3Addr string, cert *certtest.Cert) stuckClient {
	switch proto {
	case "h2c":
		return newStuckH2C(t, tcpAddr)
	case "h3":
		return newStuckH3(t, h3Addr, cert)
	default:
		return newStuckHTTP1(t, tcpAddr)
	}
}

func checkStuck(t *testing.T, stuck []stuckClient) {
	t.Helper()
	for i, s := range stuck {
		if n, cut := s.done(t); !cut {
			t.Errorf("stuck client %d: %d bytes, and its response was not cut short", i, n)
		}
	}
}

// TestStuckClientOnSharedConnection is the review's first case: an h2c
// upstream with max_conns 1, whose one connection carries every request,
// and one client that stops reading a download.
func TestStuckClientOnSharedConnection(t *testing.T) {
	for _, proto := range []string{"http/1.1", "h2c", "h3"} {
		t.Run(proto, func(t *testing.T) {
			tcpAddr, h3Addr, cert := stallFit(t, stallBackend(t, "h2c"), `, "max_conns": 1`)
			began := time.Now()
			stuck := []stuckClient{startStuck(t, proto, tcpAddr, h3Addr, cert)}
			time.Sleep(300 * time.Millisecond) // the download is under way, and stuck
			// The stuck download holds back its own stream, not the
			// connection: the others go at once.
			slowest := others(t, tcpAddr, 10)
			if slowest > time.Second {
				t.Errorf("the slowest of the others took %v", slowest)
			}
			time.Sleep(time.Until(began.Add(stallTimeout + time.Second))) // past the stuck client's stall timeout
			checkStuck(t, stuck)
		})
	}
}

// TestStuckClientWithoutStallTimeout checks that nothing relies on the stall
// timeout to keep a stuck download to itself: with it at an hour, a client
// that stops reading a download from an h2c upstream with max_conns 1 holds
// back its own stream and nothing else, and what fit holds for it stays
// bounded, rather than the 200MB download piling up for the client.
func TestStuckClientWithoutStallTimeout(t *testing.T) {
	for _, proto := range []string{"http/1.1", "h2c", "h3"} {
		t.Run(proto, func(t *testing.T) {
			tcpAddr, h3Addr, cert := stallFitWith(t, stallBackend(t, "h2c"), `, "max_conns": 1`, time.Hour)
			goruntime.GC()
			var before goruntime.MemStats
			goruntime.ReadMemStats(&before)
			stuck := startStuck(t, proto, tcpAddr, h3Addr, cert)
			time.Sleep(time.Second) // the download is under way, and stuck
			if slowest := others(t, tcpAddr, 10); slowest > time.Second {
				t.Errorf("the slowest of the others took %v", slowest)
			}
			goruntime.GC()
			var during goruntime.MemStats
			goruntime.ReadMemStats(&during)
			if grew := int64(during.HeapInuse) - int64(before.HeapInuse); grew > 32<<20 {
				t.Errorf("the heap grew by %d MB with the download stuck", grew>>20)
			}
			// Not cut off: once the client reads, the rest comes.
			if n, cut := stuck.done(t); cut || n < stuckSize {
				t.Errorf("the stuck client read %d bytes, cut %v", n, cut)
			}
		})
	}
}

// TestStuckClientsPastMaxConns is the review's second case: an HTTP/1.1
// upstream with max_conns 4, and eight clients that stop reading, so that
// the others wait for a connection the stuck ones hold.
func TestStuckClientsPastMaxConns(t *testing.T) {
	for _, proto := range []string{"http/1.1", "h2c", "h3"} {
		t.Run(proto, func(t *testing.T) {
			tcpAddr, h3Addr, cert := stallFit(t, stallBackend(t, "http/1.1"), `, "max_conns": 4`)
			var stuck []stuckClient
			for range 8 {
				stuck = append(stuck, startStuck(t, proto, tcpAddr, h3Addr, cert))
			}
			time.Sleep(300 * time.Millisecond)
			// Four stuck downloads hold the connections and four wait for them;
			// each pair is let go one stall timeout after it stalls.
			slowest := others(t, tcpAddr, 10)
			if slowest > 3*stallTimeout+2*time.Second {
				t.Errorf("the slowest of the others took %v", slowest)
			}
			// The four that waited began their downloads only when the first
			// four were let go; give them their own stall timeout before
			// they read, or a client released early reads it all.
			time.Sleep(stallTimeout + 500*time.Millisecond)
			checkStuck(t, stuck)
		})
	}
}

// TestSlowReaderNotCutOff checks that a client that reads slowly but keeps
// reading, and an upstream that pauses for longer than the stall timeout,
// do not lose their response: the stall timeout bounds each wait, not the
// response.
func TestSlowReaderNotCutOff(t *testing.T) {
	backend := serveBackend(t, "http/1.1", stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(3<<20))
		chunk := make([]byte, 1<<20)
		for i := range 3 {
			if i == 1 {
				// A pause longer than the stall timeout, with nothing to send.
				time.Sleep(2 * stallTimeout)
			}
			if _, err := w.Write(chunk); err != nil {
				return
			}
			w.(stdhttp.Flusher).Flush()
		}
	}))
	tcpAddr, _, _ := stallFit(t, backend, "")
	for _, h2c := range []bool{false, true} {
		c := client(h2c)
		c.Timeout = 30 * time.Second
		resp, err := c.Get("http://" + tcpAddr + "/big")
		if err != nil {
			t.Fatal(err)
		}
		// 3MB at about 1MB a second, in pieces, each wait well under the
		// stall timeout.
		var n int64
		buf := make([]byte, 64<<10)
		for {
			k, err := resp.Body.Read(buf)
			n += int64(k)
			if err != nil {
				if !errors.Is(err, io.EOF) {
					t.Fatalf("h2c=%v: after %d bytes: %v", h2c, n, err)
				}
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		resp.Body.Close()
		if n != 3<<20 {
			t.Fatalf("h2c=%v: %d bytes", h2c, n)
		}
	}
}
