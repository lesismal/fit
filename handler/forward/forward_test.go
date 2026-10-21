package forward_test

import (
	"bufio"
	"io"
	"net"
	stdhttp "net/http"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	_ "github.com/lesismal/fit/handler/forward"
	"github.com/lesismal/fit/listener"
	"github.com/lesismal/fit/runtime"
)

// serve starts a listener of HTTP/1.1 and h2c whose CONNECT requests go to a
// forward_proxy with params, closed when the test ends, and returns its
// address.
func serve(t *testing.T, params string) string {
	t.Helper()
	c, err := config.Load([]byte(`{
  "listeners": [{"name": "web", "address": "127.0.0.1:0", "protocols": ["http/1.1", "h2c"]}],
  "routes": [{"match": {"method": ["CONNECT"]}, "handler": {"module": "forward_proxy", "params": ` + params + `}}]
}`))
	if err != nil {
		t.Fatal(err)
	}
	g, err := runtime.Compile(c, runtime.Env{})
	if err != nil {
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
	})
	return s.Addrs()[0].Addr.String()
}

// echo is an echo server.
func echo(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	return l.Addr().String()
}

// connect sends a CONNECT for target over HTTP/1.1, and returns the
// connection, with what follows the response's head, and its status.
func connect(t *testing.T, proxy, target string) (net.Conn, *bufio.Reader, int) {
	t.Helper()
	c, err := net.DialTimeout("tcp", proxy, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = c.Write([]byte("CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n\r\n"))
	r := bufio.NewReader(c)
	resp, err := stdhttp.ReadResponse(r, &stdhttp.Request{Method: stdhttp.MethodConnect})
	if err != nil {
		t.Fatalf("CONNECT %s: %v", target, err)
	}
	return c, r, resp.StatusCode
}

func roundTrip(t *testing.T, w io.Writer, r io.Reader, msg string) {
	t.Helper()
	if _, err := w.Write([]byte(msg)); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(r, got); err != nil || string(got) != msg {
		t.Fatalf("echoed %q, %v", got, err)
	}
}

// TestConnect tunnels CONNECTs to what the rules allow, by address and by a
// host name whose address decides, and refuses the rest.
func TestConnect(t *testing.T) {
	target := echo(t)
	_, port, _ := net.SplitHostPort(target)
	proxy := serve(t, `{"clients": ["127.0.0.0/8"], "allow": ["127.0.0.0/8"], "ports": [`+port+`]}`)
	for _, dest := range []string{target, "localhost:" + port} {
		c, r, status := connect(t, proxy, dest)
		if status != 200 {
			t.Fatalf("%s: %d", dest, status)
		}
		roundTrip(t, c, r, "through "+dest)
	}
	for _, dest := range []string{"127.0.0.1:1", "192.0.2.1:" + port, "nohost"} {
		want := 403
		if dest == "nohost" {
			want = 400
		}
		if _, _, status := connect(t, proxy, dest); status != want {
			t.Errorf("%s: %d, want %d", dest, status, want)
		}
	}

	// A host name whose address deny holds is refused once connected.
	proxy = serve(t, `{"clients": ["127.0.0.1"], "deny": ["127.0.0.0/8", "::1"]}`)
	for dest, want := range map[string]int{target: 403, "localhost:" + port: 502} {
		if _, _, status := connect(t, proxy, dest); status != want {
			t.Errorf("%s: %d, want %d", dest, status, want)
		}
	}
	proxy = serve(t, `{"clients": ["10.0.0.0/8"]}`)
	if _, _, status := connect(t, proxy, target); status != 403 {
		t.Errorf("a client not allowed: %d", status)
	}
}

// TestConnectH2C tunnels a CONNECT on an h2c stream.
func TestConnectH2C(t *testing.T) {
	target := echo(t)
	proxy := serve(t, `{"clients": ["127.0.0.1"]}`)
	tr := &stdhttp.Transport{Protocols: new(stdhttp.Protocols)}
	tr.Protocols.SetUnencryptedHTTP2(true)
	t.Cleanup(tr.CloseIdleConnections)
	pr, pw := io.Pipe()
	defer pw.Close()
	req, err := stdhttp.NewRequest(stdhttp.MethodConnect, "http://"+proxy, pr)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = target
	resp, err := (&stdhttp.Client{Transport: tr, Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.ProtoMajor != 2 {
		t.Fatalf("%s %d", resp.Proto, resp.StatusCode)
	}
	roundTrip(t, pw, resp.Body, strings.Repeat("h2c ", 1000))
}

// TestConnectIdleTimeout closes a tunnel that carries nothing for the idle
// timeout.
func TestConnectIdleTimeout(t *testing.T) {
	target := echo(t)
	proxy := serve(t, `{"clients": ["127.0.0.1"], "idle_timeout": "300ms"}`)
	c, r, status := connect(t, proxy, target)
	if status != 200 {
		t.Fatalf("%d", status)
	}
	for range 4 {
		time.Sleep(100 * time.Millisecond)
		roundTrip(t, c, r, "x")
	}
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if n, err := r.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("read %d bytes, %v; want the tunnel closed", n, err)
	}
}
