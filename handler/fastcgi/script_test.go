package fastcgi

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/fibtest"
	"github.com/lesismal/fit/listener"
	"github.com/lesismal/fit/module"
	"github.com/lesismal/fit/runtime"
	"github.com/lesismal/fit/upstream"
)

// fakeApp is a scripted FastCGI application: it answers each request as its
// script, the SCRIPT_NAME fit sends, says, breaking the protocol where a
// test wants it broken.
type fakeApp struct {
	addr string
	// got hears of each /hang.php request as it arrives.
	got chan string
	// release lets a /hang.php or /die.php request go on.
	release chan struct{}
	// closed hears the script of a connection's last request once the
	// connection is closed, by either side.
	closed chan string
}

// fcgiReq is a request the fake application read.
type fcgiReq struct {
	conn      net.Conn
	r         *reader
	params    map[string]string
	stdin     []byte
	paramsRaw []byte
	paramsEnd bool
	stdinEnd  bool
}

// read reads records until fn says it has what it wants.
func (q *fcgiReq) read(enough func() bool) error {
	buf := make([]byte, 64<<10)
	for !enough() {
		n, err := q.conn.Read(buf)
		if err != nil {
			return err
		}
		if err := q.r.feed(buf[:n], func(rec record) error {
			switch rec.t {
			case typeParams:
				if len(rec.content) == 0 {
					q.paramsEnd = true
				}
				q.paramsRaw = append(q.paramsRaw, rec.content...)
			case typeStdin:
				if len(rec.content) == 0 {
					q.stdinEnd = true
				}
				q.stdin = append(q.stdin, rec.content...)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// rest reads the request's body to its end.
func (q *fcgiReq) rest() error { return q.read(func() bool { return q.stdinEnd }) }

func decodeParams(b []byte) map[string]string {
	m := map[string]string{}
	readLen := func() int {
		if b[0]&0x80 == 0 {
			n := int(b[0])
			b = b[1:]
			return n
		}
		n := int(binary.BigEndian.Uint32(b) &^ (1 << 31))
		b = b[4:]
		return n
	}
	for len(b) > 0 {
		nl := readLen()
		vl := readLen()
		m[string(b[:nl])] = string(b[nl : nl+vl])
		b = b[nl+vl:]
	}
	return m
}

func out(s string) []byte { return appendRecord(nil, typeStdout, []byte(s)) }

func end() []byte { return appendRecord(nil, typeEndRequest, make([]byte, 8)) }

// waitClosed reads conn until it is closed.
func waitClosed(conn net.Conn) chan struct{} {
	dead := make(chan struct{})
	go func() {
		buf := make([]byte, 64<<10)
		for {
			if _, err := conn.Read(buf); err != nil {
				close(dead)
				return
			}
		}
	}()
	return dead
}

func newFakeApp(t *testing.T) *fakeApp {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	a := &fakeApp{addr: ln.Addr().String(), got: make(chan string, 16), release: make(chan struct{}, 16), closed: make(chan string, 64)}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go a.serveConn(conn)
		}
	}()
	return a
}

func (a *fakeApp) serveConn(conn net.Conn) {
	var r reader
	last := ""
	defer func() {
		_ = conn.Close()
		select {
		case a.closed <- last:
		default:
		}
	}()
	for {
		q := &fcgiReq{conn: conn, r: &r}
		if err := q.read(func() bool { return q.paramsEnd }); err != nil {
			return
		}
		q.params = decodeParams(q.paramsRaw)
		last = q.params["SCRIPT_NAME"]
		if !a.answer(q, last) {
			return
		}
	}
}

// answer answers q as script says, and reports whether the connection is
// kept.
func (a *fakeApp) answer(q *fcgiReq, script string) bool {
	conn := q.conn
	write := func(bs ...[]byte) bool {
		_, err := conn.Write(bytes.Join(bs, nil))
		return err == nil
	}
	switch script {
	case "/lf.php":
		_ = q.rest()
		return write(out("Content-Type: text/plain\n\nhello"), end())
	case "/split.php":
		_ = q.rest()
		return write(out("X-A: 1\r\nConnection: close\r\nContent-Length: 4\r\n"), out("\r\nbody"), end())
	case "/bighead.php":
		_ = q.rest()
		write(out(strings.Repeat("X-Long: aaaaaaaaaaaaaaaa\r\n", 200)))
		<-waitClosed(conn)
		return false
	case "/nocolon.php":
		_ = q.rest()
		write(out("garbage\r\n\r\n"))
		<-waitClosed(conn)
		return false
	case "/badstatus.php":
		_ = q.rest()
		write(out("Status: abc\r\n\r\n"))
		<-waitClosed(conn)
		return false
	case "/bye.php":
		// Answers, then reads the next request on the connection and
		// closes it unanswered, as an application that went away while
		// the connection was idle.
		_ = q.rest()
		write(out("\r\n\r\nbye"), end())
		next := &fcgiReq{conn: conn, r: q.r}
		if next.read(func() bool { return next.paramsEnd }) == nil {
			a.got <- "dropped"
		}
		return false
	case "/location.php":
		_ = q.rest()
		return write(out("Location: /x\r\n\r\n"), end())
	case "/empty.php":
		_ = q.rest()
		return write(end())
	case "/headbad.php":
		_ = q.rest()
		write(out("junk"), end())
		<-waitClosed(conn)
		return false
	case "/stderr.php":
		_ = q.rest()
		var b []byte
		for i := range 300 {
			b = appendRecord(b, typeStderr, []byte("line "+strconv.Itoa(i)+"\n"))
		}
		return write(b, out("Content-Type: text/plain\r\n\r\nok"), end())
	case "/wrongid.php":
		_ = q.rest()
		b := out("X: y\r\n\r\n")
		b[3] = 2
		write(b)
		<-waitClosed(conn)
		return false
	case "/shortend.php":
		_ = q.rest()
		write(out("X: y\r\n\r\nbody"), appendRecord(nil, typeEndRequest, []byte{0, 0, 0, 0}))
		<-waitClosed(conn)
		return false
	case "/badversion.php":
		_ = q.rest()
		write([]byte{2, 6, 0, 1, 0, 0, 0, 0})
		<-waitClosed(conn)
		return false
	case "/cl.php":
		_ = q.rest()
		write(out("Content-Length: 100\r\n\r\npartial"))
		return false
	case "/idlejunk.php":
		_ = q.rest()
		if !write(out("\r\n\r\nok"), end()) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
		// A record while the connection is idle.
		write(out("late"))
		<-waitClosed(conn)
		return false
	case "/closeafter.php":
		_ = q.rest()
		write(out("\r\n\r\nok"), end())
		time.Sleep(100 * time.Millisecond)
		return false
	case "/early.php":
		// Answers before the body has all come.
		write(out("\r\n\r\nearly"), end())
		<-waitClosed(conn)
		return false
	case "/slowread.php":
		time.Sleep(300 * time.Millisecond)
		if err := q.rest(); err != nil {
			return false
		}
		return write(out("\r\n\r\n"+strconv.Itoa(len(q.stdin))), end())
	case "/stream.php":
		_ = q.rest()
		if !write(out("Content-Type: application/octet-stream\r\n\r\n")) {
			return false
		}
		chunk := out(strings.Repeat("x", 60000))
		for range 300 { // 18 MB
			if !write(chunk) {
				return false
			}
		}
		write(end())
		return false
	case "/hang.php", "/die.php", "/hold.php":
		if script != "/hold.php" {
			_ = q.rest()
		}
		a.got <- script
		dead := waitClosed(conn)
		select {
		case <-a.release:
		case <-dead:
			return false
		case <-time.After(10 * time.Second):
		}
		if script == "/hang.php" {
			write(out("\r\n\r\nhung"), end())
		}
		return false
	default:
		_ = q.rest()
		return write(out("Status: 404\r\n\r\n"), end())
	}
}

// serveTop runs fit with the fastcgi handler of params on every route, top
// being more of the config's top level, as its limits.
func serveTop(t *testing.T, top, params string) string {
	t.Helper()
	c, err := config.Load([]byte(`{"listeners": [{"name": "web", "address": "127.0.0.1:0", "protocols": ["http/1.1", "h2c"]}],` + top + `
  "routes": [{"handler": {"module": "fastcgi", "params": ` + params + `}}]}`))
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
	return "http://" + s.Addrs()[0].Addr.String()
}

func waitFor[T any](t *testing.T, ch chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatalf("no %s", what)
	}
	var zero T
	return zero
}

// waitClosedScript waits until a connection whose last request ran script
// is closed.
func (a *fakeApp) waitClosedScript(t *testing.T, script string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case s := <-a.closed:
			if s == script {
				return
			}
		case <-deadline:
			t.Fatalf("the connection of %s stayed open", script)
		}
	}
}

// TestResponses checks how fit reads responses the application writes,
// well and badly formed.
func TestResponses(t *testing.T) {
	a := newFakeApp(t)
	base := serveTop(t, "", `{"address": "`+a.addr+`", "root": "/srv", "max_header_bytes": 2048}`)
	for proto, c := range clients() {
		for _, tc := range []struct {
			script string
			status int
			body   string
		}{
			{"/lf.php", 200, "hello"},
			{"/split.php", 200, "body"},
			{"/location.php", 302, ""},
			{"/empty.php", 200, ""},
			{"/stderr.php", 200, "ok"},
			{"/bighead.php", 502, ""},
			{"/nocolon.php", 502, ""},
			{"/badstatus.php", 502, ""},
			{"/headbad.php", 502, ""},
			{"/wrongid.php", 502, ""},
			{"/badversion.php", 502, ""},
		} {
			req, _ := stdhttp.NewRequest("GET", base+tc.script, nil)
			resp, body := get(t, c, req)
			if resp.StatusCode != tc.status || tc.body != "" && body != tc.body {
				t.Errorf("%s %s: %d %q", proto, tc.script, resp.StatusCode, body)
			}
			if tc.script == "/split.php" && (resp.Header.Get("X-A") != "1" || resp.Header.Get("Connection") == "close") {
				t.Errorf("%s %s: %v", proto, tc.script, resp.Header)
			}
		}
		// The body has begun when the end record breaks the protocol, and
		// when the application cuts a response of a declared length: the
		// client is told it broke off.
		for _, script := range []string{"/shortend.php", "/cl.php"} {
			resp, err := c.Get(base + script)
			if err != nil {
				t.Fatal(err)
			}
			_, err = io.ReadAll(resp.Body)
			resp.Body.Close()
			if err == nil && resp.Trailer.Get("Fit-Error") == "" && resp.StatusCode != 502 {
				t.Errorf("%s %s: a broken response read whole", proto, script)
			}
		}
	}
}

// TestIdleConnections checks connections kept idle: a record on one while
// idle closes it, as does the application, its idle timeout, and more idle
// connections than are kept.
func TestIdleConnections(t *testing.T) {
	a := newFakeApp(t)
	base := serveTop(t, "", `{"address": "`+a.addr+`", "root": "/srv", "max_idle_conns": 1, "idle_timeout": "300ms"}`)
	c := clients()["http/1.1"]
	for _, script := range []string{"/idlejunk.php", "/closeafter.php"} {
		req, _ := stdhttp.NewRequest("GET", base+script, nil)
		if resp, body := get(t, c, req); resp.StatusCode != 200 || body != "ok" {
			t.Fatalf("%s: %d %q", script, resp.StatusCode, body)
		}
		a.waitClosedScript(t, script)
	}
	// The idle timeout.
	req, _ := stdhttp.NewRequest("GET", base+"/lf.php", nil)
	if resp, _ := get(t, c, req); resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	a.waitClosedScript(t, "/lf.php")
	// Two at once, one kept idle after, the other closed.
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for range 2 {
		wg.Go(func() {
			req, _ := stdhttp.NewRequest("GET", base+"/hang.php", nil)
			resp, _ := get(t, c, req)
			codes <- resp.StatusCode
		})
	}
	waitFor(t, a.got, "first request")
	waitFor(t, a.got, "second request")
	a.release <- struct{}{}
	a.release <- struct{}{}
	wg.Wait()
	for range 2 {
		if code := <-codes; code != 200 {
			t.Errorf("%d", code)
		}
	}
}

// TestQueued checks requests that wait for the only connection: one whose
// client goes while it waits, and one that gets a new connection once the
// application closes the busy one.
func TestQueued(t *testing.T) {
	a := newFakeApp(t)
	base := serveTop(t, "", `{"address": "`+a.addr+`", "root": "/srv", "max_conns": 1}`)
	c := clients()["h2c"]

	busy := make(chan int, 1)
	go func() {
		req, _ := stdhttp.NewRequest("GET", base+"/hang.php", nil)
		resp, _ := get(t, c, req)
		busy <- resp.StatusCode
	}()
	waitFor(t, a.got, "the busy request")
	ctx, cancel := context.WithCancel(context.Background())
	waited := make(chan error, 1)
	go func() {
		req, _ := stdhttp.NewRequestWithContext(ctx, "GET", base+"/lf.php", nil)
		resp, err := c.Do(req)
		if err == nil {
			resp.Body.Close()
		}
		waited <- err
	}()
	time.Sleep(300 * time.Millisecond) // for it to queue
	cancel()
	if err := waitFor(t, waited, "the cancelled request"); err == nil {
		t.Error("a cancelled request answered")
	}
	a.release <- struct{}{}
	if code := waitFor(t, busy, "the busy response"); code != 200 {
		t.Errorf("busy: %d", code)
	}

	// A pool of its own: the application closes /hang.php's connection
	// after answering, and fit may not have heard by the time the next
	// request takes it from the idle set.
	a = newFakeApp(t)
	base = serveTop(t, "", `{"address": "`+a.addr+`", "root": "/srv", "max_conns": 1}`)
	go func() {
		req, _ := stdhttp.NewRequest("GET", base+"/die.php", nil)
		resp, _ := get(t, c, req)
		busy <- resp.StatusCode
	}()
	waitFor(t, a.got, "the dying request")
	queued := make(chan int, 1)
	go func() {
		req, _ := stdhttp.NewRequest("GET", base+"/lf.php", nil)
		resp, _ := get(t, c, req)
		queued <- resp.StatusCode
	}()
	time.Sleep(300 * time.Millisecond) // for it to queue
	a.release <- struct{}{}
	if code := waitFor(t, busy, "the dying response"); code != 502 {
		t.Errorf("a connection closed under its request: %d", code)
	}
	if code := waitFor(t, queued, "the queued response"); code != 200 {
		t.Errorf("queued behind a closed connection: %d", code)
	}
}

// TestClientsGo checks clients that go while the application has not
// answered, in the middle of a body, and that stop reading a response.
func TestClientsGo(t *testing.T) {
	a := newFakeApp(t)
	base := serveTop(t, `"limits": {"write_stall_timeout": "500ms"},`, `{"address": "`+a.addr+`", "root": "/srv"}`)
	addr := strings.TrimPrefix(base, "http://")

	// Gone before the response's header.
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := stdhttp.NewRequestWithContext(ctx, "GET", base+"/hang.php", nil)
	done := make(chan struct{})
	go func() {
		if resp, err := clients()["h2c"].Do(req); err == nil {
			resp.Body.Close()
		}
		close(done)
	}()
	waitFor(t, a.got, "the request")
	cancel()
	<-done
	a.waitClosedScript(t, "/hang.php")

	// Gone in the middle of its body, once it streams.
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(conn, "POST /hold.php HTTP/1.1\r\nHost: x\r\nContent-Length: 8000000\r\n\r\n"+strings.Repeat("0", 100<<10)) // past the threshold a body streams from
	waitFor(t, a.got, "the request with a body")
	_ = conn.Close()
	a.waitClosedScript(t, "/hold.php")

	// Stops reading a long response.
	conn, err = net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = io.WriteString(conn, "GET /stream.php HTTP/1.1\r\nHost: x\r\n\r\n")
	a.waitClosedScript(t, "/stream.php")
}

// TestBodies checks a body the application reads slowly, held back while
// it does, and one it answers before reading.
func TestBodies(t *testing.T) {
	a := newFakeApp(t)
	base := serveTop(t, "", `{"address": "`+a.addr+`", "root": "/srv"}`)
	for proto, c := range clients() {
		upload := bytes.Repeat([]byte("u"), 8<<20)
		req, _ := stdhttp.NewRequest("POST", base+"/slowread.php", bytes.NewReader(upload))
		if resp, body := get(t, c, req); resp.StatusCode != 200 || body != strconv.Itoa(len(upload)) {
			t.Errorf("%s: a slow reader: %d %q", proto, resp.StatusCode, body)
		}
		pr, pw := io.Pipe()
		req, _ = stdhttp.NewRequest("POST", base+"/early.php", pr)
		req.ContentLength = 1 << 20
		go func() {
			chunk := make([]byte, 64<<10)
			for range 16 {
				if _, err := pw.Write(chunk); err != nil {
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			_ = pw.Close()
		}()
		resp, err := c.Do(req)
		if err != nil {
			_ = pw.CloseWithError(err)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(body) != "early" {
			t.Errorf("%s: answered early: %d %q", proto, resp.StatusCode, body)
		}
		_ = pr.Close()
	}
}

// TestOverallTimeout checks an exchange that takes longer than timeout:
// 504, and its connection closed.
func TestOverallTimeout(t *testing.T) {
	a := newFakeApp(t)
	base := serveTop(t, "", `{"address": "`+a.addr+`", "root": "/srv", "timeout": "300ms"}`)
	req, _ := stdhttp.NewRequest("GET", base+"/hang.php", nil)
	if resp, _ := get(t, clients()["http/1.1"], req); resp.StatusCode != 504 {
		t.Errorf("%d", resp.StatusCode)
	}
	a.waitClosedScript(t, "/hang.php")
}

// TestNoApplication checks addresses nothing listens on: 502.
func TestNoApplication(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "none.sock")
	base := serveTop(t, "", `{"address": "unix/`+sock+`", "root": "/srv"}`)
	req, _ := stdhttp.NewRequest("GET", base+"/x.php", nil)
	if resp, _ := get(t, clients()["http/1.1"], req); resp.StatusCode != 502 {
		t.Errorf("no socket: %d", resp.StatusCode)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	base = serveTop(t, "", `{"address": "`+addr+`", "root": "/srv"}`)
	req, _ = stdhttp.NewRequest("GET", base+"/x.php", nil)
	if resp, _ := get(t, clients()["http/1.1"], req); resp.StatusCode != 502 {
		t.Errorf("a closed port: %d", resp.StatusCode)
	}
}

func loadHandler(t *testing.T, params string, env *module.Env) (*FastCGI, error) {
	t.Helper()
	v, err := config.ParseValue([]byte(`{"module": "fastcgi", "params": `+params+`}`), "handler")
	if err != nil {
		t.Fatal(err)
	}
	var ref config.ModuleRef
	if err := v.Decode(&ref); err != nil {
		t.Fatal(err)
	}
	m, err := module.Load(module.Handlers, ref, env)
	if err != nil {
		return nil, err
	}
	return m.(*FastCGI), nil
}

func TestProvisionErrors(t *testing.T) {
	for _, params := range []string{
		`{"address": "nowhere", "root": "/srv"}`,
		`{"address": "127.0.0.1:9000", "root": "srv"}`,
		`{"address": "127.0.0.1:9000", "root": "/srv", "try_files": ["index.php"]}`,
	} {
		if _, err := loadHandler(t, params, nil); err == nil {
			t.Errorf("%s: no error", params)
		}
	}
	h, err := loadHandler(t, `{"address": "127.0.0.1:9000", "root": "/srv"}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Checked only, with no engine: no pool to clean up.
	h.Cleanup()
	h.Drain()
}

// TestDrainAndCleanup checks the pool closed under requests: busy ones cut,
// waiting ones and later ones answered 503, and a connection given back
// once it is closed is closed.
func TestDrainAndCleanup(t *testing.T) {
	a := newFakeApp(t)
	engine, stop, err := upstream.StartEngine()
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	params := `{"address": "` + a.addr + `", "root": "/srv", "max_conns": 1}`
	serve := func(h *FastCGI, r *stdhttp.Request) chan *fibtest.Recorder {
		ch := make(chan *fibtest.Recorder, 1)
		go func() { ch <- fibtest.ServeRequest(h, r) }()
		return ch
	}

	h, err := loadHandler(t, params, &module.Env{Engine: engine})
	if err != nil {
		t.Fatal(err)
	}
	busy := serve(h, httptest.NewRequest("GET", "/hang.php", nil))
	waitFor(t, a.got, "the busy request")
	queued := serve(h, httptest.NewRequest("GET", "/lf.php", nil))
	time.Sleep(300 * time.Millisecond) // for it to queue
	h.Drain()
	if rec := waitFor(t, queued, "the queued response"); rec.Response.StatusCode != 503 {
		t.Errorf("queued: %d", rec.Response.StatusCode)
	}
	if rec := waitFor(t, busy, "the busy response"); rec.Response.StatusCode != 502 {
		t.Errorf("busy: %d", rec.Response.StatusCode)
	}
	if rec := fibtest.ServeRequest(h, httptest.NewRequest("GET", "/lf.php", nil)); rec.Response.StatusCode != 503 {
		t.Errorf("after drain: %d", rec.Response.StatusCode)
	}

	h, err = loadHandler(t, params, &module.Env{Engine: engine})
	if err != nil {
		t.Fatal(err)
	}
	busy = serve(h, httptest.NewRequest("GET", "/hang.php", nil))
	waitFor(t, a.got, "the busy request")
	h.Cleanup()
	a.release <- struct{}{}
	if rec := waitFor(t, busy, "the busy response"); rec.Response.StatusCode != 200 {
		t.Errorf("busy across cleanup: %d", rec.Response.StatusCode)
	}
}

// TestParamsTLS checks the parameters of a request over TLS, with no
// RequestURI of its own.
func TestParamsTLS(t *testing.T) {
	h := &FastCGI{Root: "/srv", Env: map[string]string{"A": "b"}}
	r := httptest.NewRequest("POST", "https://example.com/x.php/p?q=1", strings.NewReader("abc"))
	r.TLS = &tls.ConnectionState{}
	r.RequestURI = ""
	x := &exchange{h: h, c: fibhttp.NewStreamContext(nil, r, &fibtest.Recorder{}), script: "/x.php", pathInfo: "/p"}
	m := decodeParams(x.params())
	for k, want := range map[string]string{"HTTPS": "on", "REQUEST_SCHEME": "https", "SERVER_PORT": "443", "REQUEST_URI": "/x.php/p?q=1", "CONTENT_LENGTH": "3", "A": "b", "PATH_TRANSLATED": "/srv/p"} {
		if m[k] != want {
			t.Errorf("%s: %q, want %q", k, m[k], want)
		}
	}
}

func TestConnUnconnected(t *testing.T) {
	c := &conn{}
	c.OnPriorityData(nil, nil)
	if err := c.send([]byte("x")); err == nil {
		t.Error("sent on no connection")
	}
	if statusOf(errors.New("x")) != 502 || statusOf(fmt.Errorf("w: %w", errTimeout)) != 504 {
		t.Error("statusOf")
	}
}

// TestStaleIdle checks a request that takes an idle connection the
// application closes unanswered: sent again on a new connection when it has
// no body and a method safe to repeat, answered 502 otherwise.
func TestStaleIdle(t *testing.T) {
	a := newFakeApp(t)
	engine, stop, err := upstream.StartEngine()
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	h, err := loadHandler(t, `{"address": "`+a.addr+`", "root": "/srv"}`, &module.Env{Engine: engine})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Cleanup()
	for _, tc := range []struct {
		method, body string
		code         int
	}{
		{"GET", "", 200},
		{"HEAD", "", 200},
		{"POST", "", 502},
		{"POST", "abc", 502},
	} {
		if rec := fibtest.ServeRequest(h, httptest.NewRequest("GET", "/bye.php", nil)); rec.Response.StatusCode != 200 {
			t.Fatalf("bye: %d", rec.Response.StatusCode)
		}
		// The response ends before the connection is given back.
		deadline := time.Now().Add(10 * time.Second)
		for {
			h.pool.mu.Lock()
			idle := len(h.pool.idleSet)
			h.pool.mu.Unlock()
			if idle == 1 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the connection not idle")
			}
			time.Sleep(time.Millisecond)
		}
		var body io.Reader
		if tc.body != "" {
			body = strings.NewReader(tc.body)
		}
		rec := fibtest.ServeRequest(h, httptest.NewRequest(tc.method, "/lf.php", body))
		if got := waitFor(t, a.got, "the dropped request"); got != "dropped" {
			t.Fatalf("%s %q: %q", tc.method, tc.body, got)
		}
		if rec.Response.StatusCode != tc.code {
			t.Errorf("%s %q: %d", tc.method, tc.body, rec.Response.StatusCode)
		}
	}
}

func TestReplayable(t *testing.T) {
	for _, tc := range []struct {
		method string
		length int64
		want   bool
	}{
		{"GET", 0, true}, {"OPTIONS", 0, true}, {"TRACE", 0, true}, {"HEAD", 5, true},
		{"GET", 5, false}, {"GET", -1, false}, {"POST", 0, false}, {"PUT", 0, false}, {"DELETE", 0, false},
	} {
		r := httptest.NewRequest(tc.method, "/", nil)
		r.ContentLength = tc.length
		if got := replayable(r); got != tc.want {
			t.Errorf("%s %d: %v", tc.method, tc.length, got)
		}
	}
}
