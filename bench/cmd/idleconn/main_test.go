package main

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"testing"
	"time"
)

func mustParse(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func closeAll(conns []net.Conn) {
	for _, c := range conns {
		abort(c)
	}
}

func TestOpenHTTP1(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	defer srv.Close()
	u := mustParse(t, srv.URL+"/idle")
	conns, err := openAll("http1", u, 5, 2, 1)
	if err != nil || len(conns) != 5 {
		t.Fatalf("%d connections: %v", len(conns), err)
	}
	// The connections are idle and open: the server sends nothing more.
	_ = conns[0].SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	if _, err := conns[0].Read(make([]byte, 1)); err == nil {
		t.Error("read from an idle connection")
	}
	closeAll(conns)

	// No concurrency asked for still opens them.
	conns, err = openAll("http1", u, 2, 0, 1)
	if err != nil || len(conns) != 2 {
		t.Fatalf("%d connections: %v", len(conns), err)
	}
	closeAll(conns)
}

func TestOpenTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()
	conns, err := openAll("tls", mustParse(t, srv.URL+"/idle"), 3, 3, 1)
	if err != nil || len(conns) != 3 {
		t.Fatalf("%d connections: %v", len(conns), err)
	}
	closeAll(conns)

	// TLS to a server that does not speak it.
	plain := httptest.NewServer(http.NotFoundHandler())
	defer plain.Close()
	if c, err := open("tls", mustParse(t, plain.URL), nil); err == nil {
		c.Close()
		t.Error("TLS handshake with a plain server")
	}
}

func TestOpenWS(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(acceptWS))
	defer srv.Close()
	conns, err := openAll("ws", mustParse(t, srv.URL+"/ws"), 3, 2, 1)
	if err != nil || len(conns) != 3 {
		t.Fatalf("%d connections: %v", len(conns), err)
	}
	closeAll(conns)

	// A server that does not upgrade.
	plain := httptest.NewServer(http.NotFoundHandler())
	defer plain.Close()
	if _, err := open("ws", mustParse(t, plain.URL+"/ws"), nil); err == nil || !strings.Contains(err.Error(), "not 101") {
		t.Errorf("got %v, want an error for the status", err)
	}
}

func TestOpenFails(t *testing.T) {
	// A server closing the connection after its response.
	closing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Connection", "close")
	}))
	defer closing.Close()
	if _, err := open("http1", mustParse(t, closing.URL), nil); err == nil || !strings.Contains(err.Error(), "closes the connection") {
		t.Errorf("got %v", err)
	}

	// A server closing the connection without a response.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = bufio.NewReader(c).ReadString('\n')
			c.Close()
		}
	}()
	if _, err := open("http1", mustParse(t, "http://"+ln.Addr().String()+"/"), nil); err == nil {
		t.Error("no response read")
	}

	// Nothing listening: openAll stops at the first failure.
	gone, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := gone.Addr().String()
	gone.Close()
	conns, err := openAll("http1", mustParse(t, "http://"+addr+"/"), 10, 4, 1)
	if err == nil || len(conns) != 0 {
		t.Errorf("%d connections: %v", len(conns), err)
	}

	// Without a port, the scheme's default; whatever answers there, the
	// call returns.
	if c, err := open("tls", mustParse(t, "https://127.0.0.1/"), nil); err == nil {
		c.Close()
	}
}

func TestSources(t *testing.T) {
	if sourceAddr(5, 1) != nil || sourceAddr(5, 0) != nil {
		t.Error("a source address for one source")
	}
	for i, want := range []string{"127.0.0.1", "127.0.0.2", "127.0.0.3", "127.0.0.1"} {
		if got := sourceAddr(i, 3).IP.String(); got != want {
			t.Errorf("sourceAddr(%d, 3) = %s, want %s", i, got, want)
		}
	}
	if got := sourceAddr(299, 300).IP.String(); got != "127.0.1.44" {
		t.Errorf("sourceAddr(299, 300) = %s", got)
	}
	// Other systems answer only on 127.0.0.1.
	if runtime.GOOS != "linux" {
		t.Skip("several loopback addresses are for Linux")
	}
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	conns, err := openAll("http1", mustParse(t, srv.URL), 4, 2, 2)
	if err != nil || len(conns) != 4 {
		t.Fatalf("%d connections: %v", len(conns), err)
	}
	seen := map[string]bool{}
	for _, c := range conns {
		seen[c.LocalAddr().(*net.TCPAddr).IP.String()] = true
	}
	if !seen["127.0.0.1"] || !seen["127.0.0.2"] {
		t.Errorf("from %v", seen)
	}
	closeAll(conns)
}

func TestAcceptWS(t *testing.T) {
	// Not an upgrade.
	rec := httptest.NewRecorder()
	acceptWS(rec, httptest.NewRequest(http.MethodGet, "/ws", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status %d", rec.Code)
	}
	// An upgrade the writer cannot hijack.
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	rec = httptest.NewRecorder()
	acceptWS(rec, req)
	if rec.Body.Len() != 0 {
		t.Errorf("answered %q", rec.Body)
	}

	// The upgrade's answer, and the connection held until the peer closes.
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		acceptWS(w, r)
	}))
	defer srv.Close()
	c, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(c, "GET /ws HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols || resp.Header.Get("Sec-WebSocket-Accept") != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Errorf("%s %v", resp.Status, resp.Header)
	}
	select {
	case <-done:
		t.Fatal("the connection was not held")
	case <-time.After(50 * time.Millisecond):
	}
	c.Close()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the connection was held after the peer closed it")
	}
}
