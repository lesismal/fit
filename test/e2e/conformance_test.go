//go:build !windows

package e2e

import (
	"bufio"
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// The proxy conformance suite: what RFC 9110 and RFC 9112 ask of an
// intermediary, checked through the fit binary with requests written byte by
// byte, against a backend that records what reached it.

// recorder is a backend that answers each request by its path and records
// the requests that reached it.
type recorder struct {
	mu   sync.Mutex
	seen []string // "METHOD URI body"
	addr string
}

func (r *recorder) record(req *stdhttp.Request, body []byte) {
	r.mu.Lock()
	r.seen = append(r.seen, req.Method+" "+req.RequestURI+" "+string(body))
	r.mu.Unlock()
}

func (r *recorder) requests() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.seen...)
}

func startRecorder(t *testing.T) *recorder {
	t.Helper()
	rec := &recorder{}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	rec.addr = ln.Addr().String()
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go rec.serve(conn)
		}
	}()
	return rec
}

// serve answers requests on one connection: raw, so that the backend can
// send what a well-behaved server library would not.
func (r *recorder) serve(conn net.Conn) {
	defer conn.Close()
	br := bufio.NewReader(conn)
	for {
		req, err := stdhttp.ReadRequest(br)
		if err != nil {
			return
		}
		body, _ := io.ReadAll(req.Body)
		r.record(req, body)
		var out string
		switch req.URL.Path {
		case "/garbage":
			out = "this is not HTTP\r\n\r\n"
		case "/304":
			out = "HTTP/1.1 304 Not Modified\r\nEtag: \"v1\"\r\n\r\n"
		case "/204":
			out = "HTTP/1.1 204 No Content\r\nX-Kind: empty\r\n\r\n"
		case "/103":
			out = "HTTP/1.1 103 Early Hints\r\nLink: </a.css>; rel=preload\r\n\r\n" +
				"HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nafter"
		case "/close":
			out = "HTTP/1.1 200 OK\r\nConnection: close\r\nContent-Length: 6\r\n\r\nclosed"
		case "/big":
			chunk := strings.Repeat("b", 32<<10)
			out = "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n"
			for range 4 {
				out += fmt.Sprintf("%x\r\n%s\r\n", len(chunk), chunk)
			}
			out += "0\r\n\r\n"
		case "/fold":
			reply := "x-a=" + req.Header.Get("X-A")
			out = fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n%s", len(reply), reply)
		default:
			reply := "echo " + req.Method + " " + req.RequestURI + " " + string(body)
			out = fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n%s", len(reply), reply)
		}
		if _, err := io.WriteString(conn, out); err != nil || req.URL.Path == "/garbage" || req.URL.Path == "/close" {
			return
		}
	}
}

// exchange writes raw to fit and reads back what it answers until the
// connection is quiet for a moment or closes.
func exchange(t *testing.T, addr, raw string) string {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, raw); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	buf := make([]byte, 64<<10)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, err := conn.Read(buf)
		out.Write(buf[:n])
		if err != nil {
			return out.String()
		}
	}
}

// closes reports whether fit closes the connection after answering raw.
func closes(t *testing.T, addr, raw string) bool {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, raw); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, err := stdhttp.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	_, err = conn.Read(make([]byte, 1))
	return err == io.EOF
}

func statusLine(resp string) string {
	line, _, _ := strings.Cut(resp, "\r\n")
	return line
}

func TestProxyConformance(t *testing.T) {
	rec := startRecorder(t)
	_, addrs := startFit(t, `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "upstreams": [{"name": "app", "addresses": ["`+rec.addr+`"], "max_fails": 0}],
  "routes": [{"handler": {"module": "proxy", "params": {"upstream": "app"}}}]
}`)
	addr := addrs["web"]

	t.Run("smuggling with Content-Length and Transfer-Encoding", func(t *testing.T) {
		resp := exchange(t, addr, "POST /a HTTP/1.1\r\nHost: x\r\nContent-Length: 4\r\nTransfer-Encoding: chunked\r\n\r\n"+
			"0\r\n\r\nGET /smuggled HTTP/1.1\r\nHost: x\r\n\r\n")
		for _, r := range rec.requests() {
			if strings.Contains(r, "/smuggled") {
				t.Fatalf("a smuggled request reached the upstream: %q (fit answered %q)", r, statusLine(resp))
			}
		}
		if !strings.HasPrefix(resp, "HTTP/1.1 400") && !strings.HasPrefix(resp, "HTTP/1.1 200") {
			t.Errorf("answered %q", statusLine(resp))
		}
	})
	for _, tt := range []struct{ name, raw string }{
		{"conflicting Content-Length", "POST /a HTTP/1.1\r\nHost: x\r\nContent-Length: 3\r\nContent-Length: 4\r\n\r\nabcd"},
		{"space before the colon", "GET /a HTTP/1.1\r\nHost: x\r\nX-A : one\r\n\r\n"},
		{"no Host in HTTP/1.1", "GET /a HTTP/1.1\r\n\r\n"},
		{"two Hosts", "GET /a HTTP/1.1\r\nHost: x\r\nHost: y\r\n\r\n"},
		{"a bad chunk size", "POST /a HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\n\r\nzz\r\nabc\r\n0\r\n\r\n"},
		{"a NUL in a header", "GET /a HTTP/1.1\r\nHost: x\r\nX-A: a\x00b\r\n\r\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			before := len(rec.requests())
			resp := exchange(t, addr, tt.raw)
			if !strings.HasPrefix(resp, "HTTP/1.1 400") {
				t.Errorf("answered %q, want 400", statusLine(resp))
			}
			if after := rec.requests(); len(after) != before {
				t.Errorf("the upstream saw %q", after[before:])
			}
		})
	}
	t.Run("obs-fold", func(t *testing.T) {
		// RFC 9112 section 5.2: refused, or each fold replaced by a space.
		resp := exchange(t, addr, "GET /fold HTTP/1.1\r\nHost: x\r\nX-A: one\r\n two\r\n\r\n")
		if !strings.HasPrefix(resp, "HTTP/1.1 400") && !strings.Contains(resp, "x-a=one two") {
			t.Errorf("answered %q", resp)
		}
	})
	t.Run("a header past the limit", func(t *testing.T) {
		resp := exchange(t, addr, "GET /a HTTP/1.1\r\nHost: x\r\nX-Big: "+strings.Repeat("a", 2<<20)+"\r\n\r\n")
		if !strings.HasPrefix(resp, "HTTP/1.1 431") {
			t.Errorf("answered %q, want 431", statusLine(resp))
		}
	})
	t.Run("Expect: 100-continue", func(t *testing.T) {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		body := strings.Repeat("e", 100<<10) // past the stream threshold
		fmt.Fprintf(conn, "PUT /expect HTTP/1.1\r\nHost: x\r\nContent-Length: %d\r\nExpect: 100-continue\r\n\r\n", len(body))
		br := bufio.NewReader(conn)
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		line, err := br.ReadString('\n')
		if err != nil || !strings.HasPrefix(line, "HTTP/1.1 100") {
			t.Fatalf("before the body: %q %v", line, err)
		}
		for line != "\r\n" {
			if line, err = br.ReadString('\n'); err != nil {
				t.Fatal(err)
			}
		}
		_, _ = io.WriteString(conn, body)
		resp, err := stdhttp.ReadResponse(br, nil)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 || string(got) != "echo PUT /expect "+body {
			t.Errorf("after the body: %d, %d bytes", resp.StatusCode, len(got))
		}
	})
	t.Run("an HTTP/1.0 client", func(t *testing.T) {
		resp := exchange(t, addr, "GET /big HTTP/1.0\r\nHost: x\r\n\r\n")
		head, body, _ := strings.Cut(resp, "\r\n\r\n")
		if !strings.HasPrefix(head, "HTTP/1.") || strings.Contains(strings.ToLower(head), "transfer-encoding: chunked") ||
			len(body) != 128<<10 {
			t.Errorf("%q, %d bytes of body", head, len(body))
		}
	})
	t.Run("pipelined requests", func(t *testing.T) {
		resp := exchange(t, addr, "GET /one HTTP/1.1\r\nHost: x\r\n\r\nGET /two HTTP/1.1\r\nHost: x\r\n\r\nGET /three HTTP/1.1\r\nHost: x\r\n\r\n")
		i, j, k := strings.Index(resp, "echo GET /one"), strings.Index(resp, "echo GET /two"), strings.Index(resp, "echo GET /three")
		if i < 0 || j < i || k < j {
			t.Errorf("responses out of order or missing: %q", resp)
		}
	})
	t.Run("absolute-form target", func(t *testing.T) {
		resp := exchange(t, addr, "GET http://x/abs?q=1 HTTP/1.1\r\nHost: x\r\n\r\n")
		if !strings.Contains(resp, "echo GET /abs?q=1 ") {
			t.Errorf("the upstream saw %q", resp)
		}
	})
	t.Run("304 from the upstream", func(t *testing.T) {
		resp := exchange(t, addr, "GET /304 HTTP/1.1\r\nHost: x\r\n\r\nGET /after HTTP/1.1\r\nHost: x\r\n\r\n")
		if !strings.HasPrefix(resp, "HTTP/1.1 304") || !strings.Contains(resp, `Etag: "v1"`) || !strings.Contains(resp, "echo GET /after") {
			t.Errorf("%q", resp)
		}
	})
	t.Run("204 from the upstream", func(t *testing.T) {
		resp := exchange(t, addr, "GET /204 HTTP/1.1\r\nHost: x\r\n\r\nGET /after HTTP/1.1\r\nHost: x\r\n\r\n")
		if !strings.HasPrefix(resp, "HTTP/1.1 204") || !strings.Contains(resp, "X-Kind: empty") || !strings.Contains(resp, "echo GET /after") {
			t.Errorf("%q", resp)
		}
	})
	t.Run("1xx from the upstream", func(t *testing.T) {
		resp := exchange(t, addr, "GET /103 HTTP/1.1\r\nHost: x\r\n\r\n")
		if !strings.Contains(resp, "HTTP/1.1 200") || !strings.HasSuffix(resp, "after") {
			t.Errorf("%q", resp)
		}
	})
	t.Run("an upstream that does not speak HTTP", func(t *testing.T) {
		if resp := exchange(t, addr, "GET /garbage HTTP/1.1\r\nHost: x\r\n\r\n"); !strings.HasPrefix(resp, "HTTP/1.1 502") {
			t.Errorf("answered %q, want 502", statusLine(resp))
		}
	})
	t.Run("Connection: close from the client", func(t *testing.T) {
		if !closes(t, addr, "GET /a HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n") {
			t.Error("the connection stayed open")
		}
	})
	t.Run("Connection: close from the upstream is the upstream's", func(t *testing.T) {
		resp := exchange(t, addr, "GET /close HTTP/1.1\r\nHost: x\r\n\r\nGET /after HTTP/1.1\r\nHost: x\r\n\r\n")
		if !strings.Contains(resp, "closed") || !strings.Contains(resp, "echo GET /after") ||
			strings.Contains(strings.ToLower(resp), "connection: close") {
			t.Errorf("%q", resp)
		}
	})
}
