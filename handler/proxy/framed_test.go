package proxy

import (
	"bufio"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/lesismal/fit/middleware/headers"
)

// rawHeadBackend is a backend that answers every request with reply, or with
// "ok" when it is "", and sends the head of each request, as it arrived, on
// the channel it returns.
func rawHeadBackend(t *testing.T, reply string) (string, chan string) {
	t.Helper()
	if reply == "" {
		reply = "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	heads := make(chan string, 16)
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
					var head strings.Builder
					length := 0
					for {
						line, err := br.ReadString('\n')
						if err != nil {
							return
						}
						head.WriteString(line)
						if name, value, ok := strings.Cut(line, ":"); ok && strings.EqualFold(name, "Content-Length") {
							length, _ = strconv.Atoi(strings.TrimSpace(value))
						}
						if line == "\r\n" {
							break
						}
					}
					_, _ = io.CopyN(io.Discard, br, int64(length))
					heads <- head.String()
					_, _ = io.WriteString(conn, reply)
				}
			}()
		}
	}()
	return ln.Addr().String(), heads
}

// proxyRaw sends request, as it is, to the fit at base and returns the head
// the backend saw of it, and the response's.
func proxyRaw(t *testing.T, base string, heads chan string, request string) (string, string) {
	t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(base, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(conn, request); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	var reply strings.Builder
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("reply %q: %v", reply.String(), err)
		}
		reply.WriteString(line)
		if line == "\r\n" {
			break
		}
	}
	select {
	case head := <-heads:
		return head, reply.String()
	case <-time.After(5 * time.Second):
		t.Fatal("the backend saw no request")
		return "", ""
	}
}

// TestFramedRequestHeader checks the header a proxy with no middleware passes
// on as its lines arrived (outgoingFramed): in the client's order and case,
// without the hop-by-hop fields, those Connection names, Content-Length and
// the client's forwarding fields, which are replaced with the proxy's own.
func TestFramedRequestHeader(t *testing.T) {
	addr, heads := rawHeadBackend(t, "")
	base := startFit(t, config1(`{"upstream": "app"}`), addr)
	head, _ := proxyRaw(t, base, heads, "POST /p?q=1 HTTP/1.1\r\nHost: app.example\r\nzz-first: 1\r\n"+
		"connection: x-hop, keep-alive\r\nx-hop: 1\r\nkeep-alive: 5\r\nTE: trailers\r\nproxy-authorization: x\r\n"+
		"x-forwarded-for: 203.0.113.9\r\nForwarded: for=203.0.113.9\r\nX-Forwarded-Host: evil\r\nx-multi: 1\r\n"+
		"Aa-Last: 2\r\nx-multi: 2\r\ncontent-length: 3\r\n\r\nabc")
	want := "POST /p?q=1 HTTP/1.1\r\nHost: app.example\r\nContent-Length: 3\r\nzz-first: 1\r\nx-multi: 1\r\n" +
		"Aa-Last: 2\r\nx-multi: 2\r\nX-Forwarded-For: 127.0.0.1\r\nX-Forwarded-Proto: http\r\n" +
		"X-Forwarded-Host: app.example\r\nForwarded: for=127.0.0.1;host=app.example;proto=http\r\n\r\n"
	if head != want {
		t.Fatalf("the upstream saw\n%q\nwant\n%q", head, want)
	}
}

// TestFramedResponseHeader checks the header an HTTP/1 client's response is
// written with from the upstream's lines (responseBlock): in the upstream's
// order and case, with its Date, without the hop-by-hop fields and those
// Connection names, and framed afresh.
func TestFramedResponseHeader(t *testing.T) {
	addr, heads := rawHeadBackend(t, "HTTP/1.1 200 OK\r\nzz: 1\r\nDate: Mon, 02 Jan 2006 15:04:05 GMT\r\n"+
		"Connection: x-hop, keep-alive\r\nx-hop: 1\r\nKeep-Alive: timeout=5\r\nContent-Length: 2\r\nAa: 2\r\n\r\nok")
	base := startFit(t, config1(`{"upstream": "app"}`), addr)
	_, reply := proxyRaw(t, base, heads, "GET / HTTP/1.1\r\nHost: app.example\r\n\r\n")
	want := "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nzz: 1\r\nDate: Mon, 02 Jan 2006 15:04:05 GMT\r\nAa: 2\r\n\r\n"
	if reply != want {
		t.Fatalf("the client got\n%q\nwant\n%q", reply, want)
	}
}

// TestFramedRequestHeaderMiddleware checks that a proxy a middleware wraps
// builds its request from the header map, which the middleware may have
// changed, rather than from the lines that arrived.
func TestFramedRequestHeaderMiddleware(t *testing.T) {
	addr, heads := rawHeadBackend(t, "")
	doc := strings.Replace(config1(`{"upstream": "app"}`), `"routes": [{`,
		`"routes": [{"middleware": [{"module": "headers", "params": {"request": {"set": {"X-Set": "by-middleware"}}}}], `, 1)
	base := startFit(t, doc, addr)
	head, _ := proxyRaw(t, base, heads, "GET / HTTP/1.1\r\nHost: app.example\r\nZz: 1\r\nConnection: close, x-gone\r\nX-Gone: 1\r\n\r\n")
	if !strings.Contains(head, "X-Set: by-middleware\r\n") {
		t.Fatalf("the middleware's field did not reach the upstream:\n%s", head)
	}
	if strings.Contains(strings.ToLower(head), "x-gone") {
		t.Fatalf("a field the client's Connection named reached the upstream:\n%s", head)
	}
}

// TestFramedFieldNames checks what outgoingFramed and responseBlock do with
// each field name they look for, in any case, and with names of the same
// lengths they keep.
func TestFramedFieldNames(t *testing.T) {
	request := map[string]int{
		"te": fieldDrop, "HOST": fieldDrop, "expect": fieldReadAgain, "UPGRADE": fieldDrop, "trailer": fieldReadAgain,
		"forwarded": fieldDrop, "connection": fieldDrop, "KEEP-ALIVE": fieldDrop, "content-length": fieldDrop,
		"x-forwarded-for": fieldDrop, "proxy-connection": fieldDrop, "X-FORWARDED-HOST": fieldDrop,
		"transfer-encoding": fieldDrop, "x-forwarded-proto": fieldDrop, "proxy-authenticate": fieldDrop,
		"proxy-authorization": fieldDrop,
		"ab":                  fieldKeep, "abcd": fieldKeep, "abcdef": fieldKeep, "abcdefg": fieldKeep, "abcdefghi": fieldKeep,
		"abcdefghij": fieldKeep, "abcdefghijklmn": fieldKeep, "abcdefghijklmno": fieldKeep,
		"abcdefghijklmnop": fieldKeep, "abcdefghijklmnopq": fieldKeep, "abcdefghijklmnopqr": fieldKeep,
		"abcdefghijklmnopqrs": fieldKeep, "x": fieldKeep,
	}
	for name, want := range request {
		if got := framedRequestField(name); got != want {
			t.Errorf("framedRequestField(%q) = %d, want %d", name, got, want)
		}
	}
	for _, name := range []string{"te", "TRAILER", "upgrade", "connection", "keep-alive", "content-length",
		"proxy-connection", "TRANSFER-ENCODING", "proxy-authenticate", "proxy-authorization"} {
		if !isHopByHopFold(name) {
			t.Errorf("isHopByHopFold(%q) = false", name)
		}
	}
	for _, name := range []string{"ab", "abcdefg", "abcdefghij", "abcdefghijklmn", "abcdefghijklmnop",
		"abcdefghijklmnopq", "abcdefghijklmnopqr", "abcdefghijklmnopqrs", "x"} {
		if isHopByHopFold(name) {
			t.Errorf("isHopByHopFold(%q) = true", name)
		}
	}
}

// TestFramedRequestReadAgain checks that a request with Expect or Trailer,
// whose fields outgoing reads again, goes on from the header map, in
// canonical case, rather than as its lines arrived.
func TestFramedRequestReadAgain(t *testing.T) {
	addr, heads := rawHeadBackend(t, "")
	base := startFit(t, config1(`{"upstream": "app"}`), addr)
	head, _ := proxyRaw(t, base, heads, "POST / HTTP/1.1\r\nHost: app.example\r\nzz-lower: 1\r\n"+
		"Trailer: x-sum\r\nContent-Length: 3\r\n\r\nabc")
	if !strings.Contains(head, "Zz-Lower: 1\r\n") {
		t.Fatalf("a request with Trailer went on as its lines:\n%s", head)
	}
}

// TestFramedHostHeader checks that a framed request goes on with the
// proxy's host_header, and its body.
func TestFramedHostHeader(t *testing.T) {
	addr, heads := rawHeadBackend(t, "")
	base := startFit(t, config1(`{"upstream": "app", "host_header": "internal.svc"}`), addr)
	head, _ := proxyRaw(t, base, heads, "PUT /x HTTP/1.1\r\nHost: app.example\r\nzz: 1\r\nContent-Length: 2\r\n\r\nhi")
	if !strings.HasPrefix(head, "PUT /x HTTP/1.1\r\nHost: internal.svc\r\nContent-Length: 2\r\nzz: 1\r\n") {
		t.Fatalf("the upstream saw\n%s", head)
	}
}

// TestFramedResponseConnectionFields checks a response whose Connection
// fields name fields of their own, in more fields than responseBlock keeps
// the values of: every field named goes, in any case.
func TestFramedResponseConnectionFields(t *testing.T) {
	addr, heads := rawHeadBackend(t, "HTTP/1.1 200 OK\r\nConnection: x-a\r\nConnection: X-B\r\nConnection: x-c, close\r\n"+
		"x-a: 1\r\nx-b: 2\r\nX-C: 3\r\nkept: 4\r\nContent-Length: 2\r\n\r\nok")
	base := startFit(t, config1(`{"upstream": "app"}`), addr)
	_, reply := proxyRaw(t, base, heads, "GET / HTTP/1.1\r\nHost: app.example\r\n\r\n")
	lower := strings.ToLower(reply)
	for _, gone := range []string{"\nconnection:", "\nx-a:", "\nx-b:", "\nx-c:"} {
		if strings.Contains(lower, gone) {
			t.Errorf("the client got %q, which it should not", strings.TrimSpace(gone))
		}
	}
	if !strings.Contains(reply, "\r\nkept: 4\r\n") {
		t.Errorf("the client got\n%q\nwithout kept", reply)
	}
}
