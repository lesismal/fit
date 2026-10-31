package proxy

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/fibtest"
	"github.com/lesismal/fit/upstream"
)

// noGraph is a config hold that holds nothing, as a module's is in tests.
type noGraph struct{}

func (noGraph) Retain()  {}
func (noGraph) Release() {}

// coverProxy is a proxy to the upstream at addr, as Provision makes one,
// without a config around it, so that a test can serve it through a stream
// of its own and look at its exchanges and relays.
func coverProxy(t *testing.T, addr string) *Proxy {
	t.Helper()
	engine, stop, err := upstream.StartEngine()
	if err != nil {
		t.Fatal(err)
	}
	pool, err := upstream.New(engine, &config.Upstream{Name: "app", Addresses: []string{addr}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		stop()
	})
	return &Proxy{pool: pool, graph: noGraph{}, stall: time.Second, tunnelIdle: DefaultTunnelIdleTimeout}
}

// waitFor waits up to 5s for cond.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("still waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// waitIdle waits for p's exchanges and relays to be over.
func waitIdle(t *testing.T, p *Proxy) {
	t.Helper()
	waitFor(t, "the proxy to be idle", func() bool {
		for i := range p.open {
			s := &p.open[i]
			s.mu.Lock()
			busy := s.head != nil
			s.mu.Unlock()
			if busy {
				return false
			}
		}
		return openRelay(p, false) == nil
	})
}

// openRelay returns one of p's relays, or nil; with switched, only one whose
// both sides have switched.
func openRelay(p *Proxy, switched bool) *relay {
	for i := range p.relays {
		s := &p.relays[i]
		s.mu.Lock()
		for r := range s.relays {
			r.mu.Lock()
			ok := !switched || r.client != nil && r.up != nil
			r.mu.Unlock()
			if ok {
				s.mu.Unlock()
				return r
			}
		}
		s.mu.Unlock()
	}
	return nil
}

// serveCancelled serves r with h, through s, on a request its client has
// reset before h runs, as a stream whose peer went at once is.
func serveCancelled(t *testing.T, h fibhttp.Handler, r *stdhttp.Request, s fibhttp.Stream) {
	t.Helper()
	sr := &fibhttp.StreamRequest{}
	sr.Request = *r
	c := sr.Context(nil, s, nil)
	fibhttp.Serve(fibhttp.HandlerFunc(func(c *fibhttp.Context) {
		sr.Cancel(errors.New("stream reset"))
		waitFor(t, "the cancellation", func() bool { return c.Err() != nil })
		h.ServeHTTP(c)
	}), c)
}

// pacedStream is a client stream that streams responses, always says it has
// more queued than its limit, and takes write deadlines as deadline says.
type pacedStream struct {
	fibtest.Recorder
	deadline func(time.Time) error
	writable bool // what NotifyWritable reports

	mu     sync.Mutex
	status int
}

func (s *pacedStream) BeginResponse(_ *stdhttp.Request, status int, _ stdhttp.Header) error {
	s.mu.Lock()
	s.status = status
	s.mu.Unlock()
	return nil
}
func (s *pacedStream) WriteBody([]byte, bool) error           { return nil }
func (s *pacedStream) EndResponse(stdhttp.Header, bool) error { return nil }
func (s *pacedStream) WriteBodyNoWait([]byte) (bool, error)   { return true, nil }
func (s *pacedStream) NotifyWritable(func(error)) bool        { return s.writable }
func (s *pacedStream) SetReadDeadline(time.Time) error        { return nil }
func (s *pacedStream) SetWriteDeadline(t time.Time) error     { return s.deadline(t) }
func (s *pacedStream) begun() int                             { s.mu.Lock(); defer s.mu.Unlock(); return s.status }

// downloadBackend sends a 1MB body, which streams, 16KB at a time.
func downloadBackend(t *testing.T) string {
	return serveBackend(t, "http/1.1", stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		w.Header().Set("Content-Length", "1048576")
		chunk := make([]byte, 16<<10)
		for range 64 {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			w.(stdhttp.Flusher).Flush()
		}
	}))
}

// TestHoldDeadlineRefused: a client stream that refuses the write deadline
// a held download sets, or lifts, loses the download, and the upstream's
// response is given up.
func TestHoldDeadlineRefused(t *testing.T) {
	refused := errors.New("deadline refused")
	for _, tt := range []struct {
		name     string
		deadline func(time.Time) error
	}{
		// hold sets it as the client falls behind.
		{"set", func(d time.Time) error {
			if !d.IsZero() {
				return refused
			}
			return nil
		}},
		// resume lifts it, the client having room already.
		{"lifted", func(d time.Time) error {
			if d.IsZero() {
				return refused
			}
			return nil
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := coverProxy(t, downloadBackend(t))
			s := &pacedStream{deadline: tt.deadline}
			fibhttp.Serve(p, fibhttp.NewStreamContext(nil, httptest.NewRequest(stdhttp.MethodGet, "/big", nil), s))
			waitIdle(t, p)
			if got := s.begun(); got != stdhttp.StatusOK {
				t.Errorf("began with %d", got)
			}
		})
	}
}

// TestUnreadableBody: a response that arrived whole but whose body cannot
// be read, as one of another transport's might be, is answered 502.
func TestUnreadableBody(t *testing.T) {
	p := coverProxy(t, "127.0.0.1:1")
	r := httptest.NewRequest(stdhttp.MethodGet, "/", nil)
	rec := fibtest.ServeRequest(fibhttp.HandlerFunc(func(c *fibhttp.Context) {
		x := &exchange{c: c, pool: p.pool, backend: p.pool.Pick(r), graph: p.graph, open: &p.open[0], start: time.Now()}
		x.open.add(x)
		c.Retain()
		body := io.NopCloser(iotest.ErrReader(errors.New("unreadable")))
		x.onResponse(&fibhttp.ClientResponse{Response: &stdhttp.Response{StatusCode: 200, Header: stdhttp.Header{}, Body: body}}, nil)
	}), r)
	if rec.Response.StatusCode != stdhttp.StatusBadGateway {
		t.Errorf("%d, want 502", rec.Response.StatusCode)
	}
	// One that reads is read whole.
	resp := &fibhttp.ClientResponse{Response: &stdhttp.Response{Body: io.NopCloser(strings.NewReader("whole"))}}
	if body, pooled, err := takeBody(resp); err != nil || pooled || string(body) != "whole" {
		t.Errorf("%q, %v", body, err)
	}
}

// TestSettleOnce: an exchange is settled with its backend once.
func TestSettleOnce(t *testing.T) {
	p := coverProxy(t, "127.0.0.1:1")
	x := &exchange{pool: p.pool, backend: p.pool.Pick(httptest.NewRequest(stdhttp.MethodGet, "/", nil)), open: &p.open[0]}
	x.open.add(x)
	if !x.settle() || x.settle() {
		t.Error("settled other than once")
	}
}

// TestClientGoneBeforeSend: a request whose client went before it was sent
// upstream is abandoned as it is sent, and answered with nothing.
func TestClientGoneBeforeSend(t *testing.T) {
	p := coverProxy(t, echoBackend(t, "http/1.1"))
	rec := &fibtest.Recorder{}
	serveCancelled(t, p, httptest.NewRequest(stdhttp.MethodGet, "/", nil), rec)
	waitIdle(t, p)
	if rec.Written {
		t.Errorf("answered %d", rec.Response.StatusCode)
	}
}

// TestBrokenBeforeBody: an upstream that sends the header of a response of
// no declared length, which is handed over from its header, and breaks off
// before any of its body is answered for as one that sent nothing, 502,
// since nothing reached the client yet.
func TestBrokenBeforeBody(t *testing.T) {
	addr := rawBackend(t, func(c net.Conn, _ *stdhttp.Request) {
		_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n")
		time.Sleep(50 * time.Millisecond)
	})
	p := coverProxy(t, addr)
	rec := fibtest.ServeRequest(p, httptest.NewRequest(stdhttp.MethodGet, "/", nil))
	if rec.Response.StatusCode != stdhttp.StatusBadGateway {
		t.Errorf("%d, want 502", rec.Response.StatusCode)
	}
	waitIdle(t, p)
}

// rawBackend is a backend that reads a request and hands the connection to
// serve, then closes it.
func rawBackend(t *testing.T, serve func(net.Conn, *stdhttp.Request)) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				req, err := stdhttp.ReadRequest(bufio.NewReader(c))
				if err != nil {
					return
				}
				serve(c, req)
			}()
		}
	}()
	return l.Addr().String()
}

// TestUploadAfterAnswer: an upstream that answers an upload it never read
// has its answer passed on, and the rest of the upload, held back while the
// upstream took nothing and arriving after it answered, is dropped.
func TestUploadAfterAnswer(t *testing.T) {
	addr := rawBackend(t, func(c net.Conn, _ *stdhttp.Request) {
		// Long enough for the upload to fill the sockets and be held.
		time.Sleep(300 * time.Millisecond)
		_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
		_ = c.SetReadDeadline(time.Now().Add(time.Second))
		_, _ = io.Copy(io.Discard, c)
	})
	base := startFit(t, config1(`{"upstream": "app"}`), addr)
	c, err := net.Dial("tcp", strings.TrimPrefix(base, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	go func() {
		chunk := fmt.Appendf(nil, "%x\r\n%s\r\n", 64<<10, bytes.Repeat([]byte("u"), 64<<10))
		if _, err := io.WriteString(c, "POST /up HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\n\r\n"); err != nil {
			return
		}
		for range 512 { // 32MB, more than the sockets between hold
			if _, err := c.Write(chunk); err != nil {
				return
			}
		}
	}()
	resp, err := stdhttp.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "ok" {
		t.Fatalf("%d %q", resp.StatusCode, body)
	}
}

// TestRefusedHeaderName: a request with a field whose name is not a token,
// which fib refuses itself, is refused here too, upgrade or not.
func TestRefusedHeaderName(t *testing.T) {
	for _, upgrade := range []bool{false, true} {
		r := httptest.NewRequest(stdhttp.MethodGet, "/", nil)
		r.Header["X A"] = []string{"1"}
		if upgrade {
			r.Header.Set("Connection", "Upgrade")
			r.Header.Set("Upgrade", "websocket")
		}
		rec := fibtest.ServeRequest(&Proxy{}, r)
		if rec.Response.StatusCode != stdhttp.StatusBadRequest {
			t.Errorf("upgrade %v: %d, want 400", upgrade, rec.Response.StatusCode)
		}
	}
	if validToken("") {
		t.Error("an empty name is a token")
	}
}

// TestForwardedNotIP: a client whose address is not an IP address, as one
// on a Unix socket, is not trusted, and what it says it forwarded for is
// dropped.
func TestForwardedNotIP(t *testing.T) {
	r := httptest.NewRequest(stdhttp.MethodGet, "http://example.com/", nil)
	r.RemoteAddr = "@"
	r.Header.Set("X-Forwarded-For", "203.0.113.9")
	x := &exchange{}
	if !(&Proxy{}).outgoing(fibhttp.NewStreamContext(nil, r, nil), x) {
		t.Fatal("refused")
	}
	h := x.base.Header
	if _, ok := h["X-Forwarded-For"]; ok {
		t.Errorf("X-Forwarded-For %q", h["X-Forwarded-For"])
	}
	if got := h.Get("Forwarded"); got != "for=unknown;host=example.com;proto=http" {
		t.Errorf("Forwarded %q", got)
	}
}

// TestUpgradeProtocols: what a request asks to switch to, and how a
// handshake that got no answer is answered.
func TestUpgradeProtocols(t *testing.T) {
	connect := httptest.NewRequest(stdhttp.MethodConnect, "/", nil)
	if p, ext := upgradeProtocol(connect); p != "" || ext {
		t.Errorf("CONNECT without :protocol: %q %v", p, ext)
	}
	empty := httptest.NewRequest(stdhttp.MethodGet, "/", nil)
	empty.Header.Set("Connection", "Upgrade")
	empty.Header["Upgrade"] = []string{" , websocket"}
	if p, _ := upgradeProtocol(empty); p != "" {
		t.Errorf("an empty Upgrade asks for %q", p)
	}
	for _, tt := range []struct {
		err  error
		want int
	}{
		{&net.OpError{Op: "dial", Err: timeoutError{}}, 504},
		{errDrained, 503},
		{upstream.ErrBusy, 503},
		{errors.New("refused"), 502},
	} {
		if got := upgradeStatus(tt.err); got != tt.want {
			t.Errorf("%v: %d, want %d", tt.err, got, tt.want)
		}
	}
	// Only a WebSocket can go upstream as an HTTP/1.1 handshake.
	r := httptest.NewRequest(stdhttp.MethodConnect, "/", nil)
	r.ProtoMajor, r.ProtoMinor = 2, 0
	r.Header[":protocol"] = []string{"connect-udp"}
	if rec := fibtest.ServeRequest(&Proxy{}, r); rec.Response.StatusCode != stdhttp.StatusNotImplemented {
		t.Errorf("connect-udp: %d, want 501", rec.Response.StatusCode)
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

// wsRequest is an HTTP/1.1 WebSocket handshake.
func wsRequest() *stdhttp.Request {
	r := httptest.NewRequest(stdhttp.MethodGet, "/ws", nil)
	r.Header.Set("Connection", "Upgrade")
	r.Header.Set("Upgrade", "websocket")
	r.Header.Set("Sec-WebSocket-Version", "13")
	r.Header.Set("Sec-WebSocket-Key", "MDEyMzQ1Njc4OWFiY2RlZg==")
	return r
}

// TestUpgradeNotSwitched: a client whose request cannot switch, after the
// upstream has, gets 502, and the upstream's tunnel, with what it sent with
// its 101, is closed.
func TestUpgradeNotSwitched(t *testing.T) {
	addr := rawBackend(t, func(c net.Conn, req *stdhttp.Request) {
		_, _ = io.WriteString(c, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n"+
			"Sec-WebSocket-Accept: "+wsAccept(req.Header.Get("Sec-WebSocket-Key"))+"\r\n\r\nhello")
		_, _ = io.Copy(io.Discard, c)
	})
	p := coverProxy(t, addr)
	// A stream recorded is not one that can switch.
	rec := fibtest.ServeRequest(p, wsRequest())
	if rec.Response.StatusCode != stdhttp.StatusBadGateway {
		t.Errorf("%d, want 502", rec.Response.StatusCode)
	}
	waitIdle(t, p)
}

// TestUpgradeClientGone: a handshake whose client went before it was sent
// is given up, and answered with nothing.
func TestUpgradeClientGone(t *testing.T) {
	p := coverProxy(t, newWSBackend(t).addr)
	rec := &fibtest.Recorder{}
	serveCancelled(t, p, wsRequest(), rec)
	waitIdle(t, p)
	if rec.Written {
		t.Errorf("answered %d", rec.Response.StatusCode)
	}
}

// switchingStream is a client stream that switches an extended CONNECT to a
// tunnel, after upgrading runs, if it is set.
type switchingStream struct {
	fibtest.Recorder
	upgrading func()
	feed      *fibhttp.TunnelFeed
}

func (s *switchingStream) Upgrade(*stdhttp.Request, string, stdhttp.Header) (*fibhttp.Tunnel, error) {
	if s.upgrading != nil {
		s.upgrading()
	}
	s.feed = fibhttp.NewTunnelFeed(nil, &feedWriter{s: s}, 0)
	return s.feed.Tunnel(), nil
}

// feedWriter is the client's side of a switchingStream's tunnel, which
// drops what is sent and ends the tunnel when closed.
type feedWriter struct {
	s    *switchingStream
	once sync.Once
}

func (w *feedWriter) SendParts(_, _ []byte) error { return nil }
func (w *feedWriter) SendOwned([]byte) error      { return nil }
func (w *feedWriter) CloseAfterSend()             { w.end() }
func (w *feedWriter) Close() error                { w.end(); return nil }
func (w *feedWriter) end()                        { w.once.Do(func() { go w.s.feed.End(nil) }) }

// extendedConnect is an HTTP/2 WebSocket's extended CONNECT.
func extendedConnect() *stdhttp.Request {
	r := httptest.NewRequest(stdhttp.MethodConnect, "/chat", nil)
	r.Proto, r.ProtoMajor, r.ProtoMinor = "HTTP/2.0", 2, 0
	r.Header[":protocol"] = []string{"websocket"}
	return r
}

// TestDrainClosesTunnels: a drain closes a tunnel still open, on both sides.
func TestDrainClosesTunnels(t *testing.T) {
	p := coverProxy(t, newWSBackend(t).addr)
	// Of one upstream, picked as a split picks it.
	p.split = []weighted{{pool: p.pool, upTo: 1}}
	s := &switchingStream{}
	fibhttp.Serve(p, fibhttp.NewStreamContext(nil, extendedConnect(), s))
	waitFor(t, "the tunnel", func() bool { return openRelay(p, true) != nil })
	p.Drain()
	waitIdle(t, p)
}

// TestClientGoneAsSwitching: a client that goes while its request switches
// has both tunnels closed once they are open.
func TestClientGoneAsSwitching(t *testing.T) {
	p := coverProxy(t, newWSBackend(t).addr)
	sr := &fibhttp.StreamRequest{}
	sr.Request = *extendedConnect()
	s := &switchingStream{upgrading: func() {
		sr.Cancel(errors.New("stream reset"))
		waitFor(t, "the relay to hear of it", func() bool {
			r := openRelay(p, false)
			return r != nil && r.causeErr() != nil
		})
	}}
	fibhttp.Serve(p, sr.Context(nil, s, nil))
	waitIdle(t, p)
}

// TestCallFailures: what a gRPC call the proxy answers itself is told.
func TestCallFailures(t *testing.T) {
	for _, tt := range []struct {
		status, code int
		message      string
	}{
		{413, grpcResourceExhausted, "fit: the request is too large"},
		{400, grpcInternal, "fit: the request was malformed"},
	} {
		code := grpcCode(tt.status)
		if code != tt.code || grpcMessage(code) != tt.message {
			t.Errorf("%d: %d %q", tt.status, code, grpcMessage(code))
		}
	}
	// A call cut short by its deadline after its response began.
	x := &exchange{c: fibhttp.NewStreamContext(nil, httptest.NewRequest(stdhttp.MethodPost, "/", nil), nil)}
	x.cancel(errRouteTimeout)
	x.grpcBroken()
	if got := x.c.Header().Get(stdhttp.TrailerPrefix + "Grpc-Status"); got != "4" {
		t.Errorf("grpc-status %q, want 4", got)
	}
}

// TestUploadStatuses: how a request body that failed on its way upstream is
// answered, and said.
func TestUploadStatuses(t *testing.T) {
	x := &exchange{}
	err := &uploadError{fibhttp.ErrBodyHeldTooLong}
	if got := x.statusOf(err); got != stdhttp.StatusGatewayTimeout {
		t.Errorf("held too long: %d, want 504", got)
	}
	if !strings.HasPrefix(err.Error(), "request body: ") {
		t.Errorf("%q", err.Error())
	}
}

// TestRetryBodyGone: a request whose body cannot be had again is not sent
// again.
func TestRetryBodyGone(t *testing.T) {
	x := &exchange{policy: newRetryPolicy(&Retry{})}
	x.base.Method = stdhttp.MethodGet
	x.base.Body = io.NopCloser(strings.NewReader("body"))
	x.base.GetBody = func() (io.ReadCloser, error) { return nil, errors.New("gone") }
	if x.retry(nil, &net.OpError{Op: "dial", Err: errors.New("refused")}) {
		t.Error("sent again")
	}
}
